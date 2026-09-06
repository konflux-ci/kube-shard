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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// TestAlignPostgreSQLVolumeClaims_PreservesLiveSize verifies that a larger
// requested size is overwritten by the live VolumeClaimTemplate size.
func TestAlignPostgreSQLVolumeClaims_PreservesLiveSize(t *testing.T) {
	g := NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))
	desired := BuildPostgreSQLStatefulSet(persistentShard("100Gi"))

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(HaveLen(1))
	got := desired.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	g.Expect(got.String()).To(Equal("20Gi"))
}

// TestAlignPostgreSQLVolumeClaims_EmptyDirToPersistenceKeepsEmptyDir verifies
// that enabling persistence on an emptyDir StatefulSet does not add a VCT.
func TestAlignPostgreSQLVolumeClaims_EmptyDirToPersistenceKeepsEmptyDir(t *testing.T) {
	g := NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(emptyDirShard())
	desired := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(BeEmpty())
	g.Expect(hasEmptyDirVolume(desired.Spec.Template.Spec.Volumes, dataVolumeName)).To(BeTrue())
}

// TestAlignPostgreSQLVolumeClaims_PersistenceToEmptyDirKeepsVCT verifies that
// removing persistence keeps the live VolumeClaimTemplate.
func TestAlignPostgreSQLVolumeClaims_PersistenceToEmptyDirKeepsVCT(t *testing.T) {
	g := NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))
	desired := BuildPostgreSQLStatefulSet(emptyDirShard())

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(HaveLen(1))
	g.Expect(hasEmptyDirVolume(desired.Spec.Template.Spec.Volumes, dataVolumeName)).To(BeFalse())
}

// TestVolumeClaimTemplatesMatch covers size and storage-class comparison,
// including nil versus explicit empty storageClassName.
func TestVolumeClaimTemplatesMatch(t *testing.T) {
	g := NewGomegaWithT(t)
	twenty := vct("20Gi", nil)
	hundred := vct("100Gi", nil)
	withClass := vct("20Gi", ptr.To("gp3"))
	none := vct("20Gi", ptr.To(""))
	one := func(c corev1.PersistentVolumeClaim) []corev1.PersistentVolumeClaim {
		return []corev1.PersistentVolumeClaim{c}
	}

	g.Expect(VolumeClaimTemplatesMatch(nil, nil)).To(BeTrue())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(twenty))).To(BeTrue())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(hundred))).To(BeFalse())
	g.Expect(VolumeClaimTemplatesMatch(nil, one(twenty))).To(BeFalse())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(withClass))).To(BeTrue(),
		"nil requested storage class must ignore live defaulting")
	g.Expect(VolumeClaimTemplatesMatch(one(withClass), one(twenty))).To(BeFalse())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(none))).To(BeFalse(),
		"nil requested class must not match a live explicit empty class")
	g.Expect(VolumeClaimTemplatesMatch(one(none), one(none))).To(BeTrue())
}

// TestDescribeVolumeClaimTemplates distinguishes omitted, empty, and named
// storage classes in status text.
func TestDescribeVolumeClaimTemplates(t *testing.T) {
	g := NewGomegaWithT(t)

	g.Expect(DescribeVolumeClaimTemplates(nil)).To(Equal("emptyDir (no persistent volume)"))
	g.Expect(DescribeVolumeClaimTemplates([]corev1.PersistentVolumeClaim{
		vct("20Gi", nil),
	})).To(ContainSubstring("storageClassName <default>"))
	g.Expect(DescribeVolumeClaimTemplates([]corev1.PersistentVolumeClaim{
		vct("20Gi", ptr.To("")),
	})).To(ContainSubstring("storageClassName <none>"))
	g.Expect(DescribeVolumeClaimTemplates([]corev1.PersistentVolumeClaim{
		vct("20Gi", ptr.To("gp3")),
	})).To(ContainSubstring("storageClassName gp3"))
}

// persistentShard returns an APIShard with InClusterPostgreSQL persistence
// of the given size and no explicit storage class.
func persistentShard(size string) *kubeshardv1alpha1.APIShard {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{
		Persistence: &kubeshardv1alpha1.PersistenceSpec{
			Size: resource.MustParse(size),
		},
	}
	return shard
}

// emptyDirShard returns an APIShard with InClusterPostgreSQL and no persistence.
func emptyDirShard() *kubeshardv1alpha1.APIShard {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}
	return shard
}

// vct builds a VolumeClaimTemplate-shaped PVC named data for comparison tests.
func vct(size string, storageClass *string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: dataVolumeName},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}
}

// hasEmptyDirVolume reports whether vols contains an emptyDir volume with the
// given name.
func hasEmptyDirVolume(vols []corev1.Volume, name string) bool {
	for _, v := range vols {
		if v.Name == name && v.EmptyDir != nil {
			return true
		}
	}
	return false
}
