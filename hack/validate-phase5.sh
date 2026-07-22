#!/usr/bin/env bash
set -euo pipefail

# validate-phase5.sh - Validate Phase 5: PostgreSQL backend
#
# Purpose:
#   Verifies that the secondary API server works correctly with PostgreSQL
#   as the Kine backend, and tests data persistence across Kine restarts.
#
# What it tests:
#   1. PipelineRun creation and completion (basic API aggregation with PostgreSQL)
#   2. Data persistence: restart Kine and verify existing resources survive
#   3. Kueue integration still works with PostgreSQL backend
#   4. Webhook enforcement (Tekton admission webhooks) with PostgreSQL
#   5. RBAC enforcement (authorization webhook) with PostgreSQL
#
# Prerequisites:
#   Phase 5 must be deployed (make phase5)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_NS="phase5-test"
PASS=0
FAIL=0
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

pass() { echo "    [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "    [FAIL] $1"; FAIL=$((FAIL + 1)); }

echo "==> Phase 5 Validation: PostgreSQL Backend"
echo ""

# ---------- Verify PostgreSQL is the backend ----------
echo "--- Verifying Kine is using PostgreSQL..."
KINE_ARGS=$(kubectl -n "${NAMESPACE}" get deployment kine -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null || echo "")
if echo "${KINE_ARGS}" | grep -q "postgres://"; then
  pass "Kine configured with PostgreSQL endpoint"
else
  fail "Kine is NOT using PostgreSQL (args: ${KINE_ARGS})"
fi
echo ""

# ---------- Setup ----------
echo "--- Setting up test namespace '${TEST_NS}'..."
kubectl create namespace "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -

kubectl create serviceaccount phase5-test-user -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f -
kubectl create clusterrole phase5-tekton-admin \
  --verb='*' \
  --resource=pipelineruns.tekton.dev,pipelines.tekton.dev,taskruns.tekton.dev,tasks.tekton.dev \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl create clusterrolebinding phase5-tekton-admin-binding \
  --clusterrole=phase5-tekton-admin \
  --serviceaccount="${TEST_NS}:phase5-test-user" \
  --dry-run=client -o yaml | kubectl apply -f -

# Create LocalQueue in test namespace (required by tekton-kueue webhook)
cat <<EOF | kubectl apply --server-side -f -
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: tekton-queue
  namespace: ${TEST_NS}
spec:
  clusterQueue: tekton-cluster-queue
EOF

# Mirror namespace to secondary
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true; kubectl delete namespace ${TEST_NS} --ignore-not-found --wait=false >/dev/null 2>&1 || true; kubectl delete clusterrole phase5-tekton-admin --ignore-not-found >/dev/null 2>&1 || true; kubectl delete clusterrolebinding phase5-tekton-admin-binding --ignore-not-found >/dev/null 2>&1 || true" EXIT
sleep 3

for i in $(seq 1 15); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    break
  fi
  sleep 2
done

kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  create namespace "${TEST_NS}" --dry-run=client -o yaml | \
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" apply -f - >/dev/null 2>&1

# Generate kubeconfig for test user
TEST_TOKEN=$(kubectl create token phase5-test-user -n "${TEST_NS}" --duration=3600s)
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CA_DATA=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' 2>/dev/null || echo "")
if [[ -z "${CA_DATA}" ]]; then
  CA_FILE=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority}' 2>/dev/null || echo "")
  if [[ -n "${CA_FILE}" && -f "${CA_FILE}" ]]; then
    CA_DATA=$(base64 -w0 < "${CA_FILE}")
  fi
fi

TEST_KUBECONFIG="/tmp/phase5-test.kubeconfig"
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

# ---------- Test 1: PipelineRun creation and completion ----------
echo "--- Test 1: PipelineRun creation and completion (PostgreSQL backend)..."

cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - 2>&1 | sed 's/^/    /'
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: pg-basic-run
spec:
  pipelineSpec:
    tasks:
    - name: hello
      taskSpec:
        steps:
        - name: echo
          image: alpine:3.20
          script: echo "PostgreSQL backend works!"
YAML

echo "    Waiting for PipelineRun to complete (up to 90s)..."
for i in $(seq 1 45); do
  STATUS=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun pg-basic-run -n "${TEST_NS}" \
    -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].status}' 2>/dev/null || echo "")
  if [[ "${STATUS}" == "True" ]]; then
    pass "PipelineRun completed on PostgreSQL backend"
    break
  fi
  if [[ "${STATUS}" == "False" ]]; then
    REASON=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun pg-basic-run -n "${TEST_NS}" \
      -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].reason}' 2>/dev/null || echo "")
    fail "PipelineRun failed: ${REASON}"
    break
  fi
  if [[ $i -eq 45 ]]; then
    fail "PipelineRun did not complete within 90s"
  fi
  sleep 2
done

echo ""

# ---------- Test 2: Data persistence across Kine restart ----------
echo "--- Test 2: Data persistence across Kine restart..."

# Create a sentinel PipelineRun before restart
cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - >/dev/null 2>&1
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: pg-persist-test
spec:
  pipelineSpec:
    tasks:
    - name: sentinel
      taskSpec:
        steps:
        - name: echo
          image: alpine:3.20
          script: echo "persist me"
