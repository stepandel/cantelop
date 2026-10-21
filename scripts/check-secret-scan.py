#!/usr/bin/env python3
"""Prove fixture exceptions still detect synthetic credentials in the same files."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scanner", type=Path)
    args = parser.parse_args()
    paths = ["console/.env.example", "console/core/db/release-repository.test.mjs",
             "fire-fuse/controlauth/controlauth_test.go", "fire-fuse/gateway/gateway_test.go",
             "platform/ansible/README.md"]
    with tempfile.TemporaryDirectory(prefix="cantelop-scan-negative-") as directory:
        root = Path(directory)
        for name in paths:
            path = root / "source" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            synthetic = hashlib.sha256(("synthetic-negative-control:" + name).encode()).hexdigest()
            path.write_text((f"CLOUDFLARE_OBSERVABILITY_TOKEN={synthetic[:40]}\n" if name.endswith(".env.example")
                             else f'const testToken = "{synthetic}"\n'))
        report = root / "report.json"
        result = subprocess.run([str(args.scanner.resolve()), "dir", str(root / "source"),
                                 "--config", str(ROOT / ".gitleaks.toml"), "--redact=100",
                                 "--ignore-gitleaks-allow", "--no-banner", "--report-format", "json",
                                 "--report-path", str(report)], capture_output=True)
        if result.returncode != 1 or not report.is_file():
            raise SystemExit("secret-scan negative controls were not detected; check scanner/configuration")
        detected = {Path(finding["File"]).as_posix() for finding in json.loads(report.read_text())}
        missing = [name for name in paths if not any(path.endswith("/" + name) or path == name for path in detected)]
        if missing:
            raise SystemExit("secret-scan exceptions suppressed new credentials in: " + ", ".join(missing))
        print(f"Verified synthetic credential detection in all {len(paths)} fixture paths")


if __name__ == "__main__":
    main()
