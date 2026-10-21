#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
sdk_directory="${CANTELOP_SDK_DIRECTORY:-${repository_directory}/../cantelop-sdk}"
temporary_directory="$(mktemp -d "${TMPDIR:-/tmp}/cantelop-starter-check.XXXXXX")"
container_name="cantelop-starter-check-$$"
image_id=""
container_started=false

# The public qualification must not depend on a developer's registry credential
# helper. Preserve the selected daemon endpoint, then use an empty Docker config
# for public base-image access and all ephemeral resources below.
docker_host="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
if [[ "${docker_host}" != unix://* && "${docker_host}" != tcp://127.0.0.1:* && "${docker_host}" != tcp://localhost:* ]]; then
  echo "Docker context resolved to an unsupported daemon endpoint" >&2
  exit 1
fi
mkdir "${temporary_directory}/docker-config"
export DOCKER_HOST="${docker_host}"
export DOCKER_CONFIG="${temporary_directory}/docker-config"

cleanup() {
  if [[ "${container_started}" == true ]]; then
    docker rm -f "${container_name}" >/dev/null 2>&1 || true
  fi
  if [[ "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    docker image rm "${image_id}" >/dev/null 2>&1 || true
  fi
  rm -rf "${temporary_directory}"
}
trap cleanup EXIT

if [[ ! -f "${sdk_directory}/package.json" ]]; then
  echo "CANTELOP_SDK_DIRECTORY must point to the Cantelop SDK repository" >&2
  exit 1
fi
sdk_version="$(node -p "require('${sdk_directory}/package.json').version")"
sdk_build_protocol="$(node --input-type=module --eval '
  const sdk = await import(process.argv[1]);
  process.stdout.write(String(sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION));
' "${sdk_directory}/dist/build.js")"
if [[ "${sdk_build_protocol}" != 6 ]]; then
  echo "SDK ${sdk_version} uses build protocol ${sdk_build_protocol}; App starters require protocol 6" >&2
  exit 1
fi
starter_sdk_version="$(node -p "require('${repository_directory}/examples/hello/package.json').dependencies['@cantelop/sdk']")"
if [[ "${sdk_version}" != "${starter_sdk_version}" ]]; then
  echo "SDK ${sdk_version} and starter dependency ${starter_sdk_version} do not match" >&2
  exit 1
fi
mkdir -p "${temporary_directory}/project"
touch "${temporary_directory}/npmrc"
export NPM_CONFIG_USERCONFIG="${temporary_directory}/npmrc"
cp -R "${repository_directory}/examples/hello/." "${temporary_directory}/project/"

(
  cd "${temporary_directory}/project"
  npm pack "${sdk_directory}" --ignore-scripts --pack-destination "${temporary_directory}" >/dev/null
  npm install "${temporary_directory}/cantelop-sdk-${sdk_version}.tgz" --ignore-scripts --no-audit --no-fund --package-lock=false
  bun test src/agent.test.ts
  npm run build

)
test -s "${temporary_directory}/project/dist/hello/edge/worker.mjs"
test -s "${temporary_directory}/project/dist/hello/edge/cantelop-edge.json"
test -s "${temporary_directory}/project/dist/hello/runtime/session-runtime.mjs"
"${repository_directory}/scripts/validate-api-artifact-contract.sh" \
  "${temporary_directory}/project/dist/hello/edge"
mkdir "${temporary_directory}/project/.cantelop-qualification"
cp "${temporary_directory}/project/dist/hello/runtime/session-runtime.mjs" \
  "${temporary_directory}/project/.cantelop-qualification/session-runtime.mjs"

docker build --platform linux/amd64 \
  --file "${repository_directory}/scripts/starter-qualification.Dockerfile" \
  --iidfile "${temporary_directory}/image-id" \
  "${temporary_directory}/project"
image_id="$(tr -d '\n' <"${temporary_directory}/image-id")"
if [[ ! "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "Docker returned an invalid starter image ID" >&2
  exit 1
fi
docker run --detach --platform linux/amd64 --name "${container_name}" --env CANTELOP_INTERNAL_PORT=57321 --env CANTELOP_SANDBOX_ID=sbx-11111111111111111111111111111111 --publish 127.0.0.1::57321 "${image_id}" >/dev/null
container_started=true
mapping="$(docker port "${container_name}" 57321/tcp)"
port="${mapping##*:}"
if [[ ! "${port}" =~ ^[0-9]{1,5}$ ]] || (( port < 1 || port > 65535 )); then
  echo "Docker returned an invalid starter port mapping" >&2
  exit 1
fi

node --input-type=module --eval '
  const origin = process.argv[1];
  const messageID = "msg_0123456789abcdef0123456789abcdef";
  let response;
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      response = await fetch(`${origin}/__cantelop/v2/messages`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Cantelop-Sandbox-ID":"sbx-11111111111111111111111111111111" },
        body: JSON.stringify({
          session: {
            id: "qualification",
            workspace_id: "wsp_0123456789abcdef0123456789abcdef",
            keep_alive_seconds: 300,
          },
          message: { id: messageID, payload: { message: "qualified" } },
        }),
      });
      if (response.ok) break;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!response?.ok) throw new Error("starter session-runtime did not become ready");
  const receipt = await response.json();
  if(response.status !== 202 || receipt.message_id !== messageID || receipt.sandbox_id !== "sbx-11111111111111111111111111111111") throw new Error("invalid mailbox receipt");
  const headers = {"X-Cantelop-Sandbox-ID":receipt.sandbox_id};
  let settled = false;
  const cursors = { events: 0, observations: 0 };
  let sawEcho = false;
  for(let attempt=0;attempt<100;attempt++){
    // Act as the broker: consume and acknowledge output before awaiting quiescence.
    for (const kind of ["events", "observations"]) {
      const batch = await (await fetch(`${origin}/__cantelop/v2/runtime/${kind}?after=${cursors[kind]}&wait=0`, {headers})).json();
      if (!Array.isArray(batch[kind])) throw new Error("invalid runtime output batch");
      for (const item of batch[kind]) {
        cursors[kind] = item.cursor;
        if (kind === "events" && item.event?.message === "qualified") sawEcho = true;
      }
      const acknowledged = await fetch(`${origin}/__cantelop/v2/runtime/${kind}/ack`, {
        method: "POST", headers: {...headers, "content-type": "application/json"},
        body: JSON.stringify({through: cursors[kind]}),
      });
      if (!acknowledged.ok) throw new Error("runtime output acknowledgment failed");
    }
    const status = await (await fetch(`${origin}/__cantelop/v2/messages/${messageID}`,{headers})).json();
    const runtime = await (await fetch(`${origin}/__cantelop/v2/runtime`,{headers})).json();
    if(status.state === "failed" || status.state === "timed_out") throw new Error("starter handler failed");
    if(status.state === "succeeded" && runtime.quiescent && runtime.generation>=receipt.generation){settled=true;break;}
    await new Promise(resolve=>setTimeout(resolve,100));
  }
  if(!settled || !sawEcho) throw new Error("starter did not echo, settle and become quiescent");
' "http://127.0.0.1:${port}"

printf 'Qualified hello starter with built SDK %s and image %s\n' "${sdk_version}" "${image_id}"
