# Group quality gate

## ADDED Requirements

### Requirement: Per-group policy

Each group MUST have an optional policy in the `group_quality_gate_policies` setting, keyed by group ID, with `enabled`, `max_error_rate` (default 0.10), `max_warm_ttft_p90_ms` (default 20000), `fast_warm_ttft_p90_ms` (default 10000) and `pinned_account_ids`.

- A group without an entry MUST behave as disabled, and a disabled group MUST select exactly as before.
- Only OpenAI-platform groups MAY enable the gate. Enabling it on another platform MUST return 400.
- Writes MUST carry `expected_version` and MUST return 409 on mismatch.
- Validation MUST require 0 < error rate ≤ 1, 1000 ≤ fast-tier line < ejection line ≤ 600000 ms, and pinned IDs that are current members.
- The change MUST NOT add a migration or an Ent schema change.

#### Scenario: Concurrent edit

- **WHEN** two administrators save the same group's policy from the same version
- **THEN** the second save returns 409 and the first policy stays stored.

### Requirement: Account-level metrics

Metrics MUST be computed per account and shared by every group. The evaluator MUST query them in a read-only transaction once at startup and then every 60 seconds, and MUST NOT write statistics on the request path. The first round MUST run in the background loop without blocking service assembly; every round MUST have a 30-second overall timeout. Membership MUST be loaded in one query without proxy/group hydration, including disabled OpenAI groups for ordering tiers. Observation history MUST stay bounded to the latest 2 hours and 5 minutes.

- Error rate (15 minutes) MUST be soft failures / (successes + soft failures), judged only with at least 20 calls and 5 soft failures.
  - Successes come from usage records.
  - Soft failures are upstream attempts classified TRANSIENT (first-output timeout, 5xx, stream break, connection error), plus unclassified `request_error` attempts that have no status and were not canceled, deduplicated by request, account and attempt time.
  - 429, balance, auth, request errors, client cancel, capacity shedding and unsupported model MUST NOT count.
- Warm TTFT p90 (30 minutes) MUST use successful streaming requests whose previous request in the same session on the same account was at most 5 minutes earlier, judged only with at least 10 samples. First-output timeouts count only toward the error rate.
- Warm zero-cache rate (2 hours; warm requests with at least 2048 prompt tokens; at least 20 samples) MUST NOT eject. It MUST only affect ordering: above 30%, the account is not in the fast tier.

Decisions for a group and account MUST use only data after the latest of: the window start, the account's `group_quality_reset_at`, the end of the account's last ejection in that group, and the process start. Data from before an ejection MUST NOT count once that ejection has ended, including after a restart.

#### Scenario: Rate limits are not quality failures

- **WHEN** an account returns many 429 responses and no transient failures
- **THEN** its error rate is unaffected and the quality gate does not eject it.

#### Scenario: Cold request excluded

- **WHEN** the first request of a session on an account has a slow first token
- **THEN** that request does not enter the warm TTFT p90.

### Requirement: Ejection, backoff and recovery

For each member of an enabled group, the evaluator MUST eject the account in that group when its error rate or warm TTFT p90 exceeds the group's limit with enough samples.

- Ejection lasts 5, 10, 20, then 30 minutes (cap) for consecutive strikes. The strike count MUST reset after 2 hours without ejection.
- On expiry, only data since the ejection started MUST count:
  - enough new data over a limit MUST eject the account for the next step;
  - enough new samples for every metric that triggered ejection, within 80% of every sample-ready limit, MUST make it healthy;
  - otherwise, including too little new data, the account MUST enter trial.
- A trial account MUST be selectable and MUST be judged only on data since the trial started. Two soft failures within its first 20 requests, or meeting the ejection condition on that data, MUST eject it for the next step; 20 requests MUST promote it. Trial counts MUST accumulate from the trial start, however long the trial lasts. State MUST retain at most the first 20 distinct request IDs and their cumulative soft failures, consuming only intervals after the previous round; history MUST NOT be widened to the trial start.
- Ejection MUST affect only selection in that group and MUST NOT change `schedulable`, `temp_unschedulable` or group membership.

#### Scenario: Repeated ejection backs off

- **WHEN** an account's first ejection expires while it is still over the limit
- **THEN** its second ejection lasts 10 minutes, and a third would last 20 minutes.

#### Scenario: Stale data does not hold a recovered account

- **WHEN** an account ejected for slow first tokens gets no traffic during its 5-minute ejection
- **THEN** at expiry it enters trial instead of being ejected again on the samples from before the ejection.

#### Scenario: Borderline recovery

- **WHEN** an ejection expires and the account's p90 is between 80% and 100% of the limit
- **THEN** the account enters trial and is ejected again if 2 of its first 20 requests are soft failures.

### Requirement: Layered member result, guard and pinned accounts

The evaluator MUST report, per group and member, separate facts instead of one Boolean:

- usable: platform match, enabled, schedulable, free of hard states, and within the group's profit ceiling when its profit gate is on;
- passing: within the limits (healthy or trial), measured on real metrics for pinned accounts too;
- pinned and guard-held flags;
- vetoed: ejected, and neither pinned nor guard-held.

