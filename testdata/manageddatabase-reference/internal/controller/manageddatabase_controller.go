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

// Reference ManagedDatabase controller for validating operator-gauntlet.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	benchv1alpha1 "example.com/manageddatabase-operator/api/v1alpha1"
	"example.com/manageddatabase-operator/internal/cloud"
)

const finalizer = "bench.example.com/cloud-database"

// waitCloud is how often to poll the cloud API while it's provisioning, resizing or deleting.
const waitCloud = 2 * time.Second

// ManagedDatabaseReconciler reconciles a ManagedDatabase object
type ManagedDatabaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Cloud  *cloud.Client
}

// +kubebuilder:rbac:groups=bench.example.com,resources=manageddatabases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=bench.example.com,resources=manageddatabases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bench.example.com,resources=manageddatabases/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// cloudName is deterministic, so a crash between "create" and "record it" can be recovered by lookup.
func cloudName(md *benchv1alpha1.ManagedDatabase) string {
	h := sha256.Sum256([]byte(md.UID))
	n := fmt.Sprintf("%s-%s", md.Name, md.Namespace)
	if len(n) > 50 {
		n = n[:50]
	}
	return fmt.Sprintf("%s-%s", n, hex.EncodeToString(h[:])[:8])
}

func secretName(md *benchv1alpha1.ManagedDatabase) string { return md.Name + "-conn" }

// Reconcile moves the cloud database, the Secret and status toward the spec.
func (r *ManagedDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	md := &benchv1alpha1.ManagedDatabase{}
	if err := r.Get(ctx, req.NamespacedName, md); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !md.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, md)
	}
	if controllerutil.AddFinalizer(md, finalizer) {
		return ctrl.Result{}, r.Update(ctx, md) // the update event triggers the next reconcile
	}

	db, password, err := r.ensureDatabase(ctx, md)
	if err != nil {
		return ctrl.Result{}, err
	}
	secretOK, err := r.ensureSecret(ctx, md, db, password)
	if err != nil {
		return ctrl.Result{}, err
	}
	if db.Status == "available" && db.SizeGB != md.Spec.SizeGB {
		if err := r.Cloud.Resize(ctx, db.ID, md.Spec.SizeGB); err != nil && !errors.Is(err, cloud.ErrConflict) {
			return ctrl.Result{}, err
		}
		db.Status = "resizing"
	}
	ready := db.Status == "available" && db.SizeGB == md.Spec.SizeGB && secretOK
	if err := r.updateStatus(ctx, md, db, ready); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: waitCloud}, nil // the cloud can't be watched; poll while it works
	}
	return ctrl.Result{}, nil
}

// ensureDatabase finds the database (by recorded ID, then by deterministic name)
// or creates it. password is non-empty only when it was just created.
func (r *ManagedDatabaseReconciler) ensureDatabase(ctx context.Context, md *benchv1alpha1.ManagedDatabase) (*cloud.Database, string, error) {
	if md.Status.DatabaseID != "" {
		db, err := r.Cloud.Get(ctx, md.Status.DatabaseID)
		if err == nil {
			return db, "", nil
		}
		if !errors.Is(err, cloud.ErrNotFound) {
			return nil, "", err
		}
	}
	name := cloudName(md)
	db, err := r.Cloud.FindByName(ctx, name)
	if err == nil {
		return db, "", nil
	}
	if !errors.Is(err, cloud.ErrNotFound) {
		return nil, "", err
	}
	db, err = r.Cloud.Create(ctx, name, md.Spec.Engine, md.Spec.SizeGB)
	if errors.Is(err, cloud.ErrConflict) { // created by an earlier, interrupted attempt
		db, err = r.Cloud.FindByName(ctx, name)
		if err != nil {
			return nil, "", err
		}
		return db, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return db, db.Password, nil
}

// ensureSecret keeps the connection Secret current. If the password was lost
// (for example a crash right after create), it rotates the password.
func (r *ManagedDatabaseReconciler) ensureSecret(ctx context.Context, md *benchv1alpha1.ManagedDatabase, db *cloud.Database, password string) (bool, error) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName(md), Namespace: md.Namespace}}
	if password == "" {
		err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec)
		switch {
		case apierrors.IsNotFound(err) || (err == nil && len(sec.Data["password"]) == 0):
			if password, err = r.Cloud.ResetPassword(ctx, db.ID); err != nil {
				return false, err
			}
		case err != nil:
			return false, err
		}
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sec, func() error {
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data["endpoint"] = []byte(db.Endpoint)
		sec.Data["username"] = []byte(db.Username)
		if password != "" {
			sec.Data["password"] = []byte(password)
		}
		return controllerutil.SetControllerReference(md, sec, r.Scheme)
	})
	return err == nil, err
}

func (r *ManagedDatabaseReconciler) reconcileDelete(ctx context.Context, md *benchv1alpha1.ManagedDatabase) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(md, finalizer) {
		return ctrl.Result{}, nil
	}
	if md.Spec.DeletionPolicy != "Retain" {
		db, err := r.findExisting(ctx, md)
		switch {
		case errors.Is(err, cloud.ErrNotFound):
			// gone: fall through and release the finalizer
		case err != nil:
			return ctrl.Result{}, err
		default:
			if db.Status != "deleting" {
				if err := r.Cloud.Delete(ctx, db.ID); err != nil && !errors.Is(err, cloud.ErrNotFound) {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{RequeueAfter: waitCloud}, nil
		}
	}
	controllerutil.RemoveFinalizer(md, finalizer)
	return ctrl.Result{}, r.Update(ctx, md)
}

// findExisting looks the database up without creating it.
func (r *ManagedDatabaseReconciler) findExisting(ctx context.Context, md *benchv1alpha1.ManagedDatabase) (*cloud.Database, error) {
	if md.Status.DatabaseID != "" {
		db, err := r.Cloud.Get(ctx, md.Status.DatabaseID)
		if err == nil || !errors.Is(err, cloud.ErrNotFound) {
			return db, err
		}
	}
	return r.Cloud.FindByName(ctx, cloudName(md))
}

func (r *ManagedDatabaseReconciler) updateStatus(ctx context.Context, md *benchv1alpha1.ManagedDatabase, db *cloud.Database, ready bool) error {
	st := md.Status.DeepCopy()
	st.DatabaseID = db.ID
	st.Endpoint = db.Endpoint
	st.ObservedGeneration = md.Generation
	switch db.Status {
	case "available":
		st.Phase = "Available"
	case "deleting":
		st.Phase = "Deleting"
	default:
		st.Phase = "Creating"
	}
	cond := metav1.Condition{Type: "Ready", ObservedGeneration: md.Generation,
		Status: metav1.ConditionFalse, Reason: "Provisioning", Message: "cloud database is " + db.Status}
	if ready {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "Available", "database is available"
	}
	meta.SetStatusCondition(&st.Conditions, cond)
	if equality.Semantic.DeepEqual(md.Status, *st) {
		return nil
	}
	md.Status = *st
	return r.Status().Update(ctx, md)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Cloud == nil {
		c, err := cloud.FromEnv()
		if err != nil {
			return err
		}
		r.Cloud = c
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&benchv1alpha1.ManagedDatabase{}).
		Owns(&corev1.Secret{}).
		Named("manageddatabase").
		Complete(r)
}
