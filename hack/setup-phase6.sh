#!/usr/bin/env bash
set -euo pipefail

# setup-phase6.sh - Phase 6: Konflux integration
#
# Purpose:
#   Deploys the kube-shard secondary API server on an existing Konflux cluster,
#   migrates Tekton pipeline CRDs to the secondary via API aggregation, and
#   validates that the full Konflux workflow (Chains, build-service, etc.) works.
#
# Prerequisites:
#   - A running Konflux cluster (kind-konflux context)
#   - Tekton installed via Tekton Operator
#   - cert-manager installed
#   - Konflux services deployed (build-service, integration-service, etc.)
#
# What it does:
#   1. Generates TLS certs for the secondary API server
#   2. Deploys PostgreSQL + Kine + secondary API server
#   3. Scales down the Tekton Operator (prevents CRD reconciliation conflicts)
#   4. Extracts and installs Tekton CRDs on the secondary
#   5. Configures authorization webhook on the secondary
#   6. Removes Tekton pipeline CRDs from primary, registers APIService objects
#   7. Copies webhook configs to secondary (with service→url transform)
#   8. Mirrors required namespaces to secondary
#   9. Restarts Tekton controllers to pick up the new APIService routing
#
# Environment variables:
#   KUBE_CONTEXT      - kubectl context to use (default: kind-konflux)
#   SECONDARY_PORT    - Local port for port-forward (default: 6444)
#   TENANT_NAMESPACE  - Namespace for running PipelineRuns (default: default-tenant)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KUBE_CONTEXT="${KUBE_CONTEXT:-kind-konflux}"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
TENANT_NAMESPACE="${TENANT_NAMESPACE:-default-tenant}"
WORK_DIR="${REPO_ROOT}/_output/phase6"

mkdir -p "${WORK_DIR}"

echo "============================================"
echo "  kube-shard Phase 6: Konflux Integration"
echo "============================================"
echo ""
echo "  Context:          ${KUBE_CONTEXT}"
echo "  Tenant namespace: ${TENANT_NAMESPACE}"
echo ""

# Ensure we use the correct context
kubectl config use-context "${KUBE_CONTEXT}" >/dev/null 2>&1

# ---------- Step 1: Generate certificates ----------
echo "=== Step 1/9: Generate certificates ==="
USE_EXISTING_CLUSTER=true "${REPO_ROOT}/hack/generate-certs.sh"
echo ""

# ---------- Step 2: Deploy kube-shard stack ----------
echo "=== Step 2/9: Deploy PostgreSQL + Kine + secondary API server ==="

# Apply Phase 5 overlay (includes PostgreSQL + Kine with postgres backend)
kubectl apply -k "${REPO_ROOT}/deploy/phase5"

# Create certs secret
kubectl -n "${NAMESPACE}" create secret generic secondary-apiserver-certs \
  --from-file=serving.crt="${CERT_DIR}/serving.crt" \
  --from-file=serving.key="${CERT_DIR}/serving.key" \
  --from-file=front-proxy-ca.crt="${CERT_DIR}/front-proxy-ca.crt" \
  --from-file=sa-signing.key="${CERT_DIR}/sa-signing.key" \
  --from-file=sa-signing.pub="${CERT_DIR}/sa-signing.pub" \
  --from-file=token-auth.csv="${CERT_DIR}/token-auth.csv" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "    Waiting for PostgreSQL..."
kubectl -n "${NAMESPACE}" rollout status deployment/postgresql --timeout=180s

echo "    Waiting for Kine..."
kubectl -n "${NAMESPACE}" rollout status deployment/kine --timeout=180s

echo "    Waiting for secondary API server..."
kubectl -n "${NAMESPACE}" rollout status deployment/secondary-apiserver --timeout=180s
echo ""

# ---------- Step 3: Scale down Tekton Operator ----------
echo "=== Step 3/9: Scale down Tekton Operator (prevent CRD reconciliation) ==="
echo "    The operator would re-create CRDs we delete, conflicting with APIService."
kubectl -n tekton-operator scale deployment tekton-operator --replicas=0
kubectl -n tekton-operator scale deployment tekton-operator-webhook --replicas=0
echo "    Tekton Operator scaled to 0."
echo ""

# ---------- Step 4: Install Tekton CRDs on secondary ----------
echo "=== Step 4/9: Install Tekton CRDs on secondary ==="

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
    echo "    Secondary API server is healthy."
    break
  fi
  if [[ $i -eq 15 ]]; then
    echo "    [ERROR] Secondary API server not reachable."
    exit 1
  fi
  sleep 2
