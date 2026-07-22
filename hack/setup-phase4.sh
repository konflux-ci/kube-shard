#!/usr/bin/env bash
set -euo pipefail

# setup-phase4.sh - Phase 4: Kueue + tekton-kueue integration
#
# Purpose:
#   Deploys cert-manager, Kueue, and tekton-kueue on the cluster. Copies the
#   tekton-kueue MutatingWebhookConfiguration to the secondary API server so
#   PipelineRun creation is intercepted for quota management.
#
# Prerequisites:
#   Phase 3 must be deployed (make phase3)
#
# What it does:
#   1. Installs cert-manager (required by Kueue and tekton-kueue TLS)
#   2. Installs Kueue with external framework config for PipelineRuns
#   3. Installs tekton-kueue controller + webhook
#   4. Copies tekton-kueue's MutatingWebhookConfiguration to secondary
#   5. Creates a ResourceFlavor, ClusterQueue, and LocalQueue for testing
#
# Environment variables:
#   SECONDARY_PORT       - Local port for port-forward to secondary (default: 6444)
#   CERT_MANAGER_VERSION - cert-manager version (default: v1.19.2)
#   KUEUE_VERSION        - Kueue version (default: v0.16.6)
#   TEKTON_KUEUE_VERSION - tekton-kueue version (default: v0.3.1)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="tekton-apiserver"
CERT_DIR="${REPO_ROOT}/_output/certs"
SECONDARY_PORT="${SECONDARY_PORT:-6444}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.19.2}"
KUEUE_VERSION="${KUEUE_VERSION:-v0.16.6}"
TEKTON_KUEUE_VERSION="${TEKTON_KUEUE_VERSION:-v0.3.1}"

echo "============================================"
echo "  kube-shard Phase 4: Kueue + tekton-kueue"
echo "============================================"
echo ""
echo "  cert-manager: ${CERT_MANAGER_VERSION}"
echo "  Kueue:        ${KUEUE_VERSION}"
echo "  tekton-kueue: ${TEKTON_KUEUE_VERSION}"
echo ""

# ---------- Step 1: Install cert-manager ----------
echo "=== Step 1/6: Install cert-manager ==="

if kubectl get namespace cert-manager &>/dev/null; then
  echo "    cert-manager namespace exists, checking readiness..."
  if kubectl get deployment cert-manager -n cert-manager &>/dev/null; then
    echo "    cert-manager already installed, skipping."
  else
    echo "    Namespace exists but deployment missing, installing..."
    kubectl apply --server-side -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
    kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=300s
  fi
else
  echo "    Installing cert-manager ${CERT_MANAGER_VERSION}..."
  kubectl apply --server-side -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
  kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=300s
fi
echo ""

# ---------- Step 2: Install Kueue ----------
echo "=== Step 2/6: Install Kueue ==="

WORK_DIR="${REPO_ROOT}/_output/phase4"
mkdir -p "${WORK_DIR}"

if kubectl get namespace kueue-system &>/dev/null && kubectl get deployment kueue-controller-manager -n kueue-system &>/dev/null; then
  echo "    Kueue already installed, updating config..."
else
  echo "    Installing Kueue ${KUEUE_VERSION}..."
  kubectl apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
fi

# Apply Kueue config with externalFrameworks for PipelineRuns
echo "    Applying Kueue configuration (externalFrameworks: pipelineruns.tekton.dev)..."
cat <<'EOF' | kubectl apply --server-side -f -
apiVersion: v1
kind: ConfigMap
metadata:
  labels:
    app.kubernetes.io/component: controller
    app.kubernetes.io/name: kueue
    control-plane: controller-manager
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: Configuration
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8080
    webhook:
      port: 9443
    leaderElection:
      leaderElect: true
      resourceName: c1f6bfd2.kueue.x-k8s.io
    controller:
      groupKindConcurrency:
        Job.batch: 5
        Pod: 5
        Workload.kueue.x-k8s.io: 5
        LocalQueue.kueue.x-k8s.io: 1
        ClusterQueue.kueue.x-k8s.io: 1
        ResourceFlavor.kueue.x-k8s.io: 1
    clientConnection:
      qps: 50
      burst: 100
    integrations:
      frameworks:
      - "batch/job"
      externalFrameworks:
      - "pipelineruns.tekton.dev"
EOF

echo "    Waiting for Kueue controller to be ready..."
kubectl rollout status deployment/kueue-controller-manager -n kueue-system --timeout=300s

# Restart Kueue to pick up config changes
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
kubectl rollout status deployment/kueue-controller-manager -n kueue-system --timeout=120s
echo ""

# ---------- Step 3: Install tekton-kueue ----------
echo "=== Step 3/6: Install tekton-kueue ==="

RELEASE_URL="https://github.com/konflux-ci/tekton-kueue/releases/download/${TEKTON_KUEUE_VERSION}/release-${TEKTON_KUEUE_VERSION}.yaml"

if kubectl get namespace tekton-kueue &>/dev/null && kubectl get deployment -n tekton-kueue -l app.kubernetes.io/name=tekton-kueue &>/dev/null 2>&1; then
  echo "    tekton-kueue already installed, ensuring latest..."
