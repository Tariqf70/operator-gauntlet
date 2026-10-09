// Package checks holds the per-spec fixtures and the rule checks R1–R7.
//
// NOTE: like pkg/harness, this package has not been compiled yet (no module
// downloads were possible where it was written). Run `go mod tidy && go vet ./...`.
package checks

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Tariqf70/operator-gauntlet/pkg/fakecloud"
	"github.com/Tariqf70/operator-gauntlet/pkg/harness"
)

// Fixture knows how to exercise and judge one spec.
type Fixture interface {
	// Prepare creates prerequisites for a scenario and returns the CR to create.
	// opts may carry scenario-specific settings (e.g. "deletionPolicy": "Retain").
	Prepare(ctx context.Context, e *harness.Env, scenario string, opts map[string]string) (*unstructured.Unstructured, error)
	// Converged returns nil when cluster (and cloud) state matches cr's current spec.
	Converged(ctx context.Context, e *harness.Env, key types.NamespacedName) error
	// Mutate changes cr.spec so that metadata.generation increases.
	Mutate(cr *unstructured.Unstructured)
	// Children lists the objects the operator must create and own for cr.
	Children(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) ([]harness.WriterTarget, error)
	// FirstChildExists is used by the first-child-create crash point.
	FirstChildExists(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) (bool, error)
	// AfterDelete checks external state once cr is gone.
	AfterDelete(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) error
}

// ForSpec returns the fixture for a contract's spec name.
func ForSpec(name string) (Fixture, error) {
	switch name {
	case "manageddatabase":
		return &managedDatabase{baseline: map[string]map[string]bool{}}, nil
	case "configsync":
		return &configSync{}, nil
	case "webapp":
		return &webApp{}, nil
	}
	return nil, fmt.Errorf("no fixture for spec %q", name)
}

func ensureNS(ctx context.Context, c client.Client, name string, lbl map[string]string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbl}}
	err := c.Create(ctx, ns)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func readyTrue(u *unstructured.Unstructured) error {
	st, reason, _, ok := harness.ReadyCondition(u)
	if !ok {
		return fmt.Errorf("no Ready condition")
	}
	if st != "True" {
		return fmt.Errorf("Ready=%s (reason %q)", st, reason)
	}
	return nil
}

// ---- ManagedDatabase -----------------------------------------------------

type managedDatabase struct {
	// baseline[namespace] = cloud DB IDs that existed before the scenario started,
	// so "exactly one database" ignores databases retained by earlier scenarios.
	baseline map[string]map[string]bool
}

func (f *managedDatabase) Prepare(ctx context.Context, e *harness.Env, scenario string, opts map[string]string) (*unstructured.Unstructured, error) {
	ns := "gauntlet-md-" + scenario
	if err := ensureNS(ctx, e.Admin, ns, nil); err != nil {
		return nil, err
	}
	base := map[string]bool{}
	for _, db := range e.Cloud.List() {
		base[db.ID] = true
	}
	f.baseline[ns] = base
	policy := opts["deletionPolicy"]
	if policy == "" {
		policy = "Delete"
	}
	return e.NewCR(ns, "orders", map[string]any{
		"engine": "postgres", "sizeGB": int64(20), "deletionPolicy": policy,
	}), nil
}

// newLive returns non-deleting databases created since the scenario's baseline.
func (f *managedDatabase) newLive(e *harness.Env, ns string) []fakecloud.Database {
	var out []fakecloud.Database
	for _, db := range e.Cloud.List() {
		if f.baseline[ns][db.ID] || db.Status == fakecloud.StatusDeleting {
			continue
		}
		out = append(out, db)
	}
	return out
}

func (f *managedDatabase) Converged(ctx context.Context, e *harness.Env, key types.NamespacedName) error {
	cr, err := e.GetCR(ctx, key)
	if err != nil {
		return err
	}
	live := f.newLive(e, key.Namespace)
	if len(live) != 1 {
		names := []string{}
		for _, db := range live {
			names = append(names, db.ID+"("+db.Name+")")
		}
		return fmt.Errorf("want exactly 1 cloud database for this ManagedDatabase, found %d: %v", len(live), names)
	}
	db := live[0]
	id, _, _ := unstructured.NestedString(cr.Object, "status", "databaseID")
	if id != db.ID {
		return fmt.Errorf("status.databaseID=%q, cloud database is %q", id, db.ID)
	}
	size, _, _ := unstructured.NestedInt64(cr.Object, "spec", "sizeGB")
	if db.Status != fakecloud.StatusAvailable || int64(db.SizeGB) != size {
		return fmt.Errorf("cloud database %s is %s at %dGB, spec wants %dGB", db.ID, db.Status, db.SizeGB, size)
	}
	sec := &corev1.Secret{}
	if err := e.Admin.Get(ctx, types.NamespacedName{Namespace: key.Namespace, Name: key.Name + "-conn"}, sec); err != nil {
		return fmt.Errorf("secret %s-conn: %w", key.Name, err)
	}
	if string(sec.Data["endpoint"]) != db.Endpoint || string(sec.Data["username"]) != db.Username {
		return fmt.Errorf("secret endpoint/username don't match the cloud database")
	}
	if !e.Cloud.Verify(db.ID, string(sec.Data["password"])) {
		return fmt.Errorf("secret password is not valid for %s (lost after a crash?)", db.ID)
	}
	if err := harness.ObservedGenerationCurrent(cr); err != nil {
		return err
	}
	return readyTrue(cr)
}

