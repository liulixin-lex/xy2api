#!/usr/bin/env python3
"""Creates the staging fixtures through the admin API (staging-validation §4).

Idempotent: groups, accounts and users are looked up by name/email first.
Upstream accounts point at fault-upstream with the scenario pinned in the
base URL path, so every group behaves deterministically. Generated API keys
are written to fixtures/keys.json (mode 0600) and never printed.

Usage (on the staging host):
    set -a; . ./.env; [ -f .env.real ] && . ./.env.real; set +a
    [ACCEPT_ADMIN_COMPLIANCE=1] python3 fixtures.py http://127.0.0.1:18210

ACCEPT_ADMIN_COMPLIANCE=1 accepts the admin compliance commitment on this
instance and must only be set with the operator's approval.
"""

from __future__ import annotations

import json
import os
import secrets
import sys
import urllib.error
import urllib.request

FAULT = "http://fault-upstream:8080/f/"

# group name -> (platform, [(account name, scenario spec, priority)], options)
GROUPS: dict[str, tuple[str, list[tuple[str, str, int]], dict]] = {
    # OpenAI, original scheduler
    "G-single": ("openai", [("single-ok", "BAL;key=single", 1)], {}),
    "G-pair": ("openai", [("pair-a", "BAL;key=pair-a", 1), ("pair-b", "BAL;key=pair-b", 1)], {}),
    "G-fail-then-ok": ("openai", [("fto-bad", "F07", 1), ("fto-ok", "F00", 2)], {}),
    "G-slow-headers": ("openai", [("slow-a", "F01;secs=200", 1), ("slow-ok", "F00", 2)], {}),
    "G-long-first": ("openai", [("long-first", "F17;first=150", 1)], {}),
    "G-slow-150": ("openai", [("slow150", "F01;secs=150", 1)], {}),
    "G-anthropic-slow": ("anthropic", [("anth-slow150", "F01;secs=150", 1)], {}),
    "G-busy-stream": ("openai", [("busy-a", "F02;after=20", 1), ("busy-ok", "F00", 2)], {}),
    "G-ratelimit-stream": ("openai", [("rl-stream", "F03;after=20", 1)], {}),
    "G-ctx-overflow": ("openai", [("ctx", "F05;after=1", 1)], {}),
    "G-stall": ("openai", [("stall", "F06;secs=290", 1)], {}),
    "G-all-502": ("openai", [("cf502-a", "F07", 1), ("cf502-b", "F07", 1), ("cf502-c", "F07", 1), ("cf502-d", "F07", 1), ("cf502-e", "F07", 1)], {}),
    "G-503-single": ("openai", [("s503", "F09", 1)], {}),
    "G-balance-402": ("openai", [("bal402", "F10", 1)], {}),
    "G-balance-429": ("openai", [("bal429", "F12", 1), ("bal429-ok", "F00", 2)], {}),
    "G-rate-reset": ("openai", [("rl-reset", "F13;reset=30", 1)], {}),
    "G-rate-noreset": ("openai", [("rl-noreset", "F14", 1)], {}),
    "G-invalid-key": ("openai", [("bad-key", "F15", 1), ("good-key", "F00", 2)], {}),
    "G-overloaded": ("openai", [("ovl-a", "F16;status=503", 1), ("ovl-ok", "F00", 2)], {}),
    "G-image": ("openai", [("img", "F18;secs=60", 1)], {}),
    "G-mix": ("openai", [("mix-a", "MIX;p=1", 1), ("mix-b", "MIX;p=1", 1), ("mix-c", "MIX;p=1", 1)], {}),
    # availability fallback
    "G-fallback-backup": ("openai", [("fb-backup", "F00", 1)], {}),
    "G-fallback-primary": ("openai", [("fb-primary", "BAL;key=fb-primary", 1)], {"availability_fallback": "G-fallback-backup"}),
    # Anthropic and Gemini shapes
    "G-anthropic": ("anthropic", [("anth-ok", "F00", 1)], {}),
    "G-anthropic-stall": ("anthropic", [("anth-stall", "F21;secs=200", 1), ("anth-ok2", "F00", 2)], {}),
    "G-anthropic-overloaded": ("anthropic", [("anth-529", "F16", 1)], {}),
    "G-claude-only": ("anthropic", [("cc-only", "F00", 1)], {"claude_code_only": True}),
    "G-gemini": ("gemini", [("gem-ok", "F22;first=1", 1)], {}),
}

