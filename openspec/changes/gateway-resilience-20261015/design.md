# 网关韧性设计

> 起点：origin/main `f8fec15a2`（业务代码与 v0.3.0 `1e0fbdd70` 一致）。
> 证据：[`evidence/production-analysis.md`](evidence/production-analysis.md)、[`evidence/research-summary.md`](evidence/research-summary.md)。
> 决策编号 D1–D33 见 [`proposal.md`](proposal.md)。
> 本文中的文件路径均相对 `backend/`。

## 0. 原则

1. **自动机制只跳过、排除，不改意图。** 层级、优先级、权重、排空只由管理员设定。冷却、回避、末位兜底、兜底分组都只改变“这一次请求或这个会话暂时用谁”，不改写任何配置，也不按统计自动调权（D4，与份额调度一致）。
2. **以“语义输出”为换号边界。** 首个语义字节送出之前，失败可以换号；送出之后不透明重放，写协议内的可重试终止帧并立即关流（D17）。心跳字节与暂存的元数据事件不算语义输出。
3. **罚则只分两档。** 硬状态严格排除；软状态在“没有健康候选”时可以兜底使用（D5）。请求级错误不惩罚账号。
4. **一个请求一份预算。** 次数和首输出前的时长统一扣减；客户端离开立即停止等待（D18）。
5. **对客户端行为负责。**
   - Codex 的流空闲计时为 300 秒，且不认注释行；5xx 不带 `Retry-After` 时，一个回合约产生 30 次请求；`Retry-After` 没有上限；429 不带 `Retry-After` 即终止。
   - Anthropic SDK 丢弃没有 `event` 行的数据帧。
   - Gemini SDK 遇到注释行会报错。
   - 所有时限与错误帧都按这些行为设计。
6. **自有模块，最少钩子。** 逻辑集中在新包 `internal/resilience`；在上游原文件中只加可登记的钩子（D10）。
7. **每项能力一个开关。** 默认开启，只作应急关闭；关闭时行为回到 0.3.0（D23）。

## 1. 现状锚点与缺陷

| 主题 | 位置 | 现状 |
|---|---|---|
| 换号循环（OpenAI Responses） | `internal/handler/openai_gateway_handler.go` 约 860–940 | 失败时视 `RetryableOnSameAccount` 同号重试，否则换号，上限 `max_account_switches=10`；首输出超时换号只允许 1 次（`openAIFirstOutputFailoverExhausted`，约 3862） |
| 同号重试 | `internal/handler/failover_loop.go` | 起始 0.5 秒，请求级降载翻倍到 8 秒；池模式默认对 401/403/429 重试 3 次 |
| Gemini 原地重试 | `internal/service/gemini_messages_compat_service.go`（`geminiMaxRetries=5`，1–16 秒退避，`time.Sleep` 不响应取消） | — |
| 流处理与 keepalive | `internal/service/openai_gateway_response_handling.go`、`gateway_upstream_response.go` | `stream_keepalive_interval=10s` 只在上游响应头到达后生效；间隔检测 ticker 周期等于阈值（**G2**） |
| 首包前心跳原语 | `internal/service/openai_compact_sse_keepalive.go`（`StartOpenAICompactSSEKeepalive`、`OpenAICompactKeepaliveAdjustedWrittenSize`）、`openai_images_json_keepalive.go` | 已有延迟首拍、心跳字节扣除、提交后错误转流内事件，但只用于 compact 和图片 |
| Anthropic 提交后的错误 | `internal/handler/gateway_handler.go` 的 `handleStreamingAwareErrorWithCode`（约 1990–2020） | 写 `data: {"type":"error",...}`，缺少 `event: error`（**G3**） |
| 暂存账号头 | `openai_gateway_response_handling.go` 的 `applyAttemptResponseHeaders` | `c.Writer.Written()` 后不再下发，`x-codex-turn-state` 会丢失（**G8**） |
| 流内失败换号判定 | `internal/service/openai_gateway_passthrough.go`（`openAIStreamClientOutputStarted`、`openAIStreamFailedEventShouldFailover`） | 尚无客户端输出、且语义为 429 或瞬时错误时才换号 |
| 账号罚则 | `internal/service/ratelimit_service.go`（`handle403`、`handleOpenAI403`、`handle429`、`apply429FallbackRateLimit`、default 分支） | OpenAI 403：冷却 10 分钟，3 次后永久置错；其他平台 403：立即 `handleAuthError`；429 无重置时间冷却 2 秒（现 45 秒）；5xx 只记日志 |
| 可恢复余额（仅 CN 供应商） | `internal/service/ratelimit_cn_providers.go`（`handleCNProviderInsufficientBalance`，冷却为 2 倍检测周期，由周期任务恢复） | 可泛化 |
| 粘性会话 | `internal/service/openai_account_scheduler.go` 的 `selectBySessionHash`；`gateway_service.go` 的 `shouldClearStickySession`、`openai_sticky_atomic.go` 的 `deleteStickySessionAccountIDIfOwner` | 账号处于冷却等不可调度状态时立即清除绑定；sticky escape 只看账号整体错误率与 TTFT |
| 份额调度 | `internal/sharesched/plan.go`（`Candidate{Eligible, RecoverAt, Excluded, StableSince}`、`softSpill`，默认 300 秒）、`internal/service/share_scheduling_exec.go`；原因码见 `internal/sharesched/reason.go` | 已有 `soft_spill`、`spill_rebind`、`rebind_unavailable`、`failover_next` |
| 无号分类 | `internal/handler/no_account_error.go` | 503 “Service temporarily unavailable”或 404 `model_not_found`；全部限流时为 429（不带 `Retry-After`） |
| 兜底分组 | `internal/service/gateway_scheduling.go` 的 `resolveGatewayGroup` | `fallback_group_id` 只用于“仅限 Claude Code”分组的降级，不是无号兜底 |
| 用户余额 | `internal/server/middleware/api_key_auth.go`（403 `INSUFFICIENT_BALANCE` “Insufficient account balance”）、`internal/handler/gateway_handler.go` 的 `billingErrorDetails`（403 `billing_error` “insufficient balance”） | 两套格式；文案与 sub2api 系上游相同 |
| 在途预留 | `internal/handler/gateway_inflight_reservation.go`、`internal/service/billing_inflight_reservation.go` | 余额减在途小于估算即拒绝；估算 = 输入 Token × 输入价（冷写价取高）+ max_tokens（默认 8192，上限 128k）× 输出价 |
| 异步图片 | `internal/handler/image_task_handler.go`（176 内容审计先于 180 起的准入） | 被拒的重复提交每次都触发审计 |
| 迁移 | `migrations/` 最新为 `271_drop_platform_check_constraints.sql`，共 318 条 | 新迁移从 272 开始 |

## 2. 上游失败分类（`internal/resilience/classify.go`）

### 2.1 输入

每次上游尝试结束后，生成一份 `Outcome`：

- 来源：HTTP 应答、流内 `response.failed`/`error`/`data:{error}` 事件、网络错误、本地首输出超时、停滞超时、客户端取消；
- 平台、账号类型、HTTP 状态、上游错误 code/type/message（已脱敏）、流内事件语义状态；
- 已送达的语义字节数、尝试耗时、上游给出的 `retry_after`（响应头或正文，例如中转商 Cloudflare 错误页里的 `retry_after: 60/120`；正文中 `try again in 1m30.5s`、`1h2m` 这类复合时长同样识别）。

### 2.2 类别与动作

判定顺序：**先看文本与 code，再看状态码**，使余额类信号在 401、403、429 下也能被识别。

| 类别 | 主要判据 | 换号 | 同号重试 | 账号罚则 | 会话（D4） |
|---|---|---|---|---|---|
| `BALANCE` | 402；code 为 `insufficient_quota`、`insufficient_user_quota`、`pre_consume_token_quota_failed`、`credit_balance_exhausted`；文本含 insufficient (account) balance、余额不足、额度不足、credit balance、usage balance exhausted，以及 new-api 的 quota is not enough、额度已用尽、预扣费额度失败等（关键字集合可配置） | 是 | 否 | 硬：暂停 `balance.pause_seconds`（§3.2） | 立即改绑 |
| `AUTH` | 401、invalid/revoked key；结构化的账号停用 code（`deactivated_workspace` 等） | 是 | 否 | 硬：OpenAI 维持现有 3 次规则；其他平台改为计数加临时冷却（§3.3） | 立即改绑 |
| `RATE_LIMIT_RESET` | 429，且带明确的重置时间（头部或正文） | 是 | 否 | 软，冷却到重置时间；重置时间超过 300 秒视为硬 | 不超过 300 秒时只临时溢出；超过则改绑 |
| `RATE_LIMIT` | 429 无重置时间；流内 `rate_limit_exceeded`；“The service is busy” 等 | 是 | 否 | 软，冷却时长取设置 `rate_limit_429_cooldown_settings`（当前 45 秒） | 计入失败计数 |
| `CAPACITY_SHED` | 529、overloaded、`server_is_overloaded`、Codex at capacity（请求级降载） | 是，最多换 1 次，且换到不同 base_url 主机 | 是，每账号最多 1 次，0.5–2 秒带抖动 | 无（请求级） | 不计 |
| `TRANSIENT` | 5xx、524、504、连接错误、HTML 错误页 | 是 | 否（504/524 及耗时 ≥60 秒的失败一律不同号重试） | 软：冷却 `transient_cooldown_seconds`（默认 30 秒）；上游给出 `retry_after` 时取 min(retry_after, 120 秒) | 计入失败计数 |
| `FIRST_OUTPUT_TIMEOUT` | 本地首输出超时（§7.4），包括网关自身合成的首事件超时 504（`first_output_timeout`）；中转返回的普通 504 仍为 `TRANSIENT` | 是 | 否 | 不冷却账号（本地超时与上游应答的 `first_output_timeout` 都一样，见下方补充规则）；OpenAI API Key 账号的上游 5xx（含这种 504）沿用按模型瞬时屏蔽 | 计入失败计数 |
| `STALL` | 已输出后超过停滞阈值无数据（§7.5） | 否（已输出） | 否 | 软，冷却 30 秒 | 计入失败计数 |
| `CLIENT_CANCEL` | 客户端断开 | 否 | 否 | 无 | 不计 |
| `CLIENT_ERROR` | 400/413/422 的确定性参数错误、上下文超长、内容策略、cyber policy；上游内容安全拒绝（内容安全 code，或 400/403/451 上的安全检查文案，§18.4） | 否（沿用现有“模型不存在可换号”等例外）；内容安全拒绝连池模式同号重试也不做 | 否 | 无 | 不计 |
| `MODEL_UNSUPPORTED` | 账号级模型不存在（现有 `isOpenAICompatibleModelNotFound400`） | 是 | 否 | 账号加模型级瞬时阻断（沿用现有） | 不计 |

补充规则：

