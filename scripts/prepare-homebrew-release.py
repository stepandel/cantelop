#!/usr/bin/env python3
"""Validate a Cantelop CLI release and render its public Homebrew formula."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import shlex
import sys
import tarfile


SEMVER_PATTERN = re.compile(
    r"^(?P<major>0|[1-9][0-9]*)\."
    r"(?P<minor>0|[1-9][0-9]*)\."
    r"(?P<patch>0|[1-9][0-9]*)"
    r"(?:-(?P<prerelease>[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$",
)
TAG_PATTERN = re.compile(r"^v(?P<version>.+)$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
CHECKSUM_PATTERN = re.compile(r"^(?P<digest>[0-9a-f]{64})  (?P<name>[^/]+)$")
PLATFORMS = (
    "darwin_amd64",
    "darwin_arm64",
    "linux_amd64",
    "linux_arm64",
)


class ReleaseError(ValueError):
    pass


def release_version(tag: str) -> str:
    match = TAG_PATTERN.fullmatch(tag)
    if match is None or SEMVER_PATTERN.fullmatch(match.group("version")) is None:
        raise ReleaseError(f"unsupported Cantelop release tag: {tag}")
    version = match.group("version")
    prerelease = SEMVER_PATTERN.fullmatch(version).group("prerelease")
    if prerelease is not None and any(
        part.isdigit() and len(part) > 1 and part.startswith("0")
        for part in prerelease.split(".")
    ):
        raise ReleaseError(f"unsupported Cantelop release tag: {tag}")
    return version


def compare_versions(left: str, right: str) -> int:
    def parsed(version: str) -> tuple[tuple[int, int, int], list[str] | None]:
        match = SEMVER_PATTERN.fullmatch(version)
        if match is None:
            raise ReleaseError(f"unsupported semantic version: {version}")
        core = tuple(int(match.group(name)) for name in ("major", "minor", "patch"))
        prerelease = match.group("prerelease")
        return core, prerelease.split(".") if prerelease is not None else None

    left_core, left_pre = parsed(left)
    right_core, right_pre = parsed(right)
    if left_core != right_core:
        return (left_core > right_core) - (left_core < right_core)
    if left_pre is None or right_pre is None:
        return (left_pre is None) - (right_pre is None)
    for left_part, right_part in zip(left_pre, right_pre):
        if left_part == right_part:
            continue
        left_numeric, right_numeric = left_part.isdigit(), right_part.isdigit()
        if left_numeric and right_numeric:
            return (int(left_part) > int(right_part)) - (int(left_part) < int(right_part))
        if left_numeric != right_numeric:
            return -1 if left_numeric else 1
        return (left_part > right_part) - (left_part < right_part)
    return (len(left_pre) > len(right_pre)) - (len(left_pre) < len(right_pre))


def require_no_downgrade(tag: str, formula_path: pathlib.Path) -> None:
    target = release_version(tag)
    try:
        formula = formula_path.read_text()
    except OSError as error:
        raise ReleaseError(f"read current Homebrew formula: {error}") from error
    match = re.search(r'^  version "(?P<version>[^"]+)"$', formula, re.MULTILINE)
    if match is None:
        raise ReleaseError("current Homebrew formula has no explicit version")
    current = match.group("version")
    if compare_versions(target, current) < 0:
        raise ReleaseError(f"refusing to downgrade Homebrew from {current} to {target}")


def archive_names(version: str) -> tuple[str, ...]:
    return tuple(f"cantelop_{version}_{platform}.tar.gz" for platform in PLATFORMS)


def mirrored_names(version: str) -> tuple[str, ...]:
    return (*archive_names(version), "SHA256SUMS", "install.sh")


def read_json(path: pathlib.Path) -> dict[str, object]:
    try:
        document = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise ReleaseError(f"read {path.name}: {error}") from error
    if not isinstance(document, dict):
        raise ReleaseError(f"{path.name} must contain a JSON object")
    return document


def read_checksums(path: pathlib.Path, expected_names: tuple[str, ...]) -> dict[str, str]:
    try:
        lines = path.read_text().splitlines()
    except OSError as error:
        raise ReleaseError(f"read SHA256SUMS: {error}") from error
    checksums: dict[str, str] = {}
    for line in lines:
        match = CHECKSUM_PATTERN.fullmatch(line)
        if match is None:
            raise ReleaseError("SHA256SUMS contains an invalid entry")
        name = match.group("name")
        if name in checksums:
            raise ReleaseError(f"SHA256SUMS repeats {name}")
        checksums[name] = match.group("digest")
    if set(checksums) != set(expected_names):
        raise ReleaseError("SHA256SUMS must cover exactly the four CLI archives")
    return checksums


def digest(path: pathlib.Path) -> str:
    hasher = hashlib.sha256()
    try:
        with path.open("rb") as artifact:
            while chunk := artifact.read(1024 * 1024):
                hasher.update(chunk)
    except OSError as error:
        raise ReleaseError(f"read {path.name}: {error}") from error
    return hasher.hexdigest()


def validate_archive(path: pathlib.Path) -> None:
    try:
        with tarfile.open(path, "r:gz") as archive:
            members = archive.getmembers()
            by_name = {member.name: member for member in members}
            if len(by_name) != len(members) or not {"cantelop", "README.md"} <= by_name.keys():
                raise ReleaseError(f"{path.name} must contain cantelop and README.md exactly once")
            for member in members:
                member_path = pathlib.PurePosixPath(member.name)
                if member_path.is_absolute() or ".." in member_path.parts:
                    raise ReleaseError(f"{path.name} contains an unsafe path: {member.name}")
                # License and source notices may accompany the binary in subdirectories.
                if not (member.isfile() or (member.isdir() and member.name not in {"cantelop", "README.md"})):
                    raise ReleaseError(f"{path.name} contains a non-regular file")
            if by_name["cantelop"].mode & 0o111 == 0:
                raise ReleaseError(f"{path.name} contains a non-executable cantelop binary")
            if by_name["cantelop"].size == 0 or by_name["README.md"].size == 0:
                raise ReleaseError(f"{path.name} contains an empty release file")
    except (OSError, tarfile.TarError) as error:
        raise ReleaseError(f"read {path.name}: {error}") from error


def validate_release(tag: str, directory: pathlib.Path) -> dict[str, object]:
    version = release_version(tag)
    names = archive_names(version)
    checksums = read_checksums(directory / "SHA256SUMS", names)
    for name in names:
        path = directory / name
        if digest(path) != checksums[name]:
            raise ReleaseError(f"checksum mismatch for {name}")
        validate_archive(path)

    evidence = read_json(directory / "release-evidence.json")
    if evidence.get("version") != version:
        raise ReleaseError("release evidence version does not match the release tag")
    source_commit = evidence.get("source_commit")
    if not isinstance(source_commit, str) or COMMIT_PATTERN.fullmatch(source_commit) is None:
        raise ReleaseError("release evidence contains an invalid source commit")
    evidence_checksums = evidence.get("artifacts")
    if evidence_checksums != checksums:
        raise ReleaseError("release evidence checksums do not match SHA256SUMS")
    notarization = evidence.get("notarization")
    if not isinstance(notarization, dict) or (
        notarization.get("status") != "Accepted"
        or notarization.get("status_code") != 0
        or notarization.get("issues") is not None
    ):
        raise ReleaseError("release evidence does not record accepted notarization")

    notarization_log = read_json(directory / "notarization-log.json")
    if (
        notarization_log.get("status") != "Accepted"
        or notarization_log.get("statusSummary") != "Ready for distribution"
        or notarization_log.get("statusCode") != 0
        or notarization_log.get("issues") is not None
    ):
        raise ReleaseError("Apple notarization log is not accepted without issues")
    tickets = notarization_log.get("ticketContents")
    if not isinstance(tickets, list):
        raise ReleaseError("Apple notarization log does not cover both macOS architectures")
    tickets_by_arch = {
        ticket.get("arch"): ticket.get("cdhash")
        for ticket in tickets
        if isinstance(ticket, dict)
    }
    if set(tickets_by_arch) != {"x86_64", "arm64"} or not all(
        isinstance(cdhash, str) and re.fullmatch(r"[0-9a-f]{40}", cdhash)
        for cdhash in tickets_by_arch.values()
    ):
        raise ReleaseError("Apple notarization log does not cover both macOS architectures")
    if notarization.get("submitted_archive_sha256") != notarization_log.get("sha256"):
        raise ReleaseError("release evidence does not match the submitted notarization archive")
    if notarization.get("tickets") != {
        "darwin_amd64": tickets_by_arch["x86_64"],
        "darwin_arm64": tickets_by_arch["arm64"],
    }:
        raise ReleaseError("release evidence does not match Apple's notarization tickets")

    return {
        "version": version,
        "source_commit": source_commit,
        "checksums": checksums,
    }


def compare_mirror(tag: str, source: pathlib.Path, mirror: pathlib.Path, allow_missing: bool) -> None:
    version = release_version(tag)
    for name in mirrored_names(version):
        source_path = source / name
        mirror_path = mirror / name
        if not mirror_path.exists():
            if allow_missing:
                continue
            raise ReleaseError(f"public tap release is missing {name}")
        if digest(source_path) != digest(mirror_path):
            raise ReleaseError(f"public tap release has conflicting {name}")


def render_formula(tag: str, checksums_path: pathlib.Path, tap_repository: str) -> str:
    version = release_version(tag)
    checksums = read_checksums(checksums_path, archive_names(version))
    tap_tag = f"cantelop-v{version}"

    def source(platform: str) -> tuple[str, str]:
        name = f"cantelop_{version}_{platform}.tar.gz"
        url = f"https://github.com/{tap_repository}/releases/download/{tap_tag}/{name}"
        return url, checksums[name]

    darwin_amd64 = source("darwin_amd64")
    darwin_arm64 = source("darwin_arm64")
    linux_amd64 = source("linux_amd64")
    linux_arm64 = source("linux_arm64")
    return f'''# typed: false
# frozen_string_literal: true

class Cantelop < Formula
  desc "Deploy complete API and Session runtime releases to Cantelop"
  homepage "https://cantelop.com"
  version "{version}"

  on_macos do
    if Hardware::CPU.intel?
      url "{darwin_amd64[0]}"
      sha256 "{darwin_amd64[1]}"
    end

    if Hardware::CPU.arm?
      url "{darwin_arm64[0]}"
      sha256 "{darwin_arm64[1]}"
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "{linux_amd64[0]}"
      sha256 "{linux_amd64[1]}"
    end

    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "{linux_arm64[0]}"
      sha256 "{linux_arm64[1]}"
    end
  end

  def install
    bin.install "cantelop"
  end

  test do
    assert_match "cantelop #{{version}}", shell_output("#{{bin}}/cantelop version")
  end
end
'''


def render_installer(tag: str, checksums_path: pathlib.Path, tap_repository: str) -> str:
    version = release_version(tag)
    checksums = read_checksums(checksums_path, archive_names(version))
    tap_tag = f"cantelop-v{version}"

    checksum_cases = "\n".join(
        f"  {platform}) expected_sha256={shlex.quote(checksums[f'cantelop_{version}_{platform}.tar.gz'])} ;;"
        for platform in PLATFORMS
    )
    release_base = (
        f"https://github.com/{tap_repository}/releases/download/{tap_tag}"
    )
    return f'''#!/bin/sh
set -eu

version={shlex.quote(version)}
release_base={shlex.quote(release_base)}
default_install_directory="${{HOME:?HOME must be set}}/.local/bin"
install_directory="${{CANTELOP_INSTALL_DIR:-$default_install_directory}}"

file_has_line() {{
  file=$1
  wanted=$2
  [ -f "$file" ] || return 1
  while IFS= read -r existing_line || [ -n "$existing_line" ]; do
    [ "$existing_line" = "$wanted" ] && return 0
  done <"$file"
  return 1
}}

configure_path() {{
  case ":${{PATH:-}}:" in
    *":${{install_directory}}:"*) return ;;
  esac
  if [ -n "${{CANTELOP_NO_MODIFY_PATH:-}}" ]; then
    echo "$install_directory is not on PATH; profile update skipped by CANTELOP_NO_MODIFY_PATH."
    return
  fi
  if [ "$install_directory" != "$default_install_directory" ]; then
    echo "$install_directory is not on PATH; add this custom directory to your shell profile."
    return
  fi

  shell_name=${{SHELL##*/}}
  case "$shell_name" in
    zsh)
      profile="$HOME/.zshrc"
      profile_line='export PATH="$HOME/.local/bin:$PATH" # added by cantelop installer'
      ;;
    bash)
      if [ "$(uname -s)" = Darwin ]; then
        profile="$HOME/.bash_profile"
      else
        profile="$HOME/.bashrc"
      fi
      profile_line='export PATH="$HOME/.local/bin:$PATH" # added by cantelop installer'
      ;;
    fish)
      profile="${{XDG_CONFIG_HOME:-$HOME/.config}}/fish/conf.d/cantelop.fish"
      profile_line='set -gx PATH "$HOME/.local/bin" $PATH # added by cantelop installer'
      ;;
    *)
      profile="$HOME/.profile"
      profile_line='export PATH="$HOME/.local/bin:$PATH" # added by cantelop installer'
      ;;
  esac

  mkdir -p "${{profile%/*}}"
  if file_has_line "$profile" "$profile_line"; then
    echo "$default_install_directory is already configured in $profile."
  elif printf '\n%s\n' "$profile_line" >>"$profile"; then
    echo "Added $default_install_directory to PATH in $profile."
  else
    echo "Could not update $profile; add $default_install_directory to PATH manually." >&2
    return
  fi
  echo "Restart your shell, or run: . $profile"
}}

for command in curl tar install; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "cantelop installer requires $command" >&2
    exit 1
  fi
done

case "$install_directory" in
  /*) ;;
  *) echo "CANTELOP_INSTALL_DIR must be an absolute path" >&2; exit 1 ;;
esac

existing_cantelop="$(command -v cantelop 2>/dev/null || true)"
if [ -n "$existing_cantelop" ] && [ "$existing_cantelop" != "$install_directory/cantelop" ]; then
  echo "cantelop is already installed at $existing_cantelop" >&2
  echo "Remove the existing installation before installing another copy." >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin) operating_system=darwin ;;
  Linux) operating_system=linux ;;
  *) echo "cantelop does not support operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
  *) echo "cantelop does not support architecture: $(uname -m)" >&2; exit 1 ;;
esac

platform="${{operating_system}}_${{architecture}}"
case "$platform" in
{checksum_cases}
  *) echo "cantelop does not provide an archive for $platform" >&2; exit 1 ;;
esac

archive="cantelop_${{version}}_${{platform}}.tar.gz"
temporary_directory="$(mktemp -d "${{TMPDIR:-/tmp}}/cantelop-install.XXXXXX")"
staged_path="${{install_directory}}/.cantelop.$$"
trap 'rm -rf "$temporary_directory"; rm -f "$staged_path"' 0 HUP INT TERM

curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --output "${{temporary_directory}}/${{archive}}" \
  "${{release_base}}/${{archive}}"

if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "$expected_sha256" "$archive" \
    | (cd "$temporary_directory" && sha256sum --check --status -)
elif command -v shasum >/dev/null 2>&1; then
  printf '%s  %s\n' "$expected_sha256" "$archive" \
    | (cd "$temporary_directory" && shasum -a 256 --check --status -)
else
  echo "cantelop installer requires sha256sum or shasum" >&2
  exit 1
fi

tar -C "$temporary_directory" -xzf "${{temporary_directory}}/${{archive}}" cantelop
actual_version="$("${{temporary_directory}}/cantelop" version)"
case "$actual_version" in
  "cantelop $version ("????????????")") ;;
  *) echo "downloaded CLI reported an unexpected version: $actual_version" >&2; exit 1 ;;
esac

mkdir -p "$install_directory"
install -m 0755 "${{temporary_directory}}/cantelop" "$staged_path"
mv -f "$staged_path" "${{install_directory}}/cantelop"

echo "Installed cantelop $version to ${{install_directory}}/cantelop"
configure_path
'''


def write_output(path: pathlib.Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


def append_output(path: pathlib.Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a") as output:
        output.write(content)


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    validate = subparsers.add_parser("validate")
    validate.add_argument("--tag", required=True)
    validate.add_argument("--directory", required=True, type=pathlib.Path)
    validate.add_argument("--github-output", type=pathlib.Path)

    compare = subparsers.add_parser("compare-mirror")
    compare.add_argument("--tag", required=True)
    compare.add_argument("--source", required=True, type=pathlib.Path)
    compare.add_argument("--mirror", required=True, type=pathlib.Path)
    compare.add_argument("--allow-missing", action="store_true")

    check_version = subparsers.add_parser("check-version")
    check_version.add_argument("--tag", required=True)
    check_version.add_argument("--formula", required=True, type=pathlib.Path)

    formula = subparsers.add_parser("render-formula")
    formula.add_argument("--tag", required=True)
    formula.add_argument("--checksums", required=True, type=pathlib.Path)
    formula.add_argument("--tap-repository", default="stepandel/homebrew-tap")
    formula.add_argument("--output", required=True, type=pathlib.Path)

    installer = subparsers.add_parser("render-installer")
    installer.add_argument("--tag", required=True)
    installer.add_argument("--checksums", required=True, type=pathlib.Path)
    installer.add_argument("--tap-repository", default="stepandel/homebrew-tap")
    installer.add_argument("--output", required=True, type=pathlib.Path)

    args = parser.parse_args()
    try:
        if args.command == "validate":
            result = validate_release(args.tag, args.directory)
            print(json.dumps(result, sort_keys=True))
            if args.github_output is not None:
                append_output(
                    args.github_output,
                    f"version={result['version']}\nsource_commit={result['source_commit']}\n",
                )
        elif args.command == "compare-mirror":
            compare_mirror(args.tag, args.source, args.mirror, args.allow_missing)
        elif args.command == "check-version":
            require_no_downgrade(args.tag, args.formula)
        elif args.command == "render-formula":
            write_output(
                args.output,
                render_formula(args.tag, args.checksums, args.tap_repository),
            )
        else:
            write_output(
                args.output,
                render_installer(args.tag, args.checksums, args.tap_repository),
            )
    except ReleaseError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
