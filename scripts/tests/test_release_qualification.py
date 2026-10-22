import hashlib
import io
import json
import os
import pathlib
import platform
import re
import runpy
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch


ROOT = pathlib.Path(__file__).resolve().parents[2]


def release_notices(commit: str) -> dict[str, bytes]:
    helper = runpy.run_path(str(ROOT / "scripts/release-notices.py"))
    return {name: path.read_bytes() for name, path in helper["notice_sources"]().items()} | {
        "SOURCE.json": helper["source_bytes"](commit),
    }


class ReleaseQualificationTests(unittest.TestCase):
    def setUp(self) -> None:
        # Archive fixtures model an independent build, not the current hosted
        # publication. Subprocess validators inherit the same source identity.
        environment = patch.dict(os.environ, {"CANTELOP_SOURCE_REPOSITORY": "https://github.com/operator/release-fixture"})
        environment.start()
        self.addCleanup(environment.stop)

    def test_release_state_schema_matches_runtime(self) -> None:
        manager = (ROOT / "fire-fuse/runtime/persistence/sqliteschema/manager.go").read_text()
        match = re.search(r"const CurrentVersion = (\d+)", manager)
        self.assertIsNotNone(match)
        current_version = int(match.group(1))

        for path, field in (
            ("scripts/build-fire-fuse-release.sh", f'"state_schema_version": {current_version}'),
            ("scripts/check-fire-fuse-release.sh", f'"state_schema_version": {current_version}'),
            (
                "scripts/qualify-release-candidate.sh",
                f"fire_fuse_state_schema_version: {current_version}",
            ),
        ):
            self.assertIn(field, (ROOT / path).read_text())

        state_check = (ROOT / "scripts/check-fire-fuse-state-compatibility.sh").read_text()
        self.assertIn(f"schema-v{current_version}", state_check)
        self.assertIn("pre-cutoff schema rejection", state_check)

    def test_cli_sdk_and_deployment_identities_are_independent(self) -> None:
        qualification = (ROOT / "scripts/qualify-release-candidate.sh").read_text()
        starter = (ROOT / "scripts/check-starter.sh").read_text()

        self.assertNotIn("fire_fuse_version", qualification)
        self.assertIn('deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${cli_commit:0:12}}"', qualification)
        self.assertNotIn('"${cli_version}" != "${sdk_version}"', qualification)
        self.assertIn("starter dependency ${starter_sdk_version}", starter)
        self.assertNotIn("SDK ${sdk_version} and CLI", starter)

    def test_evidence_records_component_versions(self) -> None:
        qualification = (ROOT / "scripts/qualify-release-candidate.sh").read_text()

        self.assertIn("schema_version: 5", qualification)
        self.assertIn("sdk: sdkVersion", qualification)
        self.assertIn("cli: cliVersion", qualification)
        self.assertIn("revision: deploymentRevision", qualification)
        self.assertIn("source_commit: cliCommit", qualification)
        self.assertIn("cantelop-sdk-${sdkVersion}.tgz", qualification)
        self.assertIn("sdk-platform-contract", qualification)
        self.assertIn("message-lifecycle", qualification)
        self.assertIn("TestSDKMailbox", qualification)

    def test_check_logs_record_timing_and_status(self) -> None:
        qualification = (ROOT / "scripts/qualify-release-candidate.sh").read_text()

        self.assertIn("started_at=", qualification)
        self.assertIn("completed_at=", qualification)
        self.assertIn("duration_seconds=", qualification)
        self.assertIn('status="${PIPESTATUS[0]}"', qualification)
        self.assertIn('return "${status}"', qualification)

    def test_qualification_builds_cli_and_fire_fuse_once(self) -> None:
        qualification = (ROOT / "scripts/qualify-release-candidate.sh").read_text()

        self.assertEqual(qualification.count("scripts/build-cli-release.sh"), 1)
        self.assertEqual(qualification.count("scripts/build-fire-fuse-release.sh"), 1)
        self.assertLess(qualification.index("run_check cli-build"), qualification.index("run_check cli-release"))
        self.assertLess(
            qualification.index("run_check fire-fuse-build"),
            qualification.index("run_check fire-fuse-release"),
        )
        self.assertIn('--validate "${output_directory}/artifacts/cli"', qualification)
        self.assertIn('--validate "${output_directory}/artifacts/fire-fuse"', qualification)

    def test_release_checks_can_validate_prebuilt_artifacts(self) -> None:
        cli_checker = (ROOT / "scripts/check-cli-release.sh").read_text()
        fire_fuse_checker = (ROOT / "scripts/check-fire-fuse-release.sh").read_text()

        for checker in (cli_checker, fire_fuse_checker):
            self.assertIn('elif [[ "$#" == 2 && "$1" == "--validate" ]]', checker)
            self.assertIn('release_directory="$2"', checker)

    def test_cli_release_check_validates_existing_archives(self) -> None:
        commit = "0123456789abcdef0123456789abcdef01234567"
        version = (ROOT / "clients/cli/VERSION").read_text().strip()
        native_os = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        native_arch = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64"}[platform.machine()]

        with tempfile.TemporaryDirectory() as directory:
            release = pathlib.Path(directory)
            checksums = []
            for operating_system in ("darwin", "linux"):
                for architecture in ("amd64", "arm64"):
                    archive = release / f"cantelop_{version}_{operating_system}_{architecture}.tar.gz"
                    with tarfile.open(archive, "w:gz") as package:
                        program = f"#!/bin/sh\nprintf 'cantelop {version} (0123456789ab)\\n'\n".encode()
                        info = tarfile.TarInfo("cantelop")
                        info.mode = 0o755
                        info.size = len(program)
                        package.addfile(info, io.BytesIO(program))
                        readme = b"CLI release fixture\n"
                        info = tarfile.TarInfo("README.md")
                        info.mode = 0o644
                        info.size = len(readme)
                        package.addfile(info, io.BytesIO(readme))
                        for name, content in release_notices(commit).items():
                            info = tarfile.TarInfo(name)
                            info.mode = 0o644
                            info.size = len(content)
                            package.addfile(info, io.BytesIO(content))
                    checksums.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n")
            (release / "SHA256SUMS").write_text("".join(checksums))

            result = subprocess.run(
                [ROOT / "scripts/check-cli-release.sh", "--validate", release],
                check=False,
                capture_output=True,
                text=True,
                env=os.environ | {"CANTELOP_BUILD_COMMIT": commit},
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f"cantelop {version} (0123456789ab)", result.stdout)
        self.assertIn(f"cantelop_{version}_{native_os}_{native_arch}.tar.gz: OK", result.stdout)

    def test_fire_fuse_release_check_validates_existing_archive(self) -> None:
        commit = "0123456789abcdef0123456789abcdef01234567"
        revision = "test-revision"
        cli_version = (ROOT / "clients/cli/VERSION").read_text().strip()
        bundle_name = f"fire-fuse_{revision}_linux-amd64"
        artifact_names = [
            "cantelop", "fire-fuse", "fire-fuse-async-dispatcher", "fire-fuse-gateway", "fire-fuse-image",
            "fire-fuse-integration", "fire-fuse-managed-watchdog", "fire-fuse-observer", "fire-fuse-snapshot",
        ]

        with tempfile.TemporaryDirectory() as directory:
            release = pathlib.Path(directory)
            bundle = release / bundle_name
            binaries = bundle / "bin"
            binaries.mkdir(parents=True)
            for name in artifact_names:
                identity = f"{name}\n{revision}\n{commit}\n{commit[:12]}\n{cli_version}\n"
                (binaries / name).write_text(identity)
            manifest = {
                "schema_version": 2,
                "deployment_revision": revision,
                "source_commit": commit,
                "platform": {"os": "linux", "architecture": "amd64"},
                "state_schema_version": 28,
                "artifacts": artifact_names,
            }
            (bundle / "manifest.json").write_text(json.dumps(manifest))
            for name, content in release_notices(commit).items():
                destination = bundle / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(content)
            nested = [
                f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.relative_to(bundle)}\n"
                for path in sorted(bundle.rglob("*")) if path.is_file()
            ]
            (bundle / "SHA256SUMS").write_text("".join(nested))
            archive = release / f"{bundle_name}.tar.gz"
            with tarfile.open(archive, "w:gz") as package:
                package.add(bundle, arcname=bundle.name)
            (release / "SHA256SUMS").write_text(
                f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n",
            )
            environment = os.environ | {
                "CANTELOP_BUILD_COMMIT": commit,
                "CANTELOP_DEPLOYMENT_REVISION": revision,
            }
            result = subprocess.run(
                [ROOT / "scripts/check-fire-fuse-release.sh", "--validate", release],
                check=False,
                capture_output=True,
                text=True,
                env=environment,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f"Qualified Fire Fuse deployment {revision} ({commit[:12]}) bundle", result.stdout)

    def test_fire_fuse_bundle_separates_deployment_and_cli_identity(self) -> None:
        builder = (ROOT / "scripts/build-compute-linux-amd64.sh").read_text()
        checker = (ROOT / "scripts/check-fire-fuse-release.sh").read_text()

        self.assertIn("releaseinfo.revision=${deployment_revision}", builder)
        self.assertIn("-X main.version=${cli_version}", builder)
        self.assertIn("-X main.commit=${release_commit:0:12}", builder)
        self.assertIn('deployment_revision="${CANTELOP_DEPLOYMENT_REVISION:-${commit:0:12}}"', checker)
        self.assertIn('bin/cantelop\" | grep -Fx \"${cli_version}\"', checker)
        self.assertIn('grep -Fx \"${commit:0:12}\"', checker)

    def test_fire_fuse_has_no_semantic_version_file(self) -> None:
        self.assertFalse((ROOT / "fire-fuse/VERSION").exists())


if __name__ == "__main__":
    unittest.main()
