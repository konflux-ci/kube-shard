#!/usr/bin/env bash
set -euo pipefail

# setup-phase5.sh - Phase 5: PostgreSQL backend
#
# Purpose:
#   Switches the Kine storage backend from SQLite to in-cluster PostgreSQL.
#   Deploys PostgreSQL in the tekton-apiserver namespace and reconfigures
#   Kine to use the PostgreSQL connection string.
#
# Prerequisites:
#   Phase 3 must be deployed (make phase3). Phase 4 (Kueue) is independent
#   and can be applied before or after this phase.
#
# What it does:
#   1. Applies the Phase 5 kustomize overlay (adds PostgreSQL, patches Kine)
#   2. Waits for PostgreSQL to be ready
#   3. Waits for Kine to reconnect with the new backend
#   4. Waits for the secondary API server to stabilize
#   5. Re-registers Tekton CRDs and webhook configs on the secondary
#      (new PostgreSQL backend starts empty)
#
# Environment variables:
#   SECONDARY_PORT  - Local port for port-forward to secondary (default: 6444)
#
# Note: Switching from SQLite to PostgreSQL loses existing data since the
# new backend starts empty. CRDs and webhook configs must be re-applied.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

echo "============================================"
echo "  kube-kine Phase 5: PostgreSQL Backend"
echo "============================================"
echo ""

# ---------- Step 1: Apply Phase 5 overlay ----------
echo "=== Step 1/5: Apply Phase 5 overlay (PostgreSQL + Kine reconfiguration) ==="
kubectl apply -k "${REPO_ROOT}/deploy/phase5"
echo ""

# ---------- Step 2: Wait for PostgreSQL ----------
echo "=== Step 2/5: Wait for PostgreSQL to be ready ==="
kubectl -n "${NAMESPACE}" rollout status deployment/postgresql --timeout=180s

# Verify PostgreSQL is accepting connections
echo "    Verifying PostgreSQL connectivity..."
for i in $(seq 1 30); do
  if kubectl -n "${NAMESPACE}" exec deployment/postgresql -- \
    pg_isready -U kine -d kine 2>/dev/null | grep -q "accepting connections"; then
    echo "    PostgreSQL is accepting connections."
    break
  fi
  if [[ $i -eq 30 ]]; then
    echo "    [ERROR] PostgreSQL not ready after 60s."
    exit 1
  fi
  sleep 2
done
echo ""

# ---------- Step 3: Wait for Kine to reconnect ----------
echo "=== Step 3/5: Restart Kine (ensure clean connection to PostgreSQL) ==="
kubectl -n "${NAMESPACE}" rollout restart deployment/kine
kubectl -n "${NAMESPACE}" rollout status deployment/kine --timeout=180s

# Verify Kine is healthy
echo "    Verifying Kine connectivity (etcd endpoint)..."
for i in $(seq 1 20); do
  if kubectl -n "${NAMESPACE}" exec deployment/kine -- \
    wget -q -O /dev/null http://localhost:2379/health 2>/dev/null; then
    echo "    Kine is healthy."
    break
  fi
  # Alternative: check TCP
  KINE_READY=$(kubectl -n "${NAMESPACE}" get pods -l app=kine \
    -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
  if [[ "${KINE_READY}" == "True" ]]; then
    echo "    Kine pod is ready."
    break
  fi
  if [[ $i -eq 20 ]]; then
    echo "    [WARN] Kine health check inconclusive, proceeding..."
  fi
  sleep 3
done
echo ""

# ---------- Step 4: Restart secondary API server ----------
echo "=== Step 4/5: Restart secondary API server (clear stale revision cache) ==="
kubectl -n "${NAMESPACE}" rollout restart deployment/secondary-apiserver
kubectl -n "${NAMESPACE}" rollout status deployment/secondary-apiserver --timeout=180s

ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true" EXIT
sleep 3

echo "    Waiting for secondary API server health..."
for i in $(seq 1 20); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    echo "    Secondary API server is healthy."
    break
  fi
  if [[ $i -eq 20 ]]; then
    echo "    [ERROR] Secondary API server not healthy after 60s."
    exit 1
  fi
  sleep 3
done
echo ""

# ---------- Step 5: Re-apply CRDs and webhook configs ----------
echo "=== Step 5/5: Re-apply Tekton CRDs and webhook configs to secondary ==="
echo "    (New PostgreSQL backend starts empty; CRDs and webhooks must be reinstalled)"

# Re-apply Tekton CRDs
TEKTON_VERSION="v0.65.2"
CRD_FILE="${REPO_ROOT}/_output/tekton/crds-${TEKTON_VERSION}.yaml"
if [[ -f "${CRD_FILE}" ]]; then
  echo "    Applying Tekton CRDs to secondary..."
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    apply -f "${CRD_FILE}" 2>&1 | sed 's/^/      /'
else
  echo "    [WARN] CRD file not found: ${CRD_FILE}"
  echo "    Run 'make poc' first to download Tekton release."
fi

# Disable non-functional CRD conversion webhooks (same as Phase 3)
echo "    Disabling non-functional CRD conversion webhooks..."
TEKTON_CRDS=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get crd -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep "tekton.dev" || true)

for crd in ${TEKTON_CRDS}; do
  CONV_STRATEGY=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    get crd "${crd}" -o jsonpath='{.spec.conversion.strategy}' 2>/dev/null || echo "None")

  if [[ "${CONV_STRATEGY}" == "Webhook" ]]; then
    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
      --token="${ADMIN_TOKEN}" \
      patch crd "${crd}" --type=json \
      -p '[{"op": "replace", "path": "/spec/conversion", "value": {"strategy": "None"}}]' >/dev/null 2>&1
  fi
done
echo "    CRD conversion webhooks disabled."

# Re-apply webhook configs from Phase 3 output
PHASE3_DIR="${REPO_ROOT}/_output/phase3"
if [[ -d "${PHASE3_DIR}" ]]; then
  echo "    Applying Tekton webhook configs to secondary..."
  for f in "${PHASE3_DIR}/validating-webhooks.yaml" "${PHASE3_DIR}/mutating-webhooks.yaml"; do
    if [[ -f "$f" ]] && grep -q "kind:" "$f"; then
      kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
        --token="${ADMIN_TOKEN}" \
        apply -f "$f" 2>&1 | sed 's/^/      /'
    fi
  done
else
  echo "    [WARN] Phase 3 webhook configs not found. Run 'make phase3' to generate them."
fi

# Re-apply tekton-kueue webhook config (Phase 4)
PHASE4_DIR="${REPO_ROOT}/_output/phase4"
if [[ -f "${PHASE4_DIR}/tekton-kueue-webhook-clean.yaml" ]]; then
  echo "    Applying tekton-kueue webhook config to secondary..."
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    apply -f "${PHASE4_DIR}/tekton-kueue-webhook-clean.yaml" 2>&1 | sed 's/^/      /'
fi

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

echo ""
echo "============================================"
echo "  Phase 5 setup complete!"
echo ""
echo "  Backend: PostgreSQL (in-cluster)"
echo "  Connection: postgres://kine:***@postgresql.tekton-apiserver.svc:5432/kine"
echo ""
echo "  Verify with: make test-phase5"
echo "============================================"
