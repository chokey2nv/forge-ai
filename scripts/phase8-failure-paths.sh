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
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(verify-retry two-reviewers-pass split-verdict exhaustion crash-resume add-agent reviewer-timeout escalate-accept escalate-retry final-review-escalates parallel-siblings diamond concurrency-cap overlap-serialized cycle-rejected parallel-reviewers deliver-local deliver-target-moved deliver-dirty deliver-pr deliver-pr-closed)

for p in $CP_PORT $AD_PORT; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then echo "port $p is in use"; exit 2; fi
done

export CONTROL_PLANE_ADDR=":$CP_PORT" CONTROL_PLANE_SELF_URL="$CP" ADAPTER_URL="http://localhost:$AD_PORT"
export ADAPTER_PORT=$AD_PORT MONGODB_DATABASE=$DB
export MODEL_PROVIDER=scripted MODEL_NAME=scripted DEFAULT_PROVIDER=scripted DEFAULT_MODEL=scripted
export FORGE_ALLOWED_PROVIDERS=scripted STALE_AFTER=15s MAX_VERIFICATION_ATTEMPTS=2 MAX_REVIEW_ROUNDS=2
# Reviewers answer instantly here; a short timeout lets reviewer-timeout finish quickly.
export REVIEWER_TIMEOUT=8s REVIEW_TIMEOUT_GRACE=2s
export SCRIPTED_PROVIDER_LOG="$WORK/prompts.jsonl" # every agent's task prompt, for assertions

# A stand-in gh: tests never call the real one (it would act as your account).
mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<'GH'
#!/bin/sh
echo "gh $*" >>"${GH_LOG:-/dev/null}"
case "$1 $2" in
  "auth status") echo "  ✓ Logged in to github.com account forge-test (keyring)"; exit 0 ;;
  "pr create") echo "https://github.com/example/app/pull/7"; exit 0 ;;
  # The harness plays the forge: it writes OPEN, MERGED or CLOSED to $GH_PR_STATE.
  "pr view") s=$(cat "$GH_PR_STATE" 2>/dev/null || echo OPEN)
    echo "{\"state\":\"$s\",\"mergeCommit\":$([ "$s" = MERGED ] && echo '{"oid":"f0f0f0f0"}' || echo null)}"; exit 0 ;;
esac
echo "stub gh: unsupported: $*" >&2; exit 1
GH
chmod +x "$WORK/bin/gh"
export FORGE_GH="$WORK/bin/gh" GH_LOG="$WORK/gh.log" GH_PR_STATE="$WORK/pr-state" PR_POLL_INTERVAL=2s

# A private copy of the agent definitions: add-agent writes to it, never to agents/.
cp -R "$ROOT/agents" "$WORK/agents"
export AGENTS_DIR="$WORK/agents" SKILLS_DIR="$ROOT/skills"

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

wait_http() { for _ in $(seq 1 60); do curl -sf -m1 "$1" >/dev/null 2>&1 && return 0; sleep 1; done; echo "timeout waiting for $1"; return 1; }
start_cp() { # [VAR=value ...] extra environment for this control plane
  (cd "$ROOT/services/control-plane" && exec env "$@" "$WORK/forge") >>"$WORK/cp.log" 2>&1 &
  CP_PID=$!
  wait_http "$CP/tasks"
}
stop_cp() { kill "$CP_PID" 2>/dev/null || true; wait "$CP_PID" 2>/dev/null || true; CP_PID=""; }
start_cp

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

