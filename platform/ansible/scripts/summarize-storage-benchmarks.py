#!/usr/bin/env python3
"""Validate and summarize comparable fire-fuse storage benchmark results."""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
from datetime import datetime, timezone
import functools
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import sys
from typing import Any, Iterable

sys.path.insert(0, str(Path(__file__).resolve().parent))
import storage_script_common as common  # noqa: E402  (installed beside this script)

HOST_RESULT_SCHEMA_VERSION = 5


class SummaryError(ValueError):
    """The input set cannot produce a trustworthy summary."""


def require_integer(
    value: Any,
    name: str,
    *,
    minimum: int = 0,
) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise SummaryError(f"{name} must be an integer >= {minimum}")
    return value


def require_string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value:
        raise SummaryError(f"{name} must be a nonempty string")
    return value


def require_utc_timestamp(value: Any, name: str) -> str:
    timestamp = require_string(value, name)
    try:
        parsed = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    except ValueError as error:
        raise SummaryError(f"{name} must be an RFC 3339 timestamp") from error
    if parsed.tzinfo is None or parsed.utcoffset() != timezone.utc.utcoffset(parsed):
        raise SummaryError(f"{name} must use UTC")
    return timestamp


require_mapping = functools.partial(common.require_mapping, error=SummaryError)


def require_list(value: Any, name: str) -> list[Any]:
    if not isinstance(value, list):
        raise SummaryError(f"{name} must be an array")
    return value


def validated_warmup_trace(value: Any, source: str) -> dict[str, Any]:
    trace = require_mapping(value, f"{source}.lifecycle.warmup_trace")
    if set(trace) != {"total_ns", "steps"}:
        raise SummaryError(f"{source}.lifecycle.warmup_trace has an unexpected shape")
    total_ns = require_integer(
        trace.get("total_ns"),
        f"{source}.lifecycle.warmup_trace.total_ns",
        minimum=1,
    )
    raw_steps = require_list(
        trace.get("steps"),
        f"{source}.lifecycle.warmup_trace.steps",
    )
    if not 1 <= len(raw_steps) <= 256:
        raise SummaryError(f"{source}.lifecycle.warmup_trace.steps must contain 1-256 entries")
    steps: list[dict[str, Any]] = []
    for index, raw_step in enumerate(raw_steps):
        step_name = f"{source}.lifecycle.warmup_trace.steps[{index}]"
        step = require_mapping(raw_step, step_name)
        if set(step) != {"name", "started_offset_ns", "duration_ns"}:
            raise SummaryError(f"{step_name} has an unexpected shape")
        name = require_string(step.get("name"), f"{step_name}.name")
        if re.fullmatch(r"[a-z][a-z0-9_.]{0,95}", name) is None:
            raise SummaryError(f"{step_name}.name is not a stable metric name")
        started_offset_ns = require_integer(
            step.get("started_offset_ns"),
            f"{step_name}.started_offset_ns",
        )
        duration_ns = require_integer(
            step.get("duration_ns"),
            f"{step_name}.duration_ns",
            minimum=1,
        )
        if started_offset_ns + duration_ns > total_ns:
            raise SummaryError(f"{step_name} finishes after the trace total")
        steps.append(
            {
                "name": name,
                "started_offset_ns": started_offset_ns,
                "duration_ns": duration_ns,
            }
        )
    if not any(step["name"] == "guest.assign_snapshot" for step in steps):
        raise SummaryError(
            f"{source}.lifecycle.warmup_trace.steps must include snapshot assignment"
        )
    return {"total_ns": total_ns, "steps": steps}


def load_document(path: Path, document_type: str) -> dict[str, Any]:
    if not path.is_file():
        raise SummaryError(f"{path}: {document_type} is not a regular file")
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise SummaryError(f"{path}: cannot decode JSON: {error}") from error
    return require_mapping(document, str(path))


def load_result(path: Path) -> dict[str, Any]:
    return load_document(path, "result")


def load_policy(path: Path) -> dict[str, Any]:
    return load_document(path, "policy")


