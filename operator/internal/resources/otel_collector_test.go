package resources

import (
	"testing"

	. "github.com/onsi/gomega"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func TestBuildOTelCollectorConfig_InCluster_DefaultIntervals(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{
		Enabled: true,
	}

	params := InClusterPostgreSQLConnectionParams(shard)
	config := BuildOTelCollectorConfig(shard, params)

	g.Expect(config).To(ContainSubstring("collection_interval: 30s"))
	g.Expect(config).To(ContainSubstring("collection_interval: 5m"))
	g.Expect(config).To(ContainSubstring("test-shard-postgresql.test-ns.svc"))
	g.Expect(config).To(ContainSubstring("pgstattuple"))
	g.Expect(config).To(ContainSubstring("reclaimable_bytes"))
	g.Expect(config).To(ContainSubstring("live_bytes"))
	g.Expect(config).To(ContainSubstring("total_table_bytes"))
	g.Expect(config).To(ContainSubstring("statement_timeout"))
	g.Expect(config).To(ContainSubstring("pg_catalog"))
	g.Expect(config).To(ContainSubstring("information_schema"))
}

func TestBuildOTelCollectorConfig_CustomIntervals(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{
		Enabled:            true,
		CollectionInterval: "60s",
		PostgreSQL: &kubeshardv1alpha1.PostgreSQLMonitoringSpec{
			BloatInterval: "10m",
		},
	}

	params := InClusterPostgreSQLConnectionParams(shard)
	config := BuildOTelCollectorConfig(shard, params)

	g.Expect(config).To(ContainSubstring("collection_interval: 60s"))
	g.Expect(config).To(ContainSubstring("collection_interval: 10m"))
}

func TestBuildOTelCollectorConfig_TLS_Disable(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{Enabled: true}

	params := OTelConnectionParams{
		Host:    "db.example.com",
		Port:    "5432",
		DBName:  "kine",
		SSLMode: "disable",
	}

	config := BuildOTelCollectorConfig(shard, params)
	g.Expect(config).To(ContainSubstring("insecure: true"))
	g.Expect(config).ToNot(ContainSubstring("ca_file"))
	g.Expect(config).ToNot(ContainSubstring("sslrootcert"))
}

func TestBuildOTelCollectorConfig_TLS_Require(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{Enabled: true}

	params := OTelConnectionParams{
		Host:    "db.example.com",
		Port:    "5432",
		DBName:  "kine",
		SSLMode: "require",
	}

	config := BuildOTelCollectorConfig(shard, params)
	g.Expect(config).To(ContainSubstring("insecure: false"))
	g.Expect(config).To(ContainSubstring("ca_file: /etc/otel/tls/ca.crt"))
	g.Expect(config).ToNot(ContainSubstring("server_name"))
	g.Expect(config).To(ContainSubstring("sslrootcert=/etc/otel/tls/ca.crt"))
}

func TestBuildOTelCollectorConfig_TLS_VerifyFull(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{Enabled: true}

	params := OTelConnectionParams{
		Host:    "db.example.com",
		Port:    "5432",
		DBName:  "kine",
		SSLMode: "verify-full",
	}

	config := BuildOTelCollectorConfig(shard, params)
	g.Expect(config).To(ContainSubstring("insecure: false"))
	g.Expect(config).To(ContainSubstring("ca_file: /etc/otel/tls/ca.crt"))
	g.Expect(config).To(ContainSubstring("server_name: db.example.com"))
	g.Expect(config).To(ContainSubstring("sslrootcert=/etc/otel/tls/ca.crt"))
}

func TestBuildOTelCollectorConfig_DSN_SingleQuoted(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Monitoring = &kubeshardv1alpha1.StorageMonitoringSpec{Enabled: true}

	params := InClusterPostgreSQLConnectionParams(shard)
	config := BuildOTelCollectorConfig(shard, params)

	g.Expect(config).To(ContainSubstring("user='${env:PG_USERNAME}'"))
	g.Expect(config).To(ContainSubstring("password='${env:PG_PASSWORD}'"))
}

func TestBuildOTelCollectorDeployment(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL

	params := InClusterPostgreSQLConnectionParams(shard)
	deploy := BuildOTelCollectorDeployment(shard, "test-shard-postgresql-credentials", "test-shard-postgresql-metrics-config-abc123", params)

	g.Expect(deploy.Name).To(Equal("test-shard-postgresql-metrics"))
	g.Expect(deploy.Namespace).To(Equal("test-ns"))
	g.Expect(deploy.Labels).To(HaveKeyWithValue(LabelName, NameOTelCollector))
	g.Expect(deploy.Labels).To(HaveKeyWithValue(LabelComponent, ComponentMonitoring))
	g.Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))

	containers := deploy.Spec.Template.Spec.Containers
	g.Expect(containers).To(HaveLen(1))
	c := containers[0]
	g.Expect(c.Name).To(Equal(NameOTelCollector))
	g.Expect(c.Image).To(Equal(OTelCollectorImage))
	g.Expect(c.Args).To(Equal([]string{"--config=/etc/otel/config.yaml"}))
	g.Expect(c.Ports).To(HaveLen(2))
	g.Expect(c.Ports[0].ContainerPort).To(Equal(OTelMetricsPort))
	g.Expect(c.Ports[1].ContainerPort).To(Equal(OTelHealthPort))

	g.Expect(c.Env).To(HaveLen(2))
	g.Expect(c.Env[0].Name).To(Equal("PG_USERNAME"))
	g.Expect(c.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("test-shard-postgresql-credentials"))
	g.Expect(c.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("POSTGRES_USER"))
	g.Expect(c.Env[1].Name).To(Equal("PG_PASSWORD"))
	g.Expect(c.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("POSTGRES_PASSWORD"))

	g.Expect(c.LivenessProbe).ToNot(BeNil())
	g.Expect(c.ReadinessProbe).ToNot(BeNil())

	g.Expect(deploy.Spec.Template.Spec.Volumes).To(HaveLen(1))
	g.Expect(deploy.Spec.Template.Spec.Volumes[0].ConfigMap.Name).To(
		Equal("test-shard-postgresql-metrics-config-abc123"))
}

