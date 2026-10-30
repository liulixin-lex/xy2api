## 1. Baseline and contracts

- [x] 1.1 Confirm the baseline: only JSON import exists; user authorized new email/password/TOTP direct login/import.
- [x] 1.2 Add route/API/schema/data-flow maps for support tickets and managed OpenAI credentials.
- [x] 1.3 Verify all new admin handlers enforce full-admin/operator boundaries and all secrets stay outside account DTOs, logs and task status.

## 2. Website support tickets

- [x] 2.1 Add append-only migration, checksum, repository and concurrency-safe ownership/limits.
- [x] 2.2 Add settings/defaults, public opt-in flag, management request fields and audit diff.
- [x] 2.3 Add user and admin handlers/routes with authorization, rate limits and feature fail-closed behavior.
- [x] 2.4 Add frontend routes, views, components, nav badges, settings editor and zh/en locale entries.
- [x] 2.5 Add backend unit/integration and frontend behavior/security tests.
- [x] 2.6 Add selectable `support_tickets` view/manage permission when creating and editing an operator; verify denied/read-only/manage routes and controls, with no access by default.

## 3. Managed OpenAI credential and TOTP rotation

- [x] 3.0 Add encrypted local-worker direct login tasks and idempotent server-side session import.
- [x] 3.1 Add append-only encrypted credential/task schema and separate repository/service contracts.
- [x] 3.2 Add full-admin, step-up protected credential save/status/export and worker-authenticated claim/phase/finish/recovery APIs.
- [x] 3.3 Implement worker/runtime and encrypted durable journal with no automatic mutation retries.
- [x] 3.4 Add per-account TOTP management dialog, safe task status/reconciliation actions and explicit no-store export flows.
- [x] 3.5 Add failure-injection, access control, redaction and frontend secret-lifecycle tests.
- [x] 3.6 Document worker, persistent storage, encryption-key backups, rollout, uncertain-task recovery and rollback limits.

## 4. Final verification

- [x] 4.1 Regenerate Wire and verify generated tree is clean.
- [x] 4.2 Run focused Go/backend integration and frontend test/lint/typecheck/build.
- [x] 4.3 Run `python3 tools/check_docs_consistency.py`, `python3 tools/upstream-sync/sync.py scheduler-diff`, `python3 -m unittest discover -s tools/tests -p 'test_*.py' -v`, and clean-tree `sync.py audit`.
- [x] 4.4 Complete a focused independent security review of owner isolation, admin privilege gates, worker credentials, encrypted storage and ambiguous-rotation recovery.
