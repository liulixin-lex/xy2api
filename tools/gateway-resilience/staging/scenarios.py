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
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
APP, FAULT, CDN90, CDN125, CDN300, DIRECT = 18210, 18211, 18212, 18213, 18214, 18215
# Real Cloudflare (orange cloud) and the DNS-only host of the same Caddy site,
# from the staging .env (STG_CF_HOST, STG_DIRECT_HOST).
REAL_CF, REAL_DIRECT = os.environ.get("STG_CF_HOST", ""), os.environ.get("STG_DIRECT_HOST", "")
KEYS = json.load(open(os.path.join(HERE, "fixtures", "keys.json")))["keys"]


def now() -> float:
    return time.time()


# ---- HTTP helpers -------------------------------------------------------------

def stream_request(port, path: str, key: str, body: dict, *, headers=None, disconnect_after=None, timeout=330):
    """Sends a request and records every received line with its offset.
    port is a local port, or an https host name (real Cloudflare tests)."""
    if isinstance(port, str):
        conn = http.client.HTTPSConnection(port, 443, timeout=disconnect_after if disconnect_after else timeout)
    else:
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


def responses_body(model="gpt-6.1-sol", stream=True, effort=None, session=None):
    body = {"model": model, "input": "hello from staging", "stream": stream}
    if effort:
        body["reasoning"] = {"effort": effort}
    if session:
        # Own conversation: the failed-turn cap counts per conversation, and
        # identical bodies without one share a content-derived session.
        body["prompt_cache_key"] = session
    return body


def fresh_session() -> str:
    return "stg-" + uuid.uuid4().hex


def chat_body(model="gpt-6.1-sol", stream=True):
    return {"model": model, "messages": [{"role": "user", "content": "hello"}], "stream": stream,
            "stream_options": {"include_usage": True}}


def anthropic_body(model="claude-sonnet-5-5", stream=True):
    return {"model": model, "max_tokens": 64, "messages": [{"role": "user", "content": "hello"}], "stream": stream}


def redis_cli(*args: str) -> str:
    cmd = ["docker", "compose", "-p", "xy2api-staging", "exec", "-T", "redis", "redis-cli", *args]
    return subprocess.run(cmd, cwd=HERE, capture_output=True, text=True, timeout=60).stdout.strip()


def admin_api():
    from fixtures import API  # same directory
    api = API(f"http://127.0.0.1:{APP}")
    api.token = api.login(os.environ["ADMIN_EMAIL"], os.environ["ADMIN_PASSWORD"])
    return api


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


def cdn_cuts(port: int) -> list[dict]:
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/_cuts", timeout=10) as resp:
        return json.loads(resp.read() or b"[]") or []


@scenario("KA-8", "CDN 300 s: upstream headers after 280 s; heartbeats and the 240 s budget end the request before the edge")
def ka8():
    checks = []
    before = len(cdn_cuts(CDN300))
    r = stream_request(CDN300, "/v1/responses", KEYS["G-slow-280"], responses_body(session=fresh_session()))
    hb = heartbeats(r)
    lines = [l for _, l in r["lines"]]
    gaps = [b - a for a, b in zip([0.0] + hb, hb)]
    r["cuts"] = cdn_cuts(CDN300)[before:]
    check(r["status"] == 200, "status 200 (committed by heartbeats)", checks)
    check(bool(hb) and max(gaps) <= 20, "no silence longer than 20 s before the end", checks)
    check(any("response.failed" in l for l in lines), "ends with a retryable response.failed", checks)
    check(r["elapsed"] < 260, "ended by the 240 s budget, before the 280 s upstream and the 300 s edge", checks)
    check(not r["cuts"], "the edge cut nothing", checks)
    return r, checks


