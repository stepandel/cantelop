#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${1:-${repo_dir}/dist}"
# shellcheck source=scripts/lib/common.sh
source "${repo_dir}/scripts/lib/common.sh"

release_commit="${CANTELOP_BUILD_COMMIT:-$(git -C "${repo_dir}" rev-parse HEAD)}"
deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${release_commit:0:12}}"
cli_version="$(tr -d '\n' <"${repo_dir}/clients/cli/VERSION")"
release_ldflags="-s -w -X github.com/stepandel/cantelop/internal/releaseinfo.revision=${deployment_revision} -X github.com/stepandel/cantelop/internal/releaseinfo.commit=${release_commit}"
release_cli_ldflags="-s -w -X main.version=${cli_version} -X main.commit=${release_commit:0:12}"

if [[ ! "${release_commit}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "CANTELOP_BUILD_COMMIT must be a full lowercase Git commit" >&2
  exit 1
fi
if [[ ! "${deployment_revision}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "CANTELOP_DEPLOYMENT_REVISION must be a safe 1-64 character identifier" >&2
  exit 1
fi
if [[ ! "${cli_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "clients/cli/VERSION is not a supported semantic version" >&2
  exit 1
fi

mkdir -p "${output_dir}"

build_env=(CGO_ENABLED=0 GOOS=linux GOARCH=amd64)
build_flags=(-buildvcs=false -trimpath)
# Commands that share the release ldflags. go build names each binary after
# its package directory; the artifact adds a -linux-amd64 suffix.
release_commands=(
  ./fire-fuse/runtime/cmd/fire-fuse
  ./fire-fuse/runtime/cmd/fire-fuse-managed-watchdog
  ./tools/snapshot-builder/cmd/fire-fuse-snapshot
  ./tools/image-builder/cmd/fire-fuse-image
  ./fire-fuse/gateway/cmd/fire-fuse-gateway
  ./fire-fuse/dispatcher/cmd/fire-fuse-async-dispatcher
  ./fire-fuse/host/cmd/fire-fuse-observer
)
make_temporary_directory release_build_dir cantelop-compute-build

(
  cd "${repo_dir}"
  env "${build_env[@]}" go build "${build_flags[@]}" \
    -ldflags="${release_ldflags}" \
    -o "${release_build_dir}/" \
    "${release_commands[@]}"

  env "${build_env[@]}" go build "${build_flags[@]}" \
    -ldflags="${release_cli_ldflags}" \
    -o "${output_dir}/cantelop-linux-amd64" \
    ./clients/cli/cmd/cantelop

  env "${build_env[@]}" go test -c \
    -buildvcs=false \
    -o "${output_dir}/fire-fuse-integration-linux-amd64" \
    ./tests/integration

  for command in "${release_commands[@]}"; do
    name="$(basename "${command}")"
    mv -f "${release_build_dir}/${name}" "${output_dir}/${name}-linux-amd64"
  done
)

sha256_files \
  "${output_dir}/fire-fuse-linux-amd64" \
  "${output_dir}/cantelop-linux-amd64" \
  "${output_dir}/fire-fuse-snapshot-linux-amd64" \
  "${output_dir}/fire-fuse-image-linux-amd64" \
  "${output_dir}/fire-fuse-gateway-linux-amd64" \
  "${output_dir}/fire-fuse-async-dispatcher-linux-amd64" \
  "${output_dir}/fire-fuse-observer-linux-amd64" \
  "${output_dir}/fire-fuse-managed-watchdog-linux-amd64" \
  "${output_dir}/fire-fuse-integration-linux-amd64" \
  > "${output_dir}/SHA256SUMS"

printf 'Compute artifacts written to %s\n' "${output_dir}"
