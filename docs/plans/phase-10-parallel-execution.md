> **History.** The design as planned for this phase, which later work may have changed. Check anything here against the code; current docs start at [`CLAUDE.md`](../../CLAUDE.md).

# Phase 10 — Parallel execution

**Status:** done (2 Nov 2026) · **Drafted:** 2 Nov 2026 · **Builds on:** verified Phase 9 (`docs/operations/RUNBOOK.md` §9)

## Goal

Independent subtasks run at the same time, safely. A requirement split into three subtasks where two are independent should take roughly two subtasks' worth of wall time, not three, and the result must be the same as if they had run one after another: every child integrates exactly once, in a consistent order, onto a branch that still passes its tests.

Reviewers of one change also run side by side instead of one after another.

## Where it starts

Read from the code, not assumed:

| Area | Today |
|---|---|
| Agents of independent children | Already run concurrently: `StartRun` is fire-and-forget |
| Provisioning | Serial, inside the scheduler's 5s tick (`dispatchChild` clones, runs `npm install`, starts the container). One slow install stalls every other child and parent |
| Concurrency limit | None. Each run is a 2 GB / 2 CPU container; a wide graph can start as many as it has ready children |
| Integration | `Integrator.Integrate` runs from whichever HTTP callback finishes a child's review. Two siblings finishing together run `git apply` + `git commit` in the same integration worktree concurrently |
| Applying a sibling's diff | Plain `git apply` of a diff taken against the integration HEAD at the child's start; no 3-way fallback when HEAD has moved |
| Scope overlap | The decomposition skill says independent tasks must not touch the same files; nothing enforces it |
| Stale runs | A run waiting on provisioning longer than `STALE_AFTER` is wrongly marked `RESUMABLE` (Phase 9 follow-up); concurrency makes long provisioning more likely |
| Reviewers | Sequential by design (Phase 9): they share the implementer's container, and the adapter runs `git add -A` / `git reset` there on every checkpoint |
| Scripted tests | One turn queue per role; parallel children would take each other's turns |

## Decisions

