"""Non-secret deployment identities shared by rendering, preflight and Ansible."""

from __future__ import annotations

import json
from pathlib import Path
import re
from urllib.parse import urlsplit

LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z")


def valid_dns_name(value, maximum=253):
    if not isinstance(value, str) or len(value) > maximum:
        return False
    labels = value.split(".")
    return len(labels) >= 2 and all(LABEL.fullmatch(part) for part in labels) and bool(re.match(r"[a-z]", labels[-1]))


def valid_app_domain(value):
    return valid_dns_name(value, 189)


def valid_https_origin(value):
    if not isinstance(value, str):
        return False
    try:
        url = urlsplit(value)
        port = url.port
        canonical = f"https://{url.hostname}" + (f":{port}" if port and port != 443 else "")
        return (url.scheme == "https" and valid_dns_name(url.hostname) and not url.username and
                not url.password and not url.path and not url.query and not url.fragment and
                port != 0 and value == canonical)
    except ValueError:
        return False


def validate_profile(value):
    required = {"schema_version", "app_domain", "zone_name", "console_origin", "resource_prefix", "environment"}
    if not isinstance(value, dict) or required - value.keys() or value.keys() - required - {"console_origins"}:
        raise ValueError("deployment profile requires only the documented non-secret fields")
    if type(value["schema_version"]) is not int or value["schema_version"] != 1:
        raise ValueError("unsupported deployment profile schema_version")
    if not valid_app_domain(value["app_domain"]):
        raise ValueError("app_domain must be a canonical lowercase DNS domain of at most 189 characters")
    if not valid_dns_name(value["zone_name"]):
        raise ValueError("zone_name must be a canonical lowercase DNS zone")
    if value["app_domain"] != value["zone_name"] and not value["app_domain"].endswith("." + value["zone_name"]):
        raise ValueError("app_domain must be inside zone_name")
    if not valid_https_origin(value["console_origin"]):
        raise ValueError("console_origin must be an exact HTTPS origin without credentials, path, query or fragment")
    console_host = urlsplit(value["console_origin"]).hostname
    if (console_host == value["app_domain"] or console_host.endswith("." + value["app_domain"])) and console_host != "console." + value["app_domain"]:
        raise ValueError("console_origin must use the reserved console host or a domain outside app_domain")
    if not isinstance(value["resource_prefix"], str) or len(value["resource_prefix"]) > 24 or not LABEL.fullmatch(value["resource_prefix"]):
        raise ValueError("resource_prefix must be a lowercase DNS label of at most 24 characters")
    if not isinstance(value["environment"], str) or not re.fullmatch(r"[a-z][a-z0-9]{0,15}", value["environment"]):
        raise ValueError("environment must be 1-16 lowercase letters/digits starting with a letter")
    origins = value.get("console_origins", [value["console_origin"]])
    if not isinstance(origins, list) or not origins or not all(valid_https_origin(origin) for origin in origins) or value["console_origin"] not in origins:
        raise ValueError("console_origins must contain the console origin and only exact HTTPS origins")
    result = {**value, "console_origins": list(dict.fromkeys(origins))}
    result.update(origin_hostname=f"edge-origin.{value['app_domain']}",
                  release_gate_origin=f"https://release-gate.{value['app_domain']}",
                  observer_domain=f"apps.{value['app_domain']}",
                  observer_hostname=f"status.apps.{value['app_domain']}",
                  dispatch_namespace=f"{value['resource_prefix']}-user-workers-{value['environment']}")
    return result


def load_profile(path):
    try:
        return validate_profile(json.loads(Path(path).read_text()))
    except json.JSONDecodeError:
        raise ValueError("deployment profile must be valid JSON") from None
