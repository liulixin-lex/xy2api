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
- 已送达的语义字节数、尝试耗时、上游给出的 `retry_after`（响应头或正文，例如中转商 Cloudflare 错误页里的 `retry_after: 60/120`）。

### 2.2 类别与动作

判定顺序：**先看文本与 code，再看状态码**，使余额类信号在 401、403、429 下也能被识别。

| 类别 | 主要判据 | 换号 | 同号重试 | 账号罚则 | 会话（D4） |
|---|---|---|---|---|---|
| `BALANCE` | 402；code 为 `insufficient_quota`、`insufficient_user_quota`、`pre_consume_token_quota_failed`、`credit_balance_exhausted`；文本含 insufficient (account) balance、余额不足、额度不足、credit balance、usage balance exhausted，以及 new-api 的 quota is not enough、额度已用尽、预扣费额度失败等（关键字集合可配置） | 是 | 否 | 进入余额状态机（§3.2，硬） | 立即改绑 |
| `AUTH` | 401、invalid/revoked key；结构化的账号停用 code（`deactivated_workspace` 等） | 是 | 否 | 硬：OpenAI 维持现有 3 次规则；其他平台改为计数加临时冷却（§3.3） | 立即改绑 |
| `RATE_LIMIT_RESET` | 429，且带明确的重置时间（头部或正文） | 是 | 否 | 软，冷却到重置时间；重置时间超过 300 秒视为硬 | 不超过 300 秒时只临时溢出；超过则改绑 |
| `RATE_LIMIT` | 429 无重置时间；流内 `rate_limit_exceeded`；“The service is busy” 等 | 是 | 否 | 软，冷却时长取设置 `rate_limit_429_cooldown_settings`（当前 45 秒） | 计入失败计数 |
| `CAPACITY_SHED` | 529、overloaded、`server_is_overloaded`、Codex at capacity（请求级降载） | 是，最多换 1 次，且换到不同 base_url 主机 | 是，每账号最多 1 次，0.5–2 秒带抖动 | 无（请求级） | 不计 |
| `TRANSIENT` | 5xx、524、504、连接错误、HTML 错误页 | 是 | 否（504/524 及耗时 ≥60 秒的失败一律不同号重试） | 软：冷却 `transient_cooldown_seconds`（默认 30 秒）；上游给出 `retry_after` 时取 min(retry_after, 120 秒) | 计入失败计数 |
| `FIRST_OUTPUT_TIMEOUT` | 本地首输出超时（§7.4） | 是 | 否 | 软，冷却 30 秒 | 计入失败计数 |
| `STALL` | 已输出后超过停滞阈值无数据（§7.5） | 否（已输出） | 否 | 软，冷却 30 秒 | 计入失败计数 |
| `CLIENT_CANCEL` | 客户端断开 | 否 | 否 | 无 | 不计 |
| `CLIENT_ERROR` | 400/413/422 的确定性参数错误、上下文超长、内容策略、cyber policy | 否（沿用现有“模型不存在可换号”等例外） | 否 | 无 | 不计 |
| `MODEL_UNSUPPORTED` | 账号级模型不存在（现有 `isOpenAICompatibleModelNotFound400`） | 是 | 否 | 账号加模型级瞬时阻断（沿用现有） | 不计 |

补充规则：

- **本站与上游余额分开。** `BALANCE` 只来自上游应答。本站计费产生的余额错误走 §9.5，不进入分类。
- **池模式账号保留管理员配置的同号重试状态码**，但同样受预算与“每账号最多 1 次”约束。
- 分类结果写入请求上下文，供换号循环、会话处理、ops 记录与 Explain 使用。

## 3. 账号罚则与余额恢复

### 3.1 硬、软分级（D5）

| 档 | 状态 | 选号行为 |
|---|---|---|
| 硬 | 管理员停用（`schedulable=false`）、`status=error`、余额 `EXHAUSTED`、凭据失效、重置时间超过 300 秒的限流、IQ 质量门、利润门、模型不支持、分组不匹配 | 永不选中 |
| 软 | 余额 `SUSPECT`、`TRANSIENT`/`FIRST_OUTPUT_TIMEOUT`/`STALL` 冷却、无重置时间的 429 冷却、不超过 300 秒的有重置时间 429、过载冷却（`overload_cooldown_settings`，当前 2 分钟）、模型级瞬时阻断（`openai_model_transient_state`）、会话回避（§4） | 平时不选；没有任何健康候选时可以作为末位兜底（§4.3） |

