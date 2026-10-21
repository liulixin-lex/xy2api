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
import re
import socket
import subprocess
import sys
import threading
import time
import urllib.request
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
APP, FAULT, CDN90, CDN125, CDN300, DIRECT = 18210, 18211, 18212, 18213, 18214, 18215
# Second app process of scenario LEASE-1 (compose profile "overlap").
APP2 = 18216
# Real Cloudflare (orange cloud) and the DNS-only host of the same Caddy site,
# from the staging .env (STG_CF_HOST, STG_DIRECT_HOST).
REAL_CF, REAL_DIRECT = os.environ.get("STG_CF_HOST", ""), os.environ.get("STG_DIRECT_HOST", "")
KEYS = json.load(open(os.path.join(HERE, "fixtures", "keys.json")))["keys"]


def now() -> float:
    return time.time()


# ---- HTTP helpers -------------------------------------------------------------

def leave(sock):
    """Drops the connection now. close() alone keeps it open while the
    response's file object still refers to the socket."""
    try:
        sock.shutdown(socket.SHUT_RDWR)
    except OSError:
        pass
    sock.close()


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
        # With Connection: close the response takes the socket over and
        # conn.sock becomes None, so keep it to time reads and to leave.
        sock = conn.sock
        try:
            resp = conn.getresponse()
        except TimeoutError:
            # Leave before the response headers arrived.
            out["disconnected_at"] = round(now() - start, 2)
            leave(sock)
            return out
        out["status"] = resp.status
        out["headers"] = {k.lower(): v for k, v in resp.getheaders()}
        out["header_at"] = round(now() - start, 2)
        while True:
            if disconnect_after is not None:
                remaining = disconnect_after - (now() - start)
                if remaining <= 0:
                    out["disconnected_at"] = round(now() - start, 2)
                    leave(sock)
                    break
                sock.settimeout(remaining)
            try:
                line = resp.readline()
            except TimeoutError:
                if disconnect_after is None:
                    out["error"] = "read timeout"
                    break
                continue
            except OSError as exc:  # connection dropped by the peer
                out["error"] = f"read: {exc}"
                break
            if not line:
                break
            text = line.decode("utf-8", "replace").rstrip("\n")
            if text:
                out["lines"].append([round(now() - start, 2), text])
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


def opened_then_failed(lines: list[str]) -> bool:
    """A Responses stream the gateway ends itself opens with response.created
    and fails the same response: strict SDKs (openai-python) reject a
    response.failed that is not preceded by response.created."""
    events = []
    for line in lines:
        if line.startswith("data:"):
            try:
                events.append(json.loads(line[5:]))
            except ValueError:
                continue
    types = [e.get("type") for e in events]
    if not types or types[0] != "response.created" or "response.failed" not in types:
        return False
    failed = events[types.index("response.failed")]
    return events[0].get("response", {}).get("id") == failed.get("response", {}).get("id")


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
    return subprocess.run(cmd, cwd=HERE, capture_output=True, text=True, timeout=60, check=True).stdout.strip()


def admin_api():
    from fixtures import API  # same directory
    api = API(f"http://127.0.0.1:{APP}")
    api.token = api.login(os.environ["ADMIN_EMAIL"], os.environ["ADMIN_PASSWORD"])
    return api


