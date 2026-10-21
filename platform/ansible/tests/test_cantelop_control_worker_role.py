from pathlib import Path
import unittest

from support import runs_before


ANSIBLE = Path(__file__).parents[1]
ROLE = ANSIBLE / "roles" / "builder" / "cantelop_control_worker"
UNIT = ANSIBLE.parent / "systemd" / "builder" / "cantelop-control-worker.service"


class CantelopControlWorkerRoleTest(unittest.TestCase):
    def test_role_pins_artifact_and_keeps_credentials_root_managed(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("vault_cantelop_database_url", defaults)
        self.assertIn("vault_cantelop_cloudflare_api_token", defaults)
        self.assertIn("vault_fire_fuse_compute_ingress_token", defaults)
        self.assertNotIn("vault_fire_fuse_control_token", defaults)
        self.assertIn("cantelop_control_worker_binary_checksum", tasks)
        self.assertIn("cantelop_control_migrate_checksum", tasks)
        self.assertIn("Install checksum-pinned Cantelop control migrations", tasks)
        self.assertIn("cantelop_control_migrations_manifest_checksum", tasks)
        self.assertIn("Install the checksum-addressed Drizzle migration bundle", tasks)
        self.assertIn("Refuse a changed checksum-addressed migration bundle", tasks)
        self.assertIn("Check the installed Drizzle migration files in place", tasks)
        self.assertIn("or cantelop_control_migrations_installed_check.rc | default(1) != 0", tasks)
        self.assertIn("Verify every installed Drizzle migration file", tasks)
        self.assertIn("CANTELOP_MIGRATIONS_PATH", tasks)
        self.assertIn("Apply serialized Cantelop control database migrations", tasks)
        self.assertIn("become_user: \"{{ cantelop_control_worker_user }}\"", tasks)
        self.assertTrue(
            runs_before(
                "cantelop_control_worker",
                "Apply serialized Cantelop control database migrations",
                "Apply Cantelop control worker configuration changes",
            )
        )
        self.assertIn("cantelop_control_worker_database_ssl == 'verify-full'", tasks)
        self.assertIn("cantelop_control_worker_compute_ingress_url == 'http://10.77.0.1:8084'", tasks)
        self.assertIn("owner: root", tasks)
        self.assertIn('mode: "0640"', tasks)
        self.assertIn("diff: false", tasks)
        self.assertIn("no_log: true", tasks)

    def test_builder_host_keeps_provisioning_separate_from_worker_rollout(self) -> None:
        playbook = (ANSIBLE / "playbooks" / "builder" / "control-worker.yml").read_text(encoding="utf-8")
        prepare = (ANSIBLE / "playbooks" / "builder" / "prepare-control-builder.yml").read_text(encoding="utf-8")
        host = (ANSIBLE / "builder.yml").read_text(encoding="utf-8")

        self.assertNotIn("role: control_builder", playbook)
        self.assertIn("role: cantelop_control_worker", playbook)
        self.assertIn("role: control_builder", prepare)
        self.assertIn("playbooks/builder/prepare-control-builder.yml", host)
        self.assertIn("playbooks/builder/control-worker.yml", host)

    def test_unit_validates_configuration_and_is_hardened(self) -> None:
        unit = UNIT.read_text(encoding="utf-8")

        self.assertIn("User=cantelop-control", unit)
        self.assertIn("EnvironmentFile=/etc/cantelop-control/worker.env", unit)
        self.assertIn("ExecStartPre=/usr/local/bin/cantelop-control-worker --check", unit)
        self.assertIn("Requires=wg-quick@wg0.service cantelop-build-materializer.service", unit)
        self.assertNotIn("PartOf=fire-fuse.service", unit)
        self.assertIn("ProtectSystem=strict", unit)
        self.assertIn("ProtectHome=yes", unit)
        self.assertIn("NoNewPrivileges=yes", unit)
        self.assertNotIn("0.0.0.0", unit)

    def test_role_qualifies_private_provider_boundary_and_service(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn('url: "{{ cantelop_control_worker_compute_ingress_url }}/healthz"', tasks)
        self.assertIn('Authorization: "Bearer {{ cantelop_control_worker_compute_control_token }}"', tasks)
        self.assertIn("cantelop_control_worker_compute_health.json.status == 'ok'", tasks)
        self.assertIn("is-active", tasks)


if __name__ == "__main__":
    unittest.main()
