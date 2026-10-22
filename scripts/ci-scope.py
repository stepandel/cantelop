#!/usr/bin/env python3
"""Select PR checks from the tested merge tree; main/manual runs check everything."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess


STANDARD_LANES = ("go", "console", "edge", "policy", "terraform")
LANES = (*STANDARD_LANES, "juicefs")


def classify(paths: list[str], full: bool = False) -> dict[str, bool]:
    selected = set(LANES) if full else set()
    for path in paths:
        if path in ("LICENSE", "THIRD_PARTY_NOTICES.md", "CONTRIBUTING.md", "SECURITY.md", "docs/licensing.md", "docs/cli.md"):
            selected.add("policy")
            continue
        if path.startswith("docs/") or (path.endswith(".md") and not path.startswith(("console/", "licenses/"))):
            continue
        # Shared contracts, dependencies and tooling can affect every component.
        if path.startswith((
            ".github/", "scripts/", "schemas/", "internal/", "fire-fuse/", "clients/",
            "console/core/", "console/workers/", "edge/contracts/", "examples/",
        )) or path in ("Makefile", "go.mod", "go.sum", "console/package.json", "console/bun.lock"):
            selected.update(STANDARD_LANES)
        elif path.startswith("console/"):
            selected.add("console")
            if path.startswith(("console/web/app/v1/", "console/web/app/internal/", "console/web/app/api/cli/")):
                selected.update(STANDARD_LANES)
        elif path.startswith("edge/workers/"):
            selected.update(("edge", "policy"))
        elif path.startswith("platform/cloudflare/"):
            selected.update(("edge", "policy", "terraform"))
        elif path.startswith("platform/ansible/"):
            selected.add("policy")
        elif path.startswith(("builder/", "tools/", "tests/", "platform/", "storage/")):
            selected.update(("go", "policy"))
        elif path.startswith("licenses/"):
            selected.add("policy")
        else:
            # New or unclassified build inputs fail toward broader coverage.
            selected.update(STANDARD_LANES)

        # Only these inputs determine the independently pinned JuiceFS build.
        if path in ("storage/juicefs/prepare-juicefs-linux-amd64.sh", "storage/juicefs/workspace-data-only.patch", ".github/workflows/check.yml"):
            selected.add("juicefs")
    return {lane: lane in selected for lane in LANES}


def changed_paths(base: str) -> list[str]:
    if not re.fullmatch(r"[0-9a-f]{40}", base):
        raise ValueError("PR base must be a full Git commit SHA")
    # Checkout is the PR merge ref. Include both sides of a rename so moving a
    # source file into docs cannot hide its deletion from the original lane.
    data = subprocess.check_output(["git", "diff", "--no-renames", "--name-only", "-z", base, "HEAD", "--"])
    return [os.fsdecode(path) for path in data.split(b"\0") if path]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="")
    args = parser.parse_args()
    selected = classify(changed_paths(args.base) if args.base else [], full=not args.base)
    print(json.dumps(selected, sort_keys=True))
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        for lane, enabled in selected.items():
            output.write(f"{lane}={str(enabled).lower()}\n")


if __name__ == "__main__":
    main()
