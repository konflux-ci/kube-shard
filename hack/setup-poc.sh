#!/usr/bin/env bash
set -euo pipefail

# setup-poc.sh - Main orchestration for the Phase 1 PoC.
#
# Purpose:
#   Deploys the complete kube-shard stack, validating that API aggregation works
#   end-to-end with Tekton controllers.
#
# Supports two modes:
#   1. Fresh kind cluster (default): creates a new kind cluster from scratch
#   2. Existing cluster: deploys onto the current kubectl context
#
# What it does:
#   1. (Optional) Creates a kind cluster
#   2. Generates TLS certificates and a static admin token
#   3. Deploys the tekton-apiserver namespace and injects certs as a Secret
#   4. Deploys Kine (etcd-to-SQL shim) with a SQLite backend
#   5. Deploys the secondary kube-apiserver pointed at Kine
#   6. Port-forwards to the secondary and installs Tekton CRDs directly
#   7. Removes existing Tekton CRDs from primary (if present), registers
#      APIService objects, and (optionally) installs the Tekton controller
#
# Usage:
#   # Fresh kind cluster (default)
#   ./hack/setup-poc.sh
#
#   # Deploy on existing cluster (uses current kubectl context)
#   USE_EXISTING_CLUSTER=true ./hack/setup-poc.sh
#
#   # Existing cluster that already has Tekton installed
#   USE_EXISTING_CLUSTER=true SKIP_TEKTON_INSTALL=true ./hack/setup-poc.sh
#
# Environment variables:
#   USE_EXISTING_CLUSTER  - Skip kind creation, use current context (default: false)
#   SKIP_TEKTON_INSTALL   - Don't install Tekton controller (default: false)
#   KIND_CLUSTER_NAME     - Kind cluster name (default: kube-shard-poc)
#   TEKTON_VERSION        - Tekton Pipeline version (default: v0.65.2)
#   FRONT_PROXY_CA        - Path to front-proxy CA cert. Auto-detected for kind.
#                           Required for non-kind clusters if auto-detection fails.
#   MIRROR_NAMESPACES     - Space-separated list of namespaces to mirror to
#                           the secondary (default: "default")
#   SECONDARY_PORT        - Local port for port-forward to secondary (default: 6444)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kube-shard-poc}"
NAMESPACE="tekton-apiserver"
TEKTON_VERSION="${TEKTON_VERSION:-v0.65.2}"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
USE_EXISTING_CLUSTER="${USE_EXISTING_CLUSTER:-false}"
SKIP_TEKTON_INSTALL="${SKIP_TEKTON_INSTALL:-false}"
MIRROR_NAMESPACES="${MIRROR_NAMESPACES:-default}"

echo "============================================"
echo "  kube-shard Phase 1 PoC Setup"
echo "  Kine backend: SQLite"
echo "  Tekton version: ${TEKTON_VERSION}"
if [[ "${USE_EXISTING_CLUSTER}" == "true" ]]; then
  echo "  Mode: existing cluster"
  echo "  Context: $(kubectl config current-context)"
else
  echo "  Mode: fresh kind cluster"
fi
echo "============================================"
echo ""

# ---------- Step 1: Cluster ----------
if [[ "${USE_EXISTING_CLUSTER}" == "true" ]]; then
  echo "=== Step 1/7: Using existing cluster ==="
  echo "    Context: $(kubectl config current-context)"
  kubectl cluster-info
else
  echo "=== Step 1/7: Create kind cluster ==="
  "${REPO_ROOT}/hack/setup-kind.sh"
  kubectl config use-context "kind-${KIND_CLUSTER_NAME}"
fi
echo ""

# ---------- Step 2: Certificates ----------
echo "=== Step 2/7: Generate certificates ==="
"${REPO_ROOT}/hack/generate-certs.sh"
echo ""

# ---------- Step 3: Deploy kube-shard stack (kustomize) ----------
echo "=== Step 3/7: Deploy kube-shard stack ==="
kubectl apply -k "${REPO_ROOT}/deploy/poc"

kubectl -n "${NAMESPACE}" create secret generic secondary-apiserver-certs \
  --from-file=serving.crt="${CERT_DIR}/serving.crt" \
  --from-file=serving.key="${CERT_DIR}/serving.key" \
  --from-file=front-proxy-ca.crt="${CERT_DIR}/front-proxy-ca.crt" \
  --from-file=sa-signing.key="${CERT_DIR}/sa-signing.key" \
  --from-file=sa-signing.pub="${CERT_DIR}/sa-signing.pub" \
  --from-file=token-auth.csv="${CERT_DIR}/token-auth.csv" \
  --dry-run=client -o yaml | kubectl apply -f -
echo ""

# ---------- Step 4: Wait for Kine ----------
echo "=== Step 4/7: Wait for Kine ==="
echo "    Waiting for Kine to be ready..."
kubectl -n "${NAMESPACE}" rollout status deployment/kine --timeout=120s
echo ""

# ---------- Step 5: Wait for Secondary API Server ----------
echo "=== Step 5/7: Wait for secondary API server ==="
echo "    Waiting for secondary API server to be ready..."
kubectl -n "${NAMESPACE}" rollout status deployment/secondary-apiserver --timeout=180s
echo ""

# ---------- Step 6: Tekton CRDs on Secondary ----------
echo "=== Step 6/7: Install Tekton CRDs on secondary ==="
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")

# Port-forward to the secondary API server (kill any stale forward first)
echo "    Starting port-forward to secondary API server..."
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true" EXIT

