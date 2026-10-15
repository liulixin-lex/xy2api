# Availability fallback group

## ADDED Requirements

### Requirement: Group-level availability fallback

A group MAY reference another group in `availability_fallback_group_id`. This field MUST be independent of the existing `fallback_group_id`, which only handles Claude-Code-only routing. Saving MUST reject:
- self references;
- cycles;
- fallback groups on an incompatible platform.

#### Scenario: Cycle rejected
- **WHEN** an administrator sets group A's fallback to B while B already falls back to A
- **THEN** the save fails with a validation error.

### Requirement: Trigger only when the primary group has no available account

The fallback group MUST be used only when account selection in the primary group yields no available account, either because every account is hard-penalised or absent, or because only soft-penalised accounts remain. Running out of attempts during failover MUST NOT trigger the fallback. Each request MUST fall back at most once, without chaining. The fallback group's own eligibility rules MUST apply:
- Claude-Code-only;
- model allow-list;
- profit control;
- concurrency;
- RPM.

#### Scenario: Primary has only soft-penalised accounts
- **WHEN** every account in the primary group is in a transient cooldown and a fallback group with a healthy account exists
- **THEN** the request is served by the fallback group.

#### Scenario: Failover exhaustion does not cross groups
- **WHEN** a request has already used its attempt budget in the primary group
- **THEN** it ends with the primary group's error and is not retried in the fallback group.

### Requirement: Billing and visibility

A request served by the fallback group MUST be billed at the fallback group's rates. `usage_logs.group_id` MUST hold the fallback group, and `usage_logs.origin_group_id` MUST hold the primary group. The user's usage list MUST show that fallback occurred, with both group names. The group pages MUST show the fallback relationship and rate.

#### Scenario: User sees fallback pricing
- **WHEN** a user's request is served by the fallback group
- **THEN** the usage record shows "fallback: <primary> → <fallback>" with the fallback group's rate.

### Requirement: Return to the primary group

Every request MUST evaluate the primary group first. Once the primary group has at least one account with no hard or soft penalty, continuously for `fallback_group.return_stable_seconds` (default 120 s), new requests and the next request of every session MUST be placed in the primary group again, without waiting for caches to cool. Bindings inside the fallback group MUST use a TTL of `fallback_group.binding_ttl_seconds` (default 600 s).

#### Scenario: Primary recovers
- **WHEN** a session has been served by the fallback group and the primary group has been healthy for 120 s
- **THEN** the session's next request is placed in the primary group and recorded as `fallback_return`.

#### Scenario: Flapping primary
- **WHEN** the primary group becomes healthy for 30 s and then fails again
- **THEN** sessions remain in the fallback group.
