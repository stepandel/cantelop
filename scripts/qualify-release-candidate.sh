#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${repository_directory}/scripts/lib/common.sh"
sdk_directory="${CANTELOP_SDK_DIRECTORY:-${repository_directory}/../cantelop-sdk}"
cli_version="$(tr -d '\n' <"${repository_directory}/clients/cli/VERSION")"
sdk_version="$(node -p "require('${sdk_directory}/package.json').version")"
cli_commit="$(git -C "${repository_directory}" rev-parse HEAD)"
deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${cli_commit:0:12}}"
sdk_commit="$(git -C "${sdk_directory}" rev-parse HEAD)"
qualified_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
default_run="${cli_version}-${cli_commit:0:12}-$(date -u +%Y%m%dT%H%M%SZ)"
output_directory="${1:-${repository_directory}/dist/release-qualification/${default_run}}"

for candidate in "${cli_version}" "${sdk_version}"; do
  if [[ ! "${candidate}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "release candidate versions must use supported semantic versions" >&2
    exit 1
  fi
done
if [[ ! "${deployment_revision}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "deployment revision must be a safe 1-64 character identifier" >&2
  exit 1
fi
for repository in "${repository_directory}" "${sdk_directory}"; do
  if [[ "$(git -C "${repository}" branch --show-current)" != "main" ]]; then
    echo "release qualification requires both repositories on main" >&2
    exit 1
  fi
  if [[ -n "$(git -C "${repository}" status --porcelain)" ]]; then
    echo "release qualification requires clean repositories: ${repository}" >&2
    exit 1
  fi
done
if [[ -e "${output_directory}" ]]; then
  echo "qualification output already exists: ${output_directory}" >&2
  exit 1
fi
mkdir -p "${output_directory}/logs" "${output_directory}/artifacts/sdk" \
  "${output_directory}/artifacts/cli" "${output_directory}/artifacts/fire-fuse"

run_check() {
  local name="$1"
  shift
  local log_path="${output_directory}/logs/${name}.log"
  local started_at started_epoch completed_at completed_epoch duration_seconds status
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  started_epoch="$(date +%s)"
  printf '==> %s (started %s)\n' "${name}" "${started_at}" | tee "${log_path}"
  set +e
  "$@" 2>&1 | tee -a "${log_path}"
  status="${PIPESTATUS[0]}"
  set -e
  completed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  completed_epoch="$(date +%s)"
  duration_seconds="$((completed_epoch - started_epoch))"
  printf '<== %s (completed %s, duration %ss, status %s)\n' \
    "${name}" "${completed_at}" "${duration_seconds}" "${status}" | tee -a "${log_path}"
  return "${status}"
}

run_check public-source make -C "${repository_directory}" check-public-source
run_check cantelop-check make -C "${repository_directory}" check
run_check control-worker-build bun "${repository_directory}/scripts/check-control-worker-build.ts"
run_check sdk-check pnpm --dir "${sdk_directory}" check
run_check sdk-test pnpm --dir "${sdk_directory}" test
run_check message-lifecycle env CANTELOP_SDK_RUNTIME_TEST_MODULE="${sdk_directory}/dist/runtime.js" go test -race \
  "${repository_directory}/fire-fuse/runtime/httpapi" \
  -run 'TestSDKMailbox' -count=1
run_check local-remote-messaging-parity env CANTELOP_SDK_RUNTIME_TEST_MODULE="${sdk_directory}/dist/runtime.js" go test -race \
  "${repository_directory}/clients/cli/internal/devruntime" "${repository_directory}/fire-fuse/gateway" \
  -run 'Test(LocalSDKMessagingParity|LocalPublicSDKContract|RemotePublicSDKContract)' -count=1
run_check local-workspace-databases env CANTELOP_SDK_DATABASE_TEST_MODULE="${sdk_directory}/dist/database.js" \
  CANTELOP_LOCAL_DATABASE_CONTAINER_TEST=1 go test -race \
  "${repository_directory}/clients/cli/internal/devruntime" \
  -run 'Test(LocalDatabase|LocalSDKWorkspaceDatabase|ProjectDevWorkspaceDatabaseRoundTrip|NativeSandboxProcessExit)' -count=1
run_check sdk-examples pnpm --dir "${sdk_directory}" check:examples
run_check sdk-package pnpm --dir "${sdk_directory}" check:package
run_check application-mailbox-transport go test -race \
  "${repository_directory}/fire-fuse/dispatcher" "${repository_directory}/fire-fuse/gateway" \
  "${repository_directory}/fire-fuse/messageprotocol"
run_check fire-fuse-state make -C "${repository_directory}" check-fire-fuse-state
run_check sdk-platform-contract env CANTELOP_SDK_DIRECTORY="${sdk_directory}" \
  make -C "${repository_directory}" check-sdk-platform-contract
run_check starter env CANTELOP_SDK_DIRECTORY="${sdk_directory}" make -C "${repository_directory}" check-starter

run_check sdk-artifact npm pack "${sdk_directory}" \
  --pack-destination "${output_directory}/artifacts/sdk"
run_check cli-build env CANTELOP_BUILD_COMMIT="${cli_commit}" \
  "${repository_directory}/scripts/build-cli-release.sh" "${output_directory}/artifacts/cli"
run_check cli-release "${repository_directory}/scripts/check-cli-release.sh" \
  --validate "${output_directory}/artifacts/cli"
run_check fire-fuse-build env CANTELOP_BUILD_COMMIT="${cli_commit}" \
  CANTELOP_DEPLOYMENT_REVISION="${deployment_revision}" \
  "${repository_directory}/scripts/build-fire-fuse-release.sh" \
  "${output_directory}/artifacts/fire-fuse"
run_check fire-fuse-release env CANTELOP_BUILD_COMMIT="${cli_commit}" \
  CANTELOP_DEPLOYMENT_REVISION="${deployment_revision}" \
  "${repository_directory}/scripts/check-fire-fuse-release.sh" \
  --validate "${output_directory}/artifacts/fire-fuse"

(
  cd "${output_directory}/artifacts"
  sha256_files sdk/*.tgz cli/*.tar.gz cli/SHA256SUMS \
    fire-fuse/*.tar.gz fire-fuse/SHA256SUMS >SHA256SUMS
)

node -e '
  const fs = require("node:fs");
  const [path, cliVersion, deploymentRevision, sdkVersion, qualifiedAt, cliCommit, sdkCommit] = process.argv.slice(1);
  const evidence = {
    schema_version: 5,
    versions: {
      sdk: sdkVersion,
      cli: cliVersion,
    },
    deployment: {
      revision: deploymentRevision,
      source_commit: cliCommit,
    },
    qualified_at: qualifiedAt,
    source: {
      cantelop: { branch: "main", commit: cliCommit },
      sdk: { branch: "main", commit: sdkCommit },
    },
    checks: [
      "public-source",
      "cantelop-check",
      "control-worker-build",
      "message-lifecycle",
      "local-remote-messaging-parity",
      "local-workspace-databases",
      "application-mailbox-transport",
      "sdk-check",
      "sdk-test",
      "sdk-examples",
      "sdk-package",
      "fire-fuse-state",
      "sdk-platform-contract",
      "starter",
      "sdk-artifact",
      "cli-build",
      "cli-release",
      "fire-fuse-build",
      "fire-fuse-release",
    ],
    artifacts: {
      sdk: `cantelop-sdk-${sdkVersion}.tgz`,
      cli_platforms: ["darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"],
      fire_fuse: `fire-fuse_${deploymentRevision}_linux-amd64.tar.gz`,
      fire_fuse_platform: "linux-amd64",
      fire_fuse_state_schema_version: 28,
      checksums: "artifacts/SHA256SUMS",
    },
    production_operations_performed: false,
  };
  fs.writeFileSync(path, `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o644 });
' "${output_directory}/evidence.json" "${cli_version}" "${deployment_revision}" "${sdk_version}" "${qualified_at}" "${cli_commit}" "${sdk_commit}"

for repository in "${repository_directory}" "${sdk_directory}"; do
  if [[ -n "$(git -C "${repository}" status --porcelain)" ]]; then
    echo "qualification modified tracked repository state: ${repository}" >&2
    exit 1
  fi
done

printf 'Qualified CLI %s with deployment %s without production operations\nEvidence: %s\n' \
  "${cli_version}" "${deployment_revision}" "${output_directory}/evidence.json"
