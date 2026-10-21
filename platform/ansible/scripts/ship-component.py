#!/usr/bin/env python3
"""Narrow binary updates on an already provisioned host, invoked by Ansible."""

import argparse
from contextlib import closing
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import stat
import subprocess
import sys
import time
import urllib.request

STATE = Path("/var/lib/fire-fuse/state/state.db")
RELEASES = Path("/var/lib/cantelop/local-ship")
TUNNEL = "cloudflared-cantelop-edge.service"
RUNTIME = "fire-fuse.service"
DEPENDENTS = ["fire-fuse-gateway.service", "fire-fuse-async-dispatcher.service", "fire-fuse-observer.service"]
TARGETS = {
    "runtime": ("/usr/local/bin/fire-fuse", [RUNTIME], "http://127.0.0.1:8080/readyz"),
    "gateway": ("/usr/local/bin/fire-fuse-gateway", [DEPENDENTS[0]], "http://127.0.0.1:8081/readyz"),
    "dispatcher": ("/usr/local/bin/fire-fuse-async-dispatcher", [DEPENDENTS[1]], None),
    "observer": ("/usr/local/bin/fire-fuse-observer", [DEPENDENTS[2]], "http://127.0.0.1:8082/snapshot"),
    "materializer": ("/usr/local/bin/cantelop-build-materializer", ["cantelop-build-materializer.service"], None),
    "minio": ("/usr/local/bin/minio", ["fire-fuse-minio.service"], "http://10.78.0.2:9000/minio/health/ready"),
    "juicefs": ("/usr/local/bin/juicefs", [], None),
    "compute-juicefs": ("/usr/local/bin/juicefs", ["fire-fuse-juicefs-admin.service", "fire-fuse-juicefs-gateway.service", "nfs-kernel-server.service"], None),
}
OBSERVER_TOKEN = Path("/etc/fire-fuse-observer/observer.token")
MOUNTS = ("/var/lib/fire-fuse/workspaces", "/var/lib/fire-fuse/workspace-admin")
MATERIALIZER_SOCKET = Path("/run/cantelop-builder/materializer.sock")
# The control worker reaches provider ingress over WireGuard, not the Tunnel.
INGRESS_FENCE = ["INPUT", "--protocol", "tcp", "--syn", "--destination-port", "8084", "--match", "comment",
                 "--comment", "cantelop-local-ship-fence", "--jump", "REJECT", "--reject-with", "tcp-reset"]
RUN_ID = re.compile(r"[0-9TZ-]{16,40}[0-9a-f]{12}")
RETAINED_RUNS = 3


def command(*args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if result.returncode:
        # Command output can contain environment or provider credentials.
        raise RuntimeError(f"{Path(args[0]).name} failed (exit {result.returncode})")
    return result.stdout.strip()


def succeeds(*args):
    return subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def active(service):
    return succeeds("systemctl", "is-active", "--quiet", service)


def restarts(service):
    return command("systemctl", "show", "--property=NRestarts", "--value", service)


def mounted():
    for mount in MOUNTS:
        command("mountpoint", "-q", mount)


def materializer_responds():
    command("curl", "--silent", "--show-error", "--unix-socket", str(MATERIALIZER_SOCKET), "--max-time", "5", "http://localhost/")


def trusted(path, executable=False):
    info = Path(path).lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise RuntimeError(f"untrusted host file: {path}")
    if executable and not info.st_mode & 0o111:
        raise RuntimeError(f"host binary is not executable: {path}")


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream)
    temporary.replace(path)


