import hashlib
import json
import pathlib
import tempfile
import unittest

from support import load_script


ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE = load_script("finalize-cli-release.py")


class FinalizeCLIReleaseTests(unittest.TestCase):
    version = "0.1.0-rc.12"
    commit = "0123456789abcdef0123456789abcdef01234567"
    identity = "Developer ID Application: Stepan Arsentjev (T2T986DDGJ)"
    fingerprint = "A" * 40
    amd64_cdhash = "1" * 40
    arm64_cdhash = "2" * 40

    def fixture(self, root: pathlib.Path) -> dict[str, object]:
        archives = root / "archives"
        archives.mkdir()
        for platform in MODULE.PLATFORMS:
            (archives / f"cantelop_{self.version}_{platform}.tar.gz").write_bytes(
                f"archive for {platform}".encode(),
            )
        submitted = root / "cantelop_darwin.zip"
        submitted.write_bytes(b"signed mac binaries")
        submitted_digest = hashlib.sha256(submitted.read_bytes()).hexdigest()
        log = root / "notarization-log.json"
        log.write_text(json.dumps({
            "jobId": "submission-id",
            "status": "Accepted",
            "statusSummary": "Ready for distribution",
            "statusCode": 0,
            "issues": None,
            "sha256": submitted_digest,
            "ticketContents": [
                {"arch": "x86_64", "cdhash": self.amd64_cdhash},
                {"arch": "arm64", "cdhash": self.arm64_cdhash},
            ],
        }))
        return {"archives": archives, "submitted": submitted, "log": log}

    def finalize(self, root: pathlib.Path, **overrides: str) -> dict[str, object]:
        fixture = self.fixture(root)
        arguments = {
            "version": self.version,
            "source_commit": self.commit,
            "source_tag": f"v{self.version}",
            "workflow_run_url": "https://github.com/stepandel/cantelop/actions/runs/123",
            "archives": fixture["archives"],
            "submitted_archive": fixture["submitted"],
            "notarization_log_path": fixture["log"],
            "signing_identity": self.identity,
            "signing_fingerprint": self.fingerprint,
            "team_id": "T2T986DDGJ",
            "darwin_amd64_cdhash": self.amd64_cdhash,
            "darwin_arm64_cdhash": self.arm64_cdhash,
        }
        arguments.update(overrides)
        return MODULE.finalize(**arguments)

    def test_writes_checksums_and_complete_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)

            evidence = self.finalize(root)
            checksum_lines = (root / "archives/SHA256SUMS").read_text().splitlines()

        self.assertEqual(evidence["schema_version"], 2)
        self.assertEqual(evidence["source_commit"], self.commit)
        self.assertEqual(evidence["notarization"]["submission_id"], "submission-id")
        self.assertEqual(evidence["notarization"]["tickets"]["darwin_arm64"], self.arm64_cdhash)
        self.assertEqual(len(checksum_lines), 4)
        self.assertEqual(set(evidence["artifacts"]), {
            f"cantelop_{self.version}_{platform}.tar.gz" for platform in MODULE.PLATFORMS
        })

    def test_rejects_mismatched_notarization_ticket(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(MODULE.FinalizationError, "Apple's tickets"):
                self.finalize(pathlib.Path(directory), darwin_arm64_cdhash="4" * 40)

    def test_rejects_mismatched_submitted_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            fixture = self.fixture(root)
            fixture["submitted"].write_bytes(b"changed after submission")
            with self.assertRaisesRegex(MODULE.FinalizationError, "submitted archive"):
                MODULE.finalize(
                    version=self.version,
                    source_commit=self.commit,
                    source_tag=f"v{self.version}",
                    workflow_run_url="https://github.com/stepandel/cantelop/actions/runs/123",
                    archives=fixture["archives"],
                    submitted_archive=fixture["submitted"],
                    notarization_log_path=fixture["log"],
                    signing_identity=self.identity,
                    signing_fingerprint=self.fingerprint,
                    team_id="T2T986DDGJ",
                    darwin_amd64_cdhash=self.amd64_cdhash,
                    darwin_arm64_cdhash=self.arm64_cdhash,
                )

    def test_rejects_incomplete_archive_set(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            fixture = self.fixture(root)
            next(fixture["archives"].glob("*linux_arm64*")).unlink()
            with self.assertRaisesRegex(MODULE.FinalizationError, "read"):
                MODULE.finalize(
                    version=self.version,
                    source_commit=self.commit,
                    source_tag=f"v{self.version}",
                    workflow_run_url="https://github.com/stepandel/cantelop/actions/runs/123",
                    archives=fixture["archives"],
                    submitted_archive=fixture["submitted"],
                    notarization_log_path=fixture["log"],
                    signing_identity=self.identity,
                    signing_fingerprint=self.fingerprint,
                    team_id="T2T986DDGJ",
                    darwin_amd64_cdhash=self.amd64_cdhash,
                    darwin_arm64_cdhash=self.arm64_cdhash,
                )

    def test_release_retry_allows_missing_assets_and_rejects_conflicts(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / "source"
            published = root / "published"
            source.mkdir()
            published.mkdir()
            for name in MODULE.release_asset_names(self.version):
                (source / name).write_text(f"qualified {name}")
            first = MODULE.release_asset_names(self.version)[0]
            (published / first).write_text(f"qualified {first}")

            MODULE.compare_release_assets(self.version, source, published, True)
            (published / first).write_text("different bytes")
            with self.assertRaisesRegex(MODULE.FinalizationError, "conflicting"):
                MODULE.compare_release_assets(self.version, source, published, True)

    def test_release_verification_rejects_missing_and_unexpected_assets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / "source"
            published = root / "published"
            source.mkdir()
            published.mkdir()
            for name in MODULE.release_asset_names(self.version):
                (source / name).write_text(f"qualified {name}")
                (published / name).write_text(f"qualified {name}")

            MODULE.compare_release_assets(self.version, source, published, False)
            (published / "surprise.txt").write_text("unexpected")
            with self.assertRaisesRegex(MODULE.FinalizationError, "unexpected"):
                MODULE.compare_release_assets(self.version, source, published, False)


if __name__ == "__main__":
    unittest.main()
