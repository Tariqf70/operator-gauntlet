#!/usr/bin/env bash
# Create the gauntlet kind cluster with audit logging and APF throttling.
# Run from anywhere; paths resolve relative to the repo root.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
mkdir -p .kind-audit
kind create cluster --name gauntlet --config kind/cluster.yaml --kubeconfig "$ROOT/.kind-kubeconfig"
kubectl --kubeconfig "$ROOT/.kind-kubeconfig" apply -f kind/apf-throttle.yaml
echo
echo "Run with:"
echo "  bin/gauntlet run --mode kind --kubeconfig $ROOT/.kind-kubeconfig --audit-log $ROOT/.kind-audit/audit.log --spec specs/<spec> --operator <dir>"
