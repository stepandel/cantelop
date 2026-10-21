from pathlib import Path
import unittest

from support import runs_before, task


ROLE = Path(__file__).parents[1] / "roles" / "compute" / "juicefs_gateway"


class JuiceFSGatewayRoleTest(unittest.TestCase):
    def test_data_only_policy_is_required_on_running_mount(self) -> None:
        tasks = (ROLE / "tasks/main.yml").read_text()
        unit = (ROLE / "templates/fire-fuse-juicefs-gateway.service.j2").read_text()
        self.assertIn("--workspace-data-only", unit)
        # nfsd pages READDIR by offset; JuiceFS only keeps that order stable when sorted.
        self.assertIn("--sort-dir", unit)
        self.assertIn("fuse.juicefs-workspace-v1", tasks)
        self.assertTrue(
            runs_before(
                "juicefs_gateway",
                "Require the running JuiceFS data-only policy",
                "Enforce the trusted mounted JuiceFS root",
            )
        )

    def test_administrative_mount_is_explicitly_root_only(self) -> None:
        tasks = (ROLE / "tasks/main.yml").read_text()
        unit = (ROLE / "templates/fire-fuse-juicefs-admin.service.j2").read_text()
        self.assertIn("--root-only", unit)
        self.assertIn("'allow_other' not in", tasks)

    def test_mountpoints_keep_the_shared_root_mode_idempotent(self) -> None:
        tasks = (ROLE / "tasks/main.yml").read_text()
        self.assertIn(
            'path: "{{ juicefs_gateway_admin_mountpoint }}"\n'
            '      # Both mounts expose the same JuiceFS root after startup. Keep their\n'
            '      # mountpoint mode aligned so a repeat run does not toggle the live root.\n'
            '      mode: "0750"',
            tasks,
        )
        self.assertIn(
            'path: "{{ juicefs_gateway_admin_cache_directory }}"\n'
            '      mode: "0700"',
            tasks,
        )

    def test_maintenance_guards_inventory_both_mount_types(self) -> None:
        roles = ROLE.parents[1] / "storage"
        for role in ("storage_backup_capture_preflight", "storage_backup_capture_metadata",
                     "storage_rotate_redis", "storage_rotate_minio"):
            tasks = (roles / role / "tasks/main.yml").read_text()
            self.assertIn("fuse.juicefs,fuse.juicefs-workspace-v1", tasks)
            self.assertNotIn("- fuse.juicefs\n", tasks)

    def test_mounted_workspace_root_is_hardened(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("- name: Enforce the trusted mounted JuiceFS root", tasks)
        self.assertIn("- name: Require the trusted mounted JuiceFS root", tasks)
        root = task("juicefs_gateway", "Require the trusted mounted JuiceFS root")
        self.assertEqual(
            root["ansible.builtin.assert"]["that"],
            [
                "juicefs_gateway_mountpoint_status.stat "
                "is trusted_directory(owner='root', group='root', mode='0750')"
            ],
        )

    def test_gateway_mount_requires_private_storage_tunnel(self) -> None:
        unit = (ROLE / "templates/fire-fuse-juicefs-gateway.service.j2").read_text()
        self.assertIn("Requires=wg-quick@wg-storage.service", unit)
        self.assertIn("After=wg-quick@wg-storage.service", unit)
        self.assertNotIn("fire-fuse-redis.service", unit)
        self.assertNotIn("fire-fuse-minio.service", unit)

    def test_portmapper_is_firewalled_before_nfs_is_installed(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        firewall = (
            ROLE / "templates" / "configure-nfs-firewall.j2"
        ).read_text(encoding="utf-8")

        self.assertTrue(
            runs_before(
                "juicefs_gateway",
                "Enable and apply the JuiceFS gateway NFS firewall",
                "Install the NFSv4 server behind the firewall",
            )
        )
        self.assertIn(
            "readonly rpcbind_port={{ juicefs_gateway_rpcbind_port | quote }}",
            firewall,
        )
        self.assertIn('for firewall in "${iptables}" "${ip6tables}"', firewall)
        self.assertIn("for protocol in tcp udp", firewall)
        self.assertIn("--destination-port \"${rpcbind_port}\"", firewall)
        self.assertIn("--jump DROP", firewall)
        self.assertIn(
            "readonly rpcbind_loopback_comment=",
            firewall,
        )
        loopback_allow = firewall.index(
            '--comment "${rpcbind_loopback_comment}"'
        )
        self.assertIn("--in-interface lo", firewall[loopback_allow - 200:loopback_allow])
        self.assertIn("--jump ACCEPT", firewall[loopback_allow:loopback_allow + 200])
        staged_drop = firewall.index('append "${next_chain}" --jump DROP')
        preserve_workspace_rules = firewall.index(
            'index($0, ":workspace-nfs */")',
            staged_drop,
        )
        activate_staged = firewall.index(
            'insert INPUT "${jump_position}"',
            preserve_workspace_rules,
        )
        remove_active = firewall.index('delete INPUT', activate_staged)
        self.assertLess(staged_drop, activate_staged)
        self.assertLess(preserve_workspace_rules, activate_staged)
        self.assertLess(activate_staged, remove_active)
        self.assertIn("- name: Require the Portmapper firewall denies", tasks)
        self.assertIn(
            "- name: Require the Portmapper loopback allowances",
            tasks,
        )

    def test_portmapper_is_disabled_for_nfsv4_only_server(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        nfs_config = (
            ROLE / "templates" / "fire-fuse-nfs.conf.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("vers2=n", nfs_config)
        self.assertIn("vers3=n", nfs_config)
        self.assertIn("vers4=y", nfs_config)
        self.assertIn("juicefs_gateway_rpcbind_units:", defaults)
        self.assertIn("  - rpcbind.socket", defaults)
        self.assertIn("  - rpcbind.service", defaults)
        self.assertIn(
            "- name: Stop and mask Portmapper for the NFSv4-only server",
            tasks,
        )
        self.assertIn("masked: true", tasks)
        self.assertIn("- name: Require Portmapper to be masked and inactive", tasks)
        self.assertIn("- name: Require Portmapper to have no network listeners", tasks)


if __name__ == "__main__":
    unittest.main()
