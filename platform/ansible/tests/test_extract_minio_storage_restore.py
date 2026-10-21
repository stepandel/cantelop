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


EXTRACT = load_script("extract-minio-storage-restore.py")


class MinioStorageRestoreExtractionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.restore_root = Path(self.temporary_directory.name) / "restores"
        self.restore_root.mkdir(mode=0o700)
        self.rehearsal_id = "restore-20260728T130000Z-89abcdef"
        self.set_id = "backup-20260728T120000Z-0123abcd"
        self.workspace = self.restore_root / self.rehearsal_id
        self.workspace.mkdir(mode=0o700)
        self.archive = self.workspace / "minio-objects.tar"
        self.write_archive()
        self.write_materialization()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_archive(
        self,
        *,
        unsafe_name: str | None = None,
        symlink: bool = False,
    ) -> None:
        with tarfile.open(self.archive, "w") as archive:
            for directory in (".minio.sys", "fire-fuse-workspaces"):
                member = tarfile.TarInfo(directory)
                member.type = tarfile.DIRTYPE
                member.mode = 0o750
                archive.addfile(member)
            payload = b'{"format":"xl-single"}\n'
            member = tarfile.TarInfo(".minio.sys/format.json")
            member.size = len(payload)
            member.mode = 0o600
            archive.addfile(member, io.BytesIO(payload))
            object_payload = b"workspace object\n"
            member = tarfile.TarInfo(
                unsafe_name or "fire-fuse-workspaces/object/xl.meta"
            )
            if symlink:
                member.type = tarfile.SYMTYPE
                member.linkname = "/etc/passwd"
            else:
                member.size = len(object_payload)
            member.mode = 0o600
            archive.addfile(
                member,
                None if symlink else io.BytesIO(object_payload),
            )
        self.archive.chmod(0o600)

    def write_materialization(self) -> None:
        artifact_specs = (
            ("juicefs_metadata", "metadata.dump", b"metadata\n"),
            ("minio_objects", "minio-objects.tar", self.archive.read_bytes()),
            ("redis_persistence", "redis-persistence.tar.gz", b"redis\n"),
            (
                "encrypted_restore_configuration",
                "restore-secrets.vault",
                b"$ANSIBLE_VAULT;1.2;AES256;restore\n",
            ),
        )
        artifacts = []
        for component, filename, payload in artifact_specs:
            path = self.workspace / filename
            path.write_bytes(payload)
            path.chmod(0o600)
            artifacts.append({
                "component": component,
                "path": filename,
                "size_bytes": len(payload),
                "sha256": hashlib.sha256(payload).hexdigest(),
            })
        minio = next(
            artifact
            for artifact in artifacts
            if artifact["component"] == "minio_objects"
        )
        manifest = {
            "schema_version": 1,
            "set_id": self.set_id,
            "created_at": "2026-07-28T12:00:00Z",
            "source": {
                "volume_name": "fire-fuse-workspaces",
                "volume_uuid": "01234567-89ab-cdef-0123-456789abcdef",
                "juicefs_version": "1.4.0",
                "redis_version": "7.2.5",
                "minio_version": "RELEASE.2026-01-01T00-00-00Z",
                "object_snapshot_id": f"sha256:{minio['sha256']}",
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
        return EXTRACT.extract_minio_snapshot(
            self.rehearsal_id,
            self.restore_root,
            expected_owner_uid=os.getuid(),
        )

    def test_extracts_regular_files_and_publishes_evidence(self) -> None:
        result = self.extract()

        self.assertEqual(result["status"], "minio-snapshot-extracted")
        data = self.workspace / "services" / "minio" / "data"
        self.assertEqual(
            (data / ".minio.sys" / "format.json").read_text(),
            '{"format":"xl-single"}\n',
        )
        self.assertTrue((data / "fire-fuse-workspaces").is_dir())
        evidence = self.workspace / "minio-extraction.json"
        self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)
        verified = EXTRACT.verify_existing_extraction(
            self.rehearsal_id,
            self.restore_root,
            expected_owner_uid=os.getuid(),
        )
        self.assertEqual(verified["status"], "minio-extraction-verified")
        self.assertRegex(verified["tree_sha256"], r"^[0-9a-f]{64}$")

    def test_existing_verification_rejects_changed_extracted_data(self) -> None:
        self.extract()
        data = self.workspace / "services" / "minio" / "data"
        (data / ".minio.sys" / "format.json").write_text("changed\n")

        with self.assertRaisesRegex(EXTRACT.ExtractionError, "changed"):
            EXTRACT.verify_existing_extraction(
                self.rehearsal_id,
                self.restore_root,
                expected_owner_uid=os.getuid(),
            )

    def test_rejects_path_traversal_without_writing_outside_target(self) -> None:
        self.write_archive(unsafe_name="../escaped")
        self.write_materialization()

        with self.assertRaisesRegex(EXTRACT.ExtractionError, "unsafe member"):
            self.extract()

        self.assertFalse((self.workspace / "services" / "escaped").exists())
        self.assertFalse((self.workspace / "minio-extraction.json").exists())

    def test_rejects_links(self) -> None:
        self.write_archive(symlink=True)
        self.write_materialization()

        with self.assertRaisesRegex(EXTRACT.ExtractionError, "links or special"):
            self.extract()

        self.assertFalse((self.workspace / "minio-extraction.json").exists())

    def test_refuses_an_existing_minio_target(self) -> None:
        target = self.workspace / "services"
        target.mkdir(mode=0o700)

        with self.assertRaisesRegex(EXTRACT.ExtractionError, "already exists"):
            self.extract()


if __name__ == "__main__":
    unittest.main()
