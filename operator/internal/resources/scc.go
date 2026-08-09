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

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

// APIServerSCCName returns the name of the custom SecurityContextConstraints
// created for the secondary kube-apiserver. The upstream kube-apiserver binary
// carries a cap_net_bind_service file capability that is incompatible with the
// restricted-v2 SCC's allowPrivilegeEscalation=false requirement.
func APIServerSCCName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver", shard.Name)
}

// APIServerSCCClusterRoleName returns the name of the ClusterRole that grants
// "use" on the custom apiserver SCC.
func APIServerSCCClusterRoleName(shard *kubeshardv1alpha1.APIShard) string {
	return fmt.Sprintf("%s-apiserver-scc", shard.Name)
}

// BuildAPIServerSCC constructs a SecurityContextConstraints resource for the
// secondary kube-apiserver. It mirrors the OpenShift restricted-v2 SCC with one
// exception: allowPrivilegeEscalation is set to true so that the kube-apiserver
// binary's file capabilities do not trigger a kernel EPERM during exec().
// Built as unstructured to avoid a dependency on the OpenShift API module.
func BuildAPIServerSCC(shard *kubeshardv1alpha1.APIShard) *unstructured.Unstructured {
	name := APIServerSCCName(shard)

	scc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "security.openshift.io/v1",
			"kind":       "SecurityContextConstraints",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					LabelInstance:  shard.Name,
					LabelManagedBy: ManagedByValue,
					LabelComponent: ComponentAPIServer,
				},
			},
			"allowPrivilegeEscalation": true,
			"allowPrivilegedContainer": false,
			"allowHostDirVolumePlugin": false,
			"allowHostIPC":             false,
			"allowHostNetwork":         false,
			"allowHostPID":             false,
			"allowHostPorts":           false,
			"allowedCapabilities":      []interface{}{"NET_BIND_SERVICE"},
			"defaultAddCapabilities":   nil,
			"requiredDropCapabilities": []interface{}{"ALL"},
			"readOnlyRootFilesystem":   false, // not enforced by this SCC
			"runAsUser":                map[string]interface{}{"type": "MustRunAsRange"},
			"seLinuxContext":           map[string]interface{}{"type": "MustRunAs"},
			"fsGroup":                  map[string]interface{}{"type": "MustRunAs"},
			"supplementalGroups":       map[string]interface{}{"type": "RunAsAny"},
			"seccompProfiles":          []interface{}{"runtime/default"},
			"volumes":                  []interface{}{"configMap", "csi", "downwardAPI", "emptyDir", "ephemeral", "persistentVolumeClaim", "projected", "secret"},
		},
	}

	return scc
}

// BuildAPIServerSCCClusterRole constructs a ClusterRole that grants the "use"
// verb on the custom apiserver SCC.
func BuildAPIServerSCCClusterRole(shard *kubeshardv1alpha1.APIShard) *rbacv1.ClusterRole {
	name := APIServerSCCClusterRoleName(shard)
	labels := map[string]string{
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentAPIServer,
	}

	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{APIServerSCCName(shard)},
				Verbs:         []string{"use"},
			},
		},
	}
}

// BuildAPIServerSCCRoleBinding constructs a RoleBinding in the target namespace
// that grants the apiserver ServiceAccount permission to use the custom SCC.
func BuildAPIServerSCCRoleBinding(shard *kubeshardv1alpha1.APIShard) *rbacv1.RoleBinding {
	name := APIServerSCCClusterRoleName(shard)
	labels := map[string]string{
		LabelInstance:  shard.Name,
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentAPIServer,
	}

	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: shard.Spec.TargetNamespace,
			Labels:    labels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     APIServerSCCClusterRoleName(shard),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      SecondaryServiceAccountName(shard),
				Namespace: shard.Spec.TargetNamespace,
			},
		},
	}
}
