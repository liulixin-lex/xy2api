# Upstream failure classification

## ADDED Requirements

### Requirement: Single classification for every upstream attempt

The gateway MUST classify the outcome of every upstream attempt into exactly one category: `BALANCE`, `AUTH`, `RATE_LIMIT_RESET`, `RATE_LIMIT`, `CAPACITY_SHED`, `TRANSIENT`, `FIRST_OUTPUT_TIMEOUT`, `STALL`, `CLIENT_CANCEL`, `CLIENT_ERROR`, `MODEL_UNSUPPORTED` or `SUCCESS`. The classification MUST use the same rules for HTTP responses, in-stream `response.failed`/`error`/`data: {"error":…}` events, network errors and locally detected timeouts. Message text and structured error codes MUST be evaluated before the HTTP status, so that balance signals carried by 400, 401, 402, 403 or 429 responses are recognised.

#### Scenario: Balance message wrapped in a 429
- **WHEN** an API-key account returns HTTP 429 with the body message `Insufficient account balance`
- **THEN** the attempt is classified as `BALANCE`, not `RATE_LIMIT`.

#### Scenario: Balance failure inside a stream
- **WHEN** an upstream returns HTTP 200 and later emits `response.failed` whose error code is `insufficient_quota`
- **THEN** the attempt is classified as `BALANCE`.

#### Scenario: Busy message inside a stream
- **WHEN** an upstream returns HTTP 200 and 40 seconds later emits `response.failed` with message `The service is busy. Please retry later.`
- **THEN** the attempt is classified as `RATE_LIMIT`.

#### Scenario: Relay timeout page
- **WHEN** an upstream returns HTTP 524 with a body containing `"retry_after":120`
- **THEN** the attempt is classified as `TRANSIENT` with `retry_after=120s`.

### Requirement: Category determines failover, same-account retry, penalty and session handling

Each category MUST map to a fixed policy:

| Category | Next account | Same-account retry | Account penalty | Session failure count |
|---|---|---|---|---|
| BALANCE | yes | never | balance state machine (hard) | immediate rebind |
| AUTH | yes | never | hard (counted for 403) | immediate rebind |
| RATE_LIMIT_RESET | yes | never | soft until reset; hard if reset > 300 s | no / rebind if hard |
| RATE_LIMIT | yes | never | soft, configured 429 cooldown | +1 |
| CAPACITY_SHED | at most one switch to a different base_url host | at most once per account | none | not counted |
| TRANSIENT | yes | never for 504/524 or attempts ≥ 60 s | soft, transient cooldown or capped retry_after | +1 |
| FIRST_OUTPUT_TIMEOUT | yes | never | none (OpenAI API-key accounts: per-model transient block of upstream 5xx) | +1 |
| STALL | no (output already delivered) | never | soft, 30 s | +1 |
| CLIENT_CANCEL, CLIENT_ERROR | no | never | none | not counted |
| MODEL_UNSUPPORTED | yes | never | existing account/model block | not counted |

#### Scenario: Request-scoped overload does not punish the account
- **WHEN** an attempt is classified as `CAPACITY_SHED`
- **THEN** the account receives no cooldown
- **AND** at most one same-account retry and at most one switch to an account with a different base_url host are allowed.

#### Scenario: Client errors never switch accounts
- **WHEN** an attempt fails with a context-length error
- **THEN** no other account is tried and the session binding is untouched.

#### Scenario: An upstream content-safety refusal is the request's fault
- **WHEN** an upstream answers 403 with code `request_check_blocked` ("Request rejected by preflight safety checks"), or another content-safety code, or a 400/403/451 whose text names a safety check and not the account
- **THEN** the attempt is `CLIENT_ERROR` with reason `content_policy`, the account takes no 403 strike or cooldown, no same-account retry (pool mode included) and no switch happen
- **AND** the client receives HTTP 400 `invalid_request_error` with code `content_policy_violation` and the redacted upstream text.

#### Scenario: Anthropic in-stream errors keep the status of their type
- **WHEN** an Anthropic relay sends an in-stream `rate_limit_error` before any output
- **THEN** the attempt is `RATE_LIMIT`: the account gets the soft 429 cooldown and the request switches accounts
- **AND** when no account is left the client receives 429 with `Retry-After` instead of 502; `overloaded_error` maps to 529 and `api_error` to 502.

### Requirement: Platform user balance is not an upstream failure

Errors produced by the gateway's own billing checks (user balance, subscription quota, in-flight reservation) MUST NOT be classified as upstream failures and MUST NOT affect any account state.

#### Scenario: User out of funds
- **WHEN** the API-key middleware rejects a request because the user's balance is zero
- **THEN** no upstream attempt is recorded and no account penalty is applied.

### Requirement: Configurable balance keywords

Administrators MUST be able to add balance keywords through `gateway_resilience_settings.failure.balance_keywords_extra` without a release. Unclassified upstream 4xx bodies MUST be sampled (redacted, truncated) into structured logs so that new keywords can be added.

#### Scenario: New relay wording
- **WHEN** an administrator adds `余额已用尽` to the extra keywords
- **THEN** the next upstream response containing that text is classified as `BALANCE`.
