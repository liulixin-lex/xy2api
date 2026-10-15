# 任务

> 起点：origin/main `f8fec15a2`（业务代码与 v0.3.0 相同）。
> 开发分支：`feat/gateway-resilience-20261015`。XY-image 的配套改动在其仓库单独建分支。
> 全部能力在一个 xy2api PR 和一个 XY-image PR 中交付（D23）；不做灰度（D24）。
> 每个“验证”项都要保留实际命令、退出码与产物路径，失败记录不删除。
> 推送、PR、合并、发版、DNS 修改、生产部署都需要用户单独授权。

## 0 准备与工具

- [ ] 0.1 建开发分支，在 `docs/PROJECT_MEMORY.md` 的“进行中的工作”登记。
- [ ] 0.2 基线与度量工具 `tools/gateway-resilience/`：
  - `baseline.sql`：只读统计 design §14 的全部指标；
  - `caddy_cuts.py`：按入口统计 CDN 切断，并把切断记录与应用日志、用量逐条匹配；
  - `session_streaks.py`：统计同一会话在同一账号上的连续失败。

  基于本次分析脚本整理，不含凭据。
- [ ] 0.3 在生产只读环境采集一次基线（沿用已授权的只读范围），结果写入证据目录。
- [ ] 0.4 首输出阈值校准：只读导出生产 7 天“模型 × effort”的首字分布（P50/P95/P99/P99.9），确认 60/120/180 秒的分档与按模型覆盖值。
- [ ] 0.5 故障注入上游 `tools/gateway-resilience/fault-upstream/`（Go，无外部依赖）。以 OpenAI Responses、Chat、Anthropic Messages、Gemini、`/v1/usage`、new-api 计费接口的形状，按请求参数或头选择场景：
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
- [ ] 0.6 CDN 模拟代理：可配置“首字节前读超时”和“空闲读超时”（90、125、300 秒），并能注入 `X-XY-Edge` 标记。
- [ ] 0.7 真实客户端端到端测试脚本：驱动 Codex CLI、Claude Code、OpenAI/Anthropic SDK、OpenCode、go-genai/python-genai、curl，统计每个回合的请求数、最终错误文案与耗时。

## 1 上游失败分类（spec: upstream-failure-classification）

- [ ] 1.1 新增 `internal/resilience/classify.go`，按 design §2 实现：先看文本与 code，再看状态码；解析 `retry_after`（头、JSON 正文、Cloudflare 错误页）。
- [ ] 1.2 在以下路径接入分类，结果写入请求上下文与 ops 记录：
  - `ratelimit_service.go` 的 `HandleUpstreamErrorRaw`；
  - OpenAI 上游错误与流内 failed 事件；
  - Anthropic 网关；
  - Gemini 兼容服务；
  - 透传路径。
- [ ] 1.3 关键字集合可配置（`failure.balance_keywords_extra`）；未分类的上游 4xx 正文做脱敏、限长的采样日志。
- [ ] 1.4 确保本站计费错误不进入分类。
- [ ] 1.5 验证：表驱动单测覆盖 spec 中的所有场景，以及生产采样到的文案（脱敏后）；`go test -race ./internal/resilience/...`。

## 2 账号罚则与余额恢复（spec: account-penalties-and-balance-recovery）

- [ ] 2.1 `PenaltyOf`：区分硬、软，返回 `recoverAt` 与原因；`temp_unschedulable` 的 `reason` 前缀约定。
- [ ] 2.2 瞬时失败的软冷却（30 秒；有上游 `retry_after` 时取 min(retry_after, 120 秒)）。
- [ ] 2.3 余额状态机：
  - 存储 `accounts.extra.resilience_balance`；
  - 实现状态迁移与复检时间表（1/2/5/10/15 分钟，±15% 抖动）；
  - 把以下现有分支改走可恢复路径：Anthropic credit、402、非 OpenAI 403、余额类 429、图片 402。
- [ ] 2.4 `BalanceProbeService`：
  - 泛化 `CNProviderBalanceCheckService`；
  - 支持 sub2api 系 `/v1/usage`、new-api 计费接口、1 Token 生成探测（日预算 48）；
  - Redis 单飞锁；
  - 探测返回 401 时转凭据路径。
