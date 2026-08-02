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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	DefaultSecondaryImage = "registry.k8s.io/kube-apiserver:v1.36.2"
	SecondaryPort         = 6443
)

func SecondaryDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

func SecondaryServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

func SecondaryEndpoint(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("https://%s.%s.svc",
		SecondaryServiceName(shard),
		shard.Spec.TargetNamespace,
	)
}

func BuildSecondaryDeployment(shard *kubeshardv1alpha1.APIShard) *appsv1.Deployment {
	name := SecondaryDeploymentName(shard)
	image := shard.Spec.Secondary.Image
	if image == "" {
		image = DefaultSecondaryImage
	}
	replicas := shard.Spec.Secondary.Replicas
	if replicas == 0 {
		replicas = 1
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "kube-apiserver",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "apiserver",
	}

	kineSvc := KineServiceName(shard)
	etcdServers := fmt.Sprintf("http://%s.%s.svc:%d", kineSvc, shard.Spec.TargetNamespace, KinePort)

	args := []string{
		// Kine endpoint emulating the etcd v3 API over SQLite/PostgreSQL.
		"--etcd-servers=" + etcdServers,
		// HTTPS listen port; must match the Service targetPort and APIService spec.
		"--secure-port=" + fmt.Sprintf("%d", SecondaryPort),
		// TLS serving certificate issued by cert-manager for the secondary's FQDN.
		"--tls-cert-file=/etc/kubernetes/pki/tls.crt",
		"--tls-private-key-file=/etc/kubernetes/pki/tls.key",
		// CA used to verify client certificates (e.g. the operator's admin kubeconfig).
		"--client-ca-file=/etc/kubernetes/pki/ca.crt",
		// SA token verification/signing keys. Reuses the serving key because the
		// secondary never issues tokens consumed externally; it only needs a valid
		// key pair to satisfy the mandatory kube-apiserver flags.
		"--service-account-key-file=/etc/kubernetes/pki/tls.key",
		"--service-account-signing-key-file=/etc/kubernetes/pki/tls.key",
		"--service-account-issuer=https://kubernetes.default.svc",
		// NamespaceLifecycle is disabled because namespaces are mirrored from the
		// primary by the NamespaceSync controller — the secondary is not the source
		// of truth. ServiceAccount is disabled because all authentication happens on
		// the primary; the secondary receives pre-authenticated identity via
		// request headers. ResourceQuota is disabled because quota objects are not
		// synced from the primary; the secondary only stores custom resources
		// (not compute resources), so typical quotas don't apply, and naively
		// syncing count quotas would create split-brain usage tracking.
		"--disable-admission-plugins=NamespaceLifecycle,ServiceAccount,ResourceQuota",
		// Request-header (front-proxy) authentication: the primary's aggregation
		// proxy presents a client cert signed by this CA and forwards the original
		// user identity in X-Remote-* headers.
		"--requestheader-client-ca-file=/etc/kubernetes/requestheader/requestheader-client-ca-file",
		"--requestheader-allowed-names=front-proxy-client",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-username-headers=X-Remote-User",
		// Webhook authorization delegates SubjectAccessReview decisions to the
		// primary cluster, so existing RBAC policies apply transparently.
		"--authorization-mode=Webhook",
		"--authorization-webhook-config-file=/etc/kubernetes/auth/webhook-config.yaml",
		"--authorization-webhook-version=v1",
		// kube-apiserver v1.32+ disables anonymous auth by default when the
		// AnonymousAuthConfigurableEndpoints feature gate is enabled (beta).
		// Re-enable it so that API discovery endpoints remain accessible to
		// unauthenticated clients during transient cert-loading windows.
		"--anonymous-auth=true",
		// Admission webhooks are re-enabled so that mutating/validating webhooks
		// synced from the primary by WebhookSync are enforced on the secondary.
		"--enable-admission-plugins=MutatingAdmissionWebhook,ValidatingAdmissionWebhook",
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "kube-apiserver",
							Image:   image,
							Command: []string{"kube-apiserver"},
							Args:    args,
							Ports: []corev1.ContainerPort{
								{
									Name:          "https",
									ContainerPort: int32(SecondaryPort),
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "pki",
									MountPath: "/etc/kubernetes/pki",
									ReadOnly:  true,
								},
								{
									Name:      "auth-config",
									MountPath: "/etc/kubernetes/auth",
									ReadOnly:  true,
								},
								{
									Name:      "requestheader-ca",
									MountPath: "/etc/kubernetes/requestheader",
									ReadOnly:  true,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/healthz",
										Port:   intstr.FromInt32(int32(SecondaryPort)),
										Scheme: corev1.URISchemeHTTPS,
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/livez",
										Port:   intstr.FromInt32(int32(SecondaryPort)),
										Scheme: corev1.URISchemeHTTPS,
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       30,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "pki",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: fmt.Sprintf("%s-pki", shard.Name),
								},
							},
						},
						{
							Name: "auth-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("%s-authz-config", shard.Name),
									},
								},
							},
						},
						{
							Name: "requestheader-ca",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("%s-requestheader-ca", shard.Name),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return deployment
}

func BuildSecondaryService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := SecondaryServiceName(shard)
	labels := map[string]string{
		"app.kubernetes.io/name":       "kube-apiserver",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "apiserver",
	}

	return &corev1.Service{
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
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromInt32(int32(SecondaryPort)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}
