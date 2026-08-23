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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func TestBuildKineDeployment_Args(t *testing.T) {
	tests := []struct {
		name       string
		kine       kubeshardv1alpha1.KineSpec
		wantArgs   map[string]string
		absentArgs []string
	}{
		{
			name: "defaults only",
			kine: kubeshardv1alpha1.KineSpec{Replicas: 1},
			wantArgs: map[string]string{
				"--endpoint":             "sqlite:///data/kine.db",
				"--listen-address":       "tcp://0.0.0.0:2379",
				"--metrics-bind-address": ":8080",
			},
			absentArgs: []string{
				"--datastore-max-idle-connections",
				"--datastore-max-open-connections",
				"--datastore-connection-max-lifetime",
				"--compact-interval",
				"--compact-min-retain",
				"--compact-batch-size",
				"--compact-timeout",
				"--poll-batch-size",
				"--watch-progress-notify-interval",
			},
		},
		{
			name: "connection pool configured",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
				ConnectionPool: &kubeshardv1alpha1.ConnectionPoolConfig{
					MaxIdleConnections: ptr.To[int32](5),
					MaxOpenConnections: ptr.To[int32](10),
					MaxLifetime:        &metav1.Duration{Duration: 30 * time.Minute},
				},
			},
			wantArgs: map[string]string{
				"--datastore-max-idle-connections":    "5",
				"--datastore-max-open-connections":    "10",
				"--datastore-connection-max-lifetime": "30m0s",
			},
		},
		{
			name: "connection pool with zero values",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
				ConnectionPool: &kubeshardv1alpha1.ConnectionPoolConfig{
					MaxOpenConnections: ptr.To[int32](0),
				},
			},
			wantArgs: map[string]string{
				"--datastore-max-open-connections": "0",
			},
			absentArgs: []string{
				"--datastore-max-idle-connections",
				"--datastore-connection-max-lifetime",
			},
		},
		{
			name: "compaction configured",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
				Compaction: &kubeshardv1alpha1.CompactionConfig{
					Interval:  &metav1.Duration{Duration: 5 * time.Minute},
					MinRetain: ptr.To[int64](1000),
					BatchSize: ptr.To[int64](500),
				},
			},
			wantArgs: map[string]string{
				"--compact-interval":   "5m0s",
				"--compact-min-retain": "1000",
				"--compact-batch-size": "500",
			},
		},
		{
			name: "compaction timeout configured",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
				Compaction: &kubeshardv1alpha1.CompactionConfig{
					Timeout: &metav1.Duration{Duration: 30 * time.Second},
				},
			},
			wantArgs: map[string]string{
				"--compact-timeout": "30s",
			},
			absentArgs: []string{
				"--compact-interval",
				"--compact-min-retain",
				"--compact-batch-size",
			},
		},
		{
			name: "poll batch size and watch interval",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas:                    1,
				PollBatchSize:               ptr.To[int64](250),
				WatchProgressNotifyInterval: &metav1.Duration{Duration: 10 * time.Second},
			},
			wantArgs: map[string]string{
				"--poll-batch-size":                "250",
				"--watch-progress-notify-interval": "10s",
			},
		},
		{
			name: "all fields configured",
			kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
				ConnectionPool: &kubeshardv1alpha1.ConnectionPoolConfig{
					MaxIdleConnections: ptr.To[int32](2),
					MaxOpenConnections: ptr.To[int32](20),
					MaxLifetime:        &metav1.Duration{Duration: 1 * time.Hour},
				},
				Compaction: &kubeshardv1alpha1.CompactionConfig{
					Interval:  &metav1.Duration{Duration: 3 * time.Minute},
					MinRetain: ptr.To[int64](500),
					BatchSize: ptr.To[int64](100),
					Timeout:   &metav1.Duration{Duration: 30 * time.Second},
				},
				PollBatchSize:               ptr.To[int64](512),
				WatchProgressNotifyInterval: &metav1.Duration{Duration: 30 * time.Second},
			},
			wantArgs: map[string]string{
				"--datastore-max-idle-connections":    "2",
				"--datastore-max-open-connections":    "20",
				"--datastore-connection-max-lifetime": "1h0m0s",
				"--compact-interval":                  "3m0s",
				"--compact-min-retain":                "500",
				"--compact-batch-size":                "100",
				"--compact-timeout":                   "30s",
				"--poll-batch-size":                   "512",
				"--watch-progress-notify-interval":    "30s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			shard := newTestShard()
			shard.Spec.Kine = tt.kine
			shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite

			deploy := BuildKineDeployment(shard)
			args := deploy.Spec.Template.Spec.Containers[0].Args

			argMap := make(map[string]string)
			for i := 0; i < len(args)-1; i += 2 {
				argMap[args[i]] = args[i+1]
			}

			for flag, want := range tt.wantArgs {
				g.Expect(argMap).To(HaveKey(flag))
				g.Expect(argMap[flag]).To(Equal(want))
			}

			for _, flag := range tt.absentArgs {
				g.Expect(argMap).ToNot(HaveKey(flag))
			}
		})
	}
}

