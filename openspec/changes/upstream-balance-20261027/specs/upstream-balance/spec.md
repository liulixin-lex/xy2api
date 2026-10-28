# 上游余额规格增量

## ADDED Requirements

### Requirement: Eligible accounts

The system MUST collect upstream balances only for accounts whose type is `apikey`, whose platform is concrete, whose API key is non-empty and whose effective base URL is non-empty, passes outbound URL validation and is not an official API domain. For kimi and deepseek, an account MUST be treated as official exactly when its effective base URL uses `https` and its host equals the host of the platform's default base URL; adaptive CN accounts MUST use the Chat Completions base URL.

#### Scenario: Official domain
- **WHEN** an apikey account's base URL host is an official API domain
- **THEN** the read endpoint reports `state=not_applicable` with null value fields
- **AND** no upstream request is sent for it.

#### Scenario: Relay Kimi account
- **WHEN** a kimi pay-as-you-go account's base URL is `https://relay.example.com/v1`
- **THEN** it is eligible for the upstream balance column
- **AND** the CN balance check, the manual CN balance query and the channel monitor do not send its key to the Moonshot balance endpoint.

### Requirement: Collection without side effects

Collection MUST NOT write the `accounts` table or emit scheduler outbox events. Results MUST be stored only in Redis. At most one collection round MUST start per minute across all instances, and each account MUST be attempted at most once per round. When Redis is unavailable, collection MUST pause.

#### Scenario: Multi-instance
- **WHEN** two instances tick within the same minute
- **THEN** only one of them runs a collection round.

#### Scenario: Read path
- **WHEN** an admin calls `GET /api/v1/admin/accounts/upstream-balances`
- **THEN** zero upstream requests, zero `accounts` writes and zero outbox events occur.

### Requirement: Upstream identification and parsing

The system MUST identify sub2api upstreams by `GET /v1/usage` (HTTP 200, `mode` in {`quota_limited`, `unrestricted`}, boolean `isValid`) and new-api upstreams by the public `GET /api/status` at the site origin (`success:true` with `data.quota_per_unit`). HTTP 401, 403, 429 and non-JSON error pages MUST NOT mark an upstream unsupported. new-api `success:false` (including HTTP 200) MUST be treated as an authentication failure, never as a value. A new-api key MUST be treated as unlimited only when the site displays quota in USD or CNY and `hard_limit_usd` equals 100000000 exactly; in TOKENS display mode that value MUST NOT be shown as unlimited. Upstream negative amounts MUST be kept as returned. Without an access token, the new-api basis MUST be reported as unknown.

#### Scenario: Finite TOKENS key equal to the sentinel
- **WHEN** a new-api site displays quota in TOKENS and a limited key's billing hard limit is exactly 100000000
- **THEN** the item is not reported as unlimited.

#### Scenario: Version segment in base URL
- **WHEN** the base URL is `https://h/v1beta`
- **THEN** the sub2api usage URL is `https://h/v1/usage` with the query parameters merged through URL values
- **AND** new-api `/api/*` URLs are built from `https://h`.

#### Scenario: new-api key rejected
- **WHEN** new-api billing returns 401 and `/api/usage/token/` reports `total_available > 0` and the key is not expired
- **THEN** the result is `reason=auth_failed` and no amount is recorded.

### Requirement: Rate limits toward the upstream

Calls to new-api `/api/usage/token/` MUST be limited to 401 disambiguation, and MUST be at least 75 seconds apart per site origin across all instances. A 429 MUST be honoured through `Retry-After`, or a 5-minute cooldown when the header is missing; a new-api 429 MUST stop requests from every account and instance to that origin until the deadline, and rate-limit deadlines MUST NOT be shortened by the cadence slack. Network errors and 5xx MUST back off exponentially up to 10 minutes. A sub2api account whose single response takes more than 3 seconds MUST be slowed to once every 5 minutes until 3 consecutive responses arrive within 1 second.

#### Scenario: Many accounts on one new-api site
- **WHEN** 30 accounts on one new-api origin all receive 401 from billing in the same round
- **THEN** at most one `/api/usage/token/` request is sent to that origin in any 75-second window.

#### Scenario: Site-wide cooldown
- **WHEN** one account receives `429` with `Retry-After: 1200` from a new-api origin
- **THEN** no account on any instance sends a request to that origin for 1200 seconds.

### Requirement: Read endpoint contract

`GET /api/v1/admin/accounts/upstream-balances?ids=` MUST accept at most 200 ids and return 400 `UPSTREAM_BALANCE_TOO_MANY_IDS` beyond that. Each item MUST report `state` `ok` only when a success record exists for the account's current identity fingerprint (platform, type, effective base URL, API key, access token, user id, proxy id and proxy connection details, effective header overrides). After a failure following a success, the item MUST keep the last success value with the failure `reason`. When Redis is unavailable, every eligible item MUST be `unavailable` with `reason=redis_unavailable` and HTTP 200. The response MUST include `server_time`.

#### Scenario: Key changed
- **WHEN** an admin changes an account's API key, base URL, access token, user id or proxy
- **THEN** the previous value is no longer returned for that account until a new collection succeeds.

### Requirement: new-api access token handling

`newapi_access_token` MUST be a sensitive credential: responses MUST omit it and report `credentials_status.has_newapi_access_token`. Omitting the key on update MUST keep the stored value, and `null` MUST clear it. When an update changes the effective base URL origin or the proxy and does not carry a new token string, the stored token MUST be deleted. A bulk update that changes the effective base URL origin (including a protocol switch) or the proxy of any selected account without a token MUST clear the token of every selected account. Duplicating an account MUST NOT copy the token. `newapi_user_id`, when present, MUST be 1–20 digits, otherwise 400 `NEWAPI_USER_ID_INVALID`. Keys and tokens MUST NOT appear in logs, error messages, `upstream_balance:*` Redis values or API responses.

#### Scenario: Base URL moved to another host
- **WHEN** an account with a stored token is updated from `https://a.example.com` to `https://b.example.com` without a new token
- **THEN** the stored token is removed and no request carrying it is sent to `b.example.com`.

### Requirement: Accounts page column

The accounts page MUST show an `upstream_balance` column, visible by default, reading the endpoint every 30 seconds for the current page's ids in chunks of at most 200, and immediately on page, filter or id-set changes, when a dialog closes, when the tab becomes visible and when the column is shown again. It MUST NOT poll while a dialog or menu is open, the tab is hidden or the column is hidden, and MUST NOT offer a manual refresh. A value MUST be grey when it is older than 3 minutes and a later attempt failed, or older than 10 minutes, using server time.

#### Scenario: Clock skew
- **WHEN** the browser clock is 5 minutes ahead of the server and the value was observed 60 seconds ago by server time
- **THEN** the value is not grey.

#### Scenario: Relay CN account usage cell
- **WHEN** a kimi or deepseek account uses a non-official base URL
- **THEN** the usage column hides the CN balance sub-cell and its placeholder says the balance is in the upstream balance column.