done

# Extract Tekton pipeline CRDs from primary (tekton.dev + resolution.tekton.dev only)
echo "    Extracting Tekton pipeline CRDs from primary..."
PIPELINE_CRDS=$(kubectl get crds -o name | grep -E "^customresourcedefinition\.apiextensions\.k8s\.io/(pipelineruns|taskruns|pipelines|tasks|customruns|stepactions|verificationpolicies)\.tekton\.dev$|^customresourcedefinition\.apiextensions\.k8s\.io/resolutionrequests\.resolution\.tekton\.dev$" || true)

for crd_name in ${PIPELINE_CRDS}; do
  short_name=$(echo "${crd_name}" | sed 's|customresourcedefinition.apiextensions.k8s.io/||')
  echo "      Extracting: ${short_name}"
  kubectl get crd "${short_name}" -o yaml > "${WORK_DIR}/${short_name}.yaml"
done

# Apply CRDs to secondary (strip metadata cruft)
echo "    Applying CRDs to secondary..."
for f in "${WORK_DIR}"/*.tekton.dev.yaml; do
  if [[ -f "$f" ]]; then
    # Strip operator annotations, resourceVersion, uid, etc.
    if command -v yq &>/dev/null; then
      yq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp,
              .metadata.generation, .metadata.managedFields,
              .metadata.annotations["operator.tekton.dev/last-applied-hash"],
              .status)' "$f" > "${f}.clean"
    else
      cp "$f" "${f}.clean"
    fi

    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
      --token="${ADMIN_TOKEN}" \
      apply -f "${f}.clean" 2>&1 | sed 's/^/      /'
  fi
done

# Disable non-functional CRD conversion webhooks
echo "    Disabling CRD conversion webhooks on secondary..."
SECONDARY_CRDS=$(kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get crd -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep "tekton.dev" || true)

for crd in ${SECONDARY_CRDS}; do
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
echo ""

# ---------- Step 5: Verify authorization webhook is configured ----------
echo "=== Step 5/9: Verify authorization webhook on secondary ==="
# The Phase 5 kustomize overlay (applied in Step 2) already includes the Phase 2
# authorization webhook configuration (phase5 → phase3 → phase2 → poc chain).
# No additional apply is needed; just verify the secondary has the authz config.
AUTHZ_MODE=$(kubectl -n "${NAMESPACE}" get deployment secondary-apiserver \
  -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null | grep -o "authorization-mode=[^ ]*" || echo "")
if echo "${AUTHZ_MODE}" | grep -q "Webhook"; then
  echo "    Authorization webhook is configured: ${AUTHZ_MODE}"
else
  echo "    [WARN] Authorization webhook not detected. Args: ${AUTHZ_MODE}"
fi
echo ""

# ---------- Step 6: Remove CRDs from primary + register APIService ----------
echo "=== Step 6/9: Remove Tekton pipeline CRDs from primary, register APIService ==="

# Remove only the pipeline CRDs we're aggregating (not operator/triggers/pac)
echo "    Removing tekton.dev and resolution.tekton.dev pipeline CRDs from primary..."
CRDS_TO_REMOVE=$(kubectl get crds -o name | grep -E "(pipelineruns|taskruns|pipelines|tasks|customruns|stepactions|verificationpolicies)\.tekton\.dev|resolutionrequests\.resolution\.tekton\.dev" || true)
if [[ -n "${CRDS_TO_REMOVE}" ]]; then
  echo "${CRDS_TO_REMOVE}" | sed 's/^/      /'
  echo "${CRDS_TO_REMOVE}" | xargs kubectl delete --wait=false 2>&1 | sed 's/^/      /'
  # Wait for removal
  for i in $(seq 1 30); do
    REMAINING=$(kubectl get crds -o name 2>/dev/null | grep -E "(pipelineruns|taskruns|pipelines|tasks|customruns|stepactions|verificationpolicies)\.tekton\.dev|resolutionrequests\.resolution\.tekton\.dev" || true)
    if [[ -z "${REMAINING}" ]]; then
      echo "    Pipeline CRDs removed from primary."
      break
    fi
    if [[ $i -eq 30 ]]; then
      echo "    [WARN] Some CRDs still present:"
      echo "${REMAINING}" | sed 's/^/      /'
    fi
    sleep 2
  done
fi

# Register APIService objects
echo "    Registering APIService objects..."
CA_BUNDLE=$(base64 -w0 < "${CERT_DIR}/serving-ca.crt")
sed "s|caBundle: PLACEHOLDER|caBundle: ${CA_BUNDLE}|g" \
  "${REPO_ROOT}/deploy/poc/apiservice.yaml" | kubectl apply -f - 2>&1 | sed 's/^/      /'

echo "    Waiting for APIServices to be available..."
for svc in v1.tekton.dev v1beta1.tekton.dev v1alpha1.tekton.dev v1beta1.resolution.tekton.dev v1alpha1.resolution.tekton.dev; do
  for i in $(seq 1 30); do
    AVAILABLE=$(kubectl get apiservice "${svc}" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || echo "")
    if [[ "${AVAILABLE}" == "True" ]]; then
      echo "      ${svc}: Available"
      break
    fi
    if [[ $i -eq 30 ]]; then
      echo "      ${svc}: NOT available (timeout)"
    fi
    sleep 2
  done
done
echo ""

# ---------- Step 7: Copy webhook configs to secondary ----------
echo "=== Step 7/9: Copy Tekton webhook configs to secondary ==="

# Extract and transform webhooks (service → url)
for wh_type in mutatingwebhookconfiguration validatingwebhookconfiguration; do
  WEBHOOKS=$(kubectl get "${wh_type}" -l app.kubernetes.io/part-of=tekton-pipelines -o name 2>/dev/null || true)
  for wh in ${WEBHOOKS}; do
    wh_name=$(echo "${wh}" | sed "s|${wh_type}.admissionregistration.k8s.io/||")
    echo "    Processing: ${wh_name} (${wh_type})"

    kubectl get "${wh_type}" "${wh_name}" -o yaml > "${WORK_DIR}/${wh_name}.yaml"

    if command -v yq &>/dev/null; then
      yq '
        del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp,
            .metadata.generation, .metadata.managedFields) |
        (.webhooks[]?.clientConfig | select(.service != null)) |=
          (. + {"url": "https://" + .service.name + "." + .service.namespace + ".svc:" +
            ((.service.port // 443) | tostring) + (.service.path // "")} | del(.service))
      ' "${WORK_DIR}/${wh_name}.yaml" > "${WORK_DIR}/${wh_name}-clean.yaml"
    else
      cp "${WORK_DIR}/${wh_name}.yaml" "${WORK_DIR}/${wh_name}-clean.yaml"
    fi

    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
      --token="${ADMIN_TOKEN}" \
      apply -f "${WORK_DIR}/${wh_name}-clean.yaml" 2>&1 | sed 's/^/      /'
  done
done
echo ""

# ---------- Step 8: Mirror namespaces ----------
echo "=== Step 8/9: Mirror namespaces to secondary ==="

NAMESPACES_TO_MIRROR="${TENANT_NAMESPACE} default tekton-pipelines"
for ns in ${NAMESPACES_TO_MIRROR}; do
  kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    create namespace "${ns}" --dry-run=client -o yaml | \
    kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
    --token="${ADMIN_TOKEN}" \
    apply -f - 2>&1 | sed 's/^/    /'
done
echo ""

# ---------- Step 9: Restart Tekton controllers ----------
echo "=== Step 9/9: Restart Tekton controllers to pick up APIService routing ==="

kubectl -n tekton-pipelines rollout restart deployment/tekton-pipelines-controller
kubectl -n tekton-pipelines rollout restart deployment/tekton-pipelines-webhook
kubectl -n tekton-pipelines rollout restart deployment/tekton-chains-controller 2>/dev/null || true

echo "    Waiting for controllers to stabilize..."
kubectl -n tekton-pipelines rollout status deployment/tekton-pipelines-controller --timeout=120s
kubectl -n tekton-pipelines rollout status deployment/tekton-pipelines-webhook --timeout=120s
kubectl -n tekton-pipelines rollout status deployment/tekton-chains-controller --timeout=120s 2>/dev/null || true

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

echo ""
echo "============================================"
echo "  Phase 6 setup complete!"
echo ""
echo "  Tekton pipeline APIs now served from kube-shard secondary."
echo "  Tekton Operator is scaled down (manual management)."
echo ""
echo "  Verify with: make test-phase6"
echo "  Tenant namespace: ${TENANT_NAMESPACE}"
echo "============================================"
