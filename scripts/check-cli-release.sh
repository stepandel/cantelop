#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
commit="${CANTELOP_BUILD_COMMIT:-$(git -C "${repository_directory}" rev-parse HEAD)}"
make_temporary_directory temporary_directory cantelop-cli-check

if [[ "$#" == 0 ]]; then
  release_directory="${temporary_directory}/release"
  "${repository_directory}/scripts/build-cli-release.sh" "${release_directory}"
elif [[ "$#" == 2 && "$1" == "--validate" ]]; then
  release_directory="$2"
  if [[ ! -d "${release_directory}" ]]; then
    echo "CLI release directory does not exist: ${release_directory}" >&2
    exit 1
  fi
else
  echo "usage: $0 [--validate RELEASE_DIRECTORY]" >&2
  exit 2
fi

archive_count="$(find "${release_directory}" -maxdepth 1 -type f -name 'cantelop_*.tar.gz' | wc -l | tr -d ' ')"
if [[ "${archive_count}" != "4" ]]; then
  echo "CLI release must contain exactly four platform archives" >&2
  exit 1
fi

(
  cd "${release_directory}"
  sha256_files --check SHA256SUMS
)
python3 "${repository_directory}/scripts/release-notices.py" verify-archives \
  --commit "${commit}" "${release_directory}"/cantelop_*.tar.gz

case "$(uname -s)" in
  Darwin) native_os="darwin" ;;
  Linux) native_os="linux" ;;
  *) echo "unsupported qualification operating system" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) native_arch="arm64" ;;
  x86_64) native_arch="amd64" ;;
  *) echo "unsupported qualification architecture" >&2; exit 1 ;;
esac

version="$(tr -d '\n' <"${repository_directory}/clients/cli/VERSION")"
native_archive="${release_directory}/cantelop_${version}_${native_os}_${native_arch}.tar.gz"
mkdir "${temporary_directory}/native"
tar -C "${temporary_directory}/native" -xzf "${native_archive}"
actual="$("${temporary_directory}/native/cantelop" version)"
if [[ ! "${actual}" =~ ^cantelop\ ${version}\ \([0-9a-f]{12}\)$ ]]; then
  echo "native CLI reported an invalid embedded version: ${actual}" >&2
  exit 1
fi
if [[ "${actual}" != "cantelop ${version} (${commit:0:12})" ]]; then
  echo "native CLI source identity does not match the selected commit" >&2
  exit 1
fi
if [[ ! -s "${temporary_directory}/native/README.md" ]]; then
  echo "native CLI archive omitted README.md" >&2
  exit 1
fi

printf 'Qualified %s across four release archives\n' "${actual}"
