# 第一期发布与值班 runbook

## 发版前置

1. 两路设计签字与代码审查完成，GT六维、ER有限回归、AST守卫、SQL查询计划、压测p99增幅≤1ms证据齐全。
2. 迁移292/293分别设置lock_timeout=5s、statement_timeout=30s；只nullable，无default/index/backfill。只读pg_stat_activity检查两表长事务，避开长事务发版。
3. 首启关闭自动重启或限定次数并退避。55P03/57014不在initializeDatabaseWithRetry的进程内重试范围，启动失败时停止发版，保留旧版低峰人工重试；否则unless-stopped会反复等锁影响旧实例。两次迁移可各挡一次，不承诺总≤5s；拿锁后直到提交仍持锁，可能半迁移。
4. 测试库分别量等锁、执行/提交持锁，验证新进程失败行为，验证全迁移/半迁移下0.5.11启动与usage/ops读写。当前串行窗口内禁止执行。

## 配置切换

通过管理端“网关韧性”读取当前完整设置（GET /api/v1/admin/settings/gateway-resilience），保留其他字段，只改budget下的目标键，再用同页保存（PUT同路径）。请求开始快照，切换只影响新请求；保存后重新GET核对值。严禁两个开关同窗切换。

- 阶段0：arrival+observe 48h，对0.5.11相同星期时段。文本失败率差≤0.05个百分点，FOT/budget_exhausted相对差≤10%，否则暂停。
- 阶段1：body_eof+observe 48h，按anchor_eligible看成功率/大体积桶/取消和迟发；其他字段仍原样。
- 阶段2：先完成N3（高级调度器是否开启及applied是否豁免）PM裁决，再enforce 48h。applied=true时slow_streak_decision advanced=true与latency_hold_sample recorded=true必须均为0。

一般护栏越线持续2h回滚该阶段切换键；逐单失败/迟发立即告警复核。观察未送达header_wait/pre_output、队列p99、门槛转移、CDN断开、体积桶成功率；client_family在240–290s固定±5s聚集取消则全局回arrival并核实客户端时限。cdn_observed来源不可信。

## G9 硬回滚：触发

阶段 1（`body_eof+observe`）及之后启用。进入阶段 1 前，必须先发布包含本节观测链路的新二进制，并完成 K10；0.5.12 的 observe 发版门不能替代这项验证。执行方为获授权的生产操作方，确认即切回，不等一般护栏的 2 小时。

两条 Warn 均在断开当下记录，`budget_start_mode` 取该请求创建预算时的快照。`late_attempt_undelivered` 在完成原有 abuse 判定后记录：`disconnect_unix_ms` 是保存的断开时刻，`abuse` 是当次判定，`billed` 是**断开时的临时状态**。以下触发只看进入 abuse 状态及丢日志兜底，**完全不读临时 billed，也不等待最终扣费**。

- **T-a0**：SQL-1a 返回任意行，即无计时器迟发的请求自己进入 abuse 状态；一条即确认。
- **T-a**：SQL-1b 返回任意行，即同一 key、同一 UTC 小时内既有无计时器迟发未送达，又有进入 abuse 状态。迟发按 `disconnect_unix_ms` 分小时，缺字段时退回日志时间；abuse 按日志时间分小时。abuse 一侧不按模式过滤，迟发一侧只计 `body_eof`。
- **T-b**：任一实例 sink 的累计 `dropped_count` 或 `write_failed_count` 上升，且满足任一迟发条件：本次 `late_attempt_start` 的 `false:body_eof` 在 60 分钟窗口内 > 0，或 SQL-1c 返回行。SQL 查询失败另按下文处理。某个实例读不到，或发生非计划重启（身份变化或累计计数下降），也直接按 T-b 命中。

**指标口径与巡检**：逐实例直连读取 `GET /api/v1/admin/ops/system-logs/health` 和 `GET /api/v1/admin/resilience/stats?window=15m`、`?window=60m`。sink 的累计数只与同一实例身份的上次成功读数比较；下降或身份变化视为重启；非计划重启直接按 T-b 命中，不把缺失当作 0。阶段 1、2 冻结非紧急重启和切流；必须重启时先跑 R0 并保存旧实例读数。迟发指标的 label 为 `<has_timer>:<budget_start_mode>`（如 `false:body_eof`），在 guard 决定后的派发前计数，在途即能看到，Warm/发送样本更新及 FinishTiming 不会重复计数。两条 Warn、请求时序和指标共用实际派发 owner 的冻结集合，handler/profile 预设不计数；后建 response-stage timer 不回写分类。Gemini 同号重试若仍受原共享 attempt guard 覆盖，记有计时器，但不改变发送起点、guard 或 plan。它是分钟桶的滑动窗口和，只看窗口内 > 0，不能比较相邻读数求增量，也不能据其下降推断重启。桶满时迟发指标只合并 group/account ID，保留四种模式与 timer label，汇总行不带 group_id；T-b 按 label 汇总所有行（含无 group_id 的实例汇总行），不能先按分组过滤。

