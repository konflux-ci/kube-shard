# Conversion Webhook Transform Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transform CRD conversion webhook service references to URLs during CRD sync, and add update support so changes on the primary propagate to the secondary.

**Architecture:** Add a `transformCRDConversion` helper that converts `spec.conversion.webhook.clientConfig.service` to a direct URL. Modify `syncCRDsToSecondary` to call it and to update existing CRDs (not just create).

**Tech Stack:** Go, controller-runtime, `k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1`

## Global Constraints

- Go module: `github.com/konflux-ci/kube-shard`
- All exported/unexported functions need doc comments starting with the function name
- Tests use gomega with ginkgo (`Describe`/`It` blocks, `Expect(...)`)
- Existing test patterns in `operator/internal/controller/apishard/reconciler_test.go` use envtest (`k8sClient`)

---

### Task 1: Add transformCRDConversion helper and update syncCRDsToSecondary

**Files:**
- Modify: `operator/internal/controller/apishard/reconciler.go:891-960` (syncCRDsToSecondary function)

**Interfaces:**
- Consumes: `apiextensionsv1.CustomResourceDefinition` (from `k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1`)
- Produces: `transformCRDConversion(crd *apiextensionsv1.CustomResourceDefinition)` — mutates the CRD's conversion config in place

- [ ] **Step 1: Write the failing test for transformCRDConversion**

Add a new `Describe("transformCRDConversion", ...)` block in `operator/internal/controller/apishard/reconciler_test.go`:

```go
var _ = Describe("transformCRDConversion", func() {
	It("should convert service reference to URL", func() {
		port := int32(443)
		path := "/convert"
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							Service: &apiextensionsv1.ServiceReference{
								Namespace: "openshift-pipelines",
								Name:      "tekton-pipelines-webhook",
								Port:      &port,
								Path:      &path,
							},
							CABundle: []byte("fake-ca-bundle"),
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Strategy).To(Equal(apiextensionsv1.WebhookConverter))
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.Service).To(BeNil())
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.URL).NotTo(BeNil())
		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(
			"https://tekton-pipelines-webhook.openshift-pipelines.svc:443/convert",
		))
		Expect(crd.Spec.Conversion.Webhook.ClientConfig.CABundle).To(Equal([]byte("fake-ca-bundle")))
	})

	It("should use default port 443 when port is nil", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							Service: &apiextensionsv1.ServiceReference{
								Namespace: "openshift-pipelines",
								Name:      "tekton-pipelines-webhook",
							},
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(
			"https://tekton-pipelines-webhook.openshift-pipelines.svc:443",
		))
	})

	It("should not modify CRD with strategy None", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.NoneConverter,
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Strategy).To(Equal(apiextensionsv1.NoneConverter))
		Expect(crd.Spec.Conversion.Webhook).To(BeNil())
	})

	It("should not modify CRD with no conversion", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion).To(BeNil())
	})

	It("should not modify CRD with URL already set", func() {
		existingURL := "https://already-set.example.com:8443/convert"
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Conversion: &apiextensionsv1.CustomResourceConversion{
					Strategy: apiextensionsv1.WebhookConverter,
					Webhook: &apiextensionsv1.WebhookConversion{
						ConversionReviewVersions: []string{"v1"},
						ClientConfig: &apiextensionsv1.WebhookClientConfig{
							URL: &existingURL,
						},
					},
				},
			},
		}

		transformCRDConversion(crd)

		Expect(crd.Spec.Conversion.Webhook.ClientConfig.Service).To(BeNil())
		Expect(*crd.Spec.Conversion.Webhook.ClientConfig.URL).To(Equal(existingURL))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd operator && go test ./internal/controller/apishard/ -run "transformCRDConversion" -v`
Expected: Compilation failure — `transformCRDConversion` is undefined

- [ ] **Step 3: Implement transformCRDConversion**

Add this function in `operator/internal/controller/apishard/reconciler.go`, after the `syncCRDsToSecondary` function (around line 961):

