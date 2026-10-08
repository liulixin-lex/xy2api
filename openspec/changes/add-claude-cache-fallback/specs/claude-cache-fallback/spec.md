# Claude 自动缓存规格（分组迭代）

## ADDED Requirements

### Requirement: Group-wide opt-in

The system MUST accept only an enabled switch and selected group IDs. Selection MUST cover all accounts, API keys and models within those groups without account, URL, key or model allowlists. Only Anthropic wire requests are modified.

#### Scenario: Legacy upgrade
- **WHEN** persisted settings contain old precise rules
- **THEN** group selections are deduplicated and preserved with caching disabled
- **AND** an explicit current-format save is required to enable the broader scope.

#### Scenario: Partial save and failure
- **WHEN** a settings update omits the policy
- **THEN** the saved policy remains unchanged.
- **WHEN** an expired policy cannot be refreshed
- **THEN** insertion is disabled.

### Requirement: Preserve client intent and protocol limits

Original protocol declarations MUST suppress fallback even when null or malformed. Tool argument/schema data MUST NOT be mistaken for declarations. Existing gateway/OAuth checkpoints MUST be preserved and counted. New checkpoints MUST use default 5m. They MUST NOT exceed four total checkpoints, mark thinking/empty/unknown blocks, or precede existing longer-TTL checkpoints.

#### Scenario: Missing declaration
- **WHEN** an eligible request has no original declaration
- **THEN** available slots are applied to the tail, system, last non-deferred tool and previous user anchor in priority order.
- **AND** repeated application is idempotent and shared request bytes remain unchanged.

#### Scenario: OAuth coexistence
- **WHEN** OAuth preprocessing already added some checkpoints
- **THEN** those checkpoints remain unchanged and consume the same four-point budget.
- **AND** the fallback feature does not enable the old destructive message rewrite setting.

### Requirement: Actual outbound integration

Native API Key, OAuth/SetupToken, passthrough, Vertex, Bedrock, and Chat/Responses converted to Anthropic MUST use the final outbound insertion. Bedrock payload changes MUST precede signing. Non-Anthropic protocols and count_tokens MUST remain unchanged.

#### Scenario: In-flight policy change or retry
- **WHEN** a policy is enabled after admission, a group is removed, or an attempt switches to an out-of-scope account/group
- **THEN** it cannot acquire an unreserved fallback declaration.
- **AND** prior attempt insertions never propagate through shared ParsedRequest bytes.

### Requirement: Real-usage billing

Cold-write reservation MUST consider configured normal/5m prices and candidate mapped models. Settlement MUST continue using actual upstream usage and existing pricing/override policies, without inventing cache hits or charging reservation values.

#### Scenario: Streaming and converted response
- **WHEN** an upstream returns cache usage over JSON or SSE
- **THEN** recorded raw usage and final conversion preserve input/write/read counts and existing TTL accounting.
- **AND** enabling caching does not introduce paid probes or fallback replays.

### Requirement: Compact and recoverable settings UI

The page MUST show a switch, searchable group selector and removable selections, concise scope/cost copy and save status. It MUST retain failed edits, allow disabling with unavailable/deleted groups, and avoid success feedback if an old backend ignores the setting.

#### Scenario: Retry directory load
- **WHEN** group directory retrieval fails and is retried after edits
- **THEN** the edited policy remains intact.
