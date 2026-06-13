package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

// WorkflowReconciler reconciles a Workflow object.
type WorkflowReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=workflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=workflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *WorkflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	wf := &runnersv1alpha1.Workflow{}
	if err := r.Get(ctx, req.NamespacedName, wf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !wf.DeletionTimestamp.IsZero() {
		logger.Info("Workflow is being deleted, relying on OwnerReferences for cleanup")
		return ctrl.Result{}, nil
	}

	// Dispatch: job-based or flat-step workflow
	if len(wf.Spec.Jobs) > 0 {
		return r.reconcileJobWorkflow(ctx, wf)
	}
	return r.reconcileFlatWorkflow(ctx, wf)
}

func (r *WorkflowReconciler) reconcileFlatWorkflow(ctx context.Context, wf *runnersv1alpha1.Workflow) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if len(wf.Spec.Steps) == 0 {
		r.Recorder.Event(wf, corev1.EventTypeWarning, "NoSteps", "Workflow has no steps defined")
		return ctrl.Result{}, nil
	}

	if cycle := detectCycle(wf.Spec.Steps); cycle != "" {
		logger.Info("Cycle detected in workflow steps", "cycle", cycle)
		r.Recorder.Eventf(wf, corev1.EventTypeWarning, "CycleDetected", "Dependency cycle detected: %s", cycle)
		return ctrl.Result{}, nil
	}

	updated := false

	if wf.Spec.Timeout != nil && !isWorkflowTerminal(wf.Status.Phase) && wf.Status.StartTime != nil {
		elapsed := time.Since(wf.Status.StartTime.Time)
		if elapsed > wf.Spec.Timeout.Duration {
			return r.handleTimeoutFlat(ctx, wf, elapsed)
		}
	}

	stepRunners, err := r.listStepRunners(ctx, wf)
	if err != nil {
		return ctrl.Result{}, err
	}

	patchBase := client.MergeFrom(wf.DeepCopy())

	if wf.Status.StartTime == nil {
		now := metav1.Now()
		wf.Status.StartTime = &now
		updated = true
	}

	updated = r.reconcileSteps(ctx, wf, stepRunners) || updated

	newPhase := computeFlatWorkflowPhase(wf)
	if newPhase != wf.Status.Phase {
		r.Recorder.Eventf(wf, corev1.EventTypeNormal, "PhaseChanged", "Workflow phase changed to %s", newPhase)
		wf.Status.Phase = newPhase
		updated = true
	}
	if isWorkflowTerminal(wf.Status.Phase) && wf.Status.CompletionTime == nil {
		now := metav1.Now()
		wf.Status.CompletionTime = &now
		updated = true
	}

	if updated {
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !isWorkflowTerminal(wf.Status.Phase) && wf.Spec.Timeout != nil && wf.Status.StartTime != nil {
		elapsed := time.Since(wf.Status.StartTime.Time)
		remaining := wf.Spec.Timeout.Duration - elapsed
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) listStepRunners(ctx context.Context, wf *runnersv1alpha1.Workflow) ([]runnersv1alpha1.Runner, error) {
	runnerList := &runnersv1alpha1.RunnerList{}
	if err := r.List(ctx, runnerList, client.InNamespace(wf.Namespace), client.MatchingLabels{
		"runner-operator.io/workflow": wf.Name,
	}); err != nil {
		return nil, err
	}
	return runnerList.Items, nil
}

func (r *WorkflowReconciler) handleTimeoutFlat(ctx context.Context, wf *runnersv1alpha1.Workflow, elapsed time.Duration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Workflow timed out", "timeout", wf.Spec.Timeout.Duration, "elapsed", elapsed)
	r.Recorder.Event(wf, corev1.EventTypeWarning, "TimedOut", "Workflow timed out")

	patchBase := client.MergeFrom(wf.DeepCopy())

	for _, step := range wf.Spec.Steps {
		upsertStepStatus(wf, step.Name, runnersv1alpha1.StepPhaseFailed)
	}

	wf.Status.Phase = runnersv1alpha1.WorkflowPhaseFailed
	now := metav1.Now()
	wf.Status.CompletionTime = &now
	wf.Status.ObservedGeneration = wf.Generation

	if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) handleTimeoutJobs(ctx context.Context, wf *runnersv1alpha1.Workflow, elapsed time.Duration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Workflow timed out", "timeout", wf.Spec.Timeout.Duration, "elapsed", elapsed)
	r.Recorder.Event(wf, corev1.EventTypeWarning, "TimedOut", "Workflow timed out")

	patchBase := client.MergeFrom(wf.DeepCopy())

	for _, job := range wf.Spec.Jobs {
		upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseFailed)
		for _, step := range job.Steps {
			upsertStepStatus(wf, step.Name, runnersv1alpha1.StepPhaseFailed)
		}
	}

	wf.Status.Phase = runnersv1alpha1.WorkflowPhaseFailed
	now := metav1.Now()
	wf.Status.CompletionTime = &now
	wf.Status.ObservedGeneration = wf.Generation

	if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) reconcileSteps(ctx context.Context, wf *runnersv1alpha1.Workflow, stepRunners []runnersv1alpha1.Runner) bool {
	logger := log.FromContext(ctx)
	updated := false

	stepStatusMap := buildStepStatusMap(wf)
	runnerMap := buildRunnerMap(stepRunners)

	for _, step := range wf.Spec.Steps {
		existing, hasRunner := runnerMap[step.Name]
		status, hasStatus := stepStatusMap[step.Name]

		if hasStatus {
			switch status.Phase {
			case runnersv1alpha1.StepPhaseSucceeded, runnersv1alpha1.StepPhaseFailed, runnersv1alpha1.StepPhaseSkipped:
				if hasRunner {
					continue
				}
			}
		}

		decision := evaluateStep(step, stepStatusMap)

		switch decision {
		case stepSkip:
			if !hasStatus {
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:    step.Name,
					Phase:   runnersv1alpha1.StepPhaseSkipped,
					Message: fmt.Sprintf("Dependencies did not meet the required condition (when: %q)", step.When),
				})
				updated = true
			}
			continue

		case stepWait:
			if !hasStatus {
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:  step.Name,
					Phase: runnersv1alpha1.StepPhaseWaiting,
				})
				updated = true
			}
			continue

		case stepRun:
			if !hasRunner {
				runner := r.buildStepRunner(ctx, wf, &step)
				if err := controllerutil.SetControllerReference(wf, runner, r.Scheme); err != nil {
					logger.Error(err, "Failed to set owner reference", "step", step.Name)
					continue
				}
				logger.Info("Creating Runner for step", "step", step.Name)
				r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRunnerCreated", "Created Runner for step %q", step.Name)
				if err := r.Create(ctx, runner); err != nil {
					logger.Error(err, "Failed to create Runner", "step", step.Name)
					continue
				}
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:    step.Name,
					Phase:   runnersv1alpha1.StepPhasePending,
					Message: "Runner created",
				})
				updated = true
			} else {
				stepPhase := runnerPhaseToStepPhase(existing.Status.Phase)
				if stepPhase == runnersv1alpha1.StepPhaseFailed &&
					step.Retry != nil && step.Retry.MaxRetries > 0 &&
					status.RetryCount < step.Retry.MaxRetries {
					if !retryBackoffElapsed(&step, &status) {
						continue
					}
					logger.Info("Step failed, retrying", "step", step.Name, "attempt", status.RetryCount+1)
					r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRetrying", "Step %q failed, retrying (attempt %d/%d)", step.Name, status.RetryCount+1, step.Retry.MaxRetries)
					if err := r.Delete(ctx, &existing); err != nil {
						logger.Error(err, "Failed to delete failed Runner for retry", "step", step.Name)
					}
					stepPhase = runnersv1alpha1.StepPhasePending
					upsertStepStatus(wf, step.Name, stepPhase)
					for i, s := range wf.Status.StepStatuses {
						if s.Name == step.Name {
							wf.Status.StepStatuses[i].RetryCount++
							break
						}
					}
					updated = true
				} else if !hasStatus || status.Phase != stepPhase {
					upsertStepStatus(wf, step.Name, stepPhase)
					updated = true
				}
			}
		}
	}

	return updated
}

