#!/usr/bin/env bash
# OpenAI Codex CLI adapter (non-interactive `codex exec`).
# Docs: https://github.com/openai/codex/blob/main/docs/exec.md
#
# --sandbox workspace-write lets the agent edit files only in the workspace.
# The agent needs network access for `go mod download`. If that fails inside the
# sandbox, allow network for workspace-write in ~/.codex/config.toml (check the
# Codex docs for the current key) rather than disabling the sandbox.
set -euo pipefail
ws=$1
prompt=$2
model=$3
cd "$ws"
codex --version >.gauntlet/agent-version.txt 2>&1 || true
# model "default" = the CLI's configured default (record which one it was!)
margs=(); [ "$model" != default ] && margs=(-m "$model")
codex exec ${margs[@]+"${margs[@]}"} --sandbox workspace-write --skip-git-repo-check --json \
  -o .gauntlet/agent-final.txt - <"$prompt" >.gauntlet/agent-events.jsonl
