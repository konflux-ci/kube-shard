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
	"fmt"
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
	widgetNamespace = "default"
)

var _ = Describe("CRD Sharding", Ordered, func() {
	var testdataDir string

	BeforeAll(func() {
		projectDir := getProjectDir()
		testdataDir = filepath.Join(projectDir, "test", "e2e", "testdata")

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

		By("labeling default namespace to trigger namespace sync")
		cmd = exec.Command("kubectl", "label", "ns", widgetNamespace,
			"e2e-test=widget-shard", "--overwrite")
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("creating the MutatingWebhookConfiguration")
		cmd = exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "mutating_webhook.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create MutatingWebhookConfiguration")

		By("waiting for cert-manager to inject CA bundle")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "mutatingwebhookconfiguration",
				"e2e-widget-webhook",
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(BeEmpty(), "CA bundle not yet injected")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for operator to create and reconcile WebhookSync")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "webhooksync",
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", shardName),
				"-o", "jsonpath={.items[0].status.phase}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}, 3*time.Minute, 10*time.Second).Should(Succeed())
	})

	AfterAll(func() {
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

	It("should persist the resource in the Kine SQLite database", func() {
		By("finding the Kine pod")
		cmd := exec.Command("kubectl", "get", "pods",
			"-n", shardNamespace,
			"-l", fmt.Sprintf("app.kubernetes.io/name=kine,app.kubernetes.io/instance=%s", shardName),
			"-o", "jsonpath={.items[0].metadata.name}")
		kinePod, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to find Kine pod")
		Expect(kinePod).NotTo(BeEmpty(), "No Kine pod found")

		By("querying SQLite for the Widget resource key")
		query := fmt.Sprintf(
			`SELECT name FROM kine WHERE name LIKE '%%/widgets/%s/%s' AND deleted=0 LIMIT 1`,
			widgetNamespace, widgetName)
		cmd = exec.Command("kubectl", "exec", kinePod,
			"-n", shardNamespace,
			"--", "sqlite3", "/data/kine.db", query)
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to query SQLite database")
		Expect(output).To(ContainSubstring(widgetName),
			"Widget key not found in Kine SQLite database")
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
		os.Remove(archive)
		cmd := exec.Command("podman", "save", "-o", archive, name)
		if _, err := run(cmd); err != nil {
			return err
		}
		defer os.Remove(archive)

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
