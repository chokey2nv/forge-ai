#!/usr/bin/env bash
# Phase 10 slice with a real model: two independent subtasks that should run
# in parallel, and a third that depends on both.
#
# The fixture (tests/evaluations/fixtures/parallel-features) starts with a
# passing suite and is copied into a fresh git repo on every run. Besides
# the usual outcome checks, the script reports wall time against the sum of
# the subtasks' own durations: the gain from running them in parallel.
set -euo pipefail

# Signed API calls and a project to work in (Phase 13.2, 13.4).
source "$(dirname "$0")/lib/forge-api.sh"

CP=${CP:-http://localhost:8080}
PROVIDER=${PROVIDER:-claude}
MODEL=${MODEL:-claude-haiku-5-5}
POLLS=${POLLS:-900} # x 2s

FIXTURE=$(pwd)/tests/evaluations/fixtures/parallel-features
REPO=$(mktemp -d "${TMPDIR:-/tmp}/forge-parallel-repo.XXXXXX")
cp -R "$FIXTURE/." "$REPO/"
git -C "$REPO" init -q
git -C "$REPO" add -A
git -C "$REPO" -c user.name=forge -c user.email=forge@localhost commit -q -m "fixture: parallel-features"
echo "repo: $REPO"

forge_register_repo "$REPO"
TASK=$(curl -sf -X POST "$CP/tasks" -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Add three pricing helpers, each in its own module with its own test file, as three subtasks. (1) taxFor(amount, ratePercent) in src/tax.js, returning the tax rounded to cents, tested in tests/tax.test.js. (2) formatPrice(amount) in src/format.js, returning a dollar string with two decimals, tested in tests/format.test.js. (3) priceLabel(amount, ratePercent) in src/label.js, returning formatPrice(amount + taxFor(amount, ratePercent)) followed by ' incl. tax', tested in tests/label.test.js. Subtasks 1 and 2 are independent and touch only their own two files; declare those files as each subtask's diffScope so they can run in parallel. Subtask 3 depends on both.\",
  \"acceptance\":[
    \"taxFor(50, 8) returns 4\",
    \"formatPrice(4) returns '\$4.00'\",
    \"priceLabel(50, 8) returns '\$54.00 incl. tax'\",
    \"npm test passes\"
  ],
  \"projectId\":\"$PROJECT\",\"repoPath\":\"$REPO\",
  \"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":30,\"maxRetries\":2,\"wallClockMs\":900000}
}" | jq -r .id)
echo "task: $TASK"
curl -sf -X POST "$CP/tasks/$TASK/runs" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"$PROVIDER\",\"model\":\"$MODEL\"}" >/dev/null

LAST="" STATUS=""
for i in $(seq 1 "$POLLS"); do
  STATUS=$(curl -sf "$CP/tasks/$TASK" | jq -r .status)
  if [ "$STATUS" != "$LAST" ]; then echo "  [$i] $STATUS"; LAST=$STATUS; fi
  case "$STATUS" in REVIEW_PENDING|BLOCKED|REJECTED|ESCALATED) break ;; esac
  sleep 2
done

PARENT=$(curl -sf "$CP/tasks/$TASK")
CHILDREN=$(curl -sf "$CP/tasks/$TASK/children")
N=$(echo "$CHILDREN" | jq length)

FAILS=()
fail() { FAILS+=("$1"); }

