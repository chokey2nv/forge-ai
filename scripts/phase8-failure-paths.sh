#!/usr/bin/env bash
# Phase 8 failure paths, end to end and deterministic.
#
# Starts a private control plane and adapter (own ports, own database, own
# reaper scope) with the scripted model provider, then drives each scenario in
# tests/failure-paths/ through the real pipeline: Docker workspaces,
# verification gates, review, integration. No model API is called.
#
#   verify-retry   first fix fails verification, the retry passes
#   two-reviewers-pass  every listed reviewer reviews, child and parent (Phase 9)
#   split-verdict  one reviewer rejects; the implementer gets its defects, round 2 passes
#   exhaustion     verification keeps failing; child and parent block
#   crash-resume   adapter dies mid-run; run goes RESUMABLE and resumes
#   add-agent      a new agent is a markdown file and a reload: no code changes
#   reviewer-timeout  a reviewer never reports; the watcher records an error, round 2 runs
#   escalate-accept   reviewers keep rejecting -> ESCALATED; a human accepts; parent finishes
#   escalate-retry    a human sends it back with guidance; the next round passes
#   final-review-escalates  the combined change is rejected -> parent ESCALATED; accept -> REVIEW_PENDING
#   parallel-siblings  two independent subtasks run at the same time and both integrate (Phase 10)
#   diamond            two run in parallel; a third starts only after both integrated
#   concurrency-cap    MAX_CONCURRENT_RUNS=1: the same two subtasks never overlap
#   overlap-serialized one subtask declares no diffScope, so it waits for its sibling
#   cycle-rejected     a cyclic task graph blocks the parent at decomposition
#   parallel-reviewers two 3s reviewers finish one round in well under 6s (Phase 10.3)
#   deliver-local      no remote: approval merges the parent branch into the target (Phase 11.3)
#   deliver-target-moved  the target advanced: merged in, verified again, then merged
#   deliver-dirty      the target is checked out with edits: blocked; clean it, retry -> merged
#   deliver-pr         a remote: approval pushes and opens a PR; merged on the forge -> MERGED
#   deliver-pr-closed  the PR is closed without merging -> REJECTED
#   deliver-pr-moved   the target moves on the forge only: delivery fetches it, merges it in, verifies; new tasks start there
#   rollback-local     a delivered local merge is reverted by a commit on the target (Phase 11.4)
#   rollback-pr        a merged PR is reverted by a revert PR; merged on the forge -> ROLLED_BACK
#   feedback-request-changes  threads on r1, request changes -> head engineer follow-up -> r2 -> approve -> MERGED (Phase 12.3)
#   feedback-unhandled-thread the follow-up graph leaves a thread unhandled: refused, back to REVIEW_PENDING
#   feedback-escalated-parent-retry  the final review escalates; a human sends it back with guidance -> fixed -> r1
#   feedback-budget    a task budgeted for one revision cannot be sent back
#   shared-deps        packages install once into a read-only volume; the second task doesn't reinstall; the combined run skips repeat tests
#   max-steps-resume   the agent runs out of steps, blocks and says so; resuming gives it more and it finishes
#   resume-twice       blocked again after a resume: the second resume keeps its new container and the saved work
#   container-lost     the container is removed mid-run: the run blocks and says so, the checkpoint survives, resume finishes
#   review-retry-steps review sends back an agent near its step limit: it gets more steps, not an instant stop
#   scope-conflict     the agent reports a criterion it can't meet: the first rejection escalates, naming it
#   verify-escalate    verification fails twice; escalating lets a person send it back with guidance, and it passes
#   combined-escalate  each subtask passes but the combined change fails; escalating and accepting reaches REVIEW_PENDING
#   skip-checks        a person stops the running checks; accepting integrates the subtask, and the parent goes to review without its own
#   no-model-accept    the model refuses every call: nothing retries, and a subtask with saved work can still be accepted
#   long-model-reply   one reply outlasts STALE_AFTER: heartbeats keep the run alive, it isn't marked stale
#   cut-off-reply      the model's reply hits the output limit; it's asked again with a higher one
#   cut-off-always     every reply hits the limit: blocked, and the reason says so
#   blank-reply        the agent stops with no text and no change: blocked, and the reason says so
#   subfolder-tests    the project is in app/ with pnpm: its tests run, a wrong fix is caught, the retry passes
#   sandbox-postinstall an agent adds an install hook; it runs offline, unprivileged, on a read-only root (Phase 13.5)
#
# Usage: scripts/phase8-failure-paths.sh [scenario ...]
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CP_PORT=${CP_PORT:-8090}
AD_PORT=${AD_PORT:-8091}
CP=http://localhost:$CP_PORT
DB=${FAILPATHS_DB:-forge_ai_failpaths}
SCEN="$ROOT/tests/failure-paths"
FIXTURE="$ROOT/tests/evaluations/fixtures/cart-pricing"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/forge-failpaths.XXXXXX")
SCENARIOS=("$@")
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(verify-retry two-reviewers-pass split-verdict exhaustion crash-resume add-agent reviewer-timeout escalate-accept escalate-retry final-review-escalates parallel-siblings diamond concurrency-cap overlap-serialized cycle-rejected parallel-reviewers deliver-local deliver-target-moved deliver-dirty deliver-pr deliver-pr-closed deliver-pr-moved rollback-local rollback-pr feedback-request-changes feedback-unhandled-thread feedback-escalated-parent-retry feedback-budget sandbox-postinstall budget-tokens restart-mid-delivery shared-deps max-steps-resume resume-twice container-lost review-retry-steps scope-conflict verify-escalate combined-escalate skip-checks no-model-accept long-model-reply cut-off-reply cut-off-always blank-reply subfolder-tests)

for p in $CP_PORT $AD_PORT; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then echo "port $p is in use"; exit 2; fi
done

export CONTROL_PLANE_ADDR="127.0.0.1:$CP_PORT" CONTROL_PLANE_SELF_URL="$CP" ADAPTER_URL="http://localhost:$AD_PORT"
# Phase 13.1: the services trust each other through these; fresh for every run.
export FORGE_ADAPTER_TOKEN=$(openssl rand -hex 32) FORGE_CALLBACK_SECRET=$(openssl rand -hex 32)
export FORGE_CONSOLE_ORIGINS="http://localhost:5173"
# Phase 13.2: until the first user exists this acts as an instance admin.
export FORGE_BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
export ADAPTER_PORT=$AD_PORT MONGODB_DATABASE=$DB
export MODEL_PROVIDER=scripted MODEL_NAME=scripted DEFAULT_PROVIDER=scripted DEFAULT_MODEL=scripted
export FORGE_ALLOWED_PROVIDERS=scripted STALE_AFTER=15s MAX_VERIFICATION_ATTEMPTS=2 MAX_REVIEW_ROUNDS=2
# Reviewers answer instantly here; a short timeout lets reviewer-timeout finish quickly.
export REVIEWER_TIMEOUT=8s REVIEW_TIMEOUT_GRACE=2s
export SCRIPTED_PROVIDER_LOG="$WORK/prompts.jsonl" # every agent's task prompt, for assertions
export FORGE_MODEL_PRICES="scripted=10/50" # budget-tokens checks what its turns cost

# A stand-in gh: tests never call the real one (it would act as your account).
mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<'GH'
#!/bin/sh
echo "gh $*" >>"${GH_LOG:-/dev/null}"
case "$1 $2" in
  "auth status") echo "  ✓ Logged in to github.com account forge-test (keyring)"; exit 0 ;;
  "pr create") echo "https://github.com/example/app/pull/7"; exit 0 ;;
  "pr list") echo "[]"; exit 0 ;;
  # The harness plays the forge: it writes OPEN, MERGED or CLOSED to $GH_PR_STATE.
  "pr view") s=$(cat "$GH_PR_STATE" 2>/dev/null || echo OPEN)
    # ...and, once it has merged the PR into the bare origin, the merge commit to $GH_PR_MERGE.
    m=$(cat "$GH_PR_MERGE" 2>/dev/null || echo f0f0f0f0)
    echo "{\"state\":\"$s\",\"mergeCommit\":$([ "$s" = MERGED ] && echo "{\"oid\":\"$m\"}" || echo null)}"; exit 0 ;;
esac
echo "stub gh: unsupported: $*" >&2; exit 1
GH
chmod +x "$WORK/bin/gh"
export FORGE_GH="$WORK/bin/gh" GH_LOG="$WORK/gh.log" GH_PR_STATE="$WORK/pr-state" GH_PR_MERGE="$WORK/pr-merge" PR_POLL_INTERVAL=2s

# A private copy of the agent definitions: add-agent writes to it, never to agents/.
cp -R "$ROOT/agents" "$WORK/agents"
export AGENTS_DIR="$WORK/agents" SKILLS_DIR="$ROOT/skills"

CP_PID="" AD_PID="" BEACON_PID=""
cleanup() {
  [ -n "$BEACON_PID" ] && kill "$BEACON_PID" 2>/dev/null || true
  [ -n "$AD_PID" ] && stop_adapter
  [ -n "$CP_PID" ] && kill "$CP_PID" 2>/dev/null || true
  # Containers are labeled with this instance; nothing else is touched.
  ids=$(docker ps -aq --filter "label=forge.instance=$DB" || true)
  [ -n "$ids" ] && docker rm -f $ids >/dev/null 2>&1 || true
}
trap cleanup EXIT

mongosh --quiet "$DB" --eval 'db.dropDatabase()' >/dev/null

(cd "$ROOT/services/control-plane" && go build -o "$WORK/forge" ./cmd/forge)

wait_http() { for _ in $(seq 1 60); do curl -sf -m1 "$1" >/dev/null 2>&1 && return 0; sleep 1; done; echo "timeout waiting for $1"; return 1; }
start_cp() { # [VAR=value ...] extra environment for this control plane
  (cd "$ROOT/services/control-plane" && exec env "$@" "$WORK/forge") >>"$WORK/cp.log" 2>&1 &
  CP_PID=$!
  wait_http "$CP/health"
}
stop_cp() { kill "$CP_PID" 2>/dev/null || true; wait "$CP_PID" 2>/dev/null || true; CP_PID=""; }
start_cp

FAILED=()
check() { # description, condition result (0 = ok)
  if [ "$2" -eq 0 ]; then echo "    ok   $1"; else echo "    FAIL $1"; FAILED+=("$SC: $1"); fi
}

# Phase 13.2: sign in once. The bootstrap token creates the harness's admin,
# which signs in and makes the API token every later call uses.
SC=sign-in; echo "== $SC"
code() { command curl -s -o "$WORK/code.out" -w '%{http_code}' "$@"; }
check "without credentials the API answers 401; health stays open" \
  "$([ "$(code "$CP/tasks")" = 401 ] && [ "$(code "$CP/health")" = 200 ]; echo $?)"
check "the bootstrap token creates the first user, an instance admin" \
  "$([ "$(code -X POST "$CP/users" -H "Authorization: Bearer $FORGE_BOOTSTRAP_TOKEN" -H 'Content-Type: application/json' -d '{"name":"harness","password":"harness-password"}')" = 201 ] && jq -e '.instanceAdmin' "$WORK/code.out" >/dev/null; echo $?)"
check "and stops working once a user exists" \
  "$([ "$(code "$CP/auth/me" -H "Authorization: Bearer $FORGE_BOOTSTRAP_TOKEN")" = 401 ]; echo $?)"
check "a wrong password is refused" \
  "$([ "$(code -X POST "$CP/auth/login" -H 'Content-Type: application/json' -d '{"name":"harness","password":"not-the-password"}')" = 401 ]; echo $?)"
JAR="$WORK/cookies"
check "signing in sets an HttpOnly, SameSite=Strict session cookie" \
  "$([ "$(code -c "$JAR" -X POST "$CP/auth/login" -H 'Content-Type: application/json' -d '{"name":"harness","password":"harness-password"}')" = 200 ] && grep -q '#HttpOnly_.*forge_session' "$JAR" && command curl -s -D - -o /dev/null -X POST "$CP/auth/login" -H 'Content-Type: application/json' -d '{"name":"harness","password":"harness-password"}' | grep -i '^set-cookie: forge_session' | grep -qi 'samesite=strict'; echo $?)"
FORGE_TOKEN=$(command curl -sf -b "$JAR" -X POST "$CP/auth/tokens" -H 'Content-Type: application/json' -d '{"name":"failure-path harness"}' | jq -r .token)
check "the session makes an API token, which works" \
  "$([[ "$FORGE_TOKEN" == fgt_* ]] && [ "$(code "$CP/auth/me" -H "Authorization: Bearer $FORGE_TOKEN")" = 200 ] && jq -e '.user.name == "harness" and .via == "token"' "$WORK/code.out" >/dev/null; echo $?)"
spare=$(command curl -sf -b "$JAR" -X POST "$CP/auth/tokens" -H 'Content-Type: application/json' -d '{"name":"spare"}')
check "a revoked token stops working" \
  "$([ "$(code -X DELETE "$CP/auth/tokens/$(echo "$spare" | jq -r .id)" -H "Authorization: Bearer $FORGE_TOKEN")" = 204 ] && [ "$(code "$CP/auth/me" -H "Authorization: Bearer $(echo "$spare" | jq -r .token)")" = 401 ]; echo $?)"
check "signing out ends the session" \
  "$([ "$(code -b "$JAR" -X POST "$CP/auth/logout")" = 204 ] && [ "$(code -b "$JAR" "$CP/auth/me")" = 401 ]; echo $?)"
