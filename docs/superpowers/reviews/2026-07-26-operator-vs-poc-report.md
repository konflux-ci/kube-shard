# Operator vs PoC Scripts Comparison

**Date:** 2026-07-26

## Summary

The operator consolidates 17 shell scripts across 6 PoC phases into a single declarative `APIShard` CR with two sub-CRs (`NamespaceSync`, `WebhookSync`). It replaces imperative setup with reconciliation-driven convergence, adds cert auto-renewal via cert-manager, and provides dynamic namespace/webhook sync instead of manual one-shot scripts.

| Metric | PoC Scripts | Operator |
|--------|------------|----------|
| Scripts/Controllers | 17 scripts, 6 phases | 3 controllers, 1 CR |
| Cert management | openssl CLI, 365-day, no renewal | cert-manager, auto-renewal |
| Authorization | Phase 1: AlwaysAllow, Phase 2: Webhook | Webhook from day 1 |
| Storage options | Phase 1: SQLite, Phase 5: PostgreSQL | SQLite, InClusterPostgreSQL, PostgreSQL (from spec) |
| Namespace sync | Manual (MIRROR_NAMESPACES list) | Automatic (label selector + event watches) |
| Webhook sync | Manual (Phase 3 script) | Automatic (API group filtering + event watches) |
| Cleanup | delete kind cluster | Finalizer (APIServices) + ownerRef GC |
| Idempotency | Partial | Full (SSA) |

---

## Architecture

### PoC
```
User → run setup-phase*.sh in sequence →
  setup-kind.sh → generate-certs.sh → kustomize apply → install-tekton-crds.sh →
  apiservice registration → Tekton controller install
```

### Operator
```
User → kubectl apply APIShard CR →
  Reconciler: namespace → certs → storage → Kine → secondary → kubeconfig →
  APIServices → CRD conflict detection → NamespaceSync sub-CR → WebhookSync sub-CR
```

---

## Certificate Generation

| Certificate | PoC (openssl) | Operator (cert-manager) |
|-------------|---------------|------------------------|
| Self-signed CA | `openssl req -x509` → `serving-ca.key/crt` | SelfSigned Issuer → CA Certificate |
| Serving cert | `openssl x509 -req` with SANs → `serving.key/crt` | Certificate from CA Issuer with DNS SANs |
| SA signing key | `openssl genrsa` → `sa-signing.key/pub` | Reuses cert-manager TLS key |
| Front-proxy CA | `docker cp` from kind node or kube-system ConfigMap | Copy from kube-system ConfigMap |
| Auto-renewal | No | Yes (cert-manager) |

---

## Secondary API Server Flags

| Flag | PoC | Operator | Notes |
|------|-----|----------|-------|
| `--etcd-servers` | `http://kine.tekton-apiserver.svc:2379` | `http://<shard>-kine.<ns>.svc:2379` | Equivalent |
| `--secure-port` | `6443` | `6443` | Same |
| `--tls-cert-file/key` | From openssl Secret | From cert-manager Secret | Both valid |
| `--requestheader-*` | front-proxy-ca + headers | Same | Same |
| `--authorization-mode` | Phase 1: `AlwaysAllow`; Phase 2: `Webhook` | `Webhook` always | Operator more secure |
| `--authorization-webhook-version` | Not set | `v1` | Operator matches spec |
| `--token-auth-file` | `token-auth.csv` (static token) | Not used | Operator uses webhook auth |
| `--disable-admission-plugins` | `NamespaceLifecycle,ServiceAccount` | Same | Same |
| `--enable-admission-plugins` | Phase 3: `MutatingAdmissionWebhook,ValidatingAdmissionWebhook` | Same (from day 1) | Operator enables earlier |
| `--client-ca-file` | Not set | cert-manager CA | Operator extra |

---

## Phase-by-Phase Coverage

### Phase 1: Basic Stack (setup-poc.sh)

**PoC steps:**
1. Create kind cluster
2. Generate certs (openssl)
3. Deploy Kine + secondary (kustomize)
4. Wait for rollouts
5. Install Tekton CRDs on secondary
6. Delete primary CRDs, register APIServices
7. Install Tekton controller

**Operator equivalent:** APIShard reconciler steps 1-8. Fully covered except:
- Kind cluster creation (out of scope — operator runs in existing cluster)
- Tekton controller installation (out of scope — controllers are unchanged)
- CRD installation is reactive (during conflict detection) not proactive

