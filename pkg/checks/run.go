package checks

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Tariqf70/operator-gauntlet/pkg/audit"
	"github.com/Tariqf70/operator-gauntlet/pkg/contract"
	"github.com/Tariqf70/operator-gauntlet/pkg/fakecloud"
	"github.com/Tariqf70/operator-gauntlet/pkg/harness"
	"github.com/Tariqf70/operator-gauntlet/pkg/rbaccheck"
	"github.com/Tariqf70/operator-gauntlet/pkg/results"
)

// Config tunes a run.
type Config struct {
	VisibleOnly     bool          // setup S4 feedback: skip and omit hidden rules
	ConvergeTimeout time.Duration // default 90s
	QuietWindow     time.Duration // R3 steady-state window, default 60s
	IdleWindow      time.Duration // R2 flapping window when R3 is skipped, default 20s
	WriterDuration  time.Duration // R6 concurrent-writer phase, default 30s
	WriterPeriod    time.Duration // default 200ms (about 5 Hz)
	Log             io.Writer
}

func (c *Config) defaults() {
	if c.ConvergeTimeout == 0 {
		c.ConvergeTimeout = 90 * time.Second
	}
	if c.QuietWindow == 0 {
		c.QuietWindow = 60 * time.Second
	}
	if c.IdleWindow == 0 {
		c.IdleWindow = 20 * time.Second
	}
	if c.WriterDuration == 0 {
		c.WriterDuration = 30 * time.Second
	}
	if c.WriterPeriod == 0 {
		c.WriterPeriod = 200 * time.Millisecond
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
}

type runner struct {
	e       *harness.Env
	fx      Fixture
	cfg     Config
	c       *contract.Contract
	out     map[string]*results.RuleResult
	started time.Time // just before the manager first starts; audit queries begin here
}

func (r *runner) logf(format string, args ...any) {
	fmt.Fprintf(r.cfg.Log, "[gauntlet] "+format+"\n", args...)
}

func (r *runner) rule(id string) *results.RuleResult {
	if rr, ok := r.out[id]; ok {
		return rr
	}
	rr := &results.RuleResult{ID: id, Pass: true, Stated: r.c.Stated(id), Hidden: contract.HiddenRules[id], Metrics: map[string]float64{}}
	r.out[id] = rr
	return rr
}

func (r *runner) fail(id, format string, args ...any) {
	rr := r.rule(id)
	rr.Pass = false
	rr.Details = append(rr.Details, fmt.Sprintf(format, args...))
	r.logf("%s FAIL: "+format, append([]any{id}, args...)...)
}

func (r *runner) note(id, format string, args ...any) {
	rr := r.rule(id)
	rr.Details = append(rr.Details, fmt.Sprintf(format, args...))
}

// notEvaluated fails a rule that couldn't be checked because an earlier phase failed.
func (r *runner) notEvaluated(id, why string) {
	if r.enabled(id) {
		r.fail(id, "not evaluated: %s", why)
	}
}

func (r *runner) enabled(id string) bool { return !(r.cfg.VisibleOnly && contract.HiddenRules[id]) }

func keyOf(u *unstructured.Unstructured) types.NamespacedName {
	return types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}
}

func (r *runner) converge(ctx context.Context, key types.NamespacedName) error {
	return harness.Eventually(ctx, r.cfg.ConvergeTimeout, func(ctx context.Context) error { return r.fx.Converged(ctx, r.e, key) })
}

// updateSpec re-reads the CR, applies Mutate, and updates it (retrying conflicts).
func (r *runner) updateSpec(ctx context.Context, key types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cr, err := r.e.GetCR(ctx, key)
		if err != nil {
			return err
		}
		r.fx.Mutate(cr)
		return r.e.Admin.Update(ctx, cr)
	})
}

