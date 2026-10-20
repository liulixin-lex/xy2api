# 设计：精简份额调度（2026-10-20）

> 取代 [`replace-controlled-with-share-scheduling`](../replace-controlled-with-share-scheduling/design.md) 中的配置模型（§4）、纠偏（§6.9）、影子（§14.3）、重平衡与管理接口（§9）。HRW、缓存冷热判定、亲和与溢出（§6.1–§6.8）的算法不变，只是参数改为常量。本文的文件路径相对仓库根目录。
>
> 依据：仓库外的后端审计、前端交互审计、生产只读分析与 PM 综合。本文不写生产细节。

## 1. 目标模型

**一句话**：份额模式和原版共用同一套账号设置（优先级分层、负载因子定比例）。区别只在算法：份额按比例分配**新会话**并保护会话缓存，原版按负载率分配**请求**。

| 概念 | 取值 | 代码位置 |
|---|---|---|
| 层级 | `Account.Priority`，数字越小越优先，严格分层 | 内核 `sharesched.Plan` 的条目层级取 `Candidate.Priority` |
| 权重 | `Account.EffectiveLoadFactor()`：负载因子 1–10000；未设时取并发，并发 ≤0 时取 1 | 新增 `Candidate.Weight`，由 5 个构造点填写：Gateway 与 OpenAI 两个适配器、绑定账号不在列表时的补位候选、末位兜底放宽一轮、管理端成员视图。所有构造点、成员视图、预览、解释与账号份额接口都经 `shareWeight` 一个函数取值；它在负载因子上叠加 §11 的健康系数与爬升系数 |
| 账号规则 | 策略 JSON 里的 `accounts`（tier、weight、drain、drain_until、cache_domain）**读时忽略**，保存时原样保留（§3） | — |
| 满载 | 与原版对齐，见 §5 | 份额执行器 |
| 每组设置 | 调度方式 `sub2api` / `share`；会话保持 开（`affinity.key=auto`）/ 关（`none`） | §3、§7 |
| 全局设置 | 只有总开关 `enabled` | §7.1 |

**常量**（取值等于 0.4.3 的缺省值）：

| 原字段 | 常量 |
|---|---|
| `defaults.tier`、`defaults.weight` | 删除（层级取优先级，权重取负载因子） |
| `affinity.ttl_seconds` | 3600 秒（与原版粘性 TTL 相同） |
| `affinity.wait.mode`、`ms_per_1k_tokens`、`max_ms`、`max_waiting` | auto、100 毫秒、15000 毫秒、3 |
| `affinity.on_spill` | rebind（溢出后改绑） |
| `affinity.soft_spill_max_seconds` | 删除，改用韧性判定（§4） |
| `cache.ttl_seconds`、`cache.realign` | 按协议、cold |
| `tiers.on_saturated`、`on_failed`、`return`、`stable_seconds`、`return_deadline_seconds` | spill、next_tier、cold、120 秒、无截止 |
| `balance.*` | 删除（纠偏模块删除；报表口径由前端选择） |
| 全局 `cache_attribution`、`attribution_sample_rate`、`decision_samples` | 归因常开、抽样率 4；决策样本删除 |

**会话保持的读法**：`affinity.enabled=false` 或 `key=none` 视为关；`auto`、`session`、`prefix` 视为开（按 auto 处理，Gateway 的 `prefix` 分组改用对话锚点）。

## 2. 与原版调度的差异

| 方面 | 原版（legacy） | 份额（本变更后） |
|---|---|---|
| 分层 | 账号优先级，严格 | 同左 |
| 同层比例 | 负载因子加权的最少连接（在途 ÷ 负载因子取最小，再 LRU、随机）；空闲时接近均分 | 新会话按负载因子比例分配（HRW），与当前负载无关 |
| 满载 | 负载率 ≥100 不参与负载感知选择；槽位满时跳过 | 新放置与溢出同样跳过负载率 ≥100 或槽位满的账号（§5）；已绑定会话的续用只看槽位（与原版粘性一致） |
| 小负载因子 | 在途达到负载因子即不再接新放置，相当于“新放置的软并发上限” | 由 §5 的满载对齐提供同样的上限 |
| 会话粘性 | 1 小时，忙时最多等 120 秒 | 1 小时；缓存仍热时最多等 15 秒，冷了就按比例重对齐 |
| 故障后回升 | 会话留在低层，直到空闲过期 | 高层稳定 120 秒后，缓存变冷的会话迁回 |
| 不经份额的请求 | — | OpenAI 续链与 guardian 父账号、compact、图片、质量路由持有的会话，以及组 0（与 0.4.3 相同） |
| 以下 legacy 排序**不应用于份额分组** | OpenAI 的“低上游费率优先”（`openai_low_upstream_rate_priority_enabled`，仅高级调度关闭时生效）、Gateway 的 `gateway.scheduling.prefer_soonest_reset`、0.2.6 的缓存偏好排序、OpenAI 高级调度的评分 | 份额分组按负载因子比例分配。需要这些排序的分组继续使用原版 |

