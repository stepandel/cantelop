import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"
CLOUDFLARE_DEPLOY_SCRIPT = ROOT / "platform" / "cloudflare" / "deploy-workers.sh"


class DeploymentWorkflowTests(unittest.TestCase):
    def test_cloudflare_deployment_is_manual_and_isolated(self):
        workflow = (WORKFLOWS / "deploy-cloudflare.yml").read_text()

        self.assertIn("workflow_dispatch:", workflow)
        self.assertNotIn("\n  push:", workflow)
        self.assertIn("name: Deploy Cloudflare", workflow)
        self.assertIn("environment: cloudflare", workflow)
        self.assertIn("group: deploy-cloudflare", workflow)
        self.assertIn("runs-on: ubuntu-latest", workflow)
        self.assertIn("bun-version-file: console/package.json", workflow)
        self.assertNotIn("cloudflare-production", workflow)
        self.assertNotIn("cloudflare-staging", workflow)
        self.assertNotIn("self-hosted", workflow)
        self.assertNotIn("apply_infrastructure", workflow)
        self.assertNotIn("CLOUDFLARE_TFVARS_PATH", workflow)
        self.assertNotIn("CLOUDFLARE_STATE_PATH", workflow)
        self.assertNotIn("terraform ", workflow)
        self.assertIn("platform/cloudflare/deploy-workers.sh", workflow)
        self.assertIn("APP_ROUTES_KV_NAMESPACE_ID", workflow)
        self.assertIn("ORIGIN_ACCESS_CLIENT_SECRET", workflow)
        self.assertNotIn("deploy-fire-fuse", workflow)

        deploy_script = CLOUDFLARE_DEPLOY_SCRIPT.read_text()
        self.assertNotIn("terraform ", deploy_script)
        self.assertIn("APP_ROUTES_KV_NAMESPACE_ID", deploy_script)
        self.assertIn("put_environment_secret", deploy_script)
        self.assertNotIn("USER_WORKER_MODULE", deploy_script)
        self.assertNotIn("--dispatch-namespace", deploy_script)
        self.assertNotIn("wrangler kv key put", deploy_script)

if __name__ == "__main__":
    unittest.main()
