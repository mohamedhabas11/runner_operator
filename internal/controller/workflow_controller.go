package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

func setWorkflowCondition(wf *runnersv1alpha1.Workflow, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&wf.Status.Conditions, NewCondition(ConditionTypeReady).
		WithStatus(status).
		WithReason(reason).
		WithMessage(msg).
		WithObservedGeneration(wf.Generation).
		Build())
}

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
		patchBase := client.MergeFrom(wf.DeepCopy())
		setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowNoSteps, "Workflow has no steps defined")
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if cycle := detectCycle(wf.Spec.Steps); cycle != "" {
		logger.Info("Cycle detected in workflow steps", "cycle", cycle)
		r.Recorder.Eventf(wf, corev1.EventTypeWarning, "CycleDetected", "Dependency cycle detected: %s", cycle)
		patchBase := client.MergeFrom(wf.DeepCopy())
		setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowCycleDetected, "Dependency cycle detected: "+cycle)
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
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
		switch newPhase {
		case runnersv1alpha1.WorkflowPhaseRunning:
			setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowRunning, "Workflow is running")
		case runnersv1alpha1.WorkflowPhaseSucceeded:
			setWorkflowCondition(wf, metav1.ConditionTrue, ReasonWorkflowSucceeded, "All steps completed successfully")
		case runnersv1alpha1.WorkflowPhaseFailed:
			setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowFailed, "Workflow failed")
		}
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
	setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowTimedOut, "Workflow timed out")

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
	setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowTimedOut, "Workflow timed out")

	if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) reconcileSteps(ctx context.Context, wf *runnersv1alpha1.Workflow, stepRunners []runnersv1alpha1.Runner) bool {
	return r.reconcileStepLoop(ctx, wf, wf.Spec.Steps, stepRunners,
		func(step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner {
			return r.buildStepRunner(ctx, wf, step)
		}, "")
}

// reconcileStepLoop is the shared engine for both flat and job-based step
// reconciliation. It iterates the given steps, evaluates dependencies, and
// creates/manages Runner resources. When jobName is non-empty, log and event
// messages include job context, and the caller-provided buildRunner closure
// is expected to include job-specific decoration (env, shared volumes, etc.).
func (r *WorkflowReconciler) reconcileStepLoop(
	ctx context.Context,
	wf *runnersv1alpha1.Workflow,
	steps []runnersv1alpha1.WorkflowStep,
	stepRunners []runnersv1alpha1.Runner,
	buildRunner func(step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner,
	jobName string,
) bool {
	logger := log.FromContext(ctx)
	updated := false

	stepStatusMap := buildStepStatusMap(wf)
	runnerMap := buildRunnerMap(stepRunners)

	for _, step := range steps {
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
				stepStatusMap = buildStepStatusMap(wf)
			}
			continue

		case stepWait:
			if !hasStatus {
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:  step.Name,
					Phase: runnersv1alpha1.StepPhaseWaiting,
				})
				updated = true
				stepStatusMap = buildStepStatusMap(wf)
			}
			continue

		case stepRun:
			if !hasRunner {
				runner := buildRunner(&step)
				if err := controllerutil.SetControllerReference(wf, runner, r.Scheme); err != nil {
					logger.Error(err, "Failed to set owner reference", "step", step.Name, "job", jobName)
					continue
				}
				logger.Info("Creating Runner for step", "step", step.Name, "job", jobName)
				eventMsg := fmt.Sprintf("Created Runner for step %s", step.Name)
				if jobName != "" {
					eventMsg = fmt.Sprintf("Created Runner for step %s in job %s", step.Name, jobName)
				}
				r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRunnerCreated", eventMsg)
				if err := r.Create(ctx, runner); err != nil {
					logger.Error(err, "Failed to create Runner", "step", step.Name, "job", jobName)
					continue
				}
				wf.Status.StepStatuses = append(wf.Status.StepStatuses, runnersv1alpha1.StepStatus{
					Name:    step.Name,
					Phase:   runnersv1alpha1.StepPhasePending,
					Message: "Runner created",
				})
				updated = true
				stepStatusMap = buildStepStatusMap(wf)
			} else {
				stepPhase := runnerPhaseToStepPhase(existing.Status.Phase)
				if stepPhase == runnersv1alpha1.StepPhaseFailed &&
					step.Retry != nil && step.Retry.MaxRetries > 0 &&
					status.RetryCount < step.Retry.MaxRetries {
					if !retryBackoffElapsed(&step, &status) {
						continue
					}
					logger.Info("Step failed, retrying", "step", step.Name, "job", jobName, "attempt", status.RetryCount+1)
					retryMsg := fmt.Sprintf("Step %s failed, retrying (attempt %d/%d)", step.Name, status.RetryCount+1, step.Retry.MaxRetries)
					if jobName != "" {
						retryMsg = fmt.Sprintf("Step %s in job %s failed, retrying (attempt %d/%d)", step.Name, jobName, status.RetryCount+1, step.Retry.MaxRetries)
					}
					r.Recorder.Eventf(wf, corev1.EventTypeNormal, "StepRetrying", retryMsg)
					if err := r.Delete(ctx, &existing); err != nil {
						logger.Error(err, "Failed to delete failed Runner for retry", "step", step.Name, "job", jobName)
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
					stepStatusMap = buildStepStatusMap(wf)
				} else if !hasStatus || status.Phase != stepPhase {
					upsertStepStatus(wf, step.Name, stepPhase)
					updated = true
					stepStatusMap = buildStepStatusMap(wf)
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

// evaluateStep determines whether a step should run, wait, or be skipped based on
// its dependency statuses and the `when` condition. Semantics:
//   - Dependencies not yet resolved → stepWait (re-evaluate next cycle)
//   - when="always" → run regardless of dep outcomes
//   - when="on_failure" → run only if any dependency failed
//   - when="on_success" (default) → run only if all dependencies succeeded
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

// sanitizeStepName converts a human-readable step name to a valid Kubernetes
// resource name suffix (lowercase alphanumeric + hyphens, max 50 chars).
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

// buildStepRunner creates a Runner CR from a WorkflowStep. The resolution order is:
// 1. step.Image (explicit image) → 2. step.RunnerRef (copy template spec) → 3. busybox (fallback).
// step-level Command/Args/Env/GitRepo/Timeout override the template when RunnerRef is used.
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
		ns := wf.Namespace
		if step.RunnerRef.Namespace != "" {
			ns = step.RunnerRef.Namespace
		}
		if err := r.Get(ctx, types.NamespacedName{Name: step.RunnerRef.Name, Namespace: ns}, template); err == nil {
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
			stepRef := fmt.Sprintf("%s/%s", ns, step.RunnerRef.Name)
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

// detectCycle is a thin wrapper that adapts WorkflowStep to the generic
// cycleDetector. It uses DependsOn as the dependency edge set.
func detectCycle(steps []runnersv1alpha1.WorkflowStep) string {
	return cycleDetector(steps,
		func(s runnersv1alpha1.WorkflowStep) string { return s.Name },
		func(s runnersv1alpha1.WorkflowStep) []string { return s.DependsOn },
		"step")
}

// cycleDetector implements 3-color DFS cycle detection for any item type
// with a name and a list of dependency names. It returns a human-readable
// error string, or "" if the graph is acyclic.
func cycleDetector[T any](items []T, name func(T) string, deps func(T) []string, kind string) string {
	names := map[string]bool{}
	for _, item := range items {
		names[name(item)] = true
	}

	for _, item := range items {
		for _, dep := range deps(item) {
			if !names[dep] {
				return fmt.Sprintf("%s %q depends on unknown %s %q", kind, name(item), kind, dep)
			}
		}
	}

	visited := map[string]int{}
	var dfs func(cur string) bool
	dfs = func(cur string) bool {
		if visited[cur] == 1 {
			return true
		}
		if visited[cur] == 2 {
			return false
		}
		visited[cur] = 1
		for _, item := range items {
			if name(item) == cur {
				if slices.ContainsFunc(deps(item), dfs) {
					return true
				}
				break
			}
		}
		visited[cur] = 2
		return false
	}

	for _, item := range items {
		if dfs(name(item)) {
			return fmt.Sprintf("cycle detected involving %s %q", kind, name(item))
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
		patchBase := client.MergeFrom(wf.DeepCopy())
		setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowNoSteps, "Workflow has no jobs defined")
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if cycle := detectJobCycle(wf.Spec.Jobs); cycle != "" {
		logger.Info("Cycle detected in workflow jobs", "cycle", cycle)
		r.Recorder.Eventf(wf, corev1.EventTypeWarning, "CycleDetected", "Job dependency cycle detected: %s", cycle)
		patchBase := client.MergeFrom(wf.DeepCopy())
		setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowCycleDetected, "Job dependency cycle detected: "+cycle)
		wf.Status.ObservedGeneration = wf.Generation
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
		}
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
		switch newPhase {
		case runnersv1alpha1.WorkflowPhaseRunning:
			setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowRunning, "Workflow is running")
		case runnersv1alpha1.WorkflowPhaseSucceeded:
			setWorkflowCondition(wf, metav1.ConditionTrue, ReasonWorkflowSucceeded, "All jobs completed successfully")
		case runnersv1alpha1.WorkflowPhaseFailed:
			setWorkflowCondition(wf, metav1.ConditionFalse, ReasonWorkflowFailed, "Workflow failed")
		}
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
	return r.reconcileStepLoop(ctx, wf, job.Steps, stepRunners,
		func(step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner {
			return r.buildJobStepRunner(ctx, wf, job, step)
		}, job.Name)
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
	return cycleDetector(jobs,
		func(j runnersv1alpha1.JobSpec) string { return j.Name },
		func(j runnersv1alpha1.JobSpec) []string { return j.Needs },
		"job")
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
	return computeWorkflowPhase(getSpecJobNames(wf.Spec.Jobs), wf.Status.JobStatuses,
		findJobStatus, isActiveJobPhase, isFailedJobPhase)
}

func computeFlatWorkflowPhase(wf *runnersv1alpha1.Workflow) runnersv1alpha1.WorkflowPhase {
	return computeWorkflowPhase(getSpecStepNames(wf.Spec.Steps), wf.Status.StepStatuses,
		findStepStatus, isActiveStepPhase, isFailedStepPhase)
}

func getSpecJobNames(jobs []runnersv1alpha1.JobSpec) []string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return names
}

func getSpecStepNames(steps []runnersv1alpha1.WorkflowStep) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name
	}
	return names
}

func isActiveJobPhase(s runnersv1alpha1.JobStatus) bool {
	return s.Phase == runnersv1alpha1.JobPhasePending || s.Phase == runnersv1alpha1.JobPhaseWaiting || s.Phase == runnersv1alpha1.JobPhaseRunning
}

func isFailedJobPhase(s runnersv1alpha1.JobStatus) bool {
	return s.Phase == runnersv1alpha1.JobPhaseFailed
}

func isActiveStepPhase(s runnersv1alpha1.StepStatus) bool {
	return s.Phase == runnersv1alpha1.StepPhasePending || s.Phase == runnersv1alpha1.StepPhaseWaiting || s.Phase == runnersv1alpha1.StepPhaseRunning
}

func isFailedStepPhase(s runnersv1alpha1.StepStatus) bool {
	return s.Phase == runnersv1alpha1.StepPhaseFailed
}

func findJobStatus(statuses []runnersv1alpha1.JobStatus, name string) (runnersv1alpha1.JobStatus, bool) {
	for _, j := range statuses {
		if j.Name == name {
			return j, true
		}
	}
	return runnersv1alpha1.JobStatus{}, false
}

func findStepStatus(statuses []runnersv1alpha1.StepStatus, name string) (runnersv1alpha1.StepStatus, bool) {
	for _, s := range statuses {
		if s.Name == name {
			return s, true
		}
	}
	return runnersv1alpha1.StepStatus{}, false
}

// computeWorkflowPhase aggregates per-item status into a workflow-level phase.
// specItemNames is the full list of items expected; statuses are the observed results.
// findStatus, isActive, and isFailed abstract over concrete status types.
func computeWorkflowPhase[T any](
	specItemNames []string,
	statuses []T,
	findStatus func([]T, string) (T, bool),
	isActive func(T) bool,
	isFailed func(T) bool,
) runnersv1alpha1.WorkflowPhase {
	if len(statuses) == 0 {
		return runnersv1alpha1.WorkflowPhasePending
	}

	allDone := true
	hasFailed := false
	hasRunning := false

	for _, name := range specItemNames {
		status, found := findStatus(statuses, name)
		if !found {
			allDone = false
			continue
		}
		if isActive(status) {
			allDone = false
			hasRunning = true
		}
		if isFailed(status) {
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

func isWorkflowTerminal(phase runnersv1alpha1.WorkflowPhase) bool {
	return phase == runnersv1alpha1.WorkflowPhaseSucceeded || phase == runnersv1alpha1.WorkflowPhaseFailed
}

// retryBackoffElapsed implements exponential backoff for step retries.
// Returns true when the backoff period has elapsed since the step's last completion.
// Formula: delay = InitialDelay * 2^retryCount, capped at MaxDelay.
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
