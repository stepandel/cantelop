#!/usr/bin/env python3
"""Produce a non-mutating retention plan for verified storage backups."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import sys
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


AUDITOR = common.load_sibling("audit_storage_backups", "audit-storage-backups.py")

EVIDENCE_VALIDATOR = common.load_sibling(
    "validate_storage_backup_restore_evidence",
    "validate-storage-backup-restore-evidence.py",
)

BucketKey = Callable[[datetime], object]


class PlanningError(ValueError):
    """A safe retention plan cannot be produced."""


def parse_timestamp(value: Any, name: str) -> datetime:
    try:
        text = AUDITOR.require_timestamp(value, name)
    except AUDITOR.AuditError as error:
        raise PlanningError(str(error)) from error
    return datetime.fromisoformat(text[:-1] + "+00:00")


def load_json(path: Path, name: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise PlanningError(f"{name} is unreadable") from error
    if not isinstance(value, dict):
        raise PlanningError(f"{name} is not an object")
    return value


def choose_recovery_points(
    backups: list[dict[str, Any]],
    limit: int,
    bucket_key: BucketKey,
) -> set[str]:
    selected: set[str] = set()
    observed_buckets: set[object] = set()
    for backup in backups:
        bucket = bucket_key(backup["created"])
        if bucket in observed_buckets:
            continue
        if len(observed_buckets) >= limit:
            break
        observed_buckets.add(bucket)
        selected.add(backup["set_id"])
    return selected


def validate_policy(daily: int, weekly: int, monthly: int) -> None:
    for value, name, maximum in (
        (daily, "daily", 366),
        (weekly, "weekly", 104),
        (monthly, "monthly", 120),
    ):
        if (
            isinstance(value, bool)
            or not isinstance(value, int)
            or value < 1
            or value > maximum
        ):
            raise PlanningError(
                f"{name} recovery-point count must be from 1 to {maximum}"
            )


def plan_retention(
    staging_root: Path,
    completed_root: Path,
    restore_root: Path,
    restore_qualified_set_id: str,
    *,
    daily: int = 14,
    weekly: int = 8,
    monthly: int = 12,
    expected_owner_uid: int | None = None,
    now: datetime | None = None,
) -> dict[str, Any]:
    validate_policy(daily, weekly, monthly)
    if AUDITOR.SET_ID.fullmatch(restore_qualified_set_id) is None:
        raise PlanningError("restore-qualified set ID is invalid")
    observed_now = now or datetime.now(timezone.utc)
    if (
        observed_now.tzinfo is None
        or observed_now.utcoffset() is None
        or observed_now.utcoffset().total_seconds() != 0
    ):
        raise PlanningError("planning clock must be UTC")

    try:
        restore_evidence = EVIDENCE_VALIDATOR.validate_restore_evidence(
            restore_qualified_set_id,
            restore_root,
            staging_root,
            completed_root,
            expected_owner_uid=expected_owner_uid,
        )
    except EVIDENCE_VALIDATOR.EvidenceError as error:
        raise PlanningError(str(error)) from error

    try:
        audit = AUDITOR.audit_backups(
            staging_root,
            completed_root,
            expected_owner_uid=expected_owner_uid,
        )
    except AUDITOR.AuditError as error:
        raise PlanningError(str(error)) from error
    if audit["status"] != "clean" or audit["counts"]["inconsistent"] != 0:
        raise PlanningError(
            "backup inventory is inconsistent; retention planning is unsafe"
        )

    transferred = []
    non_transferred = []
    for entry in audit["sets"]:
        set_id = entry["set_id"]
        set_directory = staging_root / set_id
        manifest_path = set_directory / "manifest.json"
        if entry["state"] == "incomplete":
            non_transferred.append(
                {
                    "set_id": set_id,
                    "state": "incomplete",
                    "created_at": None,
                }
            )
            continue
        try:
            manifest = AUDITOR.VERIFIER.verify_manifest(manifest_path)
        except AUDITOR.VERIFIER.VerificationError as error:
            raise PlanningError(str(error)) from error
        created_at = manifest["created_at"]
        created = parse_timestamp(created_at, "manifest.created_at")
        if entry["state"] == "complete_local":
            non_transferred.append(
                {
                    "set_id": set_id,
                    "state": "complete_local",
                    "created_at": created_at,
                }
            )
            continue
        if entry["state"] != "transferred":
            raise PlanningError("audit returned an unknown backup state")

        record_path = completed_root / f"{set_id}.json"
        record = load_json(record_path, "transfer record")
        remote = record["remote"]
        evidence = [*remote["artifacts"], remote["manifest"]]
        retention_deadlines = [
            parse_timestamp(
                item["retained_until"],
                "transfer evidence retained_until",
            )
            for item in evidence
        ]
        transferred.append(
            {
                "set_id": set_id,
                "state": "transferred",
                "created_at": created_at,
                "created": created,
                "remote_retained_until": max(retention_deadlines),
            }
        )

    transferred.sort(
        key=lambda backup: (backup["created"], backup["set_id"]),
        reverse=True,
    )
    transferred_ids = {backup["set_id"] for backup in transferred}
    if restore_qualified_set_id not in transferred_ids:
        raise PlanningError(
            "restore-qualified set must be present and transferred"
        )

    daily_points = choose_recovery_points(
        transferred,
        daily,
        lambda timestamp: timestamp.date(),
    )
    weekly_points = choose_recovery_points(
        transferred,
        weekly,
        lambda timestamp: timestamp.isocalendar()[:2],
    )
    monthly_points = choose_recovery_points(
        transferred,
        monthly,
        lambda timestamp: (timestamp.year, timestamp.month),
    )
    newest_set_id = transferred[0]["set_id"]

    decisions = []
    for backup in transferred:
        reasons = []
        set_id = backup["set_id"]
        if set_id == newest_set_id:
            reasons.append("final_known_good")
        if set_id == restore_qualified_set_id:
            reasons.append("restore_qualified")
        if set_id in daily_points:
            reasons.append("daily_recovery_point")
        if set_id in weekly_points:
            reasons.append("weekly_recovery_point")
        if set_id in monthly_points:
            reasons.append("monthly_recovery_point")
        if backup["remote_retained_until"] > observed_now:
            reasons.append("remote_retention_active")
        action = "keep" if reasons else "candidate_expire_complete_set"
        decisions.append(
            {
                "set_id": set_id,
                "state": "transferred",
                "created_at": backup["created_at"],
                "remote_retained_until": (
                    backup["remote_retained_until"]
                    .isoformat()
                    .replace("+00:00", "Z")
                ),
                "action": action,
                "reasons": reasons or ["outside_retention_policy"],
            }
        )

    for backup in non_transferred:
        decisions.append(
            {
                "set_id": backup["set_id"],
                "state": backup["state"],
                "created_at": backup["created_at"],
                "remote_retained_until": None,
                "action": "keep",
                "reasons": [
                    "capture_incomplete"
                    if backup["state"] == "incomplete"
                    else "off_host_transfer_absent"
                ],
            }
        )

    decisions.sort(key=lambda decision: decision["set_id"])
    candidates = sum(
        decision["action"] == "candidate_expire_complete_set"
        for decision in decisions
    )
    return {
        "schema_version": 1,
        "status": "planned",
        "generated_at": observed_now.isoformat().replace("+00:00", "Z"),
        "policy": {
            "daily": daily,
            "weekly": weekly,
            "monthly": monthly,
            "preserve_final_known_good": True,
            "restore_qualified_set_id": restore_qualified_set_id,
            "restore_rehearsal_id": restore_evidence["rehearsal_id"],
            "restore_evidence_completed_at": restore_evidence["completed_at"],
        },
        "counts": {
            "sets": len(decisions),
            "keep": len(decisions) - candidates,
            "candidates": candidates,
        },
        "decisions": decisions,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Plan storage-backup retention without deleting data",
    )
    parser.add_argument("--restore-qualified-set-id", required=True)
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
    parser.add_argument(
        "--restore-root",
        type=Path,
        default=Path("/var/lib/fire-fuse/storage-backups/restores"),
    )
    parser.add_argument("--daily", type=int, default=14)
    parser.add_argument("--weekly", type=int, default=8)
    parser.add_argument("--monthly", type=int, default=12)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        plan = plan_retention(
            args.staging_root,
            args.completed_root,
            args.restore_root,
            args.restore_qualified_set_id,
            daily=args.daily,
            weekly=args.weekly,
            monthly=args.monthly,
            expected_owner_uid=0,
        )
    except PlanningError as error:
        print(f"retention planning failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(plan, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