def validated_artifacts(
    value: Any,
    source: str,
) -> dict[str, Any]:
    artifacts = require_mapping(value, f"{source}.artifacts")
    if set(artifacts) != {"host", "guest"}:
        raise SummaryError(f"{source}.artifacts must contain host and guest")
    expected_names = {
        "host": {
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
        },
        "guest": {"workspace-benchmark"},
    }
    canonical: dict[str, list[dict[str, Any]]] = {}
    for scope in ("host", "guest"):
        entries = require_list(artifacts.get(scope), f"{source}.artifacts.{scope}")
        observed_names: set[str] = set()
        observed_paths: set[str] = set()
        canonical_entries: list[dict[str, Any]] = []
        expected_keys = {"name", "path", "sha256"}
        if scope == "host":
            expected_keys.add("size")
        for index, raw_entry in enumerate(entries):
            entry_name = f"{source}.artifacts.{scope}[{index}]"
            entry = require_mapping(raw_entry, entry_name)
            if set(entry) != expected_keys:
                raise SummaryError(f"{entry_name} has an unexpected shape")
            name = require_string(entry.get("name"), f"{entry_name}.name")
            path = require_string(entry.get("path"), f"{entry_name}.path")
            checksum = require_string(entry.get("sha256"), f"{entry_name}.sha256")
            if not path.startswith("/"):
                raise SummaryError(f"{entry_name}.path must be absolute")
            if re.fullmatch(r"[0-9a-f]{64}", checksum) is None:
                raise SummaryError(f"{entry_name}.sha256 must be lowercase SHA-256")
            if name in observed_names or path in observed_paths:
                raise SummaryError(f"{entry_name} repeats an artifact name or path")
            observed_names.add(name)
            observed_paths.add(path)
            canonical_entry: dict[str, Any] = {
                "name": name,
                "path": path,
                "sha256": checksum,
            }
            if scope == "host":
                canonical_entry["size"] = require_integer(
                    entry.get("size"),
                    f"{entry_name}.size",
                    minimum=1,
                )
            canonical_entries.append(canonical_entry)
        if observed_names != expected_names[scope]:
            raise SummaryError(
                f"{source}.artifacts.{scope} does not contain the expected set"
            )
        canonical[scope] = sorted(
            canonical_entries,
            key=lambda entry: entry["name"],
        )
    return canonical


def compatibility_key(
    result: dict[str, Any],
    source: str,
) -> dict[str, Any]:
    coordination = require_mapping(result.get("coordination"), f"{source}.coordination")
    key = {
        "host": require_string(result.get("host"), f"{source}.host"),
        "profile": require_string(result.get("profile"), f"{source}.profile"),
        "vm": require_mapping(result.get("vm"), f"{source}.vm"),
        "parameters": require_mapping(
            result.get("parameters"),
            f"{source}.parameters",
        ),
        "artifacts": validated_artifacts(
            result.get("artifacts"),
            source,
        ),
        "clock_model": require_string(
            coordination.get("clock_model"),
            f"{source}.coordination.clock_model",
        ),
    }
    image_id = require_string(result.get("image_id"), f"{source}.image_id")
    if re.fullmatch(r"sha256:[0-9a-f]{64}", image_id) is None:
        raise SummaryError(f"{source}.image_id must be a content address")
    key["image_id"] = image_id
    return key


def metric_key(metric: dict[str, Any]) -> tuple[str, str, str, str, int | None]:
    workload = require_string(metric.get("workload"), "metric.workload")
    cache_scope = metric.get("cache_scope", "")
    cache_state = metric.get("cache_state", "")
    cache_guarantee = metric.get("cache_guarantee", "")
    for value, name in (
        (cache_scope, "metric.cache_scope"),
        (cache_state, "metric.cache_state"),
        (cache_guarantee, "metric.cache_guarantee"),
    ):
        if not isinstance(value, str):
            raise SummaryError(f"{name} must be a string when present")
    concurrency = metric.get("concurrency")
    if concurrency is not None:
        concurrency = require_integer(
            concurrency,
            "metric.concurrency",
            minimum=2,
        )
    return workload, cache_scope, cache_state, cache_guarantee, concurrency


def nearest_rank(values: list[int], percentile: float) -> int:
    ordered = sorted(values)
    index = max(0, math.ceil(percentile * len(ordered)) - 1)
    return ordered[index]


