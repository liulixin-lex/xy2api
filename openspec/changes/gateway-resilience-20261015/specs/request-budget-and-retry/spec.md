# Request budget and retry

## ADDED Requirements

### Requirement: One budget per request

Every gateway request MUST carry a single budget with:
- a maximum number of upstream attempts, `budget.max_attempts` (default 4), counting the first attempt, account switches and same-account retries;
- for text endpoints, a pre-output deadline of `budget.pre_output_budget_seconds` (default 240 s) after the request starts.

Every retry path MUST draw from this budget: OpenAI, Anthropic and Gemini handler failover loops, same-account retries, Gemini in-service retries, and passthrough transport retries. The effective switch limit MUST be the smaller of the budget and the existing `max_account_switches`. Image endpoints and asynchronous image tasks MUST NOT be subject to the pre-output deadline.

#### Scenario: Four attempts at most
- **WHEN** four consecutive attempts fail with `TRANSIENT`
- **THEN** no fifth attempt is made and the request ends with the last failure.

#### Scenario: Deadline reached before output
- **WHEN** 240 s have passed without a semantic byte and an attempt is still waiting
- **THEN** the attempt is cancelled and the request ends:
  - with the uncommitted error response, if the SSE headers were not committed;
  - with a protocol terminal frame, if they were.

#### Scenario: A slot wait ends with the budget
- **WHEN** a text request waits for an account slot whose wait plan allows 120 s while 30 s of the pre-output budget remain
- **THEN** the wait ends after at most 30 s and the request ends as for an exhausted budget.

#### Scenario: Images are exempt from the deadline
- **WHEN** a 4K image generation takes 200 s
- **THEN** it is neither cancelled by the pre-output deadline nor by a text first-output timeout.

### Requirement: Same-account retries are rare and bounded

Same-account retries MUST only occur for `CAPACITY_SHED` outcomes, for status codes configured on pool-mode accounts and for the single web_search declaration replay below, and at most `budget.same_account_retry_max` (default 1) times per account per request. Account-level outcomes MUST switch accounts without a same-account retry. Account-level outcomes are:
- `BALANCE`, `AUTH` and long rate limits;
- 504 and 524;
- any failure whose attempt took 60 s or longer.

#### Scenario: Insufficient balance is not retried on the same account
- **WHEN** a pool-mode account returns 403 `insufficient balance`
- **THEN** the gateway switches to another account without retrying the same account.

#### Scenario: Web search history rejected by an API-key relay
- **WHEN** an OpenAI API-key account rejects a request before any output with "response protection is unavailable" and the request input replays `web_search_call` items without declaring web_search
- **THEN** the request is replayed once on the same account with the web_search declaration, if the budget allows another attempt
- **AND** after a successful replay the account gets the declaration up front for six hours; when the replay fails too, the request ends on that account with 502 and the reason, the account is not penalised and the session counts one failure.

### Requirement: Gemini in-place retries

Gemini compatibility services MUST retry an upstream failure on the same account at most `budget.gemini_same_account_retries` (default 1) times. Waits MUST be cancellable and MUST stop when the client disconnects.

#### Scenario: Client leaves during a Gemini back-off
- **WHEN** the client disconnects while a Gemini retry back-off is pending
- **THEN** the back-off ends immediately and no further upstream request is sent.

### Requirement: Back-off and upstream Retry-After

Back-off delays MUST include ±20 % jitter. An upstream `Retry-After` MUST be honoured only up to `budget.upstream_retry_after_cap_seconds` (default 60 s), and only within the remaining budget. Otherwise the gateway MUST switch accounts or end the request.

#### Scenario: Upstream asks for 120 s
- **WHEN** an upstream answers 503 with `Retry-After: 120`
- **THEN** the gateway does not wait 120 s on that account and instead switches accounts within the budget.

### Requirement: Capacity shedding switches source

A `CAPACITY_SHED` outcome MAY trigger at most one account switch, and only to an account whose base_url host differs from the failed account's. If no such account exists, the gateway MUST end the request with the no-available-account response.

#### Scenario: Shared relay overload
- **WHEN** an account on relay host H returns `server_is_overloaded` and the only alternatives are on host H
- **THEN** the gateway returns 503 with `Retry-After` instead of trying the other accounts on host H.
