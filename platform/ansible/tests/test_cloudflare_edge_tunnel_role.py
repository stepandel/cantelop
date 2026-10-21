from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]
ROLE = ANSIBLE / "roles" / "compute" / "cloudflare_edge_tunnel"


class CloudflareEdgeTunnelRoleTest(unittest.TestCase):
    def test_role_pins_artifact_and_keeps_tokens_out_of_arguments(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        unit = (
            ROLE / "templates" / "cloudflared-cantelop-edge.service.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("cloudflare_edge_tunnel_binary_checksum", tasks)
        self.assertIn("stat.checksum | default('')", tasks)
        self.assertIn("Install the private Tunnel token", tasks)
        self.assertIn('mode: "0640"', tasks)
        self.assertIn("no_log: true", tasks)
        self.assertIn("^[A-Za-z0-9_.-]+={0,2}$", tasks)
        self.assertIn("--token-file {{ cloudflare_edge_tunnel_token_file }}", unit)
        self.assertNotIn("{{ cloudflare_edge_tunnel_token }}", unit)
        self.assertIn("127.0.0.1:20241", defaults)

    def test_service_is_unprivileged_outbound_only_and_hardened(self) -> None:
        unit = (
            ROLE / "templates" / "cloudflared-cantelop-edge.service.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("User={{ cloudflare_edge_tunnel_user }}", unit)
        self.assertIn("NoNewPrivileges=yes", unit)
        self.assertIn("ProtectSystem=strict", unit)
        self.assertIn("RestrictAddressFamilies=AF_INET AF_INET6", unit)
        self.assertIn("CapabilityBoundingSet=", unit)
        self.assertNotIn("Listen", unit)

    def test_role_requires_connected_readiness(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("Enable and start the Cantelop edge Tunnel", tasks)
        self.assertIn("Require the edge Tunnel connector to be ready", tasks)
        self.assertIn("/ready", tasks)
        self.assertIn("status_code: 200", tasks)


if __name__ == "__main__":
    unittest.main()
