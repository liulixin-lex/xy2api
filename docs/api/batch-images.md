# Gemini 批量生图 API

批量生图一次提交最多 200 张图，由 Gemini Batch API 或 Vertex 批量预测在后台生成，完成后按成功的图片张数结算。本文面向调用网关 API 的开发者，以及负责启用这项功能的运维。单张图片的异步生成见 [异步图片任务](images.md)。

接口使用普通的网关 API Key 认证（`Authorization: Bearer sk-...`），任务只归属于提交它的那个 API Key。

支持两种上游：

| `provider` | 上游 | 适用账号 |
|---|---|---|
| `gemini_api` | Gemini Batch API（JSONL 文件模式） | 填写了 API Key 的 Gemini `apikey` 账号 |
| `vertex` | Vertex `BatchPredictionJob`（托管 GCS JSONL） | 带有效 service account JSON 的 Gemini `service_account` 账号 |

请求中不指定 `provider` 时，依次尝试 `gemini_api` 与 `vertex`。API 用户看不到 Gemini 文件名、Vertex 任务名、GCS 路径、签名 URL、API Key 或 service account 信息，下载经由 XY2API 代理。

用户界面中的“批量生图”页面（`/batch-image`）提供同样的功能和使用说明。

## 启用

1. 先在 Google 官方控制台开通能力：使用开启计费的 Google Cloud 项目，启用 Gemini API / Vertex AI；使用 Vertex 时创建一个固定的 Cloud Storage 存储桶用于批量输入输出，并给运行时与 Vertex 服务代理授予最小所需的存储桶权限。XY2API 的开关本身不会在 Google 侧开通任何权限。
2. 在配置中打开 `batch_image.enabled` 和 `batch_image.queue_enabled`（后者启动处理任务的 worker；不打开时任务会一直排队）。需要 Redis。
3. 使用 Vertex 时设置 `batch_image.vertex_enabled`、`vertex_project_id`、`vertex_location`、`vertex_managed_gcs_bucket`（只填存储桶名，不是 URI）。
4. 在管理后台为目标 Gemini 分组开启“允许图片生成”，然后开启“批量生图”（`allow_batch_image_generation`）。只有 Gemini 平台分组可以开启批量生图，开启图片生成后界面才显示该开关。
5. 在分组中配置可调度的 Gemini API Key 或 service account 账号、模型和价格。启用前先用一个低成本的小批量任务验证。

建议为 Vertex 托管存储桶关闭 Cloud Storage 软删除，或谨慎配置生命周期，避免产生隐藏的存储费用。

## 配置项

`batch_image` 段的主要配置（不在 `deploy/config.example.yaml` 中，需要时自行添加）：

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `false` | 总开关；关闭时接口返回 `404 BATCH_IMAGE_DISABLED` |
| `queue_enabled` | `false` | 启动 Redis 队列 worker |
| `max_items_per_job_default` | `200` | 单个任务最多条目数 |
| `max_output_images_per_job` | `200` | 单个任务展开后最多输出图片数 |
| `max_output_images_per_item` | `4` | 单条提示词的 `output_count` 上限 |
| `max_prompt_chars_per_item` | `8000` | 单条提示词长度上限 |
| `max_reference_images_per_job` | `1000` | 单个任务展开后的参考图附件总数上限 |
| `max_reference_inline_bytes_per_job` | `134217728`（128 MB） | 单个任务内联参考图解码后总字节上限 |
| `default_response_mime_type` / `default_image_size` | `image/png` / `1K` | 默认输出格式与尺寸 |
| `max_download_items_zip` | `200` | 单个 ZIP 最多图片数 |
| `max_download_bytes_per_request` | `536870912`（512 MB） | 单次下载字节上限 |
| `max_download_duration_seconds` | `600` | 单次下载最长时间 |
| `max_download_concurrency_per_user` | `1` | 每用户同时下载数 |
| `input_retention_after_terminal_hours` | `24` | 任务结束后输入保留时间 |
| `output_retention_after_terminal_hours` | `72` | 任务结束后输出保留时间 |
| `output_retention_max_days` | `7` | 输出最长保留天数 |
| `cleanup_interval_minutes` / `cleanup_batch_size` | `30` / `100` | 清理周期与每批数量 |
| `vertex_enabled` | `false` | 启用 Vertex 上游 |
| `vertex_project_id` / `vertex_location` | 空 / `global` | Vertex 项目与区域 |
| `vertex_managed_gcs_bucket` / `vertex_managed_gcs_prefix` | 空 / `batch-image/{env}/{batch_id}` | 托管存储桶与前缀（前缀必须包含 `{batch_id}`） |
| `vertex_input_retention_hours` / `vertex_output_retention_hours` | `24` / `72` | Vertex 侧输入输出保留时间 |

队列相关的 Redis 键名与重试节奏（`queue_ready_key`、`queue_delayed_key`、`queue_active_key`、`inflight_key_prefix`、`lock_key_prefix`、`idempotency_key_prefix`、`job_lock_ttl_seconds`、`stale_active_after_seconds` 等）一般不需要修改。PostgreSQL 是任务状态的唯一来源，Redis 只用于唤醒、重试、协调、单任务锁和下载限流；worker 从 Redis 队列领取任务，不轮询数据库。