```go
// transformCRDConversion rewrites the CRD's conversion webhook clientConfig
// from a service reference to a direct URL. The secondary API server cannot
// resolve service references through its own service proxy (it has no Service
// objects), but it can reach webhook pods via cluster DNS.
func transformCRDConversion(crd *apiextensionsv1.CustomResourceDefinition) {
	if crd.Spec.Conversion == nil {
		return
	}
	if crd.Spec.Conversion.Strategy != apiextensionsv1.WebhookConverter {
		return
	}
	if crd.Spec.Conversion.Webhook == nil || crd.Spec.Conversion.Webhook.ClientConfig == nil {
		return
	}
	svcRef := crd.Spec.Conversion.Webhook.ClientConfig.Service
	if svcRef == nil {
		return
	}

	port := int32(443)
	if svcRef.Port != nil {
		port = *svcRef.Port
	}
	path := ""
	if svcRef.Path != nil {
		path = *svcRef.Path
	}

	url := fmt.Sprintf("https://%s.%s.svc:%d%s",
		svcRef.Name,
		svcRef.Namespace,
		port,
		path,
	)
	crd.Spec.Conversion.Webhook.ClientConfig.URL = &url
	crd.Spec.Conversion.Webhook.ClientConfig.Service = nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd operator && go test ./internal/controller/apishard/ -run "transformCRDConversion" -v`
Expected: All 5 test cases pass

- [ ] **Step 5: Wire transformCRDConversion into syncCRDsToSecondary and add update path**

Replace the CRD sync loop in `syncCRDsToSecondary` (lines 932-957) with:

```go
	for _, name := range crdNames {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
			logger.Error(err, "Failed to get CRD from primary", "crd", name)
			continue
		}

		secondaryCRD := crd.DeepCopy()
		secondaryCRD.ResourceVersion = ""
		secondaryCRD.UID = ""
		secondaryCRD.OwnerReferences = nil
		secondaryCRD.ManagedFields = nil
		secondaryCRD.Generation = 0
		secondaryCRD.Finalizers = nil

		transformCRDConversion(secondaryCRD)

		existing := &apiextensionsv1.CustomResourceDefinition{}
		err := secondaryClient.Get(ctx, types.NamespacedName{Name: name}, existing)
		if apierrors.IsNotFound(err) {
			if createErr := secondaryClient.Create(ctx, secondaryCRD); createErr != nil {
				logger.Error(createErr, "Failed to create CRD on secondary", "crd", name)
				continue
			}
			logger.Info("Synced CRD to secondary", "crd", name)
		} else if err == nil {
			existing.Spec.Conversion = secondaryCRD.Spec.Conversion
			existing.Spec.Versions = secondaryCRD.Spec.Versions
			if updateErr := secondaryClient.Update(ctx, existing); updateErr != nil {
				logger.Error(updateErr, "Failed to update CRD on secondary", "crd", name)
				continue
			}
			logger.V(1).Info("Updated CRD on secondary", "crd", name)
		} else {
			logger.Error(err, "Failed to check CRD on secondary", "crd", name)
		}
	}
```

- [ ] **Step 6: Run the full reconciler test suite**

Run: `cd operator && go test ./internal/controller/apishard/ -v`
Expected: All tests pass (existing `reconcileCRDConflicts` tests still pass since they don't set up a secondary client)

- [ ] **Step 7: Run linter**

Run: `cd operator && go vet ./...`
Expected: No errors

- [ ] **Step 8: Commit**

```bash
git add operator/internal/controller/apishard/reconciler.go \
       operator/internal/controller/apishard/reconciler_test.go \
       README.md
git commit -m "$(cat <<'EOF'
feat: transform CRD conversion webhooks during sync to secondary

When syncing CRDs to the secondary API server, transform conversion
webhook clientConfig.service references into direct URLs. The secondary
cannot resolve service references through its own proxy (it has no
Service objects), but it can reach webhook pods via cluster DNS.

Also adds an update path: when a CRD already exists on the secondary,
spec.conversion and spec.versions are updated to reflect primary changes.

Closes konflux-ci/kube-shard#41

Assisted-by: Cursor
EOF
)"
```
