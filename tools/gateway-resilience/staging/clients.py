#!/usr/bin/env python3
"""Real-client end-to-end runs on the staging stack (staging-validation §6.2).

Each case drives one real client (Codex CLI, Claude Code, OpenCode, the
OpenAI / Anthropic / google-genai SDKs, curl) against one fixture group and
records: client version, exit code, wall time, the client requests counted
by the counting proxy in front of the gateway (direct-counter or cdn-sim),
their statuses, and the last lines the client printed (keys masked).

Clients run with an isolated HOME under clients/home, never the host's own
configuration. Usage (on the staging host):
    python3 clients.py [--only CASE,CASE] [--evidence DIR]
"""

from __future__ import annotations

import argparse
import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
CLIENTS = os.path.join(HERE, "clients")
HOME = os.path.join(CLIENTS, "home")
BIN = os.path.join(CLIENTS, "node_modules", ".bin")
PY = os.path.join(CLIENTS, "py")
KEYS = json.load(open(os.path.join(HERE, "fixtures", "keys.json")))["keys"]
DIRECT, CDN125, CDN90 = 18215, 18213, 18212
MODEL = "gpt-6.1-sol"
PROMPT = "Reply with exactly one word: pong"
SECRET = re.compile(r"sk-[A-Za-z0-9_-]{8,}")


def mask(text: str) -> str:
    return SECRET.sub("sk-***", text)


def proxy_mark(port: int) -> int:
    rows = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/_requests?since=0", timeout=10))
    return rows[-1]["id"] if rows else 0


def proxy_since(port: int, mark: int) -> list[dict]:
    rows = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/_requests?since={mark}", timeout=10))
    return [r for r in rows if not r["path"].startswith("/_")]


def env_for(key: str, port: int) -> dict:
    env = {k: v for k, v in os.environ.items() if not k.startswith(("OPENAI", "ANTHROPIC", "CODEX", "CLAUDE", "REAL"))}
    env.update({
        "HOME": HOME,
        "CODEX_HOME": os.path.join(HOME, ".codex"),
        "XDG_CONFIG_HOME": os.path.join(HOME, ".config"),
        "XDG_DATA_HOME": os.path.join(HOME, ".local", "share"),
        "XDG_CACHE_HOME": os.path.join(HOME, ".cache"),
        "PATH": f"{BIN}:{os.environ.get('PATH', '')}",
        "PYTHONPATH": PY,
        "STG_KEY": key,
        "STG_BASE": f"http://127.0.0.1:{port}",
        "NO_COLOR": "1",
        "CI": "1",
    })
    return env


def write_client_config(port: int):
    os.makedirs(os.path.join(HOME, ".codex"), exist_ok=True)
    with open(os.path.join(HOME, ".codex", "config.toml"), "w") as fh:
        fh.write(f'''model = "{MODEL}"
model_provider = "stg"
model_reasoning_effort = "low"
[model_providers.stg]
name = "xy2api staging"
base_url = "http://127.0.0.1:{port}/v1"
env_key = "STG_KEY"
wire_api = "responses"
''')
    os.makedirs(os.path.join(HOME, ".config", "opencode"), exist_ok=True)
    with open(os.path.join(HOME, ".config", "opencode", "opencode.json"), "w") as fh:
        json.dump({
            "$schema": "https://opencode.ai/config.json",
            "provider": {"stg": {"npm": "@ai-sdk/openai-compatible", "name": "xy2api staging",
                                 "options": {"baseURL": f"http://127.0.0.1:{port}/v1", "apiKey": "{env:STG_KEY}"},
                                 "models": {MODEL: {}}}},
            "model": f"stg/{MODEL}",
        }, fh)


PY_OPENAI = f'''
import os, sys
from openai import OpenAI
c = OpenAI(base_url=os.environ["STG_BASE"] + "/v1", api_key=os.environ["STG_KEY"], timeout=600)
text = ""
try:
    with c.responses.stream(model="{MODEL}", input="{PROMPT}", reasoning={{"effort": "low"}}) as s:
        for e in s:
            if e.type == "response.output_text.delta":
                text += e.delta
    print("OUTPUT:", text.strip())
except Exception as e:
    print("ERROR:", type(e).__name__, getattr(e, "status_code", ""), str(e)[:300])
    sys.exit(1)
'''

