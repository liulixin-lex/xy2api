#!/usr/bin/env python3
"""24-hour soak on the staging stack (staging-validation §8).

Load: 20-50 concurrent clients (the target moves between the two every hour)
send Responses, Chat, Messages, Gemini and image requests, streaming and
non-streaming 7:3, against fault-upstream groups that inject about 5% of each
fault type. About 2% of streams disconnect early. Every hour the single
account of G-soak-bal runs out of balance for ten minutes.

Every minute it samples memory, goroutines, database connections, Redis keys
(concurrency slots, in-flight reservations, resilience state) and log volume.
After the load it idles until the counters settle and checks: resources back
to the baseline, anonymous memory without an upward trend, no orphan slots or
reservations, no panic, no credential in the logs, no duplicate usage or
undelivered rows.

Usage (on the staging host, after fixtures.py):
    set -a; . ./.env; set +a
    python3 soak.py --hours 24 --evidence evidence/soak
    python3 soak.py --check-only --evidence evidence/soak   # re-run the checks

--cycle-minutes shortens the load cycle (default 60): the concurrency ramp
and the balance outage then repeat every cycle, and the heap and memory
slopes use per-cycle medians with the first cycle as warm-up. The 0.4.2
final soak runs 3 hours with 30-minute cycles (user decision 2026-10-19).
"""

from __future__ import annotations

import argparse
import datetime
import http.client
import json
import os
import random
import re
import statistics
import subprocess
import threading
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
APP = int(os.environ.get("STAGING_APP_PORT", "18210"))
FAULT = int(os.environ.get("STAGING_FAULT_PORT", "18211"))
CDN125 = int(os.environ.get("STAGING_CDN_PORT", "18213"))
PROJECT = os.environ.get("STAGING_PROJECT", "xy2api-staging")
if PROJECT not in ("xy2api-staging", "xy2api-staging-soak"):
    raise ValueError("unsupported staging project")
KEYS = json.load(open(os.path.join(HERE, "fixtures", "keys.json")))["keys"]
MODEL = "gpt-6.1-sol"

# (weight, name, group, path, body builder)
MIX = [
    (35, "responses", "G-soak-openai", "/v1/responses",
     lambda s: {"model": MODEL, "input": "soak", "stream": s, "prompt_cache_key": session()}),
    (20, "chat", "G-soak-openai", "/v1/chat/completions",
     lambda s: {"model": MODEL, "messages": [{"role": "user", "content": "soak " + session()}], "stream": s}),
    (20, "messages", "G-soak-anthropic", "/v1/messages",
     lambda s: {"model": "claude-sonnet-5-5", "max_tokens": 64, "stream": s,
                "messages": [{"role": "user", "content": "soak " + session()}]}),
    (10, "gemini", "G-soak-gemini", None,
     lambda s: {"contents": [{"role": "user", "parts": [{"text": "soak " + session()}]}]}),
    (3, "image", "G-soak-image", "/v1/images/generations",
     lambda s: {"model": "gpt-image-2", "prompt": "soak", "n": 1, "size": "1024x1024"}),
    (5, "balance", "G-soak-bal", "/v1/responses",
     lambda s: {"model": MODEL, "input": "soak", "stream": s, "prompt_cache_key": session()}),
    (7, "slow", "G-soak-slow", "/v1/responses",
     lambda s: {"model": MODEL, "input": "soak", "stream": True, "prompt_cache_key": session()}),
]
SECRET = re.compile(r"sk-[A-Za-z0-9_-]{16,}")


def session() -> str:
    # A few conversations per worker repeat, the rest are new.
    return f"soak-{random.randint(0, 400)}"


def now() -> float:
    return time.time()


class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.minute: dict[str, int] = {}
        self.total: dict[str, int] = {}

    def add(self, key: str):
        with self.lock:
            self.minute[key] = self.minute.get(key, 0) + 1
            self.total[key] = self.total.get(key, 0) + 1

    def take(self) -> dict[str, int]:
        with self.lock:
            out, self.minute = self.minute, {}
            return out


