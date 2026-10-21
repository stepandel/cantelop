#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
sdk_directory="${CANTELOP_SDK_DIRECTORY:-${repository_directory}/../cantelop-sdk}"
make_temporary_directory temporary_directory cantelop-sdk-platform-contract

if [[ ! -f "${sdk_directory}/package.json" || ! -f "${sdk_directory}/dist/build.js" ]]; then
  echo "CANTELOP_SDK_DIRECTORY must point to a built Cantelop SDK repository" >&2
  exit 1
fi

sdk_version="$(node -p 'require(process.argv[1]).version' "${sdk_directory}/package.json")"
node --input-type=module --eval '
  import { mkdir, writeFile } from "node:fs/promises";
  import path from "node:path";
  import { pathToFileURL } from "node:url";

  const [sdkDirectory, outputDirectory] = process.argv.slice(1);
  const build = await import(pathToFileURL(path.join(sdkDirectory, "dist/build.js")));
  if (build.CANTELOP_CLI_BUILD_PROTOCOL_VERSION !== 6 ||
      typeof build.buildAppArtifacts !== "function" || typeof build.buildSessionRuntime !== "function") {
    throw new Error("adjacent SDK does not implement App build protocol 6");
  }

  const sourceDirectory = path.join(outputDirectory, "source");
  await mkdir(sourceDirectory, { recursive: true });
  const apiImport = path.join(sdkDirectory, "dist/index.js");
  const authImport = path.join(sdkDirectory, "dist/harnesses/claude.js");
  const apiEntrypoint = path.join(sourceDirectory, "api.mjs");
  await writeFile(apiEntrypoint,
    `import { CantelopClient } from ${JSON.stringify(apiImport)};\n` +
    `import { claudeAuth } from ${JSON.stringify(authImport)};\n` +
    `const cantelop = new CantelopClient(); export const agent = cantelop.app({name:"contract-agent",runtime:{receive() {}}}); export const native = cantelop.app({name:"contract-native-auth",auth:claudeAuth,runtime:{receive() {}}});\n`);
  await build.buildAppArtifacts({definition:apiEntrypoint,outdir:outputDirectory,projectRoot:sourceDirectory});
' "${sdk_directory}" "${temporary_directory}"

test -s "${temporary_directory}/contract-agent/edge/cantelop-edge.json"
test -s "${temporary_directory}/contract-agent/edge/worker.mjs"
test -s "${temporary_directory}/contract-agent/runtime/session-runtime.mjs"

(
  cd "${repository_directory}/console"
  bun --eval '
    import { readFile } from "node:fs/promises";
    import { parseAPIArtifactManifest } from "./core/api-deployments/artifact.ts";
    import { parseSessionRuntimeManifest, validateArtifactPair } from "./core/api-deployments/protocol-artifact.ts";
    for (const name of ["contract-agent", "contract-native-auth"]) {
      const directory = `${process.argv[1]}/${name}`;
      const edge = parseAPIArtifactManifest(JSON.parse(await readFile(`${directory}/edge/cantelop-edge.json`, "utf8")));
      const runtime = parseSessionRuntimeManifest(JSON.parse(await readFile(`${directory}/runtime/cantelop-runtime.json`, "utf8")));
      validateArtifactPair(edge, runtime);
    }
  ' "${temporary_directory}"
)

cd "${repository_directory}"
CANTELOP_SDK_DIRECTORY="${sdk_directory}" go test ./clients/cli/internal/devruntime -run TestSDKOneAppArtifacts -count=1

printf 'Qualified SDK %s artifacts through CLI and Console boundaries\n' "${sdk_version}"
