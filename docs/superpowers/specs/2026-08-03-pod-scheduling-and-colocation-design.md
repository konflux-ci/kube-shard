# Pod Scheduling & Co-location Design

Addresses [#7](https://github.com/konflux-ci/kube-shard/issues/7) (scheduling
primitives) and [#9](https://github.com/konflux-ci/kube-shard/issues/9)
(co-location).

## Overview

The `APIShard` CRD currently provides no way to control pod scheduling. This
design adds three capabilities:

1. **User-facing scheduling primitives** — `nodeSelector`, `tolerations`, and
   `topologySpreadConstraints` on `SecondarySpec` and `KineSpec`.
2. **Auto anti-affinity** — the operator automatically spreads replicas across
   nodes when `replicas > 1`.
3. **Co-location with topology-aware routing** — the operator co-locates
   apiserver and Kine pods on the same nodes and enables `PreferSameNode`
   traffic distribution on the Kine Service, minimizing gRPC latency.

## Motivation

- **nodeSelector / tolerations**: Pin API shard components to dedicated
  infra/storage nodes or tolerate taints on dedicated node pools.
- **topologySpreadConstraints**: Spread replicas across availability zones for
  HA.
- **Anti-affinity**: Prevent multiple replicas of the same component from
  landing on the same node (single point of failure).
- **Co-location**: The secondary apiserver talks to Kine over gRPC for every
  read/write. In the many-to-many model (N apiservers, M Kine pods, any-to-any
  via a Service), co-locating them on the same nodes and enabling
  `PreferSameNode` traffic distribution minimizes cross-node network hops.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scheduling field scope | Per-component only (`SecondarySpec`, `KineSpec`) | YAGNI — a shared top-level block adds merge complexity for little benefit |
| `InClusterStorage` scheduling | Not added | Dev/staging only; not worth the API surface |
| Anti-affinity trigger | Auto-inject when `replicas > 1` | Sensible default; no user-facing field needed |
| Anti-affinity strength | `preferredDuringSchedulingIgnoredDuringExecution` | Won't block scheduling on small clusters |
| Co-location default | Enabled by default (`colocateComponents: true`) | Soft preference, safe, beneficial for latency |
| Traffic distribution | `PreferSameNode` on Kine Service | GA in k8s 1.35, beta (enabled by default) in 1.34; graceful fallback if unsupported |

## CRD Changes

### SecondarySpec

```go
type SecondarySpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains the secondary apiserver pods to nodes
	// matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows the secondary apiserver pods to schedule onto
	// nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how secondary apiserver pods
	// are distributed across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}
```

### KineSpec

```go
type KineSpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// ... existing ConnectionPool, Compaction, PollBatchSize,
	//     WatchProgressNotifyInterval fields unchanged ...

	// NodeSelector constrains Kine pods to nodes matching the specified labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allows Kine pods to schedule onto nodes with matching taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// TopologySpreadConstraints controls how Kine pods are distributed
	// across topology domains (zones, racks, etc.).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}
```

### APIShardSpec

```go
type APIShardSpec struct {
	// ... existing fields unchanged ...

	// ColocateComponents enables co-location of apiserver and Kine pods
	// on the same nodes, combined with topology-aware routing
	// (trafficDistribution: PreferSameNode) on the Kine Service.
	// This minimizes gRPC latency between the apiserver and Kine.
	// Requires Kubernetes 1.33+ (PreferSameNode feature gate).
	// On older clusters or network plugins that don't support
	// PreferSameNode, the system falls back gracefully to same-zone
	// or cluster-wide routing.
	// +optional
	// +kubebuilder:default=true
	ColocateComponents *bool `json:"colocateComponents,omitempty"`
}
```

Note: `*bool` is required to distinguish "not set" (nil → default true) from
"explicitly false". A plain `bool` with `omitempty` would silently convert
explicit `false` back to `true` via the kubebuilder default.

### Example CR

```yaml
apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: tekton
spec:
  targetNamespace: tekton-apiserver
  colocateComponents: true  # default
  secondary:
    replicas: 3
    nodeSelector:
      node-role.kubernetes.io/infra: ""
    tolerations:
      - key: node-role.kubernetes.io/infra
        operator: Exists
        effect: NoSchedule
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app.kubernetes.io/component: apiserver
  kine:
    replicas: 3
    nodeSelector:
      node-role.kubernetes.io/infra: ""
    tolerations:
      - key: node-role.kubernetes.io/infra
        operator: Exists
        effect: NoSchedule
  storage:
    type: PostgreSQL
    connectionSecretRef:
      name: rds-credentials
      key: KINE_ENDPOINT
  apiGroups:
    - group: tekton.dev
      versions: ["v1", "v1beta1"]
  namespaceSync:
    labelSelector:
      matchLabels:
        konflux.dev/tenant: "true"
```

## Operator Behavior

### 1. User-facing scheduling primitives

The resource builders (`BuildSecondaryDeployment`, `BuildKineDeployment`)
propagate these fields directly into the generated Deployment's PodSpec:

```go
Spec: corev1.PodSpec{
    NodeSelector:              shard.Spec.Secondary.NodeSelector,
    Tolerations:               shard.Spec.Secondary.Tolerations,
    TopologySpreadConstraints: shard.Spec.Secondary.TopologySpreadConstraints,
    // ...
}
```

If not set, the fields are `nil` / empty (no scheduling constraints applied).

### 2. Auto anti-affinity

When `replicas > 1`, the operator injects a soft `podAntiAffinity` rule into
each Deployment's PodSpec. This is done in the resource builder, not by the
user.

**Secondary apiserver Deployment:**

```yaml
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/instance: <shard-name>
              app.kubernetes.io/component: apiserver
          topologyKey: kubernetes.io/hostname
```

**Kine Deployment:**

```yaml
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/instance: <shard-name>
              app.kubernetes.io/component: storage
          topologyKey: kubernetes.io/hostname
```

The weight of 100 (maximum) makes anti-affinity the strongest scheduling
preference. When `replicas == 1`, no anti-affinity is injected.

### 3. Co-location with topology-aware routing

When `spec.colocateComponents` is `true` (the default):

**a) Pod affinity on the secondary apiserver Deployment:**