type stepDecision int

const (
	defaultWhen = "on_success"

	stepSkip stepDecision = iota
	stepWait
	stepRun
)

func evaluateStep(step runnersv1alpha1.WorkflowStep, stepStatusMap map[string]runnersv1alpha1.StepStatus) stepDecision {
	for _, dep := range step.DependsOn {
		status, ok := stepStatusMap[dep]
		if !ok || status.Phase == runnersv1alpha1.StepPhasePending || status.Phase == runnersv1alpha1.StepPhaseRunning || status.Phase == runnersv1alpha1.StepPhaseWaiting {
			return stepWait
		}
	}

	when := strings.TrimSpace(step.When)
	if when == "" {
		when = defaultWhen
	}

	allDepSucceeded := true
	for _, dep := range step.DependsOn {
		status, ok := stepStatusMap[dep]
		if !ok || status.Phase != runnersv1alpha1.StepPhaseSucceeded {
			allDepSucceeded = false
			break
		}
	}

	switch when {
	case "always":
		return stepRun
	case "on_failure":
		if !allDepSucceeded {
			return stepRun
		}
		return stepSkip
	case defaultWhen:
		fallthrough
	default:
		if allDepSucceeded {
			return stepRun
		}
		return stepSkip
	}
}

func sanitizeStepName(name string) string {
	sanitized := strings.ToLower(name)
	sanitized = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, sanitized)
	sanitized = strings.Trim(sanitized, "-")
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "step"
	}
	return sanitized
}

