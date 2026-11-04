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
#   rollback-local     a delivered local merge is reverted by a commit on the target (Phase 11.4)
#   rollback-pr        a merged PR is reverted by a revert PR; merged on the forge -> ROLLED_BACK
#   feedback-request-changes  threads on r1, request changes -> head engineer follow-up -> r2 -> approve -> MERGED (Phase 12.3)
#   feedback-unhandled-thread the follow-up graph leaves a thread unhandled: refused, back to REVIEW_PENDING
#   feedback-escalated-parent-retry  the final review escalates; a human sends it back with guidance -> fixed -> r1
#   feedback-budget    a task budgeted for one revision cannot be sent back
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
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(verify-retry two-reviewers-pass split-verdict exhaustion crash-resume add-agent reviewer-timeout escalate-accept escalate-retry final-review-escalates parallel-siblings diamond concurrency-cap overlap-serialized cycle-rejected parallel-reviewers deliver-local deliver-target-moved deliver-dirty deliver-pr deliver-pr-closed rollback-local rollback-pr feedback-request-changes feedback-unhandled-thread feedback-escalated-parent-retry feedback-budget)

for p in $CP_PORT $AD_PORT; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then echo "port $p is in use"; exit 2; fi
done

export CONTROL_PLANE_ADDR="127.0.0.1:$CP_PORT" CONTROL_PLANE_SELF_URL="$CP" ADAPTER_URL="http://localhost:$AD_PORT"
# Phase 13.1: the services trust each other through these; fresh for every run.
export FORGE_ADAPTER_TOKEN=$(openssl rand -hex 32) FORGE_CALLBACK_SECRET=$(openssl rand -hex 32)
export FORGE_CONSOLE_ORIGINS="http://localhost:5173"
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
  echo "$repo"
}

create_and_dispatch() { # repo [extra budget fields, e.g. ,"maxRevisions":2] -> task id
  local task
  task=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
    \"requirement\":\"Fix applyDiscount in src/price.js so it applies a percentage discount rounded to cents.\",
    \"acceptance\":[\"applyDiscount(200, 10) returns 180\",\"applyDiscount(19.99, 25) returns 14.99\",\"npm test passes\"],
    \"repoPath\":\"$1\",\"baseCommit\":\"HEAD\",
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
      thr=$(post_thread "$task" "{\"author\":\"dana\",\"body\":\"Why divide by 100 here?\",\"anchor\":{\"path\":\"src/price.js\",\"line\":$line}}")
      check "a thread opens on the Math.round line of r1 (HTTP $(head -c3 <<<"$thr"))" \
        "$(tail -c +4 <<<"$thr" | jq -e --arg l "$line" '.revision == 1 and .status == "open" and .anchor.side == "new" and .anchor.line == ($l|tonumber) and (.anchor.lineText | contains("Math.round"))' >/dev/null; echo $?)"
      local tid; tid=$(tail -c +4 <<<"$thr" | jq -r .id)
      check "a line the diff does not show is refused (400)" \
        "$([ "$(post_thread "$task" '{"author":"dana","body":"x","anchor":{"path":"src/price.js","line":999}}' | head -c3)" = 400 ]; echo $?)"
      check "a general thread opens" "$([ "$(post_thread "$task" '{"author":"dana","body":"Please add a test for 0%."}' | head -c3)" = 201 ]; echo $?)"
      check "a human replies" "$([ "$(thread_post "$task" "$tid/comments" '{"author":"lee","body":"Cents."}')" = 200 ]; echo $?)"
      check "an agent cannot resolve (403)" "$([ "$(thread_post "$task" "$tid/resolve" '{"author":"reviewer"}')" = 403 ]; echo $?)"
      check "a human resolves" "$([ "$(thread_post "$task" "$tid/resolve" '{"author":"dana"}')" = 200 ]; echo $?)"
      # Phase 13.1: only the adapter that was given a run's token may report on it.
      local fr="run-$task-final-review"
      check "a forged completion without the run's token is refused (401)" \
        "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/complete" -H 'Content-Type: application/json' -d '{"status":"complete","workspaceDiff":"x"}')" = 401 ] && [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/run-$child/checkpoint" -H "Authorization: Bearer $(run_token "$fr")" -H 'Content-Type: application/json' -d '{}')" = 401 ]; echo $?)"
      check "the right token on a complete run is refused too (409)" \
        "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/runs/$fr/complete" -H "Authorization: Bearer $(run_token "$fr")" -H 'Content-Type: application/json' -d '{}')" = 409 ]; echo $?)"
      start_adapter "$SCEN/$SC.json" # stopped once the scenario settled; nothing is run on it
      check "the adapter refuses runs without its secret (401), health stays open" \
        "$([ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$AD_PORT/runs" -H 'Content-Type: application/json' -d '{}')" = 401 ] && [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$AD_PORT/runs" -H 'Authorization: Bearer wrong' -H 'Content-Type: application/json' -d '{}')" = 401 ] && curl -sf "http://localhost:$AD_PORT/health" >/dev/null; echo $?)"
      check "both services listen on loopback only" \
        "$(lsof -nP -iTCP:"$CP_PORT" -sTCP:LISTEN | grep -q '127.0.0.1:' && lsof -nP -iTCP:"$AD_PORT" -sTCP:LISTEN | grep -q '127.0.0.1:' && ! lsof -nP -iTCP:"$CP_PORT" -iTCP:"$AD_PORT" -sTCP:LISTEN | grep -qE '\*:|0\.0\.0\.0'; echo $?)"
      stop_adapter
      check "CORS answers the console's origin only" \
        "$(curl -sI "$CP/tasks" -H 'Origin: http://localhost:5173' | grep -qi '^access-control-allow-origin: http://localhost:5173' && ! curl -sI "$CP/tasks" -H 'Origin: https://evil.example' | grep -qi '^access-control-allow-origin'; echo $?)"
      check "GET threads: one resolved with two comments, one open general" \
        "$(curl -sf "$CP/tasks/$task/threads" | jq -e --arg t "$tid" '(.threads | length == 2) and (.threads[] | select(.id == $t) | .status == "resolved" and .resolvedBy == "dana" and (.comments | length == 2)) and ([.threads[] | select(.anchor == null and .status == "open")] | length == 1) and (.notes | type == "array")' >/dev/null; echo $?)"
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
    -d "{\"requirement\":\"noop\",\"acceptance\":[\"x\"],\"repoPath\":\"$repo\",\"baseCommit\":\"HEAD\"}")
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
        "$([ -n "$f" ] && curl -sf "$CP/tasks/$f" | jq -e --arg t "$t1" '.addresses == [$t] and .status == "INTEGRATED" and (.requirement | contains("Review comments this subtask addresses:") and contains("dana: Name the 100."))' >/dev/null && [ "$(run_base "$f")" = "$(curl -sf "$CP/tasks/$task" | jq -r '.revisions[0].commit')" ]; echo $?)"
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
    rollback-*) run_delivery "${SC#rollback-}" rollback ;;
    deliver-*) run_delivery "${SC#deliver-}" ;;
    feedback-*) run_feedback "${SC#feedback-}" ;;
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
