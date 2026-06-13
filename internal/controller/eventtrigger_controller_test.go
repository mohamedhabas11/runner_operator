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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
		webhookSrv := events.NewServer(k8sClient, "0")

		BeforeEach(func() {
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
})
