import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from datetime import date, timedelta


CHECKER = Path(__file__).with_name("check_pnpm_audit_exceptions.py")


class AuditGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.audit = self.root / "audit.json"
        self.exceptions = self.root / "exceptions.yml"
        self.exceptions.write_text("version: 1\nexceptions:\n", encoding="utf-8")

    def run_gate(self, payload, exit_code=None):
        self.audit.write_text(json.dumps(payload), encoding="utf-8")
        command = [sys.executable, str(CHECKER), "--audit", str(self.audit),
                   "--exceptions", str(self.exceptions)]
        if exit_code is not None:
            command += ["--audit-exit-code", str(exit_code)]
        return subprocess.run(command, text=True, capture_output=True)

    def high_report(self, advisory="GHSA-fixture-exact"):
        return {"advisories": {"1": {"module_name": "fixture-package", "severity": "high",
                                      "github_advisory_id": advisory, "title": "Fixture finding"}}}

    def allow_exception(self, expires_on):
        self.exceptions.write_text(
            "version: 1\nexceptions:\n  - package: fixture-package\n"
            "    advisory: GHSA-fixture-exact\n    severity: high\n"
            f"    expires_on: {expires_on}\n    mitigation: Isolated test fixture\n",
            encoding="utf-8",
        )

    def test_error_responses_and_unknown_structures_fail(self):
        for payload in ({"error": {"code": "FIXTURE_UNAVAILABLE"}}, {}, [],
                        {"metadata": {"vulnerabilities": {"high": 0}}}, {"advisories": []}):
            with self.subTest(payload=payload):
                result = self.run_gate(payload)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertNotIn("Audit exceptions validated.", result.stdout)

    def test_error_cannot_hide_beside_valid_empty_container(self):
        result = self.run_gate({"error": None, "advisories": {}})
        self.assertEqual(result.returncode, 1)

    def test_supported_zero_finding_formats_pass(self):
        for key in ("advisories", "vulnerabilities"):
            with self.subTest(key=key):
                result = self.run_gate({key: {}}, 0)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_command_failure_cannot_become_pass(self):
        for code in (2, 127, -9):
            with self.subTest(exit_code=code):
                result = self.run_gate({"advisories": {}}, code)
                self.assertEqual(result.returncode, 1)
                self.assertIn("exit status", result.stderr)

    def test_findings_exit_requires_findings(self):
        result = self.run_gate({"advisories": {}}, 1)
        self.assertEqual(result.returncode, 1)

    def test_current_exact_exception_passes(self):
        self.allow_exception(date.today() + timedelta(days=1))
        result = self.run_gate(self.high_report(), 1)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_expired_exception_fails(self):
        self.allow_exception(date.today() - timedelta(days=1))
        result = self.run_gate(self.high_report(), 1)
        self.assertEqual(result.returncode, 1)
        self.assertIn("Exceptions expired", result.stderr)

    def test_exception_does_not_allow_another_advisory(self):
        self.allow_exception(date.today() + timedelta(days=1))
        result = self.run_gate(self.high_report("GHSA-fixture-different"), 1)
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing exceptions", result.stderr)

    def test_high_counts_without_advisory_details_fail(self):
        result = self.run_gate({"advisories": {}, "metadata": {"vulnerabilities": {"high": 1}}})
        self.assertEqual(result.returncode, 1)

    def test_critical_counts_cannot_hide_behind_excepted_high_advisory(self):
        self.allow_exception(date.today() + timedelta(days=1))
        for key in ("advisories", "vulnerabilities"):
            with self.subTest(format=key):
                if key == "advisories":
                    payload = self.high_report()
                else:
                    payload = {key: {"fixture-package": {"severity": "high",
                                "via": [{"github_advisory_id": "GHSA-fixture-exact"}]}}}
                payload["metadata"] = {"vulnerabilities": {"high": 1, "critical": 1}}
                result = self.run_gate(payload, 1)
                self.assertEqual(result.returncode, 1)
                self.assertIn("critical counts without same-severity", result.stderr)

    def test_high_vulnerability_without_evidence_fails(self):
        for via in ([], [{}], None):
            with self.subTest(via=via):
                result = self.run_gate({"vulnerabilities": {"fixture-package": {"severity": "high", "via": via}}})
                self.assertEqual(result.returncode, 1)

    def test_malformed_severity_fails(self):
        result = self.run_gate({"advisories": {"1": {"module_name": "fixture-package", "severity": 7}}})
        self.assertEqual(result.returncode, 1)

    def test_moderate_findings_keep_existing_policy(self):
        result = self.run_gate({"advisories": {"1": {"module_name": "fixture-package", "severity": "moderate",
                                                       "github_advisory_id": "GHSA-fixture-moderate"}}}, 1)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
