# 任务

> 起点：origin/main `f8fec15a2`（业务代码与 v0.3.0 相同）。
> 开发分支：`feat/gateway-resilience-20261015`。XY-image 的配套改动在其仓库单独建分支。
> 全部能力在一个 xy2api PR 和一个 XY-image PR 中交付（D23）；不做灰度（D24）。
> 每个“验证”项都要保留实际命令、退出码与产物路径，失败记录不删除。
> 推送、PR、合并、发版、DNS 修改、生产部署都需要用户单独授权。

## 0 准备与工具

- [x] 0.1 建开发分支，在 `docs/PROJECT_MEMORY.md` 的“进行中的工作”登记。
- [x] 0.2 基线与度量工具 `tools/gateway-resilience/`：
  - `baseline.sql`：只读统计 design §14 的全部指标；
  - `caddy_cuts.py`：按入口统计 CDN 切断，并把切断记录与应用日志、用量逐条匹配；
  - `session_streaks.py`：统计同一会话在同一账号上的连续失败。

  基于本次分析脚本整理，不含凭据。
  - 另有 `first_token_calibration.sql`；三个工具只输出聚合结果，已在临时 PostgreSQL 与合成日志上验证。
- [x] 0.3 在生产只读环境采集一次基线（沿用已授权的只读范围），结果写入证据目录。
  - 2026-10-15T16:15Z 采集 48 小时基线（仓库外 `production/` 目录）：无号 503 共 9710 次（13 个分组），最长整组无号连续 85 分钟；余额原因停用事件 47 次；“余额不足”展示 2431 次（其中 1994 次的用户当前余额为正，近似口径）；同 Key 同账号连续失败 P99 为 11、最大 322；流式首字 P50/P95：OpenAI 5.1/33.1 秒、Anthropic 3.8/9.7 秒。Caddy 切断率需在部署窗口用 `caddy_cuts.py` 采集（Caddy 日志只保留约 2 小时）。
- [x] 0.4 首输出阈值校准：只读导出生产 7 天“模型 × effort”的首字分布（P50/P95/P99/P99.9），确认 60/120/180 秒的分档与按模型覆盖值。
  - 结论：生产 0.3.0 未启用首输出超时；原分档会误切约 1.3%（OpenAI 默认档 60 秒）与 0.4%（xhigh 120 秒、max 180 秒）的成功请求。缺省改为 default 180 / high 180 / xhigh 200 / max 220 秒，各档超出比例约 0.10%–0.26%，仍给第二次尝试留出 20–60 秒；暂不设按模型覆盖。240 秒首输出前总时长会截断约 0.04%–0.16% 的成功请求（max 档最高），维持设计取值。
- [x] 0.5 故障注入上游 `tools/gateway-resilience/fault-upstream/`（Go，无外部依赖）。以 OpenAI Responses、Chat、Anthropic Messages、Gemini、`/v1/usage`、new-api 计费接口的形状，按请求参数或头选择场景：
  - 慢响应头（可设秒数）；
  - 先 200 SSE，再在 N 秒后发出 `response.failed`（忙、限流、余额、上下文超长）；
  - 已输出后停滞；
  - 502/503/504/524（含 Cloudflare 错误页与 `retry_after`）；
  - 402/403/429 余额（含 “Insufficient account balance” 等多种文案）；
  - 401 失效；
  - 429 带或不带重置时间；
  - 529 与 `server_is_overloaded`；
  - 正常长推理（可设首字时间）；
  - 图片长耗时。
  - 实现：场景 F00–F22、`BAL`（按状态键切换余额）、`MIX`（按比例随机注入）；每个请求记录结局（完成、取消、断开、流内失败、错误状态），用于证明断开即取消上游。
- [x] 0.6 CDN 模拟代理：可配置“首字节前读超时”和“空闲读超时”（90、125、300 秒），并能注入 `X-XY-Edge` 标记。
  - 实现：首字节超时返回 524 并取消回源，空闲超时断开客户端并取消回源；`EDGE=none` 作直连计数代理；`/_cuts`、`/_requests` 记录。
- [x] 0.7 真实客户端端到端测试脚本：驱动 Codex CLI、Claude Code、OpenAI/Anthropic SDK、OpenCode、go-genai/python-genai、curl，统计每个回合的请求数、最终错误文案与耗时。
  - 实现：`tools/gateway-resilience/staging/clients.py`，15 个用例（隔离 HOME），按代理计数统计每回合请求数、退出码、输出尾部与耗时。

## 1 上游失败分类（spec: upstream-failure-classification）

- [x] 1.1 新增 `internal/resilience/classify.go`，按 design §2 实现：先看文本与 code，再看状态码；解析 `retry_after`（头、JSON 正文、Cloudflare 错误页）。
- [x] 1.2 在以下路径接入分类，结果写入请求上下文与 ops 记录：
  - `ratelimit_service.go` 的 `HandleUpstreamErrorRaw`；
  - OpenAI 上游错误与流内 failed 事件；
  - Anthropic 网关；
  - Gemini 兼容服务；
  - 透传路径。
  - 进度（2026-10-15）：共用入口 `HandleUpstreamErrorRaw` 与 `FailoverState.HandleFailoverError` 已接入；分类结果写入 ops 记录待任务 9。
  - 完成（2026-10-16）：所有路径的上游失败事件都经 `appendOpsUpstreamError` 汇总，在那里按状态码与正文（流内失败按 `response.error.code`）补写 `resilience_category`；总开关关闭时不写。
- [x] 1.3 关键字集合可配置（`failure.balance_keywords_extra`）；未分类的上游 4xx 正文做脱敏、限长的采样日志。
  - 进度（2026-10-15）：`balance_keywords_extra` 已生效；未分类 4xx 采样日志待做。
  - 完成（2026-10-16）：只按状态码归类的 4xx（401、402 除外）以 `resilience.unclassified` 采样（每进程每分钟约 10 条，文案脱敏并截断到 300 字符）。
- [x] 1.4 确保本站计费错误不进入分类。
  - 本站的用户余额与在途预留错误在选号前后由 `CheckBillingEligibility` 直接写回，不经过上游失败处理；多级中转时，上游 xy2api 的这类答复按含义分类（预留 429 为限流、用户余额 403 为余额、终止应答为请求级降载），见 `TestChainedGatewayErrorShapes`。
- [x] 1.5 验证：表驱动单测覆盖 spec 中的所有场景，以及生产采样到的文案（脱敏后）；`go test -race ./internal/resilience/...`。
  - 2026-10-16：补测生产与常见中转文案时发现 new-api 的“该令牌额度已用尽”“quota is not enough”“预扣费额度失败”被判为 AUTH（403）或限流（429），已加入内置余额关键字（`TestClassifyRelayWordings`）；`-race` 通过。

## 2 账号罚则与余额恢复（spec: account-penalties-and-balance-recovery）

- [x] 2.1 `PenaltyOf`：区分硬、软，返回 `recoverAt` 与原因；`temp_unschedulable` 的 `reason` 前缀约定。
- [x] 2.2 瞬时失败的软冷却（30 秒；有上游 `retry_after` 时取 min(retry_after, 120 秒)）。
- [x] 2.3 余额状态机：
  - 存储 `accounts.extra.resilience_balance`；
  - 实现状态迁移与复检时间表（1/2/5/10/15 分钟，±15% 抖动）；
  - 把以下现有分支改走可恢复路径：Anthropic credit、402、非 OpenAI 403、余额类 429、图片 402。
