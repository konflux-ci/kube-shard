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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

// testPrimaryIssuers is the default primary issuer list used across secondary tests.
var testPrimaryIssuers = []string{"https://kubernetes.default.svc"}

func TestBuildSecondaryDeployment_RequestHeaderAllowedNames_Kubernetes(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	allowedNames := []string{"front-proxy-client"}

	deploy := BuildSecondaryDeployment(shard, allowedNames, testPrimaryIssuers)
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

	deploy := BuildSecondaryDeployment(shard, allowedNames, testPrimaryIssuers)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--requestheader-allowed-names=")
	g.Expect(got).To(Equal("--requestheader-allowed-names=kube-apiserver-proxy,system:kube-apiserver-proxy,system:openshift-aggregator"))
}

func TestBuildSecondaryDeployment_DefaultImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal(DefaultSecondaryImage))
}

func TestBuildSecondaryDeployment_CustomImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Secondary.Image = "custom-registry/kube-apiserver:v1.33.0"

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal("custom-registry/kube-apiserver:v1.33.0"))
}

func TestBuildSecondaryDeployment_GracefulShutdown(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

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

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
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

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
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

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
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

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
	podSpec := deploy.Spec.Template.Spec

	g.Expect(podSpec.TopologySpreadConstraints).To(HaveLen(1))
	g.Expect(podSpec.TopologySpreadConstraints[0].TopologyKey).To(Equal("topology.kubernetes.io/zone"))
	g.Expect(podSpec.TopologySpreadConstraints[0].MaxSkew).To(Equal(int32(1)))
}

