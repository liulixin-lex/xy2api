"""Standalone worker contract tests against mocks and a loopback-only API."""
import contextlib
from dataclasses import replace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import os
from pathlib import Path
import shutil
import socket
import tempfile
import threading
import time
import unittest
from unittest.mock import Mock, patch

from tools.openai_totp_rotation import RotationError
from tools.openai_totp_worker import (
    API_ROOT, MAX_BYTES, ActiveTask, Worker, WorkerAPI, WorkerConfig,
    acquire_journal_lock, main, preflight, validate_claim,
)


def config(root=Path('/synthetic')):
    return WorkerConfig('http://127.0.0.1:8080', 's' * 32, 'test-worker', root, root / 'journal', shutil.which('node'))


def claim(operation='totp_rotation'):
    return dict(operation=operation, task_id=7, account_id=42, action='rotate' if operation == 'totp_rotation' else 'login',
                login_email='owner@example.test', password='synthetic-password', totp_secret='JBSWY3DPEHPK3PXP',
                claim_token='token-' + 'x' * 32)


@contextlib.contextmanager
def mock_server(reply):
    calls = []
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            raw = self.rfile.read(int(self.headers.get('Content-Length', '0')))
            calls.append({'path': self.path, 'headers': dict(self.headers), 'payload': json.loads(raw)})
            code, headers, body = reply(calls[-1])
            self.send_response(code)
            for key, value in headers.items():
                self.send_header(key, value)
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f'http://127.0.0.1:{server.server_port}', calls
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def ok(data=None):
    return 200, {'Cache-Control': 'no-store'}, json.dumps({'code': 0, 'data': data}).encode()