- [x] 2.4 `BalanceProbeService`：
  - 泛化 `CNProviderBalanceCheckService`；
  - 支持 sub2api 系 `/v1/usage`、new-api 计费接口、1 Token 生成探测（日预算 48）；
  - Redis 单飞锁；
  - 探测返回 401 时转凭据路径。
- [x] 2.5 `RECOVERING` 半开：临时并发上限 1，满 3 次成功或 120 秒后回到 `OK`。
- [x] 2.6 管理接口 `POST /api/v1/admin/accounts/:id/balance/recheck`；账号列表显示余额状态与下次复检时间；进入、恢复以及超过 72 小时未恢复时，通过现有告警与邮件通知管理员。
  - 2026-10-16：复检接口与账号徽标随任务 9 完成；通知走现有告警：新增指标 `account_balance_exhausted_count`、`account_balance_unrecovered_count`（时长取 `balance.notify_after_hours`），迁移 276 预置同名规则并开启邮件；进入时触发、恢复时解除。
  - 进度（2026-10-15）：recheck 接口与 72 小时未恢复判定已完成；账号列表展示与通知接入待任务 9。
- [x] 2.7 非 OpenAI 平台的 403 统一为“豁免、计数、冷却”。
- [x] 2.8 验证：单测加真实 PostgreSQL/Redis 集成（状态迁移、单飞、半开、多实例）；`-race`；用故障注入上游演练“没钱 → 充值 → 自动恢复”。
  - `resilience_integration_test.go`（真实 PostgreSQL 18、Redis 8.4，测试服务器上以 testcontainers 运行）：会话失败与回避、16 路并发争探测锁只一个成功、RECOVERING 窗口、分组稳定、余额状态统计；韧性相关包 `-race` 通过；BAL-1、BAL-2 与浸泡中每小时断余额再恢复。

## 3 会话失败处理与末位兜底（spec: session-failure-handling）

- [x] 3.1 会话失败计数与回避集（Redis，TTL 600 秒），成功时清零。
- [x] 3.2 原版调度钩子（`openai_account_scheduler.go` 的 `selectBySessionHash`、`gateway_scheduling.go` 的粘性路径、`gateway_service.go` 的 `shouldClearStickySession`）：软罚时保留绑定、本次请求跳过；硬状态才清除；连续失败达到阈值时按 owner 删除并加入回避。
- [x] 3.3 份额调度适配（`share_scheduling_exec.go` 与候选构造）：软罚表示为 `RecoverAt`；回避表示为 `Excluded`；硬状态不带 `RecoverAt`；新增原因 `resil_failure_streak`、`last_resort_soft`、`fallback_return`。
  - 进度（2026-10-15）：未开始：份额调度显式 `RecoverAt`/新原因码。
  - 完成（2026-10-16）：余额耗尽改为 `balance_exhausted` 且不带 `RecoverAt`（修正份额调度下余额耗尽仍软溢出保留绑定，违反“余额耗尽立即改绑”）；会话回避的排除与改绑记 `resil_failure_streak`；末位兜底记 `last_resort_soft`。其他临时不可调度原因保持原处理（Q8）。`fallback_return` 不是份额放置决策，由韧性指标记录（偏差记入 design §4）。
- [x] 3.4 末位兜底（两种调度器都要实现）：先转投兜底组（任务 8），否则在最高的有软罚账号的那一层，取罚期最短者；不受单次首输出超时约束。
  - 进度（2026-10-15）：兜底组转投随任务 8 实现。
  - 调整（2026-10-16，`07df4f016`）：按优先级、再按罚期结束排序后依次尝试并发槽位，全部占满才在第一个账号上等待（design §4.3）。
- [x] 3.5 Explain：份额调度的 Explain 显示罚则、计数、回避与兜底；原版调度新增只读 Explain。
- [x] 3.6 在 `tools/upstream-sync/scheduler-diff.json` 中登记新增块，`sync.py scheduler-diff` 通过。
- [x] 3.7 验证：
  - `TestResilienceSchedulingMatrix` 24 例覆盖矩阵（经中间件转组判定与组内选号两步）；份额调度与 Gateway reliability 测试随全量 unit 通过；`BenchmarkShareSchedulingSelection500` 与 0.3.0 持平（份额约 476→481 µs，原版约 309→311 µs）。缓存命中与首字在生产按 14.2 复核（测试环境流量为合成，不可比）。
  - 矩阵测试：原版或份额调度 × 单号或多号组 × 软罚、硬罚、连续失败 × 有无兜底组；
  - 现有份额调度与 Gateway reliability 测试全部通过；
  - 用现有份额调度仿真与压测工具回归，确认缓存命中与首字不劣化（design §14 最后一行）。

## 4 错误语义与防风暴（spec: error-semantics-and-storm-protection）

- [x] 4.1 选号层返回“组内最早解冻时间”；无号 503 与全部限流 429 加 `Retry-After`（2–30 秒），正文加 `Please try again in Ns.`。
- [x] 4.2 配置类 503 加 `Retry-After: 60` 与 `x-should-retry: false`，状态码与文案不变（D14）。
- [x] 4.3 无号负缓存（2 秒），账号恢复事件触发失效。
- [x] 4.4 同 Key 快速失败（30 秒内 ≥10 次相同 503 时原样返回），按 spec 豁免。
- [x] 4.5 上游余额对用户隐藏，包括图片余额 402。
- [x] 4.6 用户余额：两条路径统一为 403 `INSUFFICIENT_BALANCE` 并换用新文案；在途预留超额改为 429 `concurrent_balance_reserved` 加 `Retry-After: 5`；估算改为缓存感知并以 P99 输出为上限。验收：用生产 7 天数据只读回放，余额为正的用户被拒次数减少 ≥90%，余额为负的金额不增加。（实现完成；生产只读回放验收在 12 执行）
- [x] 4.7 `image_task_handler.go`：准入检查放到内容审计之前；审计失败时释放准入。
- [x] 4.9 失败回合封顶（design §9.7，测试服务器发现）：会话连续第 5 次耗尽失败改为终止应答（无 `Retry-After`、`x-should-retry: false`、`server_is_overloaded`、5xx 改 503）；真正成功或间隔超过 120 秒清零。原因：Codex 对带 `Retry-After` 的 5xx 一个回合重试 30 次、历时 871 秒；流内失败重试 6 次。
- [x] 4.10 续链被拒时整轮重放（design §9.8，测试服务器用真实中转发现）：附带 `previous_response_id` 的 Messages 转 Responses 请求收到笼统 4xx 时去掉续链完整重放一次，并对该会话关闭续链。
- [x] 4.8 验证：单测、handler 集成测试；真实客户端测试中 Codex 一个失败回合的请求数 ≤5。
  - Codex 全失败回合 5 次请求、92.5 秒（修复前 30 次、871 秒）；流内失败 5 次。

## 5 请求预算与重试（spec: request-budget-and-retry）

