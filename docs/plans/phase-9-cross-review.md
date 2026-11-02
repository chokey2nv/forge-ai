# Phase 9 — Cross-review and escalation

**Status:** 9.1–9.4 done (2 Nov 2026) · **Drafted:** 2 Nov 2026 · **Builds on:** verified Phase 8 (`docs/operations/RUNBOOK.md` §9)

## Goal

Turn single-reviewer supervision into a team: every reviewer an agent lists judges its work, the change integrates only when all of them pass, rejections come back as one consolidated set of defects, and a change the reviewers keep rejecting is escalated to a human with the evidence instead of silently blocking.

This closes the one vision requirement still marked partial in `mile-stone-review.md` ("supervise / review each other") and removes the `reviewers[0]` smell: the registry's `reviewers: [...]` list becomes the runtime's truth.

## Where it starts

| Area | Today |
|---|---|
| Reviewer choice | `runVerificationAndReview` takes `impl.Reviewers[0]` |
| Verdicts | One `reviewerVerdict` string on the run, overwritten each round |
| Outcome | The single verdict decides; a rejection re-invokes the implementer until `MAX_REVIEW_ROUNDS`, then `BLOCKED` |
| Parent final review | `head-engineer.reviewers[0]`; a rejection blocks the parent outright |
| Reviewer agents | Only `reviewer` |
| Stuck reviewer | A reviewer that never calls back leaves the task in `AGENT_REVIEW` forever (the watcher only watches `RUNNING` runs) |
| Console | One verdict per task's latest run; children of a parent are not shown |

## Decisions

1. **Reviewers run one after another, not in parallel.** They share the implementer's container, and the adapter's diff capture runs `git add -A` / `git reset` there on every checkpoint, so parallel reviewers would race on the index. Sequential is safe and enough to prove cross-review; parallelism belongs with Phase 10.
2. **Every reviewer runs each round, even after a rejection.** One round then yields every reviewer's defects, so the implementer fixes them together instead of discovering them one round at a time.
3. **Escalation goes to a human.** Rounds exhausted by reviewer rejection end in a new `ESCALATED` state with all verdicts attached. Objective failures (verification exhausted, agent produced nothing) stay `BLOCKED`. Re-planning by the head engineer is deferred to Phase 10–12.
4. **Reviewers are review agents.** A reviewer is any agent with `submission_tool: submit_review`; `qa-engineer` stays a work agent that writes tests. The loader rejects a `reviewers` entry that is not a review agent, or is the agent itself.

## Design

### Review record

New collection `reviews`, one document per reviewer per round:

```
{ _id, taskId, runId, round, reviewer, agentDefinition, verdict: pass|fail|error,
  summary, defects: [{severity, description, location}], startedAt, finishedAt }
```

`run.reviewerVerdict` is kept as a summary of the latest round for the existing console, and `run.reviewRound` (added in Phase 8) stays the round counter. The run also records the reviewer it is waiting for (`reviewPending: {round, index, reviewer, startedAt}`).

### Review engine

One function drives a round for both children and parents:

1. After verification passes, start reviewer `index 0` of the agent's `reviewers`, with run ID `<run>-rev<round>-<index>`.
2. On each reviewer completion, check it is the reviewer the run is waiting for (stale or duplicate callbacks are logged and ignored), store its review, and start the next reviewer.
3. When the round is complete, decide with a pure function over the round's verdicts:
   - all `pass` → integrate (child) or `REVIEW_PENDING` (root);
   - any `fail`/`error`, rounds left → re-invoke the implementer with every rejecting reviewer's defects, grouped by reviewer;
   - any `fail`/`error`, no rounds left → `ESCALATED`.
4. A reviewer that exceeds its wall clock plus a margin is recorded as `error` by the watcher and the round continues. A reviewer that ends without calling `submit_review` is also `error`.

A parent's final review uses the same engine with the head engineer's `reviewers`. A parent has no implementer to send back to, so any rejection goes straight to `ESCALATED`.

### Escalation