```yaml
affinity:
  podAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 80
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/instance: <shard-name>
              app.kubernetes.io/component: storage
          topologyKey: kubernetes.io/hostname
```

The co-location affinity weight (80) is intentionally lower than the
anti-affinity weight (100). When they conflict (e.g., a node already has an
apiserver pod AND a Kine pod), spreading across nodes takes priority.

**b) Traffic distribution on the Kine Service:**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: <shard>-kine
spec:
  trafficDistribution: PreferSameNode
  # ...
```

This tells kube-proxy to prefer routing to the Kine pod on the same node as
the calling apiserver. The fallback chain is:

1. Same-node Kine pod (if `PreferSameNode` is supported)
2. Same-zone Kine pod (if only `PreferSameZone` is supported by the proxy)
3. Any healthy Kine pod (standard round-robin)

### Why both podAffinity AND PreferSameNode are needed

Pod affinity alone ensures pods land on the same nodes, but doesn't influence
Service routing — kube-proxy may still route to a Kine pod on a different node.

`PreferSameNode` alone influences routing but is useless if there's no Kine pod
on the same node as the apiserver.

Together, they ensure (a) both components exist on the same nodes and (b)
traffic prefers the local one.

### Combined affinity block

When `replicas > 1` and `colocateComponents: true`, the secondary apiserver's
PodSpec gets both rules in a single `affinity` block:

```yaml
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/instance: tekton
              app.kubernetes.io/component: apiserver
          topologyKey: kubernetes.io/hostname
  podAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 80
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/instance: tekton
              app.kubernetes.io/component: storage
          topologyKey: kubernetes.io/hostname
```

The Kine Deployment only gets `podAntiAffinity` (no co-location affinity on
the Kine side — the apiserver is the one that seeks out Kine, not the other
way around).

## Compatibility

| Kubernetes version | `PreferSameNode` status | Behavior |
|--------------------|-------------------------|----------|
| 1.32 and below | Not available | `trafficDistribution` field ignored by kube-proxy; standard routing |
| 1.33 | Alpha (feature gate disabled by default) | Falls back to standard routing unless gate is enabled |
| 1.34 | Beta (enabled by default) | Works; may fall back to `PreferSameZone` if network plugin doesn't support `ForNodes` hints |
| 1.35+ | GA | Full support |

**OVN-Kubernetes (OpenShift):** If OVN-K doesn't support `ForNodes` hints in
OCP 4.21 (k8s 1.34), it falls back to `PreferSameZone` semantics. The
`trafficDistribution` field is safe to set regardless — no errors, just
reduced locality.

## Scope

### In scope

- `nodeSelector`, `tolerations`, `topologySpreadConstraints` on `SecondarySpec`
  and `KineSpec`
- Auto `podAntiAffinity` when `replicas > 1`
- `colocateComponents` field with podAffinity + `PreferSameNode`
- Unit tests for resource builders

### Out of scope

- Scheduling fields on `InClusterStorage` (dev-only)
- Shared/top-level scheduling block (YAGNI)
- Custom `affinity` field on specs (operator owns the affinity block)
- Hard anti-affinity option (soft preference is sufficient)
