# 错误语义与终止出口（0.4.5）

## ADDED Requirements

### Requirement: Upstream Retry-After counts as a known recovery on exhaustion

With `errors.failed_frame_retry_after` on, when a committed Responses stream ends in `response.failed` after the request exhausted its accounts, and the retry advice from the group snapshot is unknown (no snapshot, stale snapshot, or only accounts this request already failed on), the gateway MUST use the `Retry-After` of the last retryable upstream response as the recovery time:
- only an upstream whose original status is 429 or 5xx qualifies; a 400 or 404 that the handler maps to 502 does not;
- the value MUST be a positive integer of seconds or a future HTTP date, rounded up and clamped to 2–10 seconds;
- missing, invalid, non-positive or past values, and the gateway's own default of 30 seconds, MUST NOT be used.

A healthy alternative (1 second or none), a recovery time computed from the snapshot, and terminal suppression (turn cap, the user's own balance, content refusal) MUST take precedence. The header `error.headers.retry-after` and the message text "try again in Ns" MUST carry the same N. A cancelled request MUST NOT get this advice.

#### Scenario: Capacity failure with an upstream wait (RA-1)
- **WHEN** a CDN request has committed heartbeats, the only account answers 503 with `Retry-After: 8` twice, and the request exhausts
- **THEN** the client receives `response.failed` with `error.headers.retry-after` "8" and a message ending with "try again in 8s".

#### Scenario: Mapped client error does not count
- **WHEN** the last upstream answered 404 with a `Retry-After` header and the handler reported it as 502
- **THEN** that value is not used and, with no other recovery time, the frame carries no retry-after.

#### Scenario: Invalid or expired upstream value
- **WHEN** the last upstream `Retry-After` is "0", negative, unparsable or an HTTP date in the past
- **THEN** the frame carries no retry-after.

#### Scenario: Healthy alternative still wins
- **WHEN** an untried healthy account remains at the time the frame is written
- **THEN** the frame carries 1 second or no retry-after, not the upstream wait.

#### Scenario: Switch off keeps the 0.4.3 text
- **WHEN** `errors.failed_frame_retry_after` is off in the RA-1 situation
- **THEN** the frame carries no `error.headers`, and the message is the same as on 0.4.3.

### Requirement: Retry advice is resolved only for a failure frame

The retry advice provider MUST NOT be evaluated at no-account or exhausted exits that end in a plain JSON response. It MUST be evaluated when a `response.failed` frame is written, and the value written in the frame MUST become the request's final advice.

#### Scenario: JSON exit does not resolve
- **WHEN** a non-streaming request ends with a no-account 503 JSON
- **THEN** the group advisor is not called.

#### Scenario: Committed stream resolves once
- **WHEN** a committed Responses stream exhausts in the RA-1 situation
- **THEN** the provider is evaluated once when the frame is written, and the frame carries 8 seconds.
