package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// taskInfo captures the metadata extracted from a single PipelineTask that is
// needed to generate a load-test equivalent. For bundle-resolved tasks the
// bundleRef and bundleTaskName point at the OCI image; for inline tasks the
// inlineSteps/inlineSize describe the embedded spec directly.
type taskInfo struct {
	name           string
	bundleRef      string
	bundleTaskName string
	runAfter       []string
	matrix         *pipelinev1.Matrix
	isFinallyTask  bool
	inlineSteps    int
	inlineSize     int
}

// config holds the CLI-provided settings that control the generated
// PipelineRun's namespace, naming, container image, sleep range, and the
// fallback task size/step count used when bundle resolution is skipped or fails.
type config struct {
	namespace         string
	prefix            string
	image             string
	sleepMin          int
	sleepMax          int
	defaultTaskSizeKB int
	defaultSteps      int
}

// extractTaskInfos walks the PipelineRun's inline PipelineSpec and returns a
// taskInfo for every regular and finally task. Returns nil when the
// PipelineRun has no inline PipelineSpec.
func extractTaskInfos(pr *pipelinev1.PipelineRun) []taskInfo {
	var infos []taskInfo
	if pr.Spec.PipelineSpec == nil {
		return infos
	}
	for _, pt := range pr.Spec.PipelineSpec.Tasks {
		infos = append(infos, extractSingleTaskInfo(pt, false))
	}
	for _, pt := range pr.Spec.PipelineSpec.Finally {
		infos = append(infos, extractSingleTaskInfo(pt, true))
	}
	return infos
}

// extractSingleTaskInfo converts one PipelineTask into a taskInfo, extracting
// the bundle resolver reference (if present) and inline spec metrics.
func extractSingleTaskInfo(pt pipelinev1.PipelineTask, isFinally bool) taskInfo {
	info := taskInfo{
		name:          pt.Name,
		runAfter:      pt.RunAfter,
		matrix:        pt.Matrix,
		isFinallyTask: isFinally,
	}

	if pt.TaskRef != nil {
		if string(pt.TaskRef.Resolver) == "bundles" {
			for _, p := range pt.TaskRef.Params {
				switch p.Name {
				case "bundle":
					info.bundleRef = p.Value.StringVal
				case "name":
					info.bundleTaskName = p.Value.StringVal
				}
			}
		}
	}

	if pt.TaskSpec != nil {
		info.inlineSteps = len(pt.TaskSpec.Steps)
		data, _ := yaml.Marshal(pt.TaskSpec)
		info.inlineSize = len(data)
	}

	return info
}

// resolveMatrixValues substitutes $(params.<name>) references in Matrix
// parameters with the concrete array values from the PipelineRun's params.
// Returns nil when matrix is nil.
func resolveMatrixValues(matrix *pipelinev1.Matrix, prParams []pipelinev1.Param) *pipelinev1.Matrix {
	if matrix == nil {
		return nil
	}

	paramMap := make(map[string][]string)
	for _, p := range prParams {
		if p.Value.Type == pipelinev1.ParamTypeArray {
			paramMap[p.Name] = p.Value.ArrayVal
		}
	}

	resolved := &pipelinev1.Matrix{
		Params:  make(pipelinev1.Params, len(matrix.Params)),
		Include: matrix.Include,
	}

	for i, mp := range matrix.Params {
		resolved.Params[i] = pipelinev1.Param{
			Name:  mp.Name,
			Value: mp.Value,
		}
		if len(mp.Value.ArrayVal) == 1 {
			ref := mp.Value.ArrayVal[0]
			if strings.HasPrefix(ref, "$(params.") && strings.HasSuffix(ref, ")") {
				paramName := ref[len("$(params.") : len(ref)-1]
				if vals, ok := paramMap[paramName]; ok {
					resolved.Params[i].Value = pipelinev1.ParamValue{
						Type:     pipelinev1.ParamTypeArray,
						ArrayVal: vals,
					}
				}
			}
		}
	}
	return resolved
}

// matrixExpansionCount returns the number of TaskRuns that the matrix will
// fan out into. For a nil matrix the count is 1 (no expansion). When only
// Include entries are present the count equals len(Include).
func matrixExpansionCount(matrix *pipelinev1.Matrix) int {
	if matrix == nil {
		return 1
	}
	count := 1
	for _, mp := range matrix.Params {
		if n := len(mp.Value.ArrayVal); n > 0 {
			count *= n
		}
	}
	if count == 1 && len(matrix.Params) == 0 && len(matrix.Include) > 0 {
		return len(matrix.Include)
	}
	return count
}

