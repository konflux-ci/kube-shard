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
					MaxIdleConnections: ptr.To(5),
					MaxOpenConnections: ptr.To(10),
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
					MaxOpenConnections: ptr.To(0),
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
					MaxIdleConnections: ptr.To(2),
					MaxOpenConnections: ptr.To(20),
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
				got, ok := argMap[flag]
				if !ok {
					t.Errorf("expected flag %s not found in args", flag)
					continue
				}
				if got != want {
					t.Errorf("flag %s = %q, want %q", flag, got, want)
				}
			}

			for _, flag := range tt.absentArgs {
				if _, ok := argMap[flag]; ok {
					t.Errorf("flag %s should be absent when not configured", flag)
				}
			}
		})
	}
}

func TestBuildKineDeployment_DefaultImage(t *testing.T) {
	shard := newTestShard()
	deploy := BuildKineDeployment(shard)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	if got != DefaultKineImage {
		t.Errorf("image = %q, want %q", got, DefaultKineImage)
	}
}

func TestBuildKineDeployment_CustomImage(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Kine.Image = "custom-registry/kine:v1.0.0"

	deploy := BuildKineDeployment(shard)

	got := deploy.Spec.Template.Spec.Containers[0].Image
	if got != "custom-registry/kine:v1.0.0" {
		t.Errorf("image = %q, want custom image", got)
	}
}

func TestBuildKineDeployment_SQLiteVolume(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite

	deploy := BuildKineDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	if len(volumes) != 1 || volumes[0].Name != "data" {
		t.Fatalf("expected 1 volume named 'data', got %d volumes", len(volumes))
	}
	if volumes[0].EmptyDir == nil {
		t.Error("SQLite volume should use EmptyDir")
	}

	mounts := deploy.Spec.Template.Spec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].MountPath != "/data" {
		t.Error("expected volume mount at /data")
	}
}
