#!/usr/bin/env python3
"""Upgrade, rollback, roll-forward and backup/restore drill
(staging-validation §7) on the isolated database xy2api_upgrade.

  1. the old release (0.3.0, or UPGRADE_FROM) on an empty database; admin,
     compliance, one group, account, key;
  2. production-sized synthetic rows (usage_logs 3.3 M, ops_system_logs
     5.8 M, ops_error_logs 370 k, usage_billing_dedup 3.3 M, accounts 400,
     users 1 900, api_keys 25 k) — no real data;
  3. the candidate: migration time, the longest blocked probe on usage_logs
     while migrating, table sizes; a gateway request and a settings change;
  4. back to the old release: health, login, gateway request, billing row;
  5. forward to the candidate again: settings kept;
  6. pg_dump / pg_restore of the whole database, row counts compared.

Every app container is temporary (port 127.0.0.1:18216, Redis db 3) and is
removed at the end. Run on the staging host from the staging directory:
    ACCEPT_ADMIN_COMPLIANCE=1 python3 upgrade_drill.py
    UPGRADE_FROM=ghcr.io/liulixin-lex/xy2api:0.4.1 ACCEPT_ADMIN_COMPLIANCE=1 python3 upgrade_drill.py
"""

from __future__ import annotations

import datetime
import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
OLD = os.environ.get("UPGRADE_FROM") or "ghcr.io/liulixin-lex/xy2api:0.3.0"
OLD_TAG = OLD.rsplit(":", 1)[-1]
DB = "xy2api_upgrade"
NAME = "xy2api-staging-upgrade"
PORT = 18216
BASE = f"http://127.0.0.1:{PORT}"
NET = "xy2api-staging-net"


def env_file() -> dict:
    out = {}
    for line in open(os.path.join(HERE, ".env")):
        if "=" in line and not line.startswith("#"):
            k, v = line.rstrip("\n").split("=", 1)
            out[k] = v
    return out


ENV = env_file()
NEW = ENV["APP_IMAGE"]
OUT = os.path.join(HERE, "evidence", "upgrade-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%MZ"))
REPORT: dict = {"old": OLD, "new": NEW, "steps": []}


def sh(*args: str, check: bool = True, timeout: int = 3600) -> str:
    proc = subprocess.run(list(args), capture_output=True, text=True, timeout=timeout)
    if check and proc.returncode != 0:
        raise SystemExit(f"{' '.join(args[:6])} failed: {proc.stderr[-500:]}")
    return proc.stdout


def psql(sql: str, db: str = DB, timeout: int = 3600) -> str:
    return sh("docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "psql", "-U", "xy2api", "-d", db,
              "-v", "ON_ERROR_STOP=1", "-At", "-c", sql, timeout=timeout).strip()


