# 分组质量门槛

分组质量门槛按“分组 × 账号”在进程内评估 OpenAI 账号质量。策略关闭或没有策略条目时，调度行为保持原样；状态不写入账号调度字段、临时停用字段或分组成员关系。多实例各自计算，不使用 Redis 保存门槛状态。

## 策略与默认值

设置键为 `SettingKeyGroupQualityGatePolicies`（存储名 `group_quality_gate_policies`），值是带 `version` 的 JSON：

```json
{"version":3,"groups":{"7":{"enabled":true,"max_error_rate":0.10,"max_warm_ttft_p90_ms":20000,"fast_warm_ttft_p90_ms":10000,"pinned_account_ids":[101]}}}
```

错误率默认 10%，续轮首字 p90 熔断线默认 20000 ms，快档线默认 10000 ms。快档线只参与排序，不触发熔断。错误率取值为 `(0,1]`；首字阈值为 1000–600000 ms，且快档线小于熔断线。固定账号必须是当前成员；只有 OpenAI 分组可以启用门槛。

策略写入携带 `expected_version`，版本不一致返回 409，成功后版本加一。本实例策略缓存立即失效；新启用的分组在下一轮评估后进入状态接口。

## 指标与有效数据

评估器在后台循环启动后立即运行首轮，不阻塞服务装配，之后每 60 秒运行一次；每轮整体超时为 30 秒。成员通过一次查询加载必要字段（包括未启用门槛、但需要快慢档排序的 OpenAI 分组），不加载代理或完整分组关系。指标查询固定读取最近 2 小时 5 分钟，在只读事务中执行并设置 5 秒语句超时。错误率窗口为 15 分钟，分母为成功请求与软失败之和，至少 20 个请求且至少 5 个软失败才可熔断。软失败沿用韧性 TRANSIENT、首字超时、STALL 和未分类的非取消 `request_error`；429、余额、鉴权、容量削峰、模型不支持和用户取消不计入。

续轮首字 p90 使用同一会话、同一账号、前一请求间隔不超过 5 分钟的流式成功请求，窗口为 30 分钟，至少 10 个样本。续轮零缓存率窗口为 2 小时，仅统计提示 token（`input_tokens + cache_read_tokens + cache_creation_tokens`）至少 2048 的续轮，至少 20 个样本时用于快档排序；超过 30% 会降到其他档，不会熔断。

有效数据起点是窗口起点、`group_quality_reset_at`、该账号在本组最近一次熔断结束和进程启动时刻中的最晚者。熔断结束后不再使用熔断前样本，重启后不读取启动前样本；试用状态只保存请求标识集合（最多 20 个）及这些请求的软失败标识集合。每轮重新扫描试用开始与当前时刻减去 125 分钟两者中较晚者之后的数据，请求按请求标识去重，软失败按 `(request_id, account_id, at_unix_ms)` 去重，确保晚写入的记录仍可计入。计数取集合大小；较早的已计入记录滑出窗口后仍保留在集合中，不会为长时间试用扩大查询范围。

数据新鲜度只看 `usage_logs` 的最新行（没有任何用量行时按无样本处理），错误日志的新记录不能掩盖用量写入停滞。查询失败或用量超过 5 分钟未更新时沿用上一轮指标、不新增熔断（包括试用再熔断），已有熔断照常到期转试用；成员信息可读取时仍处理管理端重置。每个连续 stale 期间只记录一次 `group_quality_gate.evaluate_failed`，携带原因及 stale 时长；恢复正常评估后才允许再次熔断。

## 状态、熔断与保底

状态为合格、熔断或试用。熔断退避为 5、10、20、30 分钟封顶；连续 2 小时未再次熔断时清零。到期后，新数据仍超线进入下一档；触发熔断的每项指标都有足够的新样本，且所有样本足够的指标不超过上限 80% 时转合格；其他情况进入试用。试用前 20 个请求内软失败达到 2 次或满足熔断条件时再次熔断，满 20 个请求转合格。

