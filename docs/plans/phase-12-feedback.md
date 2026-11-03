# Phase 12 — Human feedback

**Status:** Done (3 Nov 2026): 12.1–12.5 · **Drafted:** 3 Nov 2026 · **Builds on:** Phase 11 (`docs/plans/phase-11-delivery.md`)

## Goal

A human reviewing a task can comment on lines of its change, ask for changes instead of only approving or rejecting, and get answers: the agents rework the change, reply in each thread, and the task comes back for review with a new revision that shows what changed since the last one. Nothing is delivered until a human approves.

## Where it starts

| Area | Today |
|---|---|
| Human decision | `REVIEW_PENDING` → Approve (delivers) or Reject (terminal `REJECTED`). There is no "request changes" |
| What the human reviews | Every root task is decomposed, so the human always reviews the parent's combined change. The task page shows the latest run's checkpoint diff, which for a parent is the head engineer's run, not the combined change. The combined diff (`DiffFromBase` on the integration worktree) is only given to the final reviewers |
| Diff display | A raw `<pre>`: no files, no line numbers, nothing to click |
| Agent reviewer findings | `Defect{severity, description, location}`, where `location` is free text (often a path). Shown in the Reviews list, not on the diff |
| Sending work back | Child tasks: `reinvokeImplementer` with feedback (review rejection, escalation retry). Parents: none. Escalation retry on a parent answers 409 ("no implementer to send back to") |
| Final review records | Fixed IDs: run `run-<task>-final-review`, verification `vr-<task>-combined`. A second final review of the same task would collide |

## Decisions

1. **Comments are threads anchored to a revision.** A *revision* is the parent branch's tip each time the task reaches `REVIEW_PENDING` (r1, r2, …). A thread anchors to `{revision, path, side (old|new), line}`, plus the line's text and a few lines of context so it can be re-located later. A thread can also be general, with no line. Threads live in a new `threads` collection, not in the task document.
2. **Request changes is a third decision**, next to Approve and Reject: `POST /tasks/{id}/request-changes {reviewer, summary}`. It needs at least one open thread or a summary. It is recorded in `approvals` with action `request_changes`.
3. **Changes to a combined change go through the head engineer.** The combined change has no single implementer, so the head engineer is run again in *follow-up mode*. It gets the requirement, the subtasks done so far, the combined diff and the open threads, and submits a follow-up task graph. The new subtasks branch from the parent branch's current tip and merge back into it as in Phase 11. Integration, combined verification and the final review run as usual, and the task returns to `REVIEW_PENDING` as the next revision. The same path lets a human retry an escalated parent with guidance, removing today's 409.
   - *Alternative considered:* reopening the subtask whose files a thread touches. That breaks down for threads spanning several subtasks' files or needing new work, and it needs a way to reopen integrated children. The head engineer already does this routing.
4. **Every open thread must be handled.** Each follow-up subtask lists the threads it addresses (`addresses: [threadId]`). The head engineer may answer a thread directly instead (`responses: [{threadId, body}]`), for example a question or a "won't change, because…". A follow-up graph that leaves an open thread unaddressed and unanswered is refused, as an invalid graph is today.
5. **Agents reply; only humans resolve.** A subtask's brief includes the threads it addresses (scoped as in Phase 10.4). It replies with a `reply_to_thread` tool (implementers have no submission to extend); the control plane reads the calls from its conversation and appends them to the threads as agent comments once its work integrates. A replied-to thread shows as *addressed*. Resolving and reopening are human actions.
6. **Outdated threads are kept, not moved silently.** On a new revision, a thread whose line is unchanged (followed through the diff between the two revisions, so moves are exact) moves to the new line number. One whose line changed is marked *outdated* and shown with its original context and the agent's reply. *Outdated* is a flag beside the status, so an outdated thread can still be open, addressed or resolved.
7. **Approving with open threads is allowed, with a warning.** The console shows the count. The approval record and the PR body list the unresolved threads. *(If you would rather block approval until every thread is resolved, that is a one-line change.)*
8. **Reviewer agents' findings appear on the diff.** A defect whose `location` parses as `path` or `path:line` is shown as a read-only reviewer note on that file or line in the revision it was found in. The final reviewer prompt asks for `path:line` locations.
9. **Final review records are per revision**: `run-<task>-final-review-r<n>` and `vr-<task>-combined-r<n>`. Revision 1 keeps today's IDs so existing data still reads.
10. **Feedback stays in Forge.** Once a PR is open, review happens on GitHub; threads are not synced to PR review comments.

