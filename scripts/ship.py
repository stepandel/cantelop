#!/usr/bin/env python3
"""Build and ship one host component from this machine."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
CONTROLLER_SHA256 = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
DEFAULT_CONFIG = Path.home() / ".config/cantelop/ship.json"
RETAINED_SOURCES = 3
# Each component is one output of the canonical release build for its surface.
COMPONENTS = {
    "fire-fuse": {
        "runtime": ("fire_fuse_hosts", "fire-fuse-linux-amd64", ["./fire-fuse/runtime/...", "./fire-fuse/host/..."]),
        "gateway": ("fire_fuse_hosts", "fire-fuse-gateway-linux-amd64", ["./fire-fuse/gateway/..."]),
        "dispatcher": ("fire_fuse_hosts", "fire-fuse-async-dispatcher-linux-amd64", ["./fire-fuse/dispatcher/...", "./fire-fuse/messageprotocol/..."]),
        "managed-watchdog": ("fire_fuse_hosts", "fire-fuse-managed-watchdog-linux-amd64", ["./fire-fuse/runtime/managedwatchdog/..."]),
        "observer": ("fire_fuse_hosts", "fire-fuse-observer-linux-amd64", ["./fire-fuse/host/observer/..."]),
    },
    "builder": {
        "worker": ("control_builder_hosts", "cantelop-control-worker-linux-amd64", []),
        "materializer": ("control_builder_hosts", "cantelop-build-materializer-linux-amd64", ["./builder/..."]),
    },
    "storage": {
        "minio": ("storage_hosts", "minio-linux-amd64", []),
        "juicefs": ("storage_hosts", "juicefs-linux-amd64", []),
    },
}
BUILDS = {
    "fire-fuse": "scripts/build-compute-linux-amd64.sh",
    "builder": "scripts/build-builder-linux-amd64.sh",
    "minio": "storage/minio/build-minio-linux-amd64.sh",
    "juicefs": "storage/juicefs/prepare-juicefs-linux-amd64.sh",
}


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream, indent=2)
        stream.write("\n")
    temporary.replace(path)


@contextmanager
def lock(path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("a") as stream:
        os.chmod(path, 0o600)
        try:
            fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("another local shipping command holds this lock") from None
        yield


class Runner:
    def __init__(self, evidence):
        self.evidence = evidence
        evidence.mkdir(parents=True, exist_ok=True, mode=0o700)

    def run(self, step, command, cwd=None, env=None):
        print(f"→ {step}", flush=True)
        start = time.monotonic()
        log = self.evidence / f"{step}.log"
        with log.open("w") as stream:
            os.chmod(log, 0o600)
            result = subprocess.run(command, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT)
        elapsed = round(time.monotonic() - start, 2)
        with (self.evidence / "timings.jsonl").open("a") as stream:
            os.chmod(stream.name, 0o600)
            stream.write(json.dumps({"step": step, "elapsed_seconds": elapsed, "exit_code": result.returncode}) + "\n")
        print(f"  {elapsed}s · {'passed' if result.returncode == 0 else 'failed'} · {log}", flush=True)
        if result.returncode:
            raise RuntimeError(f"{step} failed; inspect {log}")


def source_tree(commit, cache):
    destination = cache / "sources" / commit
    if destination.exists():
        os.utime(destination)
        return destination
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_suffix(".tmp")
    shutil.rmtree(temporary, ignore_errors=True)
    temporary.mkdir()
    archive = temporary / "source.tar"
    subprocess.run(["git", "archive", "--format=tar", "--output", str(archive), commit], cwd=ROOT, check=True)
    with tarfile.open(archive) as stream:
        stream.extractall(temporary, filter="data")
    archive.unlink()
    temporary.replace(destination)
    return destination


def prune_sources(cache, commit):
    # Snapshots are rebuilt from Git on demand; checked artifacts stay because
    # inventories can pin them.
    sources = sorted((path for path in (cache / "sources").iterdir() if path.name != commit), key=lambda path: path.stat().st_mtime)
    for path in sources[:-RETAINED_SOURCES]:
        shutil.rmtree(path, ignore_errors=True)


def valid_artifacts(directory, commit, component):
    try:
        manifest = json.loads((directory / "manifest.json").read_text())
        return (manifest["source_commit"] == commit and manifest["component"] == component
                and manifest["builder_sha256"] == CONTROLLER_SHA256
                and bool(manifest["artifacts"])
                and all(not Path(name).is_absolute() and ".." not in Path(name).parts
                        and digest(directory / name) == checksum for name, checksum in manifest["artifacts"].items()))
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        return False


def prepare(surface, component, commit, cache, runner):
    directory = cache / "artifacts" / f"{surface}-{component}" / commit
    if valid_artifacts(directory, commit, component):
        print(f"Reusing checked artifacts: {directory}", flush=True)
        return directory
    source = source_tree(commit, cache)
    shutil.rmtree(directory, ignore_errors=True)
    directory.mkdir(parents=True)
    _, output, packages = COMPONENTS[surface][component]
    environment = {**os.environ, "CANTELOP_BUILD_COMMIT": commit, "CANTELOP_DEPLOYMENT_REVISION": commit[:12],
                   "CANTELOP_STORAGE_BUILD_CACHE": str(cache / "storage-build")}
    schema = None
    if packages:
        runner.run("component-tests", ["go", "test", *packages], cwd=source)
    with tempfile.TemporaryDirectory(dir=cache) as build:
        build = Path(build)
        runner.run("component-build", ["bash", str(source / (BUILDS.get(surface) or BUILDS[component])), str(build)], cwd=source, env=environment)
        shutil.copy(build / output, directory / "binary")
        if component == "worker":
            shutil.copy(build / "cantelop-control-migrate-linux-amd64", directory / "migrate")
            shutil.copytree(build / "cantelop-control-migrations", directory / "migrations")
    if component == "runtime":
        schema = int(re.search(r"const CurrentVersion = (\d+)", (source / "fire-fuse/runtime/persistence/sqliteschema/manager.go").read_text())[1])
    elif component == "worker":
        runner.run("worker-tests", ["bun", "test", "core", "workers"], cwd=source / "console")
        runner.run("worker-linux-check", ["bun", str(source / "scripts/check-control-worker-build.ts")], cwd=source)
        # The shipped binary must finish module initialization on Linux and
        # stop only at its first environment requirement.
        runner.run("worker-linux-start", ["docker", "run", "--rm", "--platform", "linux/amd64", "-v", f"{directory / 'binary'}:/worker:ro",
                   "debian:bookworm-slim", "sh", "-c", "/worker 2>&1 | grep -F 'CANTELOP_OPERATION_WORKER_ID is required'"])
    artifacts = {str(path.relative_to(directory)): digest(path) for path in sorted(directory.rglob("*"))
                 if path.is_file() and not any(part.startswith(".") for part in path.relative_to(directory).parts)}
    write_json(directory / "manifest.json", {"schema_version": 1, "source_commit": commit, "surface": surface, "qualification_run": str(runner.evidence),
               "component": component, "state_schema": schema, "builder_sha256": CONTROLLER_SHA256, "artifacts": artifacts})
    return directory


def deploy(surface, component, artifacts, settings, cache, runner, source, preflight_only=False):
    inventory = Path(settings["inventory"]).expanduser().resolve()
    vault = Path(settings["vault_password_file"]).expanduser().resolve()
    if not inventory.is_file() or not vault.is_file():
        raise RuntimeError("inventory or vault password file is missing; run ship.py configure")
    manifest = json.loads((artifacts / "manifest.json").read_text())
    run_id = runner.evidence.name
    status_directory = runner.evidence / "host-status"
    status_directory.mkdir(mode=0o700, exist_ok=True)
    environment = {**os.environ, "ANSIBLE_LOCAL_TEMP": str(cache / "ansible-tmp"), "ANSIBLE_PIPELINING": "True"}
    base = ["ansible-playbook", "--inventory", str(inventory), "--vault-password-file", str(vault)]
    variables = {"ship_component": component, "ship_revision": manifest["source_commit"], "ship_schema": manifest["state_schema"],
                 "ship_binary": str(artifacts / "binary"), "ship_checksum": digest(artifacts / "binary"), "ship_run_id": run_id,
                 "ship_drain_seconds": settings.get("drain_seconds", 120), "ship_hosts": COMPONENTS[surface][component][0],
                 "ship_status_directory": str(status_directory)}
    if component == "worker":
        variables.update(cantelop_control_worker_binary_artifact=str(artifacts / "binary"), cantelop_control_worker_binary_checksum=digest(artifacts / "binary"),
                         cantelop_control_migrate_artifact=str(artifacts / "migrate"), cantelop_control_migrate_checksum=digest(artifacts / "migrate"),
                         cantelop_control_migrations_artifact=str(artifacts / "migrations"), cantelop_control_migrations_manifest_checksum=digest(artifacts / "migrations/SHA256SUMS"))
    variables_file = runner.evidence / "ansible-vars.json"
    write_json(variables_file, variables)
    # Playbooks, roles, units, and the host helper come from the shipped commit.
    cwd = source / "platform/ansible"

    def ansible(step, playbook, extra=None, check=False):
        command = base + ["--extra-vars", "@" + str(variables_file)]
        if check:
            command += ["--check"]
        if extra:
            command += ["--extra-vars", json.dumps(extra)]
        runner.run(step, command + [playbook], cwd=cwd, env=environment)

    boundary = component in ("runtime", "minio", "juicefs")
    boundary_attempted = False
    try:
        # Every host preflight finishes before admission is fenced.
        if component != "worker":
            ansible("host-preflight", "playbooks/ship/binary.yml", {"ship_action": "preflight"})
            if component == "juicefs":
                ansible("compute-client-preflight", "playbooks/ship/binary.yml", {"ship_action": "preflight", "ship_hosts": "fire_fuse_hosts", "ship_component": "compute-juicefs"})
        elif preflight_only:
            ansible("worker-preflight", "playbooks/builder/control-worker.yml", check=True)
        if component != "worker":
            statuses = [json.loads(path.read_text()) for path in status_directory.glob("*.json")]
            if not statuses or any(type(status.get("needs_update")) is not bool for status in statuses):
                raise RuntimeError("host preflight did not return valid component status; nothing will be activated")
            if not any(status["needs_update"] for status in statuses):
                print("The selected binaries are already installed; no activation is needed.", flush=True)
                return
        if preflight_only:
            return
        if boundary:
            boundary_attempted = True
            ansible("enter-maintenance", "playbooks/ship/boundary.yml", {"ship_action": "fence"})
        if component == "worker":
            ansible("deploy-worker", "playbooks/builder/control-worker.yml")
            ansible("record-worker-revision", "playbooks/ship/worker-receipt.yml")
        else:
            ansible("deploy-component", "playbooks/ship/binary.yml", {"ship_action": "apply"})
            if component == "juicefs":
                ansible("deploy-compute-client", "playbooks/ship/binary.yml", {"ship_action": "apply", "ship_hosts": "fire_fuse_hosts", "ship_component": "compute-juicefs"})
        print(f"Pin the artifacts in {artifacts} in the inventory before the next provisioning run.", flush=True)
    finally:
        if boundary_attempted:
            # Resume only the services recorded by this run, and reopen the
            # Tunnel only after runtime readiness. No database is restored.
            ansible("restore-ingress", "playbooks/ship/boundary.yml", {"ship_action": "resume"})


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("surface", choices=[*COMPONENTS, "configure"])
    parser.add_argument("--component")
    parser.add_argument("--ref", default="HEAD", help="committed source to ship; uncommitted edits are excluded")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--plan", action="store_true", help="print the selected scope without building or contacting hosts")
    modes.add_argument("--prepare", action="store_true", help="test and build locally without contacting hosts")
    modes.add_argument("--preflight", action="store_true", help="prepare and preflight hosts without restarting services")
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    parser.add_argument("--inventory")
    parser.add_argument("--vault-password-file")
    parser.add_argument("--cache-dir")
    args = parser.parse_args(argv)
    args.config = args.config.expanduser().resolve()
    stored = json.loads(args.config.read_text()) if args.config.exists() else {}
    if set(stored) - {"inventory", "vault_password_file", "cache_dir", "drain_seconds"}:
        raise RuntimeError("machine configuration contains unsupported fields; use only documented paths and drain_seconds")
    if type(stored.get("drain_seconds", 120)) is not int or not 1 <= stored.get("drain_seconds", 120) <= 3600:
        raise RuntimeError("drain_seconds must be an integer between 1 and 3600")
    defaults = {"inventory": str(ROOT / "platform/ansible/inventory/staging/hosts.yml"),
                "vault_password_file": str(Path.home() / ".config/cantelop/staging/ansible-vault-password"),
                "cache_dir": str(Path.home() / ".cache/cantelop/ship")}
    explicit = {key: str(Path(getattr(args, key)).expanduser().resolve()) for key in defaults if getattr(args, key, None)}
    settings = {**defaults, **stored}
    for key in defaults:
        value = explicit.get(key) or os.environ.get("CANTELOP_SHIP_" + key.upper()) or settings[key]
        settings[key] = str(Path(value).expanduser().resolve())
    if args.surface == "configure":
        for key in ("inventory", "vault_password_file"):
            if not Path(settings[key]).is_file():
                raise RuntimeError(f"{key} does not exist: {settings[key]}; pass --{key.replace('_', '-')}")
        # Save only explicit choices; defaults and environment overrides stay dynamic.
        write_json(args.config, {**stored, **explicit})
        print(f"Machine configuration saved: {args.config}")
        return 0
    component = args.component or {"fire-fuse": "runtime", "builder": "worker"}.get(args.surface)
    if component not in COMPONENTS[args.surface]:
        parser.error(f"select --component from: {', '.join(COMPONENTS[args.surface])}")
    commit = subprocess.check_output(["git", "rev-parse", "--verify", "--end-of-options", args.ref + "^{commit}"], cwd=ROOT, text=True).strip()
    print(f"Shipping {args.surface}/{component} from {commit[:12]} (committed source)")
    if args.plan:
        print(json.dumps({"surface": args.surface, "component": component, "source_commit": commit,
                          "hosts": COMPONENTS[args.surface][component][0], "compute_client_dependency": component == "juicefs",
                          "maintenance_boundary": component in ("runtime", "minio", "juicefs"), "settings": settings}, indent=2))
        return 0
    cache = Path(settings["cache_dir"])
    run_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + "-" + commit[:12]
    with lock(cache / "locks/controller.lock"):
        runner = Runner(cache / "runs" / run_id)
        source = source_tree(commit, cache)
        prune_sources(cache, commit)
        if not args.prepare:
            for key in ("inventory", "vault_password_file"):
                if not Path(settings[key]).is_file():
                    raise RuntimeError(f"{key} is missing; run ship.py configure")
            groups = COMPONENTS[args.surface][component][0]
            if component in ("minio", "juicefs"):
                groups += ":fire_fuse_hosts"
            runner.run("connection-preflight", ["ansible", groups, "--inventory", settings["inventory"],
                       "--vault-password-file", settings["vault_password_file"], "--module-name", "ansible.builtin.ping"],
                       cwd=source / "platform/ansible", env={**os.environ, "ANSIBLE_LOCAL_TEMP": str(cache / "ansible-tmp"), "ANSIBLE_PIPELINING": "True"})
        artifacts = prepare(args.surface, component, commit, cache, runner)
        if not args.prepare:
            deploy(args.surface, component, artifacts, settings, cache, runner, source, preflight_only=args.preflight)
        result = "Prepared" if args.prepare else "Preflighted" if args.preflight else "Shipped"
        print(f"{result} {args.surface}/{component}. Evidence: {runner.evidence}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, OSError, subprocess.CalledProcessError, ValueError) as error:
        print(f"ship: {error}", file=sys.stderr)
        sys.exit(1)