func (r *WorkflowReconciler) buildStepRunner(ctx context.Context, wf *runnersv1alpha1.Workflow, step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner {
	runnerName := fmt.Sprintf("%s-%s", wf.Name, sanitizeStepName(step.Name))

	spec := runnersv1alpha1.RunnerSpec{
		Command: step.Command,
		Args:    step.Args,
		Env:     step.Env,
	}

	if step.Image != "" {
		spec.Image = step.Image
	} else if step.RunnerRef != nil {
		template := &runnersv1alpha1.Runner{}
		var err error
		if err = r.Get(ctx, types.NamespacedName{Name: step.RunnerRef.Name, Namespace: wf.Namespace}, template); err == nil {
			spec = *template.Spec.DeepCopy()
			if step.Command != nil {
				spec.Command = step.Command
			}
			if step.Args != nil {
				spec.Args = step.Args
			}
			if step.Env != nil {
				spec.Env = step.Env
			}
			if step.Timeout != nil {
				spec.TimeoutAfter = step.Timeout
			}
			if step.GitRepo != nil {
				spec.GitRepo = step.GitRepo
			}
		} else {
			stepRef := fmt.Sprintf("%s/%s", wf.Namespace, step.RunnerRef.Name)
			log.FromContext(ctx).Error(err, "RunnerRef not found, using default image", "runnerRef", stepRef)
			spec.Image = "busybox:latest"
		}
	} else {
		spec.Image = "busybox:latest"
	}

	if step.GitRepo != nil {
		spec.GitRepo = step.GitRepo
	}

	return &runnersv1alpha1.Runner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runnerName,
			Namespace: wf.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "runner-operator",
				"runner-operator.io/workflow": wf.Name,
				"runner-operator.io/step":     step.Name,
			},
		},
		Spec: spec,
	}
}

func detectCycle(steps []runnersv1alpha1.WorkflowStep) string {
	stepNames := map[string]bool{}
	for _, s := range steps {
		stepNames[s.Name] = true
	}

	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if !stepNames[dep] {
				return fmt.Sprintf("step %q depends on unknown step %q", s.Name, dep)
			}
		}
	}

	visited := map[string]int{}
	var dfs func(name string) bool
	dfs = func(name string) bool {
		if visited[name] == 1 {
			return true
		}
		if visited[name] == 2 {
			return false
		}
		visited[name] = 1
		for _, s := range steps {
			if s.Name == name {
				if slices.ContainsFunc(s.DependsOn, dfs) {
					return true
				}
				break
			}
		}
		visited[name] = 2
		return false
	}

	for _, s := range steps {
		if dfs(s.Name) {
			return fmt.Sprintf("cycle detected involving step %q", s.Name)
		}
	}
	return ""
}

