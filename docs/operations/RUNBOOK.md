# Forge AI — Operations Runbook

Setup, startup, and validation for the platform at its current build state (Phases 1–13; §9 lists what each phase's evidence is and what isn't covered yet).

**Audience:** whoever is running the platform, not building it.
**Last updated:** 4 Nov 2026 (Phases 8–13 verified end to end; see §5.7–§5.12 and §9).

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

### 2.7 Users and tokens (Phase 13.2)

The API and the console need a signed-in user. Add one with `make`, from the repository's root; it asks for the password without showing it, and Forge doesn't need to be running. The first user becomes an instance admin:

```bash
make user NAME=ceejay                  # role member
make user NAME=rae ROLE=reviewer       # viewer, member or reviewer
make user NAME=sam ADMIN=1             # instance admin
make users                             # list them
```

Sign in on the console with that name and password. A user who isn't an instance admin sees nothing until a project admin adds them on the Project page. The `forge` on your `PATH` may be another program (Foundry has one too), and `services/control-plane/cmd/forge` is a source folder, not a program ("permission denied"), so use the `make` targets or `go run` as below.

For scripts, the password is the first line of stdin:

```bash
cd services/control-plane
echo '<a password of 10+ characters>' | go run ./cmd/forge users add <name>     # first user: instance admin
echo '<password>' | go run ./cmd/forge users add <other> [--admin] [--email a@b]
go run ./cmd/forge users list
go run ./cmd/forge tokens create <name> "my scripts"                            # prints an API token once
```

The console signs in with the name and password (a session cookie). Scripts send the token as `Authorization: Bearer fgt_…`, either with `-H` or once for every `curl` through a private config file:

```bash
export FORGE_TOKEN=fgt_…
export CURL_HOME=$(mktemp -d); printf 'header = "Authorization: Bearer %s"\n' "$FORGE_TOKEN" >"$CURL_HOME/.curlrc"
```

**Projects (13.4).** Every task belongs to a project, and works in one of the project's registered repositories; other paths on the machine are refused. You see only your projects' tasks: anything else answers 404 (not 403, so its ID is not confirmed). Instance admins create projects and see them all; a project's admins add its repositories (the absolute path of a repository's top folder; a repository belongs to one project) and its members, each with a role there: `viewer`, `member`, `reviewer` or `admin`. A project keeps at least one admin. Tasks from before projects are put into a project per repository on the control plane's first start (named after the folder, with every user as a member at their instance role); it does nothing on later starts.

```bash
curl -s -X POST localhost:8080/projects -d '{"name":"shop"}'                                # instance admins → {"id":"prj-…"}
curl -s -X POST localhost:8080/projects/<projectId>/repos -d '{"path":"/Users/you/code/shop","defaultTarget":"main"}'
curl -s -X POST localhost:8080/projects/<projectId>/members -d '{"name":"rae","role":"reviewer"}'
curl -s localhost:8080/projects                                                             # yours, with "myRole"
curl -s localhost:8080/tasks?projectId=<projectId>
```

Creating a task names its project: `POST /tasks {"projectId": …, "repoPath": …}`. The console has a project switcher in the header, picks the repository from the project's list in *New task*, and a *Project* page where project admins manage repositories and members (and instance admins create projects).

**Roles (13.3).** Within a project, your role there decides what you can do. Each user also has an instance role, which covers what belongs to no project (eval results) and is the role given when tasks from before projects are migrated: `viewer` reads; `member` also creates and starts tasks, opens and answers threads, resumes runs; `reviewer` also approves, rejects, requests changes, decides escalations, resolves threads, retries deliveries and rolls back. Instance admins can do all of that and manage users and agents. New users are members unless given `--role`; users from before roles are members. Every decision and comment is recorded as the signed-in user (approvals, threads, follow-ups, rollbacks, task creators, and an `actor` on their events); a `reviewer` or `author` in a request body is ignored.

```bash
echo '<password>' | go run ./cmd/forge users add rae --role reviewer
go run ./cmd/forge users role rae member [--admin|--no-admin]
curl -s -X POST localhost:8080/users/<userId>/role -H 'Content-Type: application/json' -d '{"role":"reviewer"}'   # instance admins
```

A request your role does not allow answers 403 ("this needs the reviewer role"); the console hides what you cannot do.