fi

echo "    Installing tekton-kueue ${TEKTON_KUEUE_VERSION}..."
kubectl apply --server-side -f "${RELEASE_URL}"
echo "    Waiting for tekton-kueue deployments..."
kubectl wait --for=condition=Available deployment --all -n tekton-kueue --timeout=300s

# Configure tekton-kueue to use 'tekton-queue' as the default queue name
echo "    Configuring tekton-kueue default queue name..."
cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: tekton-kueue-config
  namespace: tekton-kueue
  labels:
    app.kubernetes.io/part-of: tekton-kueue
data:
  config.yaml: |
    queueName: tekton-queue
    cel:
      expressions:
        - priority("tekton-kueue-default")
EOF
kubectl rollout restart deployment -n tekton-kueue
kubectl wait --for=condition=Available deployment --all -n tekton-kueue --timeout=120s
echo ""

# ---------- Step 4: Copy tekton-kueue webhook config to secondary ----------
echo "=== Step 4/6: Copy tekton-kueue webhook to secondary ==="

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

# Wait for cert-manager to inject caBundle into tekton-kueue's webhook config
echo "    Waiting for cert-manager to inject caBundle into tekton-kueue webhook..."
for i in $(seq 1 30); do
  CA_BUNDLE=$(kubectl get mutatingwebhookconfiguration tekton-kueue-mutating-webhook-configuration \
    -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || echo "")
  if [[ -n "${CA_BUNDLE}" ]]; then
    echo "    caBundle injected."
    break
  fi
  if [[ $i -eq 30 ]]; then
    echo "    [WARN] caBundle not yet injected after 60s. Proceeding anyway..."
  fi
  sleep 2
done

echo "    Extracting tekton-kueue MutatingWebhookConfiguration..."
kubectl get mutatingwebhookconfiguration tekton-kueue-mutating-webhook-configuration -o yaml \
  > "${WORK_DIR}/tekton-kueue-webhook.yaml" 2>/dev/null

# Transform: remove metadata cruft and convert service → url
echo "    Transforming webhook config (service -> url)..."
if command -v yq &>/dev/null; then
  yq '
    del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp,
        .metadata.generation, .metadata.managedFields, .metadata.annotations["cert-manager.io/inject-ca-from"]) |
    (.webhooks[]?.clientConfig | select(.service != null)) |=
      (. + {"url": "https://" + .service.name + "." + .service.namespace + ".svc:" +
        ((.service.port // 443) | tostring) + (.service.path // "")} | del(.service))
  ' "${WORK_DIR}/tekton-kueue-webhook.yaml" > "${WORK_DIR}/tekton-kueue-webhook-clean.yaml"
else
  echo "    [ERROR] yq is required to transform webhook configs"
  exit 1
fi

echo "    Applying tekton-kueue webhook to secondary..."
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  apply -f "${WORK_DIR}/tekton-kueue-webhook-clean.yaml" 2>&1 | sed 's/^/    /'
echo ""

# ---------- Step 5: Create Kueue resources ----------
echo "=== Step 5/6: Create Kueue resources (ResourceFlavor, ClusterQueue, LocalQueue) ==="

cat <<'EOF' | kubectl apply --server-side -f -
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: default-flavor
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: tekton-cluster-queue
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu", "memory", "tekton.dev/pipelineruns"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 10
      - name: "memory"
        nominalQuota: 10Gi
      - name: "tekton.dev/pipelineruns"
        nominalQuota: 100
EOF

# Create LocalQueue in the default namespace (for testing)
cat <<'EOF' | kubectl apply --server-side -f -
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: tekton-queue
  namespace: default
spec:
  clusterQueue: tekton-cluster-queue
EOF

echo "    Kueue resources created."
echo ""

# ---------- Step 6: Verify ----------
echo "=== Step 6/6: Verify setup ==="

echo "    ClusterQueue status:"
kubectl get clusterqueue tekton-cluster-queue -o jsonpath='    {.metadata.name}: {.status.conditions[?(@.type=="Active")].status}' 2>&1 || true
echo ""

echo "    LocalQueue status:"
kubectl get localqueue tekton-queue -n default -o jsonpath='    {.metadata.name}: {.status.conditions[?(@.type=="Active")].status}' 2>&1 || true
echo ""

echo "    tekton-kueue webhook on secondary:"
kubectl --server="https://localhost:${SECONDARY_PORT}" --insecure-skip-tls-verify=true \
  --token="${ADMIN_TOKEN}" \
  get mutatingwebhookconfiguration 2>&1 | sed 's/^/      /'

kill ${PF_PID} 2>/dev/null || true
trap - EXIT

echo ""
echo "============================================"
echo "  Phase 4 setup complete!"
echo ""
echo "  Verify with: make test-phase4"
echo "  Kueue + tekton-kueue are now managing PipelineRun quota."
echo ""
echo "  To submit a queued PipelineRun, add the label:"
echo "    kueue.x-k8s.io/queue-name: tekton-queue"
echo "============================================"
