# XY2API 账号调度现状分析与外部调研（2026-10-09，2026-10-10 补充缓存与首字专项）

> 基线：`main` = `3569d4f00aae3eada079eb7ac058dc62256a66b8`（产品 0.2.5，Sub2API 兼容 0.2.14）。上游对照：Wei-Shaw/sub2api `v0.2.14` 与今日发布的 `v0.2.15`（调度相关的 `gateway_scheduling.go`、`openai_account_scheduler.go` 在两版之间零差异）。本文只做分析与调研；新调度方案见 [OpenSpec 变更](../openspec/changes/replace-controlled-with-share-scheduling/proposal.md)。

## 0. 结论摘要

1. **系统里同时存在两套调度**，由 `scheduling_system_mode` 单行表选择：`sub2api`（上游原版）与 `controlled`（智能可控调度）。**表中无记录时默认是 `controlled`**（`backend/internal/scheduling/mode.go:31-37`），而且**每个网关请求都会同步读一次 PostgreSQL** 获取模式，读失败直接返回 503（`backend/internal/service/controlled_scheduling_request.go:337-370`）。
2. **Sub2API 原版里根本没有“账号流量权重”**。Anthropic/Gemini/Antigravity 路径是“模型路由 → 粘性会话 → 最小优先级 → 最低负载率 → LRU → 随机”；`load_factor` 只在有并发占用时影响负载率，低并发下同优先级账号近似均分。OpenAI 实验调度器把优先级归一化成 8 个评分因子之一再 Top-K 加权随机，**备用优先级账号在空闲时也能拿到约 33% 的流量**（见第 6 节仿真）。
3. **智能可控调度**有严格层级 + 层内 SWRR，但它把“健康状态机、容量溢出、恢复探针、重试预算、故障域、首输出超时”等大量自动机制叠加在分配之上，分配状态按“候选成员集合”分桶（成员一变就换一个 SWRR 计数器），且**完全不做会话亲和**（只保留协议强续接），对 Claude/OpenAI 的提示缓存不友好。它约 6.0k 行核心 + 4.9k 行服务适配 + 1.2k 行管理接口，侵入 65 个非测试文件、10 个迁移和整条 HTTP 上游链路。
4. “调了优先级和权重仍不可控”的根因不是参数没调好，而是**两套算法都没有把“管理员意图”和“自动避让”分层**：自动因素以评分、隐式排序、未记录的回退等形式直接改写分配结果，且不给出可追溯的偏差原因。
5. 外部项目的共识做法是：**硬优先级分层 + 层内显式权重 + 会话亲和（粘性有 TTL，失效后按权重重分配）+ 冷却/熔断作为显式闸门 + 决策可追踪**（new-api、LiteLLM、Portkey、APISIX、Kong、AxonHub、OmniRoute 等）。分数混合式“自适应”路由（Bifrost 企业版、AxonHub 评分、ranxi2001 分支的优先级调度）效果强但牺牲人为可控性。
6. **缓存与首字专项（第 7 节）**：上游缓存按账号（或同一缓存域）隔离、寿命从请求开始计并命中续期，所以任何调度都只能靠“同一会话留在同一账号”保住缓存。原版在四处损失缓存或首字：
   - 无会话标识的 Anthropic 客户端，会话哈希逐轮变化或跨 Key 共用；
   - 粘性排队最长 120 秒；
   - 排队满即永久改绑；
   - 账号限流 1 秒也删除绑定。

   2026-10-09 的初版份额规则，因“溢出不改绑 + 高层稳定即迁回”，在仿真中比原版缓存命中率低 3.3–6.5 个百分点。修订后的规则在四个场景下与原版持平，TTFT P95 为原版的 0.67–0.93 倍。修订规则包括：
   - 按缓存价值有界等待；
   - 溢出即改绑；
   - 短时不可用软溢出；
   - 只在缓存变冷时做策略性迁移。

## 1. 调度入口与模式切换

| 入口 | 文件 | 用途 |
|---|---|---|
| `GatewayService.SelectAccountWithLoadAwareness` | `backend/internal/service/gateway_scheduling.go:107` | Anthropic / Gemini 原生 / Antigravity / 混合调度主路径，带并发槽与等待计划 |
| `GatewayService.SelectAccountForModelWithExclusions` | 同文件 `:34` | 非负载感知路径（count_tokens 等） |
| `OpenAIGatewayService.selectAccountWithScheduler` | `backend/internal/service/openai_account_scheduler.go:2164` | OpenAI/Grok 等兼容平台主路径（含实验调度器分支） |
| `OpenAIGatewayService.SelectAccountWithLoadAwareness` / `SelectAccountForModelWithExclusions` / `SelectAccountForTokenCount` | `backend/internal/service/openai_gateway_scheduling.go` | 旧公共入口与辅助请求 |
| `GeminiMessagesCompatService.SelectAccountForModelWithExclusions` / `SelectAccountForAIStudioEndpoints` | `backend/internal/service/gemini_messages_compat_service.go` | Gemini 兼容路径 |
| `SelectAccountByPreviousResponseID`、`SelectMediaVideoRequestAccount` | `openai_ws_forwarder_support.go`、`grok_media.go` | 协议强续接（previous_response_id、媒体任务） |

