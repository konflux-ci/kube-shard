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

package condition

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetCondition updates or adds a condition, automatically setting ObservedGeneration
// from the object's current generation. If the condition status hasn't changed,
// apimeta.SetStatusCondition preserves LastTransitionTime.
func SetCondition(obj client.Object, conditions *[]metav1.Condition, condition metav1.Condition) {
	condition.ObservedGeneration = obj.GetGeneration()
	apimeta.SetStatusCondition(conditions, condition)
}

// ReconcileErrorHandler provides consistent error reporting for reconcilers.
// It sets a failed condition, updates status, and returns the error for controller-runtime.
type ReconcileErrorHandler struct {
	log          logr.Logger
	statusClient client.StatusWriter
	obj          client.Object
	conditions   *[]metav1.Condition
	setPhase     func(string)
}

// NewReconcileErrorHandler creates a handler for a specific reconciled object.
// setPhase is called with the error phase string; pass nil if the CR has no phase field.
func NewReconcileErrorHandler(
	log logr.Logger,
	statusClient client.StatusWriter,
	obj client.Object,
	conditions *[]metav1.Condition,
	setPhase func(string),
) *ReconcileErrorHandler {
	return &ReconcileErrorHandler{
		log:          log,
		statusClient: statusClient,
		obj:          obj,
		conditions:   conditions,
		setPhase:     setPhase,
	}
}

// Handle sets a failed Reconciled condition, updates status, and returns the error.
func (h *ReconcileErrorHandler) Handle(ctx context.Context, err error, message string) (ctrl.Result, error) {
	h.log.Error(err, message)

	if h.setPhase != nil {
		h.setPhase("Error")
	}

	SetCondition(h.obj, h.conditions, metav1.Condition{
		Type:    "Reconciled",
		Status:  metav1.ConditionFalse,
		Reason:  "ReconcileError",
		Message: fmt.Sprintf("%s: %v", message, err),
	})

	if updateErr := h.statusClient.Update(ctx, h.obj); updateErr != nil {
		h.log.Error(updateErr, "Failed to update status after error")
	}

	return ctrl.Result{}, err
}
