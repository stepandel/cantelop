#!/usr/bin/env python3
"""Verify that a JuiceFS storage backup set is complete and internally sound."""

from __future__ import annotations

import argparse
from datetime import datetime
import functools
import json
from pathlib import Path, PurePosixPath
import re
import stat
import sys
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


REQUIRED_COMPONENTS = {
    "juicefs_metadata",
    "minio_objects",
    "redis_persistence",
    "encrypted_restore_configuration",
}
DOCUMENT_KEYS = {"schema_version", "set_id", "created_at", "source", "artifacts"}
SOURCE_KEYS = {
    "volume_name",
    "volume_uuid",
    "juicefs_version",
    "redis_version",
    "minio_version",
    "object_snapshot_id",
}
ARTIFACT_KEYS = {"component", "path", "size_bytes", "sha256"}
SAFE_IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
LOWERCASE_SHA256 = re.compile(r"[0-9a-f]{64}")
CANONICAL_UUID = re.compile(
    r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-"
    r"[0-9a-f]{4}-[0-9a-f]{12}"
)


class VerificationError(ValueError):
    """The backup set cannot be trusted."""


require_exact_keys = functools.partial(
    common.require_exact_keys, error=VerificationError, detailed=True
)


require_mapping = functools.partial(common.require_mapping, error=VerificationError)


def require_string(value: Any, name: str) -> str:
    if (
        not isinstance(value, str)
        or not value
        or any(ord(character) < 32 for character in value)
    ):
        raise VerificationError(
            f"{name} must be a nonempty string without control characters"
        )
    return value


def require_integer(value: Any, name: str, *, minimum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise VerificationError(f"{name} must be an integer >= {minimum}")
    return value


def require_utc_timestamp(value: Any, name: str) -> str:
    timestamp = require_string(value, name)
    if not timestamp.endswith("Z"):
        raise VerificationError(f"{name} must be an RFC 3339 UTC timestamp")
    try:
        parsed = datetime.fromisoformat(timestamp[:-1] + "+00:00")
    except ValueError as error:
        raise VerificationError(
            f"{name} must be an RFC 3339 UTC timestamp"
        ) from error
    if parsed.utcoffset() is None or parsed.utcoffset().total_seconds() != 0:
        raise VerificationError(f"{name} must be an RFC 3339 UTC timestamp")
    return timestamp


def artifact_path(root: Path, value: Any, name: str) -> tuple[str, Path]:
    relative = require_string(value, name)
    posix_path = PurePosixPath(relative)
    if (
        posix_path.is_absolute()
        or relative != posix_path.as_posix()
        or relative in {".", ""}
        or any(part in {"", ".", ".."} for part in posix_path.parts)
    ):
        raise VerificationError(f"{name} must be a clean relative POSIX path")
    candidate = root.joinpath(*posix_path.parts)
    try:
        metadata = candidate.lstat()
    except OSError as error:
        raise VerificationError(f"{name} does not identify a readable file") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise VerificationError(f"{name} must identify a regular non-symlink file")
    return relative, candidate


sha256_file = functools.partial(
    common.sha256_file, error=VerificationError, message="cannot read artifact"
)


def load_manifest(path: Path) -> dict[str, Any]:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise VerificationError(f"{path}: manifest is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise VerificationError(f"{path}: manifest must be a regular non-symlink file")
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise VerificationError(f"{path}: cannot decode manifest JSON") from error
    return require_mapping(document, "manifest")


def verify_manifest(path: Path) -> dict[str, Any]:
    manifest = load_manifest(path)
    require_exact_keys(manifest, DOCUMENT_KEYS, "manifest")

    schema_version = require_integer(
        manifest["schema_version"],
        "manifest.schema_version",
        minimum=1,
    )
    if schema_version != 1:
        raise VerificationError("manifest.schema_version must be 1")

    set_id = require_string(manifest["set_id"], "manifest.set_id")
    if SAFE_IDENTIFIER.fullmatch(set_id) is None:
        raise VerificationError("manifest.set_id is not a safe identifier")
    created_at = require_utc_timestamp(manifest["created_at"], "manifest.created_at")

    source = require_mapping(manifest["source"], "manifest.source")
    require_exact_keys(source, SOURCE_KEYS, "manifest.source")
    validated_source = {
        key: require_string(source[key], f"manifest.source.{key}")
        for key in sorted(SOURCE_KEYS)
    }
    if CANONICAL_UUID.fullmatch(validated_source["volume_uuid"]) is None:
        raise VerificationError(
            "manifest.source.volume_uuid must be a lowercase canonical UUID"
        )

    artifacts = manifest["artifacts"]
    if not isinstance(artifacts, list):
        raise VerificationError("manifest.artifacts must be an array")
    observed_components: set[str] = set()
    observed_paths: set[str] = set()
    verified_artifacts = []
    for index, raw_artifact in enumerate(artifacts):
        name = f"manifest.artifacts[{index}]"
        artifact = require_mapping(raw_artifact, name)
        require_exact_keys(artifact, ARTIFACT_KEYS, name)

        component = require_string(artifact["component"], f"{name}.component")
        if component not in REQUIRED_COMPONENTS:
            raise VerificationError(f"{name}.component is not recognized")
        if component in observed_components:
            raise VerificationError(f"{name}.component is duplicated")

        relative, file_path = artifact_path(
            path.parent,
            artifact["path"],
            f"{name}.path",
        )
        if relative in observed_paths:
            raise VerificationError(f"{name}.path is duplicated")
        expected_size = require_integer(
            artifact["size_bytes"],
            f"{name}.size_bytes",
            minimum=1,
        )
        actual_size = file_path.stat().st_size
        if actual_size != expected_size:
            raise VerificationError(
                f"{name}.size_bytes is {expected_size}, actual size is {actual_size}"
            )
        expected_checksum = require_string(
            artifact["sha256"],
            f"{name}.sha256",
        )
        if LOWERCASE_SHA256.fullmatch(expected_checksum) is None:
            raise VerificationError(f"{name}.sha256 must be lowercase SHA-256")
        actual_checksum = sha256_file(file_path)
        if actual_checksum != expected_checksum:
            raise VerificationError(f"{name}.sha256 does not match the artifact")

        observed_components.add(component)
        observed_paths.add(relative)
        verified_artifacts.append(
            {
                "component": component,
                "path": relative,
                "size_bytes": actual_size,
                "sha256": actual_checksum,
            }
        )

    if observed_components != REQUIRED_COMPONENTS:
        missing = ", ".join(sorted(REQUIRED_COMPONENTS - observed_components))
        raise VerificationError(
            f"manifest.artifacts is incomplete (missing {missing})"
        )

    return {
        "schema_version": schema_version,
        "set_id": set_id,
        "created_at": created_at,
        "source": validated_source,
        "artifacts": sorted(
            verified_artifacts,
            key=lambda artifact: artifact["component"],
        ),
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Verify a complete JuiceFS storage backup set",
    )
    parser.add_argument("manifest", type=Path, help="path to manifest.json")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        verified = verify_manifest(args.manifest)
    except VerificationError as error:
        print(f"backup verification failed: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "status": "verified",
                "set_id": verified["set_id"],
                "created_at": verified["created_at"],
                "artifacts": len(verified["artifacts"]),
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
