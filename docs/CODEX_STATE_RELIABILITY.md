# STATE reliability upgrade

Source baseline: `1a4fd39c71eadc4d5d587a39373bcded692e4fa5` (0.1.4).
Implementation copy: `/xy/artifacts/codex-state-upgrade/reliability-work`.

## Administrator controls

The account toggle saves immediately using PATCH with `enabled` and
`expected_revision`. Model/plan/missing-policy saves omit `enabled`. A conflict
returns HTTP 409 / CODEX_TICKET_CONFLICT. Explicit disabling can retry once after
reading the latest revision; enabling is never automatically replayed. Disabling
invalidates job publication and clears active, standby and pending candidates,
while preserving real cooldown and background-call accounting.

“立即获取” and “全部获取” accept persistent jobs even before renewal is due
or during ordinary failure backoff. A 202 response means accepted; the task state
reports queued, acquiring, verifying, waiting, success, unchanged, failed or
cancelled. Real upstream cooldowns, capacity and budgets remain authoritative.
Temporary proxy selection applies only to that task. Repeated account/model
requests share a running task. Reusing its request ID is idempotent.

Gateway settings contain up to 20 named acquisition proxies. Blank saved-address
fields retain credentials. List edits use a pool revision. Tests use the harvest
transport, fresh SID and configured outer proxy, with a ten-second deadline,
one-MiB response cap and at most three concurrent probes. IP and country are
parsed from one response. A failed current test may display the explicitly
labelled previous successful result. Editing an address discards old results.
Network quarantine is separate from candidate rejection.

## Staged strategy switches

All new strategy switches default to false. Existing account/global switches
remain authoritative. Configure under `gateway.openai_codex_ticket`:

```yaml
unified_probe_enabled: false
standby_enabled: false
adaptive_scheduling_enabled: false
quality_observation_enabled: false
quality_isolation_enabled: false
adaptive_concurrency_account_ids: []
```

Enable controls/diagnostics first. Enable unified request construction for the
controlled comparison; then standby and adaptive scheduling after the baseline
window. Concurrency calibration is scoped by configuration, for example `[265]`,
not by a hard-coded account ID. It caps the configured account limit at five,
halves on a new 429 cooldown, and adds one after thirty minutes without a new 429
when successful traffic resumes.

Adaptive scheduling uses at most three candidate attempts per round, a jittered
20-second interval with the lease released while waiting, and 2/5/10-minute
ordinary automatic backoff. Cluster STATE work is limited to two, with one per
account and IQ lease exclusion. Shared background budgets reserve replay capacity
before harvesting and charge on dispatch. Normal hourly limits are 24 per account
and 16 per model; eight account calls are reserved for recovery. Rescue limits
are 48/32. Enabling quality observation also enables shared accounting.

With standby enabled, each model keeps an active ticket, one verified standby and
one pending candidate. Repeated values keep their original time bounds. Revoked
values cannot be rediscovered into validity. Standby promotion requires at least
ten minutes remaining and fixed-link validation within five minutes; stale
standbys are replayed before promotion. A complete business response may supply a
new pending candidate, but it must pass fixed-link replay before publication.

The independent versioned 12-question quality set covers logic, code, tool JSON
format and context. Three questions run per six-hour cycle, with at most 36 daily
calls per account/model including retries. Incomplete or failed protocol results
are unknown, retaining the last valid full result. A complete but incorrect answer
or invalid requested answer format counts as incorrect. Calibration requires at
least 72 hours and three complete sets. Optional isolation requires two complete
scores at least fifteen minutes apart, each at least three points below baseline;
two complete scores within one point restore eligibility. Isolation affects only
the account/model and never writes its STATE switch.

## Storage and diagnostics

Migration 251 adds independent `codex_ticket_events` (72-hour retention) and
`codex_ticket_hourly` (30-day retention). Event IDs make flush retries idempotent.
Queues are bounded; dropped observations are explicitly counted. Event details
contain IDs, lengths, public request-structure digests and stage outcomes, never
opaque tickets, bearer tokens, proxy credentials or prompts.

Proxy pool and probe health use separate settings records. Pool addresses are
encrypted by the existing secret encryption service. Initial migration prefers
the database scalar, then configuration only when the scalar is absent/empty.
Once the pool exists, an empty list is authoritative. Legacy single-address
updates can replace a single-entry pool atomically; multi-entry pools reject
legacy replacement. Health checks do not advance pool revisions.

Before upgrading or saving the first proxy, configure a persistent
`TOTP_ENCRYPTION_KEY` (64 hexadecimal characters) and use the same key on every
replica and after every restart. Preserve an already configured key: changing it
also affects other credentials encrypted by the existing service. A non-empty
automatically generated process key is not persistent. With that temporary key,
non-empty pool migration, reads and saves return
`CODEX_PROXY_ENCRYPTION_REQUIRED`; failed migration leaves the legacy address
untouched and does not create an authoritative pool. Configure the stable key
and restart before retrying. An empty pool can still be initialized, read or
saved without encryption, and remains authoritative over legacy configuration.

Account runtime stores revision-bound leases, accepted task state, rolling
budget calls, quality progress and bounded renewal samples. Account edits,
imports, duplication and token-refresh paths cannot overwrite these server-owned
fields. New strategies use existing PostgreSQL row/advisory locks and database
time for shared admission and mutations.

## Rollout acceptance and rollback

Local verification records are in
`/xy/artifacts/codex-state-upgrade/reliability-20260920/`; the four delivery roles
remain in `/xy/artifacts/codex-state-upgrade/`. Their final ledger contains literal
commands, stdout/stderr, exit codes, source hashes and transaction observations.
Do not treat a local test pass as the production success-rate experiment.

Production acceptance still requires the approved windows: comparable 24-hour
baseline, 30 acquisition calls per group across three windows, 265's twelve-hour /
200-unique-request concurrency observation, three full renewals and 72-hour
coverage observation. Report account/model sample sizes, confidence intervals,
actual upstream calls and unique new tickets. Include missing-ticket and cooldown
periods in coverage, and missing-ticket blocks in business success rates.

Operational rollback first disables the added strategies and restores a compatible
application build. Preserve real cooldown, revocation and accounting records;
do not restore expired or revoked tickets from a database backup into service.
`ROLLBACK.sh` is a source-copy rollback tool, not a production database rollback.

## Local source handoff

All seven configured Go linters, full unit tests, affected regression tests,
service/parser and real PostgreSQL repository race tests, frontend checks and
embedded application build passed. A full frontend run initially passed 2326 of
2327 tests; the obsolete scalar-proxy settings assertion was migrated to the pool
contract, then 64 affected tests and the final 113-test control/locale set passed.
The macOS-only deployment script requires macOS and was not accepted as passing
on this Linux host. Full command records retain failed and interrupted runs.

The verified implementation is committed locally on `feat/state-reliability` and
imported as a local branch into `/xy/xy2api`; the existing main checkout is retained.
Use `reliability-20260920/COMMIT_RESULT.json` and `LOCAL_BRANCH.json` for the exact
commit and clean-tree verification. No remote push or production rollout is part
of this handoff. The source archive, patch and commit must have the same Git tree.
