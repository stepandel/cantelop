#!/usr/bin/env python3
"""Materialize exact off-host backup versions for an isolated restore rehearsal."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import stat
import subprocess
import sys
import tempfile
import time
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)


AUDITOR = common.load_sibling("audit_storage_backups", "audit-storage-backups.py")
VERIFIER = common.load_sibling("verify_storage_backup", "verify-storage-backup.py")
QUALIFIER = common.load_sibling(
    "qualify_storage_backup_destination",
    "qualify-storage-backup-destination.py",
)

Downloader = Callable[[list[str], dict[str, str], Path, int, int], tuple[str, int]]
SET_ID = re.compile(r"backup-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}")
REHEARSAL_ID = re.compile(
    r"restore-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}"
)


class MaterializationError(ValueError):
    """The remote restore inputs could not be materialized safely."""


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def new_rehearsal_id() -> str:
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return f"restore-{timestamp}-{secrets.token_hex(4)}"


def require_private_directory(
    path: Path,
    *,
    expected_owner_uid: int | None,
) -> None:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise MaterializationError(f"{path}: directory is not readable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        raise MaterializationError(f"{path}: must be a non-symlink directory")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise MaterializationError(f"{path}: has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise MaterializationError(f"{path}: must have mode 0700")


def subprocess_download(
    command: list[str],
    environment: dict[str, str],
    destination: Path,
    timeout_seconds: int,
    expected_size: int,
) -> tuple[str, int]:
    partial = destination.with_name(destination.name + ".partial")
    digest = hashlib.sha256()
    size = 0
    deadline = time.monotonic() + timeout_seconds
    try:
        with partial.open("xb") as output, tempfile.TemporaryFile() as errors:
            os.chmod(partial, 0o600)
            process = subprocess.Popen(
                command,
                env=environment,
                stdout=subprocess.PIPE,
                stderr=errors,
            )
            assert process.stdout is not None
            selector = selectors.DefaultSelector()
            selector.register(process.stdout, selectors.EVENT_READ)
            try:
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise subprocess.TimeoutExpired(command, timeout_seconds)
                    if not selector.select(timeout=remaining):
                        raise subprocess.TimeoutExpired(command, timeout_seconds)
                    chunk = os.read(process.stdout.fileno(), 1024 * 1024)
                    if not chunk:
                        break
                    if size + len(chunk) > expected_size:
                        raise MaterializationError("remote object exceeds the recorded size")
                    output.write(chunk)
                    digest.update(chunk)
                    size += len(chunk)
                output.flush()
                os.fsync(output.fileno())
                returncode = process.wait(timeout=max(1, int(deadline - time.monotonic())))
            except (OSError, subprocess.TimeoutExpired, MaterializationError) as error:
                process.kill()
                process.wait()
                if isinstance(error, MaterializationError):
                    raise
                raise MaterializationError("remote object download failed or timed out") from error
            finally:
                selector.close()
                process.stdout.close()
            if returncode != 0:
                raise MaterializationError("mc could not download the recorded object version")
        partial.replace(destination)
        return digest.hexdigest(), size
    finally:
        partial.unlink(missing_ok=True)


def download_exact(
    downloader: Downloader,
    mc_binary: Path,
    target: str,
    version_id: str,
    destination: Path,
    environment: dict[str, str],
    timeout_seconds: int,
    expected_sha256: str,
    expected_size: int,
) -> None:
    checksum, size = downloader(
        [str(mc_binary), "cat", target, "--version-id", version_id],
        environment,
        destination,
        timeout_seconds,
        expected_size,
    )
    if checksum != expected_sha256:
        raise MaterializationError(
            f"{destination.name}: downloaded checksum does not match transfer evidence"
        )
    if size != expected_size:
        raise MaterializationError(
            f"{destination.name}: downloaded size does not match the manifest"
        )
    try:
        metadata = destination.lstat()
    except OSError as error:
        raise MaterializationError(
            f"{destination.name}: downloader did not publish a file"
        ) from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise MaterializationError(
            f"{destination.name}: downloader published an unsafe file"
        )
    os.chmod(destination, 0o600)


def materialize_restore(
    set_id: str,
    destination_path: Path,
    mc_binary: Path,
    staging_root: Path,
    completed_root: Path,
    restore_root: Path,
    *,
    rehearsal_id: str | None = None,
    timeout_seconds: int = 86400,
    expected_owner_uid: int | None = None,
    runner: Callable[..., subprocess.CompletedProcess[str]] = subprocess.run,
    downloader: Downloader = subprocess_download,
) -> dict[str, Any]:
    if SET_ID.fullmatch(set_id) is None:
        raise MaterializationError("backup set ID is invalid")
    if timeout_seconds < 60 or timeout_seconds > 172800:
        raise MaterializationError("timeout must be between 60 and 172800 seconds")
    for root in (staging_root, completed_root, restore_root):
        if not root.is_absolute():
            raise MaterializationError("all storage roots must be absolute")

    try:
        audit = AUDITOR.audit_backups(
            staging_root,
            completed_root,
            expected_owner_uid=expected_owner_uid,
        )
    except AUDITOR.AuditError as error:
        raise MaterializationError(str(error)) from error
    matching = [item for item in audit["sets"] if item["set_id"] == set_id]
    if audit["status"] != "clean" or matching != [{
        "set_id": set_id,
        "state": "transferred",
        "detail": "local_and_remote_evidence_match",
    }]:
        raise MaterializationError(
            "local audit must be clean and the requested set must be transferred"
        )

    local_set = staging_root / set_id
    manifest, local_manifest_path = AUDITOR.validate_local_set(
        local_set,
        set_id,
        expected_owner_uid=expected_owner_uid,
    )
    record_path = completed_root / f"{set_id}.json"
    AUDITOR.validate_record(
        record_path,
        set_id,
        local_set,
        local_manifest_path,
        manifest,
        expected_owner_uid=expected_owner_uid,
    )
    record = AUDITOR.load_record(
        record_path,
        expected_owner_uid=expected_owner_uid,
    )

    try:
        qualification = QUALIFIER.qualify_destination(
            destination_path,
            mc_binary,
            expected_owner_uid=expected_owner_uid,
            runner=runner,
        )
        destination = QUALIFIER.VALIDATOR.validate_destination(
            destination_path,
            expected_owner_uid=expected_owner_uid,
        )
        credentials = QUALIFIER.read_credentials(
            Path(destination["credentials_file"]),
            expected_owner_uid,
        )
    except (
        QUALIFIER.QualificationError,
        QUALIFIER.VALIDATOR.DestinationError,
    ) as error:
        raise MaterializationError(str(error)) from error

    remote = record["remote"]
    configured = (
        qualification["endpoint_url"],
        qualification["bucket"],
        destination["prefix"],
    )
    recorded = (remote["endpoint_url"], remote["bucket"], remote["prefix"].rsplit("/", 1)[0])
    if configured != recorded:
        raise MaterializationError(
            "configured destination does not match the recorded transfer destination"
        )

    require_private_directory(
        restore_root,
        expected_owner_uid=expected_owner_uid,
    )
    selected_rehearsal_id = rehearsal_id or new_rehearsal_id()
    if REHEARSAL_ID.fullmatch(selected_rehearsal_id) is None:
        raise MaterializationError("restore rehearsal ID is invalid")
    workspace = restore_root / selected_rehearsal_id
    try:
        workspace.mkdir(mode=0o700)
    except FileExistsError as error:
        raise MaterializationError("restore rehearsal directory already exists") from error
    except OSError as error:
        raise MaterializationError("restore rehearsal directory cannot be created") from error

    alias = "firefusebackup"
    remote_base = f"{alias}/{remote['bucket']}/{remote['prefix']}"
    environment = {
        "HOME": "/root",
        "MC_CONFIG_DIR": str(workspace / ".mc"),
        f"MC_HOST_{alias}": QUALIFIER.mc_host_value(
            destination["endpoint_url"],
            credentials["access_key_id"],
            credentials["secret_access_key"],
        ),
        "MC_NO_COLOR": "1",
    }
    (workspace / ".mc").mkdir(mode=0o700)

    manifest_evidence = remote["manifest"]
    materialized_manifest = workspace / "manifest.json"
    download_exact(
        downloader,
        mc_binary,
        f"{remote_base}/manifest.json",
        manifest_evidence["version_id"],
        materialized_manifest,
        environment,
        timeout_seconds,
        manifest_evidence["sha256"],
        local_manifest_path.stat().st_size,
    )

    remote_artifacts = {
        item["path"]: item for item in remote["artifacts"]
    }
    for artifact in manifest["artifacts"]:
        evidence = remote_artifacts[artifact["path"]]
        download_exact(
            downloader,
            mc_binary,
            f"{remote_base}/{artifact['path']}",
            evidence["version_id"],
            workspace / artifact["path"],
            environment,
            timeout_seconds,
            evidence["sha256"],
            artifact["size_bytes"],
        )

    try:
        verified = VERIFIER.verify_manifest(materialized_manifest)
    except VERIFIER.VerificationError as error:
        raise MaterializationError(str(error)) from error
    if verified["set_id"] != set_id:
        raise MaterializationError("materialized manifest identifies another set")

    record_checksum = AUDITOR.sha256_file(record_path)
    result = {
        "schema_version": 1,
        "status": "materialized-and-verified",
        "rehearsal_id": selected_rehearsal_id,
        "set_id": set_id,
        "materialized_at": utc_now(),
        "workspace": str(workspace),
        "source": {
            "endpoint_url": remote["endpoint_url"],
            "bucket": remote["bucket"],
            "remote_prefix": remote["prefix"],
            "transfer_record_sha256": record_checksum,
            "manifest_version_id": manifest_evidence["version_id"],
            "manifest_sha256": manifest_evidence["sha256"],
        },
        "artifacts": [
            {
                "path": artifact["path"],
                "version_id": remote_artifacts[artifact["path"]]["version_id"],
                "size_bytes": artifact["size_bytes"],
                "sha256": artifact["sha256"],
            }
            for artifact in manifest["artifacts"]
        ],
    }
    record_output = workspace / "materialization.json"
    temporary_output = workspace / ".materialization.json.partial"
    temporary_output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary_output, 0o600)
    with temporary_output.open("rb") as output:
        os.fsync(output.fileno())
    temporary_output.replace(record_output)
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Materialize exact off-host versions for a restore rehearsal",
    )
    parser.add_argument("set_id")
    parser.add_argument(
        "--destination",
        type=Path,
        default=Path("/etc/fire-fuse-storage/backup-destination.json"),
    )
    parser.add_argument("--mc", type=Path, default=Path("/usr/local/bin/mc"))
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
        default=Path("/var/lib/fire-fuse/storage-restores/staging"),
    )
    parser.add_argument("--timeout-seconds", type=int, default=86400)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        result = materialize_restore(
            args.set_id,
            args.destination,
            args.mc,
            args.staging_root,
            args.completed_root,
            args.restore_root,
            timeout_seconds=args.timeout_seconds,
            expected_owner_uid=0,
        )
    except (MaterializationError, AUDITOR.AuditError, OSError) as error:
        print(f"restore materialization failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
