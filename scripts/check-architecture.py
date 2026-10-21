#!/usr/bin/env python3
"""Reject source dependencies that cross Cantelop architecture boundaries."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from pathlib import Path
import re


GO_MODULE = "github.com/stepandel/cantelop/fire-fuse/"
BUILDER_MODULE = "github.com/stepandel/cantelop/builder"
# The builder host produces images that compute hosts boot, so it may share
# the guest and image contracts but never Fire Fuse runtime or host behavior.
BUILDER_FIRE_FUSE_CONTRACTS = {
    "config",
    "guest-runtime/guestconfig",
    "host/hostpath",
    "runtime/sandboxprofile",
}
IMPORT = re.compile(r'["\']([^"\']+)["\']')
@dataclass(frozen=True)
class Violation:
    path: Path
    line: int
    message: str

    def __str__(self) -> str:
        return f"{self.path}:{self.line}: {self.message}"


def quoted_imports(path: Path):
    text = path.read_text(encoding="utf-8")
    for match in IMPORT.finditer(text):
        yield text.count("\n", 0, match.start()) + 1, match.group(1)


def check_go(root: Path) -> list[Violation]:
    violations: list[Violation] = []
    fire_fuse = root / "fire-fuse"
    for path in fire_fuse.rglob("*.go"):
        relative = path.relative_to(root)
        surface = relative.parts[1] if len(relative.parts) > 1 else ""
        for line, imported in quoted_imports(path):
            if imported == BUILDER_MODULE or imported.startswith(BUILDER_MODULE + "/"):
                violations.append(
                    Violation(relative, line, "compute must not import builder code")
                )
            if (
                len(relative.parts) > 2
                and relative.parts[1:3] == ("runtime", "microvm")
                and not path.name.endswith("_test.go")
                and imported == "database/sql"
            ):
                violations.append(
                    Violation(
                        relative,
                        line,
                        "microvm orchestration must use persistence repositories",
                    )
                )
            if not imported.startswith(GO_MODULE):
                continue
            target = imported.removeprefix(GO_MODULE)
            target_surface = target.split("/", 1)[0]

            if surface == "gateway":
                allowed_runtime = target == "runtime/model" or target.startswith(
                    "runtime/session"
                )
                if target_surface in {"host", "guest-runtime"} or (
                    target_surface == "runtime" and not allowed_runtime
                ):
                    violations.append(
                        Violation(relative, line, f"gateway must not import {target}")
                    )

            dispatcher_runtime_contract = target.startswith("runtime/session")
            if surface == "dispatcher" and (
                target_surface in {"gateway", "guest-runtime", "host"}
                or (target_surface == "runtime" and not dispatcher_runtime_contract)
            ):
                violations.append(
                    Violation(relative, line, f"dispatcher must not import {target}")
                )

            if surface in {
                "controlclient",
                "edgeprotocol",
                "messagedelivery",
                "messageprotocol",
                "sandboxhttp",
                "sessionbroker",
            } and target_surface == "gateway":
                violations.append(
                    Violation(
                        relative,
                        line,
                        "request coordination capabilities must not import gateway",
                    )
                )

            if surface == "guest-runtime" and target_surface in {
                "gateway",
                "host",
                "runtime",
            }:
                violations.append(
                    Violation(relative, line, f"guest runtime must not import {target}")
                )

            if surface == "host" and target_surface == "gateway":
                violations.append(
                    Violation(relative, line, f"host must not import {target}")
                )

            if surface == "host" and target_surface == "runtime":
                allowed_runtime = target in {
                    "runtime/model",
                    "runtime/sandboxprofile",
                }
                if not allowed_runtime:
                    violations.append(
                        Violation(relative, line, "host must use runtime model contracts")
                    )

            if (
                len(relative.parts) > 2
                and relative.parts[1:3] == ("runtime", "runtimeprovider")
                and target == "runtime/microvm"
            ):
                violations.append(
                    Violation(relative, line, "runtime providers must use model contracts")
                )

            if (
                len(relative.parts) > 2
                and relative.parts[1:3] == ("runtime", "persistence")
                and target == "runtime/microvm"
            ):
                violations.append(
                    Violation(relative, line, "persistence must not import orchestration")
                )
    return violations


def check_builder(root: Path) -> list[Violation]:
    violations: list[Violation] = []
    builder = root / "builder"
    if not builder.exists():
        return violations
    for path in builder.rglob("*.go"):
        relative = path.relative_to(root)
        for line, imported in quoted_imports(path):
            if not imported.startswith(GO_MODULE):
                continue
            target = imported.removeprefix(GO_MODULE)
            if target not in BUILDER_FIRE_FUSE_CONTRACTS:
                violations.append(
                    Violation(
                        relative,
                        line,
                        f"builder must use shared image contracts, not {target}",
                    )
                )
    return violations


def check_console(root: Path) -> list[Violation]:
    violations: list[Violation] = []
    for source in (root / "console" / "core", root / "console" / "workers"):
        if not source.exists():
            continue
        for path in source.rglob("*.ts"):
            relative = path.relative_to(root)
            for line, imported in quoted_imports(path):
                if imported.startswith("@/") or (imported.startswith(".") and "/web/" in f"/{imported}/"):
                    violations.append(
                        Violation(
                            relative,
                            line,
                            "Console Core and workers must not import Console Web",
                        )
                    )
    return violations


def check(root: Path) -> list[Violation]:
    return sorted(
        check_go(root) + check_builder(root) + check_console(root),
        key=lambda item: (item.path, item.line),
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    violations = check(args.root.resolve())
    if violations:
        for violation in violations:
            print(violation)
        return 1
    print("architecture boundaries passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
