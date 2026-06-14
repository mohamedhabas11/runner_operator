package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

func TestNewCondition_defaults(t *testing.T) {
	c := NewCondition(ConditionTypeReady).Build()

	if c.Type != ConditionTypeReady {
		t.Fatalf("expected Type %q, got %q", ConditionTypeReady, c.Type)
	}
	if c.Status != metav1.ConditionUnknown {
		t.Fatalf("expected Status Unknown, got %q", c.Status)
	}
	if c.Reason != "" {
		t.Fatalf("expected empty Reason, got %q", c.Reason)
	}
	if c.Message != "" {
		t.Fatalf("expected empty Message, got %q", c.Message)
	}
	if c.ObservedGeneration != 0 {
		t.Fatalf("expected ObservedGeneration 0, got %d", c.ObservedGeneration)
	}
	if c.LastTransitionTime.IsZero() {
		t.Fatal("expected non-zero LastTransitionTime")
	}
}

func TestConditionBuilder_chain(t *testing.T) {
	c := NewCondition("Deployed").
		WithStatus(metav1.ConditionTrue).
		WithReason("DeploySucceeded").
		WithMessage("All replicas are ready").
		WithObservedGeneration(3).
		Build()

	if c.Type != "Deployed" {
		t.Fatalf("expected Type 'Deployed', got %q", c.Type)
	}
	if c.Status != metav1.ConditionTrue {
		t.Fatalf("expected Status True, got %q", c.Status)
	}
	if c.Reason != "DeploySucceeded" {
		t.Fatalf("expected Reason 'DeploySucceeded', got %q", c.Reason)
	}
	if c.Message != "All replicas are ready" {
		t.Fatalf("expected Message 'All replicas are ready', got %q", c.Message)
	}
	if c.ObservedGeneration != 3 {
		t.Fatalf("expected ObservedGeneration 3, got %d", c.ObservedGeneration)
	}
}

func TestConditionBuilder_WithNamespacedName(t *testing.T) {
	nn := types.NamespacedName{Namespace: "default", Name: "my-runner"}
	c := NewCondition("Ready").
		WithNamespacedName(nn).
		Build()

	if c.Message != "default/my-runner" {
		t.Fatalf("expected Message 'default/my-runner', got %q", c.Message)
	}
}

func TestSetRunnerCondition(t *testing.T) {
	runner := &runnersv1alpha1.Runner{}
	setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerPending, "Runner is pending")

	if len(runner.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(runner.Status.Conditions))
	}
	c := runner.Status.Conditions[0]
	if c.Type != ConditionTypeReady {
		t.Fatalf("expected Type %q, got %q", ConditionTypeReady, c.Type)
	}
	if c.Status != metav1.ConditionFalse {
		t.Fatalf("expected Status False, got %q", c.Status)
	}
	if c.Reason != ReasonRunnerPending {
		t.Fatalf("expected Reason %q, got %q", ReasonRunnerPending, c.Reason)
	}
}

func TestSetWorkflowCondition(t *testing.T) {
	wf := &runnersv1alpha1.Workflow{}
	setWorkflowCondition(wf, metav1.ConditionTrue, ReasonWorkflowSucceeded, "All steps completed")

	if len(wf.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(wf.Status.Conditions))
	}
	c := wf.Status.Conditions[0]
	if c.Type != ConditionTypeReady {
		t.Fatalf("expected Type %q, got %q", ConditionTypeReady, c.Type)
	}
	if c.Status != metav1.ConditionTrue {
		t.Fatalf("expected Status True, got %q", c.Status)
	}
	if c.Reason != ReasonWorkflowSucceeded {
		t.Fatalf("expected Reason %q, got %q", ReasonWorkflowSucceeded, c.Reason)
	}
}

func TestSetTriggerCondition(t *testing.T) {
	trigger := &runnersv1alpha1.EventTrigger{}
	setTriggerCondition(trigger, metav1.ConditionTrue, ReasonTriggerRouteRegistered, "Route registered")

	if len(trigger.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(trigger.Status.Conditions))
	}
	c := trigger.Status.Conditions[0]
	if c.Type != ConditionTypeReady {
		t.Fatalf("expected Type %q, got %q", ConditionTypeReady, c.Type)
	}
	if c.Status != metav1.ConditionTrue {
		t.Fatalf("expected Status True, got %q", c.Status)
	}
	if c.Reason != ReasonTriggerRouteRegistered {
		t.Fatalf("expected Reason %q, got %q", ReasonTriggerRouteRegistered, c.Reason)
	}
}

func TestSetStatusCondition_upsert(t *testing.T) {
	wf := &runnersv1alpha1.Workflow{}
	setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowRunning, "Workflow is running")
	setWorkflowCondition(wf, metav1.ConditionTrue, ReasonWorkflowSucceeded, "All steps completed")

	if len(wf.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition after upsert, got %d", len(wf.Status.Conditions))
	}
	c := wf.Status.Conditions[0]
	if c.Reason != ReasonWorkflowSucceeded {
		t.Fatalf("expected Reason %q after upsert, got %q", ReasonWorkflowSucceeded, c.Reason)
	}
	if c.Status != metav1.ConditionTrue {
		t.Fatalf("expected Status True after upsert, got %q", c.Status)
	}
}
