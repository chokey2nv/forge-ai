// services/runtime-adapter/src/openai-compatible.ts
import type {
  ModelCapabilities, ModelProvider, ModelRequest, ModelResponse,
  ModelToolCall, ModelUsage,
} from "./types.js";

export interface CompatibleProviderConfig {
  id: string;
  baseUrl: string;           // ends with /v1, no trailing slash
  apiKey: string;            // "ollama" for local; real key for OpenAI
  defaultModel: string;
  timeoutMs: number;
  supportsStructuredOutput: boolean;
}

export abstract class OpenAICompatibleProvider implements ModelProvider {
  readonly id: string;

  constructor(protected cfg: CompatibleProviderConfig) {
    this.id = cfg.id;
  }

  async healthCheck(): Promise<{ ok: boolean; detail: string }> {
    try {
      const res = await fetch(`${this.cfg.baseUrl}/models`, {
        headers: { Authorization: `Bearer ${this.cfg.apiKey}` },
        signal: AbortSignal.timeout(5_000),
      });
      if (!res.ok) return { ok: false, detail: `HTTP ${res.status}` };
      return { ok: true, detail: "reachable" };
    } catch (e) {
      return { ok: false, detail: String(e) };
    }
  }

  async listModels(): Promise<string[]> {
    const res = await fetch(`${this.cfg.baseUrl}/models`, {
      headers: { Authorization: `Bearer ${this.cfg.apiKey}` },
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) throw new Error(`listModels: HTTP ${res.status}`);
    const body = (await res.json()) as {
      models?: Array<{ id?: string; name?: string }>;
      data?: Array<{ id?: string; name?: string }>;
    };
    // Ollama's /v1/models returns { models: [{ id }] }; OpenAI returns { data: [{ id }] }.
    const list = body.models ?? body.data ?? [];
    return list.map((m) => m.id ?? m.name ?? "").filter(Boolean);
  }

  async getCapabilities(_model: string): Promise<ModelCapabilities> {
    return {
      toolCalling: true,
      structuredOutput: this.cfg.supportsStructuredOutput,
      streaming: false,
      contextWindow: 32_768,
    };
  }

  async complete(req: ModelRequest): Promise<ModelResponse> {
    const model = req.model ?? this.cfg.defaultModel;
    if (!model) throw new Error("model is not set");

    const body: Record<string, unknown> = {
      model,
      messages: req.messages.map(toOpenAIMessage),
      temperature: req.temperature ?? 0.2,
      stream: false,
    };
    if (req.maxTokens) body.max_tokens = req.maxTokens;
    if (req.tools?.length) {
      body.tools = req.tools.map((t) => ({
        type: "function",
        function: { name: t.name, description: t.description, parameters: t.inputSchema },
      }));
    }
    if (req.responseSchema) {
      body.response_format = this.cfg.supportsStructuredOutput
        ? { type: "json_schema", json_schema: { name: "result", schema: req.responseSchema, strict: true } }
        : { type: "json_object" };
    }

    const start = Date.now();
    const res = await fetch(`${this.cfg.baseUrl}/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.cfg.apiKey}`,
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(req.timeoutMs ?? this.cfg.timeoutMs),
    });

    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`complete: HTTP ${res.status} — ${redact(text)}`);
    }

    const json = (await res.json()) as OpenAIResponse;
    const choice = json.choices?.[0];
    if (!choice) throw new Error("complete: empty choices");

    const toolCalls: ModelToolCall[] = (choice.message.tool_calls ?? []).map((tc) => ({
      id: tc.id,
      name: tc.function.name,
      arguments: parseArgs(tc.function.arguments),
    }));

    const usage: ModelUsage = {
      inputTokens: json.usage?.prompt_tokens,
      outputTokens: json.usage?.completion_tokens,
      wallMs: Date.now() - start,
    };

    return {
      content: choice.message.content ?? "",
      toolCalls,
      finishReason: mapFinish(choice.finish_reason, toolCalls.length > 0),
      usage,
    };
  }
}

function toOpenAIMessage(m: ModelRequest["messages"][number]) {
  if (m.role === "tool") {
    return { role: "tool", tool_call_id: m.toolCallId, content: m.content };
  }
  return { role: m.role, content: m.content };
}

function parseArgs(raw: unknown): Record<string, unknown> {
  if (typeof raw === "object" && raw !== null) return raw as Record<string, unknown>;
  if (typeof raw === "string") {
    try { return JSON.parse(raw) as Record<string, unknown>; }
    catch { return {}; }
  }
  return {};
}

function mapFinish(r: string | undefined, hasToolCalls: boolean): ModelResponse["finishReason"] {
  if (hasToolCalls) return "tool_calls";
  if (r === "stop") return "stop";
  if (r === "length") return "length";
  return "error";
}

function redact(text: string): string {
  return text.replace(/sk-[A-Za-z0-9_\-]{20,}/g, "sk-***").slice(0, 300);
}

interface OpenAIResponse {
  choices?: Array<{
    message: {
      content: string | null;
      tool_calls?: Array<{ id: string; function: { name: string; arguments: string } }>;
    };
    finish_reason?: string;
  }>;
  usage?: { prompt_tokens?: number; completion_tokens?: number };
}