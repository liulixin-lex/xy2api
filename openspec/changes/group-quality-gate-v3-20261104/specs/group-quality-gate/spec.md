# Group quality gate v3 specification

## Policy compatibility

The versioned `group_quality_gate_policies` setting MUST store v3 in an optional
per-group subobject. Absent v3 MUST inherit only legacy enabled and use
15 seconds, 10% errors, 50% cache retention and non-strict defaults. A present
subobject MUST retain explicit false and validate 5–60 integer seconds,
0.05–0.50 error rate and 0.50–0.70 cache floor. PUT MUST require v3, ignore all
submitted legacy values, preserve stored legacy JSON fields, and keep CAS
version conflict behavior. GET MUST expose effective v3 without writing it.
A newly v3-only group MUST remain disabled when read by the 0.5.1 parser.

## Observations and statistics

Only OpenAI accounts contribute. Every window MUST be truncated at process
startup and the contributing account's manual reset. Successful usage means
positive actual cost OR positive output tokens. Placeholders participate in
LAG but do not count as successes. Strict continuation requires the same
API key, service group, nonempty session, adjacent account and model, with an
interval at most five minutes; account filters MUST NOT precede LAG.

Speed samples MUST use only positive attempt_first_output_ms, with no fallback
or imputation for zero, negative or null values. Any
failure on the same account/request excludes its speed sample, including a
first-output cutoff. Other-account failures do not exclude the rescued usage.
Cache retention and group composition MUST use eligible usage rows regardless of
matching failure events; only B1 and GM30 exclude failed speed samples.
Empty request IDs cannot be aligned. Same-account retry success can therefore
be conservatively undercounted because persisted final attempt IDs are absent.

B1 MUST use two hours, account medians followed by their median, at least three
accounts and 30 samples per cell. Cells are model × effort × prompt band ×
cache retention, degrading to no cache split, paired adjacent bands (0/1 and
2/3), then all bands. GM30 MUST use 30 minutes, at most 50 samples and three
per API-key/group/session, at least ten samples and three sessions, and the
geometric mean of ratios capped at four. Group composition MUST match current
B1 before taking its latest 100 strict continuations within six hours, need
50 samples, and use their expected-value P90. Groups MUST NOT borrow another
group's composition. Prediction MUST equal GM30 × group composition P90.

Errors MUST preserve soft-failure classification except first-output cutoffs,
deduplicate by account/request/event time, suppress same-account same-request
usage when a soft failure exists, then take the latest 100 events in 30
minutes. Empty IDs count independently. At least 20 events are required;
exceeding the ceiling requires at least five failures. Error Wilson intervals
are display-only. Recent health MUST retain its existing cutoff classification.

Cache MUST take at most 40 strict successful continuations in 30 minutes,
require current and previous prompts at least 2048 and current at least half
previous, and need 20 samples. Retention is reading at least half the previous
prompt. Failure is Wilson upper below the configured floor; passing is point
estimate at or above it; the intervening range is neutral.

## State and ordering

Each account/group/dimension MUST receive at most one vote per minute. Two
consecutive aligned votes establish pass or fail. Neutral votes break the
streak without changing established state. Missing evidence sets none and
breaks the streak. Recovery MUST use 90% of the speed limit, 80% of the error
limit and the cache floor. Bad memory MUST be dimension-specific, last two
hours after last fail, and clear only after two passing votes in that dimension.

Current fail yields tier 2. Otherwise unknown with bad memory, or any unknown
in strict mode, yields tier 1. All others yield tier 0. Pinned accounts MUST
remain tier 0 and display pinned plus actual dimension states. Disabled groups
MUST return tier 0 and MUST NOT produce evaluation tiers. Unknown MUST NOT be
represented as passing. Stale usage beyond five minutes or query failure MUST
freeze automatic state and memory; one failure event is emitted per stale
period. Explicit manual reset still clears state and truncates history.
Departure state MUST survive for two hours after departure; restart clears it.

## Views, migration targets and telemetry

Status JSON MUST conform to design §5.3, including null unavailable values,
empty arrays, independent dimension sample counts, bad-until times, group
composition and disjoint summaries. A live lookup supplies current latency
holds; absent or expired holds MUST be null. Profit preview MUST retain its
response shape and existing meaning: passing counts schedulable members that
pass the profit gate, independently of quality-gate states or pinned status.

ExpectedP90Ms MUST expose the available prediction to scheduling. Migration
Target MUST reject any current failing dimension, including pinned members.
Ordinary targets require valid prediction ≤90% X and no error/cache current
failure or bad memory. Strict targets additionally require valid error/cache
statistics within 80% Y and at least Z; two established pass votes are not an
additional prerequisite. Scheduling checks capacity and hard admission.

Once a prior member snapshot exists, every tier change MUST emit one Warn
group_quality_gate.tier with account, group, previous/new tier and reason
dimension/value. The first evaluation MUST NOT emit a tier-change event.
Warn migrate, last_resort and evaluate_failed remain. The former hard-exclusion
state machine, backoff, observation period, fast-tier memory and floor guard
MUST be removed.

Insufficient group composition and absence of qualified migration targets have
no direct counters in this release. Acceptance MUST estimate the former offline
from usage_logs using the e_G rules, and approximate the latter from tier and
latency-hold-start events. These approximations MUST NOT be presented as exact
online target availability measurements.
