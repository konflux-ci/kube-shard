# Task 2: Reconciler Integration — RBAC, managedGVKs, PDB in reconcileKine/reconcileSecondary

## What to Build

Integrate the `BuildPDB` function (from Task 1) into the APIShard reconciler so PDBs are automatically created alongside Kine and secondary apiserver deployments.

### Changes to: `operator/internal/controller/apishard/reconciler.go`

#### 1. Add PDB GVK to `managedGVKs` (around line 68-77)

Add this entry to the `managedGVKs` slice:

```go
{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
```

This ensures orphan cleanup removes the PDB when replicas are scaled from >=2 to 1 (the PDB stops being applied, and `CleanupOrphans` deletes it automatically).

#### 2. Add RBAC marker (around line 86-99)

Add this kubebuilder RBAC marker alongside the existing ones:

```go
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
```

#### 3. Add import for `policyv1` (only if needed for type references)

The `resources.BuildPDB` function returns `*policyv1.PodDisruptionBudget`, but since the reconciler only passes it to `tc.ApplyOwned(ctx, pdb)` which accepts `client.Object`, no new import is needed. The `resources` package is already imported.

#### 4. Integrate PDB in `reconcileKine` (around line 298-320)

After the Kine deployment apply and before the service apply, add:

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

Note: Use `deployment.Spec.Selector.MatchLabels` from the deployment built on the line above, or build the labels map inline — either is fine. The PDB name must match the Kine Deployment name (`{shard.Name}-kine`).

Also note: If `shard.Spec.Kine.Replicas` is 0, it defaults to 1 inside `BuildKineDeployment`, so use the same defaulting logic here. Check: `replicas := shard.Spec.Kine.Replicas; if replicas == 0 { replicas = 1 }` — or just pass the raw value since `BuildPDB` returns nil for anything < 2, and 0 < 2. So passing the raw `shard.Spec.Kine.Replicas` directly is fine.

#### 5. Integrate PDB in `reconcileSecondary` (around line 335-347)

Same pattern, after the secondary deployment apply:

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

The PDB name must match the secondary Deployment name (`{shard.Name}-apiserver`).

## Testing

Run the existing tests to verify nothing breaks:

```bash
cd operator && go test ./internal/... -count=1
```

Also verify the RBAC markers are correct by checking that the file compiles.

No new integration test is needed for this task — the `BuildPDB` function is already unit tested in Task 1, and the reconciler integration is a straightforward apply-if-non-nil pattern matching existing code (like how Deployments and Services are already applied).

## Conventions

- Every function must have a Go doc comment starting with the function name
- Follow existing error wrapping pattern: `fmt.Errorf("component: %w", err)`
- RBAC markers go with the other markers above the `Reconcile` method
- The `managedGVKs` slice is alphabetically organized by Group
