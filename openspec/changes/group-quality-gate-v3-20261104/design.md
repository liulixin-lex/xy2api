# 设计与接口契约

依据是 2026-11-01 至 11-03 的生产只读数据回放，以及与反方评审的五轮复核。逐账号数据与回放脚本在本地交接包，不入库。本文的数值都是已批准的定值，实施中不得自行调整。

## 1. 统计（每个进程独立，每 60 秒评估一次）

- **范围**：只统计 OpenAI 平台账号。窗口下界取窗口起点与进程启动时间中较晚者，并按 `group_quality_reset_at` 截断。不跨重启读取样本。
- **严格续轮**：同一 (`api_key_id`, `group_id`, `session_id`) 内，与上一个请求同账号、同模型、相隔 ≤5 分钟。
- **账号级**：统计属于账号，各组套用各自的标准。
- **数据陈旧**：最新用量记录距今超过 5 分钟时，本轮不投票、不改任何状态、不清坏记忆，记一次 `group_quality_gate.evaluate_failed`。

| 维度 | 样本 | 窗口与有效条件 | 越线 | 达标 | 恢复线 |
|---|---|---|---|---|---|
| 首字 | 成功的严格续轮，取 `attempt_first_output_ms`；最终尝试有失败记录的请求不算；首字截止不算 | 见下 | 预计 P90 > X | 预计 P90 ≤ X | ≤ 0.9X |
| 错误 | 每次上游尝试，含被换号救回的失败；软失败沿用 v2 口径，但移出首字截止 | 去重后最近 100 个事件且在 30 分钟内，≥20 个 | 失败率 > Y 且失败 ≥5 | 失败率 ≤ Y | ≤ 0.8Y |
| 缓存 | 严格续轮，本轮与上一轮提示都 ≥2048，且本轮 ≥ 上一轮的 50% | 30 分钟内最近 40 个，≥20 个 | 保持率 Wilson 上界 < Z | 点估计 ≥ Z | 同达标 |

- **首字预测**：预计 P90(a, g) = GM30(a) × e_G(g)。
  - 请求单元 = 模型 × 推理强度 × 提示总量档（<20k、20–60k、60–120k、≥120k）× 缓存是否保持（`cache_read` ≥ 上一请求提示的 50%）。单元无效时依次退到 (模型, 强度, 档)、(模型, 强度, 相邻两档合并)、(模型, 强度)。
  - B1 基线：近 2 小时所有 OpenAI 账号的样本，每单元先求各账号中位数，再取中位数的中位数；单元需 ≥3 个账号、≥30 个样本。
  - GM30：账号近 30 分钟样本从新到旧取，每会话最多 3 个、每账号最多 50 个；比值 = 首字 ÷ 单元 B1，封顶 4；≥10 个样本且来自 ≥3 个会话；取几何平均。
  - e_G：本组 6 小时内能对应到当前 B1 的严格续轮样本，从新到旧最多 100 条（任何账号服务的都算），≥50 条；取这些样本 B1 预期首字的 P90。不足时首字维无结论，界面显示“本组样本不足”，不借用其他组。
- **错误去重**：沿用 v2 评估器。失败按（账号，请求 ID，时刻）去重；同一请求在同一账号上既有用量又有软失败时，只记一次失败；请求 ID 为空的记录各自计数。先去重，再取最近 100 个。Wilson 区间只用于界面。

## 2. 状态机与分档

- 每个账号 × 组 × 维度，每分钟最多一票：越线、达标或中间带；没有可用统计时该维为“无结论”（`none`）。
- 该维此前为“不达标”时，达标票还必须落在恢复线以内，否则按中间带处理。
- 连续 2 次同向投票才改为 `fail` 或 `pass`；中间带不改变状态。
- **坏记忆**：该维最后一次处于 `fail` 距今 ≤2 小时，且此后没有回到 `pass`。只有该维连续两次达标才清除，一维达标不清另一维。
- **重启**清空全部状态与坏记忆。管理员“恢复状态”清空该账号各组的 v3 状态与坏记忆，并按 `group_quality_reset_at` 截断窗口。离组后状态保留 2 小时。

| 档 | 条件 |
|---|---|
| 2（不达标） | 任一维当前为 `fail` |
| 1（待复测） | 无 `fail`，且（某维 `none` 且有该维坏记忆）或（严格模式且任一维 `none`） |
| 0 | 其余，包括从未测过的号 |

