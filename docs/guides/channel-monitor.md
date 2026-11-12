# 渠道监控

渠道监控用来持续观察各上游渠道是否可用、响应是否变慢。它有两种实现，二选一：**V1 主动探测**按间隔向渠道发送一道简单的加法题，用“多久拿到正确答案”判定绿、黄、红；**V2 被动监控**不发探测请求，而是按真实网关流量聚合错误率、首字延迟等健康指标。本文面向配置监控的管理员；普通用户在「渠道状态」页面查看结果。

## 启用与模式

「设置 → 功能开关 → 渠道监控」：

| 界面名称 | 设置键 | 默认 | 说明 |
|---|---|---|---|
| 启用渠道监控 | `channel_monitor_enabled` | 开启 | 关闭后 V1 调度器与 V2 聚合都停止，用户端入口隐藏；已有配置与历史保留 |
| 监控模式 | `channel_monitor_mode` | `v1` | `v1`（V1 主动探测）或 `v2`（V2 被动监控）；空值或非法值按 `v1` 处理 |
| 默认检测间隔（秒） | `channel_monitor_default_interval_seconds` | `60` | 仅 V1：新建监控时表单的默认值，范围 15–3600 |
| 向用户展示渠道用量/余额 | `channel_monitor_show_quota` | 关闭 | 仅 V1：在用户端展示配额模式监控关联账号的用量窗口与余额 |
| 对用户隐藏吞吐速率（RPM / TPM） | `channel_monitor_hide_throughput` | — | 仅 V2：用户端页面与接口不返回 RPM / TPM，避免反推集群规模 |
| 对用户隐藏用户排行 | `channel_monitor_hide_user_ranking` | 关闭 | 仅 V2：用户端不显示「用户排行」页，接口不返回排行数据 |

- V2 需要主动选择。切换到 V2 后，V1 的定时探测与手动“立即检测”都会停止；切回 V1 即恢复。
- 升级不会改动已保存的 `channel_monitor_mode`；没有这个键的部署按 `v1` 运行。
- 管理员在「渠道管理 → 渠道监控」中配置监控项；用户需登录后在侧栏「渠道状态」（`/monitor`）查看，页面按当前模式显示 V1 或 V2 视图。

## V1 主动探测

### 创建监控

| 字段 | 说明 |
|---|---|
| 供应商（`provider`） | `openai`、`anthropic`、`gemini`、`grok`、`kimi`、`zhipu`、`deepseek`、`minimax`；`antigravity` 只支持配额模式 |
| API 模式（`api_mode`） | `chat_completions`（默认）或 `responses`；只有 OpenAI 与 Grok 支持 Responses |
| 接口地址 | 必须是 HTTPS，不能带查询参数或片段 |
| API Key | 编辑时留空保留已保存的 Key |
| 主模型与附加模型 | 每轮对每个模型各探测一次 |
| 检测模式（`check_mode`） | `probe`（探活）、`quota`（配额）、`quota_probe`（探活 + 配额） |
| 间隔与抖动 | `interval_seconds` 15–3600；`jitter_seconds` 需满足“间隔 − 抖动 ≥ 15” |
| 请求体覆盖（`body_override_mode`） | `off`（默认）、`merge`（合并）、`replace`（覆盖），见下文 |
| 附加请求头、请求模板 | 模板可复用到多个监控；禁止的请求头名会被丢弃 |

配额模式需要关联一个平台与供应商一致的账号，读取该账号的用量窗口或余额：快照缓存 5 分钟、错误缓存 60 秒，任一用量窗口超过 90% 时显示降级。详细历史保留 30 天。

### 判定口径

每轮对每个模型发一次探测，结果是三种颜色之一：

| 颜色 | 条件 | 历史状态 |
|---|---|---|
| 绿 | 30 秒内拿到正确回答，且响应时间不超过 10 秒 | `operational` |
| 黄 | 拿到正确回答，但响应时间超过 10 秒 | `degraded` |
| 红 | 30 秒内拿不到正确回答 | 超时、网络错误、非 2xx、流内错误为 `error`；空回复或答错为 `failed` |

- 响应时间是从发出请求到收到完整答案的时间，即用户实际等到回答的时间。
- 30 秒是一轮的总预算，包含网关里的排队、换号以及至多一次重试。
- 7 / 15 / 30 天可用率把 `operational` 与 `degraded` 都计为可用。

### 探测请求

题目是一句零样本加法 `Reply with only the number. a + b = ?`，`a`、`b` 在 1–20 之间随机。Responses 的 `instructions`、Anthropic 的 `system` 只有一句 “Reply with only the number.”。

