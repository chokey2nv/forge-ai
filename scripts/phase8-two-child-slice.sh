#!/usr/bin/env bash
# Phase 8 slice with two dependent children.
#
# Exercises what the single-child demo cannot: a multi-node task graph,
# dependency-ordered dispatch, and two child diffs integrating onto one
# parent branch. The fixture is copied into a fresh git repo on every run,
# so runs are independent and leave no branches in the source tree.
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
REPO=$(mktemp -d "${TMPDIR:-/tmp}/forge-slice-repo.XXXXXX")
cp -R "$FIXTURE/." "$REPO/"
git -C "$REPO" init -q
git -C "$REPO" add -A
git -C "$REPO" -c user.name=forge -c user.email=forge@localhost commit -q -m "fixture: cart-pricing"
echo "repo: $REPO"

TASK=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Two pieces of work, delivered as two subtasks. First, fix applyDiscount in src/price.js: it subtracts the percentage as an absolute amount instead of applying a percentage discount rounded to cents. Second, add cartTotal(items, percent) in a new file src/cart.js that sums price * qty over items and applies the discount with applyDiscount, with tests in tests/cart.test.js. The cart subtask depends on the corrected discount, so it must only start after the discount fix is integrated.\",
  \"acceptance\":[
    \"applyDiscount(200, 10) returns 180\",
    \"applyDiscount(19.99, 25) returns 14.99\",
    \"cartTotal([{price: 10, qty: 3}, {price: 5, qty: 2}], 10) returns 36\",
    \"cartTotal([], 10) returns 0\",
    \"npm test passes\"
  ],
  \"repoPath\":\"$REPO\",
  \"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":30,\"maxRetries\":2,\"wallClockMs\":900000}
}" | jq -r .id)
echo "task: $TASK"

RUN=$(curl -sf -X POST "$CP/tasks/$TASK/runs" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"$PROVIDER\",\"model\":\"$MODEL\"}" | jq -r .runId)
echo "run:  $RUN"

LAST="" STATUS=""
for i in $(seq 1 "$POLLS"); do
  STATUS=$(curl -sf "$CP/tasks/$TASK" | jq -r .status)
  if [ "$STATUS" != "$LAST" ]; then echo "  [$i] $STATUS"; LAST=$STATUS; fi
  case "$STATUS" in REVIEW_PENDING|BLOCKED|REJECTED) break ;; esac
  sleep 2
done

PARENT=$(curl -sf "$CP/tasks/$TASK")
CHILDREN=$(curl -sf "$CP/tasks" | jq --arg p "$TASK" '[.[] | select(.parentId == $p)]')

echo
echo "=== children ==="
echo "$CHILDREN" | jq -r '.[] | "\(.id)  \(.status)  agent=\(.assignedAgent)  dependsOn=\(.dependsOn // [] | join(","))  \(.title)"'

FAILS=()
fail() { FAILS+=("$1"); }

[ "$STATUS" = "REVIEW_PENDING" ] || fail "parent ended $STATUS, want REVIEW_PENDING"

N=$(echo "$CHILDREN" | jq length)
[ "$N" -ge 2 ] || fail "head engineer produced $N child(ren), want >= 2"

NOT_INTEGRATED=$(echo "$CHILDREN" | jq -r '[.[] | select(.status != "INTEGRATED") | .id] | join(",")')
[ -z "$NOT_INTEGRATED" ] || fail "children not INTEGRATED: $NOT_INTEGRATED"

# Dependency order: a child's run must start no earlier than each dependency
# integrated (the dependency's last update is its INTEGRATED transition).
ts() { jq -r 'sub("\\.[0-9]+";"") | fromdateiso8601'; }
EDGES=0
for row in $(echo "$CHILDREN" | jq -r '.[] | select((.dependsOn // []) | length > 0) | .id'); do
  started=$(curl -sf "$CP/tasks/$row/runs" | jq '[.[].createdAt] | min' | ts)
  for dep in $(echo "$CHILDREN" | jq -r --arg id "$row" '.[] | select(.id == $id) | .dependsOn[]'); do
    EDGES=$((EDGES + 1))
    integrated=$(echo "$CHILDREN" | jq --arg d "$dep" '.[] | select(.id == $d) | .updatedAt' | ts)
    [ "$started" -ge "$integrated" ] || fail "$row started before its dependency $dep integrated"
  done
done
[ "$EDGES" -ge 1 ] || fail "task graph has no dependency edge; the cart subtask should depend on the discount fix"

BRANCH=$(echo "$PARENT" | jq -r .integrationBranch)
BASE=$(echo "$PARENT" | jq -r .integrationBaseCommit)
WT=$(echo "$PARENT" | jq -r .integrationWorktree)
COMMITS=$(git -C "$REPO" rev-list --count --merges "$BASE..$BRANCH" 2>/dev/null || echo 0)
[ "$COMMITS" -eq "$N" ] || fail "integration branch has $COMMITS merge commit(s) for $N children"

echo
echo "=== integration branch $BRANCH ==="
git -C "$REPO" log --oneline --graph "$BASE..$BRANCH" || true
echo
git -C "$REPO" diff "$BASE..$BRANCH" || true

# Independent of the platform's own gates: the combined result must pass on the host.
echo
echo "=== node --test on the integration worktree ==="
if (cd "$WT" && node --test >/dev/null 2>&1); then echo "pass"; else echo "FAIL"; fail "tests fail on the integrated branch"; fi

echo
if [ ${#FAILS[@]} -eq 0 ]; then
  echo "PASS"
else
  printf 'FAIL: %s\n' "${FAILS[@]}"
  exit 1
fi
