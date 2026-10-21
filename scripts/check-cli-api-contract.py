#!/usr/bin/env python3
import json
import re
import sys
from pathlib import Path


HTTP_METHOD = re.compile(r"export\s+(?:async\s+)?function\s+(GET|POST|PUT|PATCH|DELETE)\s*\(")


def function_body(document: str, symbol: str) -> str:
    match = re.search(rf"\bfunc\s+{re.escape(symbol)}\s*\(", document)
    if not match:
        raise ValueError(f"missing CLI function {symbol}")
    start = document.find("{", match.end())
    if start < 0:
        raise ValueError(f"missing body for CLI function {symbol}")
    depth = 0
    for index in range(start, len(document)):
        if document[index] == "{":
            depth += 1
        elif document[index] == "}":
            depth -= 1
            if depth == 0:
                return document[start:index + 1]
    raise ValueError(f"unterminated body for CLI function {symbol}")


def check(root: Path) -> list[str]:
    manifest = json.loads((root / "schemas/cli-api-contract.json").read_text())
    failures: list[str] = []
    declared: set[tuple[str, str]] = set()
    for entry in manifest.get("routes", []):
        route = entry["route"]
        method = entry["method"]
        declared.add((route, method))
        route_document = (root / route).read_text()
        if method not in HTTP_METHOD.findall(route_document):
            failures.append(f"{route} does not export {method}")
        cli_document = (root / entry["cli_file"]).read_text()
        try:
            body = function_body(cli_document, entry["cli_symbol"])
        except ValueError as error:
            failures.append(str(error))
            continue
        for token in entry["tokens"]:
            if token not in body:
                failures.append(f"{entry['cli_symbol']} no longer contains {token!r} for {method} {route}")

    for entry in manifest.get("non_cli_routes", []):
        declared.add((entry["route"], entry["method"]))

    for base in (root / "console/web/app/v1", root / "console/web/app/api/cli"):
        for route_path in base.rglob("route.ts"):
            relative = route_path.relative_to(root).as_posix()
            for method in HTTP_METHOD.findall(route_path.read_text()):
                if (relative, method) not in declared:
                    failures.append(f"public route has no CLI contract: {method} {relative}")

    operations = (root / "console/web/lib/customer-operations.ts").read_text()
    for retired in ("getTenantResource", "deleteTenantResource", "CANTELOP_CONTROL_URL"):
        if retired in operations:
            failures.append(f"Session operations regressed to retired tenant bridge: {retired}")
    for required in ("getCustomerSnapshot", "terminateSession"):
        if required not in operations:
            failures.append(f"Session operations no longer use {required}")

    # The SDK loader's JavaScript protocol list is generated from this Go slice.
    protocol_match = None
    for source_path in sorted((root / "clients/cli").rglob("*.go")):
        protocol_match = re.search(
            r"\b[Ss]upportedSDKBuildProtocolVersions = \[\]int\{([0-9, ]+)\}", source_path.read_text()
        )
        if protocol_match is not None:
            break
    if protocol_match is None:
        failures.append("project deployment no longer enforces an SDK build protocol")
    else:
        protocols = re.findall(r"\d+", protocol_match.group(1))
        cli_guide = (root / "docs/cli.md").read_text()
        documented = re.search(
            r"accepted SDK must report build protocol version ((?:`[0-9]+`(?: or )?)+)", cli_guide
        )
        if documented is None or re.findall(r"\d+", documented.group(1)) != protocols:
            failures.append(
                f"CLI guide build protocol does not match implementation versions {', '.join(protocols)}"
            )
    return failures


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    failures = check(root)
    if failures:
        for failure in failures:
            print(f"cli-api contract: {failure}", file=sys.stderr)
        return 1
    print("cli-api contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
