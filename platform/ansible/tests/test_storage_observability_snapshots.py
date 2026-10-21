from pathlib import Path
import unittest


ROLE = Path(__file__).parents[1] / "roles" / "shared" / "storage_observability"


class StorageObservabilitySnapshotsTest(unittest.TestCase):
    def test_backend_snapshots_are_atomic_and_sanitized(self) -> None:
        for name in ("observe-redis.sh.j2", "observe-minio.sh.j2"):
            probe = (ROLE / "templates" / name).read_text(encoding="utf-8")
            self.assertIn('mktemp "${state_file}.XXXXXX"', probe)
            self.assertIn('chmod 0644 "$temporary_file"', probe)
            self.assertIn('mv -f "$temporary_file" "$state_file"', probe)
            self.assertIn("observed_at_epoch", probe)
            self.assertNotIn('"password":', probe)
            self.assertNotIn('"secret":', probe)

    def test_juicefs_snapshot_describes_only_the_host_gateway(self) -> None:
        probe = (
            ROLE / "templates" / "observe-juicefs.sh.j2"
        ).read_text(encoding="utf-8")
        publish = probe.rsplit("publish_state", maxsplit=1)[1]

        self.assertIn('event:"gateway_metrics"', publish)
        self.assertIn("cache_bytes", publish)
        self.assertIn("object_request_errors_total", publish)
        self.assertNotIn("machine_id", publish)
        self.assertNotIn("guest_ip", publish)

    def test_probe_units_recreate_volatile_state_after_boot(self) -> None:
        for name in (
            "fire-fuse-observe-redis.service.j2",
            "fire-fuse-observe-minio.service.j2",
            "fire-fuse-observe-juicefs.service.j2",
        ):
            unit = (ROLE / "templates" / name).read_text(encoding="utf-8")
            self.assertIn("RuntimeDirectory=fire-fuse-observability", unit)
            self.assertIn("RuntimeDirectoryPreserve=yes", unit)
            self.assertIn(
                "ReadWritePaths={{ storage_observability_state_directory }}",
                unit,
            )


if __name__ == "__main__":
    unittest.main()