// generatePipelineRun assembles a complete PipelineRun with inline task specs
// sized to match the resolved (or default) storage footprint. Each task's
// steps contain padded sleep scripts so the Tekton controller produces
// realistic TaskRun objects.
func generatePipelineRun(tasks []taskInfo, resolved map[string]*resolvedTask, prParams []pipelinev1.Param, cfg config) *pipelinev1.PipelineRun {
	pr := &pipelinev1.PipelineRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "tekton.dev/v1",
			Kind:       "PipelineRun",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: cfg.prefix,
			Namespace:    cfg.namespace,
		},
		Spec: pipelinev1.PipelineRunSpec{
			PipelineSpec: &pipelinev1.PipelineSpec{},
		},
	}

	for _, t := range tasks {
		pt := generatePipelineTask(t, resolved[t.name], prParams, cfg)
		if t.isFinallyTask {
			pr.Spec.PipelineSpec.Finally = append(pr.Spec.PipelineSpec.Finally, pt)
		} else {
			pr.Spec.PipelineSpec.Tasks = append(pr.Spec.PipelineSpec.Tasks, pt)
		}
	}

	return pr
}

// generatePipelineTask builds a single PipelineTask with an inline TaskSpec
// whose step count and total serialized size approximate the original task.
// Resolved bundle metadata takes priority, then inline spec metrics, then
// config defaults.
func generatePipelineTask(t taskInfo, r *resolvedTask, prParams []pipelinev1.Param, cfg config) pipelinev1.PipelineTask {
	stepCount := cfg.defaultSteps
	targetSize := cfg.defaultTaskSizeKB * 1024
	var stepNames []string

	if r != nil && r.Err == nil {
		if len(r.StepNames) > 0 {
			stepCount = len(r.StepNames)
			stepNames = r.StepNames
		}
		if r.SizeBytes > 0 {
			targetSize = r.SizeBytes
		}
	} else if r != nil && r.SizeBytes > 0 {
		targetSize = r.SizeBytes
	}

	if t.bundleRef == "" && t.inlineSteps > 0 {
		stepCount = t.inlineSteps
		if t.inlineSize > 0 {
			targetSize = t.inlineSize
		}
	}

	if stepCount < 1 {
		stepCount = 1
	}
	steps := make([]pipelinev1.Step, stepCount)
	perStepSize := targetSize / stepCount
	if perStepSize < 100 {
		perStepSize = 100
	}

	for i := range steps {
		sName := fmt.Sprintf("step-%d", i)
		if i < len(stepNames) && stepNames[i] != "" {
			sName = stepNames[i]
		}
		steps[i] = pipelinev1.Step{
			Name:   sName,
			Image:  cfg.image,
			Script: padScript(randomInt(cfg.sleepMin, cfg.sleepMax), perStepSize),
		}
	}

	pt := pipelinev1.PipelineTask{
		Name:     t.name,
		RunAfter: t.runAfter,
		TaskSpec: &pipelinev1.EmbeddedTask{
			TaskSpec: pipelinev1.TaskSpec{
				Steps: steps,
			},
		},
	}

	if t.matrix != nil {
		resolvedMatrix := resolveMatrixValues(t.matrix, prParams)
		pt.Matrix = resolvedMatrix
		for _, mp := range resolvedMatrix.Params {
			pt.TaskSpec.Params = append(pt.TaskSpec.Params, pipelinev1.ParamSpec{
				Name: mp.Name,
				Type: pipelinev1.ParamTypeString,
			})
		}
	}

	return pt
}

// padScript generates a shell script that sleeps for sleepSecs and is padded
// with random base64 data in a comment to reach approximately targetBytes in
// total length. This ensures the serialized TaskSpec matches the real task's
// storage footprint.
func padScript(sleepSecs int, targetBytes int) string {
	base := fmt.Sprintf("#!/usr/bin/env sh\nsleep %d\n", sleepSecs)
	prefix := "# padding: "
	overhead := len(base) + len(prefix) + 1
	needed := targetBytes - overhead
	if needed <= 0 {
		return base
	}
	rawBytes := (needed * 3) / 4
	if rawBytes <= 0 {
		return base
	}
	buf := make([]byte, rawBytes)
	_, _ = rand.Read(buf)
	padding := base64.StdEncoding.EncodeToString(buf)
	if len(padding) > needed {
		padding = padding[:needed]
	}
	return base + prefix + padding + "\n"
}

// randomInt returns a cryptographically random integer in [min, max].
// When min >= max it returns min. Falls back to min if the entropy
// source is unavailable.
func randomInt(min, max int) int {
	if min >= max {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}
