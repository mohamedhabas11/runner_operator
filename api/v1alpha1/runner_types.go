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

// GitAuth defines authentication for cloning a Git repository.
type GitAuth struct {
	// SecretRef references a Secret containing Git credentials.
	// For SSH auth: the secret must have key "ssh-privatekey".
	// For HTTPS auth: the secret must have keys "username" and "password".
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// GitRepo defines a Git repository to clone before executing the runner.
type GitRepo struct {
	// URL of the Git repository to clone (HTTPS or SSH).
	// +required
	URL string `json:"url"`

	// Revision is the branch, tag, or commit SHA to checkout.
	// Defaults to the remote HEAD if empty.
	// +optional
	Revision string `json:"revision,omitempty"`

	// Path within the repository to use as the working directory.
	// Example: "terraform/prod" or "ansible/playbooks".
	// +optional
	Path string `json:"path,omitempty"`

	// Auth defines authentication for private repositories.
	// +optional
	Auth *GitAuth `json:"auth,omitempty"`
}

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

	// Resources defines CPU and memory limits/requests for the runner container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// GitRepo defines a Git repository to clone before executing the command.
	// When set, an init container clones the repo into a shared volume and the
	// main container's working directory is set to the checkout path.
	// +optional
	GitRepo *GitRepo `json:"gitRepo,omitempty"`
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
