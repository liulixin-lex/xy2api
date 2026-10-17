# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-12 及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（2026-10-17 原样移入）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### 版本与环境速览（2026-10-17T08:40Z 核实）

- 正式版：XY2API v0.3.0（2026-10-13，Sub2API 兼容基线 0.2.15）；生产自 2026-10-15 运行 0.3.0。
- 进行中：XY2API 0.4.0，晋级分支 `release/0.4.0`（PR #100），`v0.4.0-rc.1` 已发布；进度见下一节。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-17 定时检测），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.2）；golangci-lint v2.14.0（与 CI 同版）。

### 网关韧性 0.4.0、份额调度与前端规范化（状态更新：2026-10-17T04:45Z；PR #97 已合并，`v0.4.0-rc.1` 已发布并验证；0.4.0 晋级 PR 准备中，等用户确认“未送达不计费的覆盖路径”一项后再合并与打正式标签；生产未改）

**背景**

- 生产 0.3.0 只读分析发现：上游失败不分类、单号分组瞬时错误即整组不可用、慢首包被 CDN 切断仍计费、同会话在失败账号上反复重试、Codex 一个失败回合最多约 30 次请求。完整报告不入库，去标识摘要见方案 `evidence/production-analysis.md`。
- 方案 `openspec/changes/gateway-resilience-20261015/`（proposal 含决策 D1–D33、design、8 个 spec、tasks、runbook、staging-validation）；管理员说明 `docs/GATEWAY_RESILIENCE.md`。
- 份额调度（`openspec/changes/replace-controlled-with-share-scheduling/`）已随 0.3.0 发布、默认关闭。

**目标**

1. xy2api 0.4.0（先 RC）：网关韧性全部能力上线可用，之后 XY-image 0.0.17。
2. 全方位审查算法、策略、调度的遗漏与臃肿，规范化代码，达到生产部署标准（用户 2026-10-16T06:10Z 要求）。
3. 同标准规范化 0.3.0 的份额调度（后端与前端）（07:30Z 追加）。
4. 交付前规范各功能的前端布局、排版、交互与文案，页面高效、不臃肿（07:40Z 追加）。
5. 维护本文件，让后续 Agent 能实时了解背景、目标、要求、已完成和未完成项（08:00Z 追加）。
6. 派子 Agent 分析生产近 3 小时报错请求的根因，把根因与证据纳入本次优化（并发上限与粘性、重试、熔断等）；必要时多 Agent 并行开发（08:05Z 追加）。

**要求与约束**

- 不做灰度：在测试服务器（隔离 Compose 项目，地址见仓库外 `private/HOSTS.md`）完整验证、全部闭环后再 PR 与发版；用户已授权推送、PR、0.4.0-rc.1 与 0.4.0，**生产部署每次单独授权**。
- 生产只读；不提交到 Sub2API 上游；仓库公开：主机地址、宿主机路径、服务清单只写仓库外私有目录；不写入任何密钥、Token、口令。
- 上游同步只走既有机制：上游文件的新钩子登记 `policy.json` 的 `required_literals`，调度文件改动登记 `scheduler-diff.json`，对上游行为的修正登记为具名补丁（`policy.json` 与 `UPSTREAM_BASE.json` 的 `custom_patches` 及 `custom_patch_tests`）。
- 每个修复一个提交并带测试；提交前跑 golangci-lint v2.14.0（`/tmp/xy2-toolchain/bin`）、`sync.py audit` 与 `scheduler-diff`。
- 不动 XY-image 的 agent01 工作区，只在其副本与 v0016 兼容工作树内工作；真实中转测试 Key 只在测试服务器 0600 文件中。

**已完成**

- 韧性能力开发与测试服务器验证（场景 37/37、真实客户端 16/16、真实 Cloudflare、升级回退演练、XY-image 回放 181/181，均在前一候选上）。
- 2026-10-16 全面审查：约 35 项修复已提交（会话失败上报、末位兜底只试首个候选、余额耗尽档位、网关自身 504 分类、兜底分组 Redis 往返、余额状态 JSON 往返、中转保活过滤分配、死代码删除等），golangci-lint 清零，`scheduler-diff` 补登记，新增 CI 必测门禁 `tools/gateway-resilience/ci_gate.py`（177 个韧性测试，`-race`，零跳过）。
- 份额调度后端审查：修复回升截止时间在恢复时间未知时立即强制迁移热会话、余额耗尽与配额暂停不推迟回升、报表窗口下限；其余（策略校验、CAS、Redis 扫描与条件删除、统计上界、影子并发上限）复核无问题。
- 上游缺陷修复：index.html 不再返回 304（CSP nonce 与缓存正文不一致导致每次刷新拦截内联配置脚本），具名补丁 `xy-index-nonce-no-304`。
- 前端规范化：份额调度页去掉与顶栏重复的大标题卡、分组列表只给已配置分组显示徽标、策略表 1440 宽不再横向溢出、报表统一下拉与合并空状态、数字统一等宽数位；网关韧性设置按能力分组（开关与参数同卡）；运维韧性面板显示分组名并与选号解释共用分组列表；文案中可配置的默认值不再写成固定值；份额调度菜单图标与“渠道状态”区分。1440/390 × 明暗截图检查无横向溢出、无控制台错误（本机 vite 代理测试环境 API）。
- 本机验证（`fd5eb7bf4` 时）：全量 unit、无 tag 测试、四种标签 vet、前端 lint/vue-tsc/vitest 389 文件/构建、部署脚本检查、同步审计均通过；`-race` 仅上游测试桩 `TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant` 有数据竞争（测试代码，非本分支引入，CI 不对该包跑 race）。

**未完成**

1. 生产报错优化（进行中）：报告 `xy2api-artifacts/.../production/errors-3h-20261016T0753Z.md`（04:53–07:53Z，推理请求 14,866，失败 1,649 即 11.1%）。代码项：
   - R1（P0，主 Agent，**已完成** `6169e2128`）：Grok 单号分组一次 429（正文“余额不足”）被锁约 28.6 分钟、整组 429，761 次（窗口前约 2,660 次）。API Key 类、非池模式 Grok 账号的余额字样错误先进余额状态机（30 秒内有成功为弱信号 SUSPECT、软档可末位兜底；否则 EXHAUSTED，60 秒起探测），不再按中转重置头装限流；OAuth、池模式与非余额错误不变。遗留（P2，未做）：Grok 进程内阻断（模型配额、团队限流）对 `resiliencePenaltyOf` 不可见。
   - P2 已完成（主 Agent）：全 0 会话 ID 视为缺失（`859bde132`，具名补丁 `xy-placeholder-session-id`）；分组白名单 404 列出可用模型（`5851faae9`，`xy-allowlist-404-hint`，248 次）；上传中断的 400 按 499 记录（`e759c0166`，R10b）；Explain 给出模型被白名单或渠道定价拒绝（`d5f713d5d`，R5 代码侧）。
   - R4+R7（子 Agent A，**已合入** `a5fe6d324`、`f5579e776`）：内容安全拒绝归 `CLIENT_ERROR`（原因 `content_policy`）、不计 strike、不重试不换号、客户端 400 `content_policy_violation` 带脱敏原文；Anthropic 流内 `rate_limit_error` → 429（可换号、带 Retry-After），`overloaded_error` → 529，`api_error` → 502。
   - R2（子 Agent B，**已合入** `0bf650227`、`1f98afd89`）：已输出后的失败（失败事件、读错误、无终止事件）每次尝试上报一次：会话按 TRANSIENT 计数，账号 60 秒内 3 次软冷却 `transient:POST_OUTPUT`（池模式除外）；停滞也计入会话失败。新指标 `post_output_failure`。
   - R3（子 Agent C，**已合入** `b678afc91`）：API Key 中转在输出前以 “response protection is unavailable” 拒绝含 `web_search_call` 且未声明的请求时，同号补声明重放一次（计预算），成功后账号记 6 小时；仍失败则 502 带原因、不换号、计会话失败与回合封顶。
   - 三个 Agent 的提交均无冲突 cherry-pick；文档（design §2/§13/§18、tasks、specs、coverage-matrix、`GATEWAY_RESILIENCE.md`）已同步；测试服务器新场景 PE-1/2/3/4/7 与故障 F26/F27/F29、BAL `reset`。
   - 遗留 P2 见 tasks“生产报错优化”末项（Grok 进程内阻断可见性、WebSocket 路径、Gemini 等流未接入已输出后失败）。
   - 需管理员做的配置（不改代码）：分组 53 的 gpt-5.5 补渠道定价或移出白名单（159 次）；单号分组补号或配无号兜底分组；因余额 403 置错的账号充值后复检；加大 Caddy 日志保留。安全：settings 表中 `openai_codex_ticket_harvest_proxy_url` 含明文代理凭据，建议轮换。
   - 文档同步本轮修复已合入（`2d819361c`、`2587e20fb`）。
