# Task 1: Create BuildPDB function + unit tests

## What to Build

A generic PDB builder function in the `resources` package, plus unit tests.

### File: `operator/internal/resources/pdb.go`

Create a new file with a single exported function:

```go
func BuildPDB(name, namespace string, replicas int32, selector map[string]string) *policyv1.PodDisruptionBudget
```

**Behavior:**
- Returns `nil` when `replicas < 2` (single-replica deployments get no PDB)
- When `replicas >= 2`, returns a PDB with:
  - `maxUnavailable: 1` (at least N-1 pods remain available during voluntary disruptions)
  - TypeMeta set: `apiVersion: policy/v1`, `Kind: PodDisruptionBudget`
  - ObjectMeta: `Name` = `name`, `Namespace` = `namespace`, `Labels` = same as `selector`
  - Spec.Selector.MatchLabels = `selector`

**Import:** `policyv1 "k8s.io/api/policy/v1"`

### File: `operator/internal/resources/pdb_test.go`

Unit tests using gomega (same pattern as existing tests in the package). Must use `g := NewGomegaWithT(t)` and `g.Expect(...)`.

**Test cases:**
1. `TestBuildPDB_ReturnsNil_WhenReplicasLessThan2` — replicas=1 returns nil
2. `TestBuildPDB_ReturnsNil_WhenReplicasZero` — replicas=0 returns nil
3. `TestBuildPDB_ReturnsPDB_WhenReplicasTwo` — replicas=2 returns non-nil PDB with correct fields
4. `TestBuildPDB_ReturnsPDB_WhenReplicasThree` — replicas=3 returns non-nil with correct maxUnavailable=1
5. `TestBuildPDB_CorrectLabelsAndSelector` — verify labels on ObjectMeta match selector, and Spec.Selector.MatchLabels match

## Conventions

- Every function must have a Go doc comment starting with the function name
- Use the Apache 2.0 license header (same as other files in the package)
- Follow the existing import style (grouped: stdlib, k8s.io, project imports)
- The `intstr` package is at `"k8s.io/apimachinery/pkg/util/intstr"` for the maxUnavailable value

## Existing Patterns

Look at `operator/internal/resources/scheduling.go` and `operator/internal/resources/kine.go` for the existing code style. Tests are in `*_test.go` in the same package. The test helper `newTestShard()` is defined in `secondary_test.go` and is available across test files in the same package.
