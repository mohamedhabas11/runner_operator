package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ConditionBuilder is a builder for metav1.Condition.
// WHY: Every controller needs to set status conditions with consistent field
// population. This builder ensures no field is accidentally omitted (e.g.,
// Reason or Message left empty), centralizes the zero-timestamp edge case,
// and makes call sites read as declarative specs rather than struct literals.
type ConditionBuilder struct {
	condition metav1.Condition
}

// NewCondition starts a condition builder for the given type.
// The condition is initialised with Unknown status, an empty reason,
// and the zero-time sentinel that controller-runtime's meta.SetStatusCondition
// uses to detect "first observation".
func NewCondition(condType string) *ConditionBuilder {
	return &ConditionBuilder{
		condition: metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: 0,
			LastTransitionTime: metav1.Now(),
			Reason:             "",
			Message:            "",
		},
	}
}

// WithStatus sets the condition status (True / False / Unknown).
func (b *ConditionBuilder) WithStatus(status metav1.ConditionStatus) *ConditionBuilder {
	b.condition.Status = status
	return b
}

// WithReason sets a short CamelCase reason code (e.g. "JobCreated", "ReconciliationFailed").
func (b *ConditionBuilder) WithReason(reason string) *ConditionBuilder {
	b.condition.Reason = reason
	return b
}

// WithMessage sets a human-readable description of the current state.
func (b *ConditionBuilder) WithMessage(msg string) *ConditionBuilder {
	b.condition.Message = msg
	return b
}

// WithObservedGeneration records the resource generation that produced this condition.
func (b *ConditionBuilder) WithObservedGeneration(gen int64) *ConditionBuilder {
	b.condition.ObservedGeneration = gen
	return b
}

// WithNamespacedName records the resource name in the message for traceability.
func (b *ConditionBuilder) WithNamespacedName(nn types.NamespacedName) *ConditionBuilder {
	b.condition.Message = nn.String()
	return b
}

// Build returns the constructed Condition.
func (b *ConditionBuilder) Build() metav1.Condition {
	return b.condition
}

// ──────────────────────────────────────────────────────────────────────────────
// Predefined condition reasons — one per distinct transition in the state machine.
// These serve as the single source of truth for reason codes so that controllers
// don't scatter magic strings.
// ──────────────────────────────────────────────────────────────────────────────

const (
	// ConditionTypeReady is the standard Ready condition type used by all controllers.
	ConditionTypeReady = "Ready"

	// Runner reasons
	ReasonRunnerPending           = "Pending"
	ReasonRunnerJobCreated        = "JobCreated"
	ReasonRunnerRunning           = "Running"
	ReasonRunnerSucceeded         = "JobSucceeded"
	ReasonRunnerFailed            = "JobFailed"
	ReasonRunnerSpecDriftDeferred = "SpecDriftDeferred"
	ReasonRunnerSpecDriftReplaced = "SpecDriftReplaced"
	ReasonRunnerReconcileError    = "ReconciliationError"
	ReasonRunnerValidationFailed  = "ValidationFailed"

	// Workflow reasons
	ReasonWorkflowPending        = "Pending"
	ReasonWorkflowRunning        = "Running"
	ReasonWorkflowSucceeded      = "AllStepsSucceeded"
	ReasonWorkflowFailed         = "WorkflowFailed"
	ReasonWorkflowTimedOut       = "TimedOut"
	ReasonWorkflowCycleDetected  = "CycleDetected"
	ReasonWorkflowNoSteps        = "NoStepsDefined"
	ReasonWorkflowReconcileError = "ReconciliationError"

	// EventTrigger reasons
	ReasonTriggerRouteRegistered  = "RouteRegistered"
	ReasonTriggerRouteFailed      = "RouteRegistrationFailed"
	ReasonTriggerNamespaceBlocked = "NamespaceNotAllowed"
	ReasonTriggerPathCollision    = "PathCollision"
	ReasonTriggerReconcileError   = "ReconciliationError"

	// Namespace quota annotation
	MaxWorkflowsAnnotation = "runner-operator.io/max-concurrent-workflows"
)
