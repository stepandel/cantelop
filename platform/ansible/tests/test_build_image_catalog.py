import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


BUILDER = (
    Path(__file__).parents[1]
    / "roles/compute/fire_fuse_stage/files/build-image-catalog.py"
)


class BuildImageCatalogTest(unittest.TestCase):
    def test_uses_verified_remote_rootfs_metadata_without_local_rootfs(self):
        with tempfile.TemporaryDirectory() as directory:
            process = Path(directory) / "process.json"
            process.write_text(json.dumps({
                "args": ["/app"], "env": [], "working_directory": "/",
                "uid": 1000, "gid": 1000,
            }), encoding="utf-8")
            digest = "a" * 64
            completed = subprocess.run([
                sys.executable, str(BUILDER),
                "--rootfs-size-bytes", "1073741824",
                "--rootfs-sha256", digest,
                "--process", str(process),
                "--installed-rootfs", f"/var/lib/fire-fuse/images/sha256-{digest}.ext4",
            ], capture_output=True, text=True, check=True)
            result = json.loads(completed.stdout)
            self.assertEqual(result["image"]["rootfs_sha256"], digest)
            self.assertEqual(result["image"]["rootfs_size_bytes"], 1073741824)
            self.assertRegex(result["build_id"], r"^sha256:[0-9a-f]{64}$")

    def test_rejects_empty_rootfs_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            process = Path(directory) / "process.json"
            process.write_text("{}", encoding="utf-8")
            completed = subprocess.run([
                sys.executable, str(BUILDER),
                "--rootfs-size-bytes", "0",
                "--rootfs-sha256", "a" * 64,
                "--process", str(process),
                "--installed-rootfs", "/images/test.ext4",
            ], capture_output=True, text=True)
            self.assertNotEqual(completed.returncode, 0)
            self.assertIn("must be positive", completed.stderr)


if __name__ == "__main__":
    unittest.main()
