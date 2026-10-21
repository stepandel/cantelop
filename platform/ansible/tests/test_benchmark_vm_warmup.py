import unittest

from support import load_script


MODULE = load_script("benchmark-vm-warmup.py")


class BenchmarkVMWarmupTest(unittest.TestCase):
    def test_snapshot_pool_hit_rejects_miss_even_if_hit_is_present(self):
        self.assertTrue(MODULE.snapshot_pool_hit({'steps': [{'name': 'host_pool.hit'}]}))
        self.assertFalse(MODULE.snapshot_pool_hit({'steps': [{'name': 'host_pool.miss'}]}))
        self.assertFalse(MODULE.snapshot_pool_hit({'steps': [
            {'name': 'host_pool.hit'}, {'name': 'host_pool.miss'},
        ]}))

    def test_gate_uses_nearest_rank_p95_and_requires_ten_hits(self):
        trials = [{'startup_ns': value, 'pool_hit': True} for value in (
            220_000_000, 230_000_000, 240_000_000, 245_000_000, 245_000_000,
            250_000_000, 255_000_000, 258_000_000, 270_000_000, 299_000_000,
        )]
        trials.append({'startup_ns': 900_000_000, 'pool_hit': False})
        summary = MODULE.summarize(trials)
        self.assertEqual(summary['samples'], 10)
        self.assertEqual(summary['median_ns'], 247_500_000)
        self.assertEqual(summary['p95_nearest_rank_ns'], 299_000_000)
        self.assertTrue(MODULE.passes_policy(summary, 10, 260_000_000, 300_000_000, []))
        self.assertFalse(MODULE.passes_policy(summary, 11, 260_000_000, 300_000_000, []))
        self.assertFalse(MODULE.passes_policy(summary, 10, 260_000_000, 298_000_000, []))
        self.assertFalse(MODULE.passes_policy(summary, 10, 260_000_000, 300_000_000, ['cleanup failed']))
        trials[9]['startup_ns'] = 366_708_000
        self.assertFalse(MODULE.passes_policy(MODULE.summarize(trials), 10, 260_000_000, 300_000_000, []))


if __name__ == '__main__':
    unittest.main()
