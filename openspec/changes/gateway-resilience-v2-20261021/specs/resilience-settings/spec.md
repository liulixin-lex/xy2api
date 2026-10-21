# 新开关与回退语义（0.4.4）

## ADDED Requirements

### Requirement: New switches default on and depend on the master switch

The settings MUST provide `stream.prelude_staging`, `stream.native_prelude_whitelist`, `stream.strict_terminal`, `failure.decision_v2`, `errors.failed_frame_retry_after`, `errors.codex_retry_after_cap_seconds` (0–30, default 10, 0 disables), `capability.stream_only_learning` and `capability.learn_ttl_seconds` (600–86400, default 21600). Every boolean defaults to on. Each new behaviour MUST take effect only when the master switch and, where design §1 lists one, its section switch are on. Changes MUST take effect without restart.

#### Scenario: Old settings document
- **WHEN** the stored settings document was written by 0.4.3 and has none of the new fields
- **THEN** every new field takes its default and the document stays valid.

#### Scenario: Out-of-range value
- **WHEN** an administrator saves `errors.codex_retry_after_cap_seconds` = 45
- **THEN** the save is rejected and the current settings stay in effect.

### Requirement: Rollback semantics

Turning the master switch off MUST restore 0.3.0 behaviour for all resilience features including the new ones; fixes listed in design §9 stay in effect. Turning off one new switch MUST restore the 0.4.3 / #117 behaviour of that feature only. Rolling back the binary to 0.4.3 MUST NOT require any database change and MUST leave the 0.4.3 settings valid.

#### Scenario: Master switch off
- **WHEN** the master switch is off
- **THEN** compatibility conversion writes its opening on `response.created` and stream-only learning neither learns nor filters.

#### Scenario: Binary rollback
- **WHEN** a deployment saved settings with the new fields and is rolled back to 0.4.3
- **THEN** 0.4.3 starts with valid resilience settings and behaves as 0.4.3.
