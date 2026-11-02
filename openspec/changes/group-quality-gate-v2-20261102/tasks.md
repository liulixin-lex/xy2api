# 实施与验收

分支 `feat/group-quality-gate-v2-20261102`（基于 `origin/main`），一个 PR 交付。派工说明、逐账号证据与回放脚本在本地交接包，不入库。

约束：
- 不加数据库迁移，不改 Ent，不新增设置项；
- 推送、PR、合并、发版与部署须经用户授权；
- 改到 `resilience_session.go`、`AccountsView.vue` 等在 `tools/upstream-sync/policy.json` 登记过字面量的文件时，保持这些字面量。

## 1 后端

- [x] 1.1 **度量**：
  - `backend/internal/repository/group_quality_gate_repo.go` 的 `loadGroupQualityObservations`：history CTE 改为 `COALESCE(NULLIF(end_to_end_first_output_ms,0), first_token_ms, 0) AS first_output_ms`；
  - `GroupQualityObservation.FirstTokenMs` 改名为 `FirstOutputMs`（`group_quality_gate.go`）；
  - `groupQualityMetricsFor` 用它采样。
- [x] 1.2 **档位**：
  - `resolveGroupQualityTier` 改为两档规则（规格“Two ordering tiers”）：零样本不再直接返回快档，与“样本不足”同走档位记忆；
  - 评估循环 `evaluate` 对每组先算完全部成员的指标与状态，再算 `best` 与实时快档线，最后定档；
  - 新增 `DataTier`、`FastAt` 字段，`TierAt` 只在按数据定档时刷新（数据新鲜，且 ≥10 个续轮样本或满足零缓存条件；不看熔断状态，触发熔断的当轮也先记档）；判为 0 档时刷新 `FastAt`。无数据时 30 分钟内沿用 `DataTier`，否则 6 小时内快过排 0 档，否则 2 档。熔断、到期、手动恢复、观察结束、门槛关闭（`evaluate` 中按组关闭的分支）、离组保留都携带这三个字段；离组保留延长到最后一次熔断结束或按数据定档后 2 小时（`departedGroupQualityMembers` 的 `end` 取两者较晚者），进程重启清空；
  - `OrderTier` 与状态视图对“已到期、未重新评估”的熔断返回 2；
  - 删除 `GroupQualityGateTierTrial`，常量值保持快 = 0、其他 = 2；
  - 状态接口每组加 `effective_fast_warm_ttft_p90_ms`（`GroupQualityGroupView`）。
- [x] 1.3 **观察**：
  - `evaluateGroupQualityMember` 的试用分支：满 20 个请求或距试用起点 30 分钟即结束，30 分钟这一条不要求数据新鲜，原因 `trial_elapsed`，事件由 `logGroupQualityTransition` 输出；
  - 状态视图中试用成员的 `until` = 试用起点 + 30 分钟；
  - 试用入口、再熔断条件、手动恢复分支不变。
- [x] 1.4 **门槛组迁移**（`openai_account_latency_hold.go` 的 `migrateOpenAILatencyHeldOwner`）：
  - 调用点位置不变（`openai_gateway_scheduling.go` 的 `selectAccountWithLoadAwareness`），只多传 `preferLowUpstreamRate`；该行登记在 `tools/upstream-sync/scheduler-diff.json`，同步更新并跑 `sync.py scheduler-diff`；
  - 保留开头的通用前置条件：绑定号存在、`openAINoStickyMigration`、`SessionOn`、`qualityOwnsSticky`，门槛分支另要求本组未启用份额调度（`shareSchedulingActive`），且本次选号开启低倍率优先（调用点已有的 `preferLowUpstreamRate`；只有此时档位决定选号顺序）；
  - 把门槛组的直接返回换成独立分支，放在判慢保持的两处检查（`latestHoldUntilUnixNano`、绑定号 `Held`）之前，不依赖判慢保持状态；
  - 门槛视图增加只读判断：绑定号在本组未熔断、非固定、续轮样本 ≥10、p90 > 策略快档线，数据取评估快照；
  - 候选复用现有过滤（资格、渠道限制、运行时阻断、利润门否决、实时负载），门槛分支另要求候选在本组为 0 档且未被否决；有空位才删除绑定；
  - 记 `group_quality_gate.migrate`（Warn，字段 `group_id`、`account_id`、`warm_ttft_p90_ms`、`fast_warm_ttft_p90_ms`）；
  - 非门槛组的现有逻辑不变。
