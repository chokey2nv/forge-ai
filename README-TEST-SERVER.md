# Three things — Docker is required, the task is stuck, and OpenAI doesn't replace Docker

Short answer first: **Docker is required regardless of which model you use.** OpenAI replaces the *brain*; Docker runs the *hands*. You need both.

---

## 1. Why Docker is required

The agent writes code, runs `npm test`, and edits files. Where does that happen?

- **Not on your host** — an agent that runs `rm -rf ~` would succeed
- **Not in a worktree alone** — worktrees isolate directories, not processes; the agent could still read your SSH keys
- **In a container** — `--network=none`, worktree-only mount, `--memory=2g`, `--pids-limit=256`, no access to `~/.ssh`, no access to your OpenAI key

This is the design decision from Phase 2: "Why not worktrees alone." A model that can run shell commands without process isolation is a security hole. Whether it's qwen or gpt-4o driving, the container is non-negotiable.

The agent's *reasoning* is done by the model (Ollama or OpenAI). The agent's *actions* happen in the container.

**Start Docker Desktop.** That's the fix.

```bash
open -a Docker
# Wait ~30 seconds, then:
docker info | head -3
```

Expected: `Server Version: ...` with no error.

While you're at it, build the worker image (you haven't yet — it requires Docker):

```bash
cd /Users/chijiokeagu/forge-ai
make worker-build
docker images | grep forge-worker
```

---

## 2. The stuck task

You can't retry `task-95864004`. Its state is `DECOMPOSING`, and the state machine doesn't allow `DECOMPOSING → anything useful for a retry`. You have to create a new one.

**Why it happened:** the dispatch code transitions to `DECOMPOSING`, then tries to provision a Docker container. Docker was down, so provisioning failed, so the handler returned a 500 with a plain-text error (which is what broke your `jq` earlier). But it never transitioned the task back to `BLOCKED`. So the task is stranded at `DECOMPOSING`.

This is a **bug in the dispatch code** — worth fixing but not blocking. The fix is one line: on provision failure, transition to `BLOCKED` before returning. Save it for after Test 2 works. For now, just abandon the stuck task.

---

## 3. Switching to OpenAI — configuration

The adapter supports OpenAI. From the Phase 7 code, it reads `MODEL_PROVIDER` from env, not from the dispatch body. So the switch is entirely in `.env`:

### Step 3a — Add your OpenAI key and switch provider

Edit `.env`:

```bash
MODEL_PROVIDER=openai
OPENAI_API_KEY=sk-proj-...                    # your actual key
OPENAI_BASE_URL=https://api.openai.com/v1
MODEL_NAME=gpt-4o-mini                        # cheaper default; upgrade to gpt-4o if needed
FORGE_ALLOWED_PROVIDERS=ollama,openai         # allow both, so you can switch back
```

The `FORGE_ALLOWED_PROVIDERS` needs `openai` added or the control plane will reject the dispatch with `provider not allowed`.

### Step 3b — Restart the adapter

`Ctrl+C` the adapter terminal, then:

```bash
set -a; source .env; set +a
cd services/runtime-adapter
pnpm dev
```

Watch for `adapter listening on :8081`.

Verify the adapter now points at OpenAI:

```bash
# This hits OpenAI's /v1/models via the adapter's config
curl -s localhost:8081/health
# → {"ok":true}
```

If `OPENAI_API_KEY` is wrong, the first real model call will return `401`, visible in the adapter log. You'll see it in the run status as `RunBlocked` with a 401 message.

### Step 3c — Restart the control plane

Also `Ctrl+C` and restart, because it needs the updated `FORGE_ALLOWED_PROVIDERS`:

```bash
set -a; source .env; set +a
cd services/control-plane
go run ./cmd/forge
```

Watch for:
```
agent registry loaded: 5 agents, 5 skills
control-plane listening on :8080 (adapter=http://localhost:8081, worker=forge-worker:dev, allowed=[ollama openai])
```

The `allowed=[ollama openai]` confirms the change took.

### Step 3d — Verify env is actually set

```bash
set -a; source .env; set +a
echo "PROVIDER=[$MODEL_PROVIDER]"
echo "MODEL=[$MODEL_NAME]"
echo "KEY_LEN=[${#OPENAI_API_KEY}]"           # length, not the key itself
echo "ALLOWED=[$FORGE_ALLOWED_PROVIDERS]"
```

Expected:
```
PROVIDER=[openai]
MODEL=[gpt-4o-mini]
KEY_LEN=[51 or whatever your key length is — NOT 0]
ALLOWED=[ollama,openai]
```

If `KEY_LEN=0`, the key didn't source — check for a typo or hidden character.

**Security note:** `.env` is gitignored. If it weren't, you'd be one `git push` away from a leaked key. Confirm it's ignored:

```bash
grep '^.env$' .gitignore
# Should print ".env"
```

If it doesn't, add it now:

```bash
echo ".env" >> .gitignore
```

---

## 4. Model choice for testing with OpenAI

For the head-engineer role specifically, `gpt-4o-mini` might struggle with the decomposition tool call (it sometimes produces malformed `submit_task_graph` args). If the first test run ends `BLOCKED` with "head-engineer produced no tasks" or "invalid task graph JSON," switch to `gpt-4o`:

```bash
MODEL_NAME=gpt-4o
```

The head-engineer is the hardest agent in the system — it has to output a structured DAG, not just code. If you want the highest chance of success on the first try, use `gpt-4o`. Cost is ~$0.03–0.10 for one full run. Worth it for the first test.

Once the pipeline works end-to-end, switch back to `gpt-4o-mini` (or Ollama) and see if it still passes. That's the eval-suite point — measure which model can drive the system at which cost.

---

## 5. Ordered steps to get to a real dispatch

Once you've applied the above:

**Terminal 1 — Mongo (verify):**
```bash
mongosh --quiet --eval 'db.runCommand({ping:1})' mongodb://127.0.0.1:27017/
# → { ok: 1 }
```

**Terminal 2 — Control plane:**
```bash
cd /Users/chijiokeagu/forge-ai
set -a; source .env; set +a
cd services/control-plane
go run ./cmd/forge
# Wait for: "agent registry loaded: 5 agents, 5 skills"
```

**Terminal 3 — Adapter:**
```bash
cd /Users/chijiokeagu/forge-ai
set -a; source .env; set +a
cd services/runtime-adapter
pnpm dev
# Wait for: "adapter listening on :8081"
```

**Terminal 4 — Docker running:**
```bash
open -a Docker
# Wait for the whale icon in the menu bar to stop animating
docker info | grep "Server Version"
# → Server Version: 2x.x.x
```

**Terminal 5 — Health checks:**
```bash
curl -s localhost:8080/health && echo " CP OK"
curl -s localhost:8081/health && echo " ADAPTER OK"
docker images | grep forge-worker && echo " WORKER OK"
curl -s localhost:8080/agents/health | jq '.healthy'
# → true
```

All four must be green.

**Then create a fresh task and dispatch:**

```bash
cd /Users/chijiokeagu/forge-ai
set -a; source .env; set +a
REPO=$(pwd)/tests/evaluations/fixtures/pagination-off-by-one

TASK=$(curl -s -X POST localhost:8080/tasks \
  -H 'Content-Type: application/json' \
  -d "{
    \"requirement\": \"Fix the off-by-one bug in src/paginate.js.\",
    \"acceptance\": [\"paginate([1,2,3,4,5], 1, 2) returns [1,2]\", \"All tests pass\"],
    \"repoPath\": \"$REPO\",
    \"baseCommit\": \"HEAD\",
    \"budget\": {\"maxSteps\": 20, \"maxRetries\": 2, \"wallClockMs\": 600000}
  }" | jq -r .id)

echo "TASK=$TASK"

RESP=$(curl -s -X POST localhost:8080/tasks/$TASK/runs \
  -H 'Content-Type: application/json' \
  -d "{\"provider\": \"$MODEL_PROVIDER\", \"model\": \"$MODEL_NAME\"}")

echo "DISPATCH RESPONSE: $RESP"

RUN=$(echo "$RESP" | jq -r .runId)
echo "RUN=$RUN"
```

If `RUN=run-...` prints and `DISPATCH RESPONSE` is `{"runId":"...","status":"DISPATCHED"}`, dispatch worked. If `DISPATCH RESPONSE` is plain text, that's the error you need to read.

Watch the state:

```bash
for i in $(seq 1 120); do
  S=$(curl -s localhost:8080/tasks/$TASK | jq -r .status)
  echo "[$(date +%H:%M:%S)] $S"
  case "$S" in REVIEW_PENDING|BLOCKED|REJECTED|APPROVED) break ;; esac
  sleep 5
done
```

Expect: `DECOMPOSING → DISPATCHED → (children run) → INTEGRATING → VERIFYING → AGENT_REVIEW → REVIEW_PENDING`. First run with `gpt-4o` typically takes 2–5 minutes.

---

## What to paste back

If it works: the final `for i` output (the state transitions) plus:
```bash
curl -s localhost:8080/runs/$RUN | jq '.lastCheckpoint.workspaceDiff'
```

If it fails: the `DISPATCH RESPONSE` (plain text or JSON), plus the last 20 lines of the control-plane log and the last 20 lines of the adapter log.

One more thing — this Docker bug in dispatch (failed provision doesn't transition to BLOCKED) will bite you every time Docker isn't running. Worth fixing after Test 2 works:

In `DispatchRun` and `handleHeadEngineerDone`, before each `http.Error(w, ..., 500)` after a resource failure, add:

```go
_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "provision failed: "+err.Error())
```

That turns "stuck forever" into "retryable." But don't make that fix now — get one clean end-to-end run first.