- [x] 5.1 `resilience.Budget`：上下文挂载，包含最多尝试数 4、首输出前截止 240 秒（文本）、每账号同号重试计数，以及 `Canceled()`。
  - 预算由中间件按路径挂到请求状态（图片、视频无首输出截止；WebSocket 不挂连接级预算）。尝试进行中到达截止时取消，随任务 6 的首输出超时一起实现。
  - 调整（2026-10-16，`181e38602`）：删除无调用的 `Canceled()` 与上下文访问器；客户端取消由 `CanAttempt` 判断（耗尽原因 `canceled`）。
- [x] 5.2 接入 OpenAI 的 Responses、Chat、Messages、Images 循环；移除首输出超时只换 1 次的限制；有效换号上限取预算与 `max_account_switches` 的较小值。
  - 偏差：WebSocket 建连首回合的换号仍按 `max_account_switches`（连接级预算会误伤长连接）；Grok 媒体、alpha search、embeddings 循环同样接入预算。
- [x] 5.3 接入 Anthropic 网关循环（Messages、Responses、Chat 兼容）。
- [x] 5.4 Gemini 原地重试从 5 次降为 1 次，等待改为可取消。
  - 偏差：Antigravity 平台的智能重试（模型容量 1 秒间隔）只受首输出前截止与 `Retry-After` 上限约束，不计尝试次数；其通用重试计入预算。服务内 Anthropic 转发、透传、Bedrock 的原地重试在预算下只用于 529。
- [x] 5.5 同号重试只用于容量降载与池模式配置码，每账号最多 1 次；容量降载只换到不同主机；账号级错误直接换号。
- [x] 5.6 退避加 ±20% 抖动；上游 `Retry-After` 封顶 60 秒且不超出剩余预算。
- [x] 5.7 验证：spec 全部场景；故障注入上游组合场景；`-race`。
  - 见 `evidence/coverage-matrix.md`；BUD-1、SW-1、FO-1、RL-1、ANT-2 等场景；resilience、middleware、handler、service 相关 `-race` 通过（修复一处测试数据竞争）。

## 6 首包前保活、首输出超时、停滞与提交后的失败（spec: pre-output-keepalive）

  - 进度（2026-10-15）：spec 场景单测与 `-race` 通过；故障注入组合场景待任务 0.5 与 12。
- [x] 6.1 应用侧：仅当对端是受信代理时解析 `X-XY-Edge`；按 `keepalive.edges` 判断是否经 CDN。
- [x] 6.2 泛化 compact 心跳原语为 `PreOutputKeepalive`：
  - 首拍延迟 15 秒、之后间隔 10 秒；
  - 提交时带 SSE 头与 `X-Client-Request-ID`；
  - 心跳字节计入扣除口径；
  - 写侧加锁，与正常写入互斥；
  - 换号时不重置计时。
- [x] 6.3 各协议的心跳帧；Gemini 与官方 OAuth 账号不提前提交；图片与异步端点不提前提交。
  - 实现说明：复用 compact 心跳的上下文键，现有“已提交→流内终止帧”和“扣除心跳字节”的判定全部生效；心跳提交后若仍有代码写 JSON 错误，写入器把它改写为对应协议的终止帧。服务内三处“未输出才可换号”的判定改为语义输出口径。前提：`SERVER_TRUSTED_PROXIES` 必须包含 Caddy 到应用的对端地址（写入 runbook）。
- [x] 6.8 中转站伪保活帧（design §7.8，测试服务器发现）：读取上游 SSE 时丢弃 `SSE-Keep-Alive` 空 delta 帧，不转发、不算语义输出。原因：真实中转站在 `response.created` 前发送该帧，openai-python 报错，且网关会提前放弃换号与首输出超时。
- [x] 6.4 首输出超时：OpenAI 按 effort 分档（校准后 180/180/200/220 秒，见 0.4）并支持按模型覆盖；Anthropic 与 Gemini 首事件超时 90 秒；最后一个候选不设单次超时。
  - 实现：OpenAI 复用现有首输出守卫（原生 HTTP Responses），预算开启时按分档并受剩余预算约束；Anthropic 流式转发新增首事件守卫（头部等待与首行两阶段，守卫取消不被误判为客户端断开）。偏差：Gemini compat 服务的首事件守卫未接入，由停滞检测兜底；WebSocket 维持原配置。
- [x] 6.5 修复 G2：按 `min(T/4, 5 秒)` 检查；文本已输出后停滞 120 秒判定为断流；图片维持 900 秒。
  - 实现：检测周期 min(T/4, 5 秒)；语义输出后取 min(配置间隔, 120 秒)，输出前沿用配置间隔；Responses 出现 image_generation_call 后豁免文本阈值；停滞时对账号施加 STALL 软冷却。
- [x] 6.6 提交后的终止帧：
  - Responses：`response.failed`，带 code 与 “try again in Ns”；
  - Chat：`data:{error}` 加 `[DONE]`；
  - Anthropic：`event: error`（修复 G3）；
  - 写完立即关流。
  - 实现：已提交后的失败在消息中带 “Please try again in Ns.”（N 为组内最早恢复，2–30 秒）；Responses 码按设计映射；Chat 为 data+[DONE]；服务内的 stream_timeout 帧保持原样。
- [x] 6.7 验证：
  - 最终候选 `4e8180540`（与 `fe6d43c50` 只差进程内清扫）：KA-1（125 秒）、KA-3（90 秒）、KA-8（300 秒）均无切断；快速失败保持原状态码（ERR-4 与单测）；心跳后换号（FO-1）；预算耗尽给可重试终止帧（KA-4、KA-8、C-codex-busy-stream）；anthropic-python 把已提交流里的 `event: error` 抛为 `APIStatusError`（C-anthropic-py-committed-error）；Gemini 不注入心跳（GEM-1），官方 OAuth 例外由单测覆盖；经真实 Cloudflare 心跳与 SSE 不被缓冲（CF-1、CF-4）。
  - 用 CDN 模拟代理验证 90、125、300 秒三档都不再切断；
  - 快速失败仍返回原状态码；
  - 心跳后仍可换号；
  - Codex 300 秒空闲约束下，预算耗尽时客户端收到可重试终止帧；
  - Anthropic SDK 能把 `event: error` 抛为 APIError；
  - Gemini 与 OAuth 例外生效；
  - 心跳与 SSE 都不被 Caddy 缓冲（`encode` 不匹配 `text/event-stream`）。

## 7 客户端断开与计费（spec: client-disconnect-and-billing）

- [x] 7.1 送达状态跟踪（未送达、部分送达、完成），所有协议与非流式路径都要覆盖。
  - 实现：网关中间件在最内层包一层计数 writer，已送达语义字节 = 总写出 − 首包前/compact 心跳 − 流内 keepalive − 图片填充；断开在客户端连接 context 结束时判定一次（处理器自建的超时 context 不算断开），请求正常结束先标记 finished。
- [x] 7.2 未送达时立即取消上游，覆盖选号、等槽位、等响应头、换号间隙、首输出前；释放预留与并发槽位。
  - 实现：两个上游分离 context 函数统一接入；选号与等槽位本就随客户端 context 取消。偏差：阶段只区分 select（含等槽位）、header_wait、pre_output、streaming。
