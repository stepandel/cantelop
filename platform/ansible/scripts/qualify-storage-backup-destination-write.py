#!/usr/bin/env python3
"""Controlled write qualification for an S3 storage backup destination."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


READ_QUALIFIER = common.load_sibling(
    "qualify_storage_backup_destination",
    "qualify-storage-backup-destination.py",
)

DELETE_DENIED = re.compile(
    r"access[\s_-]*denied|forbidden|not[\s_-]*authorized|"
    r"retention|object[\s_-]*lock|worm",
    re.IGNORECASE,
)


class WriteQualificationError(ValueError):
    """The remote destination failed controlled write qualification."""


def parse_json_stream(value: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    offset = 0
    documents = []
    while offset < len(value):
        while offset < len(value) and value[offset].isspace():
            offset += 1
        if offset == len(value):
            break
        try:
            document, offset = decoder.raw_decode(value, offset)
        except json.JSONDecodeError as error:
            raise WriteQualificationError("mc returned invalid JSON") from error
        if not isinstance(document, dict):
            raise WriteQualificationError("mc returned a non-object JSON value")
        documents.append(document)
    if not documents:
        raise WriteQualificationError("mc returned no JSON evidence")
    return documents


def run_command(
    runner: READ_QUALIFIER.Runner,
    command: list[str],
    environment: dict[str, str],
    *,
    json_output: bool = False,
) -> subprocess.CompletedProcess[str]:
    result = runner(
        command,
        env=environment,
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )
    if result.returncode != 0:
        raise WriteQualificationError(
            f"mc command failed: {' '.join(command[1:3])}"
        )
    if json_output:
        for document in parse_json_stream(result.stdout):
            if document.get("status") != "success":
                raise WriteQualificationError(
                    "mc returned an unsuccessful JSON result"
                )
    return result


def find_version_ids(value: Any) -> set[str]:
    versions: set[str] = set()
    if isinstance(value, dict):
        for key, nested in value.items():
            if key.lower() == "versionid" and isinstance(nested, str) and nested:
                versions.add(nested)
            versions.update(find_version_ids(nested))
    elif isinstance(value, list):
        for nested in value:
            versions.update(find_version_ids(nested))
    return versions


def parse_utc_timestamp(value: Any, name: str) -> datetime:
    if not isinstance(value, str) or not value.endswith("Z"):
        raise WriteQualificationError(f"{name} is not a UTC timestamp")
    try:
        timestamp = datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError as error:
        raise WriteQualificationError(f"{name} is not a UTC timestamp") from error
    if timestamp.utcoffset() != timedelta(0):
        raise WriteQualificationError(f"{name} is not a UTC timestamp")
    return timestamp


def require_retention(
    documents: list[dict[str, Any]],
    *,
    now: datetime,
    minimum_retention_days: int,
) -> str:
    modes = {
        mode
        for document in documents
        if (mode := READ_QUALIFIER.find_retention_mode(document)) is not None
    }
    if modes != {"COMPLIANCE"}:
        raise WriteQualificationError(
            "qualification object retention is not COMPLIANCE mode"
        )
    until_values = {
        nested
        for document in documents
        for key, nested in document.items()
        if key.lower() == "until" and isinstance(nested, str)
    }
    if len(until_values) != 1:
        raise WriteQualificationError(
            "qualification object has no unique retention timestamp"
        )
    until_text = until_values.pop()
    until = parse_utc_timestamp(until_text, "retention until")
    minimum_until = now + timedelta(days=minimum_retention_days, minutes=-5)
    if until < minimum_until:
        raise WriteQualificationError(
            "qualification object retention is shorter than required"
        )
    return until_text


def require_version_delete_denied(
    runner: READ_QUALIFIER.Runner,
    command: list[str],
    environment: dict[str, str],
) -> None:
    result = runner(
        command,
        env=environment,
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    output = f"{result.stdout}\n{result.stderr}"
    if result.returncode == 0:
        raise WriteQualificationError(
            "qualification object version could be deleted"
        )
    if DELETE_DENIED.search(output) is None:
        raise WriteQualificationError(
            "version deletion failed for an unrecognized reason"
        )


def qualify_write(
    destination_path: Path,
    mc_binary: Path,
    *,
    minimum_retention_days: int = 14,
    expected_owner_uid: int | None = None,
    runner: READ_QUALIFIER.Runner = subprocess.run,
    now: datetime | None = None,
    marker_id: str | None = None,
) -> dict[str, Any]:
    if (
        isinstance(minimum_retention_days, bool)
        or not isinstance(minimum_retention_days, int)
        or minimum_retention_days < 1
    ):
        raise WriteQualificationError(
            "minimum retention days must be a positive integer"
        )
    read_evidence = READ_QUALIFIER.qualify_destination(
        destination_path,
        mc_binary,
        expected_owner_uid=expected_owner_uid,
        runner=runner,
    )
    destination = READ_QUALIFIER.VALIDATOR.validate_destination(
        destination_path,
        expected_owner_uid=expected_owner_uid,
    )
    credentials = READ_QUALIFIER.read_credentials(
        Path(destination["credentials_file"]),
        expected_owner_uid,
    )

    observed_now = now or datetime.now(timezone.utc)
    if observed_now.utcoffset() != timedelta(0):
        raise WriteQualificationError("qualification clock must be UTC")
    identifier = marker_id or uuid.uuid4().hex
    if re.fullmatch(r"[0-9a-f]{32}", identifier) is None:
        raise WriteQualificationError(
            "qualification marker ID must be 32 lowercase hexadecimal characters"
        )
    created_at = observed_now.isoformat().replace("+00:00", "Z")
    payload = (
        json.dumps(
            {
                "schema_version": 1,
                "qualification_id": identifier,
                "created_at": created_at,
            },
            sort_keys=True,
            separators=(",", ":"),
        )
        + "\n"
    )
    checksum = hashlib.sha256(payload.encode()).hexdigest()
    object_key = (
        f"{destination['prefix']}/.qualification/"
        f"write-{identifier}.json"
    )
    alias = "firefusebackup"
    object_target = f"{alias}/{destination['bucket']}/{object_key}"

    with tempfile.TemporaryDirectory(
        prefix="fire-fuse-backup-write-"
    ) as work_directory:
        work_path = Path(work_directory)
        marker_path = work_path / "marker.json"
        marker_path.write_text(payload, encoding="utf-8")
        config_directory = work_path / "mc"
        config_directory.mkdir(mode=0o700)
        environment = {
            "HOME": "/root",
            "MC_CONFIG_DIR": str(config_directory),
            f"MC_HOST_{alias}": READ_QUALIFIER.mc_host_value(
                destination["endpoint_url"],
                credentials["access_key_id"],
                credentials["secret_access_key"],
            ),
            "MC_NO_COLOR": "1",
        }

        run_command(
            runner,
            [
                str(mc_binary),
                "cp",
                str(marker_path),
                object_target,
                "--json",
            ],
            environment,
            json_output=True,
        )
        stat_result = run_command(
            runner,
            [
                str(mc_binary),
                "stat",
                "--versions",
                object_target,
                "--json",
            ],
            environment,
            json_output=True,
        )
        version_ids = {
            version
            for document in parse_json_stream(stat_result.stdout)
            for version in find_version_ids(document)
        }
        if len(version_ids) != 1:
            raise WriteQualificationError(
                "qualification object does not have exactly one visible version"
            )
        version_id = version_ids.pop()

        downloaded = run_command(
            runner,
            [
                str(mc_binary),
                "cat",
                "--version-id",
                version_id,
                object_target,
            ],
            environment,
        )
        if downloaded.stdout != payload:
            raise WriteQualificationError(
                "qualification object content does not match its upload"
            )
        retention_result = run_command(
            runner,
            [
                str(mc_binary),
                "retention",
                "info",
                "--version-id",
                version_id,
                object_target,
                "--json",
            ],
            environment,
            json_output=True,
        )
        retained_until = require_retention(
            parse_json_stream(retention_result.stdout),
            now=observed_now,
            minimum_retention_days=minimum_retention_days,
        )
        require_version_delete_denied(
            runner,
            [
                str(mc_binary),
                "rm",
                "--version-id",
                version_id,
                "--force",
                object_target,
                "--json",
            ],
            environment,
        )

    return {
        "status": "qualified-write",
        "endpoint_url": read_evidence["endpoint_url"],
        "bucket": read_evidence["bucket"],
        "object_key": object_key,
        "version_id": version_id,
        "sha256": checksum,
        "created_at": created_at,
        "retained_until": retained_until,
        "version_delete": "denied",
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Controlled write qualification of an S3 backup destination",
    )
    parser.add_argument("destination", type=Path)
    parser.add_argument("--mc", type=Path, default=Path("/usr/local/bin/mc"))
    parser.add_argument("--minimum-retention-days", type=int, default=14)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        evidence = qualify_write(
            args.destination,
            args.mc,
            minimum_retention_days=args.minimum_retention_days,
            expected_owner_uid=0,
        )
    except (
        WriteQualificationError,
        READ_QUALIFIER.QualificationError,
        READ_QUALIFIER.VALIDATOR.DestinationError,
    ) as error:
        print(f"backup write qualification failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(evidence, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
