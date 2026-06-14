package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ──────────────────────────────────────────────────────────────────────────────
// Custom Prometheus metrics for the runner-operator.
// WHY: Default controller-runtime metrics track reconciliation counts and
// durations, but not business-level outcomes (job completions, workflow phase
// transitions). These custom metrics enable SLO monitoring and cost attribution
// per namespace.
//
// CARDINALITY WARNING: Labels on all metrics below must stay bounded.
//   namespace      — O(N) across clusters, OK.
//   phase          — fixed set {succeeded,failed,unknown,...}, OK.
//   error_type     — fixed set {api_server,validation}, OK.
// NEVER add runner name, pod ID, image digest, or spec hash as labels.
// ──────────────────────────────────────────────────────────────────────────────

var (
	// RunnerJobCompletedTotal counts completed Runner jobs by phase.
	// Phase values: succeeded, failed, unknown.
	RunnerJobCompletedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "runner_job_completed_total",
			Help: "Total number of completed Runner jobs by phase and namespace",
		},
		[]string{"namespace", "phase"},
	)

	// RunnerDurationSeconds tracks individual Runner execution duration
	// from start time to completion (terminal phase).
	RunnerDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "runner_duration_seconds",
			Help:    "Duration of Runner execution from start to completion",
			Buckets: []float64{1, 5, 10, 30, 60, 300, 900, 3600},
		},
		[]string{"namespace"},
	)

	// WorkflowPhaseTransitions counts workflow phase transitions.
	WorkflowPhaseTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "workflow_phase_transitions_total",
			Help: "Total number of workflow phase transitions by phase and namespace",
		},
		[]string{"namespace", "phase"},
	)

	// WorkflowDurationSeconds tracks workflow execution duration from start to completion.
	WorkflowDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "workflow_duration_seconds",
			Help:    "Duration of workflow execution in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace"},
	)

	// StepRetriesTotal counts step retries across all workflows.
	StepRetriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "step_retries_total",
			Help: "Total number of step retries by namespace",
		},
		[]string{"namespace"},
	)
)

func init() {
	// Register all custom metrics with the controller-runtime global registry.
	// The metrics endpoint is served at the address configured via --metrics-bind-address.
	metrics.Registry.MustRegister(
		RunnerJobCompletedTotal,
		RunnerDurationSeconds,
		WorkflowPhaseTransitions,
		WorkflowDurationSeconds,
		StepRetriesTotal,
	)
}
