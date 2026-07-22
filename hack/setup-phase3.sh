#!/usr/bin/env bash
set -euo pipefail

# setup-phase3.sh - Phase 3: Tekton admission webhooks
#
# Purpose:
#   Enables Tekton's ValidatingAdmissionWebhook and MutatingAdmissionWebhook on
#   the secondary API server. Copies webhook configurations (with injected
#   caBundle) from the primary cluster and patches CRDs for conversion webhooks.
#
# Prerequisites:
#   Phase 2 must be deployed (make phase2)
#
# What it does:
#   1. Applies the Phase 3 kustomize overlay which enables admission plugins
#   2. Waits for the secondary API server to roll out
#   3. Extracts Tekton webhook configs from the primary (caBundle pre-injected)
#   4. Applies them to the secondary via direct access
#   5. Patches CRDs on the secondary with the correct caBundle for conversion
#
# The Tekton webhook pod runs on the primary cluster and is reachable from
# the secondary via cluster DNS (tekton-pipelines-webhook.tekton-pipelines.svc).
#
# Environment variables:
#   SECONDARY_PORT  - Local port for port-forward to secondary (default: 6444)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"

echo "============================================"
echo "  kube-shard Phase 3: Tekton Admission Webhooks"
echo "============================================"
echo ""

# ---------- Step 1: Apply Phase 3 overlay ----------
echo "=== Step 1/5: Apply Phase 3 overlay ==="
echo "    (enables MutatingAdmissionWebhook + ValidatingAdmissionWebhook plugins)"
kubectl apply -k "${REPO_ROOT}/deploy/phase3"
echo ""

# ---------- Step 2: Wait for rollout ----------
echo "=== Step 2/5: Wait for secondary API server rollout ==="
kubectl -n "${NAMESPACE}" rollout status deployment/secondary-apiserver --timeout=180s
echo ""

# ---------- Step 3: Port-forward to secondary ----------
echo "=== Step 3/5: Start port-forward to secondary ==="
ADMIN_TOKEN=$(cat "${CERT_DIR}/admin-token")
pkill -f "port-forward svc/tekton-apiserver ${SECONDARY_PORT}" 2>/dev/null || true
sleep 1
kubectl -n "${NAMESPACE}" port-forward svc/tekton-apiserver ${SECONDARY_PORT}:443 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true" EXIT
sleep 3

for i in $(seq 1 15); do
  if curl -sk -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    "https://localhost:${SECONDARY_PORT}/healthz" 2>/dev/null | grep -q "ok"; then
    echo "    Secondary API server is ready."
    break
  fi
  if [[ $i -eq 15 ]]; then
    echo "    [ERROR] Secondary API server not reachable after 15 attempts."
    exit 1
  fi
  sleep 2
done
echo ""

# ---------- Step 4: Copy webhook configs from primary to secondary ----------
echo "=== Step 4/5: Copy Tekton webhook configs to secondary ==="

WORK_DIR="${REPO_ROOT}/_output/phase3"
mkdir -p "${WORK_DIR}"

echo "    Extracting ValidatingWebhookConfigurations from primary..."
kubectl get validatingwebhookconfiguration -l app.kubernetes.io/part-of=tekton-pipelines -o yaml > "${WORK_DIR}/validating-webhooks.yaml" 2>/dev/null || true

echo "    Extracting MutatingWebhookConfigurations from primary..."
kubectl get mutatingwebhookconfiguration -l app.kubernetes.io/part-of=tekton-pipelines -o yaml > "${WORK_DIR}/mutating-webhooks.yaml" 2>/dev/null || true

# If label-based extraction yielded no results, try by name
if ! grep -q "kind: ValidatingWebhookConfiguration" "${WORK_DIR}/validating-webhooks.yaml" 2>/dev/null; then
  echo "    Label selector empty, trying known names..."
  kubectl get validatingwebhookconfiguration \
    validation.webhook.pipeline.tekton.dev \
    config.webhook.pipeline.tekton.dev \
    -o yaml > "${WORK_DIR}/validating-webhooks.yaml" 2>/dev/null || true
fi