### Phase 2: Webhook Authorization (setup-phase2.sh)

**PoC:** Applies phase2 kustomize overlay that adds `--authorization-mode=Webhook` and webhook config.

**Operator:** Built-in from day 1. The secondary always uses webhook authorization delegating to the primary.

### Phase 3: Admission Webhooks (setup-phase3.sh)

**PoC:** Copies ValidatingWebhookConfiguration and MutatingWebhookConfiguration from primary to secondary, transforms `clientConfig.service` to `clientConfig.url` via sed, disables CRD conversion webhooks.

**Operator:** WebhookSync sub-CR handles this automatically:
- Filters by API group intersection (not hardcoded names)
- Transforms service → URL in code
- Watches for webhook changes and re-syncs
- Deletes stale webhooks removed from primary

### Phase 4: Kueue (setup-phase4.sh)

**PoC:** Installs cert-manager, Kueue, tekton-kueue; creates queue resources.

**Operator:** Not covered. Kueue is an orthogonal workload management concern, not part of the sharding operator's responsibility.

### Phase 5: PostgreSQL Storage (setup-phase5.sh)

**PoC:** Applies phase5 kustomize overlay switching Kine from SQLite to PostgreSQL.

**Operator:** `spec.storage.type: InClusterPostgreSQL` or `PostgreSQL` in the APIShard CR. The operator deploys PostgreSQL Deployment + Service + credentials Secret for in-cluster mode, or validates user-provided connection Secret for external mode.

### Phase 6: Konflux Integration (setup-phase6.sh)

**PoC:** Full Konflux cluster integration: scales down Tekton Operator, extracts CRDs, registers APIServices, copies webhooks, mirrors namespaces, restarts controllers.

**Operator:** Partial coverage:
- CRD conflict detection (reports, doesn't remediate)
- APIService registration
- Namespace sync (label selector)
- Webhook sync (API group filtering)
- Does NOT: scale down upstream operators, restart controllers

---

## Namespace Sync

| Aspect | PoC | Operator |
|--------|-----|----------|
| Method | `kubectl create namespace` from `MIRROR_NAMESPACES` list | NamespaceSync CR watches primary with label selector |
| Dynamic | No (static list at setup time) | Yes (reacts to namespace create/delete events) |
| Stale cleanup | No | Yes (deletes secondary namespaces no longer matching) |
| System ns protection | Manual | Automatic (kube-system, kube-public, kube-node-lease, default) |

## Webhook Sync

| Aspect | PoC (Phase 3) | Operator |
|--------|---------------|----------|
| Discovery | Hardcoded Tekton webhook names | List all + filter by API group rules |
| Transform | sed: service ref → URL | Code: clientConfig.service → clientConfig.url |
| caBundle | Preserved | Preserved (deep copy) |
| Stale cleanup | No | Yes |
| Scope | Tekton-specific | Generic (any API group) |
| Trigger | Manual script run | Automatic (watches webhook config changes) |

---

## What the Operator Does Not Cover

1. **Kueue integration (Phase 4)** — Orthogonal concern; separate deployment
2. **Upstream operator scaling** — Design decision: detect + report, not remediate
3. **Kind cluster management** — Dev/CI concern, handled by Makefile
4. **Direct secondary access shim** — Replaced by admin kubeconfig Secret
5. **CRD conversion webhook disabling** — Not yet implemented; CRDs are copied as-is during conflict sync

---

## Key Improvements Over PoC

1. **Declarative lifecycle** — One CR replaces 17 scripts. Spec changes trigger reconciliation.
2. **Cert auto-renewal** — cert-manager handles rotation vs 365-day openssl certs with no renewal.
3. **Dynamic sync** — Namespace and webhook sync react to cluster events vs manual re-runs.
4. **Generic API groups** — Any CRD-based API group can be aggregated vs Tekton-only.
5. **Orphan cleanup** — SSA tracking client + finalizer vs no cleanup beyond cluster deletion.
6. **Secure by default** — Webhook authorization from day 1 vs AlwaysAllow in Phase 1.
7. **Storage flexibility** — SQLite/InClusterPostgreSQL/ExternalPostgreSQL from CR spec vs multi-phase script progression.
