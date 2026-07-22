#!/usr/bin/env bash
set -euo pipefail

# validate-phase2.sh - Validate Phase 2: Webhook authorization + RBAC
#
# Purpose:
#   Tests that the secondary API server correctly delegates authorization
#   decisions to the main cluster's RBAC system via the aggregation layer.
#
# How it works:
#   Requests go through the main API server (aggregation proxy), which
#   authenticates the user's SA token and passes the identity to the
#   secondary via X-Remote-User/X-Remote-Group headers. The secondary
#   then calls SubjectAccessReview on the main cluster to authorize.
#
# What it tests:
#   1. An authorized user (with RoleBinding) can create/list PipelineRuns
#   2. An unauthorized user (no RoleBinding) is denied access
#   3. Cross-namespace isolation works
#   4. The cluster-admin still works through aggregation
#
# Prerequisites:
#   Phase 2 must be deployed (make phase2)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_NS="phase2-test"
PASS=0
FAIL=0
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

pass() { echo "    [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "    [FAIL] $1"; FAIL=$((FAIL + 1)); }

# Generate a kubeconfig that uses ONLY token auth (no client cert)
# This is necessary because kind kubeconfigs use client certs which
# bypass token auth at the TLS layer.
make_token_kubeconfig() {
  local token="$1"
  local kubeconfig="$2"
  local server
  local ca_data

  server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
  ca_data=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' 2>/dev/null || echo "")

  if [[ -z "${ca_data}" ]]; then
    local ca_file
    ca_file=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority}' 2>/dev/null || echo "")
    if [[ -n "${ca_file}" && -f "${ca_file}" ]]; then
      ca_data=$(base64 -w0 < "${ca_file}")
    fi
  fi

  cat > "${kubeconfig}" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: ${server}
    certificate-authority-data: ${ca_data}
users:
- name: test-user
  user:
    token: ${token}
contexts:
- name: test
  context:
    cluster: test
    user: test-user
current-context: test
EOF
}

echo "==> Phase 2 Validation: Webhook Authorization + RBAC"
echo ""

# ---------- Setup test namespace ----------
echo "--- Setting up test namespace '${TEST_NS}'..."
kubectl create namespace "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -

# Mirror namespace to secondary
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true; rm -f /tmp/phase2-*.kubeconfig; kubectl delete namespace ${TEST_NS} --ignore-not-found --wait=false >/dev/null 2>&1 || true" EXIT
sleep 3

# Wait for port-forward
for i in $(seq 1 10); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    break
  fi
  sleep 1
done

# Create test namespace on secondary (direct access with admin token)
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  create namespace "${TEST_NS}" --dry-run=client -o yaml | \
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" apply -f - >/dev/null 2>&1

echo ""

# ---------- Create test users (ServiceAccounts with RoleBindings) ----------
echo "--- Creating test ServiceAccounts and RBAC..."

# Authorized user: has access to PipelineRuns in test namespace
kubectl create serviceaccount authz-user -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -
kubectl create role tekton-user \
  --verb=get,list,create,delete \
  --resource=pipelineruns.tekton.dev,pipelines.tekton.dev,taskruns.tekton.dev \
  -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -
kubectl create rolebinding tekton-user-binding \
  --role=tekton-user \
  --serviceaccount="${TEST_NS}:authz-user" \
  -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -

# Unauthorized user: no Tekton access at all
kubectl create serviceaccount noauthz-user -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -

# Get tokens for test users (short-lived)
AUTHZ_TOKEN=$(kubectl create token authz-user -n "${TEST_NS}" --duration=600s)
NOAUTHZ_TOKEN=$(kubectl create token noauthz-user -n "${TEST_NS}" --duration=600s)

# Create token-only kubeconfigs (no client cert → server uses token auth)
make_token_kubeconfig "${AUTHZ_TOKEN}" /tmp/phase2-authz.kubeconfig
make_token_kubeconfig "${NOAUTHZ_TOKEN}" /tmp/phase2-noauthz.kubeconfig

echo ""

# ---------- Test 1: Authorized user can list PipelineRuns ----------
echo "--- Testing authorized user access (via aggregation)..."

