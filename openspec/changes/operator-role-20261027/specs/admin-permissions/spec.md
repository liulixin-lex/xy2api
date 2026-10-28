# 普通管理员规格增量

## ADDED Requirements

### Requirement: Operator permissions

The system MUST accept `operator` as a value of `users.role` next to `admin` and `user`. An operator MUST be able to enter the admin console, and `IsAdmin` MUST keep meaning super admin. Each operator MUST hold, per module, one of none, `view` or `manage` (where `manage` includes `view`) for the 17 modules `dashboard`, `ops`, `users`, `groups`, `channels`, `accounts`, `share_scheduling`, `announcements`, `proxies`, `security_audit`, `redeem_codes`, `promo_codes`, `affiliates`, `orders`, `usage`, `audit_logs` and `subscriptions`, plus any of the five flags `users.balance_adjust`, `orders.refund`, `redeem_codes.issue`, `data.export` and `accounts.upstream_address`, all off by default.

Every route behind admin authentication, including the payment admin group, `/api/v1/pages`, the ops WebSocket and the usage-export download ticket, MUST have exactly one catalog row keyed by method and full path. For an operator, `AdminPermission()` MUST check the row before the compliance guard and the handler. A route with no row, or an empty full path, MUST be denied with 403 `ADMIN_PERMISSION_DENIED` and `metadata.required = "catalog"`. A `super_only` row MUST give 403 `SUPER_ADMIN_REQUIRED`. A missing level or flag MUST give 403 `ADMIN_PERMISSION_DENIED` with `metadata.required`. Super admins and the global admin API key MUST skip every catalog check.

The operator's user row and permissions MUST be read on every request, so that a change applies to the next request. A load failure MUST give 503 `OPERATOR_ACCESS_UNAVAILABLE`, and a stored value that does not decode MUST count as no permissions. On rows with a target or field rule, an operator request body MUST be one canonical JSON object with no two keys equal under case folding, otherwise 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.cause = "noncanonical"`.

#### Scenario: Unregistered route
- **WHEN** a route behind admin authentication has no catalog row
- **THEN** the route enumeration test on the production engine fails
- **AND** at run time an operator calling it gets 403 `ADMIN_PERMISSION_DENIED` with `metadata.required = "catalog"`.

#### Scenario: View-only module
- **WHEN** an operator with `accounts: view` sends `PUT /api/v1/admin/accounts/:id`
- **THEN** the response is 403 `ADMIN_PERMISSION_DENIED` with `metadata.required = "accounts:manage"`
- **AND** nothing is written.

#### Scenario: Revoked operator
- **WHEN** a super admin removes an operator's `ops` module while the operator holds an open `/ops/ws/qps` connection
- **THEN** the operator's next HTTP request to an ops route is denied
- **AND** the WebSocket closes within 30 seconds.

