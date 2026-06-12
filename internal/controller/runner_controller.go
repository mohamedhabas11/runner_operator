package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

const RunnerFinalizer = "runners.runner-operator.io/finalizer"

// RunnerReconciler reconciles a Runner object.
type RunnerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *RunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	runner := &runnersv1alpha1.Runner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !runner.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, runner)
	}

	if !controllerutil.ContainsFinalizer(runner, RunnerFinalizer) {
		logger.Info("Adding finalizer")
		controllerutil.AddFinalizer(runner, RunnerFinalizer)
		if err := r.Update(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	specHash, err := computeSpecHash(runner.Spec)
	if err != nil {
		logger.Error(err, "Failed to compute spec hash")
		return ctrl.Result{}, err
	}

	jobName := runner.Name + "-job"
	existingJob := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: runner.Namespace}, existingJob)

	if apierrors.IsNotFound(err) {
		job := r.buildJob(runner, jobName, specHash)
		if err := controllerutil.SetControllerReference(runner, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Creating Job", "job", jobName)
		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, err
		}

		runner.Status.Phase = runnersv1alpha1.RunnerPhasePending
		runner.Status.ResourceHash = specHash
		if err := r.Status().Update(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if runner.Status.ResourceHash != specHash {
		logger.Info("Spec drift detected, deleting and recreating Job")
		if err := r.Delete(ctx, existingJob); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Update(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.updateStatusFromJob(ctx, runner, existingJob)
}

func (r *RunnerReconciler) reconcileDelete(ctx context.Context, runner *runnersv1alpha1.Runner) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(runner, RunnerFinalizer) {
		return ctrl.Result{}, nil
	}

	jobName := runner.Name + "-job"
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: runner.Namespace}, job); err == nil {
		logger.Info("Cleaning up Job", "job", jobName)
		if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(runner, RunnerFinalizer)
	if err := r.Update(ctx, runner); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *RunnerReconciler) buildJob(runner *runnersv1alpha1.Runner, jobName, specHash string) *batchv1.Job {
	backoffLimit := int32(0)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: runner.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "runner-operator",
				"runner-operator.io/runner":    runner.Name,
				"runner-operator.io/spec-hash": specHash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":       "runner-operator",
						"runner-operator.io/runner":    runner.Name,
						"runner-operator.io/spec-hash": specHash,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "runner",
							Image:   runner.Spec.Image,
							Env:     runner.Spec.Env,
							EnvFrom: runner.Spec.EnvFrom,
							Args:    runner.Spec.Args,
							Command: runner.Spec.Command,
							VolumeMounts: func() []corev1.VolumeMount {
								if len(runner.Spec.Mounts) > 0 {
									return runner.Spec.Mounts
								}
								return nil
							}(),
						},
					},
					Volumes: func() []corev1.Volume {
						if len(runner.Spec.Volumes) > 0 {
							return runner.Spec.Volumes
						}
						return nil
					}(),
				},
			},
		},
	}

	if runner.Spec.TimeoutAfter != nil {
		activeDeadline := runner.Spec.TimeoutAfter.Seconds()
		activeDeadlineSeconds := int64(activeDeadline)
		job.Spec.ActiveDeadlineSeconds = &activeDeadlineSeconds
	}

	return job
}

func (r *RunnerReconciler) updateStatusFromJob(ctx context.Context, runner *runnersv1alpha1.Runner, job *batchv1.Job) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	phase := runnersv1alpha1.RunnerPhaseUnknown
	var startTime, completionTime *metav1.Time

	for _, cond := range job.Status.Conditions {
		switch cond.Type {
		case batchv1.JobComplete:
			if cond.Status == corev1.ConditionTrue {
				phase = runnersv1alpha1.RunnerPhaseSucceeded
				completionTime = job.Status.CompletionTime
			}
		case batchv1.JobFailed:
			if cond.Status == corev1.ConditionTrue {
				phase = runnersv1alpha1.RunnerPhaseFailed
				completionTime = job.Status.CompletionTime
			}
		}
	}

	if phase == runnersv1alpha1.RunnerPhaseUnknown {
		if job.Status.StartTime != nil {
			phase = runnersv1alpha1.RunnerPhaseRunning
			startTime = job.Status.StartTime
		} else {
			phase = runnersv1alpha1.RunnerPhasePending
		}
	}

	if runner.Status.Phase != phase {
		logger.Info("Runner phase changed", "phase", phase)
		runner.Status.Phase = phase
	}

	if startTime != nil {
		runner.Status.StartTime = startTime
	}
	if completionTime != nil {
		runner.Status.CompletionTime = completionTime
	}

	if err := r.Status().Update(ctx, runner); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&runnersv1alpha1.Runner{}).
		Owns(&batchv1.Job{}).
		Named("runner").
		Complete(r)
}

func computeSpecHash(spec any) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:8]), nil
}
