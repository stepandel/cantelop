import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("self_hosting", ROOT / "scripts/self-hosting.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)
from scripts.deployment_config import validate_profile, valid_app_domain, valid_https_origin


class SelfHostingTest(unittest.TestCase):
    def setUp(self):
        self.profile = json.loads((ROOT / "deployment.example.json").read_text())

    def test_render_keeps_all_boundaries_on_one_operator_identity(self):
        profile = validate_profile(self.profile)
        with tempfile.TemporaryDirectory() as parent:
            output = Path(parent) / "deployment"
            result = installer.render(profile, output)
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            self.assertEqual(result["resources_created"], 0)
            configs = {name: json.loads((output / name / "wrangler.jsonc").read_text()) for name in installer.WORKERS}
            for config in configs.values():
                self.assertEqual(config["vars"]["CANTELOP_APP_DOMAIN"], "agents.example.net")
                self.assertTrue(Path(config["main"]).is_file())
            dispatcher = configs["dispatcher"]
            self.assertEqual(dispatcher["routes"], [{"pattern": "*.agents.example.net/*", "zone_name": "example.net"}])
            self.assertEqual(dispatcher["dispatch_namespaces"][0]["namespace"], profile["dispatch_namespace"])
            self.assertEqual(dispatcher["dispatch_namespaces"][0]["outbound"]["service"], configs["outbound"]["name"])
            self.assertEqual(configs["outbound"]["services"][0]["service"], configs["origin-broker"]["name"])
            self.assertEqual(dispatcher["tail_consumers"][0]["service"], configs["tail"]["name"])
            variables = json.loads((output / "ansible-deployment-vars.json").read_text())
            env = installer.read_environment(output / "console.env.example")
            self.assertEqual(variables["cantelop_control_worker_release_gate_origin"], env["RELEASE_GATE_ORIGIN"])
            self.assertEqual(env["CANTELOP_APP_DOMAIN"], variables["fire_fuse_gateway_app_domain"])
            self.assertEqual(env["CANTELOP_CONTROL_ORIGIN"], variables["fire_fuse_gateway_product_control_origin"])
            self.assertEqual(env["FIRE_FUSE_OBSERVER_URL"], "https://" + variables["fire_fuse_observer_hostname"])
            self.assertEqual(variables["fire_fuse_stage_host_pool"]["slots_per_profile"], 2)
            self.assertEqual(validate_profile(json.loads((output / "deployment.json").read_text())), profile)
            self.assertTrue(any(not check["ok"] for check in installer.check_environment(env, profile)))
            with self.assertRaises(ValueError):
                installer.render(profile, output)

    def test_rejects_unsafe_or_ambiguous_identities(self):
        for domain in ["Example.net", "*.example.net", "example.net.", "example.net:443", "a..net", "a.1net", "127.0.0.1", "../example.net", "-a.net"]:
            with self.subTest(domain=domain):
                self.assertFalse(valid_app_domain(domain))
                with self.assertRaises(ValueError):
                    validate_profile({**self.profile, "app_domain": domain})
        for origin in ["http://console.example.net", "https://*.example.net", "https://a.example.net/", "https://a.example.net:443", "https://user@a.example.net", "https://a.example.net?x=1", "https://a.example.net/%s", "https://a.example.net:0"]:
            self.assertFalse(valid_https_origin(origin), origin)
        for override in [{"zone_name": "other.net"}, {"resource_prefix": "../private"}, {"environment": "Prod"}, {"secret": "redacted"}, {"console_origins": ["https://other.net"]}]:
            with self.assertRaises(ValueError):
                validate_profile({**self.profile, **override})

    def test_environment_parsing_never_executes_or_reports_values(self):
        with tempfile.TemporaryDirectory() as parent:
            path = Path(parent) / "private.env"
            marker = Path(parent) / "must-not-exist"
            sentinel = "private-sentinel-do-not-print"
            path.write_text(f"NEXTAUTH_SECRET='$(touch {marker})'\nGOOGLE_SECRET='{sentinel}'\n")
            values = installer.read_environment(path)
            self.assertFalse(marker.exists())
            report = installer.check_environment(values, validate_profile(self.profile))
            self.assertNotIn(sentinel, json.dumps(report))
            path.write_text(f"NEXTAUTH_SECRET='{sentinel}\n")
            with self.assertRaises(ValueError) as error:
                installer.read_environment(path)
            self.assertNotIn(sentinel, str(error.exception))

    def test_missing_provider_configuration_cannot_qualify_an_installation(self):
        result = subprocess.run([sys.executable, str(ROOT / "scripts/self-hosting.py"), "preflight", "--config", str(ROOT / "deployment.example.json"), "--skip-tools"], capture_output=True, text=True, check=True)
        report = json.loads(result.stdout)
        self.assertTrue(report["profile_valid"])
        self.assertFalse(report["environment_checked"])
        self.assertFalse(report["installation_qualified"])
        self.assertFalse(report["provider_calls_performed"])


if __name__ == "__main__":
    unittest.main()
