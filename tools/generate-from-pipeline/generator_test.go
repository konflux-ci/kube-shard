package main

import (
	"strings"
	"testing"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractTaskInfos_NilPipelineSpec(t *testing.T) {
	pr := &pipelinev1.PipelineRun{}
	infos := extractTaskInfos(pr)
	if len(infos) != 0 {
		t.Fatalf("expected 0 taskInfos, got %d", len(infos))
	}
}

func TestExtractTaskInfos_RegularAndFinallyTasks(t *testing.T) {
	pr := &pipelinev1.PipelineRun{
		Spec: pipelinev1.PipelineRunSpec{
			PipelineSpec: &pipelinev1.PipelineSpec{
				Tasks: []pipelinev1.PipelineTask{
					{Name: "build", RunAfter: []string{"clone"}},
					{Name: "test"},
				},
				Finally: []pipelinev1.PipelineTask{
					{Name: "cleanup"},
				},
			},
		},
	}

	infos := extractTaskInfos(pr)
	if len(infos) != 3 {
		t.Fatalf("expected 3 taskInfos, got %d", len(infos))
	}

	if infos[0].name != "build" || infos[0].isFinallyTask {
		t.Errorf("task 0: expected build (regular), got %q (finally=%v)", infos[0].name, infos[0].isFinallyTask)
	}
	if infos[1].name != "test" || infos[1].isFinallyTask {
		t.Errorf("task 1: expected test (regular), got %q (finally=%v)", infos[1].name, infos[1].isFinallyTask)
	}
	if infos[2].name != "cleanup" || !infos[2].isFinallyTask {
		t.Errorf("task 2: expected cleanup (finally), got %q (finally=%v)", infos[2].name, infos[2].isFinallyTask)
	}
}

func TestExtractSingleTaskInfo_BundleResolver(t *testing.T) {
	pt := pipelinev1.PipelineTask{
		Name:     "build-images",
		RunAfter: []string{"clone"},
		TaskRef: &pipelinev1.TaskRef{
			ResolverRef: pipelinev1.ResolverRef{
				Resolver: "bundles",
				Params: pipelinev1.Params{
					{Name: "bundle", Value: pipelinev1.ParamValue{
						Type:      pipelinev1.ParamTypeString,
						StringVal: "quay.io/org/task:latest",
					}},
					{Name: "name", Value: pipelinev1.ParamValue{
						Type:      pipelinev1.ParamTypeString,
						StringVal: "my-task",
					}},
				},
			},
		},
	}

	info := extractSingleTaskInfo(pt, false)
	if info.bundleRef != "quay.io/org/task:latest" {
		t.Errorf("expected bundleRef %q, got %q", "quay.io/org/task:latest", info.bundleRef)
	}
	if info.bundleTaskName != "my-task" {
		t.Errorf("expected bundleTaskName %q, got %q", "my-task", info.bundleTaskName)
	}
	if info.isFinallyTask {
		t.Error("expected isFinallyTask=false")
	}
}

func TestExtractSingleTaskInfo_InlineSpec(t *testing.T) {
	pt := pipelinev1.PipelineTask{
		Name: "inline-task",
		TaskSpec: &pipelinev1.EmbeddedTask{
			TaskSpec: pipelinev1.TaskSpec{
				Steps: []pipelinev1.Step{
					{Name: "step-1", Image: "busybox"},
					{Name: "step-2", Image: "busybox"},
				},
			},
		},
	}

	info := extractSingleTaskInfo(pt, true)
	if info.inlineSteps != 2 {
		t.Errorf("expected 2 inlineSteps, got %d", info.inlineSteps)
	}
	if info.inlineSize == 0 {
		t.Error("expected inlineSize > 0")
	}
	if !info.isFinallyTask {
		t.Error("expected isFinallyTask=true")
	}
	if info.bundleRef != "" {
		t.Errorf("expected empty bundleRef for inline task, got %q", info.bundleRef)
	}
}

func TestExtractSingleTaskInfo_NonBundleResolver(t *testing.T) {
	pt := pipelinev1.PipelineTask{
		Name: "git-task",
		TaskRef: &pipelinev1.TaskRef{
			ResolverRef: pipelinev1.ResolverRef{
				Resolver: "git",
				Params: pipelinev1.Params{
					{Name: "url", Value: pipelinev1.ParamValue{StringVal: "https://example.com"}},
				},
			},
		},
	}

	info := extractSingleTaskInfo(pt, false)
	if info.bundleRef != "" {
		t.Errorf("expected empty bundleRef for non-bundle resolver, got %q", info.bundleRef)
	}
}

func TestResolveMatrixValues_NilMatrix(t *testing.T) {
	result := resolveMatrixValues(nil, nil)
	if result != nil {
		t.Error("expected nil for nil matrix")
	}
}

func TestResolveMatrixValues_NoSubstitution(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Params: pipelinev1.Params{
			{Name: "platform", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"linux/amd64", "linux/arm64"},
			}},
		},
	}

	result := resolveMatrixValues(matrix, nil)
	if len(result.Params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(result.Params))
	}
	if len(result.Params[0].Value.ArrayVal) != 2 {
		t.Errorf("expected 2 values, got %d", len(result.Params[0].Value.ArrayVal))
	}
}

