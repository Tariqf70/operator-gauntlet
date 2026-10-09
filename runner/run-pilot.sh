#!/usr/bin/env bash
# Run the pilot matrix: every model in models.txt x SPECS x SETUPS x RUNS.
# Finished runs (results/<name>.json exists) are skipped, so it can be resumed.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SPECS=${SPECS:-"configsync manageddatabase webapp"}
SETUPS=${SETUPS:-"S1 S2 S3 S4"}
RUNS=${RUNS:-2}
MODELS_FILE=${MODELS_FILE:-$ROOT/runner/models.txt}
OUT=${OUT:-$ROOT/out}
RESULTS=${RESULTS:-$ROOT/results}
AGENT_TIMEOUT=${AGENT_TIMEOUT:-45m}
GAUNTLET_FLAGS=${GAUNTLET_FLAGS:-}   # e.g. "--quiet-window 20s" for quick tests; empty for real runs
SKIP_OWN_TESTS=${SKIP_OWN_TESTS:-0}
GAUNTLET="$ROOT/bin/gauntlet"

# GNU timeout (on macOS: brew install coreutils, which provides gtimeout).
if command -v timeout >/dev/null 2>&1; then TIMEOUT=timeout
elif command -v gtimeout >/dev/null 2>&1; then TIMEOUT=gtimeout
else echo "need GNU timeout: on macOS run 'brew install coreutils'" >&2; exit 2
fi

: "${KUBEBUILDER_ASSETS:?set KUBEBUILDER_ASSETS, e.g. export KUBEBUILDER_ASSETS=\$(setup-envtest use -p path)}"
[ -f "$MODELS_FILE" ] || { echo "copy runner/models.example.txt to $MODELS_FILE and edit it" >&2; exit 2; }
(cd "$ROOT" && go build -o bin/gauntlet ./cmd/gauntlet && go build -o bin/fakecloud ./cmd/fakecloud)
export PATH="$ROOT/bin:$PATH"   # agents can run `fakecloud` locally
mkdir -p "$OUT" "$RESULTS"

while IFS='|' read -r -u 3 label adapter model_id; do
  [[ -z "${label// }" || "$label" == \#* ]] && continue
  for spec in $SPECS; do
    for setup in $SETUPS; do
      for n in $(seq 1 "$RUNS"); do
        name="$spec-$setup-$label-$n"
        ws="$OUT/$name"
        result="$RESULTS/$name.json"
        if [ -f "$result" ]; then echo "skip $name (already scored)"; continue; fi
        rm -rf "$ws"
        "$ROOT/runner/prepare.sh" "$spec" "$setup" "$ws"
        echo "== $name: agent ($model_id)"
        if ! "$TIMEOUT" "$AGENT_TIMEOUT" "$ROOT/runner/agents/$adapter" "$ws" "$ws/.gauntlet/prompt.md" "$model_id" \
            </dev/null >"$ws/.gauntlet/agent.log" 2>&1; then
          echo "agent exited non-zero or timed out" >>"$ws/.gauntlet/agent.log"
        fi
        python3 "$ROOT/runner/usage.py" "$ws" >"$ws/.gauntlet/usage.json" 2>/dev/null || echo '{}' >"$ws/.gauntlet/usage.json"
        own=false
        if [ "$SKIP_OWN_TESTS" = 1 ]; then own=unknown
        elif (cd "$ws" && "$TIMEOUT" 20m make test) >"$ws/.gauntlet/own-tests.log" 2>&1; then own=true; fi
        echo "== $name: gauntlet (own tests passed: $own)"
        # shellcheck disable=SC2086
        "$GAUNTLET" run $GAUNTLET_FLAGS --spec "$ROOT/specs/$spec" --operator "$ws" \
          --setup "$setup" --model "$model_id" --agent "$adapter" --attempt "$n" \
          --own-tests "$own" --usage-file "$ws/.gauntlet/usage.json" \
          --out "$result" --log "$ws/.gauntlet/gauntlet.log" || true
      done
    done
  done
done 3<"$MODELS_FILE"

"$GAUNTLET" report "$RESULTS"/*.json | tee "$RESULTS/REPORT.md"
