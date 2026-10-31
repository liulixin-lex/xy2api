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

Metrics MUST be computed per account and shared by every group. The evaluator MUST query them in a read-only transaction once at startup and then every 60 seconds, and MUST NOT write statistics on the request path.

- Error rate (15 minutes) MUST be soft failures / (successes + soft failures), judged only with at least 20 calls and 5 soft failures.
  - Successes come from usage records.
  - Soft failures are upstream attempts classified TRANSIENT (first-output timeout, 5xx, stream break, connection error), plus unclassified `request_error` attempts that have no status and were not canceled, deduplicated by request, account and attempt time.
  - 429, balance, auth, request errors, client cancel, capacity shedding and unsupported model MUST NOT count.
- Warm TTFT p90 (30 minutes) MUST use successful streaming requests whose previous request in the same session on the same account was at most 5 minutes earlier, judged only with at least 10 samples. First-output timeouts count only toward the error rate.
- Warm zero-cache rate (2 hours; warm requests with at least 2048 prompt tokens; at least 20 samples) MUST be display-only.

When an account has `group_quality_reset_at`, only data after that time MUST count.

#### Scenario: Rate limits are not quality failures

- **WHEN** an account returns many 429 responses and no transient failures
- **THEN** its error rate is unaffected and the quality gate does not eject it.

#### Scenario: Cold request excluded

- **WHEN** the first request of a session on an account has a slow first token
- **THEN** that request does not enter the warm TTFT p90.

### Requirement: Ejection, backoff and recovery

For each member of an enabled group, the evaluator MUST eject the account in that group when its error rate or warm TTFT p90 exceeds the group's limit with enough samples.

- Ejection lasts 5, 10, 20, then 30 minutes (cap) for consecutive strikes. The strike count MUST reset after 2 hours without ejection.
- On expiry:
  - an account still over a limit MUST be ejected for the next step;
  - an account with enough samples within 80% of every limit MUST become healthy;
  - any other account MUST enter trial.
- A trial account MUST be selectable. Two soft failures within its first 20 requests, or meeting the ejection condition, MUST eject it for the next step; 20 requests MUST promote it.
- Ejection MUST affect only selection in that group and MUST NOT change `schedulable`, `temp_unschedulable` or group membership.

#### Scenario: Repeated ejection backs off

- **WHEN** an account's first ejection expires while it is still over the limit
- **THEN** its second ejection lasts 10 minutes, and a third would last 20 minutes.

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
- Below that floor, the evaluator MUST keep the least-bad ejected accounts as guard-held and log `group_quality_gate.guard_hold`. This MUST also apply to groups that have a fallback group.
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

With low-rate priority enabled, candidates MUST be ordered by fast tier, `Account.Priority`, cost (`openAISchedulingRate`), load, least recent use, then ID. With it disabled, fast tier and cost MUST NOT take part.

- Fast tier means warm TTFT p90 at or below the group's fast-tier line (10000 ms without a policy). Accounts without data MUST count as fast. With too few samples, the last tier MUST be kept for up to 6 hours.
- Every legacy sort point MUST use this one comparator. E2 replacement MUST only reorder within the same fast tier, cost and load band.
- When a sticky account cannot get a slot, the request MUST borrow an idle eligible account in the group without changing the binding. It MUST wait for the best account only when no account has an immediate slot. Strong continuations MUST keep waiting for their own account.

#### Scenario: Cheap but slow account

- **WHEN** low-rate priority is on, the cheapest account is above the fast-tier line and a pricier account is below it
- **THEN** new sessions go to the pricier fast account.

#### Scenario: Saturated sticky account

- **WHEN** the bound account is at its concurrency cap and another eligible account is idle
- **THEN** the idle account serves the request and the session stays bound to the original account.

#### Scenario: Low-rate priority off

- **WHEN** low-rate priority is disabled
- **THEN** candidates are ordered exactly as in the previous release.

### Requirement: State and evaluation failure

Gate state MUST live in process memory as an atomic snapshot and MUST be recomputed in the first round after a restart. When a query fails or the data is more than 5 minutes old, the evaluator MUST keep the previous state, MUST NOT add ejections and MUST log `group_quality_gate.evaluate_failed`. Existing ejections MUST still expire on time.

#### Scenario: Metric query times out

- **WHEN** the metric query times out
- **THEN** no new account is ejected and current ejections expire on schedule.

### Requirement: Manual recovery, API and events

- Only the admin "recover state" action (`POST /api/v1/admin/accounts/:id/recover-state`) MAY write `accounts.extra.group_quality_reset_at`. It MUST do so through an explicit recovery option.
  - The successful-test recovery and the OpenAI quota-reset workflow also call the same recovery routine. They MUST NOT write it.
  - The write MUST merge into `extra` without dropping other keys. If it fails after earlier cleanup succeeded, the action MUST return an error, and a retry MUST be safe.
  - Within the next evaluation, the account's ejections in all groups MUST be cleared, its strikes reset and the account placed in trial.
- "Test" MUST NOT affect the gate, and no buttons are added.
- The admin API MUST provide:
  - `GET /api/v1/admin/group-quality-gate/policies`
  - `PUT /api/v1/admin/group-quality-gate/policies/:group_id`
  - `GET /api/v1/admin/group-quality-gate/status`
- Reads MUST follow account-management permission. Writes MUST follow group-management permission and MUST be audited.
- Events `group_quality_gate.eject`, `.trial`, `.recover`, `.guard_hold`, `.last_resort` and `.evaluate_failed` MUST be logged with group, account, metric, limit and duration.

#### Scenario: Administrator recovers an account

- **WHEN** an administrator recovers an ejected account
- **THEN** within 60 seconds it is in trial in every enabled group, failures before the reset no longer count, and its other `extra` keys are unchanged.

#### Scenario: Successful test

- **WHEN** an administrator tests an ejected account successfully
- **THEN** its runtime errors are cleared as before and its quality gate state is unchanged.
