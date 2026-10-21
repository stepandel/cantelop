#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_directory="$(cd -- "${script_directory}/../.." && pwd)"
workers_directory="${repository_directory}/edge/workers"

put_environment_secret() {
  local variable_name="$1"
  local secret_name="$2"
  local config_path="$3"
  printf '%s' "${!variable_name}" |
    bunx wrangler secret put "${secret_name}" --config "${config_path}"
}

for variable_name in \
  APP_ROUTES_KV_NAMESPACE_ID \
  EDGE_INGRESS_TOKEN \
  ORIGIN_ACCESS_CLIENT_ID \
  ORIGIN_ACCESS_CLIENT_SECRET \
  ORIGIN_URL \
  OBSERVABILITY_INGEST_TOKEN \
  RELEASE_GATE_TOKEN; do
  if [[ -z "${!variable_name:-}" ]]; then
    echo "${variable_name} is required" >&2
    exit 1
  fi
done

if [[ ! "${APP_ROUTES_KV_NAMESPACE_ID}" =~ ^[0-9a-f]{32}$ ]]; then
  echo "APP_ROUTES_KV_NAMESPACE_ID is malformed" >&2
  exit 1
fi
if [[ ! "${RELEASE_GATE_TOKEN}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "RELEASE_GATE_TOKEN is malformed" >&2
  exit 1
fi

cd "${workers_directory}"
bun install --frozen-lockfile
bunx wrangler whoami >/dev/null

bunx wrangler deploy --config origin-broker/wrangler.jsonc
put_environment_secret ORIGIN_URL ORIGIN_URL origin-broker/wrangler.jsonc
put_environment_secret EDGE_INGRESS_TOKEN EDGE_INGRESS_TOKEN origin-broker/wrangler.jsonc
put_environment_secret ORIGIN_ACCESS_CLIENT_ID ORIGIN_ACCESS_CLIENT_ID origin-broker/wrangler.jsonc
put_environment_secret ORIGIN_ACCESS_CLIENT_SECRET ORIGIN_ACCESS_CLIENT_SECRET origin-broker/wrangler.jsonc

bunx wrangler deploy --config outbound/wrangler.jsonc

# The Tail Worker must exist before the dispatcher can name it as a consumer.
# It receives logs from the dispatcher and every user Worker in the dispatch
# namespace, then writes correlated observations with the edge-only credential.
bunx wrangler deploy --config tail/wrangler.jsonc
put_environment_secret OBSERVABILITY_INGEST_TOKEN OBSERVABILITY_INGEST_TOKEN tail/wrangler.jsonc

dispatcher_config="$(mktemp "${workers_directory}/dispatcher/wrangler.generated.XXXXXX.json")"
trap 'rm -f "${dispatcher_config}"' EXIT
jq --arg namespace_id "${APP_ROUTES_KV_NAMESPACE_ID}" \
  '. + {kv_namespaces: [{binding: "APP_ROUTES", id: $namespace_id}]} | del(.tail_consumers)' \
  dispatcher/wrangler.jsonc >"${dispatcher_config}"
bunx wrangler deploy --config "${dispatcher_config}"
put_environment_secret OBSERVABILITY_INGEST_TOKEN OBSERVABILITY_INGEST_TOKEN "${dispatcher_config}"
put_environment_secret RELEASE_GATE_TOKEN RELEASE_GATE_TOKEN "${dispatcher_config}"
# Tail consumers are non-versioned settings. Cloudflare's script upload API
# rejects them on some Workers for Platforms accounts even though its settings
# API accepts the same attachment. Sync and read back the attachment explicitly.
bun "${script_directory}/sync-tail-settings.ts" dispatcher/wrangler.jsonc
