package main

import (
	"strings"
	"testing"
)

func TestCleanupYAML_RemovesEmptyFields(t *testing.T) {
	input := strings.Join([]string{
		"apiVersion: tekton.dev/v1",
		"kind: PipelineRun",
		"metadata: {}",
		"spec:",
		"  pipelineSpec:",
		"    tasks:",
		"    - name: build",
		"      taskSpec:",
		"        steps:",
		"        - name: step-0",
		"          computeResources: {}",
		"          image: busybox",
		"  status: {}",
		"  taskRunTemplate: {}",
		"status: {}",
	}, "\n")

	result := string(cleanupYAML([]byte(input)))

	for _, removed := range []string{
		"metadata: {}",
		"computeResources: {}",
		"status: {}",
		"taskRunTemplate: {}",
	} {
		if strings.Contains(result, removed) {
			t.Errorf("expected %q to be removed", removed)
		}
	}

	for _, kept := range []string{
		"apiVersion: tekton.dev/v1",
		"kind: PipelineRun",
		"name: build",
		"image: busybox",
	} {
		if !strings.Contains(result, kept) {
			t.Errorf("expected %q to be kept", kept)
		}
	}
}

func TestCleanupYAML_PreservesNonEmptyFields(t *testing.T) {
	input := strings.Join([]string{
		"metadata:",
		"  generateName: load-test-",
		"  namespace: default",
		"spec:",
		"  pipelineSpec:",
		"    tasks: []",
	}, "\n")

	result := string(cleanupYAML([]byte(input)))
	if result != input {
		t.Errorf("expected no changes for non-empty fields:\ngot:  %q\nwant: %q", result, input)
	}
}

func TestCleanupYAML_RemovesSpecNull(t *testing.T) {
	input := "spec: null\nother: value"
	result := string(cleanupYAML([]byte(input)))
	if strings.Contains(result, "spec: null") {
		t.Error("expected 'spec: null' to be removed")
	}
	if !strings.Contains(result, "other: value") {
		t.Error("expected 'other: value' to be preserved")
	}
}

func TestCleanupYAML_EmptyInput(t *testing.T) {
	result := cleanupYAML([]byte(""))
	if string(result) != "" {
		t.Errorf("expected empty output, got %q", string(result))
	}
}

func TestCleanupYAML_IndentedEmptyFields(t *testing.T) {
	input := strings.Join([]string{
		"spec:",
		"  pipelineSpec:",
		"    tasks:",
		"    - name: t1",
		"      taskSpec:",
		"        computeResources: {}",
		"        steps:",
		"        - name: s1",
	}, "\n")

	result := string(cleanupYAML([]byte(input)))
	if strings.Contains(result, "computeResources: {}") {
		t.Error("expected indented 'computeResources: {}' to be removed")
	}
	if !strings.Contains(result, "name: s1") {
		t.Error("expected 'name: s1' to be preserved")
	}
}
