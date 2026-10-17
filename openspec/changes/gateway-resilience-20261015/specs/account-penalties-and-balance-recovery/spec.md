# Account penalties and balance pauses

## ADDED Requirements

### Requirement: Hard and soft penalty tiers

Every account state that prevents normal scheduling MUST be either hard or soft.

**Hard** states:
- administrator disabled;
- `status=error`;
- a balance pause (reason starting `balance:`);
- invalid or revoked credentials;
- rate limit with a reset more than 300 s away;
- IQ quality gate;
- profit gate;
- model unsupported;
- group mismatch.

**Soft** states:
- transient and stall cooldowns;
- 429 cooldown without a reset time, or with a reset of at most 300 s;
- overload cooldown;
- model-level transient blocks;
- per-session avoidance.

Hard states MUST always exclude the account while they last; a balance pause lasts `balance.pause_seconds`. Soft states MUST exclude the account only while a healthy candidate exists.

#### Scenario: Soft cooldown in a multi-account group
- **WHEN** account A is in a 30 s transient cooldown and account B in the same tier is healthy
- **THEN** new requests are placed on B.

#### Scenario: Hard state is never used
- **WHEN** the only account in a group is in a balance pause
- **THEN** it is not selected, even as a last resort.

### Requirement: Transient failures apply a soft cooldown

Account-scoped `TRANSIENT` and `STALL` outcomes MUST set a soft cooldown of `failure.transient_cooldown_seconds` (default 30 s). A `FIRST_OUTPUT_TIMEOUT`, whether the gateway detects it or the upstream answers it, MUST NOT cool the account down: it counts for the session failure streak and switches accounts; OpenAI API-key accounts keep the per-model transient block of upstream 5xx. When the upstream supplies `retry_after`, the cooldown MUST be `min(retry_after, failure.retry_after_cap_seconds)` (default cap 120 s).

#### Scenario: Relay asks for 60 seconds
- **WHEN** an attempt returns 502 with a Cloudflare error body containing `"retry_after":60`
- **THEN** the account is soft-penalised for 60 s.

### Requirement: Balance pause

Balance failures of eligible API-key accounts MUST NOT set `status=error`. A balance failure of a real request (an HTTP error answer, a failure inside a stream, a Grok API-key 429 with balance wording, a 402) MUST pause the account for `balance.pause_seconds` (default 300 s, 30–3600 s) through `temp_unschedulable` with a reason starting `balance:`; a longer pause of another kind stays. When the pause ends, real traffic retries the account, and a balance failure pauses it again for the same time. A recent success MUST NOT weaken a balance signal. The gateway MUST NOT keep any other balance state: no balance record, probe, recovery concurrency cap or balance alert. The administrator's "restore status" MUST end the pause at once. With `balance.enabled=false`, balance failures MUST take the 0.3.0 handling.

#### Scenario: Relay runs out of credit
- **WHEN** account A returns 403 "Insufficient account balance"
- **THEN** it is paused for 300 s with a `balance:` reason and its `status` stays `active`
- **AND** it is not selected, not even as a last resort, until the pause ends, after which it is selectable again.

#### Scenario: 402 only pauses
- **WHEN** an API-key account returns 402
- **THEN** it is paused and not set to error.

#### Scenario: Grok relay balance wording does not install a rate limit
- **WHEN** an API-key Grok account outside pool mode answers 429 "insufficient balance" together with an exhausted request window in its rate-limit headers
- **THEN** the account is paused and no rate limit is set from the headers
- **AND** OAuth Grok accounts, pool-mode accounts and 429s without balance wording keep the Grok rate-limit handling.

#### Scenario: Balance failure inside a stream
- **WHEN** a stream fails with `insufficient_quota`
- **THEN** the account is paused.

#### Scenario: Single-account group during a pause
- **WHEN** the only account of a group is paused
- **THEN** requests get 503 with a `Retry-After` no longer than the rest of the pause.

#### Scenario: Administrator restores the account
- **WHEN** an administrator calls `DELETE /api/v1/admin/accounts/:id/temp-unschedulable`
- **THEN** the pause ends at once and the account is selectable.

### Requirement: Uniform 403 handling across platforms

For every API-key platform, a non-balance 403 MUST use the OpenAI policy: HTML and Cloudflare block pages are exempt; otherwise each 403 within 180 minutes increments a counter and applies a 10-minute soft cooldown, and only the third 403 sets the error state.

#### Scenario: One 403 on an Anthropic relay
- **WHEN** an Anthropic API-key account returns a single non-balance 403
- **THEN** the account is cooled down for 10 minutes and remains `active`.
