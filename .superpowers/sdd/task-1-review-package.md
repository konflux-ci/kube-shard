732b8c2 Add BuildPDB helper for multi-replica deployments
---STAT---
 operator/internal/resources/pdb.go      | 51 ++++++++++++++++++++
 operator/internal/resources/pdb_test.go | 83 +++++++++++++++++++++++++++++++++
 2 files changed, 134 insertions(+)
---DIFF---
diff --git a/operator/internal/resources/pdb.go b/operator/internal/resources/pdb.go
new file mode 100644
index 0000000..4962ae2
--- /dev/null
+++ b/operator/internal/resources/pdb.go
@@ -0,0 +1,51 @@
+/*
+Copyright 2026.
+
+Licensed under the Apache License, Version 2.0 (the "License");
+you may not use this file except in compliance with the License.
+You may obtain a copy of the License at
+
+    http://www.apache.org/licenses/LICENSE-2.0
+
+Unless required by applicable law or agreed to in writing, software
+distributed under the License is distributed on an "AS IS" BASIS,
+WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
+See the License for the specific language governing permissions and
+limitations under the License.
+*/
+
+package resources
+
+import (
+	policyv1 "k8s.io/api/policy/v1"
+	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
+	"k8s.io/apimachinery/pkg/util/intstr"
+)
+
+// BuildPDB constructs a PodDisruptionBudget for a multi-replica deployment.
+// Returns nil when replicas is less than 2, since single-replica deployments
+// do not benefit from a PDB.
+func BuildPDB(name, namespace string, replicas int32, selector map[string]string) *policyv1.PodDisruptionBudget {
+	if replicas < 2 {
+		return nil
+	}
+
+	maxUnavailable := intstr.FromInt32(1)
+	return &policyv1.PodDisruptionBudget{
+		TypeMeta: metav1.TypeMeta{
+			APIVersion: "policy/v1",
+			Kind:       "PodDisruptionBudget",
+		},
+		ObjectMeta: metav1.ObjectMeta{
+			Name:      name,
+			Namespace: namespace,
+			Labels:    selector,
+		},
+		Spec: policyv1.PodDisruptionBudgetSpec{
+			MaxUnavailable: &maxUnavailable,
+			Selector: &metav1.LabelSelector{
+				MatchLabels: selector,
+			},
+		},
+	}
+}
diff --git a/operator/internal/resources/pdb_test.go b/operator/internal/resources/pdb_test.go
new file mode 100644
index 0000000..8a5606b
--- /dev/null
+++ b/operator/internal/resources/pdb_test.go
@@ -0,0 +1,83 @@
+/*
+Copyright 2026.
+
+Licensed under the Apache License, Version 2.0 (the "License");
+you may not use this file except in compliance with the License.
+You may obtain a copy of the License at
+
+    http://www.apache.org/licenses/LICENSE-2.0
+
+Unless required by applicable law or agreed to in writing, software
+distributed under the License is distributed on an "AS IS" BASIS,
+WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
+See the License for the specific language governing permissions and
+limitations under the License.
+*/
+
+package resources
+
+import (
+	"testing"
+
+	. "github.com/onsi/gomega"
+	"k8s.io/apimachinery/pkg/util/intstr"
+)
+
+func TestBuildPDB_ReturnsNil_WhenReplicasLessThan2(t *testing.T) {
+	g := NewGomegaWithT(t)
+
+	pdb := BuildPDB("test-pdb", "test-ns", 1, map[string]string{"app": "test"})
+
+	g.Expect(pdb).To(BeNil())
+}
+
+func TestBuildPDB_ReturnsNil_WhenReplicasZero(t *testing.T) {
+	g := NewGomegaWithT(t)
+
+	pdb := BuildPDB("test-pdb", "test-ns", 0, map[string]string{"app": "test"})
+
+	g.Expect(pdb).To(BeNil())
+}
+
+func TestBuildPDB_ReturnsPDB_WhenReplicasTwo(t *testing.T) {
+	g := NewGomegaWithT(t)
+	selector := map[string]string{
+		"app.kubernetes.io/name":      "kine",
+		"app.kubernetes.io/instance":  "test-shard",
+		"app.kubernetes.io/managed-by": "kube-shard-operator",
+	}
+
+	pdb := BuildPDB("test-shard-kine", "test-ns", 2, selector)
+
+	g.Expect(pdb).ToNot(BeNil())
+	g.Expect(pdb.APIVersion).To(Equal("policy/v1"))
+	g.Expect(pdb.Kind).To(Equal("PodDisruptionBudget"))
+	g.Expect(pdb.Name).To(Equal("test-shard-kine"))
+	g.Expect(pdb.Namespace).To(Equal("test-ns"))
+	g.Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromInt32(1)))
+}
+
+func TestBuildPDB_ReturnsPDB_WhenReplicasThree(t *testing.T) {
+	g := NewGomegaWithT(t)
+
+	pdb := BuildPDB("test-pdb", "test-ns", 3, map[string]string{"app": "test"})
+
+	g.Expect(pdb).ToNot(BeNil())
+	g.Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromInt32(1)))
+}
+
+func TestBuildPDB_CorrectLabelsAndSelector(t *testing.T) {
+	g := NewGomegaWithT(t)
+	selector := map[string]string{
+		"app.kubernetes.io/name":     "apiserver",
+		"app.kubernetes.io/instance":   "test-shard",
+		"app.kubernetes.io/component":  "apiserver",
+		"app.kubernetes.io/managed-by": "kube-shard-operator",
+	}
+
+	pdb := BuildPDB("test-shard-secondary", "test-ns", 2, selector)
+
+	g.Expect(pdb).ToNot(BeNil())
+	g.Expect(pdb.Labels).To(Equal(selector))
+	g.Expect(pdb.Spec.Selector.MatchLabels).To(Equal(selector))
+}
