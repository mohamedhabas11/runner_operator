package controller

import (
	"testing"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeJob(name string, needs ...string) runnersv1alpha1.JobSpec {
	return runnersv1alpha1.JobSpec{
		Name:  name,
		Needs: needs,
		When:  "always",
		Steps: []runnersv1alpha1.WorkflowStep{{Name: name, Image: "busybox"}},
	}
}

func TestDetectJobCycle(t *testing.T) {
	tests := []struct {
		name string
		jobs []runnersv1alpha1.JobSpec
		want string
	}{
		{
			name: "no needs",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a"), makeJob("b")},
			want: "",
		},
		{
			name: "linear needs",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a"), makeJob("b", "a"), makeJob("c", "b")},
			want: "",
		},
		{
			name: "self cycle",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a", "a")},
			want: "cycle detected involving job \"a\"",
		},
		{
			name: "direct cycle",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a", "b"), makeJob("b", "a")},
			want: "cycle detected involving job \"a\"",
		},
		{
			name: "indirect cycle",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a", "b"), makeJob("b", "c"), makeJob("c", "a")},
			want: "cycle detected involving job \"a\"",
		},
		{
			name: "unknown need",
			jobs: []runnersv1alpha1.JobSpec{makeJob("a", "nonexistent")},
			want: "job \"a\" depends on unknown job \"nonexistent\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectJobCycle(tt.jobs)
			if got != tt.want {
				t.Errorf("detectJobCycle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEvaluateJobWhen(t *testing.T) {
	pending := runnersv1alpha1.JobStatus{Phase: runnersv1alpha1.JopPhasePending}
	running := runnersv1alpha1.JobStatus{Phase: runnersv1alpha1.JopPhaseRunning}
	success := runnersv1alpha1.JobStatus{Phase: runnersv1alpha1.JopPhaseSucceeded}
	failed := runnersv1alpha1.JobStatus{Phase: runnersv1alpha1.JopPhaseFailed}

	tests := []struct {
		name string
		job  *runnersv1alpha1.JobSpec
		m    map[string]runnersv1alpha1.JobStatus
		want jobDecision
	}{
		{
			name: "always, no needs",
			job:  &runnersv1alpha1.JobSpec{Name: "a", When: "always"},
			m:    map[string]runnersv1alpha1.JobStatus{},
			want: jobRun,
		},
		{
			name: "on_success, need succeeded",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_success", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": success},
			want: jobRun,
		},
		{
			name: "on_success, need failed",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_success", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": failed},
			want: jobSkip,
		},
		{
			name: "on_success, need pending",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_success", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": pending},
			want: jobWait,
		},
		{
			name: "on_failure, need failed",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_failure", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": failed},
			want: jobRun,
		},
		{
			name: "on_failure, need succeeded",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_failure", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": success},
			want: jobSkip,
		},
		{
			name: "always, already have status",
			job:  &runnersv1alpha1.JobSpec{Name: "a", When: "always"},
			m:    map[string]runnersv1alpha1.JobStatus{"a": running},
			want: jobRun,
		},
		{
			name: "on_success, need running",
			job:  &runnersv1alpha1.JobSpec{Name: "b", When: "on_success", Needs: []string{"a"}},
			m:    map[string]runnersv1alpha1.JobStatus{"a": running},
			want: jobWait,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateJobWhen(tt.job, tt.m)
			if got != tt.want {
				t.Errorf("evaluateJobWhen() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildJobStatusMap(t *testing.T) {
	wf := &runnersv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status: runnersv1alpha1.WorkflowStatus{
			JobStatuses: []runnersv1alpha1.JobStatus{
				{Name: "build", Phase: runnersv1alpha1.JopPhaseSucceeded},
			},
		},
	}

	m := buildJobStatusMap(wf)

	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d", len(m))
	}
	if m["build"].Phase != runnersv1alpha1.JopPhaseSucceeded {
		t.Errorf("build phase = %v, want Succeeded", m["build"].Phase)
	}
	if _, ok := m["test"]; ok {
		t.Error("test should not be in status map")
	}
}

func TestFilterRunnersByJob(t *testing.T) {
	runners := []runnersv1alpha1.Runner{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "runner-a-build-xyz",
				Labels: map[string]string{"runner-operator.io/job": "build"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "runner-a-test-abc",
				Labels: map[string]string{"runner-operator.io/job": "test"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "runner-b-build-123",
				Labels: map[string]string{"runner-operator.io/job": "build"},
			},
		},
	}

	buildRunners := filterRunnersByJob(runners, "build")
	if len(buildRunners) != 2 {
		t.Errorf("expected 2 build runners, got %d", len(buildRunners))
	}

	testRunners := filterRunnersByJob(runners, "test")
	if len(testRunners) != 1 {
		t.Errorf("expected 1 test runner, got %d", len(testRunners))
	}

	unknownRunners := filterRunnersByJob(runners, "deploy")
	if len(unknownRunners) != 0 {
		t.Errorf("expected 0 deploy runners, got %d", len(unknownRunners))
	}
}

func TestFindJobStatus(t *testing.T) {
	statuses := []runnersv1alpha1.JobStatus{
		{Name: "build", Phase: runnersv1alpha1.JopPhaseSucceeded},
		{Name: "test", Phase: runnersv1alpha1.JopPhaseFailed},
	}

	if s, ok := findJobStatus(statuses, "build"); !ok {
		t.Error("expected to find build")
	} else if s.Phase != runnersv1alpha1.JopPhaseSucceeded {
		t.Errorf("build phase = %v", s.Phase)
	}

	if _, ok := findJobStatus(statuses, "deploy"); ok {
		t.Error("expected not to find deploy")
	}

	if s, ok := findJobStatus(statuses, "test"); !ok {
		t.Error("expected to find test")
	} else if s.Phase != runnersv1alpha1.JopPhaseFailed {
		t.Errorf("test phase = %v", s.Phase)
	}
}

func TestUpsertJobStatus(t *testing.T) {
	wf := &runnersv1alpha1.Workflow{
		Status: runnersv1alpha1.WorkflowStatus{
			JobStatuses: []runnersv1alpha1.JobStatus{
				{Name: "build", Phase: runnersv1alpha1.JopPhasePending},
			},
		},
	}

	upsertJobStatus(wf, "build", runnersv1alpha1.JopPhaseRunning)
	if len(wf.Status.JobStatuses) != 1 {
		t.Errorf("expected 1 entry, got %d", len(wf.Status.JobStatuses))
	}
	if wf.Status.JobStatuses[0].Phase != runnersv1alpha1.JopPhaseRunning {
		t.Errorf("build phase = %v, want Running", wf.Status.JobStatuses[0].Phase)
	}

	upsertJobStatus(wf, "test", runnersv1alpha1.JopPhasePending)
	if len(wf.Status.JobStatuses) != 2 {
		t.Errorf("expected 2 entries, got %d", len(wf.Status.JobStatuses))
	}

	wf2 := &runnersv1alpha1.Workflow{}
	upsertJobStatus(wf2, "a", runnersv1alpha1.JopPhasePending)
	if len(wf2.Status.JobStatuses) != 1 {
		t.Errorf("expected 1 entry, got %d", len(wf2.Status.JobStatuses))
	}
}

func TestComputeJobWorkflowPhase(t *testing.T) {
	// All succeeded
	wf1 := &runnersv1alpha1.Workflow{
		Spec: runnersv1alpha1.WorkflowSpec{
			Jobs: []runnersv1alpha1.JobSpec{{Name: "a"}, {Name: "b"}},
		},
	}
	upsertJobStatus(wf1, "a", runnersv1alpha1.JopPhaseSucceeded)
	upsertJobStatus(wf1, "b", runnersv1alpha1.JopPhaseSucceeded)

	if got := computeJobWorkflowPhase(wf1); got != runnersv1alpha1.WorkflowPhaseSucceeded {
		t.Errorf("all succeeded: got %v, want Succeeded", got)
	}

	// One failed
	wf2 := &runnersv1alpha1.Workflow{
		Spec: runnersv1alpha1.WorkflowSpec{
			Jobs: []runnersv1alpha1.JobSpec{{Name: "a"}, {Name: "b"}},
		},
	}
	upsertJobStatus(wf2, "a", runnersv1alpha1.JopPhaseSucceeded)
	upsertJobStatus(wf2, "b", runnersv1alpha1.JopPhaseFailed)

	if got := computeJobWorkflowPhase(wf2); got != runnersv1alpha1.WorkflowPhaseFailed {
		t.Errorf("one failed: got %v, want Failed", got)
	}

	// All skipped
	wf3 := &runnersv1alpha1.Workflow{
		Spec: runnersv1alpha1.WorkflowSpec{
			Jobs: []runnersv1alpha1.JobSpec{{Name: "a"}, {Name: "b"}},
		},
	}
	upsertJobStatus(wf3, "a", runnersv1alpha1.JopPhaseSkipped)
	upsertJobStatus(wf3, "b", runnersv1alpha1.JopPhaseSkipped)

	if got := computeJobWorkflowPhase(wf3); got != runnersv1alpha1.WorkflowPhaseSucceeded {
		t.Errorf("all skipped: got %v, want Succeeded", got)
	}

	// Running
	wf4 := &runnersv1alpha1.Workflow{
		Spec: runnersv1alpha1.WorkflowSpec{
			Jobs: []runnersv1alpha1.JobSpec{{Name: "a"}, {Name: "b"}},
		},
	}
	upsertJobStatus(wf4, "a", runnersv1alpha1.JopPhaseRunning)
	upsertJobStatus(wf4, "b", runnersv1alpha1.JopPhasePending)

	if got := computeJobWorkflowPhase(wf4); got != runnersv1alpha1.WorkflowPhaseRunning {
		t.Errorf("running: got %v, want Running", got)
	}

	// No statuses
	wf5 := &runnersv1alpha1.Workflow{}
	if got := computeJobWorkflowPhase(wf5); got != runnersv1alpha1.WorkflowPhasePending {
		t.Errorf("no statuses: got %v, want Pending", got)
	}
}
