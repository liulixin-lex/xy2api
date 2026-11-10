# 用量指标与导出

本文说明使用记录、Token 趋势、账号列表和运维看板上各项用量指标的计算口径，以及使用记录的后台导出任务如何启用和运行。指标都基于已记录的计费用量与耗时，不根据页面刷新速度或流式文本长度估算。读者是需要解读这些数字的管理员和运维，以及部署导出功能的运维。

## 单次请求平均 TPS

使用记录“延迟”列显示**平均TPS**，单位 **t/s**（每秒输出 Token 数）；运维页的单次请求速率叫“平均输出速率”，单位 tokens/s，口径相同。

```text
单次请求平均TPS = output_tokens × 1000 / duration_ms
```

- `duration_ms` 是该用量记录的完整请求耗时，包含首 Token 等待。流式与非流式使用相同公式。例如输出 100 Token、总耗时 3000 ms，结果为 33.3 t/s，与首 Token 耗时无关。
- 不用 `duration_ms - first_token_ms` 作分母：输出可能包含首个可见 Token 之前的推理 Token，也可能直到结束才返回缓冲的工具输出，现有记录也没有可靠的逐 Token 时间。因此它**不是**模型的纯解码速度。
- 输出计数可能已经包含推理 Token，不再额外相加。
- 只统计适用的 Token 请求，排除图片输出、视频、非 Token 计费和 Live/未知请求类型。输出或耗时不是有限正数时显示 `—`，不显示 0 t/s。
- 运维中的 P5/P10/P50 是筛选范围内各条有效记录速率的分位数，Avg 是这些单次速率的算术平均。

### 首字耗时字段

| 字段 | 起点 | 用途 |
|---|---|---|
| `first_token_ms` | 最后一次上游尝试开始 | 使用记录、运维看板与健康分、渠道监控的首字，高级调度的首 Token 评分 |
| `end_to_end_first_output_ms` | 入站请求开始，含排队、选号和先前失败的尝试 | 账号列表“近期表现”的首字 |
| `attempt_first_output_ms` | 最终尝试开始，只算可见输出 | [分组质量门槛](group-quality-gate.md)的速度样本，不进入接口 |

- `first_token_ms` 与 `duration_ms` 都不含此前失败的尝试与换号等待。换过号的请求客户端实际等待更长，各次尝试见运维错误记录的 `upstream_errors`。
- OpenAI 的 `first_token_ms` 口径由设置 `openai_ttft_mode` 决定：`semantic`（默认）把中转插入的保活帧也记为首个事件；`visible` 只算真实输出。详见[网关韧性](gateway-resilience.md)。
- `end_to_end_first_output_ms` 与 `attempt_first_output_ms` 只认语义输出：错误帧、`[DONE]`、纯终态、开场与心跳都不算；没有语义输出时为 `NULL`。
- 三个字段起点不同，不能直接相互比较。

## 缓存读取占比

用量日志记录的是**计费用量**，页面显示的是**缓存读取占比**，不是上游真实的缓存命中率：

```text
缓存读取占比 = cache_read_tokens
             / (input_tokens + cache_creation_tokens + cache_read_tokens) × 100%
```

三类输入 Token 是互斥桶：`input_tokens` 是未缓存输入，`cache_creation_tokens` 是缓存创建，`cache_read_tokens` 是缓存读取。OpenAI 原始总输入含缓存明细，写入时已经减去缓存读取与创建；Anthropic 的输入本来就不含缓存桶。自行计算时不要再扣一次缓存，也不要把 `cache_creation_5m_tokens`/`cache_creation_1h_tokens` 再加到已经包含它们的创建总量上。

- 强制缓存计费和账号“缓存写入计费分类覆盖”会改变持久化的计费桶，因此这组数字不能还原真实上游命中情况，也不能当作请求命中次数占比。
- 趋势和区间统计先累加三类 Token 再套用同一公式，不平均各请求的百分比。例如一个 100 Token 全命中请求与一个 900 Token 未命中请求，区间占比是 10%，不是 50%。
- 没有有效输入（分母为 0）或字段异常时，用量明细显示 `—`，Token 趋势保留断点；有输入但没有缓存读取时才显示 0.0%。
- 渠道监控的用户接口隐藏了分子分母，对无有效输入返回 0，无法区分“无输入”和“有输入但未读取缓存”。

**Anthropic 缓存 TTL**：用量接口返回 `anthropic_cache_ttl_policy`（`off`/`5m`/`1h`）与 `anthropic_cache_ttl_applied`（布尔），历史记录和非目标出口为 `NULL`，使用记录页面不显示。`applied=true` 只表示最终请求的缓存声明都符合指定 TTL，不表示供应商采用了该 TTL 或命中了缓存。价格与计费规则见[提示词缓存](caching.md)。

