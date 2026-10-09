#!/usr/bin/env bash
# Anthropic Claude Code adapter (print mode).
# Docs: https://code.claude.com/docs/en/cli-reference and https://code.claude.com/docs/en/headless
#
# The benchmark needs the agent to run shell commands (go, make, kubebuilder)
# without prompts, so this uses --permission-mode bypassPermissions. Only do that
# in a disposable VM or container that holds nothing but the workspace. The
# guard below makes you confirm it.
set -euo pipefail
if [ "${GAUNTLET_SANDBOXED:-}" != 1 ]; then
  echo "refusing to run with permissions bypassed outside a sandbox: set GAUNTLET_SANDBOXED=1 inside a disposable VM/container" >&2
  exit 3
fi
ws=$1
prompt=$2
model=$3
cd "$ws"
claude --version >.gauntlet/agent-version.txt 2>&1 || true
margs=(); [ "$model" != default ] && margs=(--model "$model")
claude -p "$(cat "$prompt")" ${margs[@]+"${margs[@]}"} --permission-mode bypassPermissions \
  --output-format json --max-turns 150 >.gauntlet/agent-output.json
