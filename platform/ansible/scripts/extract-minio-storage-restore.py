#!/usr/bin/env python3
"""Safely extract a materialized MinIO snapshot for a restore rehearsal."""

from __future__ import annotations

import argparse
import functools
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import tarfile
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


VERIFIER = common.load_sibling("verify_storage_backup", "verify-storage-backup.py")

SET_ID = re.compile(r"backup-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
REHEARSAL_ID = re.compile(
    r"restore-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}"
)
SHA256 = re.compile(r"[0-9a-f]{64}")
SAFE_VERSION = re.compile(r"[ -~]{1,512}")
MATERIALIZATION_KEYS = {
    "schema_version",
    "status",
    "rehearsal_id",
    "set_id",
    "materialized_at",
    "workspace",
    "source",
    "artifacts",
}
SOURCE_KEYS = {
    "endpoint_url",
    "bucket",
    "remote_prefix",
    "transfer_record_sha256",
    "manifest_version_id",
    "manifest_sha256",
}
ARTIFACT_KEYS = {"path", "version_id", "size_bytes", "sha256"}


class ExtractionError(ValueError):
    """The materialized MinIO archive cannot be extracted safely."""


require_exact_keys = functools.partial(common.require_exact_keys, error=ExtractionError)


require_mapping = functools.partial(common.require_mapping, error=ExtractionError)


require_private_file = functools.partial(
    common.require_private_file, error=ExtractionError
)


def require_private_directory(path: Path, expected_owner_uid: int | None) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise ExtractionError(f"{path}: directory is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise ExtractionError(f"{path}: must be a non-symlink directory")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise ExtractionError(f"{path}: has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise ExtractionError(f"{path}: must have mode 0700")


def load_materialization(
    workspace: Path,
    rehearsal_id: str,
    expected_owner_uid: int | None,
) -> tuple[dict[str, Any], dict[str, Any]]:
    record_path = workspace / "materialization.json"
    manifest_path = workspace / "manifest.json"
    require_private_file(record_path, expected_owner_uid)
    require_private_file(manifest_path, expected_owner_uid)
    try:
        record = require_mapping(
            json.loads(record_path.read_text(encoding="utf-8")),
            "materialization record",
        )
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ExtractionError("materialization record is not valid JSON") from error
    require_exact_keys(record, MATERIALIZATION_KEYS, "materialization record")
    if record["schema_version"] != 1:
        raise ExtractionError("materialization schema_version must be 1")
    if record["status"] != "materialized-and-verified":
        raise ExtractionError("materialization is not verified")
    if record["rehearsal_id"] != rehearsal_id:
        raise ExtractionError("materialization rehearsal ID does not match")
    if record["workspace"] != str(workspace):
        raise ExtractionError("materialization workspace binding does not match")
    set_id = record["set_id"]
    if not isinstance(set_id, str) or SET_ID.fullmatch(set_id) is None:
        raise ExtractionError("materialization set ID is invalid")

    try:
        manifest = VERIFIER.verify_manifest(manifest_path)
    except VERIFIER.VerificationError as error:
        raise ExtractionError(str(error)) from error
    if manifest["set_id"] != set_id:
        raise ExtractionError("materialization manifest set ID does not match")

    source = require_mapping(record["source"], "materialization source")
    require_exact_keys(source, SOURCE_KEYS, "materialization source")
    manifest_sha256 = VERIFIER.sha256_file(manifest_path)
    if source["manifest_sha256"] != manifest_sha256:
        raise ExtractionError("materialization manifest checksum does not match")
    if (
        not isinstance(source["transfer_record_sha256"], str)
        or SHA256.fullmatch(source["transfer_record_sha256"]) is None
    ):
        raise ExtractionError("materialization transfer checksum is invalid")
    if (
        not isinstance(source["manifest_version_id"], str)
        or SAFE_VERSION.fullmatch(source["manifest_version_id"]) is None
    ):
        raise ExtractionError("materialization manifest version is invalid")

    expected_artifacts = {
        artifact["path"]: artifact for artifact in manifest["artifacts"]
    }
    raw_artifacts = record["artifacts"]
    if not isinstance(raw_artifacts, list) or len(raw_artifacts) != 4:
        raise ExtractionError("materialization must contain four artifacts")
    observed: set[str] = set()
    for index, raw in enumerate(raw_artifacts):
        artifact = require_mapping(raw, f"materialization artifact {index}")
        require_exact_keys(
            artifact,
            ARTIFACT_KEYS,
            f"materialization artifact {index}",
        )
        path = artifact["path"]
        if path not in expected_artifacts or path in observed:
            raise ExtractionError("materialization artifact set is invalid")
        expected = expected_artifacts[path]
        if (
            artifact["size_bytes"] != expected["size_bytes"]
            or artifact["sha256"] != expected["sha256"]
        ):
            raise ExtractionError("materialization artifact binding does not match")
        if (
            not isinstance(artifact["version_id"], str)
            or SAFE_VERSION.fullmatch(artifact["version_id"]) is None
        ):
            raise ExtractionError("materialization artifact version is invalid")
        require_private_file(workspace / path, expected_owner_uid)
        observed.add(path)
    if observed != set(expected_artifacts):
        raise ExtractionError("materialization artifact set is incomplete")
    return record, manifest


def clean_member_path(value: str) -> tuple[str, ...]:
    path = PurePosixPath(value)
    parts = tuple(part for part in path.parts if part != ".")
    if (
        path.is_absolute()
        or not parts
        or any(part in {"", ".", ".."} for part in parts)
    ):
        raise ExtractionError("MinIO archive contains an unsafe member path")
    return parts


def hash_regular_tree(root: Path) -> dict[str, Any]:
    try:
        root_metadata = root.lstat()
    except OSError as error:
        raise ExtractionError("extracted MinIO data directory is unreadable") from error
    if stat.S_ISLNK(root_metadata.st_mode) or not stat.S_ISDIR(root_metadata.st_mode):
        raise ExtractionError("extracted MinIO data is not a safe directory")
    entries: list[bytes] = []
    regular_file_count = 0
    total_file_bytes = 0
    for directory, directory_names, file_names in os.walk(
        root,
        topdown=True,
        followlinks=False,
    ):
        directory_path = Path(directory)
        for name in sorted(directory_names + file_names):
            path = directory_path / name
            relative = path.relative_to(root).as_posix()
            metadata = path.lstat()
            if stat.S_ISLNK(metadata.st_mode):
                raise ExtractionError("extracted MinIO data contains a link")
            if stat.S_ISDIR(metadata.st_mode):
                entries.append(b"D\0" + relative.encode() + b"\0")
                continue
            if not stat.S_ISREG(metadata.st_mode):
                raise ExtractionError(
                    "extracted MinIO data contains a special file"
                )
            checksum = VERIFIER.sha256_file(path)
            entries.append(
                b"F\0"
                + relative.encode()
                + b"\0"
                + str(metadata.st_size).encode()
                + b"\0"
                + checksum.encode()
                + b"\0"
            )
            regular_file_count += 1
            total_file_bytes += metadata.st_size
    digest = hashlib.sha256()
    for entry in sorted(entries):
        digest.update(entry)
    return {
        "tree_sha256": digest.hexdigest(),
        "member_count": len(entries),
        "regular_file_count": regular_file_count,
        "total_file_bytes": total_file_bytes,
    }


def verify_existing_extraction(
    rehearsal_id: str,
    restore_root: Path,
    *,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    if REHEARSAL_ID.fullmatch(rehearsal_id) is None:
        raise ExtractionError("restore rehearsal ID is invalid")
    if not restore_root.is_absolute():
        raise ExtractionError("restore root must be absolute")
    require_private_directory(restore_root, expected_owner_uid)
    workspace = restore_root / rehearsal_id
    require_private_directory(workspace, expected_owner_uid)
    materialization, _ = load_materialization(
        workspace,
        rehearsal_id,
        expected_owner_uid,
    )
    record_path = workspace / "minio-extraction.json"
    require_private_file(record_path, expected_owner_uid)
    try:
        record = require_mapping(
            json.loads(record_path.read_text(encoding="utf-8")),
            "MinIO extraction record",
        )
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ExtractionError("MinIO extraction record is not valid JSON") from error
    if (
        record.get("schema_version") != 1
        or record.get("status") != "minio-snapshot-extracted"
        or record.get("rehearsal_id") != rehearsal_id
        or record.get("set_id") != materialization["set_id"]
    ):
        raise ExtractionError("MinIO extraction record binding is invalid")
    target = require_mapping(record.get("target"), "MinIO extraction target")
    data_directory = workspace / "services" / "minio" / "data"
    if target.get("data_directory") != str(data_directory):
        raise ExtractionError("MinIO extraction data-directory binding is invalid")
    observed = hash_regular_tree(data_directory)
    for key in (
        "tree_sha256",
        "member_count",
        "regular_file_count",
        "total_file_bytes",
    ):
        if target.get(key) != observed[key]:
            raise ExtractionError("extracted MinIO data changed after extraction")
    return {
        "schema_version": 1,
        "status": "minio-extraction-verified",
        "rehearsal_id": rehearsal_id,
        "set_id": materialization["set_id"],
        "data_directory": str(data_directory),
        **observed,
    }


def extract_minio_snapshot(
    rehearsal_id: str,
    restore_root: Path,
    *,
    bucket: str = "fire-fuse-workspaces",
    maximum_members: int = 1_000_000,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    if REHEARSAL_ID.fullmatch(rehearsal_id) is None:
        raise ExtractionError("restore rehearsal ID is invalid")
    if not restore_root.is_absolute():
        raise ExtractionError("restore root must be absolute")
    if not isinstance(maximum_members, int) or isinstance(maximum_members, bool):
        raise ExtractionError("maximum member count must be an integer")
    if maximum_members < 2 or maximum_members > 10_000_000:
        raise ExtractionError("maximum member count is outside safe bounds")
    if re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", bucket) is None:
        raise ExtractionError("workspace bucket name is invalid")

    require_private_directory(restore_root, expected_owner_uid)
    workspace = restore_root / rehearsal_id
    require_private_directory(workspace, expected_owner_uid)
    materialization, manifest = load_materialization(
        workspace,
        rehearsal_id,
        expected_owner_uid,
    )
    minio_artifact = next(
        artifact
        for artifact in manifest["artifacts"]
        if artifact["component"] == "minio_objects"
    )
    archive = workspace / minio_artifact["path"]
    if (
        manifest["source"]["object_snapshot_id"]
        != f"sha256:{minio_artifact['sha256']}"
    ):
        raise ExtractionError("object snapshot identity does not match its archive")

    service_root = workspace / "services"
    minio_root = service_root / "minio"
    data_directory = minio_root / "data"
    try:
        service_root.mkdir(mode=0o700)
        minio_root.mkdir(mode=0o700)
        data_directory.mkdir(mode=0o700)
    except FileExistsError as error:
        raise ExtractionError("MinIO restore target already exists") from error
    except OSError as error:
        raise ExtractionError("MinIO restore target cannot be created") from error

    member_count = 0
    regular_file_count = 0
    total_file_bytes = 0
    observed_paths: set[tuple[str, ...]] = set()
    format_found = False
    bucket_found = False
    try:
        with tarfile.open(archive, mode="r:") as source:
            for member in source:
                member_count += 1
                if member_count > maximum_members:
                    raise ExtractionError("MinIO archive has too many members")
                if member.name in {".", "./"} and member.isdir():
                    continue
                parts = clean_member_path(member.name)
                if parts in observed_paths:
                    raise ExtractionError("MinIO archive contains duplicate paths")
                observed_paths.add(parts)
                if not (member.isdir() or member.isreg()):
                    raise ExtractionError(
                        "MinIO archive contains links or special files"
                    )
                target = data_directory.joinpath(*parts)
                if member.isdir():
                    target.mkdir(mode=0o700, parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    extracted = source.extractfile(member)
                    if extracted is None:
                        raise ExtractionError("MinIO archive file cannot be read")
                    with target.open("xb") as output:
                        os.chmod(target, 0o600)
                        while chunk := extracted.read(1024 * 1024):
                            output.write(chunk)
                            total_file_bytes += len(chunk)
                            if total_file_bytes > minio_artifact["size_bytes"]:
                                raise ExtractionError(
                                    "MinIO archive expands beyond its raw size"
                                )
                        output.flush()
                        os.fsync(output.fileno())
                    if target.stat().st_size != member.size:
                        raise ExtractionError("MinIO archive file size changed")
                    regular_file_count += 1
                format_found |= parts == (".minio.sys", "format.json")
                bucket_found |= parts == (bucket,)
    except (OSError, tarfile.TarError) as error:
        raise ExtractionError("MinIO archive is not a readable raw tar") from error

    if not format_found or not bucket_found:
        raise ExtractionError(
            "MinIO archive omitted the storage format or workspace bucket"
        )
    if regular_file_count < 1:
        raise ExtractionError("MinIO archive contains no regular files")
    tree = hash_regular_tree(data_directory)
    if (
        tree["regular_file_count"] != regular_file_count
        or tree["total_file_bytes"] != total_file_bytes
    ):
        raise ExtractionError("extracted MinIO tree does not match the archive")

    result = {
        "schema_version": 1,
        "status": "minio-snapshot-extracted",
        "rehearsal_id": rehearsal_id,
        "set_id": materialization["set_id"],
        "source": {
            "archive": minio_artifact["path"],
            "size_bytes": minio_artifact["size_bytes"],
            "sha256": minio_artifact["sha256"],
            "object_snapshot_id": manifest["source"]["object_snapshot_id"],
        },
        "target": {
            "data_directory": str(data_directory),
            "member_count": tree["member_count"],
            "regular_file_count": regular_file_count,
            "total_file_bytes": total_file_bytes,
            "tree_sha256": tree["tree_sha256"],
            "format_path": str(data_directory / ".minio.sys" / "format.json"),
            "bucket_path": str(data_directory / bucket),
        },
    }
    temporary_record = workspace / ".minio-extraction.json.partial"
    record_path = workspace / "minio-extraction.json"
    temporary_record.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary_record, 0o600)
    temporary_record.replace(record_path)
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Safely extract a materialized MinIO restore snapshot",
    )
    parser.add_argument("rehearsal_id")
    parser.add_argument(
        "--restore-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-restores/staging"),
    )
    parser.add_argument("--bucket", default="fire-fuse-workspaces")
    parser.add_argument("--maximum-members", type=int, default=1_000_000)
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
            result = extract_minio_snapshot(
                args.rehearsal_id,
                args.restore_root,
                bucket=args.bucket,
                maximum_members=args.maximum_members,
                expected_owner_uid=0,
            )
    except (ExtractionError, OSError) as error:
        print(f"MinIO restore extraction failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
