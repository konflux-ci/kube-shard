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

### Prerequisites

- [kind](https://kind.sigs.k8s.io/) v0.20+
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- Docker or Podman
- openssl
- curl

### Option A: Fresh kind cluster

Creates a dedicated kind cluster with the full stack from scratch:

```bash
make poc       # Setup: kind + Kine + secondary apiserver + Tekton
make test      # Validate with a PipelineRun
make clean     # Tear down
```

### Option B: Deploy on an existing cluster

Deploys the secondary API server stack onto your current kubectl context. Useful when you already have a kind cluster running Konflux:

```bash
# Deploy on current context (assumes Tekton is already installed)
make poc-existing

# Or with full control over options:
USE_EXISTING_CLUSTER=true \
  SKIP_TEKTON_INSTALL=true \
  MIRROR_NAMESPACES="default my-tenant-ns" \
  ./hack/setup-poc.sh
```

If the cluster already has Tekton CRDs installed, the script removes them from the primary and registers APIService objects instead (CRDs and APIService cannot coexist for the same group/version). The existing Tekton controller is restarted to pick up the new aggregation path.

### Common operations

```bash
make test              # Validate with a PipelineRun
make logs-apiserver    # Tail secondary API server logs
make logs-kine         # Tail Kine logs
make secondary ARGS="get crds"  # Direct kubectl to secondary

# Direct access to the secondary API server (bypasses aggregation)
./hack/kubectl-secondary.sh get pipelineruns -A
./hack/kubectl-secondary.sh get namespaces
```

### Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `USE_EXISTING_CLUSTER` | `false` | Skip kind creation, deploy on current kubectl context |
| `SKIP_TEKTON_INSTALL` | `false` | Don't install Tekton controller (use existing) |
| `KIND_CLUSTER_NAME` | `kube-kine-poc` | Name of the kind cluster to create/use |
| `TEKTON_VERSION` | `v0.65.2` | Tekton Pipeline release to install |
| `FRONT_PROXY_CA` | *(auto-detected)* | Path to the cluster's front-proxy CA cert |
| `MIRROR_NAMESPACES` | `default` | Space-separated namespaces to create on secondary |
| `SECONDARY_PORT` | `6444` | Local port for port-forward to secondary |
| `KEEP_PORT_FWD` | `false` | Don't kill port-forward on exit (kubectl-secondary) |

Image versions are managed in `deploy/poc/kustomization.yaml`. To override:

```bash
cd deploy/poc
kustomize edit set image rancher/kine:v0.16.3
kustomize edit set image registry.k8s.io/kube-apiserver:v1.36.2
```

### Architecture

The PoC deploys:
- A secondary `kube-apiserver` backed by Kine (SQLite)
- Tekton CRDs installed only on the secondary
- APIService objects routing `tekton.dev` and `resolution.tekton.dev` through API aggregation
- The Tekton Pipeline controller reconciling PipelineRuns transparently

The manifests use [Kustomize](https://kustomize.io/) for image management and are deployed via `kubectl apply -k deploy/poc`.

## License

Apache-2.0
