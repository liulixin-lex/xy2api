# OpenAI scheduling v3

## ADDED Requirements

### Requirement: Quality ordering remains independent of cost preference
Enabled groups MUST order hard-admitted accounts by quality tier 0/1/2 and place latency-held accounts last inside each quality and compact-capability segment. Priority, capacity band, cost, predicted P90, load and last use MUST follow the design's in-tier ordering. Missing predictions are neutral. Randomization, cache/session preferences and borrowing MUST NOT cross quality or latency boundaries. Disabled groups preserve their existing behavior.

#### Scenario: Held tier 0 versus healthy tier 2
- **WHEN** tier 0 is held and tier 2 is healthy
- **THEN** tier 0 remains ahead of tier 2; a non-held member of tier 0 precedes the held member.

### Requirement: Advanced and shared scheduling honor tiers
Advanced scheduling MUST run existing TopK and weights separately in six pools (tier 0 healthy/held, tier 1 healthy/held, tier 2 healthy/held), including borrowing. Shared scheduling MUST quality-sort placement candidates without adding quality migration or vetoing a valid sticky owner. Quality MUST NOT reduce operations availability or trigger group fallback.

### Requirement: Scoped low-rate preference
The global switch MUST retain its prior prerequisites (advanced off and upstream token cost used). Scope MUST default to `all`; `groups` uses only the listed group IDs and an empty list disables the preference. Settings update, read and runtime caches MUST carry both new fields.

### Requirement: Unified migration retains an exact target
Both legacy and advanced scheduling in enabled groups MUST share one migration implementation. A tier-2 or latency-held owner triggers migration unless protocol ownership, routing ownership or pinned-turn guards prohibit it. Fixed accounts remain subject to latency-triggered migration. The view's `MigrationTarget` is authoritative for quality eligibility; hard admission, no latency hold and fresh capacity are also required.

#### Scenario: Recheck only candidates that can serve
- **WHEN** migration evaluates candidates
- **THEN** it first filters snapshots by quality, latency hold and request compatibility, filters full candidates using one fresh batch load query, then follows the in-tier order for individual DB rechecks and slot acquisition.
- **WHEN** the first candidate fails its DB recheck or slot acquisition
- **THEN** the next candidate is tried; the first candidate passing both becomes the target. Untried and full candidates MUST NOT receive per-candidate DB rechecks. If all fail, the original binding is retained.

#### Scenario: Target owner changes
- **WHEN** the atomic owner-to-target CAS fails/errors
- **THEN** migration stops, its slot is released if held, no cooldown/event is recorded, and migration does not delete the original binding.
- **WHEN** the cache lacks CAS support
- **THEN** migration is abandoned; delete-and-claim is forbidden for both modern and legacy session keys.

### Requirement: Successful migration cooldown
Only successful CAS MUST create a five-minute group/session cooldown in a bounded in-process LRU (16384 keys) and emit Warn `group_quality_gate.migrate` with source, target and trigger. Failure-driven rebinding MUST ignore this cooldown. Recovered old owners only rejoin candidates; healthy new owners MUST NOT proactively migrate back.

### Requirement: OpenAI latency escalation
The slow fraction MUST be 30%, with existing 5-minute EWMA, at least 6 weighted samples and 10-second slow threshold. Re-triggering within 30 minutes after the previous hold ended MUST escalate 3/6/12/15 minutes, capped at 15; later triggers restart at 3. Samples continue during hold and reset on triggering. Expiry only ends ordering demotion. Status returns active models and the latest expiry. Warn start events include hold duration and level. Claude MUST retain 35% and fixed 3-minute holds despite sharing the counting kernel.