func TestBuildSecondaryDeployment_RollingUpdateStrategy(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	strategy := deploy.Spec.Strategy
	g.Expect(strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
	g.Expect(strategy.RollingUpdate).ToNot(BeNil())
	g.Expect(*strategy.RollingUpdate.MaxUnavailable).To(Equal(intstr.FromInt32(0)))
	g.Expect(*strategy.RollingUpdate.MaxSurge).To(Equal(intstr.FromInt32(1)))
}

func TestBuildSecondaryDeployment_SecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	podSC := deploy.Spec.Template.Spec.SecurityContext
	g.Expect(podSC).ToNot(BeNil())
	g.Expect(podSC.RunAsNonRoot).To(BeNil(),
		"RunAsNonRoot must be unset — upstream kube-apiserver image USER is 0")
	g.Expect(podSC.RunAsUser).To(BeNil())
	g.Expect(podSC.SeccompProfile).ToNot(BeNil())
	g.Expect(podSC.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

	csc := deploy.Spec.Template.Spec.Containers[0].SecurityContext
	g.Expect(csc).ToNot(BeNil())
	g.Expect(csc.AllowPrivilegeEscalation).ToNot(BeNil())
	g.Expect(*csc.AllowPrivilegeEscalation).To(BeTrue(), "AllowPrivilegeEscalation must be true for kube-apiserver (binary has file capabilities)")
	g.Expect(csc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
	g.Expect(csc.Capabilities.Add).To(ConsistOf(corev1.Capability("NET_BIND_SERVICE")))
}

func TestBuildSecondaryDeployment_TmpVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	var tmpVol *corev1.Volume
	for i := range deploy.Spec.Template.Spec.Volumes {
		if deploy.Spec.Template.Spec.Volumes[i].Name == "tmp" {
			tmpVol = &deploy.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	g.Expect(tmpVol).ToNot(BeNil(), "expected tmp volume")
	g.Expect(tmpVol.EmptyDir).ToNot(BeNil())

	var tmpMount *corev1.VolumeMount
	for i := range deploy.Spec.Template.Spec.Containers[0].VolumeMounts {
		if deploy.Spec.Template.Spec.Containers[0].VolumeMounts[i].MountPath == "/tmp" {
			tmpMount = &deploy.Spec.Template.Spec.Containers[0].VolumeMounts[i]
			break
		}
	}
	g.Expect(tmpMount).ToNot(BeNil(), "expected /tmp volume mount")
}

func TestBuildSecondaryDeployment_DedicatedServiceAccount(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	g.Expect(deploy.Spec.Template.Spec.ServiceAccountName).To(Equal(SecondaryServiceAccountName(shard)))
	g.Expect(deploy.Spec.Template.Spec.ServiceAccountName).ToNot(Equal("default"))
}

func TestBuildSecondaryServiceAccount(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	sa := BuildSecondaryServiceAccount(shard)

	g.Expect(sa.Name).To(Equal(SecondaryServiceAccountName(shard)))
	g.Expect(sa.Namespace).To(Equal(shard.Spec.TargetNamespace))
	g.Expect(sa.Labels).To(HaveKeyWithValue(LabelComponent, ComponentAPIServer))
	g.Expect(sa.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))
}

// TestBuildSecondaryDeployment_EtcdTLSArgs verifies that the secondary
// deployment connects to Kine over HTTPS and includes etcd client TLS flags.
func TestBuildSecondaryDeployment_EtcdTLSArgs(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	etcdServersArg := findArg(args, "--etcd-servers=")
	g.Expect(etcdServersArg).To(HavePrefix("--etcd-servers=https://"),
		"expected etcd-servers to use https")

	g.Expect(findArg(args, "--etcd-certfile=")).To(
		Equal("--etcd-certfile=/etc/kubernetes/etcd-client/tls.crt"))
	g.Expect(findArg(args, "--etcd-keyfile=")).To(
		Equal("--etcd-keyfile=/etc/kubernetes/etcd-client/tls.key"))
	g.Expect(findArg(args, "--etcd-cafile=")).To(
		Equal("--etcd-cafile=/etc/kubernetes/etcd-client/ca.crt"))
}

// TestBuildSecondaryDeployment_EtcdClientVolumeMount verifies that the
// secondary deployment mounts the etcd client certificate Secret.
func TestBuildSecondaryDeployment_EtcdClientVolumeMount(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	var etcdMount *corev1.VolumeMount
	for i := range mounts {
		if mounts[i].Name == "etcd-client-cert" {
			etcdMount = &mounts[i]
			break
		}
	}
	g.Expect(etcdMount).ToNot(BeNil(), "expected etcd-client-cert volume mount")
	g.Expect(etcdMount.MountPath).To(Equal("/etc/kubernetes/etcd-client"))
	g.Expect(etcdMount.ReadOnly).To(BeTrue())

	volumes := deploy.Spec.Template.Spec.Volumes
	var etcdVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "etcd-client-cert" {
			etcdVol = &volumes[i]
			break
		}
	}
	g.Expect(etcdVol).ToNot(BeNil(), "expected etcd-client-cert volume")
	g.Expect(etcdVol.Secret).ToNot(BeNil())
	g.Expect(etcdVol.Secret.SecretName).To(Equal("test-shard-etcd-client-cert"))
}

// TestBuildSecondaryDeployment_EtcdCountMetricPollPeriod verifies that the
// secondary deployment sets --etcd-count-metric-poll-period to avoid per-scrape
// etcd client creation and the resulting gRPC channel churn warnings.
func TestBuildSecondaryDeployment_EtcdCountMetricPollPeriod(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--etcd-count-metric-poll-period=")
	g.Expect(got).To(Equal("--etcd-count-metric-poll-period=60s"))
}

// TestBuildSecondaryDeployment_AuthnTokenWebhook verifies that the secondary
// deployment includes the authentication token webhook config file flag.
func TestBuildSecondaryDeployment_AuthnTokenWebhook(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, testPrimaryIssuers)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--authentication-token-webhook-config-file=")
	g.Expect(got).To(Equal("--authentication-token-webhook-config-file=/etc/kubernetes/auth/authn-webhook-config.yaml"))
}

// TestBuildSecondaryDeployment_APIAudiences verifies that the secondary includes
// both its own issuer and the discovered primary issuer in --api-audiences, so
// that tokens validated via the authentication webhook are accepted.
func TestBuildSecondaryDeployment_APIAudiences(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	customIssuers := []string{"https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"}
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"}, customIssuers)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--api-audiences=")
	g.Expect(got).To(ContainSubstring("https://test-shard-apiserver.test-ns.svc"),
		"should include the secondary's own issuer")
	g.Expect(got).To(ContainSubstring("https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"),
		"should include the discovered primary issuer")
	g.Expect(got).NotTo(ContainSubstring("kubernetes.default.svc"),
		"should not hard-code the default issuer when a custom one is provided")
}
