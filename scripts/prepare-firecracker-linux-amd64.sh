#!/usr/bin/env bash
set -Eeuo pipefail

readonly firecracker_release='1.15.1'
readonly firecracker_architecture='x86_64'
readonly archive_name="firecracker-v${firecracker_release}-${firecracker_architecture}.tgz"
readonly archive_url="https://github.com/firecracker-microvm/firecracker/releases/download/v${firecracker_release}/${archive_name}"
readonly archive_checksum='d4a32ab2322d887ca1bc4a4e7afa9cc35393e6362dfc2b3becb389d362e4275a'
readonly release_directory="release-v${firecracker_release}-${firecracker_architecture}"

fail() {
  printf 'prepare-firecracker: %s\n' "$*" >&2
  exit 1
}

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repo_dir}/scripts/lib/common.sh"
output_dir="${1:-${repo_dir}/dist}"

for command in curl file install mktemp tar; do
  command -v "${command}" >/dev/null 2>&1 \
    || fail "required command is unavailable: ${command}"
done

sha256_files --version >/dev/null 2>&1 || fail 'sha256sum or shasum is required'

make_temporary_directory work_dir cantelop-firecracker
mkdir -p "${output_dir}"

archive_path="${work_dir}/${archive_name}"
curl --fail --location --silent --show-error \
  --output "${archive_path}" \
  "${archive_url}"

actual_checksum="$(sha256_files "${archive_path}")"
actual_checksum="${actual_checksum%% *}"
[[ "${actual_checksum}" == "${archive_checksum}" ]] \
  || fail 'Firecracker release archive SHA-256 mismatch'

firecracker_member="${release_directory}/firecracker-v${firecracker_release}-${firecracker_architecture}"
jailer_member="${release_directory}/jailer-v${firecracker_release}-${firecracker_architecture}"
tar -xzf "${archive_path}" -C "${work_dir}" \
  "${firecracker_member}" \
  "${jailer_member}"

for member in "${firecracker_member}" "${jailer_member}"; do
  [[ -f "${work_dir}/${member}" && ! -L "${work_dir}/${member}" ]] \
    || fail "release archive is missing a regular binary: ${member}"
  binary_description="$(LC_ALL=C file --brief "${work_dir}/${member}")"
  [[ "${binary_description}" == *ELF* && "${binary_description}" == *x86-64* ]] \
    || fail "release member is not a Linux x86-64 ELF binary: ${member}"
done

firecracker_output="${output_dir}/firecracker-linux-amd64"
jailer_output="${output_dir}/jailer-linux-amd64"
install -m 0755 "${work_dir}/${firecracker_member}" "${firecracker_output}"
install -m 0755 "${work_dir}/${jailer_member}" "${jailer_output}"

sha256_files "${firecracker_output}" \
  > "${output_dir}/firecracker-linux-amd64.sha256"
sha256_files "${jailer_output}" \
  > "${output_dir}/jailer-linux-amd64.sha256"

printf 'Pinned Firecracker and jailer %s written to %s\n' \
  "${firecracker_release}" \
  "${output_dir}"
