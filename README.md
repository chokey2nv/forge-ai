HOW TO RUN 

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

Refuse to run on the wrong provider (this is the point of the allow-list):
MODEL_PROVIDER=openai FORGE_ALLOWED_PROVIDERS=ollama make phase2-demo
# → "provider "openai" is not allowed for this project..."


# Save as `docs/operations/RUNBOOK.md`

Copy everything between the markers into your repo.

<!-- ==================== FILE START ==================== -->

# Forge AI — Operations Runbook

Setup, startup, and validation for the platform at its current build state (Phases 1–3). Phase 4+ sections are marked *Not yet implemented*.

**Audience:** whoever is running the platform, not building it.
**Last updated:** 25 Oct 2026 (Phase 3 complete).

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
| `MODEL_PROVIDER` | `ollama` | Adapter | Provider selection (`ollama` or `openai`) |
| `MODEL_NAME` | *(required)* | Adapter | Model identifier sent to the provider |
| `MODEL_TIMEOUT_MS` | `120000` | Adapter | Per-request timeout |
| `OLLAMA_BASE_URL` | `http://127.0.0.1:11434/v1` | Adapter | Ollama endpoint — note `/v1` suffix |
| `OPENAI_API_KEY` | *(unset)* | Adapter | Required only when `MODEL_PROVIDER=openai` |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Adapter | Override for compatible proxies |
| `FORGE_ALLOWED_PROVIDERS` | `ollama` | Adapter + CP | Comma-separated allow-list; enabling `openai` requires opt-in |
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

### 4.4 Startup checklist

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

## 6. Observing state

### 6.1 Event trail

```bash
curl -s localhost:8080/runs/<runId>/events \
  | jq -r '.[] | "\(.at)  \(.type)  \(.from // "")->\(.to // "")  \(.reason // "")"'
```

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

### 6.6 Stale runs

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
| 8080 | Control plane | `CONTROL_PLANE_ADDR=:8090`; update `CONTROL_PLANE_SELF_URL` |
| 8081 | Adapter | `ADAPTER_PORT=8091`; update `ADAPTER_URL` |
| 27017 | Mongo | `MONGODB_URI` |
| 11434 | Ollama | `OLLAMA_HOST=127.0.0.1:11435 ollama serve`; update `OLLAMA_BASE_URL` |

---

## 8. Reset and teardown

### 8.1 Restore the fixture repo

Worktrees are cleaned up on graceful shutdown. If a run crashed mid-`worktree add`:

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

```bash
docker ps -a --filter "label=forge.run" --format "{{.Names}}" | xargs -r docker rm -f
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

| Phase | Status | Exit criterion |
|---|---|---|
| 1. Skeleton + contracts + D0 | Built | Smoke test passes; D0 closed |
| 2. Single-agent loop, containerized | Built | `make phase2-demo` produces a reviewable diff |
| 3. Control plane + persistence | Built | `make phase3-demo` reaches `REVIEW_PENDING`; `make phase3-resume-demo` recovers after crash |
| 4. Verification gates | Not yet implemented | Non-compliant change blocked by a gate |
| 5. Planner + Reviewer roles | Not yet implemented | Reviewer catches seeded defect |
| 6. Eval suite formalized + console | Not yet implemented | Eval runs on every change; UI for review/approve |
| 7. Parallelism + delivery | Not yet implemented | Two independent runs don't interfere |

**Phase 3 caveat:** `RecordComplete` transitions to `REVIEW_PENDING` on the agent's `status: "complete"` without verifying the diff. Correctness gating is Phase 4's job — that is why the state machine reserves `VERIFYING`.

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

# ---- inspect ----
curl -s localhost:8080/runs/<runId> | jq
curl -s localhost:8080/runs/<runId>/events | jq
curl -s localhost:8080/runs/<runId>/diff | jq -r .diff
mongosh forge_ai --eval 'db.tasks.find().sort({createdAt:-1}).limit(1).pretty()'
```

---

*Runbook reflects Phase 3. Sections marked "Not yet implemented" will be updated as Phases 4–7 land. If a command fails in a way not covered by §7, add it — that's a real gap.*

<!-- ==================== FILE END ==================== -->