def distribution(values: Iterable[int]) -> dict[str, int | float]:
    samples = list(values)
    if not samples:
        raise SummaryError("cannot summarize an empty measurement series")
    median = statistics.median(samples)
    return {
        "samples": len(samples),
        "min": min(samples),
        "median": int(median) if float(median).is_integer() else median,
        "p95_nearest_rank": nearest_rank(samples, 0.95),
        "max": max(samples),
    }


def validated_metrics(
    result: dict[str, Any],
    source: str,
    run_id: str,
) -> tuple[list[dict[str, Any]], Counter[tuple[str, str, str, str, int | None]]]:
    metrics = require_list(result.get("metrics"), f"{source}.metrics")
    if not metrics:
        raise SummaryError(f"{source}.metrics must not be empty")
    counts: Counter[tuple[str, str, str, str, int | None]] = Counter()
    participants: defaultdict[
        tuple[str, str, str, str, int | None],
        set[int],
    ] = defaultdict(set)
    validated: list[dict[str, Any]] = []
    for index, raw_metric in enumerate(metrics):
        metric = require_mapping(raw_metric, f"{source}.metrics[{index}]")
        if require_integer(
            metric.get("schema_version"),
            f"{source}.metrics[{index}].schema_version",
            minimum=1,
        ) != 2:
            raise SummaryError(f"{source}.metrics[{index}] is not schema version 2")
        if require_string(
            metric.get("run_id"),
            f"{source}.metrics[{index}].run_id",
        ) != run_id:
            raise SummaryError(f"{source}.metrics[{index}] has the wrong run ID")
        require_integer(
            metric.get("duration_ns"),
            f"{source}.metrics[{index}].duration_ns",
            minimum=1,
        )
        require_integer(
            metric.get("operations"),
            f"{source}.metrics[{index}].operations",
            minimum=1,
        )
        require_integer(
            metric.get("bytes"),
            f"{source}.metrics[{index}].bytes",
        )
        key = metric_key(metric)
        concurrency = key[-1]
        if concurrency is not None:
            participant = require_integer(
                metric.get("participant"),
                f"{source}.metrics[{index}].participant",
                minimum=1,
            )
            if participant > concurrency:
                raise SummaryError(
                    f"{source}.metrics[{index}].participant exceeds concurrency"
                )
            if participant in participants[key]:
                raise SummaryError(
                    f"{source}: metric group {key!r} repeats participant "
                    f"{participant}"
                )
            participants[key].add(participant)
            scheduled_start = require_integer(
                metric.get("scheduled_start_ns"),
                f"{source}.metrics[{index}].scheduled_start_ns",
                minimum=1,
            )
            actual_start = require_integer(
                metric.get("actual_start_ns"),
                f"{source}.metrics[{index}].actual_start_ns",
                minimum=scheduled_start,
            )
            start_lag = require_integer(
                metric.get("start_lag_ns"),
                f"{source}.metrics[{index}].start_lag_ns",
            )
            if start_lag != actual_start - scheduled_start:
                raise SummaryError(
                    f"{source}.metrics[{index}] has inconsistent start lag"
                )
        counts[key] += 1
        validated.append(metric)
    for key, observed in participants.items():
        concurrency = key[-1]
        assert concurrency is not None
        if observed != set(range(1, concurrency + 1)):
            raise SummaryError(
                f"{source}: metric group {key!r} does not contain every "
                "participant exactly once"
            )
    return validated, counts


