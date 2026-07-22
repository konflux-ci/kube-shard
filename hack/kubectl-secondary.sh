#!/usr/bin/env bash
set -euo pipefail

# kubectl-secondary: a shim that wraps kubectl for direct interaction with the
# secondary API server, bypassing API aggregation.
#
# Usage:
#   ./hack/kubectl-secondary.sh get crds
#   ./hack/kubectl-secondary.sh get pipelineruns -A
#   ./hack/kubectl-secondary.sh create namespace my-namespace
#   ./hack/kubectl-secondary.sh api-resources --api-group=tekton.dev
#
# This is useful for:
#   - Managing CRDs on the secondary (install, update, delete)
#   - Inspecting raw state on the secondary
#   - Creating namespaces on the secondary (Phase 1, before namespace sync)
#   - Debugging aggregation issues by comparing direct vs proxied responses
#
# Environment variables:
#   SECONDARY_PORT  - Local port for port-forward (default: 6444)
#   KEEP_PORT_FWD   - If "true", don't kill port-forward on exit (default: false)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
KEEP_PORT_FWD="${KEEP_PORT_FWD:-false}"
NAMESPACE="tekton-apiserver"

if [[ ! -f "${CERT_DIR}/admin-token" ]]; then
  echo "ERROR: No admin token found at ${CERT_DIR}/admin-token" >&2
  echo "       Run 'make poc' first to generate credentials." >&2
  exit 1
fi

ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")

# Check if port-forward is already running on SECONDARY_PORT
PF_PID=""
if ! curl -sk -o /dev/null "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null; then
  # Start port-forward
  kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver "${SECONDARY_PORT}:443" >/dev/null 2>&1 &
  PF_PID=$!

  if [[ "${KEEP_PORT_FWD}" != "true" ]]; then
    trap "kill ${PF_PID} 2>/dev/null || true" EXIT
  fi

  # Wait for port-forward
  for i in $(seq 1 10); do
    if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
      "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
      break
    fi
    if [[ $i -eq 10 ]]; then
      echo "ERROR: Could not connect to secondary API server on port ${SECONDARY_PORT}" >&2
      exit 1
    fi
    sleep 1
  done
fi

# Execute kubectl against the secondary
exec kubectl \
  --server="https://localhost:${SECONDARY_PORT}" \
  --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  "$@"
