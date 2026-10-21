#!/usr/bin/env python3
"""Render the tracked VM warmup benchmark history as a Markdown table."""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from datetime import date, datetime, time, timezone
import math
from pathlib import Path
from typing import Any


DATE_PREFIX = re.compile(r"^(?P<date>\d{4}-\d{2}-\d{2})-")
DEFAULT_HISTORY = Path(__file__).resolve().parents[1] / "tests" / "benchmarks" / "history"
RETIRED_BENCHMARKS = frozenset({"cold-request-execution-lifecycle"})


class HistoryError(ValueError):
    """A tracked benchmark summary is missing required warmup data."""


@dataclass(frozen=True)
class WarmupStep:
    name: str
    occurrences: int
    median_started_ns: int | float
    median_duration_ns: int | float
    p95_duration_ns: int


@dataclass(frozen=True)
class GuestMilestone:
    name: str
    median_uptime_ns: int | float
    p95_uptime_ns: int


@dataclass(frozen=True)
class WarmupEntry:
    measured_on: date
    measured_at: datetime
    host: str
    profile: str
    image_id: str | None
    vcpus: int
    memory_mib: int
    trials: int
    minimum_ns: int
    median_ns: int | float
    p95_ns: int
    maximum_ns: int
    trace_median_ns: int | float | None
    steps: tuple[WarmupStep, ...]
    guest_milestones: tuple[GuestMilestone, ...]
    series_id: str
    source: Path


def require_mapping(value: Any, name: str, source: Path) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise HistoryError(f"{source}: {name} must be an object")
    return value


def require_string(value: Any, name: str, source: Path) -> str:
    if not isinstance(value, str) or not value:
        raise HistoryError(f"{source}: {name} must be a non-empty string")
    return value


def optional_image_id(value: Any, source: Path) -> str | None:
    if value is None:
        return None
    image_id = require_string(value, "compatibility.image_id", source)
    if re.fullmatch(r"sha256:[0-9a-f]{64}", image_id) is None:
        raise HistoryError(
            f"{source}: compatibility.image_id must be a content address"
        )
    return image_id


def require_integer(
    value: Any,
    name: str,
    source: Path,
    *,
    minimum: int,
) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise HistoryError(f"{source}: {name} must be an integer >= {minimum}")
    return value


def require_positive_integer(value: Any, name: str, source: Path) -> int:
    return require_integer(value, name, source, minimum=1)


def require_number(
    value: Any,
    name: str,
    source: Path,
    *,
    minimum: int,
) -> int | float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not math.isfinite(value)
        or value < minimum
    ):
        raise HistoryError(f"{source}: {name} must be a number >= {minimum}")
    return value


def require_list(value: Any, name: str, source: Path) -> list[Any]:
    if not isinstance(value, list):
        raise HistoryError(f"{source}: {name} must be an array")
    return value


def distribution_value(
    value: Any,
    name: str,
    statistic: str,
    trials: int,
    source: Path,
    *,
    minimum: int = 1,
) -> int:
    distribution = require_mapping(value, name, source)
    samples = require_positive_integer(distribution.get("samples"), f"{name}.samples", source)
    if samples != trials:
        raise HistoryError(
            f"{source}: {name} samples ({samples}) do not match trials ({trials})"
        )
    return require_integer(
        distribution.get(statistic),
        f"{name}.{statistic}",
        source,
        minimum=minimum,
    )


def distribution_median(
    value: Any,
    name: str,
    trials: int,
    source: Path,
    *,
    minimum: int = 1,
) -> int | float:
    distribution = require_mapping(value, name, source)
    samples = require_positive_integer(distribution.get("samples"), f"{name}.samples", source)
    if samples != trials:
        raise HistoryError(
            f"{source}: {name} samples ({samples}) do not match trials ({trials})"
        )
    return require_number(
        distribution.get("median"),
        f"{name}.median",
        source,
        minimum=minimum,
    )


