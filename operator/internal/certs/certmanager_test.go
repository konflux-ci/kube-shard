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

package certs

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func newTestShard() *kubeshardv1alpha1.APIShard {
	return &kubeshardv1alpha1.APIShard{
		ObjectMeta: metav1.ObjectMeta{Name: "test-shard"},
		Spec: kubeshardv1alpha1.APIShardSpec{
			TargetNamespace: "test-ns",
			Secondary: kubeshardv1alpha1.SecondarySpec{
				Replicas: 1,
			},
			Kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
			},
		},
	}
}

// certSpec is a helper that extracts the spec map from a cert-manager Certificate.
func certSpec(cert *unstructured.Unstructured) map[string]interface{} {
	spec, _, _ := unstructured.NestedMap(cert.Object, "spec")
	return spec
}

// issuerRef extracts the issuerRef map from a cert-manager Certificate spec.
func issuerRef(cert *unstructured.Unstructured) map[string]interface{} {
	ref, _, _ := unstructured.NestedMap(cert.Object, "spec", "issuerRef")
	return ref
}

// TestBuildServingCertificate verifies the refactored serving certificate builder
// produces the correct resource for the secondary API server.
func TestBuildServingCertificate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cert := BuildServingCertificate(shard)

	g.Expect(cert.GetKind()).To(Equal("Certificate"))
	g.Expect(cert.GetAPIVersion()).To(Equal("cert-manager.io/v1"))
	g.Expect(cert.GetName()).To(Equal("test-shard-serving-cert"))
	g.Expect(cert.GetNamespace()).To(Equal("test-ns"))

	spec := certSpec(cert)
	g.Expect(spec["secretName"]).To(Equal("test-shard-pki"))
	g.Expect(spec["duration"]).To(Equal("8760h"))
	g.Expect(spec["renewBefore"]).To(Equal("720h"))

	ref := issuerRef(cert)
	g.Expect(ref["name"]).To(Equal("test-shard-ca-issuer"))
	g.Expect(ref["kind"]).To(Equal("Issuer"))
	g.Expect(ref["group"]).To(Equal("cert-manager.io"))

	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	g.Expect(dnsNames).To(ContainElement("test-shard-apiserver"))
	g.Expect(dnsNames).To(ContainElement("test-shard-apiserver.test-ns"))
	g.Expect(dnsNames).To(ContainElement("test-shard-apiserver.test-ns.svc"))
	g.Expect(dnsNames).To(ContainElement("test-shard-apiserver.test-ns.svc.cluster.local"))
}

// TestBuildKineServingCertificate verifies the Kine serving certificate uses the
// Kine-dedicated CA issuer and the correct service DNS SANs.
func TestBuildKineServingCertificate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cert := BuildKineServingCertificate(shard)

	g.Expect(cert.GetKind()).To(Equal("Certificate"))
	g.Expect(cert.GetName()).To(Equal("test-shard-kine-serving"))
	g.Expect(cert.GetNamespace()).To(Equal("test-ns"))

	spec := certSpec(cert)
	g.Expect(spec["secretName"]).To(Equal("test-shard-kine-serving-cert"))
	g.Expect(spec["duration"]).To(Equal("8760h"))
	g.Expect(spec["renewBefore"]).To(Equal("720h"))

	ref := issuerRef(cert)
	g.Expect(ref["name"]).To(Equal("test-shard-kine-ca-issuer"),
		"Kine serving cert must be issued by the Kine-dedicated CA")

	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	g.Expect(dnsNames).To(ContainElement("test-shard-kine"))
	g.Expect(dnsNames).To(ContainElement("test-shard-kine.test-ns.svc.cluster.local"))
}

// TestBuildEtcdClientCertificate verifies the etcd client certificate uses the
// Kine-dedicated CA issuer and has clientAuth key usage.
func TestBuildEtcdClientCertificate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cert := BuildEtcdClientCertificate(shard)

	g.Expect(cert.GetKind()).To(Equal("Certificate"))
	g.Expect(cert.GetName()).To(Equal("test-shard-etcd-client"))
	g.Expect(cert.GetNamespace()).To(Equal("test-ns"))

	spec := certSpec(cert)
	g.Expect(spec["secretName"]).To(Equal("test-shard-etcd-client-cert"))
	g.Expect(spec["commonName"]).To(Equal("test-shard-etcd-client"))
	g.Expect(spec["duration"]).To(Equal("8760h"))
	g.Expect(spec["renewBefore"]).To(Equal("720h"))

	ref := issuerRef(cert)
	g.Expect(ref["name"]).To(Equal("test-shard-kine-ca-issuer"),
		"etcd client cert must be issued by the Kine-dedicated CA")

	usages, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "usages")
	g.Expect(usages).To(ConsistOf("client auth"))
}

// TestBuildKineCACertificate verifies the Kine CA certificate is a CA cert
// issued by the Kine self-signed issuer.
func TestBuildKineCACertificate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cert := BuildKineCACertificate(shard)

	g.Expect(cert.GetKind()).To(Equal("Certificate"))
	g.Expect(cert.GetName()).To(Equal("test-shard-kine-ca"))
	g.Expect(cert.GetNamespace()).To(Equal("test-ns"))

	spec := certSpec(cert)
	g.Expect(spec["isCA"]).To(BeTrue())
	g.Expect(spec["commonName"]).To(Equal("test-shard-kine-ca"))
	g.Expect(spec["secretName"]).To(Equal("test-shard-kine-ca-keypair"))
	g.Expect(spec["duration"]).To(Equal("87600h")) // 10 years

	ref := issuerRef(cert)
	g.Expect(ref["name"]).To(Equal("test-shard-kine-selfsigned"))
	g.Expect(ref["kind"]).To(Equal("Issuer"))
}

