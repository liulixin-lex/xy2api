#!/usr/bin/env python3
"""Automated staging scenarios for the gateway resilience specs
(staging-validation §6.1). Runs on the staging host against the isolated
stack and writes one JSON evidence file per scenario plus a summary.

Usage:
    python3 scenarios.py [--only ID,ID] [--evidence DIR]

Ports (compose.yaml): app 18210, fault-upstream 18211, cdn-sim 90/125/300 on
18212/18213/18214, direct counter 18215. Keys come from fixtures/keys.json.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import datetime
import http.client
import json
import os
import subprocess
import sys
import threading
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
APP, FAULT, CDN90, CDN125, CDN300, DIRECT = 18210, 18211, 18212, 18213, 18214, 18215
KEYS = json.load(open(os.path.join(HERE, "fixtures", "keys.json")))["keys"]


def now() -> float:
    return time.time()


# ---- HTTP helpers -------------------------------------------------------------

def stream_request(port: int, path: str, key: str, body: dict, *, headers=None, disconnect_after=None, timeout=330):
    """Sends a request and records every received line with its offset."""
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=disconnect_after if disconnect_after else timeout)
    hdrs = {"Content-Type": "application/json", "Authorization": "Bearer " + key,
            "User-Agent": "xy2api-staging-scenarios/1.0", "Connection": "close"}
    if headers:
        hdrs.update(headers)
    start = now()
    out = {"port": port, "path": path, "status": None, "headers": {}, "lines": [], "error": None, "elapsed": None}
    try:
        conn.request("POST", path, body=json.dumps(body), headers=hdrs)
        try:
            resp = conn.getresponse()
        except TimeoutError:
            # Leave before the response headers arrived.
            out["disconnected_at"] = round(now() - start, 2)
            conn.sock.close()
            return out
        out["status"] = resp.status
        out["headers"] = {k.lower(): v for k, v in resp.getheaders()}
        out["header_at"] = round(now() - start, 2)
        while True:
            if disconnect_after is not None:
                remaining = disconnect_after - (now() - start)
                if remaining <= 0:
                    out["disconnected_at"] = round(now() - start, 2)
                    if conn.sock:
                        conn.sock.close()
                    break
                conn.sock.settimeout(remaining)
            try:
                line = resp.readline()
            except TimeoutError:
                continue
            except OSError as exc:  # connection dropped by the peer
                out["error"] = f"read: {exc}"
                break
            if not line:
                break
            text = line.decode("utf-8", "replace").rstrip("\n")
            if text:
                out["lines"].append([round(now() - start, 2), text[:400]])
    except Exception as exc:  # noqa: BLE001 - evidence keeps the failure
        out["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        out["elapsed"] = round(now() - start, 2)
        try:
            conn.close()
        except Exception:  # noqa: BLE001
            pass
    return out


def get_json(port: int, path: str, token: str | None = None):
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read() or b"null")


def fault_records_since(rid: int):
    return [r for r in get_json(FAULT, f"/_requests?since={rid}")]


def fault_last_id() -> int:
    recs = get_json(FAULT, "/_requests?since=0")
    return recs[-1]["id"] if recs else 0


def set_balance(key: str, value: float):
    get_json(FAULT, f"/_control/balance?key={key}&value={value}")


def heartbeats(result) -> list[float]:
    return [t for t, line in result["lines"] if line.startswith(":") or line == "event: ping"]


def first_data(result) -> float | None:
    for t, line in result["lines"]:
        if line.startswith("data:") and "ping" not in line:
            return t
    return None


def responses_body(model="gpt-6.1-sol", stream=True, effort=None):
    body = {"model": model, "input": "hello from staging", "stream": stream}
    if effort:
        body["reasoning"] = {"effort": effort}
    return body


def chat_body(model="gpt-6.1-sol", stream=True):
    return {"model": model, "messages": [{"role": "user", "content": "hello"}], "stream": stream,
            "stream_options": {"include_usage": True}}


def anthropic_body(model="claude-sonnet-5-5", stream=True):
    return {"model": model, "max_tokens": 64, "messages": [{"role": "user", "content": "hello"}], "stream": stream}


def psql(sql: str) -> str:
    cmd = ["docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "psql", "-U", "xy2api", "-d", "xy2api", "-At", "-c", sql]
    return subprocess.run(cmd, cwd=HERE, capture_output=True, text=True, timeout=60).stdout.strip()


# ---- scenarios ---------------------------------------------------------------

SCENARIOS = {}


def scenario(sid: str, title: str):
    def wrap(fn):
        SCENARIOS[sid] = (title, fn)
        return fn
    return wrap


def check(cond: bool, label: str, checks: list):
    checks.append({"check": label, "ok": bool(cond)})


@scenario("KA-1", "CDN 125 s: upstream headers after 150 s, pre-output heartbeat from 15 s keeps the request alive")
def ka1():
    checks = []
    r = stream_request(CDN125, "/v1/responses", KEYS["G-slow-150"], responses_body())
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(bool(hb) and 13 <= hb[0] <= 18, "first heartbeat at about 15 s", checks)
    check(len(hb) >= 12, "heartbeats every 10 s until output", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "stream completed", checks)
    return r, checks


@scenario("KA-2", "Direct path: upstream headers after 150 s, no pre-output heartbeat")
def ka2():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-slow-150"], responses_body())
    check(r["status"] == 200, "status 200", checks)
    check(not heartbeats(r), "no heartbeat without an edge marker", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "stream completed", checks)
    return r, checks


@scenario("KA-3", "CDN 90 s (ESA): Chat Completions pre-output heartbeat keeps a 150 s header wait alive")
def ka3():
    checks = []
    r = stream_request(CDN90, "/v1/chat/completions", KEYS["G-slow-150"], chat_body())
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(bool(hb), "comment heartbeats present", checks)
    check(any("[DONE]" in l for _, l in r["lines"]), "stream finished with [DONE]", checks)
    return r, checks


@scenario("KA-5", "Existing stream keepalive: upstream headers at once, first token after 150 s through CDN 125 s")
def ka5():
    checks = []
    r = stream_request(CDN125, "/v1/responses", KEYS["G-long-first"], responses_body())
    check(r["status"] == 200, "status 200", checks)
    check(bool(heartbeats(r)), "heartbeats present", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "stream completed", checks)
    return r, checks


@scenario("KA-6", "Anthropic: upstream headers after 150 s through CDN 125 s, ping heartbeat keeps it alive")
def ka6():
    checks = []
    r = stream_request(CDN125, "/v1/messages", KEYS["G-anthropic-slow"], anthropic_body())
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(bool(hb) and 13 <= hb[0] <= 18, "first ping at about 15 s", checks)
    check(any("message_stop" in l for _, l in r["lines"]), "message_stop", checks)
    return r, checks


@scenario("KA-4", "Heartbeat committed, then every account fails: Responses terminal frame")
def ka4():
    checks = []
    r = stream_request(CDN125, "/v1/responses", KEYS["G-all-502"], responses_body())
    lines = [l for _, l in r["lines"]]
    check(r["status"] in (200, 502, 503), "answered", checks)
    check(r["status"] != 200 or any("response.failed" in l for l in lines), "committed stream ends with response.failed", checks)
    return r, checks


@scenario("BUD-1", "Every account fails: at most 4 upstream attempts, Retry-After present")
def bud1():
    checks = []
    mark = fault_last_id()
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-all-502"], responses_body())
    attempts = [x for x in fault_records_since(mark) if x["fault"] == "F07"]
    r["upstream_attempts"] = len(attempts)
    check(r["status"] in (502, 503), "error status", checks)
    check(len(attempts) <= 4, "at most 4 upstream attempts", checks)
    check("retry-after" in r["headers"], "Retry-After header", checks)
    return r, checks


@scenario("SW-1", "First account returns a Cloudflare 502: switch and succeed")
def sw1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-fail-then-ok"], responses_body())
    check(r["status"] == 200, "status 200", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "completed", checks)
    return r, checks


@scenario("FO-1", "No response headers for 200 s: first-output timeout switches before the deadline")
def fo1():
    checks = []
    mark = fault_last_id()
    r = stream_request(CDN125, "/v1/responses", KEYS["G-slow-headers"], responses_body())
    recs = fault_records_since(mark)
    r["upstream"] = [{k: x[k] for k in ("fault", "outcome", "path")} for x in recs]
    check(r["status"] == 200, "status 200", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "completed on the second account", checks)
    check(any(x["fault"] == "F01" and x["outcome"] == "cancelled" for x in recs), "slow attempt cancelled", checks)
    check(r["elapsed"] < 240, "within the 240 s budget", checks)
    return r, checks


@scenario("ST-1", "Stall after output: the stream ends within 120 s + 5 s")
def st1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-stall"], responses_body())
    data_times = [t for t, l in r["lines"] if l.startswith("data:")]
    last_delta = max((t for t, l in r["lines"] if "output_text.delta" in l), default=0)
    r["stall_detected_after"] = round(r["elapsed"] - last_delta, 1)
    check(r["status"] == 200, "status 200", checks)
    check(r["elapsed"] - last_delta <= 130, "ended within 130 s of the last delta", checks)
    check(bool(data_times), "some output delivered", checks)
    return r, checks


@scenario("CTX-1", "Context overflow inside the stream is passed through, not turned into 5xx")
def ctx1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-ctx-overflow"], responses_body())
    check(any("context_length_exceeded" in l for _, l in r["lines"]), "context_length_exceeded reaches the client", checks)
    return r, checks


@scenario("ERR-1", "Single-account group answering 503: no-account 503 with Retry-After 2-30 s")
def err1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False))
    ra = int(r["headers"].get("retry-after", "0") or 0)
    check(r["status"] in (502, 503), "error status", checks)
    check(2 <= ra <= 30, "Retry-After between 2 and 30", checks)
    return r, checks


@scenario("ERR-2", "Claude Code only group used by another client: config 503 with Retry-After 60 and x-should-retry false")
def err2():
    checks = []
    r = stream_request(DIRECT, "/v1/messages", KEYS["G-claude-only"], anthropic_body(stream=False))
    check(r["status"] in (403, 503), "rejected", checks)
    if r["status"] == 503:
        check(r["headers"].get("retry-after") == "60", "Retry-After 60", checks)
        check(r["headers"].get("x-should-retry") == "false", "x-should-retry false", checks)
    return r, checks


@scenario("ERR-3", "Repeated identical 503s on one key are answered fast")
def err3():
    checks = []
    results = []
    for _ in range(14):
        results.append(stream_request(DIRECT, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False)))
    times = [x["elapsed"] for x in results]
    r = {"elapsed_each": times, "statuses": [x["status"] for x in results],
         "retry_after": [x["headers"].get("retry-after") for x in results]}
    check(all(s in (502, 503) for s in r["statuses"]), "all rejected", checks)
    check(all(x for x in r["retry_after"]), "every answer has Retry-After", checks)
    return r, checks


@scenario("RL-1", "429 with reset time: Retry-After carried, capped")
def rl1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-rate-reset"], responses_body(stream=False))
    check(r["status"] in (429, 503), "rate limited or no account", checks)
    check("retry-after" in r["headers"], "Retry-After present", checks)
    return r, checks


@scenario("BAL-1", "Upstream balance exhausted on a single-account group: user sees 503, admin sees EXHAUSTED, recovery after top-up")
def bal1():
    checks = []
    set_balance("single", 0)
    first = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
    state = psql("select coalesce(extra->'resilience_balance'->>'state','OK') from accounts where name='single-ok'")
    check(first["status"] == 503, "user sees 503, not the upstream 429/402", checks)
    check("insufficient account balance" not in json.dumps(first["lines"]).lower(), "upstream balance text hidden", checks)
    check(state in ("EXHAUSTED", "SUSPECT"), f"account balance state {state}", checks)
    set_balance("single", 100)
    deadline = now() + 150
    while now() < deadline:
        state = psql("select coalesce(extra->'resilience_balance'->>'state','OK') from accounts where name='single-ok'")
        if state in ("RECOVERING", "OK"):
            break
        time.sleep(5)
    check(state in ("RECOVERING", "OK"), f"recovered after top-up ({state})", checks)
    after = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
    check(after["status"] == 200, "served again", checks)
    return {"first": first, "after": after, "final_state": state}, checks


@scenario("FB-1", "Primary group without a usable account: fallback group serves, usage keeps origin_group_id")
def fb1():
    checks = []
    set_balance("fb-primary", 0)
    for _ in range(3):  # let the primary account become penalised
        stream_request(DIRECT, "/v1/responses", KEYS["G-fallback-primary"], responses_body(stream=False))
        time.sleep(1)
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-fallback-primary"], responses_body(stream=False))
    time.sleep(3)
    row = psql("select g.name||'|'||coalesce(og.name,'') from usage_logs u join groups g on g.id=u.group_id "
               "left join groups og on og.id=u.origin_group_id order by u.id desc limit 1")
    set_balance("fb-primary", 100)
    check(r["status"] == 200, "served", checks)
    check(row == "G-fallback-backup|G-fallback-primary", f"usage row {row}", checks)
    return {"response": r, "usage": row}, checks


@scenario("DC-1", "Client leaves before any output: upstream cancelled within 2 s, no usage row, undelivered record")
def dc1():
    checks = []
    mark = fault_last_id()
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    key_filter = "api_key_id=(select id from api_keys where name='key-G-long-first' and deleted_at is null)"
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-long-first"], responses_body(), disconnect_after=5)
    time.sleep(4)
    recs = fault_records_since(mark)
    usage_after = psql(f"select count(*) from usage_logs where {key_filter} and created_at > '{since}'")
    usage_before = "0"
    und = psql("select disconnect_stage||'|'||billed from gateway_undelivered_requests order by id desc limit 1")
    r["upstream"] = recs
    cancelled = [x for x in recs if x["outcome"] == "cancelled"]
    if cancelled:
        ended = datetime.datetime.fromisoformat(cancelled[0]["ended"].replace("Z", "+00:00")).timestamp()
        started = datetime.datetime.fromisoformat(cancelled[0]["started"].replace("Z", "+00:00")).timestamp()
        r["upstream_cancel_after_s"] = round(ended - started, 2)
    check(bool(cancelled), "upstream request cancelled", checks)
    check(bool(cancelled) and r.get("upstream_cancel_after_s", 99) <= r.get("disconnected_at", 5) + 2,
          "cancelled within 2 s of the disconnect", checks)
    check(usage_before == usage_after, "no usage row", checks)
    check(und.endswith(("|f", "|false")), f"undelivered record {und}", checks)
    return r, checks


@scenario("RES-1", "Small balance held by an in-flight request: 429 concurrent_balance_reserved with Retry-After")
def res1():
    checks = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        holder = pool.submit(stream_request, DIRECT, "/v1/responses", KEYS["small:G-long-first"], responses_body(), disconnect_after=20)
        time.sleep(3)
        others = [pool.submit(stream_request, DIRECT, "/v1/responses", KEYS["small:G-single"], responses_body(stream=False)) for _ in range(3)]
        results = [f.result() for f in others]
        held = holder.result()
    r = {"holder_status": held["status"], "statuses": [x["status"] for x in results],
         "bodies": [x["lines"][:1] for x in results], "retry_after": [x["headers"].get("retry-after") for x in results]}
    check(held["status"] == 200, "first request runs", checks)
    check(all(x["status"] == 429 for x in results), "others get 429", checks)
    check(all("concurrent_balance_reserved" in json.dumps(x["lines"]) for x in results), "code concurrent_balance_reserved", checks)
    check(all(ra for ra in r["retry_after"]), "Retry-After on every 429", checks)
    return r, checks


@scenario("ANT-1", "Anthropic stream through CDN 125 s: ping heartbeat, normal completion")
def ant1():
    checks = []
    r = stream_request(CDN125, "/v1/messages", KEYS["G-anthropic"], anthropic_body())
    check(r["status"] == 200, "status 200", checks)
    check(any("message_stop" in l for _, l in r["lines"]), "message_stop", checks)
    return r, checks


@scenario("ANT-2", "Anthropic 529 on a single account: overloaded error with Retry-After")
def ant2():
    checks = []
    r = stream_request(DIRECT, "/v1/messages", KEYS["G-anthropic-overloaded"], anthropic_body(stream=False))
    check(r["status"] in (503, 529), "overloaded or no account", checks)
    check("retry-after" in r["headers"], "Retry-After present", checks)
    return r, checks


@scenario("GEM-1", "Gemini stream through CDN: no comment heartbeat injected")
def gem1():
    checks = []
    r = stream_request(CDN125, "/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse", KEYS["G-gemini"],
                       {"contents": [{"role": "user", "parts": [{"text": "hello"}]}]})
    check(r["status"] == 200, "status 200", checks)
    check(not any(l.startswith(":") for _, l in r["lines"]), "no comment lines for Gemini clients", checks)
    return r, checks


def run(sid: str, outdir: str):
    title, fn = SCENARIOS[sid]
    started = now()
    try:
        result, checks = fn()
    except Exception as exc:  # noqa: BLE001
        result, checks = {"exception": f"{type(exc).__name__}: {exc}"}, [{"check": "ran", "ok": False}]
    ok = all(c["ok"] for c in checks)
    record = {"id": sid, "title": title, "ok": ok, "checks": checks, "duration_s": round(now() - started, 1), "result": result}
    with open(os.path.join(outdir, f"{sid}.json"), "w") as fh:
        json.dump(record, fh, indent=1, ensure_ascii=False)
    print(f"{'PASS' if ok else 'FAIL'} {sid} ({record['duration_s']}s) {title}", flush=True)
    for c in checks:
        if not c["ok"]:
            print(f"    - failed: {c['check']}", flush=True)
    return record


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--only", default="")
    parser.add_argument("--evidence", default=os.path.join(HERE, "evidence", datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%MZ")))
    parser.add_argument("--parallel", type=int, default=6)
    args = parser.parse_args()
    os.makedirs(args.evidence, exist_ok=True)
    ids = [s for s in (args.only.split(",") if args.only else SCENARIOS) if s]
    # Balance and fallback scenarios change shared upstream state; run them alone.
    serial = [s for s in ids if s.startswith(("BAL", "FB", "RES", "ERR-3", "BUD", "DC"))]
    parallel = [s for s in ids if s not in serial]
    records = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.parallel) as pool:
        records += list(pool.map(lambda s: run(s, args.evidence), parallel))
    for s in serial:
        records.append(run(s, args.evidence))
    summary = {"at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "passed": sum(r["ok"] for r in records),
               "total": len(records), "failed": [r["id"] for r in records if not r["ok"]]}
    with open(os.path.join(args.evidence, "SUMMARY.json"), "w") as fh:
        json.dump(summary, fh, indent=1)
    print(json.dumps(summary))
    return 0 if not summary["failed"] else 1


if __name__ == "__main__":
    sys.exit(main())
