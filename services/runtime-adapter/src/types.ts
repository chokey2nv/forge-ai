export type Role = "system" | "user" | "assistant" | "tool";

export interface ModelMessage {
  role: Role;
  content: string;
  toolCallId?: string;
  toolName?: string;
  /** Present on assistant messages that issued tool calls (OpenAI + Claude wire formats both require this). */
  toolCalls?: ModelToolCall[];
}
export type ToolSchema = ModelTool;
export interface ModelTool {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

export interface ModelRequest {
  model?: string;
  messages: ModelMessage[];
  tools?: ModelTool[];
  temperature?: number;
  maxTokens?: number;
  responseSchema?: Record<string, unknown>;
  timeoutMs?: number;
}

export interface ModelToolCall {
  id: string;
  name: string;
  arguments: Record<string, unknown>;
}

/** The provider refused the call for now; retryAfterMs is how long it asked to wait, if it said. */
export class RateLimitError extends Error {
  constructor(message: string, readonly retryAfterMs?: number) {
    super(message);
  }
}

export interface ModelUsage {
  /** Tokens at full price. Cached ones are counted apart. */
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  wallMs: number;
}

export interface ModelResponse {
  content: string;
  toolCalls: ModelToolCall[];
  finishReason: "stop" | "tool_calls" | "length" | "error";
  usage: ModelUsage;
}

export interface ModelCapabilities {
  toolCalling: boolean;
  structuredOutput: boolean;
  streaming: boolean;
  contextWindow: number;
}

export interface ModelProvider {
  readonly id: string;
  healthCheck(): Promise<{ ok: boolean; detail: string }>;
  listModels(): Promise<string[]>;
  getCapabilities(model: string): Promise<ModelCapabilities>;
  complete(req: ModelRequest): Promise<ModelResponse>;
}