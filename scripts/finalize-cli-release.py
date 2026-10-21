#!/usr/bin/env python3
"""Finalize checksums and evidence for a signed Cantelop CLI release."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import sys


VERSION_PATTERN = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
DIGEST_PATTERN = re.compile(r"^[0-9a-f]{64}$")
CDHASH_PATTERN = re.compile(r"^[0-9a-f]{40}$")
FINGERPRINT_PATTERN = re.compile(r"^[0-9A-F]{40}$")
TEAM_PATTERN = re.compile(r"^[A-Z0-9]{10}$")
PLATFORMS = (
    "darwin_amd64",
    "darwin_arm64",
    "linux_amd64",
    "linux_arm64",
)
SUPPORTING_ASSETS = (
    "SHA256SUMS",
    "release-evidence.json",
    "notarization-log.json",
)


class FinalizationError(ValueError):
    pass


def file_digest(path: pathlib.Path) -> str:
    hasher = hashlib.sha256()
    try:
        with path.open("rb") as artifact:
            while chunk := artifact.read(1024 * 1024):
                hasher.update(chunk)
    except OSError as error:
        raise FinalizationError(f"read {path}: {error}") from error
    return hasher.hexdigest()


def release_asset_names(version: str) -> tuple[str, ...]:
    return (
        *(f"cantelop_{version}_{platform}.tar.gz" for platform in PLATFORMS),
        *SUPPORTING_ASSETS,
    )


def compare_release_assets(
    version: str,
    source: pathlib.Path,
    published: pathlib.Path,
    allow_missing: bool,
) -> None:
    expected = set(release_asset_names(version))
    try:
        actual = {path.name for path in published.iterdir() if path.is_file()}
    except OSError as error:
        raise FinalizationError(f"read published release assets: {error}") from error
    unexpected = actual - expected
    if unexpected:
        raise FinalizationError(
            f"published release contains unexpected assets: {', '.join(sorted(unexpected))}",
        )
    missing = expected - actual
    if missing and not allow_missing:
        raise FinalizationError(
            f"published release is missing assets: {', '.join(sorted(missing))}",
        )
    for name in actual:
        if file_digest(source / name) != file_digest(published / name):
            raise FinalizationError(f"published release has conflicting {name}")


def read_notarization_log(path: pathlib.Path) -> dict[str, object]:
    try:
        document = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise FinalizationError(f"read notarization log: {error}") from error
    if not isinstance(document, dict):
        raise FinalizationError("notarization log must contain a JSON object")
    if (
        document.get("status") != "Accepted"
        or document.get("statusSummary") != "Ready for distribution"
        or document.get("statusCode") != 0
        or document.get("issues") is not None
    ):
        raise FinalizationError("notarization was not accepted without issues")
    return document


def notarization_tickets(document: dict[str, object]) -> dict[str, str]:
    contents = document.get("ticketContents")
    if not isinstance(contents, list):
        raise FinalizationError("notarization log has no ticket contents")
    tickets = {
        item.get("arch"): item.get("cdhash")
        for item in contents
        if isinstance(item, dict)
    }
    if set(tickets) != {"x86_64", "arm64"} or not all(
        isinstance(value, str) and CDHASH_PATTERN.fullmatch(value)
        for value in tickets.values()
    ):
        raise FinalizationError("notarization log must cover x86_64 and arm64 exactly")
    return {
        "darwin_amd64": tickets["x86_64"],
        "darwin_arm64": tickets["arm64"],
    }


def finalize(
    *,
    version: str,
    source_commit: str,
    source_tag: str,
    workflow_run_url: str,
    archives: pathlib.Path,
    submitted_archive: pathlib.Path,
    notarization_log_path: pathlib.Path,
    signing_identity: str,
    signing_fingerprint: str,
    team_id: str,
    darwin_amd64_cdhash: str,
    darwin_arm64_cdhash: str,
) -> dict[str, object]:
    if VERSION_PATTERN.fullmatch(version) is None or source_tag != f"v{version}":
        raise FinalizationError("release version and tag do not match")
    if COMMIT_PATTERN.fullmatch(source_commit) is None:
        raise FinalizationError("source commit must be a full lowercase Git commit")
    if not workflow_run_url.startswith("https://github.com/"):
        raise FinalizationError("workflow run URL must be a GitHub URL")
    if FINGERPRINT_PATTERN.fullmatch(signing_fingerprint) is None:
        raise FinalizationError("signing fingerprint must be 40 uppercase hexadecimal characters")
    if TEAM_PATTERN.fullmatch(team_id) is None or team_id not in signing_identity:
        raise FinalizationError("signing identity does not contain the Apple Team ID")
    supplied_cdhashes = {
        "darwin_amd64": darwin_amd64_cdhash,
        "darwin_arm64": darwin_arm64_cdhash,
    }
    if not all(CDHASH_PATTERN.fullmatch(value) for value in supplied_cdhashes.values()):
        raise FinalizationError("signed binary CDHashes are invalid")

    notarization = read_notarization_log(notarization_log_path)
    tickets = notarization_tickets(notarization)
    if tickets != supplied_cdhashes:
        raise FinalizationError("signed binary CDHashes do not match Apple's tickets")
    submitted_digest = file_digest(submitted_archive)
    if notarization.get("sha256") != submitted_digest:
        raise FinalizationError("submitted archive does not match Apple's notarization log")
    submission_id = notarization.get("jobId")
    if not isinstance(submission_id, str) or not submission_id:
        raise FinalizationError("notarization log has no submission ID")

    artifact_checksums: dict[str, str] = {}
    for platform in PLATFORMS:
        name = f"cantelop_{version}_{platform}.tar.gz"
        artifact_checksums[name] = file_digest(archives / name)
    checksum_document = "".join(
        f"{digest}  {name}\n" for name, digest in artifact_checksums.items()
    )
    (archives / "SHA256SUMS").write_text(checksum_document)

    return {
        "schema_version": 2,
        "version": version,
        "source_qualification": f"github-actions:{workflow_run_url}",
        "source_commit": source_commit,
        "release_operation": {
            "operator": "github-actions",
            "authorization": f"signed release tag {source_tag}",
            "workflow_run": workflow_run_url,
            "rollback_owner": "stepandel",
        },
        "signing": {
            "identity": signing_identity,
            "fingerprint": signing_fingerprint,
            "team_id": team_id,
            "identifier": "dev.cantelop.cli",
            "hardened_runtime": True,
            "secure_timestamp": True,
        },
        "notarization": {
            "submission_id": submission_id,
            "status": "Accepted",
            "status_summary": "Ready for distribution",
            "status_code": 0,
            "issues": None,
            "submitted_archive_sha256": submitted_digest,
            "tickets": tickets,
            "log": "notarization-log.json",
        },
        "artifacts": artifact_checksums,
        "checksums": "SHA256SUMS",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    finalize_parser = subparsers.add_parser("finalize")
    finalize_parser.add_argument("--version", required=True)
    finalize_parser.add_argument("--source-commit", required=True)
    finalize_parser.add_argument("--source-tag", required=True)
    finalize_parser.add_argument("--workflow-run-url", required=True)
    finalize_parser.add_argument("--archives", required=True, type=pathlib.Path)
    finalize_parser.add_argument("--submitted-archive", required=True, type=pathlib.Path)
    finalize_parser.add_argument("--notarization-log", required=True, type=pathlib.Path)
    finalize_parser.add_argument("--signing-identity", required=True)
    finalize_parser.add_argument("--signing-fingerprint", required=True)
    finalize_parser.add_argument("--team-id", required=True)
    finalize_parser.add_argument("--darwin-amd64-cdhash", required=True)
    finalize_parser.add_argument("--darwin-arm64-cdhash", required=True)
    finalize_parser.add_argument("--output", required=True, type=pathlib.Path)

    compare_parser = subparsers.add_parser("compare-assets")
    compare_parser.add_argument("--version", required=True)
    compare_parser.add_argument("--source", required=True, type=pathlib.Path)
    compare_parser.add_argument("--published", required=True, type=pathlib.Path)
    compare_parser.add_argument("--allow-missing", action="store_true")
    args = parser.parse_args()
    try:
        if args.command == "finalize":
            evidence = finalize(
                version=args.version,
                source_commit=args.source_commit,
                source_tag=args.source_tag,
                workflow_run_url=args.workflow_run_url,
                archives=args.archives,
                submitted_archive=args.submitted_archive,
                notarization_log_path=args.notarization_log,
                signing_identity=args.signing_identity,
                signing_fingerprint=args.signing_fingerprint,
                team_id=args.team_id,
                darwin_amd64_cdhash=args.darwin_amd64_cdhash,
                darwin_arm64_cdhash=args.darwin_arm64_cdhash,
            )
            args.output.write_text(json.dumps(evidence, indent=2) + "\n")
        else:
            compare_release_assets(
                args.version,
                args.source,
                args.published,
                args.allow_missing,
            )
    except (FinalizationError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
