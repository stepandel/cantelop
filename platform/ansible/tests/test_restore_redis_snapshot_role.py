from pathlib import Path
import unittest


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_restore_redis_snapshot"
)


class IsolatedRedisSnapshotRoleTest(unittest.TestCase):
    def test_ubuntu_multicall_server_symlink_is_narrowly_trusted(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "storage_backup_restore_redis_snapshot_server_target: "
            "/usr/bin/redis-check-rdb",
            defaults,
        )
        self.assertIn("item.item.expected == 'redis_server'", tasks)
        self.assertIn(
            "== storage_backup_restore_redis_snapshot_server_target",
            tasks,
        )

    def test_unit_is_loopback_sandboxed_and_uses_extracted_bind(self) -> None:
        unit = (
            ROLE / "templates" / "fire-fuse-restore-redis.service.j2"
        ).read_text(encoding="utf-8")
        config = (
            ROLE / "templates" / "redis-restore.conf.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("bind 127.0.0.1", config)
        self.assertIn("IPAddressDeny=any", unit)
        self.assertIn("IPAddressAllow=localhost", unit)
        self.assertIn(
            "BindPaths={{ "
            "storage_backup_restore_redis_snapshot_data_directory }}:",
            unit,
        )
        self.assertNotIn("169.254.100.1", unit)
        self.assertNotIn("/var/lib/fire-fuse/redis", unit)

    def test_unit_is_unprivileged_and_not_restart_persistent(self) -> None:
        unit = (
            ROLE / "templates" / "fire-fuse-restore-redis.service.j2"
        ).read_text(encoding="utf-8")

        self.assertIn(
            "User={{ storage_backup_restore_redis_snapshot_user }}",
            unit,
        )
        self.assertIn("NoNewPrivileges=yes", unit)
        self.assertIn("ProtectSystem=strict", unit)
        self.assertIn("Restart=no", unit)

    def test_config_disables_automatic_rewrites_and_eviction(self) -> None:
        config = (
            ROLE / "templates" / "redis-restore.conf.j2"
        ).read_text(encoding="utf-8")

        self.assertIn('save ""', config)
        self.assertIn("auto-aof-rewrite-percentage 0", config)
        self.assertIn("maxmemory-policy noeviction", config)
        self.assertIn(
            "requirepass {{ storage_backup_restore_redis_snapshot_password }}",
            config,
        )


if __name__ == "__main__":
    unittest.main()
