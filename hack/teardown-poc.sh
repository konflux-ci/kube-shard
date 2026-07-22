#!/usr/bin/env bash
set -euo pipefail

# teardown-poc.sh - Tears down the Phase 1 PoC.
#
# Purpose:
#   Deletes the kind cluster and cleans up generated artifacts (_output/).
#
# Environment variables:
#   KIND_CLUSTER_NAME - Cluster to delete (default: kube-kine-poc)

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kube-kine-poc}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> Tearing down PoC..."

if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
  kind delete cluster --name "${KIND_CLUSTER_NAME}"
  echo "    Kind cluster '${KIND_CLUSTER_NAME}' deleted."
else
  echo "    Kind cluster '${KIND_CLUSTER_NAME}' does not exist."
fi

if [[ -d "${REPO_ROOT}/_output" ]]; then
  rm -rf "${REPO_ROOT}/_output"
  echo "    Cleaned _output/ directory."
fi

echo "==> Teardown complete."
