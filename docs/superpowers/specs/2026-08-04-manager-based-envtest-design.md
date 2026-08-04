# Manager-Based envtest Suite for APIShard Reconciler

## Problem

The current APIShard envtest suite (44 tests in `reconciler_test.go`) calls reconciler
methods directly — it never starts a controller manager. This means watches, predicates,
event handlers, and the full reconcile-via-queue pipeline configured in
`SetupWithManager` are completely untested. Bugs in mapper functions, predicate filters,
or event handler wiring can ship undetected.

## Goal

Run the actual controller manager in envtest so that:

- Watches trigger reconciliation when dependent objects (ConfigMaps, Secrets, CRDs) change.
- Predicates filter events correctly (e.g., `DeploymentReadinessPredicate`).
- Custom event handlers (`crdEventHandler`, `requestHeaderCAMapper`, `connectionSecretMapper`) are exercised end-to-end.
- The full `Reconcile` loop runs asynchronously through the work queue, matching production behavior.

## Scope

- **APIShard controller only.** NamespaceSync and WebhookSync can follow in a later PR.
- **Additive.** Existing direct-call tests in `reconciler_test.go` are left untouched. New manager-based tests go in new files. A follow-up PR removes the old tests once coverage is confirmed.

## Design

### 1. Interface Extraction

Two interfaces decouple the reconciler from external dependencies that don't exist in envtest.

#### PKIReconciler

Abstracts cert-manager interaction. Lives in the `apishard` package.

```go
type PKIReconciler interface {
    ReconcilePKI(ctx context.Context, tc *tracking.Client, shard *kubeshardv1alpha1.APIShard) error
}
```

**Production implementation** (`certManagerPKI`): wraps the existing `reconcileCertManager`
body — creates cert-manager Issuer and Certificate unstructured objects via the tracking
client.

**Test stub** (`stubPKI`): generates a real self-signed CA + serving cert + admin client
cert using Go's `crypto/x509` stdlib and creates two Secrets
(`<shard>-pki`, `<shard>-admin-client-cert`) directly. All downstream code
(`reconcileAdminKubeconfig`, `reconcileAPIServices`, `syncCRDsToSecondary`,
`verifySecondaryAuth`) finds real Secrets with real cert data — no further mocking needed.

#### SecondaryClientFactory (secondary.ClientFactory)

Abstracts the `ClientProvider` for secondary API server communication. Lives in the
`secondary` package alongside the existing `ClientProvider`.

```go
type ClientFactory interface {
    GetOrCreate(shardName string, cfg ClientConfig) (client.Client, error)
    Invalidate(shardName string)
}
```

`*ClientProvider` already implements both methods — no changes to the struct, just add the
interface and update the `Reconciler.ClientProvider` field type.

**Test stub** (`fakeClientFactory`): returns a pre-built client connected to the second
envtest instance, ignoring credentials. The reconciler's credential-reading code still
executes; only the actual client creation is bypassed.

#### Updated Reconciler Struct

```go
type Reconciler struct {
    client.Client
    Scheme         *runtime.Scheme
    ClientProvider secondary.ClientFactory  // was *secondary.ClientProvider
    PKI            PKIReconciler            // new field
}
```

The `reconcileCertManager` call in `Reconcile()` becomes `r.PKI.ReconcilePKI(...)`.

#### Production Wiring (cmd/main.go)

```go
if err = (&apishard.Reconciler{
    Client:         mgr.GetClient(),
    Scheme:         mgr.GetScheme(),
    ClientProvider: clientProvider,
    PKI:            apishard.NewCertManagerPKI(mgr.GetClient()),
}).SetupWithManager(mgr); err != nil { ... }
```

### 2. Test Suite Setup (suite_test.go)

```
BeforeSuite:
  1. Register schemes (kube-shard CRDs, apiextensionsv1, apiregistrationv1, policyv1)
  2. Start primary envtest (with kube-shard CRDs from config/crd/bases)
  3. Start secondary envtest (no CRDs — they get synced by the reconciler)
  4. Create controller manager against primary envtest
  5. Wire Reconciler with stubPKI + fakeClientFactory(secondaryClient)
  6. Call SetupWithManager (registers watches, predicates, handlers)
  7. Start manager in background goroutine

AfterSuite:
  1. Cancel context (stops manager)
  2. Stop both envtest instances
```

Shared globals: `ctx`, `cancel`, `testEnv`, `cfg`, `k8sClient`, `secondaryEnv`,
`secondaryCfg`, `secondaryClient`.

The old direct-call tests continue to work because `k8sClient` and `cfg` remain
available. Old tests construct their own `Reconciler{}` without `PKI` or
`ClientProvider`, which is unchanged from their current behavior.

