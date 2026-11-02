// services/runtime-adapter/src/claude-provider.ts
import type {
  ModelCapabilities,
  ModelProvider,
  ModelRequest,
  ModelResponse,
  ModelTool,
  ModelToolCall,
  ModelUsage,
} from "./types";
import type { ModelMessage } from "./types";

export interface ClaudeProviderConfig {
  baseUrl?: string; // e.g. https://api.anthropic.com/v1
  apiKey: string;
  defaultModel: string;
  timeoutMs?: number;
  version?: string;
}

const DEFAULT_ANTHROPIC_VERSION = "2026-10-21";

type AnthropicTextBlock = { type: "text"; text: string };
type AnthropicToolUseBlock = { type: "tool_use"; id: string; name: string; input: Record<string, unknown> };
type AnthropicToolResultBlock = { type: "tool_result"; tool_use_id: string; content: string };
type AnthropicContentBlock = AnthropicTextBlock | AnthropicToolUseBlock | AnthropicToolResultBlock;

interface AnthropicMessage {
  role: "user" | "assistant";
  content: string | AnthropicContentBlock[];
}

interface AnthropicResponse {
  content?: Array<
    | { type: "text"; text?: string }
    | { type: "tool_use"; id: string; name: string; input?: Record<string, unknown> }
  >;
  stop_reason?: string;
  usage?: { input_tokens?: number; output_tokens?: number };
}

/**
 * Claude provider — talks Anthropic's /v1/messages API directly.
 *
 * Not OpenAI wire-compatible (own endpoint, headers, system param,
 * tool_use/tool_result content blocks, stop_reason values), so it implements
 * ModelProvider itself and converts the internal message history explicitly.
 */
export class ClaudeProvider implements ModelProvider {
  readonly id = "claude";

  constructor(private cfg: ClaudeProviderConfig) {
    if (!cfg.apiKey) throw new Error("ANTHROPIC_API_KEY is required when MODEL_PROVIDER=claude");
  }

  private get baseUrl(): string {
    return (this.cfg.baseUrl ?? "https://api.anthropic.com/v1").replace(/\/+$/, "");
  }

  private get version(): string {
    return this.cfg.version ?? DEFAULT_ANTHROPIC_VERSION;
  }

  /**
   * API versions >= 2026-10-21 removed the `temperature`, `top_p` and `top_k`
   * request params from the Messages API — sending them returns a 400.
   */
  private supportsSamplingParams(): boolean {
    return this.version < "2026-10-21";
  }

  private headers(): Record<string, string> {
    return {
      "content-type": "application/json",
      "x-api-key": this.cfg.apiKey,
      "anthropic-version": this.version,
    };
  }

  async healthCheck(): Promise<{ ok: boolean; detail: string }> {
    try {
      const res = await fetch(`${this.baseUrl}/models`, {
        headers: this.headers(),
        signal: AbortSignal.timeout(5_000),
      });
      if (!res.ok) return { ok: false, detail: `HTTP ${res.status}` };
      return { ok: true, detail: "reachable" };
    } catch (e) {
      return { ok: false, detail: String(e) };
    }
  }

  async listModels(): Promise<string[]> {
    const res = await fetch(`${this.baseUrl}/models`, {
      headers: this.headers(),
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) throw new Error(`listModels: HTTP ${res.status}`);
    const json = (await res.json()) as { data?: Array<{ id?: string }> };
    return (json.data ?? []).map((m) => m.id ?? "").filter(Boolean);
  }

  async getCapabilities(_model: string): Promise<ModelCapabilities> {
    return {
      toolCalling: true,
      structuredOutput: true, // via tool use
      streaming: false,
      contextWindow: 200_000,
    };
  }

  async complete(req: ModelRequest): Promise<ModelResponse> {
    const model = req.model ?? this.cfg.defaultModel;
    if (!model) throw new Error("model is not set");
    const start = Date.now();

    const { system, messages } = this.buildMessages(req.messages);

    const body: Record<string, unknown> = {
      model,
      max_tokens: req.maxTokens ?? 4096,
      messages,
    };
    if (system) body.system = system;
    if (this.supportsSamplingParams() && req.temperature !== undefined) {
      body.temperature = req.temperature;
    }
    if (req.tools && req.tools.length > 0) {
      body.tools = req.tools.map((t: ModelTool) => ({
        name: t.name,
        description: t.description,
        input_schema: t.inputSchema,
      }));
    }
    // NOTE: `response_format` is intentionally not sent — Anthropic's current
    // Messages API (2026-10-21) rejects it with "Extra inputs are not permitted".
    // Structured output is instead expected from the model when asked to return JSON.
    body.stream = false;

    const res = await fetch(`${this.baseUrl}/messages`, {
      method: "POST",
      headers: this.headers(),
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(req.timeoutMs ?? this.cfg.timeoutMs ?? 120_000),
    });

    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`complete: HTTP ${res.status} — ${redact(text)}`);
    }

