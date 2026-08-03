package resources

import (
	"testing"

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