func buildStepStatusMap(wf *runnersv1alpha1.Workflow) map[string]runnersv1alpha1.StepStatus {
	m := map[string]runnersv1alpha1.StepStatus{}
	for _, s := range wf.Status.StepStatuses {
		m[s.Name] = s
	}
	return m
}

func buildRunnerMap(runners []runnersv1alpha1.Runner) map[string]runnersv1alpha1.Runner {
	m := map[string]runnersv1alpha1.Runner{}
	for _, r := range runners {
		if stepName, ok := r.Labels["runner-operator.io/step"]; ok {
			m[stepName] = r
		}
	}
	return m
}

func runnerPhaseToStepPhase(rp runnersv1alpha1.RunnerPhase) runnersv1alpha1.StepPhase {
	switch rp {
	case runnersv1alpha1.RunnerPhasePending:
		return runnersv1alpha1.StepPhasePending
	case runnersv1alpha1.RunnerPhaseRunning:
		return runnersv1alpha1.StepPhaseRunning
	case runnersv1alpha1.RunnerPhaseSucceeded:
		return runnersv1alpha1.StepPhaseSucceeded
	case runnersv1alpha1.RunnerPhaseFailed:
		return runnersv1alpha1.StepPhaseFailed
	default:
		return runnersv1alpha1.StepPhasePending
	}
}

func upsertStepStatus(wf *runnersv1alpha1.Workflow, stepName string, phase runnersv1alpha1.StepPhase) {
	for i, s := range wf.Status.StepStatuses {
		if s.Name == stepName {
			if s.Phase != phase {
				wf.Status.StepStatuses[i].Phase = phase
				if phase == runnersv1alpha1.StepPhaseRunning {
					if s.StartedAt == nil {
						now := metav1.Now()
						wf.Status.StepStatuses[i].StartedAt = &now
					}
					wf.Status.StepStatuses[i].CompletedAt = nil
				}
				if phase == runnersv1alpha1.StepPhaseSucceeded || phase == runnersv1alpha1.StepPhaseFailed {
					now := metav1.Now()
					wf.Status.StepStatuses[i].CompletedAt = &now
				}
			}
			return
		}
	}
	status := runnersv1alpha1.StepStatus{Name: stepName, Phase: phase}
	if phase == runnersv1alpha1.StepPhaseRunning {
		now := metav1.Now()
		status.StartedAt = &now
	}
	wf.Status.StepStatuses = append(wf.Status.StepStatuses, status)
}

// ─── Job-based reconciler ────────────────────────────────────────────────

