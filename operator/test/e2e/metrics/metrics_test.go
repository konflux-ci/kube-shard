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

package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/konflux-ci/kube-shard/operator/test/utils"
)

const (
	shardName           = "e2e-metrics-shard"
	shardNamespace      = "e2e-metrics-shard-ns"
	clientNamespace     = "e2e-metrics-client"
	monitoringNamespace = "e2e-monitoring"
	prometheusSAName    = "prometheus-k8s"
	metricsAPIGroup     = "metricstest.example.com"
	metricsCRDName      = "metricsitems.metricstest.example.com"
)

var _ = Describe("APIShard Metrics", Ordered, func() {
	BeforeAll(func() {
		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "crd", metricsCRDName, "--ignore-not-found"},
			{"delete", "apiservice", "v1." + metricsAPIGroup, "--ignore-not-found"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", clientNamespace, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", monitoringNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = utils.Run(cmd)
		}

		By("waiting for APIShard to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
				fmt.Sprintf("apishard %s should be deleted", shardName))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for namespace to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "ns", shardNamespace, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
				fmt.Sprintf("namespace %s should be deleted", shardNamespace))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for client namespace to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "ns", clientNamespace, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
				fmt.Sprintf("namespace %s should be deleted", clientNamespace))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for monitoring namespace to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "ns", monitoringNamespace, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
				fmt.Sprintf("namespace %s should be deleted", monitoringNamespace))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("installing the metrics test CRD")
		projectDir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		cmd := exec.Command("kubectl", "apply", "-f",
			filepath.Join(projectDir, "test", "e2e", "testdata", "metrics_crd.yaml"))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install metrics test CRD")

		By("creating the APIShard resource")
		apishardYAML := fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %s
spec:
  targetNamespace: %s
  apiGroups:
    - group: %s
      versions:
        - v1
  storage:
    type: SQLite
  namespaceSync:
    labelSelector:
      matchLabels:
        test: e2e-metrics
  secondary:
    replicas: 1
  kine:
    replicas: 1
  monitoring:
    prometheusServiceAccountName: %s
    prometheusNamespace: %s
