#!/usr/bin/env bash
# Phase 8 failure paths, end to end and deterministic.
#
# Starts a private control plane and adapter (own ports, own database, own
# reaper scope) with the scripted model provider, then drives each scenario in
# tests/failure-paths/ through the real pipeline: Docker workspaces,
# verification gates, review, integration. No model API is called.
#
#   verify-retry   first fix fails verification, the retry passes
#   review-retry   reviewer rejects once, the re-invoked implementer passes
#   exhaustion     verification keeps failing; child and parent block
#   crash-resume   adapter dies mid-run; run goes RESUMABLE and resumes
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
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(verify-retry review-retry exhaustion crash-resume)

for p in $CP_PORT $AD_PORT; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then echo "port $p is in use"; exit 2; fi
done

export CONTROL_PLANE_ADDR=":$CP_PORT" CONTROL_PLANE_SELF_URL="$CP" ADAPTER_URL="http://localhost:$AD_PORT"
export ADAPTER_PORT=$AD_PORT MONGODB_DATABASE=$DB
export MODEL_PROVIDER=scripted MODEL_NAME=scripted DEFAULT_PROVIDER=scripted DEFAULT_MODEL=scripted
export FORGE_ALLOWED_PROVIDERS=scripted STALE_AFTER=5s MAX_VERIFICATION_ATTEMPTS=2

CP_PID="" AD_PID=""
cleanup() {
  [ -n "$AD_PID" ] && stop_adapter
  [ -n "$CP_PID" ] && kill "$CP_PID" 2>/dev/null || true
  # Containers are labeled with this instance; nothing else is touched.
  ids=$(docker ps -aq --filter "label=forge.instance=$DB" || true)
  [ -n "$ids" ] && docker rm -f $ids >/dev/null 2>&1 || true
}
trap cleanup EXIT

mongosh --quiet "$DB" --eval 'db.dropDatabase()' >/dev/null

(cd "$ROOT/services/control-plane" && go build -o "$WORK/forge" ./cmd/forge)
(cd "$ROOT/services/control-plane" && exec "$WORK/forge") >"$WORK/cp.log" 2>&1 &
CP_PID=$!

wait_http() { for _ in $(seq 1 60); do curl -sf -m1 "$1" >/dev/null 2>&1 && return 0; sleep 1; done; echo "timeout waiting for $1"; return 1; }
wait_http "$CP/tasks"

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
}

new_repo() {
  local repo; repo=$(mktemp -d "$WORK/repo.XXXXXX")
  cp -R "$FIXTURE/." "$repo/"
  git -C "$repo" init -q && git -C "$repo" add -A
  git -C "$repo" -c user.name=forge -c user.email=forge@localhost commit -q -m fixture
  echo "$repo"
}

create_and_dispatch() { # repo -> task id
  local task
  task=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
    \"requirement\":\"Fix applyDiscount in src/price.js so it applies a percentage discount rounded to cents.\",
    \"acceptance\":[\"applyDiscount(200, 10) returns 180\",\"applyDiscount(19.99, 25) returns 14.99\",\"npm test passes\"],
    \"repoPath\":\"$1\",\"baseCommit\":\"HEAD\",
    \"budget\":{\"maxSteps\":10,\"maxRetries\":0,\"wallClockMs\":300000}}" | jq -r .id)
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

FAILED=()
check() { # description, condition result (0 = ok)
  if [ "$2" -eq 0 ]; then echo "    ok   $1"; else echo "    FAIL $1"; FAILED+=("$SC: $1"); fi
}
has_reason() { reasons "$1" | grep -qF "$2"; }

run_scenario() {
  local repo task child final
  repo=$(new_repo)
  start_adapter "$SCEN/$SC.json"
  task=$(create_and_dispatch "$repo")
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  child=$(child_of "$task")
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'

  case "$SC" in
    verify-retry)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "child retried after failed verification" "$(has_reason "$child" "verification failed; retrying"; echo $?)"
      check "child integrated" "$([ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
      check "integrated fix rounds to cents" "$(git -C "$repo" show "forge/$task:src/price.js" | grep -q Math.round; echo $?)"
      ;;
    review-retry)
      check "parent reaches REVIEW_PENDING" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
      check "child retried after reviewer rejection" "$(has_reason "$child" "reviewer rejected; retrying"; echo $?)"
      check "reworked change was re-verified" "$(reasons "$child" | grep -c "RUNNING -> VERIFYING" | grep -qx 2; echo $?)"
      check "child integrated" "$([ "$(status_of "$child")" = INTEGRATED ]; echo $?)"
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
  local code; code=$(curl -s -o "$WORK/resume.out" -w '%{http_code}' -X POST "$CP/runs/$run/resume")
  check "resume accepted (HTTP $code: $(head -c 120 "$WORK/resume.out"))" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 300) || true
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'
  check "parent reaches REVIEW_PENDING after resume" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  check "resumed run was given the agent's tools" "$(grep -E "runId=$run-r .*tools=[1-9]" "$WORK/adapter.log" >/dev/null; echo $?)"
  check "run points at the resumed workspace" "$(curl -sf "$CP/runs/$run" | jq -e '.containerName | endswith("-r")' >/dev/null; echo $?)"
}

for SC in "${SCENARIOS[@]}"; do
  echo "== $SC"
  if [ "$SC" = crash-resume ]; then run_crash_resume; else run_scenario; fi
done

echo
echo "logs: $WORK"
if [ ${#FAILED[@]} -eq 0 ]; then
  echo "PASS"
else
  printf 'FAIL: %s\n' "${FAILED[@]}"
  exit 1
fi