@scenario("CONT-1", "Claude dispatched to a pooled relay that rejects previous_response_id: every turn succeeds")
def cont1():
    checks = []
    if "R-chain-reject" not in KEYS:
        return {"skipped": "needs .env.real"}, [{"check": "R-chain-reject fixture present", "ok": False}]
    prompt = "Reply with one word: pong " + uuid.uuid4().hex[:8]
    body = {"model": "claude-sonnet-5-5", "max_tokens": 64, "stream": True, "messages": [{"role": "user", "content": prompt}]}
    mark = fault_last_id()
    turns = [stream_request(DIRECT, "/v1/messages", KEYS["R-chain-reject"], body) for _ in range(3)]
    recs = [x for x in fault_records_since(mark) if x["fault"] == "F25"]
    result = {"statuses": [t["status"] for t in turns], "upstream": [x["outcome"] for x in recs]}
    check(all(t["status"] == 200 and any("message_stop" in l for _, l in t["lines"]) for t in turns), "all three turns answered", checks)
    check(result["upstream"].count("error_status") == 1, "one rejected chain, then continuation stays off", checks)
    return result, checks


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
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-all-502"], responses_body(session=fresh_session()))
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
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False, session=fresh_session()))
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


@scenario("ERR-3", "One conversation failing repeatedly: Retry-After four times, then a final answer")
def err3():
    checks = []
    session = fresh_session()
    results = [stream_request(DIRECT, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False, session=session))
               for _ in range(6)]
    r = {"statuses": [x["status"] for x in results],
         "retry_after": [x["headers"].get("retry-after") for x in results],
         "should_retry": [x["headers"].get("x-should-retry") for x in results],
         "overloaded": [any("server_is_overloaded" in l for _, l in x["lines"]) for x in results]}
    check(all(s in (502, 503) for s in r["statuses"][:4]), "first four rejected", checks)
    check(all(r["retry_after"][:4]), "first four carry Retry-After", checks)
    check(r["statuses"][4:] == [503, 503], "then 503", checks)
    check(not any(r["retry_after"][4:]), "final answers have no Retry-After", checks)
    check(r["should_retry"][4:] == ["false", "false"], "final answers say x-should-retry false", checks)
    check(all(r["overloaded"][4:]), "final answers carry server_is_overloaded", checks)
    return r, checks


@scenario("ERR-4", "Many conversations of one key failing once each: every answer keeps Retry-After")
def err4():
    checks = []
    results = [stream_request(DIRECT, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False, session=fresh_session()))
               for _ in range(14)]
    r = {"statuses": [x["status"] for x in results],
         "retry_after": [x["headers"].get("retry-after") for x in results],
         "fast_fail": [x["headers"].get("x-xy-fast-fail") for x in results],
         "elapsed_each": [x["elapsed"] for x in results]}
    check(all(s in (502, 503) for s in r["statuses"]), "all rejected", checks)
    check(all(r["retry_after"]), "every answer has Retry-After", checks)
    return r, checks


@scenario("RL-1", "429 with reset time: Retry-After carried, capped")
def rl1():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-rate-reset"], responses_body(stream=False, session=fresh_session()))
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


@scenario("PE-1", "Grok relay 429 'insufficient balance' with an exhausted window: balance state, no 28-minute rate limit, recovery after top-up")
def pe1():
    checks = []
    set_balance("grok-bal", 0)
    first = stream_request(DIRECT, "/v1/responses", KEYS["G-grok-balance"], responses_body(model="grok-4.6", stream=False))
    row = psql("select coalesce(extra->'resilience_balance'->>'state','OK') || '|' || "
               "coalesce(extract(epoch from rate_limit_reset_at - now())::int::text, '') from accounts where name='grok-bal'")
    state, _, left = row.partition("|")
    check(first["status"] >= 400, f"the request fails ({first['status']})", checks)
    check(state in ("EXHAUSTED", "SUSPECT"), f"account balance state {state}", checks)
    check(left == "" or int(left) < 300, f"no rate limit from the relay's 1700 s window (left {left or 'none'})", checks)
    set_balance("grok-bal", 100)
    deadline = now() + 150
    while now() < deadline:
        state = psql("select coalesce(extra->'resilience_balance'->>'state','OK') from accounts where name='grok-bal'")
        if state in ("RECOVERING", "OK"):
            break
        time.sleep(5)
    check(state in ("RECOVERING", "OK"), f"recovered after top-up ({state})", checks)
    after = stream_request(DIRECT, "/v1/responses", KEYS["G-grok-balance"], responses_body(model="grok-4.6", stream=False))
    check(after["status"] == 200, "served again", checks)
    return {"first": first, "after": after, "final_state": state}, checks


