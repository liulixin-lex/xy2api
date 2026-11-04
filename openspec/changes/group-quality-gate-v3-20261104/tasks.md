# 实施与验收

集成分支 `feat/gate-v3-20261104`（基于 `c89f8e66d`），一个 PR、一次发版。各路在集成分支的子分支上开发，由集成方合入。逐项改动点、证据与回放在本地交接包，不入库。

约束：
- 推送、PR、合并、发版、部署与生产查询须经用户授权；
- 测试只覆盖本变更的规则（状态机、排序、迁出、递增、开关），不做回放、影子模式或大样本模拟；
- 改到 `tools/upstream-sync/policy.json` 登记过字面量的文件或调度差异块时，同步更新 `policy.json` 与 `scheduler-diff.json`，提交后在干净工作树上跑 `sync.py audit`；
- 每路交付前运行相关包测试、golangci-lint 与 `python3 tools/check_docs_consistency.py`。

## 0 契约（集成方）

- [x] 0.1 档位常量改名为 `GroupQualityGateTierPass`/`Retest`/`Fail`；门槛视图增加 `MigrationTarget`（v3 评估器实现前恒为 false）。
- [x] 0.2 本变更目录与 design.md §5 接口契约。

## 1 后端甲：数据列与网关韧性（WP1、WP6、WP7）

- [x] 1.1 `usage_logs.attempt_first_output_ms`：迁移 289、checksum、ent 生成、写入（OpenAI `RecordUsage`、通用 `buildRecordUsageLog`）、插入与读取、固定参数位置的测试。定向单测及四条入库路径的 PostgreSQL 往返验证通过。
- [ ] 1.2 P1-B：`session.post_output_rebind` 开关与输出后首次失败改绑。
- [ ] 1.3 同站断流暂停：`same_site_hold.enabled` 开关、进程内跟踪器、选号硬跳过与末位兜底。
- [ ] 1.4 文档：`USAGE_METRICS.md`、`GATEWAY_RESILIENCE.md`（会话行、P1-B、同站暂停、§5.3 事件）、`gateway-resilience-20261015/design.md` §10、部署 runbook 的开关位置；规格 `specs/gateway-resilience/spec.md`。
  - WP1 的字段、迁移与回退说明，以及尝试级首字规格已完成；WP6、WP7 文档待第二步。

## 2 后端乙：v3 评估器、策略与状态（WP2）

- [x] 2.1 策略 `v3` 子对象、继承、校验与 PUT 只取 `v3`；v2 字段原样保留。
- [x] 2.2 观测查询：严格续轮按 v3 重算、2 小时 B1、30 分钟账号窗口、e_G 的组内最近 100 条、首字只读新列；错误去重沿用 v2。
- [x] 2.3 评估器与状态机替换 v2（罢工、退避、试用、档位记忆、快档重测、保底），保留每分钟评估、陈旧保护、离组保留、窗口截断与恢复状态。
- [x] 2.4 视图：`OrderTier` 只对启用组返回 0/1/2，`MigrationTarget` 实现；状态接口与利润预览按 design.md §5.3。
- [x] 2.5 事件 `group_quality_gate.tier`；删除 v2 专有事件。
- [x] 2.6 文档：`GROUP_QUALITY_GATE.md` 整体改写为 v3；退役名登记；部署 runbook 的各组初始设置；规格 `specs/group-quality-gate/spec.md`。

## 3 后端丙：选号、迁出与判慢（WP3、WP4、WP5）

- [x] 3.1 否决调用点全部改为按档排序，删除 `Vetoed`、`SlowerThanFastLine` 与 质量否决函数。
- [x] 3.2 分档与低倍率优先脱钩；判慢分区五个调用点按“档 × 判慢”分段；打散、会话偏好、缓存偏好、借用路径不越档。
- [x] 3.3 低倍率优先按组范围（设置键、解析、写入、缓存刷新、运行时判断）。
- [x] 3.4 高级调度按“档 × 判慢”分池；共享调度按档排序。
- [x] 3.5 统一迁出：触发、豁免、目标条件、显式绑定、5 分钟冷却、`group_quality_gate.migrate` 字段；快照与批量空位过滤后按序 DB 复核/占槽，满载与未尝试候选不做逐号 DB 复核。
- [x] 3.6 判慢参数：30%、3/6/12/15 分钟递增、30 分钟重置；事件加 `hold_seconds`、`level`；为状态接口提供按账号的判慢查询。
- [x] 3.7 文档：`GATEWAY_RESILIENCE.md` §4.5、`account-scheduling.md`；`policy.json` 与 `scheduler-diff.json`；规格 `specs/openai-scheduling/spec.md`。

## 4 前端（WP8）

- [ ] 4.1 每组门槛设置：启用、首字上限（秒）、错误上限、缓存下限、严格模式；不展示、不改写 v2 字段。
- [ ] 4.2 状态展示：档位与状态、三维状态/数值/样本、坏记忆、判慢与到期、“本组样本不足”；账号页分组标识同步。
- [ ] 4.3 低倍率优先范围（全部 / 指定分组），空集提示等于关闭。
- [ ] 4.4 网关韧性设置：`session.post_output_rebind`、`same_site_hold.enabled`。
- [ ] 4.5 中英文 i18n；相关 spec 显式运行或加入关键清单。

## 5 集成与发版前

- [ ] 5.1 合入各路，接上状态接口的判慢字段；全量门禁（unit、`ci_gate.py`、golangci-lint、前端 lint/typecheck/test/build、文档一致性、干净树 `sync.py audit`）。
- [ ] 5.2 两路红队无阻断。
- [ ] 5.3 发版前只读核对（与开发并行）：用端到端首字中没有失败尝试的请求近似新列，复核初始值与迁出估计；≥4 周回测四条回滚线与同站暂停告警线。报告经用户确认后发版。

## 6 上线后

- [ ] 6.1 按各组初始值设置：53 组 10 秒 / 8% / 50% / 严格；18、47、50 组 15 秒 / 10% / 50%；83 组 20 秒 / 10% / 50%；其余组不开。低倍率优先范围“全部”，两个新开关开启。
- [ ] 6.2 48 小时只读核对：新列非空率 ≥95%（OpenAI 成功流式请求）、新列与判慢样本及端到端首字的差异、档位分布、“本组样本不足”占比、迁出次数（按触发原因）。偏离时只调整各组设置。
- [ ] 6.3 7 天验收：四条回滚线（端到端首字 <10 秒占比降 >2 个百分点、缓存读取占比降 >1.5 个百分点、用户可见失败率升 >0.3 个百分点、记录内估计成本每百万提示 token 升 >3%），任一连续 2 天越线按版本回滚；P1-B 双门、同站暂停告警线、判慢与新列单独评估。
