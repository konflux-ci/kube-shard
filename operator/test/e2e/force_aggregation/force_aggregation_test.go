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

package force_aggregation

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	shardName       = "e2e-force-agg-shard"
	shardNamespace  = "e2e-force-agg-ns"
	gadgetName      = "force-agg-gadget"
	gadgetNamespace = "e2e-force-agg-workload"
)

var _ = Describe("Force Aggregation", Ordered, func() {
	var testdataDir string

	BeforeAll(func() {
		projectDir := getProjectDir()
		testdataDir = filepath.Join(projectDir, "test", "e2e", "testdata")

		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "gadget", gadgetName, "-n", gadgetNamespace, "--ignore-not-found"},
			{"delete", "apiservice", "v1.forceagg.example.com", "--ignore-not-found"},
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "crd", "gadgets.forceagg.example.com", "--ignore-not-found"},
			{"delete", "ns", gadgetNamespace, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = run(cmd)
		}

		By("waiting for namespaces to be fully deleted")
		Eventually(func(g Gomega) {
			for _, ns := range []string{shardNamespace, gadgetNamespace} {
				cmd := exec.Command("kubectl", "get", "ns", ns, "--no-headers")
				output, _ := run(cmd)
				g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
					fmt.Sprintf("namespace %s should be deleted", ns))
			}
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the APIShard with forceAggregation enabled")
		apishardYAML := fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %s
spec:
  targetNamespace: %s
  forceAggregation: true
  apiGroups:
    - group: forceagg.example.com
      versions:
        - v1
  storage:
    type: SQLite
  namespaceSync:
    labelSelector:
      matchLabels:
        e2e-test: force-agg-shard
  secondary:
    replicas: 1
  kine:
    replicas: 1
`, shardName, shardNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(apishardYAML)
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create APIShard")

		By("waiting for APIShard to become Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("creating the workload namespace with sync label")
		cmd = exec.Command("kubectl", "create", "ns", gadgetNamespace)
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create workload namespace")
		cmd = exec.Command("kubectl", "label", "ns", gadgetNamespace,
			"e2e-test=force-agg-shard")
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for namespace to be synced to the secondary")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "namespacesync",
				"-n", shardNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", shardName),
				"-o", `jsonpath={.items[0].status.conditions[?(@.type=="Ready")].status}`)
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("True"), "NamespaceSync should be Ready")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		if os.Getenv("SKIP_CLEANUP") == "true" {
			By("SKIP_CLEANUP=true — leaving resources in place for inspection")
			return
		}
		By("cleaning up Gadget CR")
		cmd := exec.Command("kubectl", "delete", "gadget", gadgetName,
			"-n", gadgetNamespace, "--ignore-not-found", "--wait=false")
		_, _ = run(cmd)

		By("cleaning up CRD from primary if present")
		cmd = exec.Command("kubectl", "delete", "crd", "gadgets.forceagg.example.com", "--ignore-not-found")
		_, _ = run(cmd)

		By("cleaning up APIService")
		cmd = exec.Command("kubectl", "delete", "apiservice",
			"v1.forceagg.example.com", "--ignore-not-found")
		_, _ = run(cmd)

		By("cleaning up APIShard")
		cmd = exec.Command("kubectl", "delete", "apishard", shardName,
			"--ignore-not-found", "--wait=false")
		_, _ = run(cmd)

		By("cleaning up workload namespace")
		cmd = exec.Command("kubectl", "delete", "ns", gadgetNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = run(cmd)
	})

	It("should not block when a conflicting CRD is installed on the primary", func() {
		By("installing the Gadget CRD on the primary (simulating a pre-existing CRD)")
		cmd := exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "gadget_crd.yaml"))
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRD on primary")

		By("waiting for CRDConflict condition with ForcedAggregation reason")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.conditions[?(@.type=='CRDConflict')].reason}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("ForcedAggregation"),
				"CRDConflict reason should be ForcedAggregation when force is enabled")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying phase is NOT Blocked (forceAggregation prevents blocking)")
		cmd = exec.Command("kubectl", "get", "apishard", shardName,
			"-o", "jsonpath={.status.phase}")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).NotTo(Equal("Blocked"),
			"Phase should not be Blocked when forceAggregation is true")
	})

	It("should label the APIService as automanaged=false", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apiservice", "v1.forceagg.example.com",
				"-o", "jsonpath={.metadata.labels.kube-aggregator\\.kubernetes\\.io/automanaged}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("false"),
				"APIService should have automanaged=false when forceAggregation is enabled")
		}, 30*time.Second, 5*time.Second).Should(Succeed())
	})

	It("should route Gadget CRs through aggregation despite the CRD conflict", func() {
		By("creating a Gadget CR via the primary API server")
		gadgetYAML := fmt.Sprintf(`apiVersion: forceagg.example.com/v1
kind: Gadget
metadata:
  name: %s
  namespace: %s
spec:
  message: "hello from force-aggregation e2e"
`, gadgetName, gadgetNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(gadgetYAML)
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Gadget CR")

		By("verifying the Gadget is retrievable via the primary API")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "gadget", gadgetName,
				"-n", gadgetNamespace,
				"-o", "jsonpath={.spec.message}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("hello from force-aggregation e2e"))
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

	It("should store the resource on the secondary API server", func() {
		tmpDir, err := os.MkdirTemp("", "e2e-force-agg-auth-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(tmpDir) }()

		By("extracting admin client credentials from cluster secrets")
		for _, item := range []struct {
			secret, key, file string
		}{
			{fmt.Sprintf("%s-admin-client-cert", shardName), "tls.crt", "tls.crt"},
			{fmt.Sprintf("%s-admin-client-cert", shardName), "tls.key", "tls.key"},
			{fmt.Sprintf("%s-pki", shardName), "ca.crt", "ca.crt"},
		} {
			cmd := exec.Command("kubectl", "get", "secret", item.secret,
				"-n", shardNamespace,
				"-o", fmt.Sprintf("jsonpath={.data.%s}", strings.ReplaceAll(item.key, ".", "\\.")))
			b64, err := run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to read %s from %s", item.key, item.secret)

			cmd = exec.Command("base64", "-d")
			cmd.Stdin = stringReader(b64)
			decoded, err := run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(tmpDir, item.file), []byte(decoded), 0600)).To(Succeed())
		}

		By("finding a free local port for port-forward")
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		localPort := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()

		By(fmt.Sprintf("port-forwarding to secondary API server on localhost:%d", localPort))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pfCmd := exec.CommandContext(ctx, "kubectl", "port-forward",
			fmt.Sprintf("svc/%s-apiserver", shardName),
			fmt.Sprintf("%d:443", localPort),
			"-n", shardNamespace)
		pfCmd.Stdout = GinkgoWriter
		pfCmd.Stderr = GinkgoWriter
		Expect(pfCmd.Start()).To(Succeed())
		defer func() {
			cancel()
			_ = pfCmd.Wait()
		}()

		By("waiting for port-forward to be ready")
		Eventually(func() error {
			conn, err := net.DialTimeout("tcp",
				fmt.Sprintf("127.0.0.1:%d", localPort), time.Second)
			if err != nil {
				return err
			}
			_ = conn.Close()
			return nil
		}, 30*time.Second, time.Second).Should(Succeed())

		By("writing a standalone kubeconfig for the secondary")
		kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: secondary
  cluster:
    server: https://127.0.0.1:%d
    insecure-skip-tls-verify: true
users:
- name: admin
  user:
    client-certificate: %s
    client-key: %s
contexts:
- name: default
  context:
    cluster: secondary
    user: admin
current-context: default
`, localPort, filepath.Join(tmpDir, "tls.crt"), filepath.Join(tmpDir, "tls.key"))
		kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
		Expect(os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600)).To(Succeed())

		By("querying the Gadget directly on the secondary (bypassing aggregation)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl",
				"--kubeconfig", kubeconfigPath,
				"get", "gadget", gadgetName,
				"-n", gadgetNamespace,
				"-o", "jsonpath={.spec.message}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred(),
				"Failed to query Gadget directly on secondary")
			g.Expect(output).To(Equal("hello from force-aggregation e2e"),
				"Gadget should exist directly on the secondary, proving aggregation works with forceAggregation")
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})
})

func getProjectDir() string {
	wd, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	return filepath.Join(wd, "..", "..", "..")
}

func run(cmd *exec.Cmd) (string, error) {
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return string(output), nil
}

func stringReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