负载因子是跨分组的账号属性：改一个账号的负载因子，会同时改变它所在所有分组的分配，原版分组与份额分组都受影响，方向相同（越大越多）。管理端成员视图给出 `other_group_ids` 供界面提示（§7.3）。

## 3. 兼容与回退

### 3.1 解码：接受并忽略

- `sharesched.DecodePolicy` 仍以 `DefaultPolicy` 为底严格解码，**删除的字段改为接受并忽略**：结构里保留旧字段名，类型为 `json.RawMessage`，解码后不读取、序列化时不输出。所有旧版写出的 JSON 都能解码，包括 0.3.0 迁移 270 导入的草稿与生产形态的 v4 策略。对未知的**新**字段仍然报错，防止拼写错误被静默吞掉。
- `AccountRule` 的解码同样放宽：旧规则的必填字段缺失不再报错，内容一律忽略。
- 系统设置 PUT：`cache_attribution`、`attribution_sample_rate`、`decision_samples` 接受并忽略；其他未知字段仍返回 400。
- 参照：网关韧性的 `TestParseIgnoresRemovedBalanceFields`。

### 3.2 写入：只改两个字段

- `PUT /groups/:id/policy` 读出已存 JSON，**只修改** `scheduler` 与会话保持（`affinity.key`，必要时 `affinity.enabled`），其余内容（包括旧账号规则、旧参数）逐字节保留；表列 `scheduler` 同步更新。
  - 会话保持没有变化时，不改动 `affinity`。
  - 关闭会话保持：写 `affinity.key="none"`。
  - 打开会话保持：写 `affinity.key="auto"`，并写 `affinity.enabled=true`。
- 新建策略写出旧版可解码的最小 JSON：`{"schema":1,"scheduler":"…","accounts":[],"affinity":{"enabled":true,"key":"auto"|"none"}}`。旧版从缺省值解码，其余字段取旧缺省值。
- **不新增任何 JSON 字段**：旧版严格解码遇到未知字段会失败，分组会静默回到原版。
- 已存 JSON 已经无法解码（损坏）时，保存用最小 JSON 覆盖。
- 版本校验（CAS）不变：`expected_version` 不匹配返回 409 `SHARE_SCHEDULING_VERSION_CONFLICT`。

### 3.3 `shadow`

已存的 `scheduler=shadow` 一律按 `sub2api` 处理，共三处：
- 请求路径（`groupPolicy`）；
- 分组列表（读的是表列 `scheduler`）；
- 策略视图。

新版不再写入 `shadow`；数据库 CHECK 约束保持不变。视图用 `legacy_shadow=true` 标出这类分组，供界面说明“影子已取消，当前按原版运行”。

### 3.4 回退语义

| 情形 | 行为 |
|---|---|
| 升级到本版本 | 份额分组的层级改取优先级、权重改取负载因子；规则表不再生效；影子分组按原版运行 |
| 新版保存过、回退到 0.4.3 / 0.3.0 | 旧版读到的是保存前的规则与参数（§3.2 只改两个字段），恢复升级前的行为；新版新建的策略在旧版下按旧缺省运行：权重取并发、层级取优先级 |
| 迁移 | 不新增；`backend/migrations/checksums.json` 不动 |

用测试锁定：
1. 生产形态的 v4 策略（影子、缺省权重 equal、10 条带层级与权重的规则、所有旧参数）在新版下的解码结果与选号行为；
2. 新版保存后的 JSON 用 v0.4.3 的 `policy.go` 解码器（测试内嵌一份副本）能解码并通过校验。

## 4. 软溢出改用韧性判定

- 现状：份额对 `temp_unschedulable` 只把 `balance:` 前缀当硬状态（还受韧性失败开关控制），其余停调带恢复时间时一律软溢出，阈值是 `soft_spill_max_seconds`。网关韧性对同一账号的判定是 `resiliencePenaltyOf`：空原因和非软罚前缀的停调为硬，限流超过 `resilience.HardRateLimitThreshold`（300 秒）为硬。两边可能对同一账号得出相反结论。
- 改为：绑定账号不在候选列表或不合格时，用韧性的 `resiliencePenaltyOf`（OpenAI 路径用 `resilienceOpenAIPenaltyOf`）判定。软罚并给出恢复时间时，仍按原规则判断是否软溢出：恢复时间加空闲时长低于 0.8 倍缓存寿命，且请求可缓存。硬罚立即改绑。
- 行为变化：原因为空或不是软罚前缀的停调（例如凭证类、管理员规则类），绑定会话立即改绑，与原版加韧性一致；余额暂停是硬罚。
- 韧性总开关关闭（`FailureOn()` 为 false）时回到 0.3.0：任何带结束时间、且在 300 秒内结束的状态都按软溢出判断（300 秒即旧 `soft_spill_max_seconds` 缺省），`balance:` 暂停按普通停调处理。保持“关闭网关韧性即回到旧行为”的承诺。

## 5. 满载与原版对齐

