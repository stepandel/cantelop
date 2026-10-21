from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

from support import load_script


VALIDATOR = load_script("validate-storage-backup-restore-evidence.py")


class RestoreEvidenceValidationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.staging = self.root / "staging"
        self.completed = self.root / "completed"
        self.restores = self.root / "restores"
        for path in (self.staging, self.completed, self.restores):
            path.mkdir(mode=0o700)
        self.set_id = "backup-20260728T120000Z-0123abcd"
        self.set_directory = self.staging / self.set_id
        self.set_directory.mkdir(mode=0o700)

        artifacts = []
        for component, filename in (
            ("juicefs_metadata", "metadata.dump"),
            ("minio_objects", "minio-objects.tar"),
            ("redis_persistence", "redis-persistence.tar.gz"),
            ("encrypted_restore_configuration", "restore-secrets.vault"),
        ):
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
        self.volume_uuid = "01234567-89ab-cdef-0123-456789abcdef"
        manifest = {
            "schema_version": 1,
            "set_id": self.set_id,
            "created_at": "2026-07-28T12:00:00Z",
            "source": {
                "volume_name": "fire-fuse-workspaces",
                "volume_uuid": self.volume_uuid,
                "juicefs_version": "1.4.0",
                "redis_version": "7.2.5",
                "minio_version": "RELEASE.2026-01-01T00-00-00Z",
                "object_snapshot_id": "sha256:" + "a" * 64,
            },
            "artifacts": artifacts,
        }
        self.manifest_path = self.set_directory / "manifest.json"
        self.manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        self.manifest_path.chmod(0o600)
        manifest_sha256 = hashlib.sha256(
            self.manifest_path.read_bytes()
        ).hexdigest()
        retained_until = "2026-09-01T00:00:00Z"
        self.remote_prefix = f"shared-volume/backup-sets/{self.set_id}"
        record = {
            "schema_version": 1,
            "set_id": self.set_id,
            "recorded_at": "2026-07-28T12:30:00Z",
            "local": {
                "set_directory": str(self.set_directory),
                "manifest_path": str(self.manifest_path),
                "manifest_sha256": manifest_sha256,
            },
            "remote": {
                "endpoint_url": "https://backup.example.com",
                "bucket": "fire-fuse-backups",
                "prefix": self.remote_prefix,
                "artifacts": [
                    {
                        "path": artifact["path"],
                        "version_id": f"version-{index}",
                        "sha256": artifact["sha256"],
                        "retained_until": retained_until,
                    }
                    for index, artifact in enumerate(artifacts)
                ],
                "manifest": {
                    "path": "manifest.json",
                    "version_id": "manifest-version",
                    "sha256": manifest_sha256,
                    "retained_until": retained_until,
                },
            },
        }
        self.record_path = self.completed / f"{self.set_id}.json"
        self.record_path.write_text(json.dumps(record), encoding="utf-8")
        self.record_path.chmod(0o600)
        self.evidence = {
            "schema_version": 1,
            "set_id": self.set_id,
            "started_at": "2026-07-29T01:00:00Z",
            "completed_at": "2026-07-29T01:30:00Z",
            "source": {
                "manifest_sha256": manifest_sha256,
                "transfer_record_sha256": hashlib.sha256(
                    self.record_path.read_bytes()
                ).hexdigest(),
                "volume_uuid": self.volume_uuid,
                "remote_prefix": self.remote_prefix,
            },
            "target": {
                "rehearsal_id": "restore-20260729T010000Z-89abcdef",
                "isolated": True,
                "restored_volume_uuid": self.volume_uuid,
            },
            "checks": {
                "manifest_verified": True,
                "object_snapshot_restored": True,
                "redis_snapshot_restored": True,
                "metadata_dump_loaded": True,
                "juicefs_status_verified": True,
                "first_mount_marker_sha256": "b" * 64,
                "clean_unmount_verified": True,
                "remount_marker_sha256": "b" * 64,
                "production_guard_before_sha256": "c" * 64,
                "production_guard_after_sha256": "c" * 64,
            },
        }
        self.evidence_path = self.restores / f"{self.set_id}.json"
        self.write_evidence()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_evidence(self) -> None:
        self.evidence_path.write_text(
            json.dumps(self.evidence),
            encoding="utf-8",
        )
        self.evidence_path.chmod(0o600)

    def validate(self) -> dict[str, str]:
        return VALIDATOR.validate_restore_evidence(
            self.set_id,
            self.restores,
            self.staging,
            self.completed,
            expected_owner_uid=os.getuid(),
        )

    def test_validates_bound_restore_evidence(self) -> None:
        result = self.validate()

        self.assertEqual(result["status"], "restore-evidence-verified")
        self.assertEqual(result["set_id"], self.set_id)

    def test_rejects_a_changed_manifest_binding(self) -> None:
        self.evidence["source"]["manifest_sha256"] = "0" * 64
        self.write_evidence()

        with self.assertRaisesRegex(VALIDATOR.EvidenceError, "binding"):
            self.validate()

    def test_requires_an_isolated_target(self) -> None:
        self.evidence["target"]["isolated"] = False
        self.write_evidence()

        with self.assertRaisesRegex(VALIDATOR.EvidenceError, "not isolated"):
            self.validate()

    def test_requires_the_marker_to_survive_remount(self) -> None:
        self.evidence["checks"]["remount_marker_sha256"] = "d" * 64
        self.write_evidence()

        with self.assertRaisesRegex(VALIDATOR.EvidenceError, "after remount"):
            self.validate()

    def test_requires_the_production_guard_to_remain_unchanged(self) -> None:
        self.evidence["checks"]["production_guard_after_sha256"] = "d" * 64
        self.write_evidence()

        with self.assertRaisesRegex(VALIDATOR.EvidenceError, "production guard"):
            self.validate()

    def test_requires_every_restore_check_to_pass(self) -> None:
        self.evidence["checks"]["metadata_dump_loaded"] = False
        self.write_evidence()

        with self.assertRaisesRegex(
            VALIDATOR.EvidenceError,
            "metadata_dump_loaded",
        ):
            self.validate()

    def test_rejects_world_readable_evidence(self) -> None:
        self.evidence_path.chmod(0o644)

        with self.assertRaisesRegex(
            VALIDATOR.EvidenceError,
            "trusted private file",
        ):
            self.validate()


if __name__ == "__main__":
    unittest.main()