def psql(sql: str) -> str:
    cmd = ["docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "psql", "-U", "xy2api", "-d", "xy2api", "-v", "ON_ERROR_STOP=1", "-At", "-c", sql]
    return subprocess.run(cmd, cwd=HERE, capture_output=True, text=True, timeout=60, check=True).stdout.strip()


# ---- scenarios ---------------------------------------------------------------

SCENARIOS = {}


def scenario(sid: str, title: str):
    def wrap(fn):
        if sid in SCENARIOS:
            raise ValueError(f"duplicate scenario id {sid}")
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
    check(opened_then_failed(lines), "response.created first, then response.failed of the same response", checks)
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
    check(r["status"] != 200 or opened_then_failed(lines), "a committed stream opens with response.created", checks)
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


@scenario("SW-2", "A relay behind Cloudflare answers Cloudflare's JSON 502 saying 'overloaded': the account cools down for its retry_after and the request switches to the account on the same host (0.4.3)")
def sw2():
    checks = []
    mark = fault_last_id()
    try:
        r = stream_request(DIRECT, "/v1/messages", KEYS["G-cf-json"], anthropic_body())
        recs = [x["fault"] for x in fault_records_since(mark)]
        reason, left, _ = account_pause("cfj-bad")
        check(r["status"] == 200, f"status 200 ({r['status']})", checks)
        check(any("message_stop" in l for _, l in r["lines"]), "completed", checks)
        check("F30" in recs and "F00" in recs, f"switched from the failing relay ({recs})", checks)
        check(reason.startswith("transient:"), f"transient cooldown ({reason[:40]})", checks)
        check(left is not None and 45 <= left <= 60, f"cooled down for the upstream's retry_after ({left} s left)", checks)
        return r, checks
    finally:
        restore_account("cfj-bad")


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


@scenario("ERR-1", "Single-account group answering 503: an error with Retry-After 2-30 s")
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


@scenario("BAL-1", "Upstream balance exhausted on a single-account group: the account pauses, users get 503 with Retry-After, real traffic serves again when the pause ends; a 402 pauses too")
def bal1():
    checks = []
    balance_pause_seconds(30)
    try:
        set_balance("single", 0)
        first = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
        reason, left, status = account_pause("single-ok")
        check(first["status"] == 503, "user sees 503, not the upstream 429", checks)
        check("insufficient account balance" not in json.dumps(first["lines"]).lower(), "upstream balance text hidden", checks)
        check(reason.startswith("balance:"), f"paused with a balance reason ({reason[:40]})", checks)
        check(left is not None and 20 <= left <= 30, f"paused for balance.pause_seconds ({left} s left)", checks)
        check(status == "active", f"status stays active ({status})", checks)
        set_balance("single", 100)
        mark = fault_last_id()
        during = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
        retry_after = during["headers"].get("retry-after", "")
        _, left_now, _ = account_pause("single-ok")
        upstream = [x for x in fault_records_since(mark) if "key=single" in x.get("spec", "")]
        check(during["status"] == 503, "still 503 during the pause although topped up: nothing probes", checks)
        check(retry_after.isdigit() and left_now is not None and int(retry_after) <= max(left_now, 2) + 1,
              f"Retry-After {retry_after} within the pause ({left_now} s left)", checks)
        check(not upstream, f"no upstream request while paused ({len(upstream)})", checks)
        time.sleep(max(left_now or 0, 0) + 2)
        after = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
        check(after["status"] == 200, "served again once the pause ended", checks)
        # A 402 only pauses; it never sets the error state.
        r402 = stream_request(DIRECT, "/v1/responses", KEYS["G-balance-402"], responses_body(stream=False))
        reason402, _, status402 = account_pause("bal402")
        check(reason402.startswith("balance:") and status402 == "active", f"402 pauses without the error state ({status402}, {reason402[:30]})", checks)
        return {"first": first, "during": during, "after": after, "status_402": r402["status"]}, checks
    finally:
        set_balance("single", 100)
        balance_pause_seconds(300)
        restore_account("single-ok")
        restore_account("bal402")


@scenario("PE-1", "Grok relay 429 'insufficient balance' with an exhausted window: the account pauses instead of a 28-minute rate limit")
def pe1():
    checks = []
    try:
        set_balance("grok-bal", 0)
        first = stream_request(DIRECT, "/v1/responses", KEYS["G-grok-balance"], responses_body(model="grok-4.6", stream=False))
        reason, left, _ = account_pause("grok-bal")
        rate_left = psql("select coalesce(extract(epoch from rate_limit_reset_at - now())::int::text, '') from accounts where name='grok-bal'")
        check(first["status"] >= 400, f"the request fails ({first['status']})", checks)
        check(reason.startswith("balance:"), f"paused with a balance reason ({reason[:40]})", checks)
        check(left is not None and 250 <= left <= 300, f"paused for the default 300 s ({left} s left)", checks)
        check(rate_left == "" or int(rate_left) < 300, f"no rate limit from the relay's 1700 s window (left {rate_left or 'none'})", checks)
        set_balance("grok-bal", 100)
        restore_account("grok-bal")
        after = stream_request(DIRECT, "/v1/responses", KEYS["G-grok-balance"], responses_body(model="grok-4.6", stream=False))
        check(after["status"] == 200, "served again after restore status", checks)
        return {"first": first, "after": after, "pause_left": left}, checks
    finally:
        set_balance("grok-bal", 100)
        restore_account("grok-bal")


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


@scenario("PE-2", "Failures after output: the second rebinds the session, the third cools the account down")
def pe2():
    checks = []
    key, sess = KEYS["G-post-output"], fresh_session()
    turns = [stream_request(DIRECT, "/v1/responses", key, responses_body(session=sess)) for _ in range(3)]

    def failed_after_output(res) -> bool:
        text = json.dumps(res["lines"])
        return res["status"] == 200 and "output_text.delta" in text and "response.failed" in text

    served = ["failed" if failed_after_output(t) else "completed" if "response.completed" in json.dumps(t["lines"]) else "other"
              for t in turns]
    check(served[:2] == ["failed", "failed"], f"first two turns fail after output on the bound account ({served})", checks)
    check(served[2] == "completed", "third turn is rebound to the healthy account", checks)
    # A new session still lands on the failing account and makes its third failure in 60 s.
    stream_request(DIRECT, "/v1/responses", key, responses_body(session=fresh_session()))
    reason = psql("select coalesce(temp_unschedulable_reason, '') from accounts where name='po-fail' and temp_unschedulable_until > now()")
    check("POST_OUTPUT" in reason, f"account in the post-output soft cooldown ({reason or 'none'})", checks)
    after = stream_request(DIRECT, "/v1/responses", key, responses_body(session=fresh_session()))
    check("response.completed" in json.dumps(after["lines"]), "new sessions are served by the healthy account", checks)
    return {"served": served, "reason": reason}, checks


def web_search_history_body() -> dict:
    # A compacted Codex turn: replays a hosted web search, declares no tools.
    return {"model": "gpt-6.1-sol", "stream": True, "tools": [], "prompt_cache_key": fresh_session(), "input": [
        {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "look it up"}]},
        {"type": "web_search_call", "id": "ws_stg_1", "status": "completed", "action": {"type": "search", "query": "staging"}},
        {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "and now?"}]},
    ]}


@scenario("PE-3", "API-key relay rejects web_search history: one replay with the declaration, then the account gets it up front")
def pe3():
    checks = []
    rid = fault_last_id()
    first = stream_request(DIRECT, "/v1/responses", KEYS["G-web-search"], web_search_history_body())
    first_attempts = [r for r in fault_records_since(rid) if r["fault"] == "F29" and r["path"].endswith("/responses")]
    rid = fault_last_id()
    second = stream_request(DIRECT, "/v1/responses", KEYS["G-web-search"], web_search_history_body())
    second_attempts = [r for r in fault_records_since(rid) if r["fault"] == "F29" and r["path"].endswith("/responses")]
    first_outcomes = [r["outcome"] for r in first_attempts]
    # The app remembers the account for six hours: an earlier run in the same
    # process makes the first request declare the tool up front as well.
    remembered = first_outcomes == ["completed"]
    check(first["status"] == 200 and "response.completed" in json.dumps(first["lines"]), "first request completes", checks)
    check(first_outcomes == ["failed_in_stream", "completed"] or remembered,
          f"rejected once and replayed once, or already remembered ({first_outcomes})", checks)
    check(second["status"] == 200 and [r["outcome"] for r in second_attempts] == ["completed"], "next request declares web_search up front", checks)
    return {"first": first, "second": second, "remembered_before": remembered}, checks


@scenario("PE-7", "Anthropic in-stream rate_limit_error before output: switch and succeed; a single account answers 429 with Retry-After")
def pe7():
    checks = []
    ok = stream_request(DIRECT, "/v1/messages", KEYS["G-anthropic-rl"], anthropic_body())
    single = stream_request(DIRECT, "/v1/messages", KEYS["G-anthropic-rl-single"], anthropic_body())
    text = json.dumps(single["lines"])
    check(ok["status"] == 200 and "message_stop" in json.dumps(ok["lines"]), "switched to the second account", checks)
    check(single["status"] == 429, f"single account: 429, not 502 ({single['status']})", checks)
    check(single["headers"].get("retry-after", "").isdigit(), f"Retry-After {single['headers'].get('retry-after')}", checks)
    check("forbidden" not in text.lower(), "no 'access forbidden' text", checks)
    return {"ok": ok, "single": single}, checks


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


@scenario("KA-9", "Relay keeps a quiet period after output alive with keep-alive frames for 135 s: no stall, frames dropped, completes")
def ka9():
    checks = []
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-ka-quiet"], responses_body(session=fresh_session()))
    lines = [l for _, l in r["lines"]]
    deltas = [l for l in lines if l.startswith("data:") and '"response.output_text.delta"' in l]
    check(r["status"] == 200, "status 200", checks)
    check(not any("SSE-Keep-Alive" in l for l in lines), "no keep-alive frame reaches the client", checks)
    check(not any("stream_timeout" in l for l in lines), "not ended as stalled", checks)
    check(len(deltas) >= 2 and any("response.completed" in l for l in lines), f"both deltas and response.completed ({len(deltas)} deltas)", checks)
    check((r["elapsed"] or 0) >= 130, f"ran through the quiet period ({r['elapsed']} s)", checks)
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


