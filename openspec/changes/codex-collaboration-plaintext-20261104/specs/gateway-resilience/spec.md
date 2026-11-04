# Gateway resilience delta

## ADDED Requirements

### Requirement: Gated collaboration plaintext channel

The gateway SHALL default collaboration_plaintext.enabled to false and models to an empty array. The gateway SHALL apply the atomic namespace transform only to declared collaboration tools on eligible native Responses HTTP requests using the final mapped model and an OpenAI account with its switch enabled or continuity eligibility. API Key and OAuth SHALL both be wired, while OAuth MUST remain disabled until separately prechecked. WS, passthrough, compact, bridge and flatten routes SHALL remain unmodified by P1′.

#### Scenario: Eligible outbound declaration

- **GIVEN** every eligibility gate passes and the request declares collaboration
- **WHEN** the gateway prepares the native HTTP upstream body
- **THEN** it SHALL atomically rename the namespace to collaboration-xy and remove message.encrypted only for spawn_agent, send_message and followup_task
- **AND** it SHALL update exact functions.collaboration. references only in top-level instructions and developer/system message text, recording DevTextRewrites
- **AND** historical encrypted items, IDs, arguments, user messages and tool outputs SHALL remain unchanged.

#### Scenario: Original state exclusions

- **GIVEN** the original request has store:true, a nonempty previous_response_id or ambiguous root/protocol-container keys, including recursive duplicate keys within complete collaboration namespace schemas
- **WHEN** compatibility normalization changes/removes these fields or collapses duplicate keys
- **THEN** P1′ SHALL remain unsupported and SHALL NOT transform the declaration.
- **AND** original checks SHALL run only when raw bytes contain the literal `"collaboration"`; otherwise no plan SHALL be granted, even if normalization later materializes an escaped declaration. Continuity SHALL be frozen before normalization and SHALL NOT be recomputed from the rewritten body.
- **AND** non-string tool_choice, dotted/dunder request histories and Chat function shapes SHALL remain unsupported; only absent or auto/none/required tool_choice is supported.

#### Scenario: Frozen response restoration

- **GIVEN** an applied plan for this attempt
- **WHEN** a stream, JSON or aggregated SSE response arrives after settings change
- **THEN** the original plan SHALL restore the canonical namespace and bare leaf name
- **AND** nonempty encrypted evidence SHALL veto adding an empty encrypted_function_args marker.
- **AND** known instructions echoes SHALL restore only the exact references rewritten by this plan.
- **AND** final non-2xx HTTP error envelopes and SSE type=error data SHALL decode JSON string tokens to match aliases, including escaped forms, rewriting only matching tokens and preserving other bytes. Non-JSON errors SHALL use literal replacement. Every JSON response/event SHALL restore non-null top-level error and response.error values, including HTTP 200 failures; ambiguous carriers SHALL terminate locally while output and arguments remain unchanged. Original errors SHALL remain the source for service classification and observations; nil plans SHALL not rewrite.
- **AND** before Forward returns an applied attempt’s UpstreamFailoverError, its ResponseBody and nonempty ClientMessage SHALL be restored through ErrorBody so exhausted-failover handler exits receive canonical identities. Observe-only errors SHALL retain their original bytes; subsequent P0 classification SHALL remain unchanged. Exhausted-failover handler passthrough rules SHALL match canonical names while service rules match the original error; rule keywords SHOULD NOT use the private alias. Internal diagnostic logs MAY retain private aliases but SHALL NOT be used as client response text.

#### Scenario: Unsafe restoration

- **WHEN** an applied response has ambiguous identity or cannot be safely parsed
- **THEN** the gateway SHALL terminate locally under the existing commit gate
- **AND** SHALL NOT disclose private names or replay via failover.

#### Scenario: Continuity and soft rollback

