#!/usr/bin/env python3
"""Exercise admin settings, wire dispatch, and settlement on isolated services."""

import argparse
import contextlib
import datetime
import http.server
import json
import math
import os
import pathlib
import secrets
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

MODEL = "gpt-6.2-astra"
EMAIL = "openai-tier-smoke@example.test"


def wait_for(check, timeout=45):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
        except (OSError, subprocess.CalledProcessError):
            result = None
        if result:
            return result
        time.sleep(0.2)
    raise RuntimeError("Timed out waiting for the local smoke-test service")


@contextlib.contextmanager
def container(image, port, *options):
    container_id = subprocess.check_output(
        ["docker", "run", "--rm", "-d", "-p", f"127.0.0.1::{port}", *options, image],
        text=True,
    ).strip()
    try:
        address = subprocess.check_output(["docker", "port", container_id, str(port)], text=True)
        yield container_id, int(address.strip().rsplit(":", 1)[1])
    finally:
        subprocess.run(["docker", "stop", container_id], stdout=subprocess.DEVNULL, check=False)


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        probe = body.get("tool_choice") == "required"
        if not probe:
            self.server.received.append((self.path, body))
        output = [{"type": "message", "role": "assistant",
                   "content": [{"type": "output_text", "text": "smoke response"}]}]
        if probe:
            output = [{"type": "function_call", "call_id": "call_smoke",
                       "name": "gateway_probe", "arguments": "{}"}]
        response = {
            "id": "resp_smoke_" + secrets.token_hex(8),
            "object": "response",
            "status": "completed",
            "model": body.get("model", MODEL),
            "service_tier": body.get("service_tier"),
            "output": output,
            "usage": {"input_tokens": 100, "output_tokens": 50, "total_tokens": 150},
        }
        payload = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def run_checks(address, upstream, password, postgres_id):
    def call(method, path, body=None, token=None, expected=200):
        headers = {"Content-Type": "application/json", "Idempotency-Key": secrets.token_hex(16)}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        request = urllib.request.Request(
            address + path, None if body is None else json.dumps(body).encode(),
            headers, method=method,
        )
        try:
            result = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            result = error
        with result:
            content = result.read()
            if result.status != expected:
                if path == "/api/v1/auth/login":
                    raise RuntimeError(f"Login returned HTTP {result.status}")
                raise RuntimeError(f"{method} {path} returned HTTP {result.status}: {content[:400]!r}")
            data = json.loads(content)
            return data.get("data", data)

    jwt = call("POST", "/api/v1/auth/login", {"email": EMAIL, "password": password})
    user_id, access_token = jwt["user"]["id"], jwt["access_token"]

    def admin(method, path, body=None, expected=200):
        return call(method, "/api/v1/admin" + path, body, access_token, expected)

    compliance = admin("GET", "/compliance")
    admin("POST", "/compliance/accept", {"phrase": compliance["ack_phrase_en"], "language": "en"})
    admin("POST", f"/users/{user_id}/balance", {"operation": "set", "balance": 10})
    group = admin("POST", "/groups", {"name": "tier-smoke", "platform": "openai", "rate_multiplier": 0.5})
    group_path = f"/groups/{group['id']}"
    revision = admin("GET", group_path)["revision"]
    policy = {
        "revision": revision, "openai_tier_policy_version": 2,
        "openai_fast_mode": "force_off", "openai_ultrafast_mode": "force_on",
        "free_openai_fast": True, "free_openai_ultrafast": True,
    }
    updated = admin("PUT", group_path, policy)
    assert updated["free_openai_fast"] and updated["free_openai_ultrafast"], updated
    assert updated["openai_tier_policy_version"] == 2
    admin("PUT", group_path, policy, expected=409)

    card = {"platform": "openai", "models": [MODEL], "billing_mode": "token",
            "input_price": 0.001, "output_price": 0.002, "fast_multiplier": 2.5}
    channel = admin("POST", "/channels", {"name": "tier-smoke", "group_ids": [group["id"]],
                                          "model_pricing": [card]})
    channel_path = f"/channels/{channel['id']}"
    channel = admin("GET", channel_path)
    card = {**channel["model_pricing"][0], "ultrafast_multiplier": 4.5}
    channel = admin("PUT", channel_path, {"revision": channel["revision"], "model_pricing": [card]})
    assert channel["model_pricing"][0]["fast_multiplier"] == 2.5
    assert channel["model_pricing"][0]["ultrafast_multiplier"] == 4.5
    invalid_card = {**card, "fast_multiplier": 2.1234567}
    admin("PUT", channel_path, {"revision": channel["revision"], "model_pricing": [invalid_card]}, expected=400)
    assert admin("GET", channel_path)["model_pricing"][0]["fast_multiplier"] == 2.5

    supported_tiers = [{"id": "default"}, {"id": "priority"}, {"id": "ultrafast"}]
    synced_at = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
    admin("POST", "/accounts", {
        "name": "tier-smoke-upstream", "platform": "openai", "type": "apikey", "concurrency": 3, "priority": 1,
        "group_ids": [group["id"]], "upstream_billing_probe_enabled": False,
        "credentials": {"api_key": "stub-key", "base_url": f"http://127.0.0.1:{upstream.server_port}/v1"},
        "extra": {"openai_responses_supported": True, "upstream_model_metadata": {
            "source": "smoke_fixture", "synced_at": synced_at, "models": {MODEL: {
                "id": MODEL, "service_tiers_synced_at": synced_at,
                "codex_tool_capabilities": {"service_tiers": supported_tiers},
            }},
        }},
    })
    key = call("POST", "/api/v1/keys", {"name": "tier-smoke", "group_id": group["id"]}, access_token)
    call("POST", "/v1/responses", {"model": MODEL, "input": "ping", "service_tier": "priority"}, key["key"])
    assert len(upstream.received) == 1, upstream.received
    path, body = upstream.received[0]
    assert path == "/v1/responses" and body["service_tier"] == "ultrafast", (path, body)

    def usage_row():
        logs = admin("GET", f"/usage?api_key_id={key['id']}")
        return next((item for item in logs["items"] if item.get("service_tier_details")), None)

    usage = wait_for(usage_row, timeout=15)
    assert usage["service_tier"] == "ultrafast", usage
    assert math.isclose(usage["total_cost"], 0.9), usage
    assert math.isclose(usage["actual_cost"], 0.1), usage
    detail = usage["service_tier_details"]
    assert detail["outbound_tier"] == detail["settled_tier"] == "ultrafast", detail
    assert detail["free_tier"] == "ultrafast", detail
    assert detail["tier_multiplier"] == 4.5, detail
    settled = subprocess.check_output([
        "docker", "exec", postgres_id, "psql", "-U", "postgres", "-d", "smoke", "-Atc",
        "SELECT i.state FROM usage_billing_intents i JOIN usage_logs u USING (request_id, api_key_id) "
        f"WHERE u.id={int(usage['id'])}",
    ], text=True).strip()
    assert settled == "settled", settled
    print("Admin revision, Fast/Ultrafast pricing, outbound Ultrafast, and settled 0.90 / 0.10 passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("server", type=lambda arg: pathlib.Path(arg).resolve(strict=True))
    args = parser.parse_args()
    password = secrets.token_hex(16)
    with (
        tempfile.TemporaryDirectory(prefix="xy2api-tier-smoke-") as workdir,
        container("postgres:18-alpine", 5432, "-e", f"POSTGRES_PASSWORD={password}", "-e",
                  "POSTGRES_DB=smoke") as (postgres_id, postgres_port),
        container("redis:8.4-alpine", 6379) as (_, redis_port),
        http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream) as upstream,
    ):
        upstream.received = []
        threading.Thread(target=upstream.serve_forever, daemon=True).start()

        def postgres_ready():
            return subprocess.run(["docker", "exec", postgres_id, "pg_isready", "-h", "127.0.0.1"],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0

        wait_for(postgres_ready)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            server_port = listener.getsockname()[1]
        address = f"http://127.0.0.1:{server_port}"
        env = {**os.environ,
               "DATA_DIR": workdir, "AUTO_SETUP": "true", "RUN_MODE": "standard",
               "ADMIN_EMAIL": EMAIL, "ADMIN_PASSWORD": password, "JWT_SECRET": secrets.token_hex(32),
               "SERVER_HOST": "127.0.0.1", "SERVER_PORT": str(server_port),
               "DATABASE_HOST": "127.0.0.1", "DATABASE_PORT": str(postgres_port),
               "DATABASE_USER": "postgres", "DATABASE_PASSWORD": password, "DATABASE_DBNAME": "smoke",
               "DATABASE_SSLMODE": "disable", "TZ": "UTC",
               "REDIS_HOST": "127.0.0.1", "REDIS_PORT": str(redis_port), "REDIS_DB": "0",
               "REDIS_USERNAME": "", "REDIS_PASSWORD": "", "REDIS_ENABLE_TLS": "false",
               "SECURITY_URL_ALLOWLIST_ENABLED": "false",
               "SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP": "true",
               "SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS": "true"}
        log_path = pathlib.Path(workdir) / "server.log"
        with log_path.open("w") as log:
            server = subprocess.Popen([args.server], cwd=workdir, env=env, stdout=log, stderr=log)
            try:
                wait_for(lambda: urllib.request.urlopen(address + "/ready", timeout=1).status == 200)
                run_checks(address, upstream, password, postgres_id)
            except Exception:
                print(log_path.read_text(errors="replace").replace(password, "[redacted]")[-9000:])
                raise
            finally:
                server.terminate()
                try:
                    server.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait()
                upstream.shutdown()


if __name__ == "__main__":
    main()
