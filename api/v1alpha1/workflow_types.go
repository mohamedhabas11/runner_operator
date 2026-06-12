package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkflowPhase is a label for the current lifecycle phase of a Workflow.
type WorkflowPhase string

const (
	WorkflowPhasePending   WorkflowPhase = "Pending"
	WorkflowPhaseRunning   WorkflowPhase = "Running"
	WorkflowPhaseSucceeded WorkflowPhase = "Succeeded"
	WorkflowPhaseFailed    WorkflowPhase = "Failed"
	WorkflowPhaseUnknown   WorkflowPhase = "Unknown"
)

// StepPhase is a label for the current lifecycle phase of a Workflow step.
type StepPhase string

const (
	StepPhasePending   StepPhase = "Pending"
	StepPhaseRunning   StepPhase = "Running"
	StepPhaseSucceeded StepPhase = "Succeeded"
	StepPhaseFailed    StepPhase = "Failed"
	StepPhaseSkipped   StepPhase = "Skipped"
	StepPhaseWaiting   StepPhase = "Waiting"
)

// RetryPolicy defines retry behaviour for a workflow step.
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts.
	// +kubebuilder:validation:Minimum=0
	// +required
	MaxRetries int `json:"maxRetries"`

	// Backoff defines the retry backoff strategy.
	// +optional
	Backoff *BackoffConfig `json:"backoff,omitempty"`
}

// BackoffConfig defines backoff parameters for retries.
type BackoffConfig struct {
	// InitialDelay between retry attempts.
	// +required
	InitialDelay metav1.Duration `json:"initialDelay"`

	// MaxDelay caps the delay between retry attempts.
	// +optional
	MaxDelay *metav1.Duration `json:"maxDelay,omitempty"`
}

// WorkflowStep defines a single step in a Workflow.
type WorkflowStep struct {
	// Name of the step; must be unique within the workflow.
	// +required
	Name string `json:"name"`

	// RunnerRef references a Runner resource to use as a template.
	// +optional
	RunnerRef *corev1.LocalObjectReference `json:"runnerRef,omitempty"`

	// Image is the Docker image to run (inline alternative to RunnerRef).
	// +optional
	Image string `json:"image,omitempty"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args passed to the container entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env defines environment variables for this step.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Retry configures retry behaviour for this step.
	// +optional
	Retry *RetryPolicy `json:"retry,omitempty"`

	// DependsOn lists step names that must complete before this one runs.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// When is a condition expression that controls whether this step runs.
	// Evaluated after all dependencies complete. Accepted values: "on_success" (default), "on_failure", "always".
	// +optional
	When string `json:"when,omitempty"`

	// Timeout for this step.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// WorkflowSpec defines the desired state of Workflow.
type WorkflowSpec struct {
	// Steps defines the ordered steps of the workflow.
	// +listType=map
	// +listMapKey=name
	// +required
	Steps []WorkflowStep `json:"steps"`

	// Timeout for the entire workflow execution.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// StepStatus tracks the execution status of an individual workflow step.
type StepStatus struct {
	// Name of the step.
	Name string `json:"name"`

	// Phase of the step execution.
	Phase StepPhase `json:"phase"`

	// RetryCount is how many times the step has been retried.
	// +optional
	RetryCount int `json:"retryCount,omitempty"`

	// StartedAt is when the step started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the step finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Message provides details about the step status.
	// +optional
	Message string `json:"message,omitempty"`
}

// WorkflowStatus defines the observed state of Workflow.
type WorkflowStatus struct {
	// Conditions represent the current state of the Workflow resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is the current lifecycle phase of the workflow.
	// +optional
	Phase WorkflowPhase `json:"phase,omitempty"`

	// StepStatuses tracks the status of each workflow step.
	// +optional
	StepStatuses []StepStatus `json:"stepStatuses,omitempty"`

	// StartTime is when the workflow started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the workflow finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// ObservedGeneration is the last generation the controller reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Workflow is the Schema for the workflows API.
type Workflow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec WorkflowSpec `json:"spec"`

	// +optional
	Status WorkflowStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkflowList contains a list of Workflow.
type WorkflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Workflow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workflow{}, &WorkflowList{})
}