export FORGE_TOKEN
# Every later curl carries the token through its config file; a call that
# sends its own Authorization starts with -q, which skips the file. (A shell
# function wrapper does not work: bash 3.2 exits on its failing return.)
export CURL_HOME="$WORK/curl"; mkdir -p "$CURL_HOME"
printf 'header = "Authorization: Bearer %s"\n' "$FORGE_TOKEN" >"$CURL_HOME/.curlrc"

# Phase 13.3: one user per role, each with its own token.
mkuser() { # name role -> API token
  curl -sf -X POST "$CP/users" -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"password\":\"$1-password-1\",\"role\":\"$2\"}" >/dev/null
  command curl -q -sf -c "$WORK/jar-$1" -X POST "$CP/auth/login" -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"password\":\"$1-password-1\"}" >/dev/null
  command curl -q -sf -b "$WORK/jar-$1" -X POST "$CP/auth/tokens" -H 'Content-Type: application/json' -d '{"name":"harness"}' | jq -r .token
}
VIEWER=$(mkuser vic viewer) MEMBER=$(mkuser mo member) REVIEWER=$(mkuser rae reviewer)
as() { # token curl-args… -> HTTP code; the body lands in $WORK/as.out
  local t=$1; shift
  command curl -q -s -o "$WORK/as.out" -w '%{http_code}' -H "Authorization: Bearer $t" "$@"
}
# Phase 13.4: the harness's project; the role users are its members.
PROJECT=$(curl -sf -X POST "$CP/projects" -H 'Content-Type: application/json' -d '{"name":"harness"}' | jq -r .id)
for m in vic:viewer mo:member rae:reviewer; do
  curl -sf -X POST "$CP/projects/$PROJECT/members" -H 'Content-Type: application/json' -d "{\"name\":\"${m%%:*}\",\"role\":\"${m##*:}\"}" >/dev/null
done
OUTSIDER=$(mkuser otto reviewer) # a reviewer, but of no project
check "users are created with roles; a member cannot create users (403)" \
  "$([[ "$VIEWER" == fgt_* && "$MEMBER" == fgt_* && "$REVIEWER" == fgt_* ]] && [ "$(as "$MEMBER" -X POST "$CP/users" -H 'Content-Type: application/json' -d '{"name":"x","password":"x-password-1"}')" = 403 ] && [ "$(as "$VIEWER" "$CP/auth/me")" = 200 ] && jq -e '.user.role == "viewer"' "$WORK/as.out" >/dev/null; echo $?)"

start_adapter() { # script.json
  (cd "$ROOT/services/runtime-adapter" && SCRIPTED_PROVIDER_FILE="$1" exec npx tsx src/server.ts) >>"$WORK/adapter.log" 2>&1 &
  AD_PID=$!
  wait_http "http://localhost:$AD_PORT/health"
}
stop_adapter() { # npx does not always pass the signal on to node, so stop whatever holds the port
  kill "$AD_PID" 2>/dev/null || true
  lsof -ti "tcp:$AD_PORT" -sTCP:LISTEN | xargs kill 2>/dev/null || true
  wait "$AD_PID" 2>/dev/null || true
  AD_PID=""
  # The next adapter's health check must not reach this one while it exits.
  for _ in $(seq 1 30); do lsof -ti "tcp:$AD_PORT" -sTCP:LISTEN >/dev/null 2>&1 || return 0; sleep 0.5; done
  lsof -ti "tcp:$AD_PORT" -sTCP:LISTEN | xargs kill -9 2>/dev/null || true
}

new_repo() { # [fixture name] (default cart-pricing)
  local repo src="$FIXTURE"; repo=$(mktemp -d "$WORK/repo.XXXXXX")
  [ -n "${1:-}" ] && src="$ROOT/tests/evaluations/fixtures/$1"
  cp -R "$src/." "$repo/"
  git -C "$repo" init -q && git -C "$repo" add -A
  git -C "$repo" -c user.name=forge -c user.email=forge@localhost commit -q -m fixture
  # Phase 13.4: tasks work only in their project's repositories.
  curl -sf -X POST "$CP/projects/$PROJECT/repos" -H 'Content-Type: application/json' -d "{\"path\":\"$repo\"}" >/dev/null
  echo "$repo"
}

create_and_dispatch() { # repo [extra budget fields, e.g. ,"maxRevisions":2] -> task id
  local task
  task=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
    \"requirement\":\"Fix applyDiscount in src/price.js so it applies a percentage discount rounded to cents.\",
    \"acceptance\":[\"applyDiscount(200, 10) returns 180\",\"applyDiscount(19.99, 25) returns 14.99\",\"npm test passes\"],
    \"projectId\":\"$PROJECT\",\"repoPath\":\"$1\",\"baseCommit\":\"HEAD\",
    \"budget\":{\"maxSteps\":10,\"maxRetries\":0,\"wallClockMs\":300000${2:-}}}" | jq -r .id)
  curl -sf -X POST "$CP/tasks/$task/runs" -H 'Content-Type: application/json' \
    -d '{"provider":"scripted","model":"scripted"}' >/dev/null
  echo "$task"
}

status_of() { curl -sf "$CP/tasks/$1" | jq -r .status; }
child_of() { curl -sf "$CP/tasks" | jq -r --arg p "$1" '[.[] | select(.parentId == $p)][0].id // empty'; }

wait_status() { # task "STATE|STATE" seconds
  local s=""
  for _ in $(seq 1 "$3"); do
    s=$(status_of "$1")
    [[ "$s" =~ ^($2)$ ]] && { echo "$s"; return 0; }
    sleep 1
  done
  echo "$s"; return 1
}

reasons() { # task -> transition reasons, oldest first
  mongosh --quiet "$DB" --eval "db.events.find({taskId:'$1', type:'state.transition'}).sort({at:1})
    .forEach(e => print(e.from + ' -> ' + e.to + ': ' + (e.reason || '').slice(0, 160)))"
}

has_reason() { reasons "$1" | grep -qF "$2"; }
verdicts() { # run -> "round reviewer verdict,..." in review order
  curl -sf "$CP/runs/$1/reviews" | jq -r '[.[] | "\(.round) \(.reviewer) \(.verdict)"] | join(",")'
}

