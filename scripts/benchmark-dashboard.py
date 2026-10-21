#!/usr/bin/env python3
"""Serve a local development dashboard for the tracked benchmark history.

The server reads ``tests/benchmarks/history/*.json`` on every request, so refreshing
the browser picks up newly recorded series without a restart. Each summary is
validated with report-vm-warmup.py's parser and served as normalized JSON, so
the dashboard and the Markdown report agree on which series are valid. It has
no third-party dependencies and binds to localhost by default.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


def load_report_module() -> Any:
    """Load report-vm-warmup.py, the one parser for tracked warmup history."""
    path = Path(__file__).resolve().parent / "report-vm-warmup.py"
    spec = importlib.util.spec_from_file_location("report_vm_warmup", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules.setdefault(spec.name, module)
    spec.loader.exec_module(module)
    return module


REPORT = load_report_module()
DEFAULT_HISTORY = REPORT.DEFAULT_HISTORY
STATIC_ROOT = Path(__file__).resolve().parent / "dashboard"
STATIC_FILES = {
    "/": ("index.html", "text/html; charset=utf-8"),
    "/app.js": ("app.js", "text/javascript; charset=utf-8"),
    "/style.css": ("style.css", "text/css; charset=utf-8"),
}


def entry_payload(entry: Any, document: dict[str, Any]) -> dict[str, Any]:
    """Serialize a validated WarmupEntry, plus the panels the report omits."""
    source = entry.source
    lifecycle = REPORT.require_mapping(document.get("lifecycle"), "lifecycle", source)
    shutdown = lifecycle.get("shutdown_duration_ns")
    shutdown_median_ns = None
    if shutdown is not None:
        shutdown_median_ns = REPORT.require_number(
            REPORT.require_mapping(
                shutdown, "lifecycle.shutdown_duration_ns", source
            ).get("median"),
            "lifecycle.shutdown_duration_ns.median",
            source,
            minimum=0,
        )
    metrics = REPORT.require_list(document.get("metrics", []), "metrics", source)
    for index, metric in enumerate(metrics):
        REPORT.require_mapping(metric, f"metrics[{index}]", source)
    trace = None
    if entry.trace_median_ns is not None:
        trace = {
            "median_ns": entry.trace_median_ns,
            "steps": [
                {
                    "name": step.name,
                    "occurrences": step.occurrences,
                    "median_started_ns": step.median_started_ns,
                    "median_duration_ns": step.median_duration_ns,
                    "p95_duration_ns": step.p95_duration_ns,
                }
                for step in entry.steps
            ],
            "guest_milestones": [
                {
                    "name": milestone.name,
                    "median_uptime_ns": milestone.median_uptime_ns,
                    "p95_uptime_ns": milestone.p95_uptime_ns,
                }
                for milestone in entry.guest_milestones
            ],
        }
    return {
        "file": source.name,
        "measured_on": entry.measured_on.isoformat(),
        "measured_at": entry.measured_at.isoformat(),
        "series_id": entry.series_id,
        "host": entry.host,
        "profile": entry.profile,
        "image_id": entry.image_id,
        "vcpus": entry.vcpus,
        "memory_mib": entry.memory_mib,
        "trials": entry.trials,
        "startup": {
            "min_ns": entry.minimum_ns,
            "median_ns": entry.median_ns,
            "p95_ns": entry.p95_ns,
            "max_ns": entry.maximum_ns,
        },
        "shutdown_median_ns": shutdown_median_ns,
        "trace": trace,
        "metrics": metrics,
    }


def load_series(source: Path) -> dict[str, Any] | None:
    """Validate one summary with the report parser; None for retired series."""
    try:
        document = json.loads(source.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise REPORT.HistoryError(f"{source}: cannot load JSON: {error}") from error
    document = REPORT.require_mapping(document, "summary", source)
    if document.get("benchmark") in REPORT.RETIRED_BENCHMARKS:
        return None
    return entry_payload(REPORT.load_entry(source), document)


def build_history_payload(directory: Path) -> dict[str, Any]:
    """Build the /api/history response: normalized series plus file errors."""
    try:
        sources = sorted(directory.glob("*.json"))
    except OSError as error:
        return {"history_dir": str(directory), "series": [], "errors": [str(error)]}
    series: list[dict[str, Any]] = []
    errors: list[str] = []
    for source in sources:
        try:
            entry = load_series(source)
        except REPORT.HistoryError as error:
            message = str(error).removeprefix(f"{source}: ")
            errors.append(f"{source.name}: {message}")
            continue
        if entry is not None:
            series.append(entry)
    series.sort(key=lambda item: (item["measured_at"], item["series_id"]))
    return {"history_dir": str(directory), "series": series, "errors": errors}


class DashboardHandler(BaseHTTPRequestHandler):
    history_dir: Path = DEFAULT_HISTORY

    def do_GET(self) -> None:  # noqa: N802 - http.server API
        path = self.path.split("?", 1)[0]
        if path == "/api/history":
            body = json.dumps(build_history_payload(self.history_dir)).encode("utf-8")
            self.send_body(body, "application/json; charset=utf-8")
            return
        static = STATIC_FILES.get(path)
        if static is None:
            self.send_error(HTTPStatus.NOT_FOUND, "unknown path")
            return
        file_name, content_type = static
        try:
            body = (STATIC_ROOT / file_name).read_bytes()
        except OSError:
            self.send_error(HTTPStatus.NOT_FOUND, f"missing asset {file_name}")
            return
        self.send_body(body, content_type)

    def send_body(self, body: bytes, content_type: str) -> None:
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: Any) -> None:
        sys.stderr.write(f"{self.address_string()} {format % args}\n")


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Serve the local benchmark history dashboard."
    )
    parser.add_argument(
        "history",
        nargs="?",
        type=Path,
        default=DEFAULT_HISTORY,
        help=f"history directory (default: {DEFAULT_HISTORY})",
    )
    parser.add_argument("--host", default="127.0.0.1", help="bind address")
    parser.add_argument("--port", type=int, default=8321, help="listen port")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    handler = type(
        "BoundDashboardHandler",
        (DashboardHandler,),
        {"history_dir": args.history},
    )
    server = ThreadingHTTPServer((args.host, args.port), handler)
    print(f"benchmark dashboard: http://{args.host}:{args.port}/")
    print(f"serving history from {args.history}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