- **固定账号**（v2 的 `pinned_account_ids`）：质量档固定为 0，状态显示“固定”与各维实际状态；判慢、急性保护照常；作为迁出目标不享例外。
- 只有启用 v3 的组产出档位；未启用的组 `OrderTier` 返回 0。

## 3. 选号与迁出

- **启用门槛的组**：硬准入 → 质量档 0/1/2 → 档内判慢后排 → 档内原有顺序。低倍率优先作用于本组时，档内为优先级 → 接近满载置后 → 倍率 → 同倍率按预计首字 → 负载 → 最近使用；否则为优先级 → 负载 → 最近使用并在排序组内随机打散。打散与缓存偏好不越档、不越判慢分隔。
- **未启用的组**：不分档，判慢的号排到候选末尾（现状）。
- **高级调度**：启用门槛的组按 0 未判慢 → 0 判慢 → 1 未判慢 → 1 判慢 → 2 未判慢 → 2 判慢 分池，逐池运行原有 TopK 与权重，取到即停；缓存偏好与借用路径同样按池。
- **v2 否决取消**：原调用 质量否决函数 的路径（普通选号、粘性命中、借用、高级调度、共享调度、占槽后复核）改为按档排序；运维可用性与兜底分组判定视为“未被否决”。
- **迁出触发**：绑定号对本组为 2 档，或在本模型上正处判慢让位。豁免：续链、WebSocket、指定传输方式、`withOpenAIStickyMigrationGuard`、`qualityOwnsSticky`、`OpenAIQualityTurnPinned`。绑定在固定账号上的会话不豁免判慢迁出。
- **迁出目标**：通过硬准入、未判慢、有空位，且 `MigrationTarget` 为真：
  - 普通组：首字已测到且预计 P90 ≤ 0.9X；错误、缓存没有当前 `fail`，也没有坏记忆；
  - 严格组：三维都已测到，且都在恢复线以内（首字 ≤0.9X、错误 ≤0.8Y、缓存点估计 ≥Z）。
  - 先用快照做质量、判慢与请求兼容过滤，再批量过滤满载；按档内顺序逐个做 DB 复核及占槽，第一个同时通过的就是目标，不对未尝试或满载候选做逐号 DB 复核。候选复核或占槽失败时尝试下一个，全部失败保留原绑定。直接 CAS 原子绑定选中的目标；CAS 失败或报错时释放目标槽位并放弃本次迁出。
- **冷却**：质量迁出成功后，同组同会话 5 分钟内不再质量迁出，只在改绑成功后登记。失败类改绑（P1-B、失败连击、同站暂停、硬冷却）不受限。
- **共享调度**只接入分档排序，不迁出。

## 4. 急性保护

- **判慢让位**：按账号 × 模型，EWMA τ=5 分钟，尝试级可见首字 ≥10 秒记为慢，有效加权样本 ≥6，慢占比 ≥30% 触发。距该账号 × 模型上次让位结束 ≤30 分钟再次触发时，时长依次 3 → 6 → 12 → 15 分钟（封顶），否则从 3 分钟开始。让位期间继续累积样本，触发时清零。到期只解除后排，不改 v3 状态。参数为代码常量。
- **同站断流暂停**：站点 = `GetOpenAIBaseURL()` 的主机名（空值回落官方地址）。只计 `resilienceStreamBrokenAfterOutput` 入口的传输层断流（读流出错、输出后缺终态），中转流内失败不计；同号 3 秒内合并。某账号断流时，若同站另一账号此前 60 秒内也断过流，对这些断流账号中尚未暂停的各暂停 300 秒；暂停期间再断流不延长。暂停与慢失败冷却同级：普通选号跳过，末位兜底仍可用；绑定会话下次请求改绑。同站没有断流的账号不受影响。
- **P1-B**：仅 OpenAI 平台账号（`PlatformOpenAI`，不含 Anthropic、Grok 等其他平台）输出后失败（流内 `response.failed`、读流出错、缺终态）第一次发生，就执行与连击达到阈值相同的改绑：按属主删除绑定、记会话避开（`session.avoid_seconds`，缺省 600 秒）、标记会话、加入本请求避开集合。沿用 `ReportStreamFailure` 的 attempt 标识去重：HTTP 请求内同一失败只动作一次，WebSocket 连接的每个 turn 各自可触发一次。输出前失败及其他平台的输出后失败仍按原连击阈值。质量路由持有的绑定不越权删除，避开照常生效，末位兜底不变。