@scenario("PE-4", "Upstream content-safety 403: client gets 400 with the upstream text, one attempt, the account keeps serving")
def pe4():
    checks = []
    rid = fault_last_id()
    res = stream_request(DIRECT, "/v1/responses", KEYS["G-content-safety"], responses_body(stream=False))
    attempts = [r for r in fault_records_since(rid) if r["fault"] in ("F27", "F00")]
    text = json.dumps(res["lines"])
    row = psql("select status || '|' || coalesce((temp_unschedulable_until > now())::text, 'false') from accounts where name='cs-block'")
    check(res["status"] == 400, f"client gets 400 ({res['status']})", checks)
    check("content_policy_violation" in text and "preflight safety" in text, "code content_policy_violation and the upstream text", checks)
    check(len(attempts) == 1, f"one upstream attempt, no retry or switch ({len(attempts)})", checks)
    check(row == "active|false", f"account not penalised ({row})", checks)
    return {"res": res, "attempts": len(attempts), "account": row}, checks


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


@scenario("FB-2", "Failover exhaustion stays in the primary group; the next request goes to the fallback group")
def fb2():
    checks = []
    session = fresh_session()
    first = stream_request(DIRECT, "/v1/responses", KEYS["G-fb2-primary"], responses_body(stream=False, session=session))
    time.sleep(1)
    second = stream_request(DIRECT, "/v1/responses", KEYS["G-fb2-primary"], responses_body(stream=False, session=session))
    time.sleep(3)
    row = psql("select g.name||'|'||coalesce(og.name,'') from usage_logs u join groups g on g.id=u.group_id "
               "left join groups og on og.id=u.origin_group_id order by u.id desc limit 1")
    check(first["status"] in (502, 503), "first request ends with the primary group's error", checks)
    check(second["status"] == 200, "next request served", checks)
    check(row == "G-fb2-backup|G-fb2-primary", f"served by the fallback group: {row}", checks)
    return {"first": first, "second": second, "usage": row}, checks


@scenario("KA-7", "Relay keep-alive deltas before any output: dropped, and the gateway still switches accounts")
def ka7():
    checks = []
    mark = fault_last_id()
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-ka-relay"], responses_body())
    recs = fault_records_since(mark)
    lines = [l for _, l in r["lines"]]
    first_data = next((l for l in lines if l.startswith("data:")), "")
    r["upstream"] = [x["fault"] for x in recs]
    check(r["status"] == 200, "status 200", checks)
    check(not any("SSE-Keep-Alive" in l for l in lines), "no keep-alive frame reaches the client", checks)
    check('"response.created"' in first_data, "first event is response.created", checks)
    check(any("response.completed" in l for l in lines), "completed by the next account", checks)
    check("F24" in r["upstream"] and "F00" in r["upstream"], "switched from the keep-alive relay", checks)
    return r, checks


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
    api = admin_api()
    from fixtures import items
    user = next(u for u in items(api.call("GET", "/api/v1/admin/users?page=1&page_size=200"))
                if u["email"] == "stg-small@xy2api-staging.local")
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        holder = pool.submit(stream_request, DIRECT, "/v1/responses", KEYS["small:G-long-first"],
                             responses_body(session=fresh_session()), disconnect_after=20)
        time.sleep(3)
        # The estimate depends on pricing and the recent output P99; size the
        # balance so that the holder's reservation fits and one more does not.
        held_amounts = redis_cli("HVALS", f"billing:inflight_amt:{{{user['id']}}}").split()
        estimate = max((float(x) for x in held_amounts), default=0.0)
        api.call("POST", f"/api/v1/admin/users/{user['id']}/balance",
                 {"balance": round(estimate * 1.5, 6), "operation": "set", "notes": "staging RES-1"})
        others = [pool.submit(stream_request, DIRECT, "/v1/responses", KEYS["small:G-single"],
                              responses_body(stream=False, session=fresh_session())) for _ in range(3)]
        results = [f.result() for f in others]
        held = holder.result()
    api.call("POST", f"/api/v1/admin/users/{user['id']}/balance", {"balance": 0.12, "operation": "set", "notes": "staging RES-1"})
    r = {"estimate": estimate, "holder_status": held["status"], "statuses": [x["status"] for x in results],
         "bodies": [x["lines"][:1] for x in results], "retry_after": [x["headers"].get("retry-after") for x in results]}
    check(estimate > 0, "the holder reserved its estimate", checks)
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


