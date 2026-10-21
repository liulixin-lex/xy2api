# 份额调度规格增量（精简）

## MODIFIED Requirements

### Requirement: Per-group scheduler selection

The system MUST select the scheduler per group: `sub2api` (default) or `share`. A stored `shadow` scheduler MUST be treated as `sub2api` on the request path, in the group list and in the policy view, and MUST NOT be written by this version. A global switch MUST disable share scheduling for every group immediately. Switching a group between schedulers MUST NOT move any existing session binding.

#### Scenario: Stored shadow policy after upgrade
- **WHEN** a group's stored policy has scheduler `shadow`
- **THEN** its requests are selected by the Sub2API algorithm
- **AND** the group list and the policy view report scheduler `sub2api` with `legacy_shadow=true`.

### Requirement: Strict tiers

Within a share-scheduled group, the tier of an account MUST be its account priority. A new session or sessionless request MUST NOT be placed on a lower priority while a higher priority has an eligible account that is neither saturated nor at its slot limit. Account rules stored in the policy MUST NOT affect the tier.

#### Scenario: Stored rule tier is ignored
- **WHEN** a stored rule gives an account tier 2 and the account's priority is 1
- **THEN** the account is placed in priority 1.

### Requirement: Proportional new-session placement

New sessions MUST be placed among eligible accounts of the active priority by weighted rendezvous hashing over the affinity key. The weight of an account MUST be its effective load factor (the load factor when set, otherwise its concurrency, otherwise 1) times the adaptive factors of the requirement "Adaptive weights". Weights stored in the policy MUST be ignored.

#### Scenario: Load factor ratio
- **WHEN** three eligible accounts of one priority have load factors 500, 300 and 200 and 10,000 distinct session keys are placed
- **THEN** each account's share deviates from 50%, 30% and 20% by at most one percentage point.

#### Scenario: Unset load factor
- **WHEN** an account has no load factor and concurrency 100, and another has load factor 100
- **THEN** both receive the same share of new sessions.

### Requirement: Session affinity

A group MUST have one session-keeping setting. When it is on, affinity keys and bindings behave as with `affinity.key=auto`. When it is off, every request is placed by weight without binding. The binding lifetime, bounded wait, spill rebinding, cold realignment and tier return MUST use the fixed values of design §1.

A bound account that cannot serve MUST keep the binding (soft spill) only when the gateway-resilience penalty of the account is soft and its expected recovery plus the session's idle time is below 0.8 of the cache lifetime. A hard penalty, including a temporary unschedulable state whose reason is empty or not a soft prefix, MUST rebind at once. While the gateway-resilience master switch is off, the 0.3.0 rule MUST apply instead: any state that ends within 300 seconds counts as soft, a balance pause included.

#### Scenario: Hard temporary state rebinds
- **WHEN** the bound account is temporarily unschedulable with a reason that gateway resilience classifies as hard, for 60 seconds
- **THEN** the session is served elsewhere and its binding moves to the serving account.

#### Scenario: Resilience off keeps the 0.3.0 rule
- **WHEN** the gateway-resilience master switch is off
- **AND** the bound account is temporarily unschedulable for 60 seconds, with any reason
- **THEN** the request is served elsewhere and the binding still points at the bound account.

#### Scenario: Short rate limit keeps the binding
- **WHEN** the bound account is rate limited for 60 seconds and the session's cache is warm
- **THEN** the request is served elsewhere and the binding still points at the bound account.

### Requirement: Administrator policy safety

Saving a group policy MUST change only the scheduler and the session-keeping setting of the stored document and keep every other stored field unchanged, so that an earlier version reads the same rules after a rollback. Saving MUST NOT add a field that the policy decoder of 0.4.3 rejects. Saving MUST be guarded by the expected version.

#### Scenario: Rollback reads the original rules
- **WHEN** a v4 policy with account rules is saved by this version with scheduler `share`
- **THEN** the 0.4.3 decoder decodes the saved document
- **AND** it contains the original account rules unchanged.

#### Scenario: Removed fields are accepted
- **WHEN** a stored policy or a request from an older admin page contains removed fields
- **THEN** decoding succeeds and the removed fields have no effect.

#### Scenario: Concurrent edits
- **WHEN** two administrators save the same group policy from version 3
- **THEN** one save succeeds with version 4 and the other receives 409 without overwriting it.

### Requirement: Spill and failover order

Pre-output failover MUST keep the existing handler loop, retry limits, cooldown handling and output-commit protection. The spill and failover order of a session MUST be its rendezvous order, with accounts of the bound account's platform and account type first. Saturated accounts (design §5) MUST be skipped. Lower priorities MUST only be used when the current priority has no usable account.