- **本站与上游余额分开。** `BALANCE` 只来自上游应答。本站计费产生的余额错误走 §9.5，不进入分类。
- **Gemini 分钟级限额不是余额。** “You exceeded your current quota” 是 OpenAI 的余额文案，Gemini 的 `RESOURCE_EXHAUSTED` 限额也用同一句话；带 Gemini 限额标记时按 429 归入限流类，不触发余额暂停。
- **首输出超时不冷却账号。** 首输出慢多因中转排队或长推理，与请求的推理档位相关，冷却账号会把其他档位的请求也挪走，收益不明；它只计会话连败（同会话同账号 2 次即改绑）并换号。本地超时不写冷却，也不触发按模型瞬时屏蔽；上游以 `first_output_timeout` 应答（级联的 xy2api）同样不写冷却，只有 OpenAI API Key 账号按任何上游 5xx 的原有规则记入按模型瞬时屏蔽（进程内，第 2 次连败起 10 秒、第 3 次起 45 秒）。管理员配置 `stream_timeout_settings` 后，本地超时沿用其原有处置（生产未配置）。2026-10-16 生产核查时按代码对齐文档；用例 `TestLocalFirstOutputTimeoutLeavesTheAccountSchedulable`、`TestUpstreamFirstOutputTimeoutLeavesTheAccountSchedulable`。
- **池模式账号保留管理员配置的同号重试状态码**，但同样受预算与“每账号最多 1 次”约束。
- 分类结果写入请求上下文，供换号循环、会话处理、ops 记录与 Explain 使用。

## 3. 账号罚则与余额暂停

### 3.1 硬、软分级（D5）

| 档 | 状态 | 选号行为 |
|---|---|---|
| 硬 | 管理员停用（`schedulable=false`）、`status=error`、余额不足暂停（原因 `balance:`，暂停期内，§3.2）、凭据失效、重置时间超过 300 秒的限流、IQ 质量门、利润门、模型不支持、分组不匹配 | 永不选中 |
| 软 | `TRANSIENT`/`STALL` 冷却、无重置时间的 429 冷却、不超过 300 秒的有重置时间 429、过载冷却（`overload_cooldown_settings`，当前 2 分钟）、模型级瞬时阻断（`openai_model_transient_state`）、会话回避（§4） | 平时不选；没有任何健康候选时可以作为末位兜底（§4.3） |

实现：在 `internal/resilience` 中提供 `PenaltyOf(account, model, now) (tier, recoverAt, reason)`，由两个调度器的候选构造钩子调用。现有冷却仍写在原有字段里：`rate_limit_reset_at`、`overload_until`、`temp_unschedulable_until`（`reason` 前缀区分 `balance:`、`transient:`、`auth:`）。快照与数据库口径都不变。

### 3.2 余额不足暂停（D6，0.4.0 精简）

> 2026-10-17 用户决定：经常给上游充值，不需要余额的监控、告警与探测，只要求简单、可预期。RC 中的四态状态机（`OK`/`SUSPECT`/`EXHAUSTED`/`RECOVERING`）、复检探测、恢复期并发上限、余额告警与“长期未恢复”通知全部取消。复核时还发现 RC 实现的两处缺陷：余额告警永远不会触发（`ListOpsAccountsForStats` 不读 `extra`，指标恒为 0）；探测查不出结论时暂停只延不缩，中转不支持余额接口、生成探测又失败时欠费账号一直不放回。

- **识别不变：** §2 的 `BALANCE`（错误码、缺省关键字含 `insufficient user balance`、`failure.balance_keywords_extra`）。适用范围不变：API Key 类账号；CN 供应商、OpenCode Zen/Go、Command Code、Cline 与 Antigravity 走各自原有的余额路径。余额类 403 不走“403 计数、第 3 次置错”。
- **处置：** 识别到余额不足就把账号暂停 `balance.pause_seconds`（缺省 300 秒，30–3600）：`SetTempUnschedulable`，原因 `balance:exhausted: <证据>`。暂停期间按硬罚处理，不参与选号，也不作末位兜底，不做同号重试。到期后自动放回，由真实请求重试；仍然欠费就再暂停同样时长，不递增。写入按罚档进行（0.4.2，审查 R01，仓储 `SetTempUnschedulableTiered`）：仍在生效的软冷却（`transient:`、403 计数冷却）无论多长都改记为余额暂停，结束时间取两者较晚者；更长的硬暂停（凭据、管理员规则）保留，更短的被延长。0.4.0 用的 `SetTempUnschedulable` 只延长不缩短，更长的 403 冷却会让余额暂停写不进去，账号仍按软罚参与末位兜底。
- **入口：** HTTP 错误应答、流内余额失败（`ApplyResilienceOutcome` 的 `BALANCE`）、Grok API Key 带余额字样的 429（§18.1）、402（只会是积分不足，不落到永久置错的默认分支）。不再区分弱信号：429 加近期成功也一样暂停。
- **手动放回：** 账号列表的“恢复状态”（`DELETE /api/v1/admin/accounts/:id/temp-unschedulable`）立即结束暂停，不新增界面。
- **无号应答：** 单号分组暂停期间返回 503，`Retry-After` 取暂停的剩余时间（§9.1，夹到 2–30 秒）。
- **份额调度与 Explain：** 原因记为 `balance_exhausted`（份额调度 `ReasonBalanceExhausted`，绑定会话立即迁移）。
- **不保留余额状态：** 不读写 `accounts.extra.resilience_balance`，RC 写下的存量键直接忽略、不清理；迁移 277 删除 276 预置的两条告警规则（名称与 `metric_type` 同时匹配才删）。
- **开关：** `balance.enabled=false` 时余额失败回到 0.3.0 的处理（402 置错、403 按 403 规则等）。

以下现有分支改走余额暂停：

- Anthropic 400 “credit balance”；
- 402 的默认分支；
- 非 OpenAI 平台的 403；
- 正文带余额字样的 429（在 `handle429` 之前先分类）；
- 图片余额 402（`openai_images_balance.go`）。

### 3.3 非 OpenAI 平台的 403 与凭据失效

把 `handleOpenAI403` 的“HTML/Cloudflare 拦截页豁免 + 180 分钟内累计 3 次 + 每次冷却 10 分钟”推广到所有 API Key 平台（Anthropic、Grok、Gemini、DeepSeek 等）。余额类 403 先走 §3.2 的暂停。真正的凭据失效（401 invalid、revoked）维持现有置错逻辑。

### 3.4 瞬时失败冷却

`TRANSIENT`、`STALL` 写入软冷却（`FIRST_OUTPUT_TIMEOUT` 不写，§2）：`temp_unschedulable_until = now + cooldown`，`reason="transient:<类别>"`。

- `cooldown` 默认 30 秒；上游正文或头部给出 `retry_after` 时，取 min(retry_after, `retry_after_cap_seconds`=120)。
- 冷却只是软罚：单号组仍可通过末位兜底使用该账号（§4.3）。
- 软冷却不覆盖仍在生效的硬暂停（余额、凭据、管理员规则），只延长更短的软冷却；计数 403 冷却同样按罚档写入（总开关或 `failure` 关闭时回到 0.3.0 的写法）。成功服务后解除冷却时，只清除读取到的那一次冷却（原因与结束时间都相同，`ClearTempUnschedulableIf`），期间新写入的暂停保留（0.4.2，审查 R02）。
- **全部调用方按罚档写入（0.4.2，审查 R01 的推广）：** 除韧性层自己的暂停外，还有约 20 处调用方（临时不可调度规则、令牌刷新、传输错误、健康熔断、CN 供应商余额、Command Code 消费上限、Antigravity 等）直接写临时不可调度。`failure` 开启时，仓储的 `SetTempUnschedulable` 对它们一律按罚档写入；关闭时为 0.3.0 的只延长写法。硬罚替换仍在生效的软冷却时用**它自己的结束时间**，只有余额暂停保留两者中较晚的结束时间。取舍：短硬罚（如 30 秒的传输错误）替换 10 分钟的 403 计数冷却后，硬罚一结束，软冷却的回避也随之结束。这是偏宽松的处理，换来单号组不会以硬罚身份被停满 10 分钟；403 若仍在，下一次 403 会重新计数冷却。用例 `TestShortHardPauseKeepsItsOwnEndOverASoftCooldown`、集成 `TestAccountRepositoryTieredTempUnschedulable`。
- OpenAI 系账号的 5xx 沿用现有的按模型瞬时屏蔽（`openai_account_model_transient.go`），不整号冷却；只有 API Key 中转明确给出 `retry_after`（说明整站不可用）时才整号冷却。已输出后失败的阈值冷却（§7.6）例外，对所有非池模式账号整号软冷却。

## 4. 选号与会话失败处理（D4、D5）

### 4.1 会话失败计数

Redis 键 `xy:resil:sf:{group_id}:{session_hash}` 为 Hash，字段为 `account_id`，值为失败次数；每个账号另有字段 `e:<account_id>` 记录自己的窗口结束时间（`streak_window_seconds`，600 秒），窗口结束后计数从 1 重新开始，账号自己的失败把窗口顺延，同一会话其他账号的失败不影响它（0.4.2，审查 R06：0.4.0 每次失败都刷新整个 Hash 的 TTL，B 的失败会让 A 已过期的计数继续有效）。键的 TTL 取最新的窗口。计数规则：

- 只对计入失败的类别（`RATE_LIMIT`、`TRANSIENT`、`FIRST_OUTPUT_TIMEOUT`、`STALL`）递增；
- 所有换号循环（共用循环与 OpenAI 各路由自有的循环）每次失败尝试都经同一入口 `resilienceAttemptFailed` 上报一次，同时扣减预算；
- 已输出后的上游失败不会进入换号循环，由流处理分支自行上报一次（§7.6）；同一尝试的错误若再到达换号循环，不重复计数；
- 成功响应清零该会话对该账号的计数。本请求内在该账号上有过失败尝试（流内错误事件、停滞）时，即使按部分用量计费也不算成功：不清零计数、不解除冷却；
- 没有会话标识的请求不计数。

### 4.2 改绑规则

| 情况 | 动作 | 份额调度原因码 |
|---|---|---|
| 单次计入失败，账号进入软冷却 | **保留绑定**。下一请求遇到绑定账号仍在软冷却时，临时溢出到同层按权重的次选，不写新绑定；冷却结束后回到原账号 | `soft_spill`（份额）／`resil_soft_skip`（原版） |
| 同一会话在同一账号上，`streak_window` 内失败次数 ≥ `streak_threshold`（2） | 改绑：用 `deleteStickySessionAccountIDIfOwner` 做 CAS 删除（份额调度用 owner 校验删除），并把该账号写入会话回避集 `xy:resil:avoid:{group_id}:{session_hash}`（TTL `avoid_seconds`，600 秒）；下一请求在同层按权重重新放置。在本请求内触发时，被回避的原绑定账号不再为本请求保留绑定，本请求实际服务的账号可以写入新绑定 | `spill_rebind`，附 `resil_failure_streak` |
| 绑定账号进入硬状态（`BALANCE`、`AUTH`、管理员停用、重置时间超过 300 秒的限流） | 立即改绑 | `rebind_unavailable` |
| 并发满 | 沿用原有的等待与溢出，不改绑 | 原有 |
| TTFT 抖动 | 沿用现有 sticky escape 阈值，不加严 | 原有 |
| 客户端取消、客户端错误、本站策略拒绝 | 不动绑定，也不计数 | — |
| 带 `previous_response_id` 的续接请求 | 遵守现有所有权约束，不改绑 | 原有 |

