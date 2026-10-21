from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from support import load_script


REPORT = load_script("report-vm-warmup.py")


def summary(*, series_id: str = "series-one") -> dict[str, object]:
    return {
        "schema_version": 1,
        "series_id": series_id,
        "measured_at": "2026-07-29T20:00:00Z",
        "trials": 3,
        "compatibility": {
            "host": "staging",
            "profile": "ubuntu-24",
            "vm": {"vcpus": 1, "memory_mib": 512},
        },
        "lifecycle": {
            "startup_duration_ns": {
                "samples": 3,
                "min": 3_000_000_000,
                "median": 3_100_000_000,
                "p95_nearest_rank": 3_200_000_000,
                "max": 3_200_000_000,
            }
        },
    }


def detailed_summary() -> dict[str, object]:
    document = summary()
    document["measured_at"] = "2026-07-30T20:00:00Z"
    distribution = {
        "samples": 3,
        "min": 1_000_000_000,
        "median": 1_100_000_000,
        "p95_nearest_rank": 1_200_000_000,
        "max": 1_200_000_000,
    }
    document["warmup_trace"] = {
        "total_ns": distribution,
        "steps": [
            {
                "name": "jail.copy_platform_rootfs",
                "occurrences_per_trial": 1,
                "started_offset_ns": {
                    "samples": 3,
                    "min": 0,
                    "median": 0,
                    "p95_nearest_rank": 0,
                    "max": 0,
                },
                "duration_ns": {
                    "samples": 3,
                    "min": 400_000_000,
                    "median": 500_000_000,
                    "p95_nearest_rank": 600_000_000,
                    "max": 600_000_000,
                },
            }
        ],
        "guest_milestones": [
            {
                "name": "workspace_mounted",
                "guest_uptime_ns": {
                    "samples": 3,
                    "min": 800_000_000,
                    "median": 900_000_000,
                    "p95_nearest_rank": 1_000_000_000,
                    "max": 1_000_000_000,
                },
            }
        ],
    }
    return document


class ReportVMWarmupTests(unittest.TestCase):
    def write_summary(
        self, directory: Path, document: dict[str, object], name: str
    ) -> Path:
        path = directory / name
        path.write_text(json.dumps(document), encoding="utf-8")
        return path

    def test_loads_and_renders_tracked_summary(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(root, summary(), "2026-07-29-staging-series-one.json")

            entries = REPORT.load_history(root)
            rendered = REPORT.render_markdown(entries)

        self.assertEqual(len(entries), 1)
        self.assertIn("2026-07-29", rendered)
        self.assertIn("1 vCPU / 512 MiB", rendered)
        self.assertIn("3.100 s", rendered)
        self.assertIn("3.200 s", rendered)

    def test_skips_retired_request_execution_summaries(self) -> None:
        retired = {
            "schema_version": 1,
            "benchmark": "cold-request-execution-lifecycle",
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(root, retired, "2026-07-28-retired.json")
            self.write_summary(root, summary(), "2026-07-29-staging-series-one.json")

            entries = REPORT.load_history(root)

        self.assertEqual([entry.series_id for entry in entries], ["series-one"])

    def test_renders_snapshot_image_identity(self) -> None:
        document = summary()
        document["compatibility"]["image_id"] = "sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(
                root,
                document,
                "2026-07-29-staging-series-image.json",
            )

            entries = REPORT.load_history(root)
            rendered = REPORT.render_markdown(entries)

        self.assertEqual(entries[0].image_id, "sha256:" + "a" * 64)
        self.assertIn("`sha256:aaaaaaaaaaaa`", rendered)

    def test_renders_latest_detailed_host_and_guest_breakdown(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(
                root,
                detailed_summary(),
                "2026-07-30-staging-series-detailed.json",
            )

            rendered = REPORT.render_markdown(REPORT.load_history(root))

        self.assertIn("Latest detailed breakdown", rendered)
        self.assertIn("`jail.copy_platform_rootfs`", rendered)
        self.assertIn("45.5%", rendered)
        self.assertIn("`workspace_mounted`", rendered)
        self.assertIn("0.900 s", rendered)

    def test_renders_host_breakdown_without_guest_milestones(self) -> None:
        document = detailed_summary()
        del document["warmup_trace"]["guest_milestones"]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(
                root,
                document,
                "2026-07-30-staging-series-assigned.json",
            )

            rendered = REPORT.render_markdown(REPORT.load_history(root))

        self.assertIn("`jail.copy_platform_rootfs`", rendered)
        self.assertNotIn("Guest milestone", rendered)

    def test_uses_measurement_timestamp_to_select_latest_same_day_trace(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            earlier = detailed_summary()
            earlier["series_id"] = "series-z"
            earlier["measured_at"] = "2026-07-30T20:00:00Z"
            earlier["warmup_trace"]["steps"][0]["name"] = "earlier.step"
            later = detailed_summary()
            later["series_id"] = "series-a"
            later["measured_at"] = "2026-07-30T21:00:00Z"
            later["warmup_trace"]["steps"][0]["name"] = "later.step"
            self.write_summary(root, earlier, "2026-07-30-earlier.json")
            self.write_summary(root, later, "2026-07-30-later.json")

            rendered = REPORT.render_markdown(REPORT.load_history(root))

        self.assertIn("`later.step`", rendered)
        self.assertNotIn("`earlier.step`", rendered)

    def test_accepts_fractional_medians_from_even_trial_counts(self) -> None:
        document = detailed_summary()
        document["trials"] = 2
        document["lifecycle"]["startup_duration_ns"]["samples"] = 2
        document["lifecycle"]["startup_duration_ns"]["median"] = 3_050_000_000.5
        document["warmup_trace"]["total_ns"]["samples"] = 2
        document["warmup_trace"]["total_ns"]["median"] = 1_050_000_000.5
        step = document["warmup_trace"]["steps"][0]
        step["started_offset_ns"]["samples"] = 2
        step["started_offset_ns"]["median"] = 0.5
        step["duration_ns"]["samples"] = 2
        step["duration_ns"]["median"] = 500_000_000.5
        milestone = document["warmup_trace"]["guest_milestones"][0]
        milestone["guest_uptime_ns"]["samples"] = 2
        milestone["guest_uptime_ns"]["median"] = 900_000_000.5

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(
                root,
                document,
                "2026-07-30-staging-series-even.json",
            )
            rendered = REPORT.render_markdown(REPORT.load_history(root))

        self.assertIn("3.050 s", rendered)
        self.assertIn("0.500 s", rendered)

    def test_rejects_sample_count_that_does_not_match_trials(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            document = summary()
            document["lifecycle"]["startup_duration_ns"]["samples"] = 2
            source = self.write_summary(
                root, document, "2026-07-29-staging-series-one.json"
            )

            with self.assertRaisesRegex(
                REPORT.HistoryError, "startup samples .* do not match trials"
            ):
                REPORT.load_entry(source)

    def test_rejects_duplicate_series(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_summary(root, summary(), "2026-07-29-first.json")
            self.write_summary(root, summary(), "2026-07-29-second.json")

            with self.assertRaisesRegex(REPORT.HistoryError, "duplicate benchmark"):
                REPORT.load_history(root)


if __name__ == "__main__":
    unittest.main()
