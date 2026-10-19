# Session failure handling and last-resort selection

## ADDED Requirements

### Requirement: Automatic mechanisms never change scheduling intent

Session failure handling, cooldowns, avoidance and last-resort selection MUST NOT modify account priority, share-scheduling tier, weight, drain state or any stored policy, and MUST NOT adjust weights from statistics. Replacement accounts MUST be chosen in the existing order:
1. priority or tier first;
2. then weight (share scheduling) or the original load and LRU rules.

A lower tier MUST only be used when the whole higher tier is unavailable.

#### Scenario: Replacement respects tiers
- **WHEN** the bound account in tier 1 must be replaced and another tier-1 account is healthy
- **THEN** the replacement is the tier-1 account chosen by weight, not any tier-2 account.

### Requirement: Graded handling of failures on a bound account

For a request with a session identifier whose bound account fails:

- **Soft penalty from a single counted failure:** the binding MUST be kept. While the penalty lasts, requests MUST be placed temporarily on another account without writing a new binding, and MUST return to the bound account when the penalty ends. Counted failures are `RATE_LIMIT`, `TRANSIENT`, `FIRST_OUTPUT_TIMEOUT` and `STALL`.
- **Repeated failures:** when the same session has at least `session.streak_threshold` (default 2) counted failures on the same account within `session.streak_window_seconds` (default 600 s), the gateway MUST delete the binding if it still points to that account, and MUST exclude that account for that session for `session.avoid_seconds` (default 600 s). The window MUST be kept per account: failures of another account of the same session MUST NOT keep an old count alive (0.4.2).
- **Failures after output:** an upstream failure after semantic output reached the client (a failure event, a broken stream) cannot move to another account but MUST count once per attempt as `TRANSIENT`, unless it is a client error, a content-policy refusal or a client cancellation. Three such failures of one account within 60 s MUST put the account into the transient soft cooldown (pool-mode accounts excepted).
- **Hard state:** when the bound account enters a hard state, the binding MUST be replaced immediately.
- **No change:** concurrency saturation, first-token jitter, client cancellation, client errors and continuation requests owned by an account (`previous_response_id`) MUST NOT change the binding.

#### Scenario: One busy failure keeps the session
- **WHEN** a Codex session bound to account A receives one in-stream `The service is busy` failure
- **THEN** account A is soft-penalised, the session's next request goes to another account in the same tier without rebinding
- **AND** after the penalty ends the session returns to account A.

#### Scenario: Second failure moves the session
- **WHEN** the same session fails on account A again within 10 minutes
- **THEN** the binding is deleted with an owner check, account A is avoided for this session for 10 minutes
- **AND** the next request is placed by weight in the same tier.

#### Scenario: Failures after output move the session
- **WHEN** a session's streams on account A fail with `Our servers are currently overloaded` after output twice within 10 minutes
- **THEN** the second failure deletes the binding and account A is avoided for this session
- **AND** a third such failure of account A within 60 s soft-penalises it for `failure.transient_cooldown_seconds`, while a single-account group can still use it as a last resort.

#### Scenario: Balance exhaustion moves the session at once
- **WHEN** the bound account is paused for its balance
- **THEN** the session is rebound on its next request.

#### Scenario: Saturation does not rebind
- **WHEN** the bound account is at its concurrency limit
- **THEN** the existing wait or spill behaviour applies and the binding is unchanged.

### Requirement: Same semantics under both schedulers

In groups using share scheduling:
- soft penalties MUST be presented to the planner as ineligible candidates with `RecoverAt` within 300 s, so that `soft_spill` applies;
- avoided accounts MUST be presented as `Excluded` for the session;
- hard states MUST be presented without `RecoverAt`, so that `rebind_unavailable` applies.

In groups using the original scheduler, a bound account in a soft penalty MUST NOT have its binding cleared; only hard states clear the binding. This holds for both OpenAI scheduler paths (the advanced scheduler and the legacy path used when it is off), and the account that serves the request in the meantime MUST NOT take over the binding.

#### Scenario: Original scheduler during a cooldown
- **WHEN** the bound account of a session in a `sub2api` group has a 45 s 429 cooldown
- **THEN** the session's binding remains and the request is served by another account.

#### Scenario: OpenAI legacy scheduler during a cooldown
- **WHEN** the advanced OpenAI scheduler is off and the bound account of a session has a soft 429 cooldown
- **THEN** another account serves the request and the session stays bound to the original account.

