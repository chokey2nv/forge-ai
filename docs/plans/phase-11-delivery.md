# Phase 11 — Delivery

**Status:** 11.1–11.4 done (3 Nov 2026) · **Drafted:** 2 Nov 2026 · **Builds on:** verified Phase 10 (`docs/operations/RUNBOOK.md` §9)

## Goal

Every task's work lives on real git branches from the moment it is created, and a human approval delivers it: a pull request opened with `gh` when the repository has a remote, otherwise a merge into the target branch. The console says beforehand which of the two approval will do. Nothing reaches the target branch or a remote without that approval, nothing delivered is rewritten, and a delivered change can be rolled back.

## Where it starts

| Area | Today |
|---|---|
| Parent branch | `forge/<task-id>` is created at decomposition, from `baseCommit` resolved to a SHA |
| Subtask work | Each subtask works in a per-run clone; at integration its diff is applied 3-way to the parent's integration worktree and committed there. No subtask branch exists in the repository |
| Approval | `POST /tasks/{id}/approve` moves `REVIEW_PENDING → APPROVED`. Nothing else happens; `APPROVED → MERGED` is unused |
| Target branch | Not recorded: `baseCommit: "HEAD"` loses which branch the work is for |
| Integration worktrees | `forge-int-*` kept forever |
| Tooling | git 2.45 (`merge-tree --write-tree` merges without a checkout); `gh` 2.72 installed |

## Decisions

1. **The parent branch is created when the task is created**, before decomposition, named from the task's description: `forge/<slug>-<short id>`, the slug being the first words of the requirement, lower-case, hyphenated, at most ~40 characters (the id keeps names unique). It starts at the target branch's tip, which is also the integration base.
2. **Each subtask gets its own branch, cut from the parent branch** when it is dispatched: `<parent branch>-<subtask slug>` (git cannot have both `forge/x` and `forge/x/y`). After its review passes, the subtask's work is committed on that branch and **merged into the parent branch** with a merge commit. Parallel siblings therefore merge like any two branches; a merge conflict blocks the subtask, as an apply conflict does today.
3. **Approval delivers, by remote:**
   - **Remote exists** (`origin`, else the first remote): push the parent branch and open a pull request into the target branch with the installed `gh` (`gh pr create`). A human merges it on the forge; Forge watches it (`gh pr view`) and moves the task to `MERGED` or `REJECTED`.
   - **No remote:** merge the parent branch into the target branch locally.
4. **The console shows what approval will do** before anyone clicks: the remote (or "no remote"), the target and parent branches, and the resulting action ("Approve → open a PR from `forge/…` into `main` on github.com/owner/repo" / "Approve → merge `forge/…` into `main` locally"), plus a warning when `gh` is missing or not logged in, or the remote is not one `gh` can open PRs on. The same information is served by `GET /tasks/{id}/delivery`.
5. **The target branch is recorded at task creation**: the branch `baseCommit` names, or the repository's current branch for `HEAD`; overridable with `targetBranch`.
6. **Target moved? Merge it in and verify again.** If the target advanced since the parent branch was created, delivery merges it into the parent branch first; a conflict blocks; a clean merge re-runs the verification gates before anything is delivered.
7. **Never touch a working tree the user is using.** Local delivery builds the merge commit without a checkout (`merge-tree --write-tree`, `commit-tree`) and moves the branch with a compare-and-swap `update-ref`. A target checked out with uncommitted changes is refused; a clean checkout of it is fast-forwarded.
8. **Rollback reverts; it never rewrites.** Local: a revert commit for the delivery merge. Remote: a revert pull request.
9. **Cleanup.** The integration worktree is removed after delivery or rejection. Branches stay as the audit trail; only the parent branch is pushed.

## Design

### Branches

```
main ─────●────────────────────────────●  (local delivery: merge commit)
           \                          /
forge/fix-paginate-1a2b ──●──────●───●     parent branch, created with the task
                          ↑      ↑
            subtask merges (merge commits)
             /                    \
  forge/fix-paginate-1a2b-fix      forge/fix-paginate-1a2b-tests
```

- **Task creation:** resolve the target branch and its tip; create the parent branch there; record `targetBranch`, `branch`, `integrationBaseCommit`. Decomposition only creates the integration worktree on the existing branch.
- **Subtask dispatch:** create `<parent>-<subtask slug>` at the parent branch's current tip (the commit Phase 10 already pins as the run's base).
- **Subtask integration:** build the subtask's commit from its recorded diff onto its branch (a temporary worktree at the run's base: apply, commit, remove), then `git merge --no-ff` it into the parent branch in the integration worktree, under the Phase 10 per-parent lock. This uses the checkpoint diff, so it works even after the subtask's workspace was reclaimed (e.g. accepted from `ESCALATED`).

### States

```
REVIEW_PENDING → APPROVED → DELIVERING → MERGED                     (no remote)
                                       → PR_OPEN → MERGED          (remote; merged on the forge)
                                                 → REJECTED        (PR closed unmerged)
                          DELIVERING → BLOCKED   (conflict with target, gates fail on the merged result,
                                                  dirty checkout, push or gh error)
MERGED → ROLLED_BACK
```

