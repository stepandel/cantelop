# Shared helpers for the build and release scripts in scripts/.
# Source it from a script that already runs with `set -Eeuo pipefail`:
#
#   source "${repo_dir}/scripts/lib/common.sh"
#
# The helpers avoid Bash 4 features so they also run under macOS /bin/bash.

# sha256_files [sha256sum options] FILE...
# Run sha256sum, or `shasum -a 256` where sha256sum is unavailable. Both print
# the same "<hash>  <file>" lines and accept --check.
sha256_files() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  else
    printf 'sha256sum or shasum is required\n' >&2
    return 1
  fi
}

common_temporary_directories=()

common_remove_temporary_directories() {
  local directory
  for directory in ${common_temporary_directories[@]+"${common_temporary_directories[@]}"}; do
    rm -rf -- "${directory}"
  done
}

# make_temporary_directory VARIABLE NAME
# Create a private directory "${TMPDIR:-/tmp}/NAME.XXXXXX", store its path in
# VARIABLE, and remove it when the script exits. Scripts that need their own
# EXIT trap should call common_remove_temporary_directories from it.
make_temporary_directory() {
  local directory
  directory="$(mktemp -d "${TMPDIR:-/tmp}/$2.XXXXXX")"
  common_temporary_directories+=("${directory}")
  trap common_remove_temporary_directories EXIT
  printf -v "$1" '%s' "${directory}"
}
