from pathlib import Path
import unittest


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_start_minio_restore"
)


class IsolatedMinioRestoreRoleTest(unittest.TestCase):
    def test_unit_is_loopback_only_and_uses_the_extracted_data_bind(self) -> None:
        unit = (
            ROLE / "templates" / "fire-fuse-restore-minio.service.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("--address 127.0.0.1:", unit)
        self.assertIn("--console-address 127.0.0.1:", unit)
        self.assertIn("IPAddressDeny=any", unit)
        self.assertIn(
            "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
            unit,
        )
        self.assertIn("IPAddressAllow=localhost", unit)
        self.assertIn(
            "BindPaths={{ storage_backup_start_minio_restore_data_directory }}:",
            unit,
        )
        self.assertNotIn("169.254.100.1", unit)
        self.assertNotIn("/var/lib/fire-fuse/minio", unit)

    def test_unit_runs_unprivileged_and_is_not_restart_persistent(self) -> None:
        unit = (
            ROLE / "templates" / "fire-fuse-restore-minio.service.j2"
        ).read_text(encoding="utf-8")

        self.assertIn(
            "User={{ storage_backup_start_minio_restore_user }}",
            unit,
        )
        self.assertIn("NoNewPrivileges=yes", unit)
        self.assertIn("ProtectSystem=strict", unit)
        self.assertIn("Restart=no", unit)

    def test_credentials_template_contains_no_embedded_secret(self) -> None:
        environment = (
            ROLE / "templates" / "minio-restore.env.j2"
        ).read_text(encoding="utf-8")

        self.assertEqual(
            environment,
            "MINIO_ROOT_USER={{ storage_backup_start_minio_restore_root_user }}\n"
            "MINIO_ROOT_PASSWORD={{ "
            "storage_backup_start_minio_restore_root_password }}\n",
        )


if __name__ == "__main__":
    unittest.main()