// Run executes every enabled rule and returns results in rule order.
func Run(ctx context.Context, e *harness.Env, fx Fixture, cfg Config) []results.RuleResult {
	cfg.defaults()
	r := &runner{e: e, fx: fx, cfg: cfg, c: e.Opts.Contract, out: map[string]*results.RuleResult{}}
	for _, id := range contract.RulesFor(cfg.VisibleOnly) {
		r.rule(id)
	}

	// R7 static: parse and bind RBAC. The manager runs with exactly these rules.
	roles, err := e.BindManagerRBAC(ctx)
	if err != nil {
		r.fail("R7", "could not read or bind RBAC: %v", err)
	} else {
		var chk []rbaccheck.Role
		for _, pr := range roles {
			chk = append(chk, pr.ForCheck())
		}
		for _, f := range rbaccheck.Check(chk, r.c) {
			r.fail("R7", "%s", f)
		}
	}

	r.checkCRDStatusSubresource(ctx)

	main, ok := r.phaseCrash(ctx) // R1
	if ok {
		r.phaseStatus(ctx, main) // R2 (+R3 window)
		if r.enabled("R6") {
			r.phaseConflicts(ctx, main) // R6
		}
		r.phaseOwnership(ctx, main) // R5 (may delete main in kind mode)
	} else {
		for _, id := range []string{"R2", "R3", "R5", "R6"} {
			if r.enabled(id) {
				r.fail(id, "not evaluated: the operator never converged in the crash phase")
			}
		}
	}
	r.phaseDeletion(ctx, main) // R4

	r.auditSummary()

	var out []results.RuleResult
	for _, id := range contract.RulesFor(cfg.VisibleOnly) {
		out = append(out, *r.rule(id))
	}
	return out
}

func (r *runner) checkCRDStatusSubresource(ctx context.Context) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	name := r.c.API.Resource + "." + r.c.API.Group
	if err := r.e.Admin.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
		r.fail("R2", "CRD %s not found: %v", name, err)
		return
	}
	for _, v := range crd.Spec.Versions {
		if v.Name == r.c.API.Version {
			if v.Subresources == nil || v.Subresources.Status == nil {
				r.fail("R2", "CRD version %s has no status subresource", v.Name)
			}
			return
		}
	}
	r.fail("R2", "CRD has no version %s", r.c.API.Version)
}

// phaseCrash creates the main CR and kills the manager at the contract's crash point.
func (r *runner) phaseCrash(ctx context.Context) (*unstructured.Unstructured, bool) {
	cr, err := r.fx.Prepare(ctx, r.e, "main", nil)
	if err != nil {
		r.fail("R1", "fixture setup failed: %v", err)
		return nil, false
	}
	crashed := make(chan struct{})
	var once sync.Once
	fire := func() {
		once.Do(func() {
			r.logf("crash point reached: SIGKILL manager")
			if err := r.e.Manager.Kill(); err != nil {
				r.logf("kill: %v", err)
			}
			close(crashed)
		})
	}

	switch r.c.CrashPoint {
	case contract.CrashFakeCloudCreate:
		r.e.Cloud.SetOnCreate(func(fakecloud.Database) { fire() })
		defer r.e.Cloud.SetOnCreate(nil)
	case contract.CrashFirstChild:
		// The API proxy kills the manager the instant its first create succeeds,
		// before the manager sees the response.
		r.e.API.ArmCreateCrash(func(method, path string) {
			r.logf("first create by the manager: %s %s", method, path)
			fire()
		})
		defer r.e.API.Disarm()
	}

	r.started = time.Now()
	if err := r.e.Manager.Start(); err != nil {
		r.fail("R1", "manager did not start: %v", err)
		return nil, false
	}
	if err := r.e.Admin.Create(ctx, cr); err != nil {
		r.fail("R1", "creating %s: %v", r.c.API.Kind, err)
		return nil, false
	}
	select {
	case <-crashed:
	case <-time.After(r.cfg.ConvergeTimeout):
		r.fail("R1", "crash point %q was never reached (the operator never created anything)", r.c.CrashPoint)
		return cr, false
	}
	if err := r.e.Manager.Start(); err != nil {
		r.fail("R1", "manager did not restart: %v", err)
		return cr, false
	}
	if err := r.converge(ctx, keyOf(cr)); err != nil {
		r.fail("R1", "did not converge after a crash at %q: %v", r.c.CrashPoint, err)
		return cr, false
	}
	if r.e.Cloud != nil {
		r.rule("R1").Metrics["cloudCreateCalls"] = float64(r.e.Cloud.Stats().CreateCalls)
	}
	return cr, true
}