if ! grep -q "kind: MutatingWebhookConfiguration" "${WORK_DIR}/mutating-webhooks.yaml" 2>/dev/null; then
  kubectl get mutatingwebhookconfiguration \
    webhook.pipeline.tekton.dev \
    -o yaml > "${WORK_DIR}/mutating-webhooks.yaml" 2>/dev/null || true
fi

# Transform webhook configs for the secondary:
# 1. Remove metadata that would conflict (resourceVersion, uid, etc.)
# 2. Convert clientConfig.service to clientConfig.url (the secondary can't look up
#    Services in its store; URL-based refs use DNS which works in-cluster)
echo "    Transforming webhook configs (service -> url)..."
for f in "${WORK_DIR}/validating-webhooks.yaml" "${WORK_DIR}/mutating-webhooks.yaml"; do
  if [[ -f "$f" ]] && grep -q "kind:" "$f"; then
    if command -v yq &>/dev/null; then
      yq '
        del(.items[].metadata.resourceVersion, .items[].metadata.uid,
            .items[].metadata.creationTimestamp, .items[].metadata.generation,
            .items[].metadata.managedFields, .metadata.resourceVersion) |
        (.items[].webhooks[]?.clientConfig | select(.service != null)) |=
          (. + {"url": "https://" + .service.name + "." + .service.namespace + ".svc:" +
            ((.service.port // 443) | tostring) + (.service.path // "")} | del(.service))
      ' "$f" > "${f}.clean" && mv "${f}.clean" "$f"
    else
      echo "    [ERROR] yq is required to transform webhook configs"
      exit 1
    fi
  fi
done

echo "    Applying webhook configs to secondary..."
for f in "${WORK_DIR}/validating-webhooks.yaml" "${WORK_DIR}/mutating-webhooks.yaml"; do
  if [[ -f "$f" ]] && grep -q "kind:" "$f"; then
    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
      --token="${ADMIN_TOKEN}" \
      apply -f "$f" 2>&1 | sed 's/^/    /'
  fi
done
echo ""

# ---------- Step 5: Patch CRDs with caBundle for conversion webhooks ----------
echo "=== Step 5/5: Patch CRD conversion webhooks with caBundle ==="

# Get the caBundle from one of the webhook configs on the primary
CA_BUNDLE=""
CA_BUNDLE=$(kubectl get validatingwebhookconfiguration \
  validation.webhook.pipeline.tekton.dev \
  -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)

if [[ -z "${CA_BUNDLE}" ]]; then
  # Try from mutating webhook
  CA_BUNDLE=$(kubectl get mutatingwebhookconfiguration \
    webhook.pipeline.tekton.dev \
    -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
fi

# Tekton's webhook server does NOT implement CRD conversion (responds with
# "no controller registered for: /"). The CRDs declare v1beta1 as served with
# a Webhook conversion strategy, but it's non-functional. We disable it by
# setting strategy to None -- only v1 (stored version) will be served.
echo "    Disabling non-functional CRD conversion webhooks..."

TEKTON_CRDS=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get crd -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep "tekton.dev" || true)

for crd in ${TEKTON_CRDS}; do
  CONV_STRATEGY=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    get crd "${crd}" -o jsonpath='{.spec.conversion.strategy}' 2>/dev/null || echo "None")

  if [[ "${CONV_STRATEGY}" == "Webhook" ]]; then
    echo "    Patching ${crd}: conversion strategy -> None..."
    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
      --token="${ADMIN_TOKEN}" \
      patch crd "${crd}" --type=json \
      -p '[{"op": "replace", "path": "/spec/conversion", "value": {"strategy": "None"}}]' 2>&1 | sed 's/^/      /'
  fi
done

echo ""

# ---------- Verify ----------
echo "=== Verifying admission webhooks are registered on secondary ==="
echo "    ValidatingWebhookConfigurations:"
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get validatingwebhookconfiguration 2>&1 | sed 's/^/      /'

echo "    MutatingWebhookConfigurations:"
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get mutatingwebhookconfiguration 2>&1 | sed 's/^/      /'

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

echo ""
echo "============================================"
echo "  Phase 3 setup complete!"
echo ""
echo "  Verify with: make test-phase3"
echo "  Tekton admission webhooks are now active on the secondary."
echo "============================================"
