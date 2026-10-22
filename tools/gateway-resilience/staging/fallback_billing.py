#!/usr/bin/env python3
"""Targeted fallback billing check. Plan-only until PM releases a development SHA.

Run beside the staging compose.yaml. Creates its own user, A/B groups, accounts
and K1/K2 keys. Only return_stable_seconds is temporarily changed globally.
No builds, app switches, shared fixtures, real upstreams, or Redis flushes.
"""
from __future__ import annotations

import argparse
import copy
import datetime as dt
from decimal import Decimal
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


MODEL_A = "gpt-6.1-sol"
MODEL_B = "gpt-6-astra"
STABLE_SECONDS = 15
SETTINGS = "/api/v1/admin/settings/gateway-resilience"
CASES = {
    "FBILL-1": "A healthy: K1 served by A, user×A rate 0.8; baseline models",
    "FBILL-2": "A unschedulable: K1 served by B with origin A",
    "FBILL-3": "User list/detail/filter/stats/trend/group distribution use A",
    "FBILL-4": "Available channels/groups omit every availability_fallback* field",
    "FBILL-5": "Fallback actual_cost equals baseline and total_cost × 0.8, not × 2",
    "FBILL-6": "Admin list/filter retain serving B and origin A",
    "FBILL-7": "B-only model rejected with 404; /v1/models remains A's list",
    "FBILL-8": "Recovered A: first K2 request uses A; early K1 still uses B",
    "FBILL-9": "After 15 s stable: K1 returns to A, same user projection/price",
    "FBILL-10": "Restore setting; delete only created fixtures and their usage",
}


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def scrub(value):
    if isinstance(value, dict):
        return {k: "<redacted>" if k.lower() in {
            "key", "password", "password_hash", "access_token", "refresh_token",
            "credentials", "authorization", "jwt_secret", "totp_encryption_key",
        } else scrub(v) for k, v in value.items()}
    if isinstance(value, list):
        return [scrub(v) for v in value]
    return value


def forbidden_fields(value, prefix):
    if isinstance(value, dict):
        return [k for k in value if k.startswith(prefix)] + [
            k for v in value.values() for k in forbidden_fields(v, prefix)]
    if isinstance(value, list):
        return [k for v in value for k in forbidden_fields(v, prefix)]
    return []


def items(data):
    return data.get("items", []) if isinstance(data, dict) else data or []


class API:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.token = ""

    def request(self, method, path, body=None, token=None):
        request = urllib.request.Request(
            self.base + path, method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "User-Agent": "xy2api-fallback-qa/1"})
        auth = self.token if token is None else token
        if auth:
            request.add_header("Authorization", "Bearer " + auth)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as error:
            raw = error.read()
            try:
                data = json.loads(raw)
            except ValueError:
                data = {"non_json_bytes": len(raw)}
            return error.code, data

    def call(self, method, path, body=None, token=None):
        status, data = self.request(method, path, body, token)
        if status != 200 or data.get("code", 0) != 0:
            # Do not print response bodies that could contain credentials.
            raise RuntimeError(f"{method} {path}: HTTP {status}, code={data.get('code')}")
        return data.get("data")