    const json = (await res.json()) as AnthropicResponse;
    const content = json.content ?? [];
    let textOut = content
      .filter((b) => b.type === "text")
      .map((b) => b.text ?? "")
      .join("");
    if (req.responseSchema) {
      // Models sometimes wrap JSON in markdown fences; structured-output callers want bare JSON.
      textOut = stripCodeFences(textOut).trim();
    }
    const toolCalls: ModelToolCall[] = content
      .filter((b) => b.type === "tool_use")
      .map((b) => ({ id: b.id, name: b.name, arguments: b.input ?? {} }));
    const finishReason = mapStopReason(json.stop_reason, toolCalls.length > 0);
    const usage: ModelUsage = {
      inputTokens: json.usage?.input_tokens,
      outputTokens: json.usage?.output_tokens,
      wallMs: Date.now() - start,
    };
    return { content: textOut, toolCalls, finishReason, usage };
  }

  private buildMessages(messages: ModelMessage[]): { system?: string; messages: AnthropicMessage[] } {
    let system: string | undefined;
    const out: AnthropicMessage[] = [];
    let pendingToolResults: Array<{ tool_use_id: string; content: string }> = [];

    const flushToolResults = () => {
      if (pendingToolResults.length === 0) return;
      out.push({
        role: "user",
        content: pendingToolResults.map((r) => ({ type: "tool_result" as const, ...r })),
      });
      pendingToolResults = [];
    };

    for (const m of messages) {
      if (m.role === "system") {
        system = m.content;
        continue;
      }
      if (m.role === "tool") {
        pendingToolResults.push({ tool_use_id: m.toolCallId ?? "", content: m.content });
        continue;
      }
      if (m.role === "assistant") {
        flushToolResults();
        const blocks: AnthropicContentBlock[] = [];
        if (m.content) blocks.push({ type: "text", text: m.content });
        for (const tc of m.toolCalls ?? []) {
          blocks.push({ type: "tool_use", id: tc.id, name: tc.name, input: tc.arguments ?? {} });
        }
        if (blocks.length === 0) blocks.push({ type: "text", text: m.content ?? "" });
        out.push({ role: "assistant", content: blocks });
        continue;
      }
      // user
      if (m.toolCallId) {
        pendingToolResults.push({ tool_use_id: m.toolCallId, content: m.content });
        continue;
      }
      flushToolResults();
      out.push({ role: "user", content: m.content });
    }
    flushToolResults();
    return { system, messages: out };
  }
}

function mapStopReason(reason: string | undefined, hasToolCalls: boolean): ModelResponse["finishReason"] {
  if (hasToolCalls) return "tool_calls";
  if (reason === "end" || reason === "end_turn") return "stop";
  if (reason === "stop_sequence") return "stop";
  if (reason === "max_tokens") return "length";
  return "error";
}

function stripCodeFences(s: string): string {
  const trimmed = s.trim();
  const fence = trimmed.match(/^```[a-zA-Z0-9]*\s*\n([\s\S]*?)\n?\s*```$/);
  if (fence) return fence[1].trim();
  if (trimmed.startsWith("```")) {
    const end = trimmed.indexOf("```", 3);
    if (end > 0) return trimmed.slice(3, end).trim();
  }
  return trimmed;
}

function redact(text: string): string {
  return text.replace(/sk-[A-Za-z0-9_\-]{20,}/g, "sk-***").slice(0, 300);
}
