#!/usr/bin/env bash
# Build one agent workspace for a spec and setup.
# Usage: runner/prepare.sh <spec> <S1|S2|S3|S4> <workspace>
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
spec=$1
setup=$2
ws=$3

case "$setup" in
  S1) mkdir -p "$ws" ;;
  S2|S3|S4) "$ROOT/runner/scaffold.sh" "$spec" "$ws" ;;
  *) echo "unknown setup $setup" >&2; exit 2 ;;
esac
mkdir -p "$ws/.gauntlet"
cp "$ROOT/specs/$spec/SPEC.md" "$ws/"
note=""
if [ -f "$ROOT/specs/$spec/CLOUD_API.md" ]; then
  cp "$ROOT/specs/$spec/CLOUD_API.md" "$ws/"
  note=', and the cloud API it talks to is documented in `CLOUD_API.md`. A local fake of that API runs with `fakecloud` (see its --help)'
fi
if [ "$setup" = S3 ] || [ "$setup" = S4 ]; then
  mkdir -p "$ws/skills"
  cp -R "$ROOT/skill/controller-best-practices" "$ws/skills/"
fi
if [ "$setup" = S4 ]; then
  cat > "$ws/gauntlet-check" <<EOS
#!/usr/bin/env bash
# Runs the visible checks only. Hidden checks are never shown to the agent.
exec "$ROOT/bin/gauntlet" run --visible-only --spec "$ROOT/specs/$spec" --operator "\$(pwd)" --log "\$(pwd)/.gauntlet/check.log"
EOS
  chmod +x "$ws/gauntlet-check"
fi
{
  sed "s|{{CLOUD_API_NOTE}}|$note|" "$ROOT/runner/prompts/base.md"
  echo
  cat "$ROOT/runner/prompts/$setup.md"
} > "$ws/.gauntlet/prompt.md"
