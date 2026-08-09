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
)

// pkiSecretName returns the name of the Secret holding the shard's CA and
// serving certificates. Duplicated from the certs package to avoid an import
// cycle (certs already imports resources).
func pkiSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-pki", shard.Name)
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
// metrics endpoint (plain HTTP on the metrics port).
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
					Interval: metricsScrapeInterval,
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
			APIVersion: "rbac.authorization.k8s.io/v1",
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
			APIVersion: "rbac.authorization.k8s.io/v1",
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