## 5. 接口契约（各工作包共用，名字不得改）

### 5.1 数据与 Go 命名

| 项 | 名字 |
|---|---|
| `usage_logs` 新列 | `attempt_first_output_ms` BIGINT NULL，无 DEFAULT、无索引、不回填 |
| ent 字段 / Go 字段 | `attempt_first_output_ms` / `UsageLog.AttemptFirstOutputMs *int64` |
| 韧性请求状态方法 | `AttemptFirstOutputMs()`，与 `EndToEndFirstOutputMs()` 并列 |
| 迁移 | `backend/migrations/289_usage_log_attempt_first_output.sql` |
| 档位常量 | `GroupQualityGateTierPass` = 0、`GroupQualityGateTierRetest` = 1、`GroupQualityGateTierFail` = 2 |

调度消费的门槛视图（`backend/internal/service/group_quality_gate.go`）最终形态：

```go
type GroupQualityGateView interface {
	Enabled(groupID int64) bool
	OrderTier(groupID, accountID int64) int        // 启用组返回 0/1/2，未启用组返回 0
	MigrationTarget(groupID, accountID int64) bool // §3 目标条件中的质量部分
	ExpectedP90Ms(groupID, accountID int64) (ms int, ok bool) // 预计 P90 = GM30 × e_G，供同倍率排序；不可用时 ok=false
}
```

- 判慢查询（调度侧提供，状态接口消费）：`OpenAIGatewayService.OpenAIAccountLatencyHoldStatus(accountID int64, now time.Time) (until time.Time, models []string)`；门槛服务通过 `SetLatencyHoldLookup` 接收同签名函数。
- 首字单元退化第 3 级的“相邻两档合并”固定为 [<20k, 20–60k] 与 [60–120k, ≥120k]（与回放一致）。

`Vetoed` 与 `SlowerThanFastLine` 随 v2 逻辑删除。

### 5.2 门槛策略（设置键 `group_quality_gate_policies`）

每组策略增加子对象 `v3`；v2 字段（`enabled`、`max_error_rate`、`max_warm_ttft_p90_ms`、`fast_warm_ttft_p90_ms`、`pinned_account_ids`）读写都原样保留，v3 永不写入 v2 字段。

```json
"v3": {
  "enabled": true,
  "max_ttft_p90_seconds": 15,
  "max_error_rate": 0.10,
  "min_cache_keep_rate": 0.50,
  "strict": false
}
```

| 字段 | 范围 | 缺省 |
|---|---|---|
| `enabled` | 布尔 | 没有 `v3` 子对象时继承 v2 的 `enabled` |
| `max_ttft_p90_seconds` | 5–60 的整数 | 15 |
| `max_error_rate` | 0.05–0.50 | 0.10 |
| `min_cache_keep_rate` | 0.50–0.70 | 0.50 |
| `strict` | 布尔 | false |

- Go 中用指针区分“缺失”与显式关闭；有子对象时一律按子对象的值。
- `GET /admin/group-quality-gate/policies` 每组返回生效的 `v3`（缺失时按继承规则填好），不写库。
- `PUT /admin/group-quality-gate/policies/:group_id` 请求体沿用 `{expected_version, policy}`，服务端只取 `policy.v3`（必填），v2 字段一律取库中已存值（组没有条目时 v2 字段为零值，回滚后 0.5.1 视为未启用）。版本不一致仍返回 409。

### 5.3 门槛状态（`GET /admin/group-quality-gate/status`）

```json
{
  "generated_at": "RFC3339",
  "stale": false,
  "groups": [{
    "group_id": 18,
    "enabled": true,
    "policy": { "enabled": true, "max_ttft_p90_seconds": 15, "max_error_rate": 0.10, "min_cache_keep_rate": 0.50, "strict": false },
    "expected_p90_ms": 9100,
    "expected_samples": 87,
    "summary": { "passing": 3, "unknown": 2, "retest": 1, "failing": 1, "pinned": 0 },
    "accounts": [{
      "account_id": 101,
      "status": "passing",
      "tier": 0,
      "pinned": false,
      "latency_hold": { "until": "RFC3339", "models": ["gpt-5.5"] },
      "ttft":  { "state": "pass", "value": 9800, "factor": 1.08, "samples": 23, "sessions": 6, "bad_until": null },
      "error": { "state": "pass", "value": 0.04, "samples": 80, "failures": 3, "wilson_low": 0.013, "wilson_high": 0.105, "bad_until": null },
      "cache": { "state": "none", "value": 0.82, "samples": 14, "wilson_low": 0.59, "wilson_high": 0.94, "bad_until": null }
    }]
  }]
}
```

