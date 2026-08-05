# Load Test: etcd vs kube-shard — August 5, 2026

## Test Parameters

| Parameter | Value |
|-----------|-------|
| PipelineRuns | 500 |
| Tasks per PipelineRun | 17 (based on Konflux reverse-proxy pipeline) |
| Parallelism (creation) | 10 concurrent kubectl creates |
| Namespace | default |
| Image | registry.access.redhat.com/hi/core-runtime:2.43 |
| Image pull policy | IfNotPresent |

### Tekton Pipeline Controller Configuration

| Parameter | Value |
|-----------|-------|
| Controller replicas | 4 (StatefulSet with ordinals) |
| Buckets (work sharding) | 4 |
| Threads per controller | 8 (32 total workers across 4 replicas) |
| Pruner | Enabled — keep=10, resources: pipelinerun + taskrun, schedule: `*/5 * * * *` |

### Cluster

- OpenShift **4.21.18** on AWS, 12 worker nodes (16 vCPU, ~60 GB RAM each)
- etcd limit: 8 GB (default)
- Kubernetes events TTL: 1 hour (default)

### Component Versions

| Component | Image |
|-----------|-------|
| Secondary API server | `registry.k8s.io/kube-apiserver:v1.36.2` |
| Kine | `ghcr.io/k3s-io/kine:v0.16.3` |
| PostgreSQL | `registry.access.redhat.com/hi/postgresql:18.4` |

---

## Run 1: etcd (No kube-shard)

All Tekton resources stored directly in the primary cluster's etcd.

| Metric | Value |
|--------|-------|
| **Baseline etcd size** | 1,748 MB |
| **PRs created** | 500 |
| **Creation rate** | 4.4 PRs/s |
| **Creation time** | ~2 min |
| **Test stopped at** | ~15 min (approaching etcd limit) |
| **PRs completed (at stop)** | 225 |
| **PRs still running (at stop)** | 275 |
| **PRs failed** | 0 |
| **Peak TaskRuns** | ~9,096 |
| **Peak etcd size** | 7,336 MB (91% of 8 GB limit) |
| **etcd growth** | +5,588 MB (+320%) |

### etcd Growth Timeline

| Time | PRs (run/ok/fail) | TaskRuns | etcd Size | Delta from baseline |
|------|-------------------|----------|-----------|-------------------|
| 13:34 (start) | 500/0/0 | 1,003 | 2,636 MB | +888 MB |
| 13:35 | 500/0/0 | 1,574 | 3,374 MB | +1,626 MB |
| 13:36 | 500/0/0 | 2,190 | 4,055 MB | +2,307 MB |
| 13:37 | 500/0/0 | 3,137 | 4,654 MB | +2,906 MB |
| 13:38 | 497/3/0 | 3,996 | 5,274 MB | +3,526 MB |
| 13:39 | 490/10/0 | 4,786 | 5,973 MB | +4,225 MB |
| 13:40 | 468/32/0 | 5,177 | 6,119 MB | +4,371 MB |
| 13:42 | 439/61/0 | 5,759 | 6,119 MB | +4,371 MB |
| 13:43 | 421/79/0 | 6,301 | 6,336 MB | +4,588 MB |
| 13:49 (stop) | 275/225/0 | 9,096 | 7,336 MB | +5,588 MB |

**Observation:** etcd reached 91% of its 8 GB limit with only 225 of 500 PRs completed. The test was stopped to protect cluster stability. At the observed growth rate, the 8 GB limit would have been breached before all 500 PRs completed.

---

## Run 2: kube-shard (PostgreSQL)

All Tekton resources offloaded to the secondary API server backed by Kine + PostgreSQL.

### Configuration

| Component | Replicas | Memory |
|-----------|----------|--------|
| Secondary KAS | 3 | 48 GB |
| Kine | 3 | 16 GB |
| PostgreSQL | 1 | 4 GB |

| Metric | Value |
|--------|-------|
| **Baseline etcd size** | 1,305 MB |
| **Baseline DB size** | 0 (fresh PostgreSQL) |
| **PRs created** | 500 |
| **Creation rate** | 1.4 PRs/s (cold start); 2.2 PRs/s on a warm system |
| **Creation time** | ~6 min (cold); ~4 min (warm) |
| **All PRs completed at** | ~31 min wall-clock |
| **PRs succeeded** | 500 (100%) |
| **PRs failed** | 0 |
| **Peak TaskRuns** | ~7,707 |
| **Peak DB size** | 11 GB |
| **Peak KAS memory** | ~28 GB |
| **etcd growth (delta)** | +1,885 MB |
| **Peak etcd size** | 3,190 MB (40% of 8 GB limit) |

### kube-shard Growth Timeline