PY_OPENAI_CHAT = f'''
import os, sys
from openai import OpenAI
c = OpenAI(base_url=os.environ["STG_BASE"] + "/v1", api_key=os.environ["STG_KEY"], timeout=600)
try:
    text = ""
    for ch in c.chat.completions.create(model="{MODEL}", messages=[{{"role": "user", "content": "{PROMPT}"}}], stream=True):
        if ch.choices and ch.choices[0].delta.content:
            text += ch.choices[0].delta.content
    print("OUTPUT:", text.strip())
except Exception as e:
    print("ERROR:", type(e).__name__, getattr(e, "status_code", ""), str(e)[:300])
    sys.exit(1)
'''

PY_ANTHROPIC = f'''
import os, sys
import anthropic
c = anthropic.Anthropic(base_url=os.environ["STG_BASE"], api_key=os.environ["STG_KEY"], timeout=600)
try:
    with c.messages.stream(model="claude-sonnet-5-5", max_tokens=64, messages=[{{"role": "user", "content": "{PROMPT}"}}]) as s:
        text = "".join(s.text_stream)
    print("OUTPUT:", text.strip())
except Exception as e:
    print("ERROR:", type(e).__name__, getattr(e, "status_code", ""), str(e)[:300])
    sys.exit(1)
'''

PY_GENAI = '''
import os, sys
from google import genai
from google.genai import types
c = genai.Client(api_key=os.environ["STG_KEY"], http_options=types.HttpOptions(base_url=os.environ["STG_BASE"], api_version="v1beta"))
try:
    text = "".join((ch.text or "") for ch in c.models.generate_content_stream(model="gemini-3.8-flash", contents="hello"))
    print("OUTPUT:", text.strip()[:80])
except Exception as e:
    print("ERROR:", type(e).__name__, str(e)[:300])
    sys.exit(1)
'''


