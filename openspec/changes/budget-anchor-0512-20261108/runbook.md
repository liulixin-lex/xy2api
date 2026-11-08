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

## G9 硬回滚：值班步骤（修订 9 N2、修订 10 P2-1、Opus P2-D）

- **适用**：阶段 1 及之后，即 `budget_start=body_eof` 生效期间。
- **执行方**：原 PM（生产操作方）。接力 PM 与 B 只接收通报。
- **时限**：从确认时刻 T_confirm 起，15 分钟内完成切回和核验。不等一般护栏的“越线持续 2 h”。
- **只动一个键**：`budget.budget_start`。`budget_cut_attribution` 不随之回滚（修订 9 只要求切 `budget_start`）。
- 下面的 SQL 一律由已获授权的生产操作方只读执行。事件单里只写 `api_key_id`，不写任何凭据。

| 步骤 | 时间 | 操作 | 通过条件 |
|---|---|---|---|
| R0 发现与确认 | T_confirm | 阶段 1、2 期间指标 `late_attempt_start{has_timer=false}` 有增量后 5 分钟跑 SQL-1，之后每 10 分钟复查，覆盖 62–242 s 断开延迟。每次同时读 `GET /api/v1/admin/ops/system-logs/health`；dropped_count 或 write_failed_count 上升且无计时器迟发指标有增量时视同命中。SQL-1 查到任一行即确认，不再观察，也不等更多样本 | 记下 T_confirm、命中的 `api_key_id` 和 UTC 小时 |
| R1 留底 | ≤ T_confirm + 2 min | `GET /api/v1/admin/settings/gateway-resilience`，把返回的 `settings` 原文存进事件单 | 当前 `budget.budget_start` 为 `body_eof` |
| R2 切回 | ≤ T_confirm + 3 min | `PUT /api/v1/admin/settings/gateway-resilience`，请求体只含 `{"budget":{"budget_start":"arrival"}}`（§2.5 切换机制） | 响应中 `settings.budget.budget_start` 为 `arrival`，与 R1 相比只有这一个键不同；记下响应时刻 T_switch |
| R3 等待传播 | T_switch + 30 s | 不操作。其他实例 ≤10 s 内完成重载 | — |
| R4 核验 | ≤ T_confirm + 15 min | a. 再 `GET` 一次，确认 `budget_start` 为 `arrival`；<br>b. 跑 SQL-2；<br>c. 可选：`GET /api/v1/admin/resilience/stats?window=5m`，看本实例的 `budget_snapshot` 在 T_switch 之后不再出现 `body_eof`（Stats 是实例级，`service/resilience_reservation.go:180`） | SQL-2 只返回 `arrival`，且计数 > 0。窗口内还没有新的文本请求落库时，延长窗口再跑 |
| R4′ 未通过 | 立即 | 先重发一次 R2。仍有 `body_eof` 行时，查对应时段 `reloadSettings` 的错误日志，找出没有重载成功的实例并重启它（冷启动会从库里读设置）。重启是另一项生产操作，执行前通报接力 PM | 重跑 SQL-2 通过 |
| R5 收尾 | 当天 | 事件单写明 T_confirm、T_switch、R4 结果、命中的 `api_key_id`。“无计时器路径的新尝试以 T0+base−2 s 为限”立为 1.5 期代码项（修订 9 N2）。恢复 body_eof 需要 PM 重新裁决 | — |

SQL-1（确认：同一 key、同一 UTC 小时内，迟发未送达与 abuse_guard 计费同时出现）：

```sql
SELECT api_key_id,
       date_trunc('hour', created_at AT TIME ZONE 'UTC') AS utc_hour,
       count(*) FILTER (WHERE message = 'resilience.late_attempt_undelivered') AS late_undelivered,
       count(*) FILTER (WHERE message = 'resilience.abuse_guard') AS abuse_billed
FROM ops_system_logs
WHERE created_at >= now() - interval '3 hours'
  AND lower(level) IN ('warn', 'warning')
  AND message IN ('resilience.late_attempt_undelivered', 'resilience.abuse_guard')
  AND api_key_id IS NOT NULL
GROUP BY 1, 2
HAVING count(*) FILTER (WHERE message = 'resilience.late_attempt_undelivered') > 0
   AND count(*) FILTER (WHERE message = 'resilience.abuse_guard') > 0
ORDER BY 2 DESC, 1;
```

SQL-2（核验。`<T_switch_ms>` 是 T_switch 的 Unix 毫秒，加 15 s 覆盖重载间隔）：

```sql
SELECT budget_start_mode, count(*)
FROM usage_logs
WHERE created_at >= to_timestamp(<T_switch_ms>/1000.0)
  AND budget_t0_unix_ms >= <T_switch_ms> + 15000
  AND budget_start_mode IS NOT NULL
GROUP BY 1;
```

- SQL-1 依赖两条 Warn 落进 `ops_system_logs`（§2.4）。sink 丢日志时可能漏判，所以 R0 同时看指标；GT-26 断言 Warn 的字段。
- SQL-2 用 `budget_t0_unix_ms` 而不用 `created_at` 过滤：usage 行在请求结束时才写，持有旧快照的在途请求会在切换之后才落库，不应算作切回失败。
- 规则按 Opus P2-D：只要同一 key、同一 UTC 小时内两条 Warn 同时出现就切回，不再逐单确认被计费的那一单是否就是迟发的那一单。

本 runbook 不授权本实施分支操作生产；回滚历史边界见 [发布说明](release-notes.md)。

阶段 1 启用前确认日志级别不高于 warn 且 sink 正在落库。两条 Warn 的配对表示迟发未送达与进入 abuse 状态，不证明具体请求已扣费；硬触发按进入状态执行。
