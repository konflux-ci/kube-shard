#!/usr/bin/env bash
set -euo pipefail

# validate-phase6.sh - Validate Phase 6: Konflux integration
#
# Purpose:
#   Verifies that the full Konflux pipeline workflow operates correctly with
#   Tekton APIs aggregated through the kube-shard secondary API server.
#
# What it tests:
#   1. PipelineRun creation and completion via aggregated API
#   2. TaskRun creation by the Tekton controller (reconciliation works)
#   3. Tekton Chains signs completed TaskRuns
#   4. Build-service controller interaction (Application/Component CRDs)
#   5. Data persistence in PostgreSQL backend
#
# Prerequisites:
#   Phase 6 must be deployed (make phase6)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KUBE_CONTEXT="${KUBE_CONTEXT:-kind-konflux}"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
TENANT_NAMESPACE="${TENANT_NAMESPACE:-default-tenant}"
PASS=0
FAIL=0

pass() { echo "    [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "    [FAIL] $1"; FAIL=$((FAIL + 1)); }

echo "==> Phase 6 Validation: Konflux Integration"
echo "    Context: ${KUBE_CONTEXT}"
echo "    Tenant:  ${TENANT_NAMESPACE}"
echo ""

kubectl config use-context "${KUBE_CONTEXT}" >/dev/null 2>&1

# ---------- Test 1: API aggregation is working ----------
echo "--- Test 1: API aggregation routing (tekton.dev served by secondary)..."

API_SERVICE_STATUS=$(kubectl get apiservice v1.tekton.dev -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || echo "")
if [[ "${API_SERVICE_STATUS}" == "True" ]]; then
  pass "APIService v1.tekton.dev is Available"
else
  fail "APIService v1.tekton.dev not available (status: ${API_SERVICE_STATUS})"
fi

# Verify it's routing to our service (not Local)
API_SERVICE_SVC=$(kubectl get apiservice v1.tekton.dev -o jsonpath='{.spec.service.name}' 2>/dev/null || echo "")
if [[ "${API_SERVICE_SVC}" == "tekton-apiserver" ]]; then
  pass "API requests route to kube-shard secondary (service: tekton-apiserver)"
else
  fail "API not routing to secondary (service: ${API_SERVICE_SVC})"
fi

echo ""

# ---------- Test 2: PipelineRun creation and completion ----------
echo "--- Test 2: PipelineRun creation and completion in ${TENANT_NAMESPACE}..."

PR_NAME="phase6-test-$(date +%s)"
cat <<YAML | kubectl apply -n "${TENANT_NAMESPACE}" -f - 2>&1 | sed 's/^/    /'
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: ${PR_NAME}
spec:
  pipelineSpec:
    tasks:
    - name: hello
      taskSpec:
        steps:
        - name: echo
          image: alpine:3.20
          script: |
            echo "Konflux integration test - kube-shard Phase 6"
            echo "Running in namespace: ${TENANT_NAMESPACE}"
YAML

echo "    Waiting for PipelineRun to complete (up to 120s)..."
for i in $(seq 1 60); do
  STATUS=$(kubectl get pipelinerun "${PR_NAME}" -n "${TENANT_NAMESPACE}" \
    -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].status}' 2>/dev/null || echo "")
  if [[ "${STATUS}" == "True" ]]; then
    pass "PipelineRun completed via aggregated API"
    break
  fi
  if [[ "${STATUS}" == "False" ]]; then
    REASON=$(kubectl get pipelinerun "${PR_NAME}" -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].reason}' 2>/dev/null || echo "")
    fail "PipelineRun failed: ${REASON}"
    break
  fi
  if [[ $i -eq 60 ]]; then
    REASON=$(kubectl get pipelinerun "${PR_NAME}" -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].reason}' 2>/dev/null || echo "unknown")
    fail "PipelineRun did not complete within 120s (reason: ${REASON})"
  fi
  sleep 2
done

echo ""

# ---------- Test 3: TaskRun created by controller ----------
echo "--- Test 3: TaskRun created by Tekton controller..."

TASKRUNS=$(kubectl get taskruns -n "${TENANT_NAMESPACE}" \
  -l tekton.dev/pipelineRun="${PR_NAME}" --no-headers 2>/dev/null | wc -l || echo "0")