实现：在 `internal/resilience` 中提供 `PenaltyOf(account, model, now) (tier, recoverAt, reason)`，由两个调度器的候选构造钩子调用。现有冷却仍写在原有字段里：`rate_limit_reset_at`、`overload_until`、`temp_unschedulable_until`（`reason` 前缀区分 `balance:`、`transient:`、`auth:`）。快照与数据库口径都不变。

### 3.2 余额状态机（D6）

状态存在 `accounts.extra.resilience_balance`：`state`、`since`、`next_check_at`、`level`、`last_evidence`、`last_probe_at`、`probe_source`。排除通过 `temp_unschedulable_until` 加 `reason="balance:<state>"` 实现，不新增列，与 CN 供应商的做法一致。

| 状态 | 进入 | 行为 | 退出 |
|---|---|---|---|
| `OK` | 默认 | 正常调度 | — |
| `SUSPECT`（TTL 60–120 秒） | 弱信号：429 正文含余额字样，但同一账号 30 秒内有过成功 | 软罚，不接新会话；若全组只剩它，放行一个半开请求；触发一次单飞探测 | 探测显示有余额 → `OK`；探测确认无余额 → `EXHAUSTED`；TTL 到期 → `OK` |
| `EXHAUSTED` | 真实请求出现强信号（`BALANCE`）；或探测得到余额 ≤ 阈值 | 硬排除，不做同号重试；复检间隔 1、2、5、10 分钟，之后每 15 分钟，±15% 抖动；管理员“已充值”立即复检；超过 `notify_after_hours`（72 小时）仍未恢复时通知 | 探测得到正向证据 → `RECOVERING` |
| `RECOVERING` | 余额接口显示 > 阈值，或生成探测成功 | 并发上限临时压到 `recovering_concurrency`（1），持续 `recovering_window_seconds`（120 秒）或累计 `recovering_success_target`（3 次）成功 | 达标 → `OK`；再次出现余额错误 → `EXHAUSTED`，复检间隔升一级 |

探测按成本从低到高，由 `BalanceProbeService` 泛化现有的 `CNProviderBalanceCheckService` 实现：

1. **sub2api 系中转：** `GET {base_url}/v1/usage`，携带账号 Key，读取 `remaining`/`balance`，不消耗推理。实现参考 xy2api 自身的 `/v1/usage` 处理（`internal/handler/gateway_handler.go` 约 1590–1850）。
2. **new-api 系中转：** `/v1/dashboard/billing/subscription` 与 `/v1/dashboard/billing/usage`。**上线前必须用真实中转实测**（tasks 12.6），未通过时该类账号走第 3 步。
3. **兜底：** 流式请求，`max_tokens=1`，模型取该账号可用的最便宜模型，以收到首字为准。每个账号每天不超过 `generation_probe_daily_budget`（48 次）。
4. **不能作为恢复证据：** `/v1/models` 可达、凭据类探针成功。

多实例下用 Redis 锁 `xy:resil:balance_probe:{account_id}`（TTL 60 秒）保证单飞。探测返回 401/invalid 时改走 `AUTH` 路径。状态迁移写 `ops_system_logs`（warn 级，消息为 `resilience.balance_transition`），并通过现有告警与邮件通道通知管理员。

需要改为可恢复路径的现有分支：

- Anthropic 400 “credit balance”；
- 402 的默认分支；
- 非 OpenAI 平台的 403；
- 正文带余额字样的 429（在 `handle429` 之前先分类）；
- 图片余额 402（`openai_images_balance.go`）。

### 3.3 非 OpenAI 平台的 403 与凭据失效

把 `handleOpenAI403` 的“HTML/Cloudflare 拦截页豁免 + 180 分钟内累计 3 次 + 每次冷却 10 分钟”推广到所有 API Key 平台（Anthropic、Grok、Gemini、DeepSeek 等）。余额类 403 先走 §3.2。真正的凭据失效（401 invalid、revoked）维持现有置错逻辑。

### 3.4 瞬时失败冷却