例行每 10 分钟跑 R0；15 分钟窗口内有 `false:body_eof` 时，5 分钟后加跑一次，再恢复例行节奏。检查推迟时，加跑窗口取“距上次检查时长 + 5 分钟”，不超过 60 分钟。记录 `T_stage1 = body_eof 切换响应时刻 + 15 秒`，再次进入阶段 1 时重新记录；首小时的长窗口取 `now − T_stage1`，之后用 60 分钟。SQL 的日志下限也不早于 T_stage1，并按请求快照模式排除阶段 0 开始、阶段 1 才断开的 arrival 请求。分钟桶只有分钟精度，重进阶段 1 的同分钟旧 body_eof 事件可能使回滚偏保守，允许偶发多切一次，不能漏报。

**SQL 失败**：SQL-1a/1b/1c 任一报错、超时或读不到库，在本轮 2 分钟内重试一次。仍失败且同期 sink 丢弃/写失败上升，本轮按 T-b 命中（不要求读到迟发指标）；只有成功读到 sink 且确认未上升时，本轮才记未完成；读不到实例按 T-b 命中。连续两轮未完成也按 T-b 命中。正常返回空结果不等于查询失败。

**前置检查**：日志级别不高于 warn，sink 确实落库且 `level='warn'`；所有实例的 health/stats 可直连读取；以 SQL-0 与 `written_count` 核实写入。阶段 0 留下 sink 丢失基线，并每小时空跑三条 SQL（仅空跑时去掉 body_eof 过滤），记录 arrival 隐藏重放的基数。进入阶段 1 前保留可用的 0.5.11 回滚镜像并通过 R2′ 演练。

| 步骤 | 时限 | 操作及通过条件 |
|---|---|---|
| R0 检查与确认 | 例行每 10 分钟；有迟发时 5 分钟加跑 | 跑 SQL-1a/1b/1c，逐实例读 health 和两个 stats 窗口，记录实例身份与本次成功读数时刻。T-a0/T-a/T-b 任一成立即确认，记下 T_confirm、触发类型及相关 key/请求/读数 |
| R1 留底 | T_confirm + 2 分钟内 | GET `/api/v1/admin/settings/gateway-resilience`，保存 settings，确认 `budget_start=body_eof` |
| R2 切回 | T_confirm + 3 分钟内 | PUT 同路径，最小补丁 `{"budget":{"budget_start":"arrival"}}`；响应及重新 GET 确认只有此键改变，记 T_switch；不改 budget_cut_attribution |
| R2′ 库不可写 | R2 失败后立即 | 执行下文已演练的镜像回退；非 2xx、超时或响应不是 arrival 均算 R2 失败 |
| R3 等传播 | T_switch + 30 秒 | 其他实例正常在 10 秒内重载 |
| R4 核验 | T_confirm + 15 分钟内 | 再 GET 确认 arrival；SQL-2 只返回 arrival 且计数 > 0；无新文本请求落库时稍后补查。指标 budget_snapshot 只作逐实例辅助 |
| R4′ 失败 | 立即 | 重发 R2；仍失败时定位重载失败实例，由生产操作方通报后处置，重跑 SQL-2 |
| R5 事后核实 | 当天 | 按下一节整理最终扣费和逐单结局；恢复 body_eof 需 PM 重新裁决 |

### G9 事后核实

通过 `request_id`、`client_request_id`、`api_key_id` 将 Warn 对应到 `gateway_undelivered_requests`，最终是否扣费以该表的 `billed` 或相应 usage 记录为准。Warn 的临时 `billed=false` 不代表最终免费，`resilience.abuse_guard` 也不单独证明已扣费。事后核实不阻塞 T-a0/T-a/T-b 的切回。

事件单保留 T_confirm、触发类型、T_switch、R4 结果，以及迟发 Warn 的 `anchor_basis`、两个迟发计数、快照模式和逐单结局；只记 key ID，不记凭据。缺失 Warn 或未送达行标为未知，并核对 sink 丢失及 `undelivered_record_dropped` / `undelivered_record_failed`。用 anchor_basis 区分锚点后移与原有隐藏重放；“无计时器路径新增尝试限制”仍属后续代码项。

### G9 只读 SQL

占位符 `<T_stage1>`、`<T_last>` 填 ISO 8601 UTC 时间。`T_last` 取各实例上次成功读数中最早的一个；SQL-1c 的 UTC 小时下限按修订 15 取 **T_last 减 5 分钟后再向下取整**，覆盖时钟偏差。三条 SQL 都不使用 Warn 的临时 billed。

SQL-0（sink 在写）：

```sql
SELECT lower(level) AS level, count(*)
FROM ops_system_logs
WHERE created_at >= now() - interval '15 minutes'
GROUP BY 1 ORDER BY 1;
```

SQL-1a（T-a0：迟发请求自己进入 abuse 状态）：

```sql
SELECT created_at, api_key_id,
       (extra->>'late_attempt_count')::numeric          AS late_attempts,
       (extra->>'late_attempt_no_timer_count')::numeric AS late_no_timer,
       extra->>'stage'                                  AS stage
FROM ops_system_logs
WHERE created_at >= greatest(now() - interval '3 hours', '<T_stage1>'::timestamptz)
  AND level = 'warn'
  AND message = 'resilience.abuse_guard'
  AND extra->>'budget_start_mode' = 'body_eof'
  AND COALESCE((extra->>'late_attempt_no_timer_count')::numeric, 0) > 0
ORDER BY created_at DESC;
```

