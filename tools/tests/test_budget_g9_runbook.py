"""G9 runbook contract: trigger queries never wait for billing settlement."""
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNBOOK = ROOT / "openspec/changes/budget-anchor-0512-20261108/runbook.md"


class BudgetG9RunbookTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = RUNBOOK.read_text(encoding="utf-8")

    def query(self, name):
        result = re.search(re.escape(name) + r"（[^\n]+）：\n\n```sql\n(.*?)```", self.text, re.S)
        self.assertIsNotNone(result, name)
        return result.group(1)

    def test_hard_triggers_do_not_depend_on_temporary_billing(self):
        self.assertIn("## G9 硬回滚：触发", self.text)
        self.assertIn("### G9 事后核实", self.text)
        for name in ("SQL-1a", "SQL-1b", "SQL-1c"):
            query = self.query(name)
            self.assertNotIn("billed", query)
            self.assertIn("budget_start_mode' = 'body_eof'", query)
            self.assertIn("'<T_stage1>'::timestamptz", query)
            self.assertIn("level = 'warn'", query)
        self.assertIn("late_attempt_no_timer_count", self.query("SQL-1a"))
        self.assertIn("resilience.abuse_guard", self.query("SQL-1a"))
        self.assertIn("最终是否扣费", self.text)
        self.assertIn("gateway_undelivered_requests", self.text)

    def test_cross_hour_pairing_and_clock_margin(self):
        pair = self.query("SQL-1b")
        self.assertIn("disconnect_unix_ms", pair)
        self.assertIn("AT TIME ZONE 'UTC'", pair)
        abuse = pair.split("), abuse AS (")[1]
        self.assertNotIn("budget_start_mode", abuse)
        self.assertIn("interval '4 hours'", abuse)
        self.assertIn("('<T_last>'::timestamptz - interval '5 minutes')", self.query("SQL-1c"))

    def test_inflight_metrics_and_unavailable_sql_are_explicit(self):
        for text in ("false:body_eof", "尚未 FinishTiming", "连续两轮", "正常返回空结果", "临时状态", "R2′", "RestartCount", "分钟精度", "非计划重启", "某个实例读不到", "切换响应时刻 + 15 秒"):
            self.assertIn(text, self.text)
        self.assertNotIn("SQL-1（", self.text)


if __name__ == "__main__":
    unittest.main()
