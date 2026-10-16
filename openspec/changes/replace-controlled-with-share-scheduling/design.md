# 设计：剔除智能可控调度，新增份额调度，保留 Sub2API 原版

> 现状与调研见 [`docs/SCHEDULING_ANALYSIS.md`](../../../docs/SCHEDULING_ANALYSIS.md)。仿真见 [`simulation/RESULTS.md`](simulation/RESULTS.md)：第一部分验证份额与最小扰动，第二部分验证缓存命中与首字延迟。本文中的文件路径均相对仓库根目录。
>
> 2026-10-10 修订：围绕“不降低缓存命中率与首字延迟（TTFT）”重写了亲和、溢出、回升与排空规则（§6.2–§6.8），新增首字预算（§12）、缓存与首字观测、影子运行与门禁（§14）。修订依据是离散事件仿真：初版规则相对原版缓存命中率低 3.3–6.5 个百分点，修订后与原版持平，TTFT P95/P99 更低。

## 1. 目标

| 目标 | 可验收的含义 |
|---|---|
| 人为可控 | 分组的层级、权重、排空是唯一的流量意图；层级严格；层内新会话份额与权重成比例；任何偏离都有原因码 |
| 缓存与首字不退化 | 同一负载下，上游缓存读占比不低于原版，TTFT P50/P95 不高于原版；由仿真（§14.1）与灰度门禁（§14.4）兑现 |
| 智能决策 | 自动机制只排除真实不可用的账号（复用原版闸门），或在管理员给定区间内缩放新会话权重；全部可解释、可关闭 |
| 可维护 | 新调度不包装上游 HTTP、不改故障转移语义、不在热路径读数据库；与上游同步的冲突面只剩少量分发钩子 |
| 可回退 | 全局开关与分组开关都能立即回到 Sub2API 原版，且回退本身不迁移任何会话（复用同一套粘性键）；配置异常时自动回落原版而非 503 |

## 2. 总体结构

```
请求 → handler（原版故障转移循环，不改）
        └─ Select*（原版入口函数）
             ├─ [钩子] shareScheduler.Route(ctx, ...) ── handled? ── 是 → 份额调度结果
             └─ 否 → Sub2API 原版算法（逐字保持上游）
```

- 调度器选择**按分组**：分组策略的 `scheduler ∈ {sub2api, shadow, share}`，默认 `sub2api`；`shadow` 由原版选号、份额调度只计算并记录“它会怎么选”（§14.3）；全局开关 `share_scheduling_enabled` 关闭时所有分组走原版。
- 份额调度复用原版的候选列表（调度快照）、资格判定函数、并发槽、会话数限制、粘性存储、利润门与 hydrate；只替换“在合格账号中如何排序、何时等待、何时改绑”。
- 份额调度分为纯算法包 `backend/internal/sharesched`（不依赖 service 类型，便于穷举测试）和服务适配层（每个入口一个小文件）。

## 3. 控制契约

份额调度对管理员承诺以下性质，并以测试与报表兑现：

- **C1 层级严格**：只要某层存在“合格且有容量”的账号，就不会把新会话或无会话请求放到更低层；例外只有显式配置的“满载溢出”和“失败降层”，并逐次记录原因。已绑定在低层的会话按回升策略（§6.7）迁回，默认在缓存变冷时迁回。
- **C2 新会话份额**：同一层合格账号之间，新会话按 `有效权重 / 有效权重之和` 分配。确定性测试中 1 万个以上会话的份额误差不超过 1 个百分点（仿真 20 万会话误差 ≤0.1 个百分点）。
- **C3 亲和稳定**：绑定会话只在三类情况下改绑，并记录原因：
  1. 硬不可用：绑定账号不合格且预计不会在缓存过期前恢复。
  2. 满载溢出：等待预算用尽后改绑到会话的 HRW 次选，缓存随会话走。
  3. 缓存已冷：会话空闲超过缓存寿命，或没有可缓存前缀，此时向理想账号重对齐。

  另有管理员显式设定的截止时间（排空到期、可选的回升截止）。
- **C4 最小扰动**：账号退出时只有它的会话被重新放置，且按其余账号权重比例分散；改权重立即作用于新会话，已有会话只在缓存变冷时重对齐，迁移量等于理论最小值。
- **C5 偏差可解释**：最终账号不同于“理想账号”时，记录理想账号被跳过的原因码。理想账号指配置最高层、配置权重下的 HRW 首选。报表按账号汇总“目标份额 / 实际份额 / 原因分布”。
- **C6 自动机制有界**：闸门只能排除账号；纠偏只能在 `[下限, 上限]` 区间内缩放新会话权重；不存在隐藏评分、随机 Top-K 或进程内私有统计影响选择。
- **C7 可用性**：热路径不新增数据库查询；策略缓存不可用时使用最后已知版本；冷启动拿不到策略的分组按原版调度；调度器内部错误不返回 503，而是回落原版并告警。
- **C8 份额精度的边界**：请求份额与新会话份额接近；按成本统计时，重尾大会话会造成可见波动（仿真均值约 6–8 个百分点）。需要硬封顶时，使用账号窗口费用、额度阈值等原版硬闸门。
- **C9 缓存保护**：份额调度自身的策略动作不迁移缓存仍热的会话。策略动作包括改权重、回升、重对齐、纠偏和切换调度器。唯一的例外是管理员设定的截止时间，到期迁移按会话错峰。调度器不修改请求体。
- **C10 首字保护**：选择阶段的 Redis 往返次数不多于原版同一路径，不新增数据库往返。绑定账号满载时的等待受缓存价值约束，上限 15 秒（原版粘性排队上限 120 秒）。故障转移次数、同账号重试与冷却沿用原版。

## 4. 配置模型

### 4.1 分组策略（每组一条，带版本 CAS）

```json
{
  "schema": 1,
  "scheduler": "share",
  "accounts": [
    {"account_id": 244, "tier": 1, "weight": 50, "cache_domain": "anthropic-ws-a"},
    {"account_id": 281, "tier": 1, "weight": 30, "cache_domain": "anthropic-ws-a"},
    {"account_id": 317, "tier": 2, "weight": 100, "drain": false}
  ],
  "defaults": {"tier": "account_priority", "weight": "concurrency"},
  "affinity": {
    "enabled": true,
    "key": "auto",
    "ttl_seconds": 3600,
    "wait": {"mode": "auto", "ms_per_1k_tokens": 100, "max_ms": 15000, "max_waiting": 3},
    "on_spill": "rebind",
    "soft_spill_max_seconds": 300
  },
  "cache": {"ttl": "auto", "realign": "cold"},
  "tiers": {"on_saturated": "spill", "on_failed": "next_tier", "return": "cold", "stable_seconds": 120, "return_deadline_seconds": 0},
  "balance": {"unit": "sessions", "correction": "off", "window_minutes": 60, "min_volume": 50}
}
```

