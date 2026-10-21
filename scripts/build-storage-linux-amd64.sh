#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"

# JuiceFS builds in a pinned Docker image. MinIO verifies its embedded
# release identity by executing the binary, so it must run on Linux amd64.
if [[ "$(uname -s)/$(uname -m)" != "Linux/x86_64" ]]; then
  echo "storage artifacts must be built on Linux x86_64" >&2
  exit 1
fi
"${repo_dir}/storage/juicefs/prepare-juicefs-linux-amd64.sh" "${output_dir}"
"${repo_dir}/storage/minio/build-minio-linux-amd64.sh" "${output_dir}"

printf 'Storage artifacts written to %s\n' "${output_dir}"
