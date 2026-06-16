package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/mohamedhabas11/runner_operator/internal/gitops"
)

// setRunnerCondition is a helper that constructs a condition using
// ConditionBuilder and upserts it via meta.SetStatusCondition.
func setRunnerCondition(runner *runnersv1alpha1.Runner, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&runner.Status.Conditions, NewCondition(ConditionTypeReady).
		WithStatus(status).
		WithReason(reason).
		WithMessage(msg).
		WithObservedGeneration(runner.Generation).
		Build())
}

// RunnerReconciler reconciles a Runner object.
type RunnerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=runners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *RunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	runner := &runnersv1alpha1.Runner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !runner.DeletionTimestamp.IsZero() {
		if isNonTerminalPhase(runner.Status.Phase) {
			RunnerActiveCount.WithLabelValues(runner.Namespace, string(runner.Status.Phase)).Dec()
		}
		logger.Info("Runner is being deleted, relying on OwnerReferences for cleanup")
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

	// Job does not exist — first reconcile: validate, set status to Pending, store a hash
	// of the spec for drift detection, then create the Kubernetes Job. Validation runs
	// BEFORE the first status patch so a validation failure is persisted as a condition.
	if apierrors.IsNotFound(err) {
		if err := r.validateGitRepoSecret(ctx, runner); err != nil {
			logger.Error(err, "Git auth secret validation failed")
			setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerValidationFailed, err.Error())
			patchBase := client.MergeFrom(runner.DeepCopy())
			runner.Status.Phase = runnersv1alpha1.RunnerPhasePending
			runner.Status.ResourceHash = specHash
			runner.Status.ObservedGeneration = runner.Generation
			if patchErr := r.Status().Patch(ctx, runner, patchBase); patchErr != nil {
				logger.Error(patchErr, "Failed to patch status after validation failure")
				return ctrl.Result{}, patchErr
			}
			r.Recorder.Event(runner, corev1.EventTypeWarning, "ValidationFailed", err.Error())
			// Return nil (not the validation error) to prevent infinite requeue.
			// The next reconcile only triggers on spec change (generation bump).
			return ctrl.Result{}, nil
		}

		patchBase := client.MergeFrom(runner.DeepCopy())
		runner.Status.DesiredSpecHash = ""
		runner.Status.Phase = runnersv1alpha1.RunnerPhasePending
		runner.Status.ResourceHash = specHash
		runner.Status.ObservedGeneration = runner.Generation
		if err := r.Status().Patch(ctx, runner, patchBase); err != nil {
			return ctrl.Result{}, err
		}
		RunnerActiveCount.WithLabelValues(runner.Namespace, "Pending").Inc()

		job := r.buildJob(runner, jobName, specHash)
		if err := controllerutil.SetControllerReference(runner, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Creating Job", "job", jobName)
		if err := r.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.Info("Job already exists, likely created by concurrent reconcile", "job", jobName)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}

		r.Recorder.Event(runner, corev1.EventTypeNormal, "JobCreated", "Job created for Runner")

		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// Spec drift detected: the ResourceHash differs from the current spec hash,
	// or a DesiredSpecHash from a previous defer cycle indicates pending drift.
	// If the Job is still running, defer deletion to avoid interrupting execution.
	// Once the Job is finished (or hasn't started), delete it so the next reconcile
	// recreates it with the new spec.
	if runner.Status.ResourceHash != specHash || runner.Status.DesiredSpecHash != "" {
		if existingJob.Status.StartTime != nil && existingJob.Status.CompletionTime == nil {
			if runner.Status.ResourceHash != specHash {
				logger.Info("Spec drift detected but Job is running, deferring update")
				runner.Status.DesiredSpecHash = specHash
			}
			patchBase := client.MergeFrom(runner.DeepCopy())
			setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerSpecDriftDeferred, "Spec drift deferred until Job completes")
			if err := r.Status().Patch(ctx, runner, patchBase); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		// Stale DesiredSpecHash with no real drift: spec matches running Job, clear it.
		if runner.Status.ResourceHash == specHash {
			runner.Status.DesiredSpecHash = ""
			patchBase := client.MergeFrom(runner.DeepCopy())
			if err := r.Status().Patch(ctx, runner, patchBase); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Cleared stale DesiredSpecHash, spec matches running Job")
			return ctrl.Result{}, nil
		}
		// Re-run validation on the new spec before replacing the Job.
		if err := r.validateGitRepoSecret(ctx, runner); err != nil {
			logger.Error(err, "Git auth secret validation failed on spec drift")
			setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerValidationFailed, err.Error())
			r.Recorder.Event(runner, corev1.EventTypeWarning, "ValidationFailed", err.Error())
			return ctrl.Result{}, nil
		}

		logger.Info("Spec drift detected, deleting and recreating Job")
		r.Recorder.Event(runner, corev1.EventTypeNormal, "SpecDrift", "Spec changed, recreating Job")
		patchBase := client.MergeFrom(runner.DeepCopy())
		runner.Status.DesiredSpecHash = ""
		runner.Status.ResourceHash = specHash
		setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerSpecDriftReplaced, "Spec changed, recreating Job")
		if err := r.Status().Patch(ctx, runner, patchBase); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Delete(ctx, existingJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.updateStatusFromJob(ctx, runner, existingJob)
}

