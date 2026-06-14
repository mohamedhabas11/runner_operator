/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
	"github.com/mohamedhabas11/runner_operator/internal/webhook/events"
)

var _ = Describe("EventTrigger Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		eventtrigger := &runnersv1alpha1.EventTrigger{}
		var webhookSrv *events.Server

		BeforeEach(func() {
			webhookSrv = events.NewServer(k8sClient, k8sClient.Scheme(), "0")

			By("creating the custom resource for the Kind EventTrigger")
			err := k8sClient.Get(ctx, typeNamespacedName, eventtrigger)
			if err != nil && errors.IsNotFound(err) {
				resource := &runnersv1alpha1.EventTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: runnersv1alpha1.EventTriggerSpec{
						Webhook: &runnersv1alpha1.WebhookConfig{
							Path: "/test-webhook",
						},
						WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
							Name: "test-template",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &runnersv1alpha1.EventTrigger{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance EventTrigger")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &EventTriggerReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Recorder:   record.NewFakeRecorder(10),
				WebhookSrv: webhookSrv,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the resource was updated")
			Expect(k8sClient.Get(ctx, typeNamespacedName, eventtrigger)).To(Succeed())
			Expect(eventtrigger.Status.Registered).To(BeTrue())

			By("Reconciling again to verify idempotency")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When a webhook path collides", func() {
		const firstTriggerName = "trigger-first"
		const secondTriggerName = "trigger-second"
		const collidingPath = "/colliding-path"

		ctx := context.Background()

		firstNamespacedName := types.NamespacedName{
			Name:      firstTriggerName,
			Namespace: "default",
		}
		secondNamespacedName := types.NamespacedName{
			Name:      secondTriggerName,
			Namespace: "default",
		}

		firstTrigger := &runnersv1alpha1.EventTrigger{}
		secondTrigger := &runnersv1alpha1.EventTrigger{}

		var webhookSrv *events.Server
		var reconciler *EventTriggerReconciler

		BeforeEach(func() {
			webhookSrv = events.NewServer(k8sClient, k8sClient.Scheme(), "0")
			reconciler = &EventTriggerReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Recorder:   record.NewFakeRecorder(10),
				WebhookSrv: webhookSrv,
			}

			first := &runnersv1alpha1.EventTrigger{
				ObjectMeta: metav1.ObjectMeta{
					Name:      firstTriggerName,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.EventTriggerSpec{
					Webhook: &runnersv1alpha1.WebhookConfig{
						Path: collidingPath,
					},
					WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
						Name: "test-template",
					},
				},
			}
			By("Creating the first EventTrigger")
			Expect(k8sClient.Create(ctx, first)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: firstNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying first trigger registered successfully")
			Expect(k8sClient.Get(ctx, firstNamespacedName, firstTrigger)).To(Succeed())
			Expect(firstTrigger.Status.Registered).To(BeTrue())

			second := &runnersv1alpha1.EventTrigger{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secondTriggerName,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.EventTriggerSpec{
					Webhook: &runnersv1alpha1.WebhookConfig{
						Path: collidingPath,
					},
					WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
						Name: "test-template",
					},
				},
			}
			By("Creating the second EventTrigger with the same path")
			Expect(k8sClient.Create(ctx, second)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: secondNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			By("Cleaning up both EventTriggers")
			for _, nn := range []types.NamespacedName{firstNamespacedName, secondNamespacedName} {
				resource := &runnersv1alpha1.EventTrigger{}
				err := k8sClient.Get(ctx, nn, resource)
				if err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			}
		})

		It("should set path collision condition on the second trigger", func() {
			By("Checking the second trigger has path collision condition")
			Expect(k8sClient.Get(ctx, secondNamespacedName, secondTrigger)).To(Succeed())
			Expect(secondTrigger.Status.Registered).To(BeFalse())
			Expect(secondTrigger.Status.LastError).To(ContainSubstring("already in use"))

			cond := findCondition(secondTrigger.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("PathCollision"))
		})

		It("should not affect the first trigger's registered status", func() {
			By("Checking the first trigger is still registered")
			Expect(k8sClient.Get(ctx, firstNamespacedName, firstTrigger)).To(Succeed())
			Expect(firstTrigger.Status.Registered).To(BeTrue())

			cond := findCondition(firstTrigger.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When a secret referenced by an EventTrigger changes", func() {
		const triggerName = "trigger-with-secret"
		const secretName = "webhook-hmac-secret"
		const triggerPath = "/secret-trigger"

		ctx := context.Background()
		triggerNamespacedName := types.NamespacedName{
			Name:      triggerName,
			Namespace: "default",
		}

		var reconciler *EventTriggerReconciler

		BeforeEach(func() {
			reconciler = &EventTriggerReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
				},
				StringData: map[string]string{
					"hmac-key": "test-secret-value",
				},
			}
			By("Creating the HMAC secret")
			err := k8sClient.Create(ctx, secret)
			Expect(err).To(Succeed())

			trigger := &runnersv1alpha1.EventTrigger{
				ObjectMeta: metav1.ObjectMeta{
					Name:      triggerName,
					Namespace: "default",
				},
				Spec: runnersv1alpha1.EventTriggerSpec{
					Webhook: &runnersv1alpha1.WebhookConfig{
						Path: triggerPath,
						SecretRef: &corev1.SecretReference{
							Name:      secretName,
							Namespace: "default",
						},
					},
					WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
						Name: "test-template",
					},
				},
			}
			By("Creating the EventTrigger that references the secret")
			Expect(k8sClient.Create(ctx, trigger)).To(Succeed())
		})

		AfterEach(func() {
			for _, nn := range []types.NamespacedName{
				{Name: secretName, Namespace: "default"},
				triggerNamespacedName,
			} {
				obj := client.Object(nil)
				if nn.Name == secretName {
					obj = &corev1.Secret{}
				} else {
					obj = &runnersv1alpha1.EventTrigger{}
				}
				err := k8sClient.Get(ctx, nn, obj)
				if err == nil {
					Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
				}
			}
		})

		It("should return a reconcile request for the referencing trigger", func() {
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)).To(Succeed())

			requests := reconciler.mapSecretToTriggers(ctx, secret)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName).To(Equal(triggerNamespacedName))
		})

		It("should return empty for an unrelated secret", func() {
			unrelated := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unrelated-secret",
					Namespace: "default",
				},
			}
			Expect(k8sClient.Create(ctx, unrelated)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, unrelated)).To(Succeed())
			})

			requests := reconciler.mapSecretToTriggers(ctx, unrelated)
			Expect(requests).To(BeEmpty())
		})

		It("should return empty for a non-Secret object", func() {
			requests := reconciler.mapSecretToTriggers(ctx, &runnersv1alpha1.EventTrigger{})
			Expect(requests).To(BeEmpty())
		})

		It("should return empty when secret name matches but namespace differs", func() {
			secret := &corev1.Secret{}
			secret.Name = secretName
			secret.Namespace = "other-ns"
			requests := reconciler.mapSecretToTriggers(ctx, secret)
			Expect(requests).To(BeEmpty())
		})

		It("should skip triggers without a webhook config", func() {
			noWebhookTrigger := &runnersv1alpha1.EventTrigger{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-webhook-trigger",
					Namespace: "default",
				},
				Spec: runnersv1alpha1.EventTriggerSpec{
					WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
						Name: "test-template",
					},
				},
			}
			Expect(k8sClient.Create(ctx, noWebhookTrigger)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, noWebhookTrigger)).To(Succeed())
			})

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)).To(Succeed())

			requests := reconciler.mapSecretToTriggers(ctx, secret)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName).To(Equal(triggerNamespacedName))
		})

		It("should match triggers with empty namespace in SecretRef", func() {
			By("Creating a trigger with empty secretRef namespace (same namespace as trigger)")
			sameNsTrigger := &runnersv1alpha1.EventTrigger{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "same-ns-trigger",
					Namespace: "default",
				},
				Spec: runnersv1alpha1.EventTriggerSpec{
					Webhook: &runnersv1alpha1.WebhookConfig{
						Path: "/same-ns-path",
						SecretRef: &corev1.SecretReference{
							Name: secretName,
						},
					},
					WorkflowTemplate: runnersv1alpha1.WorkflowTemplateRef{
						Name: "test-template",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sameNsTrigger)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, sameNsTrigger)).To(Succeed())
			})

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)).To(Succeed())

			requests := reconciler.mapSecretToTriggers(ctx, secret)
			Expect(requests).To(HaveLen(2))
		})
	})
})

// findCondition returns the condition with the given type, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
