import copy
import json
import os
from pathlib import Path
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = yaml.load((ROOT / ".github/workflows/check.yml").read_text(), Loader=yaml.BaseLoader)


class CIGateTests(unittest.TestCase):
    def setUp(self):
        self.results = {name: {"result": "skipped", "outputs": {}} for name in WORKFLOW["jobs"] if name != "check"}
        self.results["changes"] = {"result": "success", "outputs": {lane: "false" for lane in ("go", "console", "edge", "policy", "terraform", "juicefs")}}
        self.results["secrets"]["result"] = "success"

    def gate(self, repository="1323478043"):
        script = WORKFLOW["jobs"]["check"]["steps"][0]["run"]
        return subprocess.run(["bash", "-e", "-c", script], capture_output=True, text=True,
                              env=os.environ | {"RESULTS": json.dumps(self.results), "GITHUB_REPOSITORY_ID": repository})

    def test_gate_runs_after_every_job_even_on_failure(self):
        gate = WORKFLOW["jobs"]["check"]
        self.assertEqual(set(gate["needs"]), set(self.results))
        self.assertEqual(gate["if"], "${{ always() }}")

    def test_documentation_pr_can_skip_unselected_suites(self):
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_selected_jobs_must_complete_and_succeed(self):
        initial = copy.deepcopy(self.results)
        for lane in ("go", "console", "edge", "policy", "juicefs"):
            for status in ("failure", "cancelled", "skipped"):
                with self.subTest(lane=lane, status=status):
                    self.results = copy.deepcopy(initial)
                    self.results["changes"]["outputs"][lane] = "true"
                    self.results[lane]["result"] = status
                    result = self.gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(lane, result.stderr)

    def test_selection_and_secret_scan_cannot_be_skipped_or_failed(self):
        initial = copy.deepcopy(self.results)
        for job in ("changes", "secrets"):
            for status in ("failure", "cancelled", "skipped"):
                with self.subTest(job=job, status=status):
                    self.results = copy.deepcopy(initial)
                    self.results[job]["result"] = status
                    self.assertNotEqual(self.gate().returncode, 0)

    def test_public_forks_skip_private_juicefs_but_still_require_selected_tests(self):
        self.results["changes"]["outputs"].update(juicefs="true", console="true")
        self.assertNotEqual(self.gate(repository="public-fork").returncode, 0)
        self.results["console"]["result"] = "success"
        result = self.gate(repository="public-fork")
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
