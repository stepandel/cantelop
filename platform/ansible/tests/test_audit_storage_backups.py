from __future__ import annotations

from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

from support import load_script


AUDIT = load_script("audit-storage-backups.py")


class StorageBackupAuditTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.staging = self.root / "staging"
        self.completed = self.root / "completed"
        self.staging.mkdir(mode=0o700)
        self.completed.mkdir(mode=0o700)
        self.set_id = "backup-20260728T120000Z-0123abcd"

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def create_set(self, *, manifest: bool = True) -> tuple[Path, dict[str, object]]:
        set_directory = self.staging / self.set_id
        set_directory.mkdir(mode=0o700)
        artifacts = []
        for component, filename in (
            ("juicefs_metadata", "metadata.dump"),
            ("minio_objects", "minio-objects.tar"),
            ("redis_persistence", "redis-persistence.tar.gz"),
            ("encrypted_restore_configuration", "restore-secrets.vault"),
        ):
            path = set_directory / filename
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
        document = {
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
        if manifest:
            manifest_path = set_directory / "manifest.json"
            manifest_path.write_text(json.dumps(document), encoding="utf-8")
            manifest_path.chmod(0o600)
        return set_directory, document

    def create_record(
        self,
        set_directory: Path,
        manifest: dict[str, object],
    ) -> Path:
        manifest_path = set_directory / "manifest.json"
        manifest_sha256 = hashlib.sha256(manifest_path.read_bytes()).hexdigest()
        retained_until = (
            datetime.now(timezone.utc) + timedelta(days=30)
        ).isoformat().replace("+00:00", "Z")
        record = {
            "schema_version": 1,
            "set_id": self.set_id,
            "recorded_at": "2026-07-28T12:30:00Z",
            "local": {
                "set_directory": str(set_directory),
                "manifest_path": str(manifest_path),
                "manifest_sha256": manifest_sha256,
            },
            "remote": {
                "endpoint_url": "https://backup.example.com",
                "bucket": "fire-fuse-backups",
                "prefix": f"shared-volume/backup-sets/{self.set_id}",
                "artifacts": [
                    {
                        "path": artifact["path"],
                        "version_id": f"version-{index}",
                        "sha256": artifact["sha256"],
                        "retained_until": retained_until,
                    }
                    for index, artifact in enumerate(manifest["artifacts"])
                ],
                "manifest": {
                    "path": "manifest.json",
                    "version_id": "manifest-version",
                    "sha256": manifest_sha256,
                    "retained_until": retained_until,
                },
            },
        }
        path = self.completed / f"{self.set_id}.json"
        path.write_text(json.dumps(record), encoding="utf-8")
        path.chmod(0o600)
        return path

    def audit(self) -> dict[str, object]:
        return AUDIT.audit_backups(
            self.staging,
            self.completed,
            expected_owner_uid=os.getuid(),
        )

    def test_reports_empty_roots_as_clean(self) -> None:
        result = self.audit()

        self.assertEqual(result["status"], "clean")
        self.assertEqual(result["sets"], [])

    def test_identifies_an_incomplete_set(self) -> None:
        self.create_set(manifest=False)

        result = self.audit()

        self.assertEqual(result["counts"]["incomplete"], 1)
        self.assertEqual(result["sets"][0]["state"], "incomplete")

    def test_identifies_a_complete_local_set(self) -> None:
        self.create_set()

        result = self.audit()

        self.assertEqual(result["counts"]["complete_local"], 1)
        self.assertEqual(result["status"], "clean")

    def test_identifies_a_transferred_set(self) -> None:
        set_directory, manifest = self.create_set()
        self.create_record(set_directory, manifest)

        result = self.audit()

        self.assertEqual(result["counts"]["transferred"], 1)
        self.assertEqual(result["status"], "clean")

    def test_identifies_an_invalid_local_set(self) -> None:
        set_directory, _ = self.create_set()
        (set_directory / "metadata.dump").write_bytes(b"changed\n")

        result = self.audit()

        self.assertEqual(result["counts"]["inconsistent"], 1)
        self.assertEqual(result["sets"][0]["detail"], "invalid_local_set")

    def test_identifies_a_mismatched_transfer_record(self) -> None:
        set_directory, manifest = self.create_set()
        record_path = self.create_record(set_directory, manifest)
        record = json.loads(record_path.read_text(encoding="utf-8"))
        record["local"]["manifest_sha256"] = "0" * 64
        record_path.write_text(json.dumps(record), encoding="utf-8")

        result = self.audit()

        self.assertEqual(result["counts"]["inconsistent"], 1)
        self.assertEqual(result["sets"][0]["detail"], "invalid_transfer_record")

    def test_identifies_an_orphan_transfer_record(self) -> None:
        set_directory, manifest = self.create_set()
        self.create_record(set_directory, manifest)
        for path in set_directory.iterdir():
            path.unlink()
        set_directory.rmdir()

        result = self.audit()

        self.assertEqual(result["counts"]["inconsistent"], 1)
        self.assertEqual(result["sets"][0]["detail"], "orphan_transfer_record")

    def test_identifies_an_unexpected_staging_entry(self) -> None:
        unexpected = self.staging / "notes.txt"
        unexpected.write_text("not a backup\n", encoding="utf-8")

        result = self.audit()

        self.assertEqual(result["counts"]["inconsistent"], 1)
        self.assertEqual(result["sets"][0]["detail"], "unexpected_staging_entry")


if __name__ == "__main__":
    unittest.main()
