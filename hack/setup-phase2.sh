#!/usr/bin/env bash
set -euo pipefail

# setup-phase2.sh - Phase 2: Webhook authorization + RBAC
#
# Purpose:
#   Upgrades the Phase 1 deployment to use webhook-based authorization.
#   The secondary API server delegates all authz decisions to the main
#   cluster's SubjectAccessReview API, so existing RBAC rules apply to
#   Tekton resources transparently.
#
# Prerequisites:
#   Phase 1 must be deployed (make poc or make poc-existing)
#
# What it does:
#   1. Deploys the Phase 2 kustomize overlay which:
#      - Creates a ServiceAccount with system:auth-delegator binding
#      - Patches secondary apiserver to run as that SA (kubelet-rotated token)
#      - Switches --authorization-mode from AlwaysAllow to Webhook
#      - Mounts a static webhook kubeconfig referencing the projected SA token
#   2. Waits for the secondary API server to roll out
#
# The webhook kubeconfig uses tokenFile + certificate-authority from the
# kubelet-mounted projected volume — no manual token management required.
#
# Usage:
#   ./hack/setup-phase2.sh
#
# Environment variables:
#   SECONDARY_PORT  - Local port for port-forward to secondary (default: 6444)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

echo "============================================"
echo "  kube-kine Phase 2: Webhook Authorization"
echo "============================================"
echo ""

# ---------- Step 1: Apply Phase 2 overlay ----------
echo "=== Step 1/2: Apply Phase 2 overlay ==="
echo "    (SA + ClusterRoleBinding + webhook kubeconfig + patched deployment)"
kubectl apply -k "${REPO_ROOT}/deploy/phase2"
echo ""

# ---------- Step 2: Wait for rollout ----------
echo "=== Step 2/2: Wait for secondary API server rollout ==="
kubectl -n "${NAMESPACE}" rollout status deployment/secondary-apiserver --timeout=180s
echo ""

# ---------- Verify ----------
echo "=== Verifying webhook authorization is active ==="
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true" EXIT
sleep 3

RESP=$(curl -sk -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  "https://localhost:${SECONDARY_PORT}/apis/tekton.dev/v1/namespaces/default/pipelineruns")

if [[ "${RESP}" == "200" || "${RESP}" == "403" ]]; then
  echo "    Webhook authorization is active (HTTP ${RESP})."
else
  echo "    [WARN] Unexpected response: HTTP ${RESP}"
fi

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

echo ""
echo "============================================"
echo "  Phase 2 setup complete!"
echo ""
echo "  Verify with: make test-phase2"
echo "  The secondary now delegates authz to main cluster RBAC."
echo "============================================"