## Token 吞吐量

渠道监控和运维流量卡的 **Token 吞吐量（tokens/s）** 是统计窗口内全部计费 Token（输入、输出、缓存创建、缓存读取）除以窗口秒数。它随并发量变化，不能与单次请求平均 TPS 直接比较。渠道监控的 `TPM ÷ 60` 与之等价。

## 显示与公开

“系统设置 → 功能开关 → 管理员使用记录”控制缓存读取占比和平均 TPS 两项指标的显示，只影响显示，不影响统计与计费。

| 设置键 | 默认值 | 作用 |
|---|---|---|
| `admin_usage_cache_hit_rate_enabled` | 开 | 显示缓存读取占比。关闭后对管理员和普通用户都不显示 |
| `admin_usage_token_speed_enabled` | 开 | 显示平均 TPS。关闭后对管理员和普通用户都不显示 |
| `usage_cache_hit_rate_public` | 关 | 打开后普通用户的使用记录也显示缓存读取占比 |
| `usage_token_speed_public` | 关 | 打开后普通用户的使用记录也显示平均 TPS |

公开设置（`GET /api/v1/settings/public`）只给出生效结果 `user_usage_cache_hit_rate_enabled`、`user_usage_token_speed_enabled`：显示开关与公开开关都打开才为 `true`。管理员自己的“我的账户 → 使用记录”属于用户页面，同样按公开状态显示。用户接口本来就返回计算所需的 Token 与耗时，公开只决定页面是否展示，不是数据权限控制。

## 账号列表的近期表现

账号管理的“近期表现”列紧跟“状态”列，可以在列显示里隐藏（隐藏时不请求）。它显示近期失败尝试、续轮首字和缓存保持三项。窗口先按指标资格筛选，再取时间倒序最近 N 个；不足 N 个时只用时间上限内的样本。悬停显示实际样本数和覆盖分钟数。

| 指标 | 统计对象与窗口 | 显示与颜色 |
|---|---|---|
| 失败 | 最近 200 个事件，限 60 分钟。事件由成功用量行和门槛软失败尝试组成；失败按每次上游尝试归到账号，被换号救回的尝试也计入 | 失败为 0 时不显示。`calls` ≥20 显示失败率和失败数，按 Wilson 95% 区间下限着色：≥10% 红、≥5% 黄、其余灰；不足 20 时显示灰色失败数。点击打开按账号筛选的“使用记录 → 错误请求”（只含对客失败，条数可能更少） |
| 首字 | 严格续轮的流式成功请求：会话中相邻上一请求属于同一账号、`model` 相同且间隔不超过 5 分钟。首字取 `COALESCE(NULLIF(end_to_end_first_output_ms, 0), first_token_ms)`。最近 50 个，限 2 小时 | ≥20 个显示 P90；≥50 个才按 10/30/60 秒分档着色。不足 20 显示“样本不足 n” |
| 缓存保持 | 严格续轮，且本轮与上一轮提示 Token（输入 + 缓存读取 + 缓存写入）都至少 2048。按本轮 `cache_read_tokens` ÷ 上一轮提示 Token 分为 0、(0, 0.5)、[0.5, 0.9)、≥0.9 四档，后两档计为保持。最近 50 个，限 2 小时 | ≥20 个显示保持占比；≥50 个才着色，低于 90% 黄、低于 75% 红。Gemini 使用隐式缓存，不着色 |

三项都只把 `actual_cost > 0 OR output_tokens > 0` 的用量行当作成功请求。失败样本只来自 `ops_error_logs.upstream_errors`，运维监控关闭时不会产生软失败记录。短横线表示近期窗口没有可计入的表现，也用于加载中或加载失败（悬停说明）。

接口为 `POST /api/v1/admin/accounts/recent-health/batch`，请求体 `account_ids` 最多 1000 个，快照缓存 10 秒并支持 ETag。响应 `stats` 中每个账号含 `failure`、`first_output`、`cache` 三个可空对象，并声明 `failure_max_events`、`failure_max_age_seconds`、`sample_max`、`sample_max_age_seconds`、`min_display_samples`、`min_color_samples`。

## 容量列的分组并发

账号列表和分组列表的容量列可以显示“当前并发 / 总并发 / 本分组并发”。绿色的第三个数是这些账号当前正在处理、且归属于该分组的请求槽位数；账号同时属于多个分组时各分组显示各自的数量，“当前并发”仍是账号的实际总槽位。第三个数不会超过第一个数；并发上限为 0（不限制）的账号没有槽位记录，第三个数始终为 0。

