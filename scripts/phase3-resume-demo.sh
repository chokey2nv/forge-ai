#!/usr/bin/env bash
set -euo pipefail

CP=http://localhost:8080
REPO=$(pwd)/tests/evaluations/fixtures/pagination-off-by-one

TASK=$(curl -sf -X POST $CP/tasks -H 'Content-Type: application/json' -d "{
  \"requirement\":\"Fix the off-by-one bug. Also add a test for an empty array.\",
  \"acceptance\":[],
  \"repoPath\":\"$REPO\",\"baseCommit\":\"HEAD\",
  \"budget\":{\"maxSteps\":25,\"maxRetries\":2,\"wallClockMs\":900000}
}" | jq -r .id)

RUN=$(curl -sf -X POST $CP/tasks/$TASK/runs -H 'Content-Type: application/json' \
  -d '{"provider":"ollama","model":"qwen3-coder:30b"}' | jq -r .runId)
echo "task=$TASK run=$RUN"

# Wait for first checkpoint
for i in $(seq 1 60); do
  STEPS=$(curl -sf $CP/runs/$RUN | jq -r .stepCount)
  [ "$STEPS" -ge 1 ] && break
  sleep 1
done
echo "checkpoints flowing (step=$STEPS). Killing adapter..."

# Kill the adapter hard
pkill -9 -f 'tsx src/server.ts' || true

# Wait for stale detector to fire
echo "waiting for RESUMABLE..."
for i in $(seq 1 30); do
  S=$(curl -sf $CP/tasks/$TASK | jq -r .status)
  [ "$S" = "RESUMABLE" ] && break
  sleep 2
done
echo "status: $S"
[ "$S" = "RESUMABLE" ] || { echo "FAIL: not RESUMABLE"; exit 1; }

# Restart adapter
echo "restarting adapter..."
cd services/runtime-adapter && pnpm dev &
ADAPTER_PID=$!
sleep 3
cd -

# Resume
curl -sf -X POST $CP/runs/$RUN/resume | jq .
for i in $(seq 1 120); do
  S=$(curl -sf $CP/tasks/$TASK | jq -r .status)
  echo "  [$i] $S"
  case "$S" in REVIEW_PENDING|BLOCKED|REJECTED) break ;; esac
  sleep 2
done
[ "$S" = "REVIEW_PENDING" ] || { echo "FAIL: $S"; kill $ADAPTER_PID; exit 1; }

echo
echo "=== final diff ==="
curl -sf $CP/runs/$RUN/diff | jq -r .diff
kill $ADAPTER_PID 2>/dev/null || true
echo "PASS"