def step(name: str, **facts):
    facts["step"] = name
    facts["at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    REPORT["steps"].append(facts)
    print(json.dumps(facts, ensure_ascii=False), flush=True)


def api(method: str, path: str, body=None, token: str = ""):
    req = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(), method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as err:
        return err.code, json.loads(err.read() or b"{}")


def start(image: str) -> float:
    sh("docker", "rm", "-f", NAME, check=False)
    started = time.time()
    sh("docker", "run", "-d", "--name", NAME, "--network", NET, "-p", f"127.0.0.1:{PORT}:8080", "--memory", "3g",
       "-e", "AUTO_SETUP=true", "-e", "SERVER_HOST=0.0.0.0", "-e", "SERVER_PORT=8080", "-e", "SERVER_MODE=release",
       "-e", "DATABASE_HOST=postgres", "-e", "DATABASE_PORT=5432", "-e", "DATABASE_USER=xy2api",
       "-e", f"DATABASE_PASSWORD={ENV['POSTGRES_PASSWORD']}", "-e", f"DATABASE_DBNAME={DB}", "-e", "DATABASE_SSLMODE=disable",
       "-e", "REDIS_HOST=redis", "-e", "REDIS_PORT=6379", "-e", "REDIS_DB=3",
       "-e", f"ADMIN_EMAIL={ENV['ADMIN_EMAIL']}", "-e", f"ADMIN_PASSWORD={ENV['ADMIN_PASSWORD']}",
       "-e", f"JWT_SECRET={ENV['JWT_SECRET']}", "-e", f"TOTP_ENCRYPTION_KEY={ENV['TOTP_ENCRYPTION_KEY']}",
       "-e", "SECURITY_URL_ALLOWLIST_ENABLED=false", "-e", "SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=true",
       "-e", "SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=true", "-e", "TZ=Asia/Shanghai", image)
    deadline = time.time() + 900
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(BASE + "/health", timeout=5) as resp:
                if resp.status == 200:
                    return round(time.time() - started, 1)
        except Exception:  # noqa: BLE001
            pass
        time.sleep(1)
    logs = sh("docker", "logs", "--tail", "60", NAME, check=False)
    raise SystemExit(f"{image} not healthy:\n{logs[-3000:]}")


def admin_token() -> str:
    status, body = api("POST", "/api/v1/auth/login", {"email": ENV["ADMIN_EMAIL"], "password": ENV["ADMIN_PASSWORD"]})
    if status != 200:
        raise SystemExit(f"admin login failed: {status} {body}")
    token = body["data"]["access_token"]
    _, comp = api("GET", "/api/v1/admin/compliance", token=token)
    if comp.get("data", {}).get("required"):
        if os.environ.get("ACCEPT_ADMIN_COMPLIANCE") != "1":
            raise SystemExit("compliance acceptance needs ACCEPT_ADMIN_COMPLIANCE=1")
        api("POST", "/api/v1/admin/compliance/accept", {"phrase": comp["data"]["ack_phrase_zh"], "language": "zh"}, token)
    return token


def gateway_request(key: str) -> dict:
    req = urllib.request.Request(BASE + "/v1/responses", method="POST",
                                 data=json.dumps({"model": "gpt-6.1-sol", "input": "drill"}).encode())
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return {"status": resp.status}
    except urllib.error.HTTPError as err:
        return {"status": err.code, "body": err.read()[:200].decode("utf-8", "replace")}


SYNTH = """
INSERT INTO users (email, password_hash, balance, created_at, updated_at)
  SELECT 'synth'||i||'@drill.local', 'x', (random()*100)::numeric(20,8), now(), now() FROM generate_series(1,1900) i;
INSERT INTO accounts (name, platform, type, credentials, extra, created_at, updated_at)
  SELECT 'synth-acct-'||i, 'openai', 'apikey', '{{}}'::jsonb, '{{}}'::jsonb, now(), now() FROM generate_series(1,400) i;
INSERT INTO api_keys (user_id, key, name, group_id, created_at, updated_at)
  SELECT (SELECT min(id) FROM users) + (i % 1900), 'sk-synth-'||md5(i::text), 'synth-'||i, {group_id}, now(), now()
  FROM generate_series(1,25000) i;
"""

SYNTH_BIG = [
    ("usage_logs", 3_300_000, """
INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, input_tokens, output_tokens,
    cache_read_tokens, input_cost, output_cost, total_cost, actual_cost, stream, duration_ms, group_id, created_at)
  SELECT {uid0} + (i % 1900), {kid0} + (i % 25000), {aid0} + (i % 400), 'synth-'||i, 'gpt-6.1-sol',
         1000 + i % 5000, 100 + i % 900, i % 3000, 0.001, 0.002, 0.003, 0.003, (i % 3) > 0, 1000 + i % 60000, {group_id},
         now() - make_interval(secs => (i % 2592000))
  FROM generate_series({lo},{hi}) i"""),
    ("usage_billing_dedup", 3_300_000, """
INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
  SELECT 'synth-'||i, {kid0} + (i % 25000), md5(i::text), now() - make_interval(secs => (i % 2592000))
  FROM generate_series({lo},{hi}) i"""),
    ("ops_system_logs", 5_800_000, """
INSERT INTO ops_system_logs (created_at, level, component, message, extra)
  SELECT now() - make_interval(secs => (i % 2592000)), CASE WHEN i % 10 = 0 THEN 'error' ELSE 'warn' END,
         'synth', 'synth event '||(i % 50), '{{}}'::jsonb
  FROM generate_series({lo},{hi}) i"""),
    ("ops_error_logs", 370_000, """
INSERT INTO ops_error_logs (error_phase, error_type, status_code, user_id, api_key_id, group_id, model, created_at)
  SELECT CASE WHEN i % 3 = 0 THEN 'routing' ELSE 'upstream' END, 'api_error', CASE WHEN i % 3 = 0 THEN 503 ELSE 502 END,
         {uid0} + (i % 1900), {kid0} + (i % 25000), {group_id}, 'gpt-6.1-sol', now() - make_interval(secs => (i % 2592000))
  FROM generate_series({lo},{hi}) i"""),
]


def probe_locks(stop: threading.Event, worst: list):
    """Times a tiny usage_logs query every 200 ms while the migration runs."""
    while not stop.is_set():
        started = time.time()
        try:
            psql("SELECT count(*) FROM (SELECT 1 FROM usage_logs ORDER BY id DESC LIMIT 1) t", timeout=600)
        except SystemExit:
            pass
        worst[0] = max(worst[0], time.time() - started)
        time.sleep(0.2)


def main() -> int:
    os.makedirs(OUT, exist_ok=True)
    sh("docker", "rm", "-f", NAME, check=False)
    psql(f"DROP DATABASE IF EXISTS {DB}", db="postgres")
    psql(f"CREATE DATABASE {DB}", db="postgres")

    took = start(OLD)
    token = admin_token()
    _, g = api("POST", "/api/v1/admin/groups", {"name": "drill", "platform": "openai", "rate_multiplier": 1, "subscription_type": "standard"}, token)
    gid = g["data"]["id"]
    api("POST", "/api/v1/admin/accounts", {"name": "drill-acct", "platform": "openai", "type": "apikey",
        "credentials": {"api_key": "sk-fault-drill", "base_url": "http://fault-upstream:8080/f/F00"}, "group_ids": [gid], "concurrency": 10}, token)
    _, u = api("POST", "/api/v1/admin/users", {"email": "drill@drill.local", "password": "drill-password-1", "balance": 100, "concurrency": 10}, token)
    _, ul = api("POST", "/api/v1/auth/login", {"email": "drill@drill.local", "password": "drill-password-1"})
    _, k = api("POST", "/api/v1/keys", {"name": "drill-key", "group_id": gid}, ul["data"]["access_token"])
    key = k["data"]["key"]
    # An administrator rule that shares a name 276 seeds but watches another
    # metric: 276 skips that name and 277 must leave the rule alone.
    psql("INSERT INTO ops_alert_rules (name, description, enabled, metric_type, operator, threshold, window_minutes, "
         "sustained_minutes, severity, notify_email, cooldown_minutes, created_at, updated_at) VALUES "
         "('上游余额长期未恢复', 'drill', false, 'account_error_count', '>', 0, 5, 1, 'P3', false, 60, NOW(), NOW())")
    step(f"{OLD_TAG} on an empty database", start_s=took, gateway=gateway_request(key))

    started = time.time()
    psql(SYNTH.format(group_id=gid))
    uid0, kid0, aid0 = (psql("SELECT min(id) FROM users WHERE email LIKE 'synth%'"),
                        psql("SELECT min(id) FROM api_keys WHERE name LIKE 'synth-%'"),
                        psql("SELECT min(id) FROM accounts WHERE name LIKE 'synth-acct-%'"))
    for table, rows, sql in SYNTH_BIG:
        for lo in range(1, rows + 1, 500_000):
            hi = min(rows, lo + 499_999)
            psql(sql.format(lo=lo, hi=hi, uid0=uid0, kid0=kid0, aid0=aid0, group_id=gid))
    psql("ANALYZE")
    sizes = psql("SELECT string_agg(relname||'='||pg_size_pretty(pg_total_relation_size(relid)), ', ' ORDER BY relname) "
                 "FROM pg_stat_user_tables WHERE relname IN ('usage_logs','ops_system_logs','ops_error_logs','usage_billing_dedup','groups')")
    step("synthetic production-sized data", seconds=round(time.time() - started, 1), sizes=sizes)

    sh("docker", "stop", "-t", "30", NAME)
    stop, worst = threading.Event(), [0.0]
    prober = threading.Thread(target=probe_locks, args=(stop, worst), daemon=True)
    prober.start()
    took = start(NEW)
    stop.set()
    prober.join()
    migrations = sh("docker", "logs", NAME, check=False)
    mig_lines = [l for l in migrations.splitlines() if "27" in l and ("migrat" in l.lower() or ".sql" in l)][:20]
    token = admin_token()
    columns = psql("SELECT string_agg(table_name||'.'||column_name, ', ') FROM information_schema.columns "
                   "WHERE (table_name, column_name) IN (('groups','availability_fallback_group_id'),('usage_logs','origin_group_id'),"
                   "('ops_error_logs','disconnect_stage'),('ops_error_logs','delivered_bytes'))")
    undelivered = psql("SELECT to_regclass('public.gateway_undelivered_requests') IS NOT NULL")
    # 276 seeds two balance alert rules and 277 removes them again when name
    # and metric both match: only the administrator's rule is left.
    balance_rules = psql("SELECT coalesce(string_agg(name||':'||metric_type, ', ' ORDER BY name), '') FROM ops_alert_rules "
                         "WHERE name IN ('上游余额不足', '上游余额长期未恢复')")
    status, settings = api("GET", "/api/v1/admin/settings/gateway-resilience", token=token)
    s = settings["data"]["settings"]
    s["keepalive"]["delay_seconds"] = 17
    put_status, _ = api("PUT", "/api/v1/admin/settings/gateway-resilience", s, token)
    step("upgrade to the candidate", start_s=took, longest_blocked_probe_s=round(worst[0], 2), migration_log=mig_lines,
         new_columns=columns, undelivered_table=undelivered, settings_get=status, settings_put=put_status,
         balance_alert_rules=balance_rules, balance_alert_rules_ok=balance_rules == "上游余额长期未恢复:account_error_count",
         gateway=gateway_request(key), sizes=psql("SELECT pg_size_pretty(pg_total_relation_size('usage_logs'))"))

    sh("docker", "stop", "-t", "30", NAME)
    took = start(OLD)
    token = admin_token()
    before = int(psql("SELECT count(*) FROM usage_logs WHERE api_key_id=(SELECT id FROM api_keys WHERE name='drill-key')"))
    gw = gateway_request(key)
    time.sleep(3)
    after = int(psql("SELECT count(*) FROM usage_logs WHERE api_key_id=(SELECT id FROM api_keys WHERE name='drill-key')"))
    step(f"roll back to {OLD_TAG}", start_s=took, login="ok", gateway=gw, billed_rows=after - before)

    sh("docker", "stop", "-t", "30", NAME)
    took = start(NEW)
    token = admin_token()
    _, settings = api("GET", "/api/v1/admin/settings/gateway-resilience", token=token)
    step("roll forward to the candidate", start_s=took, gateway=gateway_request(key),
         settings_kept=settings["data"]["settings"]["keepalive"]["delay_seconds"] == 17)

    sh("docker", "rm", "-f", NAME, check=False)
    started = time.time()
    dump = "/tmp/xy2api_upgrade.dump"
    sh("docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "sh", "-c",
       f"pg_dump -U xy2api -Fc -d {DB} -f {dump}", timeout=7200)
    psql("DROP DATABASE IF EXISTS xy2api_restore", db="postgres")
    psql("CREATE DATABASE xy2api_restore", db="postgres")
    sh("docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "sh", "-c",
       f"pg_restore -U xy2api -d xy2api_restore -j 4 {dump}", timeout=7200)
    count_sql = ("SELECT (SELECT count(*) FROM usage_logs)||'/'||(SELECT count(*) FROM ops_system_logs)||'/'||"
                 "(SELECT count(*) FROM settings)||'/'||(SELECT count(*) FROM api_keys)")
    original, restored = psql(count_sql), psql(count_sql, db="xy2api_restore")
    sh("docker", "compose", "-p", "xy2api-staging", "exec", "-T", "postgres", "rm", "-f", dump)
    psql("DROP DATABASE xy2api_restore", db="postgres")
    step("backup and restore", seconds=round(time.time() - started, 1), original=original, restored=restored, equal=original == restored)

    with open(os.path.join(OUT, "REPORT.json"), "w") as fh:
        json.dump(REPORT, fh, indent=1, ensure_ascii=False)
    print("report:", os.path.join(OUT, "REPORT.json"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
