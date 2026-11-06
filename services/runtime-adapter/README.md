# Runtime adapter

Runs one agent's loop against a model, and reports to the control plane. It has no database and no decisions about the pipeline.

Start with `src/server.ts` (`POST /runs`, `POST /runs/:id/cancel`, `GET /health`), then `src/agent.ts` (the loop).

| File | Job |
|---|---|
| `src/agent.ts` | Call the model, run tools, repeat; retries, rate-limit waits, cut-off replies |
| `src/executor.ts` | Runs each tool call in the container |
| `src/types.ts` | The `ModelProvider` contract and its error types |
| `src/provider-factory.ts` | Picks a provider; enforces `FORGE_ALLOWED_PROVIDERS` |
| `src/claude-provider.ts` | Claude over the HTTP API, with prompt caching |
| `src/openai-compatible.ts` | Shared base for `src/openai-provider.ts` and `src/ollama-provider.ts` |
| `src/scripted-provider.ts` | Test double: replays canned replies per agent |

`src/cli.ts`, `src/workspace.ts`, `src/tools.ts`, `src/roles.ts` and `src/checkpoint.ts` are mostly for the standalone `pnpm forge` mode from Phase 2. Under the control plane, tools and prompts come from the agent definitions.

Traps:

- **Rate limits don't use up retries,** and they wait up to five minutes. A refused call (bad key, no credit) isn't retried at all.
- Temperature isn't sent on API versions from `2026-10-21`, which reject it.
- `ANTHROPIC_PROMPT_CACHE=off` is for a proxy that rejects `cache_control`.
- It finds `.env` by walking up from its working directory.
- `scripts/smoke-test.ts` (`make smoke`) calls the configured model for real.

Test: `pnpm test`.
