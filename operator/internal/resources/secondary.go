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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	// renovate: datasource=docker depName=registry.k8s.io/kube-apiserver
	DefaultSecondaryImage    = "registry.k8s.io/kube-apiserver:v1.36.2"
	SecondaryPort            = 6443
	tmpVolumeName            = "tmp"
	varRunKubeVolumeName     = "var-run-kubernetes"
	etcdClientCertVolumeName = "etcd-client-cert"
)

// SecondaryServiceAccountName returns the name of the ServiceAccount used by
// the secondary apiserver pods. A dedicated SA isolates the auth-delegator
// ClusterRoleBinding from the namespace's default SA.
func SecondaryServiceAccountName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

// SecondaryDeploymentName returns the name of the secondary apiserver Deployment for the given shard.
func SecondaryDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

// SecondaryServiceName returns the name of the secondary apiserver Service for the given shard.
func SecondaryServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

// SecondaryEndpoint returns the in-cluster HTTPS URL for the secondary apiserver.
func SecondaryEndpoint(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("https://%s.%s.svc",
		SecondaryServiceName(shard),
		shard.Spec.TargetNamespace,
	)
}

// BuildSecondaryServiceAccount constructs the ServiceAccount for the secondary
// kube-apiserver pods. The auth-delegator ClusterRoleBinding references this SA.
func BuildSecondaryServiceAccount(shard *kubeshardv1alpha1.APIShard) *corev1.ServiceAccount {
	name := SecondaryServiceAccountName(shard)
	labels := map[string]string{
		LabelName:      NameAPIServer,
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentAPIServer,
	}

	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
	}
}