- **公式**：负载率 = （在途 + 排队）× 100 ÷ `EffectiveLoadFactor()`，整数除法，与原版相同（`ConcurrencyService.GetAccountsLoadBatch`）。负载率 ≥100 视为满载。
- **适用范围**：只约束**新放置与溢出**，即计划中“不是本会话绑定账号”的步骤。亲和命中时的绑定账号沿用原版粘性语义，只看并发槽，槽满时按缓存价值有界等待。原版的粘性层同样不看负载率。
- **读取**：执行器第一次需要尝试非绑定账号时，对本次计划的全部步骤批量读取一次负载（复用原版的短 TTL 缓存）；亲和命中且绑定账号拿到槽位的请求（最常见路径）不读。读取失败时按“未满载”处理，与原版失败时退回旧顺序的做法一致。
- **跳过记录**：负载率满载跳过记为 `saturated`；Codex 自适应并发低于配置并发时记为 `codex_pressure` 的逻辑不变。
- **基准**：交付时对比 500 账号选号基准（`BenchmarkShareSchedulingSelection500`，迁到 `share_scheduling_benchmark_test.go`）改动前后的耗时、分配与 Redis 往返。

## 6. 删除清单

| 删除 | 范围 |
|---|---|
| 影子对比 | 调度器值 `shadow`（读成原版）、影子 goroutine 与并发上限、影子计数与报表字段 |
| 重平衡 | `POST /groups/:id/rebalance` → 410；`ClearBindings` 与 Lua 条件删除；受保护键前缀列表 |
| 纠偏 | `balance.*`、`CorrectionFactor`、`Request.Correction`、`Entry.EffectiveWeight`、定时聚合 `usage_logs` |
| 选号开销 | 抽样耗时与 Redis/数据库计量、报表“选号开销”、`meterIO`。`servertiming` 包本身与其他使用者不动；500 账号基准迁移保留 |
| 决策样本 | `GET /groups/:id/decisions` → 410；Redis 列表 `xy2:share:dec:*`（24 小时后自然过期） |
| 缓存域 | 规则字段、溢出时“同域优先”（溢出排序只剩“同类账号优先”） |
| 排空与排空截止 | 规则字段、`drain_cold`、`drain_deadline`；平滑下线改为“把负载因子调到 1”，立即下线仍是关闭“可调度” |
| 预览旧内容 | `capacity_mismatch`、抽样落点变化、预计迁移会话数 |
| 首字直方图计数 | `ttft:*`、`acct_ttft:*`（首字改由 `usage_logs` 精确统计，§8） |
| 死常量 | `ReasonCompactUnsupported`、`ReasonDrained`、`KeyPromptCache`、`ReasonAdminRebalance` 等不再产生的原因码 |

退役接口返回 HTTP 410，`reason=SHARE_SCHEDULING_ENDPOINT_RETIRED`，参照 `TestRetiredSchedulingRoutesAnswerGone`。

## 7. 管理接口契约

前缀 `/api/v1/admin/share-scheduling`，沿用管理员鉴权与审计日志。响应外层与其他管理接口相同（`{code, message, data}`）。下文只写 `data`。时间为 RFC 3339。比例为 0–1 的小数，没有样本时为 `null`。请求体严格解码，§3.1 所列的旧字段除外。

### 7.1 `GET /settings`、`PUT /settings`

- 响应：`{ "enabled": bool }`。
- PUT 请求：`{ "enabled": bool }`。旧字段 `cache_attribution`、`attribution_sample_rate`、`decision_samples` 接受并忽略，其他未知字段返回 400。

### 7.2 `GET /groups`

返回启用中的分组列表（按分组排序字段），每项：

| 字段 | 类型 | 说明 |
|---|---|---|
| `group_id`、`name`、`platform` | int64、string、string | |
| `scheduler` | `"sub2api"` \| `"share"` | 已存 `shadow` 返回 `sub2api` |
| `session_keep` | bool | 没有策略时为 true |
| `legacy_shadow` | bool | 已存调度器是 `shadow` |
| `configured` | bool | 有策略行 |
| `invalid` | bool | 策略行无法解码（请求路径按原版） |
| `effective` | bool | 总开关开，且 `scheduler=share` |
| `version` | int64 | 0 表示从未保存 |
| `updated_at` | time \| null | |
| `member_count` | int64 | 未删除的成员数（分组列表已有的计数） |
| `available_count` | int64 | 可调度的成员数（同上） |
| `requests_1h` | int64 | 近 1 小时本组成功请求数（`usage_logs`，全部分组一次分组查询） |
| `cache_hit_ratio_1h` | float \| null | 同一查询：Σ缓存读 ÷ Σ（输入 + 缓存读 + 缓存写） |
| `switch_loss_ratio_1h` | float \| null | 近 1 小时“因换号损失的缓存”，来自缓存归因抽样计数（§8） |

三个 1 小时摘要在进程内缓存 30 秒：数据库分组查询一份，归因计数按分组各一份。账号页与份额页频繁刷新时不重复查大表。策略字段与成员计数每次现读。

### 7.3 `GET /groups/:id/policy`

| 字段 | 类型 | 说明 |
|---|---|---|
| `group_id`、`group_name`、`platform` | | |
| `scheduler`、`session_keep`、`legacy_shadow`、`effective`、`version`、`updated_by`、`updated_at` | | 同 §7.2；`updated_by` 是管理员用户 ID 或 null |
| `enabled` | bool | 全局总开关 |
| `invalid` | string \| null | 已存策略无法解码的原因 |
| `active_priority` | int \| null | 当前接新会话的优先级段：有健康成员的最小优先级值；没有健康成员时为 null |
| `members` | MemberView[] | 本组全部未删除成员（含停用、错误状态），按优先级、账号 ID 排序 |