**原版调度的语义调整**（钩子位于 `openai_account_scheduler.go` 的 `selectBySessionHash`、`openai_gateway_scheduling.go` 的旧路径（高级调度器关闭时，即缺省情况）两处粘性读取，以及 `gateway_scheduling.go` 中对应的粘性路径）：绑定账号处于软罚时，**不再清除绑定**，只在本次请求跳过它（临时溢出），本请求实际服务的账号也不写入绑定（OpenAI 粘性写入处统一检查）；硬状态仍按原样清除。OpenAI 的按模型瞬时屏蔽（`openai_model_transient_state`）只在进程内、不写账号行；0.4.2 起两条 OpenAI 粘性路径用 `resilienceOpenAIPenaltyOf` 判断绑定账号，屏蔽期间同样保留绑定（0.4.0 时会改绑）。

**份额调度**通过 `shareMissingBoundCandidate` 与候选构造钩子，把软罚账号表示为 `Eligible=false` 并带 `RecoverAt`，由现有的 `softSpill` 逻辑（`soft_spill_max_seconds`，缺省 300 秒）决定保留绑定、临时溢出。会话回避集中的账号对该会话标为 `Excluded`，原因 `resil_failure_streak`，改绑决策也记为 `resil_failure_streak`。余额耗尽（`balance:exhausted`）的账号原因为 `balance_exhausted`、`RecoverAt` 为零，走改绑；其他已有的临时不可调度原因保持 0.3.0 的处理，避免绑定释放过严（Q8）。末位兜底选中软罚账号时，在该组份额统计中记 `last_resort_soft`。兜底分组的转投与切回不是份额放置决策，计入韧性指标 `fallback_group`、`fallback_return`。

两种调度都**按优先级或层级、再按权重**选择替代账号：先在同层；只有整层不可用时才按份额调度现有的 `tier_overflow_unavailable` 规则降层。

份额分组末位兜底时（§4.3），原绑定账号不是硬罚且未被回避的，绑定保持不变（只有它自己服务时才刷新）；被回避的改绑到实际服务的账号；硬罚的由计划改绑。绑定账号在 RPM 或窗口费用的“仅粘性可用”区间时，门禁按粘性请求放行，仍可作为末位兜底（审查 F3）。

### 4.3 末位兜底

当候选集合因为“只剩软罚账号”而为空时，按以下顺序选择：

1. **分组配置了无号兜底组**（§5）：先转投兜底组。
2. **没有兜底组，或兜底组也无健康候选：** 在原组（或兜底组）里，软罚账号按优先级、再按罚期结束时间排序，依次尝试取并发槽位，取到即用；全部占满时在排序第一的账号上排队等待（不会因为最优账号忙而放着空闲的次优账号不用）。Gateway 调度器（OpenAI 以外的平台）另外跳过会话数已满的候选。
   - **门禁与正常选号一致（审查 F4）：** Gateway 用与份额候选共用的 `gatewayCandidateGates`（平台、模型、渠道限制、利润门、额度、窗口费用、RPM、会话上限等），OpenAI 用调度器原有的资格检查（含 compact 支持、Grok 额度自动暂停）；只放宽软罚：软冷却、软限流、软阈值内的模型限流，以及 OpenAI 的进程内运行时阻断。超过窗口费用或 RPM 硬上限的账号不会被选中。OpenAI 取槽与等待计划用 Codex 自适应并发（`codexEffectiveConcurrency`）。表驱动测试 `TestResilienceGateConsistency` 让每个账号只违反一个门禁，断言原版选号、份额候选与末位兜底都拒绝它。
   - **OpenAI 按模型瞬时屏蔽：** 上游连续 5xx 后按账号与模型屏蔽 10 秒、再 45 秒，只在进程内、账号行上看不到。末位兜底按屏蔽结束时间把只被它挡住的账号视为软罚（`resilienceOpenAIPenaltyOf`），否则单号 OpenAI 分组在屏蔽期间整组无号（测试服务器复现）。Codex 票据门禁不是罚则，仍是门禁。
   - **份额分组（审查 F3）：** 末位兜底是份额计划的“放宽一轮”：软罚账号与只被会话回避的健康账号视为可用（原因 `last_resort_soft`），交给 `sharesched.Plan`，遵守层级、排空、权重、HRW 与缓存域，由份额执行器取槽（含会话上限与 Codex 自适应并发）。不走模型路由优先；不写决策样本，只计 `last_resort_soft`。绑定账号软罚但正在饱和时，按份额等待预算先等它再溢出。
3. 末位兜底请求不受单次首输出超时约束，只受总预算约束（§6）。
4. **派发前复核（0.4.2，审查 R09）：** 候选来自 2 秒的分组账号列表，管理员的停用、删除、移组并不使它失效。选中的账号在取槽与派发前按 id 重读一次（只在末位兜底这条少见路径上多一次查询），已停用、删除、移出分组或进入硬罚的跳过并丢弃该组的列表缓存；份额分组经执行器的 `verify` 钩子做同样的复核。重读失败时按列表中的账号继续。
5. **会话上限（F2 跟进）：** 候选只因新会话撞上账号会话数上限而全部失败时，请求记 `LastResortSessionLimited`，这个“无号”只对新会话成立，不写负缓存（§9.2），已绑定会话照常服务。

硬状态账号永不参与兜底。末位兜底计入原因码 `last_resort_soft`，并写 ops 统计；兜底账号成功服务后解除其瞬时冷却，回到正常选号。若这一步仍然没有候选，才返回“无可用账号”。

### 4.4 Explain 与可观测

份额调度的 Explain 以及新增的原版调度 Explain 只读接口，对每个候选显示：硬、软档；罚则原因；`recoverAt`；会话失败计数；是否在回避集；末位兜底与兜底分组决策。所有阈值都可在“网关韧性”设置中调整。

原版调度 Explain 为 `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=`，对任何分组可用，入口在运维页“网关韧性”面板；份额调度 Explain 的结果附同一结构（`resilience` 字段）。末位兜底只显示按档位与恢复时间排序后的首个候选，不计模型、渠道、额度等逐请求条件。

## 5. 无号兜底分组（D8）

- **字段：** `groups.availability_fallback_group_id`（可空，迁移 272），与现有 `fallback_group_id`（仅限 Claude Code 用）互不影响。
- **触发条件：** 本请求在主组选号得到 `ErrNoAvailableAccounts`，并且原因是“全部账号为硬状态、不存在、或只剩软罚”（§4.3 第 1 步）。“换号耗尽后没有新候选”不触发，以免一次请求跨组反复重试。
- **约束：**
  - 每个请求最多兜底一次，不链式；
  - 兜底组平台须与主组一致，或可兼容（组合分组按其目标平台判断）；
  - 兜底组自身受仅限 Claude Code、模型白名单、利润门、并发与 RPM 约束；
  - 保存时做环检测，不能指向自己或形成环。
- **计费与可见性：**
  - 用量按兜底组的倍率计费，`usage_logs.group_id` 记兜底组，`usage_logs.origin_group_id`（迁移 273）记原组；
  - 用户的用量列表显示“兜底：原分组 → 实际分组”，在可用分组与渠道页显示各组的兜底关系与倍率；
  - 文档写明价格差异。
- **会话：** 兜底组内的绑定 TTL 为 `binding_ttl_seconds`（600 秒）。每个请求都先评估主组。
- **切回：**
  - 主组“稳定健康”判定：主组至少有一个硬、软两档都无罚则的可调度账号，且从首次满足起已持续 `return_stable_seconds`（120 秒）。
  - 稳定时间戳记在 Redis `xy:resil:group_stable:{group_id}`；任一时刻不满足即清除。
  - 满足后，会话的下一请求回到主组，不等缓存变冷，原因码 `fallback_return`。
  - 主组健康、且本实例在 `binding_ttl_seconds` 内没有把该组任何 Key 转去兜底时，直接留在主组，不访问 Redis（稳态零往返）。代价：重启前转出的 Key 在重启后立即回到主组。
- **界面：** 分组编辑增加“无号兜底分组”选择；保存时检查环与平台。

## 6. 请求预算与重试（D18）

### 6.1 预算对象

`resilience.Budget` 挂在请求上下文中：

| 字段 | 含义 |
|---|---|
| `MaxAttempts` | 默认 4，包括首次尝试、换号与同号重试。例外：Antigravity 平台的智能重试（`antigravity_gateway_retry.go`，按上游给出的短等待在同一账号上重发）不计入尝试次数，只受剩余首输出前时间与 `Retry-After` 封顶约束（等待放不进剩余预算就不再重试） |
| `PreOutputDeadline` | 文本请求为开始时间 + 240 秒；图片端点与异步图片任务不设。非流式请求的“首输出”就是整个响应：截止时间只决定能否再发起新的尝试，进行中的单次非流式请求不按它截断（审查 R04 的取舍：截断仍在生成的长回答只会多一次失败、多一份上游成本；单次请求仍受上游与客户端各自的超时约束） |
| `Attempts`、`SameAccountRetries[account]` | 已用次数 |
| 请求上下文 | 客户端或转发上下文取消后不再允许新的尝试（耗尽原因 `canceled`） |

接入点：

- OpenAI 的 Responses、Chat、Messages、Images 换号循环（`openai_gateway_handler.go`、`openai_chat_completions.go`、`openai_images.go`、`openai_gateway_messages` 相关 handler）；
- Anthropic 网关循环（`gateway_handler.go`、`gateway_handler_responses.go`、`gateway_handler_chat_completions.go`）；
- Gemini 兼容服务的原地重试（`gemini_*_compat_service.go`）；
- 透传传输重试；
- 用户与账号槽位等待（`waitForSlotWithPingTimeout`）：等待时长截到剩余的首输出前时间，否则排队的流可能越过 240 秒、由客户端自己的空闲超时结束，而不是收到终止应答（审查 F12）。

每次失败尝试后记一次（`RecordAttemptAt`），再由 `CanAttempt` 判断能否继续，耗尽则结束：

- **未提交：** 返回现有的最后一次失败应答，按 §9 加 `Retry-After`；
- **已提交：** 按 §7.6 写终止帧。

### 6.2 同号重试

- 只用于 `CAPACITY_SHED`，以及池模式账号上管理员配置的状态码；每个账号最多 `same_account_retry_max` 次（1 次）。
- 账号级错误（`BALANCE`、`AUTH`、长时间的限流、504/524、耗时 ≥60 秒的失败）一律直接换号。
- Gemini 原地重试从 5 次降为 `gemini_same_account_retries`（1 次）；没有预算的请求（WebSocket、内部调用）保持 0.3.0 的循环上限。退避等待改为可取消的 `select`，替换 `time.Sleep`。
- 服务内原地重试（Anthropic 转发、透传、Bedrock）在耗时与等待检查都通过、确定执行时才扣减预算与同号次数，避免同一次失败被记两次、提前一次耗尽。
- 退避一律加 ±20% 抖动。尊重上游的 `Retry-After`，上限 `upstream_retry_after_cap_seconds`（60 秒），并且不超出剩余预算。

