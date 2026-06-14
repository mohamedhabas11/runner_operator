package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

var _ = Describe("Workflow Controller", func() {
	ctx := context.Background()

	Context("When reconciling a resource", func() {
		const resourceName = "test-workflow-resource"
		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		workflow := &runnersv1alpha1.Workflow{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Workflow")
			err := k8sClient.Get(ctx, typeNamespacedName, workflow)
			if err != nil && errors.IsNotFound(err) {
				resource := &runnersv1alpha1.Workflow{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: runnersv1alpha1.WorkflowSpec{
						Steps: []runnersv1alpha1.WorkflowStep{
							{
								Name:  "test-step",
								Image: "busybox:latest",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			workflow := &runnersv1alpha1.Workflow{}
			err := k8sClient.Get(ctx, typeNamespacedName, workflow)
			if err == nil {
				_ = k8sClient.Delete(ctx, workflow)
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &WorkflowReconciler{
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

	Context("Step orchestration", func() {
		It("should create a Runner for each step in a multi-step workflow", func() {
			name := "test-wf-multi"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			wf := &runnersv1alpha1.Workflow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.WorkflowSpec{
					Steps: []runnersv1alpha1.WorkflowStep{
						{Name: "step-1", Image: "busybox:latest"},
						{Name: "step-2", Image: "busybox:latest"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, wf)).To(Succeed())
			defer cleanupWorkflow(ctx, name)

			r := &WorkflowReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Runners were created for each step")
			for _, step := range wf.Spec.Steps {
				runner := &runnersv1alpha1.Runner{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: name + "-" + step.Name, Namespace: "default"}, runner)
				Expect(err).NotTo(HaveOccurred())
				Expect(runner.Spec.Image).To(Equal("busybox:latest"))
			}

			By("Verifying the workflow status has step statuses")
			Expect(k8sClient.Get(ctx, nsName, wf)).To(Succeed())
			Expect(wf.Status.StepStatuses).To(HaveLen(2))
			for _, s := range wf.Status.StepStatuses {
				Expect(s.Phase).To(Equal(runnersv1alpha1.StepPhasePending))
			}
		})

		It("should respect dependsOn ordering by creating Runner only after dependency completes", func() {
			name := "test-wf-deps"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			wf := &runnersv1alpha1.Workflow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.WorkflowSpec{
					Steps: []runnersv1alpha1.WorkflowStep{
						{
							Name:      "step-a",
							Image:     "busybox:latest",
							DependsOn: []string{},
						},
						{
							Name:      "step-b",
							Image:     "busybox:latest",
							DependsOn: []string{"step-a"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, wf)).To(Succeed())
			defer cleanupWorkflow(ctx, name)

			r := &WorkflowReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			By("First reconcile — only step-a should get a Runner (step-b is waiting)")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			// step-a gets a Runner
			runnerA := &runnersv1alpha1.Runner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-step-a", Namespace: "default"}, runnerA)).To(Succeed())

			// step-b has no Runner yet
			runnerB := &runnersv1alpha1.Runner{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: name + "-step-b", Namespace: "default"}, runnerB)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("Simulating step-a completion by updating its Runner status")
			runnerA.Status.Phase = runnersv1alpha1.RunnerPhaseSucceeded
			Expect(k8sClient.Status().Update(ctx, runnerA)).To(Succeed())

			By("Second reconcile — step-b should now get a Runner")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-step-b", Namespace: "default"}, runnerB)).To(Succeed())
			Expect(runnerB.Spec.Image).To(Equal("busybox:latest"))
		})

		It("should skip a step when when=on_failure and all dependencies succeeded", func() {
			name := "test-wf-skip"
			nsName := types.NamespacedName{Name: name, Namespace: "default"}

			wf := &runnersv1alpha1.Workflow{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.WorkflowSpec{
					Steps: []runnersv1alpha1.WorkflowStep{
						{
							Name:  "main-step",
							Image: "busybox:latest",
						},
						{
							Name:  "on-fail-step",
							Image: "busybox:latest",
							When:  "on_failure",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, wf)).To(Succeed())
			defer cleanupWorkflow(ctx, name)

			r := &WorkflowReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			By("First reconcile — both steps created (on-fail doesn't need condition yet)")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			// main-step gets a Runner
			runnerMain := &runnersv1alpha1.Runner{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-main-step", Namespace: "default"}, runnerMain)).To(Succeed())

			By("Simulating main-step succeeding")
			runnerMain.Status.Phase = runnersv1alpha1.RunnerPhaseSucceeded
			Expect(k8sClient.Status().Update(ctx, runnerMain)).To(Succeed())

			By("Second reconcile — on-fail-step should be skipped")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nsName, wf)).To(Succeed())
			found := false
			for _, s := range wf.Status.StepStatuses {
				if s.Name == "on-fail-step" {
					Expect(s.Phase).To(Equal(runnersv1alpha1.StepPhaseSkipped))
					found = true
				}
			}
			Expect(found).To(BeTrue())
		})
	})
})

func cleanupWorkflow(ctx context.Context, name string) {
	wf := &runnersv1alpha1.Workflow{}
	nsName := types.NamespacedName{Name: name, Namespace: "default"}
	if err := k8sClient.Get(ctx, nsName, wf); err != nil {
		return
	}
	_ = k8sClient.Delete(ctx, wf)
}