2. 内存诊断（08:50Z 初判“无泄漏”过早，见操作日志 12:10Z 更正；现以 GC 后存活堆判定）：诊断镜像 `diag-0efbafc53` 负载 80 分钟内堆在用 53.4→59.0 MB（增量为连接 bufio、WS 状态绑定与日志池，均有界），goroutine 270–315 平稳，匿名内存预热后稳定在 92–94 MiB（前段中位 81、后段 93，低于判据 1.2×+16）。旧浸泡的增长是预热期。重建最终候选时停掉诊断负载并删除诊断镜像与 `diag-heap.sh`。
3. 最终候选 `eaa63fd30`（含全部生产报错修复）：本机全量验证全绿（四种 vet、lint 0、unit/无 tag/embed、CI 门禁 221 个、同步审计、前端 389 文件/3047 项与构建）；测试服务器场景 42/42、真实客户端 16/16、XY-image 回放 0.0.17/0.0.16 均 181/181、在线对话 4/4，白名单 404 提示与 Explain 限制端到端确认；无迁移改动，升级演练结论沿用。**浸泡经用户决定缩短为 6.5 小时：11:12:30Z 启动（`soak-968df0942`，应用带 `GODEBUG=gctrace=1`），约 17:53Z 连同 10 分钟回落与检查结束；以 GC 后存活堆完整小时中位数斜率 ≤1 MiB/h 判定泄漏**；结束后跑 `/tmp/xy2-postsoak-metrics.sh` → `metrics_report.py` 指标报告 → 定稿私有 `staging/STAGING_REPORT.md`。
4. [PR #97](https://github.com/liulixin-lex/xy2api/pull/97) 已开（经用户同意与浸泡并行跑 CI）；浸泡与 CI 都通过后合并 → 在合并提交打 `v0.4.0-rc.1` → 校验 Release 制品 → 测试服务器用 RC 制品复跑关键子集 → 晋级 PR（VERSION 0.4.0）→ `v0.4.0`；之后 XY-image 0.0.17（先交接留言并经用户确认）。推送用 `git -c credential.helper= -c 'credential.helper=!gh auth git-credential'`（本机 GCM 会卡在交互提示）。
5. 生产部署（单独授权，按 runbook）。
6. 收尾清理：本地备份分支 `backup/gateway-resilience-pre-rewrite`、测试服务器 itest 目录与 `xy2-itest-*` 卷、诊断镜像与 `diag-heap.sh`、`/tmp/xy2-staging/live-keys.json` 与 `ui-session.json`。

- **2026-10-16T12:40Z 用户决定（独立只读审查后）**：暂停合并 PR #97 与打 `v0.4.0-rc.1`，先在 PR #97 内修复 0.4.0 范围：F1（排队 ping 计为已送达）、F2+F5（负缓存与快速失败只针对组级无号，强制平台入键）、F3（份额分组末位兜底走份额内核“放宽一轮”）、F4（末位兜底门禁补全、Codex 自适应并发、门禁一致性表测试）、F7（运维日志保留原始状态码）、F12（槽位等待截到剩余预算）、F14（不受总开关控制的 5 项与生效重试上限表）。审查文档在仓库外 `review/SCHEDULING_REVIEW_20261016.md`。每项先写失败用例证实，证实不成立记“复核不成立”。0.4.1/0.5 事项只登记到 tasks 后续小节；份额调度在 0.4.1 合并前不在任何生产分组启用。新候选需重跑全部场景、真实客户端与 6.5 小时浸泡；F1、F3 补测试服务器场景。分工：F3+F4 由子 Agent 在独立 worktree 完成，其余主 Agent。
- **进度（13:20Z）**：已提交 F1 `685986a68`、F12 `f2b077c4e`、F7 `25bb15ca2`、F2+F5 `6cfd31e4a`（含端到端 `TestStickyOnlyAccountKeepsServingBoundSessions`）、F5 补充 `e18275960`（不写负缓存的续链/图片/compact 选号按同一判据标记，OpenAI 只对可缓存选号查缓存）、F14 `a53b90175` 及文档、DC-4（F1）、LR-1（F3）场景与 fault-upstream 记录完整 spec。审查会话复核：F1、F2+F5、F7、F12、F14 与子 Agent 的 F4（`d24a7d5b6`，尚在子 Agent 分支）正确；另提三点：①份额会话 `StickyOwner` 为 0，F4 的末位兜底 sticky 参数对份额会话失效（已转告子 Agent 在 F3 处理并补用例）；②F5 收窄了快速失败（已按方案 a 修复）；③F2 判据不看会话上限（已登记 0.4.1）。待：子 Agent 完成 F3 → 合入 → 全量验证 → 新候选部署测试服务器（停旧浸泡）→ 全部场景、客户端、6.5 小时浸泡 → 推送 PR #97。
- **进度（13:35Z）**：子 Agent 的 F4 `60cc7d6fb`、F3 `1b3d8e4fe`（含复核第 1 点：门禁 sticky 取份额绑定账号，`TestResilienceLastResortGrantsStickyAllowanceToBoundAccount`）已 cherry-pick 合入，worktree 与分支已删除。子 Agent 另发现：不支持 compact 的软罚账号会被末位兜底派去处理 compact 请求（随 F4 修复）；OpenAI 旧调度路径（高级调度器关闭，缺省）软罚时仍清除绑定并把临时账号写成新绑定，主 Agent 已修（`d6c8fd1e6`，上游文件钩子已登记 required_literals 与 scheduler-diff）。design §4.2/§4.3、session spec 新场景、覆盖矩阵、tasks 与管理员说明已同步（`fed225d84`、`e44be85d0`）。下一步：本机全量验证 → 停旧浸泡 → `redeploy2.sh` 部署新候选 → `final-run.sh`（浸泡改 6.5 小时）→ 指标报告 → 推送 PR #97、更新 PR 描述、等 CI。
- **进度（14:10Z）**：本机全量验证（HEAD `e44be85d0`）全绿：四种 vet、lint 0、unit/无 tag/embed、CI 门禁 245 个韧性测试（-race、0 跳过）、同步审计与 scheduler-diff、前端 389 文件/3047 项与构建。旧浸泡 `soak-968df0942` 运行 2 小时 23 分后于 13:35Z 停止（存活堆 58 MB、匿名内存 89 MiB 均平稳）。新候选 `c21883e9c` 已部署（`final-run.sh` 浸泡已改 6.5 小时），13:55Z 启动全量场景 → 客户端 → 浸泡。部署前单跑发现两处测试工具问题并修复：①F1 场景与已有 DC-4 同号被覆盖、从未执行，改为 DC-5 并让重复编号加载即报错（`c21883e9c`）；②`stream_request` 计划断开时读 `conn.sock`（`Connection: close` 下取得响应后为 None）崩溃且 `close()` 不真正断开，旧 DC-4 实为响应头后立即离开（`76dd9b3f5`）。LR-1 原用 chat completions 无会话，份额分组按 API Key 归为同一会话，第二个请求沿用第一个的绑定（F3 的软罚保留绑定语义，非缺陷），改为每请求独立会话后通过。DC-5 已在新候选上通过（F1 端到端确认）。迁移、schema、前端 API 自 `eaa63fd30` 无改动，升级演练与 XY-image 回放沿用。
- **进度（14:45Z）**：候选 `c21883e9c` 场景 44/44、客户端 16/16 后，对比旧候选证据发现 ERR-4 的快速失败不再触发。根因：OpenAI 按模型瞬时屏蔽（上游连续 5xx 后 10/45 秒）只在进程内，`resiliencePenaltyOf` 看不到，末位兜底把只被它挡住的账号排除，单号 OpenAI 分组在屏蔽期间整组无号（旧候选同样如此，F2 后又不计快速失败）；design §2 本把它列为软罚。修复 `31407833d`（先写失败用例；原版与份额两条 OpenAI 末位兜底按屏蔽结束时间视为软罚；屏蔽期间粘性绑定仍被上游逻辑清除，登记 0.4.1）。测试服务器确认：ERR-1/3/4 改为经末位兜底到达上游（502 + Retry-After，19 次 `last_resort_soft`）。`c21883e9c` 浸泡已停；`31407833d` 本机全量验证全绿（CI 门禁 247），14:31Z 启动 `final-run.sh`（场景 → 客户端 → 6.5 小时浸泡，约 21:40Z 结束）。之后：指标报告（`/tmp/xy2-postsoak-metrics.sh 31407833d 7`）→ 私有 `STAGING_REPORT.md` → 推送 PR #97、更新描述（草稿 `PR_BODY.md`，旧版 `PR_BODY.v1.md`）→ 等 CI → 合并与 RC。
- **进度（15:05Z）**：`31407833d` 场景 44/44（含 DC-5、LR-1）、真实客户端 16/16；14:58:42Z 浸泡启动（`soak-31407833d`，约 21:40Z 连同回落与检查结束）。复核第 2 点确认：ERR-3 前四次为经末位兜底到达上游的 502（均带 `Retry-After: 2`），之后两次封顶终止应答 503 + `x-should-retry: false`；ERR-4 共 14 次均带 `Retry-After`，因属上游失败而非组级无号，按 F2 不再触发快速失败；IMG-1 260 秒 200 不变；图片、compact、续链无号计入快速失败由单测覆盖（测试环境无此分组形态）。浸泡结束后按“下一步”执行。
- **进度（19:35Z，生产只读核查转交）**：评审 agent 的生产核查（仓库外 `production/PROD_CONFLICT_REVIEW_20261016.md`）确认 240 秒预算与分档、冷却、余额状态机、F3/F4 保持。0.4.0 修复：①伪保活帧被丢弃后仍算上游存活（停滞检测取最后读到一行与最后丢帧中较晚者，覆盖 Responses 流、Chat Completions 与 Messages 转换的流式与非流式；按账号计 `relay_keepalive`；测试服务器 KA-9，fault F24 加 `output=1`）`9adce14cb`；②缺省余额关键字加 `insufficient user balance`，`billing_error` 单独不算余额（Sub2API 系中转的所有计费前置失败都用它）`8285e6926`；③本地首输出超时不冷却账号，按代码对齐 design 与两份 spec，加锁定用例 `fdb7cfdcb`；runbook 补部署前提、上线前核对、上线后观测与管理员配置项，tasks 登记 0.4.1 的 xhigh/max 静默上游不换号 `6af7870d0`；staging-validation 改为 6.5 小时与存活堆判据 `934bcf2bc`。本机全量验证全绿（CI 门禁 255）。`31407833d` 浸泡 4 小时 23 分后停止，补采回落样本后全部检查通过（存活堆斜率 0.5 MiB/h，匿名内存 90→95.3 MiB）。新候选 `934bcf2bc` 部署中，按用户此前要求重跑全部场景、客户端与 6.5 小时浸泡（改动在流读取热路径）。注意：本机 gocache 再次涨到 29G 写满磁盘，已清理。
- **进度（20:05Z）**：`934bcf2bc` 场景 45/45（含新 KA-9；F23/F24/F26 与停滞类均过）、真实客户端 16/16；19:59Z 启动 6.5 小时浸泡（`soak-934bcf2bc`，约 02:40Z 连同回落与检查结束，等待脚本 `/tmp/xy2-soak-wait.sh 934bcf2bc`）。之后：`/tmp/xy2-postsoak-metrics.sh 934bcf2bc 7` → 私有 `STAGING_REPORT.md` 定稿 → 推送 PR #97、更新描述（`PR_BODY.md` 已含生产核查节）→ 等 CI → 合并、RC、晋级 0.4.0。升级演练与 XY-image 回放沿用（自 `eaa63fd30` 无迁移、schema 与应答形态改动）。
- **进度（20:20Z，评审复核 `9adce14cb`…`934bcf2bc`）**：①②、runbook、tasks 通过，公开仓库无生产标识，`billing_error` 不算余额的取舍获同意。更正：`fdb7cfdcb` 写的“上游以 `first_output_timeout` 应答（级联网关）仍软冷却”代码中不存在——上游错误处理的 switch 只处理 BALANCE/TRANSIENT/CLIENT_ERROR，按 `SoftCooldown()` 写冷却的 `ApplyResilienceOutcome` 只由停滞调用；实际是本地与上游应答的首输出超时都不冷却账号，只有 OpenAI API Key 账号按上游 5xx 原有规则记入按模型瞬时屏蔽（本地超时不触发）。`7043f32cb` 按代码修正 design §2/§3、两份 spec 与 tasks，并加 `TestUpstreamFirstOutputTimeoutLeavesTheAccountSchedulable` 锁定（只改文档与测试，镜像不变，不重跑验证）；同时登记 0.4.1：原生 Chat Completions 路径传空存活记录，`relay_keepalive` 计数偏少。教训：文档里的行为描述须有代码与用例依据，写之前先追调用链。
- **进度（2026-10-17T00:15Z）**：最终 HEAD `a7e012c19` 本机全量验证全绿（CI 门禁 256）；推送前扫描 `688bd9d68..HEAD` 共 36 个提交无主机、域名、凭据，作者均为 noreply；origin/main 仍为分支点 `f8fec15a2`，合并无冲突。浸泡 `soak-934bcf2bc` 约 4 小时，存活堆整点小时中位数 56/58/58/59 MB（约 0.5 MiB/h）。仓库外已备：`PR_BODY.md`（浸泡结果处为 `SOAK_RESULT_PLACEHOLDER`）、`RELEASE_NOTES_v0.4.0-rc.1.md`（annotated tag 消息；git 会删掉以 `#` 开头的行，故不用标题）。发版流程参照 0.3.0：常规 merge → 合并提交打 annotated `v0.4.0-rc.1` → Release 制品双 SHA、GHCR 双架构与标签、prerelease 与稳定别名未动、`vcs.modified` → RC 镜像新装 323 迁移、0.3.0 升级与回退、成套恢复（测试服务器 `upgrade_drill.py`）与关键场景子集 → 晋级 PR（`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 的 `xy2api_version` 改为 0.4.0）→ annotated `v0.4.0` → 正式制品校验 → 发版后纯文档收尾 PR。
- **进度（02:55Z，测试服务器闭环）**：`soak-934bcf2bc` 通过（11 项检查全过；160,478 个请求；存活堆 53→55 MB、0.3 MiB/h；匿名内存 92→95 MiB；回落后槽位与预留归零）。design §14 报告 15 项达标，未达标 4 项（无号 503 次数、连续无号 20 分钟、anthropic/openai 缓存读取占比）均为合成负载所致：前两项全部来自每小时人为断余额的 `G-soak-bal`（1,365 次全在第 30–48 分钟，充值后等下一次复查），后者为故障上游固定用量，与演练报告一致，生产部署后 24/72 小时复核。候选镜像全新安装（`/tmp/xy2-staging/fresh_install.py`，323 条迁移）通过。tasks 12.9–12.11 与 staging-validation §9 已勾选。私有 `STAGING_REPORT.md` 与 `PR_BODY.md` 已定稿。RC 制品校验脚本 `/tmp/xy2-verify-release.sh <tag> <commit>` 已在 v0.3.0 上试跑全过。
- **进度（2026-10-17T04:45Z，RC）**：PR #97 头 `997861f1a` 两组 18/18 全绿（“Gateway resilience” 256 个测试）后常规 merge 为 `ca2bdd111`（03:08:38Z），main CI 与安全扫描全绿。annotated `v0.4.0-rc.1`（对象 `3042fb5b4`）→ `ca2bdd111`，Release run 38997674298 成功；制品校验全过（6 附件 198,283,776 字节双 SHA；五平台 `vcs.modified=false`、embed；GHCR index `sha256:b6ea2ef1…` 双架构与标签正确；prerelease=true；latest/0/0.3 仍为 0.3.0 的 `sha256:5560a6…`）。测试服务器 RC 镜像：全新安装 323 迁移通过；场景 45/45；升级、回退、前滚、成套恢复通过；真实客户端 14/16（C-codex-ok、C-openai-py-slow-first-cdn 失败于真实中转高峰期 502、输出后流内失败与首字过慢，日志为证，未重跑——重跑命令被用户中止）。测试服务器应用现运行 RC 镜像（`.env` 的 `APP_IMAGE` 已改）。工具教训：脚本先 `set -a; . ./.env` 再 `sed` 改 `.env` 时，docker compose 优先用已导出的旧 `APP_IMAGE`，须显式导出新值并核对运行镜像；远程命令里 `a && b &` 会把整条链放进后台，长任务用 `setsid nohup … < /dev/null &` 单独启动。
- **进度（04:45Z，晋级分支）**：本地 `release/0.4.0`（自 main `ca2bdd111`，已取消上游跟踪）：`24cfc6511` 按用户 2026-10-17 指示把用户余额提示恢复为 0.3.0 原文 `Insufficient account balance`（常量覆盖鉴权预检与计费预检，403、`INSUFFICIENT_BALANCE`、OpenAI 兼容端点 type `insufficient_balance` 不变；中间件该行与测试恢复上游原样）；随后晋级提交把 VERSION 与 `UPSTREAM_BASE.json` 改为 0.4.0 并记录 RC。待定：评审提出的“未送达不计费只覆盖 OpenAI Responses 路径”用户仍在确认，确认后再定是否进 0.4.0（若改动较大，可能需要 rc.2）；合并晋级 PR、打 `v0.4.0` 前等此项。正式版发版说明草稿在仓库外 `RELEASE_NOTES_v0.4.0.md`，全新安装检查脚本 `/tmp/xy2-staging/fresh_install.py`、RC 复跑脚本 `/tmp/xy2-staging/rc-run.sh`（测试服务器同名文件）。另：定时上游同步检测到 Sub2API v0.2.17 已发布（exit 2），需按既有同步机制另开任务。

**卡点与风险**

- 2026-10-16T11:10Z 推送前历史整理（分支从未推送）：经用户选择，134 个提交的作者与提交者由本机缺省身份改写为 `LIXIN LIU <180751112+liulixin-lex@users.noreply.github.com>`；另从全部历史中去掉测试子域名（灰云直连子域会暴露测试服务器地址），改为 `stg-direct.example.test`/`stg-cf.example.test` 占位。提交信息与本文件、方案文档中的分支内哈希均已按映射更新；映射表在仓库外私有目录。改写前备份分支 `backup/pre-author-rewrite-2424b8b38`（仍含子域名，只在本机，不推送，发版后删除）。测试服务器镜像 tag 与证据目录仍为改写前的 `968df0942`，对应最终候选 `eaa63fd30`（应用代码相同）。仓库已设本地 git 身份为同一 noreply。
- 浸泡期间不要重启或重新部署测试服务器的 `xy2api-staging` 应用；需要改代码时先在本机提交，浸泡结束后再决定是否需要新候选（新候选须重跑场景、客户端与浸泡）。
- 本机磁盘：`/tmp/xy2-toolchain/gocache` 两次涨到 30G 以上写满磁盘，长时间测试前后执行 `go clean -cache`。

**关键位置**

- 分支 `feat/gateway-resilience-20261015`（基于 origin/main `f8fec15a2`）；工具链 `source /tmp/xy2-toolchain/env.sh`（Go 1.27.2），wire 只用 `/tmp/xy2-toolchain/wiregen.sh`。
- 仓库外产物 `xy2api-artifacts/gateway-resilience-20261015/`：`research/`、`production/`、`metrics/`、`staging/`（含截图与报告草稿）、`private/HOSTS.md`、`PR_BODY.md`。
- 测试服务器脚本：`redeploy2.sh`（本机 `/tmp/xy2-staging/`）、服务器上的 `final-run.sh`、`soak.py`（按匿名内存趋势判断泄漏）。
- 安全提醒：两台主机的 SSH 密码在对话中出现过，建议改用密钥并更换；只读分析时曾短暂看到 `openai_codex_ticket_proxy_pool_v1` 的部分凭据片段与已退役的 Caddy 金丝雀 token，未写入文件，建议轮换。

### Sub2API 0.2.15 / XY2API 0.3.0 正式发布完成（2026-10-13）

- 独立工作树 `/lex/xy2api-sync-20261013`；起始main `7b1d5d8c387c461a660befa3ac936b39715b4bf0`。固定官方annotated v0.2.15（object `86a80c13dcba86f52f9ca815b5a471cecc236227`，target `f2669c8cf62555cd92389b3f55920e9e6e7c6ff2`），真实base `0363b8cdba8cec3e2ba4b2dbd49c4481143fa55d`。146个上游提交、27项冲突裁决见[同步说明](UPSTREAM_SYNC_V0215.md)。
- [同步PR #92](https://github.com/liulixin-lex/xy2api/pull/92)已按固定head `bcb62e5801996fb3b3a3411096cae22f5fa2651c`、18/18成功检查合入main `8f47f8fb2ee82d9cd85acd7d7d9353c64e41e614`。PR90、91已自动标记merged，原head分别`2c8507f8b8ff695f4a4f20689535aa9df1098455`、`5eec0830aad240e0b3d2171505fa7299e84225a6`；保留真实merge历史。
- 备份现场经授权只读确认：应用角色无法读历史外部schema，客户端18.6/数据库18.4及TLS正常。用户选择public；新显式scope保留旧全库默认，升级后须在页面选择并保存public。native TOC保留public与管理员pg_trgm，真实PG覆盖函数/类型/序列/数据、其他schema不变及SQL错误事务回滚。恢复目标须先初始化本版本schema/扩展。见[备份说明](BACKUP_RELIABILITY.md)。生产未改权限/配置/服务，未执行导出或恢复。
- controlled退役，share默认关闭，旧策略只导入未启用草稿；异步图片独立context与R1–R4心跳/对象清理/账本/存储关闭修复均已回归。tokens/s采用完整请求耗时，缓存改称基于互斥计费Token桶的读取占比；中英文与显示开关一致，见[统计口径](USAGE_METRICS.md)。
- 316条已发布SQL/checksum保持，仅追加270/271，总计318。安装空密码DSN使用统一构造；来源审计、调度差异白名单、生成后零diff、完整Go lint 0 issues及embed构建通过。前端384文件/3026项、最后文案38项、lint/typecheck/build与24张浏览器截图通过。后端本地unit62包/23651测试及子测试通过（40条件跳过）；原integration唯一迁移242引用失败修正为271，四项真实PG复验通过，最终head远端完整integration/race/真实Redis/PG及安全门禁再次全绿。最初gofmt同类报告上限3掩盖余10处import，已按全部改动Go文件一次修正，失败记录保留。
- [RC v0.3.0-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.3.0-rc.1) annotated object `e96985ec1a7a6afb8704d788763756dd7f73d707` → `8f47f8fb2ee82d9cd85acd7d7d9353c64e41e614`；[Release run38345930894](https://github.com/liulixin-lex/xy2api/actions/runs/38345930894)成功。5平台包+checksums六资产196948970字节独立双SHA校验通过；双架构image index `sha256:f5b7500bc785c679f200266d312ec695f1857904618116d072fb1af20ba8c563`，labels版本/revision/source一致；prerelease=true/draft=false且stable aliases未移动。RC制品vcs.modified=true已在隔离checkout复现：发布前tidy删除10条生成工具多余go.sum，go.mod与5个平台二进制运行依赖未变；前端资源本身不会使工作树变脏。正式晋级纳入相同checksum整理，并在make generate末尾tidy，实际重新生成后工作树为零diff；正式五平台制品已逐一验证vcs.modified=false。
- 真实发行RC镜像完成全新318迁移、0.2.6的316→318升级、成套PostgreSQL/Redis/app-data恢复再运行旧版，health/login/me/6静态资源均通过；恢复snapshot与baseline同为`50674a127239ee9e5ee307f8ecc0eb71ad2020a4ab27534a69145c9333e85378`。三个PG客户端均18.6。4个既有部署ID、状态、配置和挂载摘要全部不变。
- [正式晋级PR #93](https://github.com/liulixin-lex/xy2api/pull/93)最终head `77d43e92b924ec22311487c4b5821521d56cdd41`两组18/18检查成功，常规merge为`1e0fbdd704b9080bb730d337e2003136c683ab78`；[正式v0.3.0](https://github.com/liulixin-lex/xy2api/releases/tag/v0.3.0) annotated object `db6f317ef6af57c74aef55112ccd8fe58d220c92`指向该提交。[Release run38353591650](https://github.com/liulixin-lex/xy2api/actions/runs/38353591650)成功，draft=false/prerelease=false。
- 正式六资产196942614字节双SHA、五平台原生格式/源码revision/版本与vcs.modified=false全部通过；GHCR linux/amd64与linux/arm64、标签version/revision/source及latest/0.3/0全部一致，index `sha256:5560a67e10b463dbab3e20fca5e719b2933f93dd9f341e890bfe34a12f7f7281`。
- 正式真实镜像全新318迁移、health/login/me与静态资源通过，pg_dump/pg_restore/psql均18.6。本任务容器/网络/卷及7处演练凭据、备份和应用数据路径已删除；4个既有部署ID/状态/配置/挂载均与起始完全相同。临时dirty复现clone已删除，诊断与校验报告保留在`/lex/xy2api-release-20261013/`。生产仅只读诊断，未部署或改变权限/设置。
- 收尾按AGENTS要求在发行后补记本文件，单独走纯文档PR；不移动已发布标签。故收尾后main可比正式tag多纯记忆提交，非本记忆文件与正式tag必须零差异，发行源始终固定为1e0fbdd704。同步/晋级短期分支待该纯文档收尾合并后删除；保留原用户feature分支和全部不可变标签。
- 后续部署事项：升级后显式保存public备份范围；新目标先初始化所需扩展，production备份仍需在实际部署后验证。share默认关闭，实际影子/逐组启用与业务账单观察是部署后的流程。非阻断维护项：GoReleaser旧配置字段弃用提醒、前端依赖审计中未达high/critical阻断等级的告警；本次未借此扩展依赖升级。

## 进行中的工作

### 20261017-repo-consistency-sweep — 全仓库代码、结构与文档一致性审查与规范化（进行中）

- 2026-10-17T08:15Z 用户要求：全量审查工作区的代码、结构与文档，按最新进度规范化，补齐漏改的代码与文档，并建立机制，让后续开发时刻保持最新、不漏模块或文档。评审 agent 执行，直接提交到 `release/0.4.0`（起点 `5fdd1e589`，工作树干净）；已告知主 agent（同一工作区，正在等 `soak-c6b837dca` 浸泡）。
- 范围：管理员与部署文档、openspec 中与代码不符之处、DEV_GUIDE/README、`tools/gateway-resilience` 的 README 与 SQL、`.gitignore` 的 docs 白名单、docs 索引、AGENTS.md 变更同步规则、文档一致性检查脚本与 CI 步骤、项目记忆归档（历史原样移到归档文件）。
- 约束：不改 `backend/`、`frontend/` 源码，浸泡过的候选与正式镜像不因此产生差异；不推送、不合并、不打标签（主 agent 按既有授权随 PR #100 推送）；不访问生产与测试服务器。

### 20261015-gateway-resilience — 网关韧性 0.4.0 开发与测试服务器验证（进行中）

- 2026-10-15T11:30Z 用户授权“开始执行”：按 `openspec/changes/gateway-resilience-20261015/tasks.md` 开发，在测试服务器部署隔离环境验证；Cloudflare 测试 DNS 已由用户配置；真实测试 Key 由用户在需要时提供。PR、发版、生产部署仍按方案在全部闭环后进行。
- 本机分支 `feat/gateway-resilience-20261015`（基于 origin/main `f8fec15a2`，已取消上游跟踪，未推送）。Go 1.27.2 工具链在 `/tmp/xy2-toolchain`（`source /tmp/xy2-toolchain/env.sh`，SHA256 已校验）。
- 进度以 tasks.md 勾选为准；关键发现与验证结果追加到操作日志。
- 2026-10-15T13:30Z 已提交（本地，未推送）：核心包 `internal/resilience`（分类、预算、帧、防风暴、余额状态机，`17821b1fd`）；罚则与余额状态机接入 `HandleUpstreamError`（`9ff90e446`）；余额探测循环、`POST /admin/accounts/:id/balance/recheck`、RECOVERING 并发上限（`1279a2aea`）；会话失败改绑、回避集与两种调度器的末位兜底（`230a738a1`）。每个切片 unit 全绿（service/handler/routes/server）。
- 注意：
  - 运行 wire 生成必须用 `/tmp/xy2-toolchain/wiregen.sh`（会还原 go.mod/go.sum，避免发版校验和问题）；
  - gofmt 只对自己的文件执行，`openai_token_provider_test.go` 是仓库原有未格式化文件，不要动；
  - 调度相关上游文件的新增块要在任务 10 登记 `scheduler-diff.json`，否则 CI 的 Original scheduler diff allowlist 会失败；
  - `TestServerTimingConnectorRecordsDriverCallsWithoutRowLifetime` 在全量并发下偶发失败、单跑通过，与本改动无关。
- 2026-10-15T16:30Z 任务 4（错误语义与防风暴）完成并提交（`5d9c46707`、`efe8af053`）：无号/限流 Retry-After（2–30 秒，服务端封顶）、配置类 `Retry-After: 60` + `x-should-retry: false`、2 秒负缓存、同 Key 快速失败、上游余额隐藏、用户余额统一文案、在途预留 429（仅当余额本身够付且被其他在途占用）、缓存感知估算与 7 天 P99 输出上限、异步图片准入前移。service/handler/repository/server unit 全绿。
  - 发现并修复：选号诊断文本恒含 `profit_threshold=0`，原先按子串排除 profit 导致网关负缓存永不生效；改为只匹配非零计数。
  - 4.6 的生产 7 天只读回放验收、4.8 的真实客户端验证留到任务 12。
- 2026-10-15T18:00Z 任务 5（请求预算与重试）完成并提交（`cea0fd5de`）：中间件按路径挂预算；FailoverState 与 OpenAI 各循环计尝试、同号重试按账号级失败与上限收紧、容量降载只换一次且换主机、Gemini 原地重试 5→1 且可取消。全量 unit 与相关 `-race` 通过。偏差（WebSocket 首回合、Antigravity 智能重试）记在 tasks 5.2/5.4；尝试中途到截止时取消随任务 6 实现。
- 2026-10-15T20:00Z 任务 6（首包前保活、首输出超时、停滞、终止帧）代码完成并提交（`38e65c253`、`aaa154f03`），6.7 留待测试服务器 CDN 模拟验证。要点：预热复用 compact 心跳上下文键；`X-XY-Edge` 只信任 `SERVER_TRUSTED_PROXIES` 内的对端（部署前提，需写入 runbook）；Anthropic 首事件守卫取消不再被当作客户端断开。
  - 教训：handler 包里无 build tag 的测试文件引用了 unit tag 文件中的辅助函数，导致 `go test ./...`（Makefile test 目标）与 integration/e2e 构建失败；已把辅助文件去掉 tag。今后改测试后要分别跑 `go vet`、`-tags=unit`、`-tags=integration`、`-tags=e2e`。
- 2026-10-15T22:00Z 任务 7（客户端断开与计费）完成并提交（`28326275d`）：迁移 272–275 已一次写入（含任务 8 的两列），临时 PostgreSQL 全量迁移与仓储 SQL 验证通过；真实连接端到端测试证明未送达断开会立即取消上游。滥用保护采用“超阈值不取消上游、拿到用量按上游成本计费”的实现（偏差记在 tasks 7.5）。

- 2026-10-15 任务 8（无号兜底分组）提交（`2a98b7d9a`）：鉴权后中间件在主组无无罚账号且兜底组有时换组；按 Key 记忆（Redis，600 秒），主组稳定 120 秒后切回；保存校验自引用、环、平台与余额计费；`usage_logs.origin_group_id` 四条写入路径在临时 PostgreSQL 上验证。
- 2026-10-15 任务 9（设置、管理界面、可观测、文档）提交（`2fb125ded`）：
  - 后端：进程内分钟计数与 `GET /admin/resilience/stats`；用量读取回填 `origin_group_id` 与原分组名；可用渠道接口返回各组兜底分组与倍率；`keepalive_committed` 日志带 `edge`；管理员关注的事件为 warn（进运维系统日志），量大的保持 info。
  - 前端：网关韧性设置卡、运维页韧性面板、账号余额徽标与“已充值”、分组兜底选择器、用量兜底标记、可用渠道兜底标签。
  - 文档：`docs/GATEWAY_RESILIENCE.md`（已加入 `.gitignore` 白名单）；runbook 增加 `SERVER_TRUSTED_PROXIES` 前提；方案文件中的宿主机路径与端口改为代称。
  - 验证：前端 lint、vue-tsc、全量 vitest（387 文件 / 3041 用例）、构建通过；后端 gofmt、四种构建标签 vet、全量 unit 通过。浏览器检查（9.6）随任务 12。
  - 注意：`/tmp/xy2-toolchain/gocache` 曾涨到 36G 把磁盘写满，导致链接失败；已 `go clean -cache`。长时间开发时留意磁盘余量。
  - 本机 pnpm 版本与 lockfile 的 `pnpm.overrides` 不兼容，`pnpm run` 会拒绝执行；直接用 `node_modules/.bin/{vitest,vue-tsc,eslint,vite}`，不要运行 `pnpm install`。

- 2026-10-15T17:30Z 任务 0、10 完成，任务 11、12 进行中：
  - 任务 10：7 个具名补丁、22 条字面量、调度白名单 `gateway-resilience` 块；`sync.py audit`、`scheduler-diff` 通过；版本 `0.4.0-rc.1`（RC 惯例）。
  - 任务 0：`tools/gateway-resilience/`（基线 SQL、首字校准、Caddy 切断与会话连败脚本、fault-upstream、cdn-sim、测试环境 compose/夹具/场景/真实客户端/升级演练脚本）。生产只读基线（48h）：无号 503 共 9710 次、最长整组无号 85 分钟、同 Key 同账号连败 P99=11；证据在仓库外 `production/`。
  - 首输出分档校准：生产 0.3.0 实际未启用首输出超时；原 60/120/120/180 秒会误切约 1.3%（OpenAI 默认档）成功请求，缺省改为 180/180/200/220 秒。
  - 测试服务器隔离环境已部署（独立 Compose 项目、独立网络与端口段、独立 Caddy 片段，测试域名 stg-api 灰云、stg-cf 橙云已生效）。用户 2026-10-15 授权：仅在该隔离测试实例上代为确认管理员合规承诺；提供 3 个 GPT 上游测试 Key（只存测试服务器 0600 文件，不入 Git/记忆）；要求真实客户端测试用 gpt-6.1-sol 或 gpt-6-astra。
  - 自动化场景第 4 轮 22/22 通过。测试中发现并修复 5 个后端问题：耗尽 502 透传未封顶的上游 Retry-After；换号耗尽的 5xx 无重试提示；慢响应头到达时按“剩余预算”计算首输出上限导致立即误切；单账号分组被按单次档位切断（唯一可用账号现视为最后候选）；经 Cloudflare 时源站 502/504 被替换为纯文本页（经 CDN 边缘的请求改答 503 并带 `X-XY-Upstream-Status`）。另外 Caddy API 错误页统一 503。
  - 真实 Cloudflare：150 秒慢首包请求经心跳穿过 CF 边缘超时，直连不发早期心跳，Anthropic ping 心跳正常；CF-3 待新候选复测。
  - XY-image 0.0.17（任务 11）在独立副本分支开发，X1–X7 与实验室模拟上游完成，962 个单测通过；已在 XY-image 交接文件第 4 节登记并留言；`XY2API_VERIFIED_VERSIONS` 加 0.4.0 需等 0.4.0 正式镜像录制后进行。
  - 注意：客户端（Codex、Claude Code、OpenCode、SDK）装在测试目录、以隔离 HOME 运行，不碰测试机全局配置；测试机有其他服务，场景脚本只用自己的分组与端口。
- 2026-10-15T18:10Z 真实客户端矩阵第 1 轮 10/15 通过，5 项失败的原因与处理（`025706c22`、`b118624e3`，本地未推送）：
  - Codex 全失败回合 30 次请求、871 秒；流内失败重试 6 次。读 codex-rs 源码确认：带 `Retry-After` 的 5xx 传输层每轮 5 次、回合层 5 轮，不封顶；`server_is_overloaded` 与 429 只有服务端给延迟才重试。新增失败回合封顶（design §9.7，`errors.turn_cap`）：同会话连续第 5 次耗尽失败改为终止应答。
  - 真实中转站在 `response.created` 前发 `SSE-Keep-Alive` 空 delta 帧：openai-python 报错，网关还会把它当首输出、放弃换号与首输出超时。读取上游 SSE 时丢弃（design §7.8）。
  - 测试脚本问题：Claude Code 缺 `ANTHROPIC_BASE_URL/API_KEY`；Claude 模型的分发要设按族映射（`default_mapped_model` 对 Claude 不生效）。
  - 顺带把 design 与 spec 的首输出分档改为校准后的 180/180/200/220 秒。
  - 下一步：复测这 5 项，再跑全量场景（含 CF-3、CF-5、DC-2..4、IMG-1、BAL-2）与完整客户端矩阵。
- 2026-10-15T18:35Z 复测与新一轮场景（本地提交至 `f5f72cb49`，未推送）：
  - 5 项客户端复测全过；Codex 全失败回合 5 次请求、92.5 秒（原 30 次、871 秒）。
  - 新修复：封顶终止应答不进入同 Key 快速失败（否则重放会补回 Retry-After）；施加冷却时刷新分组账号快照（原 2 秒快照让紧随失败的请求看不到冷却，FB-2 发现）。
  - 测试工具发现：fault-upstream 未回应网关的 Responses 工具探测，23 个故障账号被静默改走 Chat Completions，现已改为像真实中转站一样回应；新增 F24（中转站伪保活帧）、KA-7、FB-2、ERR-3/4、浸泡脚本 `soak.py`；RES-1、DC-2 改为与历史运行隔离（P99 输出封顶把预留估算从约 0.08 降到约 0.01 美元）。
  - 场景第 7 轮 33/35（失败的 RES-1、DC-2 为用例隔离问题，已修，待复跑）；真实 Cloudflare CF-1..5 全过；后端全量 unit、四种 tag vet 通过。
  - 覆盖矩阵 `openspec/.../evidence/coverage-matrix.md`：70 个场景对应单测与测试服务器用例。
  - XY-image 副本加 `XY2API_LAB_SKIP_PULL`（`d7821fe`，本地），用于实验室接入本地候选镜像。
  - 待办：复跑 RES-1、DC-2；完整客户端矩阵；升级回退演练；XY-image 实验室（候选镜像，端口 18220/18221）与 0.0.16 兼容回放；24 小时浸泡；浏览器检查；改写含宿主机路径的历史提交后推送。
- 2026-10-15T19:05Z 候选 `9b338b1c7`（改写前为 `ed3e826f7`，代码树相同）验证结果：
  - 场景 35/35（第 7 轮 33 项 + 第 8 轮 RES-1、DC-2）；真实客户端矩阵 15/15。
  - 升级演练（生产规模合成数据）：升级启动 1.2 秒、usage_logs 最长阻塞 0.11 秒；回退 0.3.0、前滚、全库备份恢复（行数一致）全部通过；演练库已删除。
  - XY-image：候选版本录制 59 个场景；0.0.17 与 0.0.16 回放均 181/181；新增可选在线对话测试（`6049848`，本地）4/4；Caddy 错误标记（直连与经 Cloudflare）均为 503 + `X-XY-Proxy-Error`，XY-image 映射为 `xy2api_unavailable`、未扣费、可重试。
  - 24 小时浸泡已于 18:58Z 在测试服务器启动（`soak.py`，证据目录 `soak-ed3e826f7`），预计 2026-10-16T19:10Z 结束并自动检查。
  - 分支历史已用 `git filter-branch` 改写：两份方案文档早期版本中的宿主机路径与生产端口替换为代称，39 个提交均无残留，最终树不变；改写前的本地备份分支 `backup/gateway-resilience-pre-rewrite`（不推送，验证后可删）。origin/main 仍为 `f8fec15a2`。
- 2026-10-15T20:40Z 剩余开发与验证（本地提交至 `529fa7442`，未推送）：
  - 新增开发：1.2–1.5（ops 记录带 `resilience_category`、按状态码归类的 4xx 采样）、2.6（余额告警指标与迁移 276 预置规则）、3.3（份额调度按罚则档位与原因码）、3.5（`GET /admin/resilience/groups/:id/explain` 只读 Explain，运维页入口；份额 Explain 附同一视图）、12.10 工具（`metrics.sql`、`metrics_report.py`，日志脚本支持 `--json` 与控制台格式）。
  - 新增测试：调度矩阵 24 例（原版/份额 × 单/双号 × 软罚/硬罚/连败 × 有无兜底）；韧性 Redis/PostgreSQL 集成 6 例（测试服务器上用 golang 容器 + testcontainers 跑，真实 PG 18、Redis 8.4，全过）；可用性兜底与 Claude Code 兜底互不影响。
  - `-race` 发现并修复一处测试数据竞争（图片 JSON 保活测试在心跳协程写入时读 recorder）。
  - 完整集成测试仅 `TestPgDumperRealPostgresBackupAndAtomicRestore` 失败：容器无 `pg_restore`，v0.3.0 同样失败，属环境原因（CI 环境另行验证）。
  - 选号基准与 0.3.0 持平（份额约 476→481 µs，原版约 309→311 µs）。前端 lint、vue-tsc、全量 vitest（389 文件 / 3045 用例）、构建通过。
  - 生产 0.3.0 指标基线已只读采集（数据库 48 小时、Caddy 约 2.7 小时、应用日志约 19 小时），文件在仓库外 `metrics/`。注意：用户调整 CDN 超时后，生产 CF 切断率已降到 0.03%、ESA 为 0；无号 503 为 9786 次/48 小时，最长整组无号 85 分钟；生产日志只有少数请求带会话哈希，基线的“连败 P99=9”是同 Key 口径。
  - 测试环境当前浸泡（旧候选）中的无号 503 全部来自 `G-soak-bal`：每小时刻意断余额 10 分钟、未配兜底，恢复需等下一次复检（约 18 分钟），属设计行为。
- 2026-10-15T22:00Z 最终候选复验（本地提交至 `dc98efc57`，未推送；应用镜像 `fe6d43c50`，其后只改测试工具与文档）：
  - 场景 37/37（新增 KA-8 覆盖 300 秒 CDN 档、CONT-1 覆盖续链被拒）；真实客户端 16/16（新增 anthropic-python 已提交流里的错误事件抛 `APIStatusError`）；XY-image 实验室 0.0.17 与 0.0.16 回放 181/181、在线对话 4/4；升级演练（迁移至 276）通过；浏览器 28 次加载与键盘操作通过。
  - 真实中转复测发现 0.3.0 已有缺陷并修复（`fe6d43c50`，design §9.8）：Claude 请求分发到 GPT 中转时，多账号中转站以笼统 400 拒绝 `previous_response_id`，会话从第二轮起每轮失败；现在去掉续链整轮重放一次并关闭该会话续链。上游同步登记了两条 required_literals。
  - 旧候选 `ed3e826f7` 的浸泡运行 81 分钟后停止，沉淀后检查全部通过。
  - 私有产物：`staging/STAGING_REPORT.md`（草稿）、`PR_BODY.md`（PR 描述草稿，可公开）、`metrics/`。
- 2026-10-16T02:30Z 浸泡中发现内存缓慢上升（2 小时 95→103 MiB）：会话失败/回避标记与 0.3.0 原有的续链绑定表都只在会话再次出现时清理，不再回来的会话常驻内存。修复为每分钟清扫标记、每 1024 次写入清扫绑定表（`4e8180540`）。`fe6d43c50` 的 4 小时部分浸泡沉淀后检查全过。新候选场景 37/37、客户端 16/16，24 小时浸泡重新开始（`soak-d0b7e7309`）；XY-image 回放、升级演练、浏览器检查沿用前一候选（改动只涉及进程内清扫）。
- 2026-10-16T06:10Z 用户要求：全方位审查算法、策略与调度是否遗漏或臃肿，规范化代码，使其可落地上生产。进行中：先做正确性审查与精简审查（分支相对 origin/main），再做设计层面的取舍复核；有代码改动则重建候选并重新浸泡。当前浸泡 `soak-d0b7e7309` 继续运行，作为现有代码的泄漏验证。
- 2026-10-16T07:40Z 审查进展（本地提交至 `fd5eb7bf4` 之后，未推送）：
  - 已修复约 30 项审查发现（每项一个带测试的提交），重点：末位兜底只试排序第一的账号、忙时不用空闲的次优账号（两种调度器都改为依次取槽位）；余额耗尽的硬档在排除期结束后仍为硬档；网关自己的首事件 504 被当作中转瞬时错误；会话失败在部分换号循环里漏报、在另一处重复上报（统一到 `resilienceAttemptFailed`）；死代码（`resilience/frames.go` 整个未被生产调用，已删）。
  - 性能：主组健康且本实例未转出 Key 时兜底判断不访问 Redis；余额状态直接读 map 不做 JSON 往返；中转保活过滤从每行复制改为别名（500 帧 1504→7 次分配）。不做：失败分类结果缓存（只在失败路径，收益小）。
  - 规范：本机装了 CI 同版 golangci-lint v2.14.0（`/tmp/xy2-toolchain/bin`），此前分支有 6 个 lint 问题（CI 会失败），已清零；`scheduler-diff.json` 漏登记 Gemini 预算守卫的改动，已补。今后每次提交前跑 golangci-lint 与 `sync.py scheduler-diff`。
  - 文档：design 补单实例前提下的进程内索引清单、探测锁 90 秒、OpenAI 冷却口径、末位兜底取槽规则；`docs/GATEWAY_RESILIENCE.md` 已知限制同步。
  - 注意：`/tmp/xy2-toolchain/gocache` 再次涨到 34G 写满磁盘（vitest 报 ENOSPC），已清理；跑 `-race` 前后留意余量。
  - 下一步：本机全量验证 → 重建候选、测试服务器场景/客户端/浸泡重跑（`soak-d0b7e7309` 将停止）。
- 2026-10-16T19:30Z 用户转交评审 agent 的生产只读核查（仓库外 `production/PROD_CONFLICT_REVIEW_20261016.md`，以最新版为准）：240 秒预算与分档、冷却、余额状态机、F3/F4 均保持。0.4.0（RC 前，先写失败用例）：①伪保活帧被丢弃后不刷新停滞计时（P1 回归，三个调用点，加按账号计数）；②缺省余额关键字加 `insufficient user balance`（`billing_error` 自定）；③design 的 FIRST_OUTPUT_TIMEOUT 软冷却与代码不一致（P2，倾向改文档）。0.4.1 登记：xhigh/max 档位遇完全静默上游时停滞先于档位触发、OpenAI 停滞不换号。另补 runbook/发版说明：部署前提、上线前核对、上线后观测、管理员配置项。`31407833d` 的浸泡继续跑完，作为其余代码的证据；新候选按门禁定验证范围。
- 2026-10-17T05:00Z 用户经评审 agent 转交 0.4.0 晋级前清单（以此为准）：A1 余额提示恢复原文（已完成 `24cfc6511`）；A2 `openai_ttft_mode=semantic` 时被丢弃的伪保活帧按 0.3.0 口径计入首字（取第一次丢帧与第一个真实事件中较早者，自本次尝试起算；visible 不变；时序、换号、停滞、送达判断不变；覆盖 Responses、CC/Messages 转换流式与非流式、passthrough，`scanCCStream` 与 `streamRawChatCompletions` 一并传入存活记录）；A3 “未送达不计费”只在 OpenAI Responses 流式按语义输出判定，其余路径与 0.3.0 相同，0.4.0 只改文档，0.4.1 实现登记；A4 runbook 与发版说明补余额告警、观测项与首字/耗时口径；A5 说明 RC 上 C-openai-py-slow-first-cdn 的失败检查并确认 240 秒末位切断按设计生效。每项先写失败用例；A1/A2 形成新候选，验证按既有门禁；不访问生产，公开仓库不写生产账号号码。
  - 2026-10-17T05:43Z 进展：A2 `dd5ae0c0f`（Responses、passthrough、CC/Messages 流式转换、原生 CC 直转在 semantic 口径下首字取首个丢帧与首个真实事件中较早者；非流式转换两版都不记首字、两条 chat fallback 一直不计帧，均不改；原生 CC 与两条 chat fallback 传入存活记录，丢帧按账号计数，原 0.4.1 项提前完成）。A5 `8798e40d2`：失败检查为“client got an answer”，240 秒切断生效（两次 `first_output_timeout`：180 秒故障号、真实中转剩余 59.98 秒），终止帧缺 `response.created` 致 openai-python 报协议错误；已修两处终止帧，KA-4/KA-8 加顺序检查，新增 `C-openai-py-budget-cdn`。A3/A4 文档 `116be5d6d`。下一步：本机全量验证 → 新候选在测试服务器跑场景、客户端与浸泡 → 推送 PR #100、更新描述。
- 2026-10-17T06:10Z 用户经评审 agent 决定把“上游余额处理精简”并入 0.4.0（以评审消息为准）：余额识别保留（错误码、关键字含 `insufficient user balance`、`balance_keywords_extra`、`resilienceBalanceEligible` 范围、余额类 403 不走计数置错）；处置改为 `SetTempUnschedulable` 暂停 `balance.pause_seconds`（缺省 300，30–3600，原因前缀 `balance:`，暂停期间硬罚、不作末位兜底，到期由真实请求重试，固定时长）；HTTP 错误、流内 BALANCE、Grok 余额字样 429、402 全走这一条；取消 SUSPECT；“恢复状态”按钮立即放回；`NoAccountRetryAfter` 改看暂停结束时间；`balance.enabled=false` 回到 0.3.0。删除三态与 `extra.resilience_balance` 读写、复查循环与三种探测、`RecheckBalance` 与“已充值”按钮、RECOVERING 并发上限（含 `concurrency_service.go` 钩子与 required_literals）、两个告警指标与“长期未恢复”通知、账号列表徽标、运维余额分布、Explain 余额状态、设置里多余字段；份额调度与选号改看 `balance:` 暂停。新增迁移 277 删除 276 的两条规则（名称与 `metric_type` 同时匹配）。原因：现实现余额告警恒为 0（`ListOpsAccountsForStats` 不选 `extra`），探测不确定时暂停无限顺延。先写失败用例；完成后出新候选，整套验证（全部门禁、45 场景含改写的 BAL-*、17 客户端、升级演练、6.5 小时浸泡）只跑一次。当前候选 `fb0c2267c` 的 final-run 已停（场景 44/45：DC-2 因 31 次请求跨整点、按小时计数器重置而失败，非回归，场景将改为临近整点先等过整点；客户端跑到第 4 个时停止）；PR #100 已推送 `fb0c2267c`，保持草稿。
  - 2026-10-17T06:37Z 进展：余额精简 `cc5fc3208`（代码、前端、迁移 277、用例；删除 2296 行、新增 764 行）、文档 `f66c5562f`、DC-2 整点保护 `e039c2b6f`。先写的失败用例（暂停约 660 秒而非 300 秒、429 在成功后为 SUSPECT、Retry-After 为 0、恢复状态后记录仍按硬罚、Grok 同理、设置多余字段、迁移文件缺失）已全部转绿；后端 unit 全量、lint 0、四种 vet、前端相关 spec 与 vue-tsc 通过。`concurrency_service.go`、`ops_account_availability.go`、`ops_realtime_models.go`、`ops_alert_evaluator_service(_test).go`、`ops_alerts_handler.go`、前端 `ops.ts`/`OpsAlertRulesCard.vue` 回到上游原样（仅模块路径与导入顺序）。迁移 277 的数据库用例为 integration 标签，本机无 Docker 跳过，CI 的 `make test-integration` 会跑；测试服务器升级演练再实测。下一步：本机全量验证 → 新候选部署 → 场景 45、客户端 17、升级演练、6.5 小时浸泡（整套只跑一次）→ 推送 PR #100。
  - 2026-10-17T07:25Z 进展：本机全量验证（`526efd035`）全绿，CI 门禁 246 个韧性测试。测试服务器候选 `c6b837dca`：迁移 277 已执行、RC 形状设置文档照常加载；场景 45/45（BAL-1/BAL-2/PE-1 新行为、KA-4/KA-8 事件顺序、DC-2）；真实客户端 17/17（`C-openai-py-budget-cdn` 读到 response.failed，`C-openai-py-slow-first-cdn` 216.9 秒通过）；升级演练通过（277 只删种下的规则，管理员同名改指标规则保留；回退、前滚、备份恢复正常）。07:24Z 起 6.5 小时浸泡 `soak-c6b837dca`，约 14:05Z 结束（等待脚本 `/tmp/xy2-soak-wait.sh c6b837dca`）。已推送 `c6b837dca` 到 PR #100（草稿）并更新描述，CI 进行中。浸泡通过后：指标报告 → PR 转为可合并、CI 全绿后常规 merge → annotated `v0.4.0` → 制品核验与正式镜像全新安装。
- 2026-10-16T07:30Z 用户追加：同时规范化 0.3.0 已发布的“份额调度”（`internal/sharesched`、`service/share_scheduling_*`、仓储、管理接口与前端），达到同样的上线标准；改动并入本分支与 0.4.0。浸泡中内存每小时约 +5 MiB，已停 `soak-d0b7e7309`，测试服务器跑带 pprof 的诊断镜像（pprof 文件只在服务器源码副本，不入库）定位。

## 操作陷阱

- 处理 Sub2API 同步只以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。`DEV_GUIDE.md` 的“Git 操作”曾写着直接 merge/rebase 上游 main，2026-10-17 已改为指向同步规范，这两条命令也登记为 `tools/docs_consistency.json` 的退役写法。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。
- GoReleaser 的稳定别名（`latest`、主版本、次版本）现用 `skip_push: auto`，prerelease 不再改写它们（v0.3.0-rc.1、v0.4.0-rc.1 发布后均核实未动）。早期 v0.0.7 RC 曾短暂改写别名，正式发布后已恢复。
- GitHub Actions 在本次 RC/正式 Release 中提示 `docker/login-action@v3`、`docker/setup-buildx-action@v3`、`docker/setup-qemu-action@v3` 的 Node.js 20 runtime 已弃用，Runner 强制切到 Node.js 24 后仍成功；这是后续 Action 版本维护项，不是本次发布阻塞。

## 项目速览

### 产品与架构

- XY2API 是 Sub2API 的二开 AI API 网关，负责多上游账号调度、API Key、配额/计费、协议转发、管理后台等。
- 后端位于 `backend/`：Go（版本以 `backend/go.mod` 为准，当前 1.27.2）、Gin、Ent、Wire，依赖 PostgreSQL 与 Redis；入口为 `backend/cmd/server/main.go`，前端构建产物嵌入 `backend/internal/web/dist/`。XY2API 自有的规则内核在 `backend/internal/resilience`（网关韧性）与 `backend/internal/sharesched`（份额调度）。
- 前端位于 `frontend/`：Vue 3 + TypeScript + Vite + Pinia + Vue Router，包管理器固定为 pnpm。
- `deploy/` 保存 Compose、镜像与部署脚本；`openspec/` 保存重要功能规格和验证证据；`skills/` 保存面向 Agent 的项目技能；`tools/` 保存同步、韧性、份额调度与文档一致性工具。全部文档及状态见 [`README.md`](README.md)。
- 修改 Ent schema 后需重新生成 `backend/ent/`；修改 Wire provider 后需重新生成 `backend/cmd/server/wire_gen.go`。生成产物必须和源定义一起审查、提交。

### 常用验证入口

```bash
# 仓库同步工具与升级预检单测
python3 -m unittest discover -s tools/tests -p 'test_*.py' -v

# 只应在干净工作树执行
python3 tools/upstream-sync/sync.py audit

# 上游同步环境体检；准备新同步前要求 strict 通过
python3 skills/xy2api-upstream-sync/scripts/doctor.py --strict

# 原版调度差异白名单与文档一致性（改动调度文件、文档或版本号后必跑）
python3 tools/upstream-sync/sync.py scheduler-diff
python3 tools/check_docs_consistency.py

# 后端（Go 版本见 backend/go.mod）
cd backend && go test -tags=unit ./...
cd backend && go test -tags=integration ./...
cd backend && go vet ./... && go vet -tags=unit ./... && go vet -tags=integration ./... && go vet -tags=e2e ./...
cd backend && python3 ../tools/gateway-resilience/ci_gate.py   # 韧性必测门禁，-race、零跳过
cd backend && golangci-lint run ./...                          # v2.14.0

# 前端（需要 pnpm）
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

根 `Makefile` 提供 `build`、`test`、`test-backend`、`test-frontend`；后端 `Makefile` 提供 `build`、`generate`、`test-unit`、`test-integration` 和 `test-e2e`。验证前应按变更范围选择，不要机械运行无关长测试。

## 上游同步机制摘要

### 事实来源

| 文件 | 职责 |
| --- | --- |
| `skills/xy2api-upstream-sync/SKILL.md` | 阶段选择、授权边界、停止条件和交付证据 |
| `skills/xy2api-upstream-sync/references/runbook.md` | prepare、人工裁决、PR、RC、正式版和收尾执行手册 |
| `docs/UPSTREAM_SYNC.md` | 项目正式同步规范与兼容不变量 |
| `tools/upstream-sync/sync.py` | `report`、`prepare`、`normalize`、`audit` 实现 |
| `tools/upstream-sync/policy.json` | 所有权分类、兼容字面量、禁止回流文件、二开补丁与绑定测试 |
| `UPSTREAM_BASE.json` | 当前 main 已审计上游基线与不可变 provenance |
| `docs/upstream-sync/<tag>.json` | 每次同步的三方文件矩阵和影响报告 |
| `.github/workflows/upstream-sync.yml` | 选择稳定 release、固定标签、准备分支与 Draft PR |
| `.github/workflows/release.yml` | 校验产品版本标签并构建/发布制品，不负责改版本文件 |

### 标准流程

1. 只选择正式、非 draft、非 prerelease 的 semver annotated tag；保存官方 tag object，并固定到 `refs/tags/sub2api/<tag>`。
2. 冻结目标 commit、当前 fork 起点和真实 merge base；禁止跟随移动的 `upstream/main`。
3. `report` 计算上游与二开两侧 commit、变更文件、重叠矩阵、所有权和 migration/API/config/dependency/generated 影响。
4. `prepare` 要求干净工作树，执行 `git merge --no-ff --no-commit`。清单外冲突立即阻断；清单内冲突先保留 XY2API 侧并登记 `pending`，不能把它当作已解决。
5. 官方 merge 单独提交；仅对本次上游变更涉及的 `.go`/`.proto` 做 module path 字节替换并单独提交。禁止全仓 `sub2api -> xy2api` 替换。
6. 人工逐项比较 base/upstream/XY2API，按 `UPSTREAM`、`XY_OWNED`、`COMPAT_INVARIANT`、`MANUAL_MERGE`、`GENERATED` 五类裁决；更新 provenance、兼容版本、migration manifest、二开补丁和生成代码。
7. `audit` 检查干净工作树、冲突标记、已发布 migration checksum、policy 结构、具名补丁测试、必需/禁止契约、module path、标签/祖先关系、双版本一致性、pending 状态和未分类差异。
8. 自动化最多创建一个 Draft PR，从不自动 merge 或 release。同步合入后先做 RC，隔离验证制品/升级/回滚，再用独立正式版 PR 晋级产品版本。

### 不可破坏的兼容边界

- 已发布 migration 及 `backend/migrations/checksums.json`：既有 SQL/hash 不得改写，只能追加新编号与 checksum。
- 插件 v1 包名、Magic Cookie、`requires.sub2api` 等第三方兼容字段继续使用 Sub2API 语义。
- 已发布 HTTP 路由、Redis/浏览器 key、稳定 UUID/KDF 输入、WebSocket 子协议、旧配置目录、旧二进制名和 Grok 兼容 header 不因品牌改造重命名。
- `VERSION` 是 XY2API 产品版本；`SUB2API_COMPAT_VERSION` 是已完成审计的 Sub2API 基线，二者独立演进。
- 品牌、仓库、镜像、安装/发布策略和 sponsor-free 政策归 XY2API；同步功能时不能让 CLA、广告或赞助资源回流。
- 冲突裁决、二开兼容补丁、生成产物、provenance、RC/正式版本应保持可追踪的分层提交。

## 记忆维护规范

### 顶部状态的结构

进行中的大任务在“当前交接状态”用固定小节：**背景**、**目标**（含用户追加要求及时间）、**要求与约束**、**已完成**、**未完成**（按执行顺序编号）、**卡点与风险**、**关键位置**。标题写明状态更新时间。每完成一项就把它从“未完成”移到“已完成”，让接手的 Agent 只读这一块就能知道现在做到哪、下一步做什么；细节与证据放在“进行中的工作”和操作日志。

### 更新时机

- 开始任务：核实 Git 状态并在“进行中的工作”登记。
- 执行中：只在范围、关键事实、决策、文件集合、验证结果或卡点变化时更新。
- 结束前：更新顶部当前状态；将自己的进行中条目删除、标为阻塞或按“归档”移走；在日志末尾追加记录。
- 外部状态可能变化时（PR、CI、Release、上游 tag、价格/依赖等），记录“核实时间”，后续 Agent 必须重新查询。

### 归档

- 任务结束（合并、发布或放弃）后，由执行者把它在“当前交接状态”的小节与“进行中的工作”的条目原样移到 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md) 对应章节末尾；本文件“当前交接状态”只留几行结果摘要（版本、PR、结论、遗留事项与去向）。
- 操作日志在本文件只保留最近一两个版本周期；更早的条目按时间顺序原样追加到归档文件“操作日志”末尾。
- 归档只追加、不改写；发现旧记录有误，在原条目后追加更正。
- 本文件上限 192 KiB，操作日志条目一律用 `###` 标题；`tools/check_docs_consistency.py` 检查这两点（规则见 `tools/docs_consistency.json` 的 `memory`）。

