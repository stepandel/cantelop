from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]
ROLE = ANSIBLE / "roles" / "shared" / "netdata_dashboard"


class NetdataDashboardRoleTest(unittest.TestCase):
    def test_release_is_checksum_pinned_and_auto_updates_are_masked(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("netdata_dashboard_release: v2.10.2", defaults)
        self.assertIn(
            "sha256:6ebea70b9e9782f298b95246823b22e0d2669ad4c3705da830c47531c0a5009d",
            defaults,
        )
        self.assertIn("checksum: \"{{ netdata_dashboard_installer_checksum }}\"", tasks)
        self.assertIn("name: netdata-updater.timer", tasks)
        self.assertIn("masked: true", tasks)

    def test_dashboard_is_loopback_only_and_resource_bounded(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        config = (ROLE / "templates" / "netdata.conf.j2").read_text(
            encoding="utf-8"
        )
        charts_config = (
            ROLE / "templates" / "charts.d.conf.j2"
        ).read_text(encoding="utf-8")
        limits = (
            ROLE / "templates" / "resource-limits.conf.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("netdata_dashboard_bind_address: 127.0.0.1", defaults)
        self.assertIn(
            "allow connections from = localhost 127.0.0.1 ::1", config
        )
        self.assertIn("allow connections by dns = no", config)
        self.assertIn("storage tiers = 1", config)
        self.assertIn("enable stock health configuration = no", config)
        self.assertIn("enabled = no", config)
        self.assertIn('enable_all_charts="no"', charts_config)
        self.assertIn('fire_fuse="yes"', charts_config)
        self.assertIn("CPUQuota=", limits)
        self.assertIn("MemoryMax=", limits)
        self.assertIn("IOWeight=", limits)

    def test_collector_uses_only_sanitized_aggregate_state(self) -> None:
        collector = (
            ROLE / "templates" / "fire_fuse.chart.sh.j2"
        ).read_text(encoding="utf-8")

        for state in ("redis.json", "minio.json", "juicefs.json"):
            self.assertIn(state, collector)
        self.assertNotIn("machine_id", collector)
        self.assertNotIn("guest_ip", collector)
        self.assertNotIn("redis-password", collector)
        self.assertNotIn("minio.env", collector)

    def test_dashboard_remains_a_separate_playbook(self) -> None:
        dashboard = (
            ANSIBLE / "playbooks" / "shared" / "dashboard.yml"
        ).read_text(encoding="utf-8")
        storage = (
            ANSIBLE / "playbooks" / "storage" / "storage.yml"
        ).read_text(encoding="utf-8")
        activation = (
            ANSIBLE / "playbooks" / "compute" / "activate-runtime.yml"
        ).read_text(encoding="utf-8")

        self.assertIn("role: netdata_dashboard", dashboard)
        self.assertNotIn("netdata_dashboard", storage)
        self.assertNotIn("netdata_dashboard", activation)

    def test_each_machine_streams_to_the_compute_parent(self) -> None:
        dashboard = (
            ANSIBLE / "playbooks" / "shared" / "dashboard.yml"
        ).read_text(encoding="utf-8")
        stream_check = (
            ANSIBLE / "playbooks" / "shared" / "dashboard-verify-streams.yml"
        ).read_text(encoding="utf-8")
        config = (ROLE / "templates" / "netdata.conf.j2").read_text(
            encoding="utf-8"
        )
        stream = (ROLE / "templates" / "stream.conf.j2").read_text(
            encoding="utf-8"
        )
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("hosts: fire_fuse_hosts", dashboard)
        self.assertIn("netdata_dashboard_node: compute", dashboard)
        self.assertIn("hosts: storage_hosts:control_builder_hosts", dashboard)
        self.assertIn("'storage' if 'storage_hosts' in group_names else 'builder'", dashboard)
        self.assertIn("import_playbook: dashboard-verify-streams.yml", dashboard)
        self.assertIn("/api/v1/chart?chart=system.cpu", stream_check)
        self.assertIn("time.time() - last_entry", stream_check)
        # Only the WireGuard listeners accept streams, and only from the
        # two peers; dashboards stay on loopback everywhere.
        self.assertIn(":{{ netdata_dashboard_port }}=streaming", config)
        self.assertIn(
            "allow dashboard from = localhost 127.0.0.1 ::1", config
        )
        self.assertIn("allow streaming from = none", config)
        self.assertIn(
            "allow from = {{ netdata_dashboard_child_addresses", stream
        )
        self.assertIn("health enabled by default = no", stream)
        self.assertIn("no_log: true", tasks)

    def test_collector_splits_services_by_machine(self) -> None:
        collector = (
            ROLE / "templates" / "fire_fuse.chart.sh.j2"
        ).read_text(encoding="utf-8")
        compute, rest = collector.split(
            "{% elif netdata_dashboard_node == 'storage' %}"
        )
        storage, builder = rest.split(
            "{% elif netdata_dashboard_node == 'builder' %}"
        )

        self.assertIn("juicefs.json", compute)
        self.assertNotIn("redis.json", compute)
        self.assertIn("redis.json", storage)
        self.assertIn("minio.json", storage)
        self.assertNotIn("juicefs.json", storage)
        self.assertIn("cantelop-control-worker.service", builder)
        self.assertIn("cantelop-build-materializer.service", builder)


if __name__ == "__main__":
    unittest.main()
