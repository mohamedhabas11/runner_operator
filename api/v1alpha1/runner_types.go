package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerPhase is a label for the current lifecycle phase of a Runner.
type RunnerPhase string

const (
	RunnerPhasePending   RunnerPhase = "Pending"
	RunnerPhaseRunning   RunnerPhase = "Running"
	RunnerPhaseSucceeded RunnerPhase = "Succeeded"
	RunnerPhaseFailed    RunnerPhase = "Failed"
	RunnerPhaseUnknown   RunnerPhase = "Unknown"
)

// RunnerSpec defines the desired state of Runner.
type RunnerSpec struct {
	// Image is the Docker image to run.
	// +required
	Image string `json:"image"`

	// Env defines environment variables for the runner container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// EnvFrom references Secrets or ConfigMaps whose data is loaded as env vars.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// Volumes defines volumes available to the runner.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// Mounts defines volume mounts for the runner container.
	// +optional
	Mounts []corev1.VolumeMount `json:"mounts,omitempty"`

	// Args passed to the container entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// TimeoutAfter defines the maximum duration before the runner is terminated.
	// Must be a valid Go duration string (e.g. "30m", "1h").
	// +optional
	TimeoutAfter *metav1.Duration `json:"timeoutAfter,omitempty"`
}

// RunnerStatus defines the observed state of Runner.
type RunnerStatus struct {
	// Conditions represent the current state of the Runner resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is the current lifecycle phase.
	// +optional
	Phase RunnerPhase `json:"phase,omitempty"`

	// ObservedGeneration is the last generation the controller reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ResourceHash is a hash of the spec for drift detection.
	// +optional
	ResourceHash string `json:"resourceHash,omitempty"`

	// StartTime is when the runner started execution.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the runner finished execution.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Runner is the Schema for the runners API.
type Runner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec RunnerSpec `json:"spec"`

	// +optional
	Status RunnerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RunnerList contains a list of Runner.
type RunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Runner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Runner{}, &RunnerList{})
}
