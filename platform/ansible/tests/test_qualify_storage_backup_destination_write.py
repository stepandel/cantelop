from __future__ import annotations

from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from support import load_script


QUALIFIER = load_script("qualify-storage-backup-destination-write.py")


class FakeRunner:
    version_id = "version-00000000-0000-0000-0000-000000000001"

    def __init__(
        self,
        *,
        now: datetime,
        retention_days: int = 30,
        downloaded_payload: str | None = None,
        delete_returncode: int = 1,
        delete_error: str = "Object is WORM protected by retention",
    ) -> None:
        self.now = now
        self.retention_days = retention_days
        self.downloaded_payload = downloaded_payload
        self.delete_returncode = delete_returncode
        self.delete_error = delete_error
        self.uploaded_payload = ""

    def __call__(
        self,
        command: list[str],
        **kwargs: object,
    ) -> subprocess.CompletedProcess[str]:
        operation = command[1]
        if operation == "ls":
            return subprocess.CompletedProcess(
                command,
                1,
                stdout="",
                stderr="AccessDenied",
            )
        if operation == "version":
            document = {
                "status": "success",
                "versioning": {
                    "status": "Enabled",
                    "MFADelete": "",
                    "ExcludedPrefixes": [],
                },
            }
        elif operation == "retention" and "--version-id" not in command:
            document = {
                "status": "success",
                "retention": {"mode": "COMPLIANCE", "validity": "30d"},
            }
        elif operation == "cp":
            self.uploaded_payload = Path(command[2]).read_text(encoding="utf-8")
            document = {"status": "success", "target": command[3]}
        elif operation == "stat" and "--versions" in command:
            document = {
                "status": "success",
                "versionID": self.version_id,
            }
        elif operation == "cat":
            return subprocess.CompletedProcess(
                command,
                0,
                stdout=(
                    self.uploaded_payload
                    if self.downloaded_payload is None
                    else self.downloaded_payload
                ),
                stderr="",
            )
        elif operation == "retention":
            until = self.now + timedelta(days=self.retention_days)
            document = {
                "status": "success",
                "mode": "COMPLIANCE",
                "until": until.isoformat().replace("+00:00", "Z"),
                "versionID": self.version_id,
            }
        elif operation == "rm":
            return subprocess.CompletedProcess(
                command,
                self.delete_returncode,
                stdout="",
                stderr=self.delete_error,
            )
        else:
            document = {"status": "success", "type": "folder"}
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=json.dumps(document),
            stderr="",
        )


class StorageBackupDestinationWriteQualificationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.mc_binary = self.root / "mc"
        self.mc_binary.write_text("test executable", encoding="utf-8")
        self.mc_binary.chmod(0o755)
        self.credentials_path = self.root / "credentials.json"
        self.credentials_path.write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "access_key_id": "backup-access",
                    "secret_access_key": "backup-secret",
                }
            ),
            encoding="utf-8",
        )
        self.credentials_path.chmod(0o600)
        self.destination_path = self.root / "destination.json"
        self.destination_path.write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "driver": "s3",
                    "endpoint_url": "https://s3-backup.example.com",
                    "region": "example-1",
                    "bucket": "fire-fuse-storage-backups",
                    "prefix": "shared-volume/backup-sets",
                    "credentials_file": str(self.credentials_path),
                    "immutability_mode": "object_lock",
                }
            ),
            encoding="utf-8",
        )
        self.now = datetime(2026, 7, 28, 12, 0, tzinfo=timezone.utc)
        self.marker_id = "0123456789abcdef0123456789abcdef"

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def qualify(self, runner: FakeRunner) -> dict[str, object]:
        return QUALIFIER.qualify_write(
            self.destination_path,
            self.mc_binary,
            minimum_retention_days=14,
            expected_owner_uid=os.getuid(),
            runner=runner,
            now=self.now,
            marker_id=self.marker_id,
        )

    def test_qualifies_retained_write(self) -> None:
        result = self.qualify(FakeRunner(now=self.now))

        self.assertEqual(result["status"], "qualified-write")
        self.assertEqual(result["version_id"], FakeRunner.version_id)
        self.assertEqual(result["version_delete"], "denied")
        self.assertIn(self.marker_id, result["object_key"])

    def test_rejects_modified_download(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.WriteQualificationError,
            "content does not match",
        ):
            self.qualify(
                FakeRunner(now=self.now, downloaded_payload="modified\n")
            )

    def test_rejects_short_retention(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.WriteQualificationError,
            "shorter than required",
        ):
            self.qualify(FakeRunner(now=self.now, retention_days=2))

    def test_rejects_deletable_version(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.WriteQualificationError,
            "could be deleted",
        ):
            self.qualify(
                FakeRunner(now=self.now, delete_returncode=0)
            )

    def test_rejects_unrecognized_delete_failure(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.WriteQualificationError,
            "unrecognized reason",
        ):
            self.qualify(
                FakeRunner(now=self.now, delete_error="connection reset")
            )

    def test_rejects_invalid_retention_days(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.WriteQualificationError,
            "positive integer",
        ):
            QUALIFIER.qualify_write(
                self.destination_path,
                self.mc_binary,
                minimum_retention_days=0,
                expected_owner_uid=os.getuid(),
                runner=FakeRunner(now=self.now),
                now=self.now,
                marker_id=self.marker_id,
            )


if __name__ == "__main__":
    unittest.main()
