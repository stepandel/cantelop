import io
import pathlib
import runpy
import tarfile
import tempfile
import unittest
from unittest.mock import patch


ROOT = pathlib.Path(__file__).resolve().parents[2]
HELPER = runpy.run_path(str(ROOT / "scripts/release-notices.py"))
COMMIT = "0123456789abcdef0123456789abcdef01234567"


class ReleaseNoticesTests(unittest.TestCase):
    def archive(self, path, prefix="", omit=None, changed=None):
        files = {name: source.read_bytes() for name, source in HELPER["notice_sources"]().items()}
        files["SOURCE.json"] = HELPER["source_bytes"](COMMIT)
        if omit:
            files.pop(omit)
        if changed:
            files[changed] = b"incorrect license or source identity\n"
        with tarfile.open(path, "w:gz") as package:
            for name, content in files.items():
                member = tarfile.TarInfo(f"{prefix}/{name}" if prefix else name)
                member.size = len(content)
                package.addfile(member, io.BytesIO(content))

    def test_cli_and_prefixed_host_archives_include_source_and_notices(self):
        with tempfile.TemporaryDirectory() as directory:
            for prefix in ("", "fire-fuse_test_linux-amd64"):
                path = pathlib.Path(directory) / "release.tar.gz"
                self.archive(path, prefix)
                HELPER["verify_archive"](path, COMMIT, prefix)

    def test_missing_or_altered_license_fails_even_with_valid_tar(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "release.tar.gz"
            for change in ({"omit": "LICENSE"}, {"changed": "licenses/go/GO-LICENSE"}):
                self.archive(path, **change)
                with self.assertRaises(ValueError):
                    HELPER["verify_archive"](path, COMMIT, "")

    def test_source_identity_must_match_the_selected_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "release.tar.gz"
            self.archive(path)
            with self.assertRaisesRegex(ValueError, "SOURCE.json"):
                HELPER["verify_archive"](path, "f" * 40, "")
        with self.assertRaisesRegex(ValueError, "full lowercase Git commit"):
            HELPER["source_bytes"]("main")

    def test_forks_can_identify_their_source_without_embedding_credentials(self):
        with patch.dict("os.environ", {"CANTELOP_SOURCE_REPOSITORY": "https://github.com/operator/fork.git"}):
            self.assertIn(f"https://github.com/operator/fork/tree/{COMMIT}".encode(), HELPER["source_bytes"](COMMIT))
        with patch.dict("os.environ", {"CANTELOP_SOURCE_REPOSITORY": "https://user:secret@github.com/operator/fork"}):
            with self.assertRaisesRegex(ValueError, "without credentials"):
                HELPER["source_bytes"](COMMIT)
        with patch.dict("os.environ", {"CANTELOP_SOURCE_REPOSITORY": "https://github.com/.git"}):
            with self.assertRaisesRegex(ValueError, "without credentials"):
                HELPER["source_bytes"](COMMIT)

    def test_archive_file_list_matches_prepared_notices(self):
        with tempfile.TemporaryDirectory() as directory:
            HELPER["prepare"](pathlib.Path(directory), COMMIT)
            prepared = sorted(str(path.relative_to(directory)) for path in pathlib.Path(directory).rglob("*") if path.is_file())
        self.assertEqual(prepared, sorted(HELPER["archive_files"]()))

    def test_inventory_ignores_go_directives_but_not_requirements(self):
        requirements = HELPER["go_requirements"]()
        self.assertTrue(requirements)
        self.assertTrue(all("@v" in requirement and not requirement.startswith(("go@", "toolchain@")) for requirement in requirements))

    def test_valid_notices_do_not_allow_archive_traversal_or_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            good = pathlib.Path(directory) / "good.tar.gz"
            bad = pathlib.Path(directory) / "bad.tar.gz"
            self.archive(good)
            for name, kind in (("../outside", tarfile.REGTYPE), ("redirect", tarfile.SYMTYPE)):
                with tarfile.open(good) as source, tarfile.open(bad, "w:gz") as output:
                    for member in source.getmembers():
                        output.addfile(member, source.extractfile(member))
                    extra = tarfile.TarInfo(name)
                    extra.type = kind
                    extra.linkname = "../outside" if kind == tarfile.SYMTYPE else ""
                    output.addfile(extra)
                with self.assertRaisesRegex(ValueError, "unsafe archive member"):
                    HELPER["verify_archive"](bad, COMMIT, "")


if __name__ == "__main__":
    unittest.main()
