# 异步图片任务 API

长时间运行的 OpenAI 与 Grok 生图、改图请求可以异步提交：提交后立即返回任务 ID，再轮询结果，不必长时间占住一个 HTTP 连接，从而避开 Cloudflare 524 之类的代理超时。本文面向调用网关 API 的开发者，以及负责启用这项功能的运维。一次提交多张图片的离线批量生成见 [Gemini 批量生图](batch-images.md)。

接口使用普通的网关 API Key 认证（`Authorization: Bearer sk-...`），任务只归属于提交它的那个 API Key。

## 启用

异步生图任务**默认关闭**，并且依赖对象存储：生成的图片先上传到 S3 兼容存储，Redis 里只保存短链接，避免几 MB 的 `b64_json` 堆积在 Redis 中。开关关闭或存储凭据不完整时，异步接口返回 `404 async image tasks are not enabled`，不会创建任务。

**在管理后台启用（推荐）**：“系统设置 → 数据备份 → 异步生图对象存储”。保存后立即生效，无需重启。表单默认“复用上方备份的 S3 配置”：借用备份的端点、区域和凭据，只单独设置存储桶和 Key 前缀（存储桶留空则沿用备份存储桶），这样备份在 `backups/` 下，图片在 `images/` 下。取消勾选可以指向完全独立的账号。启用了二次验证时，保存需要通过二次验证。

关闭开关只停止接收新任务，已受理的任务仍可查询。

**通过配置文件启用**：后台从未保存过该设置时，使用 `config.yaml` 的 `image_storage` 段（也可用 `IMAGE_STORAGE_*` 环境变量覆盖）：

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `image_storage.enabled` | `false` | 开关 |
| `image_storage.endpoint` | 空 | S3 兼容端点；AWS 官方可留空，R2 形如 `https://<account_id>.r2.cloudflarestorage.com` |
| `image_storage.region` | `auto` | 区域 |
| `image_storage.bucket` / `access_key_id` / `secret_access_key` | 空 | 必填 |
| `image_storage.prefix` | `images/` | 对象 Key 前缀 |
| `image_storage.force_path_style` | `false` | MinIO 等需要路径风格访问时设为 `true` |
| `image_storage.public_base_url` | 空 | 填写公开桶或 CDN 域名时返回 `public_base_url/key` 永久链接；留空返回预签名临时链接 |
| `image_storage.presign_expiry_hours` | `24` | 预签名链接有效期（小时） |
| `image_storage.max_download_bytes` | `33554432`（32 MB） | 上游返回图片 URL 时，下载转存的字节上限 |

运行边界在 `image_tasks` 段（开关仍是 `image_storage`）：

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `image_tasks.max_in_flight_per_key` | `0` | 每个 API Key 同时进行的任务上限；`0` 表示用户并发 × 2，至少 2 |
| `image_tasks.max_in_flight_per_instance` | `256` | 单实例同时执行的任务上限 |
| `image_tasks.long_poll_max_seconds` | `30` | 长轮询 `wait` 的最长秒数（最大 60） |
| `image_tasks.poll_rate_per_second` | `10` | 每个 API Key 每秒查询/删除次数上限；`0` 为不限 |
| `image_tasks.drain_timeout_seconds` | `600` | 收到 SIGTERM 后等待在途任务的秒数 |

完整示例见 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)。

## 接口

```text
POST   /v1/images/generations/async
POST   /v1/images/edits/async
GET    /v1/images/tasks/{task_id}
DELETE /v1/images/tasks/{task_id}
GET    /v1/images/async/capabilities
```

也可以去掉 `/v1` 前缀使用（例如 `/images/generations/async`）。只支持 OpenAI 与 Grok 平台的分组，分组还需允许图片生成。请求体与对应的同步接口相同（JSON 或 multipart）；流式请求会被拒绝，因为轮询的任务只返回一个最终 JSON。

另外，向同步接口 `POST /v1/images/generations` 或 `/v1/images/edits` 发送请求头 `Prefer: respond-async`（RFC 7240）时，行为与 `/async` 路径相同，返回 `202` 和 `Preference-Applied: respond-async`。异步任务关闭时忽略该请求头，按同步处理（`200`）。

## 提交任务

