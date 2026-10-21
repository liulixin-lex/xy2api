# 可重放前缀与明确终止（0.4.5）

## ADDED Requirements

### Requirement: Deferred opening on the Gateway converters through Anthropic upstreams

With `stream.prelude_staging` on, the Gateway inbound Responses and Chat conversions over an Anthropic upstream MUST defer `response.created` and the Chat role chunk until the first content or terminal event, classified by `resilience.ClassifyStreamEvent("messages", …)`. A tool call as the first event MUST release the opening at once. Before release:
- a retryable failure (in-band `overloaded_error` and similar, EOF, read error, stall) MUST switch accounts, and the client MUST see only the opening of the attempt that serves;
- the Anthropic first-event deadline MUST extend to the first content and MUST also cover the wait for response headers;
- upstream `ping` events, comment lines and the gateway keepalive MUST be written as comment heartbeats that do not count as output.

Non-retryable in-band errors, `max_tokens` incomplete, refusals, valid empty answers and usage-only answers MUST NOT be replayed. With both `stream.prelude_staging` and `stream.strict_terminal` off, the conversions MUST behave as on 0.4.4.

#### Scenario: Overloaded after message_start
- **WHEN** account A sends `message_start` and then an in-band `overloaded_error` before any content, and account B answers
- **THEN** the client receives exactly one `response.created` (or one role chunk) carrying B's identity, followed by B's content.

#### Scenario: Silent after message_start
- **WHEN** account A sends `message_start` and nothing else until the Anthropic first-event deadline
- **THEN** the request switches accounts with reason `first_output_timeout`, and the client has seen only comment heartbeats.

#### Scenario: Non-retryable error is not replayed
- **WHEN** account A sends `message_start` and then `invalid_request_error`
- **THEN** the request does not switch accounts and ends with the protocol's failure terminal.

### Requirement: Explicit terminal on the Gateway converters through Anthropic upstreams

With `stream.strict_terminal` on, the Gateway inbound Responses and Chat conversions over an Anthropic upstream MUST NOT drop in-band errors and MUST NOT turn a broken stream into success. After output, a break, read error or stall MUST end with `response.failed` (preceded by `response.created` if nothing was written yet) for Responses, or `data: {"error":…}` followed by `[DONE]` for Chat. A stream whose `message_delta` already carries `stop_reason` and only lacks `message_stop` MUST count as complete. Usage read so far MUST be billed once.

#### Scenario: Break after content
- **WHEN** an Anthropic upstream sends content and then the connection breaks
- **THEN** a Responses client receives `response.failed`, a Chat client receives an error chunk and `[DONE]`, and the usage from `message_start` is billed once.

#### Scenario: Missing message_stop after stop_reason
- **WHEN** the upstream sends `message_delta` with `stop_reason: end_turn` and then EOF
- **THEN** the request completes normally.

### Requirement: First-output guard on the OpenAI Responses passthrough

With `stream.strict_terminal` on, the OpenAI Responses passthrough MUST apply the same first-output deadline as the native path, including the wait for response headers, and MUST switch accounts when only opening events and heartbeats arrived before it. A read loop MUST drive the deadline, stall detection and staging heartbeats; upstream comment lines MUST be forwarded immediately during staging. After output, a stall MUST end with `response.failed` (`stream_timeout`). Independently of the switch, the passthrough MUST stage the opening in the bounded native stage (8 MiB, spilling to an unlinked temporary file above 64 KiB) and MUST switch accounts when the bound is exceeded. With the switch on, a request cancelled before the first output MUST close the upstream and end without switching accounts; with the switch off, a client cancel MUST be ignored and the upstream read to the end, as on 0.4.4. The upstream request itself always uses a detached context. Also independently of the switch, a passthrough stream that broke after output (`response.failed`, missing terminal, read error, stall) MUST bill the usage read so far exactly once, and a cancel before output MUST bill nothing. On both the passthrough and the native OpenAI Responses path, independently of the switch, when heartbeats have committed HTTP 200 and an in-stream `response.failed` before any output matches an admin error passthrough rule, the gateway MUST end with `response.created` and `response.failed` carrying the rule's type and message instead of writing a JSON body; before any commit the rule's status code and JSON body still apply. With the switch off, the passthrough MUST behave as on 0.4.4 apart from the bounded stage, this billing and this terminal.

#### Scenario: Silent after created
- **WHEN** a relay sends `response.created` and nothing else until the first-output deadline
- **THEN** the request switches accounts with reason `first_output_timeout`, and the client has seen only heartbeats.

#### Scenario: Headers never arrive
- **WHEN** a relay accepts the request but sends no response headers before the deadline
- **THEN** the request switches accounts at stage `response_headers`.

#### Scenario: Error passthrough rule after heartbeats
- **WHEN** heartbeats have committed HTTP 200 on the passthrough or the native OpenAI Responses path, and an in-stream `response.failed` before any output matches an admin error passthrough rule
- **THEN** the client receives `response.created` followed by `response.failed` with the rule's type and message, and no JSON body after the comment lines.

