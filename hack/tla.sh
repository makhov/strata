#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
spec_dir="${repo_root}/specs/tla/election"

cd "${spec_dir}"

if command -v tlc >/dev/null 2>&1; then
  exec tlc -deadlock -config Election.cfg Election.tla
fi

if [[ -n "${TLA2TOOLS_JAR:-}" ]]; then
  exec java -cp "${TLA2TOOLS_JAR}" tlc2.TLC -deadlock -config Election.cfg Election.tla
fi

jar_path="$("${repo_root}/hack/download-tla.sh")"
exec java -cp "${jar_path}" tlc2.TLC -deadlock -config Election.cfg Election.tla
