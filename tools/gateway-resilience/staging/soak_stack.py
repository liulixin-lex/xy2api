#!/usr/bin/env python3
"""Plan, start, run or clean the independent soak stack; mutations require --execute."""

import argparse
import datetime
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import time
import urllib.request

import phase2

HERE = Path(__file__).resolve().parent
ROOT = HERE / 'soak-stack'
PROJECT = 'xy2api-staging-soak'
PORTS = [18310, 18311, 18313]
SUBNET = ipaddress.ip_network('172.31.211.0/24')
COMPOSE = ['docker', 'compose', '-p', PROJECT, '--env-file', str(ROOT / '.env'), '-f', str(ROOT / 'compose.yaml')]


def command(*args, **kwargs):
    return subprocess.check_output(args, cwd=HERE, text=True, **kwargs).strip()


def render(files, project):
    # Config validation never reads the real .env or creates a project.
    env = dict(os.environ, APP_IMAGE='xy2api-staging:plan-only', FAULT_IMAGE='xy2api-staging-fault-upstream:local',
               CDN_IMAGE='xy2api-staging-cdn-sim:local', POSTGRES_PASSWORD='plan-only', ADMIN_EMAIL='plan@example.invalid',
               ADMIN_PASSWORD='plan-only', JWT_SECRET='plan-only', TOTP_ENCRYPTION_KEY='plan-only')
    args = ['docker', 'compose', '-p', project, '--env-file', '/dev/null', '--profile', 'overlap']
    for file in files:
        args += ['-f', str(HERE / file)]
    return json.loads(command(*args, 'config', '--format', 'json', env=env))


def budgets(config):
    return {name: dict(memory_bytes=int(s['deploy']['resources']['limits']['memory']),
                       cpus=float(s['deploy']['resources']['limits']['cpus'])) for name, s in config['services'].items()}


def plan():
    main = render(['compose.yaml', 'compose.parallel.yaml'], 'xy2api-staging')
    soak = render(['compose.soak.yaml'], PROJECT)
    main_budget, soak_budget = budgets(main), budgets(soak)
    memory = sum(s['memory_bytes'] for s in [*main_budget.values(), *soak_budget.values()])
    cpus = sum(s['cpus'] for s in [*main_budget.values(), *soak_budget.values()])
    resources = phase2.resources()
    listeners = command('ss', '-H', '-ltn')
    occupied = {int(line.split()[3].rsplit(':', 1)[1]) for line in listeners.splitlines()}
    conflicts = sorted(set(PORTS) & occupied)
    network_ids = command('docker', 'network', 'ls', '-q').split()
    networks = json.loads(command('docker', 'network', 'inspect', *network_ids)) if network_ids else []
    subnet_conflict = any(SUBNET.overlaps(ipaddress.ip_network(c['Subnet'])) for n in networks
                          for c in (n.get('IPAM', {}).get('Config') or []) if c.get('Subnet') and ':' not in c['Subnet'])
    # Already-running main memory is deliberately not credited back to Available.
    enough = resources['disk_free_bytes'] >= 30 * 2**30 and resources['memory_available_bytes'] >= memory + 2 * 2**30
    enough = enough and cpus <= os.cpu_count() - 2
    for volume in soak.get('volumes', {}).values():
        if volume.get('external') or not volume['name'].startswith(PROJECT + '_'):
            raise RuntimeError('soak volume is not independent')
    result = dict(mode='plan-only', project=PROJECT, ports=PORTS, port_conflicts=conflicts,
                  subnet=str(SUBNET), subnet_conflict=subnet_conflict, main_limits=main_budget, soak_limits=soak_budget,
                  combined_memory_limit_bytes=memory, combined_cpu_limit=round(cpus, 2),
                  memory_reserve_bytes=2 * 2**30, cpu_reserve=2, resources=resources,
                  enough=enough, ready=enough and not conflicts and not subnet_conflict)
    return result