def database_status(schema=None, drain=False, integrity=True):
    trusted(STATE)
    with closing(sqlite3.connect(f"file:{STATE}?mode=ro", uri=True)) as connection:
        if integrity and not drain and connection.execute("PRAGMA quick_check").fetchone()[0] != "ok":
            raise RuntimeError("Fire Fuse state integrity check failed")
        actual = connection.execute("PRAGMA user_version").fetchone()[0]
        if schema is not None and actual != schema:
            raise RuntimeError(f"component shipping requires unchanged state schema (host {actual}, candidate {schema}); use the coordinated rollout")
        if drain:
            # Failed sandboxes have no live microVM; running ones die with the runtime.
            return sum(connection.execute(sql).fetchone()[0] for sql in [
                "SELECT count(*) FROM sandboxes WHERE state NOT IN ('terminated','failed')",
                "SELECT count(*) FROM message_executions WHERE terminal=0",
                "SELECT count(*) FROM message_deliveries WHERE work_state NOT IN ('quiescent','unavailable')",
                "SELECT count(*) FROM session_work_leases",
                "SELECT count(*) FROM app_build_operations WHERE status IN ('receiving','queued','processing')",
            ])
    return 0


def wait_ready(services, url=None, token=None, check=None):
    if not (services or url or check):
        return
    # Type=simple units are active as soon as they exec. Without a probe,
    # require consecutive passes; any automatic restart fails the candidate.
    stable = 1 if url or check else 3
    baseline = {service: restarts(service) for service in services}
    passes = 0
    for _ in range(30):
        if any(restarts(service) != count for service, count in baseline.items()):
            raise RuntimeError("component restarted during readiness")
        try:
            if not all(active(service) for service in services):
                raise RuntimeError("service is not active")
            if url:
                request = urllib.request.Request(url)
                if token:
                    request.add_header("Authorization", "Bearer " + token.read_text().strip())
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open(request, timeout=2) as response:
                    if response.status != 200:
                        raise RuntimeError("readiness failed")
            if check:
                check()
            passes += 1
        except (OSError, RuntimeError):
            passes = 0
        if passes == stable:
            return
        time.sleep(1)
    raise RuntimeError("component readiness did not pass within 30 attempts")


def provider_ingress(closed):
    present = succeeds("/usr/sbin/iptables", "--wait", "5", "--check", *INGRESS_FENCE)
    if closed and not present:
        command("/usr/sbin/iptables", "--wait", "5", "--insert", *INGRESS_FENCE)
    while not closed and present:
        command("/usr/sbin/iptables", "--wait", "5", "--delete", *INGRESS_FENCE)
        present = succeeds("/usr/sbin/iptables", "--wait", "5", "--check", *INGRESS_FENCE)


def quiesce_materializer(seconds):
    # The worker connects through the socket's group permission. A restarted
    # materializer recreates the socket with its normal mode.
    MATERIALIZER_SOCKET.chmod(0)
    deadline = time.monotonic() + seconds
    while any(str(MATERIALIZER_SOCKET) in line for line in command("ss", "--unix", "--no-header", "state", "connected").splitlines()):
        if time.monotonic() >= deadline:
            MATERIALIZER_SOCKET.chmod(0o660)
            raise RuntimeError("materializer conversions did not drain; no binaries were replaced")
        time.sleep(1)


def installed():
    """Locally shipped binaries that are still installed, by destination."""
    shipped = {}
    for record in sorted(RELEASES.glob("*/*.json")):
        value = json.loads(record.read_text())
        destination = value.get("destination", "")
        if Path(destination).is_file() and digest(destination) == value.get("sha256"):
            shipped[destination] = {"source_commit": value.get("source_commit"), "sha256": value["sha256"]}
    return shipped


def prune(run_id, checksum):
    runs = sorted(path for path in RELEASES.iterdir() if path.is_dir() and RUN_ID.fullmatch(path.name) and path.name != run_id)
    for record in runs[:-RETAINED_RUNS]:
        # Keep the small deployment records; provisioning reads them.
        for path in [*record.glob("*.previous"), record / "state.db"]:
            path.unlink(missing_ok=True)
    artifacts = RELEASES / "artifacts"
    if artifacts.is_dir():
        staged = sorted((path for path in artifacts.iterdir() if path.name != checksum), key=lambda path: path.stat().st_mtime)
        for directory in staged[:-RETAINED_RUNS]:
            shutil.rmtree(directory)