| 字段 | 含义 | 范围 / 默认 |
|---|---|---|
| `tier` | 层级，数字越小越优先（与原版 priority 方向一致） | int32；未配置账号默认取账号 `priority` |
| `weight` | 层内权重，决定新会话份额 | 1–10000；未配置账号默认取账号并发上限 `concurrency`（与原版负载率均衡的稳态比例一致，避免小账号被分到超出容量的份额） |
| `drain` | 排空：不再接新会话，已绑定会话在缓存变冷时迁走，`drain_until` 到期后强制迁移（错峰 10 分钟） | 默认 false；可选 `drain_until` |
| `cache_domain` | 缓存域：确认共享上游缓存的账号填同一标识，例如同一 Anthropic workspace、同一 OpenAI 组织与区域、同一 Bedrock 账号与区域、同一 Vertex 项目。溢出与故障转移优先同域 | 默认每个账号自成一域 |
| `affinity.enabled` | 是否会话亲和；图片/向量等无状态分组可关闭，改为逐请求按权重分配 | 默认 true |
| `affinity.key` | 亲和键来源（§6.2） | `auto`（默认）/ `session` / `prefix` / `none` |
| `affinity.ttl_seconds` | 绑定空闲过期时间，命中续期 | 300–86400，默认 3600（与原版粘性一致，覆盖 1 小时缓存） |
| `affinity.wait` | 绑定账号满载时的等待预算（§6.5） | `auto`：`min(max_ms, 预计可读缓存 Token/1000 × ms_per_1k_tokens)`；`fixed`：固定 `max_ms`；`off`：不等 |
| `affinity.on_spill` | 等待预算用尽后溢出到会话 HRW 次选时是否改绑 | `rebind`（默认，缓存随会话走）/ `keep`（只临时溢出） |
| `affinity.soft_spill_max_seconds` | 绑定账号短时不可用且预计在此时长内恢复时，只临时溢出不改绑（§6.6） | 0–1800，默认 300 |
| `cache.ttl` | 判定“缓存已冷”的阈值（§6.4） | `auto`（按协议与请求声明）或固定秒数 |
| `cache.realign` | 缓存变冷时把会话重对齐到当前理想账号 | `cold`（默认）/ `off` |
| `tiers.on_saturated` | 本层合格账号全部满载时 | `spill`（默认，溢出下一层）/ `wait`（按原版兜底排队等待本层） |
| `tiers.on_failed` | 本层合格账号在本请求内都失败时 | `next_tier`（默认）/ `stop` |
| `tiers.return` | 会话绑定在低层而高层恢复时 | `cold`（默认，高层稳定 `stable_seconds` 后，在会话缓存变冷时迁回）/ `immediate` / `session_end` |
| `tiers.return_deadline_seconds` | 回升强制截止，0 表示不设；设置后到期仍未迁回的会话按会话错峰 10 分钟强制迁回 | 0 或 600–86400，默认 0 |
| `balance.unit` | 报表与纠偏使用的份额单位 | `sessions`（默认）/ `requests` / `tokens` / `standard_cost` |
| `balance.correction` | 新会话权重纠偏强度 | `off`（默认）/ `gentle`（γ=0.5，区间 0.5–2）/ `strong`（γ=1，区间 0.25–4）；`unit=sessions` 时必须为 off |

保存时校验：
- 账号唯一且属于该分组。组 0 在标准模式下为未分组账号，在简易模式下为全部账号，沿用原版成员规则。
- 启用时至少要有一个未排空账号，否则拒绝并提示。
- `cache_domain` 内的账号必须同一平台。
- JSON 拒绝未知字段与尾随内容。
- `expected_version` CAS 冲突时返回 409，并保留前端草稿。

### 4.2 全局设置（settings 表，进程内缓存 5 秒 + 变更广播）

- `share_scheduling_enabled`：总开关，默认 false。关闭即全部分组走原版（一键回退）。
- 默认值模板：新建分组策略时使用的 `affinity/cache/tiers/balance` 默认值。
- 观测设置：缓存归因采样率（默认 1/4 会话）、门禁阈值与是否自动回退（默认只告警，§14.4）。

## 5. 选择流水线

```text
Route(ctx, entry, group, model, req, excluded, needSlot):
  0. 分发：总开关关 / 无策略 / scheduler=sub2api → handled=false（走原版）
           scheduler=shadow → handled=false，另起无副作用计算并记录（§14.3）
  1. 协议强续接（语义与原版完全一致，不经过份额算法）：
       OpenAI previous_response_id 不可迁移、guardian 父账号、Grok 媒体任务、WS 同一轮
  2. 亲和键与缓存画像（§6.2、§6.4）：键来源、预计输入 Token、缓存寿命阈值
  3. 候选：原版 listSchedulableAccounts(group, platform)（调度快照）
       + 原版窗口费用 / RPM 批量预取
       + 分组模型路由命中时先限定到路由账号（全部不合格时回到全体，记 model_routing_fallback）
  4. 资格：对每个候选调用原版判定函数，得到 ok 或原因码（§7）；
       绑定账号按“粘性请求”判定窗口费用 / RPM 的“仅粘性可用”区间；
       不合格时附带预计恢复时间（rate_limit_reset_at / overload_until / temp_unschedulable_until）
  5. 读绑定：一个管道内 GET + PTTL（与原版 GET 同一次往返），得到绑定账号与空闲时长
  6. 计划（纯函数 sharesched.Plan）：
       a. 规则与有效权重（缺省值、纠偏、排空=0）；活动层；会话 HRW 次序（同域优先的溢出次序）
       b. 有绑定：
            硬不可用              → 重新放置（rebind_unavailable）
            短时不可用            → 软溢出到 HRW 次选，不改绑（soft_spill）
            缓存已冷且不是理想账号 → 重对齐（realign_cold / return_cold / drain_cold）
            到达管理员截止         → 重新放置（return_deadline / drain_deadline）
            否则                  → 绑定账号（affinity_hit）
       c. 无绑定：活动层 HRW 首选（new_session）；无亲和键时按权重随机（sessionless）
       d. 理想账号 = 配置最高层、配置权重下的 HRW 首选（只用于偏差归因）
  7. 执行：
       - 目标账号 tryAcquireAccountSlot（原版）成功 → checkAndRegisterSession → 复检 / hydrate / 利润门（原版）
       - 绑定账号满载：等待预算 > 0 且排队人数 < max_waiting → 返回原版等待计划（Timeout=预算），
         超时后同一请求以 no_wait 重入，溢出到 HRW 次序中第一个有空位的账号，并按 on_spill 改绑
       - 新会话首选满载 → 同层 HRW 次序 → on_saturated 降层；都满 → 原版兜底等待计划
       - 绑定写入：新会话 SET NX；改绑覆盖；软溢出不写；命中续期（与原版相同的 EXPIRE）
       - 决策上下文挂在请求上，结算时用于缓存归因（§14.2），统计异步写出
  8. 无可用账号：返回带原因汇总的 ErrNoAvailableAccounts（原版语义）
```

故障转移不改。handler 在首字前失败时，把账号加入排除集后再次调用 `Select*`。份额调度把排除集视为原因码 `excluded_in_request`，下一个账号按会话的溢出次序（§6.8）确定：同域优先、本层其次，本层用尽后由 `on_failed` 决定是否降层。最大切换次数、同账号重试、临时冷却与强制缓存计费全部沿用原版。

## 6. 关键算法

### 6.1 加权会话一致性哈希（HRW，对数法）

```go
// u ∈ (0,1]：对 (salt, affinityKey, accountID) 做 64 位哈希（固定算法，跨实例一致）
score(account) = -ln(u) / effectiveWeight(account)   // 取最小者，按升序得到完整回退次序
```

- 被选概率严格为 `w_i / Σw`；账号退出只影响其自身会话；改权重的迁移量等于理论最小值（仿真 13.27% vs 13.33%）。
- `salt = group_id`：不同分组相互独立，避免同一批会话在多个分组同时压到同一账号。
- 多实例无需共享计数器：相同输入在任何节点算出相同次序，首请求并发到两个节点也会选同一账号。

### 6.2 亲和键：跨轮稳定、不跨租户

缓存只在“同一账号或同一缓存域 + 完全相同的前缀”上命中，所以亲和键必须在一次对话的各轮之间保持不变。`affinity.key=auto` 时按序取：

1. **协议强续接**（不变）：`previous_response_id`、WS 同一连接/轮次、guardian、媒体任务。
2. **显式会话标识**（与原版同一哈希，切换调度器不丢绑定）：
   - Anthropic：`metadata.user_id` 中的 session。
   - OpenAI：原版已识别的 `session_id` / `conversation_id` / `x-session-affinity` / `x-session-id` 等会话头，以及 `prompt_cache_key`。
3. **对话锚点**：`hash(api_key_id, 系统提示/instructions 文本, 工具定义摘要, 第一条用户消息)`。
   - 只在份额调度分组中替换 Anthropic 原版的两级回退：带 `cache_control` 的内容、全部消息摘要。
   - 原版这两级在客户端移动缓存断点时会逐轮变化；只给 system 打断点时，又会让不同 API Key 的会话共用一个哈希。
   - OpenAI 原版的内容回退（模型 + 系统 + 工具 + 首条用户消息）本身已跨轮稳定，保持不变。