# Groups with real relay accounts (staging-validation §6.2), created only
# when .env.real provides REAL<n>_BASE_URL / REAL<n>_API_KEY. "REAL<n>" in an
# account spec means that relay; anything else is a fault-upstream scenario.
REAL_MODEL = "gpt-6-astra"
REAL_GROUPS: dict[str, tuple[list[tuple[str, str, int]], dict]] = {
    "R-ok": ([("real-1", "REAL1", 1), ("real-2", "REAL2", 1), ("real-3", "REAL3", 1)], {}),
    "R-fail-first": ([("rf-502", "F07", 1), ("rf-real", "REAL1", 2)], {}),
    "R-slow-first": ([("rs-slow", "F01;secs=600", 1), ("rs-real", "REAL2", 2)], {}),
    "R-all-fail": ([("ra-502", "F07", 1), ("ra-503", "F09", 1)], {}),
    "R-busy-stream": ([("rb-busy", "F02;after=20", 1)], {}),
    "R-claude": ([("rc-real", "REAL1", 1), ("rc-real2", "REAL3", 1)],
                 {"allow_messages_dispatch": True, "default_mapped_model": REAL_MODEL}),
}

USERS = {
    "stg-user@xy2api-staging.local": 1000.0,
    # One in-flight estimate (about 0.08 USD) fits, two do not (RES-1).
    "stg-small@xy2api-staging.local": 0.12,
}


