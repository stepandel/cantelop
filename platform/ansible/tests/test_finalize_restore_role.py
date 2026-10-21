from pathlib import Path
import unittest

from support import enclosing_block, module_args

ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_finalize_restore"
)


class FinalizeRestoreRoleTest(unittest.TestCase):
    def test_requires_every_qualified_restore_stage(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        for status in (
            "materialized-and-verified",
            "production-guard-captured",
            "minio-recovery-bucket-qualified",
            "isolated-redis-snapshot-qualified",
            "juicefs-metadata-restored",
            "juicefs-mount-remount-qualified",
        ):
            self.assertIn(status, tasks)

    def test_compares_stable_production_guard(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("== storage_backup_finalize_restore_guard_before.guard", tasks)
        self.assertIn(
            "== storage_backup_finalize_restore_guard_before.guard_sha256",
            tasks,
        )
        self.assertIn("to_json(sort_keys=true)", tasks)

    def test_validates_and_removes_rejected_evidence(self) -> None:
        guarded = enclosing_block(
            "storage_backup_finalize_restore", "Validate canonical restore evidence"
        )
        self.assertEqual(
            [entry["name"] for entry in guarded["rescue"]],
            [
                "Remove rejected canonical restore evidence",
                "Fail rejected restore finalization",
            ],
        )
        removal = module_args(guarded["rescue"][0], "file")
        self.assertEqual(
            removal,
            {
                "path": "{{ storage_backup_finalize_restore_evidence_path }}",
                "state": "absent",
            },
        )
        self.assertIn("ansible.builtin.fail", guarded["rescue"][1])

    def test_finalizer_never_accesses_production_workspace(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertNotIn("/workspace", tasks)


if __name__ == "__main__":
    unittest.main()