class ConfigTests(unittest.TestCase):
    def setUp(self):
        self.env = {'XY2API_OPENAI_TOTP_WORKER_SECRET': 's' * 32, 'TOSUB2_ROOT': '/synthetic-runtime',
                    'OPENAI_TOTP_JOURNAL_DIR': '/synthetic-journal', 'PATH': os.environ['PATH']}

    def load(self, **changes):
        with patch.dict(os.environ, {**self.env, **changes}, clear=True):
            return WorkerConfig.from_env()

    def test_defaults_and_https(self):
        self.assertEqual(self.load().base_url, 'http://127.0.0.1:8080')
        self.assertEqual(self.load(XY2API_OPENAI_TOTP_API_URL='https://api.example.test/').base_url, 'https://api.example.test')
        self.assertTrue(self.load().worker_id.startswith('totp-'))

    def test_rejects_insecure_urls_and_url_secrets(self):
        for url in ('http://api.example.test', 'http://127.1', 'ftp://localhost', 'http://user@localhost',
                    'http://127.0.0.1:0', 'https://api.example.test/path', 'https://api.example.test?x=1',
                    'http://127.0.0.1#x', 'http://[::1]:oops', 'https://api.example.test/\nfoo'):
            with self.subTest(url=url), self.assertRaises(RotationError):
                self.load(XY2API_OPENAI_TOTP_API_URL=url)

    def test_short_secret_missing_runtime_bad_interval_and_unsafe_worker_id(self):
        for field, value in (('XY2API_OPENAI_TOTP_WORKER_SECRET', 'short'), ('TOSUB2_ROOT', ''),
                             ('OPENAI_TOTP_JOURNAL_DIR', 'relative'), ('NODE_EXECUTABLE', '/missing-node'),
                             ('XY2API_OPENAI_TOTP_HEARTBEAT_SECONDS', 'NaN'),
                             ('XY2API_OPENAI_TOTP_REQUEST_TIMEOUT', '100'),
                             ('XY2API_OPENAI_TOTP_WORKER_ID', 'email@example.test')):
            with self.subTest(field=field), self.assertRaises(RotationError):
                self.load(**{field: value})

    def test_preflight_failure_does_not_contact_api(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaisesRegex(RotationError, 'runtime_unavailable'):
                preflight(config(Path(root)))

    def test_cli_error_never_prints_raw_secret(self):
        output = io.StringIO()
        with patch.dict(os.environ, self.env, clear=True), patch('tools.openai_totp_worker.preflight',
                side_effect=RuntimeError('secret-password')), contextlib.redirect_stdout(output):
            self.assertEqual(main(['--check']), 1)
        self.assertEqual(output.getvalue(), 'openai_totp_worker: startup_failed\n')


class APITests(unittest.TestCase):
    def test_bearer_claim_contract_and_heartbeat_never_claims_extra_task(self):
        with mock_server(lambda _: ok()) as (url, calls):
            api = WorkerAPI(replace(config(), base_url=url))
            api.claim(heartbeat_only=True)
            active = ActiveTask(api, claim())
            api.claim(heartbeat_only=True, active=active)
            self.assertEqual(len(calls), 2)
            self.assertEqual(calls[0]['path'], API_ROOT + '/claim')
            self.assertEqual(calls[0]['headers']['Authorization'], 'Bearer ' + 's' * 32)
            self.assertEqual(calls[0]['payload'], {'worker_id': 'test-worker', 'runtime_ready': True,
                                                  'journal_ready': True, 'heartbeat_only': True})
            self.assertEqual(calls[1]['payload']['task_id'], 7)
            self.assertEqual(calls[1]['payload']['claim_token'], active.claim['claim_token'])

    def test_phase_finish_and_recover_use_exact_contract(self):
        with mock_server(lambda _: ok()) as (url, calls):
            api = WorkerAPI(replace(config(), base_url=url))
            active = ActiveTask(api, claim())
            active.totp_phase(7, 'prepared', 'CANDIDATE')
            active.totp_finish(7, False, 'lease_lost')
            api.totp_recover(7, 'CANDIDATE')
            self.assertEqual(calls[0]['payload'], {'claim_token': active.claim['claim_token'], 'phase': 'prepared', 'secret': 'CANDIDATE'})
            self.assertEqual(calls[1]['payload']['error_code'], 'worker_timeout')
            self.assertEqual(calls[2]['payload'], {'task_id': 7, 'secret': 'CANDIDATE'})

    def test_redirect_never_forwards_bearer_or_body(self):
        with mock_server(lambda _: ok()) as (target, leaked):
            with mock_server(lambda _: (307, {'Location': target + '/leak'}, b'')) as (url, calls):
                with self.assertRaisesRegex(RotationError, '^worker_api_failed$'):
                    WorkerAPI(replace(config(), base_url=url)).claim()
                self.assertEqual(len(calls), 1)
                self.assertEqual(leaked, [])

    def test_inherited_proxy_not_used(self):
        with mock_server(lambda _: ok()) as (url, calls):
            with patch.dict(os.environ, {'HTTP_PROXY': 'http://127.0.0.1:1', 'http_proxy': 'http://127.0.0.1:1', 'NO_PROXY': ''}):
                self.assertIsNone(WorkerAPI(replace(config(), base_url=url)).claim())
            self.assertEqual(len(calls), 1)

    def test_no_retry_on_http_failure_or_secret_message(self):
        with mock_server(lambda _: (503, {}, b'password=CANDIDATE token=SECRET')) as (url, calls):
            with self.assertRaisesRegex(RotationError, '^worker_api_failed$'):
                WorkerAPI(replace(config(), base_url=url)).claim()
            self.assertEqual(len(calls), 1)

    def test_invalid_json_envelope_and_oversized_response(self):
        for body in (b'not-json', b'[]', b'{"code":false}', b'{"code":9,"message":"secret"}', b'x' * (MAX_BYTES + 1)):
            with self.subTest(length=len(body)), mock_server(lambda _: (200, {}, body)) as (url, _):
                with self.assertRaises(RotationError):
                    WorkerAPI(replace(config(), base_url=url)).claim()

    def test_claim_validation_never_defaults_to_unsafe_operation(self):
        self.assertEqual(validate_claim(claim())['task_id'], 7)
        for changes in ({'task_id': True}, {'task_id': '../file'}, {'operation': 'other'}, {'claim_token': ''},
                        {'password': None}, {'action': 'verify_new'}, {'account_id': None},
                        {'proxy_url': 'file:///etc/passwd'}):
            with self.subTest(changes=changes), self.assertRaises(RotationError):
                validate_claim({**claim(), **changes})

    def test_non_null_heartbeat_result_is_protocol_error(self):
        with mock_server(lambda _: ok(claim())) as (url, _), self.assertRaises(RotationError):
            WorkerAPI(replace(config(), base_url=url)).claim(heartbeat_only=True)

    def test_verify_old_without_candidate_is_a_valid_recovery_claim(self):
        value = {**claim(), 'action': 'verify_old'}
        self.assertEqual(validate_claim(value), value)


class LeaseTests(unittest.TestCase):
    def setUp(self):
        self.api = Mock(config=config())
        self.worker = Worker(config(), api=self.api, check=Mock())
        self.active = ActiveTask(self.api, claim())
        self.worker.active = self.active

    def test_holder_heartbeat_renews_without_claiming(self):
        self.api.claim.return_value = None
        self.active.last_renewed = 0
        self.worker.heartbeat_once()
        self.api.claim.assert_called_once_with(heartbeat_only=True, active=self.active)
        self.assertGreater(self.active.last_renewed, 0)

    def test_failed_heartbeat_or_preflight_cancels_active_task(self):
        for target in (self.api.claim, self.worker.check):
            target.side_effect = RuntimeError('secret')
            with self.assertRaisesRegex(RotationError, '^heartbeat_failed$'):
                self.worker.heartbeat_once()
            self.assertTrue(self.active.cancel.is_set())
            with self.assertRaisesRegex(RotationError, 'lease_lost'):
                self.active.guard()
            target.side_effect = None

    def test_phase_failure_loses_lease_and_next_phase_is_blocked(self):
        self.api.post.side_effect = RuntimeError()
        with self.assertRaises(RuntimeError):
            self.active.totp_phase(7, 'enrolling')
        with self.assertRaisesRegex(RotationError, 'lease_lost'):
            self.active.totp_phase(7, 'activating')
        self.assertEqual(self.api.post.call_count, 1)

    def test_stale_local_renewal_is_fail_closed(self):
        self.active.last_renewed = time.monotonic() - 46
        with self.assertRaisesRegex(RotationError, 'lease_lost'):
            self.active.guard()

    def test_shutdown_interrupts_wait_and_runner(self):
        self.worker.shutdown()
        with self.assertRaisesRegex(RotationError, 'lease_lost'):
            self.active.wait(30)

    def test_rotation_and_login_dispatch_only_one_claim(self):
        self.worker.active = None
        for operation, handler in (('totp_rotation', 'rotate'), ('login', 'run_login')):
            self.api.claim.reset_mock()
            self.api.claim.return_value = claim(operation)
            with patch('tools.openai_totp_worker.' + handler) as run:
                self.assertTrue(self.worker.run_once())
                run.assert_called_once()
                self.api.claim.assert_called_once_with()
                self.assertIsNone(self.worker.active)

    def test_heartbeat_thread_during_blocking_task_never_claims(self):
        self.worker.config = replace(config(), heartbeat_seconds=0.01)
        self.api.claim.return_value = None
        heartbeat = threading.Thread(target=self.worker.heartbeat_loop)
        heartbeat.start()
        try:
            deadline = time.monotonic() + 1
            while self.api.claim.call_count < 2 and time.monotonic() < deadline:
                time.sleep(0.005)
            self.assertGreaterEqual(self.api.claim.call_count, 2)
            self.assertTrue(all(c.kwargs.get('heartbeat_only') is True for c in self.api.claim.call_args_list))
        finally:
            self.worker.shutdown()
            heartbeat.join()

    def test_bad_preflight_never_registers_readiness_or_claims(self):
        self.worker.check.side_effect = RotationError('journal_unavailable')
        with self.assertRaises(RotationError):
            self.worker.run_once()
        self.api.claim.assert_not_called()

    def test_early_recover_409_keeps_journal_and_does_not_block_claim(self):
        from tools.openai_totp_rotation import Journal
        journal = object.__new__(Journal)
        journal.call = Mock(side_effect=[[7], {'candidate': 'SYNTHETIC-CANDIDATE'}])
        self.api.totp_recover.side_effect = RotationError('worker_api_failed')
        self.api.claim.return_value = None
        self.worker.check.return_value = journal
        self.assertFalse(self.worker.run_once())
        self.api.claim.assert_called_once_with()
        self.assertFalse(any(c.args[0].get('action') == 'acknowledge' for c in journal.call.call_args_list))


class JournalLockTests(unittest.TestCase):
    def test_same_journal_refuses_second_worker(self):
        with tempfile.TemporaryDirectory() as root:
            cfg = config(Path(root))
            fd = acquire_journal_lock(cfg)
            try:
                with self.assertRaisesRegex(RotationError, 'journal_locked'):
                    acquire_journal_lock(cfg)
            finally:
                os.close(fd)
            fd = acquire_journal_lock(cfg)
            os.close(fd)


if __name__ == '__main__':
    unittest.main()
