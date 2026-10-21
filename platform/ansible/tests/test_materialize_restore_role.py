from pathlib import Path
import unittest

from support import runs_before


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_materialize_restore"
)


class MaterializeRestoreRoleTest(unittest.TestCase):
    def test_captures_guard_before_materialization(self) -> None:
        self.assertTrue(
            runs_before(
                "storage_backup_materialize_restore",
                "Read production JuiceFS status before restore materialization",
                "Download and verify the recorded object versions",
            )
        )

    def test_guard_contains_only_stable_production_identity(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("metadata_url_sha256:", tasks)
        self.assertIn("volume_name:", tasks)
        self.assertIn("volume_uuid:", tasks)
        self.assertIn("storage:", tasks)
        self.assertIn("bucket:", tasks)
        self.assertNotIn("storage_backup_materialize_restore_guard:\n      Sessions", tasks)

    def test_guard_is_bound_to_the_materialized_manifest(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "== storage_backup_materialize_restore_manifest.source.volume_name",
            tasks,
        )
        self.assertIn(
            "== storage_backup_materialize_restore_manifest.source.volume_uuid",
            tasks,
        )
        self.assertIn("/production-guard-before.json", tasks)


if __name__ == "__main__":
    unittest.main()