#### Scenario: Break after output is billed
- **WHEN** a passthrough account streams content and then the connection breaks
- **THEN** the request is billed once with the usage read so far.

#### Scenario: Comment from a relay
- **WHEN** a relay sends `: keepalive` after `response.created`
- **THEN** the comment reaches the client at once, and the opening is still released only with the first content.

### Requirement: Heartbeats commit only the gateway's own stream headers

When a heartbeat commits HTTP 200 before the first output on the Chat and Messages conversions (staged, or before the first upstream line with staging off), or on the raw Chat passthrough in strict mode, the response MUST carry only the gateway's stream headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`) and MUST NOT carry the filtered headers of that attempt's upstream. Without a heartbeat, the first output MUST still carry the upstream headers as before. The OpenAI Responses passthrough is excluded: its heartbeats MUST keep carrying the current attempt's upstream headers, including `x-codex-turn-state`, as on 0.4.4, so that a request that heartbeats and then succeeds still delivers the turn state. The passthrough MUST NOT record turn-state provenance for an attempt whose response arrives after the response to the client was committed, so that a later account cannot take over the record with a turn state the client never receives.

### Requirement: Native Responses heartbeat carries the turn state

On the native OpenAI Responses path, independently of any switch, when the first staging heartbeat commits HTTP 200 and the current attempt's staged upstream headers contain `x-codex-turn-state`, the gateway MUST write that one header with the commit and MUST record the turn-state provenance at the same moment. Other upstream headers (`x-request-id`, `x-codex-*` usage headers) MUST still wait for the first semantic output and MUST NOT be written once the response is committed. Without a heartbeat before the first output, all staged headers MUST be written at the first output as before. Without a turn state, or when the response was already committed before the upstream response arrived, the heartbeat MUST carry only the stable stream headers and MUST NOT record provenance.

#### Scenario: Long first token, single attempt
- **WHEN** an OAuth account on the native path mints a turn state, the first output takes longer than the keepalive interval, and the same attempt succeeds
- **THEN** the committed response headers contain that attempt's `x-codex-turn-state` but not its `x-request-id` or `x-codex-*` usage headers, the provenance names that account, and the body starts with `:` followed by the normal output.

#### Scenario: Switch after the heartbeat
- **WHEN** account A's attempt commits a heartbeat with A's turn state, A then times out before output, and account B serves (whether or not B mints its own turn state)
- **THEN** the client keeps A's turn state and the provenance stays with A; on the next request, the outbound guard strips A's turn state if the request is routed to B and keeps it if routed to A.

#### Scenario: No turn state from the upstream
- **WHEN** the upstream response carries no `x-codex-turn-state` and a heartbeat commits first
- **THEN** the heartbeat carries only the stable stream headers and no provenance is recorded.

#### Scenario: Passthrough keeps the attempt headers on heartbeats
- **WHEN** a passthrough attempt commits a heartbeat and then succeeds
- **THEN** the client receives that attempt's `x-codex-turn-state` and `x-request-id`.

#### Scenario: No turn-state provenance after the response is committed
- **WHEN** a passthrough attempt on account A commits a heartbeat with A's turn state, fails, and account B serves and mints its own turn state
- **THEN** the turn-state provenance stays with A, and when the client sends A's turn state to B on the next request, the outbound guard strips it.

#### Scenario: Switch after heartbeat
- **WHEN** account A's attempt commits a heartbeat, fails before content, and account B serves
- **THEN** the client's response headers contain no `x-request-id` from A.

### Requirement: A cancel racing the first write is a client cancel

With `stream.prelude_staging` on, when the client cancels while the first output is being written and the gateway's cancel callback has closed the upstream body, a following read error or EOF on the Chat and Messages conversions and on the Gateway conversions over Anthropic upstreams MUST end as a client disconnect: usage read so far is billed, the account is not reported as failed, and no post-output break is recorded. A read error after the cancel callback was stopped by the first write MUST still count as an upstream break.

#### Scenario: Cancel during the first write
- **WHEN** the client cancels during the first `Write` and the upstream body is closed by the cancel callback
- **THEN** the request ends as a client disconnect and the account is not penalised.

### Requirement: Raw Chat error needs failure evidence

With `stream.strict_terminal` on, a raw Chat chunk MUST count as a failure only when its `error` is a non-empty string, an object with a non-empty `message`, `code` or `type`, or when the root `type` is `"error"`. `null`, empty or whitespace strings, `false`, `0`, `{}` and objects whose fields are all empty MUST pass through as normal content. The raw Chat observer and `ClassifyStreamEvent("chat", …)` MUST use the same rule.

#### Scenario: Placeholder error field
- **WHEN** a relay puts `"error": ""` in every normal chunk
- **THEN** the stream completes normally and the request is billed as a success.

#### Scenario: String error
- **WHEN** a relay sends `"error": "rate limited"` before any output, followed by `[DONE]`
- **THEN** the request switches accounts.