- `expected_p90_ms` 为 e_G，不可用时为 null（界面显示“本组样本不足”），`expected_samples` 为实际条数。
- `status`：`passing`（三维都是 `pass`）、`unknown`（0 档但有维 `none`）、`retest`（1 档）、`failing`（2 档）、`pinned`（固定账号）。`unknown` 不得显示为达标。
- 维度 `state`：`pass`、`fail`、`none`。`value`：首字为预计 P90 毫秒，错误为失败率，缓存为保持率点估计；没有统计时为 null。`factor` 为 GM30。`bad_until` = 最后一次 `fail` + 2 小时，坏记忆已清除时为 null。
- `latency_hold`：该账号正在判慢让位的模型与最晚到期时刻，没有时为 null。到期不得显示为“已恢复”。
- `GET /admin/groups/:id/profit-control-preview` 不变：`passing` 仍是通过利润门的成员数，与质量门槛无关。
- 策略校验失败的 400 在 `metadata.field` 中用 `v3.<字段>`（如 `v3.max_error_rate`）。

### 5.4 系统设置：低倍率优先范围

| 设置键 | 取值 | 缺省 |
|---|---|---|
| `openai_low_upstream_rate_priority_enabled` | 原全局开关 | 不变 |
| `openai_low_upstream_rate_priority_scope` | `all` / `groups` | `all` |
| `openai_low_upstream_rate_priority_group_ids` | 分组 ID 的 JSON 数组 | `[]` |

管理端设置接口用同名字段（`scope` 为字符串，`group_ids` 为数字数组）。`groups` 且集合为空等于不启用。生效前提不变：高级调度关闭且使用上游 token 成本。

### 5.5 网关韧性设置（`gateway_resilience_settings`）

| JSON 路径 | 缺省 | 关闭后 |
|---|---|---|
| `session.post_output_rebind` | true | 回到现状；`session.enabled` 关闭时一并失效 |
| `same_site_hold.enabled` | true | 不再新建暂停，已有暂停同时失效 |

管理接口严格解码、拒绝未知键，前后端同版发布。

### 5.6 事件（Warn，写入 `ops_system_logs`，`message` 为事件名）

| 事件 | 何时 | `extra` 字段 |
|---|---|---|
| `group_quality_gate.tier`（新增） | 账号在某组档位改变 | `account_id`、`group_id`、`from_tier`、`to_tier`、`dimension`、`value` |
| `group_quality_gate.migrate`（沿用） | 质量迁出成功 | `session_hash`（前缀）、`group_id`、`from_account_id`、`to_account_id`、`trigger`（`tier` 或 `latency_hold`） |
| `group_quality_gate.last_resort`、`group_quality_gate.evaluate_failed` | 沿用 | 沿用 |
| `openai_account_latency_hold_started`（沿用） | 判慢开始 | 现有字段，加 `hold_seconds`、`level` |
| `resilience.same_site_hold`（新增） | 每个账号每次暂停 | `host_hash`、`account_id`、`peer_account_id`、`until` |
| `resilience.session_rebind`（沿用） | P1-B 改绑 | 现有字段，`reason` = `post_output_first_failure` |

旧版硬熔断、观察、恢复与质量保底专有事件退役；精确名称登记在 `tools/docs_consistency.json`，仅历史记录保留。

## 6. 兼容与回滚

- **升级**：组没有 `v3` 子对象时，启用状态继承 v2，其余取缺省值；升级后不出现门槛空档。
- **回滚**：v2 字段未被改动，0.5.1 按原 v2 设置运行，忽略 `v3` 子对象与新列。只有 `v3` 子对象的组，在 0.5.1 中视为未启用。低倍率优先若只选了部分分组，回滚前先决定旧版全局开关。
- **P1-B 与同站暂停**：关闭开关即回到现状。
- **重启与多实例**：v3 状态、判慢及其递增计数、同站暂停、会话避开都在进程内，重启清空；各实例从共享数据库独立评估。