- New task status `ESCALATED`. Transitions: `AGENT_REVIEW → ESCALATED`; from `ESCALATED` to `INTEGRATED` (child accepted), `REVIEW_PENDING` (root accepted), `RUNNING` (sent back with guidance: one more round), or `REJECTED`.
- `POST /tasks/{id}/escalation` with `{decision: accept|retry|reject, reviewer, rationale, guidance?}`. Recorded in `approvals` like human approvals today; `retry` passes `guidance` to the implementer alongside the reviewers' defects.
- A parent waits on an escalated child rather than blocking; the scheduler's "parent blocks when a child blocks" rule stays limited to `BLOCKED`/`REJECTED`.
- The reaper treats `ESCALATED` as inactive. Accept integrates from the checkpoint diff; retry provisions a fresh workspace from it, as retries already do.

### Agents

- Add `test-reviewer` (judges whether tests actually exercise the acceptance criteria; skill `testing-discipline`) and `security-reviewer` (skill `security-review`).
- `implementer.reviewers: [reviewer, test-reviewer]`; `head-engineer.reviewers: [reviewer, security-reviewer]`; `qa-engineer` and `planner` unchanged.
- Adding these is markdown only; the Phase 8 `add-agent` scenario already proves the path.

### Console

- Task view lists a parent's children with status and assigned agent.
- Per-round, per-reviewer verdicts with defects.
- Escalated tasks: all verdicts plus Accept / Send back (with guidance) / Reject.

The Phase 6 console has not been re-verified since Phase 8; part of 9.4 is getting it running against the current API.

## Increments

Each ends with its tests passing and a commit.

| # | Scope | Exit check |
|---|---|---|
| 9.1 ✅ | `reviews` collection, sequential review engine, verdict aggregation, consolidated defects, loader validation of `reviewers`, `test-reviewer` + `security-reviewer` agents. The parent's final review moved onto the engine here too (it shares the callback path) | Unit tests for aggregation, callbacks and loader rules; scripted `two-reviewers-pass` and `split-verdict` pass; both Claude slices pass with two reviewers per child and on the parent |
| 9.2 ✅ | `ESCALATED` state, escalation API, reviewer timeout in the watcher (with a single-winner claim so a late verdict and the timeout cannot both settle a reviewer) | Scripted `escalate-accept`, `escalate-retry` and `reviewer-timeout` pass. Found and fixed: a run re-entering `RUNNING` kept its old heartbeat, so the watcher marked retries `RESUMABLE` mid-provisioning |
| 9.3 ✅ | Parent final-review rejection → `ESCALATED`; reviewers see the platform's gate results with the tail of each gate's output (they cannot run tests themselves, and a Claude test-reviewer said so) | Scripted `final-review-escalates` passes, including that child and final reviewers' prompts carry the gate results; all 10 scripted scenarios and both Claude slices pass |
| 9.4 ✅ | Console: subtasks, per-round reviewer verdicts and defects, escalation actions, live refresh; `GET /tasks/{id}/children` | Against a live control plane (scripted `escalate-accept`): escalated subtask accepted from the UI, parent went through final review to `REVIEW_PENDING` and was approved from the UI |
| 9.5 | Runbook §5 / §9 updates, `mile-stone-review.md` vision table | Docs match the verified behaviour |

The scripted provider serves review turns from one queue in call order, which is deterministic because reviewers run sequentially; no provider change is needed.

## Costs and risks

- **Model cost:** two reviewers per child plus two on the parent roughly doubles review calls. `reviewers` lists are the knob; the scripted suite keeps the test loop free.
- **Latency:** sequential reviews add one reviewer's wall time per extra reviewer. Acceptable until Phase 10.
- **Reviewer quality:** a strict reviewer can push every task to `ESCALATED`. The Claude slices are the signal; prompts are markdown and cheap to tune.
- **Callback identity:** several reviewers report against one run ID. The engine relies on `reviewPending` to reject stale callbacks; 9.1 tests this directly.

## Out of scope

Parallel reviewers and children (Phase 10), delivery and merge on approval (Phase 11), inline diff comments (Phase 12), auth and multi-tenancy (Phase 13), head-engineer re-planning on escalation.
