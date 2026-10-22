from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch


HELPER = runpy.run_path(str(Path(__file__).resolve().parents[1] / "ci-scope.py"))
classify = HELPER["classify"]


class CIScopeTests(unittest.TestCase):
    def enabled(self, *paths):
        return {lane for lane, enabled in classify(list(paths)).items() if enabled}

    def test_site_changes_do_not_start_runtime_or_infrastructure(self):
        self.assertEqual(self.enabled("console/web/app/page.tsx", "console/web/app/home.module.css"), {"console"})
        self.assertEqual(self.enabled("docs/contributor-ci.md", "README.md", "fire-fuse/README.md", "storage/juicefs/README.md"), set())

    def test_shared_contracts_dependencies_and_ci_run_all_components(self):
        for path in ("go.mod", "console/bun.lock", "console/package.json", "console/core/db/schema.ts", "console/web/app/v1/apps/route.ts", "console/web/app/internal/v1/runtime/route.ts", "schemas/cli-api-contract.json", "edge/contracts/index.ts", "fire-fuse/runtime/model/model.go", "clients/cli/internal/deploy/build.go", "scripts/build-cli-release.sh", ".github/workflows/check.yml"):
            with self.subTest(path=path):
                self.assertTrue({"go", "console", "edge", "policy"} <= self.enabled(path))

    def test_platform_and_edge_changes_keep_their_policy_checks(self):
        self.assertEqual(self.enabled("platform/ansible/roles/storage/tasks/main.yml"), {"policy"})
        self.assertEqual(self.enabled("edge/workers/dispatcher/src/core.ts"), {"edge", "policy"})
        self.assertEqual(self.enabled("platform/cloudflare/main.tf"), {"edge", "policy", "terraform"})
        self.assertEqual(self.enabled("platform/systemd/compute/fire-fuse.service"), {"go", "policy"})

    def test_juicefs_qualification_tracks_its_actual_build_inputs(self):
        self.assertIn("juicefs", self.enabled("storage/juicefs/workspace-data-only.patch"))
        self.assertIn("juicefs", self.enabled("storage/juicefs/prepare-juicefs-linux-amd64.sh"))
        self.assertNotIn("juicefs", self.enabled("platform/ansible/roles/storage/tasks/main.yml", "go.mod"))

    def test_unknown_inputs_and_main_or_manual_runs_keep_broad_coverage(self):
        self.assertTrue({"go", "console", "edge", "policy"} <= self.enabled("new-build-config.json"))
        self.assertTrue(all(classify([], full=True).values()))

    def test_diff_includes_deleted_source_when_renamed_to_documentation(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                return subprocess.check_output(["git", "-C", directory, *args], stderr=subprocess.DEVNULL).decode().strip()

            git("init")
            git("config", "user.name", "Fixture")
            git("config", "user.email", "fixture@example.invalid")
            source = Path(directory) / "console/web/page.tsx"
            source.parent.mkdir(parents=True)
            source.write_text("fixture\n")
            git("add", ".")
            git("commit", "-m", "base")
            base = git("rev-parse", "HEAD")
            (Path(directory) / "docs").mkdir()
            git("mv", "console/web/page.tsx", "docs/page.md")
            git("commit", "-m", "move")
            original = subprocess.check_output
            with patch("subprocess.check_output", side_effect=lambda args: original(args, cwd=directory)):
                paths = HELPER["changed_paths"](base)
            self.assertEqual(set(paths), {"console/web/page.tsx", "docs/page.md"})
            self.assertEqual(self.enabled(*paths), {"console"})

    def test_invalid_base_cannot_be_interpreted_as_a_git_option(self):
        with self.assertRaisesRegex(ValueError, "full Git commit"):
            HELPER["changed_paths"]("--output=somewhere")


if __name__ == "__main__":
    unittest.main()
