import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock
from contextlib import redirect_stdout
import io

from support import load_module


ROOT = Path(__file__).parents[3]
RUNNER = ROOT / 'platform/ansible/scripts/benchmark-host-capacity.py'
COMPARATOR = ROOT / 'scripts/compare-host-capacity.py'


benchmark = load_module(RUNNER, 'benchmark_host_capacity')
compare = load_module(COMPARATOR, 'compare_host_capacity')


class HostCapacityBenchmarkTest(unittest.TestCase):
    def test_nfs_clients_must_have_distinct_sandbox_owners(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for index, name in enumerate((
                    'Linux NFSv4.2 sandbox-a/ubuntu-fc-uvm',
                    'Linux NFSv4.2 sandbox-b/ubuntu-fc-uvm')):
                client = root / str(index)
                client.mkdir()
                (client / 'info').write_text(f'name: "{name}"\n')
            with mock.patch.object(benchmark, 'NFS_CLIENTS', root):
                self.assertEqual(benchmark.nfs_client_identity({'sandbox-a', 'sandbox-b'}), {
                    'expected': 2, 'matched': 2, 'missing': [],
                })
                (root / '1' / 'info').write_text(
                    'name: "Linux NFSv4.2 sandbox-a/ubuntu-fc-uvm"\n')
                self.assertEqual(benchmark.nfs_client_identity({'sandbox-a', 'sandbox-b'}), {
                    'expected': 2, 'matched': 1, 'missing': ['sandbox-b'],
                })

    def test_profile_rejects_more_active_workloads_than_vms(self):
        original = json.loads((ROOT / 'tests/benchmarks/profiles/host-capacity-example-v1.json').read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'profile.json'
            path.write_text(json.dumps(original))
            self.assertEqual(benchmark.load_profile(path)['target_vms'], 100)
            original['workspace_active_vms'] = 101
            path.write_text(json.dumps(original))
            with self.assertRaisesRegex(ValueError, 'cannot exceed target_vms'):
                benchmark.load_profile(path)

    def test_failed_guest_does_not_discard_successful_io_samples(self):
        def fake_guest(ip, code, args, timeout):
            if ip == 'second':
                raise subprocess.TimeoutExpired('ssh', timeout, output=b'{"phase":"fsync"}\n')
            return json.dumps({'write_ms': 3, 'fsync_ms': 7, 'read_ms': 2}), 15

        with mock.patch.object(benchmark, 'guest', side_effect=fake_guest):
            results, errors = benchmark.run_parallel(
                [{'guest_ip': 'first'}, {'guest_ip': 'second'}], benchmark.IO_CODE,
                lambda index: (f'file-{index}', 4096), 90)
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]['fsync_ms'], 7)
        self.assertEqual(errors, [{'index': 1, 'reason': 'guest/SSH timeout during fsync'}])

    def test_workspace_reports_kernel_thread_shortage_counter(self):
        def host_sample(epoch, enqueued, woken):
            return {
                'epoch': epoch, 'available_mib': 10000, 'swap_used_mib': 0,
                'service_memory_mib': 1000, 'cpu_pressure_avg10': 0,
                'memory_pressure_avg10': 0, 'nfs': {
                    'sockets_enqueued': enqueued, 'threads_woken': woken,
                },
            }

        monitor = mock.Mock(samples=[], abort=mock.Mock())
        monitor.abort.is_set.return_value = False
        samples = iter((host_sample(1, 5, 100), host_sample(2, 8, 120)))
        result = ({'write_ms': 3, 'fsync_ms': 7, 'read_ms': 2,
                   'ssh_round_trip_ms': 15},)
        with (mock.patch.object(benchmark, 'sample', side_effect=samples),
              mock.patch.object(benchmark, 'run_parallel', return_value=(list(result), []))):
            axis = benchmark.execute_axis('workspace', [{'guest_ip': 'first'}], {
                'workspace_active_vms': 1, 'workspace_bytes_per_vm': 4096,
            }, monitor)
        self.assertEqual(axis['nfs_sockets_enqueued_delta'], 3)

    def test_comparison_requires_same_workload_and_host(self):
        baseline = {'profile': {'target_vms': 100}, 'identity': {
            'host': 'host-a', 'build_id': 'sha256:test', 'guest_memory_mib': 1024,
            'guest_vcpus': 4,
        }}
        candidate = json.loads(json.dumps(baseline))
        compare.comparable(baseline, candidate)
        candidate['profile']['target_vms'] = 99
        with self.assertRaisesRegex(ValueError, 'different workload profiles'):
            compare.comparable(baseline, candidate)
        candidate = json.loads(json.dumps(baseline))
        candidate['identity']['host'] = 'host-b'
        with self.assertRaisesRegex(ValueError, 'differ in host'):
            compare.comparable(baseline, candidate)

    def test_comparison_flags_a_startup_regression(self):
        identity = {'host': 'host-a', 'build_id': 'sha256:test',
                    'guest_memory_mib': 1024, 'guest_vcpus': 4, 'nfs_threads': 8}
        axes = {
            'cpu': {'mean_host_cores_used': 2, 'host': {'maximum_cpu_pressure_avg10': 1}},
            'memory': {'host': {'maximum_service_memory_mib': 2000,
                                'maximum_memory_pressure_avg10': 0}},
            'workspace': {'write_ms': {'p95': 10}, 'fsync_ms': {'p95': 20},
                          'read_ms': {'p95': 10}, 'throughput_mib_per_second': 40},
            'startup_throughput': {'vms_per_minute': 100},
            'startup_time': {'request_ms': {'p95': 100}},
        }
        baseline = {'schema_version': 1, 'profile': {'target_vms': 100},
                    'identity': identity, 'axes': axes, 'passed': True}
        candidate = json.loads(json.dumps(baseline))
        candidate['axes']['startup_time']['request_ms']['p95'] = 130
        with tempfile.TemporaryDirectory() as directory:
            before, after = Path(directory) / 'before.json', Path(directory) / 'after.json'
            before.write_text(json.dumps(baseline))
            after.write_text(json.dumps(candidate))
            with redirect_stdout(io.StringIO()):
                code = compare.main(['--max-regression-percent', '20', str(before), str(after)])
        self.assertEqual(code, 1)


if __name__ == '__main__':
    unittest.main()