### 3. Test Stubs (helpers_test.go)

#### stubPKI

Generates real self-signed certificates using `crypto/x509` + `crypto/ecdsa` +
`encoding/pem`. Creates two Secrets per reconcile:

| Secret | Keys | Purpose |
|--------|------|---------|
| `<shard>-pki` | `tls.crt`, `tls.key`, `ca.crt` | Serving cert for secondary API server |
| `<shard>-admin-client-cert` | `tls.crt`, `tls.key`, `ca.crt` | Admin client cert for operator→secondary auth |

Uses `CreateOrUpdate` for idempotency across reconcile loops.

#### fakeClientFactory

```go
type fakeClientFactory struct {
    client client.Client
}

func (f *fakeClientFactory) GetOrCreate(_ string, _ secondary.ClientConfig) (client.Client, error) {
    return f.client, nil
}

func (f *fakeClientFactory) Invalidate(_ string) {}
```

#### Shared helpers

- `createAPIShard(name, storageType)` — creates an APIShard with sensible defaults, unique target namespace.
- `deleteAPIShard(shard)` — deletes and waits for NotFound via Eventually.
- `updateAuthConfigMap(data)` — upserts `kube-system/extension-apiserver-authentication`.

### 4. Test File Organization

| File | Coverage |
|------|----------|
| `suite_test.go` | Two envtest instances, manager lifecycle, scheme registration |
| `helpers_test.go` | stubPKI, fakeClientFactory, cert generation, shared builders |
| `reconciler_lifecycle_test.go` | Creation (finalizer, namespace, all resources provisioned), deletion (finalizer removed, APIServices cleaned), not-found |
| `reconciler_watches_test.go` | ConfigMap change triggers reconcile (requestHeaderCAMapper), Secret change triggers reconcile (connectionSecretMapper), CRD create/delete triggers reconcile (crdEventHandler), Deployment readiness change triggers reconcile (predicate) |
| `reconciler_resources_test.go` | Kine Deployment/Service, Secondary Deployment/Service, auth config/delegator, admin kubeconfig, PDB auto-creation, PostgreSQL StatefulSet/Service/Secret, APIService registration |
| `reconciler_status_test.go` | Phase transitions (Provisioning→Ready, →Error, →Blocked), conditions, CRD conflict detection with ForceAggregation |
| `reconciler_test.go` | **Unchanged** — existing 44 direct-call tests (removed in follow-up PR) |

### 5. Assertion Pattern

All assertions use `Eventually` with the `g Gomega` pattern:

```go
Eventually(func(g Gomega) {
    obj := &appsv1.Deployment{}
    g.Expect(k8sClient.Get(ctx, types.NamespacedName{
        Name:      resources.KineDeploymentName(shard),
        Namespace: shard.Spec.TargetNamespace,
    }, obj)).To(Succeed())
    g.Expect(obj.Spec.Replicas).To(HaveValue(BeEquivalentTo(1)))
}, timeout, interval).Should(Succeed())
```

Constants: `timeout = 30s`, `interval = 250ms`.

### 6. Watch/Predicate Test Pattern

1. Create an APIShard and wait for initial reconcile to complete (status becomes non-empty).
2. Record a baseline (e.g., a resource's content or the shard's status).
3. Mutate the watched object (ConfigMap, Secret, CRD, or Deployment status).
4. Assert the reconciler reacted — verify the downstream effect changed (e.g., a copied ConfigMap updated, a condition flipped, CRDs appeared on secondary).

### 7. Cleanup

Every test uses `DeferCleanup` with a helper that deletes the APIShard and polls until
it's gone. Each test uses a unique APIShard name (e.g., `"watch-cm-rh"`,
`"lifecycle-del"`) so tests are isolated by resource, not by manager lifecycle.

### 8. Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Manager-based tests are slower (async reconcile + Eventually polling) | 30s timeout with 250ms interval; tests are inherently I/O-bound on envtest |
| Second envtest doubles memory usage | envtest API servers are lightweight; acceptable for CI |
| stubPKI cert generation adds complexity | Use minimal self-signed certs; helper functions are reusable |
| Old and new tests run together temporarily | Old tests construct their own Reconciler — no conflict with manager |

### 9. Files Changed (Production Code)

| File | Change |
|------|--------|
| `operator/internal/controller/apishard/reconciler.go` | Add `PKIReconciler` interface, `certManagerPKI` type, update `Reconciler` struct field types, replace `reconcileCertManager` call with `r.PKI.ReconcilePKI` |
| `operator/internal/secondary/client.go` | Add `ClientFactory` interface |
| `operator/cmd/main.go` | Wire `PKI: apishard.NewCertManagerPKI(...)` in reconciler construction |
