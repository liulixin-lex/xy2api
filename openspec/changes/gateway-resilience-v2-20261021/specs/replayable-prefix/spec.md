# 可重放前缀与明确终止（0.4.4）

## ADDED Requirements

### Requirement: One replayable-prefix rule for every stream path

The gateway MUST classify each complete upstream SSE event as prelude, content, terminal success, terminal failure (retryable or not) or unknown with one shared function, and every covered stream path (native Responses staging and pending lines, Chat and Messages compatibility conversion, delivery tracking) MUST use it. Unknown events MUST be treated as content.

Prelude events are:
- Responses: `response.created`, `response.in_progress`, `response.queued`, `keepalive`, `response.metadata` (exact type only, with `stream.native_prelude_whitelist` on; revised 2026-10-22, see design §3.1), and `response.output_item.added`, `response.content_part.added` or `response.reasoning_summary_part.added` whose known fields are all empty (no text, no encrypted reasoning, no tool arguments, no unknown non-empty field);
- Chat: a chunk that carries only the role, or an empty content delta;
- Messages: `message_start`, `ping`, and `content_block_start` of an empty text or thinking block.

#### Scenario: Unknown event is content
- **WHEN** an upstream Responses stream sends an event type the gateway does not know before any content
- **THEN** the event is treated as content and the request is no longer switched to another account.

#### Scenario: Codex metadata event is not prelude (CV-9)
- **WHEN** an upstream sends `codex.response.metadata` after `response.created` and before any text, then fails
- **THEN** the event counts as output, the failure is forwarded and the request does not switch accounts.

#### Scenario: response.metadata before content is prelude (revised 2026-10-22)
- **WHEN** with `stream.native_prelude_whitelist` on, an upstream sends `response.created`, `response.in_progress` and `response.metadata`, then a retryable failure such as a bare `error` with `server_is_overloaded`, before any content
- **THEN** nothing from the failed attempt is written to the client and the failure is handled before output as in CV-5 (at most one same-account retry for capacity shedding, then another account); the client then sees only the successful attempt's events. Like other prelude, `response.metadata` does not stop the first-output deadline. With the switch off, `response.metadata` counts as output as in 0.4.3, also in the Chat and Messages conversions.

#### Scenario: Empty delta before created
- **WHEN** a relay sends a Responses delta whose `delta` is an empty or whitespace-only string before `response.created`
- **THEN** the frame is dropped, counts as upstream liveness and as `relay_keepalive`, and does not count as output.

#### Scenario: Non-empty delta before created
- **WHEN** a relay sends a non-empty delta before `response.created`
- **THEN** the delta is written to the client at once as output, `pre_created_delta` is counted once for the attempt, and the event `resilience.pre_created_delta` records its byte count without content; later whitespace deltas in that attempt are forwarded as content.

#### Scenario: Encrypted reasoning is content
- **WHEN** `response.output_item.added` carries a reasoning item with `encrypted_content`
- **THEN** it is content.

### Requirement: Semantics before replay

While nothing but prelude has been written, a failure or end of stream MUST be decided in this order: client cancellation or a rejected request ends the request; a legitimate empty answer, `response.incomplete` and a content refusal end normally without replay; a retryable failure becomes an account switch within the request budget; any other failure ends the request as before.

#### Scenario: Incomplete is not replayed
- **WHEN** the upstream sends `response.created` then `response.incomplete`
- **THEN** the client receives the incomplete result and the gateway does not switch accounts.

#### Scenario: Usage-only empty answer
- **WHEN** the upstream completes with usage and no output text
- **THEN** the request succeeds once and is not replayed.

#### Scenario: Client cancels while staged
- **WHEN** the client disconnects while only prelude has been staged
- **THEN** the upstream request is cancelled immediately and nothing is billed.

### Requirement: Native Responses prelude whitelist

With `stream.native_prelude_whitelist` on, native Responses streaming MUST keep staging while only prelude events have arrived, and a retryable failure in that window MUST switch accounts. After a switch the client MUST see only the new attempt's `response.created` as its first event; prelude of failed attempts MUST be discarded, not spliced. With the switch off, the prelude set MUST be `response.created`, `response.in_progress` and `keepalive` only (0.4.3).