模式切换的实现：

- 迁移 263 建表 `scheduling_system_mode`，注释明确“保留已部署的 controlled，直到管理员显式选择 Sub2API”。`GetSchedulingMode` 查不到行时返回 `controlled`。
- `ControlledSchedulingMiddleware` 挂在全部网关路由（`backend/internal/server/routes/gateway.go:32-34,198,357,380,397,500,510,528`），每个请求 3 秒超时读模式；失败返回 `503 Scheduling mode is temporarily unavailable`。读到 `sub2api` 后才放行原版路径。
- 进入原版后，各入口里的 `selectControlledGatewayAccount` / `loadPolicy` 分支（`gateway_scheduling.go:78,138`，`openai_account_scheduler.go:2179-2194`）判定后回落原版代码。
- `ProvideControlledSchedulingService` 把 OpenAI、Gateway、Gemini、Antigravity 四个服务的 `httpUpstream` 全部包装成 `controlledHTTPUpstream`（`backend/internal/service/wire.go:1083-1098`），原版模式下也会经过该包装（大多直接透传）。

## 2. Sub2API 原版调度（当前集成版本）

xy2api 对上游三个调度文件的改动仅是：插入 controlled 钩子、`isOpenAIAccountRequestRuntimeBlocked` 增加 `requireCompact` 参数、OpenAI 质量路由钩子、Codex 有效并发钩子。`scheduler_snapshot_service.go`、`concurrency_service.go` 与上游 v0.2.14 字节一致（模块路径除外）。

### 2.1 Anthropic / Gemini / Antigravity：`SelectAccountWithLoadAwareness`

1. **Layer 1 模型路由**（`gateway_scheduling.go:287-532`）：分组 `model_routing` 命中时，只在路由账号里选；路由账号内仍按“粘性 → 优先级 → 负载率 → LRU”排序，全部不可用才回落普通选择。**它优先于粘性会话与一切优先级。**
2. **Layer 1.5 粘性会话**（`:534-654`）：会话哈希 → 账号 ID（Redis，TTL 1 小时，命中刷新，`gateway_service.go:36`）。账号可用即直接用；槽位满且等待人数 < `sticky_session_max_waiting`(3) 时返回等待计划（默认最长 120 秒）。
3. **Layer 2 负载感知**（`:656-788`）：候选过滤后执行
   `filterByMinPriority → (可选 filterBySoonestReset) → filterByMinLoadRate → selectByLRU`。
   - 负载率 = `(当前并发 + 等待数) × 100 / load_factor`，取整数（`backend/internal/repository/concurrency_cache.go:999-1001`），批量结果有 200ms 缓存。
   - LRU 使用 `last_used_at`，该字段**每 10 秒批量刷库**（`deferred_service.go`、`wire.go:461`）再经 outbox（默认 1 秒轮询）进入调度快照。请求很快结束、各账号负载率又回到相同值时，10 秒内的请求会反复选中同一个“最久未用”账号，表现为短时集中；只有并发真正堆积时负载率才把请求推向其他账号。
   - 同 (优先级, 负载率, last_used 秒) 的账号组内随机打散（`shuffleWithinSortGroups`）。
4. **Layer 3 兜底排队**（`:790-804`）：按优先级 + LRU（或随机）返回等待计划，最多等待 30 秒、100 人。

会话哈希来源（`gateway_service.go:902-966`）：`metadata.user_id` 中的 session → 带 `cache_control` 的可缓存内容 → (IP、UA、API Key、system、全部消息) 摘要。

- 第二级取“第一条带断点的消息”的文本（`gateway_service.go:1060-1250`），客户端把断点放在最新消息时逐轮变化；只有 system 带断点时取 system 文本，不含 API Key，同一分组里不同租户的会话共用一个哈希。
- 第三级每轮对话消息变长，哈希随之变化。

因此**无显式会话标识的客户端基本没有跨轮粘性**，详见 §7.3。

### 2.2 OpenAI 默认（旧）路径：`selectAccountWithLoadAwareness`

`openai_gateway_scheduling.go:1159-1519`：粘性会话（可在等待队列满时一次性溢出且不改绑定）→ 优先级 → 负载率 → LRU → 组内随机；**若开启“低上游倍率优先”，会再按倍率做一次稳定排序（`:1381-1385`），倍率直接压过优先级**；之后逐个做内存/数据库复检再抢槽，最后兜底排队。

### 2.3 OpenAI 实验调度器（`openai_advanced_scheduler_enabled`）

`openai_account_scheduler.go:383-1130`：

- 层次：`previous_response_id` 强续接 → guardian 父账号 → 会话粘性（TTFT EWMA > 15 秒、错误率 EWMA > 0.5 或并发满时“逃逸”）→ 负载均衡。
- 评分（`:991-1040`）：
  `score = 1.0×优先级因子 + 1.0×(1-负载率) + 0.7×(1-等待/最大等待) + 0.8×(1-错误率) + 0.5×TTFT因子 (+重置/额度/成本，默认 0)`，
  优先级因子把候选中最小/最大优先级线性归一到 1..0。