def undelivered_after_opening(group: str, path: str, body: dict, opening: str, deferred=False) -> tuple[dict, list]:
    """A client leaves after the gateway wrote only opening events (0.4.2,
    review R03): the upstream is cancelled, nothing is billed and an
    undelivered record is written."""
    checks = []
    mark = fault_last_id()
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    key_filter = f"api_key_id=(select id from api_keys where name='key-{group}' and deleted_at is null)"
    r = stream_request(DIRECT, path, KEYS[group], body, disconnect_after=5)
    time.sleep(4)
    recs = fault_records_since(mark)
    r["upstream"] = [{k: x.get(k) for k in ("fault", "outcome", "started", "ended")} for x in recs]
    received = "\n".join(l for _, l in r["lines"])
    usage = psql(f"select count(*) from usage_logs where {key_filter} and created_at > '{since}'")
    und = psql(f"select disconnect_stage||'|'||billed from gateway_undelivered_requests where {key_filter} order by id desc limit 1")
    if opening:
        check((opening not in received) if deferred else (opening in received),
              f"opening {opening} {'deferred until content' if deferred else 'received before leaving'}", checks)
    check(not any('"text_delta"' in l or '"content":"' in l.replace(" ", "") or "output_text.delta" in l for _, l in r["lines"]),
          "no content reached the client", checks)
    check(any(x["outcome"] == "cancelled" for x in recs), "upstream request cancelled", checks)
    check(usage == "0", f"no usage row ({usage})", checks)
    check(und.endswith(("|f", "|false")), f"undelivered record {und}", checks)
    return r, checks


@scenario("DC-6", "No content before client disconnect: compatibility openings deferred, native Anthropic opening unbilled, upstream cancelled")
def dc6():
    results, checks = {}, []
    for name, (group, path, body, opening) in {
        "messages": ("G-msg-long", "/v1/messages", anthropic_body(), "message_start"),
        # Compatibility openings wait for content; native Anthropic still
        # forwards message_start without treating it as billable output.
        "chat": ("G-long-first", "/v1/chat/completions", chat_body(), '"role":"assistant"'),
        "anthropic": ("G-anthropic-stall", "/v1/messages", anthropic_body(), "message_start"),
    }.items():
        r, c = undelivered_after_opening(group, path, body, opening, deferred=name != "anthropic")
        results[name] = {k: r.get(k) for k in ("status", "elapsed", "upstream")}
        checks += [{"check": f"{name}: {x['check']}", "ok": x["ok"]} for x in c]
    return results, checks


def silent_upstream(effort: str, low: float, high: float) -> tuple[dict, list]:
    """Headers and response.created, then silence (0.4.2, review P1-C): the
    first-output tier of the effort switches accounts; the 180 s stall check
    no longer ends the request first."""
    checks = []
    mark = fault_last_id()
    r = stream_request(DIRECT, "/v1/responses", KEYS["G-silent"], responses_body(effort=effort, session=str(uuid.uuid4())))
    recs = fault_records_since(mark)
    r["upstream"] = [{k: x.get(k) for k in ("fault", "outcome", "path")} for x in recs]
    lines = [l for _, l in r["lines"]]
    check(r["status"] == 200, "status 200", checks)
    check(any("response.completed" in l for l in lines), "completed on the second account", checks)
    check(not any("stream_timeout" in l for l in lines), "not ended as stalled", checks)
    check(any(x["fault"] == "F17" and x["outcome"] == "cancelled" for x in recs), "silent attempt cancelled", checks)
    check(low <= (r["elapsed"] or 0) <= high, f"switched at the {effort} tier ({r['elapsed']} s, expected {low}-{high})", checks)
    return r, checks


@scenario("FO-2", "Silent upstream at xhigh: switched at the 200 s tier, not ended at 180 s")
def fo2():
    return silent_upstream("xhigh", 195, 235)


@scenario("FO-3", "Silent upstream at max: switched at the 220 s tier, not ended at 180 s")
def fo3():
    return silent_upstream("max", 215, 239)


def compat_header_silence(path: str, body: dict, done: str) -> tuple[dict, list]:
    """0.4.2, review G1: a converted stream (Chat or Messages on an OpenAI
    Responses upstream) whose upstream never sends its headers is switched
    at the 180 s first-output tier; the conversion had no bound before."""
    checks = []
    mark = fault_last_id()
    r = stream_request(DIRECT, path, KEYS["G-cv-headers"], body)
    recs = fault_records_since(mark)
    r["upstream"] = [{k: x.get(k) for k in ("fault", "outcome")} for x in recs]
    lines = [l for _, l in r["lines"]]
    check(r["status"] == 200, "status 200", checks)
    check(any(done in l for l in lines), "completed on the second account", checks)
    check(any(x["fault"] == "F01" and x["outcome"] == "cancelled" for x in recs), "the silent attempt was cancelled", checks)
    check(any(x["fault"] == "F00" for x in recs), "the second account answered", checks)
    check(170 <= (r["elapsed"] or 0) <= 215, f"switched at the 180 s tier ({r['elapsed']} s)", checks)
    return r, checks


@scenario("CV-1", "Chat conversion, upstream headers never come: switched at the 180 s tier")
def cv1():
    return compat_header_silence("/v1/chat/completions", chat_body(), "[DONE]")


@scenario("CV-2", "Messages conversion, upstream headers never come: switched at the 180 s tier")
def cv2():
    return compat_header_silence("/v1/messages", anthropic_body(), "message_stop")


def compat_stall(path: str, body: dict, group: str, account: str, delta: str) -> tuple[dict, list]:
    """0.4.2, review G1: a converted stream that stalls after output ends at
    the 120 s text stall threshold (not the 180 s interval) and the account
    gets the STALL cooldown, as on the Responses path."""
    checks = []
    restore_account(account)
    r = stream_request(DIRECT, path, KEYS[group], body)
    last = max((t for t, l in r["lines"] if delta in l.replace(" ", "")), default=0)
    r["stall_detected_after"] = round(r["elapsed"] - last, 1)
    reason, left, _ = account_pause(account)
    r["pause"] = {"reason": reason[:60], "seconds_left": left}
    check(r["status"] == 200, "status 200", checks)
    check(last > 0, "some output delivered", checks)
    check(r["elapsed"] - last <= 130, "ended within 130 s of the last delta", checks)
    check("STALL" in reason and left is not None, f"the account got the STALL cooldown ({reason[:40]})", checks)
    restore_account(account)
    return r, checks