### 日志模板

```markdown
### <UTC 时间> — <任务 ID> — <状态>

- 请求/目标：
- 开始状态：分支、HEAD、工作树，以及相关远端标识。
- 完成操作：关键检查、决策和修改，不逐条粘贴命令流水账。
- 修改文件：
- 验证：命令与结果；未运行项及原因。
- 卡点/风险：无则写“无”。
- 下一步：无则写“无”。
```

## 操作日志

<!-- 按时间从旧到新追加。历史记录只允许追加更正，不应静默改写。 -->

### 2026-10-13 — 20261013-sub2api-v0.2.15-full-release — 本地集成与专项修复

- 请求范围含标准上游同步、备份修复、开放PR整合、指标算法/文案及发版；用户追加并行开发和生产只读分析。执行使用独立clone并保留原main未提交记忆。
- 通过doctor严格预检后prepare首次被17个新冲突阻断，核对后独立登记policy并重新执行；保留原失败报告。官方merge、归一、人工裁决、两PR merge、生成代码、备份/指标/图像修复分别提交，旧迁移不改写。
- 备份：生产版本/权限只读确认后选择public；普通表范围演练曾通过，但扩展索引演练实际失败（DROP SCHEMA遇pg_trgm依赖），因此没有放行；最终采用原生TOC精确省略namespace元条目、保留业务对象/数据，新恢复矩阵通过。旧psql SQL错误会回滚但退出0，新参数消除误报成功；没有声称旧版部分提交。
- 验证失败和资源中断分别保留：初轮后端旧charged fixture、前端IQ筛选旧索引，以及多并发编译造成内存压力。改为单个重型Go进程、低并发前端，未改断言来掩盖业务问题。最终全量及远端/制品工作仍在进行，以上条目不是发版完成记录。

