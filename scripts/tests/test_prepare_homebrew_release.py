import hashlib
import io
import json
import pathlib
import platform
import shutil
import stat
import subprocess
import tarfile
import tempfile
import unittest

from support import load_script


ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE = load_script("prepare-homebrew-release.py")


class PrepareHomebrewReleaseTests(unittest.TestCase):
    version = "0.1.0-rc.11"
    tag = f"v{version}"
    commit = "0123456789abcdef0123456789abcdef01234567"
    amd64_cdhash = "1" * 40
    arm64_cdhash = "2" * 40
    notarization_sha256 = "3" * 64

    def create_release(self, root: pathlib.Path, extra: dict[str, bytes] | None = None) -> dict[str, str]:
        checksums = {}
        for name in MODULE.archive_names(self.version):
            path = root / name
            with tarfile.open(path, "w:gz") as archive:
                binary = b"qualified CLI"
                info = tarfile.TarInfo("cantelop")
                info.mode = 0o755
                info.size = len(binary)
                archive.addfile(info, io.BytesIO(binary))
                readme = b"Cantelop CLI\n"
                info = tarfile.TarInfo("README.md")
                info.mode = 0o644
                info.size = len(readme)
                archive.addfile(info, io.BytesIO(readme))
                for extra_name, content in (extra or {}).items():
                    info = tarfile.TarInfo(extra_name)
                    info.mode = 0o644
                    info.size = len(content)
                    archive.addfile(info, io.BytesIO(content))
            checksums[name] = hashlib.sha256(path.read_bytes()).hexdigest()
        (root / "SHA256SUMS").write_text(
            "".join(f"{checksum}  {name}\n" for name, checksum in checksums.items()),
        )
        (root / "release-evidence.json").write_text(json.dumps({
            "version": self.version,
            "source_commit": self.commit,
            "artifacts": checksums,
            "notarization": {
                "status": "Accepted",
                "status_code": 0,
                "issues": None,
                "submitted_archive_sha256": self.notarization_sha256,
                "tickets": {
                    "darwin_amd64": self.amd64_cdhash,
                    "darwin_arm64": self.arm64_cdhash,
                },
            },
        }))
        (root / "notarization-log.json").write_text(json.dumps({
            "status": "Accepted",
            "statusSummary": "Ready for distribution",
            "statusCode": 0,
            "issues": None,
            "sha256": self.notarization_sha256,
            "ticketContents": [
                {"arch": "x86_64", "cdhash": self.amd64_cdhash},
                {"arch": "arm64", "cdhash": self.arm64_cdhash},
            ],
        }))
        return checksums

    def create_native_installer(
        self,
        root: pathlib.Path,
    ) -> tuple[pathlib.Path, pathlib.Path, dict[str, str]]:
        system = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
        machine = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(
            platform.machine(),
        )
        if system is None or machine is None:
            self.skipTest("installer only supports release platforms")

        checksums = self.create_release(root)
        native_name = f"cantelop_{self.version}_{system}_{machine}.tar.gz"
        native_archive = root / native_name
        binary = f"#!/bin/sh\necho 'cantelop {self.version} ({self.commit[:12]})'\n".encode()
        with tarfile.open(native_archive, "w:gz") as archive:
            info = tarfile.TarInfo("cantelop")
            info.mode = 0o755
            info.size = len(binary)
            archive.addfile(info, io.BytesIO(binary))
        checksums[native_name] = hashlib.sha256(native_archive.read_bytes()).hexdigest()
        (root / "SHA256SUMS").write_text(
            "".join(f"{checksum}  {name}\n" for name, checksum in checksums.items()),
        )

        installer = root / "install.sh"
        installer.write_text(MODULE.render_installer(self.tag, root / "SHA256SUMS", "example/tap"))
        fake_bin = root / "bin"
        fake_bin.mkdir()
        fake_curl = fake_bin / "curl"
        fake_curl.write_text(
            "#!/bin/sh\n"
            "while [ \"$#\" -gt 0 ]; do\n"
            "  case \"$1\" in --output) output=$2; shift 2 ;; *) shift ;; esac\n"
            "done\n"
            "cp \"$FAKE_ARCHIVE\" \"$output\"\n",
        )
        fake_curl.chmod(fake_curl.stat().st_mode | stat.S_IXUSR)
        for command in ("cp", "gzip", "install", "mkdir", "mktemp", "mv", "rm", "tar", "uname"):
            command_path = shutil.which(command)
            if command_path is None:
                self.skipTest(f"installer test requires {command}")
            (fake_bin / command).symlink_to(command_path)
        checksum_command = "sha256sum" if shutil.which("sha256sum") else "shasum"
        checksum_path = shutil.which(checksum_command)
        if checksum_path is None:
            self.skipTest("installer test requires sha256sum or shasum")
        (fake_bin / checksum_command).symlink_to(checksum_path)

        home = root / "home"
        temporary = root / "tmp"
        home.mkdir()
        temporary.mkdir()
        environment = {
            "FAKE_ARCHIVE": str(native_archive),
            "HOME": str(home),
            "PATH": str(fake_bin),
            "SHELL": "/bin/zsh",
            "TMPDIR": str(temporary),
        }
        return installer, fake_bin, environment

    def test_validates_qualified_release(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            checksums = self.create_release(root)

            result = MODULE.validate_release(self.tag, root)

        self.assertEqual(result["version"], self.version)
        self.assertEqual(result["source_commit"], self.commit)
        self.assertEqual(result["checksums"], checksums)

    def test_accepts_release_notices_but_not_unsafe_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            self.create_release(root, {"LICENSE": b"license\n", "licenses/go/GO-LICENSE": b"go\n"})
            MODULE.validate_release(self.tag, root)
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            self.create_release(root, {"../LICENSE": b"license\n"})
            with self.assertRaisesRegex(MODULE.ReleaseError, "unsafe path"):
                MODULE.validate_release(self.tag, root)

    def test_rejects_changed_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            self.create_release(root)
            archive = root / MODULE.archive_names(self.version)[0]
            archive.write_bytes(archive.read_bytes() + b"changed")

            with self.assertRaisesRegex(MODULE.ReleaseError, "checksum mismatch"):
                MODULE.validate_release(self.tag, root)

    def test_rejects_unaccepted_notarization(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            self.create_release(root)
            log = json.loads((root / "notarization-log.json").read_text())
            log["status"] = "Invalid"
            (root / "notarization-log.json").write_text(json.dumps(log))

            with self.assertRaisesRegex(MODULE.ReleaseError, "not accepted"):
                MODULE.validate_release(self.tag, root)

    def test_renders_formula_from_exact_checksums(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            checksums = self.create_release(root)

            formula = MODULE.render_formula(
                self.tag,
                root / "SHA256SUMS",
                "stepandel/homebrew-tap",
            )

        self.assertIn(f'version "{self.version}"', formula)
        for name, checksum in checksums.items():
            self.assertIn(
                f"https://github.com/stepandel/homebrew-tap/releases/download/"
                f"cantelop-v{self.version}/{name}",
                formula,
            )
            self.assertIn(f'sha256 "{checksum}"', formula)

    def test_renders_installer_from_exact_checksums(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            checksums = self.create_release(root)

            installer = MODULE.render_installer(
                self.tag,
                root / "SHA256SUMS",
                "stepandel/homebrew-tap",
            )

        self.assertTrue(installer.startswith("#!/bin/sh\nset -eu\n"))
        self.assertIn(f"version={self.version}", installer)
        for name, checksum in checksums.items():
            platform_name = name.removeprefix(f"cantelop_{self.version}_").removesuffix(".tar.gz")
            self.assertIn(f"{platform_name}) expected_sha256={checksum} ;;", installer)
        self.assertIn(
            f"https://github.com/stepandel/homebrew-tap/releases/download/cantelop-v{self.version}",
            installer,
        )
        self.assertIn("CANTELOP_NO_MODIFY_PATH", installer)
        self.assertIn("cantelop is already installed at", installer)

    def test_installer_downloads_verifies_and_installs_native_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            installer, _, environment = self.create_native_installer(root)
            destination = root / "installed"
            environment.update({
                "CANTELOP_INSTALL_DIR": str(destination),
                "CANTELOP_NO_MODIFY_PATH": "1",
            })

            result = subprocess.run(
                ["/bin/sh", str(installer)],
                check=True,
                capture_output=True,
                text=True,
                env=environment,
            )

            installed = destination / "cantelop"
            self.assertTrue(installed.exists())
            self.assertTrue(installed.stat().st_mode & stat.S_IXUSR)
            self.assertIn(f"Installed cantelop {self.version}", result.stdout)

    def test_installer_configures_zsh_path_idempotently(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            installer, _, environment = self.create_native_installer(root)

            first = subprocess.run(
                ["/bin/sh", str(installer)],
                check=True,
                capture_output=True,
                text=True,
                env=environment,
            )
            second = subprocess.run(
                ["/bin/sh", str(installer)],
                check=True,
                capture_output=True,
                text=True,
                env=environment,
            )

            profile = pathlib.Path(environment["HOME"]) / ".zshrc"
            path_line = 'export PATH="$HOME/.local/bin:$PATH" # added by cantelop installer'
            self.assertEqual(profile.read_text().splitlines().count(path_line), 1)
            self.assertIn("Added", first.stdout)
            self.assertIn("already configured", second.stdout)

    def test_installer_configures_bash_and_fish_profiles(self) -> None:
        bash_profile = ".bash_profile" if platform.system() == "Darwin" else ".bashrc"
        cases = (
            ("/bin/bash", bash_profile, 'export PATH="$HOME/.local/bin:$PATH"'),
            ("/usr/bin/fish", ".config/fish/conf.d/cantelop.fish", 'set -gx PATH "$HOME/.local/bin" $PATH'),
        )
        for shell, relative_profile, expected in cases:
            with self.subTest(shell=shell), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                installer, _, environment = self.create_native_installer(root)
                environment["SHELL"] = shell

                subprocess.run(
                    ["/bin/sh", str(installer)],
                    check=True,
                    capture_output=True,
                    text=True,
                    env=environment,
                )

                profile = pathlib.Path(environment["HOME"]) / relative_profile
                self.assertIn(expected, profile.read_text())

    def test_installer_path_update_can_be_disabled(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            installer, _, environment = self.create_native_installer(root)
            environment["CANTELOP_NO_MODIFY_PATH"] = "1"

            result = subprocess.run(
                ["/bin/sh", str(installer)],
                check=True,
                capture_output=True,
                text=True,
                env=environment,
            )

            self.assertFalse((pathlib.Path(environment["HOME"]) / ".zshrc").exists())
            self.assertIn("profile update skipped", result.stdout)

    def test_installer_refuses_to_shadow_an_existing_cli(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            installer, fake_bin, environment = self.create_native_installer(root)
            existing = fake_bin / "cantelop"
            existing.write_text("#!/bin/sh\nexit 0\n")
            existing.chmod(existing.stat().st_mode | stat.S_IXUSR)

            result = subprocess.run(
                ["/bin/sh", str(installer)],
                check=False,
                capture_output=True,
                text=True,
                env=environment,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn(f"cantelop is already installed at {existing}", result.stderr)
            self.assertFalse((pathlib.Path(environment["HOME"]) / ".local/bin/cantelop").exists())

    def test_mirror_comparison_allows_only_missing_assets(self) -> None:
        with tempfile.TemporaryDirectory() as source_directory, tempfile.TemporaryDirectory() as mirror_directory:
            source = pathlib.Path(source_directory)
            mirror = pathlib.Path(mirror_directory)
            self.create_release(source)
            first = MODULE.mirrored_names(self.version)[0]
            (mirror / first).write_bytes((source / first).read_bytes())

            MODULE.compare_mirror(self.tag, source, mirror, allow_missing=True)
            with self.assertRaisesRegex(MODULE.ReleaseError, "missing"):
                MODULE.compare_mirror(self.tag, source, mirror, allow_missing=False)
            (mirror / first).write_bytes(b"different")
            with self.assertRaisesRegex(MODULE.ReleaseError, "conflicting"):
                MODULE.compare_mirror(self.tag, source, mirror, allow_missing=True)

    def test_public_mirror_includes_versioned_installer(self) -> None:
        self.assertIn("install.sh", MODULE.mirrored_names(self.version))

    def test_rejects_noncanonical_release_tag(self) -> None:
        for tag in ("0.1.0", "vnext", "v01.0.0", "v0.1.0-rc.01", "v0.1.0/../../main"):
            with self.subTest(tag=tag), self.assertRaises(MODULE.ReleaseError):
                MODULE.release_version(tag)

    def test_semantic_versions_order_numeric_release_candidates(self) -> None:
        self.assertGreater(MODULE.compare_versions("0.1.0-rc.11", "0.1.0-rc.9"), 0)
        self.assertGreater(MODULE.compare_versions("0.1.0", "0.1.0-rc.11"), 0)
        self.assertLess(MODULE.compare_versions("0.1.0-alpha.2", "0.1.0-alpha.beta"), 0)
        self.assertEqual(MODULE.compare_versions("1.2.3", "1.2.3"), 0)

    def test_refuses_to_downgrade_formula(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            formula = pathlib.Path(directory) / "cantelop.rb"
            formula.write_text('class Cantelop < Formula\n  version "0.1.0-rc.11"\nend\n')

            MODULE.require_no_downgrade("v0.1.0-rc.12", formula)
            with self.assertRaisesRegex(MODULE.ReleaseError, "refusing to downgrade"):
                MODULE.require_no_downgrade("v0.1.0-rc.9", formula)

    def test_appends_github_outputs(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "output"
            output.write_text("existing=value\n")

            MODULE.append_output(output, "version=0.1.0\n")

            self.assertEqual(output.read_text(), "existing=value\nversion=0.1.0\n")


if __name__ == "__main__":
    unittest.main()
