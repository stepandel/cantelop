import importlib.util
from pathlib import Path
import unittest

PATH = Path(__file__).parents[1] / "roles/compute/fire_fuse_managed/files/configure.py"
spec = importlib.util.spec_from_file_location("managed_config", PATH)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ManagedInstanceConfigTest(unittest.TestCase):
    def standard(self):
        return {"runtime_dir": "/var/lib/fire-fuse/runtime", "state_db": "/var/lib/fire-fuse/state/state.db",
                "workspace": {"root": "/var/lib/fire-fuse/workspaces", "admin_root": "/var/lib/fire-fuse/workspace-admin"},
                "network": {"address_pool": "172.31.0.0/16"}, "profiles": {"default": {"snapshot_bundle": "immutable"}}}

    def test_disjoint_state_network_and_bounded_capacity(self):
        original = self.standard()
        managed = module.managed_config(original)
        self.assertEqual(managed["network"], {"namespace": "managed", "address_pool": "172.30.0.0/16"})
        self.assertNotEqual(managed["state_db"], original["state_db"])
        self.assertNotEqual(managed["runtime_dir"], original["runtime_dir"])
        self.assertEqual(managed["limits"]["max_vms"], 2)
        self.assertEqual(managed["limits"]["max_total_memory_mib"], 8192)
        self.assertEqual(managed["profiles"], original["profiles"])
        self.assertEqual(original, self.standard())

    def test_refuses_overlapping_network_and_unknown_mounts(self):
        for field, value in [("network", {"address_pool": "172.16.0.0/12"}), ("workspace", {"root": "/unknown", "admin_root": "/unknown"})]:
            config = self.standard()
            config[field] = value
            with self.assertRaises(ValueError):
                module.managed_config(config)

    def test_watchdog_and_runtime_agree_on_durable_state_and_heartbeat(self):
        import shlex
        units = Path(__file__).parents[2] / "systemd/compute"
        runtime = (units / "fire-fuse-managed.service").read_text()
        watchdog = (units / "fire-fuse-fleet-watchdog.service").read_text()
        command = shlex.split(next(line.removeprefix("ExecStart=") for line in watchdog.splitlines() if line.startswith("ExecStart=")))
        flags = dict(zip(command[1::2], command[2::2]))
        self.assertEqual(flags["-state"], module.managed_config(self.standard())["state_db"])
        directory = next(line.removeprefix("RuntimeDirectory=") for line in watchdog.splitlines() if line.startswith("RuntimeDirectory="))
        self.assertEqual(flags["-heartbeat"], "/run/" + directory + "/heartbeat")
        self.assertIn("Environment=CANTELOP_MANAGED_WATCHDOG_HEARTBEAT=" + flags["-heartbeat"], runtime)
        self.assertEqual(flags["-cgroups"], "/sys/fs/cgroup/system.slice/fire-fuse-managed.service/sandboxes")