4. 无可用内容：无会话请求，按权重逐请求随机（`sessionless`）。

`affinity.key=prefix` 用于批处理、RAG 等“共享大前缀、每次用户消息不同”的分组。此时键只取 `(api_key_id, 系统提示, 工具定义)`，同前缀请求集中到同一账号，热点由满载溢出分散。`session` 只用第 1–2 级；`none` 关闭亲和。原版分组的会话哈希一字不改。

### 6.3 绑定生命周期

| 状态/事件 | 判定 | 动作 | 原因码 |
|---|---|---|---|
| 命中 | 绑定账号合格且有空位 | 使用，`EXPIRE` 续期 | `affinity_hit` |
| 满载、缓存有价值 | 等待预算 > 0 且排队 < `max_waiting` | 原版等待计划，超时按下一行处理 | `affinity_wait` |
| 满载、等待用尽或无价值 | — | 溢出到 HRW 次序（同域优先），`on_spill=rebind` 时改绑 | `spill_rebind` / `spill_keep` |
| 短时不可用 | 有恢复时间，剩余 ≤ `soft_spill_max_seconds`，且 `剩余 + 空闲 < 0.8 × 缓存寿命` | 溢出，不改绑，恢复后回到原账号仍可命中 | `soft_spill` |
| 硬不可用 | 不满足上一行（停用、不可调度、额度超、模型不支持、移出分组、长时间限流） | 重新放置 | `rebind_unavailable` |
| 缓存已冷 | 空闲 ≥ 缓存寿命，或预计输入低于模型最小可缓存长度 | 若当前理想账号不是绑定账号，则重对齐 | `realign_cold` / `return_cold` / `drain_cold` |
| 管理员截止 | `drain_until` 或 `return_deadline_seconds` 到期，按会话哈希错峰 0–600 秒 | 重新放置（明知缓存会冷启动） | `drain_deadline` / `return_deadline` |
| 失败换号 | handler 排除 | 溢出次序中的下一个；成功后改绑 | `failover_next` |
| 管理员重平衡 | 显式操作 | 清除绑定，下次请求重新放置 | `admin_rebalance` |

### 6.4 缓存冷热判定

- **空闲时长**：原版每次命中都把粘性键 `EXPIRE` 为完整 TTL，于是 `空闲 = affinity.ttl_seconds − PTTL`。读取绑定时把 `GET` 与 `PTTL` 放在同一个管道里，不增加往返，也不改变键格式。缓存寿命从“读写缓存的请求开始”计算，正好与选号时刻续期对齐。软溢出与满载溢出也会续期，这时空闲时长偏小，判定更保守，不会误迁热会话。
- **缓存寿命**（`cache.ttl=auto`，取保守上界，宁可晚迁不误迁）：

| 平台 / 请求 | 阈值 | 依据 |
|---|---|---|
| Anthropic 系（含 Bedrock、Vertex、透传）出站请求中出现 `"ttl":"1h"` 的 `cache_control` | 3600 秒 | 官方 1 小时缓存；订阅版 Claude Code 默认请求 1 小时 |
| Anthropic 系其他 | 300 秒 | 官方默认 5 分钟，命中续期 |
| OpenAI（含 Codex、Responses、Chat） | 1800 秒 | GPT-5.6 及以后固定 30 分钟；更早模型的扩展保留“通常约 30 分钟” |
| Gemini 隐式缓存 | 300 秒 | 官方未给出隐式缓存寿命，取与 Anthropic 默认相同的保守值 |
| 其他 / 未识别 | 300 秒 | — |

- **缓存价值**：用请求体估算输入 Token，复用现有冷写预留的估算函数。低于模型最小可缓存长度时，会话没有可保护的缓存，按“已冷”处理：可以随时重对齐，满载也不等待。各家门槛为：Anthropic 512–4096，OpenAI 1024，Gemini 1024–4096；未知模型取 1024。
- Claude 缓存补齐功能（按分组注入 5 分钟断点）作用于最终出站请求体，TTL 判定读取的就是补齐后的请求体。

### 6.5 绑定账号满载：等待预算与溢出改绑

```text
est = 估算输入 Token（绑定账号缓存仍热时），否则 0
budget = min(max_ms, est / 1000 × ms_per_1k_tokens)       （est < 最小可缓存长度时为 0）
budget > 0 且 排队人数 < max_waiting → 等待绑定账号（原版等待计划机制）
否则或超时 → 溢出到 HRW 次序第一个有空位的账号；on_spill=rebind 时改绑
```

- 依据：等待的收益是避免整段前缀重算，节省的首字时间约与缓存长度成正比。Anthropic 发布时的数据是 10 万 Token 缓存后首字从 11.5 秒降到 2.4 秒，约 90ms/1k。等待还避免一次 1.25×（1 小时为 2×）的缓存写入。默认 100ms/1k、上限 15 秒；阶段 0 用 `usage_logs` 中首字与未缓存输入的回归斜率校准。
- 原版的粘性排队不看缓存价值，一律最多等 120 秒。份额调度在短会话上不等、在长上下文上有界地等，所以尾延迟更低。
- **为什么溢出要改绑**：只临时溢出不改绑（初版的 `wait_ms=0`）会让会话在溢出账号上冷启动一次，下一轮回到原账号时又丢失溢出期间新增的前缀，形成往返颠簸。仿真中这是初版缓存命中率比原版低 3.3–6.5 个百分点的主因（RESULTS 第二部分）。改绑后缓存随会话走，满载账号也自然卸掉负载。会话回到理想账号由 §6.7 的冷窗口重对齐完成，不产生热迁移。

### 6.6 短时不可用：软溢出

绑定账号进入 429/529/临时不可调度时，原版直接删除粘性绑定（`shouldClearStickySession`）。份额调度先判断：

- 预计恢复时间已知、剩余不超过 `soft_spill_max_seconds`（默认 300 秒），并且恢复时原账号上的缓存仍未过期，即 `剩余 + 空闲 < 0.8 × 缓存寿命`，则只溢出、保留绑定。恢复后会话回到原账号，继续读取原缓存。
- 否则视为硬不可用，重新放置并改绑，避免恢复后再迁回造成第二次冷启动。

仿真中，默认 300 秒的上限让短时限流（3 分钟）走软溢出，整层限流（10 分钟）走改绑，两种缓存寿命下都与原版持平。

### 6.7 冷窗口重对齐：回升、排空、改权重

会话请求到达时，如果缓存已冷（§6.4）且绑定账号不是当前理想账号，就在本次请求重新放置。理想账号指活动层合格、未排空、按有效权重算出的 HRW 首选。此时迁移没有缓存损失。同一机制统一处理：

- **回升高层**：`tiers.return=cold` 时，高层稳定 `stable_seconds` 后，低层会话在各自的冷窗口迁回；`return_deadline_seconds` 为可选的强制截止，只从高层已知的恢复时间起算（§20）。`immediate` 会立即迁移（明知冷启动），`session_end` 则一直保持到绑定过期。
- **排空**：不再接新会话；已有会话在冷窗口迁走，`drain_until` 到期后强制迁移。
- **改权重、纠偏、满载改绑后的漂移**：都在冷窗口收敛回目标份额。

代价是回升有滞后。5 分钟缓存下，会话停顿超过 5 分钟就会迁回；1 小时缓存下，多数会话会在原账号上自然结束。仿真中，1 小时缓存场景整层故障恢复后，第 2 层的滞留请求占健康期请求的 16%（原版 18%）。需要更快回升的分组设置 `return_deadline_seconds`，每次强制迁移的成本在预览中按“前缀 Token × (写入价 − 读取价)”估算。仿真中 30 分钟截止的代价约为缓存命中率降低 0.8–1.1 个百分点。

### 6.8 缓存域与溢出/故障转移次序

会话的溢出与失败次序在 HRW 完整次序的基础上稳定分区：

1. 与绑定账号同一 `cache_domain`：缓存共享，换号不冷启动。
2. 同平台、同账号类型：避免在 Anthropic 直连、Bedrock、Vertex、Antigravity、上游中转之间切换，触发 thinking 签名整流重试与前缀改写。
3. 其余账号，各区内保持 HRW 次序。