- **GIVEN** the request declares collaboration and a top-level input item has type=function_call, namespace=collaboration, one of the three message tool names and an explicitly present encrypted_function_args=[]
- **AND** absent, nonempty, null or other typed encrypted_function_args SHALL NOT satisfy continuity, regardless of the message
- **WHEN** the selected API Key account switch is off
- **THEN** the gateway SHALL treat the account gate as satisfied and report eligibility=continuity, without relaxing any other gate or scanning nested prose.
- **AND** OAuth with its account switch off SHALL remain observe-only even with matching history; OAuth SHALL require explicit administrator opt-in after separate prechecks.
- **AND** a new session lacking that product SHALL remain unmodified; an enabled account SHALL report eligibility=account.
- **AND** disabling the master/feature switch, clearing models or an original store:true/nonempty previous_response_id SHALL still prevent conversion.

### Requirement: Preserve reserved collaboration schema

The schema sanitizer SHALL skip the entire exact collaboration namespace. Other tools SHALL retain existing compatibility repairs regardless of the plaintext feature switch.

### Requirement: Bounded collaboration observations

The gateway SHALL report attempt outcome/model, EFA marginal counts and action marginal counts separately. It SHALL deduplicate logical calls and retain earlier nonempty evidence. Calls/EFA SHALL count only the three message tools, excluding wait/list/interrupt. Observe-only calls SHALL expose msg=opaque|text shape counts for valid string messages. Statistics SHALL publish only at arm and finish, before P0 reads final attempt facts. Applied attempts returning an error SHALL add a failure warning only when the existing classifier reports CLIENT_ERROR, excluding rate-limit and transient equivalents and account/balance categories. This SHALL include pre-output HTTP 200 stream failures with in_band=true, retaining upstream_status=200 and headers_committed. Network failures, 5xx and stream disconnects SHALL NOT independently trigger this warning; existing restore-failure and anomalous-stat warnings SHALL remain independent. Warnings SHALL contain only metadata and counts.

Long-window acceptance SHALL use persistent state_rejection.collab_plaintext events and usage_logs grouped by account/model/UA as an approximate request denominator, without claiming exact eligible message coverage. Single-instance counters retain at most one hour and SHALL only support gray-release sampling.

### Requirement: Rollout and rollback constraints

Operators SHALL keep accounts within the same upstream site consistently configured and precheck every model in the global list per site. Switching configuration can invalidate the ongoing prompt prefix once from the declaration onward. Existing sessions MAY be rejected after hard rollback; affected users MUST start a new session. The account switch SHALL provide soft rollback for new sessions while existing product-bearing sessions continue on otherwise eligible API Key accounts. Codex providers not named OpenAI remove historical EFA and SHALL NOT receive continuity; disabling an account switch therefore acts as hard rollback for them. Disabling master/feature or clearing models SHALL provide hard rollback for all new attempts. Continuity SHALL be a client-declared protocol condition, not authorization: only client-supplied empty markers MAY satisfy it; plaintext history without EFA SHALL NOT satisfy continuity, and eligibility=continuity SHALL NOT imply gateway provenance. No server-side session storage SHALL be introduced; unvalidated API Key accounts MUST be excluded from the gray-release pool rather than relying on their account switch to stop continuity traffic. The reachable pool SHALL include fallback groups and full-load borrowing targets. OAuth accounts with the switch off SHALL be treated as an on-to-off transition for an existing session and excluded until prechecked. Switching to an unlisted final mapped model SHALL be treated as hard rollback for that request. Codex version changes SHALL trigger state-rejection sampling by client_version.

### Requirement: P0 state rejection classification

The gateway SHALL classify state rejections as request scoped CLIENT_ERROR results with a StateFamily of relay_state, conversation_invalid, function_output, agent_message, reasoning or other_encrypted. Classification SHALL preserve the v3 narrow boundary and SHALL remain separate from repair eligibility. State rejection handling SHALL apply to native and passthrough HTTP, SSE failed/error events, Chat/Messages shared routes, WS/account side effects and handler recovery independently of resilience settings.

#### Scenario: Dedicated code and narrow message fallback

