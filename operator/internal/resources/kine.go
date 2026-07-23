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
	DefaultKineImage = "rancher/kine:v0.14.14"
	KinePort         = 2379
)

func KineDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine", shard.Name)
}

func KineServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine", shard.Name)
}

func KineEndpoint(shard *kubeshardv1alpha1.APIShard) string {
	switch shard.Spec.Storage.Type {
	case kubeshardv1alpha1.StorageTypeSQLite:
		return "sqlite:///data/kine.db"
	case kubeshardv1alpha1.StorageTypeInClusterPostgreSQL, kubeshardv1alpha1.StorageTypePostgreSQL:
		return "$(KINE_ENDPOINT)"
	default:
		return "sqlite:///data/kine.db"
	}
}

func BuildKineDeployment(shard *kubeshardv1alpha1.APIShard) *appsv1.Deployment {
	name := KineDeploymentName(shard)
	image := shard.Spec.Kine.Image
	if image == "" {
		image = DefaultKineImage
	}
	replicas := shard.Spec.Kine.Replicas
	if replicas == 0 {
		replicas = 1
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "kine",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "storage",
	}

	args := []string{
		"--endpoint", KineEndpoint(shard),
		"--listen-address", fmt.Sprintf("tcp://0.0.0.0:%d", KinePort),
	}

	var envFrom []corev1.EnvFromSource
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	if shard.Spec.Storage.Type == kubeshardv1alpha1.StorageTypeSQLite {
		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "data",
			MountPath: "/data",
		})
	}

	switch shard.Spec.Storage.Type {
	case kubeshardv1alpha1.StorageTypePostgreSQL:
		if shard.Spec.Storage.ConnectionSecretRef != nil {
			envFrom = append(envFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: shard.Spec.Storage.ConnectionSecretRef.Name,
					},
				},
			})
		}
	case kubeshardv1alpha1.StorageTypeInClusterPostgreSQL:
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: PostgreSQLSecretName(shard),
				},
			},
		})
	default:
	}

	deployment := &appsv1.Deployment{
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
							Name:         "kine",
							Image:        image,
							Args:         args,
							EnvFrom:      envFrom,
							VolumeMounts: volumeMounts,
							Ports: []corev1.ContainerPort{
								{
									Name:          "grpc",
									ContainerPort: int32(KinePort),
									Protocol:      corev1.ProtocolTCP,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(int32(KinePort)),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(int32(KinePort)),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       20,
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	return deployment
}

func BuildKineService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := KineServiceName(shard)
	labels := map[string]string{
		"app.kubernetes.io/name":       "kine",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "storage",
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       int32(KinePort),
					TargetPort: intstr.FromInt32(int32(KinePort)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}
