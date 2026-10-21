from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from support import load_script


QUALIFIER = load_script("qualify-storage-backup-destination.py")


class FakeRunner:
    def __init__(
        self,
        *,
        version_status: str = "Enabled",
        excluded_prefixes: list[str] | None = None,
        retention_mode: str = "COMPLIANCE",
        list_returncode: int = 1,
        list_error: str = "AccessDenied",
    ) -> None:
        self.version_status = version_status
        self.excluded_prefixes = excluded_prefixes or []
        self.retention_mode = retention_mode
        self.list_returncode = list_returncode
        self.list_error = list_error
        self.environments: list[dict[str, str]] = []

    def __call__(
        self,
        command: list[str],
        **kwargs: object,
    ) -> subprocess.CompletedProcess[str]:
        self.environments.append(kwargs["env"])
        operation = command[1:-1]
        if operation[0] == "ls":
            return subprocess.CompletedProcess(
                command,
                self.list_returncode,
                stdout="",
                stderr=self.list_error,
            )
        if operation[0] == "version":
            document = {
                "status": "success",
                "versioning": {
                    "status": self.version_status,
                    "MFADelete": "",
                    "ExcludedPrefixes": self.excluded_prefixes,
                },
            }
        elif operation[0] == "retention":
            document = {
                "status": "success",
                "retention": {"mode": self.retention_mode, "validity": "30d"},
            }
        else:
            document = {"status": "success", "type": "folder"}
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=json.dumps(document),
            stderr="",
        )


class StorageBackupDestinationQualificationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.mc_binary = self.root / "mc"
        self.mc_binary.write_text("test executable", encoding="utf-8")
        self.mc_binary.chmod(0o755)
        self.credentials = {
            "schema_version": 1,
            "access_key_id": "access/key",
            "secret_access_key": "secret:key",
        }
        self.credentials_path = self.root / "credentials.json"
        self.credentials_path.write_text(
            json.dumps(self.credentials),
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

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def qualify(self, runner: FakeRunner) -> dict[str, object]:
        return QUALIFIER.qualify_destination(
            self.destination_path,
            self.mc_binary,
            expected_owner_uid=os.getuid(),
            runner=runner,
        )

    def test_qualifies_read_only_destination(self) -> None:
        runner = FakeRunner()

        result = self.qualify(runner)

        self.assertEqual(result["status"], "qualified-read-only")
        self.assertEqual(result["account_bucket_listing"], "denied")
        host_value = runner.environments[0]["MC_HOST_firefusebackup"]
        self.assertIn("access%2Fkey:secret%3Akey@", host_value)

    def test_rejects_disabled_versioning(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "versioning is not enabled",
        ):
            self.qualify(FakeRunner(version_status="Suspended"))

    def test_rejects_versioning_exclusions(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "excludes one or more prefixes",
        ):
            self.qualify(FakeRunner(excluded_prefixes=["unprotected/"]))

    def test_rejects_governance_retention(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "not COMPLIANCE",
        ):
            self.qualify(FakeRunner(retention_mode="GOVERNANCE"))

    def test_rejects_account_wide_bucket_listing(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "enumerate account-wide",
        ):
            self.qualify(FakeRunner(list_returncode=0))

    def test_rejects_unrecognized_listing_failure(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "unrecognized reason",
        ):
            self.qualify(FakeRunner(list_error="connection reset"))

    def test_rejects_relative_mc_binary(self) -> None:
        with self.assertRaisesRegex(
            QUALIFIER.QualificationError,
            "must be absolute",
        ):
            QUALIFIER.qualify_destination(
                self.destination_path,
                Path("mc"),
                expected_owner_uid=os.getuid(),
                runner=FakeRunner(),
            )


if __name__ == "__main__":
    unittest.main()