def summarize_results(
    documents: list[tuple[str, dict[str, Any]]],
) -> dict[str, Any]:
    if len(documents) < 2:
        raise SummaryError("at least two benchmark result files are required")

    expected_compatibility: dict[str, Any] | None = None
    expected_counts: Counter[
        tuple[str, str, str, str, int | None]
    ] | None = None
    expected_series_id: str | None = None
    expected_trial_count: int | None = None
    trial_numbers: set[int] = set()
    run_ids: set[str] = set()
    run_records: list[dict[str, Any]] = []
    measured_at: list[str] = []
    lifecycle_startup: list[int] = []
    lifecycle_shutdown: list[int] = []
    coordination_skew: list[int] = []
    coordination_wall: list[int] = []
    coordination_throughput: list[int] = []
    grouped_metrics: defaultdict[
        tuple[str, str, str, str, int | None],
        list[dict[str, Any]],
    ] = defaultdict(list)
    expected_warmup_step_counts: Counter[str] | None = None
    warmup_totals: list[int] = []
    warmup_step_durations: defaultdict[str, list[int]] = defaultdict(list)
    warmup_step_starts: defaultdict[str, list[int]] = defaultdict(list)

    for source, result in documents:
        source_schema = require_integer(
            result.get("schema_version"),
            f"{source}.schema_version",
            minimum=1,
        )
        if source_schema != HOST_RESULT_SCHEMA_VERSION:
            raise SummaryError(
                f"{source}: host result is not schema version "
                f"{HOST_RESULT_SCHEMA_VERSION}"
            )
        series_id = require_string(result.get("series_id"), f"{source}.series_id")
        if expected_series_id is None:
            expected_series_id = series_id
        elif series_id != expected_series_id:
            raise SummaryError(f"{source}: result belongs to a different series")
        trial_count = require_integer(
            result.get("trial_count"),
            f"{source}.trial_count",
            minimum=2,
        )
        if expected_trial_count is None:
            expected_trial_count = trial_count
        elif trial_count != expected_trial_count:
            raise SummaryError(f"{source}: trial count differs from the first result")
        trial = require_integer(
            result.get("trial"),
            f"{source}.trial",
            minimum=1,
        )
        if trial > trial_count:
            raise SummaryError(f"{source}: trial exceeds the declared trial count")
        if trial in trial_numbers:
            raise SummaryError(f"{source}: duplicate trial number {trial}")
        trial_numbers.add(trial)
        run_id = require_string(result.get("run_id"), f"{source}.run_id")
        if run_id in run_ids:
            raise SummaryError(f"{source}: duplicate run ID {run_id!r}")
        run_ids.add(run_id)
        measured_at.append(
            require_utc_timestamp(result.get("timestamp"), f"{source}.timestamp")
        )
        run_records.append(
            {
                "trial": trial,
                "run_id": run_id,
                "source": source,
            }
        )

        current_compatibility = compatibility_key(
            result,
            source,
        )
        if expected_compatibility is None:
            expected_compatibility = current_compatibility
        elif current_compatibility != expected_compatibility:
            raise SummaryError(
                f"{source}: host, profile, VM, parameters, or clock model "
                "or artifact identity differs from the first result"
            )

        metrics, current_counts = validated_metrics(result, source, run_id)
        if expected_counts is None:
            expected_counts = current_counts
        elif current_counts != expected_counts:
            raise SummaryError(
                f"{source}: metric groups or samples-per-trial differ "
                "from the first result"
            )
        for metric in metrics:
            grouped_metrics[metric_key(metric)].append(metric)

        lifecycle = require_mapping(result.get("lifecycle"), f"{source}.lifecycle")
        lifecycle_startup.append(
            require_integer(
                lifecycle.get("startup_duration_ns"),
                f"{source}.lifecycle.startup_duration_ns",
                minimum=1,
            )
        )
        lifecycle_shutdown.append(
            require_integer(
                lifecycle.get("shutdown_duration_ns"),
                f"{source}.lifecycle.shutdown_duration_ns",
                minimum=1,
            )
        )
        warmup = validated_warmup_trace(
            lifecycle.get("warmup_trace"),
            source,
        )
        step_counts = Counter(step["name"] for step in warmup["steps"])
        if expected_warmup_step_counts is None:
            expected_warmup_step_counts = step_counts
        elif step_counts != expected_warmup_step_counts:
            raise SummaryError(f"{source}: warmup steps differ from the first result")
        warmup_totals.append(warmup["total_ns"])
        steps_by_name: defaultdict[str, list[dict[str, Any]]] = defaultdict(list)
        for step in warmup["steps"]:
            steps_by_name[step["name"]].append(step)
        for name, steps in steps_by_name.items():
            warmup_step_durations[name].append(
                sum(step["duration_ns"] for step in steps)
            )
            warmup_step_starts[name].append(
                min(step["started_offset_ns"] for step in steps)
            )

        coordination = require_mapping(
            result.get("coordination"),
            f"{source}.coordination",
        )
        participants = require_integer(
            coordination.get("participants"),
            f"{source}.coordination.participants",
            minimum=2,
        )
        parameters = current_compatibility["parameters"]
        configured_concurrency = require_integer(
            parameters.get("concurrency"),
            f"{source}.parameters.concurrency",
            minimum=2,
        )
        if participants != configured_concurrency:
            raise SummaryError(
                f"{source}: coordination participants do not match parameters"
            )
        scheduled_start = require_integer(
            coordination.get("scheduled_start_ns"),
            f"{source}.coordination.scheduled_start_ns",
            minimum=1,
        )
        skew = require_integer(
            coordination.get("observed_start_skew_ns"),
            f"{source}.coordination.observed_start_skew_ns",
        )
        wall = require_integer(
            coordination.get("observed_wall_duration_ns"),
            f"{source}.coordination.observed_wall_duration_ns",
            minimum=1,
        )
        aggregate_bytes = require_integer(
            coordination.get("aggregate_bytes"),
            f"{source}.coordination.aggregate_bytes",
            minimum=1,
        )
        concurrent_metrics = [
            metric
            for metric in metrics
            if metric.get("concurrency") == participants
        ]
        if len(concurrent_metrics) != participants:
            raise SummaryError(
                f"{source}: concurrent metric count does not match participants"
            )
        if any(
            metric.get("scheduled_start_ns") != scheduled_start
            for metric in concurrent_metrics
        ):
            raise SummaryError(
                f"{source}: concurrent metric schedule differs from coordination"
            )
        if sum(metric["bytes"] for metric in concurrent_metrics) != aggregate_bytes:
            raise SummaryError(
                f"{source}: concurrent metric bytes differ from aggregate bytes"
            )
        actual_starts = [metric["actual_start_ns"] for metric in concurrent_metrics]
        if max(actual_starts) - min(actual_starts) != skew:
            raise SummaryError(
                f"{source}: concurrent metric starts differ from observed skew"
            )
        latest_completion = max(
            metric["actual_start_ns"] + metric["duration_ns"]
            for metric in concurrent_metrics
        )
        if latest_completion - min(actual_starts) != wall:
            raise SummaryError(
                f"{source}: concurrent metric completions differ from wall duration"
            )
        coordination_skew.append(skew)
        coordination_wall.append(wall)
        coordination_throughput.append((aggregate_bytes * 1_000_000_000) // wall)

    assert expected_compatibility is not None
    assert expected_counts is not None
    assert expected_series_id is not None
    assert expected_trial_count is not None
    if len(documents) != expected_trial_count or trial_numbers != set(
        range(1, expected_trial_count + 1)
    ):
        raise SummaryError(
            "results do not contain the complete contiguous trial sequence"
        )
    metric_summaries: list[dict[str, Any]] = []
    for key in sorted(
        grouped_metrics,
        key=lambda item: tuple("" if value is None else str(value) for value in item),
    ):
        workload, cache_scope, cache_state, cache_guarantee, concurrency = key
        metrics = grouped_metrics[key]
        summary: dict[str, Any] = {
            "workload": workload,
            "cache_scope": cache_scope or None,
            "cache_state": cache_state or None,
            "cache_guarantee": cache_guarantee or None,
            "concurrency": concurrency,
            "samples_per_trial": expected_counts[key],
            "duration_ns": distribution(
                require_integer(metric["duration_ns"], "metric.duration_ns", minimum=1)
                for metric in metrics
            ),
            "operations_per_second": distribution(
                (
                    require_integer(
                        metric["operations"],
                        "metric.operations",
                        minimum=1,
                    )
                    * 1_000_000_000
                )
                // require_integer(
                    metric["duration_ns"],
                    "metric.duration_ns",
                    minimum=1,
                )
                for metric in metrics
            ),
        }
        byte_values = [
            require_integer(metric["bytes"], "metric.bytes") for metric in metrics
        ]
        if any(byte_values):
            if not all(byte_values):
                raise SummaryError(
                    f"metric group {key!r} mixes zero and nonzero byte counts"
                )
            summary["bytes_per_second"] = distribution(
                (byte_count * 1_000_000_000)
                // require_integer(
                    metric["duration_ns"],
                    "metric.duration_ns",
                    minimum=1,
                )
                for byte_count, metric in zip(byte_values, metrics, strict=True)
            )
        metric_summaries.append(summary)

    summary = {
        "schema_version": 1,
        "source_schema_version": HOST_RESULT_SCHEMA_VERSION,
        "trials": len(documents),
        "series_id": expected_series_id,
        "measured_at": max(measured_at),
        "trial_numbers": sorted(trial_numbers),
        "compatibility": expected_compatibility,
        "sources": [source for source, _ in documents],
        "run_ids": sorted(run_ids),
        "runs": sorted(run_records, key=lambda record: record["trial"]),
        "lifecycle": {
            "startup_duration_ns": distribution(lifecycle_startup),
            "shutdown_duration_ns": distribution(lifecycle_shutdown),
        },
        "coordination": {
            "observed_start_skew_ns": distribution(coordination_skew),
            "observed_wall_duration_ns": distribution(coordination_wall),
            "aggregate_bytes_per_second": distribution(coordination_throughput),
        },
        "metrics": metric_summaries,
    }
    assert expected_warmup_step_counts is not None
    summary["warmup_trace"] = {
        "total_ns": distribution(warmup_totals),
        "steps": [
            {
                "name": name,
                "occurrences_per_trial": expected_warmup_step_counts[name],
                "started_offset_ns": distribution(warmup_step_starts[name]),
                "duration_ns": distribution(warmup_step_durations[name]),
            }
            for name in sorted(expected_warmup_step_counts)
        ],
    }
    return summary


