from pathlib import Path
import unittest

from support import runs_before


ROLE = Path(__file__).parents[1] / "roles" / "compute" / "fire_fuse_activate"


class FireFuseActivateRoleTest(unittest.TestCase):
    def test_recovers_enabled_services_stopped_with_the_supervisor(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire_fuse_activate_dependent_services:", defaults)
        self.assertIn("fire-fuse-gateway.service", defaults)
        self.assertNotIn("  - fire-fuse-tenant-api.service", defaults)
        self.assertIn("fire-fuse-observer.service", defaults)
        self.assertNotIn("cantelop-control-worker.service", defaults)
        self.assertIn("Record enabled dependent services before activation", tasks)
        self.assertIn("Start enabled services that depend on fire-fuse", tasks)
        self.assertIn("Require dependent services to be running after activation", tasks)
        self.assertTrue(
            runs_before(
                "fire_fuse_activate",
                "Wait for fire-fuse readiness",
                "Start enabled services that depend on fire-fuse",
            )
        )

    def test_requires_lifecycle_state_validation_before_activation(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("Validate lifecycle state before activation", tasks)
        self.assertIn("- -check-state-only", tasks)
        self.assertIn("when: not fire_fuse_activate_was_running", tasks)
        self.assertNotIn("- -preflight-only", tasks)
        self.assertNotIn("legacy", tasks)


if __name__ == "__main__":
    unittest.main()
