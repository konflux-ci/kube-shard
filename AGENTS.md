# Agent Instructions

Instructions for AI agents working on this repository.

## Project Overview

kube-kine is a secondary aggregated Kubernetes API server backed by Kine (SQLite/PostgreSQL) instead of etcd. It offloads Tekton CRDs from the main cluster's etcd to eliminate the 8 GB storage size constraint that caps PipelineRun concurrency.

The full design is in [docs/design.md](docs/design.md).

## Architecture (Phase 1 PoC)

```
Client/Controller → Main kube-apiserver → [APIService aggregation] → Secondary kube-apiserver → Kine → SQLite
```

Key properties:
- **Tekton CRDs exist ONLY on the secondary** API server. They are never installed on the primary.
- **APIService objects** on the primary route `tekton.dev` and `resolution.tekton.dev` to the secondary.
- **Controllers are unchanged** -- they talk to the main API server; aggregation routes transparently.
- **Authorization**: Phase 1 uses `AlwaysAllow` + static token. Phase 2 adds webhook delegation.
- **Namespaces**: Phase 1 manually creates namespaces on the secondary. Phase 2 adds a sync controller.

## Repository Structure

```
docs/                       Design documents
deploy/poc/                 Kustomize directory for the Phase 1 PoC
  kustomization.yaml        Kustomize config (image versions managed here)
  namespace.yaml            tekton-apiserver namespace
  kine.yaml                 Kine Deployment + Service (SQLite)
  secondary-apiserver.yaml  Secondary kube-apiserver Deployment + Service
  apiservice.yaml           APIService registrations (applied separately with sed)
  test/                     Test resources (not part of kustomize base)
    pipeline.yaml           Test Pipeline + PipelineRun for validation
hack/                       Scripts
  setup-poc.sh              Main orchestration (runs all steps)
  setup-kind.sh             Creates kind cluster (docker/podman)
  generate-certs.sh         Generates TLS certs + static token
  install-tekton-crds.sh    Installs Tekton CRDs on the secondary
  validate-poc.sh           End-to-end validation test
  teardown-poc.sh           Cleanup
  kubectl-secondary.sh      Shim for direct kubectl to secondary
_output/                    (gitignored) Generated certs, downloaded releases
```

## Running the PoC

```bash
# Fresh kind cluster
make poc           # Full setup from scratch
make test          # Validate with a PipelineRun
make clean         # Tear down

# On an existing cluster (e.g., one already running Konflux)
make poc-existing  # Deploys secondary stack on current context
```

### Existing Cluster Mode

When `USE_EXISTING_CLUSTER=true`:
- Kind cluster creation is skipped; the current kubectl context is used
- The front-proxy CA is extracted from the `extension-apiserver-authentication` configmap in `kube-system` (works for any kubeadm-based cluster, including kind)
- If Tekton CRDs already exist on the primary, they are **deleted** before registering APIService objects (they cannot coexist)
- `SKIP_TEKTON_INSTALL=true` prevents re-installing the controller (existing one is restarted)
- `MIRROR_NAMESPACES` specifies which namespaces to create on the secondary

## Interacting with the Secondary API Server

Use the shim script for direct access to the secondary (bypassing aggregation):

```bash
# List CRDs on the secondary
./hack/kubectl-secondary.sh get crds

# Get resources stored on the secondary
./hack/kubectl-secondary.sh get pipelineruns -A

# Create a namespace on the secondary
./hack/kubectl-secondary.sh create namespace my-namespace
```

## Key Technical Notes

- kube-apiserver v1.31+ **disables anonymous auth** when `--authorization-mode=AlwaysAllow`. The PoC uses `--token-auth-file` for direct authentication.
- The `v1alpha1.tekton.dev` APIService must be registered (not just `v1` and `v1beta1`). The Tekton controller's VerificationPolicy informer blocks all reconciliation if it can't list v1alpha1 resources.
- TCP probes are used for liveness/readiness because HTTP health endpoints require authentication when anonymous auth is disabled.
- The front-proxy CA is extracted from the kind control plane node -- this is what the secondary uses to trust identity headers from the main API server.
- **Disabled admission plugins** (`--disable-admission-plugins=NamespaceLifecycle,ServiceAccount`):
  - `NamespaceLifecycle`: Namespaces are mirrored from the primary, not authoritative on the secondary. This plugin would reject requests targeting namespaces that haven't been synced yet.
  - `ServiceAccount`: All authentication happens on the primary; the secondary receives pre-authenticated identity via request headers. Enabling this plugin would require syncing ServiceAccount objects to the secondary.
- **Image versions** are managed centrally in `deploy/poc/kustomization.yaml`. Override with `kustomize edit set image`.

## Environment Variables

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

## Development Conventions

- Go module: `github.com/konflux-ci/kube-kine`
- Scripts in `hack/` should be self-documenting with a header comment block
- Kubernetes manifests use Kustomize (`deploy/poc/kustomization.yaml`)
- Generated/temporary files go in `_output/` (gitignored)
- Environment variables provide configuration; all have sensible defaults
