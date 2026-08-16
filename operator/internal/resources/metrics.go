package resources

import (
	"fmt"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	MetricsReaderClusterRoleName = "kube-shard-metrics-reader"
	metricsScrapeInterval        = monitoringv1.Duration("30s")
	rbacAPIVersion               = "rbac.authorization.k8s.io/v1"
)

// pkiSecretName returns the name of the Secret holding the shard's CA and
// serving certificates. Duplicated from the certs package to avoid an import
// cycle (certs already imports resources).
func pkiSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-pki", shard.Name)
}

// kineServingSecretName returns the name of the Secret holding the Kine serving
// certificate (issued by the Kine-dedicated CA). Duplicated from the certs
// package to avoid an import cycle.
func kineServingSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine-serving-cert", shard.Name)
}

// MetricsReaderServiceAccountName returns the name of the per-shard ServiceAccount
// used by Prometheus to authenticate against the secondary apiserver's /metrics endpoint.
func MetricsReaderServiceAccountName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-metrics-reader", shard.Name)
}

// MetricsReaderTokenSecretName returns the name of the Secret containing the
// bearer token for the metrics-reader ServiceAccount.
func MetricsReaderTokenSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-metrics-reader-token", shard.Name)
}

// BuildKineServiceMonitor constructs a ServiceMonitor that scrapes the Kine
// metrics endpoint over HTTPS (Kine serves TLS on all ports when certs are configured).
func BuildKineServiceMonitor(shard *kubeshardv1alpha1.APIShard) *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "ServiceMonitor",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kine-metrics", shard.Name),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					LabelName:     NameKine,
					LabelInstance: shard.Name,
				},
			},
			Endpoints: []monitoringv1.Endpoint{
				{
					Port:     "metrics",
					Path:     "/metrics",
					Scheme:   ptr.To(monitoringv1.SchemeHTTPS),
					Interval: metricsScrapeInterval,
					HTTPConfigWithProxyAndTLSFiles: monitoringv1.HTTPConfigWithProxyAndTLSFiles{
						HTTPConfigWithTLSFiles: monitoringv1.HTTPConfigWithTLSFiles{
							TLSConfig: &monitoringv1.TLSConfig{
								SafeTLSConfig: monitoringv1.SafeTLSConfig{
									CA: monitoringv1.SecretOrConfigMap{
										Secret: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: kineServingSecretName(shard),
											},
											Key: "ca.crt",
										},
									},
									ServerName: ptr.To(fmt.Sprintf(
										"%s.%s.svc",
										KineServiceName(shard),
										shard.Spec.TargetNamespace,
									)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// BuildSecondaryServiceMonitor constructs a ServiceMonitor that scrapes the
// secondary kube-apiserver's /metrics endpoint over HTTPS with bearer token auth.
func BuildSecondaryServiceMonitor(shard *kubeshardv1alpha1.APIShard) *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "ServiceMonitor",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-apiserver-metrics", shard.Name),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					LabelName:     NameAPIServer,
					LabelInstance: shard.Name,
				},
			},
			Endpoints: []monitoringv1.Endpoint{
				{
					Port:     "https",
					Path:     "/metrics",
					Scheme:   ptr.To(monitoringv1.SchemeHTTPS),
					Interval: metricsScrapeInterval,
					HTTPConfigWithProxyAndTLSFiles: monitoringv1.HTTPConfigWithProxyAndTLSFiles{
						HTTPConfigWithTLSFiles: monitoringv1.HTTPConfigWithTLSFiles{
							HTTPConfigWithoutTLS: monitoringv1.HTTPConfigWithoutTLS{
								Authorization: &monitoringv1.SafeAuthorization{
									Type: "Bearer",
									Credentials: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: MetricsReaderTokenSecretName(shard),
										},
										Key: "token",
									},
								},
							},
							TLSConfig: &monitoringv1.TLSConfig{
								SafeTLSConfig: monitoringv1.SafeTLSConfig{
									CA: monitoringv1.SecretOrConfigMap{
										Secret: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: pkiSecretName(shard),
											},
											Key: "ca.crt",
										},
									},
									ServerName: ptr.To(fmt.Sprintf(
										"%s.%s.svc",
										SecondaryServiceName(shard),
										shard.Spec.TargetNamespace,
									)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// BuildMetricsReaderServiceAccount constructs the ServiceAccount that Prometheus
// uses to authenticate against the secondary apiserver's /metrics endpoint.
func BuildMetricsReaderServiceAccount(shard *kubeshardv1alpha1.APIShard) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      MetricsReaderServiceAccountName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
	}
}

// BuildMetricsReaderClusterRole constructs the shared ClusterRole granting GET
// access to the /metrics nonResourceURL. This is shared across all shards.
func BuildMetricsReaderClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacAPIVersion,
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: MetricsReaderClusterRoleName,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				NonResourceURLs: []string{"/metrics"},
				Verbs:           []string{"get"},
			},
		},
	}
}