YAML

# Wait for it to be stored
sleep 5

# Verify it exists before restart
PRE_RESTART=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun pg-persist-test -n "${TEST_NS}" \
  -o jsonpath='{.metadata.name}' 2>/dev/null || echo "")
if [[ "${PRE_RESTART}" != "pg-persist-test" ]]; then
  fail "Sentinel PipelineRun not found before restart"
else
  echo "    Sentinel PipelineRun exists. Restarting Kine..."
  kubectl -n "${NAMESPACE}" rollout restart deployment/kine
  kubectl -n "${NAMESPACE}" rollout status deployment/kine --timeout=120s

  # Wait for secondary to stabilize after Kine restart
  echo "    Waiting for secondary to stabilize after Kine restart..."
  sleep 10
  for i in $(seq 1 20); do
    if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
      "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
      break
    fi
    sleep 3
  done

  # Check if the sentinel PipelineRun survived
  POST_RESTART=$(kubectl --kubeconfig="${TEST_KUBECONFIG}" get pipelinerun pg-persist-test -n "${TEST_NS}" \
    -o jsonpath='{.metadata.name}' 2>/dev/null || echo "")
  if [[ "${POST_RESTART}" == "pg-persist-test" ]]; then
    pass "Data persisted across Kine restart"
  else
    fail "Sentinel PipelineRun lost after Kine restart"
  fi
fi

echo ""

# ---------- Test 3: Webhook enforcement still works ----------
echo "--- Test 3: Tekton admission webhook enforcement (PostgreSQL backend)..."

# Invalid PipelineRun should be rejected
RESULT=$(cat <<'YAML' | kubectl --kubeconfig="${TEST_KUBECONFIG}" apply -n "${TEST_NS}" -f - 2>&1 || true
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: pg-invalid-run
spec: {}
YAML
)

if echo "${RESULT}" | grep -qi "denied\|invalid\|missing\|rejected\|error\|admission\|pipelineRef\|pipelineSpec"; then
  pass "Invalid PipelineRun rejected by webhook (PostgreSQL backend)"
else
  fail "Invalid PipelineRun was NOT rejected: ${RESULT}"
fi

echo ""

# ---------- Test 4: RBAC enforcement still works ----------
echo "--- Test 4: RBAC enforcement (PostgreSQL backend)..."

# Create a user with no Tekton permissions
kubectl create serviceaccount phase5-no-access -n "${TEST_NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
NO_ACCESS_TOKEN=$(kubectl create token phase5-no-access -n "${TEST_NS}" --duration=3600s)

NO_ACCESS_KUBECONFIG="/tmp/phase5-noaccess.kubeconfig"
cat > "${NO_ACCESS_KUBECONFIG}" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: ${SERVER}
    certificate-authority-data: ${CA_DATA}
users:
- name: no-access
  user:
    token: ${NO_ACCESS_TOKEN}
contexts:
- name: test
  context:
    cluster: test
    user: no-access
current-context: test
EOF

RESULT=$(kubectl --kubeconfig="${NO_ACCESS_KUBECONFIG}" get pipelineruns -n "${TEST_NS}" 2>&1 || true)
if echo "${RESULT}" | grep -qi "forbidden\|cannot"; then
  pass "Unauthorized user cannot list PipelineRuns (RBAC enforced)"
else
  fail "Unauthorized user could list PipelineRuns: ${RESULT}"
fi
rm -f "${NO_ACCESS_KUBECONFIG}"

echo ""

# ---------- Test 5: PostgreSQL data verification ----------
echo "--- Test 5: Verify data is stored in PostgreSQL..."

ROW_COUNT=$(kubectl -n "${NAMESPACE}" exec deployment/postgresql -- \
  psql -U kine -d kine -t -c "SELECT count(*) FROM kine;" 2>/dev/null | tr -d ' ' || echo "0")

if [[ "${ROW_COUNT}" -gt 0 ]]; then
  pass "PostgreSQL has ${ROW_COUNT} rows in kine table"
else
  fail "PostgreSQL kine table is empty or inaccessible (count: ${ROW_COUNT})"
fi

echo ""

# ---------- Cleanup ----------
echo "--- Cleaning up test resources..."
kubectl --kubeconfig="${TEST_KUBECONFIG}" delete pipelinerun --all -n "${TEST_NS}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
rm -f "${TEST_KUBECONFIG}"

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

kubectl delete namespace "${TEST_NS}" --ignore-not-found --wait=false >/dev/null 2>&1 &
kubectl delete clusterrole phase5-tekton-admin --ignore-not-found >/dev/null 2>&1 || true
kubectl delete clusterrolebinding phase5-tekton-admin-binding --ignore-not-found >/dev/null 2>&1 || true

echo ""
echo "============================================"
echo "  Phase 5 Validation Results"
echo "  Passed: ${PASS}"
echo "  Failed: ${FAIL}"
echo "============================================"

if [[ ${FAIL} -gt 0 ]]; then
  exit 1
fi
