# 分组质量门槛第二版

These requirements supersede the matching clauses of `group-quality-gate-20261031` (warm TTFT source, ordering tiers, trial, the gated-group exemption from latency-hold migration, the retention of departed members, and event levels). Every other requirement of that change stays in force.

## ADDED Requirements

### Requirement: Warm first-output metric

The warm TTFT p90 used for ejection, ordering tiers, recovery and the status API MUST be computed from each warm sample's user-visible first output: `end_to_end_first_output_ms` when it is greater than 0, otherwise `first_token_ms`.

- Sample selection MUST stay unchanged: successful streaming requests whose previous request in the same session on the same account was at most 5 minutes earlier, a 30-minute window, and at least 10 samples.
- The observation field MUST be named for what it holds (`FirstOutputMs`, SQL alias `first_output_ms`).
- `openai_ttft_mode` and the `first_token_ms` written to usage records MUST NOT change.
- Policy fields and their JSON names (`max_warm_ttft_p90_ms`, `fast_warm_ttft_p90_ms`) MUST stay; they now apply to the user-visible first output.

#### Scenario: Relay keepalive

- **WHEN** a relay sends keepalive frames after 2 s and its first content after 14 s on warm streams
- **THEN** the gate's warm p90 reflects about 14 s, while usage records keep showing the semantic first token.

#### Scenario: Sample without an end-to-end value

- **WHEN** a warm sample has `end_to_end_first_output_ms` equal to 0, as on WebSocket
- **THEN** its `first_token_ms` is used.

### Requirement: Two ordering tiers with a group-relative fast line

With low-rate priority enabled, every OpenAI group, whether or not its gate is enabled, MUST order candidates by two tiers: tier 0 (fast) and tier 2 (other). Tier 1 (trial) MUST NOT be assigned any more; the API `tier` field MUST only carry 0 or 2.

- **Best**: in each evaluation round and group, `best` MUST be the minimum warm p90 among members that are usable, not ejected, and have at least 10 warm samples.
- **Effective fast line**: min(policy fast line, `best` + 4000 ms), or the policy fast line when `best` is undefined.
- **Tier 0 by data**: an account with at least 10 warm samples, a warm p90 at or below the effective fast line, and no zero-cache condition (at least 20 samples and a rate above 30%). An account without such data can be tier 0 only through the tier memory below.
- **Tier memory**: the evaluator MUST keep, per group and member, the last data-based tier (`DataTier`), when it was decided (`TierAt`) and the last time it was tier 0 (`FastAt`).
  - A tier is data-based with at least 10 warm samples or when the zero-cache condition applies, whatever the ejection state. A round that reuses metrics because the data is stale MUST NOT refresh the memory. The round that ejects an account MUST still record its data-based tier first (tier 2 for a warm TTFT ejection); the ejection then overrides only the current tier.
  - An account without such data MUST order by `DataTier` if `TierAt` is at most 30 minutes old; otherwise as tier 0 if `FastAt` is at most 6 hours old, so that it is measured again; otherwise as tier 2.
  - Ejection, expiry, manual reset, trial end and gate disable MUST NOT clear this memory. A member that leaves the group keeps it until two hours after its last ejection end or data-based decision, unless a manual reset happens meanwhile; a process restart clears it.
  - The current tier (used for ordering, the guard and migration targets) is separate from `DataTier`: ejection, guard hold and non-passing override only the current tier. The zero-cache condition is data-based and records `DataTier` 2.
- **Tier 2 regardless of samples**: ejected accounts (vetoed or guard-held), non-passing accounts and zero-cache accounts, as before. An ejection that has expired but not yet been re-evaluated MUST also order as tier 2.
- **Unchanged**: within a tier, the comparator (priority, ≥80% load last, cost, load, recent use, ID). With low-rate priority off, the order MUST stay exactly as before.
- **Status**: each group MUST expose its effective fast line as `effective_fast_warm_ttft_p90_ms`, null when `best` is undefined.

#### Scenario: Relative fast line

- **WHEN** a group's best warm p90 is 3.3 s and its fast line is 10 s
- **THEN** an account at 8 s is in tier 2 and an account at 6 s is in tier 0.

#### Scenario: Account without data

- **WHEN** an account has no data-based decision in the last 30 minutes and was not tier 0 in the last 6 hours
- **THEN** it is in tier 2 and ordered by cost among the tier-2 accounts.

#### Scenario: Night-time memory

- **WHEN** a tier-0 account drops to 4 warm samples in the window
- **THEN** it keeps its last data-based tier for 30 minutes and afterwards stays in tier 0 until 6 hours after its last tier-0 decision.

#### Scenario: Fast account after an error ejection

- **WHEN** the cheapest tier-0 account is ejected for errors and its ejection expires with no new data
- **THEN** it orders as tier 0, receives new sessions, and two soft failures among its first 20 requests eject it again.

#### Scenario: Fast account drained by migration

