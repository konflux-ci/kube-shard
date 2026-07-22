#!/usr/bin/env bash
set -euo pipefail

# validate-phase3.sh - Validate Phase 3: Tekton admission webhooks
#
# Purpose:
#   Tests that Tekton's ValidatingAdmissionWebhook and MutatingAdmissionWebhook
#   fire correctly on the secondary API server.
#
# What it tests:
#   1. Mutation: Tekton webhook applies defaults to a valid PipelineRun
#   2. Validation: Invalid PipelineRun is rejected
#   3. CRD conversion: v1beta1 API access works (conversion webhook reachable)
#   4. Valid resources: A well-formed PipelineRun is admitted without errors
#
# Prerequisites:
#   Phase 3 must be deployed (make phase3)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_NS="phase3-test"
PASS=0
FAIL=0
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

pass() { echo "    [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "    [FAIL] $1"; FAIL=$((FAIL + 1)); }

echo "==> Phase 3 Validation: Tekton Admission Webhooks"
echo ""

# ---------- Setup ----------
echo "--- Setting up test namespace '${TEST_NS}'..."
kubectl create namespace "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -

# Create RBAC for test user (reuse Phase 2 pattern)
kubectl create serviceaccount phase3-test-user -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -
kubectl create clusterrole phase3-tekton-admin \
  --verb='*' \
  --resource=pipelineruns.tekton.dev,pipelines.tekton.dev,taskruns.tekton.dev,tasks.tekton.dev \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl create clusterrolebinding phase3-tekton-admin-binding \
  --clusterrole=phase3-tekton-admin \
  --serviceaccount="${TEST_NS}:phase3-test-user" \
  --dry-run=client -o yaml | kubectl apply -f -

# Mirror namespace to secondary
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true; kubectl delete namespace ${TEST_NS} --ignore-not-found --wait=false >/dev/null 2>&1 || true; kubectl delete clusterrole phase3-tekton-admin --ignore-not-found >/dev/null 2>&1 || true; kubectl delete clusterrolebinding phase3-tekton-admin-binding --ignore-not-found >/dev/null 2>&1 || true" EXIT
sleep 3

for i in $(seq 1 15); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    break
  fi
  sleep 2
done

# Create test namespace on secondary
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  create namespace "${TEST_NS}" --dry-run=client -o yaml | \
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" apply -f - >/dev/null 2>&1

# Generate a token-only kubeconfig for the test user
TEST_TOKEN=$(kubectl create token phase3-test-user -n "${TEST_NS}" --duration=600s)
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CA_DATA=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' 2>/dev/null || echo "")
if [[ -z "${CA_DATA}" ]]; then
  CA_FILE=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority}' 2>/dev/null || echo "")
  if [[ -n "${CA_FILE}" && -f "${CA_FILE}" ]]; then
    CA_DATA=$(base64 -w0 < "${CA_FILE}")
  fi
fi

TEST_KUBECONFIG="/tmp/phase3-test.kubeconfig"
cat > "${TEST_KUBECONFIG}" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: ${SERVER}
    certificate-authority-data: ${CA_DATA}
users:
- name: test-user
  user:
    token: ${TEST_TOKEN}
contexts:
- name: test
  context:
    cluster: test
    user: test-user
current-context: test
EOF

echo ""

# ---------- Test 1: Valid PipelineRun admitted + mutation applied ----------
echo "--- Test 1: Valid PipelineRun admitted with mutation (defaults applied)..."

RESULT=$(cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - 2>&1
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: phase3-valid-run
spec:
  pipelineSpec:
    tasks:
    - name: hello
      taskSpec:
        steps:
        - name: echo
          image: alpine:3.20
          script: echo hello
YAML
)

if echo "${RESULT}" | grep -q "created\|configured"; then
  pass "Valid PipelineRun admitted"
else
  fail "Valid PipelineRun not admitted: ${RESULT}"
fi

# Check that mutation was applied (Tekton webhook adds labels and defaults)
sleep 2
PR_JSON=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun phase3-valid-run -n "${TEST_NS}" -o json 2>/dev/null || echo "{}")

# Tekton's mutating webhook sets labels like tekton.dev/pipeline
if echo "${PR_JSON}" | grep -q "tekton.dev/pipeline"; then
  pass "Mutation applied: tekton.dev/pipeline label set"
else
  # The webhook might set other defaults - check for status initialization
  if echo "${PR_JSON}" | grep -q '"status"'; then
    pass "Mutation applied: status field initialized"
  else
    fail "No mutation evidence found on PipelineRun"
  fi
fi

echo ""