`TRANSIENT`、`FIRST_OUTPUT_TIMEOUT`、`STALL` 写入软冷却：`temp_unschedulable_until = now + cooldown`，`reason="transient:<类别>"`。

- `cooldown` 默认 30 秒；上游正文或头部给出 `retry_after` 时，取 min(retry_after, `retry_after_cap_seconds`=120)。
- 冷却只是软罚：单号组仍可通过末位兜底使用该账号（§4.3）。

## 4. 选号与会话失败处理（D4、D5）

### 4.1 会话失败计数

Redis 键 `xy:resil:sf:{group_id}:{session_hash}` 为 Hash，字段为 `account_id`，值为失败次数，TTL `streak_window_seconds`（600 秒）。计数规则：

- 只对计入失败的类别（`RATE_LIMIT`、`TRANSIENT`、`FIRST_OUTPUT_TIMEOUT`、`STALL`）递增；
- 成功响应清零该会话对该账号的计数；
- 没有会话标识的请求不计数。

### 4.2 改绑规则

| 情况 | 动作 | 份额调度原因码 |
|---|---|---|
| 单次计入失败，账号进入软冷却 | **保留绑定**。下一请求遇到绑定账号仍在软冷却时，临时溢出到同层按权重的次选，不写新绑定；冷却结束后回到原账号 | `soft_spill`（份额）／`resil_soft_skip`（原版） |
| 同一会话在同一账号上，`streak_window` 内失败次数 ≥ `streak_threshold`（2） | 改绑：用 `deleteStickySessionAccountIDIfOwner` 做 CAS 删除（份额调度用 owner 校验删除），并把该账号写入会话回避集 `xy:resil:avoid:{group_id}:{session_hash}`（TTL `avoid_seconds`，600 秒）；下一请求在同层按权重重新放置 | `spill_rebind`，附 `resil_failure_streak` |
| 绑定账号进入硬状态（`BALANCE`、`AUTH`、管理员停用、重置时间超过 300 秒的限流） | 立即改绑 | `rebind_unavailable` |
| 并发满 | 沿用原有的等待与溢出，不改绑 | 原有 |
| TTFT 抖动 | 沿用现有 sticky escape 阈值，不加严 | 原有 |
| 客户端取消、客户端错误、本站策略拒绝 | 不动绑定，也不计数 | — |
| 带 `previous_response_id` 的续接请求 | 遵守现有所有权约束，不改绑 | 原有 |

**原版调度的语义调整**（钩子位于 `openai_account_scheduler.go` 的 `selectBySessionHash`，以及 `gateway_scheduling.go` 中对应的粘性路径）：绑定账号处于软罚时，**不再清除绑定**，只在本次请求跳过它（临时溢出）；硬状态仍按原样清除。

**份额调度**通过 `shareMissingBoundCandidate` 与候选构造钩子，把软罚账号表示为 `Eligible=false` 并带 `RecoverAt`，由现有的 `softSpill` 逻辑（`soft_spill_max_seconds`，缺省 300 秒）决定保留绑定、临时溢出。会话回避集中的账号对该会话标为 `Excluded`，原因 `resil_failure_streak`，改绑决策也记为 `resil_failure_streak`。余额耗尽（`balance:exhausted`）的账号原因为 `balance_exhausted`、`RecoverAt` 为零，走改绑；其他已有的临时不可调度原因保持 0.3.0 的处理，避免绑定释放过严（Q8）。末位兜底选中软罚账号时，在该组份额统计中记 `last_resort_soft`。兜底分组的转投与切回不是份额放置决策，计入韧性指标 `fallback_group`、`fallback_return`。

两种调度都**按优先级或层级、再按权重**选择替代账号：先在同层；只有整层不可用时才按份额调度现有的 `tier_overflow_unavailable` 规则降层。

### 4.3 末位兜底

当候选集合因为“只剩软罚账号”而为空时，按以下顺序选择：

1. **分组配置了无号兜底组**（§5）：先转投兜底组。
2. **没有兜底组，或兜底组也无健康候选：** 在原组（或兜底组）里，从**最高优先级（层）中有软罚账号的那一层**选出剩余罚期最短的账号。同层多个时按权重或原版负载选择。
3. 末位兜底请求不受单次首输出超时约束，只受总预算约束（§6）。

