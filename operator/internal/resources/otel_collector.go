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
	"net"
	"net/url"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

const (
	// renovate: datasource=docker depName=registry.access.redhat.com/hi/opentelemetry-collector-contrib
	DefaultOTelCollectorImage       = "registry.access.redhat.com/hi/opentelemetry-collector-contrib:0.155.0"
	OTelMetricsPort           int32 = 9187
	OTelHealthPort            int32 = 13133
	OTelMetricsPortName             = "otel-metrics"

	defaultCollectionInterval = "30s"
	defaultBloatInterval      = "5m"

	otelTLSVolumeName = "tls-ca"
	otelTLSMountPath  = "/etc/otel/tls"
	otelCACertFile    = "/etc/otel/tls/ca.crt"
)

// OTelCollectorDeploymentName returns the name of the OTel Collector Deployment.
func OTelCollectorDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics", shard.Name)
}

// OTelCollectorServiceName returns the name of the OTel Collector Service.
func OTelCollectorServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics", shard.Name)
}

// OTelCollectorConfigMapBaseName returns the base name of the OTel Collector ConfigMap
// (used as input to the hashed configmap utility).
func OTelCollectorConfigMapBaseName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics-config", shard.Name)
}

// OTelConnectionParams holds parsed PostgreSQL connection parameters
// used to generate the OTel Collector configuration.
type OTelConnectionParams struct {
	Host             string
	Port             string
	User             string
	Password         string
	DBName           string
	CACertSecretName string
	CACertSecretKey  string
}

// BuildOTelCollectorConfig generates the OTel Collector YAML configuration string
// for PostgreSQL metrics collection. It is separated from the ConfigMap builder
// so callers can use it with hashed-configmap utilities.
// All connections use verify-full TLS.
func BuildOTelCollectorConfig(shard *kubeshardv1alpha1.APIShard, params OTelConnectionParams) string {
	collectionInterval := defaultCollectionInterval
	bloatInterval := defaultBloatInterval

	if shard.Spec.Storage.Monitoring != nil {
		if shard.Spec.Storage.Monitoring.CollectionInterval != "" {
			collectionInterval = shard.Spec.Storage.Monitoring.CollectionInterval
		}
		if shard.Spec.Storage.Monitoring.PostgreSQL != nil &&
			shard.Spec.Storage.Monitoring.PostgreSQL.BloatInterval != "" {
			bloatInterval = shard.Spec.Storage.Monitoring.PostgreSQL.BloatInterval
		}
	}

	endpoint := net.JoinHostPort(params.Host, params.Port)

	return fmt.Sprintf(`extensions:
  health_check:
    endpoint: "0.0.0.0:%d"

receivers:
  postgresql:
    endpoint: %s
    username: ${env:PG_USERNAME}
    password: ${env:PG_PASSWORD}
    databases: [%s]
    collection_interval: %s
    tls:
      insecure: false
      ca_file: %s

  sqlquery:
    driver: postgres
    datasource: "host='%s' port='%s' user='${env:PG_USERNAME}' password='${env:PG_PASSWORD}' dbname='%s' sslmode=verify-full sslrootcert=%s"
    collection_interval: %s
    queries:
      - sql: |
          CREATE EXTENSION IF NOT EXISTS pgstattuple;
          SET statement_timeout = '30s';
          SELECT
            coalesce(sum(dead_tuple_len + free_space), 0)::bigint AS reclaimable_bytes,
            coalesce(sum(tuple_len), 0)::bigint AS live_bytes,
            coalesce(sum(table_len), 0)::bigint AS total_table_bytes
          FROM (
            SELECT (pgstattuple(oid)).*
            FROM pg_class
            WHERE relkind IN ('r', 't')
              AND relnamespace NOT IN (
                SELECT oid FROM pg_namespace
                WHERE nspname IN ('pg_catalog', 'information_schema')
              )
          ) t
        metrics:
          - metric_name: postgresql.tables_reclaimable_bytes
            value_column: reclaimable_bytes
            data_type: gauge
            value_type: int
            description: "Bytes reclaimable by VACUUM FULL across all user tables (dead tuples + free space)"
          - metric_name: postgresql.tables_live_bytes
            value_column: live_bytes
            data_type: gauge
            value_type: int
            description: "Bytes used by live tuples across all user tables"
          - metric_name: postgresql.tables_size_bytes
            value_column: total_table_bytes
            data_type: gauge
            value_type: int
            description: "Total bytes on disk across all user tables (live + dead + free)"

exporters:
  prometheus:
    endpoint: "0.0.0.0:%d"

service:
  extensions: [health_check]
  pipelines:
    metrics:
      receivers: [postgresql, sqlquery]
      exporters: [prometheus]
`,
		OTelHealthPort,
		endpoint,
		params.DBName,
		collectionInterval,
		otelCACertFile,
		params.Host, params.Port, params.DBName, otelCACertFile,
		bloatInterval,
		OTelMetricsPort,
	)
}

