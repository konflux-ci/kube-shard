# Secondary API Server for Tekton CRDs (Kine + PostgreSQL)

## Summary

Eliminate the etcd 8 GB size constraint on per-cluster PipelineRun concurrency by running a secondary kube-apiserver backed by PostgreSQL (via [Kine](https://github.com/k3s-io/kine)). Tekton CRDs are installed on the secondary server, and the main kube-apiserver routes `tekton.dev` and `resolution.tekton.dev` requests to it via Kubernetes API aggregation (APIService).

This removes the ~300 concurrent PipelineRun ceiling without waiting for upstream CRD sharding support (uncertain, 12-18+ months) or requiring ROSA HCP migration. PostgreSQL has no practical size limit for this workload, enabling 1000+ concurrent PipelineRuns per cluster.

## Problem Statement

Etcd's 8 GB memory limit is the primary ceiling on per-cluster PipelineRun concurrency. Each PipelineRun with ~22 TaskRuns consumes ~23 MB of etcd storage (accounting for revisions from multiple controllers updating status). At ~300 concurrent PipelineRuns, the cluster approaches the 8 GB limit.

The upstream Kubernetes effort to support CRD sharding via `--etcd-servers-overrides` is at a very early stage ([kubernetes/kubernetes#118858](https://github.com/kubernetes/kubernetes/issues/118858), [PR #139883](https://github.com/kubernetes/kubernetes/pull/139883)) and is currently on hold. Optimistically 12-18 months from agreement; pessimistically not at all.

Etcd sharding by resource kind (Events, Leases, Pods) is available via HyperShift but only for built-in Kubernetes resources -- CRDs cannot be sharded. This approach bypasses both limitations using existing, proven mechanisms.

## Architecture

```mermaid
flowchart TB
    subgraph mainCP ["Main Control Plane (managed by ROSA)"]
        mainKAS["kube-apiserver<br/>(main)"]
        etcd["etcd (8 GB)"]
        mainKAS --> etcd
    end

    subgraph secondaryCP ["Secondary API Server (Konflux-managed)"]
        secKAS["kube-apiserver<br/>(secondary)"]
        kine["Kine"]
        secKAS -->|"etcd v3 gRPC"| kine
    end

    subgraph storage ["Storage Backend"]
        pg["PostgreSQL<br/>(RDS in prod, in-cluster for dev)"]
        kine -->|"SQL"| pg
    end

    subgraph webhooks ["Admission Webhooks"]
        tektonWH["Tekton Webhook"]
        kueueWH["Kueue / tekton-kueue"]
    end

    mainKAS -->|"APIService proxy<br/>(tekton.dev, resolution.tekton.dev)"| secKAS
    secKAS -->|"authz delegation<br/>(SubjectAccessReview)"| mainKAS
    secKAS --> tektonWH
    secKAS --> kueueWH

    subgraph controllers ["Controllers (unchanged)"]
        tektonCtrl["Tekton Pipeline Controller"]
        chainsCtrl["Tekton Chains"]
        kueueCtrl["tekton-kueue"]
        konfluxCtrl["Konflux Controllers"]
    end

    controllers -->|"all API calls"| mainKAS
```

### Key Design Properties

- **Controllers don't change.** They talk to the main kube-apiserver using their normal ServiceAccount tokens. API aggregation transparently routes `tekton.dev` and `resolution.tekton.dev` requests to the secondary server.
- **No etcd size limit.** PostgreSQL can handle hundreds of GB. The per-cluster PipelineRun ceiling becomes compute-bound, not storage-bound.
- **Works on ROSA Classic today.** APIService is standard Kubernetes API; no HCP migration or platform changes required.
- **Complements other scaling strategies.** Orthogonal to MultiKueue, etcd sharding (built-in resources), and per-PipelineRun footprint reduction. Can be combined with any of them.

### Resource Placement

| Resource | Location | Notes |
|----------|----------|-------|
| PipelineRun, TaskRun, Pipeline, Task, StepAction, CustomRun | Secondary → PostgreSQL | No etcd involvement |
| ResolutionRequest | Secondary → PostgreSQL | Short-lived, created per pipeline run |
| Pods, ConfigMaps, Secrets, Events, Leases | Main → etcd | Unchanged |
| Namespaces | Both (mirrored) | Sync controller copies from main → secondary |
| Konflux CRDs (Snapshot, Release) | Phase 9: Secondary → PostgreSQL | Start with Tekton only |

## Secondary API Server Configuration

The secondary kube-apiserver runs as a Deployment in a dedicated namespace (`tekton-apiserver`) on the Konflux cluster.

### Key Flags

| Flag | Value | Rationale |
|------|-------|-----------|
| `--etcd-servers` | `http://kine.tekton-apiserver.svc:2379` | Kine endpoint (in-cluster) |
| `--authorization-mode` | `Webhook` | Delegates authz to main cluster |
| `--authorization-webhook-config-file` | `/etc/kube/authz/webhook-config.yaml` | Points to main cluster's SAR endpoint |
| `--authorization-webhook-version` | `v1` | Required for k8s 1.36+ (default is v1beta1) |
| `--requestheader-client-ca-file` | `/etc/kube/pki/front-proxy-ca.crt` | Trust the main KAS identity headers |
| `--requestheader-allowed-names` | `front-proxy-client` | Only accept headers from the main KAS |
| `--disable-admission-plugins` | `NamespaceLifecycle,ServiceAccount,ResourceQuota` | Namespaces are mirrored; SA tokens handled by main; see [Resource Policy Enforcement](#resource-policy-enforcement) |
| `--enable-admission-plugins` | `MutatingAdmissionWebhook,ValidatingAdmissionWebhook` | For Tekton/Kueue webhooks |
| `--tls-cert-file` / `--tls-private-key-file` | Serving cert for the Service | TLS between main KAS and secondary |
| `--service-account-key-file` | SA signing public key | Required by kube-apiserver binary (unused for auth) |

Note: `--service-account-key-file` and `--service-account-signing-key-file` are required by the kube-apiserver binary but not used for authentication. The secondary never validates raw ServiceAccount tokens -- the main KAS authenticates all tokens and passes the authenticated identity via request headers. The secondary only trusts these headers (via `--requestheader-client-ca-file`).

For the authz webhook callback (secondary → main KAS SubjectAccessReview), the secondary pod runs as a ServiceAccount with `system:auth-delegator` ClusterRoleBinding. The webhook kubeconfig references the kubelet-projected token (`tokenFile`) and cluster CA from the pod's mounted service account volume -- no manual token management required.

### What is NOT enabled on the secondary

- No scheduler, no controller-manager, no kubelet integration
- No built-in controllers (garbage collection handled by main cluster's GC through the aggregation layer)
- No service account token validation (tokens authenticated by main cluster; secondary receives identity via request headers)

### Deployment Spec

- **Replicas:** 3 (HA -- loss of a single pod doesn't interrupt service)
- **Resources:** ~2 CPU, ~4 GB memory per replica (to be validated under load)
- **Node placement:** Anti-affinity across availability zones
- **Health checks:** `/livez` and `/readyz` endpoints (built into kube-apiserver)

### Kine Deployment

Kine runs as a separate Deployment in the same namespace, exposing an etcd v3 gRPC endpoint:

- **Replicas:** 2-3 (stateless translator, can scale horizontally)
- **Connection to PostgreSQL:** Standard connection string, TLS enforced, credentials from a Secret

## Authentication and Authorization

**Authentication** is handled entirely by the main kube-apiserver. The main server authenticates the request, then passes the authenticated identity to the secondary via request headers (`X-Remote-User`, `X-Remote-Group`, `X-Remote-Extra-*`). The secondary trusts these headers via `--requestheader-client-ca-file`.

**Authorization** is NOT handled by the main kube-apiserver for aggregated APIs. The main server only checks a coarse proxy-level permission on the APIService, not fine-grained RBAC on the target resource.

The secondary uses `--authorization-mode=Webhook`, delegating every authz decision to the main cluster's SubjectAccessReview API:

1. Request arrives at main KAS → authenticated → proxied to secondary with user identity in headers
2. Secondary receives request → calls SubjectAccessReview on main cluster → "can user X create pipelineruns in namespace foo?"
3. Main cluster evaluates its RBAC rules (same RoleBindings that exist today) → returns allow/deny
4. Secondary proceeds or rejects

RBAC rules remain managed exclusively on the main cluster. No RBAC synchronization is needed.

## Admission Webhooks

The main kube-apiserver does **not** run admission webhooks for aggregated API requests -- it only proxies. The secondary server is responsible for its own admission pipeline.

Tekton's validation/mutation webhooks and Kueue's admission controller need to be registered on the secondary server (as ValidatingWebhookConfiguration/MutatingWebhookConfiguration objects in the secondary's store). They point to the same in-cluster webhook Services, which are reachable from the secondary via cluster DNS.

### caBundle synchronization

The Tekton webhook controller self-manages its TLS CA and injects the `caBundle` into webhook configurations and CRD conversion specs -- but only on objects in the **primary's** store. The secondary receives these objects via a copy/sync mechanism:

1. The WebhookSync controller extracts fully-configured webhook configs (with `caBundle` already injected) from the primary
2. Applies them to the secondary's store (transforming `clientConfig.service` to `clientConfig.url`)
3. The CRD sync transforms conversion webhook `clientConfig` the same way

**Cert rotation:** The Tekton webhook may rotate its CA on pod restart. When this happens, the `caBundle` on the secondary becomes stale. The WebhookSync controller watches for `caBundle` changes and automatically mirrors updates to the secondary.

### CRD conversion webhooks

Tekton CRDs declare `v1beta1` as a served version with `spec.conversion.strategy: Webhook`, pointing at `tekton-pipelines-webhook`. However, the Tekton webhook server does **not** implement a conversion endpoint (responds with "no controller registered for: /"). This means CRD conversion is non-functional regardless of how the webhook is configured.

The operator handles this during CRD sync by transforming `spec.conversion.webhook.clientConfig.service` references to URL-based references, preserving the `caBundle`. If the webhook endpoint is non-functional, the conversion strategy can be set to `None` on the secondary. Only v1 (the stored version) is actively used.

### Webhook `clientConfig.service` vs `url`

When a webhook config uses `clientConfig.service`, the API server looks up the Service object in its **own** store (not via DNS). Since the secondary doesn't have `tekton-pipelines-webhook` Service in its store, service-based webhook references fail.

The WebhookSync controller transforms all `clientConfig.service` references to `clientConfig.url` (e.g., `https://tekton-pipelines-webhook.tekton-pipelines.svc:443/defaulting`). URL-based references use standard DNS resolution, which works because the secondary pod is in the same cluster and can resolve cluster-internal Service names.

## Resource Policy Enforcement

ResourceQuota and LimitRange are **not synced** from the primary to the secondary, and ResourceQuota is explicitly disabled as an admission plugin on the secondary. This is intentional, not an oversight.

**LimitRange is irrelevant.** LimitRange only applies to Pods, Containers, and PersistentVolumeClaims. The secondary stores custom resources (PipelineRuns, TaskRuns, Pipelines, etc.) -- none of which are affected by LimitRange. Syncing it would be dead weight.

**ResourceQuota is explicitly disabled** (`--disable-admission-plugins` includes `ResourceQuota`) because naively syncing quota objects from the primary would give a false sense of enforcement:

- **Typical quotas don't apply.** Most ResourceQuota specs constrain compute resources (`cpu`, `memory`, `pods`, `services`). The secondary doesn't host any of those.
- **Count quotas can't be naively synced.** While ResourceQuota supports `count/<resource>.<group>` quotas (e.g., `count/pipelineruns.tekton.dev`), blindly copying the primary's quota object to the secondary would be incorrect. The quota on the primary was set with the expectation that it governs the same API server that tracks usage. Once the resource lives on the secondary, the primary's quota object becomes a stale copy with independent usage tracking -- a split-brain problem.
- **Making it explicit avoids confusion.** Without disabling the plugin, the ResourceQuota admission controller runs but finds no quota objects (since none are synced), silently allowing everything. Disabling it makes the behavior obvious.

If resource count enforcement on the secondary is ever needed, the recommended approach is a **validating admission webhook** that checks the primary's quotas on each create request, since the primary remains the source of truth for policy. A dedicated `ResourceQuotaSync` controller (following the WebhookSync pattern) is another option, but would require solving the split-brain usage tracking problem.

## Namespace Synchronization

`NamespaceLifecycle` admission is disabled on the secondary. However, the secondary still needs Namespace objects in its store for list/watch to scope correctly.

A lightweight sync controller (running on the main cluster) watches Namespaces and mirrors create/delete to the secondary. This will be one reconciliation loop of the **kube-shard-operator** (a Kubebuilder-based operator that owns the full lifecycle of the secondary API server):

- Watches Namespaces on main → creates/deletes corresponding Namespace on secondary
- Only syncs `metadata.name` and `metadata.labels`
- Uses a label selector (e.g., `konflux.dev/tenant`) to limit scope to tenant namespaces
- Ignores system namespaces (`kube-system`, `openshift-*`, etc.)

The kube-shard-operator will also handle webhook config sync, CRD management, APIService registration, and secondary lifecycle management -- consolidating all sync/management concerns into a single operator.

**Failure modes:**
- Sync controller behind: new namespaces won't exist on secondary immediately. PipelineRun creation still accepted (NamespaceLifecycle disabled), but list by namespace returns empty until sync catches up.
- Secondary unreachable: sync controller retries with backoff. No data loss.

**Namespace deletion:** When a namespace is deleted on main, the sync controller deletes it on secondary. The main cluster's garbage collector handles cascading deletion of Tekton resources in that namespace (via the aggregation layer).

## Garbage Collection

Garbage collection is handled by the main cluster's `kube-controller-manager`, NOT the secondary. The GC discovers aggregated APIs via discovery, watches resources through the aggregation proxy, and deletes orphans via the same path.

Cross-server ownership chains work correctly:

| Owner (deleted) | Dependent (GC'd) | Mechanism |
|---|---|---|
| PipelineRun → TaskRun | Both on secondary | GC watches/deletes both via aggregation |
| TaskRun → Pod | TaskRun on secondary, Pod on main | GC watches TaskRuns via aggregation, deletes Pods directly |
| Namespace → PipelineRun | Namespace on main, PipelineRun on secondary | Namespace deletion triggers GC of all resources in that namespace |

If the secondary is temporarily unavailable, the GC retries (does not prematurely delete dependents).

## Storage Layer

### Kine

[Kine](https://github.com/k3s-io/kine) translates etcd's gRPC API into SQL operations. It's the same component powering k3s clusters running on PostgreSQL/MySQL/SQLite.

### PostgreSQL (Production -- AWS RDS)

| Property | Value | Rationale |
|----------|-------|-----------|
| Engine | PostgreSQL 15+ | Kine's recommended backend |
| Instance class | `db.r6g.large` (2 vCPU, 16 GB) initially | Right-size based on load testing |
| Storage | 100 GB gp3, auto-scaling | PipelineRun data is transient (TTL'd); steady-state bounded |
| Multi-AZ | Yes | HA with automatic failover |
| Encryption | At rest (KMS) + in transit (TLS) | Standard security posture |
| Backup | Automated daily snapshots, 7-day retention | Recovery from corruption |
| Dedicated instance | Yes (separate from Tekton Results RDS) | Isolate write-heavy Kine from read-heavy Results |

### Deployment Modes

| Environment | Backend | Purpose |
|-------------|---------|---------|
| kind / dev | Kine + SQLite | Local development and testing |
| kind / dev | Kine + in-cluster PostgreSQL | PostgreSQL-specific behavior validation |
| ROSA staging | Kine + RDS PostgreSQL | Production-representative validation |
| ROSA production | Kine + RDS PostgreSQL (Multi-AZ) | Full production deployment |

### Capacity Estimates

| Concurrent PipelineRuns | Estimated data | PostgreSQL feasibility |
|---|---|---|
| 300 (today's ceiling) | ~7 GB | Trivial |
| 1000 (3x) | ~23 GB | Comfortable |
| 3000 (10x) | ~69 GB | Well within RDS limits |

With Tekton's PipelineRun pruner (TTL-based cleanup), steady-state size is bounded by the retention window.

## API Aggregation Setup

### APIService Registration

One APIService per group/version:

```yaml
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1.tekton.dev
spec:
  group: tekton.dev
  version: v1
  service:
    namespace: tekton-apiserver
    name: tekton-apiserver
  caBundle: <secondary-server-CA>
  groupPriorityMinimum: 1000
  versionPriority: 100
```

Additional APIService objects for:
- `v1beta1.tekton.dev` (if still served)
- `v1beta1.resolution.tekton.dev`
- `v1alpha1.resolution.tekton.dev` (if applicable)

### Migration from CRDs to APIService

CRDs and aggregated APIService objects cannot coexist for the same group/version by default. This was validated experimentally:

**Proven behavior:** When a CRD exists on the primary for the same group/version as an aggregated APIService (one with `spec.service` pointing to an external server), the kube-aggregator controller:

1. Labels the APIService with `kube-aggregator.kubernetes.io/automanaged: "true"`
2. **Removes the `service` field** from the APIService spec
3. Converts the APIService status to `reason: Local` with message "Local APIServices are always available"
4. The primary now serves the resource directly from its own etcd (via CRD handler), bypassing aggregation entirely

**Consequence:** All data stored on the secondary (Kine/SQLite/PostgreSQL) becomes invisible to clients of the primary. The primary's etcd has no Tekton resources, so clients see an empty list. There is a brief race window where the first request may still reach the secondary before the kube-aggregator reconciles, but it takes over within seconds.

**Recovery:** Deleting the CRDs from the primary and re-applying the APIService with the `service` field restores proper aggregation and all data reappears from the secondary.

**Production implication:** If the OpenShift Pipelines operator (or any Tekton installation) creates CRDs on the same cluster, it will immediately break aggregation. The kube-shard operator handles this automatically by overriding the kube-aggregator's auto-register controller -- it sets the `automanaged` label to `"false"` on APIService objects via SSA and syncs conflicting CRDs to the secondary. This allows CRDs and aggregated APIService objects to coexist without manual CRD removal.

Migration sequence for existing clusters:

1. **Drain:** Stop pipeline activity (or accept brief disruption)
2. **Export:** Dump live Tekton resources from etcd (non-terminal PipelineRuns/TaskRuns)
3. **Delete CRDs:** Remove Tekton CRDs from main cluster
4. **Deploy secondary:** kube-apiserver + Kine + PostgreSQL; install Tekton CRDs on secondary
5. **Register APIService:** Main cluster routes tekton.dev to secondary
6. **Import:** Restore live resources into secondary via API
7. **Resume:** Controllers reconnect (watches re-establish through aggregation)

For new deployments, no migration is needed -- the operator installs CRDs on the secondary and registers APIService objects while existing CRDs remain on the primary.

## High Availability and Failure Modes

| Failure | Impact | Recovery |
|---------|--------|----------|
| Secondary KAS pod crash | Remaining replicas serve; transparent | Seconds (pod restart) |
| All secondary KAS pods down | Tekton API calls fail (503); running Pods continue; no new pipelines start | Pod rescheduling (seconds to minutes) |
| Kine crash | Secondary KAS loses backend; API calls fail | Pod restart (seconds) |
| RDS failover | ~60-120s disruption; Kine reconnects | Automatic (RDS-managed) |
| RDS data loss | Total loss of Tekton state | Restore from backup |
| Namespace sync controller down | New namespaces don't propagate; existing operations unaffected | Pod restart |

The blast radius of secondary API server failure is similar to today's etcd failure -- both stop all Tekton operations. This design replaces one critical dependency (managed etcd) with another (Konflux-managed secondary KAS + PostgreSQL), trading a hard size constraint for operational responsibility.

## Risks and Open Questions

| Risk | Severity | Mitigation |
|------|----------|------------|
| **Kine watch performance at scale** -- SQL-polling watch handling 6000+ objects with 5+ concurrent watchers | High | Load testing validated up to 1000+ PipelineRuns. Kine polling interval is configurable. Upstream k3s benchmarks available. |
| **ROSA compatibility** -- custom APIService registration on managed ROSA | Medium | Standard K8s API; OCP uses it extensively. Validate that ROSA operators don't reconcile/remove user-created APIServices. |
| **OpenShift Pipelines operator conflict** -- operator expects to own Tekton CRDs | High | **Validated:** CRDs on primary cause kube-aggregator to auto-manage the APIService, removing the `service` field and breaking aggregation. Operator CRD management must be disabled on Konflux clusters. Long-term: operator-native support. |
| **Tekton controller assumptions** -- controllers relying on cross-resource resourceVersion ordering | Medium | Code review of Tekton pipeline controller. Unlikely (informer-based, not direct etcd). |
| **Webhook certificate management** -- Tekton webhook self-generates certs | Medium | Provide certs externally (cert-manager) and disable self-cert logic. |
| **Observability** -- etcd dashboards don't apply; need new PostgreSQL/Kine monitoring | Low | Build dashboards for Kine metrics and RDS CloudWatch. |

## Validation History

The design was validated incrementally through a series of phases before the operator was built. Key findings are preserved inline in the relevant sections above. The phases were:

1. **Basic wiring** (kind + SQLite) -- validated API aggregation end-to-end with Tekton controllers
2. **Webhook authorization + RBAC** -- validated `--authorization-mode=Webhook` delegation to main cluster's SubjectAccessReview
3. **Tekton admission webhooks** -- validated webhook mirroring with `clientConfig.service` → `clientConfig.url` transformation
4. **Kueue + tekton-kueue integration** -- validated Kueue quota system with the aggregated API server
5. **PostgreSQL backend** -- validated production-representative storage with Kine + PostgreSQL
6. **Konflux integration** -- validated Tekton Chains, real Konflux pipelines, and interaction with Konflux controllers
7. **kube-shard operator** -- replaced all manual scripts with the operator (see `operator/`)

### Tekton Operator CRD Ownership

The Tekton Operator manages pipeline CRDs via `TektonInstallerSet` resources and reconciles them continuously. Since CRDs and APIService objects for the same group/version cannot coexist on the primary (the aggregator auto-manages Local APIServices when CRDs exist), this creates a conflict.

The kube-shard operator handles this automatically by overriding the auto-register controller -- it sets the `automanaged` label to `"false"` via SSA with ForceOwnership. This allows CRDs to remain on the primary while aggregation routes requests to the secondary.

**Design constraint:** The kube-shard operator does NOT patch upstream operator resources (e.g., TektonInstallerSets). That approach is fragile, couples kube-shard to implementation details of every upstream operator, and would need to be re-implemented for each new operator we integrate with.

**Alternative approaches considered:**

1. **Upstream Tekton Operator change** -- a flag like `tektonconfig.spec.pipeline.manageCRDs: false` to skip CRD installation. Clean for Tekton but does not generalize.
2. **Generic "CRD guard" admission webhook** -- reject CREATE operations on CRDs for aggregated API groups. Generic but may cause operator error/retry loops.
3. **Aggregation-aware CRD coexistence** -- upstream Kubernetes change to prefer APIService with a `service` field over auto-managed Local APIService. Would solve the problem for everyone.

The current approach (always overriding the auto-register controller) is the implemented solution. Upstream changes remain worth pursuing for a cleaner long-term path.

## Roadmap

### Konflux API groups aggregation (Snapshots + Releases)

Extend the secondary to serve additional Konflux-specific CRDs:

1. Install Snapshot and Release CRDs on secondary
2. Register APIService objects for `appstudio.redhat.com` (Snapshots, Releases)
3. Validate Konflux controllers continue to reconcile these resources
4. Verify cross-resource ownership chains (GC) work across groups

**Success criteria:** Snapshot and Release resources stored on secondary (PostgreSQL); controllers unaffected; GC handles cross-group ownership.

### Production readiness (ROSA staging)

1. Deploy on Konflux staging cluster (ROSA)
2. Validate APIService survives cluster operator reconciliation
3. Test OpenShift Pipelines operator coexistence
4. Failover testing (kill secondary pods, RDS failover simulation)
5. Validate monitoring and alerting (Kine metrics, PostgreSQL CloudWatch)

## Deployment Strategy

- **Short term:** Konflux-specific deployment overlay. Remove Tekton CRDs from main cluster via GitOps; deploy secondary API server stack; register APIService objects.
- **Long term:** Change how OpenShift Pipelines deploys on Konflux clusters -- operator-native support for aggregated API server mode.

## Scope

**Current:** `tekton.dev` and `resolution.tekton.dev` API groups (validated with real Konflux pipelines).

**Next:** Konflux-specific CRDs (Snapshot, Release) moved to the same secondary API server.

**Future exploration:** If proven at scale, this approach could replace KubArchive for storing historical pipeline snapshots and release resources -- the PostgreSQL backend provides native query capabilities without needing a separate archival system.

## Related Work

- [KONFLUX-14236](https://redhat.atlassian.net/browse/KONFLUX-14236) -- Reduce PipelineRun etcd footprint (complementary)
- [KONFLUX-14235](https://redhat.atlassian.net/browse/KONFLUX-14235) -- MultiKueue multicluster scheduling (orthogonal)
- [kubernetes/kubernetes#118858](https://github.com/kubernetes/kubernetes/issues/118858) -- Upstream CRD sharding (this approach bypasses the need)
- [openshift/enhancements#1979](https://github.com/openshift/enhancements/pull/1979) -- Etcd sharding for built-in resources (complementary)
- [k3s-io/kine](https://github.com/k3s-io/kine) -- Etcd API to SQL translation layer