- [ ] 2.5 `RECOVERING` 半开：临时并发上限 1，满 3 次成功或 120 秒后回到 `OK`。
- [ ] 2.6 管理接口 `POST /api/v1/admin/accounts/:id/balance/recheck`；账号列表显示余额状态与下次复检时间；进入、恢复以及超过 72 小时未恢复时，通过现有告警与邮件通知管理员。
- [ ] 2.7 非 OpenAI 平台的 403 统一为“豁免、计数、冷却”。
- [ ] 2.8 验证：单测加真实 PostgreSQL/Redis 集成（状态迁移、单飞、半开、多实例）；`-race`；用故障注入上游演练“没钱 → 充值 → 自动恢复”。

## 3 会话失败处理与末位兜底（spec: session-failure-handling）

- [ ] 3.1 会话失败计数与回避集（Redis，TTL 600 秒），成功时清零。
- [ ] 3.2 原版调度钩子（`openai_account_scheduler.go` 的 `selectBySessionHash`、`gateway_scheduling.go` 的粘性路径、`gateway_service.go` 的 `shouldClearStickySession`）：软罚时保留绑定、本次请求跳过；硬状态才清除；连续失败达到阈值时按 owner 删除并加入回避。
- [ ] 3.3 份额调度适配（`share_scheduling_exec.go` 与候选构造）：软罚表示为 `RecoverAt`；回避表示为 `Excluded`；硬状态不带 `RecoverAt`；新增原因 `resil_failure_streak`、`last_resort_soft`、`fallback_return`。
- [ ] 3.4 末位兜底（两种调度器都要实现）：先转投兜底组（任务 8），否则在最高的有软罚账号的那一层，取罚期最短者；不受单次首输出超时约束。
- [ ] 3.5 Explain：份额调度的 Explain 显示罚则、计数、回避与兜底；原版调度新增只读 Explain。
- [ ] 3.6 在 `tools/upstream-sync/scheduler-diff.json` 中登记新增块，`sync.py scheduler-diff` 通过。
- [ ] 3.7 验证：
  - 矩阵测试：原版或份额调度 × 单号或多号组 × 软罚、硬罚、连续失败 × 有无兜底组；
  - 现有份额调度与 Gateway reliability 测试全部通过；
  - 用现有份额调度仿真与压测工具回归，确认缓存命中与首字不劣化（design §14 最后一行）。

## 4 错误语义与防风暴（spec: error-semantics-and-storm-protection）

- [ ] 4.1 选号层返回“组内最早解冻时间”；无号 503 与全部限流 429 加 `Retry-After`（2–30 秒），正文加 `Please try again in Ns.`。
- [ ] 4.2 配置类 503 加 `Retry-After: 60` 与 `x-should-retry: false`，状态码与文案不变（D14）。
- [ ] 4.3 无号负缓存（2 秒），账号恢复事件触发失效。
- [ ] 4.4 同 Key 快速失败（30 秒内 ≥10 次相同 503 时原样返回），按 spec 豁免。
- [ ] 4.5 上游余额对用户隐藏，包括图片余额 402。
- [ ] 4.6 用户余额：两条路径统一为 403 `INSUFFICIENT_BALANCE` 并换用新文案；在途预留超额改为 429 `concurrent_balance_reserved` 加 `Retry-After: 5`；估算改为缓存感知并以 P99 输出为上限。验收：用生产 7 天数据只读回放，余额为正的用户被拒次数减少 ≥90%，余额为负的金额不增加。
- [ ] 4.7 `image_task_handler.go`：准入检查放到内容审计之前；审计失败时释放准入。
- [ ] 4.8 验证：单测、handler 集成测试；真实客户端测试中 Codex 一个失败回合的请求数 ≤5。

## 5 请求预算与重试（spec: request-budget-and-retry）

- [ ] 5.1 `resilience.Budget`：上下文挂载，包含最多尝试数 4、首输出前截止 240 秒（文本）、每账号同号重试计数，以及 `Canceled()`。
- [ ] 5.2 接入 OpenAI 的 Responses、Chat、Messages、Images 循环；移除首输出超时只换 1 次的限制；有效换号上限取预算与 `max_account_switches` 的较小值。
- [ ] 5.3 接入 Anthropic 网关循环（Messages、Responses、Chat 兼容）。
- [ ] 5.4 Gemini 原地重试从 5 次降为 1 次，等待改为可取消。
- [ ] 5.5 同号重试只用于容量降载与池模式配置码，每账号最多 1 次；容量降载只换到不同主机；账号级错误直接换号。
- [ ] 5.6 退避加 ±20% 抖动；上游 `Retry-After` 封顶 60 秒且不超出剩余预算。
- [ ] 5.7 验证：spec 全部场景；故障注入上游组合场景；`-race`。

