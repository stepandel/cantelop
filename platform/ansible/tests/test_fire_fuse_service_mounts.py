"""Run the live namespace regression with CANTELOP_TEST_SYSTEMD_MOUNTS=1.

Requires a provisioned Linux host with the protected workspace mounted. The
transient service reads mountinfo only; it never starts or stops Fire Fuse.
"""
import json
import os
from pathlib import Path
import shlex
import subprocess
import unittest
import uuid


ROOT = Path(__file__).resolve().parents[3]
WORKSPACE = Path('/var/lib/fire-fuse/workspaces')
ADMIN_WORKSPACE = Path('/var/lib/fire-fuse/workspace-admin')


def service_properties():
    return dict(
        line.split('=', 1)
        for line in (ROOT / 'platform/systemd/compute/fire-fuse.service').read_text().splitlines()
        if '=' in line and not line.startswith('#')
    )


class FireFuseServiceMountsTest(unittest.TestCase):
    def test_writable_paths_do_not_clone_workspace_parent(self):
        properties = service_properties()
        writable = [Path(p.lstrip('-')) for p in shlex.split(properties['ReadWritePaths'])]
        writable += [Path('/var/lib') / p for p in shlex.split(properties['StateDirectory'])]
        for path in writable:
            self.assertNotIn(path, WORKSPACE.parents, f'{path} clones the workspace mount')
        self.assertIn(WORKSPACE, writable)
        self.assertIn(ADMIN_WORKSPACE, writable)
        config = json.loads((ROOT / 'config.example.json').read_text())
        for path in [Path(config['runtime_dir']), Path(config['state_db']).parent]:
            self.assertTrue(any(p == path or p in path.parents for p in writable), path)

    @unittest.skipUnless(os.environ.get('CANTELOP_TEST_SYSTEMD_MOUNTS') == '1',
                         'requires opt-in on a provisioned Linux systemd host')
    def test_packaged_namespace_has_protected_writable_workspace_mounts(self):
        properties = service_properties()
        command = ['systemd-run', '--quiet', '--wait', '--pipe', '--collect',
                   '--unit=cantelop-mount-test-' + uuid.uuid4().hex]
        # Reproduce the packaged filesystem isolation without running its daemon,
        # loading credentials, or acquiring its state database lock.
        for key in ('ProtectSystem', 'ProtectHome', 'PrivateTmp',
                    'ProtectKernelTunables', 'ProtectKernelModules',
                    'ProtectKernelLogs', 'ProtectControlGroups',
                    'ReadWritePaths', 'StateDirectory', 'StateDirectoryMode'):
            command += ['--property', key + '=' + properties[key]]
        command += ['/usr/bin/cat', '/proc/self/mountinfo']
        result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=30)
        mounts = [line.split() for line in result.stdout.splitlines()]
        workspace = [m for m in mounts if m[4] == str(WORKSPACE)]
        self.assertEqual(len(workspace), 1, result.stdout)
        mount = workspace[0]
        separator = mount.index('-')
        self.assertEqual(mount[3], '/')
        self.assertEqual(mount[separator + 1], 'fuse.juicefs-workspace-v1')
        self.assertIn('rw', mount[5].split(','))
        self.assertTrue({'user_id=0', 'default_permissions', 'allow_other'} <=
                        set(mount[separator + 3].split(',')))
        admin_workspace = [m for m in mounts if m[4] == str(ADMIN_WORKSPACE)]
        self.assertEqual(len(admin_workspace), 1, result.stdout)
        admin_mount = admin_workspace[0]
        admin_separator = admin_mount.index('-')
        self.assertEqual(admin_mount[3], '/')
        self.assertEqual(admin_mount[admin_separator + 1], 'fuse.juicefs')
        self.assertIn('rw', admin_mount[5].split(','))
        self.assertIn('user_id=0', admin_mount[admin_separator + 3].split(','))
        self.assertNotIn('allow_other', admin_mount[admin_separator + 3].split(','))
        for path in ('state', 'runtime', 'images'):
            matches = [m for m in mounts if m[4] == '/var/lib/fire-fuse/' + path]
            self.assertEqual(len(matches), 1, (path, result.stdout))
            self.assertIn('rw', matches[0][5].split(','))
        self.assertFalse(any(m[4] == '/var/lib/fire-fuse' for m in mounts))
        self.assertIn('ro', next(m for m in mounts if m[4] == '/')[5].split(','))


if __name__ == '__main__':
    unittest.main()
