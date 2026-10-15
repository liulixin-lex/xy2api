#!/usr/bin/env python3
"""Session failure streaks and switch latency from application JSON logs
(design §14).

- Streaks: per (session, account), the number of consecutive failed attempts
  before a success on that account or the end of the window. The session is
  the request's session_hash when logged, otherwise its API key.
- Switch latency: end-to-end latency of requests that switched accounts at
  least once and finished with 200.

Lines are joined by request_id. Only aggregates are printed. Usage:
    session_streaks.py app.log [app.log.gz ...]
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
        return "-"
    values = sorted(values)
    return values[min(len(values) - 1, int(q * len(values)))]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("logs", nargs="+", help="application JSON log(s)")
    args = parser.parse_args()

    requests: dict[str, dict] = collections.defaultdict(lambda: {"failures": []})
    for path in args.logs:
        with open_text(path) as fh:
            for line in fh:
                if not line.startswith("{"):
                    continue
                try:
                    d = json.loads(line)
                except json.JSONDecodeError:
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
        session = r.get("session_hash") or (f"key:{r['api_key_id']}" if r.get("api_key_id") else None)
        if session:
            for ts, account in r["failures"]:
                events[(session, account)].append((ts, False))
            if r.get("status") == 200 and r.get("account_id"):
                events[(session, r["account_id"])].append((r["end"], True))
        if r["failures"] and r.get("status") == 200:
            switch_latency.append(r["latency"])

    streaks = []
    for seq in events.values():
        run = 0
        for _, ok in sorted(seq):
            if ok:
                if run:
                    streaks.append(run)
                run = 0
            else:
                run += 1
        if run:
            streaks.append(run)

    print(f"requests: {len(requests)}, session/account pairs: {len(events)}")
    print(f"consecutive failures on one account: runs={len(streaks)} "
          f"p50={percentile(streaks, 0.5)} p99={percentile(streaks, 0.99)} max={max(streaks) if streaks else '-'} (target p99 <= 2)")
    print(f"switched then 200: n={len(switch_latency)} "
          f"p50={percentile(switch_latency, 0.5)}s p95={percentile(switch_latency, 0.95)}s (target p50 < 30s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