def load_detailed_trace(
    document: dict[str, Any],
    trials: int,
    source: Path,
) -> tuple[int | float | None, tuple[WarmupStep, ...], tuple[GuestMilestone, ...]]:
    raw_trace = document.get("warmup_trace")
    if raw_trace is None:
        return None, (), ()
    trace = require_mapping(raw_trace, "warmup_trace", source)
    trace_median_ns = distribution_median(
        trace.get("total_ns"),
        "warmup_trace.total_ns",
        trials,
        source,
    )
    steps: list[WarmupStep] = []
    for index, raw_step in enumerate(
        require_list(trace.get("steps"), "warmup_trace.steps", source)
    ):
        name = f"warmup_trace.steps[{index}]"
        step = require_mapping(raw_step, name, source)
        steps.append(
            WarmupStep(
                name=require_string(step.get("name"), f"{name}.name", source),
                occurrences=require_positive_integer(
                    step.get("occurrences_per_trial"),
                    f"{name}.occurrences_per_trial",
                    source,
                ),
                median_started_ns=distribution_median(
                    step.get("started_offset_ns"),
                    f"{name}.started_offset_ns",
                    trials,
                    source,
                    minimum=0,
                ),
                median_duration_ns=distribution_median(
                    step.get("duration_ns"),
                    f"{name}.duration_ns",
                    trials,
                    source,
                ),
                p95_duration_ns=distribution_value(
                    step.get("duration_ns"),
                    f"{name}.duration_ns",
                    "p95_nearest_rank",
                    trials,
                    source,
                ),
            )
        )
    milestones: list[GuestMilestone] = []
    for index, raw_milestone in enumerate(
        require_list(
            # Only cold-boot-era summaries carry guest milestones.
            trace.get("guest_milestones", []),
            "warmup_trace.guest_milestones",
            source,
        )
    ):
        name = f"warmup_trace.guest_milestones[{index}]"
        milestone = require_mapping(raw_milestone, name, source)
        milestones.append(
            GuestMilestone(
                name=require_string(milestone.get("name"), f"{name}.name", source),
                median_uptime_ns=distribution_median(
                    milestone.get("guest_uptime_ns"),
                    f"{name}.guest_uptime_ns",
                    trials,
                    source,
                ),
                p95_uptime_ns=distribution_value(
                    milestone.get("guest_uptime_ns"),
                    f"{name}.guest_uptime_ns",
                    "p95_nearest_rank",
                    trials,
                    source,
                ),
            )
        )
    return trace_median_ns, tuple(steps), tuple(milestones)


