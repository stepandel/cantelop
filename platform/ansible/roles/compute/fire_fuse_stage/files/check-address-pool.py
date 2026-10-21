#!/usr/bin/env python3
"""Reject a proposed fire-fuse guest CIDR that overlaps host IPv4 routes."""

from __future__ import annotations

import hashlib
import ipaddress
import json
import re
import subprocess
import sys
from typing import Any, NoReturn

IP_BINARY = "/usr/sbin/ip"
TAP_NAME = re.compile(r"^ff[0-9a-f]{13}$")
VM_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]{0,63}$")
PRIVATE_POOLS = tuple(
    ipaddress.ip_network(value)
    for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
)


def fail(message: str) -> NoReturn:
    print(f"check-address-pool: {message}", file=sys.stderr)
    raise SystemExit(1)


def parse_candidate(value: str) -> ipaddress.IPv4Network:
    try:
        candidate = ipaddress.ip_network(value, strict=True)
    except ValueError as error:
        raise ValueError(f"invalid canonical address pool {value!r}: {error}") from error
    if not isinstance(candidate, ipaddress.IPv4Network):
        raise ValueError("address pool must be IPv4")
    if candidate.prefixlen > 30:
        raise ValueError("address pool must contain at least one /30 subnet")
    if not any(candidate.subnet_of(private_pool) for private_pool in PRIVATE_POOLS):
        raise ValueError("address pool must be entirely private")
    return candidate


def owned_tap_names(links: list[dict[str, Any]]) -> set[str]:
    owned: set[str] = set()
    for link in links:
        name = link.get("ifname")
        alias = link.get("ifalias")
        if not isinstance(name, str) or not TAP_NAME.fullmatch(name):
            continue
        if not isinstance(alias, str) or not alias.startswith("fire-fuse:"):
            continue
        vm_id = alias.removeprefix("fire-fuse:")
        if not VM_ID.fullmatch(vm_id):
            continue
        expected = "ff" + hashlib.sha256(vm_id.encode()).hexdigest()[:13]
        if name == expected:
            owned.add(name)
    return owned


def find_conflicts(
    candidate: ipaddress.IPv4Network,
    routes: list[dict[str, Any]],
    owned_taps: set[str],
) -> list[str]:
    conflicts: list[str] = []
    for route in routes:
        destination = route.get("dst")
        if destination in (None, "default"):
            continue
        if not isinstance(destination, str):
            raise ValueError("route destination is not a string")
        try:
            network = ipaddress.ip_network(destination, strict=False)
        except ValueError as error:
            raise ValueError(f"invalid route destination {destination!r}") from error
        if not isinstance(network, ipaddress.IPv4Network) or not candidate.overlaps(network):
            continue

        device = route.get("dev", "")
        if (
            isinstance(device, str)
            and device in owned_taps
            and network.subnet_of(candidate)
        ):
            continue

        table = route.get("table", "main")
        route_type = route.get("type", "unicast")
        conflicts.append(
            f"{network} table={table} type={route_type} dev={device or '-'}"
        )
    return sorted(set(conflicts))


def read_ip_json(arguments: list[str]) -> list[dict[str, Any]]:
    result = subprocess.run(
        [IP_BINARY, *arguments],
        check=True,
        capture_output=True,
        text=True,
        timeout=10,
    )
    decoded = json.loads(result.stdout)
    if not isinstance(decoded, list) or not all(isinstance(item, dict) for item in decoded):
        raise ValueError(f"ip {' '.join(arguments)} did not return a JSON array")
    return decoded


def main() -> None:
    if len(sys.argv) != 2:
        fail("usage: check-address-pool.py CIDR")
    try:
        candidate = parse_candidate(sys.argv[1])
        links = read_ip_json(["-j", "link", "show"])
        routes = read_ip_json(["-j", "-4", "route", "show", "table", "all"])
        conflicts = find_conflicts(candidate, routes, owned_tap_names(links))
    except (ValueError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        fail(str(error))

    if conflicts:
        print(
            f"check-address-pool: {candidate} overlaps existing host routes:",
            file=sys.stderr,
        )
        for conflict in conflicts:
            print(f"  {conflict}", file=sys.stderr)
        raise SystemExit(1)
    print(f"fire-fuse address pool {candidate} has no host route conflicts")


if __name__ == "__main__":
    main()
