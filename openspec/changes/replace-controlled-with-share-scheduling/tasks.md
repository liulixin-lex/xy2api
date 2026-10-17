# 任务

> 规划日期 2026-10-09，起点 `main=3569d4f00aae3eada079eb7ac058dc62256a66b8`；2026-10-10 按“缓存命中与首字不退化”修订（design §6.2–§6.8、§12、§14）。各阶段独立 PR、独立门禁；阶段 1 可以单独发布（只删不增，行为回到原版）。所有“验证”项必须保留实际命令与退出码，失败记录不删除。

## 阶段 0：准备（不改行为）

- [x] 0.1 D1–D10 定稿（2026-10-10 用户授权全权决策 D5/D8/D9/D10，全部按 proposal 建议值实施）。
- [x] 0.2 决定（2026-10-10）：本轮不合入，阶段 1 以 main `3569d4f00` 为起点；上游 v0.2.15 之后按仓库同步流程单独合入，届时由 0.3 的差异白名单检查调度文件。原评估项：评估是否先按仓库同步流程合入上游 v0.2.15（调度文件与 v0.2.14 相同，冲突面主要在新平台与协议修复）；如合入，阶段 1 以合入后的 main 为起点。
- [x] 0.3 建立“原版调度差异白名单”检查脚本与测试，写在 `tools/` 下并接入 CI。
  - 实施：`tools/upstream-sync/scheduler-diff.json`（按 `UPSTREAM_BASE.json` 的上游提交记录 11 个命名二开块）、`sync.py scheduler-diff`（行级比对，忽略空行与移动的 import；`--draft` 生成草稿）、`tools/tests/test_upstream_sync.py` 3 项测试，CI `upstream-sync-audit` 作业与 `doctor.py --audit` 均已接入；注入一行未登记改动可被检出。另有 `tools/tests/test_controlled_scheduling_residue.py` 防止 controlled 残留回流。
  - 覆盖文件：`gateway_scheduling.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`gemini_messages_compat_service.go`。
  - 记录相对固定上游标签允许存在的二开块：模块路径、`requireCompact` 参数、Codex Ticket、组合分组、份额钩子。
- [ ] 0.4 基线度量（只读）。合并后在生产只读副本执行（需另行授权）。
  - 进度（2026-10-10）：只读脚本 `tools/share-scheduling/baseline.sql` 覆盖 0.4–0.6 的 SQL 部分，已在本地迁移后的库上用合成数据验证（份额、分桶与斜率结果符合构造值），用法见 `docs/SHARE_SCHEDULING.md`「灰度前基线」。
  - 用 `usage_logs` 统计近 7 天各分组按账号的请求/Token/标准成本份额、Token 加权缓存读占比、首字 P50/P95/P99，按模型与输入大小分桶，作为灰度对照。
  - 记录当前生产 `scheduling_system_mode` 实际值与已保存的分组策略数量。
- [ ] 0.5 等待系数校准（只读，合并后执行；脚本以最小二乘斜率加分桶中位数近似分位回归）：按模型族对 `first_token_ms` 与“未缓存输入 Token = 输入 − 缓存读”做分位回归，得到每 1k Token 的首字斜率，校准 `affinity.wait.ms_per_1k_tokens` 默认值（规划值 100）。
- [ ] 0.6 会话键画像（只读，合并后执行；显式会话覆盖率由脚本按 `usage_logs.session_id` 统计，Anthropic 键来源需统计 `sticky.hash_source` 日志）。
  - 从现有 `sticky.hash_source` 日志统计 Anthropic 请求的会话键来源比例：`metadata_user_id` / `cacheable_content` / `message_content_fallback`。
  - 统计 OpenAI 显式会话头与 `prompt_cache_key` 的覆盖率，用于评估对话锚点（D9）的收益范围。

## 阶段 1：剔除智能可控调度（行为回到 Sub2API 原版）

- [x] 1.1 网关：删除 `schedulingMode` 中间件及 `ControlledSchedulingMiddleware`；确认网关请求不再读取 `scheduling_system_mode`。
- [x] 1.2 选择入口：删除 controlled 钩子并恢复原版语义。
  - 涉及文件：`gateway_scheduling.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`gemini_messages_compat_service.go`。
  - `selectAccountWithScheduler` 恢复为原版语义：原版模式当前调用 `selectAccountWithoutQualityRouting`，保持不启用质量路由（D3）。
  - 实施（2026-10-10）：入口经 `selectAccountWithQualityRouting`，质量路由只在请求上下文显式 opt-in（`withOpenAIQualityRouting`）时生效；生产路径从不 opt-in，因此直接落到 `selectAccountWithoutQualityRouting`，与原版等价；质量路由测试显式 opt-in 以保持覆盖。
  - `tryAcquireAccountSlot` 恢复原版并发；Codex 压力并发留给份额调度（D4）。