账号页只有筛选了一个具体分组时才显示第三个数；分组页每个分组都显示。读取失败时显示 0，不影响请求处理。已知局限：部分等待路径按 API Key 的分组而不是实际调度分组计数，回退分组下可能少算（不会多算）；智商检测和 Codex 工单的后台探测不计入任何分组。

## 运维看板的 SLA

运维看板的“SLA（排除业务限制）”只计状态码 ≥400 且没有标为业务限制的错误请求。

- **业务限制**（不计入 SLA）：用户自身条件或本地策略导致的失败，包括余额与额度、订阅、用户并发与 RPM 限制、Key 鉴权与 IP 限制、分组策略、本地模型配置（分组模型白名单、账号模型映射），以及上游的内容安全拒绝（内容拒绝的 code/type，或 400、403、451 上的内容安全文案，带账号措辞的除外）。
- **计入 SLA**：选号阶段找不到可用账号（无号 503，阶段 `routing`）、组内没有账号支持所请求的模型、`invalid_prompt`、普通 400 与上游 5xx。

快层确认的输出后失败（读流出错、缺终态、带内 `stream_failed`、停滞）会补记失败证据并归因到上游供应商。确认失败后客户端才取消、最终为 HTTP 200 的请求，与换号后恢复的请求一样写一条 2xx 恢复行：不计入错误总数、SLA 与请求错误列表，计入上游错误率与供应商错误列表。上游错误率按责任方 `provider` 统计并排除 429/529。详见[网关韧性](gateway-resilience.md)。

## 后台导出

使用记录导出以后台任务运行：用户导出 CSV，管理员导出 XLSX，两者共用同一个 PostgreSQL 持久化任务引擎。浏览器只提交任务、查询状态和发起原生下载，不再逐页读取历史记录。

### 启用与配置

`usage_export.enabled` 默认 `false`，只控制新建任务；已有任务继续处理，完成的文件仍可下载。所有实例必须使用一致的配置；修改后需要重启生效。

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `false` | 是否允许新建导出任务 |
| `allowed_user_ids` | `[]` | 非空时只有名单内的用户（含管理员）可以新建；空表示全部用户 |
| `directory` | `./data/usage-exports` | 本地存储目录。设置了 `DATA_DIR` 且未改默认值时为 `$DATA_DIR/usage-exports` |
| `concurrency` | `2` | 同时执行的任务数，取值 1–32 |
| `queue_limit` | `100` | 排队上限 |
| `max_rows` | `1000000` | 单个任务最多行数，不能超过 1,000,000；超出返回 `EXPORT_ROW_LIMIT`，不发布截断结果 |
| `max_file_bytes` | `536870912`（512 MiB） | 单个文件上限 |
| `task_disk_bytes` | `4294967296`（4 GiB） | 单个任务暂存磁盘上限 |
| `storage_bytes` | `21474836480`（20 GiB） | 已发布文件的总存储预算 |
| `free_bytes` | `2147483648`（2 GiB） | 需要保留的磁盘空闲空间 |
| `read_seconds` / `run_seconds` / `queue_seconds` | `300` / `900` / `1800` | 读取、执行、排队超时 |
| `retention_seconds` | `86400` | 完成文件的有效期 |
| `metadata_seconds` | `604800` | 任务记录保留时间 |
| `create_rpm` / `status_rpm` / `ticket_rpm` | `5` / `60` / `10` | 每用户新建、查询状态、申请下载凭证的每分钟次数 |
| `download_concurrency` | `2` | 同时进行的下载数，取值 1–32 |
| `storage.type` | `local` | `local` 或 `s3` |
| `storage.endpoint` / `region` / `bucket` / `prefix` / `access_key_id` / `secret_access_key` / `force_path_style` | 空 / `auto` / 空 / `usage-exports/` / 空 / 空 / `false` | S3 兼容存储的独立配置 |

这些键不在 `deploy/config.example.yaml` 中，需要时在配置文件里加 `usage_export:` 段，环境变量写法见[配置文件与环境变量](../deployment/configuration.md)。

**存储要求**

- 本地目录必须可写、不是 Web 静态目录，并有足够空间；不要用 `/tmp`。其中 `files/` 是发布的文件，`tmp/` 是暂存。多实例使用本地存储时必须挂载同一个共享目录。
- 使用 S3 时，bucket/prefix 应为私有；仍需要本地暂存磁盘。请为 bucket 配置对象生命周期和未完成分片清理，保留时间不短于 `retention_seconds`。
- 临时文件增长按周期监测，请用独立卷或文件系统配额提供硬上限。
- 数据库连接池需要容纳执行槽的会话锁、快照事务、心跳和正常业务连接。不要用事务池模式的 PgBouncer，它无法承载会话级 advisory lock。

