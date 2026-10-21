from pathlib import Path
import unittest

from support import module_args, task


ROLE = Path(__file__).parents[1] / "roles" / "compute" / "fire_fuse_qualify"


class FireFuseQualifyRoleTest(unittest.TestCase):
    def test_requires_explicit_content_addressed_build(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn('fire_fuse_qualify_build_id: ""', defaults)
        self.assertIn("is match('^sha256:[0-9a-f]{64}$')", tasks)
        self.assertIn(
            "--setenv=FIRE_FUSE_INTEGRATION_BUILD_ID={{",
            tasks,
        )

    def test_checks_the_internal_sandbox_inventory_before_stopping_runtime(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "fire_fuse_qualify_control_token_file: /etc/fire-fuse/control-clients/operator.token",
            defaults,
        )
        self.assertIn("Require the trusted control-plane token", tasks)
        self.assertIn("Read the control-plane token", tasks)
        inventory_task = task(
            "fire_fuse_qualify", "Inventory live Sandboxes before qualification"
        )
        self.assertIs(inventory_task["no_log"], True)
        self.assertEqual(
            module_args(inventory_task, "uri")["headers"],
            {"Authorization": "Bearer {{ fire_fuse_qualify_control_token }}"},
        )
        self.assertIn("fire_fuse_qualify_listen_port }}/v1/sandboxes", tasks)
        self.assertNotIn("fire_fuse_qualify_listen_port }}/v1/sessions", tasks)
        self.assertIn("fire_fuse_qualify_sandbox_inventory.json['items']", tasks)

    def test_runs_in_an_isolated_delegated_systemd_service(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire_fuse_qualify_systemd_run: /usr/bin/systemd-run", defaults)
        self.assertIn("--service-type=exec", tasks)
        self.assertIn("--property=Delegate=yes", tasks)
        self.assertIn("--unit=fire-fuse-qualification", tasks)
        self.assertIn("--setenv=FIRE_FUSE_INTEGRATION=1", tasks)
        self.assertIn("--setenv=FIRE_FUSE_INTEGRATION_SYSTEMD_RUN={{", tasks)

    def test_restores_companion_services_stopped_with_the_supervisor(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire_fuse_qualify_dependent_services:", defaults)
        self.assertIn("fire-fuse-gateway.service", defaults)
        self.assertNotIn("fire-fuse-tenant-api.service", defaults)
        self.assertIn("fire-fuse-observer.service", defaults)
        self.assertNotIn("cantelop-control-worker.service", defaults)
        self.assertIn("Record active dependent services before qualification", tasks)
        self.assertIn("Restore dependent services that qualification stopped", tasks)
        self.assertIn("fire_fuse_qualify_dependent_restores.results", tasks)


if __name__ == "__main__":
    unittest.main()
