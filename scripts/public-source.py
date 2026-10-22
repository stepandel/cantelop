#!/usr/bin/env python3
"""Export reviewed source without private history; pin and verify its public commit.

This tool never pushes, changes repository visibility, or deploys anything.
"""

from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
POLICY_FILE = ".public-source-policy.json"
PIN_FILE = "console/public-source.json"
SNAPSHOT_FILE = "SOURCE_SNAPSHOT.json"
DEFAULT_REPOSITORY = "https://github.com/stepandel/cantelop"
PUBLIC_IGNORE = b"# Public snapshots use exact fixture allowlists in .gitleaks.toml.\n"


def document(value: object) -> bytes:
    return (json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode()


def repository_url(value: str) -> str:
    value = value.rstrip("/").removesuffix(".git")
    parsed = urlsplit(value)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or
            parsed.password or "?" in value or "#" in value or not parsed.path.strip("/")):
        raise ValueError("source repository must be an HTTPS repository URL without credentials, query, or fragment")
    return value


def empty_pin(repository: str = DEFAULT_REPOSITORY) -> dict:
    return {"schema_version": 1, "repository": repository_url(repository),
            "commit": None, "snapshot_sha256": None}


def excluded(name: str, policy: dict) -> bool:
    path = Path(name)
    return (name in policy["private_files"] or name in policy["ignored_files"] or
            any(name.startswith(prefix) for prefix in policy["private_prefixes"]) or
            any(part in policy["ignored_directories"] for part in path.parts) or
            any(fnmatch.fnmatchcase(path.name, pattern) for pattern in policy["ignored_file_patterns"]))


def tracked_files(root: Path) -> list[str]:
    result = subprocess.run(["git", "ls-files", "-z"], cwd=root, check=True, capture_output=True)
    return sorted(set(result.stdout.decode().split("\0")) - {""})


def source_files(root: Path, repository: str = DEFAULT_REPOSITORY) -> dict[str, tuple[bytes, int]]:
    policy = json.loads((root / POLICY_FILE).read_text())
    if policy.get("schema_version") != 1:
        raise ValueError("unsupported publication policy")
    files = {}
    for name in tracked_files(root):
        if name == SNAPSHOT_FILE or excluded(name, policy):
            continue
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"publication requires a regular source file: {name}")
        data = path.read_bytes()
        if name == PIN_FILE:
            data = document(empty_pin(repository))
        elif name == ".gitleaksignore":
            data = PUBLIC_IGNORE
        files[name] = (data, 0o755 if path.stat().st_mode & 0o111 else 0o644)
    if not {PIN_FILE, POLICY_FILE, "LICENSE", "CONTRIBUTING.md", "SECURITY.md"}.issubset(files):
        raise ValueError("publication is missing required tracked files; stage new files before exporting")
    return files


def snapshot(files: dict[str, tuple[bytes, int]]) -> bytes:
    return document({"schema_version": 1, "files": {
        name: {"sha256": hashlib.sha256(data).hexdigest(), "mode": mode}
        for name, (data, mode) in files.items()
    }})


def export(destination: Path, root: Path = ROOT, repository: str = DEFAULT_REPOSITORY) -> None:
    if destination.exists():
        raise ValueError("export destination must not exist")
    files = source_files(root, repository)
    destination.mkdir(parents=True)
    for name, (data, mode) in files.items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        target.chmod(mode)
    (destination / SNAPSHOT_FILE).write_bytes(snapshot(files))
    print(f"Exported {len(files)} source files; no Git history, issues, releases, or account settings copied")