`, shardName, shardNamespace, metricsAPIGroup, prometheusSAName, monitoringNamespace)
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = utils.StringReader(apishardYAML)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create APIShard")

		By("waiting for APIShard to become Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			if output != "Ready" {
				condCmd := exec.Command("kubectl", "get", "apishard", shardName,
					"-o", "jsonpath={.status.conditions}")
				condOut, _ := utils.Run(condCmd)
				g.Expect(output).To(Equal("Ready"),
					"phase=%s conditions=%s", output, condOut)
			}
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("creating the client namespace for cross-namespace scraping tests")
		cmd = exec.Command("kubectl", "create", "namespace", clientNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create client namespace")

		By("creating the monitoring namespace and Prometheus ServiceAccount")
		cmd = exec.Command("kubectl", "create", "namespace", monitoringNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create monitoring namespace")

		cmd = exec.Command("kubectl", "create", "serviceaccount", prometheusSAName,
			"-n", monitoringNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Prometheus ServiceAccount")
	})

	AfterAll(func() {
		By("deleting the APIShard")
		cmd := exec.Command("kubectl", "delete", "apishard", shardName, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)

		By("cleaning up CRD and APIService")
		cmd = exec.Command("kubectl", "delete", "crd", metricsCRDName, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "apiservice",
			"v1."+metricsAPIGroup, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("deleting the shard namespace")
		cmd = exec.Command("kubectl", "delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)

		By("deleting the client namespace")
		cmd = exec.Command("kubectl", "delete", "ns", clientNamespace, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)

		By("deleting the monitoring namespace")
		cmd = exec.Command("kubectl", "delete", "ns", monitoringNamespace, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		By("cleaning up curl pods that may have been left behind")
		for _, podName := range []string{"curl-kine-metrics"} {
			cmd := exec.Command("kubectl", "delete", "pod", podName,
				"-n", clientNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		}
		for _, podName := range []string{"curl-apiserver-metrics"} {
			cmd := exec.Command("kubectl", "delete", "pod", podName,
				"-n", shardNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		}
		for _, podName := range []string{"curl-prometheus-rbac", "curl-prometheus-endpoints"} {
			cmd := exec.Command("kubectl", "delete", "pod", podName,
				"-n", monitoringNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		}
	})

	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(10 * time.Second)

	Context("Kine metrics", func() {
		It("should expose Kine metrics as described by its ServiceMonitor", func() {
			if os.Getenv("PROMETHEUS_INSTALL_SKIP") == "true" {
				Skip("Prometheus Operator not installed")
			}

			By("fetching the Kine ServiceMonitor")
			cmd := exec.Command("kubectl", "get", "servicemonitor",
				fmt.Sprintf("%s-kine-metrics", shardName),
				"-n", shardNamespace, "-o", "json")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var sm map[string]interface{}
			Expect(json.Unmarshal([]byte(output), &sm)).To(Succeed())

			endpoints := sm["spec"].(map[string]interface{})["endpoints"].([]interface{})
			ep := endpoints[0].(map[string]interface{})
			portName := ep["port"].(string)
			path := ep["path"].(string)
			scheme := "http"
			if s, ok := ep["scheme"].(string); ok && s != "" {
				scheme = strings.ToLower(s)
			}

			By("resolving port from the Kine Service")
			cmd = exec.Command("kubectl", "get", "service",
				fmt.Sprintf("%s-kine", shardName),
				"-n", shardNamespace, "-o", "json")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var svc map[string]interface{}
			Expect(json.Unmarshal([]byte(output), &svc)).To(Succeed())

			resolvedPort := resolveServicePort(svc, portName)
			Expect(resolvedPort).ToNot(BeZero(), "port %s not found in Kine Service", portName)

			By("scraping metrics from the Kine endpoint via curl pod in client namespace")
			metricsURL := fmt.Sprintf("%s://%s-kine.%s.svc:%d%s",
				scheme, shardName, shardNamespace, resolvedPort, path)
			var curlOutput string
			Eventually(func(g Gomega) {
				_ = exec.Command("kubectl", "delete", "pod", "curl-kine-metrics",
					"-n", clientNamespace, "--ignore-not-found").Run()
				curlArgs := []string{"-s", "-m", "10"}
				if scheme == "https" {
					curlArgs = append(curlArgs, "-k")
				}
				curlArgs = append(curlArgs, metricsURL)
				kubectlArgs := append([]string{
					"run", "curl-kine-metrics", "--rm", "-i",
					"--restart=Never", "--image=curlimages/curl:latest",
					"-n", clientNamespace, "--", "curl",
				}, curlArgs...)
				cmd := exec.Command("kubectl", kubectlArgs...)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("go_goroutines"),
					"response from %s: %s", metricsURL, out)
				curlOutput = out
			}).Should(Succeed())
			Expect(curlOutput).To(ContainSubstring("go_goroutines"))
		})
	})

	Context("Secondary apiserver metrics", func() {
		It("should expose secondary apiserver metrics with a primary-issued bearer token", func() {
			By("resolving port from the apiserver Service")
			cmd := exec.Command("kubectl", "get", "service",
				fmt.Sprintf("%s-apiserver", shardName),
				"-n", shardNamespace, "-o", "json")
			svcOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var svc map[string]interface{}
			Expect(json.Unmarshal([]byte(svcOutput), &svc)).To(Succeed())
			portName := "https"
			resolvedPort := resolveServicePort(svc, portName)
			Expect(resolvedPort).ToNot(BeZero(),
				"port %s not found in apiserver Service", portName)

			By("scraping metrics using the metrics-reader SA token")
			metricsURL := fmt.Sprintf(
				"https://%s-apiserver.%s.svc:%d/metrics",
				shardName, shardNamespace, resolvedPort)
			Eventually(func(g Gomega) {
				out, err := runAsMetricsReader(
					"curl-apiserver-metrics", metricsURL)
				g.Expect(err).NotTo(HaveOccurred(),
					"curl pod failed; output: %s", out)
				g.Expect(out).To(ContainSubstring("apiserver_request_total"),
					"response from %s: %s", metricsURL, out)
			}).WithTimeout(90 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
		})
	})

	Context("Prometheus discovery RBAC", func() {
		It("should allow the Prometheus SA to list ServiceMonitors in the shard namespace", func() {
			if os.Getenv("PROMETHEUS_INSTALL_SKIP") == "true" {
				Skip("Prometheus Operator not installed")
			}

			By("running a curl pod as the Prometheus SA to list ServiceMonitors")
			apiURL := fmt.Sprintf(
				"https://kubernetes.default.svc/apis/monitoring.coreos.com/v1/namespaces/%s/servicemonitors",
				shardNamespace)
			output, err := runAsPrometheus("curl-prometheus-rbac", apiURL)
			Expect(err).NotTo(HaveOccurred(),
				"Prometheus SA should be able to list ServiceMonitors in the shard namespace")
			Expect(output).To(ContainSubstring("ServiceMonitorList"))
		})

		It("should allow the Prometheus SA to list endpoints in the shard namespace", func() {
			By("running a curl pod as the Prometheus SA to list endpoints")
			apiURL := fmt.Sprintf(
				"https://kubernetes.default.svc/api/v1/namespaces/%s/endpoints",
				shardNamespace)
			output, err := runAsPrometheus("curl-prometheus-endpoints", apiURL)
			Expect(err).NotTo(HaveOccurred(),
				"Prometheus SA should be able to list endpoints in the shard namespace")
			Expect(output).To(ContainSubstring("EndpointsList"))
		})
	})

	Context("ServiceMonitor absence", func() {
		It("should not create ServiceMonitors when Prometheus Operator is not installed", func() {
			if os.Getenv("PROMETHEUS_INSTALL_SKIP") != "true" {
				Skip("Prometheus Operator is installed; skipping negative test")
			}

			By("verifying no Kine ServiceMonitor exists")
			cmd := exec.Command("kubectl", "get", "servicemonitor",
				fmt.Sprintf("%s-kine-metrics", shardName),
				"-n", shardNamespace)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Kine ServiceMonitor should not exist without Prometheus Operator")

			By("verifying no apiserver ServiceMonitor exists")
			cmd = exec.Command("kubectl", "get", "servicemonitor",
				fmt.Sprintf("%s-apiserver-metrics", shardName),
				"-n", shardNamespace)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Apiserver ServiceMonitor should not exist without Prometheus Operator")
		})
	})
})

// resolveServicePort returns the port number with the given name from a Service JSON object.
// Returns 0 if the named port is not found.
func resolveServicePort(svc map[string]interface{}, portName string) int {
	ports, _ := svc["spec"].(map[string]interface{})["ports"].([]interface{})
	for _, p := range ports {
		pm, _ := p.(map[string]interface{})
		if name, _ := pm["name"].(string); name == portName {
			if port, ok := pm["port"].(float64); ok {
				return int(port)
			}
		}
	}
	return 0
}

// runAsPrometheus runs a curl pod in the monitoring namespace using the Prometheus
// ServiceAccount and queries the given API URL using the mounted SA token.
// It creates the pod, waits for completion, then reads logs to avoid the race
// condition where kubectl run --rm -i misses output from fast-exiting pods.
func runAsPrometheus(podName, apiURL string) (string, error) {
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	curlCmd := fmt.Sprintf(
		"curl -s -m 15 -k -H 'Authorization: Bearer '$(cat %s) %s",
		tokenPath, apiURL)

	overrides := map[string]interface{}{
		"spec": map[string]interface{}{
			"serviceAccountName":           prometheusSAName,
			"automountServiceAccountToken": true,
			"containers": []map[string]interface{}{
				{
					"name":    podName,
					"image":   "curlimages/curl:latest",
					"command": []string{"sh", "-c", curlCmd},
				},
			},
		},
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return "", fmt.Errorf("marshaling overrides: %w", err)
	}

	// Create the pod (don't attach or auto-remove)
	createArgs := []string{
		"run", podName,
		"--restart=Never",
		"--image=curlimages/curl:latest",
		"-n", monitoringNamespace,
		"--overrides", string(overridesJSON),
	}
	cmd := exec.Command("kubectl", createArgs...)
	if _, err := utils.Run(cmd); err != nil {
		return "", fmt.Errorf("creating pod: %w", err)
	}

	// Wait for the pod to complete (Succeeded or Failed)
	waitArgs := []string{
		"wait", "--for=jsonpath={.status.phase}=Succeeded",
		"pod/" + podName, "-n", monitoringNamespace,
		"--timeout=30s",
	}
	cmd = exec.Command("kubectl", waitArgs...)
	if _, err := utils.Run(cmd); err != nil {
		// Grab logs even on failure for diagnostics
		logsCmd := exec.Command("kubectl", "logs", podName, "-n", monitoringNamespace)
		logs, _ := utils.Run(logsCmd)
		_ = exec.Command("kubectl", "delete", "pod", podName,
			"-n", monitoringNamespace, "--ignore-not-found").Run()
		return logs, fmt.Errorf("pod did not succeed: %w\nlogs: %s", err, logs)
	}

	// Read the pod logs
	logsCmd := exec.Command("kubectl", "logs", podName, "-n", monitoringNamespace)
	output, err := utils.Run(logsCmd)

	// Clean up the pod
	_ = exec.Command("kubectl", "delete", "pod", podName,
		"-n", monitoringNamespace, "--ignore-not-found").Run()

	return output, err
}

// runAsMetricsReader runs a curl pod as the metrics-reader ServiceAccount in the
// shard namespace, using the pod's mounted SA token for authentication. This
// mirrors how Prometheus actually scrapes the secondary apiserver.
func runAsMetricsReader(podName, apiURL string) (string, error) {
	metricsReaderSA := fmt.Sprintf("%s-metrics-reader", shardName)
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	curlCmd := fmt.Sprintf(
		"curl -s -m 15 -k -H 'Authorization: Bearer '$(cat %s) %s",
		tokenPath, apiURL)

	overrides := map[string]interface{}{
		"spec": map[string]interface{}{
			"serviceAccountName":           metricsReaderSA,
			"automountServiceAccountToken": true,
			"containers": []map[string]interface{}{
				{
					"name":    podName,
					"image":   "curlimages/curl:latest",
					"command": []string{"sh", "-c", curlCmd},
				},
			},
		},
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return "", fmt.Errorf("marshaling overrides: %w", err)
	}

	_ = exec.Command("kubectl", "delete", "pod", podName,
		"-n", shardNamespace, "--ignore-not-found").Run()

	createArgs := []string{
		"run", podName,
		"--restart=Never",
		"--image=curlimages/curl:latest",
		"-n", shardNamespace,
		"--overrides", string(overridesJSON),
	}
	cmd := exec.Command("kubectl", createArgs...)
	if _, err := utils.Run(cmd); err != nil {
		return "", fmt.Errorf("creating pod: %w", err)
	}

	waitArgs := []string{
		"wait", "--for=jsonpath={.status.phase}=Succeeded",
		"pod/" + podName, "-n", shardNamespace,
		"--timeout=30s",
	}
	cmd = exec.Command("kubectl", waitArgs...)
	if _, err := utils.Run(cmd); err != nil {
		logsCmd := exec.Command("kubectl", "logs", podName, "-n", shardNamespace)
		logs, _ := utils.Run(logsCmd)
		_ = exec.Command("kubectl", "delete", "pod", podName,
			"-n", shardNamespace, "--ignore-not-found").Run()
		return logs, fmt.Errorf("pod did not succeed: %w\nlogs: %s", err, logs)
	}

	logsCmd := exec.Command("kubectl", "logs", podName, "-n", shardNamespace)
	output, err := utils.Run(logsCmd)

	_ = exec.Command("kubectl", "delete", "pod", podName,
		"-n", shardNamespace, "--ignore-not-found").Run()

	return output, err
}

// curlOTelMetrics creates a curl pod, waits for completion, and returns its logs.
// This is more reliable than `kubectl run --rm -i` which can drop stdout.
func curlOTelMetrics(podName, namespace, metricsURL string) (string, error) {
	_ = exec.Command("kubectl", "delete", "pod", podName,
		"-n", namespace, "--ignore-not-found").Run()

	cmd := exec.Command("kubectl", "run", podName,
		"--restart=Never",
		"--image=curlimages/curl:latest",
		"-n", namespace,
		"--", "curl", "-s", "-m", "10", metricsURL)
	if _, err := utils.Run(cmd); err != nil {
		return "", fmt.Errorf("creating curl pod: %w", err)
	}

	waitCmd := exec.Command("kubectl", "wait",
		"--for=jsonpath={.status.phase}=Succeeded",
		"pod/"+podName, "-n", namespace, "--timeout=30s")
	if _, err := utils.Run(waitCmd); err != nil {
		logsCmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
		logs, _ := utils.Run(logsCmd)
		_ = exec.Command("kubectl", "delete", "pod", podName,
			"-n", namespace, "--ignore-not-found").Run()
		return logs, fmt.Errorf("curl pod did not succeed: %w\nlogs: %s", err, logs)
	}

	logsCmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
	output, err := utils.Run(logsCmd)
	_ = exec.Command("kubectl", "delete", "pod", podName,
		"-n", namespace, "--ignore-not-found").Run()
	return output, err
}

const (
	pgShardName      = "e2e-pgmetrics-shard"
	pgShardNamespace = "e2e-pgmetrics-ns"
)

var _ = Describe("PostgreSQL Storage Monitoring", Ordered, func() {
	BeforeAll(func() {
		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "apishard", pgShardName, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", pgShardNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = utils.Run(cmd)
		}

		By("waiting for APIShard to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", pgShardName, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for namespace to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "ns", pgShardNamespace, "--no-headers")
			output, _ := utils.Run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the APIShard with InClusterPostgreSQL and monitoring enabled")
		apishardYAML := fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %s
spec:
  targetNamespace: %s
  apiGroups:
    - group: %s
      versions:
        - v1
  storage:
    type: InClusterPostgreSQL
    inCluster:
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
    monitoring:
      enabled: true
      collectionInterval: "30s"
      postgresql:
        bloatInterval: "5m"
  namespaceSync:
    labelSelector:
      matchLabels:
        test: e2e-pgmetrics
  secondary:
    replicas: 1
  kine:
    replicas: 1
  monitoring:
    prometheusServiceAccountName: %s
    prometheusNamespace: %s
`, pgShardName, pgShardNamespace, metricsAPIGroup, prometheusSAName, monitoringNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = utils.StringReader(apishardYAML)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create PostgreSQL APIShard")

		By("waiting for APIShard to become Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", pgShardName,
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			if output != "Ready" {
				condCmd := exec.Command("kubectl", "get", "apishard", pgShardName,
					"-o", "jsonpath={.status.conditions}")
				condOut, _ := utils.Run(condCmd)
				g.Expect(output).To(Equal("Ready"),
					"phase=%s conditions=%s", output, condOut)
			}
		}, 8*time.Minute, 10*time.Second).Should(Succeed())

		By("waiting for OTel Collector deployment to be ready")
		cmd = exec.Command("kubectl", "get", "deployment",
			fmt.Sprintf("%s-postgresql-metrics", pgShardName),
			"-n", pgShardNamespace, "-o", "jsonpath={.status.readyReplicas}")
		Eventually(func(g Gomega) {
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("1"))
		}, 3*time.Minute, 10*time.Second).Should(Succeed())

		By("waiting for OTel Collector to complete first PostgreSQL scrape")
		metricsURL := fmt.Sprintf("http://%s-postgresql-metrics.%s.svc:9187/metrics",
			pgShardName, pgShardNamespace)
		Eventually(func(g Gomega) {
			out, err := curlOTelMetrics("curl-otel-warmup", pgShardNamespace, metricsURL)
			g.Expect(err).NotTo(HaveOccurred(), "curl failed: %s", out)
			g.Expect(out).To(ContainSubstring("postgresql_db_size"),
				"OTel collector has not emitted database-level metrics yet")
		}, 5*time.Minute, 15*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		By("deleting the PostgreSQL APIShard")
		cmd := exec.Command("kubectl", "delete", "apishard", pgShardName,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)

		By("deleting the shard namespace")
		cmd = exec.Command("kubectl", "delete", "ns", pgShardNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		for _, podName := range []string{"curl-otel-metrics", "curl-otel-bloat", "curl-otel-warmup"} {
			cmd := exec.Command("kubectl", "delete", "pod", podName,
				"-n", pgShardNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		}
	})

	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(10 * time.Second)

	Context("OTel Collector Deployment", func() {
		It("should create the OTel Collector Deployment", func() {
			cmd := exec.Command("kubectl", "get", "deployment",
				fmt.Sprintf("%s-postgresql-metrics", pgShardName),
				"-n", pgShardNamespace, "-o", "jsonpath={.status.readyReplicas}")
			Eventually(func(g Gomega) {
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"),
					"OTel Collector Deployment should have 1 ready replica")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())
		})

		It("should create the OTel Collector Service", func() {
			cmd := exec.Command("kubectl", "get", "service",
				fmt.Sprintf("%s-postgresql-metrics", pgShardName),
				"-n", pgShardNamespace, "-o", "jsonpath={.spec.ports[0].port}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("9187"))
		})

		It("should create the OTel Collector ConfigMap (hashed name)", func() {
			cmd := exec.Command("kubectl", "get", "configmap",
				"-n", pgShardNamespace,
				"-l", "kube-shard.konflux-ci.dev/otel-config=true",
				"-o", "jsonpath={range .items[*]}{.metadata.name}{'\\n'}{end}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring(fmt.Sprintf("%s-postgresql-metrics-config-", pgShardName)),
				"expected hashed OTel ConfigMap to exist")
		})

		It("should create the PostgreSQL init ConfigMap", func() {
			cmd := exec.Command("kubectl", "get", "configmap",
				fmt.Sprintf("%s-postgresql-init", pgShardName),
				"-n", pgShardNamespace,
				"-o", "jsonpath={.data.init-pgstattuple\\.sql}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("CREATE EXTENSION IF NOT EXISTS pgstattuple"))
		})
	})

	Context("OTel Collector metrics endpoint", func() {
		It("should expose Prometheus metrics on port 9187", func() {
			metricsURL := fmt.Sprintf("http://%s-postgresql-metrics.%s.svc:9187/metrics",
				pgShardName, pgShardNamespace)

			out, err := curlOTelMetrics("curl-otel-metrics", pgShardNamespace, metricsURL)
			Expect(err).NotTo(HaveOccurred(), "curl failed; output: %s", out)
			Expect(out).To(ContainSubstring("postgresql_db_size"),
				"expected standard PostgreSQL metrics in response")
		})

		It("should emit pgstattuple bloat metrics", func() {
			metricsURL := fmt.Sprintf("http://%s-postgresql-metrics.%s.svc:9187/metrics",
				pgShardName, pgShardNamespace)

			Eventually(func(g Gomega) {
				out, err := curlOTelMetrics("curl-otel-bloat", pgShardNamespace, metricsURL)
				g.Expect(err).NotTo(HaveOccurred(),
					"curl failed; output: %s", out)
				g.Expect(out).To(ContainSubstring("postgresql_tables_reclaimable_bytes"),
					"expected tables_reclaimable_bytes bloat metric")
				g.Expect(out).To(ContainSubstring("postgresql_tables_live_bytes"),
					"expected tables_live_bytes bloat metric")
				g.Expect(out).To(ContainSubstring("postgresql_tables_size_bytes"),
					"expected tables_size_bytes bloat metric")
			}, 5*time.Minute, 15*time.Second).Should(Succeed())
		})
	})

	Context("PostgreSQL metrics ServiceMonitor", func() {
		It("should create a ServiceMonitor for the OTel Collector", func() {
			if os.Getenv("PROMETHEUS_INSTALL_SKIP") == "true" {
				Skip("Prometheus Operator not installed")
			}

			cmd := exec.Command("kubectl", "get", "servicemonitor",
				fmt.Sprintf("%s-postgresql-metrics", pgShardName),
				"-n", pgShardNamespace, "-o", "json")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var sm map[string]interface{}
			Expect(json.Unmarshal([]byte(output), &sm)).To(Succeed())

			endpoints := sm["spec"].(map[string]interface{})["endpoints"].([]interface{})
			Expect(endpoints).To(HaveLen(1))
			ep := endpoints[0].(map[string]interface{})
			Expect(ep["port"]).To(Equal("otel-metrics"))
			Expect(ep["path"]).To(Equal("/metrics"))
		})
	})
})