评估结果分别给出可用、达标、固定、保底保留和状态。可用复用现有硬状态分级，并在利润门开启时检查分组利润上限；可服务由 `groupMemberServable` 统一判断，另外排除韧性软罚和质量否决。保底目标为 `min(max(2, ceil(可用数/2)), 可计入数)`；可计入成员排除固定且不达标账号。不足时按熔断时保存的超线程度从轻到重保留账号；同分优先上一轮已保底保留的账号，再按账号 ID 稳定排序，无新数据不会逐轮漂移；目标不可达时可计入成员全部保留。固定账号不被质量否决，但不达标时不计入保底分子。

档位 0 为合格快档，档位 1 为试用，档位 2 为其他。首字没有数据算快档；样本不足沿用上次档位最长 6 小时；熔断结束清除档位记忆。没有启用门槛的分组只有 0、2 档。门槛状态只影响本组选择，最后兜底是否放宽质量门槛由调度与兜底模块处理。

## 管理接口

普通管理员读取接口沿用账号管理权限，策略写入沿用分组管理权限，并由后台审计中间件记录：

- `GET /api/v1/admin/group-quality-gate/policies`
- `PUT /api/v1/admin/group-quality-gate/policies/:group_id`
- `GET /api/v1/admin/group-quality-gate/status`

状态接口的 `group_id` 可选。不带参数时返回全部已启用门槛的分组，`account_metrics` 覆盖这些分组的全部成员；带参数时只返回该组，即使未启用门槛也返回成员指标并标记 `enabled=false`。状态快照包含 `generated_at`、`stale`、账号指标、状态、熔断结束时间、原因、档位、可用、达标、固定、保底保留和实际否决结果。`groups`、`accounts`、`reasons` 始终是数组（空集合为 `[]`），`account_metrics` 始终是对象（空集合为 `{}`）。`summary.eligible` 是未被否决的成员数，包含试用、固定及保底保留；`summary.ejected` 是实际否决数；`summary.trial` 单独统计试用数，属于 eligible 的子集。

每个 `groups[].accounts[]` 成员的 `metrics` 是本组判定实际使用的有效数据，包含 `calls`、`soft_failures`、`error_rate`、`warm_samples`、`warm_ttft_p90_ms`。`calls`、`soft_failures`、`warm_samples` 始终为实际计数；`error_rate=null` 表示总数 <20 或软失败 <5，未达判定条件；`warm_ttft_p90_ms=null` 表示续轮样本 <10。重启前、重置前及本组熔断结束前的旧数据按有效起点排除；过期熔断也不再显示触发熔断那轮的旧指标。

`headroom` 只使用成员 `metrics` 中非 null 的 `error_rate` 与 `warm_ttft_p90_ms`，取最小的 `1 − 实测值 / 上限`：负数表示超线，0 表示压线；两项均为 null 时返回 `null`。状态、原因、实测、上限和余量按本组有效数据展示；顶层 `account_metrics` 保持完整窗口，供账号行显示，不能用于成员门槛的实测和余量。策略校验失败沿用 `{code, message, reason, metadata}` 错误结构，`reason=INVALID_GROUP_QUALITY_GATE_POLICY`，`message` 说明具体字段约束或非成员账号，`metadata.field` 标出字段。

## 恢复与观测

只有 `POST /api/v1/admin/accounts/:id/recover-state` 通过 `AccountRecoveryOptions.ResetGroupQualityGate` 写入 `accounts.extra.group_quality_reset_at`，写入使用 JSONB 合并，不丢失其他字段。进程运行期间新出现或变化的重置时刻，在下一轮评估清除该账号所有分组的熔断、清零次数并转试用；早于或等于进程启动的历史重置只作为数据起点，不在重启后重新转试用。成功测试恢复和 OpenAI 额度重置不设置该选项。

手动重置与熔断到期转试用记录 `trial`；试用转合格、到期直接合格记录 `recover`。事件使用统一前缀：`group_quality_gate.eject`、`group_quality_gate.trial`、`group_quality_gate.recover`、`group_quality_gate.guard_hold`、`group_quality_gate.last_resort`、`group_quality_gate.evaluate_failed`。

## 回退

关闭分组门槛即可回到原有选择行为；门槛状态是进程内快照，不写入账号调度字段、临时停用字段或成员关系。该功能不增加数据库迁移。
