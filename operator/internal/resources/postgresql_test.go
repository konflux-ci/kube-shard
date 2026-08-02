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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func TestBuildPostgreSQLDeployment_NoPersistence_UsesEmptyDir(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	deploy := BuildPostgreSQLDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	if volumes[0].EmptyDir == nil {
		t.Error("expected EmptyDir volume source when persistence is nil")
	}
}

func TestBuildPostgreSQLStatefulSet_WithPersistence(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	storageClass := "gp3-csi"
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size:             resource.MustParse("50Gi"),
			StorageClassName: &storageClass,
		},
	}

	sts := BuildPostgreSQLStatefulSet(shard)

	if sts.Name != PostgreSQLDeploymentName(shard) {
		t.Errorf("StatefulSet name = %q, want %q", sts.Name, PostgreSQLDeploymentName(shard))
	}
	if sts.Namespace != shard.Spec.TargetNamespace {
		t.Errorf("StatefulSet namespace = %q, want %q", sts.Namespace, shard.Spec.TargetNamespace)
	}

	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("expected 1 volumeClaimTemplate, got %d", len(sts.Spec.VolumeClaimTemplates))
	}
	vct := sts.Spec.VolumeClaimTemplates[0]
	if vct.Name != "data" {
		t.Errorf("VCT name = %q, want 'data'", vct.Name)
	}

	expectedSize := resource.MustParse("50Gi")
	gotSize := vct.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotSize.Equal(expectedSize) {
		t.Errorf("VCT size = %s, want %s", gotSize.String(), expectedSize.String())
	}

	if vct.Spec.StorageClassName == nil || *vct.Spec.StorageClassName != storageClass {
		t.Errorf("VCT storageClassName = %v, want %q", vct.Spec.StorageClassName, storageClass)
	}

	if len(vct.Spec.AccessModes) != 1 || vct.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("VCT access modes = %v, want [ReadWriteOnce]", vct.Spec.AccessModes)
	}
}

func TestBuildPostgreSQLStatefulSet_PodTemplate(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size: resource.MustParse("10Gi"),
		},
	}

	sts := BuildPostgreSQLStatefulSet(shard)

	containers := sts.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	if containers[0].Name != "postgresql" {
		t.Errorf("container name = %q, want 'postgresql'", containers[0].Name)
	}
	if containers[0].Image != DefaultPostgreSQLImage {
		t.Errorf("container image = %q, want %q", containers[0].Image, DefaultPostgreSQLImage)
	}

	// Should have volume mount for data
	var dataMount *corev1.VolumeMount
	for i := range containers[0].VolumeMounts {
		if containers[0].VolumeMounts[i].Name == "data" {
			dataMount = &containers[0].VolumeMounts[i]
			break
		}
	}
	if dataMount == nil {
		t.Fatal("expected 'data' volume mount")
	}
	if dataMount.MountPath != "/var/lib/postgresql/data" {
		t.Errorf("data mount path = %q, want '/var/lib/postgresql/data'", dataMount.MountPath)
	}

	// StatefulSet should have no Volumes (data comes from VCT)
	if len(sts.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("expected 0 volumes in pod template (data via VCT), got %d", len(sts.Spec.Template.Spec.Volumes))
	}
}

func TestBuildPostgreSQLStatefulSet_ServiceName(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size: resource.MustParse("10Gi"),
		},
	}

	sts := BuildPostgreSQLStatefulSet(shard)

	if sts.Spec.ServiceName != PostgreSQLServiceName(shard) {
		t.Errorf("ServiceName = %q, want %q", sts.Spec.ServiceName, PostgreSQLServiceName(shard))
	}
}

func TestPostgreSQLPersistenceEnabled(t *testing.T) {
	tests := []struct {
		name     string
		inCluster *kubeshardv1alpha1.InClusterStorage
		want     bool
	}{
		{
			name:     "nil InCluster",
			inCluster: nil,
			want:     false,
		},
		{
			name:     "nil Persistence",
			inCluster: &kubeshardv1alpha1.InClusterStorage{},
			want:     false,
		},
		{
			name: "Persistence set",
			inCluster: &kubeshardv1alpha1.InClusterStorage{
				Persistence: &kubeshardv1alpha1.PersistenceSpec{
					Size: resource.MustParse("10Gi"),
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shard := newTestShard()
			shard.Spec.Storage.InCluster = tt.inCluster
			got := PostgreSQLPersistenceEnabled(shard)
			if got != tt.want {
				t.Errorf("PostgreSQLPersistenceEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