### 2026-10-13 — 20261013-sub2api-v0.2.15-full-release — 草稿 PR 与首轮最终门禁

- 创建独立RC版本提交并推送同步分支/不可变sub2api/v0.2.15标签，开草稿PR #92，单独提交真实sync_pr来源；main和已发布标签保持原状态。
- 前端最终3026项完整测试、38项文案补验及构建通过；后端最终unit62包/23651测试及子测试通过（40项依赖条件跳过），完整integration与固定head远端门禁进行中。
- CI静态检查发现3个上游归一化文件import未gofmt，纯格式修正；原始失败日志保留。已通过前后端安全、前端、Windows插件、shell、审计与发布辅助上下文，仍须新head全部通过。
- RC/正式发布仍未执行，本记录不构成发行成功结论。生产只读故障定位完成，备份验证临时容器/进程/归档已独立核查无残留。

### 2026-10-13 — 20261013-sub2api-v0.2.15-full-release — 集成合并、RC 发布与制品演练

- PR92最终bcb62e580两组18项检查成功，精确head审计comment后常规merge成8f47f8fb2；PR90/91自动merged且历史保持。未绕过保护或force push。
- RC annotated e96985ec1→8f47f8fb2，Release run38345930894成功；六资产双SHA、五平台二进制、GHCR双架构labels与stable aliases保持通过。镜像index f5b7500bc785c679f200266d312ec695f1857904618116d072fb1af20ba8c563。
- 真实发行镜像新装、316→318升级、完整PG/Redis/app-data备份恢复到0.2.6通过；3个PG客户端18.6，4个既有部署完全不变。原本地integration242引用失败及gofmt截断诊断已闭环，但原失败日志仍保留。
- 正式晋级从main独立创建release/0.3.0，VERSION与provenance产品版本提交独立于记忆；业务/前端/SQL与已验RC保持。正式外部发布门禁尚待执行，不能视作已完成。生产没有写入或部署。