**流式读取，读到答案即停**：所有供应商都发流式请求（Gemini 使用 `/v1beta/models/{model}:streamGenerateContent?alt=sse`）。网关边读边拼接正文，正文出现期望答案、且答案已完整（后面是非数字字符或流结束）时立即判定并断开。

- 不算回答的内容：SSE 注释与心跳、`event: ping`、只有 role 的增量、usage、生命周期事件、空增量。推理增量（Responses 的 reasoning、Chat 的 `reasoning_content` / `reasoning`、Anthropic 的 thinking）只算“有输出”，不参与答案校验；正文中的 `<think>…</think>` 先剥离。
- 流内错误照常判红：Responses 的 `response.failed`、Chat 的 `data: {"error":…}`（其后的 `[DONE]` 不算成功）或 `event: error`、Anthropic 的 `event: error`、Gemini 的错误对象。没有任何正文就结束判为空回复。
- 读法按响应的 Content-Type 选择，上游忽略 `stream: true` 直接返回 JSON 时也能判定。

**专用 HTTP 客户端**：只跟随同一主机内的重定向（跨主机重定向会带走 Key 与签名头，按非 2xx 处理）；拨号与 TLS 握手各 10 秒；不设整体超时，由一轮 30 秒的预算控制；空闲连接保持 10 分钟，HTTP/2 空闲 30 秒发 PING，因此 2–5 分钟间隔的探测可以复用连接。目标地址不能是内网、回环、链路本地等私有地址。对接口源站的 HEAD ping 与模型请求并行，只测到入口（经 CDN 时是 CDN 边缘），不影响判定。

**请求形状**：每种协议只发最小请求，不带 `temperature`、`top_p`、`stream_options`：

| 协议 | 字段 |
|---|---|
| OpenAI Chat（含 Grok、Kimi、智谱、DeepSeek、MiniMax） | `model`、`messages`、`max_tokens`、`stream` |
| OpenAI Responses（含 Grok） | `model`、`instructions`、`input`、`max_output_tokens`、`stream` |
| Anthropic Messages | `model`、`system`、`messages`、`max_tokens`、`stream` |
| Gemini | `contents`、`generationConfig.maxOutputTokens` |

推理参数与输出上限按模型族设置。推理 token 计在输出上限里，上限太小会“推理吃光预算、正文为空”；读到答案即停，所以上限给大不会变慢：

| 模型族 | 推理参数 | 输出上限 |
|---|---|---|
| OpenAI GPT-5 及以上、o 系列、codex | Responses `reasoning.effort: "low"`；Chat `reasoning_effort: "low"` | 1024 |
| Grok 4 及以上、grok-3-mini、grok-code | 同上（Responses 用 `reasoning.effort`，Chat 用 `reasoning_effort`） | 1024 |
| Gemini 2.5 及以上的 flash | `thinkingConfig.thinkingBudget: 0` | 1024 |
| Gemini 2.5 及以上的 pro | `thinkingConfig.thinkingBudget: 128`（思考不能关闭，取最低值） | 1024 |
| Anthropic | 不加（默认不开 thinking） | 16 |
| Kimi、智谱、DeepSeek、MiniMax，以及名字像推理模型的（think、reason、r1、qwq 等） | 不加 | 1024 |
| 其他非推理模型 | 不加 | 16 |

名字里含 non-reasoning、non-thinking、no-think 的模型按非推理处理。

**请求体覆盖**：

- `merge`：在最小请求上浅合并用户字段，用户写了推理参数或输出上限时以用户为准；`model`、消息与 `stream` 等关键字段不可覆盖。
- `replace`：原样发送用户的请求体，不注入任何参数，按其中的 `stream` 决定流式或非流式读法，读到正文完整为止。

### 参数自适应

上游以 400 或 422 拒绝网关添加的可选参数时，同一轮内修正后重发，不判红：

- 错误文本提到推理参数，或没有点名地拒绝了某个参数：去掉注入的推理参数；
- Chat 要求用 `max_completion_tokens` 代替 `max_tokens`：改名；
- 拒绝流式：改为非流式。

修正按供应商、接口、API 模式、模型与所用 Key 记在进程内，之后每轮直接使用修正后的形状；6 小时后过期并重新尝试原始形状。用户在 `merge` 中自己写的推理参数和 `replace` 模式不做修正。

### 重试与费用

- 只对快速失败的连接级错误立即重试一次：连接被重置、EOF 等，5 秒内返回的 502 / 503，以及看起来是 CDN 错误页的任何 5xx（HTML 正文，或 Cloudflare 的错误 JSON）。超时、答错不重试；剩余预算不足 10 秒时不重试。
- 每个模型每轮最多发 2 次请求，与参数修正共用。响应时间从本轮第一次请求算起；第二次仍失败时，消息以 `attempt 2/2:` 开头。
- 探测照常经过网关计费。读到答案即断开时，经本站网关的请求按已输出的部分计费，网关会继续读完上游以结算；推理模型的输出上限为 1024，话多的模型单次费用相应更高。

