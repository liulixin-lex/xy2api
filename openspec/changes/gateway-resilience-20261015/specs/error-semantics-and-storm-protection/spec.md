# Error semantics and retry-storm protection

## ADDED Requirements

### Requirement: Existing status codes are kept

The gateway MUST keep the existing HTTP status codes for the following:
- channel pricing restriction;
- Claude-Code-only rejection;
- temporarily unavailable accounts;
- `model_not_found`.

Only the response headers and body hints defined below are added. The single exception is the in-flight reservation rejection, which changes from 403 to 429.

#### Scenario: Pricing restriction keeps 503
- **WHEN** a request is blocked by a channel pricing restriction
- **THEN** the response is still 503 with the existing message
- **AND** it includes `Retry-After: 60` and `x-should-retry: false`.

### Requirement: Retry-After on no-account and rate-limit responses

- **Temporary no-account 503:** `Retry-After` MUST equal the time until the earliest account in the group recovers, clamped to 2–30 s, and the message MUST state `Please try again in Ns.`.
- **All-accounts-rate-limited 429:** MUST carry `Retry-After`, clamped to 2–30 s.
- **Configuration 503s** (pricing restriction; Claude-Code-only without a fallback group): MUST carry `Retry-After: 60` and `x-should-retry: false`.
- **Server cap:** every `Retry-After` sent by the gateway MUST be capped by the server.

#### Scenario: Codex backs off instead of hammering
- **WHEN** a Codex request receives a no-account 503
- **THEN** the response carries a `Retry-After` between 2 and 30 s.

### Requirement: Negative cache for no-account results

The gateway MUST cache a "no available account" result per (group, platform, model) for `errors.negative_cache_seconds` (default 2 s). During that time it MUST return the same response with the remaining `Retry-After`, without querying the database or the scheduler snapshot. Account recovery events MUST invalidate the cached entry.

#### Scenario: Burst of retries
- **WHEN** 50 requests for the same group and model arrive within 2 s after a no-account result
- **THEN** only the first performs account selection.

### Requirement: Per-key fast fail returns the identical response

When one API key receives at least `errors.fast_fail.threshold` (default 10) identical 503 responses within `errors.fast_fail.window_seconds` (default 30 s), further requests of that key for the same model in the window MUST receive the same status, body and remaining `Retry-After` before account selection and content moderation; requests for other models MUST be processed normally. Identical means the same status, `error.type` and message.

The following MUST be exempt and MUST never trigger it:
- asynchronous task polling;
- capabilities;
- DELETE;
- `/v1/usage`;
- `/v1/models`;
- idempotent replays.

Fast-failed requests MUST NOT count as account failures.

#### Scenario: Retry storm on a single-account group
- **WHEN** one key sends 200 requests per minute to a group whose only account is exhausted
- **THEN** after the tenth identical 503 within 30 s, the following requests return the same 503 immediately.

### Requirement: Failed turns end after a bounded number of attempts

The gateway MUST count consecutive exhausted failures of one conversation (API key, group, model and sticky session hash): no-account responses, 429 or 5xx after failover is exhausted, and the same failures written into a committed stream. Requests without a session hash are independent and MUST NOT be counted. From the `errors.turn_cap.failures`-th (default 5) failure in a row, the failure MUST be final:
- no `Retry-After`, and `x-should-retry: false`;
- `error.code` `server_is_overloaded`, in the HTTP body and in a committed stream's terminal frame;
- a 5xx status becomes 503; 429 stays 429.

A successful request of the conversation, or a gap longer than `errors.turn_cap.window_seconds` (default 120 s) between failures, MUST reset the count. A 200 response that carried an in-stream failure MUST NOT reset it. Every request MUST still go through normal account selection. Final answers MUST NOT feed the per-key fast fail, whose replay would add `Retry-After` again.

#### Scenario: Codex stops after five requests
- **WHEN** Codex sends a turn to a group whose accounts all fail
- **THEN** the first four failures carry `Retry-After`, the fifth is final and the turn ends after at most 5 requests.

#### Scenario: In-stream failures end the turn
- **WHEN** every attempt of a conversation fails after output has started
- **THEN** the fifth terminal frame carries `server_is_overloaded` and Codex does not retry it.

#### Scenario: Recovery is not delayed
- **WHEN** the group recovers while a conversation's count is at the cap
- **THEN** the next request succeeds and the count resets.

### Requirement: Upstream balance problems are hidden from users

Upstream `BALANCE` failures, including image-account 402 responses, MUST NOT be shown to end users. When they leave no available account, the user MUST receive the standard 503 `Service temporarily unavailable` with `Retry-After`. Administrators MUST see the original upstream message in operations views and alerts. An administrator's error passthrough rule that matches the upstream response MUST take precedence, on the gateway and OpenAI routes alike.

#### Scenario: Image account out of credit
- **WHEN** the only image account returns 402 `Upstream image account has insufficient balance`
- **THEN** the user receives 503 `Service temporarily unavailable` and the administrator sees the 402 message.

### Requirement: Clear user balance errors

When the user's own balance is insufficient, both the API-key pre-check and the post-wait billing check MUST return:
- status 403 with code `INSUFFICIENT_BALANCE`;
- message `Insufficient balance on your xy2api account, please recharge`.

OpenAI-compatible endpoints MUST nest this as `{"error":{"type":"insufficient_balance","code":"INSUFFICIENT_BALANCE","message":…}}`. When the balance is positive but already reserved by in-flight requests, the gateway MUST return:
- status 429;
- `error.type` `rate_limit_error` and code `concurrent_balance_reserved`;
- a message stating the available and reserved amounts;
- `Retry-After: 5`.

#### Scenario: Concurrent requests with a small balance
- **WHEN** a user with $0.80 balance has $0.74 reserved by in-flight requests and a new request is estimated at $0.30
- **THEN** the new request receives 429 `concurrent_balance_reserved` with `Retry-After`, not 403 `insufficient balance`.

### Requirement: Cache-aware reservation estimate

The in-flight reservation estimate MUST:
- price the cached share of input at the cache-read price, for requests whose session recently reported cache reads;
- cap estimated output tokens at the model's recent P99 output, with a floor of 1024 tokens and never above `max_tokens`.

Replaying seven days of production requests MUST show at least 90 % fewer rejections of users with a positive balance, without increasing the negative-balance amount.

#### Scenario: Cached Codex conversation
- **WHEN** a session's last request read 90 % of its input from cache
- **THEN** the reservation for the next request prices about 90 % of its input at the cache-read price.

### Requirement: Admission before moderation for asynchronous images

For asynchronous image submission, the per-instance and per-key admission checks MUST run before content moderation. A submission rejected for admission MUST NOT trigger moderation.

#### Scenario: Re-submitted batch
- **WHEN** a client re-submits a task that is rejected by the per-key in-flight limit
- **THEN** the response is `429 too_many_tasks` with `Retry-After` and no moderation call is made.