func TestResolveMatrixValues_WithSubstitution(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Params: pipelinev1.Params{
			{Name: "platform", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"$(params.PLATFORMS)"},
			}},
		},
	}
	prParams := []pipelinev1.Param{
		{Name: "PLATFORMS", Value: pipelinev1.ParamValue{
			Type:     pipelinev1.ParamTypeArray,
			ArrayVal: []string{"linux/amd64", "linux/arm64", "linux/s390x"},
		}},
	}

	result := resolveMatrixValues(matrix, prParams)
	if len(result.Params[0].Value.ArrayVal) != 3 {
		t.Fatalf("expected 3 substituted values, got %d", len(result.Params[0].Value.ArrayVal))
	}
	if result.Params[0].Value.ArrayVal[2] != "linux/s390x" {
		t.Errorf("expected linux/s390x, got %q", result.Params[0].Value.ArrayVal[2])
	}
}

func TestResolveMatrixValues_UnresolvedParam(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Params: pipelinev1.Params{
			{Name: "platform", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"$(params.MISSING)"},
			}},
		},
	}

	result := resolveMatrixValues(matrix, nil)
	if result.Params[0].Value.ArrayVal[0] != "$(params.MISSING)" {
		t.Error("expected unresolved param reference to be preserved")
	}
}

func TestMatrixExpansionCount_NilMatrix(t *testing.T) {
	if got := matrixExpansionCount(nil); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestMatrixExpansionCount_SingleParam(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Params: pipelinev1.Params{
			{Name: "platform", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"amd64", "arm64"},
			}},
		},
	}
	if got := matrixExpansionCount(matrix); got != 2 {
		t.Errorf("expected 2, got %d", got)
	}
}

func TestMatrixExpansionCount_MultipleParams(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Params: pipelinev1.Params{
			{Name: "platform", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"amd64", "arm64"},
			}},
			{Name: "os", Value: pipelinev1.ParamValue{
				Type:     pipelinev1.ParamTypeArray,
				ArrayVal: []string{"linux", "darwin", "windows"},
			}},
		},
	}
	if got := matrixExpansionCount(matrix); got != 6 {
		t.Errorf("expected 2*3=6, got %d", got)
	}
}

func TestMatrixExpansionCount_IncludeOnly(t *testing.T) {
	matrix := &pipelinev1.Matrix{
		Include: []pipelinev1.IncludeParams{
			{Name: "combo-1"},
			{Name: "combo-2"},
			{Name: "combo-3"},
		},
	}
	if got := matrixExpansionCount(matrix); got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
}

func TestGeneratePipelineRun_BasicStructure(t *testing.T) {
	tasks := []taskInfo{
		{name: "build", runAfter: []string{"clone"}},
		{name: "cleanup", isFinallyTask: true},
	}
	cfg := config{
		namespace:         "test-ns",
		prefix:            "lt-",
		image:             "alpine",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 1,
		defaultSteps:      2,
	}

	pr := generatePipelineRun(tasks, nil, nil, nil, cfg)

	if pr.GenerateName != "lt-" {
		t.Errorf("expected generateName %q, got %q", "lt-", pr.GenerateName)
	}
	if pr.Namespace != "test-ns" {
		t.Errorf("expected namespace %q, got %q", "test-ns", pr.Namespace)
	}
	if pr.APIVersion != "tekton.dev/v1" {
		t.Errorf("expected apiVersion tekton.dev/v1, got %q", pr.APIVersion)
	}
	if len(pr.Spec.PipelineSpec.Tasks) != 1 {
		t.Fatalf("expected 1 regular task, got %d", len(pr.Spec.PipelineSpec.Tasks))
	}
	if len(pr.Spec.PipelineSpec.Finally) != 1 {
		t.Fatalf("expected 1 finally task, got %d", len(pr.Spec.PipelineSpec.Finally))
	}
	if pr.Spec.PipelineSpec.Tasks[0].Name != "build" {
		t.Errorf("expected task name %q, got %q", "build", pr.Spec.PipelineSpec.Tasks[0].Name)
	}
	if pr.Spec.PipelineSpec.Finally[0].Name != "cleanup" {
		t.Errorf("expected finally task name %q, got %q", "cleanup", pr.Spec.PipelineSpec.Finally[0].Name)
	}
}

