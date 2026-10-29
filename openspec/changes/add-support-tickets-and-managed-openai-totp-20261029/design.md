## Scope and terminology

“Platform TOTP” is the existing user's XY2API login second factor. “Managed OpenAI TOTP” is a separate email/password/TOTP credential used by an administrator's managed OpenAI OAuth parent account for upstream reauthentication. The two stores, APIs and user experiences remain separate.

“Direct 2FA login/import” is an existing XY2API capability per the user's clarification. This change does not redesign it. The implementation must verify the current branch's exact entry point and reuse its account-creation integration when a managed credential is first saved.

## Website support tickets

### Data and configuration

- Add `support_tickets` and `support_ticket_messages`, with user ownership FK, bounded title/body/category/status values, timestamps, per-side unread markers and indexes for owner/activity and status/activity.
- Store `support_ticket_enabled` (false by default) and a JSON config in `settings`: categories, maximum open tickets per user and an optional notice. Missing values use conservative defaults; malformed config or settings-store errors fail closed.
- Messages are plain text. The browser creates anchors only for validated `http:`/`https:` URLs; no message body is rendered with HTML.

### User behavior

- List/open/create/reply/close/reopen only the authenticated user's tickets. Repository queries include `user_id`; an unknown or foreign ID returns the same not-found response.
- Enforce open-ticket and daily-create limits, per-user message rate limits, title/body/category bounds and a per-ticket user-message cap inside write transactions.
- User messages put the ticket back in pending and mark it unread for admins. Opening a conversation clears the caller's unread marker only if the message count has not changed since read.
- Ticket detail polls while visible and stops polling when unmounted.

### Admin behavior

- Only full administrators manage tickets. Operator-scoped admins and observer/read-only account roles have no ticket access.
- List/filter/search, summary badge, inspect author context, reply with an explicit next status, set processing/closed state and delete.
- Replies mark the ticket unread for its owner. Status and messages change atomically.
- Support-ticket endpoints are placed behind the existing admin auth, permission and compliance middleware, and user endpoints behind existing authenticated user middleware and panel rate limits.

## Managed OpenAI credentials and TOTP rotation

### Credential storage

- Use a dedicated table keyed by `account_id`, not `accounts.credentials` or `accounts.extra`.
- Save login email, AES-256-GCM encrypted password and TOTP secret, proxy source/id, config revision and timestamps. Decrypt only when creating an authenticated worker claim or an explicit no-store administrator export.
- Require a managed OpenAI OAuth parent account and exact normalized identity match. Shadows, API keys and non-OpenAI accounts are rejected.
- Saving credentials requires a full administrator with a recent step-up grant. Credential configuration updates use revision/CAS and are blocked while a rotation is uncertain or active.

### Worker and task state

- Worker traffic uses a separate high-entropy worker secret and task-scoped worker ownership. Worker claims are bounded, short-lived, audited by safe operation metadata, and served with `Cache-Control: no-store`.
- Rotation task transitions are `queued → running → enrolling → prepared → activating → verifying → succeeded|failed`; timeout/lost acknowledgement after activation enters `uncertain`. Only one active task may exist per account.
- Before any upstream mutation, require a writable durable local recovery directory. Save the old credential snapshot encrypted. On receiving a candidate, fsync the encrypted candidate and bind session ID in the recovery journal, then commit the encrypted candidate to PostgreSQL and acknowledge it before sending activation.
- Never auto-retry enrollment or activation. An uncertain task locks new rotations, credential edits and automatic reauthentication until a full administrator chooses “verify new” or “verify old”. Network errors, security challenges and non-MFA failures are not evidence that an old key was revoked.
- Finish rotation only after a fresh login confirms candidate acceptance and (where the upstream protocol provides a definitive signal) old-key rejection. If the upstream cannot provide definitive rejection, retain an explicit unconfirmed state rather than claim full verification.
- Task/status APIs never return candidate, old secret, password, cookie, raw upstream response, journal content or worker credentials.

### Export and UI

- Show safe task status, action and normalized error class. Provide explicit source selection (`current`, `candidate`, `previous`) and password opt-in for downloads. All export responses are no-store and audited without secret values.
- Clear secrets and QR data from UI state on close, account switch, navigation and unmount; ignore late requests from a closed dialog.
- UI explicitly says this manages the upstream OpenAI account, not platform-user 2FA.

### Runtime boundary

- Reuse the upstream rotation state machine and tests, but implement a XY2API-owned worker adapter that fits the repository's release/deployment model. Do not silently require a hosted login provider.
- If the matching login runtime or persistent recovery storage is unavailable, refuse to queue a rotation. Build/CI and local mock validation remain independent from real upstream acceptance.

## Tests and verification

- Ticket unit/integration tests: enabled/disabled fail-closed, owner isolation across every route/repository operation, status/unread transitions, limits under concurrent writes, escaping/link safety, pagination and admin permission boundaries.
- Managed-credential tests: encryption/decryption separation, identity/account eligibility, step-up, revision CAS, secret redaction, no-store headers, worker auth/ownership and invalid transitions.
- Rotation failure injection: journal unavailable before enrollment; DB acknowledgement lost; activation timeout; worker disappears; candidate recovery; candidate cannot be overwritten; verify-new/verify-old; no second enrollment on recovery; config edits blocked until resolution.
- Frontend tests: opt-in route guards, conversation behavior, safe links, admin filters, secret clearing and stale-response suppression.
- Run Go unit/race and relevant repository integration tests, frontend typecheck/lint/tests/build, generated Wire check, migration checksum/docs consistency and upstream-sync policy checks.
- No real OpenAI login or MFA activation in default CI.

## Rollout and rollback

- Tickets remain off until explicitly enabled after upgrade.
- Managed OpenAI rotation is unavailable until the API, matching local worker/runtime and persistent recovery volume are all ready. Deploy these components together and validate with an isolated authorized account.
- Back up database, `TOTP_ENCRYPTION_KEY`, and the full encrypted recovery directory. Do not clear uncertain tasks or recovery journals to roll back binaries.
- Binary rollback does not reverse an upstream TOTP change. Reconcile active tasks and update the stored current TOTP before switching to older software.
