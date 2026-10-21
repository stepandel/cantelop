from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

from support import load_script


PLANNER = load_script("plan-storage-backup-retention.py")


class StorageBackupRetentionPlanningTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.staging = self.root / "staging"
        self.completed = self.root / "completed"
        self.restores = self.root / "restores"
        self.staging.mkdir(mode=0o700)
        self.completed.mkdir(mode=0o700)
        self.restores.mkdir(mode=0o700)
        self.now = datetime(2026, 8, 1, 12, 0, tzinfo=timezone.utc)

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def create_set(
        self,
        set_id: str,
        created_at: str,
        *,
        state: str = "transferred",
        retained_until: str = "2026-07-30T12:00:00Z",
    ) -> None:
        set_directory = self.staging / set_id
        set_directory.mkdir(mode=0o700)
        if state == "incomplete":
            return
        artifacts = []
        for component, filename in (
            ("juicefs_metadata", "metadata.dump"),
            ("minio_objects", "minio-objects.tar"),
            ("redis_persistence", "redis-persistence.tar.gz"),
            ("encrypted_restore_configuration", "restore-secrets.vault"),
        ):
            path = set_directory / filename
            payload = f"{set_id} {component}\n".encode()
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
        manifest = {
            "schema_version": 1,
            "set_id": set_id,
            "created_at": created_at,
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
        manifest_path = set_directory / "manifest.json"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        manifest_path.chmod(0o600)
        if state == "complete_local":
            return

        manifest_sha256 = hashlib.sha256(manifest_path.read_bytes()).hexdigest()
        record = {
            "schema_version": 1,
            "set_id": set_id,
            "recorded_at": "2026-07-31T00:00:00Z",
            "local": {
                "set_directory": str(set_directory),
                "manifest_path": str(manifest_path),
                "manifest_sha256": manifest_sha256,
            },
            "remote": {
                "endpoint_url": "https://backup.example.com",
                "bucket": "fire-fuse-backups",
                "prefix": f"shared-volume/backup-sets/{set_id}",
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
        record_path = self.completed / f"{set_id}.json"
        record_path.write_text(json.dumps(record), encoding="utf-8")
        record_path.chmod(0o600)
        evidence = {
            "schema_version": 1,
            "set_id": set_id,
            "started_at": "2026-07-31T01:00:00Z",
            "completed_at": "2026-07-31T01:30:00Z",
            "source": {
                "manifest_sha256": manifest_sha256,
                "transfer_record_sha256": hashlib.sha256(
                    record_path.read_bytes()
                ).hexdigest(),
                "volume_uuid": manifest["source"]["volume_uuid"],
                "remote_prefix": record["remote"]["prefix"],
            },
            "target": {
                "rehearsal_id": f"restore-20260731T010000Z-{set_id[-8:]}",
                "isolated": True,
                "restored_volume_uuid": manifest["source"]["volume_uuid"],
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
        evidence_path = self.restores / f"{set_id}.json"
        evidence_path.write_text(json.dumps(evidence), encoding="utf-8")
        evidence_path.chmod(0o600)

    def plan(
        self,
        restore_qualified_set_id: str,
        *,
        daily: int = 1,
        weekly: int = 1,
        monthly: int = 1,
    ) -> dict[str, object]:
        return PLANNER.plan_retention(
            self.staging,
            self.completed,
            self.restores,
            restore_qualified_set_id,
            daily=daily,
            weekly=weekly,
            monthly=monthly,
            expected_owner_uid=os.getuid(),
            now=self.now,
        )

    def test_keeps_final_known_good_and_restore_qualified_set(self) -> None:
        set_id = "backup-20260731T120000Z-00000001"
        self.create_set(set_id, "2026-07-31T12:00:00Z")

        plan = self.plan(set_id)

        decision = plan["decisions"][0]
        self.assertEqual(decision["action"], "keep")
        self.assertIn("final_known_good", decision["reasons"])
        self.assertIn("restore_qualified", decision["reasons"])

    def test_selects_expired_sets_outside_all_recovery_tiers(self) -> None:
        newest = "backup-20260731T120000Z-00000001"
        middle = "backup-20260731T110000Z-00000002"
        oldest = "backup-20260731T100000Z-00000003"
        self.create_set(newest, "2026-07-31T12:00:00Z")
        self.create_set(middle, "2026-07-31T11:00:00Z")
        self.create_set(oldest, "2026-07-31T10:00:00Z")

        plan = self.plan(newest)

        candidates = {
            decision["set_id"]
            for decision in plan["decisions"]
            if decision["action"] == "candidate_expire_complete_set"
        }
        self.assertEqual(candidates, {middle, oldest})

    def test_active_remote_retention_prevents_selection(self) -> None:
        newest = "backup-20260731T120000Z-00000001"
        older = "backup-20260731T110000Z-00000002"
        self.create_set(newest, "2026-07-31T12:00:00Z")
        self.create_set(
            older,
            "2026-07-31T11:00:00Z",
            retained_until="2026-09-01T00:00:00Z",
        )

        plan = self.plan(newest)

        decision = next(
            item for item in plan["decisions"] if item["set_id"] == older
        )
        self.assertEqual(decision["action"], "keep")
        self.assertIn("remote_retention_active", decision["reasons"])

    def test_keeps_incomplete_and_local_only_sets(self) -> None:
        transferred = "backup-20260731T120000Z-00000001"
        local = "backup-20260730T120000Z-00000002"
        incomplete = "backup-20260729T120000Z-00000003"
        self.create_set(transferred, "2026-07-31T12:00:00Z")
        self.create_set(
            local,
            "2026-07-30T12:00:00Z",
            state="complete_local",
        )
        self.create_set(
            incomplete,
            "2026-07-29T12:00:00Z",
            state="incomplete",
        )

        plan = self.plan(transferred)

        decisions = {
            decision["set_id"]: decision
            for decision in plan["decisions"]
        }
        self.assertEqual(decisions[local]["action"], "keep")
        self.assertIn("off_host_transfer_absent", decisions[local]["reasons"])
        self.assertEqual(decisions[incomplete]["action"], "keep")
        self.assertIn(
            "capture_incomplete",
            decisions[incomplete]["reasons"],
        )

    def test_refuses_an_inconsistent_inventory(self) -> None:
        transferred = "backup-20260731T120000Z-00000001"
        self.create_set(transferred, "2026-07-31T12:00:00Z")
        (self.staging / "unexpected.txt").write_text(
            "not a set\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(
            PLANNER.PlanningError,
            "inconsistent",
        ):
            self.plan(transferred)

    def test_requires_a_transferred_restore_qualified_set(self) -> None:
        transferred = "backup-20260731T120000Z-00000001"
        missing = "backup-20260730T120000Z-00000002"
        self.create_set(transferred, "2026-07-31T12:00:00Z")

        with self.assertRaisesRegex(
            PLANNER.PlanningError,
            "restore evidence file",
        ):
            self.plan(missing)

    def test_planning_does_not_modify_inventory(self) -> None:
        transferred = "backup-20260731T120000Z-00000001"
        self.create_set(transferred, "2026-07-31T12:00:00Z")
        before = {
            str(path.relative_to(self.root)): (
                path.stat().st_mode,
                path.stat().st_mtime_ns,
                path.read_bytes() if path.is_file() else None,
            )
            for path in self.root.rglob("*")
        }

        self.plan(transferred)

        after = {
            str(path.relative_to(self.root)): (
                path.stat().st_mode,
                path.stat().st_mtime_ns,
                path.read_bytes() if path.is_file() else None,
            )
            for path in self.root.rglob("*")
        }
        self.assertEqual(after, before)


if __name__ == "__main__":
    unittest.main()
