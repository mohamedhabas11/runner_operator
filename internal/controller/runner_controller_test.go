package controller

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

var _ = Describe("Runner Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-runner-resource"
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		runner := &runnersv1alpha1.Runner{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Runner")
			err := k8sClient.Get(ctx, typeNamespacedName, runner)
			if err != nil && errors.IsNotFound(err) {
				resource := &runnersv1alpha1.Runner{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: runnersv1alpha1.RunnerSpec{
						Image: "busybox:latest",
						Args:  []string{"sh", "-c", "echo test"},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// Remove finalizer first so deletion can complete
			err := k8sClient.Get(ctx, typeNamespacedName, runner)
			if err == nil {
				original := runner.DeepCopy()
				runner.Finalizers = nil
				if err := k8sClient.Patch(ctx, runner, client.MergeFrom(original)); err == nil {
					Expect(k8sClient.Delete(ctx, runner)).To(Succeed())
				}
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &RunnerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Job creation", func() {
		ctx := context.Background()

		It("should create a Job with the correct spec", func() {
			name := "test-runner-job-spec"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			runner := &runnersv1alpha1.Runner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.RunnerSpec{
					Image: "busybox:latest",
					Args:  []string{"sh", "-c", "echo test"},
				},
			}
			Expect(k8sClient.Create(ctx, runner)).To(Succeed())
			defer cleanupRunner(ctx, name)

			r := &RunnerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)
			Expect(err).NotTo(HaveOccurred())
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("busybox:latest"))
			Expect(job.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"sh", "-c", "echo test"}))
			Expect(job.Spec.BackoffLimit).To(HaveValue(Equal(int32(0))))

			sc := job.Spec.Template.Spec.Containers[0].SecurityContext
			Expect(sc).NotTo(BeNil())
			Expect(sc.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
			Expect(sc.ReadOnlyRootFilesystem).To(HaveValue(BeTrue()))
		})

		It("should transition through Pending and Succeeded phases", func() {
			name := "test-runner-phases"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			runner := &runnersv1alpha1.Runner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.RunnerSpec{
					Image: "busybox:latest",
					Args:  []string{"sh", "-c", "echo test"},
				},
			}
			Expect(k8sClient.Create(ctx, runner)).To(Succeed())
			defer cleanupRunner(ctx, name)

			r := &RunnerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			By("Reconciling — phase should be Pending with a Job created")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, nsName, runner)).To(Succeed())
			Expect(runner.Status.Phase).To(Equal(runnersv1alpha1.RunnerPhasePending))

			By("Simulating the Job starting — phase should transition to Running")
			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)).To(Succeed())
			now := metav1.Now()
			job.Status = batchv1.JobStatus{
				StartTime: &now,
				Active:    1,
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, nsName, runner)).To(Succeed())
			Expect(runner.Status.Phase).To(Equal(runnersv1alpha1.RunnerPhaseRunning))

			By("Simulating the Job succeeding — phase should transition to Succeeded")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)).To(Succeed())
			completionTime := metav1.Now()
			job.Status = batchv1.JobStatus{
				StartTime:      &now,
				CompletionTime: &completionTime,
				Succeeded:      1,
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, nsName, runner)).To(Succeed())
			Expect(runner.Status.Phase).To(Equal(runnersv1alpha1.RunnerPhaseSucceeded))
			Expect(runner.Status.CompletionTime).NotTo(BeNil())
		})

		It("should delete the old Job when spec drifts after completion", func() {
			name := "test-runner-drift"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			runner := &runnersv1alpha1.Runner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.RunnerSpec{
					Image: "busybox:latest",
					Args:  []string{"sh", "-c", "echo test"},
				},
			}
			Expect(k8sClient.Create(ctx, runner)).To(Succeed())
			defer cleanupRunner(ctx, name)

			r := &RunnerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			By("First reconcile — creates Job with initial spec")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)).To(Succeed())

			By("Simulating Job completion")
			now := metav1.Now()
			job.Status = batchv1.JobStatus{
				StartTime:      &now,
				CompletionTime: &now,
				Succeeded:      1,
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			By("Modifying the Runner spec to trigger drift")
			Expect(k8sClient.Get(ctx, nsName, runner)).To(Succeed())
			runner.Spec.Args = []string{"sh", "-c", "echo drifted"}
			Expect(k8sClient.Update(ctx, runner)).To(Succeed())

			By("Second reconcile — detects drift, initiates old Job deletion")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			// The controller calls Delete on the old Job (background propagation).
			// In envtest there's no GC controller, so the object persists with a
			// non-nil DeletionTimestamp until the next reconcile processes it.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)).To(Succeed())
			Expect(job.DeletionTimestamp).NotTo(BeNil())
		})
	})

	Context("Validation", func() {
		ctx := context.Background()

		It("should log a warning and proceed when git secret is not found (CSI / external-secrets)", func() {
			name := "test-runner-git-notfound"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			runner := &runnersv1alpha1.Runner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.RunnerSpec{
					Image: "busybox:latest",
					Args:  []string{"sh", "-c", "echo test"},
					GitRepo: &runnersv1alpha1.GitRepo{
						URL: "https://github.com/octocat/hello-world.git",
						Auth: &runnersv1alpha1.GitAuth{
							Type: runnersv1alpha1.GitAuthTypeToken,
							SecretRef: corev1.LocalObjectReference{
								Name: "nonexistent-secret",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, runner)).To(Succeed())
			defer cleanupRunner(ctx, name)

			r := &RunnerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			// Should proceed to create a Job despite missing secret
			job := &batchv1.Job{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: name + "-job", Namespace: "default"}, job)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func cleanupRunner(ctx context.Context, name string) {
	runner := &runnersv1alpha1.Runner{}
	nsName := types.NamespacedName{Name: name, Namespace: "default"}
	if err := k8sClient.Get(ctx, nsName, runner); err != nil {
		return
	}
	// Remove finalizer so the object can be deleted
	original := runner.DeepCopy()
	runner.Finalizers = nil
	if err := k8sClient.Patch(ctx, runner, client.MergeFrom(original)); err != nil {
		return
	}
	_ = k8sClient.Delete(ctx, runner)
}