def preflight(component, binary, checksum, schema=None, activation=False):
    destination, services, _ = TARGETS[component]
    trusted(destination, executable=True)
    trusted(binary)
    if digest(binary) != checksum:
        raise RuntimeError("staged binary SHA-256 mismatch")
    with binary.open("rb") as stream:
        header = stream.read(20)
    if len(header) != 20 or header[:6] != b"\x7fELF\x02\x01" or header[18:20] != b"\x3e\x00":
        raise RuntimeError("candidate must be a Linux amd64 ELF binary")
    binary.chmod(0o700)
    for service in services:
        unit = command("systemctl", "show", "--property=FragmentPath", "--value", service)
        trusted(unit)
    if component == "runtime":
        database_status(schema)
        # Activation runs -check-state-only, which includes this configuration check.
        if not activation:
            command(str(binary), "-config", "/etc/fire-fuse/config.json", "-preflight-only")
    elif component in ("juicefs", "compute-juicefs"):
        command(str(binary), "--version")
        help_text = command(str(binary), "mount", "--help")
        if not all(flag in help_text for flag in ("--workspace-data-only", "--root-only")):
            raise RuntimeError("JuiceFS candidate is missing the workspace policy")
    elif component == "minio":
        command(str(binary), "--version")


def fence(receipt, seconds):
    if receipt.exists():
        raise RuntimeError("maintenance receipt already exists")
    services = [RUNTIME, *DEPENDENTS, TUNNEL]
    previous = {service: active(service) for service in services}
    if not previous[RUNTIME]:
        raise RuntimeError("Fire Fuse must be serving before entering a local shipping boundary")
    # Persist restoration intent before the first mutation, including timeout.
    write_json(receipt, {"services": previous})
    command("systemctl", "stop", TUNNEL)
    provider_ingress(closed=True)
    deadline = time.monotonic() + seconds
    while remaining := database_status(drain=True):
        if time.monotonic() >= deadline:
            raise RuntimeError(f"accepted work did not drain ({remaining} durable items remain); no binaries were replaced")
        time.sleep(1)
    command("systemctl", "stop", RUNTIME)
    if database_status(drain=True):
        raise RuntimeError("durable work appeared during drain; no binaries were replaced")


def resume(receipt, component="runtime"):
    if not receipt.exists():
        return False
    trusted(receipt)
    previous = json.loads(receipt.read_text())["services"]
    if set(previous) != {RUNTIME, *DEPENDENTS, TUNNEL} or any(type(value) is not bool for value in previous.values()):
        raise RuntimeError("invalid maintenance receipt")
    # Activation checked integrity once before replacing any binary.
    database_status(integrity=False)
    if component == "minio":
        wait_ready([], TARGETS["minio"][2])
    elif component == "juicefs":
        wait_ready(TARGETS["compute-juicefs"][1], check=mounted)
    if previous[RUNTIME]:
        command("systemctl", "start", RUNTIME)
        wait_ready([RUNTIME], TARGETS["runtime"][2])
    for service in DEPENDENTS:
        if previous[service]:
            command("systemctl", "start", service)
            wait_ready([service], TARGETS["gateway"][2] if service == DEPENDENTS[0] else None)
    provider_ingress(closed=False)
    if previous[TUNNEL]:
        command("systemctl", "start", TUNNEL)
        wait_ready([TUNNEL], "http://127.0.0.1:20241/ready")
    receipt.rename(receipt.with_suffix(".completed.json"))
    return True


