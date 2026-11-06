# Forge AI

Start here, whether you are a person or Claude. Forge takes a requirement and acceptance criteria, has a team of AI agents do the work in Docker containers, checks and reviews the result, and hands it to a person to approve and merge.

Read next: [how it works](docs/ARCHITECTURE.md). Each code folder has a short `README.md`. Everything else is listed in the [docs index](docs/README.md).

## Layout

| Path | What it is |
|---|---|
| `services/control-plane/` | Go service: the API, task pipeline and all state. Most changes land here. |
| `services/runtime-adapter/` | TypeScript service: runs one agent's loop against a model. |
| `apps/console/` | React console: the Kanban board and task pages. |
| `agents/`, `skills/` | The agents' prompts and tools, as markdown. No code. |
| `scripts/` | `forge.sh` runs everything; the rest are test harnesses and slices. |
| `tests/` | Harness scenarios and eval cases. |
| `infrastructure/` | The worker image and a Mongo compose file. |

## Running it

```bash
scripts/forge.sh up        # also: down, restart, status, logs [service], user, users, eval
```

Forge's own ports are **8085** (control plane), **8086** (adapter) and **5174** (console). `forge.sh` sets them and they override `.env`.

**Ports 8080 and 5173 belong to another project on this machine.** They are not Forge, even though `.env.example` and older docs mention them. Don't stop or test against anything on them.

Settings and the API key live in `.env` (ignored by git; `.env.example` lists the settings). Never print the key.

## Rules

- **Claude costs real money.** Test with the scripted model. Don't run `make eval`, the `phase*-slice` targets or real-model tasks unless the user asks; real-model runs are saved for the end of a phase.
- **Don't start or restart the user's Forge.** They run `forge.sh restart` themselves, and a restart can interrupt their running subtasks. Test on separate ports and a separate database (see [scripts](scripts/README.md)).
- **Commit or push only when asked.** Work happens on `master`. Leave out anything unrelated that is already modified.
- **Don't edit the user's `.env`.** Say what to add.
- `verification/gates.go` is not gofmt-clean; don't run `gofmt -w` on it, because it would bury the real change.
- **Never add a `.md` file to `agents/` or `skills/`.** Every one is loaded as a definition, and a bad one makes the agent registry fail.

## Testing

| What | Command | Cost |
|---|---|---|
| Control plane | `cd services/control-plane && go test ./...` | Free |
| Adapter | `cd services/runtime-adapter && pnpm test` | Free |
| Console | `cd apps/console && npm test && npx tsc --noEmit -p .` | Free |
| Whole pipeline, scripted model | `make phase8-failure-paths`, or `scripts/phase8-failure-paths.sh <scenario>` for one | Free, but slow and heavy on Docker |
| Docs | `make docs-check` | Free |

## Where to change things

| To change | Look in |
|---|---|
| Which status can follow which | `statemachine/task.go`, plus `boardColumns.ts` for the new status's column |
| What happens when an agent finishes | `api/completion.go`, then `api/runs.go`, `api/review.go` or `api/decomposition.go` |
| An agent's behaviour | `agents/<name>.md` and `skills/`, then reload; no code |
| A tool agents can call | `agent/tools.go` and `services/runtime-adapter/src/executor.ts`; both are needed |
| The checks run on a change | `verification/gates.go` |
| Merging, pull requests, rollback | `api/deliver.go`, `api/rollback.go`, `delivery/` |
| Containers and their sandbox | `workspace/sandbox.go`, `workspace/provision.go` |
| Cost caps and prices | `api/spend.go` |
| An HTTP route and who may call it | `api/routes.go` |
| A model provider | `services/runtime-adapter/src/provider-factory.ts` |
| The board | `apps/console/src/Board.tsx`, `boardColumns.ts` |
| The Team page | `apps/console/src/Team.tsx`, `agentRoles.ts` |

Bare paths above are under `services/control-plane/internal/controlplane/`.

## Keeping docs true

When a folder's purpose or a flow changes, update its README or `docs/ARCHITECTURE.md` in the same commit. Docs explain purpose and traps, not every file. `make docs-check` fails if a code folder has no README, or a doc names a file or make target that doesn't exist.
