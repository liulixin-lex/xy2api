"""Regression tests for soak evidence aggregation; no staging services needed."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("soak_under_test", Path(__file__).with_name("soak.py"))
soak = importlib.util.module_from_spec(SPEC)
with mock.patch("builtins.open", mock.mock_open(read_data='{"keys": {}}')):
    SPEC.loader.exec_module(soak)


class OverlapEvidenceTests(unittest.TestCase):
    def evaluate(self, activities, root):
        sample = {"at": "2026-01-01T00:00:00+00:00", "app_mem": "50MiB",
                  "goroutines": 10, "pg_backends": 6, "inflight_keys": 0,
                  "slots_account": 0, "slots_user": 0, "slots_key": 0, "slots_live": 0}
        (root / "metrics.jsonl").write_text(json.dumps(sample) + "\n")
        overlap = root / "activity.jsonl"
        overlap.write_text("".join(json.dumps(row) + "\n" for row in activities))
        with mock.patch.dict(soak.os.environ, {"STAGING_OVERLAP_FILE": str(overlap)}), \
                mock.patch.object(soak, "live_heap", return_value=[]), \
                mock.patch.object(soak, "app_logs", return_value=""), \
                mock.patch.object(soak, "psql", return_value="0"), \
                mock.patch.object(soak, "anon_trend", return_value=(50, 50)):
            return soak.checks(str(root), sample["at"])

    def test_sequence_events_need_no_evidence_and_keep_upgrade_io(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            upgrade = root / "upgrade"
            upgrade.mkdir()
            steps = [{"step": "synthetic data fill begins"},
                     {"step": "synthetic production-sized data"},
                     {"step": "backup and restore begins"},
                     {"step": "backup and restore"}, {"step": "unrelated check"}]
            (upgrade / "REPORT.json").write_text(json.dumps({"steps": steps}))
            activities = [{"event": "sequence_blocked", "reason": "fixture review"},
                          {"event": "upgrade_end", "evidence": str(upgrade)},
                          {"event": "sequence_complete"}]
            result = self.evaluate(activities, root)
            self.assertEqual(result["other_stack_activity"], activities)
            self.assertEqual(result["other_stack_io_steps"], steps[:4])

    def test_malformed_upgrade_still_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(KeyError):
                self.evaluate([{"event": "upgrade_end"}], Path(directory))


if __name__ == "__main__":
    unittest.main()
