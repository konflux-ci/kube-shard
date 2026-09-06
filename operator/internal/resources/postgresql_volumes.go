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
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// AlignPostgreSQLVolumeClaims copies live VolumeClaimTemplates onto desired
// so a later apply does not attempt an immutable field update. It also keeps
// the pod template data volume consistent with whether the live StatefulSet
// uses a PVC or emptyDir.
func AlignPostgreSQLVolumeClaims(desired, existing *appsv1.StatefulSet) {
	desired.Spec.VolumeClaimTemplates = append(
		[]corev1.PersistentVolumeClaim(nil),
		existing.Spec.VolumeClaimTemplates...,
	)

	liveVCTNames := volumeClaimTemplateNames(existing.Spec.VolumeClaimTemplates)
	if liveVCTNames.Len() > 0 {
		desired.Spec.Template.Spec.Volumes = slices.DeleteFunc(
			desired.Spec.Template.Spec.Volumes,
			func(v corev1.Volume) bool { return liveVCTNames.Has(v.Name) },
		)
		return
	}

	desiredNames := volumeNames(desired.Spec.Template.Spec.Volumes)
	for _, v := range existing.Spec.Template.Spec.Volumes {
		if v.EmptyDir == nil || desiredNames.Has(v.Name) {
			continue
		}
		desired.Spec.Template.Spec.Volumes = append(desired.Spec.Template.Spec.Volumes, v)
	}
}

// VolumeClaimTemplatesMatch reports whether spec-requested VCTs are compatible
// with the live StatefulSet. A nil storageClassName in the spec matches a live
// defaulted class name so API-server defaulting is not treated as a user
// change. It does not match a live explicit empty string.
func VolumeClaimTemplatesMatch(requested, live []corev1.PersistentVolumeClaim) bool {
	if len(requested) != len(live) {
		return false
	}
	for i := range requested {
		if requested[i].Name != live[i].Name {
			return false
		}
		reqSize := requested[i].Spec.Resources.Requests[corev1.ResourceStorage]
		liveSize := live[i].Spec.Resources.Requests[corev1.ResourceStorage]
		if reqSize.Cmp(liveSize) != 0 {
			return false
		}
		if !storageClassNamesMatch(requested[i].Spec.StorageClassName, live[i].Spec.StorageClassName) {
			return false
		}
	}
	return true
}

// DescribeVolumeClaimTemplates returns a short human-readable summary of VCTs
// for logs and status messages.
func DescribeVolumeClaimTemplates(vcts []corev1.PersistentVolumeClaim) string {
	if len(vcts) == 0 {
		return "emptyDir (no persistent volume)"
	}
	size := vcts[0].Spec.Resources.Requests[corev1.ResourceStorage]
	sc := describeStorageClassName(vcts[0].Spec.StorageClassName)
	return fmt.Sprintf("size %s, storageClassName %s", size.String(), sc)
}

// storageClassNamesMatch compares requested and live StorageClassName values.
// A nil requested class matches a live defaulted class name because the API
// server may fill in the cluster default. It does not match a live explicit
// empty string, which disables dynamic provisioning.
func storageClassNamesMatch(requested, live *string) bool {
	if requested == nil {
		return live == nil || *live != ""
	}
	if live == nil {
		return false
	}
	return *requested == *live
}

// describeStorageClassName renders a StorageClassName pointer for logs and
// status messages. Nil means the cluster default; an explicit empty string
// disables dynamic provisioning.
func describeStorageClassName(sc *string) string {
	if sc == nil {
		return "<default>"
	}
	if *sc == "" {
		return "<none>"
	}
	return *sc
}

// volumeClaimTemplateNames returns the set of VolumeClaimTemplate names.
func volumeClaimTemplateNames(vcts []corev1.PersistentVolumeClaim) sets.Set[string] {
	names := sets.New[string]()
	for _, vct := range vcts {
		names.Insert(vct.Name)
	}
	return names
}

// volumeNames returns the set of pod volume names.
func volumeNames(vols []corev1.Volume) sets.Set[string] {
	names := sets.New[string]()
	for _, v := range vols {
		names.Insert(v.Name)
	}
	return names
}
