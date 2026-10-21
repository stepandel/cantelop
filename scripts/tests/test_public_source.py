import json
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = runpy.run_path(str(ROOT / "scripts/public-source.py"))


class PublicSourceTests(unittest.TestCase):
    def git(self, root, *args):
        return subprocess.check_output(["git", *args], cwd=root, stderr=subprocess.DEVNULL).decode().strip()

    def fixture(self, root):
        self.git(root, "init", "-b", "main")
        self.git(root, "config", "user.name", "Fixture")
        self.git(root, "config", "user.email", "fixture@example.invalid")
        for name in (".public-source-policy.json", "console/public-source.json"):
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((ROOT / name).read_bytes() if name.startswith(".public") else HELPER["document"](HELPER["empty_pin"]()))
        for name in ("LICENSE", "CONTRIBUTING.md", "SECURITY.md", ".gitleaksignore", "main.go"):
            (root / name).write_text("fixture\n")
        history = root / "docs/history/private.json"
        history.parent.mkdir(parents=True)
        history.write_text("private operational record")
        (root / ".env").write_text("private local credential")
        self.git(root, "add", ".")

    def test_export_preserves_source_and_excludes_private_history(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "private"
            root.mkdir()
            self.fixture(root)
            public = Path(directory) / "public"
            HELPER["export"](public, root)
            self.assertFalse((public / ".git").exists())
            self.assertFalse((public / ".env").exists())
            self.assertFalse((public / "docs/history/private.json").exists())
            self.assertEqual((public / "main.go").read_bytes(), (root / "main.go").read_bytes())
            self.assertEqual((public / ".gitleaksignore").read_bytes(), HELPER["PUBLIC_IGNORE"])
            with self.assertRaisesRegex(ValueError, "must not exist"):
                HELPER["export"](public, root)

    def test_pin_detects_changed_added_and_missing_source_and_corrupt_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "private"
            root.mkdir()
            self.fixture(root)
            public = Path(directory) / "public"
            HELPER["export"](public, root)
            self.git(public, "init", "-b", "main")
            self.git(public, "config", "user.name", "Fixture")
            self.git(public, "config", "user.email", "fixture@example.invalid")
            self.git(public, "add", ".")
            self.git(public, "commit", "-m", "Public snapshot")
            commit = self.git(public, "rev-parse", "HEAD")
            HELPER["pin"](public, commit, root)
            self.assertEqual(HELPER["verify"](root)["commit"], commit)
            # Operational records can change without misrepresenting platform source.
            (root / "docs/history/private.json").write_text("new private record")
            HELPER["verify"](root)
            (root / "main.go").write_text("changed source")
            with self.assertRaisesRegex(ValueError, "source changed"):
                HELPER["verify"](root)
            (root / "main.go").write_text("fixture\n")
            (root / "new.go").write_text("new source")
            self.git(root, "add", "new.go")
            with self.assertRaisesRegex(ValueError, "source changed"):
                HELPER["verify"](root)
            self.git(root, "rm", "--cached", "new.go")
            (root / "new.go").unlink()
            (root / "SOURCE_SNAPSHOT.json").write_text("{}")
            with self.assertRaisesRegex(ValueError, "digest"):
                HELPER["verify"](root)

    def test_export_rejects_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            (root / "link.go").symlink_to("main.go")
            self.git(root, "add", "link.go")
            with self.assertRaisesRegex(ValueError, "regular source file"):
                HELPER["source_files"](root)


if __name__ == "__main__":
    unittest.main()