#### Scenario: Folded duplicate key
- **WHEN** an operator with `users: manage` sends `{"balance":null,"Balance":1000000}` to `POST /api/v1/admin/users`
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.cause = "noncanonical"`
- **AND** no user is created.

### Requirement: Explicit actor for protected service functions

The account, user and group write functions that carry operator rules MUST run those rules for an operator scope, and MUST skip them only for the super-admin marker set by admin authentication or for the system marker set with `operatoraccess.WithSystemActor`. On the production service instance, a call with none of the three MUST return 503 `ADMIN_ACTOR_MISSING` before its first write. No header, query value or gin key MUST be able to set a marker.

#### Scenario: Bare context
- **WHEN** code calls `UpdateAccount` on the production admin service with `context.Background()`
- **THEN** it returns 503 `ADMIN_ACTOR_MISSING`
- **AND** the account is unchanged.

### Requirement: Target protection

An operator MUST act only on users whose role is `user` and who are not the operator. Target users MUST be resolved from path and body user ids, from API key, subscription and order owners, from redeem-code users, and from the members and per-user rate rows of a group. When any target fails, the whole request MUST be refused with 403 `ADMIN_TARGET_FORBIDDEN`. Batch user updates by an operator MUST NOT accept `all: true`, and the service MUST re-check the targets on the ids it is about to write.

#### Scenario: Mixed batch
- **WHEN** an operator with `users: manage` sends `POST /api/v1/admin/users/batch-status` with the ids of a plain user and of a super admin
- **THEN** the response is 403 `ADMIN_TARGET_FORBIDDEN`
- **AND** neither user is changed.

#### Scenario: Self
- **WHEN** an operator with `users: manage` edits their own user through `PUT /api/v1/admin/users/:id`
- **THEN** the response is 403 `ADMIN_TARGET_FORBIDDEN`.

#### Scenario: Group with protected members
- **WHEN** an operator with `groups: manage` deletes a group in which an operator holds a subscription
- **THEN** the response is 403 `ADMIN_TARGET_FORBIDDEN`.

### Requirement: Funding flags

Without `users.balance_adjust`, an operator MUST NOT change balances, grant, extend, reset or restore subscriptions, change any group, plan, user-group or affiliate price field, or put an account into a group or take it out. These rules MUST run in the service on the decoded input. When such an operator creates a user, the user MUST start at balance 0 whatever the default-balance setting is, MUST receive no default subscription, and MUST NOT receive an exclusive group or a system-reserved email domain. Such an operator MUST create accounts with no group, including the platform default group. Such an operator MUST NOT delete a group that still has accounts: the delete MUST answer 409 `GROUP_NOT_EMPTY` and change nothing.

Without `orders.refund`, an operator MUST NOT refund, query refunds, retry fulfilment or process affiliate withdrawals. Without `redeem_codes.issue`, an operator MUST NOT generate redeem or promo codes or batch-update redeem codes. Without `data.export`, an operator MUST NOT create or download exports, and the admin-scope download MUST re-check `usage:view` and `data.export` when the ticket is consumed.

#### Scenario: Create user without the flag
- **WHEN** the default-balance setting is 5 and the default subscriptions are configured
- **AND** an operator with `users: manage` and without `users.balance_adjust` creates a user without a `balance` field
- **THEN** the user is created with balance 0 and no subscription.

#### Scenario: Group limit set to null
- **WHEN** an operator without `users.balance_adjust` sends `{"daily_limit_usd": null}` to `PUT /api/v1/admin/groups/:id` for a group with a daily limit
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.field = "daily_limit_usd"`.

#### Scenario: Account placement
- **WHEN** an operator with `accounts: manage` and without `users.balance_adjust` creates an account with `group_ids`
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.field = "group_ids"`.

#### Scenario: Create-and-redeem with a padded existing code
- **WHEN** an operator with `users.balance_adjust` sends `POST /api/v1/admin/redeem-codes/create-and-redeem` with `code` set to an existing unused code padded with spaces
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.field = "code"` and the existing code stays unused.

#### Scenario: Export after revoke
- **WHEN** an operator obtains a download ticket and a super admin then removes `data.export`
- **THEN** the download with that ticket is refused.

### Requirement: Upstream address flag

Without `accounts.upstream_address`, responses to an operator MUST NOT contain account base URLs, custom base URLs or channel-monitor endpoints, and error detail in account-related responses and SSE events MUST be replaced by the fixed text `upstream error details hidden`. Such an operator MUST NOT write any network field: account address keys, `proxy_id`, `type`, `api_protocol`, `account_mode` or `aws_region` on update; address keys or `proxy_id` on create; the model-sync preview `base_url`; or the protocol, host, port, user name, password, fallback mode or backup chain of a proxy, including proxy creation. Such writes MUST give 403 `OPERATOR_NETWORK_CHANGE_FORBIDDEN` with `metadata.cause = "address"`. An absent address key on update MUST keep the stored value.

