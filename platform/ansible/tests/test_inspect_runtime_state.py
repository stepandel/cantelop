import json
import hashlib
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = (
    Path(__file__).parents[1]
    / "roles"
    / "compute" / "fire_fuse_promote"
    / "files"
    / "inspect-runtime-state.py"
)


class InspectRuntimeStateTest(unittest.TestCase):
    def run_inspector(self, path: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["python3", str(SCRIPT), str(path)],
            check=False,
            capture_output=True,
            text=True,
        )

    def test_reports_missing_database(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_inspector(Path(directory) / "state.db")
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertFalse(report["exists"])
        self.assertEqual(report["sha256"], "absent")
        self.assertEqual(sorted(report), ["exists", "sha256"])

    def test_reports_existing_file_without_opening_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.db"
            path.write_bytes(b"not a SQLite database")
            result = self.run_inspector(path)
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertTrue(report["exists"])
        self.assertEqual(report["sha256"], hashlib.sha256(b"not a SQLite database").hexdigest())
        self.assertEqual(sorted(report), ["exists", "sha256"])

    def test_rejects_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target.db"
            target.write_bytes(b"")
            link = root / "state.db"
            link.symlink_to(target)
            result = self.run_inspector(link)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not a symlink", result.stderr)


if __name__ == "__main__":
    unittest.main()
