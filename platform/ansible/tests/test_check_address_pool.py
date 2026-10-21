from __future__ import annotations

import hashlib
import ipaddress
from pathlib import Path
import unittest

from support import load_module


SCRIPT = (
    Path(__file__).parents[1]
    / "roles"
    / "compute" / "fire_fuse_stage"
    / "files"
    / "check-address-pool.py"
)
CHECKER = load_module(SCRIPT, "check_address_pool")


class AddressPoolTest(unittest.TestCase):
    def test_parse_candidate_requires_canonical_private_ipv4_pool(self) -> None:
        self.assertEqual(
            CHECKER.parse_candidate("172.31.0.0/16"),
            ipaddress.ip_network("172.31.0.0/16"),
        )
        for value in (
            "172.31.0.1/16",
            "172.31.0.0/31",
            "192.0.2.0/24",
            "2001:db8::/64",
        ):
            with self.subTest(value=value), self.assertRaises(ValueError):
                CHECKER.parse_candidate(value)

    def test_owned_taps_require_derived_name_and_alias(self) -> None:
        vm_id = "vm-test"
        expected = "ff" + hashlib.sha256(vm_id.encode()).hexdigest()[:13]
        links = [
            {"ifname": expected, "ifalias": f"fire-fuse:{vm_id}"},
            {"ifname": "ff0000000000000", "ifalias": f"fire-fuse:{vm_id}"},
            {"ifname": "ff1111111111111", "ifalias": "someone-else:vm-test"},
            {"ifname": "ff-storage0", "ifalias": "fire-fuse:storage"},
        ]
        self.assertEqual(CHECKER.owned_tap_names(links), {expected})

    def test_conflicts_ignore_default_and_owned_tap_routes_only(self) -> None:
        candidate = ipaddress.ip_network("172.31.0.0/16")
        owned_tap = "ff123456789abcd"
        routes = [
            {"dst": "default", "dev": "eth0"},
            {"dst": "10.0.0.0/8", "dev": "eth0"},
            {"dst": "172.31.0.0/30", "dev": owned_tap},
            {"dst": "172.31.0.1/32", "dev": owned_tap, "table": "local"},
        ]
        self.assertEqual(
            CHECKER.find_conflicts(candidate, routes, {owned_tap}),
            [],
        )

    def test_conflicts_report_host_vpn_container_and_spoofed_tap_routes(self) -> None:
        candidate = ipaddress.ip_network("172.31.0.0/16")
        routes = [
            {"dst": "172.31.0.0/24", "dev": "docker0"},
            {"dst": "172.31.2.0/24", "dev": "wg0", "table": 100},
            {"dst": "172.31.3.0/30", "dev": "ff0000000000000"},
            {"dst": "172.31.4.9/32", "type": "blackhole"},
        ]
        conflicts = CHECKER.find_conflicts(candidate, routes, set())
        self.assertEqual(len(conflicts), 4)
        self.assertTrue(any("docker0" in conflict for conflict in conflicts))
        self.assertTrue(any("wg0" in conflict for conflict in conflicts))
        self.assertTrue(any("ff0000000000000" in conflict for conflict in conflicts))
        self.assertTrue(any("blackhole" in conflict for conflict in conflicts))


if __name__ == "__main__":
    unittest.main()
