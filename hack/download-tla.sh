#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

version="${TLA_TOOLS_VERSION:-1.8.0}"
sha256="${TLA_TOOLS_SHA256:-e47073579d0ff27989bd3789ce0cc8ab42ca6e2c4374c0e95c3dfa0bff9f0113}"
jar_dir="${TLA_TOOLS_DIR:-${repo_root}/.cache/tla}"
jar_path="${TLA2TOOLS_JAR:-${jar_dir}/tla2tools-${version}.jar}"
url="https://github.com/tlaplus/tlaplus/releases/download/v${version}/tla2tools.jar"

mkdir -p "${jar_dir}"

verify() {
  local got
  got="$(shasum -a 256 "${jar_path}" | awk '{print $1}')"
  [[ "${got}" == "${sha256}" ]]
}

if [[ -f "${jar_path}" ]] && verify; then
  printf '%s\n' "${jar_path}"
  exit 0
fi

tmp="${jar_path}.tmp"
rm -f "${tmp}"

curl -fsSL -o "${tmp}" "${url}"
mv "${tmp}" "${jar_path}"

if ! verify; then
  rm -f "${jar_path}"
  echo "downloaded TLA+ tools jar failed SHA-256 verification" >&2
  exit 1
fi

printf '%s\n' "${jar_path}"
