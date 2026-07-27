# Plan: Migrate to Konflux-Style Status Update Architecture

## Goal

Replace the current "defensive multi-layer" approach (DeepEqual + ObservedGeneration
fast path + SSA avoidance) with the Konflux operator's simpler architecture:
**always write status at the end of reconcile, rely solely on watch-layer predicates
and controller-runtime defaults to prevent loops**.

## Changes Overview

| Component | Current (kube-kine) | Target (Konflux-style) |
|-----------|-------------------|------------------------|
| Status write | Gated by `equality.Semantic.DeepEqual` | Unconditional `Status().Update()` |
| Fast path | `ObservedGeneration == Generation` skips SSA | Remove — always run full reconcile |
| Error path | `setErrorAndRequeue` bypasses DeepEqual | Same — always writes (no change needed) |
| Predicates | Same as Konflux | Keep as-is (no change) |
| Condition helpers | Inline `meta.SetStatusCondition` | Extract to shared `condition` package |
| Sub-CR aggregation | Pull model (filtered by predicate) | Push model (remove predicate from sub-CR `Owns()`) |

## Step-by-Step

### Step 1: Remove `statusBefore` / `DeepEqual` gates from all reconcilers

**Files:**
- `operator/internal/controller/apishard/reconciler.go`
- `operator/internal/controller/namespacesync/reconciler.go`
- `operator/internal/controller/webhooksync/reconciler.go`

**What to do:**
- Remove `statusBefore := ...DeepCopy()` at the top of each `Reconcile()`
- Remove all `if !equality.Semantic.DeepEqual(statusBefore, ...) {` conditional blocks
- Replace with unconditional `r.Status().Update(ctx, &obj)` at the end of reconcile
- Remove the `"k8s.io/apimachinery/pkg/api/equality"` import if no longer used

**Example (APIShard reconciler, end of Reconcile):**

```go
// Before:
if !equality.Semantic.DeepEqual(statusBefore, &shard.Status) {
    if err := r.Status().Update(ctx, &shard); err != nil {
        return ctrl.Result{}, err
    }
}

// After:
if err := r.Status().Update(ctx, &shard); err != nil {
    return ctrl.Result{}, err
}
```

**NamespaceSync/WebhookSync nuance:** `LastSyncTime` is currently set inside the
DeepEqual block (only when status changed). After removing the gate, only set
`LastSyncTime` when actual sync work was performed (e.g., namespaces were
created/deleted), not on every no-op reconcile.

### Step 2: Remove the ObservedGeneration fast path

**File:** `operator/internal/controller/apishard/reconciler.go`

**What to do:**
- Remove the `reconcileFastPath` method entirely (~65 lines)
- Remove the fast-path gate:
  ```go
  specUnchanged := shard.Status.ObservedGeneration == shard.Generation
  alreadyReady := shard.Status.Phase == kubeshardv1alpha1.PhaseReady
  if specUnchanged && alreadyReady {
      return r.reconcileFastPath(ctx, &shard, statusBefore)
  }
  ```
- The full reconcile path already includes health checks and CRD conflict detection

**Trade-off:** Every periodic requeue will now run full SSA applies. SSA is
idempotent and the API server doesn't persist no-op patches. The
`IgnoreStatusUpdatesPredicate` on owned resources prevents no-op status events
from triggering more reconciles. The cost is slightly more API calls per cycle.

### Step 3: Change sub-CR Owns() watches to push model

**File:** `operator/internal/controller/apishard/reconciler.go` (SetupWithManager)

**What to do:**
- Remove `IgnoreStatusUpdatesPredicate` from `NamespaceSync` and `WebhookSync`
  Owns watches so sub-CR status changes trigger APIShard reconciliation
