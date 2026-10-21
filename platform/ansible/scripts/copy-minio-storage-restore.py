#!/usr/bin/env python3
"""Copy and verify the current MinIO snapshot into an empty recovery bucket."""

from __future__ import annotations

import argparse
import functools
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import stat
import subprocess
import sys
import tempfile
import time
from typing import Any, Callable
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


Runner = Callable[..., subprocess.CompletedProcess[str]]
StreamHasher = Callable[[list[str], dict[str, str], int], tuple[str, int]]
REHEARSAL_ID = re.compile(
    r"restore-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}"
)
SET_ID = re.compile(r"backup-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
BUCKET = re.compile(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]")
HEX_SECRET = re.compile(r"[0-9a-f]{64}")
NOT_FOUND = re.compile(
    r"not[ _-]*found|does not exist|no such|nosuch",
    re.IGNORECASE,
)


class RecoveryCopyError(ValueError):
    """The isolated recovery bucket could not be copied safely."""


def require_private_directory(path: Path, expected_owner_uid: int | None) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise RecoveryCopyError(f"{path}: directory is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise RecoveryCopyError(f"{path}: must be a non-symlink directory")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise RecoveryCopyError(f"{path}: has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise RecoveryCopyError(f"{path}: must have mode 0700")


require_private_file = functools.partial(
    common.require_private_file, error=RecoveryCopyError
)


def load_json(path: Path, name: str, expected_owner_uid: int | None) -> dict[str, Any]:
    require_private_file(path, expected_owner_uid)
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise RecoveryCopyError(f"{name} is not valid JSON") from error
    if not isinstance(value, dict):
        raise RecoveryCopyError(f"{name} must be an object")
    return value


def load_credentials(path: Path, expected_owner_uid: int | None) -> tuple[str, str]:
    require_private_file(path, expected_owner_uid)
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeError) as error:
        raise RecoveryCopyError("isolated MinIO credentials are unreadable") from error
    values: dict[str, str] = {}
    for line in lines:
        key, separator, value = line.partition("=")
        if not separator or key in values:
            raise RecoveryCopyError("isolated MinIO credentials have invalid syntax")
        values[key] = value
    if set(values) != {"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"}:
        raise RecoveryCopyError("isolated MinIO credentials have an invalid shape")
    if values["MINIO_ROOT_USER"] != "restoreadmin":
        raise RecoveryCopyError("isolated MinIO root user is unexpected")
    if HEX_SECRET.fullmatch(values["MINIO_ROOT_PASSWORD"]) is None:
        raise RecoveryCopyError("isolated MinIO password is invalid")
    return values["MINIO_ROOT_USER"], values["MINIO_ROOT_PASSWORD"]


def parse_json_stream(value: str, name: str) -> list[dict[str, Any]]:
    documents = []
    for line in value.splitlines():
        if not line.strip():
            continue
        try:
            document = json.loads(line)
        except json.JSONDecodeError as error:
            raise RecoveryCopyError(f"{name} returned invalid JSON") from error
        if not isinstance(document, dict) or document.get("status") != "success":
            raise RecoveryCopyError(f"{name} returned an unsuccessful result")
        documents.append(document)
    if not documents:
        raise RecoveryCopyError(f"{name} returned no JSON evidence")
    return documents


def run_mc(
    runner: Runner,
    command: list[str],
    environment: dict[str, str],
    timeout_seconds: int,
    name: str,
) -> list[dict[str, Any]]:
    result = runner(
        command,
        env=environment,
        capture_output=True,
        text=True,
        timeout=timeout_seconds,
        check=False,
    )
    if result.returncode != 0:
        raise RecoveryCopyError(f"{name} failed")
    return parse_json_stream(result.stdout, name)


def subprocess_stream_hash(
    command: list[str],
    environment: dict[str, str],
    timeout_seconds: int,
) -> tuple[str, int]:
    digest = hashlib.sha256()
    size = 0
    deadline = time.monotonic() + timeout_seconds
    with tempfile.TemporaryFile() as errors:
        process = subprocess.Popen(
            command,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=errors,
        )
        assert process.stdout is not None
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        try:
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(timeout=remaining):
                    raise subprocess.TimeoutExpired(command, timeout_seconds)
                chunk = os.read(process.stdout.fileno(), 1024 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
                size += len(chunk)
            returncode = process.wait(
                timeout=max(1, int(deadline - time.monotonic()))
            )
        except subprocess.TimeoutExpired as error:
            process.kill()
            process.wait()
            raise RecoveryCopyError("object content verification timed out") from error
        finally:
            selector.close()
        if returncode != 0:
            raise RecoveryCopyError("object content could not be read")
    return digest.hexdigest(), size


def object_key(document: dict[str, Any]) -> tuple[str, int] | None:
    if document.get("type") in {"folder", "directory"}:
        return None
    raw_key = document.get("key")
    size = document.get("size")
    if not isinstance(raw_key, str) or not isinstance(size, int) or isinstance(size, bool):
        raise RecoveryCopyError("object listing entry has an invalid shape")
    key = raw_key
    path = PurePosixPath(key)
    if (
        not key
        or path.is_absolute()
        or key != path.as_posix()
        or any(part in {"", ".", ".."} for part in path.parts)
        or any(ord(character) < 32 for character in key)
        or size < 0
    ):
        raise RecoveryCopyError("object listing contains an unsafe key")
    return key, size


def list_bucket(
    runner: Runner,
    mc_binary: Path,
    alias: str,
    bucket: str,
    environment: dict[str, str],
    timeout_seconds: int,
) -> dict[str, int]:
    target = f"{alias}/{bucket}"
    result = runner(
        [str(mc_binary), "ls", "--recursive", target, "--json"],
        env=environment,
        capture_output=True,
        text=True,
        timeout=timeout_seconds,
        check=False,
    )
    if result.returncode != 0:
        raise RecoveryCopyError("bucket listing failed")
    documents = (
        parse_json_stream(result.stdout, "bucket listing")
        if result.stdout.strip()
        else []
    )
    listed: dict[str, int] = {}
    for document in documents:
        item = object_key(document)
        if item is None:
            continue
        key, size = item
        if key in listed:
            raise RecoveryCopyError("object listing contains duplicate keys")
        listed[key] = size
    return listed


def hash_bucket(
    runner: Runner,
    stream_hasher: StreamHasher,
    mc_binary: Path,
    alias: str,
    bucket: str,
    environment: dict[str, str],
    timeout_seconds: int,
) -> dict[str, Any]:
    target = f"{alias}/{bucket}"
    listed = list_bucket(
        runner,
        mc_binary,
        alias,
        bucket,
        environment,
        timeout_seconds,
    )
    if not listed:
        raise RecoveryCopyError("source bucket contains no current objects")

    digest = hashlib.sha256()
    total_bytes = 0
    for key in sorted(listed):
        checksum, actual_size = stream_hasher(
            [str(mc_binary), "cat", f"{target}/{key}"],
            environment,
            timeout_seconds,
        )
        if actual_size != listed[key]:
            raise RecoveryCopyError("listed object size changed during verification")
        digest.update(
            b"F\0"
            + key.encode()
            + b"\0"
            + str(actual_size).encode()
            + b"\0"
            + checksum.encode()
            + b"\0"
        )
        total_bytes += actual_size
    return {
        "object_count": len(listed),
        "total_bytes": total_bytes,
        "tree_sha256": digest.hexdigest(),
    }


def copy_recovery_bucket(
    rehearsal_id: str,
    restore_root: Path,
    mc_binary: Path,
    *,
    timeout_seconds: int = 86400,
    expected_owner_uid: int | None = None,
    runner: Runner = subprocess.run,
    stream_hasher: StreamHasher = subprocess_stream_hash,
) -> dict[str, Any]:
    if REHEARSAL_ID.fullmatch(rehearsal_id) is None:
        raise RecoveryCopyError("restore rehearsal ID is invalid")
    if not restore_root.is_absolute():
        raise RecoveryCopyError("restore root must be absolute")
    if timeout_seconds < 60 or timeout_seconds > 172800:
        raise RecoveryCopyError("timeout is outside safe bounds")
    if (
        not mc_binary.is_absolute()
        or not mc_binary.is_file()
        or mc_binary.is_symlink()
        or not os.access(mc_binary, os.X_OK)
    ):
        raise RecoveryCopyError("mc must be an absolute executable regular file")

    require_private_directory(restore_root, expected_owner_uid)
    workspace = restore_root / rehearsal_id
    require_private_directory(workspace, expected_owner_uid)
    runtime = load_json(
        workspace / "minio-runtime.json",
        "isolated MinIO runtime evidence",
        expected_owner_uid,
    )
    extraction = load_json(
        workspace / "minio-extraction.json",
        "MinIO extraction evidence",
        expected_owner_uid,
    )
    extraction_source = extraction.get("source")
    if (
        not isinstance(extraction_source, dict)
        or runtime.get("schema_version") != 1
        or runtime.get("status") != "isolated-minio-qualified"
        or runtime.get("rehearsal_id") != rehearsal_id
        or runtime.get("set_id") != extraction.get("set_id")
        or runtime.get("object_snapshot_id")
        != extraction_source.get("object_snapshot_id")
    ):
        raise RecoveryCopyError("isolated MinIO runtime binding is invalid")
    endpoint = runtime.get("endpoint_url")
    source_bucket = runtime.get("bucket")
    if (
        not isinstance(endpoint, str)
        or re.fullmatch(r"http://127\.0\.0\.1:[0-9]{4,5}", endpoint) is None
        or not isinstance(source_bucket, str)
        or BUCKET.fullmatch(source_bucket) is None
    ):
        raise RecoveryCopyError("isolated MinIO endpoint or bucket is invalid")
    set_id = runtime["set_id"]
    if not isinstance(set_id, str) or SET_ID.fullmatch(set_id) is None:
        raise RecoveryCopyError("isolated MinIO set ID is invalid")
    recovery_bucket = (
        "fire-fuse-recovery-"
        + rehearsal_id.removeprefix("restore-").lower()
    )
    if BUCKET.fullmatch(recovery_bucket) is None:
        raise RecoveryCopyError("derived recovery bucket is invalid")
    output = workspace / "minio-recovery-bucket.json"
    if output.exists() or output.is_symlink():
        raise RecoveryCopyError("recovery bucket evidence already exists")

    credential_path = workspace / "services" / "minio" / "minio.env"
    username, password = load_credentials(credential_path, expected_owner_uid)
    alias = "restored"
    config_directory = workspace / "services" / "minio" / "mc-copy"
    try:
        config_directory.mkdir(mode=0o700)
    except FileExistsError as error:
        raise RecoveryCopyError("recovery copy client directory already exists") from error
    environment = {
        "HOME": "/root",
        "MC_CONFIG_DIR": str(config_directory),
        f"MC_HOST_{alias}": (
            f"http://{quote(username, safe='')}:{quote(password, safe='')}"
            f"@{endpoint.removeprefix('http://')}"
        ),
        "MC_NO_COLOR": "1",
    }
    target = f"{alias}/{recovery_bucket}"
    existence = runner(
        [str(mc_binary), "stat", target, "--json"],
        env=environment,
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    if existence.returncode == 0:
        raise RecoveryCopyError("recovery bucket already exists")
    if NOT_FOUND.search(f"{existence.stdout}\n{existence.stderr}") is None:
        raise RecoveryCopyError("recovery bucket absence could not be established")

    source_before = hash_bucket(
        runner,
        stream_hasher,
        mc_binary,
        alias,
        source_bucket,
        environment,
        timeout_seconds,
    )
    run_mc(
        runner,
        [str(mc_binary), "mb", target, "--json"],
        environment,
        30,
        "recovery bucket creation",
    )
    if list_bucket(
        runner,
        mc_binary,
        alias,
        recovery_bucket,
        environment,
        30,
    ):
        raise RecoveryCopyError("new recovery bucket is not empty")
    run_mc(
        runner,
        [
            str(mc_binary),
            "mirror",
            "--summary",
            f"{alias}/{source_bucket}",
            target,
            "--json",
        ],
        environment,
        timeout_seconds,
        "recovery bucket copy",
    )
    source_after = hash_bucket(
        runner,
        stream_hasher,
        mc_binary,
        alias,
        source_bucket,
        environment,
        timeout_seconds,
    )
    recovered = hash_bucket(
        runner,
        stream_hasher,
        mc_binary,
        alias,
        recovery_bucket,
        environment,
        timeout_seconds,
    )
    if source_before != source_after:
        raise RecoveryCopyError("captured source bucket changed during recovery copy")
    if source_before != recovered:
        raise RecoveryCopyError("recovery bucket content does not match the snapshot")

    result = {
        "schema_version": 1,
        "status": "minio-recovery-bucket-qualified",
        "rehearsal_id": rehearsal_id,
        "set_id": set_id,
        "endpoint_url": endpoint,
        "source_bucket": source_bucket,
        "recovery_bucket": recovery_bucket,
        "object_snapshot_id": runtime["object_snapshot_id"],
        **recovered,
        "source_snapshot_unchanged": True,
    }
    temporary = workspace / ".minio-recovery-bucket.json.partial"
    temporary.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary, 0o600)
    temporary.replace(output)
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Copy the isolated MinIO snapshot to an empty recovery bucket",
    )
    parser.add_argument("rehearsal_id")
    parser.add_argument(
        "--restore-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-restores/staging"),
    )
    parser.add_argument("--mc", type=Path, default=Path("/usr/local/bin/mc"))
    parser.add_argument("--timeout-seconds", type=int, default=86400)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        result = copy_recovery_bucket(
            args.rehearsal_id,
            args.restore_root,
            args.mc,
            timeout_seconds=args.timeout_seconds,
            expected_owner_uid=0,
        )
    except (RecoveryCopyError, OSError) as error:
        print(f"MinIO recovery copy failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