#### Scenario: Repeated failure order
- **WHEN** the same session fails on its first account twice in separate requests
- **THEN** both retries are placed on the same second-choice account.

### Requirement: Explainability and reporting

The admin API MUST explain one placement without side effects, simulating a new session when no session hash is given. The report MUST give, for the current window and for the equal window before the last scheduler change, the request count, cache hit ratio, first-token P50 and P95, error rate, account-switch rate and the share of cache lost to account switches, each with its sample count.

#### Scenario: Explain a new session
- **WHEN** explain is called without a session hash
- **THEN** the result is a new-session placement and no binding or slot is touched.

## ADDED Requirements

### Requirement: Saturation aligned with the original scheduler

For new placements and spills, an account whose load rate, `(in flight + waiting) × 100 ÷ effective load factor`, is 100 or more MUST be treated as saturated and skipped, as the original scheduler does. The bound account of a session MUST be judged only by its slot limit.

#### Scenario: Small load factor caps new placements
- **WHEN** an account has load factor 2 and two requests in flight
- **THEN** new sessions are placed on another account of the same priority
- **AND** sessions already bound to it continue while it has a free slot.

### Requirement: Adaptive weights

The weight of an account MUST be multiplied by a health factor and a ramp factor and MUST NOT fall below 5% of its effective load factor. Neither factor MUST have a setting, and both MUST be 1 while the gateway-resilience master switch is off. The factors MUST NOT change the account of a session that hits its binding, the saturation check or the tiers.

- Health factor: over the last 30 minutes, the account's soft failures (attempts that gateway resilience answers with a soft cooldown) over its attempts (successful requests plus soft failures), shrunk towards the tier baseline with 20 pseudo-attempts, MUST give `max(0.2, 2^(−excess ÷ 5 points))` where the excess is the shrunk rate above the baseline plus 3 points, quantized to 0.05. The baseline MUST be the lowest raw rate of the tier's healthy members with at least 20 attempts, or with any attempt when none has 20. A tier with fewer than two healthy members, an account without attempts, stale or unreadable statistics, and a disabled ops monitor MUST give no factor (×1).
- Ramp factor: after a balance pause, a hard penalty, or an outage of at least 5 minutes observed by the instance, the factor MUST climb from 0.1 to 1 over 10 minutes, quantized to 0.05. A short soft cooldown MUST NOT ramp.

#### Scenario: Unstable account in a tier
- **WHEN** three accounts of one priority with load factor 100 had 1.1%, 9.0% and 20.8% soft failures in the last 30 minutes
- **THEN** their health factors are 1, 0.5 and 0.2 and new sessions split about 59%, 29% and 12%.

#### Scenario: Low-sample peer
- **WHEN** a healthy peer had 2 attempts without failure and the others more than 20 attempts
- **THEN** the baseline is taken from the members with at least 20 attempts.

#### Scenario: Recovery from a balance pause
- **WHEN** an account's balance pause ended 3 minutes ago
- **THEN** its weight is 35% of its load factor and reaches 100% 10 minutes after the pause ended.

### Requirement: Placement preview

Before saving, the admin API MUST return the expected new-session share of every member and structured warnings: global switch off, no healthy account, highest priority without a healthy account, one account above 80% of new sessions, accounts with a load factor of 999 or more, accounts that are ramping, and accounts with a health factor of 0.5 or less.

#### Scenario: Dominant account
- **WHEN** a healthy account's load factor gives it more than 80% of the active priority's weight
- **THEN** the preview returns a `single_account_dominant` warning with its account and share.

## REMOVED Requirements

### Requirement: Bounded correction

**Reason**: never used in production; it only affected configured accounts and aggregated `usage_logs` periodically. **Migration**: none; stored `balance` fields are ignored.

### Requirement: Shadow evaluation

**Reason**: the preview gives the expected shares directly and rollback is immediate. **Migration**: stored `shadow` groups run the original scheduler.

### Requirement: Rollout gates

**Reason**: automatic gates and fallback were never implemented. The report now compares the current window with the window before the last scheduler change, and the console applies its thresholds, which are pending production calibration. **Migration**: none.

### Requirement: Cache-preserving migration

**Reason**: drain, drain deadlines, return deadlines and rebalance are removed. Cold-window realignment and tier return remain as fixed rules under Session affinity. **Migration**: stored drain fields are ignored; lower an account's load factor to 1 to reduce its new sessions, or turn its scheduling switch off to stop it.
