#!/usr/bin/env bash
set -euo pipefail

# kubectl-secondary: a shim that wraps kubectl for direct interaction with the
# secondary API server deployed by the kube-shard operator.
#
# Usage:
#   ./hack/kubectl-secondary.sh <shard-name> get crds
#   ./hack/kubectl-secondary.sh <shard-name> get pipelineruns -A
#   ./hack/kubectl-secondary.sh <shard-name> create namespace my-namespace
#   ./hack/kubectl-secondary.sh <shard-name> api-resources
#
# If only one APIShard exists, the shard name can be omitted:
#   ./hack/kubectl-secondary.sh get crds
#
# This is useful for:
#   - Managing CRDs on the secondary (install, update, delete)
#   - Inspecting raw state on the secondary
#   - Debugging aggregation issues by comparing direct vs proxied responses
#
# Environment variables:
#   SECONDARY_PORT  - Local port for port-forward (default: auto-detect free port)
#   KEEP_PORT_FWD   - If "true", don't kill port-forward on exit (default: false)
#   SHARD_NAMESPACE - Override the shard namespace (default: <shard-name>-ns)
#   REFRESH_CREDS   - If "true", force credential refresh (default: false)

KEEP_PORT_FWD="${KEEP_PORT_FWD:-false}"
REFRESH_CREDS="${REFRESH_CREDS:-false}"

# --- Determine the shard name ---
KUBECTL_VERBS="get|describe|create|apply|delete|patch|edit|label|annotate|explain|logs|exec|port-forward|top|api-resources|api-versions|config|version|wait|auth|scale|rollout|set|expose|run|cp|attach|cordon|uncordon|drain|taint|debug"

if [[ $# -gt 0 ]] && ! echo "$1" | grep -qE "^(${KUBECTL_VERBS})$" && ! [[ "$1" == -* ]]; then
  SHARD_NAME="$1"
  shift
else
  SHARDS=$(kubectl get apishards -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
  SHARD_COUNT=$(echo "${SHARDS}" | wc -w)
  if [[ ${SHARD_COUNT} -eq 0 ]]; then
    echo "ERROR: No APIShard resources found in the cluster." >&2
    echo "       Create an APIShard first, or specify the shard name explicitly." >&2
    exit 1
  elif [[ ${SHARD_COUNT} -gt 1 ]]; then
    echo "ERROR: Multiple APIShards found: ${SHARDS}" >&2
    echo "       Specify which shard to connect to:" >&2
    echo "         ./hack/kubectl-secondary.sh <shard-name> <kubectl-args...>" >&2
    exit 1
  fi
  SHARD_NAME="${SHARDS}"
fi

SHARD_NAMESPACE="${SHARD_NAMESPACE:-${SHARD_NAME}-ns}"
SVC_NAME="${SHARD_NAME}-apiserver"

# --- Credential cache ---
CACHE_DIR="${XDG_CACHE_HOME:-${HOME}/.cache}/kubectl-secondary/${SHARD_NAME}"

has_cached_creds() {
  [[ -f "${CACHE_DIR}/tls.crt" ]] && \
  [[ -f "${CACHE_DIR}/tls.key" ]] && \
  [[ -f "${CACHE_DIR}/ca.crt" ]]
}

refresh_creds() {
  mkdir -p "${CACHE_DIR}"
  chmod 700 "${CACHE_DIR}"

  local secret key escaped_key outfile
  for pair in "tls.crt:${SHARD_NAME}-admin-client-cert" "tls.key:${SHARD_NAME}-admin-client-cert" "ca.crt:${SHARD_NAME}-pki"; do
    key="${pair%%:*}"
    secret="${pair#*:}"
    escaped_key="${key//./\\.}"
    outfile="${CACHE_DIR}/${key}"

    kubectl get secret "${secret}" -n "${SHARD_NAMESPACE}" \
      -o "jsonpath={.data.${escaped_key}}" 2>/dev/null | base64 -d > "${outfile}"
    if [[ ! -s "${outfile}" ]]; then
      echo "ERROR: Failed to extract ${key} from secret ${secret} in namespace ${SHARD_NAMESPACE}" >&2
      rm -f "${outfile}"
      exit 1
    fi
    chmod 600 "${outfile}"
  done
}

if [[ "${REFRESH_CREDS}" == "true" ]] || ! has_cached_creds; then
  refresh_creds
fi

# --- Port-forward ---
TMPDIR=$(mktemp -d)
PF_PID=""
cleanup() {
  rm -rf "${TMPDIR}"
  if [[ -n "${PF_PID}" ]] && [[ "${KEEP_PORT_FWD}" != "true" ]]; then
    kill "${PF_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

if [[ -n "${SECONDARY_PORT:-}" ]]; then
  LOCAL_PORT="${SECONDARY_PORT}"
else
  LOCAL_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null \
    || echo "6444")
fi

if ! timeout 1 bash -c "echo >/dev/tcp/localhost/${LOCAL_PORT}" 2>/dev/null; then
  kubectl -n "${SHARD_NAMESPACE}" port-forward "svc/${SVC_NAME}" "${LOCAL_PORT}:443" >/dev/null 2>&1 &
  PF_PID=$!

  for i in $(seq 1 15); do
    if timeout 1 bash -c "echo >/dev/tcp/localhost/${LOCAL_PORT}" 2>/dev/null; then
      break
    fi
    if [[ $i -eq 15 ]]; then
      echo "ERROR: Could not connect to secondary API server (svc/${SVC_NAME}) on port ${LOCAL_PORT}" >&2
      exit 1
    fi
    sleep 1
  done
fi

# --- Write kubeconfig ---
write_kubeconfig() {
  cat > "${TMPDIR}/kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: secondary
  cluster:
    server: https://127.0.0.1:${LOCAL_PORT}
    insecure-skip-tls-verify: true
users:
- name: admin
  user:
    client-certificate: ${CACHE_DIR}/tls.crt
    client-key: ${CACHE_DIR}/tls.key
contexts:
- name: default
  context:
    cluster: secondary
    user: admin
current-context: default
EOF
}

write_kubeconfig

# --- Execute kubectl, retry on auth failure ---
output=$(kubectl --kubeconfig="${TMPDIR}/kubeconfig" "$@" 2>&1) && {
  echo "${output}"
  exit 0
}
rc=$?

if echo "${output}" | grep -qiE "provide credentials|Unauthorized|certificate.*(expired|invalid)|PEM data|tls:"; then
  echo "INFO: Credentials rejected, refreshing cache..." >&2
  refresh_creds
  write_kubeconfig
  exec kubectl --kubeconfig="${TMPDIR}/kubeconfig" "$@"
fi

echo "${output}" >&2
exit ${rc}
