#!/usr/bin/env bash
set -euo pipefail

CP=http://localhost:8080
REPO=$(pwd)/tests/evaluations/fixtures/pagination-off-by-one

# Create task
TASK=$(curl -sf -X POST $CP/tasks \
  -H 'Content-Type: application/json' \
  -d "{
    \"requirement\":\"Fix the off-by-one bug in src/paginate.ts. Run tests to confirm.\",
    \"acceptance\":[\"paginate([1,2,3,4,5],3,2) returns [5]\"],
    \"repoPath\":\"$REPO\",
    \"baseCommit\":\"HEAD\",
    \"budget\":{\"maxSteps\":20,\"maxRetries\":2,\"wallClockMs\":600000}
  }" | jq -r .id)
echo "task: $TASK"

# Dispatch run
RUN=$(curl -sf -X POST $CP/tasks/$TASK/runs \
  -H 'Content-Type: application/json' \
  -d '{"provider":"ollama","model":"qwen3-coder:30b"}' | jq -r .runId)
echo "run:  $RUN"

# Poll
for i in $(seq 1 90); do
  STATUS=$(curl -sf $CP/tasks/$TASK | jq -r .status)
  echo "  [$i] $STATUS"
  case "$STATUS" in
    REVIEW_PENDING|BLOCKED|REJECTED) break ;;
  esac
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