func (r *WorkflowReconciler) reconcileJobWorkflow(ctx context.Context, wf *runnersv1alpha1.Workflow) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if len(wf.Spec.Jobs) == 0 {
		r.Recorder.Event(wf, corev1.EventTypeWarning, "NoJobs", "Workflow has no jobs defined")
		return ctrl.Result{}, nil
	}

	if cycle := detectJobCycle(wf.Spec.Jobs); cycle != "" {
		logger.Info("Cycle detected in workflow jobs", "cycle", cycle)
		r.Recorder.Eventf(wf, corev1.EventTypeWarning, "CycleDetected", "Job dependency cycle detected: %s", cycle)
		return ctrl.Result{}, nil
	}

	updated := false

	if wf.Spec.Timeout != nil && !isWorkflowTerminal(wf.Status.Phase) && wf.Status.StartTime != nil {
		elapsed := time.Since(wf.Status.StartTime.Time)
		if elapsed > wf.Spec.Timeout.Duration {
			return r.handleTimeoutJobs(ctx, wf, elapsed)
		}
	}

	allRunners, err := r.listStepRunners(ctx, wf)
	if err != nil {
		return ctrl.Result{}, err
	}

	patchBase := client.MergeFrom(wf.DeepCopy())

	if wf.Status.StartTime == nil {
		now := metav1.Now()
		wf.Status.StartTime = &now
		updated = true
	}

	jobStatusMap := buildJobStatusMap(wf)

	for i := range wf.Spec.Jobs {
		jobUpdated := r.reconcileJob(ctx, wf, &wf.Spec.Jobs[i], allRunners, jobStatusMap)
		updated = updated || jobUpdated
	}

	newPhase := computeJobWorkflowPhase(wf)
	if newPhase != wf.Status.Phase {
		r.Recorder.Eventf(wf, corev1.EventTypeNormal, "PhaseChanged", "Workflow phase changed to %s", newPhase)
		wf.Status.Phase = newPhase
		updated = true
	}
	if isWorkflowTerminal(wf.Status.Phase) && wf.Status.CompletionTime == nil {
		now := metav1.Now()
		wf.Status.CompletionTime = &now
		updated = true
	}

	if updated {
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !isWorkflowTerminal(wf.Status.Phase) && wf.Spec.Timeout != nil && wf.Status.StartTime != nil {
		elapsed := time.Since(wf.Status.StartTime.Time)
		remaining := wf.Spec.Timeout.Duration - elapsed
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) reconcileJob(ctx context.Context, wf *runnersv1alpha1.Workflow, job *runnersv1alpha1.JobSpec, allRunners []runnersv1alpha1.Runner, jobStatusMap map[string]runnersv1alpha1.JobStatus) bool {
	updated := false

	js, hasStatus := jobStatusMap[job.Name]

	if hasStatus {
		switch js.Phase {
		case runnersv1alpha1.JobPhaseSucceeded, runnersv1alpha1.JobPhaseFailed, runnersv1alpha1.JobPhaseSkipped:
			return false
		}
	}

	decision := evaluateJobWhen(job, jobStatusMap)

	switch decision {
	case jobSkip:
		if !hasStatus {
			upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseSkipped)
			return true
		}
		return false

	case jobWait:
		if !hasStatus || js.Phase != runnersv1alpha1.JobPhaseWaiting {
			upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseWaiting)
			updated = true
		}
		return updated

	case jobRun:
		if !hasStatus || js.Phase != runnersv1alpha1.JobPhaseRunning {
			upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseRunning)
			updated = true
		}

		stepRunners := filterRunnersByJob(allRunners, job.Name)
		stepUpdated := r.reconcileJobSteps(ctx, wf, job, stepRunners)
		updated = updated || stepUpdated

		stepStatusMap := buildStepStatusMap(wf)
		allDone := true
		hasFailed := false
		for _, step := range job.Steps {
			st, found := stepStatusMap[step.Name]
			if !found {
				allDone = false
				continue
			}
			switch st.Phase {
			case runnersv1alpha1.StepPhasePending, runnersv1alpha1.StepPhaseWaiting, runnersv1alpha1.StepPhaseRunning:
				allDone = false
			case runnersv1alpha1.StepPhaseFailed:
				hasFailed = true
			}
		}

		if allDone {
			if hasFailed {
				upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseFailed)
			} else {
				upsertJobStatus(wf, job.Name, runnersv1alpha1.JobPhaseSucceeded)
			}
			updated = true
		}
	}

	return updated
}

