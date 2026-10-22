# 仅流式能力学习（0.4.5）

## ADDED Requirements

### Requirement: Capability reason is named only when it is the cause

A no-account or exhausted error MUST append the `(capability_stream_only)` reason only when every failed attempt of the request was a stream-only capability exclusion, the same rule as the capability-only exhaustion of 0.4.4. The client-visible status, message and retry headers MUST NOT change.

#### Scenario: Mixed failures
- **WHEN** one attempt was excluded as stream-only and another failed with a transient upstream error
- **THEN** the operations record and log do not append `(capability_stream_only)`.

#### Scenario: Capability only
- **WHEN** every failed attempt of the request was a stream-only exclusion
- **THEN** the reason `(capability_stream_only)` is appended.