### 6.3 优先换号

“最大换号次数”由预算取代。现有 `max_account_switches` 仍作为上限保留，取两者较小值。`CAPACITY_SHED` 最多换 1 次，并且要求换到不同 base_url 主机；做不到时直接按无号处理。

## 7. 首包前保活、首输出超时、停滞与提交后的失败

### 7.1 边缘识别（D12；0.4.1 修订：自动识别 CDN 回源）

应用按以下顺序判断请求是否经 CDN（`resilienceNoteEdge`）：

1. **反向代理的标记（可选）：** 对端是受信代理（`SERVER_TRUSTED_PROXIES`）时读取 `X-XY-Edge`，值在 `keepalive.edges` 白名单内即采用；非受信对端发来的忽略（spec 场景 “Spoofed marker is ignored”）。
2. **CDN 自带的回源头（0.4.1 起）：** 没有有效标记时，`CF-Ray` 或 `CDN-Loop` 含 `cloudflare` 判为 `cf`，`CDN-Loop` 含 `esa` 判为 `esa`（阿里云 ESA）。`CDN-Loop`（RFC 8586）按经过顺序列出 CDN，取离源站最近且在白名单内的一个；只有 `CF-Ray` 时取 `cf`。推断出的名字同样须在 `keepalive.edges` 内，管理员去掉 `cf` 或 `esa` 即关闭对应识别；不新增设置字段。

- **生产依据（2026-10-18，只读核对访问日志）：** Cloudflare 站点的回源请求全部带 `CF-Ray` 与 `CDN-Loop: cloudflare; loops=N`；ESA 站点全部带 `CDN-Loop: esa;loop=N`（另有 `Eagleeye-Traceid`），没有 `CF-Ray`；XY-image 经内网直连的请求两者都不带。ESA 用 `CDN-Loop` 识别：它是标准头，与 Cloudflare 同一机制。
- **推断为什么不要求受信对端：** 伪造这些头只改变伪造者自己那个请求的返回形式，即提前提交 SSE、15 秒后发心跳、失败改为流内终止帧、502/504 改为 503；不涉及鉴权、计费或其他用户。XY-image 与内部脚本直连时不带这些头，不受影响。
- **为什么不自动配置 `SERVER_TRUSTED_PROXIES`：** Docker 下对端是网桥网关，端口对外发布时直连客户端也来自同一地址；自动信任会放开来源 IP 伪造，绕过 IP 白名单与限流。改成自动识别后，首包前保活不再依赖它。
- **Caddy（可选）：** 接入其他 CDN 或自定义部署时，仍可在站点中先删除客户端自带的 `X-XY-Edge`，再按**对端 IP**（`remote_ip` 匹配器，不信任任何请求头）是否属于 CDN 回源网段注入 `X-XY-Edge`；此时须把反向代理到应用的对端地址配进 `SERVER_TRUSTED_PROXIES`。配置见 runbook §3.1。0.4.0 的这两项部署前提在 0.4.1 不再必需。

### 7.2 提交条件（D3、D12、D15、D21）

同时满足以下条件时，`resilience.ArmPreOutputKeepalive` 为请求设置延迟心跳：

1. 请求为流式（`stream=true`；Responses 和 Chat 以请求体为准，Anthropic Messages 以 `stream` 为准）；
2. 入站协议为 OpenAI Responses、OpenAI Chat Completions 或 Anthropic Messages；Gemini 原生入口与 genai UA **不做**；
3. 经 CDN（§7.1 的边缘识别，D12）；
4. 鉴权、余额与订阅检查、在途预留、参数与模型映射、内容审计、选号都已完成（失败仍按原状态码返回）；
5. 选中的账号不是官方 OAuth 账号（`AccountTypeOAuth` 等 Codex 原生类型，D21）；
6. 不是图片或异步任务端点。

设置后：