```bash
curl -i https://api.example.com/v1/images/generations/async \
  -H 'Authorization: Bearer sk-...' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: cover-2026-11-10-001' \
  -d '{
    "model": "gpt-image-1",
    "prompt": "A lighthouse during a winter storm",
    "size": "1536x1024"
  }'
```

响应 `202 Accepted`，带 `Location`（轮询路径）、`Retry-After: 3`（建议轮询间隔）和 `Cache-Control: no-store`：

```json
{
  "id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "task_id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1784092800,
  "expires_at": 1784179200,
  "poll_url": "/v1/images/tasks/imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "request_id": "client:6f1c2a8e-...",
  "idempotency_key": "cover-2026-11-10-001",
  "billing": { "status": "pending" }
}
```

提交仍是一次普通的网关请求：限流、余额检查和内容审核都在返回 `202` 之前执行。`202` 之前的任何失败都意味着没有任务、没有生成、不计费。

**幂等提交**：`Idempotency-Key` 为 1–128 个 `A-Z a-z 0-9 . _ : -` 字符，按用户和 API Key 隔离，有效期与任务相同。

| 情况 | 响应 |
|---|---|
| 首次提交 | `202`，响应回显 `idempotency_key` |
| 相同键、相同请求 | `200`，返回现有任务视图和 `poll_url`，响应头 `Idempotent-Replayed: true`，不会再次生成 |
| 相同键、不同请求 | `422 idempotency_key_reused` |
| 相同键、首次提交仍在受理中 | `409 idempotency_in_progress`，`Retry-After: 1` |

JSON 请求体逐字节比较；multipart 按各部分（字段名、文件名、内容）比较，客户端重建表单换了 boundary 也能匹配。在 `202` 之前被拒绝的提交会释放它的键。

## 查询任务

用提交任务的同一个 API Key 查询：

```bash
curl https://api.example.com/v1/images/tasks/imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c?wait=25 \
  -H 'Authorization: Bearer sk-...'
```

`wait` 为长轮询秒数：任务完成或等满 `wait` 秒后返回，上限为 `image_tasks.long_poll_max_seconds`。

进行中：

```json
{
  "id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "task_id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "object": "image.generation.task",
  "status": "processing",
  "request_id": "client:6f1c2a8e-...",
  "model": "gpt-image-1",
  "endpoint": "generations",
  "billing": { "status": "pending" },
  "created_at": 1784092800,
  "started_at": 1784092801,
  "heartbeat_at": 1784092891,
  "expires_at": 1784179200
}
```

成功时 `result` 与同步接口的响应体相同，但每张图都已转存到对象存储：`data[].url` 指向存储对象，`b64_json` 被移除。`image_url` 等于第一个 `data[].url`，方便简单客户端使用：

```json
{
  "id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "task_id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "object": "image.generation.task",
  "status": "completed",
  "http_status": 200,
  "image_url": "https://images.example.com/images/....png",
  "result": {
    "created": 1784092923,
    "data": [{ "url": "https://images.example.com/images/....png" }]
  },
  "request_id": "client:6f1c2a8e-...",
  "model": "gpt-image-1",
  "endpoint": "generations",
  "billing": { "status": "charged", "request_id": "client:6f1c2a8e-..." },
  "created_at": 1784092800,
  "completed_at": 1784092923,
  "expires_at": 1784179323
}
```

失败时状态为 `failed`，在可用时返回原始的 OpenAI 兼容错误对象：

```json
{
  "id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "task_id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "object": "image.generation.task",
  "status": "failed",
  "http_status": 502,
  "error": { "type": "api_error", "message": "Upstream request failed" },
  "billing": { "status": "not_charged" },
  "created_at": 1784092800,
  "completed_at": 1784092923,
  "expires_at": 1784179323
}
```

除 `completed` 和 `failed` 以外的状态都视为仍在运行。任务和结果在最后一次状态更新后保留 24 小时；单个任务最多执行 30 分钟。未知的任务 ID 和属于其他 API Key 的任务 ID 都返回 `404 image task not found`，不暴露任务是否存在。正常的认证、禁用 Key、用户、IP 与分组检查仍然生效。

## 计费状态

每个任务都带 `request_id`（即该次生成在 `usage_logs.request_id` 中的计费请求 ID）和 `billing` 对象：