## 6 首包前保活、首输出超时、停滞与提交后的失败（spec: pre-output-keepalive）

- [ ] 6.1 应用侧：仅当对端是受信代理时解析 `X-XY-Edge`；按 `keepalive.edges` 判断是否经 CDN。
- [ ] 6.2 泛化 compact 心跳原语为 `PreOutputKeepalive`：
  - 首拍延迟 15 秒、之后间隔 10 秒；
  - 提交时带 SSE 头与 `X-Client-Request-ID`；
  - 心跳字节计入扣除口径；
  - 写侧加锁，与正常写入互斥；
  - 换号时不重置计时。
- [ ] 6.3 各协议的心跳帧；Gemini 与官方 OAuth 账号不提前提交；图片与异步端点不提前提交。
- [ ] 6.4 首输出超时：OpenAI 按 effort 分档（60/120/180 秒）并支持按模型覆盖；Anthropic 与 Gemini 首事件超时 90 秒；最后一个候选不设单次超时。
- [ ] 6.5 修复 G2：按 `min(T/4, 5 秒)` 检查；文本已输出后停滞 120 秒判定为断流；图片维持 900 秒。
- [ ] 6.6 提交后的终止帧：
  - Responses：`response.failed`，带 code 与 “try again in Ns”；
  - Chat：`data:{error}` 加 `[DONE]`；
  - Anthropic：`event: error`（修复 G3）；
  - 写完立即关流。
- [ ] 6.7 验证：
  - 用 CDN 模拟代理验证 90、125、300 秒三档都不再切断；
  - 快速失败仍返回原状态码；
  - 心跳后仍可换号；
  - Codex 300 秒空闲约束下，预算耗尽时客户端收到可重试终止帧；
  - Anthropic SDK 能把 `event: error` 抛为 APIError；
  - Gemini 与 OAuth 例外生效；
  - 心跳与 SSE 都不被 Caddy 缓冲（`encode` 不匹配 `text/event-stream`）。

## 7 客户端断开与计费（spec: client-disconnect-and-billing）

- [ ] 7.1 送达状态跟踪（未送达、部分送达、完成），所有协议与非流式路径都要覆盖。
- [ ] 7.2 未送达时立即取消上游，覆盖选号、等槽位、等响应头、换号间隙、首输出前；释放预留与并发槽位。
- [ ] 7.3 迁移 274，新建 `gateway_undelivered_requests` 表及其写入、按保留期清理；管理接口 `GET /api/v1/admin/resilience/undelivered`。
- [ ] 7.4 部分送达后断开：最多再读 180 秒，到时取消，按已得用量计费。
- [ ] 7.5 滥用保护：每 Key 每小时超过 30 次后按上游成本计费，`billing_mode='undelivered_abuse'`。
- [ ] 7.6 迁移 275：`ops_error_logs` 的断开阶段与已送达字节数；499 补全耗时。
- [ ] 7.7 验证：
  - 未送达请求在 `usage_logs` 中没有行；
  - XY-image 的核对器按“查不到即未扣费”收敛；
  - 计费与预留的账目一致性测试；
  - 真实 PostgreSQL 集成测试。

## 8 无号兜底分组（spec: availability-fallback-group）

- [ ] 8.1 迁移 272、273，以及 Ent schema 与重新生成的代码。
- [ ] 8.2 分组管理接口与前端：选择兜底组，校验环与平台。
- [ ] 8.3 选号集成：只在无号时触发，每请求一次，兜底组的资格约束全部生效。
- [ ] 8.4 计费：按兜底组倍率；`origin_group_id`；用户用量列表与可用分组页显示兜底关系。
- [ ] 8.5 主组稳定 120 秒后切回（Redis 时间戳）；兜底组内绑定的 TTL 为 600 秒。
- [ ] 8.6 验证：spec 场景；计费与用量正确；切回不抖动；与仅限 Claude Code 的 `fallback_group_id` 互不影响。

## 9 设置、管理界面、可观测与文档

