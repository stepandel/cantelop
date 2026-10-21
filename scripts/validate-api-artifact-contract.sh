#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
if [[ "$#" != 1 || ! -d "$1" ]]; then
  echo "usage: validate-api-artifact-contract.sh ARTIFACT_DIRECTORY" >&2
  exit 1
fi
artifact_directory="$(cd -- "$1" && pwd -P)"
make_temporary_directory temporary_directory cantelop-api-artifact-contract

(
  cd "${repository_directory}"
  CANTELOP_QUALIFICATION_API_ARTIFACT="${artifact_directory}" \
    GOCACHE="${temporary_directory}/go-build" \
    go test ./clients/cli/internal/projectconfig -run '^TestLoadQualifiedSDKAPIArtifact$' -count=1
)

(
  cd "${repository_directory}/console"
  bun --eval '
    import { readFile } from "node:fs/promises";
    import { parseAPIArtifactManifest } from "./core/api-deployments/artifact.ts";
    const manifest = JSON.parse(await readFile(process.argv[1], "utf8"));
    parseAPIArtifactManifest(manifest);
  ' "$(if [[ -f "${artifact_directory}/cantelop-edge.json" ]]; then echo "${artifact_directory}/cantelop-edge.json"; else echo "${artifact_directory}/cantelop-api.json"; fi)"
)
