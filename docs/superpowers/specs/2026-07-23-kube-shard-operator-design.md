# kube-shard Operator Design

## Overview

The kube-shard operator manages the lifecycle of secondary API servers that serve
CRD-based API groups via Kubernetes API aggregation. It replaces the manual
`setup-phase*.sh` scripts with a declarative, reconciliation-driven approach.

The operator is generic — not tied to Tekton or any specific CRD. Any API group
backed by CRDs can be aggregated to a Kine-backed secondary API server.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Primary CR name | `APIShard` | Explicit about what's being sharded |
| CR scope | Cluster-scoped | APIService, CRDs, and webhooks are all cluster-scoped |
| Architecture | Top-down config flow with sub-CRs | Matches Konflux operator pattern; each reconciler owns one resource type |
| Cert management | cert-manager (hardcoded conventions) | Already deployed on Konflux clusters; no user config needed |
| CRD source | Extract from primary | Leverages existing CRDs installed by upstream operators |
| CRD conflict handling | Detect and report; user removes CRDs | Operator avoids destructive actions on resources it doesn't own |
| Storage | SQLite, InClusterPostgreSQL, or external PostgreSQL | SQLite for dev, in-cluster PG for staging, external for prod |
| Namespace sync | Label selector | Dynamic, consistent with Konflux tenant model |
| Webhook sync | API group match | Self-consistent; operator knows which groups it aggregates |
| Operator namespace | `kube-shard-operator` | Standard pattern, operator manages shards in other namespaces |
| Testing | envtest + e2e (kind) | Unit tests fast with envtest; e2e validates real lifecycle |

## Custom Resource Definitions

### APIShard (cluster-scoped)

The user-facing parent CR. One `APIShard` = one secondary API server instance.

```yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: tekton-shard
spec:
  # Namespace where the shard stack is deployed
  targetNamespace: tekton-apiserver

  # API groups to aggregate to this shard
  apiGroups:
  - group: tekton.dev
    versions: ["v1", "v1beta1", "v1alpha1"]
  - group: resolution.tekton.dev
    versions: ["v1beta1", "v1alpha1"]

  # Storage backend configuration
  storage:
    type: PostgreSQL  # SQLite | InClusterPostgreSQL | PostgreSQL
    # Required when type=PostgreSQL:
    connectionSecretRef:
      name: pg-credentials
      namespace: tekton-apiserver
      key: dsn
    # Optional when type=InClusterPostgreSQL:
    # inCluster:
    #   resources:
    #     requests: { cpu: 100m, memory: 256Mi }
    #     limits: { memory: 1Gi }

  # Namespace sync configuration
  namespaceSync:
    labelSelector:
      matchLabels:
        konflux.dev/type: tenant

  # Secondary API server configuration
  secondary:
    replicas: 1
    image: registry.k8s.io/kube-apiserver:v1.36.2
    resources:
      requests: { cpu: 250m, memory: 512Mi }
      limits: { memory: 1Gi }

  # Kine configuration
  kine:
    replicas: 1
    image: rancher/kine:v0.14.14

  # Prometheus monitoring integration (optional)
  # monitoring:
  #   prometheusServiceAccountName: prometheus-k8s       # default
  #   prometheusServiceAccountNamespace: openshift-monitoring  # default

status:
  phase: Ready  # Provisioning | Blocked | Ready | Degraded
  connectionSecret:
    name: tekton-shard-admin-kubeconfig
    namespace: tekton-apiserver
  secondaryEndpoint: "https://tekton-shard-apiserver.tekton-apiserver.svc:443"
  conditions:
  - type: SecondaryHealthy
    status: "True"
  - type: CRDsInstalled
    status: "True"
  - type: APIServicesRegistered
    status: "True"
  - type: CRDConflictDetected
    status: "False"
    message: ""
  - type: NamespaceSyncReady
    status: "True"
  - type: WebhookSyncReady
    status: "True"
  observedGeneration: 1
```

**Status phases:**

| Phase | Meaning |
|-------|---------|
| `Provisioning` | Stack is being deployed (certs, Kine, kube-apiserver not yet healthy) |
| `Blocked` | CRDs exist on primary, APIService cannot take effect |
| `Ready` | All components healthy, API aggregation active |
| `Degraded` | Previously ready but a component is unhealthy (sub-CR failure, secondary down) |

### NamespaceSync (namespace-scoped, sub-CR)

