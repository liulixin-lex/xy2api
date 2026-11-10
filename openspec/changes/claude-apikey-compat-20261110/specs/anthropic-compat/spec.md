## ADDED Requirements

### Requirement: Explicit account compatibility

The gateway MUST enable Claude Code compatibility only for explicitly configured Anthropic API Key accounts and MUST retain existing behavior when disabled.

#### Scenario: Old account editor saves extra
- WHEN an update omits the profile, including an empty extra object
- THEN the existing profile and its managed identity remain unchanged
- AND an explicit off disables compatibility without changing unrelated extra semantics.

#### Scenario: Conflicting settings
- WHEN compatibility is combined with passthrough, invalid account type or contradictory controlled headers
- THEN configuration is rejected with a specific reason.

#### Scenario: Bulk updates with stale account IDs
- WHEN a generic credential or extra patch includes missing account IDs
- THEN existing bulk update semantics are preserved and loaded compatibility accounts still validate the merged configuration
- AND an explicit profile patch requires every requested account to exist before writing.

### Requirement: Preserve caller semantics

The adapter MUST preserve business system role, tool definitions and pairing, message order and explicitly supplied generation parameters.

#### Scenario: Existing native CLI session
- WHEN a native Messages request already has a complete consistent CLI shape requiring no new compatibility rewrite
- THEN it follows the existing path
- AND known profile history is checked before this decision.

#### Scenario: Retry on another account
- WHEN an unbound request retries before output
- THEN each attempt is built from the logical original request and the new account's frozen configuration.

### Requirement: Isolate and protect signed history

The gateway MUST resolve every known signed block within the authenticated tenant and Key scope before selecting its account and MUST reject incompatible continuation.

#### Scenario: Mixed or unavailable provenance
- WHEN signed history has unknown or conflicting provenance, or its source cannot be checked
- THEN the gateway returns a specific state error without deleting history or silently switching its source account.

#### Scenario: Disabled profile continuation
- WHEN previously registered profile history is sent after its profile is disabled or with a CLI-looking User-Agent
- THEN its original source guard still applies.

### Requirement: Persist before success

The gateway MUST atomically register all response signatures only after complete upstream success and before emitting any equivalent final success signal.

#### Scenario: Stop reason without message stop
- WHEN an upstream sends a stop reason then truncates before message_stop
- THEN no successful terminal or new provenance is emitted.

#### Scenario: Provenance write failure
- WHEN registration fails after upstream completion
- THEN the client receives a protocol error, actual usage remains available for settlement, and the gateway does not replay or penalize the account for the local failure.

### Requirement: Honest capabilities

The gateway MUST use the same compatibility definition for forwarding, prompt counting and account diagnostics and MUST reject unsupported continuation features.

#### Scenario: Provider counting is unavailable
- WHEN the upstream does not support the counting endpoint
- THEN the gateway reports that limitation without fabricating a token count.

#### Scenario: Account validation
- WHEN an administrator validates ordinary-client compatibility
- THEN the diagnostic uses the ordinary request shape and the actual adapter against the requested account, without fallback to another account.
