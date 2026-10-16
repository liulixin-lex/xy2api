# Pre-output keepalive, first-output timeout and stall detection

## ADDED Requirements

### Requirement: Edge marker from the reverse proxy

The gateway MUST treat a request as arriving through a CDN only when all of the following hold:
- its immediate peer is a configured trusted proxy;
- it carries `X-XY-Edge` with a value listed in `keepalive.edges` (default `cf`, `esa`).

The reverse proxy MUST remove any client-supplied `X-XY-Edge`, and MUST set it only from the immediate peer address matching Cloudflare or ESA origin ranges.

#### Scenario: Internal client is never marked
- **WHEN** XY-image calls the gateway through the host-local reverse proxy
- **THEN** the request carries no `X-XY-Edge` marker and pre-output keepalive is not armed.

#### Scenario: Spoofed marker is ignored
- **WHEN** a client sends `X-XY-Edge: cf` directly to the origin
- **THEN** the reverse proxy removes it and the request is treated as direct.

### Requirement: Delayed commit of SSE headers

For a streaming request that meets all of the following, the gateway MUST arm a pre-output keepalive:
- marked as coming through a CDN;
- inbound protocol is OpenAI Responses, OpenAI Chat Completions or Anthropic Messages;
- has passed authentication, balance and subscription checks, in-flight reservation, parameter and model mapping checks, content moderation and account selection;
- the selected account is not an official OAuth account;
- the endpoint is not an image or asynchronous task endpoint.

If no byte has been written to the client after `keepalive.delay_seconds` (default 15 s), the gateway MUST:
1. commit the SSE response headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, `X-Client-Request-ID`);
2. write a heartbeat every `keepalive.interval_seconds` (default 10 s) until the first semantic byte or the end of the request.

The keepalive clock MUST NOT reset on account switches.

#### Scenario: Fast failure keeps its status code
- **WHEN** account selection fails after 2 s
- **THEN** the client receives the normal HTTP status (for example 503 with `Retry-After`) and no SSE headers.

#### Scenario: Slow upstream through Cloudflare
- **WHEN** a Cloudflare-marked Codex request waits 100 s for the upstream response headers
- **THEN** SSE headers are committed at 15 s and heartbeats are written every 10 s, so the CDN does not cut the connection.

#### Scenario: Official OAuth account
- **WHEN** the selected account is an official OAuth account
- **THEN** no early commit happens, so upstream response headers such as `x-codex-turn-state` can still be forwarded.

#### Scenario: Gemini is excluded
- **WHEN** a streaming request arrives on a Gemini native endpoint
- **THEN** no pre-output keepalive is armed.

### Requirement: Protocol-specific heartbeat frames

Heartbeats MUST be:
- `:\n\n` for OpenAI Responses and Chat Completions;
- `event: ping\ndata: {"type":"ping"}\n\n` for Anthropic Messages.

Heartbeat bytes MUST NOT count as client output when deciding whether failover is still allowed.

#### Scenario: Failover after heartbeats
- **WHEN** heartbeats have been written but no semantic event has been sent and the attempt fails with `RATE_LIMIT`
- **THEN** the gateway may still switch to another account.

### Requirement: First-output timeout

For OpenAI text requests, each attempt MUST have a first-output timeout, measured from the start of the attempt, equal to the smaller of the remaining pre-output budget and:
- 180 s for reasoning effort none, minimal, low, medium or high;
- 200 s for xhigh;
- 220 s for max;
- any per-model override.

Anthropic and Gemini attempts MUST have a first-upstream-event timeout (default 90 s). On timeout the attempt MUST be classified `FIRST_OUTPUT_TIMEOUT` and the gateway MUST switch accounts within the budget. The attempt on the last available candidate, including the only usable account of a group, MUST NOT have a per-attempt timeout, only the pre-output deadline. The previous limit of one first-output switch MUST be removed.

#### Scenario: Slow relay is abandoned in time
- **WHEN** a medium-effort request gets no output from account A within 180 s and account B is available
- **THEN** the gateway cancels the attempt on A, soft-penalises A and tries B.

#### Scenario: Last candidate waits for the budget
- **WHEN** account A is the only candidate
- **THEN** the attempt continues past 180 s until output arrives or the 240 s pre-output deadline ends it.

### Requirement: Relay keep-alive frames are not output

Frames that some relays inject into Responses streams, of type `response.output_text.delta` with an empty `delta` and `item_id` `SSE-Keep-Alive` (or a `SSE-Keep-Alive: true` field), MUST be dropped when the upstream stream is read. They MUST NOT reach the client and MUST NOT count as semantic output. All other bytes MUST pass through unchanged.

#### Scenario: Keep-alive before response.created
- **WHEN** a relay sends such a frame before `response.created`
- **THEN** the client's first event is `response.created` and the openai-python stream helper completes.

#### Scenario: Keep-alive does not disarm failover
- **WHEN** a relay sends only such frames and then fails before real output
- **THEN** the gateway still switches accounts as before output.

A dropped frame MUST still count as upstream activity for stall detection: the stall timers run from the later of the last line read and the last frame dropped. Each dropped frame MUST be counted per account (`relay_keepalive`).

#### Scenario: Keep-alive frames during a quiet period
- **WHEN** after output a relay sends only such frames for longer than the text stall threshold, then continues
- **THEN** the stream is not ended as stalled, the frames do not reach the client, and the response completes.

### Requirement: Accurate stall detection

Stream inactivity MUST be checked at a period of at most `min(threshold/4, 5 s)`, so detection happens no later than the threshold plus 5 s. After semantic output has started, a text stream with no upstream data for `stall.text_stall_seconds` (default 120 s) MUST be ended as `STALL`. Image streams MUST keep `image_stream_data_interval_timeout`.

#### Scenario: Stall detected within the threshold
- **WHEN** the upstream stops sending after partial output
- **THEN** the gateway ends the stream between 120 s and 125 s after the last upstream data.

### Requirement: Terminal frames after commit

When a request fails after the response headers were committed, the gateway MUST write exactly one protocol terminal frame, flush it and close the response immediately:

- **Responses:** `response.failed` with `response.error.code` `rate_limit_exceeded` (rate limited or busy) or `server_error` (other), and a message containing `Please try again in Ns.` with N between 2 and 30 s.
- **Chat Completions:** `data: {"error":{"type","code","message","status"}}` followed by `data: [DONE]`.
- **Anthropic Messages:** `event: error` with `{"type":"error","error":{"type":"overloaded_error"|"api_error","message"}}`.

Error JSON MUST be on a single line and MUST include a stable `code` and the original HTTP semantic status as `error.status` where the protocol allows.

#### Scenario: Anthropic error after a ping
- **WHEN** an Anthropic request fails after `event: ping` frames were written
- **THEN** the client receives an `event: error` frame that the Anthropic SDK raises as an API error.

#### Scenario: Codex sees the failure promptly
- **WHEN** an upstream emits `The service is busy` after output started
- **THEN** the gateway writes `response.failed` with `rate_limit_exceeded` and closes the stream, and Codex retries after the indicated delay.