### 2026-10-13 — 20261013-sub2api-v0.2.15-full-release — 发布构建可追溯性修正

- RC五个平台的vcs.modified=true不是前端产物导致；精确RC隔离clone下载实际前端artifact后status仍空，随后与发布流程相同的Go1.27.2 tidy仅删除5个未使用工具模块的10条go.sum记录，go.mod完全不变，5个模块均不在已发RC二进制dep中。
- 正式PR93追加该checksum整理，并让make generate在Ent/Wire后执行tidy，避免工具生成再次引入多余sum。完整make generate与git diff --exit-code实跑通过，未改运行时代码、SQL、前端或依赖版本；backend/Makefile经审查登记manual_merge及resolved来源说明。
- PR93原head ab0af029两组18项检查已全部通过；追加修正后须以新精确head重新通过完整CI/安全，不复用旧head为最终通过。正式资产核验提高为require-clean-vcs。RC标签保持不动，升级/回退证据继续适用于相同业务代码和运行依赖。

### 2026-10-13 — 20261013-sub2api-v0.2.15-full-release — 正式发行与清理完成

- 正式PR93最终77d43e92两组18/18检查全绿，以merge commit1e0fbdd704合入受保护main，annotated v0.3.0 objectdb6f317e指向该源码；未force push、未改既有tag或分支保护。Release38353591650成功，正式flags正确。
- 独立六资产双SHA、五平台buildinfo（均vcs.modified=false）、双架构image index5560a67e10b463dbab3e20fca5e719b2933f93dd9f341e890bfe34a12f7f7281及version/revision/source/stable aliases全部通过。正式镜像新装318迁移与三个PG18.6客户端通过；RC已完成316→318升级与PG/Redis/app-data成套恢复。
- 清理仅本任务prefix+label容器/网络/卷和指定敏感fixture路径；保留hash/JSON/日志，4个既有部署完全不变。临时VCS复现clone已删；生产没有写入或部署。
- 按AGENTS在正式发行后补齐记忆并清理本任务进行中条目，本收尾仅改PROJECT_MEMORY，使用独立PR和完整保护检查。不可变发行tag保持1e0fbdd704；收尾main的业务树与正式tag完全一致，此纯文档领先记录是对runbook收尾“main等于tag”的明确例外，避免为补记发行结果移动标签。
- 没有未完成业务修复或发行验收；后续仅为用户实际部署时的public设置、生产恢复验证与可选share影子/灰度，未在此次只读生产授权外执行。证据及历史失败/中断均保留，未保证无法证明的零风险。

