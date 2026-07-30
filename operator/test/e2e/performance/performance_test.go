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

package performance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	shardName         = "e2e-perf-shard"
	shardNamespace    = "e2e-perf-shard-ns"
	workloadNS        = "e2e-perf-workload"
	resourceCount     = 50
	benchmarkAPIGroup = "perftest.example.com"
	maxConcurrency    = 10
)

// External PostgreSQL constants, only used when PERF_STORAGE_MODE=PostgreSQL.
const pgSecretName = "e2e-perf-pg-credentials"

func storageMode() string {
	if mode := os.Getenv("PERF_STORAGE_MODE"); mode != "" {
		return mode
	}
	return "InClusterPostgreSQL"
}

func useExternalPostgreSQL() bool {
	return storageMode() == "PostgreSQL"
}

// safeBuffer is a concurrency-safe bytes.Buffer for capturing
// subprocess output that is read from another goroutine.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *safeBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *safeBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// cmdLog is a file-backed log for kubectl commands. It captures every
// command and its output without cluttering the Ginkgo console output.
// The file is uploaded as a CI artifact for post-mortem inspection.
var cmdLog *os.File

var _ = Describe("Performance with PostgreSQL backend", Ordered, func() {
	var testdataDir string

	BeforeAll(func() {
		logDir := os.Getenv("ARTIFACT_DIR")
		if logDir == "" {
			logDir = os.TempDir()
		}
		var err error
		cmdLog, err = os.Create(filepath.Join(logDir, "perf-kubectl.log"))
		Expect(err).NotTo(HaveOccurred())

		_, _ = fmt.Fprintf(GinkgoWriter, "Storage mode: %s\n", storageMode())
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl log: %s\n", cmdLog.Name())

		projectDir := getProjectDir()
		testdataDir = filepath.Join(projectDir, "test", "e2e", "testdata")

		By("cleaning up resources from previous test runs")
		for _, args := range [][]string{
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "crd", "benchmarks.perftest.example.com", "--ignore-not-found"},
			{"delete", "apiservice", "v1.perftest.example.com", "--ignore-not-found"},
			{"delete", "ns", workloadNS, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = run(cmd)
		}

		By("waiting for stale APIShard to be fully deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "apishard", shardName, "--no-headers")
			output, _ := run(cmd)
			g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")))
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for namespaces to be fully deleted")
		Eventually(func(g Gomega) {
			for _, ns := range []string{shardNamespace, workloadNS} {
				cmd := exec.Command("kubectl", "get", "ns", ns, "--no-headers")
				output, _ := run(cmd)
				g.Expect(output).To(Or(BeEmpty(), ContainSubstring("not found")),
					fmt.Sprintf("namespace %s should be deleted", ns))
			}
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		if useExternalPostgreSQL() {
			setupExternalPostgreSQL()
		}

		By("installing the Benchmark CRD on the primary")
		cmd := exec.Command("kubectl", "apply", "-f", filepath.Join(testdataDir, "benchmark_crd.yaml"))
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install Benchmark CRD")

		By("creating the APIShard")
		apishardYAML := buildAPIShardYAML()
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

		By("creating the workload namespace with sync label")
		cmd = exec.Command("kubectl", "create", "ns", workloadNS)
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred())
		cmd = exec.Command("kubectl", "label", "ns", workloadNS, "e2e-test=perf-shard")
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
			g.Expect(output).To(Equal("True"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		if cmdLog != nil {
			_ = cmdLog.Close()
		}
		if os.Getenv("SKIP_CLEANUP") == "true" {
			By("SKIP_CLEANUP=true — leaving resources in place for inspection")
			return
		}
		for _, args := range [][]string{
			{"delete", "benchmarks.perftest.example.com", "--all", "-n", workloadNS, "--ignore-not-found"},
			{"delete", "crd", "benchmarks.perftest.example.com", "--ignore-not-found"},
			{"delete", "apiservice", "v1.perftest.example.com", "--ignore-not-found"},
			{"delete", "apishard", shardName, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", workloadNS, "--ignore-not-found", "--wait=false"},
			{"delete", "ns", shardNamespace, "--ignore-not-found", "--wait=false"},
		} {
			cmd := exec.Command("kubectl", args...)
			_, _ = run(cmd)
		}
	})

	It("should handle bulk creation of resources", func() {
		By(fmt.Sprintf("creating %d Benchmark resources with concurrency %d", resourceCount, maxConcurrency))
		var failures atomic.Int32
		start := time.Now()

		sem := make(chan struct{}, maxConcurrency)
		var wg sync.WaitGroup
		for i := 0; i < resourceCount; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				yaml := fmt.Sprintf(`apiVersion: perftest.example.com/v1
kind: Benchmark
metadata:
  name: bm-%04d
  namespace: %s
spec:
  index: %d
  payload: "initial-value-%04d"
`, idx, workloadNS, idx, idx)
				if err := runWithRetry(yaml, 3); err != nil {
					failures.Add(1)
				}
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)

		Expect(failures.Load()).To(Equal(int32(0)),
			"all creates should succeed")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"bulk create: %d resources in %v (%.1f/sec)\n",
			resourceCount, elapsed, float64(resourceCount)/elapsed.Seconds())
	})

	It("should list all created resources consistently", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "benchmarks",
				"-n", workloadNS, "-o", "jsonpath={.items[*].metadata.name}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			names := strings.Fields(output)
			g.Expect(names).To(HaveLen(resourceCount),
				fmt.Sprintf("expected %d resources, got %d", resourceCount, len(names)))
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

	It("should handle bulk patching of resources", func() {
		By(fmt.Sprintf("patching all %d resources with concurrency %d", resourceCount, maxConcurrency))
		var wg sync.WaitGroup
		var failures atomic.Int32
		start := time.Now()

		sem := make(chan struct{}, maxConcurrency)
		for i := 0; i < resourceCount; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				patch := fmt.Sprintf(`{"spec":{"payload":"patched-value-%04d"}}`, idx)
				cmd := exec.Command("kubectl", "patch", "benchmark",
					fmt.Sprintf("bm-%04d", idx),
					"-n", workloadNS,
					"--type=merge", "-p", patch)
				if _, err := run(cmd); err != nil {
					failures.Add(1)
				}
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)

		Expect(failures.Load()).To(Equal(int32(0)),
			"all concurrent patches should succeed")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"bulk patch: %d resources in %v (%.1f/sec)\n",
			resourceCount, elapsed, float64(resourceCount)/elapsed.Seconds())

		By("verifying all patches were applied")
		Eventually(func(g Gomega) {
			for i := 0; i < resourceCount; i++ {
				cmd := exec.Command("kubectl", "get", "benchmark",
					fmt.Sprintf("bm-%04d", i), "-n", workloadNS,
					"-o", "jsonpath={.spec.payload}")
				output, err := run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(fmt.Sprintf("patched-value-%04d", i)))
			}
		}, 60*time.Second, 5*time.Second).Should(Succeed())
	})

	It("should deliver watch events reliably", func() {
		By("starting a watch on benchmarks")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		watchCmd := exec.CommandContext(ctx, "kubectl", "get", "benchmarks",
			"-n", workloadNS, "--watch", "--output-watch-events",
			"-o", "jsonpath={.type} {.object.metadata.name}{\"\\n\"}")
		var watchOutput safeBuffer
		watchCmd.Stdout = &watchOutput
		watchCmd.Stderr = GinkgoWriter
		Expect(watchCmd.Start()).To(Succeed())
		defer func() {
			cancel()
			_ = watchCmd.Wait()
		}()

		time.Sleep(2 * time.Second)

		By("creating a new resource to trigger a watch event")
		yaml := fmt.Sprintf(`apiVersion: perftest.example.com/v1
kind: Benchmark
metadata:
  name: bm-watch-test
  namespace: %s
spec:
  index: 9999
  payload: "watch-test"
`, workloadNS)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(yaml)
		_, err := run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("patching the resource to generate a MODIFIED event")
		cmd = exec.Command("kubectl", "patch", "benchmark", "bm-watch-test",
			"-n", workloadNS, "--type=merge",
			"-p", `{"spec":{"payload":"watch-updated"}}`)
		_, err = run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying ADDED and MODIFIED events were received")
		Eventually(func(g Gomega) {
			out := watchOutput.String()
			g.Expect(out).To(ContainSubstring("ADDED bm-watch-test"))
			g.Expect(out).To(ContainSubstring("MODIFIED bm-watch-test"))
		}, 30*time.Second, time.Second).Should(Succeed())

		By("cleaning up watch test resource")
		cmd = exec.Command("kubectl", "delete", "benchmark", "bm-watch-test",
			"-n", workloadNS, "--ignore-not-found")
		_, _ = run(cmd)
	})

	It("should handle bulk deletion of resources", func() {
		By(fmt.Sprintf("deleting all %d resources with concurrency %d", resourceCount, maxConcurrency))
		var wg sync.WaitGroup
		var failures atomic.Int32
		start := time.Now()

		sem := make(chan struct{}, maxConcurrency)
		for i := 0; i < resourceCount; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cmd := exec.Command("kubectl", "delete", "benchmark",
					fmt.Sprintf("bm-%04d", idx),
					"-n", workloadNS, "--ignore-not-found")
				if _, err := run(cmd); err != nil {
					failures.Add(1)
				}
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)

		Expect(failures.Load()).To(Equal(int32(0)),
			"all concurrent deletes should succeed")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"bulk delete: %d resources in %v (%.1f/sec)\n",
			resourceCount, elapsed, float64(resourceCount)/elapsed.Seconds())

		By("verifying all resources are deleted")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "benchmarks",
				"-n", workloadNS, "-o", "jsonpath={.items}")
			output, err := run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Or(Equal("[]"), BeEmpty()))
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})

	It("should handle rapid create-read-update-delete cycles", func() {
		By(fmt.Sprintf("running %d sequential CRUD cycles", resourceCount))
		start := time.Now()

		for i := 0; i < resourceCount; i++ {
			name := fmt.Sprintf("bm-crud-%04d", i)

			yaml := fmt.Sprintf(`apiVersion: perftest.example.com/v1
kind: Benchmark
metadata:
  name: %s
  namespace: %s
spec:
  index: %d
  payload: "created"
`, name, workloadNS, i)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = stringReader(yaml)
			_, err := run(cmd)
			Expect(err).NotTo(HaveOccurred(), "create failed at iteration %d", i)

			cmd = exec.Command("kubectl", "get", "benchmark", name,
				"-n", workloadNS, "-o", "jsonpath={.spec.index}")
			output, err := run(cmd)
			Expect(err).NotTo(HaveOccurred(), "read failed at iteration %d", i)
			Expect(output).To(Equal(strconv.Itoa(i)))

			cmd = exec.Command("kubectl", "patch", "benchmark", name,
				"-n", workloadNS, "--type=merge",
				"-p", fmt.Sprintf(`{"spec":{"payload":"updated-%d"}}`, i))
			_, err = run(cmd)
			Expect(err).NotTo(HaveOccurred(), "update failed at iteration %d", i)

			cmd = exec.Command("kubectl", "delete", "benchmark", name,
				"-n", workloadNS)
			_, err = run(cmd)
			Expect(err).NotTo(HaveOccurred(), "delete failed at iteration %d", i)
		}

		elapsed := time.Since(start)
		_, _ = fmt.Fprintf(GinkgoWriter,
			"CRUD cycles: %d in %v (%.1f cycles/sec)\n",
			resourceCount, elapsed, float64(resourceCount)/elapsed.Seconds())
	})
})

