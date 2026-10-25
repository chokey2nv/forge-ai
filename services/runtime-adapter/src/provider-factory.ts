// services/runtime-adapter/src/provider-factory.ts
import process from "node:process";
import { OllamaProvider } from "./ollama-provider";
import { OpenAIProvider } from "./openai-provider";
import type { ModelProvider } from "./types";

export type ProviderId = "ollama" | "openai";

export interface ProviderConfig {
  provider: ProviderId;
  modelName: string;
  timeoutMs: number;
  ollamaBaseUrl?: string;
  openaiApiKey?: string;
  openaiBaseUrl?: string;
}

export function createProvider(cfg: ProviderConfig): ModelProvider {
  if (!cfg.modelName) throw new Error("MODEL_NAME is required");
  switch (cfg.provider) {
    case "ollama":
      return new OllamaProvider({
        baseUrl: cfg.ollamaBaseUrl ?? "http://127.0.0.1:11434/v1",
        defaultModel: cfg.modelName,
        timeoutMs: cfg.timeoutMs,
      });
    case "openai":
      if (!cfg.openaiApiKey) {
        throw new Error("OPENAI_API_KEY is required when MODEL_PROVIDER=openai");
      }
      return new OpenAIProvider({
        baseUrl: cfg.openaiBaseUrl,
        apiKey: cfg.openaiApiKey,
        defaultModel: cfg.modelName,
        timeoutMs: cfg.timeoutMs,
      });
    default:
      throw new Error(`unknown MODEL_PROVIDER: ${cfg.provider}`);
  }
}

/**
 * Enforce the project's allowed provider set.
 * Phase 2: env var. Phase 3+: Project.allowed_providers.
 * Default is local-only — enabling openai is an explicit opt-in.
 */
export function assertProviderAllowed(providerId: ProviderId): void {
  const raw = (process.env.FORGE_ALLOWED_PROVIDERS ?? "ollama") as string;
  const allowed = raw.split(",").map((s) => s.trim()).filter(Boolean);
  if (!allowed.includes(providerId)) {
    throw new Error(
      `provider "${providerId}" is not allowed for this project. ` +
      `Allowed: ${allowed.join(", ")}. ` +
      `To enable, set FORGE_ALLOWED_PROVIDERS=${allowed.join(",")},${providerId} ` +
      `(Phase 2 temporary; Phase 3 moves this to Project.allowed_providers).`
    );
  }
}

export function providerFromEnv(): ModelProvider {
  const provider = (process.env.MODEL_PROVIDER ?? "ollama") as ProviderId;
  assertProviderAllowed(provider);
  return createProvider({
    provider,
    modelName: process.env.MODEL_NAME ?? "",
    timeoutMs: Number(process.env.MODEL_TIMEOUT_MS ?? 120_000),
    ollamaBaseUrl: process.env.OLLAMA_BASE_URL,
    openaiApiKey: process.env.OPENAI_API_KEY,
    openaiBaseUrl: process.env.OPENAI_BASE_URL,
  });
}