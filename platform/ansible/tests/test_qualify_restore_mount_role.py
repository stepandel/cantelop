from pathlib import Path
import unittest


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_qualify_restore_mount"
)


class QualifyRestoreMountRoleTest(unittest.TestCase):
    def test_helper_uses_distinct_caches_and_clean_unmounts(self) -> None:
        helper = (
            ROLE / "templates" / "qualify-juicefs-restore-mount.j2"
        ).read_text(encoding="utf-8")

        self.assertIn('mount_volume "${cache_root}/first"', helper)
        self.assertIn('mount_volume "${cache_root}/second"', helper)
        self.assertGreaterEqual(helper.count('unmount_volume'), 3)
        self.assertIn('"${juicefs}" rmr --skip-trash "${marker}"', helper)

    def test_helper_disables_background_metadata_mutations(self) -> None:
        helper = (
            ROLE / "templates" / "qualify-juicefs-restore-mount.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("--backup-meta=0", helper)
        self.assertIn("--no-bgjob", helper)
        self.assertIn("--no-usage-report", helper)

    def test_helper_excludes_mutable_juicefs_control_files(self) -> None:
        helper = (
            ROLE / "templates" / "qualify-juicefs-restore-mount.j2"
        ).read_text(encoding="utf-8")

        for name in (".config", ".stats", ".accesslog", ".control"):
            self.assertIn(f'! -path "${{mountpoint}}/{name}"', helper)

    def test_role_has_no_production_mount_or_storage_paths(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertNotIn("/workspace", tasks)
        self.assertNotIn("/var/lib/fire-fuse/minio", tasks)
        self.assertNotIn("/var/lib/fire-fuse/redis", tasks)
        self.assertNotIn("169.254.100.1", tasks)

    def test_role_binds_metadata_runtime_to_isolated_service(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("'fire-fuse-restore-metadata-redis-'", tasks)
        self.assertIn("'redis://127.0.0.1:16380/1'", tasks)
        self.assertIn("metadata.database == 1", tasks)

    def test_role_verifies_empty_unmounted_mountpoint(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("mountpoint, --quiet", tasks)
        self.assertIn(
            "storage_backup_qualify_restore_mount_unmounted.rc != 32",
            tasks,
        )
        self.assertIn("mountpoint_contents.matched == 0", tasks)


if __name__ == "__main__":
    unittest.main()
