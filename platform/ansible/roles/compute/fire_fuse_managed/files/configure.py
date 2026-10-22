#!/usr/bin/env python3
"""Derive the bounded managed instance without altering the ordinary runtime."""
import ipaddress
import json
import pathlib
import subprocess


def managed_config(standard):
    cfg = json.loads(json.dumps(standard))
    if cfg["runtime_dir"] != "/var/lib/fire-fuse/runtime" or cfg["state_db"] != "/var/lib/fire-fuse/state/state.db":
        raise ValueError("unexpected ordinary runtime paths")
    if cfg["workspace"]["root"] != "/var/lib/fire-fuse/workspaces" or cfg["workspace"]["admin_root"] != "/var/lib/fire-fuse/workspace-admin":
        raise ValueError("unexpected shared workspace mounts")
    if ipaddress.ip_network(cfg["network"]["address_pool"]).overlaps(ipaddress.ip_network("172.30.0.0/16")):
        raise ValueError("managed guest subnet overlaps ordinary pool")
    cfg["runtime_dir"] = "/var/lib/fire-fuse-managed/runtime"
    cfg["state_db"] = "/var/lib/fire-fuse-managed/state/state.db"
    cfg["network"].update(namespace="managed", address_pool="172.30.0.0/16")
    cfg["host_pool"] = {"slots_per_profile": 2}
    cfg["limits"] = {"max_concurrent_starts": 1, "max_total_memory_mib": 8192, "max_vms": 2}
    cfg["config_encryption_key_file"] = "/etc/fire-fuse-managed/encryption.token"
    cfg["image_catalog"] = "/etc/fire-fuse-managed/image-catalog.json"
    return cfg


def main():
    # Shared immutable images are safe only with collection disabled in BOTH
    # instances: their independent SQLite reference sets cannot drive shared GC.
    command = subprocess.check_output(["systemctl", "show", "fire-fuse.service", "-p", "ExecStart", "--value"], text=True)
    if "image-gc-grace" in command or "image-collection" in command:
        raise ValueError("disable shared image collection before co-location")
    cfg = managed_config(json.loads(pathlib.Path("/etc/fire-fuse/config.json").read_text()))
    changed = False
    for path, data in [(pathlib.Path("/etc/fire-fuse-managed/config.json"), json.dumps(cfg, indent=2) + "\n")]:
        if not path.exists() or path.read_text() != data:
            path.write_text(data)
            path.chmod(0o600)
            changed = True
    catalog = pathlib.Path(cfg["image_catalog"])
    if not catalog.exists():
        catalog.write_text(pathlib.Path("/etc/fire-fuse/image-catalog.json").read_text())
        catalog.chmod(0o600)
        changed = True
    print("changed" if changed else "unchanged")


if __name__ == "__main__":
    main()
