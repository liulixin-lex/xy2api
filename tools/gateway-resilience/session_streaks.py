#!/usr/bin/env python3
"""Session failure streaks and switch latency from application JSON logs
(design §14).

- Streaks: per (session, account), the number of consecutive failed attempts
  before a success on that account or the end of the window. The session is
  the request's session_hash (logged by the sticky-session lines). Requests
  without one are independent, so they are counted per API key separately
  (key_streak_*) and do not enter the session metric.
- Switch latency: end-to-end latency of requests that switched accounts at
  least once and finished with 200.

Lines are joined by request_id. Only aggregates are printed (--json: one
JSON object). Usage:
    session_streaks.py [--json] app.log [app.log.gz ...]
"""

from __future__ import annotations

import argparse
import collections
import datetime
import gzip
import json
import sys

FAILURE_MESSAGES = (
    "gateway.failover_switch_account",
    "gateway.failover_same_account_retry",
    "upstream_failover_switching",
    "resilience.budget_exhausted",
    "resilience.first_output_timeout",
)


def open_text(path: str):
    if path.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8", errors="replace")
    return open(path, encoding="utf-8", errors="replace")


def app_line(line: str) -> dict | None:
    """One application log line: JSON, or the console format (time, level,
    caller, [logger,] message and JSON fields, tab separated). Kept in both
    log scripts so each runs as a single file (python3 - on the host)."""
    line = line.strip()
    if line.startswith("{"):
        try:
            return json.loads(line)
        except json.JSONDecodeError:
            return None
    parts = line.split("\t")
    if len(parts) < 4 or not parts[-1].startswith("{"):
        return None
    try:
        d = json.loads(parts[-1])
    except json.JSONDecodeError:
        return None
    d.setdefault("time", parts[0])
    d.setdefault("msg", parts[-2])
    return d


def parse_time(value) -> float | None:
    if isinstance(value, (int, float)):
        return float(value)
    for fmt in ("%Y-%m-%dT%H:%M:%S.%f%z", "%Y-%m-%dT%H:%M:%S%z"):
        try:
            return datetime.datetime.strptime(value, fmt).timestamp()
        except (TypeError, ValueError):
            continue
    return None


def percentile(values: list[float], q: float):
    if not values:
        return None
    values = sorted(values)
    return values[min(len(values) - 1, int(q * len(values)))]


def analyse(paths: list[str]) -> dict:
    """Streak and switch-latency aggregates over the given logs."""
    requests: dict[str, dict] = collections.defaultdict(lambda: {"failures": []})
    for path in paths:
        with open_text(path) as fh:
            for line in fh:
                d = app_line(line)
                if d is None:
                    continue
                rid = d.get("request_id")
                if not rid:
                    continue
                r = requests[rid]
                ts = parse_time(d.get("time") or d.get("ts"))
                for key in ("session_hash", "api_key_id"):
                    if d.get(key) and key not in r:
                        r[key] = d[key]
                msg = str(d.get("msg") or "")
                if any(m in msg for m in FAILURE_MESSAGES) and d.get("account_id") and ts is not None:
                    r["failures"].append((ts, int(d["account_id"])))
                if msg == "http request completed" and ts is not None:
                    r["end"] = ts
                    r["status"] = int(d.get("status_code") or 0)
                    r["latency"] = float(d.get("latency_ms") or 0) / 1000
                    if d.get("account_id"):
                        r["account_id"] = int(d["account_id"])

    events: dict[tuple, list] = collections.defaultdict(list)
    switch_latency = []
    for r in requests.values():
        if r.get("session_hash"):
            session = ("session", r["session_hash"])
        elif r.get("api_key_id"):
            session = ("key", r["api_key_id"])
        else:
            session = None
        if session:
            for ts, account in r["failures"]:
                events[(session, account)].append((ts, False))
            if r.get("status") == 200 and r.get("account_id"):
                events[(session, r["account_id"])].append((r["end"], True))
        if r["failures"] and r.get("status") == 200:
            switch_latency.append(r["latency"])

    streaks: dict[str, list[int]] = {"session": [], "key": []}
    for (session, _), seq in events.items():
        run = 0
        for _, ok in sorted(seq):
            if ok:
                if run:
                    streaks[session[0]].append(run)
                run = 0
            else:
                run += 1
        if run:
            streaks[session[0]].append(run)

    out = {
        "requests": len(requests),
        "session_requests": sum(1 for r in requests.values() if r.get("session_hash")),
        "session_account_pairs": sum(1 for (session, _) in events if session[0] == "session"),
    }
    for prefix, runs in (("streak", streaks["session"]), ("key_streak", streaks["key"])):
        out[f"{prefix}_runs"] = len(runs)
        out[f"{prefix}_p50"] = percentile(runs, 0.5)
        out[f"{prefix}_p99"] = percentile(runs, 0.99)
        out[f"{prefix}_max"] = max(runs) if runs else None
    out["switch_success"] = len(switch_latency)
    out["switch_success_p50_s"] = percentile(switch_latency, 0.5)
    out["switch_success_p95_s"] = percentile(switch_latency, 0.95)
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--json", action="store_true", help="print one JSON object instead of text")
    parser.add_argument("logs", nargs="+", help="application JSON log(s)")
    args = parser.parse_args()

    out = analyse(args.logs)
    if args.json:
        print(json.dumps(out))
        return 0
    show = lambda v: "-" if v is None else v  # noqa: E731
    print(f"requests: {out['requests']} ({out['session_requests']} with a session), "
          f"session/account pairs: {out['session_account_pairs']}")
    print(f"consecutive failures of one session on one account: runs={out['streak_runs']} "
          f"p50={show(out['streak_p50'])} p99={show(out['streak_p99'])} max={show(out['streak_max'])} (target p99 <= 2)")
    print(f"same, per API key for requests without a session: runs={out['key_streak_runs']} "
          f"p99={show(out['key_streak_p99'])} max={show(out['key_streak_max'])}")
    print(f"switched then 200: n={out['switch_success']} "
          f"p50={show(out['switch_success_p50_s'])}s p95={show(out['switch_success_p95_s'])}s (target p50 < 30s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