**MemberView**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `account_id`、`name`、`platform`、`type` | | |
| `status` | string | 账号状态（`active` / `inactive` / `error` 等） |
| `schedulable` | bool | 账号“可调度”开关 |
| `healthy` | bool | 当前能接新会话（`Account.IsSchedulable()`）。按请求的门禁（模型、渠道限制等）不在此判断 |
| `reason` | string \| null | `healthy=false` 时的原因码：`disabled`、`inactive`、`not_schedulable`、`rate_limited`、`overloaded`、`temp_unschedulable`、`balance_exhausted`、`expired`、`iq_degraded`、`quota_exceeded` |
| `recover_at` | time \| null | 限流、过载、临时停调的预计结束时间 |
| `priority` | int | 层级 |
| `concurrency` | int | 并发上限 |
| `load_factor` | int \| null | 账号原值，未设为 null |
| `effective_load_factor` | int | 生效负载因子，即 `EffectiveLoadFactor()` |
| `health_factor` | float \| null | 健康系数 0.2–1（§11.1）。健康降权未生效（运维监控关、韧性总开关关、统计过期、查询失败）或样本不足时为 null。未按份额运行的分组按需计算（§11.1），切换确认看到的就是切换后的份额 |
| `health` | object \| null | `{window_minutes, attempts, soft_failures, rate, tier_baseline_rate, insufficient}`：近 30 分钟的尝试数、软罚类失败数、原始失败率与同层基线（0–1）。未生效时为 null；生效但窗口内没有尝试时为对象，`insufficient=true`、`rate=null` |
| `ramp_factor` | float | 恢复爬升系数 0.1–1（§11.2）；不在爬升时为 1 |
| `ramp` | object \| null | 爬升中为 `{cause, since, full_at}`，`cause` 只取 `balance_pause`、`hard_penalty`、`reenabled`；不在爬升时为 null |
| `effective_weight` | float | 有效权重 = `effective_load_factor` × 健康系数 × 爬升系数，至少为负载因子的 5%，保留 2 位小数；由 `shareWeight` 一处给出。两个系数按 0.05 量化，输出为干净的小数（如 0.35） |
| `base_share` | float | 只按负载因子计算的份额，用来对照自动调整了多少 |
| `other_group_ids` | int64[] | 该账号所在的其他分组（不含本组） |
| `expected_share` | float | 预计新会话份额，见下文“份额口径”，按有效权重 |
| `share_if_healthy` | float | 只有该账号恢复健康、其余成员保持现状时的预计份额，按有效权重。健康账号等于 `expected_share` |
| `actual_requests_1h` | int64 | 近 1 小时本组内由该账号完成的成功请求数（`usage_logs`，按本组） |
| `actual_share_1h` | float \| null | 占本组近 1 小时成功请求的比例 |

账号的失败率不在本节：PM 2026-10-20 决定成员视图只保留一个失败率口径，即自适应权重的 `health.rate`（§11），原先计划的 `recent_requests_1h`、`recent_errors_1h`、`recent_error_rate_1h` 不提供。

**份额口径**：份额计算只取决于成员的优先级、负载因子与健康状态，不取决于分组当前的调度方式。所以“当前状态（份额模式）”与“切到份额后”是同一个数，`expected_share` 对原版分组同样有效，供切换确认用。原版分组的现状由 `actual_share_1h` 表达。

- 计算：活动优先级段内的健康成员按 `effective_weight` 占比分配，其余成员为 0；`base_share` 用 `effective_load_factor` 按同样规则计算。
- 不含满载溢出（§5）与按请求的门禁，是新会话份额的期望值，不是请求份额的承诺。

**开销**：本组按账号的近 1 小时成功请求数走 `usage_logs (group_id, created_at)` 索引，每个视图 1 条 SQL；结果在进程内缓存 30 秒，键为分组 ID，**只缓存请求数**。成员列表、优先级、负载因子、健康状态与份额每次现读、现场计算，所以修改负载因子后视图立即反映。

### 7.4 `PUT /groups/:id/policy`

- 请求：`{ "scheduler": "sub2api"|"share", "session_keep": bool, "expected_version": int64 }`，三项必填；`expected_version=0` 表示新建。
- 兼容旧页面：请求体为旧形状 `{ "expected_version", "policy": {…} }` 时，只取 `policy.scheduler` 与 `policy.affinity`（按 §1 的读法），其余忽略。蓝绿期间旧页面可能提交 `scheduler:"shadow"`，**按 `sub2api` 写入**（新形状提交 `shadow` 返回 400）。
- 调度方式发生变化时，同时写切换锚点（§7.7）。
- 写入规则见 §3.2。
- 响应：同 `GET /groups/:id/policy`。
- 错误：
  - 400 `SHARE_SCHEDULING_POLICY_INVALID`（`metadata.problems`）；
  - 404 `SHARE_SCHEDULING_GROUP_NOT_FOUND`；
  - 409 `SHARE_SCHEDULING_VERSION_CONFLICT`；
  - 503 `SHARE_SCHEDULING_UNAVAILABLE`。