// phaseStatus checks R2 (observedGeneration, stable conditions, status subresource) and R3.
func (r *runner) phaseStatus(ctx context.Context, cr *unstructured.Unstructured) {
	key := keyOf(cr)
	if err := r.updateSpec(ctx, key); err != nil {
		r.fail("R2", "updating spec: %v", err)
		r.notEvaluated("R3", "the spec update failed")
		return
	}
	if err := r.converge(ctx, key); err != nil {
		r.fail("R2", "did not converge after a spec change: %v", err)
		r.notEvaluated("R3", "the operator never converged after a spec change")
		return
	}
	before, _ := r.e.GetCR(ctx, key)
	_, _, t0, _ := harness.ReadyCondition(before)

	window := r.cfg.IdleWindow
	if r.enabled("R3") {
		window = r.cfg.QuietWindow
	}
	start := time.Now()
	r.logf("idle window %v (R2 flapping%s)", window, map[bool]string{true: ", R3 writes", false: ""}[r.enabled("R3")])
	time.Sleep(window)
	end := time.Now()

	after, err := r.e.GetCR(ctx, key)
	if err == nil {
		if _, _, t1, _ := harness.ReadyCondition(after); t1 != t0 {
			r.fail("R2", "Ready lastTransitionTime changed while idle (%s -> %s): the condition is flapping or rewritten every reconcile", t0, t1)
		}
		if after.GetResourceVersion() != before.GetResourceVersion() {
			r.note("R2", "the %s was rewritten while idle", r.c.API.Kind)
		}
	}

	events, err := r.e.AuditEvents()
	if err != nil {
		r.fail("R2", "reading audit log: %v", err)
		r.notEvaluated("R3", "the audit log could not be read")
		return
	}
	split := audit.StatusWrites(events, r.e.ManagerUsername, r.c.API.Resource)
	r.rule("R2").Metrics["statusSubresourceWrites"] = float64(split.Subresource)
	if split.Subresource == 0 {
		r.fail("R2", "no successful writes to %s/status: status must go through the status subresource", r.c.API.Resource)
	}

	if r.enabled("R3") {
		f := audit.Filter{User: r.e.ManagerUsername, Since: start.Add(2 * time.Second), Until: end}
		wpm := audit.WritesPerMinute(events, f)
		rr := r.rule("R3")
		rr.Metrics["writesPerMinute"] = wpm
		rr.Metrics["windowSeconds"] = end.Sub(start).Seconds()
		if wpm > r.c.SteadyStateMaxWritesPerMinute {
			r.fail("R3", "%.1f writes/min while converged and idle (limit %.1f)", wpm, r.c.SteadyStateMaxWritesPerMinute)
			for _, ev := range audit.Select(events, f) {
				if ev.IsWrite() && ev.ObjectRef != nil {
					r.note("R3", "e.g. %s %s/%s %s", ev.Verb, ev.ObjectRef.Resource, ev.ObjectRef.Subresource, ev.ObjectRef.Name)
					break
				}
			}
		}
	}
}

