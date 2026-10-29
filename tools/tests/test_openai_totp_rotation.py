"""Offline failure injection; no real credentials or upstream HTTP."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from tools.openai_totp_rotation import (
    Journal, RotationError, WebSession, guarded_run, login_web, rotate, run_login, totp,
)

OLD = 'JBSWY3DPEHPK3PXP'
NEW = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'


class RotationTests(unittest.TestCase):
    def setUp(self):
        self.events = []
        self.claim = dict(task_id=7, account_id=42, action='rotate', login_email='owner@example.test',
                          password='synthetic-password', totp_secret=OLD)
        self.api = Mock(config=SimpleNamespace(tosub2_root=Path('/fake'), node='node'))
        self.api.totp_phase.side_effect = lambda task, phase, *args: self.events.append(phase)
        self.api.totp_finish.side_effect = lambda *args, **kwargs: self.events.append(('finish', args))
        self.journal = Mock()
        self.journal.save.side_effect = lambda *args: self.events.append('journal_candidate' if len(args) > 1 else 'journal_old')
        self.journal.acknowledge.side_effect = lambda *_: self.events.append('journal_ack')
        self.session = Mock()
        def request(method, route, payload):
            self.events.append('activate' if route.endswith('activate_enrollment') else 'enroll')
            return {'success': True} if route.endswith('activate_enrollment') else {'secret': NEW, 'session_id': 'fake'}
        self.session.request.side_effect = request
        self.login = Mock(side_effect=[{'cookies': []}, None])
        self.wait = Mock(side_effect=lambda *_: self.events.append('next_window'))

    def run_rotation(self):
        rotate(self.api, self.claim, session_factory=lambda *a, **kw: self.session,
               login=self.login, journal_factory=lambda config: self.journal, wait=self.wait)

    def test_activation_requires_journal_fsync_then_database_ack_then_phase(self):
        self.run_rotation()
        self.assertEqual(self.events[:8], ['journal_old', 'enrolling', 'enroll', 'journal_candidate',
                                          'prepared', 'journal_ack', 'activating', 'activate'])
        self.api.totp_finish.assert_called_once_with(7, True)
        self.assertEqual([c.args[2] for c in self.login.call_args_list], [NEW, OLD])
        self.wait.assert_called_once()

    def test_journal_unavailable_prevents_all_upstream_calls(self):
        factory = Mock()
        rotate(self.api, self.claim, session_factory=factory,
               journal_factory=Mock(side_effect=RotationError('journal_unavailable')))
        factory.assert_not_called()
        self.api.totp_phase.assert_not_called()

    def test_initial_journal_write_failure_prevents_login(self):
        self.journal.save.side_effect = OSError('disk full')
        self.run_rotation()
        self.session.request.assert_not_called()

    def test_candidate_journal_failure_never_activates(self):
        self.journal.save.side_effect = [None, OSError('disk full')]
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)
        self.api.totp_finish.assert_called_once_with(7, False, 'enrollment_failed')

    def test_database_ack_loss_keeps_candidate_and_never_activates(self):
        self.api.totp_phase.side_effect = lambda task, phase, *a: (_ for _ in ()).throw(TimeoutError()) if phase == 'prepared' else None
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)
        self.assertEqual(self.journal.save.call_args.args[1], NEW)
        self.journal.acknowledge.assert_not_called()

    def test_journal_ack_failure_after_database_ack_still_never_activates(self):
        self.journal.acknowledge.side_effect = OSError()
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)

    def test_activation_timeout_never_reenrolls_or_verifies(self):
        self.session.request.side_effect = [{'secret': NEW, 'session_id': 'fake'}, TimeoutError('ambiguous')]
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 2)
        self.login.assert_not_called()
        self.api.totp_finish.assert_called_once_with(7, False, 'activation_unconfirmed')

    def test_lost_activating_phase_ack_never_mutates(self):
        def phase(task, name, *a):
            if name == 'activating':
                raise TimeoutError()
        self.api.totp_phase.side_effect = phase
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)

    def test_lost_lease_after_enroll_still_journals_raw_candidate(self):
        def guard():
            if 'enroll' in self.events:
                raise RotationError('lease_lost')
        self.api.guard.side_effect = guard
        self.api.totp_phase.side_effect = lambda *a: self.api.guard()
        self.run_rotation()
        self.assertEqual(self.journal.save.call_args.args[1], NEW)
        self.assertEqual(self.session.request.call_count, 1)

    def test_malformed_candidate_is_journaled_before_validation(self):
        self.session.request.side_effect = [{'secret': 'malformed!!!!', 'session_id': 'fake'}]
        self.run_rotation()
        self.assertEqual(self.journal.save.call_args.args[1], 'malformed!!!!')
        self.assertEqual(self.session.request.call_count, 1)

    def test_enrollment_same_key_never_activates(self):
        self.session.request.side_effect = [{'secret': OLD.lower(), 'session_id': 'fake'}]
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 1)

    def test_explicit_activation_failure_never_reports_success(self):
        self.session.request.side_effect = [{'secret': NEW, 'session_id': 'fake'}, {'success': False}]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'activation_unconfirmed')

    def test_old_key_still_accepted_is_failure(self):
        self.login.side_effect = [{}, {}]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'verification_failed')

    def test_network_error_cannot_prove_old_key_rejection(self):
        self.login.side_effect = [{}, RotationError('login_failed')]
        self.run_rotation()
        self.api.totp_finish.assert_called_once_with(7, False, 'verification_failed')

    def test_rejected_candidate_cannot_succeed(self):
        self.login.side_effect = [None]
        self.run_rotation()
        self.wait.assert_not_called()
        self.api.totp_finish.assert_called_once_with(7, False, 'verification_failed')

    def test_recovery_new_only_verifies_persisted_keys(self):
        self.claim.update(action='verify_new', candidate_secret=NEW)
        self.run_rotation()
        self.session.request.assert_not_called()
        self.journal.save.assert_not_called()
        self.api.totp_phase.assert_called_once_with(7, 'verify_recovery')
        self.api.totp_finish.assert_called_once_with(7, True)

    def test_recovery_old_checks_inverse_pair(self):
        self.claim.update(action='verify_old', candidate_secret=NEW)
        self.run_rotation()
        self.assertEqual([c.args[2] for c in self.login.call_args_list], [OLD, NEW])
        self.session.request.assert_not_called()

    def test_recovery_old_without_candidate_only_verifies_old_key(self):
        self.claim.update(action='verify_old')
        self.login.side_effect = [{}]
        self.run_rotation()
        self.login.assert_called_once_with(self.api.config, self.claim, OLD)
        self.wait.assert_not_called()
        self.session.request.assert_not_called()
        self.api.totp_finish.assert_called_once_with(7, True)

    def test_exception_is_not_reported(self):
        self.session.request.side_effect = RuntimeError(NEW + self.claim['password'])
        self.run_rotation()
        self.assertNotIn(NEW, str(self.api.totp_finish.call_args))
        self.assertNotIn(self.claim['password'], str(self.api.totp_finish.call_args))

    def test_finish_failure_does_not_retry_upstream_or_finish_success(self):
        self.api.totp_finish.side_effect = TimeoutError()
        self.run_rotation()
        self.assertEqual(self.session.request.call_count, 2)
        self.assertEqual(self.api.totp_finish.call_count, 2)  # failed success ACK, then failure notification
        self.assertEqual(self.api.totp_finish.call_args.args, (7, False, 'verification_failed'))

    def test_totp_rfc6238_and_invalid_input(self):
        self.assertEqual(totp(NEW, 59), '287082')
        for secret in ('!', None, '', 'this contains spaces but not base32'):
            with self.subTest(secret=secret), self.assertRaises(RotationError):
                totp(secret)


class LoginTests(unittest.TestCase):
    def setUp(self):
        self.claim = dict(task_id=8, login_email='owner@example.test', password='synthetic-password', totp_secret=OLD)
        self.config = SimpleNamespace(tosub2_root=Path('/synthetic'), node='node')

    def test_missing_mfa_flag_not_a_verified_login(self):
        def child(command, **kwargs):
            Path(command[-1]).write_text(json.dumps({'web': {'mfaVerified': False}}))
            return SimpleNamespace(returncode=0, stdout='', stderr='')
        with patch('tools.openai_totp_rotation.guarded_run', child), self.assertRaisesRegex(RotationError, 'verification_failed'):
            login_web(self.config, self.claim, OLD)

    def test_runner_credentials_only_env_not_argv(self):
        with patch.dict('os.environ', {'XY2API_OPENAI_TOTP_WORKER_SECRET': 'private-worker-token',
                                      'NODE_OPTIONS': '--inspect', 'HTTPS_PROXY': 'private-proxy',
                                      'CHATGPT_BASE': 'http://malicious.invalid'}):
            with patch('tools.openai_totp_rotation.guarded_run', return_value=SimpleNamespace(returncode=1, stdout='', stderr='')) as run:
                with self.assertRaises(RotationError):
                    login_web(self.config, self.claim, OLD)
            args, kwargs = run.call_args
            self.assertNotIn(OLD, str(args))
            self.assertNotIn(self.claim['password'], str(args))
            for key in ('XY2API_OPENAI_TOTP_WORKER_SECRET', 'NODE_OPTIONS', 'HTTPS_PROXY', 'CHATGPT_BASE'):
                self.assertNotIn(key, kwargs['env'])
            self.assertEqual(kwargs['env']['TOSUB2_PYTHON'], sys.executable)

    def test_only_explicit_totp_rejection_returns_none(self):
        marker = '[warn] The code generated from the configured 2FA key was rejected; enter a current code manually.'
        for output, rejection in ((marker, True), ('prefix ' + marker, False), ('HTTP 403 bad OTP connection refused', False)):
            with self.subTest(output=output), patch('tools.openai_totp_rotation.guarded_run',
                    return_value=SimpleNamespace(returncode=1, stdout=output, stderr='')):
                if rejection:
                    self.assertIsNone(login_web(self.config, self.claim, OLD))
                else:
                    with self.assertRaises(RotationError):
                        login_web(self.config, self.claim, OLD)

    def test_password_rejection_is_fixed_code(self):
        with patch('tools.openai_totp_rotation.guarded_run', return_value=SimpleNamespace(
                returncode=1, stdout='[warn] Password was rejected. Enter it again, or q to quit.', stderr='')):
            with self.assertRaisesRegex(RotationError, '^password_rejected$'):
                login_web(self.config, self.claim, OLD)

    def test_real_node_runner_uses_fresh_private_output_and_cleans_it(self):
        import shutil
        with tempfile.TemporaryDirectory() as root:
            src = Path(root) / 'src'
            src.mkdir()
            (src / 'tls-transport.mjs').write_text('export class TlsFingerprintTransport { request() {} send() {} }')
            (src / 'protocol-login.mjs').write_text('''
import fs from 'node:fs';
const dest=process.argv[process.argv.indexOf('--out')+1];
if (!process.env.CHATGPT_LOGIN_PASSWORD || !process.env.CHATGPT_TOTP_SECRET) process.exit(2);
fs.writeFileSync(dest,JSON.stringify({web:{mfaVerified:true},cookies:[]}));
fs.appendFileSync(new URL('../outputs',import.meta.url),dest+'\\n');
''')
            cfg = SimpleNamespace(tosub2_root=Path(root), node=shutil.which('node'))
            self.assertTrue(login_web(cfg, self.claim, OLD)['web']['mfaVerified'])
            self.assertTrue(login_web(cfg, self.claim, OLD)['web']['mfaVerified'])
            outputs = (Path(root) / 'outputs').read_text().splitlines()
            self.assertEqual(len(set(outputs)), 2)
            self.assertTrue(all(not Path(name).parent.exists() for name in outputs))

    def test_process_guard_cancels_real_local_subprocess(self):
        cancel = threading.Event()
        cancel.set()
        with self.assertRaisesRegex(RotationError, 'lease_lost'):
            guarded_run([sys.executable, '-c', 'import time; time.sleep(60)'], timeout=2,
                        cancel=cancel, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def test_web_session_validates_email_and_uses_no_redirects(self):
        client = Mock()
        payload = {'user': {'email': self.claim['login_email']}, 'accessToken': 'synthetic-token', 'expires': '2099-01-01'}
        client.request.return_value = SimpleNamespace(status_code=200, content=b'{}', json=lambda: payload)
        module = SimpleNamespace(Session=Mock(return_value=client))
        with patch.dict(sys.modules, {'curl_cffi.requests': module}):
            session = WebSession(self.config, self.claim, OLD, login=lambda *a: {'cookies': []})
            self.assertEqual(session.auth_session_payload, payload)
            self.assertIs(client.request.call_args.kwargs['allow_redirects'], False)
            self.assertIs(module.Session.call_args.kwargs['trust_env'], False)
            session.close()
            payload['user']['email'] = 'other@example.test'
            with self.assertRaisesRegex(RotationError, 'identity_mismatch'):
                WebSession(self.config, self.claim, OLD, login=lambda *a: {'cookies': []})

    def test_login_only_sends_complete_session_to_api(self):
        payload = {'user': {'email': self.claim['login_email']}, 'accessToken': 'fake', 'expires': '2099', 'extra': {'field': 'kept'}}
        session = Mock(auth_session_payload=payload)
        api = Mock(config=self.config)
        run_login(api, self.claim, session_factory=lambda *a, **kw: session)
        api.totp_finish.assert_called_once_with(8, True, session=payload)
        session.close.assert_called_once()

    def test_login_failure_is_sanitized(self):
        api = Mock(config=self.config)
        run_login(api, self.claim, session_factory=Mock(side_effect=RuntimeError(self.claim['password'])))
        api.totp_finish.assert_called_once_with(8, False, 'login_failed')


class JournalRecoveryTests(unittest.TestCase):
    def test_recover_sends_only_candidate_and_acknowledges_only_success(self):
        journal = object.__new__(Journal)
        journal.call = Mock(side_effect=[[7, 8], {'candidate': NEW}, None, {'candidate': OLD}])
        api = Mock()
        api.totp_recover.side_effect = [None, RuntimeError('server not uncertain')]
        journal.recover(api)
        self.assertEqual(journal.call.call_count, 4)
        self.assertEqual(journal.call.call_args_list[2].args[0], {'action': 'acknowledge', 'task_id': 7})
        self.assertEqual(api.totp_recover.call_count, 2)


if __name__ == '__main__':
    unittest.main()
