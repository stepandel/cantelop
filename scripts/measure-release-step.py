#!/usr/bin/env python3
"""Run one release command and append a private, argument-free timing record."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument("--step", required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,63}", args.step):
        parser.error("--step must be a short lowercase label")
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a command is required after --")
    if not args.evidence.parent.is_dir():
        parser.error("the evidence directory must already exist")

    flags = os.O_WRONLY | os.O_CREAT | os.O_APPEND | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(args.evidence, flags, 0o600)
    if stat.S_IMODE(os.fstat(descriptor).st_mode) != 0o600:
        os.close(descriptor)
        parser.error("release timing evidence must be mode 0600")

    started = datetime.now(timezone.utc)
    start_clock = time.monotonic()
    try:
        result = subprocess.run(command, check=False)
        status = result.returncode
    except OSError as error:
        print(f"could not start release step {args.step}: {error}", file=sys.stderr)
        status = 127
    except KeyboardInterrupt:
        print(f"release step {args.step} was interrupted", file=sys.stderr)
        status = 130
    finished = datetime.now(timezone.utc)
    record = {
        "step": args.step,
        "started_at": started.isoformat(),
        "finished_at": finished.isoformat(),
        "elapsed_seconds": round(time.monotonic() - start_clock, 3),
        "exit_code": status,
    }
    try:
        os.write(descriptor, (json.dumps(record, sort_keys=True) + "\n").encode())
    finally:
        os.close(descriptor)
    return status


if __name__ == "__main__":
    raise SystemExit(main())
