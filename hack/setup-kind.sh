#!/usr/bin/env bash
set -euo pipefail

# setup-kind.sh - Creates a kind cluster for the kube-shard PoC.
#
# Purpose:
#   Provisions a single-node kind cluster that acts as the "primary" cluster.
#   The secondary API server components are deployed inside this cluster.
#
# What it does:
#   - Auto-detects docker or podman as the container runtime
#   - Creates a kind cluster with a single control-plane node
#   - Skips creation if the cluster already exists (idempotent)
#
# Environment variables:
#   KIND_CLUSTER_NAME          - Cluster name (default: kube-shard-poc)
#   KIND_EXPERIMENTAL_PROVIDER - Override runtime detection (docker|podman)

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kube-shard-poc}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Auto-detect container runtime
if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
  export KIND_EXPERIMENTAL_PROVIDER="${KIND_EXPERIMENTAL_PROVIDER:-docker}"
elif command -v podman &>/dev/null; then
  export KIND_EXPERIMENTAL_PROVIDER="${KIND_EXPERIMENTAL_PROVIDER:-podman}"
else
  echo "ERROR: Neither docker nor podman found. Install one to continue."
  exit 1
fi
echo "==> Using container runtime: ${KIND_EXPERIMENTAL_PROVIDER}"

if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
  echo "==> Kind cluster '${KIND_CLUSTER_NAME}' already exists, skipping creation."
  kubectl cluster-info --context "kind-${KIND_CLUSTER_NAME}"
  exit 0
fi

echo "==> Creating kind cluster '${KIND_CLUSTER_NAME}'..."

cat <<EOF | kind create cluster --name "${KIND_CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF

kubectl cluster-info --context "kind-${KIND_CLUSTER_NAME}"
echo "==> Kind cluster '${KIND_CLUSTER_NAME}' is ready."
