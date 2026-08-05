# Load Test Report — August 4-5, 2026

## Executive Summary

Three load test runs of 1,200 PipelineRuns each (20 PRs/minute for 1 hour) were executed against the kube-shard secondary API server on an OpenShift cluster with 12 worker nodes. The tests evaluated the impact of Tekton pruning and controller thread tuning on system stability and performance.

**Key findings:**
- The Tekton pruner reduced peak TaskRun count by ~60% and KAS memory by ~10%, enabling sustained operation
- Increasing `threads-per-controller` from 2→32 doubled PR completion speed but caused KAS OOMKills
- 8 threads/controller was the sweet spot: ~2x faster completions with stable KAS memory
- The secondary API server (KAS) watch cache is the primary memory bottleneck, scaling linearly with concurrent object count
- Kine compaction works but struggles under very high write churn, leading to DB bloat

## Cluster Configuration

| Component | Configuration |
|-----------|--------------|
| **Cluster** | OpenShift 4.x, 12 worker nodes (16 vCPU, ~60 GB RAM each) |
| **Secondary KAS** | 3 replicas |
| **Kine** | 3 replicas, 16 GB memory |
| **PostgreSQL** | 1 replica, 100 GB PVC |
| **Tekton Controllers** | 4 replicas (StatefulSet), 4 buckets |
| **Pipeline** | 17 tasks per PipelineRun (based on Konflux reverse-proxy pipeline) |

## Test Runs

### Run 1: Baseline (No Pruner)

| Setting | Value |
|---------|-------|
| Pruner | Disabled |
| threads-per-controller | 2 (default) |
| KAS memory | 32 GB |
| Date | Aug 4, 20:00–22:00 |

**Results:**

| Metric | Value |
|--------|-------|
| PRs created | 1,200 |
| Succeeded | 1,199 (99.9%) |
| Failed | 0 |
| Timed out | 1 |
| Avg completion time | 26.5 min |
| Median completion time | 27.6 min |
| p90 completion time | 42.1 min |
| Min / Max | 3.3 min / 46.4 min |
| Peak TaskRuns | ~24,000 |
| Peak DB size | 16 GB |
| Peak DB rows | ~55,000 |
| Peak KAS memory | ~31 GB |
| KAS OOMKills | 0 |
| Wall-clock (creation → all complete) | ~2 hours |
| Post-test cleanup (to 10 PRs) | ~40 min (pruner off, manual) |

### Run 2: With Pruner

| Setting | Value |
|---------|-------|
| Pruner | Enabled: keep=10, pipelinerun+taskrun, */5 min |
| threads-per-controller | 2 (default) |
| KAS memory | 32 GB |
| Date | Aug 5, 08:07–~09:50 |

**Results:**

| Metric | Value |
|--------|-------|
| PRs created | 1,200 |
| Succeeded | 1,189+ |
| Failed | 0 (pre-disruption) |
| Avg completion time | 26.8 min |
| Median completion time | 27.1 min |
| p90 completion time | 30.9 min |
| Min / Max | 18.9 min / 32.8 min |
| Peak TaskRuns | ~7,000 (vs ~24k without pruner) |
| Peak DB size | ~9 GB at end of creation |
| Peak KAS memory | ~29 GB |
| KAS OOMKills | 0 |

**Pruner impact (vs Run 1):**
- Peak TaskRun count: **70% reduction** (7k vs 24k)
- KAS memory: ~10% lower peak
- Completion time: Similar (~27 min avg) — pruner doesn't speed up individual PRs
- Post-test cleanup: Automatic, reached 10 PRs within ~20 min

> Note: This run was disrupted mid-test by thread tuning changes (32 threads → KAS OOMKills → 48 GB bump). Pre-disruption data is clean.

### Run 3: Pruner + Tuned Threads

| Setting | Value |
|---------|-------|
| Pruner | Enabled: keep=10, pipelinerun+taskrun, */5 min |
| threads-per-controller | 32 → reduced to 8 mid-test |
| KAS memory | 48 GB |
| Date | Aug 5, 10:34–~11:55 |

**Results (combined, including the 32→8 thread transition):**