- [x] 1.3 handler：删除 `ControlledSchedulingEnabled`、`handleControlledSchedulingStop`、`controlledAccountSwitchExhausted`、`CaptureControlled*` 分支，逐处与上游同版本对照。
  - 涉及文件：`failover_loop.go`、`gateway_handler*.go`、`openai_gateway_handler.go`、`openai_chat_completions.go`、`openai_images.go`、`openai_embeddings.go`、`openai_alpha_search.go`、`grok_media.go`、`gemini_v1beta_handler.go`、`gateway_systemone.go` 等。
- [x] 1.4 转发与服务：删除 65 个非测试文件中的 controlled 分支，`Sub2APISchedulingEnabled` 恒真分支展开。
  - 涉及文件：`openai_ws_*`、`gemini_messages_compat_service.go`、`bedrock_stream.go`、`antigravity_*`、`ratelimit_service.go`、`openai_gateway_*` 等。
  - 恢复原版 5xx/过载冷却、流超时惩罚、同账号重试、签名整流等语义。
- [x] 1.5 计费。
  - `usage_billing_recovery.go` 去掉调度确认。
  - `UsageBillingCommand.SchedulingAttemptID` 保留为兼容字段并忽略。
  - `scheduling_usage.go` 删除，`writeSchedulingUsageLog` 改回原版写法。
  - 补测试：升级前格式（含 `SchedulingAttemptID`）的计费意图能被新版本恢复，且只结算一次。
- [x] 1.6 依赖注入：删除 `ProvideControlledSchedulingService` 与四处 `controlledHTTPUpstream` 包装、`CloseControlledScheduling`；重新生成 `wire_gen.go` 并审查。
- [x] 1.7 删除 `backend/internal/scheduling/` 整包，以及全部 controlled 服务文件与测试。
  - 对每个被删测试，确认其断言要么只属于 controlled，要么已在原版路径有等价测试；PR #74 相关回归必须保留或改挂。
  - 只读负载窥视 `PeekAccountsLoadBatch` 保留，并解除对 `scheduling` 包的依赖。
- [x] 1.8 管理接口：删除 `handler/admin/scheduling_*.go`；`/api/v1/admin/scheduling/*` 与账号调度控制/故障域路由改为统一 410 退役处理器（附新接口说明）。
- [x] 1.9 前端。
  - 删除 design §13.1 所列页面、组件、store、API、类型、文案与测试。
  - `SettingsView.vue` 恢复原版设置常显。
  - `AccountsView.vue` 恢复原版列与调度评分列。
  - 账号弹窗优先级文案恢复。
  - 侧栏移除入口。
- [x] 1.10 CI 与同步策略。
  - 替换 `backend-ci.yml` 中点名的 controlled 测试。
  - 移除或改挂 `typesafe-controlled-settlement` 补丁。
  - ~~`UPSTREAM_BASE.json` 无需改历史记录~~ 更正（2026-10-10）：`sync.py audit` 要求 policy 与 `UPSTREAM_BASE.json` 的 `custom_patches` 清单一致，因此两处同时移除 `typesafe-controlled-settlement`；同步历史报告 `docs/upstream-sync/v0.2.14.json` 不改。
  - 实施：`Scheduling reliability` 改为 `Gateway reliability`（GPT61 Sol 校验、非流读失败换号、重放安全、质量路由默认惰性、缓冲读失败换号、退役路由 410）；`TypeSafe scheduling and hosted recharge integration` 改为 `Hosted recharge integration with real PostgreSQL`。
- [x] 1.11 文档：controlled 相关文档标注历史或删除；README 中调度说明回到原版描述。
- [x] 1.12 验证（证据见文末「验证记录」）：
  - 构建与测试：`cd backend && go build ./... && go test -tags=unit ./...`，关键包 `-race`，`go test -tags=integration ./...`（真实 PG/Redis，零跳过守卫）；
  - 残留检查：`rg "internal/scheduling|ControlledScheduling|controlledScheduling|Sub2APISchedulingEnabled" backend frontend/src` 无结果（文档与迁移除外）；
  - 原版差异白名单检查通过；
  - 网关请求在 sqlmock / 连接计数下不再出现 `scheduling_system_mode` 查询；
  - 前端：`pnpm lint:check && pnpm typecheck && pnpm test:run && pnpm build`；
  - 隔离环境烟测：0.2.5 → 新版本升级、新装、恢复备份回退 0.2.5，检查健康、登录与选号；
  - `python3 tools/upstream-sync/sync.py audit` 在干净树通过。

## 阶段 2：观测基线、份额调度内核与服务适配

- [x] 2.0 缓存归因与选择耗时，先于份额调度上线，原版分组同样启用，用于积累同口径基线（design §14.2）。
  - 进度（2026-10-10）：结算时归因、抽样会话记录与按决策类型首字直方图已完成，原版分组按 1/4 抽样同样统计；决策样本含选择耗时。
  - 完成（2026-10-10）：抽样选号按调度器记录 `sel:<模式>:*`（耗时、Redis 往返与耗时、数据库查询），报表「选号开销」展示。Redis/数据库指标依赖 `server.enable_server_timing` 安装的计时钩子，未开启的节点只记耗时，接口以 `io_samples` 与 `null` 如实表示（design §17）。本地真实 Redis 实测见验证记录。
  - 实现决策上下文、结算时归因、抽样会话记录、`xy2:cache:attr` 计数。
  - 用 `servertiming` 记录选择阶段 Redis 往返次数与耗时。
  - 原版选号代码只增加只读观察，不改变选择结果。
