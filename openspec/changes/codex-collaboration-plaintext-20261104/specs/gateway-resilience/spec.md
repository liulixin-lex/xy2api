# Gateway resilience delta

## ADDED Requirements

### Requirement: Gated collaboration plaintext channel

The gateway SHALL default collaboration_plaintext.enabled to false and models to an empty array. The gateway SHALL apply the atomic namespace transform only to declared collaboration tools on eligible native Responses HTTP requests using the final mapped model and an enabled OpenAI account. API Key and OAuth SHALL both be supported. WS, passthrough, compact, bridge and flatten routes SHALL remain unmodified by P1′.

#### Scenario: Eligible outbound declaration

- **GIVEN** every eligibility gate passes and the request declares collaboration
- **WHEN** the gateway prepares the native HTTP upstream body
- **THEN** it SHALL atomically rename the namespace to collaboration-xy and remove message.encrypted only for spawn_agent, send_message and followup_task
- **AND** it SHALL update exact functions.collaboration. references only in top-level instructions and developer/system message text, recording DevTextRewrites
- **AND** historical encrypted items, IDs, arguments, user messages and tool outputs SHALL remain unchanged.

#### Scenario: Frozen response restoration

- **GIVEN** an applied plan for this attempt
- **WHEN** a stream, JSON or aggregated SSE response arrives after settings change
- **THEN** the original plan SHALL restore the canonical namespace and bare leaf name
- **AND** nonempty encrypted evidence SHALL veto adding an empty encrypted_function_args marker.

#### Scenario: Unsafe restoration

- **WHEN** an applied response has ambiguous identity or cannot be safely parsed
- **THEN** the gateway SHALL terminate locally under the existing commit gate
- **AND** SHALL NOT disclose private names or replay via failover.

### Requirement: Preserve reserved collaboration schema

The schema sanitizer SHALL skip the entire exact collaboration namespace. Other tools SHALL retain existing compatibility repairs regardless of the plaintext feature switch.

### Requirement: Bounded collaboration observations

The gateway SHALL report attempt outcome/model, EFA marginal counts and action marginal counts separately. It SHALL deduplicate logical calls and retain earlier nonempty evidence. Warnings SHALL contain only metadata and counts.

### Requirement: P0 state rejection classification

P0 工作包将在集成后补充本节场景与状态族识别细节。