- 取 Top-K（默认 7）后按 `score - min + 1` 作为权重加权随机排序，再依次抢槽（`:805-858`）。有会话锚点时随机种子由会话哈希派生，**同一会话的随机顺序固定**。
- “粘性加权”模式把硬粘性改成 +5/+3 分加成；“订阅优先”先在订阅账号池内选。
- 运行时统计（错误率、TTFT EWMA）只存在**单进程内存**，多实例之间各不相同。

### 2.4 资格闸门（两套模式共用的硬条件）

账号 `IsSchedulable()`（`account.go:166-187`）：启用、`schedulable` 开关、IQ 未降级、未过期、未处于 `overload_until`(529)、`rate_limit_reset_at`(429)、`temp_unschedulable_until`（错误规则/OAuth 401/流超时/利用率阈值等）、API Key/Bedrock 额度未超。另有：模型支持与模型级限流、窗口费用与 RPM（超阈值后“仅粘性可用”）、最大会话数、渠道定价限制、分组利润门、隐私要求、平台/传输/能力匹配、Codex 用量自动暂停、影子母账号健康、Grok 免费额度与团队限流、代理流隔离（全部隔离时失败开放）等。

这些闸门是合理的“真实不可用”信号，但**当前没有一个统一的地方告诉管理员“某账号这段时间因为哪个闸门少拿了多少流量”**。

### 2.5 失败与故障转移

`backend/internal/handler/failover_loop.go`：同账号可重试错误先原地重试（账号级 `pool_mode_retry_count`），用尽后 `TempUnscheduleRetryableError`，账号加入本请求排除集，再重新选号；切换上限由 `gateway.max_account_switches` 控制（默认 10）。已向客户端写出内容后禁止切换。粘性会话切换账号时开启“强制缓存计费”——把用户侧输入 token 按缓存读计价，**上游缓存失效的成本由站点承担**。

## 3. 智能可控调度（controlled）

### 3.1 规模与侵入面

| 部分 | 规模 |
|---|---|
| `backend/internal/scheduling/`（34 个源码 + 34 个测试） | 6,002 + 6,062 行 |
| `internal/service` 中 controlled 专属源码 / 测试 | 4,896 / 6,182 行 |
| 管理接口 `handler/admin/scheduling_*` | 1,195 行 |
| 含 controlled 判断的非测试文件 | 65 个（含 handler 故障转移循环、转发器、限流服务、计费恢复） |
| 迁移 | 257–266 共 10 个（策略、控制、尝试票据、故障域、健康修订、反馈队列、分组策略、模式、未知恢复、单账号开关；266 为通用账号 `updated_at` 单调触发器） |
| CI | `backend-ci.yml` 中“Scheduling reliability”真实 PG/Redis 必过门禁点名多个 `TestControlled*` |
| 同步策略 | `tools/upstream-sync/policy.json` 的 `typesafe-controlled-settlement` 自有补丁及其测试 |

### 3.2 算法

- 分组策略：每账号 `priority` / `traffic_weight`，未配置账号继承账号优先级与权重 1（`scheduling/account_pool.go:103-145`）。
- 选择：严格取“有可用容量的最高层”，层内按权重做共享 SWRR（Redis Lua），容量不足时溢出到下一层（标记 `capacity_overflow`，`account_pool.go:20-90`）。
- 分配状态键 = 分组 + 层 + 首次/重试/owner 类型 + **成员集合及权重**（`account_pool.go:178-200`）。任何账号因容量、健康、模型能力进出候选，都会切换到另一套计数器，长期比例只在“成员集合不变”的子序列里成立。
- 健康：近 5 次中 3 次可归因失败即熔断，冷却 30/60/120/240/300 秒；半开探针并发 1；恢复按 10%→30%→100% 分阶段，每阶段至少 30 秒与 3 个合格终态（`scheduling/README.md`）。
- 请求预算：默认最多 3 个账号、单次首有效输出 120 秒、总等待 240 秒，同账号最多一次，先取消再换号。
- 每个请求的热路径：读模式（PG）→ 读分组策略（PG）→ 账号活跃数（PG）→ 候选准入（PG，含凭据 JSONB）→ 健康快照与 SWRR 预留（Redis Lua）→ 选中账号再 `GetByID`（PG）→ 派发票据写入与租约维护（PG）→ 终态意图与结算确认（PG）。
- 语义改写：controlled 请求关闭了原版的 5xx/过载冷却、流超时惩罚、空响应惩罚、同账号重试、签名/历史剥离重放等，改由自身观测器处理（`scheduling/README.md` “Adapter boundary”）。同一个上游错误在两种模式下导致的账号状态不同。
- 亲和：OpenAI 只保留 `previous_response_id`、guardian、质量钉住三类强续接（`openai_controlled_scheduling.go:22-90`）；Anthropic/Gemini 完全不读粘性绑定。**同一会话的后续请求会被 SWRR 轮到其他账号，上游缓存命中率与首字时延都会受损。**

