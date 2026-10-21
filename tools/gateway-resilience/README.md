# Gateway resilience test and measurement tools

Tools for the 0.4.0 gateway resilience work (`openspec/changes/gateway-resilience-20261015/`, tasks §0 and staging-validation). None of them needs credentials in files: database access goes through the caller's `psql` environment, and every script prints aggregates only.

| Tool | Purpose |
|---|---|
| `baseline.sql` | Read-only design §14 metrics from PostgreSQL: errors by phase, no-account 503 and the longest no-account streak per group, balance-disabled accounts, "insufficient balance" shown to users with a positive balance, consecutive failures of one key on one account, cache read share and TTFT; on 0.4.0 also undelivered requests, fallback usage and accounts in a balance pause. |
| `first_token_calibration.sql` | First token latency per model and reasoning effort over N days (P50–P99.9) and the share above the current tiers (180/200/220 seconds) and the 240-second budget, to calibrate `budget.first_output_timeout_seconds`. |
| `metrics.sql` | The `baseline.sql` numbers as JSON lines, for `metrics_report.py`. Sets the session read-only itself. |
| `caddy_cuts.py` | CDN cut rate per entry (Cloudflare, ESA, direct) from Caddy JSON access logs; with `--app`, matches each cut to the application log to find requests completed (and billed) after the client was cut; share of 503 responses with `Retry-After`. `--json` prints one object. |
| `session_streaks.py` | From application logs (JSON or console format): consecutive failures of one session on one account (requests without a session hash are counted per API key, separately), and the latency of requests that switched accounts and still succeeded. `--json` prints one object. |
| `metrics_report.py` | design §14 report: `collect` measures one environment into a JSON file, `report` compares a baseline with a candidate against the §14 targets (Markdown; exit 1 when a measured metric misses). |
| `fault-upstream/` | Fault-injecting upstream (Go, standard library only) with OpenAI Responses/Chat, Anthropic, Gemini, image, `/v1/usage` and new-api billing shapes. Scenarios F00–F39 (including F34B), `BAL` and `MIX`. |
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
| F19 / F20 | `/v1/usage` and new-api billing endpoints (served the RC balance probes, which 0.4.0 removed; no scenario uses them now) | `key` |
| F21 | Anthropic `message_start`, then stall | `secs` |
| F23 | Slow stream: a delta every `every` seconds for `secs` seconds, then completion with usage | `secs`, `every` |
| F24 | Relay keep-alive: empty `SSE-Keep-Alive` deltas (the first before `response.created`), then `response.failed` busy (Responses streams); with `output=1` a real delta comes first and the stream then completes | `secs`, `every`, `output` |
| F25 | Pooled relay: a Responses request with `previous_response_id` gets a vague 400 (`Upstream request failed`), others answer normally | |
| F26 | 200 SSE with some output, then an in-stream failure after `after` seconds | `after`, `code` (`busy` or `rate_limit_exceeded`) |
| F27 | 403 content-safety refusal in Zhipu's shape (top-level code `request_check_blocked`) | |
| F28 | Anthropic relay queue timeout: 200 SSE whose first event, after `after` seconds, is an `error` of type `rate_limit_error` | `after` |
| F29 | Codex backend relay: a Responses request replaying `web_search_call` input without declaring web_search gets 200 SSE, then `response.failed` "response protection is unavailable"; others answer normally | |
| F30 | 502 Cloudflare JSON error answer (`origin_bad_gateway`, the detail says the origin "is overloaded or misconfigured", `retry_after` 60 in the body) | |
| F31 | Responses `response.created`, then `response.failed` with `server_is_overloaded` | `after` (default 0.05 s) |
| F32 | Responses created then failed with `server_error` / `The service is busy` | `after` (default 0.05 s) |
| F33 | Responses created → `response.in_progress` → overloaded failure | `after` |
| F34 | Responses created → unknown `codex.response.metadata` → overloaded failure; conservative case that must prevent retry | `after` |
| F34B | Unmarked empty `response.output_text.delta` → created → in_progress → empty message added → empty output_text part added → overloaded failure | `before` (default 0.1 s), `after` |
| F35 | Responses endpoint returns 404; raw Chat stream emits error then `[DONE]` (nonstream returns 429) | |
| F36 | Nonstream returns 400 `This group only accepts streaming requests` with explicit `error.param: null` in the OpenAI error body; streaming succeeds | `first` |
| F37 | HTTP 503 `server_is_overloaded` with a known `Retry-After` | `reset` (default 8 s), `delay` (use 20 s for CDN commitment) |
| F38 | Streaming starts with a flushed `: keep-alive` SSE comment, then emits a normal completion or a failure (`response.created` → `response.failed` for Responses) | `outcome` (`failed` by default, or `ok`), `code`, `after`, `first`, `after_comment` (seconds before any data frame) |
| F39 | Anthropic streaming sends `message_delta(stop_reason=end_turn)` with terminal usage and closes at EOF without `message_stop`; Responses and Chat conversions should synthesize completion | |
| BAL | Normal while the balance of `key` is positive, F12 otherwise | `key`, `first`, `reset` (adds a relay's exhausted rate-limit headers) |
| MIX | A random fault with probability `p` percent per fault type, otherwise normal | `p` |

Any scenario accepts `delay` (seconds) to hold the answer back, for example `F16;delay=20` to return 529 after the gateway's pre-output heartbeats.

Control endpoints: `GET /_scenarios`, `GET /_requests?since=N` (outcome per request: `completed`, `cancelled`, `dropped`, `failed_in_stream`, `error_status`), `GET|POST /_control/balance?key=K&value=V`, `GET /healthz`.

F34B (also accepted as F34b) is a synthetic empty-delta regression case. Probes established the event ordering, but did not inspect delta content. F34's unknown event was not observed; it checks that unknown metadata is treated conservatively. F31–F34B, F36–F39 answer the gateway's tool capability probe normally so the scenario reaches the native Responses path. F38's leading comment is a wire-level heartbeat and is followed by a semantic event; use `outcome=failed;code=busy` for an error passthrough check and `outcome=ok` for a successful stream. F39 is intended for accounts whose upstream endpoint is Anthropic Messages; a Responses or Chat request converted through that account should complete and retain the terminal usage after the upstream EOF.

## Staging regressions for 0.4.4

Run `staging/fixtures.py` before `staging/scenarios.py`. Keep keys and evidence outside Git. The runner preserves complete SSE JSON so response IDs and terminal headers can be asserted, and reports SQL/HTTP failures instead of treating them as empty results.

| Scenarios | Assertions |
|---|---|
| CV-5–CV-8 | Retry pre-output failures; only B's opening and completion reach the client. CV-5 permits at most two A attempts under D18 and checks their persisted `CAPACITY_SHED` records; CV-6 checks exactly one assistant role and the busy classification; CV-8 rejects the unmarked empty delta before created. |
| CV-9 | Unknown metadata commits output; no B attempt follows. |
| RAW-1 / RAW-2 | Raw Chat error + DONE retries on B; a single-account failure is recorded and has no success usage row. |
| SO-1 | Learn Responses stream-only capability from F36's 400 with `error.param: null`, retry on B, show the Explain exclusion, skip A on later nonstream requests, still use A for streams, clear and relearn; no account failure or cooldown. |
| RA-1 / RA-2 | After CDN heartbeat, an exhausted request has one created→failed pair and matching 2–10 s advice; a recovered request completes without delayed advice. |
| ANT-3 | Anthropic no-account error is `api_error`; ANT-1 and ANT-2 retain their existing IDs. |
| SH-1 / SH-2 | Switch to share using account priority and load factors, return to original scheduling, and verify real transient failures reduce health weight and new-session traffic share. |
| LR-1 / DC-6 | Last resort follows account priority; compatibility openings wait for content while a native Anthropic opening remains unbilled. |
| Q-1 | A dedicated user with concurrency 1 queues behind a silent holder. After observing a queue ping, set that user's balance to zero; Responses gets created→failed, Chat gets error→DONE, Messages gets a named error. Every entry rejects bare JSON. Restore the user afterwards. |
| CX-1 | An empty group returns 503; either a Codex originator or Codex UA limits Retry-After to 10 s, while ordinary requests before and after keep the configured maximum. |

Capacity retry fixtures give B a different Docker DNS host name for the same fault service, so the one-host-switch rule can be tested without contacting a real upstream. Scenarios that change shared settings run serially. SH-2 generates real success/502 attempts on its dedicated accounts (temporarily deactivating B during failure injection), then allows up to 150 seconds for the minute-aligned health window; it restores account configuration and scheduling settings afterwards. Before sampling 80 fresh sessions, it proves A can serve at the best priority, then restores equal priorities. This separates health weighting from a lingering cooldown. The cohort checks reduced A traffic, not exact convergence to the new target, because allocation history survives weight changes.

Q-1 uses an Anthropic group for all three queued entry protocols, covering the Gateway Responses/Chat conversion writers and native Messages. The holder uses a separate key of the same user on an OpenAI group; its disconnect releases the shared user slot. A live line callback confirms the queue heartbeat arrived before the balance change, and fault records confirm the queued request never reached an upstream. Responses and Chat allow only SSE comments before their opening/error; Messages also permits its typed ping data. These additions bring the suite to 71 scenarios.

### Candidate validation preparation

`staging/build_candidate.py COMMIT --pr-head PR_HEAD --host SSH_ALIAS --directory STAGING_DIR`
plans a build from `git archive` without connecting to the host. After the candidate
is released, add `--execute` to build it once. The script refuses an existing image
or evidence directory, verifies the uploaded archive, and records the commit,
source tree, PR head tree, image ID and `/app/xy2api` SHA-256 in
`evidence/candidate-COMMIT/BUILD.json`. It leaves the running app unchanged.
Omit `--pr-head` only when the candidate itself is the intended head; compare the
recorded tree again if the PR head later changes.

On the staging host, `python3 phase2.py resources` checks disk and available memory
(30 GiB and 4 GiB minimum). `python3 phase2.py scenarios` prints the 71-case plan.
`python3 soak_stack.py plan` also renders both Compose configurations, checks the
proposed ports and subnet, and budgets both stacks together. It requires the sum
of their memory limits plus 2 GiB available, and leaves two host CPUs outside the
combined CPU quotas. Plan commands do not create containers, networks or volumes.

After release, select the candidate and apply `compose.yaml` together with
`compose.parallel.yaml` to the main stack, then refresh `fixtures.py`. The main
stack runs its 71 scenarios serially, followed by the upgrade drill:

```sh
python3 phase2.py scenarios --parallel-soak --execute --build evidence/candidate-COMMIT/BUILD.json --evidence evidence/run-COMMIT
python3 phase2.py upgrade --parallel-soak --execute --build evidence/candidate-COMMIT/BUILD.json --evidence evidence/upgrade-COMMIT
```

The runner loads only `.env`, binds both the running image and binary to BUILD.json,
locks the staging project, rejects existing output directories and records script
hashes. Scenarios run with `--parallel 1`; `SUITE.json` includes all 71 records.
The existing staging CF/direct host settings are required for their six cases.

In a separate process, the independent `xy2api-staging-soak` project can run
alongside that line. It has its own PostgreSQL, Redis, fault service, network,
volumes, generated credentials and fixture keys. It initializes fixtures with
`--soak-only`: six workload groups and a dedicated user, keeping key creation
below the per-user hourly limit. Its ports are 18310 (app),
18311 (fault) and 18313 (CDN simulator), all bound to loopback. No image is built
by this runner: the app is pinned to BUILD.json's image ID and both stacks' image
IDs and binary SHA-256 values must match before and after the soak.

```sh
python3 soak_stack.py start --execute --build evidence/candidate-COMMIT/BUILD.json --evidence evidence/soak-COMMIT
python3 soak_stack.py run --execute --build evidence/candidate-COMMIT/BUILD.json --evidence evidence/soak-COMMIT
# After collecting the report, remove only the owned soak project and temporary files:
python3 soak_stack.py cleanup --execute
```

Without `--execute`, start/run/cleanup only perform the read-only plan. Execution
uses a separate lock from the main stack. Startup verifies that the main stack's
parallel limits are applied. The limits total 6.125 GiB and 5.85 CPUs, including
LEASE-1's extra process or the bounded upgrade process. Both apps have 1 GiB limits.
Soak evidence stays outside the temporary `soak-stack` directory and survives
cleanup; the main stack and all candidate images are retained.

The upgrade stage uses 0.4.3, a separate synthetic database, rollback and another
forward upgrade, followed by backup/restore. Every transition checks health,
readiness, settings and the migration filename/checksum set. It produces a real
F36 capability learning record and verifies that the account still streams on
0.4.3, the extra key survives, and non-default v2 settings survive the round trip.
It does not save settings on 0.4.3: an old-version save drops new fields, as specified
by the C0 compatibility contract. Failure evidence is retained and the temporary
application container is removed. With `--parallel-soak`, its process is limited
to 1 GiB and 0.5 CPU. Upgrade start/end and bulk-fill/backup timestamps are included
in the soak report so overlapping disk I/O remains visible.

Soak uses `--hours 3 --cycle-minutes 30`, concurrency 20–50 and ten minutes of
settling; the independent stack enables `gctrace=1` on the same candidate image.
Logs are captured continuously to evidence so Docker rotation cannot remove the
early GC samples. Heap data from
at least three complete cycles after warm-up is mandatory, with slope at most
1 MiB/h. Missing heap or goroutine data cannot pass; SQL failures stop validation.
Every ERROR count is reported; the automatic flood threshold is at least 60 lines
per minute for three consecutive minutes. Individual errors still need attribution
in the final report. Existing slot, reservation, memory, panic and duplicate usage
checks remain in effect.

## Supplemental 0.4.5 checks

`staging/supplement_045.py` covers twelve Gateway-to-Anthropic cases: both
Responses and Chat exercise pre-output retry, post-output error, early EOF,
stall, first-content timeout and non-retryable failure. It is plan-only by
default; execution requires the full candidate SHA, its `BUILD.json`, the
expected image ID and a new evidence directory. Run after the 71-case suite,
using the same main-stack lock. It verifies image and binary identity and
restores the two fixture accounts and resilience settings afterwards.

Each case uses unique input/message text. These Gateway handlers derive
their fallback session from that text; changing `session_id` or
`prompt_cache_key` alone does not isolate them. Reusing a body allows a prior
case's sticky binding or session failure avoidance to skip fault account A.
Assertions require A's actual upstream attempt as well as terminal and usage
evidence, so a direct success on B cannot count as a passed fault case.

`staging/scenarios_045.py` is plan-only by default. With `--execute`, a new
evidence directory and the expected running image ID, it runs eight cases
serially on the main stack. Run it first against the formal 0.4.4 image and
then against the released candidate. It never builds or switches an app image.
The dedicated `G-qa-*` fixtures isolate these requests; settings, account
configuration and the temporary error rule are restored and checked afterwards.
Seed only these groups with `fixtures.py http://127.0.0.1:18210 --qa-only`;
this subset preserves the existing scenario groups and user balance.

- PT-7/PT-8 require the 12-second first-content/header deadline to cancel A
  and complete on B. Only PT-7 requires an internal comment heartbeat.
- PT-9 requires `stream_timeout` around 30 seconds and upstream cancellation,
  so a natural EOF after 50 seconds cannot pass. PT-11 checks one zero-token
  usage row after a failed passthrough stream.
- F38-9 enables API-key passthrough and checks that the upstream comment is
  visible before a two-second semantic gap. F38-12 uses a non-retryable error
  and a unique rule message to prove the in-stream error-rule path emits a
  matching created/failed pair after the comment, without bare JSON.
- F39-5R/F39-5C capture the upstream terminal EOF and verify the actual
  Anthropic attempt, a single successful converted terminal and one complete
  usage row (120 input, 24 output, 64 cache-read tokens).

OAuth headers/turn-state, failed terminals carrying nonzero usage, and exact
write/cancellation races are outside these supplemental scenarios.

## cdn-sim

```bash
cd cdn-sim && go build -o cdn-sim . && \
  LISTEN=:8080 TARGET=http://app:8080 FIRST_BYTE_TIMEOUT=125s IDLE_TIMEOUT=125s EDGE=cf ./cdn-sim
```

- No response headers within `FIRST_BYTE_TIMEOUT`: the client gets 524 and the origin request is cancelled.
- No bytes for `IDLE_TIMEOUT` while streaming: the client connection is dropped and the origin request is cancelled.
- `EDGE=none` forwards without a marker (useful as a request counter on the direct path).
- `GET /_cuts` lists cuts; `GET /_requests?since=N` lists every proxied request.

The application only honours `X-XY-Edge` from peers in `SERVER_TRUSTED_PROXIES`, so the simulator's network must be listed there. The simulator sends no CDN headers (`CF-Ray`, `CDN-Loop`), so since 0.4.1 a request through it is an edge request only through this marker, while requests through real Cloudflare or ESA are recognised by their own headers.