#### Scenario: OpenAI per-model transient block keeps the binding
- **WHEN** the bound OpenAI account of a session is held back only by the scheduler's per-model transient block after upstream 5xx (legacy or advanced scheduler)
- **THEN** another account serves the request and the session stays bound to the original account.

#### Scenario: Another account's failure does not extend the window
- **WHEN** a session fails once on account A, then once on account B, and fails on A again after A's window ended
- **THEN** A's count restarts at one and the session is not rebound away from A.

### Requirement: Last-resort use of soft-penalised accounts

When no healthy candidate exists and at least one candidate is only soft-penalised, the gateway MUST NOT return "no available accounts". It MUST:
1. route to the group's availability fallback group when one is configured;
2. otherwise order the soft-penalised accounts by priority or tier, then by the end of their penalty, and use the first one with a free concurrency slot; only when all of them are busy, wait for the first one.

Last-resort attempts MUST NOT be subject to the per-attempt first-output timeout and MUST record the reason `last_resort_soft`.

Last-resort selection MUST apply every gate of normal selection (platform, model support, channel restrictions, quota, window cost, RPM, session limit, compact support, Grok quota auto-pause) and relax only soft penalties: soft cooldowns, soft rate limits, model rate limits within the soft threshold and OpenAI in-process runtime blocks. The bound account of a session MUST pass the window-cost and RPM gates as a sticky request. OpenAI last-resort slots MUST use the Codex adaptive concurrency.

In groups using share scheduling, the last resort MUST be a relaxed round of the share plan: soft-penalised accounts and accounts avoided only by the session count as eligible, and tiers, draining, weights and the cache domain still apply. A session bound to an account that is only soft-penalised and not avoided MUST keep its binding; an avoided bound account MUST be rebound to the account that serves; a hard-penalised one is rebound by the plan.

#### Scenario: Busy last-resort account
- **WHEN** the soft-penalised account whose penalty ends first is at its concurrency limit and another soft-penalised account has a free slot
- **THEN** the request is served by the account with the free slot instead of waiting.

#### Scenario: Single-account Grok group with a transient error
- **WHEN** the only account of a group receives a 30 s transient cooldown and no fallback group is configured
- **THEN** requests during the cooldown are still sent to that account instead of failing with 503.

#### Scenario: Single-account OpenAI group after upstream 5xx
- **WHEN** the only account of an OpenAI group has failed with 5xx three times in a row, so the scheduler's per-model breaker holds it back for 45 s, and no fallback group is configured
- **THEN** requests during the block are still sent to that account as a last resort instead of failing with "no available accounts".

#### Scenario: Hard state with no alternative
- **WHEN** the only account is in a balance pause and no fallback group is configured
- **THEN** the request fails with 503 and a `Retry-After` header.

#### Scenario: Account disabled after the group list was read
- **WHEN** last resort plans with the cached group list and the administrator disabled the chosen account after that list was read
- **THEN** that account is not dispatched; another eligible account serves, or the request gets "no available accounts", on both schedulers.

#### Scenario: Only the session limit stops last resort
- **WHEN** every account of a group is soft-penalised and last resort fails only because the new session hits an account's session limit
- **THEN** the "no available accounts" answer is not cached for the group, and the group's bound sessions are still served.

#### Scenario: Last resort respects hard limits
- **WHEN** every account of a group is soft-penalised and one of them is above its window-cost or RPM limit
- **THEN** the last resort never selects that account.

#### Scenario: Share-group last resort keeps tiers and the binding
- **WHEN** every account of a share group is soft-penalised and a session is bound to one of them
- **THEN** the last resort follows the share tiers, skips draining accounts and leaves the session bound to its account.

### Requirement: Explainability and configuration

The share-scheduling Explain view, and a read-only Explain for original-scheduler groups, MUST show for each candidate:
- its penalty tier and reason;
- `recoverAt`;
- the session's failure count;
- avoidance;
- the last-resort or fallback decision.

All thresholds MUST be configurable in `gateway_resilience_settings`, and `session.enabled=false` MUST restore the 0.3.0 binding behaviour.

#### Scenario: Emergency off
- **WHEN** an administrator sets `session.enabled` to false
- **THEN** the next requests use 0.3.0 sticky-session behaviour without a restart.
