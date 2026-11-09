# 0.5.12 第一期设计契约

依据预算设计 v3.3 与 PM 修订 12；本文件覆盖第一期。

## 截止与时钟

T0 = Started()，Te = 首次成功读到 EOF 的时刻。KindText、budget_start=body_eof 且 Te−T0≥1 s 时 anchor=Te，其余 anchor=T0。D=min(anchor+base,T0+290 s)。图片预算无截止；nil Budget 不产生截止。首次生成计时器或设置 AttemptProfile 后冻结锚点；重复接收只计数。

锚点只用于截止。spent=start−T0，首试 lastMark=T0，≥60 s 即账号级失败的定义不改动。AttemptTimeout 非末候选=min(Remaining,max(tier_eff,spent))，末候选=Remaining。上传、出首字后的阶段不新增强制取消；进行中的尝试仍由其已安装计时器约束。body_eof 下，CanAttempt、ConsumeInPlaceRetry、ClampWait 与等槽等待若能确认下一次尝试由现有计时器覆盖则沿用 D；无法判断或无计时器的准入与等待沿用 arrival 上限 T0+base（迟发判据为 T0+base−2 s）。arrival 模式保持不变。

性质 M 来自 D_new≥D_old 与截止消费者的单调性。Remaining 过期后下限为 1 ns，故“剩余恰增 δ”只在首次旧截止约束前成立。ER-3 是有限轨迹回归检查，不是任意输入的数学证明。

## 不可变计时器归因

每次创建或重算计时器时构造 TimeoutPlan，包含 seq、account/model、start、tier_eff、planned、effective、remaining、source、would、cut_after_full_tier、applied。remaining 是构造时的值；cut_after_full_tier := remaining_at_plan >= tier_eff（不受 would 门控），等号算已给满；派生观测字段 budget_cut_after_full_tier := would && cut_after_full_tier。would 只在 remaining < planned 的 remaining_cap / last_candidate_budget 路径成立。applied = would && !cut_after_full_tier && attribution=enforce。

没有档位（Anthropic 响应阶段、WS 或 tier_eff≤0）applied=false。Gemini 末候选不新增计时器。计时器触发后只读它持有的 plan，不用可变 profile 或 elapsed 重算 applied；planned/Reason、Warm/Stream、hold 样本模型/体积及 !planned 的 HandleStreamTimeout 仍按现有 profile 读取。

UpstreamFailoverError 挂 BudgetCutPlan，BudgetCutApplied(err) 为进程内唯一判定入口。既有 planned 豁免 HandleStreamTimeout、API-key 健康熔断、OpenAI SessionFailure；applied 仅追加豁免慢连击、latency-hold、近期健康展示和 share。applied 但非 planned 仍触发前三项；高级调度器失败上报、Gemini breaker、Anthropic/Gemini SessionFailure 不变。U11 钉住 applied FOT 仍上报失败。

近期健康在 DISTINCT ON 前过滤 applied，share 在 GROUP BY/LIMIT 前过滤；历史 JSON 缺字段按 false，gate v3 includeAll 输入不滤。kind/category 不改。末次 applied 的 budget_exhausted 请求标 error_owner=budget。

## 观测与配置

budget_start=arrival、budget_cut_attribution=observe 为发版缺省，新增两键在 NewBudget 时快照；档位仍按用时读取。off 不计算 cut，observe 只记录且 applied=false。FOT 事件附 plan 字段，全部 omitempty；事件保留字节预算按去掉新增键的旧口径计算，保留集合不变。Anthropic 响应阶段只写日志、指标和 resilience_timing.response_stage，不新增 upstream_errors。

OpenAI 派出只在真实 HTTP 传输前计一次；插件提前失败不计、WS/Anthropic/Gemini 不纳入此直方图。迟发定义 start>T0+base−2 s，不写死 238；无计时器迟发另计 late_attempt_no_timer_count，只进请求 JSON/日志/指标。未送达 Warn resilience.late_attempt_undelivered 供 G9 与同 key 同 UTC 小时的 abuse_guard 配对。

292 为 usage 可空观测列（含 budget_start_mode、budget_t0_unix_ms），293 为 ops resilience_timing；无默认值、索引、回填。等锁 5 s、语句 30 s，两个独立事务可能半迁移。G9 切回流程和核验 SQL 见 [runbook](runbook.md)。

## T8–T11 旧契约同步

T8：pre-output-keepalive 中的固定 240 s 改为相对 D。T9：预算场景改为相对 D，无计时器路径新尝试最晚 D−2 s。T10：旧 design/runbook 与现行文档使用 D，并说明 Gemini 末候选风险。T11：≥60 s 账号级定义不改动，从到达起算；E2/E4 移至第二期待探针和数据。

正常 arrival 准入不产生迟发，隐藏重放绕过准入时计数与 Warn 仍可非零，仅增加观测。Warn 包含 request_id/client_request_id；abuse_guard 仅证明进入 abuse 状态。G9 只作迟发观测与核验，不自动切回 arrival，也不是启用 body_eof 的硬前置门槛；观测字段与检查流程见 runbook。
