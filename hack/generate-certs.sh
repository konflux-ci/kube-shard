#!/usr/bin/env bash
set -euo pipefail

# generate-certs.sh - Generates TLS certificates for the secondary API server.
#
# Purpose:
#   Creates all PKI material needed for the secondary API server to serve HTTPS
#   and to trust proxied identity headers from the primary's front-proxy.
#
# What it generates (in _output/certs/):
#   serving-ca.{crt,key}    - Self-signed CA for the secondary's serving cert
#   serving.{crt,key}       - Serving cert (SAN: tekton-apiserver.tekton-apiserver.svc)
#   sa-signing.{key,pub}    - Dummy SA key (required by kube-apiserver, unused)
#   token-auth.csv          - Static token file for direct API access (PoC only)
#   admin-token             - The token value for use with kubectl --token
#   front-proxy-ca.crt      - Extracted from kind or provided via FRONT_PROXY_CA
#
# The serving-ca.crt is what goes into APIService.spec.caBundle so the primary
# can verify TLS when proxying to the secondary.
#
# Environment variables:
#   KIND_CLUSTER_NAME    - Used to find the control-plane container (default: kube-kine-poc)
#   USE_EXISTING_CLUSTER - If "true", won't attempt kind container extraction
#   FRONT_PROXY_CA       - Path to an existing front-proxy CA cert file.
#                          If set, skips extraction from kind. Required for
#                          non-kind clusters unless auto-detection works.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="${REPO_ROOT}/_output/certs"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kube-kine-poc}"
USE_EXISTING_CLUSTER="${USE_EXISTING_CLUSTER:-false}"
FRONT_PROXY_CA="${FRONT_PROXY_CA:-}"

mkdir -p "${CERT_DIR}"

echo "==> Generating secondary API server certificates..."

# Generate a CA for the secondary API server's serving certificate
openssl genrsa -out "${CERT_DIR}/serving-ca.key" 2048
openssl req -x509 -new -nodes \
  -key "${CERT_DIR}/serving-ca.key" \
  -sha256 -days 365 \
  -out "${CERT_DIR}/serving-ca.crt" \
  -subj "/CN=secondary-apiserver-ca"

# Generate the serving certificate for the secondary API server
# SAN must include the Service DNS names
openssl genrsa -out "${CERT_DIR}/serving.key" 2048

cat > "${CERT_DIR}/serving-csr.conf" <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_dn
prompt = no

[req_dn]
CN = tekton-apiserver.tekton-apiserver.svc

[v3_req]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = tekton-apiserver
DNS.2 = tekton-apiserver.tekton-apiserver
DNS.3 = tekton-apiserver.tekton-apiserver.svc
DNS.4 = tekton-apiserver.tekton-apiserver.svc.cluster.local
EOF

openssl req -new \
  -key "${CERT_DIR}/serving.key" \
  -out "${CERT_DIR}/serving.csr" \
  -config "${CERT_DIR}/serving-csr.conf"

openssl x509 -req \
  -in "${CERT_DIR}/serving.csr" \
  -CA "${CERT_DIR}/serving-ca.crt" \
  -CAkey "${CERT_DIR}/serving-ca.key" \
  -CAcreateserial \
  -out "${CERT_DIR}/serving.crt" \
  -days 365 \
  -sha256 \
  -extensions v3_req \
  -extfile "${CERT_DIR}/serving-csr.conf"

# Generate a dummy SA signing key (required by kube-apiserver but unused in aggregation mode)
openssl genrsa -out "${CERT_DIR}/sa-signing.key" 2048
openssl rsa -in "${CERT_DIR}/sa-signing.key" -pubout -out "${CERT_DIR}/sa-signing.pub"

# Generate static token auth file for direct access (PoC only)
echo "==> Generating static token auth file..."
POC_TOKEN=$(openssl rand -hex 16)
echo "${POC_TOKEN},admin,admin,\"system:masters\"" > "${CERT_DIR}/token-auth.csv"
echo "${POC_TOKEN}" > "${CERT_DIR}/admin-token"

# Obtain front-proxy CA
echo "==> Obtaining front-proxy CA..."
if [[ -n "${FRONT_PROXY_CA}" ]]; then
  # User provided it explicitly
  echo "    Using provided FRONT_PROXY_CA: ${FRONT_PROXY_CA}"
  cp "${FRONT_PROXY_CA}" "${CERT_DIR}/front-proxy-ca.crt"

elif [[ "${USE_EXISTING_CLUSTER}" == "true" ]]; then
  # Try to extract from the cluster's configmap (works for kubeadm-based clusters including kind)
  echo "    Attempting to extract front-proxy CA from cluster..."
  if kubectl -n kube-system get configmap extension-apiserver-authentication -o jsonpath='{.data.requestheader-client-ca-file}' > "${CERT_DIR}/front-proxy-ca.crt" 2>/dev/null && \
     [[ -s "${CERT_DIR}/front-proxy-ca.crt" ]]; then
    echo "    Extracted from extension-apiserver-authentication configmap."
  else
    echo "    [ERROR] Could not auto-detect front-proxy CA."
    echo "    Set FRONT_PROXY_CA=/path/to/front-proxy-ca.crt and re-run."
    exit 1
  fi

else
  # Kind cluster: extract directly from the container filesystem
  CONTROL_PLANE="${KIND_CLUSTER_NAME}-control-plane"
  echo "    Extracting from kind control-plane container: ${CONTROL_PLANE}"
  if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
    CONTAINER_RUNTIME="docker"
  else
    CONTAINER_RUNTIME="podman"
  fi
  ${CONTAINER_RUNTIME} cp "${CONTROL_PLANE}:/etc/kubernetes/pki/front-proxy-ca.crt" "${CERT_DIR}/front-proxy-ca.crt"
fi

echo "==> Certificates generated in ${CERT_DIR}"
ls -la "${CERT_DIR}"
