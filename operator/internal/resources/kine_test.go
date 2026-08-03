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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				"--endpoint":       "sqlite:///data/kine.db",
				"--listen-address": "tcp://0.0.0.0:2379",
			},
			absentArgs: []string{
				"--datastore-max-idle-connections",
				"--datastore-max-open-connections",
				"--datastore-connection-max-lifetime",
				"--compact-interval",
				"--compact-min-retain",
				"--compact-batch-size",
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
	g.Expect(volumes).To(HaveLen(1))
	g.Expect(volumes[0].Name).To(Equal("data"))
	g.Expect(volumes[0].EmptyDir).ToNot(BeNil())

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	g.Expect(mounts).To(HaveLen(1))
	g.Expect(mounts[0].MountPath).To(Equal("/data"))
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
	g.Expect(*svc.Spec.TrafficDistribution).To(Equal("PreferSameNode"))
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
	g.Expect(*svc.Spec.TrafficDistribution).To(Equal("PreferSameNode"))
}
