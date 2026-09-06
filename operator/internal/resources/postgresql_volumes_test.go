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

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func TestAlignPostgreSQLVolumeClaims_PreservesLiveSize(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))
	desired := BuildPostgreSQLStatefulSet(persistentShard("100Gi"))

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(gomega.HaveLen(1))
	got := desired.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	g.Expect(got.String()).To(gomega.Equal("20Gi"))
}

func TestAlignPostgreSQLVolumeClaims_EmptyDirToPersistenceKeepsEmptyDir(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(emptyDirShard())
	desired := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(gomega.BeEmpty())
	g.Expect(hasEmptyDirVolume(desired.Spec.Template.Spec.Volumes, dataVolumeName)).To(gomega.BeTrue())
}

func TestAlignPostgreSQLVolumeClaims_PersistenceToEmptyDirKeepsVCT(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	live := BuildPostgreSQLStatefulSet(persistentShard("20Gi"))
	desired := BuildPostgreSQLStatefulSet(emptyDirShard())

	AlignPostgreSQLVolumeClaims(desired, live)

	g.Expect(desired.Spec.VolumeClaimTemplates).To(gomega.HaveLen(1))
	g.Expect(hasEmptyDirVolume(desired.Spec.Template.Spec.Volumes, dataVolumeName)).To(gomega.BeFalse())
}

func TestVolumeClaimTemplatesMatch(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	twenty := vct(dataVolumeName, "20Gi", nil)
	hundred := vct(dataVolumeName, "100Gi", nil)
	withClass := vct(dataVolumeName, "20Gi", ptr.To("gp3"))
	one := func(c corev1.PersistentVolumeClaim) []corev1.PersistentVolumeClaim {
		return []corev1.PersistentVolumeClaim{c}
	}

	g.Expect(VolumeClaimTemplatesMatch(nil, nil)).To(gomega.BeTrue())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(twenty))).To(gomega.BeTrue())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(hundred))).To(gomega.BeFalse())
	g.Expect(VolumeClaimTemplatesMatch(nil, one(twenty))).To(gomega.BeFalse())
	g.Expect(VolumeClaimTemplatesMatch(one(twenty), one(withClass))).To(gomega.BeTrue(),
		"nil requested storage class must ignore live defaulting")
	g.Expect(VolumeClaimTemplatesMatch(one(withClass), one(twenty))).To(gomega.BeFalse())
}

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

func emptyDirShard() *kubeshardv1alpha1.APIShard {
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}
	return shard
}

func vct(name, size string, storageClass *string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
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

func hasEmptyDirVolume(vols []corev1.Volume, name string) bool {
	for _, v := range vols {
		if v.Name == name && v.EmptyDir != nil {
			return true
		}
	}
	return false
}