### 7.5 `POST /groups/:id/preview`

- 请求：`{ "scheduler", "session_keep" }`，可带 `expected_version`（忽略）。旧形状 `{ "policy": {…} }` 同 §7.4。
- 不写入任何东西。
- 响应：

| 字段 | 类型 | 说明 |
|---|---|---|
| `scheduler`、`session_keep` | | 回显请求 |
| `enabled` | bool | 总开关 |
| `effective_after` | bool | 保存后是否按份额运行：`enabled && scheduler=share` |
| `active_priority` | int \| null | 同 §7.3 |
| `members` | MemberView[] | 同 §7.3 |
| `warnings` | Warning[] | 只在 `scheduler=share` 时给出；切回原版时为空数组 |

**Warning**：`{ "code": string, "account_id": int64|null, "priority": int|null, "value": float|null }`

| `code` | 条件 | 附带字段 |
|---|---|---|
| `global_switch_off` | 总开关关闭 | — |
| `no_healthy_account` | 没有任何健康成员（请求只能走末位兜底或无号兜底分组） | — |
| `top_priority_unhealthy` | 数值最小的优先级段没有健康成员，新会话会落到更低的段 | `priority`=最高段，`value`=当前活动段 |
| `single_account_dominant` | 某账号 `expected_share` >0.8，且活动段至少有 2 个健康成员 | `account_id`，`value`=份额 |
| `high_load_factor` | 成员的 `effective_load_factor` ≥999（健康与否都报，恢复后会拿到大部分新会话） | `account_id`，`value`=负载因子 |
| `ramping` | 成员正在恢复爬升（提示级） | `account_id`，`value`=爬升系数 |
| `health_downweighted` | 成员的健康系数 ≤0.5 | `account_id`，`value`=健康系数 |

### 7.6 `POST /groups/:id/explain`

- 请求：`{ "model": string, "session_hash": string?, "estimated_tokens": int?, "cache_one_hour": bool?, "body": object? }`。
- `session_hash` 缺省或为空时，后端生成一个随机会话键，结果等同一个新会话，`simulated_new_session=true`。每次调用的落点按权重随机，可能不同。
- `model` 可以为空字符串，表示不按模型过滤：按分组内任一可服务账号的视角模拟（模型支持与模型级限流的门禁不参与）。
- 不占槽位、不写绑定；分组在原版模式下同样可用。
- 响应：

| 字段 | 说明 |
|---|---|
| `group_id`、`platform`、`scheduler`、`version` | |
| `simulated_new_session` | bool |
| `key_source`、`session`（会话键指纹）、`bound_account_id`、`idle_ms` | |
| `cache_ttl_seconds`、`estimated_tokens`、`cacheable`、`warm` | |
| `wait_budget_ms`、`max_waiting` | |
| `reason`、`steps`（账号 ID 顺序）、`write`、`spill_write`、`spill_reason`、`target`、`ideal`、`active_priority` | |
| `entries[]` | `{account_id, priority, load_factor, health_factor, ramp_factor, effective_weight, eligible, excluded, reason, rank, class, load_rate, saturated}`：`load_factor` 是生效负载因子；`health_factor`、`ramp_factor`、`effective_weight` 同 §7.3；`load_rate` 是 §5 的负载率（按负载因子），读不到时为 null；`saturated` 表示 §5 意义上的满载 |
| `resilience` | 网关韧性解释（不变） |

与 0.4.3 相比：`active_tier` 改名为 `active_priority`；`entries.tier`、`entries.weight` 改名为 `priority`、`load_factor`；`effective_weight` 改为负载因子乘两个系数（§11）；删除 `configured`、`drain`、`cache_domain`。

### 7.7 `GET /groups/:id/report?window=1h`

- `window` 为 Go 时长，1m–24h，缺省 1h。
- **切换锚点**：只在调度方式变化（`sub2api` ↔ `share`）时记录切换时间，写入 Redis 键 `xy2:share:switched:<group_id>`。
  - 值为毫秒时间戳，TTL 194 小时，与 `cache` 类小时桶保留期一致。
  - 只改会话保持的保存不写这个键。
  - 不改表结构，也不加 JSON 字段。
  - 键不存在（例如升级前切换、键已过期、Redis 写失败）时，回退到策略行的 `updated_at`。
  - 响应用 `anchor_source` 标明锚点来源：`switch_record` 或 `policy_updated_at`。
- **窗口**：
  - 只在 `scheduler=share` 时给出基线。基线 = 锚点前的等长窗口 `[锚点 − window, 锚点)`。
  - 锚点落在当前窗口内时，`current` 从锚点开始算 `[锚点, now)`，不混入切换前的数据；前端据此提示“切换后数据只有 N 分钟”。
  - 锚点早于当前窗口时，`current = [now − window, now)`。
  - 理由：健康卡要回答“切到份额后是否变差”。“上一个等长窗口”在切换一天后比较的是份额对份额，发现不了回归。
- 响应：