func TestBuildOTelCollectorDeployment_TLS_MountsCA(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	params := OTelConnectionParams{
		Host:             "db.example.com",
		Port:             "5432",
		DBName:           "kine",
		SSLMode:          "require",
		CACertSecretName: "my-ca-secret",
	}

	deploy := BuildOTelCollectorDeployment(shard, "creds", "config-hash123", params)

	g.Expect(deploy.Spec.Template.Spec.Volumes).To(HaveLen(2))
	g.Expect(deploy.Spec.Template.Spec.Volumes[1].Name).To(Equal("tls-ca"))
	g.Expect(deploy.Spec.Template.Spec.Volumes[1].Secret.SecretName).To(Equal("my-ca-secret"))

	c := deploy.Spec.Template.Spec.Containers[0]
	g.Expect(c.VolumeMounts).To(HaveLen(2))
	g.Expect(c.VolumeMounts[1].Name).To(Equal("tls-ca"))
	g.Expect(c.VolumeMounts[1].MountPath).To(Equal("/etc/otel/tls"))
}

func TestBuildOTelCollectorService(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	svc := BuildOTelCollectorService(shard)

	g.Expect(svc.Name).To(Equal("test-shard-postgresql-metrics"))
	g.Expect(svc.Namespace).To(Equal("test-ns"))
	g.Expect(svc.Spec.Ports).To(HaveLen(1))
	g.Expect(svc.Spec.Ports[0].Port).To(Equal(OTelMetricsPort))
	g.Expect(svc.Spec.Ports[0].Name).To(Equal(OTelMetricsPortName))
	g.Expect(svc.Spec.Selector).To(HaveKeyWithValue(LabelName, NameOTelCollector))
}

func TestBuildPostgreSQLInitConfigMap(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL

	cm := BuildPostgreSQLInitConfigMap(shard)

	g.Expect(cm.Name).To(Equal("test-shard-postgresql-init"))
	g.Expect(cm.Namespace).To(Equal("test-ns"))
	g.Expect(cm.Data).To(HaveKey("init-pgstattuple.sql"))
	g.Expect(cm.Data["init-pgstattuple.sql"]).To(ContainSubstring("CREATE EXTENSION IF NOT EXISTS pgstattuple"))
}

