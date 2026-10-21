import importlib.util
from pathlib import Path
import unittest

from jinja2 import Environment, StrictUndefined
import yaml

ANSIBLE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("deployment_tests", ANSIBLE / "test_plugins/deployment.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)


class DeploymentIdentityTest(unittest.TestCase):
    def setUp(self):
        self.env = Environment(undefined=StrictUndefined)
        self.env.tests.update(plugin.TestModule().tests())

    def test_operator_service_commands_preserve_loopback_and_credential_boundaries(self):
        role = ANSIBLE / "roles/compute/fire_fuse_gateway"
        values = yaml.safe_load((role / "defaults/main.yml").read_text())
        values.update(fire_fuse_gateway_app_domain="agents.example.net", fire_fuse_gateway_product_control_origin="https://console.example.net", fire_fuse_gateway_observability_origin="https://console.example.net")
        for name in ["deployment.conf.j2", "dispatcher-deployment.conf.j2"]:
            rendered = self.env.from_string((role / "templates" / name).read_text()).render(values)
            self.assertIn("-control-plane http://127.0.0.1:8080", rendered)
            self.assertIn("https://console.example.net", rendered)
            self.assertNotIn("cantelop.dev", rendered)
        gateway = self.env.from_string((role / "templates/deployment.conf.j2").read_text()).render(values)
        self.assertIn("-listen 127.0.0.1:8081", gateway)
        self.assertIn("-app-domain agents.example.net", gateway)
        self.assertIn("-product-token-file /etc/fire-fuse-gateway/product-control.token", gateway)

    def test_observer_requires_canonical_domain_exact_status_host_and_certificate_path(self):
        role = ANSIBLE / "roles/compute/fire_fuse_observer"
        values = yaml.safe_load((role / "defaults/main.yml").read_text())
        values.update(fire_fuse_observer_domain="apps.agents.example.net", fire_fuse_observer_hostname="status.apps.agents.example.net")
        for name in ["fire_fuse_observer_tls_certificate", "fire_fuse_observer_tls_key"]:
            values[name] = self.env.from_string(values[name]).render(values)
        expressions = yaml.safe_load((role / "tasks/main.yml").read_text())[0]["ansible.builtin.assert"]["that"]
        identity = [expression for expression in expressions if any(key in expression for key in ["observer_domain", "observer_hostname", "observer_tls_"])]
        self.assertEqual(len(identity), 4)
        self.assertTrue(all(self.env.compile_expression(expression)(**values) for expression in identity))
        for override in [{"fire_fuse_observer_domain": "../../private"}, {"fire_fuse_observer_hostname": "foreign.example.net"}, {"fire_fuse_observer_tls_key": "/tmp/privkey.pem"}]:
            self.assertFalse(all(self.env.compile_expression(expression)(**{**values, **override}) for expression in identity))
        for origin in ["https://console.example.net/%n", "https://console.example.net;touch", "https://user@console.example.net", "https://console.example.net:443"]:
            self.assertFalse(self.env.tests["deployment_https_origin"](origin))


if __name__ == "__main__":
    unittest.main()
