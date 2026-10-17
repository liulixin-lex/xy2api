import contextlib
import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

TOOLS = Path(__file__).resolve().parents[1] / "gateway-resilience"


def load(name):
    spec = importlib.util.spec_from_file_location(name, TOOLS / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


caddy_cuts = load("caddy_cuts")
session_streaks = load("session_streaks")
metrics_report = load("metrics_report")


def write_lines(directory, name, rows):
    path = os.path.join(directory, name)
    with open(path, "w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row) + "\n")
    return path


def caddy_row(ts, status, duration, size=0, edge="cf", retry_after=False, ip="10.0.0.1", uri="/v1/responses"):
    return {"ts": ts, "status": status, "duration": duration, "size": size,
            "request": {"uri": uri, "client_ip": ip, "headers": {"X-Xy-Edge": [edge]}},
            "resp_headers": {"Retry-After": ["5"]} if retry_after else {}}


class CaddyCutsTest(unittest.TestCase):
    def test_cut_rate_retry_after_and_billed_after_cut(self):
        with tempfile.TemporaryDirectory() as tmp:
            caddy = write_lines(tmp, "access.log", [
                caddy_row(1000.0, 0, 125.2),                       # cut at the 125 s timeout
                caddy_row(1100.0, 200, 3.0, size=900),
                caddy_row(1200.0, 503, 0.1, retry_after=True),
                caddy_row(1300.0, 503, 0.1),
                caddy_row(1400.0, 200, 1.0, size=10, edge="none"),
                caddy_row(1500.0, 200, 1.0, uri="/static/app.js"),  # not an API row
            ])
            app = write_lines(tmp, "app.log", [
                {"msg": "http request completed", "time": "1970-01-01T00:19:34.800+0000", "latency_ms": 300000,
                 "status_code": 200, "client_ip": "10.0.0.1", "path": "/v1/responses"},
            ])
            out = caddy_cuts.analyse([caddy], [app])
        self.assertEqual(out["api_rows"], 5)
        self.assertEqual(out["entries"]["cf"], {"requests": 4, "cuts": 1, "cut_rate_pct": 25.0})
        self.assertEqual(out["entries"]["none"]["cuts"], 0)
        self.assertEqual(out["cuts_by_timeout"], {"125s": 1})
        self.assertEqual(out["status_503_retry_after_pct"], 50.0)
        self.assertEqual(out["app_200_after_cut"], 1)

    def test_no_api_rows(self):
        with tempfile.TemporaryDirectory() as tmp:
            caddy = write_lines(tmp, "access.log", [caddy_row(1.0, 200, 1.0, uri="/")])
            self.assertEqual(caddy_cuts.analyse([caddy], []), {})


class SessionStreaksTest(unittest.TestCase):
    def test_streaks_and_switch_latency(self):
        lines = []
        for i in range(3):
            lines.append({"request_id": "r1", "msg": "gateway.failover_switch_account", "account_id": 1,
                          "session_hash": "s", "time": f"2026-10-15T00:00:0{i}.000+0000"})
        lines.append({"request_id": "r1", "msg": "http request completed", "account_id": 2, "status_code": 200,
                      "latency_ms": 12000, "time": "2026-10-15T00:00:09.000+0000"})
        lines.append({"request_id": "r2", "msg": "http request completed", "account_id": 1, "status_code": 200,
                      "latency_ms": 900, "session_hash": "s", "time": "2026-10-15T00:01:00.000+0000"})
        with tempfile.TemporaryDirectory() as tmp:
            out = session_streaks.analyse([write_lines(tmp, "app.log", lines)])
        self.assertEqual(out["requests"], 2)
        self.assertEqual(out["session_requests"], 2)
        self.assertEqual(out["streak_runs"], 1)
        self.assertEqual(out["streak_p99"], 3)
        self.assertEqual(out["key_streak_runs"], 0)
        self.assertEqual(out["switch_success"], 1)
        self.assertEqual(out["switch_success_p50_s"], 12.0)

    def test_requests_without_a_session_count_per_key(self):
        lines = []
        for i in range(3):
            lines.append({"request_id": f"k{i}", "msg": "openai.upstream_failover_switching", "account_id": 7,
                          "api_key_id": 40, "time": f"2026-10-15T00:00:0{i}.000+0000"})
        with tempfile.TemporaryDirectory() as tmp:
            out = session_streaks.analyse([write_lines(tmp, "app.log", lines)])
        self.assertEqual(out["streak_runs"], 0)
        self.assertIsNone(out["streak_p99"])
        self.assertEqual(out["key_streak_p99"], 3)

    def test_console_format(self):
        fields = {"request_id": "r1", "account_id": 3, "status_code": 200, "latency_ms": 4000, "session_hash": "s"}
        lines = [
            "2026-10-16T03:55:10.000+0800\tWARN\tslog\tlogger/slog_handler.go:55\tgateway.failover_switch_account\t"
            + json.dumps({"request_id": "r1", "account_id": 1}),
            "2026-10-16T03:55:11.085+0800\tINFO\tmiddleware/logger.go:82\thttp request completed\t" + json.dumps(fields),
            "2026-10-16T03:55:11.100+0800\tINFO\tserver started",
        ]
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "app.log")
            with open(path, "w") as fh:
                fh.write("\n".join(lines) + "\n")
            out = session_streaks.analyse([path])
        self.assertEqual(out["requests"], 1)
        self.assertEqual(out["streak_runs"], 1)
        self.assertEqual(out["switch_success_p50_s"], 4.0)

    def test_empty_logs(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = session_streaks.analyse([write_lines(tmp, "app.log", [])])
        self.assertIsNone(out["streak_p99"])
        self.assertIsNone(out["switch_success_p50_s"])


BASELINE = {
    "label": "prod-0.3.0", "hours": 48,
    "db": {"hours": 48, "no_account_503": 3202, "no_account_longest_minutes": 40, "balance_disabled_events": 50,
           "insufficient_shown_positive_balance": 500,
           "platforms": {"openai": {"cache_read_pct": 80.0, "ttft_p50_ms": 2000, "ttft_p95_ms": 9000}}},
    "caddy": {"since": 0, "until": 48 * 3600, "entries": {"cf": {"cut_rate_pct": 1.5}, "esa": {"cut_rate_pct": 3.5}},
              "status_503_retry_after_pct": 0.0, "app_200_after_cut": 43},
    "app": {"streak_p99": 9, "switch_success_p50_s": 94.8},
    "manual": {"codex_failed_turn_requests": 30},
}


def by_metric(table):
    return {row["metric"]: row for row in table}


class MetricsReportTest(unittest.TestCase):
    def test_candidate_meeting_targets_passes_and_counts_scale_to_48_hours(self):
        cand = {
            "label": "prod-0.4.0-24h", "hours": 24,
            "db": {"hours": 24, "no_account_503": 100, "no_account_longest_minutes": 2, "balance_disabled_events": 0,
                   "insufficient_shown_positive_balance": 20,
                   "platforms": {"openai": {"cache_read_pct": 79.8, "ttft_p50_ms": 2050, "ttft_p95_ms": 9400}}},
            "caddy": {"since": 0, "until": 24 * 3600, "entries": {"cf": {"cut_rate_pct": 0.1}, "esa": {"cut_rate_pct": 0.2}},
                      "status_503_retry_after_pct": 100.0, "app_200_after_cut": 0},
            "app": {"streak_p99": 2, "switch_success_p50_s": 21.0},
            "clients": {"codex_failed_turn_requests": 5},
        }
        table = by_metric(metrics_report.rows(BASELINE, cand))
        self.assertEqual(table["“无可用账号”503（每 48 小时）"]["candidate"], "200")
        self.assertEqual(table["有余额却看到“余额不足”（每 48 小时）"]["candidate"], "40")
        self.assertEqual({row["verdict"] for row in table.values()}, {"PASS", "参考"})
        self.assertEqual(table["无会话请求：同一 Key 同一账号连续失败 P99"]["verdict"], "参考")
        self.assertIn("CDN 切断率（esa）", table)
        self.assertNotIn("CDN 切断率（direct）", table)

    def test_misses_and_missing_data(self):
        cand = {
            "label": "staging", "hours": 24,
            "caddy": {"since": 0, "until": 3600, "entries": {"cf": {"cut_rate_pct": 0.5}}, "status_503_retry_after_pct": 100.0},
            "app": {"streak_p99": 3, "switch_success_p50_s": None},
        }
        table = by_metric(metrics_report.rows(BASELINE, cand))
        self.assertEqual(table["CDN 切断率（cf）"]["verdict"], "FAIL")
        self.assertEqual(table["CDN 切断率（esa）"]["verdict"], "n/a")
        self.assertEqual(table["同一会话同一账号连续失败 P99"]["verdict"], "FAIL")
        self.assertEqual(table["换号后成功 P50 耗时"]["verdict"], "n/a")
        self.assertEqual(table["“无可用账号”503（每 48 小时）"]["verdict"], "n/a")
        self.assertEqual(table["503 带 Retry-After"]["verdict"], "PASS")
        self.assertNotIn("缓存读取占比（openai）", table)

    def test_report_exit_code_and_collect_from_sql_and_clients(self):
        with tempfile.TemporaryDirectory() as tmp:
            clients = os.path.join(tmp, "clients")
            os.makedirs(clients)
            for cid, client, requests in (("C-codex-all-fail", "codex", 5), ("C-codex-ok", "codex", 1),
                                          ("C-openai-py-all-fail", "openai-py", 3)):
                with open(os.path.join(clients, f"{cid}.json"), "w") as fh:
                    json.dump({"id": cid, "client": client, "client_requests": requests, "ok": True}, fh)
            caddy_json = os.path.join(tmp, "caddy.json")
            with open(caddy_json, "w") as fh:
                fh.write(json.dumps({"api_rows": 3, "status_503_retry_after_pct": 100.0}) + "\n")
            fake_psql = f"{sys.executable} -c \"print('noise'); print('{{\\\"hours\\\": 24, \\\"no_account_503\\\": 9}}'); print('{{\\\"undelivered\\\": 1}}')\""
            args = metrics_report.argparse.Namespace(label="cand", hours=24, psql=fake_psql, caddy=[], app=[],
                                                     clients=clients, set=["switch_success_p50_s=12.5"],
                                                     section=[f"caddy={caddy_json}"])
            doc = metrics_report.collect(args)
            self.assertEqual(doc["db"], {"hours": 24, "no_account_503": 9, "undelivered": 1})
            self.assertEqual(doc["clients"]["codex_failed_turn_requests"], 5)
            self.assertEqual(doc["manual"], {"switch_success_p50_s": 12.5})
            self.assertEqual(doc["caddy"]["status_503_retry_after_pct"], 100.0)
            base_path = os.path.join(tmp, "base.json")
            cand_path = os.path.join(tmp, "cand.json")
            with open(base_path, "w") as fh:
                json.dump(BASELINE, fh)
            with open(cand_path, "w") as fh:
                json.dump(doc, fh)
            sys.argv = ["metrics_report.py", "report", base_path, cand_path]
            with contextlib.redirect_stdout(io.StringIO()) as printed:
                self.assertEqual(metrics_report.main(), 0)
            self.assertIn("| “无可用账号”503（每 48 小时） | 3202 | 18 | < 300 | PASS |", printed.getvalue())
            doc["clients"]["codex_failed_turn_requests"] = 6
            with open(cand_path, "w") as fh:
                json.dump(doc, fh)
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(metrics_report.main(), 1)


if __name__ == "__main__":
    unittest.main()
