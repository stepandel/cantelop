#!/usr/bin/env bash
set -Eeuo pipefail

readonly minio_release='RELEASE.2025-10-15T17-29-55Z'
readonly minio_version='2025-10-15T17:29:55Z'
readonly minio_commit='9e49d5e7a648'
readonly minio_ldflags="-s -w \
-X github.com/minio/minio/cmd.Version=${minio_version} \
-X github.com/minio/minio/cmd.CopyrightYear=2025 \
-X github.com/minio/minio/cmd.ReleaseTag=${minio_release} \
-X github.com/minio/minio/cmd.CommitID=${minio_commit} \
-X github.com/minio/minio/cmd.ShortCommitID=${minio_commit}"

fail() {
  printf 'build-minio: %s\n' "$*" >&2
  exit 1
}

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"

# The local shipping controller can be macOS. Execute the same pinned build
# and Linux identity check in the builder image used by our JuiceFS build.
if [[ "$(uname -s)/$(uname -m)" != "Linux/x86_64" ]]; then
  command -v docker >/dev/null 2>&1 || fail 'Docker is required to build MinIO on this machine'
  mkdir -p "${output_dir}"
  output_dir="$(cd "${output_dir}" && pwd)"
  build_cache="${CANTELOP_STORAGE_BUILD_CACHE:-${output_dir}/.minio-build-cache}"
  mkdir -p "${build_cache}/tmp"
  build_cache="$(cd "${build_cache}" && pwd)"
  docker run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" \
    -e GOCACHE=/cache/build -e GOMODCACHE=/cache/modules -e GOTOOLCHAIN=local -e TMPDIR=/cache/tmp \
    -v "${repo_dir}:/src:ro" -v "${output_dir}:/out" -v "${build_cache}:/cache" \
    -w /src \
    'golang:1.26.2-bookworm@sha256:47ce5636e9936b2c5cbf708925578ef386b4f8872aec74a67bd13a627d242b19' \
    bash storage/minio/build-minio-linux-amd64.sh /out
  exit 0
fi

for command in go grep install mktemp; do
  command -v "${command}" >/dev/null 2>&1 || fail "required command is unavailable: ${command}"
done

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
mkdir -p "${output_dir}"
module_cache="$(go env GOMODCACHE)"

env \
  CGO_ENABLED=0 \
  GOOS=linux \
  GOARCH=amd64 \
  GOPATH="${work_dir}" \
  GOMODCACHE="${module_cache}" \
  go install \
    -trimpath \
    -ldflags "${minio_ldflags}" \
    "github.com/minio/minio@${minio_release}"

minio_binary="${work_dir}/bin/linux_amd64/minio"
if [[ ! -f "${minio_binary}" ]]; then
  minio_binary="${work_dir}/bin/minio"
fi
[[ -f "${minio_binary}" ]] || fail 'Go build did not produce a MinIO binary'
"${minio_binary}" --version \
  | grep -F "minio version ${minio_release} " >/dev/null \
  || fail 'Go build did not embed the pinned MinIO release identity'

install -m 0755 \
  "${minio_binary}" \
  "${output_dir}/minio-linux-amd64"

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${output_dir}/minio-linux-amd64" \
    > "${output_dir}/minio-linux-amd64.sha256"
else
  shasum -a 256 "${output_dir}/minio-linux-amd64" \
    > "${output_dir}/minio-linux-amd64.sha256"
fi

printf 'Pinned MinIO %s written to %s\n' "${minio_release}" "${output_dir}"
