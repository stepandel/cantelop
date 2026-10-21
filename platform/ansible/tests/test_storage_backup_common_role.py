import re
import unittest

from support import (
    ROLES,
    SCRIPTS,
    module_args,
    role_dependencies,
    role_vars,
    task,
    tasks,
    tasks_text,
)


COMMON = "storage_backup_common"
SHARED = re.compile(
    r"storage_backup_(staging|completed|evidence|restore)_root"
    r"|storage_backup_script_source|storage_backup_program_directory"
    r"|tasks_from: install_programs"
)
FIXED = {
    "storage_backup_staging_root": "/var/lib/fire-fuse/storage-backups/staging",
    "storage_backup_completed_root": "/var/lib/fire-fuse/storage-backups/completed",
    "storage_backup_evidence_root": "/var/lib/fire-fuse/storage-backups/restores",
    "storage_backup_restore_root": "/var/lib/fire-fuse/storage-restores/staging",
    "storage_backup_program_directory": "/usr/local/lib/fire-fuse",
}


def storage_roles():
    return sorted(path.name for path in (ROLES / "storage").iterdir() if path.is_dir())


class StorageBackupCommonRoleTest(unittest.TestCase):
    def test_shared_paths_are_role_vars_checked_against_extra_vars(self) -> None:
        constants = role_vars(COMMON, "vars")
        self.assertEqual(role_vars(COMMON, "defaults"), {})
        for name, value in FIXED.items():
            self.assertEqual(constants[name], value)
        guard = task(COMMON, "Require the fixed storage backup paths")
        for name, value in FIXED.items():
            self.assertIn(f"{name} == '{value}'", guard["ansible.builtin.assert"]["that"])

    def test_roles_using_shared_paths_depend_on_the_common_role(self) -> None:
        for role in storage_roles():
            if role == COMMON:
                continue
            with self.subTest(role=role):
                text = tasks_text(role)
                uses_shared = SHARED.search(text) is not None
                self.assertEqual(COMMON in role_dependencies(role), uses_shared)
                self.assertNotIn("role_path }}/../../../scripts", text)
                for name, value in FIXED.items():
                    self.assertNotIn(f"'{value}'", text, name)

    def test_program_installs_ship_the_shared_script_module(self) -> None:
        module = role_vars(COMMON, "vars")["storage_backup_script_module"]
        self.assertTrue((SCRIPTS / module).is_file())
        install = tasks(COMMON, "install_programs.yml")
        self.assertEqual(
            [entry["name"] for entry in install],
            ["Install the shared storage script module", "Install exact storage programs"],
        )
        module_copy = module_args(install[0], "copy")
        self.assertEqual(
            module_copy["dest"],
            "{{ storage_backup_program_directory }}/{{ storage_backup_script_module }}",
        )
        self.assertEqual(module_copy["mode"], "0644")
        programs = module_args(install[1], "copy")
        self.assertEqual(programs["mode"], "0755")
        self.assertEqual(install[1]["loop"], "{{ storage_backup_common_programs }}")

    def test_every_installed_storage_program_exists(self) -> None:
        for role in storage_roles():
            for entry in tasks(role) if (ROLES / "storage" / role / "tasks" / "main.yml").exists() else []:
                imported = entry.get("ansible.builtin.import_role")
                if not imported or imported.get("tasks_from") != "install_programs":
                    continue
                with self.subTest(role=role, task=entry["name"]):
                    self.assertEqual(imported["name"], COMMON)
                    for program in entry["vars"]["storage_backup_common_programs"]:
                        self.assertTrue((SCRIPTS / program).is_file(), program)


if __name__ == "__main__":
    unittest.main()
