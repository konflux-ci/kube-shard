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
)

func TestBuildAPIServerSCC(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	scc := BuildAPIServerSCC(shard)

	g.Expect(scc.GetKind()).To(Equal("SecurityContextConstraints"))
	g.Expect(scc.GetAPIVersion()).To(Equal("security.openshift.io/v1"))
	g.Expect(scc.GetName()).To(Equal(APIServerSCCName(shard)))

	privEsc, found := unstructuredNestedBool(scc.Object, "allowPrivilegeEscalation")
	g.Expect(found).To(BeTrue())
	g.Expect(privEsc).To(BeTrue(), "SCC must allow privilege escalation for kube-apiserver file capabilities")

	privileged, found := unstructuredNestedBool(scc.Object, "allowPrivilegedContainer")
	g.Expect(found).To(BeTrue())
	g.Expect(privileged).To(BeFalse())
}

func TestBuildAPIServerSCCClusterRole(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cr := BuildAPIServerSCCClusterRole(shard)

	g.Expect(cr.Name).To(Equal(APIServerSCCClusterRoleName(shard)))
	g.Expect(cr.Rules).To(HaveLen(1))

	rule := cr.Rules[0]
	g.Expect(rule.APIGroups).To(ConsistOf("security.openshift.io"))
	g.Expect(rule.Resources).To(ConsistOf("securitycontextconstraints"))
	g.Expect(rule.ResourceNames).To(ConsistOf(APIServerSCCName(shard)))
	g.Expect(rule.Verbs).To(ConsistOf("use"))
}

func TestBuildAPIServerSCCRoleBinding(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	rb := BuildAPIServerSCCRoleBinding(shard)

	g.Expect(rb.Name).To(Equal(APIServerSCCClusterRoleName(shard)))
	g.Expect(rb.Namespace).To(Equal(shard.Spec.TargetNamespace))

	g.Expect(rb.RoleRef.Kind).To(Equal("ClusterRole"))
	g.Expect(rb.RoleRef.Name).To(Equal(APIServerSCCClusterRoleName(shard)))

	g.Expect(rb.Subjects).To(HaveLen(1))
	g.Expect(rb.Subjects[0].Kind).To(Equal("ServiceAccount"))
	g.Expect(rb.Subjects[0].Name).To(Equal(SecondaryServiceAccountName(shard)))
	g.Expect(rb.Subjects[0].Namespace).To(Equal(shard.Spec.TargetNamespace))
}

// unstructuredNestedBool extracts a bool from a nested map structure.
func unstructuredNestedBool(obj map[string]interface{}, fields ...string) (bool, bool) {
	val, found := nestedField(obj, fields...)
	if !found {
		return false, false
	}
	b, ok := val.(bool)
	if !ok {
		return false, true
	}
	return b, true
}

func nestedField(obj map[string]interface{}, fields ...string) (interface{}, bool) {
	var current interface{} = obj
	for _, field := range fields {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		val, exists := m[field]
		if !exists {
			return nil, false
		}
		current = val
	}
	return current, true
}