- [x] 2.1 `backend/internal/sharesched` 纯算法：
  - 策略类型与严格校验（含 `cache_domain` 同平台）、默认值（权重默认取并发）；
  - HRW 排序与同域/同类稳定分区；
  - `Plan` 纯函数：层级、亲和生命周期（§6.3）、缓存冷热、等待预算、软溢出、冷窗口重对齐、回升与排空截止（含错峰）、降层、理想账号；
  - 纠偏数学、原因码枚举、决策结构。
- [x] 2.2 内核测试：
  - 比例性：固定种子 ≥1 万会话，误差 ≤1 个百分点；
  - 最小扰动（精确断言）、跨实例决定性；
  - 层级严格：性质测试；
  - 亲和生命周期：表驱动，覆盖命中、有价值等待、无价值不等、等待超时改绑、软溢出边界、硬不可用、冷窗口重对齐、截止错峰；
  - **缓存保护性质测试**：随机生成策略变更序列（改权重、回升、排空无截止、纠偏、切换调度器），断言没有任何空闲时长小于缓存寿命且有可缓存前缀的会话被迁移；
  - 纠偏区间；
  - 校验拒绝未知字段、越界、重复账号、跨平台缓存域。
- [x] 2.3 迁移 `270_share_scheduling.sql`：建表，并把旧分组策略导入为未启用草稿；更新 `migrations/checksums.json`；迁移测试覆盖空表、旧策略含权重 0、无旧表三种情况。
- [x] 2.4 策略仓储（XY 自有原生 SQL）。
  - 读取、CAS 写入、审计字段。
  - 进程内缓存 + Redis 广播 + 30 秒刷新；全局开关缓存 5 秒。
  - 读取失败回落最后已知值；冷启动无值时按原版。
- [x] 2.5 绑定读写：在 XY 自有文件中实现。
  - `GET`+`PTTL` 单管道读取；`SET NX` 新绑定，改绑覆盖，命中续期；键格式与原版一致，OpenAI 走现有兼容读写。
  - 集成测试断言往返次数与原版相同（design §12）。
- [x] 2.6 亲和键与缓存画像。
  - 对话锚点（API Key + 系统/工具摘要 + 首条用户消息），仅份额分组使用；
  - `prefix` 键；
  - 缓存寿命判定：出站请求体中的 `ttl:"1h"`、平台、`cache.ttl` 覆盖；
  - 复用冷写预留的 Token 估算；
  - 模型最小可缓存长度表。
- [x] 2.7 Gateway 适配：`SelectAccountWithLoadAwareness` / `SelectAccountForModelWithExclusions` 钩子 + `share_scheduling_gateway.go`。
  - 候选、资格原因与恢复时间、窗口费用/RPM 粘性语义；
  - 会话数、抢槽、原版等待计划（预算为超时），超时后 no_wait 重入；
  - 模型路由。
- [x] 2.8 OpenAI 适配：`selectAccountWithScheduler` 等入口钩子 + `share_scheduling_openai.go`。
  - previous_response_id、guardian、媒体强续接保持原版。
  - 能力、传输、compact、复检、利润门。
  - Codex 压力并发作为显式容量闸门（D4）。
- [x] 2.9 Gemini 兼容适配：`share_scheduling_gemini.go`。
  - 实际无需独立文件：Gemini v1beta 与 Antigravity 生成路径均经已挂钩的 `GatewayService.SelectAccountWithLoadAwareness`；AI Studio 元数据接口与 `count_tokens` 保持原版。
- [x] 2.10 决策记录：异步批量归因计数与决策环形列表；Redis 故障时只丢统计。
- [x] 2.11 影子运行：`scheduler=shadow` 抽样计算 `Plan`（无副作用），记录一致率、预计溢出/改绑/重对齐/热迁移与耗时。
- [x] 2.12 纠偏后台任务：对启用纠偏的分组，每 30 秒聚合一次 `usage_logs`。
- [x] 2.13 在 `tools/upstream-sync/policy.json` 登记份额钩子与归因观察点为自有补丁并绑定测试（`custom_patches` / `custom_patch_tests`）。
- [x] 2.14 验证：
  - 适配层单测（假快照/假并发/假 PTTL）；
  - 真实 PG/Redis 集成：策略 CAS、广播、NX 竞争、PTTL 空闲推算、归因计数、报表 SQL；
  - 两实例一致性测试；
  - 500 账号选择基准：P99 增量 < 1ms，数据库查询增量 0，Redis 往返不多于原版；
  - 影子模式与 `sub2api` 的选择结果、绑定、并发计数逐请求一致。
  - 进度（2026-10-10）：适配层单测与 `-race` 已通过（含 CI “Gateway reliability” 门禁模拟）。
  - 完成（2026-10-10）：真实 PG 18 / Redis 8 本地服务上的 `repository/share_scheduling_integration_test.go`（CAS、广播、NX 竞争、PTTL、归因计数、报表 SQL）；`service/share_scheduling_instances_test.go` 两实例一致性；`share_scheduling_cost_test.go` 500 账号预算（Redis 往返与数据库查询不多于原版）与基准；`share_scheduling_shadow_test.go` 影子与原版逐请求一致（Gateway 400 请求含无会话与满载/排除，OpenAI 400 请求仅会话，因其无会话选号含时钟熵）。CI 中这些集成测试由 testcontainers 启动 PG/Redis 执行。

