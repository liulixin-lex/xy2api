"""Owned OpenAI MFA protocol, adapted from ranxi2001/sub2api at 12a49ef4.

Source: tools/openai_totp_rotation.py (LGPL-3.0). API/lease integration and runner
isolation are XY2API changes. No upstream mutation is automatically retried.
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import sys
import tempfile
import time


class RotationError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def totp(secret, at=None):
    try:
        value = ''.join(secret.upper().split()).rstrip('=')
        if not 16 <= len(value) <= 128:
            raise ValueError()
        key = base64.b32decode(value + '=' * (-len(value) % 8))
        counter = int((time.time() if at is None else at) // 30)
        digest = hmac.new(key, struct.pack('>Q', counter), hashlib.sha1).digest()
        offset = digest[-1] & 15
        return f'{(struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}'
    except Exception:
        raise RotationError('enrollment_failed') from None


def child_environment(config):
    # Do not pass the worker bearer, journal key paths, inherited proxies, Node
    # preload flags, or alternate upstream URLs to the credential subprocess.
    env = {k: v for k, v in os.environ.items() if k in (
        'PATH', 'LANG', 'SYSTEMROOT', 'TEMP', 'TMP', 'LD_LIBRARY_PATH',
        'SSL_CERT_FILE', 'CURL_CA_BUNDLE',
    )}
    env.update(TOSUB2_PYTHON=sys.executable, TOSUB2_ROOT=str(config.tosub2_root))
    return env


def guarded_run(command, *, timeout, cancel=None, guard=lambda: None, **kwargs):
    """Kill the entire Node -> Python group on timeout or lost lease."""
    with subprocess.Popen(command, start_new_session=True, **kwargs) as child:
        deadline = time.monotonic() + timeout
        try:
            while True:
                guard()
                if cancel is not None and cancel.is_set():
                    raise RotationError('lease_lost')
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise RotationError('login_failed')
                try:
                    output, errors = child.communicate(timeout=min(0.25, remaining))
                    return subprocess.CompletedProcess(command, child.returncode, output, errors)
                except subprocess.TimeoutExpired:
                    continue
        finally:
            # Also stop children that survive a normal/failed runner exit.
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()


class Journal:
    def __init__(self, config):
        self.config = config
        self.call({'action': 'check'})

    def call(self, payload):
        try:
            child = subprocess.run(
                [self.config.node, str(Path(__file__).with_name('openai_totp_journal.mjs'))],
                input=json.dumps(payload), capture_output=True, text=True, timeout=30,
                env={**child_environment(self.config),
                     'OPENAI_TOTP_JOURNAL_DIR': str(self.config.journal_dir)}, check=False,
            )
            if child.returncode or len(child.stdout) > 2 * 1024 * 1024:
                raise ValueError()
            return json.loads(child.stdout)
        except Exception:
            raise RotationError('journal_unavailable') from None

    def save(self, claim, candidate='', session_id=''):
        self.call({'action': 'write', 'record': {
            'task_id': claim['task_id'], 'account_id': claim.get('account_id'),
            'email': claim['login_email'], 'password': claim['password'],
            'previous': claim['totp_secret'], 'candidate': candidate, 'session_id': session_id,
        }})

    def acknowledge(self, task_id):
        self.call({'action': 'acknowledge', 'task_id': task_id})

    def recover(self, api):
        for task_id in self.call({'action': 'pending'}):
            record = self.call({'action': 'read', 'task_id': task_id})
            if isinstance(record.get('candidate'), str) and record['candidate']:
                try:
                    api.totp_recover(task_id, record['candidate'])
                    self.acknowledge(task_id)
                except Exception:
                    pass  # Retain encrypted material; no enroll/activate replay.


def login_web(config, claim, secret, *, cancel=None, guard=lambda: None):
    """Fresh process and cookie jar. Only the explicit MFA marker means rejection."""
    with tempfile.TemporaryDirectory(prefix='xy2api-totp-login-') as folder:
        session_file = Path(folder) / 'session.json'
        env = child_environment(config)
        env.update(CHATGPT_LOGIN_PASSWORD=claim['password'], CHATGPT_TOTP_SECRET=secret,
                   CHATGPT_PROXY_MAX_ATTEMPTS='1', CHATGPT_PROXY_URL=claim.get('proxy_url') or '')
        command = [config.node, str(Path(__file__).with_name('openai_totp_runner.mjs')),
                   '--email', claim['login_email'], '--web-only', '--output-mode', 'session',
                   '--no-sub2api-export', '--out', str(session_file)]
        try:
            result = guarded_run(command, cwd=folder, env=env, stdin=subprocess.DEVNULL,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                 timeout=180, cancel=cancel, guard=guard)
            lines = (result.stdout + '\n' + result.stderr).splitlines()
            if '[warn] The code generated from the configured 2FA key was rejected; enter a current code manually.' in lines:
                return None
            if '[warn] Password was rejected. Enter it again, or q to quit.' in lines:
                raise RotationError('password_rejected')
            if result.returncode or not session_file.is_file() or session_file.stat().st_size > 1024 * 1024:
                raise RotationError('login_failed')
            data = json.loads(session_file.read_text())
            if not isinstance(data, dict) or data.get('web', {}).get('mfaVerified') is not True:
                raise RotationError('verification_failed')
            return data
        except RotationError:
            raise
        except Exception:
            raise RotationError('login_failed') from None


class WebSession:
    def __init__(self, config, claim, secret, *, login=login_web):
        data = login(config, claim, secret)
        if data is None:
            raise RotationError('totp_rejected')
        from curl_cffi.requests import Session
        self.client = Session(impersonate='chrome146', trust_env=False,
                              proxy=claim.get('proxy_url') or None)
        try:
            self.client.headers.update({'Origin': 'https://chatgpt.com', 'Referer': 'https://chatgpt.com/'})
            for cookie in data.get('cookies', []):
                domain = str(cookie.get('domain', '')).lstrip('.')
                if domain in ('chatgpt.com', 'auth.openai.com', 'openai.com'):
                    self.client.cookies.set(cookie['name'], cookie['value'],
                                            domain=cookie['domain'], path=cookie.get('path', '/'))
            payload = self.request('GET', '/api/auth/session')
            email = payload.get('user', {}).get('email')
            if not isinstance(email, str) or email.strip().casefold() != claim['login_email'].strip().casefold():
                raise RotationError('identity_mismatch')
            token = payload.get('accessToken')
            if not isinstance(token, str) or not token or not payload.get('expires'):
                raise RotationError('login_failed')
            self.auth_session_payload = payload
            self.client.headers.update({'Authorization': 'Bearer ' + token,
                                        'Origin': 'https://chatgpt.com', 'Referer': 'https://chatgpt.com/',
                                        'oai-device-id': data.get('web', {}).get('deviceId', '')})
        except Exception:
            self.close()
            raise

    def request(self, method, route, payload=None):
        try:
            response = self.client.request(method, 'https://chatgpt.com' + route, json=payload,
                                           timeout=30, allow_redirects=False)
            if response.status_code != 200:
                raise RotationError('upstream_rejected')
            if len(response.content) > 1024 * 1024:
                raise RotationError('invalid_response')
            data = response.json()
            if not isinstance(data, dict):
                raise RotationError('invalid_response')
            return data
        except RotationError:
            raise
        except Exception:
            raise RotationError('upstream_unavailable') from None

    def close(self):
        self.client.close()


def verify_pair(config, claim, candidate, action, login, *, guard=lambda: None, wait=time.sleep):
    accepted, rejected = ((claim['totp_secret'], candidate) if action == 'verify_old'
                          else (candidate, claim['totp_secret']))
    guard()
    if not accepted or login(config, claim, accepted) is None:
        raise RotationError('verification_failed')
    if rejected:
        # Next time window prevents mistaking OTP replay rejection for revocation.
        wait(31 - time.time() % 30)
        guard()
        if login(config, claim, rejected) is not None:
            raise RotationError('verification_failed')


def rotate(api, claim, *, session_factory=WebSession, login=login_web,
           journal_factory=Journal, wait=time.sleep):
    task, action = claim['task_id'], claim['action']
    candidate, session, stage = claim.get('candidate_secret', ''), None, 'login_failed'
    try:
        api.guard()
        journal = journal_factory(api.config)
        if action == 'rotate':
            journal.save(claim)
            session = session_factory(api.config, claim, claim['totp_secret'], login=login)
            api.totp_phase(task, 'enrolling')
            stage = 'enrollment_failed'
            api.guard()
            data = session.request('POST', '/backend-api/accounts/mfa/enroll', {'factor_type': 'totp'})
            candidate, session_id = data.get('secret', ''), data.get('session_id', '')
            # Preserve even malformed returned candidate BEFORE validation/lease check.
            journal.save(claim, candidate, session_id)
            if not isinstance(candidate, str) or not isinstance(session_id, str) or not candidate or not session_id:
                raise RotationError('enrollment_failed')
            normalized = lambda value: ''.join(value.upper().split()).rstrip('=')
            if normalized(candidate) == normalized(claim['totp_secret']):
                raise RotationError('enrollment_failed')
            totp(candidate)  # Validate before persisting as a candidate in the database.
            api.totp_phase(task, 'prepared', candidate)
            journal.acknowledge(task)
            api.totp_phase(task, 'activating')
            stage = 'activation_unconfirmed'
            api.guard()
            activation = session.request('POST', '/backend-api/accounts/mfa/user/activate_enrollment',
                                         {'factor_type': 'totp', 'session_id': session_id, 'code': totp(candidate)})
            if activation.get('success') is not True:
                raise RotationError('activation_unconfirmed')
            api.totp_phase(task, 'verifying')
        elif action in ('verify_new', 'verify_old'):
            api.totp_phase(task, 'verify_recovery')
        else:
            raise RotationError('worker_failed')
        stage = 'verification_failed'
        verify_pair(api.config, claim, candidate, action, login, guard=api.guard, wait=wait)
        api.totp_finish(task, True)
    except Exception:
        try:
            api.totp_finish(task, False, stage)
        except Exception:
            pass  # Expired lease is reconciled as uncertain by the server.
    finally:
        if session is not None:
            try:
                session.close()
            except Exception:
                pass


def run_login(api, claim, *, session_factory=WebSession, login=login_web):
    session = None
    try:
        api.guard()
        session = session_factory(api.config, claim, claim['totp_secret'], login=login)
        api.totp_finish(claim['task_id'], True, session=session.auth_session_payload)
    except Exception as error:
        code = error.code if isinstance(error, RotationError) and error.code in {
            'password_rejected', 'totp_rejected', 'identity_mismatch', 'verification_failed', 'lease_lost',
        } else 'login_failed'
        try:
            api.totp_finish(claim['task_id'], False, code)
        except Exception:
            pass
    finally:
        if session is not None:
            try:
                session.close()
            except Exception:
                pass
