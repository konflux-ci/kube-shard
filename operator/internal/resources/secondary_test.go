/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func newTestShard() *kubeshardv1alpha1.APIShard {
	return &kubeshardv1alpha1.APIShard{
		ObjectMeta: metav1.ObjectMeta{Name: "test-shard"},
		Spec: kubeshardv1alpha1.APIShardSpec{
			TargetNamespace: "test-ns",
			Secondary: kubeshardv1alpha1.SecondarySpec{
				Replicas: 1,
			},
			Kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
			},
		},
	}
}

func findArg(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}

func TestBuildSecondaryDeployment_RequestHeaderAllowedNames_Kubernetes(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	allowedNames := []string{"front-proxy-client"}

	deploy := BuildSecondaryDeployment(shard, allowedNames)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--requestheader-allowed-names=")
	g.Expect(got).To(Equal("--requestheader-allowed-names=front-proxy-client"))
}

func TestBuildSecondaryDeployment_RequestHeaderAllowedNames_OpenShift(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	allowedNames := []string{
		"kube-apiserver-proxy",
		"system:kube-apiserver-proxy",
		"system:openshift-aggregator",
	}

	deploy := BuildSecondaryDeployment(shard, allowedNames)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--requestheader-allowed-names=")
	g.Expect(got).To(Equal("--requestheader-allowed-names=kube-apiserver-proxy,system:kube-apiserver-proxy,system:openshift-aggregator"))
}

func TestBuildSecondaryDeployment_DefaultImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal(DefaultSecondaryImage))
}

func TestBuildSecondaryDeployment_CustomImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Image = "custom-registry/kube-apiserver:v1.33.0"

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal("custom-registry/kube-apiserver:v1.33.0"))
}

func TestBuildSecondaryDeployment_GracefulShutdown(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	args := deploy.Spec.Template.Spec.Containers[0].Args

	g.Expect(findArg(args, "--shutdown-delay-duration=")).To(Equal("--shutdown-delay-duration=15s"))
	g.Expect(findArg(args, "--shutdown-send-retry-after=")).To(Equal("--shutdown-send-retry-after=true"))

	podSpec := deploy.Spec.Template.Spec
	g.Expect(podSpec.TerminationGracePeriodSeconds).ToNot(BeNil())
	g.Expect(*podSpec.TerminationGracePeriodSeconds).To(Equal(int64(65)))
}

func TestBuildSecondaryDeployment_SchedulingFields(t *testing.T) {
	g := NewGomegaWithT(t)
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

	g.Expect(podSpec.NodeSelector).ToNot(BeNil())
	g.Expect(podSpec.NodeSelector).To(HaveKey("node-role.kubernetes.io/infra"))
	g.Expect(podSpec.Tolerations).To(HaveLen(1))
	g.Expect(podSpec.Tolerations[0].Key).To(Equal("node-role.kubernetes.io/infra"))
}

func TestBuildSecondaryDeployment_AntiAffinityInjected(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(false)

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	affinity := deploy.Spec.Template.Spec.Affinity

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
	g.Expect(affinity.PodAffinity).To(BeNil())
}

func TestBuildSecondaryDeployment_ColocateAffinity(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Replicas = 3
	shard.Spec.ColocateComponents = ptr.To(true)

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	affinity := deploy.Spec.Template.Spec.Affinity

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
	g.Expect(affinity.PodAffinity).ToNot(BeNil())
}

func TestBuildSecondaryDeployment_TopologySpreadConstraints(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/component": "apiserver"},
			},
		},
	}

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})
	podSpec := deploy.Spec.Template.Spec

	g.Expect(podSpec.TopologySpreadConstraints).To(HaveLen(1))
	g.Expect(podSpec.TopologySpreadConstraints[0].TopologyKey).To(Equal("topology.kubernetes.io/zone"))
	g.Expect(podSpec.TopologySpreadConstraints[0].MaxSkew).To(Equal(int32(1)))
}
