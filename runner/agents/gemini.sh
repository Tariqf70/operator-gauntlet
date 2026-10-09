#!/usr/bin/env bash
# Google Gemini CLI adapter (non-interactive -p).
# Docs: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/headless.md
#
# --approval-mode=yolo auto-approves every tool call. Only do that in a
# disposable VM or container; the guard below makes you confirm it.
set -euo pipefail
if [ "${GAUNTLET_SANDBOXED:-}" != 1 ]; then
  echo "refusing to auto-approve tools outside a sandbox: set GAUNTLET_SANDBOXED=1 inside a disposable VM/container" >&2
  exit 3
fi
ws=$1
prompt=$2
model=$3
cd "$ws"
gemini --version >.gauntlet/agent-version.txt 2>&1 || true
margs=(); [ "$model" != default ] && margs=(-m "$model")
gemini ${margs[@]+"${margs[@]}"} --approval-mode=yolo -o json -p "$(cat "$prompt")" >.gauntlet/agent-output.json