## 阶段 3：管理接口与前端

- [x] 3.1 管理接口：settings、groups、policy（CAS）、preview（含预计热迁移、重建 Token 与容量告警）、explain（含缓存画像）、report（含缓存归因与按决策类型首字）、decisions、rebalance（默认只清已冷绑定）；严格 JSON、审计动作、权限与参数上限。
  - 偏差（2026-10-10）：预览给出落点变化比例与预计强制迁移会话数，未估算重建 Token；审计沿用管理端审计中间件的自动动作名；分组列表仅含启用分组，组 0 不可在管理端配置。
- [x] 3.2 前端。
  - 进度（2026-10-10）：系统设置卡片、份额调度页面（分组列表含影子与草稿标记、行内与批量编辑、预览、冲突保留草稿并可重新套用、报表、解释、重平衡、导入提示）已完成。
  - 完成（2026-10-10）：层级卡片拖拽改层（多选一起移动、拖到新卡片建层、选中本层、只看本层），账号编辑弹窗只读显示各分组生效规则（`GET /accounts/:id/rules`），报表「选号开销」。
  - 系统设置：总开关、默认值与观测设置。
  - 份额调度页面：
    - 分组列表，含影子状态；
    - 账号表：行内/批量/按层编辑，含缓存域；
    - 保存前预览，显示热迁移与代价；冲突时保留草稿；
    - 报表：份额、缓存归因、首字；
    - 解释工具、草稿导入提示。
  - 账号详情：只读显示所在分组的层级、权重与缓存域。
- [x] 3.3 i18n（中英）与使用说明 `docs/SHARE_SCHEDULING.md`。
  - 精度边界与硬封顶建议。
  - 缓存寿命与冷窗口说明、回升截止的代价、缓存域的正确用法。
- [x] 3.4 验证：前端全套 + 新组件交互测试；真实浏览器桌面/手机、深浅色、键盘；接口契约测试。
  - 进度（2026-10-10）：lint、typecheck、全量 vitest、构建与新组件/接口契约测试已通过。
  - 完成（2026-10-10）：本机 Chrome 无头模式经 DevTools 协议实测（详见验证记录），发现并修复两处：页头描述过长导致 1440px 桌面横向溢出 146px（改用短副标题）；账号弹窗中规则框挤在优先级列内（改为整行）。

## 阶段 4：端到端验收与灰度

- [x] 4.1 合成上游端到端（`TestShareSchedulingE2EWeightsOutagesAndPolicyChanges`、`TestShareSchedulingE2ETierReturnsInColdWindow`；份额误差按 3σ，见 design §17）：3 账号 5:3:2，3000 个会话。合成上游按账号保存前缀，按 5 分钟/1 小时寿命返回缓存读写 usage。
  - 新会话份额误差 ≤1 个百分点。
  - 对 A 注入 429（重置 120 秒）：A 的会话软溢出且绑定不变，恢复后回到 A 并读到缓存。
  - 注入 600 秒限流：改绑并按 B:C=3:2 分散，报表归因 `rate_limited` / `miss_switch_unavailable`。
  - 改权重与无截止排空：热迁移计数为 0。
  - 第 2 层会话在冷窗口迁回（`return_cold`）。
- [x] 4.2 首字前失败换号次序确定（`TestShareSchedulingE2EFailoverOrderIsDeterministic`；已输出后不换号由既有 `TestStreamWrittenGuard_*_AbortFailoverOnSSEContentWritten` 覆盖）：同会话重复失败总是落到同一次选，同缓存域优先；已输出内容后不换号（沿用原版保护）。
- [x] 4.3 满载场景（`TestShareSchedulingE2ESaturatedLongSessionWaitsThenRebinds`，往返预算见 2.14 与验证记录）：长上下文会话在预算内等待绑定账号，超时改绑；短会话不等待；等待上限与 Redis 往返次数符合 design §12。
- [x] 4.4 全局开关与分组开关即时回退（`TestShareSchedulingE2ESwitchesFallBackWithoutMigrating`，含策略库不可用、冷启动与绑定读取失败），且回退不迁移绑定；策略库不可用时不返回 503。
- [x] 4.5 RC 制品、隔离升级/回退演练。
  - 进度（2026-10-10）：本地隔离演练已完成（见验证记录）；RC 制品需发版授权，合并后执行。
  - 2026-10-13：随 `v0.3.0-rc.1` 完成：制品与双架构镜像校验、全新 318 条迁移、0.2.6 旧库升级、成套恢复后运行旧版（见 `docs/UPSTREAM_SYNC_V0215.md` 与项目记忆 0.3.0 发布记录）。4.6、4.7 待做：份额调度在 0.4.1 合并前不在任何生产分组启用（`gateway-resilience-20261015` tasks“后续”）。