def optional_string(value: Any, name: str) -> str | None:
    if value is None:
        return None
    return require_string(value, name)


def resolve_policy_target(
    summary: dict[str, Any],
    target: dict[str, Any],
    name: str,
) -> dict[str, int | float]:
    section = require_string(target.get("section"), f"{name}.section")
    measurement = require_string(
        target.get("measurement"),
        f"{name}.measurement",
    )
    if section in {"lifecycle", "coordination"}:
        if set(target) != {"section", "measurement"}:
            raise SummaryError(f"{name} has unexpected non-metric selector fields")
        section_value = require_mapping(summary.get(section), f"summary.{section}")
        return require_mapping(
            section_value.get(measurement),
            f"summary.{section}.{measurement}",
        )
    if section != "metric":
        raise SummaryError(f"{name}.section is not lifecycle, coordination, or metric")
    expected_keys = {
        "section",
        "workload",
        "cache_scope",
        "cache_state",
        "cache_guarantee",
        "concurrency",
        "measurement",
    }
    if set(target) != expected_keys:
        raise SummaryError(f"{name} must provide the complete metric selector")
    selector = {
        "workload": require_string(target.get("workload"), f"{name}.workload"),
        "cache_scope": optional_string(
            target.get("cache_scope"),
            f"{name}.cache_scope",
        ),
        "cache_state": optional_string(
            target.get("cache_state"),
            f"{name}.cache_state",
        ),
        "cache_guarantee": optional_string(
            target.get("cache_guarantee"),
            f"{name}.cache_guarantee",
        ),
        "concurrency": target.get("concurrency"),
    }
    if selector["concurrency"] is not None:
        selector["concurrency"] = require_integer(
            selector["concurrency"],
            f"{name}.concurrency",
            minimum=2,
        )
    matches = [
        metric
        for metric in require_list(summary.get("metrics"), "summary.metrics")
        if all(metric.get(field) == value for field, value in selector.items())
    ]
    if len(matches) != 1:
        raise SummaryError(f"{name} selects {len(matches)} metric groups, expected 1")
    return require_mapping(
        matches[0].get(measurement),
        f"{name}.measurement",
    )


