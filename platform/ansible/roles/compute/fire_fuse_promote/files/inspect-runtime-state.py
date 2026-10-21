#!/usr/bin/env python3
"""Report whether fire-fuse lifecycle state exists without opening it."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import sys


def inspect(path: Path) -> dict[str, object]:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        return {"exists": False, "sha256": "absent"}
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise ValueError("state database must be a regular file, not a symlink")

    digest = hashlib.sha256()
    with path.open("rb") as state_file:
        for chunk in iter(lambda: state_file.read(1024 * 1024), b""):
            digest.update(chunk)
    return {"exists": True, "sha256": digest.hexdigest()}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("path")
    args = parser.parse_args()
    if not os.path.isabs(args.path) or os.path.normpath(args.path) != args.path:
        parser.error("path must be clean and absolute")
    try:
        result = inspect(Path(args.path))
    except ValueError as error:
        print(f"inspect-runtime-state: {error}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
