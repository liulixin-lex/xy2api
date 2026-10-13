# 份额调度规格

## ADDED Requirements

### Requirement: Per-group scheduler selection

The system MUST select the scheduler per group: `sub2api` (default), `shadow` or `share`. A global switch MUST disable share scheduling for every group immediately. When the switch is off, a group has no policy, or its scheduler is `sub2api` or `shadow`, account selection MUST execute the unmodified Sub2API algorithm. Switching a group between schedulers MUST NOT move any existing session binding.

#### Scenario: Default after upgrade
- **WHEN** an installation upgrades from a version that used controlled scheduling
- **THEN** every group uses the `sub2api` scheduler
- **AND** previously saved group priorities and weights exist only as disabled share drafts.

#### Scenario: Emergency rollback
- **WHEN** an administrator turns the global switch off
- **THEN** the next request in every group is selected by the Sub2API algorithm without restarting the service
- **AND** sessions bound by explicit session identifiers keep their bound accounts.

### Requirement: Strict tiers

Within a share-scheduled group, a new session or sessionless request MUST NOT be placed on a lower tier while the highest tier containing an eligible, non-drained account with free capacity exists. Placement on a lower tier MUST only occur through the configured saturation or failure policy and MUST be recorded with `tier_overflow_saturated`, `tier_overflow_unavailable` or `failover_next`. Sessions already bound to a lower tier MUST follow the configured return policy.

#### Scenario: Backup tier stays idle
- **WHEN** tier 1 has an eligible account with free capacity and tier 2 is configured as backup
- **THEN** no new session or sessionless request is placed on tier 2.

#### Scenario: Saturated top tier with wait policy
- **WHEN** all tier 1 accounts are eligible but saturated and `on_saturated=wait`
- **THEN** the request receives the existing Sub2API wait plan for a tier 1 account instead of spilling to tier 2.

#### Scenario: Default return waits for a cold cache
- **WHEN** a session is bound to a tier 2 account, tier 1 has been eligible for `stable_seconds`, `return=cold` and no return deadline is set
- **THEN** the session moves to tier 1 only on a request that arrives after the session has been idle at least the cache lifetime, or when it has no cacheable prefix
- **AND** the move is recorded as `return_cold`.

#### Scenario: Optional return deadline
- **WHEN** `return_deadline_seconds` is set and the deadline after tier recovery has passed
- **THEN** remaining lower-tier sessions move to tier 1 on their next request, staggered per session over at most 600 seconds, and each move is recorded as `return_deadline`.

### Requirement: Proportional new-session placement

New sessions MUST be placed among eligible accounts of the active tier by weighted rendezvous hashing over the affinity key, so that each account's probability equals its effective weight divided by the sum of effective weights. The ranking MUST be deterministic across service instances for identical inputs and MUST be salted by group. Accounts without a configured weight MUST default to their concurrency limit.

#### Scenario: Configured ratio
- **WHEN** three eligible accounts have weights 5, 3 and 2 and 10,000 distinct session keys are placed
- **THEN** each account's share deviates from 50%, 30% and 20% by at most one percentage point.

#### Scenario: Minimal disruption
- **WHEN** one account becomes ineligible
- **THEN** only sessions whose first choice was that account change placement
- **AND** those sessions are distributed among the remaining accounts in proportion to their weights.

### Requirement: Stable affinity keys

For share-scheduled groups with `affinity.key=auto`, the affinity key MUST be derived, in order, from protocol continuation, explicit session identifiers, and a conversation anchor. Explicit session identifiers are the Anthropic `metadata.user_id` session, the session headers already recognized by the OpenAI path, and `prompt_cache_key`. They MUST produce the same binding key as Sub2API. The conversation anchor MUST be derived from the API key, system or instructions text, tool definitions and the first user message, so that it stays identical across turns of one conversation and differs between API keys. Groups using the `sub2api` scheduler MUST keep the original session hash unchanged.

#### Scenario: Client moves cache breakpoints every turn
- **WHEN** an Anthropic client without a session identifier sends successive turns of one conversation with `cache_control` on the latest message
- **THEN** every turn has the same affinity key and stays on the same account.

#### Scenario: Shared system prompt across tenants
- **WHEN** two API keys send different conversations with the same cached system prompt
- **THEN** their affinity keys differ.

### Requirement: Session affinity

A bound session MUST stay on its bound account while that account is eligible and has capacity. The binding MUST only be replaced in the following cases, each recorded with a reason code:
- the bound account is unavailable beyond the soft-spill rule;
- the wait budget is exhausted and `on_spill=rebind`;
- the session's cache is cold and its bound account is no longer its ideal account;
- an administrator deadline has passed;
- an administrator rebalances the session;
- pre-output failover succeeds on another account.

