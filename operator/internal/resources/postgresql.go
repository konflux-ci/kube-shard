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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	DefaultPostgreSQLImage = "registry.access.redhat.com/rhel9/postgresql-16:latest"
	PostgreSQLPort         = 5432
)

func PostgreSQLDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql", shard.Name)
}

func PostgreSQLServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql", shard.Name)
}

func PostgreSQLSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-credentials", shard.Name)
}

// PostgreSQLDSN returns the connection string for Kine to connect to the in-cluster PostgreSQL.
func PostgreSQLDSN(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("postgres://kine:kine@%s.%s.svc:%d/kine?sslmode=disable",
		PostgreSQLServiceName(shard),
		shard.Spec.TargetNamespace,
		PostgreSQLPort,
	)
}

// BuildPostgreSQLSecret creates the credentials secret for in-cluster PostgreSQL.
func BuildPostgreSQLSecret(shard *kubeshardv1alpha1.APIShard) *corev1.Secret {
	name := PostgreSQLSecretName(shard)
	labels := postgresLabels(shard)

	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		StringData: map[string]string{
			"POSTGRES_USER":     "kine",
			"POSTGRES_PASSWORD": "kine",
			"POSTGRES_DB":       "kine",
			"KINE_ENDPOINT":     PostgreSQLDSN(shard),
		},
	}
}

// BuildPostgreSQLDeployment creates the in-cluster PostgreSQL deployment.
func BuildPostgreSQLDeployment(shard *kubeshardv1alpha1.APIShard) *appsv1.Deployment {
	name := PostgreSQLDeploymentName(shard)
	labels := postgresLabels(shard)

	var resourceReqs corev1.ResourceRequirements
	if shard.Spec.Storage.InCluster != nil {
		resourceReqs = shard.Spec.Storage.InCluster.Resources
	}
	if resourceReqs.Requests == nil {
		resourceReqs.Requests = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}
	}

	return &appsv1.Deployment{
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
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "postgresql",
							Image: DefaultPostgreSQLImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "tcp",
									ContainerPort: PostgreSQLPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							EnvFrom: []corev1.EnvFromSource{
								{
									SecretRef: &corev1.SecretEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: PostgreSQLSecretName(shard),
										},
									},
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "PGDATA",
									Value: "/var/lib/postgresql/data/pgdata",
								},
							},
							Resources: resourceReqs,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/var/lib/postgresql/data",
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(PostgreSQLPort),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(PostgreSQLPort),
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       30,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
}

// BuildPostgreSQLService creates the service for in-cluster PostgreSQL.
func BuildPostgreSQLService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := PostgreSQLServiceName(shard)
	labels := postgresLabels(shard)

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
					Name:       "tcp",
					Port:       PostgreSQLPort,
					TargetPort: intstr.FromInt32(PostgreSQLPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func postgresLabels(shard *kubeshardv1alpha1.APIShard) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "postgresql",
		"app.kubernetes.io/instance":   shard.Name,
		"app.kubernetes.io/managed-by": "kube-shard-operator",
		"app.kubernetes.io/component":  "database",
	}
}
