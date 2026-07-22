#!/usr/bin/env bash
set -euo pipefail

# install-tekton-crds.sh - Installs Tekton Pipeline CRDs on the secondary API server.
#
# Purpose:
#   Downloads the upstream Tekton Pipeline release manifest, extracts ONLY the
#   CRD resources, and applies them directly to the secondary API server.
#
#   CRDs are installed ONLY on the secondary, never on the primary. The primary
#   routes tekton.dev requests via APIService objects (API aggregation).
#
# What it does:
#   1. Downloads the Tekton Pipeline release YAML (cached in _output/tekton/)
#   2. Extracts CustomResourceDefinition documents from the release
#   3. Applies the CRDs to the secondary via port-forward using the admin token
#
# Prerequisites:
#   - Secondary API server must be running
#   - Port-forward to the secondary must be active on SECONDARY_PORT
#
# Environment variables:
#   TEKTON_VERSION   - Tekton Pipeline version (default: v0.65.2)
#   SECONDARY_PORT   - Port-forward local port (default: 6444)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEKTON_VERSION="${TEKTON_VERSION:-v0.65.2}"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
SECONDARY_URL="https://localhost:${SECONDARY_PORT}"
CERT_DIR="${REPO_ROOT}/_output/certs"
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token" 2>/dev/null || echo "")

echo "==> Installing Tekton Pipeline ${TEKTON_VERSION} CRDs on secondary API server..."

# Download Tekton Pipeline release if not cached
TEKTON_RELEASE_DIR="${REPO_ROOT}/_output/tekton"
mkdir -p "${TEKTON_RELEASE_DIR}"

RELEASE_FILE="${TEKTON_RELEASE_DIR}/release-${TEKTON_VERSION}.yaml"
if [[ ! -f "${RELEASE_FILE}" ]]; then
  echo "    Downloading Tekton Pipeline release ${TEKTON_VERSION}..."
  curl -sSL \
    "https://storage.googleapis.com/tekton-releases/pipeline/previous/${TEKTON_VERSION}/release.yaml" \
    -o "${RELEASE_FILE}"
fi

# Extract only CRD resources from the release
echo "    Extracting CRDs..."
CRD_FILE="${TEKTON_RELEASE_DIR}/crds-${TEKTON_VERSION}.yaml"

# Use yq or python to extract CRDs; fall back to awk-based approach
if command -v yq &>/dev/null; then
  yq 'select(.kind == "CustomResourceDefinition")' "${RELEASE_FILE}" > "${CRD_FILE}"
else
  # awk-based extraction: split on --- and keep CRD documents
  awk '
    BEGIN { doc="" ; is_crd=0 }
    /^---$/ {
      if (is_crd && doc != "") print doc "---"
      doc=""
      is_crd=0
      next
    }
    /kind: CustomResourceDefinition/ { is_crd=1 }
    { doc = doc $0 "\n" }
    END { if (is_crd && doc != "") print doc }
  ' "${RELEASE_FILE}" > "${CRD_FILE}"
fi

echo "    Applying CRDs to secondary API server at ${SECONDARY_URL}..."
kubectl --server="${SECONDARY_URL}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  apply -f "${CRD_FILE}"

echo "==> Tekton CRDs installed on secondary API server."
echo "    Verifying..."
kubectl --server="${SECONDARY_URL}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  api-resources --api-group=tekton.dev