同域内改绑不计为迁移。默认每个账号自成一域，这一规则等同于“同类优先的 HRW 次序”。

### 6.9 有界纠偏（可选）

```text
目标 t_i = w_i / Σ(本层合格账号 w)
实际 r_i = v_i / Σ v           （v = 窗口内该账号在本组的请求数 / Token / 标准成本）
系数 c_i = clamp((t_i / max(r_i, ε))^γ, 下限, 上限)
有效权重 = w_i × c_i            （只影响新会话、无会话请求与冷窗口重对齐）
```

- 数据来源：后台任务每 30 秒按分组聚合 `usage_logs`（已有 `(group_id, created_at)` 索引）。Token = 输入 + 输出 + 缓存写 + 缓存读；标准成本 = `total_cost`（未乘账号倍率，与原版窗口费用口径一致）。各实例独立计算，结果相同到秒级。
- 窗口内总量低于 `min_volume` 时不纠偏。仿真显示，纠偏可把请求份额偏差从 1.5% 降到 1.0%，轻尾成本从 2.3% 降到 1.6%，对重尾成本作用有限（§3 C8）。

### 6.10 无会话请求与关闭亲和

无亲和键或 `affinity.enabled=false` 时，每个请求用随机键做 HRW，等价于按有效权重随机，统计上收敛到权重比例（仿真逐请求偏差 0.3%）。仿真中逐请求分配的缓存命中率比原版低 11–20 个百分点，所以只适合无状态请求（图片、向量、短提示），不应作为对话分组的设置。

## 7. 原因码

| 类别 | 原因码 |
|---|---|
| 选择层 | `owner_continuation`、`affinity_hit`、`affinity_wait`、`spill_rebind`、`spill_keep`、`soft_spill`、`new_session`、`sessionless`、`realign_cold`、`return_cold`、`return_deadline`、`drain_cold`、`drain_deadline`、`rebind_unavailable`、`tier_overflow_saturated`、`tier_overflow_unavailable`、`failover_next`、`admin_rebalance`、`model_routing`、`model_routing_fallback` |
| 亲和键来源 | `key_continuation`、`key_session`、`key_prompt_cache_key`、`key_anchor`、`key_prefix`、`key_none` |
| 排除（复用原版闸门） | `disabled`、`inactive`、`expired`、`rate_limited`、`overloaded`、`temp_unschedulable:<来源>`、`quota_exceeded`、`model_unsupported`、`model_rate_limited`、`window_cost`、`rpm_limit`、`session_limit`、`iq_degraded`、`channel_restricted`、`profit_gate`、`privacy_required`、`platform_mismatch`、`transport_mismatch`、`capability_mismatch`、`compact_unsupported`、`runtime_blocked`、`proxy_quarantined`、`quota_auto_pause:<窗口>`、`shadow_parent_unhealthy`、`grok_quota`、`scheduling_threshold`、`excluded_in_request` |
| 份额 | `drained`、`saturated`、`codex_pressure`（D4 确认后启用） |
| 缓存归因（§14.2） | `cache_hit`、`miss_new_session`、`miss_switch_policy`、`miss_switch_saturated`、`miss_switch_unavailable`、`miss_switch_failover`、`miss_ttl_expired`、`miss_prefix_drift`、`below_min_cacheable` |

原因码是稳定枚举，前端通过 i18n 显示；`temp_unschedulable` 的来源取原版 `TempUnschedulableReason` 中的结构化来源字段。

## 8. 存储与运行时状态

| 位置 | 内容 |
|---|---|
| PostgreSQL `share_scheduling_policies` | `group_id` 主键、`version`、`scheduler`、`policy JSONB`、`imported_from`、`updated_by`、`updated_at`。由新迁移创建，并把 `scheduling_group_policies` 导入为未启用草稿：`scheduler='sub2api'`，`priority→tier`，`traffic_weight→weight`，权重 0 转为 `drain=true, weight=1` |
| settings 表 | `share_scheduling_enabled`、默认值模板、观测设置 |
| Redis（已有） | 粘性绑定键（格式不变，读取改为 `GET`+`PTTL` 管道）、并发槽、等待计数（全部沿用原版） |
| Redis（新增） | `xy2:share:attr:{gid}:{分钟}`：选择归因计数，异步批量 HINCRBY，TTL 26 小时（2026-10-11 起分钟桶 2 小时 10 分，另写小时桶 `xy2:share:attrh:{gid}:{小时}` 保留 26 小时，见 §18）<br>`xy2:cache:attr:{gid}:{分钟}`：缓存归因的请求数与 Token 数，原版与份额分组都写，TTL 同上（小时桶 `xy2:cache:attrh`）<br>`xy2:cache:sess:{gid}:{会话指纹}`：抽样会话的上一请求 `{账号, 开始时间, 输入总量}`，TTL 1 小时<br>`xy2:share:dec:{gid}`：最近 500 条决策，LPUSH/LTRIM，TTL 24 小时<br>`xy2:share:policy`：策略变更广播 |
| 进程内 | 策略缓存（版本号）、纠偏系数（30 秒）、账号最近不合格时间、影子运行累计 |

策略变更流程：管理员保存 → 数据库 CAS 成功 → 广播 `{group_id, version}` → 各实例重新加载该组。另有 30 秒全量刷新兜底。决策记录携带策略版本，便于排查“某节点仍用旧版本”。会话指纹为键哈希的截断值，不存原始会话标识与内容。

## 9. 管理接口（`/api/v1/admin/share-scheduling`，沿用管理员鉴权与审计）

| 方法与路径 | 用途 |
|---|---|
| `GET/PUT /settings` | 总开关、默认值模板与观测设置 |
| `GET /groups` | 分组列表：调度器、策略版本、是否有导入草稿、最近 1 小时偏差与缓存摘要 |
| `GET/PUT /groups/:id/policy` | 读写策略，PUT 需 `expected_version`，严格 JSON |
| `POST /groups/:id/preview` | 对“拟保存策略”做无副作用计算：<br>• 各账号目标份额，以及按当前合格状态的有效份额；<br>• 新会话放置变化比例；<br>• 预计热迁移数：默认规则为 0，设置截止时按当前绑定估算会话数与重建 Token；<br>• 权重与并发容量不匹配告警：份额超过容量占比 1.5 倍时容易满载溢出；<br>• 风险提示：某层无合格账号、全部排空、缓存域跨平台 |
| `POST /groups/:id/explain` | 给定模型、端点、可选会话键或请求样本，返回：<br>• 亲和键来源、缓存寿命阈值、空闲时长、预计 Token 与等待预算；<br>• 候选的层、权重、有效权重、资格原因、是否满载、缓存域、HRW 次序；<br>• 绑定状态与最终决策。<br>不抢槽、不写绑定 |
| `GET /groups/:id/report?window=1h&unit=...` | 目标与实际份额（请求、Token、标准成本）；缓存读占比与缓存归因分布；按决策类型拆分的首字 P50/P95/P99 与等待时间；热迁移计数与原因；亲和命中、溢出、换号次数；影子运行一致率 |
| `GET /groups/:id/decisions?limit=100` | 最近决策样本 |
| `POST /groups/:id/rebalance` | 显式重平衡：可选“只清除已冷绑定”（默认）或“全部清除”，返回受影响数量 |

## 10. 前端

- **系统设置 → 网关**：
  - 删除“调度模式”卡片；
  - 原版高级调度、低倍率优先、粘性等设置恢复为始终显示；
  - 新增“份额调度”总开关、默认值与观测设置。
- **份额调度页面**（新路由 `/admin/share-scheduling`，侧栏常驻）：
  - 左侧分组列表：调度器（含影子）、版本、偏差与缓存摘要、草稿标记；
  - 右侧账号表：层级、权重、排空、缓存域、当前资格（实时原因）、目标份额、实际份额；
    - 支持行内编辑、批量设置、按层拖拽；
    - 保存前展示预览差异，含预计热迁移与容量告警；
    - CAS 冲突时保留草稿；
  - 报表标签页：目标/实际堆叠条、偏差原因分布、缓存读占比与缓存归因、首字分位（按决策类型）、最近决策；
  - 解释工具：输入模型与会话键或粘贴请求样本，显示整个决策过程。