func (r *WorkflowReconciler) reconcileJobSteps(ctx context.Context, wf *runnersv1alpha1.Workflow, job *runnersv1alpha1.JobSpec, stepRunners []runnersv1alpha1.Runner) bool {
	logger := log.FromContext(ctx)
	updated := false

	stepStatusMap := buildStepStatusMap(wf)
	runnerMap := buildRunnerMap(stepRunners)

	for _, step := range job.Steps {
		existing, hasRunner := runnerMap[step.Name]
		status, hasStatus := stepStatusMap[step.Name]

		if hasStatus {
			switch status.Phase {
			case runnersv1alpha1.StepPhaseSucceeded, runnersv1alpha1.StepPhaseFailed, runnersv1alpha1.StepPhaseSkipped:
				if hasRunner {
					continue
				}
			}
		}

		decision := evaluateStep(step, stepStatusMap)

		switch decision {
		case stepSkip:
			if !hasStatus {
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:    step.Name,
					Phase:   runnersv1alpha1.StepPhaseSkipped,
					Message: fmt.Sprintf("Dependencies did not meet the required condition (when: %q)", step.When),
				})
				updated = true
			}
			continue

		case stepWait:
			if !hasStatus {
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:  step.Name,
					Phase: runnersv1alpha1.StepPhaseWaiting,
				})
				updated = true
			}
			continue

		case stepRun:
			if !hasRunner {
				runner := r.buildJobStepRunner(ctx, wf, job, &step)
				if err := controllerutil.SetControllerReference(wf, runner, r.Scheme); err != nil {
					logger.Error(err, "Failed to set owner reference", "step", step.Name, "job", job.Name)
					continue
				}
				logger.Info("Creating Runner for step", "step", step.Name, "job", job.Name)
				r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRunnerCreated", "Created Runner for step %q in job %q", step.Name, job.Name)
				if err := r.Create(ctx, runner); err != nil {
					logger.Error(err, "Failed to create Runner", "step", step.Name, "job", job.Name)
					continue
				}
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:    step.Name,
					Phase:   runnersv1alpha1.StepPhasePending,
					Message: "Runner created",
				})
				updated = true
			} else {
				stepPhase := runnerPhaseToStepPhase(existing.Status.Phase)
				if stepPhase == runnersv1alpha1.StepPhaseFailed &&
					step.Retry != nil && step.Retry.MaxRetries > 0 &&
					status.RetryCount < step.Retry.MaxRetries {
					if !retryBackoffElapsed(&step, &status) {
						continue
					}
					logger.Info("Step failed, retrying", "step", step.Name, "job", job.Name, "attempt", status.RetryCount+1)
					r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRetrying", "Step %q in job %q failed, retrying (attempt %d/%d)", step.Name, job.Name, status.RetryCount+1, step.Retry.MaxRetries)
					if err := r.Delete(ctx, &existing); err != nil {
						logger.Error(err, "Failed to delete failed Runner for retry", "step", step.Name, "job", job.Name)
					}
					stepPhase = runnersv1alpha1.StepPhasePending
					upsertStepStatus(wf, step.Name, stepPhase)
					for i, s := range wf.Status.StepStatuses {
						if s.Name == step.Name {
							wf.Status.StepStatuses[i].RetryCount++
							break
						}
					}
					updated = true
				} else if !hasStatus || status.Phase != stepPhase {
					upsertStepStatus(wf, step.Name, stepPhase)
					updated = true
				}
			}
		}
	}

	return updated
}

func (r *WorkflowReconciler) buildJobStepRunner(ctx context.Context, wf *runnersv1alpha1.Workflow, job *runnersv1alpha1.JobSpec, step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner {
	runner := r.buildStepRunner(ctx, wf, step)

	runner.Labels["runner-operator.io/job"] = job.Name

	if len(job.Env) > 0 {
		runner.Spec.Env = append(job.Env, runner.Spec.Env...)
	}

	if job.GitRepo != nil && runner.Spec.GitRepo == nil {
		runner.Spec.GitRepo = job.GitRepo
	}

	if job.SharedVolume != nil && job.SharedVolume.EmptyDir != nil {
		mountPath := "/workspace"
		if job.SharedVolume.MountPath != "" {
			mountPath = job.SharedVolume.MountPath
		}
		volName := "job-shared-volume"
		runner.Spec.Volumes = append(runner.Spec.Volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: job.SharedVolume.EmptyDir,
			},
		})
		runner.Spec.Mounts = append(runner.Spec.Mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: mountPath,
		})
	}

	if job.SharedVolume != nil && job.SharedVolume.PersistentVolumeClaim != nil {
		mountPath := "/workspace"
		if job.SharedVolume.MountPath != "" {
			mountPath = job.SharedVolume.MountPath
		}
		volName := "job-shared-volume"
		runner.Spec.Volumes = append(runner.Spec.Volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: job.SharedVolume.PersistentVolumeClaim,
			},
		})
		runner.Spec.Mounts = append(runner.Spec.Mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: mountPath,
		})
	}

	return runner
}

