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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/konflux-ci/kube-shard/operator/test/utils"
)

const (
	shardName      = "e2e-metrics-shard"
	shardNamespace = "e2e-metrics-shard-ns"
)

var _ = Describe("APIShard Metrics", Ordered, func() {
	BeforeAll(func() {
		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
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
  secondary:
    replicas: 1
  kine:
    replicas: 1
`, shardName, shardNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = utils.StringReader(apishardYAML)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create APIShard")

		By("waiting for APIShard to become Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName,
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}, 5*time.Minute, 10*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		By("deleting the APIShard")
		cmd := exec.Command("kubectl", "delete", "apishard", shardName, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)

		By("deleting the shard namespace")
		cmd = exec.Command("kubectl", "delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		By("cleaning up curl pods that may have been left behind")
		for _, podName := range []string{"curl-kine-metrics", "curl-apiserver-metrics"} {
			cmd := exec.Command("kubectl", "delete", "pod", podName,
				"-n", shardNamespace, "--ignore-not-found")
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
				scheme = s
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

			By("scraping metrics from the Kine endpoint via curl pod")
			metricsURL := fmt.Sprintf("%s://%s-kine.%s.svc:%d%s",
				scheme, shardName, shardNamespace, resolvedPort, path)
			curlArgs := []string{"-s", "-f"}
			if scheme == "https" {
				curlArgs = append(curlArgs, "-k")
			}
			curlArgs = append(curlArgs, metricsURL)
			kubectlArgs := append([]string{"run", "curl-kine-metrics", "--rm", "-i",
				"--restart=Never", "--image=curlimages/curl:latest",
				"-n", shardNamespace, "--", "curl"}, curlArgs...)
			cmd = exec.Command("kubectl", kubectlArgs...)
			curlOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(curlOutput).To(ContainSubstring("go_goroutines"))
		})
	})

	Context("Secondary apiserver metrics", func() {
		It("should expose secondary apiserver metrics as described by its ServiceMonitor", func() {
			if os.Getenv("PROMETHEUS_INSTALL_SKIP") == "true" {
				Skip("Prometheus Operator not installed")
			}

			By("fetching the apiserver ServiceMonitor")
			cmd := exec.Command("kubectl", "get", "servicemonitor",
				fmt.Sprintf("%s-apiserver-metrics", shardName),
				"-n", shardNamespace, "-o", "json")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var sm map[string]interface{}
			Expect(json.Unmarshal([]byte(output), &sm)).To(Succeed())

			endpoints := sm["spec"].(map[string]interface{})["endpoints"].([]interface{})
			ep := endpoints[0].(map[string]interface{})
			portName := ep["port"].(string)
			path := ep["path"].(string)
			scheme := "https"
			if s, ok := ep["scheme"].(string); ok && s != "" {
				scheme = s
			}
			tokenSecretName := extractAuthSecretName(ep)

			By("getting the bearer token from the secret")
			cmd = exec.Command("kubectl", "get", "secret", tokenSecretName,
				"-n", shardNamespace,
				"-o", "go-template={{.data.token | base64decode}}")
			tokenOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(tokenOutput).NotTo(BeEmpty(), "bearer token should not be empty")

			By("resolving port from the apiserver Service")
			cmd = exec.Command("kubectl", "get", "service",
				fmt.Sprintf("%s-apiserver", shardName),
				"-n", shardNamespace, "-o", "json")
			svcOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var svc map[string]interface{}
			Expect(json.Unmarshal([]byte(svcOutput), &svc)).To(Succeed())

			resolvedPort := resolveServicePort(svc, portName)
			Expect(resolvedPort).ToNot(BeZero(), "port %s not found in apiserver Service", portName)

			By("scraping metrics from the apiserver endpoint via curl pod")
			metricsURL := fmt.Sprintf("%s://%s-apiserver.%s.svc:%d%s",
				scheme, shardName, shardNamespace, resolvedPort, path)
			curlArgs := []string{"-s", "-f"}
			if scheme == "https" {
				curlArgs = append(curlArgs, "-k")
			}
			curlArgs = append(curlArgs,
				"-H", fmt.Sprintf("Authorization: Bearer %s", tokenOutput),
				metricsURL)
			kubectlArgs := append([]string{"run", "curl-apiserver-metrics", "--rm", "-i",
				"--restart=Never", "--image=curlimages/curl:latest",
				"-n", shardNamespace, "--", "curl"}, curlArgs...)
			cmd = exec.Command("kubectl", kubectlArgs...)
			curlOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(curlOutput).To(ContainSubstring("apiserver_request_total"))
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

// extractAuthSecretName extracts the bearer token secret name from a ServiceMonitor endpoint.
// It supports both the legacy bearerTokenSecret field and the newer authorization.credentials field.
func extractAuthSecretName(ep map[string]interface{}) string {
	if auth, ok := ep["authorization"].(map[string]interface{}); ok {
		if creds, ok := auth["credentials"].(map[string]interface{}); ok {
			if name, ok := creds["name"].(string); ok {
				return name
			}
		}
	}
	if bts, ok := ep["bearerTokenSecret"].(map[string]interface{}); ok {
		if name, ok := bts["name"].(string); ok {
			return name
		}
	}
	return ""
}
