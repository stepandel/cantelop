#!/usr/bin/env python3
"""Exercise gateway Session allocation and live guest memory growth/shrink.

Run on the compute host with a static Linux amd64 pressure binary. The script
uses the host's private loopback APIs and always releases its Session.
"""

import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def api(base, method, path, headers, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(
        base + path,
        data=data,
        headers={**headers, **({"Content-Type": "application/json"} if data else {})},
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=90) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        body = error.read().decode(errors="replace")
        try:
            return error.code, json.loads(body)
        except ValueError:
            return error.code, body


def require(status, expected, result, operation):
    if status != expected:
        raise RuntimeError(f"{operation}: HTTP {status}: {result}")
    return result


def wait_for_memory(control, renew_lease, sandbox_id, predicate, timeout, label,
                    host_memory_ceiling_bytes=None):
    deadline = time.monotonic() + timeout
    next_renewal = time.monotonic() + 60
    last = None
    while time.monotonic() < deadline:
        if time.monotonic() >= next_renewal:
            renew_lease()
            next_renewal = time.monotonic() + 60
        status, result = control("GET", "/v1/sandbox-usage")
        require(status, 200, result, "read usage")
        entry = next(
            (entry for entry in result["items"]
             if entry["sandbox_id"] == sandbox_id),
            None,
        )
        last = entry
        if entry is not None and predicate(entry["guest_memory_mib"]) and (
                host_memory_ceiling_bytes is None or
                entry["memory_current_bytes"] <= host_memory_ceiling_bytes):
            print(f"{label}: guest {entry['guest_memory_mib']} MiB, "
                  f"host {entry['memory_current_bytes'] / 1048576:.1f} MiB", flush=True)
            return entry
        time.sleep(3)
    raise RuntimeError(f"{label}: timed out; last guest memory was {last} MiB")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for option in ("app-id", "workspace-id", "hostname", "pressure-binary", "guest-key"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--control-token", default="/etc/fire-fuse/control-clients/operator.token")
    parser.add_argument("--edge-token", default="/etc/fire-fuse-gateway/edge-ingress.token")
    args = parser.parse_args()
    if not Path(args.pressure_binary).is_file() or not Path(args.guest_key).is_file():
        parser.error("the pressure binary and guest SSH key must exist")
    if shutil.which("ssh") is None:
        parser.error("ssh is required on the compute host")
    session_id = "ses_elastic" + uuid.uuid4().hex
    lease_id = "lease_" + uuid.uuid4().hex
    control_token = Path(args.control_token).read_text(encoding="ascii").strip()
    edge_token = Path(args.edge_token).read_text(encoding="ascii").strip()

    def control(method, path, body=None):
        return api("http://127.0.0.1:8080", method, path,
                   {"Authorization": "Bearer " + control_token}, body)

    def edge(method, path, body=None):
        return api("http://127.0.0.1:8081", method, path, {
            "X-Cantelop-Edge-Dispatch": "1",
            "X-Cantelop-Edge-Authorization": "Bearer " + edge_token,
            "X-Cantelop-Edge-Request-ID": "req_" + uuid.uuid4().hex,
            "X-Cantelop-Edge-App-ID": args.app_id,
            "X-Cantelop-Edge-Hostname": args.hostname,
        }, body)

    created = False
    leased = False
    ssh = None
    try:
        status, session = edge("POST", "/__cantelop/v1/sessions", {
            "id": session_id,
            "workspace_id": args.workspace_id,
            "keep_alive_seconds": 900,
        })
        require(status, 202, session, "create Session through gateway")
        created = True
        if session.get("id") != session_id:
            raise RuntimeError("gateway returned a different Session")
        print(f"Session created: {session_id}", flush=True)

        status, lease = control("POST", f"/v1/sessions/{session_id}/work-leases", {
            "lease_id": lease_id, "ttl_seconds": 300,
        })
        require(status, 200, lease, "create work lease")
        leased = True
        if lease.get("lease_id") != lease_id:
            raise RuntimeError("control API returned a different work lease")

        def renew_lease():
            status, result = control(
                "POST", f"/v1/sessions/{session_id}/work-leases/{lease_id}/renew",
                {"ttl_seconds": 300})
            require(status, 200, result, "renew work lease")

        status, route = control("GET", f"/v1/sessions/{session_id}/route")
        require(status, 200, route, "read Session route")
        sandbox_id = route["sandbox_id"]
        status, machine = control("GET", f"/v1/sandboxes/{sandbox_id}")
        require(status, 200, machine, "read allocated Sandbox")
        if machine["memory_mib"] != 512 or machine["memory_max_mib"] != 4608:
            raise RuntimeError(f"unexpected memory shape: {machine['memory_mib']}, {machine['memory_max_mib']}")
        print(f"Sandbox allocated: {sandbox_id}, 512 MiB boot, 4 GiB hotplug", flush=True)

        endpoint = route["endpoint"]
        runtime_url = endpoint["scheme"] + "://" + endpoint["authority"] + "/__cantelop/v2/runtime"
        runtime_request = urllib.request.Request(
            runtime_url, headers={"X-Cantelop-Sandbox-ID": sandbox_id})

        def check_runtime():
            with urllib.request.urlopen(runtime_request, timeout=10) as response:
                runtime = json.load(response)
            if runtime.get("sandbox_id") != sandbox_id or runtime.get("protocol") != 2:
                raise RuntimeError("live app runtime contract mismatch")

        check_runtime()
        print("Live app runtime responded", flush=True)
        guest_ip = machine["network"]["guest_ip"]
        for _ in range(20):
            try:
                with socket.create_connection((guest_ip, 19095), timeout=2) as connection:
                    connection.settimeout(2)
                    if not connection.recv(80):
                        raise RuntimeError("guest memory status was empty")
                break
            except OSError:
                time.sleep(1)
        else:
            raise RuntimeError("guest memory status service is unreachable")

        ssh = ["ssh", "-i", args.guest_key, "-o", "BatchMode=yes",
               "-o", "ConnectTimeout=5", "-o", "IdentitiesOnly=yes",
               "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
               "root@" + guest_ip]
        baseline = wait_for_memory(control, renew_lease, sandbox_id,
                                   lambda memory: memory == 512, 30, "baseline")
        # Guest free pages in fixed boot RAM can remain physically resident.
        # Require the hotplug footprint to disappear from real host consumption,
        # allowing boot RAM plus bounded VMM/kernel overhead.
        host_memory_ceiling = max(baseline["memory_current_bytes"], 512 * 1048576) + 128 * 1048576
        with Path(args.pressure_binary).open("rb") as binary:
            subprocess.run(ssh + [
                "cat > /run/elastic-memory-pressure && chmod 0755 /run/elastic-memory-pressure "
                "&& systemd-run --unit=elastic-memory-pressure --collect /run/elastic-memory-pressure"
            ], stdin=binary, check=True, timeout=30, capture_output=True)
        wait_for_memory(control, renew_lease, sandbox_id,
                        lambda memory: memory >= 768, 180, "memory grew")
        subprocess.run(ssh + ["systemctl stop elastic-memory-pressure.service"],
                       check=True, timeout=30, capture_output=True)
        # Growth adds up to 1 GiB per request and shrink requires five idle
        # minutes per 256 MiB step, so size the timeout from how far memory grew
        # once in-flight growth settles, plus unplug latency.
        time.sleep(5)
        grown = wait_for_memory(control, renew_lease, sandbox_id,
                                lambda memory: True, 30, "pressure stopped")
        shrink_steps = max(1, -(-(grown["guest_memory_mib"] - 512) // 256))
        shrink_timeout = shrink_steps * 300 + 120
        wait_for_memory(control, renew_lease, sandbox_id,
                        lambda memory: memory == 512, shrink_timeout, "memory shrank",
                        host_memory_ceiling_bytes=host_memory_ceiling)
        check_runtime()
        print("Live app runtime responded after resize", flush=True)
    finally:
        if ssh is not None:
            try:
                subprocess.run(ssh + ["systemctl stop elastic-memory-pressure.service 2>/dev/null || true; "
                                      "rm -f /run/elastic-memory-pressure"],
                               timeout=30, capture_output=True, check=False)
            except (OSError, subprocess.TimeoutExpired):
                pass
        try:
            if leased:
                status, result = control("DELETE", f"/v1/sessions/{session_id}/work-leases/{lease_id}")
                require(status, 200, result, "release work lease")
        finally:
            if created:
                status, result = edge("DELETE", f"/__cantelop/v1/sessions/{session_id}")
                require(status, 200, result, "delete Session")
                print("Session deleted", flush=True)


if __name__ == "__main__":
    main()
