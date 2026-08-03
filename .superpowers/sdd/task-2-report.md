# Task 2 Report: Reconciler Integration for PDB Auto-Creation

## Status: DONE

## Summary

Integrated `resources.BuildPDB()` into the APIShard reconciler so PodDisruptionBudgets are automatically created for Kine and secondary apiserver deployments when replica count is >= 2.

## Changes Made

**File:** `operator/internal/controller/apishard/reconciler.go`

### 1. Added PDB GVK to `managedGVKs`

```go
{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
```

Added after the cert-manager entries. This enables orphan cleanup: when replicas scale from >=2 to 1, `BuildPDB` returns nil (PDB no longer applied), and `CleanupOrphans` removes the stale PDB.

### 2. Added RBAC marker

```go
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
```

Placed alongside existing RBAC markers above the `Reconcile` method.

### 3. PDB apply in `reconcileKine`

After Kine deployment apply, before service apply:

```go
if pdb := resources.BuildPDB(
    resources.KineDeploymentName(shard),
    shard.Spec.TargetNamespace,
    shard.Spec.Kine.Replicas,
    deployment.Spec.Selector.MatchLabels,
); pdb != nil {
    if err := tc.ApplyOwned(ctx, pdb); err != nil {
        return fmt.Errorf("kine pdb: %w", err)
    }
}
```

### 4. PDB apply in `reconcileSecondary`

After secondary deployment apply, before service apply:

```go
if pdb := resources.BuildPDB(
    resources.SecondaryDeploymentName(shard),
    shard.Spec.TargetNamespace,
    shard.Spec.Secondary.Replicas,
    deployment.Spec.Selector.MatchLabels,
); pdb != nil {
    if err := tc.ApplyOwned(ctx, pdb); err != nil {
        return fmt.Errorf("secondary pdb: %w", err)
    }
}
```

No new imports were needed — `BuildPDB` returns `*policyv1.PodDisruptionBudget` which satisfies `client.Object` for `tc.ApplyOwned`.

## Verification

| Check | Result |
|-------|--------|
| `go build ./...` | PASS |
| `go test ./internal/aggregation -count=1` | PASS |
| `go test ./internal/predicate -count=1` | PASS |
| `go test ./internal/resources -count=1` | PASS |
| `go test ./internal/controller/... -count=1` | FAIL (envtest etcd missing) |

Controller integration tests require kubebuilder envtest binaries (`/usr/local/kubebuilder/bin/etcd`) which are not installed in this environment. This is a pre-existing infrastructure limitation, not caused by these changes. All unit tests pass.

## Self-Review Checklist

- [x] PDB GVK added to `managedGVKs`
- [x] RBAC marker added for `poddisruptionbudgets`
- [x] PDB creation in `reconcileKine`
- [x] PDB creation in `reconcileSecondary`
- [x] Error wrapping matches existing pattern (`fmt.Errorf("component: %w", err)`)
- [x] Apply ordering: deployment → PDB → service (matches task spec)
- [x] PDB names match deployment names via `KineDeploymentName` / `SecondaryDeploymentName`
- [x] Selector labels taken from `deployment.Spec.Selector.MatchLabels`
- [x] Raw replica values passed (BuildPDB returns nil for < 2)
- [x] Code compiles
- [x] Existing unit tests pass

## Commit

```
46f816d Integrate PDB auto-creation into APIShard reconciler
```

## Concerns

None related to the implementation. Controller envtest suites could not run locally due to missing kubebuilder binaries; CI should cover these.
