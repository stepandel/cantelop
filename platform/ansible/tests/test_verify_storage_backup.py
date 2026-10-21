from __future__ import annotations

import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

from support import load_script


VERIFIER = load_script("verify-storage-backup.py")


class StorageBackupVerificationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        components = (
            "juicefs_metadata",
            "minio_objects",
            "redis_persistence",
            "encrypted_restore_configuration",
        )
        artifacts = []
        for component in components:
            artifact_path = self.root / f"{component}.bin"
            payload = f"test payload for {component}\n".encode()
            artifact_path.write_bytes(payload)
            artifacts.append(
                {
                    "component": component,
                    "path": artifact_path.name,
                    "size_bytes": len(payload),
                    "sha256": hashlib.sha256(payload).hexdigest(),
                }
            )
        self.manifest = {
            "schema_version": 1,
            "set_id": "backup-20260728T120000Z",
            "created_at": "2026-07-28T12:00:00Z",
            "source": {
                "volume_name": "fire-fuse-workspaces",
                "volume_uuid": "01234567-89ab-cdef-0123-456789abcdef",
                "juicefs_version": "1.3.0",
                "redis_version": "7.2.5",
                "minio_version": "RELEASE.2026-01-01T00-00-00Z",
                "object_snapshot_id": "snapshot-20260728T120000Z",
            },
            "artifacts": artifacts,
        }
        self.manifest_path = self.root / "manifest.json"

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_manifest(self, manifest: dict[str, object] | None = None) -> None:
        self.manifest_path.write_text(
            json.dumps(self.manifest if manifest is None else manifest),
            encoding="utf-8",
        )

    def test_verifies_complete_set(self) -> None:
        self.write_manifest()

        verified = VERIFIER.verify_manifest(self.manifest_path)

        self.assertEqual(verified["set_id"], self.manifest["set_id"])
        self.assertEqual(len(verified["artifacts"]), 4)

    def test_rejects_checksum_mismatch(self) -> None:
        self.manifest["artifacts"][0]["sha256"] = "0" * 64
        self.write_manifest()

        with self.assertRaisesRegex(
            VERIFIER.VerificationError,
            "does not match",
        ):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_missing_component(self) -> None:
        self.manifest["artifacts"].pop()
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "incomplete"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_duplicate_component(self) -> None:
        self.manifest["artifacts"][1]["component"] = "juicefs_metadata"
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "duplicated"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_path_traversal(self) -> None:
        self.manifest["artifacts"][0]["path"] = "../metadata.dump"
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "clean relative"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_symlink_artifact(self) -> None:
        target = self.root / self.manifest["artifacts"][0]["path"]
        symlink = self.root / "linked-metadata.dump"
        symlink.symlink_to(target)
        self.manifest["artifacts"][0]["path"] = symlink.name
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "non-symlink"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_unexpected_manifest_field(self) -> None:
        manifest = copy.deepcopy(self.manifest)
        manifest["redis_password"] = "must-never-appear-here"
        self.write_manifest(manifest)

        with self.assertRaisesRegex(VERIFIER.VerificationError, "unexpected"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_boolean_schema_version(self) -> None:
        self.manifest["schema_version"] = True
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "integer"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_non_utc_timestamp(self) -> None:
        self.manifest["created_at"] = "2026-07-28T12:00:00+02:00"
        self.write_manifest()

        with self.assertRaisesRegex(VERIFIER.VerificationError, "UTC"):
            VERIFIER.verify_manifest(self.manifest_path)

    def test_rejects_symlink_manifest(self) -> None:
        real_manifest = self.root / "real-manifest.json"
        real_manifest.write_text(json.dumps(self.manifest), encoding="utf-8")
        self.manifest_path.symlink_to(real_manifest)

        with self.assertRaisesRegex(VERIFIER.VerificationError, "non-symlink"):
            VERIFIER.verify_manifest(self.manifest_path)


if __name__ == "__main__":
    unittest.main()
