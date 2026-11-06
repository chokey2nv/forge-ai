# Control plane

The Go service that owns all state and runs the task pipeline. For the pipeline itself, read [ARCHITECTURE](../../docs/ARCHITECTURE.md) first.

Entry point: [`cmd/forge/main.go`](cmd/forge/main.go) reads the environment, wires everything together and starts the background loops (scheduler, stale-run watcher, pull request poller, reaper).

Packages, under `internal/controlplane/`:

| Package | Job |
|---|---|
| `api/` | HTTP handlers, and most of the pipeline's logic |
| `domain/` | The data types; no logic to speak of |
| `statemachine/` | Which status can follow which |
| `orchestration/` | Starts ready subtasks; merges them into the parent |
| `verification/` | The gates run on a change |
| `workspace/` | Containers, worktrees, the sandbox, shared packages |
| `delivery/` | Git and `gh`: branches, merges, pull requests, reverts |
| `agent/` | Loads agents and skills; the tool catalog |
| `adapter/` | Client for the runtime adapter |
| `runner/` | Background watcher and reaper |
| `store/` | Repository interfaces, and their Mongo versions |

Dependencies point inward: `api` uses everything, and `domain` uses nothing. `orchestration` can't import `api`, so `orchestration/hooks.go` holds a function variable that `api` sets at startup.

Test: `go test ./...`. Some tests need Docker or Mongo running.
