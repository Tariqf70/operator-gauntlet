#!/usr/bin/env bash
# End-to-end pipeline check with the oracle adapter (copies the reference
# controller in place of a real agent). Expect 7/7.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
echo "oracle|replay-reference.sh|reference" >"$tmp/models.txt"
MODELS_FILE="$tmp/models.txt" OUT="$tmp/out" RESULTS="$tmp/results" \
  SPECS="${SPECS:-webapp}" SETUPS="${SETUPS:-S3}" RUNS=1 SKIP_OWN_TESTS="${SKIP_OWN_TESTS:-1}" \
  GAUNTLET_FLAGS="--converge-timeout 40s --quiet-window 20s --writer-duration 15s" \
  "$ROOT/runner/run-pilot.sh"
echo "workspaces and logs: $tmp"
