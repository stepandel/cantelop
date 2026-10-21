from pathlib import Path
import unittest

from support import role_dependencies, runs_before


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_cleanup_restore"
)


class CleanupRestoreRoleTest(unittest.TestCase):
    def test_cleanup_requires_validated_canonical_evidence(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertTrue(
            runs_before(
                "storage_backup_cleanup_restore",
                "Revalidate canonical restore evidence",
                "Remove isolated rehearsal data and cache",
            )
        )
        self.assertIn("== 'restore-evidence-verified'", tasks)

    def test_cleanup_targets_only_rehearsal_scoped_paths(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertEqual(
            role_dependencies("storage_backup_cleanup_restore"),
            ["storage_backup_common"],
        )
        self.assertIn("storage_backup_restore_root", tasks)
        self.assertIn("/mnt/fire-fuse-restores", defaults)
        self.assertIn("/var/cache/fire-fuse-restores", defaults)
        self.assertNotIn("/workspace", tasks)
        self.assertNotIn("/var/lib/fire-fuse/minio", tasks)
        self.assertNotIn("/var/lib/fire-fuse/redis", tasks)
        self.assertIn(
            "storage_backup_cleanup_restore_mount_state.rc != 32",
            tasks,
        )

    def test_cleanup_preserves_canonical_evidence(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("'canonical_evidence_preserved': true", tasks)
        self.assertIn(
            "Revalidate preserved canonical evidence after cleanup",
            tasks,
        )
        self.assertNotIn(
            'path: "{{ storage_backup_evidence_root }}"',
            tasks,
        )

    def test_cleanup_records_production_service_guard(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire-fuse-redis.service", tasks)
        self.assertIn("fire-fuse-minio.service", tasks)
        self.assertIn("'production_services_unchanged': true", tasks)


if __name__ == "__main__":
    unittest.main()
