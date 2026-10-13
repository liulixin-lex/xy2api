# Asynchronous Image Tasks

Asynchronous image tasks let clients submit long-running OpenAI-compatible image requests without keeping one HTTP connection open. This avoids proxy/CDN response timeouts such as Cloudflare 524 while preserving the existing image routing, billing, moderation, concurrency, and failover behavior.

## Endpoints

The authenticated gateway exposes both `/v1` paths and their existing no-prefix aliases:

```text
POST /v1/images/generations/async
POST /v1/images/edits/async
GET  /v1/images/tasks/{task_id}
```

The aliases are `/images/generations/async`, `/images/edits/async`, and `/images/tasks/{task_id}`.

Only OpenAI and Grok groups are supported. Requests use the same JSON or multipart payload as the corresponding synchronous endpoint. Streaming image requests are rejected because a polled task returns one final JSON result.

## Enabling the feature (object storage)

Asynchronous image tasks are **disabled by default** and gated on object storage. When the switch is off — or the S3 credentials are incomplete — the async endpoints return `404` and never create a task or write to Redis. This is deliberate: without offloading, large `b64_json` results (several MB each, e.g. `gpt-image-1`) would accumulate in Redis and exhaust its memory.

### From the admin UI (recommended)

**Admin → Backup → Async image object storage.** Saving the form takes effect immediately — the object-storage client is rebuilt on the next request, so there is no container restart.

Because the async image storage and the database backup share one S3 client, the form defaults to **reusing the backup S3 configuration**: it borrows the endpoint, region and credentials already configured above and keeps only its own bucket and prefix, so backups stay under `backups/` while images go to `images/`. Leave the bucket empty to use the backup bucket as well. Untick the box to point images at a completely separate account.

Saving requires step-up 2FA when that gate is enabled, for the same reason the backup S3 form does: changing the target redirects generated content to another account.

Turning the switch off stops new submissions but keeps already-accepted tasks pollable, so nothing in flight is stranded.

### From the config file

The admin setting takes precedence. When nothing has ever been saved there, the `image_storage` block in `config.yaml` is used instead, so deployments that enabled the feature before the admin UI existed keep working untouched.

Configure an S3-compatible object store (AWS S3, Cloudflare R2, Aliyun OSS, MinIO, …) in `config.yaml` (all keys also accept the `IMAGE_STORAGE_*` environment overrides):

```yaml
image_storage:
  enabled: true
  endpoint: "https://<account_id>.r2.cloudflarestorage.com"  # AWS 官方可留空
  region: "auto"
  bucket: "my-images"
  access_key_id: "..."
  secret_access_key: "..."
  prefix: "images/"
  force_path_style: false          # MinIO/path-style buckets set true
  public_base_url: ""              # set to return public_base_url/key直链; empty → presigned URL
  presign_expiry_hours: 24         # presigned link TTL when public_base_url is empty
  max_download_bytes: 33554432     # cap when re-hosting an upstream image URL (32MB)
```

When a task completes, each generated image is uploaded to the bucket and the result is rewritten to a compact form: `data[].url` points at the stored object (a permanent `public_base_url/key` link, or a time-limited presigned URL) and `b64_json` is removed. Only this small JSON is stored in Redis. A failed upload is retried three times (after 1 s, 3 s and 9 s); if it still fails the task is marked `failed` with `error.code = result_storage_failed` and `billing.status = charged` rather than persisting the raw base64.

To support a different vendor beyond the S3-compatible client, implement the `service.ImageStorage` interface (`Save(ctx, key, contentType, data) (url, error)`) and provide it in place of the S3 implementation.

### Troubleshooting: the endpoints return 404 after enabling

`404 async image tasks are not enabled` means `image_storage` did not resolve to a complete configuration, so the feature stayed off. The route exists either way — the 404 comes from the handler, not from an unregistered path, which makes it easy to mistake for a missing build.

Check the startup log for:

```text
WARN image_storage.enabled is true but object storage is not fully configured; async image tasks are disabled  missing_keys=[...]
```

`missing_keys` names exactly which credentials were empty when the config was loaded.

Note that releases **before v0.1.161 silently dropped `IMAGE_STORAGE_ENDPOINT`, `_BUCKET`, `_ACCESS_KEY_ID`, `_SECRET_ACCESS_KEY` and `_PUBLIC_BASE_URL`** when they were supplied only through the environment: those keys had no registered default, and viper cannot see an environment variable for a key it does not already know about. Deployments driven purely by `environment:` — which is what `deploy/docker-compose.yml` does by default — therefore reported `enabled: true` with empty credentials and 404'd on every async call. On an affected release the workaround is to also place the `image_storage` block in `/app/data/config.yaml` (copy it from `deploy/config.example.yaml`); once the keys exist in the file, the environment overrides apply normally.

