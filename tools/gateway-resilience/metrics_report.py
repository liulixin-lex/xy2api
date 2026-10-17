#!/usr/bin/env python3
"""Design §14 success metrics: collect one measurement, compare two.

collect gathers what is available into one JSON file:
  --psql CMD     runs metrics.sql through CMD (any command that runs psql
                 with SQL on stdin; -X -At -v hours=N are appended);
  --caddy FILE   Caddy JSON access logs (caddy_cuts.py), with --app for the
                 cut-then-billed match;
  --app FILE     application JSON logs (session_streaks.py);
  --clients DIR  clients.py evidence: requests of failed Codex turns;
  --set K=V      a number measured elsewhere (for example the production
                 same-session analysis of Codex turns);
  --section S=F  a section (db, caddy, app, clients) computed elsewhere, such
                 as caddy_cuts.py --json run on the proxy host.

report compares a baseline and a candidate measurement against the targets
of design §14 and prints a Markdown table; it exits 1 when a measured
metric misses its target. Counts are scaled to 48 hours. Metrics without
data on either side are "n/a", never a pass; "参考" rows have no target.

Usage:
    metrics_report.py collect --label prod-0.3.0 --hours 48 --psql 'psql' \\
        --caddy access.log --app app.log --out baseline.json
    metrics_report.py report baseline.json candidate.json
"""

from __future__ import annotations

import argparse
import datetime
import glob
import json
import os
import shlex
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import caddy_cuts  # noqa: E402
import session_streaks  # noqa: E402

PASS, FAIL, NA, INFO = "PASS", "FAIL", "n/a", "参考"


def run_sql(cmd: str, hours: float) -> dict:
    with open(os.path.join(HERE, "metrics.sql"), "rb") as fh:
        proc = subprocess.run(shlex.split(cmd) + ["-X", "-At", "-v", "ON_ERROR_STOP=1", "-v", f"hours={hours:g}"],
                              stdin=fh, capture_output=True, check=False)
    if proc.returncode != 0:
        raise SystemExit(f"metrics.sql failed: {proc.stderr.decode(errors='replace').strip()[:500]}")
    out: dict = {}
    for line in proc.stdout.decode(errors="replace").splitlines():
        line = line.strip()
        if line.startswith("{"):
            out.update(json.loads(line))
    return out


def codex_turns(evidence: str) -> dict:
    """Requests per client case; the failed-turn metric is the largest
    request count of a Codex case that ends in failure."""
    cases = {}
    for path in sorted(glob.glob(os.path.join(evidence, "C-*.json"))):
        with open(path, encoding="utf-8") as fh:
            rec = json.load(fh)
        cases[rec["id"]] = {"client": rec.get("client"), "requests": rec.get("client_requests"), "ok": rec.get("ok")}
    failed = [c["requests"] for cid, c in cases.items()
              if c["client"] == "codex" and cid.endswith(("all-fail", "busy-stream")) and c["requests"] is not None]
    out = {"cases": cases}
    if failed:
        out["codex_failed_turn_requests"] = max(failed)
    return out


def collect(args) -> dict:
    doc = {"label": args.label, "hours": args.hours,
           "collected_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")}
    if args.psql:
        doc["db"] = run_sql(args.psql, args.hours)
    if args.caddy:
        doc["caddy"] = caddy_cuts.analyse(args.caddy, args.app)
    if args.app:
        doc["app"] = session_streaks.analyse(args.app)
    if args.clients:
        doc["clients"] = codex_turns(args.clients)
    for item in args.section:
        name, _, path = item.partition("=")
        if name not in ("db", "caddy", "app", "clients"):
            raise SystemExit(f"unknown section {name!r}")
        doc[name] = {}
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if line.startswith("{"):
                    doc[name].update(json.loads(line))
    for item in args.set:
        key, _, value = item.partition("=")
        doc.setdefault("manual", {})[key.strip()] = float(value)
    return doc


