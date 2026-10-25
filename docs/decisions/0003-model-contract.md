## Amendment (2026-10-25) — Two first-class providers

The `ModelProvider` contract is unchanged (OpenAI-compatible /v1/chat/completions).
Two concrete providers are shipped:

- **OllamaProvider** — local, default, no key, loopback endpoint.
- **OpenAIProvider** — cloud, opt-in, requires OPENAI_API_KEY.

Both extend `OpenAICompatibleProvider`; adding a third compatible provider
(vLLM, LiteLLM, Azure, Groq, etc.) is a ~15-line wrapper.

**Security consequence:** MODEL_PROVIDER=openai sends prompts, file contents,
and diffs to OpenAI. This must be enforced by `Project.allowed_providers`
(§8.1) once Phase 3 lands. Interim enforcement: FORGE_ALLOWED_PROVIDERS,
defaulting to "ollama".

**No silent fallback.** Provider failures surface as errors. Switching
providers is governed by §3.4's model-swap playbook.