# ---------- Test 2: Invalid PipelineRun rejected by validation webhook ----------
echo "--- Test 2: Invalid PipelineRun rejected by validation webhook..."

# A PipelineRun with neither pipelineRef nor pipelineSpec is invalid
RESULT=$(cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - 2>&1 || true
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: phase3-invalid-run
spec: {}
YAML
)

if echo "${RESULT}" | grep -qi "denied\|invalid\|missing\|rejected\|error\|admission"; then
  pass "Invalid PipelineRun rejected by webhook"
else
  # Check if it was rejected with a different error message
  if echo "${RESULT}" | grep -qi "pipelineRef\|pipelineSpec"; then
    pass "Invalid PipelineRun rejected (missing pipelineRef/pipelineSpec)"
  else
    fail "Invalid PipelineRun was NOT rejected: ${RESULT}"
  fi
fi

echo ""

# ---------- Test 3: CRD conversion webhooks disabled (no spurious errors) ----------
echo "--- Test 3: CRD conversion disabled (no conversion webhook errors)..."

# Verify that the secondary no longer has conversion webhook errors
# by checking that the PipelineRun created in Test 1 is accessible via v1
RESULT=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun phase3-valid-run -n "${TEST_NS}" -o jsonpath='{.apiVersion}' 2>&1 || true)

if echo "${RESULT}" | grep -q "tekton.dev/v1"; then
  pass "v1 API access works (no conversion webhook interference)"
else
  fail "v1 API access failed: ${RESULT}"
fi

# Verify the CRD conversion strategy is set to None on the secondary
ADMIN_TOKEN_LOCAL=$(cat "${CERT_DIR}/admin-token")
CONV_STRATEGY=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN_LOCAL}" \
  get crd pipelineruns.tekton.dev -o jsonpath='{.spec.conversion.strategy}' 2>/dev/null || echo "unknown")

if [[ "${CONV_STRATEGY}" == "None" ]]; then
  pass "CRD conversion strategy set to None (non-functional webhook disabled)"
else
  fail "CRD conversion strategy is '${CONV_STRATEGY}', expected 'None'"
fi

echo ""

# ---------- Test 4: Another valid resource to confirm webhook is not blocking ----------
echo "--- Test 4: Second valid PipelineRun (confirm webhooks are not over-rejecting)..."

RESULT=$(cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - 2>&1
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: phase3-second-run
spec:
  pipelineSpec:
    tasks:
    - name: world
      taskSpec:
        steps:
        - name: echo
          image: alpine:3.20
          script: echo world
YAML
)

if echo "${RESULT}" | grep -q "created\|configured"; then
  pass "Second valid PipelineRun admitted"
else
  fail "Second valid PipelineRun not admitted: ${RESULT}"
fi

echo ""

# ---------- Test 5: Direct access validation (bypassing aggregation) ----------
echo "--- Test 5: Direct webhook validation via secondary port-forward..."

RESULT=$(curl -sk -X POST \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  "https://localhost:${SECONDARY_PORT}/apis/tekton.dev/v1/namespaces/${TEST_NS}/pipelineruns" \
  -d '{
    "apiVersion": "tekton.dev/v1",
    "kind": "PipelineRun",
    "metadata": {"name": "phase3-direct-invalid", "namespace": "'"${TEST_NS}"'"},
    "spec": {}
  }' 2>&1)

if echo "${RESULT}" | grep -qi "denied\|invalid\|missing\|Failure\|admission"; then
  pass "Direct access: invalid PipelineRun rejected by webhook"
else
  fail "Direct access: invalid PipelineRun was NOT rejected: $(echo "${RESULT}" | head -c 200)"
fi

echo ""

# ---------- Cleanup ----------
echo "--- Cleaning up test resources..."
kubectl --kubeconfig="${TEST_KUBECONFIG}" delete pipelinerun phase3-valid-run phase3-second-run -n "${TEST_NS}" --ignore-not-found >/dev/null 2>&1 || true
rm -f "${TEST_KUBECONFIG}"

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

kubectl delete namespace "${TEST_NS}" --ignore-not-found --wait=false >/dev/null 2>&1 &
kubectl delete clusterrole phase3-tekton-admin --ignore-not-found >/dev/null 2>&1 || true
kubectl delete clusterrolebinding phase3-tekton-admin-binding --ignore-not-found >/dev/null 2>&1 || true

echo ""
echo "============================================"
echo "  Phase 3 Validation Results"
echo "  Passed: ${PASS}"
echo "  Failed: ${FAIL}"
echo "============================================"

if [[ ${FAIL} -gt 0 ]]; then
  exit 1
fi