- [x] 7.3 迁移 274，新建 `gateway_undelivered_requests` 表及其写入、按保留期清理；管理接口 `GET /api/v1/admin/resilience/undelivered`。
  - 实现：迁移 272–275 一次写入并更新 checksums（临时 PostgreSQL 全量 322 个迁移顺序执行与重复执行通过）；未送达表用原生 SQL 仓储（未建 Ent schema），保留期固定 30 天、每 6 小时清理。
- [x] 7.4 部分送达后断开：最多再读 180 秒，到时取消，按已得用量计费。
- [x] 7.5 滥用保护：每 Key 每小时超过 30 次后按上游成本计费，`billing_mode='undelivered_abuse'`。
  - 实现：每 Key 每小时计数在断开判定时递增一次；超过阈值的未送达请求不取消上游（按部分送达的读取上限），拿到用量后按上游成本（账号统计成本或 total_cost × 账号倍率）计费并标 `undelivered_abuse`。上游未报用量时不计费。
- [x] 7.6 迁移 275：`ops_error_logs` 的断开阶段与已送达字节数；499 补全耗时。
- [x] 7.7 验证：
  - 未送达无用量行（DC-1、浸泡检查、集成测试）；XY-image 核对器（实验室与在线测试，未扣费）；计费与预留一致（单测、浸泡的重复行与孤儿预留检查）；真实 PostgreSQL 集成（`TestUndeliveredRepositoryRoundTripAndRetention`）。
  - 未送达请求在 `usage_logs` 中没有行；
  - XY-image 的核对器按“查不到即未扣费”收敛；
  - 计费与预留的账目一致性测试；
  - 真实 PostgreSQL 集成测试。

## 8 无号兜底分组（spec: availability-fallback-group）

  - 进度（2026-10-15）：真实连接端到端测试（未送达立即取消、部分送达限时读取）、计费闸门与滥用计费单测、仓储 SQL 在临时 PostgreSQL 上验证通过；XY-image 对账与生产口径回放待任务 11、12。
- [x] 8.1 迁移 272、273，以及 Ent schema 与重新生成的代码。
  - 实现：Ent 字段追加在 Fields 末尾（runtime 下标不变，减少同步冲突）；usage_logs 四条写入路径追加 `origin_group_id` 并在临时 PostgreSQL 上验证。
- [x] 8.2 分组管理接口与前端：选择兜底组，校验环与平台。
  - 实现：后端保存校验（自引用、环、平台一致、两侧均为余额计费）；分组创建与编辑表单提供同平台、启用、非订阅分组的选择器，清空时提交 0。
- [x] 8.3 选号集成：只在无号时触发，每请求一次，兜底组的资格约束全部生效。
  - 实现：鉴权后、模型白名单前的中间件判定“主组无任何无罚账号”时把本次请求的 API Key 换成兜底组，因此白名单、系统提示词、组合路由、选号、限额与计费都按兜底组；兜底组自身也须有无罚账号，否则留在主组（由末位兜底处理）。换号耗尽不跨组（判定只在请求入口）。
- [x] 8.4 计费：按兜底组倍率；`origin_group_id`；用户用量列表与可用分组页显示兜底关系。
  - 实现：按兜底组倍率计费，`usage_logs.group_id` 为兜底组、`origin_group_id` 为原组；用量读取随关联批量加载原分组名，管理端与用户用量列表显示“兜底：原分组 → 实际分组”；可用渠道页的分组旁显示兜底分组与倍率（优先用户专属倍率）。
- [x] 8.5 主组稳定 120 秒后切回（Redis 时间戳）；兜底组内绑定的 TTL 为 600 秒。
  - 偏差：“已在兜底组”的记忆按 API Key（Redis，TTL `binding_ttl_seconds`）而非会话；兜底组内的粘性绑定沿用原有 TTL。
- [x] 8.6 验证：spec 场景；计费与用量正确；切回不抖动；与仅限 Claude Code 的 `fallback_group_id` 互不影响。
  - FB-1、FB-2（`origin_group_id`、按兜底组倍率）；`TestAvailabilityFallbackReturnAndFlapping`；`TestAvailabilityFallbackIndependentOfClaudeCodeFallback`。

## 9 设置、管理界面、可观测与文档

- [x] 9.1 `gateway_resilience_settings`：服务层、`GET/PUT /api/v1/admin/settings/gateway-resilience`、校验规则（design §10）、读取失败时用最后已知值。
  - 实现：随任务 1 完成；每 10 秒刷新，读取失败或文档非法时保留最后有效值。
- [x] 9.2 前端（中英文）：
  - “网关韧性”设置卡；
  - 运维页的韧性面板；
  - 账号余额状态与“已充值”按钮；
  - 未送达记录列表；
  - 分组兜底字段；
  - 用量中的兜底标记。
  - 实现：系统设置“网关”页的“网关韧性”卡（总开关、各能力开关、常用数值、完整 JSON，范围与服务端一致）；运维页“网关韧性”面板（计数汇总与明细、余额状态分布、未送达列表）；账号状态列的余额徽标与“已充值”复检；分组表单兜底选择器；用量表兜底标记；可用渠道页兜底分组标签。
- [x] 9.3 结构化日志（design §12）与统计接口 `GET /api/v1/admin/resilience/stats`。
  - 实现：进程内按分钟分桶的计数（保留 1 小时，每桶最多 4096 个键，超出折叠为 overflow），统计接口按窗口汇总并附余额状态分布。
  - 偏差：计数为单实例口径；`keepalive_committed`、`undelivered`、`transient_*` 量大，保持 info（不进 `ops_system_logs`），其余管理员关注的事件为 warn；`resilience.classified` 由 `transient_cooldown` 与 `balance_transition` 分别承担。
- [x] 9.4 降噪：`upstream_billing_rate_sync_applied` 降为 debug；余额批量探测不写审计；`ops_system_logs.message` 只保留模板。
- [x] 9.5 文档 `docs/GATEWAY_RESILIENCE.md`（管理员说明与配置），并加入 `.gitignore` 的 docs 白名单；更新本记忆。
  - 另：runbook 增加 `SERVER_TRUSTED_PROXIES` 前提与基于 `keepalive_committed` 的 `edge` 校验；方案文件中的主机路径与端口改为代称（真实值在仓库外私有清单）。
- [x] 9.6 验证：前端 lint、typecheck、全量 vitest、构建；浏览器检查 1440/390 宽度、明暗主题、键盘操作；后端 handler 与 repository 测试。
  - 前端 lint、vue-tsc、全量 vitest（389 文件 / 3045 用例）、构建通过；浏览器 1440/390 × 明暗共 28 次页面加载无控制台错误、无横向溢出（含运维页 Explain）；键盘可完整操作运维页 Explain，Tab 到达的元素焦点可见；handler、repository（含 PostgreSQL/Redis 集成）测试通过。
  - 进度（2026-10-15）：前端 lint、vue-tsc、全量 vitest（387 文件 / 3041 用例）与构建通过；后端 gofmt、四种构建标签的 vet、全量 unit 测试通过。浏览器检查在测试服务器上随任务 12 进行。

## 10 上游同步登记

