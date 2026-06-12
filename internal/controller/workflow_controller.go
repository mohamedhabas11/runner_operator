package controller

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

// WorkflowReconciler reconciles a Workflow object.
type WorkflowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=workflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=workflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners/status,verbs=get

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

	if len(wf.Spec.Steps) == 0 {
		return ctrl.Result{}, nil
	}

	stepRunners, err := r.listStepRunners(ctx, wf)
	if err != nil {
		return ctrl.Result{}, err
	}

	updated := r.reconcileSteps(ctx, wf, stepRunners)

	if wf.Status.StartTime == nil {
		now := metav1.Now()
		wf.Status.StartTime = &now
		updated = true
	}

	newPhase := computeWorkflowPhase(wf)
	if newPhase != wf.Status.Phase {
		wf.Status.Phase = newPhase
		updated = true
	}
	if isWorkflowTerminal(wf.Status.Phase) && wf.Status.CompletionTime == nil {
		now := metav1.Now()
		wf.Status.CompletionTime = &now
		updated = true
	}

	if updated {
		patchBase := client.MergeFrom(wf.DeepCopy())
		if err := r.Status().Patch(ctx, wf, patchBase); err != nil {
			return ctrl.Result{}, err
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
					logger.Info("Step failed, retrying", "step", step.Name, "attempt", status.RetryCount+1)
					if err := r.Delete(ctx, &existing); err != nil {
						logger.Error(err, "Failed to delete failed Runner for retry", "step", step.Name)
					}
					stepPhase = runnersv1alpha1.StepPhaseRunning
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
		when = "on_success"
	}

	allDepSucceeded := true
	for _, dep := range step.DependsOn {
		status := stepStatusMap[dep]
		if status.Phase != runnersv1alpha1.StepPhaseSucceeded {
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
	case "on_success":
		fallthrough
	default:
		if allDepSucceeded {
			return stepRun
		}
		return stepSkip
	}
}

func (r *WorkflowReconciler) buildStepRunner(ctx context.Context, wf *runnersv1alpha1.Workflow, step *runnersv1alpha1.WorkflowStep) *runnersv1alpha1.Runner {
	runnerName := fmt.Sprintf("%s-%s", wf.Name, step.Name)

	spec := runnersv1alpha1.RunnerSpec{
		Command: step.Command,
		Args:    step.Args,
		Env:     step.Env,
	}

	if step.Image != "" {
		spec.Image = step.Image
	} else if step.RunnerRef != nil {
		template := &runnersv1alpha1.Runner{}
		if err := r.Get(ctx, types.NamespacedName{Name: step.RunnerRef.Name, Namespace: wf.Namespace}, template); err == nil {
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
		} else {
			stepRef := fmt.Sprintf("%s/%s", wf.Namespace, step.RunnerRef.Name)
			log.FromContext(ctx).Error(err, "RunnerRef not found, using default image", "runnerRef", stepRef)
			spec.Image = "busybox:latest"
		}
	} else {
		spec.Image = "busybox:latest"
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

func computeWorkflowPhase(wf *runnersv1alpha1.Workflow) runnersv1alpha1.WorkflowPhase {
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

// SetupWithManager sets up the controller with the Manager.
func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&runnersv1alpha1.Workflow{}).
		Owns(&runnersv1alpha1.Runner{}).
		Named("workflow").
		Complete(r)
}