- Keep `IgnoreStatusUpdatesPredicate` on `APIService` (its status is managed by
  kube-aggregator — we don't want to react to `Available` condition churn)

```go
// Before:
Owns(&kubeshardv1alpha1.NamespaceSync{}, builder.WithPredicates(shardpredicate.IgnoreStatusUpdatesPredicate)).
Owns(&kubeshardv1alpha1.WebhookSync{}, builder.WithPredicates(shardpredicate.IgnoreStatusUpdatesPredicate)).

// After:
Owns(&kubeshardv1alpha1.NamespaceSync{}).
Owns(&kubeshardv1alpha1.WebhookSync{}).
```

**Why:** APIShard reacts immediately to sub-CR readiness changes instead of
waiting for the next periodic requeue. Since APIShard unconditionally writes
status, and its own `For()` default predicate ignores status-only updates
(generation unchanged), this won't cause a loop.

### Step 4: Create a `condition` helper package (optional)

**New file:** `operator/internal/condition/condition.go`

Extract helpers:
- `SetCondition(obj, conditions, condition)` — auto-sets `ObservedGeneration`
- `ReconcileErrorHandler` — consistent error-path status updates

```go
package condition

import (
    apimeta "k8s.io/apimachinery/pkg/api/meta"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

func SetCondition(obj client.Object, conditions *[]metav1.Condition, condition metav1.Condition) {
    condition.ObservedGeneration = obj.GetGeneration()
    apimeta.SetStatusCondition(conditions, condition)
}
```

### Step 5: Keep `ObservedGeneration` field, simplify role

- Keep setting `shard.Status.ObservedGeneration = shard.Generation` (informational)
- Remove its use as control-flow decision (fast path is gone)

### Step 6: Keep predicates as-is

The predicate package is already aligned with Konflux. No changes needed except
what's described in step 3.

### Step 7: Keep `setErrorAndRequeue` as-is

Already unconditionally writes status. Optionally refactor to use the new
`condition.ReconcileErrorHandler` from step 4.

### Step 8: Replace `RequeueAfter` with explicit periodic event source

**Goal:** Make periodic reconciliation explicit and distinguishable from
watch-triggered reconciles. Instead of `RequeueAfter` (which is indistinguishable
from a watch-triggered reconcile in logs/metrics), use a `source.Channel` fed by
a ticker goroutine.

**Why this matters:**
- `RequeueAfter` reconciles look identical to event-driven reconciles in logs
- You can't tell from metrics whether load is from watches or timers
- A channel source makes the trigger reason explicit
- Easier to tune intervals per-controller without touching reconciler logic

**Pattern:**

```go
package periodic

import (
    "context"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/event"
    "sigs.k8s.io/controller-runtime/pkg/handler"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"
    "sigs.k8s.io/controller-runtime/pkg/source"
)

// NewTickerSource returns a source.Source that enqueues all instances of a CR
// at the given interval. Register it with WatchesRawSource in SetupWithManager.
// The returned manager.Runnable must be added to the manager.
func NewTickerSource(
    interval time.Duration,
    listFunc func(ctx context.Context) ([]reconcile.Request, error),
) (source.Source, manager.Runnable) {
    ch := make(chan event.GenericEvent, 1)

    runnable := manager.RunnableFunc(func(ctx context.Context) error {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return nil
            case <-ticker.C:
                // Send a sentinel event; the mapper will list actual CRs
                ch <- event.GenericEvent{
                    Object: &metav1.PartialObjectMetadata{
                        ObjectMeta: metav1.ObjectMeta{
                            Name: "periodic-sync",
                        },
                    },
                }
            }
        }
    })

    src := source.Channel(ch, handler.EnqueueRequestsFromMapFunc(
        func(ctx context.Context, _ client.Object) []reconcile.Request {
            requests, _ := listFunc(ctx)
            return requests
        },
    ))

    return src, runnable
}
```

**Usage in NamespaceSync:**

```go
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
    periodicSrc, ticker := periodic.NewTickerSource(5*time.Minute,
        func(ctx context.Context) ([]reconcile.Request, error) {
            var list kubeshardv1alpha1.NamespaceSyncList
            if err := r.List(ctx, &list); err != nil {
                return nil, err
            }
            requests := make([]reconcile.Request, len(list.Items))
            for i := range list.Items {
                requests[i] = reconcile.Request{
                    NamespacedName: types.NamespacedName{
                        Name:      list.Items[i].Name,
                        Namespace: list.Items[i].Namespace,
                    },
                }
            }
            return requests, nil
        },
    )
    mgr.Add(ticker)

    return ctrl.NewControllerManagedBy(mgr).
        For(&kubeshardv1alpha1.NamespaceSync{}).
        WatchesRawSource(periodicSrc).
        Watches(&corev1.Namespace{}, &namespaceEventHandler{client: r.Client}).
        Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
            secretToNamespaceSyncMapper(mgr.GetClient()),
        )).
        Named("namespacesync").
        Complete(r)
}
```

**Changes per controller:**

| Controller | Current | Target |
|------------|---------|--------|
| APIShard | `RequeueAfter: 30s` | **No periodic** — purely event-driven (watches + sub-CR push) |
| NamespaceSync | `RequeueAfter: 60s` | Channel ticker at 5min (secondary drift correction) |
| WebhookSync | `RequeueAfter: 60s` | Channel ticker at 5min (secondary drift correction) |

**APIShard needs no periodic trigger** because:
- Deployment health → `DeploymentReadinessPredicate` (watch)
- CRD conflicts → `crdEventHandler` (watch)
- Sub-CR status → `Owns()` push model (step 3)

**NamespaceSync/WebhookSync still need periodic** for secondary drift correction
(no watch on the secondary cluster), but the interval can be much longer (5min)
since primary-side changes are already event-driven.

**Reconciler return value change:**

```go
// Before (all reconcilers):
return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

// After (APIShard):
return ctrl.Result{}, nil

// After (NamespaceSync/WebhookSync):
return ctrl.Result{}, nil  // periodic trigger handled by channel source
```

On error paths, continue returning `ctrl.Result{}, err` — controller-runtime's
exponential backoff handles retries automatically.

---

## Risk Assessment

| Risk | Mitigation |
|------|-----------|
| Increased API calls (no fast path) | SSA no-ops are cheap; API server doesn't persist if unchanged |
| Self-loop from unconditional status write | controller-runtime's default `For()` predicate filters status-only updates (generation unchanged) |
| Sub-CR status → APIShard → APIShard status → re-trigger | Won't happen: `For()` default predicate ignores status-only changes |
| `LastSyncTime` on every reconcile | Only set when actual work performed |
| APIShard never reconciles if no events | Sub-CR push + deployment watches + CRD watches cover all triggers; no steady-state work needed |
| Channel goroutine leak | Registered as `manager.Runnable` — cancelled when manager stops |
| Missed periodic tick (channel buffer full) | Buffer of 1 is fine — a missed tick just means a slightly later sync; next tick will fire |

## Validation

1. **Unit tests:** Reconcile completes without error when status is unchanged
2. **E2E test:** Deploy APIShard, wait for Ready, verify reconcile count stabilizes
3. **Metric check:** `controller_runtime_reconcile_total` shows periodic rate (~2/min), not unbounded growth

## Commit Order

1. Step 4 (condition package) — no-behavior-change refactor
2. Steps 1 + 2 + 5 — main behavioral change (one commit)
3. Step 3 — push model for sub-CRs (separate commit, easy to revert)
4. Step 8 — replace RequeueAfter with channel ticker (separate commit)
5. Step 7 — optional cleanup