// BuildOTelCollectorDeployment creates the Deployment for the OTel Collector
// that collects PostgreSQL metrics. The configMapName parameter should be the
// full (possibly hashed) name of the config ConfigMap.
func BuildOTelCollectorDeployment(
	shard *kubeshardv1alpha1.APIShard,
	credentialSecretName, configMapName string,
	params OTelConnectionParams,
) *appsv1.Deployment {
	name := OTelCollectorDeploymentName(shard)
	labels := otelLabels(shard)

	certKey := params.CACertSecretKey
	if certKey == "" {
		certKey = "ca.crt"
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "otel-config",
			MountPath: "/etc/otel",
			ReadOnly:  true,
		},
		{
			Name:      otelTLSVolumeName,
			MountPath: otelTLSMountPath,
			ReadOnly:  true,
		},
	}

	volumes := []corev1.Volume{
		{
			Name: "otel-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		},
		{
			Name: otelTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: params.CACertSecretName,
					Items: []corev1.KeyToPath{
						{Key: certKey, Path: "ca.crt"},
					},
				},
			},
		},
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
					SecurityContext: RestrictedPodSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            NameOTelCollector,
							Image:           DefaultOTelCollectorImage,
							SecurityContext: RestrictedContainerSecurityContext(),
							Args:            []string{"--config=/etc/otel/config.yaml"},
							Ports: []corev1.ContainerPort{
								{
									Name:          OTelMetricsPortName,
									ContainerPort: OTelMetricsPort,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "health",
									ContainerPort: OTelHealthPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "PG_USERNAME",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: credentialSecretName,
											},
											Key: "POSTGRES_USER",
										},
									},
								},
								{
									Name: "PG_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: credentialSecretName,
											},
											Key: "POSTGRES_PASSWORD",
										},
									},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							VolumeMounts: volumeMounts,
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.FromInt32(OTelHealthPort),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       30,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.FromInt32(OTelHealthPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
}

// BuildOTelCollectorService creates the Service for the OTel Collector
// that exposes the Prometheus metrics endpoint.
func BuildOTelCollectorService(shard *kubeshardv1alpha1.APIShard) *corev1.Service {
	name := OTelCollectorServiceName(shard)
	labels := otelLabels(shard)

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
					Name:       OTelMetricsPortName,
					Port:       OTelMetricsPort,
					TargetPort: intstr.FromInt32(OTelMetricsPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

// InClusterPostgreSQLConnectionParams returns the OTel connection parameters
// for the in-cluster PostgreSQL instance.
func InClusterPostgreSQLConnectionParams(shard *kubeshardv1alpha1.APIShard) OTelConnectionParams {
	return OTelConnectionParams{
		Host:   fmt.Sprintf("%s.%s.svc", PostgreSQLServiceName(shard), shard.Spec.TargetNamespace),
		Port:   fmt.Sprintf("%d", PostgreSQLPort),
		User:   NameKine,
		DBName: NameKine,
	}
}

// ParsePostgreSQLDSN parses a Kine-format PostgreSQL DSN
// (postgres://user:pass@host:port/dbname) into OTelConnectionParams.
func ParsePostgreSQLDSN(dsn string) (OTelConnectionParams, error) {
	params := OTelConnectionParams{
		Port:   "5432",
		DBName: "kine",
	}

	dsn = strings.TrimSpace(dsn)

	u, err := url.Parse(dsn)
	if err != nil {
		return params, fmt.Errorf("invalid DSN: %w", err)
	}

	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return params, fmt.Errorf("invalid DSN: unsupported scheme %q", u.Scheme)
	}

	if u.User != nil {
		params.User = u.User.Username()
		if p, ok := u.User.Password(); ok {
			params.Password = p
		}
	}

	params.Host = u.Hostname()
	if params.Host == "" {
		return params, fmt.Errorf("invalid DSN: host is empty")
	}

	if params.User == "" {
		return params, fmt.Errorf("invalid DSN: user is empty")
	}

	if port := u.Port(); port != "" {
		params.Port = port
	}

	// Validate port contains only digits.
	for _, c := range params.Port {
		if c < '0' || c > '9' {
			return params, fmt.Errorf("invalid DSN: port %q contains non-digit characters", params.Port)
		}
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName != "" {
		params.DBName = dbName
	}

	// Reject values that could inject YAML when interpolated into the
	// OTel Collector config template (e.g. newlines from %-encoded DSNs).
	for _, check := range []struct {
		name, value string
	}{
		{"host", params.Host},
		{"dbname", params.DBName},
		{"user", params.User},
	} {
		if strings.ContainsAny(check.value, "\n\r") {
			return params, fmt.Errorf("invalid DSN: %s contains newline characters", check.name)
		}
	}

	return params, nil
}

// otelLabels returns the standard Kubernetes labels for OTel Collector resources.
func otelLabels(shard *kubeshardv1alpha1.APIShard) map[string]string {
	return map[string]string{
		LabelName:      NameOTelCollector,
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentMonitoring,
	}
}