func (f *managedDatabase) Mutate(cr *unstructured.Unstructured) {
	size, _, _ := unstructured.NestedInt64(cr.Object, "spec", "sizeGB")
	next := int64(30)
	if size == 30 {
		next = 20
	}
	_ = unstructured.SetNestedField(cr.Object, next, "spec", "sizeGB")
}

func (f *managedDatabase) Children(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) ([]harness.WriterTarget, error) {
	return []harness.WriterTarget{{
		GVK: corev1.SchemeGroupVersion.WithKind("Secret"),
		Key: types.NamespacedName{Namespace: cr.GetNamespace(), Name: cr.GetName() + "-conn"},
	}}, nil
}

func (f *managedDatabase) FirstChildExists(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) (bool, error) {
	return len(f.newLive(e, cr.GetNamespace())) > 0, nil
}

func (f *managedDatabase) AfterDelete(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) error {
	policy, _, _ := unstructured.NestedString(cr.Object, "spec", "deletionPolicy")
	id, _, _ := unstructured.NestedString(cr.Object, "status", "databaseID")
	return harness.Eventually(ctx, e.Cloud.DeleteDuration+15*time.Second, func(context.Context) error {
		live := f.newLive(e, cr.GetNamespace())
		switch policy {
		case "Retain":
			for _, db := range live {
				if db.ID == id {
					return nil
				}
			}
			return fmt.Errorf("deletionPolicy Retain, but cloud database %q is gone or deleting", id)
		default:
			if len(live) != 0 {
				return fmt.Errorf("deletionPolicy Delete, but %d cloud database(s) remain (orphaned)", len(live))
			}
			return nil
		}
	})
}

// ---- ConfigSync ----------------------------------------------------------

type configSync struct{}

const csSourceName = "ca-bundle"

func (f *configSync) Prepare(ctx context.Context, e *harness.Env, scenario string, _ map[string]string) (*unstructured.Unstructured, error) {
	src := "gauntlet-cs-src-" + scenario
	if err := ensureNS(ctx, e.Admin, src, map[string]string{"gauntlet-scenario": scenario, "tier": "prod"}); err != nil {
		return nil, err
	}
	for suffix, tier := range map[string]string{"a": "prod", "b": "prod", "c": "dev"} {
		if err := ensureNS(ctx, e.Admin, "gauntlet-cs-"+scenario+"-"+suffix, map[string]string{"gauntlet-scenario": scenario, "tier": tier}); err != nil {
			return nil, err
		}
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: src, Name: csSourceName},
		Data:       map[string]string{"ca.crt": "-----BEGIN CERTIFICATE-----\nMIIgauntlet\n-----END CERTIFICATE-----\n"},
	}
	if err := e.Admin.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return e.NewCR("", "cs-"+scenario, map[string]any{
		"source": map[string]any{"namespace": src, "name": csSourceName},
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{
			"gauntlet-scenario": scenario, "tier": "prod",
		}},
	}), nil
}

func (f *configSync) expected(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) (want []string, scenarioNS []string, err error) {
	selMap, _, _ := unstructured.NestedMap(cr.Object, "spec", "namespaceSelector")
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
		return nil, nil, err
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return nil, nil, err
	}
	srcNS, _, _ := unstructured.NestedString(cr.Object, "spec", "source", "namespace")
	all := &corev1.NamespaceList{}
	if err := e.Admin.List(ctx, all); err != nil {
		return nil, nil, err
	}
	scenario := ls.MatchLabels["gauntlet-scenario"]
	for _, ns := range all.Items {
		if ns.Labels["gauntlet-scenario"] == scenario {
			scenarioNS = append(scenarioNS, ns.Name)
		}
		if ns.Name != srcNS && sel.Matches(labels.Set(ns.Labels)) && ns.DeletionTimestamp == nil {
			want = append(want, ns.Name)
		}
	}
	sort.Strings(want)
	return want, scenarioNS, nil
}