// BuildMetricsReaderClusterRoleBinding constructs the per-shard ClusterRoleBinding
// that binds the metrics-reader ServiceAccount to the shared ClusterRole.
func BuildMetricsReaderClusterRoleBinding(shard *kubeshardv1alpha1.APIShard) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacAPIVersion,
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-metrics-reader", shard.Name),
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     MetricsReaderClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      MetricsReaderServiceAccountName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
		},
	}
}

// BuildMetricsReaderTokenSecret constructs a Secret of type
// kubernetes.io/service-account-token that the token controller populates with
// a bearer token for the metrics-reader ServiceAccount.
func BuildMetricsReaderTokenSecret(shard *kubeshardv1alpha1.APIShard) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      MetricsReaderTokenSecretName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": MetricsReaderServiceAccountName(shard),
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
}

// PrometheusServiceAccount returns the Prometheus ServiceAccount name and
// namespace for the given shard, falling back to OpenShift defaults when the
// monitoring section is unset or fields are empty.
func PrometheusServiceAccount(shard *kubeshardv1alpha1.APIShard) (name, namespace string) {
	name = "prometheus-k8s"
	namespace = "openshift-monitoring"
	if shard.Spec.Monitoring != nil {
		if shard.Spec.Monitoring.PrometheusServiceAccountName != "" {
			name = shard.Spec.Monitoring.PrometheusServiceAccountName
		}
		if shard.Spec.Monitoring.PrometheusNamespace != "" {
			namespace = shard.Spec.Monitoring.PrometheusNamespace
		}
	}
	return
}

// prometheusDiscoveryRoleName returns the name of the Prometheus discovery Role
// for the given shard.
func prometheusDiscoveryRoleName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-prometheus-discovery", shard.Name)
}

// BuildPrometheusDiscoveryRole constructs a Role in the target namespace granting
// read access to the resources Prometheus needs for service discovery.
func BuildPrometheusDiscoveryRole(shard *kubeshardv1alpha1.APIShard) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacAPIVersion,
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusDiscoveryRoleName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"services", "endpoints", "pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"monitoring.coreos.com"},
				Resources: []string{"servicemonitors"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

// BuildPrometheusDiscoveryRoleBinding constructs a RoleBinding in the target
// namespace that binds the Prometheus discovery Role to the configured Prometheus
// ServiceAccount.
func BuildPrometheusDiscoveryRoleBinding(shard *kubeshardv1alpha1.APIShard) *rbacv1.RoleBinding {
	saName, saNamespace := PrometheusServiceAccount(shard)
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacAPIVersion,
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusDiscoveryRoleName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels: map[string]string{
				LabelManagedBy: ManagedByValue,
				LabelInstance:  shard.Name,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     prometheusDiscoveryRoleName(shard),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: saNamespace,
			},
		},
	}
}