- **账号管理**：
  - 恢复原版列，调度评分列由原版列显示开关控制；
  - 账号详情中只读显示其所在分组的层级、权重与缓存域，并链接到份额调度页。
- 删除旧的 `SchedulingView.vue`、`SchedulingModeSettings.vue`、`stores/schedulingMode.ts` 等（清单见 §13）。

## 11. 与现有功能的关系

| 功能 | 在份额调度中的处理 |
|---|---|
| 分组模型路由 | 作为管理员规则保留：先限定路由账号，全部不合格时回到全体，记原因 |
| 组合分组 / 目标平台解析 | 在钩子之前已完成，份额调度使用解析后的平台与上游模型 |
| Antigravity 混合调度、简易模式、组 0 | 沿用原版候选规则 |
| 窗口费用 / RPM “仅粘性可用” | 绑定会话按粘性判定，新会话按非粘性判定，与原版一致 |
| 最大会话数（Anthropic OAuth） | 抢槽后调用原版 `checkAndRegisterSession`，拒绝即试下一个 |
| IQ 降级、利用率阈值暂停、额度自动暂停 | 原版资格闸门，排除并记原因；带恢复时间的闸门参与软溢出判定 |
| 利润门 | 沿用原版的“选号阶段 + handler 终检”两段式 |
| 强制缓存计费 | 原版 handler 逻辑不变；份额调度减少热换号，即减少站点代付的缓存重建 |
| Claude 缓存补齐（按分组） | 同组所有账号使用同一补齐规则，前缀在账号间一致；TTL 判定读补齐后的出站请求体 |
| 账号级请求改写（OAuth 伪装、Codex 指令转换、工具名改写、模型映射） | 份额调度不修改请求体；这些改写因账号类型而异，换类型必然冷启动，因此溢出与故障转移次序同类优先（§6.8）；改写引入的前缀漂移由缓存归因（§14.2）单独暴露 |
| OpenAI 质量路由（D3） | 一期不接入；二期可作为显式闸门（被观测降级的账号在该会话内排除） |
| Codex 压力并发（D4） | 作为显式容量闸门：有效并发 = min(配置, 压力上限)，满载记 `codex_pressure` |
| `accounts.schedulable` 一键开关 | 硬停止：不合格，绑定会话立即迁走；`drain` 为软停止，按冷窗口迁移 |

## 12. 多实例、性能与首字预算

**选择阶段的往返**（以 `servertiming` 采集器按请求记录 Redis 往返次数与耗时）：

| 路径 | 原版 | 份额调度 |
|---|---|---|
| 绑定命中 | 读粘性 1 + 抢槽 1 + 续期 1 | 读粘性与 PTTL 同一管道 1 + 抢槽 1 + 续期 1 |
| 新会话 | 读粘性 1 + 批量负载 1 + 抢槽 1 + 写粘性 1 | 读粘性 1 + 抢槽 1（首选有空位）+ SET NX 1；首选满载时先批量负载 1 再抢槽 |
| 满载等待 | 原版等待计划（最长 120 秒） | 原版等待计划（预算 ≤15 秒） |
| 数据库 | 0（剔除 controlled 后） | 0 |

- CPU：在原版已有的候选列举与资格判定之上，只增加 N 次 64 位哈希与排序（500 个候选约数十微秒）。验收基准：相对原版同一入口的 P99 增量 < 1ms，热路径数据库查询数增量为 0，以 sqlmock / 连接计数断言。
- Redis：归因与决策记录异步批量写（每秒一次管道），失败只丢统计，不影响请求。
- 连接预热：默认 `account_proxy` 连接池隔离、空闲 90 秒回收。流量稀少的账号首个请求需要重新建立 TCP/TLS（及代理）连接。原版的 LRU 让每个账号都持续有流量，份额调度按权重集中，低权重账号更容易冷连接。报表按账号给出冷连接占比；不做主动探活请求，避免对上游产生额外调用。
- 一致性：HRW 决定性 + 绑定 NX + 策略广播，保证多实例在秒级内一致；决策记录带节点与策略版本。

## 13. 剔除智能可控调度

### 13.1 删除清单

后端：
- `backend/internal/scheduling/`（整个包，含 README 与测试）。
- `backend/internal/service/` 下的 controlled 文件：
  - 源文件：`controlled_*.go`、`openai_controlled_scheduling.go`、`openai_ws_controlled*.go`、`scheduling_mode.go`、`scheduling_usage.go`、`account_pool_policy.go`、`account_pool_quality.go`、`openai_legacy_owner_control.go`、`controlled_agent_identity.go`；
  - 全部对应测试：`controlled_*_test.go`、`account_pool_*_test.go`、`scheduling_mode*_test.go`、`one_click_stream_drain_test.go`、`gpt61_sol_scheduling_integration_test.go` 等；逐个确认测试意图后删除或改挂原版路径。
- 保留只读负载窥视：`concurrency_readonly.go` 与 `repository/concurrency_cache_readonly.go`。改用本包错误值，去掉对 `scheduling` 包的依赖，供份额调度的 Explain 无副作用读取负载。
- 65 个含 controlled 分支的非测试文件：删除 controlled 分支，保留原版分支；`Sub2APISchedulingEnabled(ctx)` 恒为真的分支直接展开。这些文件以 `ControlledSchedulingEnabled`、`Sub2APISchedulingEnabled`、`handleControlledSchedulingStop`、`controlledScheduling`、`CaptureControlled*` 等标识识别。
- handler 与路由：
  - 删除 `backend/internal/handler/controlled_scheduling_errors.go` 与 `handler/admin/scheduling_*.go`；
  - `server/routes/admin.go` 的 `/scheduling/*` 与账号调度控制/故障域路由改为 410 退役处理器，保留一个版本；
  - `server/routes/gateway.go` 的 `schedulingMode` 中间件全部移除。
- 依赖注入与计费：删除 `service/wire.go` 的 `ProvideControlledSchedulingService` 与四处 `controlledHTTPUpstream` 包装；去掉 `usage_billing_recovery.go` 对调度确认的依赖；重新生成 `cmd/server/wire_gen.go`。

前端：
- 删除：
  - `views/admin/SchedulingView.vue`；
  - `components/settings/SchedulingModeSettings.vue`；
  - `stores/schedulingMode.ts`；
  - `api/admin/scheduling.ts`、`api/admin/schedulingMode.ts`；
  - `types/scheduling.ts`；
  - `utils/groupScheduling.ts`；
  - `composables/useSchedulingFeedback.ts`；
  - `i18n/locales/{zh,en}/admin/scheduling.ts`；
  - 以及对应测试。
- 修改：
  - `AccountsView.vue`；
  - `SettingsView.vue`：`v-if="schedulingModeStore.isSub2API"` 包裹的原版设置恢复为常显；
  - `AppSidebar.vue`、`router/index.ts`；
  - `Create/Edit/BulkEditAccountModal.vue`：“默认优先级”文案恢复为原版“优先级”。

其他：
- `.github/workflows/backend-ci.yml`：“Scheduling reliability”与 TypeSafe systemone 步骤中的 controlled 测试名，替换为原版路径与份额调度测试；保留真实 PG/Redis 零跳过守卫机制。
- `tools/upstream-sync/policy.json`：移除 `typesafe-controlled-settlement` 补丁；若 TypeSafe 非流式结算仍需二开保护，改为原版路径测试并重新登记。
- 文档：
  - `docs/CONTROLLED_SCHEDULING.md`、`docs/CONTROLLED_SCHEDULING_ACCEPTANCE.md`、`docs/account-scheduling.md`、`docs/ui-scheduling-*.md` 标注为历史或删除；
  - 新增 `docs/SHARE_SCHEDULING.md` 使用说明。

### 13.2 保留且不得误删

