from pathlib import Path
import unittest


PLAYBOOK = (
    Path(__file__).parents[1]
    / "playbooks"
    / "storage"
    / "qualify-storage-backup-restore.yml"
)


class QualifyRestorePlaybookTest(unittest.TestCase):
    def test_sequences_every_restore_stage(self) -> None:
        playbook = PLAYBOOK.read_text(encoding="utf-8")
        roles = (
            "storage_backup_materialize_restore",
            "storage_backup_extract_minio_restore",
            "storage_backup_start_minio_restore",
            "storage_backup_copy_minio_restore",
            "storage_backup_restore_redis_snapshot",
            "storage_backup_restore_metadata",
            "storage_backup_qualify_restore_mount",
            "storage_backup_finalize_restore",
        )

        positions = [playbook.index(f"name: {role}") for role in roles]
        self.assertEqual(positions, sorted(positions))

    def test_propagates_generated_rehearsal_id(self) -> None:
        playbook = PLAYBOOK.read_text(encoding="utf-8")

        self.assertIn(
            "storage_backup_materialize_restore_evidence.rehearsal_id",
            playbook,
        )
        self.assertGreaterEqual(
            playbook.count("storage_backup_qualify_restore_rehearsal_id"),
            9,
        )

    def test_cleanup_remains_a_separate_reviewed_action(self) -> None:
        playbook = PLAYBOOK.read_text(encoding="utf-8")

        self.assertNotIn("storage_backup_cleanup_restore", playbook)
        self.assertIn("cleanup_required: true", playbook)


if __name__ == "__main__":
    unittest.main()