Two further causes of a 404 that are unrelated to storage: the API key's group must be on the **OpenAI or Grok** platform (any other platform, or a key with no group at all, yields `Images API is not supported for this platform`), and a task may only be polled with the **same API key that submitted it** — polling with a different key of the same user returns `image task not found` by design.

## Submit a task

```bash
curl -i https://api.example.com/v1/images/generations/async \
  -H 'Authorization: Bearer sk-...' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-image-1",
    "prompt": "A lighthouse during a winter storm",
    "size": "1536x1024"
  }'
```

The server stores the initial task in Redis and responds with `202 Accepted`:

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1784092800,
  "expires_at": 1784179200,
  "poll_url": "/v1/images/tasks/imgtask_0123456789abcdef"
}
```

`Location` contains the polling path and `Retry-After: 3` provides the recommended polling interval.

## Poll a task

Use the same API key that submitted the task:

```bash
curl https://api.example.com/v1/images/tasks/imgtask_0123456789abcdef \
  -H 'Authorization: Bearer sk-...'
```

While work is in progress:

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1784092800,
  "expires_at": 1784179200
}
```

On success, `result` mirrors the synchronous image API body, except each image has been offloaded to object storage: `data[].url` points at the stored object and `b64_json` is stripped (so both URL and base64 upstream formats end up as compact stored links):

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "completed",
  "http_status": 200,
  "image_url": "https://...",
  "result": {
    "created": 1784092923,
    "data": [{"url": "https://..."}]
  },
  "created_at": 1784092800,
  "completed_at": 1784092923,
  "expires_at": 1784179323
}
```

For URL responses, `image_url` mirrors the first `data[].url` for simple clients. On failure, the task reaches `failed` and exposes the original OpenAI-compatible error object where available:

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "failed",
  "http_status": 502,
  "error": {
    "type": "api_error",
    "message": "Upstream request failed"
  },
  "created_at": 1784092800,
  "completed_at": 1784092923,
  "expires_at": 1784179323
}
```

All submit and poll responses include `Cache-Control: no-store`, preventing a CDN from caching the `processing` state. Tasks and results expire 24 hours after their latest state update. A task executes for at most 30 minutes.

Task ownership is scoped to both user and API key. Unknown task IDs and IDs owned by another key both return `404`, avoiding task-existence disclosure. Polling remains available when the completed generation used the key's remaining balance; normal authentication, disabled-key, user, IP, and group checks still apply.

## Version 2 additions

Version 2 only adds to the API above: every path, field and status of 0.2.6 keeps its meaning, so 0.2.6 clients need no change. Treat any status other than `completed` and `failed` as still running.

### Discover what the gateway supports

```bash
curl https://api.example.com/v1/images/async/capabilities -H 'Authorization: Bearer sk-...'
```

```json
{
  "object": "image.async.capabilities",
  "version": 2,
  "enabled": true,
  "platform": "openai",
  "endpoints": ["generations", "edits"],
  "prefer_respond_async": true,
  "idempotency": true,
  "billing_in_task": true,
  "delete": true,
  "long_poll_max_seconds": 30,
  "poll_rate_per_second": 10,
  "max_in_flight_per_key": 8,
  "task_ttl_seconds": 86400,
  "execution_timeout_seconds": 1800
}
```

A 0.2.6 gateway answers this path with 404, which is how a client tells the versions apart.

### Request id and billing on every task

The 202 and the task view carry `request_id`, the `usage_logs.request_id` the generation is billed under (`client:<X-Client-Request-ID of the submit response>`), and a `billing` object:

| `billing.status` | Meaning |
| --- | --- |
| `pending` | The task is still running, or the ledger has not settled yet; poll again. |
| `charged` | A usage row with `billing.request_id` exists. Always the case for `completed`. |
| `not_charged` | No usage was recorded: the upstream refused or failed before producing billable output. |

The conclusion for a failed task comes from the gateway's own ledger (`usage_logs`, plus unsettled rows of `usage_billing_intents` as `pending`), so it always matches what the account was billed. The task view also adds `model`, `endpoint` (`generations` or `edits`), `started_at` and `heartbeat_at`.

### Idempotent submission

Send `Idempotency-Key` (1–128 characters of `A-Z a-z 0-9 . _ : -`) to make a retry of a lost 202 safe. Keys are scoped to the user and API key and live as long as the task.

