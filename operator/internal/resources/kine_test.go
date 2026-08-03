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
