#!/usr/bin/env bash
# Phase 12 slice with a real model: a human's line comment drives a revision.
#
# The task is fixed and put up for review as r1. A human comments on the
# fixed line asking for a named constant and requests changes; the head
# engineer plans a follow-up, an agent makes the change and replies on the
# thread, and the task comes back as r2. The human resolves the thread and
# approves; the change is merged into main of a repository with no remote.
# The fixture is copied into a fresh git repo on every run.
set -euo pipefail

# Phase 13.2: the API needs a signed-in caller. Create a token with
# `forge tokens create <user> slices` and export FORGE_TOKEN; every curl
# below sends it (through a private curl config file).
: "${FORGE_TOKEN:?export FORGE_TOKEN (an API token: forge tokens create <user> <name>)}"
export CURL_HOME=$(mktemp -d "${TMPDIR:-/tmp}/forge-curl.XXXXXX")
printf 'header = "Authorization: Bearer %s"\n' "$FORGE_TOKEN" >"$CURL_HOME/.curlrc"

CP=${CP:-http://localhost:8080}
PROVIDER=${PROVIDER:-claude}
MODEL=${MODEL:-claude-haiku-5-5}
POLLS=${POLLS:-600} # x 2s

FIXTURE=$(pwd)/tests/evaluations/fixtures/cart-pricing
REPO=$(mktemp -d "${TMPDIR:-/tmp}/forge-feedback-repo.XXXXXX")
cp -R "$FIXTURE/." "$REPO/"
git -C "$REPO" init -q -b main
git -C "$REPO" add -A
git -C "$REPO" -c user.name=forge -c user.email=forge@localhost commit -q -m "fixture: cart-pricing"
BASE=$(git -C "$REPO" rev-parse main)
echo "repo: $REPO"

FAILS=()
check() { if [ "$2" -eq 0 ]; then echo "  ok   $1"; else echo "  FAIL $1"; FAILS+=("$1"); fi; }
wait_until() { # jq predicate on the task -> its final status
  local t s="" last=""
  for i in $(seq 1 "$POLLS"); do
    t=$(curl -sf "$CP/tasks/$TASK")
    s=$(echo "$t" | jq -r '"\(.status) r\(.revisions // [] | length)"')
    [ "$s" != "$last" ] && { echo "  [$i] $s" >&2; last=$s; }
    echo "$t" | jq -e "$1" >/dev/null && break
    # A subtask waiting on a human (or blocked) holds its parent: stop and say so.
    stuck=$(curl -sf "$CP/tasks/$TASK/children" | jq -r '[.[] | select(.status == "ESCALATED" or .status == "BLOCKED") | "\(.id) \(.status)"] | join(", ")')
    [ -n "$stuck" ] && { echo "  subtask needs a human: $stuck" >&2; break; }
    sleep 2
  done
  echo "${s% *}"
}
show_thread() { curl -sf "$CP/tasks/$TASK/threads" | jq -r --arg t "$THREAD" '.threads[] | select(.id == $t) |
  "  [\(.status)\(if .outdated then ", outdated r\(.outdatedOn)" else "" end)] \(.anchor.path):\(.anchor.line) on r\(.shownOn)",
  (.comments[] | "    r\(.revision) \(.author) (\(.kind)): \(.body)")'; }

TASK=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Fix applyDiscount in src/price.js: it subtracts the percentage as an absolute amount instead of applying a percentage discount rounded to cents.\",
  \"acceptance\":[\"applyDiscount(200, 10) returns 180\",\"applyDiscount(19.99, 25) returns 14.99\",\"npm test passes\"],
  \"repoPath\":\"$REPO\",
  \"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":30,\"maxRetries\":2,\"wallClockMs\":900000,\"maxRevisions\":2}
}" | jq -r .id)
echo "task: $TASK"
BRANCH=$(curl -sf "$CP/tasks/$TASK" | jq -r .integrationBranch)

echo
echo "=== r1: the fix ==="
curl -sf -X POST "$CP/tasks/$TASK/runs" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"$PROVIDER\",\"model\":\"$MODEL\"}" >/dev/null
STATUS=$(wait_until '.status | test("^(REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED)$")')
check "the fix reaches REVIEW_PENDING as r1 ($STATUS)" "$([ "$STATUS" = REVIEW_PENDING ]; echo $?)"
[ "$STATUS" = REVIEW_PENDING ] || { printf 'FAIL: %s\n' "${FAILS[@]}"; exit 1; }
R1=$(git -C "$REPO" rev-parse "$BRANCH")
git -C "$REPO" show "$R1:src/price.js"

# The line that computes the discount, wherever the model put it.
LINE=$(git -C "$REPO" show "$R1:src/price.js" | grep -n '^[[:space:]]*return' | head -1 | cut -d: -f1)
echo
echo "=== a human comments on line $LINE and requests changes ==="
THREAD=$(curl -sf -X POST "$CP/tasks/$TASK/threads" -H 'Content-Type: application/json' -d "{
  \"author\":\"dana\",
  \"body\":\"Please don't leave the cents factor as a bare number here. Add a constant CENTS = 100 in src/price.js, use it for the rounding, export it alongside applyDiscount, and add a test that CENTS is 100.\",
  \"anchor\":{\"path\":\"src/price.js\",\"side\":\"new\",\"line\":$LINE}}" | jq -r .id)
check "a thread opened on src/price.js:$LINE of r1 ($THREAD)" "$([[ "$THREAD" == thr-* ]]; echo $?)"
curl -sf -X POST "$CP/tasks/$TASK/request-changes" -H 'Content-Type: application/json' \
  -d '{"reviewer":"dana"}' | jq -c .

# r2, or a refused follow-up (back to REVIEW_PENDING on r1), or a failure.
STATUS=$(wait_until '(.status == "REVIEW_PENDING" and (.revisions | length) == 2) or (.status | test("^(BLOCKED|REJECTED|ESCALATED)$")) or (((.followUps // [])[0].outcome // "") | startswith("refused"))')
REVS=$(curl -sf "$CP/tasks/$TASK" | jq '.revisions | length')
check "the follow-up comes back for review as r2 ($STATUS, $REVS revisions)" "$([ "$STATUS" = REVIEW_PENDING ] && [ "$REVS" = 2 ]; echo $?)"
curl -sf "$CP/tasks/$TASK" | jq -r '.followUps[] | "  follow-up \(.n): \(.kind) -> \(.outcome)"'
[ "$STATUS" = REVIEW_PENDING ] || { printf 'FAIL: %s\n' "${FAILS[@]}"; exit 1; }
sleep 2 # threads are placed on r2 just after it is recorded

echo
echo "=== r2 ==="
git -C "$REPO" show "$BRANCH:src/price.js"
show_thread
FOLLOW=$(curl -sf "$CP/tasks/$TASK/children" | jq -r --arg t "$THREAD" '[.[] | select((.addresses // []) | index($t))][0].id // empty')
check "a follow-up subtask took the thread on and integrated ($FOLLOW)" \
  "$([ -n "$FOLLOW" ] && [ "$(curl -sf "$CP/tasks/$FOLLOW" | jq -r .status)" = INTEGRATED ]; echo $?)"
check "r2 exports CENTS = 100 and applyDiscount still passes the tests" \
  "$(d=$(mktemp -d); git -C "$REPO" archive "$BRANCH" | tar -x -C "$d" && (cd "$d" && node -e 'process.exit(require("./src/price.js").CENTS === 100 ? 0 : 1)' && node --test >/dev/null 2>&1); echo $?)"
check "r2's changes since r1 introduce CENTS" \
  "$(curl -sf "$CP/tasks/$TASK/diff?revision=2&since=1" | jq -r .diff | grep -q '^+.*CENTS'; echo $?)"
check "an agent replied on the thread on r2, and it is addressed" \
  "$(curl -sf "$CP/tasks/$TASK/threads" | jq -e --arg t "$THREAD" '.threads[] | select(.id == $t) | .status == "addressed" and any(.comments[]; .kind == "agent" and .revision == 2)' >/dev/null; echo $?)"
check "the thread was carried to r2 or marked outdated there" \
  "$(curl -sf "$CP/tasks/$TASK/threads" | jq -e --arg t "$THREAD" '.threads[] | select(.id == $t) | .shownOn == 2 or (.outdated and .outdatedOn == 2)' >/dev/null; echo $?)"

echo
echo "=== the human resolves the thread and approves ==="
curl -sf -X POST "$CP/tasks/$TASK/threads/$THREAD/resolve" -H 'Content-Type: application/json' -d '{"author":"dana"}' | jq -c '{id, status, resolvedBy}'
curl -sf -X POST "$CP/tasks/$TASK/approve" -H 'Content-Type: application/json' \
  -d '{"reviewer":"dana","rationale":"constant named"}' | jq -c .
STATUS=$(wait_until '.status | test("^(MERGED|BLOCKED)$")')
check "approval delivered r2: MERGED ($STATUS)" "$([ "$STATUS" = MERGED ]; echo $?)"
check "the approval left no thread open" \
  "$(curl -sf "$CP/tasks/$TASK/approvals" | jq -e '[.[] | select(.action == "approve")][0] | (.unresolvedThreads // []) | length == 0' >/dev/null; echo $?)"
check "main has the fix with CENTS, a clean checkout, and passes its tests" \
  "$(cd "$REPO" && [ -z "$(git status --porcelain --untracked-files=no)" ] && node -e 'process.exit(require("./src/price.js").CENTS === 100 ? 0 : 1)' && node --test >/dev/null 2>&1; echo $?)"
git -C "$REPO" log --oneline --graph -10 main

echo
if [ ${#FAILS[@]} -eq 0 ]; then echo "PASS"; else printf 'FAIL: %s\n' "${FAILS[@]}"; exit 1; fi
