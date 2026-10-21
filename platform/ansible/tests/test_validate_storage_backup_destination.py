from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import tempfile
import unittest

from support import load_script


VALIDATOR = load_script("validate-storage-backup-destination.py")


class StorageBackupDestinationValidationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.credentials_path = self.root / "credentials.json"
        self.credentials_path.write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "access_key_id": "backup-only-access-key",
                    "secret_access_key": "backup-only-secret-key",
                }
            ),
            encoding="utf-8",
        )
        self.credentials_path.chmod(0o600)
        self.destination = {
            "schema_version": 1,
            "driver": "s3",
            "endpoint_url": "https://s3-backup.example.com",
            "region": "example-1",
            "bucket": "fire-fuse-storage-backups",
            "prefix": "shared-volume/backup-sets",
            "credentials_file": str(self.credentials_path),
            "immutability_mode": "object_lock",
        }
        self.destination_path = self.root / "destination.json"

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_destination(
        self,
        destination: dict[str, object] | None = None,
    ) -> None:
        self.destination_path.write_text(
            json.dumps(self.destination if destination is None else destination),
            encoding="utf-8",
        )

    def validate(self) -> dict[str, object]:
        return VALIDATOR.validate_destination(
            self.destination_path,
            expected_owner_uid=os.getuid(),
        )

    def test_validates_s3_destination(self) -> None:
        self.write_destination()

        validated = self.validate()

        self.assertEqual(validated["driver"], "s3")
        self.assertEqual(validated["immutability_mode"], "object_lock")

    def test_rejects_http_endpoint(self) -> None:
        self.destination["endpoint_url"] = "http://object.example.test"
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "HTTPS origin"):
            self.validate()

    def test_rejects_endpoint_credentials(self) -> None:
        self.destination["endpoint_url"] = (
            "https://access:secret@object.example.test"
        )
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "HTTPS origin"):
            self.validate()

    def test_rejects_path_traversal_prefix(self) -> None:
        self.destination["prefix"] = "../live-bucket"
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "clean relative"):
            self.validate()

    def test_rejects_ip_address_bucket(self) -> None:
        self.destination["bucket"] = "192.168.100.100"
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "IP address"):
            self.validate()

    def test_requires_object_lock(self) -> None:
        self.destination["immutability_mode"] = "none"
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "object_lock"):
            self.validate()

    def test_rejects_relative_credentials_path(self) -> None:
        self.destination["credentials_file"] = "credentials.json"
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "absolute"):
            self.validate()

    def test_rejects_exposed_credentials(self) -> None:
        self.credentials_path.chmod(0o640)
        self.write_destination()

        with self.assertRaisesRegex(
            VALIDATOR.DestinationError,
            "group or other",
        ):
            self.validate()

    def test_rejects_symlink_credentials(self) -> None:
        real_credentials = self.root / "real-credentials.json"
        self.credentials_path.rename(real_credentials)
        self.credentials_path.symlink_to(real_credentials)
        self.write_destination()

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "non-symlink"):
            self.validate()

    def test_rejects_secret_in_destination(self) -> None:
        destination = copy.deepcopy(self.destination)
        destination["secret_access_key"] = "must-not-be-here"
        self.write_destination(destination)

        with self.assertRaisesRegex(VALIDATOR.DestinationError, "unexpected"):
            self.validate()


if __name__ == "__main__":
    unittest.main()
