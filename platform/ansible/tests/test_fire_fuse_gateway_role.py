from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]
ROLE = ANSIBLE / "roles" / "compute" / "fire_fuse_gateway"
INVENTORY = ANSIBLE / "inventory" / "staging"


class FireFuseGatewayRoleTest(unittest.TestCase):
    def test_role_pins_artifacts_and_private_edge_boundary(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire_fuse_gateway_edge_token:", defaults)
        self.assertIn("fire_fuse_gateway_config_directory:", defaults)
        self.assertIn("fire_fuse_gateway_edge_token_file:", defaults)
        self.assertIn("vault_fire_fuse_gateway_runtime_token", defaults)
        self.assertIn("vault_fire_fuse_gateway_product_token", defaults)
        self.assertIn("vault_fire_fuse_dispatcher_runtime_token", defaults)
        self.assertIn("vault_fire_fuse_operator_token", defaults)
        self.assertIn("vault_fire_fuse_compute_ingress_token", defaults)
        self.assertIn("vault_fire_fuse_observer_runtime_token", defaults)
        self.assertIn("fire_fuse_gateway_user == 'fire-fuse-gateway'", tasks)
        self.assertIn("fire_fuse_gateway_user != fire_fuse_dispatcher_user", tasks)
        self.assertIn("fire_fuse_observability_ingest_token:", defaults)
        self.assertIn("fire_fuse_observability_ingest_token_file:", defaults)
        self.assertIn("fire_fuse_gateway_binary_checksum", tasks)
        self.assertIn("fire_fuse_async_dispatcher_binary_checksum", tasks)
        self.assertIn("item.stat.checksum | default('') == item.item.checksum", tasks)
        self.assertIn("fire_fuse_gateway_edge_token is match('^[0-9a-f]{64}$')", tasks)
        self.assertIn("Install the private edge ingress credential", tasks)
        self.assertIn("Install the private observability ingestion credential", tasks)
        self.assertIn("Install the private gateway runtime credential", tasks)
        self.assertIn("Install the private gateway product-control credential", tasks)
        self.assertIn("Install the private dispatcher runtime credential", tasks)
        self.assertIn("fire_fuse_observability_ingest_token != fire_fuse_gateway_edge_token", tasks)
        self.assertIn("| unique | length == 8", tasks)
        self.assertIn('mode: "0640"', tasks)
        self.assertIn("no_log: true", tasks)

    def test_role_has_no_public_ingress_and_qualifies_loopback(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("Require the base fire-fuse service", tasks)
        self.assertIn("http://127.0.0.1:8081/readyz", tasks)
        self.assertNotIn("Install the nginx gateway site", tasks)
        self.assertNotIn("wildcard TLS", tasks)

    def test_packaged_service_is_unprivileged_and_loopback_only(self) -> None:
        unit = (
            Path(__file__).parents[2]
            / "systemd"
            / "compute"
            / "fire-fuse-gateway.service"
        ).read_text(encoding="utf-8")

        self.assertIn("User=fire-fuse-gateway", unit)
        self.assertNotIn("SupplementaryGroups=fire-fuse-control", unit)
        self.assertIn("-listen 127.0.0.1:8081", unit)
        self.assertIn("-control-plane http://127.0.0.1:8080", unit)
        self.assertIn(
            "-edge-token-file /etc/fire-fuse-gateway/edge-ingress.token", unit
        )
        self.assertIn("NoNewPrivileges=yes", unit)

    def test_packaged_dispatcher_is_a_separate_unprivileged_service(self) -> None:
        unit = (
            Path(__file__).parents[2]
            / "systemd"
            / "compute"
            / "fire-fuse-async-dispatcher.service"
        ).read_text(encoding="utf-8")

        self.assertIn("User=fire-fuse-dispatcher", unit)
        self.assertIn("Group=fire-fuse-dispatcher", unit)
        self.assertNotIn("SupplementaryGroups=fire-fuse-control", unit)
        self.assertIn(
            "ExecStart=/usr/local/bin/fire-fuse-async-dispatcher", unit
        )
        self.assertIn("-control-plane http://127.0.0.1:8080", unit)
        self.assertIn(
            "-observability-token-file /etc/fire-fuse-dispatcher/observability-ingest.token",
            unit,
        )
        self.assertIn("StateDirectory=fire-fuse-observability", unit)
        self.assertIn("ReadWritePaths=/var/lib/fire-fuse-observability", unit)
        self.assertIn("NoNewPrivileges=yes", unit)

        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        self.assertIn(
            "Enable and start the fire-fuse asynchronous dispatcher", tasks
        )

    def test_staging_inventory_template_pins_the_dispatcher_artifact(self) -> None:
        inventory = (INVENTORY / "hosts.example.yml").read_text(encoding="utf-8")
        self.assertIn("fire_fuse_async_dispatcher_binary_artifact:", inventory)
        self.assertIn("fire_fuse_async_dispatcher_binary_checksum:", inventory)


if __name__ == "__main__":
    unittest.main()