### 3.3 历史演进

V2→V5 设计、按模型策略 → 按分组策略 → 双模式 → 一键开关，9 月 27–30 日间多次重写（见 `docs/PROJECT_MEMORY.md`）。每次上游同步都需要人工裁决 handler 与网关服务里的 controlled 钩子（`UPSTREAM_BASE.json` 的 manual_resolutions）。

## 4. “调了优先级和权重仍不可控”的根因

| # | 因素 | 所在模式 | 机制 | 对分配的影响 |
|---|---|---|---|---|
| 1 | 无账号权重 | 原版 Anthropic 等 | 同优先级按负载率与 LRU，`load_factor` 只缩放负载率 | 低并发近似均分，相对 50/30/20 目标的结构偏差 16.7 个百分点 |
| 2 | 优先级是软因子 | 原版 OpenAI 实验调度 | 优先级归一后与负载/错误/TTFT 相加，Top-K 加权随机 | 备用层空闲时也拿约 33%；优先级 1 与 5 效果相同 |
| 3 | 倍率覆盖优先级 | 原版 OpenAI 旧路径 | “低上游倍率优先”稳定排序在优先级之后执行 | 低倍率的低优先级账号先被选 |
| 4 | 粘性会话不随配置迁移 | 原版 | 绑定 1 小时、命中续期 | 改优先级/权重后已有会话不动；重度会话决定 token 份额 |
| 5 | 模型路由前置 | 原版 | Layer 1 优先于粘性与优先级 | 某些模型的流量完全不受分组优先级影响 |
| 6 | 陈旧 LRU | 原版 | 10 秒批量刷新 + outbox | 短请求、负载率相同时 10 秒内集中打到同一账号 |
| 7 | 闸门静默生效 | 两者 | 429/529/临时不可调度/阈值/窗口费用/RPM/会话数等 | 流量被转移但没有归因记录 |
| 8 | 进程内统计 | 原版 OpenAI 实验调度 | 错误率/TTFT 只在本进程 | 多实例结论不一致 |
| 9 | 成员集合分桶 | controlled | SWRR 计数器随候选集合切换 | 目标比例只在局部成立 |
| 10 | 容量溢出与健康爬坡 | controlled | 满载溢出下一层；熔断与 10/30/100% 恢复 | 低层账号意外获得流量，高层账号恢复期份额受限 |
| 11 | 无会话亲和 | controlled | 每请求独立 SWRR | 缓存失效、首字变慢、站点承担缓存重建成本 |
| 12 | 热路径依赖数据库 | controlled（模式读取对两者都生效） | 每请求多次 PG 往返 | PG 抖动放大为全局 503 或延迟 |

## 5. 外部调研

### 5.1 开源网关与路由器