### Delivery preview — `GET /tasks/{id}/delivery`

`{ mode: "pull_request" | "local_merge", remote: {name, url, host} | null, targetBranch, branch, ghReady: bool, warnings: [...], action: "…human sentence…" }`, computed live from the repository (so adding a remote changes it). Delivery itself uses the same function, so the hint and the action cannot disagree.

### Rollback

`POST /tasks/{id}/rollback {reviewer, rationale}` on a `MERGED` task: local revert commit (compare-and-swap, as delivery), or a revert branch and `gh pr create`. Recorded in `approvals`.

## Increments

| # | Scope | Exit check |
|---|---|---|
| 11.1 ✅ | Parent branch at task creation (named from the description), target branch recorded, remote/`gh` detection, `GET /tasks/{id}/delivery`, console hint on the task page | Unit tests on real repos: slug naming, target resolution (HEAD, named branch, explicit, missing, SHA, detached), branch creation never moves an existing branch; remote URL parsing; preview with no remote, GitHub + `gh` logged in, origin preferred, `gh` not logged in, `gh` missing, local-path remote. Scripted `verify-retry` checks the branch exists after creation and the preview switches from local merge to pull request when a remote is added (stub `gh`). Console hint checked for both modes against a live control plane |
| 11.2 ✅ | Subtask branches `<parent>-<slug of title>` (unique among siblings) created at dispatch at the subtask's pinned start; integration commits the subtask's diff on its branch in a temporary worktree and merges it into the parent branch with `merge --no-ff` (`integrate(<id>): <title>`), replacing 3-way diff-apply; a conflicting merge is aborted and blocks | Real-repo unit tests: commit from a diff, siblings merge, conflict aborted cleanly, empty and non-applying diffs; integrator end-to-end (two siblings merge, branches created on demand, a conflicting third blocks child and parent). Scripted: one merge commit per subtask and one commit per subtask branch; all 16 scenarios pass; Claude parallel slice passes |
| 11.3 ✅ | Delivery on approval: `APPROVED → DELIVERING → MERGED` (local merge: target-moved merge-in and re-verification, dirty refusal, compare-and-swap ref update) or `→ PR_OPEN` (push, `gh pr create`) with a watcher (`PR_OPEN → MERGED / REJECTED`); failures block with the reason and `POST /tasks/{id}/deliver` retries; integration worktree removed after delivery; console delivery record and retry button | Real-repo unit tests: merge into a checked-out and a not-checked-out target, dirty checkout refused untouched, moved target detected, push to a bare origin never forces, stub-`gh` PR create/view. Scripted `deliver-local`, `deliver-target-moved`, `deliver-dirty` (block, clean, retry), `deliver-pr` and `deliver-pr-closed` (GitHub URL with `pushInsteadOf` a local bare repo; stub `gh` records arguments and serves the PR state the harness sets) |
| 11.4 ✅ | Rollback by revert: `POST /tasks/{id}/rollback {reviewer, rationale}` on a `MERGED` task → `ROLLING_BACK` → `ROLLED_BACK` (local: a revert commit landed like the merge — dirty refusal, fast-forward or compare-and-swap) or `REVERT_PR_OPEN` (a `<parent>-revert` branch on the remote's target, pushed, `gh pr create`; the watcher maps merged → `ROLLED_BACK`, closed → `MERGED`); a failed rollback returns to `MERGED` with the reason; recorded in `approvals`. Remotes are identified by their configured URL, not the `insteadOf`-rewritten one. Console: rollback form and record | Real-repo unit tests: revert keeps later work and history, dirty checkout refused, conflicting revert refused, revert branch built on the remote's target. Scripted `rollback-local`, `rollback-pr` (the harness merges PRs into the bare origin, so the revert undoes a real forge merge; all git traffic stays local). Claude slice `scripts/phase11-delivery-slice.sh` passed: approve → `MERGED`, `main` holds the fix and passes; roll back → `main`'s tree is the original. Console: rollback form on a merged task, rolled back from the UI. A second rollback of an already-reverted delivery is refused rather than committing an empty revert |

Known gaps after 11.4: a task left in `DELIVERING` by a control-plane restart is not resumed (it stays `DELIVERING`; set it back to `BLOCKED` with its delivery record and retry), and a PR whose remote is a local path is attempted and blocks with `gh`'s error (the hint warns beforehand).

## Risks

- **Writing to the user's repository.** Branches now appear in the repository at task creation, and approval moves the target branch. Mitigations: delivery only on approval, compare-and-swap ref updates, no rewriting, refusing dirty checkouts, rollback by revert.
- **`gh` acts as the logged-in user.** PRs are opened under whoever is logged in to `gh` on the control plane's machine. The console hint names the account and repository before approval.
- **Real remotes in tests.** Tests use a local bare repository as `origin` and a stub `gh`; nothing in the automated suite touches GitHub.

## Out of scope

Auto-merging on the forge, squash or rebase strategies, pushing subtask branches, forges `gh` cannot drive (the hint warns, delivery blocks), deploys, inline review comments (Phase 12).