// ─── Job helpers ─────────────────────────────────────────────────────────

func detectJobCycle(jobs []runnersv1alpha1.JobSpec) string {
	jobNames := map[string]bool{}
	for _, j := range jobs {
		jobNames[j.Name] = true
	}

	for _, j := range jobs {
		for _, need := range j.Needs {
			if !jobNames[need] {
				return fmt.Sprintf("job %q depends on unknown job %q", j.Name, need)
			}
		}
	}

	visited := map[string]int{}
	var dfs func(name string) bool
	dfs = func(name string) bool {
		if visited[name] == 1 {
			return true
		}
		if visited[name] == 2 {
			return false
		}
		visited[name] = 1
		for _, j := range jobs {
			if j.Name == name {
				if slices.ContainsFunc(j.Needs, dfs) {
					return true
				}
				break
			}
		}
		visited[name] = 2
		return false
	}

	for _, j := range jobs {
		if dfs(j.Name) {
			return fmt.Sprintf("cycle detected involving job %q", j.Name)
		}
	}
	return ""
}

type jobDecision int

const (
	jobSkip jobDecision = iota
	jobWait
	jobRun
)

func evaluateJobWhen(job *runnersv1alpha1.JobSpec, jobStatusMap map[string]runnersv1alpha1.JobStatus) jobDecision {
	for _, need := range job.Needs {
		js, ok := jobStatusMap[need]
		if !ok {
			return jobWait
		}
		switch js.Phase {
		case runnersv1alpha1.JobPhasePending, runnersv1alpha1.JobPhaseRunning, runnersv1alpha1.JobPhaseWaiting:
			return jobWait
		}
	}

	when := strings.TrimSpace(job.When)
	if when == "" {
		when = defaultWhen
	}

	allSucceeded := true
	for _, need := range job.Needs {
		js := jobStatusMap[need]
		if js.Phase != runnersv1alpha1.JobPhaseSucceeded {
			allSucceeded = false
			break
		}
	}

	switch when {
	case "always":
		return jobRun
	case "on_failure":
		if !allSucceeded {
			return jobRun
		}
		return jobSkip
	case defaultWhen:
		fallthrough
	default:
		if allSucceeded {
			return jobRun
		}
		return jobSkip
	}
}

func buildJobStatusMap(wf *runnersv1alpha1.Workflow) map[string]runnersv1alpha1.JobStatus {
	m := map[string]runnersv1alpha1.JobStatus{}
	for _, j := range wf.Status.JobStatuses {
		m[j.Name] = j
	}
	return m
}

func upsertJobStatus(wf *runnersv1alpha1.Workflow, jobName string, phase runnersv1alpha1.JobPhase) {
	for i, j := range wf.Status.JobStatuses {
		if j.Name == jobName {
			if j.Phase != phase {
				wf.Status.JobStatuses[i].Phase = phase
				if phase == runnersv1alpha1.JobPhaseRunning {
					if j.StartedAt == nil {
						now := metav1.Now()
						wf.Status.JobStatuses[i].StartedAt = &now
					}
					wf.Status.JobStatuses[i].CompletedAt = nil
				}
				if phase == runnersv1alpha1.JobPhaseSucceeded || phase == runnersv1alpha1.JobPhaseFailed || phase == runnersv1alpha1.JobPhaseSkipped {
					now := metav1.Now()
					wf.Status.JobStatuses[i].CompletedAt = &now
				}
			}
			return
		}
	}
	status := runnersv1alpha1.JobStatus{Name: jobName, Phase: phase}
	if phase == runnersv1alpha1.JobPhaseRunning {
		now := metav1.Now()
		status.StartedAt = &now
	}
	wf.Status.JobStatuses = append(wf.Status.JobStatuses, status)
}

