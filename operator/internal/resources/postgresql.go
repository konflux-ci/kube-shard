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
	"crypto/rand"
	"encoding/hex"
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
	// renovate: datasource=docker depName=registry.access.redhat.com/hi/postgresql
	DefaultPostgreSQLImage      = "registry.access.redhat.com/hi/postgresql:18.6"
	PostgreSQLPort              = 5432
	postgresqlTLSVolumeName     = "postgresql-tls"
	postgresqlTLSMountPath      = "/etc/postgresql/tls"
	postgresqlServingSecretName = "%s-postgresql-serving-cert"
	postgresqlTLSCertMode       = int32(0644)
	postgresqlTLSKeyMode        = int32(0640)
)

func PostgreSQLStatefulSetName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql", shard.Name)
}

func PostgreSQLServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql", shard.Name)
}

func PostgreSQLSecretName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-credentials", shard.Name)
}

// PostgreSQLDSN returns the connection string for Kine to connect to the in-cluster PostgreSQL.
func PostgreSQLDSN(shard *kubeshardv1alpha1.APIShard, user, password string) string {
	return fmt.Sprintf("postgres://%s:%s@%s.%s.svc:%d/kine?sslmode=verify-full&sslrootcert=/etc/kine/pg-ca/ca.crt",
		user, password,
		PostgreSQLServiceName(shard),
		shard.Spec.TargetNamespace,
		PostgreSQLPort,
	)
}

// GeneratePassword returns a cryptographically random 16-byte hex-encoded password.
func GeneratePassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// BuildPostgreSQLSecret creates the credentials secret for in-cluster PostgreSQL.
// The caller must provide the password; this function does not generate one so
// that existing secrets can be preserved across reconciliation loops.
func BuildPostgreSQLSecret(shard *kubeshardv1alpha1.APIShard, password string) *corev1.Secret {
	name := PostgreSQLSecretName(shard)
	labels := postgresLabels(shard)
	user := NameKine

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
			"POSTGRES_USER":     user,
			"POSTGRES_PASSWORD": password,
			"POSTGRES_DB":       NameKine,
			"KINE_ENDPOINT":     PostgreSQLDSN(shard, user, password),
		},
	}
}

// BuildPostgreSQLStatefulSet creates the in-cluster PostgreSQL StatefulSet.
// When persistence is configured, VolumeClaimTemplates provide durable storage.
// When persistence is nil, an emptyDir volume is used (data lost on pod restart).
func BuildPostgreSQLStatefulSet(shard *kubeshardv1alpha1.APIShard) *appsv1.StatefulSet {
	name := PostgreSQLStatefulSetName(shard)
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

	volumes := make([]corev1.Volume, 0, 2)
	var vcts []corev1.PersistentVolumeClaim

	persistence := persistenceFromShard(shard)
	if persistence != nil {
		vcts = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: dataVolumeName,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: persistence.StorageClassName,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: persistence.Size,
						},
					},
				},
			},
		}
	} else {
		volumes = []corev1.Volume{
			{
				Name: dataVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		}
	}

	volumes = append(volumes, corev1.Volume{
		Name: "tmp",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
	volumes = append(volumes, corev1.Volume{
		Name: postgresqlTLSVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: fmt.Sprintf(postgresqlServingSecretName, shard.Name),
				Items: []corev1.KeyToPath{
					{Key: "tls.crt", Path: "tls.crt", Mode: ptr.To(postgresqlTLSCertMode)},
					{Key: "tls.key", Path: "tls.key", Mode: ptr.To(postgresqlTLSKeyMode)},
					{Key: CACertKey, Path: CACertKey, Mode: ptr.To(postgresqlTLSCertMode)},
				},
			},
		},
	})

	volumes = append(volumes, corev1.Volume{
		Name: "init-scripts",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: PostgreSQLInitConfigMapName(shard),
				},
				Optional: ptr.To(true),
			},
		},
	})
	initVolumeMounts := []corev1.VolumeMount{
		{
			Name:      "init-scripts",
			MountPath: "/docker-entrypoint-initdb.d",
			ReadOnly:  true,
		},
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    ptr.To(int32(1)),
			ServiceName: PostgreSQLServiceName(shard),
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: RestrictedPodSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            "postgresql",
							Image:           DefaultPostgreSQLImage,
							SecurityContext: RestrictedContainerSecurityContext(),
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
							Args: []string{
								"-c", "ssl=on",
								"-c", "ssl_cert_file=" + postgresqlTLSMountPath + "/tls.crt",
								"-c", "ssl_key_file=" + postgresqlTLSMountPath + "/tls.key",
								"-c", "ssl_ca_file=" + postgresqlTLSMountPath + "/ca.crt",
							},
							Resources: resourceReqs,
							VolumeMounts: append([]corev1.VolumeMount{
								{
									Name:      dataVolumeName,
									MountPath: "/var/lib/postgresql/data",
								},
								{
									Name:      "tmp",
									MountPath: "/tmp",
								},
								{
									Name:      postgresqlTLSVolumeName,
									MountPath: postgresqlTLSMountPath,
									ReadOnly:  true,
								},
							}, initVolumeMounts...),
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
					Volumes: volumes,
				},
			},
			VolumeClaimTemplates: vcts,
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
		LabelName:      "postgresql",
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentDatabase,
	}
}

func persistenceFromShard(shard *kubeshardv1alpha1.APIShard) *kubeshardv1alpha1.PersistenceSpec {
	if shard.Spec.Storage.InCluster == nil {
		return nil
	}
	return shard.Spec.Storage.InCluster.Persistence
}

// StorageMonitoringEnabled returns true if storage monitoring is configured and enabled.
func StorageMonitoringEnabled(shard *kubeshardv1alpha1.APIShard) bool {
	return shard.Spec.Storage.Monitoring != nil && shard.Spec.Storage.Monitoring.Enabled
}

// PostgreSQLInitConfigMapName returns the name of the init script ConfigMap.
func PostgreSQLInitConfigMapName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-init", shard.Name)
}

// BuildPostgreSQLInitConfigMap creates a ConfigMap containing the SQL init script
// that creates the pgstattuple extension. This is mounted into the PostgreSQL
// container at /docker-entrypoint-initdb.d/ and runs on first database startup.
func BuildPostgreSQLInitConfigMap(shard *kubeshardv1alpha1.APIShard) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PostgreSQLInitConfigMapName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels:    postgresLabels(shard),
		},
		Data: map[string]string{
			"init-pgstattuple.sql": "CREATE EXTENSION IF NOT EXISTS pgstattuple;\n",
		},
	}
}