def load_entry(source: Path) -> WarmupEntry:
    match = DATE_PREFIX.match(source.name)
    if match is None:
        raise HistoryError(f"{source}: filename must start with YYYY-MM-DD-")
    try:
        measured_on = date.fromisoformat(match.group("date"))
    except ValueError as error:
        raise HistoryError(f"{source}: filename has an invalid date") from error

    try:
        document = json.loads(source.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise HistoryError(f"{source}: cannot load JSON: {error}") from error
    document = require_mapping(document, "summary", source)
    if document.get("schema_version") != 1:
        raise HistoryError(f"{source}: summary must be schema version 1")
    raw_measured_at = document.get("measured_at")
    if raw_measured_at is None:
        measured_at = datetime.combine(measured_on, time.min, timezone.utc)
    else:
        measured_at_text = require_string(raw_measured_at, "measured_at", source)
        try:
            measured_at = datetime.fromisoformat(
                measured_at_text.replace("Z", "+00:00")
            )
        except ValueError as error:
            raise HistoryError(
                f"{source}: measured_at must be an RFC 3339 timestamp"
            ) from error
        if (
            measured_at.tzinfo is None
            or measured_at.utcoffset() != timezone.utc.utcoffset(measured_at)
        ):
            raise HistoryError(f"{source}: measured_at must use UTC")
        if measured_at.date() != measured_on:
            raise HistoryError(
                f"{source}: measured_at date must match the filename date"
            )

    compatibility = require_mapping(
        document.get("compatibility"), "compatibility", source
    )
    vm = require_mapping(compatibility.get("vm"), "compatibility.vm", source)
    lifecycle = require_mapping(document.get("lifecycle"), "lifecycle", source)
    startup = require_mapping(
        lifecycle.get("startup_duration_ns"),
        "lifecycle.startup_duration_ns",
        source,
    )

    trials = require_positive_integer(document.get("trials"), "trials", source)
    samples = require_positive_integer(
        startup.get("samples"),
        "lifecycle.startup_duration_ns.samples",
        source,
    )
    if samples != trials:
        raise HistoryError(
            f"{source}: startup samples ({samples}) do not match trials ({trials})"
        )
    trace_median_ns, steps, guest_milestones = load_detailed_trace(
        document,
        trials,
        source,
    )

    entry = WarmupEntry(
        measured_on=measured_on,
        measured_at=measured_at,
        host=require_string(compatibility.get("host"), "compatibility.host", source),
        profile=require_string(
            compatibility.get("profile"), "compatibility.profile", source
        ),
        image_id=optional_image_id(compatibility.get("image_id"), source),
        vcpus=require_positive_integer(
            vm.get("vcpus"), "compatibility.vm.vcpus", source
        ),
        memory_mib=require_positive_integer(
            vm.get("memory_mib"), "compatibility.vm.memory_mib", source
        ),
        trials=trials,
        minimum_ns=require_positive_integer(
            startup.get("min"), "lifecycle.startup_duration_ns.min", source
        ),
        median_ns=require_number(
            startup.get("median"),
            "lifecycle.startup_duration_ns.median",
            source,
            minimum=1,
        ),
        p95_ns=require_positive_integer(
            startup.get("p95_nearest_rank"),
            "lifecycle.startup_duration_ns.p95_nearest_rank",
            source,
        ),
        maximum_ns=require_positive_integer(
            startup.get("max"), "lifecycle.startup_duration_ns.max", source
        ),
        trace_median_ns=trace_median_ns,
        steps=steps,
        guest_milestones=guest_milestones,
        series_id=require_string(document.get("series_id"), "series_id", source),
        source=source,
    )
    if not (
        entry.minimum_ns
        <= entry.median_ns
        <= entry.p95_ns
        <= entry.maximum_ns
    ):
        raise HistoryError(f"{source}: startup distribution is not ordered")
    return entry


def load_history(directory: Path) -> list[WarmupEntry]:
    try:
        sources = sorted(directory.glob("*.json"))
    except OSError as error:
        raise HistoryError(f"{directory}: cannot list history: {error}") from error
    if not sources:
        raise HistoryError(f"{directory}: no benchmark summaries found")
    entries: list[WarmupEntry] = []
    for source in sources:
        try:
            document = json.loads(source.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise HistoryError(f"{source}: cannot load JSON: {error}") from error
        document = require_mapping(document, "summary", source)
        if document.get("benchmark") in RETIRED_BENCHMARKS:
            continue
        entries.append(load_entry(source))
    if not entries:
        raise HistoryError(f"{directory}: no VM warmup benchmark summaries found")
    series_ids = [entry.series_id for entry in entries]
    if len(series_ids) != len(set(series_ids)):
        raise HistoryError(f"{directory}: duplicate benchmark series ID")
    return sorted(entries, key=lambda entry: (entry.measured_at, entry.series_id))


def seconds(nanoseconds: int | float) -> str:
    return f"{nanoseconds / 1_000_000_000:.3f}"


def short_image_id(image_id: str | None) -> str:
    if image_id is None:
        return "unrecorded"
    return image_id[:19]


def render_markdown(entries: list[WarmupEntry]) -> str:
    lines = [
        "| Date | Host | Profile | Image | VM | Trials | Median | p95* | Min | Max |",
        "|---|---|---|---|---:|---:|---:|---:|---:|---:|",
    ]
    for entry in entries:
        lines.append(
            "| "
            f"{entry.measured_on.isoformat()} | {entry.host} | {entry.profile} | "
            f"`{short_image_id(entry.image_id)}` | "
            f"{entry.vcpus} vCPU / {entry.memory_mib} MiB | {entry.trials} | "
            f"{seconds(entry.median_ns)} s | {seconds(entry.p95_ns)} s | "
            f"{seconds(entry.minimum_ns)} s | {seconds(entry.maximum_ns)} s |"
        )
    lines.extend(
        [
            "",
            "\\* p95 is the nearest-rank value. Treat it as directional when the "
            "trial count is small.",
        ]
    )
    latest = entries[-1]
    if latest.trace_median_ns is not None:
        lines.extend(
            [
                "",
                f"### Latest detailed breakdown ({latest.measured_on.isoformat()})",
                "",
                "| Host step | Occurrences | Median start | Median duration | p95 duration | Share |",
                "|---|---:|---:|---:|---:|---:|",
            ]
        )
        for step in sorted(
            latest.steps,
            key=lambda item: (-item.median_duration_ns, item.name),
        ):
            share = step.median_duration_ns * 100 / latest.trace_median_ns
            lines.append(
                f"| `{step.name}` | {step.occurrences} | "
                f"{seconds(step.median_started_ns)} s | "
                f"{seconds(step.median_duration_ns)} s | "
                f"{seconds(step.p95_duration_ns)} s | {share:.1f}% |"
            )
    if latest.trace_median_ns is not None and latest.guest_milestones:
        lines.extend(
            [
                "",
                "| Guest milestone | Median boot uptime | p95 boot uptime |",
                "|---|---:|---:|",
            ]
        )
        for milestone in sorted(
            latest.guest_milestones,
            key=lambda item: (item.median_uptime_ns, item.name),
        ):
            lines.append(
                f"| `{milestone.name}` | "
                f"{seconds(milestone.median_uptime_ns)} s | "
                f"{seconds(milestone.p95_uptime_ns)} s |"
            )
    return "\n".join(lines) + "\n"


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Report VM startup/warmup latency from tracked summaries."
    )
    parser.add_argument(
        "history",
        nargs="?",
        type=Path,
        default=DEFAULT_HISTORY,
        help=f"history directory (default: {DEFAULT_HISTORY})",
    )
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        sys.stdout.write(render_markdown(load_history(args.history)))
    except HistoryError as error:
        print(f"report-vm-warmup: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