@scenario("CV-3", "Chat conversion, stall after output: ended within 120 s + 5 s, STALL cooldown")
def cv3():
    return compat_stall("/v1/chat/completions", chat_body(), "G-cv-stall-chat", "cvs-chat", '"content":"')


@scenario("CV-4", "Messages conversion, stall after output: ended within 120 s + 5 s, STALL cooldown")
def cv4():
    return compat_stall("/v1/messages", anthropic_body(), "G-cv-stall-msg", "cvs-msg", "text_delta")


def wait_ready(port: int, seconds: float) -> bool:
    # The readiness answer itself, not any 200: a build whose embedded
    # frontend takes /ready answers index.html.
    end = now() + seconds
    while now() < end:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/ready", timeout=3) as resp:
                if resp.status == 200 and json.loads(resp.read() or b"{}").get("status") == "ready":
                    return True
        except Exception:
            pass
        time.sleep(1)
    return False


@scenario("RDY-1", "The candidate image answers /ready and /health itself, not with the embedded frontend")
def rdy1():
    checks = []
    result = {}
    for path in ("/ready", "/health"):
        with urllib.request.urlopen(f"http://127.0.0.1:{APP}{path}", timeout=5) as resp:
            body = resp.read()
            result[path] = {"status": resp.status, "type": resp.headers.get("Content-Type", ""), "body": body[:80].decode(errors="replace")}
        check(result[path]["status"] == 200 and "application/json" in result[path]["type"], f"{path} answers JSON", checks)
    check(json.loads(result["/ready"]["body"] or "{}").get("status") == "ready", "/ready reports ready", checks)
    return result, checks


@scenario("LEASE-1", "Two app processes on one Redis (blue-green overlap): the starting process keeps the running one's slots")
def lease1():
    checks = []
    compose = ["docker", "compose", "-p", "xy2api-staging", "--profile", "overlap"]
    subprocess.run(compose + ["rm", "-sf", "app2"], cwd=HERE, capture_output=True, timeout=180)
    mark = fault_last_id()
    first: dict = {}
    t = threading.Thread(target=lambda: first.update(
        stream_request(APP, "/v1/responses", KEYS["G-lease"], responses_body(session=str(uuid.uuid4())))))
    t.start()
    time.sleep(3)  # the first request now holds the account's only slot
    up = subprocess.run(compose + ["up", "-d", "app2"], cwd=HERE, capture_output=True, text=True, timeout=300)
    check(up.returncode == 0, "second process started", checks)
    check(wait_ready(APP2, 120), "second process answers /ready", checks)
    second = stream_request(APP2, "/v1/responses", KEYS["G-lease"], responses_body(session=str(uuid.uuid4())))
    t.join(timeout=240)
    subprocess.run(compose + ["rm", "-sf", "app2"], cwd=HERE, capture_output=True, timeout=180)
    recs = sorted((x for x in fault_records_since(mark) if x["fault"] == "F17"), key=lambda x: x["started"])
    r = {"first": {k: first.get(k) for k in ("status", "elapsed")}, "second": {k: second.get(k) for k in ("status", "elapsed")},
         "upstream": [{k: x.get(k) for k in ("outcome", "started", "ended")} for x in recs]}
    check(first.get("status") == 200 and second.get("status") == 200, "both requests served", checks)
    overlap = len(recs) >= 2 and recs[1]["started"] < recs[0]["ended"]
    check(len(recs) == 2 and not overlap, "the single-slot account never served two requests at once", checks)
    check(wait_ready(APP, 5), "the first process keeps serving", checks)
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


def admin_call(method: str, path: str, body=None) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"http://127.0.0.1:{APP}/api/v1/admin{path}", method=method, data=data)
    req.add_header("Authorization", "Bearer " + admin_token())
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read() or b"{}")


def balance_pause_seconds(seconds: int):
    admin_call("PUT", "/settings/gateway-resilience", {"balance": {"pause_seconds": seconds}})


def account_pause(name: str) -> tuple[str, int | None, str]:
    """The temp_unschedulable reason, seconds left (None when not paused) and
    status of an account."""
    row = psql("select coalesce(temp_unschedulable_reason, '') || '|' || "
               "coalesce(extract(epoch from temp_unschedulable_until - now())::int::text, '') || '|' || status "
               f"from accounts where name='{name}'")
    reason, left, status = (row.split("|") + ["", "", ""])[:3]
    seconds = int(left) if left.lstrip("-").isdigit() else None
    return reason, (seconds if seconds is not None and seconds > 0 else None), status


def restore_account(name: str) -> bool:
    """The account list's "restore status" (clears a temp_unschedulable)."""
    acct = psql(f"select id from accounts where name='{name}'")
    try:
        admin_call("DELETE", f"/accounts/{acct}/temp-unschedulable")
        return True
    except Exception:  # noqa: BLE001
        return False


@scenario("CF-5", "A client-supplied X-XY-Edge is removed by the proxy: no early heartbeat")
def cf5():
    checks = []
    r = stream_request(REAL_DIRECT, "/v1/responses", KEYS["G-slow-150"], responses_body(), headers={"X-XY-Edge": "cf"})
    hb = heartbeats(r)
    check(r["status"] == 200, "status 200", checks)
    check(not hb or hb[0] >= 140, "spoofed marker ignored", checks)
    return r, checks


@scenario("CF-6", "Internal request straight to the app (XY-image style, no CDN headers): no early heartbeat")
def cf6():
    # 0.4.1 recognises Cloudflare and ESA by CF-Ray / CDN-Loop; requests
    # from XY-image and local scripts carry neither and stay direct.
    checks = []
    r = stream_request(APP, "/v1/responses", KEYS["G-slow-150"], responses_body(),
                       headers={"User-Agent": "GGUU-AI-Image/0.0.16"})
    check(r["status"] == 200, "status 200", checks)
    check(not heartbeats(r), "no heartbeat without a CDN", checks)
    check(any("response.completed" in l for _, l in r["lines"]), "stream completed", checks)
    return r, checks


@scenario("DC-2", "A key that keeps leaving before output: the 31st request in an hour runs on and is billed at upstream cost")
def dc2():
    checks = []
    key = KEYS["G-abuse"]
    key_id = psql("select id from api_keys where name='key-G-abuse' and deleted_at is null")
    # The counter is per hour: 31 requests that straddle the hour reach the
    # threshold in neither (candidate fb0c2267c, 05:59:48-06:00:18Z).
    to_hour = 3600 - int(time.time()) % 3600
    if to_hour < 90:
        time.sleep(to_hour + 2)
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