@scenario("CF-1", "Real Cloudflare: upstream headers after 150 s, heartbeat keeps the request past the 100-125 s edge timeout")
def cf1():
    checks = []
    r = stream_request(REAL_CF, "/v1/responses", KEYS["G-slow-150"], responses_body())
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(r["headers"].get("server") == "cloudflare", "answered through Cloudflare", checks)
    check(bool(hb) and hb[0] <= 20, "heartbeat from about 15 s", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "completed after 150 s", checks)
    return r, checks


@scenario("CF-2", "DNS-only host: no edge marker, so no early heartbeat")
def cf2():
    checks = []
    r = stream_request(REAL_DIRECT, "/v1/responses", KEYS["G-slow-150"], responses_body())
    check(r["status"] == 200, "status 200", checks)
    check(not heartbeats(r) or heartbeats(r)[0] >= 140, "no pre-output heartbeat", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "completed", checks)
    return r, checks


@scenario("CF-3", "Real Cloudflare passes the gateway's Retry-After")
def cf3():
    checks = []
    r = stream_request(REAL_CF, "/v1/responses", KEYS["G-503-single"], responses_body(stream=False, session=fresh_session()))
    ra = int(r["headers"].get("retry-after", "0") or 0)
    check(r["status"] in (502, 503), "error status", checks)
    check(2 <= ra <= 30, f"Retry-After {ra} reached the client", checks)
    return r, checks


@scenario("CF-4", "Real Cloudflare: Anthropic slow headers kept alive with ping events")
def cf4():
    checks = []
    r = stream_request(REAL_CF, "/v1/messages", KEYS["G-anthropic-slow"], anthropic_body())
    check(r["status"] == 200, "status 200", checks)
    check(any(l == "event: ping" for _, l in r["lines"]), "ping heartbeats", checks)
    check(any("message_stop" in l for _, l in r["lines"]), "message_stop", checks)
    return r, checks


def admin_token() -> str:
    env = {}
    for line in open(os.path.join(HERE, ".env")):
        if "=" in line:
            k, v = line.rstrip("\n").split("=", 1)
            env[k] = v
    req = urllib.request.Request(f"http://127.0.0.1:{APP}/api/v1/auth/login", method="POST",
                                 data=json.dumps({"email": env["ADMIN_EMAIL"], "password": env["ADMIN_PASSWORD"]}).encode())
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())["data"]["access_token"]


@scenario("CF-5", "A client-supplied X-XY-Edge is removed by the proxy: no early heartbeat")
def cf5():
    checks = []
    r = stream_request(REAL_DIRECT, "/v1/responses", KEYS["G-slow-150"], responses_body(), headers={"X-XY-Edge": "cf"})
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(not hb or hb[0] >= 140, "spoofed marker ignored", checks)
    return r, checks


