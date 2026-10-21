#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  printf '%s\n' \
    'Usage:' \
    '  robot-server.sh products [PRODUCT_ID]' \
    '  robot-server.sh keys' \
    '  robot-server.sh servers' \
    '  robot-server.sh add-key NAME PUBLIC_KEY_PATH' \
    '  robot-server.sh order [--execute]' \
    '  robot-server.sh order-status TRANSACTION_ID' \
    '  robot-server.sh cancel SERVER_NUMBER [--execute]' \
    '' \
    'Required for every command:' \
    '  HETZNER_ROBOT_USER, HETZNER_ROBOT_PASSWORD' \
    '' \
    'Required for order:' \
    '  HETZNER_PRODUCT_ID, HETZNER_LOCATION, HETZNER_DIST, HETZNER_SSH_KEY_FINGERPRINT' \
    '  Set HETZNER_ADD_IPV4=1 to request a primary IPv4 address.' \
    '  Without --execute, order sends test=true and cancel performs only a GET.'
}

fail() {
  printf 'robot-server: %s\n' "$*" >&2
  exit 1
}

[[ $# -ge 1 ]] || { usage; exit 2; }

command_name="$1"
shift
if [[ "${command_name}" == 'help' || "${command_name}" == '-h' || "${command_name}" == '--help' ]]; then
  usage
  exit 0
fi

[[ -n "${HETZNER_ROBOT_USER:-}" ]] || fail 'HETZNER_ROBOT_USER is required'
[[ -n "${HETZNER_ROBOT_PASSWORD:-}" ]] || fail 'HETZNER_ROBOT_PASSWORD is required'
[[ "${HETZNER_ROBOT_USER}" != *$'\n'* && "${HETZNER_ROBOT_USER}" != *$'\r'* ]] || fail 'Robot user contains a newline'
[[ "${HETZNER_ROBOT_PASSWORD}" != *$'\n'* && "${HETZNER_ROBOT_PASSWORD}" != *$'\r'* ]] || fail 'Robot password contains a newline'

netrc_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "${value}"
}

netrc_file="$(mktemp)"
trap 'rm -f "${netrc_file}"' EXIT
chmod 0600 "${netrc_file}"
{
  printf 'machine robot-ws.your-server.de login '
  netrc_quote "${HETZNER_ROBOT_USER}"
  printf ' password '
  netrc_quote "${HETZNER_ROBOT_PASSWORD}"
  printf '\n'
} > "${netrc_file}"

base_url='https://robot-ws.your-server.de'
curl_robot() {
  curl \
    --fail-with-body \
    --silent \
    --show-error \
    --netrc-file "${netrc_file}" \
    "$@"
}

case "${command_name}" in
  add-key)
    [[ $# -eq 2 ]] || fail 'add-key requires a name and public-key path'
    key_name="$1"
    key_path="$2"
    [[ -n "${key_name}" && "${key_name}" != *$'\n'* && "${key_name}" != *$'\r'* ]] || \
      fail 'invalid SSH key name'
    [[ -f "${key_path}" && -s "${key_path}" ]] || \
      fail "SSH public key is not a non-empty regular file: ${key_path}"
    curl_robot \
      --data-urlencode "name=${key_name}" \
      --data-urlencode "data@${key_path}" \
      "${base_url}/key"
    ;;
  keys)
    [[ $# -eq 0 ]] || fail 'keys accepts no arguments'
    curl_robot "${base_url}/key"
    ;;
  servers)
    [[ $# -eq 0 ]] || fail 'servers accepts no arguments'
    curl_robot "${base_url}/server"
    ;;
  products)
    [[ $# -le 1 ]] || fail 'products accepts at most one product ID'
    if [[ $# -eq 1 ]]; then
      [[ "$1" =~ ^[A-Za-z0-9._-]+$ ]] || fail 'invalid product ID'
    fi
    curl_robot "${base_url}/order/server/product${1:+/$1}"
    ;;
  order)
    execute=0
    if [[ "${1:-}" == '--execute' ]]; then
      execute=1
      shift
    fi
    [[ $# -eq 0 ]] || fail 'order accepts only the optional --execute flag'
    for variable in HETZNER_PRODUCT_ID HETZNER_LOCATION HETZNER_DIST HETZNER_SSH_KEY_FINGERPRINT; do
      [[ -n "${!variable:-}" ]] || fail "${variable} is required for order"
    done
    arguments=(
      --data-urlencode "product_id=${HETZNER_PRODUCT_ID}"
      --data-urlencode "location=${HETZNER_LOCATION}"
      --data-urlencode "dist=${HETZNER_DIST}"
      --data-urlencode "authorized_key[]=${HETZNER_SSH_KEY_FINGERPRINT}"
    )
    if [[ "${HETZNER_ADD_IPV4:-0}" == '1' ]]; then
      arguments+=(--data-urlencode 'addon[]=primary_ipv4')
    fi
    if [[ "${execute}" == '0' ]]; then
      arguments+=(--data-urlencode 'test=true')
    fi
    curl_robot "${arguments[@]}" "${base_url}/order/server/transaction"
    ;;
  order-status)
    [[ $# -eq 1 ]] || fail 'order-status requires one transaction ID'
    [[ "$1" =~ ^[A-Za-z0-9-]+$ ]] || fail 'invalid transaction ID'
    curl_robot "${base_url}/order/server/transaction/$1"
    ;;
  cancel)
    [[ $# -ge 1 && $# -le 2 ]] || fail 'cancel requires a server number and optional --execute'
    server_number="$1"
    [[ "${server_number}" =~ ^[0-9]+$ ]] || fail 'invalid server number'
    if [[ "${2:-}" == '--execute' ]]; then
      curl_robot \
        --data-urlencode 'cancellation_date=now' \
        --data-urlencode 'cancellation_reason=Server no longer necessary due to project ending' \
        "${base_url}/server/${server_number}/cancellation"
    elif [[ $# -eq 1 ]]; then
      curl_robot "${base_url}/server/${server_number}/cancellation"
    else
      fail 'unknown cancel option'
    fi
    ;;
  *)
    usage >&2
    fail "unknown command: ${command_name}"
    ;;
esac

printf '\n'
