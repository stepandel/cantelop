#!/usr/bin/env python3
"""Render non-secret configuration and perform read-only self-hosting preflight."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from scripts.deployment_config import load_profile, validate_profile, valid_https_origin

WORKERS = {"origin-broker": "origin-broker", "outbound": "outbound", "tail": "edge-tail", "dispatcher": "dispatcher"}


def json_bytes(value):
    return (json.dumps(value, indent=2) + "\n").encode()


def render(profile, destination, root=ROOT):
    profile = validate_profile({key: value for key, value in profile.items() if key in {
        "schema_version", "app_domain", "zone_name", "console_origin", "console_origins", "resource_prefix", "environment"}})
    destination = Path(destination)
    if destination.exists():
        raise ValueError("render destination must not exist; use a new private output directory")
    destination.mkdir(parents=True, mode=0o700)
    prefix = profile["resource_prefix"]
    names = {worker: f"{prefix}-{suffix}" for worker, suffix in WORKERS.items()}
    for worker in WORKERS:
        source = root / "edge/workers" / worker
        config = json.loads((source / "wrangler.jsonc").read_text())
        config.update(name=names[worker], main=str(source / "src/index.ts"),
                      **{"$schema": str(root / "edge/workers/node_modules/wrangler/config-schema.json")})
        config.setdefault("vars", {})["CANTELOP_APP_DOMAIN"] = profile["app_domain"]
        if worker in {"dispatcher", "tail"}:
            config["vars"]["OBSERVABILITY_INGEST_ORIGIN"] = profile["console_origin"]
        if worker == "dispatcher":
            config["routes"] = [{"pattern": f"*.{profile['app_domain']}/*", "zone_name": profile["zone_name"]}]
            config["tail_consumers"] = [{"service": names["tail"]}]
            config["dispatch_namespaces"][0]["namespace"] = profile["dispatch_namespace"]
            config["dispatch_namespaces"][0]["outbound"]["service"] = names["outbound"]
        if worker == "outbound":
            config["services"] = [{"binding": "BROKER", "service": names["origin-broker"]}]
        target = destination / worker / "wrangler.jsonc"
        target.parent.mkdir()
        target.write_bytes(json_bytes(config))
    terraform = {key: profile[key] for key in ["app_domain", "zone_name", "resource_prefix", "environment"]}
    (destination / "deployment.auto.tfvars.json").write_bytes(json_bytes(terraform))
    variables = {
        "fire_fuse_gateway_app_domain": profile["app_domain"],
        "fire_fuse_gateway_product_control_origin": profile["console_origin"],
        "fire_fuse_gateway_observability_origin": profile["console_origin"],
        "fire_fuse_observer_domain": profile["observer_domain"],
        "fire_fuse_observer_hostname": profile["observer_hostname"],
        "fire_fuse_control_cutover_tls_domain": profile["observer_domain"],
        "cantelop_control_worker_app_domain": profile["app_domain"],
        "cantelop_control_worker_release_gate_origin": profile["release_gate_origin"],
        "cantelop_control_worker_cloudflare_dispatch_namespace": profile["dispatch_namespace"],
        "cantelop_control_worker_artifact_bucket": f"{prefix}-api-artifacts",
        "fire_fuse_stage_host_pool": {"slots_per_profile": 2},
        "fire_fuse_stage_limits": {"max_vms": 8, "max_total_memory_mib": 32768, "max_concurrent_starts": 2},
    }
    (destination / "ansible-deployment-vars.json").write_bytes(json_bytes(variables))
    values = {
        "CANTELOP_APP_DOMAIN": profile["app_domain"],
        "CANTELOP_CONSOLE_ORIGINS": ",".join(profile["console_origins"]),
        "CANTELOP_CONTROL_ORIGIN": profile["console_origin"],
        "CANTELOP_DOCS_ORIGIN": profile["console_origin"],
        "NEXTAUTH_URL": profile["console_origin"],
        "CLOUDFLARE_DISPATCH_NAMESPACE": profile["dispatch_namespace"],
        "CANTELOP_TURSO_ENVIRONMENT": profile["environment"],
        "CANTELOP_ARTIFACT_BUCKET": f"{prefix}-api-artifacts",
        "FIRE_FUSE_OBSERVER_URL": f"https://{profile['observer_hostname']}",
        "RELEASE_GATE_ORIGIN": profile["release_gate_origin"],
    }
    lines = (root / "console/.env.example").read_text().splitlines()
    for key, value in values.items():
        replaced = False
        for index, line in enumerate(lines):
            if line.startswith(key + "="):
                lines[index] = f"{key}={value}"
                replaced = True
                break
        if not replaced:
            lines.append(f"{key}={value}")
    (destination / "console.env.example").write_text("\n".join(lines) + "\n")
    (destination / "deployment.json").write_bytes(json_bytes({key: value for key, value in profile.items() if key in terraform or key in {"schema_version", "console_origin", "console_origins"}}))
    return {"worker_names": names, "resources_created": 0, "credentials_generated": False}


def read_environment(path):
    values = {}
    for number, line in enumerate(Path(path).read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:]
        try:
            fields = shlex.split(line, comments=True)
        except ValueError:
            raise ValueError(f"environment line {number} has invalid quoting") from None
        if len(fields) != 1 or "=" not in fields[0]:
            raise ValueError(f"environment line {number} must be one literal KEY=value assignment")
        key, value = fields[0].split("=", 1)
        if not re.fullmatch(r"[A-Z][A-Z0-9_]*", key) or key in values:
            raise ValueError(f"environment line {number} has an invalid or duplicate key")
        values[key] = value
    return values


def check_environment(values, profile):
    required = ["NEXTAUTH_SECRET", "GITHUB_ID", "GITHUB_SECRET", "GOOGLE_ID", "GOOGLE_SECRET", "ADMIN_EMAIL",
                "CANTELOP_DATABASE_URL", "CANTELOP_DATABASE_SSL", "CANTELOP_CONFIG_ENCRYPTION_KEY", "CANTELOP_CONTROL_PRIVATE_KEY",
                "CANTELOP_COMPUTE_INGRESS_URL", "CANTELOP_COMPUTE_CONTROL_TOKEN", "CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN",
                "CANTELOP_ARTIFACT_ENDPOINT", "CANTELOP_ARTIFACT_ACCESS_KEY_ID", "CANTELOP_ARTIFACT_SECRET_ACCESS_KEY",
                "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ROUTE_NAMESPACE_ID", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_OBSERVABILITY_TOKEN",
                "CANTELOP_OBSERVABILITY_INGEST_TOKEN", "CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN", "RELEASE_GATE_TOKEN",
                "CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY", "CANTELOP_TURSO_ORGANIZATION", "CANTELOP_TURSO_GROUP",
                "CANTELOP_TURSO_PLATFORM_TOKEN", "FIRE_FUSE_STATUS_TOKEN", "FIRE_FUSE_RUNTIME_TOKEN"]
    checks = []
    for name in required:
        value = values.get(name, "")
        valid = bool(value) and not any(marker in value for marker in ["replace-", "your-cantelop", "example.com"])
        checks.append({"name": name, "ok": valid, "detail": "configured" if valid else "missing or placeholder"})
    expected = {"CANTELOP_APP_DOMAIN": profile["app_domain"], "CANTELOP_CONTROL_ORIGIN": profile["console_origin"],
                "CANTELOP_CONSOLE_ORIGINS": ",".join(profile["console_origins"]), "RELEASE_GATE_ORIGIN": profile["release_gate_origin"],
                "CLOUDFLARE_DISPATCH_NAMESPACE": profile["dispatch_namespace"], "CANTELOP_TURSO_ENVIRONMENT": profile["environment"],
                "CANTELOP_ARTIFACT_BUCKET": f"{profile['resource_prefix']}-api-artifacts",
                "NEXTAUTH_URL": profile["console_origin"], "FIRE_FUSE_OBSERVER_URL": f"https://{profile['observer_hostname']}"}
    for name, value in expected.items():
        checks.append({"name": name, "ok": values.get(name) == value, "detail": "must match deployment profile"})
    checks.append({"name": "CANTELOP_DATABASE_SSL", "ok": values.get("CANTELOP_DATABASE_SSL") == "verify-full", "detail": "TLS certificate verification required"})
    token_names = ["CANTELOP_COMPUTE_CONTROL_TOKEN", "CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN", "CANTELOP_OBSERVABILITY_INGEST_TOKEN",
                   "CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN", "CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY", "RELEASE_GATE_TOKEN"]
    tokens = [values.get(name, "") for name in token_names]
    checks.append({"name": "dedicated-service-credentials", "ok": len(set(tokens)) == len(tokens) and all(re.fullmatch(r"[0-9a-f]{64}", token) for token in tokens), "detail": "six distinct 64-character lowercase hex credentials required"})
    for name in ["CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ROUTE_NAMESPACE_ID"]:
        checks.append({"name": name, "ok": bool(re.fullmatch(r"[0-9a-f]{32}", values.get(name, ""))), "detail": "32 lowercase hex characters required"})
    for name in ["CANTELOP_ARTIFACT_ENDPOINT"]:
        checks.append({"name": name, "ok": valid_https_origin(values.get(name)), "detail": "canonical HTTPS origin required"})
    formats = {
        "CANTELOP_CONFIG_ENCRYPTION_KEY": r"[A-Za-z0-9_-]{43}",
        "CANTELOP_DATABASE_URL": r"postgres(?:ql)?://[^\r\n ]+",
        "CANTELOP_COMPUTE_INGRESS_URL": r"http://10\.77\.0\.1:8084",
        "FIRE_FUSE_STATUS_TOKEN": r"[0-9a-f]{64}",
        "FIRE_FUSE_RUNTIME_TOKEN": r"[0-9a-f]{64}",
        "CANTELOP_SOURCE_REPOSITORY": r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+",
    }
    for name, pattern in formats.items():
        checks.append({"name": name, "ok": bool(re.fullmatch(pattern, values.get(name, ""))), "detail": "valid operator setting required"})
    commit = values.get("CANTELOP_SOURCE_COMMIT") or values.get("VERCEL_GIT_COMMIT_SHA", "")
    checks.append({"name": "public-source-revision", "ok": bool(re.fullmatch(r"[0-9a-f]{40}", commit)), "detail": "full corresponding public source commit required"})
    dedicated = tokens + [values.get("FIRE_FUSE_STATUS_TOKEN", ""), values.get("FIRE_FUSE_RUNTIME_TOKEN", "")]
    checks.append({"name": "observer-credential-separation", "ok": len(set(dedicated)) == len(dedicated), "detail": "observer and Session mutation credentials must also be distinct"})
    return checks


def controller_checks():
    checks = []
    commands = {"git": ["--version"], "go": ["version"], "bun": ["--version"], "terraform": ["version", "-json"],
                "ansible-playbook": ["--version"], "ssh": ["-V"], "openssl": ["version"], "docker": ["info", "--format", "{{.OSType}}"], "jq": ["--version"]}
    for name, args in commands.items():
        executable = shutil.which(name)
        ok = False
        if executable:
            try:
                result = subprocess.run([executable, *args], capture_output=True, timeout=15,
                                        env={**os.environ, "GOTOOLCHAIN": "local", "ANSIBLE_LOCAL_TEMP": tempfile.gettempdir()})
                ok = result.returncode == 0
                if name == "docker":
                    ok = ok and result.stdout.strip() == b"linux"
                if name == "terraform":
                    parsed = json.loads(result.stdout).get("terraform_version", "0.0.0")
                    ok = ok and tuple(int(part) for part in parsed.split(".")[:2]) >= (1, 8)
                if name == "go":
                    parsed = re.search(rb"go([0-9]+)\.([0-9]+)\.([0-9]+)", result.stdout)
                    minimum = re.search(r"^go ([0-9]+)\.([0-9]+)\.([0-9]+)", (ROOT / "go.mod").read_text(), re.M)
                    ok = ok and bool(parsed) and tuple(map(int, parsed.groups())) >= tuple(map(int, minimum.groups()))
                if name == "bun":
                    version = json.loads((ROOT / "console/package.json").read_text())["packageManager"].removeprefix("bun@")
                    ok = ok and result.stdout.decode().strip() == version
            except (OSError, ValueError, subprocess.TimeoutExpired):
                pass
        checks.append({"name": name, "ok": ok, "detail": "available" if ok else "missing, wrong version, or unavailable"})
    checks.append({"name": "python", "ok": sys.version_info >= (3, 12), "detail": "Python 3.12 or later required"})
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for command in ["render", "preflight"]:
        sub = commands.add_parser(command)
        sub.add_argument("--config", required=True, type=Path)
        if command == "render":
            sub.add_argument("--output", required=True, type=Path)
        else:
            sub.add_argument("--env-file", type=Path)
            sub.add_argument("--skip-tools", action="store_true", help="configuration checks only; does not qualify the controller")
    args = parser.parse_args()
    try:
        profile = load_profile(args.config)
        if args.command == "render":
            print(json.dumps(render(profile, args.output), indent=2))
            return
        checks = [] if args.skip_tools else controller_checks()
        if args.env_file:
            checks.extend(check_environment(read_environment(args.env_file), profile))
        result = {"profile_valid": True, "checks": checks, "environment_checked": bool(args.env_file),
                  "tools_checked": not args.skip_tools, "resources_created": 0, "provider_calls_performed": False,
                  "fresh_host_qualification": "not_run", "installation_qualified": False,
                  "checks_passed": all(check["ok"] for check in checks)}
        print(json.dumps(result, indent=2))
        if not result["checks_passed"]:
            raise SystemExit(1)
    except (ValueError, OSError) as error:
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()