硬状态账号永不参与兜底。末位兜底计入原因码 `last_resort_soft`，并写 ops 统计。若这一步仍然没有候选，才返回“无可用账号”。

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
- **界面：** 分组编辑增加“无号兜底分组”选择；保存时检查环与平台。

## 6. 请求预算与重试（D18）

### 6.1 预算对象

`resilience.Budget` 挂在请求上下文中：

| 字段 | 含义 |
|---|---|
| `MaxAttempts` | 默认 4，包括首次尝试、换号与同号重试 |
| `PreOutputDeadline` | 文本请求为开始时间 + 240 秒；图片端点与异步图片任务不设 |
| `Attempts`、`SameAccountRetries[account]` | 已用次数 |
| `Canceled()` | 客户端或转发上下文取消时为真 |

接入点：

- OpenAI 的 Responses、Chat、Messages、Images 换号循环（`openai_gateway_handler.go`、`openai_chat_completions.go`、`openai_images.go`、`openai_gateway_messages` 相关 handler）；
- Anthropic 网关循环（`gateway_handler.go`、`gateway_handler_responses.go`、`gateway_handler_chat_completions.go`）；
- Gemini 兼容服务的原地重试（`gemini_*_compat_service.go`）；
- 透传传输重试。

每次尝试前调用 `Budget.Allow(kind)`，耗尽则结束：

- **未提交：** 返回现有的最后一次失败应答，按 §9 加 `Retry-After`；
- **已提交：** 按 §7.6 写终止帧。

### 6.2 同号重试

- 只用于 `CAPACITY_SHED`，以及池模式账号上管理员配置的状态码；每个账号最多 `same_account_retry_max` 次（1 次）。
- 账号级错误（`BALANCE`、`AUTH`、长时间的限流、504/524、耗时 ≥60 秒的失败）一律直接换号。
- Gemini 原地重试从 5 次降为 `gemini_same_account_retries`（1 次）。退避等待改为可取消的 `select`，替换 `time.Sleep`。
- 退避一律加 ±20% 抖动。尊重上游的 `Retry-After`，上限 `upstream_retry_after_cap_seconds`（60 秒），并且不超出剩余预算。

### 6.3 优先换号

“最大换号次数”由预算取代。现有 `max_account_switches` 仍作为上限保留，取两者较小值。`CAPACITY_SHED` 最多换 1 次，并且要求换到不同 base_url 主机；做不到时直接按无号处理。

## 7. 首包前保活、首输出超时、停滞与提交后的失败

### 7.1 边缘标记（D12）

- **Caddy：** 在 `gguuai.com`、`api.aiaimax.com` 站点中，先删除客户端自带的 `X-XY-Edge`；再按**对端 IP**（`remote_ip` 匹配器，不信任任何请求头）是否属于 Cloudflare 或 ESA 回源网段，注入 `X-XY-Edge: cf` 或 `esa`。XY-image 经内网直连、本机脚本等对端不在这些网段，因此不会被标记。配置见 runbook §3。
- **应用：** 只有对端是受信代理（`SERVER_TRUSTED_PROXIES`）时才读取该头；值不在 `edges` 白名单内的视为直连。

### 7.2 提交条件（D3、D12、D15、D21）

同时满足以下条件时，`resilience.ArmPreOutputKeepalive` 为请求设置延迟心跳：

1. 请求为流式（`stream=true`；Responses 和 Chat 以请求体为准，Anthropic Messages 以 `stream` 为准）；
2. 入站协议为 OpenAI Responses、OpenAI Chat Completions 或 Anthropic Messages；Gemini 原生入口与 genai UA **不做**；
3. 带有边缘标记（D12）；
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
- **最后一个可用候选**（没有其他健康候选、分组内只有这一个可用账号，或已是末位兜底）不设单次超时，只受 `PreOutputDeadline` 约束。
- 图片端点与异步图片任务不适用。

### 7.5 停滞判定（G2 修复）

- 把间隔检测改为以 `min(T/4, 5s)` 为周期检查 `lastRead`，使误差不超过 5 秒。位置：`openai_gateway_response_handling.go` 约 190、915；`gateway_upstream_response.go` 约 782、1120。
- 文本流已输出后的停滞阈值为 `text_stall_seconds`（120 秒），明显小于 Codex 的 300 秒。到点后按 §7.6 结束，并对账号施加 `STALL` 软冷却。
- 图片流维持 `image_stream_data_interval_timeout`（900 秒）；OAuth Responses 生图期间豁免文本阈值。

