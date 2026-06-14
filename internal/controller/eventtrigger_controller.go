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
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
	"github.com/mohamedhabas11/runner_operator/internal/webhook/events"
)

func setTriggerCondition(trigger *runnersv1alpha1.EventTrigger, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&trigger.Status.Conditions, NewCondition(ConditionTypeReady).
		WithStatus(status).
		WithReason(reason).
		WithMessage(msg).
		WithObservedGeneration(trigger.Generation).
		Build())
}

// EventTriggerReconciler reconciles a EventTrigger object.
type EventTriggerReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	WebhookSrv *events.Server
}

// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=eventtriggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=eventtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runners.runner-operator.io,resources=workflows,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile reconciles an EventTrigger: registers or deregisters webhook routes.
func (r *EventTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	trigger := &runnersv1alpha1.EventTrigger{}
	if err := r.Get(ctx, req.NamespacedName, trigger); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !trigger.DeletionTimestamp.IsZero() {
		if trigger.Spec.Webhook != nil && trigger.Spec.Webhook.Path != "" {
			r.WebhookSrv.DeregisterRoute(trigger.Spec.Webhook.Path)
			logger.Info("Deregistered webhook route", "path", trigger.Spec.Webhook.Path)
		}
		return ctrl.Result{}, nil
	}

	if len(trigger.Spec.AllowedNamespaces) > 0 {
		allowed := slices.Contains(trigger.Spec.AllowedNamespaces, trigger.Namespace)
		if !allowed {
			logger.Info("Trigger namespace not in allowed namespaces", "namespace", trigger.Namespace)
			r.Recorder.Eventf(trigger, corev1.EventTypeWarning, "NamespaceNotAllowed",
				"Trigger namespace %s is not in allowedNamespaces", trigger.Namespace)
			patchBase := client.MergeFrom(trigger.DeepCopy())
			trigger.Status.Registered = false
			trigger.Status.LastError = fmt.Sprintf("namespace %s not in allowedNamespaces", trigger.Namespace)
			setTriggerCondition(trigger, metav1.ConditionFalse, ReasonTriggerNamespaceBlocked,
				"Trigger namespace not in allowedNamespaces")
			if err := r.Status().Patch(ctx, trigger, patchBase); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	// Check for webhook path uniqueness before attempting registration
	if trigger.Spec.Webhook != nil && trigger.Spec.Webhook.Path != "" {
		var allTriggers runnersv1alpha1.EventTriggerList
		if err := r.List(ctx, &allTriggers); err != nil {
			logger.Error(err, "Failed to list EventTriggers for path uniqueness check")
			return ctrl.Result{}, err
		}
		for i := range allTriggers.Items {
			existing := allTriggers.Items[i]
			if existing.UID == trigger.UID {
				continue
			}
			if existing.Spec.Webhook != nil && existing.Spec.Webhook.Path == trigger.Spec.Webhook.Path {
				logger.Info("Webhook path already in use by another trigger", "path", trigger.Spec.Webhook.Path, "existing", existing.Name)
				r.Recorder.Eventf(trigger, corev1.EventTypeWarning, "PathCollision",
					"Webhook path %q already in use by EventTrigger %s", trigger.Spec.Webhook.Path, existing.Name)
				patchBase := client.MergeFrom(trigger.DeepCopy())
				trigger.Status.Registered = false
				trigger.Status.LastError = fmt.Sprintf("webhook path %q already in use by %s", trigger.Spec.Webhook.Path, existing.Name)
				setTriggerCondition(trigger, metav1.ConditionFalse, ReasonTriggerPathCollision,
					"Webhook path already in use")
				if err := r.Status().Patch(ctx, trigger, patchBase); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
		}
	}

	patchBase := client.MergeFrom(trigger.DeepCopy())
	updated := false

	if trigger.Spec.Webhook != nil && trigger.Spec.Webhook.Path != "" {
		if err := r.WebhookSrv.RegisterRoute(ctx, *trigger); err != nil {
			logger.Error(err, "Failed to register webhook route", "path", trigger.Spec.Webhook.Path)
			r.Recorder.Eventf(trigger, corev1.EventTypeWarning, "RouteRegistrationFailed",
				"Failed to register webhook route %q: %v", trigger.Spec.Webhook.Path, err)
			trigger.Status.Registered = false
			trigger.Status.LastError = fmt.Sprintf("route registration failed: %v", err)
			setTriggerCondition(trigger, metav1.ConditionFalse, ReasonTriggerRouteFailed,
				"Failed to register webhook route")
			updated = true
		} else {
			if !trigger.Status.Registered {
				logger.Info("Registered webhook route", "path", trigger.Spec.Webhook.Path)
				r.Recorder.Eventf(trigger, corev1.EventTypeNormal, "RouteRegistered",
					"Webhook route %q registered", trigger.Spec.Webhook.Path)
				trigger.Status.Registered = true
				trigger.Status.LastError = ""
				setTriggerCondition(trigger, metav1.ConditionTrue, ReasonTriggerRouteRegistered,
					"Webhook route registered")
				updated = true
			}
		}
	}

	if updated {
		if err := r.Status().Patch(ctx, trigger, patchBase); err != nil {
			return ctrl.Result{}, err
		}
	}

	if trigger.Spec.Webhook != nil && trigger.Spec.Webhook.Path != "" {
		if !trigger.Status.Registered {
			return ctrl.Result{}, fmt.Errorf("route %q not registered", trigger.Spec.Webhook.Path)
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EventTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("eventtrigger-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&runnersv1alpha1.EventTrigger{}).
		Named("eventtrigger").
		Complete(r)
}
