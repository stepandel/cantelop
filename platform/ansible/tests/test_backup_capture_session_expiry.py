from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]


class BackupCaptureSessionExpiryTest(unittest.TestCase):
    def test_capture_gates_on_unexpired_sessions(self) -> None:
        for role in (
            "storage_backup_capture_preflight",
            "storage_backup_capture_metadata",
        ):
            tasks = (
                ANSIBLE / "roles" / "storage" / role / "tasks" / "main.yml"
            ).read_text(encoding="utf-8")
            with self.subTest(role=role):
                self.assertIn(
                    "item.Expire\n            | to_datetime"
                    if role.endswith("preflight")
                    else "item.Expire\n        | to_datetime",
                    tasks,
                )
                self.assertIn("total_seconds() > 0", tasks)
                self.assertIn("active_sessions | length == 0", tasks)
                self.assertNotIn(
                    "status.Sessions | length == 0",
                    tasks,
                )


if __name__ == "__main__":
    unittest.main()
