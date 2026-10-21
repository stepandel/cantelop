#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
commit="${CANTELOP_BUILD_COMMIT:-$(git -C "${repository_directory}" rev-parse HEAD)}"
deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${commit:0:12}}"
make_temporary_directory work_directory cantelop-fire-fuse-check
bundle_name="fire-fuse_${deployment_revision}_linux-amd64"

if [[ "$#" == 0 ]]; then
  release_directory="${work_directory}/release"
  CANTELOP_BUILD_COMMIT="${commit}" CANTELOP_DEPLOYMENT_REVISION="${deployment_revision}" \
    "${repository_directory}/scripts/build-fire-fuse-release.sh" "${release_directory}"
elif [[ "$#" == 2 && "$1" == "--validate" ]]; then
  release_directory="$2"
  if [[ ! -d "${release_directory}" ]]; then
    echo "Fire Fuse release directory does not exist: ${release_directory}" >&2
    exit 1
  fi
else
  echo "usage: $0 [--validate RELEASE_DIRECTORY]" >&2
  exit 2
fi
(
  cd "${release_directory}"
  sha256_files --check SHA256SUMS
)
python3 "${repository_directory}/scripts/release-notices.py" verify-archives \
  --commit "${commit}" --prefix "${bundle_name}" "${release_directory}/${bundle_name}.tar.gz"
tar -C "${work_directory}" -xzf "${release_directory}/${bundle_name}.tar.gz"
(
  cd "${work_directory}/${bundle_name}"
  sha256_files --check SHA256SUMS
)
python3 - "${work_directory}/${bundle_name}" "${deployment_revision}" "${commit}" "${repository_directory}" <<'PY'
import json
import pathlib
import runpy
import sys

root, revision, commit = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
manifest = json.loads((root / "manifest.json").read_text())
expected = {
    "schema_version": 2,
    "deployment_revision": revision,
    "source_commit": commit,
    "platform": {"os": "linux", "architecture": "amd64"},
    "state_schema_version": 28,
    "artifacts": [
        "cantelop", "fire-fuse", "fire-fuse-async-dispatcher", "fire-fuse-gateway", "fire-fuse-image",
        "fire-fuse-integration", "fire-fuse-managed-watchdog", "fire-fuse-observer", "fire-fuse-snapshot",
    ],
}
if manifest != expected:
    raise SystemExit("unexpected Fire Fuse release manifest")
notices = runpy.run_path(str(pathlib.Path(sys.argv[4]) / "scripts/release-notices.py"))
expected_files = {"SHA256SUMS", "manifest.json", *notices["archive_files"]()} | {f"bin/{name}" for name in expected["artifacts"]}
actual_files = {str(path.relative_to(root)) for path in root.rglob("*") if path.is_file()}
if actual_files != expected_files:
    raise SystemExit(f"unexpected Fire Fuse bundle files: {sorted(actual_files ^ expected_files)}")
PY

if ! strings "${work_directory}/${bundle_name}/bin/fire-fuse" | grep -Fx "${deployment_revision}" >/dev/null; then
  echo "Fire Fuse binary does not contain the deployment revision" >&2
  exit 1
fi
if ! strings "${work_directory}/${bundle_name}/bin/fire-fuse" | grep -Fx "${commit}" >/dev/null; then
  echo "Fire Fuse binary does not contain the source commit" >&2
  exit 1
fi
cli_version="$(tr -d '\n' <"${repository_directory}/clients/cli/VERSION")"
if ! strings "${work_directory}/${bundle_name}/bin/cantelop" | grep -Fx "${cli_version}" >/dev/null; then
  echo "bundled operator CLI does not contain the CLI version" >&2
  exit 1
fi
if ! strings "${work_directory}/${bundle_name}/bin/cantelop" | grep -Fx "${commit:0:12}" >/dev/null; then
  echo "bundled operator CLI does not contain the source commit" >&2
  exit 1
fi
printf 'Qualified Fire Fuse deployment %s (%s) bundle\n' \
  "${deployment_revision}" "${commit:0:12}"
