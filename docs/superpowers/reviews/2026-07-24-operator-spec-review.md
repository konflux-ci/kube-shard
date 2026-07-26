# Operator Implementation vs Spec Review

**Date:** 2026-07-24
**Spec:** [2026-07-23-kube-shard-operator-design.md](../specs/2026-07-23-kube-shard-operator-design.md)
**Plan:** [2026-07-23-kube-shard-operator.md](../plans/2026-07-23-kube-shard-operator.md)

## Summary

| Metric | Count |
|--------|-------|
| Plan tasks | 10 |
| Implemented | 8 |
| Gaps / Deviations | 9 |
| Dead code files | 2 |

---

## Plan Task Progress

| Task | Description | Status |
|------|-------------|--------|
| 1 | Scaffold + APIShard CRD types | Done |
| 2 | APIShardReconciler — deploy Kine + secondary | Done |
| 3 | Secondary client + health checking | Done |
| 4 | CRD aggregation + conflict detection | Done |
| 5 | NamespaceSync CRD + reconciler | Deviated |
| 6 | WebhookSync CRD + reconciler | Deviated |
| 7 | Storage backends (SQLite/PG/External PG) | Done |
| 8 | cert-manager integration | Done |
| 9 | Sample CRs + Makefile targets | Partial |
| 10 | E2E test suite | Partial |

---

## Dead Code

Two old controller files exist at the `controller` package level but are never imported by `cmd/main.go`. The active controllers live in subdirectories.

| File | Replaced By |
|------|-------------|
| `internal/controller/apishard_controller.go` | `internal/controller/apishard/reconciler.go` |
| `internal/controller/namespacesync_controller.go` | `internal/controller/namespacesync/reconciler.go` |

These should be deleted.

---

## Project Structure

The implementation is more modular than the plan specified. Resource builders, cert-manager helpers, and aggregation logic are extracted into separate packages.

| Spec/Plan Path | Actual Path | Status |
|----------------|-------------|--------|
| `internal/controller/apishard_controller.go` | `internal/controller/apishard/reconciler.go` | Renamed |
| `internal/controller/namespacesync_controller.go` | `internal/controller/namespacesync/reconciler.go` | Renamed |
| `internal/controller/webhooksync_controller.go` | `internal/controller/webhooksync/reconciler.go` | Renamed |
| `internal/secondary/client.go` | `internal/secondary/client.go` | Matches |
| (not in plan) | `internal/resources/kine.go, secondary.go, postgresql.go` | Extra |
| (not in plan) | `internal/aggregation/apiservice.go, conflict.go` | Extra |
| (not in plan) | `internal/certs/certmanager.go` | Extra |

---

## API Types

### APIShard Status

| Spec Field | Implemented | Notes |
|------------|-------------|-------|
| `phase`: Provisioning\|Blocked\|Ready\|Degraded | Partial | Missing: `Blocked`. Added: `Error`, `Waiting` |
| `connectionSecret` | Partial | Field exists in types but never populated by reconciler |
| `secondaryEndpoint` | Matches | |
| `registeredAPIServices` | Extra | Tracks managed APIService names for orphan cleanup |
| `message` | Extra | Error message field (not in spec) |
| `conditions` (6 types) | Matches | All 6 condition constants defined |
| `observedGeneration` | Matches | |

### NamespaceSync

| Spec Design | Implementation | Notes |
|-------------|----------------|-------|
| Namespace-scoped | **Cluster-scoped** | Architectural change |
| `secondaryConnection` (serviceRef + authSecretRef + caSecretRef) | **`shardRef` (string)** | Violates top-down config flow: reconciler reads parent APIShard |
| `labelSelector` | `labelSelector` | Matches |
| (not in spec) | `excludeNamespaces` | Extra feature for filtering out system namespaces |
| `syncedNamespaces: int32`, `lastSyncTime` | `syncedNamespaces: []SyncedNamespace`, `syncedCount`, `phase` | Richer status with per-item timestamps |

