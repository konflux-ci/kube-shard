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
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	g.Expect(sts.Spec.VolumeClaimTemplates).To(BeEmpty(), "expected 0 VCTs when persistence is nil")

	var dataVol *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "data" {
			dataVol = &sts.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	g.Expect(dataVol).ToNot(BeNil(), "expected 'data' volume")
	g.Expect(dataVol.EmptyDir).ToNot(BeNil(), "expected EmptyDir volume source when persistence is nil")
}

func TestBuildPostgreSQLStatefulSet_WithPersistence_UsesVCT(t *testing.T) {
	g := NewGomegaWithT(t)
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

	g.Expect(sts.Spec.Template.Spec.Volumes).To(HaveLen(2),
		"tmp + postgresql-tls volumes expected when persistence is set (data via VCT)")
	g.Expect(sts.Spec.Template.Spec.Volumes[0].Name).To(Equal("tmp"))

	g.Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
	vct := sts.Spec.VolumeClaimTemplates[0]
	g.Expect(vct.Name).To(Equal("data"))

	expectedSize := resource.MustParse("50Gi")
	gotSize := vct.Spec.Resources.Requests[corev1.ResourceStorage]
	g.Expect(gotSize.Equal(expectedSize)).To(BeTrue(), "VCT size = %s, want %s", gotSize.String(), expectedSize.String())
	g.Expect(vct.Spec.StorageClassName).ToNot(BeNil())
	g.Expect(*vct.Spec.StorageClassName).To(Equal(storageClass))
	g.Expect(vct.Spec.AccessModes).To(ConsistOf(corev1.ReadWriteOnce))
}

func TestBuildPostgreSQLStatefulSet_PodTemplate(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	g.Expect(sts.Name).To(Equal(PostgreSQLStatefulSetName(shard)))
	g.Expect(sts.Namespace).To(Equal(shard.Spec.TargetNamespace))
	g.Expect(sts.Spec.ServiceName).To(Equal(PostgreSQLServiceName(shard)))

	containers := sts.Spec.Template.Spec.Containers
	g.Expect(containers).To(HaveLen(1))
	g.Expect(containers[0].Name).To(Equal("postgresql"))
	g.Expect(containers[0].Image).To(Equal(DefaultPostgreSQLImage))

	var dataMount *corev1.VolumeMount
	for i := range containers[0].VolumeMounts {
		if containers[0].VolumeMounts[i].Name == "data" {
			dataMount = &containers[0].VolumeMounts[i]
			break
		}
	}
	g.Expect(dataMount).ToNot(BeNil(), "expected 'data' volume mount")
	g.Expect(dataMount.MountPath).To(Equal("/var/lib/postgresql/data"))
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

// TestPostgreSQLDSN verifies the DSN uses sslmode=verify-full and the correct sslrootcert path.
func TestPostgreSQLDSN(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL

	dsn := PostgreSQLDSN(shard, "kine", "secret")
	g.Expect(dsn).To(Equal(
		"postgres://kine:secret@test-shard-postgresql.test-ns.svc:5432/kine?sslmode=verify-full&sslrootcert=/etc/kine/pg-ca/ca.crt",
	))
}

// TestBuildPostgreSQLStatefulSet_TLSVolume verifies the TLS secret volume with per-file modes
// and the postgres -c ssl=on runtime arguments.
func TestBuildPostgreSQLStatefulSet_TLSVolume(t *testing.T) {
	g := NewGomegaWithT(t)
	shard := newTestShard()
	shard.Spec.Storage.Type = kubeshardv1alpha1.StorageTypeInClusterPostgreSQL
	shard.Spec.Storage.InCluster = &kubeshardv1alpha1.InClusterStorage{}

	sts := BuildPostgreSQLStatefulSet(shard)

	var tlsVol *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == postgresqlTLSVolumeName {
			tlsVol = &sts.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	g.Expect(tlsVol).ToNot(BeNil(), "expected postgresql-tls volume")
	g.Expect(tlsVol.Secret).ToNot(BeNil())
	g.Expect(tlsVol.Secret.SecretName).To(Equal("test-shard-postgresql-serving-cert"))
	g.Expect(tlsVol.Secret.Items).To(HaveLen(3))
	for _, item := range tlsVol.Secret.Items {
		g.Expect(item.Mode).ToNot(BeNil())
		if item.Key == "tls.key" {
			g.Expect(*item.Mode).To(Equal(postgresqlTLSKeyMode),
				"tls.key must be 0640 for PostgreSQL")
		} else {
			g.Expect(*item.Mode).To(Equal(postgresqlTLSCertMode),
				"%s should be 0644", item.Key)
		}
	}

	container := sts.Spec.Template.Spec.Containers[0]
	var tlsMount *corev1.VolumeMount
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == postgresqlTLSVolumeName {
			tlsMount = &container.VolumeMounts[i]
			break
		}
	}
	g.Expect(tlsMount).ToNot(BeNil(), "expected postgresql-tls volume mount")
	g.Expect(tlsMount.MountPath).To(Equal(postgresqlTLSMountPath))
	g.Expect(tlsMount.ReadOnly).To(BeTrue())

	g.Expect(container.Args).To(ConsistOf(
		"-c", "ssl=on",
		"-c", "ssl_cert_file="+postgresqlTLSMountPath+"/tls.crt",
		"-c", "ssl_key_file="+postgresqlTLSMountPath+"/tls.key",
		"-c", "ssl_ca_file="+postgresqlTLSMountPath+"/ca.crt",
	))
}
