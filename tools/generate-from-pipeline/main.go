package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	input := flag.String("input", "", "Input PipelineRun YAML file (required)")
	output := flag.String("output", "", "Output load test PipelineRun YAML file (required)")
	namespace := flag.String("namespace", "default", "Namespace for generated PipelineRun")
	prefix := flag.String("prefix", "load-test-", "generateName prefix")
	image := flag.String("image", "busybox", "Step container image")
	sleepRange := flag.String("sleep-range", "1,10", "Min,max sleep seconds per step")
	skipResolve := flag.Bool("skip-resolve", false, "Don't resolve bundles, use defaults")
	defaultTaskSizeKB := flag.Int("default-task-size-kb", 17, "Fallback task size in KB when not resolving")
	defaultSteps := flag.Int("default-steps", 3, "Fallback step count when not resolving")
	flag.Parse()

	if *input == "" || *output == "" {
		fmt.Fprintf(os.Stderr, "Usage: generate-from-pipeline -input <file> -output <file>\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	sleepMin, sleepMax := 1, 10
	if parts := strings.SplitN(*sleepRange, ",", 2); len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &sleepMin)
		fmt.Sscanf(parts[1], "%d", &sleepMax)
	}

	cfg := config{
		namespace:         *namespace,
		prefix:            *prefix,
		image:             *image,
		sleepMin:          sleepMin,
		sleepMax:          sleepMax,
		defaultTaskSizeKB: *defaultTaskSizeKB,
		defaultSteps:      *defaultSteps,
	}

	data, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	var pr pipelinev1.PipelineRun
	if err := yaml.Unmarshal(data, &pr); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing PipelineRun YAML: %v\n", err)
		os.Exit(1)
	}

	if pr.Spec.PipelineSpec == nil {
		fmt.Fprintf(os.Stderr, "Error: PipelineRun has no inline pipelineSpec\n")
		os.Exit(1)
	}

	tasks := extractTaskInfos(&pr)
	if len(tasks) == 0 {
		fmt.Fprintf(os.Stderr, "No tasks found in PipelineRun\n")
		os.Exit(1)
	}

	var prParams []pipelinev1.Param
	prParams = append(prParams, pr.Spec.Params...)

	resolved := make(map[string]*resolvedTask)
	if !*skipResolve {
		bundleCount := 0
		for _, t := range tasks {
			if t.bundleRef != "" {
				bundleCount++
			}
		}
		if bundleCount > 0 {
			totalTaskRuns := 0
			for _, t := range tasks {
				m := resolveMatrixValues(t.matrix, prParams)
				totalTaskRuns += matrixExpansionCount(m)
			}
			fmt.Printf("Resolving %d task bundles (%d TaskRuns with matrix expansion)...\n", bundleCount, totalTaskRuns)
			resolved = resolveBundles(tasks)
		}
	}

	totalSteps := 0
	totalTaskRuns := 0
	totalSize := 0
	for _, t := range tasks {
		matrix := resolveMatrixValues(t.matrix, prParams)
		multiplier := matrixExpansionCount(matrix)
		totalTaskRuns += multiplier

		r := resolved[t.name]
		steps := cfg.defaultSteps
		size := cfg.defaultTaskSizeKB * 1024
		status := "default"

		if r != nil {
			if r.Err == nil {
				steps = len(r.StepNames)
				size = r.SizeBytes
				status = "resolved"
			} else if r.SizeBytes > 0 {
				size = r.SizeBytes
				status = fmt.Sprintf("partial (%v)", r.Err)
			} else {
				status = fmt.Sprintf("failed (%v)", r.Err)
			}
		} else if t.inlineSteps > 0 {
			steps = t.inlineSteps
			size = t.inlineSize
			status = "inline"
		}

		totalSteps += steps * multiplier
		totalSize += size * multiplier

		multiplierStr := ""
		if multiplier > 1 {
			multiplierStr = fmt.Sprintf(" [x%d]", multiplier)
		}
		fmt.Printf("  %-35s %6.1f KB, %2d steps  (%s)%s\n",
			t.name, float64(size)/1024, steps, status, multiplierStr)
	}

	outPR := generatePipelineRun(tasks, resolved, prParams, cfg)

	outData, err := yaml.Marshal(outPR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling output: %v\n", err)
		os.Exit(1)
	}
	outData = cleanupYAML(outData)

	if err := os.WriteFile(*output, outData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nWritten %s\n", *output)
	fmt.Printf("  %d tasks, %d TaskRuns (with matrix), %d total steps\n",
		len(tasks), totalTaskRuns, totalSteps)
	fmt.Printf("  PipelineRun YAML size: %.1f KB\n", float64(len(outData))/1024)
	fmt.Printf("  Expected per-build storage: ~%d KB (organic, via controller-created TaskRuns)\n",
		totalSize/1024)
}

// cleanupYAML removes empty/zero-value YAML fields that the Kubernetes types
// serialize by default (e.g. "metadata: {}", "status: {}") to keep the
// generated output clean and readable.
func cleanupYAML(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "metadata: {}", "spec: null", "computeResources: {}",
			"status: {}", "taskRunTemplate: {}":
			continue
		}
		cleaned = append(cleaned, line)
	}
	return []byte(strings.Join(cleaned, "\n"))
}
