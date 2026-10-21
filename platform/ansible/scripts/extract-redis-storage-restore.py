#!/usr/bin/env python3
"""Safely extract and verify a materialized Redis persistence snapshot."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import stat
import sys
import tarfile
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


MINIO_EXTRACTOR = common.load_sibling(
    "extract_minio_storage_restore",
    "extract-minio-storage-restore.py",
)


class RedisExtractionError(ValueError):
    """The Redis persistence archive cannot be extracted safely."""


def require_new_target(
    workspace: Path,
    expected_owner_uid: int | None,
) -> Path:
    services = workspace / "services"
    service_root = services / "redis"
    data_directory = service_root / "data"
    try:
        services.mkdir(mode=0o700, exist_ok=True)
        MINIO_EXTRACTOR.require_private_directory(
            services,
            expected_owner_uid=expected_owner_uid,
        )
        service_root.mkdir(mode=0o700)
        data_directory.mkdir(mode=0o700)
    except FileExistsError as error:
        raise RedisExtractionError("Redis restore target already exists") from error
    except MINIO_EXTRACTOR.ExtractionError as error:
        raise RedisExtractionError(str(error)) from error
    except OSError as error:
        raise RedisExtractionError("Redis restore target cannot be created") from error
    return data_directory


def extract_redis_snapshot(
    rehearsal_id: str,
    restore_root: Path,
    *,
    maximum_members: int = 1_000_000,
    maximum_expanded_bytes: int = 16 * 1024 * 1024 * 1024,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    if MINIO_EXTRACTOR.REHEARSAL_ID.fullmatch(rehearsal_id) is None:
        raise RedisExtractionError("restore rehearsal ID is invalid")
    if not restore_root.is_absolute():
        raise RedisExtractionError("restore root must be absolute")
    if (
        isinstance(maximum_members, bool)
        or not isinstance(maximum_members, int)
        or maximum_members < 3
        or maximum_members > 10_000_000
    ):
        raise RedisExtractionError("maximum member count is outside safe bounds")
    if (
        isinstance(maximum_expanded_bytes, bool)
        or not isinstance(maximum_expanded_bytes, int)
        or maximum_expanded_bytes < 1024 * 1024
        or maximum_expanded_bytes > 1024 * 1024 * 1024 * 1024
    ):
        raise RedisExtractionError("maximum expanded size is outside safe bounds")

    try:
        MINIO_EXTRACTOR.require_private_directory(
            restore_root,
            expected_owner_uid=expected_owner_uid,
        )
        workspace = restore_root / rehearsal_id
        MINIO_EXTRACTOR.require_private_directory(
            workspace,
            expected_owner_uid=expected_owner_uid,
        )
        materialization, manifest = MINIO_EXTRACTOR.load_materialization(
            workspace,
            rehearsal_id,
            expected_owner_uid,
        )
    except MINIO_EXTRACTOR.ExtractionError as error:
        raise RedisExtractionError(str(error)) from error
    artifact = next(
        item
        for item in manifest["artifacts"]
        if item["component"] == "redis_persistence"
    )
    archive = workspace / artifact["path"]
    data_directory = require_new_target(workspace, expected_owner_uid)
    observed: set[tuple[str, ...]] = set()
    member_count = 0
    regular_file_count = 0
    total_file_bytes = 0
    dump_found = False
    aof_files = 0
    try:
        with tarfile.open(archive, mode="r:gz") as source:
            for member in source:
                member_count += 1
                if member_count > maximum_members:
                    raise RedisExtractionError(
                        "Redis archive has too many members"
                    )
                if member.name in {".", "./"} and member.isdir():
                    continue
                try:
                    parts = MINIO_EXTRACTOR.clean_member_path(member.name)
                except MINIO_EXTRACTOR.ExtractionError as error:
                    raise RedisExtractionError(str(error)) from error
                if parts in observed:
                    raise RedisExtractionError(
                        "Redis archive contains duplicate paths"
                    )
                observed.add(parts)
                if not (member.isdir() or member.isreg()):
                    raise RedisExtractionError(
                        "Redis archive contains links or special files"
                    )
                if member.size < 0 or total_file_bytes + member.size > maximum_expanded_bytes:
                    raise RedisExtractionError(
                        "Redis archive exceeds the expanded-size limit"
                    )
                target = data_directory.joinpath(*parts)
                if member.isdir():
                    target.mkdir(mode=0o700, parents=True, exist_ok=True)
                    continue
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                extracted = source.extractfile(member)
                if extracted is None:
                    raise RedisExtractionError("Redis archive file cannot be read")
                written = 0
                with target.open("xb") as output:
                    os.chmod(target, 0o600)
                    while chunk := extracted.read(1024 * 1024):
                        output.write(chunk)
                        written += len(chunk)
                    output.flush()
                    os.fsync(output.fileno())
                if written != member.size:
                    raise RedisExtractionError("Redis archive file size changed")
                total_file_bytes += written
                regular_file_count += 1
                dump_found |= parts == ("dump.rdb",)
                aof_files += int(
                    len(parts) >= 2 and parts[0] == "appendonlydir"
                )
    except (OSError, tarfile.TarError) as error:
        raise RedisExtractionError(
            "Redis archive is not a readable gzip tar"
        ) from error
    if not dump_found or aof_files < 2:
        raise RedisExtractionError(
            "Redis archive omitted dump.rdb or multi-part AOF persistence"
        )
    try:
        tree = MINIO_EXTRACTOR.hash_regular_tree(data_directory)
    except MINIO_EXTRACTOR.ExtractionError as error:
        raise RedisExtractionError(str(error)) from error
    if (
        tree["regular_file_count"] != regular_file_count
        or tree["total_file_bytes"] != total_file_bytes
    ):
        raise RedisExtractionError("extracted Redis tree does not match archive")

    result = {
        "schema_version": 1,
        "status": "redis-snapshot-extracted",
        "rehearsal_id": rehearsal_id,
        "set_id": materialization["set_id"],
        "source": {
            "archive": artifact["path"],
            "size_bytes": artifact["size_bytes"],
            "sha256": artifact["sha256"],
            "redis_version": manifest["source"]["redis_version"],
        },
        "target": {
            "data_directory": str(data_directory),
            "member_count": tree["member_count"],
            "regular_file_count": tree["regular_file_count"],
            "total_file_bytes": tree["total_file_bytes"],
            "tree_sha256": tree["tree_sha256"],
            "dump_path": str(data_directory / "dump.rdb"),
            "aof_directory": str(data_directory / "appendonlydir"),
            "aof_file_count": aof_files,
        },
    }
    temporary = workspace / ".redis-extraction.json.partial"
    output = workspace / "redis-extraction.json"
    temporary.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary, 0o600)
    temporary.replace(output)
    return result


def verify_existing_extraction(
    rehearsal_id: str,
    restore_root: Path,
    *,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    if MINIO_EXTRACTOR.REHEARSAL_ID.fullmatch(rehearsal_id) is None:
        raise RedisExtractionError("restore rehearsal ID is invalid")
    workspace = restore_root / rehearsal_id
    try:
        MINIO_EXTRACTOR.require_private_directory(
            restore_root,
            expected_owner_uid=expected_owner_uid,
        )
        MINIO_EXTRACTOR.require_private_directory(
            workspace,
            expected_owner_uid=expected_owner_uid,
        )
        materialization, _ = MINIO_EXTRACTOR.load_materialization(
            workspace,
            rehearsal_id,
            expected_owner_uid,
        )
        record_path = workspace / "redis-extraction.json"
        MINIO_EXTRACTOR.require_private_file(
            record_path,
            expected_owner_uid=expected_owner_uid,
        )
    except MINIO_EXTRACTOR.ExtractionError as error:
        raise RedisExtractionError(str(error)) from error
    try:
        record = json.loads(record_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise RedisExtractionError(
            "Redis extraction record is not valid JSON"
        ) from error
    if (
        not isinstance(record, dict)
        or record.get("schema_version") != 1
        or record.get("status") != "redis-snapshot-extracted"
        or record.get("rehearsal_id") != rehearsal_id
        or record.get("set_id") != materialization["set_id"]
        or not isinstance(record.get("target"), dict)
    ):
        raise RedisExtractionError("Redis extraction record binding is invalid")
    data_directory = workspace / "services" / "redis" / "data"
    if record["target"].get("data_directory") != str(data_directory):
        raise RedisExtractionError("Redis data-directory binding is invalid")
    try:
        observed = MINIO_EXTRACTOR.hash_regular_tree(data_directory)
    except MINIO_EXTRACTOR.ExtractionError as error:
        raise RedisExtractionError(str(error)) from error
    for key in (
        "tree_sha256",
        "member_count",
        "regular_file_count",
        "total_file_bytes",
    ):
        if record["target"].get(key) != observed[key]:
            raise RedisExtractionError("extracted Redis data changed")
    return {
        "schema_version": 1,
        "status": "redis-extraction-verified",
        "rehearsal_id": rehearsal_id,
        "set_id": materialization["set_id"],
        "data_directory": str(data_directory),
        **observed,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Safely extract a materialized Redis persistence snapshot",
    )
    parser.add_argument("rehearsal_id")
    parser.add_argument(
        "--restore-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-restores/staging"),
    )
    parser.add_argument("--maximum-members", type=int, default=1_000_000)
    parser.add_argument(
        "--maximum-expanded-bytes",
        type=int,
        default=16 * 1024 * 1024 * 1024,
    )
    parser.add_argument("--verify-existing", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.verify_existing:
            result = verify_existing_extraction(
                args.rehearsal_id,
                args.restore_root,
                expected_owner_uid=0,
            )
        else:
            result = extract_redis_snapshot(
                args.rehearsal_id,
                args.restore_root,
                maximum_members=args.maximum_members,
                maximum_expanded_bytes=args.maximum_expanded_bytes,
                expected_owner_uid=0,
            )
    except (RedisExtractionError, OSError) as error:
        print(f"Redis restore extraction failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
