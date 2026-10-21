from pathlib import Path
import unittest

from support import module_args, task


ROLE_NAME = "storage_backup_capture_restore_configuration"
ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_capture_restore_configuration"
)


class CaptureRestoreConfigurationRoleTest(unittest.TestCase):
    def test_capture_role_declares_fixed_storage_identities(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        for declaration in (
            "redis_metadata_database: 1",
            "minio_object_root_user: firefuseadmin",
            "minio_object_juicefs_access_key: juicefs",
            "minio_object_bucket: fire-fuse-workspaces",
        ):
            self.assertIn(declaration, defaults)

    def test_credential_validation_remains_redacted(self) -> None:
        secret_task = task(ROLE_NAME, "Validate encrypted restore-configuration secrets")
        self.assertIs(secret_task["no_log"], True)
        checks = " ".join(module_args(secret_task, "assert")["that"])
        self.assertIn("redis_metadata_password", checks)
        self.assertIn("minio_object_root_password", checks)
        self.assertIn("minio_object_juicefs_secret_key", checks)

    def test_ciphertext_copy_disables_automatic_vault_decryption(self) -> None:
        install_task = task(
            ROLE_NAME, "Install the verified encrypted restore configuration"
        )
        self.assertIs(install_task["no_log"], True)
        self.assertIs(install_task["diff"], False)
        self.assertIs(module_args(install_task, "copy")["decrypt"], False)


if __name__ == "__main__":
    unittest.main()
