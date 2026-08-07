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
)

func TestRestrictedPodSecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	psc := RestrictedPodSecurityContext()

	g.Expect(psc.RunAsNonRoot).ToNot(BeNil())
	g.Expect(*psc.RunAsNonRoot).To(BeTrue())
	g.Expect(psc.RunAsUser).To(BeNil(), "RunAsUser must not be set so OpenShift can assign from the namespace range")
	g.Expect(psc.SeccompProfile).ToNot(BeNil())
	g.Expect(psc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
}

func TestRestrictedContainerSecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	sc := RestrictedContainerSecurityContext()

	g.Expect(sc.AllowPrivilegeEscalation).ToNot(BeNil())
	g.Expect(*sc.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(sc.ReadOnlyRootFilesystem).ToNot(BeNil())
	g.Expect(*sc.ReadOnlyRootFilesystem).To(BeTrue())
	g.Expect(sc.Capabilities).ToNot(BeNil())
	g.Expect(sc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
}

func TestAPIServerPodSecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	psc := APIServerPodSecurityContext()

	g.Expect(psc.RunAsNonRoot).ToNot(BeNil())
	g.Expect(*psc.RunAsNonRoot).To(BeTrue())
	g.Expect(psc.RunAsUser).To(BeNil())
	g.Expect(psc.SeccompProfile).ToNot(BeNil())
	g.Expect(psc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
}

func TestAPIServerContainerSecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	sc := APIServerContainerSecurityContext()

	g.Expect(sc.AllowPrivilegeEscalation).ToNot(BeNil())
	g.Expect(*sc.AllowPrivilegeEscalation).To(BeTrue(),
		"AllowPrivilegeEscalation must be true — upstream kube-apiserver has file capabilities")
	g.Expect(sc.ReadOnlyRootFilesystem).ToNot(BeNil())
	g.Expect(*sc.ReadOnlyRootFilesystem).To(BeTrue())
	g.Expect(sc.Capabilities).ToNot(BeNil())
	g.Expect(sc.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
	g.Expect(sc.Capabilities.Add).To(ConsistOf(corev1.Capability("NET_BIND_SERVICE")),
		"NET_BIND_SERVICE must be in bounding set for kube-apiserver file capability exec")
}
