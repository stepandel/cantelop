from __future__ import annotations

import copy
from contextlib import redirect_stderr
import io
import json
from pathlib import Path
import tempfile
import unittest

from support import load_script, role_file_text, tasks_text


SUMMARIZER = load_script("summarize-storage-benchmarks.py")
BENCHMARK_PLAYBOOK = (
    Path(__file__).resolve().parents[1] / "playbooks" / "compute" / "benchmark-storage.yml"
)
WARMUP_GATE_PLAYBOOK = (
    Path(__file__).resolve().parents[1] / "playbooks" / "compute" / "gate-vm-warmup.yml"
)
WARMUP_GATE_POLICY = (
    Path(__file__).resolve().parents[3]
    / "tests"
    / "benchmarks"
    / "policies"
    / "hetzner-snapshot-warmup-v1.json"
)


def result(
    run_id: str,
    offset: int = 0,
    *,
    trial: int = 1,
    trial_count: int = 2,
    series_id: str = "series-00000000-0000-0000-0000-000000000001",
) -> dict[str, object]:
    metric = {
        "schema_version": 2,
        "workload": "small-files",
        "run_id": run_id,
        "operations": 100,
        "bytes": 900,
        "duration_ns": 1_000 + offset,
    }
    concurrent = {
        "schema_version": 2,
        "workload": "sequential-read",
        "run_id": run_id,
        "operations": 1,
        "bytes": 1_048_576,
        "duration_ns": 2_000 + offset,
        "cache_scope": "fresh-vm-shared-workspace-coordinated-read",
        "cache_state": "concurrent-candidate",
        "cache_guarantee": (
            "guest-clients-reset-host-and-backend-caches-retained"
        ),
        "concurrency": 2,
        "participant": 1,
        "scheduled_start_ns": 1_800_000_000_000_000_000,
        "actual_start_ns": 1_800_000_000_000_000_100,
        "start_lag_ns": 100,
    }
    concurrent_peer = dict(
        concurrent,
        participant=2,
        duration_ns=3_000 + offset,
        actual_start_ns=1_800_000_000_000_000_200,
        start_lag_ns=200,
    )
    return {
        "schema_version": 5,
        "series_id": series_id,
        "run_id": run_id,
        "timestamp": f"2026-07-30T20:00:0{trial}Z",
        "trial": trial,
        "trial_count": trial_count,
        "host": "staging-1",
        "profile": "default",
        "image_id": "sha256:" + "a" * 64,
        "vm": {"vcpus": 1, "memory_mib": 256},
        "artifacts": {
            "host": [
                {
                    "name": name,
                    "path": f"/host/{name}",
                    "sha256": f"{index:064x}",
                    "size": index + 1,
                }
                for index, name in enumerate(
                    (
                        "orchestrator",
                        "systemd-unit",
                        "configuration",
                        "image-catalog",
                        "firecracker",
                        "jailer",
                        "guest-kernel",
                        "guest-rootfs",
                        "container-rootfs",
                        "snapshot-manifest",
                        "snapshot-vmstate",
                        "snapshot-memory",
                    ),
                    start=1,
                )
            ],
            "guest": [
                {
                    "name": name,
                    "path": f"/guest/{name}",
                    "sha256": f"{index:064x}",
                }
                for index, name in enumerate(
                    ("workspace-benchmark",),
                    start=20,
                )
            ],
        },
        "parameters": {
            "workspace_cache_size_mib": 256,
            "small_file_count": 100,
            "dependency_file_count": 500,
            "sequential_size_mib": 1,
            "concurrency": 2,
            "schedule_delay_seconds": 15,
            "maximum_start_lag_ms": 250,
        },
        "lifecycle": {
            "startup_duration_ns": 10_000 + offset,
            "shutdown_duration_ns": 5_000 + offset,
            "warmup_trace": {
                "total_ns": 9_000 + offset,
                "steps": [
                    {
                        "name": "http.decode_create_request",
                        "started_offset_ns": 0,
                        "duration_ns": 100,
                    },
                    {
                        "name": "firecracker.load_snapshot",
                        "started_offset_ns": 1_000,
                        "duration_ns": 4_000 + offset,
                    },
                    {
                        "name": "jail.set_asset_ownership",
                        "started_offset_ns": 5_100 + offset,
                        "duration_ns": 100,
                    },
                    {
                        "name": "guest.assign_snapshot",
                        "started_offset_ns": 5_500 + offset,
                        "duration_ns": 500,
                    },
                    {
                        "name": "jail.set_asset_ownership",
                        "started_offset_ns": 5_300 + offset,
                        "duration_ns": 100,
                    },
                ],
            },
        },
        "coordination": {
            "clock_model": "guest-wall-clocks-assumed-synchronized",
            "participants": 2,
            "scheduled_start_ns": 1_800_000_000_000_000_000,
            "observed_start_skew_ns": 100,
            "observed_wall_duration_ns": 3_100 + offset,
            "aggregate_bytes": 2_097_152,
        },
        "metrics": [metric, concurrent, concurrent_peer],
    }