- 若 `delay_seconds`（15 秒）内仍未向客户端写过任何字节，就提交 SSE 响应头，然后每 `interval_seconds`（10 秒）写一次心跳，直到第一个语义字节或请求终止。
- 提交的响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`、`X-Client-Request-ID`、请求 ID。
- 保活时钟按请求计算，换号时不重置。
- 实现上泛化 `openai_compact_sse_keepalive.go` 的原语：延迟首拍；心跳字节计入 `openAIStreamKeepaliveBytesKey` 并从“已输出”判断中扣除；写侧加锁，与正常写入互斥。

### 7.3 心跳帧（D15）

| 入站协议 | 心跳帧 |
|---|---|
| OpenAI Responses / Chat | `:\n\n`（注释行） |
| Anthropic Messages | `event: ping\ndata: {"type":"ping"}\n\n` |
| Gemini | 不发 |

注释行心跳只防 CDN 切断，不重置 Codex 的空闲计时。因此首输出前的总时长必须小于 300 秒（§7.4）。

### 7.4 首输出超时（D16）

- **OpenAI：** 启用并扩展现有的 `openai_first_output_timeout`（`openai_first_output_timeout.go`）。单次超时 = min(分档阈值, 剩余预算)，从本次尝试开始计时：
  - `reasoning_effort` 为空、minimal、low、medium：180 秒；
  - high：180 秒；xhigh：200 秒；
  - max：220 秒；
  - 可按模型覆盖（`model_overrides`）。
  - 分档按生产 7 天首字分布校准（tasks 0.4）：原 60/120/180 秒会误切约 1.3% 的成功请求，现各档超出比例约 0.10%–0.26%。
- **Anthropic、Gemini：** 新增“首个上游事件或首块”超时，默认 90 秒。
- 取消“首输出超时只换 1 次”的限制（`openAIFirstOutputFailoverExhausted`），改由预算约束。
- **最后一个可用候选**（没有其他健康候选、分组内只有这一个可用账号，或已是末位兜底）不设单次超时，只受 `PreOutputDeadline` 约束。“可用”按本请求判断（0.4.2，审查 R07）：其他账号不支持请求的模型、该模型被硬限流，或已在本请求中失败的，不算备选。
- 图片端点与异步图片任务不适用。

### 7.5 停滞判定（G2 修复）

- 把间隔检测改为以 `min(T/4, 5s)` 为周期检查 `lastRead`，使误差不超过 5 秒。位置：`openai_gateway_response_handling.go` 约 190、915；`gateway_upstream_response.go` 约 782、1120。
- 文本流已输出后的停滞阈值为 `text_stall_seconds`（120 秒），明显小于 Codex 的 300 秒。到点后按 §7.6 结束，对账号施加 `STALL` 软冷却，并计入会话失败（§4.1，每个请求每个账号一次）；同时标记失败回合，停滞的 200 不清零回合计数（0.4.2，审查 R10）。
- **输出前（0.4.2，审查 P1-C）：** OpenAI 路径输出前的停滞阈值沿用 0.3.0 的 `stream_data_interval_timeout`（180 秒），早于 xhigh/max 的 200/220 秒档位，上游给出响应头后完全静默时请求以终止帧结束、不换号（只有 Grok 换号）。首输出守卫已启用且客户端尚无输出时，停滞检测让给守卫：到档位时按首输出超时换号（不冷却账号，与 §2 一致）。守卫未启用（关闭预算且未配置 0.3.0 首输出超时）时保持原行为。
- 图片流维持 `image_stream_data_interval_timeout`（900 秒）；OAuth Responses 生图期间豁免文本阈值。

### 7.6 已提交后的失败（D17、G3）

| 入站协议 | 终止帧 |
|---|---|
| Responses | `event: response.failed`，`response.error.code` 为 `rate_limit_exceeded`（限流或忙）或 `server_error`（停滞、5xx、预算耗尽）；`message` 含 `Please try again in Ns.`（N = clamp(冷却剩余, 2, 30)）；保留 `sequence_number`。客户端此前只收到心跳（首包前保活、排队 ping）时，先发同一响应的 `response.created`（`status: in_progress`，`sequence_number` 0），`response.failed` 的序号为 1：openai-python、openai-node 要求流以 `response.created` 开始，否则抛协议错误，看不到网关的错误（RC 真实客户端发现）。已有上游事件送达时不补；总开关关闭时与 0.3.0 一致 |
| Chat Completions | `data: {"error":{"type":...,"code":...,"message":...,"status":<原 HTTP 语义码>}}`，然后 `data: [DONE]` |
| Anthropic Messages | `event: error\ndata: {"type":"error","error":{"type":"overloaded_error"或"api_error","message":...}}`（修复 G3：所有提交后的错误帧都补 `event: error`；`errors` 开关关闭时恢复 0.3.0 的无 `event` 行格式） |

写完终止帧后立即 `Flush` 并结束响应，因为 Codex 只在流结束时上报 `response.failed`。错误 JSON 只占一行，带稳定的 `code`，以及原 HTTP 语义状态 `error.status`（XY-image 与其他客户端可据此还原语义）。

**已输出后失败的计数（生产 R2）。** 已输出后不能换号，但失败仍属于该账号。识别出已输出后上游失败的流分支（流内失败事件、读错误、无终止事件即结束）对该尝试只上报一次（`resilience_post_output.go`）：

- 会话按 `TRANSIENT` 计入失败计数（§4.1），达到 `streak_threshold` 即改绑并回避。降载类文案（`CAPACITY_SHED`）在输出前是请求级的，输出后说明该账号此刻不可靠，同样计入；
- 账号进程内滑动计数，60 秒内 3 次即施加瞬时软冷却 `transient:POST_OUTPUT`（时长 `transient_cooldown_seconds`），施加后清零；软冷却不影响末位兜底，池模式账号不罚；
- 本请求在该账号上不算成功（`MarkAccountFailed`）；
- 输出前会换号的失败才计入：客户端错误、内容安全与 cyber 拒绝、上下文超长、客户端断开不计；
- 总开关关闭时不计；`session` 关闭时只保留账号侧冷却。指标 `post_output_failure`（细分 `stream_failed`、`stream_read_error`、`missing_terminal`），日志 `resilience.post_output_failure`（warn）。

### 7.7 与现有流内 keepalive 的关系

上游响应头到达后，仍由现有的 10 秒流内 keepalive 负责，口径不变。首包前心跳与它共享“已输出”扣除口径，换号判定以语义输出为准。G8（暂存账号头丢失）由 D21 规避：官方 OAuth 账号不提前提交。现有流内 keepalive 下也存在的 G8 影响列为 P2 观察项（tasks 12）。

### 7.8 中转站伪保活帧（测试服务器发现）

部分中转站会在 `response.created` 之前插入伪保活帧：

```text
event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","delta":"","SSE-Keep-Alive":true}
```

它不属于协议。原样转发会让严格 SDK 报错（openai-python：“Expected to have received response.created before response.output_text.delta”）。网关还会把它当成语义输出，从而提前放弃换号和首输出超时。因此读取上游 SSE 时直接丢弃这类帧：类型为 `response.output_text.delta`、`delta` 为空，且 `item_id` 为 `SSE-Keep-Alive` 或带 `SSE-Keep-Alive: true`。其他字节原样通过。总开关关闭时不过滤。

丢弃的帧算“上游存活、不算输出”：停滞检测（§7.5，输出后 120 秒、输出前按 `stream_data_interval_timeout`）以最后读到一行与最后丢弃一帧中较晚者计时，与 0.3.0 帧原样通过时一致；否则只靠这种帧维持静默期的中转会被误判停滞、截断回答并冷却账号（生产核查发现，0.4.0 引入的回归）。覆盖 Responses 流、经 Responses 上游的 Chat Completions 与 Messages 转换（流式与非流式读终止事件）；passthrough 与原生 Chat Completions 上游的路径（`streamRawChatCompletions` 与两条 chat fallback 的 `scanCCStream`）无停滞检测，只计数。每丢一帧按账号计 `relay_keepalive`，上线后据此判断这类中转是否真实存在。不解决 Codex 自身的 300 秒空闲上限（它只认带数据的事件，向客户端发数据事件有兼容风险，0.4.0 不做）。

首字时间与 0.3.0 一致（用户要求）：0.3.0 中这类帧原样通过，被当作首个事件记进 `first_token_ms`。`openai_ttft_mode=semantic`（缺省）时，结果取本次尝试中首个被丢帧与首个真实事件中较早者，从本次尝试开始计时；`visible` 不变，不算这类帧。只在结束构造结果时取值：帧照样丢弃，首输出超时与换号、停滞检测、送达判定和首输出守卫仍只认真实输出，计时不变。覆盖 Responses 流、passthrough、经 Responses 上游的 Chat Completions 与 Messages 流式转换、原生 Chat Completions 直转。非流式转换在 0.3.0 与现在都不记录首字；两条 chat fallback 的首字一直取首个输出 chunk（帧没有 choices，0.3.0 在那里也不计）：二者均不变。代价同 0.3.0：对发这类帧的中转，依赖首字的统计（调度首字评分、渠道监控健康分、首输出分档校准）偏乐观，要看真实值改用 `visible`。

## 8. 客户端断开与计费（D2、D9、D20）

### 8.1 送达状态

| 状态 | 判定 |
|---|---|
| `undelivered` | 尚未写出任何语义字节（心跳与已提交的头不算，包括等待用户槽、账号槽和用户消息队列时发出的 ping（审查 F1）；非流式则是响应体尚未写出） |
| `partial` | 已写出语义字节，但未完成 |
| `complete` | 正常完成 |

**口径（0.4.2，审查 R03；取代下段 0.4.0 的说明）。** 送达写入器（`deliveryWriter`）对 `text/event-stream` 响应逐块判读，直到第一个内容块：只有 SSE 注释行、`ping`、`keepalive`、Anthropic `message_start` 与空的 text/thinking `content_block_start`、Responses `response.created`/`in_progress`/`queued`、只带角色的 Chat 分块算开场事件；其余（含错误事件、`[DONE]`、`tool_use` 块、未知事件、超过 64 KiB 仍未结束的块）一律算内容，所以判读不了的形式只会回到字节计数。之后不再判读。送达探针在流式响应写出内容前返回 0，写出后返回字节数（至少为 1）；非流式响应仍按字节计数。只改计数：事件照常、按时写出，换号判断仍用原有的写出字节扣除保活后的计数。覆盖全部流式路径，不需要在各路径登记字节（原计划的 `NoteDeliveryHeartbeat` 逐点登记不再需要）。

**0.4.0 的口径（评审 A3，已由上段取代）。** 首包前保活心跳与排队 ping 在所有路径都不算语义字节。流内保活注释只在 OpenAI `/v1/responses` 流式路径（含透传）扣除，因此“首个语义输出前断开不计费”只在这条路径完整成立。其他流式路径在首个真实内容前就会写出开场事件或流内心跳，这些字节目前算已送达，此后断开按 `partial` 处理：Chat Completions 兼容转换（收到 `response.created` 时的角色块、`:` 心跳）、Messages 兼容转换（`message_start`；`ping` 会置 `clientOutputStarted`）、Anthropic 原生与直通（`gateway_upstream_response.go`、`gateway_anthropic_passthrough.go`、`openai_gateway_messages_anthropic_native.go`）、Grok `/v1/responses`（不暂存，`: ping`）、图片 SSE（`openai_images.go`、`openai_images_responses.go`）。非流式与图片 JSON 以响应体是否写出为准。原计划在 0.4.1 用 `NoteDeliveryHeartbeat` 逐点登记这些字节，0.4.2 改为由送达写入器统一判读实现（上段），约束不变：只改计数，不压住、不推迟开场事件，不改计时，不增加上游成本。

### 8.2 处理

- **`undelivered` 时断开：**
  - 立即取消上游上下文（包括等待响应头与换号间隙），停止预算内的后续尝试，释放在途预留与并发槽位；
  - **不写 `usage_logs`，不计费**；
  - 写一条 `gateway_undelivered_requests`（迁移 274）：`request_id`、`client_request_id`、`user_id`、`api_key_id`、`group_id`、`account_id`、`platform`、`model`、`inbound_endpoint`、`disconnect_stage`（`select` 选号，含等待槽位；`header_wait` 等待响应头；`pre_output` 首输出前）、`duration_ms`、`upstream_usage_json`（上游已给出时）、`estimated_upstream_cost`、`billed`、`created_at`。
- **`partial` 时断开：**
  - 继续读取上游以获得用量，最长 `partial_drain_max_seconds`（180 秒）；
  - 到时取消，按已解析到的用量计费；
  - 上游未给出用量时，按已送达内容的本地估算计费（复用现有估算）。
- **滥用保护：**
  - Redis 计数 `xy:resil:undelivered:{api_key_id}:{hour}`；
  - 同一 Key 每小时超过 `threshold_per_hour`（30）次后，后续 `undelivered` 按上游成本计费：上游给出用量时按用量；否则按输入 Token 估算 × 账号上游成本倍率；
  - 这些计费写入 `usage_logs`（`billing_mode='undelivered_abuse'`），同时在未送达记录中把 `billed` 置为 true，用户侧可见。
- **`ops_error_logs` 补充：** 499 记录补全 `duration_ms`，并新增 `disconnect_stage` 与 `delivered_bytes` 两列（迁移 275）。生产开启了 `ignore_context_canceled`，普通取消不入 `ops_error_logs`，以未送达记录与新增统计为准。

## 9. 错误语义与防风暴（D14、D19、D13）

### 9.1 `Retry-After` 与重试头

| 场景 | 状态（不变） | 新增 |
|---|---|---|
| 暂时无可用账号 | 503 | `Retry-After = clamp(组内最早解冻剩余时间, 2, 30)` 秒；正文追加 `Please try again in Ns.` |
| 全部限流 | 429 | `Retry-After = clamp(最早重置剩余时间, 2, 30)`；没有它 Codex 会立即终止 |
| 配置类 503（渠道定价限制、仅限 Claude Code 且无降级组） | 503 | `Retry-After: 60`、`x-should-retry: false` |
| 上游余额问题导致无号（§9.4） | 503 | 同“暂时无可用账号” |
| 在途预留超额（§9.5） | 429（原为 403） | `Retry-After: 5`（±1 秒抖动） |

选号层需要返回“最早解冻时间”，由罚则查询顺带给出。所有 `Retry-After` 都由服务端封顶。

### 9.2 无号负缓存

在进程内缓存“(分组, 平台, 模型) 无可用账号”的结论 `negative_cache_seconds`（2 秒），期间直接返回相同的 503 和剩余的 `Retry-After`，不重复查库与快照。键由分组与模型构成（分组决定平台，组合分组的目标平台由模型决定）；OpenAI 选号另按传输方式、端点能力与平台区分，一种请求的结论不回答另一种。带 `previous_response_id`、图片能力或 compact 的 OpenAI 选号以及利润控制分组不缓存；重放出的结论不会再次写入，条目按时过期。

- **只缓存组级结论（审查 F2）：** 选号在粘性查找之前查负缓存，而“无号”可能只对本请求成立：超过 RPM 或窗口费用阈值的 Anthropic OAuth 账号只服务已绑定会话，会话数上限只拦新会话。所以只有组内不存在“平台匹配、支持该模型且无罚”的账号时才写入（判定用 2 秒的分组账号列表）；否则一个新会话的结论会让已绑定会话持续 503。强制平台的路由（`/antigravity`）把平台写进键，不回答同组普通路由。本网关施加或解除冷却、施加余额暂停时主动失效对应分组的条目；管理员改动等其他来源靠 2 秒过期，最多 2 秒后可见。

### 9.3 同 Key 快速失败

- **触发：** 同一 API Key 在 `window_seconds`（30 秒）内收到 ≥ `threshold`（10）次**完全相同**的 503（状态、`error.type`、`message` 均相同）。只计对该 Key 与模型的所有请求都成立的 503：组级无号（与 §9.2 同一判据，含负缓存命中；不写负缓存的续链、图片与 compact 选号也按同一判据标记）和配置错误；只关乎一个会话的 503 不计，否则会连带拒绝同一 Key 的其他会话（审查 F5）。
- **动作：** 之后窗口内同一模型的请求在选号与内容审计之前就直接返回**同一应答**（状态、正文、剩余的 `Retry-After` 均不变）；同一 Key 对其他模型的请求照常处理。只有已触发时才读取请求体中的 `model`（Gemini 取路径中的模型）。
- **豁免：** 异步任务轮询、capabilities、DELETE、`/v1/usage`、`/v1/models`、幂等重放。
- **存储：** 进程内滑动窗口，多实例时各自计数，属于尽力而为。
- **作用域（0.4.2，审查 R05）：** 计数与重放都按请求路径与 Key 的分组区分（`scope = path|group`，在可用性兜底改动分组之前取值）。强制平台的路由（`/antigravity/...`）、另一端点或 Key 改组前的 503 不回答其他路由。
- 快速失败不计入账号失败，单独计数。

### 9.4 上游余额对用户隐藏（D13-1）

上游 `BALANCE` 类错误在换号耗尽或无号时，统一以 503 “Service temporarily unavailable”加 `Retry-After` 返回，包括图片余额 402（`Upstream image account has insufficient balance`）。管理员在运维错误详情与账号的暂停原因中看到原文。管理员配置的错误透传规则命中该上游应答时以规则为准，网关路由与 OpenAI 路由一致，此时不加隐藏余额的重试提示。

### 9.5 用户自身余额（D13-2、D13-3）

- **真实余额不足**（`api_key_auth.go` 的预检，以及 `billingErrorDetails` 中的余额分支）：统一为 403，`code: INSUFFICIENT_BALANCE`，文案沿用 0.3.0 原文 `Insufficient account balance`（用户 2026-10-17 要求不改提示文案；下游 sub2api 系站点按缺省余额关键字识别）。OpenAI 兼容端点使用 `{"error":{"type":"insufficient_balance","code":"INSUFFICIENT_BALANCE","message":...}}`，中间件路径保持原有的 `{code,message}` 形状。
- **在途预留超额**（`reserveInflight` 拒绝时余额 > 0）：改为 429，`error.type="rate_limit_error"`，`code="concurrent_balance_reserved"`，文案为 `Available balance is reserved by in-flight requests (available $X, reserved $Y); please retry shortly`，并带 `Retry-After`。
- **估算改进**（`billing_inflight_reservation.go` 的 `estimateOne`）：
  - 输入部分：对带会话标识的请求，按该会话最近一次的“缓存读 / 输入”比例折算，以缓存读价格计入（会话缓存比例存于 Redis，TTL 30 分钟）；无历史时维持原口径；
  - 输出部分：取 min(`max_tokens`, 该模型近 7 天 P99 输出 Token)，下限 1024；
  - 验收（tasks 4.6）：用生产 7 天数据回放，误拒减少 ≥90%，同时余额为负的金额不超过改前。

### 9.6 异步图片准入前移

在 `image_task_handler.go` 中，把“每实例与每 Key 的准入检查”放到 `checkSecurityAuditBeforeSubmit` 之前。被拒的请求不再消耗内容审计，也不读取完整请求体。审计未通过时释放准入。

### 9.7 失败回合封顶（测试服务器发现）

Codex 对带 `Retry-After` 的 5xx 没有次数上限：传输层每轮 5 次请求，回合层再重试 5 轮。测试服务器上，一个全失败分组的回合产生了 30 次请求、历时 871 秒。流内失败（已提交后的 `response.failed`）也会被回合层重试 5 次。

- **计数：** 按会话（API Key、分组、模型、粘性会话哈希）统计连续的“耗尽”失败，即无号 503、换号耗尽后的 429/5xx，以及已提交流里的同类失败。选号时记录会话；首次选号前的失败不计。没有会话哈希的请求彼此独立，不计数（Codex、Claude Code 都带会话标识）。
- **封顶：** 连续第 `errors.turn_cap.failures`（5）次失败改为终止应答：
  - 不带 `Retry-After`，加 `x-should-retry: false`（OpenAI、Anthropic SDK 与 Claude Code 据此停止重试）；
  - `error.code` 为 `server_is_overloaded`：Codex 只在服务端给出延迟时才重试它（HTTP 与流内都一样）；
  - 5xx 改为 503（Codex 只在 503 上识别这个 code）；429 保持 429，不带 `Retry-After` 的 429 Codex 直接终止。
- **重置：** 同一会话一次真正的成功（2xx 且未在流内失败）清零；两次失败间隔超过 `window_seconds`（120 秒）重新计数。已输出后的上游失败（§18.2）不计数（终止应答已无法改写），但它的 200 也不清零。
- 封顶只改变失败应答的形态，每次请求仍正常选号与尝试，分组恢复后立即成功。
- 进程内尽力而为。快速失败（§9.3）的原样返回发生在选号之前，不参与计数；封顶后的终止应答只属于一个会话，也不计入快速失败（否则重放时会补上 `Retry-After`，让 Codex 重新开始重试）。
- 指标 `turn_cap`，日志 `resilience.turn_cap`（warn）。

### 9.8 续链被拒时整轮重放（测试服务器发现）

Claude 客户端请求分发到 GPT 中转（Messages 转 Responses）时，网关按会话附带上一轮的 `previous_response_id`，只发送最后一轮输入。真实多账号中转站会把上一轮响应存在另一个上游账号上，再次请求时返回笼统的 400（如 “Upstream request failed”“The request could not be processed”），而不是可识别的“previous response not found”。0.3.0 只在可识别时才去掉续链重试，于是该会话从第二轮起每轮都是 400；按 §2 这类 400 属于客户端错误，不换号。

- 附带续链的请求收到 400/404/409/422，且不是完整重放同样会失败的错误（上下文超长、内容审核、图片、模型不存在）时，去掉续链按完整历史重放一次。0.4.2 起（审查 R08、F8）只认指向续链的应答：404/409；文案提到 previous response 或 response id；400/422 上的笼统中转文案（单独的 “Upstream request failed”、“request failed”、“bad request”、含 “could not be processed”，或没有文案）。`error.param` 指名其他参数、或文案是具体的参数错误时不重放；
- 按笼统文案猜测的重放计入请求预算的一次尝试（`ConsumeInPlaceRetry`），预算不允许时不重放、原样返回；可识别的“续链不存在/不支持”的重放只是撤销网关自己加的续链，预算用尽也照常重放；
- 同时对该会话在该账号上关闭续链（与“上游不支持续链”相同，时长为原有粘性 TTL），后续轮次直接完整重放，不再先失败一次；若完整重放同样失败，说明是请求本身的错误，续链随即恢复；
- 可识别的“续链不存在”仍按原逻辑只删除绑定。
- 续链绑定表（进程内）原先只在查询时删除过期项，不再回来的会话会一直留在内存里；现在每写入 1024 次清扫一遍过期项。会话失败与回避标记同样每分钟清扫（浸泡中发现内存缓慢上升）。
- 验证：单测 `TestForwardAsAnthropic_ContinuationRejectedReplaysAndStaysOff`；测试服务器场景 CONT-1（fault-upstream F25 模拟拒绝续链的中转）与真实中转复测。

## 10. 配置项（`gateway_resilience_settings`）

存于 `settings` 表（JSON）。管理接口为 `GET/PUT /api/v1/admin/settings/gateway-resilience`，前端为“网关韧性”设置卡（每项能力一张卡片，开关、说明与该能力的常用数值放在一起）。缺省值即设计值：

```json
{
  "enabled": true,
  "failure": { "transient_cooldown_seconds": 30, "retry_after_cap_seconds": 120,
               "balance_keywords_extra": [] },
  "session": { "enabled": true, "streak_threshold": 2, "streak_window_seconds": 600, "avoid_seconds": 600 },
  "last_resort": { "enabled": true },
  "balance": { "enabled": true, "pause_seconds": 300 },
  "budget": { "enabled": true, "max_attempts": 4, "pre_output_budget_seconds": 240,
              "first_output_timeout_seconds": { "default": 180, "high": 180, "xhigh": 200, "max": 220 },
              "anthropic_first_event_timeout_seconds": 90, "gemini_first_event_timeout_seconds": 90,
              "model_overrides": {}, "same_account_retry_max": 1, "gemini_same_account_retries": 1,
              "upstream_retry_after_cap_seconds": 60 },
  "keepalive": { "enabled": true, "delay_seconds": 15, "interval_seconds": 10, "edges": ["cf", "esa"],
                 "skip_official_oauth": true },
  "stall": { "enabled": true, "text_stall_seconds": 120 },
  "disconnect": { "enabled": true, "cancel_undelivered": true, "partial_drain_max_seconds": 180,
                  "abuse_guard": { "enabled": true, "threshold_per_hour": 30, "mode": "bill_upstream_cost" } },
  "errors": { "enabled": true, "no_account_retry_after": { "min": 2, "max": 30 }, "config_retry_after_seconds": 60,
              "reservation_retry_after_seconds": 5, "negative_cache_seconds": 2,
              "fast_fail": { "enabled": true, "window_seconds": 30, "threshold": 10 },
              "turn_cap": { "enabled": true, "failures": 5, "window_seconds": 120 } },
  "fallback_group": { "enabled": true, "return_stable_seconds": 120, "binding_ttl_seconds": 600 },
  "reservation": { "cache_aware_estimate": true, "output_p99_cap": true }
}
```

校验规则：

- `keepalive.delay_seconds` 取 5–60；`interval_seconds` 取 5–30；
- `pre_output_budget_seconds` 取 60–290；
- 每个首输出阈值不得超过预算；
- `text_stall_seconds` 取 30–280；
- `max_attempts` 取 1–10；
- `errors.turn_cap.failures` 取 2–30，`window_seconds` 取 10–3600；
- 所有 TTL ≥1 秒。

某个子开关为 false 时，对应能力回到 0.3.0 行为。读取失败时使用最后已知值；冷启动无值时用缺省值。

设置在读取与发布时都深拷贝：PUT 请求体解码到副本上，不写入请求路径正在读取的快照，未通过校验的 PUT 不改变运行中的任何值。请求体中出现的 `budget.model_overrides` 整体替换原值（因此可以删除键）。

## 11. 数据库迁移（只追加）

| 文件 | 内容 |
|---|---|
| `272_add_group_availability_fallback.sql` | `ALTER TABLE groups ADD COLUMN IF NOT EXISTS availability_fallback_group_id BIGINT NULL REFERENCES groups(id) ON DELETE SET NULL;` |
| `273_add_usage_logs_origin_group.sql` | `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS origin_group_id BIGINT NULL;`（不加外键与默认值，大表上只改元数据） |
| `274_create_gateway_undelivered_requests.sql` | 新表 `gateway_undelivered_requests`（字段见 §8.2）；索引 `(created_at)`、`(api_key_id, created_at)`、`(user_id, created_at)`；按 `ops_advanced_settings.data_retention` 清理，默认 30 天 |
| `275_add_ops_error_logs_disconnect_stage.sql` | `ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS disconnect_stage VARCHAR(32) NULL, ADD COLUMN IF NOT EXISTS delivered_bytes BIGINT NULL;` |
| `276_seed_balance_alert_rules.sql` | 预置告警规则“上游余额不足”（`account_balance_exhausted_count > 0`，P2）与“上游余额长期未恢复”（`account_balance_unrecovered_count > 0`，P1），`ON CONFLICT (name) DO NOTHING`；0.3.0 不认识这两个指标，评估时跳过。已在 RC 与测试环境执行，保持不变 |
| `277_remove_balance_alert_rules.sql` | 删除 276 预置的两条规则（名称与 `metric_type` 同时匹配才删，管理员改过名或改指着其他指标的规则保留），§3.2 |

- `migrations/checksums.json` 只追加；已发布的 318 条不变。
- Ent schema（`groups`、`usage_logs`、`ops_error_logs` 与新表）同步更新，并重新生成 `ent/`。
- 回退到 0.3.0 时，旧版忽略新列与新表，不影响写入（全部可空，无新约束）。

## 12. 可观测性与管理界面

- **结构化日志**：消息即事件名（前缀 `resilience.`），细节放在字段中。管理员关注的为 warn 级，进入 `ops_system_logs`；量大的为 info，只写标准输出。失败分类不单独记日志，由 `resilience.transient_cooldown`、`resilience.balance_paused` 与 ops 记录的 `resilience_category` 承担。各事件的字段见 `docs/GATEWAY_RESILIENCE.md` §5.3：
  - warn：`balance_paused`、`session_rebind`、`last_resort`、`fallback_group`、`fallback_return`、`budget_exhausted`、`capacity_switch_exhausted`、`stall`、`post_output_failure`、`fast_fail`、`turn_cap`、`abuse_guard`、`web_search_history_rejected`；
  - info：`keepalive_committed`、`undelivered`、`unclassified`、`transient_cooldown`、`transient_lifted`、`undelivered_cleanup`、`content_policy_refusal`、`content_policy_no_failover`、`web_search_replay`、`web_search_declaration_learned`；
  - 以 `_failed` 结尾的事件表示存储或查询失败（warn），功能降级为原有行为。
- **ops 错误记录的状态码**是网关自己的应答状态：经 CDN 的 502/504 对客户端改写为 503（§7.6、X-XY-Upstream-Status），ops 记录仍记原值，否则会与无号 503 混在一起、让 §14 的前后对比失真（审查 F7）；读请求体时连接中断造成的 400 记为 499。
- **分类结果**写入每次上游失败的 ops 记录（`upstream_errors[].resilience_category`）；只按状态码归类的上游 4xx（401、402 除外）以 info 级 `resilience.unclassified` 采样记录脱敏、截断后的文案（每进程每分钟约 10 条），用于补充关键字。
- **统计接口**：`GET /api/v1/admin/resilience/stats?window=`，按分组与账号汇总以下指标（计数名与含义见 `docs/GATEWAY_RESILIENCE.md` §5.2）：
  - 首包前心跳提交数（按协议）；
  - 首输出超时换号（按阶段）；
  - 预算耗尽；
  - 末位兜底；
  - 兜底分组与切回；
  - 会话改绑；
  - 未送达（按阶段）；
  - 推断的 CDN 切断（未送达且阶段为等待响应头或首输出前，按耗时分入 90、125、300 秒桶）；
  - 快速失败；
  - 失败回合封顶；
  - 滥用保护计费；
  - 停滞断流；
  - 已输出后的上游失败（按失败类型，§7.6）；
  - 丢弃的中转伪保活帧（按账号，§7.8）。
- **运维页**新增“网关韧性”面板；余额不足暂停在账号列表中显示为临时不可调度（原因 `balance:`），“恢复状态”立即放回；未送达记录可查询（`GET /api/v1/admin/resilience/undelivered`）；计数与未送达列表显示分组名；面板底部为“选号解释”（§4.4 的原版调度 Explain）。
- **降低日志噪声**：
  - `upstream_billing_rate_sync_applied` 从 info 降为 debug；
  - `ops_system_logs.message` 只保留模板，原文进入 `extra`。

## 13. 上游同步与补丁登记（D10）

- 新包 `internal/resilience` 以及新的 handler、管理接口文件，在 `tools/upstream-sync/policy.json` 中登记为 XY_OWNED（`backend/internal/**` 已覆盖，另在 custom_patches 中写明）。
- 原上游文件中的钩子各登记为具名补丁，每个补丁带绑定测试；钩子行本身登记在 `policy.json` 的 required_literals（以下文件清单以其为准）：
  - `xy-resilience-classify-hook`：`ratelimit_service.go`（失败分类与罚则入口）、`ops_upstream_context.go`（ops 记录与本请求失败账号标记）；
  - `xy-resilience-budget-hook`：`failover_loop.go`（`resilienceAttemptFailed`）、`gateway_forward.go`（服务内原地重试）；
  - `xy-resilience-session-hook`：`gateway_scheduling.go`（选号收尾：保留软罚绑定、末位兜底）、`gateway_service.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`；
  - `xy-resilience-keepalive-hook`：`gateway_upstream_response.go`、`openai_gateway_response_handling.go`（停滞检测基线）；
  - `xy-resilience-disconnect-hook`：`client_request_id.go`（送达跟踪）、`gateway_usage_billing.go`、`openai_gateway_usage.go`（未送达计费）；
  - `xy-resilience-errors-hook`：`api_key_auth.go`（同 Key 快速失败、失败回合封顶）、`image_task_handler.go`（异步图片准入前移）；
  - `xy-resilience-fallback-hook`：`routes/gateway.go`（兜底分组中间件）；
  - 续链整轮重放（§9.8）：`openai_gateway_messages.go`、`openai_messages_continuation.go`；
  - 生产报错修复（§18）：`openai_gateway_grok.go`（Grok 余额）；`gateway_forward.go` 与各 handler 入口（内容安全拒绝、Anthropic 流内状态）；`openai_gateway_forward.go`、`openai_gateway_passthrough.go`、`openai_gateway_response_handling.go`、`openai_gateway_handler.go`（web_search 重放）；`gateway_upstream_response.go`、OpenAI 各流处理与 WebSocket 转发文件（已输出后失败）；`ops_error_logger.go`（上传中断）。
- 上游缺陷修复同样登记为具名补丁：
  - `xy-index-nonce-no-304`：`internal/web/embed_on.go`。index.html 带本次请求的 CSP nonce；304 会让浏览器用新的 CSP 头配缓存正文里的旧 nonce，每次刷新都拦截内联配置脚本。改为始终返回完整页面、不带 ETag。sub2api main 有同样的问题。
  - `xy-placeholder-session-id`：`gateway_service.go`、`metadata_userid.go`。全 0 的 `metadata.session_id` 视为缺失（§18.5）。
  - `xy-allowlist-404-hint`：`server/middleware/group_model_allowlist.go`。白名单 404 列出本分组可用模型（§18.5）。
- `scheduler-diff.json` 为 `gateway_scheduling.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`gemini_messages_compat_service.go` 中新增的块登记白名单。
- `UPSTREAM_BASE.json` 的 custom_patches 与 policy 保持一致；`VERSION` 升为 0.4.0；`SUB2API_COMPAT_VERSION` 保持 0.2.15。
- 不向 Sub2API 官方提交。
- **CI 门禁：** `backend-ci.yml` 的 test 作业运行 `tools/gateway-resilience/ci_gate.py`（步骤名 “Gateway resilience”）。脚本收集 `internal/resilience` 全部测试文件，以及 `internal/service`、`internal/handler`、`internal/handler/admin`、`internal/server/middleware` 中韧性测试文件的全部 `Test*`（`060f6f7ea` 时 178 个），以 `-race -tags=unit` 运行；任一失败、跳过或未运行即失败，新增测试自动纳入。仓储层的真实 PostgreSQL/Redis 集成测试由 integration 作业运行。

## 14. 成功指标（D25）与度量方法

| 指标 | 现状 | 目标 | 度量方法 |
|---|---|---|---|
| CDN 切断率 | ESA 3.5%，CF 1.5% | < 0.3% | Caddy 状态为 0、时长约等于 90/125/300 秒、尺寸为 0 的 API 请求 ÷ API 请求（按入口） |
| 切断后仍计费 | 2 小时 43 次 | 0 | Caddy 切断记录与应用 200 + `usage_logs` 按“IP、路径、开始时间相差 < 2 秒”逐条匹配（复用分析脚本） |
| “无可用账号”503 / 48 小时 | 3,202 | < 300；不存在连续超过 5 分钟的整组无号 | `ops_error_logs` 中 `error_phase='routing'` 且状态为 503，按组和分钟聚合 |
| 换号后成功：P50 耗时 | 94.8 秒 | < 30 秒 | 应用日志中带换号事件、最终 200 的请求的延迟 |
| 同一会话同一账号连续失败（P99） | 9 | ≤ 2 | `resilience` 会话失败计数分布 |
| 因余额被永久置错 / 48 小时 | ≥ 50 | 0 | `account_disabled_auth_error` 中原因含余额的条数 |
| 有余额却看到“余额不足” / 48 小时 | 约 500 | 减少 90% | 余额为正时的 `INSUFFICIENT_BALANCE`/`billing_error` 条数 |
| 503 带 `Retry-After` | 0% | 100% | Caddy 响应头采样 |
| 一个失败的 Codex 回合产生的请求数 | 约 30 | ≤ 5 | 真实客户端端到端测试，以及生产同会话请求串分析 |
| 缓存读取占比与 TTFT | 基线 | 不低于基线 −0.5 个百分点；P50 不劣于 +3%，P95 不劣于 +5% | 份额调度报表与 `usage_logs` 首字分位 |

测试服务器上用故障注入与真实客户端达到这些目标后才提 PR。发布部署后在生产按同一口径复核（24 小时与 72 小时报告）。

## 15. 兼容性

- **客户端矩阵**（真实客户端端到端）：
  - 最新版 Codex（CLI 与 Desktop，HTTP SSE）；
  - 最新版 Claude Code；
  - OpenAI Python/Node SDK；
  - Anthropic SDK；
  - OpenCode（AI SDK）；
  - go-genai/python-genai；
  - curl；
  - XY-image 0.0.16 与 0.0.17。
- **XY-image 约束**（见 [`xy-image-appendix.md`](xy-image-appendix.md) 第 1 节）：
  - 确定性检查在提交前完成；
  - 只用注释行心跳；
  - 流内错误带 code 与 `error.status`；
  - 图片端点豁免超时；
  - 不新增与 “Key 不可用”集合冲突的 code；
  - 保留 `INSUFFICIENT_BALANCE` 加 403；
  - 快速失败返回同一应答；
  - 未送达不写用量行；
  - 不为 XY-image 开启非流式保活；
  - Caddy 不返回无标记的纯文本 5xx；
  - 所有应答带 `X-Client-Request-ID`。
- **版本回退：** 0.4.0 → 0.3.0 只需换回镜像。新迁移全部可空，0.3.0 忽略即可。`gateway_resilience_settings` 被旧版忽略。

## 16. 风险与对策

| 风险 | 对策 |
|---|---|
| 首输出阈值过紧，误杀慢但正常的长推理 | 按 effort 分档，并可按模型覆盖；最后一个候选不设单次超时；测试服务器用生产 TTFT 分布回放校准 |
| 提前提交后，状态码语义变为流内错误 | 只对经 CDN 的流式请求，并延迟 15 秒；确定性检查都在提交前；流内错误带 code 与 status |
| 软冷却过度（一次失败让所有会话都临时溢出） | 默认 30 秒；只有账号级的瞬时类别冷却，请求级降载不冷却；单号组由末位兜底保证可用 |
| 余额误判或中转已充值仍被暂停 | 识别规则见 §2；暂停固定 5 分钟，到期由真实请求重试；管理员可在账号列表立即恢复 |
| 兜底分组价格差异引发投诉 | 用户侧可见；默认不为任何分组配置，由管理员显式设置 |
| 未送达不计费被滥用 | 每 Key 每小时阈值后按上游成本计费；上游请求立即取消，限制成本 |
| 多实例下状态不一致 | 会话计数、回避集、稳定时间戳、兜底 Key 都放 Redis。进程内尽力而为的有：负缓存、快速失败、失败回合封顶、续链绑定，以及几个只用于省去 Redis 往返的本地索引（会话失败与回避标记、末位兜底使用记录、兜底转移记录）。多实例时，另一实例记下的回避不会被本实例读取，本实例的成功也不会清零另一实例的计数。生产为单实例；改为多实例前需把这些索引改为 Redis 读取或接受上述偏差 |
| 上游同步冲突 | 逻辑集中在自有包；钩子都登记在 policy 与 scheduler-diff 中 |
| 现有测试的期望值改变 | 逐条更新断言，并保留改动理由；新增针对每个 Scenario 的测试 |

## 17. 生态借鉴与许可

思路来源（只借鉴设计；LGPL、MIT 项目可以参考实现，但仍在 xy2api 内重写；无许可证或 AGPL 的项目只借鉴思路）：

- 余额状态机与探测：S2AM-GO、relay-tower、transit-hub、guardian（RC 实现，0.4.0 精简为固定暂停，§3.2）；
- 会话失败改绑：kiss-kedaya、sub2api-plus；
- 无号兜底分组：DeanZFC、sub2api_cloudflare；
- 请求预算：DeanZFC、Portkey、LiteLLM；
- 首包前心跳：上游 compact 心跳、DR-lin-eng；
- 协议终止帧：bejix、上游 Responses；
- 停滞检测修正：tickernelz；
- 断开处理：bejix、ranxi、kiss；
- 错误语义与防风暴：is7Qin、zhu961212、LiteLLM；
- 客户端行为：openai/codex、anthropic-sdk-typescript 源码。

每个来源的仓库、SHA、文件与行号，见 [`evidence/research-summary.md`](evidence/research-summary.md) 指向的完整调研报告（仓库外保存）。

## 18. 生产报错复盘与修复（2026-10-16）

生产 0.3.0 近 3 小时（04:53–07:53Z）推理请求 14,866 次、失败 1,649 次（11.1%）。根因、次数与证据见 [`evidence/production-errors-20261016.md`](evidence/production-errors-20261016.md)。18.1–18.4 受总开关约束，关闭时恢复 0.3.0 行为；18.5 中白名单 404 提示、上传中断记 499 与全 0 会话 ID 不受开关控制（缺陷修复），完整清单见 runbook §6。

### 18.1 Grok 余额 429 改走余额暂停（R1，761 次）

Grok 走专属错误处理 `handleGrokAccountUpstreamError`，不经过 `RateLimitService.HandleUpstreamError`，§2 分类与 §3.2 余额处理都没有接入。一个中转在 429 正文里写“insufficient balance”，同时给出请求窗口耗尽的重置头；网关据此限流约 28.6 分钟，单号分组整组 429。

- API Key 类、非池模式的 Grok 账号：正文按 §2 分类为 `BALANCE` 时按 §3.2 暂停 `balance.pause_seconds`，不再按重置头装限流；
- OAuth 账号（直连 xAI）、池模式账号与非余额错误保持 Grok 原有处理；
- 钩子：`openai_gateway_grok.go`（required_literals）。
- 遗留：Grok 进程内阻断（模型配额、团队限流）对 `resiliencePenaltyOf` 不可见，Explain 与 `Retry-After` 看不到它们（P2）。

### 18.2 已输出后的失败计入会话与账号（R2，238 次）

已输出后无法换号，但 0.3.0 的流内失败既不计入会话失败，也不罚账号：同一会话一直绑在高失败率账号上，失败五分之一流的账号仍承担全部新流量。处理见 §4.1、§7.6 与 `resilience_post_output.go`：

- 每次尝试只上报一次：会话按 `TRANSIENT` 计数（连续 2 次改绑）；
- 账号在进程内 60 秒窗口中计数，第 3 次施加软冷却 `transient:POST_OUTPUT`（时长同 `failure.transient_cooldown_seconds`）并清零；池模式不冷却；软冷却保留末位兜底；
- 客户端错误、内容安全与 cyber 拒绝、上下文超长和客户端取消不计。

### 18.3 API Key 中转的 web_search 历史回放（R3，65 次）

Codex 压缩上下文时回放 `web_search_call` 却声明 `tools:[]`，ChatGPT 后端返回 “response protection is unavailable”。0.3.0 的补声明修复只对 OAuth 账号生效；对所有中转都补声明也不安全（有的中转不认识托管工具类型会返回 400）。因此按账号学习：

- OpenAI API Key 账号在语义输出前因该文案失败，且输入含 `web_search_call`、未声明 web_search 时，用与 OAuth 相同的声明同号重放一次（计入请求预算）；
- 重放成功后进程内记住该账号 6 小时（最多 4096 个），之后同类请求发送前直接补声明；
- 重放仍失败（或预算不足、已补声明仍被拒）：不换号、不罚账号，会话计一次失败，客户端收到 502 与可读原因，并计入失败回合封顶（§9.7）；
- 未覆盖 WebSocket 路径（各有重连与失败处理）。

### 18.4 内容安全拒绝与 Anthropic 流内错误状态（R4 49 次、R7 14 次）

- **内容安全拒绝是请求的错误。** 内容安全 code（`request_check_blocked`、`content_policy_violation`、`content_filter`、`data_inspection_failed`，含顶层 code）与 400/403/451 上的保守文案（preflight safety、safety check、content policy、安全策略、内容安全、敏感内容/词/话题等）归为 `CLIENT_ERROR`（原因 `content_policy`）。文案提到账号、封禁、停用时不算。账号不计 403 strike、不冷却；所有换号循环在第一次遇到时结束（含池模式同号重试）；客户端收到 400 `content_policy_violation` 与脱敏后的上游原文（只关 `errors` 开关时回到 0.3.0 的 502 文案）。未覆盖 Responses WebSocket 循环（会换号，但账号已免罚）。
- **Anthropic 流内错误按类型给状态。** 0.3.0 对流内错误默认 403，handler 再映射为 502“访问被禁止”。现在 `rate_limit_error` → 429（未输出时按 `RATE_LIMIT` 换号并软冷却，耗尽返回 429 加 `Retry-After`），`overloaded_error` → 529（已输出也是），`api_error` → 502，其他保持 403。已输出后只改状态，不罚账号。

### 18.5 小项（R5、R6、R10b 与粘性）

- **Explain 指出模型被拒（R5，159 次）。** 给定模型时，Explain 返回 `model_restriction`：`not_allowed`（分组白名单）或 `channel_pricing`（渠道定价），管理界面在账号表上方提示。
- **白名单 404 列出可用模型（R6，248 次）。** HTTP 入口的 404 文案附本分组白名单的前 10 项；WebSocket 关闭原因受 123 字节限制，不变。具名补丁 `xy-allowlist-404-hint`。
- **上传中断按 499 记录（R10b，12 次）。** ops 中间件包装请求体，传输层读错误（非大小限制）造成的 400 记为 499，随 `IgnoreContextCanceled` 设置过滤。
- **全 0 会话 ID 视为缺失。** 一个 Key 的 `metadata.session_id` 全为 0，所有请求共用一个粘性键；现在改用可缓存内容的摘要。具名补丁 `xy-placeholder-session-id`。

### 18.6 需管理员处理

渠道定价与白名单一致；单号分组补号或配置无号兜底分组；余额 403 累计置错的账号充值后手动恢复；加大 Caddy 日志保留；轮换设置中含明文凭据的代理地址。

## 19. 生产就绪审查修复（2026-10-19，0.4.2）

两份审查（“0.3.0→0.4.1 更新代码与生产就绪审查”与“份额调度与网关韧性生产就绪审查”，原文在仓库外）逐条对照代码核实后的处理。正文各节已按 0.4.2 修订，此处汇总：

| 审查项 | 结论 | 处理（节） |
|---|---|---|
| R01 软罚的“更长”压过余额硬罚 | 属实 | 按罚档写入（§3.2、§3.4） |
| R02 解除冷却与并发硬罚的竞态 | 属实 | 条件清除（§3.4） |
| R03 开场事件算已送达 | 属实（tasks 已登记） | 送达写入器判读开场事件（§8.1） |
| R04 非流式不受预算截断 | 属实，按设计 | 文档写明范围（§6.1） |
| R05 快速失败跨路由重放 | 属实 | 按路由与分组区分（§9.3） |
| R06 失败窗口整体续期 | 属实 | 按账号的窗口（§4.1） |
| R07 唯一候选判断不看模型 | 属实 | 按本请求判断（§7.4） |
| R08 续链重放过宽、不计预算 | 属实（F8 已登记） | 收窄并计入预算（§9.8） |
| R09 末位兜底用旧快照派发 | 属实 | 派发前复核（§4.3） |
| R10 停滞未标记失败回合 | 属实 | 停滞标记失败回合（§7.5） |
| P1-C 静默上游不换号 | 属实（tasks 已登记） | 输出前停滞让给首输出守卫（§7.5） |
| 跨进程槽位误清理（继承） | 属实 | 进程租约（下文） |
| `/health` 恒 200 | 属实 | 新增 `/ready`（下文） |
| 未送达记录尽力而为 | 属实 | 有界写入并重试一次（§8.2，管理员文档 §4.4） |
| 负缓存窄范围污染（F2 跟进） | 属实 | 会话上限不写负缓存（§4.3） |
| OpenAI 按模型屏蔽期间改绑 | 属实（tasks 已登记） | 保留绑定（§4.2） |
| 韧性设置无版本校验 | 属实 | 暂不做：单管理员维护；管理员文档写明限制 |
| 高级 JSON 前端校验浅 | 属实 | 暂不做：后端严格校验仍在，异常只影响页面显示 |
| 输出 P99、缓存比估算 | 设计取舍 | 不改（F16 仍在后续） |
| 进程内状态不跨实例 | 设计范围 | 文档写明：支持蓝绿重叠，长期多活另行评估 |
| CDN 推断可由客户端触发 | 设计取舍（§7.1） | 不改；不得作为安全边界 |
| 部署实例：连接池、降级限流、备份、告警、真实 IP | 实例配置 | runbook §3.4（预上线核查与上线前配置） |

**并发槽位的进程租约。** 新进程启动时原先清掉所有不带本进程请求 ID 前缀的账号与用户槽位，以及全部等待计数（上游 Sub2API 的行为）。蓝绿切换时旧进程仍在服务，它的槽位被清掉后，账号与用户的并发可以超出上限，直到这些请求结束。0.4.2 起每个进程持有 Redis 租约 `concurrency:process_lease:<前缀>`（15 秒，每 5 秒续期）；启动清理保留仍有租约的进程的槽位，另有存活进程时不删共享的等待计数；周期清理（`slot_cleanup_interval`）在连续两次看不到被保留进程的租约后再清一次它的残留（一次续期失败，例如 Redis 抖动超过 15 秒，不会清掉存活进程），崩溃的进程约在租约加两个周期内（约 75 秒）被清理；运行期清理不删共享的等待计数（本进程的等待者也在里面，陈旧计数随 TTL 过期）；单进程启动时与原来一样立即清理。进程退出时不主动删除自己的租约：排空期间仍有在途请求，提前删除会让新进程清掉这些请求的槽位。0.4.1 及以前的进程不写租约，与已退出的进程无法区分，因此从这些版本首次蓝绿切换、以及回退到这些版本时，仍按旧行为清掉对方槽位（runbook §3.3 写明规避办法）。

**`/ready`。** `/health` 不访问任何依赖，只表示进程存活，仍用作容器健康检查；`/ready` 检查数据库（`PingContext`）与 Redis（`PING`），各 2 秒、并行，结果复用 1 秒，失败时 503 只列出失败项名称。供反向代理主动健康检查与外部监控使用。