def pin(public_checkout: Path, commit: str, root: Path = ROOT,
        repository: str = DEFAULT_REPOSITORY) -> None:
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("public commit must be a full lowercase Git commit")
    actual = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=public_checkout).decode().strip()
    dirty = subprocess.check_output(["git", "status", "--porcelain"], cwd=public_checkout).strip()
    if actual != commit or dirty:
        raise ValueError("pin requires the exact clean public checkout")
    data = (public_checkout / SNAPSHOT_FILE).read_bytes()
    if data != snapshot(source_files(root, repository)):
        raise ValueError("public snapshot does not match the current reviewed source")
    if set(tracked_files(public_checkout)) != set(source_files(root, repository)) | {SNAPSHOT_FILE}:
        raise ValueError("public checkout file list differs from the reviewed snapshot")
    # Verify the actual public files too, rather than trusting their manifest.
    for name, (contents, mode) in source_files(root, repository).items():
        path = public_checkout / name
        if path.is_symlink() or path.read_bytes() != contents or (0o755 if path.stat().st_mode & 0o111 else 0o644) != mode:
            raise ValueError(f"public source differs: {name}")
    (root / SNAPSHOT_FILE).write_bytes(data)
    record = empty_pin(repository)
    record.update(commit=commit, snapshot_sha256=hashlib.sha256(data).hexdigest())
    (root / PIN_FILE).write_bytes(document(record))
    print(f"Pinned public source {repository}/tree/{commit}")


def pin_private(commit: str, repository: str, root: Path = ROOT) -> None:
    """Record an exact private checkout without exporting or contacting a remote."""
    actual = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root).decode().strip()
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or actual != commit:
        raise ValueError("private pin requires the exact current commit")
    changed = subprocess.check_output(["git", "diff", "--name-only", commit], cwd=root).decode().splitlines()
    if any(name not in {PIN_FILE, SNAPSHOT_FILE} for name in changed):
        raise ValueError("commit source changes before pinning a private snapshot")
    files = source_files(root, repository)
    data = snapshot(files)
    (root / SNAPSHOT_FILE).write_bytes(data)
    record = empty_pin(repository)
    record.update(visibility="private", commit=commit, snapshot_sha256=hashlib.sha256(data).hexdigest())
    (root / PIN_FILE).write_bytes(document(record))
    print(f"Pinned private source {repository_url(repository)}/tree/{commit}; no source exported")


def verify(root: Path = ROOT) -> dict | None:
    record = json.loads((root / PIN_FILE).read_text())
    if record.get("commit") is None and record.get("snapshot_sha256") is None:
        return None  # Public development checkouts and independent operators.
    repository_url(record["repository"])
    if record.get("visibility", "public") not in {"public", "private"}:
        raise ValueError("invalid source visibility")
    if record.get("schema_version") != 1 or not re.fullmatch(r"[0-9a-f]{40}", record.get("commit", "")) or not re.fullmatch(r"[0-9a-f]{64}", record.get("snapshot_sha256", "")):
        raise ValueError("invalid public source pin")
    data = (root / SNAPSHOT_FILE).read_bytes()
    if hashlib.sha256(data).hexdigest() != record["snapshot_sha256"]:
        raise ValueError("public source manifest digest does not match the pin")
    if data != snapshot(source_files(root, record["repository"])):
        raise ValueError("source changed since publication; export, review, publish, and pin a new snapshot")
    return record


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("export")
    prepare.add_argument("destination", type=Path)
    prepare.add_argument("--repository", default=DEFAULT_REPOSITORY)
    bind = commands.add_parser("pin")
    bind.add_argument("public_checkout", type=Path)
    bind.add_argument("--commit", required=True)
    bind.add_argument("--repository", default=DEFAULT_REPOSITORY)
    private = commands.add_parser("pin-private")
    private.add_argument("--commit", required=True)
    private.add_argument("--repository", required=True)
    commands.add_parser("verify")
    args = parser.parse_args()
    try:
        if args.command == "export":
            export(args.destination, repository=args.repository)
        elif args.command == "pin":
            pin(args.public_checkout, args.commit, repository=args.repository)
        elif args.command == "pin-private":
            pin_private(args.commit, args.repository)
        else:
            record = verify()
            print(f"{record.get('visibility', 'public').capitalize()} source pin verified" if record else "Public development checkout: no private deployment pin")
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()