- [ ] 4.6 影子运行 1–2 个分组至少 3 天：一致率、预计热迁移为 0、开销达标。
- [ ] 4.7 灰度一个分组 3–7 天，按 design §14.4 门禁与 0.4 基线比较；形成灰度报告后再推广。
- [ ] 4.8 后续版本：controlled 历史表导出后用独立迁移删除（D7）；退役路由删除。

## 交付物检查

- [x] `docs/PROJECT_MEMORY.md` 按阶段登记、交接与日志。
- [ ] 每个阶段的 PR 描述列出删除/新增文件、迁移、配置、回退方法与验证证据。

## 验证记录（2026-10-10，本机 macOS arm64，Go 1.27.0，PostgreSQL 18.6 / Redis 8.10.2 本地服务）

| 项目 | 命令 | 结果 |
|---|---|---|
| 构建与静态检查 | `cd backend && go build ./... && go vet ./...`，另以 `-tags=unit`、`-tags=integration` 各 vet 一次 | 全部 exit 0 |
| 后端单测 | `go test -tags=unit ./...` | exit 0，61 个包 ok |
| CI “Gateway reliability” 复现 | 同工作流命令（`-race -p 1 -parallel 1`，含 `TestShareScheduling*` 等） | exit 0，55 个顶层用例通过、0 跳过 |
| 份额相关 `-race` | service / sharesched / handler/admin / servertiming 的份额用例 | exit 0 |
| golangci-lint v2.13.2 | `golangci-lint run ./...` | 0 issues |
| 真实存储集成 | `go test -tags=integration -count=1 ./...`（本地服务经仅本地使用、未提交的 harness 接线；CI 用 testcontainers） | exit 0；repository 包 705 通过、3 跳过（用量导出 S3 等既有条件用例） |
| 500 账号基准 | `go test -tags=unit ./internal/service -run '^$' -bench ShareSchedulingSelection500 -benchmem -count=3` | 份额 498–502 µs/op、1094 allocs；原版 317–339 µs/op、382–406 allocs；均值增量约 0.18 ms（预算 1 ms） |
| 前端 | `eslint .`、`vue-tsc --noEmit`、`vitest run`、`check:i18n`、`vue-tsc -b`、`vite build` | 全部 exit 0；vitest 359 文件 2802 例 |
| 工具与同步 | `python -m unittest discover -s tools/tests -p 'test_*.py'`、`sync.py scheduler-diff`、`git diff --check` | 19 例通过；白名单一致；无空白错误 |
| 基线脚本 | 本地迁移后库 + 2000 行合成 `usage_logs` 执行 `baseline.sql` | exit 0；份额、分桶与斜率（构造 100 ms/1k，算得 100.0）符合预期；临时库已删除 |

隔离烟测（本地，`127.0.0.1`，合成 Anthropic 上游，结束后删除库、Redis DB 5/6 与凭据文件）：

1. 0.2.5（`3569d4f00` 构建）自动安装、建分组/两个 API Key 账号/用户 Key，并通过旧接口保存受控策略（账号 B `traffic_weight=0`）；`/v1/messages` 12 次 200，第二轮 6 次读到缓存。
2. 停服、`pg_dump` 后在同库启动新版本：迁移 270 应用，旧策略导入为 `sub2api` 草稿（`imported_from=scheduling_group_policies:v1`，A 层 1 权重 3，B 层 2 排空）；健康、登录、请求正常；旧路由 `GET /admin/scheduling/groups/:id` 返回 410；保存份额策略后旧版本号再存返回 409；解释、账号规则、报表、决策接口正常。
3. 总开关关闭时策略不生效（`effective=false`），开启后 200 个新会话按 74%/26% 落位（目标 75/25），第二轮 200 次亲和命中并全部读到缓存；流式请求的首字分位正常统计。
4. 开启 `ENABLE_SERVER_TIMING` 后同负载实测选号 Redis 往返：份额 10.0 次、原版 10.2 次，数据库查询均为 0。未开启时 Redis/数据库指标此前恒为 0，已改为按采集样本统计并在无样本时返回 `null`（design §17）。
5. 浏览器（Chrome 无头 + DevTools 协议）：桌面 1440×900 与手机 390×844 × 浅/深色 × 中/英文，份额调度页三个标签与账号编辑弹窗无横向溢出、无控制台错误或 4xx/5xx；键盘 Tab 顺序为侧栏 → 页头 → 分组 → 标签页 → 操作 → 调度器 → 层级卡片 → 账号行，Enter 切换标签，Esc 关闭弹窗，焦点样式可见。修复了页头描述溢出与弹窗规则框布局两处问题。
6. 回退：0.2.5 直接运行在已迁移的库上（旧表与设置未动）健康、登录、流式/非流式请求正常；另以升级前备份恢复（回到 269、无新表）后 0.2.5 同样正常。
7. 全新安装：新版本空库迁移至 270（317 条），建数据、请求、保存并启用份额策略正常，日志无 ERROR。