### 2026-10-15T03:20Z — `20261015-prod-ux-analysis` — 完成

- 请求/目标：用户授权只读 SSH 生产，分析近 48 小时全部请求、错误、用户断连、重试/回退与账号调度，实时抓取最新请求，规划除上游自身原因以外的体验优化方向。
- 开始状态：本机原在 `feat/share-scheduling-20261010`（已随 v0.3.0 合入）；fetch 后基于 origin/main `f8fec15a2` 新建 `analysis/prod-ux-20261015`。生产运行 `xy2api:0.3.0`（他人于 00:42Z 部署、00:46Z 切换流量）。
- 完成操作：
  - 只读 psql（`default_transaction_read_only=on`）统计 `usage_logs`、`ops_error_logs`、`ops_system_logs`、`ops_system_metrics`、`settings`、`accounts`、`groups`、`audit_logs`；
  - 下载应用日志（约 2 小时）与 Caddy 轮转日志，实时抓取约 25 分钟；
  - 对照 v0.3.0 源码核实重试、换号、冷却、keepalive 与错误映射；
  - 用“IP、路径、开始时间”把 CDN 切断与应用结果逐条对应。
- 修改文件：新增 `docs/PROD_UX_ANALYSIS_20261015.md`；更新本文件。没有业务代码改动。
- 验证：所有数字来自上述只读查询与日志脚本，报告中注明了口径和样本窗口；未运行构建或测试（无代码改动）。
- 卡点/风险：
  - Caddy 日志只保留约 2 小时，CDN 层结论是样本外推；
  - 00:46Z 切换后 3.5 分钟不可用的确切原因（健康检查状态）未能确认；
  - 旧镜像期间“余额不足同号重试”的触发条件在 0.3.0 下没有复现，需另行回归。
