# Architecture

How Forge works today. For how to run and operate it, see the [runbook](operations/RUNBOOK.md); for why a past phase was built the way it was, see [plans](plans/).

## The three services

```
 person ──> console ──HTTP──> control plane ──starts a run──> adapter ──> model
            (React)           (Go, MongoDB)  <──callbacks───  (TS)
                                    │                           │
                                    └── Docker containers, git ─┘
```

- **Control plane** owns all state and decides what happens next. It creates containers and git worktrees, runs the checks, and merges.
- **Adapter** holds no lasting state. It runs one agent's loop: call the model, run its tool calls in the container, repeat. It reports heartbeats, checkpoints and completion back to the control plane.
- **Console** is a client of the control plane's API, and never talks to the adapter.

The control plane and adapter trust each other through `FORGE_ADAPTER_TOKEN` and a per-run callback token. Both listen on localhost by default.

## A task's life

Most work follows this path. Each name in capitals is a task status.

**1. Create.** `POST /tasks` makes a task in `CREATED` and its parent branch, `forge/<slug>-<id>`, off the target branch. Nothing runs and nothing is spent yet. On the board it sits in Backlog or Todo.

**2. Plan.** `POST /tasks/{id}/runs` moves it to `DECOMPOSING` and starts the head engineer, which reads the repository and calls `submit_task_graph`. That creates the subtasks and moves the parent to `DISPATCHED`.

**3. Run the subtasks.** The scheduler ticks through dispatched parents and starts each subtask whose dependencies are `INTEGRATED`, up to `MAX_CONCURRENT_RUNS`. Subtasks whose file scopes overlap, or that declare none, are made to wait for each other. Each gets its own branch, worktree and container, and goes `DISPATCHED` → `RUNNING`.

**4. Check a subtask.** When its agent finishes:
- `VERIFYING`: the gates run in a container (secrets, diff-scope, build, test). A failure goes back to the agent with the output, up to `MAX_VERIFICATION_ATTEMPTS`, then `BLOCKED`.
- `AGENT_REVIEW`: every reviewer the agent's definition lists reviews at once, and all must pass. A rejection goes back with their findings, up to `MAX_REVIEW_ROUNDS`, then `ESCALATED`. If the agent reported a criterion it can't meet (`report_out_of_scope`), the first rejection escalates.
- `INTEGRATED`: the subtask's work is committed on its branch and merged into the parent branch.

**5. Check the combined change.** Once every subtask is `INTEGRATED`, the parent goes `INTEGRATING` → `VERIFYING` → `AGENT_REVIEW`, using the head engineer's reviewers. Tests are skipped if a subtask already passed them on exactly this code. Passing records a **revision** and moves it to `REVIEW_PENDING`.

**6. A person decides.**
- **Approve:** `APPROVED` → `DELIVERING`, then `MERGED` (no remote: a local merge) or `PR_OPEN` (a remote: push and open a pull request; a poller then moves it to `MERGED`, or to `REJECTED` if closed).
- **Request changes:** back to `DECOMPOSING`. The head engineer plans follow-up subtasks for the open review threads, and the result comes back as the next revision, up to `MAX_REVISIONS`.
- **Reject:** `REJECTED`.

A merged task can be rolled back: `ROLLING_BACK` → `ROLLED_BACK`, through a revert commit or a revert pull request. If the rollback fails, the task returns to `MERGED`.

The full list of allowed moves is in [`task.go`](../services/control-plane/internal/controlplane/statemachine/task.go). Every status change goes through the state machine's service, which records an event and refuses a move if another writer got there first.

## When things go wrong

| Situation | Status | Way forward |
|---|---|---|
| A check or agent gave up | `BLOCKED` | Resume with more steps, or Escalate |
| Agent stopped sending heartbeats for `STALE_AFTER` | `RESUMABLE` | Resume: a new container is built from the last checkpoint |
| Reviewers keep rejecting, or a person stepped in | `ESCALATED` | A person accepts, sends back or rejects |
| Delivery failed (dirty checkout, conflict, push error) | `BLOCKED` with a delivery error | Fix the cause, then Retry delivery |
| Spend cap reached | `BLOCKED` | Raise the cap, then resume |
| Model refuses calls (bad key, no credit) | `BLOCKED`, not retried | Fix the key or credit, or Escalate and Accept |

Accepting an escalation calls no model, so work can still be delivered when Claude can't be reached. **Skip the checks** in the console escalates a task that is still verifying or being reviewed.

A parent stays `BLOCKED` while any subtask is `BLOCKED` or `REJECTED`, and is reopened when that subtask moves on.

## Workspaces and safety

Each run works in a git worktree mounted at `/workspace` in a container from the worker image. Containers run as an unprivileged user with all capabilities dropped, a read-only root filesystem and no network. Packages are installed once per set of dependency files into a shared, read-only volume, with the project's own install scripts removed, so nothing an agent writes can reach other tasks.

A background reaper removes containers and workspaces nothing can use any more. It only touches ones labelled with its own database name, so a test instance on another database can't harm a running Forge.

## People and access

People sign in with a session cookie; scripts use `fgt_` API tokens. Each task belongs to a project, and each project lists its repositories and members. Roles add up: viewer, member (create and start work, comment), reviewer (decide), admin (manage the project). Instance admins can do everything. `routes.go` states the access level of every route.

## Agents

An agent is a markdown file in `agents/`: frontmatter for its tools, skills, reviewers and submission tool, and a system prompt as the body. `submission_tool` decides what its completion means to the pipeline, so a new agent needs no code. See the [agent package](../services/control-plane/internal/controlplane/agent/README.md).

## Money

Every model call's tokens are reported on checkpoints and priced by `api/spend.go`, including prompt-cache reads and writes. Caps apply per task (shared with its subtasks) and per project. Over a cap, the checkpoint's answer tells the adapter to stop before its next model call. The Claude provider uses prompt caching, because an agent resends its whole conversation at every step.

## Terms

| Term | Meaning |
|---|---|
| Parent / subtask | A top-level task, and the pieces the head engineer split it into (`parentId`). |
| Run | One attempt at a task by an agent (`WorkflowRun`). Retries and resumes add runs or new IDs. |
| Checkpoint | The conversation and workspace diff saved after a step; what Resume rebuilds from. |
| Gate | One automated check in verification. |
| Round | One pass of all the reviewers over a change. |
| Revision | A version of the combined change put in front of a person. |
| Parent branch | The branch subtasks merge into; also called the integration branch in code. |
| Target branch | Where the finished work is delivered. |
| Thread | A person's comment on a line of a revision. |
| Follow-up | A trip back to the head engineer after Request changes or a sent-back escalation. |
| Board lane | A stored choice of column, only where the status leaves one: Backlog or Todo, Done or Closed. |
| Scripted model | A test provider that replays canned replies; costs nothing. |

## Known gaps

- The planner role exists (`PLANNER_ENABLED`) but has not been verified with a real model.
- Resuming a rollback interrupted by a restart isn't covered.
- The eval suite has only been run with the scripted model.