| Time | PRs (run/ok/fail) | TaskRuns | DB Size | etcd Size | etcd Delta | KAS Memory |
|------|-------------------|----------|---------|-----------|------------|------------|
| 14:35 (start) | 500/0/0 | 1,381 | 2.0 GB | 1,305 MB | baseline | 5,036 Mi |
| 14:37 | 495/5/0 | 1,986 | 2.7 GB | 1,497 MB | +192 MB | 6,989 Mi |
| 14:39 | 482/18/0 | 2,389 | 3.4 GB | 1,797 MB | +492 MB | 8,110 Mi |
| 14:42 | 449/51/0 | 2,911 | 4.2 GB | 1,858 MB | +553 MB | 10,517 Mi |
| 14:46 | 420/80/0 | 3,969 | 5.3 GB | 2,183 MB | +878 MB | 13,339 Mi |
| 14:50 | 358/142/0 | 4,918 | 6.5 GB | 2,607 MB | +1,302 MB | 17,128 Mi |
| 14:54 | 276/177/0 | 5,749 | 8.4 GB | 2,978 MB | +1,673 MB | 20,105 Mi |
| 14:59 | 185/232/0 | 7,670 | 8.7 GB | 3,036 MB | +1,731 MB | 23,629 Mi |
| 15:03 | 71/263/0 | 5,794 | 11 GB | 3,190 MB | +1,885 MB | 26,190 Mi |
| 15:05 (done) | 1/278/0 | 5,700 | 11 GB | 3,190 MB | +1,885 MB | 28,404 Mi |

---

## Comparison

> **Note on baselines:** The etcd test started with a 1,748 MB etcd baseline (including pre-existing cluster data and Tekton CRDs served locally). The kube-shard test started with a 1,305 MB etcd baseline (Tekton CRDs removed from etcd, served by secondary). All storage growth figures use **delta from baseline** for fair comparison.

| Metric | etcd | kube-shard | Difference |
|--------|------|------------|------------|
| **Storage backend** | etcd (8 GB limit) | PostgreSQL (100 GB PVC) | 12.5x capacity |
| **Baseline etcd** | 1,748 MB | 1,305 MB | -443 MB (Tekton CRDs not in etcd) |
| **PR creation rate** | 4.5/s | 1.4/s (cold) / 2.2/s (warm) | etcd 2–3x faster |
| **All 500 PRs completed** | No (stopped at 225) | Yes (500/500) | kube-shard only |
| **Success rate** | 100% (of completed) | 100% | Tie |
| **etcd growth (delta)** | **+5,588 MB** | **+1,885 MB** | 3x less etcd growth |
| **etcd peak (absolute)** | 7,336 MB (91% of limit) | 3,190 MB (40% of limit) | 51 percentage points lower |
| **etcd growth from Tekton** | +5,588 MB | ~0 MB (growth is from pods) | 100% Tekton offload |
| **Peak concurrent TRs** | ~9,096 | ~7,707 | Similar |
| **Wall-clock to complete** | >30 min (stopped early) | ~31 min | Similar |
| **Peak KAS memory** | N/A (primary KAS) | 28 GB (secondary) | Additional resource cost |

## Key Takeaways

1. **etcd cannot handle 500 concurrent Konflux PipelineRuns.** At 91% of the 8 GB limit with only 45% of PRs completed, the test was stopped to protect cluster stability. etcd serves ALL Kubernetes resources -- hitting the limit would cause cluster-wide disruption.

2. **kube-shard eliminates the etcd storage bottleneck.** All 500 PRs completed with zero failures. etcd grew only +1,885 MB (from pod objects, not Tekton) vs +5,588 MB without kube-shard — a **3x reduction in etcd growth**. Tekton data itself is fully offloaded.

3. **etcd remains well within safe limits.** With kube-shard, etcd peaked at 40% of its 8 GB limit vs 91% without it. This leaves ample headroom for other cluster workloads.

4. **Creation rate trade-off.** etcd is 2-3x faster for object creation (4.5 vs 1.4-2.2 PRs/s) due to the additional aggregation hop through the secondary API server. The cold-start rate (1.4/s) reflects a freshly deployed system; a warm system achieves 2.2/s. This does not affect overall completion time, which is bounded by the Tekton controller's reconciliation capacity.

5. **Residual etcd growth is from pods, not Tekton.** The +1,885 MB etcd delta during the kube-shard test comes from pod objects (core Kubernetes resources). Each PipelineRun creates ~17 pods, which are always stored in the primary etcd regardless of where Tekton resources live. This growth is temporary and reclaimed as pods complete.

6. **Additional resource cost.** kube-shard requires a secondary KAS (peaked at 28 GB), Kine (16 GB), and PostgreSQL. This is a deliberate trade-off: dedicated infrastructure for Tekton storage vs shared etcd with a hard ceiling.