- PR #74 等“纯官方”可靠性修复（终端发送、非流式安全读取回退、PG 账号模型冷却等）：它们在原版路径同样生效，删除 controlled 分支时逐处核对。
- 迁移 266 的账号 `updated_at` 单调触发器与调度快照 v2 键 CAS：防止旧写覆盖新开关，属于通用正确性修复。
- `accounts.schedulable` 一键开关及其严格 JSON 校验。
- 耐久计费（迁移 267）：`UsageBillingCommand.SchedulingAttemptID` 保留为兼容字段，使升级前写入的意图可被新版本恢复；新版本不再写入，也不再确认。

### 13.3 数据与迁移

- 不改写 257–269 任何已发布迁移与 checksum。
- 新迁移 `270_share_scheduling.sql`：建 `share_scheduling_policies` 并导入旧分组策略草稿。
- controlled 表（`scheduling_*`）保留一个正式版本，无代码读写；之后在导出后由独立迁移删除（D7）。保留期间回滚到 0.2.5 仍可读到原表与原模式。
- `scheduling_system_mode` 不再被读取；升级后全部分组走 `sub2api`（D1），管理员审阅草稿后启用份额调度。

### 13.4 退役接口

`/api/v1/admin/scheduling/*` 与 `/accounts/:id/scheduling-control|failure-domains*` 在本版本返回 `410 Gone` 与新接口指引；下一版本删除路由。

## 14. 缓存与首字：验证、观测与门禁

### 14.1 仿真（规划阶段，已完成）

[`simulation/cache_ttft_sim.py`](simulation/cache_ttft_sim.py) 是标准库离散事件仿真，使用固定种子。它在同一负载、同一组事件下比较原版、初版、修订版、修订版（严格回升）与逐请求随机：

- 负载：多轮会话、长停顿、上下文增长、压缩与高峰；
- 事件：短时限流、整层限流、改权重、排空；
- 缓存寿命：分 5 分钟与 1 小时两种。

结论见 RESULTS 第二部分：修订版的缓存命中率与原版持平（逐种子差 −0.3 至 +0.4 个百分点），TTFT P95 为原版的 0.67–0.93 倍、P99 为 0.70–0.91 倍；同时第 1 层份额接近 5:3:2，健康期第 2 层占比低于原版。仿真只比较策略之间的相对差异，延迟与价格系数不代表任何上游。

### 14.2 缓存归因（原版分组与份额分组都启用，用于同口径对照）

结算时，用请求上的决策上下文与 usage 中的缓存 Token，把每个请求归入一个缓存结果：

| 归因 | 判定 |
|---|---|
| `cache_hit` | 缓存读 > 0 |
| `below_min_cacheable` | 输入低于模型最小可缓存长度 |
| `miss_new_session` | 会话首个请求 |
| `miss_switch_*` | 抽样会话的上一请求在另一账号（不同缓存域），且未超过缓存寿命；按决策原因分为策略、满载、不可用、失败换号 |
| `miss_ttl_expired` | 同一账号，但距上一请求超过缓存寿命 |
| `miss_prefix_drift` | 同一账号、未超寿命，缓存读低于上一请求输入总量的一半：前缀被客户端或网关改写，例如上游 issue #7400 的 system 前移 |

- 计数写 `xy2:cache:attr`（请求数与 Token 数）；会话上一请求记录按会话指纹抽样（默认 1/4），异步写入，失败只丢统计。
- 意义：把“调度造成的未命中”（`miss_switch_*`）与“客户端/改写/超时造成的未命中”分开。只有前者是调度的责任。

### 14.3 影子运行

`scheduler=shadow` 时由原版选号；份额调度用同一请求的候选与资格结果计算 `Plan`，不抢槽、不写绑定，只记录以下指标：

- 两者选择一致率；
- 份额调度会产生的溢出、改绑与重对齐次数；
- 预计热迁移数；
- 选择耗时。

它用于启用前在真实流量上验证资格原因、性能开销与“零热迁移”。这也是缓存感知路由研究中建议的上线方式：启用亲和前先做影子回放，因为亲和收益依赖实际负载。

### 14.4 灰度门禁

对照组为同组过去 7 天同时段，以及同期仍用原版的相似分组，按模型与输入大小分桶比较。

| 指标 | 门槛 |
|---|---|
| 缓存读占比（Token 加权） | ≥ 基线 − 0.5 个百分点 |
| 调度造成的未命中 Token（`miss_switch_*`） | ≤ 基线 |
| TTFT P50 / P95（同模型、同输入分桶） | ≤ 基线 +3% / +5% |
| 策略性热迁移（不含管理员截止） | ≤ 请求数的 0.1% |
| 选择阶段 P99 增量 / 数据库查询增量 | < 1ms / 0 |
| 错误率、换号率 | 不高于基线 |

触发告警后先关分组开关，再关总开关；两者都不会迁移会话。可选的“自动回退”（默认关闭）在连续 30 分钟越线时把该分组切回 `sub2api` 并通知管理员。

### 14.5 推广顺序

1. 发布：全局开关默认关闭。确认原版行为与上游一致，即调度三文件与上游 v0.2.15 的差异只剩登记的白名单块；网关请求不再查询模式表；503“Scheduling mode is temporarily unavailable”消失；缓存归因开始积累原版基线。
2. 影子：选 1–2 个分组运行 `shadow` 至少 3 天，核对一致率、预计热迁移为 0、开销达标。
3. 灰度：选择一个低风险分组启用 `share` 3–7 天，按 §14.4 判定。
4. 推广：按组逐步启用；任何异常先关分组开关。

## 15. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 删除 controlled 分支时误改原版语义 | 以上游同版本文件为基准做三方对照，逐文件评审；原版全量测试 + 真实存储集成；增加“调度文件与上游差异白名单”CI 检查 |
| 计费恢复依赖调度确认 | 兼容字段保留并忽略；用升级前格式的意图做恢复测试 |
| 大号池资格判定开销 | 复用原版批量预取；500 账号基准纳入 CI |
| 多实例绑定竞争 | `SET NX` + 改绑覆盖规则，最多一次额外缓存冷启动 |
| 策略在实例间短暂不一致 | 广播 + 30 秒刷新，决策记录带版本 |
| 管理员混淆账号 priority 与分组 tier | 页面明确标注“原版使用账号优先级 / 份额调度使用分组层级”；未配置账号默认继承账号优先级 |
| 重尾大会话导致成本份额波动 | 文档与报表明确精度边界；建议用账号窗口费用、额度阈值做硬封顶 |
| 上游同步冲突 | 每个入口只有 3–5 行钩子，登记为自有补丁并绑定测试 |
| 等待预算系数不适合实际上游 | 阶段 0 用首字与未缓存输入的回归斜率校准；报表持续给出斜率与等待命中率 |
| 冷窗口回升滞后（尤其 1 小时缓存） | 预览与报表显示低层滞留比例；需要严格回升的分组设置回升截止，并在预览中显示代价 |
| 满载改绑使份额向空闲账号漂移 | 属于容量约束下的必然结果；冷窗口重对齐收敛；预览提示权重与容量不匹配 |
| 缓存域配置错误（实际不共享） | 缓存归因会在同域换号上出现 `miss_switch_*`，报表告警；Explain 显示缓存域 |
| 对话锚点碰撞（同一 API Key 下首条消息相同） | 只影响同一租户；碰撞会话共享前缀反而有利于缓存，热点由满载溢出分散 |
| PTTL 推算的空闲时长偏小（溢出也续期） | 只会让判定更保守（晚迁不误迁） |

## 16. 备选方案

