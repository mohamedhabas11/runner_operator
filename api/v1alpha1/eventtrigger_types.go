package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebhookConfig defines the HTTP webhook endpoint for an EventTrigger.
type WebhookConfig struct {
	// Path is the HTTP path for this webhook (e.g. "/webhooks/github-push").
	// Must be unique across all EventTriggers in the cluster.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^/
	Path string `json:"path"`

	// SecretRef references a Secret containing the HMAC secret for payload validation.
	// The secret must have key "hmac-secret". Defaults to the EventTrigger's namespace.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// AllowedIPs restricts webhook traffic to specific CIDR ranges.
	// +optional
	AllowedIPs []string `json:"allowedIPs,omitempty"`
}

// ParameterMapping maps a webhook JSON payload field to a workflow environment variable.
type ParameterMapping struct {
	// Name of the environment variable to set on the created workflow.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Source is a dot-path into the webhook JSON payload (e.g. "$.ref").
	// +required
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`

	// Sanitize strips shell metacharacters from the extracted value.
	// +optional
	Sanitize bool `json:"sanitize,omitempty"`

	// Default value if the source field is missing from the payload.
	// Ignored when Required is true.
	// +optional
	Default string `json:"default,omitempty"`

	// Required marks this parameter as mandatory. When true and the source
	// field is missing from the webhook payload, the EventTrigger sets a
	// Ready=False condition and does not create a Workflow.
	// When false (default), missing fields produce an empty env var and a
	// warning-level log entry, and the Workflow is created.
	// +optional
	Required bool `json:"required,omitempty"`
}

// RateLimitConfig controls how aggressively a trigger may create workflows.
type RateLimitConfig struct {
	// MaxPerMinute limits the number of workflow creations per minute.
	// Zero means unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxPerMinute int `json:"maxPerMinute,omitempty"`

	// MaxConcurrent limits the number of concurrently running workflows.
	// Zero means unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// WorkflowTemplateRef references a Workflow CR to instantiate when the trigger fires.
type WorkflowTemplateRef struct {
	// Name of the Workflow template CR to instantiate.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Workflow template. Defaults to the trigger's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// EventTriggerSpec defines the desired state of EventTrigger.
type EventTriggerSpec struct {
	// Webhook configures the HTTP webhook endpoint for this trigger.
	// +optional
	Webhook *WebhookConfig `json:"webhook,omitempty"`

	// WorkflowTemplate references the Workflow CR to create when triggered.
	// +required
	WorkflowTemplate WorkflowTemplateRef `json:"workflowTemplate"`

	// Parameters maps webhook payload fields to environment variables on the
	// created workflow's first step.  Extracted values are injected as Env vars.
	// +optional
	Parameters []ParameterMapping `json:"parameters,omitempty"`

	// RateLimit controls how often this trigger may create workflows.
	// +optional
	RateLimit *RateLimitConfig `json:"rateLimit,omitempty"`

	// AllowedNamespaces restricts which namespaces workflows may be created in.
	// Defaults to the trigger's namespace when empty.
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
}

// EventTriggerStatus defines the observed state of EventTrigger.
type EventTriggerStatus struct {
	// Conditions represent the current state of the EventTrigger.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastTriggerTime is when this trigger last created a workflow.
	// +optional
	LastTriggerTime *metav1.Time `json:"lastTriggerTime,omitempty"`

	// WorkflowCount is the total number of workflows created by this trigger.
	// +optional
	WorkflowCount int `json:"workflowCount,omitempty"`

	// LastError is the error message from the most recent failed trigger attempt.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Registered indicates whether the webhook route is currently registered.
	// +optional
	// +kubebuilder:default=false
	Registered bool `json:"registered"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=".spec.webhook.path"
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=".spec.workflowTemplate.name"
// +kubebuilder:printcolumn:name="Registered",type=boolean,JSONPath=".status.registered"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// EventTrigger is the Schema for the eventtriggers API.
type EventTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec EventTriggerSpec `json:"spec"`

	// +optional
	Status EventTriggerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// EventTriggerList contains a list of EventTrigger.
type EventTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EventTrigger `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EventTrigger{}, &EventTriggerList{})
}