Created and owned by the `APIShardReconciler`. One per `APIShard`.

```yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: NamespaceSync
metadata:
  name: tekton-shard-ns-sync
  namespace: tekton-apiserver
  ownerReferences:
  - apiVersion: kube-shard.konflux-ci.dev/v1alpha1
    kind: APIShard
    name: tekton-shard
spec:
  secondaryConnection:
    serviceRef:
      name: tekton-shard-apiserver
      namespace: tekton-apiserver
      port: 443
    authSecretRef:
      name: tekton-shard-admin-kubeconfig
    caSecretRef:
      name: tekton-shard-serving-ca
  labelSelector:
    matchLabels:
      konflux.dev/type: tenant
status:
  conditions:
  - type: Ready
    status: "True"
  syncedNamespaces: 42
  lastSyncTime: "2026-07-23T10:00:00Z"
```

### WebhookSync (namespace-scoped, sub-CR)

Created and owned by the `APIShardReconciler`. One per `APIShard`.

```yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: WebhookSync
metadata:
  name: tekton-shard-webhook-sync
  namespace: tekton-apiserver
  ownerReferences:
  - apiVersion: kube-shard.konflux-ci.dev/v1alpha1
    kind: APIShard
    name: tekton-shard
spec:
  secondaryConnection:
    serviceRef:
      name: tekton-shard-apiserver
      namespace: tekton-apiserver
      port: 443
    authSecretRef:
      name: tekton-shard-admin-kubeconfig
    caSecretRef:
      name: tekton-shard-serving-ca
  apiGroups:
  - tekton.dev
  - resolution.tekton.dev
status:
  conditions:
  - type: Ready
    status: "True"
  syncedWebhooks:
    validating: 2
    mutating: 1
  lastSyncTime: "2026-07-23T10:00:00Z"
```

## Reconciler Architecture

### Controllers

| Controller | Watches | Creates/Manages | Triggers |
|-----------|---------|----------------|----------|
| `APIShardReconciler` | `APIShard`, owned Deployments/Services/Secrets, CRDs (for configured groups) | Deployments, Services, Secrets, ConfigMaps, cert-manager Certificates, APIService objects, `NamespaceSync` CR, `WebhookSync` CR | APIShard spec change, owned resource change, CRD create/delete |
| `NamespaceSyncReconciler` | `NamespaceSync`, `Namespace` (primary) | Namespace objects on secondary | NamespaceSync spec change, Namespace create/delete on primary |
| `WebhookSyncReconciler` | `WebhookSync`, `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration` (primary) | Webhook configs on secondary | WebhookSync spec change, webhook create/update/delete on primary |

### Top-Down Config Flow