func TestBuildPostgreSQLStatefulSet_AlwaysMountsInitScript(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	var initVol bool
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.Name == "init-scripts" {
			initVol = true
			g.Expect(v.ConfigMap.Name).To(Equal("test-shard-postgresql-init"))
			g.Expect(*v.ConfigMap.Optional).To(BeTrue())
		}
	}
	g.Expect(initVol).To(BeTrue(), "expected 'init-scripts' volume even without monitoring enabled")

	container := sts.Spec.Template.Spec.Containers[0]
	var initMount bool
	for _, vm := range container.VolumeMounts {
		if vm.Name == "init-scripts" {
			initMount = true
			g.Expect(vm.MountPath).To(Equal("/docker-entrypoint-initdb.d"))
			g.Expect(vm.ReadOnly).To(BeTrue())
		}
	}
	g.Expect(initMount).To(BeTrue(), "expected init-scripts volume mount")
}

func TestParsePostgreSQLDSN(t *testing.T) {
	g := NewGomegaWithT(t)

	params, err := ParsePostgreSQLDSN("postgres://myuser:mypass@db.example.com:5433/mydb?sslmode=require")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(params.Host).To(Equal("db.example.com"))
	g.Expect(params.Port).To(Equal("5433"))
	g.Expect(params.User).To(Equal("myuser"))
	g.Expect(params.Password).To(Equal("mypass"))
	g.Expect(params.DBName).To(Equal("mydb"))
	g.Expect(params.SSLMode).To(Equal("require"))
}

func TestParsePostgreSQLDSN_DefaultPort(t *testing.T) {
	g := NewGomegaWithT(t)

	params, err := ParsePostgreSQLDSN("postgres://user:pass@host/db")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(params.Host).To(Equal("host"))
	g.Expect(params.Port).To(Equal("5432"))
	g.Expect(params.DBName).To(Equal("db"))
	g.Expect(params.SSLMode).To(Equal("disable"))
}

func TestParsePostgreSQLDSN_InvalidScheme(t *testing.T) {
	g := NewGomegaWithT(t)

	_, err := ParsePostgreSQLDSN("mysql://user:pass@host/db")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unsupported scheme"))
}

func TestParsePostgreSQLDSN_EmptyHost(t *testing.T) {
	g := NewGomegaWithT(t)

	_, err := ParsePostgreSQLDSN("postgres://user:pass@/db")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("host is empty"))
}

func TestParsePostgreSQLDSN_IPv6(t *testing.T) {
	g := NewGomegaWithT(t)

	params, err := ParsePostgreSQLDSN("postgres://user:pass@[::1]:5432/db")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(params.Host).To(Equal("::1"))
	g.Expect(params.Port).To(Equal("5432"))
}

func TestParsePostgreSQLDSN_WhitespaceTrimmed(t *testing.T) {
	g := NewGomegaWithT(t)

	params, err := ParsePostgreSQLDSN("  postgres://user:pass@host/db  \n")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(params.Host).To(Equal("host"))
}

func TestInClusterPostgreSQLConnectionParams(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	params := InClusterPostgreSQLConnectionParams(shard)

	g.Expect(params.Host).To(Equal("test-shard-postgresql.test-ns.svc"))
	g.Expect(params.Port).To(Equal("5432"))
	g.Expect(params.User).To(Equal("kine"))
	g.Expect(params.DBName).To(Equal("kine"))
	g.Expect(params.SSLMode).To(Equal("disable"))
}

func TestBuildPostgreSQLMetricsServiceMonitor(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	sm := BuildPostgreSQLMetricsServiceMonitor(shard)

	g.Expect(sm.Name).To(Equal("test-shard-postgresql-metrics"))
	g.Expect(sm.Namespace).To(Equal("test-ns"))
	g.Expect(sm.Labels).To(HaveKeyWithValue(LabelManagedBy, ManagedByValue))
	g.Expect(sm.Labels).To(HaveKeyWithValue(LabelInstance, "test-shard"))

	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelName, NameOTelCollector))
	g.Expect(sm.Spec.Selector.MatchLabels).To(HaveKeyWithValue(LabelInstance, "test-shard"))

	g.Expect(sm.Spec.Endpoints).To(HaveLen(1))
	ep := sm.Spec.Endpoints[0]
	g.Expect(ep.Port).To(Equal(OTelMetricsPortName))
	g.Expect(ep.Path).To(Equal("/metrics"))
}
