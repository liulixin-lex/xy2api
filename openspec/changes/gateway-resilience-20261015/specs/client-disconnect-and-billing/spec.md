# Client disconnect and billing

## ADDED Requirements

### Requirement: Delivery state

Each request MUST track its delivery state:
- `undelivered`: no semantic byte written. Committed headers and heartbeat bytes do not count, including the pre-output keepalive heartbeats and the pings written while the request waits for a user or account slot or the user message queue; for non-streaming requests and image JSON, the body has not been written.
- `partial`: some semantic bytes written, but not complete.
- `complete`.

On every `text/event-stream` response the opening events and heartbeats written before the first content block MUST NOT count (0.4.2): SSE comment lines, `ping` and `keepalive` events, the Anthropic `message_start` and an empty text or thinking `content_block_start`, the Responses `response.created`, `response.in_progress` and `response.queued`, and Chat chunks that only announce the role. (Superseded for Responses by the 0.4.4 replayable-prefix requirement in `gateway-resilience-v2-20261021`: with `stream.native_prelude_whitelist` on, content-free added structures and, since 2026-10-22, `response.metadata` are opening too.) Every other block, including error events, `[DONE]`, `tool_use` blocks and unknown events, MUST count as content. The events MUST still be written unchanged and on time, and the failover checks MUST NOT use this classification. In 0.4.0 this held only on the OpenAI `/v1/responses` stream path.

#### Scenario: Heartbeats only
- **WHEN** a request has written SSE headers and pre-output keepalive heartbeats but no semantic event
- **THEN** its delivery state is `undelivered`.

#### Scenario: Responses keepalive comments only
- **WHEN** an OpenAI `/v1/responses` stream has written only in-stream keepalive comments when the client leaves
- **THEN** the request is `undelivered` and not billed.

#### Scenario: Queued and pinged, then abandoned
- **WHEN** a streaming request waits for an account slot long enough to receive pings, gets the slot and the client leaves before the upstream's first output
- **THEN** the request is `undelivered`: the upstream is cancelled, it is not billed and an undelivered record is written.

#### Scenario: Messages conversion wrote only opening events
- **WHEN** the upstream of a Messages-to-Responses conversion sent only `response.created`, the gateway wrote `message_start` and pings, and the client leaves
- **THEN** the request is `undelivered`: the upstream is cancelled and the request is not billed.

#### Scenario: Content after the opening events
- **WHEN** a stream wrote `message_start` and then a `content_block_delta` with text before the client leaves
- **THEN** the request is `partial` and the bounded drain applies.

#### Scenario: Unknown block
- **WHEN** the first block after the opening events is an event type the gateway does not know
- **THEN** it counts as content.

### Requirement: Undelivered requests are cancelled and not billed

When the client disconnects while the request is `undelivered`, the gateway MUST:
1. cancel the upstream context immediately, including while waiting for response headers or between account switches;
2. stop any further attempts;
3. release the in-flight reservation and concurrency slots;
4. write no `usage_logs` row and charge nothing;
5. record a `gateway_undelivered_requests` row containing request IDs, user, key, group, account, platform, model, endpoint, disconnect stage, duration, upstream usage (if reported), estimated upstream cost and `billed=false`.

These records MUST be visible only to administrators and operators.

#### Scenario: CDN cuts a slow request
- **WHEN** a client connection closes after 125 s before any semantic output
- **THEN** the upstream request is cancelled, the user is not charged, and one undelivered record is written.

#### Scenario: XY-image reconciliation stays correct
- **WHEN** an XY-image request is undelivered
- **THEN** no usage row exists for its client request ID, so XY-image eventually marks it as not charged.

### Requirement: Bounded drain after partial delivery

When the client disconnects while the request is `partial`, the gateway MUST keep reading the upstream to obtain usage for at most `disconnect.partial_drain_max_seconds` (default 180 s). It MUST then cancel the upstream and bill:
- the usage parsed so far; or,
- when the upstream reported none, the existing local estimate of the delivered content.

#### Scenario: Long generation abandoned midway
- **WHEN** a client disconnects 30 s into a stream that would run for 20 minutes
- **THEN** the gateway stops reading the upstream at most 180 s later and bills the usage obtained.

### Requirement: Abuse guard

The gateway MUST count undelivered disconnects per API key per hour. Once `disconnect.abuse_guard.threshold_per_hour` (default 30) is exceeded, each further undelivered request in that hour MUST be billed at upstream cost:
- the reported upstream usage when available;
- otherwise estimated input tokens multiplied by the account's upstream cost rate.

These charges MUST be written to `usage_logs` with `billing_mode='undelivered_abuse'` and marked `billed=true` in the undelivered record.

#### Scenario: Key keeps disconnecting before output
- **WHEN** an API key has its 31st undelivered disconnect within one hour
- **THEN** that request is billed at upstream cost and the user can see the charge.

#### Scenario: A broken upload is no abuse strike
- **WHEN** a client disconnects before the request body has fully arrived
- **THEN** the undelivered record has stage `upload` and the disconnect does not count toward the key's hourly allowance.

### Requirement: Disconnect diagnostics

Undelivered records MUST be written in the background by a bounded number of writers (16), each retried once after a short pause; a record that finds every writer busy is dropped and logged (`resilience.undelivered_record_dropped`). Billing MUST NOT depend on these records.


For client disconnects, `ops_error_logs` MUST record:
- `duration_ms`;
- `disconnect_stage` (`upload` while the request body is still arriving, `select`, which includes waiting for a slot, `header_wait`, `pre_output` or `streaming`);
- `delivered_bytes`.

#### Scenario: Disconnect during header wait
- **WHEN** a client disconnects while the gateway waits for upstream headers
- **THEN** the stored stage is `header_wait` with the elapsed duration.
