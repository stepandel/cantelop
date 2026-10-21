#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
output_directory="${1:-${repository_directory}/dist/cli}"
version="$(tr -d '\n' <"${repository_directory}/clients/cli/VERSION")"
commit="${CANTELOP_BUILD_COMMIT:-$(git -C "${repository_directory}" rev-parse HEAD)}"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "clients/cli/VERSION is not a supported semantic version" >&2
  exit 1
fi
if [[ ! "${commit}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "CANTELOP_BUILD_COMMIT must be a full lowercase Git commit" >&2
  exit 1
fi

mkdir -p "${output_directory}"
make_temporary_directory temporary_directory cantelop-cli-release
notices_directory="${temporary_directory}/notices"
python3 "${repository_directory}/scripts/release-notices.py" prepare "${notices_directory}" --commit "${commit}"
notice_list="$(python3 "${repository_directory}/scripts/release-notices.py" list)"
notice_files=()
while IFS= read -r notice_file; do
  notice_files+=("${notice_file}")
done <<<"${notice_list}"

platforms=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
)

archives=()
for platform in "${platforms[@]}"; do
  read -r operating_system architecture <<<"${platform}"
  archive="cantelop_${version}_${operating_system}_${architecture}.tar.gz"
  package_directory="${temporary_directory}/${operating_system}-${architecture}"
  mkdir -p "${package_directory}"
  (
    cd "${repository_directory}"
    CGO_ENABLED=0 GOOS="${operating_system}" GOARCH="${architecture}" go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -X main.version=${version} -X main.commit=${commit:0:12}" \
      -o "${package_directory}/cantelop" \
      ./clients/cli/cmd/cantelop
  )
  cp "${repository_directory}/docs/cli.md" "${package_directory}/README.md"
  cp -R "${notices_directory}/." "${package_directory}/"
  COPYFILE_DISABLE=1 tar -C "${package_directory}" -czf "${output_directory}/${archive}" \
    cantelop README.md "${notice_files[@]}"
  archives+=("${archive}")
done

(
  cd "${output_directory}"
  sha256_files "${archives[@]}" >SHA256SUMS
)

printf 'Cantelop CLI %s release candidates written to %s\n' "${version}" "${output_directory}"
