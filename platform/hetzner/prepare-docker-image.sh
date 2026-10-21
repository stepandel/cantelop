#!/usr/bin/env bash
set -Eeuo pipefail
umask 0022

fail() {
  printf 'prepare-docker-image: %s\n' "$*" >&2
  exit 1
}

[[ "${EUID}" -eq 0 ]] || fail 'must run as root to preserve image ownership'
[[ "$(uname -s)" == 'Linux' ]] || fail 'requires Linux'
[[ $# -eq 3 ]] || \
  fail 'usage: prepare-docker-image.sh IMAGE OUTPUT_EXT4 OUTPUT_PROCESS_JSON'

image="$1"
output_image="$2"
output_process="$3"
output_image_checksum="${output_image}.sha256"
output_process_checksum="${output_process}.sha256"
[[ -n "${image}" ]] || fail 'Docker image reference is required'
[[ "${output_image}" == /* ]] || fail 'OUTPUT_EXT4 must be absolute'
[[ "${output_process}" == /* ]] || fail 'OUTPUT_PROCESS_JSON must be absolute'
[[ ! -e "${output_image}" ]] || fail "output already exists: ${output_image}"
[[ ! -e "${output_process}" ]] || fail "output already exists: ${output_process}"
[[ ! -e "${output_image_checksum}" ]] || \
  fail "output already exists: ${output_image_checksum}"
[[ ! -e "${output_process_checksum}" ]] || \
  fail "output already exists: ${output_process_checksum}"
[[ -d "$(dirname "${output_image}")" ]] || fail 'OUTPUT_EXT4 parent does not exist'
[[ -d "$(dirname "${output_process}")" ]] || fail 'OUTPUT_PROCESS_JSON parent does not exist'

for command in awk basename chmod dirname docker du install jq mkfs.ext4 mktemp mv realpath rm sha256sum tar truncate; do
  command -v "${command}" >/dev/null 2>&1 ||
    fail "required command is unavailable: ${command}"
done

output_paths=()
for output_path in \
  "${output_image}" \
  "${output_process}" \
  "${output_image_checksum}" \
  "${output_process_checksum}"
do
  output_paths+=("$(realpath -m -- "${output_path}")")
done
for ((left = 0; left < ${#output_paths[@]}; left++)); do
  for ((right = left + 1; right < ${#output_paths[@]}; right++)); do
    [[ "${output_paths[left]}" != "${output_paths[right]}" ]] ||
      fail "output paths must be distinct: ${output_paths[left]}"
  done
done

work_directory="$(mktemp -d)"
container_id=''
staging_image=''
staging_process=''
staging_image_checksum=''
staging_process_checksum=''
publication_complete=0
published_paths=()
pending_source=''
pending_destination=''
cleanup() {
  if [[ -n "${container_id}" ]]; then
    docker rm --force "${container_id}" >/dev/null 2>&1 || true
  fi
  [[ -z "${staging_image}" ]] ||
    rm -f -- "${staging_image}"
  [[ -z "${staging_process}" ]] ||
    rm -f -- "${staging_process}"
  [[ -z "${staging_image_checksum}" ]] ||
    rm -f -- "${staging_image_checksum}"
  [[ -z "${staging_process_checksum}" ]] ||
    rm -f -- "${staging_process_checksum}"
  if [[ "${publication_complete}" != '1' ]]; then
    if [[ -n "${pending_destination}" && ! -e "${pending_source}" ]]; then
      rm -f -- "${pending_destination}"
    fi
    for published_path in "${published_paths[@]}"; do
      rm -f -- "${published_path}"
    done
  fi
  [[ -n "${work_directory:-}" && "${work_directory}" == /tmp/* ]] || return
  rm -rf -- "${work_directory}"
}
trap cleanup EXIT
staging_image="$(
  mktemp "$(dirname "${output_image}")/.$(basename "${output_image}").tmp.XXXXXX"
)"
staging_process="$(
  mktemp "$(dirname "${output_process}")/.$(basename "${output_process}").tmp.XXXXXX"
)"
staging_image_checksum="$(
  mktemp "$(dirname "${output_image}")/.$(basename "${output_image_checksum}").tmp.XXXXXX"
)"
staging_process_checksum="$(
  mktemp "$(dirname "${output_process}")/.$(basename "${output_process_checksum}").tmp.XXXXXX"
)"

docker image inspect -- "${image}" > "${work_directory}/inspect.json" ||
  fail "Docker image is not available locally: ${image}"
[[ "$(jq 'length' "${work_directory}/inspect.json")" == '1' ]] ||
  fail 'Docker image inspection did not return exactly one image'

arguments_json="$(
  jq -c '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // []))' \
    "${work_directory}/inspect.json"
)"
[[ "$(jq 'length' <<< "${arguments_json}")" -gt 0 ]] ||
  fail 'Docker image has no Entrypoint or Cmd'
jq -e 'all(.[]; type == "string")' <<< "${arguments_json}" >/dev/null ||
  fail 'Docker image arguments must be strings'
environment_json="$(
  jq -c '(.[0].Config.Env // [])' "${work_directory}/inspect.json"
)"
jq -e '
  all(.[]; type == "string" and contains("=") and (startswith("=") | not))
  and ((map(split("=")[0]) | length) == (map(split("=")[0]) | unique | length))
' <<< "${environment_json}" >/dev/null ||
  fail 'Docker image environment must contain unique NAME=value entries'
working_directory="$(jq -r '.[0].Config.WorkingDir // ""' "${work_directory}/inspect.json")"
[[ -n "${working_directory}" ]] || working_directory='/'
[[ "${working_directory}" == /* ]] || fail 'Docker WorkingDir must be absolute'
[[ "${working_directory}" != *'//'*
  && "${working_directory}" != *'/./'*
  && "${working_directory}" != *'/../'*
  && "${working_directory}" != */.
  && "${working_directory}" != */..
  && ("${working_directory}" == '/' || "${working_directory}" != */) ]] ||
  fail 'Docker WorkingDir must be a clean absolute path'
user="$(jq -r '.[0].Config.User // ""' "${work_directory}/inspect.json")"

container_id="$(docker create -- "${image}")" ||
  fail "could not create a container from ${image}"
docker export --output "${work_directory}/rootfs.tar" "${container_id}" ||
  fail 'could not export the Docker container filesystem'
docker rm "${container_id}" >/dev/null ||
  fail 'could not remove the temporary Docker container'
container_id=''

install -d -m 0755 "${work_directory}/rootfs"
tar \
  --extract \
  --file "${work_directory}/rootfs.tar" \
  --directory "${work_directory}/rootfs" \
  --numeric-owner \
  --same-owner \
  --xattrs \
  --acls

lookup_passwd() {
  local name="$1"
  local field="$2"
  local passwd_path="${work_directory}/rootfs/etc/passwd"
  [[ -f "${passwd_path}" && ! -L "${passwd_path}" ]] ||
    fail "Docker user ${name} requires a regular /etc/passwd"
  awk -F: -v name="${name}" -v field="${field}" \
    '$1 == name { print $field; found = 1; exit } END { if (!found) exit 1 }' \
    "${passwd_path}" ||
    fail "Docker user does not exist in /etc/passwd: ${name}"
}

lookup_group() {
  local name="$1"
  local group_path="${work_directory}/rootfs/etc/group"
  [[ -f "${group_path}" && ! -L "${group_path}" ]] ||
    fail "Docker group ${name} requires a regular /etc/group"
  awk -F: -v name="${name}" \
    '$1 == name { print $3; found = 1; exit } END { if (!found) exit 1 }' \
    "${group_path}" ||
    fail "Docker group does not exist in /etc/group: ${name}"
}

user_part="${user%%:*}"
group_part=''
if [[ "${user}" == *:* ]]; then
  group_part="${user#*:}"
fi
if [[ -z "${user_part}" ]]; then
  uid=0
  default_gid=0
elif [[ "${user_part}" =~ ^[0-9]+$ ]]; then
  uid="${user_part}"
  default_gid="${uid}"
else
  uid="$(lookup_passwd "${user_part}" 3)"
  default_gid="$(lookup_passwd "${user_part}" 4)"
fi
if [[ -z "${group_part}" ]]; then
  gid="${default_gid}"
elif [[ "${group_part}" =~ ^[0-9]+$ ]]; then
  gid="${group_part}"
else
  gid="$(lookup_group "${group_part}")"
fi
[[ "${uid}" =~ ^[0-9]+$ ]] || fail 'Docker UID is not numeric'
[[ "${gid}" =~ ^[0-9]+$ ]] || fail 'Docker GID is not numeric'
((${#uid} <= 10)) || fail 'Docker UID is outside uint32 range'
((${#gid} <= 10)) || fail 'Docker GID is outside uint32 range'
uid=$((10#${uid}))
gid=$((10#${gid}))
((uid <= 4294967295)) || fail 'Docker UID is outside uint32 range'
((gid <= 4294967295)) || fail 'Docker GID is outside uint32 range'

jq -n \
  --argjson args "${arguments_json}" \
  --argjson env "${environment_json}" \
  --arg working_directory "${working_directory}" \
  --argjson uid "${uid}" \
  --argjson gid "${gid}" \
  '{
    args: $args,
    env: $env,
    working_directory: $working_directory,
    uid: $uid,
    gid: $gid
  }' > "${work_directory}/process.json"

rootfs_bytes="$(du --summarize --block-size=1 "${work_directory}/rootfs" | awk '{print $1}')"
minimum_bytes=$((256 * 1024 * 1024))
image_bytes=$((rootfs_bytes + rootfs_bytes / 4 + 64 * 1024 * 1024))
((image_bytes >= minimum_bytes)) || image_bytes="${minimum_bytes}"
mebibyte=$((1024 * 1024))
image_bytes=$((((image_bytes + mebibyte - 1) / mebibyte) * mebibyte))

truncate -s "${image_bytes}" "${staging_image}"
mkfs.ext4 -q -F -d "${work_directory}/rootfs" "${staging_image}"
install -m 0644 "${work_directory}/process.json" "${staging_process}"
image_sha256="$(sha256sum "${staging_image}")"
image_sha256="${image_sha256%% *}"
process_sha256="$(sha256sum "${staging_process}")"
process_sha256="${process_sha256%% *}"
printf '%s  %s\n' "${image_sha256}" "$(basename "${output_image}")" \
  > "${staging_image_checksum}"
printf '%s  %s\n' "${process_sha256}" "$(basename "${output_process}")" \
  > "${staging_process_checksum}"
chmod 0644 "${staging_image_checksum}" "${staging_process_checksum}"

publish() {
  local source_path="$1"
  local destination_path="$2"
  pending_source="${source_path}"
  pending_destination="${destination_path}"
  mv -nT -- "${source_path}" "${destination_path}"
  [[ ! -e "${source_path}" ]] ||
    fail "output appeared during publication: ${destination_path}"
  published_paths+=("${destination_path}")
  pending_source=''
  pending_destination=''
}
publish "${staging_image}" "${output_image}"
publish "${staging_process}" "${output_process}"
publish "${staging_image_checksum}" "${output_image_checksum}"
publish "${staging_process_checksum}" "${output_process_checksum}"
staging_image=''
staging_process=''
staging_image_checksum=''
staging_process_checksum=''
publication_complete=1

printf 'Prepared Docker image filesystem %s and process configuration %s with checksums\n' \
  "${output_image}" \
  "${output_process}"
