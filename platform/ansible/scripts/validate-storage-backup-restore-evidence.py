#!/usr/bin/env python3
"""Validate machine-generated evidence from an isolated backup restore."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta
import functools
import json
from pathlib import Path
import re
import sys
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


AUDITOR = common.load_sibling("audit_storage_backups", "audit-storage-backups.py")

DOCUMENT_KEYS = {
    "schema_version",
    "set_id",
    "started_at",
    "completed_at",
    "source",
    "target",
    "checks",
}
SOURCE_KEYS = {
    "manifest_sha256",
    "transfer_record_sha256",
    "volume_uuid",
    "remote_prefix",
}
TARGET_KEYS = {"rehearsal_id", "isolated", "restored_volume_uuid"}
CHECK_KEYS = {
    "manifest_verified",
    "object_snapshot_restored",
    "redis_snapshot_restored",
    "metadata_dump_loaded",
    "juicefs_status_verified",
    "first_mount_marker_sha256",
    "clean_unmount_verified",
    "remount_marker_sha256",
    "production_guard_before_sha256",
    "production_guard_after_sha256",
}
REHEARSAL_ID = re.compile(
    r"restore-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}"
)


class EvidenceError(ValueError):
    """Restore evidence is missing, malformed, or does not match its backup."""


require_exact_keys = functools.partial(common.require_exact_keys, error=EvidenceError)


require_mapping = functools.partial(common.require_mapping, error=EvidenceError)


def parse_timestamp(value: Any, name: str) -> datetime:
    try:
        text = AUDITOR.require_timestamp(value, name)
    except AUDITOR.AuditError as error:
        raise EvidenceError(str(error)) from error
    return datetime.fromisoformat(text[:-1] + "+00:00")


sha256_file = functools.partial(
    common.sha256_file, error=EvidenceError, message="cannot checksum file"
)


def load_evidence(
    path: Path,
    *,
    expected_owner_uid: int | None,
) -> dict[str, Any]:
    try:
        AUDITOR.require_private_file(
            path,
            expected_owner_uid=expected_owner_uid,
        )
    except AUDITOR.AuditError as error:
        raise EvidenceError(
            "restore evidence file is not a trusted private file"
        ) from error
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise EvidenceError("restore evidence is not valid JSON") from error
    return require_mapping(value, "restore evidence")


def validate_restore_evidence(
    set_id: str,
    restore_root: Path,
    staging_root: Path,
    completed_root: Path,
    *,
    expected_owner_uid: int | None = None,
) -> dict[str, str]:
    if AUDITOR.SET_ID.fullmatch(set_id) is None:
        raise EvidenceError("restore evidence set ID is invalid")
    try:
        AUDITOR.require_root(
            restore_root,
            expected_owner_uid=expected_owner_uid,
        )
    except AUDITOR.AuditError as error:
        raise EvidenceError(str(error)) from error

    evidence_path = restore_root / f"{set_id}.json"
    evidence = load_evidence(
        evidence_path,
        expected_owner_uid=expected_owner_uid,
    )
    require_exact_keys(evidence, DOCUMENT_KEYS, "restore evidence")
    if (
        isinstance(evidence["schema_version"], bool)
        or evidence["schema_version"] != 1
    ):
        raise EvidenceError("restore evidence schema_version must be 1")
    if evidence["set_id"] != set_id:
        raise EvidenceError("restore evidence set_id does not match")
    started_at = parse_timestamp(
        evidence["started_at"],
        "restore evidence.started_at",
    )
    completed_at = parse_timestamp(
        evidence["completed_at"],
        "restore evidence.completed_at",
    )
    duration = completed_at - started_at
    if duration < timedelta(seconds=1) or duration > timedelta(days=7):
        raise EvidenceError("restore rehearsal duration is invalid")

    set_directory = staging_root / set_id
    try:
        manifest, manifest_path = AUDITOR.validate_local_set(
            set_directory,
            set_id,
            expected_owner_uid=expected_owner_uid,
        )
    except (AUDITOR.AuditError, OSError) as error:
        raise EvidenceError("restore evidence local set is invalid") from error
    transfer_record_path = completed_root / f"{set_id}.json"
    try:
        AUDITOR.validate_record(
            transfer_record_path,
            set_id,
            set_directory,
            manifest_path,
            manifest,
            expected_owner_uid=expected_owner_uid,
        )
        transfer_record = AUDITOR.load_record(
            transfer_record_path,
            expected_owner_uid=expected_owner_uid,
        )
    except AUDITOR.AuditError as error:
        raise EvidenceError(
            "restore evidence transfer record is invalid"
        ) from error

    manifest_sha256 = sha256_file(manifest_path)
    transfer_record_sha256 = sha256_file(transfer_record_path)
    source = require_mapping(evidence["source"], "restore evidence.source")
    require_exact_keys(source, SOURCE_KEYS, "restore evidence.source")
    expected_source = {
        "manifest_sha256": manifest_sha256,
        "transfer_record_sha256": transfer_record_sha256,
        "volume_uuid": manifest["source"]["volume_uuid"],
        "remote_prefix": transfer_record["remote"]["prefix"],
    }
    if source != expected_source:
        raise EvidenceError("restore evidence source binding does not match")

    target = require_mapping(evidence["target"], "restore evidence.target")
    require_exact_keys(target, TARGET_KEYS, "restore evidence.target")
    rehearsal_id = target["rehearsal_id"]
    if (
        not isinstance(rehearsal_id, str)
        or REHEARSAL_ID.fullmatch(rehearsal_id) is None
    ):
        raise EvidenceError("restore evidence rehearsal_id is invalid")
    if target["isolated"] is not True:
        raise EvidenceError("restore rehearsal target was not isolated")
    if target["restored_volume_uuid"] != manifest["source"]["volume_uuid"]:
        raise EvidenceError("restored volume UUID does not match the backup")

    checks = require_mapping(evidence["checks"], "restore evidence.checks")
    require_exact_keys(checks, CHECK_KEYS, "restore evidence.checks")
    for check in (
        "manifest_verified",
        "object_snapshot_restored",
        "redis_snapshot_restored",
        "metadata_dump_loaded",
        "juicefs_status_verified",
        "clean_unmount_verified",
    ):
        if checks[check] is not True:
            raise EvidenceError(f"restore evidence check {check} did not pass")
    for check in (
        "first_mount_marker_sha256",
        "remount_marker_sha256",
        "production_guard_before_sha256",
        "production_guard_after_sha256",
    ):
        if (
            not isinstance(checks[check], str)
            or AUDITOR.SHA256.fullmatch(checks[check]) is None
        ):
            raise EvidenceError(f"restore evidence check {check} is invalid")
    if (
        checks["first_mount_marker_sha256"]
        != checks["remount_marker_sha256"]
    ):
        raise EvidenceError("restore marker changed after remount")
    if (
        checks["production_guard_before_sha256"]
        != checks["production_guard_after_sha256"]
    ):
        raise EvidenceError("production guard changed during restore rehearsal")

    return {
        "status": "restore-evidence-verified",
        "set_id": set_id,
        "rehearsal_id": rehearsal_id,
        "completed_at": evidence["completed_at"],
        "manifest_sha256": manifest_sha256,
        "transfer_record_sha256": transfer_record_sha256,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate isolated storage-backup restore evidence",
    )
    parser.add_argument("set_id")
    parser.add_argument(
        "--restore-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-backups/restores"),
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
        result = validate_restore_evidence(
            args.set_id,
            args.restore_root,
            args.staging_root,
            args.completed_root,
            expected_owner_uid=0,
        )
    except EvidenceError as error:
        print(f"restore evidence validation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
