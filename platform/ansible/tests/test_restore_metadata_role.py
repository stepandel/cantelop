from pathlib import Path
import unittest


ROLE = (
    Path(__file__).parents[1]
    / "roles"
    / "storage" / "storage_backup_restore_metadata"
)


class RestoreMetadataRoleTest(unittest.TestCase):
    def test_ubuntu_multicall_server_symlink_is_narrowly_trusted(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "storage_backup_restore_metadata_redis_server_target: "
            "/usr/bin/redis-check-rdb",
            defaults,
        )
        self.assertIn("item.item.expected == 'redis_server'", tasks)
        self.assertIn(
            "== storage_backup_restore_metadata_redis_server_target",
            tasks,
        )

    def test_redis_unit_is_isolated_and_loopback_only(self) -> None:
        unit = (
            ROLE
            / "templates"
            / "fire-fuse-restore-metadata-redis.service.j2"
        ).read_text(encoding="utf-8")
        config = (
            ROLE / "templates" / "metadata-redis.conf.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("bind 127.0.0.1", config)
        self.assertIn("IPAddressDeny=any", unit)
        self.assertIn("IPAddressAllow=localhost", unit)
        self.assertIn("NoNewPrivileges=yes", unit)
        self.assertIn("Restart=no", unit)
        self.assertNotIn("169.254.100.1", unit + config)
        self.assertNotIn("/var/lib/fire-fuse/redis", unit + config)

    def test_role_loads_binary_dump_before_configuring_recovery_bucket(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        load = tasks.index("- load")
        binary = tasks.index("- --binary", load)
        configure = tasks.index("- config", binary)
        bucket = tasks.index("- --bucket", configure)
        self.assertLess(load, binary)
        self.assertLess(binary, configure)
        self.assertLess(configure, bucket)

    def test_role_never_embeds_production_storage_locations(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertNotIn("169.254.100.1", tasks)
        self.assertNotIn("/var/lib/fire-fuse/minio", tasks)
        self.assertNotIn("redis://:", tasks)

    def test_metadata_dump_checksum_is_selected_by_path(self) -> None:
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("'item.path'", tasks)
        self.assertIn("storage_backup_restore_metadata_dump", tasks)
        self.assertNotIn(
            "storage_backup_restore_metadata_paths.results[", tasks
        )


if __name__ == "__main__":
    unittest.main()