// BuildSecondaryDeployment constructs the secondary kube-apiserver Deployment resource
// for the given shard, including TLS, authorization webhook, and request-header configuration.
func BuildSecondaryDeployment(
	shard *kubeshardv1alpha1.APIShard,
	requestHeaderAllowedNames []string,
	primaryIssuers []string,
) *appsv1.Deployment {
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
		LabelName:      NameAPIServer,
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentAPIServer,
	}

	kineSvc := KineServiceName(shard)
	etcdServers := fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", kineSvc, shard.Spec.TargetNamespace, KinePort)

	args := []string{
		// Kine endpoint emulating the etcd v3 API over TLS.
		"--etcd-servers=" + etcdServers,
		// mTLS credentials for the Kine connection: client certificate, key,
		// and the CA that signed the Kine serving certificate.
		"--etcd-certfile=/etc/kubernetes/etcd-client/tls.crt",
		"--etcd-keyfile=/etc/kubernetes/etcd-client/tls.key",
		"--etcd-cafile=/etc/kubernetes/etcd-client/ca.crt",
		// HTTPS listen port; must match the Service targetPort and APIService spec.
		"--secure-port=" + fmt.Sprintf("%d", SecondaryPort),
		// TLS serving certificate issued by cert-manager for the secondary's FQDN.
		"--tls-cert-file=/etc/kubernetes/pki/tls.crt",
		"--tls-private-key-file=/etc/kubernetes/pki/tls.key",
		// CA used to verify client certificates (e.g. the operator's admin kubeconfig).
		"--client-ca-file=/etc/kubernetes/pki/ca.crt",
		// SA token verification/signing keys. Reuses the serving key because the
		// secondary never issues tokens consumed externally; it only needs a valid
		// key pair to satisfy the mandatory kube-apiserver flags. The issuer MUST
		// differ from the primary's issuer so that primary-issued SA tokens are
		// NOT claimed by the secondary's local SA authenticator (which would fail
		// signature validation) and instead fall through to the token webhook.
		"--service-account-key-file=/etc/kubernetes/pki/tls.key",
		"--service-account-signing-key-file=/etc/kubernetes/pki/tls.key",
		fmt.Sprintf("--service-account-issuer=https://%s-apiserver.%s.svc",
			shard.Name, shard.Spec.TargetNamespace),
		// api-audiences must include both the secondary's own issuer (for
		// self-issued tokens) AND the primary's issuer(s) so that tokens
		// validated via the authentication webhook are accepted. Without this,
		// the secondary rejects webhook-authenticated tokens whose audience
		// doesn't match the secondary's issuer. The primary issuer is
		// discovered at startup from /.well-known/openid-configuration.
		"--api-audiences=" + strings.Join(
			append([]string{fmt.Sprintf("https://%s-apiserver.%s.svc",
				shard.Name, shard.Spec.TargetNamespace)}, primaryIssuers...),
			","),
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
		"--requestheader-allowed-names=" + strings.Join(requestHeaderAllowedNames, ","),
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-username-headers=X-Remote-User",
		// Webhook authorization delegates SubjectAccessReview decisions to the
		// primary cluster, so existing RBAC policies apply transparently.
		"--authorization-mode=Webhook",
		"--authorization-webhook-config-file=/etc/kubernetes/auth/webhook-config.yaml",
		"--authorization-webhook-version=v1",
		// Token webhook authentication delegates TokenReview to the primary
		// cluster, allowing the secondary to validate bearer tokens issued
		// by the primary (e.g. for Prometheus metrics scraping).
		"--authentication-token-webhook-config-file=/etc/kubernetes/auth/authn-webhook-config.yaml",
		"--authentication-token-webhook-version=v1",
		// kube-apiserver v1.32+ disables anonymous auth by default when the
		// AnonymousAuthConfigurableEndpoints feature gate is enabled (beta).
		// Re-enable it so that API discovery endpoints remain accessible to
		// unauthenticated clients during transient cert-loading windows.
		"--anonymous-auth=true",
		// Admission webhooks are re-enabled so that mutating/validating webhooks
		// synced from the primary by WebhookSync are enforced on the secondary.
		"--enable-admission-plugins=MutatingAdmissionWebhook,ValidatingAdmissionWebhook",
		// Graceful shutdown: delay keeps the process alive and serving while the
		// aggregation layer propagates endpoint removal. Retry-After headers tell
		// clients to retry on another instance during draining.
		"--shutdown-delay-duration=15s",
		"--shutdown-send-retry-after=true",
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
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 0},
					MaxSurge:       &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:            SecondaryServiceAccountName(shard),
					TerminationGracePeriodSeconds: ptr.To(int64(65)),
					SecurityContext:               APIServerPodSecurityContext(),
					NodeSelector:                  shard.Spec.Secondary.NodeSelector,
					Tolerations:                   shard.Spec.Secondary.Tolerations,
					TopologySpreadConstraints:     shard.Spec.Secondary.TopologySpreadConstraints,
					Affinity:                      BuildSecondaryAffinity(shard),
					Containers: []corev1.Container{
						{
							Name:            NameAPIServer,
							Image:           image,
							Command:         []string{NameAPIServer},
							Args:            args,
							Resources:       shard.Spec.Secondary.Resources,
							SecurityContext: APIServerContainerSecurityContext(),
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
								{
									Name:      etcdClientCertVolumeName,
									MountPath: "/etc/kubernetes/etcd-client",
									ReadOnly:  true,
								},
								{
									Name:      tmpVolumeName,
									MountPath: "/tmp",
								},
								{
									Name:      varRunKubeVolumeName,
									MountPath: "/var/run/kubernetes",
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
						{
							Name: etcdClientCertVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: fmt.Sprintf("%s-etcd-client-cert", shard.Name),
								},
							},
						},
						{
							Name: tmpVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: varRunKubeVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	return deployment
}

// BuildSecondaryService constructs the secondary apiserver Service resource for the given shard.
func BuildSecondaryService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := SecondaryServiceName(shard)
	labels := map[string]string{
		LabelName:      NameAPIServer,
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentAPIServer,
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
