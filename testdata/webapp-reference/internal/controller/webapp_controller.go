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

// Reference WebApp controller for validating operator-gauntlet. It should pass
// all seven rules. testdata/make-mutants.py derives one broken copy per rule by
// rewriting the regions marked MUTANT below.

package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	benchv1alpha1 "example.com/webapp-operator/api/v1alpha1"
)

const (
	nameLabel      = "app.kubernetes.io/name"
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "webapp-operator"
	containerName  = "app"
)

// WebAppReconciler reconciles a WebApp object
type WebAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// MUTANT:R7 rbac-markers begin
// +kubebuilder:rbac:groups=bench.example.com,resources=webapps,verbs=get;list;watch
// +kubebuilder:rbac:groups=bench.example.com,resources=webapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// MUTANT:R7 rbac-markers end

// Reconcile drives the Deployment and Service toward the WebApp spec and reports status.
func (r *WebAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	app := &benchv1alpha1.WebApp{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// MUTANT:R4 finalizer begin
	// MUTANT:R4 finalizer end
	if !app.DeletionTimestamp.IsZero() {
		// Children are garbage-collected through owner references; nothing to clean up.
		return ctrl.Result{}, nil
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace}}
	depOp, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		r.mutateDeployment(app, dep)
		// MUTANT:R5 owner-deployment begin
		return controllerutil.SetControllerReference(app, dep, r.Scheme)
		// MUTANT:R5 owner-deployment end
	})
	if err != nil {
		return ctrl.Result{}, err // conflicts are retried with backoff
	}

	// MUTANT:R1 service begin
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		r.mutateService(app, svc)
		// MUTANT:R5 owner-service begin
		return controllerutil.SetControllerReference(app, svc, r.Scheme)
		// MUTANT:R5 owner-service end
	}); err != nil {
		return ctrl.Result{}, err
	}
	// MUTANT:R1 service end
	_ = depOp
	log.V(1).Info("reconciled children", "deployment", depOp)

	return r.updateStatus(ctx, app, dep)
}

// mutateDeployment sets only the fields this operator owns, so other tools'
// labels and annotations, and API-server defaults, are preserved.
func (r *WebAppReconciler) mutateDeployment(app *benchv1alpha1.WebApp, dep *appsv1.Deployment) {
	// MUTANT:R6 labels begin
	if dep.Labels == nil {
		dep.Labels = map[string]string{}
	}
	dep.Labels[nameLabel] = app.Name
	dep.Labels[managedByLabel] = managedByValue
	// MUTANT:R6 labels end

	replicas := app.Spec.Replicas
	dep.Spec.Replicas = &replicas
	if dep.Spec.Selector == nil { // immutable after creation
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{nameLabel: app.Name}}
	}
	if dep.Spec.Template.Labels == nil {
		dep.Spec.Template.Labels = map[string]string{}
	}
	dep.Spec.Template.Labels[nameLabel] = app.Name

	idx := -1
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == containerName {
			idx = i
		}
	}
	if idx < 0 {
		dep.Spec.Template.Spec.Containers = append(dep.Spec.Template.Spec.Containers, corev1.Container{Name: containerName})
		idx = len(dep.Spec.Template.Spec.Containers) - 1
	}
	c := &dep.Spec.Template.Spec.Containers[idx]
	c.Image = app.Spec.Image
	c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: app.Spec.Port, Protocol: corev1.ProtocolTCP}}
}

func (r *WebAppReconciler) mutateService(app *benchv1alpha1.WebApp, svc *corev1.Service) {
	if svc.Labels == nil {
		svc.Labels = map[string]string{}
	}
	svc.Labels[nameLabel] = app.Name
	svc.Labels[managedByLabel] = managedByValue
	svc.Spec.Selector = map[string]string{nameLabel: app.Name}
	if len(svc.Spec.Ports) == 0 {
		svc.Spec.Ports = []corev1.ServicePort{{}}
	}
	svc.Spec.Ports = svc.Spec.Ports[:1]
	p := &svc.Spec.Ports[0]
	p.Name = "http"
	p.Protocol = corev1.ProtocolTCP
	p.Port = app.Spec.Port
	p.TargetPort = intstr.FromInt32(app.Spec.Port)
}

// updateStatus writes status through the status subresource, and only when it changed.
func (r *WebAppReconciler) updateStatus(ctx context.Context, app *benchv1alpha1.WebApp, dep *appsv1.Deployment) (ctrl.Result, error) {
	st := app.Status.DeepCopy()
	st.AvailableReplicas = dep.Status.AvailableReplicas
	st.URL = fmt.Sprintf("http://%s.%s.svc:%d", app.Name, app.Namespace, app.Spec.Port)
	// MUTANT:R2 observed-generation begin
	st.ObservedGeneration = app.Generation
	// MUTANT:R2 observed-generation end

	ready := metav1.Condition{Type: "Ready", ObservedGeneration: app.Generation}
	if dep.Status.AvailableReplicas == app.Spec.Replicas {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionTrue, "Available", "all replicas are available"
	} else {
		ready.Status, ready.Reason = metav1.ConditionFalse, "Progressing"
		ready.Message = fmt.Sprintf("%d of %d replicas available", dep.Status.AvailableReplicas, app.Spec.Replicas)
	}
	meta.SetStatusCondition(&st.Conditions, ready)

	progressing := metav1.Condition{Type: "Progressing", ObservedGeneration: app.Generation,
		Status: metav1.ConditionFalse, Reason: "Complete", Message: "rollout complete"}
	if dep.Status.ObservedGeneration < dep.Generation || dep.Status.UpdatedReplicas != app.Spec.Replicas ||
		dep.Status.AvailableReplicas != app.Spec.Replicas {
		progressing.Status, progressing.Reason, progressing.Message = metav1.ConditionTrue, "RollingOut", "rollout in progress"
	}
	meta.SetStatusCondition(&st.Conditions, progressing)

	// MUTANT:R3 status-write begin
	if equality.Semantic.DeepEqual(app.Status, *st) {
		return ctrl.Result{}, nil
	}
	app.Status = *st
	return ctrl.Result{}, r.Status().Update(ctx, app)
	// MUTANT:R3 status-write end
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&benchv1alpha1.WebApp{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("webapp").
		Complete(r)
}