- [x] 10.1 `policy.json` 与 `UPSTREAM_BASE.json`：登记 7 个具名补丁（design §13），每个带 required_literals 与绑定测试。
  - 实现：`xy-resilience-{classify,budget,session,keepalive,disconnect,errors,fallback}-hook` 共 22 条字面量；方案目录登记为 XY_OWNED。偏差：`openai_gateway_upstream_errors.go`、`share_scheduling_exec.go` 实际无钩子，未登记；兜底钩子在路由中间件而非 `gateway_scheduling.go`。
- [x] 10.2 `scheduler-diff.json` 白名单更新；`sync.py audit` 在干净工作树上通过。
  - 实现：四个调度文件各加 `gateway-resilience` 块；原 `brand-identifier`、`require-compact` 块中被韧性改写的两行移入新块。`sync.py audit`、`scheduler-diff` 与 `tools/tests` 均通过。
- [x] 10.3 `VERSION` 升为 0.4.0；`SUB2API_COMPAT_VERSION` 不变。
  - 按仓库 RC 惯例先设为 `0.4.0-rc.1`（`VERSION` 与 `UPSTREAM_BASE.json` 一致），测试服务器验证后在发版时提升为 0.4.0。

## 11 XY-image 0.0.17（见 xy-image-appendix.md）

- [ ] 11.1 按附录第 2 节完成 X1–X8，各带单测。
- [ ] 11.2 与 xy2api 0.3.0（生产现状）和 0.4.0 候选双向兼容；XY-image 实验室 `deploy/xy2api-lab` 全量通过。

## 12 测试服务器集成验证（见 staging-validation.md）

- [x] 12.1 部署隔离环境：Compose 项目 `xy2api-staging`，包括候选镜像、PostgreSQL 18、Redis 8、故障注入上游、CDN 模拟、测试用 Caddy 站点；不触碰已有服务。
  - 测试服务器独立 Compose 项目，未触碰已有服务。
- [x] 12.2 测试数据：
  - 夹具分组、用户与 Key、3 个真实中转 Key（只存测试服务器 0600 文件）；真实流量费用很低，中转余额下降主要来自测试环境之外。
  - 夹具分组（单号、双号、多号；原版与份额调度；有无兜底组）；
  - 用户与 Key；
  - 用户提供的真实中转测试 Key（低价模型，总预算不超过 20 美元）。
- [x] 12.3 故障注入矩阵：逐条覆盖 8 个 spec 的全部 Scenario，留存证据。
  - 最终候选 `4e8180540` 场景 37/37（含新增 KA-8、CONT-1）。
  - 生产报错修复后的最终候选 `eaa63fd30`：场景 42/42（新增 PE-1/2/3/4/7 复现生产 R1/R2/R3/R4/R7；第一轮 41/42 的 PE-3 为前一次单独运行让应用已记住该账号，用例已兼容并重启应用后整轮复跑）。
- [x] 12.4 真实客户端端到端矩阵（design §15），统计每回合请求数、错误文案与耗时。
  - 最终候选 `4e8180540` 16/16；Codex 全失败回合 5 次请求、92.5 秒。
  - 最终候选 `eaa63fd30` 16/16。
- [x] 12.5 真实 CDN：经用户授权，在 Cloudflare 添加橙云测试子域，验证切断消失与心跳格式；ESA 视条件而定。
  - CF-1..5 通过；ESA 无测试站点，以 cdn-sim 90 秒档（KA-3）验证。
- [x] 12.6 用真实 new-api 系中转实测余额接口；不支持的类型确认会降级到生成探测。
  - 三个中转为 sub2api 系（`/v1/usage`），强制复检 EXHAUSTED → RECOVERING；new-api 形态由 F20 与单测覆盖。
- [x] 12.7 升级与回退演练：
  - 最终候选（迁移至 276）：升级启动 1.2 秒、usage_logs 最长阻塞 0.11 秒；回退 0.3.0、前滚、全库备份恢复行数一致。`eaa63fd30` 相对演练候选没有迁移与 ent schema 改动，结论沿用。
  - 生产只导出表结构（只读），加同量级合成数据（usage_logs 约 330 万、ops_system_logs 约 580 万、ops_error_logs 约 37 万、api_keys 约 2.5 万等）；
  - 完成 0.3.0 → 0.4.0-rc 升级，记录迁移耗时与锁；
  - 回退到 0.3.0 镜像后能正常运行；
  - 再前滚回 0.4.0-rc。
- [x] 12.8 XY-image 0.0.17 测试实例接入测试 xy2api，覆盖生图（同步与异步）、对话、余额核对。
  - 实验室录制 59 个场景，与 0.3.0 的差异仅为新增 `Retry-After` 与 `availability_fallback_group_id`；0.0.17 与 0.0.16 回放均 181/181；在线对话 4/4。测试环境未启用对象存储，异步生图由单测与 handler 测试覆盖。
  - `eaa63fd30`：重新录制 59 个场景，与前一候选无形态、状态、头或计费差异；0.0.17 与 0.0.16 回放均 181/181；在线对话 4/4。
- [ ] 12.9 24 小时浸泡（混合负载与故障注入）：协程、并发槽位、预留、Redis 键、内存无泄漏；日志里没有凭据。
  - 内存诊断（带 pprof 的诊断镜像，80 分钟负载）：堆与 goroutine 平稳、匿名内存预热后持平，无泄漏。
  - `eaa63fd30` 浸泡于 2026-10-16T10:26Z 启动（证据目录 `soak-968df0942`），约 2026-10-17T10:30Z 结束并自动判定。
- [ ] 12.10 按 design §14 核验指标；生产回放用的对照脚本输出达标报告。
  - 对照脚本：`tools/gateway-resilience/metrics_report.py`（`collect`、`report`），配合 `metrics.sql`、`caddy_cuts.py --json`、`session_streaks.py --json`；生产 0.3.0 基线已采集（私有目录）。待最终候选浸泡后出正式报告。
- [ ] 12.11 退出条件（全部闭环）：所有 Scenario 通过、P0/P1 缺陷为零、指标达标、升级与回退通过、真实客户端与真实 CDN 通过、XY-image 实验室通过。全部满足后才能提 PR。

## 上线前全面审查（2026-10-16）

> 用户 2026-10-16 要求在发版前全方位审查算法、策略与调度的遗漏与臃肿。每项修复一个提交并带测试，行为变化已同步到 design、spec、runbook 与 `docs/GATEWAY_RESILIENCE.md`；份额调度的修复见 `replace-controlled-with-share-scheduling` 的 design §20。
> 验证：`fd5eb7bf4` 时本机全量 unit、四种标签 vet、前端 lint/vue-tsc/vitest/构建与同步审计通过；此后的提交随最终候选复验（12.9–12.11、13.1）。

失败分类与罚则：

- [x] Gemini 的 `RESOURCE_EXHAUSTED` 分钟级限额（“You exceeded your current quota”）按限流处理，不再判为余额耗尽而排除约 11 分钟（`749060679`）。
- [x] “try again in 1m30.5s”“1h2m”等复合时长识别为重置时间，不再落到缺省冷却（`dafce54a5`）。
- [x] 网关自身合成的首事件超时 504 归为 `FIRST_OUTPUT_TIMEOUT`，不再按中转 504 记为 `TRANSIENT`（`a23119859`）。
- [x] 余额 `EXHAUSTED` 的硬档随排除期（下次复检后 10 分钟）结束，与选号一致（`a23119859`）。
- [x] `/v1/usage` 对不限额订阅返回的 `remaining=-1` 不再读成余额为零（`d20f6738b`）。
- [x] 被更长冷却（如 403）挡住而过期的余额暂停重新施加，不再被当作管理员手动恢复（`d20f6738b`）。
- [x] 断流（流内错误事件、停滞）后按部分用量计费的账号不再算成功：不解除冷却、不计入 `RECOVERING` 成功次数（`79d2a8c7e`）。
- [x] 探测锁 TTL 长于探测连同落库的耗时，落库使用独立时限（`51850f717`）。