| Situation | Response |
| --- | --- |
| First submission | `202` as usual, the body echoes `idempotency_key` |
| Same key, same request | `200` with the existing task view plus `poll_url`, header `Idempotent-Replayed: true`. No second generation. |
| Same key, different request | `422 idempotency_key_reused` |
| Same key while the first submission is still being accepted | `409 idempotency_in_progress`, `Retry-After: 1` |

JSON bodies are compared byte for byte; multipart bodies are compared by their parts (field name, file name, content), so a client that rebuilds the form with a new boundary still matches. A submission refused before the 202 (validation, moderation, limits) releases its key.

### `Prefer: respond-async`

`POST /v1/images/generations` and `/v1/images/edits` with `Prefer: respond-async` (RFC 7240) behave like the `/async` paths and answer `202` with `Preference-Applied: respond-async`. When async tasks are disabled the preference is ignored and the call stays synchronous (`200`).

### Long polling

`GET /v1/images/tasks/{id}?wait=25` holds the request until the task finishes or `wait` seconds pass (capped by `image_tasks.long_poll_max_seconds`, default 30, far below proxy timeouts such as Cloudflare's 100–125 s).

### Deleting a finished task

`DELETE /v1/images/tasks/{id}` removes the stored images and the task record and returns `{"deleted": true}`. A task that is still `processing` returns `409 IMAGE_TASK_RUNNING`: deleting is not cancelling, the generation continues and is billed. Call it once the images have been downloaded; the bucket's lifecycle rule remains the safety net.

### Limits

| Limit | Default | Response |
| --- | --- | --- |
| Running tasks per API key | user concurrency × 2, at least 2 (`image_tasks.max_in_flight_per_key` overrides) | `429 too_many_tasks`, `Retry-After: 5` |
| Running tasks per instance | 256 (`max_in_flight_per_instance`) | `429 too_many_tasks`, `Retry-After: 5` |
| Polls and deletes per API key | 10 per second (`poll_rate_per_second`) | `429 too_many_requests`, `Retry-After: 1` |

Submission is still a normal gateway request: rate limits, balance checks and moderation apply before the 202, and every failure before the 202 means no task, no generation and no usage.

### Reliability

- **Heartbeat.** A running task refreshes `heartbeat_at` every 15 seconds.
- **Sweeper.** Every instance checks once a minute (one at a time, via a Redis lock) for tasks whose heartbeat is older than two minutes, and concludes them as `failed` with `error.code = task_interrupted` and the billing conclusion from the ledger. Tasks are never re-run: a re-run would be a second paid upstream call.
- **Graceful shutdown.** On SIGTERM new submissions get `503 IMAGE_TASK_UNAVAILABLE` (not submitted) and running tasks get up to `image_tasks.drain_timeout_seconds` (default 600) to finish. Tasks still running then are cancelled and concluded as `task_interrupted`. Give the container a matching stop grace period (`docker stop -t`, Compose `stop_grace_period`); with Docker's default 10 seconds the sweeper of the next instance concludes the tasks instead.
- **Terminal states are final.** A late result for a task that was already concluded is dropped and logged as `image_task.late_result_dropped`.
- **Controlled scheduling.** Tasks get their own scheduling ledger, so they run in `controlled` mode as well. In 0.2.6 they failed right after the 202 with `upstream request failed: context canceled` in that mode.

### Error codes assigned by the gateway

| `error.code` | `http_status` | Meaning | `billing.status` |
| --- | --- | --- | --- |
| (upstream error) | upstream status | Refusal or failure reported by the upstream, unchanged | from the ledger, normally `not_charged` |
| `timeout` | 504 | The 30-minute execution limit passed | from the ledger |
| `task_interrupted` | 502 / 503 | The instance stopped (sweeper) or shut down (drain) | from the ledger |
| `result_storage_failed` | 502 | Generated and billed, but storing the images failed three times | `charged` |

### Observability

Structured log events `image_task.submitted`, `started`, `completed`, `failed`, `interrupted`, `offload_retry`, `offload_failed`, `deleted` and `late_result_dropped` carry `task_id`, `request_id`, `user_id`, `api_key_id`, `group_id`, `model`, `endpoint`, `duration_ms`, `error_code` and `billing_status`; prompts and links are never logged. A failed task also writes an `ops_error_logs` row whose `request_path` is the submit path (for example `/v1/images/generations/async`) and whose `client_request_id` is the task's request id, so it shows up in the admin error log next to synchronous failures.