合并 main（0.2.6 GPT 缓存可靠性，`7b1d5d8c`）后复验（Go 1.27.2，golangci-lint v2.14.0，与 CI 一致）：

| 项目 | 结果 |
|---|---|
| `go build ./...`、`go vet`（默认 / unit / integration） | exit 0 |
| `go test -tags=unit ./...` | exit 0，61 个包 ok |
| CI “Gateway reliability” 复现（`-race`） | exit 0，138 个顶层用例通过 |
| CI “OpenAI cache reliability with real Redis” 复现（本地 Redis） | exit 0，40 例通过，16 个必需用例全部实际执行 |
| `go test -tags=integration -count=1 ./...`（本地服务接线同上，未提交） | exit 0，55 个包 ok（含 repository） |
| golangci-lint v2.14.0 | 0 issues |
| `tools/tests`、`sync.py scheduler-diff`、`sync.py audit`（干净树） | 19 例通过；白名单一致（新增 0.2.6 已审 hunk）；审计通过 |

## 上线前加固与压测（2026-10-11）

加固项见 design §18。压测在本机进行：同一 PostgreSQL / Redis 上的两个网关实例，请求随机分发；合成 Anthropic 上游按账号保存提示缓存（5 分钟），首字 = (80 ms + 每 1k 未缓存 Token 30 ms) × 抖动。分组 5 个 API Key 账号：第 1 层权重 4:3:2:1、第 2 层 1 个备用，并发均为 6。负载为多轮会话：4–9 轮，间隔 0.3–1.5 秒，70% 流式，80% 带客户端会话标识；80 个并发客户端，过载阶段 160 个。每阶段 90–150 秒，原版与份额调度在同一时间线上注入同样的故障。

| 阶段 | 调度 | 请求 | 成功率 | 后续轮缓存命中 | 后续轮同账号 | 首字 P50 / P95 / P99（ms） | 后续轮首字 P95 |
|---|---|---|---|---|---|---|---|
| 稳态 | 原版 | 8372 | 100% | 90.7% | 85.3% | 111 / 336 / 408 | 334 |
| 稳态 | 份额 | 8536 | 100% | 98.9% | 98.9% | 110 / 317 / 394 | 205 |
| 故障（A 429、B 慢 2.5 秒、Redis 暂停 3 秒、C 断连、D 529） | 原版 | 9751 | 99.97%（3 次等待队列满 429） | 89.1% | 82.8% | 114 / 370 / 2977 | 362 |
| 同上 | 份额 | 9952 | 100% | 95.0% | 94.6% | 114 / 364 / 2768 | 351 |
| 运维（改权重、排空 B、冷重平衡、总开关关 10 秒再开、恢复） | 份额 | 10584 | 100% | 97.4% | 97.0% | 111 / 322 / 392 | 272 |
| Redis 清空重启 + 重启后改策略 | 份额 | 8390 | 100% | 98.4% | 98.4% | 112 / 328 / 817 | 263 |
| 过载（160 并发，账号并发合计 30） | 原版 | 12132 | 99.41%（71 次 429、1 次等槽超时） | 67.8% | 56.2% | 285 / 2261 / 7824 | 1868 |
| 同上 | 份额 | 13501 | 99.99%（1 次等槽超时） | 71.3% | 62.9% | 263 / 1171 / 6939 | 954 |
| 影子对比 | 原版选号 | 6214 | 99.98%（原版 1 次 429） | 89.3% | 83.3% | 111 / 338 / 412 | 337 |

观察：