### WebhookSync

| Spec Design | Implementation | Notes |
|-------------|----------------|-------|
| Namespace-scoped | **Cluster-scoped** | Same scope change as NamespaceSync |
| `secondaryConnection` (serviceRef + authSecretRef + caSecretRef) | **`shardRef` (string)** | Same top-down violation |
| `apiGroups`: filter webhooks by API group rules | **`sourceLabelSelector` + `sourceNames`** | Completely different filtering. Spec: semantic filter by whether webhook rules target aggregated groups. Impl: label selectors or explicit names |
| (not in spec) | `syncMutating` / `syncValidating` booleans | Extra: toggle mutating/validating independently |
| `syncedWebhooks`: `{validating: int, mutating: int}` | `syncedWebhooks: []SyncedWebhook` | List of individual entries instead of aggregate counts |

---

## Reconciler Lifecycle

### APIShardReconciler (11 steps from spec)

| Step | Spec Description | Status | Notes |
|------|-----------------|--------|-------|
| 1 | Ensure target namespace | Matches | Correctly avoids owner references to prevent cascade delete |
| 2 | Ensure cert-manager Certificates | Matches | Self-signed Issuer → CA Cert → CA Issuer → Serving Cert chain |
| 3 | Ensure storage backend | Matches | All 3 types: SQLite, InClusterPostgreSQL, external PostgreSQL |
| 4 | Ensure Kine Deployment + Service | Matches | |
| 5 | Ensure secondary kube-apiserver | Matches | Full flag set with cert mounts |
| 6 | Ensure admin kubeconfig Secret | **Missing** | Field exists in status but Secret is never created |
| 7 | Install CRDs on secondary | Partial | Only triggered during CRD conflict detection, not a standalone step |
| 8 | Register APIServices + CRD conflict detection | Matches | Includes orphan cleanup via status-tracked names |
| 9 | Create NamespaceSync sub-CR | **Missing** | APIShard does NOT create sub-CRs; must be created manually |
| 10 | Create WebhookSync sub-CR | **Missing** | Same gap — no sub-CR creation |
| 11 | Aggregate sub-CR statuses | **Missing** | APIShard status does not reflect NamespaceSync/WebhookSync health |

---

## Secondary API Server Flags

| Flag | Spec Value | Implementation Value | Status |
|------|-----------|---------------------|--------|
| `--etcd-servers` | `http://<shard>-kine.<ns>.svc:2379` | `http://<shard>-kine.<ns>.svc:2379` | Matches |
| `--secure-port` | `6443` | `6443` | Matches |
| `--tls-cert-file/key` | cert-manager Secret | cert-manager Secret | Matches |
| `--requestheader-*` | front-proxy CA + headers | front-proxy CA + headers | Matches |
| `--authorization-mode` | `Webhook` | `RBAC,Webhook` | Partial |
| `--authorization-webhook-version` | `v1` | (not set) | Missing |
| `--enable-admission-plugins` | `MutatingAdmissionWebhook,ValidatingAdmissionWebhook` | (not set) | Missing |
| `--client-ca-file` | (not in spec) | set | Extra |
| `--authentication-token-webhook-config-file` | (not in spec) | set | Extra |

---

## Naming Conventions

| Resource | Spec Pattern | Actual Pattern | Status |
|----------|-------------|---------------|--------|
| Kine Deployment | `<shard>-kine` | `<shard>-kine` | Matches |
| Kine Service | `<shard>-kine` | `<shard>-kine` | Matches |
| Secondary Deployment | `<shard>-apiserver` | `<shard>-apiserver` | Matches |
| Secondary Service | `<shard>-apiserver` | `<shard>-apiserver` | Matches |
| PostgreSQL Deployment | `<shard>-postgresql` | `<shard>-postgresql` | Matches |
| Serving Certificate | `<shard>-serving-cert` | `<shard>-serving` | Partial |
| CA Certificate | `<shard>-front-proxy-ca` | `<shard>-ca` | Partial |
| PKI Secret | (from cert-manager) | `<shard>-pki` | Extra |
| Admin kubeconfig | `<shard>-admin-kubeconfig` | (not created) | Missing |
| Auth ConfigMap | `<shard>-authz-config` | `<shard>-auth-config` | Partial |
| SA signing Secret | `<shard>-sa-signing` | (not created) | Missing |
| Requestheader CA | (not in spec) | `<shard>-requestheader-ca` | Extra |

