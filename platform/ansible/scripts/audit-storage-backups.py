#!/usr/bin/env python3
"""Audit local JuiceFS backup sets and off-host transfer records."""

from __future__ import annotations

import argparse
from collections import Counter
from datetime import datetime
import functools
import json
from pathlib import Path
import re
import stat
import sys
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


VERIFIER = common.load_sibling("verify_storage_backup", "verify-storage-backup.py")

SET_ID = re.compile(r"backup-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
SHA256 = re.compile(r"[0-9a-f]{64}")
SAFE_TEXT = re.compile(r"[ -~]{1,512}")
HTTPS_ENDPOINT = re.compile(r"https://[^/?#@\s]+/?")
BUCKET = re.compile(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]")
REMOTE_PREFIX = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]{1,382}")
RECORD_KEYS = {
    "schema_version",
    "set_id",
    "recorded_at",
    "local",
    "remote",
}
LOCAL_KEYS = {"set_directory", "manifest_path", "manifest_sha256"}
REMOTE_KEYS = {
    "endpoint_url",
    "bucket",
    "prefix",
    "artifacts",
    "manifest",
}
EVIDENCE_KEYS = {"path", "version_id", "sha256", "retained_until"}


class AuditError(ValueError):
    """A local backup or transfer record is not trustworthy."""


def path_lexists(path: Path) -> bool:
    try:
        path.lstat()
    except FileNotFoundError:
        return False
    except OSError as error:
        raise AuditError(f"{path}: cannot inspect path") from error
    return True


require_exact_keys = functools.partial(common.require_exact_keys, error=AuditError)


require_mapping = functools.partial(common.require_mapping, error=AuditError)


def require_text(value: Any, name: str) -> str:
    if not isinstance(value, str) or SAFE_TEXT.fullmatch(value) is None:
        raise AuditError(f"{name} must be safe nonempty text")
    return value


def require_timestamp(value: Any, name: str) -> str:
    text = require_text(value, name)
    if not text.endswith("Z"):
        raise AuditError(f"{name} must be a UTC timestamp")
    try:
        parsed = datetime.fromisoformat(text[:-1] + "+00:00")
    except ValueError as error:
        raise AuditError(f"{name} must be a UTC timestamp") from error
    if parsed.utcoffset() is None or parsed.utcoffset().total_seconds() != 0:
        raise AuditError(f"{name} must be a UTC timestamp")
    return text