## 接口

```text
POST   /v1/images/batches                                   提交
GET    /v1/images/batches                                   列表
GET    /v1/images/batches/models                            可用模型
GET    /v1/images/batches/{id}                              任务详情
GET    /v1/images/batches/{id}/items                        条目列表
GET    /v1/images/batches/{id}/items/{custom_id}/content    下载单张图片
GET    /v1/images/batches/{id}/download                     下载 ZIP
POST   /v1/images/batches/{id}/cancel                       取消
DELETE /v1/images/batches/{id}/outputs                      删除输出
DELETE /v1/images/batches/{id}                              删除任务记录
```

所有接口只能访问当前 API Key 提交的任务。

## 提交任务

```bash
curl https://api.example.com/v1/images/batches \
  -H 'Authorization: Bearer sk-...' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: catalog-2026-11-10' \
  -d @batch.json
```

```json
{
  "model": "gemini-2.5-flash-image",
  "task_name": "商品主图",
  "provider": "gemini_api",
  "items": [
    {
      "custom_id": "cover_001",
      "prompt": "A clean product hero image on a white background",
      "output_count": 2,
      "reference_images": [
        {
          "id": "product-front",
          "type": "subject",
          "mime_type": "image/png",
          "data": "<不带 data URL 前缀的 base64 图片数据>"
        }
      ]
    }
  ],
  "image_size": "1K",
  "response_mime_type": "image/png"
}
```

| 字段 | 说明 |
|---|---|
| `model` | 必填。可用模型见 `GET /v1/images/batches/models`（按 Key 所属分组的账号与模型白名单过滤） |
| `task_name` | 可选，任务名称 |
| `provider` | 可选，`gemini_api` 或 `vertex` |
| `items[].custom_id` | 条目标识，同一任务内不能重复 |
| `items[].prompt` | 提示词，最长 8000 字符 |
| `items[].output_count` | 可选，默认 1，最大 4。表示“同一提示词和参考图重复生成 N 次”，后端展开为多条上游请求，标识带后缀，例如 `cover_001_01`、`cover_001_02` |
| `items[].reference_images` | 可选。每张为 `data`（base64，单张解码后不超过 10 MiB）或 `file_uri`（必须是 `gs://`，供 Vertex 使用）二选一；`mime_type` 只能是 `image/png`、`image/jpeg`、`image/webp` |
| `image_size` / `response_mime_type` / `aspect_ratio` | 可选，输出尺寸、格式与比例 |

数量限制：

- 每条提示词的参考图：Flash Image 模型（模型名含 `flash-image`）最多 3 张，Pro Image 模型（含 `pro-image`）最多 14 张，其他模型不支持参考图。
- 每个任务展开后最多 200 张输出图片，与单个 ZIP 的默认图片数上限一致，因此新任务总能按张数整包下载（ZIP 的字节数仍受 `max_download_bytes_per_request` 限制）。更大的工作量请在提交前拆分成多个任务。
- 每个任务展开后最多 1000 个参考图附件、内联参考图合计不超过 128 MB。参考图很多或重复使用时，优先用 `gs://` 引用或拆分任务。
- Vertex 上游的输出请按 `1K`/默认尺寸使用，不要依赖 `2K` 或 `4K`。

提交成功返回 `200` 和任务对象。提交前会按估算费用预扣余额，余额不足返回 `402 BATCH_IMAGE_INSUFFICIENT_BALANCE`。`Idempotency-Key` 相同但请求不同返回 `409 BATCH_IMAGE_IDEMPOTENCY_CONFLICT`。

```json
{
  "id": "imgbatch_0123456789abcdef0123456789abcdef",
  "object": "image.batch",
  "task_name": "商品主图",
  "status": "queued",
  "model": "gemini-2.5-flash-image",
  "provider": "gemini_api",
  "item_count": 2,
  "success_count": 0,
  "fail_count": 0,
  "estimated_cost": 0.25,
  "hold_amount": 0.25,
  "actual_cost": null,
  "created_at": 1783123200,
  "submitted_at": 1783123201,
  "settled_at": null
}
```

## 状态

| `status` | 含义 |
|---|---|
| `queued` | 已受理，正在上传或等待上游开始 |
| `running` | 上游生成中 |
| `processing_results` | 整理结果 |
| `settling` | 结算中 |
| `completed` | 已完成 |
| `failed` | 失败 |
| `cancelled` | 已取消 |
| `output_deleted` | 输出已被手动删除或按保留期清理 |

## 查询条目与下载

`GET /v1/images/batches/{id}/items` 支持 `status`、`limit`、`cursor` 查询参数：

```json
{
  "object": "list",
  "data": [
    {
      "custom_id": "cover_001_01",
      "status": "succeeded",
      "mime_type": "image/png",
      "file_extension": "png",
      "image_count": 1,
      "error": null
    }
  ],
  "has_more": false
}
```

条目状态为 `pending`、`succeeded`、`failed` 或 `cancelled`。失败条目的 `error` 包含 `code` 与 `message`。

