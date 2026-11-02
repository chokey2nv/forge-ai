# Forge AI

Autonomous AI software-engineering agent team.

Forge AI takes a software requirement plus acceptance criteria, decomposes the work into a dependency-ordered task graph of specialized sub-tasks (head engineer → planner / implementer / reviewer / qa-engineer), executes each sub-task in an isolated Docker workspace, runs automated verification gates, reviews the diff, and surfaces it for human approval.

**Stack:** Go control plane · TypeScript runtime adapter · React console · MongoDB · Ollama / OpenAI / Claude.

## Status

**Current build state: Phase 8, verified end to end** — head engineer, task graph, scheduler, integrator and combined parent verification run with a real model (`make phase8-vertical-slice-demo`, `make phase8-two-child-slice`), and retry, crash-recovery and add-an-agent paths are covered by `make phase8-failure-paths`. See the [runbook](docs/operations/RUNBOOK.md#9-phase-status) for per-phase evidence and `mile-stone-review.md` for the Phase 9–13 roadmap.

## How to run

make smoke          # uses ollama
make phase2-demo

SWITCH TO OPENAI 

# .env additions:
# MODEL_PROVIDER=openai
# MODEL_NAME=gpt-4o-mini
# OPENAI_API_KEY=sk-...
# FORGE_ALLOWED_PROVIDERS=ollama,openai

make smoke
make phase2-demo

---

SWITCH TO CLAUDE

# .env additions:
# MODEL_PROVIDER=claude
# MODEL_NAME=claude-3-5-haiku-20241022   # or claude-sonnet-4-20250514, etc.
# ANTHROPIC_API_KEY=sk-ant-...
# ANTHROPIC_BASE_URL=https://api.anthropic.com/v1   # optional, override for proxies
# FORGE_ALLOWED_PROVIDERS=ollama,openai,claude

make smoke
make phase2-demo

---

Refuse to run on the wrong provider (this is the point of the allow-list):
MODEL_PROVIDER=openai FORGE_ALLOWED_PROVIDERS=ollama make phase2-demo
# → "provider "openai" is not allowed for this project..."


## Operations

Setup, configuration, the Phase 8 slice and failure-path tests, troubleshooting and teardown are in [`docs/operations/RUNBOOK.md`](docs/operations/RUNBOOK.md).




Start the server 

```sh 
set -a; source .env; set +a
cd services/control-plane
go run ./cmd/forge
```