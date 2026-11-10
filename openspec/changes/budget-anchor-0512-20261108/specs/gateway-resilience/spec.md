# Gateway resilience delta

## ADDED Requirements

### Requirement: Timer-bound budget-cut attribution

Each first-output timer MUST retain the immutable TimeoutPlan created for that timer. would MUST indicate a remaining-budget cap. cut_after_full_tier MUST compare remaining_at_plan≥tier_eff, including equality. applied MUST be would && !cut_after_full_tier && attribution=enforce. Paths without a positive tier MUST have applied=false. observe MUST always have applied=false; off MUST omit cut attribution.

#### Scenario: Tier boundary
- GIVEN remaining is tier−1 ms, tier, tier+1 ms and would=true
- THEN enforce applied MUST be true, false, false respectively, for both last and non-last candidates.
- THEN observe applied MUST be false in all cases.

### Requirement: Applied supplements planned penalties

The existing planned/Reason/profile decisions MUST remain unchanged. Applied MUST additionally skip OpenAI slow-streak advancement and latency-hold sampling, and MUST exclude the event before recent-health DISTINCT ON and share GROUP BY/LIMIT. gate v3 includeAll inputs and kind/category MUST remain unchanged.

#### Scenario: Applied but not planned
- GIVEN an applied FOT whose current profile does not match
- THEN HandleStreamTimeout, API-key health failure and OpenAI SessionFailure MUST still follow existing non-planned behavior.
- THEN advanced scheduler failure reporting MUST remain active. Gemini breaker and Anthropic/Gemini SessionFailure MUST remain unchanged.

### Requirement: Late attempts are observable

Late attempts MUST mean start>T0+base−2 s. In body_eof mode, no-timer attempts and attempts whose timer coverage cannot be determined MUST use the arrival upper bound for admission and waits; attempts with an installed timer MUST continue using D. No-timer attempts MUST have a separate request JSON count. Undelivered pre-output requests with a positive count MUST emit resilience.late_attempt_undelivered at Warn with key, group, stage and elapsed values. G9 MAY pair that Warn with abuse_guard by key and UTC hour for observation, but MUST NOT automatically restore arrival and is not a hard precondition for enabling body_eof; no new migration is required.
