#!/usr/bin/env python3
"""0.4.5 Gateway→Anthropic supplemental checks; deploy beside scenarios.py.

Plan-only unless --execute. Run AFTER the serial 71-case suite, under its
same .phase2.lock. Only the two G-anthropic-stall fixture accounts and the
main stack resilience settings are temporarily changed, then restored.
No image builds, service restarts, or changes to the independent soak.
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

GROUP = "G-anthropic-stall"
NAMES = ("anth-stall", "anth-ok2")
MODES = {
    "retry": ("F02;after=0.1", "message_start → overloaded → B success", None),
    "post_error": ("F26;after=0.1", "output → overloaded; one usage row", (10, 1, 0)),
    "post_drop": ("F06;secs=1", "output → interrupted body; one usage row with cache", (120, 1, 64)),
    "post_stall": ("F06;secs=50", "output → 30 s stall; one usage row with cache", (120, 1, 64)),
    "first_content": ("F21;secs=45", "message_start → 12 s first-content deadline → B", None),
    "nonretry": ("F05;after=0.1", "message_start → invalid_request_error; no replay", None),
}
RESTORE_FIELDS = (
    "credentials", "extra", "priority", "status", "error_message", "schedulable",
    "rate_limited_at", "rate_limit_reset_at", "overload_until",
    "temp_unschedulable_until", "temp_unschedulable_reason", "last_used_at",
)


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def write_json(path, data):
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")


def quoted(value):
    return "'" + str(value).replace("'", "''") + "'"


def cmd(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--candidate", help="PM 明确放行的完整提交 SHA；没有默认候选")
    parser.add_argument("--build", type=Path, help="唯一候选 BUILD.json")
    parser.add_argument("--evidence", type=Path, help="new, non-existing directory")
    parser.add_argument("--expected-image-id", help="candidate BUILD.json image_id")
    args = parser.parse_args()
    plan = {"mode": "plan-only", "candidate": args.candidate, "serial": True,
            "cases": [f"GW045-{protocol}-{mode}" for protocol in ("responses", "chat") for mode in MODES],
            "reuse_group": GROUP, "accounts": list(NAMES),
            "temporary_settings": {"anthropic_first_event_timeout_seconds": 12,
                                   "text_stall_seconds": 30, "keepalive_delay_seconds": 5,
                                   "keepalive_interval_seconds": 5, "same_account_retry_max": 0},
            "execution_gate": "after 71 cases; same .phase2.lock; expected candidate image ID",
            "estimated_minutes": "3–5", "not_covered": ["OAuth passthrough", "attempt response headers",
                     "raw Chat empty error", "message_delta end_turn without message_stop",
                     "first-write cancellation race", "upstream comments", "failed frame carrying usage"]}
    if not args.execute:
        print(json.dumps(plan, indent=2, ensure_ascii=False))
        return 0
    if not args.build or not args.evidence or not re.fullmatch(r"[0-9a-f]{40}", args.candidate or "") or not re.fullmatch(r"sha256:[0-9a-f]{64}", args.expected_image_id or ""):
        parser.error("--execute requires the released --candidate, --build, --evidence and --expected-image-id")
    build = json.loads(args.build.read_text())
    if (build.get("source_commit") != args.candidate or build.get("version") != "0.4.5"
            or build.get("image_id") != args.expected_image_id or not build.get("same_source_tree")
            or build.get("source_tree") != build.get("pr_head_tree")):
        parser.error("BUILD does not identify the released candidate and matching source tree")
    here = Path(__file__).resolve().parent
    os.chdir(here)
    # The operator must deploy beside the existing, already provisioned scripts.
    for required in ("scenarios.py", "fixtures.py", "fixtures/keys.json", "compose.yaml"):
        if not (here / required).is_file():
            raise RuntimeError("missing staging dependency: " + required)
    if not os.environ.get("ADMIN_EMAIL") or not os.environ.get("ADMIN_PASSWORD"):
        raise RuntimeError("load only staging .env before execution")
    lock = (here / ".phase2.lock").open("a+")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise RuntimeError("71-case suite or upgrade still holds .phase2.lock") from None
    active = [line for line in cmd("ps", "-eo", "args").splitlines()
              if re.search(r"(?:^|\s|/)scenarios\.py(?:\s|$)", line)]
    if active:
        raise RuntimeError("standalone scenarios.py is still running; do not overlap")
    mem = {line.split(":")[0]: int(line.split()[1]) * 1024
           for line in Path("/proc/meminfo").read_text().splitlines() if ":" in line}
    resources = {"at": utc(), "available_memory_bytes": mem["MemAvailable"],
                 "free_disk_bytes": shutil.disk_usage(here).free, "load_average": os.getloadavg()}
    if mem["MemAvailable"] < 2 * 2**30 or resources["free_disk_bytes"] < 10 * 2**30:
        raise RuntimeError("resource reserve below 2 GiB memory / 10 GiB disk")
    app = json.loads(cmd("docker", "inspect", "xy2api-staging-app-1"))[0]
    if app["Image"] != args.expected_image_id:
        raise RuntimeError("main app does not run the expected candidate image")
    binary = cmd("docker", "exec", "xy2api-staging-app-1", "sha256sum", "/app/xy2api").split()[0]
    if binary != build.get("binary_sha256"):
        raise RuntimeError("main binary does not match unique candidate BUILD")
    args.evidence = args.evidence.resolve()
    args.evidence.mkdir(parents=True, exist_ok=False, mode=0o700)
    os.chmod(args.evidence, 0o700)
    sys.path.insert(0, str(here))
    import scenarios as S
    from fixtures import items
    api = S.admin_api()
    accounts = {a["name"]: a for a in items(api.call("GET", "/api/v1/admin/accounts?page=1&page_size=500"))}
    chosen = [accounts[name] for name in NAMES]
    snapshots = []
    for a in chosen:
        row = json.loads(S.psql("SELECT to_jsonb(a) FROM accounts a WHERE id=" + str(int(a["id"]))))
        if row["platform"] != "anthropic" or row["type"] != "apikey" or "fault-upstream" not in row["credentials"].get("base_url", ""):
            raise RuntimeError("refuse to change a non-fixture account")
        snapshots.append({"id": row["id"], "name": row["name"],
                          "fields": {field: row[field] for field in RESTORE_FIELDS}})
    saved_settings = copy.deepcopy(api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"])
    backup = {"settings": saved_settings, "accounts": snapshots}
    backup_path = args.evidence / "RESTORE_PRIVATE.json"
    write_json(backup_path, backup)
    os.chmod(backup_path, 0o600)
    meta = {"candidate": args.candidate, "build": build, "started_at": utc(), "image_id": app["Image"],
            "binary_sha256": binary,
            "resources": resources, "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "cases": [], "restored": False, "plan": plan}
    write_json(args.evidence / "RUN.json", meta)

    def account_put(snap, credentials, priority, restore=False):
        fields = snap["fields"]
        payload = {"credentials": credentials, "priority": priority,
                   "status": fields["status"] if restore else "active",
                   "schedulable": fields["schedulable"] if restore else True,
                   "extra": fields["extra"] or {}}
        api.call("PUT", "/api/v1/admin/accounts/" + str(snap["id"]), payload)

    def clear_pause(snap):
        api.call("DELETE", "/api/v1/admin/accounts/" + str(snap["id"]) + "/temp-unschedulable")
        S.psql("UPDATE accounts SET status='active', schedulable=true, error_message=NULL, "
               "rate_limited_at=NULL, rate_limit_reset_at=NULL, overload_until=NULL, "
               "temp_unschedulable_until=NULL, temp_unschedulable_reason=NULL WHERE id=" + str(snap["id"]))

    def restore():
        errors = []
        try:
            api.call("PUT", "/api/v1/admin/settings/gateway-resilience", saved_settings)
        except BaseException as exc:
            errors.append("settings: " + str(exc))
        for snap in snapshots:
            try:
                api.call("DELETE", "/api/v1/admin/accounts/" + str(snap["id"]) + "/temp-unschedulable")
                # API restores config and invalidates account caches. Exact DB
                # runtime fields then return to their pre-supplement values.
                account_put(snap, snap["fields"]["credentials"], snap["fields"]["priority"], restore=True)
                values = quoted(json.dumps(snap["fields"]))
                assignments = ", ".join(f"{field}=r.{field}" for field in RESTORE_FIELDS)
                S.psql(f"UPDATE accounts a SET {assignments} FROM jsonb_populate_record(NULL::accounts, {values}::jsonb) r WHERE a.id={snap['id']}")
                actual = json.loads(S.psql("SELECT to_jsonb(a) FROM accounts a WHERE id=" + str(snap["id"])))
                if any(actual.get(field) != value for field, value in snap["fields"].items()):
                    raise RuntimeError("restored account DB fields differ")
            except BaseException as exc:
                errors.append(snap["name"] + ": " + str(exc))
        try:
            actual = api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"]
            if actual != saved_settings:
                errors.append("restored settings differ")
        except BaseException as exc:
            errors.append("settings verification: " + str(exc))
        meta["restored"] = not errors
        meta["restore_errors"] = errors

    def terminated(signum, _frame):
        raise SystemExit("terminated by signal " + str(signum))

    signal.signal(signal.SIGTERM, terminated)
    try:
        settings = copy.deepcopy(saved_settings)
        settings["enabled"] = True
        settings["stream"].update(prelude_staging=True, strict_terminal=True)
        settings["budget"].update(enabled=True, anthropic_first_event_timeout_seconds=12,
                                   same_account_retry_max=0)
        settings["stall"].update(enabled=True, text_stall_seconds=30)
        settings["keepalive"].update(enabled=True, delay_seconds=5, interval_seconds=5)
        api.call("PUT", "/api/v1/admin/settings/gateway-resilience", settings)
        write_json(args.evidence / "SETTINGS.json", api.call("GET", "/api/v1/admin/settings/gateway-resilience")["settings"])
        for protocol in ("responses", "chat"):
            for mode, (fault, title, expected_usage) in MODES.items():
                sid = f"GW045-{protocol}-{mode}"
                print(json.dumps({"event": "start", "case": sid, "at": utc()}, ensure_ascii=False), flush=True)
                tag = "supp045-" + uuid.uuid4().hex
                for i, snap in enumerate(snapshots):
                    clear_pause(snap)
                    creds = copy.deepcopy(snap["fields"]["credentials"])
                    host = "fault-upstream" if i == 0 else "xy2api-staging-fault-upstream-1"
                    spec = fault if i == 0 else "F00;first=0.05"
                    creds["base_url"] = f"http://{host}:8080/f/{spec};tag={tag}-{'a' if i == 0 else 'b'}"
                    account_put(snap, creds, i + 1)
                time.sleep(1)
                since = utc()
                mark = S.fault_last_id()
                body = (S.responses_body(model="claude-sonnet-5-5", session=S.fresh_session())
                        if protocol == "responses" else S.chat_body(model="claude-sonnet-5-5"))
                # GatewayService derives its session from input/messages; the
                # OpenAI session header/cache key does not isolate these paths.
                if protocol == "responses":
                    body["input"] = "hello from staging " + tag
                else:
                    body["messages"][0]["content"] = "hello from staging " + tag
                path = "/v1/responses" if protocol == "responses" else "/v1/chat/completions"
                result = S.stream_request(S.CDN125, path, S.KEYS[GROUP], body,
                                          headers={"session_id": S.fresh_session()}, timeout=90)
                time.sleep(3)
                events = S.data_events(result)
                records = [r for r in S.fault_records_since(mark) if tag in r.get("spec", "") and r["stream"]]
                rows = json.loads(S.psql("SELECT coalesce(jsonb_agg(jsonb_build_object('id',u.id,'account_id',u.account_id,"
                                        "'input_tokens',u.input_tokens,'output_tokens',u.output_tokens,"
                                        "'cache_read_tokens',u.cache_read_tokens,'actual_cost',u.actual_cost)), '[]'::jsonb) "
                                        "FROM usage_logs u WHERE api_key_id=(SELECT id FROM api_keys WHERE name='key-G-anthropic-stall' "
                                        "AND deleted_at IS NULL) AND created_at>=" + quoted(since)))
                attempts = S.attempt_events(NAMES[0], since)
                checks = []
                def check(ok, label):
                    checks.append({"check": label, "ok": bool(ok)})
                a_records = [r for r in records if tag + "-a" in r["spec"]]
                b_records = [r for r in records if tag + "-b" in r["spec"]]
                check(result["status"] == 200, "HTTP 200 SSE")
                check(result["error"] is None, "no client transport error")
                check(bool(a_records) and all(r["path"].endswith("/messages") for r in records), "Anthropic upstream path exercised")
                if protocol == "responses":
                    created = [e for e in events if e.get("type") == "response.created"]
                    failures = [e for e in events if e.get("type") == "response.failed"]
                    successes = [e for e in events if e.get("type") == "response.completed"]
                    check(len(created) == 1, "exactly one response.created")
                    if created:
                        terminal = failures + successes
                        check(len(terminal) == 1 and created[0]["response"]["id"] == terminal[0]["response"]["id"],
                              "one terminal with the opening response ID")
                else:
                    roles = [c["delta"]["role"] for e in events for c in e.get("choices", []) if "role" in c.get("delta", {})]
                    failures = [e for e in events if e.get("error")]
                    successes = [c for e in events for c in e.get("choices", []) if c.get("finish_reason") == "stop"]
                    check(len(roles) <= 1 if mode == "nonretry" else roles == ["assistant"], "no duplicated Chat assistant role")
                    check(sum(line.strip() == "data: [DONE]" for _, line in result["lines"]) == 1, "exactly one DONE")
                if mode in ("retry", "first_content"):
                    check(bool(b_records) and bool(successes) and not failures, "A discarded; B completes")
                    check(len(rows) == 1 and rows[0]["account_id"] == snapshots[1]["id"], "only B is billed once")
                    if mode == "first_content":
                        check(any(r["outcome"] == "cancelled" for r in a_records), "silent A cancelled")
                        check(10 <= result["elapsed"] < 40, "12 s first-content cutoff, bounded replay")
                        check(any(e.get("kind") == "first_output_timeout" for e in attempts), "first_output_timeout recorded")
                        first_data = next((t for t, line in result["lines"] if line.startswith("data:")), float("inf"))
                        before = [(t, line) for t, line in result["lines"] if line.startswith(":") and t < first_data]
                        check(bool(before) and before[0][0] < 10, "comment heartbeat before first semantic output")
                else:
                    check(not b_records, "no replay after nonretryable failure or real output")
                    check(len(failures) == 1 and not successes, "one failure terminal, no synthetic success")
                    if mode == "nonretry":
                        check(len(a_records) == 1, "invalid request has exactly one upstream attempt")
                    if mode == "post_stall":
                        check("stream_timeout" in json.dumps(failures), "stall terminal is stream_timeout")
                        check(28 <= result["elapsed"] <= 40, "30 s stall detected before upstream 50 s drop")
                    if expected_usage is not None:
                        check(len(rows) == 1, "one usage row despite abnormal end")
                        if len(rows) == 1:
                            check(rows[0]["account_id"] == snapshots[0]["id"], "usage attributed to failed A")
                            actual = tuple(rows[0][k] for k in ("input_tokens", "output_tokens", "cache_read_tokens"))
                            check(actual == expected_usage, f"message_start usage retained {actual} == {expected_usage}")
                            check(float(rows[0]["actual_cost"]) > 0, "positive billed cost")
                record = {"id": sid, "title": title, "ok": all(c["ok"] for c in checks), "checks": checks,
                          "started_at": since, "finished_at": utc(), "request_body": body, "result": result,
                          "upstream": records, "usage": rows, "attempt_events": attempts}
                write_json(args.evidence / (sid + ".json"), record)
                meta["cases"].append({"id": sid, "ok": record["ok"]})
                write_json(args.evidence / "RUN.json", meta)
                print(json.dumps({"event": "end", "case": sid, "ok": record["ok"], "at": utc(),
                                  "failed": [c["check"] for c in checks if not c["ok"]]}, ensure_ascii=False), flush=True)
    except BaseException as exc:
        meta["exception"] = type(exc).__name__ + ": " + str(exc)
    finally:
        restore()
        try:
            after = json.loads(cmd("docker", "inspect", "xy2api-staging-app-1"))[0]
            after_binary = cmd("docker", "exec", "xy2api-staging-app-1", "sha256sum", "/app/xy2api").split()[0]
            meta["identity_unchanged"] = (after["Id"] == app["Id"] and after["Image"] == app["Image"]
                                            and after["State"]["StartedAt"] == app["State"]["StartedAt"]
                                            and after["RestartCount"] == app["RestartCount"] and after_binary == binary)
        except BaseException as exc:
            meta["identity_unchanged"] = False
            meta["identity_exception"] = type(exc).__name__ + ": " + str(exc)
        meta["finished_at"] = utc()
        meta["ok"] = bool(meta["cases"]) and len(meta["cases"]) == 12 and all(c["ok"] for c in meta["cases"]) and meta["restored"] and meta["identity_unchanged"] and "exception" not in meta
        write_json(args.evidence / "RUN.json", meta)
        write_json(args.evidence / "SUMMARY.json", {k: meta[k] for k in ("candidate", "started_at", "finished_at", "cases", "restored", "restore_errors", "identity_unchanged", "ok")})
        print(json.dumps({"event": "finished", "ok": meta["ok"], "restored": meta["restored"], "evidence": str(args.evidence)}, ensure_ascii=False), flush=True)
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()
    return 0 if meta["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
