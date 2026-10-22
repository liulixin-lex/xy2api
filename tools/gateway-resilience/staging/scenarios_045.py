#!/usr/bin/env python3
"""Serial supplemental checks for the 0.4.5 resilience paths.

The script is deliberately plan-only by default.  With ``--execute`` it runs
the eight supplemental cases serially against the already-running main stack:
four API-key passthrough cases, two SSE-comment cases, and two Anthropic
terminal-EOF conversions.  It uses only the dedicated ``G-qa-*`` fixtures,
holds the staging validation lock, snapshots every changed setting/account,
and restores and verifies them in ``finally``.

Examples (on the staging host)::

    python3 scenarios_045.py
    python3 scenarios_045.py --execute --label baseline \
      --expected-image-id sha256:<0.4.4-image> --evidence evidence/045-baseline
    python3 scenarios_045.py --execute --label candidate \
      --expected-image-id sha256:<b966-image> --evidence evidence/045-candidate

The baseline run is expected to expose the old failures and therefore exits
non-zero when a check fails.  The candidate run is green only when all checks
and the restore verification pass.
"""

from __future__ import annotations

import argparse
import copy
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
APP_CONTAINER = "xy2api-staging-app-1"
APP_PORT = 18210
DIRECT = 18215
MODEL = "gpt-6.1-sol"
RESTORE_FIELDS = (
    "credentials", "extra", "priority", "status", "error_message", "schedulable",
    "rate_limited_at", "rate_limit_reset_at", "overload_until",
    "temp_unschedulable_until", "temp_unschedulable_reason", "last_used_at",
)
ACCOUNT_NAMES = ("qa-pt-a", "qa-pt-b", "qa-f38", "qa-f39")
CASES = ("PT-7", "PT-8", "PT-9", "PT-11", "F38-9", "F38-12", "F39-5R", "F39-5C")
RULE_MESSAGE = "qa045-f38-rule-hit"


def utc() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def write_json(path: Path, value) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def quoted(value) -> str:
    return "'" + str(value).replace("'", "''") + "'"


def command(*args: str) -> str:
    return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()


def memory_available() -> int:
    for line in Path("/proc/meminfo").read_text().splitlines():
        if line.startswith("MemAvailable:"):
            return int(line.split()[1]) * 1024
    return 0


def resource_check() -> dict:
    free = shutil.disk_usage(HERE).free
    mem = memory_available()
    processes = command("ps", "-eo", "pid=,args=").splitlines()
    active = []
    for line in processes:
        fields = line.strip().split(maxsplit=1)
        if len(fields) != 2 or int(fields[0]) == os.getpid():
            continue
        if re.search(r"(?:^|\s|/)(?:scenarios|upgrade_drill|scenarios_045)\.py(?:\s|$)", fields[1]):
            active.append(line)
    if active:
        raise RuntimeError("another staging scenario process is running")
    if mem < 2 * 2**30 or free < 10 * 2**30:
        raise RuntimeError(f"resource reserve below 2 GiB memory / 10 GiB disk ({mem}, {free})")
    app = json.loads(command("docker", "inspect", APP_CONTAINER))[0]
    return {"at": utc(), "memory_available_bytes": mem, "disk_free_bytes": free,
            "load_average": os.getloadavg(), "app_image": app["Image"],
            "app_started_at": app["State"].get("StartedAt")}


def import_scenarios():
    os.chdir(HERE)
    sys.path.insert(0, str(HERE))
    import scenarios as S  # pylint: disable=import-outside-toplevel
    from fixtures import items  # pylint: disable=import-outside-toplevel
    return S, items


def events(result, S):
    return S.data_events(result)


def check(checks: list[dict], ok: bool, label: str) -> None:
    checks.append({"check": label, "ok": bool(ok)})


