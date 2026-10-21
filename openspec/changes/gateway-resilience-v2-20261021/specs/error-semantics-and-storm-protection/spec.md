# 错误语义与终止出口（0.4.4）

## ADDED Requirements

### Requirement: Conditional retry-after on synthesized response.failed

With `errors.failed_frame_retry_after` on, every `response.failed` the gateway synthesizes MUST set `error.headers.retry-after` from the request's retry advice:
- the request still has a healthy alternative (untried, no hard penalty): no value, or 1 second;
- the group has no healthy candidate and the earliest recovery time is known: 2–10 seconds, and the message text "try again in Ns" MUST use the same N;
- turn cap, terminal balance failure or content refusal: no value;
- no advice: the 0.4.3 frame.

With the switch off, synthesized frames MUST NOT carry `error.headers`.

#### Scenario: Exhausted after heartbeat with a known recovery (RA-1)
- **WHEN** a CDN request has committed heartbeats, every account of the group is overloaded, and the earliest recovery is 6 seconds away
- **THEN** the client receives `response.failed` with `error.headers.retry-after` "6" and a message saying "try again in 6s".

#### Scenario: Healthy alternative exists (RA-1)
- **WHEN** the request ends while an untried healthy account remains
- **THEN** `response.failed` carries no retry-after, or 1 second.

#### Scenario: Turn cap
- **WHEN** the session reaches the turn cap
- **THEN** `response.failed` carries no retry-after.

### Requirement: Dedicated exhausted exits use the common retry hint and turn cap

The Gateway Responses, Gateway Chat and OpenAI-to-Messages exhausted exits MUST apply the common exhausted retry hint and turn cap. At the cap, both the HTTP `Retry-After` and any retry-after embedded in the body MUST be removed. A request MUST write at most one terminal frame.

#### Scenario: Gateway Chat exhausted at the cap
- **WHEN** a session's fifth consecutive exhausted failure arrives through the Gateway Chat path
- **THEN** the response carries no `Retry-After` in headers or body and `x-should-retry: false`.

### Requirement: Protocol terminal frame after queue pings

When a request has already received queue-wait pings and then fails with a billing or other terminal error, the gateway MUST end it with the inbound protocol's terminal frame: Responses `response.created` then `response.failed`, Chat an `error` chunk then `[DONE]`. It MUST NOT write a bare JSON body. This fix is not controlled by any switch.

#### Scenario: Balance exhausted after a queue ping (Q-1)
- **WHEN** a user's concurrency is 1, a second streaming request waits in the queue and receives a ping, and then fails on insufficient balance
- **THEN** a Responses client receives `response.created` then `response.failed`, and a Chat client receives an `error` chunk then `[DONE]`.

### Requirement: Anthropic gateway failures use api_error

For Anthropic Messages clients, the gateway's own no-account and transient failures, including error bodies that carry only a code, MUST use error type `api_error`. Only an upstream's own `overloaded_error` MUST be passed through with that type. HTTP status codes MUST NOT change.

#### Scenario: No account on Messages (ANT-1)
- **WHEN** a Messages request finds no available account
- **THEN** the response is 503 with error type `api_error`, not `overloaded_error`.

### Requirement: Retry-After cap for Codex

For requests identified as Codex (user agent or `originator` header), the `Retry-After` of a no-account 503 MUST NOT exceed `errors.codex_retry_after_cap_seconds` (default 10; 0 disables the cap). Other clients keep 2–30 seconds.

#### Scenario: Codex no-account
- **WHEN** a Codex request finds no account and the group's estimated recovery is 25 seconds
- **THEN** the response carries `Retry-After` of at most 10.

#### Scenario: Other client
- **WHEN** a non-Codex request finds no account and the estimated recovery is 25 seconds
- **THEN** the response carries `Retry-After: 25`.
