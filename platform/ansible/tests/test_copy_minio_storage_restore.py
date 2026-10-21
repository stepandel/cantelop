from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from support import load_script


COPY = load_script("copy-minio-storage-restore.py")


class FakeMinio:
    def __init__(self) -> None:
        self.source_bucket = "fire-fuse-workspaces"
        self.recovery_bucket = (
            "fire-fuse-recovery-20260728t130000z-89abcdef"
        )
        self.buckets = {
            self.source_bucket: {
                "chunks/0/0": b"first object\n",
                "chunks/0/1": b"second object\n",
            }
        }
        self.commands: list[list[str]] = []
        self.corrupt_copy = False

    @staticmethod
    def result(
        returncode: int = 0,
        stdout: str = "",
        stderr: str = "",
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess([], returncode, stdout, stderr)

    def runner(self, command: list[str], **_: object) -> subprocess.CompletedProcess[str]:
        self.commands.append(command)
        action = command[1]
        if action == "stat":
            bucket = command[2].split("/", 1)[1]
            if bucket in self.buckets:
                return self.result(stdout='{"status":"success"}\n')
            return self.result(returncode=1, stderr="bucket does not exist")
        if action == "mb":
            bucket = command[2].split("/", 1)[1]
            self.buckets[bucket] = {}
            return self.result(stdout='{"status":"success"}\n')
        if action == "mirror":
            source = command[-3].split("/", 1)[1]
            target = command[-2].split("/", 1)[1]
            self.buckets[target] = dict(self.buckets[source])
            if self.corrupt_copy:
                self.buckets[target]["chunks/0/1"] = b"corrupt\n"
            return self.result(stdout='{"status":"success"}\n')
        if action == "ls":
            bucket = command[-2].split("/", 1)[1]
            lines = [
                json.dumps({
                    "status": "success",
                    "type": "file",
                    "key": key,
                    "size": len(payload),
                })
                for key, payload in sorted(self.buckets[bucket].items())
            ]
            return self.result(stdout="\n".join(lines) + "\n")
        raise AssertionError(f"unexpected command: {command}")

    def stream_hasher(
        self,
        command: list[str],
        _: dict[str, str],
        __: int,
    ) -> tuple[str, int]:
        _, path = command[2].split("/", 1)
        bucket, key = path.split("/", 1)
        payload = self.buckets[bucket][key]
        return hashlib.sha256(payload).hexdigest(), len(payload)


class MinioStorageRestoreCopyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.restore_root = Path(self.temporary_directory.name) / "restores"
        self.restore_root.mkdir(mode=0o700)
        self.rehearsal_id = "restore-20260728T130000Z-89abcdef"
        self.workspace = self.restore_root / self.rehearsal_id
        service_root = self.workspace / "services" / "minio"
        service_root.mkdir(parents=True, mode=0o700)
        self.workspace.chmod(0o700)
        runtime = {
            "schema_version": 1,
            "status": "isolated-minio-qualified",
            "rehearsal_id": self.rehearsal_id,
            "set_id": "backup-20260728T120000Z-0123abcd",
            "endpoint_url": "http://127.0.0.1:19000",
            "bucket": "fire-fuse-workspaces",
            "object_snapshot_id": "sha256:" + "a" * 64,
        }
        extraction = {
            "set_id": runtime["set_id"],
            "source": {"object_snapshot_id": runtime["object_snapshot_id"]},
        }
        for filename, document in (
            ("minio-runtime.json", runtime),
            ("minio-extraction.json", extraction),
        ):
            path = self.workspace / filename
            path.write_text(json.dumps(document), encoding="utf-8")
            path.chmod(0o600)
        credentials = service_root / "minio.env"
        credentials.write_text(
            "MINIO_ROOT_USER=restoreadmin\n"
            f"MINIO_ROOT_PASSWORD={'b' * 64}\n",
            encoding="utf-8",
        )
        credentials.chmod(0o600)
        self.mc = Path(self.temporary_directory.name) / "mc"
        self.mc.write_text("fake\n", encoding="utf-8")
        self.mc.chmod(0o755)
        self.remote = FakeMinio()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def copy(self) -> dict[str, object]:
        return COPY.copy_recovery_bucket(
            self.rehearsal_id,
            self.restore_root,
            self.mc,
            timeout_seconds=60,
            expected_owner_uid=os.getuid(),
            runner=self.remote.runner,
            stream_hasher=self.remote.stream_hasher,
        )

    def test_copies_to_new_bucket_and_verifies_exact_object_content(self) -> None:
        result = self.copy()

        self.assertEqual(result["status"], "minio-recovery-bucket-qualified")
        self.assertEqual(result["object_count"], 2)
        self.assertTrue(result["source_snapshot_unchanged"])
        self.assertEqual(
            self.remote.buckets[self.remote.source_bucket],
            self.remote.buckets[self.remote.recovery_bucket],
        )
        mirror = next(
            command for command in self.remote.commands if command[1] == "mirror"
        )
        self.assertNotIn("--overwrite", mirror)
        self.assertNotIn("--remove", mirror)
        evidence = self.workspace / "minio-recovery-bucket.json"
        self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)
        self.assertNotIn("b" * 64, evidence.read_text())

    def test_rejects_an_existing_recovery_bucket(self) -> None:
        self.remote.buckets[self.remote.recovery_bucket] = {}

        with self.assertRaisesRegex(COPY.RecoveryCopyError, "already exists"):
            self.copy()

        self.assertFalse(
            (self.workspace / "minio-recovery-bucket.json").exists()
        )

    def test_content_mismatch_does_not_publish_evidence(self) -> None:
        self.remote.corrupt_copy = True

        with self.assertRaisesRegex(COPY.RecoveryCopyError, "does not match"):
            self.copy()

        self.assertFalse(
            (self.workspace / "minio-recovery-bucket.json").exists()
        )

    def test_refuses_to_overwrite_recovery_evidence(self) -> None:
        evidence = self.workspace / "minio-recovery-bucket.json"
        evidence.write_text("{}\n", encoding="utf-8")

        with self.assertRaisesRegex(COPY.RecoveryCopyError, "already exists"):
            self.copy()

        self.assertEqual(self.remote.commands, [])


if __name__ == "__main__":
    unittest.main()
