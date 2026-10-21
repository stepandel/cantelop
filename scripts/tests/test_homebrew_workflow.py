import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/publish-homebrew.yml"


class HomebrewWorkflowTests(unittest.TestCase):
    def test_published_releases_manual_retries_and_workflow_calls_are_supported(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn("workflow_call:", workflow)
        self.assertIn("release:\n    types: [published]", workflow)
        self.assertIn("workflow_dispatch:", workflow)
        self.assertIn("SOURCE_TAG: ${{ github.event.release.tag_name || inputs.tag }}", workflow)
        self.assertIn("group: publish-homebrew-tap", workflow)
        self.assertIn("cancel-in-progress: false", workflow)

    def test_source_token_is_read_only_and_tap_uses_separate_secret(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn("permissions:\n  contents: read", workflow)
        self.assertIn("GH_TOKEN: ${{ github.token }}", workflow)
        self.assertIn("GH_TOKEN: ${{ secrets.TAP_GITHUB_TOKEN }}", workflow)
        self.assertNotIn("contents: write", workflow)

    def test_public_assets_are_verified_before_formula_push(self) -> None:
        workflow = WORKFLOW.read_text()

        validation = workflow.index("Validate qualified release")
        tag_identity = workflow.index("Verify tag identity")
        installer = workflow.index("Render curl installer")
        mirror_upload = workflow.index("Create or repair public tap release")
        mirror_verification = workflow.index("Verify public tap assets")
        distribution = workflow.index("Update tap distribution")
        push = workflow.index("push origin HEAD:main")
        self.assertLess(validation, tag_identity)
        self.assertLess(tag_identity, installer)
        self.assertLess(installer, mirror_upload)
        self.assertLess(mirror_upload, mirror_verification)
        self.assertLess(mirror_verification, distribution)
        self.assertLess(distribution, push)

    def test_installer_is_published_and_committed_with_the_formula(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn("render-installer", workflow)
        self.assertIn("build/homebrew-source/install.sh", workflow)
        self.assertIn("sh -n build/homebrew-source/install.sh", workflow)
        stage = workflow.index("git -C build/homebrew-tap add Formula/cantelop.rb install.sh")
        unchanged = workflow.index("git -C build/homebrew-tap diff --cached --quiet")
        commit = workflow.index('git -C build/homebrew-tap commit -m "Distribution update')
        self.assertLess(stage, unchanged)
        self.assertLess(unchanged, commit)

    def test_workflow_reuses_release_artifacts_without_rebuilding(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn("release-evidence.json", workflow)
        self.assertIn("notarization-log.json", workflow)
        self.assertNotIn("build-cli-release.sh", workflow)
        self.assertNotIn("go build", workflow)


if __name__ == "__main__":
    unittest.main()