func (f *configSync) Converged(ctx context.Context, e *harness.Env, key types.NamespacedName) error {
	cr, err := e.GetCR(ctx, key)
	if err != nil {
		return err
	}
	want, scenarioNS, err := f.expected(ctx, e, cr)
	if err != nil {
		return err
	}
	srcNS, _, _ := unstructured.NestedString(cr.Object, "spec", "source", "namespace")
	src := &corev1.ConfigMap{}
	if err := e.Admin.Get(ctx, types.NamespacedName{Namespace: srcNS, Name: csSourceName}, src); err != nil {
		return err
	}
	wantSet := map[string]bool{}
	for _, ns := range want {
		wantSet[ns] = true
	}
	for _, ns := range scenarioNS {
		if ns == srcNS {
			continue
		}
		cm := &corev1.ConfigMap{}
		err := e.Admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: csSourceName}, cm)
		switch {
		case wantSet[ns] && err != nil:
			return fmt.Errorf("namespace %s: copy missing: %v", ns, err)
		case wantSet[ns]:
			if !reflect.DeepEqual(cm.Data, src.Data) || !reflect.DeepEqual(cm.BinaryData, src.BinaryData) {
				return fmt.Errorf("namespace %s: copy is out of date", ns)
			}
			if cm.Labels["bench.example.com/managed-by"] != cr.GetName() {
				return fmt.Errorf("namespace %s: copy missing managed-by label", ns)
			}
		case err == nil && cm.DeletionTimestamp == nil:
			return fmt.Errorf("namespace %s no longer matches, but still has a copy", ns)
		}
	}
	got, _, _ := unstructured.NestedStringSlice(cr.Object, "status", "syncedNamespaces")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("status.syncedNamespaces=%v, want %v", got, want)
	}
	if err := harness.ObservedGenerationCurrent(cr); err != nil {
		return err
	}
	return readyTrue(cr)
}

func (f *configSync) Mutate(cr *unstructured.Unstructured) {
	tier, _, _ := unstructured.NestedString(cr.Object, "spec", "namespaceSelector", "matchLabels", "tier")
	next := "dev"
	if tier == "dev" {
		next = "prod"
	}
	_ = unstructured.SetNestedField(cr.Object, next, "spec", "namespaceSelector", "matchLabels", "tier")
}

func (f *configSync) Children(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) ([]harness.WriterTarget, error) {
	want, _, err := f.expected(ctx, e, cr)
	if err != nil {
		return nil, err
	}
	var out []harness.WriterTarget
	for _, ns := range want {
		out = append(out, harness.WriterTarget{
			GVK: corev1.SchemeGroupVersion.WithKind("ConfigMap"),
			Key: types.NamespacedName{Namespace: ns, Name: csSourceName},
		})
	}
	return out, nil
}