// phaseConflicts runs a concurrent writer against the CR and its children (R6).
func (r *runner) phaseConflicts(ctx context.Context, cr *unstructured.Unstructured) {
	key := keyOf(cr)
	children, err := r.fx.Children(ctx, r.e, cr)
	if err != nil {
		r.fail("R6", "listing children: %v", err)
		return
	}
	targets := append([]harness.WriterTarget{{GVK: r.e.GVK, Key: key}}, children...)
	exitsBefore := len(r.e.Manager.UnexpectedExits())
	start := time.Now()

	wctx, cancel := context.WithTimeout(ctx, r.cfg.WriterDuration)
	w := harness.NewWriter(r.e.Admin, r.cfg.WriterPeriod, targets...)
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	for i := 0; i < 3; i++ { // spec changes while the writer runs
		if err := r.updateSpec(ctx, key); err != nil {
			r.note("R6", "spec update during writer phase: %v", err)
		}
		time.Sleep(r.cfg.WriterDuration / 3)
	}
	cancel()
	<-done

	if err := r.converge(ctx, key); err != nil {
		r.fail("R6", "did not converge after concurrent writes: %v", err)
	}
	if exits := r.e.Manager.UnexpectedExits()[exitsBefore:]; len(exits) > 0 {
		// procctl restarts crashed managers (like kubelet would), so the run continues.
		r.fail("R6", "manager exited %d time(s) under concurrent writes (first exit code %d)", len(exits), exits[0].ExitCode)
	}
	live, err := r.e.GetCR(ctx, key)
	if err != nil {
		r.fail("R6", "re-reading %s: %v", r.c.API.Kind, err)
		return
	}
	expected, err := r.fx.Children(ctx, r.e, live)
	if err != nil {
		r.fail("R6", "listing children: %v", err)
		return
	}
	expected = append(expected, harness.WriterTarget{GVK: r.e.GVK, Key: key})
	for _, lost := range w.Verify(ctx, expected) {
		r.fail("R6", "%s", lost)
	}
	writes, errs := w.Stats()
	rr := r.rule("R6")
	rr.Metrics["writerWrites"], rr.Metrics["writerErrors"] = float64(writes), float64(errs)
	if events, err := r.e.AuditEvents(); err == nil {
		rr.Metrics["managerConflicts409"] = float64(audit.Conflicts(events, audit.Filter{User: r.e.ManagerUsername, Since: start}))
	}
}

// phaseOwnership checks controller owner references, and garbage collection in kind (R5).
func (r *runner) phaseOwnership(ctx context.Context, cr *unstructured.Unstructured) {
	key := keyOf(cr)
	live, err := r.e.GetCR(ctx, key)
	if err != nil {
		r.fail("R5", "re-reading %s: %v", r.c.API.Kind, err)
		return
	}
	children, err := r.fx.Children(ctx, r.e, live)
	if err != nil {
		r.fail("R5", "listing children: %v", err)
		return
	}
	for _, ch := range children {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(ch.GVK)
		if err := r.e.Admin.Get(ctx, ch.Key, u); err != nil {
			r.fail("R5", "%s %s: %v", ch.GVK.Kind, ch.Key, err)
			continue
		}
		if !harness.ControllerRefTo(u, live) {
			r.fail("R5", "%s %s has no controller ownerReference to the %s", ch.GVK.Kind, ch.Key, r.c.API.Kind)
		}
	}
	rr := r.rule("R5")
	if r.e.Opts.Mode != harness.ModeKind {
		rr.Metrics["gcTested"] = 0
		r.note("R5", "garbage collection not exercised in envtest (no kube-controller-manager); owner references checked statically")
		return
	}
	rr.Metrics["gcTested"] = 1
	// Delete the CR with the operator down (ConfigSync, WebApp) so only Kubernetes can clean up.
	operatorDown := !r.c.UsesFakeCloud
	if operatorDown {
		_ = r.e.Manager.Stop()
	}
	if err := r.e.Admin.Delete(ctx, live); err != nil {
		r.fail("R5", "deleting %s: %v", r.c.API.Kind, err)
	} else if err := r.e.WaitGone(ctx, live, 30*time.Second); err != nil {
		r.fail("R5", "%s not deleted with the operator down: %v", r.c.API.Kind, err)
	} else if err := r.fx.AfterDelete(ctx, r.e, live); err != nil {
		r.fail("R5", "%v", err)
	}
	if operatorDown {
		_ = r.e.Manager.Start()
	}
}