def command(client: str) -> list[str]:
    if client == "codex":
        return [os.path.join(BIN, "codex"), "exec", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", PROMPT]
    if client == "claude":
        return [os.path.join(BIN, "claude"), "-p", PROMPT, "--model", "sonnet", "--output-format", "text"]
    if client == "opencode":
        return [os.path.join(BIN, "opencode"), "run", PROMPT]
    if client == "curl":
        return ["sh", "-c", 'curl -sS -N -m 600 "$STG_BASE/v1/responses" -H "Authorization: Bearer $STG_KEY" '
                '-H "Content-Type: application/json" -d \'{"model":"%s","input":"%s","stream":true}\' -w "\\nHTTP %%{http_code}\\n"' % (MODEL, PROMPT)]
    script = {"openai-py": PY_OPENAI, "openai-chat-py": PY_OPENAI_CHAT, "anthropic-py": PY_ANTHROPIC, "genai-py": PY_GENAI}[client]
    return [sys.executable, "-c", script]


def version(client: str) -> str:
    try:
        if client in ("codex", "claude", "opencode"):
            return subprocess.run([os.path.join(BIN, client), "--version"], capture_output=True, text=True, timeout=60,
                                  env=env_for("x", DIRECT)).stdout.strip().splitlines()[0]
        if client.endswith("-py"):
            mod = {"openai-py": "openai", "openai-chat-py": "openai", "anthropic-py": "anthropic", "genai-py": "google.genai"}[client]
            return subprocess.run([sys.executable, "-c", f"import {mod} as m; print('{mod}', getattr(m, '__version__', '?'))"],
                                  capture_output=True, text=True, env={"PYTHONPATH": PY}, timeout=60).stdout.strip()
        return subprocess.run(["curl", "--version"], capture_output=True, text=True).stdout.splitlines()[0]
    except Exception as exc:  # noqa: BLE001
        return f"unknown ({exc})"


# case id -> (client, group key, port, expectation, max client requests)
CASES = {
    "C-codex-ok": ("codex", "R-ok", DIRECT, "success", 1),
    "C-codex-fail-first": ("codex", "R-fail-first", DIRECT, "success", 1),
    "C-codex-slow-first-cdn": ("codex", "R-slow-first", CDN125, "success", 1),
    "C-codex-all-fail": ("codex", "R-all-fail", DIRECT, "failure", 5),
    "C-codex-busy-stream": ("codex", "R-busy-stream", DIRECT, "failure", 5),
    "C-claude-ok": ("claude", "R-claude", DIRECT, "success", 3),
    "C-opencode-ok": ("opencode", "R-ok", DIRECT, "success", 3),
    "C-openai-py-ok": ("openai-py", "R-ok", DIRECT, "success", 1),
    "C-openai-py-fail-first": ("openai-py", "R-fail-first", DIRECT, "success", 1),
    "C-openai-py-slow-first-cdn": ("openai-py", "R-slow-first", CDN125, "success", 1),
    "C-openai-py-all-fail": ("openai-py", "R-all-fail", DIRECT, "failure", 3),
    "C-openai-chat-py-cdn90": ("openai-chat-py", "R-ok", CDN90, "success", 1),
    "C-anthropic-py-ok": ("anthropic-py", "R-claude", DIRECT, "success", 1),
    "C-genai-py-cdn": ("genai-py", "G-gemini", CDN125, "success", 1),
    "C-curl-ok": ("curl", "R-ok", DIRECT, "success", 1),
}


def run_case(cid: str, outdir: str) -> dict:
    client, group, port, expect, max_requests = CASES[cid]
    write_client_config(port)
    mark = proxy_mark(port)
    started = time.time()
    try:
        proc = subprocess.run(command(client), capture_output=True, text=True, timeout=900,
                              env=env_for(KEYS[group], port), cwd=CLIENTS)
        code, out, err = proc.returncode, proc.stdout, proc.stderr
    except subprocess.TimeoutExpired as exc:
        code, out, err = -1, (exc.stdout or b"").decode() if isinstance(exc.stdout, bytes) else (exc.stdout or ""), "timeout"
    elapsed = round(time.time() - started, 1)
    time.sleep(1)
    reqs = proxy_since(port, mark)
    model_reqs = [r for r in reqs if r["path"].endswith(("/responses", "/chat/completions", "/messages")) or ":stream" in r["path"]]
    text = mask((out or "") + "\n" + (err or ""))
    succeeded = code == 0 and ("pong" in text.lower() or "OUTPUT:" in text)
    record = {
        "id": cid, "client": client, "version": version(client), "group": group, "port": port,
        "exit_code": code, "elapsed_s": elapsed, "client_requests": len(model_reqs),
        "statuses": [r["status"] for r in model_reqs], "paths": sorted({r["path"] for r in reqs}),
        "output_tail": text.strip().splitlines()[-12:],
    }
    checks = []
    if expect == "success":
        checks.append({"check": "client got an answer", "ok": succeeded})
    else:
        checks.append({"check": "client failed cleanly", "ok": code != 0 or "error" in text.lower()})
    checks.append({"check": f"client requests <= {max_requests}", "ok": len(model_reqs) <= max_requests})
    record["checks"] = checks
    record["ok"] = all(c["ok"] for c in checks)
    with open(os.path.join(outdir, f"{cid}.json"), "w") as fh:
        json.dump(record, fh, indent=1, ensure_ascii=False)
    print(f"{'PASS' if record['ok'] else 'FAIL'} {cid} {client} {elapsed}s requests={len(model_reqs)} statuses={record['statuses']}", flush=True)
    for c in checks:
        if not c["ok"]:
            print(f"    - failed: {c['check']}", flush=True)
    return record


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--only", default="")
    parser.add_argument("--evidence", default=os.path.join(HERE, "evidence", "clients-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%MZ")))
    args = parser.parse_args()
    os.makedirs(args.evidence, exist_ok=True)
    os.makedirs(HOME, exist_ok=True)
    ids = [c for c in (args.only.split(",") if args.only else CASES) if c]
    records = [run_case(c, args.evidence) for c in ids]
    summary = {"at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "passed": sum(r["ok"] for r in records),
               "total": len(records), "failed": [r["id"] for r in records if not r["ok"]]}
    with open(os.path.join(args.evidence, "SUMMARY.json"), "w") as fh:
        json.dump(summary, fh, indent=1)
    print(json.dumps(summary))
    shutil.rmtree(os.path.join(HOME, ".codex", "sessions"), ignore_errors=True)
    return 0 if not summary["failed"] else 1


if __name__ == "__main__":
    sys.exit(main())