Following the [Konflux operator pattern](https://github.com/konflux-ci/konflux-ci/blob/main/AGENTS.md#architecture-notes):

- The `APIShardReconciler` forwards configuration to sub-CRs via their spec fields
- Sub-CR reconcilers read their own spec — never reach back to the parent `APIShard`
- This prevents circular dependencies and keeps each reconciler independently testable
- The `APIShardReconciler` watches sub-CR status and aggregates into `APIShard.status`

### APIShardReconciler Lifecycle

Each step is idempotent. The reconciler progresses through steps in order:

```
Reconcile(APIShard) {
  0. Handle deletion (finalizer — see below)
  0b. Ensure finalizer is present before any resource creation
  1. Ensure target namespace exists
  2. Ensure cert-manager Certificate resources
     → Requeue if certificate Secret not yet populated
  3. Ensure storage:
     - SQLite: no-op (Kine uses emptyDir)
     - InClusterPostgreSQL: deploy PostgreSQL Deployment + Service + Secret
     - PostgreSQL: validate connection Secret exists
  4. Ensure Kine Deployment + Service
     - Configure --endpoint based on storage type
     → Requeue if Kine not ready
  5. Ensure secondary kube-apiserver Deployment + Service
     - Mount: serving cert, front-proxy CA, SA keys, authz webhook config
     → Requeue if secondary not healthy (/healthz)
  6. Ensure admin connection Secret (kubeconfig for secondary)
     → Surface in status.connectionSecret
  7. Install CRDs on secondary
     - Extract CRDs for spec.apiGroups from primary
     - Apply to secondary (strip operator metadata)
     - Disable non-functional conversion webhooks (strategy: None)
  8. Ensure APIService objects registered on primary
     - Always register (even if CRDs exist — harmless when shadowed)
     - Check for CRD conflict: if CRDs exist for aggregated groups
       → Set CRDConflictDetected=True, phase=Blocked or Degraded
     - If no conflict: set CRDConflictDetected=False
  9. Create NamespaceSync sub-CR (gated on secondary healthy)
  10. Create WebhookSync sub-CR (gated on secondary healthy)
  11. Aggregate sub-CR statuses → update APIShard status + phase
}
```

#### Deletion: APIService Cleanup Finalizer

The reconciler uses a finalizer (`kube-shard.konflux-ci.dev/apiservice-cleanup`)
to ensure APIService objects are deregistered from the primary before the
APIShard is deleted.

**Why a finalizer instead of ownerReferences?**

Both `APIShard` and `APIService` are cluster-scoped, so ownerReferences would
work for garbage collection. However, GC deletes dependents concurrently and in
no guaranteed order. When an APIShard is deleted, the namespace-scoped resources
(secondary Deployment, Kine, Services) would be garbage-collected at the same
time as the APIServices. This creates a race window where the APIService still
exists and routes traffic for the aggregated API groups to a secondary that has
already been torn down, causing 503 errors for any client hitting those groups.

The finalizer gives the reconciler control over deletion order: it removes the
APIServices **first** (stopping the aggregation proxy from routing traffic to
the secondary), and only then allows the APIShard to be deleted — which triggers
cleanup of the namespace-scoped resources via the tracking client's owner labels.

**Requeue behavior:**
- Steps waiting on readiness (cert, Kine, secondary): `RequeueAfter: 5s`
- Steady state (everything healthy): no requeue (react to watches only)
- Degraded (sub-CR reports failure): `RequeueAfter: 30s`

### NamespaceSyncReconciler

```
Reconcile(NamespaceSync) {
  1. Build client to secondary (from spec.secondaryConnection)
     → If connection fails: set Ready=False reason=SecondaryUnavailable, requeue
  2. List namespaces on primary matching spec.labelSelector
  3. List namespaces on secondary
  4. For each primary namespace NOT on secondary: create it
  5. For each secondary namespace NOT on primary (and not system ns): delete it
  6. Update status (syncedNamespaces count, lastSyncTime, Ready=True)
}
```

Watches `Namespace` objects on primary — create/delete of a matching namespace
triggers reconciliation via `EnqueueRequestsFromMapFunc`.

### WebhookSyncReconciler

```
Reconcile(WebhookSync) {
  1. Build client to secondary (from spec.secondaryConnection)
     → If connection fails: set Ready=False reason=SecondaryUnavailable, requeue
  2. List ValidatingWebhookConfiguration + MutatingWebhookConfiguration on primary
  3. Filter: keep only webhooks with rules targeting spec.apiGroups
  4. For each matching webhook:
     a. Transform clientConfig.service → clientConfig.url
        (https://<service>.<namespace>.svc:<port><path>)
     b. Preserve caBundle
     c. Strip metadata (resourceVersion, uid, managedFields)
     d. Apply to secondary (create or update)
  5. For webhooks on secondary that no longer match primary: delete
  6. Update status (syncedWebhooks counts, lastSyncTime, Ready=True)
}
```

Watches `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` on
primary — any change triggers reconciliation via mapper.

## Secondary API Server Configuration

The operator generates the secondary kube-apiserver Deployment with these flags:

| Flag | Source |
|------|--------|
| `--etcd-servers` | Kine Service URL: `http://<shard>-kine.<ns>.svc:2379` |
| `--secure-port` | `6443` |
| `--tls-cert-file` / `--tls-private-key-file` | cert-manager Certificate Secret |
| `--requestheader-client-ca-file` | cert-manager Certificate Secret (front-proxy CA) |
| `--requestheader-allowed-names` | `front-proxy-client` |
| `--requestheader-username-headers` | `X-Remote-User` |
| `--requestheader-group-headers` | `X-Remote-Group` |
| `--requestheader-extra-headers-prefix` | `X-Remote-Extra-` |
| `--authorization-mode` | `Webhook` |
| `--authorization-webhook-config-file` | Generated ConfigMap (points to primary SAR) |
| `--authorization-webhook-version` | `v1` |
| `--service-account-key-file` | cert-manager Certificate Secret |
| `--service-account-signing-key-file` | cert-manager Certificate Secret |
| `--service-account-issuer` | `https://kubernetes.default.svc` |
| `--disable-admission-plugins` | `NamespaceLifecycle,ServiceAccount` |
| `--enable-admission-plugins` | `MutatingAdmissionWebhook,ValidatingAdmissionWebhook` |

### Authorization Webhook Config

Generated ConfigMap containing a kubeconfig that points the secondary's authz
webhook at the primary's SubjectAccessReview API using projected ServiceAccount
token:

```yaml
apiVersion: v1
kind: Config
clusters:
- name: primary
  cluster:
    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
    server: https://kubernetes.default.svc
users:
- name: secondary-apiserver
  user:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
contexts:
- name: default
  context:
    cluster: primary
    user: secondary-apiserver
current-context: default
```

### Naming Conventions

All generated resources follow `<shard-name>-<purpose>`:

| Resource | Name pattern |
|----------|-------------|
| Kine Deployment | `<shard>-kine` |
| Kine Service | `<shard>-kine` |
| Secondary Deployment | `<shard>-apiserver` |
| Secondary Service | `<shard>-apiserver` |
| PostgreSQL Deployment | `<shard>-postgresql` |
| PostgreSQL Service | `<shard>-postgresql` |
| Serving Certificate | `<shard>-serving-cert` |
| Front-proxy CA Certificate | `<shard>-front-proxy-ca` |
| Admin kubeconfig Secret | `<shard>-admin-kubeconfig` |
| Authz webhook ConfigMap | `<shard>-authz-config` |
| SA signing Secret | `<shard>-sa-signing` |

## CRD Conflict Detection

The operator does NOT delete CRDs or scale down upstream operators. It detects
and reports conflicts, leaving remediation to the user.

**Behavior:**

1. APIService objects are **always** registered (they're harmless when shadowed
   by Local APIServices created by the CRD controller)
2. On each reconcile, check if CRDs exist on primary for aggregated groups
3. If CRDs exist: set `CRDConflictDetected=True`, update phase
4. If CRDs don't exist: set `CRDConflictDetected=False`
5. Watch CRDs for configured API groups to react quickly to create/delete events

**Why keep APIService registered during conflict:**

When the user removes the conflicting CRDs, the aggregator controller
automatically removes the auto-managed Local APIService. Since our external
APIService is already registered, traffic immediately routes to the secondary
with no operator action required. This is simpler and more self-healing than
deleting and re-creating the APIService.

**Status example during conflict:**

```yaml
status:
  phase: Blocked
  conditions:
  - type: CRDConflictDetected
    status: "True"
    reason: CRDsExistOnPrimary
    message: "CRDs for tekton.dev still exist on primary. Remove them and
              scale down the Tekton Operator to enable API aggregation.
              Conflicting CRDs: pipelineruns.tekton.dev, taskruns.tekton.dev, ..."
```

## Storage Backends

| Type | Kine endpoint | Managed by operator | Use case |
|------|--------------|--------------------:|----------|
| `SQLite` | `sqlite:///data/kine.db` | emptyDir volume on Kine pod | Dev, CI, experiments |
| `InClusterPostgreSQL` | `postgres://...@<shard>-postgresql.<ns>.svc:5432/kine?sslmode=disable` | PostgreSQL Deployment + Service + Secret | Staging, single-cluster |
| `PostgreSQL` | From user-provided Secret | Nothing (user manages DB) | Production (RDS, CloudSQL) |

**SQLite limitations:** Single Kine replica only. Data is ephemeral (emptyDir) —
lost on pod restart. Not suitable for anything beyond quick testing.

## RBAC Requirements

The operator ServiceAccount (`kube-shard-operator` namespace) needs:

| Resource | Verbs | Reason |
|----------|-------|--------|
| `APIShard`, `NamespaceSync`, `WebhookSync` | all | Own CRDs |
| `Deployment`, `Service`, `Secret`, `ConfigMap` | all | Manage shard stack |
| `Namespace` | get, list, watch, create | Namespace sync (watch primary, create on secondary via client) |
| `CustomResourceDefinition` | get, list, watch | CRD conflict detection + extraction |
| `APIService` | get, list, create, update, delete | Register aggregation |
| `ValidatingWebhookConfiguration` | get, list, watch | Webhook sync source |
| `MutatingWebhookConfiguration` | get, list, watch | Webhook sync source |
| `Certificate` (cert-manager.io) | get, list, create, update, delete | Cert lifecycle |
| `ClusterIssuer` (cert-manager.io) | get, list | Reference issuer |

The secondary API server's ServiceAccount needs `system:auth-delegator`
ClusterRoleBinding for SubjectAccessReview delegation to the primary.

## Dependencies

The operator reuses packages from the [Konflux operator](https://github.com/konflux-ci/konflux-ci/tree/main/operator/pkg) to avoid reimplementing common patterns:

| Package | Use in kube-shard |
|---------|-------------------|
| `pkg/tracking` | Track owned resources, detect drift |
| `pkg/customization` | Apply user customizations to generated manifests |
| `pkg/hashedconfigmap` | Create ConfigMaps with content hashes (triggers rollouts on config change) |
| `pkg/hashedsecret` | Create Secrets with content hashes |
| `pkg/clusterinfo` | Detect cluster capabilities (e.g., cert-manager availability) |
| `pkg/kubernetes` | Kubernetes utility helpers |

Import as: `github.com/konflux-ci/konflux-ci/operator/pkg/<package>`

## Project Structure

```
operator/
├── api/
│   └── v1alpha1/
│       ├── apishard_types.go
│       ├── namespacesync_types.go
│       ├── webhooksync_types.go
│       ├── groupversion_info.go
│       └── zz_generated.deepcopy.go
├── cmd/
│   └── main.go
├── internal/
│   ├── controller/
│   │   ├── apishard_controller.go
│   │   ├── apishard_controller_test.go
│   │   ├── namespacesync_controller.go
│   │   ├── namespacesync_controller_test.go
│   │   ├── webhooksync_controller.go
│   │   ├── webhooksync_controller_test.go
│   │   └── suite_test.go
│   └── secondary/
│       ├── client.go
│       └── client_test.go
├── config/
│   ├── crd/
│   ├── rbac/
│   ├── manager/
│   └── samples/
│       └── tekton-shard.yaml
├── test/
│   └── e2e/
│       ├── e2e_suite_test.go
│       ├── apishard_test.go
│       ├── namespacesync_test.go
│       └── webhooksync_test.go
├── go.mod
├── go.sum
├── Makefile
└── Dockerfile
```

## Testing Strategy

| Layer | Framework | What it tests | Speed |
|-------|-----------|--------------|-------|
| Unit | envtest + Gomega | Each reconciler in isolation, mock secondary client | ~30s |
| Integration | envtest + real secondary | Full reconcile loop with a real secondary kube-apiserver | ~2min |
| E2E | Ginkgo + kind | Full operator lifecycle, sync validation, degradation scenarios | ~5min |

**Key test scenarios:**

- APIShard creation → stack deploys → secondary healthy
- CRD conflict detection → status shows Blocked
- CRDs removed → APIService takes effect → status Ready
- Namespace with matching label created → synced to secondary
- Webhook caBundle rotated → secondary webhook config updated
- Secondary pod killed → sub-CRs report Degraded → pod recovers → Ready
- APIShard deleted → all owned resources cleaned up (ownerReferences GC)

## Implementation Phases

Ordered for incremental delivery. Each phase produces a working, testable state.

### Phase 1: Scaffold + APIShard CR + basic lifecycle

**Goal:** Operator deploys a functional secondary kube-apiserver + Kine stack
from an `APIShard` CR.

**Tasks:**

1. Scaffold operator with Kubebuilder (`kubebuilder init`, `kubebuilder create api`)
2. Define `APIShard` CRD types (spec + status)
3. Implement `APIShardReconciler` steps 1-6:
   - Ensure target namespace
   - Create cert-manager Certificate resources
   - Deploy Kine (SQLite mode for simplicity in this phase)
   - Deploy secondary kube-apiserver with full flag set
   - Create admin kubeconfig Secret
4. Implement status conditions: `SecondaryHealthy`
5. Write envtest tests for the reconciler
6. Add Makefile targets: `make test`, `make build`, `make deploy`

**Deliverable:** `kubectl apply` an `APIShard` CR → operator deploys the stack →
secondary responds on `/healthz` → status shows `SecondaryHealthy=True`.

### Phase 2: CRD aggregation + conflict detection

**Goal:** Operator installs CRDs on secondary and registers APIService objects.

**Tasks:**

1. Implement `APIShardReconciler` step 7: extract CRDs from primary, install on
   secondary, disable conversion webhooks
2. Implement step 8: register APIService objects, detect CRD conflicts
3. Add CRD watch (for configured API groups) to trigger reconciliation
4. Implement status conditions: `CRDsInstalled`, `APIServicesRegistered`,
   `CRDConflictDetected`
5. Implement `phase` transitions: Provisioning → Blocked / Ready
6. Write envtest tests for CRD extraction, APIService registration, conflict
   detection

**Deliverable:** With CRDs removed from primary, traffic for configured API
groups routes through the secondary. Status accurately reflects conflict state.

### Phase 3: NamespaceSync sub-CR + reconciler

**Goal:** Namespaces are automatically mirrored from primary to secondary.

**Tasks:**

1. Define `NamespaceSync` CRD types
2. Implement `NamespaceSyncReconciler`:
   - Build client to secondary from spec
   - List/create/delete namespaces based on label selector
   - Handle secondary unavailable gracefully
3. Add Namespace watch on primary with `EnqueueRequestsFromMapFunc`
4. Implement gated creation in `APIShardReconciler` (step 9)
5. Implement status aggregation: `NamespaceSyncReady` on `APIShard`
6. Write envtest tests (mock secondary client)

**Deliverable:** Create a namespace with the configured label on primary → it
appears on secondary within seconds. Delete from primary → removed from secondary.

### Phase 4: WebhookSync sub-CR + reconciler

**Goal:** Admission webhooks are automatically mirrored with service→url
transform.

**Tasks:**

1. Define `WebhookSync` CRD types
2. Implement `WebhookSyncReconciler`:
   - Filter webhooks by API group rules match
   - Transform `clientConfig.service` → `clientConfig.url`
   - Preserve and sync caBundle
   - Handle secondary unavailable gracefully
3. Add ValidatingWebhookConfiguration + MutatingWebhookConfiguration watches
4. Implement gated creation in `APIShardReconciler` (step 10)
5. Implement status aggregation: `WebhookSyncReady` on `APIShard`
6. Write envtest tests (mock secondary client)

**Deliverable:** Tekton webhook rotates CA → operator detects caBundle change →
secondary's webhook configs updated → admission continues working.

### Phase 5: Storage backend options

**Goal:** Support all three storage modes (SQLite, InClusterPostgreSQL,
PostgreSQL).

**Tasks:**

1. Implement `InClusterPostgreSQL` mode:
   - Deploy PostgreSQL Deployment + Service + credentials Secret
   - Configure Kine with PostgreSQL endpoint
2. Implement external `PostgreSQL` mode:
   - Validate connection Secret exists
   - Configure Kine with user-provided DSN
3. SQLite already works from Phase 1 (default)
4. Write tests for each storage mode

**Deliverable:** `APIShard` with any `storage.type` value deploys correctly.

### Phase 6: E2E tests

**Goal:** End-to-end validation using a kind cluster.

**Tasks:**

1. Set up e2e test infrastructure (Ginkgo suite, kind cluster provisioning)
2. Test scenarios:
   - APIShard creation → full stack healthy
   - CRD removal → APIService routing works → PipelineRun creation succeeds
   - Namespace sync (create labeled namespace → appears on secondary)
   - Webhook sync (create webhook targeting aggregated group → mirrored)
   - Degradation (kill secondary pod → status degrades → recovers)
   - CRD re-creation → conflict detected → status reflects it
   - APIShard deletion → cleanup (ownerReferences GC)
3. CI integration: `make test-e2e`

**Deliverable:** `make test-e2e` passes against a kind cluster with the operator
installed.

## Future Considerations (out of scope for Phase 7)

- **Upstream operator coordination:** Generic "CRD guard" admission webhook or
  upstream operator flags (e.g., `tektonconfig.spec.pipeline.manageCRDs: false`)
  to prevent CRD re-creation. Explored in Phase 7 but implemented later.
- **Multi-shard:** Multiple `APIShard` CRs on one cluster, each serving different
  API groups. The design supports this (naming conventions, per-shard namespaces)
  but testing is deferred.
- **HA secondary:** Multiple replicas of the secondary kube-apiserver. Requires
  shared Kine (PostgreSQL) and leader election for CRD installation.
- **Metrics and monitoring:** Expose Prometheus metrics for sync lag, secondary
  health, reconcile latency.
- **CRD source alternatives:** If upstream operators can be configured to skip
  CRD installation, the operator could fetch CRDs from OCI artifacts or release
  URLs instead of extracting from primary.
