---
name: load-test
description: >-
  Run a storage load test against the kube-shard secondary API server by
  creating large PipelineRuns. Use when the user asks to load test, stress
  test, or benchmark the shard's storage capacity.
---

# Storage Load Test

Stress-test the kube-shard secondary API server by creating many large
PipelineRuns that the Tekton controller processes into TaskRuns, multiplying
the actual storage footprint.

## Prerequisites

- kube-shard operator deployed with an APIShard CR active
- Tekton controller running and connected to the shard
- PostgreSQL pod accessible for DB size checks
- `kubectl` configured to talk to the cluster

## Quick Start

```bash
# 1000 PipelineRuns, 100KB each, 10 tasks per PR, 10 in parallel
./hack/loadtest/run-loadtest.sh 1000 100 10 10
```

## Parameters

| Position | Env Var | Default | Description |
|----------|---------|---------|-------------|
| `$1` | - | 1000 | Number of PipelineRuns to create |
| `$2` | - | 100 | Size of each PipelineRun in KB |
| `$3` | - | 10 | Number of tasks per PipelineRun |
| `$4` | - | 10 | Parallel kubectl processes |
| - | `NAMESPACE` | default | Target namespace |
| - | `PIPELINERUN_YAML` | (generated) | Path to pre-generated YAML |

## Storage Multiplier

Each PipelineRun with N tasks generates:
- 1 PipelineRun object (written + updated multiple times)
- N TaskRun objects (each written + updated)
- Status updates on all objects

A 100KB PipelineRun with 10 tasks produces roughly **11x** the raw PR size in
total DB writes (including Kine's revision history).

## Sizing Guide

| Goal | Command |
|------|---------|
| Quick smoke test | `./hack/loadtest/run-loadtest.sh 100 100 10 10` |
| Exceed 8GB etcd limit | `./hack/loadtest/run-loadtest.sh 1000 100 10 10` |
| Heavy stress test | `./hack/loadtest/run-loadtest.sh 5000 100 10 10` |

## Monitoring DB Size

While the load test runs, monitor PostgreSQL:

```bash
kubectl exec deployment/konflux-tekton-shard-postgresql -n kube-shard-operator -- \
  psql -U kine -d kine -c "SELECT pg_size_pretty(pg_database_size('kine')) AS db_size, count(*) AS rows FROM kine;"
```

## Monitoring Pod Health

Watch for OOM kills on Kine and apiserver pods:

```bash
kubectl get pods -n kube-shard-operator -l app.kubernetes.io/instance=<shard-name> -w
```

If Kine pods OOM, bump memory via the APIShard CR:

```bash
kubectl patch apishard <name> --type=merge -p '{"spec":{"kine":{"resources":{"limits":{"memory":"16Gi"}}}}}'
```

## Cleanup

```bash
kubectl delete pipelineruns --all -n default --wait=false
kubectl delete taskruns --all -n default --wait=false
```

After deletion, run a PostgreSQL VACUUM to reclaim disk:

```bash
kubectl exec deployment/konflux-tekton-shard-postgresql -n kube-shard-operator -- \
  psql -U kine -d kine -c "VACUUM FULL kine;"
```

## Scripts

- `hack/loadtest/generate-pipelinerun.py` -- generates a single PipelineRun
  YAML of a target size with random non-compressible padding
- `hack/loadtest/run-loadtest.sh` -- batch-creates PipelineRuns and reports
  progress with estimated storage usage
