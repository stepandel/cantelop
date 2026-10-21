from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[3]
ROLE = ROOT / "platform" / "ansible" / "roles" / "compute" / "hetzner_robot_firewall"


class HetznerRobotFirewallRoleTests(unittest.TestCase):
    def test_policy_blocks_rpc_and_nfs_for_ipv4_and_ipv6(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        input_policy = defaults.split("hetzner_robot_firewall_output_rules:", 1)[0]

        for version in ("ipv4", "ipv6"):
            self.assertEqual(input_policy.count(f"ip_version: {version}"), 4)
        for protocol in ("tcp", "udp"):
            for port in ("111", "2049"):
                self.assertIn(f"protocol: {protocol}\n    dst_port: \"{port}\"", defaults)
        self.assertIn("filter_ipv6: \"true\"", (ROLE / "tasks" / "main.yml").read_text())

    def test_role_reads_before_replace_and_verifies_exact_policy(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertLess(tasks.index("method: GET"), tasks.index("method: POST"))
        self.assertNotIn("cantelop:", defaults)
        self.assertIn("Refuse to overwrite an unmanaged Hetzner Robot firewall", tasks)
        self.assertIn("rules not owned by Cantelop", tasks)
        self.assertIn("hetzner_robot_firewall_input_rules == hetzner_robot_firewall_reviewed_input_rules", tasks)
        self.assertIn("hetzner_robot_firewall_current_input_owned_count", tasks)
        self.assertIn("hetzner_robot_firewall_current_output_owned_count", tasks)
        self.assertIn("until:", tasks)
        self.assertIn("hetzner_robot_firewall_current_rules_normalized != hetzner_robot_firewall_desired_rules_normalized", tasks)
        self.assertIn("firewall.whitelist_hos | default(false) | bool", tasks)
        self.assertIn("hetzner_robot_firewall_current_rules_normalized == hetzner_robot_firewall_legacy_rules_normalized", tasks)

    def test_policy_models_provider_injected_smtp_egress_blocks(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertNotIn('dst_port: "25,465"', defaults)
        self.assertEqual(tasks.count('name: "Block mail ports"'), 4)
        self.assertIn("hetzner_robot_firewall_provider_output_rules_normalized", tasks)
        self.assertIn("hetzner_robot_firewall_current_provider_output_rules", tasks)
        self.assertIn("- 202", tasks)

    def test_credentials_are_secret_and_check_mode_does_not_write(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("vault_hetzner_robot_user", defaults)
        self.assertIn("vault_hetzner_robot_password", defaults)
        self.assertNotIn("lookup('env'", defaults)
        self.assertIn("no_log: true", tasks)
        self.assertIn("check_mode: false", tasks)
        self.assertIn("not ansible_check_mode", tasks)
        self.assertIn("changed_when: hetzner_robot_firewall_has_drift", tasks)


if __name__ == "__main__":
    unittest.main()
