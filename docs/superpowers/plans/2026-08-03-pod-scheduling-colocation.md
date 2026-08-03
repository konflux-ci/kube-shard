# Pod Scheduling & Co-location Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add pod scheduling primitives (nodeSelector, tolerations, topologySpreadConstraints), auto anti-affinity, and co-location with topology-aware routing to the APIShard operator.

**Architecture:** CRD types get new scheduling fields on `SecondarySpec` and `KineSpec`, plus a `ColocateComponents` boolean on `APIShardSpec`. Resource builders propagate user fields to PodSpec, auto-inject podAntiAffinity when replicas > 1, and inject podAffinity + `PreferSameNode` on the Kine Service when co-location is enabled.

**Tech Stack:** Go, Kubebuilder, controller-runtime, standard `testing` package

## Global Constraints

- Go module: `github.com/konflux-ci/kube-shard`
- CRD types: `operator/api/v1alpha1/apishard_types.go`
- After CRD type changes, run `make manifests generate` from `operator/`
- Tests run via `make test` from `operator/`
- All new fields are optional (pointer or nil-able)
- `ColocateComponents` uses `*bool` to distinguish nil (default true) from explicit false
- Design spec: `docs/superpowers/specs/2026-08-03-pod-scheduling-and-colocation-design.md`

---

### Task 1: Add scheduling fields to CRD types

**Files:**
- Modify: `operator/api/v1alpha1/apishard_types.go`

**Interfaces:**
- Produces: `SecondarySpec.NodeSelector`, `SecondarySpec.Tolerations`, `SecondarySpec.TopologySpreadConstraints`, `KineSpec.NodeSelector`, `KineSpec.Tolerations`, `KineSpec.TopologySpreadConstraints`, `APIShardSpec.ColocateComponents`

- [ ] **Step 1: Add scheduling fields to SecondarySpec**

In `operator/api/v1alpha1/apishard_types.go`, add three fields after `Resources` in `SecondarySpec`:

```go
type SecondarySpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}
```

- [ ] **Step 2: Add scheduling fields to KineSpec**

In the same file, add three fields after `Resources` in `KineSpec` (before the existing `ConnectionPool` field):

```go
type KineSpec struct {
	// +kubebuilder:default=1
	Replicas  int32                       `json:"replicas,omitempty"`
	Image     string                      `json:"image,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// ... existing ConnectionPool, Compaction, PollBatchSize,
	//     WatchProgressNotifyInterval fields remain unchanged ...
}
```

- [ ] **Step 3: Add ColocateComponents to APIShardSpec**

In the same file, add the field to `APIShardSpec` after `ForceAggregation`:

```go
type APIShardSpec struct {
	// ... existing fields ...
	// +kubebuilder:default=true
	ForceAggregation bool `json:"forceAggregation,omitempty"`

	// +optional
	// +kubebuilder:default=true
	ColocateComponents *bool `json:"colocateComponents,omitempty"`
}
```

- [ ] **Step 4: Regenerate CRD manifests and deepcopy**

Run from `operator/`:

```bash
make manifests generate
```

Expected: no errors. Files updated under `config/crd/bases/` and `api/v1alpha1/zz_generated.deepcopy.go`.

- [ ] **Step 5: Verify tests still pass**

Run from `operator/`:

```bash
make test
```

Expected: all existing tests pass (no behavioral changes yet).

- [ ] **Step 6: Commit**

```bash
git add operator/api/ operator/config/
git commit -m "Add scheduling and co-location fields to APIShard CRD types

Add nodeSelector, tolerations, topologySpreadConstraints to SecondarySpec
and KineSpec. Add colocateComponents (*bool, default true) to APIShardSpec.

Refs: #7, #9

Assisted-by: Cursor"
```

---

### Task 2: Add a scheduling helper to build affinity rules

**Files:**
- Create: `operator/internal/resources/scheduling.go`
- Create: `operator/internal/resources/scheduling_test.go`

**Interfaces:**
- Consumes: `kubeshardv1alpha1.APIShard` (shard name, replicas, colocateComponents)
- Produces: `BuildSecondaryAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity`, `BuildKineAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity`

- [ ] **Step 1: Write the failing tests**

Create `operator/internal/resources/scheduling_test.go`:

```go
package resources

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func TestBuildSecondaryAffinity_SingleReplica_NoColocate(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 1
	shard.Spec.ColocateComponents = ptr.To(false)

	affinity := BuildSecondaryAffinity(shard)

	if affinity != nil {
		t.Error("expected nil affinity for single replica without co-location")
	}
}

