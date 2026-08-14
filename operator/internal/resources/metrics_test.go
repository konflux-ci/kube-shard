package resources

import (
	"testing"

	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// TestBuildKineServiceMonitor verifies selectors and endpoint config.
func TestBuildKineServiceMonitor(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	sm := BuildKineServiceMonitor(shard)

	g.Expect(sm.Name).To(Equal("test-shard-kine-metrics"))
	g.Expect(sm.Namespace).To(Equal("test-ns"))
	g.Expect(sm.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))
	g.Expect(sm.Labels).To(HaveKeyWithValue(LabelInstance, "test-shard"))

	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelName, NameKine))
	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelInstance, "test-shard"))

	g.Expect(sm.Spec.Endpoints).To(HaveLen(1))
	ep := sm.Spec.Endpoints[0]
	g.Expect(ep.Port).To(Equal("metrics"))
	g.Expect(ep.Path).To(Equal("/metrics"))
	g.Expect(ep.Interval).To(Equal(monitoringv1.Duration("30s")))
}

// TestBuildSecondaryServiceMonitor verifies HTTPS, bearer token auth,
// and TLS config on the apiserver ServiceMonitor.
func TestBuildSecondaryServiceMonitor(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	sm := BuildSecondaryServiceMonitor(shard)

	g.Expect(sm.Name).To(Equal("test-shard-apiserver-metrics"))
	g.Expect(sm.Namespace).To(Equal("test-ns"))
	g.Expect(sm.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))

	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelName, NameAPIServer))
	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelInstance, "test-shard"))

	g.Expect(sm.Spec.Endpoints).To(HaveLen(1))
	ep := sm.Spec.Endpoints[0]
	g.Expect(ep.Port).To(Equal("https"))
	g.Expect(ep.Path).To(Equal("/metrics"))
	g.Expect(ep.Scheme).To(Equal(ptr.To(monitoringv1.SchemeHTTPS)))
	g.Expect(ep.Authorization).ToNot(BeNil())
	g.Expect(ep.Authorization.Credentials.Name).To(Equal("test-shard-metrics-reader-token"))
	g.Expect(ep.Authorization.Credentials.Key).To(Equal("token"))
	g.Expect(ep.TLSConfig).ToNot(BeNil())
	g.Expect(ep.TLSConfig.InsecureSkipVerify).To(BeNil())
	g.Expect(ep.TLSConfig.CA.Secret).ToNot(BeNil())
	g.Expect(ep.TLSConfig.CA.Secret.Name).To(Equal("test-shard-pki"))
	g.Expect(ep.TLSConfig.CA.Secret.Key).To(Equal("ca.crt"))
	g.Expect(ep.TLSConfig.ServerName).To(Equal(ptr.To("test-shard-apiserver.test-ns.svc")))
}

// TestBuildMetricsReaderServiceAccount verifies the metrics-reader ServiceAccount name and namespace.
func TestBuildMetricsReaderServiceAccount(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	sa := BuildMetricsReaderServiceAccount(shard)

	g.Expect(sa.Name).To(Equal("test-shard-metrics-reader"))
	g.Expect(sa.Namespace).To(Equal("test-ns"))
}

// TestBuildMetricsReaderClusterRole verifies the shared ClusterRole grants GET on /metrics.
func TestBuildMetricsReaderClusterRole(t *testing.T) {
	g := NewGomegaWithT(t)

	cr := BuildMetricsReaderClusterRole()

	g.Expect(cr.Name).To(Equal(MetricsReaderClusterRoleName))
	g.Expect(cr.Rules).To(HaveLen(1))
	g.Expect(cr.Rules[0].NonResourceURLs).To(ContainElement("/metrics"))
	g.Expect(cr.Rules[0].Verbs).To(ContainElement("get"))
}