- **WHEN** an account's warm p90 exceeded the fast line, its sessions migrated away and it has no data since
- **THEN** it orders as tier 2 for 30 minutes after that decision and then as tier 0 again, while an account that was never tier 0 in the last 6 hours stays in tier 2.

#### Scenario: Warm TTFT ejection

- **WHEN** an account is ejected for warm TTFT with at least 10 samples in that round
- **THEN** its `DataTier` becomes 2, and after the ejection expires it orders as tier 2 until 30 minutes after that decision.

#### Scenario: Group without data

- **WHEN** no member of a group has 10 warm samples and none has a tier memory
- **THEN** all members are in tier 2 and are ordered by the unchanged comparator.

### Requirement: Bounded observation after ejection or manual reset

The trial state MUST become a bounded observation that does not change ordering.

- **Entry is unchanged**:
  - an ejection that expires without enough new data, or within the limits but not recovered directly, enters trial;
  - a manual reset clears the account's ejections in all groups, resets strikes, bounds data at the reset time and enters trial.
- A trial account MUST be selectable and MUST be ordered by the two-tier rule.
- Two soft failures within its first 20 requests, or meeting the ejection condition on data since the trial start, MUST eject it for the next step, as before.
- The trial MUST end as healthy when 20 distinct requests have been counted or 30 minutes have passed since the trial start, whichever comes first.
- The 30-minute end MUST apply even while data is stale; stale data MUST still not add ejections.
- The status `until` of a trial member MUST be the trial start plus 30 minutes.
- Ending a trial MUST log `group_quality_gate.recover` with reason `trial_passed` (20 requests) or `trial_elapsed` (30 minutes).

#### Scenario: No traffic after expiry

- **WHEN** an ejection expires and the account receives no requests afterwards
- **THEN** it is ordered by the two-tier rule during the observation and becomes healthy 30 minutes after the ejection end.

#### Scenario: Manual recovery

- **WHEN** an administrator recovers an ejected account
- **THEN** within 60 seconds it is selectable in every enabled group with strikes reset, failures before the reset no longer count, and two soft failures among its first 20 requests eject it again for 5 minutes.

### Requirement: Recovery entry in the account menu

The account action menu MUST offer the existing "recover state" action when the account is ejected, guard-held or in trial in any group whose quality status has been loaded on the page, in addition to the existing runtime states. No new button or endpoint is added.

#### Scenario: Gate-only state

- **WHEN** an active, schedulable account is in trial in one group and has no other runtime state
- **THEN** its action menu shows "recover state", and using it calls `POST /api/v1/admin/accounts/:id/recover-state`.

### Requirement: Gated-group session migration

In a group whose gate is enabled, the scheduler MUST remove a session's binding before sticky selection, so that normal selection runs, when all of the following hold:

- **Bound account**: a member of the group that is not ejected and not pinned, with at least 10 warm samples and a warm p90 above the group's policy fast line (not the effective fast line).
- **Target**: at least one other member that is in tier 0 in this group, is not vetoed, is eligible for this request (existing eligibility, channel restriction, latency hold, parent health, runtime block and profit-gate checks) and has a free slot by real-time load.
- **Request**: not a strong continuation (`previous_response_id`, Codex sub-agent parent-thread affinity, or a WebSocket turn pinned to its chain); session failover rebinding is on; the session is not owned by OpenAI quality routing; the group is not under share scheduling; and low-rate priority is in effect for this selection, since only then do the tiers steer normal selection to tier 0.

The check MUST run at the call site of the 0.4.9 migration, in the original load-aware selection.

Further rules:
- The decision MUST NOT depend on the 0.4.9 latency-hold state.
- A migration MUST log `group_quality_gate.migrate` at Warn with `group_id`, `account_id`, `warm_ttft_p90_ms` and `fast_warm_ttft_p90_ms`.
- No cooldown or per-session state is added.
- Groups without an enabled gate MUST keep the 0.4.9 latency-hold migration unchanged.

#### Scenario: Slow bound account

- **WHEN** a session is bound to an account with a warm p90 of 14 s, the fast line is 10 s, and a tier-0 member has a free slot
- **THEN** the binding is removed, the request is served by a tier-0 member, and `group_quality_gate.migrate` is logged.

#### Scenario: Between the two lines

- **WHEN** the bound account's warm p90 is 8 s, the effective fast line is 7.3 s and the fast line is 10 s
- **THEN** the binding is kept, while new sessions prefer tier-0 accounts.

#### Scenario: No fast capacity

- **WHEN** every tier-0 member is at its concurrency cap
- **THEN** the binding is kept.

#### Scenario: Strong continuation

- **WHEN** the request carries `previous_response_id`
- **THEN** the session is not migrated.

### Requirement: Gate event levels

`group_quality_gate.guard_hold`, `group_quality_gate.last_resort` and `group_quality_gate.migrate` MUST be logged at Warn so that they are stored in the operations system log. Other gate events MUST keep their levels and fields.

#### Scenario: Guard hold is stored

- **WHEN** the guard keeps an ejected account selectable
- **THEN** a `group_quality_gate.guard_hold` record appears in the operations system log.