run_scenario() {
  local repo task child final
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  child=$(child_of "$task")
  if [ "$SC" = max-steps-resume ]; then
    check "out of steps: the subtask blocks and says so ($final)" "$([ "$final" = BLOCKED ] && has_reason "$child" "max steps (10) exhausted"; echo $?)"
    check "only a project admin changes max steps; a member gets 403" \
      "$([ "$(as "$MEMBER" -X PUT "$CP/tasks/$task/max-steps" -H 'Content-Type: application/json' -d '{"maxSteps":12}')" = 403 ]; echo $?)"
    check "max steps can't be changed on a subtask, or set below 1" \
      "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$CP/tasks/$child/max-steps" -H 'Content-Type: application/json' -d '{"maxSteps":12}')" = 400 ] && [ "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$CP/tasks/$task/max-steps" -H 'Content-Type: application/json' -d '{"maxSteps":0}')" = 400 ]; echo $?)"
    check "an admin changes max steps, on the task and its subtasks" \
      "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$CP/tasks/$task/max-steps" -H 'Content-Type: application/json' -d '{"maxSteps":12}')" = 200 ] && [ "$(curl -sf "$CP/tasks/$task" | jq .budget.maxSteps)" = 12 ] && [ "$(curl -sf "$CP/tasks/$child" | jq .budget.maxSteps)" = 12 ]; echo $?)"
    check "resuming gives it more steps" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")" = 202 ]; echo $?)"
    final=$(wait_status "$task" "REVIEW_PENDING|REJECTED" 300) || true
  fi
  if [ "$SC" = resume-twice ]; then
    local first second
    check "resumed once, it blocks again" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")" = 202 ] && [ "$(wait_status "$task" "REVIEW_PENDING|BLOCKED" 300)" = BLOCKED ]; echo $?)"
    first=$(curl -sf "$CP/runs/run-$child" | jq -r .containerName)
    check "the second resume is accepted" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")" = 202 ]; echo $?)"
    second=$(curl -sf "$CP/runs/run-$child" | jq -r .containerName)
    check "each resume has its own container ($first, $second)" "$([ "$first" != "$second" ]; echo $?)"
    final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  fi
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'

  case "$SC" in
    review-retry-steps)
      check "sent back near its step limit, it finishes ($final)" "$([ "$final" = REVIEW_PENDING ] && ! has_reason "$child" "max steps"; echo $?)"
      check "review rejected it once" "$(has_reason "$child" "review rejected; retrying"; echo $?)"
      ;;
    resume-twice)
      check "and it finishes: the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "the work survived both resumes" \
        "$(curl -sf "$CP/runs/run-$child" | jq -e '.lastCheckpoint.workspaceDiff | contains("Math.round")' >/dev/null; echo $?)"
      ;;
    max-steps-resume)
      check "and it finishes: the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      ;;
    long-model-reply)
      check "the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "the run was never marked stale" "$(! has_reason "$child" "no heartbeat"; echo $?)"
      ;;
    cut-off-reply)
      check "asked again with a higher limit, the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "the cut-off call is spent, the partial reply isn't kept" \
        "$(curl -sf "$CP/tasks/$child/spend" | jq -e '.task.spend.outputTokens >= 4096' >/dev/null && ! curl -sf "$CP/runs/run-$child" | jq -e '.lastCheckpoint.messages[] | select(.content == "A long partial repl")' >/dev/null; echo $?)"
      ;;
    cut-off-always)
      check "parent and subtask BLOCKED ($final, $(status_of "$child"))" "$([ "$final" = BLOCKED ] && [ "$(status_of "$child")" = BLOCKED ]; echo $?)"
      check "the reason says the reply was cut off" "$(has_reason "$child" "cut off even at"; echo $?)"
      ;;
    blank-reply)
      check "parent and subtask BLOCKED ($final, $(status_of "$child"))" "$([ "$final" = BLOCKED ] && [ "$(status_of "$child")" = BLOCKED ]; echo $?)"
      check "the reason isn't blank, and says how to get a report" \
        "$(has_reason "$child" "gave no explanation" && has_reason "$child" "A subtask has to produce a change"; echo $?)"
      ;;
    verify-retry)
      check "the parent branch was created with the task, named from its description" \
        "$(b=$(branch_of "$task"); [[ "$b" == forge/fix-applydiscount-in-src-price-js-so-it-* ]] && [[ "$b" == *"${task#task-}" ]] && git -C "$repo" show-ref --verify --quiet "refs/heads/$b"; echo $?)"
      check "the target branch is the repository's checked-out branch" \
        "$([ "$(curl -sf "$CP/tasks/$task" | jq -r .targetBranch)" = "$(git -C "$repo" symbolic-ref --short HEAD)" ]; echo $?)"
      check "no remote: approval would merge locally" \
        "$(curl -sf "$CP/tasks/$task/delivery" | jq -e '.mode == "local_merge" and (.action | contains("locally"))' >/dev/null; echo $?)"
      git -C "$repo" remote add origin https://github.com/example/app.git
      check "a GitHub remote: approval would open a pull request as the gh account" \
        "$(curl -sf "$CP/tasks/$task/delivery" | jq -e '.mode == "pull_request" and .ghReady and (.action | contains("on github.com/example/app as forge-test"))' >/dev/null; echo $?)"
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "child retried after failed verification" "$(has_reason "$child" "verification failed; retrying"; echo $?)"
      check "child integrated" "$([ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
      check "the subtask keeps the head engineer's reason for its agent" \
        "$([ "$(curl -sf "$CP/tasks/$child" | jq -r .rationale)" = "Single file fix." ]; echo $?)"
      check "the parent's events carry the plan's notes" \
        "$(curl -sf "$CP/tasks/$task/events" | jq -e 'any(.[]; .type == "decomposition.complete" and .reason == "One subtask.")' >/dev/null; echo $?)"
      check "the subtask's work is on its own branch, merged into the parent branch" \
        "$(cb=$(curl -sf "$CP/tasks/$child" | jq -r .branch); git -C "$repo" show "$cb:src/price.js" | grep -q Math.round && git -C "$repo" merge-base --is-ancestor "$cb" "$(branch_of "$task")"; echo $?)"
      check "integrated fix rounds to cents" "$(git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -q Math.round; echo $?)"

      check "revision r1 recorded at the parent branch tip, with its final review" \
        "$(curl -sf "$CP/tasks/$task" | jq -e --arg tip "$(git -C "$repo" rev-parse "$(branch_of "$task")")" --arg run "run-$task-final-review" \
          '.revisions | length == 1 and .[0].n == 1 and .[0].commit == $tip and .[0].finalReviewRunId == $run' >/dev/null; echo $?)"
      check "GET /tasks/{id}/diff serves r1's combined change" \
        "$(curl -sf "$CP/tasks/$task/diff" | jq -e --arg base "$(curl -sf "$CP/tasks/$task" | jq -r .integrationBaseCommit)" \
          '.revision == 1 and .from == $base and (.diff | contains("diff --git a/src/price.js b/src/price.js") and contains("Math.round"))' >/dev/null; echo $?)"
      check "an unknown revision is 404" "$([ "$(curl -s -o /dev/null -w '%{http_code}' "$CP/tasks/$task/diff?revision=2")" = 404 ]; echo $?)"

      # Phase 12.2: threads on r1.
      local line thr
      line=$(git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -n Math.round | head -1 | cut -d: -f1)
      # Phase 12.2 threads, as the member mo (13.3: the body's author is ignored).
      check "a viewer reads the change but cannot comment or decide (403)" \
        "$([ "$(as "$VIEWER" "$CP/tasks/$task/diff")" = 200 ] && [ "$(as "$VIEWER" -X POST "$CP/tasks/$task/threads" -H 'Content-Type: application/json' -d '{"body":"x"}')" = 403 ] && [ "$(as "$VIEWER" -X POST "$CP/tasks/$task/approve" -H 'Content-Type: application/json' -d '{}')" = 403 ]; echo $?)"
      local tid code
      code=$(as "$MEMBER" -X POST "$CP/tasks/$task/threads" -H 'Content-Type: application/json' -d "{\"author\":\"mallory\",\"body\":\"Why divide by 100 here?\",\"anchor\":{\"path\":\"src/price.js\",\"line\":$line}}")
      check "a member opens a thread on the Math.round line of r1, as themself (HTTP $code)" \
        "$([ "$code" = 201 ] && jq -e --arg l "$line" '.revision == 1 and .status == "open" and .anchor.side == "new" and .anchor.line == ($l|tonumber) and (.anchor.lineText | contains("Math.round")) and .comments[0].author == "mo" and (.comments[0].authorId | startswith("usr-"))' "$WORK/as.out" >/dev/null; echo $?)"
      tid=$(jq -r .id "$WORK/as.out")
      check "a line the diff does not show is refused (400)" \
        "$([ "$(as "$MEMBER" -X POST "$CP/tasks/$task/threads" -H 'Content-Type: application/json' -d '{"body":"x","anchor":{"path":"src/price.js","line":999}}')" = 400 ]; echo $?)"
      check "a general thread opens" "$([ "$(as "$MEMBER" -X POST "$CP/tasks/$task/threads" -H 'Content-Type: application/json' -d '{"body":"Please add a test for 0%."}')" = 201 ]; echo $?)"
      check "the member replies, but cannot resolve or approve (403)" \
        "$([ "$(as "$MEMBER" -X POST "$CP/tasks/$task/threads/$tid/comments" -H 'Content-Type: application/json' -d '{"body":"Cents."}')" = 200 ] && [ "$(as "$MEMBER" -X POST "$CP/tasks/$task/threads/$tid/resolve")" = 403 ] && [ "$(as "$MEMBER" -X POST "$CP/tasks/$task/approve" -H 'Content-Type: application/json' -d '{}')" = 403 ]; echo $?)"
      check "a reviewer resolves" "$([ "$(as "$REVIEWER" -X POST "$CP/tasks/$task/threads/$tid/resolve")" = 200 ] && jq -e '.resolvedBy == "rae"' "$WORK/as.out" >/dev/null; echo $?)"
      # Phase 13.1: only the adapter that was given a run's token may report on it.
      local fr="run-$task-final-review"
      check "a forged completion without the run's token is refused (401), even with a user's token" \
        "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/complete" -H 'Content-Type: application/json' -d '{"status":"complete","workspaceDiff":"x"}')" = 401 ] && [ "$(curl -q -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/checkpoint" -H "Authorization: Bearer $(run_token "$fr")" -H 'Content-Type: application/json' -d '{}')" = 401 ]; echo $?)"
      check "the right token on a complete run is refused too (409)" \
        "$([ "$(curl -q -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/$fr/complete" -H "Authorization: Bearer $(run_token "$fr")" -H 'Content-Type: application/json' -d '{}')" = 409 ]; echo $?)"
      start_adapter "$SCEN/$SC.json" # stopped once the scenario settled; nothing is run on it
      check "the adapter refuses runs without its secret (401), health stays open" \
        "$([ "$(curl -q -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$AD_PORT/runs" -H 'Content-Type: application/json' -d '{}')" = 401 ] && [ "$(curl -q -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$AD_PORT/runs" -H 'Authorization: Bearer wrong' -H 'Content-Type: application/json' -d '{}')" = 401 ] && curl -sf "http://localhost:$AD_PORT/health" >/dev/null; echo $?)"
      check "both services listen on loopback only" \
        "$(lsof -nP -iTCP:"$CP_PORT" -sTCP:LISTEN | grep -q '127.0.0.1:' && lsof -nP -iTCP:"$AD_PORT" -sTCP:LISTEN | grep -q '127.0.0.1:' && ! lsof -nP -iTCP:"$CP_PORT" -iTCP:"$AD_PORT" -sTCP:LISTEN | grep -qE '\*:|0\.0\.0\.0'; echo $?)"
      stop_adapter
      check "CORS answers the console's origin only" \
        "$(curl -sI "$CP/tasks" -H 'Origin: http://localhost:5173' | grep -qi '^access-control-allow-origin: http://localhost:5173' && ! curl -sI "$CP/tasks" -H 'Origin: https://evil.example' | grep -qi '^access-control-allow-origin'; echo $?)"
      check "GET threads: one resolved with two comments, one open general" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t "$tid" '(.threads | length == 2) and (.threads[] | select(.id == $t) | .status == "resolved" and .resolvedBy == "rae" and (.comments | length == 2) and .comments[1].author == "mo") and ([.threads[] | select(.anchor == null and .status == "open")] | length == 1) and (.notes | type == "array")' >/dev/null; echo $?)"
      # Phase 13.4: the task is the harness project's; outsiders cannot see it.
      check "the task and its subtask belong to the harness project" \
        "$([ "$(curl -sf "$CP/tasks/$task" | jq -r .projectId)" = "$PROJECT" ] && [ "$(curl -sf "$CP/tasks/$child" | jq -r .projectId)" = "$PROJECT" ]; echo $?)"
      check "a reviewer of no project gets 404 for the task, its run, diff and approval, and an empty list" \
        "$([ "$(as "$OUTSIDER" "$CP/tasks/$task")" = 404 ] && [ "$(as "$OUTSIDER" "$CP/runs/run-$child")" = 404 ] && [ "$(as "$OUTSIDER" "$CP/tasks/$task/diff")" = 404 ] && [ "$(as "$OUTSIDER" -X POST "$CP/tasks/$task/approve" -H 'Content-Type: application/json' -d '{}')" = 404 ] && [ "$(as "$OUTSIDER" "$CP/tasks")" = 200 ] && jq -e 'length == 0' "$WORK/as.out" >/dev/null; echo $?)"
      check "members list the project's tasks; GET /projects shows their role" \
        "$([ "$(as "$MEMBER" "$CP/tasks")" = 200 ] && jq -e --arg t "$task" 'any(.[]; .id == $t)' "$WORK/as.out" >/dev/null && [ "$(as "$MEMBER" "$CP/projects")" = 200 ] && jq -e --arg p "$PROJECT" 'length == 1 and .[0].id == $p and .[0].myRole == "member"' "$WORK/as.out" >/dev/null; echo $?)"
      local stray; stray=$(mktemp -d "$WORK/stray.XXXXXX"); git -C "$stray" init -q; git -C "$stray" -c user.name=x -c user.email=x@x commit -q --allow-empty -m x
      local c1 c2
      c1=$(as "$MEMBER" -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "$(jq -nc --arg p "$PROJECT" --arg r "$stray" '{projectId:$p, requirement:"x", acceptance:["x"], repoPath:$r}')")
      c2=$(as "$MEMBER" -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "$(jq -nc --arg r "$repo" '{projectId:"prj-nope", requirement:"x", acceptance:["x"], repoPath:$r}')")
      check "a task on a repository the project has not registered is refused ($c1), in a project you are not in too ($c2)" \
        "$([ "$c1" = 400 ] && [ "$c2" = 404 ]; echo $?)"
      local other; other=$(curl -sf -X POST "$CP/projects" -H 'Content-Type: application/json' -d '{"name":"other"}' | jq -r .id)
      check "a repository belongs to one project (409); only project admins add repositories and members (403)" \
        "$([ "$(code -X POST "$CP/projects/$other/repos" -H 'Content-Type: application/json' -d "{\"path\":\"$repo\"}")" = 409 ] && [ "$(as "$REVIEWER" -X POST "$CP/projects/$PROJECT/repos" -H 'Content-Type: application/json' -d "{\"path\":\"$stray\"}")" = 403 ] && [ "$(as "$REVIEWER" -X POST "$CP/projects/$PROJECT/members" -H 'Content-Type: application/json' -d '{"name":"otto","role":"viewer"}')" = 403 ]; echo $?)"
      code=$(as "$REVIEWER" -X POST "$CP/tasks/$task/approve" -H 'Content-Type: application/json' -d '{"reviewer":"mallory","rationale":"ok"}')
      check "the reviewer approves; the record and the event name the signed-in reviewer, not the body's (HTTP $code)" \
        "$([ "$code" = 200 ] && curl -sf "$CP/tasks/$task/approvals" | jq -e '.[0].action == "approve" and .[0].reviewer == "rae" and (.[0].reviewerId | startswith("usr-"))' >/dev/null && curl -sf "$CP/tasks/$task/events" | jq -e 'any(.[]; .type == "approval.approve" and .actor == "rae")' >/dev/null; echo $?)"
      ;;
    parallel-reviewers)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "both child reviewers passed in round 1" \
        "$(verdicts "run-$child" | grep -qx '1 reviewer pass,1 test-reviewer pass'; echo $?)"
      local span
      span=$(curl -sf "$CP/runs/run-$child/reviews" | jq '[.[] | select(.round == 1)] | ((map(.finishedAt | sub("\\.[0-9]+"; "") | fromdateiso8601) | max) - (map(.startedAt | sub("\\.[0-9]+"; "") | fromdateiso8601) | min))')
      echo "      round 1 took ${span}s with two 3s reviewers"
      check "the two reviewers ran at the same time (round took <6s)" "$([ -n "$span" ] && [ "$span" -lt 6 ]; echo $?)"
      ;;
    two-reviewers-pass)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "child reviewed by reviewer then test-reviewer, both pass" \
        "$(verdicts "run-$child" | grep -qx '1 reviewer pass,1 test-reviewer pass'; echo $?)"
      check "combined change reviewed by reviewer then security-reviewer, both pass" \
        "$(verdicts "run-$task-final-review" | grep -qx '1 reviewer pass,1 security-reviewer pass'; echo $?)"
      ;;
    split-verdict)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "round 1: reviewer passes, test-reviewer rejects; round 2: both pass" \
        "$(verdicts "run-$child" | grep -qx '1 reviewer pass,1 test-reviewer fail,2 reviewer pass,2 test-reviewer pass'; echo $?)"
      check "child retried after the rejection" "$(has_reason "$child" "review rejected; retrying"; echo $?)"
      check "implementer received the test-reviewer's defects" \
        "$(curl -sf "$CP/runs/run-$child" | jq -e '[.lastCheckpoint.messages[].content] | any(contains("## test-reviewer (fail)") and contains("No test covers a 0% discount"))' >/dev/null; echo $?)"
      check "reworked change was re-verified" "$(reasons "$child" | grep -c "RUNNING -> VERIFYING" | grep -qx 2; echo $?)"
      check "child integrated" "$([ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
      ;;
    reviewer-timeout)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "silent test-reviewer recorded as error; round 2 both pass" \
        "$(verdicts "run-$child" | grep -qx '1 reviewer pass,1 test-reviewer error,2 reviewer pass,2 test-reviewer pass'; echo $?)"
      check "error verdict says it timed out" \
        "$(curl -sf "$CP/runs/run-$child/reviews" | jq -e 'any(.verdict == "error" and (.summary | contains("timed out")))' >/dev/null; echo $?)"
      ;;
    exhaustion)
      check "child blocks after exhausting verification" "$(has_reason "$child" "verification failed after 2 attempts"; echo $?)"
      check "parent blocks because the child did" "$([ "$final" = BLOCKED ] && has_reason "$task" "is BLOCKED"; echo $?)"
      ;;
  esac
}

run_crash_resume() {
  local repo task child run final
  repo=$(new_repo)
  start_adapter "$SCEN/crash-resume-a.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  run="run-$child"
  # Wait for the first checkpoint (the fix is written), then kill the adapter mid-turn.
  for _ in $(seq 1 180); do
    steps=$(curl -sf "$CP/runs/$run" 2>/dev/null | jq -r '.lastCheckpoint.stepCount // 0' 2>/dev/null || true)
    [ "${steps:-0}" -ge 1 ] && break; sleep 1
  done
  stop_adapter
  echo "  adapter killed during $run"
  local s; s=$(wait_status "$child" "RESUMABLE" 60) || true
  check "watcher marks the child RESUMABLE" "$([ "$s" = RESUMABLE ]; echo $?)"

  start_adapter "$SCEN/crash-resume-b.json"
  # An earlier resume that died part-way must not stop this one.
  mongosh --quiet "$DB" --eval "db.workflowRuns.updateOne({_id: '$run'}, {\$set: {status: 'PROVISIONING'}})" >/dev/null
  local code; code=$(curl -s -o "$WORK/resume.out" -w '%{http_code}' -X POST "$CP/runs/$run/resume")
  check "resume accepted (HTTP $code: $(head -c 120 "$WORK/resume.out"))" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  check "the resumed agent is told it was interrupted" \
    "$(curl -sf "$CP/runs/$run" | jq -e '[.lastCheckpoint.messages[] | select(.role == "user" and (.content | contains("You were interrupted")))] | length == 1' >/dev/null; echo $?)"
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'
  check "parent reaches REVIEW_PENDING after resume" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  check "resumed run was given the agent's tools" "$(grep -E "runId=$run-r[0-9]+ .*tools=[1-9]" "$WORK/adapter.log" >/dev/null; echo $?)"
  check "run points at the resumed workspace" "$(curl -sf "$CP/runs/$run" | jq -e '.containerName | test("-r[0-9]+$")' >/dev/null; echo $?)"
}

