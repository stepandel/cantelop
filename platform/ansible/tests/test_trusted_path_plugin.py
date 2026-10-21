import re
import unittest

from support import ANSIBLE_ROOT, PLAYBOOKS, ROLES, load_module


PLUGIN = load_module(ANSIBLE_ROOT / "test_plugins" / "trusted_path.py")


def stat(**overrides):
    value = {
        "exists": True,
        "isreg": True,
        "isdir": False,
        "islnk": False,
        "pw_name": "root",
        "gr_name": "root",
        "mode": "0600",
    }
    value.update(overrides)
    return value


class TrustedPathPluginTest(unittest.TestCase):
    def test_plugin_is_enabled_for_every_playbook(self) -> None:
        config = (ANSIBLE_ROOT / "ansible.cfg").read_text(encoding="utf-8")
        self.assertIn("test_plugins = test_plugins", config)
        self.assertEqual(
            set(PLUGIN.TestModule().tests()), {"trusted_file", "trusted_directory"}
        )

    def test_trusted_file_requires_an_existing_nonsymlinked_regular_file(self) -> None:
        self.assertTrue(PLUGIN.trusted_file(stat()))
        self.assertFalse(PLUGIN.trusted_file({"exists": False}))
        self.assertFalse(PLUGIN.trusted_file(stat(islnk=True)))
        self.assertFalse(PLUGIN.trusted_file(stat(isreg=False, isdir=True)))
        self.assertFalse(PLUGIN.trusted_file(None))
        self.assertFalse(PLUGIN.trusted_file({"isreg": True}))

    def test_trusted_directory_requires_an_existing_nonsymlinked_directory(self) -> None:
        directory = stat(isreg=False, isdir=True, mode="0750")
        self.assertTrue(PLUGIN.trusted_directory(directory, mode="0750"))
        self.assertFalse(PLUGIN.trusted_directory(stat()))
        self.assertFalse(PLUGIN.trusted_directory({**directory, "islnk": True}))

    def test_owner_group_and_modes_are_checked_only_when_given(self) -> None:
        candidate = stat(pw_name="minio", gr_name="minio")
        self.assertTrue(PLUGIN.trusted_file(candidate))
        self.assertTrue(PLUGIN.trusted_file(candidate, owner="minio", group="minio"))
        self.assertFalse(PLUGIN.trusted_file(candidate, owner="root"))
        self.assertFalse(PLUGIN.trusted_file(candidate, group="root"))
        self.assertFalse(PLUGIN.trusted_file(candidate, owner=None))
        self.assertTrue(PLUGIN.trusted_file(candidate, mode="0600"))
        self.assertFalse(PLUGIN.trusted_file(candidate, mode="0640"))
        self.assertTrue(PLUGIN.trusted_file(candidate, modes=["0400", "0600"]))
        self.assertFalse(PLUGIN.trusted_file(candidate, modes=["0640"]))
        self.assertFalse(PLUGIN.trusted_file({**candidate, "mode": None}, mode="0600"))

    def test_roles_do_not_repeat_the_negative_symlink_check(self) -> None:
        # Positive symlink checks and find-module entries remain explicit.
        allowed = {
            "storage_backup_restore_redis_snapshot": "and item.stat.islnk",
            "storage_backup_restore_metadata": "and item.stat.islnk",
            "storage_backup_capture_redis": "not (item.islnk",
        }
        pattern = re.compile(r"not \(?\s*[\w.\[\]]+\.stat\.islnk")
        for path in [*ROLES.glob("**/*.yml"), *PLAYBOOKS.glob("**/*.yml")]:
            text = path.read_text(encoding="utf-8")
            with self.subTest(path=path.relative_to(ANSIBLE_ROOT)):
                self.assertIsNone(pattern.search(text))
                if "islnk" in text:
                    role = path.parts[len(ROLES.parts) + 1]
                    self.assertIn(allowed.get(role, "<none>"), text)


if __name__ == "__main__":
    unittest.main()
