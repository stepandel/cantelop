#!/usr/bin/env python3
"""Compare two results from the same five-axis host-capacity workload."""

import argparse
import json
from pathlib import Path


METRICS = (
    ("CPU mean cores", ("cpu", "mean_host_cores_used"), None),
    ("CPU pressure avg10 max", ("cpu", "host", "maximum_cpu_pressure_avg10"), "lower"),
    ("Memory peak MiB", ("memory", "host", "maximum_service_memory_mib"), "lower"),
    ("Memory pressure avg10 max", ("memory", "host", "maximum_memory_pressure_avg10"), "lower"),
    ("Workspace write p95 ms", ("workspace", "write_ms", "p95"), "lower"),
    ("Workspace fsync p95 ms", ("workspace", "fsync_ms", "p95"), "lower"),
    ("Workspace read p95 ms", ("workspace", "read_ms", "p95"), "lower"),
    ("Workspace MiB/s", ("workspace", "throughput_mib_per_second"), "higher"),
    ("Startup VMs/min", ("startup_throughput", "vms_per_minute"), "higher"),
    ("Startup p95 ms", ("startup_time", "request_ms", "p95"), "lower"),
)


def load(path: Path) -> dict:
    result = json.loads(path.read_text())
    if result.get("schema_version") != 1 or not isinstance(result.get("axes"), dict):
        raise ValueError(f"unsupported capacity result: {path}")
    return result


def comparable(baseline: dict, candidate: dict) -> None:
    if baseline.get("profile") != candidate.get("profile"):
        raise ValueError("results use different workload profiles")
    for key in ("host", "build_id", "guest_memory_mib", "guest_vcpus"):
        if baseline.get("identity", {}).get(key) != candidate.get("identity", {}).get(key):
            raise ValueError(f"results differ in {key}")


def value(result: dict, path: tuple[str, ...]):
    current = result.get("axes", {})
    for key in path:
        if not isinstance(current, dict):
            return None
        current = current.get(key)
    return current if isinstance(current, (int, float)) else None


def regression_percent(before: float, after: float, direction: str | None):
    if direction is None:
        return None
    difference = after - before if direction == "lower" else before - after
    if before == 0:
        return float("inf") if difference > 0 else 0.0
    return round(100 * difference / abs(before), 1)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("--max-regression-percent", type=float)
    args = parser.parse_args(argv)
    if args.max_regression_percent is not None and args.max_regression_percent < 0:
        parser.error("--max-regression-percent cannot be negative")
    try:
        baseline, candidate = load(args.baseline), load(args.candidate)
        comparable(baseline, candidate)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        parser.error(str(error))
    print(f"Host: {candidate['identity']['host']}  "
          f"VMs: {candidate['profile']['target_vms']}  "
          f"NFS threads: {baseline['identity']['nfs_threads']} -> "
          f"{candidate['identity']['nfs_threads']}")
    print(f"Run status: {baseline.get('passed')} -> {candidate.get('passed')}")
    print(f"{'Metric':34} {'Baseline':>12} {'Candidate':>12} {'Regression':>12}")
    failures = []
    for label, path, direction in METRICS:
        before, after = value(baseline, path), value(candidate, path)
        if before is None or after is None:
            print(f"{label:34} {'incomplete':>12}")
            failures.append(label + " incomplete")
            continue
        change = regression_percent(before, after, direction)
        shown = "n/a" if change is None else ("infinite" if change == float("inf") else f"{change:+.1f}%")
        print(f"{label:34} {before:12.3f} {after:12.3f} {shown:>12}")
        if (args.max_regression_percent is not None and change is not None
                and change > args.max_regression_percent):
            failures.append(label)
    if not baseline.get("passed") or not candidate.get("passed"):
        failures.append("one or both runs failed")
    if failures:
        print("Unqualified comparison: " + ", ".join(failures))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
