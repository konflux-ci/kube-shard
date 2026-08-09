# Agent Instructions

Instructions for AI agents working on this repository.

## Project Overview

kube-shard is a secondary aggregated Kubernetes API server backed by Kine (SQLite/PostgreSQL) instead of etcd. It offloads CRDs from the main cluster's etcd to eliminate the 8 GB storage size constraint that caps PipelineRun concurrency.

The full design is in [docs/design.md](docs/design.md).

## Architecture

The kube-shard operator watches `APIShard` custom resources and reconciles the full secondary API server stack:

```
User/GitOps → APIShard CR → kube-shard Operator → Kine Deployment + Secondary kube-apiserver + APIService objects + CRDs + NamespaceSync + WebhookSync
```

Request flow once deployed:

```
Client/Controller → Main kube-apiserver → [APIService aggregation] → Secondary kube-apiserver → Kine → PostgreSQL/SQLite
```

Key properties:
- **Operator-managed.** A single `APIShard` CR drives deployment of the entire secondary stack.
- **Generic.** Not tied to Tekton -- any API group backed by CRDs can be offloaded.
- **Controllers are unchanged** -- they talk to the main API server; aggregation routes transparently.
- **Authorization** is delegated to the main cluster via `--authorization-mode=Webhook` (SubjectAccessReview).
- **Namespace sync** mirrors namespaces from the primary based on a label selector.
- **Webhook sync** mirrors admission and conversion webhooks for sharded API groups.
- **CRD coexistence** (`forceAggregation: true`) overrides the kube-aggregator's auto-register controller so existing CRDs don't need removal.

## Repository Structure

```
operator/                          Kubebuilder operator (production codebase)
  api/v1alpha1/                    CRD types (APIShard, NamespaceSync, WebhookSync)
  cmd/                             Operator entrypoint
  config/                          Kustomize (CRD, RBAC, manager, prometheus, samples)
  internal/
    aggregation/                   APIService reconciliation + conflict detection
    certs/                         cert-manager integration
    condition/                     Status condition helpers
    controller/
      apishard/                    Main reconciler
      namespacesync/               Namespace mirroring controller
      webhooksync/                 Webhook mirroring controller
    resources/                     Resource builders (Kine, secondary, PostgreSQL, metrics, PDB, SCC)
    secondary/                     Client for secondary API server
  test/e2e/                        E2E test suites
  Makefile                         Build, test, deploy targets
docs/                              Design documents and load test reports
deploy/loadtest/                   APIShard + TektonConfig manifests for load testing
tools/generate-from-pipeline/      Generates realistic load-test PipelineRuns from Konflux pipelines
hack/loadtest/                     Scripts for running storage load tests
```

## Running the Operator

All commands run from the `operator/` directory:

```bash
# Install CRDs
make install

# Run the operator locally (outside cluster, against current kubectl context)
make run

# Build and push the operator image
make docker-build docker-push IMG=<your-registry>/kube-shard-operator:latest

# Deploy the operator to a cluster
make deploy IMG=<your-registry>/kube-shard-operator:latest

# Uninstall
make undeploy
make uninstall
```

### Create an APIShard

```yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: tekton-shard
spec:
  targetNamespace: kube-shard-operator
  apiGroups:
  - group: tekton.dev
    versions: ["v1", "v1beta1"]
  - group: resolution.tekton.dev
    versions: ["v1beta1"]
  storage:
    type: InClusterPostgreSQL
  namespaceSync:
    labelSelector:
      matchLabels:
        konflux.dev/type: tenant
  secondary:
    replicas: 1
  kine:
    replicas: 1
```

## Running Tests

From the `operator/` directory:

```bash
# Unit tests
make test

# E2E tests (requires a running cluster)
make test-e2e

# Generate manifests and code after API changes
make manifests generate

# Lint
make lint
```

## Key Technical Notes

- **Disabled admission plugins** on the secondary (`NamespaceLifecycle`, `ServiceAccount`, `ResourceQuota`): namespaces are mirrored from the primary; SA tokens are handled by the main cluster; quota enforcement is intentionally not synced (see [docs/design.md](docs/design.md) for rationale).
- **Webhook handling:** the operator transforms `clientConfig.service` references to `clientConfig.url` because the secondary has no Service objects in its store.
- **CRD conversion webhooks** are transformed the same way during CRD sync to the secondary.

## Development Conventions

- Go module: `github.com/konflux-ci/kube-shard`
- Generated/temporary files go in `_output/` (gitignored)
- **Doc comments**: Every exported and unexported function/method must have a Go doc comment. The comment must start with the function name (e.g., `// BuildKineDeployment constructs ...`).
- **Test assertions**: Use [gomega](https://onsi.github.io/gomega/) for all test assertions. Initialize with `g := NewGomegaWithT(t)` and use `g.Expect(...)` instead of raw `t.Error`/`t.Fatal` patterns.
