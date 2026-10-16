# Client disconnect and billing

## ADDED Requirements

### Requirement: Delivery state

Each request MUST track its delivery state:
- `undelivered`: no semantic byte written. Committed headers and heartbeat bytes do not count, including the pings written while the request waits for a user or account slot or the user message queue; for non-streaming requests, the body has not been written.
- `partial`: some semantic bytes written, but not complete.
- `complete`.

#### Scenario: Heartbeats only
- **WHEN** a request has written SSE headers and heartbeats but no semantic event
- **THEN** its delivery state is `undelivered`.

#### Scenario: Queued and pinged, then abandoned
- **WHEN** a streaming request waits for an account slot long enough to receive pings, gets the slot and the client leaves before the upstream's first output
- **THEN** the request is `undelivered`: the upstream is cancelled, it is not billed and an undelivered record is written.

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

### Requirement: Disconnect diagnostics

For client disconnects, `ops_error_logs` MUST record:
- `duration_ms`;
- `disconnect_stage` (`select`, `slot_wait`, `header_wait`, `pre_output` or `streaming`);
- `delivered_bytes`.

#### Scenario: Disconnect during header wait
- **WHEN** a client disconnects while the gateway waits for upstream headers
- **THEN** the stored stage is `header_wait` with the elapsed duration.
