#!/usr/bin/env python3
"""Derive one deliberately broken operator per rule from testdata/webapp-reference.

Each mutant rewrites one region of internal/controller/webapp_controller.go
marked `// MUTANT:<rule> <name> begin` ... `end`. The suite is valid only if the
reference passes every rule and each mutant fails the rule it targets.

Usage: testdata/make-mutants.py [outdir]   (default testdata/out)
Then run `make manifests` in the R4 and R7 mutants (their RBAC markers change);
testdata/validate.sh does this for you.
"""
import os
import re
import shutil
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REF = os.path.join(HERE, "webapp-reference")
CTRL = "internal/controller/webapp_controller.go"

# rule -> {region name: replacement code}
MUTANTS = {
    "R1": {  # crash-unsafe: the Service is created only in the reconcile that created the Deployment
        "service": '''	if depOp == controllerutil.OperationResultCreated {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
			r.mutateService(app, svc)
			return controllerutil.SetControllerReference(app, svc, r.Scheme)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
''',
    },
    "R2": {  # observedGeneration is set once and never updated
        "observed-generation": '''	if st.ObservedGeneration == 0 {
		st.ObservedGeneration = app.Generation
	}
''',
    },
    "R3": {  # rewrites status on every reconcile and requeues every second
        "status-write": '''	app.Status = *st
	if err := r.Status().Update(ctx, app); err != nil {
		return ctrl.Result{}, err
	}
	_ = equality.Semantic
	return ctrl.Result{RequeueAfter: time.Second}, nil
''',
    },
    "R4": {  # adds a finalizer it never removes (with the RBAC it needs, so only R4 breaks)
        "R7:rbac-markers": '''// +kubebuilder:rbac:groups=bench.example.com,resources=webapps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=bench.example.com,resources=webapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bench.example.com,resources=webapps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
''',
        "finalizer": '''	if controllerutil.AddFinalizer(app, "bench.example.com/cleanup") {
		if err := r.Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}
''',
    },
    "R5": {  # no owner references on children
        "owner-deployment": "\t\treturn nil\n",
        "owner-service": "\t\treturn nil\n",
    },
    "R6": {  # replaces the Deployment's labels, erasing labels other tools set
        "labels": '''	dep.Labels = map[string]string{nameLabel: app.Name, managedByLabel: managedByValue}
''',
    },
    "R7": {  # wildcard RBAC
        "rbac-markers": '''// +kubebuilder:rbac:groups=*,resources=*,verbs=*
''',
    },
}


def mutate(src, rule, regions):
    for name, repl in regions.items():
        region_rule = rule
        if ":" in name:  # a region owned by another rule, e.g. "R7:rbac-markers"
            region_rule, name = name.split(":", 1)
        pat = re.compile(
            r"([ \t]*// MUTANT:%s %s begin\n)(.*?)([ \t]*// MUTANT:%s %s end\n)"
            % (region_rule, re.escape(name), region_rule, re.escape(name)),
            re.S,
        )
        src, n = pat.subn(lambda m: m.group(1) + repl + m.group(3), src)
        if n != 1:
            raise SystemExit("region %s/%s not found exactly once" % (region_rule, name))
    if rule == "R3" and '"time"' not in src:
        src = src.replace('\t"fmt"\n', '\t"fmt"\n\t"time"\n', 1)
    return src


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, "out")
    ref_src = open(os.path.join(REF, CTRL)).read()
    for rule, regions in MUTANTS.items():
        dst = os.path.join(out, "webapp-" + rule)
        if os.path.exists(dst):
            shutil.rmtree(dst)
        shutil.copytree(REF, dst, ignore=shutil.ignore_patterns("bin"))
        with open(os.path.join(dst, CTRL), "w") as f:
            f.write(mutate(ref_src, rule, regions))
        print("wrote", dst)


if __name__ == "__main__":
    main()
