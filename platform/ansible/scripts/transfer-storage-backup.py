#!/usr/bin/env python3
"""Transfer one verified storage backup set to immutable S3 storage."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


VERIFIER = common.load_sibling(
    "verify_storage_backup",
    "verify-storage-backup.py",
)
READ_QUALIFIER = common.load_sibling(
    "qualify_storage_backup_destination",
    "qualify-storage-backup-destination.py",
)
WRITE_QUALIFIER = common.load_sibling(
    "qualify_storage_backup_destination_write",
    "qualify-storage-backup-destination-write.py",
)

Runner = Callable[..., subprocess.CompletedProcess[str]]
StreamHasher = Callable[[list[str], dict[str, str], int], str]

SET_ID = re.compile(r"backup-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
NOT_FOUND = re.compile(
    r"not[\s_-]*found|does not exist|no such key|nosuchkey|"
    r"specified key does not exist",
    re.IGNORECASE,
)


class TransferError(ValueError):
    """The backup set could not be safely transferred."""


def parse_json_stream(value: str) -> list[dict[str, Any]]:
    try:
        documents = WRITE_QUALIFIER.parse_json_stream(value)
    except WRITE_QUALIFIER.WriteQualificationError as error:
        raise TransferError(str(error)) from error
    for document in documents:
        if document.get("status") != "success":
            raise TransferError("mc returned an unsuccessful JSON result")
    return documents


def run_mc(
    runner: Runner,
    command: list[str],
    environment: dict[str, str],
    timeout_seconds: int,
    *,
    json_output: bool = False,
) -> subprocess.CompletedProcess[str]:
    result = runner(
        command,
        env=environment,
        capture_output=True,
        text=True,
        timeout=timeout_seconds,
        check=False,
    )
    if result.returncode != 0:
        raise TransferError(f"mc command failed: {' '.join(command[1:3])}")
    if json_output:
        parse_json_stream(result.stdout)
    return result


def subprocess_stream_sha256(
    command: list[str],
    environment: dict[str, str],
    timeout_seconds: int,
) -> str:
    with tempfile.TemporaryFile() as remote_errors:
        remote = subprocess.Popen(
            command,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=remote_errors,
        )
        assert remote.stdout is not None
        hasher = subprocess.Popen(
            ["/usr/bin/sha256sum"],
            stdin=remote.stdout,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        remote.stdout.close()
        try:
            hash_output, hash_errors = hasher.communicate(timeout=timeout_seconds)
            remote_returncode = remote.wait(timeout=30)
        except subprocess.TimeoutExpired as error:
            hasher.kill()
            remote.kill()
            hasher.wait()
            remote.wait()
            raise TransferError("remote checksum stream timed out") from error
        if remote_returncode != 0:
            remote_errors.seek(0)
            raise TransferError("mc could not stream the uploaded object")
        if hasher.returncode != 0 or hash_errors:
            raise TransferError("sha256sum could not hash the uploaded object")
        checksum = hash_output.split(maxsplit=1)[0]
        if re.fullmatch(r"[0-9a-f]{64}", checksum) is None:
            raise TransferError("sha256sum returned an invalid checksum")
        return checksum


def require_trusted_file(
    path: Path,
    *,
    expected_owner_uid: int | None,
    expected_mode: int,
) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise TransferError(f"{path}: file is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise TransferError(f"{path}: must be a regular non-symlink file")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise TransferError(f"{path}: has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != expected_mode:
        raise TransferError(f"{path}: has an unexpected mode")


def require_local_set(
    local_root: Path,
    set_id: str,
    *,
    expected_owner_uid: int | None,
) -> tuple[dict[str, Any], Path]:
    if not local_root.is_absolute():
        raise TransferError("local backup root must be absolute")
    if SET_ID.fullmatch(set_id) is None:
        raise TransferError("backup set ID is invalid")
    set_directory = local_root / set_id
    try:
        directory_metadata = set_directory.lstat()
    except OSError as error:
        raise TransferError("backup set directory is not readable") from error
    if (
        stat.S_ISLNK(directory_metadata.st_mode)
        or not stat.S_ISDIR(directory_metadata.st_mode)
    ):
        raise TransferError(
            "backup set directory must be a non-symlink directory"
        )
    if (
        expected_owner_uid is not None
        and directory_metadata.st_uid != expected_owner_uid
    ):
        raise TransferError("backup set directory has an unexpected owner")
    if stat.S_IMODE(directory_metadata.st_mode) != 0o700:
        raise TransferError("backup set directory must have mode 0700")

    manifest_path = set_directory / "manifest.json"
    require_trusted_file(
        manifest_path,
        expected_owner_uid=expected_owner_uid,
        expected_mode=0o600,
    )
    try:
        manifest = VERIFIER.verify_manifest(manifest_path)
    except VERIFIER.VerificationError as error:
        raise TransferError(str(error)) from error
    if manifest["set_id"] != set_id:
        raise TransferError("manifest set ID does not match its directory")
    for artifact in manifest["artifacts"]:
        require_trusted_file(
            set_directory / artifact["path"],
            expected_owner_uid=expected_owner_uid,
            expected_mode=0o600,
        )
    return manifest, manifest_path


def remote_object_exists(
    runner: Runner,
    mc_binary: Path,
    target: str,
    environment: dict[str, str],
    timeout_seconds: int,
) -> bool:
    result = runner(
        [str(mc_binary), "stat", target, "--json"],
        env=environment,
        capture_output=True,
        text=True,
        timeout=timeout_seconds,
        check=False,
    )
    if result.returncode == 0:
        parse_json_stream(result.stdout)
        return True
    if NOT_FOUND.search(f"{result.stdout}\n{result.stderr}") is None:
        raise TransferError("remote manifest check failed unexpectedly")
    return False


def current_version_id(
    runner: Runner,
    mc_binary: Path,
    target: str,
    environment: dict[str, str],
    timeout_seconds: int,
) -> str:
    result = run_mc(
        runner,
        [str(mc_binary), "stat", target, "--json"],
        environment,
        timeout_seconds,
        json_output=True,
    )
    versions = {
        version
        for document in parse_json_stream(result.stdout)
        for version in WRITE_QUALIFIER.find_version_ids(document)
    }
    if len(versions) != 1:
        raise TransferError("uploaded object has no unique current version")
    return versions.pop()


def verify_remote_object(
    runner: Runner,
    stream_hasher: StreamHasher,
    mc_binary: Path,
    target: str,
    expected_sha256: str,
    environment: dict[str, str],
    timeout_seconds: int,
    minimum_retention_days: int,
) -> dict[str, str]:
    version_id = current_version_id(
        runner,
        mc_binary,
        target,
        environment,
        timeout_seconds,
    )
    actual_sha256 = stream_hasher(
        [
            str(mc_binary),
            "cat",
            "--version-id",
            version_id,
            target,
        ],
        environment,
        timeout_seconds,
    )
    if actual_sha256 != expected_sha256:
        raise TransferError("uploaded object checksum does not match local data")
    retention = run_mc(
        runner,
        [
            str(mc_binary),
            "retention",
            "info",
            "--version-id",
            version_id,
            target,
            "--json",
        ],
        environment,
        timeout_seconds,
        json_output=True,
    )
    try:
        retained_until = WRITE_QUALIFIER.require_retention(
            parse_json_stream(retention.stdout),
            now=datetime.now(timezone.utc),
            minimum_retention_days=minimum_retention_days,
        )
    except WRITE_QUALIFIER.WriteQualificationError as error:
        raise TransferError(str(error)) from error
    return {
        "version_id": version_id,
        "sha256": actual_sha256,
        "retained_until": retained_until,
    }


def upload_object(
    runner: Runner,
    stream_hasher: StreamHasher,
    mc_binary: Path,
    source: Path,
    target: str,
    expected_sha256: str,
    environment: dict[str, str],
    timeout_seconds: int,
    minimum_retention_days: int,
) -> dict[str, str]:
    run_mc(
        runner,
        [str(mc_binary), "cp", str(source), target, "--json"],
        environment,
        timeout_seconds,
        json_output=True,
    )
    return verify_remote_object(
        runner,
        stream_hasher,
        mc_binary,
        target,
        expected_sha256,
        environment,
        timeout_seconds,
        minimum_retention_days,
    )


def transfer_backup(
    set_id: str,
    destination_path: Path,
    mc_binary: Path,
    local_root: Path,
    *,
    minimum_retention_days: int = 14,
    timeout_seconds: int = 86400,
    expected_owner_uid: int | None = None,
    runner: Runner = subprocess.run,
    stream_hasher: StreamHasher = subprocess_stream_sha256,
) -> dict[str, Any]:
    if (
        isinstance(minimum_retention_days, bool)
        or not isinstance(minimum_retention_days, int)
        or minimum_retention_days < 14
        or minimum_retention_days > 3650
    ):
        raise TransferError("minimum retention days must be from 14 to 3650")
    if (
        isinstance(timeout_seconds, bool)
        or not isinstance(timeout_seconds, int)
        or timeout_seconds < 60
        or timeout_seconds > 172800
    ):
        raise TransferError("transfer timeout must be from 60 to 172800 seconds")

    manifest, manifest_path = require_local_set(
        local_root,
        set_id,
        expected_owner_uid=expected_owner_uid,
    )
    try:
        manifest_sha256 = VERIFIER.sha256_file(manifest_path)
    except VERIFIER.VerificationError as error:
        raise TransferError(str(error)) from error
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

    alias = "firefusebackup"
    remote_root = (
        f"{alias}/{destination['bucket']}/{destination['prefix']}/{set_id}"
    )
    with tempfile.TemporaryDirectory(
        prefix="fire-fuse-backup-transfer-"
    ) as config_directory:
        os.chmod(config_directory, 0o700)
        environment = {
            "HOME": "/root",
            "MC_CONFIG_DIR": config_directory,
            f"MC_HOST_{alias}": READ_QUALIFIER.mc_host_value(
                destination["endpoint_url"],
                credentials["access_key_id"],
                credentials["secret_access_key"],
            ),
            "MC_NO_COLOR": "1",
        }
        remote_manifest = f"{remote_root}/manifest.json"
        manifest_exists = remote_object_exists(
            runner,
            mc_binary,
            remote_manifest,
            environment,
            timeout_seconds,
        )

        uploaded = []
        if manifest_exists:
            manifest_evidence = verify_remote_object(
                runner,
                stream_hasher,
                mc_binary,
                remote_manifest,
                manifest_sha256,
                environment,
                timeout_seconds,
                minimum_retention_days,
            )
            for artifact in manifest["artifacts"]:
                target = f"{remote_root}/{artifact['path']}"
                evidence = verify_remote_object(
                    runner,
                    stream_hasher,
                    mc_binary,
                    target,
                    artifact["sha256"],
                    environment,
                    timeout_seconds,
                    minimum_retention_days,
                )
                uploaded.append(
                    {
                        "path": artifact["path"],
                        **evidence,
                    }
                )
            status = "already-transferred-and-verified"
        else:
            for artifact in manifest["artifacts"]:
                target = f"{remote_root}/{artifact['path']}"
                evidence = upload_object(
                    runner,
                    stream_hasher,
                    mc_binary,
                    manifest_path.parent / artifact["path"],
                    target,
                    artifact["sha256"],
                    environment,
                    timeout_seconds,
                    minimum_retention_days,
                )
                uploaded.append(
                    {
                        "path": artifact["path"],
                        **evidence,
                    }
                )

            try:
                current_manifest_sha256 = VERIFIER.sha256_file(manifest_path)
            except VERIFIER.VerificationError as error:
                raise TransferError(str(error)) from error
            if current_manifest_sha256 != manifest_sha256:
                raise TransferError("local manifest changed during transfer")
            manifest_evidence = upload_object(
                runner,
                stream_hasher,
                mc_binary,
                manifest_path,
                remote_manifest,
                manifest_sha256,
                environment,
                timeout_seconds,
                minimum_retention_days,
            )
            status = "transferred-and-verified"

        try:
            final_manifest_sha256 = VERIFIER.sha256_file(manifest_path)
        except VERIFIER.VerificationError as error:
            raise TransferError(str(error)) from error
        if final_manifest_sha256 != manifest_sha256:
            raise TransferError("local manifest changed during transfer")

    return {
        "status": status,
        "set_id": set_id,
        "endpoint_url": read_evidence["endpoint_url"],
        "bucket": read_evidence["bucket"],
        "remote_prefix": f"{destination['prefix']}/{set_id}",
        "artifacts": uploaded,
        "manifest": {
            "path": "manifest.json",
            **manifest_evidence,
        },
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Transfer one verified storage backup to immutable S3",
    )
    parser.add_argument("set_id")
    parser.add_argument(
        "--destination",
        type=Path,
        default=Path(
            "/etc/fire-fuse-storage/backup-destination.json"
        ),
    )
    parser.add_argument(
        "--mc",
        type=Path,
        default=Path("/usr/local/bin/mc"),
    )
    parser.add_argument(
        "--local-root",
        type=Path,
        default=Path(
            "/var/lib/fire-fuse/storage-backups/staging"
        ),
    )
    parser.add_argument("--minimum-retention-days", type=int, default=14)
    parser.add_argument("--timeout-seconds", type=int, default=86400)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        evidence = transfer_backup(
            args.set_id,
            args.destination,
            args.mc,
            args.local_root,
            minimum_retention_days=args.minimum_retention_days,
            timeout_seconds=args.timeout_seconds,
            expected_owner_uid=0,
        )
    except (
        TransferError,
        READ_QUALIFIER.QualificationError,
        READ_QUALIFIER.VALIDATOR.DestinationError,
    ) as error:
        print(f"backup transfer failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(evidence, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
