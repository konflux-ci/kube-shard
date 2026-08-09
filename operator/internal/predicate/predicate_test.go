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

package predicate

import (
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestDeploymentReadinessPredicate_StatusOnly_NoReplicaChange(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	new := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if DeploymentReadinessPredicate.Update(e) {
		t.Error("expected false when nothing changed")
	}
}

func TestDeploymentReadinessPredicate_ReadyReplicasChanged(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 1},
	}
	new := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !DeploymentReadinessPredicate.Update(e) {
		t.Error("expected true when ReadyReplicas changed")
	}
}

func TestDeploymentReadinessPredicate_GenerationChanged(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	new := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !DeploymentReadinessPredicate.Update(e) {
		t.Error("expected true when generation changed")
	}
}

func TestStatefulSetReadinessPredicate_StatusOnly_NoReplicaChange(t *testing.T) {
	old := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1},
	}
	new := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if StatefulSetReadinessPredicate.Update(e) {
		t.Error("expected false when nothing changed")
	}
}

func TestStatefulSetReadinessPredicate_ReadyReplicasChanged(t *testing.T) {
	old := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 2},
	}
	new := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !StatefulSetReadinessPredicate.Update(e) {
		t.Error("expected true when ReadyReplicas changed")
	}
}

func TestStatefulSetReadinessPredicate_AvailableReplicasChanged(t *testing.T) {
	old := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 1},
	}
	new := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !StatefulSetReadinessPredicate.Update(e) {
		t.Error("expected true when AvailableReplicas changed")
	}
}

func TestStatefulSetReadinessPredicate_GenerationChanged(t *testing.T) {
	old := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1},
	}
	new := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Status:     appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !StatefulSetReadinessPredicate.Update(e) {
		t.Error("expected true when generation changed")
	}
}

func TestIgnoreStatusUpdatesPredicate_StatusOnly(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	new := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if IgnoreStatusUpdatesPredicate.Update(e) {
		t.Error("expected false for status-only update")
	}
}

func TestIgnoreStatusUpdatesPredicate_LabelChange(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1, Labels: map[string]string{"a": "1"}},
	}
	new := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1, Labels: map[string]string{"a": "2"}},
	}

	e := event.UpdateEvent{ObjectOld: old, ObjectNew: new}
	if !IgnoreStatusUpdatesPredicate.Update(e) {
		t.Error("expected true when labels changed")
	}
}

// --- APIServiceAvailabilityPredicate tests ---

func newAPIService(
	available apiregistrationv1.ConditionStatus,
) *apiregistrationv1.APIService {
	svc := &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v1.example.com", Generation: 1,
		},
	}
	if available != "" {
		svc.Status.Conditions = []apiregistrationv1.APIServiceCondition{
			{Type: apiregistrationv1.Available, Status: available},
		}
	}
	return svc
}

func TestAPIServiceAvailabilityPredicate_TriggersOnAvailableChange(t *testing.T) {
	g := NewGomegaWithT(t)

	oldObj := newAPIService(apiregistrationv1.ConditionFalse)
	newObj := newAPIService(apiregistrationv1.ConditionTrue)

	result := APIServiceAvailabilityPredicate.Update(event.UpdateEvent{
		ObjectOld: oldObj,
		ObjectNew: newObj,
	})
	g.Expect(result).To(BeTrue(),
		"should trigger when Available changes from False to True")
}

func TestAPIServiceAvailabilityPredicate_IgnoresNoChange(t *testing.T) {
	g := NewGomegaWithT(t)

	oldObj := newAPIService(apiregistrationv1.ConditionTrue)
	newObj := newAPIService(apiregistrationv1.ConditionTrue)

	result := APIServiceAvailabilityPredicate.Update(event.UpdateEvent{
		ObjectOld: oldObj,
		ObjectNew: newObj,
	})
	g.Expect(result).To(BeFalse(),
		"should not trigger when Available stays True")
}

func TestAPIServiceAvailabilityPredicate_TriggersOnGenerationChange(t *testing.T) {
	g := NewGomegaWithT(t)

	oldObj := newAPIService(apiregistrationv1.ConditionTrue)
	newObj := newAPIService(apiregistrationv1.ConditionTrue)
	newObj.Generation = 2

	result := APIServiceAvailabilityPredicate.Update(event.UpdateEvent{
		ObjectOld: oldObj,
		ObjectNew: newObj,
	})
	g.Expect(result).To(BeTrue(),
		"should trigger on generation change")
}

func TestAPIServiceAvailabilityPredicate_TriggersOnNewCondition(t *testing.T) {
	g := NewGomegaWithT(t)

	oldObj := newAPIService("")
	newObj := newAPIService(apiregistrationv1.ConditionTrue)

	result := APIServiceAvailabilityPredicate.Update(event.UpdateEvent{
		ObjectOld: oldObj,
		ObjectNew: newObj,
	})
	g.Expect(result).To(BeTrue(),
		"should trigger when Available condition appears")
}
