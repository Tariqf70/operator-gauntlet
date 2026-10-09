#!/usr/bin/env bash
# Agent adapter template. Copy it per coding agent (one file per tool) and list
# it in runner/models.txt.
#
# Contract:
#   $1  workspace directory (the agent must work only here)
#   $2  prompt file
#   $3  model ID (pin exact versions; record the tool version too)
# Run the agent non-interactively until it finishes. Exit 0 on success.
#
# Tips:
#   - Sandbox the agent to $1, and give it no network access beyond the Go
#     module proxy, so it can't read this repo's hidden checks.
#   - Give it the same tools in every setup (shell, file edits, go, make, kubebuilder).
#   - Log token usage so cost per setup can be reported.
set -euo pipefail
ws=$1
prompt=$2
model=$3
cd "$ws"
echo "example-adapter: replace this with a call to your coding agent (model $model, prompt $prompt)" >&2
exit 1
