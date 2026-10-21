#!/usr/bin/env python3
"""Repeatable five-axis Fire Fuse capacity benchmark. Run only on a drained host."""

from __future__ import annotations

import argparse
import concurrent.futures
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shlex
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

CONFIG_PATH = Path("/etc/fire-fuse/config.json")
TOKEN_PATH = Path("/etc/fire-fuse/control-clients/operator.token")
CGROUP = Path("/sys/fs/cgroup/system.slice/fire-fuse.service")
NFS_POOL_STATS = Path("/proc/fs/nfsd/pool_stats")
NFS_CLIENTS = Path("/proc/fs/nfsd/clients")
RUNTIME = "http://127.0.0.1:8080"
COMPUTE = "http://10.77.0.1:8084"
TOKEN = ""
SSH_KEY = ""


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def percentile(values: list[float], percent: int) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    return round(ordered[math.ceil(len(ordered) * percent / 100) - 1], 3)


def distribution(values: list[float]) -> dict:
    return {
        "count": len(values),
        "p50": percentile(values, 50),
        "p95": percentile(values, 95),
        "p99": percentile(values, 99),
        "max": round(max(values), 3) if values else None,
    }


def load_profile(path: Path) -> dict:
    profile = json.loads(path.read_text())
    fields = {
        "schema_version": (1, 1),
        "target_vms": (1, 500),
        "startup_concurrency": (1, 16),
        "cpu_active_vms": (1, 500),
        "cpu_workers_per_vm": (1, 4),
        "cpu_seconds": (2, 60),
        "memory_active_vms": (1, 500),
        "memory_mib_per_vm": (1, 1024),
        "memory_hold_seconds": (2, 60),
        "workspace_active_vms": (1, 500),
        "workspace_bytes_per_vm": (4096, 67108864),
        "minimum_available_mib": (8192, 1048576),
        "maximum_swap_used_mib": (0, 1048576),
    }
    if not isinstance(profile, dict) or set(profile) != set(fields):
        raise ValueError("profile must contain exactly the documented schema-v1 fields")
    for key, (minimum, maximum) in fields.items():
        value = profile[key]
        if type(value) is not int or not minimum <= value <= maximum:
            raise ValueError(f"{key} must be an integer between {minimum} and {maximum}")
    target = profile["target_vms"]
    for key in ("cpu_active_vms", "memory_active_vms", "workspace_active_vms"):
        if profile[key] > target:
            raise ValueError(f"{key} cannot exceed target_vms")
    if profile["workspace_bytes_per_vm"] % 4096:
        raise ValueError("workspace_bytes_per_vm must be a multiple of 4096")
    return profile


def request(method: str, url: str, payload=None, timeout=90):
    data = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Authorization": "Bearer " + TOKEN, "Content-Type": "application/json",
    })
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=timeout) as res:
        body = res.read()
        return json.loads(body) if body else {}


def live_sandboxes() -> list[dict]:
    return [item for item in request("GET", RUNTIME + "/v1/sandboxes")["items"]
            if item["state"] != "terminated"]


def meminfo() -> dict[str, int]:
    result = {}
    for line in Path("/proc/meminfo").read_text().splitlines():
        key, rest = line.split(":", 1)
        if key in ("MemTotal", "MemAvailable", "SwapTotal", "SwapFree"):
            result[key] = int(rest.split()[0]) // 1024
    return result


def cpu_stat() -> dict[str, int]:
    values = dict(line.split() for line in (CGROUP / "cpu.stat").read_text().splitlines())
    return {key: int(values[key]) for key in ("usage_usec", "nr_throttled", "throttled_usec")}


def oom_kills() -> int:
    values = dict(line.split() for line in (CGROUP / "memory.events").read_text().splitlines())
    return int(values["oom_kill"])


def nfs_pool() -> dict[str, int]:
    lines = [line for line in NFS_POOL_STATS.read_text().splitlines() if not line.startswith("#")]
    if len(lines) != 1:
        raise ValueError("expected one NFS thread pool")
    values = lines[0].split()
    return {"packets_arrived": int(values[1]), "sockets_enqueued": int(values[2]),
            "threads_woken": int(values[3])}


