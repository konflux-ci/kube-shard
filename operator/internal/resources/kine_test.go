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

func TestBuildKineDeployment_SQLite_NoPersistence_UsesEmptyDir(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite

	deploy := BuildKineDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	if volumes[0].Name != "data" {
		t.Errorf("expected volume name 'data', got %q", volumes[0].Name)
	}
	if volumes[0].EmptyDir == nil {
		t.Error("expected EmptyDir volume source when persistence is nil")
	}
}

func TestBuildKineDeployment_SQLite_WithPersistence_UsesPVCRef(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeSQLite
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size: resource.MustParse("10Gi"),
		},
	}

	deploy := BuildKineDeployment(shard)

	volumes := deploy.Spec.Template.Spec.Volumes
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	if volumes[0].Name != "data" {
		t.Errorf("expected volume name 'data', got %q", volumes[0].Name)
	}
	if volumes[0].PersistentVolumeClaim == nil {
		t.Fatal("expected PersistentVolumeClaim volume source when persistence is set")
	}
	expectedPVCName := KinePVCName(shard)
	if volumes[0].PersistentVolumeClaim.ClaimName != expectedPVCName {
		t.Errorf("PVC claim name = %q, want %q",
			volumes[0].PersistentVolumeClaim.ClaimName, expectedPVCName)
	}
}

func TestBuildKinePVC_DefaultAccessModes(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size: resource.MustParse("20Gi"),
		},
	}

	pvc := BuildKinePVC(shard)

	if pvc.Name != KinePVCName(shard) {
		t.Errorf("PVC name = %q, want %q", pvc.Name, KinePVCName(shard))
	}
	if pvc.Namespace != shard.Spec.TargetNamespace {
		t.Errorf("PVC namespace = %q, want %q", pvc.Namespace, shard.Spec.TargetNamespace)
	}

	expectedSize := resource.MustParse("20Gi")
	gotSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotSize.Equal(expectedSize) {
		t.Errorf("PVC size = %s, want %s", gotSize.String(), expectedSize.String())
	}

	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("PVC access modes = %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}

	if pvc.Spec.StorageClassName != nil {
		t.Errorf("PVC storageClassName should be nil (cluster default), got %q", *pvc.Spec.StorageClassName)
	}
}

func TestBuildKinePVC_CustomStorageClass(t *testing.T) {
	shard := newTestShard()
	storageClass := "gp3-csi"
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size:             resource.MustParse("50Gi"),
			StorageClassName: &storageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		},
	}

	pvc := BuildKinePVC(shard)

	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != storageClass {
		t.Errorf("PVC storageClassName = %v, want %q", pvc.Spec.StorageClassName, storageClass)
	}

	expectedSize := resource.MustParse("50Gi")
	gotSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if !gotSize.Equal(expectedSize) {
		t.Errorf("PVC size = %s, want %s", gotSize.String(), expectedSize.String())
	}
}

func TestBuildKinePVC_CustomAccessModes(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size:        resource.MustParse("10Gi"),
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}

	pvc := BuildKinePVC(shard)

	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("PVC access modes = %v, want [ReadWriteMany]", pvc.Spec.AccessModes)
	}
}
