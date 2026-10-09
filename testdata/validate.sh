#!/usr/bin/env bash
# Validate the suite: the reference must pass all rules, and each mutant must
# fail the rule it targets. Prints a matrix and exits non-zero on a mismatch.
# Short timings keep a full validation under ~10 minutes.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${OUT:-$ROOT/testdata/out}
FLAGS=${FLAGS:-"--converge-timeout 40s --quiet-window 20s --writer-duration 15s"}
: "${KUBEBUILDER_ASSETS:?set KUBEBUILDER_ASSETS (setup-envtest use -p path)}"
cd "$ROOT"
go build -o bin/gauntlet ./cmd/gauntlet
python3 testdata/make-mutants.py "$OUT"
# R4 and R7 change RBAC markers, so regenerate their role.yaml with the reference's controller-gen.
(cd testdata/webapp-reference && make controller-gen >/dev/null)
for r in R4 R7; do
  (cd "$OUT/webapp-$r" && make manifests CONTROLLER_GEN="$ROOT/testdata/webapp-reference/bin/controller-gen" >/dev/null)
done

run() { # name spec dir
  bin/gauntlet run --spec "specs/$2" --operator "$3" $FLAGS \
    --log "$OUT/$1.log" --out "$OUT/$1.json" >/dev/null 2>&1 || true
}
for spec in webapp configsync manageddatabase; do
  echo "running $spec reference"
  run "reference-$spec" "$spec" "testdata/$spec-reference"
done
for r in R1 R2 R3 R4 R5 R6 R7; do
  echo "running webapp mutant $r"
  run "webapp-$r" webapp "$OUT/webapp-$r"
done

python3 - "$OUT" <<'PY'
import json, sys, os
out = sys.argv[1]
rules = ["R1","R2","R3","R4","R5","R6","R7"]
bad = 0
print("| operator | " + " | ".join(rules) + " | verdict |")
print("| --- |" + " --- |" * (len(rules) + 1))
names = ["reference-webapp", "reference-configsync", "reference-manageddatabase"] + ["webapp-" + r for r in rules]
for name in names:
    p = os.path.join(out, name + ".json")
    if not os.path.exists(p):
        print("| %s | (no result) |" % name); bad += 1; continue
    res = {x["id"]: x["pass"] for x in json.load(open(p)).get("rules", [])}
    cells = ["pass" if res.get(r) else "**FAIL**" for r in rules]
    if name.startswith("reference-"):
        ok = all(res.get(r) for r in rules)
    else:
        target = name.split("-")[1]
        ok = res.get(target) is False
    bad += 0 if ok else 1
    print("| %s | %s | %s |" % (name, " | ".join(cells), "ok" if ok else "MISMATCH"))
sys.exit(1 if bad else 0)
PY