def nfs_client_identity(sandbox_ids: set[str]) -> dict:
    names = []
    for path in NFS_CLIENTS.glob("*/info"):
        match = re.search(r'name: "([^"]+)"', path.read_text())
        if match:
            names.append(match.group(1))
    missing = sorted(sandbox for sandbox in sandbox_ids if not any(
        name.startswith("Linux NFSv4.2 " + sandbox + "/") for name in names))
    return {"expected": len(sandbox_ids), "matched": len(sandbox_ids) - len(missing),
            "missing": missing}


def pressure(path: Path) -> float:
    some = path.read_text().splitlines()[0]
    return float(some.split("avg10=")[1].split()[0])


def sample() -> dict:
    memory = meminfo()
    return {
        "epoch": time.time(),
        "available_mib": memory["MemAvailable"],
        "swap_used_mib": memory["SwapTotal"] - memory["SwapFree"],
        "service_memory_mib": int((CGROUP / "memory.current").read_text()) // 1048576,
        "cpu_pressure_avg10": pressure(CGROUP / "cpu.pressure"),
        "memory_pressure_avg10": pressure(CGROUP / "memory.pressure"),
        "oom_kills": oom_kills(),
        "cpu": cpu_stat(),
        "nfs": nfs_pool(),
    }


class Monitor:
    def __init__(self, profile: dict):
        self.profile = profile
        self.samples: list[dict] = []
        self.stop = threading.Event()
        self.abort = threading.Event()
        self.baseline_oom = oom_kills()

    def run(self):
        while not self.stop.is_set():
            try:
                current = sample()
                self.samples.append(current)
                if (current["available_mib"] < self.profile["minimum_available_mib"]
                        or current["swap_used_mib"] > self.profile["maximum_swap_used_mib"]
                        or current["oom_kills"] > self.baseline_oom):
                    self.abort.set()
            except (OSError, ValueError, KeyError) as error:
                self.samples.append({"epoch": time.time(), "sample_error": str(error)})
                self.abort.set()
            self.stop.wait(0.5)


def wait_slots(target: int, monitor: Monitor) -> dict:
    deadline = time.monotonic() + 300
    last = {}
    while time.monotonic() < deadline:
        if monitor.abort.is_set():
            raise RuntimeError("safety guard tripped while waiting for warm slots")
        last = request("GET", RUNTIME + "/v1/host-capacity")["warm_slots"]
        if last["ready"] >= target:
            return last
        time.sleep(2)
    raise RuntimeError(f"warm slot timeout: {last}")


def guest(ip: str, code: str, args=(), timeout=90) -> tuple[str, float]:
    command = "python3 -c " + shlex.quote(code) + "".join(" " + shlex.quote(str(x)) for x in args)
    started = time.perf_counter()
    result = subprocess.run([
        "ssh", "-i", SSH_KEY, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "-o", "ConnectTimeout=8",
        "root@" + ip, command,
    ], capture_output=True, text=True, timeout=timeout)
    elapsed_ms = (time.perf_counter() - started) * 1000
    if result.returncode:
        raise RuntimeError(f"guest {ip} command failed ({result.returncode}): {result.stderr[-200:]}")
    return result.stdout.strip().splitlines()[-1], elapsed_ms


CPU_CODE = """import json,multiprocessing,sys,time
def spin(until):
    while time.monotonic()<until:pass
workers=int(sys.argv[1]);duration=float(sys.argv[2]);start=time.monotonic();until=start+duration
procs=[multiprocessing.Process(target=spin,args=(until,)) for _ in range(workers)]
for proc in procs:proc.start()
for proc in procs:proc.join()
print(json.dumps({'elapsed_ms':(time.monotonic()-start)*1000}))
"""