# Wait for port-forward to be ready
sleep 3
for i in $(seq 1 10); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    echo "    Port-forward is ready."
    break
  fi
  if [[ $i -eq 10 ]]; then
    echo "    [FAIL] Port-forward not ready after 10 attempts"
    exit 1
  fi
  sleep 2
done

# Install CRDs
SECONDARY_PORT="${SECONDARY_PORT}" "${REPO_ROOT}/hack/install-tekton-crds.sh"

# Mirror namespaces to the secondary
echo "    Mirroring namespaces to secondary: ${MIRROR_NAMESPACES}"
for ns in ${MIRROR_NAMESPACES}; do
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    create namespace "${ns}" --dry-run=client -o yaml | \
    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    apply -f - 2>/dev/null || true
done

# Kill port-forward
kill ${PF_PID} 2>/dev/null || true
trap - EXIT
echo ""

# ---------- Step 7: APIService + Tekton Controller ----------
echo "=== Step 7/7: Register APIService and deploy Tekton controller ==="

# Check for existing Tekton CRDs on the primary and remove them
# (CRDs and APIService cannot coexist for the same group/version)
EXISTING_CRDS=$(kubectl get crds -o name 2>/dev/null | grep -E "tekton\.dev|resolution\.tekton\.dev" || true)
if [[ -n "${EXISTING_CRDS}" ]]; then
  echo "    Removing existing Tekton CRDs from primary (required for APIService)..."
  echo "    CRDs to remove:"
  echo "${EXISTING_CRDS}" | sed 's/^/      /'
  echo "${EXISTING_CRDS}" | xargs kubectl delete --wait=false
  # Wait for CRDs to be gone
  sleep 5
  for i in $(seq 1 20); do
    REMAINING=$(kubectl get crds -o name 2>/dev/null | grep -E "tekton\.dev|resolution\.tekton\.dev" || true)
    if [[ -z "${REMAINING}" ]]; then
      echo "    CRDs removed."
      break
    fi
    if [[ $i -eq 20 ]]; then
      echo "    [WARN] Some CRDs still exist (may have finalizers):"
      echo "${REMAINING}" | sed 's/^/      /'
    fi
    sleep 2
  done
fi

# Register APIService objects
CA_BUNDLE=$(base64 -w0 < "${CERT_DIR}/serving-ca.crt")
sed "s|caBundle: PLACEHOLDER|caBundle: ${CA_BUNDLE}|g" \
  "${REPO_ROOT}/deploy/poc/apiservice.yaml" | kubectl apply -f -

echo "    Waiting for APIService to be available..."
for i in $(seq 1 30); do
  AVAILABLE=$(kubectl get apiservice v1.tekton.dev -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || echo "")
  if [[ "${AVAILABLE}" == "True" ]]; then
    echo "    APIService v1.tekton.dev is Available."
    break
  fi
  if [[ $i -eq 30 ]]; then
    echo "    [WARN] APIService not yet Available after 60s. Checking status..."
    kubectl get apiservice v1.tekton.dev -o yaml
  fi
  sleep 2
done

# Install Tekton Pipeline controller (unless skipped)
if [[ "${SKIP_TEKTON_INSTALL}" == "true" ]]; then
  echo "    Skipping Tekton controller install (SKIP_TEKTON_INSTALL=true)."
  echo "    Restarting existing Tekton controller to pick up APIService..."
  kubectl -n tekton-pipelines rollout restart deployment/tekton-pipelines-controller 2>/dev/null || true
  kubectl -n tekton-pipelines rollout status deployment/tekton-pipelines-controller --timeout=180s 2>/dev/null || true
else
  echo "    Installing Tekton Pipeline controller..."
  TEKTON_RELEASE_DIR="${REPO_ROOT}/_output/tekton"
  RELEASE_FILE="${TEKTON_RELEASE_DIR}/release-${TEKTON_VERSION}.yaml"

  if [[ ! -f "${RELEASE_FILE}" ]]; then
    mkdir -p "${TEKTON_RELEASE_DIR}"
    curl -sSL \
      "https://storage.googleapis.com/tekton-releases/pipeline/previous/${TEKTON_VERSION}/release.yaml" \
      -o "${RELEASE_FILE}"
  fi

  # Apply everything except CRDs (CRDs are on secondary via aggregation)
  awk '
    BEGIN { doc=""; is_crd=0; first=1 }
    /^---$/ {
      if (!is_crd && doc != "") {
        if (!first) print "---"
        printf "%s", doc
        first=0
      }
      doc=""
      is_crd=0
      next
    }
    /kind: CustomResourceDefinition/ { is_crd=1 }
    { doc = doc $0 "\n" }
    END {
      if (!is_crd && doc != "") {
        if (!first) print "---"
        printf "%s", doc
      }
    }
  ' "${RELEASE_FILE}" | kubectl apply --server-side -f - 2>&1 | grep -c "serverside-applied" | xargs -I{} echo "    Applied {} resources"

  echo "    Waiting for Tekton controller to be ready..."
  kubectl -n tekton-pipelines rollout status deployment/tekton-pipelines-controller --timeout=180s
  kubectl -n tekton-pipelines rollout status deployment/tekton-pipelines-webhook --timeout=180s || true
fi

echo ""
echo "============================================"
echo "  Phase 1 PoC setup complete!"
echo ""
echo "  Verify with: make test"
echo "  View logs:   make logs-apiserver"
echo "               make logs-kine"
echo "  Direct access: ./hack/kubectl-secondary.sh get crds"
echo "============================================"