func TestGeneratePipelineTask_WithResolvedBundle(t *testing.T) {
	ti := taskInfo{name: "scan", runAfter: []string{"build"}}
	r := &resolvedTask{
		Name:      "scan",
		StepNames: []string{"pull", "scan", "report"},
		SizeBytes: 5000,
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
	}

	pt := generatePipelineTask(ti, r, nil, cfg)
	if pt.Name != "scan" {
		t.Errorf("expected name %q, got %q", "scan", pt.Name)
	}
	if len(pt.TaskSpec.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(pt.TaskSpec.Steps))
	}
	if pt.TaskSpec.Steps[0].Name != "pull" {
		t.Errorf("expected step 0 name %q, got %q", "pull", pt.TaskSpec.Steps[0].Name)
	}
	if pt.TaskSpec.Steps[1].Name != "scan" {
		t.Errorf("expected step 1 name %q, got %q", "scan", pt.TaskSpec.Steps[1].Name)
	}
	if pt.TaskSpec.Steps[2].Name != "report" {
		t.Errorf("expected step 2 name %q, got %q", "report", pt.TaskSpec.Steps[2].Name)
	}
}

func TestGeneratePipelineTask_WithDefaults(t *testing.T) {
	ti := taskInfo{name: "default-task"}
	cfg := config{
		image:             "busybox",
		sleepMin:          5,
		sleepMax:          5,
		defaultTaskSizeKB: 2,
		defaultSteps:      4,
	}

	pt := generatePipelineTask(ti, nil, nil, cfg)
	if len(pt.TaskSpec.Steps) != 4 {
		t.Fatalf("expected 4 default steps, got %d", len(pt.TaskSpec.Steps))
	}
	for i, s := range pt.TaskSpec.Steps {
		if s.Image != "busybox" {
			t.Errorf("step %d: expected image busybox, got %q", i, s.Image)
		}
		if !strings.Contains(s.Script, "sleep 5") {
			t.Errorf("step %d: expected script to contain 'sleep 5'", i)
		}
	}
}

func TestGeneratePipelineTask_InlineTask(t *testing.T) {
	ti := taskInfo{
		name:        "inline",
		inlineSteps: 5,
		inlineSize:  3000,
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
	}

	pt := generatePipelineTask(ti, nil, nil, cfg)
	if len(pt.TaskSpec.Steps) != 5 {
		t.Errorf("expected 5 steps from inline spec, got %d", len(pt.TaskSpec.Steps))
	}
}

func TestGeneratePipelineTask_WithMatrix(t *testing.T) {
	ti := taskInfo{
		name: "matrix-task",
		matrix: &pipelinev1.Matrix{
			Params: pipelinev1.Params{
				{Name: "platform", Value: pipelinev1.ParamValue{
					Type:     pipelinev1.ParamTypeArray,
					ArrayVal: []string{"$(params.PLATFORMS)"},
				}},
			},
		},
	}
	prParams := []pipelinev1.Param{
		{Name: "PLATFORMS", Value: pipelinev1.ParamValue{
			Type:     pipelinev1.ParamTypeArray,
			ArrayVal: []string{"amd64", "arm64"},
		}},
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 1,
		defaultSteps:      1,
	}

	pt := generatePipelineTask(ti, nil, prParams, cfg)
	if pt.Matrix == nil {
		t.Fatal("expected matrix to be set")
	}
	if len(pt.Matrix.Params[0].Value.ArrayVal) != 2 {
		t.Errorf("expected 2 matrix values, got %d", len(pt.Matrix.Params[0].Value.ArrayVal))
	}
	if len(pt.TaskSpec.Params) != 1 || pt.TaskSpec.Params[0].Name != "platform" {
		t.Error("expected task param declared for matrix param")
	}
}

