#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
output_directory="${1:-${repository_directory}/dist/fire-fuse-release}"
commit="${CANTELOP_BUILD_COMMIT:-$(git -C "${repository_directory}" rev-parse HEAD)}"
deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${commit:0:12}}"
platform="linux-amd64"
archive_name="fire-fuse_${deployment_revision}_${platform}.tar.gz"
make_temporary_directory work_directory cantelop-fire-fuse-release
bundle_directory="${work_directory}/fire-fuse_${deployment_revision}_${platform}"
build_directory="${work_directory}/build"

if [[ ! "${deployment_revision}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "Fire Fuse deployment revision must be a safe 1-64 character identifier" >&2
  exit 1
fi
if [[ ! "${commit}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "fire-fuse build commit must be a full lowercase Git commit" >&2
  exit 1
fi

mkdir -p "${output_directory}" "${bundle_directory}/bin" "${build_directory}"
CANTELOP_DEPLOYMENT_REVISION="${deployment_revision}" CANTELOP_BUILD_COMMIT="${commit}" \
  "${repository_directory}/scripts/build-compute-linux-amd64.sh" "${build_directory}" >/dev/null

declare -A artifacts=(
  [fire-fuse]="fire-fuse-linux-amd64"
  [cantelop]="cantelop-linux-amd64"
  [fire-fuse-snapshot]="fire-fuse-snapshot-linux-amd64"
  [fire-fuse-image]="fire-fuse-image-linux-amd64"
  [fire-fuse-gateway]="fire-fuse-gateway-linux-amd64"
  [fire-fuse-async-dispatcher]="fire-fuse-async-dispatcher-linux-amd64"
  [fire-fuse-managed-watchdog]="fire-fuse-managed-watchdog-linux-amd64"
  [fire-fuse-observer]="fire-fuse-observer-linux-amd64"
  [fire-fuse-integration]="fire-fuse-integration-linux-amd64"
)
for destination in "${!artifacts[@]}"; do
  install -m 0755 "${build_directory}/${artifacts[${destination}]}" \
    "${bundle_directory}/bin/${destination}"
done

python3 - "${bundle_directory}/manifest.json" "${deployment_revision}" "${commit}" <<'PY'
import json
import pathlib
import sys

path, revision, commit = sys.argv[1:]
manifest = {
    "schema_version": 2,
    "deployment_revision": revision,
    "source_commit": commit,
    "platform": {"os": "linux", "architecture": "amd64"},
    "state_schema_version": 28,
    "artifacts": [
        "cantelop",
        "fire-fuse",
        "fire-fuse-async-dispatcher",
        "fire-fuse-gateway",
        "fire-fuse-image",
        "fire-fuse-integration",
        "fire-fuse-managed-watchdog", "fire-fuse-observer",
        "fire-fuse-snapshot",
    ],
}
pathlib.Path(path).write_text(json.dumps(manifest, indent=2) + "\n")
PY

python3 "${repository_directory}/scripts/release-notices.py" prepare "${bundle_directory}" --commit "${commit}"
notice_list="$(python3 "${repository_directory}/scripts/release-notices.py" list)"
(
  cd "${bundle_directory}"
  # Notice paths contain no whitespace, so the newline-separated list splits safely.
  # shellcheck disable=SC2086
  sha256_files bin/* manifest.json ${notice_list} >SHA256SUMS
)
COPYFILE_DISABLE=1 tar -C "${work_directory}" -czf "${output_directory}/${archive_name}" \
  "$(basename "${bundle_directory}")"
(
  cd "${output_directory}"
  sha256_files "${archive_name}" >SHA256SUMS
)
printf 'Fire Fuse deployment %s (%s) written to %s\n' \
  "${deployment_revision}" "${commit:0:12}" "${output_directory}"