// TestBuildMetricsReaderClusterRoleBinding verifies the per-shard ClusterRoleBinding references the correct role and subject.
func TestBuildMetricsReaderClusterRoleBinding(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	crb := BuildMetricsReaderClusterRoleBinding(shard)

	g.Expect(crb.Name).To(Equal("test-shard-metrics-reader"))
	g.Expect(crb.RoleRef).To(Equal(rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "ClusterRole",
		Name:     MetricsReaderClusterRoleName,
	}))
	g.Expect(crb.Subjects).To(HaveLen(1))
	g.Expect(crb.Subjects[0].Kind).To(Equal("ServiceAccount"))
	g.Expect(crb.Subjects[0].Name).To(Equal("test-shard-metrics-reader"))
	g.Expect(crb.Subjects[0].Namespace).To(Equal("test-ns"))
}

// TestBuildMetricsReaderTokenSecret verifies the token Secret type and ServiceAccount annotation.
func TestBuildMetricsReaderTokenSecret(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	secret := BuildMetricsReaderTokenSecret(shard)

	g.Expect(secret.Name).To(Equal("test-shard-metrics-reader-token"))
	g.Expect(secret.Namespace).To(Equal("test-ns"))
	g.Expect(secret.Type).To(Equal(corev1.SecretTypeServiceAccountToken))
	g.Expect(secret.Annotations).To(HaveKeyWithValue(
		"kubernetes.io/service-account.name", "test-shard-metrics-reader",
	))
}

// TestBuildPrometheusDiscoveryRole verifies the Role grants read access for service discovery.
func TestBuildPrometheusDiscoveryRole(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	role := BuildPrometheusDiscoveryRole(shard)

	g.Expect(role.Name).To(Equal("test-shard-prometheus-discovery"))
	g.Expect(role.Namespace).To(Equal("test-ns"))
	g.Expect(role.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))
	g.Expect(role.Labels).To(HaveKeyWithValue(LabelInstance, "test-shard"))
	g.Expect(role.Rules).To(HaveLen(2))

	g.Expect(role.Rules[0].APIGroups).To(ContainElement(""))
	g.Expect(role.Rules[0].Resources).To(ConsistOf("services", "endpoints", "pods"))
	g.Expect(role.Rules[0].Verbs).To(ConsistOf("get", "list", "watch"))

	g.Expect(role.Rules[1].APIGroups).To(ContainElement("monitoring.coreos.com"))
	g.Expect(role.Rules[1].Resources).To(ConsistOf("servicemonitors"))
	g.Expect(role.Rules[1].Verbs).To(ConsistOf("get", "list", "watch"))
}

// TestBuildPrometheusDiscoveryRoleBinding verifies the RoleBinding references the correct Role and subject.
func TestBuildPrometheusDiscoveryRoleBinding(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	rb := BuildPrometheusDiscoveryRoleBinding(shard)

	g.Expect(rb.Name).To(Equal("test-shard-prometheus-discovery"))
	g.Expect(rb.Namespace).To(Equal("test-ns"))
	g.Expect(rb.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))
	g.Expect(rb.Labels).To(HaveKeyWithValue(LabelInstance, "test-shard"))
	g.Expect(rb.RoleRef).To(Equal(rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     "test-shard-prometheus-discovery",
	}))
	g.Expect(rb.Subjects).To(HaveLen(1))
	g.Expect(rb.Subjects[0].Kind).To(Equal("ServiceAccount"))
	g.Expect(rb.Subjects[0].Name).To(Equal("prometheus-k8s"))
	g.Expect(rb.Subjects[0].Namespace).To(Equal("openshift-monitoring"))
}

// TestBuildPrometheusDiscoveryRoleBindingCustomSA verifies that custom Prometheus SA config is respected.
func TestBuildPrometheusDiscoveryRoleBindingCustomSA(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Monitoring = &kubeshardv1alpha1.MonitoringConfig{
		PrometheusServiceAccountName:      "custom-prometheus",
		PrometheusServiceAccountNamespace: "monitoring",
	}

	rb := BuildPrometheusDiscoveryRoleBinding(shard)

	g.Expect(rb.Subjects).To(HaveLen(1))
	g.Expect(rb.Subjects[0].Name).To(Equal("custom-prometheus"))
	g.Expect(rb.Subjects[0].Namespace).To(Equal("monitoring"))
}