func TestGeneratePipelineRun_PreservesRunAfter(t *testing.T) {
	tasks := []taskInfo{
		{name: "clone"},
		{name: "build", runAfter: []string{"clone"}},
		{name: "test", runAfter: []string{"build"}},
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 1,
		defaultSteps:      1,
	}

	pr := generatePipelineRun(tasks, nil, nil, nil, cfg)
	pipelineTasks := pr.Spec.PipelineSpec.Tasks

	if len(pipelineTasks[0].RunAfter) != 0 {
		t.Errorf("clone should have no runAfter, got %v", pipelineTasks[0].RunAfter)
	}
	if len(pipelineTasks[1].RunAfter) != 1 || pipelineTasks[1].RunAfter[0] != "clone" {
		t.Errorf("build should runAfter clone, got %v", pipelineTasks[1].RunAfter)
	}
	if len(pipelineTasks[2].RunAfter) != 1 || pipelineTasks[2].RunAfter[0] != "build" {
		t.Errorf("test should runAfter build, got %v", pipelineTasks[2].RunAfter)
	}
}

func TestPadScript_SmallTarget(t *testing.T) {
	script := padScript(5, 10)
	if !strings.HasPrefix(script, "#!/usr/bin/env sh\n") {
		t.Error("expected shebang prefix")
	}
	if !strings.Contains(script, "sleep 5") {
		t.Error("expected sleep command")
	}
}

func TestPadScript_LargeTarget(t *testing.T) {
	target := 5000
	script := padScript(3, target)
	if !strings.Contains(script, "sleep 3") {
		t.Error("expected sleep 3")
	}
	if !strings.Contains(script, "# padding:") {
		t.Error("expected padding comment")
	}
	diff := len(script) - target
	if diff < -50 || diff > 50 {
		t.Errorf("expected script length ~%d, got %d (diff=%d)", target, len(script), diff)
	}
}

func TestPadScript_ZeroTarget(t *testing.T) {
	script := padScript(1, 0)
	if !strings.HasPrefix(script, "#!/usr/bin/env sh\n") {
		t.Error("expected base script only")
	}
	if strings.Contains(script, "# padding:") {
		t.Error("should not contain padding for zero target")
	}
}

func TestRandomInt_MinEqualsMax(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := randomInt(5, 5); got != 5 {
			t.Fatalf("expected 5, got %d", got)
		}
	}
}

func TestRandomInt_MinGreaterThanMax(t *testing.T) {
	if got := randomInt(10, 3); got != 10 {
		t.Errorf("expected 10, got %d", got)
	}
}

func TestRandomInt_Range(t *testing.T) {
	for i := 0; i < 200; i++ {
		got := randomInt(1, 10)
		if got < 1 || got > 10 {
			t.Fatalf("randomInt(1,10)=%d, out of range", got)
		}
	}
}