// phaseDeletion checks R4: deletion completes, and external resources follow the policy.
func (r *runner) phaseDeletion(ctx context.Context, main *unstructured.Unstructured) {
	if !r.e.Manager.Running() {
		_ = r.e.Manager.Start()
	}
	del := func(label string, cr *unstructured.Unstructured) {
		// Re-read so AfterDelete sees the final status.
		live, err := r.e.GetCR(ctx, keyOf(cr))
		if err != nil {
			r.fail("R4", "%s: re-reading: %v", label, err)
			return
		}
		if err := r.e.Admin.Delete(ctx, live); err != nil {
			r.fail("R4", "%s: delete: %v", label, err)
			return
		}
		timeout := 30 * time.Second
		if r.e.Cloud != nil {
			timeout += r.e.Cloud.DeleteDuration
		}
		if err := r.e.WaitGone(ctx, live, timeout); err != nil {
			r.fail("R4", "%s: deletion hung: %v", label, err)
			return
		}
		if err := r.fx.AfterDelete(ctx, r.e, live); err != nil {
			r.fail("R4", "%s: %v", label, err)
		}
	}
	fresh := func(scenario string, opts map[string]string) *unstructured.Unstructured {
		cr, err := r.fx.Prepare(ctx, r.e, scenario, opts)
		if err == nil {
			err = r.e.Admin.Create(ctx, cr)
		}
		if err == nil {
			err = r.converge(ctx, keyOf(cr))
		}
		if err != nil {
			r.fail("R4", "%s: setup did not converge: %v", scenario, err)
			return nil
		}
		return cr
	}

	// 1. Normal delete (reuse the main CR if R5 didn't already delete it).
	target := main
	if target != nil {
		if _, err := r.e.GetCR(ctx, keyOf(target)); err != nil {
			target = nil
		}
	}
	if target == nil {
		target = fresh("delete", nil)
	}
	if target != nil {
		del("delete", target)
	}

	if !r.c.UsesFakeCloud {
		return
	}
	// 2. Retain policy.
	if cr := fresh("retain", map[string]string{"deletionPolicy": "Retain"}); cr != nil {
		del("retain", cr)
	}
	// 3. Delete while the cloud create call is still in flight.
	cr, err := r.fx.Prepare(ctx, r.e, "inflight", nil)
	if err != nil {
		r.fail("R4", "inflight: setup: %v", err)
		return
	}
	received := make(chan struct{})
	var once sync.Once
	r.e.Cloud.SetCreateHold(5 * time.Second)
	r.e.Cloud.SetOnCreate(func(fakecloud.Database) { once.Do(func() { close(received) }) })
	defer func() { r.e.Cloud.SetCreateHold(0); r.e.Cloud.SetOnCreate(nil) }()
	if err := r.e.Admin.Create(ctx, cr); err != nil {
		r.fail("R4", "inflight: create: %v", err)
		return
	}
	select {
	case <-received:
	case <-time.After(r.cfg.ConvergeTimeout):
		r.fail("R4", "inflight: operator never called the cloud create API")
		return
	}
	if err := r.e.Admin.Delete(ctx, cr); err != nil {
		r.fail("R4", "inflight: delete: %v", err)
		return
	}
	if err := r.e.WaitGone(ctx, cr, 45*time.Second); err != nil {
		r.fail("R4", "inflight: deletion hung: %v", err)
		return
	}
	if err := r.fx.AfterDelete(ctx, r.e, cr); err != nil {
		r.fail("R4", "inflight: %v", err)
	}
}

// auditSummary records 403s (under-privileged RBAC) for R7 and overall write counts.
func (r *runner) auditSummary() {
	events, err := r.e.AuditEvents()
	if err != nil {
		return
	}
	fb := audit.Forbidden(events, audit.Filter{User: r.e.ManagerUsername, Since: r.started})
	rr := r.rule("R7")
	total := 0
	var keys []string
	for k, n := range fb {
		total += n
		keys = append(keys, k)
	}
	rr.Metrics["forbidden403"] = float64(total)
	if total > 0 {
		r.note("R7", "manager was denied: %s (its RBAC is missing permissions it uses)", strings.Join(keys, ", "))
	}
	if ex := r.e.Manager.UnexpectedExits(); len(ex) > 0 {
		r.note("R1", "manager exited unexpectedly %d time(s) during the run", len(ex))
	}
}
