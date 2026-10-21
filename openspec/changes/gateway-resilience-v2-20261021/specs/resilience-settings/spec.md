# 新开关与回退语义（0.4.4）

## ADDED Requirements

### Requirement: New switches default on and depend on the master switch

The settings MUST provide `stream.prelude_staging`, `stream.native_prelude_whitelist`, `stream.strict_terminal`, `failure.decision_v2`, `errors.failed_frame_retry_after`, `errors.codex_retry_after_cap_seconds` (0–30, default 10, 0 disables), `capability.stream_only_learning` and `capability.learn_ttl_seconds` (600–86400, default 21600). Every boolean defaults to on. The new fields MUST be stored in the existing `gateway_resilience_settings` document; no new settings key or migration is added. Each new behaviour MUST take effect only when the master switch is on; `errors.failed_frame_retry_after` and `errors.codex_retry_after_cap_seconds` MUST also require `errors.enabled`. Switching to another account after a stream-only rejection MUST require both `capability.stream_only_learning` and `failure.decision_v2`. Changes MUST take effect without restart on the saving instance and within 10 seconds on other instances.

#### Scenario: Old settings document
- **WHEN** the stored settings document was written by 0.4.3 and has none of the new fields
- **THEN** every new field takes its default and the document stays valid.

#### Scenario: Out-of-range value
- **WHEN** an administrator saves `errors.codex_retry_after_cap_seconds` = 45
- **THEN** the save is rejected with `GATEWAY_RESILIENCE_SETTINGS_INVALID` and the current settings stay in effect.

#### Scenario: Errors section off
- **WHEN** `errors.enabled` is false and `errors.failed_frame_retry_after` is true
- **THEN** synthesized `response.failed` frames carry no `error.headers` and the Codex cap does not apply.

### Requirement: Rollback semantics

Turning the master switch off MUST restore 0.3.0 behaviour for all resilience features including the new ones; fixes listed in design §9 stay in effect. Turning off one new switch MUST restore the 0.4.3 / #117 behaviour of that feature only. Rolling back the binary to 0.4.3 MUST NOT require any database change: 0.4.3 MUST read a document saved by 0.4.4 as valid, ignoring the new fields and keeping every existing field. Saving the settings on 0.4.3 drops the new fields; a later upgrade MUST read them as their defaults.

#### Scenario: Master switch off
- **WHEN** the master switch is off
- **THEN** compatibility conversion writes its opening on `response.created` and stream-only learning neither learns nor filters.

#### Scenario: Binary rollback
- **WHEN** a deployment saved settings with the new fields and is rolled back to 0.4.3
- **THEN** 0.4.3 starts with valid resilience settings, keeps every existing value and behaves as 0.4.3.

#### Scenario: Saved on 0.4.3, upgraded again
- **WHEN** an administrator turned `stream.prelude_staging` off on 0.4.4, rolled back to 0.4.3, saved the settings there, and upgraded to 0.4.4 again
- **THEN** `stream.prelude_staging` is on again (default) and every other new field has its default.

#### Scenario: Old admin page after rollback
- **WHEN** after a rollback to 0.4.3 a browser still showing the 0.4.4 admin page saves the settings with the new fields
- **THEN** 0.4.3 rejects the save as unknown fields and the stored settings do not change.

#### Scenario: Learned capability while switched off
- **WHEN** account A was learned as stream-only, `capability.stream_only_learning` is turned off and on again before the mark expires
- **THEN** the mark filters non-streaming requests again until its original expiry.