RESULT=$(kubectl --kubeconfig=/tmp/phase2-authz.kubeconfig \
  get pipelineruns -n "${TEST_NS}" -o name 2>&1 && echo "OK" || echo "DENIED")

if echo "${RESULT}" | grep -q "OK\|No resources found"; then
  pass "Authorized user can list PipelineRuns"
else
  fail "Authorized user cannot list PipelineRuns: ${RESULT}"
fi

# ---------- Test 2: Authorized user can create a PipelineRun ----------
cat <<'YAML' | kubectl --kubeconfig=/tmp/phase2-authz.kubeconfig apply -n "${TEST_NS}" -f - 2>&1
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: authz-test-run
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
RESULT=$?

if [[ ${RESULT} -eq 0 ]]; then
  pass "Authorized user can create PipelineRun"
else
  fail "Authorized user cannot create PipelineRun"
fi

echo ""

# ---------- Test 3: Unauthorized user is denied ----------
echo "--- Testing unauthorized user access (via aggregation)..."

RESULT=$(kubectl --kubeconfig=/tmp/phase2-noauthz.kubeconfig \
  get pipelineruns -n "${TEST_NS}" 2>&1 || true)

if echo "${RESULT}" | grep -qi "forbidden\|cannot"; then
  pass "Unauthorized user denied list PipelineRuns"
else
  fail "Unauthorized user NOT denied list (got: ${RESULT})"
fi

RESULT=$(cat <<'YAML' | kubectl --kubeconfig=/tmp/phase2-noauthz.kubeconfig apply -n "${TEST_NS}" -f - 2>&1 || true
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: noauthz-test-run
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

if echo "${RESULT}" | grep -qi "forbidden\|cannot"; then
  pass "Unauthorized user denied create PipelineRun"
else
  fail "Unauthorized user NOT denied create (got: ${RESULT})"
fi

echo ""

# ---------- Test 4: Cross-namespace isolation ----------
echo "--- Testing cross-namespace isolation..."

RESULT=$(kubectl --kubeconfig=/tmp/phase2-authz.kubeconfig \
  get pipelineruns -n default 2>&1 || true)

if echo "${RESULT}" | grep -qi "forbidden\|cannot"; then
  pass "Authorized user denied in other namespace (cross-ns isolation)"
else
  fail "Cross-namespace NOT isolated (got: ${RESULT})"
fi

echo ""

# ---------- Test 5: Admin (cluster-admin) still works via aggregation ----------
echo "--- Testing cluster-admin access via aggregation..."

RESULT=$(kubectl get pipelineruns -n "${TEST_NS}" -o name 2>&1 && echo "OK" || echo "DENIED")
if echo "${RESULT}" | grep -q "OK\|authz-test-run"; then
  pass "Cluster-admin can access PipelineRuns via aggregation"
else
  fail "Cluster-admin access failed: ${RESULT}"
fi

echo ""

# ---------- Test 6: Direct access with admin token still works ----------
echo "--- Testing direct access with static admin token..."

HTTP_CODE=$(curl -sk -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  "https://localhost:${SECONDARY_PORT}/apis/tekton.dev/v1/namespaces/${TEST_NS}/pipelineruns")

if [[ "${HTTP_CODE}" == "200" ]]; then
  pass "Direct access with admin token works (HTTP 200)"
else
  fail "Direct access with admin token got HTTP ${HTTP_CODE} (expected 200)"
fi

echo ""

# ---------- Cleanup ----------
echo "--- Cleaning up test resources..."
kubectl --kubeconfig=/tmp/phase2-authz.kubeconfig delete pipelinerun authz-test-run -n "${TEST_NS}" --ignore-not-found >/dev/null 2>&1 || true
rm -f /tmp/phase2-authz.kubeconfig /tmp/phase2-noauthz.kubeconfig

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

kubectl delete namespace "${TEST_NS}" --ignore-not-found --wait=false >/dev/null 2>&1 &

echo ""
echo "============================================"
echo "  Phase 2 Validation Results"
echo "  Passed: ${PASS}"
echo "  Failed: ${FAIL}"
echo "============================================"

if [[ ${FAIL} -gt 0 ]]; then
  exit 1
fi
