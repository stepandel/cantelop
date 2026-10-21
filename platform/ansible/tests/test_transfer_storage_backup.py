from __future__ import annotations

from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

from support import load_script


TRANSFER = load_script("transfer-storage-backup.py")


class FakeRemote:
    def __init__(self) -> None:
        self.objects: dict[str, str] = {}
        self.uploads: list[str] = []
        self.manifest_exists = False
        self.corrupt_path: str | None = None
        self.retained_until = (
            datetime.now(timezone.utc) + timedelta(days=30)
        ).isoformat().replace("+00:00", "Z")

    @staticmethod
    def result(
        returncode: int = 0,
        stdout: str = "",
        stderr: str = "",
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args=[],
            returncode=returncode,
            stdout=stdout,
            stderr=stderr,
        )

    def runner(self, command: list[str], **_: object) -> subprocess.CompletedProcess[str]:
        action = command[1]
        if action == "stat":
            target = command[2]
            if target.endswith("/manifest.json") and self.manifest_exists:
                return self.result(
                    stdout=json.dumps(
                        {"status": "success", "versionID": "existing"}
                    )
                )
            if target not in self.objects:
                return self.result(
                    returncode=1,
                    stderr="The specified key does not exist (NoSuchKey)",
                )
            return self.result(
                stdout=json.dumps(
                    {
                        "status": "success",
                        "versionID": f"version-{len(self.uploads)}",
                    }
                )
            )
        if action == "cp":
            source = Path(command[2])
            target = command[3]
            self.objects[target] = hashlib.sha256(source.read_bytes()).hexdigest()
            self.uploads.append(target)
            if target.endswith("/manifest.json"):
                self.manifest_exists = True
            return self.result(stdout=json.dumps({"status": "success"}))
        if action == "retention":
            return self.result(
                stdout=json.dumps(
                    {
                        "status": "success",
                        "mode": "COMPLIANCE",
                        "until": self.retained_until,
                    }
                )
            )
        raise AssertionError(f"unexpected command: {command}")

    def stream_hasher(
        self,
        command: list[str],
        _: dict[str, str],
        __: int,
    ) -> str:
        target = command[-1]
        if target == self.corrupt_path:
            return "0" * 64
        return self.objects[target]


class StorageBackupTransferTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.local_root = self.root / "backups"
        self.local_root.mkdir(mode=0o700)
        self.set_id = "backup-20260728T120000Z-0123abcd"
        self.set_directory = self.local_root / self.set_id
        self.set_directory.mkdir(mode=0o700)
        self.destination = self.root / "destination.json"
        self.destination.write_text("{}\n", encoding="utf-8")
        self.mc_binary = self.root / "mc"
        self.mc_binary.write_text("fake\n", encoding="utf-8")
        self.mc_binary.chmod(0o755)

        artifact_specs = (
            ("juicefs_metadata", "metadata.dump"),
            ("minio_objects", "minio-objects.tar"),
            ("redis_persistence", "redis-persistence.tar.gz"),
            ("encrypted_restore_configuration", "restore-secrets.vault"),
        )
        artifacts = []
        for component, filename in artifact_specs:
            path = self.set_directory / filename
            payload = f"payload for {component}\n".encode()
            path.write_bytes(payload)
            path.chmod(0o600)
            artifacts.append(
                {
                    "component": component,
                    "path": filename,
                    "size_bytes": len(payload),
                    "sha256": hashlib.sha256(payload).hexdigest(),
                }
            )
        self.manifest = {
            "schema_version": 1,
            "set_id": self.set_id,
            "created_at": "2026-07-28T12:00:00Z",
            "source": {
                "volume_name": "fire-fuse-workspaces",
                "volume_uuid": "01234567-89ab-cdef-0123-456789abcdef",
                "juicefs_version": "1.4.0",
                "redis_version": "7.2.5",
                "minio_version": "RELEASE.2026-01-01T00-00-00Z",
                "object_snapshot_id": "sha256:" + "a" * 64,
            },
            "artifacts": artifacts,
        }
        self.manifest_path = self.set_directory / "manifest.json"
        self.manifest_path.write_text(
            json.dumps(self.manifest),
            encoding="utf-8",
        )
        self.manifest_path.chmod(0o600)
        self.remote = FakeRemote()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def transfer(self) -> dict[str, object]:
        destination = {
            "endpoint_url": "https://backup.example.com",
            "bucket": "fire-fuse-backups",
            "prefix": "shared-volume/backup-sets",
            "credentials_file": str(self.root / "credentials.json"),
        }
        qualification = {
            "endpoint_url": destination["endpoint_url"],
            "bucket": destination["bucket"],
        }
        with ExitStack() as patches:
            patches.enter_context(
                mock.patch.object(
                    TRANSFER.READ_QUALIFIER,
                    "qualify_destination",
                    return_value=qualification,
                )
            )
            patches.enter_context(
                mock.patch.object(
                    TRANSFER.READ_QUALIFIER.VALIDATOR,
                    "validate_destination",
                    return_value=destination,
                )
            )
            patches.enter_context(
                mock.patch.object(
                    TRANSFER.READ_QUALIFIER,
                    "read_credentials",
                    return_value={
                        "access_key_id": "backup-writer",
                        "secret_access_key": "secret-value",
                    },
                )
            )
            return TRANSFER.transfer_backup(
                self.set_id,
                self.destination,
                self.mc_binary,
                self.local_root,
                expected_owner_uid=os.getuid(),
                runner=self.remote.runner,
                stream_hasher=self.remote.stream_hasher,
            )

    def test_uploads_manifest_last_and_verifies_every_object(self) -> None:
        evidence = self.transfer()

        self.assertEqual(evidence["status"], "transferred-and-verified")
        self.assertEqual(len(self.remote.uploads), 5)
        self.assertTrue(self.remote.uploads[-1].endswith("/manifest.json"))
        self.assertEqual(len(evidence["artifacts"]), 4)
        self.assertEqual(evidence["manifest"]["path"], "manifest.json")

    def populate_remote_set(self) -> None:
        remote_root = (
            "firefusebackup/fire-fuse-backups/"
            f"shared-volume/backup-sets/{self.set_id}"
        )
        for artifact in self.manifest["artifacts"]:
            self.remote.objects[f"{remote_root}/{artifact['path']}"] = artifact[
                "sha256"
            ]
        self.remote.objects[f"{remote_root}/manifest.json"] = hashlib.sha256(
            self.manifest_path.read_bytes()
        ).hexdigest()
        self.remote.manifest_exists = True

    def test_reconciles_an_existing_remote_manifest_without_writes(self) -> None:
        self.populate_remote_set()

        evidence = self.transfer()

        self.assertEqual(
            evidence["status"],
            "already-transferred-and-verified",
        )
        self.assertEqual(self.remote.uploads, [])

    def test_rejects_a_mismatched_existing_remote_set(self) -> None:
        self.populate_remote_set()
        remote_root = (
            "firefusebackup/fire-fuse-backups/"
            f"shared-volume/backup-sets/{self.set_id}"
        )
        self.remote.objects[f"{remote_root}/manifest.json"] = "0" * 64

        with self.assertRaisesRegex(TRANSFER.TransferError, "checksum"):
            self.transfer()

        self.assertEqual(self.remote.uploads, [])

    def test_does_not_publish_manifest_after_checksum_failure(self) -> None:
        remote_root = (
            "firefusebackup/fire-fuse-backups/"
            f"shared-volume/backup-sets/{self.set_id}"
        )
        self.remote.corrupt_path = f"{remote_root}/metadata.dump"

        with self.assertRaisesRegex(
            TRANSFER.TransferError,
            "checksum",
        ):
            self.transfer()

        self.assertFalse(self.remote.manifest_exists)
        self.assertNotIn(f"{remote_root}/manifest.json", self.remote.uploads)

    def test_rejects_a_world_readable_local_artifact(self) -> None:
        (self.set_directory / "metadata.dump").chmod(0o644)

        with self.assertRaisesRegex(
            TRANSFER.TransferError,
            "unexpected mode",
        ):
            self.transfer()

    def test_rejects_a_short_retention_policy(self) -> None:
        self.remote.retained_until = (
            datetime.now(timezone.utc) + timedelta(days=2)
        ).isoformat().replace("+00:00", "Z")

        with self.assertRaisesRegex(
            TRANSFER.TransferError,
            "shorter than required",
        ):
            self.transfer()


if __name__ == "__main__":
    unittest.main()
