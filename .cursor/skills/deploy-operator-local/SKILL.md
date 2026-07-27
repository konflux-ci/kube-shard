---
name: deploy-operator-local
description: >-
  Deploy the kube-shard operator to a local kind cluster for development and
  testing. Covers building the image, loading it into kind, installing
  dependencies (cert-manager), CRDs, deploying the operator, and running e2e
  tests. Use when the user asks to deploy, test, or run the operator locally.
---

# Deploy Operator to Local Kind Cluster

## Prerequisites

- `kind` CLI installed
- `podman` available (preferred) or `docker`
- `kubectl` configured
- A kind cluster running (default name: `kube-shard-test`)

## Full Deployment Workflow

All commands run from the `operator/` directory.

### 1. Ensure Kind Cluster Exists

```bash
kind get clusters | grep -q kube-shard-test || kind create cluster --name kube-shard-test
```

### 2. Build the Operator Image

Use a non-`latest` tag to avoid Kubernetes defaulting to `Always` pull policy:

```bash
make docker-build CONTAINER_TOOL=podman IMG=localhost/controller:e2e
```

### 3. Load Image into Kind

```bash
dir=$(mktemp -d) && \
podman save localhost/controller:e2e -o ${dir}/operator.tar && \
kind load image-archive -n kube-shard-test ${dir}/operator.tar && \
rm -r ${dir}
```

### 4. Install Dependencies

```bash
make cert-manager
```

### 5. Install CRDs

```bash
make install
```

### 6. Deploy the Operator

```bash
make deploy IMG=localhost/controller:e2e
```

Wait for readiness:

```bash
kubectl wait --for=condition=Available deployment/operator-controller-manager \
  -n kube-shard-operator --timeout=120s
```

### 7. Run Tests

Unit tests:

```bash
make test
```

E2e tests (CRD sharding):

```bash
KIND_CLUSTER=kube-shard-test go test ./test/e2e/crd_sharding/ -v -ginkgo.v -timeout 5m
```

Full e2e suite (includes metrics test that requires Prometheus Operator):

```bash
KIND_CLUSTER=kube-shard-test go test ./test/e2e/ -v -ginkgo.v -timeout 5m
```

## Important Notes

- **Image tag**: Never use `:latest` for kind-loaded images. Kubernetes treats `:latest` as `imagePullPolicy: Always`, causing `ImagePullBackOff` since the image only exists locally.
- **CONTAINER_TOOL**: The Makefile defaults to `docker`. Pass `CONTAINER_TOOL=podman` if docker is unavailable.
- **KIND_CLUSTER env var**: The e2e test suite reads this to find the correct cluster. Must match the kind cluster name.
- **Stale resources**: If tests fail on `already exists` errors (e.g., `clusterrolebinding operator-metrics-binding`), delete the stale resource and retry.
- **Prometheus test**: The metrics e2e test requires Prometheus Operator CRDs (`ServiceMonitor`). Skip with `PROMETHEUS_INSTALL_SKIP=true` or install Prometheus separately.

## Teardown

```bash
make undeploy
make uninstall
make cert-manager-undeploy
kind delete cluster --name kube-shard-test
```

## Quick Reference: Makefile Targets

| Target | Description |
|--------|-------------|
| `docker-build` | Build operator container image |
| `cert-manager` | Install cert-manager on cluster |
| `cert-manager-undeploy` | Remove cert-manager |
| `install` | Install CRDs |
| `uninstall` | Remove CRDs |
| `deploy` | Deploy operator (set `IMG=`) |
| `undeploy` | Remove operator deployment |
| `test` | Run unit tests |
| `test-e2e` | Run full e2e suite |