// setupExternalPostgreSQL creates the target namespace and a Secret with the
// connection string from PERF_PG_ENDPOINT. The PostgreSQL instance must already
// be running and reachable from the cluster.
func setupExternalPostgreSQL() {
	endpoint := os.Getenv("PERF_PG_ENDPOINT")
	Expect(endpoint).NotTo(BeEmpty(),
		"PERF_STORAGE_MODE=PostgreSQL requires PERF_PG_ENDPOINT to be set "+
			"(e.g. postgres://user:pass@host:5432/db?sslmode=disable)")

	By("creating the target namespace for external PostgreSQL")
	cmd := exec.Command("kubectl", "create", "ns", shardNamespace)
	_, err := run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By("creating the connection Secret from PERF_PG_ENDPOINT")
	secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  KINE_ENDPOINT: %q
`, pgSecretName, shardNamespace, endpoint)

	cmd = exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = stringReader(secretYAML)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(),
		"Failed to create PostgreSQL connection Secret (output redacted to avoid leaking credentials)")
	_ = output
}

// buildAPIShardYAML returns the APIShard manifest configured for the active storage mode.
func buildAPIShardYAML() string {
	if useExternalPostgreSQL() {
		return fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %[1]s
spec:
  targetNamespace: %[2]s
  forceAggregation: true
  apiGroups:
    - group: %[3]s
      versions:
        - v1
  storage:
    type: PostgreSQL
    connectionSecretRef:
      name: %[4]s
      key: KINE_ENDPOINT
  namespaceSync:
    labelSelector:
      matchLabels:
        e2e-test: perf-shard
  secondary:
    replicas: 1
  kine:
    replicas: 1
`, shardName, shardNamespace, benchmarkAPIGroup, pgSecretName)
	}

	return fmt.Sprintf(`apiVersion: kube-shard.konflux-ci.dev/v1alpha1
kind: APIShard
metadata:
  name: %[1]s
spec:
  targetNamespace: %[2]s
  forceAggregation: true
  apiGroups:
    - group: %[3]s
      versions:
        - v1
  storage:
    type: InClusterPostgreSQL
    inCluster:
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          memory: 512Mi
  namespaceSync:
    labelSelector:
      matchLabels:
        e2e-test: perf-shard
  secondary:
    replicas: 1
  kine:
    replicas: 1
`, shardName, shardNamespace, benchmarkAPIGroup)
}

func getProjectDir() string {
	wd, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	return filepath.Join(wd, "..", "..", "..")
}

func run(cmd *exec.Cmd) (string, error) {
	command := strings.Join(cmd.Args, " ")
	output, err := cmd.CombinedOutput()
	if cmdLog != nil {
		_, _ = fmt.Fprintf(cmdLog, "$ %s\n%s", command, string(output))
		if err != nil {
			_, _ = fmt.Fprintf(cmdLog, "EXIT: %v\n", err)
		}
		_, _ = fmt.Fprintln(cmdLog)
	}
	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return string(output), nil
}

// runWithRetry applies YAML via kubectl with retries on transient failures.
func runWithRetry(yaml string, maxRetries int) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = stringReader(yaml)
		if _, err := run(cmd); err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func stringReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
