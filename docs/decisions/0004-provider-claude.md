## Amendment (2026-11-01) — Claude as third provider

The `ModelProvider` contract is unchanged. Claude (`anthropic` / Claude family)
is now a first-class provider alongside Ollama and OpenAI.

- **ClaudeProvider** — standalone client for Anthropic's `/v1/messages` API.
  Implemented directly against `ModelProvider` rather than extending
  `OpenAICompatibleProvider`, because Claude is not OpenAI wire-compatible
  (own endpoint, `x-api-key` + `anthropic-version` headers, top-level `system`
  param, `tool_use` / `tool_result` content blocks, `stop_reason` values).
- **Config:** `MODEL_PROVIDER=claude`, `MODEL_NAME=<claude-model>`, required
  `ANTHROPIC_API_KEY`, optional `ANTHROPIC_BASE_URL` (proxy override).
- **Opt-in:** add `claude` to `FORGE_ALLOWED_PROVIDERS` (e.g. `ollama,openai,claude`).
- **Capabilities:** tool calling + structured output via tool use; context window
  assumed 200k.

Adding a genuinely OpenAI-compatible third provider (vLLM, LiteLLM, Azure, Groq)
remains a ~15-line wrapper; Claude was the exception and got a first-class client.