def apply(component, binary, checksum, revision, run_id, schema=None, drain_seconds=120):
    preflight(component, binary, checksum, schema, activation=True)
    destination, services, url = TARGETS[component]
    token = OBSERVER_TOKEN if component == "observer" else None
    check = {"materializer": materializer_responds, "compute-juicefs": mounted}.get(component)
    destination = Path(destination)
    if digest(destination) == checksum:
        return False
    if component in ("runtime", "compute-juicefs") and active(RUNTIME):
        raise RuntimeError("this update requires the recorded compute maintenance boundary")
    record = RELEASES / run_id
    record.mkdir(mode=0o700, parents=True, exist_ok=True)
    previous = {service: active(service) for service in services}
    backup = record / f"{component}.previous"
    if backup.exists():
        raise RuntimeError("previous binary snapshot already exists for this run")
    shutil.copyfile(destination, backup)
    backup.chmod(0o700)
    if component == "runtime":
        with closing(sqlite3.connect(STATE)) as connection, closing(sqlite3.connect(record / "state.db")) as snapshot:
            connection.backup(snapshot)
        (record / "state.db").chmod(0o600)
        command(str(binary), "-config", "/etc/fire-fuse/config.json", "-check-state-only")
    if component == "materializer" and previous[services[0]]:
        quiesce_materializer(drain_seconds)
    staged = destination.with_name(destination.name + ".ship-next")
    try:
        for service in reversed(services):
            if previous[service]:
                command("systemctl", "stop", service)
        shutil.copyfile(binary, staged)
        staged.chmod(0o755)
        staged.replace(destination)
        for service in services:
            if previous[service]:
                command("systemctl", "start", service)
        if component == "runtime":
            command("systemctl", "start", RUNTIME)
            wait_ready([RUNTIME], url)
            database_status(schema, integrity=False)
        elif any(previous.values()):
            wait_ready([service for service in services if previous[service]], url, token, check)
        write_json(record / f"{component}.json", {"component": component, "source_commit": revision, "sha256": checksum,
                                                  "previous_sha256": digest(backup), "destination": str(destination)})
    except BaseException:
        for service in reversed(services):
            if active(service):
                command("systemctl", "stop", service)
        shutil.copyfile(backup, staged)
        staged.chmod(0o755)
        staged.replace(destination)
        restarted = [service for service in services if previous[service] or component == "runtime"]
        for service in restarted:
            command("systemctl", "start", service)
        if restarted:
            wait_ready(restarted, url, token, check)
        raise
    finally:
        staged.unlink(missing_ok=True)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["preflight", "apply", "fence", "resume", "installed"])
    parser.add_argument("--component", choices=TARGETS, default="runtime")
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--checksum")
    parser.add_argument("--revision")
    parser.add_argument("--run-id")
    parser.add_argument("--schema", type=int)
    parser.add_argument("--drain-seconds", type=int, default=120)
    args = parser.parse_args()
    if os.geteuid() == 0 and args.action == "installed":
        print(json.dumps(installed()))
        return
    if os.geteuid() != 0 or not re.fullmatch(r"[0-9a-f]{40}", args.revision or "") or not RUN_ID.fullmatch(args.run_id or ""):
        parser.error("shipping requires root and validated release identities")
    if args.action in ("fence", "apply") and not 1 <= args.drain_seconds <= 3600:
        parser.error("drain timeout must be between 1 and 3600 seconds")
    RELEASES.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (RELEASES / "host.lock").open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        receipt = RELEASES / (args.run_id + ".boundary.json")
        outstanding = list(RELEASES.glob("*.boundary.json"))
        if any(path != receipt for path in outstanding):
            raise RuntimeError("another shipping run has an unfinished maintenance boundary; restore that run first")
        if args.action == "fence":
            fence(receipt, args.drain_seconds)
            changed = True
        elif args.action == "resume":
            changed = resume(receipt, args.component)
        else:
            if args.binary is None or not re.fullmatch(r"[0-9a-f]{64}", args.checksum or ""):
                parser.error("a staged binary and SHA-256 are required")
            if args.binary != RELEASES / "artifacts" / args.checksum / "binary":
                parser.error("candidate must be in the checksum-addressed shipping directory")
            if args.component == "runtime" and (args.schema is None or args.schema < 1):
                parser.error("runtime shipping requires an explicit unchanged state schema")
            prune(args.run_id, args.checksum)
            if args.action == "preflight":
                preflight(args.component, args.binary, args.checksum, args.schema)
                changed = False
            else:
                changed = apply(args.component, args.binary, args.checksum, args.revision, args.run_id, args.schema, args.drain_seconds)
        result = {"changed": changed, "component": args.component, "action": args.action}
        if args.action == "preflight":
            result["needs_update"] = digest(TARGETS[args.component][0]) != args.checksum
        print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, sqlite3.Error) as error:
        print(f"local shipping: {error}", file=sys.stderr)
        sys.exit(1)
