#!/usr/bin/env python3
"""Standalone XY2API OpenAI login/TOTP worker. All console errors are fixed codes.

Protocol/journal provenance: ranxi2001/sub2api 12a49ef4 (LGPL-3.0). The worker API,
readiness, claim-token leases and offline preflight are independently implemented.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import fcntl
import hashlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import uuid

try:
    from .openai_totp_rotation import Journal, RotationError, child_environment, login_web, rotate, run_login
except ImportError:
    from openai_totp_rotation import Journal, RotationError, child_environment, login_web, rotate, run_login

MAX_BYTES = 2 * 1024 * 1024
API_ROOT = '/api/v1/internal/openai-totp'


def number_env(name, default, low, high):
    try:
        value = float(os.getenv(name, default))
        if not math.isfinite(value) or not low <= value <= high:
            raise ValueError()
        return value
    except ValueError:
        raise RotationError('invalid_configuration') from None


@dataclass(frozen=True)
class WorkerConfig:
    base_url: str
    worker_secret: str
    worker_id: str
    tosub2_root: Path
    journal_dir: Path
    node: str
    poll_seconds: float = 5
    heartbeat_seconds: float = 10
    request_timeout: float = 10

    @classmethod
    def from_env(cls):
        base_url = os.getenv('XY2API_OPENAI_TOTP_API_URL', 'http://127.0.0.1:8080').strip().rstrip('/')
        try:
            parsed = urlsplit(base_url)
            host, port = parsed.hostname, parsed.port
            if (parsed.scheme not in ('http', 'https') or not host or parsed.username is not None
                    or parsed.password is not None or parsed.query or parsed.fragment
                    or parsed.path or any(c.isspace() or ord(c) < 32 for c in base_url)
                    or (port is not None and port <= 0)):
                raise ValueError()
            if parsed.scheme == 'http' and host.lower() not in ('localhost', '127.0.0.1', '::1'):
                raise ValueError()
        except ValueError:
            raise RotationError('invalid_api_url') from None
        secret = os.getenv('XY2API_OPENAI_TOTP_WORKER_SECRET', '').strip()
        if len(secret) < 32 or any(ord(c) < 33 or ord(c) > 126 for c in secret):
            raise RotationError('invalid_worker_secret')
        worker_id = os.getenv('XY2API_OPENAI_TOTP_WORKER_ID', '').strip() or 'totp-' + uuid.uuid4().hex
        if not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', worker_id):
            raise RotationError('invalid_worker_id')
        root_raw = os.getenv('TOSUB2_ROOT', '')
        journal_raw = os.getenv('OPENAI_TOTP_JOURNAL_DIR', '')
        if not root_raw or not Path(root_raw).is_absolute():
            raise RotationError('runtime_unavailable')
        if not journal_raw or not Path(journal_raw).is_absolute():
            raise RotationError('journal_unavailable')
        node = shutil.which(os.getenv('NODE_EXECUTABLE', 'node'))
        if not node:
            raise RotationError('runtime_unavailable')
        return cls(base_url, secret, worker_id, Path(root_raw), Path(journal_raw), node,
                   number_env('XY2API_OPENAI_TOTP_POLL_SECONDS', '5', 1, 60),
                   number_env('XY2API_OPENAI_TOTP_HEARTBEAT_SECONDS', '10', 1, 15),
                   number_env('XY2API_OPENAI_TOTP_REQUEST_TIMEOUT', '10', 1, 15))


def preflight(config):
    """No API registration until Python, Node, runtime and durable journal pass."""
    try:
        if importlib.metadata.version('curl_cffi') != '0.15.0':
            raise ValueError()
        from curl_cffi import Curl
        curl = Curl()
        try:
            if curl.impersonate('chrome146') != 0:
                raise ValueError()
        finally:
            curl.close()
        manifest = json.loads((Path(__file__).parent / 'openai-totp-runtime/source.json').read_text())
        for name, expected in manifest['sha256'].items():
            source = config.tosub2_root / name
            if not source.is_file() or hashlib.sha256(source.read_bytes()).hexdigest() != expected:
                raise ValueError()
        env = child_environment(config)
        for script in ('protocol-login.mjs', 'tls-transport.mjs'):
            result = subprocess.run([config.node, '--check', str(config.tosub2_root / 'src' / script)],
                                    capture_output=True, env=env, timeout=15, check=False)
            if result.returncode:
                raise ValueError()
        result = subprocess.run([config.node, str(Path(__file__).with_name('openai_totp_preflight.mjs'))],
                                capture_output=True, text=True, env=env, timeout=20, check=False)
        if result.returncode or json.loads(result.stdout) != {'ok': True}:
            raise ValueError()
    except Exception:
        raise RotationError('runtime_unavailable') from None
    return Journal(config)  # check includes a real write, file fsync and directory fsync.


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class WorkerAPI:
    def __init__(self, config):
        self.config = config

    def post(self, route, payload):
        try:
            body = json.dumps(payload, allow_nan=False).encode()
            if len(body) > MAX_BYTES:
                raise ValueError()
            request = Request(self.config.base_url + API_ROOT + route, data=body, method='POST', headers={
                'Authorization': 'Bearer ' + self.config.worker_secret,
                'Content-Type': 'application/json', 'Accept': 'application/json',
                'Cache-Control': 'no-store',
            })
            # Ignore inherited proxy env; never redirect the bearer or credentials.
            with build_opener(ProxyHandler({}), NoRedirect()).open(request, timeout=self.config.request_timeout) as response:
                if response.status != 200:
                    raise ValueError()
                raw = response.read(MAX_BYTES + 1)
            if len(raw) > MAX_BYTES:
                raise ValueError()
            envelope = json.loads(raw)
            if not isinstance(envelope, dict) or type(envelope.get('code')) is not int or envelope['code'] != 0:
                raise ValueError()
            return envelope.get('data')
        except HTTPError as error:
            error.close()  # Never read/print exception bodies or server messages.
            raise RotationError('worker_api_failed') from None
        except (URLError, OSError, ValueError, TypeError):
            raise RotationError('worker_api_failed') from None

    def claim(self, *, heartbeat_only=False, active=None):
        payload = {'worker_id': self.config.worker_id, 'journal_ready': True, 'runtime_ready': True}
        if heartbeat_only:
            payload['heartbeat_only'] = True
            if active is not None:
                payload.update(task_id=active.claim['task_id'], claim_token=active.claim['claim_token'])
        data = self.post('/claim', payload)
        if heartbeat_only:
            if data is not None:
                raise RotationError('invalid_heartbeat_response')
            return None
        return validate_claim(data)

    def totp_recover(self, task_id, secret):
        return self.post('/recover', {'task_id': task_id, 'secret': secret})


def validate_claim(claim):
    if claim is None:
        return None
    if not isinstance(claim, dict):
        raise RotationError('invalid_claim')
    valid_id = lambda value: type(value) is int and 0 < value <= 2**53 - 1
    if (not valid_id(claim.get('task_id')) or claim.get('operation') not in ('login', 'totp_rotation')
            or not isinstance(claim.get('claim_token'), str) or not 16 <= len(claim['claim_token']) <= 512):
        raise RotationError('invalid_claim')
    for field in ('login_email', 'password', 'totp_secret'):
        value = claim.get(field)
        if not isinstance(value, str) or not value or len(value) > 16384 or '\0' in value:
            raise RotationError('invalid_claim')
    if claim['operation'] == 'totp_rotation':
        if not valid_id(claim.get('account_id')) or claim.get('action') not in ('rotate', 'verify_new', 'verify_old'):
            raise RotationError('invalid_claim')
        if claim['action'] == 'verify_new' and (not isinstance(claim.get('candidate_secret'), str) or not claim['candidate_secret']):
            raise RotationError('invalid_claim')
        if claim.get('candidate_secret') is not None and not isinstance(claim['candidate_secret'], str):
            raise RotationError('invalid_claim')
    proxy = claim.get('proxy_url')
    if proxy:
        try:
            url = urlsplit(proxy)
            if url.scheme not in ('http', 'https', 'socks5', 'socks5h') or not url.hostname or url.fragment:
                raise ValueError()
            _ = url.port
        except (ValueError, TypeError, AttributeError):
            raise RotationError('invalid_claim') from None
    return claim


class ActiveTask:
    def __init__(self, api, claim):
        self.api, self.config, self.claim = api, api.config, claim
        self.cancel = threading.Event()
        self.last_renewed = time.monotonic()

    def guard(self):
        # Independently fail closed if a hung heartbeat thread stops renewing.
        if self.cancel.is_set() or time.monotonic() - self.last_renewed > 45:
            self.cancel.set()
            raise RotationError('lease_lost')

    def totp_phase(self, task_id, phase, secret=''):
        self.guard()
        payload = {'claim_token': self.claim['claim_token'], 'phase': phase}
        if secret:
            payload['secret'] = secret
        try:
            return self.api.post(f'/{task_id}/phase', payload)
        except Exception:
            self.cancel.set()
            raise

    def totp_finish(self, task_id, success, error_code='', *, session=None):
        if success:
            self.guard()
        code = {'totp_rejected': 'mfa_rejected', 'password_rejected': 'login_failed',
                'lease_lost': 'worker_timeout'}.get(error_code, error_code)
        if code and code not in {'login_failed', 'mfa_rejected', 'verification_failed', 'runtime_unavailable',
                                'journal_unavailable', 'worker_timeout', 'identity_mismatch'}:
            code = 'operation_failed'
        payload = {'claim_token': self.claim['claim_token'], 'success': success, 'error_code': code}
        if session is not None:
            payload['session'] = session
        return self.api.post(f'/{task_id}/finish', payload)

    def login(self, config, claim, secret):
        self.guard()
        return login_web(config, claim, secret, cancel=self.cancel, guard=self.guard)

    def wait(self, seconds):
        if self.cancel.wait(seconds):
            raise RotationError('lease_lost')
        self.guard()


class Worker:
    def __init__(self, config, *, api=None, check=preflight):
        self.config, self.api, self.check = config, api or WorkerAPI(config), check
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.active = None

    def heartbeat_once(self):
        with self.lock:
            active = self.active
        try:
            self.check(self.config)
            self.api.claim(heartbeat_only=True, active=active)
            if active:
                active.last_renewed = time.monotonic()
        except Exception:
            if active:
                active.cancel.set()
            raise RotationError('heartbeat_failed') from None

    def heartbeat_loop(self):
        while not self.stop.wait(self.config.heartbeat_seconds):
            try:
                self.heartbeat_once()
            except Exception:
                print('openai_totp_worker: heartbeat_failed', flush=True)

    def run_once(self):
        journal = self.check(self.config)
        journal.recover(self.api)
        claim = self.api.claim()
        if claim is None:
            return False
        active = ActiveTask(self.api, claim)
        with self.lock:
            self.active = active
        try:
            if self.stop.is_set():
                active.cancel.set()
            if claim['operation'] == 'totp_rotation':
                rotate(active, claim, login=active.login, wait=active.wait)
            else:
                run_login(active, claim, login=active.login)
        finally:
            with self.lock:
                self.active = None
        return True

    def shutdown(self, *_):
        self.stop.set()
        with self.lock:
            if self.active:
                self.active.cancel.set()

    def run(self):
        self.check(self.config)
        self.heartbeat_once()
        heartbeat = threading.Thread(target=self.heartbeat_loop, name='totp-heartbeat', daemon=True)
        heartbeat.start()
        try:
            while not self.stop.is_set():
                try:
                    self.run_once()
                except Exception:
                    print('openai_totp_worker: task_poll_failed', flush=True)
                self.stop.wait(self.config.poll_seconds)
        finally:
            self.shutdown()
            heartbeat.join(timeout=45)


def acquire_journal_lock(config):
    # One process per journal volume, preventing concurrent key/record replacement.
    Journal(config)
    try:
        fd = os.open(config.journal_dir / 'worker.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid() or info.st_nlink != 1:
                raise OSError()
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except Exception:
            os.close(fd)
            raise
    except Exception:
        raise RotationError('journal_locked') from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='offline dependency and fsync check; no API/HTTP login')
    args = parser.parse_args(argv)
    fd = None
    try:
        os.umask(0o077)
        config = WorkerConfig.from_env()
        preflight(config)
        fd = acquire_journal_lock(config)
        if args.check:
            print('openai_totp_worker: preflight_ok')
            return 0
        worker = Worker(config)
        signal.signal(signal.SIGINT, worker.shutdown)
        signal.signal(signal.SIGTERM, worker.shutdown)
        worker.run()
        return 0
    except KeyboardInterrupt:
        return 0
    except Exception:
        print('openai_totp_worker: startup_failed', flush=True)
        return 1
    finally:
        if fd is not None:
            os.close(fd)


if __name__ == '__main__':
    raise SystemExit(main())