run_container_lost() {
  local repo task child final container
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 240); do
    child=$(child_of "$task")
    [ -n "$child" ] && curl -sf "$CP/runs/run-$child" 2>/dev/null | jq -e '.lastCheckpoint.workspaceDiff // "" | contains("Math.round")' >/dev/null && break
    sleep 1
  done
  container=$(curl -sf "$CP/runs/run-$child" | jq -r .containerName)
  docker rm -f "$container" >/dev/null
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  check "the subtask blocks ($final, $(status_of "$child"))" "$([ "$(status_of "$child")" = BLOCKED ]; echo $?)"
  check "the reason says the container was removed, not that the agent changed nothing" \
    "$(has_reason "$child" "container was removed" && ! has_reason "$child" "without changing the workspace"; echo $?)"
  check "git_status reported the missing container, not a clean tree" \
    "$(curl -sf "$CP/runs/run-$child" | jq -e '[.lastCheckpoint.messages[] | select(.toolName == "git_status")] | length > 0 and all(.content | contains("No such container"))' >/dev/null; echo $?)"
  check "the checkpoint kept the saved work" \
    "$(curl -sf "$CP/runs/run-$child" | jq -e '.lastCheckpoint.workspaceDiff | contains("Math.round")' >/dev/null; echo $?)"
  check "resume is accepted" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'
  check "restored from the checkpoint, the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
}

run_scope_conflict() {
  local repo task child s final code
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  s=$(wait_status "$child" "ESCALATED|BLOCKED|INTEGRATED|REJECTED" 300) || true
  check "the first rejection escalates ($s)" \
    "$([ "$s" = ESCALATED ] && has_reason "$child" "review rejected the change in 1 round(s)"; echo $?)"
  check "the reason names the criterion and what it needs" \
    "$([ "$(event_count "$child" state.transition "Shows per-run result rows .needs: seed data")" -ge 1 ]; echo $?)"
  code=$(decide "$child" accept "")
  check "accepting it continues (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'
  check "parent reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
}

run_verify_escalate() {
  local repo task child s final code guidance="Round to cents with Math.round."
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  s=$(wait_status "$child" "ESCALATED|BLOCKED|INTEGRATED|REJECTED" 300) || true
  check "the subtask blocks after failing verification ($s)" "$([ "$s" = BLOCKED ] && has_reason "$child" "verification failed after 2 attempts"; echo $?)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/escalate")
  check "a parent blocked by its subtask can't be escalated itself (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
  check "a viewer can't escalate (403)" "$([ "$(as "$VIEWER" -X POST "$CP/tasks/$child/escalate")" = 403 ]; echo $?)"
  check "a member escalates (HTTP 202)" "$([ "$(as "$MEMBER" -X POST "$CP/tasks/$child/escalate")" = 202 ]; echo $?)"
  check "the subtask is ESCALATED, naming the failed gate" "$([ "$(status_of "$child")" = ESCALATED ] && has_reason "$child" "verification failed (test); escalated by"; echo $?)"
  check "the parent waits on it instead of being blocked" "$([ "$(status_of "$task")" = DISPATCHED ]; echo $?)"
  check "escalating twice is refused (409)" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$child/escalate")" = 409 ]; echo $?)"
  code=$(decide "$child" retry "$guidance")
  check "send back accepted (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'
  check "the agent was told what failed, and the guidance" \
    "$(curl -sf "$CP/runs/run-$child" | jq -e --arg g "$guidance" '[.lastCheckpoint.messages[].content] | any(contains("failed verification") and contains("test: FAILED") and contains($g))' >/dev/null; echo $?)"
  check "it passes, and no review round was used" \
    "$([ "$final" = REVIEW_PENDING ] && verdicts "run-$child" | grep -q '^1 reviewer pass'; echo $?)"
}

run_combined_escalate() {
  local repo task s code final
  repo=$(new_repo parallel-features)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  s=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 400) || true
  check "each subtask passes, the combined change fails ($s)" "$([ "$s" = BLOCKED ] && has_reason "$task" "combined verification failed"; echo $?)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/escalate")
  check "the parent escalates (HTTP $code)" "$([ "$code" = 202 ] && [ "$(status_of "$task")" = ESCALATED ]; echo $?)"
  code=$(decide "$task" accept "")
  check "accepting is taken (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  final=$(status_of "$task")
  stop_adapter
  echo "  parent $task -> $final"
  reasons "$task" | sed 's/^/      /'
  check "the change goes up for human review as a revision ($final)" \
    "$([ "$final" = REVIEW_PENDING ] && [ "$(curl -sf "$CP/tasks/$task" | jq '.revisions | length')" = 1 ]; echo $?)"
}

run_skip_checks() {
  local repo task child s code final
  repo=$(new_repo)
  mkdir -p "$repo/tests"
  cat > "$repo/tests/slow.test.js" <<'SLOW'
const { test } = require("node:test");
test("slow", () => new Promise((r) => setTimeout(r, 120000)));
SLOW
  git -C "$repo" add -A && git -C "$repo" -c user.name=forge -c user.email=forge@localhost commit -q -m slow
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  s=$(wait_status "$child" "VERIFYING" 200) || true
  check "the subtask's checks are running ($s)" "$([ "$s" = VERIFYING ]; echo $?)"
  check "a running agent can't be escalated: the parent has subtasks in progress (409)" "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/escalate")" = 409 ]; echo $?)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$child/escalate")
  check "stopping them is accepted (HTTP $code)" "$([ "$code" = 202 ] && [ "$(status_of "$child")" = ESCALATED ]; echo $?)"
  code=$(decide "$child" accept "")
  check "accepting integrates the subtask (HTTP $code)" "$([ "$code" = 202 ] && [ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
  s=$(wait_status "$task" "VERIFYING" 60) || true
  check "the parent's combined check starts ($s)" "$([ "$s" = VERIFYING ]; echo $?)"
  sleep 3
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/escalate")
  check "stopping the parent's is accepted too (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  code=$(decide "$task" accept "")
  final=$(status_of "$task")
  check "accepting puts it up for human review (HTTP $code, $final)" "$([ "$code" = 202 ] && [ "$final" = REVIEW_PENDING ]; echo $?)"
  sleep 8
  s=$(status_of "$task")
  code=$(mongosh --quiet "$DB" --eval "print(db.verificationRuns.countDocuments({taskId:{\$in:['$task','$child']}}))")
  check "the stopped checks don't change anything later ($s, $code results kept)" "$([ "$s" = REVIEW_PENDING ] && [ "$code" = 0 ]; echo $?)"
  check "their containers are gone" "$([ -z "$(docker ps -q --filter "name=forge-worker-$task")" ] && [ -z "$(docker ps -q --filter "name=forge-worker-$child")" ]; echo $?)"
  stop_adapter
  reasons "$task" | sed 's/^/      /'
}

run_no_model_accept() {
  local repo task child s code final
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  s=$(wait_status "$child" "ESCALATED|BLOCKED|INTEGRATED|REJECTED" 200) || true
  check "out of steps, it blocks with saved work ($s)" "$([ "$s" = BLOCKED ]; echo $?)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$child/escalate")
  check "any blocked subtask with saved work can be escalated (HTTP $code)" "$([ "$code" = 202 ] && has_reason "$child" "max steps (10) exhausted; escalated by"; echo $?)"
  code=$(decide "$child" accept "")
  check "accepting integrates it without a model (HTTP $code)" "$([ "$code" = 202 ] && [ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|ESCALATED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  check "the reviewers can't run, so the parent escalates and says why ($final)" \
    "$([ "$final" = ESCALATED ] && [ "$(event_count "$task" state.transition "could not give a verdict")" -ge 1 ]; echo $?)"
}

run_add_agent() {
  local repo task child final code
  code=$(curl -s -o /dev/null -w '%{http_code}' "$CP/agents/docs-writer")
  check "docs-writer is not registered yet (HTTP $code)" "$([ "$code" = 404 ]; echo $?)"

  cat >"$WORK/agents/docs-writer.md" <<'AGENT'
---
name: docs-writer
description: Makes small, well-explained code and documentation changes.
version: 1
skills:
  - generic-coding
tools:
  - read_file
  - list_dir
  - search
  - write_file
  - apply_patch
  - run_command
  - git_status
  - git_diff
reviewers:
  - reviewer
---

You are a careful engineer who explains every change you make.
AGENT
  code=$(curl -s -o "$WORK/reload.out" -w '%{http_code}' -X POST "$CP/agents/reload")
  check "reload accepts the new file (HTTP $code: $(head -c 120 "$WORK/reload.out"))" "$([ "$code" = 200 ]; echo $?)"
  check "docs-writer is registered after reload" \
    "$(curl -sf "$CP/agents/docs-writer" | jq -e '.name == "docs-writer" and (.reviewers == ["reviewer"])' >/dev/null; echo $?)"

  repo=$(new_repo)
  start_adapter "$SCEN/add-agent.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  child=$(child_of "$task")
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'

  local he_prompt
  he_prompt=$(mongosh --quiet "$DB" --eval "const t=db.tasks.findOne({_id:'$task'}); const r=db.workflowRuns.findOne({_id:t.latestRunId}); print(r.lastCheckpoint.messages[1].content)")
  check "head engineer is offered docs-writer" "$(grep -qF -- '**docs-writer**' <<<"$he_prompt"; echo $?)"
  check "child is assigned to docs-writer" "$(curl -sf "$CP/tasks/$child" | jq -e '.assignedAgent == "docs-writer"' >/dev/null; echo $?)"
  check "child ran as docs-writer with its tools" "$(grep -E "runId=run-$child role=docs-writer .*tools=8" "$WORK/adapter.log" >/dev/null; echo $?)"
  check "run provenance records docs-writer@v1" \
    "$(curl -sf "$CP/runs/run-$child" | jq -e '.provenance.agentDefinition == "docs-writer@v1"' >/dev/null; echo $?)"
  check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"

  rm "$WORK/agents/docs-writer.md"
  curl -sf -X POST "$CP/agents/reload" >/dev/null
  code=$(curl -s -o /dev/null -w '%{http_code}' "$CP/agents/docs-writer")
  check "docs-writer is gone after deleting the file and reloading (HTTP $code)" "$([ "$code" = 404 ]; echo $?)"
}

decide() { # task decision guidance -> HTTP code
  curl -s -o "$WORK/decide.out" -w '%{http_code}' -X POST "$CP/tasks/$1/escalation" -H 'Content-Type: application/json' \
    -d "{\"decision\":\"$2\",\"reviewer\":\"harness\",\"rationale\":\"scenario $SC\",\"guidance\":\"$3\"}"
}

run_escalation() { # accept|retry
  local repo task child final s code guidance="Keep the rounding: the acceptance criteria require it."
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  for _ in $(seq 1 120); do child=$(child_of "$task"); [ -n "$child" ] && break; sleep 1; done
  s=$(wait_status "$child" "ESCALATED|BLOCKED|INTEGRATED|REJECTED" 300) || true
  check "child escalates after 2 rejected rounds ($s)" \
    "$([ "$s" = ESCALATED ] && has_reason "$child" "review rejected the change in 2 round(s)"; echo $?)"
  check "parent waits on the escalated child instead of blocking" "$([ "$(status_of "$task")" = DISPATCHED ]; echo $?)"
  code=$(decide "$task" accept "")
  check "decision on a task that is not escalated is refused (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"

  code=$(decide "$child" "$1" "$guidance")
  check "$1 decision accepted (HTTP $code: $(head -c 100 "$WORK/decide.out"))" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'

  check "decision recorded" \
    "$(curl -sf "$CP/tasks/$child/approvals" | jq -e --arg a "escalation-$1" 'any(.action == $a and .reviewer == "harness")' >/dev/null; echo $?)"
  check "child integrated" "$([ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
  check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  if [ "$1" = retry ]; then
    check "implementer received the human's guidance" \
      "$(curl -sf "$CP/runs/run-$child" | jq -e --arg g "$guidance" '[.lastCheckpoint.messages[].content] | any(contains($g))' >/dev/null; echo $?)"
    check "round 3 ran after the retry and passed" \
      "$(verdicts "run-$child" | grep -q '3 reviewer pass,3 test-reviewer pass$'; echo $?)"
  fi
}