- [x] 1.5 **事件级别**：`group_quality_gate.guard_hold`（`group_quality_gate_service.go`）与 `group_quality_gate.last_resort`（`resilience_session.go` 的 `noteOpenAILastResort`）改为 Warn。
- [x] 1.6 **测试**（表驱动，每个规格场景一条，不扩展矩阵）：
  - 档位：最快值缺失、实时快档线边界、快档线封顶、零缓存、熔断与不达标、未开门槛的组；记忆（同一张表）：错误率熔断后回 0 档、迁空 30 分钟后回 0 档、6 小时内没快过留 2 档、30 分钟内沿用、首字熔断当轮记 2 档；
  - 观察：到期无流量 30 分钟结束（新鲜与过期数据各一）、20 个请求结束、2 次软失败再熔断、手动恢复；
  - 迁移：慢号有快档空位则迁、两线之间不迁、快档满载不迁、强续接不迁、质量路由接管不迁、固定账号不迁、非门槛组行为不变；
  - 仓储集成：端到端为 0 时回落到 `first_token_ms`，一条。

## 2 前端

- [x] 2.1 `AccountActionMenu.vue` 新增布尔 prop（如 `qualityRecoverable`），`hasRecoverableState` 纳入它；`AccountsView.vue` 由已加载的门槛单元格（ejected、guard_held、trial）计算后传入。组件测试一条。
- [x] 2.2 **文案**（zh 与 en）：
  - `groupQualityGate.ts`：
    - “试用”改为“观察中”，显示“观察至 hh:mm”；
    - 说明文字改为“到期后观察 30 分钟”；
    - “首字 p90”改为“可见首字 p90”，提示标题改为“续轮用户可见首字 p90（近 30 分钟）”；
    - 档位说明为“快档 / 其他”，并显示本组实时快档线。
  - `settings.ts` 的 `openaiTTFTModeHint`：说明门槛按端到端首字判定、不受此设置影响；列出受影响的显示模块（用量记录、运维看板与健康分、渠道监控）。
- [x] 2.3 `types/groupQualityGate.ts` 加 `effective_fast_warm_ttft_p90_ms`；`groupQualityPresentation.ts` 去掉试用档的档位映射，保留试用（观察）状态。

## 3 文档与登记

- [x] 3.1 `docs/GROUP_QUALITY_GATE.md`：
  - 指标来源；
  - 两档与实时快档线；
  - 观察；
  - 门槛组迁移；
  - 事件与级别：“恢复与观测”一节称状态事件为 Warn，而 `guard_hold`、`last_resort` 原为 Info，随 1.5 一并改正；补 `migrate` 与 `trial_elapsed`；
  - 升级说明与回退。
- [x] 3.2 `docs/USAGE_METRICS.md`：门槛读端到端首字，用量记录首字口径不变。
- [x] 3.3 `docs/GATEWAY_RESILIENCE.md`（`response.metadata` 判为开场那一条）：删除“缺省语义首字仍从 `response.metadata` 起算”的过时说法。
- [x] 3.4 `docs/account-scheduling.md` 会话迁移一条：改为“门槛组按门槛判据迁出”。
- [x] 3.5 `tools/docs_consistency.json`：门槛事件契约的正则加 `migrate`；`GroupQualityGateTierTrial` 登记到 `retired`，只允许出现在历史记录中。
- [x] 3.6 `python3 tools/check_docs_consistency.py` 通过。

## 4 验证与交付

- [x] 4.1 后端：在 `backend/` 下运行 `go test ./internal/service/... ./internal/repository/...`（仓储集成用例需要 Docker），以及按 pins 版本运行 golangci-lint（`--max-issues-per-linter=0 --max-same-issues=0`）。
- [x] 4.2 前端：`npx -y pnpm@9 --dir frontend run lint:check`、`typecheck`、`test:run`、`build`。
- [ ] 4.3 开 PR，正文列出规格场景与验证结果；不合并、不发版、不部署（发版按惯例交给 astra）。
- [ ] 4.4 上线 24 小时后按 design §7 复核（本地交接包里有回放脚本）。

## 5 运维（用户在管理端配置，不随代码交付）

- [ ] 5.1 可用账号数告警：号池只有 1–2 个可调度账号的几个组各一条（组号在本地交接包）：

  | 字段 | 取值 |
  |---|---|
  | 指标 | `group_available_accounts` |
  | 条件 | `<` 1 |
  | 持续 | 2 分钟 |
  | 冷却 | 30 分钟 |
  | 级别 | P1 |
  | 过滤 | `group_id` 与平台 |
  | 邮件 | 按现有邮件通知设置 |
