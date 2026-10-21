# 上游失败分类修正（0.4.4）

## ADDED Requirements

### Requirement: Only real capacity signals are capacity shedding

With `failure.decision_v2` on, a failure MUST be classified `CAPACITY_SHED` only for `server_is_overloaded`, `slow_down`, 529, a structured overloaded error, or 503 with overloaded wording (the 0.4.3 exception for CDN error pages stays). A request-scoped transient failure MUST NOT be promoted to `CAPACITY_SHED` on that ground alone. The operations record (`resilience_category`) and the handler's switching rules MUST use the same classification. With the switch off, the 0.4.3 classification applies.

#### Scenario: Grok idle is not capacity
- **WHEN** a Grok stream goes idle before output on account A and another OAuth account B on the same host can serve
- **THEN** the failure is classified `TRANSIENT`, the request may switch to B, and A's host is not excluded as for capacity.

#### Scenario: Raw 429 is rate limit
- **WHEN** a raw Chat or Grok upstream returns a retryable 429 without balance or reset evidence
- **THEN** the failure is `RATE_LIMIT`, not `CAPACITY_SHED`.

#### Scenario: Busy wording switches to any host
- **WHEN** an upstream returns a 5xx with "The service is busy", "Too many pending requests" or "Concurrency limit exceeded for account"
- **THEN** the failure is `TRANSIENT` (or `RATE_LIMIT` for 429) and the request may switch to an account on the same host.

#### Scenario: Stronger evidence wins
- **WHEN** a 429 body says the upstream balance is insufficient
- **THEN** the failure is `BALANCE`.

#### Scenario: Overloaded keeps capacity rules
- **WHEN** an upstream returns 503 `server_is_overloaded`
- **THEN** the failure is `CAPACITY_SHED` and D18 applies unchanged: one same-account retry, then at most one switch to another host.

#### Scenario: Switch off
- **WHEN** `failure.decision_v2` is off and a Grok stream goes idle
- **THEN** the failure is classified as in 0.4.3.

### Requirement: Typed first-output timeout is distinguishable

A typed first-output timeout MUST keep the category `TRANSIENT` in the operations record for compatibility, and MUST also record that it was a first-output timeout and the timeout that actually applied, in seconds.

#### Scenario: Claude first-event timeout
- **WHEN** a Claude request hits the Anthropic first-event timeout and switches accounts
- **THEN** the operations record shows `TRANSIENT`, a first-output-timeout reason and the applied timeout.
