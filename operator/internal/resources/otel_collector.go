package resources

import (
	"fmt"
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
	OTelCollectorImage        = "registry.access.redhat.com/hi/opentelemetry-collector-contrib:latest"
	OTelMetricsPort     int32 = 9187
	OTelHealthPort      int32 = 13133
	OTelMetricsPortName       = "otel-metrics"

	NameOTelCollector   = "otel-collector"
	ComponentMonitoring = "monitoring"

	defaultCollectionInterval = "30s"
	defaultBloatInterval      = "5m"
)

// OTelCollectorDeploymentName returns the name of the OTel Collector Deployment.
func OTelCollectorDeploymentName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics", shard.Name)
}

// OTelCollectorServiceName returns the name of the OTel Collector Service.
func OTelCollectorServiceName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics", shard.Name)
}

// OTelCollectorConfigMapName returns the name of the OTel Collector ConfigMap.
func OTelCollectorConfigMapName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-metrics-config", shard.Name)
}

// PostgreSQLInitConfigMapName returns the name of the init script ConfigMap.
func PostgreSQLInitConfigMapName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-postgresql-init", shard.Name)
}

// OTelConnectionParams holds parsed PostgreSQL connection parameters
// used to generate the OTel Collector configuration.
type OTelConnectionParams struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// BuildOTelCollectorConfigMap creates the ConfigMap containing the OTel Collector
// configuration for PostgreSQL metrics collection.
func BuildOTelCollectorConfigMap(shard *kubeshardv1alpha1.APIShard, params OTelConnectionParams) *corev1.ConfigMap {
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

	sslMode := params.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	insecure := "true"
	if sslMode != "disable" {
		insecure = "false"
	}

	config := fmt.Sprintf(`extensions:
  health_check:
    endpoint: "0.0.0.0:%d"

receivers:
  postgresql:
    endpoint: %s:%s
    username: ${env:PG_USERNAME}
    password: ${env:PG_PASSWORD}
    databases: [%s]
    collection_interval: %s
    tls:
      insecure: %s

  sqlquery:
    driver: postgres
    datasource: "host=%s port=%s user=${env:PG_USERNAME} password=${env:PG_PASSWORD} dbname=%s sslmode=%s"
    collection_interval: %s
    queries:
      - sql: |
          SELECT
            coalesce(sum(dead_tuple_len + free_space), 0)::bigint AS reclaimable_bytes,
            coalesce(sum(tuple_len), 0)::bigint AS live_bytes,
            coalesce(sum(table_len), 0)::bigint AS total_table_bytes
          FROM (
            SELECT (pgstattuple(oid)).*
            FROM pg_class
            WHERE relkind = 'r'
          ) t
        metrics:
          - metric_name: postgresql.reclaimable_bytes
            value_column: reclaimable_bytes
            data_type: gauge
            value_type: int
            description: "Bytes reclaimable by VACUUM FULL (dead tuples + free space)"
          - metric_name: postgresql.live_bytes
            value_column: live_bytes
            data_type: gauge
            value_type: int
            description: "Bytes used by live tuples"
          - metric_name: postgresql.total_table_bytes
            value_column: total_table_bytes
            data_type: gauge
            value_type: int
            description: "Total table bytes on disk (live + dead + free)"

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
		params.Host, params.Port,
		params.DBName,
		collectionInterval,
		insecure,
		params.Host, params.Port, params.DBName, sslMode,
		bloatInterval,
		OTelMetricsPort,
	)

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      OTelCollectorConfigMapName(shard),
			Namespace: shard.Spec.TargetNamespace,
			Labels:    otelLabels(shard),
		},
		Data: map[string]string{
			"config.yaml": config,
		},
	}
}

// BuildOTelCollectorDeployment creates the Deployment for the OTel Collector
// that collects PostgreSQL metrics.
func BuildOTelCollectorDeployment(shard *kubeshardv1alpha1.APIShard, credentialSecretName string) *appsv1.Deployment {
	name := OTelCollectorDeploymentName(shard)
	labels := otelLabels(shard)

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
							Image:           OTelCollectorImage,
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
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "otel-config",
									MountPath: "/etc/otel",
									ReadOnly:  true,
								},
							},
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
					Volumes: []corev1.Volume{
						{
							Name: "otel-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: OTelCollectorConfigMapName(shard),
									},
								},
							},
						},
					},
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

// InClusterPostgreSQLConnectionParams returns the OTel connection parameters
// for the in-cluster PostgreSQL instance.
func InClusterPostgreSQLConnectionParams(shard *kubeshardv1alpha1.APIShard) OTelConnectionParams {
	return OTelConnectionParams{
		Host:    fmt.Sprintf("%s.%s.svc", PostgreSQLServiceName(shard), shard.Spec.TargetNamespace),
		Port:    fmt.Sprintf("%d", PostgreSQLPort),
		User:    NameKine,
		DBName:  NameKine,
		SSLMode: "disable",
	}
}

// ParsePostgreSQLDSN parses a Kine-format PostgreSQL DSN
// (postgres://user:pass@host:port/dbname?sslmode=...) into OTelConnectionParams.
func ParsePostgreSQLDSN(dsn string) (OTelConnectionParams, error) {
	params := OTelConnectionParams{
		Port:    "5432",
		SSLMode: "disable",
		DBName:  "kine",
	}

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
	if port := u.Port(); port != "" {
		params.Port = port
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName != "" {
		params.DBName = dbName
	}

	if sslmode := u.Query().Get("sslmode"); sslmode != "" {
		params.SSLMode = sslmode
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