MEMORY_CODE = """import json,sys,time
mib=int(sys.argv[1]);seconds=float(sys.argv[2]);start=time.monotonic()
buffer=bytearray(mib*1048576)
for offset in range(0,len(buffer),4096):buffer[offset]=1
time.sleep(seconds)
print(json.dumps({'allocated_mib':mib,'elapsed_ms':(time.monotonic()-start)*1000}))
"""

IO_CODE = """import json,os,sys,tempfile,time
size=int(sys.argv[1]);root='/workspace';st=os.stat(root)
os.setgid(st.st_gid);os.setuid(st.st_uid);data=os.urandom(4096)
with tempfile.TemporaryFile(mode='w+b',dir=root) as f:
    print(json.dumps({'phase':'write'}),flush=True)
    t=time.monotonic()
    for _ in range(size//4096):f.write(data)
    write_ms=(time.monotonic()-t)*1000
    print(json.dumps({'phase':'fsync'}),flush=True)
    t=time.monotonic();f.flush();os.fsync(f.fileno())
    fsync_ms=(time.monotonic()-t)*1000
    print(json.dumps({'phase':'read'}),flush=True)
    os.posix_fadvise(f.fileno(),0,0,os.POSIX_FADV_DONTNEED)
    t=time.monotonic();f.seek(0);content=f.read()
    read_ms=(time.monotonic()-t)*1000
    if len(content)!=size or content[:4096]!=data or content[-4096:]!=data:
        raise RuntimeError('Workspace readback mismatch')
print(json.dumps({'write_ms':write_ms,'fsync_ms':fsync_ms,'read_ms':read_ms}))
"""


def run_parallel(items: list[dict], code: str, arguments, timeout: int) -> tuple[list[dict], list[dict]]:
    results, errors = [], []
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(items)) as executor:
        futures = {executor.submit(guest, item["guest_ip"], code, arguments(i), timeout): i
                   for i, item in enumerate(items)}
        for future in concurrent.futures.as_completed(futures):
            try:
                output, ssh_ms = future.result()
                result = json.loads(output)
                result["ssh_round_trip_ms"] = round(ssh_ms, 3)
                results.append(result)
            except subprocess.TimeoutExpired as error:
                output = error.stdout or b""
                if isinstance(output, bytes):
                    output = output.decode(errors="replace")
                phases = []
                for line in output.splitlines():
                    try:
                        phases.append(json.loads(line).get("phase"))
                    except (json.JSONDecodeError, AttributeError):
                        pass
                reason = "guest/SSH timeout"
                if phases and phases[-1]:
                    reason += " during " + phases[-1]
                errors.append({"index": futures[future], "reason": reason})
            except Exception as error:
                errors.append({"index": futures[future], "reason": str(error)[-250:]})
    return results, errors


def metric_window(samples: list[dict], start: float, end: float) -> dict:
    window = [s for s in samples if start <= s.get("epoch", 0) <= end and "available_mib" in s]
    if not window:
        return {"sample_count": 0}
    return {
        "sample_count": len(window),
        "minimum_available_mib": min(s["available_mib"] for s in window),
        "maximum_service_memory_mib": max(s["service_memory_mib"] for s in window),
        "maximum_cpu_pressure_avg10": max(s["cpu_pressure_avg10"] for s in window),
        "maximum_memory_pressure_avg10": max(s["memory_pressure_avg10"] for s in window),
        "maximum_swap_used_mib": max(s["swap_used_mib"] for s in window),
    }


