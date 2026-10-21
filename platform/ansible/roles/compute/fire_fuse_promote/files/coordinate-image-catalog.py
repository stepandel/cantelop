#!/usr/bin/env python3
"""Acquire or release the cross-process image catalog deployment lease."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import sys


SHA256 = re.compile(r"^[0-9a-f]{64}$")
TOKEN = re.compile(r"^[0-9a-f]{64}$")


def fail(message: str) -> None:
    raise ValueError(message)


def require_clean_absolute(path: Path, label: str) -> None:
    if not path.is_absolute() or Path(os.path.normpath(path)) != path:
        fail(f"{label} must be a clean absolute path")


def trusted_regular(path: Path, label: str, mode: int | None = None) -> os.stat_result:
    try:
        metadata = path.lstat()
    except OSError as error:
        fail(f"inspect {label}: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        fail(f"{label} must be a regular file, not a symlink")
    if metadata.st_uid != 0:
        fail(f"{label} must be owned by root")
    permissions = stat.S_IMODE(metadata.st_mode)
    if permissions & 0o022:
        fail(f"{label} must not be group or world writable")
    if mode is not None and permissions != mode:
        fail(f"{label} mode is {permissions:04o}, want {mode:04o}")
    return metadata


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def open_lock(path: Path) -> int:
    flags = os.O_CREAT | os.O_RDWR | os.O_CLOEXEC | os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    metadata = os.fstat(descriptor)
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0:
        os.close(descriptor)
        fail("image catalog writer lock must be a root-owned regular file")
    if stat.S_IMODE(metadata.st_mode) != 0o600:
        os.close(descriptor)
        fail("image catalog writer lock mode must be 0600")
    fcntl.flock(descriptor, fcntl.LOCK_EX)
    return descriptor


def catalog_identity(path: Path) -> str:
    try:
        path.lstat()
    except FileNotFoundError:
        return "absent"
    trusted_regular(path, "live image catalog", 0o640)
    return sha256_file(path)


def begin(catalog: Path, expected: str) -> dict[str, object]:
    lock_path = Path(str(catalog) + ".lock")
    lease_path = Path(str(lock_path) + ".deployment")
    descriptor = open_lock(lock_path)
    try:
        if lease_path.exists() or lease_path.is_symlink():
            fail(f"image catalog deployment lease already exists: {lease_path}")
        actual = catalog_identity(catalog)
        if actual != expected:
            fail(
                "live image catalog changed after staging: "
                f"got {actual}, staged from {expected}; restage the release"
            )
        token = secrets.token_hex(32)
        lease_flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_CLOEXEC | os.O_NOFOLLOW
        lease_descriptor = os.open(lease_path, lease_flags, 0o600)
        try:
            os.fchown(lease_descriptor, 0, 0)
            os.fchmod(lease_descriptor, 0o600)
            os.write(lease_descriptor, (token + "\n").encode("ascii"))
            os.fsync(lease_descriptor)
        finally:
            os.close(lease_descriptor)
        sync_directory(catalog.parent)
        return {"catalog_sha256": actual, "lease_token": token}
    finally:
        os.close(descriptor)


def end(catalog: Path, token: str) -> dict[str, object]:
    lock_path = Path(str(catalog) + ".lock")
    lease_path = Path(str(lock_path) + ".deployment")
    descriptor = open_lock(lock_path)
    try:
        trusted_regular(lease_path, "image catalog deployment lease", 0o600)
        try:
            actual_token = lease_path.read_text(encoding="ascii").strip()
        except (OSError, UnicodeError) as error:
            fail(f"read image catalog deployment lease: {error}")
        if actual_token != token:
            fail("image catalog deployment lease token does not match")
        lease_path.unlink()
        sync_directory(catalog.parent)
        return {"released": True}
    finally:
        os.close(descriptor)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="operation", required=True)
    begin_parser = subparsers.add_parser("begin")
    begin_parser.add_argument("--catalog", required=True, type=Path)
    begin_parser.add_argument("--expected-sha256", required=True)
    end_parser = subparsers.add_parser("end")
    end_parser.add_argument("--catalog", required=True, type=Path)
    args = parser.parse_args()
    require_clean_absolute(args.catalog, "catalog")
    if args.operation == "begin":
        if args.expected_sha256 != "absent" and not SHA256.fullmatch(args.expected_sha256):
            parser.error("--expected-sha256 must be absent or a lowercase SHA-256")
    else:
        args.token = os.environ.get("CANTELOP_CATALOG_LEASE_TOKEN", "")
        if not TOKEN.fullmatch(args.token):
            parser.error("CANTELOP_CATALOG_LEASE_TOKEN must be 64 lowercase hexadecimal digits")
    return args


def main() -> int:
    if os.geteuid() != 0:
        print("coordinate-image-catalog: root is required", file=sys.stderr)
        return 1
    try:
        args = parse_args()
        if args.operation == "begin":
            result = begin(args.catalog, args.expected_sha256)
        else:
            result = end(args.catalog, args.token)
    except (OSError, ValueError) as error:
        print(f"coordinate-image-catalog: {error}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
