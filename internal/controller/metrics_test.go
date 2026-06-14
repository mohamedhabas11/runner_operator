package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetrics_initialized(t *testing.T) {
	if RunnerJobCompletedTotal == nil {
		t.Fatal("RunnerJobCompletedTotal must not be nil after init")
	}
	if RunnerDurationSeconds == nil {
		t.Fatal("RunnerDurationSeconds must not be nil after init")
	}
	if WorkflowPhaseTransitions == nil {
		t.Fatal("WorkflowPhaseTransitions must not be nil after init")
	}
	if WorkflowDurationSeconds == nil {
		t.Fatal("WorkflowDurationSeconds must not be nil after init")
	}
	if StepRetriesTotal == nil {
		t.Fatal("StepRetriesTotal must not be nil after init")
	}
}

func TestMetrics_canIncrement(t *testing.T) {
	RunnerJobCompletedTotal.WithLabelValues("ns1", "succeeded").Inc()
	RunnerJobCompletedTotal.WithLabelValues("ns1", "failed").Inc()
	WorkflowPhaseTransitions.WithLabelValues("ns1", "Running").Inc()
	StepRetriesTotal.WithLabelValues("ns1").Inc()
	WorkflowDurationSeconds.WithLabelValues("ns1").Observe(2.5)

	var m dto.Metric
	if err := RunnerJobCompletedTotal.WithLabelValues("ns1", "succeeded").Write(&m); err != nil {
		t.Fatalf("Write metric: %v", err)
	}
	if m.Counter.GetValue() != 1 {
		t.Errorf("expected counter 1, got %f", m.Counter.GetValue())
	}

	// Clean up
	RunnerJobCompletedTotal.Reset()
	RunnerDurationSeconds.Reset()
	WorkflowPhaseTransitions.Reset()
	StepRetriesTotal.Reset()
	WorkflowDurationSeconds.Reset()
}

func TestMetrics_describe(t *testing.T) {
	// Verify each metric's name through its Desc
	tests := []struct {
		collector prometheus.Collector
		name      string
		help      string
	}{
		{RunnerJobCompletedTotal, "runner_job_completed_total", "completed Runner jobs"},
		{RunnerDurationSeconds, "runner_duration_seconds", "Runner execution"},
		{WorkflowPhaseTransitions, "workflow_phase_transitions_total", "workflow phase transitions"},
		{WorkflowDurationSeconds, "workflow_duration_seconds", "workflow execution"},
		{StepRetriesTotal, "step_retries_total", "step retries"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan *prometheus.Desc, 1)
			tt.collector.Describe(ch)
			close(ch)
			desc := <-ch
			if desc == nil {
				t.Fatal("expected non-nil Desc")
			}
			s := desc.String()
			if !strings.Contains(s, tt.name) {
				t.Errorf("Desc should contain %q, got: %s", tt.name, s)
			}
			if !strings.Contains(s, tt.help) {
				t.Errorf("Desc should contain help %q, got: %s", tt.help, s)
			}
		})
	}
}

func TestMetrics_quotaAnnotation(t *testing.T) {
	if MaxWorkflowsAnnotation != "runner-operator.io/max-concurrent-workflows" {
		t.Errorf("expected annotation key, got %q", MaxWorkflowsAnnotation)
	}
}

func TestMetrics_labelCardinality(t *testing.T) {
	// CounterVec with 2 labels
	RunnerJobCompletedTotal.WithLabelValues("ns", "succeeded").Inc()
	RunnerJobCompletedTotal.WithLabelValues("ns", "failed").Inc()
	RunnerJobCompletedTotal.WithLabelValues("other", "succeeded").Inc()

	var m dto.Metric
	if err := RunnerJobCompletedTotal.WithLabelValues("ns", "succeeded").Write(&m); err != nil {
		t.Fatalf("Write metric: %v", err)
	}
	if len(m.Label) != 2 {
		t.Errorf("expected 2 labels, got %d", len(m.Label))
	}
	RunnerJobCompletedTotal.Reset()
}