The guard and the request path MUST use them as follows:

- After each evaluation, usable members that are passing or guard-held MUST number at least max(2, ⌈usable members / 2⌉), capped at the number of countable members: usable members other than pinned accounts outside the limits. When the target cannot be met, every countable member MUST stay selectable.
- Below that floor, the evaluator MUST keep the least-bad ejected accounts using severity captured at ejection; ties MUST prefer previous guard holders and then account ID for stable selection. The evaluator MUST log `group_quality_gate.guard_hold`. This MUST also apply to groups that have a fallback group.
- When no other candidate is selectable at request time, the existing last resort MUST relax quality ejection only and log `group_quality_gate.last_resort`. The profit gate, IQ, balance, auth and unsupported-model checks MUST NOT be relaxed.
- Pinned accounts MUST never be vetoed and MUST still obey every other rule. A pinned account outside the limits MUST NOT count toward the floor.

#### Scenario: Small group keeps two accounts

- **WHEN** a group has three usable members and all three exceed the limits
- **THEN** the two least-bad are guard-held and stay selectable, and one is ejected.

#### Scenario: Single usable member

- **WHEN** a group has one usable member and it exceeds the limits
- **THEN** it is guard-held and stays selectable.

#### Scenario: Pinned account outside the limits

- **WHEN** a pinned account exceeds the limits
- **THEN** it stays selectable, shows as pinned and outside the limits, and does not count toward the floor.

### Requirement: Scheduling integration

The quality gate MUST be installed exactly where the profit gate is installed in the OpenAI scheduler:

- text entry points at request start;
- defensive installs in `selectAccountWithScheduler`, `SelectAccountWithLoadAwareness` and `SelectAccountByPreviousResponseID`;
- every WebSocket turn;
- the effective group carried by the selection result, such as the sub-group resolved for a composite group.

Paths exempt from the profit gate MUST also be exempt from the quality gate: dedicated image and video endpoints, media, count_tokens and live. Model catalog lookups (`SelectAccountForModel*`) MUST NOT be gated.

The veto MUST use reason `group_quality_ejected` at every check:

- candidate filtering and in-scheduler final checks;
- legacy eligibility and the DB recheck;
- sticky hits;
- share scheduling: normal candidates, missing-bound candidates and the final verify;
- handler final checks after a slot or wait plan is acquired.

The following rules MUST apply around the veto:

- A final-check veto on an HTTP request MUST release the slot, exclude the account for this request and select again.
- A veto on a WebSocket turn MUST close the connection and ask the client to reconnect. It MUST NOT switch accounts inside the connection.
- Strong continuations MUST be exempt in selection, in the DB recheck and in WebSocket turns. These are previous_response requests, the guardian parent account, and WebSocket turns pinned to their chain (carrying `previous_response_id` or incomplete tool calls, as the existing OpenAI quality routing decides).
- A session bound to a vetoed account MUST be rebound to the newly selected account. Short soft penalties and saturation MUST keep the binding.

#### Scenario: Ejected sticky account

- **WHEN** a session is bound to an account that becomes ejected in its group
- **THEN** the next request selects another account and the binding moves to it.

#### Scenario: Account ejected while waiting for a slot

- **WHEN** a request waited for a slot on an account that was ejected meanwhile
- **THEN** the slot is released and another account is selected for the request.

#### Scenario: WebSocket continuation

- **WHEN** a WebSocket turn is pinned to its chain and its account is ejected
- **THEN** the turn proceeds on that account and the connection stays open.

#### Scenario: WebSocket fresh turn

- **WHEN** a WebSocket turn that is not pinned to a chain finds its account ejected
- **THEN** the connection is closed with a request to reconnect.

### Requirement: Ordering and saturation

With low-rate priority enabled, candidates MUST be ordered by tier, `Account.Priority`, load at or above 80% last, cost (`openAISchedulingRate`), load, least recent use, then ID. With it disabled, the order MUST stay exactly as before.

- Tier 0 holds healthy fast accounts. Fast means:
  - warm TTFT p90 at or below the group's fast-tier line (10000 ms without a policy);
  - and a warm zero-cache rate not above 30%; this condition is ignored with fewer than 20 zero-cache samples.
  - Accounts without TTFT data MUST count as fast. With too few TTFT samples, the last tier MUST be kept for up to 6 hours.
- Tier 1 holds trial accounts, whatever their previous tier. When an ejection ends, the account's remembered tier in that group MUST be cleared; after promotion, too few TTFT samples count as no data, not as the pre-ejection tier.
- Tier 2 holds every other account: slow, non-caching, guard-held, and pinned outside the limits.
- Groups without an enabled gate MUST use only tiers 0 and 2. Without an evaluator, every account is in tier 0.
- Load is (in flight + waiting) ÷ concurrency cap. Accounts with neither a concurrency cap nor a load factor MUST be neutral for the 80% key.
- Every legacy sort point MUST use this one comparator. E2 replacement MUST only reorder within the same tier, cost and load band.

