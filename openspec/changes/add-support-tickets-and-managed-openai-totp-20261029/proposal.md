## Why

XY2API 0.4.12 already has platform-user TOTP login, setup and disable, but it does not provide the separate support-ticket workflow or managed OpenAI account TOTP lifecycle found in the current Sub2API repository. The existing account importer and the platform user's second factor are separate concerns; this change must not replace either flow.

## What Changes

- Add an opt-in website support-ticket feature with user conversations, admin triage, configurable categories/notice, unread badges, status changes, limits and abuse controls.
- Add a dedicated managed OpenAI credential vault and administrator workflow for saving login credentials, rotating an account TOTP secret, reconciling an interrupted rotation, and explicitly exporting saved credentials.
- Add an authenticated local worker contract for managed-account login and TOTP operations. Persist candidates before upstream activation and keep uncertain tasks locked for reconciliation.
- Add localized user/admin pages, settings, API routes, migrations, tests, deployment notes and current docs.
- Keep platform-user TOTP endpoints/semantics unchanged and never include managed OpenAI login secrets in ordinary account/list/export APIs.

## Capabilities

### New Capabilities

- `website-support-tickets`: opt-in user/admin support conversations with ownership isolation, unread markers, configurable limits and safe text rendering.
- `managed-openai-totp`: encrypted managed OpenAI login credentials, local-worker TOTP rotation, recovery/reconciliation and explicit secret export.

### Modified Capabilities

None.

## Impact

- Backend: new support-ticket and managed-account credential services/repositories/handlers; existing settings, account management and server route wiring.
- Database: append-only migrations for ticket conversations, encrypted managed credentials and rotation tasks; append migration checksums.
- Frontend: user/admin ticket routes and views, admin account TOTP management, settings controls, navigation badges and zh/en messages.
- Operations: a local worker/runtime and durable private recovery directory are required before rotation can be enabled. Mock-based tests do not establish compatibility with OpenAI's non-public web MFA protocol.
- Security: managed credentials are separately encrypted at rest, restricted to full administrators, excluded from normal account payloads and logs, and only returned by explicit no-store export calls.

## Compatibility

- Website tickets default off; unavailable settings or database reads fail closed.
- Existing platform-user TOTP login and step-up behavior do not change.
- No existing account credential JSON or published migration is rewritten.
