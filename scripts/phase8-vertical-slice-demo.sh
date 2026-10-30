#!/usr/bin/env bash
set -euo pipefail
CP=http://localhost:8080
REPO=$(pwd)/tests/evaluations/fixtures/pagination-off-by-one

TASK=$(curl -sf -X POST $CP/tasks -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Fix the off-by-one pagination bug and add regression tests for empty input and out-of-range pages.\",
  \"acceptance\":[
    \"paginate([1,2,3,4,5], 3, 2) returns [5]\",
    \"paginate([], 1, 10) returns []\",
    \"paginate([1,2,3], 0, 2) returns []\"
  ],
  \"repoPath\":\"$REPO\",
  \"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":30,\"maxRetries\":2,\"wallClockMs\":900000}
}" | jq -r .id)
echo "task: $TASK"

RUN=$(curl -sf -X POST $CP/tasks/$TASK/runs -H 'Content-Type: application/json' \
  -d '{"provider":"ollama","model":"qwen3-coder:30b"}' | jq -r .runId)
echo "run:  $RUN"

LAST=""
for i in $(seq 1 300); do
  STATUS=$(curl -sf $CP/tasks/$TASK | jq -r .status)
  if [ "$STATUS" != "$LAST" ]; then echo "  [$i] $STATUS"; LAST=$STATUS; fi
  case "$STATUS" in REVIEW_PENDING|BLOCKED|REJECTED) break ;; esac
  sleep 2
done

echo
echo "=== task ==="
curl -sf $CP/tasks/$TASK | jq .
echo
echo "=== run ==="
curl -sf $CP/runs/$RUN | jq .
echo
echo "=== events ==="
curl -sf $CP/runs/$RUN/events | jq -r '.[] | "\(.at)  \(.type)  \(.from // "") -> \(.to // "")  \(.reason // "")"'
echo
echo "=== diff ==="
curl -sf $CP/runs/$RUN/diff | jq -r .diff

[ "$STATUS" = "REVIEW_PENDING" ] || { echo "FAIL: $STATUS"; exit 1; }
echo "PASS"
