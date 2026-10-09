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

// Reference ConfigSync controller for validating operator-gauntlet.

package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	benchv1alpha1 "example.com/configsync-operator/api/v1alpha1"
)

const managedByLabel = "bench.example.com/managed-by"

// ConfigSyncReconciler reconciles a ConfigSync object
type ConfigSyncReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=bench.example.com,resources=configsyncs,verbs=get;list;watch
// +kubebuilder:rbac:groups=bench.example.com,resources=configsyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile keeps a copy of the source ConfigMap in every matching namespace.
func (r *ConfigSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cs := &benchv1alpha1.ConfigSync{}
	if err := r.Get(ctx, req.NamespacedName, cs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cs.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // copies are garbage-collected through owner references
	}

	src := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cs.Spec.Source.Namespace, Name: cs.Spec.Source.Name}, src)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.setStatus(ctx, cs, cs.Status.SyncedNamespaces, metav1.ConditionFalse, "SourceNotFound", "source ConfigMap does not exist")
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	sel, err := metav1.LabelSelectorAsSelector(&cs.Spec.NamespaceSelector)
	if err != nil {
		return ctrl.Result{}, r.setStatus(ctx, cs, nil, metav1.ConditionFalse, "InvalidSelector", err.Error())
	}
	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ctrl.Result{}, err
	}
	want := map[string]bool{}
	var synced []string
	for _, ns := range nsList.Items {
		if ns.Name == src.Namespace || !ns.DeletionTimestamp.IsZero() {
			continue
		}
		want[ns.Name] = true
		cp := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: ns.Name}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cp, func() error {
			if cp.Labels == nil {
				cp.Labels = map[string]string{}
			}
			cp.Labels[managedByLabel] = cs.Name
			cp.Data = src.Data
			cp.BinaryData = src.BinaryData
			return controllerutil.SetControllerReference(cs, cp, r.Scheme)
		}); err != nil {
			return ctrl.Result{}, err
		}
		synced = append(synced, ns.Name)
	}
	sort.Strings(synced)

	// Remove copies from namespaces that no longer match.
	copies := &corev1.ConfigMapList{}
	if err := r.List(ctx, copies, client.MatchingLabels{managedByLabel: cs.Name}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range copies.Items {
		cp := &copies.Items[i]
		if !want[cp.Namespace] && metav1.IsControlledBy(cp, cs) {
			if err := r.Delete(ctx, cp); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
	}
	return ctrl.Result{}, r.setStatus(ctx, cs, synced, metav1.ConditionTrue, "Synced", "all matching namespaces are up to date")
}

func (r *ConfigSyncReconciler) setStatus(ctx context.Context, cs *benchv1alpha1.ConfigSync, synced []string, status metav1.ConditionStatus, reason, msg string) error {
	st := cs.Status.DeepCopy()
	st.SyncedNamespaces = synced
	st.ObservedGeneration = cs.Generation
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: msg, ObservedGeneration: cs.Generation,
	})
	if equality.Semantic.DeepEqual(cs.Status, *st) {
		return nil
	}
	cs.Status = *st
	return r.Status().Update(ctx, cs)
}

// enqueueAll maps any event to every ConfigSync (the set is small).
func (r *ConfigSyncReconciler) enqueueAll(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &benchv1alpha1.ConfigSyncList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, cs := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cs.Name}})
	}
	return reqs
}

// enqueueForSource maps a ConfigMap change to the ConfigSyncs that use it as their source.
func (r *ConfigSyncReconciler) enqueueForSource(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &benchv1alpha1.ConfigSyncList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, cs := range list.Items {
		if cs.Spec.Source.Namespace == obj.GetNamespace() && cs.Spec.Source.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cs.Name}})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&benchv1alpha1.ConfigSync{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.enqueueForSource)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll)).
		Named("configsync").
		Complete(r)
}
