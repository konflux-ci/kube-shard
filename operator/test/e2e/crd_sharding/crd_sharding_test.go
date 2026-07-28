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

package crd_sharding

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
	shardName       = "e2e-widget-shard"
	shardNamespace  = "e2e-widget-shard-ns"
	webhookNS       = "e2e-webhook"
	webhookImage    = "localhost/e2e-webhook-server:e2e"
	widgetName      = "test-widget"
	widgetNamespace = "e2e-widget-workload"
)

var _ = Describe("CRD Sharding", Ordered, func() {
	var testdataDir string

	BeforeAll(func() {
		projectDir := getProjectDir()
		testdataDir = filepath.Join(projectDir, "test", "e2e", "testdata")

		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "widget", widgetName, "-n", widgetNamespace, "--ignore-not-found"},
			{"delete", "mutatingwebhookconfiguration", "e2e-widget-webhook", "--ignore-not-found"},
			{"delete", "apiservice", "v1.example.com", "--ignore-not-found"},
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "crd", "widgets.example.com", "--ignore-not-found"},
			{"delete", "ns", webhookNS, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", widgetNamespace, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = run(cmd)
		}

		By("waiting for namespaces to be fully deleted")
		Eventually(func(g Gomega) {
			for _, ns := range []string{shardNamespace, webhookNS, widgetNamespace} {
				cmd := exec.Command("kubectl", "get", "ns", ns, "--no-headers")
				output, _ := run(cmd)
				g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
					fmt.Sprintf("namespace %s should be deleted", ns))
			}
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("building the webhook server image")
		webhookServerDir := filepath.Join(projectDir, "test", "e2e", "webhook-server")
		containerRuntime := detectContainerRuntime()
		cmd := exec.Command(containerRuntime, "build", "-t", webhookImage, webhookServerDir)
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to build webhook server image")

		By("loading webhook server image into Kind")
		err = loadImageToKindCluster(webhookImage)
		Expect(err).NotTo(HaveOccurred(), "Failed to load webhook server image into Kind")

		By("deploying the webhook server")
		cmd = exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "webhook_server.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy webhook server")

		By("waiting for webhook server to be ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods",
				"-n", webhookNS,
				"-l", "app=e2e-webhook-server",
				"-o", "jsonpath={.items[0].status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Running"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the APIShard resource")
		apishardYAML := fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %s
spec:
  targetNamespace: %s
  forceAggregation: false
  apiGroups:
    - group: example.com
      versions:
        - v1
  storage:
    type: SQLite
  namespaceSync:
    labelSelector:
      matchLabels:
        e2e-test: widget-shard
  secondary:
    replicas: 1
  kine:
    replicas: 1
`, shardName, shardNamespace)
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(apishardYAML)
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create APIShard")

		By("waiting for APIShard to become Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("installing the dummy CRD on the primary (triggers operator CRD sync)")
		cmd = exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "dummy_crd.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRD on primary")

		By("waiting for CRDConflict condition to be True (operator detected the conflict)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.conditions[?(@.type=='CRDConflict')].status}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("True"), "CRDConflict condition should be True")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying phase is Blocked during CRD conflict")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Blocked"), "Phase should be Blocked when CRDs conflict")
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		By("deleting the CRD from the primary (resolving the conflict)")
		cmd = exec.Command("kubectl", "delete", "-f", filepath.Join(testdataDir, "dummy_crd.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete CRD from primary")

		By("waiting for CRDConflict condition to become False")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.conditions[?(@.type=='CRDConflict')].status}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("False"), "CRDConflict condition should be False after deletion")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying phase returns to Ready after conflict resolution")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"), "Phase should return to Ready after conflict resolution")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the workload namespace with sync label")
		cmd = exec.Command("kubectl", "create", "ns", widgetNamespace)
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create workload namespace")
		cmd = exec.Command("kubectl", "label", "ns", widgetNamespace,
			"e2e-test=widget-shard")
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

		By("creating the MutatingWebhookConfiguration")
		cmd = exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "mutating_webhook.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create MutatingWebhookConfiguration")

		By("waiting for cert-manager to inject CA bundle matching the webhook cert")
		var expectedCA string
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secret", "e2e-webhook-tls",
				"-n", webhookNS, "-o", "jsonpath={.data.ca\\.crt}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(BeEmpty(), "webhook cert secret not ready")
			expectedCA = output
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "mutatingwebhookconfiguration",
				"e2e-widget-webhook",
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(expectedCA), "CA bundle should match the webhook cert CA")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for operator to create and reconcile WebhookSync with synced webhooks")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "webhooksync",
				"-n", shardNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", shardName),
				"-o", `jsonpath={.items[0].status.conditions[?(@.type=="Ready")].status}`)
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("True"))

			cmd = exec.Command("kubectl", "get", "webhooksync",
				"-n", shardNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", shardName),
				"-o", "jsonpath={.items[0].status.syncedWebhooks.mutating}")
			output, err = run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(Equal("0"), "at least one mutating webhook should be synced")
			g.Expect(output).NotTo(BeEmpty(), "at least one mutating webhook should be synced")
		}, 3*time.Minute, 10*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		if os.Getenv("SKIP_CLEANUP") == "true" {
			By("SKIP_CLEANUP=true — leaving resources in place for inspection")
			return
		}
		By("cleaning up Widget CR")
		cmd := exec.Command("kubectl", "delete", "widget", widgetName,
			"-n", widgetNamespace, "--ignore-not-found", "--wait=false")
		_, _ = run(cmd)

		By("cleaning up CRD from primary if present")
		cmd = exec.Command("kubectl", "delete", "crd", "widgets.example.com", "--ignore-not-found")
		_, _ = run(cmd)

		By("cleaning up MutatingWebhookConfiguration")
		cmd = exec.Command("kubectl", "delete", "mutatingwebhookconfiguration",
			"e2e-widget-webhook", "--ignore-not-found")
		_, _ = run(cmd)

		By("cleaning up APIService")
		cmd = exec.Command("kubectl", "delete", "apiservice",
			"v1.example.com", "--ignore-not-found")
		_, _ = run(cmd)

		By("cleaning up APIShard")
		cmd = exec.Command("kubectl", "delete", "apishard", shardName,
			"--ignore-not-found", "--wait=false")
		_, _ = run(cmd)

		By("cleaning up webhook server namespace")
		cmd = exec.Command("kubectl", "delete", "ns", webhookNS,
			"--ignore-not-found", "--wait=false")
		_, _ = run(cmd)

		By("cleaning up workload namespace")
		cmd = exec.Command("kubectl", "delete", "ns", widgetNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = run(cmd)
	})

	It("should route Widget CRs through aggregation", func() {
		By("creating a Widget CR via the primary API server")
		widgetYAML := fmt.Sprintf(`apiVersion: example.com/v1
kind: Widget
metadata:
  name: %s
  namespace: %s
spec:
  message: "hello from e2e test"
`, widgetName, widgetNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(widgetYAML)
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Widget CR")

		By("verifying the Widget is retrievable via the primary API")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "widget", widgetName,
				"-n", widgetNamespace,
				"-o", "jsonpath={.spec.message}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("hello from e2e test"))
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

	It("should have the webhook annotation proving mutation occurred", func() {
		By("checking the Widget for the webhook annotation")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "widget", widgetName,
				"-n", widgetNamespace,
				"-o", `jsonpath={.metadata.annotations.e2e-webhook\.example\.com/processed}`)
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("true"),
				"Webhook annotation not found -- webhook was not invoked on the secondary")
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

	It("should store the resource directly on the secondary API server", func() {
		tmpDir, err := os.MkdirTemp("", "e2e-secondary-auth-*")
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

		By("querying the Widget directly on the secondary (bypassing aggregation)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl",
				"--kubeconfig", kubeconfigPath,
				"get", "widget", widgetName,
				"-n", widgetNamespace,
				"-o", "jsonpath={.spec.message}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred(),
				"Failed to query Widget directly on secondary")
			g.Expect(output).To(Equal("hello from e2e test"),
				"Widget should exist directly on the secondary, proving it's stored in Kine")
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

})

// getProjectDir returns the operator project root directory.
func getProjectDir() string {
	wd, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	return filepath.Join(wd, "..", "..", "..")
}

// run executes the provided command and returns its combined output.
func run(cmd *exec.Cmd) (string, error) {
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return string(output), nil
}

// loadImageToKindCluster loads a container image into the Kind cluster.
// For podman, it saves to a tarball and uses `kind load image-archive`.
func loadImageToKindCluster(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}

	runtime := detectContainerRuntime()
	if runtime == "podman" {
		archive := filepath.Join(os.TempDir(), "e2e-webhook-server.tar")
		_ = os.Remove(archive)
		cmd := exec.Command("podman", "save", "-o", archive, name)
		if _, err := run(cmd); err != nil {
			return err
		}
		defer func() { _ = os.Remove(archive) }()

		cmd = exec.Command("kind", "load", "image-archive", archive, "--name", cluster)
		_, err := run(cmd)
		return err
	}

	cmd := exec.Command("kind", "load", "docker-image", name, "--name", cluster)
	_, err := run(cmd)
	return err
}

// detectContainerRuntime returns "podman" if available, otherwise "docker".
func detectContainerRuntime() string {
	if _, err := exec.LookPath("podman"); err == nil {
		return "podman"
	}
	return "docker"
}

func stringReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
