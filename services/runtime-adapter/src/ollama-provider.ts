import { OpenAICompatibleProvider } from "./openai-compatible";
import type {
  ModelCapabilities, ModelProvider, ModelRequest, ModelResponse,
  ModelToolCall, ModelUsage,
} from "./types";

function normalize(url: string): string {
  return url.replace(/\/+$/, "");
}
interface OllamaProviderConfig {
  baseUrl: string;     // e.g. http://127.0.0.1:11434
  defaultModel: string;
  timeoutMs: number;
}

// Ollama ignores Authorization but the OpenAI-shaped client expects one.
const PLACEHOLDER_KEY = "ollama";

export class OllamaProvider extends OpenAICompatibleProvider implements ModelProvider {
  readonly id = "ollama";

  constructor(cfg: OllamaProviderConfig) {
    super({
      id: "ollama",
      baseUrl: normalize(cfg.baseUrl ?? "http://127.0.0.1:11434/v1"),
      apiKey: "ollama", // Ollama ignores this
      defaultModel: cfg.defaultModel,
      timeoutMs: cfg.timeoutMs ?? 120_000,
      supportsStructuredOutput: true,
    });
  }

  async healthCheck(): Promise<{ ok: boolean; detail: string }> {
    try {
      const res = await fetch(`${this.cfg.baseUrl}/api/tags`, {
        signal: AbortSignal.timeout(5_000),
      });
      if (!res.ok) return { ok: false, detail: `HTTP ${res.status}` };
      return { ok: true, detail: "reachable" };
    } catch (e) {
      return { ok: false, detail: String(e) };
    }
  }

  async listModels(): Promise<string[]> {
    const res = await fetch(`${this.cfg.baseUrl}/api/tags`, {
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) throw new Error(`listModels: HTTP ${res.status}`);
    const body = (await res.json()) as { models?: Array<{ name: string }> };
    return (body.models ?? []).map((m) => m.name);
  }

  async getCapabilities(model: string): Promise<ModelCapabilities> {
    // Ollama doesn't expose a capability endpoint; we assume tool calling and
    // structured output are supported and let the smoke test / runtime
    // detect otherwise. Context window defaulted conservatively.
    return {
      toolCalling: true,
      structuredOutput: true,
      streaming: false,
      contextWindow: 32_768,
    };
  }

  async complete(req: ModelRequest): Promise<ModelResponse> {
    const model = req.model ?? this.cfg.defaultModel;
    if (!model) throw new Error("MODEL_NAME is not set");

    const start = Date.now();
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
      body.response_format = {
        type: "json_schema",
        json_schema: { name: "result", schema: req.responseSchema },
      };
    }

    const res = await fetch(`${this.cfg.baseUrl}/v1/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${PLACEHOLDER_KEY}`,
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(req.timeoutMs ?? this.cfg.timeoutMs),
    });

    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`complete: HTTP ${res.status} — ${text.slice(0, 200)}`);
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

interface OpenAIResponse {
  choices?: Array<{
    message: {
      content: string | null;
      tool_calls?: Array<{
        id: string;
        function: { name: string; arguments: string };
      }>;
    };
    finish_reason?: string;
  }>;
  usage?: { prompt_tokens?: number; completion_tokens?: number };
}