func TestBuildKineDeployment_DefaultImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal(DefaultKineImage))
}

func TestBuildKineDeployment_CustomImage(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.Image = "custom-registry/kine:v1.0.0"

	deploy := BuildKineDeployment(shard)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	g.Expect(got).To(Equal("custom-registry/kine:v1.0.0"))
}

func TestBuildKineDeployment_SQLiteVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite

	deploy := BuildKineDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	g.Expect(volumes).To(HaveLen(3), "expected tmp + kine-serving-cert + data volumes")

	var dataVol, tmpVol *corev1.Volume
	for i := range volumes {
		switch volumes[i].Name {
		case "data":
			dataVol = &volumes[i]
		case "tmp":
			tmpVol = &volumes[i]
		}
	}
	g.Expect(dataVol).ToNot(BeNil(), "expected data volume")
	g.Expect(dataVol.EmptyDir).ToNot(BeNil())
	g.Expect(tmpVol).ToNot(BeNil(), "expected tmp volume")
	g.Expect(tmpVol.EmptyDir).ToNot(BeNil())

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	g.Expect(mounts).To(HaveLen(3), "expected /tmp + /etc/kine/tls + /data mounts")
}

func TestBuildKineDeployment_SchedulingFields(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.NodeSelector = map[string]string{
		"node-role.kubernetes.io/infra": "",
	}

	deploy := BuildKineDeployment(shard)
	podSpec := deploy.Spec.Template.Spec

	g.Expect(podSpec.NodeSelector).ToNot(BeNil())
	g.Expect(podSpec.NodeSelector).To(HaveKey("node-role.kubernetes.io/infra"))
}

func TestBuildKineDeployment_TopologySpreadConstraints(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/component": "storage"},
			},
		},
	}

	deploy := BuildKineDeployment(shard)
	podSpec := deploy.Spec.Template.Spec

	g.Expect(podSpec.TopologySpreadConstraints).To(HaveLen(1))
	g.Expect(podSpec.TopologySpreadConstraints[0].TopologyKey).To(Equal("topology.kubernetes.io/zone"))
	g.Expect(podSpec.TopologySpreadConstraints[0].MaxSkew).To(Equal(int32(1)))
}

func TestBuildKineDeployment_AntiAffinityInjected(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 3

	deploy := BuildKineDeployment(shard)
	affinity := deploy.Spec.Template.Spec.Affinity

	g.Expect(affinity).ToNot(BeNil())
	g.Expect(affinity.PodAntiAffinity).ToNot(BeNil())
}

func TestBuildKineDeployment_NoAffinitySingleReplica(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Kine.Replicas = 1

	deploy := BuildKineDeployment(shard)
	affinity := deploy.Spec.Template.Spec.Affinity

	g.Expect(affinity).To(BeNil())
}

func TestBuildKineService_PreferSameNode_Enabled(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.ColocateComponents = ptr.To(true)

	svc := BuildKineService(shard)

	g.Expect(svc.Spec.TrafficDistribution).ToNot(BeNil())
	g.Expect(*svc.Spec.TrafficDistribution).To(Equal(corev1.ServiceTrafficDistributionPreferSameNode))
}

func TestBuildKineService_PreferSameNode_Disabled(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.ColocateComponents = ptr.To(false)

	svc := BuildKineService(shard)

	g.Expect(svc.Spec.TrafficDistribution).To(BeNil())
}

func TestBuildKineService_PreferSameNode_Default(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.ColocateComponents = nil

	svc := BuildKineService(shard)

	g.Expect(svc.Spec.TrafficDistribution).ToNot(BeNil())
	g.Expect(*svc.Spec.TrafficDistribution).To(Equal(corev1.ServiceTrafficDistributionPreferSameNode))
}

// TestBuildKineDeployment_MetricsPort verifies that the Kine deployment exposes the metrics bind address and container port.
func TestBuildKineDeployment_MetricsPort(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	deploy := BuildKineDeployment(shard)
	container := deploy.Spec.Template.Spec.Containers[0]

	// Verify --metrics-bind-address is always set
	args := container.Args
	argMap := make(map[string]string)
	for i := 0; i < len(args)-1; i += 2 {
		argMap[args[i]] = args[i+1]
	}
	g.Expect(argMap).To(HaveKeyWithValue("--metrics-bind-address", ":8080"))

	// Verify metrics container port is present
	var found bool
	for _, p := range container.Ports {
		if p.Name == "metrics" && p.ContainerPort == 8080 {
			found = true
			break
		}
	}
	g.Expect(found).To(BeTrue(), "expected metrics port 8080 in container ports")
}