def runtime_environment():
    env = {k: v for k, v in os.environ.items() if not k.startswith(('REAL1_', 'REAL2_', 'REAL3_'))}
    env.pop('COMPOSE_FILE', None)
    env.pop('COMPOSE_PROJECT_NAME', None)
    # These fresh secrets belong only to the independent stack.
    for line in (ROOT / '.env').read_text().splitlines():
        key, value = line.split('=', 1)
        env[key] = value
    env.update(STAGING_PROJECT=PROJECT, STAGING_APP_PORT='18310', STAGING_FAULT_PORT='18311',
               STAGING_CDN_PORT='18313', ACCEPT_ADMIN_COMPLIANCE='1')
    return env


def same_candidate(build):
    main = phase2.identity(build)
    app = json.loads(command('docker', 'inspect', PROJECT + '-app-1'))[0]
    binary = command('docker', 'exec', PROJECT + '-app-1', 'sha256sum', '/app/xy2api').split()[0]
    if app['Image'] != main['image_id'] or binary != main['binary_sha256']:
        raise RuntimeError('the two stacks do not use the same candidate image and binary')
    main_app = json.loads(command('docker', 'inspect', 'xy2api-staging-app-1'))[0]
    if set(app['NetworkSettings']['Networks']) & set(main_app['NetworkSettings']['Networks']):
        raise RuntimeError('the stacks share a network')
    for service in ('postgres', 'app'):
        left = json.loads(command('docker', 'inspect', 'xy2api-staging-' + service + '-1'))[0]
        right = json.loads(command('docker', 'inspect', PROJECT + '-' + service + '-1'))[0]
        if {m['Source'] for m in left['Mounts']} & {m['Source'] for m in right['Mounts']}:
            raise RuntimeError('the stacks share a data mount')
    return dict(main=main, soak=dict(container_id=app['Id'], image_id=app['Image'], binary_sha256=binary,
                                   restart_count=app['RestartCount'], started_at=app['State']['StartedAt']))


def verify_main_limits(expected):
    ids = command('docker', 'ps', '-q', '--filter', 'label=com.docker.compose.project=xy2api-staging').split()
    for cid in ids:
        data = json.loads(command('docker', 'inspect', cid))[0]
        service = data['Config']['Labels']['com.docker.compose.service']
        limit = expected[service]
        actual = data['HostConfig']
        if not (0 < actual['Memory'] <= limit['memory_bytes'] and 0 < actual['NanoCpus'] <= limit['cpus'] * 1e9):
            raise RuntimeError('apply compose.parallel.yaml to the main stack before starting the soak: ' + service)


