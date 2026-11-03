#!/usr/bin/env bash
# Phase 11 slice with a real model: a task's whole life in git.
#
# The parent branch appears when the task is created; the subtask's work is
# merged into it; approval merges it into the target branch of a repository
# with no remote (local delivery); a rollback then reverts it. The fixture is
# copied into a fresh git repo on every run, so your own repositories are
# never touched.
set -euo pipefail

CP=${CP:-http://localhost:8080}
PROVIDER=${PROVIDER:-claude}
MODEL=${MODEL:-claude-haiku-5-5}
POLLS=${POLLS:-600} # x 2s

FIXTURE=$(pwd)/tests/evaluations/fixtures/cart-pricing
REPO=$(mktemp -d "${TMPDIR:-/tmp}/forge-delivery-repo.XXXXXX")
cp -R "$FIXTURE/." "$REPO/"
git -C "$REPO" init -q -b main
git -C "$REPO" add -A
git -C "$REPO" -c user.name=forge -c user.email=forge@localhost commit -q -m "fixture: cart-pricing"
BASE=$(git -C "$REPO" rev-parse main)
echo "repo: $REPO"

FAILS=()
check() { if [ "$2" -eq 0 ]; then echo "  ok   $1"; else echo "  FAIL $1"; FAILS+=("$1"); fi; }
wait_for() { # "STATE|STATE" -> final state
  local s="" last=""
  for i in $(seq 1 "$POLLS"); do
    s=$(curl -sf "$CP/tasks/$TASK" | jq -r .status)
    [ "$s" != "$last" ] && { echo "  [$i] $s" >&2; last=$s; }
    [[ "$s" =~ ^($1)$ ]] && break
    sleep 2
  done
  echo "$s"
}

TASK=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Fix applyDiscount in src/price.js: it subtracts the percentage as an absolute amount instead of applying a percentage discount rounded to cents.\",
  \"acceptance\":[\"applyDiscount(200, 10) returns 180\",\"applyDiscount(19.99, 25) returns 14.99\",\"npm test passes\"],
  \"repoPath\":\"$REPO\",
  \"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":30,\"maxRetries\":2,\"wallClockMs\":900000}
}" | jq -r .id)
echo "task: $TASK"
BRANCH=$(curl -sf "$CP/tasks/$TASK" | jq -r .integrationBranch)
check "parent branch $BRANCH created with the task, at main" \
  "$([ "$(git -C "$REPO" rev-parse "$BRANCH" 2>/dev/null)" = "$BASE" ]; echo $?)"
check "the console hint says approval merges locally into main" \
  "$(curl -sf "$CP/tasks/$TASK/delivery" | jq -e '.mode == "local_merge" and .deliversOnApproval and .targetBranch == "main"' >/dev/null; echo $?)"

curl -sf -X POST "$CP/tasks/$TASK/runs" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"$PROVIDER\",\"model\":\"$MODEL\"}" >/dev/null
STATUS=$(wait_for "REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED")
check "the work reaches REVIEW_PENDING ($STATUS)" "$([ "$STATUS" = REVIEW_PENDING ]; echo $?)"
[ "$STATUS" = REVIEW_PENDING ] || { printf 'FAIL: %s\n' "${FAILS[@]}"; exit 1; }
check "main has not moved before approval" "$([ "$(git -C "$REPO" rev-parse main)" = "$BASE" ]; echo $?)"

echo
echo "=== approve ==="
curl -sf -X POST "$CP/tasks/$TASK/approve" -H 'Content-Type: application/json' \
  -d '{"reviewer":"slice","rationale":"looks right"}' | jq -c .
STATUS=$(wait_for "MERGED|BLOCKED")
MERGE=$(git -C "$REPO" rev-parse main)
check "approval delivered: MERGED ($STATUS)" "$([ "$STATUS" = MERGED ]; echo $?)"
check "main is a merge of its old tip and the parent branch" \
  "$([ "$(git -C "$REPO" rev-parse "$MERGE^1")" = "$BASE" ] && [ "$(git -C "$REPO" rev-parse "$MERGE^2")" = "$(git -C "$REPO" rev-parse "$BRANCH")" ]; echo $?)"
check "main's checkout holds the fix and passes its tests" \
  "$(cd "$REPO" && [ -z "$(git status --porcelain --untracked-files=no)" ] && node --test >/dev/null 2>&1; echo $?)"
git -C "$REPO" log --oneline --graph -8 main

echo
echo "=== roll back ==="
curl -sf -X POST "$CP/tasks/$TASK/rollback" -H 'Content-Type: application/json' \
  -d '{"reviewer":"slice","rationale":"slice check"}' | jq -c .
STATUS=$(wait_for "ROLLED_BACK|MERGED")
check "rolled back ($STATUS)" "$([ "$STATUS" = ROLLED_BACK ]; echo $?)"
check "main is one revert commit on top of the merge, restoring the original tree" \
  "$([ "$(git -C "$REPO" rev-parse main^)" = "$MERGE" ] && [ "$(git -C "$REPO" rev-parse main^{tree})" = "$(git -C "$REPO" rev-parse "$BASE^{tree}")" ]; echo $?)"
git -C "$REPO" log --oneline -3 main

echo
if [ ${#FAILS[@]} -eq 0 ]; then echo "PASS"; else printf 'FAIL: %s\n' "${FAILS[@]}"; exit 1; fi
