# 网关韧性：观测与排障

本文列出观察网关韧性运行情况的入口：管理后台面板、选号解释、管理接口、计数项与日志事件，并给出常见问题的排查步骤。适合负责网关稳定性的运维与管理员。能力本身的说明见 [网关韧性](gateway-resilience.md)。

## 管理后台

- **运维看板**。全局或按平台查看时，自动或预聚合模式使用的整点小时汇总不含无分组 Key 请求的末次首字和端到端首输出。只有开启 `allow_ungrouped_key_scheduling` 才会有这类请求，原始查询会计入符合筛选条件的这类请求。
- **运维监控 → 网关韧性**：当前实例最近 5 分钟至 1 小时的计数（按指标、分组、账号与细分标签），以及未送达请求列表。计数在进程内存中，多实例部署时只反映所在实例。
- **选号解释**：在同一面板底部选择任一分组（含原版调度分组），可选填模型、会话哈希与请求类型，只读查看每个账号的档位（健康、软罚、硬罚）、原因、恢复时间、会话失败次数与是否被暂避，以及末位兜底账号与兜底分组的决策。
  - 填了模型时，若该模型不在分组白名单或渠道定价不含它（所有请求在选号前即失败），在账号表上方提示。
  - 选择“Responses 非流式”或“Chat 非流式”时，被学为仅流式的账号标为跳过（原因 `capability_stream_only`，恢复时间为学习到期时间）；不选则看整体健康。
  - 常见原因：`unavailable`（停用或不可调度）、`expired`、`quota_exceeded`、`balance_exhausted`（上游余额不足）、`temp_unschedulable`、`rate_limited`、`overloaded`、`model_rate_limited`、`model_transient`（非 OpenAI 账号的模型级冷却，只反映当前实例）、`capability_stream_only`。
  - 末位兜底账号的判断不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。OpenAI 的按模型瞬时屏蔽与质量状态不显示在选号解释中，OpenAI 分组的当前名次与层看分组详情的“选号顺序”（见 [分组页](group-quality-gate.md#分组页)）。
- **账号管理**：余额暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文，“恢复状态”立即放回；被学为仅流式的账号在状态列下方显示“仅流式（自动学习，剩余 X 小时）”，有管理权限时“清除”按钮保持可见，文字过长可悬停看完整说明。
- **运维错误详情**：每次上游尝试带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`），按完整上游载荷计算，与换号决策一致。
- **使用记录**：由兜底分组服务的请求，管理端主标签显示原分组，下方提示“兜底 → 兜底组名”，悬停说明按原始分组和实际服务分组判定、按兜底分组计费。账单的 `fallback` 标志另有含义，见 [两条日志与 fallback 的含义](#两条日志与-fallback-的含义)。

SLA 口径：选号阶段找不到可用账号的无号 503 计入 SLA；余额、用户并发、本地模型配置与上游内容安全拒绝标为业务限制，不计入 SLA。

## 运维错误记录的状态码

迁移 316 为 `ops_error_logs` 增加两个可空列，与质量尝试表或采样日志里的同名字段分开看：

| 列 | 含义与取值 |
|---|---|
| `status_origin` | `upstream_status_code` 的来源：`1` 为上游返回，`2` 为网关合成；历史记录、来源未确认或没有上游状态时为 `NULL`。不能把空值当作真实上游来源 |
| `client_status` | 已实际提交给客户端的 HTTP 状态码；尚未写出响应时为 `NULL`，不把框架缺省的 200 当成已发送 |

原有 `status_code` 保留请求记录与内部归类口径；`upstream_status_code` 保留上游状态或网关投影码，来源由 `status_origin` 区分。流式失败时，客户端已提交的状态码不再覆盖上游码，而是分别记录。例如响应头已发出 200、之后流内失败，`client_status` 仍可能是 200，内部与上游错误状态另存。

**当前查看入口**：这两列只落库，运维面板和错误接口都不返回。运维监控的错误列表与错误详情仍显示原有的“状态码 / 上游状态码”；接口的 `status_code` 优先取 `upstream_status_code`，缺失时取原记录的 `status_code`，不能把面板状态当成新的 `client_status`。

需要区分时，用只读数据库连接查询，不改记录：

```sql
BEGIN READ ONLY;
SELECT created_at, status_code, upstream_status_code,
       status_origin, client_status
FROM ops_error_logs
ORDER BY created_at DESC, id DESC
LIMIT 20;
COMMIT;
```

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

## 原始计量与采购成本

使用记录的客户 token 仍是补偿及 TTL 政策后的结算值。管理员接口的 `supplier_cost_estimate` 是最终已观测尝试按原始计量计算的采购估计，已含账号倍率；NULL 为未知或历史行，0 为确认免费。`metering_adjustment` 记录原始 token、presence/source、各调整类型、补偿前客户金额及净调整额；成本未知时也写入 `supplier_cost_source=unknown`，以区别漏写。无调整且成本已知的普通行省略 JSON。

看板、账号统计与新版管理员导出优先读取新采购金额，缺失才按旧 `account_stats_cost/total_cost × account_rate_multiplier` 回退；历史不回填。余额、配额与未送达 abuse 扣费继续使用原结算公式。采购估计不含未观测失败尝试，不能据此断言全链真实毛利，字段和导出版本详见 [用量说明](usage.md#计量结算与采购成本)。

## 未送达请求

管理员接口返回可空的 `gateway_request_id`，与用量、未计费和错误记录的网关请求键相同，上传阶段断开也保留；原 `request_id` 与 `client_request_id` 不变，历史行保持空值，不按客户端编号推测回填。

Gemini 入口按实际方法与动作分别记录模型列表 `/v1beta/models`、模型详情 `/v1beta/models/{model}`、生成 `/v1beta/models:generateContent`、流式生成 `/v1beta/models:streamGenerateContent` 和令牌计数 `/v1beta/models:countTokens`；标签不含模型名，未知动作不归生成，历史 `/v1beta/models` 仍是旧版混合桶，统计时应保留该桶。

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
| `session_rebind` | 请求提出的会话避让（后台叫“会话避让（请求提出）”），不代表归属已换，见 [两条日志与 fallback 的含义](#两条日志与-fallback-的含义) |
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

## 两条日志与 fallback 的含义

下面几处名字容易读成“换了号”或“换了分组”，实际含义如下。

| 看到的 | 含义 | 要确认什么，看哪里 |
|---|---|---|
| 日志 `resilience.session_rebind`、计数 `session_rebind` | 这次请求提出离开会话当前的号（避让的意图），在提交绑定之前记下 | 归属是否已换，以提交记录为准：看 `sched_quality.session_switch` 的 `committed`。`committed=false` 表示没有换成 |
| 使用记录的 `fallback=true` | 本次由后排号承接（OpenAI 质量选号落到后排层时也会置上），不表示换了分组 | 门槛组里带这个标志的请求很多，实际跨分组的可以是 0 |
| 跨没跨分组 | 只看原始分组和实际服务分组：管理端使用记录的 `origin_group_id` 有值且不等于 `group_id`，才是由兜底分组承接（界面上的“兜底 → 组名”） | 日志 `resilience.fallback_group`、`resilience.fallback_return` |
| 无号可用时的末位候选 | 不进账单，账单里没有对应的列 | 看计数 `last_resort` 和日志 `resilience.last_resort`。选中会话避让的账号时另记 `resilience.last_resort_avoided` |

## 日志事件

结构化日志的消息固定为事件名，细节在字段中。日志不含正文、Key 或完整会话值。

### Warn（同时写入运维监控的系统日志）

| 事件 | 说明 |
|---|---|
| `resilience.balance_paused` | 账号余额暂停，含暂停时长、档位 `step` 与上游原文 |
| `resilience.session_rebind` | 请求提出的会话避让（离开原号的意图，不代表归属已换；是否已换看 `sched_quality.session_switch` 的 `committed`），`reason` 为 `resil_failure_streak`、`post_output_first_failure` 或 `terminal_rejection_streak`，含会话哈希前缀、分组、账号、类别、连击次数与暂避时长 |
| `resilience.last_resort`、`resilience.last_resort_avoided` | 末位兜底选中软罚账号；后者表示选中了本会话避让的账号。OpenAI 的 `avoid_source` 为 `session`（普通会话避让）、`first_output`（首输出避让）或 `both`（同时命中）。`candidates` 为本轮扫描的候选数，`penalty_reason` 为按原因计数的摘要 |
| `resilience.same_site_hold` | 同站断流暂停，含 `host_hash`（主机名摘要）、`account_id`、`peer_account_id`、`until` |
| `resilience.fallback_group`、`resilience.fallback_return` | 转投兜底分组（`reason` 为 `balance_exhausted` 或 `no_servable_account`，只在首次记住该 Key 时记录）与切回 |
| `resilience.budget_exhausted`、`resilience.capacity_switch_exhausted` | 请求预算或容量换号额度耗尽 |
| `resilience.stall`、`resilience.fast_fail`、`resilience.turn_cap`、`resilience.abuse_guard` | 对应能力触发 |
| `resilience.post_output_failure` | 已输出后的上游失败，含 `group_id`、`account_id`、`reason`、窗口内次数与是否施加冷却 |
| `resilience.state_rejection` | 请求级会话状态拒绝，含 `family`、`path`、`stage` 等，不记正文；HTTP 续接细分为 `continuation_owner_mismatch`、`http_continuation_unsupported`、`tool_output_missing_call_id`、`upstream_continuation_expired`，本地拒绝的 `stage=local`，尚未选中账号时不伪造账号字段 |
| `resilience.fco_shape` | Responses 出站独立 `function_call_output` 的结构摘要；只记录数量和有限枚举（层、名称桶、call_id/output 形态、previous_response_id、出站重复键），不记录正文、ID 或 URL。 |
| `resilience.fco_compat` | 独立 `function_call_output` 的改写与重试：`layer`（`core`、`ext`、`admin`）、`action`（`map`）、`outcome`（`sent` 已发出改写后的请求、`completed` 改写后的请求正常完成、`normalize_error` 改写失败按原样发送）；不记录正文、ID 或条目名原值。指标 `fco_compat` 只有一个标签，改写记为 `层:map:结果`（如 `core:map:sent`），形状观测记为 `refusal_<文案变体>` 和 `success_sample`。 |
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

OpenAI 质量判定、选号、换号与尝试收尾使用以下事件。事件字段不记录请求或响应正文、凭据与完整会话值。查询入口分三类：

- 带 `component=sched_quality` 的 Info 会落入运维监控的系统日志，可按组件筛选；协议转换的 `openai_conversion_retry` 也带这个组件。其余普通 Info 不进库。
- Warn 及以上会落库。`sched_quality.cycle` 的 Error、`sched_quality.view_stale`、`sched_quality.takeover_avoid_failed`、`sched_quality.takeover_commit_failed`、`sched_quality.binding_alias_failed`、`sched_quality.invalid_policy`、`sched_quality.invalid_reset_time`、`sched_quality.rear_entries_read_failed`、`sched_quality.trial_full_record_failed`、`sched_quality.release_trial_group`，以及 `sched_quality.cleanup` 中判定记录清理失败的 Error 均按事件名查找。这些事件不带 `component`。
- 不带 `component` 的 Info 只在应用标准输出里，包括 `resilience.budget_timeout_plan`、`sched_quality.capability_mark_evicted` 和 `sched_quality.cleanup` 中 `kind=decisions` 的成功清理记录。

成功尝试的 `sched_quality.attempt_finished` 为 Debug，查看时需启用对应日志级别。

| 事件 | 级别 | 何时写 / 关键字段 |
|---|---|---|
| `sched_quality.loading` | 进度 / 完成 Info，加载失败 Warn | 带 `component=sched_quality`；`status` 为 `page`、`complete` 或 `incomplete`、累计 `rows`、`pages`；`page` 和 `incomplete` 另有 `layering=false`，失败有 `error`；加载完成前不分层 |
| `sched_quality.replay` | Debug | 处理积压的事务提交后；带组件，`horizon` 为处理水位，`batches`、`assessments` 为补跑批次与判定次数，需启用 Debug |
| `sched_quality.summary` | Info | 带 `component=sched_quality`；每实例每 5 分钟汇总，含 `cycles`、`cycles_failed`、`cycle_ms_max`、`cooled`、`released`、`trial`、`relax`、`clear` |
| `sched_quality.state` | Info | 带 `component=sched_quality`；判定事务提交后每动作一条，含保存门槛引发的重评。字段见下文 |
| `sched_quality.history_missing` | Info | 带 `component=sched_quality`；事务提交后按线汇总缺历史的评估。含 `account_id`、`model`、`group_id`、`line`、`first_at`、`last_at`（首末评估时刻）、`assessments`（次数） |
| `sched_quality.missing_deadline` | Warn | 带 `component=sched_quality`；后排或试用记录缺期限，按发现时到期处理并提交动作后记录。含 `at`、`account_id`、`model`、`group_id`、`line` |
| `sched_quality.performance_ready` | Info | 带 `component=sched_quality`，进系统日志库。长期表现的后台任务每次启动后，第一次完整装载发布时记一次，部分装载不记；通常就是每次进程启动一次，但同一进程里引擎停止后再启动也会再记一次。事件不带进程或启动标识，核对“每次启动都有”要对照启动时刻；`snapshot_at`、`from` 为统计窗口终点和起点，`rows` 为装入的尝试样本数，`load_ms` 为首次加载到发布的毫秒数（含重试等待） |
| `sched_quality.performance_load` | Warn | 不带组件，按事件名查；长期表现加载或配置读取失败，含 `error` |
| `sched_quality.first_output_avoid` | Info | 原归属号等够首输出时限后记录避让结果。含 `gateway_request_id`、`group_id`、`model`、`account_id`、`owner_gen`、`expires_at_ms`、`reason`、`confirmed`，存储错误或超时另有 `error_class` |
| `sched_quality.session_switch` | Info | 带 `component=sched_quality`；会话绑定事务结束，或放弃会话变更，见下表 |
| `sched_quality.new_session` | Info | 带 `component=sched_quality`；新会话首次普通选号返回时记录去向或无号原因，字段见下文 |
| `resilience.budget_timeout_plan` | Info | 首输出时限计划。含 `gateway_request_id`、`attempt_seq`、`group_id`、`account_id`、`model` 与时限诊断字段 |
| `openai.sticky_bypass` | `reason=first_output_avoid` 时为 Info | `reason=first_output_avoid` 表示仅因首输出避让绕开仍合格的归属号，另选账号临时服务。含 `gateway_request_id`、`group_id`、`owner`、`selected`。此原因带 `component=sched_quality`，可在系统日志查询。会改绑定的原因和 `unrecorded` 记为 Warn |
| `sched_quality.attempt_finished` | 成功 Debug，其余 Info | 尝试收尾；请求、尝试 ID、分类、状态来源、终态及实际输出字节，见下文 |
| `sched_quality.policy_changed` | Info | 保存分组门槛或全局首字规则；`operator_id`、`old`、`new`、`affected_lines`、`cleared`，分组门槛另有 `group_id` |
| `sched_quality.cleanup` | Info；失败 Error | 清理真正执行时；`kind=ledger` 为超过 7 天的尝试，`kind=decisions` 为超过 30 天的判定；含 `rows`，尝试清理另有 `duration_ms`，失败有 `error`；`ledger` 可按组件筛，`decisions` 成功仅标准输出，失败按事件名找 |
| `sched_quality.cycle` | Warn / Error | 引擎周期超过 5 秒时 Warn，带 `component=sched_quality`，含 `duration_ms`，每 5 分钟最多一次；周期失败为 Error，含 `error`，不带组件，按事件名找 |
| `sched_quality.view_stale` | Warn | 质量视图超过 60 秒未更新，暂停自动分层；含 `view_at`、`view_version`，最多每分钟一次 |
| `sched_quality.ttft_assessment` | Warn | 首字评估计算失败，保持未知；`account_id`、`model`、`group_id`、`error` |
| `sched_quality.delivery_capture_mismatch` | Warn | 请求已交付，但单次发送的质量采样为失败；`request_id`、`sends`、`sample_error_class`，不把这次交付误标为救回 |
| `sched_quality.invalid_policy` | Warn | 读取到无效分组门槛；`group_id`、`error` |
| `sched_quality.invalid_reset_time` | Warn | 账号重置时间无法读取；`account_id` |
| `sched_quality.binding_alias_failed` | Warn | 保存绑定别名失败；`account_id`、`error` |
| `sched_quality.takeover_avoid_failed` | Warn | 换号失败后写会话避让失败；`account_id`、`error` |
| `sched_quality.takeover_commit_failed` | Warn | 会话绑定提交存储失败；`account_id`、`error` |
| `sched_quality.rear_entries_read_failed` | Warn | 分组摘要的近 24 小时进后排统计读取失败，`rear_entries_24h` 为 `null`。含 `error` |
| `sched_quality.trial_room_failed` | Warn | 试用名额查询失败，按满额降层；`account_id`、`error` |
| `sched_quality.trial_acquire_failed` | Warn | 试用名额获取失败，按满额继续选号；`error`，部分调用另带 `account_id` |
| `sched_quality.trial_full_record_failed` | Warn | 满位时刻写入失败，本轮试用超时时，名额状态可能记为 `not_observed`（未观察到名额已满）。含 `error`、`account_id`、`group_id` |
| `sched_quality.trial_touch_failed` | Warn | 试用名额续期失败；`error`，部分调用另带 `account_id` |
| `sched_quality.trial_promote_failed` | Warn | 已提交绑定后，请求名额转会话名额失败；`account_id`、`error`，随后尽力释放请求名额 |
| `sched_quality.trial_release_failed` | Warn | 单个名额释放失败；`account_id`、`group_id`、`error` |
| `sched_quality.release_trial_group` | Warn | 试用结束后整组名额释放失败；`account_id`、`model`、`group_id`、`error` |
| `sched_quality.capability_mark_evicted` | Info | 能力标记满额时替换最早到期的一条；`account_id`、`limit`；不带组件，仅应用标准输出 |

`summary` 的 `cycles` 是引擎每 5 秒的工作周期，正常每 5 分钟约 60 次；与每 5 分钟一次的普通质量判定不同。`cooled` 计进入后排、续期、试用失败；`released` 计试用放回；`trial` 计进入试用；`relax` 计门槛放宽写入的动作；`clear` 保留这个键，现在恒为 0。只统计提交成功的动作，回滚的重评不发布也不计数；管理员手动恢复不计入这些计数。

**`sched_quality.state`** 的 `at` 是逻辑判定时刻，`rule_version` 当前为 `4.0`；用 `account_id`、`model`、`group_id`、`line` 定位线，`line` 为 `ttfo`、`error` 或 `cache`。`action` 取 `enter`、`renew`、`trial`、`trial_pass`、`trial_fail`、`trial_timeout`、`reset`、`relax`，见 [判定记录](group-quality-gate.md#判定记录)；旧日志还可见 `release`、`clear`。管理员重置提交后，原先在后排或试用的线各记一条 `reset`。`level` 规则 4.0 写 0；旧日志的 0–4 对应 20、40、80、160、240 分钟。`until` 在进入后排、续期、试用失败时写后排期限，进入试用时写试用期限，其余为零值。统计试用结束时先按 `action` 限定为 `trial_fail`、`trial_pass`、`trial_timeout`，再看下表两项；异常迟到的复核可能不经 `trial` 直接记 `trial_timeout`，见 [判定记录](group-quality-gate.md#判定记录)。

| 字段 | 含义 |
|---|---|
| `release_reason` | 试用结束的动作（`trial_fail`、`trial_pass`、`trial_timeout`）上写：`fail`（不达标回后排）、`good`（满 15 分钟且达标）、`deadline`（到截止）；旧日志还有 `qualified`、`no_samples`。其余动作为空串，`reset` 也是 |
| `trial_duration_seconds` | 试用结束的动作上写实际试用秒数，其余动作为 0。到截止结束时通常为 3600；升级前开始的试用可能是 7200 |
| `history_missing` | 是否缺少这次评估所需的历史 |
| `basis` | 门槛、证据来源和逐线证据，也含结束原因、试用时长和缺历史标记；缺期限时另有 `missing_deadline`，为 `true`。缓存线的 `path`、`low_sessions_10`、`window_from`、`window_to` 和首字线的 `tolerance`、`confidence` 见 [判定记录](group-quality-gate.md#判定记录)；`own_sample_at_60` 只在旧日志里 |
| `attempt_ids` | 相关尝试 ID，最多 50 个 |

首字证据有时间折扣和起估值，不能只看 P50 推断判定。

**`sched_quality.first_output_avoid`** 的 `confirmed=true` 表示写入成功，且回执已由当前请求确认。未确认时，`reason` 可为 `session_disabled`（开关已关）、`protocol_ineligible`（请求无交接资格）、`binding_changed`（原绑定不匹配）、`rejected`（存储拒绝）、`error`（存储错误）、`timeout`（超时）、`expired`（避让期已过）或 `receipt_stale`（回执已不匹配当前轮次）。`error_class` 只记录错误类别。

`owner_gen` 是写入时比对的归属代数。`expires_at_ms` 是脚本返回的实际到期时刻（Unix 毫秒），没有取得写入回执时为空。

**`sched_quality.session_switch`** 字段

| 字段 | 含义 |
|---|---|
| `gateway_request_id` | 网关本地请求 ID，可关联避让与尝试记录 |
| `group_id`、`model` | 分组与请求模型 |
| `from`、`to` | 原账号与目标账号 |
| `from_model`、`to_model` | 原账号和目标账号各自的实际调度模型，未确定的一端为空 |
| `from_priority`、`to_priority` | 两端的账号优先级，新建无原号或选号时未知的一端为空 |
| `kind` | 如 `create`（新绑定）、`migrate`（迁移）、`takeover`（接管）、`first_output_follow`（救场交接） |
| `reason` | 首字和报错迁移分别为 `quality_migration_ttfo`、`quality_migration_error`。救场交接用 `faster_delivery`。放弃时可见下列原因，其他未单列原因时取 `kind` |
| `committed` | `true` 表示绑定已提交。请求是否交付需结合尝试结果 |
| `faster_complete` | 救场交接收尾时记录，是否完整交付且首输出严格更快 |
| `commit_attempted` | 各类变更都记录，表示是否调用原子绑定提交 |
| `expected_gen`、`observed_gen`、`new_gen` | 预期代数、脚本读到的代数、写入的新代数。没有旧绑定、未读到或未写入时，相应字段为空 |
| `commit_result` | `committed`（已提交）、`binding_changed`（绑定已变）、`target_avoided`（目标仍在避让期）、`error`（提交错误）。未尝试提交时为空 |
| `attempt_seq` | 目标在当前请求内的物理发送序号，未实发或没有对应发送记录时为空 |
| `takeover_idle_setting`、`takeover_idle_observed` | 接管的配置值及从绑定剩余 TTL 推算的空闲秒数。配置为 0 或尚未观测时，实际空闲秒数为空 |
| `avoid_seen_at_select`、`session_on`、`transfer_allowed`、`quality_owned` | 接管选号时是否读到有效避让、是否开启会话韧性、协议是否允许交接，以及是否由会话质量路由持有。跳过或未完成的检查为空 |

救场交接放弃时可按 `reason` 查询：

- `session_disabled`（开关已关）、`protocol_ineligible`（请求无交接资格）、`avoid_unconfirmed`（避让未确认）、`existing_change`（已有其他绑定变更）。
- `not_front`（目标不在前排）、`borrowed`（临时借号）、`target_ineligible`（目标未通过准入复核）、`quality_owned`（归属由会话质量路由持有）、`quality_unavailable`（无法确认质量路由归属）。
- `pre_dispatch_invalidated`（目标未实际派发）、`incomplete_delivery`（未完整交付）、`canceled`（请求取消）、`missing_clock`（缺计时）、`not_faster`（首输出相等或更慢）。
- `binding_changed`（原绑定不匹配或已变化）、`commit_error`（绑定提交出错）。

接管或质量迁移放弃变更时，相关放弃事件每个请求最多记一条，`committed=false`。可按 `reason=target_avoided`（目标仍在首输出避让期）、`avoid_read_error`（读避让失败）或 `quality_owned`（由会话质量路由持有）查询。提交复验挡下的变更另可查 `commit_result=target_avoided`。因转换身份冲突决定换号重发、因此放弃原会话变更时，可见 `reason=identity_conflict`。

**`sched_quality.new_session`** 在首次普通选号且确认没有会话绑定（含旧绑定）时使用。选号返回可以是已取得槽位，也可以是等待计划；用 `gateway_request_id` 关联尝试记录的 `request_id` 核实实际发送，关联换号事件的 `kind=create`、`committed=true` 核实建立归属。

| 字段 | 含义 |
|---|---|
| `component`、`gateway_request_id`、`group_id`、`platform` | `sched_quality`、网关本地请求 ID、分组 ID（无分组为 0）、调度平台 `openai` |
| `requested_model`、`actual_model` | 传入调度的模型、选中号的调度模型；无选中号时后者为 `null`。透传加模型映射的账号实际出站模型可能不同 |
| `entry` | `messages`、`responses`、`chat_completions`、`responses_websocket`；内部调用回退到入口能力名，无能力名时为 `scheduler` |
| `selected_account_id`、`selected_quality_position` | 选中账号 ID、参与排序时的质量位置：`front`（前排）、`trial_with_seat`（试用有位）、`trial_full`（满位）、`trial_unknown`（名额未知）、`rear`（后排）。无选中号时 ID 为 `null`；没有位置记录时位置为空串 |
| `selected_sort_rate_multiplier`、`selected_sort_rate_source` | 排序用倍率和来源：`upstream_probe`（探测）、`account_stored`（账号存储）、`oauth_setting`（OAuth 设置）、`default`（默认）、`not_used`（不比价）、`unknown`（未知）；不用或未知时倍率为 `null` |
| `cheapest_downgraded_account_id`、`cheapest_downgraded_rate_multiplier`、`cheapest_downgraded_rate_source`、`cheapest_downgraded_reason` | 已排序账号中已知倍率最低的降层号、倍率、同上来源、降层原因数组（后排线名 `ttfo`、`error`、`cache`，或 `trial_full`、`unknown`）。有选中号时只比同优先级、同固定状态；同价按原排序。无可比降层号时均为 `null` |
| `next_higher_rate_multiplier`、`next_higher_is_downgraded` | 比上一项倍率高的最近一档，仍限其同优先级、同固定状态；该价档有前排或有位试用即为 `false`。没有这一档则两项均为 `null` |
| `candidate_pool_count`、`eligible_candidate_count` | 初筛候选数、通过初筛数；刷新不叠加同一批 |
| `filtered_model_not_supported_count`、`filtered_profit_floor_count`、`filtered_profit_ceiling_count`、`filtered_profit_invalid_rate_count` | 初筛按每个号首个失败原因分别计模型不支持、低于倍率下限、高于倍率上限、倍率无效 |
| `front_or_trial_with_seat_count`、`trial_full_count`、`trial_unknown_count`、`rear_count` | 本次实际排过序的不同账号数，分别计前排加有位试用、满位试用、名额未知试用、后排；跨优先级和固定状态 |
| `load_full_skipped_count`、`slot_unavailable_skipped_count` | 实际因负载满、拿不到并发槽而跳过的账号数，各自按账号去重 |
| `no_candidate_reason` | 选中则为空串；无号原因见下文 |

无号原因：`profit_ceiling`（倍率上限）、`profit_floor`（倍率下限）、`model_unsupported`（模型不支持）、`no_schedulable_candidate`（初筛无可调度号）、`candidate_read_failed`（候选读取失败）、`selection_failed`（选号失败）、`cached_no_candidate`（命中无号缓存）、`request_cancelled`（取消或到期）、`capacity_unavailable`（观察到容量不足）、`no_candidate`（无其他具体原因）。

打开协作偏好时，选中的号可能比同层、同固定状态的号优先级低或更贵（见[调度](scheduling.md#openai-分组的选号顺序)），这时不能只拿 `cheapest_downgraded_*`、`next_higher_*` 解释为什么选了它。

同一事件里还有这次派发的候选名单：

| 字段 | 含义 |
|---|---|
| `pool_rounds` | 这次派发成功读到普通候选池的次数。还没成功读过普通池时为 0（如成员读取失败、命中无号缓存）；成功读过之后重读失败，保留最后一次成功的次数和名单，`no_candidate_reason` 仍可能是 `candidate_read_failed`。为 `null` 表示这次没有采集名单（派发开始时 Info 日志没开） |
| `candidates` | 最后一次成功读到的普通候选池里通过初筛的号，取并发检查之前的集合。每项 `account_id`、`placement`、`rank`，以及可省略的 `lines`、`skip`。没读到池为 `null`，读到空池为 `[]` |
| `placement` | 取值同 `selected_quality_position`；没查过试用名额的试用号记 `trial_unknown` |
| `lines` | 这个号处于后排或试用的线：键 `ttfo`、`error`、`cache`，值 `rear` 或 `trial`，一个号可以一条在后排、一条在试用。按状态记，豁免生效或质量分层暂停时也照记 |
| `rank` | 普通派发选中的号为 1；其余参加过排序的号按最后一次参加的那一轮从新到旧、轮内次序排；从没参加排序的号（如一开始就被负载挡下）按账号编号排在最后。不是第一次比较时的名次 |
| `skip` | 因容量被跳过的原因：`load_full`、`slot_unavailable`，可以都有。被跳过的号仍在名单里，后来也可能又参加排序、甚至被选中 |
| `excluded` | 初筛时被 `model`（模型不支持）、`profit_floor`、`profit_ceiling`、`profit_invalid_account_rate` 筛掉的号，每项 `account_id`、`reason`。别的筛选原因不在这里，它不是完整的淘汰名单 |
| `rescue_attempted` | 兜底路径实际排过序时为 `true`，否则省略 |
| `rescue` | 兜底返回的号：`account_id`、`placement`；可能已经拿到槽，也可能只是等待目标。不并进 `candidates`。兜底排过序、但没有返回号（连等待目标也没有）时，只有 `rescue_attempted` |

**名单和原有计数怎么对。** 原有计数分两类：`candidate_pool_count`、`eligible_candidate_count` 和四个 `filtered_*_count` 是最后一次普通初筛的值；四个位置计数和两个 `*_skipped_count` 跨普通候选池的重读按账号去重累计，兜底的排序和抢槽也会计进去。名单只是最后一次成功读到的普通候选池的快照。所以下面的关系只在 `pool_rounds` 为 1、并且没有 `rescue_attempted` 的派发上成立：`candidates` 的个数等于 `eligible_candidate_count`；各 `placement` 的个数不小于对应的位置计数（`front_or_trial_with_seat_count` 是 `front` 加 `trial_with_seat`），多出来的是没参加过排序的号；`excluded` 各原因的个数等于对应的 `filtered_*_count`；带某种 `skip` 的账号数等于对应的 `*_skipped_count`。其余派发只核对名单本身：名次连续、没有重复账号、选中的号名次为 1 或者在 `rescue` 里。核对和统计以名单为准，原有计数留着和旧日志接得上。

上线后要分开数的三个量，不能互相代替：

1. 实际选中的号在后排：`selected_quality_position` 为 `rear`。
2. 普通候选池不空、但合格的号全在后排：`candidates` 非空，每项 `placement` 都是 `rear`。
3. 普通候选池里一个合格的号都没有：`pool_rounds` 至少为 1，`candidates` 为 `[]`。

选号结果可能是等待，不等于请求已经发出。看最终结果用 `gateway_request_id` 连使用记录的 `gateway_request_id`，未计费的请求连未计费记录的 `request_id`。系统日志可能因队列满或写入失败丢失，“没有这条事件”不能当成“没有新会话”。

**`resilience.budget_timeout_plan`** 是 Info 事件，没有 `component=sched_quality`，默认不写入 `ops_system_logs`。统计截止次数时查进程的结构化日志（容器部署时查容器日志）。只查 `ops_system_logs` 会少算。

`gateway_request_id` 对应尝试记录的 `request_id`，`attempt_seq` 标出本请求内的物理发送次序。`group_id` 是分组，`model` 是时限计划的模型。连接或 TLS 阶段就到点时，发送尚未确认，`attempt_seq` 为空，单列为“未确认发送”。

**`sched_quality.attempt_finished`**：`request_id` 是网关本地 ID，`attempt_id` 标识本次发送；`outcome`、`class` 是采样结果与归责类别，`http_status`、`status_origin` 分开记录状态与来源。`client_disconnect`、`terminal_seen`、`terminal_event`、`terminal_failed`、`error_source`、`send_error`、`context_done` 用于区分终态、网络、取消与清理。`terminal_ms`、`finished_ms` 相对本次尝试开始；`first_output_request_ms` 是请求级端到端首输出；`output_bytes` 是实际写给客户端的字节，含已写前奏，不等同语义输出字节。

协作恢复的阶梯和按记录借号发出的可选尝试、被优先保留内容截下改走恢复的原尝试，都是真实的上游发送，但不写质量尝试记录，也没有这条事件；所以不能用质量尝试记录还原全部上游发送。协作发送的去向看使用记录的 `collab_observation`，见 [Codex 协作](codex-collaboration.md)。

以下同前缀名称是管理审计动作，不是上述运行日志事件：`sched_quality.reset`、`sched_quality.override`、`sched_quality.clear_override`、`sched_quality.clear_auto_state`、`sched_quality.policy`、`sched_quality.ttft_rule`。恢复审计记录已清、保留及失败数量；配置保存的运行依据看 `sched_quality.policy_changed`。

### 协议转换

| 事件 | 级别 | 何时写 / 在哪查 |
|---|---|---|
| `upstream_conversion_failed` | Warn | 转换错误取得终态写入权时记一次。不带 `component=sched_quality`，在系统日志按事件名查 |
| `openai_conversion_retry` | Info | Chat 请求走 Responses 上游且 `conversion_reason=conflicting_output_identity` 时，每请求记一次是否换号实发及结果。带 `component=sched_quality`，进系统日志库；`gateway_request_id` 是网关本地请求 ID，可连尝试记录的 `request_id` |

流式工具调用身份冲突（`tool_identity_conflict`）、输出序号冲突（`conflicting_output_index`）以及两种输出歧义（`ambiguous_output_identity`、`ambiguous_unindexed_output`）沿用原来的错误处理；查 `upstream_conversion_failed`，不会有 `openai_conversion_retry`。

两条日志共用下面的固定诊断字段。它们不记录错误原文、生成内容、实际条目 ID、调用 ID 或工具名称。

| 字段 | 取值 |
|---|---|
| `source_protocol` | `responses`、`chat`、`anthropic`、`gemini`、`unknown` |
| `conversion_field` | 响应与条目：`response`、`output`、`output_index`、`content`、`content_index`、`summary_index`、`item_id`、`response_error`、`response_output`、`response_status`、`response_output_content_text`、`delta`；工具与推理：`arguments`、`reasoning`、`encrypted_function_args`、`tool_calls`、`tool_calls_index`、`output_arguments`、`output_status`；状态与其他：`status`、`stream`、`finish_reason`、`stop_reason`、`choices_index`、`choices_message_content`、`choices_delta_content`、`other` |
| `conversion_reason` | 身份：`ambiguous_output_identity`、`conflicting_output_identity`、`conflicting_output_index`、`ambiguous_unindexed_output`、`tool_identity_conflict`；内容和工具：`invalid_content_index`、`invalid_summary_index`、`conflicting_content_part_types`、`ambiguous_unindexed_content_part`、`invalid_tool_arguments`、`incomplete_tool_call`、`conflicting_tool_arguments`、`conflicting_reasoning`、`unsupported_tool`、`unsupported_choice_index`；响应与终态：`invalid_response`、`unsupported_terminal`、`missing_response`、`missing_terminal`、`upstream_response_failed`、`invalid_event` |
| `identity_conflict` | 身份类原因另有子类：按条目 ID 匹配为 `item_id_call_id`、`item_id_type`、`item_id_name`、`item_id_output_index`；按调用 ID 匹配为 `call_id_item_id`、`call_id_type`、`call_id_name`、`call_id_output_index`；按输出序号匹配为 `output_index_item_id`、`output_index_call_id`、`output_index_type`、`output_index_name`；多条目歧义为 `item_id_call_id_multiple_items`、`unindexed_multiple_items`；未细分为 `unspecified` |
| `upstream_status` | 真实上游响应状态，读不到时为 0；换号日志里是原尝试的状态 |

`upstream_conversion_failed` 另有 `client_status`（客户端实际 HTTP 状态）和 `client_error_status`（转换错误的逻辑状态 502）。流式响应已开场时，客户端实际状态可能仍是 200；也不能把逻辑 502 归给上游。

`openai_conversion_retry` 的 `retried` 为布尔值，只在候选账号的补发越过发送边界后为 `true`，即使随后没收到上游响应也算。`skip_reason` 在实发时为空串，未实发时取下表的固定值。

| 检查环节 | `skip_reason` |
|---|---|
| 交付或取消 | `client_canceled`（客户端取消或断开）、`output_started`（已经输出）、`usage_seen`（上游已报告用量） |
| 重放资格 | `missing_replay_marker`（缺标记）、`replay_marker_false`（标记为否）、`state_continuation`（状态续接或受保护交接）、`missing_wire_body`（缺原出站体）、`duplicate_key`（JSON 有重复键）、`wire_model_mapped`（原号出站模型与调度模型不同） |
| 请求结构 | `unrecognized_input`（请求体或输入结构不识别）、`unrecognized_tools`（工具定义或选择方式不识别）、`provider_tools`（上游代执行工具）、`custom_tools`（自定义工具） |
| 预算 | `missing_budget`（没有请求预算）、`budget_exhausted`（时间或派发次数无余量）、`replay_limit`（已用过本次换号重发额度）、`switch_budget`（换号额度用完） |
| 选号与准备 | `no_candidate`（无合规候选）、`candidate_protocol`（候选不走 Responses）、`candidate_wire`（候选模型不符、改写路径不同或发送前出站体校验不符）、`slot_unavailable`（未取得并发槽）、`prepare_failed`（未完成重发请求构建）、`send_not_started`（已构建重发请求，但在本地发送准备阶段失败，未越过发送边界）。无号且两类候选检查都失败时，优先记 `candidate_wire` |

`result` 为 `success`（重发成功）、`conversion_error`（首输出前再次遇到转换错误）、`interrupted`（补发已输出后中断）、`canceled`（取消或断开）、`upstream_error`（其他失败）或 `not_retried`（未重发，沿用原错误收口）。统计补发次数以 `retried` 为准，前置准备失败也会记录结果。重发条件见 [调度](scheduling.md#协议转换失败时换号重发)。

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

独立 `function_call_output` 被改写并实际发出后，请求级 `degradations` 记一项 `input[].function_call_output` / `map`。兼容模式未关闭、账号在生效范围内、尚未向客户端输出时，上游以该原因拒绝（错误不带与之冲突的 `param`）会记一条上游错误事件，带 `reason=fco_standalone_rejected` 和结构摘要 `fco_shape`；改写后的请求实际重发才将 `resent` 设为 true。

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

1. 重启后先看 `sched_quality.loading`：`page` 表示仍在分页加载，`incomplete` 带错误，加载完成前不分层；`complete` 表示证据加载完成。长期表现另看系统日志里启动之后的那条 `sched_quality.performance_ready`。再看 `sched_quality.summary` 是否每 5 分钟按时出现、周期是否变慢：每个实例约 60 个周期；`cycles_failed` 大于 0、`cycle_ms_max` 变大或出现 `sched_quality.cycle` 的 Warn，说明引擎周期变慢或失败。`cycle` 的 Error 不带组件，须按事件名找，结合错误字段排查。
2. 再按账号看 `sched_quality.state`：何时进入后排、试用或回正常，哪条线（`line`）、依据来自哪里（`basis`），以及试用结束的原因（`release_reason`）。补判资料或期限不全时，再看 `sched_quality.history_missing`、`sched_quality.missing_deadline`。
3. 新会话没选到号或落到后排，先看 `sched_quality.new_session` 的候选名单；会话搬动看 `sched_quality.session_switch` 的 `committed`，用 `gateway_request_id` 连到访问日志和尝试记录的 `request_id`。`sched_quality.attempt_finished` 可区分首输出截止、真实上游失败与取消；怀疑协议转换失败时查 [协议转换日志](#协议转换)。

**设置保存后其他实例行为没变**：其他实例每 10 秒刷新；若长时间未生效，查找 `resilience.settings_refresh_failed`。

### 真实上游发送次数

新请求的 `dispatch_phases.count_version = 2` 表示统一发送口径：`dispatch_count` 统计所有平台实际启动的上游发送尝试，包含拨号失败、同号重试、换号重试、HTTP 重定向及底层传输重发；它不表示供应商确认收到的次数。
HTTP 在实际传输边界计数，`DoWithTLS` 委托 `Do` 不重复计。请求准备失败、预算或本地参数拒绝不计。WS 每次应用请求帧写入计一次（含生成预热帧），握手和 ping 不计；协作结果认领不算新发送。
插件成功返回 HTTP 响应或报错携带 request_sent=true 时计 1，明确 request_sent=false 的准备失败计 0；插件 v1 不回报内部重发次数，因此只要尝试提交 Start，本次请求的 dispatch_phases 就写入 count_inexact=true，并在同一 turn 的重试中保留。精确计数时省略此标记；单发补偿必须同时满足计数为 1 且精确（DispatchCountExact()），不得把插件的 1 当作精确单发证据。
只统计带请求计时状态的上下文；没有预算或非 text 请求也统计，后台探测、刷新、余额查询不产生客户请求计数。计数不参与预算、选号、响应、计费或账号配额。
相同计数也保存在 `dispatch_phases.dispatch_count`，供没有独立计数列的无账单请求记录使用；没有派发时为 0。阶段耗时仍只记录首发前已完成的阶段。
WebSocket 的每个新逻辑 turn 重新开始计数和计时；同一 turn 的重试、换号、修复和预热继续累加。Live sideband 的客户文本与二进制应用帧也计数，无请求状态的后台帧不计。

历史记录没有 `count_version` 时维持旧口径，尤其非 OpenAI 历史 0 不代表未发送，不回填，也不可直接与版本 2 比较。