- 下一步：等待用户确认优化规划与计费决策。本地已删除下载的生产日志副本，SSH 连接已关闭；分析脚本在 `/tmp/xy2-prodana/bin`，不含凭据，可删除。

### 2026-10-15T03:40Z — `20261015-prod-ux-analysis` — 补充 CDN 超时建议

- 核对官方文档：ESA 回源 HTTP 请求超时默认 30s、上限 300s；Cloudflare 的 Proxy Read Timeout 默认 125s，仅 Enterprise 可调（最多 6000s）。已把报告 7.1 第 1 项改为：ESA 设 300s；Cloudflare 不升级套餐，改走 ESA 或仅 DNS 子域名，并与网关首包心跳、首包超时换号一起实施。只改了本地未提交的报告与本记忆。

### 2026-10-15T06:55Z — `20261015-prod-ux-analysis` — Codex 重连现象排查（只读）

- 用户反馈 ESA 改为 300s 后 Codex 出现“idle timeout waiting for SSE”及两种 429。只读排查结论：与 ESA 调整无关。
  - ESA 约在 06:40Z 生效；同类错误 06:20 起已在账号 292 出现，Cloudflare 链路也有；
  - 两种 429 来自上游账号 307、292 在流内发出的忙或限流失败，网关原样透传，只冷却 2s，粘性会话又把请求送回同一账号；
  - idle timeout 是 Codex 自身 300s 的事件空闲计时，起因是上游中途停住。
- 已追加到报告第 8 节（建议把 429 冷却调到 30–60s、对问题账号降权，并做流内失败换号与改绑等代码改进）。生产未做任何修改。
- 补充（同日）：核实了“为何没有换号”。0.3.0 只在客户端尚无输出时才允许换号；这几次失败前已向客户端写出 2.3–3.8KB 真实内容（心跳会被扣除）。客户端重连后，又因 429 冷却只有 2s、sticky escape 只看账号整体指标、没有按会话连续失败改绑的规则，回到同一账号。已追加到报告第 8 节。

### 2026-10-15T07:10Z — `20261015-prod-ux-analysis` — 复核用户配置调整并更新剩余清单

- 只读确认生产配置：429 冷却 45s（07:05:04 修改）、过载冷却 2 分钟、ESA 300s。429 冷却每次从库中读取，立即生效。
- 最近 60 分钟仍然突出的问题：
  - 组 28 只剩账号 158 可用，出现重试风暴；
  - 组 53 的 `gpt-5.5` 定价限制返回 503，约 94 次/小时；
  - 组 72 两个 OAuth 账号均已 revoked；
  - 多名用户在同一不稳定账号上连续失败。
- 已把进度与剩余清单写入报告第 9 节。没有修改生产或代码。

### 2026-10-15T07:40Z — `20261015-prod-ux-analysis` — 优化规划访谈（grilling）进行中

- 用户要求全量读取两份生态 CSV（精选 36 项、候选 349 项，其中 140 项为无独有描述的通用 fork），参考生态实现，并结合线上请求与 XY-image 代码，规划可上线的优化方案。已派出 3 个只读研究代理：生态调度与健康、生态重试与保活、XY-image 集成；报告写入 `/tmp/xy2-grill/research/`。
- 第 1 轮已定稿：
  - 范围为 P0 与 P1 做到可上线，P2 只给进入条件，交付 OpenSpec 加运维 runbook；
  - 零字节未送达：立即取消上游且不向用户计费；
  - 流式请求延迟提交 SSE 头加心跳（通过校验与选号后，T=15s 仍无上游响应头才提交）；
  - 余额不足进入可恢复的独立状态；
  - 采用“xy2api 自有模块加最少钩子”，不向 Sub2API 官方提交任何改动；
  - 本次不修改 XY-image，xy2api 必须保持对它兼容；
  - 会话改绑必须照顾单号组，不能因为一次波动导致整组无号。
- 更正：`fallback_group_id` 不是无号兜底，只用于仅限 Claude Code 分组的降级；报告第 7.1、9 节已改，并补充第 10 节更正记录。
- 第 2 轮定稿（2026-10-15）：
  - Q10 同源联动：不做。
  - Q12：未送达的请求不写用量行，单独存一份仅运营可见的记录。
  - Q8、Q9、Q11 按生态调研结论执行：
    - Q8 失败即按 owner 解绑；回避集 60–120s，10 分钟内失败 ≥2 次延长到 10 分钟；选号分三遍；并发满、TTFT 抖动、客户端错误不改绑。
    - Q9 余额状态机 OK/SUSPECT/EXHAUSTED/RECOVERING；复检间隔 1→2→5→10→15 分钟；探测优先 sub2api 的 `/v1/usage`，其次 new-api（待实测），最后 1 token 生成探测（有日预算）；半开逐步恢复。
    - Q11 无号兜底组：只在无可用账号时触发、每个请求一次、同平台、按兜底组计费并对用户可见；先走兜底组，再在兜底组里取最不坏的账号，没有兜底组才在原组取最不坏的。
  - 用户改为允许修改 XY-image。XY-image 调研结论见 `/tmp/xy2-grill/research/xy-image-integration.md`；生产为 v0.0.16（258c0c9），生图并发 16。生态调度调研见 `/tmp/xy2-grill/research/ecosystem-scheduling.md`。
- 第 3 轮（2026-10-15）：
  - Q16：状态码维持不变（C）。
  - Q13、Q14 按推荐：XY-image 修高危问题；只对经 CDN 的请求提前提交。
  - 用户新增要求：Q8 的绑定释放规则太严，必须与份额调度一致，层级与权重由管理员掌控。已改为分级处理，等待确认。
  - Q11 主组恢复后要切回；Q15 上游余额只给管理员看。
- 已核实的事实：
  - 用户看到的“余额不足”几乎都来自本站计费：48h 内 1457 次是真余额 ≤0，约 500 次是在途预留之和超过余额（用户余额为正），上游余额泄漏只有 4 次（图片账号 402）。
  - xy2api 现存缺陷：
    - G2：流间隔检测的 ticker 周期等于阈值，实际在 T 到 2T 之间才触发；
    - G3：Anthropic 心跳后的错误帧缺 `event: error`，SDK 静默丢弃（`gateway_handler.go` 约 2012 行）；
    - G8：暂存的账号响应头（含 `x-codex-turn-state`）只在首次写出前生效，心跳先写出后会丢失。
- 研究报告：
  - `/tmp/xy2-grill/research/ecosystem-retry-keepalive.md`。要点：Codex 300s 空闲计时不被注释行重置；5xx 不带 Retry-After 时一个回合约产生 30 次请求；Retry-After 无上限；429 不带 Retry-After 即终止。
  - Gemini SDK 遇注释行会报错；上游 sub2api 在 v0.2.15 之后没有相关修复。
