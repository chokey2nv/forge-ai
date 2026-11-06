# api

HTTP handlers, and most of the pipeline's logic: a callback from the adapter is what moves a task forward, so "what happens next" lives here.

Start with `routes.go` (every route and who may call it) and `handlers.go` (the `Handlers` struct and its settings).

| Area | Files |
|---|---|
| Tasks, starting a head engineer | `tasks.go`, `board.go` |
| Adapter callbacks | `checkpoints.go`, then `runs.go` (`RecordComplete`) |
| Routing a finished agent by its kind | `completion.go` |
| Head engineer's graph | `decomposition.go`, `task_graph.go` |
| Verifying and retrying a run | `runs.go` |
| Agent review rounds | `review.go` |
| Combined change on a parent | `parent_verification.go`, `revisions.go` |
| Decisions: escalate, accept, send back | `escalate_verification.go`, `escalation.go` |
| Request changes, threads | `followup.go`, `threads.go`, `reanchor.go` |
| Resume | `resume.go` |
| Delivery, rollback | `deliver.go`, `rollback.go` |
| Spend and caps | `spend.go` |
| Sign-in, projects, run tokens | `auth.go`, `projects.go`, `runauth.go`, `middleware.go` |
| Agents and evals, read-only | `agents.go`, `evals.go` |

Traps:

- **A reviewer reports against the implementer's run ID.** So callbacks are routed by the agent that ran, not by the run's provenance. See `completionKindOf`.
- **Results can arrive late.** A person may have escalated, or the watcher marked a run stale, while checks or a reviewer were still going. Handlers re-read the task and discard stale results (`stillVerifying`), so keep that when adding a background step.
- **Long work runs in goroutines after the handler returns,** so it can't use the request's context.
- **A failed status change can be a lost race,** not a bug: see `ErrStaleTransition` in `statemachine/`.
- **Don't let an empty diff replace saved work.** A stale or lost workspace can report one; `keptDiff` guards this.
