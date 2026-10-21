from pathlib import Path
import unittest


SCRIPT = Path(__file__).parents[3] / "storage" / "minio" / "build-minio-linux-amd64.sh"


class BuildMinioReleaseIdentityTest(unittest.TestCase):
    def test_build_embeds_and_verifies_pinned_release_identity(self) -> None:
        script = SCRIPT.read_text(encoding="utf-8")
        release = "RELEASE.2025-10-15T17-29-55Z"
        self.assertIn(f"readonly minio_release='{release}'", script)
        self.assertIn("github.com/minio/minio/cmd.ReleaseTag=", script)
        self.assertIn("github.com/minio/minio/cmd.CommitID=", script)
        self.assertIn('-ldflags "${minio_ldflags}"', script)
        self.assertIn(
            '"minio version ${minio_release} "',
            script,
        )


if __name__ == "__main__":
    unittest.main()
