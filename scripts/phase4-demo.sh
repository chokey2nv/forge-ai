#!/usr/bin/env bash
set -euo pipefail
CP=http://localhost:8080
REPO=$(pwd)/tests/evaluations/fixtures/pagination-off-by-one

TASK=$(curl -sf -X POST $CP/tasks -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Fix the off-by-one bug in src/paginate.js so the last page includes the correct item. Run tests to confirm.\",
  \"repoPath\":\"$REPO\",\"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":20,\"maxRetries\":2,\"wallClockMs\":600000}
}" | jq -r .id)
echo "task: $TASK"

RUN=$(curl -sf -X POST $CP/tasks/$TASK/runs -H 'Content-Type: application/json' \
  -d '{"provider":"ollama","model":"qwen3-coder:30b"}' | jq -r .runId)
echo "run:  $RUN"

LAST=""
for i in $(seq 1 180); do
  STATUS=$(curl -sf $CP/tasks/$TASK | jq -r .status)
  if [ "$STATUS" != "$LAST" ]; then echo "  [$i] $STATUS"; LAST=$STATUS; fi
  case "$STATUS" in REVIEW_PENDING|BLOCKED|REJECTED) break ;; esac
  sleep 2
done

echo
echo "=== verification ==="
curl -sf $CP/runs/$RUN/verification | jq '{passed, attempt, gates: [.gates[] | {name, passed, skipped, durationMs}]}'
echo
echo "=== events ==="
curl -sf $CP/runs/$RUN/events | jq -r '.[] | "\(.at)  \(.type)  \(.reason // "")"'

[ "$STATUS" = "REVIEW_PENDING" ] || { echo "FAIL: $STATUS"; exit 1; }
echo "PASS"