run_final_review_escalates() {
  local repo task code final
  repo=$(new_repo)
  : >"$WORK/prompts.jsonl"
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "ESCALATED|REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  check "parent escalates when the final review rejects ($final)" \
    "$([ "$final" = ESCALATED ] && has_reason "$task" "final review rejected: reviewer: fail, security-reviewer: pass"; echo $?)"
  code=$(request_changes "$task" '{"reviewer":"harness","summary":"x"}')
  check "changes cannot be requested while ESCALATED (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
  code=$(decide "$task" accept "")
  check "accept decision accepted (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 60) || true
  stop_adapter
  check "parent reaches REVIEW_PENDING after accept ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  check "accepting the escalation records r1 with the escalated final review" \
    "$(curl -sf "$CP/tasks/$task" | jq -e --arg run "run-$task-final-review" '.revisions | length == 1 and .[0].finalReviewRunId == $run' >/dev/null; echo $?)"
  # Reviewers cannot run tests; the platform's gate results must be in their prompt.
  check "child reviewers were shown the gate results and test output" \
    "$(jq -e -s '[.[] | select(.role == "reviewer" and (.task | contains("Task requirement:\nFix applyDiscount\n")))] | length == 2 and all(.task | contains("Verification (run by the platform") and contains("- test: passed") and contains("# pass 3"))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
  check "final reviewers were shown the combined verification" \
    "$(jq -e -s '[.[] | select(.role == "reviewer")][2:] | length == 2 and all(.task | contains("Verification (run by the platform") and contains("- test: skipped (the same code already passed its tests"))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
}

# Epoch ms of a task's first FROM -> TO transition, or empty.
at() {
  mongosh --quiet "$DB" --eval "const e = db.events.find({taskId:'$1', type:'state.transition', from:'$2', to:'$3'}).sort({at:1}).limit(1).toArray()[0]; print(e ? e.at.getTime() : '')"
}
event_count() { # task type reason-substring -> number of matching events
  local q
  q=$(printf 'print(db.events.countDocuments({taskId: "%s", type: "%s", reason: {$regex: "%s"}}))' "$1" "$2" "$3")
  mongosh --quiet "$DB" --eval "$q"
}
branch_of() { curl -sf "$CP/tasks/$1" | jq -r .integrationBranch; }
child_titled() { curl -sf "$CP/tasks/$1/children" | jq -r --arg t "$2" '.[] | select(.title == $t) | .id'; }
run_base() { curl -sf "$CP/runs/run-$1" | jq -r .baseCommit; }

run_parallel() { # siblings | diamond | cap
  local repo task final a b c n
  repo=$(new_repo parallel-features)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED" 300) || true
  stop_adapter
  a=$(child_titled "$task" "Add taxFor"); b=$(child_titled "$task" "Add formatPrice")
  echo "  parent $task -> $final"
  for x in $(curl -sf "$CP/tasks/$task/children" | jq -r '.[].id'); do
    echo "      $(curl -sf "$CP/tasks/$x" | jq -r '"\(.title): \(.status)"')"
  done
  check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"

  # Agent windows (RUNNING until the agent reports) and claim-to-integration windows.
  local ra rb va vb ca cb ia ib
  ra=$(at "$a" DISPATCHED RUNNING); va=$(at "$a" RUNNING VERIFYING)
  rb=$(at "$b" DISPATCHED RUNNING); vb=$(at "$b" RUNNING VERIFYING)
  ca=$(at "$a" CREATED DISPATCHED); ia=$(at "$a" AGENT_REVIEW INTEGRATED)
  cb=$(at "$b" CREATED DISPATCHED); ib=$(at "$b" AGENT_REVIEW INTEGRATED)
  echo "      agent windows: taxFor ${ra}-${va}, formatPrice ${rb}-${vb}"
  if [ "$1" = serialized ]; then
    check "the subtask without a scope waits for its sibling (implicit dependency)" \
      "$(curl -sf "$CP/tasks/$b" | jq -e --arg a "$a" '.implicitDependsOn == [$a] and ((.dependsOn // []) | length == 0)' >/dev/null; echo $?)"
    check "the reason is recorded" "$(event_count "$b" child.serialized "no diffScope" | grep -qx 1; echo $?)"
    check "they never overlap" \
      "$([ -n "$ia" ] && [ -n "$cb" ] && [ "$cb" -ge "$ia" ]; echo $?)"
  elif [ "$1" = cap ]; then
    check "with MAX_CONCURRENT_RUNS=1 the subtasks never overlap" \
      "$([ -n "$ia" ] && [ -n "$ib" ] && { [ "$cb" -ge "$ia" ] || [ "$ca" -ge "$ib" ]; }; echo $?)"
  else
    check "independent subtasks' agents ran at the same time" \
      "$([ -n "$va" ] && [ -n "$vb" ] && [ "$ra" -lt "$vb" ] && [ "$rb" -lt "$va" ]; echo $?)"
    check "the implementer was told its scope and its sibling" \
      "$(jq -e -s 'any(.[]; .role == "implementer" and (.task | startswith("Add taxFor\n") and contains("may change only: src/tax.js, tests/tax.test.js") and contains("- Add formatPrice (src/format.js, tests/format.test.js;")))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
    check "reviewers were told not to fail it for a sibling's work" \
      "$(jq -e -s 'any(.[]; .role == "reviewer" and (.task | contains("Task requirement:\nAdd taxFor\n") and contains("Do not fail it for work a sibling subtask owns")))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
    check "both started from the same integration commit" \
      "$([ "$(run_base "$a")" = "$(run_base "$b")" ] && [ "$(run_base "$a")" = "$(curl -sf "$CP/tasks/$task" | jq -r .integrationBaseCommit)" ]; echo $?)"
  fi
  if [ "$1" = diamond ]; then
    c=$(child_titled "$task" "Add priceLabel")
    check "dependent starts only after both dependencies integrated" \
      "$(cc=$(at "$c" CREATED DISPATCHED); [ -n "$cc" ] && [ "$cc" -ge "$ia" ] && [ "$cc" -ge "$ib" ]; echo $?)"
    check "dependent started from a commit that includes both" \
      "$(git -C "$repo" merge-base --is-ancestor "$(git -C "$repo" log --format=%H --grep "integrate($b)" "$(branch_of "$task")")" "$(run_base "$c")" && git -C "$repo" merge-base --is-ancestor "$(git -C "$repo" log --format=%H --grep "integrate($a)" "$(branch_of "$task")")" "$(run_base "$c")"; echo $?)"
  fi
  n=$(curl -sf "$CP/tasks/$task/children" | jq length)
  check "one merge commit per subtask on the parent branch ($n)" \
    "$([ "$(git -C "$repo" rev-list --count --merges "$(curl -sf "$CP/tasks/$task" | jq -r .integrationBaseCommit)..$(branch_of "$task")")" -eq "$n" ]; echo $?)"
  local bad=""
  for x in $(curl -sf "$CP/tasks/$task/children" | jq -r '.[].id'); do
    cb=$(curl -sf "$CP/tasks/$x" | jq -r .branch)
    [[ "$cb" == "$(branch_of "$task")-"* ]] && [ "$(git -C "$repo" rev-list --count "$(run_base "$x")..$cb" 2>/dev/null)" = 1 ] || bad="$bad $cb"
  done
  check "each subtask has its own branch off the parent, holding its one commit" "$([ -z "$bad" ]; echo $?)"
  check "integrated branch passes its tests on the host" \
    "$(cd "$(curl -sf "$CP/tasks/$task" | jq -r .integrationWorktree)" && node --test >/dev/null 2>&1; echo $?)"
}

post_thread() { # task json -> HTTP code followed by the body
  local code; code=$(curl -s -o "$WORK/thread.out" -w '%{http_code}' -X POST "$CP/tasks/$1/threads" -H 'Content-Type: application/json' -d "$2")
  echo "$code$(cat "$WORK/thread.out")"
}
thread_post() { # task "tid/action" json -> HTTP code
  curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$1/threads/$2" -H 'Content-Type: application/json' -d "$3"
}

approve() { # task -> HTTP code
  curl -s -o "$WORK/approve.out" -w '%{http_code}' -X POST "$CP/tasks/$1/approve" -H 'Content-Type: application/json' \
    -d "{\"reviewer\":\"harness\",\"rationale\":\"scenario $SC\"}"
}
delivery_verifications() { # task -> passed verification runs made during delivery
  mongosh --quiet "$DB" --eval "$(printf 'print(db.verificationRuns.countDocuments({_id: {$regex: "^vr-%s-delivery-"}, passed: true}))' "$1")"
}
delivery_of() { curl -sf "$CP/tasks/$1" | jq -c '.delivery // {}'; }

# Plays the forge merging a PR: merge --no-ff head into base in the bare origin.
forge_merge() { # origin base head -> merge commit
  local c; c=$(mktemp -d "$WORK/forge.XXXXXX")
  git clone -q -b "$2" "$1" "$c" && git -C "$c" fetch -q origin "$3"
  git -C "$c" -c user.name=github -c user.email=gh@localhost merge -q --no-ff -m "Merge pull request #7" FETCH_HEAD
  git -C "$c" push -q origin "$2" && git -C "$c" rev-parse HEAD
}

run_delivery() { # local | moved | dirty | pr | pr-closed, then [rollback]
  local repo task final target branch wt d origin
  repo=$(new_repo)
  target=$(git -C "$repo" symbolic-ref --short HEAD)
  if [[ "$1" == pr* ]]; then
    # A GitHub-looking remote whose git traffic (fetch and push) goes to a
    # local bare repository: nothing reaches github.com.
    origin="$WORK/origin-$RANDOM.git"; git init -q --bare "$origin"
    git -C "$repo" remote add origin https://github.com/example/app.git
    git -C "$repo" config "url.$origin.insteadOf" https://github.com/example/app.git
    git -C "$repo" push -q origin "$(git -C "$repo" symbolic-ref --short HEAD)"
    echo OPEN >"$GH_PR_STATE"; : >"$GH_LOG"; rm -f "$GH_PR_MERGE"
  fi
  start_adapter "$SCEN/two-reviewers-pass.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  check "parent reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  branch=$(branch_of "$task"); wt=$(curl -sf "$CP/tasks/$task" | jq -r .integrationWorktree)

  case "$1" in
    moved) # someone lands an unrelated change on the target meanwhile
      echo "# Notes" >"$repo/NOTES.md"; git -C "$repo" add NOTES.md
      git -C "$repo" -c user.name=someone -c user.email=s@localhost commit -q -m "notes" ;;
    dirty) echo "// my edit" >>"$repo/src/price.js" ;;
    pr-moved) # a PR merges on the forge; the local target knows nothing of it
      local c; c=$(mktemp -d "$WORK/forge.XXXXXX")
      git clone -q -b "$target" "$origin" "$c"; echo "# Notes" >"$c/NOTES.md"; git -C "$c" add NOTES.md
      git -C "$c" -c user.name=github -c user.email=gh@localhost commit -q -m "merged on the forge"
      git -C "$c" push -q origin "$target" ;;
  esac
  local before; before=$(git -C "$repo" rev-parse "$target")
  [[ "$1" == pr ]] && post_thread "$task" '{"author":"dana","body":"Follow up on rounding mode later."}' >/dev/null

  check "approval answers DELIVERING" "$([ "$(approve "$task")" = 200 ] && jq -e '.status == "DELIVERING"' "$WORK/approve.out" >/dev/null; echo $?)"
  final=$(wait_status "$task" "MERGED|PR_OPEN|BLOCKED|REJECTED" 180) || true
  d=$(delivery_of "$task"); echo "  delivery: $d"
  reasons "$task" | tail -4 | sed 's/^/      /'

  case "$1" in
    local|moved)
      check "task MERGED ($final)" "$([ "$final" = MERGED ]; echo $?)"
      check "the target's tip is a merge of its old tip and the parent branch" \
        "$(p=$(git -C "$repo" rev-list --parents -n1 "$target"); [ "$(echo "$p" | wc -w)" -eq 3 ] && [ "$(echo "$p" | awk '{print $2}')" = "$before" ] && [ "$(echo "$p" | awk '{print $3}')" = "$(git -C "$repo" rev-parse "$branch")" ]; echo $?)"
      check "the delivery records that merge commit" "$(echo "$d" | jq -e --arg m "$(git -C "$repo" rev-parse "$target")" '.mode == "local_merge" and .mergeCommit == $m' >/dev/null; echo $?)"
      check "the checked-out target has the fix and is clean" \
        "$(grep -q Math.round "$repo/src/price.js" && [ -z "$(git -C "$repo" status --porcelain --untracked-files=no)" ]; echo $?)"
      check "tests pass on the target" "$(cd "$repo" && node --test >/dev/null 2>&1; echo $?)"
      check "no threads once the task is MERGED (409)" "$([ "$(post_thread "$task" '{"author":"dana","body":"late"}' | head -c3)" = 409 ]; echo $?)"
      check "the integration worktree is removed" "$([ ! -e "$wt" ] && [ "$(curl -sf "$CP/tasks/$task" | jq -r '.integrationWorktree // ""')" = "" ]; echo $?)"
      if [ "$1" = moved ]; then
        check "the moved target was merged in and verified again" \
          "$(echo "$d" | jq -e '.targetMoved' >/dev/null && [ "$(event_count "$task" delivery.target_merged "Merge $target")" -ge 1 ] && [ "$(delivery_verifications "$task")" -ge 1 ]; echo $?)"
        check "the target holds both changes" "$([ -f "$repo/NOTES.md" ] && git -C "$repo" merge-base --is-ancestor "$before" "$target"; echo $?)"
      fi
      ;;
    dirty)
      check "delivery blocks on the dirty checkout ($final)" \
        "$([ "$final" = BLOCKED ] && echo "$d" | jq -e '.error | contains("uncommitted changes") and contains("retry")' >/dev/null; echo $?)"
      check "the target and the user's edit are untouched" \
        "$([ "$(git -C "$repo" rev-parse "$target")" = "$before" ] && grep -q "my edit" "$repo/src/price.js"; echo $?)"
      git -C "$repo" checkout -q -- src/price.js
      code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/deliver")
      final=$(wait_status "$task" "MERGED|BLOCKED" 120) || true
      check "after cleaning up, retry delivers ($code, $final)" "$([ "$code" = 202 ] && [ "$final" = MERGED ]; echo $?)"
      check "the retried delivery has no error and merged into the target" \
        "$(delivery_of "$task" | jq -e '.error == null and .mergeCommit != null' >/dev/null && git -C "$repo" merge-base --is-ancestor "$branch" "$target"; echo $?)"
      code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/deliver")
      check "a delivered task cannot be retried (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
      ;;
    pr-moved)
      local tip; tip=$(git -C "$origin" rev-parse "$target")
      check "task PR_OPEN ($final)" "$([ "$final" = PR_OPEN ]; echo $?)"
      check "the pushed branch holds the forge's new commit" "$(git -C "$origin" merge-base --is-ancestor "$tip" "$branch"; echo $?)"
      check "it was merged in and verified again" \
        "$(echo "$d" | jq -e '.targetMoved' >/dev/null && [ "$(event_count "$task" delivery.target_merged "Merge $target")" -ge 1 ] && [ "$(delivery_verifications "$task")" -ge 1 ]; echo $?)"
      check "the local target did not move" "$([ "$(git -C "$repo" rev-parse "$target")" = "$before" ]; echo $?)"
      local next; next=$(curl -s -X POST "$CP/tasks" -H 'Content-Type: application/json' \
        -d "{\"projectId\":\"$PROJECT\",\"requirement\":\"next\",\"acceptance\":[\"x\"],\"repoPath\":\"$repo\",\"baseCommit\":\"HEAD\"}")
      check "a new task starts from the forge's tip, not the stale local one" \
        "$(echo "$next" | jq -e --arg t "$tip" '.baseCommit == $t and .integrationBaseCommit == $t' >/dev/null; echo $?)"
      check "and says why" "$([ "$(event_count "$(echo "$next" | jq -r .id)" branch.created "the local branch is behind")" -ge 1 ]; echo $?)"
      ;;
    pr|pr-closed)
      check "task PR_OPEN ($final)" "$([ "$final" = PR_OPEN ]; echo $?)"
      check "the parent branch was pushed" "$([ "$(git -C "$origin" rev-parse "$branch" 2>/dev/null)" = "$(git -C "$repo" rev-parse "$branch")" ]; echo $?)"
      check "gh opened the PR from the parent branch into the target" \
        "$(grep -qF "gh pr create --base $target --head $branch --title Fix applyDiscount" "$GH_LOG" && grep -qF -- "--repo github.com/example/app" "$GH_LOG"; echo $?)"
      check "the PR body lists the acceptance criteria and the final review" \
        "$(grep -A30 "gh pr create" "$GH_LOG" | grep -qF "applyDiscount(200, 10) returns 180" && grep -qF "**security-reviewer**: pass" "$GH_LOG"; echo $?)"
      if [ "$1" = pr ]; then
        check "approving with an open thread: the approval record lists it" \
          "$(jq -e '.unresolvedThreads == 1' "$WORK/approve.out" >/dev/null && curl -sf "$CP/tasks/$task/approvals" | jq -e '.[0].action == "approve" and (.[0].unresolvedThreads | length == 1) and .[0].unresolvedThreads[0].where == "general" and (.[0].unresolvedThreads[0].comment | contains("rounding mode"))' >/dev/null; echo $?)"
        check "the PR body lists the unresolved thread" \
          "$(grep -qF "## Unresolved review threads" "$GH_LOG" && grep -qF 'Follow up on rounding mode later.' "$GH_LOG"; echo $?)"
      fi
      check "the delivery records the PR" "$(echo "$d" | jq -e '.mode == "pull_request" and .prUrl == "https://github.com/example/app/pull/7" and .prNumber == 7' >/dev/null; echo $?)"
      check "the target branch did not move locally" "$([ "$(git -C "$repo" rev-parse "$target")" = "$before" ]; echo $?)"
      if [ "$1" = pr ]; then
        forge_merge "$origin" "$target" "$branch" >"$GH_PR_MERGE" || true
        echo MERGED >"$GH_PR_STATE"
        final=$(wait_status "$task" "MERGED|REJECTED" 30) || true
        check "merged on the forge -> MERGED with the forge's merge commit ($final)" \
          "$([ "$final" = MERGED ] && delivery_of "$task" | jq -e --arg m "$(cat "$GH_PR_MERGE")" '.mergeCommit == $m' >/dev/null; echo $?)"
      else
        echo CLOSED >"$GH_PR_STATE"
        final=$(wait_status "$task" "MERGED|REJECTED" 30) || true
        check "closed without merging -> REJECTED ($final)" "$([ "$final" = REJECTED ] && has_reason "$task" "closed without merging"; echo $?)"
      fi
      ;;
  esac
  [ "${2:-}" = rollback ] && rollback_delivery "$1"
  return 0
}