def per48(doc: dict, value, source: str = "db"):
    """Scales a count to 48 hours of the window it was measured over: the
    Caddy log span for log counts, the SQL window otherwise."""
    if value is None:
        return None
    hours = doc.get("db", {}).get("hours") or doc.get("hours") or 48
    if source == "caddy" and pick(doc, "caddy", "until") and pick(doc, "caddy", "since") is not None:
        hours = max((doc["caddy"]["until"] - doc["caddy"]["since"]) / 3600.0, 1.0)
    return value * 48.0 / float(hours)


def pick(doc: dict, *path):
    cur = doc
    for key in path:
        if not isinstance(cur, dict) or key not in cur:
            return None
        cur = cur[key]
    return cur


def manual_or(doc: dict, key: str, *path):
    value = pick(doc, "manual", key)
    return value if value is not None else pick(doc, *path)


def fmt(value, unit: str = "") -> str:
    if value is None:
        return "-"
    if isinstance(value, float):
        value = round(value, 2)
        if value == int(value):
            value = int(value)
    return f"{value}{unit}"


def verdict(value, ok) -> str:
    if value is None:
        return NA
    return PASS if ok else FAIL


def rows(base: dict, cand: dict) -> list[dict]:
    out = []

    def add(name, b, c, target, ok, unit=""):
        out.append({"metric": name, "baseline": fmt(b, unit), "candidate": fmt(c, unit), "target": target,
                    "verdict": INFO if ok is None else verdict(c, ok(c) if c is not None else False)})

    entries = sorted(set(pick(base, "caddy", "entries") or {}) | set(pick(cand, "caddy", "entries") or {}))
    for entry in [e for e in entries if e != "direct"] or ["cdn"]:
        add(f"CDN 切断率（{entry}）", pick(base, "caddy", "entries", entry, "cut_rate_pct"),
            pick(cand, "caddy", "entries", entry, "cut_rate_pct"), "< 0.3%", lambda v: v < 0.3, "%")
    add("切断后仍计费（每 48 小时）", per48(base, manual_or(base, "app_200_after_cut", "caddy", "app_200_after_cut"), "caddy"),
        per48(cand, manual_or(cand, "app_200_after_cut", "caddy", "app_200_after_cut"), "caddy"), "0", lambda v: v == 0)
    add("“无可用账号”503（每 48 小时）", per48(base, pick(base, "db", "no_account_503")),
        per48(cand, pick(cand, "db", "no_account_503")), "< 300", lambda v: v < 300)
    add("单组连续无号最长分钟数", pick(base, "db", "no_account_longest_minutes"),
        pick(cand, "db", "no_account_longest_minutes"), "≤ 5", lambda v: v <= 5)
    add("换号后成功 P50 耗时", manual_or(base, "switch_success_p50_s", "app", "switch_success_p50_s"),
        manual_or(cand, "switch_success_p50_s", "app", "switch_success_p50_s"), "< 30 秒", lambda v: v < 30, " 秒")
    add("同一会话同一账号连续失败 P99", manual_or(base, "streak_p99", "app", "streak_p99"),
        manual_or(cand, "streak_p99", "app", "streak_p99"), "≤ 2", lambda v: v <= 2)
    add("无会话请求：同一 Key 同一账号连续失败 P99", pick(base, "app", "key_streak_p99"),
        pick(cand, "app", "key_streak_p99"), "参考", None)
    add("因余额被永久置错（每 48 小时）", per48(base, pick(base, "db", "balance_disabled_events")),
        per48(cand, pick(cand, "db", "balance_disabled_events")), "0", lambda v: v == 0)
    base_shown = per48(base, pick(base, "db", "insufficient_shown_positive_balance"))
    add("有余额却看到“余额不足”（每 48 小时）", base_shown,
        per48(cand, pick(cand, "db", "insufficient_shown_positive_balance")), "较基线减少 90%",
        lambda v: base_shown is not None and v <= 0.1 * base_shown)
    add("503 带 Retry-After", pick(base, "caddy", "status_503_retry_after_pct"),
        pick(cand, "caddy", "status_503_retry_after_pct"), "100%", lambda v: v >= 100, "%")
    add("失败的 Codex 回合请求数", manual_or(base, "codex_failed_turn_requests", "clients", "codex_failed_turn_requests"),
        manual_or(cand, "codex_failed_turn_requests", "clients", "codex_failed_turn_requests"), "≤ 5", lambda v: v <= 5)

    platforms = sorted(set(pick(base, "db", "platforms") or {}) & set(pick(cand, "db", "platforms") or {}))
    for p in platforms:
        b, c = base["db"]["platforms"][p], cand["db"]["platforms"][p]
        if b.get("cache_read_pct") is not None:
            add(f"缓存读取占比（{p}）", b.get("cache_read_pct"), c.get("cache_read_pct"), "≥ 基线 − 0.5 个百分点",
                lambda v, x=b["cache_read_pct"]: v >= x - 0.5, "%")
        if b.get("ttft_p50_ms") is not None:
            add(f"首字 P50（{p}）", b.get("ttft_p50_ms"), c.get("ttft_p50_ms"), "≤ 基线 +3%",
                lambda v, x=b["ttft_p50_ms"]: v <= x * 1.03, " ms")
        if b.get("ttft_p95_ms") is not None:
            add(f"首字 P95（{p}）", b.get("ttft_p95_ms"), c.get("ttft_p95_ms"), "≤ 基线 +5%",
                lambda v, x=b["ttft_p95_ms"]: v <= x * 1.05, " ms")
    return out