### 7.6 已提交后的失败（D17、G3）

| 入站协议 | 终止帧 |
|---|---|
| Responses | `event: response.failed`，`response.error.code` 为 `rate_limit_exceeded`（限流或忙）或 `server_error`（停滞、5xx、预算耗尽）；`message` 含 `Please try again in Ns.`（N = clamp(冷却剩余, 2, 30)）；保留 `sequence_number` |
| Chat Completions | `data: {"error":{"type":...,"code":...,"message":...,"status":<原 HTTP 语义码>}}`，然后 `data: [DONE]` |
| Anthropic Messages | `event: error\ndata: {"type":"error","error":{"type":"overloaded_error"或"api_error","message":...}}`（修复 G3：所有提交后的错误帧都补 `event: error`） |

写完终止帧后立即 `Flush` 并结束响应，因为 Codex 只在流结束时上报 `response.failed`。错误 JSON 只占一行，带稳定的 `code`，以及原 HTTP 语义状态 `error.status`（XY-image 与其他客户端可据此还原语义）。

### 7.7 与现有流内 keepalive 的关系

上游响应头到达后，仍由现有的 10 秒流内 keepalive 负责，口径不变。首包前心跳与它共享“已输出”扣除口径，换号判定以语义输出为准。G8（暂存账号头丢失）由 D21 规避：官方 OAuth 账号不提前提交。现有流内 keepalive 下也存在的 G8 影响列为 P2 观察项（tasks 12）。

### 7.8 中转站伪保活帧（测试服务器发现）

部分中转站会在 `response.created` 之前插入伪保活帧：

```text
event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","delta":"","SSE-Keep-Alive":true}
```

它不属于协议。原样转发会让严格 SDK 报错（openai-python：“Expected to have received response.created before response.output_text.delta”）。网关还会把它当成语义输出，从而提前放弃换号和首输出超时。因此读取上游 SSE 时直接丢弃这类帧：类型为 `response.output_text.delta`、`delta` 为空，且 `item_id` 为 `SSE-Keep-Alive` 或带 `SSE-Keep-Alive: true`。其他字节原样通过。总开关关闭时不过滤。

## 8. 客户端断开与计费（D2、D9、D20）

### 8.1 送达状态

| 状态 | 判定 |
|---|---|
| `undelivered` | 尚未写出任何语义字节（心跳与已提交的头不算；非流式则是响应体尚未写出） |
| `partial` | 已写出语义字节，但未完成 |
| `complete` | 正常完成 |

### 8.2 处理

- **`undelivered` 时断开：**
  - 立即取消上游上下文（包括等待响应头与换号间隙），停止预算内的后续尝试，释放在途预留与并发槽位；
  - **不写 `usage_logs`，不计费**；
  - 写一条 `gateway_undelivered_requests`（迁移 274）：`request_id`、`client_request_id`、`user_id`、`api_key_id`、`group_id`、`account_id`、`platform`、`model`、`inbound_endpoint`、`disconnect_stage`（选号、等待槽位、等待响应头、首输出前）、`duration_ms`、`upstream_usage_json`（上游已给出时）、`estimated_upstream_cost`、`billed`、`created_at`。
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

在进程内缓存“(分组, 平台, 模型) 无可用账号”的结论 `negative_cache_seconds`（2 秒），期间直接返回相同的 503 和剩余的 `Retry-After`，不重复查库与快照。账号状态变化事件（解冻、恢复、管理员开关）会主动失效对应条目。

### 9.3 同 Key 快速失败

- **触发：** 同一 API Key 在 `window_seconds`（30 秒）内收到 ≥ `threshold`（10）次**完全相同**的 503（状态、`error.type`、`message` 均相同）。
- **动作：** 之后窗口内的同类请求在选号与内容审计之前就直接返回**同一应答**（状态、正文、剩余的 `Retry-After` 均不变）。
- **豁免：** 异步任务轮询、capabilities、DELETE、`/v1/usage`、`/v1/models`、幂等重放。
- **存储：** 进程内滑动窗口，多实例时各自计数，属于尽力而为。
- 快速失败不计入账号失败，单独计数。