rollback() { # task -> HTTP code
  curl -s -o "$WORK/rollback.out" -w '%{http_code}' -X POST "$CP/tasks/$1/rollback" -H 'Content-Type: application/json' \
    -d '{"reviewer":"harness","rationale":"broke checkout"}'
}

rollback_decisions() { # task -> number of recorded rollback decisions
  mongosh --quiet "$DB" --eval "$(printf 'print(db.approvals.countDocuments({taskId: "%s", action: "rollback"}))' "$1")"
}

rollback_delivery() { # local | pr  (task, repo, target, branch, origin from run_delivery)
  local merge code fixed_before
  merge=$(delivery_of "$task" | jq -r .mergeCommit)
  local other; other=$(curl -s -X POST "$CP/tasks" -H 'Content-Type: application/json' \
    -d "{\"projectId\":\"$PROJECT\",\"requirement\":\"noop\",\"acceptance\":[\"x\"],\"repoPath\":\"$repo\",\"baseCommit\":\"HEAD\"}")
  check "a task that is not delivered cannot be rolled back ($(echo "$other" | head -c 120))" \
    "$([ "$(rollback "$(echo "$other" | jq -r .id)")" = 409 ]; echo $?)"
  if [ "$1" = local ]; then
    # Unrelated work lands on the target after the delivery; the rollback must keep it.
    echo "# Notes" >"$repo/NOTES.md"; git -C "$repo" add NOTES.md
    git -C "$repo" -c user.name=someone -c user.email=s@localhost commit -q -m "notes"
    local before; before=$(git -C "$repo" rev-parse "$target")
    code=$(rollback "$task")
    final=$(wait_status "$task" "ROLLED_BACK|MERGED" 60) || true
    d=$(delivery_of "$task"); echo "  rollback: $(echo "$d" | jq -c .rollback)"
    check "rollback answers 202 and the task is ROLLED_BACK ($code, $final)" "$([ "$code" = 202 ] && [ "$final" = ROLLED_BACK ]; echo $?)"
    check "the target's tip is one revert commit on top of its old tip" \
      "$(t=$(git -C "$repo" rev-parse "$target"); [ "$(git -C "$repo" rev-parse "$t^")" = "$before" ] && echo "$d" | jq -e --arg t "$t" '.rollback.revertCommit == $t' >/dev/null && git -C "$repo" log -1 --format=%B "$t" | grep -q "This reverts $merge"; echo $?)"
    check "the fix is gone from the checkout, the later work stays, the checkout is clean" \
      "$(! grep -q Math.round "$repo/src/price.js" && [ -f "$repo/NOTES.md" ] && [ -z "$(git -C "$repo" status --porcelain --untracked-files=no)" ]; echo $?)"
    check "history is not rewritten: the delivery merge is still on the target" "$(git -C "$repo" merge-base --is-ancestor "$merge" "$target"; echo $?)"
  else
    echo OPEN >"$GH_PR_STATE"; : >"$GH_LOG"
    code=$(rollback "$task")
    final=$(wait_status "$task" "REVERT_PR_OPEN|MERGED|ROLLED_BACK" 60) || true
    d=$(delivery_of "$task"); echo "  rollback: $(echo "$d" | jq -c .rollback)"
    check "rollback answers 202 and opens a revert PR ($code, $final)" "$([ "$code" = 202 ] && [ "$final" = REVERT_PR_OPEN ]; echo $?)"
    local rbb; rbb=$(echo "$d" | jq -r .rollback.branch)
    check "the revert branch is pushed: one commit on top of the forge's merge" \
      "$([ "$rbb" = "$branch-revert" ] && [ "$(git -C "$origin" rev-parse "$rbb^")" = "$merge" ] && ! git -C "$origin" show "$rbb:src/price.js" | grep -q Math.round; echo $?)"
    check "gh opened the revert PR into the target" \
      "$(grep -qF "gh pr create --base $target --head $rbb --title Revert: Fix applyDiscount" "$GH_LOG"; echo $?)"
    check "the target did not move on the forge or locally" \
      "$([ "$(git -C "$origin" rev-parse "$target")" = "$merge" ]; echo $?)"
    forge_merge "$origin" "$target" "$rbb" >"$GH_PR_MERGE" || true
    echo MERGED >"$GH_PR_STATE"
    final=$(wait_status "$task" "ROLLED_BACK|MERGED" 30) || true
    check "revert merged on the forge -> ROLLED_BACK ($final)" "$([ "$final" = ROLLED_BACK ] && has_reason "$task" "revert pull request merged"; echo $?)"
  fi
  check "the rollback is recorded as a decision" \
    "$([ "$(rollback_decisions "$task")" = 1 ]; echo $?)"
  code=$(rollback "$task")
  check "a rolled-back task cannot be rolled back again (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
}

request_changes() { # task json -> HTTP code (body in $WORK/rc.out)
  curl -s -o "$WORK/rc.out" -w '%{http_code}' -X POST "$CP/tasks/$1/request-changes" -H 'Content-Type: application/json' -d "$2"
}
run_token() { printf 'run:%s' "$1" | openssl dgst -sha256 -hmac "$FORGE_CALLBACK_SECRET" | awk '{print $NF}'; }
has_event() { [ "$(event_count "$1" "$2" "$3")" -ge 1 ]; } # task type reason-substring
thread_ids() { curl -sf "$CP/tasks/$1/threads" | jq -r '.threads[].id'; }