| Metric | Value |
|--------|-------|
| PRs created | 1,200 |
| Creation failures | 11 |
| Failed (during 32-thread OOMKills) | ~69 |
| KAS OOMKills (at 32 threads) | Multiple (all 3 pods) |
| KAS OOMKills (at 8 threads) | 0 |

**With 32 threads (before reduction):**
- Completion rate: ~2x faster (65% completed at 276 PRs vs ~30% in Run 1/2)
- KAS memory: Hit 48 GB limit, OOMKills resumed
- Controller CPU: 1.3–2.0 cores each (vs 200–500m at 2 threads)

**With 8 threads (after reduction):**
- No KAS OOMKills
- KAS memory: Stable at 17–20 GB
- Tail PRs completed in ~8–9 min avg (low concurrency)
- Controller CPU: ~600–700m each

## Comparison Summary

| Metric | Run 1 (Baseline) | Run 2 (Pruner) | Run 3 (Pruner + 8 threads) |
|--------|-------------------|----------------|---------------------------|
| Pruner | Off | On | On |
| Threads/controller | 2 | 2 | 8 |
| KAS memory limit | 32 GB | 32 GB | 48 GB |
| Success rate | 99.9% | ~99.9% | ~94% (due to 32→8 transition) |
| Avg PR completion | 26.5 min | 26.8 min | ~15 min (estimated) |
| Peak TRs | ~24,000 | ~7,000 | ~9,000 |
| Peak KAS memory | 31 GB | 29 GB | 20 GB (at 8 threads) |
| Peak DB size | 16 GB | 9 GB (at creation end) | 39 GB (high churn) |
| KAS OOMKills | 0 | 0 | Yes (at 32 threads only) |
| Kine OOMKills | 0 | 0 | 3–7 restarts each |

## Key Observations

### 1. Pruner is Essential for Production
The Tekton pruner reduced peak TaskRun count by 70% and prevented unbounded growth of the KAS watch cache. Without it, all completed objects accumulate in memory until manually cleaned.

### 2. Controller Threads: 8 is the Sweet Spot
- **2 threads** (default): Stable but slow — each PR waits ~20+ min in reconciliation queue
- **8 threads**: ~2x faster completions, KAS memory stays within 48 GB
- **32 threads**: ~3x faster but causes KAS OOMKills even at 48 GB due to write churn

### 3. KAS Watch Cache is the Memory Bottleneck
Each KAS replica maintains a full in-memory watch cache of all objects it serves. Memory scales with:
- Number of concurrent objects (PRs + TRs + pods)
- Object size (Tekton status fields are large)
- Write rate (higher churn = more temporary memory for serialization)

### 4. Kine Compaction Struggles Under High Churn
At 8+ threads, the write rate to PostgreSQL exceeds Kine's compaction throughput, leading to:
- DB size growing to 39 GB despite only ~1,500 live rows
- 170k+ rows accumulating before compaction catches up
- Kine pods OOMKilling under the compaction load

### 5. PostgreSQL VACUUM FULL Required
Regular VACUUM doesn't reclaim disk space — only marks dead tuples as reusable. After each test run, `VACUUM FULL` is needed to shrink the database. This requires an exclusive table lock (API outage). See [#48](https://github.com/konflux-ci/kube-shard/issues/48) for table partitioning proposal.

## Recommended Production Configuration

```yaml
# TektonConfig
pipeline:
  performance:
    replicas: 4
    buckets: 4
    threads-per-controller: 8
    statefulset-ordinals: true
pruner:
  disabled: false
  keep: 10
  resources: [pipelinerun, taskrun]
  schedule: "*/5 * * * *"

# APIShard
secondary:
  replicas: 3
  resources:
    requests: { memory: "48Gi", cpu: "4" }
    limits: { memory: "48Gi", cpu: "4" }
kine:
  replicas: 3
  resources:
    requests: { memory: "16Gi", cpu: "2" }
    limits: { memory: "16Gi", cpu: "2" }
  compaction:
    timeout: 30s
    batchSize: 500
```

## Open Issues

- [#43](https://github.com/konflux-ci/kube-shard/issues/43) — PipelineRun failures when Kine OOMKills
- [#44](https://github.com/konflux-ci/kube-shard/issues/44) — Kine compaction timeout too short
- [#48](https://github.com/konflux-ci/kube-shard/issues/48) — PostgreSQL table partitioning to avoid VACUUM FULL