- [ ] 9.1 `gateway_resilience_settings`：服务层、`GET/PUT /api/v1/admin/settings/gateway-resilience`、校验规则（design §10）、读取失败时用最后已知值。
- [ ] 9.2 前端（中英文）：
  - “网关韧性”设置卡；
  - 运维页的韧性面板；
  - 账号余额状态与“已充值”按钮；
  - 未送达记录列表；
  - 分组兜底字段；
  - 用量中的兜底标记。
- [ ] 9.3 结构化日志（design §12）与统计接口 `GET /api/v1/admin/resilience/stats`。
- [ ] 9.4 降噪：`upstream_billing_rate_sync_applied` 降为 debug；余额批量探测不写审计；`ops_system_logs.message` 只保留模板。
- [ ] 9.5 文档 `docs/GATEWAY_RESILIENCE.md`（管理员说明与配置），并加入 `.gitignore` 的 docs 白名单；更新本记忆。
- [ ] 9.6 验证：前端 lint、typecheck、全量 vitest、构建；浏览器检查 1440/390 宽度、明暗主题、键盘操作；后端 handler 与 repository 测试。

## 10 上游同步登记

- [ ] 10.1 `policy.json` 与 `UPSTREAM_BASE.json`：登记 7 个具名补丁（design §13），每个带 required_literals 与绑定测试。
- [ ] 10.2 `scheduler-diff.json` 白名单更新；`sync.py audit` 在干净工作树上通过。
- [ ] 10.3 `VERSION` 升为 0.4.0；`SUB2API_COMPAT_VERSION` 不变。

## 11 XY-image 0.0.17（见 xy-image-appendix.md）

- [ ] 11.1 按附录第 2 节完成 X1–X8，各带单测。
- [ ] 11.2 与 xy2api 0.3.0（生产现状）和 0.4.0 候选双向兼容；XY-image 实验室 `deploy/xy2api-lab` 全量通过。

## 12 测试服务器集成验证（见 staging-validation.md）

- [ ] 12.1 部署隔离环境：Compose 项目 `xy2api-staging`，包括候选镜像、PostgreSQL 18、Redis 8、故障注入上游、CDN 模拟、测试用 Caddy 站点；不触碰已有服务。
- [ ] 12.2 测试数据：
  - 夹具分组（单号、双号、多号；原版与份额调度；有无兜底组）；
  - 用户与 Key；
  - 用户提供的真实中转测试 Key（低价模型，总预算不超过 20 美元）。
- [ ] 12.3 故障注入矩阵：逐条覆盖 8 个 spec 的全部 Scenario，留存证据。
- [ ] 12.4 真实客户端端到端矩阵（design §15），统计每回合请求数、错误文案与耗时。
- [ ] 12.5 真实 CDN：经用户授权，在 Cloudflare 添加橙云测试子域，验证切断消失与心跳格式；ESA 视条件而定。
- [ ] 12.6 用真实 new-api 系中转实测余额接口；不支持的类型确认会降级到生成探测。
- [ ] 12.7 升级与回退演练：
  - 生产只导出表结构（只读），加同量级合成数据（usage_logs 约 330 万、ops_system_logs 约 580 万、ops_error_logs 约 37 万、api_keys 约 2.5 万等）；
  - 完成 0.3.0 → 0.4.0-rc 升级，记录迁移耗时与锁；
  - 回退到 0.3.0 镜像后能正常运行；
  - 再前滚回 0.4.0-rc。
- [ ] 12.8 XY-image 0.0.17 测试实例接入测试 xy2api，覆盖生图（同步与异步）、对话、余额核对。
- [ ] 12.9 24 小时浸泡（混合负载与故障注入）：协程、并发槽位、预留、Redis 键、内存无泄漏；日志里没有凭据。
- [ ] 12.10 按 design §14 核验指标；生产回放用的对照脚本输出达标报告。
- [ ] 12.11 退出条件（全部闭环）：所有 Scenario 通过、P0/P1 缺陷为零、指标达标、升级与回退通过、真实客户端与真实 CDN 通过、XY-image 实验室通过。全部满足后才能提 PR。

## 13 PR 与发版

- [ ] 13.1 xy2api PR：CI 18 项全绿，并新增 “Gateway resilience” 必测作业（关键 Scenario 测试，`-race`，零跳过守卫）；XY-image PR 全绿。
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