func TestExtractTaskInfos_FullPipelineRun(t *testing.T) {
	pr := &pipelinev1.PipelineRun{
		TypeMeta: metav1.TypeMeta{APIVersion: "tekton.dev/v1", Kind: "PipelineRun"},
		Spec: pipelinev1.PipelineRunSpec{
			PipelineSpec: &pipelinev1.PipelineSpec{
				Tasks: []pipelinev1.PipelineTask{
					{
						Name: "clone",
						TaskRef: &pipelinev1.TaskRef{
							ResolverRef: pipelinev1.ResolverRef{
								Resolver: "bundles",
								Params: pipelinev1.Params{
									{Name: "bundle", Value: pipelinev1.ParamValue{StringVal: "quay.io/org/clone:v1"}},
									{Name: "name", Value: pipelinev1.ParamValue{StringVal: "git-clone"}},
								},
							},
						},
					},
					{
						Name:     "build",
						RunAfter: []string{"clone"},
						TaskRef: &pipelinev1.TaskRef{
							ResolverRef: pipelinev1.ResolverRef{
								Resolver: "bundles",
								Params: pipelinev1.Params{
									{Name: "bundle", Value: pipelinev1.ParamValue{StringVal: "quay.io/org/build:v2"}},
									{Name: "name", Value: pipelinev1.ParamValue{StringVal: "buildah"}},
								},
							},
						},
						Matrix: &pipelinev1.Matrix{
							Params: pipelinev1.Params{
								{Name: "PLATFORM", Value: pipelinev1.ParamValue{
									Type:     pipelinev1.ParamTypeArray,
									ArrayVal: []string{"$(params.PLATFORMS)"},
								}},
							},
						},
					},
				},
				Finally: []pipelinev1.PipelineTask{
					{Name: "report"},
				},
			},
			Params: pipelinev1.Params{
				{Name: "PLATFORMS", Value: pipelinev1.ParamValue{
					Type:     pipelinev1.ParamTypeArray,
					ArrayVal: []string{"linux/amd64", "linux/arm64"},
				}},
			},
		},
	}

	infos := extractTaskInfos(pr)
	if len(infos) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(infos))
	}

	if infos[0].bundleRef != "quay.io/org/clone:v1" {
		t.Errorf("expected bundleRef for clone, got %q", infos[0].bundleRef)
	}
	if infos[1].bundleRef != "quay.io/org/build:v2" {
		t.Errorf("expected bundleRef for build, got %q", infos[1].bundleRef)
	}
	if infos[1].matrix == nil {
		t.Error("expected matrix on build task")
	}

	resolved := map[string]*resolvedTask{
		"clone": {Name: "clone", StepNames: []string{"clone-step"}, SizeBytes: 10240},
		"build": {Name: "build", StepNames: []string{"build", "push", "digest"}, SizeBytes: 43008},
	}
	cfg := config{
		namespace:         "default",
		prefix:            "load-test-",
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
	}

	outPR := generatePipelineRun(infos, resolved, pr.Spec.Params, nil, cfg)
	if len(outPR.Spec.PipelineSpec.Tasks) != 2 {
		t.Fatalf("expected 2 regular tasks, got %d", len(outPR.Spec.PipelineSpec.Tasks))
	}
	if len(outPR.Spec.PipelineSpec.Finally) != 1 {
		t.Fatalf("expected 1 finally task, got %d", len(outPR.Spec.PipelineSpec.Finally))
	}

	buildTask := outPR.Spec.PipelineSpec.Tasks[1]
	if buildTask.Matrix == nil {
		t.Fatal("expected matrix on generated build task")
	}
	if len(buildTask.Matrix.Params[0].Value.ArrayVal) != 2 {
		t.Errorf("expected 2 resolved matrix values, got %d", len(buildTask.Matrix.Params[0].Value.ArrayVal))
	}
}

func TestGeneratePipelineTask_PreserveResources_Resolved(t *testing.T) {
	ti := taskInfo{name: "build", runAfter: []string{"clone"}}
	r := &resolvedTask{
		Name:      "build",
		StepNames: []string{"compile", "push"},
		StepResources: []corev1.ResourceRequirements{
			{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
			{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
			},
		},
		SizeBytes: 5000,
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
		preserveResources: true,
	}

	pt := generatePipelineTask(ti, r, nil, cfg)
	if len(pt.TaskSpec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(pt.TaskSpec.Steps))
	}
	mem := pt.TaskSpec.Steps[0].ComputeResources.Requests[corev1.ResourceMemory]
	if mem.String() != "256Mi" {
		t.Errorf("step 0: expected memory request 256Mi, got %s", mem.String())
	}
	lim := pt.TaskSpec.Steps[0].ComputeResources.Limits[corev1.ResourceMemory]
	if lim.String() != "512Mi" {
		t.Errorf("step 0: expected memory limit 512Mi, got %s", lim.String())
	}
	mem1 := pt.TaskSpec.Steps[1].ComputeResources.Requests[corev1.ResourceMemory]
	if mem1.String() != "128Mi" {
		t.Errorf("step 1: expected memory request 128Mi, got %s", mem1.String())
	}
	if pt.TaskSpec.Steps[1].ComputeResources.Limits != nil {
		t.Errorf("step 1: expected nil limits, got %v", pt.TaskSpec.Steps[1].ComputeResources.Limits)
	}
}

func TestGeneratePipelineTask_PreserveResources_Off(t *testing.T) {
	ti := taskInfo{name: "build"}
	r := &resolvedTask{
		Name:      "build",
		StepNames: []string{"compile"},
		StepResources: []corev1.ResourceRequirements{
			{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
		},
		SizeBytes: 5000,
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
		preserveResources: false,
	}

	pt := generatePipelineTask(ti, r, nil, cfg)
	if pt.TaskSpec.Steps[0].ComputeResources.Requests != nil {
		t.Errorf("expected no resources when preserveResources=false, got %v",
			pt.TaskSpec.Steps[0].ComputeResources.Requests)
	}
}

func TestGeneratePipelineTask_PreserveResources_Inline(t *testing.T) {
	ti := taskInfo{
		name:        "inline",
		inlineSteps: 2,
		inlineSize:  3000,
		inlineStepResources: []corev1.ResourceRequirements{
			{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
			{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}},
		},
	}
	cfg := config{
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 17,
		defaultSteps:      3,
		preserveResources: true,
	}

	pt := generatePipelineTask(ti, nil, nil, cfg)
	if len(pt.TaskSpec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(pt.TaskSpec.Steps))
	}
	mem := pt.TaskSpec.Steps[0].ComputeResources.Requests[corev1.ResourceMemory]
	if mem.String() != "64Mi" {
		t.Errorf("step 0: expected 64Mi, got %s", mem.String())
	}
	mem1 := pt.TaskSpec.Steps[1].ComputeResources.Requests[corev1.ResourceMemory]
	if mem1.String() != "32Mi" {
		t.Errorf("step 1: expected 32Mi, got %s", mem1.String())
	}
}