def policy(*, throughput_threshold: int = 1) -> dict[str, object]:
    return {
        "schema_version": 1,
        "minimum_trials": 2,
        "checks": [
            {
                "name": "startup-p95",
                "target": {
                    "section": "lifecycle",
                    "measurement": "startup_duration_ns",
                },
                "statistic": "p95_nearest_rank",
                "comparison": "at_most",
                "threshold": 20_000,
            },
            {
                "name": "concurrent-read-throughput-median",
                "target": {
                    "section": "metric",
                    "workload": "sequential-read",
                    "cache_scope": (
                        "fresh-vm-shared-workspace-coordinated-read"
                    ),
                    "cache_state": "concurrent-candidate",
                    "cache_guarantee": (
                        "guest-clients-reset-host-and-backend-caches-retained"
                    ),
                    "concurrency": 2,
                    "measurement": "bytes_per_second",
                },
                "statistic": "median",
                "comparison": "at_least",
                "threshold": throughput_threshold,
            },
        ],
    }


class StorageBenchmarkSummaryTest(unittest.TestCase):
    def test_hetzner_snapshot_warmup_gate_contract(self) -> None:
        playbook = WARMUP_GATE_PLAYBOOK.read_text(encoding="utf-8")
        policy_document = json.loads(WARMUP_GATE_POLICY.read_text(encoding="utf-8"))

        self.assertNotIn("benchmark-storage.yml", playbook)
        self.assertIn("benchmark-vm-warmup.py", playbook)
        self.assertIn("--trials", playbook)
        self.assertIn("- '10'", playbook)
        self.assertIn("warmup_policy", playbook)
        self.assertIn(WARMUP_GATE_POLICY.name, playbook)
        self.assertEqual(policy_document["minimum_trials"], 10)
        self.assertEqual(
            [check["name"] for check in policy_document["checks"]],
            ["vm-warmup-median", "vm-warmup-p95"],
        )
        self.assertEqual(
            [check["threshold"] for check in policy_document["checks"]],
            [260_000_000, 300_000_000],
        )

        passing = SUMMARIZER.evaluate_policy(
            {
                "trials": 10,
                "lifecycle": {
                    "startup_duration_ns": {
                        "median": 260_000_000,
                        "p95_nearest_rank": 300_000_000,
                    }
                },
            },
            policy_document,
        )
        self.assertTrue(passing["passed"])

        failing_summary = {
            "trials": 10,
            "lifecycle": {
                "startup_duration_ns": {
                    "median": 260_000_001,
                    "p95_nearest_rank": 300_000_001,
                }
            },
        }
        self.assertFalse(
            SUMMARIZER.evaluate_policy(failing_summary, policy_document)["passed"]
        )

    def test_benchmark_playbook_always_summarizes_and_optionally_enforces(self) -> None:
        playbook = BENCHMARK_PLAYBOOK.read_text(encoding="utf-8")

        self.assertIn("Find the complete controller benchmark series", playbook)
        self.assertIn("Summarize and enforce the benchmark series", playbook)
        self.assertIn("summarize-storage-benchmarks.py", playbook)
        self.assertIn("ansible_playbook_python", playbook)
        self.assertNotIn("'/usr/bin/python3'", playbook)
        self.assertIn("fire_fuse_benchmark_policy", playbook)
        self.assertIn("-summary.json", playbook)
        self.assertIn(
            "fire_fuse_benchmark_series_results.matched | int",
            playbook,
        )
        self.assertIn("fire_fuse_benchmark_trial_count | int", playbook)

    def test_benchmark_role_captures_detailed_warmup_trace(self) -> None:
        tasks = tasks_text("fire_fuse_benchmark")

        self.assertIn("Read the benchmark VM warmup trace", tasks)
        self.assertIn("sandbox warmup trace completed", tasks)
        self.assertIn("'schema_version': 5", tasks)
        self.assertIn("'image_id': fire_fuse_benchmark_build_id", tasks)
        self.assertIn("'warmup_trace': {", tasks)
        self.assertNotIn("guest_milestones", tasks)

    def test_benchmark_role_supports_snapshot_assigned_workloads(self) -> None:
        tasks = tasks_text("fire_fuse_benchmark")
        defaults = role_file_text("fire_fuse_benchmark", "defaults/main.yml")

        self.assertIn("fire_fuse_benchmark_ssh_port: 22", defaults)
        self.assertIn("fire_fuse_benchmark_runtime_port: 3000", defaults)
        self.assertIn('fire_fuse_benchmark_build_id: ""', defaults)
        self.assertIn("fire_fuse_benchmark_memory_mib: 512", defaults)
        self.assertGreaterEqual(tasks.count("fire_fuse_benchmark_ssh_port"), 5)
        self.assertEqual(tasks.count("Idempotency-Key:"), 0)
        self.assertGreaterEqual(
            tasks.count("'app_id': fire_fuse_benchmark_app_id"),
            4,
        )
        self.assertIn("fire_fuse_benchmark_create_response.build_id", tasks)
        self.assertIn("map(attribute='build_id')", tasks)
        self.assertNotIn("map(attribute='image_id')", tasks)
        self.assertIn("Require the benchmark snapshot shape", tasks)
        self.assertIn("Require the parsed snapshot manifest hash to match", tasks)
        self.assertIn("'firecracker.load_snapshot'", tasks)
        self.assertIn("'host_pool.take_slot'", tasks)
        self.assertIn("'host_pool.hit'", tasks)
        self.assertIn("'jail.bind_selected_container_rootfs'", tasks)
        self.assertIn("'host_pool.miss' not in", tasks)
        self.assertIn("'firecracker.patch_container_rootfs'", tasks)
        self.assertIn("'firecracker.resume_snapshot'", tasks)
        self.assertNotIn(".container_rootfs_image", tasks)
        self.assertIn("'guest.assign_snapshot'", tasks)

    def test_benchmark_evidence_identifies_snapshot_bundle_files(self) -> None:
        tasks = tasks_text("fire_fuse_benchmark")

        self.assertIn("- name: image-catalog", tasks)
        self.assertIn("Declare active snapshot benchmark host artifacts", tasks)
        self.assertIn("'name': 'snapshot-manifest'", tasks)
        self.assertIn("'name': 'snapshot-vmstate'", tasks)
        self.assertIn("'name': 'snapshot-memory'", tasks)

    def test_summarizes_comparable_trials(self) -> None:
        summary = SUMMARIZER.summarize_results(
            [
                ("first.json", result("benchmark-first")),
                ("second.json", result("benchmark-second", 100, trial=2)),
            ]
        )

        self.assertEqual(summary["schema_version"], 1)
        self.assertEqual(summary["source_schema_version"], 5)
        self.assertEqual(
            summary["compatibility"]["image_id"],
            "sha256:" + "a" * 64,
        )
        self.assertEqual(summary["trials"], 2)
        self.assertEqual(
            summary["lifecycle"]["startup_duration_ns"],
            {
                "samples": 2,
                "min": 10_000,
                "median": 10_050,
                "p95_nearest_rank": 10_100,
                "max": 10_100,
            },
        )
        concurrent = next(
            metric
            for metric in summary["metrics"]
            if metric["cache_state"] == "concurrent-candidate"
        )
        self.assertEqual(concurrent["samples_per_trial"], 2)
        self.assertEqual(concurrent["duration_ns"]["samples"], 4)
        warmup_steps = {
            step["name"]: step for step in summary["warmup_trace"]["steps"]
        }
        self.assertEqual(
            warmup_steps["firecracker.load_snapshot"]["duration_ns"],
            {
                "samples": 2,
                "min": 4_000,
                "median": 4_050,
                "p95_nearest_rank": 4_100,
                "max": 4_100,
            },
        )
        self.assertEqual(
            warmup_steps["jail.set_asset_ownership"]["occurrences_per_trial"],
            2,
        )
        self.assertNotIn("guest_milestones", summary["warmup_trace"])

    def test_summarizes_snapshot_artifact_trials(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", 100, trial=2)

        summary = SUMMARIZER.summarize_results(
            [("first.json", first), ("second.json", second)]
        )

        self.assertEqual(summary["trials"], 2)

    def test_rejects_traces_without_snapshot_assignment(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", 100, trial=2)
        for document in (first, second):
            document["lifecycle"]["warmup_trace"]["steps"] = [
                step
                for step in document["lifecycle"]["warmup_trace"]["steps"]
                if step["name"] != "guest.assign_snapshot"
            ]

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "must include snapshot assignment",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_partial_snapshot_artifact_set(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", 100, trial=2)
        first["artifacts"]["host"].pop()

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "does not contain the expected set",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_incompatible_parameters(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        second["parameters"]["workspace_cache_size_mib"] = 128

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "differs from the first result",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_previous_host_result_schemas(self) -> None:
        for schema_version in (3, 4):
            with self.subTest(schema_version=schema_version):
                first = result("benchmark-first")
                second = result("benchmark-second", 100, trial=2)
                first["schema_version"] = schema_version
                second["schema_version"] = schema_version

                with self.assertRaisesRegex(
                    SUMMARIZER.SummaryError,
                    "host result is not schema version 5",
                ):
                    SUMMARIZER.summarize_results(
                        [("first.json", first), ("second.json", second)]
                    )

    def test_rejects_different_warmup_step_sets(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        second["lifecycle"]["warmup_trace"]["steps"].pop()

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "warmup steps differ",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_warmup_step_after_trace_total(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        first["lifecycle"]["warmup_trace"]["steps"][0]["duration_ns"] = 10**9

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "finishes after the trace total",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_changed_artifact_identity(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        second["artifacts"]["host"][0]["sha256"] = "f" * 64

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "artifact identity differs",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_changed_image_identity(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        second["image_id"] = "sha256:" + "b" * 64

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "differs from the first result",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_missing_snapshot_only_image_identity(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        del first["image_id"]

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "image_id must be a nonempty string",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_missing_concurrent_participant(self) -> None:
        first = result("benchmark-first")
        second = result("benchmark-second", trial=2)
        second["metrics"] = copy.deepcopy(second["metrics"][:-1])

        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "every participant exactly once",
        ):
            SUMMARIZER.summarize_results(
                [("first.json", first), ("second.json", second)]
            )

    def test_rejects_duplicate_run_ids(self) -> None:
        with self.assertRaisesRegex(SUMMARIZER.SummaryError, "duplicate run ID"):
            SUMMARIZER.summarize_results(
                [
                    ("first.json", result("benchmark-same")),
                    ("second.json", result("benchmark-same", trial=2)),
                ]
            )

    def test_rejects_incomplete_series(self) -> None:
        with self.assertRaisesRegex(
            SUMMARIZER.SummaryError,
            "complete contiguous trial sequence",
        ):
            SUMMARIZER.summarize_results(
                [
                    (
                        "first.json",
                        result("benchmark-first", trial_count=3),
                    ),
                    (
                        "second.json",
                        result(
                            "benchmark-second",
                            trial=2,
                            trial_count=3,
                        ),
                    ),
                ]
            )

    def test_acceptance_policy_reports_passing_checks(self) -> None:
        summary = SUMMARIZER.summarize_results(
            [
                ("first.json", result("benchmark-first")),
                ("second.json", result("benchmark-second", 100, trial=2)),
            ]
        )

        acceptance = SUMMARIZER.evaluate_policy(summary, policy())

        self.assertTrue(acceptance["passed"])
        self.assertEqual(len(acceptance["checks"]), 2)
        self.assertTrue(all(check["passed"] for check in acceptance["checks"]))
        self.assertRegex(acceptance["policy_sha256"], r"^[0-9a-f]{64}$")

    def test_acceptance_policy_reports_failed_check(self) -> None:
        summary = SUMMARIZER.summarize_results(
            [
                ("first.json", result("benchmark-first")),
                ("second.json", result("benchmark-second", 100, trial=2)),
            ]
        )

        acceptance = SUMMARIZER.evaluate_policy(
            summary,
            policy(throughput_threshold=10**18),
        )

        self.assertFalse(acceptance["passed"])
        self.assertFalse(acceptance["checks"][1]["passed"])

    def test_main_creates_private_output_without_replacing_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "first.json"
            second = root / "second.json"
            output = root / "summary.json"
            first.write_text(json.dumps(result("benchmark-first")), encoding="utf-8")
            second.write_text(
                json.dumps(result("benchmark-second", 100, trial=2)),
                encoding="utf-8",
            )

            self.assertEqual(
                SUMMARIZER.main(
                    [
                        "--output",
                        str(output),
                        str(first),
                        str(second),
                    ]
                ),
                0,
            )
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            error = io.StringIO()
            with redirect_stderr(error):
                self.assertEqual(
                    SUMMARIZER.main(
                        [
                            "--output",
                            str(output),
                            str(first),
                            str(second),
                        ]
                    ),
                    1,
                )
            self.assertIn("cannot create output", error.getvalue())

    def test_main_writes_failed_policy_evidence_and_returns_two(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "first.json"
            second = root / "second.json"
            policy_path = root / "policy.json"
            output = root / "summary.json"
            first.write_text(json.dumps(result("benchmark-first")), encoding="utf-8")
            second.write_text(
                json.dumps(result("benchmark-second", 100, trial=2)),
                encoding="utf-8",
            )
            policy_path.write_text(
                json.dumps(policy(throughput_threshold=10**18)),
                encoding="utf-8",
            )

            self.assertEqual(
                SUMMARIZER.main(
                    [
                        "--policy",
                        str(policy_path),
                        "--output",
                        str(output),
                        str(first),
                        str(second),
                    ]
                ),
                2,
            )
            rendered = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(rendered["acceptance"]["passed"])


if __name__ == "__main__":
    unittest.main()