- 稳态新会话份额为 31/29/24/14%（目标 40/30/20/10%）：并发相同使 A 的容量份额只有 25%，A 满载被跳过 273 次，先在预算内等待（1081 次，全部等到，平均 79 ms），超出才溢出改绑（80 次）。报表的容量份额与跳过率给出了相同结论，符合“份额不是硬上限”。
- 原版的 429 来自粘性等待路径（`gateway.account_wait_queue_full`，`sticky_session_max_waiting=3`）。份额调度对绑定账号先有界等待再溢出，全部满载时才按原版最后一层排队（`fallback_wait_timeout` / `fallback_max_waiting`）。
- 运维：改权重后新会话立即按新权重放置，热会话不迁移（命中率保持约 99%）；排空后 B 不再接新会话，存量会话自然结束。整段热迁移原因只有满载溢出与故障换号，没有改权重或排空导致的迁移。总开关关闭期间命中率回到原版水平（89%），重新开启后恢复。
- Redis 清空重启：份额调度命中率只在重启的 10 秒内降到 93.6%，下一个 10 秒恢复到 98.6%（加权会话哈希使丢失绑定的会话大多落回原账号）；原版同期降到 83.7%。重启后两个实例重新订阅变更广播，从 A 保存的策略 5 秒内在 B 生效。
- 影子对比：一致率 73–76%，跳过 0 次（第二轮 4115 次请求中跳过 8 次，报表显示）。
- 压测后：没有残留的并发槽位，等待计数全部为 0；两实例日志无 panic，ERROR 只有注入的上游 429/529，没有份额调度告警；协程数空闲 178–202 → 负载 270–370 → 结束后 186–189。

计数存储改为分钟桶加小时桶后（design §18），用新构建再跑份额稳态与影子对比：结果与上表一致（成功率 100%，后续轮命中 98.96%）。分钟桶 TTL 2 小时 10 分，小时桶 26 小时；15 分钟、1 小时、6 小时、24 小时四个报表窗口读到的决策数相同（8430），没有重复计数，每次读取 8–16 ms。

| 项目 | 命令 | 结果 |
|---|---|---|
| 构建与静态检查 | `go build ./...`；`go vet ./...`（默认 / unit / integration） | 全部 exit 0 |
| 后端单测 | `go test -tags=unit ./...` | exit 0，61 个包 ok |
| 份额相关单测 `-race` | `go test -tags=unit -race ./internal/sharesched/` 与 `./internal/service/ -run 'ShareScheduling\|Share'` | exit 0 |
| CI “Gateway reliability” 复现 | 同工作流命令（`-race -p 1 -parallel 1`） | exit 0，143 个用例（含子用例）通过、0 跳过；新增 5 个加固用例均实际执行 |
| CI “OpenAI cache reliability with real Redis” 复现 | 同工作流命令，本地 Redis | exit 0，40 例通过 |
| golangci-lint v2.14.0 | `golangci-lint run --timeout=30m ./...` | 0 issues |
| 真实存储集成 | `go test -tags=integration -count=1 ./...`（本地服务接线，未提交） | exit 0，55 个包 ok；份额 8 个用例含重平衡“已改绑”与小时桶 |
| 前端 | `eslint`、`vue-tsc --noEmit`、`vitest run`、`check:i18n`、`vue-tsc -b && vite build` | 全部 exit 0；vitest 359 文件 2802 例 |
| 工具与同步 | `tools/tests`、`sync.py scheduler-diff`、`git diff --check` | 19 例通过；白名单一致；无空白错误 |

PR #90 推送后，CI 的 Unit tests 首次失败：`TestShareSchedulingShadowMatchesOriginalPerRequest` 连续发出 400 个请求，影子对比在后台排队。CI 机器负载高时，未完成的对比超过 32 个上限，多出的被跳过，用例一直等不到 400 次对比。这是上限按设计生效，不是行为错误。
回放用例改为每个请求等待其影子对比记录完成（同时统计 `shadow_skipped`），再断言 400 次全部比较、跳过为 0。本地以 `-cpu 1,2 -race -count=3` 运行，并在全量单测并行时以 `-cpu 1 -count=10` 压测，均通过。

## PR #90 复审精简后验证（2026-10-12，本机 macOS arm64，Go 1.27.2，PostgreSQL 18.6 / Redis 8.10.2）

精简内容见 design §19。起点为 PR 头 `23f191fc`，其 CI 的 Integration tests 因迁移 271 失败。

| 项目 | 命令 | 结果 |
|---|---|---|
| 构建与静态检查 | `go build ./...`；`go vet ./...`（默认 / unit / integration） | 全部 exit 0 |
| 后端单测 | `go test -tags=unit ./...` | exit 0，61 个包 ok |
| CI “Gateway reliability” 复现 | 同工作流命令（`-race -p 1 -parallel 1`） | exit 0，154 个用例（含子用例）通过、0 跳过；工作流必需用例与 5 个新增一致性用例均实际执行 |
| CI “OpenAI cache reliability with real Redis” 复现 | 同工作流命令，本地 Redis | exit 0，40 例通过 |
| 真实存储集成 | `go test -tags=integration -count=1 ./...`（本地服务接线，未提交） | exit 0，55 个包 ok，含此前 CI 失败的 `TestUpstreamV0214MigrationUpgrade` |
| golangci-lint v2.14.0 | `golangci-lint run --timeout=30m ./...` | 0 issues |
| 前端 | `eslint`、`vue-tsc --noEmit`、`vitest run`、`check:i18n`、`vue-tsc -b && vite build` | 全部 exit 0；vitest 359 文件 2816 例 |
| 反向验证 | 去掉设置纪元、刷新版本保护、前端“保持我的显式配置”后重跑对应用例 | 用例均失败；恢复后通过 |
| 500 账号基准 | `-bench ShareSchedulingSelection500 -count=5` | 原版 308–317 µs/op，份额 476–505 µs/op，均值多约 0.18 ms（预算 1 ms） |

