from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]


class BackupRegexBackreferenceTest(unittest.TestCase):
    def test_backup_roles_use_ansible_capture_group_syntax(self) -> None:
        task_files = (
            "storage_backup_capture_redis",
            "storage_backup_capture_minio",
            "storage_backup_capture_metadata",
            "storage_backup_qualify_restore_mount",
            "storage_backup_restore_redis_snapshot",
            "storage_backup_restore_metadata",
        )
        for role in task_files:
            tasks = (
                ANSIBLE / "roles" / "storage" / role / "tasks" / "main.yml"
            ).read_text(encoding="utf-8")
            with self.subTest(role=role):
                self.assertTrue(
                    "regex_search(" in tasks or "regex_replace(" in tasks
                )
                self.assertIn("'\\1'", tasks)
                self.assertNotIn(", '\\\\1')", tasks)
                self.assertNotIn(r"\\1", tasks)
                self.assertNotIn(r"\\.", tasks)
                self.assertNotIn(r"\\r", tasks)
                self.assertNotIn(r"\\n", tasks)


if __name__ == "__main__":
    unittest.main()