def markdown(base: dict, cand: dict, table: list[dict]) -> str:
    lines = ["# 网关韧性指标对照（design §14）", "",
             f"- 基线：{base.get('label', '-')}（{fmt(base.get('hours'))} 小时，采集于 {base.get('collected_at', '-')}）",
             f"- 候选：{cand.get('label', '-')}（{fmt(cand.get('hours'))} 小时，采集于 {cand.get('collected_at', '-')}）",
             "- 计数按 48 小时折算；任一侧缺数据记为 n/a。", "",
             "| 指标 | 基线 | 候选 | 目标 | 结论 |", "|---|---|---|---|---|"]
    lines += [f"| {r['metric']} | {r['baseline']} | {r['candidate']} | {r['target']} | {r['verdict']} |" for r in table]
    counts = {v: sum(1 for r in table if r["verdict"] == v) for v in (PASS, FAIL, NA)}
    lines += ["", f"通过 {counts[PASS]}，未达标 {counts[FAIL]}，无数据 {counts[NA]}（“参考”行不计入）。"]
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("collect", help="measure one environment")
    c.add_argument("--label", required=True)
    c.add_argument("--hours", type=float, default=48)
    c.add_argument("--psql", default="", help="command running psql with SQL on stdin")
    c.add_argument("--caddy", action="append", default=[])
    c.add_argument("--app", action="append", default=[])
    c.add_argument("--clients", default="")
    c.add_argument("--set", action="append", default=[], help="K=V measured elsewhere")
    c.add_argument("--section", action="append", default=[], help="S=FILE with a section computed elsewhere")
    c.add_argument("--out", default="-")
    r = sub.add_parser("report", help="compare a baseline and a candidate")
    r.add_argument("baseline")
    r.add_argument("candidate")
    r.add_argument("--json", action="store_true", help="print the rows as JSON")
    args = parser.parse_args()

    if args.cmd == "collect":
        doc = collect(args)
        text = json.dumps(doc, indent=1, ensure_ascii=False)
        if args.out == "-":
            print(text)
        else:
            with open(args.out, "w", encoding="utf-8") as fh:
                fh.write(text + "\n")
        return 0

    with open(args.baseline, encoding="utf-8") as fh:
        base = json.load(fh)
    with open(args.candidate, encoding="utf-8") as fh:
        cand = json.load(fh)
    table = rows(base, cand)
    print(json.dumps(table, ensure_ascii=False, indent=1) if args.json else markdown(base, cand, table), end="")
    return 1 if any(r["verdict"] == FAIL for r in table) else 0


if __name__ == "__main__":
    sys.exit(main())