双实例压测（合成上游与负载同 2026-10-11，账号与权重相同，全新安装库迁移到 270）：

| 阶段 | 请求 | 成功率 | 后续轮缓存命中 | 首字 P50 / P95 / P99（ms） | 后续轮首字 P95 |
|---|---|---|---|---|---|
| 原版稳态（环境基准） | 6277 | 100% | 89.0% | 111 / 343 / 428 | 340 |
| 份额稳态 | 8517 | 100% | 98.9% | 110 / 316 / 381 | 202 |
| 份额故障（同 10-11 时间线） | 10099 | 100% | 96.3% | 112 / 348 / 2699 | 329 |
| 份额运维（改权重、排空、冷重平衡、总开关） | 10609 | 100% | 97.7% | 111 / 322 / 395 | — |
| 份额 Redis 清空重启 | 8399 | 100% | 98.6% | — / 323 / 757 | — |
| 份额过载（160 并发） | 13034 | 99.96%（5 次全满载排队 30 秒超时） | 70.5% | 273 / 1331 / 7697 | 1128 |
| 影子对比 | 4145 | 99.98%（原版 1 次等待队列满 429） | 88.9% | — / 351 / — | — |

- 运维阶段的热迁移全部是满载溢出改绑；改权重和排空没有引起热迁移。Redis 重启后两个实例重新订阅，重启所在 10 秒命中率 94.3%，下一个 10 秒 99.8%。
- 报表 15 分钟、1 小时、6 小时、24 小时窗口的计数一致，精度标记分别为 `minute` 与 `hour_rounded`；统计丢弃为 0。
- 压测后无残留并发槽位，等待计数为 0；Redis DB 7 共 9173 个 key、4.4 MB，没有新的 key 族；日志无 panic、无份额调度告警；协程数在负载期间 240–350，不持续增长。
- 同等负载 A/B（80 并发、各 100 秒）：
  - 份额每次选号 12.74 ms，其中亲和等待 10.71 ms，扣除后 2.03 ms；原版 1.68 ms；Redis 往返 10.6 对 10.5 次。
  - 按请求类型的首字：新会话 P99 份额 1816 / 原版 3612 ms；后续轮 P95 203 / 338 ms，P99 371 / 379 ms。整体 P99（514 / 426 ms）的差异来自混合：份额的后续轮几乎都命中，最慢的 1% 由新会话构成。新会话超过 1 秒是突发时全部满载的排队，原版同样出现（34 次对份额 17 次）。
  - 把 `ms_per_1k_tokens` 调到合成上游的 30 后，等待超时换号增多（命中率 97.8%），P99 升到 604 ms。默认 100 在此场景更合适；生产仍按基线脚本实测的首字斜率校准。
- 回退与前滚：停新版本后，0.2.6（origin/main `7b1d5d8c` 构建）直接运行在已迁移的库上：健康、登录、账号接口正常，份额接口 404，487 个请求全部成功、无 ERROR。再切回新版本，策略版本、设置与 543 个请求均正常。

## 上线前规范化（2026-10-16）

随网关韧性 0.4.0 的上线前审查完成，详情见 design §20。验证随网关韧性最终候选统一执行（见 `gateway-resilience-20261015` 的 tasks 12.9–12.11、13.1）。

- [x] 回升截止时间只从高层已知的恢复时间起算，恢复时间未知时不再立即强制迁移热会话（`6e916fa1c`，`TestTierReturn`）。
- [x] 余额耗尽与额度自动暂停计入健康原因，恢复后的高层账号等 `tiers.stable_seconds` 再接回会话（`c5ffd57ba`，`TestRechargedAccountWaitsForStabilityBeforeTierReturn`）。
- [x] 报表窗口执行文档写明的 1 分钟下限（`26c882ef7`，`TestShareSchedulingHandlerRejectsMalformedRequests`）。
- [x] 复核策略校验、CAS 保存、Redis 扫描与条件删除、统计上界、影子对比并发上限，未发现问题。
- [x] 页头精简：去掉重复标题的卡片，只在总开关关闭时显示提示条与设置链接（`261795a74`）。
- [x] 分组列表只给已保存策略的分组显示调度器徽标（`261795a74`）。
- [x] 策略表在 1440 px 宽度不再横向溢出，排空、缓存域、份额与恢复默认按钮不再竖排换行（`995785b61`、`060f6f7ea`）。
- [x] 报表：窗口改用通用下拉组件，空分布合并为一行提示，首字表只在有样本时显示，决策样本的空状态区分“窗口内没有”与“未开启保存”（`261795a74`、`060f6f7ea`）。
- [x] 数字用等宽数位（`tabular-nums`）而非等宽字体（`261795a74`）。