### 9.4 上游余额对用户隐藏（D13-1）

上游 `BALANCE` 类错误在换号耗尽或无号时，统一以 503 “Service temporarily unavailable”加 `Retry-After` 返回，包括图片余额 402（`Upstream image account has insufficient balance`）。管理员在运维页与告警中看到原文。

### 9.5 用户自身余额（D13-2、D13-3）

- **真实余额不足**（`api_key_auth.go` 的预检，以及 `billingErrorDetails` 中的余额分支）：统一为 403，`code: INSUFFICIENT_BALANCE`，文案为 `Insufficient balance on your xy2api account, please recharge`（保留 “insufficient balance” 字样，下游 sub2api 系站点仍能识别）。OpenAI 兼容端点使用 `{"error":{"type":"insufficient_balance","code":"INSUFFICIENT_BALANCE","message":...}}`，中间件路径保持原有的 `{code,message}` 形状。
- **在途预留超额**（`reserveInflight` 拒绝时余额 > 0）：改为 429，`error.type="rate_limit_error"`，`code="concurrent_balance_reserved"`，文案为 `Available balance is reserved by in-flight requests (available $X, reserved $Y); please retry shortly`，并带 `Retry-After`。
- **估算改进**（`billing_inflight_reservation.go` 的 `estimateOne`）：
  - 输入部分：对带会话标识的请求，按该会话最近一次的“缓存读 / 输入”比例折算，以缓存读价格计入（会话缓存比例存于 Redis，TTL 30 分钟）；无历史时维持原口径；
  - 输出部分：取 min(`max_tokens`, 该模型近 7 天 P99 输出 Token)，下限 1024；
  - 验收（tasks 4.6）：用生产 7 天数据回放，误拒减少 ≥90%，同时余额为负的金额不超过改前。

### 9.6 异步图片准入前移

在 `image_task_handler.go` 中，把“每实例与每 Key 的准入检查”放到 `checkSecurityAuditBeforeSubmit` 之前。被拒的请求不再消耗内容审计，也不读取完整请求体。审计未通过时释放准入。

### 9.7 失败回合封顶（测试服务器发现）

Codex 对带 `Retry-After` 的 5xx 没有次数上限：传输层每轮 5 次请求，回合层再重试 5 轮。测试服务器上，一个全失败分组的回合产生了 30 次请求、历时 871 秒。流内失败（已提交后的 `response.failed`）也会被回合层重试 5 次。

- **计数：** 按会话（API Key、分组、模型、粘性会话哈希）统计连续的“耗尽”失败，即无号 503、换号耗尽后的 429/5xx，以及已提交流里的同类失败。选号时记录会话；首次选号前的失败不计。
- **封顶：** 连续第 `errors.turn_cap.failures`（5）次失败改为终止应答：
  - 不带 `Retry-After`，加 `x-should-retry: false`（OpenAI、Anthropic SDK 与 Claude Code 据此停止重试）；
  - `error.code` 为 `server_is_overloaded`：Codex 只在服务端给出延迟时才重试它（HTTP 与流内都一样）；
  - 5xx 改为 503（Codex 只在 503 上识别这个 code）；429 保持 429，不带 `Retry-After` 的 429 Codex 直接终止。
- **重置：** 同一会话一次真正的成功（2xx 且未在流内失败）清零；两次失败间隔超过 `window_seconds`（120 秒）重新计数。
- 封顶只改变失败应答的形态，每次请求仍正常选号与尝试，分组恢复后立即成功。
- 进程内尽力而为。快速失败（§9.3）的原样返回发生在选号之前，不参与计数；封顶后的终止应答只属于一个会话，也不计入快速失败（否则重放时会补上 `Retry-After`，让 Codex 重新开始重试）。
- 指标 `turn_cap`，日志 `resilience.turn_cap`（warn）。

### 9.8 续链被拒时整轮重放（测试服务器发现）

Claude 客户端请求分发到 GPT 中转（Messages 转 Responses）时，网关按会话附带上一轮的 `previous_response_id`，只发送最后一轮输入。真实多账号中转站会把上一轮响应存在另一个上游账号上，再次请求时返回笼统的 400（如 “Upstream request failed”“The request could not be processed”），而不是可识别的“previous response not found”。0.3.0 只在可识别时才去掉续链重试，于是该会话从第二轮起每轮都是 400；按 §2 这类 400 属于客户端错误，不换号。