def one_request(stats: Stats):
    weights = [w for w, *_ in MIX]
    _, name, group, path, build = random.choices(MIX, weights=weights)[0]
    stream = random.random() < 0.7 and name != "image"
    body = build(stream)
    if name == "gemini":
        verb = "streamGenerateContent?alt=sse" if stream else "generateContent"
        path = f"/v1beta/models/gemini-3.8-flash:{verb}"
    port = CDN125 if random.random() < 0.2 else APP
    disconnect = None
    if name == "slow":
        disconnect = 2.0  # before the first token (6 s): undelivered
    elif stream and random.random() < 0.02:
        disconnect = random.uniform(0.05, 1.5)
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=disconnect or 330)
    outcome = "error"
    try:
        conn.request("POST", path, body=json.dumps(body), headers={
            "Content-Type": "application/json", "Authorization": "Bearer " + KEYS[group],
            "x-api-key": KEYS[group], "User-Agent": "xy2api-staging-soak/1.0", "Connection": "close"})
        resp = conn.getresponse()
        started = now()
        while True:
            if disconnect is not None and now() - started > disconnect:
                outcome = "disconnected"
                break
            chunk = resp.read1(8192) if hasattr(resp, "read1") else resp.read(8192)
            if not chunk:
                outcome = str(resp.status)
                break
    except TimeoutError:
        outcome = "disconnected"
    except Exception as exc:  # noqa: BLE001
        outcome = type(exc).__name__
    finally:
        try:
            if conn.sock:
                conn.sock.close()
            conn.close()
        except Exception:  # noqa: BLE001
            pass
    stats.add(f"{name}:{outcome}")


def worker(idx: int, target: list[int], stop: threading.Event, stats: Stats):
    while not stop.is_set():
        if idx >= target[0]:
            stop.wait(5)
            continue
        one_request(stats)
        stop.wait(random.uniform(2, 6))


def sh(cmd: str, timeout=60) -> str:
    return subprocess.run(cmd, shell=True, cwd=HERE, capture_output=True, text=True, timeout=timeout).stdout.strip()


def psql(sql: str) -> str:
    return subprocess.run(["docker", "compose", "-p", PROJECT, "exec", "-T", "postgres", "psql", "-U", "xy2api",
                           "-d", "xy2api", "-v", "ON_ERROR_STOP=1", "-At", "-c", sql],
                          cwd=HERE, capture_output=True, text=True, timeout=60, check=True).stdout.strip()


# Counts keys matching ARGV[1]; with ARGV[2] == "members", sums ZCARD of the
# sorted sets instead (slot holders).
REDIS_SCAN = """
local total, cursor = 0, "0"
repeat
  local r = redis.call('SCAN', cursor, 'MATCH', ARGV[1], 'COUNT', 1000)
  cursor = r[1]
  for _, k in ipairs(r[2]) do
    if ARGV[2] == 'members' then
      if redis.call('TYPE', k).ok == 'zset' then total = total + redis.call('ZCARD', k) end
    else
      total = total + 1
    end
  end
until cursor == "0"
return total
"""


def redis_scan(pattern: str, mode: str = "keys") -> int:
    out = subprocess.run(["docker", "compose", "-p", PROJECT, "exec", "-T", "redis", "redis-cli", "EVAL", REDIS_SCAN, "0", pattern, mode],
                         cwd=HERE, capture_output=True, text=True, timeout=60).stdout.strip()
    return int(out) if out.lstrip("-").isdigit() else -1


def app_anon_mib() -> float | None:
    """Anonymous memory of the app container (heap and stacks). docker stats
    also counts active page cache, which grows with log writes and is
    reclaimable, so leaks are judged on this value."""
    pid = sh(f"docker inspect -f '{{{{.State.Pid}}}}' {PROJECT}-app-1")
    try:
        cgroup = open(f"/proc/{pid}/cgroup").read().strip().split(":", 2)[2]
        for line in open(f"/sys/fs/cgroup{cgroup}/memory.stat"):
            key, value = line.split()
            if key == "anon":
                return round(int(value) / 2**20, 1)
    except (OSError, IndexError, ValueError):
        pass
    return None


def sample(stats: Stats | None) -> dict:
    mem = sh(f"docker stats --no-stream --format '{{{{.MemUsage}}}}' {PROJECT}-app-1").split("/")[0].strip()
    row = psql("SELECT goroutine_count, db_conn_active, db_conn_idle FROM ops_system_metrics ORDER BY created_at DESC LIMIT 1")
    gor, active, idle = (row.split("|") + ["", "", ""])[:3] if row else ("", "", "")
    return {
        "at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "app_mem": mem,
        "app_anon_mib": app_anon_mib(),
        "goroutines": int(gor) if gor.isdigit() else None,
        "db_conn_active": int(active) if active.isdigit() else None,
        "db_conn_idle": int(idle) if idle.isdigit() else None,
        "pg_backends": int(psql("SELECT count(*) FROM pg_stat_activity WHERE datname = 'xy2api'") or 0),
        "slots_account": redis_scan("concurrency:account:*", "members"),
        "slots_user": redis_scan("concurrency:user:*", "members"),
        "slots_key": redis_scan("concurrency:api_key:*", "members"),
        "slots_live": redis_scan("concurrency:live:*", "members"),
        "inflight_keys": redis_scan("billing:inflight*"),
        "resil_keys": redis_scan("xy:resil:*"),
        "redis_keys": int(sh(f"docker compose -p {PROJECT} exec -T redis redis-cli DBSIZE") or 0),
        "log_lines_1m": int(sh(f"docker logs --since 60s {PROJECT}-app-1 2>&1 | wc -l") or 0),
        "requests_1m": stats.take() if stats else {},
    }


