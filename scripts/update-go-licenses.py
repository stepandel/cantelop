#!/usr/bin/env python3
"""Refresh Go notices with the pinned license tool, without changing go.mod."""

import argparse
import csv
import hashlib
import io
import json
import os
import runpy
from pathlib import Path
import shutil
import subprocess
import tempfile
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
TOOL = "github.com/google/go-licenses/v2@v2.0.1"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-licenses", type=Path, help="use an already installed v2.0.1 tool")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="cantelop-go-licenses-") as temporary:
        work = Path(temporary)
        tool = args.go_licenses
        if tool is None:
            subprocess.run(["go", "install", TOOL], env=os.environ | {"GOBIN": str(work)}, check=True)
            tool = work / "go-licenses"
        command = [str(tool.resolve())]
        destination = work / "notices"
        destination.mkdir()
        by_package = {}
        targets = [("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64")]
        for operating_system, architecture in targets:
            environment = os.environ | {"GOOS": operating_system, "GOARCH": architecture}
            report = subprocess.check_output(command + ["report", "--ignore=github.com/stepandel/cantelop", "./..."], cwd=ROOT, env=environment, text=True)
            by_package.update({row[0]: row for row in csv.reader(io.StringIO(report))})
            target_notices = work / f"{operating_system}-{architecture}"
            subprocess.run(command + ["save", "--ignore=github.com/stepandel/cantelop", "./...", f"--save_path={target_notices}"], cwd=ROOT, env=environment, check=True)
            for source in sorted(target_notices.rglob("*")):
                if not source.is_file():
                    continue
                target = destination / source.relative_to(target_notices)
                if target.exists() and target.read_bytes() != source.read_bytes():
                    raise SystemExit(f"conflicting target-specific notice: {target.relative_to(destination)}")
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
        rows = list(by_package.values())
        if not rows or any(row[2] == "Unknown" for row in rows):
            raise SystemExit("unknown Go dependency license; review before refreshing notices")
        output = subprocess.check_output(["go", "list", "-m", "-json", "all"], cwd=ROOT, text=True)
        decoder = json.JSONDecoder()
        graph = []
        while output.strip():
            output = output.lstrip()
            module, end = decoder.raw_decode(output)
            output = output[end:]
            if not module.get("Main"):
                graph.append(module)

        def owner(path):
            # Nested modules own their own subtree, so attribute a path to the longest matching module.
            return max((module["Path"] for module in graph if path == module["Path"] or path.startswith(module["Path"] + "/")), key=len, default=None)

        modules = []
        for module in graph:
            name = module["Path"]
            notices = [{"path": str(path.relative_to(destination)), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                       for path in sorted((destination / name).rglob("*"))
                       if path.is_file() and owner(str(path.parent.relative_to(destination))) == name]
            if not notices:
                continue  # Module-graph-only dependencies are not linked by these packages.
            row = next((row for row in rows if owner(row[0]) == name), None)
            if row is None:
                raise SystemExit(f"saved notices for {name} have no go-licenses report row; review before refreshing notices")
            license_url = row[1] if row[1] != "Unknown" else f"https://pkg.go.dev/{name}@{module['Version']}?tab=licenses"
            modules.append({"module": name, "version": module["Version"], "license": row[2], "license_url": license_url, "notices": notices})
        recorded = {module["module"] for module in modules}
        if any(owner(row[0]) not in recorded for row in rows):
            raise SystemExit("a reported dependency has no versioned notice inventory")
        goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], cwd=ROOT, text=True).strip())
        go_license = next((path for path in (goroot / "LICENSE", goroot.parent / "LICENSE") if path.is_file()), None)
        if go_license:
            shutil.copyfile(go_license, destination / "GO-LICENSE")
        else:
            version = subprocess.check_output(["go", "env", "GOVERSION"], cwd=ROOT, text=True).strip()
            with urllib.request.urlopen(f"https://raw.githubusercontent.com/golang/go/{version}/LICENSE", timeout=30) as response:
                (destination / "GO-LICENSE").write_bytes(response.read())
        manifest = {
            "schema_version": 2,
            "requirements": runpy.run_path(str(ROOT / "scripts/release-notices.py"))["go_requirements"](),
            "generator": TOOL,
            "scope": "non-test Go packages in this repository across Linux/Darwin amd64/arm64",
            "modules": sorted(modules, key=lambda module: module["module"]),
        }
        (destination / "modules.json").write_text(json.dumps(manifest, indent=2) + "\n")
        target = ROOT / "licenses/go"
        if target.exists():
            shutil.rmtree(target)
        shutil.copytree(destination, target)
        print(f"Recorded notices for {len(modules)} Go dependency modules across {len(targets)} targets")


if __name__ == "__main__":
    main()