class Run:
    def __init__(self, args):
        self.args = args
        self.api = API(args.base_url)
        self.out = {"started_at": utc(), "kind": "development-targeted-validation",
                    "dev_sha": args.dev_sha, "checks": [], "cases": CASES,
                    "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
        self.prefix = "qa-fallback-" + uuid.uuid4().hex[:12]
        self.created = {"groups": [], "accounts": [], "users": [], "keys": [], "channels": []}
        self.saved = None
        self.setting_changed = False
        self.user_token = ""
        self.uid = None
        self.before = None
        self.api_keys = []

    def check(self, name, ok):
        self.out["checks"].append({"check": name, "ok": bool(ok), "at": utc()})
        if not ok:
            raise AssertionError(name)

    def capture(self, *command, stdin=None):
        result = subprocess.run(command, input=stdin, text=True, capture_output=True,
                                cwd=self.args.staging_dir, timeout=45)
        if result.returncode:
            raise RuntimeError(f"command {command[0]} failed ({result.returncode}); output withheld")
        return result.stdout.strip()

    def sql(self, sql):
        return self.capture("docker", "compose", "-p", self.args.project, "exec", "-T",
                            "postgres", "psql", "-X", "-U", self.args.db_user, "-d", self.args.database,
                            "-v", "ON_ERROR_STOP=1", "-Atq", stdin=sql)

    def identity(self):
        app = self.args.project + "-app-1"
        i = json.loads(self.capture("docker", "inspect", app))[0]
        return {"container_id": i["Id"], "image_id": i["Image"],
                "started_at": i["State"]["StartedAt"], "restarts": i["RestartCount"],
                "health": i["State"].get("Health", {}).get("Status"),
                "binary_sha256": self.capture("docker", "exec", app, "sha256sum", "/app/xy2api").split()[0],
                "revision": (i["Config"].get("Labels") or {}).get("org.opencontainers.image.revision")}

    def evidence(self, name, value):
        write_json(self.args.evidence / (name + ".json"), scrub(value))

    def create(self, kind, body, token=None):
        path = "/api/v1/keys" if kind == "keys" else "/api/v1/admin/" + kind
        value = self.api.call("POST", path, body, token)
        self.created[kind].append({"id": int(value["id"]),
                                   "name": body.get("name", body.get("email"))})
        self.evidence("OWNERSHIP", {"prefix": self.prefix, "created": self.created})
        return value

    def setup(self):
        self.before = self.identity()
        self.out["identity_before"] = self.before
        self.check("expected development image and binary", self.before["image_id"] == self.args.expected_image_id
                   and self.before["binary_sha256"] == self.args.expected_binary_sha256
                   and self.before["revision"] == self.args.dev_sha and self.before["health"] == "healthy")
        available = int(next(l.split()[1] for l in Path("/proc/meminfo").read_text().splitlines()
                             if l.startswith("MemAvailable:"))) * 1024
        self.out["resources"] = {"available_memory_bytes": available,
                                 "disk_free_bytes": shutil.disk_usage(self.args.staging_dir).free}
        self.check("resource reserve", available >= 2 * 2**30
                   and self.out["resources"]["disk_free_bytes"] >= 2 * 2**30)
        login = self.api.call("POST", "/api/v1/auth/login", {
            "email": os.environ["ADMIN_EMAIL"], "password": os.environ["ADMIN_PASSWORD"]}, token="")
        self.api.token = login["access_token"]
        compliance = self.api.call("GET", "/api/v1/admin/compliance")
        self.check("existing admin compliance accepted", not compliance.get("required"))
        public = self.api.call("GET", "/api/v1/settings/public", token="")
        self.check("available-channels feature already enabled (environment prerequisite)",
                   public.get("available_channels_enabled") is True)
        self.saved = self.api.call("GET", SETTINGS)["settings"]
        self.check("fallback already enabled", self.saved["enabled"] and self.saved["fallback_group"]["enabled"])
        self.check("binding TTL exceeds return test", self.saved["fallback_group"]["binding_ttl_seconds"] >= 60)
        self.evidence("SETTINGS_BEFORE", self.saved)
        changed = copy.deepcopy(self.saved)
        changed["fallback_group"]["return_stable_seconds"] = STABLE_SECONDS
        # Mark before HTTP: a lost response must still trigger restoration.
        self.setting_changed = True
        self.api.call("PUT", SETTINGS, changed)
        self.check("only return timer changed", self.api.call("GET", SETTINGS)["settings"] == changed)
        self.groups = {}
        self.accounts = {}
        for label, rate, models in (("B", 2.0, [MODEL_A, MODEL_B]), ("A", 1.0, [MODEL_A])):
            group = self.create("groups", {"name": self.prefix + "-" + label, "platform": "openai",
                "subscription_type": "standard", "is_exclusive": True, "rate_multiplier": rate,
                "model_allowlist": {"enabled": True, "models": models}})
            self.groups[label] = group
            account = self.create("accounts", {"name": self.prefix + "-account-" + label,
                "platform": "openai", "type": "apikey", "group_ids": [group["id"]],
                "concurrency": 10, "priority": 1, "credentials": {
                    "api_key": "sk-fault-" + self.prefix + label,
                    "base_url": "http://fault-upstream:8080/f/F00;first=0.05;tag=" + self.prefix + label,
                    "model_mapping": {m: m for m in models}},
                "extra": {"openai_responses_supported": True}})
            self.accounts[label] = account
        self.a, self.b = (int(self.groups[label]["id"]) for label in ("A", "B"))
        self.api.call("PUT", f"/api/v1/admin/groups/{self.a}", {"availability_fallback_group_id": self.b})
        # No price overrides/mappings; attached only to this run's groups.
        self.channel = self.create("channels", {"name": self.prefix + "-channel",
            "group_ids": [self.a, self.b], "model_pricing": [], "restrict_models": False})
        password = secrets.token_urlsafe(32)
        email = self.prefix + "@example.invalid"
        user = self.create("users", {"email": email, "password": password, "balance": 100,
                                     "concurrency": 10, "allowed_groups": [self.a]})
        self.uid = int(user["id"])
        self.api.call("PUT", f"/api/v1/admin/groups/{self.a}/rate-multipliers",
                      {"entries": [{"user_id": self.uid, "rate_multiplier": 0.8}]})
        self.user_token = self.api.call("POST", "/api/v1/auth/login",
                                      {"email": email, "password": password}, token="")["access_token"]
        for name in ("K1", "K2"):
            self.api_keys.append(self.create("keys", {"name": self.prefix + "-" + name, "group_id": self.a}, self.user_token))
        self.body = {"model": MODEL_A, "input": self.prefix + " identical-price-request", "stream": False}
        time.sleep(2.2)  # The availability snapshot has a two-second cache.

    def rows(self, kid, after=0):
        return json.loads(self.sql(
            "SELECT coalesce(json_agg(t),'[]'::json) FROM (SELECT id,request_id,user_id,api_key_id,account_id,"
            "group_id,origin_group_id,model,input_tokens,output_tokens,cache_read_tokens,"
            "cache_creation_tokens,rate_multiplier,total_cost,actual_cost FROM usage_logs "
            f"WHERE user_id={self.uid} AND api_key_id={int(kid)} AND id>{int(after)} ORDER BY id) t;"))

    def request_usage(self, name, key):
        prior = self.rows(key["id"])
        after = max((int(r["id"]) for r in prior), default=0)
        status, response = self.api.request("POST", "/v1/responses", self.body, key["key"])
        self.evidence(name + "_response", {"status": status, "body": response, "at": utc()})
        self.check(name + ": HTTP 200", status == 200)
        deadline = time.monotonic() + 10
        while True:
            rows = self.rows(key["id"], after)
            if rows or time.monotonic() >= deadline:
                break
            time.sleep(0.2)
        self.evidence(name + "_usage", rows)
        self.check(name + ": exactly one owned usage row", len(rows) == 1)
        row = rows[0]
        self.check(name + ": user×A multiplier", Decimal(str(row["rate_multiplier"])) == Decimal("0.8"))
        cost, total = Decimal(str(row["actual_cost"])), Decimal(str(row["total_cost"]))
        self.check(name + ": actual_cost=base×0.8, not B×2", total > 0
                   and abs(cost - total * Decimal("0.8")) <= Decimal("0.0000000001")
                   and abs(cost - total * 2) > Decimal("0.0000000001"))
        return row

    def route(self, name, row, serving, origin):
        self.check(name + ": serving account/group and origin", row["account_id"] == self.accounts[serving]["id"]
                   and row["group_id"] == self.groups[serving]["id"] and row["origin_group_id"] == origin)

    def user_get(self, name, path):
        data = self.api.call("GET", path, token=self.user_token)
        self.evidence(name, data)
        self.check(name + ": no origin_group fields", not forbidden_fields(data, "origin_group"))
        self.check(name + ": no availability_fallback fields", not forbidden_fields(data, "availability_fallback"))
        return data

    def projection(self, name, row, admin_origin):
        visible = self.user_get(name + "_user_detail", f"/api/v1/usage/{row['id']}")
        self.check(name + ": user sees A", visible["group_id"] == self.a
                   and visible.get("group", {}).get("id") == self.a
                   and visible["group"]["name"] == self.groups["A"]["name"])
        listing = self.api.call("GET", f"/api/v1/admin/usage?user_id={self.uid}&api_key_id={row['api_key_id']}&page_size=100")
        matching = [v for v in items(listing) if v["id"] == row["id"]]
        self.check(name + ": exact admin usage row present", len(matching) == 1)
        admin = matching[0]
        self.evidence(name + "_admin_row", admin)
        self.check(name + ": admin sees serving/origin", admin["group_id"] == row["group_id"]
                   and admin.get("group", {}).get("id") == row["group_id"]
                   and admin.get("origin_group_id") == admin_origin
                   and (admin_origin is None or admin.get("origin_group_name") == self.groups["A"]["name"]))
        self.check(name + ": both API amounts match stored amount", all(
            Decimal(str(v["actual_cost"])) == Decimal(str(row["actual_cost"]))
            and Decimal(str(v["rate_multiplier"])) == Decimal("0.8") for v in (visible, admin)))

    def models(self, name, key):
        status, data = self.api.request("GET", "/v1/models", token=key["key"])
        self.evidence(name, {"status": status, "body": data})
        self.check(name + ": HTTP 200", status == 200)
        return {m["id"] for m in data["data"]}

    def set_primary_schedulable(self, value):
        path = f"/api/v1/admin/accounts/{self.accounts['A']['id']}"
        self.api.call("POST", path + "/schedulable", {"schedulable": value})
        actual = self.api.call("GET", path)
        self.evidence("primary_schedulable_" + str(value).lower(), actual)
        self.check("primary scheduling switch readback=" + str(value), actual["schedulable"] is value)

    def validate(self):
        k1, k2 = self.api_keys
        normal = self.request_usage("normal", k1)
        self.route("normal", normal, "A", None)
        baseline_models = self.models("models_normal", k1)
        self.check("normal models are A only", baseline_models == {MODEL_A})
        self.set_primary_schedulable(False)
        time.sleep(2.2)
        fallback = self.request_usage("fallback", k1)
        self.route("fallback", fallback, "B", self.a)
        self.projection("fallback", fallback, self.a)
        price_fields = ("model", "input_tokens", "output_tokens", "cache_read_tokens",
                        "cache_creation_tokens", "rate_multiplier", "total_cost", "actual_cost")
        self.check("identical request has identical usage and price on A/B",
                   all(normal[f] == fallback[f] for f in price_fields))
        for group, present in ((self.a, True), (self.b, False)):
            data = self.user_get(f"user_list_{group}", f"/api/v1/usage?group_id={group}&page_size=100")
            rows = items(data)
            self.check(f"user filter {group}", any(x["id"] == fallback["id"] for x in rows) == present)
            self.check(f"all user list rows use A ({group})", all(x["group_id"] == self.a
                       and x.get("group", {}).get("name") == self.groups["A"]["name"] for x in rows))
            stats = self.user_get(f"user_stats_{group}", f"/api/v1/usage/stats?group_id={group}&timezone=UTC")
            self.check(f"stats count under {group}", stats["total_requests"] == (2 if present else 0))
            expected_cost = Decimal(str(normal["actual_cost"])) + Decimal(str(fallback["actual_cost"])) if present else Decimal(0)
            self.check(f"stats amount under {group}", abs(Decimal(str(stats["total_actual_cost"])) - expected_cost) <= Decimal("0.0000000001"))
            snap = self.user_get(f"user_snapshot_{group}",
                f"/api/v1/usage/dashboard/snapshot-v2?include_group_stats=true&group_id={group}&timezone=UTC")
            self.check(f"trend under {group}", sum(x["requests"] for x in snap["trend"]) == (2 if present else 0))
            self.check(f"model distribution under {group}", sum(x["requests"] for x in snap["models"]) == (2 if present else 0))
        snap = self.user_get("user_group_distribution", "/api/v1/usage/dashboard/snapshot-v2?include_group_stats=true&timezone=UTC")
        self.check("group distribution contains only A and both requests", len(snap["groups"]) == 1
                   and snap["groups"][0]["group_id"] == self.a and snap["groups"][0]["requests"] == 2
                   and snap["groups"][0]["group_name"] == self.groups["A"]["name"])
        channels = self.user_get("available_channels", "/api/v1/channels/available")
        own = [c for c in channels if c["name"] == self.channel["name"]]
        channel_groups = [g for c in own for section in c["platforms"] for g in section["groups"]]
        self.check("dedicated channel exposes A, not hidden exclusive B", len(own) == 1
                   and {g["id"] for g in channel_groups} == {self.a})
        available = self.user_get("available_groups", "/api/v1/groups/available")
        self.check("available groups include A (non-vacuous)", any(g["id"] == self.a for g in items(available)))
        for group, present in ((self.b, True), (self.a, False)):
            data = self.api.call("GET", f"/api/v1/admin/usage?user_id={self.uid}&group_id={group}&page_size=100")
            self.evidence(f"admin_list_{group}", data)
            self.check(f"admin filter serving {group}", any(r["id"] == fallback["id"] for r in items(data)) == present)
        group = self.api.call("GET", f"/api/v1/admin/groups/{self.a}")
        self.check("admin group retains fallback id", group["availability_fallback_group_id"] == self.b)
        self.check("fallback models equal baseline A", self.models("models_fallback", k1) == baseline_models)
        before = self.rows(k1["id"])
        status, denied = self.api.request("POST", "/v1/responses", {**self.body, "model": MODEL_B}, k1["key"])
        self.evidence("blocked_model", {"status": status, "body": denied})
        self.check("B-only model rejected by A whitelist", status == 404
                   and MODEL_A in json.dumps(denied) and "not available for this group" in json.dumps(denied))
        self.check("blocked model creates no billed usage", self.rows(k1["id"]) == before)
        self.set_primary_schedulable(True)
        time.sleep(2.2)
        # K2 has never called the gateway or /models during the outage.
        stable_start = time.monotonic()
        self.out["recovery_first_probe_at"] = utc()
        never_moved = self.request_usage("k2_first_recovered", k2)
        self.route("never-moved K2 first request", never_moved, "A", None)
        early = self.request_usage("k1_before_stable", k1)
        self.check("early K1 probe completed before stability deadline", time.monotonic() - stable_start < STABLE_SECONDS)
        self.route("early moved K1", early, "B", self.a)
        time.sleep(max(0, STABLE_SECONDS + 2 - (time.monotonic() - stable_start)))
        returned = self.request_usage("k1_returned", k1)
        self.out["return_probe_elapsed_seconds"] = round(time.monotonic() - stable_start, 3)
        self.route("stable moved K1", returned, "A", None)
        self.projection("returned", returned, None)
        self.check("returned A price equals fallback price", all(returned[f] == fallback[f] for f in price_fields))
        self.check("five successful requests, no delayed duplicate billing",
                   len(self.rows(k1["id"])) == 4 and len(self.rows(k2["id"])) == 1)

    def cleanup(self):
        errors = []

        def attempt(label, fn):
            try:
                fn()
            except Exception as error:
                errors.append(label + ": " + str(error))

        def restore():
            current = self.api.call("GET", SETTINGS)["settings"]
            old = self.saved["fallback_group"]["return_stable_seconds"]
            if current["fallback_group"]["return_stable_seconds"] not in (STABLE_SECONDS, old):
                raise RuntimeError("return timer changed concurrently; preserve it for manual reconciliation")
            current["fallback_group"]["return_stable_seconds"] = old
            self.api.call("PUT", SETTINGS, current)
            actual = self.api.call("GET", SETTINGS)["settings"]
            self.evidence("SETTINGS_AFTER", actual)
            self.check("settings restored exactly", actual == self.saved)

        if self.setting_changed:
            attempt("restore settings", restore)
        # Delete through APIs first to invalidate authentication/scheduler caches.
        # Only IDs returned from this run's successful creates are accepted.
        for kind in ("users", "channels", "accounts", "groups"):
            for obj in reversed(self.created[kind]):
                def remove(kind=kind, obj=obj):
                    column = "email" if kind == "users" else "name"
                    owned = self.sql(f"SELECT {column} FROM {kind} WHERE id={obj['id']};")
                    if owned != obj["name"] or not owned.startswith(self.prefix):
                        raise RuntimeError("fixture ownership mismatch")
                    if kind == "groups":
                        self.api.call("PUT", f"/api/v1/admin/groups/{obj['id']}", {"availability_fallback_group_id": 0})
                        self.api.call("DELETE", f"/api/v1/admin/groups/{obj['id']}/rate-multipliers")
                    self.api.call("DELETE", f"/api/v1/admin/{kind}/{obj['id']}")
                    active = "" if kind == "channels" else " AND deleted_at IS NULL"
                    self.check(f"removed {kind}/{obj['id']}", self.sql(
                        f"SELECT count(*) FROM {kind} WHERE id={obj['id']}{active};") == "0")
                attempt(f"delete {kind}/{obj['id']}", remove)
        if self.uid is not None:
            def remove_usage():
                # IDs are integers recorded at creation; never delete by a name prefix.
                key_ids = ",".join(str(k["id"]) for k in self.created["keys"]) or "0"
                self.sql("BEGIN; SET LOCAL lock_timeout='3s'; SET LOCAL statement_timeout='10s';\n"
                    f"DELETE FROM usage_logs WHERE user_id={self.uid} AND api_key_id IN ({key_ids});\n"
                    f"DELETE FROM usage_billing_dedup WHERE api_key_id IN ({key_ids});\n"
                    f"DELETE FROM usage_billing_dedup_archive WHERE api_key_id IN ({key_ids});\n"
                    f"DELETE FROM ops_error_logs WHERE user_id={self.uid} AND api_key_id IN ({key_ids});\n"
                    f"DELETE FROM gateway_undelivered_requests WHERE api_key_id IN ({key_ids});\nCOMMIT;")
                self.check("owned usage and active keys absent", self.sql(
                    f"SELECT (SELECT count(*) FROM usage_logs WHERE user_id={self.uid})+"
                    f"(SELECT count(*) FROM api_keys WHERE user_id={self.uid} AND deleted_at IS NULL);") == "0")
            attempt("delete owned usage", remove_usage)
        if self.before:
            def unchanged():
                after = self.identity()
                self.out["identity_after"] = after
                self.check("app identity/start/restarts unchanged", after == self.before)
            attempt("app identity", unchanged)
        self.out["cleanup_errors"] = errors
        self.out["cleanup"] = {"restored": not errors, "created": self.created,
            "deletion": "API soft-deletes fixtures; task usage/dedup/error rows removed; audit tombstones retained",
            "shared_fixture_mutations": False, "temporary_rules_created": False}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--execute", action="store_true")
    p.add_argument("--dev-sha", help="Full development commit explicitly released by PM")
    p.add_argument("--expected-image-id")
    p.add_argument("--expected-binary-sha256")
    p.add_argument("--base-url", default="http://127.0.0.1:18210")
    p.add_argument("--project", default="xy2api-staging")
    p.add_argument("--database", default="xy2api")
    p.add_argument("--db-user", default="xy2api")
    p.add_argument("--staging-dir", type=Path, default=Path.cwd())
    p.add_argument("--evidence", type=Path)
    args = p.parse_args()
    plan = {"mode": "plan-only", "kind": "development-targeted-validation", "dev_sha": args.dev_sha,
        "cases": CASES, "new_fixtures": "exclusive A/B, two F00 accounts, one user, K1/K2, one channel without price overrides",
        "rates": {"A": 1, "B": 2, "user_A": 0.8}, "models": {"A": [MODEL_A], "B": [MODEL_A, MODEL_B]},
        "temporary_setting": {"fallback_group.return_stable_seconds": STABLE_SECONDS},
        "estimated_minutes": "2–4 excluding development image build/switch",
        "execute_requires": ["PM-released full SHA", "expected image/binary SHA", "healthy matching main stack",
                             "ADMIN_EMAIL/ADMIN_PASSWORD in environment", "existing accepted compliance",
                             "available_channels_enabled already true (otherwise stop, never toggle it)",
                             "new evidence directory", "exclusive .phase2.lock"],
        "cleanup": "restore timer, API-delete own fixtures, delete own usage/dedup/errors; retain audit tombstones",
        "coverage_limit": "HTTP/DB validation; visual component behavior is covered by FE tests",
        "network_or_server_access_in_plan": False}
    if not args.execute:
        print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0
    if not (re.fullmatch(r"[0-9a-f]{40}", args.dev_sha or "") and args.evidence
            and re.fullmatch(r"sha256:[0-9a-f]{64}", args.expected_image_id or "")
            and re.fullmatch(r"[0-9a-f]{64}", args.expected_binary_sha256 or "")):
        p.error("execution requires --dev-sha, --expected-image-id, --expected-binary-sha256 and --evidence")
    url = urllib.parse.urlsplit(args.base_url)
    if url.scheme != "http" or url.hostname not in ("127.0.0.1", "localhost", "::1") or url.username or url.path or url.query:
        p.error("only a loopback staging URL is permitted")
    if not all(re.fullmatch(r"[a-zA-Z0-9_-]+", s) for s in (args.project, args.database, args.db_user)):
        p.error("invalid staging project/database identifier")
    if not os.environ.get("ADMIN_EMAIL") or not os.environ.get("ADMIN_PASSWORD"):
        p.error("load only the staging administrator environment before execution")
    args.staging_dir = args.staging_dir.resolve()
    args.evidence = args.evidence.resolve()
    if not (args.staging_dir / "compose.yaml").is_file():
        p.error("staging compose.yaml is required")
    os.umask(0o077)
    with (args.staging_dir / ".phase2.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        args.evidence.mkdir(parents=True, exist_ok=False, mode=0o700)
        run = Run(args)
        run.evidence("PLAN", plan)

        def interrupted(signum, _frame):
            raise RuntimeError(f"interrupted by signal {signum}")

        for sig in (signal.SIGINT, signal.SIGTERM):
            signal.signal(sig, interrupted)
        try:
            run.setup()
            run.validate()
        except Exception as error:
            run.out["exception"] = type(error).__name__ + ": " + str(error)
        finally:
            # Allow the restoration path to finish after the first interrupt.
            for sig in (signal.SIGINT, signal.SIGTERM):
                signal.signal(sig, signal.SIG_IGN)
            run.cleanup()
            run.out["finished_at"] = utc()
            run.out["ok"] = not run.out.get("exception") and not run.out["cleanup_errors"] and all(
                c["ok"] for c in run.out["checks"])
            run.evidence("REPORT", run.out)
        print(json.dumps({"ok": run.out["ok"], "checks": len(run.out["checks"]),
                          "evidence": str(args.evidence), "cleanup_errors": run.out["cleanup_errors"]}))
        return 0 if run.out["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