- 附带续链的请求收到 400/404/409/422，且不是完整重放同样会失败的错误（上下文超长、内容审核、图片、模型不存在）时，去掉续链按完整历史重放一次；
- 同时对该会话在该账号上关闭续链（与“上游不支持续链”相同，时长为原有粘性 TTL），后续轮次直接完整重放，不再先失败一次；
- 可识别的“续链不存在”仍按原逻辑只删除绑定。
- 续链绑定表（进程内）原先只在查询时删除过期项，不再回来的会话会一直留在内存里；现在每写入 1024 次清扫一遍过期项。会话失败与回避标记同样每分钟清扫（浸泡中发现内存缓慢上升）。
- 验证：单测 `TestForwardAsAnthropic_ContinuationRejectedReplaysAndStaysOff`；测试服务器场景 CONT-1（fault-upstream F25 模拟拒绝续链的中转）与真实中转复测。

## 10. 配置项（`gateway_resilience_settings`）

存于 `settings` 表（JSON）。管理接口为 `GET/PUT /api/v1/admin/settings/gateway-resilience`，前端为“网关韧性”设置卡。缺省值即设计值：

```json
{
  "enabled": true,
  "failure": { "transient_cooldown_seconds": 30, "retry_after_cap_seconds": 120,
               "balance_keywords_extra": [] },
  "session": { "enabled": true, "streak_threshold": 2, "streak_window_seconds": 600, "avoid_seconds": 600 },
  "last_resort": { "enabled": true },
  "balance": { "enabled": true, "recheck_schedule_seconds": [60, 120, 300, 600, 900], "jitter_ratio": 0.15,
               "suspect_ttl_seconds": 90, "recovering_concurrency": 1, "recovering_window_seconds": 120,
               "recovering_success_target": 3, "generation_probe_daily_budget": 48, "notify_after_hours": 72 },
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

## 11. 数据库迁移（只追加）

| 文件 | 内容 |
|---|---|
| `272_add_group_availability_fallback.sql` | `ALTER TABLE groups ADD COLUMN IF NOT EXISTS availability_fallback_group_id BIGINT NULL REFERENCES groups(id) ON DELETE SET NULL;` |
| `273_add_usage_logs_origin_group.sql` | `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS origin_group_id BIGINT NULL;`（不加外键与默认值，大表上只改元数据） |
| `274_create_gateway_undelivered_requests.sql` | 新表 `gateway_undelivered_requests`（字段见 §8.2）；索引 `(created_at)`、`(api_key_id, created_at)`、`(user_id, created_at)`；按 `ops_advanced_settings.data_retention` 清理，默认 30 天 |
| `275_add_ops_error_logs_disconnect_stage.sql` | `ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS disconnect_stage VARCHAR(32) NULL, ADD COLUMN IF NOT EXISTS delivered_bytes BIGINT NULL;` |
| `276_seed_balance_alert_rules.sql` | 预置告警规则“上游余额不足”（`account_balance_exhausted_count > 0`，P2）与“上游余额长期未恢复”（`account_balance_unrecovered_count > 0`，P1），`ON CONFLICT (name) DO NOTHING`；0.3.0 不认识这两个指标，评估时跳过 |

- `migrations/checksums.json` 只追加；已发布的 318 条不变。
- Ent schema（`groups`、`usage_logs`、`ops_error_logs` 与新表）同步更新，并重新生成 `ent/`。
- 回退到 0.3.0 时，旧版忽略新列与新表，不影响写入（全部可空，无新约束）。

## 12. 可观测性与管理界面

- **结构化日志**（管理员关注的为 warn 级，进入 `ops_system_logs`；量大的 `keepalive_committed`、`undelivered`、`transient_*` 为 info），消息模板固定，正文与上游原文放在 `extra`。失败分类不单独记日志，由 `resilience.transient_cooldown`、`resilience.balance_transition` 与 ops 记录的 `resilience_category` 承担：
  - `resilience.balance_transition`
  - `resilience.session_rebind`
  - `resilience.last_resort`
  - `resilience.fallback_group`
  - `resilience.budget_exhausted`
  - `resilience.keepalive_committed`
  - `resilience.stall`
  - `resilience.undelivered`
  - `resilience.fast_fail`
  - `resilience.turn_cap`
  - `resilience.abuse_guard`
- **分类结果**写入每次上游失败的 ops 记录（`upstream_errors[].resilience_category`）；只按状态码归类的上游 4xx（401、402 除外）以 info 级 `resilience.unclassified` 采样记录脱敏、截断后的文案（每进程每分钟约 10 条），用于补充关键字。
- **统计接口**：`GET /api/v1/admin/resilience/stats?window=`，按分组与账号汇总以下指标：
  - 首包前心跳提交数；
  - 首输出超时换号；
  - 预算耗尽；
  - 末位兜底；
  - 兜底分组；
  - 改绑（按原因）；
  - 未送达（按阶段）；
  - 快速失败；
  - 失败回合封顶；
  - 余额状态分布；
  - 推断的 CDN 切断（499 且阶段为首输出前，时长按 90、125、300 秒分桶）。
- **运维页**新增“网关韧性”面板；账号列表显示余额状态与下次复检时间，提供“已充值”按钮（`POST /api/v1/admin/accounts/:id/balance/recheck`）；未送达记录可查询（`GET /api/v1/admin/resilience/undelivered`）；面板底部为韧性 Explain（§4.4）。
- **降低日志噪声**：
  - `upstream_billing_rate_sync_applied` 从 info 降为 debug；
  - 余额批量探测不写审计日志；
  - `ops_system_logs.message` 只保留模板，原文进入 `extra`。

## 13. 上游同步与补丁登记（D10）

- 新包 `internal/resilience` 以及新的 handler、管理接口文件，在 `tools/upstream-sync/policy.json` 中登记为 XY_OWNED（`backend/internal/**` 已覆盖，另在 custom_patches 中写明）。
- 原上游文件中的钩子各登记为具名补丁，每个补丁带 required_literals 与绑定测试：
  - `xy-resilience-classify-hook`：`ratelimit_service.go`、`openai_gateway_upstream_errors.go`；
  - `xy-resilience-budget-hook`：各换号循环、`failover_loop.go`、Gemini 兼容服务；
  - `xy-resilience-session-hook`：`openai_account_scheduler.go`、`gateway_scheduling.go`、`gateway_service.go`、`share_scheduling_exec.go`；
  - `xy-resilience-keepalive-hook`：`openai_gateway_response_handling.go`、`gateway_upstream_response.go`、`gateway_handler.go`（G3）；
  - `xy-resilience-disconnect-hook`：流处理与计费提交点；
  - `xy-resilience-errors-hook`：`no_account_error.go`、`api_key_auth.go`、`gateway_inflight_reservation.go`、`image_task_handler.go`；
  - `xy-resilience-fallback-hook`：`gateway_scheduling.go`。
- `scheduler-diff.json` 为 `gateway_scheduling.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`gemini_messages_compat_service.go` 中新增的块登记白名单。
- `UPSTREAM_BASE.json` 的 custom_patches 与 policy 保持一致；`VERSION` 升为 0.4.0；`SUB2API_COMPAT_VERSION` 保持 0.2.15。
- 不向 Sub2API 官方提交。

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
| 余额探测被中转商限流，或误判 | `/v1/usage` 不消耗推理；生成探测有日预算；单飞锁；探测得到 401 时转凭据路径 |
| 兜底分组价格差异引发投诉 | 用户侧可见；默认不为任何分组配置，由管理员显式设置 |
| 未送达不计费被滥用 | 每 Key 每小时阈值后按上游成本计费；上游请求立即取消，限制成本 |
| 多实例下状态不一致 | 会话计数、回避集、余额锁、稳定时间戳都放 Redis；负缓存与快速失败为进程内尽力而为 |
| 上游同步冲突 | 逻辑集中在自有包；钩子都登记在 policy 与 scheduler-diff 中 |
| 现有测试的期望值改变 | 逐条更新断言，并保留改动理由；新增针对每个 Scenario 的测试 |

## 17. 生态借鉴与许可

思路来源（只借鉴设计；LGPL、MIT 项目可以参考实现，但仍在 xy2api 内重写；无许可证或 AGPL 的项目只借鉴思路）：

- 余额状态机与探测：S2AM-GO、relay-tower、transit-hub、guardian；
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