def set_balance(value: float):
    urllib.request.urlopen(f"http://127.0.0.1:{FAULT}/_control/balance?key=soak-bal&value={value}", timeout=10).read()


def mem_mib(text: str) -> float:
    m = re.match(r"([\d.]+)\s*([KMG]i?B)", text or "")
    if not m:
        return 0.0
    return float(m.group(1)) * {"KiB": 1 / 1024, "MiB": 1, "GiB": 1024, "KB": 1 / 1024, "MB": 1, "GB": 1024}[m.group(2)]


def anon_trend(samples: list[dict]) -> tuple[float | None, float | None]:
    """Median anonymous memory early in the load (after warm-up, the second
    sixth of the run) and over its final sixth; settle samples excluded."""
    load = [s["app_anon_mib"] for s in samples if "target" in s and s.get("app_anon_mib") is not None]
    n = len(load)
    if n < 60:
        return None, None
    return statistics.median(load[n // 6:n // 3]), statistics.median(load[n - n // 6:])


def cycle_slope(points: list[tuple[datetime.datetime, float]], start: datetime.datetime,
                end: datetime.datetime, cycle_minutes: int = 60) -> float | None:
    """Slope in MiB per hour of the per-cycle medians of points, cycles
    counted from the start of the load. The load repeats every cycle, so
    medians of complete cycles cancel it; the first cycle (warm-up) and a
    partial last cycle are dropped. Needs three complete cycles after the
    first."""
    cycle = cycle_minutes * 60
    complete = int((end - start).total_seconds() // cycle)
    buckets: dict[int, list[float]] = {}
    for at, v in points:
        b = int((at - start).total_seconds() // cycle)
        if 1 <= b < complete:
            buckets.setdefault(b, []).append(v)
    xs = sorted(b for b, vs in buckets.items() if len(vs) >= 10)
    if len(xs) < 3:
        return None
    ys = [statistics.median(buckets[b]) for b in xs]
    xs = [b * cycle / 3600 for b in xs]
    mx, my = statistics.fmean(xs), statistics.fmean(ys)
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / sum((x - mx) ** 2 for x in xs)


GCTRACE = re.compile(r"^(\S+) gc \d+ @[\d.]+s .*? (\d+)->(\d+)->(\d+) MB")


def app_logs(started_at: str, timestamps: bool = False) -> str:
    """The independent stack captures logs continuously, beyond rotation."""
    path = os.environ.get("STAGING_LOG_FILE")
    if path:
        start = datetime.datetime.fromisoformat(started_at)
        lines = []
        with open(path) as source:
            for line in source:
                stamp, _, body = line.partition(" ")
                try:
                    at = datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00"))
                except ValueError:
                    continue
                if at >= start:
                    lines.append(line if timestamps else body)
        return "".join(lines)
    flag = "--timestamps " if timestamps else ""
    return sh(f"docker logs {flag}--since {started_at} {PROJECT}-app-1 2>&1", timeout=600)


def live_heap(started_at: str) -> list[tuple[datetime.datetime, float]]:
    """Live heap after each GC (MB) from the app's gctrace lines."""
    out = app_logs(started_at, timestamps=True)
    points = []
    for line in out.splitlines():
        m = GCTRACE.match(line)
        if m:
            at = datetime.datetime.fromisoformat(m.group(1)[:26].rstrip("Z") + "+00:00")
            points.append((at, float(m.group(4))))
    return points


# The live heap after GC may grow at most this much per hour of load.
MAX_LIVE_HEAP_SLOPE_MIB_PER_HOUR = 1.0


def checks(evidence: str, started_at: str, cycle_minutes: int = 60) -> dict:
    samples = [json.loads(l) for l in open(os.path.join(evidence, "metrics.jsonl"))]
    base, last = samples[0], samples[-1]
    peak_mem = max(mem_mib(s["app_mem"]) for s in samples)
    early_anon, late_anon = anon_trend(samples)
    load = [s for s in samples if "target" in s]
    load_start = datetime.datetime.fromisoformat(started_at) if load else None
    load_end = datetime.datetime.fromisoformat(load[-1]["at"]) if load else None
    anon_slope = None if load_start is None else cycle_slope(
        [(datetime.datetime.fromisoformat(s["at"]), s["app_anon_mib"]) for s in load if s.get("app_anon_mib") is not None],
        load_start, load_end, cycle_minutes)
    heap = live_heap(started_at)
    heap_slope = None if load_start is None else cycle_slope(heap, load_start, load_end, cycle_minutes)
    logs = app_logs(started_at)
    timed_logs = app_logs(started_at, timestamps=True)
    error_minutes = {}
    for line in timed_logs.splitlines():
        if re.search(r'"level"\s*:\s*"error"|\bERROR\b', line):
            minute = line[:16]
            error_minutes[minute] = error_minutes.get(minute, 0) + 1
    # Surface every error count; a sustained minute-rate is the flood gate.
    flood_run, error_flood = 0, False
    minute = datetime.datetime.fromisoformat(started_at).replace(second=0, microsecond=0)
    end = datetime.datetime.fromisoformat(samples[-1]["at"])
    while minute <= end:
        flood_run = flood_run + 1 if error_minutes.get(minute.isoformat()[:16], 0) >= 60 else 0
        error_flood = error_flood or flood_run >= 3
        minute += datetime.timedelta(minutes=1)
    secrets = list(KEYS.values())
    for n in (1, 2, 3):
        if os.environ.get(f"REAL{n}_API_KEY"):
            secrets.append(os.environ[f"REAL{n}_API_KEY"])
    leaked = sum(logs.count(s) for s in secrets if s)
    dup_usage = psql(f"SELECT count(*) FROM (SELECT request_id FROM usage_logs WHERE created_at >= '{started_at}' "
                     "AND request_id <> '' GROUP BY request_id, api_key_id HAVING count(*) > 1) d")
    dup_undelivered = psql(f"SELECT count(*) FROM (SELECT request_id FROM gateway_undelivered_requests WHERE created_at >= '{started_at}' "
                           "AND request_id <> '' GROUP BY request_id HAVING count(*) > 1) d")
    both = psql(f"SELECT count(*) FROM gateway_undelivered_requests g JOIN usage_logs u ON u.request_id = g.request_id "
                f"WHERE g.created_at >= '{started_at}' AND g.billed = false AND g.request_id <> ''")
    result = {
        "samples": len(samples),
        "baseline": {k: base[k] for k in base if k != "requests_1m"},
        "final": {k: last[k] for k in last if k != "requests_1m"},
        "peak_app_mem_mib": round(peak_mem, 1),
        "anon_mib_early_median": early_anon,
        "anon_mib_late_median": late_anon,
        "anon_slope_mib_per_hour": None if anon_slope is None else round(anon_slope, 2),
        "gc_traces": len(heap),
        "live_heap_mib_first_last": [heap[0][1], heap[-1][1]] if heap else None,
        "live_heap_slope_mib_per_hour": None if heap_slope is None else round(heap_slope, 2),
        "panics": len(re.findall(r"\bpanic\b|goroutine \d+ \[running\]", logs)),
        "error_lines": sum(error_minutes.values()),
        "error_lines_per_minute": error_minutes,
        "error_flood_threshold": "at least 60 ERROR lines/min for 3 consecutive minutes",
        "credential_hits_in_logs": leaked,
        "masked_key_like_strings": len(SECRET.findall(logs)),
        "duplicate_usage_rows": int(dup_usage or 0),
        "duplicate_undelivered_rows": int(dup_undelivered or 0),
        "unbilled_undelivered_with_usage": int(both or 0),
        "project": PROJECT,
    }
    overlap_file = os.environ.get("STAGING_OVERLAP_FILE")
    if overlap_file and os.path.exists(overlap_file):
        with open(overlap_file) as source:
            result["other_stack_activity"] = [json.loads(line) for line in source if line.strip()]
        io_steps = []
        for activity in result["other_stack_activity"]:
            if activity["event"] != "upgrade_end":
                continue  # Orchestrator status events need no evidence directory.
            report = os.path.join(activity["evidence"], "REPORT.json")
            if os.path.exists(report):
                with open(report) as source:
                    io_steps.extend(s for s in json.load(source)["steps"] if s["step"] in (
                        "synthetic data fill begins", "synthetic production-sized data", "backup and restore begins", "backup and restore"))
        result["other_stack_io_steps"] = io_steps
    ok = {
        "no orphan account/user/key slots": all(last[k] == 0 for k in ("slots_account", "slots_user", "slots_key", "slots_live")),
        "no orphan in-flight reservations": last["inflight_keys"] == 0,
        "goroutines measured and back to baseline (+50)": base["goroutines"] is not None and last["goroutines"] is not None
        and last["goroutines"] <= base["goroutines"] + 50,
        "pg backends back to baseline (+3)": last["pg_backends"] <= base["pg_backends"] + 3,
        # Leaks show as anonymous memory that keeps rising under the same load.
        "app anonymous memory does not trend up (late <= 1.2x early + 16 MiB)": late_anon <= 1.2 * early_anon + 16
        if early_anon is not None and late_anon is not None
        else mem_mib(last["app_mem"]) <= max(1.5 * mem_mib(base["app_mem"]), mem_mib(base["app_mem"]) + 64),
        "no panic": result["panics"] == 0,
        "no sustained ERROR flood": not error_flood,
        "no credential in logs": leaked == 0,
        "no duplicate usage rows": result["duplicate_usage_rows"] == 0,
        "no duplicate undelivered rows": result["duplicate_undelivered_rows"] == 0,
        "undelivered and unbilled requests have no usage row": result["unbilled_undelivered_with_usage"] == 0,
    }
    ok[f"live heap after GC measured over >=3 post-warmup cycles (<= {MAX_LIVE_HEAP_SLOPE_MIB_PER_HOUR} MiB/h)"] = (
        heap_slope is not None and heap_slope <= MAX_LIVE_HEAP_SLOPE_MIB_PER_HOUR)
    result["checks"] = [{"check": k, "ok": v} for k, v in ok.items()]
    result["ok"] = all(ok.values())
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hours", type=float, default=24)
    parser.add_argument("--min", type=int, default=20)
    parser.add_argument("--max", type=int, default=50)
    parser.add_argument("--settle-minutes", type=int, default=10)
    parser.add_argument("--cycle-minutes", type=int, default=60)
    parser.add_argument("--evidence", default=os.path.join(HERE, "evidence", "soak"))
    parser.add_argument("--check-only", action="store_true")
    args = parser.parse_args()
    os.makedirs(args.evidence, exist_ok=True)
    meta_path = os.path.join(args.evidence, "meta.json")
    metrics_path = os.path.join(args.evidence, "metrics.jsonl")

    if not args.check_only:
        started_at = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
        json.dump({"started_at": started_at, "hours": args.hours, "min": args.min, "max": args.max,
                   "cycle_minutes": args.cycle_minutes}, open(meta_path, "w"))
        set_balance(1000)
        with open(metrics_path, "w") as fh:
            fh.write(json.dumps(sample(None)) + "\n")
        stats, stop, target = Stats(), threading.Event(), [args.min]
        threads = [threading.Thread(target=worker, args=(i, target, stop, stats), daemon=True) for i in range(args.max)]
        for t in threads:
            t.start()
        end = now() + args.hours * 3600
        start = now()
        while now() < end:
            elapsed_min = (now() - start) / 60
            cycle = args.cycle_minutes
            phase = (elapsed_min % cycle) / cycle
            # Up to max over the first half of each cycle, back to min after.
            target[0] = args.min + round((args.max - args.min) * (1 - abs(2 * phase - 1)))
            # From the middle of every cycle, for a sixth of it (minutes 30-40
            # of an hour): the single balance account runs dry.
            set_balance(0 if 0.5 <= phase < 0.5 + 1 / 6 else 1000)
            time.sleep(60)
            s = sample(stats)
            s["target"] = target[0]
            with open(metrics_path, "a") as fh:
                fh.write(json.dumps(s) + "\n")
            print(f"{s['at']} target={target[0]} mem={s['app_mem']} anon={s['app_anon_mib']}MiB gor={s['goroutines']} slots={s['slots_account']} "
                  f"inflight={s['inflight_keys']} req={sum(s['requests_1m'].values())}", flush=True)
        stop.set()
        set_balance(1000)
        for t in threads:
            t.join(timeout=400)
        json.dump({"totals": stats.total}, open(os.path.join(args.evidence, "requests.json"), "w"), indent=1)
        for _ in range(args.settle_minutes):
            time.sleep(60)
            with open(metrics_path, "a") as fh:
                fh.write(json.dumps(sample(None)) + "\n")

    meta = json.load(open(meta_path))
    result = checks(args.evidence, meta["started_at"], meta.get("cycle_minutes", 60))
    json.dump(result, open(os.path.join(args.evidence, "RESULT.json"), "w"), indent=1)
    for c in result["checks"]:
        print(f"{'PASS' if c['ok'] else 'FAIL'} {c['check']}", flush=True)
    print(json.dumps({"ok": result["ok"], "panics": result["panics"], "peak_app_mem_mib": result["peak_app_mem_mib"]}), flush=True)
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
