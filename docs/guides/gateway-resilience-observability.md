# 网关韧性：观测与排障

本文列出观察网关韧性运行情况的入口：管理后台面板、选号解释、管理接口、计数项与日志事件，并给出常见问题的排查步骤。适合负责网关稳定性的运维与管理员。能力本身的说明见 [网关韧性](gateway-resilience.md)。

## 管理后台

- **运维监控 → 网关韧性**：当前实例最近 5 分钟至 1 小时的计数（按指标、分组、账号与细分标签），以及未送达请求列表。计数在进程内存中，多实例部署时只反映所在实例。
- **选号解释**：在同一面板底部选择任一分组（含原版调度分组），可选填模型、会话哈希与请求类型，只读查看每个账号的档位（健康、软罚、硬罚）、原因、恢复时间、会话失败次数与是否被暂避，以及末位兜底账号与兜底分组的决策。
  - 填了模型时，若该模型不在分组白名单或渠道定价不含它（所有请求在选号前即失败），在账号表上方提示。
  - 选择“Responses 非流式”或“Chat 非流式”时，被学为仅流式的账号标为跳过（原因 `capability_stream_only`，恢复时间为学习到期时间）；不选则看整体健康。
  - 常见原因：`unavailable`（停用或不可调度）、`expired`、`quota_exceeded`、`balance_exhausted`（上游余额不足）、`temp_unschedulable`、`rate_limited`、`overloaded`、`model_rate_limited`、`model_transient`（非 OpenAI 账号的模型级冷却，只反映当前实例）、`capability_stream_only`。
  - 末位兜底账号的判断不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。OpenAI 的按模型瞬时屏蔽与质量状态不显示在选号解释中，OpenAI 分组的当前名次与层看分组详情的“选号顺序”（见 [分组页](group-quality-gate.md#分组页)）。
- **账号管理**：余额暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文，“恢复状态”立即放回；被学为仅流式的账号在状态列下方显示“仅流式（自动学习，剩余 X 小时）”，有管理权限时“清除”按钮保持可见，文字过长可悬停看完整说明。
- **运维错误详情**：每次上游尝试带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`），按完整上游载荷计算，与换号决策一致。
- **使用记录**：由兜底分组服务的请求，管理端主标签显示原分组，下方提示“兜底 → 兜底组名”，悬停说明“按兜底分组计费”。

SLA 口径：选号阶段找不到可用账号的无号 503 计入 SLA；余额、用户并发、本地模型配置与上游内容安全拒绝标为业务限制，不计入 SLA。

## 管理接口

| 方法与路径 | 用途 |
|---|---|
| `GET /api/v1/admin/resilience/stats?window=15m` | 当前实例的计数，窗口为 Go 时长格式，最长 1 小时；响应含 `counters` 与派发时剩余预算直方图 `resilience_dispatch_remaining_seconds` |
| `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=` | 未送达请求列表，时间为 RFC3339 |
| `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=&protocol=&stream=` | 选号解释；`protocol`（`responses` 或 `chat`）与 `stream`（`true` 或 `false`）须成对出现，否则 400 |
| `DELETE /api/v1/admin/accounts/:id/capability-learning` | 清除账号的仅流式学习记录与能力不兼容标记 |
| `GET` / `PUT /api/v1/admin/settings/gateway-resilience` | 读写设置，见 [设置参考](gateway-resilience-settings.md) |

- 账号列表与详情带 `capability_learned`：`[{protocol, mode: "stream_only", expires_at}]`，没有记录时为空数组。
- 选号解释的响应含 `enabled`、`healthy_count`、`accounts[]`（`account_id`、`priority`、`tier`、`reason`、`recover_at`、`session_failures`、`avoided`、`capability_learned`）、`last_resort`、`fallback_group`、`session`，以及填了模型时的 `model_restriction`。
- 统计、未送达与选号解释接口需要“运维监控”模块的查看权限；设置接口只对超级管理员开放（见 [管理员与权限](admin-permissions.md)）。

## 未送达请求

客户端在任何语义输出前断开的请求记入未送达表（保留 30 天，每 6 小时清理），列出时间、断开阶段、耗时、用户、Key、模型、入口、上游成本估算与是否计费。断开阶段：

| 阶段 | 含义 |
|---|---|
| `upload` | 上传请求体时断开，上游没有被调用，不计入滥用次数 |
| `select` | 选号或等待并发槽位时断开 |
| `header_wait` | 等待上游响应头时断开 |
| `pre_output` | 已收到响应头、首个输出前断开 |
| `streaming` | 输出中断开（只用于阶段统计） |

上传超时且客户端仍可接收响应时返回 408，错误消息为 `Timed out reading request body`；与真正取消或断连的 499 分开。上传未完成的请求不调用上游。

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
| `resilience.state_rejection` | 请求级会话状态拒绝，含 `family`、`path`、`stage` 等，不记正文；HTTP 续接细分为 `continuation_owner_mismatch`、`http_continuation_unsupported`、`tool_output_missing_call_id`、`upstream_continuation_expired`，本地拒绝的 `stage=local`，尚未选中账号时不伪造账号字段 |
| `resilience.collab_plaintext` | 协作消息明文化遇到冲突、异常计数或转换后被拒 |
| `resilience.web_search_history_rejected` | 补 web_search 声明重放后仍失败 |
| `resilience.wrapped_error_malformed` | 识别到形似中转包装帧但字段不一致的错误帧 |
| `openai_model_transient_state` | OpenAI API Key 账号 × 模型的普通瞬时失败或明确模型不可用，含 `failure_streak`、`cooldown_ms`、`reason` |
| `anthropic_account_latency_hold_started`、`anthropic_account_latency_sticky_migrated` | Claude 账号进入判慢期、会话迁出 |
| `sched_quality.view_stale` | OpenAI 选号读到的质量视图超过 60 秒没有更新（这段时间质量后排与试用限制暂不影响选号），最多每分钟一条，含 `view_at` |

以 `_failed` 结尾的事件（如 `resilience.balance_lift_failed`、`resilience.capability_learning_failed`、`resilience.last_resort_verify_failed`、`resilience.settings_refresh_failed`）表示存储或查询失败，功能降级为原有行为。

### Info（只写应用标准输出）

`resilience.keepalive_committed`（`source` 为 `request`、`slot_wait` 或 `queue_wait`）、`resilience.undelivered`（阶段、耗时、推断的 CDN 切断与 `edge`）、`resilience.transient_cooldown`、`resilience.transient_lifted`、`resilience.model_breaker`、`resilience.unclassified`、`resilience.content_policy_refusal`、`resilience.content_policy_no_failover`、`resilience.request_repair_replay`、`resilience.web_search_replay`、`resilience.web_search_declaration_learned`、`resilience.pre_created_delta`、`resilience.capability_learned`、`resilience.client_gone_skip`（客户端已离开时跳过的上游故障）、`anthropic_account_latency_sticky_kept`。

### 质量状态

OpenAI 质量判定、换号与尝试收尾使用以下事件。事件字段不记录请求或响应正文、凭据与完整会话值。查询入口分三类：

- 带 `component=sched_quality` 的 Info 会落入运维监控的系统日志，可按组件筛选。
- Warn 及以上会落库，但以下日志不带 `component`，须按事件名找：`sched_quality.cycle` 的 Error、`sched_quality.view_stale`、`sched_quality.takeover_avoid_failed`、`sched_quality.takeover_commit_failed`、`sched_quality.binding_alias_failed`、`sched_quality.invalid_policy`、`sched_quality.invalid_reset_time`、`sched_quality.release_trial_group`，以及 `sched_quality.cleanup` 中判定记录清理失败的 Error。
- 不带 `component` 的 Info 只在应用标准输出里，包括 `sched_quality.capability_mark_evicted` 和 `sched_quality.cleanup` 中 `kind=decisions` 的成功清理记录；不能在系统日志中查到。

成功尝试的 `sched_quality.attempt_finished` 为 Debug，查看时需启用对应日志级别。

| 事件 | 级别 | 何时写 / 关键字段 |
|---|---|---|
| `sched_quality.loading` | 进度 / 完成 Info，加载失败 Warn | 带 `component=sched_quality`；`status` 为 `page`、`complete` 或 `incomplete`、累计 `rows`、`pages`；`page` 和 `incomplete` 另有 `layering=false`，失败有 `error`；加载完成前不分层 |
| `sched_quality.replay` | Debug | 处理积压的事务提交后；带组件，`horizon` 为处理水位，`batches`、`assessments` 为补跑批次与判定次数，需启用 Debug |
| `sched_quality.summary` | Info | 每实例每 5 分钟汇总，含 `cycles`、`cycles_failed`、`cycle_ms_max`、`cooled`、`released`、`trial`、`clear` |
| `sched_quality.state` | Info | 产生判定动作时；含 `at`、`rule_version`、`account_id`、`model`、`group_id`、`action`、`line`、`level`、`until`、`basis`、`attempt_ids` |
| `sched_quality.session_switch` | Info | 会话绑定事务结束，成功、失败、取消、冲突均可写；见下表 |
| `sched_quality.attempt_finished` | 成功 Debug，其余 Info | 尝试收尾；请求、尝试 ID、分类、状态来源、终态及实际输出字节，见下文 |
| `sched_quality.policy_changed` | Info | 保存分组门槛或全局首字规则；`operator_id`、`old`、`new`、`affected_lines`、`cleared`，分组门槛另有 `group_id` |
| `sched_quality.cleanup` | Info；失败 Error | 清理真正执行时；`kind=ledger` 为超过 7 天的尝试，`kind=decisions` 为超过 30 天的判定；含 `rows`，尝试清理另有 `duration_ms`，失败有 `error`；`ledger` 可按组件筛，`decisions` 成功仅标准输出，失败按事件名找 |
| `sched_quality.cycle` | Warn / Error | 引擎周期超过 5 秒时 Warn，含 `duration_ms`，每 5 分钟最多一次；周期失败为 Error，含 `error`，不带组件，按事件名找 |
| `sched_quality.view_stale` | Warn | 质量视图超过 60 秒未更新，暂停自动分层；含 `view_at`、`view_version`，最多每分钟一次 |
| `sched_quality.ttft_assessment` | Warn | 首字评估计算失败，保持未知；`account_id`、`model`、`group_id`、`error` |
| `sched_quality.delivery_capture_mismatch` | Warn | 请求已交付，但单次发送的质量采样为失败；`request_id`、`sends`、`sample_error_class`，不把这次交付误标为救回 |
| `sched_quality.invalid_policy` | Warn | 读取到无效分组门槛；`group_id`、`error` |
| `sched_quality.invalid_reset_time` | Warn | 账号重置时间无法读取；`account_id` |
| `sched_quality.binding_alias_failed` | Warn | 保存绑定别名失败；`account_id`、`error` |
| `sched_quality.takeover_avoid_failed` | Warn | 换号失败后写会话避让失败；`account_id`、`error` |
| `sched_quality.takeover_commit_failed` | Warn | 会话绑定提交存储失败；`account_id`、`error` |
| `sched_quality.trial_room_failed` | Warn | 试用名额查询失败，按满额降层；`account_id`、`error` |
| `sched_quality.trial_acquire_failed` | Warn | 试用名额获取失败，按满额继续选号；`error`，部分调用另带 `account_id` |
| `sched_quality.trial_touch_failed` | Warn | 试用名额续期失败；`error`，部分调用另带 `account_id` |
| `sched_quality.trial_promote_failed` | Warn | 已提交绑定后，请求名额转会话名额失败；`account_id`、`error`，随后尽力释放请求名额 |
| `sched_quality.trial_release_failed` | Warn | 单个名额释放失败；`account_id`、`group_id`、`error` |
| `sched_quality.release_trial_group` | Warn | 试用结束后整组名额释放失败；`account_id`、`model`、`group_id`、`error` |
| `sched_quality.capability_mark_evicted` | Info | 能力标记满额时替换最早到期的一条；`account_id`、`limit`；不带组件，仅应用标准输出 |

`summary` 的 `cycles` 是引擎每 5 秒的工作周期，正常每 5 分钟约 60 次；与每 5 分钟一次的普通质量判定不同。`cooled` 计进入、续期、试用失败；`released` 计到期达标、试用达标、试用超时；`trial` 计进入试用，`clear` 计档位清零。只统计提交成功的变化，管理员恢复不计入这些变化数。

`state` 的 `line` 是 `ttfo`、`error` 或 `cache`，一次动作对应一条线。动作包括 `enter`、`renew`、`release`、`trial`、`trial_pass`、`trial_fail`、`trial_timeout`、`reset`、`relax`、`clear`，含义见 [判定记录](group-quality-gate.md#判定记录)。`level` 为 0–4，对应 20、40、60、90、120 分钟。`until` 仅在进入 / 续期 / 试用失败时写后排期限，进入试用时写试用期限，其余为空。`basis` 包含门槛、证据来源和该线的证据；`attempt_ids` 最多 50 个。首字证据有时间折扣与同伴比较，不能只看 P50 推断判定。

**`sched_quality.session_switch`**：

| 字段 | 含义 |
|---|---|
| `gateway_request_id` | 触发绑定事务的网关本地请求 ID |
| `group_id`、`model` | 分组与调度模型名 |
| `from`、`to` | 原账号与目标账号 |
| `kind` | 如 `create`（新绑定）、`migrate`（迁移）、`takeover`（接管） |
| `reason` | 换号原因；首字和报错迁移分别为 `quality_migration_ttfo`、`quality_migration_error`；无单独原因时取 `kind` |
| `committed` | 是否实际提交绑定；`false` 不代表已经换号成功，需结合尝试结果及提交告警 |

**`sched_quality.attempt_finished`**：`request_id` 是网关本地 ID，`attempt_id` 标识本次发送；`outcome`、`class` 是采样结果与归责类别，`http_status`、`status_origin` 分开记录状态与来源。`client_disconnect`、`terminal_seen`、`terminal_event`、`terminal_failed`、`error_source`、`send_error`、`context_done` 用于区分终态、网络、取消与清理。`terminal_ms`、`finished_ms` 相对本次尝试开始；`first_output_request_ms` 是请求级端到端首输出；`output_bytes` 是实际写给客户端的字节，含已写前奏，不等同语义输出字节。

以下同前缀名称是管理审计动作，不是上述运行日志事件：`sched_quality.reset`、`sched_quality.override`、`sched_quality.clear_override`、`sched_quality.clear_auto_state`、`sched_quality.policy`、`sched_quality.ttft_rule`。恢复审计记录已清、保留及失败数量；配置保存的运行依据看 `sched_quality.policy_changed`。

### 采样写入丢弃

`sched_attempt.dropped` 为 Warn，组件 `service.sched_attempt_recorder`，可在系统日志按该组件或事件名查。`reason=stopped` 表示记录器停止后才到达的完成记录，计入 `dropped_write_failed`；`queue_full` 表示队列满导致丢样，计入 `dropped_queue_full`；`write_failed` 表示写入失败后丢弃，也计入 `dropped_write_failed`。只有 `reason=write_failed` 的日志带具体 `error`；`stopped` 和 `queue_full` 不带具体错误。日志按原因限频，不会为每条丢样各写一条。关停超期未结束的请求可能在这里留下丢弃记录。

### 请求关联字段

`post_output_failure`、`session_rebind`、`last_resort`、`turn_cap`、`keepalive_committed`、`stall` 等事件另带以下字段：

| 字段 | 含义 |
|---|---|
| `request_id` | 请求 ID，与运维错误记录、访问日志相同 |
| `gateway_request_id` | 网关为每个请求生成的本地 ID，客户端无法指定；与质量状态尝试记录（`account_attempt_samples`）的 `request_id` 相同 |
| `model` | 入站请求的模型 |
| `delivered_bytes` | 已送达客户端的内容字节（开场与心跳不算） |
| `ms_since_first_output` | 距第一次写出内容的毫秒数，尚未输出时为 0 |
| `prelude_release_event` | 结束开场暂存、开始向客户端输出的那个上游事件类型 |
| `prelude_release_ms` | 上一项距 `response.created` 的毫秒数 |

访问日志（组件 `http.access`）也带 `gateway_request_id`，取同一个本地 ID；客户端传来的同名请求头不会改变它，没有经过网关的请求（如管理接口）不带这个字段，原有的 `request_id` 不变。访问日志是否写入系统日志仍由运维监控“系统日志”里的“将访问日志写入数据库”决定（默认关）。

Responses 拒参修复（包括流内修复）在既有上游错误事件中记录 `rejected_field` 与 `resent`：字段只保存已识别的协议字段或结构路径，不保存参数值。允许修复、重建请求或消耗预算本身不代表重发；只有后续对应账号实际越过发送边界才将该事件的 `resent` 设为 true，未发送时保留 false。现有重试预算不变。上游拒绝 `max_output_tokens` 后删除该字段重发，会在请求级 `degradations` 中记录 `max_output_tokens/omit`；它表示输出上限已省略，重发事实以对应事件为准。

转换中省略、映射或补入默认值的参数记录为请求级 `degradations`：每项为字段路径和动作，`omit` 表示省略，`map` 表示改成目标能表达的值，`default` 表示补入默认值。使用记录、未计费请求记录与错误记录保留同一份降级信息，不含参数值，不添加响应头。

通用 Chat/Responses、Images、Responses 生图和 Embeddings 均在准入后关联 `gateway_request_id`；`request_scope=text/image/embedding` 独立于文本统计资格，图片及 embedding 不进入文本成功率分母。

用量与无账单请求的 `usage_observed/usage_source`、`terminal_event_seen/terminal_event_valid`、`response_body_type` 分别对应运行时的用量存在性、来源、原始终态证据和 JSON/SSE 正文类型；未观测的事实为 `NULL`。具体语义见[用量与完成证据](usage.md#用量与完成证据)。

## 排障

**单账号分组偶发 503**：在选号解释中查看该账号的档位与原因。软罚期间末位兜底会继续用它；若是硬罚（余额、鉴权、超过 300 秒的限流）则只能等恢复或配置无号兜底分组。

**客户端在长推理请求中被切断**：看 `cdn_cut` 的分桶与未送达列表的阶段。阶段为 `header_wait` 或 `pre_output` 且耗时集中在某个桶时，确认该请求被识别为经 CDN（日志 `resilience.keepalive_committed` 是否出现、`edge` 是否正确），必要时由反向代理注入 `X-XY-Edge`；也检查反向代理是否缓冲了 `text/event-stream`。

**换号太频繁、首输出超时偏多**：计数 `first_output_timeout` 偏高时，对照 `budget_cut_fot` 判断是否主要由剩余预算压短造成；需要时调大 `budget.first_output_timeout_seconds` 或针对慢模型设置 `budget.model_overrides`。

**某个账号余额已补足但仍不被选中**：账号列表中原因以 `balance:` 开头时，点“恢复状态”立即放回，并复位暂停档位。

**非流式请求总是 503 并提示改用 stream**：账号被学为仅流式。在账号列表清除学习记录，或让客户端改发流式请求。

**OpenAI 账号的后排、试用、恢复或会话搬动看不明白**：先按组件 `sched_quality` 筛选系统日志；不带组件的告警另按事件名找，仅标准输出的 Info 到应用日志里查，清单见 [质量状态](#质量状态)。

1. 重启后先看 `sched_quality.loading`：`page` 表示仍在分页加载，`incomplete` 带错误，加载完成前不分层；`complete` 表示证据加载完成。再看 `sched_quality.summary` 是否每 5 分钟按时出现、周期是否变慢：每个实例约 60 个周期；`cycles_failed` 大于 0、`cycle_ms_max` 变大或出现 `sched_quality.cycle` 的 Warn，说明引擎周期变慢或失败。`cycle` 的 Error 不带组件，须按事件名找，结合错误字段排查。
2. 再按账号看 `sched_quality.state`：何时进入后排、试用或清零，哪条线（`line`）、依据来自哪里（`basis`），以及到期复核结果。
3. 会话搬动看 `sched_quality.session_switch`，先确认 `committed`：用其中的 `gateway_request_id` 对上访问日志的同名字段与尝试记录的 `request_id`，找到触发绑定事务的请求；再用 `sched_quality.attempt_finished` 的 `request_id` / `attempt_id` 区分首输出截止、真实上游失败、取消和修复重发（访问日志要开启“将访问日志写入数据库”才在系统日志里）。

**设置保存后其他实例行为没变**：其他实例每 10 秒刷新；若长时间未生效，查找 `resilience.settings_refresh_failed`。