### 探测请求在网关中的选号

监控发出的探测请求带 `X-XY2API-Probe` 头。它用进程内随机密钥签名，签名覆盖监控 ID、时间（60 秒内有效）以及本次所用的 API Key。网关只按请求自带的 Key（`Authorization: Bearer`、`x-api-key`、`x-goog-api-key`）校验，只接受一个值；无论是否通过都会删除这个头，不转发到上游。因此监控指向第三方接口时，对方拿到的签名只对监控自己的 Key 有效；打到其他实例的探测校验不通过，按普通请求处理。

带探测标记的请求在选号时：

- **不使用粘性会话**：不读也不写会话绑定，不会被钉在已绑定的账号上等它空出来。
- **有空闲槽位时不排队**：在决定等待某个账号之前，先按真实并发上限把同一批候选再试一遍，有空闲槽就直接使用，不看负载率。只有整组确实全满时才排队，排队时间计入 30 秒预算。

覆盖 OpenAI 与 Grok 的调度、Anthropic 的兜底等待与模型路由。真实用户请求的选号行为不变。与真实请求相比，探测只受账号真实并发数限制、不受负载因子约束；不占用、也不受 Anthropic OAuth 账号会话数上限约束；OpenAI 探测不参与会话质量路由。

### 调度

- **启动**：首轮在 0 到 min(间隔, 60 秒) 之间随机延后。加载监控失败时从 5 秒起退避重试，上限 5 分钟。
- **对账**：每 5 分钟比对一次库中启用的监控与内存任务，补建缺失的、撤销多余的。
- **并发**：同时执行 16 个监控的检测；在队列里等待超过半个间隔的检测跳过本轮，不在很晚的时刻补测。
- **多实例**：每轮开始前用一条条件更新认领本轮，`last_checked_at` 早于 0.8 × (间隔 − 抖动) 才能认领，因此同一轮只有一个实例探测。`last_checked_at` 记录一轮的开始时间。
- **编辑后**：新建、编辑、启用监控后立即测一轮，不受认领间隔限制。
- **取消**：监控被停用、删除或进程停机时，进行中的检测被取消且不写历史；用完时间预算的一轮是正常结果，照常写入。`quota_probe` 模式的配额抓取与探测并行。
- **手动“立即检测”**：脱离浏览器连接执行，页面关闭也会测完并写入；同一监控已有检测在跑时复用那一轮的结果；不推进 `last_checked_at`，不会让下一轮定时检测被跳过。

最新状态与时间轴按“监控 × 当前配置的模型”逐对读取最近的记录；已从监控配置中移除的模型不再出现在最新状态里。

### 已知局限

- 推理文字直接写在正文里、且只有闭合标签 `</think>` 没有开标签的上游（少数自部署的推理模型），在标签到达前推理文字会被当作正文，其中的数字可能被提前当成答案。
- 参数修正依赖匹配错误文本；上游以 HTTP 200 加流内错误拒绝参数时不会触发修正，这一轮判红。

### 配置建议

- 监控尽量绑定真实用户在用的分组与模型。只用于监控的分组若与真实分组共享账号，探测会占用这些账号的并发槽位。
- 同一个 Key、同一分组、同一模型只建一个监控，重复的监控只会多出一张相同的卡片。
- 检测间隔不要太短：探测在网关输出前断开会计入该 Key 的“未送达”，同一 Key 每小时超过 30 次会按滥用计费，阈值由网关韧性设置中的 `disconnect.abuse_guard.threshold_per_hour` 控制，见 [网关韧性设置](gateway-resilience-settings.md)。

## Grok 的 Responses 模式

Grok 监控可以在保留 `provider=grok` 的同时选择 `api_mode=responses`，不需要把供应商改成 OpenAI。已有监控和模板仍默认使用 Chat Completions，升级不会自动切换协议。

账号页的文本测试走 `/v1/responses`，而 Chat 监控走 `/v1/chat/completions`，两条路径的上游模型路由与可用性可能不同，所以账号测试成功不能证明 Chat 监控一定成功。选择 Responses 模式可以让监控覆盖实际使用的路径。它不改变业务请求路由、账号选择、错误换号或上游模型配置，也不会消除真实的限流、余额不足和网络错误。

| 项目 | 设置 |
|---|---|
| 供应商 | Grok |
| 检测模式 | 探活，或已有配额配置时的探活 + 配额 |
| API 模式 | Responses API |
| 接口地址 | 目标基础地址，例如 `https://gateway.example` 或 `https://gateway.example/v1` |
| API Key | 目标站点中绑定 Grok 分组的 Key；编辑时留空保留已保存的 Key |
| 主模型 | 目标分组与上游都支持的模型（默认 `grok-4.5`） |

