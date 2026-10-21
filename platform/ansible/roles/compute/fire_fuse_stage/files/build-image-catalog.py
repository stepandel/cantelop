#!/usr/bin/env python3
"""Build one fire-fuse content-addressed image catalog entry."""

import argparse
import hashlib
import json
from pathlib import Path
import stat
import sys


PROCESS_KEYS = ("args", "env", "working_directory", "uid", "gid")


def fail(message: str) -> None:
    raise ValueError(message)


def go_json(value: object) -> bytes:
    """Match encoding/json's compact JSON and default HTML escaping."""
    document = json.dumps(
        value,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    document = (
        document.replace("&", "\\u0026")
        .replace("<", "\\u003c")
        .replace(">", "\\u003e")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )
    return document.encode("utf-8")


def load_process(path: Path) -> dict[str, object]:
    try:
        process = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        fail(f"read process object: {error}")
    if not isinstance(process, dict) or set(process.keys()) != set(PROCESS_KEYS):
        fail("process must contain only args, env, working_directory, uid, and gid")
    args = process["args"]
    if (
        not isinstance(args, list)
        or not 1 <= len(args) <= 1024
        or any(not isinstance(value, str) for value in args)
        or not args[0]
    ):
        fail("process args must contain 1-1024 strings with a nonempty command")
    env = process["env"]
    if (
        not isinstance(env, list)
        or len(env) > 4096
        or any(
            not isinstance(value, str)
            or "=" not in value
            or value.startswith("=")
            for value in env
        )
    ):
        fail("process env must contain at most 4096 NAME=value strings")
    names = [value.split("=", 1)[0] for value in env]
    if len(names) != len(set(names)):
        fail("process environment names must be unique")
    working_directory = process["working_directory"]
    if (
        not isinstance(working_directory, str)
        or not working_directory.startswith("/")
        or "//" in working_directory
        or "/./" in working_directory
        or "/../" in working_directory
        or working_directory.endswith("/.")
        or working_directory.endswith("/..")
        or (working_directory != "/" and working_directory.endswith("/"))
    ):
        fail("process working_directory must be a clean absolute path")
    for field in ("uid", "gid"):
        value = process[field]
        if (
            isinstance(value, bool)
            or not isinstance(value, (int, float))
            or not float(value).is_integer()
            or not 0 <= value <= 4294967295
        ):
            fail(f"process {field} must be a uint32 integer")
        process[field] = int(value)
    return {key: process[key] for key in PROCESS_KEYS}


def regular_file(path: Path, label: str) -> int:
    try:
        metadata = path.lstat()
    except OSError as error:
        fail(f"inspect {label}: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        fail(f"{label} must be a regular file, not a symlink")
    if metadata.st_size < 1:
        fail(f"{label} must not be empty")
    return metadata.st_size


def build(args: argparse.Namespace) -> dict[str, object]:
    process_path = Path(args.process)
    rootfs_size = args.rootfs_size_bytes
    regular_file(process_path, "process object")
    actual_sha256 = args.rootfs_sha256
    process = load_process(process_path)
    process_sha256 = hashlib.sha256(go_json(process)).hexdigest()
    identity = {
        "schema_version": 1,
        "rootfs_sha256": actual_sha256,
        "rootfs_size_bytes": rootfs_size,
        "process_sha256": process_sha256,
    }
    build_id = "sha256:" + hashlib.sha256(go_json(identity)).hexdigest()
    return {
        "build_id": build_id,
        "image": {
            "rootfs_image": args.installed_rootfs,
            "rootfs_sha256": actual_sha256,
            "rootfs_size_bytes": rootfs_size,
            "process": process,
        },
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rootfs-size-bytes", required=True, type=int)
    parser.add_argument("--rootfs-sha256", required=True)
    parser.add_argument("--process", required=True)
    parser.add_argument("--installed-rootfs", required=True)
    args = parser.parse_args()
    if len(args.rootfs_sha256) != 64 or any(
        character not in "0123456789abcdef" for character in args.rootfs_sha256
    ):
        parser.error("--rootfs-sha256 must be 64 lowercase hexadecimal digits")
    if args.rootfs_size_bytes < 1:
        parser.error("--rootfs-size-bytes must be positive")
    if not args.installed_rootfs.startswith("/"):
        parser.error("--installed-rootfs must be absolute")
    return args


def main() -> int:
    try:
        result = build(parse_args())
    except ValueError as error:
        print(f"build-image-catalog: {error}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
