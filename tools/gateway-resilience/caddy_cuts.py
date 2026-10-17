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

Only aggregates are printed (--json: one JSON object). Usage:
    caddy_cuts.py [--json] [--app app.log ...] caddy.log [caddy.log.gz ...]
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
    for path in paths:
        with open_text(path) as fh:
            for line in fh:
                d = app_line(line)
                if d is None or d.get("msg") != "http request completed":
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


def analyse(caddy_paths: list[str], app_paths: list[str]) -> dict:
    """Cut and Retry-After aggregates; empty when no API row was found."""
    rows = caddy_rows(caddy_paths)
    if not rows:
        return {}
    cuts = [r for r in rows if r["status"] in (0, 499) and r["size"] == 0 and cut_bucket(r["duration"])]
    total = collections.Counter(r["entry"] for r in rows)
    cut_by_entry = collections.Counter(r["entry"] for r in cuts)
    out = {
        "since": min(r["ts"] for r in rows),
        "until": max(r["ts"] for r in rows),
        "api_rows": len(rows),
        "entries": {entry: {"requests": n, "cuts": cut_by_entry.get(entry, 0),
                            "cut_rate_pct": round(100.0 * cut_by_entry.get(entry, 0) / n, 3)}
                    for entry, n in total.most_common()},
        "cuts_by_timeout": dict(collections.Counter(cut_bucket(r["duration"]) for r in cuts)),
    }
    s503 = [r for r in rows if r["status"] == 503]
    if s503:
        with_hint = sum(1 for r in s503 if r["retry_after"])
        out["status_503"] = len(s503)
        out["status_503_retry_after_pct"] = round(100.0 * with_hint / len(s503), 1)
    if app_paths:
        done = app_completions(app_paths)
        result = collections.Counter()
        for cut in cuts:
            candidates = [a for a in done if a["ip"] == cut["ip"] and a["path"] == cut["path"]
                          and abs(a["start"] - cut["start"]) < MATCH_WINDOW]
            if not candidates:
                result["unmatched"] += 1
                continue
            best = min(candidates, key=lambda a: abs(a["start"] - cut["start"]))
            result["app_200_after_cut" if best["status"] == 200 else f"app_{best['status']}"] += 1
        out["cut_matches"] = dict(result)
        out["app_200_after_cut"] = result.get("app_200_after_cut", 0)
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--app", action="append", default=[], help="application JSON log (repeatable)")
    parser.add_argument("--json", action="store_true", help="print one JSON object instead of text")
    parser.add_argument("caddy", nargs="+", help="Caddy JSON access log(s)")
    args = parser.parse_args()

    out = analyse(args.caddy, args.app)
    if not out:
        print("no API rows found", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(out))
        return 0
    span = (datetime.datetime.fromtimestamp(out["since"], datetime.timezone.utc),
            datetime.datetime.fromtimestamp(out["until"], datetime.timezone.utc))
    print(f"window: {span[0]:%Y-%m-%dT%H:%MZ} -> {span[1]:%Y-%m-%dT%H:%MZ}, API rows: {out['api_rows']}")
    print("\ncut rate by entry (target < 0.3%):")
    for entry, e in out["entries"].items():
        print(f"  {entry:8s} requests={e['requests']:7d} cuts={e['cuts']:5d} rate={e['cut_rate_pct']:.2f}%")
    print("cuts by timeout:", out["cuts_by_timeout"])
    if "status_503" in out:
        print(f"\n503 with Retry-After: {out['status_503_retry_after_pct']}% of {out['status_503']} (target 100%)")
    if "cut_matches" in out:
        print("\ncuts matched to application completions (target app_200_after_cut = 0):")
        for key, n in sorted(out["cut_matches"].items(), key=lambda kv: -kv[1]):
            print(f"  {key}: {n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
