#!/usr/bin/env python3
"""Measure snapshot-pool Sandbox startup on a drained Fire Fuse host."""

import argparse
import hashlib
import json
import math
import os
import re
import sqlite3
import statistics
import subprocess
import sys
import time
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path

TOKEN_PATH = Path('/etc/fire-fuse/control-clients/operator.token')
CONFIG_PATH = Path('/etc/fire-fuse/config.json')
STATE_PATH = Path('/var/lib/fire-fuse/state/state.db')
COMPUTE_ORIGIN = 'http://10.77.0.1:8084'
RUNTIME_ORIGIN = 'http://127.0.0.1:8080'


def file_identity(path, expected=None, allow_symlink=False):
    path = Path(path)
    if not path.is_absolute() or (path.is_symlink() and not allow_symlink) or not path.is_file():
        raise ValueError(f'untrusted benchmark artifact: {path}')
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(4 * 1024 * 1024), b''):
            digest.update(chunk)
    identity = {'path': str(path), 'sha256': digest.hexdigest(), 'size_bytes': path.stat().st_size}
    if expected and (identity['sha256'] != expected['sha256'] or identity['size_bytes'] != expected['size_bytes']):
        raise ValueError(f'benchmark artifact does not match its manifest: {path}')
    return identity


def preflight(build_id):
    if os.geteuid() != 0:
        raise ValueError('startup benchmark requires root on the compute host')
    if subprocess.run(['systemctl', 'is-active', '--quiet', 'fire-fuse.service']).returncode:
        raise ValueError('Fire Fuse is not active')
    token = TOKEN_PATH.read_text().strip()
    if not re.fullmatch(r'[0-9a-f]{64}', token):
        raise ValueError('invalid Fire Fuse control token')
    config = json.loads(CONFIG_PATH.read_text())
    if config.get('image_catalog') != '/etc/fire-fuse/image-catalog.json':
        raise ValueError('unexpected image catalog')
    profile = config['profiles']['default']
    bundle = Path(profile['snapshot_bundle'])
    manifest_path = bundle / 'manifest.json'
    manifest = json.loads(manifest_path.read_text())
    if manifest['machine']['vcpu_count'] != 1 or manifest['machine']['memory_mib'] != 1024:
        raise ValueError('benchmark snapshot is not 1 vCPU / 1024 MiB')
    catalog = json.loads(Path(config['image_catalog']).read_text())
    image = catalog['images'][build_id]
    artifacts = {
        'orchestrator': file_identity('/usr/local/bin/fire-fuse'),
        'configuration': file_identity(CONFIG_PATH),
        'catalog': file_identity(config['image_catalog']),
        'snapshot_manifest': file_identity(manifest_path),
        'kernel': file_identity(profile['kernel_image'], manifest['inputs']['kernel']),
        'platform_rootfs': file_identity(profile['rootfs_image'], manifest['inputs']['platform_rootfs']),
        'container_rootfs': file_identity(image['rootfs_image'], {
            'sha256': image['rootfs_sha256'], 'size_bytes': image['rootfs_size_bytes'],
        }),
        'snapshot_memory': file_identity(bundle / 'memory', manifest['files']['memory']),
        'snapshot_state': file_identity(bundle / 'vmstate', manifest['files']['state']),
    }
    pid = int(subprocess.check_output(['systemctl', 'show', '--property=MainPID', '--value', 'fire-fuse.service'], text=True).strip())
    if pid <= 0 or file_identity(f'/proc/{pid}/exe', allow_symlink=True)['sha256'] != artifacts['orchestrator']['sha256']:
        raise ValueError('running Fire Fuse binary differs from installed artifact')
    if running_count():
        raise ValueError('compute host is not drained')
    return token, artifacts


def running_count():
    connection = sqlite3.connect(f'file:{STATE_PATH}?mode=ro', uri=True)
    try:
        return connection.execute('select count(*) from sandboxes where state="running"').fetchone()[0]
    finally:
        connection.close()


def send(token, method, url, payload=None):
    data = None if payload is None else json.dumps(payload, separators=(',', ':')).encode()
    request = urllib.request.Request(url, data=data, method=method, headers={
        'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json',
    })
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=30) as response:
        return json.loads(response.read())


def create(token, sandbox_id, app_id, workspace_id):
    body = json.dumps({'id': sandbox_id, 'app_id': app_id, 'workspace_id': workspace_id}, separators=(',', ':'))
    result = subprocess.run([
        '/usr/bin/curl', '--noproxy', '*', '--silent', '--show-error', '--fail-with-body',
        '--max-time', '30', '--config', '-',
        '--header', 'Content-Type: application/json', '--request', 'POST', '--data', body,
        '--write-out', '\n__TIME__:%{time_total}', RUNTIME_ORIGIN + '/v1/sandboxes',
    ], input='header = "Authorization: Bearer ' + token + '"\n', capture_output=True, text=True, timeout=35)
    if result.returncode:
        raise RuntimeError('Sandbox create failed: ' + result.stderr.strip() + ' ' + result.stdout[-300:])
    document, seconds = result.stdout.rsplit('\n__TIME__:', 1)
    return json.loads(document), round(float(seconds) * 1_000_000_000)