@scenario("DC-5", "Stream queued for a busy account gets pings, then leaves before output: undelivered, not billed")
def dc4():
    # Review F1: the pings written while waiting for a slot are no output.
    checks = []
    key = KEYS["G-queue"]
    key_filter = "api_key_id=(select id from api_keys where name='key-G-queue' and deleted_at is null)"
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    mark = fault_last_id()
    holder = threading.Thread(target=stream_request, args=(DIRECT, "/v1/responses", key, responses_body(session=fresh_session())))
    holder.start()
    time.sleep(1)
    queued = stream_request(DIRECT, "/v1/responses", key, responses_body(session=fresh_session()), disconnect_after=33)
    holder.join(timeout=120)
    time.sleep(3)
    recs = [r for r in fault_records_since(mark) if r["fault"] == "F01" and r["stream"]]
    pings = [l for l in queued["lines"] if l[1].startswith(":")]
    usage = psql(f"select count(*) from usage_logs where {key_filter} and created_at > '{since}'")
    und = psql(f"select disconnect_stage||'|'||billed from gateway_undelivered_requests where {key_filter} and created_at > '{since}' order by id desc limit 1")
    check(len(pings) >= 1, f"pings while queued ({len(pings)})", checks)
    check(not any(l[1].startswith("data:") for l in queued["lines"]), "no output before the client left", checks)
    check(sorted(r["outcome"] for r in recs) == ["cancelled", "completed"], f"queued request's upstream cancelled ({[r['outcome'] for r in recs]})", checks)
    check(usage == "1", f"only the holder is billed (usage rows {usage})", checks)
    check(und.endswith(("|f", "|false")), f"undelivered record {und}", checks)
    return {"queued": queued, "upstream": recs, "usage": usage, "undelivered": und}, checks


@scenario("LR-1", "Share group with every account soft-penalised: last resort follows account priority")
def lr1():
    # Since the share simplification, tiers come from account priority:
    # lr-b (priority 1) precedes lr-a (priority 5).
    # Each request carries its own conversation: without one, a share group
    # keys the session on the API key, and the second request would follow
    # the first one's binding. Keeping a soft-penalised binding is covered
    # by unit tests: with relays that always fail, the session's failure
    # streak moves it.
    checks = []
    api = admin_api()
    gid = int(psql("select id from groups where name='G-share-lr' and deleted_at is null"))
    settings = api.call("GET", "/api/v1/admin/share-scheduling/settings")
    if not settings.get("enabled"):
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": True})
    current = api.call("GET", f"/api/v1/admin/share-scheduling/groups/{gid}/policy")
    api.call("PUT", f"/api/v1/admin/share-scheduling/groups/{gid}/policy",
             {"expected_version": current.get("version", 0), "scheduler": "share", "session_keep": False})
    time.sleep(3)

    key = KEYS["G-share-lr"]

    def tried_by(session):
        mark = fault_last_id()
        r = stream_request(DIRECT, "/v1/responses", key, responses_body(stream=False, session=session))
        return r, [x.get("spec", "").split("tag=")[-1] for x in fault_records_since(mark) if "tag=lr-" in x.get("spec", "")]

    try:
        for name in ("lr-a", "lr-b"):
            restore_account(name)
        # Both relays fail with a Cloudflare 502 and Retry-After: both are
        # soft-penalised for about a minute.
        _, first = tried_by(fresh_session())
        # A new conversation: the relaxed round uses account priority.
        second, tried = tried_by(fresh_session())
        check(bool(tried), f"last resort tried an account ({tried})", checks)
        check(bool(tried) and tried[0] == "lr-b", f"priority 1 first ({tried})", checks)
        return {"second": second, "first": first, "tried": tried}, checks
    finally:
        for name in ("lr-a", "lr-b"):
            restore_account(name)
        share_policy(api, gid, current["scheduler"], current["session_keep"])
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": settings["enabled"]})


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


@scenario("BAL-2", "Administrator restores an account paused for its balance: served again at once")
def bal2():
    checks = []
    try:
        set_balance("single", 0)
        stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
        before, left, _ = account_pause("single-ok")
        set_balance("single", 100)
        restored = restore_account("single-ok")
        after, after_left, _ = account_pause("single-ok")
        served = stream_request(DIRECT, "/v1/responses", KEYS["G-single"], responses_body(stream=False))
        check(before.startswith("balance:") and left is not None and left > 250, f"paused for the default 300 s ({left} s left)", checks)
        check(not after.startswith("balance:") and after_left is None, "restore status ended the pause", checks)
        check(served["status"] == 200, "served at once", checks)
        return {"before": before, "left": left, "restored": restored, "served": served["status"]}, checks
    finally:
        set_balance("single", 100)
        restore_account("single-ok")


# ---- 0.4.4 replay, capability and share regression scenarios -----------------

def data_events(result):
    events = []
    for _, line in result["lines"]:
        if line.startswith("data:") and line[5:].strip() != "[DONE]":
            events.append(json.loads(line[5:]))
    return events


def account_id(name):
    return int(psql(f"select id from accounts where name='{name}' and deleted_at is null"))


def attempt_events(name, since):
    """Read persisted upstream attempts, including recovered requests."""
    aid = account_id(name)
    raw = psql("select coalesce(jsonb_agg(ev), '[]'::jsonb) from ops_error_logs, "
               "jsonb_array_elements(coalesce(upstream_errors, '[]'::jsonb)) ev "
               f"where created_at >= '{since}' and ev->>'account_id' = '{aid}'")
    return [{k: ev.get(k) for k in ("account_id", "kind", "resilience_category", "upstream_status_code")}
            for ev in json.loads(raw)]


