# 网关韧性：观测与排障

本文列出观察网关韧性运行情况的入口：管理后台面板、选号解释、管理接口、计数项与日志事件，并给出常见问题的排查步骤。适合负责网关稳定性的运维与管理员。能力本身的说明见 [网关韧性](gateway-resilience.md)。

## 管理后台

- **运维监控 → 网关韧性**：当前实例最近 5 分钟至 1 小时的计数（按指标、分组、账号与细分标签），以及未送达请求列表。计数在进程内存中，多实例部署时只反映所在实例。
- **选号解释**：在同一面板底部选择任一分组（含原版调度分组），可选填模型、会话哈希与请求类型，只读查看每个账号的档位（健康、软罚、硬罚）、原因、恢复时间、会话失败次数与是否被暂避，以及末位兜底账号与兜底分组的决策。
  - 填了模型时，若该模型不在分组白名单或渠道定价不含它（所有请求在选号前即失败），在账号表上方提示。
  - 选择“Responses 非流式”或“Chat 非流式”时，被学为仅流式的账号标为跳过（原因 `capability_stream_only`，恢复时间为学习到期时间）；不选则看整体健康。
  - 常见原因：`unavailable`（停用或不可调度）、`expired`、`quota_exceeded`、`balance_exhausted`（上游余额不足）、`temp_unschedulable`、`rate_limited`、`overloaded`、`model_rate_limited`、`model_transient`（非 OpenAI 账号的模型级冷却，只反映当前实例）、`capability_stream_only`。
  - 末位兜底账号的判断不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。OpenAI 的按模型瞬时屏蔽与质量状态不显示在选号解释中，OpenAI 分组的当前名次、冷却与被排除的原因看 [分组看板](group-quality-gate.md#分组看板)。
- **账号管理**：余额暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文，“恢复状态”立即放回；被学为仅流式的账号在状态列下方显示“仅流式（自动学习，剩余 X 小时）”，可点“清除”。
- **运维错误详情**：每次上游尝试带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`），按完整上游载荷计算，与换号决策一致。
- **使用记录**：由兜底分组服务的请求，管理端主标签显示原分组，下方提示“兜底 → 兜底组名”，悬停说明“按兜底分组计费”。

SLA 口径：选号阶段找不到可用账号的无号 503 计入 SLA；余额、用户并发、本地模型配置与上游内容安全拒绝标为业务限制，不计入 SLA。

## 管理接口

| 方法与路径 | 用途 |
|---|---|
| `GET /api/v1/admin/resilience/stats?window=15m` | 当前实例的计数，窗口为 Go 时长格式，最长 1 小时；响应含 `counters` 与派发时剩余预算直方图 `resilience_dispatch_remaining_seconds` |
| `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=` | 未送达请求列表，时间为 RFC3339 |
| `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=&protocol=&stream=` | 选号解释；`protocol`（`responses` 或 `chat`）与 `stream`（`true` 或 `false`）须成对出现，否则 400 |
| `DELETE /api/v1/admin/accounts/:id/capability-learning` | 清除账号的仅流式学习记录 |
| `GET` / `PUT /api/v1/admin/settings/gateway-resilience` | 读写设置，见 [设置参考](gateway-resilience-settings.md) |

- 账号列表与详情带 `capability_learned`：`[{protocol, mode: "stream_only", expires_at}]`，没有记录时为空数组。
- 选号解释的响应含 `enabled`、`healthy_count`、`accounts[]`（`account_id`、`priority`、`tier`、`reason`、`recover_at`、`session_failures`、`avoided`、`capability_learned`）、`last_resort`、`fallback_group`、`session`，以及填了模型时的 `model_restriction`。
- 统计、未送达与选号解释接口需要“运维”模块的查看权限；设置接口只对超级管理员开放（见 [管理员与权限](admin-permissions.md)）。

## 未送达请求

客户端在任何语义输出前断开的请求记入未送达表（保留 30 天，每 6 小时清理），列出时间、断开阶段、耗时、用户、Key、模型、入口、上游成本估算与是否计费。断开阶段：

| 阶段 | 含义 |
|---|---|
| `upload` | 上传请求体时断开，上游没有被调用，不计入滥用次数 |
| `select` | 选号或等待并发槽位时断开 |
| `header_wait` | 等待上游响应头时断开 |
| `pre_output` | 已收到响应头、首个输出前断开 |
| `streaming` | 输出中断开（只用于阶段统计） |

计数 `cdn_cut` 按耗时推断 CDN 切断：85–95 秒记 `90s`，120–130 秒记 `125s`，290–310 秒记 `300s`，其他为 `other`。某个桶持续偏高时，通常说明前面某层代理或 CDN 有对应的空闲超时，检查首包前保活是否生效、该请求是否被识别为经 CDN。未送达表的写入在后台进行，同时最多 16 个，满时丢弃并记 `resilience.undelivered_record_dropped`，计费不依赖这张表。

## 计数项

| 计数 | 含义 |
|---|---|
| `keepalive_committed` | 首包前心跳提交了响应头（细分为协议），也包括流内心跳、上游注释或槽位等待 ping 的首次提交 |
| `first_output_timeout` | 首输出截止触发换号（含计划内截止与 Gemini 尝试截止） |
| `budget_exhausted` | 请求预算耗尽 |
| `last_resort` | 末位兜底选中软罚账号 |
| `fallback_group` / `fallback_return` | 转投兜底分组 / 切回主分组 |
| `session_rebind` | 会话失败改绑 |
| `undelivered` / `cdn_cut` | 未送达；按耗时推断的 CDN 切断 |
| `fast_fail` | 同一 Key 重复 503 快速返回 |
| `turn_cap` | 会话连续耗尽失败到达上限，改为终止应答 |
| `abuse_guard` | 未送达滥用保护计费 |
| `stall` | 停滞断流 |
| `post_output_failure` | 已输出后的上游失败，细分为 `stream_failed`、`stream_read_error`、`missing_terminal`、`model_unavailable_after_output` |
| `relay_keepalive` | 从上游流中丢弃的中转伪保活帧（按账号） |
| `pre_created_delta` | 上游在 `response.created` 之前发出的非空 delta（照常输出，用于观察这类中转的规模） |
| `state_rejection` | 请求级的会话状态拒绝，按分组、账号与类别计数 |
| `collab_plaintext`、`collab_call`、`collab_call_action` | 协作消息明文化的尝试结果、工具调用与处理动作 |

请求预算的观测指标（与 `budget.budget_start`、`budget.budget_cut_attribution` 配合使用）：`budget_snapshot`（按截止锚点模式）、`budget_cut_fot`（`would`/`applied`，预算截断是否发生、是否被归因）、`ttfo_lower_bound`、`late_attempt_start`、`anthropic_response_stage_timeout`。`observe` 模式下 `applied` 恒为 false。

计数按分钟分桶、保留 1 小时；每分钟的键数有上限，超出后合并为不带分组与账号的 `overflow` 汇总行。

## 日志事件

结构化日志的消息固定为事件名，细节在字段中。日志不含正文、Key 或完整会话值。

### Warn（同时写入运维监控的系统日志）

| 事件 | 说明 |
|---|---|
| `resilience.balance_paused` | 账号余额暂停，含暂停时长、档位 `step` 与上游原文 |
| `resilience.session_rebind` | 会话改绑，`reason` 为 `resil_failure_streak`、`post_output_first_failure` 或 `terminal_rejection_streak`，含会话哈希前缀、分组、账号、类别、连击次数与暂避时长 |
| `resilience.last_resort`、`resilience.last_resort_avoided` | 末位兜底选中软罚账号；后者表示选中的是会话暂避集合中的账号。`candidates` 为本轮扫描的候选数，`penalty_reason` 为按原因计数的摘要 |
| `resilience.same_site_hold` | 同站断流暂停，含 `host_hash`（主机名摘要）、`account_id`、`peer_account_id`、`until` |
| `resilience.fallback_group`、`resilience.fallback_return` | 转投兜底分组（`reason` 为 `balance_exhausted` 或 `no_servable_account`，只在首次记住该 Key 时记录）与切回 |
| `resilience.budget_exhausted`、`resilience.capacity_switch_exhausted` | 请求预算或容量换号额度耗尽 |
| `resilience.stall`、`resilience.fast_fail`、`resilience.turn_cap`、`resilience.abuse_guard` | 对应能力触发 |
| `resilience.post_output_failure` | 已输出后的上游失败，含 `group_id`、`account_id`、`reason`、窗口内次数与是否施加冷却 |
| `resilience.state_rejection` | 请求级会话状态拒绝，含 `family`、`path`、`stage` 等，不记正文 |
| `resilience.collab_plaintext` | 协作消息明文化遇到冲突、异常计数或转换后被拒 |
| `resilience.web_search_history_rejected` | 补 web_search 声明重放后仍失败 |
| `resilience.wrapped_error_malformed` | 识别到形似中转包装帧但字段不一致的错误帧 |
| `openai_model_transient_state` | OpenAI API Key 账号 × 模型的普通瞬时失败或明确模型不可用，含 `failure_streak`、`cooldown_ms`、`reason` |
| `anthropic_account_latency_hold_started`、`anthropic_account_latency_sticky_migrated` | Claude 账号进入判慢期、会话迁出 |
| `sched_quality.view_stale` | OpenAI 选号读到的质量视图超过 60 秒没有更新（冷却暂不影响选号），最多每分钟一条，含 `view_at` |

以 `_failed` 结尾的事件（如 `resilience.balance_lift_failed`、`resilience.capability_learning_failed`、`resilience.last_resort_verify_failed`、`resilience.settings_refresh_failed`）表示存储或查询失败，功能降级为原有行为。

### Info（只写应用标准输出）

`resilience.keepalive_committed`（`source` 为 `request`、`slot_wait` 或 `queue_wait`）、`resilience.undelivered`（阶段、耗时、推断的 CDN 切断与 `edge`）、`resilience.transient_cooldown`、`resilience.transient_lifted`、`resilience.model_breaker`、`resilience.unclassified`、`resilience.content_policy_refusal`、`resilience.content_policy_no_failover`、`resilience.request_repair_replay`、`resilience.web_search_replay`、`resilience.web_search_declaration_learned`、`resilience.pre_created_delta`、`resilience.capability_learned`、`resilience.client_gone_skip`（客户端已离开时跳过的上游故障）、`anthropic_account_latency_sticky_kept`。

### 质量状态

OpenAI 账号 × 模型 × 分组的质量状态（见 [分组质量门槛](group-quality-gate.md)）在进入冷却、再冷却一轮、回到正常与被管理员恢复时各写一条 Info 日志 `sched_quality.state`（组件 `sched_quality`）。它虽是 Info 级，也写入运维监控的系统日志，可按组件筛选：

| 字段 | 含义 |
|---|---|
| `account_id`、`model`、`group_id` | 状态行；`model` 是调度模型名 |
| `from`、`to` | `normal` 或 `cooling` |
| `reason` | 冷却原因 `speed`、`error`、`cache`、`acute_timeout`、`test_speed`、`test_error`、`test_cache`；回到正常时为空或 `unverified` |
| `level` | 冷却级数（0–6） |
| `via` | 只在回到正常时出现：`pass`（导致冷却的维度合格）、`no_conclusion`（主动测试关闭或到点后 10 分钟仍没有结论，放行）、`gate_off`（分组没开门槛，到点放行）、`reset`（管理员恢复） |
| `speed_n`、`speed_p90_ms`、`speed_p90_timeout`、`error_n`、`error_rate`、`error_lcb`、`cache_n`、`cache_ratio` | 本行窗口里的三个数与各自的条数 |
| `speed_verdict`、`error_verdict`、`cache_verdict` | 各维的有效结论：`pass`、`marginal`、`fail`、`nodata` |
| `speed_from_test`、`error_from_test`、`cache_from_test` | 该维结论是否来自主动测试 |
| `test_speed_n`、`test_speed_slow`、`test_max_ttfo_ms`、`test_error_n`、`test_faults`、`test_cache_n`、`test_cache_ratio` | 这一轮主动测试合并后的轮数、超限轮数、最大续轮首字、故障轮数与缓存命中率 |

“合格恢复”与“到时放行”按 `via` 分开统计。评估本身失败记 Error 级 `sched_quality.cycle`（写入系统日志）。

### 请求关联字段

`post_output_failure`、`session_rebind`、`last_resort`、`turn_cap`、`keepalive_committed`、`stall` 等事件另带以下字段：

| 字段 | 含义 |
|---|---|
| `request_id` | 请求 ID，与运维错误记录、访问日志相同 |
| `gateway_request_id` | 网关为每个请求生成的本地 ID，客户端无法指定 |
| `model` | 入站请求的模型 |
| `delivered_bytes` | 已送达客户端的内容字节（开场与心跳不算） |
| `ms_since_first_output` | 距第一次写出内容的毫秒数，尚未输出时为 0 |
| `prelude_release_event` | 结束开场暂存、开始向客户端输出的那个上游事件类型 |
| `prelude_release_ms` | 上一项距 `response.created` 的毫秒数 |

## 排障

**单账号分组偶发 503**：在选号解释中查看该账号的档位与原因。软罚期间末位兜底会继续用它；若是硬罚（余额、鉴权、超过 300 秒的限流）则只能等恢复或配置无号兜底分组。

**客户端在长推理请求中被切断**：看 `cdn_cut` 的分桶与未送达列表的阶段。阶段为 `header_wait` 或 `pre_output` 且耗时集中在某个桶时，确认该请求被识别为经 CDN（日志 `resilience.keepalive_committed` 是否出现、`edge` 是否正确），必要时由反向代理注入 `X-XY-Edge`；也检查反向代理是否缓冲了 `text/event-stream`。

**换号太频繁、首输出超时偏多**：计数 `first_output_timeout` 偏高时，对照 `budget_cut_fot` 判断是否主要由剩余预算压短造成；需要时调大 `budget.first_output_timeout_seconds` 或针对慢模型设置 `budget.model_overrides`。

**某个账号余额已补足但仍不被选中**：账号列表中原因以 `balance:` 开头时，点“恢复状态”立即放回，并复位暂停档位。

**非流式请求总是 503 并提示改用 stream**：账号被学为仅流式。在账号列表清除学习记录，或让客户端改发流式请求。

**设置保存后其他实例行为没变**：其他实例每 10 秒刷新；若长时间未生效，查找 `resilience.settings_refresh_failed`。
