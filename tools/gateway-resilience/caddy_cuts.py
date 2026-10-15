#!/usr/bin/env python3
"""CDN cut statistics from Caddy JSON access logs (design §14).

A "cut" is an API request that Caddy logged with status 0 or 499, no response
bytes and a duration close to a CDN timeout (90, 125 or 300 seconds). Rates
are reported per entry: Cloudflare, ESA or direct.

With --app, every cut is matched to the application's "http request
completed" line (same client IP and path, start times within 2 seconds); a
matched 200 means the request finished, and was billed, after the client had
already been cut off.

Also reports how many API 503 responses carried a Retry-After header.

Only aggregates are printed. Usage:
    caddy_cuts.py [--app app.log ...] caddy.log [caddy.log.gz ...]
"""

from __future__ import annotations

import argparse
import collections
import datetime
import gzip
import json
import sys
from typing import Iterator

API_PREFIXES = ("/v1", "/v1beta", "/responses", "/chat", "/backend-api")
CDN_TIMEOUTS = (90, 125, 300)
CUT_TOLERANCE = 5.0
MATCH_WINDOW = 2.0


def open_text(path: str):
    if path.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8", errors="replace")
    return open(path, encoding="utf-8", errors="replace")


def json_lines(paths: list[str]) -> Iterator[dict]:
    for path in paths:
        with open_text(path) as fh:
            for line in fh:
                line = line.strip()
                if not line.startswith("{"):
                    continue
                try:
                    yield json.loads(line)
                except json.JSONDecodeError:
                    continue


def first(values) -> str:
    if isinstance(values, list):
        return str(values[0]) if values else ""
    return str(values or "")


def entry_of(headers: dict) -> str:
    keys = {k.lower() for k in headers}
    if "x-xy-edge" in keys:
        for k, v in headers.items():
            if k.lower() == "x-xy-edge":
                return first(v) or "marked"
    if "cf-ray" in keys:
        return "cf"
    if "eagleeye-traceid" in keys or any(k.startswith("ali-") for k in keys):
        return "esa"
    return "direct"


def cut_bucket(duration: float) -> str:
    for timeout in CDN_TIMEOUTS:
        if abs(duration - timeout) <= CUT_TOLERANCE:
            return f"{timeout}s"
    return ""


def caddy_rows(paths: list[str]) -> list[dict]:
    rows = []
    for d in json_lines(paths):
        req = d.get("request") or {}
        path = (req.get("uri") or "").split("?")[0]
        if not path.startswith(API_PREFIXES):
            continue
        duration = float(d.get("duration") or 0)
        status = int(d.get("status") or 0)
        resp_headers = d.get("resp_headers") or {}
        rows.append({
            "ts": float(d.get("ts") or 0),
            "start": float(d.get("ts") or 0) - duration,
            "duration": duration,
            "status": status,
            "size": int(d.get("size") or 0),
            "ip": req.get("client_ip") or req.get("remote_ip") or "",
            "path": path,
            "entry": entry_of(req.get("headers") or {}),
            "retry_after": any(k.lower() == "retry-after" for k in resp_headers),
        })
    return rows


def parse_app_time(value: str) -> float | None:
    for fmt in ("%Y-%m-%dT%H:%M:%S.%f%z", "%Y-%m-%dT%H:%M:%S%z"):
        try:
            return datetime.datetime.strptime(value, fmt).timestamp()
        except (TypeError, ValueError):
            continue
    return None


def app_completions(paths: list[str]) -> list[dict]:
    done = []
    for d in json_lines(paths):
        if d.get("msg") != "http request completed":
            continue
        end = parse_app_time(d.get("time") or d.get("ts") or "")
        if end is None:
            continue
        latency = float(d.get("latency_ms") or 0) / 1000
        done.append({
            "start": end - latency,
            "status": int(d.get("status_code") or 0),
            "ip": d.get("client_ip") or "",
            "path": d.get("path") or "",
        })
    return done


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--app", action="append", default=[], help="application JSON log (repeatable)")
    parser.add_argument("caddy", nargs="+", help="Caddy JSON access log(s)")
    args = parser.parse_args()

    rows = caddy_rows(args.caddy)
    if not rows:
        print("no API rows found", file=sys.stderr)
        return 1
    span = (datetime.datetime.fromtimestamp(min(r["ts"] for r in rows), datetime.timezone.utc),
            datetime.datetime.fromtimestamp(max(r["ts"] for r in rows), datetime.timezone.utc))
    print(f"window: {span[0]:%Y-%m-%dT%H:%MZ} -> {span[1]:%Y-%m-%dT%H:%MZ}, API rows: {len(rows)}")

    cuts = [r for r in rows if r["status"] in (0, 499) and r["size"] == 0 and cut_bucket(r["duration"])]
    print("\ncut rate by entry (target < 0.3%):")
    total = collections.Counter(r["entry"] for r in rows)
    cut_by_entry = collections.Counter(r["entry"] for r in cuts)
    for entry, n in total.most_common():
        c = cut_by_entry.get(entry, 0)
        print(f"  {entry:8s} requests={n:7d} cuts={c:5d} rate={100.0 * c / n:.2f}%")
    print("cuts by timeout:", dict(collections.Counter(cut_bucket(r["duration"]) for r in cuts)))

    s503 = [r for r in rows if r["status"] == 503]
    if s503:
        with_hint = sum(1 for r in s503 if r["retry_after"])
        print(f"\n503 with Retry-After: {with_hint}/{len(s503)} ({100.0 * with_hint / len(s503):.1f}%; target 100%)")

    if args.app:
        done = app_completions(args.app)
        result = collections.Counter()
        for cut in cuts:
            candidates = [a for a in done if a["ip"] == cut["ip"] and a["path"] == cut["path"]
                          and abs(a["start"] - cut["start"]) < MATCH_WINDOW]
            if not candidates:
                result["unmatched"] += 1
                continue
            best = min(candidates, key=lambda a: abs(a["start"] - cut["start"]))
            result["app_200_after_cut" if best["status"] == 200 else f"app_{best['status']}"] += 1
        print("\ncuts matched to application completions (target app_200_after_cut = 0):")
        for key, n in result.most_common():
            print(f"  {key}: {n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