def replay_failure(group, fault, *, chat=False, retry=True):
    checks = []
    number = group.removeprefix("G-cv")
    name = "cv" + number + "-a"
    restore_account(name)
    mark = fault_last_id()
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    path = "/v1/chat/completions" if chat else "/v1/responses"
    body = chat_body() if chat else responses_body(session=fresh_session())
    headers = {"User-Agent": "codex_cli_rs/0.0.0 (staging)", "originator": "codex_cli_rs", "session_id": fresh_session()}
    try:
        result = stream_request(DIRECT, path, KEYS[group], body, headers=headers)
        events = data_events(result)
        recs = [r for r in fault_records_since(mark) if r["stream"] and
                (r["fault"] == fault or f"tag=cv{number}-b" in r["spec"])]
        result["upstream"] = recs
        check(any(r["fault"] == fault for r in recs), "failing A was attempted", checks)
        check(any(f"tag=cv{number}-b" in r["spec"] for r in recs) == retry,
              "B attempted" if retry else "unknown event committed: B not attempted", checks)
        check(result["status"] == 200, "HTTP 200 stream", checks)
        if chat:
            roles = [c["delta"]["role"] for e in events for c in e.get("choices", []) if "role" in c.get("delta", {})]
            check(roles == ["assistant"], "exactly one assistant role chunk", checks)
            check(any(c.get("finish_reason") == "stop" for e in events for c in e.get("choices", [])), "B completed Chat", checks)
            check(not any(e.get("error") for e in events), "no failed A error reaches client", checks)
        elif retry:
            created = [e.get("response", {}).get("id", "") for e in events if e.get("type") == "response.created"]
            completed = [e.get("response", {}).get("id", "") for e in events if e.get("type") == "response.completed"]
            check(bool(events) and events[0].get("type") == "response.created", "first event is B response.created", checks)
            check(len(created) == 1 and created == completed and bool(created[0]), "one created, same B ID completes", checks)
            check(not any(f"resp_fault_{fault.lower()}_" in json.dumps(e) for e in events), "A response ID never released", checks)
            check(not any(e.get("type") == "response.failed" for e in events), "A failure discarded", checks)
            if fault == "F34B":
                check(not any(e.get("type") == "response.output_text.delta" and e.get("delta") == "" for e in events),
                      "unmarked empty delta before created never reaches client", checks)
        else:
            check(any(e.get("type") == "codex.response.metadata" for e in events), "unknown metadata released", checks)
            check(any(e.get("type") == "response.failed" for e in events), "failure after unknown event stays on A", checks)
        time.sleep(3)
        result["attempt_events"] = attempt_events(name, since)
        if fault == "F31":
            capacity = [e for e in result["attempt_events"] if e["resilience_category"] == "CAPACITY_SHED"]
            attempts = sum(r["fault"] == fault for r in recs)
            check(1 <= attempts <= 2, f"A tried at most twice under D18 ({attempts})", checks)
            check(len(capacity) == attempts, f"one CAPACITY_SHED event per A attempt ({len(capacity)}/{attempts})", checks)
        if fault == "F32":
            categories = [e["resilience_category"] for e in result["attempt_events"]]
            check(bool(categories) and all(c in ("RATE_LIMIT", "TRANSIENT") for c in categories),
                  f"busy classified as rate limit or transient ({categories})", checks)
        return result, checks
    finally:
        restore_account(name)


@scenario("CV-5", "Codex Responses: overloaded retries A at most once, then B completes without leaking A created")
def cv5():
    return replay_failure("G-cv5", "F31")


@scenario("CV-6", "Chat conversion: created then service busy switches; one role chunk; no capacity misclassification")
def cv6():
    return replay_failure("G-cv6", "F32", chat=True)


@scenario("CV-7", "Responses: created and in_progress are replayable before overloaded")
def cv7():
    return replay_failure("G-cv7", "F33")


@scenario("CV-8", "Responses: unmarked empty delta before created and empty added events discarded on retry")
def cv8():
    return replay_failure("G-cv8", "F34B")


@scenario("CV-9", "Unknown codex.response.metadata commits output and prevents retry")
def cv9():
    return replay_failure("G-cv9", "F34", retry=False)


@scenario("RAW-1", "Raw Chat error plus DONE before output switches to B")
def raw1():
    checks = []
    restore_account("raw-a")
    mark = fault_last_id()
    try:
        r = stream_request(DIRECT, "/v1/chat/completions", KEYS["G-raw"], chat_body(), headers={"session_id": fresh_session()})
        events = data_events(r)
        r["upstream"] = [x for x in fault_records_since(mark) if x["stream"] and (x["fault"] == "F35" or "tag=raw-b" in x["spec"])]
        check(any(x["fault"] == "F35" and x["path"].endswith("/chat/completions") for x in r["upstream"]), "A used raw Chat path", checks)
        check(any("tag=raw-b" in x["spec"] for x in r["upstream"]), "B attempted", checks)
        check(r["status"] == 200 and any(c.get("finish_reason") == "stop" for e in events for c in e.get("choices", [])), "B completes", checks)
        check(not any(e.get("error") for e in events), "raw error discarded", checks)
        check(sum(line.strip() == "data: [DONE]" for _, line in r["lines"]) == 1, "one final DONE", checks)
        return r, checks
    finally:
        restore_account("raw-a")


@scenario("RAW-2", "Raw Chat single account error plus DONE is recorded as failure and never billed as success")
def raw2():
    checks = []
    restore_account("raw-single")
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    try:
        r = stream_request(DIRECT, "/v1/chat/completions", KEYS["G-raw-single"], chat_body(), headers={"session_id": fresh_session()})
        time.sleep(3)
        r["attempt_events"] = attempt_events("raw-single", since)
        usage = psql("select count(*) from usage_logs where api_key_id=(select id from api_keys "
                     f"where name='key-G-raw-single' and deleted_at is null) and created_at >= '{since}'")
        check(r["status"] >= 400 or any(e.get("error") for e in data_events(r)), "client receives a failure", checks)
        check(bool(r["attempt_events"]), "upstream failure recorded", checks)
        check(usage == "0", "no success usage row", checks)
        r["usage_rows"] = usage
        return r, checks
    finally:
        restore_account("raw-single")


@scenario("SO-1", "Only-stream capability: learn, retry nonstream, skip next nonstream, allow stream, clear and relearn")
def so1():
    checks, result = [], {}
    api = admin_api()
    aid = account_id("so-a")
    path = f"/api/v1/admin/accounts/{aid}"
    restore_account("so-a")
    def clear():
        try:
            api.call("DELETE", path + "/capability-learning")
            return True
        except RuntimeError as exc:
            if "HTTP 404" in str(exc):
                return False  # baseline: preserve the functional red evidence
            raise
    def request(label, stream):
        mark = fault_last_id()
        r = stream_request(DIRECT, "/v1/responses", KEYS["G-stream-only"], responses_body(stream=stream, session=fresh_session()))
        r["upstream"] = [x for x in fault_records_since(mark) if x["fault"] == "F36" or "tag=so-b" in x["spec"]]
        result[label] = r
        check(r["status"] == 200, label + " succeeds", checks)
        return r["upstream"]
    since = datetime.datetime.now(datetime.timezone.utc).isoformat()
    clear()
    try:
        first = request("first_nonstream", False)
        check(any(x["fault"] == "F36" and x["status"] == 400 for x in first) and any("tag=so-b" in x["spec"] for x in first), "first nonstream retries A 400 on B", checks)
        learned = api.call("GET", path).get("capability_learned", [])
        result["learned"] = learned
        check(any(x.get("protocol") == "responses" and x.get("mode") == "stream_only" and x.get("expires_at") for x in learned), "Responses stream_only capability learned with expiry", checks)
        gid = int(psql("select id from groups where name='G-stream-only' and deleted_at is null"))
        explain = api.call("GET", f"/api/v1/admin/resilience/groups/{gid}/explain?model=gpt-6.1-sol&protocol=responses&stream=false")
        result["explain"] = explain
        excluded = [a for a in explain.get("accounts", []) if a["account_id"] == aid]
        check(bool(excluded) and excluded[0].get("reason") == "capability_stream_only", "Explain shows stream-only exclusion for nonstream", checks)
        second = request("second_nonstream", False)
        check(not any(x["fault"] == "F36" for x in second), "next nonstream skips A", checks)
        streamed = request("stream", True)
        check(any(x["fault"] == "F36" and x["stream"] for x in streamed), "stream still uses A", checks)
        reason, left, status = account_pause("so-a")
        check(left is None and status == "active", f"A active with no cooldown ({reason})", checks)
        time.sleep(3)
        result["attempt_events"] = attempt_events("so-a", since)
        check(not result["attempt_events"], "capability miss does not count as account failure", checks)
        check(clear(), "clear capability endpoint succeeds", checks)
        check(not api.call("GET", path).get("capability_learned"), "learned state cleared", checks)
        after = request("after_clear", False)
        check(any(x["fault"] == "F36" and x["status"] == 400 for x in after), "A attempted again after clear", checks)
        check(bool(api.call("GET", path).get("capability_learned")), "capability relearned", checks)
        return result, checks
    finally:
        clear()
        restore_account("so-a")