@scenario("DC-2", "A key that keeps leaving before output: the 31st request in an hour runs on and is billed at upstream cost")
def dc2():
    checks = []
    key = KEYS["G-abuse"]
    key_id = psql("select id from api_keys where name='key-G-abuse' and deleted_at is null")
    # The hourly counter is shared with earlier runs in the same hour.
    redis_cli("EVAL", "for _, k in ipairs(redis.call('KEYS', ARGV[1])) do redis.call('DEL', k) end return 0", "0",
              f"xy:resil:undelivered:{key_id}:*")
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    mark = fault_last_id()
    for _ in range(31):
        stream_request(DIRECT, "/v1/responses", key, responses_body(), disconnect_after=1)
    time.sleep(5)
    recs = [x for x in fault_records_since(mark) if x["fault"] == "F17"]
    cancelled = [x for x in recs if x["outcome"] == "cancelled"]
    running = [x for x in recs if not x.get("ended") or x["ended"].startswith("0001")]
    deadline = now() + 200
    billed = ""
    while now() < deadline:
        billed = psql(f"select count(*) from usage_logs where billing_mode='undelivered_abuse' and api_key_id={key_id} "
                      f"and created_at > '{since}'")
        if billed not in ("", "0"):
            break
        time.sleep(10)
    und = psql("select count(*)||'/'||count(*) filter (where billed) from gateway_undelivered_requests where "
               "api_key_id=(select id from api_keys where name='key-G-abuse' and deleted_at is null)")
    r = {"upstream_records": len(recs), "cancelled": len(cancelled), "still_running_after_5s": len(running),
         "abuse_billed_rows": billed, "undelivered_total_billed": und}
    check(len(cancelled) >= 30, "the first 30 were cancelled", checks)
    check(billed not in ("", "0"), "the abusive request was billed (undelivered_abuse)", checks)
    return r, checks


@scenario("DC-3", "Client leaves while the upstream headers are still awaited: stage header_wait")
def dc3():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-slow-150"], responses_body(), disconnect_after=5)
    time.sleep(3)
    und = psql("select disconnect_stage from gateway_undelivered_requests where "
               "api_key_id=(select id from api_keys where name='key-G-slow-150' and deleted_at is null) order by id desc limit 1")
    r["stage"] = und
    check(und == "header_wait", f"recorded stage {und}", checks)
    return r, checks


@scenario("DC-4", "Client leaves after output started: the upstream is read to the end and billed")
def dc4():
    checks = []
    mark = fault_last_id()
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-slow-stream"], responses_body(), disconnect_after=12)
    deadline = now() + 120
    usage = "0"
    while now() < deadline:
        usage = psql("select count(*) from usage_logs where created_at > '" + since + "' and "
                     "api_key_id=(select id from api_keys where name='key-G-slow-stream' and deleted_at is null)")
        if usage not in ("", "0"):
            break
        time.sleep(5)
    recs = [x for x in fault_records_since(mark) if x["fault"] == "F23"]
    r["upstream"] = [{k: x[k] for k in ("outcome", "started", "ended")} for x in recs]
    r["usage_rows"] = usage
    check(any(x["outcome"] == "completed" for x in recs), "upstream read to the end", checks)
    check(usage not in ("", "0"), "usage recorded", checks)
    return r, checks


@scenario("IMG-1", "Image generation is exempt from the 240 s pre-output budget (260 s image)")
def img1():
    checks = []
    r = stream_request(DIRECT, "/v1/images/generations", KEYS["G-image-long"],
                       {"model": "gpt-image-2", "prompt": "a cat", "size": "1024x1024"}, timeout=400)
    check(r["status"] == 200, "status 200", checks)
    check(r["elapsed"] >= 255, "took the full 260 s", checks)
    return r, checks


@scenario("BAL-2", "Administrator confirms a top-up: recheck recovers the account at once")
def bal2():
    checks = []
    set_balance("single", 0)
    stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
    before = psql("select coalesce(extra->'resilience_balance'->>'state','OK') from accounts where name='single-ok'")
    set_balance("single", 100)
    acct = psql("select id from accounts where name='single-ok'")
    req = urllib.request.Request(f"http://127.0.0.1:{APP}/api/v1/admin/accounts/{acct}/balance/recheck", method="POST", data=b"{}")
    req.add_header("Authorization", "Bearer " + admin_token())
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as resp:
        body = json.loads(resp.read())
    after = psql("select coalesce(extra->'resilience_balance'->>'state','OK') from accounts where name='single-ok'")
    r = {"before": before, "recheck": body.get("data"), "after": after}
    check(before in ("EXHAUSTED", "SUSPECT"), f"exhausted first ({before})", checks)
    check(after in ("RECOVERING", "OK"), f"recovered at once ({after})", checks)
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
    serial = [s for s in ids if s.startswith(("BAL", "FB", "RES", "ERR-3", "ERR-4", "BUD", "DC"))]
    # DC-2 waits for a 150 s upstream; run it last.
    serial.sort(key=lambda s: s == "DC-2")
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