class API:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.token = ""

    def call(self, method: str, path: str, body=None, token: str | None = None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        tok = self.token if token is None else token
        if tok:
            req.add_header("Authorization", "Bearer " + tok)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                payload = json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as err:
            payload = json.loads(err.read() or b"{}")
            raise SystemExit(f"{method} {path}: HTTP {err.code} {payload.get('message') or payload}")
        if payload.get("code", 0) != 0:
            raise SystemExit(f"{method} {path}: {payload.get('message')}")
        return payload.get("data")

    def login(self, email: str, password: str) -> str:
        return self.call("POST", "/api/v1/auth/login", {"email": email, "password": password}, token="")["access_token"]


def items(data):
    if isinstance(data, dict) and "items" in data:
        return data["items"]
    return data or []


def main() -> int:
    base = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18210"
    api = API(base)
    api.token = api.login(os.environ["ADMIN_EMAIL"], os.environ["ADMIN_PASSWORD"])
    # The admin compliance commitment is accepted only when the operator has
    # authorised it for this isolated staging instance.
    status = api.call("GET", "/api/v1/admin/compliance")
    if status.get("required"):
        if os.environ.get("ACCEPT_ADMIN_COMPLIANCE") != "1":
            raise SystemExit("admin compliance not accepted; set ACCEPT_ADMIN_COMPLIANCE=1 only with operator approval")
        api.call("POST", "/api/v1/admin/compliance/accept", {"phrase": status["ack_phrase_zh"], "language": "zh"})

    real = {}
    for n in (1, 2, 3):
        if os.environ.get(f"REAL{n}_BASE_URL") and os.environ.get(f"REAL{n}_API_KEY"):
            real[f"REAL{n}"] = (os.environ[f"REAL{n}_BASE_URL"], os.environ[f"REAL{n}_API_KEY"])
    if len(real) == 3:
        for name, (accounts, opts) in REAL_GROUPS.items():
            GROUPS[name] = ("openai", accounts, opts)

    groups = {g["name"]: g for g in items(api.call("GET", "/api/v1/admin/groups/all"))}
    for name, (platform, _, opts) in GROUPS.items():
        if name not in groups:
            body = {"name": name, "platform": platform, "rate_multiplier": 1, "subscription_type": "standard",
                    "claude_code_only": bool(opts.get("claude_code_only"))}
            if opts.get("allow_messages_dispatch"):
                body["allow_messages_dispatch"] = True
                body["default_mapped_model"] = opts["default_mapped_model"]
            groups[name] = api.call("POST", "/api/v1/admin/groups", body)
    for name, (_, _, opts) in GROUPS.items():
        target = opts.get("availability_fallback")
        if target and groups[name].get("availability_fallback_group_id") != groups[target]["id"]:
            groups[name] = api.call("PUT", f"/api/v1/admin/groups/{groups[name]['id']}",
                                    {"availability_fallback_group_id": groups[target]["id"]})

    existing = {a["name"]: a for a in items(api.call("GET", "/api/v1/admin/accounts?page=1&page_size=500"))}
    for gname, (platform, accounts, _) in GROUPS.items():
        for aname, spec, priority in accounts:
            if aname in existing:
                continue
            if spec in real:
                credentials = {"api_key": real[spec][1], "base_url": real[spec][0]}
            else:
                credentials = {"api_key": "sk-fault-" + aname, "base_url": FAULT + spec}
            body = {"name": aname, "platform": platform, "type": "apikey",
                    "credentials": credentials,
                    "group_ids": [groups[gname]["id"]], "concurrency": 20, "priority": priority}
            existing[aname] = api.call("POST", "/api/v1/admin/accounts", body)

    users = {u["email"]: u for u in items(api.call("GET", "/api/v1/admin/users?page=1&page_size=200"))}
    keys_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "keys.json")
    keys = json.load(open(keys_path)) if os.path.exists(keys_path) else {"users": {}, "keys": {}}
    for email, balance in USERS.items():
        password = keys["users"].get(email) or secrets.token_hex(12)
        keys["users"][email] = password
        if email not in users:
            users[email] = api.call("POST", "/api/v1/admin/users",
                                    {"email": email, "password": password, "balance": balance, "concurrency": 100})
        else:
            api.call("POST", f"/api/v1/admin/users/{users[email]['id']}/balance",
                     {"balance": balance, "operation": "set", "notes": "staging fixtures"})
    owner = "stg-user@xy2api-staging.local"
    user_token = api.login(owner, keys["users"][owner])
    have = {k["name"]: k for k in items(api.call("GET", "/api/v1/keys?page=1&page_size=200", token=user_token))}
    for gname in GROUPS:
        kname = "key-" + gname
        if kname not in have:
            have[kname] = api.call("POST", "/api/v1/keys", {"name": kname, "group_id": groups[gname]["id"]}, token=user_token)
        keys["keys"][gname] = have[kname]["key"]
    small = "stg-small@xy2api-staging.local"
    small_token = api.login(small, keys["users"][small])
    have_small = {k["name"]: k for k in items(api.call("GET", "/api/v1/keys?page=1&page_size=200", token=small_token))}
    if "key-small" not in have_small:
        have_small["key-small"] = api.call("POST", "/api/v1/keys", {"name": "key-small", "group_id": groups["G-single"]["id"]}, token=small_token)
    keys["keys"]["small:G-single"] = have_small["key-small"]["key"]
    if "key-small-long" not in have_small:
        have_small["key-small-long"] = api.call("POST", "/api/v1/keys", {"name": "key-small-long", "group_id": groups["G-long-first"]["id"]}, token=small_token)
    keys["keys"]["small:G-long-first"] = have_small["key-small-long"]["key"]

    os.makedirs(os.path.dirname(keys_path), exist_ok=True)
    fd = os.open(keys_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        json.dump(keys, fh, indent=1)
    summary = {g: {"id": groups[g]["id"], "accounts": [a for a, _, _ in GROUPS[g][1]]} for g in GROUPS}
    print(json.dumps(summary, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
