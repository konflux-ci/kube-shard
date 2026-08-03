# Task 1 Report: BuildPDB function + unit tests

## What was implemented

Added `BuildPDB(name, namespace string, replicas int32, selector map[string]string) *policyv1.PodDisruptionBudget` in `operator/internal/resources/pdb.go`.

Behavior:
- Returns `nil` when `replicas < 2` (covers 0 and 1 replica cases)
- When `replicas >= 2`, returns a PDB with:
  - `TypeMeta`: `apiVersion: policy/v1`, `Kind: PodDisruptionBudget`
  - `ObjectMeta.Name` and `ObjectMeta.Namespace` set from arguments
  - `ObjectMeta.Labels` copied from the selector map
  - `Spec.MaxUnavailable: 1` (via `intstr.FromInt32(1)`)
  - `Spec.Selector.MatchLabels` set to the selector map

## What was tested

Five unit tests in `operator/internal/resources/pdb_test.go`, all using gomega:

1. `TestBuildPDB_ReturnsNil_WhenReplicasLessThan2` — replicas=1 → nil
2. `TestBuildPDB_ReturnsNil_WhenReplicasZero` — replicas=0 → nil
3. `TestBuildPDB_ReturnsPDB_WhenReplicasTwo` — verifies TypeMeta, name, namespace, maxUnavailable
4. `TestBuildPDB_ReturnsPDB_WhenReplicasThree` — verifies maxUnavailable=1 for 3 replicas
5. `TestBuildPDB_CorrectLabelsAndSelector` — verifies ObjectMeta labels and Spec selector match

### Test results

```
go test ./internal/resources/... -count=1 -v
```

All 37 tests in the package pass (5 new + 32 existing).

## Files changed

| File | Action |
|------|--------|
| `operator/internal/resources/pdb.go` | Created |
| `operator/internal/resources/pdb_test.go` | Created |

## Self-review findings

- **Completeness:** All spec requirements implemented. Edge cases for replicas 0 and 1 covered.
- **Quality:** Follows existing package patterns (license header, doc comment, gomega tests, intstr usage matching `secondary_test.go`).
- **Discipline:** No overbuilding — single exported function, no reconciler integration (deferred to Task 2).
- **Testing:** Each required test case present; positive-path tests verify all PDB fields specified in the brief.

No issues found during self-review.

## Issues or concerns

None. Ready for Task 2 integration into the reconciler.
