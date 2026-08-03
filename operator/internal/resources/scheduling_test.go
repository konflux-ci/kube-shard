package resources

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"
)

func TestBuildSecondaryAffinity_SingleReplica_NoColocate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 1
	shard.Spec.ColocateComponents = ptr.To(false)

	affinity := BuildSecondaryAffinity(shard)

	g.Expect(affinity).To(BeNil())
}

func TestBuildSecondaryAffinity_MultiReplica_AntiAffinity(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(false)

	affinity := BuildSecondaryAffinity(shard)

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
	prefs := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	g.Expect(prefs).To(HaveLen(1))
	g.Expect(prefs[0].Weight).To(Equal(int32(100)))
	g.Expect(prefs[0].PodAffinityTerm.TopologyKey).To(Equal("kubernetes.io/hostname"))
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/component", "apiserver"))
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-shard"))
	g.Expect(affinity.PodAffinity).To(BeNil())
}

func TestBuildSecondaryAffinity_MultiReplica_WithColocate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(true)

	affinity := BuildSecondaryAffinity(shard)

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
	g.Expect(affinity.PodAffinity).ToNot(BeNil())
	prefs := affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	g.Expect(prefs).To(HaveLen(1))
	g.Expect(prefs[0].Weight).To(Equal(int32(80)))
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/component", "storage"))
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-shard"))
}

func TestBuildSecondaryAffinity_ColocateDefault_NilMeansTrue(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = nil

	affinity := BuildSecondaryAffinity(shard)

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAffinity).ToNot(BeNil())
}

func TestBuildSecondaryAffinity_SingleReplica_WithColocate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 1
	shard.Spec.ColocateComponents = ptr.To(true)

	affinity := BuildSecondaryAffinity(shard)

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).To(BeNil())
	g.Expect(affinity.PodAffinity).ToNot(BeNil())
}

func TestBuildKineAffinity_SingleReplica(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 1

	affinity := BuildKineAffinity(shard)

	g.Expect(affinity).To(BeNil())
}

func TestBuildKineAffinity_MultiReplica(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 3

	affinity := BuildKineAffinity(shard)

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
	prefs := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	g.Expect(prefs).To(HaveLen(1))
	labels := prefs[0].PodAffinityTerm.LabelSelector.MatchLabels
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/component", "storage"))
	g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-shard"))
	g.Expect(affinity.PodAffinity).To(BeNil())
}