会话与选号：

- [x] OpenAI 路由自有的换号循环也上报会话失败，所有循环经 `resilienceAttemptFailed` 每次失败上报一次（`b4b736fa2`）。
- [x] 同一请求内触发连续失败改绑时，原绑定账号不再保留绑定，本请求服务的账号可写入新绑定（`1187ffbb8`）。
- [x] 末位兜底按顺序尝试每个候选的并发槽位，全部占满才在排序第一的账号上等待，两种调度器一致（`07df4f016`）。
- [x] 负缓存的重放结果不再被重新写入（此前稳定流量下账号恢复后条目永不过期）；OpenAI 选号另按传输方式、端点能力与平台区分（`254d06b7e`）。
- [x] 会话标记键的分组段改为十进制 ID（此前被格式化为时长）（`dafce54a5`）。

预算与重试：

- [x] 服务内原地重试在确定执行后才扣减预算与同号次数，同一次失败不再被记两次（`5863e19a0`）。
- [x] 没有预算的 Gemini 请求（WebSocket、内部调用）保持 0.3.0 的原地重试上限（`41f5513f7`）。
- [x] 重试时上一尝试的首事件守卫立即释放，不再保留到整个流结束（`ec5291b6f`）。

错误语义与计费：

- [x] 同 Key 快速失败只重放触发它的模型，只在已触发时读取请求体中的模型（`2fa074e67`）。
- [x] 没有会话标识的请求不再共用一个失败回合计数（`53b708411`）。
- [x] OpenAI 路由上，管理员的错误透传规则优先于“上游余额对用户隐藏”，与网关路由一致（`a0db944f2`）。
- [x] Anthropic 已提交流的 `event: error` 行随 `errors` 开关，关闭时恢复 0.3.0 格式（`1c424685d`）。
- [x] 续链被拒后完整重放同样失败时，恢复该会话的续链（`ddf2dc5ab`）。
- [x] 会话缓存读占比在强制缓存计费改写用量之前记录，粘性切换后不再记成 1.0（`79d2a8c7e`）。

设置与界面：

- [x] 设置读取与发布都深拷贝，PUT 不再写入运行中的快照；`budget.model_overrides` 整体替换，可删除键（`2d2efb31c`）。
- [x] 分组改为订阅计费时清空无号兜底分组，保存不再被拒（`9d49f5e31`）。
- [x] 设置卡按能力分组（开关、说明与该能力的数值在同一张卡片）；运维面板的计数与未送达表显示分组名，分组列表只加载一次并交给选号解释，翻页用通用文案（`309c63062`）。
- [x] 文案：可配置值的提示写“默认 …”，错误语义提示补充失败回合封顶，Explain 统一称“选号解释”（`060f6f7ea`）。

性能与清理：

- [x] 主组健康且本实例在 `binding_ttl_seconds` 内未转出 Key 时，兜底判定不访问 Redis（`edcf36037`）。
- [x] 罚则判定直接读取余额状态字段，只有 `EXHAUSTED` 才解码完整记录（`0f8c6ba74`）。
- [x] 中转伪保活过滤不再复制每一行，500 个 delta 的分配从 1504 次降到 7 次（`50bfd36ec`）。
- [x] 删除无调用的 `resilience/frames.go`、预算的上下文访问器与 `Canceled()` 等（`181e38602`），以及 `resilienceBudgetAllowsAttempt`（`dafce54a5`）。
- [x] golangci-lint v2.14.0 清零（`5471e2112`）。

门禁、工具与上游：

- [x] `scheduler-diff.json` 补登记 Gemini 兼容服务改写的预算守卫行（`ac90d82d0`）。
- [x] CI 必测门禁 `tools/gateway-resilience/ci_gate.py`（见 13.1，`5d2854e53`）。
- [x] 浸泡脚本按容器匿名内存及其趋势判断泄漏，不再用含页缓存的 `docker stats`（`9ede9d5b3`）。
- [x] 上游缺陷：index.html 不再返回 304（CSP nonce 与缓存正文不一致），具名补丁 `xy-index-nonce-no-304`（`6c65c6cba`）。
- [x] 文档按代码校正：单实例索引、探测锁 TTL、OpenAI 冷却范围、末位兜底顺序、负缓存失效口径（`a96f54f23`、`fd5eb7bf4`、`4f3e37a3f`）。

## 生产报错优化（2026-10-16）

> 用户 2026-10-16 要求分析生产近 3 小时的报错并纳入本次优化。窗口 04:53–07:53Z，失败 1,649 次（11.1%）；去标识摘要见 `evidence/production-errors-20261016.md`，设计见 design §18。R2、R3、R4/R7 由并行子 Agent 在独立 worktree 完成后 cherry-pick 合入。

- [x] R1 Grok 余额 429 进入余额状态机，不再按中转重置头限流约 28 分钟（761 次，P0）（`6169e2128`）。
- [x] R2 已输出后的失败计入会话失败，账号 60 秒内 3 次软冷却（池模式除外）；停滞同样计入会话失败；已输出后失败的 200 不再清零失败回合计数（238 次）（`0bf650227`、`1f98afd89` 与本条后续提交）。
- [x] R3 API Key 中转拒绝 web_search 历史：同号补声明重放一次并按账号记住（65 次）（`b678afc91`）。
- [x] R4 内容安全拒绝归客户端错误：不罚账号、不重试不换号、客户端 400 带原文（49 次）（`a5fe6d324`）。
- [x] R7 Anthropic 流内错误按类型给状态：`rate_limit_error` → 429 可换号（14 次）（`f5579e776`）。
- [x] R5 Explain 指出模型被白名单或渠道定价拒绝（159 次，配置另需管理员修正）（`d5f713d5d`）。
- [x] R6 白名单 404 列出本分组可用模型（248 次）（`5851faae9`）。
- [x] R10b 上传中断的 400 按 499 记录（12 次）（`e759c0166`）。
- [x] 全 0 会话 ID 视为缺失（`859bde132`）。
- [x] 内存诊断：诊断镜像负载 80 分钟，堆与 goroutine 平稳，匿名内存预热后持平，无泄漏。
- [x] 测试服务器场景：PE-1（R1）、PE-3（R3）、PE-4（R4）、PE-7（R7）、PE-2（R2），新故障 F26、F27、F29 与 BAL 的 `reset`（`e4345e9e2`、`9fd60ec40`、`d138daa20`、`da75e0b92`）。
- [ ] 遗留（P2，不在本次）：Grok 进程内阻断对罚则可见；内容安全拒绝接入 Responses WebSocket 循环；web_search 重放覆盖 WebSocket 路径；已输出后失败未接入 Gemini 流、CN Anthropic 直通与 raw Chat 流内 error。