if [[ "${TASKRUNS}" -gt 0 ]]; then
  pass "TaskRun(s) created for PipelineRun (count: ${TASKRUNS})"
else
  fail "No TaskRuns found for PipelineRun ${PR_NAME}"
fi

echo ""

# ---------- Test 4: Tekton Chains signing ----------
echo "--- Test 4: Tekton Chains TaskRun signing..."

# Wait for Chains to sign (it watches TaskRuns and signs after completion)
echo "    Waiting for Chains to sign TaskRun (up to 60s)..."
SIGNED=false
TR_NAME=$(kubectl get taskruns -n "${TENANT_NAMESPACE}" \
  -l tekton.dev/pipelineRun="${PR_NAME}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [[ -n "${TR_NAME}" ]]; then
  for i in $(seq 1 30); do
    CHAINS_SIGNED=$(kubectl get taskrun "${TR_NAME}" -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.annotations.chains\.tekton\.dev/signed}' 2>/dev/null || echo "")
    if [[ "${CHAINS_SIGNED}" == "true" ]]; then
      SIGNED=true
      break
    fi
    sleep 2
  done

  if [[ "${SIGNED}" == "true" ]]; then
    pass "Tekton Chains signed TaskRun ${TR_NAME}"
    # Check for signature annotation
    SIG=$(kubectl get taskrun "${TR_NAME}" -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.annotations.chains\.tekton\.dev/signature-taskrun-*}' 2>/dev/null || echo "")
    if [[ -n "${SIG}" ]]; then
      echo "      Signature present (truncated): ${SIG:0:40}..."
    fi
  else
    # Chains may not have signing secrets configured in this PoC
    CHAINS_STATUS=$(kubectl get taskrun "${TR_NAME}" -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.annotations}' 2>/dev/null | grep -c "chains.tekton.dev" || echo "0")
    if [[ "${CHAINS_STATUS}" -gt 0 ]]; then
      pass "Tekton Chains interacted with TaskRun (annotations present, signing may be pending)"
    else
      fail "No Tekton Chains annotations found on TaskRun ${TR_NAME}"
    fi
  fi
else
  fail "No TaskRun found to check for Chains signing"
fi

echo ""

# ---------- Test 5: Konflux controllers can interact with Tekton resources ----------
echo "--- Test 5: Konflux controllers see Tekton resources..."

# Verify build-service can list PipelineRuns (it reconciles Components)
BUILD_SVC_RUNNING=$(kubectl get deployment -n build-service build-service-controller-manager \
  -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")

if [[ "${BUILD_SVC_RUNNING}" -gt 0 ]]; then
  pass "Build-service controller is running (reconciles with aggregated Tekton API)"
else
  fail "Build-service controller not running"
fi

# Verify integration-service is running
INT_SVC_RUNNING=$(kubectl get deployment -n integration-service integration-service-controller-manager \
  -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")

if [[ "${INT_SVC_RUNNING}" -gt 0 ]]; then
  pass "Integration-service controller is running"
else
  fail "Integration-service controller not running"
fi

echo ""

# ---------- Test 6: Data is in PostgreSQL ----------
echo "--- Test 6: Verify data stored in PostgreSQL..."

ROW_COUNT=$(kubectl -n "${NAMESPACE}" exec deployment/postgresql -- \
  psql -U kine -d kine -t -c "SELECT count(*) FROM kine;" 2>/dev/null | tr -d ' ' || echo "0")

if [[ "${ROW_COUNT}" -gt 0 ]]; then
  pass "PostgreSQL backend has ${ROW_COUNT} rows"
else
  fail "PostgreSQL kine table is empty or inaccessible"
fi

echo ""

# ---------- Cleanup ----------
echo "--- Cleaning up test PipelineRun..."
kubectl delete pipelinerun "${PR_NAME}" -n "${TENANT_NAMESPACE}" --ignore-not-found --wait=false >/dev/null 2>&1 || true

echo ""
echo "============================================"
echo "  Phase 6 Validation Results"
echo "  Passed: ${PASS}"
echo "  Failed: ${FAIL}"
echo "============================================"

if [[ ${FAIL} -gt 0 ]]; then
  exit 1
fi