- **WHEN** the code is client_tool_continuation_unavailable or the existing IsEncryptedContentRejection predicate matches
- **THEN** the rejection SHALL be request scoped
- **AND** otherwise only an empty code SHALL permit the complete phrases opaque conversation state, unrecognized conversation state or encrypted content is invalid for this conversation as fallback evidence
- **AND** a nonempty server/rate-limit code with only those fallback phrases SHALL retain its existing classification; type alone SHALL NOT establish a state rejection.

#### Scenario: Preserve encrypted classification and repair predicates

- **WHEN** a state rejection is classified
- **THEN** Encrypted SHALL equal IsEncryptedContentRejection(code, message)
- **AND** the reason SHALL remain encrypted_content for an existing encrypted match and SHALL be conversation_state for other state matches
- **AND** HTTP/in-band repair and WS recovery SHALL continue to use their original predicates, budgets and output commit gates.

#### Scenario: Request rejection stops account recovery

- **GIVEN** a classified state rejection, including when the resilience master switch is disabled or no request budget is configured
- **WHEN** an account recovery or penalty decision is reached
- **THEN** the gateway SHALL stop failover before consuming recovery budget
- **AND** SHALL NOT penalize the account, increment session failure or threshold/share soft-failure state, apply post-output cooldown, or trigger P1-B.

#### Scenario: Preserve bounded repair without stripping unrelated content

- **GIVEN** S2/S6 and reasoning/compaction state already eligible for repair
- **WHEN** the existing repair gate permits replay
- **THEN** the gateway SHALL retain its bounded same-account repair
- **AND** S1/S3/S4 with unrelated reasoning SHALL leave collaboration/tool content intact and SHALL NOT replay through state classification.

#### Scenario: State rejection after semantic output

- **WHEN** a state rejection follows semantic output
- **THEN** the gateway SHALL NOT replay, rebind the account, cool down the account or increment session failure
- **AND** actual usage and existing error attribution SHALL remain intact.

#### Scenario: Final attempt observation without body storage

- **WHEN** a native or passthrough Forward attempt terminates with a state rejection
- **THEN** the gateway SHALL emit at most one resilience.state_rejection event and increment state_rejection with the family label
- **AND** the event SHALL include request correlation, family, encrypted, path, stage, headers_committed, error_ms, client_version and protocol-position fragment counts
- **AND** it SHALL read the final rule_id and collaboration Applied/Calls/EFAAbsent/EFAEmpty/EFANonEmpty/MarkerAdded/Vetoed/Contradicted/Uncovered statistics after all attempt statistics have been written
- **AND** collab_plaintext SHALL equal Applied; rule state SHALL reset per attempt and update only on an actual rule match
- **AND** a final successful attempt SHALL NOT emit a state rejection observation.

#### Scenario: Bounded census and existing error attribution

- **WHEN** state rejection observations and existing upstream_errors are recorded
- **THEN** the new observation SHALL store only identifiers, metadata and counts, without request/error bodies, ciphertext or arguments
- **AND** the census SHALL inspect only protocol-defined encrypted_content positions, including valid Unicode escapes, without recursively scanning arguments, tool output strings or user JSON
- **AND** classification SHALL use the original error code without depending on optional body logging
- **AND** state_family SHALL be attached only to existing upstream_errors; observation SHALL NOT create additional attempt attribution events.

#### Scenario: Partial WS observations

- **WHEN** a WS penalty helper identifies a state rejection
- **THEN** it SHALL return before account side effects and emit the available state rejection metadata without expanding helper signatures or the four call paths
- **AND** stage SHALL be unknown; headers_committed, client_version and fragment counts SHALL be omitted
- **AND** cumulative DeliveredBytes SHALL NOT imply post_output; bridge and WS observations SHALL omit rule_id.

#### Scenario: Preserve other recovery and passthrough behavior

- **WHEN** balance/quota, rate-limit, unsupported-model or capacity outcomes do not match the state classifier
- **THEN** their existing classifications and recovery branches SHALL remain intact
- **AND** P0 SHALL preserve the existing passthrough HTTP client error envelope and wording.
