package main

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	ociV1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"sigs.k8s.io/yaml"
)

type resolvedTask struct {
	Name      string
	StepNames []string
	SizeBytes int
	Err       error
}

func resolveBundles(tasks []taskInfo) map[string]*resolvedTask {
	results := make(map[string]*resolvedTask, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, t := range tasks {
		if t.bundleRef == "" {
			continue
		}
		wg.Add(1)
		go func(t taskInfo) {
			defer wg.Done()
			r := resolveBundle(t)
			mu.Lock()
			results[t.name] = r
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return results
}

func resolveBundle(t taskInfo) *resolvedTask {
	ref, err := name.ParseReference(t.bundleRef)
	if err != nil {
		return &resolvedTask{Name: t.name, Err: fmt.Errorf("parse ref %q: %w", t.bundleRef, err)}
	}

	img, err := remote.Image(ref)
	if err != nil {
		return &resolvedTask{Name: t.name, Err: fmt.Errorf("pull %q: %w", t.bundleRef, err)}
	}

	manifest, err := img.Manifest()
	if err != nil {
		return &resolvedTask{Name: t.name, Err: fmt.Errorf("manifest: %w", err)}
	}

	layers, err := img.Layers()
	if err != nil {
		return &resolvedTask{Name: t.name, Err: fmt.Errorf("layers: %w", err)}
	}

	for i, desc := range manifest.Layers {
		kind := desc.Annotations["dev.tekton.image.kind"]
		layerName := desc.Annotations["dev.tekton.image.name"]
		if kind != "task" {
			continue
		}
		if t.bundleTaskName != "" && layerName != t.bundleTaskName {
			continue
		}

		task, rawSize, err := extractTask(layers[i])
		if err != nil {
			return &resolvedTask{
				Name:      t.name,
				SizeBytes: int(desc.Size),
				Err:       fmt.Errorf("extract (using compressed layer size): %w", err),
			}
		}

		stepNames := make([]string, len(task.Spec.Steps))
		for j, s := range task.Spec.Steps {
			stepNames[j] = s.Name
		}
		return &resolvedTask{
			Name:      t.name,
			StepNames: stepNames,
			SizeBytes: rawSize,
		}
	}

	return &resolvedTask{Name: t.name, Err: fmt.Errorf("no task layer found in bundle")}
}

func extractTask(layer ociV1.Layer) (*pipelinev1.Task, int, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		return nil, 0, fmt.Errorf("uncompress: %w", err)
	}
	defer rc.Close()

	var lastSize int
	tr := tar.NewReader(rc)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, lastSize, fmt.Errorf("tar: %w", err)
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, lastSize, fmt.Errorf("read: %w", err)
		}
		lastSize = len(data)

		var task pipelinev1.Task
		if err := yaml.Unmarshal(data, &task); err == nil && task.Kind == "Task" {
			return &task, len(data), nil
		}
		if err := json.Unmarshal(data, &task); err == nil && task.Kind == "Task" {
			return &task, len(data), nil
		}
	}
	if lastSize > 0 {
		return nil, lastSize, fmt.Errorf("could not parse %d bytes as Task", lastSize)
	}
	return nil, 0, fmt.Errorf("empty tar")
}