def execute_axis(name: str, machines: list[dict], profile: dict, monitor: Monitor) -> dict:
    if monitor.abort.is_set():
        raise RuntimeError("safety guard tripped before " + name)
    active = profile[name + "_active_vms"]
    selected = machines[:active]
    before = sample()
    start = before["epoch"]
    if name == "cpu":
        results, errors = run_parallel(selected, CPU_CODE,
                               lambda _: (profile["cpu_workers_per_vm"], profile["cpu_seconds"]),
                               profile["cpu_seconds"] + 40)
    elif name == "memory":
        results, errors = run_parallel(selected, MEMORY_CODE,
                               lambda _: (profile["memory_mib_per_vm"], profile["memory_hold_seconds"]),
                               profile["memory_hold_seconds"] + 45)
    else:
        # Every VM writes and reads its bytes through the one host gateway.
        io_seconds = 2 * active * profile["workspace_bytes_per_vm"] / (32 * 1048576)
        results, errors = run_parallel(selected, IO_CODE,
                               lambda _: (profile["workspace_bytes_per_vm"],),
                               90 + math.ceil(io_seconds))
    after = sample()
    end = after["epoch"]
    axis = {
        "active_vms": active, "completed_vms": len(results), "errors": errors,
        "elapsed_seconds": round(end - start, 3),
        "host": metric_window([before, *monitor.samples, after], start, end),
        "guest_ssh_round_trip_ms": distribution([r["ssh_round_trip_ms"] for r in results]),
    }
    if name == "cpu":
        usage_seconds = (after["cpu"]["usage_usec"] - before["cpu"]["usage_usec"]) / 1e6
        axis.update({
            "workers_per_vm": profile["cpu_workers_per_vm"],
            "cpu_usage_seconds": round(usage_seconds, 3),
            "mean_host_cores_used": round(usage_seconds / (end - start), 3),
            "throttled_periods": after["cpu"]["nr_throttled"] - before["cpu"]["nr_throttled"],
            "throttled_seconds": round(
                (after["cpu"]["throttled_usec"] - before["cpu"]["throttled_usec"]) / 1e6, 3),
        })
    elif name == "memory":
        axis["allocated_mib_per_vm"] = profile["memory_mib_per_vm"]
    else:
        axis["bytes_per_vm"] = profile["workspace_bytes_per_vm"]
        for field in ("write_ms", "fsync_ms", "read_ms"):
            axis[field] = distribution([r[field] for r in results])
        axis["throughput_mib_per_second"] = round(
            len(results) * profile["workspace_bytes_per_vm"] / 1048576 / (end - start), 3)
        # The kernel counts transports queued because no nfsd thread was available.
        axis["nfs_sockets_enqueued_delta"] = (
            after["nfs"]["sockets_enqueued"] - before["nfs"]["sockets_enqueued"])
    axis["guard_tripped"] = monitor.abort.is_set()
    return axis


def preflight(profile: dict, build_id: str, key: Path) -> tuple[dict, dict]:
    global TOKEN, SSH_KEY
    if os.geteuid() != 0:
        raise ValueError("benchmark must run as root on the compute host")
    if not key.is_absolute() or not key.is_file() or key.is_symlink():
        raise ValueError("SSH key must be an absolute regular file")
    SSH_KEY = str(key)
    TOKEN = TOKEN_PATH.read_text().strip()
    if not re.fullmatch(r"[0-9a-f]{64}", TOKEN):
        raise ValueError("invalid Fire Fuse control token")
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
            RUNTIME + "/readyz", timeout=5) as response:
        if response.status != 200:
            raise ValueError("Fire Fuse is not ready")
    config = json.loads(CONFIG_PATH.read_text())
    catalog_path = Path(config["image_catalog"])
    catalog = json.loads(catalog_path.read_text())
    if build_id not in catalog["images"]:
        raise ValueError("Build is absent from active image catalog")
    profile_config = config["profiles"]["default"]
    manifest_path = Path(profile_config["snapshot_bundle"]) / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    guest_memory = manifest["machine"]["memory_mib"]
    target = profile["target_vms"]
    if profile["memory_mib_per_vm"] > guest_memory - 128:
        raise ValueError("memory workload must leave at least 128 MiB for each guest OS")
    if (config["limits"]["max_vms"] < target
            or config["limits"]["max_total_memory_mib"] < target * guest_memory
            or config["host_pool"]["slots_per_profile"] < target):
        raise ValueError("active configuration cannot admit the requested VM cohort")
    if live_sandboxes():
        raise ValueError("host must be drained, including failed Sandboxes")
    if meminfo()["MemAvailable"] < profile["minimum_available_mib"] * 2:
        raise ValueError("insufficient available memory before benchmark")
    identity = {
        "host": socket.gethostname(),
        "kernel": os.uname().release,
        "host_cpu_count": os.cpu_count(),
        "host_memory_mib": meminfo()["MemTotal"],
        "config_sha256": digest(CONFIG_PATH),
        "catalog_sha256": digest(catalog_path),
        "snapshot_manifest_sha256": digest(manifest_path),
        "orchestrator_sha256": digest(Path("/usr/local/bin/fire-fuse")),
        "build_id": build_id,
        "guest_memory_mib": guest_memory,
        "guest_vcpus": manifest["machine"]["vcpu_count"],
        "configured_max_vms": config["limits"]["max_vms"],
        "configured_warm_slots": config["host_pool"]["slots_per_profile"],
        "configured_start_concurrency": config["limits"]["max_concurrent_starts"],
        "nfs_threads": int(Path("/proc/fs/nfsd/threads").read_text().strip()),
    }
    return config, identity