# Epoch seconds (fractional) of a task's first transition to a status, or empty.
at() { # task status
  curl -sf "$CP/tasks/$1/events" | jq -r --arg to "$2" \
    '[.[] | select(.type == "state.transition" and .to == $to)][0].at // empty
     | capture("^(?<s>[^.Z]+)(\\.(?<f>[0-9]+))?Z$")
     | ((.s + "Z") | fromdateiso8601) + (("0." + (.f // "0")) | tonumber)'
}

# Parallel arrays (bash 3.2 has no associative arrays): id, claim, integration.
IDS=() START=() END=()
for id in $(echo "$CHILDREN" | jq -r '.[].id'); do
  IDS+=("$id"); START+=("$(at "$id" DISPATCHED)"); END+=("$(at "$id" INTEGRATED)")
done

echo
echo "=== subtasks ==="
SUM=0
for ((x = 0; x < ${#IDS[@]}; x++)); do
  dur="-"
  if [ -n "${START[$x]}" ] && [ -n "${END[$x]}" ]; then
    dur=$(echo "${END[$x]} - ${START[$x]}" | bc)
    SUM=$(echo "$SUM + $dur" | bc)
  fi
  echo "$CHILDREN" | jq -r --arg id "${IDS[$x]}" --arg d "$dur" '.[] | select(.id == $id) |
    "\(.status)  \($d)s  scope=\(.diffScope // [] | join(","))  dependsOn=\(.dependsOn // [] | length)  implicit=\(.implicitDependsOn // [] | length)  \(.title)"'
done

[ "$STATUS" = "REVIEW_PENDING" ] || fail "parent ended $STATUS, want REVIEW_PENDING"
[ "$N" -ge 3 ] || fail "head engineer produced $N subtask(s), want >= 3"
NOT=$(echo "$CHILDREN" | jq -r '[.[] | select(.status != "INTEGRATED") | .title] | join(", ")')
[ -z "$NOT" ] || fail "subtasks not INTEGRATED: $NOT"

# At least one pair of subtasks must have overlapped (claimed .. integrated).
OVERLAP=""
for ((x = 0; x < ${#IDS[@]}; x++)); do
  for ((y = x + 1; y < ${#IDS[@]}; y++)); do
    [ -n "${START[$x]}" ] && [ -n "${END[$x]}" ] && [ -n "${START[$y]}" ] && [ -n "${END[$y]}" ] || continue
    if [ "$(echo "${START[$x]} < ${END[$y]} && ${START[$y]} < ${END[$x]}" | bc)" = 1 ]; then OVERLAP=yes; fi
  done
done
if [ -z "$OVERLAP" ]; then
  fail "no two subtasks ran at the same time"
  echo "  serialization reasons:"
  for id in ${IDS[@]+"${IDS[@]}"}; do
    curl -sf "$CP/tasks/$id/events" | jq -r '.[] | select(.type == "child.serialized") | "    " + .reason'
  done
fi

# Wall time of the subtask phase versus the work it contained.
FIRST=$(printf '%s\n' ${START[@]+"${START[@]}"} | grep . | sort -n | head -1 || true)
LASTEND=$(printf '%s\n' ${END[@]+"${END[@]}"} | grep . | sort -n | tail -1 || true)
if [ -n "$FIRST" ] && [ -n "$LASTEND" ]; then
  WALL=$(echo "$LASTEND - $FIRST" | bc)
  echo
  printf "subtask phase: wall %.0fs for %.0fs of subtask work (%.2fx)\n" "$WALL" "$SUM" "$(echo "$SUM / $WALL" | bc -l)"
  [ "$(echo "$WALL < $SUM" | bc)" = 1 ] || fail "wall time ${WALL}s is not below the summed subtask time ${SUM}s"
fi

BRANCH=$(echo "$PARENT" | jq -r .integrationBranch)
BASE=$(echo "$PARENT" | jq -r .integrationBaseCommit)
WT=$(echo "$PARENT" | jq -r .integrationWorktree)
COMMITS=$(git -C "$REPO" rev-list --count --merges "$BASE..$BRANCH" 2>/dev/null || echo 0)
[ "$COMMITS" -eq "$N" ] || fail "integration branch has $COMMITS merge commit(s) for $N subtasks"

echo
echo "=== integration branch $BRANCH ==="
git -C "$REPO" log --oneline --graph "$BASE..$BRANCH" || true

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
