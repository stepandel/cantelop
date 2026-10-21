import os
import sys
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from support import load_module


SCRIPT = (
    Path(__file__).parents[1]
    / "roles"
    / "compute" / "fire_fuse_promote"
    / "files"
    / "coordinate-image-catalog.py"
)
COORDINATOR = load_module(SCRIPT, "coordinate_image_catalog")


class CatalogDeploymentLeaseTest(unittest.TestCase):
    def test_end_reads_lease_token_from_environment(self) -> None:
        with (
            mock.patch.object(sys, "argv", ["coordinate-image-catalog.py", "end", "--catalog", "/tmp/image-catalog.json"]),
            mock.patch.dict(os.environ, {"CANTELOP_CATALOG_LEASE_TOKEN": "a" * 64}),
        ):
            self.assertEqual(COORDINATOR.parse_args().token, "a" * 64)
        with (
            mock.patch.object(sys, "argv", ["coordinate-image-catalog.py", "end", "--catalog", "/tmp/image-catalog.json"]),
            mock.patch.dict(os.environ, {"CANTELOP_CATALOG_LEASE_TOKEN": ""}),
            self.assertRaises(SystemExit),
        ):
            COORDINATOR.parse_args()

    def test_begin_and_token_protected_end(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            catalog = Path(directory) / "image-catalog.json"
            token = "a" * 64

            def open_lock(_path: Path) -> int:
                return os.open(os.devnull, os.O_RDONLY)

            with (
                mock.patch.object(COORDINATOR, "open_lock", open_lock),
                mock.patch.object(
                    COORDINATOR,
                    "catalog_identity",
                    return_value="absent",
                ),
                mock.patch.object(COORDINATOR.secrets, "token_hex", return_value=token),
                mock.patch.object(COORDINATOR.os, "fchown"),
                mock.patch.object(COORDINATOR, "sync_directory"),
            ):
                result = COORDINATOR.begin(catalog, "absent")

            lease = Path(str(catalog) + ".lock.deployment")
            self.assertEqual(
                result,
                {"catalog_sha256": "absent", "lease_token": token},
            )
            self.assertEqual(lease.read_text(encoding="ascii"), token + "\n")

            with (
                mock.patch.object(COORDINATOR, "open_lock", open_lock),
                mock.patch.object(COORDINATOR, "trusted_regular"),
                mock.patch.object(COORDINATOR, "sync_directory"),
            ):
                with self.assertRaisesRegex(ValueError, "token does not match"):
                    COORDINATOR.end(catalog, "b" * 64)
                self.assertTrue(lease.exists())
                self.assertEqual(COORDINATOR.end(catalog, token), {"released": True})
            self.assertFalse(lease.exists())

    def test_begin_rejects_catalog_changed_after_staging(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            catalog = Path(directory) / "image-catalog.json"

            def open_lock(_path: Path) -> int:
                return os.open(os.devnull, os.O_RDONLY)

            with (
                mock.patch.object(COORDINATOR, "open_lock", open_lock),
                mock.patch.object(
                    COORDINATOR,
                    "catalog_identity",
                    return_value="b" * 64,
                ),
            ):
                with self.assertRaisesRegex(ValueError, "restage the release"):
                    COORDINATOR.begin(catalog, "a" * 64)

            self.assertFalse(
                Path(str(catalog) + ".lock.deployment").exists()
            )


if __name__ == "__main__":
    unittest.main()