## 独立审查修复（2026-10-16，0.4.0 范围）

> 用户安排的独立只读审查（原版调度、份额调度、网关韧性；文档在仓库外 `review/SCHEDULING_REVIEW_20261016.md`）。用户决定 0.4.0 在 PR #97 内修 F1、F2+F5、F3、F4、F7、F12 与 F14 文档，期间暂停合并与打 RC。每项先写失败用例证实再修；证实不成立的记“复核不成立”。

- [x] F1 排队 ping 计为已送达：等待用户槽、账号槽、用户消息队列时的 ping 登记进送达统计，有送达统计时只用这一套心跳计数（`685986a68`）。修复前失败：`TestAccountSlotPingsAreNotDelivery`、`TestUserMessageQueuePingsAreNotDelivery`（`delivered=true`）。测试服务器 DC-5：排队期间收到 ping、首输出前离开，上游取消、只计持槽请求、未送达记录 `billed=false`。DC-5 原与已有场景共用 DC-4 编号而从未执行，已改号并让重复编号在加载时报错（`c21883e9c`）；场景工具计划断开时读 `conn.sock` 崩溃、`close()` 不真正断开，旧 DC-4 实际在响应头后立即离开，已修（`76dd9b3f5`）。
- [x] F2+F5 负缓存与快速失败只针对组级无号：只有组内不存在平台匹配、支持该模型且无罚的账号时才写负缓存并标记请求；快速失败只计带标记的 503 与配置错误；强制平台写进缓存键（`6cfd31e4a`）。修复前失败：`TestNegativeCacheOnlyForGroupLevelNoAccount`、`TestNegativeCacheKeepsAForcedPlatformApart`、`TestFastFailCountsOnlyGroupLevelNoAccount`；端到端 `TestStickyOnlyAccountKeepsServingBoundSessions`（RPM 仅粘性账号：新会话无号后已绑定会话照常服务，修复前得到缓存的 503）。测试环境没有 Anthropic OAuth 的 RPM 账号，以单测为准。
- [x] F3 份额分组末位兜底走份额内核“放宽一轮”：软罚与只被回避的账号视为可用（`last_resort_soft`），遵守层级、排空、权重与缓存域，由份额执行器取槽；绑定账号只是软罚且未被回避时不改绑，被回避的改绑到实际服务的账号；绑定账号按粘性请求过窗口费用与 RPM 门禁（审查会话复核第 1 点：份额会话的 `StickyOwner` 为 0）（`1b3d8e4fe`）。修复前失败：`TestResilienceLastResortFollowsShareTiers`、`TestResilienceLastResortSkipsDrainedAccounts`、`TestResilienceLastResortKeepsSoftBinding`、`TestResilienceLastResortGrantsStickyAllowanceToBoundAccount` 与 OpenAI 对应用例。测试服务器 LR-1。已知取舍：份额末位兜底不走模型路由优先；绑定账号饱和时按份额等待预算先等约 1 秒再溢出；冷会话绑定在较低层的软罚账号时，放宽一轮按 `return: cold` 规则可由较高层的软罚账号服务（绑定不变），因为只走末位兜底的请求不更新层级稳定性观测（测试服务器复现，两个账号都在软罚，结果可接受）。
- [x] F4 末位兜底门禁补全：Gateway 末位兜底与份额候选共用 `gatewayCandidateGates`（窗口费用、RPM、会话上限等），只放宽软罚；OpenAI 末位兜底取槽与等待计划用 Codex 自适应并发（`60cc7d6fb`）。修复前失败：`TestResilienceGateConsistency` 的窗口费用与 RPM 行（每个账号只违反一个门禁，原版、份额候选、末位兜底三处一致拒绝）、`TestResilienceOpenAILastResortUsesCodexAdaptiveConcurrency`（取槽上限 5，应为 1）；扩展的 `TestResilienceOpenAIGateConsistency` 另发现不支持 compact 的软罚账号会被末位兜底派去处理 compact 请求，一并修复。已知取舍：非份额分组的 OpenAI 末位兜底在自适应并发收紧时更保守。遗留：Grok 进程内阻断（团队限流、模型额度）在末位兜底中仍放宽，Gateway 列表阶段的 Grok 免费额度过滤在末位兜底的分组列表中未覆盖，归入 0.4.1 复核。
- [x] 复核中发现：OpenAI 旧调度路径（高级调度器关闭，缺省）在绑定账号软罚时仍清除绑定，并把临时服务的账号写成新绑定，与 design §4.2 不符。两处粘性读取在软罚时保留绑定，粘性写入处统一检查（`d6c8fd1e6`）。修复前失败：`TestResilienceOpenAILegacySchedulerKeepsSoftBinding`。生产 `openai_advanced_scheduler_enabled` 取值未核实，两条路径都已覆盖。
- [x] 测试服务器复跑中发现：OpenAI 的按模型瞬时屏蔽（上游连续 5xx 后屏蔽 10/45 秒）只在进程内，`resiliencePenaltyOf` 看不到，末位兜底把只被它挡住的账号当作“健康但不可用”排除，单号 OpenAI 分组在屏蔽期间整组无号（ERR-3/ERR-4 的 503 均为此）；F2 之后这类无号也不再计入快速失败。design §2 本就把它列为软罚。修复：OpenAI 末位兜底（原版与份额两条）按屏蔽结束时间把它视为软罚。修复前失败：`TestResilienceOpenAILastResortUsesARuntimeBlockedAccount`（两种调度均“no available OpenAI accounts”）、`TestResilienceOpenAILastResortOrdersRuntimeBlocksByTheirEnd`。粘性绑定在屏蔽期间仍按上游逻辑清除，登记 0.4.1。
- [x] F5 补充（审查会话复核时提出）：不写负缓存的续链、图片与 compact 选号按同一判据标记组级无号，快速失败照常计入；OpenAI 选号不再对这类请求查缓存（修复前缓存条目可能给后来选到账号的请求打上标记）（`e18275960`）。修复前失败：`TestUncachedOpenAISelectionStillMarksGroupNoAccount`、`TestUncachedOpenAISelectionIgnoresTheCacheEntirely`。
- [x] F7 运维错误记录保留改写前的状态码（经 CDN 的 502/504 不再记成 503）（`25bb15ca2`）。修复前失败：`TestOpsRecordKeepsTheStatusBeforeTheEdgeRewrite`。
- [x] F12 槽位等待截到剩余首输出前预算（`f2b077c4e`）。修复前失败：`TestSlotWaitEndsWithThePreOutputBudget`（预算剩 200 毫秒仍等满 3 秒）。
- [x] F14 文档：不受总开关控制的 5 项改动、生效重试上限表（runbook §6）、回退前的余额账号检查（`a53b90175`）；F1、F2/F5、F7、F12 同步 design、spec 与管理员说明（`80cca7e2c`）。

## 生产只读核查（2026-10-16，0.4.0 范围）

> 评审 agent 对照生产 0.3.0 近 48 小时数据核查 0.4.0 方案（报告在仓库外 `production/PROD_CONFLICT_REVIEW_20261016.md`），用户同意转交。结论：240 秒预算与 180/200/220 秒分档、冷却阈值、余额状态机与 F3/F4 均保持；首输出切断针对的是生产中真实存在的“经 Cloudflare 约 125 秒 0 字节切断、网关仍读完并计费、占着并发自我放大”。