1. **Bounded parallelism.** `MAX_CONCURRENT_RUNS` (default `3`) caps live workspaces across the control plane. Ready children beyond the cap wait in `CREATED`; dependencies still gate as today.
2. **Provisioning leaves the tick.** The scheduler claims a child (Phase 8), then provisions it in a goroutine. A new run status `PROVISIONING` is ignored by the stale-run watcher, which closes the Phase 9 follow-up.
3. **Integration is serialized per parent and applied 3-way.** A per-parent lock orders integrations. Each child records the exact commit it started from, and its diff is applied with `git apply --3way` against that base, so siblings that changed different parts of the tree integrate cleanly after HEAD moves. A genuine conflict sends the child back for one rework on the current integration HEAD with the conflict in its diagnostics; a second conflict blocks it, as today.
4. **Scope overlap is serialized, not rejected.** At decomposition, two tasks with no dependency path between them whose `diffScope`s overlap, or either of which has no `diffScope`, get an implicit dependency (in graph order) and an event saying why. Parallelism is earned by declaring disjoint scopes; the diff-scope gate already keeps children inside them.
5. **Reviewers run in parallel in the shared container.** Reviewers only read. The adapter skips workspace-diff capture for review runs (the implementer's diff is already recorded) and runs reviewer git commands with `GIT_OPTIONAL_LOCKS=0`, so no reviewer takes the index lock. The review engine waits for a set of pending reviewers instead of one.
6. **One control plane per database.** The per-parent lock is in-process. Running two control planes against one database is documented as unsupported until a Mongo-backed lock exists (out of scope).

## Design

### Scheduling

- Each tick: count live runs (`PROVISIONING`, `RUNNING`, `VERIFYING`, plus reviews in flight), then claim ready children up to the free capacity, oldest parent first so one wide graph cannot starve another.
- Claimed children provision in goroutines. Provisioning failure blocks the child as today.
- The run records `baseCommit` as a SHA (resolved before cloning), not the integration branch name.

### Integration

- `Integrator.Integrate` takes a per-parent mutex for apply + commit + transition.
- Apply: `git apply --3way` with the child's diff against its recorded base. On conflict, abort cleanly (`git reset --hard` on the integration worktree to its pre-apply HEAD), then rework once, then block.
- Rework reuses the retry path (`reinvokeImplementer`) with a workspace cloned from the current integration HEAD and the checkpoint diff 3-way-applied onto it; the conflict text goes into the diagnostics.

### Decomposition rules

- After building the graph: for each pair of tasks with no path between them, if their scopes overlap (glob intersection) or either scope is empty, add an edge from the earlier to the later in submission order.
- Record `implicitDependsOn` separately from the head engineer's `dependsOn`, so the console can show "waits for X (overlapping scope)" and the head engineer's intent stays visible.

### Parallel reviewers

- `ReviewPending` becomes `{round, reviewers, outstanding: [index...], startedAt}`. All reviewers start together; each callback atomically removes its index from `outstanding` (single-winner, as Phase 9's claim), and whoever empties it decides the round.
- The reviewer timeout applies per reviewer.
- Adapter: no workspace-diff capture when `submissionTool` is `submit_review`; `docker exec -e GIT_OPTIONAL_LOCKS=0` for those runs.

### Console

Subtask table shows which children are running at the same time, and implicit dependencies with their reason.

## Increments

Each ends with its tests passing and a commit.

| # | Scope | Exit check |
|---|---|---|
| 10.1 ✅ | `PROVISIONING` status (dispatch, retry and resume); provisioning off the tick; `MAX_CONCURRENT_RUNS`; base commit as SHA; per-parent integration lock with `--3way`; scripted provider turn queues keyed by task | Unit tests (race detector): tick never waits on provisioning, runs are `PROVISIONING` meanwhile, cap honoured, base pinned, dependents wait; `--3way` on a real repo merges, conflicts cleanly. Scripted `parallel-siblings`, `diamond`, `concurrency-cap` pass on new fixture `parallel-features`; all 13 scenarios pass. Claude slices not re-run |
| 10.2 ✅ | Scope-overlap serialization with `implicitDependsOn` and a `child.serialized` event per reason; dependency cycles rejected at decomposition; console shows implicit dependencies. **Changed from the plan:** an integration conflict blocks (with a clean reset, from 10.1) instead of triggering a rework; see below | Unit tests for pattern overlap, serialization, cycle detection, and the scheduler honouring implicit dependencies; scripted `overlap-serialized` and `cycle-rejected`; all 15 scenarios pass |
| 10.3 ✅ | Parallel reviewers: all of a round's reviewers start together; each verdict settles its slot by claim → save → count, and the last to count decides the round; the watcher's timeout settles every outstanding slot; adapter skips diff capture and sets `GIT_OPTIONAL_LOCKS=0` for review runs; scripted provider can key turns by agent prompt | Unit tests for slot matching (incl. an agent listed twice); scripted `parallel-reviewers` (two 3s reviewers, round took 3s); every review scenario re-keyed by reviewer and passing. The full run found a real concurrency bug: document IDs were the time in nanoseconds and collided when parallel subtasks transitioned in the same clock tick, failing a transition that had already happened. IDs are now UUIDs (concurrency test), a transition no longer fails on its event write, and the parallel scenarios passed 3 extra times each. All 16 scenarios pass |
| 10.4 ✅ | `make phase10-parallel-slice` with a wall-time report; `GET /tasks/{id}/events`; console timeline of subtasks; subtask brief (scope and siblings) in implementer and reviewer prompts; docs | Claude: 3 subtasks, 2 in parallel, 178s wall for 267s of subtask work (1.5x), all integrated, branch green; console timeline checked on that run. Both Phase 8 Claude slices pass (pagination twice). All 16 scripted scenarios pass |

## Costs and risks

- **Machine load:** three concurrent 2 GB containers plus `npm install`s. The cap is the knob; the default is deliberately low.
- **Model rate limits:** parallel agents and reviewers multiply concurrent API calls. The adapter's retry/backoff applies; a provider 429 surfaces as a model-call error.
- **Determinism in tests:** scripted turns keyed by task keep scenarios reproducible; timing assertions use run timestamps with generous margins.
- **3-way merges that apply but are wrong:** a clean textual merge can still break behaviour. Combined verification and the final review already run on the integrated branch, which is the backstop.

## Decision change in 10.2: no automatic conflict rework

Decision 3 planned one automatic rework when a child's diff conflicts at integration. With overlap serialization in place, two children run in parallel only when their declared scopes are disjoint, and the diff-scope gate rejects any change outside a child's scope. Siblings therefore never change the same file, and the 3-way apply cannot hit a genuine conflict from parallel work; nothing in the pipeline can produce the case the rework would handle, so it could not be tested end to end. A conflict, should one arise from something unforeseen, resets the integration worktree and blocks the child and parent with git's output, which is what 10.1 already does. Revisit if conflicts are ever observed.

## Found in 10.4

The first Claude re-run of the pagination demo split it into a fix (scope `src/paginate.js`) and a sibling tests subtask. The test-reviewer, not knowing tests were a sibling's job, rejected the fix for missing tests; the implementer, not knowing its scope, added them and failed the diff-scope gate twice. Neither agent was told the subtask's scope or what its siblings own. Implementer and reviewer prompts now carry a "Scope of this subtask" section listing its files and its siblings, and reviewers are told not to fail a subtask for a sibling's work. Two re-runs hit the same split and passed with the test-reviewer passing in round 1.

## Follow-ups

- **A broken base fails every sibling.** Each child's test gate runs the whole suite, so if the base already has a failing test (as `cart-pricing` does: fixing it is one of the subtasks), an independent sibling fails verification for a bug that is not its own. Serial execution hid this because dependents start after the fix integrates. Options: scope the test gate to the child's `diffScope` plus a baseline comparison, or have the head engineer make fixes of a red suite a dependency of everything else.

- **Final review has no git.** The parent's verification and final-review container mounts the host integration worktree, whose `.git` points outside the container (the problem children had before per-run clones). A Claude security-reviewer noted it could not search the combined change. Fix: provision the parent container from a clone of the integration branch.

## Out of scope

Multiple control planes per database (needs a distributed lock), workers on other machines, parallelism across separate root tasks beyond the shared cap, delivery and merge (Phase 11), feedback comments (Phase 12), auth and multi-tenancy (Phase 13).
