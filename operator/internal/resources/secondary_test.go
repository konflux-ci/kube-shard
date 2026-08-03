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

package resources

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeshardv1alpha1 "github.com/konflux-ci/kube-shard/operator/api/v1alpha1"
)

func newTestShard() *kubeshardv1alpha1.APIShard {
	return &kubeshardv1alpha1.APIShard{
		ObjectMeta: metav1.ObjectMeta{Name: "test-shard"},
		Spec: kubeshardv1alpha1.APIShardSpec{
			TargetNamespace: "test-ns",
			Secondary: kubeshardv1alpha1.SecondarySpec{
				Replicas: 1,
			},
			Kine: kubeshardv1alpha1.KineSpec{
				Replicas: 1,
			},
		},
	}
}

func findArg(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}

func TestBuildSecondaryDeployment_RequestHeaderAllowedNames_Kubernetes(t *testing.T) {
	shard := newTestShard()
	allowedNames := []string{"front-proxy-client"}

	deploy := BuildSecondaryDeployment(shard, allowedNames)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--requestheader-allowed-names=")
	want := "--requestheader-allowed-names=front-proxy-client"
	if got != want {
		t.Errorf("requestheader-allowed-names arg = %q, want %q", got, want)
	}
}

func TestBuildSecondaryDeployment_RequestHeaderAllowedNames_OpenShift(t *testing.T) {
	shard := newTestShard()
	allowedNames := []string{
		"kube-apiserver-proxy",
		"system:kube-apiserver-proxy",
		"system:openshift-aggregator",
	}

	deploy := BuildSecondaryDeployment(shard, allowedNames)
	args := deploy.Spec.Template.Spec.Containers[0].Args

	got := findArg(args, "--requestheader-allowed-names=")
	want := "--requestheader-allowed-names=kube-apiserver-proxy,system:kube-apiserver-proxy,system:openshift-aggregator"
	if got != want {
		t.Errorf("requestheader-allowed-names arg = %q, want %q", got, want)
	}
}

func TestBuildSecondaryDeployment_DefaultImage(t *testing.T) {
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	got := deploy.Spec.Template.Spec.Containers[0].Image
	if got != DefaultSecondaryImage {
		t.Errorf("image = %q, want %q", got, DefaultSecondaryImage)
	}
}

func TestBuildSecondaryDeployment_CustomImage(t *testing.T) {
	shard := newTestShard()
	shard.Spec.Secondary.Image = "custom-registry/kube-apiserver:v1.33.0"

	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	got := deploy.Spec.Template.Spec.Containers[0].Image
	if got != "custom-registry/kube-apiserver:v1.33.0" {
		t.Errorf("image = %q, want custom image", got)
	}
}

func TestBuildSecondaryDeployment_GracefulShutdown(t *testing.T) {
	shard := newTestShard()
	deploy := BuildSecondaryDeployment(shard, []string{"front-proxy-client"})

	args := deploy.Spec.Template.Spec.Containers[0].Args

	if got := findArg(args, "--shutdown-delay-duration="); got != "--shutdown-delay-duration=15s" {
		t.Errorf("shutdown-delay-duration arg = %q, want %q", got, "--shutdown-delay-duration=15s")
	}
	if got := findArg(args, "--shutdown-send-retry-after="); got != "--shutdown-send-retry-after=true" {
		t.Errorf("shutdown-send-retry-after arg = %q, want %q", got, "--shutdown-send-retry-after=true")
	}

	podSpec := deploy.Spec.Template.Spec
	if podSpec.TerminationGracePeriodSeconds == nil {
		t.Fatal("terminationGracePeriodSeconds is nil, want 65")
	}
	if *podSpec.TerminationGracePeriodSeconds != 65 {
		t.Errorf("terminationGracePeriodSeconds = %d, want 65", *podSpec.TerminationGracePeriodSeconds)
	}
}