- [x] 伪保活帧被丢弃后不刷新停滞计时（P1，0.4.0 引入的回归）：丢帧算“上游存活、不算输出”，Responses 流与经 Responses 上游的 Chat Completions、Messages 转换（流式与非流式）的停滞检测取最后读到一行与最后丢帧中较晚者；每丢一帧按账号计 `relay_keepalive`（`9adce14cb`）。没有采用“替换成注释行”：注释行会进入 Responses 路径的行处理并在首输出前被写入暂存或缓冲，若随下游保活刷出会按 1 字节计入送达统计。修复前失败：`TestRelayKeepaliveFramesKeepAStreamAliveAfterOutput`、`…BeforeOutput`、`…ChatCompletionsStreamAlive`、`…MessagesConversionAlive`、`…BufferedConversionAlive`（均 1 秒判停滞）。上游文件钩子已登记 required_literals；测试服务器 KA-9（输出后只发伪保活帧 135 秒）。不解决 Codex 自身 300 秒空闲上限。
- [x] 缺省余额关键字加 `insufficient user balance`（P1，`8285e6926`）。修复前失败：`TestInsufficientUserBalanceIsABalanceFailure`（生产原文 403 走计数，第 3 次置错）。`billing_error` 类型单独不算余额：Sub2API 系中转把订阅失效、订阅窗口、用户额度等计费前置失败都映射成它，误判会让账号至少一个复查周期内按硬罚排除（`TestBillingErrorTypeAloneIsNoBalanceFailure`）。
- [x] 本地首输出超时不给账号挂冷却（P2，按代码对齐文档，`fdb7cfdcb`）：design §2/§3、两份 spec 改为只计会话连败并换号；`stream_timeout_settings` 配置后沿用其原有处置（生产未配置）；上游以 `first_output_timeout` 应答（级联网关）仍软冷却。`TestLocalFirstOutputTimeoutLeavesTheAccountSchedulable` 锁定行为。理由：首输出慢多因中转排队或长推理、与请求档位相关，冷却会把其他档位的请求也挪走，收益不明。
- [x] runbook：部署前提（Caddy 按对端 IP 注入 `X-XY-Edge`，否则 125 秒切断照旧，上线后抽查一次）、上线前逐个确认 OpenAI 中转余额接口语义（12.6）、余额文案处理、上线后按组观测 `first_output_timeout`/`budget_exhausted`/`post_output_failure`/`undelivered`/`relay_keepalive` 与 Caddy 125 秒 0 字节切断率、管理员配置项（具体分组与账号见仓库外核查报告）。

## 后续（只登记，不在 0.4.0 实施）

> 份额调度在 0.4.1 合并前不在任何生产分组启用；0.4.0 已修 F3，若要提前启用需用户单独确认。

0.4.1（0.4.0 正式发布后另开 PR）：

- [ ] 份额策略配置精简（每组 22 项减到约 10 项，审查 5.1；`DecodePolicy` 对已删字段改为接受并忽略）；韧性设置精简（约 60 项减到约 35 项，审查 5.2 前四项）。
- [ ] F6 余额耗尽两处“真相”（推断，先补用例证实）；F8 §9.8 续链重放只匹配已知的中转笼统文案；F9 影子模式统计对话锚点覆盖率并写明切换冷启动；F10 删除份额纠偏；F11 缓存归因只对影子与份额分组运行；F13 管理员复检改为显式时间戳；F15 Warn 级逐请求事件改 Info 加计数或按分组限速；F16 输出 P99 改增量直方图；F17 预览改用只读 `ScanBindings`；F18 软/硬档阈值统一；F19 删除死常量与单值字段。
- [ ] xhigh/max 档位遇完全静默的上游不换号（P2，非回归，生产核查第 5 节）：首输出前停滞从最后读到一行起算 180 秒，早于 200/220 秒档位；OpenAI 路径的停滞只报错、不换号（只有 Grok 换号），还冷却账号、计会话连败。修法二选一：首输出前停滞在 OpenAI 路径也返回可换号错误（与 Grok 一致），或档位不超过停滞阈值。用例：拿到响应头后完全静默，xhigh、max 各一例，断言换号。生产 0.3.0 首输出前停滞 48 小时 26 次。
- [ ] OpenAI 按模型瞬时屏蔽期间保留会话绑定（design §4.2 例外）：两条 OpenAI 粘性路径的软罚判断接入 `resilienceOpenAIPenaltyOf`，需改上游文件两处已登记钩子；Explain 与无号 `Retry-After` 同样看不到该屏蔽。
- [ ] F2 判据补全（审查会话复核时提出）：判据只看账号罚则。组内账号全部软罚、末位兜底只因新会话撞上账号会话数上限而失败时，结果仍写入负缓存，已绑定会话最多收到 2 秒的 503。范围很窄；修法是末位兜底失败原因为会话上限时不写负缓存。
- [ ] CI 必测门禁合并为一个按清单驱动的脚本；项目记忆归档（主文件约 40KB，加大小上限）。

0.5：

- [ ] 统一可用性判定（F6、F13、F18）、会话回避存储、计数存储（韧性计数改用 Redis）。
- [ ] 下线韧性分开关与 0.3.0 旧分支，只留总开关；余额处理插件化。

## 13 PR 与发版

- [ ] 13.1 xy2api PR：CI 18 项全绿，并新增 “Gateway resilience” 必测作业（关键 Scenario 测试，`-race`，零跳过守卫）；XY-image PR 全绿。
  - 门禁已作为 `backend-ci.yml` test 作业中的必过步骤 “Gateway resilience” 加入：`tools/gateway-resilience/ci_gate.py` 自动收集韧性测试文件中的全部测试（`5d2854e53` 时 177 个，`060f6f7ea` 时 178 个），`-race` 运行，任一失败、跳过或未运行即失败；本机通过。
- [ ] 13.2 打 0.4.0-rc.1 标签并跑 Release 工作流；校验附件 SHA 与双架构 GHCR；在测试服务器用 RC 制品复跑 12.3、12.4 的关键子集与 12.7。
- [ ] 13.3 正式版：xy2api 0.4.0 PR → 合并 → 标签 → Release；XY-image 0.0.17 发布；更新 provenance 与记忆。

## 14 生产部署（需用户单独授权，按 runbook.md）

- [ ] 14.1 先部署 XY-image 0.0.17，再部署 xy2api 0.4.0；Caddy 配置边缘标记、`handle_errors` 与 `lb_policy first` 零停机切换。
- [ ] 14.2 部署后 24 小时、72 小时各按 design §14 输出一份指标报告；触发应急条件时关闭开关或回退镜像。

## 15 P2 进入条件（不在本次实现）

以下事项在“0.4.0 上线满 7 天且 §14 指标达标”后再立项：

- API Key 账号的通用健康熔断与降权（只在同一优先级内，且要求足够样本）；
- 份额调度影子运行与逐组启用；
- 按 Key 的有序兜底分组；
- 用户可见的未送达记录；
- 给 XY-image 的按请求 ID 查账本接口；
- G8：`x-codex-turn-state` 在服务端回注；
- 同源额度域联动（当前决定不做，重新评估时要有新的证据）。
