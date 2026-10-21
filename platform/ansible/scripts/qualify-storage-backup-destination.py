#!/usr/bin/env python3
"""Read-only qualification for an S3 storage backup destination."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any, Callable
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


VALIDATOR = common.load_sibling(
    "validate_storage_backup_destination",
    "validate-storage-backup-destination.py",
)

Runner = Callable[..., subprocess.CompletedProcess[str]]
ACCESS_DENIED = re.compile(
    r"access[\s_-]*denied|forbidden|not[\s_-]*authorized",
    re.IGNORECASE,
)


class QualificationError(ValueError):
    """The remote destination has not satisfied the backup contract."""


def read_credentials(path: Path, expected_owner_uid: int | None) -> dict[str, str]:
    credentials = VALIDATOR.load_json_file(
        path,
        "credentials",
        expected_owner_uid=expected_owner_uid,
        private=True,
    )
    VALIDATOR.require_exact_keys(
        credentials,
        VALIDATOR.CREDENTIAL_KEYS,
        "credentials",
    )
    VALIDATOR.require_schema_version(
        credentials["schema_version"],
        "credentials.schema_version",
    )
    return {
        key: VALIDATOR.require_string(credentials[key], f"credentials.{key}")
        for key in ("access_key_id", "secret_access_key")
    }


def mc_host_value(
    endpoint_url: str,
    access_key_id: str,
    secret_access_key: str,
) -> str:
    authority = endpoint_url.removeprefix("https://")
    return (
        "https://"
        f"{quote(access_key_id, safe='')}:{quote(secret_access_key, safe='')}"
        f"@{authority}"
    )


def run_mc_json(
    runner: Runner,
    command: list[str],
    environment: dict[str, str],
) -> dict[str, Any]:
    result = runner(
        command,
        env=environment,
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    if result.returncode != 0:
        raise QualificationError(
            f"mc command failed: {' '.join(command[1:-1])}"
        )
    try:
        document = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise QualificationError("mc returned invalid JSON") from error
    if not isinstance(document, dict) or document.get("status") != "success":
        raise QualificationError("mc returned an unsuccessful result")
    return document


def require_versioning(document: dict[str, Any]) -> None:
    versioning = document.get("versioning")
    if not isinstance(versioning, dict):
        raise QualificationError("mc version response has no versioning object")
    if versioning.get("status") != "Enabled":
        raise QualificationError("backup bucket versioning is not enabled")
    excluded_prefixes = versioning.get("ExcludedPrefixes", [])
    if excluded_prefixes != []:
        raise QualificationError(
            "backup bucket versioning excludes one or more prefixes"
        )
    if versioning.get("ExcludeFolders", False) is not False:
        raise QualificationError(
            "backup bucket versioning excludes folder objects"
        )


def find_retention_mode(value: Any) -> str | None:
    if isinstance(value, dict):
        for key, nested in value.items():
            if key.lower() == "mode" and isinstance(nested, str):
                return nested.upper()
            found = find_retention_mode(nested)
            if found is not None:
                return found
    elif isinstance(value, list):
        for nested in value:
            found = find_retention_mode(nested)
            if found is not None:
                return found
    return None


def require_compliance_retention(document: dict[str, Any]) -> None:
    if find_retention_mode(document) != "COMPLIANCE":
        raise QualificationError(
            "backup bucket default retention is not COMPLIANCE mode"
        )


def require_account_listing_denied(
    runner: Runner,
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
        raise QualificationError(
            "backup credential can enumerate account-wide buckets"
        )
    if ACCESS_DENIED.search(output) is None:
        raise QualificationError(
            "account-wide bucket enumeration failed for an unrecognized reason"
        )


def qualify_destination(
    destination_path: Path,
    mc_binary: Path,
    *,
    expected_owner_uid: int | None = None,
    runner: Runner = subprocess.run,
) -> dict[str, Any]:
    destination = VALIDATOR.validate_destination(
        destination_path,
        expected_owner_uid=expected_owner_uid,
    )
    credentials = read_credentials(
        Path(destination["credentials_file"]),
        expected_owner_uid,
    )
    if not mc_binary.is_absolute():
        raise QualificationError("mc binary path must be absolute")
    if (
        not mc_binary.is_file()
        or mc_binary.is_symlink()
        or not os.access(mc_binary, os.X_OK)
    ):
        raise QualificationError(
            "mc binary must be an executable regular non-symlink file"
        )

    alias = "firefusebackup"
    bucket_target = f"{alias}/{destination['bucket']}"
    with tempfile.TemporaryDirectory(prefix="fire-fuse-backup-mc-") as config_dir:
        environment = {
            "HOME": "/root",
            "MC_CONFIG_DIR": config_dir,
            f"MC_HOST_{alias}": mc_host_value(
                destination["endpoint_url"],
                credentials["access_key_id"],
                credentials["secret_access_key"],
            ),
            "MC_NO_COLOR": "1",
        }
        run_mc_json(
            runner,
            [str(mc_binary), "stat", bucket_target, "--json"],
            environment,
        )
        versioning = run_mc_json(
            runner,
            [str(mc_binary), "version", "info", bucket_target, "--json"],
            environment,
        )
        require_versioning(versioning)
        retention = run_mc_json(
            runner,
            [
                str(mc_binary),
                "retention",
                "info",
                "--default",
                bucket_target,
                "--json",
            ],
            environment,
        )
        require_compliance_retention(retention)
        require_account_listing_denied(
            runner,
            [str(mc_binary), "ls", alias, "--json"],
            environment,
        )

    return {
        "status": "qualified-read-only",
        "endpoint_url": destination["endpoint_url"],
        "bucket": destination["bucket"],
        "prefix": destination["prefix"],
        "versioning": "Enabled",
        "default_retention_mode": "COMPLIANCE",
        "account_bucket_listing": "denied",
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Read-only qualification of an S3 backup destination",
    )
    parser.add_argument("destination", type=Path)
    parser.add_argument("--mc", type=Path, default=Path("/usr/local/bin/mc"))
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        result = qualify_destination(
            args.destination,
            args.mc,
            expected_owner_uid=0,
        )
    except (QualificationError, VALIDATOR.DestinationError) as error:
        print(f"backup destination qualification failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