With the flag, an operator MUST be able to see the address and to write these fields on `apikey`, `upstream` and `bedrock` accounts and on proxies, subject to all of the following:
- every new address MUST pass a write-time public-address check that refuses metadata host names and literal or resolved private, loopback, link-local, CGNAT and ULA addresses, and treats a resolution failure as private; failure gives 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.cause = "private_address"`;
- a change of any network identity key MUST come with every stored secret entered again or cleared, otherwise 400 `ACCOUNT_API_KEY_REQUIRED`;
- a proxy in use MUST keep its network fields other than the password.

For every operator, flag or not, a network change on an `oauth`, `setup-token` or `service_account` account MUST give 403 `OPERATOR_NETWORK_CHANGE_FORBIDDEN` with cause `refreshable_account`. A network field in a bulk update MUST give the same code with cause `bulk`. An address change through `UpdateAccountExtra` MUST also be refused. An `aws_region` that does not match `^[a-z]{2}(-[a-z]+)+-[0-9]+$` MUST give 400 `ADMIN_FIELD_FORBIDDEN` with cause `invalid_region`.

#### Scenario: Flag off
- **WHEN** an operator without the flag reads `GET /api/v1/admin/accounts/:id` for an API-key account
- **THEN** the response has no `credentials.base_url`
- **AND** a later `PUT` with a different `credentials.base_url` gives 403 `OPERATOR_NETWORK_CHANGE_FORBIDDEN` and writes nothing.

#### Scenario: Private address with the flag
- **WHEN** an operator with the flag sets an API-key account's base URL to `https://relay.example.com/v1`, whose name resolves to `10.0.0.5`
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.cause = "private_address"`
- **AND** nothing is written.

#### Scenario: Address change without the key
- **WHEN** an operator with the flag changes an API-key account's base URL to a public address and sends no new `api_key`
- **THEN** the response is 400 `ACCOUNT_API_KEY_REQUIRED` with `metadata.field = "api_key"`
- **AND** the stored key is never sent to the new address.

#### Scenario: OAuth account with the flag
- **WHEN** an operator with the flag changes the proxy of an `oauth` account
- **THEN** the response is 403 `OPERATOR_NETWORK_CHANGE_FORBIDDEN` with `metadata.cause = "refreshable_account"`.

### Requirement: Redaction for operators

Operator responses MUST be redacted by the catalog row before they are sent:
- user API keys MUST be masked;
- proxy passwords MUST be removed;
- every value of account `header_overrides` and of channel-monitor and template `extra_headers` MUST be shown as `******`, and a submitted `******` MUST keep the stored value;
- redeem and promo codes MUST be masked without `redeem_codes.issue`, and order recharge codes without `orders.refund`;
- in audit-log entries of routes an operator cannot call (`super_only` rows, routes outside the catalog, auth and user routes), the request body and query MUST be hidden;
- in the other audit-log request bodies, header-map values, `rt` and `proxy_url` values and URL userinfo MUST be masked always; without `accounts.upstream_address`, address values MUST be masked and every URL MUST be scrubbed, and a query holding a URL MUST be hidden; a body that does not parse as JSON MUST be hidden;
- for callers without `ops:view` and without `accounts.upstream_address`, upstream error messages and details in ops error views MUST be replaced by fixed text, and without `accounts.upstream_address` error-status channel-monitor history and run messages MUST be replaced by fixed text;
- machine codes such as `mixed_channel_warning` in error fields MUST be kept.

Redaction MUST keep numbers exact, MUST recompute `ETag` and `Content-Length`, and MUST answer 500 `ADMIN_REDACTION_FAILED` instead of the raw body when it fails. Searches MUST NOT reveal masked values: user search MUST skip the API-key match for operators, and without `redeem_codes.issue` redeem-code search MUST be email-only, promo-code search MUST be off and sorting by code MUST be refused.

#### Scenario: Header override round trip
- **WHEN** an operator reads an account whose `header_overrides` has `x-relay-key: secret`
- **THEN** the response shows `"x-relay-key": "******"`
- **AND** saving the form with `******` keeps `secret` stored.

#### Scenario: Super-admin-only audit body
- **WHEN** an operator with `audit_logs:view` reads the audit entry of a super admin's `PUT /api/v1/admin/settings/codex-ticket/proxies`
- **THEN** its `request_body` is `<request body hidden>` and it has no `extra.query`.

#### Scenario: Redeem search oracle
- **WHEN** an operator without `redeem_codes.issue` searches redeem codes with a string that has no `@`
- **THEN** the response is 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.field = "search"` and no codes.

