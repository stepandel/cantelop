import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/publish-cli.yml"
SIGNERS = ROOT / ".github/release-signers"


class CLIPublishWorkflowTests(unittest.TestCase):
    def test_signed_tags_and_manual_retries_are_supported(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn('tags:\n      - "v*"', workflow)
        self.assertIn("workflow_dispatch:", workflow)
        self.assertIn('git verify-tag "$SOURCE_TAG"', workflow)
        self.assertIn("git merge-base --is-ancestor", workflow)
        self.assertIn("namespaces=\"git\"", SIGNERS.read_text())

    def test_credentials_are_required_before_building(self) -> None:
        workflow = WORKFLOW.read_text()

        credentials = workflow.index("Require release credentials")
        build = workflow.index("Test and build release candidates")
        self.assertLess(credentials, build)
        for secret in (
            "MACOS_SIGN_P12",
            "MACOS_SIGN_PASSWORD",
            "MACOS_NOTARY_KEY",
            "MACOS_NOTARY_KEY_ID",
            "MACOS_NOTARY_ISSUER_ID",
            "TAP_GITHUB_TOKEN",
        ):
            self.assertIn(f"secrets.{secret}", workflow)

    def test_release_is_signed_notarized_and_verified_before_publication(self) -> None:
        workflow = WORKFLOW.read_text()

        signing = workflow.index("Sign and notarize macOS binaries")
        codesign = workflow.index("codesign \\", signing)
        notarize = workflow.index("notarytool submit")
        qualification = workflow.index("Qualify signed release")
        publication = workflow.index("Create or repair private CLI release")
        verification = workflow.index("Verify published CLI release")
        self.assertLess(signing, codesign)
        self.assertLess(codesign, notarize)
        self.assertLess(notarize, qualification)
        self.assertLess(qualification, publication)
        self.assertLess(publication, verification)

    def test_apple_g2_intermediate_is_pinned_before_identity_validation(self) -> None:
        workflow = WORKFLOW.read_text()

        download = workflow.index("https://www.apple.com/certificateauthority/DeveloperIDG2CA.cer")
        digest = workflow.index("f16cd3c54c7f83cea4bf1a3e6a0819c8aaa8e4a1528fd144715f350643d2df3a")
        identity = workflow.index("openssl pkcs12")
        self.assertLess(download, digest)
        self.assertLess(digest, identity)

    def test_signing_certificate_metadata_is_validated_and_fingerprint_is_used(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn("openssl pkcs12 \\\n            -in \"$certificate\" \\\n            -legacy", workflow)
        self.assertIn("-passin env:MACOS_SIGN_PASSWORD", workflow)
        self.assertIn('"UID=${TEAM_ID}"', workflow)
        self.assertIn('"CN=${SIGNING_IDENTITY}"', workflow)
        self.assertIn("issuer=C=US,O=Apple Inc.,OU=G2,CN=Developer ID Certification Authority", workflow)
        self.assertIn("-checkend 0", workflow)
        self.assertIn('echo "Exported signing certificate ${subject}"', workflow)
        self.assertIn("--sign \"$SIGNING_CERTIFICATE\"", workflow)
        self.assertIn("--keychain \"$SIGNING_KEYCHAIN\"", workflow)

    def test_linux_candidates_are_preserved_and_release_retries_are_conflict_safe(self) -> None:
        workflow = WORKFLOW.read_text()

        self.assertIn('"build/cli-candidate/cantelop_${VERSION}_linux_amd64.tar.gz"', workflow)
        self.assertIn("compare-assets \\", workflow)
        self.assertIn("--allow-missing", workflow)
        self.assertIn("--clobber", workflow)

    def test_private_publication_uses_repository_scoped_workflow_token(self) -> None:
        workflow = WORKFLOW.read_text()
        publication = workflow.split("- name: Create or repair private CLI release", 1)[1]
        publication = publication.split("- name: Verify published CLI release", 1)[0]
        self.assertIn("GH_TOKEN: ${{ github.token }}", publication)
        self.assertNotIn("secrets.TAP_GITHUB_TOKEN", publication)

    def test_homebrew_runs_as_a_direct_reusable_workflow_call(self) -> None:
        workflow = WORKFLOW.read_text()

        verification = workflow.index("Verify published CLI release")
        homebrew = workflow.index("uses: ./.github/workflows/publish-homebrew.yml")
        self.assertLess(verification, homebrew)
        self.assertIn("needs: publish", workflow)
        self.assertIn("tag: ${{ needs.publish.outputs.tag }}", workflow)


if __name__ == "__main__":
    unittest.main()
