# Design: Transform CRD Conversion Webhooks During Sync

**Date:** 2026-08-04  
**Issue:** [konflux-ci/kube-shard#41](https://github.com/konflux-ci/kube-shard/issues/41)  
**Status:** Proposed

## Problem

When the APIShard reconciler syncs CRDs from the primary cluster to the secondary API server, it copies the `spec.conversion` block verbatim. If the CRD uses a conversion webhook (e.g., Tekton CRDs converting between `v1` and `v1beta1`), the `clientConfig.service` reference points to a Kubernetes Service (`tekton-pipelines-webhook.openshift-pipelines.svc:443`). The secondary API server resolves this through its own service proxy — but since the Service object doesn't exist on the secondary, resolution fails with continuous errors:

```
failed to list tekton.dev/v1beta1, Kind=TaskRun: conversion webhook for tekton.dev/v1, Kind=TaskRun failed:
Post "https://tekton-pipelines-webhook.openshift-pipelines.svc:443/resource-conversion?timeout=30s":
service "tekton-pipelines-webhook" not found
```

## Solution

Transform the conversion webhook's `clientConfig` during CRD sync, using the same pattern already used by the `WebhookSync` controller for mutating/validating webhooks: convert the `service` reference into a direct URL that the secondary can resolve via cluster DNS.

The webhook service lives on the primary cluster's network, but the secondary pod runs in the same cluster. DNS names like `<service>.<namespace>.svc` resolve correctly from the secondary pod — the issue is only that the secondary's internal service proxy can't find the Service object (since the secondary has no Services). By converting to a URL, the secondary bypasses its service proxy and connects directly via cluster DNS.

## Design

### Transformation logic

Add a `transformCRDConversion` helper in the APIShard reconciler that:

1. Checks if `crd.Spec.Conversion != nil` and `crd.Spec.Conversion.Strategy == "Webhook"`
2. If `crd.Spec.Conversion.Webhook.ClientConfig.Service != nil`:
   - Constructs URL: `https://<name>.<namespace>.svc:<port><path>`
   - Sets `clientConfig.URL = &url`
   - Sets `clientConfig.Service = nil`
   - Preserves `clientConfig.CABundle` unchanged
3. If the conversion webhook already uses a URL (no service reference), leaves it unchanged
4. If conversion strategy is `None` or unset, does nothing

The URL construction formula is identical to what `transformMutatingWebhook` / `transformValidatingWebhook` use in `operator/internal/controller/webhooksync/reconciler.go`:

```go
url := fmt.Sprintf("https://%s.%s.svc:%d%s",
    svcRef.Name,
    svcRef.Namespace,
    servicePort(svcRef.Port),
    servicePath(svcRef.Path),
)
```

For CRD conversion webhooks, the `port` field lives at `clientConfig.service.port` (int32 pointer, defaults to 443) and `path` lives at `clientConfig.service.path` (string pointer).

### Update support for CRD sync

Currently `syncCRDsToSecondary` only creates CRDs on the secondary; if the CRD already exists, it's skipped. This means if the conversion webhook config changes on the primary (e.g., the service name or path changes during an operator upgrade), the secondary won't pick up the change.

The fix adds an update path: when the CRD already exists on the secondary, overwrite `existing.Spec.Conversion` with the transformed conversion config and `existing.Spec.Versions` with the primary's version list (the stored-version list may change during Tekton upgrades). Other spec fields (names, scope, schema) are left unchanged — they're set at initial sync and don't drift. The update uses the same get-then-update pattern as the WebhookSync controller.

### Where the code lives

All changes are in `operator/internal/controller/apishard/reconciler.go`:

- **`transformCRDConversion(crd *apiextensionsv1.CustomResourceDefinition)`** — mutates the CRD's conversion config in place (after DeepCopy)
- **`syncCRDsToSecondary`** — calls `transformCRDConversion` after stripping metadata, and adds an update path for existing CRDs

### What doesn't change

- The `WebhookSync` controller (handles admission webhooks, unrelated to CRD conversion)
- The secondary API server flags
- The APIShard CRD/spec — no new fields needed
- The `WebhookSyncSpec` / `WebhookSyncStatus` types

## Testing

- Unit test: given a CRD with a conversion webhook service reference, verify `transformCRDConversion` produces the expected URL and nils out the service
- Unit test: given a CRD with conversion strategy `None`, verify no transformation
- Unit test: given a CRD with a conversion webhook already using a URL, verify no change
- Integration (envtest): verify that `syncCRDsToSecondary` creates/updates CRDs on the secondary with transformed conversion config

## Rejected alternatives

**Strip conversion webhooks entirely** — setting `strategy: None` breaks multi-version support. Clients watching `v1beta1` would get errors because the API server can't convert from the stored `v1` version. The whole point of conversion webhooks is enabling multi-version support.

**Add a config knob** — YAGNI. If the webhook service is unreachable from the secondary, the operator has a fundamental networking problem that a config knob won't solve.
