import unittest

from support import load_script

module = load_script("benchmark-session-activation.py", "activation")


class ActivationBenchmarkTest(unittest.TestCase):
    def test_handler_entry_is_separate_from_reply_settlement(self):
        sample = module.validate_trial({"trialId": "trial", "handlerStartedAt": 1350,
                                        "submittedAt": 1000, "responseMs": 800}, "trial")
        self.assertEqual(sample["handler_start_ms"], 350)
        self.assertEqual(sample["response_ms"], 800)

    def test_rejects_clock_skew_and_wrong_identity(self):
        for change in ({"handlerStartedAt": 900}, {"handlerStartedAt": 1900},
                       {"trialId": "wrong"}, {"responseMs": float("nan")}):
            with self.assertRaises(ValueError):
                module.validate_trial({"trialId": "trial", "handlerStartedAt": 1350,
                                       "submittedAt": 1000, "responseMs": 800, **change}, "trial")

    def test_p95_uses_nearest_rank(self):
        self.assertEqual(module.distribution(list(range(1, 21)))["p95_nearest_rank"], 19)
