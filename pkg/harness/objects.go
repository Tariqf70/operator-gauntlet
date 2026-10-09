package harness

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewCR returns an empty custom resource of the contract's kind.
func (e *Env) NewCR(namespace, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(e.GVK)
	u.SetName(name)
	if e.Opts.Contract.API.Namespaced {
		u.SetNamespace(namespace)
	}
	if spec != nil {
		u.Object["spec"] = spec
	}
	return u
}

// GetCR re-reads a custom resource.
func (e *Env) GetCR(ctx context.Context, key types.NamespacedName) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(e.GVK)
	return u, e.Admin.Get(ctx, key, u)
}

// Eventually polls fn every 250ms until it returns nil or timeout passes; it
// returns the last error from fn on timeout.
func Eventually(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	var last error
	err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, timeout, true, func(pctx context.Context) (bool, error) {
		e := fn(pctx)
		if e == nil {
			last = nil
			return true, nil
		}
		// Keep the last real reason, not the deadline error from the final attempt.
		if pctx.Err() == nil || last == nil {
			last = e
		}
		return false, nil
	})
	if err != nil && last != nil {
		return last
	}
	return err
}

// WaitGone waits until obj no longer exists.
func (e *Env) WaitGone(ctx context.Context, obj client.Object, timeout time.Duration) error {
	return Eventually(ctx, timeout, func(ctx context.Context) error {
		err := e.Admin.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("%s still exists (finalizers %v, deletionTimestamp %v)", obj.GetName(), obj.GetFinalizers(), obj.GetDeletionTimestamp())
	})
}

// ReadyCondition returns the Ready condition from an unstructured status.
func ReadyCondition(u *unstructured.Unstructured) (status, reason string, lastTransition string, ok bool) {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return "", "", "", false
	}
	for _, c := range conds {
		m, isMap := c.(map[string]any)
		if !isMap || m["type"] != "Ready" {
			continue
		}
		s, _ := m["status"].(string)
		r, _ := m["reason"].(string)
		t, _ := m["lastTransitionTime"].(string)
		return s, r, t, true
	}
	return "", "", "", false
}

// ObservedGenerationCurrent reports whether status.observedGeneration == metadata.generation.
func ObservedGenerationCurrent(u *unstructured.Unstructured) error {
	og, found, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err != nil || !found {
		return fmt.Errorf("status.observedGeneration not set")
	}
	if og != u.GetGeneration() {
		return fmt.Errorf("status.observedGeneration=%d, metadata.generation=%d", og, u.GetGeneration())
	}
	return nil
}

// ControllerRefTo reports whether obj has a controller owner reference to owner.
func ControllerRefTo(obj metav1.Object, owner metav1.Object) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

// ---- Concurrent writer (R6) ----------------------------------------------

// WriterTarget is an object the concurrent writer touches.
type WriterTarget struct {
	GVK schema.GroupVersionKind
	Key types.NamespacedName
}

// TouchAnnotation is the annotation the concurrent writer increments.
const TouchAnnotation = "other-tool.example.com/touch"

// ForeignLabel is a label "another tool" adds; the operator must preserve it.
const ForeignLabel = "other-tool.example.com/team"

// Writer bumps resourceVersion on targets by rewriting an annotation, simulating
// another controller. It records the last value it wrote per target.
type Writer struct {
	c       client.Client
	targets []WriterTarget
	period  time.Duration

	mu     sync.Mutex
	last   map[WriterTarget]string
	uid    map[WriterTarget]types.UID
	errors int
	writes int
}

// NewWriter returns a writer that updates each target every period.
func NewWriter(c client.Client, period time.Duration, targets ...WriterTarget) *Writer {
	return &Writer{c: c, targets: targets, period: period, last: map[WriterTarget]string{}, uid: map[WriterTarget]types.UID{}}
}

// Run writes until ctx is done.
func (w *Writer) Run(ctx context.Context) {
	n := 0
	t := time.NewTicker(w.period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n++
		for _, tgt := range w.targets {
			val := strconv.Itoa(n)
			// Each write gets its own context, so stopping the writer never cancels a
			// request the server may already have applied (which would desync `last`).
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			var uid types.UID
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				u := &unstructured.Unstructured{}
				u.SetGroupVersionKind(tgt.GVK)
				if err := w.c.Get(wctx, tgt.Key, u); err != nil {
					return err
				}
				uid = u.GetUID()
				ann := u.GetAnnotations()
				if ann == nil {
					ann = map[string]string{}
				}
				ann[TouchAnnotation] = val
				u.SetAnnotations(ann)
				lbl := u.GetLabels()
				if lbl == nil {
					lbl = map[string]string{}
				}
				lbl[ForeignLabel] = "payments"
				u.SetLabels(lbl)
				return w.c.Update(wctx, u)
			})
			cancel()
			w.mu.Lock()
			if err != nil {
				w.errors++
			} else {
				w.writes++
				w.last[tgt] = val
				w.uid[tgt] = uid
			}
			w.mu.Unlock()
		}
	}
}

// Stats returns successful writes and errors.
func (w *Writer) Stats() (writes, errors int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes, w.errors
}

// Verify checks that each target still carries the writer's last annotation and
// label. Only targets in expected are checked (children can legitimately go away
// after a spec change), and a target that was deleted and recreated is skipped.
func (w *Writer) Verify(ctx context.Context, expected []WriterTarget) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	exp := map[WriterTarget]bool{}
	for _, t := range expected {
		exp[t] = true
	}
	var lost []string
	for _, tgt := range w.targets {
		want, ok := w.last[tgt]
		if !ok || !exp[tgt] {
			continue
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(tgt.GVK)
		if err := w.c.Get(ctx, tgt.Key, u); err != nil {
			lost = append(lost, fmt.Sprintf("%s %s: %v", tgt.GVK.Kind, tgt.Key, err))
			continue
		}
		if u.GetUID() != w.uid[tgt] {
			continue // recreated: not a lost update
		}
		if got := u.GetAnnotations()[TouchAnnotation]; got != want {
			lost = append(lost, fmt.Sprintf("%s %s: annotation %s=%q, writer last wrote %q (lost update)", tgt.GVK.Kind, tgt.Key, TouchAnnotation, got, want))
		}
		if u.GetLabels()[ForeignLabel] == "" {
			lost = append(lost, fmt.Sprintf("%s %s: label %s was removed", tgt.GVK.Kind, tgt.Key, ForeignLabel))
		}
	}
	return lost
}
