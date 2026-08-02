#!/usr/bin/env bash
# run-loadtest.sh - Create a batch of large PipelineRuns to stress-test storage.
#
# Usage:
#   ./hack/loadtest/run-loadtest.sh [COUNT] [SIZE_KB] [TASKS] [PARALLEL]
#
# Environment:
#   NAMESPACE     Namespace for PipelineRuns (default: default)
#   PIPELINERUN_YAML  Path to pre-generated YAML (skips generation)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
COUNT="${1:-1000}"
SIZE_KB="${2:-100}"
TASKS="${3:-10}"
PARALLEL="${4:-10}"
NAMESPACE="${NAMESPACE:-default}"

YAML="${PIPELINERUN_YAML:-}"
if [[ -z "$YAML" ]]; then
  YAML=$(mktemp /tmp/loadtest-pipelinerun-XXXXXX.yaml)
  python3 "$SCRIPT_DIR/generate-pipelinerun.py" \
    --size-kb "$SIZE_KB" \
    --tasks "$TASKS" \
    --namespace "$NAMESPACE" \
    -o "$YAML"
fi

START=$(date +%s)
echo ""
echo "=== kube-shard Load Test ==="
echo "PipelineRuns:  $COUNT"
echo "Size:          ~${SIZE_KB} KB each (minimum target)"
echo "Tasks/PR:      $TASKS"
echo "Parallelism:   $PARALLEL"
echo "Namespace:     $NAMESPACE"
echo "Started:       $(date)"
echo ""

CREATED=0
FAILED=0
PIDS=()

for i in $(seq 1 "$COUNT"); do
  kubectl create -f "$YAML" > /dev/null 2>&1 &
  PIDS+=($!)

  if (( ${#PIDS[@]} >= PARALLEL )); then
    for pid in "${PIDS[@]}"; do
      if wait "$pid"; then
        CREATED=$((CREATED + 1))
      else
        FAILED=$((FAILED + 1))
      fi
    done
    PIDS=()
  fi

  if (( i % 100 == 0 )); then
    ELAPSED=$(( $(date +%s) - START ))
    RATE=$(echo "scale=1; $CREATED / ($ELAPSED + 1)" | bc 2>/dev/null || echo "?")
    PR_GB=$(echo "scale=2; $CREATED * $SIZE_KB / 1048576" | bc 2>/dev/null || echo "?")
    MULTIPLIER=$(echo "scale=0; $TASKS + 1" | bc 2>/dev/null || echo "?")
    TOTAL_GB=$(echo "scale=2; $CREATED * $SIZE_KB * $MULTIPLIER / 1048576" | bc 2>/dev/null || echo "?")
    echo "[$(date +%H:%M:%S)] $i / $COUNT | created: $CREATED failed: $FAILED | ~${PR_GB} GB PRs | ~${TOTAL_GB} GB est. w/ TaskRuns | ${RATE}/s | ${ELAPSED}s"
  fi
done

for pid in "${PIDS[@]}"; do
  if wait "$pid"; then
    CREATED=$((CREATED + 1))
  else
    FAILED=$((FAILED + 1))
  fi
done
ELAPSED=$(( $(date +%s) - START ))
echo ""
echo "=== Done ==="
echo "Created:  $CREATED"
echo "Failed:   $FAILED"
echo "Duration: ${ELAPSED}s"
echo ""

if command -v kubectl &>/dev/null; then
  DB_SIZE=$(kubectl exec deployment/konflux-tekton-shard-postgresql -n kube-shard-operator -- \
    psql -U kine -d kine -t -c "SELECT pg_size_pretty(pg_database_size('kine'));" 2>/dev/null | tr -d ' ')
  ROWS=$(kubectl exec deployment/konflux-tekton-shard-postgresql -n kube-shard-operator -- \
    psql -U kine -d kine -t -c "SELECT count(*) FROM kine;" 2>/dev/null | tr -d ' ')
  if [[ -n "$DB_SIZE" ]]; then
    echo "DB size:  $DB_SIZE"
    echo "DB rows:  $ROWS"
  fi
fi