def trace_for(sandbox_id):
    for _ in range(10):
        result = subprocess.run([
            '/usr/bin/journalctl', '-u', 'fire-fuse.service', '--since', '5 minutes ago', '-o', 'cat', '--no-pager',
        ], capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise RuntimeError('could not read Fire Fuse warmup trace')
        for line in reversed(result.stdout.splitlines()):
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get('msg') == 'sandbox warmup trace completed' and event.get('sandbox_id') == sandbox_id:
                return event
        time.sleep(0.2)
    raise RuntimeError('warmup trace missing for ' + sandbox_id)


def summarize(trials):
    values = sorted(trial['startup_ns'] for trial in trials if trial['pool_hit'])
    if not values:
        return None
    return {
        'samples': len(values), 'min_ns': values[0], 'median_ns': statistics.median(values),
        'p95_nearest_rank_ns': values[math.ceil(0.95 * len(values)) - 1], 'max_ns': values[-1],
    }


def snapshot_pool_hit(event):
    names = {step.get('name') for step in event.get('steps', [])}
    return 'host_pool.hit' in names and 'host_pool.miss' not in names


def passes_policy(summary, required_samples, median_limit_ns, p95_limit_ns, errors):
    return bool(summary and summary['samples'] >= required_samples and not errors
                and summary['median_ns'] <= median_limit_ns
                and summary['p95_nearest_rank_ns'] <= p95_limit_ns)


def run(args):
    series = uuid.uuid4().hex
    owner, app, workspace = 'usr_' + series, 'app_' + series, 'wsp_' + series
    app_url = f'{COMPUTE_ORIGIN}/v1/internal/users/{owner}/apps/{app}'
    output = Path(args.output_directory) / f'startup-only-{datetime.now(timezone.utc):%Y%m%d}-{series}.json'
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    trials, errors, artifacts = [], [], None
    token, attempted_registration = None, False
    try:
        token, artifacts = preflight(args.build_id)
        attempted_registration = True
        send(token, 'PUT', app_url, {
            'slug': 'warmup-gate-' + series[:8], 'build_id': args.build_id,
            'session_runtime': {'runtime': {'internal_port': 3000, 'profile': 'default'}},
        })
        send(token, 'PUT', app_url + '/workspaces/' + workspace, {'slug': 'benchmark'})
        for _ in range(args.trials):
            if running_count():
                raise RuntimeError('another Sandbox is running before a trial')
            sandbox_id = 'benchmark-' + str(uuid.uuid4()) + '-single'
            try:
                response, elapsed = create(token, sandbox_id, app, workspace)
                if response.get('id') != sandbox_id or response.get('build_id') != args.build_id or response.get('profile') != 'default':
                    raise RuntimeError('Sandbox identity, Build, or profile mismatch')
                event = trace_for(sandbox_id)
                if event.get('status') != 202 or not isinstance(event.get('total_ns'), int):
                    raise RuntimeError('invalid warmup trace')
                hit = snapshot_pool_hit(event)
                trials.append({'sandbox_id': sandbox_id, 'startup_ns': elapsed,
                               'trace_total_ns': event['total_ns'], 'pool_hit': hit})
                print(f'trial {len(trials)}: {elapsed / 1e6:.3f} ms, pool_hit={hit}', file=sys.stderr, flush=True)
                if not hit:
                    raise RuntimeError('snapshot pool miss')
            finally:
                try:
                    send(token, 'DELETE', RUNTIME_ORIGIN + '/v1/sandboxes/' + sandbox_id)
                except Exception as error:
                    errors.append('Sandbox cleanup failed: ' + str(error))
            time.sleep(1)
    except Exception as error:
        errors.append(str(error))
    finally:
        if attempted_registration and token:
            try:
                send(token, 'DELETE', app_url)
            except Exception as error:
                errors.append('App cleanup failed: ' + str(error))
            else:
                try:
                    connection = sqlite3.connect(f'file:{STATE_PATH}?mode=ro', uri=True)
                    try:
                        archived = connection.execute('select deleted_at from apps where id=?', (app,)).fetchone()
                    finally:
                        connection.close()
                    if archived is None or archived[0] is None:
                        errors.append('benchmark App archival was not persisted')
                except Exception as error:
                    errors.append('could not verify benchmark App archival: ' + str(error))
    summary = summarize(trials)
    passed = passes_policy(summary, args.trials, args.median_limit_ns, args.p95_limit_ns, errors)
    result = {
        'schema_version': 1, 'kind': 'snapshot-startup-only', 'series_id': 'series-' + str(uuid.UUID(series)),
        'measured_at': datetime.now(timezone.utc).isoformat(), 'host': 'fire-fuse-staging',
        'profile': 'default', 'vm': {'vcpus': 1, 'memory_mib': 1024}, 'build_id': args.build_id,
        'method': 'curl POST /v1/sandboxes time_total through guest readiness',
        'required_pool_hit': True, 'artifacts': artifacts, 'trials': trials, 'summary': summary,
        'policy': {'minimum_trials': args.trials, 'median_at_most_ns': args.median_limit_ns,
                   'p95_at_most_ns': args.p95_limit_ns, 'passed': passed}, 'errors': errors,
    }
    output.write_text(json.dumps(result, indent=2) + '\n')
    os.chmod(output, 0o600)
    print(json.dumps({'path': str(output), 'series_id': result['series_id'], 'summary': summary, 'passed': passed}))
    return 0 if passed else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build-id', required=True)
    parser.add_argument('--output-directory', required=True)
    parser.add_argument('--trials', type=int, default=10)
    parser.add_argument('--median-limit-ns', type=int, required=True)
    parser.add_argument('--p95-limit-ns', type=int, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', args.build_id) or not 2 <= args.trials <= 20:
        parser.error('invalid Build ID or trial count')
    if args.median_limit_ns <= 0 or args.p95_limit_ns <= 0 or not Path(args.output_directory).is_absolute():
        parser.error('invalid threshold or output directory')
    return run(args)


if __name__ == '__main__':
    raise SystemExit(main())
