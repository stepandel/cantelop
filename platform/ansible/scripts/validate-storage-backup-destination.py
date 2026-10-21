#!/usr/bin/env python3
"""Validate the non-secret S3 backup destination and root-only credential."""

from __future__ import annotations

import argparse
import functools
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
from typing import Any
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


CONFIG_KEYS = {
    "schema_version",
    "driver",
    "endpoint_url",
    "region",
    "bucket",
    "prefix",
    "credentials_file",
    "immutability_mode",
}
CREDENTIAL_KEYS = {"schema_version", "access_key_id", "secret_access_key"}
BUCKET_NAME = re.compile(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]")


class DestinationError(ValueError):
    """The destination configuration does not meet the backup contract."""


require_mapping = functools.partial(common.require_mapping, error=DestinationError)


require_exact_keys = functools.partial(
    common.require_exact_keys, error=DestinationError, detailed=True
)


def require_string(value: Any, name: str) -> str:
    if (
        not isinstance(value, str)
        or not value
        or any(ord(character) < 32 for character in value)
    ):
        raise DestinationError(
            f"{name} must be a nonempty string without control characters"
        )
    return value


def require_schema_version(value: Any, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value != 1:
        raise DestinationError(f"{name} must be 1")
    return value


def load_json_file(
    path: Path,
    name: str,
    *,
    expected_owner_uid: int | None = None,
    private: bool = False,
) -> dict[str, Any]:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise DestinationError(f"{name} is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise DestinationError(f"{name} must be a regular non-symlink file")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise DestinationError(f"{name} has the wrong owner")
    if private and stat.S_IMODE(metadata.st_mode) & 0o077:
        raise DestinationError(f"{name} must not grant group or other access")
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise DestinationError(f"{name} is not valid UTF-8 JSON") from error
    return require_mapping(document, name)


def validate_endpoint(value: Any) -> str:
    endpoint = require_string(value, "destination.endpoint_url")
    try:
        parsed = urlsplit(endpoint)
        hostname = parsed.hostname
        parsed.port
    except ValueError as error:
        raise DestinationError(
            "destination.endpoint_url must be a valid URL"
        ) from error
    if (
        parsed.scheme != "https"
        or not hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in {"", "/"}
    ):
        raise DestinationError(
            "destination.endpoint_url must be an HTTPS origin without "
            "credentials, path, query, or fragment"
        )
    return endpoint.rstrip("/")


def validate_bucket(value: Any) -> str:
    bucket = require_string(value, "destination.bucket")
    if (
        BUCKET_NAME.fullmatch(bucket) is None
        or ".." in bucket
        or ".-" in bucket
        or "-." in bucket
    ):
        raise DestinationError("destination.bucket is not a valid DNS-style name")
    try:
        ipaddress.ip_address(bucket)
    except ValueError:
        return bucket
    raise DestinationError("destination.bucket must not be an IP address")


def validate_prefix(value: Any) -> str:
    prefix = require_string(value, "destination.prefix")
    parsed = PurePosixPath(prefix)
    if (
        parsed.is_absolute()
        or prefix != parsed.as_posix()
        or prefix in {".", ""}
        or any(part in {"", ".", ".."} for part in parsed.parts)
    ):
        raise DestinationError(
            "destination.prefix must be a clean relative POSIX path"
        )
    return prefix


def validate_credentials(
    path: Path,
    *,
    expected_owner_uid: int | None,
) -> None:
    credentials = load_json_file(
        path,
        "credentials",
        expected_owner_uid=expected_owner_uid,
        private=True,
    )
    require_exact_keys(credentials, CREDENTIAL_KEYS, "credentials")
    require_schema_version(credentials["schema_version"], "credentials.schema_version")
    for key in ("access_key_id", "secret_access_key"):
        require_string(credentials[key], f"credentials.{key}")


def validate_destination(
    path: Path,
    *,
    expected_owner_uid: int | None = None,
) -> dict[str, Any]:
    destination = load_json_file(
        path,
        "destination",
        expected_owner_uid=expected_owner_uid,
    )
    require_exact_keys(destination, CONFIG_KEYS, "destination")
    require_schema_version(
        destination["schema_version"],
        "destination.schema_version",
    )
    if destination["driver"] != "s3":
        raise DestinationError("destination.driver must be s3")
    if destination["immutability_mode"] != "object_lock":
        raise DestinationError(
            "destination.immutability_mode must be object_lock"
        )

    credentials_file = Path(
        require_string(
            destination["credentials_file"],
            "destination.credentials_file",
        )
    )
    if not credentials_file.is_absolute():
        raise DestinationError("destination.credentials_file must be absolute")
    validate_credentials(
        credentials_file,
        expected_owner_uid=expected_owner_uid,
    )

    return {
        "schema_version": 1,
        "driver": "s3",
        "endpoint_url": validate_endpoint(destination["endpoint_url"]),
        "region": require_string(destination["region"], "destination.region"),
        "bucket": validate_bucket(destination["bucket"]),
        "prefix": validate_prefix(destination["prefix"]),
        "credentials_file": str(credentials_file),
        "immutability_mode": "object_lock",
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate a fire-fuse storage backup destination",
    )
    parser.add_argument("destination", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        destination = validate_destination(
            args.destination,
            expected_owner_uid=0,
        )
    except DestinationError as error:
        print(f"backup destination validation failed: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "status": "validated",
                "driver": destination["driver"],
                "endpoint_url": destination["endpoint_url"],
                "bucket": destination["bucket"],
                "prefix": destination["prefix"],
                "immutability_mode": destination["immutability_mode"],
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
