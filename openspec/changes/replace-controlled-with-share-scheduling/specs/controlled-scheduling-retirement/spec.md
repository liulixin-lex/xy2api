# 智能可控调度退役规格

## REMOVED Requirements

### Requirement: Controlled scheduling mode
**Reason**: The controlled allocator, health state machine, attempt ledger, dispatch tickets, failure domains and pause controls are replaced by share scheduling and the original Sub2API behavior.
**Migration**: Saved group priorities and weights are imported as disabled share drafts; administrators enable share scheduling per group after review.

### Requirement: System scheduling mode switch (`sub2api` / `controlled`)
**Reason**: The scheduler is now selected per group with a global share switch; the system mode table is no longer read.
**Migration**: Every group starts on the Sub2API scheduler after upgrade.

## ADDED Requirements

### Requirement: No scheduling reads on the gateway hot path

Gateway routes MUST NOT read a scheduling mode or controlled policy from PostgreSQL per request, and MUST NOT reject requests because a scheduling mode cannot be read.

#### Scenario: Request without mode table access
- **WHEN** a gateway request is processed in a group using the Sub2API scheduler
- **THEN** no query against `scheduling_system_mode`, `scheduling_group_policies` or `scheduling_attempts` is executed.

### Requirement: Original Sub2API semantics restored

Removing controlled branches MUST restore the original Sub2API selection, sticky session, cooldown, same-account retry and failover semantics. Non-controlled XY2API fixes MUST remain, including terminal-send recursion protection, non-stream read fallback, account-model cooldown, the account `updated_at` monotonic trigger with versioned scheduler cache writes, and the strict one-click schedulable switch.

#### Scenario: Scheduling file drift check
- **WHEN** CI compares the Sub2API scheduling files with the pinned upstream tag
- **THEN** only registered XY2API blocks differ.

### Requirement: Retired administrator endpoints

The former `/api/v1/admin/scheduling/*` endpoints and account scheduling-control and failure-domain endpoints MUST return `410 Gone` with a pointer to the share scheduling API for one release, and MUST NOT read or modify retired tables.

#### Scenario: Old console call
- **WHEN** an outdated console calls `PUT /api/v1/admin/scheduling/groups/18`
- **THEN** the response is 410 and no table is modified.

### Requirement: Billing compatibility

Usage billing intents written before the upgrade that contain `SchedulingAttemptID` MUST still be recoverable and settled exactly once. New intents MUST NOT require scheduling acknowledgement.

#### Scenario: Pre-upgrade intent recovery
- **WHEN** the recovery worker processes an intent created by version 0.2.5 with a scheduling attempt identifier
- **THEN** the usage is settled once and the identifier is ignored.

### Requirement: Data retention and rollback

Published migrations MUST remain byte-identical. Controlled tables MUST remain untouched for at least one release so that rolling back to 0.2.5 with a database backup restores its behavior. Dropping them MUST use a later, separate migration after export.

#### Scenario: Rollback rehearsal
- **WHEN** an isolated installation upgrades from 0.2.5, then restores its backup and runs 0.2.5 again
- **THEN** health, login and account selection succeed with the previous scheduling mode.
