# Account penalties and balance recovery

## ADDED Requirements

### Requirement: Hard and soft penalty tiers

Every account state that prevents normal scheduling MUST be either hard or soft.

**Hard** states:
- administrator disabled;
- `status=error`;
- balance `EXHAUSTED`;
- invalid or revoked credentials;
- rate limit with a reset more than 300 s away;
- IQ quality gate;
- profit gate;
- model unsupported;
- group mismatch.

**Soft** states:
- balance `SUSPECT`;
- transient, first-output-timeout and stall cooldowns;
- 429 cooldown without a reset time, or with a reset of at most 300 s;
- overload cooldown;
- model-level transient blocks;
- per-session avoidance.

Hard states MUST always exclude the account while they last; an `EXHAUSTED` balance lasts until its next check plus a grace period (design §3.2), after which real traffic re-checks the account. Soft states MUST exclude the account only while a healthy candidate exists.

#### Scenario: Soft cooldown in a multi-account group
- **WHEN** account A is in a 30 s transient cooldown and account B in the same tier is healthy
- **THEN** new requests are placed on B.

#### Scenario: Hard state is never used
- **WHEN** the only account in a group is balance `EXHAUSTED`
- **THEN** it is not selected, even as a last resort.

### Requirement: Transient failures apply a soft cooldown

Account-scoped `TRANSIENT`, `FIRST_OUTPUT_TIMEOUT` and `STALL` outcomes MUST set a soft cooldown of `failure.transient_cooldown_seconds` (default 30 s). When the upstream supplies `retry_after`, the cooldown MUST be `min(retry_after, failure.retry_after_cap_seconds)` (default cap 120 s).

#### Scenario: Relay asks for 60 seconds
- **WHEN** an attempt returns 502 with a Cloudflare error body containing `"retry_after":60`
- **THEN** the account is soft-penalised for 60 s.

### Requirement: Recoverable balance state machine

Balance failures MUST NOT set `status=error`. Each API-key account MUST carry a balance state `OK`, `SUSPECT`, `EXHAUSTED` or `RECOVERING`:

- **A strong balance signal** from a real request moves the account to `EXHAUSTED`.
- **A weak signal** (balance wording on a 429 while the same account succeeded within the last 30 s) moves it to `SUSPECT` for `balance.suspect_ttl_seconds`.
- **`EXHAUSTED` accounts are re-checked** after 60, 120, 300 and 600 s, then every 900 s, each with ±15 % jitter.
- **A positive probe** moves the account to `RECOVERING`. There its concurrency is capped at `balance.recovering_concurrency` until `recovering_success_target` successes or `recovering_window_seconds` elapse; it then returns to `OK`.
- **A balance failure while `RECOVERING`** returns the account to `EXHAUSTED` at the next back-off level.

#### Scenario: Claude relay runs out of credit
- **WHEN** account A returns 403 `insufficient balance`
- **THEN** its balance state becomes `EXHAUSTED`, it is excluded from scheduling and its `status` stays `active`
- **AND** an administrator notification is emitted.

#### Scenario: Grok relay balance wording does not install a rate limit
- **WHEN** an API-key Grok account outside pool mode answers 429 "insufficient balance" together with an exhausted request window in its rate-limit headers
- **THEN** the account enters the balance state machine (`SUSPECT` after a success within 30 s, otherwise `EXHAUSTED`) and no rate limit is set from the headers
- **AND** OAuth Grok accounts, pool-mode accounts and 429s without balance wording keep the Grok rate-limit handling.

#### Scenario: Automatic recovery after top-up
- **WHEN** the next scheduled probe of an `EXHAUSTED` account reports a positive balance
- **THEN** the account enters `RECOVERING`, accepts at most one concurrent request and returns to `OK` after three successful requests.

#### Scenario: Administrator confirms a top-up
- **WHEN** an administrator calls `POST /api/v1/admin/accounts/:id/balance/recheck`
- **THEN** a probe runs immediately instead of waiting for the next scheduled check.

### Requirement: Probe order and evidence

Balance probes MUST be tried from cheapest to most expensive:

1. `GET {base_url}/v1/usage` for sub2api-style relays;
2. the new-api billing endpoints, only after they are verified for the relay type;
3. a streaming generation with `max_tokens=1` on the cheapest available model, limited to `balance.generation_probe_daily_budget` per account per day.

A reachable `/v1/models` or a credentials-only check MUST NOT count as recovery evidence. Probes for the same account MUST be single-flight across instances.

#### Scenario: Probe finds invalid credentials
- **WHEN** a balance probe returns 401 `invalid api key`
- **THEN** the account follows the authentication-failure path instead of the balance path.

#### Scenario: Two instances schedule the same probe
- **WHEN** two gateway instances reach the same account's `next_check_at`
- **THEN** only one probe request is sent upstream.

### Requirement: Uniform 403 handling across platforms

For every API-key platform, a non-balance 403 MUST use the OpenAI policy: HTML and Cloudflare block pages are exempt; otherwise each 403 within 180 minutes increments a counter and applies a 10-minute soft cooldown, and only the third 403 sets the error state.

#### Scenario: One 403 on an Anthropic relay
- **WHEN** an Anthropic API-key account returns a single non-balance 403
- **THEN** the account is cooled down for 10 minutes and remains `active`.