| 字段 | 说明 |
|---|---|
| `group_id`、`scheduler`、`session_keep`、`version`、`window_seconds` | |
| `current_from`、`current_to` | 当前窗口的实际起止（可能因切换而短于 `window`） |
| `current` | Metrics |
| `switched_at` | time \| null：锚点时间 |
| `anchor_source` | `"switch_record"`、`"policy_updated_at"` 或 null |
| `baseline_from`、`baseline_to` | time \| null：基线窗口的实际起止 |
| `baseline` | Metrics \| null |
| `baseline_unavailable` | `"not_share"`、`"never_switched"`、`"out_of_retention"` 或 null。`out_of_retention` 只影响归因三项：数据库各项照常给出，`baseline` 仍返回 |
| `accounts[]` | `{account_id, requests, request_share, tokens, token_share, standard_cost, cost_share, skipped}`，统计当前窗口。前六项来自 `usage_logs`；`skipped` 是 `{原因: 次数}`，**仅份额模式** |
| `details` | `{cache_classes: {类别: {requests, tokens}}, reasons: {原因: 次数}, waits: {count, hit, avg_ms}, attempts, share_requests, hot_migrations: {total, by_reason: {原因: 次数}}}`，统计当前窗口。`cache_classes` 两种模式都有；其余**仅份额模式**，原版模式下不返回这些键；`avg_ms` 没有等待时为 null。`hot_migrations` 是份额把会话从缓存仍热的账号移走的次数，是就绪评估的回退判据之一（超过请求数的 0.1%） |
| `counters_error` | string \| null，Redis 读取失败时的说明 |
| `counter_precision` | `"minute"` 或 `"hour_rounded"`：基线、以及超过 2 小时的窗口按整点小时汇总 |

**Metrics**（健康卡的 4 个指标都在这里，各附样本数）：

| 字段 | 来源 | 原版模式有数据？ |
|---|---|---|
| `requests` | `usage_logs` 本组成功请求数 | 有 |
| `cache_hit_ratio` | `usage_logs`：Σ`cache_read_tokens` ÷ Σ（`input_tokens` + `cache_read_tokens` + `cache_creation_tokens`）。精确值；样本数 = `requests` | 有 |
| `ttft_p50_ms`、`ttft_p95_ms`、`ttft_samples` | `usage_logs.first_token_ms` 的分位（只有流式请求有值） | 有 |
| `errors`、`error_rate` | `ops_error_logs` 本组客户可见错误（口径同 §7.3，按 `group_id` 过滤，走 `(group_id, created_at)` 索引）；`error_rate = errors ÷ (requests + errors)` | 有 |
| `attribution_samples` | 缓存归因抽样请求数（约 1/4 会话） | 有 |
| `switch_rate` | 抽样中 `class:miss_switch_*` 的请求占比：缓存未过期就换号 | 有 |
| `switch_loss_ratio` | 抽样中 `class_tokens:miss_switch_*` 的输入 Token 占比：因换号损失的缓存 | 有 |

份额专属的尝试次数、等待、决策原因、跳过原因只在 `details` 与 `accounts[].skipped` 里出现，并标为“仅份额模式”，不进入 Metrics。

### 7.8 `GET /accounts/:id/rules`

路径不变，含义改为“该账号在各份额分组里的预计份额”。只列已存调度器为 `share` 的分组，按分组 ID 排序：

`[{ "group_id", "group_name", "scheduler": "share", "effective": bool, "priority", "load_factor", "effective_load_factor", "healthy", "expected_share", "share_if_healthy" }]`，两种份额按有效权重（§11）。

### 7.9 退役接口

`POST /groups/:id/rebalance` 与 `GET /groups/:id/decisions` 返回 410：

```json
{"code":410,"message":"…","reason":"SHARE_SCHEDULING_ENDPOINT_RETIRED"}
```

### 7.10 修改负载因子

走现有账号接口 `PUT /api/v1/admin/accounts/:id`，请求体只传 `{ "load_factor": n }` 即为部分更新，其他字段不变。

- `n ≤ 0` 清除（回到取并发）。
- `n > 10000` 返回错误（现状为通用错误，前端提交前校验 1–10000）。
- 份额选号读调度快照，修改在快照刷新后生效（秒级）；管理端视图直接读数据库，立即可见。

## 8. 观测数据来源与保留期

- **数据库**：
  - 请求数、缓存命中率、首字分位、错误率取自 `usage_logs` 与 `ops_error_logs`。两种模式都有数据，精确，保留期跟随表本身（`ops_error_logs` 缺省 30 天）。
  - 报表每次最多查两个窗口（当前与基线），窗口 ≤24 小时，走 `(group_id, created_at)` 索引，只在管理端调用。