- `GET /v1/images/batches/{id}/items/{custom_id}/content` 返回单张图片（`image_index` 指定第几张，默认 0）。
- `GET /v1/images/batches/{id}/download` 以流式 ZIP 返回整批结果，附带清单文件；可用 `status` 过滤、`max_items` 限制张数。超出字节上限返回 `400 BATCH_IMAGE_DOWNLOAD_TOO_LARGE`，同时下载过多返回 `429 BATCH_IMAGE_DOWNLOAD_LIMITED`，任务未完成返回 `409 BATCH_IMAGE_NOT_READY`。
- `GET /v1/images/batches` 支持 `status`、`task_name`、`downloaded`、`from`、`to`、`limit`、`cursor` 过滤。

## 取消、删除与保留

- `POST /v1/images/batches/{id}/cancel` 取消未完成的任务，返回任务对象。
- `DELETE /v1/images/batches/{id}/outputs` 删除输出文件，返回任务对象，状态变为 `output_deleted`。之后下载返回 `410 BATCH_IMAGE_OUTPUT_DELETED`。
- `DELETE /v1/images/batches/{id}` 删除任务记录，返回 `204`。
- 任务结束后，输入默认保留 24 小时，输出保留 72 小时（最长 7 天），每 30 分钟清理一次。清理只使用服务端生成的路径。

## 计费

- 提交时按估算费用预扣（`hold_amount`），结果整理完成后结算，只对成功生成的图片收费，失败条目不收费，剩余预扣会释放。
- 参考图会作为输入发给 Gemini，可能产生少量上游输入 Token 与临时存储成本（`output_count > 1` 时按展开后的每次请求计算），但面向用户的预估、预扣和结算只按输出图片张数和配置的批量生图单价计算，不另收参考图费用。
- 结算请求 ID 为 `batch_image_settlement:{batch_id}`，结算幂等，重复执行不会重复扣费。结算失败会有限次重试，达到上限后任务失败并释放剩余预扣。
- 具体价格由分组和模型的价格配置决定。

## 错误

错误格式为 `{"error": {"type": "invalid_request_error", "code": "...", "message": "..."}}`。常见错误码：

| 错误码 | HTTP | 含义 |
|---|---|---|
| `BATCH_IMAGE_DISABLED` | 404 | 功能未启用 |
| `BATCH_IMAGE_GROUP_DISABLED` | 403 | API Key 所属分组未开启批量生图 |
| `BATCH_IMAGE_INVALID_MODEL` | 400 | 未指定模型 |
| `BATCH_IMAGE_INVALID_PROVIDER` | 400 | `provider` 不合法 |
| `BATCH_IMAGE_INVALID_ITEMS` | 400 | 请求体或条目不合法 |
| `BATCH_IMAGE_DUPLICATE_CUSTOM_ID` | 400 | `custom_id` 重复 |
| `BATCH_IMAGE_TOO_MANY_OUTPUT_IMAGES` | 400 | 展开后的输出图片数超过上限 |
| `BATCH_IMAGE_TOO_MANY_REFERENCE_IMAGES` | 400 | 参考图数量超过该模型或任务的上限 |
| `BATCH_IMAGE_REFERENCE_IMAGES_TOO_LARGE` | 400 | 内联参考图总大小超过上限 |
| `BATCH_IMAGE_PROMPT_TOO_LONG` | 400 | 提示词过长 |
| `BATCH_IMAGE_INVALID_REFERENCE_IMAGE` | 400 | 参考图不合法 |
| `BATCH_IMAGE_INSUFFICIENT_BALANCE` | 402 | 余额不足以预扣 |
| `BATCH_IMAGE_NO_ACCOUNT_AVAILABLE` | 503 | 分组中没有可用的批量生图上游账号 |
| `BATCH_IMAGE_IDEMPOTENCY_CONFLICT` | 409 | 幂等键被用于不同的请求 |
| `BATCH_IMAGE_NOT_FOUND` | 404 | 任务不存在或不属于当前 API Key |
| `BATCH_IMAGE_NOT_READY` | 409 | 任务尚未完成 |
| `BATCH_IMAGE_OUTPUT_DELETED` | 410 | 输出已删除 |
| `BATCH_IMAGE_DOWNLOAD_LIMITED` | 429 | 同时下载过多 |
| `BATCH_IMAGE_DOWNLOAD_TOO_LARGE` | 400 | 下载内容超过字节上限 |

## 注意事项

- XY2API 的 `x-goog-api-key` 兼容请求头需要的仍是 XY2API 的 API Key，不是 Google 的 API Key。
- Google API Key 适合 Gemini API 开发；生产中的 Vertex 批量任务建议使用 service account。
- 其他 Gemini 账号类型（例如 OAuth 登录类）不会被批量生图选用。
- 官方参考：[Gemini API Batch API](https://ai.google.dev/gemini-api/docs/batch-api)、[Gemini 图片生成](https://ai.google.dev/gemini-api/docs/image-generation)、[Gemini API Key](https://ai.google.dev/gemini-api/docs/api-key)。
