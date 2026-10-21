from __future__ import annotations

import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest

from support import load_script


EXTRACT = load_script("extract-redis-storage-restore.py")


class RedisStorageRestoreExtractionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.restore_root = Path(self.temporary_directory.name) / "restores"
        self.restore_root.mkdir(mode=0o700)
        self.rehearsal_id = "restore-20260728T130000Z-89abcdef"
        self.set_id = "backup-20260728T120000Z-0123abcd"
        self.workspace = self.restore_root / self.rehearsal_id
        self.workspace.mkdir(mode=0o700)
        self.archive = self.workspace / "redis-persistence.tar.gz"
        self.write_archive()
        self.write_materialization()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_archive(self, unsafe_name: str | None = None) -> None:
        with tarfile.open(self.archive, "w:gz") as archive:
            for name, payload in (
                ("dump.rdb", b"REDIS0011rdb\n"),
                (
                    "appendonlydir/appendonly.aof.manifest",
                    b"file appendonly.aof.1.base.rdb seq 1 type b\n",
                ),
                (
                    unsafe_name
                    or "appendonlydir/appendonly.aof.1.base.rdb",
                    b"REDIS0011aof\n",
                ),
            ):
                member = tarfile.TarInfo(name)
                member.size = len(payload)
                member.mode = 0o600
                archive.addfile(member, io.BytesIO(payload))
        self.archive.chmod(0o600)

    def write_materialization(self) -> None:
        specs = (
            ("juicefs_metadata", "metadata.dump", b"metadata\n"),
            ("minio_objects", "minio-objects.tar", b"minio\n"),
            (
                "redis_persistence",
                "redis-persistence.tar.gz",
                self.archive.read_bytes(),
            ),
            (
                "encrypted_restore_configuration",
                "restore-secrets.vault",
                b"$ANSIBLE_VAULT;1.2;AES256;restore\n",
            ),
        )
        artifacts = []
        for component, filename, payload in specs:
            path = self.workspace / filename
            path.write_bytes(payload)
            path.chmod(0o600)
            artifacts.append({
                "component": component,
                "path": filename,
                "size_bytes": len(payload),
                "sha256": hashlib.sha256(payload).hexdigest(),
            })
        manifest = {
            "schema_version": 1,
            "set_id": self.set_id,
            "created_at": "2026-07-28T12:00:00Z",
            "source": {
                "volume_name": "fire-fuse-workspaces",
                "volume_uuid": "01234567-89ab-cdef-0123-456789abcdef",
                "juicefs_version": "1.4.0",
                "redis_version": "7.2.5",
                "minio_version": "RELEASE.2025-10-15T17-29-55Z",
                "object_snapshot_id": "sha256:" + "a" * 64,
            },
            "artifacts": artifacts,
        }
        manifest_path = self.workspace / "manifest.json"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        manifest_path.chmod(0o600)
        record = {
            "schema_version": 1,
            "status": "materialized-and-verified",
            "rehearsal_id": self.rehearsal_id,
            "set_id": self.set_id,
            "materialized_at": "2026-07-28T13:00:00Z",
            "workspace": str(self.workspace),
            "source": {
                "endpoint_url": "https://backup.example.com",
                "bucket": "fire-fuse-backups",
                "remote_prefix": f"shared/backup-sets/{self.set_id}",
                "transfer_record_sha256": "a" * 64,
                "manifest_version_id": "manifest-version",
                "manifest_sha256": hashlib.sha256(
                    manifest_path.read_bytes()
                ).hexdigest(),
            },
            "artifacts": [{
                "path": artifact["path"],
                "version_id": f"version-{index}",
                "size_bytes": artifact["size_bytes"],
                "sha256": artifact["sha256"],
            } for index, artifact in enumerate(artifacts)],
        }
        record_path = self.workspace / "materialization.json"
        record_path.write_text(json.dumps(record), encoding="utf-8")
        record_path.chmod(0o600)

    def extract(self) -> dict[str, object]:
        return EXTRACT.extract_redis_snapshot(
            self.rehearsal_id,
            self.restore_root,
            maximum_expanded_bytes=1024 * 1024,
            expected_owner_uid=os.getuid(),
        )

    def test_extracts_rdb_and_aof_then_reverifies_tree(self) -> None:
        result = self.extract()

        self.assertEqual(result["status"], "redis-snapshot-extracted")
        self.assertEqual(result["target"]["aof_file_count"], 2)
        verified = EXTRACT.verify_existing_extraction(
            self.rehearsal_id,
            self.restore_root,
            expected_owner_uid=os.getuid(),
        )
        self.assertEqual(verified["status"], "redis-extraction-verified")
        self.assertEqual(
            verified["tree_sha256"],
            result["target"]["tree_sha256"],
        )

    def test_rejects_archive_traversal(self) -> None:
        self.write_archive(unsafe_name="../escaped")
        self.write_materialization()

        with self.assertRaisesRegex(
            EXTRACT.RedisExtractionError,
            "unsafe member",
        ):
            self.extract()

        self.assertFalse((self.workspace / "escaped").exists())
        self.assertFalse((self.workspace / "redis-extraction.json").exists())

    def test_reverification_rejects_changed_persistence(self) -> None:
        self.extract()
        dump = self.workspace / "services" / "redis" / "data" / "dump.rdb"
        dump.write_bytes(b"changed\n")

        with self.assertRaisesRegex(
            EXTRACT.RedisExtractionError,
            "changed",
        ):
            EXTRACT.verify_existing_extraction(
                self.rehearsal_id,
                self.restore_root,
                expected_owner_uid=os.getuid(),
            )


if __name__ == "__main__":
    unittest.main()