// TestBuildKineService_MetricsPort verifies that the Kine service includes the metrics port.
func TestBuildKineService_MetricsPort(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	svc := BuildKineService(shard)

	var found bool
	for _, p := range svc.Spec.Ports {
		if p.Name == "metrics" && p.Port == 8080 {
			found = true
			break
		}
	}
	g.Expect(found).To(BeTrue(), "expected metrics port 8080 in service ports")
}

func TestBuildKineDeployment_RollingUpdateStrategy(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)

	strategy := deploy.Spec.Strategy
	g.Expect(strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
	g.Expect(strategy.RollingUpdate).ToNot(BeNil())
	g.Expect(*strategy.RollingUpdate.MaxUnavailable).To(Equal(intstr.FromInt32(0)))
	g.Expect(*strategy.RollingUpdate.MaxSurge).To(Equal(intstr.FromInt32(1)))
}

func TestBuildKineDeployment_SecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)

	podSC := deploy.Spec.Template.Spec.SecurityContext
	g.Expect(podSC).ToNot(BeNil())
	g.Expect(*podSC.RunAsNonRoot).To(BeTrue())
	g.Expect(podSC.RunAsUser).To(BeNil())
	g.Expect(podSC.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

	csc := deploy.Spec.Template.Spec.Containers[0].SecurityContext
	g.Expect(csc).ToNot(BeNil())
	g.Expect(*csc.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(csc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
}

// TestBuildKineDeployment_TLSArgs verifies that the Kine deployment includes
// --server-cert-file, --server-key-file, and --trusted-ca-file flags for mTLS.
func TestBuildKineDeployment_TLSArgs(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	argMap := make(map[string]string)
	for i := 0; i < len(args)-1; i += 2 {
		argMap[args[i]] = args[i+1]
	}

	g.Expect(argMap).To(HaveKeyWithValue("--server-cert-file",
		"/etc/kine/tls/tls.crt"))
	g.Expect(argMap).To(HaveKeyWithValue("--server-key-file",
		"/etc/kine/tls/tls.key"))
	g.Expect(argMap).To(HaveKeyWithValue("--trusted-ca-file",
		"/etc/kine/tls/ca.crt"))
}

// TestBuildKineDeployment_TLSVolumeMount verifies that the Kine deployment
// mounts the serving certificate Secret at /etc/kine/tls.
func TestBuildKineDeployment_TLSVolumeMount(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	var tlsMount *corev1.VolumeMount
	for i := range mounts {
		if mounts[i].Name == "kine-serving-cert" {
			tlsMount = &mounts[i]
			break
		}
	}
	g.Expect(tlsMount).ToNot(BeNil(), "expected kine-serving-cert volume mount")
	g.Expect(tlsMount.MountPath).To(Equal("/etc/kine/tls"))
	g.Expect(tlsMount.ReadOnly).To(BeTrue())

	volumes := deploy.Spec.Template.Spec.Volumes
	var tlsVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "kine-serving-cert" {
			tlsVol = &volumes[i]
			break
		}
	}
	g.Expect(tlsVol).ToNot(BeNil(), "expected kine-serving-cert volume")
	g.Expect(tlsVol.Secret).ToNot(BeNil())
	g.Expect(tlsVol.Secret.SecretName).To(Equal("test-shard-kine-serving-cert"))
}

func TestBuildKineDeployment_SQLite_NoPGCAVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite

	deploy := BuildKineDeployment(shard)

	for _, vol := range deploy.Spec.Template.Spec.Volumes {
		g.Expect(vol.Name).ToNot(Equal(postgresqlCAVolumeName),
			"postgresql-ca volume should not be present for SQLite storage")
	}
	for _, mount := range deploy.Spec.Template.Spec.Containers[0].VolumeMounts {
		g.Expect(mount.Name).ToNot(Equal(postgresqlCAVolumeName),
			"postgresql-ca volume mount should not be present for SQLite storage")
	}
}

func TestBuildKineDeployment_InClusterPostgreSQL_PGCAVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	deploy := BuildKineDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	var pgCAVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == postgresqlCAVolumeName {
			pgCAVol = &volumes[i]
			break
		}
	}
	g.Expect(pgCAVol).ToNot(BeNil(), "expected postgresql-ca volume")
	g.Expect(pgCAVol.Secret).ToNot(BeNil())
	g.Expect(pgCAVol.Secret.SecretName).To(Equal("test-shard-postgresql-ca-keypair"))
	g.Expect(pgCAVol.Secret.Items).To(ConsistOf(corev1.KeyToPath{Key: "ca.crt", Path: "ca.crt"}))

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	var pgCAMount *corev1.VolumeMount
	for i := range mounts {
		if mounts[i].Name == postgresqlCAVolumeName {
			pgCAMount = &mounts[i]
			break
		}
	}
	g.Expect(pgCAMount).ToNot(BeNil(), "expected postgresql-ca volume mount")
	g.Expect(pgCAMount.MountPath).To(Equal(postgresqlCAMountPath))
	g.Expect(pgCAMount.ReadOnly).To(BeTrue())
}
