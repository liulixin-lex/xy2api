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

评估器启动时立即运行，之后每 60 秒运行一次。查询在只读事务中执行并设置 5 秒语句超时。错误率窗口为 15 分钟，分母为成功请求与软失败之和，至少 20 个请求且至少 5 个软失败才可熔断。软失败沿用韧性 TRANSIENT、首字超时、STALL 和未分类的非取消 `request_error`；429、余额、鉴权、容量削峰、模型不支持和用户取消不计入。

续轮首字 p90 使用同一会话、同一账号、前一请求间隔不超过 5 分钟的流式成功请求，窗口为 30 分钟，至少 10 个样本。续轮零缓存率窗口为 2 小时，仅统计提示 token（`input_tokens + cache_read_tokens + cache_creation_tokens`）至少 2048 的续轮，至少 20 个样本时用于快档排序；超过 30% 会降到其他档，不会熔断。

有效数据起点是窗口起点、`group_quality_reset_at`、该账号在本组最近一次熔断结束和进程启动时刻中的最晚者。熔断结束后不再使用熔断前样本，重启后不读取启动前样本；试用请求和软失败从试用开始累计，不受 35 分钟查询范围限制。查询失败或数据超过 5 分钟未更新时沿用上一轮，不新增熔断并记录 `group_quality_gate.evaluate_failed`，已有熔断仍按原到期时间结束。

## 状态、熔断与保底

状态为合格、熔断或试用。熔断退避为 5、10、20、30 分钟封顶；连续 2 小时未再次熔断时清零。到期后，新数据仍超线进入下一档；数据足够且所有指标不超过上限 80% 转合格；其他情况进入试用。试用前 20 个请求内软失败达到 2 次或满足熔断条件时再次熔断，满 20 个请求转合格。

评估结果分别给出可用、达标、固定、保底保留和状态。可用复用现有硬状态分级，并在利润门开启时检查分组利润上限；可服务由 `groupMemberServable` 统一判断，另外排除韧性软罚和质量否决。保底目标为 `min(max(2, ceil(可用数/2)), 可计入数)`；可计入成员排除固定且不达标账号。不足时按超线程度从轻到重保留账号；目标不可达时可计入成员全部保留。固定账号不被质量否决，但不达标时不计入保底分子。

档位 0 为合格快档，档位 1 为试用，档位 2 为其他。首字没有数据算快档；样本不足沿用上次档位最长 6 小时；熔断结束清除档位记忆。没有启用门槛的分组只有 0、2 档。门槛状态只影响本组选择，最后兜底是否放宽质量门槛由调度与兜底模块处理。

## 利润门与可服务数

利润门成本统一由 `openAIProfitGateCost` 计算。先校验原始 `rate_multiplier`：nil、负数、NaN、Inf 拒绝，0 合法。OpenAI API Key 使用定价时刻的新鲜探测（含上游高峰），过期或缺失时使用账号倍率；OAuth 使用现有运行时参考倍率，显式清空参考倍率时使用账号倍率。其他 token 平台沿用已校验的账号倍率。请求和抢槽后终检都使用同一个 `pricingAt`；等待期间刷新账号快照，但不改定价时刻。

OAuth 成本检查只读现有调度设置缓存，不查询数据库。缓存首次加载前使用既有默认值；过期后沿用最后发布值，由原设置加载器（共用 singleflight 与查询超时）或管理端设置更新刷新。

售价使用服务计价分组的用户覆盖倍率或默认倍率，再乘该组高峰；开关、毛利率和安全缓冲使用被调度分组。`groupProfitCeiling` 按分组默认倍率估算保底、兜底与运维可服务数，不针对特定用户。运维分组可用账号数、`group_available_accounts` 与 `group_available_ratio` 告警都由 `groupMemberServable` 计数；固定但不达标的账号免于质量否决，只要其他条件满足就计入。平台总数和账号级状态展示保持原口径。

## 管理接口

普通管理员的策略与状态读取沿用账号管理查看权限，策略写入沿用分组管理管理权限并进入审计。利润门预览沿用分组管理查看权限：

- `GET /api/v1/admin/group-quality-gate/policies`
- `PUT /api/v1/admin/group-quality-gate/policies/:group_id`
- `GET /api/v1/admin/group-quality-gate/status`
- `GET /api/v1/admin/groups/:id/profit-control-preview?margin=0.2`

状态接口的 `group_id` 可选。不带参数时返回全部已启用门槛的分组，`account_metrics` 覆盖这些分组的全部成员；带参数时只返回该组，即使未启用门槛也返回成员指标并标记 `enabled=false`。状态快照包含 `generated_at`、`stale`、账号指标、状态、熔断结束时间、原因、档位、可用、达标、固定、保底保留和实际否决结果。

利润门预览返回 `{members, schedulable, passing, ceiling}`：成员数、排除硬状态与软罚后的可调度数、其中通过利润门的数量，以及换算的上游倍率上限。预览不计质量熔断，便于单独评估利润设置；`margin` 范围 `[0,1)`，省略时使用保存值，安全缓冲按 0。上限与成本在同一当前时刻计算，即使利润门尚未开启也可预览；不保存任何配置。非法参数或不支持利润门的平台返回 400，分组不存在返回 404。

## 恢复与观测

只有 `POST /api/v1/admin/accounts/:id/recover-state` 通过 `AccountRecoveryOptions.ResetGroupQualityGate` 写入 `accounts.extra.group_quality_reset_at`，写入使用 JSONB 合并，不丢失其他字段。下一轮评估会清除该账号所有分组的熔断、清零次数并转试用。成功测试恢复和 OpenAI 额度重置不设置该选项。

事件使用统一前缀：`group_quality_gate.eject`、`group_quality_gate.trial`、`group_quality_gate.recover`、`group_quality_gate.guard_hold`、`group_quality_gate.last_resort`、`group_quality_gate.evaluate_failed`。

## 回退

关闭分组门槛即可回到原有选择行为；门槛状态是进程内快照，不写入账号调度字段、临时停用字段或成员关系。该功能不增加数据库迁移。