#### Scenario: Created then overloaded (CV-5)
- **WHEN** account A sends `response.created` then `response.failed` with `server_is_overloaded`, and account B can serve
- **THEN** A is tried at most twice (D18 keeps one same-account retry for capacity shedding, each attempt recorded as `CAPACITY_SHED`), the request is served by B, and the client receives exactly one `response.created`, with B's id.

#### Scenario: Created and in_progress then overloaded (CV-7)
- **WHEN** account A sends `response.created`, `response.in_progress`, then `response.failed` with `server_is_overloaded`
- **THEN** the request switches to another account and the client sees only the serving account's events.

#### Scenario: Queued then failed
- **WHEN** account A sends `response.created`, `response.queued`, then a retryable `response.failed`
- **THEN** the request switches to another account and succeeds.

#### Scenario: Empty delta before created and empty added structures (CV-8)
- **WHEN** account A sends an unmarked empty `response.output_text.delta` before `response.created`, then `response.created`, an empty message `response.output_item.added`, an empty output_text `response.content_part.added`, and a retryable `response.failed`
- **THEN** none of A's events reach the client, the request switches to another account and succeeds.

#### Scenario: Whitelist off
- **WHEN** `stream.native_prelude_whitelist` is off and account A sends `response.created`, `response.queued`, then `response.failed`
- **THEN** the failure is forwarded as in 0.4.3.

### Requirement: Deferred opening on compatibility conversion

With `stream.prelude_staging` on, Chat Completions and Messages streams converted from a Responses upstream MUST NOT write the role chunk or `message_start` until the first content or terminal event, and then MUST write the opening and that event in order. A retryable failure, EOF or read error before that point MUST switch accounts, and the client MUST see a single opening. The first-event guard MUST extend to the first content. While staged, the converter MUST write comment heartbeats at the stream keepalive interval; they count as heartbeat bytes, not output, and MUST NOT prevent an account switch. Once heartbeats have committed 200, exhaustion MUST end with the inbound protocol's terminal frame. The `first_token_ms` measurement point MUST stay at the first upstream line. With the switch off, the opening MUST be written when the upstream `response.created` arrives (0.4.3).

#### Scenario: Chat busy after created (CV-6)
- **WHEN** a streaming `/v1/chat/completions` request is converted to Responses and account A sends `response.created` then `response.failed` with "The service is busy"
- **THEN** the request is served by another account and the client receives exactly one role chunk.

#### Scenario: Messages EOF after created
- **WHEN** a streaming Messages request is converted and account A sends `response.created` then closes the stream
- **THEN** the request switches accounts and the client receives exactly one `message_start`.

#### Scenario: Staged longer than the heartbeat interval
- **WHEN** a converted stream stays staged longer than `stream_keepalive_interval` and account A then fails before content
- **THEN** the client has received `:` comment heartbeats, the request still switches accounts, and the client sees a single opening.

#### Scenario: Tool call as the first event
- **WHEN** the first content event is a tool call
- **THEN** the opening and the tool call are written immediately.

#### Scenario: Heartbeat committed before the switch
- **WHEN** a CDN request has committed SSE headers with heartbeats and account A fails before content
- **THEN** the request switches accounts and the client sees heartbeats followed by one opening from the serving account.

#### Scenario: Failed attempt usage is not billed
- **WHEN** account A reports usage in a failed staged attempt and account B succeeds
- **THEN** the request is billed only with B's usage.

### Requirement: Raw Chat explicit terminal

With `stream.strict_terminal` on, the raw Chat Completions passthrough MUST treat `data: {"error":...}` followed by `[DONE]` as a failure: before commit it MUST switch accounts; after commit it MUST be recorded as a post-output failure, not a success. A stream with usage or a finish reason and no error remains a success.

#### Scenario: Error then DONE (RAW-1)
- **WHEN** an account that does not support `/responses` streams `data: {"error":{...}}` then `[DONE]` before any content, and account B can serve
- **THEN** the request switches to B and succeeds.

#### Scenario: Error then DONE on the only account (RAW-2)
- **WHEN** the same happens in a group whose only account is A
- **THEN** the request is recorded as a failure, not a success, and no usage is billed for it.