- **Redis 归因计数**（缓存归因，按会话 1/4 抽样，所有分组常开）：只保留类别与 Token 合计字段，即 `requests`、`prompt_tokens`、`cache_read_tokens`、`cache_creation_tokens`、`class:*`、`class_tokens:*`，共 26 个以内。
  - 删除首字直方图后，单个哈希在 Redis 的 listpack 编码下约 1–2 KB。
  - `cache` 类计数的小时桶保留期从 26 小时延长到 194 小时（8 天加 2 小时），每个有流量的分组约增加 0.2–0.4 MB。删除首字直方图（原来每账号最多 15 个字段）省下的内存，在多数分组上大于延长保留期增加的量。
  - `share` 类计数只在份额分组产生，保留期仍为 26 小时，只写报表读取的字段：`requests`、`attempts`、`reason:*`、`skip:<账号>:<原因>`、`wait_count`、`wait_hit`、`wait_ms`、`hot_migration`、`hot:<原因>`。0.4.3 写的 `served:*`、`ideal:*`、`ideal_skip:*`、`key:*` 不再写入，旧键随保留期过期。
  - 分钟桶仍为 2 小时 10 分。
- **基线读取**：基线窗口整体在过去，只读小时桶，所以 `counter_precision` 为 `hour_rounded`；超出 194 小时时，归因三项为 null，`baseline_unavailable=out_of_retention`，数据库三项照常给出。
  - 小时桶取锚点所在整点之前的整点小时，个数为窗口四舍五入到小时（至少 1 个），不含锚点所在小时，所以切换之后的数据不会混入基线。
- **语句超时**：管理端对 `usage_logs`、`ops_error_logs` 的读取都在只读事务里执行，事务内 `SET LOCAL statement_timeout = '5s'`，大表异常时由数据库中止查询，不拖住连接。
- **执行计划**（本地 PostgreSQL 18，合成数据 `usage_logs` 300 万行 / 7 天 / 40 组 / 600 账号，`ops_error_logs` 30 万行；只读验证，未连任何服务器）：

  | 查询 | 用到的索引 | 耗时（冷缓存） |
  |---|---|---|
  | 列表摘要：全部分组近 1 小时 `GROUP BY group_id` | `created_at` 前缀的索引（PG 18 选 `idx_usage_logs_request_type_created_at` 的跳跃扫描；无跳跃扫描的版本走 `idx_usage_logs_created_at`），窗口只有 1 小时 | 61 ms |
  | 成员请求数、报表账号表：一组 24 小时 `GROUP BY account_id` | `idx_usage_logs_group_created_at_not_null` | 117 ms |
  | 报表数据库指标：一组 24 小时，含首字分位 | `idx_usage_logs_group_created_at_not_null` | 6 ms（热） |
  | 报表错误数：一组 24 小时 | `idx_ops_error_logs_group_time` | 18 ms |
  | 健康降权成功数：100 个账号近 30 分钟 | `idx_usage_logs_account_created_at`（仅索引扫描） | 29 ms |
  | 健康降权失败尝试：100 个账号近 30 分钟，展开 `upstream_errors` | `created_at` 前缀的索引（PG 18 选 `idx_ops_error_logs_resolved_time` 的跳跃扫描；无跳跃扫描的版本走 `idx_ops_error_logs_created_at`），再逐行展开 | 106 ms |

  原始输出在仓库外 `xy2api-artifacts/share-simplify-20261020/pg/explain-3x.txt`、`explain-r1.txt`。健康降权的两条查询每 60 秒各执行一次，只在至少一个份额分组生效时执行。

## 9. 同步登记

- 上游钩子不变：`policy.json` 的 required_literals 第 15–21 条、3 个具名补丁与其绑定用例名、`scheduler-diff.json` 的 4 个块，均不受影响。
- `policy.json` 的 required_literals 新增 `AccountsView.vue` 的两处份额钩子：`useAccountShareColumn(() => params.group)` 与 `if (!nextSchedulable) void notifyShareImpact(a)`。
- `policy.json` 的 `xy_owned` 新增本变更目录。
- `backend-ci.yml` 点名的 7 个份额用例保留原名，夹具改为账号负载因子。
- `tools/docs_consistency.json` 的 `retired` 登记删除的字段名、路由名与前端文案键，只在历史记录、兼容测试与“已移除”说明中放行。

## 10. 风险

| 风险 | 处理 |
|---|---|
| 升级瞬间份额分组的新会话分布改变：负载因子很大的账号拿到绝大多数新会话 | 升级说明写明；预览给出 `high_load_factor`、`single_account_dominant`；建议升级前在切换确认框核对预计份额 |
| 改负载因子同时影响该账号所在的原版分组 | 视图给出 `other_group_ids`，界面提示；方向与原版一致 |
| 回退后规则恢复为升级前的内容，不包含升级期间对负载因子的调整 | 升级说明写明；规则只在旧版生效 |
| 满载对齐多一次批量负载读取 | 只在需要尝试非绑定账号时读，并复用原版缓存；交付时给出基准对比 |

## 11. 自适应权重（PM 2026-10-20 追加，0 个设置项）

依据仓库外 `G_ECOSYSTEM_RESEARCH.md` §1 的 R1–R3 与 `D_PM_SYNTHESIS.md` §10。有效权重 = 负载因子 × 健康系数 × 爬升系数，至少为负载因子的 5%，在 `shareWeight` 一处计算，返回分解结构供成员视图、预览与解释使用。系数只作用于新会话、冷会话重对齐与溢出顺序的 HRW 权重；亲和命中、满载判定（负载率仍按负载因子）与层级不受影响。韧性总开关关闭时两个系数都为 1。

