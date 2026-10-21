#!/usr/bin/env bash
set -Eeuo pipefail

# Full upstream feature set, built from reviewed source plus our data-only policy.
readonly juicefs_revision='62bedf3cdecdb763c0be11edbab21f6764159aa5'
readonly archive_url="https://codeload.github.com/juicedata/juicefs/tar.gz/${juicefs_revision}"
readonly archive_checksum='75ed9324bbb78b70d0b9261ec0dcaab3b91d85b74961f049fd4cc316a31c7a7f'
readonly builder='golang:1.26.2-bookworm@sha256:47ce5636e9936b2c5cbf708925578ef386b4f8872aec74a67bd13a627d242b19'

fail() { printf 'prepare-juicefs: %s\n' "$*" >&2; exit 1; }
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"
for command in curl docker git mktemp tar; do
  command -v "${command}" >/dev/null 2>&1 || fail "required command is unavailable: ${command}"
done
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
mkdir -p "${output_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
build_cache="${CANTELOP_STORAGE_BUILD_CACHE:-${output_dir}/.juicefs-build-cache}"
mkdir -p "${build_cache}/tmp"
build_cache="$(cd "${build_cache}" && pwd)"
curl --fail --location --silent --show-error --output "${work_dir}/source.tar.gz" "${archive_url}"
actual_checksum="$(sha256 "${work_dir}/source.tar.gz")"
[[ "${actual_checksum%% *}" == "${archive_checksum}" ]] || fail 'JuiceFS source archive SHA-256 mismatch'
mkdir "${work_dir}/source"
tar -xzf "${work_dir}/source.tar.gz" --strip-components=1 -C "${work_dir}/source"
patch_file="${repo_dir}/storage/juicefs/workspace-data-only.patch"
git -C "${work_dir}/source" apply --check "${patch_file}"
git -C "${work_dir}/source" apply "${patch_file}"

# No privileged container, devices, host network, or deployment credentials.
# The tests use an in-memory backend and do not mount a filesystem.
docker run --rm --platform linux/amd64 \
  --user "$(id -u):$(id -g)" \
  -e GOCACHE=/cache/build -e GOMODCACHE=/cache/modules -e GOTOOLCHAIN=local -e TMPDIR=/cache/tmp \
  -v "${work_dir}/source:/src" -v "${output_dir}:/out" \
  -v "${build_cache}:/cache" -w /src \
  "${builder}" sh -ec '
    go test -p 2 ./pkg/vfs ./pkg/fuse -run "TestWorkspacePolicy|TestWorkspaceMountSubtype|TestVFSBasic|TestHideInternal" -count=1
    go build -p 2 -trimpath -buildvcs=false \
      -ldflags="-s -w -X github.com/juicedata/juicefs/pkg/version.revision=62bedf3-cantelop-workspace-v1 -X github.com/juicedata/juicefs/pkg/version.revisionDate=2026-07-06" \
      -o /out/juicefs-linux-amd64 .
    /out/juicefs-linux-amd64 mount --help | grep -q -- --workspace-data-only
    /out/juicefs-linux-amd64 mount --help | grep -q -- --root-only
    /out/juicefs-linux-amd64 config --help | grep -q -- --access-key-env
    /out/juicefs-linux-amd64 config --help | grep -q -- --secret-key-env
  '
sha256 "${output_dir}/juicefs-linux-amd64" > "${output_dir}/juicefs-linux-amd64.sha256"
{
  printf 'upstream_revision=%s\nsource_sha256=%s\nbuilder=%s\npolicy=workspace-v1\n' \
    "${juicefs_revision}" "${archive_checksum}" "${builder}"
  printf 'patch_sha256=%s\n' "$(sha256 "${patch_file}")"
} > "${output_dir}/juicefs-linux-amd64.build-info"
printf 'Patched JuiceFS written to %s\n' "${output_dir}"