def create_one(index: int, app: str, workspace: str, sandbox: str, monitor: Monitor) -> dict:
    if monitor.abort.is_set():
        raise RuntimeError("safety guard tripped before VM startup")
    started = time.perf_counter()
    response = request("POST", RUNTIME + "/v1/sandboxes",
                       {"id": sandbox, "app_id": app, "workspace_id": workspace}, timeout=90)
    return {
        "index": index, "id": sandbox, "guest_ip": response["network"]["guest_ip"],
        "startup_ms": round((time.perf_counter() - started) * 1000, 3),
    }


def save_report(path: Path, report: dict):
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(report, output, indent=2, sort_keys=True)
        output.write("\n")


def run(profile: dict, build_id: str, key: Path, output_dir: Path, profile_path: Path) -> int:
    _, identity = preflight(profile, build_id, key)
    run_id = uuid.uuid4().hex
    target = profile["target_vms"]
    owner = "usr_" + run_id
    apps = [("app_" + run_id + "_" + str(i),
             f"{COMPUTE}/v1/internal/users/{owner}/apps/app_{run_id}_{i}")
            for i in range(math.ceil(target / 25))]
    planned = [("wsp_" + run_id + "_" + str(i), "capacity-" + run_id[:8] + "-" + str(i),
                apps[i // 25][0], apps[i // 25][1]) for i in range(target)]
    report = {
        "schema_version": 1, "run_id": run_id,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "profile": profile, "profile_sha256": digest(profile_path),
        "runner_sha256": digest(Path(__file__)),
        "identity": identity, "axes": {}, "errors": [],
    }
    monitor = Monitor(profile)
    thread = threading.Thread(target=monitor.run, daemon=True)
    registered = []
    thread.start()
    try:
        report["initial_warm_slots"] = wait_slots(target, monitor)
        for index, (app, url) in enumerate(apps):
            request("PUT", url, {
                "slug": "capacity-" + run_id[:8] + "-" + str(index),
                "build_id": build_id,
                "session_runtime": {"runtime": {"internal_port": 3000, "profile": "default"}},
            })
            registered.append(url)
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
            futures = [executor.submit(request, "PUT", url + "/workspaces/" + workspace,
                                       {"slug": "capacity-" + run_id[:8] + "-" + str(i)})
                       for i, (workspace, _, _, url) in enumerate(planned)]
            for future in concurrent.futures.as_completed(futures):
                future.result()
        cohort_start = time.perf_counter()
        with concurrent.futures.ThreadPoolExecutor(
                max_workers=profile["startup_concurrency"]) as executor:
            futures = [executor.submit(create_one, i, app, workspace, sandbox, monitor)
                       for i, (workspace, sandbox, app, _) in enumerate(planned)]
            machines = [future.result() for future in concurrent.futures.as_completed(futures)]
        elapsed = time.perf_counter() - cohort_start
        ids = {item["id"] for item in live_sandboxes() if item["state"] == "running"}
        active = sum(sandbox in ids for _, sandbox, _, _ in planned)
        report["axes"]["startup_throughput"] = {
            "running_vms": active, "cohort_seconds": round(elapsed, 3),
            "vms_per_minute": round(active / elapsed * 60, 3),
        }
        report["axes"]["startup_time"] = {
            "request_ms": distribution([m["startup_ms"] for m in machines]),
        }
        if active != target:
            raise RuntimeError(f"only {active}/{target} VMs are running")
        report["workspace_nfs_clients"] = nfs_client_identity(
            {sandbox for _, sandbox, _, _ in planned})
        if report["workspace_nfs_clients"]["missing"]:
            raise RuntimeError("Sandbox NFSv4 client identities are not distinct")
        for name in ("cpu", "memory", "workspace"):
            axis = execute_axis(name, machines, profile, monitor)
            report["axes"][name] = axis
            if axis["errors"]:
                report["errors"].append(f"{name}: {len(axis['errors'])} guest operations failed")
            if axis["guard_tripped"]:
                raise RuntimeError("safety guard tripped during " + name)
    except Exception as error:
        report["errors"].append(f"{type(error).__name__}: {error}")
    finally:
        # Delete only this run's IDs, including failed VMs that still own a slot.
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
            futures = [executor.submit(request, "DELETE", RUNTIME + "/v1/sandboxes/" + sandbox)
                       for _, sandbox, _, _ in planned]
            for future in concurrent.futures.as_completed(futures):
                try:
                    future.result()
                except urllib.error.HTTPError as error:
                    if error.code != 404:
                        report["errors"].append(f"Sandbox cleanup HTTP {error.code}")
                except Exception as error:
                    report["errors"].append(f"Sandbox cleanup: {error}")
        # App archival leaves Workspaces; purge their records and data first.
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
            futures = [executor.submit(request, "DELETE",
                                       url + "/workspaces/" + workspace + "/purge")
                       for workspace, _, _, url in planned]
            for future in concurrent.futures.as_completed(futures):
                try:
                    future.result()
                except Exception as error:
                    report["errors"].append(f"Workspace cleanup: {error}")
        for url in registered:
            try:
                request("DELETE", url)
            except Exception as error:
                report["errors"].append(f"App cleanup: {error}")
        monitor.stop.set()
        thread.join(timeout=3)
        report["samples"] = monitor.samples
        try:
            remaining = {item["id"] for item in live_sandboxes()}
            own_ids = {sandbox for _, sandbox, _, _ in planned}
            report["remaining_run_sandboxes"] = len(remaining & own_ids)
        except Exception as error:
            report["remaining_run_sandboxes"] = None
            report["errors"].append(f"drain verification: {error}")
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        report["passed"] = (not report["errors"] and not monitor.abort.is_set()
                            and report["remaining_run_sandboxes"] == 0
                            and set(report["axes"]) ==
                            {"cpu", "memory", "workspace", "startup_throughput", "startup_time"})
        output_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        output = output_dir / f"capacity-{target}-{run_id}.json"
        summary = output_dir / f"capacity-{target}-{run_id}-summary.json"
        save_report(output, report)
        save_report(summary, {key: value for key, value in report.items() if key != "samples"})
        print(json.dumps({
            "result": str(output), "summary": str(summary), "passed": report["passed"],
            "target_vms": target, "axes": report["axes"],
            "errors": report["errors"], "remaining_run_sandboxes": report["remaining_run_sandboxes"],
        }))
    return 0 if report["passed"] else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--build-id", required=True)
    parser.add_argument("--ssh-key", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args(argv)
    if not args.profile.is_absolute() or not args.output_dir.is_absolute():
        parser.error("--profile and --output-dir must be absolute host paths")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.build_id):
        parser.error("--build-id must be a sha256 digest")
    try:
        return run(load_profile(args.profile), args.build_id, args.ssh_key,
                   args.output_dir, args.profile)
    except (OSError, ValueError, KeyError, urllib.error.URLError) as error:
        print(f"capacity benchmark preflight failed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