### 11.1 健康降权（R1）

- **口径**：账号级，近 30 分钟，窗口按整分钟对齐。失败只计韧性给软罚的尝试：`resilience_category` 为 `TRANSIENT`、`FIRST_OUTPUT_TIMEOUT`、`STALL`，以及类别为空、`kind=request_error`、状态 0 的传输错误（运维记录不给传输错误分类，韧性把它判为 `TRANSIENT`；消息含 `context canceled` 的客户端取消不计）。余额、鉴权、限流、降载、请求错误、模型不支持都不计。判定是一个纯函数 `shareSoftFailureAttempt`，输入一条 `upstream_errors` 元素。
- **分母**：尝试 = 成功数（`usage_logs` 行数）+ 软罚类失败尝试数。非软罚失败分子分母都不计。
- **数据来源**：失败取 `ops_error_logs.upstream_errors` 每次尝试的 `account_id` 与 `resilience_category`（请求最终成功时之前失败的尝试也会落一条恢复行，行本身不计，其中的尝试计入）；成功取 `usage_logs`。
- **基线**：同一分组、同一优先级的健康成员中，尝试数 ≥20 的最低原始失败率；没有成员满足时取尝试数 >0 的最低值。同层少于 2 个健康成员时系数为 1。
- **系数**：`p̂ = (失败 + 20 × 基线) ÷ (尝试 + 20)`；`超出 = max(0, p̂ − 基线 − 3 个百分点)`；`系数 = max(0.2, 2^(−超出 ÷ 5 个百分点))`，量化到 0.05。窗口内没有尝试时为样本不足，系数为空（×1）。
- **刷新**：后台每 60 秒一次，读各生效份额分组的成员，按窗口一次读取全部成员账号的成功数与失败尝试（两条 SQL，5 秒语句超时），按（分组, 账号）算出系数表，每组带自己的读取时间。请求路径与管理视图查同一张表。进程启动后立即刷新一次。
- **切换即生效**：保存使分组按份额运行的实例，在返回响应前同步算出该组的健康表并写入（切换确认刚按需算过时直接复用），所以响应与之后的选号立即计入健康系数。其他实例收到策略或总开关广播、发现有份额分组还没有健康表时，立即触发一次刷新（多次触发合并）。
- **未按份额运行的分组**（原版分组、总开关关闭时的份额分组）：成员视图、预览、解释与账号份额打开时按需计算一次，口径、窗口与函数同上，范围只限该组成员，按组缓存 60 秒。切换确认因此显示切换后的真实预计份额。韧性总开关或运维监控关闭时同样为空；`reenabled` 爬升只观察生效份额分组，其他分组的爬升只按账号行判断。
- **失效保护**：统计超过 5 分钟未更新、查询失败、运维监控关闭（配置 `ops.enabled=false` 或设置 `ops_monitoring_enabled`）、韧性总开关关闭时，系数一律为空（×1），视图 `health=null`。
- **生产回放**（就绪评估 B4，失败率 1.1% / 9.0% / 20.8%）：系数 1 / 0.5 / 0.2，新会话份额 58.8% / 29.4% / 11.8%（原版实际 52.2% / 27.0% / 13.0%）。调研报告按近似公式算出 292 为 0.56，按本节公式精确计算为 0.50。
- **精度边界**：卡流不写入 `upstream_errors`，`upstream_errors` 被截断时（`dropped_earlier_attempts`）早期尝试缺失，运维监控关闭时没有数据。这些都只会少降权，不会误降权。后续若生产出现“卡流为主、未被降权”的账号，改由韧性在施加软罚处写 Redis 分钟计数（tasks §8）。

### 11.2 恢复后慢启动（R2）

- **触发**：账号重新合格时，以下三种之一发生在 10 分钟内：
  - 余额暂停到期：`temp_unschedulable_until` 已过、原因以 `balance:` 开头（`cause=balance_pause`）；
  - 硬罚到期：`temp_unschedulable_until` 已过且原因是硬档，或 `rate_limit_reset_at` 已过且限流时长超过 300 秒（`cause=hard_penalty`）；
  - 本实例观察到连续不合格至少 5 分钟后转为合格（停用、错误、关闭可调度、管理员清除暂停等账号行看不出的情况，`cause=reenabled`）。观察由 §11.1 的刷新完成，最多晚 60 秒；新进程没有记忆时不爬升。

  多个原因同时成立时取最近的一次恢复。30 秒级软冷却不触发。
- **系数**：`0.1 + 0.9 × min(1, 已恢复时长 ÷ 10 分钟)`，量化到 0.05。
- 前两种来自账号行，各实例一致；第三种依赖进程内观察，生产为单实例加蓝绿，可接受。

### 11.3 可解释

成员视图给出 `health_factor`、`health`、`ramp_factor`、`ramp`、`effective_weight`、`base_share` 与 `expected_share`（§7.3）；解释的条目给出 `load_factor`、`health_factor`、`ramp_factor`、`effective_weight`（§7.6）；预览增加 `ramping` 与 `health_downweighted`（§7.5）。界面的失败率列用 `health.rate`，与降权同一口径。
