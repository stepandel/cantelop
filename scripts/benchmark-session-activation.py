#!/usr/bin/env python3
"""Measure a dedicated Session activation fixture through actual handler entry."""
import argparse
import json
import math
from pathlib import Path
import statistics
import time
import urllib.request
import urllib.parse
import uuid


def distribution(values):
    values = sorted(values)
    return {"samples": len(values), "min": values[0], "median": statistics.median(values),
            "p95_nearest_rank": values[math.ceil(.95 * len(values)) - 1], "max": values[-1]}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


def post(origin, token, path, body):
    request = urllib.request.Request(origin + path, data=json.dumps(body).encode(),
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/json",
                                              "User-Agent": "cantelop-session-activation-benchmark/1.0"})
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
        return json.load(response)


def validate_trial(result, trial_id):
    if result.get("trialId") != trial_id:
        raise ValueError("trial identity mismatch")
    start, submitted, response = (result.get(k) for k in ("handlerStartedAt", "submittedAt", "responseMs"))
    if any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) for v in (start, submitted, response)):
        raise ValueError("invalid milestone timings")
    activation = start - submitted
    if not 0 <= activation <= response:
        raise ValueError("handler timestamp outside request window; check clock synchronization")
    return {"trial_id": trial_id, "handler_start_ms": activation, "response_ms": response}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--origin", required=True)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--route-prefix", default="/webhooks", choices=["", "/webhooks"])
    parser.add_argument("--sdk-version", required=True)
    parser.add_argument("--fresh-workspaces", action="store_true")
    parser.add_argument("--trials", type=int, default=10)
    parser.add_argument("--platform-revision", required=True)
    parser.add_argument("--build-id", required=True)
    parser.add_argument("--confirm-dedicated-app", action="store_true", required=True)
    args = parser.parse_args()
    parsed = urllib.parse.urlsplit(args.origin)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or
            parsed.path not in ("", "/") or parsed.query or parsed.fragment or not 2 <= args.trials <= 100):
        parser.error("use an HTTPS fixture origin and 2–100 trials")
    token = args.token_file.read_text().strip()
    if len(token) < 32:
        parser.error("benchmark token must contain at least 32 characters")
    origin = args.origin.rstrip("/")
    post(origin, token, args.route_prefix + "/prepare", {})
    trials = []
    for _ in range(args.trials):
        trial_id = str(uuid.uuid4())
        started = time.perf_counter()
        result = post(origin, token, args.route_prefix + "/trial", {"trialId": trial_id, **({"freshWorkspace": True} if args.fresh_workspaces else {})})
        trial = validate_trial(result, trial_id)
        if "workspaceSlug" in result: trial["workspace_slug"] = result["workspaceSlug"]
        trial["client_round_trip_ms"] = (time.perf_counter() - started) * 1000
        trials.append(trial)
        print(f"trial {len(trials)}: handler start {trial['handler_start_ms']:.1f} ms", flush=True)
    result = {"schema_version": 1, "kind": "session-handler-activation", "origin": origin,
              "sdk_version": args.sdk_version, "workspace_mode": "fresh" if args.fresh_workspaces else "prepared", "platform_revision": args.platform_revision,
              "build_id": args.build_id, "trials": trials,
              "summary": {name: distribution([t[name] for t in trials]) for name in
                          ("handler_start_ms", "response_ms", "client_round_trip_ms")}}
    args.output.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