---

## Findings

### Critical: Sub-CR lifecycle not implemented (steps 9-11)

The APIShard reconciler does not create NamespaceSync or WebhookSync sub-CRs, and does not aggregate their status. This is the core "top-down config flow" pattern from the spec. Currently, sub-CRs must be created manually and the APIShard status does not reflect their health.

### Critical: Sub-CR reconcilers violate top-down flow

The spec designed `secondaryConnection` as a self-contained connection spec on each sub-CR so reconcilers never reach back to the parent. The implementation uses `shardRef` (a string name) to look up the APIShard, creating a reverse dependency that makes sub-CRs non-independently testable.

### Important: Admin kubeconfig Secret not created (step 6)

The spec says the reconciler should create an admin kubeconfig Secret (`<shard>-admin-kubeconfig`) and surface it in `status.connectionSecret`. The `connectionSecret` field exists in the types but is never populated.

### Important: WebhookSync filtering mechanism differs

The spec filters webhooks by checking if their rules target the aggregated API groups (semantic filtering). The implementation uses label selectors and explicit names (manual filtering). This means the operator cannot automatically discover webhooks for aggregated API groups.

### Important: Condition constants defined but not used

The `conditions.go` file defines constants like `ConditionCRDConflictDetected`, but the APIShard reconciler uses string literals like `"CRDConflict"` and `"Reconciled"`. This means the condition type names don't match the spec — the spec says `CRDConflictDetected` but the reconciler sets `CRDConflict`.

### Important: No APIShard deletion handler

The reconciler returns early on `DeletionTimestamp` without any cleanup. There is no finalizer to delete APIService objects from the primary. Namespace and workloads may be cleaned up by owner references, but APIServices are cluster-scoped and created without owner refs — they will be orphaned.

### Important: WebhookSync does not delete stale webhooks

Unlike NamespaceSync (which deletes previously-synced namespaces no longer in the desired set), the WebhookSync reconciler only creates and updates webhooks on the secondary. Webhooks removed from the primary are never cleaned up on the secondary.

### Important: Sub-CR reconcilers use insecure TLS

NamespaceSync and WebhookSync call `getSecondaryClient` with only the `Host` field set — no CA cert, no token. This means TLS verification is skipped (`Insecure: true`) and no authentication is performed. The APIShard CRD sync path separately reads the SA token, but sub-CRs don't.

### Minor: Phase constants differ from spec

Spec: `Provisioning | Blocked | Ready | Degraded`. Implementation: `Provisioning | Ready | Degraded | Error | Waiting`. The `Blocked` phase (for CRD conflicts) is not implemented. CRD conflicts set a condition but don't change the phase. `Error` and `Waiting` are additions not in the spec.

### Minor: Unit tests are thin for sub-CRs

The NamespaceSync and WebhookSync unit tests (~65 lines each) only verify that a missing APIShard sets `Phase=Waiting`. There are no envtest tests for the actual sync logic (creating/deleting namespaces, transforming webhooks). The CRD sharding e2e test covers some of this end-to-end.

### Positive: Extra features beyond spec

The implementation includes several improvements not in the spec: the `tracking.Client` pattern for SSA + orphan cleanup from the Konflux operator pkg, automatic `requestheader-client-ca-file` copying from `kube-system`, modular package structure (`resources/`, `aggregation/`, `certs/`), and CRD-to-secondary syncing during conflict detection.