func TestExtractSingleTaskInfo_InlineStepResources(t *testing.T) {
	pt := pipelinev1.PipelineTask{
		Name: "inline-resources",
		TaskSpec: &pipelinev1.EmbeddedTask{
			TaskSpec: pipelinev1.TaskSpec{
				Steps: []pipelinev1.Step{
					{
						Name:  "step-1",
						Image: "busybox",
						ComputeResources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")},
						},
					},
					{
						Name:  "step-2",
						Image: "busybox",
					},
				},
			},
		},
	}

	info := extractSingleTaskInfo(pt, false)
	if len(info.inlineStepResources) != 2 {
		t.Fatalf("expected 2 inlineStepResources, got %d", len(info.inlineStepResources))
	}
	mem := info.inlineStepResources[0].Requests[corev1.ResourceMemory]
	if mem.String() != "100Mi" {
		t.Errorf("expected 100Mi, got %s", mem.String())
	}
	if info.inlineStepResources[1].Requests != nil {
		t.Errorf("expected nil requests for step-2, got %v", info.inlineStepResources[1].Requests)
	}
}

func TestGeneratePipelineRun_TaskRunSpecs(t *testing.T) {
	tasks := []taskInfo{
		{name: "build"},
		{name: "test"},
	}
	taskRunSpecs := []pipelinev1.PipelineTaskRunSpec{
		{
			PipelineTaskName: "build",
			ComputeResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			StepSpecs: []pipelinev1.TaskRunStepSpec{
				{
					Name: "compile",
					ComputeResources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
					},
				},
			},
		},
		{
			PipelineTaskName: "unrelated",
			ComputeResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("999Mi")},
			},
		},
	}
	cfg := config{
		namespace:         "test-ns",
		prefix:            "lt-",
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 1,
		defaultSteps:      1,
		preserveResources: true,
	}

	pr := generatePipelineRun(tasks, nil, nil, taskRunSpecs, cfg)

	if len(pr.Spec.TaskRunSpecs) != 1 {
		t.Fatalf("expected 1 TaskRunSpec (build only), got %d", len(pr.Spec.TaskRunSpecs))
	}
	trs := pr.Spec.TaskRunSpecs[0]
	if trs.PipelineTaskName != "build" {
		t.Errorf("expected TaskRunSpec for build, got %q", trs.PipelineTaskName)
	}
	mem := trs.ComputeResources.Requests[corev1.ResourceMemory]
	if mem.String() != "1Gi" {
		t.Errorf("expected task-level compute 1Gi, got %s", mem.String())
	}
	if len(trs.StepSpecs) != 1 || trs.StepSpecs[0].Name != "compile" {
		t.Errorf("expected StepSpecs with compile, got %v", trs.StepSpecs)
	}
	stepLim := trs.StepSpecs[0].ComputeResources.Limits[corev1.ResourceMemory]
	if stepLim.String() != "2Gi" {
		t.Errorf("expected step limit 2Gi, got %s", stepLim.String())
	}
}

func TestGeneratePipelineRun_TaskRunSpecs_Disabled(t *testing.T) {
	tasks := []taskInfo{{name: "build"}}
	taskRunSpecs := []pipelinev1.PipelineTaskRunSpec{
		{
			PipelineTaskName: "build",
			ComputeResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
		},
	}
	cfg := config{
		namespace:         "test-ns",
		prefix:            "lt-",
		image:             "busybox",
		sleepMin:          1,
		sleepMax:          1,
		defaultTaskSizeKB: 1,
		defaultSteps:      1,
		preserveResources: false,
	}

	pr := generatePipelineRun(tasks, nil, nil, taskRunSpecs, cfg)
	if len(pr.Spec.TaskRunSpecs) != 0 {
		t.Errorf("expected no TaskRunSpecs when preserveResources=false, got %d", len(pr.Spec.TaskRunSpecs))
	}
}
