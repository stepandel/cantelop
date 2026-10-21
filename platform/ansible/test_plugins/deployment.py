"""Canonical operator identities, shared with the configuration renderer."""

import importlib.util
from pathlib import Path

_path = Path(__file__).resolve().parents[3] / "scripts" / "deployment_config.py"
_spec = importlib.util.spec_from_file_location("cantelop_deployment_config", _path)
_module = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_module)


class TestModule:
    def tests(self):
        return {
            "deployment_dns_name": _module.valid_dns_name,
            "deployment_app_domain": _module.valid_app_domain,
            "deployment_https_origin": _module.valid_https_origin,
        }