| `billing.status` | 含义 |
|---|---|
| `pending` | 任务仍在运行，或账本尚未结算；稍后再查 |
| `charged` | 存在 `billing.request_id` 对应的用量记录 |
| `not_charged` | 既没有用量记录，也没有未结算的计费意图 |

计费结论来自网关自己的账本。生成成功不代表已经结算：已完成的任务可能暂时是 `pending`，再次查询会刷新；账本查询失败时也保持 `pending`。

## 删除任务

```bash
curl -X DELETE https://api.example.com/v1/images/tasks/imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c \
  -H 'Authorization: Bearer sk-...'
```

```json
{
  "id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "task_id": "imgtask_3f2b9c1e8a7d4e6f9b0c1d2e3f4a5b6c",
  "object": "image.generation.task.deleted",
  "deleted": true
}
```

删除会移除已存储的图片和任务记录。仍在 `processing` 的任务返回 `409 IMAGE_TASK_RUNNING`：删除不是取消，生成会继续并计费。请在下载图片后删除；存储桶的生命周期规则仍是兜底。

## 能力查询

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

## 限制与错误

| 限制 | 默认值 | 超限响应 |
|---|---|---|
| 每个 API Key 运行中的任务 | 用户并发 × 2，至少 2 | `429 too_many_tasks`，`Retry-After: 5` |
| 每个实例运行中的任务 | 256 | `429 too_many_tasks`，`Retry-After: 5` |
| 每个 API Key 的查询与删除 | 每秒 10 次 | `429 too_many_requests`，`Retry-After: 1` |

运行任务数限制在内容审核之前检查，被 `429` 拒绝的重复提交不会消耗审核调用。

网关错误响应的格式为 `{"error": {"type": "...", "code": "...", "message": "..."}}`。任务失败时由网关分配的 `error.code`：

| `error.code` | `http_status` | 含义 |
|---|---|---|
| （上游错误） | 上游状态码 | 上游报告的拒绝或失败，原样返回 |
| `timeout` | 504 | 超过 30 分钟执行上限 |
| `task_interrupted` | 502 / 503 | 实例停止（巡检收尾）或关闭（排空超时） |
| `result_storage_failed` | 502 | 图片已生成，但重试 3 次（间隔 1、3、9 秒）后仍转存失败 |

计费结论都以账本为准。

## 可靠性

- 运行中的任务每 15 秒刷新 `heartbeat_at`。每个实例每分钟（通过 Redis 锁轮流）检查心跳超过 2 分钟的任务，以 `task_interrupted` 收尾。任务**不会**重跑，因为重跑意味着第二次付费的上游调用。
- 收到 SIGTERM 后，新的提交返回 `503 IMAGE_TASK_UNAVAILABLE`（未提交），运行中的任务最多等待 `image_tasks.drain_timeout_seconds`，仍未完成的以 `task_interrupted` 收尾。请把容器的停止宽限期（`docker stop -t`、Compose `stop_grace_period`）设为不小于该值；用 Docker 默认的 10 秒时，会由下一个实例的巡检收尾。
- 终态是最终的：已收尾任务迟到的结果会被丢弃，记录为 `image_task.late_result_dropped`。

## 观测与排障

结构化日志事件 `image_task.submitted`、`started`、`completed`、`failed`、`interrupted`、`offload_retry`、`offload_failed`、`deleted`、`late_result_dropped` 携带 `task_id`、`request_id`、`user_id`、`api_key_id`、`group_id`、`model`、`endpoint`、`duration_ms`、`error_code` 和 `billing_status`，从不记录提示词和链接。失败的任务同时写入一条运维错误记录，`request_path` 是提交路径（例如 `/v1/images/generations/async`），`client_request_id` 是任务的请求 ID，因此会和同步失败一起出现在后台错误日志中。

**启用后仍返回 404**：

- `404 async image tasks are not enabled`：对象存储配置不完整，功能未生效。启动日志中会出现 `image_storage.enabled is true in config but object storage is not fully configured`，其中 `missing_keys` 列出缺少的键。只通过环境变量配置时，请确认变量已传入容器。
- `Images API is not supported for this platform`：API Key 所属分组不是 OpenAI 或 Grok 平台，或 Key 没有分组。
- `image task not found`：用了与提交时不同的 API Key 查询（即使是同一用户的另一个 Key）。