run_feedback() { # request-changes | unhandled-thread | escalated-parent-retry | budget
  local repo task final code line t1 t2 t3 first r1
  repo=$(new_repo)
  : >"$WORK/prompts.jsonl"
  case "$1" in
    budget) start_adapter "$SCEN/two-reviewers-pass.json"; task=$(create_and_dispatch "$repo" ',"maxRevisions":1') ;;
    request-changes) start_adapter "$SCEN/feedback-$1.json"; task=$(create_and_dispatch "$repo" ',"maxRevisions":2') ;;
    *) start_adapter "$SCEN/feedback-$1.json"; task=$(create_and_dispatch "$repo") ;;
  esac

  if [ "$1" = escalated-parent-retry ]; then
    final=$(wait_status "$task" "ESCALATED|REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
    check "the final review rejects the debug log: parent ESCALATED ($final)" "$([ "$final" = ESCALATED ]; echo $?)"
    first=$(curl -sf "$CP/tasks/$task/children" | jq -r '.[0].id')
    code=$(decide "$task" retry "Remove the console.log; keep the rounding.")
    check "retry sends the parent back to the head engineer (HTTP $code)" \
      "$([ "$code" = 202 ] && jq -e '.status == "DECOMPOSING"' "$WORK/decide.out" >/dev/null; echo $?)"
    final=$(wait_status "$task" "REVIEW_PENDING|ESCALATED|BLOCKED|REJECTED" 300) || true
    stop_adapter
    check "the follow-up fixed it and the parent reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
    check "the head engineer was shown the escalated review and the guidance" \
      "$(jq -e -s '[.[] | select(.role == "head-engineer")] | length == 2 and (.[1].task | contains("# Final review (escalated)") and contains("Remove the debug log of the discount rate") and contains("Remove the console.log; keep the rounding.") and contains("+  console.log("))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
    check "a second subtask started from the escalated change and integrated" \
      "$(c=$(child_titled "$task" "Remove the debug log"); [ -n "$c" ] && [ "$(status_of "$c")" = INTEGRATED ] && [ "$(status_of "$first")" = INTEGRATED ] && git -C "$repo" merge-base --is-ancestor "$(curl -sf "$CP/tasks/$first" | jq -r .branch)" "$(run_base "$c")"; echo $?)"
    check "the parent branch no longer logs and still rounds" \
      "$(git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -q Math.round && ! git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -q console.log; echo $?)"
    check "r1 is recorded with the second final review attempt (-a2), the first kept" \
      "$(curl -sf "$CP/tasks/$task" | jq -e --arg run "run-$task-final-review-a2" '(.revisions | length == 1 and .[0].finalReviewRunId == $run) and (.followUps | length == 1 and .[0].kind == "escalation_retry" and .[0].outcome == "planned" and .[0].revision == 0)' >/dev/null && verdicts "run-$task-final-review" | grep -q "reviewer fail" && verdicts "run-$task-final-review-a2" | grep -qx '1 reviewer pass,1 security-reviewer pass'; echo $?)"
    return 0
  fi

  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  check "parent reaches REVIEW_PENDING with r1 ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  check "with no open thread and no summary, a request for changes is refused (400)" \
    "$([ "$(request_changes "$task" '{"reviewer":"dana","summary":"  "}')" = 400 ]; echo $?)"
  line=$(git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -n Math.round | head -1 | cut -d: -f1)
  post_thread "$task" "{\"author\":\"dana\",\"body\":\"Name the 100.\",\"anchor\":{\"path\":\"src/price.js\",\"line\":$line}}" >/dev/null
  post_thread "$task" '{"author":"dana","body":"Is 0% covered by a test?"}' >/dev/null
  [ "$1" = request-changes ] && post_thread "$task" "{\"author\":\"dana\",\"body\":\"Export a default too?\",\"anchor\":{\"path\":\"src/price.js\",\"line\":$((line + 3))}}" >/dev/null
  t1=$(thread_ids "$task" | sed -n 1p); t2=$(thread_ids "$task" | sed -n 2p); t3=$(thread_ids "$task" | sed -n 3p)

  case "$1" in
    budget)
      stop_adapter
      code=$(request_changes "$task" '{"reviewer":"dana","summary":"more"}')
      check "a task budgeted for one revision cannot be sent back (HTTP $code)" \
        "$([ "$code" = 409 ] && grep -q "revision budget is spent" "$WORK/rc.out" && [ "$(status_of "$task")" = REVIEW_PENDING ]; echo $?)"
      check "it can still be approved" "$([ "$(approve "$task")" = 200 ]; echo $?)"
      ;;
    unhandled-thread)
      code=$(request_changes "$task" '{"reviewer":"dana"}')
      check "changes requested with two open threads (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
      final=$(wait_status "$task" "REVIEW_PENDING|DISPATCHED|BLOCKED" 120) || true
      stop_adapter
      check "the graph leaving both threads unhandled is refused: back to REVIEW_PENDING ($final)" \
        "$([ "$final" = REVIEW_PENDING ] && has_reason "$task" "follow-up 1 refused: open thread(s) neither addressed by a subtask nor answered: T1 (src/price.js:$line), T2 (general)"; echo $?)"
      check "no subtask was added, r1 is still the only revision, the refusal is recorded" \
        "$([ "$(curl -sf "$CP/tasks/$task/children" | jq length)" = 1 ] && curl -sf "$CP/tasks/$task" | jq -e '(.revisions | length == 1) and (.followUps[0].outcome | startswith("refused: open thread"))' >/dev/null; echo $?)"
      check "the threads are still open" "$(curl -sf "$CP/tasks/$task/threads" | jq -e '[.threads[] | select(.status == "open")] | length == 2' >/dev/null; echo $?)"
      check "a subtask cannot have changes requested (400)" \
        "$([ "$(request_changes "$(curl -sf "$CP/tasks/$task/children" | jq -r '.[0].id')" '{"reviewer":"dana","summary":"x"}')" = 400 ]; echo $?)"
      ;;
    request-changes)
      code=$(request_changes "$task" '{"reviewer":"dana"}')
      check "request changes answers DECOMPOSING with follow-up 1" \
        "$(jq -e '.status == "DECOMPOSING" and .followUp == 1 and .openThreads == 3' "$WORK/rc.out" >/dev/null; echo $?)"
      check "threads cannot change while the follow-up runs (409)" \
        "$([ "$(post_thread "$task" '{"author":"dana","body":"late"}' | head -c3)" = 409 ]; echo $?)"
      final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED" 300) || true
      stop_adapter
      check "the follow-up ran and the parent is back in REVIEW_PENDING as r2 ($final)" \
        "$([ "$final" = REVIEW_PENDING ] && curl -sf "$CP/tasks/$task" | jq -e --arg run "run-$task-final-review-r2" '(.revisions | length == 2) and .revisions[1].finalReviewRunId == $run and (.followUps | length == 1 and .[0].outcome == "planned" and .[0].revision == 1 and .[0].runId == ($run | sub("final-review-r2"; "revise-1")))' >/dev/null; echo $?)"
      check "the head engineer was shown the threads as T1, T2 and the combined diff" \
        "$(jq -e -s '[.[] | select(.role == "head-engineer")] | length == 2 and (.[1].task | contains("## T1 — src/price.js:") and contains("Name the 100.") and contains("## T2 — general") and contains("```diff\ndiff --git a/src/price.js"))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
      local f; f=$(child_titled "$task" "Name the cents factor")
      check "the follow-up subtask addresses T1, was told about it, and started from r1" \
        "$([ -n "$f" ] && curl -sf "$CP/tasks/$f" | jq -e --arg t "$t1" '.addresses == [$t] and .status == "INTEGRATED" and (.requirement | contains("Review comments this subtask addresses:") and contains("harness: Name the 100."))' >/dev/null && [ "$(run_base "$f")" = "$(curl -sf "$CP/tasks/$task" | jq -r '.revisions[0].commit')" ]; echo $?)"
      check "T1 is taken on by that subtask; T2 has the head engineer's answer and is addressed" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t1 "$t1" --arg t2 "$t2" --arg f "$f" '(.threads[] | select(.id == $t1) | .addressedBy == $f) and (.threads[] | select(.id == $t2) | .status == "addressed" and .comments[1].author == "head-engineer" and .comments[1].kind == "agent" and (.comments[1].body | contains("already covers 0%")))' >/dev/null; echo $?)"
      check "r2's changes since r1 are just the named constant" \
        "$(curl -sf "$CP/tasks/$task/diff?revision=2&since=1" | jq -r .diff | grep -q '^+const CENTS = 100;' && [ "$(curl -sf "$CP/tasks/$task/diff?revision=2&since=1" | jq -r .diff | grep -c '^diff --git')" = 1 ]; echo $?)"
      check "the request is recorded as a decision with the three threads" \
        "$(curl -sf "$CP/tasks/$task/approvals" | jq -e '[.[] | select(.action == "request_changes")] | length == 1 and (.[0].unresolvedThreads | length == 3)' >/dev/null; echo $?)"
      # Phase 12.4: the subtask's reply, and the threads placed on r2 (just
      # after the revision is recorded).
      for _ in $(seq 1 20); do has_event "$task" thread.moved "$t3" && break; sleep 0.5; done
      check "the follow-up implementer was told to reply on T1; its reviewers were told the replies are not theirs" \
        "$(curl -sf "$CP/tasks/$f" | jq -e '.requirement | contains("- T1: src/price.js:") and (contains("reply_to_thread") | not)' >/dev/null && jq -e -s '(any(.[]; .role == "implementer" and (.task | startswith("Name the cents factor") and contains("reply on each review comment listed in the requirement with reply_to_thread")))) and ([.[] | select(.role == "reviewer" and (.task | contains("Task requirement:\nName the cents factor")))] | length == 2 and all(.task | contains("judge only the code change")))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
      check "its reply is on T1 as an agent comment on r2, and T1 is addressed" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t "$t1" '.threads[] | select(.id == $t) | .status == "addressed" and (.comments | length == 2) and .comments[1].author == "implementer" and .comments[1].kind == "agent" and .comments[1].body == "Named it CENTS." and .comments[1].revision == 2' >/dev/null; echo $?)"
      check "T1's line was rewritten in r2: outdated, still shown on r1" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t "$t1" --arg l "$line" '.threads[] | select(.id == $t) | .outdated and .outdatedOn == 2 and .shownOn == 1 and .anchor.line == ($l|tonumber)' >/dev/null && has_event "$task" thread.outdated "$t1"; echo $?)"
      check "T3's line is unchanged: moved down two lines onto r2" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t "$t3" --arg l "$line" '.threads[] | select(.id == $t) | (.outdated | not) and .shownOn == 2 and .anchor.line == ($l|tonumber) + 5 and .origin.line == ($l|tonumber) + 3 and (.anchor.lineText | startswith("module.exports"))' >/dev/null && has_event "$task" thread.moved "$t3"; echo $?)"
      code=$(request_changes "$task" '{"reviewer":"dana","summary":"once more"}')
      check "a budget of two revisions allows no second follow-up (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
      check "approving r2 delivers it -> MERGED" \
        "$([ "$(approve "$task")" = 200 ] && [ "$(wait_status "$task" "MERGED|BLOCKED" 120)" = MERGED ] && grep -q "const CENTS = 100" "$repo/src/price.js"; echo $?)"
      ;;
  esac
}

# Phase 13.5: the first subtask adds a postinstall hook and a test of its own
# container; the second subtask's workspace is set up after the hook is merged,
# so the hook runs in the offline scripts step. A listener on the host counts
# what reaches it: only the control request, never the hook or the tests.
BEACON_PORT=8093
run_sandbox() {
  local repo task final t1 t2 hits beacon probe
  hits="$WORK/beacon.log"; : >"$hits"
  node -e "require('http').createServer((q, r) => { require('fs').appendFileSync(process.argv[1], q.url + '\n'); r.end('ok') }).listen($BEACON_PORT, '127.0.0.1')" "$hits" &
  beacon=$! BEACON_PID=$!
  sleep 1
  docker run --rm --network=bridge "${WORKER_IMAGE:-forge-worker:dev}" node -e \
    "require('http').get('http://host.docker.internal:$BEACON_PORT/control', () => process.exit(0)).on('error', () => process.exit(1))" >/dev/null 2>&1 || true
  check "control: a container with network reaches the listener" "$(grep -qx /control "$hits"; echo $?)"

  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED" 300) || true
  stop_adapter
  t1=$(child_titled "$task" "Fix applyDiscount and add an install probe"); t2=$(child_titled "$task" "Note the sandbox in the README")
  echo "  parent $task -> $final; $t1 -> $(status_of "$t1"); $t2 -> $(status_of "$t2")"
  check "both subtasks integrated and the parent reached REVIEW_PENDING ($final)" \
    "$([ "$final" = REVIEW_PENDING ] && [ "$(status_of "$t1")" = INTEGRATED ] && [ "$(status_of "$t2")" = INTEGRATED ]; echo $?)"
  check "the agent's own container: non-root, read-only root, writable workspace and /tmp, no network (its tests passed in verification)" \
    "$(curl -sf "$CP/runs/run-$t1/verification" | jq -e '[.gates[] | select(.name == "test")][0] | .passed and (.output | contains("runs as a non-root user") and contains("the root filesystem is read-only") and contains("there is no network") and contains("# fail 0"))' >/dev/null; echo $?)"
  probe=$(curl -sf "$CP/runs/run-$t2" | jq -r '[(.lastCheckpoint.messages // [])[] | select(.role == "tool") | .content | select(contains("\"uid\""))][0] // empty' || true)
  echo "  install hook saw: $probe"
  check "the agent-written install hook ran, as a non-root user, with a read-only root filesystem" \
    "$(echo "$probe" | jq -e '.uid != 0 and .rootfs == "EROFS" and .usr == "EROFS" and .tmp == "writable"' >/dev/null; echo $?)"
  check "and with no network: no beacon, no DNS" \
    "$(echo "$probe" | jq -e '.network != "reached" and .dns != "resolved"' >/dev/null; echo $?)"
  check "the listener heard only the control request" "$([ "$(sort -u "$hits" | tr '\n' ' ')" = "/control " ]; echo $?)"
  kill "$beacon" 2>/dev/null || true; wait "$beacon" 2>/dev/null || true; BEACON_PID=""
}