func (f *configSync) FirstChildExists(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) (bool, error) {
	list := &corev1.ConfigMapList{}
	if err := e.Admin.List(ctx, list, client.MatchingLabels{"bench.example.com/managed-by": cr.GetName()}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

func (f *configSync) AfterDelete(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) error {
	if e.Opts.Mode != harness.ModeKind {
		return nil // envtest has no garbage collector
	}
	return harness.Eventually(ctx, 30*time.Second, func(ctx context.Context) error {
		list := &corev1.ConfigMapList{}
		if err := e.Admin.List(ctx, list, client.MatchingLabels{"bench.example.com/managed-by": cr.GetName()}); err != nil {
			return err
		}
		if n := len(list.Items); n > 0 {
			return fmt.Errorf("%d copies remain after the ConfigSync was deleted", n)
		}
		return nil
	})
}

// ---- WebApp --------------------------------------------------------------

type webApp struct{}

func (f *webApp) Prepare(ctx context.Context, e *harness.Env, scenario string, _ map[string]string) (*unstructured.Unstructured, error) {
	ns := "gauntlet-wa-" + scenario
	if err := ensureNS(ctx, e.Admin, ns, nil); err != nil {
		return nil, err
	}
	return e.NewCR(ns, "storefront", map[string]any{
		"image": "registry.k8s.io/pause:3.10", "replicas": int64(2), "port": int64(8080),
	}), nil
}

// simulate stands in for the Deployment controller in envtest.
func (f *webApp) simulate(ctx context.Context, e *harness.Env, dep *appsv1.Deployment) error {
	if e.Opts.Mode == harness.ModeKind {
		return nil
	}
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	s := dep.Status
	if s.ObservedGeneration == dep.Generation && s.AvailableReplicas == want && s.Replicas == want {
		return nil
	}
	dep.Status.ObservedGeneration = dep.Generation
	dep.Status.Replicas, dep.Status.UpdatedReplicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas = want, want, want, want
	return e.Admin.Status().Update(ctx, dep)
}

func (f *webApp) Converged(ctx context.Context, e *harness.Env, key types.NamespacedName) error {
	cr, err := e.GetCR(ctx, key)
	if err != nil {
		return err
	}
	image, _, _ := unstructured.NestedString(cr.Object, "spec", "image")
	replicas, _, _ := unstructured.NestedInt64(cr.Object, "spec", "replicas")
	port, _, _ := unstructured.NestedInt64(cr.Object, "spec", "port")

	dep := &appsv1.Deployment{}
	if err := e.Admin.Get(ctx, key, dep); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	if err := f.simulate(ctx, e, dep); err != nil {
		return fmt.Errorf("simulating deployment status: %w", err)
	}
	if dep.Labels["app.kubernetes.io/name"] != key.Name || dep.Labels["app.kubernetes.io/managed-by"] != "webapp-operator" {
		return fmt.Errorf("deployment labels %v missing app.kubernetes.io/name or managed-by", dep.Labels)
	}
	if dep.Spec.Replicas == nil || int64(*dep.Spec.Replicas) != replicas {
		return fmt.Errorf("deployment replicas != %d", replicas)
	}
	cs := dep.Spec.Template.Spec.Containers
	if len(cs) == 0 || cs[0].Image != image {
		return fmt.Errorf("deployment image != %s", image)
	}
	portOK := false
	for _, p := range cs[0].Ports {
		if int64(p.ContainerPort) == port {
			portOK = true
		}
	}
	if !portOK {
		return fmt.Errorf("container port %d not exposed", port)
	}
	svc := &corev1.Service{}
	if err := e.Admin.Get(ctx, key, svc); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	if len(svc.Spec.Ports) == 0 || int64(svc.Spec.Ports[0].Port) != port {
		return fmt.Errorf("service port != %d", port)
	}
	avail, _, _ := unstructured.NestedInt64(cr.Object, "status", "availableReplicas")
	if avail != replicas {
		return fmt.Errorf("status.availableReplicas=%d, want %d", avail, replicas)
	}
	url, _, _ := unstructured.NestedString(cr.Object, "status", "url")
	if want := fmt.Sprintf("http://%s.%s.svc:%d", key.Name, key.Namespace, port); url != want {
		return fmt.Errorf("status.url=%q, want %q", url, want)
	}
	if err := harness.ObservedGenerationCurrent(cr); err != nil {
		return err
	}
	return readyTrue(cr)
}

func (f *webApp) Mutate(cr *unstructured.Unstructured) {
	r, _, _ := unstructured.NestedInt64(cr.Object, "spec", "replicas")
	next, img := int64(3), "registry.k8s.io/pause:3.9"
	if r == 3 {
		next, img = 2, "registry.k8s.io/pause:3.10"
	}
	_ = unstructured.SetNestedField(cr.Object, next, "spec", "replicas")
	_ = unstructured.SetNestedField(cr.Object, img, "spec", "image")
}

func (f *webApp) Children(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) ([]harness.WriterTarget, error) {
	key := types.NamespacedName{Namespace: cr.GetNamespace(), Name: cr.GetName()}
	return []harness.WriterTarget{
		{GVK: appsv1.SchemeGroupVersion.WithKind("Deployment"), Key: key},
		{GVK: corev1.SchemeGroupVersion.WithKind("Service"), Key: key},
	}, nil
}

func (f *webApp) FirstChildExists(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) (bool, error) {
	key := types.NamespacedName{Namespace: cr.GetNamespace(), Name: cr.GetName()}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		if err := e.Admin.Get(ctx, key, obj); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (f *webApp) AfterDelete(ctx context.Context, e *harness.Env, cr *unstructured.Unstructured) error {
	if e.Opts.Mode != harness.ModeKind {
		return nil
	}
	key := types.NamespacedName{Namespace: cr.GetNamespace(), Name: cr.GetName()}
	return harness.Eventually(ctx, 30*time.Second, func(ctx context.Context) error {
		for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
			if err := e.Admin.Get(ctx, key, obj); err == nil {
				return fmt.Errorf("%T %s still exists after the WebApp was deleted", obj, key)
			}
		}
		return nil
	})
}
