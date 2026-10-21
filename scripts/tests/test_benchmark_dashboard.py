from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from support import load_script


DASHBOARD = load_script("benchmark-dashboard.py")


def summary(*, series_id: str = "series-one") -> dict[str, object]:
    return {
        "schema_version": 1,
        "series_id": series_id,
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


class BuildHistoryPayloadTest(unittest.TestCase):
    def write(self, directory: Path, name: str, document: object) -> None:
        (directory / name).write_text(json.dumps(document), encoding="utf-8")

    def test_orders_series_by_date_then_series_id(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            self.write(
                directory,
                "2026-07-30-host-series-b.json",
                summary(series_id="series-b"),
            )
            self.write(
                directory,
                "2026-07-29-host-series-c.json",
                summary(series_id="series-c"),
            )
            self.write(
                directory,
                "2026-07-30-host-series-a.json",
                summary(series_id="series-a"),
            )
            payload = DASHBOARD.build_history_payload(directory)
        self.assertEqual(payload["errors"], [])
        self.assertEqual(
            [entry["series_id"] for entry in payload["series"]],
            ["series-c", "series-a", "series-b"],
        )
        self.assertEqual(
            [entry["measured_on"] for entry in payload["series"]],
            ["2026-07-29", "2026-07-30", "2026-07-30"],
        )

    def test_reports_invalid_files_without_dropping_valid_series(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            self.write(directory, "2026-07-29-host-series-a.json", summary())
            self.write(directory, "no-date-prefix.json", summary())
            self.write(directory, "2026-07-30-host-series-b.json", ["not-a-summary"])
            (directory / "2026-07-31-host-series-c.json").write_text(
                "{broken", encoding="utf-8"
            )
            document = summary(series_id="series-d")
            document["schema_version"] = 2
            self.write(directory, "2026-08-01-host-series-d.json", document)
            payload = DASHBOARD.build_history_payload(directory)
        self.assertEqual(len(payload["series"]), 1)
        self.assertEqual(payload["series"][0]["file"], "2026-07-29-host-series-a.json")
        self.assertEqual(
            sorted(error.split(":", 1)[0] for error in payload["errors"]),
            [
                "2026-07-30-host-series-b.json",
                "2026-07-31-host-series-c.json",
                "2026-08-01-host-series-d.json",
                "no-date-prefix.json",
            ],
        )

    def test_serves_entries_normalized_by_the_report_parser(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            document = summary()
            document["lifecycle"]["shutdown_duration_ns"] = {"median": 600_000_000}
            document["metrics"] = [{"workload": "small-files"}]
            self.write(directory, "2026-07-29-host-series-a.json", document)
            payload = DASHBOARD.build_history_payload(directory)
        self.assertEqual(payload["errors"], [])
        self.assertEqual(
            payload["series"],
            [
                {
                    "file": "2026-07-29-host-series-a.json",
                    "measured_on": "2026-07-29",
                    "measured_at": "2026-07-29T00:00:00+00:00",
                    "series_id": "series-one",
                    "host": "staging",
                    "profile": "ubuntu-24",
                    "image_id": None,
                    "vcpus": 1,
                    "memory_mib": 512,
                    "trials": 3,
                    "startup": {
                        "min_ns": 3_000_000_000,
                        "median_ns": 3_100_000_000,
                        "p95_ns": 3_200_000_000,
                        "max_ns": 3_200_000_000,
                    },
                    "shutdown_median_ns": 600_000_000,
                    "trace": None,
                    "metrics": [{"workload": "small-files"}],
                }
            ],
        )

    def test_reports_entries_the_report_parser_rejects(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            unordered = summary(series_id="series-a")
            unordered["lifecycle"]["startup_duration_ns"]["min"] = 4_000_000_000
            self.write(directory, "2026-07-29-host-series-a.json", unordered)
            missing = summary(series_id="series-b")
            del missing["lifecycle"]
            self.write(directory, "2026-07-30-host-series-b.json", missing)
            retired = summary(series_id="series-c")
            retired["benchmark"] = "cold-request-execution-lifecycle"
            del retired["lifecycle"]
            self.write(directory, "2026-07-31-host-series-c.json", retired)
            payload = DASHBOARD.build_history_payload(directory)
        self.assertEqual(payload["series"], [])
        self.assertEqual(
            payload["errors"],
            [
                "2026-07-29-host-series-a.json: startup distribution is not ordered",
                "2026-07-30-host-series-b.json: lifecycle must be an object",
            ],
        )

    def test_empty_directory_yields_empty_payload(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            payload = DASHBOARD.build_history_payload(Path(raw))
        self.assertEqual(payload["series"], [])
        self.assertEqual(payload["errors"], [])


if __name__ == "__main__":
    unittest.main()