| 项目 | 选择算法 | 亲和 / 缓存 | 失败处理 | 可控性评价 |
|---|---|---|---|---|
| [QuantumNous/new-api](https://github.com/QuantumNous/new-api)（本地读取源码） | 同优先级内按权重随机；重试时逐级降到下一优先级（`model/channel_cache.go:117-220`） | “渠道亲和”规则：按 `prompt_cache_key` / `metadata.user_id` 等提取键，TTL 默认 1 小时，`prefer`/`strict` 模式，统计各规则的缓存命中（`service/channel_affinity.go`） | 自动禁用渠道、跨分组重试；`RequestPolicyState` 记录每个请求的路由决策事件 | 高：优先级严格、权重直观、决策可读 |
| [LiteLLM Router](https://docs.litellm.ai/docs/routing) | 默认 `simple-shuffle`（按 weight/rpm/tpm 随机）；另有 least-busy、usage-based、latency-based（带 `lowest_latency_buffer`）、cost-based | `session_affinity` 预检：会话固定到首个部署，空闲 1 小时过期，冷却期间临时转移但保留绑定 | `order` 分层、`allowed_fails` + `cooldown_time` 冷却、按错误类型重试、fallbacks | 高（默认策略）；自适应策略可选 |
| [Portkey](https://portkey.ai/docs/product/ai-gateway/load-balancing) | `loadbalance` 按归一化权重；权重 0 停流 | `sticky`：按 `hash_fields` 绑定，TTL 默认 3600 秒，仅新会话按权重分配 | fallback 可按状态码触发，可与负载均衡嵌套 | 高 |
| [Apache APISIX ai-proxy-multi](https://apisix.apache.org/zh/docs/apisix/3.15/plugins/ai-proxy-multi/) | 实例 priority 分层，层内 roundrobin（加权）或 chash | chash 一致性哈希 | `fallback_strategy`: rate_limiting / http_429 / http_5xx；健康检查 | 高 |
| [Kong AI Proxy Advanced](https://developer.konghq.com/ai-gateway/load-balancing/) | round-robin、consistent-hashing、lowest-latency、lowest-usage、least-connections、semantic、priority 组 | consistent-hashing 按请求头 | 重试与回退 | 中高（多算法可选） |
| [Envoy AI Gateway](https://aigateway.envoyproxy.io/docs/api/) | backendRefs 权重 + priority | — | BackendTrafficPolicy 重试、令牌限流 | 高 |
| [Higress](https://higress.ai/en/blog/gpu-token-50percent-llm) | 全局最少连接、前缀匹配、GPU 感知（自建推理） | 前缀缓存感知（TTFT 240→120ms，厂商数据） | — | 面向自建推理，不直接适用订阅账号 |
| [Bifrost](https://docs.getbifrost.ai/enterprise/adaptive-load-balancing) | 开源版加权随机；企业版每 5 秒按错误/延迟/利用率重算权重 | — | 健康四态 | 自适应强，人工可控弱 |
| [AxonHub](https://github.com/looplj/axonhub/blob/master/docs/zh/guides/load-balance.md) | 先按关联优先级硬分层，层内多策略打分（会话 1000、错误 200、加权轮询 150、延迟 80、限流 100/-10000） | TraceAware 会话一致 | 错误惩罚随时间衰减 | 中（硬分层可控，层内评分不透明） |
| [OmniRoute](https://github.com/diegosouzapw/OmniRoute) | 20 种组合策略（priority、weighted、fill-first、p2c、reset-aware、headroom、cache-optimized、lkgp…） | 会话亲和钉住；曾修复“评分策略每请求重算赢家导致亲和失效” | 额度预检与自动回退 | 灵活但复杂 |
| [uni-api](https://github.com/yym68686/uni-api) | fixed_priority、round_robin、weighted_round_robin、lottery、random | — | 渠道冷却（默认 300 秒）、自动重试 | 高 |
| [CLIProxyAPI](https://mintlify.com/router-for-me/CLIProxyAPI/concepts/routing) | round-robin / fill-first | — | 额度超限切换凭据并冷却 | 高 |
| [OpenRouter 供应商路由](https://openrouter.ai/docs/features/provider-routing) | 默认按价格加权负载均衡，`sort` 价格/吞吐/延迟，`order` + `allow_fallbacks` | — | 30 秒内故障的供应商降权 | 面向用户的显式控制 |
| [Wei-Shaw/claude-relay-service](https://github.com/Wei-Shaw/claude-relay-service) | 粘性映射（默认 1 小时） → 优先级 → lastUsedAt | 粘性会话 | 限流标记 | 与 Sub2API 同源 |

### 5.2 Sub2API 二开项目

| 项目 | 调度相关改动 | 借鉴点 |
|---|---|---|
| [ranxi2001/sub2api](https://github.com/ranxi2001/sub2api)（743★，最活跃分支，本地读取源码） | 新增“优先级调度”：质量/延迟(P90 TTFT)/负载/利润四因子，experience/balanced/profit 预设，层级 eligible/insufficient/degraded，10% 探索份额，OAuth 额度余量、RPM 余量、2 分钟半衰期的错误率；粘性在“容量明显失衡”时一次性迁移；输出带原因的评分快照 | 决策快照与原因码值得借鉴；但本质仍是评分式智能调度，人工份额不可预测 |
| [PIXEL-API/PixelAPI](https://github.com/PIXEL-API/PixelAPI) | 账号广场、号主房间调度健康账号、分组多路由回落 | 多租户号池场景 |
| [WilliamWang1721/LightBridge](https://github.com/WilliamWang1721/LightBridge) | 每平台多账号的 priority / weight / load factor，健康感知 | 权重与负载因子分离 |
| [nianzs/sub2api](https://github.com/nianzs/sub2api) | “分组比例调节” | 分组级比例意图 |
| [TheDustJa/sub2api-overdraft](https://github.com/TheDustJa/sub2api-overdraft) | Codex 5h/7d 透支探测、调度与恢复，可一键回退上游调度 | 显式回退开关 |
| [wenyi401/ikik-api](https://github.com/wenyi401/ikik-api) | 公共/私有/自有/拼车池调度概念 | 池化维度 |

### 5.3 算法与上游缓存事实

- **加权会话一致性哈希（HRW，对数法）**：每个会话对每个账号算 `-ln(u)/w`，取最小者；被选概率严格等于 `w_i/Σw`，账号退出时只迁移其自身会话，并按其余权重比例分散。参见 [IETF draft-ietf-bess-weighted-hrw](https://www.ietf.org/archive/id/draft-ietf-bess-weighted-hrw-02.html)、[hrw-hash crate](https://docs.rs/hrw-hash)。
- **有界负载的一致性哈希**：Google 在 Pub/Sub 落地，Vimeo 在 HAProxy 中使用后缓存带宽降至约 1/8。参见 [Google Research](https://research.google/blog/consistent-hashing-with-bounded-loads/)、[arXiv:1608.01350](https://arxiv.org/abs/1608.01350)。
- **缓存感知与负载均衡的切换**：SGLang Router 在负载均衡时走前缀缓存亲和，失衡超过绝对/相对阈值时退回最短队列。参见 [SGLang Router](https://docs.sglang.ai/advanced_features/router.html)。
- **上游缓存不跨账号**：Anthropic 缓存按组织隔离，Claude API 还按 workspace 隔离；默认 5 分钟、命中续期；写入 1.25×（1 小时为 2×），读取 0.1×（Opus 5.5/Sonnet 5.5 为 0.05×）。参见 [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)。OpenAI 缓存不跨组织，路由依据前缀哈希与 `prompt_cache_key`，GPT-5.6+ 写入 1.25×、读取 0.1×。参见 [OpenAI Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)。
- 推论：对订阅/API 账号池，**跨账号切换会话 = 上游缓存冷启动**。Sub2API 的“强制缓存计费”让用户不为此付费，成本由站点承担。会话亲和既是体验问题，也是毛利问题。

### 5.4 归纳出的设计原则

1. 管理员意图（层级、份额、排空）与自动机制（闸门、冷却、容量）分层；自动机制只能**排除**账号或在管理员给定的界内**微调**，不能改写意图。
2. 份额在“新会话”粒度上兑现，已有会话保持亲和；需要迁移时由明确策略触发，并可预估迁移量。
3. 每一次偏离理想选择都要有原因码，并能在报表中汇总成“目标 vs 实际 vs 原因”。
4. 热路径不读数据库，配置与状态读取失败时退回已知配置或原版调度，而不是拒绝请求。
5. 不在转发链路与故障转移循环里再造一套语义；复用原版的闸门、冷却与切换上限，减少与上游同步的冲突面。

## 6. 仿真证据

脚本与完整输出：[`simulation/share_sim.py`](../openspec/changes/replace-controlled-with-share-scheduling/simulation/share_sim.py)、[`RESULTS.md`](../openspec/changes/replace-controlled-with-share-scheduling/simulation/RESULTS.md)。要点：

- 原版实验调度器默认权重下，优先级 [1,2] 的空闲账号首选比例为 66.7% / 33.3%；主账号 50% 负载时为 60% / 40%。
- HRW 在 5:3:2 权重下，20 万会话的份额为 50.09% / 29.97% / 19.94%；账号 B 退出只迁移 B 的 29.97% 会话，按 71.4% / 28.6% 分散到 A、C；C 的权重 2→4 时首选变化 13.27%，等于理论最小值 13.33%。
- 会话亲和下按小时窗口统计：请求份额偏差均值 1.5%（纠偏后 1.0%）；轻尾成本 2.3%（1.6%）；重尾成本 8.3%（6.1%），最坏 30%+。重尾成本需要硬性封顶，调度只能把偏差暴露出来。

## 7. 缓存命中与首字延迟专项（2026-10-10）

### 7.1 官方事实（2026-10-10 核对）

| 项 | Anthropic（Claude API / Bedrock / Vertex） | OpenAI | Gemini |
|---|---|---|---|
| 隔离范围 | 组织之间不共享；Claude API、Claude Platform on AWS、Microsoft Foundry 还按 workspace 隔离；Bedrock、Google Cloud 只按组织隔离 | 不跨组织，也不跨区域处理边界；同组织同区域内按前缀与 `prompt_cache_key` 路由 | 按项目（Vertex） |
| 寿命 | 默认 5 分钟，命中免费续期；可选 1 小时（写入 2×）。寿命从读写该缓存的**请求开始**计算 | GPT-5.6 及以后固定 30 分钟（最近一次写入或复用后）；更早模型 `in_memory` 通常空闲 5–10 分钟、最长 1 小时，`24h` 通常约 30 分钟、最长 24 小时，非 ZDR 组织默认 `24h` | 隐式缓存未给出寿命；显式缓存默认 1 小时 |
| 命中条件 | 前缀按 tools → system → messages 顺序完全一致；最多 4 个断点，每个断点向前回看 20 个块；顶层 `cache_control` 为自动缓存，断点随对话前移 | 前缀哈希（含工具定义）+ `prompt_cache_key`；单机超过约 15 次/分钟会溢出到其他机器；GPT-5.6+ 自动路由并支持显式断点 | 把公共内容放在开头，并在短时间内发送相同前缀的请求 |
| 最小长度 | 512–4096 Token，随模型不同 | 1024 Token | 1024–4096 Token，各版本文档不一致 |
| 价格 | 写入 1.25×（1 小时 2×），读取 0.1×（Opus 5.5/Sonnet 5.5 为 0.05×，Fable/Mythos 5.1 为 0.025×） | GPT-5.6+ 写入 1.25×，读取 0.1×（GPT-6.1 Sol 0.05×） | — |
| 延迟 | 长文档的首字明显改善；5 分钟与 1 小时缓存延迟相同；不影响输出速度。发布时的例子：10 万 Token 缓存后首字从 11.5 秒降到 2.4 秒（第三方转述） | 减少开始响应前处理输入的时间；GPT-5.6+ 预热可降低后续首字 | — |
| 并发 | 缓存条目要等第一个响应开始后才可用；并行请求要先等首个响应 | — | — |
| 使缓存失效的改动 | 工具定义改动使全部失效；thinking 配置、图片、`tool_choice` 等影响消息块 | — | — |

客户端：订阅版 Claude Code 默认请求 1 小时缓存，API Key 用户默认 5 分钟，可用 `ENABLE_PROMPT_CACHING_1H` 开启 1 小时缓存（v2.1.108 起）。这一项来自 GitHub issue 与发布说明，属于二手来源。

来源：
- [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- [OpenAI Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
- [Gemini Context caching](https://ai.google.dev/gemini-api/docs/caching)
- [SD Times 对 Anthropic 发布的报道](https://sdtimes.com/ai/anthropic-introduces-prompt-caching-to-reduce-latency-and-costs)
- [claude-code#32671](https://github.com/anthropics/claude-code/issues/32671)

### 7.2 缓存亲和与首字的外部实践

| 项目 / 研究 | 做法 | 对本方案的借鉴 |
|---|---|---|
| [OpenRouter 粘性路由](https://openrouter.ai/docs/guides/best-practices/prompt-caching) | 按账号、模型和对话粘到同一供应商。对话默认由“首条 system/developer 消息 + 首条非 system 消息”识别，`session_id` 优先。供应商不可用时回退；`provider.order` 优先于粘性；只在缓存读更便宜时启用 | 对话锚点（首条消息）；显式会话标识优先 |
| [LiteLLM](https://docs.litellm.ai/docs/tutorials/claude_code_prompt_cache_routing) | `prompt_caching` 预检：把会用到缓存的请求送回写入缓存的部署。`deployment_affinity`、`previous_response_id` 续接。其[基准博客](https://docs.litellm.ai/blog/auto-router-prompt-caching-benchmark)称，会话切回曾用模型时，该模型缓存仍热的比例 1 小时 99.3%、5 分钟 97.4% | 切回原账号时缓存大概率仍热，支持“短时不可用软溢出、恢复后回原账号” |
| new-api 渠道亲和（本地源码 `service/channel_affinity.go`、`setting/operation_setting/channel_affinity_setting.go`） | 按规则（模型/路径/UA）从 `prompt_cache_key`、`metadata.user_id` 取键，默认 TTL 1 小时。`session_mode`：`off`/`prefer`/`strict`，strict 失败不换渠道。`SwitchOnSuccess` 成功后改绑；每条规则统计缓存命中 | 按规则统计缓存命中，对应本方案的缓存归因；“成功即改绑”与溢出改绑一致 |
| [Portkey 粘性负载均衡](https://portkey.ai/docs/product/ai-gateway/load-balancing) | `hash_fields` + TTL，内存 + Redis 两级缓存，新会话按权重 | 与 HRW + 绑定一致 |
| [SGLang Router](https://docs.sglang.ai/advanced_features/router.html) | 近似前缀树亲和。负载差超过绝对阈值 32 且超过相对阈值时，改走最短队列。发布称缓存命中 3.8×、吞吐 1.9× | “满载才偏离亲和”的思路 |
| [KubeAI CHWBL](https://github.com/substratusai/kubeai)、[有界负载一致性哈希](https://research.google/blog/consistent-hashing-with-bounded-loads/) | 前缀哈希 + 负载上限（超过平均负载的 1+ε 倍就转下一个），作者基准称首字快 95% | 热点分散；本方案的上限改用账号自身并发 |
| [NVIDIA Dynamo KV Router](https://docs.nvidia.com/dynamo/latest/components/router/README.html) | 代价 = 重叠权重 × 需预填充块 + 活跃块；权重越高越偏向缓存（改善首字），越低越均衡 | 首字与均衡的显式权衡参数 |
| [llm-d](https://llm-d.ai/docs/architecture/Components/inf-scheduler) | 前缀缓存评分（近似/精确）+ 队列评分 + 会话亲和评分，可配权重 | — |
| [DualMap](https://arxiv.org/abs/2602.06502)（2026） | 两个哈希候选二选一；首字超过 SLO 时从亲和切到负载感知；热点重平衡；同首字 SLO 下容量提升至 2.25× | 等待预算受首字约束 |
| [CacheRoute](https://arxiv.org/abs/2608.19677)（2026） | 周期性规划的前缀亲和，命中率从 64.1% 提到 93.2%。也指出亲和收益不足时残余负载倾斜会抵消收益，建议启用前做影子回放 | 影子运行与灰度门禁 |
| [DLPM / D²LPM](https://arxiv.org/abs/2501.14312)（2025） | 带赤字计数的局部性公平调度，兼顾前缀局部性与客户端公平 | 份额（公平）与缓存局部性可以兼得 |
| [Prequal](https://www.usenix.org/conference/nsdi24/presentation/wydrowski)（NSDI 2024） | 用在途请求数与延迟探测选服务器（HCL 规则），显著降低尾延迟 | 本方案不引入延迟评分，以有界等待控制尾延迟 |

### 7.3 本仓库与上游代码中影响缓存和首字的事实

| 事实 | 位置 | 影响 |
|---|---|---|
| Anthropic 会话哈希回退：带断点内容取“第一条带断点的消息”，客户端移动断点时逐轮变化；只有 system 带断点时不含 API Key；全文摘要逐轮变化 | `gateway_service.go:902-966`、`:1060-1250` | 无会话标识客户端几乎没有跨轮粘性，或不同租户挤到同一绑定 |
| 粘性 TTL 1 小时，每次命中 `EXPIRE` 续期 | `gateway_service.go:36`、`gateway_scheduling.go:597`、`openai_sticky_compat.go:187-206`、`repository/gateway_cache.go:56-59` | 可用 `TTL − PTTL` 推算会话空闲时长，零额外往返 |
| 粘性账号满载时排队：最多 3 人、最长 120 秒；兜底排队 30 秒、100 人 | `config.go:2626-2629`、`gateway_scheduling.go:607-631` | 缓存友好，但尾延迟高 |
| 负载均衡层选中后覆盖粘性绑定 | `gateway_scheduling.go:764-775` | 满载即永久改绑，会话不回原账号 |
| 账号只要处于限流（剩余 > 0）就删除绑定 | `gateway_service.go:558-569` | 短时限流也导致永久迁移 |
| 粘性只在无模型路由时生效（路由层另有粘性） | `gateway_scheduling.go:534-535` | — |
| Gemini 摘要链会话回退存于进程内存（5 分钟）；`FindAnthropicSession` 未被调用 | `digest_session_store.go`、`gemini_v1beta_handler.go:440-500` | 多实例下不一致 |
| OpenAI 会话键：会话头 → `prompt_cache_key` → 内容锚点（模型 + 系统 + 工具 + 首条用户消息），跨轮稳定 | `openai_gateway_scheduling.go:146-185` | 份额调度直接复用 |
| `prompt_cache_key` 按账号命名空间改写 | `openai_gateway_forward.go:553-590` | 租户隔离，换账号时缓存键随之变化 |
| thinking 签名错误时剥离后重试 | `gateway_upstream_response.go:109-200`、`gateway_forward.go:428-565` | 跨平台/类型换号可能多一次往返，并改变前缀 |
| 连接池默认 `account_proxy` 隔离，空闲连接 90 秒回收，客户端 15 分钟回收 | `repository/http_upstream.go:44-75`、`config.go:2611` | 流量稀少的账号首请求要重新握手 |
| `usage_logs` 记录 `first_token_ms` 与缓存读写（5 分钟/1 小时分列）；`session_id` 只记显式会话头 | `usage_log.go:150-190` | 可做首字斜率回归与缓存占比基线；会话级归因需另行抽样 |
| `sticky.hash_source` 以 Info 级别记录会话键来源 | `gateway_service.go:906-960` | 可直接统计客户端画像 |
| `servertiming` 采集器记录 Redis/SQL/HTTP 依赖耗时 | `internal/pkg/servertiming/` | 可度量选择阶段往返 |

上游 Sub2API 的相关 issue：
- [#5241](https://github.com/Wei-Shaw/sub2api/issues/5241)：企业用户要求可控的粘性与缓存失效，而非“为命中率牺牲可用性”。
- [#7400](https://github.com/Wei-Shaw/sub2api/issues/7400)：Responses 转换把中途 system 消息前移，破坏多轮前缀。这是转换导致的未命中，不是调度导致。
- [#2517](https://github.com/Wei-Shaw/sub2api/issues/2517)、[#1967](https://github.com/Wei-Shaw/sub2api/issues/1967)：用户反馈缓存难命中。

### 7.4 量化结论

仿真脚本与完整输出见 [`simulation/cache_ttft_sim.py`](../openspec/changes/replace-controlled-with-share-scheduling/simulation/cache_ttft_sim.py) 与 [RESULTS 第二部分](../openspec/changes/replace-controlled-with-share-scheduling/simulation/RESULTS.md)。仿真为标准库实现，固定 5 个种子，覆盖事件与稳态、5 分钟与 1 小时缓存寿命。

- 原版缓存命中率 83.2–93.0%。
- 初版份额规则低 3.3–6.5 个百分点，主要是“溢出不改绑”导致的往返颠簸。
- 修订版与原版持平：差值均值 −0.1 至 +0.1 个百分点，逐种子 −0.3 至 +0.4 个百分点。TTFT P95 为原版的 0.67–0.93 倍，P99 为 0.70–0.91 倍。
- 修订版第 1 层请求份额 49.5/32.9/17.7（目标 50/30/20）；原版约 39/34/27。
- 逐请求分配低 11–20 个百分点，只适合无状态请求。
- 严格回升（30 分钟截止）缓存命中率约低 1 个百分点，因此默认关闭。

### 7.5 设计原则（补充第 5.4 节）

6. 亲和键必须跨轮稳定、不跨租户；协议续接与显式会话标识优先，其次是对话锚点。
7. 策略性迁移只在缓存冷窗口发生：空闲超过缓存寿命，或会话没有可缓存前缀。改权重、回升、排空、纠偏都走冷窗口；只有管理员显式截止与硬不可用例外。
8. 满载时等待与缓存价值成比例且有上限；等不到就溢出并改绑，不在两个账号之间来回。
9. 短时不可用、且恢复时缓存仍热，才保留绑定；否则立即改绑。
10. 用缓存归因区分“调度造成的未命中”与“客户端或改写造成的未命中”，原版与份额分组同口径对照；启用前先影子运行。