def evaluate_policy(
    summary: dict[str, Any],
    policy: dict[str, Any],
) -> dict[str, Any]:
    if set(policy) != {"schema_version", "minimum_trials", "checks"}:
        raise SummaryError("acceptance policy has an unexpected shape")
    if require_integer(
        policy.get("schema_version"),
        "policy.schema_version",
        minimum=1,
    ) != 1:
        raise SummaryError("acceptance policy is not schema version 1")
    minimum_trials = require_integer(
        policy.get("minimum_trials"),
        "policy.minimum_trials",
        minimum=2,
    )
    if minimum_trials > 20:
        raise SummaryError("policy.minimum_trials must not exceed 20")
    if require_integer(summary.get("trials"), "summary.trials", minimum=2) < minimum_trials:
        raise SummaryError("summary does not meet policy.minimum_trials")
    checks = require_list(policy.get("checks"), "policy.checks")
    if not checks or len(checks) > 100:
        raise SummaryError("policy.checks must contain between 1 and 100 checks")

    names: set[str] = set()
    evaluated: list[dict[str, Any]] = []
    allowed_statistics = {"min", "median", "p95_nearest_rank", "max"}
    for index, raw_check in enumerate(checks):
        check_name = f"policy.checks[{index}]"
        check = require_mapping(raw_check, check_name)
        if set(check) != {
            "name",
            "target",
            "statistic",
            "comparison",
            "threshold",
        }:
            raise SummaryError(f"{check_name} has an unexpected shape")
        name = require_string(check.get("name"), f"{check_name}.name")
        if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}", name) is None:
            raise SummaryError(f"{check_name}.name is not a safe identifier")
        if name in names:
            raise SummaryError(f"acceptance policy repeats check name {name!r}")
        names.add(name)
        statistic = require_string(
            check.get("statistic"),
            f"{check_name}.statistic",
        )
        if statistic not in allowed_statistics:
            raise SummaryError(f"{check_name}.statistic is not supported")
        comparison = require_string(
            check.get("comparison"),
            f"{check_name}.comparison",
        )
        if comparison not in {"at_most", "at_least"}:
            raise SummaryError(f"{check_name}.comparison is not supported")
        threshold = require_integer(
            check.get("threshold"),
            f"{check_name}.threshold",
        )
        target = require_mapping(check.get("target"), f"{check_name}.target")
        distribution_value = resolve_policy_target(
            summary,
            target,
            f"{check_name}.target",
        )
        observed = distribution_value.get(statistic)
        if isinstance(observed, bool) or not isinstance(observed, (int, float)):
            raise SummaryError(
                f"{check_name} selected a nonnumeric summary statistic"
            )
        passed = (
            observed <= threshold
            if comparison == "at_most"
            else observed >= threshold
        )
        evaluated.append(
            {
                "name": name,
                "target": target,
                "statistic": statistic,
                "comparison": comparison,
                "threshold": threshold,
                "observed": observed,
                "passed": passed,
            }
        )

    canonical_policy = json.dumps(
        policy,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return {
        "schema_version": 1,
        "policy_sha256": hashlib.sha256(canonical_policy).hexdigest(),
        "minimum_trials": minimum_trials,
        "passed": all(check["passed"] for check in evaluated),
        "checks": evaluated,
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Validate comparable schema-v5 storage benchmark results and "
            "emit deterministic JSON statistics."
        )
    )
    parser.add_argument("results", nargs="+", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--policy", type=Path)
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        paths = sorted(args.results, key=lambda path: str(path))
        if len(set(paths)) != len(paths):
            raise SummaryError("the same result path was supplied more than once")
        documents = [(str(path), load_result(path)) for path in paths]
        summary = summarize_results(documents)
        policy_failed = False
        if args.policy is not None:
            policy = load_policy(args.policy)
            summary["acceptance"] = evaluate_policy(summary, policy)
            policy_failed = not summary["acceptance"]["passed"]
        rendered = json.dumps(summary, indent=2, sort_keys=True) + "\n"
        if args.output is None:
            sys.stdout.write(rendered)
        else:
            try:
                descriptor = os.open(
                    args.output,
                    os.O_WRONLY | os.O_CREAT | os.O_EXCL,
                    0o600,
                )
                with os.fdopen(descriptor, "w", encoding="utf-8") as output:
                    output.write(rendered)
            except OSError as error:
                raise SummaryError(
                    f"cannot create output {args.output}: {error}"
                ) from error
    except SummaryError as error:
        print(f"summarize-storage-benchmarks: {error}", file=sys.stderr)
        return 1
    return 2 if policy_failed else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