### 工作方式

- 每个用户最多一个活动任务（含排队）。新建时在一个事务里检查队列、总存储预留和用户预算；相同 `Idempotency-Key` 且参数相同的活动任务不重复创建。已完成的任务再次创建会得到新快照。
- 读取在单个 `REPEATABLE READ READ ONLY` 事务中进行，通过数据库游标每批 2,000 行，关联名称在同一事务里读取，得到一致快照。读取中断或进程丢失的任务直接失败，需要新建，不跨快照续读。
- CSV 带 BOM、标准转义和公式文本防护；XLSX 为流式单工作表，文本与长数字作为字符串，金额按原列精度。发布前校验文件并计算 SHA-256 与长度。
- 后台每秒检查心跳、取消和临时目录预算，每分钟处理排队超时、失联任务、过期文件和孤儿暂存。逻辑过期或删除立即禁止下载。

### 接口

用户接口前缀 `/api/v1/usage/exports`，管理员接口前缀 `/api/v1/admin/usage/exports`。管理员导出另需普通管理员权限中的“使用记录”查看权限和“导出业务数据”开关（见[管理员与权限](admin-permissions.md)）。这些控制接口不计入 Heavy 限流额度。

| 方法 | 相对路径 | 行为 |
|---|---|---|
| `POST` | 空 | 创建任务，必须带 `Idempotency-Key`，返回 `202`；键冲突返回 `409` |
| `GET` | 空 | 最近 24 小时创建的任务分页列表 |
| `GET` | `/:id` | 状态、阶段、计数、快照、大小、过期时间 |
| `POST` | `/:id/cancel` | 取消排队或运行中的任务 |
| `DELETE` | `/:id` | 删除终态任务，立即撤销下载 |
| `POST` | `/:id/download-ticket` | 为当前登录会话签发 60 秒的一次性下载凭证 |
| `GET` | `/:id/download` | 浏览器原生流式下载 |

下载凭证通过 HttpOnly、SameSite=Strict、限定路径的短期 Cookie 传递，不出现在 URL、JSON 或访问日志里，服务端只保存其 SHA-256。兑换时重新检查创建者、账号状态、登录会话和管理员角色。不支持断点续传；下载失败后重新申请凭证下载同一文件即可，无需重新生成任务。

前端在有活动任务且页面可见时每 5 秒查询一次状态，隐藏或离开页面后停止查询，任务在后台继续。`429` 遵守 `Retry-After`。

常见错误码：`EXPORT_DISABLED`、`EXPORT_ALREADY_ACTIVE`、`EXPORT_QUEUE_FULL`、`EXPORT_STORAGE_FULL`、`EXPORT_RATE_LIMITED`、`EXPORT_IDEMPOTENCY_KEY_REQUIRED`、`EXPORT_IDEMPOTENCY_CONFLICT`、`EXPORT_ROW_LIMIT`、`EXPORT_SIZE_LIMIT`、`EXPORT_TIMEOUT`、`EXPORT_QUEUE_TIMEOUT`、`EXPORT_INTERRUPTED`、`EXPORT_NOT_READY`、`EXPORT_EXPIRED`、`EXPORT_INVALID_TICKET`。

### 观测

任务表记录排队时间、读取进度、快照、完成时间、文件大小、失败码和存储预留；结构化日志记录任务 ID、条数、执行耗时和下载字节数，不记录正文、凭证或存储密钥。建议观察排队与执行数量、失败与取消率、长事务、磁盘和下载流量。后台任务成功不等于用户下载成功。

建议先用 `allowed_user_ids` 对少量用户启用，在与生产相同的数据量下确认耗时、内存和磁盘占用后再全量开放。

### 停用与回滚

依次关闭 `enabled`、等待或取消活动任务、停止服务，再回滚应用。导出相关表由迁移 `252_usage_export_tasks.sql` 追加创建，回滚时不需要删表。

## 升级与回滚

用量观测列（`end_to_end_first_output_ms`、`attempt_first_output_ms` 等）由追加式迁移添加，只对升级后的新记录采集，历史记录不回填；SLA、上游错误归因的口径调整同样只影响新记录。跨版本比较趋势时，上游与平台两桶此消彼长属于口径变化，不能直接当作服务质量变化。这些迁移只追加可空列，回滚旧版本时可以保留，无需删除数据。