#### Scenario: Weight change does not move warm sessions
- **WHEN** an administrator changes weights in a tier
- **THEN** new sessions follow the new weights immediately
- **AND** existing sessions move only on a request after their cache has become cold, recorded as `realign_cold`.

#### Scenario: Saturated bound account with valuable cache
- **WHEN** the bound account is eligible but saturated, the session's cache is warm, the estimated cacheable input is at least the model minimum, and fewer than `max_waiting` requests are waiting
- **THEN** the request waits for the bound account for at most `min(max_ms, estimated tokens / 1000 × ms_per_1k_tokens)`
- **AND** if no slot frees within that budget, the request is served by the first account with capacity in the session's spill order and the session is rebound to it.

#### Scenario: Saturated bound account without valuable cache
- **WHEN** the bound account is saturated and the estimated input is below the model minimum cacheable length
- **THEN** the request does not wait and is served by the next account in the session's spill order.

#### Scenario: Short unavailability keeps the binding
- **WHEN** the bound account is rate limited with a known reset in 120 seconds, the session has been idle 30 seconds and the cache lifetime is 300 seconds
- **THEN** the request is served by the next account in the spill order without changing the binding, recorded as `soft_spill`
- **AND** after the reset the session returns to the bound account.

#### Scenario: Long unavailability rebinds
- **WHEN** the bound account is rate limited for 600 seconds and the cache lifetime is 300 seconds
- **THEN** the session is rebound to the next eligible account, recorded as `rebind_unavailable`.

### Requirement: Cache-preserving migration

Share scheduling MUST NOT move a session whose cache on the bound account is still warm because of a policy action. Policy actions are weight changes, tier return, drain without deadline, bounded correction, and switching schedulers. Exceptions are an administrator deadline (`drain_until`, `return_deadline_seconds`) and hard unavailability. A cache MUST be treated as cold when the session idle time is at least the cache lifetime for the request, or when the estimated input is below the model's minimum cacheable length. Session idle time MUST be derived from the remaining TTL of the existing sticky binding key, read in the same round trip as the binding.

#### Scenario: Cache lifetime by request
- **WHEN** an Anthropic request contains a `cache_control` block with `"ttl": "1h"`
- **THEN** the cache lifetime used for that session is 3600 seconds
- **AND** without such a block it is 300 seconds; OpenAI requests use 1800 seconds; Gemini and unknown platforms use 300 seconds unless the group overrides `cache.ttl`.

#### Scenario: Drain without deadline
- **WHEN** an account is drained without `drain_until`
- **THEN** it receives no new sessions
- **AND** its bound sessions leave only on requests after their caches have become cold, recorded as `drain_cold`.

#### Scenario: Preview shows warm migrations
- **WHEN** an administrator previews a policy that sets a return deadline or `drain_until`
- **THEN** the preview reports the number of currently warm bound sessions that the deadline would move and their estimated cache rebuild tokens
- **AND** a policy change without deadlines reports zero warm migrations.

### Requirement: Eligibility reuses Sub2API gates with reason codes

Share scheduling MUST evaluate the same eligibility predicates as the Sub2API path, and each exclusion MUST produce a stable reason code. The predicates cover:
- account state: status, schedulable switch, expiry, rate limit, overload, temporary unschedulable;
- limits: quotas, window cost and RPM with sticky semantics, session limits, quota auto-pause;
- model and routing: model support and model limits, IQ, channel restriction, profit gate, privacy;
- compatibility: platform, transport, capability, runtime blocks.

Gates with a known recovery time MUST expose it for the soft-spill rule. Share scheduling MUST NOT add a separate health state machine or latency score.

#### Scenario: Sticky-only window cost
- **WHEN** an Anthropic OAuth account is in the window-cost sticky-only zone
- **THEN** its bound sessions remain eligible
- **AND** it is excluded from new-session placement with reason `window_cost`.

### Requirement: Spill and failover order

Pre-output failover MUST keep the existing handler loop, retry limits, cooldown handling and output-commit protection. Accounts excluded by the handler MUST be skipped with reason `excluded_in_request`.

The spill and failover order of a session MUST be its rendezvous order, stably partitioned:
1. accounts in the bound account's cache domain;
2. accounts of the same platform and account type;
3. all other accounts.

Within the current tier the order MUST follow this partition. Tiers MUST then be descended according to `on_saturated` or `on_failed`.

#### Scenario: Repeated failure order
- **WHEN** the same session fails on its first account twice in separate requests
- **THEN** both retries are placed on the same second-choice account.

#### Scenario: Shared cache domain first
- **WHEN** a bound account shares a cache domain with another eligible account in the same tier
- **THEN** a spill or failover from the bound account goes to that account before any account outside the domain.

### Requirement: Selection latency budget

Share scheduling MUST NOT add database queries to the request hot path and MUST NOT use more Redis round trips during selection than the Sub2API path for the same case. The cases are a bound hit, a new session and saturation. Waiting for a bound account MUST be bounded by the cache-value wait budget, with a maximum of 15 seconds by default.

