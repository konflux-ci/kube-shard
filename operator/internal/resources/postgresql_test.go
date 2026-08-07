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

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func TestBuildPostgreSQLStatefulSet_NoPersistence_UsesEmptyDir(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	if len(sts.Spec.VolumeClaimTemplates) != 0 {
		t.Errorf("expected 0 VCTs when persistence is nil, got %d", len(sts.Spec.VolumeClaimTemplates))
	}
	volumes := sts.Spec.Template.Spec.Volumes
	var dataVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "data" {
			dataVol = &volumes[i]
			break
		}
	}
	if dataVol == nil {
		t.Fatal("expected 'data' volume")
	}
	if dataVol.EmptyDir == nil {
		t.Error("expected EmptyDir volume source when persistence is nil")
	}
}

func TestBuildPostgreSQLStatefulSet_WithPersistence_UsesVCT(t *testing.T) {
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

	// Only the tmp volume should be present; data comes from VCT
	if len(sts.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("expected 1 volume (tmp) when persistence is set (data via VCT), got %d", len(sts.Spec.Template.Spec.Volumes))
	}
	if sts.Spec.Template.Spec.Volumes[0].Name != "tmp" {
		t.Errorf("expected tmp volume, got %q", sts.Spec.Template.Spec.Volumes[0].Name)
	}

	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("expected 1 VCT, got %d", len(sts.Spec.VolumeClaimTemplates))
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
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	if sts.Name != PostgreSQLStatefulSetName(shard) {
		t.Errorf("name = %q, want %q", sts.Name, PostgreSQLStatefulSetName(shard))
	}
	if sts.Namespace != shard.Spec.TargetNamespace {
		t.Errorf("namespace = %q, want %q", sts.Namespace, shard.Spec.TargetNamespace)
	}
	if sts.Spec.ServiceName != PostgreSQLServiceName(shard) {
		t.Errorf("ServiceName = %q, want %q", sts.Spec.ServiceName, PostgreSQLServiceName(shard))
	}

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
}

func TestBuildPostgreSQLStatefulSet_SecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	podSC := sts.Spec.Template.Spec.SecurityContext
	g.Expect(podSC).ToNot(BeNil())
	g.Expect(*podSC.RunAsNonRoot).To(BeTrue())
	g.Expect(podSC.RunAsUser).To(BeNil(), "RunAsUser must not be set so OpenShift can assign from the namespace range")
	g.Expect(podSC.FSGroup).To(BeNil(), "FSGroup must not be set; OpenShift restricted-v2 assigns from the namespace range")
	g.Expect(podSC.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

	csc := sts.Spec.Template.Spec.Containers[0].SecurityContext
	g.Expect(csc).ToNot(BeNil())
	g.Expect(*csc.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(*csc.ReadOnlyRootFilesystem).To(BeFalse(), "PostgreSQL needs writable root for NSS wrapper /etc/passwd writes")
	g.Expect(csc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
}

func TestBuildPostgreSQLStatefulSet_TmpVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	var tmpVol *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "tmp" {
			tmpVol = &sts.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	g.Expect(tmpVol).ToNot(BeNil(), "expected tmp volume")
	g.Expect(tmpVol.EmptyDir).ToNot(BeNil())

	var tmpMount *corev1.VolumeMount
	for i := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		if sts.Spec.Template.Spec.Containers[0].VolumeMounts[i].MountPath == "/tmp" {
			tmpMount = &sts.Spec.Template.Spec.Containers[0].VolumeMounts[i]
			break
		}
	}
	g.Expect(tmpMount).ToNot(BeNil(), "expected /tmp volume mount")
}
