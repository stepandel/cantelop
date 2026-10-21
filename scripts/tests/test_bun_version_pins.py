"""Keep every Bun pin in agreement with console/package.json.

console/package.json "packageManager" is the source of truth. Workflows and
build scripts read it directly. The remaining copies cannot: the guest image
download is pinned by URL and SHA-256 together, the guest priming script runs
inside the image, and the CLI embeds the version it requires.
"""

import json
import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]


def pinned_version() -> str:
    package = json.loads((ROOT / "console" / "package.json").read_text())
    match = re.fullmatch(r"bun@(\d+\.\d+\.\d+)", package["packageManager"])
    assert match is not None, package["packageManager"]
    return match.group(1)


class BunVersionPinTests(unittest.TestCase):
    def test_workflows_read_the_package_manager_pin(self):
        workflows = sorted((ROOT / ".github" / "workflows").glob("*.yml"))
        uses = 0
        for workflow in workflows:
            text = workflow.read_text()
            with self.subTest(workflow=workflow.name):
                self.assertNotRegex(text, r"\bbun-version:")
                for step in re.findall(r"uses: oven-sh/setup-bun@\S+\n(?:\s+.*\n){0,3}", text):
                    uses += 1
                    self.assertIn("bun-version-file: console/package.json", step)
        self.assertGreater(uses, 0)

    def test_builder_script_reads_the_package_manager_pin(self):
        script = (ROOT / "scripts" / "build-builder-linux-amd64.sh").read_text()
        self.assertIn('"${console_dir}/package.json"', script)
        self.assertIn('"packageManager": "bun@', script)
        self.assertNotRegex(script, r'expected_bun_version="\d')

    def test_copies_that_cannot_read_the_pin_agree_with_it(self):
        version = re.escape(pinned_version())
        guest = (ROOT / "platform" / "hetzner" / "prepare-guest.sh").read_text()
        self.assertRegex(
            guest,
            rf"bun_url='https://github\.com/oven-sh/bun/releases/download/bun-v{version}/",
        )
        priming = (
            ROOT / "platform" / "hetzner" / "guest" / "fire-fuse-prime-platform-runtime"
        ).read_text()
        self.assertRegex(priming, rf"(?m)^readonly expected_version={version}$")
        cli_pins = [
            match
            for path in (ROOT / "clients" / "cli").rglob("*.go")
            if not path.name.endswith("_test.go")
            for match in re.findall(r'[lL]ocalBunVersion\s*=\s*"([^"]+)"', path.read_text())
        ]
        self.assertEqual(cli_pins, [pinned_version()])


if __name__ == "__main__":
    unittest.main()
