# kube-kine

An aggregated Kubernetes API server backed by [Kine](https://github.com/k3s-io/kine) (PostgreSQL/SQLite/MySQL) instead of etcd. Offloads CRDs from the main cluster's etcd to eliminate storage size constraints.

## What it does

kube-kine runs a secondary `kube-apiserver` on a Kubernetes cluster, backed by PostgreSQL (or SQLite for development). The main cluster's kube-apiserver forwards requests for configured API groups to kube-kine via [API aggregation](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/) (APIService resources).

Controllers and clients are unaware of the split -- they talk to the main kube-apiserver as usual, and aggregation transparently routes requests to the correct backend.

## Why

Etcd has a hard 8 GB storage limit. For workloads that generate large, high-churn CRDs (e.g., Tekton PipelineRuns, TaskRuns), this becomes the primary bottleneck on cluster capacity. PostgreSQL has no practical size limit for these workloads.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Client / Controller                                    │
│  (kubectl, Tekton controller, Kueue, etc.)             │
└──────────────────────────┬──────────────────────────────┘
                           │ standard API calls
                           ▼
┌─────────────────────────────────────────────────────────┐
│  Main kube-apiserver (manages Pods, Secrets, etc.)      │
│  ┌───────────────────────────────────────────────────┐  │
│  │ APIService: forward tekton.dev → kube-kine        │  │
│  └───────────────────────────────────────────────────┘  │
└──────────────────────────┬──────────────────────────────┘
                           │ aggregation proxy
                           ▼
┌─────────────────────────────────────────────────────────┐
│  kube-kine (secondary kube-apiserver)                   │
│  • Serves configured CRDs (e.g., tekton.dev)           │
│  • Delegates authz to main cluster (webhook)           │
│  • Admission webhooks registered locally               │
└──────────────────────────┬──────────────────────────────┘
                           │ etcd v3 gRPC
                           ▼
┌─────────────────────────────────────────────────────────┐
│  Kine                                                   │
│  (translates etcd API → SQL)                           │
└──────────────────────────┬──────────────────────────────┘
                           │ SQL
                           ▼
┌─────────────────────────────────────────────────────────┐
│  PostgreSQL / SQLite / MySQL                            │
└─────────────────────────────────────────────────────────┘
```

## Status

**Early development / Proof of Concept**

See [docs/design.md](docs/design.md) for the full design document.

## Getting Started

TODO -- PoC instructions for deploying on a kind cluster.

## License

Apache-2.0