run_shared_deps() {
  local repo task child second v before after stamp tool combined
  before=$(docker volume ls -q --filter label=forge.deps=1 | sort)
  repo=$(new_repo with-deps)
  # A unique package.json: this run has to build its own volume.
  sed -i.bak "s/\"name\": \"with-deps\"/\"name\": \"with-deps-$RANDOM$RANDOM\"/" "$repo/package.json"; rm "$repo/package.json.bak"
  git -C "$repo" add -A; git -C "$repo" -c user.name=forge -c user.email=forge@localhost commit -q -m "unique"
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  child=$(child_of "$task")
  after=$(docker volume ls -q --filter label=forge.deps=1 | sort)
  v=$(comm -13 <(echo "$before") <(echo "$after"))
  echo "  parent $task -> $final, volume: $v"
  check "the task reaches REVIEW_PENDING, using the package from the volume ($final)" "$([ "$final" = REVIEW_PENDING ] && curl -sf "$CP/runs/run-$child/verification" | jq -e '[.gates[] | select(.name == "test")][0] | .passed and (.skipped | not)' >/dev/null; echo $?)"
  check "one volume was built" "$([ "$(echo "$v" | wc -w)" -eq 1 ]; echo $?)"
  check "the volume is finished, and the package is in it" \
    "$(docker run --rm --network=none -v "$v:/v:ro" "${WORKER_IMAGE:-forge-worker:dev}" sh -c 'test -f /v/.forge-complete && test -f /v/ms/index.js' ; echo $?)"
  tool=$(curl -sf "$CP/runs/run-$child" | jq -r '[.lastCheckpoint.messages[] | select(.role == "tool") | .content | select(contains("REFUSED") or contains("WROTE"))][0] // ""')
  check "an agent can't write to it ($tool)" "$([[ "$tool" == *"REFUSED EROFS"* ]]; echo $?)"
  combined=$(mongosh --quiet "$DB" --eval "print(JSON.stringify(db.verificationRuns.findOne({taskId:'$task'}).gates.find(g=>g.name==='test')))")
  check "the combined run skips tests that already passed on the same code" \
    "$(echo "$combined" | jq -e '.skipped and (.skipReason | contains("already passed"))' >/dev/null; echo $?)"

  stamp=$(docker volume inspect --format '{{.CreatedAt}}' "$v")
  second=$(create_and_dispatch "$repo")
  final=$(wait_status "$second" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  check "a second task reuses it: no new volume, none rebuilt ($final)" \
    "$([ "$final" = REVIEW_PENDING ] && [ "$(docker volume ls -q --filter label=forge.deps=1 | sort)" = "$after" ] && [ "$(docker volume inspect --format '{{.CreatedAt}}' "$v")" = "$stamp" ]; echo $?)"
  docker volume rm "$v" >/dev/null 2>&1 || true
}

child_of_latest() { curl -sf "$CP/tasks/$1/children" | jq -r '.[0].id // empty'; }
run_budget() {
  local repo task child code final s
  repo=$(new_repo)
  start_adapter "$SCEN/budget-tokens.json"
  # The head engineer uses 1,200 tokens, the implementer's first call 4,500: over the cap.
  task=$(create_and_dispatch "$repo" ',"maxTokens":5000')
  final=$(wait_status "$task" "BLOCKED|REVIEW_PENDING" 180) || true
  child=$(child_of "$task")
  check "the cap stops the run: parent and subtask BLOCKED ($final, $(status_of "$child"))" \
    "$([ "$final" = BLOCKED ] && [ "$(status_of "$child")" = BLOCKED ]; echo $?)"
  check "the subtask's block names the cap" \
    "$(has_reason "$child" "token budget reached: task $task used 5700 of 5000 tokens"; echo $?)"
  check "the agent stopped before its next model call" \
    "$(curl -sf "$CP/runs/run-$child" | jq -e '.lastCheckpoint.stepCount == 1 and (.blockedReason | contains("token budget reached"))' >/dev/null; echo $?)"
  s=$(curl -sf "$CP/tasks/$child/spend")
  check "spend is the whole task's, priced, against its cap" \
    "$(echo "$s" | jq -e --arg t "$task" '.rootTaskId == $t and .task.spend.inputTokens == 5000 and .task.spend.outputTokens == 700 and (.task.spend.costUsd - 0.085 | fabs) < 0.000001 and .task.limits.maxTokens == 5000 and .subtask.inputTokens == 4000' >/dev/null; echo $?)"

  code=$(curl -s -o "$WORK/resume.out" -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")
  check "resuming over the cap is refused, and says why ($code)" \
    "$([ "$code" = 409 ] && grep -q "raise the cap" "$WORK/resume.out"; echo $?)"
  check "only a project admin raises a cap; a member gets 403" \
    "$([ "$(as "$MEMBER" -X PUT "$CP/tasks/$task/budget" -H 'Content-Type: application/json' -d '{"maxTokens":100000}')" = 403 ]; echo $?)"
  check "a subtask shares its parent's budget (400)" \
    "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$CP/tasks/$child/budget" -H 'Content-Type: application/json' -d '{"maxTokens":100000}')" = 400 ]; echo $?)"
  check "the cap is raised, and that is recorded with who did it" \
    "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$CP/tasks/$task/budget" -H 'Content-Type: application/json' -d '{"maxTokens":100000}')" = 200 ] && curl -sf "$CP/tasks/$task/events" | jq -e 'any(.[]; .type == "budget.changed" and .actor == "harness")' >/dev/null; echo $?)"
  code=$(curl -s -o "$WORK/resume.out" -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 180) || true
  stop_adapter
  check "after raising it, resume finishes the work ($code, $final)" "$([ "$code" = 202 ] && [ "$final" = REVIEW_PENDING ]; echo $?)"
  check "the parent went back to waiting when its subtask resumed" "$(has_reason "$task" "subtask $child resumed"; echo $?)"
  check "the resumed invocation's history is not counted twice" \
    "$(curl -sf "$CP/tasks/$task/spend" | jq -e '.task.spend.inputTokens == 5100 and .task.spend.outputTokens == 750' >/dev/null; echo $?)"

  # The cap is reached as the head engineer finishes: the subtask is refused
  # before its agent starts, so it has no checkpoint. Resume starts it.
  start_adapter "$SCEN/budget-tokens.json"
  task=$(create_and_dispatch "$repo" ',"maxTokens":1000')
  final=$(wait_status "$task" "BLOCKED|REVIEW_PENDING" 180) || true
  child=$(child_of_latest "$task")
  check "a subtask refused before it starts is BLOCKED with the cap named ($final)" \
    "$([ "$final" = BLOCKED ] && has_reason "$child" "token budget reached" && curl -sf "$CP/runs/run-$child" | jq -e '.lastCheckpoint == null' >/dev/null; echo $?)"
  curl -sf -X PUT "$CP/tasks/$task/budget" -H 'Content-Type: application/json' -d '{"maxTokens":100000}' >/dev/null
  code=$(curl -s -o "$WORK/resume.out" -w '%{http_code}' -X POST "$CP/runs/run-$child/resume")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 180) || true
  stop_adapter
  check "after raising the cap, resume starts it from the beginning and it finishes ($code, $final)" \
    "$([ "$code" = 202 ] && [ "$final" = REVIEW_PENDING ]; echo $?)"

  # The cap stops the head engineer itself: no subtasks, so the task starts again.
  start_adapter "$SCEN/budget-head-engineer.json"
  task=$(create_and_dispatch "$repo" ',"maxTokens":1000')
  final=$(wait_status "$task" "BLOCKED|REVIEW_PENDING" 180) || true
  check "a head engineer stopped by the cap blocks its task, with the cap named ($final)" \
    "$([ "$final" = BLOCKED ] && has_reason "$task" "token budget reached" && [ "$(curl -sf "$CP/tasks/$task/children" | jq length)" = 0 ]; echo $?)"
  code=$(curl -s -o "$WORK/start.out" -w '%{http_code}' -X POST "$CP/tasks/$task/runs" -H 'Content-Type: application/json' -d '{"provider":"scripted","model":"scripted"}')
  check "starting it again over the cap is refused ($code)" "$([ "$code" = 409 ] && grep -q "raise the cap" "$WORK/start.out"; echo $?)"
  curl -sf -X PUT "$CP/tasks/$task/budget" -H 'Content-Type: application/json' -d '{"maxTokens":100000}' >/dev/null
  code=$(curl -s -o "$WORK/start.out" -w '%{http_code}' -X POST "$CP/tasks/$task/runs" -H 'Content-Type: application/json' -d '{"provider":"scripted","model":"scripted"}')
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 180) || true
  stop_adapter
  check "after raising the cap it starts again and finishes ($code, $final)" \
    "$([ "$code" = 202 ] && [ "$final" = REVIEW_PENDING ] && has_reason "$task" "started again after decomposition"; echo $?)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/tasks/$task/runs" -H 'Content-Type: application/json' -d '{"provider":"scripted","model":"scripted"}')
  check "a task under review cannot be started again ($code)" "$([ "$code" = 409 ]; echo $?)"

  # A project's cap covers all its tasks. It stays CREATED, ready to start once raised.
  curl -sf -X PUT "$CP/projects/$PROJECT/budget" -H 'Content-Type: application/json' -d '{"maxTokens":5000}' >/dev/null
  local other; other=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' \
    -d "{\"projectId\":\"$PROJECT\",\"requirement\":\"noop\",\"acceptance\":[\"x\"],\"repoPath\":\"$repo\",\"baseCommit\":\"HEAD\"}" | jq -r .id)
  code=$(curl -s -o "$WORK/start.out" -w '%{http_code}' -X POST "$CP/tasks/$other/runs" -H 'Content-Type: application/json' -d '{"provider":"scripted","model":"scripted"}')
  check "a project over its cap starts no new work ($code)" \
    "$([ "$code" = 409 ] && grep -q "token budget reached: project harness" "$WORK/start.out" && [ "$(status_of "$other")" = CREATED ]; echo $?)"
  check "a project's spend and cap can be read; a viewer cannot change the cap" \
    "$(curl -sf "$CP/projects/$PROJECT/spend" | jq -e '.limits.maxTokens == 5000 and .spend.inputTokens >= 5100' >/dev/null && [ "$(as "$VIEWER" -X PUT "$CP/projects/$PROJECT/budget" -H 'Content-Type: application/json' -d '{}')" = 403 ]; echo $?)"
  curl -sf -X PUT "$CP/projects/$PROJECT/budget" -H 'Content-Type: application/json' -d '{}' >/dev/null
}

run_restart_delivery() {
  local repo task target before final d branch
  repo=$(new_repo)
  target=$(git -C "$repo" symbolic-ref --short HEAD)
  start_adapter "$SCEN/two-reviewers-pass.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  check "parent reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  branch=$(branch_of "$task")

  # The target moves, so delivery merges it in and verifies again. The commit
  # that moves it holds a test that takes 25s, which only this verification
  # sees: the control plane is killed during it, after the merge was made.
  mkdir -p "$repo/tests"
  cat >"$repo/tests/slow-delivery.test.js" <<'SLOW'
const test = require('node:test');
const fs = require('node:fs');
test('slow verification', async () => {
  // Only the first run is slow: the verification interrupted by the kill.
  if (!fs.existsSync('/workspace/.slow-done')) {
    fs.writeFileSync('/workspace/.slow-done', 'x');
    await new Promise((r) => setTimeout(r, 25000));
  }
});
SLOW
  git -C "$repo" add tests/slow-delivery.test.js
  git -C "$repo" -c user.name=someone -c user.email=s@localhost commit -q -m "a slow test"
  before=$(git -C "$repo" rev-parse "$target")

  check "approval answers DELIVERING" "$([ "$(approve "$task")" = 200 ]; echo $?)"
  for _ in $(seq 1 120); do [ "$(event_count "$task" delivery.target_merged "Merge $target")" -ge 1 ] && break; sleep 1; done
  sleep 3 # into the verification
  check "the target was merged in; delivery is still running ($(status_of "$task"))" \
    "$([ "$(status_of "$task")" = DELIVERING ] && [ "$(delivery_verifications "$task")" = 0 ]; echo $?)"
  kill -9 "$CP_PID" 2>/dev/null || true; wait "$CP_PID" 2>/dev/null || true; CP_PID=""
  echo "  control plane killed during delivery"
  check "the target has not moved: nothing was delivered" "$([ "$(git -C "$repo" rev-parse "$target")" = "$before" ]; echo $?)"

  start_cp
  final=$(wait_status "$task" "MERGED|BLOCKED" 180) || true
  d=$(delivery_of "$task"); echo "  delivery: $d"
  reasons "$task" | tail -3 | sed 's/^/      /'
  check "on restart the delivery resumes and finishes ($final)" "$([ "$final" = MERGED ]; echo $?)"
  check "the restart is recorded on the task" "$([ "$(event_count "$task" delivery.resumed "restarted during delivery")" = 1 ]; echo $?)"
  check "the merge whose verification was cut short is verified after the restart" \
    "$([ "$(delivery_verifications "$task")" = 1 ] && echo "$d" | jq -e --arg b "$(git -C "$repo" rev-parse "$branch")" '.targetMoved and .verifiedCommit == $b' >/dev/null; echo $?)"
  check "the target holds the merge exactly once" \
    "$(git -C "$repo" merge-base --is-ancestor "$before" "$target" && git -C "$repo" merge-base --is-ancestor "$branch" "$target" && [ "$(git -C "$repo" rev-list --count "$before..$target" --first-parent)" = 1 ]; echo $?)"
  stop_cp; start_cp
  check "a second restart with nothing to deliver changes nothing" \
    "$([ "$(status_of "$task")" = MERGED ] && [ "$(event_count "$task" delivery.resumed "restarted")" = 1 ]; echo $?)"
}

for SC in "${SCENARIOS[@]}"; do
  echo "== $SC"
  case "$SC" in
    crash-resume) run_crash_resume ;;
    add-agent) run_add_agent ;;
    container-lost) run_container_lost ;;
    scope-conflict) run_scope_conflict ;;
    verify-escalate) run_verify_escalate ;;
    combined-escalate) run_combined_escalate ;;
    skip-checks) run_skip_checks ;;
    no-model-accept) run_no_model_accept ;;
    escalate-accept) run_escalation accept ;;
    escalate-retry) run_escalation retry ;;
    final-review-escalates) run_final_review_escalates ;;
    parallel-siblings) run_parallel siblings ;;
    diamond) run_parallel diamond ;;
    concurrency-cap) stop_cp; start_cp MAX_CONCURRENT_RUNS=1; run_parallel cap; stop_cp; start_cp ;;
    overlap-serialized) run_parallel serialized ;;
    deliver-target-moved) run_delivery moved ;;
    rollback-*) run_delivery "${SC#rollback-}" rollback ;;
    deliver-*) run_delivery "${SC#deliver-}" ;;
    feedback-*) run_feedback "${SC#feedback-}" ;;
    sandbox-postinstall) run_sandbox ;;
    budget-tokens) run_budget ;;
    restart-mid-delivery) run_restart_delivery ;;
    subfolder-tests)
      repo=$(new_repo subfolder-app); start_adapter "$SCEN/$SC.json"; task=$(create_and_dispatch "$repo")
      final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true; stop_adapter
      child=$(child_of "$task")
      check "the task reaches REVIEW_PENDING ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "the wrong fix failed the subfolder's tests, and was retried" "$(has_reason "$child" "verification failed; retrying"; echo $?)"
      v=$(mongosh --quiet "$DB" --eval "print(JSON.stringify(db.verificationRuns.find({taskId:'$child'}).sort({startedAt:1}).toArray().map(r=>r.gates.find(g=>g.name==='test'))))")
      check "the test gate ran in app/ with pnpm, failing then passing" \
        "$(echo "$v" | jq -e '.[0].passed == false and (.[0].skipped | not) and (.[0].output | contains("== app") and contains("node --test")) and .[-1].passed == true and (.[-1].skipped | not)' >/dev/null; echo $?)"
      ;;
    shared-deps) run_shared_deps ;;
    cycle-rejected)
      repo=$(new_repo parallel-features); start_adapter "$SCEN/$SC.json"; task=$(create_and_dispatch "$repo")
      final=$(wait_status "$task" "BLOCKED|DISPATCHED|REVIEW_PENDING" 120) || true; stop_adapter
      check "cyclic graph blocks the parent at decomposition ($final)" \
        "$([ "$final" = BLOCKED ] && has_reason "$task" "dependency cycle: t1 -> t2 -> t1"; echo $?)"
      check "no subtasks were created" "$([ "$(curl -sf "$CP/tasks/$task/children" | jq length)" = 0 ]; echo $?)"
      ;;
    *) run_scenario ;;
  esac
done

echo
echo "logs: $WORK"
if [ ${#FAILED[@]} -eq 0 ]; then
  echo "PASS"
else
  printf 'FAIL: %s\n' "${FAILED[@]}"
  exit 1
fi