@scenario("RA-1", "After CDN heartbeat, overload exhaustion emits failed with recovery retry-after 2–10 s and matching message")
def ra1():
    checks = []
    restore_account("ra-only")
    try:
        r = stream_request(CDN125, "/v1/responses", KEYS["G-retry-advice"], responses_body(session=fresh_session()))
        check(r["status"] == 200 and bool(heartbeats(r)), "heartbeat committed before terminal failure", checks)
        check(opened_then_failed([l for _, l in r["lines"]]), "created then failed for same ID", checks)
        failures = [e for e in data_events(r) if e.get("type") == "response.failed"]
        check(len(failures) == 1, "one failed terminal event", checks)
        err = failures[0].get("response", {}).get("error", {}) if failures else {}
        retry = str(err.get("headers", {}).get("retry-after", ""))
        match = re.search(r"try again in (\d+)s", err.get("message", ""), re.I)
        check(retry.isdigit() and 2 <= int(retry) <= 10, f"failed retry-after 2–10 s ({retry})", checks)
        check(bool(match) and match.group(1) == retry, "message seconds match retry-after", checks)
        return r, checks
    finally:
        restore_account("ra-only")


@scenario("RA-2", "After CDN heartbeat, healthy B completes without delayed retry advice")
def ra2():
    checks = []
    restore_account("ra-a")
    mark = fault_last_id()
    try:
        r = stream_request(CDN125, "/v1/responses", KEYS["G-retry-alternative"], responses_body(session=fresh_session()))
        events = data_events(r)
        recs = [x for x in fault_records_since(mark) if x["stream"] and (x["fault"] == "F37" or "tag=ra-b" in x["spec"])]
        r["upstream"] = recs
        check(bool(heartbeats(r)) and any(x["fault"] == "F37" for x in recs), "A failed after heartbeat", checks)
        check(any("tag=ra-b" in x["spec"] for x in recs) and any(e.get("type") == "response.completed" for e in events), "healthy B completed", checks)
        check(not any(e.get("type") == "response.failed" for e in events), "no failed frame or delayed retry advice on recovered request", checks)
        check(r["headers"].get("retry-after") in (None, "1"), "no HTTP delay above one second", checks)
        return r, checks
    finally:
        restore_account("ra-a")


@scenario("ANT-3", "Anthropic no-account response uses api_error")
def ant3():
    checks = []
    r = stream_request(DIRECT, "/v1/messages", KEYS["G-anthropic-empty"], anthropic_body())
    body = json.loads("\n".join(l for _, l in r["lines"]))
    check(r["status"] == 503, "no-account HTTP 503", checks)
    check(body.get("error", {}).get("type") == "api_error", "gateway no-account type is api_error", checks)
    return r, checks


def share_policy(api, gid, scheduler, session_keep=False):
    path = f"/api/v1/admin/share-scheduling/groups/{gid}/policy"
    current = api.call("GET", path)
    api.call("PUT", path, {"expected_version": current["version"], "scheduler": scheduler, "session_keep": session_keep})
    return api.call("GET", path)


@scenario("SH-1", "Share switch uses account priorities and load factors; returning to original scheduler serves normally")
def sh1():
    api, checks, result = admin_api(), [], {}
    gid = int(psql("select id from groups where name='G-share-v2' and deleted_at is null"))
    path = f"/api/v1/admin/share-scheduling/groups/{gid}/policy"
    saved = api.call("GET", path)
    settings = api.call("GET", "/api/v1/admin/share-scheduling/settings")
    accounts = {n: api.call("GET", f"/api/v1/admin/accounts/{account_id(n)}") for n in ("sh-a", "sh-b")}
    try:
        for n, factor in (("sh-a", 2), ("sh-b", 1)):
            api.call("PUT", f"/api/v1/admin/accounts/{accounts[n]['id']}", {"priority": 1, "load_factor": factor})
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": True})
        view = share_policy(api, gid, "share")
        result["share"] = view
        members = {m["name"]: m for m in view["members"]}
        check(view["scheduler"] == "share" and view["effective"], "share enabled for the group", checks)
        check(all(m["priority"] == 1 for m in members.values()), "tiers use account priority", checks)
        check(members["sh-a"]["effective_load_factor"] == 2 and members["sh-b"]["effective_load_factor"] == 1, "weights inherit account load factors", checks)
        check(abs(members["sh-a"]["base_share"] - 2/3) < 0.01, "base shares reflect 2:1 load factors", checks)
        r = stream_request(DIRECT, "/v1/responses", KEYS["G-share-v2"], responses_body(session=fresh_session()))
        check(r["status"] == 200 and any(e.get("type") == "response.completed" for e in data_events(r)), "share request completes", checks)
        result["original"] = share_policy(api, gid, "sub2api")
        check(result["original"]["scheduler"] == "sub2api" and not result["original"]["effective"], "switch back to original", checks)
        r = stream_request(DIRECT, "/v1/responses", KEYS["G-share-v2"], responses_body(session=fresh_session()))
        check(r["status"] == 200 and any(e.get("type") == "response.completed" for e in data_events(r)), "original scheduler request completes", checks)
        return result, checks
    finally:
        for n, a in accounts.items():
            api.call("PUT", f"/api/v1/admin/accounts/{a['id']}", {"priority": a["priority"], "load_factor": a.get("load_factor") or 0})
        share_policy(api, gid, saved["scheduler"], saved["session_keep"])
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": settings["enabled"]})