#### Scenario: Bound hit round trips
- **WHEN** a bound session's account has a free slot
- **THEN** selection performs one pipelined read of the binding and its remaining TTL, one slot acquisition and one TTL refresh.

### Requirement: Bounded correction

When enabled, correction MUST only scale the weights used for new sessions, sessionless requests and cold realignment, within the configured lower and upper bounds. It MUST use realized share from usage logs over the configured window. Correction MUST be disabled when the unit is `sessions` or the window volume is below `min_volume`.

#### Scenario: Under-served account
- **WHEN** an account's realized share is below its target and correction is `gentle`
- **THEN** its effective weight increases by at most a factor of two
- **AND** sessions with warm caches are not moved.

### Requirement: Explainability and reporting

Every placement whose account differs from the ideal account MUST record the reason why the ideal account was skipped. The ideal account is the HRW first choice under the configured top tier and configured weights.

The system MUST provide read-only explain and preview operations that acquire no slots and write no bindings.

The system MUST provide a report that compares target and realized shares and includes:
- deviation reasons;
- cache read ratio and cache attribution;
- first-token percentiles split by decision type;
- warm-migration counts by reason.

#### Scenario: Explain without side effects
- **WHEN** an administrator explains a request for a group, model and session key
- **THEN** the response lists the affinity key source, cache lifetime, idle time, estimated tokens and wait budget
- **AND** it lists every candidate's tier, weight, effective weight, eligibility reason, saturation, cache domain and rendezvous rank, with the final decision
- **AND** no concurrency slot, wait counter, binding or counter is changed.

### Requirement: Cache attribution

For groups on any scheduler, the system MUST classify settled requests into exactly one of:
- `cache_hit`
- `below_min_cacheable`
- `miss_new_session`
- `miss_switch_policy`
- `miss_switch_saturated`
- `miss_switch_unavailable`
- `miss_switch_failover`
- `miss_ttl_expired`
- `miss_prefix_drift`

The classification MUST use usage cache tokens and a sampled per-session record of the previous request's account, start time and input size. The record MUST store only a truncated key fingerprint. Attribution failures MUST NOT affect requests.

#### Scenario: Prefix drift is not blamed on scheduling
- **WHEN** a sampled session's request lands on the same account as its previous request within the cache lifetime and reads less than half of the previous input from cache
- **THEN** the request is attributed to `miss_prefix_drift`, not to a scheduling switch.

### Requirement: Shadow evaluation

A group with scheduler `shadow` MUST be served by the Sub2API algorithm. For sampled requests, share scheduling MUST compute its plan from the same candidates without acquiring slots or writing bindings, and record:
- agreement;
- projected spills, rebinds and realignments;
- projected warm migrations;
- computation time.

#### Scenario: Shadow has no side effects
- **WHEN** a group runs in shadow mode
- **THEN** its selected accounts, bindings and concurrency counters are identical to running `sub2api`.

### Requirement: Rollout gates

The report MUST evaluate the following gates against the group's own baseline and against concurrent Sub2API groups:
- token-weighted cache read ratio not lower than baseline by more than 0.5 percentage points;
- scheduling-caused miss tokens not higher than baseline;
- first-token P50 and P95 within 3% and 5% of baseline for matched model and input-size buckets;
- policy-driven warm migrations at most 0.1% of requests.

A gate breach MUST raise an alert. Automatic fallback to `sub2api` MUST be opt-in.

#### Scenario: Gate breach
- **WHEN** a share-scheduled group's cache read ratio stays more than 0.5 percentage points below baseline for 30 minutes
- **THEN** administrators are alerted
- **AND** if automatic fallback is enabled, the group switches to `sub2api` without moving bound sessions.

### Requirement: Hot path availability

Share scheduling MUST NOT add database queries to the request hot path. Policy and switch reads MUST use process caches refreshed by broadcast and periodic reload. If the cache cannot be refreshed, the last known policy MUST be used; if no policy was ever loaded, the group MUST use the Sub2API algorithm. Statistics failures MUST NOT fail requests.

#### Scenario: Database outage during traffic
- **WHEN** PostgreSQL is temporarily unreachable after policies were loaded
- **THEN** share-scheduled requests continue with the last known policy and no scheduling-specific 503 is returned.

### Requirement: Administrator policy safety

Policy writes MUST use expected-version compare-and-set, strict JSON without unknown fields, membership validation and range validation. Accounts sharing a cache domain MUST belong to the same platform. A conflict MUST return 409 and the console MUST keep the unsaved draft.

#### Scenario: Concurrent edits
- **WHEN** two administrators save the same group policy from version 3
- **THEN** one save succeeds with version 4 and the other receives 409 without overwriting it.