- 程序在地址后追加 `/v1/responses`，并正确处理已带 `/v1` 的基础路径。仍按 Key 在目标站点的权限、分组白名单和账号资格选择上游，监控的供应商标签不能替代这些权限。
- 默认请求为流式 Responses：`model`、`instructions`、加法题 `input`、`max_output_tokens`、`stream=true`；Grok 4 及以上带 `reasoning: {"effort": "low"}`，上游不认时同一轮自动去掉重发。
- 正文按 `response.output_text.delta` 拼接，reasoning 事件不参与答案校验；`response.failed` 等流内错误判红。上游直接返回 JSON 时，按 `output_text` 与 `output` 中的 message 解析。
- `merge` 模式保护 `model`、`instructions`、`input`、`stream`，允许调整输出上限等非关键字段。`replace` 模式要求非空的 `instructions` 与 `input`，按其中的 `stream` 决定读法；自行填写的模型仍须有权限使用。
- 请求模板也支持 Grok Responses。模板列表按供应商与协议共同筛选；切换协议会清除表单中的旧模板和自定义请求，避免把 Chat 的 `messages` 请求体带入 Responses。
- 判定口径与 V1 相同；上游非 2xx 照常显示为错误。该模式依赖上游实际支持 Responses，启用后请在目标渠道上确认监控结果。xAI Responses API 的流式说明见 [官方文档](https://docs.x.ai/developers/tools/streaming-and-sync)。

## V2 被动监控

V2 基于真实网关流量聚合健康指标，不向上游发送探测请求，因此不产生探测费用，但只能反映有真实流量的渠道。

### 配置

在「渠道管理 → 渠道监控」页面的 V2 选项卡中配置，对应接口 `GET`/`PUT /api/v1/admin/channel-monitor-v2/config`（只要求渠道监控已启用，不要求当前模式为 V2）：

| 字段 | 说明 |
|---|---|
| `enabled` | 是否启用 V2 聚合 |
| `refresh_interval_seconds` | 刷新间隔，只能是 60 或 300 |
| `platforms` | `[{platform, enabled, models}]`，要统计的平台与模型 |
| `group_ids` | 要统计的分组 |
| `health_thresholds` | 健康评分阈值，见下表 |
| `ignored_error_categories` | 不计入错误率和健康分的错误类别，仍显示在错误明细中（灰色） |

健康阈值默认值：

| 字段 | 默认 |
|---|---|
| `minimum_sample` | 50 |
| `warning_error_rate` / `critical_error_rate` | 0.05 / 0.20 |
| `target_ttft_ms` / `warning_ttft_ms` / `critical_ttft_ms` | 3000 / 3000 / 10000 |
| `warning_cache_rate` / `critical_cache_rate` | 0 / 0（缓存命中不影响健康分，除非显式配置） |
| `error_weight` / `ttft_weight` / `cache_weight` | 0.60 / 0.20 / 0.20 |

默认忽略的错误类别：`authentication`、`client_cancelled`、`content_policy`、`context_limit`、`group_access`、`model_unsupported`、`not_found`、`quota_or_balance`。管理员可以清空或扩充。

### 查看

- 管理端读取接口：`GET /api/v1/admin/channel-monitor-v2/{dimensions,snapshot,models,matrix,errors,users}`，要求渠道监控已启用且模式为 V2。
- 用户端读取接口：`GET /api/v1/channel-monitor-v2/{dimensions,snapshot,models,matrix,errors,users}`，需要登录，带较严格的频率限制，同样要求模式为 V2；V1 的用户端接口为 `GET /api/v1/channel-monitors` 与 `/api/v1/channel-monitors/:id/status`。

### 历史回填

首次启用 V2 时，聚合器会以低资源的方式逐步回填历史，避免冲击主库：

- 不使用高频的引导节拍，按 `refresh_interval_seconds` 运行；首次立即运行，先回填最近 2 小时。
- 之后每个周期先重算最近约 10 分钟的重叠窗口，再最多处理**一个**历史分块；单一领导者锁，每次运行的事务预算约 55 秒。
- 分块初始 1 小时，最小 15 分钟；最大值按回溯深度：1 天以内 2 小时，7 天以内 4 小时，更早 6 小时。重算耗时少于 15 秒时按 1.5 倍缓慢增大，失败或超时时减半；等待时间从 1 分钟起指数退避，上限 10 分钟。
- 按 90 分钟 → 1 天 → 7 天 → 30 天的产品窗口逐步覆盖，之后静默回溯到 90 天。界面显示已覆盖的部分，并提示回填尚未完成的窗口。
