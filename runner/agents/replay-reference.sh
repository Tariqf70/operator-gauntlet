#!/usr/bin/env bash
# "Oracle agent" for testing the pipeline: copies the reference controller for
# the workspace's spec into it. A pilot run with this adapter should score 7/7.
# Works with setups S2–S4 (the project must already be scaffolded).
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ws=$1
spec=$(basename "$ws" | cut -d- -f1)
ref="$ROOT/testdata/$spec-reference"
[ -d "$ws/internal/controller" ] || { echo "replay-reference needs a pre-scaffolded workspace (S2–S4)" >&2; exit 2; }
cp "$ref"/internal/controller/*_controller.go "$ws/internal/controller/"
if [ -d "$ref/internal/cloud" ]; then cp -R "$ref/internal/cloud" "$ws/internal/"; fi
cd "$ws" && make manifests generate >/dev/null
