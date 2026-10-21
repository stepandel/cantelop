#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repo_dir}/scripts/lib/common.sh"
console_dir="${repo_dir}/console"
output_dir="${1:-${repo_dir}/dist}"
migrations_output_dir="${output_dir}/cantelop-control-migrations"
# console/package.json packageManager is the repository's Bun pin.
expected_bun_version="$(sed -n 's/^ *"packageManager": "bun@\([0-9.]*\)",*$/\1/p' "${console_dir}/package.json")"
if [[ -z "${expected_bun_version}" ]]; then
  printf 'console/package.json does not pin packageManager bun@<version>\n' >&2
  exit 1
fi

if [[ "$(bun --version)" != "${expected_bun_version}" ]]; then
  printf 'bun %s is required\n' "${expected_bun_version}" >&2
  exit 1
fi

mkdir -p "${output_dir}"
(
  cd "${repo_dir}"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -o "${output_dir}/cantelop-build-materializer-linux-amd64" \
    ./builder/cmd/build-materializer
)
rm -rf "${migrations_output_dir}"
mkdir -p "${migrations_output_dir}"
cp -R "${console_dir}/core/db/drizzle" "${migrations_output_dir}/drizzle"

(
  cd "${console_dir}"
  bun install --frozen-lockfile
  bun "${repo_dir}/scripts/build-control-worker.ts" \
    "${output_dir}/cantelop-control-worker-linux-amd64"
  bun build core/db/migrate-command.ts \
    --compile \
    --target=bun-linux-x64 \
    --outfile "${output_dir}/cantelop-control-migrate-linux-amd64"
)

(
  cd "${migrations_output_dir}"
  while IFS= read -r migration_file; do
    sha256_files "${migration_file}"
  done < <(find drizzle -type f -print | LC_ALL=C sort)
) > "${migrations_output_dir}/SHA256SUMS"

sha256_files \
  "${output_dir}/cantelop-control-worker-linux-amd64" \
  "${output_dir}/cantelop-build-materializer-linux-amd64" \
  "${output_dir}/cantelop-control-migrate-linux-amd64" \
  "${migrations_output_dir}/SHA256SUMS" \
  > "${output_dir}/CANTELOP_CONTROL_WORKER_SHA256SUMS"

printf 'Builder artifacts written to %s\n' "${output_dir}"
