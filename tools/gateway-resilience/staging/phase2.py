#!/usr/bin/env python3
"""Prepare or execute the released candidate's serial validation stages.

Without --execute, only print the plan. Resources is always read-only.
Run from the staging project; .env.real is never loaded.
"""

import argparse
import ast
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.request

HERE = Path(__file__).resolve().parent
PROJECT = 'xy2api-staging'
APP = PROJECT + '-app-1'
COMPOSE = ['docker', 'compose', '-p', PROJECT]
SOAK = ['--hours', '3', '--cycle-minutes', '30', '--min', '20', '--max', '50', '--settle-minutes', '10']


def capture(*args):
    return subprocess.check_output(args, cwd=HERE, text=True).strip()


def ids():
    tree = ast.parse((HERE / 'scenarios.py').read_text())
    result = [d.args[0].value for n in tree.body if isinstance(n, ast.FunctionDef) for d in n.decorator_list
              if isinstance(d, ast.Call) and isinstance(d.func, ast.Name) and d.func.id == 'scenario']
    if len(result) != 71 or len(set(result)) != 71:
        raise RuntimeError('expected exactly 71 unique scenarios')
    return result


def resources():
    memory = {line.split(':')[0]: int(line.split()[1]) * 1024 for line in Path('/proc/meminfo').read_text().splitlines()}
    disk = shutil.disk_usage(HERE)
    # Allow for build layers, synthetic upgrade DB/restore and soak logs.
    return dict(at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                disk_free_bytes=disk.free, memory_available_bytes=memory['MemAvailable'],
                minimum_disk_bytes=30 * 2**30, minimum_memory_bytes=4 * 2**30,
                load_average=os.getloadavg(),
                enough=disk.free >= 30 * 2**30 and memory['MemAvailable'] >= 4 * 2**30)


def load_environment():
    env = {k: v for k, v in os.environ.items() if not k.startswith(('REAL1_', 'REAL2_', 'REAL3_'))}
    raw = subprocess.check_output(['bash', '-c', 'set -a; source .env; env -0'], cwd=HERE, env=env)
    for item in raw.split(b'\0'):
        if b'=' in item:
            k, v = item.split(b'=', 1)
            os.environ[k.decode()] = v.decode()
    for k in list(os.environ):
        if k.startswith(('REAL1_', 'REAL2_', 'REAL3_')):
            del os.environ[k]


def identity(build):
    running = json.loads(capture('docker', 'inspect', APP))[0]
    current = capture('docker', 'image', 'inspect', '-f', '{{.Id}}', build['image'])
    binary = capture('docker', 'exec', APP, 'sha256sum', '/app/xy2api').split()[0]
    if running['Image'] != build['image_id'] or current != build['image_id'] or binary != build['binary_sha256']:
        raise RuntimeError('running app or image does not match BUILD.json')
    return dict(container_id=running['Id'], image_id=current, binary_sha256=binary)


def wait_ready():
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen('http://127.0.0.1:18210/ready', timeout=3) as response:
                if json.load(response).get('status') == 'ready':
                    return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError('app did not become ready')


def logged(command, path):
    with path.open('x') as log:
        process = subprocess.Popen(command, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            log.write(line)
            log.flush()
            print(line, end='', flush=True)
        return process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('stage', choices=['resources', 'scenarios', 'upgrade', 'soak'])
    parser.add_argument('--build', type=Path, help='BUILD.json from the unique released candidate')
    parser.add_argument('--evidence', type=Path, help='new directory; existing evidence is never overwritten')
    parser.add_argument('--execute', action='store_true')
    args = parser.parse_args()
    scenario_ids = ids()
    if args.stage == 'resources':
        data = resources()
        if args.evidence:
            args.evidence.mkdir(parents=True, exist_ok=False)
            (args.evidence / 'RESOURCES.json').write_text(json.dumps(data, indent=2) + '\n')
        print(json.dumps(data, indent=2))
        return 0 if data['enough'] else 1
    if not args.execute:
        print(json.dumps(dict(stage=args.stage, mode='plan-only', scenario_count=len(scenario_ids),
                              scenario_ids=scenario_ids, scenario_parallelism=1,
                              soak_arguments=SOAK, upgrade_from='0.4.3',
                              upgrade_profile='resilience-v2'), indent=2))
        return 0
    if not args.build or not args.evidence:
        parser.error('--execute requires --build and --evidence')
    load_environment()
    build = json.loads(args.build.read_text())
    os.environ['APP_IMAGE'] = build['image']
    with (HERE / '.phase2.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = identity(build)
        wait_ready()
        args.evidence = args.evidence.resolve()
        args.evidence.mkdir(parents=True, exist_ok=False)
        meta = dict(build=build, stage=args.stage, before=before,
                    started_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    scripts={p: hashlib.sha256((HERE / p).read_bytes()).hexdigest()
                             for p in ('scenarios.py', 'fixtures.py', 'soak.py', 'upgrade_drill.py', 'phase2.py')})
        (args.evidence / 'RUN.json').write_text(json.dumps(meta, indent=2) + '\n')
        if args.stage == 'scenarios':
            if not os.environ.get('STG_CF_HOST') or not os.environ.get('STG_DIRECT_HOST'):
                raise RuntimeError('all 71 cases require the existing staging CF and direct hosts')
            status = logged([sys.executable, str(HERE / 'scenarios.py'), '--parallel', '1',
                             '--evidence', str(args.evidence)], args.evidence / 'run.log')
            records = [json.loads((args.evidence / (sid + '.json')).read_text()) for sid in scenario_ids]
            summary = dict(total=len(records), passed=sum(r['ok'] for r in records),
                           failed=[r['id'] for r in records if not r['ok']], records=records, build=build)
            (args.evidence / 'SUITE.json').write_text(json.dumps(summary, indent=2) + '\n')
            status = status or int(bool(summary['failed']))
        elif args.stage == 'upgrade':
            os.environ.update(UPGRADE_FROM='ghcr.io/liulixin-lex/xy2api:0.4.3', UPGRADE_TO=build['image'],
                              UPGRADE_PROFILE='resilience-v2', UPGRADE_EVIDENCE=str(args.evidence), ACCEPT_ADMIN_COMPLIANCE='1')
            status = logged([sys.executable, str(HERE / 'upgrade_drill.py')], args.evidence / 'run.log')
        else:
            os.environ['APP_GODEBUG'] = 'gctrace=1'
            subprocess.run(COMPOSE + ['up', '-d', '--no-deps', 'app'], cwd=HERE, check=True)
            wait_ready()
            identity(build)
            status = logged([sys.executable, str(HERE / 'soak.py'), *SOAK,
                             '--evidence', str(args.evidence)], args.evidence / 'run.log')
        meta.update(after=identity(build), exit_code=status,
                    finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
        (args.evidence / 'RUN.json').write_text(json.dumps(meta, indent=2) + '\n')
        return status


if __name__ == '__main__':
    raise SystemExit(main())
