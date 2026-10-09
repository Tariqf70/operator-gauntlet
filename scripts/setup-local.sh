#!/usr/bin/env bash
# One-time local setup for macOS or Linux. Installs what's missing into ./bin,
# builds the tools, and writes .env.local (sourced by the Makefile targets).
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
BIN="$ROOT/bin"
mkdir -p "$BIN"
missing=0
ok() { printf '  ok    %s\n' "$*"; }
need() { printf '  NEED  %s\n' "$*"; missing=1; }

echo "Checking prerequisites"
command -v go >/dev/null 2>&1 || { echo "Go is required: https://go.dev/dl/ (or: brew install go)"; exit 1; }
ok "go $(go env GOVERSION)"
os=$(go env GOOS)
arch=$(go env GOARCH)

if command -v timeout >/dev/null 2>&1 || command -v gtimeout >/dev/null 2>&1; then
  ok "GNU timeout"
elif command -v brew >/dev/null 2>&1; then
  brew install coreutils >/dev/null && ok "GNU timeout (installed coreutils)"
else
  need "GNU timeout (macOS: brew install coreutils)"
fi

if command -v kubebuilder >/dev/null 2>&1; then
  ok "kubebuilder ($(command -v kubebuilder))"
elif [ -x "$BIN/kubebuilder" ]; then
  ok "kubebuilder ($BIN/kubebuilder)"
else
  echo "  ..    downloading kubebuilder for $os/$arch"
  curl -sSfL -o "$BIN/kubebuilder" \
    "https://github.com/kubernetes-sigs/kubebuilder/releases/latest/download/kubebuilder_${os}_${arch}"
  chmod +x "$BIN/kubebuilder"
  ok "kubebuilder ($BIN/kubebuilder)"
fi

if [ -n "${KUBEBUILDER_ASSETS:-}" ] && [ -x "$KUBEBUILDER_ASSETS/kube-apiserver" ]; then
  assets=$KUBEBUILDER_ASSETS
else
  echo "  ..    installing setup-envtest and the envtest binaries"
  GOBIN="$BIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
  assets=$("$BIN/setup-envtest" use "${ENVTEST_K8S_VERSION:-1.34.x}" --bin-dir "$BIN/k8s" -p path)
fi
ok "envtest binaries: $assets"

go build -o "$BIN/gauntlet" ./cmd/gauntlet
go build -o "$BIN/fakecloud" ./cmd/fakecloud
ok "built bin/gauntlet and bin/fakecloud"

agents=""
for cli in codex claude gemini; do
  if command -v "$cli" >/dev/null 2>&1; then
    ok "agent CLI: $cli ($("$cli" --version 2>/dev/null | head -1))"
    agents="$agents $cli"
  fi
done
[ -n "$agents" ] || need "at least one agent CLI (codex, claude or gemini) for the pilot"

cat > .env.local <<EOT
export KUBEBUILDER_ASSETS="$assets"
export PATH="$BIN:\$PATH"
EOT
ok "wrote .env.local"

# (Re)write models.txt if it doesn't exist or has no active (uncommented) line.
active=$(grep -vcE '^[[:space:]]*(#|$)' runner/models.txt 2>/dev/null || true)
if [ "${active:-0}" = 0 ] && [ -n "$agents" ]; then
  {
    echo "# label | adapter | model id. 'default' uses the CLI's configured model;"
    echo "# pin real model IDs before the pilot you report."
    case " $agents " in *" codex "*) echo "codex|codex.sh|default" ;; esac
    case " $agents " in *" claude "*) echo "# claude|claude-code.sh|default    (needs GAUNTLET_SANDBOXED=1: run inside a VM or container)" ;; esac
    case " $agents " in *" gemini "*) echo "# gemini|gemini.sh|default    (needs GAUNTLET_SANDBOXED=1: run inside a VM or container)" ;; esac
  } > runner/models.txt
  ok "wrote runner/models.txt (edit it to choose models)"
fi

echo
if [ "$missing" = 0 ]; then
  echo "Ready. Next: make selftest, then make oracle, then make pilot."
else
  echo "Install the items marked NEED, then run make setup again."
  exit 1
fi
