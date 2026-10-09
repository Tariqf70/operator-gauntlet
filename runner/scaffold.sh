#!/usr/bin/env bash
# Pre-scaffold a Kubebuilder project with the spec's API types (setups S2–S4).
# Usage: runner/scaffold.sh <spec> <workspace>
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
spec=$1
ws=$2
contract="$ROOT/specs/$spec/contract.json"
kind=$(sed -n 's/.*"kind": *"\([A-Za-z]*\)".*/\1/p' "$contract" | head -1)
namespaced=false
grep -q '"namespaced": *true' "$contract" && namespaced=true
lower=$(echo "$kind" | tr '[:upper:]' '[:lower:]')

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(
  cd "$tmp"
  kubebuilder init --domain example.com --repo "example.com/$spec-operator" --project-name "$spec-operator" --skip-go-version-check
  kubebuilder create api --group bench --version v1alpha1 --kind "$kind" \
    --resource --controller --namespaced="$namespaced"
  cp "$ROOT/specs/$spec/scaffold/types.go.tmpl" "api/v1alpha1/${lower}_types.go"
  # Newer Kubebuilder uses apimachinery's runtime.SchemeBuilder; older uses controller-runtime's.
  if grep -q 'runtime.NewSchemeBuilder' api/v1alpha1/groupversion_info.go; then
    cat > api/v1alpha1/register.go <<EOG
package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &${kind}{}, &${kind}List{})
		return nil
	})
}
EOG
  else
    cat > api/v1alpha1/register.go <<EOG
package v1alpha1

func init() {
	SchemeBuilder.Register(&${kind}{}, &${kind}List{})
}
EOG
  fi
  make manifests generate
)
mkdir -p "$ws"
cp -a "$tmp/." "$ws/"