When a sticky account cannot get a slot:

1. The scheduler MUST retry that account's slot for up to 2 seconds, honoring cancellation, without entering the wait queue. A slot gained this way MUST come with exactly one release function.
2. Otherwise it MUST exclude the bound account for this selection and borrow an idle eligible account in the group without changing the binding. All three saturation paths (legacy without load batch, load-aware, advanced) MUST pick the borrowed account in the same session-stable order: the best tier and cost band first, then the account this session last succeeded on, then a per-session hash. Repeated borrows for a session therefore land on the same account.
3. Only when no account has an immediate slot MUST it return the final wait plan for the bound account, as before. The wait queue MUST count that plan once.

Strong continuations MUST keep waiting for their own account.

#### Scenario: Cheap but slow account

- **WHEN** low-rate priority is on, the cheapest account is above the fast-tier line and a pricier account is below it
- **THEN** new sessions go to the pricier fast account.

#### Scenario: Trial account ordering

- **WHEN** low-rate priority is on and a trial account is cheaper than a healthy fast account
- **THEN** new sessions go to the healthy fast account first, and to the trial account before any tier-2 account.

#### Scenario: Non-caching account

- **WHEN** an account's warm zero-cache rate is above 30% with at least 20 samples
- **THEN** it is in tier 2 for new sessions and is not ejected for it.

#### Scenario: Short wait keeps the cache

- **WHEN** the bound account is at its concurrency cap and frees a slot within 2 seconds
- **THEN** the request is served by the bound account.

#### Scenario: Saturated sticky account

- **WHEN** the bound account stays at its cap for 2 seconds and another eligible account is idle
- **THEN** the idle account serves the request and the session stays bound to the original account.

#### Scenario: Low-rate priority off

- **WHEN** low-rate priority is disabled
- **THEN** candidates are ordered exactly as in the previous release.

### Requirement: State and evaluation failure

Gate state MUST live in process memory as an atomic snapshot and MUST be recomputed in the first round after a restart. Freshness MUST use only the latest usage row globally (no rows means no samples), never error-log timestamps. When a query fails or usage data is more than 5 minutes old, the evaluator MUST retain previous metrics and MUST NOT add ejections, including trial failures. Existing ejections MUST still expire into trial and manual resets MUST still apply when members can be loaded. Each stale period MUST log `group_quality_gate.evaluate_failed` only once, with reason and stale duration.

#### Scenario: Metric query times out

- **WHEN** the metric query times out
- **THEN** no new account is ejected and current ejections expire on schedule.

### Requirement: Manual recovery, API and events

- Only the admin "recover state" action (`POST /api/v1/admin/accounts/:id/recover-state`) MAY write `accounts.extra.group_quality_reset_at`. It MUST do so through an explicit recovery option.
  - The successful-test recovery and the OpenAI quota-reset workflow also call the same recovery routine. They MUST NOT write it.
  - The write MUST merge into `extra` without dropping other keys. If it fails after earlier cleanup succeeded, the action MUST return an error, and a retry MUST be safe.
  - A reset timestamp newly appearing or changing during this process MUST clear the account's ejections in all groups, reset its strikes and place it in trial within the next evaluation. Historical resets at or before process start MUST only bound data, never reapply trial after restart.
- "Test" MUST NOT affect the gate, and no buttons are added.
- The admin API MUST provide:
  - `GET /api/v1/admin/group-quality-gate/policies`
  - `PUT /api/v1/admin/group-quality-gate/policies/:group_id`
  - `GET /api/v1/admin/group-quality-gate/status`
- The status endpoint MUST accept an optional `group_id`. Without it, the response MUST contain all enabled groups and metrics for every member of those groups. With it, the response MUST contain only that group, including member metrics when the gate is disabled and `enabled=false`.
- Status arrays MUST be arrays even when empty; account_metrics MUST be an object. Headroom MUST be null if every metric lacks sufficient samples. Summary eligible MUST count all non-vetoed members including trial, pinned and guard-held; trial is a subset. Expired ejection passing MUST use effective metrics. Policy validation errors MUST expose specific messages with the existing reason/metadata structure.
- Manual resets and expiry into trial MUST log trial; trial promotion and direct recovery MUST log recover.
- A successful policy write MUST immediately invalidate this instance's policy cache. A newly enabled group MUST appear in status after the next evaluation round.
- Reads MUST follow account-management permission. Writes MUST follow group-management permission and MUST be audited.
- Events `group_quality_gate.eject`, `.trial`, `.recover`, `.guard_hold`, `.last_resort` and `.evaluate_failed` MUST be logged with group, account, metric, limit and duration.

#### Scenario: Administrator recovers an account

- **WHEN** an administrator recovers an ejected account
- **THEN** within 60 seconds it is in trial in every enabled group, failures before the reset no longer count, and its other `extra` keys are unchanged.

#### Scenario: Successful test

- **WHEN** an administrator tests an ejected account successfully
- **THEN** its runtime errors are cleared as before and its quality gate state is unchanged.
