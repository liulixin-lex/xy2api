# Gateway resilience test and measurement tools

Tools for the 0.4.0 gateway resilience work (`openspec/changes/gateway-resilience-20261015/`, tasks §0 and staging-validation). None of them needs credentials in files: database access goes through the caller's `psql` environment, and every script prints aggregates only.

| Tool | Purpose |
|---|---|
| `baseline.sql` | Read-only design §14 metrics from PostgreSQL: errors by phase, no-account 503 and the longest no-account streak per group, balance-disabled accounts, "insufficient balance" shown to users with a positive balance, consecutive failures of one key on one account, cache read share and TTFT; on 0.4.0 also undelivered requests, fallback usage and balance states. |
| `first_token_calibration.sql` | First token latency per model and reasoning effort over N days (P50–P99.9) and the share above 60/120/180 seconds, to calibrate `budget.first_output_timeout_seconds`. |
| `metrics.sql` | The `baseline.sql` numbers as JSON lines, for `metrics_report.py`. Sets the session read-only itself. |
| `caddy_cuts.py` | CDN cut rate per entry (Cloudflare, ESA, direct) from Caddy JSON access logs; with `--app`, matches each cut to the application log to find requests completed (and billed) after the client was cut; share of 503 responses with `Retry-After`. `--json` prints one object. |
| `session_streaks.py` | From application logs (JSON or console format): consecutive failures of one session on one account (requests without a session hash are counted per API key, separately), and the latency of requests that switched accounts and still succeeded. `--json` prints one object. |
| `metrics_report.py` | design §14 report: `collect` measures one environment into a JSON file, `report` compares a baseline with a candidate against the §14 targets (Markdown; exit 1 when a measured metric misses). |
| `fault-upstream/` | Fault-injecting upstream (Go, standard library only) with OpenAI Responses/Chat, Anthropic, Gemini, image, `/v1/usage` and new-api billing shapes. Scenarios F00–F24, `BAL` and `MIX`. |
| `cdn-sim/` | CDN edge simulator (Go, standard library only): first-byte and idle read timeouts, `X-XY-Edge` injection, cut and request logs. |

## Database scripts

```bash
PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=120s' \
  psql -X -v ON_ERROR_STOP=1 -v hours=48 -f baseline.sql
PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=300s' \
  psql -X -v ON_ERROR_STOP=1 -v days=7 -f first_token_calibration.sql
```

## Log scripts

```bash
python3 caddy_cuts.py --app app.log access.log access-*.log.gz
python3 session_streaks.py app.log
```

## Metrics report (design §14)

`caddy_cuts.py` and `session_streaks.py` use the standard library only and run as single files, so on a host that only allows reading they can be fed through SSH without copying anything:

```bash
ssh host "sudo bash -c 'python3 - --json --app <(docker logs --since 5h APP 2>&1) /var/log/caddy/SITE-access*.log*'" < caddy_cuts.py > caddy.json
ssh host "sudo bash -c 'python3 - --json <(docker logs --since 48h APP 2>&1)'" < session_streaks.py > app.json
ssh host "docker exec -i -u postgres -e PGOPTIONS='-c default_transaction_read_only=on' DB psql -X -At -U postgres -d DBNAME -v hours=48" < metrics.sql > db.jsonl

python3 metrics_report.py collect --label prod-0.3.0 --hours 48 \
  --section db=db.jsonl --section caddy=caddy.json --section app=app.json \
  --set codex_failed_turn_requests=30 --out baseline.json
python3 metrics_report.py collect --label staging --hours 24 --psql 'psql' \
  --caddy access.log --app app.log --clients evidence/clients3 --out candidate.json
python3 metrics_report.py report baseline.json candidate.json > REPORT.md
```

- Counts are scaled to 48 hours (the SQL window, or the Caddy log span for log counts).
- Staging traffic is synthetic: cache read share, first token latency and no-account 503 there come from fault injection and are not comparable with production; read them with the scenario results. After a release, compare production 24 h and 72 h windows with the same baseline.
- Keep the JSON files and reports out of the repository (they describe a deployment).

## fault-upstream

```bash
cd fault-upstream && go build -o fault-upstream . && LISTEN=:8080 ./fault-upstream
```

Select a scenario with the `X-Fault` header or with a path prefix, which lets an account `base_url` pin it:

```text
X-Fault: F02;after=5
http://fault-upstream:8080/f/F06;secs=200      (account base_url)
```

| ID | Behaviour | Parameters |
|---|---|---|
| F00 / F17 / F22 | Normal response (Responses, Chat, Anthropic, Gemini by path) | `first` seconds before the first token |
| F01 | Slow response headers | `secs` |
| F02–F05 | 200 SSE, then a failure frame: busy, rate_limit_exceeded, insufficient_quota, context_length_exceeded | `after` |
| F06 | Stall after some output, then drop the connection | `secs` |
| F07 / F08 | Cloudflare 502 (`Retry-After: 60`) / 524 (`Retry-After: 120`) pages | |
| F09 | 503 without body | |
| F10 / F11 / F12 | 402 / 403 / 429 insufficient balance | |
| F13 / F14 | 429 rate limit with / without reset time | `reset` |
| F15 | 401 invalid key | |
| F16 | 529 overloaded (`server_is_overloaded`) | `status` |
| F18 | Image generation | `secs` |
| F19 / F20 | `/v1/usage` and new-api billing endpoints | `key` |
| F21 | Anthropic `message_start`, then stall | `secs` |
| F23 | Slow stream: a delta every `every` seconds for `secs` seconds, then completion with usage | `secs`, `every` |
| F24 | Relay keep-alive: empty `SSE-Keep-Alive` deltas (the first before `response.created`), then `response.failed` busy (Responses streams) | `secs`, `every` |
| F25 | Pooled relay: a Responses request with `previous_response_id` gets a vague 400 (`Upstream request failed`), others answer normally | |
| BAL | Normal while the balance of `key` is positive, F12 otherwise | `key`, `first` |
| MIX | A random fault with probability `p` percent per fault type, otherwise normal | `p` |

Any scenario accepts `delay` (seconds) to hold the answer back, for example `F16;delay=20` to return 529 after the gateway's pre-output heartbeats.

Control endpoints: `GET /_scenarios`, `GET /_requests?since=N` (outcome per request: `completed`, `cancelled`, `dropped`, `failed_in_stream`, `error_status`), `GET|POST /_control/balance?key=K&value=V`, `GET /healthz`.

## cdn-sim

```bash
cd cdn-sim && go build -o cdn-sim . && \
  LISTEN=:8080 TARGET=http://app:8080 FIRST_BYTE_TIMEOUT=125s IDLE_TIMEOUT=125s EDGE=cf ./cdn-sim
```

- No response headers within `FIRST_BYTE_TIMEOUT`: the client gets 524 and the origin request is cancelled.
- No bytes for `IDLE_TIMEOUT` while streaming: the client connection is dropped and the origin request is cancelled.
- `EDGE=none` forwards without a marker (useful as a request counter on the direct path).
- `GET /_cuts` lists cuts; `GET /_requests?since=N` lists every proxied request.

The application only honours `X-XY-Edge` from peers in `SERVER_TRUSTED_PROXIES`, so the simulator's network must be listed there.