func TestBuildSecondaryAffinity_MultiReplica_AntiAffinity(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(false)

	affinity := BuildSecondaryAffinity(shard)

	if affinity == nil {
		t.Fatal("expected non-nil affinity for multi-replica")
	}
	if affinity.PodAntiAffinity == nil {
		t.Fatal("expected podAntiAffinity")
	}
	prefs := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(prefs) != 1 {
		t.Fatalf("expected 1 anti-affinity rule, got %d", len(prefs))
	}
	if prefs[0].Weight != 100 {
		t.Errorf("anti-affinity weight = %d, want 100", prefs[0].Weight)
	}
	if prefs[0].PodAffinityTerm.TopologyKey != "kubernetes.io/hostname" {
		t.Errorf("topologyKey = %q, want kubernetes.io/hostname", prefs[0].PodAffinityTerm.TopologyKey)
	}
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	if labels["app.kubernetes.io/component"] != "apiserver" {
		t.Errorf("component label = %q, want apiserver", labels["app.kubernetes.io/component"])
	}
	if affinity.PodAffinity != nil {
		t.Error("expected no podAffinity when colocate is false")
	}
}

func TestBuildSecondaryAffinity_MultiReplica_WithColocate(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(true)

	affinity := BuildSecondaryAffinity(shard)

	if affinity == nil {
		t.Fatal("expected non-nil affinity")
	}
	if affinity.PodAntiAffinity == nil {
		t.Fatal("expected podAntiAffinity")
	}
	if affinity.PodAffinity == nil {
		t.Fatal("expected podAffinity for co-location")
	}
	prefs := affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(prefs) != 1 {
		t.Fatalf("expected 1 co-location rule, got %d", len(prefs))
	}
	if prefs[0].Weight != 80 {
		t.Errorf("co-location weight = %d, want 80", prefs[0].Weight)
	}
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	if labels["app.kubernetes.io/component"] != "storage" {
		t.Errorf("component label = %q, want storage", labels["app.kubernetes.io/component"])
	}
}

func TestBuildSecondaryAffinity_ColocateDefault_NilMeansTrue(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = nil

	affinity := BuildSecondaryAffinity(shard)

	if affinity == nil || affinity.PodAffinity == nil {
		t.Error("expected co-location affinity when ColocateComponents is nil (default true)")
	}
}

func TestBuildSecondaryAffinity_SingleReplica_WithColocate(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 1
	shard.Spec.ColocateComponents = ptr.To(true)

	affinity := BuildSecondaryAffinity(shard)

	if affinity == nil {
		t.Fatal("expected non-nil affinity for co-location even with 1 replica")
	}
	if affinity.PodAntiAffinity != nil {
		t.Error("expected no anti-affinity for single replica")
	}
	if affinity.PodAffinity == nil {
		t.Fatal("expected podAffinity for co-location")
	}
}

func TestBuildKineAffinity_SingleReplica(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 1

	affinity := BuildKineAffinity(shard)

	if affinity != nil {
		t.Error("expected nil affinity for single kine replica")
	}
}

