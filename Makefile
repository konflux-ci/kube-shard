SHELL := /bin/bash
.DEFAULT_GOAL := help

KUBE_VERSION ?= $(shell kubectl version --client -o json 2>/dev/null | jq -r '.clientVersion.gitVersion' | sed 's/+.*//')
KIND_CLUSTER_NAME ?= kube-kine-poc
NAMESPACE ?= tekton-apiserver

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: poc
poc: ## Run the full Phase 1 POC (fresh kind cluster + Kine/SQLite + Tekton)
	./hack/setup-poc.sh

.PHONY: poc-existing
poc-existing: ## Deploy on existing cluster (uses current kubectl context)
	USE_EXISTING_CLUSTER=true SKIP_TEKTON_INSTALL=true ./hack/setup-poc.sh

.PHONY: phase2
phase2: ## Upgrade to Phase 2: webhook authorization + RBAC (requires Phase 1)
	./hack/setup-phase2.sh

.PHONY: test-phase2
test-phase2: ## Validate Phase 2: RBAC enforcement tests
	./hack/validate-phase2.sh

.PHONY: phase3
phase3: ## Upgrade to Phase 3: Tekton admission webhooks (requires Phase 2)
	./hack/setup-phase3.sh

.PHONY: test-phase3
test-phase3: ## Validate Phase 3: Tekton webhook validation/mutation tests
	./hack/validate-phase3.sh

.PHONY: phase4
phase4: ## Upgrade to Phase 4: Kueue + tekton-kueue integration (requires Phase 3)
	./hack/setup-phase4.sh

.PHONY: test-phase4
test-phase4: ## Validate Phase 4: Kueue quota management tests
	./hack/validate-phase4.sh

.PHONY: phase5
phase5: ## Upgrade to Phase 5: PostgreSQL backend (requires Phase 3)
	./hack/setup-phase5.sh

.PHONY: test-phase5
test-phase5: ## Validate Phase 5: PostgreSQL persistence and regression tests
	./hack/validate-phase5.sh

.PHONY: kind
kind: ## Create the kind cluster only
	./hack/setup-kind.sh

.PHONY: certs
certs: ## Generate certificates for the secondary API server
	./hack/generate-certs.sh

.PHONY: test
test: ## Run the POC validation (create PipelineRun and check completion)
	./hack/validate-poc.sh

.PHONY: clean
clean: ## Tear down the POC (delete kind cluster)
	./hack/teardown-poc.sh

.PHONY: secondary
secondary: ## Run kubectl against the secondary API server directly (use ARGS="get crds")
	./hack/kubectl-secondary.sh $(ARGS)

.PHONY: logs-apiserver
logs-apiserver: ## Tail secondary API server logs
	kubectl -n $(NAMESPACE) logs -l app=secondary-apiserver -f

.PHONY: logs-kine
logs-kine: ## Tail Kine logs
	kubectl -n $(NAMESPACE) logs -l app=kine -f
