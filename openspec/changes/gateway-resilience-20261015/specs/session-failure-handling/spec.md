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
- **Repeated failures:** when the same session has at least `session.streak_threshold` (default 2) counted failures on the same account within `session.streak_window_seconds` (default 600 s), the gateway MUST delete the binding if it still points to that account, and MUST exclude that account for that session for `session.avoid_seconds` (default 600 s).
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

#### Scenario: Balance exhaustion moves the session at once
- **WHEN** the bound account enters balance `EXHAUSTED`
- **THEN** the session is rebound on its next request.

#### Scenario: Saturation does not rebind
- **WHEN** the bound account is at its concurrency limit
- **THEN** the existing wait or spill behaviour applies and the binding is unchanged.

### Requirement: Same semantics under both schedulers

In groups using share scheduling:
- soft penalties MUST be presented to the planner as ineligible candidates with `RecoverAt` within 300 s, so that `soft_spill` applies;
- avoided accounts MUST be presented as `Excluded` for the session;
- hard states MUST be presented without `RecoverAt`, so that `rebind_unavailable` applies.

In groups using the original scheduler, a bound account in a soft penalty MUST NOT have its binding cleared; only hard states clear the binding.

#### Scenario: Original scheduler during a cooldown
- **WHEN** the bound account of a session in a `sub2api` group has a 45 s 429 cooldown
- **THEN** the session's binding remains and the request is served by another account.

### Requirement: Last-resort use of soft-penalised accounts

When no healthy candidate exists and at least one candidate is only soft-penalised, the gateway MUST NOT return "no available accounts". It MUST:
1. route to the group's availability fallback group when one is configured;
2. otherwise select, within the highest tier containing soft-penalised accounts, the account whose penalty ends soonest.

Last-resort attempts MUST NOT be subject to the per-attempt first-output timeout and MUST record the reason `last_resort_soft`.

#### Scenario: Single-account Grok group with a transient error
- **WHEN** the only account of a group receives a 30 s transient cooldown and no fallback group is configured
- **THEN** requests during the cooldown are still sent to that account instead of failing with 503.

#### Scenario: Hard state with no alternative
- **WHEN** the only account is balance `EXHAUSTED` and no fallback group is configured
- **THEN** the request fails with 503 and a `Retry-After` header.

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