### Requirement: Role changes

Operators MUST NOT change any role: a non-empty `role` from an operator MUST give 400 `ADMIN_FIELD_FORBIDDEN` with `metadata.field = "role"`, and `GET` and `PUT /api/v1/admin/users/:id/operator-permissions` MUST be `super_only`. The last super admin MUST NOT be changed to any other role, including `operator`. A super admin MUST NOT change their own role. Creating a super admin or operator, any role change into or out of `admin` or `operator`, and `PUT operator-permissions` MUST require step-up when step-up is enabled. A successful permissions update MUST write an audit entry `admin.operator_permissions.update` with the values before and after. `PUT operator-permissions` MUST reject unknown keys, module ids, values (including `none`) and flag ids with 400 `ADMIN_PERMISSION_INVALID`.

#### Scenario: Last admin
- **WHEN** the only super admin is changed to `operator`
- **THEN** the change is refused and the role stays `admin`.

#### Scenario: Appoint an operator
- **WHEN** a super admin with step-up enabled changes a user's role from `user` to `operator`
- **THEN** the change requires step-up.

### Requirement: Access bootstrap endpoint

`GET /api/v1/admin/me/access` MUST be available to every super admin and operator, and MUST NOT pass through the compliance guard or the permission catalog. It MUST return:
- `role` and `is_super_admin`;
- `modules`, with modules that have no level left out;
- `flags`, sorted ascending;
- `settings`, the subset of console settings the admin pages need;
- `custom_menu_items`, always an array.

For a super admin, `modules` MUST list all 17 modules as `manage`, `flags` MUST list all five flags, and `custom_menu_items` MUST be the configured items. For an operator, `custom_menu_items` MUST be empty. A failing settings source MUST leave its fields at false or 0 and still answer 200.

#### Scenario: Before compliance acknowledgement
- **WHEN** an operator who has not accepted the compliance terms calls `/me/access`
- **THEN** the response is 200 with the operator's modules and flags
- **AND** other admin routes still answer 423 until the operator accepts.

### Requirement: Backend mode and compliance

In backend mode, operators MUST be able to sign in, refresh tokens, use passkeys, read `/auth/me`, complete step-up and use the user routes, like super admins. Operators MUST accept the deployment compliance terms per user, exactly like super admins.

#### Scenario: Backend mode login
- **WHEN** backend mode is on and an operator signs in with a password
- **THEN** the sign-in succeeds
- **AND** a plain user's sign-in is still refused.

### Requirement: Migration and rollback

Migration `282_admin_operator_permissions.sql` MUST only create the table `admin_operator_permissions` (`user_id` primary key referencing `users`, `permissions` JSONB, `updated_by`, `updated_at`). It MUST NOT alter `users`, the ent schema or the `/auth/me` and `/admin/settings` contract snapshots. A row MUST be read only while its user's role is `operator`. The documented manual rollback MUST, in one transaction, demote operators to `user`, drop the table and delete the migration's `schema_migrations` row, so that a later upgrade creates the table again.

#### Scenario: Rollback and re-upgrade
- **WHEN** the rollback block is run and the new binary is started again
- **THEN** migration 282 runs again and the table exists, empty
- **AND** a newly appointed operator's permissions load without 503.

#### Scenario: Demoted operator
- **WHEN** an operator with a permissions row is changed to `user`
- **THEN** the row is kept and ignored
- **AND** re-appointing the user shows the old set in the permissions table, which applies only after it is saved.