SQL-1b（T-a：同 key、同 UTC 小时配对）：

```sql
WITH late AS (
  SELECT api_key_id,
         date_trunc('hour', COALESCE(to_timestamp((extra->>'disconnect_unix_ms')::numeric / 1000.0), created_at)
                            AT TIME ZONE 'UTC') AS utc_hour,
         request_id
  FROM ops_system_logs
  WHERE created_at >= greatest(now() - interval '3 hours', '<T_stage1>'::timestamptz)
    AND level = 'warn'
    AND message = 'resilience.late_attempt_undelivered'
    AND extra->>'budget_start_mode' = 'body_eof'
    AND api_key_id IS NOT NULL
), abuse AS (
  SELECT api_key_id,
         date_trunc('hour', created_at AT TIME ZONE 'UTC') AS utc_hour,
         count(*) AS abuse_entered
  FROM ops_system_logs
  WHERE created_at >= greatest(now() - interval '4 hours', '<T_stage1>'::timestamptz)
    AND level = 'warn'
    AND message = 'resilience.abuse_guard'
    AND api_key_id IS NOT NULL
  GROUP BY 1, 2
)
SELECT l.api_key_id, l.utc_hour,
       count(*)               AS late_undelivered,
       max(a.abuse_entered)   AS abuse_entered,
       array_agg(l.request_id) AS late_request_ids
FROM late l
JOIN abuse a USING (api_key_id, utc_hour)
GROUP BY 1, 2
ORDER BY 2 DESC, 1;
```

SQL-1c（T-b 的迟发日志条件；T_last 减 5 分钟）：

```sql
WITH late AS (
  SELECT api_key_id,
         date_trunc('hour', COALESCE(to_timestamp((extra->>'disconnect_unix_ms')::numeric / 1000.0), created_at)
                            AT TIME ZONE 'UTC') AS utc_hour,
         request_id
  FROM ops_system_logs
  WHERE created_at >= greatest(now() - interval '3 hours', '<T_stage1>'::timestamptz)
    AND level = 'warn'
    AND message = 'resilience.late_attempt_undelivered'
    AND extra->>'budget_start_mode' = 'body_eof'
    AND api_key_id IS NOT NULL
)
SELECT api_key_id, utc_hour,
       count(*)                AS late_undelivered,
       array_agg(request_id)   AS late_request_ids
FROM late
WHERE utc_hour >= date_trunc('hour', ('<T_last>'::timestamptz - interval '5 minutes') AT TIME ZONE 'UTC')
GROUP BY 1, 2
ORDER BY 2 DESC, 1;
```

SQL-2（切回核验；`<T_switch_ms>` 为 Unix 毫秒）：

```sql
SELECT budget_start_mode, count(*)
FROM usage_logs
WHERE created_at >= to_timestamp(<T_switch_ms> / 1000.0)
  AND budget_t0_unix_ms >= <T_switch_ms> + 15000
  AND budget_start_mode IS NOT NULL
GROUP BY 1;
```

created_at 用于索引范围，budget_t0_unix_ms 用于排除持旧快照的在途请求，不能只凭落库时刻判断切回。SQL-1b 的 abuse 窗口为 4 小时，迟发为 3 小时，均不早于 T_stage1；跨小时以保存的断开时刻关联。

### R2′ 与 K10 演练

R2 写库失败不会发布新配置。R2′ 蓝绿回退到预留的 0.5.11 镜像；只由生产操作方执行并通报。切 Caddy 前确认 0.5.11 健康接口 200 且 60 秒内 RestartCount 不变，否则 R2′ 作废，保持现有实例、优先恢复库。库不可读使旧实例也无法启动时转事故处理。R2′ 尽快完成，记录用时及阶段 2 版本回滚副作用。

库恢复后，先向不在 Caddy 上游的新版本实例发送 R2，并直连 GET 确认 arrival；再加入上游、撤下 0.5.11。不得用旧版 PUT 丢弃新键。K10 ⑫ 要实测旧版与已执行迁移的库兼容、只读库启动表现和忽略新增键；不以静态推断替代演练。

K10 ④ 必须在请求仍阻塞于上游、尚未 FinishTiming 时看到 `false:body_eof`，随后把共享分钟桶填满 4096 个 key、模拟两条 Warn 同时丢失及 SQL 正常返回空，确认实例汇总行仍有 `false:body_eof > 0`，按 T-b 执行 R1–R4。另验证：跨整点断开、T-a0 不等 handler 返回、两条 Warn 丢失、窗口自然下降、SQL 超时重试/连续两轮失败、旧 arrival 请求跨阶段完成，以及快速重新进入阶段 1 时同分钟旧 body_eof 桶导致的保守命中。记录每次确认至切回的耗时；最终扣费单列事后核实。

本 runbook 不授权本实施分支操作生产；回滚历史边界见 [发布说明](release-notes.md)。