def require_root(
    path: Path,
    *,
    expected_owner_uid: int | None,
) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise AuditError(f"{path}: backup root is unreadable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise AuditError(f"{path}: backup root is not a trusted directory")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise AuditError(f"{path}: backup root has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise AuditError(f"{path}: backup root must have mode 0700")


require_private_file = functools.partial(common.require_private_file, error=AuditError)


sha256_file = functools.partial(
    common.sha256_file, error=AuditError, message="cannot read artifact"
)


def load_record(
    path: Path,
    *,
    expected_owner_uid: int | None,
) -> dict[str, Any]:
    require_private_file(path, expected_owner_uid=expected_owner_uid)
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise AuditError("transfer record is not valid JSON") from error
    return require_mapping(value, "transfer record")


def validate_evidence(
    value: Any,
    name: str,
    expected_path: str,
    expected_sha256: str,
) -> dict[str, str]:
    evidence = require_mapping(value, name)
    require_exact_keys(evidence, EVIDENCE_KEYS, name)
    if evidence["path"] != expected_path:
        raise AuditError(f"{name}.path does not match the manifest")
    if evidence["sha256"] != expected_sha256:
        raise AuditError(f"{name}.sha256 does not match the manifest")
    version_id = require_text(evidence["version_id"], f"{name}.version_id")
    retained_until = require_timestamp(
        evidence["retained_until"],
        f"{name}.retained_until",
    )
    return {
        "path": expected_path,
        "version_id": version_id,
        "sha256": expected_sha256,
        "retained_until": retained_until,
    }


def validate_record(
    path: Path,
    set_id: str,
    set_directory: Path,
    manifest_path: Path,
    manifest: dict[str, Any],
    *,
    expected_owner_uid: int | None,
) -> None:
    record = load_record(path, expected_owner_uid=expected_owner_uid)
    require_exact_keys(record, RECORD_KEYS, "transfer record")
    if (
        isinstance(record["schema_version"], bool)
        or record["schema_version"] != 1
    ):
        raise AuditError("transfer record schema_version must be 1")
    if record["set_id"] != set_id:
        raise AuditError("transfer record set_id does not match")
    require_timestamp(record["recorded_at"], "transfer record.recorded_at")

    local = require_mapping(record["local"], "transfer record.local")
    require_exact_keys(local, LOCAL_KEYS, "transfer record.local")
    manifest_sha256 = sha256_file(manifest_path)
    if local != {
        "set_directory": str(set_directory),
        "manifest_path": str(manifest_path),
        "manifest_sha256": manifest_sha256,
    }:
        raise AuditError("transfer record local binding does not match")

    remote = require_mapping(record["remote"], "transfer record.remote")
    require_exact_keys(remote, REMOTE_KEYS, "transfer record.remote")
    endpoint = require_text(
        remote["endpoint_url"],
        "transfer record.remote.endpoint_url",
    )
    if HTTPS_ENDPOINT.fullmatch(endpoint) is None:
        raise AuditError("transfer record endpoint is invalid")
    bucket = require_text(remote["bucket"], "transfer record.remote.bucket")
    if BUCKET.fullmatch(bucket) is None or ".." in bucket:
        raise AuditError("transfer record bucket is invalid")
    prefix = require_text(remote["prefix"], "transfer record.remote.prefix")
    if (
        REMOTE_PREFIX.fullmatch(prefix) is None
        or not prefix.endswith("/" + set_id)
        or ".." in prefix.split("/")
    ):
        raise AuditError("transfer record remote prefix is invalid")

    manifest_artifacts = {
        artifact["path"]: artifact["sha256"]
        for artifact in manifest["artifacts"]
    }
    raw_artifacts = remote["artifacts"]
    if not isinstance(raw_artifacts, list) or len(raw_artifacts) != 4:
        raise AuditError("transfer record must contain four artifacts")
    observed_artifacts = {}
    for index, raw_evidence in enumerate(raw_artifacts):
        if not isinstance(raw_evidence, dict):
            raise AuditError("transfer record artifact evidence is invalid")
        artifact_path = raw_evidence.get("path")
        if artifact_path not in manifest_artifacts:
            raise AuditError("transfer record has an unknown artifact path")
        if artifact_path in observed_artifacts:
            raise AuditError("transfer record duplicates an artifact path")
        observed_artifacts[artifact_path] = validate_evidence(
            raw_evidence,
            f"transfer record.remote.artifacts[{index}]",
            artifact_path,
            manifest_artifacts[artifact_path],
        )
    if set(observed_artifacts) != set(manifest_artifacts):
        raise AuditError("transfer record artifact set is incomplete")

    validate_evidence(
        remote["manifest"],
        "transfer record.remote.manifest",
        "manifest.json",
        manifest_sha256,
    )


def validate_local_set(
    set_directory: Path,
    set_id: str,
    *,
    expected_owner_uid: int | None,
) -> tuple[dict[str, Any], Path]:
    require_set_directory(
        set_directory,
        expected_owner_uid=expected_owner_uid,
    )
    manifest_path = set_directory / "manifest.json"
    require_private_file(
        manifest_path,
        expected_owner_uid=expected_owner_uid,
    )
    try:
        manifest = VERIFIER.verify_manifest(manifest_path)
    except VERIFIER.VerificationError as error:
        raise AuditError(str(error)) from error
    if manifest["set_id"] != set_id:
        raise AuditError("manifest set_id does not match its directory")
    for artifact in manifest["artifacts"]:
        require_private_file(
            set_directory / artifact["path"],
            expected_owner_uid=expected_owner_uid,
        )
    return manifest, manifest_path


def require_set_directory(
    set_directory: Path,
    *,
    expected_owner_uid: int | None,
) -> None:
    metadata = set_directory.lstat()
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise AuditError("backup set is not a non-symlink directory")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise AuditError("backup set directory has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise AuditError("backup set directory must have mode 0700")


def audit_backups(
    staging_root: Path,
    completed_root: Path,
    *,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    entries: list[dict[str, str]] = []
    staging_exists = path_lexists(staging_root)
    completed_exists = path_lexists(completed_root)
    if staging_exists:
        require_root(staging_root, expected_owner_uid=expected_owner_uid)
    if completed_exists:
        require_root(completed_root, expected_owner_uid=expected_owner_uid)

    records: dict[str, Path] = {}
    if completed_exists:
        for path in sorted(completed_root.iterdir()):
            set_id = path.name.removesuffix(".json")
            if path.name.endswith(".json") and SET_ID.fullmatch(set_id):
                records[set_id] = path
            else:
                entries.append(
                    {
                        "set_id": path.name,
                        "state": "inconsistent",
                        "detail": "unexpected_completed_entry",
                    }
                )

    observed_sets: set[str] = set()
    if staging_exists:
        for set_directory in sorted(staging_root.iterdir()):
            set_id = set_directory.name
            if SET_ID.fullmatch(set_id) is None:
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "inconsistent",
                        "detail": "unexpected_staging_entry",
                    }
                )
                continue
            observed_sets.add(set_id)
            manifest_path = set_directory / "manifest.json"
            try:
                require_set_directory(
                    set_directory,
                    expected_owner_uid=expected_owner_uid,
                )
            except (AuditError, OSError):
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "inconsistent",
                        "detail": "invalid_local_set",
                    }
                )
                continue
            if not path_lexists(manifest_path):
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "incomplete",
                        "detail": "manifest_absent",
                    }
                )
                continue
            try:
                manifest, manifest_path = validate_local_set(
                    set_directory,
                    set_id,
                    expected_owner_uid=expected_owner_uid,
                )
            except (AuditError, OSError):
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "inconsistent",
                        "detail": "invalid_local_set",
                    }
                )
                continue
            record_path = records.get(set_id)
            if record_path is None:
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "complete_local",
                        "detail": "transfer_record_absent",
                    }
                )
                continue
            try:
                validate_record(
                    record_path,
                    set_id,
                    set_directory,
                    manifest_path,
                    manifest,
                    expected_owner_uid=expected_owner_uid,
                )
            except AuditError:
                entries.append(
                    {
                        "set_id": set_id,
                        "state": "inconsistent",
                        "detail": "invalid_transfer_record",
                    }
                )
                continue
            entries.append(
                {
                    "set_id": set_id,
                    "state": "transferred",
                    "detail": "local_and_remote_evidence_match",
                }
            )

    for set_id in sorted(set(records) - observed_sets):
        entries.append(
            {
                "set_id": set_id,
                "state": "inconsistent",
                "detail": "orphan_transfer_record",
            }
        )

    entries.sort(key=lambda entry: (entry["set_id"], entry["state"]))
    counts = Counter(entry["state"] for entry in entries)
    result = {
        "schema_version": 1,
        "status": "inconsistent" if counts["inconsistent"] else "clean",
        "counts": {
            state: counts[state]
            for state in (
                "incomplete",
                "complete_local",
                "transferred",
                "inconsistent",
            )
        },
        "sets": entries,
    }
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Audit local storage backups and transfer records",
    )
    parser.add_argument(
        "--staging-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-backups/staging"),
    )
    parser.add_argument(
        "--completed-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-backups/completed"),
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        result = audit_backups(
            args.staging_root,
            args.completed_root,
            expected_owner_uid=0,
        )
    except AuditError as error:
        print(f"backup audit failed: {error}", file=sys.stderr)
        return 2
    print(json.dumps(result, sort_keys=True))
    return 1 if result["status"] == "inconsistent" else 0


if __name__ == "__main__":
    raise SystemExit(main())
