// services/runtime-adapter/src/openai-provider.ts
import { OpenAICompatibleProvider } from "./openai-compatible.js";

export interface OpenAIProviderConfig {
  baseUrl?: string;
  apiKey: string;
  defaultModel: string;
  timeoutMs?: number;
}

export class OpenAIProvider extends OpenAICompatibleProvider {
  constructor(cfg: OpenAIProviderConfig) {
    if (!cfg.apiKey) throw new Error("OPENAI_API_KEY is required");
    super({
      id: "openai",
      baseUrl: normalize(cfg.baseUrl ?? "https://api.openai.com/v1"),
      apiKey: cfg.apiKey,
      defaultModel: cfg.defaultModel,
      timeoutMs: cfg.timeoutMs ?? 60_000,
      supportsStructuredOutput: true, // gpt-4o-mini and newer
    });
  }
}

function normalize(url: string): string {
  return url.replace(/\/+$/, "");
}