## Design

### States

```
REVIEW_PENDING ──request changes──▶ DECOMPOSING (follow-up) → DISPATCHED → … → INTEGRATING
                                     → VERIFYING → AGENT_REVIEW → REVIEW_PENDING (revision n+1)
ESCALATED (parent) ──retry with guidance──▶ DECOMPOSING (follow-up)
```

New state-machine edges: `REVIEW_PENDING → DECOMPOSING` and `ESCALATED → DECOMPOSING`, plus `DECOMPOSING → REVIEW_PENDING | ESCALATED` when a follow-up graph is refused (the human decides again; a first decomposition that fails still blocks). The follow-up head-engineer run is `run-<task>-revise-<n>`. A task's revisions are capped by `budget.maxRevisions` (default `MAX_REVISIONS`, 3), so a task cannot loop forever on feedback. Escalation retries get final review IDs `-a2`, `-a3`, … for the same revision.

### Data

```
Task.Revisions []Revision        // {n, commit, baseCommit, at, finalReviewRunId}

Thread {
  id, taskId, revision,           // revision it was opened on
  anchor: {path, side, line, lineText, context[]} | null,
  status: open | addressed | resolved | outdated,
  comments: [{author, kind: human | agent | reviewer, body, revision, at}],
  addressedBy: subtask id | null,
}
```

### API

| Endpoint | Purpose |
|---|---|
| `GET /tasks/{id}/diff?revision=n&since=m` | The combined diff for a revision (default: latest), or the changes between two revisions (`git diff <rm> <rn>` on the parent branch). Includes the revision list |
| `GET /tasks/{id}/threads` | Threads with comments; reviewer notes included as read-only entries |
| `POST /tasks/{id}/threads` | Open a thread `{author, body, anchor?}`, only while `REVIEW_PENDING` or `ESCALATED` |
| `POST /tasks/{id}/threads/{tid}/comments` | Reply |
| `POST /tasks/{id}/threads/{tid}/resolve` / `reopen` | Human only |
| `POST /tasks/{id}/request-changes` | Decision 2 |

### Console

- A diff viewer that replaces the `<pre>`: files with collapsible hunks, old and new line numbers, a revision picker, and a *changes since r(n−1)* toggle.
- Click a line to open a thread. Threads are shown inline under their line, with a side list of all threads and their status. Reviewer notes are shown in a different style.
- The decision bar gains *Request changes*, next to Approve and Reject, with the open-thread count. The task timeline shows each revision.

## Increments