func (r *RunnerReconciler) buildJob(runner *runnersv1alpha1.Runner, jobName, specHash string) *batchv1.Job {
	backoffLimit := int32(0)

	sc := runner.Spec.SecurityContext
	if sc == nil {
		sc = &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
	}

	containers := []corev1.Container{
		{
			Name:            "runner",
			Image:           runner.Spec.Image,
			Env:             runner.Spec.Env,
			EnvFrom:         runner.Spec.EnvFrom,
			Args:            runner.Spec.Args,
			Command:         runner.Spec.Command,
			Resources:       runner.Spec.Resources,
			VolumeMounts:    runner.Spec.Mounts,
			SecurityContext: sc,
		},
	}

	volumes := runner.Spec.Volumes
	var initContainers []corev1.Container

	if runner.Spec.GitRepo != nil {
		strategy := gitops.NewAuthStrategy(runner.Spec.GitRepo)
		volumes = append(volumes, gitops.BuildVolumes(runner.Spec.GitRepo, strategy)...)

		containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      gitops.WorkspaceVolumeName,
			MountPath: gitops.WorkspaceMountPath,
		})

		initContainers = append(initContainers, gitops.BuildInitContainer(runner.Spec.GitRepo, strategy))

		workDir := gitops.WorkspaceMountPath + "/" + gitops.RepoSubPath
		if runner.Spec.GitRepo.Path != "" {
			workDir = workDir + "/" + runner.Spec.GitRepo.Path
		}
		containers[0].WorkingDir = workDir
	}

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
					RestartPolicy:      corev1.RestartPolicyNever,
					InitContainers:     initContainers,
					Containers:         containers,
					Volumes:            volumes,
					SecurityContext:    runnerSecurityContext(),
					ServiceAccountName: runner.Spec.ServiceAccountName,
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

func runnerSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: new(true),
		RunAsUser:    new(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
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

	if runner.Status.Phase == phase &&
		runner.Status.ObservedGeneration == runner.Generation &&
		metav1TimePtrEqual(runner.Status.StartTime, startTime) &&
		metav1TimePtrEqual(runner.Status.CompletionTime, completionTime) {
		return ctrl.Result{}, nil
	}

	patchBase := client.MergeFrom(runner.DeepCopy())
	oldPhase := runner.Status.Phase
	if oldPhase != phase {
		r.Recorder.Eventf(runner, corev1.EventTypeNormal, "PhaseChanged", "Runner phase changed to %s", phase)
	}
	runner.Status.Phase = phase
	runner.Status.ObservedGeneration = runner.Generation
	if startTime != nil {
		runner.Status.StartTime = startTime
	}
	if completionTime != nil {
		runner.Status.CompletionTime = completionTime
	}

	// Update active runner gauge on phase transitions
	if oldPhase != phase {
		if isNonTerminalPhase(oldPhase) {
			RunnerActiveCount.WithLabelValues(runner.Namespace, string(oldPhase)).Dec()
		}
		if isNonTerminalPhase(phase) {
			RunnerActiveCount.WithLabelValues(runner.Namespace, string(phase)).Inc()
		}
	}

	switch phase {
	case runnersv1alpha1.RunnerPhaseSucceeded:
		setRunnerCondition(runner, metav1.ConditionTrue, ReasonRunnerSucceeded, "Runner completed successfully")
		RunnerJobCompletedTotal.WithLabelValues(runner.Namespace, "succeeded").Inc()
		if runner.Status.StartTime != nil && runner.Status.CompletionTime != nil {
			duration := runner.Status.CompletionTime.Sub(runner.Status.StartTime.Time).Seconds()
			if duration >= 0 {
				RunnerDurationSeconds.WithLabelValues(runner.Namespace).Observe(duration)
			}
		}
	case runnersv1alpha1.RunnerPhaseFailed:
		setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerFailed, "Runner failed")
		RunnerJobCompletedTotal.WithLabelValues(runner.Namespace, "failed").Inc()
		if runner.Status.StartTime != nil && runner.Status.CompletionTime != nil {
			duration := runner.Status.CompletionTime.Sub(runner.Status.StartTime.Time).Seconds()
			if duration >= 0 {
				RunnerDurationSeconds.WithLabelValues(runner.Namespace).Observe(duration)
			}
		}
	case runnersv1alpha1.RunnerPhaseRunning:
		setRunnerCondition(runner, metav1.ConditionFalse, ReasonRunnerRunning, "Runner is running")
	case runnersv1alpha1.RunnerPhaseUnknown:
		RunnerJobCompletedTotal.WithLabelValues(runner.Namespace, "unknown").Inc()
	}
	logger.Info("Runner phase changed", "phase", phase)

	if err := r.Status().Patch(ctx, runner, patchBase); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// validateGitRepoSecret checks that the Git auth Secret exists and contains
// all required keys before a Job is created. Returns nil for no auth or public
// repos or when the secret is provided by an external mechanism (CSI, External
// Secrets Operator, etc.) and therefore doesn't exist as a Kubernetes Secret.
// Returns an error with a user-facing message if validation fails for a
// resource that does exist and is missing required keys.
func (r *RunnerReconciler) validateGitRepoSecret(ctx context.Context, runner *runnersv1alpha1.Runner) error {
	if runner.Spec.GitRepo == nil || runner.Spec.GitRepo.Auth == nil {
		return nil
	}
	secretName := runner.Spec.GitRepo.Auth.SecretRef.Name
	if secretName == "" {
		return nil
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: runner.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			// Secret may be provided by CSI SecretProviderClass, External Secrets
			// Operator, or another mechanism. Log a warning and proceed.
			log.FromContext(ctx).Info("Git auth secret not found as Kubernetes Secret, assuming external provider",
				"secret", secretName, "namespace", runner.Namespace)
			return nil
		}
		return fmt.Errorf("failed to check git auth secret %q: %w", secretName, err)
	}
	requiredKeys := gitops.RequiredSecretKeys(runner.Spec.GitRepo)
	if err := checkSecretHasKeys(secret, secretName, requiredKeys); err != nil {
		return err
	}
	return nil
}

// checkSecretHasKeys verifies that all requiredKeys exist in the Secret's Data or
// StringData map (StringData is the unencoded, write-only field). Returns a
// user-facing error if any key is missing from both maps.
func checkSecretHasKeys(secret *corev1.Secret, name string, requiredKeys []string) error {
	for _, key := range requiredKeys {
		_, inData := secret.Data[key]
		_, inStringData := secret.StringData[key]
		if !inData && !inStringData {
			return fmt.Errorf("git auth secret %q missing required key %q", name, key)
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("runner-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&runnersv1alpha1.Runner{}).
		Owns(&batchv1.Job{}).
		Named("runner").
		Complete(r)
}

// computeSpecHash returns a deterministic hash of any spec struct. Uses the first
// 8 bytes of SHA-256 — sufficient collision resistance for operator-scale objects.
func computeSpecHash(spec any) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:8]), nil
}

func metav1TimePtrEqual(a, b *metav1.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Time.Equal(b.Time)
}

// isNonTerminalPhase returns true for phases that represent an active runner.
// Terminal phases (Succeeded, Failed, Unknown) should not be counted as active.
func isNonTerminalPhase(phase runnersv1alpha1.RunnerPhase) bool {
	return phase == runnersv1alpha1.RunnerPhasePending || phase == runnersv1alpha1.RunnerPhaseRunning
}
