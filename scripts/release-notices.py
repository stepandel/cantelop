#!/usr/bin/env python3
"""Prepare and verify the legal/source material shipped with Go executables."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import runpy
import shutil
import tarfile
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]


def notice_sources() -> dict[str, Path]:
    sources = {
        "LICENSE": ROOT / "LICENSE",
        "THIRD_PARTY_NOTICES.md": ROOT / "THIRD_PARTY_NOTICES.md",
        "docs/licensing.md": ROOT / "docs/licensing.md",
        "CONTRIBUTING.md": ROOT / "CONTRIBUTING.md",
        "SECURITY.md": ROOT / "SECURITY.md",
        "licenses/MIT-EXAMPLES.txt": ROOT / "licenses/MIT-EXAMPLES.txt",
    }
    for path in sorted((ROOT / "licenses/go").rglob("*")):
        if path.is_file():
            sources[str(path.relative_to(ROOT))] = path
    return sources


def archive_files() -> list[str]:
    """Paths, relative to the archive root, that every release archive carries."""
    return [*notice_sources(), "SOURCE.json"]


def go_requirements() -> list[str]:
    """Required module@version pairs from go.mod, ignoring directives that do not change dependencies."""
    requirements = set()
    in_block = False
    for line in (ROOT / "go.mod").read_text().splitlines():
        line = line.split("//", 1)[0].strip()
        if in_block:
            if line == ")":
                in_block = False
            elif line:
                requirements.add("@".join(line.split()[:2]))
        elif line == "require (":
            in_block = True
        elif line.startswith("require "):
            requirements.add("@".join(line.split()[1:3]))
    return sorted(requirements)


def strip_trailing_commas(text: str) -> str:
    """Turn bun.lock's JSONC-style trailing commas into strict JSON, leaving strings intact."""
    return re.sub(r'("(?:\\.|[^"\\])*")|,(\s*[}\]])', lambda match: match.group(1) or match.group(2), text)


def locked_packages(lockfile: str) -> set[str]:
    data = json.loads(strip_trailing_commas((ROOT / lockfile).read_text()))
    if lockfile.endswith("bun.lock"):
        return {item[0] for item in data["packages"].values() if item[0] != "@cantelop/edge-contracts@file:../contracts"}
    return {
        f"{metadata.get('name') or key.split('node_modules/')[-1]}@{metadata.get('version')}"
        for key, metadata in data["packages"].items() if key
    }


def check_inventory() -> None:
    inventory = json.loads((ROOT / "licenses/go/modules.json").read_text())
    if inventory.get("schema_version") != 2 or not inventory.get("modules"):
        raise ValueError("invalid Go notice inventory")
    if inventory["requirements"] != go_requirements():
        raise ValueError("go.mod requirements changed: run python3 scripts/update-go-licenses.py")
    for module in inventory["modules"]:
        if not module["notices"]:
            raise ValueError(f"missing notices for {module['module']}")
        for notice in module["notices"]:
            path = ROOT / "licenses/go" / notice["path"]
            if hashlib.sha256(path.read_bytes()).hexdigest() != notice["sha256"]:
                raise ValueError(f"Go notice checksum mismatch: {notice['path']}")
    for name, path in notice_sources().items():
        if not path.is_file() or not path.read_bytes().strip():
            raise ValueError(f"missing release notice: {name}")
    javascript = json.loads((ROOT / "licenses/javascript-packages.json").read_text())
    for lockfile in javascript["lockfiles"]:
        recorded = {f"{package['name']}@{package['version']}" for package in javascript["packages"] if lockfile in package["lockfiles"]}
        if locked_packages(lockfile) != recorded:
            raise ValueError(f"{lockfile} packages changed: run node scripts/update-javascript-licenses.mjs")
    if not javascript["packages"] or any(not package.get("license") for package in javascript["packages"]):
        raise ValueError("missing JavaScript license metadata")


def source_bytes(commit: str) -> bytes:
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("source commit must be a full lowercase Git commit")
    repository = (os.environ.get("CANTELOP_SOURCE_REPOSITORY", "").strip() or "https://github.com/stepandel/cantelop").rstrip("/").removesuffix(".git")
    parsed = urlsplit(repository)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or "?" in repository or "#" in repository or not parsed.path.strip("/"):
        raise ValueError("source repository must be an HTTPS repository URL without credentials, query, or fragment")
    identity = {
        "schema_version": 1,
        "license": "AGPL-3.0-only",
        "source_commit": commit,
        "source_url": f"{repository}/tree/{commit}",
        "license_map": "docs/licensing.md",
    }
    if not os.environ.get("CANTELOP_SOURCE_REPOSITORY", "").strip():
        helper = ROOT / "scripts/public-source.py"
        pin_file = ROOT / "console/public-source.json"
        if helper.exists() and pin_file.exists():
            publication = runpy.run_path(str(helper))["verify"](ROOT)
            if publication:
                identity.update(build_commit=commit, source_commit=publication["commit"],
                                source_url=f"{publication['repository']}/tree/{publication['commit']}",
                                source_snapshot_sha256=publication["snapshot_sha256"])
    return (json.dumps(identity, indent=2) + "\n").encode()


def prepare(destination: Path, commit: str) -> None:
    check_inventory()
    source = source_bytes(commit)
    destination.mkdir(parents=True, exist_ok=True)
    for name, path in notice_sources().items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, target)
    (destination / "SOURCE.json").write_bytes(source)


def verify_archive(archive: Path, commit: str, prefix: str) -> None:
    check_inventory()
    expected = {name: path.read_bytes() for name, path in notice_sources().items()}
    expected["SOURCE.json"] = source_bytes(commit)
    assert list(expected) == archive_files()
    prefix = prefix.rstrip("/")
    with tarfile.open(archive, "r:gz") as package:
        members: dict[str, tarfile.TarInfo] = {}
        for member in package.getmembers():
            member_path = PurePosixPath(member.name)
            if member_path.is_absolute() or ".." in member_path.parts or not (member.isfile() or member.isdir()):
                raise ValueError(f"unsafe archive member: {member.name}")
            if prefix and (not member_path.parts or member_path.parts[0] != prefix):
                raise ValueError(f"archive member outside bundle: {member.name}")
            if member.name in members:
                raise ValueError(f"duplicate archive member: {member.name}")
            members[member.name] = member
        for name, data in expected.items():
            key = f"{prefix}/{name}" if prefix else name
            member = members.get(key)
            if member is None or not member.isfile():
                raise ValueError(f"{archive.name}: missing regular notice file {key}")
            contents = package.extractfile(member)
            if contents is None or contents.read() != data:
                raise ValueError(f"{archive.name}: notice/source mismatch for {key}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="mode", required=True)
    commands.add_parser("check")
    commands.add_parser("list", help="print the notice and source files every release archive carries")
    prep = commands.add_parser("prepare")
    prep.add_argument("destination", type=Path)
    prep.add_argument("--commit", required=True)
    verify = commands.add_parser("verify-archives")
    verify.add_argument("archives", type=Path, nargs="+")
    verify.add_argument("--commit", required=True)
    verify.add_argument("--prefix", default="")
    args = parser.parse_args()
    try:
        if args.mode == "check":
            check_inventory()
            print("Release notice inventory verified")
        elif args.mode == "list":
            print("\n".join(archive_files()))
        elif args.mode == "prepare":
            prepare(args.destination, args.commit)
        else:
            for archive in args.archives:
                verify_archive(archive, args.commit, args.prefix)
            print(f"Verified notices and source identity in {len(args.archives)} archive(s)")
    except (ValueError, OSError, KeyError, tarfile.TarError) as error:
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()