| # | Scope | Exit check |
|---|---|---|
| 12.1 ✅ | Revisions and the diff viewer: record a revision each time a parent reaches `REVIEW_PENDING`; `GET /tasks/{id}/diff`; per-revision final review and verification IDs; console diff viewer (files, hunks, line numbers, revision picker) showing the combined change on the parent's page | Unit tests: revision recorded at the parent branch tip, diff between revisions on a real repo, unified-diff parser (renames, new/deleted files, no trailing newline). All 23 scripted scenarios pass (173 checks; `verify-retry` and `final-review-escalates` check the recorded revision and `GET /tasks/{id}/diff`). Console against a live control plane (scripted provider): the parent's page shows r1 by file with line numbers, *work in progress* follows the branch, a subtask's page shows its own diff |
| 12.2 ✅ | Threads: collection, anchors, create/reply/resolve/reopen API; reviewer defects shown as notes; console inline threads; open-thread warning on approve; unresolved threads in the approval record and PR body | Unit tests: anchor validation against the revision's diff (new/old side, added and deleted files, lines and files not in the diff), state rules (no threads outside `REVIEW_PENDING`/`ESCALATED`, not on subtasks, agents cannot open, reply or resolve), location parsing, reviewer notes from each revision's last final-review round. Scripted scenarios: `verify-retry` (open on the `Math.round` line, refuse a line not in the diff, general thread, reply, 403 for an agent, resolve), `deliver-local` (409 once `MERGED`), `deliver-pr` (approval record and PR body list the open thread). Console against a live control plane (scripted provider): comment on a line, reply, resolve, reload and the thread is still there; reviewer notes on their line and in the list; approve with one open thread and the approval lists it |
| 12.3 ✅ | Request changes: decision and state edges; head engineer follow-up mode (prompt with threads and combined diff; graph `addresses` and `responses`; refusal when an open thread is unhandled); follow-up subtasks from the parent tip; back to `REVIEW_PENDING` as r2; escalated-parent retry through the same path | Unit tests: a follow-up graph must address or answer every open thread (by T-handle or ID), final review IDs after escalation retries (`-a2`), the revision budget, the follow-up prompt, the new edges. Scripted scenarios: `feedback-request-changes` (r1, two threads, request changes, one subtask addresses T1 and the head engineer answers T2, r2's changes since r1 are just the fix, a second request is over budget, approve → `MERGED`), `feedback-unhandled-thread` (graph refused, back to `REVIEW_PENDING`, threads still open), `feedback-escalated-parent-retry` (final review escalates, retry with guidance, the follow-up fixes it, r1 with final review `-a2`), `feedback-budget` (`maxRevisions: 1`: request refused, approve still works). Console against a live control plane: comment, request changes, the follow-up runs and the task returns as r2 with the answered thread |
| 12.4 ✅ | Threaded responses: threads in subtask briefs; `reply_to_thread` for follow-up subtasks, posted as agent comments when the work integrates; *addressed* status; re-anchoring and *outdated* on a new revision; console thread placement across revisions (outdated threads listed with their context), the "changes since" view keeping new-side threads, per-comment revision | Unit tests: line relocation through a diff (insertions, changes, deletions, renames, deleted files, pure insertions), reply parsing (T-handles, IDs, last reply wins, unknown ignored), re-anchoring on a real repo (rewritten line outdated, unchanged line moves, deleted line still deleted, resolved and general threads untouched). Scripted: in `feedback-request-changes` the implementer's reply lands on T1 as an r2 agent comment; T1, whose line r2 rewrote, is outdated; T3 moves from line 6 to 8. Console against a live control plane: the moved thread on its new line with its history, the outdated one with its code and the agent's reply |
| 12.5 ✅ | Slice and docs: Claude slice `scripts/phase12-feedback-slice.sh` (comment on a line asking for a change, request changes, the agent fixes it and replies, approve → `MERGED` with the fix); runbook sections; console run from the UI | The slice passes with `claude-haiku-5-5`: the follow-up subtask exported `CENTS`, added its test and replied on the thread, which was outdated on r2; resolve and approve gave `MERGED`. The same flow from the console passed too, with the final reviewers' `path:line` notes on their lines. Fixed on the way: the adapter now accepts `ANTHROPIC_BASE_URL` with or without `/v1`, and the reply instruction goes to the implementer only (reviewers had flagged replies they could not post) |

## Risks

- **Feedback loops cost model time.** Each revision is a full head-engineer → subtasks → final review cycle. Mitigations: the task budget covers all revisions, and the console shows the revision count and the budget left.
- **The head engineer ignores or misroutes a thread.** Decision 4 refuses graphs that leave a thread unhandled, and the final reviewers get the threads to check against.
- **Anchors drift.** Re-anchoring uses exact line text and context only; anything uncertain is marked outdated rather than guessed.
- **Revision 1 IDs.** Existing tasks keep their final review IDs (decision 9), so earlier data keeps reading.

## Out of scope

Syncing threads with GitHub PR review comments, comments on child tasks or during agent review, suggested-change blocks that apply directly, mentions and notifications, multi-user identity (reviewer names stay free text until Phase 13 auth).