new_repo() { # [fixture name] (default cart-pricing)
  local repo src="$FIXTURE"; repo=$(mktemp -d "$WORK/repo.XXXXXX")
  [ -n "${1:-}" ] && src="$ROOT/tests/evaluations/fixtures/$1"
  cp -R "$src/." "$repo/"
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
  stop_adapter
  echo "  parent $task -> $final, child $child -> $(status_of "$child")"
  reasons "$child" | sed 's/^/      /'

  case "$SC" in
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
      check "the subtask's work is on its own branch, merged into the parent branch" \
        "$(cb=$(curl -sf "$CP/tasks/$child" | jq -r .branch); git -C "$repo" show "$cb:src/price.js" | grep -q Math.round && git -C "$repo" merge-base --is-ancestor "$cb" "$(branch_of "$task")"; echo $?)"
      check "integrated fix rounds to cents" "$(git -C "$repo" show "$(branch_of "$task"):src/price.js" | grep -q Math.round; echo $?)"
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
  code=$(decide "$task" retry "")
  check "retry is refused for a parent's combined change (HTTP $code)" "$([ "$code" = 409 ]; echo $?)"
  code=$(decide "$task" accept "")
  check "accept decision accepted (HTTP $code)" "$([ "$code" = 202 ]; echo $?)"
  final=$(wait_status "$task" "REVIEW_PENDING|BLOCKED|REJECTED" 60) || true
  stop_adapter
  check "parent reaches REVIEW_PENDING after accept ($final)" "$([ "$final" = REVIEW_PENDING ]; echo $?)"
  # Reviewers cannot run tests; the platform's gate results must be in their prompt.
  check "child reviewers were shown the gate results and test output" \
    "$(jq -e -s '[.[] | select(.role == "reviewer" and (.task | contains("Task requirement:\nFix applyDiscount\n")))] | length == 2 and all(.task | contains("Verification (run by the platform") and contains("- test: passed") and contains("# pass 3"))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
  check "final reviewers were shown the combined verification" \
    "$(jq -e -s '[.[] | select(.role == "reviewer")][2:] | length == 2 and all(.task | contains("Verification (run by the platform") and contains("- test: passed"))' "$WORK/prompts.jsonl" >/dev/null; echo $?)"
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

approve() { # task -> HTTP code
  curl -s -o "$WORK/approve.out" -w '%{http_code}' -X POST "$CP/tasks/$1/approve" -H 'Content-Type: application/json' \
    -d "{\"reviewer\":\"harness\",\"rationale\":\"scenario $SC\"}"
}
delivery_verifications() { # task -> passed verification runs made during delivery
  mongosh --quiet "$DB" --eval "$(printf 'print(db.verificationRuns.countDocuments({_id: {$regex: "^vr-%s-delivery-"}, passed: true}))' "$1")"
}
delivery_of() { curl -sf "$CP/tasks/$1" | jq -c '.delivery // {}'; }

run_delivery() { # local | moved | dirty | pr | pr-closed
  local repo task final target branch wt d origin
  repo=$(new_repo)
  target=$(git -C "$repo" symbolic-ref --short HEAD)
  if [[ "$1" == pr* ]]; then
    # A GitHub-looking remote whose pushes land in a local bare repository.
    origin="$WORK/origin-$RANDOM.git"; git init -q --bare "$origin"
    git -C "$repo" remote add origin https://github.com/example/app.git
    git -C "$repo" config "url.$origin.pushInsteadOf" https://github.com/example/app.git
    echo OPEN >"$GH_PR_STATE"; : >"$GH_LOG"
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
  esac
  local before; before=$(git -C "$repo" rev-parse "$target")

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
    pr|pr-closed)
      check "task PR_OPEN ($final)" "$([ "$final" = PR_OPEN ]; echo $?)"
      check "the parent branch was pushed" "$([ "$(git -C "$origin" rev-parse "$branch" 2>/dev/null)" = "$(git -C "$repo" rev-parse "$branch")" ]; echo $?)"
      check "gh opened the PR from the parent branch into the target" \
        "$(grep -qF "gh pr create --base $target --head $branch --title Fix applyDiscount" "$GH_LOG" && grep -qF -- "--repo github.com/example/app" "$GH_LOG"; echo $?)"
      check "the PR body lists the acceptance criteria and the final review" \
        "$(grep -A30 "gh pr create" "$GH_LOG" | grep -qF "applyDiscount(200, 10) returns 180" && grep -qF "**security-reviewer**: pass" "$GH_LOG"; echo $?)"
      check "the delivery records the PR" "$(echo "$d" | jq -e '.mode == "pull_request" and .prUrl == "https://github.com/example/app/pull/7" and .prNumber == 7' >/dev/null; echo $?)"
      check "the target branch did not move locally" "$([ "$(git -C "$repo" rev-parse "$target")" = "$before" ]; echo $?)"
      if [ "$1" = pr ]; then
        echo MERGED >"$GH_PR_STATE"
        final=$(wait_status "$task" "MERGED|REJECTED" 30) || true
        check "merged on the forge -> MERGED ($final)" \
          "$([ "$final" = MERGED ] && delivery_of "$task" | jq -e '.mergeCommit == "f0f0f0f0"' >/dev/null; echo $?)"
      else
        echo CLOSED >"$GH_PR_STATE"
        final=$(wait_status "$task" "MERGED|REJECTED" 30) || true
        check "closed without merging -> REJECTED ($final)" "$([ "$final" = REJECTED ] && has_reason "$task" "closed without merging"; echo $?)"
      fi
      ;;
  esac
}

for SC in "${SCENARIOS[@]}"; do
  echo "== $SC"
  case "$SC" in
    crash-resume) run_crash_resume ;;
    add-agent) run_add_agent ;;
    escalate-accept) run_escalation accept ;;
    escalate-retry) run_escalation retry ;;
    final-review-escalates) run_final_review_escalates ;;
    parallel-siblings) run_parallel siblings ;;
    diamond) run_parallel diamond ;;
    concurrency-cap) stop_cp; start_cp MAX_CONCURRENT_RUNS=1; run_parallel cap; stop_cp; start_cp ;;
    overlap-serialized) run_parallel serialized ;;
    deliver-target-moved) run_delivery moved ;;
    deliver-*) run_delivery "${SC#deliver-}" ;;
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