`GET /auth/me` says who you are; `GET|POST /auth/tokens` and `DELETE /auth/tokens/<id>` list, create and revoke your tokens; instance admins create users with `POST /users`. Without a user, setting `FORGE_BOOTSTRAP_TOKEN` lets you `POST /users` once with it instead.

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
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com/v1` | Adapter | Override for compatible proxies; accepted with or without the trailing `/v1` (the Anthropic SDKs' form, without it, works too). A value already exported in the adapter's shell wins over `.env` |
| `FORGE_ALLOWED_PROVIDERS` | `ollama` | Adapter + CP | Comma-separated allow-list; enabling `openai` / `claude` requires opt-in |
| `MONGODB_URI` | `mongodb://127.0.0.1:27017` | Control plane | Mongo connection |
| `MONGODB_DATABASE` | `forge_ai` | Control plane | Database name |
| `WORKER_IMAGE` | `forge-worker:dev` | Control plane | Container image per run |
| `ADAPTER_URL` | `http://localhost:8081` | Control plane | Adapter base URL |
| `CONTROL_PLANE_ADDR` | `127.0.0.1:8080` | Control plane | HTTP bind address. Localhost by default; a non-loopback address (`:8080`) logs a warning: serve it through a TLS proxy (§6.12) |
| `FORGE_ADAPTER_TOKEN` | — (required) | Both | Shared secret: the adapter accepts `/runs` and cancels only with it. Same value in both services' environment; `openssl rand -hex 32` |
| `FORGE_CALLBACK_SECRET` | random, kept in Mongo | Control plane | Signs each run's callback token (§6.8). Unset, one is generated on first start and stored in the `instance` collection |
| `FORGE_CONSOLE_ORIGINS` | `http://localhost:5173,…:5174` (localhost and 127.0.0.1) | Control plane | Browser origins allowed by CORS, with credentials (the console's session cookie). Others get no CORS headers |
| `FORGE_BOOTSTRAP_TOKEN` | — | Control plane | Acts as an instance admin only while no user exists, to create the first one (`POST /users`). Prefer `forge users add` (§2.7) |
| `FORGE_SESSION_TTL` | `12h` | Control plane | A console session ends after this long unused (and 7 days after sign-in at most) |
| `FORGE_SECURE_COOKIES` | `false` | Control plane | Mark the session cookie `Secure`; set behind HTTPS |
| `FORGE_TASK_MAX_TOKENS` | `0` (none) | Control plane | Token cap for new tasks that set none (§6.10) |
| `FORGE_TASK_MAX_COST_USD` | `0` (none) | Control plane | Cost cap in dollars for new tasks that set none (§6.10) |
| `FORGE_MODEL_PRICES` | — | Control plane | Adds to or overrides the built-in prices: `model=in/out,…`, dollars per million input and output tokens |
| `FORGE_TOKEN` | — | Scripts, `cmd/eval` | An API token (`forge tokens create`) the slices and the eval runner send |
| `FORGE_PROJECT` | the project named `slices` | Slices, `cmd/eval` | The project the slices register their temporary repositories in and create tasks in (`scripts/lib/forge-api.sh`); unset, the slices use (or, with an instance admin's token, create) the project named `slices`. The eval runner needs it, with its fixture registered |
| `CONTROL_PLANE_SELF_URL` | `http://localhost:8080` | Control plane | Callback base URL passed to adapter |
| `STALE_AFTER` | `60s` | Control plane | Heartbeat gap before a run becomes `RESUMABLE` |
| `MAX_AGENT_STEPS` | `20` | Adapter | Per-run step cap (overridable per task) |
| `MODEL_MAX_OUTPUT_TOKENS` | `16384` | Adapter | Room for one model reply. A reply cut off at this gets one more try with double, then the task blocks. Only what the model writes is billed; a limit above the model's own maximum makes its calls fail |
| `MAX_RETRIES` | `2` | Adapter | Model-call retry cap |
| `TASK_TIMEOUT_SECONDS` | `1800` | Adapter | Wall-clock cap |
| `ADAPTER_PORT` | `8081` | Adapter | Adapter HTTP port |
| `ADAPTER_HOST` | `127.0.0.1` | Adapter | Address the adapter listens on; only the control plane calls it |
| `CONTROL_PLANE_URL` | `CONTROL_PLANE_SELF_URL` | Adapter | Where the adapter reports runs. A `callbackUrl` in a run request is ignored |
| `AGENTS_DIR` / `SKILLS_DIR` | `agents` / `skills` | Control plane | Agent and skill markdown, re-read on `POST /agents/reload` |
| `DEFAULT_PROVIDER` / `DEFAULT_MODEL` | `MODEL_PROVIDER`, else `ollama` / `MODEL_NAME` | Control plane | Provenance recorded on child runs (the adapter still uses `MODEL_PROVIDER`) |
| `VERIFICATION_SETUP_TIMEOUT_SECONDS` | `10m` | Control plane | How long installing a workspace's packages may take, a duration despite the name. A large repository can need minutes |
| `VERIFICATION_TEST_TIMEOUT` | `15m` | Control plane | How long the test gate may take |
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

### One click

Double-click **Start Forge.command** in the repository's folder in Finder (or run `make up`). It starts Docker and MongoDB if they aren't running, builds what's missing, starts the adapter, control plane and console in the background, waits until each answers, and opens the console. Closing the Terminal window leaves Forge running. **Stop Forge.command** (or `make down`) stops them.

```bash
scripts/forge.sh status            # what's running
scripts/forge.sh logs adapter      # follow a log: control-plane (default), adapter, console
scripts/forge.sh restart           # pick up code changes
```

| Service | Port | Change it with |
|---|---|---|
| Console | 5174 | `FORGE_CONSOLE_PORT` |
| Control plane | 8085 | `FORGE_CP_PORT` |
| Adapter | 8086 | `FORGE_ADAPTER_PORT` |

These ports win over the port settings in `.env`, which still apply to the manual steps below and to the slices. A port in use by something else stops the start with a message naming it; nothing is killed. If a service fails to come up, the end of its log is shown and whatever that start began is stopped. Everything else comes from `.env`, which must have `FORGE_ADAPTER_TOKEN`. Logs and PIDs are in `.forge/run/`.

Stopping Forge while agents are running leaves their runs to go stale; resume them after the next start with `POST /runs/{id}/resume`.

The steps below do the same by hand.

**Where verification looks.** A project is a folder with a `go.mod` or `package.json`: at the top of the repository or, when nothing above holds it, up to three folders down (not `node_modules`, `vendor`, `dist`, `build`, `target`, `fixtures`, `testdata`, `examples` or hidden folders). Every workspace except the head engineer's installs each project's packages, with pnpm or yarn when it has their lockfile, otherwise npm. The test gate runs each project's `test` script, or `go test`; a project with no test script is skipped. pnpm's packages are kept between workspaces in the user's cache folder (`forge/pnpm-store`).

A failing test blocks the task, including one that already failed before the task. Keep a repository's suite passing, and passing inside a worker's limits (2 CPUs, 2 GB, no network), or every code task on it will block.

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
| `cut-off-reply` | The implementer's first reply hits the output limit | Asked again with a higher limit; the partial reply isn't kept; the task reaches `REVIEW_PENDING` and the cut-off call is in its spend |
| `cut-off-always` | Every reply hits the limit | `BLOCKED` after two tries (16,384, then 32,768 tokens), and the reason says so and names `MODEL_MAX_OUTPUT_TOKENS` |
| `subfolder-tests` | The project is in `app/` and uses pnpm; the first fix is wrong | The test gate runs `pnpm test` in `app/`, fails, the retry passes, `REVIEW_PENDING` |
| `blank-reply` | The implementer stops with no text and no change | `BLOCKED`; the reason says it gave no explanation and how to get a report |
| `deliver-pr-moved` | Same, the target moves on the forge only | Delivery fetches it, merges it in and verifies again; `PR_OPEN`; the local target doesn't move; a new task starts from the forge's tip and its `branch.created` event says why |
| `rollback-local` | A delivered local merge, then an unrelated commit on the target, then `POST /tasks/{id}/rollback` | `ROLLED_BACK`; the target is one revert commit on top ("This reverts <merge>"); the fix is gone, the later work stays, the merge is still in history; recorded in `approvals`; a second rollback → 409 |
| `rollback-pr` | A PR merged on the forge (the harness merges it into the bare origin), then a rollback | `REVERT_PR_OPEN`; `<parent>-revert` pushed as one commit on top of the forge's merge; `gh pr create --head <parent>-revert --title Revert: …`; revert merged → `ROLLED_BACK` |
| `feedback-request-changes` | Three threads on r1, then request changes (`budget.maxRevisions: 2`) | Head engineer shown T1/T2 and the diff; one follow-up subtask addresses T1, starts from r1 and replies on it with `reply_to_thread`; T2 and T3 answered by the head engineer (*addressed*); back to `REVIEW_PENDING` as r2 whose changes since r1 are just the fix; T1's rewritten line is outdated in r2, T3 moves down two lines; a second request is 409; approve → `MERGED` |
| `feedback-unhandled-thread` | The follow-up graph ignores both threads | Refused: back to `REVIEW_PENDING` with the unhandled threads named; no subtask added; threads still open |
| `feedback-escalated-parent-retry` | The final review rejects the combined change; a human retries with guidance | Head engineer shown the findings and the guidance; a second subtask fixes it; r1 recorded with final review `-a2` |
| `feedback-budget` | `budget.maxRevisions: 1` | Request changes is 409; approve still works |
| `budget-tokens` | A task's token cap is reached while its subtask works, as its head engineer finishes, and during its head engineer; each is then raised; a project's cap is reached | The subtask stops before its next model call and it and the parent are `BLOCKED` with the cap named; spend is priced and shown against the cap; resuming is refused (409) until a project admin raises the cap (a member gets 403; a subtask shares its parent's, 400); after raising it, resume finishes the work and the resumed history is not counted twice. A subtask refused before it began resumes from the start; a stopped head engineer's task starts again; a task under review cannot be started again (409). A project over its cap starts no new task, which stays `CREATED` |
| `restart-mid-delivery` | The target moves, so delivery merges it in and verifies again; the control plane is killed (`kill -9`) during that verification | Nothing was delivered at the kill; on restart a `delivery.resumed` event is recorded, the merge is verified again (`verifiedCommit`), and the task is `MERGED` with one merge on the target; another restart changes nothing |
| `sandbox-postinstall` | A subtask adds a `postinstall` hook and a test of its own container; the next subtask's workspace is set up after it is merged | The test passes in verification (non-root, read-only root, writable `/workspace` and `/tmp`, no network); the hook runs in the offline step as a non-root user, cannot write `/etc` or `/usr`, and neither reaches a listener on the host nor resolves DNS; the listener hears only the harness's own control request |
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

### 5.12 Phase 12 — Feedback with a real model

```bash
make phase12-feedback-slice
# overrides: CP=… PROVIDER=claude MODEL=claude-haiku-5-5
```

Copies the `cart-pricing` fixture into a fresh repo with no remote, on `main`, with `budget.maxRevisions: 2`. Runs the fix to `REVIEW_PENDING` as r1; opens a thread on its `return` line asking for an exported `CENTS = 100` constant with a test; requests changes. The head engineer plans a follow-up subtask for the thread, which makes the change and replies with `reply_to_thread`; the task comes back as r2. The slice then resolves the thread and approves.

**Pass criteria:** r2 recorded after a `planned` follow-up; a follow-up subtask that addresses the thread is `INTEGRATED`; r2's parent branch exports `CENTS === 100` and passes `node --test`; r2's changes since r1 introduce `CENTS`; the thread has an agent reply on r2 and is *addressed*, and is shown on r2 or outdated there; after approval `MERGED` with no open thread in the approval, and `main` clean, exporting `CENTS` and passing its tests. If a subtask is escalated or blocked on the way, the slice stops and names it rather than waiting out its timeout.

The same flow from the console: *New task* → wait for review → click the `return` line's number, comment, *Request changes* → when r2 is back, the thread shows the implementer's reply → *Resolve* → *Approve*.

---

### 5.13 Eval suite

The scripted scenarios test Forge's pipeline. This tests how well a model does the work:

```bash
make eval              # every case in tests/evaluations
make eval CASE=0002    # cases whose file name contains this
```

Forge must be running. Each case runs in a project named `evals`, on a fresh copy of its fixture, with the model in `MODEL_PROVIDER` and `MODEL_NAME`; set `EVAL_PROVIDER` and `EVAL_MODEL` in the environment to compare another (`.env`'s `EVAL_` lines aren't used). A case passes when the task reaches `REVIEW_PENDING` and its `checks` hold: `commands` run offline in a worker container on the resulting tree, and every changed file is covered by `onlyChanged`. The agent never sees them. The task is then rejected, so nothing is delivered and its workspaces are released. Results appear on the console's Evals page.

**This spends real money with a paid model:** at most `EVAL_TASK_MAX_COST_USD` (`0.50`) a case, and no new case starts once a run has spent `EVAL_MAX_COST_USD` (`2`). Run it when you change a prompt or a model. The first run creates an API token for the first instance admin, kept in `.forge/run/eval-token`.

To test the runner itself without a model, start a private stack with `MODEL_PROVIDER=scripted MODEL_NAME=scripted FORGE_ALLOWED_PROVIDERS=scripted SCRIPTED_PROVIDER_FILE=$PWD/tests/failure-paths/evals-scripted.json`: every case passes except `0004-add-a-helper`, whose scripted change is wrong on purpose, passes Forge's reviewers, and has to be caught by the case's own checks.

---

## 6. Observing state

Every API call below needs your token (§2.7): add `-H "Authorization: Bearer $FORGE_TOKEN"`, or set up `CURL_HOME` once as shown there.

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
  -d '{"rationale":"why"}'                                # 202, ROLLING_BACK (as you, a reviewer)
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
  -d '{"body":"Why divide here?","anchor":{"path":"src/price.js","side":"new","line":3}}'
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/comments -H 'Content-Type: application/json' -d '{"body":"…"}'
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/resolve
curl -s -X POST localhost:8080/tasks/<taskId>/threads/<threadId>/reopen
```

A thread is on the latest revision unless the request gives `"revision"`. The anchor must be a line that revision's diff shows (`side` `new` for added or unchanged lines, `old` for deleted ones), or the request answers 400. Outside `REVIEW_PENDING`/`ESCALATED` every change answers 409. Comments are the signed-in user's (§2.7): members open and answer threads, reviewers resolve and reopen them, and any `author` in the body is ignored. Agents never sign in; they reply through their work (§6.5.3). Approving with unresolved threads is allowed; the console warns first, the approval record lists them (`unresolvedThreads`), and so does the pull request description.

### 6.5.3 Requesting changes

The third decision on a parent's combined change, next to Approve and Reject. It needs at least one unresolved thread (§6.5.2) or a summary:

```bash
curl -s -X POST localhost:8080/tasks/<taskId>/request-changes -H 'Content-Type: application/json' \
  -d '{"summary":"Also cover a 0% discount."}'
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
  -d '{"decision":"retry","rationale":"why","guidance":"what to change"}'
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

A run with no heartbeat for `STALE_AFTER` is marked `RESUMABLE`, and so is its task. If the agent was in fact finishing (a slow diff capture under load can leave no heartbeat for a while), its completion may arrive moments later. It is then used: the task goes `RESUMABLE → RUNNING → VERIFYING` with a `run.revived` event. If the reaper has already released the run's workspace, the task stays `RESUMABLE`; `POST /runs/<runId>/resume` rebuilds the workspace from the checkpoint the completion saved.

### 6.8 Service trust

The adapter takes runs only from the control plane: `/runs` and `/runs/<id>/cancel` need `Authorization: Bearer $FORGE_ADAPTER_TOKEN` (401 otherwise; `/health` is open). It reports only to `CONTROL_PLANE_URL` (default `CONTROL_PLANE_SELF_URL`), never to a URL a request names.

Each run's checkpoint and completion callbacks need that run's token, `HMAC-SHA256(callback key, "run:" + runId)` in hex, which the control plane hands the adapter with the run. A missing or wrong token answers 401, and a run that is `COMPLETE` answers 409. Retries, resumes and reviewers of a run get the same token. To check a token by hand while debugging:

```bash
printf 'run:%s' <runId> | openssl dgst -sha256 -hmac "$FORGE_CALLBACK_SECRET" | awk '{print $NF}'
```

If the control plane logs `adapter: HTTP 401: FORGE_ADAPTER_TOKEN differs …`, the two services were started with different values.

### 6.9 The container sandbox (Phase 13.5)

Every container Forge starts (agent workspaces, their verification, a parent's combined verification, dependency setup) runs as the control plane's own user (`nobody` when the control plane runs as root), with no Linux capabilities, `no-new-privileges`, and a read-only root filesystem. Only `/workspace` (the task's clone) and a 1 GB `/tmp` are writable; `HOME`, the npm cache and Go's caches live in `/tmp`. Agents and verification have no network.

Dependencies install in two containers. The first fetches packages with the network but runs none of their scripts (`npm install --ignore-scripts`, `go mod download`). The second runs the install scripts, the dependencies' (`npm rebuild`) and the project's own (`preinstall`, `install`, `postinstall`, `prepare`), with no network. After a task's first subtask the repository holds agent-written code, so a hook an agent adds never runs with network access.

To see a container's settings while it runs:

```bash
docker inspect forge-worker-<runId> --format '{{.Config.User}} {{.HostConfig.CapDrop}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.NetworkMode}} {{.HostConfig.SecurityOpt}}'
```

### 6.10 Spend caps (Phase 13.6)

Every agent reports its model usage with each checkpoint. The control plane prices it (`DefaultPrices` in `api/spend.go`, Anthropic's first-party rates, plus `FORGE_MODEL_PRICES`) and keeps one record per agent invocation in the `spend` collection. A model with no price counts towards token caps only; a task's panel says how many tokens that is.

There are two caps, each in tokens (input + output) and in dollars, and 0 means none:

- **A task's**, covering it and its subtasks: `budget.maxTokens` and `budget.maxCostUsd` when it is created, or `FORGE_TASK_MAX_TOKENS` and `FORGE_TASK_MAX_COST_USD` for tasks that set none.
- **A project's**, covering all its tasks, for all time.

When a cap is reached the agent stops before its next model call and the task is `BLOCKED` with the reason, as is its parent. No new agent starts while a cap is reached. The stop comes after a checkpoint, so spend can pass a cap by a step, and by what reviewers running in parallel use meanwhile. A reviewer that is stopped counts as a rejection, never a pass.

```bash
curl -s localhost:8080/tasks/<id>/spend -H "Authorization: Bearer $FORGE_TOKEN" | jq          # the task's and its project's
curl -s localhost:8080/projects/<pid>/spend -H "Authorization: Bearer $FORGE_TOKEN" | jq
# Project admins raise a cap (the top-level task's for subtasks); both fields are set, 0 removes one:
curl -s -X PUT localhost:8080/tasks/<id>/budget -H "Authorization: Bearer $FORGE_TOKEN" -d '{"maxTokens":200000,"maxCostUsd":5}'
curl -s -X PUT localhost:8080/projects/<pid>/budget -H "Authorization: Bearer $FORGE_TOKEN" -d '{"maxCostUsd":50}'
# Then carry on from the checkpoint (or from the start, if the subtask was refused before it began):
curl -s -X POST localhost:8080/runs/run-<subtaskId>/resume -H "Authorization: Bearer $FORGE_TOKEN"
# A task whose head engineer was stopped has no subtasks yet: start it again.
curl -s -X POST localhost:8080/tasks/<id>/runs -H "Authorization: Bearer $FORGE_TOKEN" -H 'Content-Type: application/json' -d '{"provider":"claude","model":"claude-haiku-5-5"}'
```

The console shows the same on a task's page and the *Project* page, with **Save caps** for admins, **Resume** on a stopped subtask, and **Start again** on a task stopped before its subtasks were planned. New tasks can set their caps under *Model and budget*.

A final reviewer or a follow-up head engineer stopped by a cap is not resumed directly. A stopped reviewer rejects, so the round fails or escalates as usual, and a refused follow-up returns the task to its human decision.

Only calls made since 13.6 are counted: usage from before is not in the totals. Prices are a snapshot, so check your provider's bill for what you actually owe.

### 6.11 Restarting during a delivery (Phase 13.6)

At startup the control plane resumes every task left `DELIVERING`, and records a `delivery.resumed` event. Each step can safely run again: a half-done merge into the parent branch is undone and redone, a merge of the target whose verification did not finish is verified (`delivery.verifiedCommit` is the tip that passed), a merge that landed is recognised, and an open pull request is found rather than opened twice. A step that fails blocks the task with its reason, and **Retry delivery** works as usual.

A local merge cut short in your own checkout of the target shows up as uncommitted changes, so the delivery blocks and asks you to clean up. `ROLLING_BACK` is not resumed: a rollback interrupted by a restart stays in that state.

### 6.12 Deploying behind a proxy

Forge has no TLS of its own, and both services bind to loopback by default. To reach it from other machines:

1. Keep the adapter on loopback or a private network. Only the control plane talks to it, and only with `FORGE_ADAPTER_TOKEN`.
2. Put the control plane and the built console behind a reverse proxy that terminates TLS. Set `CONTROL_PLANE_ADDR` to the address the proxy reaches.
3. Set `FORGE_SECURE_COOKIES=true` and `FORGE_CONSOLE_ORIGINS` to the console's public origin. If the console and API are on different sites, the `SameSite=Strict` cookie is not sent, so serve them from one site.
4. Set `FORGE_ADAPTER_TOKEN` and `FORGE_CALLBACK_SECRET` from a secret store. Leave `FORGE_BOOTSTRAP_TOKEN` unset once the first user exists.
5. Run one control plane per database: the scheduler and the delivery resume assume it.

Then set up people and work:

```bash
forge users add <name>                       # the first user is an instance admin
forge users role <name> <viewer|member|reviewer> [--admin|--no-admin]
forge tokens create <user> <token name>      # for scripts: sent as Authorization: Bearer
```

Instance admins create projects on the console's *Project* page. Project admins add its repositories and members there, and set its spend cap (§6.10).

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
| CP exits: "FORGE_ADAPTER_TOKEN is not set"; adapter exits likewise | Add `FORGE_ADAPTER_TOKEN=<openssl rand -hex 32>` to `.env` (both services read it) and restart both |
| API answers 401 "sign in required" | Send a token (`-H "Authorization: Bearer $FORGE_TOKEN"`, §2.7) or sign in to the console. A revoked token, a disabled user, or an expired session all answer 401 |
| The console shows the sign-in page right after signing in | The session cookie is not reaching the API: its origin must be in `FORGE_CONSOLE_ORIGINS`, and the console and API must be on the same site (`localhost` with `localhost`, not `127.0.0.1` with `localhost`) |
| 403 "this needs the reviewer role" (or member) | Your role does not allow it (§2.7). An instance admin can change it: `forge users role <name> reviewer` |
| Setup fails: "dependency install (none network): …" | An install script (a dependency's or the project's `postinstall`) needs the network, e.g. to download a binary; install scripts run offline since 13.5 (§6.9). Vendor or prebuild what it fetches |
| A tool fails with `EROFS` / "read-only file system" in a workspace | Since 13.5 only `/workspace` and `/tmp` are writable (§6.9); point the tool's cache or home at `/tmp` |
| 404 for a task you know exists | You are not a member of its project; a project admin adds you (§2.7) |
| Creating a task: 400 "… is not a repository of project …" | A project admin registers it on the *Project* page or with `POST /projects/<id>/repos` |
| `BLOCKED`: "token budget reached" or "cost budget reached"; a start or resume answers 409 "raise the cap" | A task's or project's spend cap (§6.10). Raise it, then resume the stopped subtask |
| Cost stays $0 while tokens rise | The model has no price: add it with `FORGE_MODEL_PRICES` |
| A slice stops: "could not register … in project …" | The token's user must be an admin of `FORGE_PROJECT` (or an instance admin, for the default `slices` project) |
| A slice stops at once: "FORGE_TOKEN: export FORGE_TOKEN …" | Create a token (`forge tokens create <user> slices`) and export it |
| Runs fail to start: "adapter: HTTP 401" | The two services have different `FORGE_ADAPTER_TOKEN` values; restart both from the same `.env` |
| Adapter log: `callback …: HTTP 401` | The run token did not match: the control plane's callback key changed under a running agent (`FORGE_CALLBACK_SECRET` edited, or the `instance` collection cleared). Resume the run |
| The console shows network errors after an upgrade to 13.1 | Its origin is not in `FORGE_CONSOLE_ORIGINS` (e.g. a dev server on another port); add it |

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
| 6. Eval suite formalized + console | Console verified; eval suite with the scripted model only | Console drives approval and escalation decisions | Phase 9.4: escalated subtask accepted and parent approved from the UI against a live control plane |
| 7. Agent registry, skills, composer | Verified | A new agent is a markdown file and a reload | §5.9 `add-agent` |
| 8. Head engineer + task graph + integrator | Verified | Decomposition → dependent children → integration → combined verification → final review | §5.7, §5.8 with Claude; §5.9 |
| 9. Cross-review and escalation | Verified | Every listed reviewer reviews; all must pass; rejections consolidated; repeated rejection escalates to a human, who decides from the API or console | §5.9 `two-reviewers-pass`, `split-verdict`, `reviewer-timeout`, `escalate-accept`, `escalate-retry`, `final-review-escalates`; §5.7–§5.8 with Claude (a Claude `test-reviewer` rejected and passed a rework on its own); console decision (§4.4) |
| 10. Parallel execution | Verified | Disjoint subtasks run in parallel up to `MAX_CONCURRENT_RUNS`; overlapping ones are serialized; integration is serialized and 3-way; a change's reviewers run together | §5.10 with Claude (1.5x); §5.9 `parallel-siblings`, `diamond`, `concurrency-cap`, `overlap-serialized`, `cycle-rejected`, `parallel-reviewers`; console timeline |
| 11. Delivery | Verified | The parent branch exists from task creation; subtasks merge into it; approval merges locally or opens a PR with `gh`; rollback reverts | §5.11 with Claude (approve → `MERGED`, roll back → original tree); §5.9 `deliver-*`, `rollback-*` (stub `gh`, local bare origin); console rollback from the UI against a live control plane |
| 12. Human feedback | Verified | Revisions of the combined change and a diff viewer; line comment threads and reviewer notes; request changes through a head engineer follow-up; agent replies and threads carried across revisions | §5.12 with Claude (comment → request changes → the agent changes it and replies → r2 → resolve → approve → `MERGED`), and the same flow from the console. 12.1: unit tests (per-revision IDs, diff by revision, diff parser); §5.9 `verify-retry` and `final-review-escalates` check the recorded revision and `GET /tasks/{id}/diff`; console diff viewer against a live control plane. 12.2: unit tests (anchors, thread rules, location parsing, reviewer notes); §5.9 `verify-retry`, `deliver-local` and `deliver-pr` check threads, the 409 after delivery, and unresolved threads in the approval and PR body (§6.5.2); console threads against a live control plane. 12.3: unit tests (thread handling in a follow-up graph, final review IDs after escalation retries, revision budget, follow-up prompt, new state edges); §5.9 `feedback-request-changes`, `feedback-unhandled-thread`, `feedback-escalated-parent-retry`, `feedback-budget` (§6.5.3); console request changes against a live control plane. 12.4: unit tests (line relocation through a diff, reply parsing, re-anchoring on a real repo); `feedback-request-changes` also checks the implementer's reply on T1, T1 outdated in r2 and T3 moved two lines; console against a live control plane. 12.5: §5.12 slice and console run with `claude-haiku-5-5` |
| 13. Hardening | Verified | Service trust, sign-in, roles, projects, sandbox, spend caps, delivery resumed after a restart | Unit tests; §5.9 `sandbox-postinstall`, `budget-tokens`, `restart-mid-delivery` and sign-in, role and project checks throughout; all five §5.7–§5.12 slices with Claude and sign-in on (`claude-haiku-5-5`, $0.09 by Forge's prices); console with Claude: a capped task stopped, cap raised, started again, approved, `MERGED` ($0.0057) |

Real GitHub pull requests were checked once by hand on 4 Nov 2026 with the scripted model, on a private repository: approve → PR opened → merged on GitHub → `MERGED`; roll back → revert PR → merged → `ROLLED_BACK`, with the target's files back as they were; a PR closed without merging → `REJECTED`. The suite still uses a stub `gh`.

Not covered yet: resuming a rollback interrupted by a restart. The eval suite (§5.13) has been run with the scripted model only.

A repository with a remote: Forge fetches the target when creating a task and again before opening its pull request, and never moves your local branch. A local target that's behind doesn't affect the work (also checked on that repository, with `main` moving on GitHub before the task and again during review). The `branch.created` event says which commit a task started from and why. If the local target and the remote's have diverged, creating a task answers 409 until you pull or push. If the fetch fails when creating a task, it starts from the local branch and the event says so; if it fails during delivery, the task is `BLOCKED` and delivery can be retried.

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

*Runbook reflects the verified Phase 13 build state (4 Nov 2026). If a command fails in a way not covered by §7, add it — that's a real gap.*
