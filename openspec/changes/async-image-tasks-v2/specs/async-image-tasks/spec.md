# 异步图片任务 v2 规格

## ADDED Requirements

### Requirement: Tasks run under their own scheduling ledger

An accepted task MUST NOT depend on the submit request's context. In `controlled` scheduling mode the task MUST dispatch normally after the 202 has been written, keeping the mode snapshot and scheduling request id taken at submit.

#### Scenario: Controlled mode
- **WHEN** a task is submitted while the scheduling mode is `controlled`
- **THEN** its dispatch does not fail with `context canceled` after the submit request ends.

### Requirement: Idempotent submission

With `Idempotency-Key`, one key per user and API key MUST start at most one generation for its lifetime.

#### Scenario: Lost 202
- **WHEN** the same key and an equivalent body are submitted again
- **THEN** the gateway answers 200 with the original task and `Idempotent-Replayed: true`, and no new generation starts.

#### Scenario: Reused key
- **WHEN** the same key is sent with a different body
- **THEN** the gateway answers 422 `idempotency_key_reused`.

#### Scenario: Refused submission
- **WHEN** a submission with a key is refused before the 202
- **THEN** the key is released and a retry may use it.

### Requirement: Billing conclusion on the task

Every task MUST carry the request id its usage is recorded under, and every terminal task MUST report `billing.status` consistent with the gateway ledger.

#### Scenario: Completed
- **WHEN** a task completes
- **THEN** `billing.status` is `charged`.

#### Scenario: Failed
- **WHEN** a task fails
- **THEN** `billing.status` is `charged` if a usage row exists, `pending` while a billing intent is unsettled or the ledger is unreachable (refreshed on later polls), else `not_charged`.

### Requirement: Interrupted tasks are concluded

A task whose instance stops MUST reach a terminal state within about three minutes; tasks MUST NOT be re-run.

#### Scenario: Instance crash
- **WHEN** a task's heartbeat is older than two minutes
- **THEN** the sweeper marks it `failed` with `error.code = task_interrupted` and a billing conclusion, and writes an ops error log row.

#### Scenario: Graceful shutdown
- **WHEN** the process receives SIGTERM
- **THEN** new submissions get 503 (not submitted), running tasks may finish within the drain timeout, and remaining tasks are concluded as `task_interrupted`.

#### Scenario: Late result
- **WHEN** a result arrives for a task already concluded
- **THEN** the first conclusion stands.

### Requirement: Storage failure after billing

Image offload MUST be retried three times. A task whose images cannot be stored MUST fail with `result_storage_failed` and `billing.status = charged`, and MUST NOT store base64 in Redis.

### Requirement: Admission and polling limits

Running tasks MUST be capped per API key and per instance (429 `too_many_tasks` with `Retry-After`); polling and deleting MUST be rate limited per key (429 with `Retry-After`).

### Requirement: Task management endpoints

- `DELETE /v1/images/tasks/{id}` MUST delete stored images and the record of a finished task and MUST answer 409 for a running task without cancelling it.
- `GET /v1/images/async/capabilities` MUST describe the features available to the calling key.
- `GET /v1/images/tasks/{id}?wait=N` MUST return as soon as the task finishes or after at most the configured long-poll cap.
- `Prefer: respond-async` on the synchronous image endpoints MUST submit a task (202, `Preference-Applied`) when async tasks are enabled and MUST be ignored otherwise.

### Requirement: Compatibility

Every 0.2.6 path, field and status MUST keep its meaning; new fields are additive and clients treat unknown statuses as running.
