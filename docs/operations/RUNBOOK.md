# Forge AI — Operations Runbook

Setup, startup, and validation for the platform at its current build state (Phases 1–10). Phase 11+ features are not covered.

**Audience:** whoever is running the platform, not building it.
**Last updated:** 3 Nov 2026 (Phases 8–11 verified end to end; see §5.7–§5.11 and §9).

---

## Table of contents

1. [Prerequisites](#1-prerequisites)
2. [One-time setup](#2-one-time-setup)
3. [Configuration reference](#3-configuration-reference)
4. [Starting services](#4-starting-services)
5. [Testing by phase](#5-testing-by-phase)
6. [Observing state](#6-observing-state)
7. [Troubleshooting](#7-troubleshooting)
8. [Reset and teardown](#8-reset-and-teardown)
9. [Phase status](#9-phase-status)
10. [File reference](#10-file-reference)
11. [Quick reference card](#11-quick-reference-card)

---

## 1. Prerequisites

### 1.1 Hardware

For local inference via Ollama:

| Memory | Recommended model | Notes |
|---|---|---|
| 24–32GB+ RAM/VRAM | `qwen3-coder:30b` | Best coding-agent default; reliable tool calls |
| 16GB | `gpt-oss:20b` | Adequate for small tasks |
| 8–16GB | `qwen3:8b` | Expect tool-call failures on multi-step tasks |

If using the OpenAI adapter (`MODEL_PROVIDER=openai`), local hardware constraints do not apply, but `OPENAI_API_KEY` is required.

### 1.2 Software

| Tool | Minimum | Verify |
|---|---|---|
| Go | 1.22 | `go version` |
| Node.js | 20 (22 recommended) | `node --version` |
| pnpm | 8+ | `pnpm --version` |
| Docker | 24+ | `docker --version && docker info` |
| MongoDB | 6.0+ (7 recommended) | `mongosh --version` |
| Ollama | latest | `ollama --version` |
| Git | 2.30+ | `git --version` |
| jq | any | `jq --version` |

Go 1.22 is required: the control plane uses `http.Request.PathValue` for route parameters.

### 1.3 Services

Both must be reachable before you start:

```bash
mongosh --quiet --eval 'db.runCommand({ping:1})' mongodb://127.0.0.1:27017/
# → { ok: 1 }

curl -s http://127.0.0.1:11434/api/tags | jq '.models | length'
# → >= 1
```

---

## 2. One-time setup

### 2.1 Clone and install dependencies

```bash
git clone <repo-url> forge-ai
cd forge-ai

# Go
cd services/control-plane
go mod tidy
go build ./...        # must be clean
cd -

# Node
cd services/runtime-adapter
pnpm install
cd -
```

### 2.2 Pull the model

```bash
ollama pull qwen3-coder:30b       # adjust to your hardware (see §1.1)
ollama list                        # confirm presence
```

### 2.3 Build the worker image

```bash
make worker-build
docker images | grep forge-worker
# → forge-worker   dev   ...
```

The worker image contains Node 22, git, and build tools. It does **not** contain the target project — the worktree is mounted at runtime.

### 2.4 Initialize the fixture repository

The Phase 2 and 3 demos target `tests/evaluations/fixtures/pagination-off-by-one`. It must be a git repository with at least one commit.

```bash
cd tests/evaluations/fixtures/pagination-off-by-one
git status >/dev/null 2>&1 || (
  git init
  git add .
  git commit -m "initial: off-by-one bug"
)
cd -
```

Confirm:

```bash
git -C tests/evaluations/fixtures/pagination-off-by-one log --oneline
# → <hash> initial: off-by-one bug
```

### 2.5 Configuration file

```bash
cp .env.example .env
```

Edit `.env` — see §3. Minimum required for Phase 3:

```bash
MODEL_PROVIDER=ollama
MODEL_NAME=qwen3-coder:30b
OLLAMA_BASE_URL=http://127.0.0.1:11434/v1
FORGE_ALLOWED_PROVIDERS=ollama
MONGODB_URI=mongodb://127.0.0.1:27017
MONGODB_DATABASE=forge_ai
WORKER_IMAGE=forge-worker:dev
ADAPTER_URL=http://localhost:8081
CONTROL_PLANE_SELF_URL=http://localhost:8080
STALE_AFTER=60s
```

`.env` is gitignored. Do not commit it.

### 2.6 Initialize the database (optional)

Collections and indexes are created on first use. To pre-create:

```bash
mongosh forge_ai --eval '
  db.createCollection("tasks");
  db.createCollection("workflowRuns");
  db.createCollection("events");
  db.workflowRuns.createIndex({lastHeartbeat: 1});
  db.events.createIndex({runId: 1, at: 1});
'
```

---

## 3. Configuration reference

All variables live in `.env` at the repo root. The adapter loads `.env` via `dotenv`; the Go binary reads the same values from the shell environment (source `.env` before running).

| Variable | Default | Consumed by | Purpose |
|---|---|---|---|
| `MODEL_PROVIDER` | `ollama` | Adapter | Provider selection (`ollama`, `openai`, `claude`, or `scripted` for tests) |
| `MODEL_NAME` | *(required)* | Adapter | Model identifier sent to the provider |
| `MODEL_TIMEOUT_MS` | `120000` | Adapter | Per-request timeout |
| `OLLAMA_BASE_URL` | `http://127.0.0.1:11434/v1` | Adapter | Ollama endpoint — note `/v1` suffix |
| `OPENAI_API_KEY` | *(unset)* | Adapter | Required only when `MODEL_PROVIDER=openai` |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Adapter | Override for compatible proxies |
| `ANTHROPIC_API_KEY` | *(unset)* | Adapter | Required only when `MODEL_PROVIDER=claude` |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com/v1` | Adapter | Override for compatible proxies |
| `FORGE_ALLOWED_PROVIDERS` | `ollama` | Adapter + CP | Comma-separated allow-list; enabling `openai` / `claude` requires opt-in |
| `MONGODB_URI` | `mongodb://127.0.0.1:27017` | Control plane | Mongo connection |
| `MONGODB_DATABASE` | `forge_ai` | Control plane | Database name |
| `WORKER_IMAGE` | `forge-worker:dev` | Control plane | Container image per run |
| `ADAPTER_URL` | `http://localhost:8081` | Control plane | Adapter base URL |
| `CONTROL_PLANE_ADDR` | `:8080` | Control plane | HTTP bind address |
| `CONTROL_PLANE_SELF_URL` | `http://localhost:8080` | Control plane | Callback base URL passed to adapter |
| `STALE_AFTER` | `60s` | Control plane | Heartbeat gap before a run becomes `RESUMABLE` |
| `MAX_AGENT_STEPS` | `20` | Adapter | Per-run step cap (overridable per task) |
| `MAX_RETRIES` | `2` | Adapter | Model-call retry cap |
| `TASK_TIMEOUT_SECONDS` | `1800` | Adapter | Wall-clock cap |
| `ADAPTER_PORT` | `8081` | Adapter | Adapter HTTP port |
| `AGENTS_DIR` / `SKILLS_DIR` | `agents` / `skills` | Control plane | Agent and skill markdown, re-read on `POST /agents/reload` |
| `DEFAULT_PROVIDER` / `DEFAULT_MODEL` | `ollama` / `MODEL_NAME` | Control plane | Provenance recorded on child runs (the adapter still uses `MODEL_PROVIDER`) |
| `MAX_VERIFICATION_ATTEMPTS` | `2` | Control plane | Consecutive verification failures before a task blocks |
| `MAX_REVISIONS` | `3` | Control plane | Revisions of a parent's combined change a human can be shown (§6.5.3); the task's `budget.maxRevisions` overrides it. Sending a change back is refused once `maxRevisions - 1` follow-ups have been made |
| `MAX_REVIEW_ROUNDS` | `2` | Control plane | Rejected review rounds before a task is `ESCALATED` to a human (§6.6); each round runs every listed reviewer and gets fresh verification attempts |
| `REVIEWER_ENABLED` | `true` | Control plane | Agent review after verification |
| `REVIEWER_TIMEOUT` | `180s` | Control plane | Wall clock given to each reviewer run |
| `REVIEW_TIMEOUT_GRACE` | `60s` | Control plane | Extra wait after `REVIEWER_TIMEOUT` before the watcher records a silent reviewer as an `error` verdict |
| `ORPHAN_CONTAINER_GRACE` | `10m` | Control plane | Age before the reaper removes an unowned container or workspace directory |
| `MAX_CONCURRENT_RUNS` | `3` | Control plane | Live workspaces across the control plane (each a 2 GB / 2 CPU container); ready subtasks beyond it wait in `CREATED`. `0` = no cap |
| `SCRIPTED_PROVIDER_FILE` | *(unset)* | Adapter | Turn script for `MODEL_PROVIDER=scripted` (failure-path tests, §5.9) |
| `SCRIPTED_PROVIDER_LOG` | *(unset)* | Adapter | With `scripted`: file to append each agent's task prompt to |
| `FORGE_GH` | `gh` | Control plane | The `gh` binary used to open pull requests on approval (and to check its login for the console hint) |
| `PR_POLL_INTERVAL` | `60s` | Control plane | How often open pull requests (`PR_OPEN` tasks) are checked with `gh pr view`: merged → `MERGED`, closed → `REJECTED` |

---

## 4. Starting services

Run each in its own terminal, in this order: Mongo → control plane → adapter.

### 4.1 MongoDB

```bash
# Compose-managed:
make dev-mongo

# Or system-installed: verify it's already running:
mongosh --quiet --eval 'db.runCommand({ping:1})' mongodb://127.0.0.1:27017/
```

### 4.2 Control plane (terminal 1)

```bash
set -a; source .env; set +a
cd services/control-plane
go run ./cmd/forge
```

Expected:

```
control-plane listening on :8080 (adapter=http://localhost:8081, worker=forge-worker:dev, allowed=[ollama])
```

Verify: `curl -s localhost:8080/health` → `ok`.

### 4.3 Adapter (terminal 2)

```bash
set -a; source .env; set +a
cd services/runtime-adapter
pnpm dev
```

Expected:

```
adapter listening on :8081
```

Verify: `curl -s localhost:8081/health` → `{"ok":true}`.

### 4.4 Console (terminal 3, optional)

```bash
make console-install    # once
cd apps/console && VITE_API_URL=http://localhost:8080 npx vite --port 5174
```

`VITE_API_URL` must point at the control plane (Vite reads `apps/console/.env*`, not the repo-root `.env`; without it the console assumes `http://localhost:8080`). **New task** creates work: give the repository's path (on the control plane's machine), pick the target branch, write the requirement and acceptance criteria (one per line). Before anything is created the form shows the branch the task will get and what approving it will do — a pull request when the repository has a remote, otherwise a local merge — with any `gh` warnings. *Create and start* creates the task (and its branch) and hands it to the head engineer; starting waits for the workspace to come up, so it can take a little while. Provider, model and budget are under *Model and budget* (defaults from `DEFAULT_PROVIDER` / `DEFAULT_MODEL`). The same can be done with the API: `POST /tasks`, then `POST /tasks/{id}/runs`; `GET /repos/inspect?path=…&targetBranch=…&requirement=…` is the form's preview.

The Tasks view lists top-level tasks and any escalated subtask; open a parent to see its subtasks, each reviewer's verdict per round, verification and its combined change (by file, with line numbers; pick a revision, or compare one with an earlier one — §6.5), comment on its lines (§6.5.2), and to approve, request changes (§6.5.3), reject, decide an escalation, retry a blocked delivery or roll back a merged one.

### 4.5 Startup checklist

```bash
curl -sf localhost:8080/health     && echo " CP OK"
curl -sf localhost:8081/health     && echo " ADAPTER OK"
mongosh --quiet --eval 'db.runCommand({ping:1}).ok' mongodb://127.0.0.1:27017/forge_ai && echo " MONGO OK"
```

All three must print without error before running any demo.

---

## 5. Testing by phase

### 5.1 Phase 1 — Model smoke test

**Validates:** the model can drive tools reliably.

**When to run:** after every model change.

```bash
set -a; source .env; set +a
make smoke
```

**Expected pass:**

```
PASS  healthCheck — reachable
PASS  listModels contains MODEL_NAME — N models
PASS  single-turn code gen — wall 3421ms
PASS  single tool call valid — read_file({"path":"greet.ts"})
PASS  multi-step tool use — final wall 5102ms
PASS  structured output valid — {"name":"forge","version":1}
PASS  tool-call validity ≥ 90% — 5/5

7/7 checks passed on model: qwen3-coder:30b
```

Exit code 0.

**Failures:**

| Failing check | Fix |
|---|---|
| `healthCheck` | Start Ollama; confirm `OLLAMA_BASE_URL` ends with `/v1` |
| `listModels contains MODEL_NAME` | `ollama pull <model>`; correct name in `.env` |
| `single tool call valid` | Wrong model class — use a coding-tuned model |
| `tool-call validity ≥ 90%` | Model too weak for the loop — upgrade model or hardware |

Do not proceed to Phase 2/3 if this fails.

### 5.2 Phase 2 — Single-agent loop (CLI mode)

**Validates:** the agent fixes a real bug in a container without the control plane.

**When to run:** after Phase 1 passes, or after changing the tool executor, agent loop, or worker image.

```bash
set -a; source .env; set +a
make phase2-demo
```

**Expected tail:**

```
workspace forge-worker-xxxxxxxx at /tmp/forge-wt-xxxxx

status: complete
steps:  8
final:  Fixed the off-by-one in src/paginate.ts. All three tests pass.

=== diff ===
diff --git a/src/paginate.ts b/src/paginate.ts
...
-  const end = start + size - 1;
+  const end = start + size;
...

=== status ===
 M src/paginate.ts
```

**Pass criteria:**

- `status: complete`
- Non-empty diff touching only `src/paginate.ts`
- Checkpoint exists: `ls .forge/checkpoints/run-*.json`
- Fixture repo on disk is unmodified (worktree was used)

**Note:** Phase 2 does not verify the fix automatically. The fixture on disk is unchanged because the run happened in a worktree. To validate the change, apply the diff or inspect the checkpoint's messages.

**Failures:**

| Symptom | Fix |
|---|---|
| `blocked, max steps exhausted` | Re-run smoke test; inspect checkpoint messages |
| `error, model call failed` | Provider unreachable; check adapter log |
| `complete` with empty diff | Model claimed done without editing; inspect `.forge/checkpoints/run-*.json` |
| `path escapes workspace` | Confinement working correctly; model should recover |

Inspect the checkpoint:

```bash
cat .forge/checkpoints/run-*.json | jq '.state.messages[] | {role, toolName, content: (.content[:200])}'
```

### 5.3 Phase 3 — Control plane lifecycle

**Validates:** the full task state machine with Mongo persistence and audit trail.

**When to run:** after §4 is up.

```bash
set -a; source .env; set +a
make phase3-demo
```

**Expected tail:**

```
task: task-xxxxxxxx
run:  run-xxxxxxxx
  [1] CREATED
  [2] PLANNED
  [3] DISPATCHED
  [4] RUNNING
  ...
  [N] REVIEW_PENDING

=== events ===
2026-10-25T...  state.transition  CREATED -> PLANNED  run requested
2026-10-25T...  state.transition  PLANNED -> DISPATCHED  adapter accepted
2026-10-25T...  state.transition  DISPATCHED -> RUNNING  adapter started
2026-10-25T...  checkpoint
...
2026-10-25T...  state.transition  RUNNING -> REVIEW_PENDING  agent reported complete

=== diff ===
diff --git a/src/paginate.ts b/src/paginate.ts
...
PASS
```

**Pass criteria:**

- Final status `REVIEW_PENDING`
- Event chain contains all transitions
- At least one `checkpoint` event before `complete`
- Diff non-empty and contains the fix

**Important caveat:** Phase 3's `RecordComplete` transitions to `REVIEW_PENDING` on any `status: "complete"` from the adapter — it does not verify the diff or run tests. Passing means the lifecycle works, not that the fix is correct. Verify manually:

```bash
curl -s localhost:8080/runs/<runId>/diff | jq -r .diff
```

**Failures:**

| Symptom | Fix |
|---|---|
| Stuck at `CREATED` | Dispatch returned an error; check CP log for `provider not allowed` or worktree provision error |
| Stuck at `DISPATCHED` | Adapter never POSTed first checkpoint; check adapter log |
| `404` on `/tasks/{id}` | Mongo connection mismatch; confirm `MONGODB_URI`/`MONGODB_DATABASE` |
| `provider "openai" is not allowed` | Add to `FORGE_ALLOWED_PROVIDERS` |
| `REVIEW_PENDING` with empty diff | Model claimed done without editing; inspect `lastCheckpoint.messages` |

### 5.4 Phase 3 — Resume after crash

**Validates:** checkpoint-based resumption and stale-run detection.

**When to run:** after `phase3-demo` passes.

```bash
set -a; source .env; set +a
make phase3-resume-demo
```

**What the script does:**

1. Creates a task and dispatches a run
2. Waits for the first checkpoint
3. `pkill -9` the adapter
4. Waits for the stale detector to transition the run to `RESUMABLE`
5. Restarts the adapter
6. POSTs `/runs/{id}/resume`
7. Polls until `REVIEW_PENDING`
8. Prints the final diff

**Expected tail:**

```
checkpoints flowing (step=3). Killing adapter...
waiting for RESUMABLE...
status: RESUMABLE
restarting adapter...
adapter listening on :8081
{"runId":"run-...","status":"RUNNING"}
  [1] RESUMABLE
  [2] RUNNING
  ...
  [N] REVIEW_PENDING

=== final diff ===
diff --git a/src/paginate.ts b/src/paginate.ts
...
PASS
```

**Pass criteria:**

- Status sequence includes `RESUMABLE`
- Resumed run completes with a diff

**Note:** the script requires `pkill` and will kill any `tsx src/server.ts` process. Do not run while other work is in progress on port 8081.

**Failures:**

| Symptom | Fix |
|---|---|
| Never reaches `RESUMABLE` | Watcher not running, or `STALE_AFTER` too high; check CP log for `marked RESUMABLE` |
| `409 run is not resumable` | Resume called too early; wait longer or reduce `STALE_AFTER` |
| `apply checkpoint diff: ...` | Checkpoint diff fails to apply; inspect `lastCheckpoint.workspaceDiff` |
| `no checkpoint to resume from` | Adapter crashed before first checkpoint |

### 5.5 Phase 3 — Invalid-transition guard

**Validates:** the state machine rejects illegal transitions.

After a completed `phase3-demo`, resume the completed run:

```bash
RUN=$(curl -s localhost:8080/runs?limit=1 | jq -r '.[0].id')
curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/runs/$RUN/resume
```

**Pass criteria:** `409`, not `202`.

### 5.6 Full system health

```bash
echo "--- health ---"
curl -s localhost:8080/health; echo
curl -s localhost:8081/health; echo

echo "--- recent tasks ---"
mongosh --quiet forge_ai --eval 'db.tasks.find({}, {requirement:1,status:1,createdAt:1}).sort({createdAt:-1}).limit(5).forEach(t=>print(JSON.stringify(t)))'

echo "--- recent runs ---"
mongosh --quiet forge_ai --eval 'db.workflowRuns.find({}, {status:1,stepCount:1,createdAt:1}).sort({createdAt:-1}).limit(5).forEach(r=>print(JSON.stringify(r)))'
```

---

### 5.7 Phase 8 — Single-child vertical slice

**Validates:** decomposition → child → verification → agent review → integration → combined verification → final review, with a real model.

```bash
make phase8-vertical-slice-demo
```

Uses `tests/evaluations/fixtures/pagination-off-by-one` (its own git repo, so integration branches accumulate there).

**Pass criteria:** the script prints `PASS`; the parent reaches `REVIEW_PENDING`. The head engineer may produce one or two children; both are fine.

### 5.8 Phase 8 — Two dependent children

**Validates:** a multi-node task graph, dependency-ordered dispatch, and two child diffs integrating onto one parent branch.

```bash
make phase8-two-child-slice
# overrides: PROVIDER=claude MODEL=claude-haiku-5-5 CP=http://localhost:8080
```

Each child is reviewed by every reviewer its agent lists (`implementer`: `reviewer`, `test-reviewer`), and the combined change by the head engineer's (`reviewer`, `security-reviewer`). Verdicts: `curl -s localhost:8080/runs/<runId>/reviews | jq`.

Copies `tests/evaluations/fixtures/cart-pricing` into a fresh temp git repo on every run (path printed as `repo:`), so runs are independent. The requirement asks for a discount fix plus a cart module that depends on it.

**Pass criteria** (checked by the script itself, independently of the platform's own gates):

- Parent reaches `REVIEW_PENDING`; at least two children, all `INTEGRATED`
- At least one dependency edge, and every dependent started no earlier than its dependency integrated
- One merge commit per child on the parent branch
- `node --test` passes on the integration worktree, run on the host

### 5.9 Phases 8–9 — Failure paths, cross-review and escalation (no model API)

**Validates:** retry, exhaustion, crash recovery, that a new agent needs only a markdown file, and Phase 9's cross-review (every listed reviewer, consolidated rejections, reviewer timeout, escalation to a human) — deterministically, with the `scripted` provider replaying canned turns from `tests/failure-paths/*.json`. The target keeps its Phase 8 name; it is the regression suite for both phases.

```bash
make phase8-failure-paths                      # all scenarios
bash scripts/phase8-failure-paths.sh exhaustion add-agent   # a subset
```

With `SCRIPTED_PROVIDER_LOG` set (the harness sets it), the scripted provider appends each agent's task prompt to that file, so scenarios can assert on what an agent was told. When runs of one role overlap (parallel subtasks, parallel reviewers), a role's turns can be keyed by a substring of the agent's system prompt or task prompt instead of one shared queue; reviewers are told apart by their agent prompt ("You review tests", ...). See `services/runtime-adapter/src/scripted-provider.ts`.

The harness starts its own control plane and adapter on `:8090` / `:8091` against the `forge_ai_failpaths` database and a private copy of `agents/`, so it can run alongside your dev services. Ports must be free. Docker workspaces, gates, review and integration are all real.

| Scenario | What happens | Pass criteria |
|---|---|---|
| `verify-retry` | First fix fails the test gate; implementer re-invoked with the gate output | Child retried, integrated, fix in the branch, parent `REVIEW_PENDING` |
| `two-reviewers-pass` | Every listed reviewer reviews in order | Child: `reviewer` then `test-reviewer` pass; combined change: `reviewer` then `security-reviewer` pass |
| `split-verdict` | `reviewer` passes, `test-reviewer` rejects | Implementer re-invoked with the test-reviewer's defects, re-verified, round 2 both pass, child integrated |
| `exhaustion` | Fix never improves | Child `BLOCKED` ("verification failed after 2 attempts"), parent `BLOCKED` |
| `crash-resume` | Adapter killed mid-run | Child `RESUMABLE`; `POST /runs/{id}/resume` → 202; resumed run gets the agent's tools and new workspace; parent `REVIEW_PENDING` |
| `add-agent` | `docs-writer.md` added, `POST /agents/reload`, task assigned to it, file deleted, reload | Registered after reload, offered to the head engineer, child runs as `docs-writer@v1` with its tools, unregistered after delete |
| `reviewer-timeout` | `test-reviewer` never reports | Watcher records it as `error` ("timed out"), the round is rejected, round 2 passes |
| `escalate-accept` | Reviewers reject two rounds | Child `ESCALATED` while the parent waits; a decision on a non-escalated task is refused (409); `accept` integrates the child and the parent finishes |
| `escalate-retry` | Same, then `retry` with guidance | The guidance reaches the implementer, round 3 passes, child integrates |
| `parallel-siblings` | Two independent subtasks (fixture `parallel-features`) | Their agents run at the same time from the same integration commit; each is committed on its own branch and merged into the parent (one merge each); the integrated branch passes `node --test` on the host |
| `diamond` | Two in parallel, a third depending on both | The third is claimed only after both integrated and starts from a commit containing both |
| `concurrency-cap` | The same two with `MAX_CONCURRENT_RUNS=1` (the harness restarts its control plane) | They never overlap |
| `overlap-serialized` | One of the two declares no `diffScope` | It gets an implicit dependency on its sibling (`implicitDependsOn`, `child.serialized` event "no diffScope declared"), and they never overlap |
| `parallel-reviewers` | Both child reviewers take 3s | They run at the same time: round 1 takes about 3s, not 6s |
| `cycle-rejected` | The head engineer submits a dependency cycle | Parent `BLOCKED` at decomposition ("dependency cycle: t1 -> t2 -> t1"); no subtasks created |
| `deliver-local` | Approve a task in a repo with no remote | `MERGED`; the target's tip is a merge of its old tip and the parent branch; the checkout is updated and clean; tests pass; worktree removed |
| `deliver-target-moved` | A commit lands on the target before approval | It is merged in and verified again (`targetMoved`), then merged; the target holds both |
| `deliver-dirty` | The target checkout has an uncommitted edit | `BLOCKED` with "uncommitted changes", edit untouched; after cleaning, `POST /tasks/{id}/deliver` → `MERGED`; retrying a delivered task → 409 |
| `deliver-pr` | A GitHub remote (pushes go to a local bare repo) | `PR_OPEN`; branch on origin; stub `gh` saw `pr create --base … --head … --repo github.com/example/app` with the criteria and reviews in the body; PR merged → `MERGED` |
| `deliver-pr-closed` | Same, the PR is closed | `REJECTED` ("closed without merging") |
| `rollback-local` | A delivered local merge, then an unrelated commit on the target, then `POST /tasks/{id}/rollback` | `ROLLED_BACK`; the target is one revert commit on top ("This reverts <merge>"); the fix is gone, the later work stays, the merge is still in history; recorded in `approvals`; a second rollback → 409 |
| `rollback-pr` | A PR merged on the forge (the harness merges it into the bare origin), then a rollback | `REVERT_PR_OPEN`; `<parent>-revert` pushed as one commit on top of the forge's merge; `gh pr create --head <parent>-revert --title Revert: …`; revert merged → `ROLLED_BACK` |
| `feedback-request-changes` | Three threads on r1, then request changes (`budget.maxRevisions: 2`) | Head engineer shown T1/T2 and the diff; one follow-up subtask addresses T1, starts from r1 and replies on it with `reply_to_thread`; T2 and T3 answered by the head engineer (*addressed*); back to `REVIEW_PENDING` as r2 whose changes since r1 are just the fix; T1's rewritten line is outdated in r2, T3 moves down two lines; a second request is 409; approve → `MERGED` |
| `feedback-unhandled-thread` | The follow-up graph ignores both threads | Refused: back to `REVIEW_PENDING` with the unhandled threads named; no subtask added; threads still open |
| `feedback-escalated-parent-retry` | The final review rejects the combined change; a human retries with guidance | Head engineer shown the findings and the guidance; a second subtask fixes it; r1 recorded with final review `-a2` |
| `feedback-budget` | `budget.maxRevisions: 1` | Request changes is 409; approve still works |
| `final-review-escalates` | Final review rejects the combined change | Parent `ESCALATED`; `retry` refused (409); `accept` → `REVIEW_PENDING`; every reviewer's prompt carried the gate results and test output |

**Pass criteria:** the script prints `PASS`. Logs (control plane, adapter) are kept in the `logs:` directory it prints.

---

### 5.10 Phase 10 — Parallel subtasks with a real model

**Validates:** independent subtasks running at the same time with a real model, a dependent one after them, and the time saved.

```bash
make phase10-parallel-slice
# overrides: CP=… PROVIDER=claude MODEL=claude-haiku-5-5
```

Copies `tests/evaluations/fixtures/parallel-features` (suite green at the start) into a fresh repo and asks for three helpers: `taxFor` and `formatPrice` independent, `priceLabel` depending on both. The script prints each subtask's duration, scope and dependencies, then the subtask phase's wall time against the summed subtask time.

**Pass criteria:** parent `REVIEW_PENDING`; at least 3 subtasks, all `INTEGRATED`; at least two ran at the same time (if not, the script prints the `child.serialized` reasons); wall time below the summed subtask time; one merge commit per subtask; `node --test` passes on the integrated branch. First run with Claude: 178s wall for 267s of subtask work (1.5x).

The console's task page shows the same as a timeline: one bar per subtask from claim to integration.

### 5.11 Phase 11 — Delivery and rollback with a real model

```bash
make phase11-delivery-slice
# overrides: CP=… PROVIDER=claude MODEL=claude-haiku-5-5
```

Copies the `cart-pricing` fixture into a fresh repo with no remote, on `main`. Checks the parent branch exists as soon as the task is created and the delivery hint says approval merges locally into `main`; runs the task to `REVIEW_PENDING` (`main` unmoved); approves; then rolls back.

**Pass criteria:** `MERGED` with `main` a merge of its old tip and the parent branch, the checkout clean, `node --test` passing; after the rollback `ROLLED_BACK`, with `main` one revert commit on top of the merge and its tree identical to the fixture's original.

---

## 6. Observing state

### 6.1 Event trail

```bash
curl -s localhost:8080/runs/<runId>/events \
  | jq -r '.[] | "\(.at)  \(.type)  \(.from // "")->\(.to // "")  \(.reason // "")"'
```

Transitions are recorded per task (they carry no run ID), so for a task use:

```bash
mongosh --quiet forge_ai --eval "db.events.find({taskId:'<taskId>', type:'state.transition'}).sort({at:1})
  .forEach(e => print(e.at.toISOString(), e.from, '->', e.to, e.reason))"
```

Children of a parent: `curl -s localhost:8080/tasks | jq '.[] | select(.parentId == "<taskId>")'`.

### 6.2 Latest task with run

```bash
mongosh --quiet forge_ai --eval '
  const t = db.tasks.find().sort({createdAt:-1}).limit(1).next();
  const r = db.workflowRuns.find({taskId: t._id}).sort({createdAt:-1}).limit(1).next();
  printjson({task: {id: t._id, status: t.status}, run: {id: r._id, status: r.status, stepCount: r.stepCount}});
'
```

### 6.3 Agent conversation

The single most useful debugging tool:

```bash
mongosh --quiet forge_ai --eval "
  const r = db.workflowRuns.findOne({_id: '<runId>'});
  r.lastCheckpoint.messages.forEach((m, i) =>
    print(\`[\${i}] \${m.role}\${m.toolName ? '(' + m.toolName + ')' : ''}: \${(m.content||'').slice(0,200)}\`));
"
```

### 6.4 Usage (tokens, wall time)

```bash
curl -s localhost:8080/runs/<runId> | jq '.lastCheckpoint.usage'
```

### 6.5 Diff artifact

```bash
curl -s localhost:8080/runs/<runId>/diff | jq -r .diff
```

A decomposed task's combined change, by revision. A revision is recorded each time the change is put up for human review (the final review passed, or an escalation was accepted), at the parent branch's tip:

```bash
curl -s localhost:8080/tasks/<taskId>/diff | jq -r .diff                       # latest revision
curl -s 'localhost:8080/tasks/<taskId>/diff?revision=1' | jq -r .diff           # a given revision
curl -s 'localhost:8080/tasks/<taskId>/diff?revision=2&since=1' | jq -r .diff   # changes since r1
curl -s 'localhost:8080/tasks/<taskId>/diff?revision=0' | jq -r .diff           # work in progress (branch tip)
curl -s localhost:8080/tasks/<taskId> | jq .revisions
```

The console shows the same as a diff by file and hunk, with line numbers, a revision picker and "compare with" (§4.4). For a task that was not decomposed, `/tasks/<taskId>/diff` serves its latest run's diff.

### 6.5.1 Branches and delivery preview

Creating a task creates its parent branch right away, at the tip of its target branch: `forge/<first words of the requirement>-<task id>`. Each subtask gets its own branch off it when dispatched, `<parent branch>-<first words of the subtask title>`; after review its work is committed there and merged into the parent branch (`integrate(<subtask id>): <title>`). `git log --graph <parent branch>` shows one merge per subtask. The target is `targetBranch` if given, else the branch `baseCommit` names, else the repository's checked-out branch; a detached HEAD or a SHA base needs `targetBranch` (the API answers 400).

```bash
curl -s localhost:8080/tasks/<taskId>/delivery | jq   # what approval will do
```

`mode` is `pull_request` when the repository has a remote (origin, else the first): approval will push the parent branch and open a PR into the target with `gh`, as the account `gh` is logged in with. With no remote it is `local_merge`. `warnings` explain why a PR would fail (`gh` missing or not logged in, a local-path remote). The console shows the same as a *Delivery* panel on a task's page.

Approving delivers (`APPROVED → DELIVERING`, the API answers `DELIVERING`):

- **Local merge** (no remote): if the target moved since the work started, it is merged into the parent branch first and the verification gates run again on the result (`delivery.targetMoved`, a `delivery.target_merged` event). Then a merge commit of the target and the parent branch is created; a clean checkout of the target is fast-forwarded to it (your files update), otherwise the ref moves by compare-and-swap. → `MERGED`, `delivery.mergeCommit`.
- **Pull request** (a remote): the parent branch is pushed (never forced) and `gh pr create --base <target> --head <branch>` opens the PR with the requirement, acceptance criteria, subtasks and final review in its body. → `PR_OPEN`, `delivery.prUrl`. The watcher then follows it: merged on the forge → `MERGED`, closed → `REJECTED`.

Either way the integration worktree is removed; the parent branch stays. A delivery that cannot finish goes to `BLOCKED` with `delivery.error`; fix the cause and retry:

```bash
curl -s localhost:8080/tasks/<taskId> | jq .delivery      # what happened
curl -s -X POST localhost:8080/tasks/<taskId>/deliver     # retry (202); the console has a Retry delivery button
```

A delivered (`MERGED`) task can be rolled back. Rollback reverts; it never rewrites history:

```bash
curl -s -X POST localhost:8080/tasks/<taskId>/rollback -H 'Content-Type: application/json' \
  -d '{"reviewer":"you","rationale":"why"}'               # 202, ROLLING_BACK
curl -s localhost:8080/tasks/<taskId> | jq .delivery.rollback
```

- **Local:** a commit undoing the delivery merge lands on the target the same way the merge did (a dirty checkout is refused). → `ROLLED_BACK`, `rollback.revertCommit`. Later commits on the target are kept.
- **Pull request:** a branch `<parent branch>-revert` is built on the remote's target, pushed, and a revert PR opened. → `REVERT_PR_OPEN`; merged on the forge → `ROLLED_BACK`, closed → back to `MERGED`.

A rollback that cannot finish returns the task to `MERGED` with `rollback.error`; fix the cause and roll back again. The console shows a *Roll back* form on a merged task.

### 6.5.2 Review threads

While a parent task is `REVIEW_PENDING` or `ESCALATED`, a human can comment on lines of its combined change. In the console, click a line number in the combined change to open a thread on that line, or use *Comment on the whole change*; replies, *Resolve* and *Reopen* are on each thread. The name in the Decision box is the author. The final reviewers' findings appear as read-only notes on the line their location names (`path:line`), or in a list when the location is not a line of that diff. The same over HTTP:

```bash
curl -s localhost:8080/tasks/<taskId>/threads | jq               # {threads, notes}
curl -s -X POST localhost:8080/tasks/<taskId>/threads -H 'Content-Type: application/json' \
  -d '{"author":"dana","body":"Why divide here?","anchor":{"path":"src/price.js","side":"new","line":3}}'
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/comments -H 'Content-Type: application/json' -d '{"author":"dana","body":"…"}'
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/resolve  -H 'Content-Type: application/json' -d '{"author":"dana"}'
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/reopen   -H 'Content-Type: application/json' -d '{"author":"dana"}'
```

A thread is on the latest revision unless the request gives `"revision"`. The anchor must be a line that revision's diff shows (`side` `new` for added or unchanged lines, `old` for deleted ones), or the request answers 400. Outside `REVIEW_PENDING`/`ESCALATED` every change answers 409. Only humans write here: an author that is a registered agent's name, or `"kind":"agent"`, answers 403; agents reply through their work (§6.5.3). Approving with unresolved threads is allowed; the console warns first, the approval record lists them (`unresolvedThreads`), and so does the pull request description.

### 6.5.3 Requesting changes

The third decision on a parent's combined change, next to Approve and Reject. It needs at least one unresolved thread (§6.5.2) or a summary:

```bash
curl -s -X POST localhost:8080/tasks/<taskId>/request-changes -H 'Content-Type: application/json' \
  -d '{"reviewer":"dana","summary":"Also cover a 0% discount."}'
```

The task goes `REVIEW_PENDING → DECOMPOSING`, and the head engineer runs again (`run-<taskId>-revise-<n>`) on the parent branch as it stands. It is shown the requirement, the subtasks done so far, your summary, the open threads as T1, T2, … and the combined diff, and submits a follow-up task graph. Each follow-up subtask lists the threads it deals with (`addresses`); a thread that needs no code change can be answered directly (`responses`), which posts the answer as an agent reply and marks the thread *addressed*. A graph that leaves an open thread neither addressed nor answered is refused, and the task goes back to `REVIEW_PENDING` with the reason in its events and in `followUps[].outcome`.

Accepted follow-up subtasks branch from the parent branch's tip, are told about the threads they address, and merge back as in Phase 11. Combined verification and the final review run again (`run-<taskId>-final-review-r2`, …), and the task returns to `REVIEW_PENDING` as the next revision, which the console's "compare with" shows against the last one. The task's `followUps` list each round and its outcome; the request is recorded in its approvals with the threads that were open.

Each follow-up subtask that addresses threads is told about them as T1, T2, … and gets a `reply_to_thread` tool. Its replies are posted as agent comments, marked with the revision being prepared, once its work is integrated, and those threads become *addressed* (a reply to a thread it does not address is ignored, with a `thread.reply_ignored` event).

When the next revision is recorded, each open line thread is placed on it. A thread whose line is unchanged since the revision it was shown on moves to that line's new number (`shownOn`, `anchor`; `origin` keeps where it was opened; a `thread.moved` event). One whose line was changed or removed, or whose file was deleted, is marked *outdated* (`outdated`, `outdatedOn`; a `thread.outdated` event) and keeps its last place and context. The console shows it on that line of its last revision, and on later revisions in a list with the code around it. Resolved threads and general threads stay as they are.

A task can be sent back `maxRevisions - 1` times (`budget.maxRevisions`, else `MAX_REVISIONS`, default 3); after that the request answers 409 and the task can only be approved or rejected. A follow-up must plan at least one subtask; a thread that needs only an answer is answered by the head engineer in `responses`.

### 6.6 Escalated tasks

A task goes `ESCALATED` when its reviewers reject `MAX_REVIEW_ROUNDS` rounds in a row; a parent goes `ESCALATED` as soon as its final review rejects the combined change. A parent waits on an escalated child; nothing proceeds until a human decides.

```bash
# What the reviewers said, per round
curl -s localhost:8080/runs/<runId>/reviews | jq '.[] | {round, reviewer, verdict, summary, defects}'

# Decide: accept (integrate / go to approval), retry (one more round), or reject
curl -s -X POST localhost:8080/tasks/<taskId>/escalation -H 'Content-Type: application/json' \
  -d '{"decision":"retry","reviewer":"<you>","rationale":"why","guidance":"what to change"}'
```

`retry` sends every rejecting reviewer's defects plus your guidance to the implementer; another rejection escalates again. For a parent's combined change, `retry` goes to the head engineer instead, as a follow-up (§6.5.3) with the final reviewers' findings, your guidance and any open threads; its final review runs again as `run-<taskId>-final-review-a2` (`-a3`, …), and it counts against the revision budget. Decisions are recorded in `GET /tasks/<taskId>/approvals`. The console offers the same three decisions on the escalated task's page (§4.4).

### 6.7 Stale runs

```bash
mongosh --quiet forge_ai --eval '
  db.workflowRuns.find({status: "RUNNING", lastHeartbeat: {$lt: new Date(Date.now() - 60000)}})
    .forEach(r => print(r._id, r.lastHeartbeat));
'
```

Non-empty output means the watcher hasn't caught them yet — check the CP log.

---

## 7. Troubleshooting

### 7.1 Go build errors

| Error | Fix |
|---|---|
| `pattern /tasks/{id}: invalid` | Go < 1.22 — upgrade Go |
| `cannot find package "go.mongodb.org/mongo-driver/v2/bson"` | Run `go get go.mongodb.org/mongo-driver/v2 && go mod tidy` inside `services/control-plane` |
| `undefined: Handlers` / `Watcher` / `mktempDir` | Files from the "missing definitions" correction are not saved — see §10 |
| `undefined: uuid` | `go get github.com/google/uuid` |

### 7.2 Adapter errors

| Error | Fix |
|---|---|
| `Cannot find module 'tsx'` | `pnpm install` in `services/runtime-adapter` |
| `MODEL_NAME is not set` | Source `.env` before starting: `set -a; source .env; set +a` |
| `require is not defined` in smoke test | `"type": "module"` in `package.json` **and** top-level `import` (not inline `require`) |

### 7.3 Runtime failures

| Symptom | Fix |
|---|---|
| `Cannot connect to the Docker daemon` | Start Docker Desktop / `systemctl start docker` |
| `docker exec` fails | Confirm `WORKER_IMAGE` matches on disk: `docker images | grep forge-worker` |
| `git worktree add` fails | Fixture is not a git repo — re-run §2.4 |
| Model loops to `max steps` | Reduce task scope; verify smoke test passed; try smaller requirement |
| `429` from OpenAI | Reduce `MAX_AGENT_STEPS`; backoff already applied in `callWithRetry` |
| CP unresponsive during a run | Watcher stall — restart CP; check `MarkStale` for a loop without a `break` |
| Task `BLOCKED`: "agent finished without changing the workspace" | The agent replied without editing (often "I'm blocked"). Read its last messages (§6.3) |
| Task `BLOCKED`: "decomposition: … assigned to … a review agent" | The head engineer gave a subtask to a reviewer or planner. Only work agents (no `submission_tool`) take subtasks; re-dispatch |
| Parent `BLOCKED`: "child … is BLOCKED" | Look at that child's transitions (§6.1); the parent blocks as soon as any child does |
| Task `ESCALATED`: "review rejected the change in N round(s)" or "final review rejected" | Reviewers kept rejecting. Read their verdicts and decide (§6.6); the parent waits meanwhile |
| Review verdict `error`: "reviewer timed out after …" | The reviewer never reported (adapter died or the model call hung). Raise `REVIEWER_TIMEOUT` for slow models; the round already continued |
| Delivery `BLOCKED`: "target branch is checked out with uncommitted changes" | Commit or stash the edits in that checkout, then `POST /tasks/{id}/deliver` |
| Delivery `BLOCKED`: "… moved since the work started and conflicts with it" | The target changed the same lines. Merge the target into the parent branch yourself, resolve, commit, then retry |
| Delivery `BLOCKED`: "verification failed after merging …" | The target's new commits break the combined change; fix on the parent branch and retry |
| Delivery `BLOCKED`: "push … rejected" / "gh pr create: …" | The remote branch has commits Forge did not make (it never forces), or `gh` is not logged in / cannot see the repo. Check the hint's warnings |
| Rollback fails: "the revert conflicts with later changes on the target" | Later commits changed the same lines. Revert by hand (`git revert -m 1 <mergeCommit>`), resolving the conflict |
| Rollback fails: "… is not on <target>" | The delivery's merge commit is not on the target (it was rewritten or reset there); nothing to revert |
| Task stays `DELIVERING` after a control-plane restart | Not resumed yet (Phase 11 gap): set it to `BLOCKED` in Mongo and retry delivery |
| Creating a task answers 400 "detached HEAD" / "is not a branch" / "does not exist" | The target branch could not be resolved. Pass `targetBranch` explicitly |
| Parent `BLOCKED`: "decomposition: dependency cycle: …" | The head engineer's `dependsOn` edges form a loop; re-dispatch |
| Subtask `BLOCKED` by `diff-scope` after a review asked for files outside its scope | Should not happen since 10.4 (agents are told the subtask's scope and siblings). If it does, check the subtask's prompt carries "## Scope of this subtask" |
| Subtasks you expected in parallel run one after another | They declare overlapping or no `diffScope`. See the `child.serialized` event on the waiting subtask |
| Reload fails: "lists reviewer …, which is not a review agent" | Every `reviewers` entry must be an agent with `submission_tool: submit_review`, and not the agent itself |
| Log: `checkpoint … save failed (diff N bytes)` | Diff over Mongo's 16MB limit — usually untracked build output. `node_modules/` and lockfiles are excluded automatically; add others to the repo's `.gitignore` |
| `fatal: not a git repository` inside a container | Workspace created by an old build as a `git worktree`. Current builds use standalone clones; restart the CP |
| `model call failed after N retries` | Adapter log shows the provider error (`[adapter] model call failed`) |

### 7.4 Cross-service debugging

Check logs in this order:

1. **Control plane terminal** — task transitions, watcher, adapter calls
2. **Adapter terminal** — model calls, tool executions, callback failures
3. **`mongosh forge_ai`** — actual persisted state (source of truth)
4. **`docker logs forge-worker-<runId>`** — if container still alive (usually cleaned up)

If the CP log shows no `checkpoint` events during a run, the callback URL is wrong (`CONTROL_PLANE_SELF_URL`) or the adapter cannot reach it.

### 7.5 Port conflicts

| Port | Service | Override |
|---|---|---|
| 8080 | Control plane | `CONTROL_PLANE_ADDR=:8085`; update `CONTROL_PLANE_SELF_URL` |
| 8081 | Adapter | `ADAPTER_PORT=8086`; update `ADAPTER_URL` |
| 8090 / 8091 | Failure-path harness (§5.9) | `CP_PORT=… AD_PORT=… bash scripts/phase8-failure-paths.sh` |
| 27017 | Mongo | `MONGODB_URI` |
| 11434 | Ollama | `OLLAMA_HOST=127.0.0.1:11435 ollama serve`; update `OLLAMA_BASE_URL` |

---

## 8. Reset and teardown

### 8.0 Concurrency

Only subtasks with declared, non-overlapping `diffScope`s run in parallel. Each subtask's implementer and reviewers are told its scope and what its sibling subtasks own, so a reviewer does not demand work a sibling is assigned. At decomposition, independent tasks whose scopes overlap, or that declare none, get an implicit dependency on the earlier one (`implicitDependsOn`, shown in the console as "(overlapping scope)", with a `child.serialized` event giving the reason). Ready subtasks are claimed up to `MAX_CONCURRENT_RUNS` live workspaces, oldest parent first. A claimed subtask's run is `PROVISIONING` while its workspace is cloned and dependencies install (the stale-run watcher ignores it), then `RUNNING`. Children of one parent integrate one at a time: each subtask's branch, which starts at the parent commit it was dispatched from, is merged into the parent branch. A change's reviewers all run at the same time in its container; they only read (no diff capture, `GIT_OPTIONAL_LOCKS=0`), and the round is decided when the last verdict is in.

Run **one control plane per database**: the integration lock is in-process. The failure-path harness uses its own database, so it can run beside your dev control plane.

### 8.0.1 Automatic cleanup

The control plane's reaper runs at startup and every 30s. It removes a run's container and workspace once its task leaves the active states (`DECOMPOSING`, `DISPATCHED`, `RUNNING`, `VERIFYING`, `AGENT_REVIEW`, `INTEGRATING`), and removes this instance's unowned containers and `forge-wt-*` / `forge-child-*` temp directories older than `ORPHAN_CONTAINER_GRACE`. Each control plane only touches resources labeled with its own database (`forge.instance=<MONGODB_DATABASE>`). Parent integration worktrees (`forge-int-*`) are kept: they hold the branch awaiting review.

### 8.1 Restore the fixture repo

Agent workspaces are standalone clones and do not register worktrees. Integration worktrees do; to drop them:

```bash
cd tests/evaluations/fixtures/pagination-off-by-one
git worktree list
git worktree prune
git worktree remove --force <path>    # if it still exists on disk
```

### 8.2 Clear Mongo

```bash
mongosh forge_ai --eval '
  db.tasks.deleteMany({});
  db.workflowRuns.deleteMany({});
  db.events.deleteMany({});
  print("cleared");
'
```

Safe — Phase 3 state is entirely demo-recreatable.

### 8.3 Remove orphaned containers

Normally unnecessary (§8.0). To force it for one instance:

```bash
docker ps -aq --filter "label=forge.instance=forge_ai" | xargs -r docker rm -f
```

### 8.4 Full reset

```bash
docker compose -f infrastructure/compose/mongo.yaml down -v
docker rmi forge-worker:dev
rm -rf .forge

# Re-run §2 from the worker-image step onward
```

---

## 9. Phase status

| Phase | Status | Exit criterion | Evidence |
|---|---|---|---|
| 1. Skeleton + contracts + D0 | Verified | Smoke test passes; D0 closed | `make smoke` (`qwen3-coder:30b`) |
| 2. Single-agent loop, containerized | Verified | Agent edits code in an isolated container | Every §5.7–§5.9 run |
| 3. Control plane + persistence | Verified | Lifecycle to `REVIEW_PENDING`; recovery after crash | §5.9 `crash-resume`; the older `phase3-*` demos are not re-checked |
| 4. Verification gates | Verified | Gates run and block failing changes | §5.9 `verify-retry`, `exhaustion` |
| 5. Planner + Reviewer roles | Reviewer verified; planner not | Reviewer gates integration and sends work back | §5.9 `split-verdict`; the planner path is unexercised |
| 6. Eval suite formalized + console | Console verified; eval harness not | Console drives approval and escalation decisions | Phase 9.4: escalated subtask accepted and parent approved from the UI against a live control plane |
| 7. Agent registry, skills, composer | Verified | A new agent is a markdown file and a reload | §5.9 `add-agent` |
| 8. Head engineer + task graph + integrator | Verified | Decomposition → dependent children → integration → combined verification → final review | §5.7, §5.8 with Claude; §5.9 |
| 10. Parallel execution | Verified | Disjoint subtasks run in parallel up to `MAX_CONCURRENT_RUNS`; overlapping ones are serialized; integration is serialized and 3-way; a change's reviewers run together | §5.10 with Claude (1.5x); §5.9 `parallel-siblings`, `diamond`, `concurrency-cap`, `overlap-serialized`, `cycle-rejected`, `parallel-reviewers`; console timeline |
| 9. Cross-review and escalation | Verified | Every listed reviewer reviews; all must pass; rejections consolidated; repeated rejection escalates to a human, who decides from the API or console | §5.9 `two-reviewers-pass`, `split-verdict`, `reviewer-timeout`, `escalate-accept`, `escalate-retry`, `final-review-escalates`; §5.7–§5.8 with Claude (a Claude `test-reviewer` rejected and passed a rework on its own); console decision (§4.4) |
| 11. Delivery | Verified | The parent branch exists from task creation; subtasks merge into it; approval merges locally or opens a PR with `gh`; rollback reverts | §5.11 with Claude (approve → `MERGED`, roll back → original tree); §5.9 `deliver-*`, `rollback-*` (stub `gh`, local bare origin); console rollback from the UI against a live control plane |
| 12. Human feedback | In progress (12.4) | Revisions of the combined change and a diff viewer; line comment threads and reviewer notes; request changes through a head engineer follow-up; agent replies and threads carried across revisions; then the Claude slice | 12.1: unit tests (per-revision IDs, diff by revision, diff parser); §5.9 `verify-retry` and `final-review-escalates` check the recorded revision and `GET /tasks/{id}/diff`; console diff viewer against a live control plane. 12.2: unit tests (anchors, thread rules, location parsing, reviewer notes); §5.9 `verify-retry`, `deliver-local` and `deliver-pr` check threads, the 409 after delivery, and unresolved threads in the approval and PR body (§6.5.2); console threads against a live control plane. 12.3: unit tests (thread handling in a follow-up graph, final review IDs after escalation retries, revision budget, follow-up prompt, new state edges); §5.9 `feedback-request-changes`, `feedback-unhandled-thread`, `feedback-escalated-parent-retry`, `feedback-budget` (§6.5.3); console request changes against a live control plane. 12.4: unit tests (line relocation through a diff, reply parsing, re-anchoring on a real repo); `feedback-request-changes` also checks the implementer's reply on T1, T1 outdated in r2 and T3 moved two lines; console against a live control plane |

Not covered yet: a real GitHub pull request (the suite uses a stub `gh`); resuming a delivery interrupted by a control-plane restart. The Phase 6 eval harness has not been re-run.

---

## 10. File reference

Files created across Phases 1–3. Missing files cause compile errors.

### Adapter (`services/runtime-adapter/`)

| File | Purpose |
|---|---|
| `src/types.ts` | `ModelProvider` contract |
| `src/openai-compatible.ts` | Shared base for the wire format |
| `src/ollama-provider.ts` | Ollama wrapper |
| `src/openai-provider.ts` | OpenAI wrapper |
| `src/provider-factory.ts` | Env-driven selection + allow-list |
| `src/tools.ts` | Tool schemas |
| `src/workspace.ts` | Container provisioning |
| `src/executor.ts` | Tool execution inside container |
| `src/agent.ts` | The loop (supports resume) |
| `src/checkpoint.ts` | File checkpointer (CLI use only) |
| `src/cli.ts` | `forge run --repo --task` |
| `src/server.ts` | Service mode (`POST /runs`) |
| `scripts/smoke-test.ts` | 7-check model validation |

### Control plane (`services/control-plane/`)

| File | Purpose |
|---|---|
| `internal/controlplane/domain/task.go` | `Task`, `Budget`, `Provenance` |
| `internal/controlplane/domain/run.go` | `WorkflowRun`, `Checkpoint`, `Event` |
| `internal/controlplane/statemachine/task.go` | Transition table |
| `internal/controlplane/statemachine/service.go` | `Transition` with audit |
| `internal/controlplane/store/store.go` | Repository interfaces |
| `internal/controlplane/store/mongo/tasks.go` | Tasks repo |
| `internal/controlplane/store/mongo/runs.go` | Runs repo + `MarkStale` |
| `internal/controlplane/store/mongo/events.go` | Events repo |
| `internal/controlplane/workspace/provision.go` | Docker + worktree |
| `internal/controlplane/adapter/client.go` | Go → TS client |
| `internal/controlplane/api/handlers.go` | `Handlers` struct |
| `internal/controlplane/api/tasks.go` | Task + dispatch handlers |
| `internal/controlplane/api/runs.go` | Run + resume + callback handlers |
| `internal/controlplane/runner/watcher.go` | Stale-run detector |
| `cmd/forge/main.go` | Wiring + graceful shutdown |

### Infrastructure and tests

| File | Purpose |
|---|---|
| `infrastructure/docker/worker/Dockerfile` | Worker image |
| `infrastructure/compose/mongo.yaml` | Local Mongo |
| `scripts/phase3-demo.sh` | Lifecycle demo |
| `scripts/phase3-resume-demo.sh` | Crash-recovery demo |
| `tests/evaluations/0001-pagination-off-by-one.json` | First eval case |
| `tests/evaluations/fixtures/pagination-off-by-one/` | Fixture repo |
| `docs/decisions/0000-stack.md` | DR-001 |
| `scripts/phase8-vertical-slice-demo.sh` | §5.7 single-child slice |
| `scripts/phase8-two-child-slice.sh` | §5.8 two dependent children |
| `scripts/phase8-failure-paths.sh` | §5.9 failure paths and add-an-agent |
| `tests/evaluations/fixtures/cart-pricing/` | Two-module fixture, copied fresh per run |
| `tests/failure-paths/*.json` | Scripted model turns per scenario |
| `services/runtime-adapter/src/scripted-provider.ts` | Test-only model provider |
| `services/control-plane/internal/controlplane/runner/reaper.go` | Container/workspace cleanup |

Disposable on cleanup: `.forge/` (Phase 2 checkpoints), `dist/`, the Mongo database.

---

## 11. Quick reference card

```bash
# ---- setup (once) ----
cp .env.example .env                    # edit MODEL_NAME
make worker-build
(cd tests/evaluations/fixtures/pagination-off-by-one && git init && git add . && git commit -m initial)

# ---- start (3 terminals) ----
mongosh ...                             # verify ping
set -a; source .env; set +a; (cd services/control-plane && go run ./cmd/forge)
set -a; source .env; set +a; (cd services/runtime-adapter && pnpm dev)

# ---- validate ----
make smoke                              # Phase 1: model capable
make phase2-demo                        # Phase 2: CLI loop
make phase3-demo                        # Phase 3: lifecycle
make phase3-resume-demo                 # Phase 3: crash recovery
make phase8-vertical-slice-demo         # Phase 8: single-child slice (real model)
make phase8-two-child-slice             # Phase 8: two dependent children (real model)
make phase8-failure-paths               # retries, crash/resume, add-an-agent, cross-review, escalation, parallelism (no API)
make phase10-parallel-slice             # Phase 10: parallel subtasks + one dependent (real model)

# ---- inspect ----
curl -s localhost:8080/runs/<runId> | jq
curl -s localhost:8080/runs/<runId>/events | jq
curl -s localhost:8080/runs/<runId>/diff | jq -r .diff
curl -s localhost:8080/runs/<runId>/reviews | jq       # every reviewer's verdict per round
curl -s localhost:8080/tasks/<taskId>/children | jq    # a parent's subtasks
curl -s localhost:8080/tasks/<taskId>/events | jq      # a task's events, incl. state transitions
mongosh forge_ai --eval 'db.tasks.find().sort({createdAt:-1}).limit(1).pretty()'
```

---

*Runbook reflects the verified Phase 10 build state (2 Nov 2026). It will be updated as Phases 11–13 land. If a command fails in a way not covered by §7, add it — that's a real gap.*