- 第 4 轮（2026-10-15）：
  - Q8、Q11、Q15、Q17–Q24 按调研方案执行（Q23 选 B，其余为 A）。
  - Q26 改为不做灰度：直接开发，在用户指定的测试服务器（地址见仓库外私有清单）上测试与预上线，确认全部闭环后再提 PR、发版。凭据只存放在本机临时文件，不写入 Git 或记忆。
  - 测试服务器只读勘察：
    - Ubuntu 26.04，8 核，15G 内存，可用磁盘 133G；是共享开发测试机。
    - 已有多套服务：旧版 xy2api、旧测试站、两套 XY-image（其中一套调用生产主站）以及若干主机服务。清单见私有清单，本方案一律不碰。
    - 测试域名所在区在 Cloudflare 上，但都是灰云直连。
  - 需要新建一套完全隔离的预上线环境。

### 2026-10-15T11:10Z — `20261015-prod-ux-analysis` — 上线级优化方案（OpenSpec）完成

- 请求/目标：用户确认访谈结论后要求“先完善整个方案”，随后指示“按上述方案执行”，即完成整套方案文档。开发、测试服务器部署、DNS、PR、发版、生产部署都不在本轮范围内。
- 完成内容：
  - 新增 `openspec/changes/gateway-resilience-20261015/`：proposal（含决策 D1–D33）、design（§0–§17）、tasks（第 0–15 节）、runbook、staging-validation、xy-image-appendix、8 个 spec（42 条需求、65 个场景），以及 `evidence/production-analysis.md`（去标识的生产分析摘要）、`evidence/research-summary.md`（34 个第三方仓库的固定 SHA、许可证与借鉴取舍，不含第三方代码）。
  - 复核两份 CSV 行数（精选 36、候选 349）。修正两处交叉引用：proposal 的 P2 进入条件指向 tasks 第 15 节；design 中 new-api 余额接口实测指向 tasks 12.6。
- 安全与数据处理：
  - 发现 liulixin-lex/xy2api 为公开仓库。已把方案文件与本轮新增（尚未提交）的记忆条目中的两台主机地址、测试机共存服务与端口、宿主机路径和内网网段改为代称；原信息移到仓库外的 `xy2api-artifacts/gateway-resilience-20261015/private/HOSTS.md`（0600，不含凭据）。
  - spec 场景中的生产账号编号改为 account A。
  - 完整生产报告副本放到仓库外 `production/` 目录。
  - 删除本机残留的生产日志摘录 `/tmp/xy2-prodana/data/inflight_rej.txt`，结论此前已记录。
  - 本机辅助脚本只从环境变量读取口令，不落盘。
- 验证：
  - 本机没有 OpenSpec CLI，改用脚本做结构检查：每个 spec 都有 ADDED 段；每条需求都含 MUST/SHALL 且至少有一个带 WHEN/THEN 的场景。结果没有结构问题。
  - 方案目录的敏感信息扫描（口令、主机地址、`ubuntu@`/`root@`、本机绝对路径、生产编号）为零命中。
  - 没有代码改动，未运行构建或测试。
- 未完成、卡点与下一步：
  - 等用户授权开始开发。授权后从 origin/main 新建 `feat/gateway-resilience-20261015`，并把本方案目录带到该分支。
  - 测试服务器部署要用户确认；测试 DNS 记录和真实测试 Key 由用户提供或授权。
  - PR、发版、生产部署各需单独授权。
  - 本分支与工作树改动都未提交、未推送。

### 2026-10-15T22:00Z — 20261015-gateway-resilience 最终候选复验与续链修复

- 内容：
  - 完成剩余开发：3.5 只读 Explain（原版调度入口在运维页）、12.10 指标对照工具；新增调度矩阵 24 例、韧性 Redis/PostgreSQL 集成测试、Claude Code 兜底独立性测试。
  - 真实中转复测发现 0.3.0 已有的续链缺陷（Messages 转 Responses 时多账号中转以笼统 400 拒绝 `previous_response_id`），修复为整轮重放并关闭会话续链（design §9.8，`fe6d43c50`）。
  - 测试工具：KA-8（300 秒 CDN 档）、CONT-1（fault-upstream F25）、anthropic-python 已提交错误事件用例、fault-upstream `delay` 参数。
- 验证：
  - 本机：后端全量 unit、四种标签 vet、韧性相关 `-race`（修复一处测试数据竞争）；前端 lint、vue-tsc、全量 vitest、构建；选号基准与 0.3.0 持平。
  - 测试服务器（最终候选 `fe6d43c50`）：场景 37/37、客户端 16/16、XY-image 回放 181/181；升级演练、浏览器与键盘检查沿用前一候选（迁移与前端未变）。
  - 集成测试在测试服务器容器内运行：韧性 6 例通过；全量仅 PgDumper 因容器缺 `pg_restore` 失败（v0.3.0 同样失败）。
- 未完成与下一步：24 小时浸泡（`soak-d80f35fdb`）与 design §14 指标报告；之后推送、PR、RC、正式版；生产部署需单独授权。未推送，未改生产。

### 2026-10-16T10:45Z — 20261015-gateway-resilience 生产报错优化合入与最终候选

- 内容：
  - 生产近 3 小时报错复盘（子 Agent 只读分析，1,649 次失败）的根因纳入本次：R1 Grok 余额 429（主 Agent）、R2 已输出后失败（子 Agent B）、R3 web_search 历史回放（子 Agent C）、R4 内容安全拒绝与 R7 Anthropic 流内状态（子 Agent A），以及 R5/R6/R10b/全 0 会话 ID 小项；三个子 Agent 在独立 worktree 开发，提交无冲突 cherry-pick 合入后删除 worktree。
  - 合并后补修：已输出后失败的 200 不再清零失败回合计数（`eaa63fd30`）。
  - 文档：design §2/§9.7/§13/§18、tasks“生产报错优化”、4 个 spec 新场景、覆盖矩阵、`GATEWAY_RESILIENCE.md`、去标识证据 `evidence/production-errors-20261016.md`。
  - 测试工具：fault-upstream F26–F29 与 BAL `reset`；场景 PE-1/2/3/4/7；夹具在场景规格变化时更新已有账号地址。
  - 内存诊断结论：无泄漏（诊断镜像与脚本已删除）。
- 验证：本机全量与 CI 门禁全绿；测试服务器 `eaa63fd30` 场景 42/42（首轮 PE-3 因同一进程已记住账号失败，用例已兼容并重启应用复跑）、客户端 16/16、XY-image 回放 181/181 ×2、在线对话 4/4。
- 未完成与下一步：等待 24 小时浸泡（约 2026-10-17T10:30Z）→ 指标报告与私有测试报告 → 用户确认提交作者身份是否改写 → 推送、PR、RC、正式版；生产部署单独授权。管理员配置项与代理凭据轮换建议已汇报用户。未推送，未改生产。

### 2026-10-16T12:10Z — 20261015-gateway-resilience 推送前历史整理、浸泡改判据、PR #97

- 内容：
  - 用户选择把分支提交作者改为 noreply 身份；推送前扫描发现方案文档含测试子域（灰云直连会暴露测试服务器地址），一并从全部历史去掉。最终树相对改写前只差该文档一行；哈希引用按映射更新（`a0fef9807`）。
  - 用户决定浸泡缩短为 6.5 小时并与 PR 的 CI 并行。
  - **更正 08:50Z 的“无泄漏”结论**：诊断运行完整数据（116 个样本）显示匿名内存后段约 +5.2 MiB/h，但 Go 堆在用 7 份 profile 持平（58–60 MB）、goroutine 持平，增长来自运行时保留的内存，不是对象泄漏。容器内存因此不能作为泄漏判据：浸泡改为开启 gctrace，以 GC 后存活堆的完整小时中位数斜率判定（`688bd9d68`），匿名内存斜率只作参考。
  - 推送因本机 Git Credential Manager 卡在交互提示而超时一次，改用 gh 凭据助手完成；PR #97 已开。
- 验证：同步审计、scheduler-diff 通过；后端与前端代码相对改写前无差异；推送后远端 head 与本地一致。
- 未完成与下一步：等 CI 与浸泡（约 17:53Z）；通过后合并、打 RC、校验制品、RC 复跑关键子集、晋级 0.4.0。生产部署单独授权。

### 2026-10-16T12:10Z — 20261016-scheduling-review — 已完成（只读；主 Agent 代为追加）

- 请求/目标：用户要求全量审查原版调度、份额调度与网关韧性的问题、重叠、互补与耦合，交主 agent 优化。
- 开始状态：分支 feat/gateway-resilience-20261015，HEAD 21f2417f4（PR #97 头 688bd9d68），工作树干净。
- 完成操作：只读通读代码与文档，未改仓库、未跑测试、未访问生产或测试服务器。
- 结论：交接文档 xy2api-artifacts/gateway-resilience-20261015/review/SCHEDULING_REVIEW_20261016.md。RC 前建议修 F1（排队 ping 计为已送达）、F2/F5（负缓存与快速失败与请求相关）；启用份额前修 F3（末位兜底不遵守份额策略）。
- 修改文件：无（仓库外新增上述文档）。
- 验证：静态审查，结论按“已读码确认/推断”标注；未运行测试。
- 卡点/风险：F2 严重度取决于生产是否使用仅粘性阈值或 /antigravity 路由，待用户确认。
- 下一步：主 agent 与用户确认第 9 节后排期。（12:20Z 用户已按“决策”一节定稿：0.4.0 修 F1、F2+F5、F3、F4、F7、F12、F14。）

### 2026-10-17T04:45Z — 20261015-gateway-resilience 审查与生产核查修复、测试服务器闭环、PR #97 合并与 v0.4.0-rc.1

- 请求/目标：按独立审查（F1–F14）与生产只读核查修复 0.4.0，测试服务器全部闭环后推送 PR #97、合并、打 RC，再晋级 0.4.0；生产部署单独授权。
- 完成操作：
  - 审查修复与复核后续：F1 `685986a68`、F2+F5 `6cfd31e4a`/`e18275960`、F3 `1b3d8e4fe`、F4 `60cc7d6fb`、F7 `25bb15ca2`、F12 `f2b077c4e`、F14 `a53b90175`；OpenAI 旧调度路径软罚保留绑定 `d6c8fd1e6`；OpenAI 按模型瞬时屏蔽按软罚参与末位兜底 `31407833d`（测试服务器复跑时发现）。
  - 生产核查修复：伪保活帧丢弃后仍算上游存活 `9adce14cb`；余额关键字补 `insufficient user balance` `8285e6926`；首输出超时不冷却账号按代码对齐文档 `fdb7cfdcb`，评审复核后更正“级联网关仍冷却”的错误说法 `7043f32cb`。
  - 测试工具：DC-5 改号与重复编号报错、`stream_request` 真正断开、LR-1 独立会话、KA-9 与 F24 `output=1`。
  - 候选 `934bcf2bc`：场景 45/45、客户端 16/16、6.5 小时浸泡 11 项全过（存活堆 0.3 MiB/h）；§14 报告 15 项达标，4 项为合成负载所致。
  - PR #97 18/18 全绿后常规 merge `ca2bdd111`；`v0.4.0-rc.1` 发布与制品、镜像校验全过；RC 镜像在测试服务器全新安装、场景 45/45、升级回退恢复通过，真实客户端 14/16（两项失败于真实中转高峰期）。
  - 用户 2026-10-17 要求余额提示恢复 0.3.0 原文：`24cfc6511`（晋级分支）。
- 验证：每轮本机全量验证（四种 vet、lint 0、unit/无 tag/embed、CI 门禁 256、同步审计与 scheduler-diff、前端 389 文件/3047 项与构建）全绿；PR 与 main CI 全绿；推送前扫描无主机、域名、凭据，作者均为 noreply。
- 卡点/风险：“未送达不计费只覆盖 OpenAI Responses 路径”待用户确认；两项真实客户端用例未在 RC 上重跑；0.4.1 后续见 tasks“后续”小节（xhigh/max 静默上游不换号、屏蔽期间保留绑定、`relay_keepalive` 原生 CC 路径计数、F2 会话上限判据等）。
- 下一步：用户确认待定项后，完成晋级 PR（`release/0.4.0`，已含版本号与文案改动）→ CI → 常规 merge → annotated `v0.4.0`（说明见仓库外 `RELEASE_NOTES_v0.4.0.md`）→ 用 `/tmp/xy2-verify-release.sh` 校验正式制品与稳定别名 → 正式镜像全新安装检查 → 发版后纯文档收尾；XY-image 0.0.17 先交接并经用户确认；生产部署单独授权。