func TestBuildKineAffinity_MultiReplica(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 3

	affinity := BuildKineAffinity(shard)

	if affinity == nil {
		t.Fatal("expected non-nil affinity for multi-replica kine")
	}
	if affinity.PodAntiAffinity == nil {
		t.Fatal("expected podAntiAffinity")
	}
	prefs := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(prefs) != 1 {
		t.Fatalf("expected 1 anti-affinity rule, got %d", len(prefs))
	}
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	if labels["app.kubernetes.io/component"] != "storage" {
		t.Errorf("component label = %q, want storage", labels["app.kubernetes.io/component"])
	}
	if affinity.PodAffinity != nil {
		t.Error("expected no podAffinity on kine (only apiserver seeks kine)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run from `operator/`:

```bash
go test ./internal/resources/ -run TestBuild.*Affinity -v
```

Expected: compilation error — `BuildSecondaryAffinity` and `BuildKineAffinity` not defined.

- [ ] **Step 3: Implement the scheduling helpers**

Create `operator/internal/resources/scheduling.go`:

```go
package resources

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func isColocateEnabled(shard *kubeshardv1alpha1.APIShard) bool {
	return shard.Spec.ColocateComponents == nil || *shard.Spec.ColocateComponents
}

func BuildSecondaryAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity {
	replicas := shard.Spec.Secondary.Replicas
	if replicas == 0 {
		replicas = 1
	}
	colocate := isColocateEnabled(shard)

	if replicas <= 1 && !colocate {
		return nil
	}

	affinity := &corev1.Affinity{}

	if replicas > 1 {
		affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "apiserver",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		}
	}

	if colocate {
		affinity.PodAffinity = &corev1.PodAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 80,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "storage",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		}
	}

	return affinity
}

func BuildKineAffinity(shard *kubeshardv1alpha1.APIShard) *corev1.Affinity {
	replicas := shard.Spec.Kine.Replicas
	if replicas == 0 {
		replicas = 1
	}

	if replicas <= 1 {
		return nil
	}

	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app.kubernetes.io/instance":  shard.Name,
								"app.kubernetes.io/component": "storage",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run from `operator/`:

```bash
go test ./internal/resources/ -run TestBuild.*Affinity -v
```

Expected: all 7 tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator/internal/resources/scheduling.go operator/internal/resources/scheduling_test.go
git commit -m "Add scheduling helper functions for affinity rules

BuildSecondaryAffinity injects podAntiAffinity (replicas > 1) and
podAffinity for co-location (colocateComponents). BuildKineAffinity
injects podAntiAffinity only.

Refs: #7, #9

Assisted-by: Cursor"
```

---

### Task 3: Wire scheduling into resource builders

**Files:**
- Modify: `operator/internal/resources/secondary.go:140` (PodSpec in BuildSecondaryDeployment)
- Modify: `operator/internal/resources/kine.go:141` (PodSpec in BuildKineDeployment)
- Modify: `operator/internal/resources/kine.go:187` (BuildKineService — add trafficDistribution)
- Modify: `operator/internal/resources/secondary_test.go` (add tests)
- Create: `operator/internal/resources/kine_test.go` (add tests)

**Interfaces:**
- Consumes: `BuildSecondaryAffinity`, `BuildKineAffinity`, `isColocateEnabled` from Task 2; scheduling fields from `SecondarySpec` and `KineSpec` from Task 1
- Produces: Updated `BuildSecondaryDeployment`, `BuildKineDeployment`, `BuildKineService` with scheduling support

- [ ] **Step 1: Write tests for secondary scheduling**

Append to `operator/internal/resources/secondary_test.go`:

```go
func TestBuildSecondaryDeployment_SchedulingFields(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.NodeSelector = map[string]string{
		"node-role.kubernetes.io/infra": "",
	}
	shard.Spec.Secondary.Tolerations = []corev1.Toleration{
		{
			Key:      "node-role.kubernetes.io/infra",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	podSpec := deploy.Spec.Template.Spec

	if podSpec.NodeSelector == nil {
		t.Fatal("expected nodeSelector to be set")
	}
	if _, ok := podSpec.NodeSelector["node-role.kubernetes.io/infra"]; !ok {
		t.Error("expected infra node selector")
	}
	if len(podSpec.Tolerations) != 1 {
		t.Fatalf("expected 1 toleration, got %d", len(podSpec.Tolerations))
	}
	if podSpec.Tolerations[0].Key != "node-role.kubernetes.io/infra" {
		t.Errorf("toleration key = %q, want node-role.kubernetes.io/infra", podSpec.Tolerations[0].Key)
	}
}

func TestBuildSecondaryDeployment_AntiAffinityInjected(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(false)

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	affinity := deploy.Spec.Template.Spec.Affinity

	if affinity == nil {
		t.Fatal("expected affinity to be set for replicas > 1")
	}
	if affinity.PodAntiAffinity == nil {
		t.Error("expected podAntiAffinity")
	}
	if affinity.PodAffinity != nil {
		t.Error("expected no podAffinity when colocate is false")
	}
}

func TestBuildSecondaryDeployment_ColocateAffinity(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(true)

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	affinity := deploy.Spec.Template.Spec.Affinity

	if affinity == nil {
		t.Fatal("expected affinity to be set")
	}
	if affinity.PodAntiAffinity == nil {
		t.Error("expected podAntiAffinity")
	}
	if affinity.PodAffinity == nil {
		t.Error("expected podAffinity for co-location")
	}
}
```

Add `corev1 "k8s.io/api/core/v1"` and `"k8s.io/utils/ptr"` to the import block of `secondary_test.go`.

- [ ] **Step 2: Write tests for kine scheduling and traffic distribution**

Create `operator/internal/resources/kine_test.go`:

```go
package resources

import (
	"testing"

	"k8s.io/utils/ptr"
)

func TestBuildKineDeployment_SchedulingFields(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.NodeSelector = map[string]string{
		"node-role.kubernetes.io/infra": "",
	}

	deploy := BuildKineDeployment(shard)
	podSpec := deploy.Spec.Template.Spec

	if podSpec.NodeSelector == nil {
		t.Fatal("expected nodeSelector to be set")
	}
	if _, ok := podSpec.NodeSelector["node-role.kubernetes.io/infra"]; !ok {
		t.Error("expected infra node selector")
	}
}

func TestBuildKineDeployment_AntiAffinityInjected(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 3

	deploy := BuildKineDeployment(shard)
	affinity := deploy.Spec.Template.Spec.Affinity

	if affinity == nil {
		t.Fatal("expected affinity for multi-replica kine")
	}
	if affinity.PodAntiAffinity == nil {
		t.Error("expected podAntiAffinity")
	}
}

func TestBuildKineDeployment_NoAffinitySingleReplica(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 1

	deploy := BuildKineDeployment(shard)
	affinity := deploy.Spec.Template.Spec.Affinity

	if affinity != nil {
		t.Error("expected no affinity for single replica")
	}
}

func TestBuildKineService_PreferSameNode_Enabled(t *testing.T) {
	shard := newTestShard()
	shard.Spec.ColocateComponents = ptr.To(true)

	svc := BuildKineService(shard)

	want := "PreferSameNode"
	if svc.Spec.TrafficDistribution == nil || *svc.Spec.TrafficDistribution != want {
		got := "<nil>"
		if svc.Spec.TrafficDistribution != nil {
			got = *svc.Spec.TrafficDistribution
		}
		t.Errorf("trafficDistribution = %s, want %s", got, want)
	}
}

func TestBuildKineService_PreferSameNode_Disabled(t *testing.T) {
	shard := newTestShard()
	shard.Spec.ColocateComponents = ptr.To(false)

	svc := BuildKineService(shard)

	if svc.Spec.TrafficDistribution != nil {
		t.Errorf("trafficDistribution = %q, want nil when colocate is false", *svc.Spec.TrafficDistribution)
	}
}

func TestBuildKineService_PreferSameNode_Default(t *testing.T) {
	shard := newTestShard()
	shard.Spec.ColocateComponents = nil

	svc := BuildKineService(shard)

	want := "PreferSameNode"
	if svc.Spec.TrafficDistribution == nil || *svc.Spec.TrafficDistribution != want {
		t.Errorf("trafficDistribution should default to %s when ColocateComponents is nil", want)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run from `operator/`:

```bash
go test ./internal/resources/ -v
```

Expected: new tests fail (scheduling fields not wired yet, `BuildKineService` doesn't accept shard for colocate check).

- [ ] **Step 4: Wire scheduling into BuildSecondaryDeployment**

In `operator/internal/resources/secondary.go`, modify the `PodSpec` section of `BuildSecondaryDeployment` (around line 140) to add the scheduling fields:

```go
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(int64(65)),
					NodeSelector:              shard.Spec.Secondary.NodeSelector,
					Tolerations:               shard.Spec.Secondary.Tolerations,
					TopologySpreadConstraints: shard.Spec.Secondary.TopologySpreadConstraints,
					Affinity:                  BuildSecondaryAffinity(shard),
					Containers: []corev1.Container{
```

- [ ] **Step 5: Wire scheduling into BuildKineDeployment**

In `operator/internal/resources/kine.go`, modify the `PodSpec` section of `BuildKineDeployment` (around line 141) to add scheduling fields:

```go
				Spec: corev1.PodSpec{
					NodeSelector:              shard.Spec.Kine.NodeSelector,
					Tolerations:               shard.Spec.Kine.Tolerations,
					TopologySpreadConstraints: shard.Spec.Kine.TopologySpreadConstraints,
					Affinity:                  BuildKineAffinity(shard),
					Containers: []corev1.Container{
```

- [ ] **Step 6: Add trafficDistribution to BuildKineService**

In `operator/internal/resources/kine.go`, modify `BuildKineService` to conditionally set `trafficDistribution`:

```go
func BuildKineService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := KineServiceName(shard)
	labels := map[string]string{
		"app.kubernetes.io/name":       "kine",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "storage",
	}

	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       int32(KinePort),
					TargetPort: intstr.FromInt32(int32(KinePort)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if isColocateEnabled(shard) {
		svc.Spec.TrafficDistribution = ptr.To("PreferSameNode")
	}

	return svc
}
```

Add `"k8s.io/utils/ptr"` to imports in `kine.go` (it's already imported in `secondary.go`).

- [ ] **Step 7: Run all tests**

Run from `operator/`:

```bash
go test ./internal/resources/ -v
```

Expected: all tests pass (old + new).

- [ ] **Step 8: Run full operator test suite**

Run from `operator/`:

```bash
make test
```

Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add operator/internal/resources/
git commit -m "Wire scheduling fields and affinity into resource builders

SecondaryDeployment and KineDeployment now propagate nodeSelector,
tolerations, topologySpreadConstraints from the CR spec. Auto
podAntiAffinity is injected when replicas > 1. Co-location podAffinity
and PreferSameNode traffic distribution are set when colocateComponents
is enabled.

Refs: #7, #9

Assisted-by: Cursor"
```