@scenario("SH-2", "Real transient failures reduce health weight and new-session share; restore original scheduler")
def sh2():
    api, checks, result = admin_api(), [], {}
    gid = int(psql("select id from groups where name='G-share-v2' and deleted_at is null"))
    path = f"/api/v1/admin/share-scheduling/groups/{gid}/policy"
    saved = api.call("GET", path)
    settings = api.call("GET", "/api/v1/admin/share-scheduling/settings")
    accounts = {n: api.call("GET", f"/api/v1/admin/accounts/{account_id(n)}") for n in ("sh-a", "sh-b")}
    def update(n, **fields):
        return api.call("PUT", f"/api/v1/admin/accounts/{accounts[n]['id']}", fields)
    def traffic(count):
        mark = fault_last_id()
        ok = 0
        for _ in range(count):
            r = stream_request(DIRECT, "/v1/responses", KEYS["G-share-v2"], responses_body(session=fresh_session()))
            ok += r["status"] == 200 and any(e.get("type") == "response.completed" for e in data_events(r))
        recs = [r for r in fault_records_since(mark) if r["stream"] and "tag=sh-" in r["spec"]]
        counts = {n: sum(f"tag={n}" in r["spec"] for r in recs) for n in accounts}
        check(ok == count, f"all {count} generated requests completed ({ok})", checks)
        return counts
    try:
        share_policy(api, gid, "sub2api")
        # Generate real successes for both accounts. No fabricated ops/usage
        # rows: health must come through the normal request recording path.
        for n in accounts:
            restore_account(n)
            credentials = dict(accounts[n]["credentials"])
            credentials["base_url"] = f"http://fault-upstream:8080/f/F00;first=0.01;tag={n}"
            update(n, priority=1, load_factor=1, credentials=credentials)
        warm = {}
        for n, other in (("sh-a", "sh-b"), ("sh-b", "sh-a")):
            update(n, priority=1)
            update(other, priority=2)
            warm[n] = traffic(24)
            check(warm[n][n] == 24, f"24 warmup successes attributed to {n}", checks)
        result["warmup"] = warm
        update("sh-a", priority=1)
        update("sh-b", priority=1)
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": True})
        share_policy(api, gid, "share")
        result["before_distribution"] = traffic(40)
        # Inject enough real 502 attempts to overcome both the prior and
        # this account's earlier successful test traffic in the 30m window.
        # Clear only this test account's cooldown between requests.
        share_policy(api, gid, "sub2api")
        bad = dict(accounts["sh-a"]["credentials"])
        bad["base_url"] = "http://fault-upstream:8080/f/F07;tag=sh-a"
        update("sh-a", priority=1, credentials=bad)
        # Isolate A during injection. A healthy B would absorb requests
        # while the scheduler refreshes its view of A's cleared cooldown.
        update("sh-b", status="inactive")
        since = datetime.datetime.now(datetime.timezone.utc).isoformat()
        successes = int(psql(f"select count(*) from usage_logs where account_id={accounts['sh-a']['id']} and created_at > now() - interval '30 minutes'"))
        failure_target = max(8, (successes + 23) // 4)
        result["failure_target"] = failure_target
        for _ in range(failure_target):
            # Clearing the account updates its snapshot immediately; bucket
            # membership can still lag until the scheduler outbox is read.
            deadline = now() + 20
            while True:
                restore_account("sh-a")
                time.sleep(2)
                mark = fault_last_id()
                r = stream_request(DIRECT, "/v1/responses", KEYS["G-share-v2"], responses_body(session=fresh_session()))
                attempts = [x for x in fault_records_since(mark) if x["stream"] and x["spec"] == "F07;tag=sh-a"]
                if attempts or now() >= deadline:
                    break
            check(len(attempts) == 1 and r["status"] in (502, 503), "one real A 502 attempt during injection", checks)
        good = dict(accounts["sh-a"]["credentials"])
        good["base_url"] = "http://fault-upstream:8080/f/F00;first=0.01;tag=sh-a"
        update("sh-a", credentials=good)
        update("sh-b", priority=1, status=accounts["sh-b"]["status"])
        restore_account("sh-a")
        time.sleep(3)
        result["failure_events"] = attempt_events("sh-a", since)
        check(sum(e["resilience_category"] == "TRANSIENT" for e in result["failure_events"]) == failure_target, f"{failure_target} real TRANSIENT attempts recorded", checks)
        share_policy(api, gid, "share")
        # Statistics end at a minute boundary and refresh every minute.
        deadline = now() + 150
        while True:
            view = api.call("GET", path)
            members = {m["name"]: m for m in view["members"]}
            a, b = members["sh-a"], members["sh-b"]
            if ((a.get("health_factor") or 1) < (b.get("health_factor") or 1) and
                    (a.get("health") or {}).get("soft_failures", 0) >= failure_target) or now() >= deadline:
                break
            time.sleep(3)
        result["health"] = view
        check((a.get("health") or {}).get("soft_failures", 0) >= failure_target, "health window contains injected failures", checks)
        check(a.get("health_factor") is not None and b.get("health_factor") is not None and a["health_factor"] < b["health_factor"], "A health factor below healthy B", checks)
        check(a["effective_weight"] < b["effective_weight"] and a["expected_share"] < 0.45, "health factor reduces effective weight and expected share", checks)
        # Prove A is eligible again before attributing the traffic shift to
        # weights. A stale cooldown must not make this a false positive.
        update("sh-a", priority=0)
        deadline = now() + 75
        while True:
            eligible = traffic(1)
            if eligible["sh-a"] or now() >= deadline:
                break
            time.sleep(2)
        result["eligibility_probe"] = eligible
        check(eligible["sh-a"] == 1, "A serves successfully when given the best priority", checks)
        update("sh-a", priority=1)
        time.sleep(2)
        counts = traffic(80)
        result["after_distribution"] = counts
        total = sum(counts.values())
        actual = counts["sh-a"] / total if total else 1
        check(total == 80 and actual < 0.45, f"new-session A traffic share reduced below 45% ({actual:.3f})", checks)
        # The scheduler carries allocation history across weight changes;
        # a short cohort need not match the new steady-state target exactly.
        return result, checks
    finally:
        for n, a in accounts.items():
            update(n, priority=a["priority"], status=a["status"], load_factor=a.get("load_factor") or 0, credentials=a["credentials"])
            restore_account(n)
        share_policy(api, gid, saved["scheduler"], saved["session_keep"])
        api.call("PUT", "/api/v1/admin/share-scheduling/settings", {"enabled": settings["enabled"]})


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
    serial = [s for s in ids if s.startswith(("BAL", "FB", "RES", "ERR-3", "ERR-4", "BUD", "DC", "LR", "SH", "SO", "RA"))]
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
