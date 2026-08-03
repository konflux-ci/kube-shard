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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	DefaultKineImage = "ghcr.io/k3s-io/kine:v0.16.3"
	KinePort         = 2379
	dataVolumeName   = "data"
)

// KineDeploymentName returns the name of the Kine Deployment for the given shard.
func KineDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine", shard.Name)
}

// KineServiceName returns the name of the Kine Service for the given shard.
func KineServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-kine", shard.Name)
}

// KineEndpoint returns the storage endpoint connection string for Kine based on
// the configured storage type (SQLite or PostgreSQL).
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

// BuildKineDeployment constructs the Kine Deployment resource for the given shard,
// including container configuration, storage volumes, and connection pool settings.
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
		LabelName:      "kine",
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentStorage,
	}

	args := []string{
		"--endpoint", KineEndpoint(shard),
		"--listen-address", fmt.Sprintf("tcp://0.0.0.0:%d", KinePort),
	}

	if shard.Spec.Kine.ConnectionPool != nil {
		cp := shard.Spec.Kine.ConnectionPool
		if cp.MaxIdleConnections != nil {
			args = append(args, "--datastore-max-idle-connections", strconv.FormatInt(int64(*cp.MaxIdleConnections), 10))
		}
		if cp.MaxOpenConnections != nil {
			args = append(args, "--datastore-max-open-connections", strconv.FormatInt(int64(*cp.MaxOpenConnections), 10))
		}
		if cp.MaxLifetime != nil {
			args = append(args, "--datastore-connection-max-lifetime", cp.MaxLifetime.Duration.String())
		}
	}

	if shard.Spec.Kine.Compaction != nil {
		c := shard.Spec.Kine.Compaction
		if c.Interval != nil {
			args = append(args, "--compact-interval", c.Interval.Duration.String())
		}
		if c.MinRetain != nil {
			args = append(args, "--compact-min-retain", strconv.FormatInt(*c.MinRetain, 10))
		}
		if c.BatchSize != nil {
			args = append(args, "--compact-batch-size", strconv.FormatInt(*c.BatchSize, 10))
		}
	}

	if shard.Spec.Kine.PollBatchSize != nil {
		args = append(args, "--poll-batch-size", strconv.FormatInt(*shard.Spec.Kine.PollBatchSize, 10))
	}
	if shard.Spec.Kine.WatchProgressNotifyInterval != nil {
		args = append(args, "--watch-progress-notify-interval", shard.Spec.Kine.WatchProgressNotifyInterval.Duration.String())
	}

	var envFrom []corev1.EnvFromSource
	var env []corev1.EnvVar
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	if shard.Spec.Storage.Type == kubeshardv1alpha1.StorageTypeSQLite {
		volumes = append(volumes, corev1.Volume{
			Name: dataVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      dataVolumeName,
			MountPath: "/data",
		})
	}

	switch shard.Spec.Storage.Type {
	case kubeshardv1alpha1.StorageTypePostgreSQL:
		if shard.Spec.Storage.ConnectionSecretRef != nil {
			env = append(env, corev1.EnvVar{
				Name: "KINE_ENDPOINT",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: shard.Spec.Storage.ConnectionSecretRef.Name,
						},
						Key: shard.Spec.Storage.ConnectionSecretRef.Key,
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
					NodeSelector:              shard.Spec.Kine.NodeSelector,
					Tolerations:               shard.Spec.Kine.Tolerations,
					TopologySpreadConstraints: shard.Spec.Kine.TopologySpreadConstraints,
					Affinity:                  BuildKineAffinity(shard),
					Containers: []corev1.Container{
						{
							Name:         "kine",
							Image:        image,
							Args:         args,
							Env:          env,
							EnvFrom:      envFrom,
							Resources:    shard.Spec.Kine.Resources,
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

// BuildKineService constructs the Kine Service resource for the given shard,
// exposing the gRPC endpoint. When colocation is enabled, traffic distribution
// is set to prefer same-node routing.
func BuildKineService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := KineServiceName(shard)
	labels := map[string]string{
		LabelName:      "kine",
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentStorage,
	}

	svc := &corev1.Service{
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
					Name:       "grpc",
					Port:       int32(KinePort),
					TargetPort: intstr.FromInt32(int32(KinePort)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if isColocateEnabled(shard) {
		svc.Spec.TrafficDistribution = ptr.To(corev1.ServiceTrafficDistributionPreferSameNode)
	}

	return svc
}