| 方案 | 结论 |
|---|---|
| 修补 controlled | 不采用：复杂度高、无亲和、热路径依赖数据库，且用户明确要求剔除 |
| 层内共享 SWRR（无亲和） | 不作为默认：请求份额最精确，但缓存命中率比原版低 11–20 个百分点（仿真）；保留为 `affinity.enabled=false` 的等价行为 |
| 评分式自适应（Bifrost、AxonHub、ranxi2001 分支、原版实验调度器、Prequal 式延迟探测） | 不采用：首字改善依赖不可预期的评分，份额不可控；份额调度用有界等待与满载改绑获得首字收益 |
| 粘性 + 新会话加权随机（Portkey、LiteLLM、new-api 渠道亲和、OpenRouter 粘性路由） | 接近本方案；选 HRW 是因为跨实例决定性、会话级稳定回退次序与最小扰动；冷窗口重对齐与缓存价值等待是在其基础上的补充 |
| 有界负载一致性哈希（CHWBL，KubeAI、Vimeo） | 不直接采用：其上限按平均负载设定，而中转账号的约束是各自的并发上限；本方案用“满载溢出 + 改绑”实现同样的热点分散 |
| 前缀树缓存感知路由（SGLang、llm-d、NVIDIA Dynamo）与双哈希二选一（DualMap） | 面向自建推理的 KV 缓存；上游 API 的缓存按账号与精确前缀隔离，会话键加冷窗口已足够，二选一还会破坏份额的确定性 |

## 17. 实施偏差（2026-10-10）

实现与上文不一致之处，以代码为准：

| 项目 | 设计 | 实现 |
|---|---|---|
| 抽样会话记录 `xy2:cache:sess` | `{账号, 开始时间, 输入总量}`，TTL 1 小时 | 另存缓存寿命，格式 `账号:毫秒:寿命秒:输入总量`，TTL 6 小时，避免 1 小时缓存会话的上一记录先于缓存过期 |
| 冷绑定重平衡 | 按会话缓存寿命判定 | 绑定不记录协议声明，按 Anthropic 1 小时（或 `cache.ttl_seconds`）保守判定“已冷” |
| 分组列表摘要 | 偏差与缓存摘要 | 仅读计数（决策数、缓存读占比、换号致失占比、影子一致率），不含偏差 |
| OpenAI 亲和键 | 对话锚点 | 沿用原版内容派生的会话种子，不新增锚点；对话锚点只用于 Gateway 份额分组，且不含工具定义；`prefix` 模式优先于客户端 metadata 会话标识 |
| 不经过份额调度的 OpenAI 请求 | 仅协议强续接 | 另有 compact、图片请求与质量路由持有的会话保持原版；续接绑定写入沿用原版 SET |
| 分组范围 | 含组 0 | 管理端只列出启用分组；组 0 无法在管理端配置，始终原版 |
| 首字分位 | P50/P95/P99 | 取直方图桶上界（100ms–30s），按归因抽样率统计 |
| 绑定读取 | GET 与 PTTL 同一往返 | 网关处理器已预取一次粘性绑定，份额分组再做一次 GET+PTTL 管道读取，即多一次往返；新会话省去批量负载读取，整条选择路径仍不多于原版（单测断言；本地真实 Redis 同负载实测份额 10.0 次 / 原版 10.2 次，数据库查询均为 0） |
| 预览 | 含强制迁移代价（前缀 Token × 价差） | 给出落点变化比例与预计强制迁移会话数，未估算 Token 代价 |
| 无法解析的已存策略 | — | 请求路径按原版处理；管理端以默认值展示并标注原因，保存时按原版本号覆盖 |
| 活动层 | — | 层级可为负数；没有健康未排空账号时 `active_tier` 为 `null` |
| 前端 | 按层拖拽、账号详情只读显示 | 按层拖拽以层级卡片实现（拖把手到卡片改层、多选一起移动、拖到新卡片建层，另有“选中本层”“只看本层”）；拖拽仅支持鼠标，键盘用行内层级与批量编辑完成同样操作。账号详情在编辑账号弹窗中只读显示各分组的生效规则（`GET /accounts/:id/rules`） |
| 报表账号表现（新增） | — | 参考外部项目的按渠道首字分位与满载信号：按账号记录被跳过次数与原因（`skip:<账号>:<原因>`）和首字直方图（`acct_ttft:<账号>:<桶>`，随归因抽样），报表给出跳过率与 P50/P95；只用于提示调权，不自动调权 |
| 等待中客户端断开 | — | 取消后立即释放排队计数并停止尝试后续账号，不把未尝试的账号计为满载 |
| 隐式缓存域 | 留空表示自成一域 | 内核把留空解析为 `account:<账号 ID>`，解释与报表直接显示；自定义缓存域禁止使用 `account:` 前缀，避免与隐式域冲突 |
| 选号开销 | 以 `servertiming` 按请求记录 Redis 往返与耗时 | 抽样选号都记录耗时；Redis 往返、耗时与数据库查询依赖仓储层计时钩子，只在开启 `server.enable_server_timing` 的节点采集，按采集样本（`io_samples`）求平均，无样本时接口返回 `null`、页面显示“—”。不为此在默认配置下常开钩子，以免改变上游开关语义 |
| 4.1 份额误差 | 3000 会话误差 ≤1 个百分点 | 3000 会话下 5:3:2 各份额的抽样标准差约 0.7–0.9 个百分点，1 个百分点低于抽样噪声；端到端改为 3σ 容差，≤1 个百分点由内核 1 万会话比例性测试保证 |
| 首字统计 | 首字 | 沿用原版 `first_token_ms`，取上游首个流事件的时间，只有流式请求有值 |

## 18. 上线前加固（2026-10-11）

复审对照外部二开仓库的调度审计清单（候选键覆盖完整候选集、配置失败时有时限地沿用上次结果、资源有界、负载信息缺失与非有限值、快照持有可变指针）以及上游“健康评分前先要求样本量”的做法，修正以下问题：

| 问题 | 风险 | 处理 | 测试 |
|---|---|---|---|
| 影子对比每个请求起一个 goroutine | 高并发时影子计算无上限，挤占选号与上游转发 | 同时运行的影子对比最多 32 个，超出直接跳过并计入 `shadow_skipped`，报表显示「影子对比跳过（负载上限）」；跳过不影响原版选号 | `TestShareSchedulingShadowComparisonsAreBounded` |
| 重平衡先读后删绑定 | 读与删之间会话已改绑到别的账号时，会误删新绑定，造成一次多余的换号 | 删除改为 Lua 比对：绑定仍指向被清除的账号才删除，返回实际删除数 | `TestShareSchedulingBindingsOnRedis`（moved 用例） |
| 已取消的请求仍进入等待 | 等待计数失败放行后，延迟递减会扣掉其他等待者的计数 | 进入等待前检查请求是否已取消，已取消直接返回 | `TestShareSchedulingWaitSkipsCancelledRequests` |
| 每个请求重建账号规则索引 | 规则多时每次选号多做一次 map 构建 | 策略加载时冻结规则索引（`Policy.Freeze`），请求路径只读；未冻结的策略行为不变 | `TestFrozenPolicyPlansIdentically`（200 次决策一致，100 条规则时分配更少） |
| 纠偏每 30 秒按全窗口聚合 `usage_logs` | 长窗口、多分组时数据库压力随分组数线性增长；查询失败立即清空系数导致份额跳变 | 每组按窗口 1/12 刷新（30 秒–15 分钟），策略版本变化立即重算；单组查询 3 秒超时，整轮不超过 10 秒（数据库卡住时不拖住策略刷新）；失败时沿用上次系数，最长 max(窗口, 10 分钟)，之后按无纠偏运行 | `TestShareSchedulingCorrectionRefreshIsSpacedAndBounded`、`TestShareSchedulingCorrectionRefreshEndsWithItsContext` |
| 决策记录逐项加锁、先序列化再判断是否保留样本 | 高并发时锁竞争与无用的 JSON 序列化 | 一次加锁累加全部计数；每组每次刷写最多保留 100 条样本，超出只计丢弃数、不再序列化，计数照常 | `TestShareSchedulingDecisionSamplesCappedPerFlush` |
| 账号类别字符串每次拼接 | 每次选号额外分配 | 类别名按（平台, 类型）驻留，写时复制，最多 256 项 | 基准 |
| 计数按分钟保留 26 小时 | 每分钟一个哈希，含按账号的选中、理想、跳过与首字桶字段；500 个活跃账号的分组每分钟约 3000 个字段，超过 listpack 上限后一天约 400 MB，24 小时报表要读 2880 个哈希 | 分钟桶只保留 2 小时 10 分，同时写小时桶保留 26 小时；不超过 2 小时的窗口按分钟读，更长的窗口读整点小时（起点取最近的整点，误差 ≤30 分钟）加当前小时的分钟桶。同等规模约 40 MB，24 小时报表读约 110 个哈希 | `TestShareSchedulingStoreCountersAndDecisions`；本地压测按字段实测估算 |
| 首字分位样本少时也显示 | 少量请求的 P95/P99 误导调权 | 样本数低于 5 / 20 / 100 时 P50 / P95 / P99 显示「—」并加注说明 | `ShareSchedulingView.spec.ts` |