def usage_rows(S, key_name: str, since: str):
    sql = ("SELECT coalesce(jsonb_agg(jsonb_build_object('id',u.id,'account_id',u.account_id,"
           "'input_tokens',u.input_tokens,'output_tokens',u.output_tokens,"
           "'cache_read_tokens',u.cache_read_tokens,'actual_cost',u.actual_cost)), '[]'::jsonb) "
           "FROM usage_logs u WHERE api_key_id=(SELECT id FROM api_keys WHERE name="
           + quoted(key_name) + " AND deleted_at IS NULL) AND created_at>=" + quoted(since))
    return json.loads(S.psql(sql))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--label", choices=("baseline", "candidate"), default="candidate")
    parser.add_argument("--expected-image-id")
    parser.add_argument("--evidence", type=Path)
    args = parser.parse_args()
    plan = {
        "mode": "plan-only" if not args.execute else "execute",
        "cases": list(CASES), "serial": True, "estimated_minutes": "5-10",
        "fixtures": {"passthrough": "G-qa-passthrough", "comment": "G-qa-f38", "anthropic_eof": "G-qa-f39"},
        "baseline": "0.4.4 is expected to reproduce the pre-fix failures; candidate must be green",
        "not_covered": ["OAuth passthrough", "nonzero usage on a failed terminal", "δ#10", "δ#13", "δ#14"],
        "restore": ["gateway-resilience settings", "four dedicated accounts", "temporary error rule"],
    }
    if not args.execute:
        print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0
    if not args.evidence or not re.fullmatch(r"sha256:[0-9a-f]{64}", args.expected_image_id or ""):
        parser.error("--execute requires --evidence and --expected-image-id sha256:<64 hex>")
    if args.evidence.exists():
        parser.error("--evidence must name a new directory")
    args.evidence.mkdir(parents=True, mode=0o700)
    os.chmod(args.evidence, 0o700)
    resources = resource_check()
    if resources["app_image"] != args.expected_image_id:
        raise RuntimeError(f"main app image is {resources['app_image']}, expected {args.expected_image_id}")
    S, items = import_scenarios()
    api = S.admin_api()
    accounts = {a["name"]: a for a in items(api.call("GET", "/api/v1/admin/accounts?page=1&page_size=500"))}
    snapshots = []
    for name in ACCOUNT_NAMES:
        if name not in accounts:
            raise RuntimeError(f"missing dedicated fixture account: {name}")
        row = json.loads(S.psql("SELECT to_jsonb(a) FROM accounts a WHERE id=" + str(int(accounts[name]["id"]))))
        if row["type"] != "apikey" or "fault-upstream" not in (row.get("credentials") or {}).get("base_url", ""):
            raise RuntimeError(f"refusing to modify non-fixture account {name}")
        snapshots.append({"id": row["id"], "name": name,
                          "fields": {field: row.get(field) for field in RESTORE_FIELDS}})
    saved_settings = copy.deepcopy(api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"])
    backup = {"label": args.label, "expected_image_id": args.expected_image_id,
              "resources": resources, "settings": saved_settings, "accounts": snapshots}
    write_json(args.evidence / "RESTORE_PRIVATE.json", backup)
    os.chmod(args.evidence / "RESTORE_PRIVATE.json", 0o600)
    binary_sha = command("docker", "exec", APP_CONTAINER, "sha256sum", "/app/xy2api").split()[0]
    meta = {"label": args.label, "started_at": utc(), "expected_image_id": args.expected_image_id,
            "actual_image_id": resources["app_image"], "binary_sha256": binary_sha,
            "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "cases": [], "restored": False, "plan": plan}
    write_json(args.evidence / "RUN.json", meta)
    lock = (HERE / ".phase2.lock").open("a+")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        raise RuntimeError("another main-stack validation holds .phase2.lock") from None
    rule_id = None

    def put_account(snap, spec: str, *, priority: int | None = None, extra=None):
        fields = snap["fields"]
        credentials = copy.deepcopy(fields["credentials"])
        credentials["base_url"] = spec
        payload = {"credentials": credentials, "priority": fields["priority"] if priority is None else priority,
                   "status": "active", "schedulable": True}
        if extra is not None:
            payload["extra"] = extra
        api.call("PUT", f"/api/v1/admin/accounts/{snap['id']}", payload)

    def clear_pause(snap):
        try:
            api.call("DELETE", f"/api/v1/admin/accounts/{snap['id']}/temp-unschedulable")
        except Exception:
            pass
        S.psql("UPDATE accounts SET status='active', schedulable=true, error_message=NULL, "
               "rate_limited_at=NULL, rate_limit_reset_at=NULL, overload_until=NULL, "
               "temp_unschedulable_until=NULL, temp_unschedulable_reason=NULL WHERE id=" + str(snap["id"]))

    def restore():
        errors = []
        try:
            api.call("PUT", "/api/v1/admin/settings/gateway-resilience", saved_settings)
        except BaseException as exc:  # noqa: BLE001
            errors.append("settings: " + str(exc))
        if rule_id is not None:
            try:
                api.call("DELETE", f"/api/v1/admin/error-passthrough-rules/{rule_id}")
            except BaseException as exc:  # noqa: BLE001
                errors.append("rule: " + str(exc))
        for snap in snapshots:
            try:
                clear_pause(snap)
                fields_json = quoted(json.dumps(snap["fields"], ensure_ascii=False))
                assignments = ", ".join(f"{field}=r.{field}" for field in RESTORE_FIELDS)
                S.psql(f"UPDATE accounts a SET {assignments} FROM jsonb_populate_record(NULL::accounts, {fields_json}::jsonb) r WHERE a.id={snap['id']}")
                api.call("PUT", f"/api/v1/admin/accounts/{snap['id']}", {
                    "credentials": snap["fields"]["credentials"], "priority": snap["fields"]["priority"],
                    "extra": snap["fields"]["extra"] or {}, "status": snap["fields"]["status"],
                    "schedulable": snap["fields"]["schedulable"]})
                S.psql(f"UPDATE accounts a SET {assignments} FROM jsonb_populate_record(NULL::accounts, {fields_json}::jsonb) r WHERE a.id={snap['id']}")
                actual = json.loads(S.psql("SELECT to_jsonb(a) FROM accounts a WHERE id=" + str(snap["id"])))
                if any(actual.get(field) != value for field, value in snap["fields"].items()):
                    raise RuntimeError("restored account fields differ")
            except BaseException as exc:  # noqa: BLE001
                errors.append(snap["name"] + ": " + str(exc))
        try:
            if api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"] != saved_settings:
                errors.append("restored settings differ")
        except BaseException as exc:  # noqa: BLE001
            errors.append("settings verify: " + str(exc))
        meta["restored"], meta["restore_errors"] = not errors, errors

    def configure_settings():
        settings = copy.deepcopy(saved_settings)
        settings["enabled"] = True
        settings["stream"].update(prelude_staging=True, strict_terminal=True)
        settings["budget"].update(enabled=True, same_account_retry_max=0)
        settings["budget"]["first_output_timeout_seconds"] = {key: 12 for key in ("default", "high", "xhigh", "max")}
        settings["budget"].setdefault("model_overrides", {})[MODEL] = 12
        settings["stall"].update(enabled=True, text_stall_seconds=30)
        settings["keepalive"].update(enabled=True, delay_seconds=5, interval_seconds=5)
        api.call("PUT", "/api/v1/admin/settings/gateway-resilience", settings)
        write_json(args.evidence / "SETTINGS.json", api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"])

    def configure(name: str, spec: str, *, priority=1, passthrough=False):
        snap = next(item for item in snapshots if item["name"] == name)
        clear_pause(snap)
        extra = copy.deepcopy(snap["fields"]["extra"] or {})
        extra["openai_passthrough"] = passthrough
        put_account(snap, spec, priority=priority, extra=extra)
        actual = api.call("GET", f"/api/v1/admin/accounts/{snap['id']}")
        if bool((actual.get("extra") or {}).get("openai_passthrough")) != passthrough:
            raise RuntimeError("fixture passthrough setting was not applied: " + name)

    def run_case(case_id: str):
        nonlocal rule_id
        checks: list[dict] = []
        started = utc()
        mark = S.fault_last_id()
        key_name = "key-G-qa-passthrough" if case_id.startswith("PT-") else "key-G-qa-f38" if case_id.startswith("F38") else "key-G-qa-f39"
        key = S.KEYS[key_name.removeprefix("key-")]
        if case_id == "PT-7":
            configure("qa-pt-a", "http://fault-upstream:8080/f/F17;first=45;tag=qa-pt-a", passthrough=True)
            configure("qa-pt-b", "http://xy2api-staging-fault-upstream-1:8080/f/F00;first=0.05;tag=qa-pt-b", priority=2, passthrough=True)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "PT-8":
            configure("qa-pt-a", "http://fault-upstream:8080/f/F01;secs=45;tag=qa-pt-a", passthrough=True)
            configure("qa-pt-b", "http://xy2api-staging-fault-upstream-1:8080/f/F00;first=0.05;tag=qa-pt-b", priority=2, passthrough=True)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "PT-9":
            configure("qa-pt-a", "http://fault-upstream:8080/f/F06;secs=50;tag=qa-pt-a", passthrough=True)
            configure("qa-pt-b", "http://xy2api-staging-fault-upstream-1:8080/f/F00;first=0.05;tag=qa-pt-b", priority=2, passthrough=True)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "PT-11":
            configure("qa-pt-a", "http://fault-upstream:8080/f/F26;after=0.1;tag=qa-pt-a", passthrough=True)
            configure("qa-pt-b", "http://xy2api-staging-fault-upstream-1:8080/f/F00;first=0.05;tag=qa-pt-b", priority=2, passthrough=True)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "F38-9":
            configure("qa-f38", "http://fault-upstream:8080/f/F38;outcome=ok;first=0;after_comment=2", passthrough=True)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "F38-12":
            configure("qa-f38", "http://fault-upstream:8080/f/F38;outcome=failed;code=context_length_exceeded;after=0;after_comment=2", passthrough=True)
            if rule_id is None:
                created = api.call("POST", "/api/v1/admin/error-passthrough-rules", {
                    "name": "qa045-f38-" + uuid.uuid4().hex[:12], "enabled": True, "priority": 0,
                    "error_codes": [], "keywords": ["context_length_exceeded"], "match_mode": "any",
                    "platforms": ["openai"], "passthrough_code": False, "response_code": 422,
                    "passthrough_body": False, "custom_message": RULE_MESSAGE, "skip_monitoring": False,
                    "description": "temporary QA F38 comment rule"})
                rule_id = int(created["id"])
                write_json(args.evidence / "TEMP_RULE.json", created)
            path, body = "/v1/responses", S.responses_body(model=MODEL, session=S.fresh_session())
        elif case_id == "F39-5R":
            configure("qa-f39", "http://fault-upstream:8080/f/F39", passthrough=False)
            path, body = "/v1/responses", S.responses_body(model="claude-sonnet-5-5", session=S.fresh_session())
        else:
            configure("qa-f39", "http://fault-upstream:8080/f/F39", passthrough=False)
            path, body = "/v1/chat/completions", S.chat_body(model="claude-sonnet-5-5")
        time.sleep(0.5)
        result = S.stream_request(DIRECT, path, key, body, headers={"session_id": S.fresh_session()}, timeout=100)
        time.sleep(1)
        records = [row for row in S.fault_records_since(mark) if row.get("stream")]
        raw_lines = [line for _, line in result["lines"]]
        ev = events(result, S)
        usage = usage_rows(S, key_name, started)
        check(checks, result["status"] == 200, "HTTP 200")
        check(checks, result["error"] is None, "no client transport error")
        if case_id in ("PT-7", "PT-8"):
            a_spec = "tag=qa-pt-a"
            b_spec = "tag=qa-pt-b"
            check(checks, any(r["outcome"] == "cancelled" and a_spec in r.get("spec", "") for r in records), "A deadline attempt cancelled")
            check(checks, any("response.completed" in line for line in raw_lines), "B completed")
            check(checks, any(b_spec in r.get("spec", "") and r["outcome"] == "completed" for r in records), "B upstream attempt completed")
            check(checks, 10 <= result["elapsed"] <= 22, "12-second deadline switches before A can answer at 45 seconds")
            if case_id == "PT-7":
                first_data = next((i for i, line in enumerate(raw_lines) if line.startswith("data:")), len(raw_lines))
                check(checks, any(line.startswith(":") for line in raw_lines[:first_data]), "comment heartbeat precedes semantic data")
        elif case_id == "PT-9":
            check(checks, any("response.failed" in line for line in raw_lines), "post-output failure reaches client")
            check(checks, "stream_timeout" in json.dumps(ev), "stall ends with stream_timeout, not natural EOF")
            check(checks, 29 <= result["elapsed"] <= 38, "30-second stall deadline, before the 50-second upstream close")
            check(checks, any(r["outcome"] == "cancelled" and "tag=qa-pt-a" in r.get("spec", "") for r in records), "stalled A cancelled")
            check(checks, not any("F00" in r.get("spec", "") for r in records), "no replay after output")
            check(checks, len(usage) == 1, "one usage row")
        elif case_id == "PT-11":
            check(checks, any("response.failed" in line for line in raw_lines), "post-output failure reaches client")
            check(checks, len(usage) == 1, "exactly one usage row")
            if usage:
                check(checks, all(int(usage[0].get(field) or 0) == 0 for field in ("input_tokens", "output_tokens", "cache_read_tokens")), "failed passthrough is zero-token")
        elif case_id == "F38-9":
            first_data = next((i for i, line in enumerate(raw_lines) if line.startswith("data:")), len(raw_lines))
            check(checks, any(line == ": keep-alive" for line in raw_lines[:first_data]), "wire comment is before first data")
            check(checks, any(e.get("type") == "response.completed" for e in ev), "normal completion after comment")
            comment_at = next((at for at, line in result["lines"] if line == ": keep-alive"), None)
            data_at = next((at for at, line in result["lines"] if line.startswith("data:")), None)
            check(checks, comment_at is not None and data_at is not None and comment_at < 1.5 and data_at - comment_at >= 1.8, "upstream comment flushed immediately across the 2-second semantic gap")
        elif case_id == "F38-12":
            types = [e.get("type") for e in ev]
            check(checks, types == ["response.created", "response.failed"], "exactly created then failed")
            check(checks, not any(line.lstrip().startswith(("{", "[")) for line in raw_lines), "no bare JSON after comment")
            check(checks, any(line == ": keep-alive" for line in raw_lines), "upstream comment actually reaches client")
            check(checks, RULE_MESSAGE in json.dumps(ev), "temporary error rule matched, proved by unique message")
            check(checks, len([r for r in records if r.get("fault") == "F38"]) == 1, "non-retryable failure uses one upstream attempt")
            ids = [e.get("response", {}).get("id") for e in ev if e.get("type") in ("response.created", "response.failed")]
            check(checks, len(ids) == 2 and ids[0] and ids[0] == ids[1], "failure keeps one response ID")
        else:
            probe = S.stream_request(S.FAULT, "/f/F39;tag=qa-f39-wire/v1/messages", "sk-fault-probe", S.anthropic_body(), timeout=10)
            probe_events = S.data_events(probe)
            write_json(args.evidence / f"{case_id}.upstream-wire.json", probe)
            terminal = probe_events[-1] if probe_events else {}
            check(checks, probe["error"] is None and terminal.get("type") == "message_delta" and terminal.get("delta", {}).get("stop_reason") == "end_turn" and not any(e.get("type") == "message_stop" for e in probe_events), "direct upstream trace ends at message_delta(end_turn), without message_stop")
            expected = {"input_tokens": 120, "output_tokens": 24, "cache_read_input_tokens": 64}
            check(checks, terminal.get("usage") == expected, "upstream terminal usage is exactly 120/24/64")
            check(checks, len(records) == 1 and records[0].get("fault") == "F39" and records[0].get("path", "").endswith("/v1/messages") and records[0].get("outcome") == "completed", "actual gateway attempt used Anthropic F39 and completed")
            check(checks, not any(e.get("error") or e.get("type") == "response.failed" for e in ev), "no error or failed terminal")
            if case_id == "F39-5R":
                check(checks, sum(e.get("type") == "response.completed" for e in ev) == 1, "Responses conversion completes once")
            else:
                check(checks, sum(line.strip() == "data: [DONE]" for line in raw_lines) == 1 and any(choice.get("finish_reason") == "stop" for e in ev for choice in e.get("choices", [])), "Chat conversion sends finish_reason stop and one DONE")
            check(checks, len(usage) == 1, "exactly one usage row")
            check(checks, len(usage) == 1 and all(usage[0].get(field) == value for field, value in {"input_tokens": 120, "output_tokens": 24, "cache_read_tokens": 64}.items()) and float(usage[0].get("actual_cost") or 0) > 0, "complete terminal usage billed exactly 120/24/64 with positive cost")
        log = subprocess.run(["docker", "logs", "--since", started, "--timestamps", APP_CONTAINER], capture_output=True, text=True, timeout=30)
        (args.evidence / f"{case_id}.app.log").write_text(log.stdout + log.stderr)
        row = {"id": case_id, "ok": all(item["ok"] for item in checks), "checks": checks,
               "started_at": started, "finished_at": utc(), "request": result,
               "raw_sse_lines": raw_lines, "events": ev, "upstream": records, "usage": usage}
        write_json(args.evidence / f"{case_id}.json", row)
        meta["cases"].append({"id": case_id, "ok": row["ok"]})
        write_json(args.evidence / "RUN.json", meta)
        print(json.dumps({"case": case_id, "ok": row["ok"], "failed": [item["check"] for item in checks if not item["ok"]]}, ensure_ascii=False), flush=True)
        return row

    def terminated(signum, _frame):
        raise SystemExit("terminated by signal " + str(signum))

    signal.signal(signal.SIGTERM, terminated)
    try:
        configure_settings()
        records = []
        for case_id in CASES:
            records.append(run_case(case_id))
        meta["ok"] = all(record["ok"] for record in records)
    except BaseException as exc:  # noqa: BLE001
        meta["exception"] = type(exc).__name__ + ": " + str(exc)
        meta["ok"] = False
    finally:
        restore()
        meta["finished_at"] = utc()
        write_json(args.evidence / "RUN.json", meta)
        write_json(args.evidence / "SUMMARY.json", {key: meta.get(key) for key in
                   ("label", "started_at", "finished_at", "cases", "restored", "restore_errors", "ok", "exception")})
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()
    print(json.dumps({"label": args.label, "ok": meta.get("ok"), "restored": meta.get("restored"),
                      "evidence": str(args.evidence)}, ensure_ascii=False))
    return 0 if meta.get("ok") and meta.get("restored") else 1


if __name__ == "__main__":
    raise SystemExit(main())