func filterRunnersByJob(runners []runnersv1alpha1.Runner, jobName string) []runnersv1alpha1.Runner {
	var filtered []runnersv1alpha1.Runner
	for _, r := range runners {
		if jn, ok := r.Labels["runner-operator.io/job"]; ok && jn == jobName {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func computeJobWorkflowPhase(wf *runnersv1alpha1.Workflow) runnersv1alpha1.WorkflowPhase {
	if len(wf.Status.JobStatuses) == 0 {
		return runnersv1alpha1.WorkflowPhasePending
	}

	allDone := true
	hasFailed := false
	hasRunning := false

	for _, job := range wf.Spec.Jobs {
		js, found := findJobStatus(wf.Status.JobStatuses, job.Name)
		if !found {
			allDone = false
			continue
		}
		switch js.Phase {
		case runnersv1alpha1.JobPhasePending, runnersv1alpha1.JobPhaseWaiting, runnersv1alpha1.JobPhaseRunning:
			allDone = false
			hasRunning = true
		case runnersv1alpha1.JobPhaseFailed:
			hasFailed = true
		}
	}

	if hasFailed && allDone {
		return runnersv1alpha1.WorkflowPhaseFailed
	}
	if allDone {
		return runnersv1alpha1.WorkflowPhaseSucceeded
	}
	if hasRunning {
		return runnersv1alpha1.WorkflowPhaseRunning
	}
	return runnersv1alpha1.WorkflowPhasePending
}

func findJobStatus(statuses []runnersv1alpha1.JobStatus, name string) (runnersv1alpha1.JobStatus, bool) {
	for _, j := range statuses {
		if j.Name == name {
			return j, true
		}
	}
	return runnersv1alpha1.JobStatus{}, false
}

func computeFlatWorkflowPhase(wf *runnersv1alpha1.Workflow) runnersv1alpha1.WorkflowPhase {
	if len(wf.Status.StepStatuses) == 0 {
		return runnersv1alpha1.WorkflowPhasePending
	}

	allDone := true
	hasFailed := false
	hasRunning := false

	for _, step := range wf.Spec.Steps {
		status, found := findStepStatus(wf.Status.StepStatuses, step.Name)
		if !found {
			allDone = false
			continue
		}
		switch status.Phase {
		case runnersv1alpha1.StepPhasePending, runnersv1alpha1.StepPhaseWaiting, runnersv1alpha1.StepPhaseRunning:
			allDone = false
			hasRunning = true
		case runnersv1alpha1.StepPhaseFailed:
			hasFailed = true
		}
	}

	if hasFailed && allDone {
		return runnersv1alpha1.WorkflowPhaseFailed
	}
	if allDone {
		return runnersv1alpha1.WorkflowPhaseSucceeded
	}
	if hasRunning {
		return runnersv1alpha1.WorkflowPhaseRunning
	}
	return runnersv1alpha1.WorkflowPhasePending
}

func findStepStatus(statuses []runnersv1alpha1.StepStatus, name string) (runnersv1alpha1.StepStatus, bool) {
	for _, s := range statuses {
		if s.Name == name {
			return s, true
		}
	}
	return runnersv1alpha1.StepStatus{}, false
}

func isWorkflowTerminal(phase runnersv1alpha1.WorkflowPhase) bool {
	return phase == runnersv1alpha1.WorkflowPhaseSucceeded || phase == runnersv1alpha1.WorkflowPhaseFailed
}

func retryBackoffElapsed(step *runnersv1alpha1.WorkflowStep, status *runnersv1alpha1.StepStatus) bool {
	if step.Retry == nil || step.Retry.Backoff == nil || status.CompletedAt == nil {
		return true
	}

	delay := step.Retry.Backoff.InitialDelay.Duration
	for i := 0; i < status.RetryCount; i++ {
		delay *= 2
	}
	if step.Retry.Backoff.MaxDelay != nil && delay > step.Retry.Backoff.MaxDelay.Duration {
		delay = step.Retry.Backoff.MaxDelay.Duration
	}

	return time.Since(status.CompletedAt.Time) >= delay
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("workflow-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&runnersv1alpha1.Workflow{}).
		Owns(&runnersv1alpha1.Runner{}).
		Named("workflow").
		Complete(r)
}