// TestBuildKineCAIssuer verifies the Kine CA-backed issuer references the
// correct CA secret.
func TestBuildKineCAIssuer(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	issuer := BuildKineCAIssuer(shard)

	g.Expect(issuer.GetKind()).To(Equal("Issuer"))
	g.Expect(issuer.GetName()).To(Equal("test-shard-kine-ca-issuer"))
	g.Expect(issuer.GetNamespace()).To(Equal("test-ns"))

	caSecretName, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName")
	g.Expect(caSecretName).To(Equal("test-shard-kine-ca-keypair"))
}

// TestBuildKineSelfSignedIssuer verifies the Kine self-signed issuer is
// independent from the shard-wide self-signed issuer.
func TestBuildKineSelfSignedIssuer(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	issuer := BuildKineSelfSignedIssuer(shard)

	g.Expect(issuer.GetKind()).To(Equal("Issuer"))
	g.Expect(issuer.GetName()).To(Equal("test-shard-kine-selfsigned"))
	g.Expect(issuer.GetNamespace()).To(Equal("test-ns"))

	selfSigned, found, _ := unstructured.NestedMap(issuer.Object, "spec", "selfSigned")
	g.Expect(found).To(BeTrue(), "expected selfSigned spec")
	g.Expect(selfSigned).To(BeEmpty())

	// Verify independence from shard-wide self-signed issuer.
	shardIssuer := BuildSelfSignedIssuer(shard)
	g.Expect(issuer.GetName()).ToNot(Equal(shardIssuer.GetName()),
		"Kine self-signed issuer must be distinct from shard-wide self-signed issuer")
}

// TestBuildAdminClientCertificate verifies the admin client certificate uses the
// shard-wide CA (not the Kine CA) and includes system:masters organization.
func TestBuildAdminClientCertificate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	cert := BuildAdminClientCertificate(shard)

	g.Expect(cert.GetKind()).To(Equal("Certificate"))
	g.Expect(cert.GetName()).To(Equal("test-shard-admin-client"))

	spec := certSpec(cert)
	g.Expect(spec["secretName"]).To(Equal("test-shard-admin-client-cert"))
	g.Expect(spec["commonName"]).To(Equal("kube-shard-admin"))

	ref := issuerRef(cert)
	g.Expect(ref["name"]).To(Equal("test-shard-ca-issuer"),
		"admin client cert must use the shard-wide CA, not Kine CA")

	usages, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "usages")
	g.Expect(usages).To(ConsistOf("client auth"))

	orgs, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "subject", "organizations")
	g.Expect(orgs).To(ConsistOf("system:masters"))
}

// TestCertLabels verifies that all cert resources include the expected labels.
func TestCertLabels(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	builders := []struct {
		name string
		fn   func() *unstructured.Unstructured
	}{
		{"BuildServingCertificate", func() *unstructured.Unstructured { return BuildServingCertificate(shard) }},
		{"BuildKineServingCertificate", func() *unstructured.Unstructured { return BuildKineServingCertificate(shard) }},
		{"BuildEtcdClientCertificate", func() *unstructured.Unstructured { return BuildEtcdClientCertificate(shard) }},
		{"BuildKineCACertificate", func() *unstructured.Unstructured { return BuildKineCACertificate(shard) }},
		{"BuildAdminClientCertificate", func() *unstructured.Unstructured { return BuildAdminClientCertificate(shard) }},
		{"BuildKineCAIssuer", func() *unstructured.Unstructured { return BuildKineCAIssuer(shard) }},
		{"BuildKineSelfSignedIssuer", func() *unstructured.Unstructured { return BuildKineSelfSignedIssuer(shard) }},
	}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			g = NewGomegaWithT(t)
			res := b.fn()
			labels := res.GetLabels()
			g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kube-shard-operator"))
			g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-shard"))
			g.Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/component", "certificates"))
		})
	}
}

// TestKineTrustBoundaryIsolation verifies that the Kine serving cert and etcd
// client cert use the Kine-dedicated CA, while the admin client cert and API
// server serving cert use the shard-wide CA.
func TestKineTrustBoundaryIsolation(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()

	kineServing := issuerRef(BuildKineServingCertificate(shard))
	etcdClient := issuerRef(BuildEtcdClientCertificate(shard))
	adminClient := issuerRef(BuildAdminClientCertificate(shard))
	apiServing := issuerRef(BuildServingCertificate(shard))

	// Kine serving and etcd client must use the Kine CA.
	g.Expect(kineServing["name"]).To(Equal(KineCAIssuerName(shard)))
	g.Expect(etcdClient["name"]).To(Equal(KineCAIssuerName(shard)))

	// Admin client and API serving must use the shard-wide CA.
	g.Expect(adminClient["name"]).To(Equal(IssuerName(shard)))
	g.Expect(apiServing["name"]).To(Equal(IssuerName(shard)))

	// The two CAs must be different.
	g.Expect(KineCAIssuerName(shard)).ToNot(Equal(IssuerName(shard)),
		"Kine CA and shard-wide CA must be distinct")
}