500 账号基准（`BenchmarkShareSchedulingSelection500`）：份额调度每次选号分配从约 1088 次降到约 585 次、内存约 2.16 MB 降到 2.08 MB；剩余分配主要来自与原版共用的账号列表与上游模型限流键。

未采纳：外部项目“全部账号失败时放行全部”的兜底（与原版行为不一致，原版此时返回无可用账号）；流式卡顿检测可作为后续独立改进。上游 v0.2.15 的 Anthropic OAuth 缓存 TTL 顺序修复（`da4419e62`）与调度无关，应按同步机制单独同步。

## 19. PR #90 复审精简（2026-10-12）

`a30fc223` 之后，PR #90 被追加了一轮“独立审查加固”（`85eae404`–`23f191fc`，约 1900 行），CI 的 Integration tests 失败。本次按 §1 的原则复审：保留原版调度、份额只管新会话落点、观测尽力而为，并对照被引用的二开项目核实各项做法的原始场景。

| 追加内容 | 处理 | 理由 |
|---|---|---|
| 迁移 271：把导入草稿中的零权重成员改为 `disabled` | 删除 | 导入草稿本就是未生效的原版调度，由管理员审核后启用；新增不可撤销的迁移得不偿失。它还让 `TestUpstreamV0214MigrationUpgrade`（升级前迁移数 314）失败，这正是 CI 失败的原因。 |
| 按分组 `disabled` 与 `drain_fallback`、严格排空 | 删除，排空恢复原语义 | guardian 的人工暂停使用宿主账号的 `schedulable=false`，本系统同样有这个开关，两种调度都生效。按分组停用为此改动了原版 OpenAI 调度（质量路由、Compact、Images 路径），违背“保留原版”；`drain_fallback` 则多出一个开关。排空保持：不接新会话；只有没有可用开放账号时才兜底，与原版的可用性一致。 |
| 等待登记改为按请求租约（ZSET、服务端时间、释放墓碑） | 删除，恢复原版等待计数 | 租约来自 ranxi2001 的 API Key 级排队状态机（FIFO 排队、实时交接），份额等待只是几秒内可选的亲和等待，等不到就溢出。原版计数在 Redis 出错时最多让等待上限短暂多放行一个，与原版行为相同；取消请求不进入等待已在 §18 修复。保留其中一点：等待中的槽位调用受等待预算约束。 |
| 首次绑定 NX/CAS 竞争时去抢胜出账号的槽位 | 删除 | HRW 是确定性的，同一会话的并发首请求通常算出同一账号；不一致只在溢出时出现，代价是一次缓存未命中。原版对所有绑定都是后写覆盖。 |
| 统计批次去重、待重试批次、重启后标记 24 小时不完整 | 删除 | 每次刷写（每秒）按“分组 × 分钟桶”写一个保留 26 小时的标记 key，双实例下每个分组常驻约 35 万个 key、约 50 MB；重启后 24 小时内报表恒为不完整，灰度门禁无法使用。改为：写入失败时把丢弃的计数项计入已有的丢弃数，报表照常提示。 |
| 升级盘点 SQL 与预检确认项 | 删除，改为升级说明 | 预检工具用于 Sub2API→XY2API 的数据身份检查；新增的确认项要求先在只读副本上跑 SQL、再手改 JSON，不提供时每次预检都告警。注意事项写入使用文档的“运维与回退”。 |
| 应用版本 `applied_version`、绑定异常与“不完整”报表字段 | 删除 | 只反映响应实例，多实例下容易误读；依附于已删除的机制。 |
| 默认权重取并发数（不再用负载因子） | 保留 | 运行时原用负载因子，而管理端视图和使用文档都写着取并发数，现在三处一致。 |
| 设置写入纪元、策略版本不回退、保存后不再读库 | 保留 | 慢的全量刷新不会把刚保存的设置或策略覆盖回旧值；已提交的保存不会因后续读取失败而报错。 |
| 前端过期结果防护、按字段合并 | 保留并简化 | 切换分组、刷新、放弃后，旧的预览和保存结果不再生效。字段级合并保留另一位管理员对其他字段的修改；双方都改的字段以自己的为准，由保存前的预览确认，不再弹出按内部路径逐项选择的界面。 |
| 长窗口精度 `counter_precision` | 保留 | 6 小时、24 小时窗口按整点汇总，报表注明误差上限。 |

结果：相对 `23f191fc` 删除约 1250 行（含迁移、等待/绑定/统计的 Lua 与新 Redis key 族、升级工具），新增 Redis key 族为零；相对 `a30fc223` 净增约 500 行，其中约 300 行是测试。新增的并发用例均做了反向验证：去掉保护逻辑后用例失败。

## 20. 上线前规范化（2026-10-16）

随网关韧性 0.4.0 的上线前审查，按生产部署标准复核份额调度的后端与前端。后端修正以下问题：

| 问题 | 风险 | 处理 | 测试 |
|---|---|---|---|
| 设置 `return_deadline_seconds` 时，高层恢复时间未知（高层只是满载、从未不健康，或本实例没见过它失败）被当作“已稳定无限久” | 因高层满载而溢出到低层的热会话，在下一次请求就被强制迁回，重建提示缓存 | 截止时间只从高层已知的恢复时间（`StableSince`）起算；恢复时间未知时不按截止迁移，会话按 `tiers.return` 处理（默认 `cold`：缓存变冷后以 `return_cold` 迁回） | `TestTierReturn`（`53b73fd36`） |
| 余额耗尽（`balance_exhausted`）与额度自动暂停（`quota_auto_pause`）不计入健康原因 | 充值或暂停解除后的高层账号立即接回会话，不等 `tiers.stable_seconds`，与回升防抖的本意不符 | 两者计入健康原因，与限流、过载、临时不可调度等一样推迟回升；模型不支持、渠道限制等按请求的闸门仍不计 | `TestRechargedAccountWaitsForStabilityBeforeTierReturn`（`024e564c4`） |
| 报表窗口只校验大于 0 | 使用文档写明下限 1 分钟，接口却接受 `30s` 等更短窗口 | `window` 须在 1m–24h，否则返回 400 | `TestShareSchedulingHandlerRejectsMalformedRequests`（`7afdfc077`） |

复核未发现问题、未改动：策略校验、CAS 保存、Redis 扫描与条件删除、统计上界、影子对比并发上限。

前端规范化（`11c12d882`、`345b376fa`、`7ef136d49`），只改布局、排版与文案，不改接口：

- **页头：** 不再用卡片重复顶栏的标题与说明；只有总开关关闭时显示提示条与“前往系统设置”链接，读取设置失败时显示错误。
- **分组列表：** 只给已保存策略（含导入草稿）的分组显示调度器徽标，使用份额调度的少数分组更醒目。
- **策略表：** 健康状态移到账号名下方，输入框按列宽，份额先显示生效值、再显示目标与容量；表格最小宽度从 960 px 降到 720 px，排空、缓存域、份额与恢复默认按钮在 1440 px 宽度下不再竖排换行，表格不再横向溢出。
- **报表：** 时间窗口改用通用下拉组件；没有数据的分布不显示，全部为空时合并为一行提示；首字分位表只在有样本时显示；“暂无决策样本”只在确实关闭了“保存决策样本”时才提示去系统设置开启。
- **数字：** 表格数字改用等宽数位（`tabular-nums`），不再用等宽字体；标识符与 JSON 仍用等宽字体。
