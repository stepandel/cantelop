from __future__ import annotations

from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from support import load_script


MATERIALIZE = load_script("materialize-storage-backup-restore.py")


class StorageBackupRestoreMaterializationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.staging = self.root / "backups" / "staging"
        self.completed = self.root / "backups" / "completed"
        self.restores = self.root / "restores"
        for directory in (self.staging, self.completed, self.restores):
            directory.mkdir(parents=True, mode=0o700)
        self.set_id = "backup-20260728T120000Z-0123abcd"
        self.set_directory = self.staging / self.set_id
        self.set_directory.mkdir(mode=0o700)
        self.rehearsal_id = "restore-20260728T130000Z-89abcdef"
        self.destination_path = self.root / "destination.json"
        self.destination_path.write_text("{}\n", encoding="utf-8")
        self.mc_binary = self.root / "mc"
        self.mc_binary.write_text("fake\n", encoding="utf-8")
        self.mc_binary.chmod(0o755)

        self.remote_payloads: dict[str, bytes] = {}
        artifacts = []
        specifications = (
            ("juicefs_metadata", "metadata.dump"),
            ("minio_objects", "minio-objects.tar"),
            ("redis_persistence", "redis-persistence.tar.gz"),
            ("encrypted_restore_configuration", "restore-secrets.vault"),
        )
        for component, filename in specifications:
            payload = f"payload for {component}\n".encode()
            path = self.set_directory / filename
            path.write_bytes(payload)
            path.chmod(0o600)
            self.remote_payloads[filename] = payload
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
                "minio_version": "RELEASE.2026-01-01T00-00-00Z",
                "object_snapshot_id": "sha256:" + "a" * 64,
            },
            "artifacts": artifacts,
        }
        manifest_bytes = json.dumps(manifest).encode()
        manifest_path = self.set_directory / "manifest.json"
        manifest_path.write_bytes(manifest_bytes)
        manifest_path.chmod(0o600)
        self.remote_payloads["manifest.json"] = manifest_bytes
        retained_until = (
            datetime.now(timezone.utc) + timedelta(days=30)
        ).isoformat().replace("+00:00", "Z")
        remote_artifacts = [{
            "path": artifact["path"],
            "version_id": f"version-{index}",
            "sha256": artifact["sha256"],
            "retained_until": retained_until,
        } for index, artifact in enumerate(artifacts)]
        record = {
            "schema_version": 1,
            "set_id": self.set_id,
            "recorded_at": "2026-07-28T12:30:00Z",
            "local": {
                "set_directory": str(self.set_directory),
                "manifest_path": str(manifest_path),
                "manifest_sha256": hashlib.sha256(manifest_bytes).hexdigest(),
            },
            "remote": {
                "endpoint_url": "https://backup.example.com",
                "bucket": "fire-fuse-backups",
                "prefix": f"shared-volume/backup-sets/{self.set_id}",
                "artifacts": remote_artifacts,
                "manifest": {
                    "path": "manifest.json",
                    "version_id": "version-manifest",
                    "sha256": hashlib.sha256(manifest_bytes).hexdigest(),
                    "retained_until": retained_until,
                },
            },
        }
        record_path = self.completed / f"{self.set_id}.json"
        record_path.write_text(json.dumps(record), encoding="utf-8")
        record_path.chmod(0o600)
        self.commands: list[list[str]] = []
        self.corrupt: str | None = None

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def downloader(
        self,
        command: list[str],
        _: dict[str, str],
        destination: Path,
        __: int,
        ___: int,
    ) -> tuple[str, int]:
        self.commands.append(command)
        filename = command[2].rsplit("/", 1)[-1]
        payload = self.remote_payloads[filename]
        if filename == self.corrupt:
            payload += b"corrupt"
        destination.write_bytes(payload)
        return hashlib.sha256(payload).hexdigest(), len(payload)

    def test_streaming_download_rejects_oversized_response_before_staging_it(self) -> None:
        destination = self.root / "oversized.bin"
        with self.assertRaisesRegex(MATERIALIZE.MaterializationError, "recorded size"):
            MATERIALIZE.subprocess_download(
                [sys.executable, "-c", "import sys; sys.stdout.buffer.write(b'x' * 1048576)"],
                os.environ.copy(),
                destination,
                5,
                16,
            )
        self.assertFalse(destination.exists())
        self.assertFalse(destination.with_name(destination.name + ".partial").exists())

    def test_streaming_download_accepts_exact_recorded_size(self) -> None:
        destination = self.root / "exact.bin"
        checksum, size = MATERIALIZE.subprocess_download(
            [sys.executable, "-c", "import sys; sys.stdout.buffer.write(b'exact')"],
            os.environ.copy(),
            destination,
            5,
            5,
        )
        self.assertEqual((checksum, size), (hashlib.sha256(b"exact").hexdigest(), 5))
        self.assertEqual(destination.read_bytes(), b"exact")

    def materialize(self, **overrides: object) -> dict[str, object]:
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
        qualification.update(
            overrides.pop("qualification_override", {})  # type: ignore[arg-type]
        )
        parameters = {
            "set_id": self.set_id,
            "destination_path": self.destination_path,
            "mc_binary": self.mc_binary,
            "staging_root": self.staging,
            "completed_root": self.completed,
            "restore_root": self.restores,
            "rehearsal_id": self.rehearsal_id,
            "timeout_seconds": 60,
            "expected_owner_uid": os.getuid(),
            "downloader": self.downloader,
        }
        parameters.update(overrides)
        with ExitStack() as patches:
            patches.enter_context(mock.patch.object(
                MATERIALIZE.QUALIFIER,
                "qualify_destination",
                return_value=qualification,
            ))
            patches.enter_context(mock.patch.object(
                MATERIALIZE.QUALIFIER.VALIDATOR,
                "validate_destination",
                return_value=destination,
            ))
            patches.enter_context(mock.patch.object(
                MATERIALIZE.QUALIFIER,
                "read_credentials",
                return_value={
                    "access_key_id": "restore-reader",
                    "secret_access_key": "never-record-this",
                },
            ))
            return MATERIALIZE.materialize_restore(**parameters)

    def test_materializes_exact_versions_and_publishes_redacted_record(self) -> None:
        result = self.materialize()

        self.assertEqual(result["status"], "materialized-and-verified")
        self.assertEqual(len(self.commands), 5)
        self.assertTrue(all("--version-id" in command for command in self.commands))
        self.assertEqual(self.commands[0][-1], "version-manifest")
        workspace = self.restores / self.rehearsal_id
        record = json.loads((workspace / "materialization.json").read_text())
        self.assertNotIn("never-record-this", json.dumps(record))
        for filename in self.remote_payloads:
            self.assertEqual(
                (workspace / filename).read_bytes(),
                self.remote_payloads[filename],
            )
            self.assertEqual((workspace / filename).stat().st_mode & 0o777, 0o600)

    def test_checksum_failure_does_not_publish_materialization_record(self) -> None:
        self.corrupt = "metadata.dump"

        with self.assertRaisesRegex(
            MATERIALIZE.MaterializationError,
            "checksum",
        ):
            self.materialize()

        self.assertFalse(
            (self.restores / self.rehearsal_id / "materialization.json").exists()
        )

    def test_refuses_to_reuse_a_rehearsal_directory(self) -> None:
        (self.restores / self.rehearsal_id).mkdir(mode=0o700)

        with self.assertRaisesRegex(
            MATERIALIZE.MaterializationError,
            "already exists",
        ):
            self.materialize()

        self.assertEqual(self.commands, [])

    def test_rejects_a_different_configured_destination(self) -> None:
        with self.assertRaisesRegex(
            MATERIALIZE.MaterializationError,
            "does not match",
        ):
            self.materialize(
                qualification_override={
                "endpoint_url": "https://other.example.com",
                }
            )


if __name__ == "__main__":
    unittest.main()