def start(build, evidence):
    if ROOT.exists() or evidence.exists():
        raise RuntimeError('temporary stack or evidence already exists; refusing to overwrite')
    proposed = plan()
    if not proposed['ready']:
        raise RuntimeError('ports, subnet or resource reserve prevents starting the soak')
    phase2.identity(build)
    verify_main_limits(proposed['main_limits'])
    ROOT.mkdir(mode=0o700)
    evidence.mkdir(parents=True, exist_ok=False)
    (ROOT / 'OWNER.json').write_text(json.dumps(dict(project=PROJECT, build=build, evidence=str(evidence))))
    shutil.copyfile(HERE / 'compose.soak.yaml', ROOT / 'compose.yaml')
    for name in ('fixtures.py', 'soak.py'):
        shutil.copyfile(HERE / name, ROOT / name)
    values = dict(POSTGRES_PASSWORD=secrets.token_hex(24), ADMIN_EMAIL='admin@soak.invalid', ADMIN_PASSWORD=secrets.token_hex(16),
                  JWT_SECRET=secrets.token_hex(32), TOTP_ENCRYPTION_KEY=secrets.token_hex(32), APP_IMAGE=build['image_id'],
                  FAULT_IMAGE=command('docker', 'inspect', '-f', '{{.Image}}', 'xy2api-staging-fault-upstream-1'),
                  CDN_IMAGE=command('docker', 'inspect', '-f', '{{.Image}}', 'xy2api-staging-cdn-sim-125-1'))
    with os.fdopen(os.open(ROOT / '.env', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as file:
        file.write(''.join(k + '=' + v + '\n' for k, v in values.items()))
    env = runtime_environment()
    subprocess.run(COMPOSE + ['up', '-d', '--no-build', '--pull', 'never'], cwd=ROOT, env=env, check=True)
    deadline = time.monotonic() + 180
    while True:
        try:
            with urllib.request.urlopen('http://127.0.0.1:18310/ready', timeout=3) as response:
                if json.load(response).get('status') == 'ready':
                    break
        except (OSError, ValueError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError('soak app did not become ready')
        time.sleep(1)
    with (evidence / 'fixtures.log').open('x') as log:
        subprocess.run([sys.executable, str(ROOT / 'fixtures.py'), 'http://127.0.0.1:18310'], cwd=ROOT, env=env,
                       stdout=log, stderr=subprocess.STDOUT, check=True)
    proof = same_candidate(build)
    (evidence / 'START.json').write_text(json.dumps(dict(build=build, plan=proposed, identities=proof), indent=2) + '\n')


def run(build, evidence):
    proof = same_candidate(build)
    env = runtime_environment()
    env.update(STAGING_LOG_FILE=str(evidence / 'app.log'),
               STAGING_OVERLAP_FILE=str(HERE / 'evidence' / ('parallel-activity-' + build['source_commit'] + '.jsonl')))
    meta = dict(started_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), identities_before=proof,
                build=build, scripts={n: hashlib.sha256((ROOT / n).read_bytes()).hexdigest() for n in ('soak.py', 'fixtures.py')})
    (evidence / 'RUN.json').write_text(json.dumps(meta, indent=2) + '\n')
    with (evidence / 'app.log').open('x') as app_log, (evidence / 'run.log').open('x') as log:
        collector = subprocess.Popen(['docker', 'logs', '--follow', '--timestamps', '--since', meta['started_at'], PROJECT + '-app-1'],
                                     stdout=app_log, stderr=subprocess.STDOUT)
        try:
            process = subprocess.run([sys.executable, str(ROOT / 'soak.py'), *phase2.SOAK, '--evidence', str(evidence)],
                                     cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
            status = process.returncode
            capture_ok = collector.poll() is None
            status = status or int(not capture_ok)
        finally:
            collector.terminate()
            collector.wait(timeout=15)
    after = same_candidate(build)
    stable = after['soak'] == proof['soak']
    status = status or int(not stable)
    meta.update(finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), exit_code=status,
                identities_after=after, continuous_log_capture=capture_ok, soak_process_unchanged=stable)
    (evidence / 'RUN.json').write_text(json.dumps(meta, indent=2) + '\n')
    return status


def cleanup():
    owner = json.loads((ROOT / 'OWNER.json').read_text())
    if owner['project'] != PROJECT or Path(owner['evidence']).is_relative_to(ROOT):
        raise RuntimeError('refusing cleanup with invalid ownership or evidence inside the temporary directory')
    subprocess.run(COMPOSE + ['down', '--volumes', '--remove-orphans'], cwd=ROOT, env=runtime_environment(), check=True)
    shutil.rmtree(ROOT)  # Only this fixed, owned temporary tree; evidence is outside.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['plan', 'start', 'run', 'cleanup'], nargs='?', default='plan')
    parser.add_argument('--build', type=Path)
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--execute', action='store_true')
    args = parser.parse_args()
    if args.action == 'plan' or not args.execute:
        proposed = plan()
        print(json.dumps(proposed, indent=2))
        return 0 if proposed['ready'] else 1
    with (HERE / '.soak.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.action == 'cleanup':
            cleanup()
            return 0
        if not args.build or not args.evidence:
            parser.error('--build and --evidence are required')
        evidence = args.evidence.resolve()
        if evidence.is_relative_to(ROOT):
            parser.error('evidence must be outside the temporary soak-stack directory')
        build = json.loads(args.build.read_text())
        if args.action == 'start':
            start(build, evidence)
            return 0
        owner = json.loads((ROOT / 'OWNER.json').read_text())
        if owner['build'] != build or owner['evidence'] != str(evidence):
            raise RuntimeError('build or evidence does not match the started stack')
        return run(build, evidence)


if __name__ == '__main__':
    raise SystemExit(main())
