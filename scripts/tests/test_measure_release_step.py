import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).parents[1] / "measure-release-step.py"


class MeasureReleaseStepTest(unittest.TestCase):
    def test_records_duration_and_failure_without_command_arguments(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory) / "timings.jsonl"
            command = [
                sys.executable,
                str(SCRIPT),
                "--evidence",
                str(evidence),
                "--step",
                "canary",
                "--",
                sys.executable,
                "-c",
                "raise SystemExit(7) # private-command-argument",
            ]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 7)
            record = json.loads(evidence.read_text())
            self.assertEqual(record["step"], "canary")
            self.assertEqual(record["exit_code"], 7)
            self.assertGreaterEqual(record["elapsed_seconds"], 0)
            self.assertNotIn("private-command-argument", evidence.read_text())
            self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
