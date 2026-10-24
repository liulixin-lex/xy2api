# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-19 及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-10-24）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### OpenAI 缓存 P0a（2026-10-24，M1 已合入）

- T0a.1–T0a.4、运行时策略与管理员面板已随 PR #132 合入 main `72c2d0169`，功能默认关闭，未变更生产。G 的详细实现与验证记录已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。
- P1 L-01 已在吸收 M1 后完成本地实现与非计时门禁，保持默认关闭，待独立评审；当前交接见下方 P1 条目。


- G：M1 日志 Attr 合并已验证；正式 d1 待复核，详见归档。

### 缓存策略历史 UTC（M1.1，本地完成）

- 基于 main `72c2d0169`，history `at` 按 UTC 输出；八例时区回归及相关门禁通过。详见归档，未推送或部署；后续性能修复独立提交。

### 缓存 P0b / M2 后端（2026-10-24，本地实现完成）

- PR #133 测试隔离补丁已完成：两个 northstar 集成测试各用独立 PostgreSQL，保留真实仓储事务与原断言；定向 3/3、完整 repository 1779 通过及 4 项原跳过。CI 索引翻转本地未复现，未改上游测试、产品或迁移；负责人批准立即交付，精确共享统计证明排队后补。

- 基于文档前置 0dfeb9980 及已合入 M1 的 main；默认关闭的在线北极星、三种金额视角和四个管理员图表接口已完成，等待独立评审、集成与 staging 验证。
- 本地功能、race、真实 PostgreSQL 对账、五种构建方式 vet、完整韧性门禁及全量 lint 已通过。handler 全包仅有两项原有跳过；原编译接缝失败修复后全包复验通过，原始记录保留。
- 迁移 279 已在共享数据库使用前定稿，含保留期索引、不含会话状态表；部署后不得改写。详细登记在下方 G2 条目，提交号以本分支日志为准。未推送、部署或访问生产。

### 缓存 M1 已合入 main（2026-10-24）

- [PR #132](https://github.com/liulixin-lex/xy2api/pull/132) 于 2026-10-24T04:35:11Z 合入，merge commit `72c2d01698cd859223e943a0fa61b3454bca31ec`；tree 与批准候选 `6ba7c345c` 一致。
- 八项必需检查全部通过、独立终审无阻塞、端到端五项通过，分支保留；功能默认关闭，未部署。正式 d1/d2 由 G 另行交付，仅影响是否开启生产观测。
- P 的原交接与登记原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)；其中“待推送”等文字保留当时状态，以本摘要为当前事实。

### 缓存 M2 与 M1.1（2026-10-24，PR #133 测试隔离修补）

- 输入：文档公共base `0dfeb9980`，G2 P0b及R16/R17至 `94481bcf4`，UI至 `913e7a6c7`，M1.1 `411540d5d`/`d24ed1a2c`；本次追加G2测试隔离 `ce0192351`，不含M3。
- 已完成：原候选 `729928626` 的本地功能、编译、lint、安全、迁移与零执行门禁、R终审及S端到端通过；PR已开。GitHub首轮7项通过，test两组在既有EXPLAIN索引断言失败，未重跑。
- 本次：仅将northstar集成测试切到独立PostgreSQL并更新记忆。G2定向3/3、完整repository1779通过/4原跳过/0失败；本地原候选也通过，因此不宣称复现或证明根因。源码补丁已获R核对，集成候选增量复核及CI待完成。
- 下一步：按负责人授权普通push追加，让CI与R并行；本地只补docs/audit/diff，复用G2仓储门禁。8项必需检查、strict和讨论要求满足且R增量通过后以merge commit合入。
- 约束：公共base不动、分支保留、不force、不绕过保护；功能默认关闭、生产不变更。279及checksum字节保持冻结。

### 运行时缓存策略 T0a.5（2026-10-23，本地完成）

- 核心已在 T0a.3 d770e4dbb 上独立提交为 c493068db；接缝独立提交，提交号以本分支 git log 为准。默认关闭、管理员审计、每请求固定快照与远端约60秒刷新已验证。
- T0a.3 接缝全量缓存 race 加 undelivered 夹具定向复验、关闭态产品证明及59处独立零执行探针、默认全量 lint 均通过。完整门禁与已知 prelude 偶发证据见归档登记；G2 输入已固定且不再 rebase；T4 与运行时开关的跨分支接缝由集成终审复核。未推送、部署或访问生产。


### XY2API 0.4.6 正式发布完成（2026-10-23；生产未部署）

- 发布：`v0.4.6`（annotated 对象 `f267b5a7e`）→ main 合并提交 `fe7a3b6db`（[PR #129](https://github.com/liulixin-lex/xy2api/pull/129)）。Release run [39840369118](https://github.com/liulixin-lex/xy2api/actions/runs/39840369118) 成功，正式 Release 非 draft、非 prerelease，包含 6 个附件：`checksums.txt` 与 macOS、Linux、Windows 五个平台压缩包。
- 合并内容：PR #124（Responses `response.metadata` 开场判据，合并提交 `c020d3e71`）、PR #126（按分组并发槽位与 30 分钟健康缓存，合并提交 `8000bcb2b`）、PR #127（管理员分组分类、标签页和筛选，含迁移 278，合并提交 `f4340ff76`）。版本 PR #129 同步 `VERSION`、`UPSTREAM_BASE.json` 到 0.4.6，并把 DOMPurify 及传递依赖锁到 3.4.17。
- 验证：三个功能 PR 与版本 PR 的最新保护检查全部通过；#127 的 Gateway resilience 单测曾出现一次时序失败，单次重跑通过。版本 PR 首轮前端审计发现 DOMPurify 新公告，升级依赖后安全扫描通过。合并前本地文档一致性、scheduler-diff、upstream sync audit、工具 41 项测试、JSON 与 diff 检查通过；本机没有 Go，未重复运行 Go/前端全量门禁，远端 CI 已覆盖。
- 遗留与去向：生产部署未执行，需要单独授权；Sub2API 兼容基线保持 `0.2.15`。已结束的本轮合并、版本准备和发布登记详见本文件末尾操作日志，后续历史按规则归档。

### 使用记录“平均TPS”与指标公开按钮（2026-10-22 合入 main；未发版、未部署）

- 内容：使用记录“延迟”列改为“平均TPS N t/s”；“功能开关 → 管理员使用记录”两项指标各加一个公开按钮，缺省仅管理员可见。无迁移。现行说明见 [`USAGE_METRICS.md`](USAGE_METRICS.md)。
- 合并：[PR #121](https://github.com/liulixin-lex/xy2api/pull/121) 于 2026-10-22T10:13Z 常规合并，合并提交 `e7fe4a8e9`。合并前 α 把 main `cfb4fae48` 并入固定 head，只有本记忆冲突，两边保留。
- 未发版，随下一个版本发布；发版与部署都需要用户另行授权。详细记录已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### 无号兜底分组对用户不可见（2026-10-22 合入 main；未发版、未部署）

- 内容：用户端不再显示兜底，分组一律显示原分组；管理端保留“兜底：原分组 → 实际分组”。计费、限额和权限都按原分组，兜底组只决定由谁服务；主分组恢复后按现有机制切回。不做迁移，`usage_logs.group_id` 仍记实际服务的分组；管理端的倍率和金额从本版起是原分组口径；历史兜底请求不追溯重新计费。现行说明见 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md)。
- 合并：[PR #125](https://github.com/liulixin-lex/xy2api/pull/125) 于 2026-10-22T16:11:41Z 常规合并，合并提交 `c369e1cab`（head `58edc20de`）。REVIEW 无 P0、P1，上一轮的 P1 与七项 P2 都已关闭；QA 在 `ccf5774a1` 上 FBILL-1 到 10 共 107/107；CI 与安全扫描 18/18。
- 后续：INC-P2-1，WebSocket 每轮的 Q8 快照取在本轮完成的回调里，客户端提前发出下一轮时有毫秒级窗口，不阻断。
- 未发版，随下一个版本发布；发版与部署都需要用户另行授权。详细记录已原样移入归档。

### XY2API 0.4.5 正式发布完成（2026-10-22；生产未部署；已被 0.4.6 取代）

- 发布：`v0.4.5`（annotated 对象 `493133ed3`）→ main 合并提交 `0d8e4920e`（[PR #122](https://github.com/liulixin-lex/xy2api/pull/122)，头 `86607a8f4`，两者源码树相同）。Release run 39708586766 成功。产物核验通过：6 个附件（199,047,660 字节）双 SHA 一致；五个平台 `vcs.revision=0d8e4920e`、`vcs.modified=false`，含前端产物；GHCR 双架构 index `sha256:391fbdc5…`，version、revision、source 标签正确；`latest`、`0`、`0.4` 指向 0.4.5，`0.3` 不变。tag 消息的主题行后少了一个空行，Release 工作流因此丢了介绍段，Release 正文已手工补齐，tag 不动。tag 触发的 CI 有一个已知偶发用例失败，见 tasks 的“发布记录”。
- 内容（OpenSpec [`gateway-resilience-045-20261021`](../openspec/changes/gateway-resilience-045-20261021/proposal.md)）：
  - RA-1 修复：最终耗尽时，最后一个可重试上游（429/5xx）给出的有效 `Retry-After` 算作已知恢复时间，夹到 2–10 秒；
  - W1.2：OpenAI Responses 透传的首输出截止、停滞检测与有界暂存，Gateway 入站经 Anthropic 上游的 Responses 与 Chat 推迟开场、明确终止；
  - 审查遗留 P2（raw Chat `error` 判据、心跳只写网关自身的头、首写与取消竞态、末位兜底快照清理等）；
  - O3：原生 Responses 第一次心跳带上 `x-codex-turn-state` 并同时记录溯源。

  无新设置，无迁移。
- 验证：
  - 本地门禁：`4bb8026db` 25 项中 24 项通过，unit 只被基线堆阈值用例阻塞（main 同样失败）。ε 只改测试的修复 `13baa2869` 并入后，在 `e9c686e84` 上四项复跑全部通过。REVIEW 最终集成审查无 P0、P1。PR 的 CI 与安全扫描 18/18。
  - 测试服务器唯一候选 `0c31b5b38`（镜像 `sha256:b18e3e35…`，与发版树只差测试与文档）：
    - 71/71，RA-1 带 `retry-after: 8`；
    - 12 项补测 12/12（首轮 6 项因夹具复用正文失败，只改夹具后重跑）；
    - 8 项透传补测 8/8（同样 8 项在 0.4.4 上 6 项失败、2 项通过）；
    - 升级演练 104/104、105/105；
    - 3 小时浸泡判定通过（0.9 MiB/h）；6.5 小时浸泡也通过（完整小时 12/12，0.5 MiB/h；汇总工具首次运行 KeyError，只修工具 `80c1e120a` 后用原始数据重算，原失败保留）。
  - O3 与 OAuth 头只由单元测试覆盖。正式镜像全新安装通过：空库，启动 3.2 秒，324 条迁移，29 项检查全部通过，公开版本 0.4.5，计费请求写入 1 条正费用的用量记录，安装日志 0 条 ERROR。明细见 OpenSpec tasks 5.2、6.x 与“发布记录”。
- 遗留与去向：
  1. 生产仍未部署 0.4.4 或 0.4.5，需要单独授权；0.4.5 的 6.5 小时浸泡是生产部署的证据。
  2. 0.4.6 待办见 tasks §7：7.2（turn-state 溯源改在响应头真正发出时记录，同时收窄 `GATEWAY_RESILIENCE.md` 的相应措辞）、7.3（LEAK-P2-1）、7.5（堆用例的测量方式）、7.6（心跳写失败后首输出 guard 未转入 drain，终态用量可能少计；0.4.4 引入，P2）。
  3. PR #120（渠道监控 v1）在 `v0.4.5` 之后合入 main（`27b472e22`，2026-10-22），已进入后续版本；本次 0.4.6 发布未包含它。
- 本任务的交接与登记（O3、集成、RA-1、δ/ε/DOCS 预备分支、QA 验证、#120 的发布后集成），以及已完成的 0.4.4 6.5 小时浸泡与 pprof 诊断登记，已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### XY2API 0.4.4 正式发布完成（2026-10-21；生产未部署）

- 发布：`v0.4.4`（annotated 对象 `333f54ad5`）→ main 合并提交 `4b0b6d94b`（[PR #118](https://github.com/liulixin-lex/xy2api/pull/118)，头 `a38ae5995`，两者源码树相同；#116、#117 随之标为已合并）。Release run 39582933562 成功。产物核验通过：6 个附件（198,881,025 字节）双 SHA 一致；五个平台 `vcs.revision=4b0b6d94b`、`vcs.modified=false`，含前端产物；GHCR 双架构，version、revision、source 标签正确；`latest`、`0`、`0.4` 指向 0.4.4，`0.3` 不变。第一次核验因校验脚本用了旧的远端入口而失败（环境问题），换现行入口重跑通过。
- 内容：
  - 网关韧性第二期（OpenSpec [`gateway-resilience-v2-20261021`](../openspec/changes/gateway-resilience-v2-20261021/proposal.md)）：可重放前缀与推迟开场、raw Chat 明确终止、分类修正、失败帧重试提示与 Codex 上限、仅流式能力学习、事件字段；8 个新开关缺省开启，关闭任一项回到 0.4.3 的对应行为；
  - 份额调度精简（#117）与账号页弹窗第一次打开即加载（#116）；
  - 安全升级：Go 1.27.3（GO-2026-6809）、前端 follow-redirects 1.16.1（两个高危公告）。

  无迁移。
- 验证：最终候选 `a38ae5995` 本地 21 项门禁全绿（`ci_gate` 375 项 `-race` 0 跳过）；PR 的 CI 与安全扫描 18/18；两轮独立代码审查放行。测试服务器唯一候选：场景 **70/71**（RA-1 为已知缺陷，0.4.5 修复）；升级演练 62/62；3 小时浸泡 **11/12，未达标**（GC 后存活堆 1.4 MiB/h，门槛 ≤1），用户 2026-10-21 决定放行。旧候选 `c086f4cc6` 因安全扫描作废，未做服务器验证。正式镜像全新安装通过：空库，启动 2.2 秒，324 条迁移，`/ready`、`/health`、首页与静态资源、`/auth/me` 正常，公开版本 0.4.4，计费请求写入 1 条正费用的用量记录，安装日志 0 条 ERROR。明细见 OpenSpec tasks 的 10.2、11.3 与“发布记录”。
- 遗留与去向：
  1. 生产未部署 0.4.4：需要单独授权。生产部署前的 6.5 小时浸泡已于 2026-10-21T17:57Z 验收通过（12/12，存活堆 0.5 MiB/h）。
  2. 存活堆斜率：上面这次 6.5 小时浸泡已达标；pprof 诊断已完成，没有发现单一的无限增长点（登记已归档）。
  3. RA-1 与第二轮审查剩余的 P2：已在 0.4.5 修复并发布（见上一节）。
  4. 0.4.3 一节的遗留（生产运行的程序与镜像、主机配置项）状态不变。
- 本任务，以及随本版发布的 #117（`20261020-share-simplify`）与 #116 的交接和登记，已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### XY2API 0.4.3 正式发布完成（2026-10-20；生产未部署）

- 发布：`v0.4.3`（annotated 对象 `f30434efb`）→ main 合并提交 `888f49db8`（[PR #114](https://github.com/liulixin-lex/xy2api/pull/114)，头 `972a2211c`，两者源码树相同）。Release run 39392556340；6 个附件（198,544,619 字节）双 SHA 校验通过；五平台 `vcs.revision=888f49db8`、`vcs.modified=false`、embed 构建；GHCR index `sha256:1a2b9127…` 双架构，version、revision、source 标签正确；latest/0/0.4 指向 0.4.3，0.3 仍为 0.3.0；tag 触发的 CI 与安全扫描全绿。
- 内容：0.4.2 生产只读观测（仓库外报告）里要改代码的项，范围按用户 2026-10-20 的决定（P1、P2，不含 P1-1）：
  - Cloudflare 风格的 5xx 错误页按 `TRANSIENT` 处理；
  - 请求体上传中断记为 `upload` 阶段，不计入滥用次数；
  - 在线更新的重启先排空在途请求（`server.restart_drain_seconds`），在容器内执行时给出提示。

  无迁移。详见 tasks“0.4.3”。
- 验证：本机全量门禁（`ci_gate` 283 个）；PR 与 main 的 CI 和安全扫描全绿；测试服务器只构建了一个候选，场景 56/56（含新增的 SW-2），0.4.1→0.4.3 与 0.4.2→0.4.3 两条升级演练通过；不做浸泡，理由见 tasks。正式镜像在测试服务器空库全新安装（单进程）：启动 3.3 秒，324 条迁移；`/ready` 返回 JSON；`/health`、首页与 6 个静态资源、`/auth/me` 均 200；公开设置的版本为 0.4.3；计费请求 200；XLSX 导出 5,001 行，下载成功；头像压缩为 JPEG 存储；日志 0 条 ERROR。
- 遗留与去向：
  1. 生产主机 2026-10-20 05:09Z 起运行后台在线更新上去的 0.4.2 程序，容器镜像与 compose 仍是 0.3.0（观测报告 P1-1，用户决定暂不考虑）。0.4.3 的分类修复要在生产换版本后才生效；部署需要单独授权，建议更新镜像而不是在线更新。
  2. 观测报告里的主机配置项（P1-3 Caddy 日志脱敏 `X-Api-Key`，以及 P2 的 Caddy、DB 降级、`cdn_only` 等）由另一位执行者在主机上处理，不在仓库里；P2-4 缓存读占比留待 24 小时报告复核。
  3. 0.4.2 的遗留（预上线主机、Sub2API v0.2.17、导出 worker、提交身份、测试服务器收尾）见下一节，状态没有变化。测试服务器另有本任务的候选镜像 `xy2api-staging:972a2211c`。
- 本任务的登记已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### XY2API 0.4.2 正式发布完成（2026-10-20；生产未部署）

- 发布：`v0.4.2`（annotated 对象 `3a0aebb8b`）→ main 合并提交 `6e0ad2f3e`。合并顺序（均为常规 merge）：[PR #109](https://github.com/liulixin-lex/xy2api/pull/109)（`release/0.4.2`，含 #106、#105、#107、#108）→ `50055270f`；#111 → `9e3b25a14`；#110 → `7a0bafa2c`；#112 → `6e0ad2f3e`。Release run 39369505085；6 个附件（198,541,883 字节）双 SHA 校验通过；五平台 `vcs.revision=6e0ad2f3e`、`vcs.modified=false`、embed 构建；GHCR index `sha256:207802bb…` 双架构，version、revision、source 标签正确；latest/0/0.4 指向 0.4.2，0.3 仍为 0.3.0；tag 触发的 CI 与安全扫描全绿。
- 内容：两份生产就绪审查与全局复审的修复（R01–R10、P1-C、G1、G2，详见 tasks“0.4.2”）、`/ready`、并发槽位进程租约、连接池启动告警；分组强制 Fast（#105）、专属分组标签（#107）、账号列表筛选与排序（#108）、账号近期健康（#110）、前端流畅度与命令面板（#112）、excelize 安全修复（#111）。无迁移。发版说明（含浸泡结果）在 tag 说明与 Release 页面。
- 验证：最终候选 `6e0ad2f3e` 的 CI 与安全扫描全绿；测试服务器场景 55/55，0.4.1→0.4.2 升级演练通过，后台接口与浏览器冒烟通过（列设置一项为脚本误判，实有 18 行），XLSX 导出 109,353 行。3 小时最终浸泡跑的是 #106 的候选 `0ad63fab7`：11 项中 10 项通过，GC 后存活堆斜率 1.2 MiB/h 超过 1.0 的上限；分析与用户决定（2026-10-20 接受分析、继续发版）见 tasks“0.4.2”。正式镜像在测试服务器空库全新安装（单进程，导出只对这个临时容器开启）：启动 3.2 秒，324 条迁移（最后为 277）；`/ready` 返回 JSON，`/health`、首页与 6 个静态资源、`/auth/me` 均 200，公开设置的版本为 0.4.2；经故障上游的计费请求 200；XLSX 导出 5,001 行，下载成功（678,739 字节，工作簿合法）；160×160 的 PNG 头像压缩为 JPEG 存储（77,048 → 17,726 字节）；日志 0 条 ERROR。
- 遗留与去向：
  1. 生产部署 0.4.2：每次单独授权，按 runbook。上线前把以前为 Fast 加价而提高的分组倍率恢复原值；核对不经 Cloudflare 的入口是否清掉客户端自带的 `CF-Connecting-IP`；从 0.4.1 及以前第一次蓝绿切换放在低峰，或先停旧再起新（发版说明“升级与上线注意”）。
  2. 预上线主机运行 `0ad63fab7`（#106 的候选），比正式版少 #105、#107、#108、#110、#111、#112；换成正式镜像要另行授权。业务验收等用户添加测试 Key；SMTP、异机备份与告警等用户提供（runbook §3.4）。
  3. Sub2API v0.2.17：定时 Upstream Sync（run 39228050925，2026-10-19T09:10Z）的 prepare 步骤失败，由用户安排另开同步任务，按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 处理。
  4. 用量导出在每个实例上都运行 worker（`usage_export.enabled` 只管新建任务），本地存储时各实例写自己的目录。[`USAGE_EXPORTS.md`](USAGE_EXPORTS.md) 已写明多实例必须挂同一个目录；蓝绿重叠期间两个进程也算多实例。测试服务器上两个进程各用自己的目录时，下载返回 500 `EXPORT_INTERNAL_ERROR`，单进程正常。
  5. 提交身份：`release/0.4.2` 上的 4 个合并提交（`a504a6896`、`798ef27b6`、`8450363ed`、`368b418ef`）作者是占位身份 `review@example.invalid`，原因是另一会话临时改了仓库本地的 git 配置，已恢复；按“不改写历史”保留。GitHub 合并生成的提交（#109、#110、#111、#112）作者是账号的默认邮箱。如需改用 noreply，在 GitHub 账号设置里开启 “Keep my email addresses private”。
  6. 测试服务器收尾：本任务的候选镜像（`xy2api-staging:*`）、`evidence/` 下的浸泡与演练目录、升级演练留下的 `xy2api_upgrade` 库，按 staging-validation §11 在生产验证后清理；预上线主机上的两份切换前备份（路径见私有清单）在正式部署后再删。
- 本任务与 #105、#107、#108、#110、#112 的交接状态与登记已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### 版本与环境速览（2026-10-23T19:45Z 核实；2026-10-23 更新正式版）

- 正式版：XY2API v0.4.6（2026-10-23，Sub2API 兼容基线 0.2.15）。生产主机 2026-10-20 09:15Z 起经在线更新运行 0.4.3 程序（2026-10-21T11:35Z 只读核实：公开设置的版本为 0.4.3，容器内程序 commit `888f49db8`），容器镜像仍是 0.3.0；部署需要用户单独授权。
- 待定：XY-image 0.0.17（先交接，并经用户确认）；0.4.6 生产部署（单独授权）。预上线主机运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。见上两节。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-17 定时检测；2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.3）；golangci-lint v2.14.0（与 CI 同版）。
- 文档一致性：`python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业执行），规则在 `tools/docs_consistency.json`，改动时按 [`AGENTS.md`](../AGENTS.md)“变更同步”同步文档。

### XY2API 0.4.1 正式发布完成（2026-10-18；生产未部署）

- 发布：`v0.4.1`（annotated 对象 `7dbdbccd9`）→ main 合并提交 `edea0d493`（[PR #103](https://github.com/liulixin-lex/xy2api/pull/103)，头 `7f112a981`），PR #101、#99、#98 随之标为已合并。Release run 39126008209；6 个附件双 SHA、五平台 `vcs.modified=false`、GHCR `sha256:baa0e446…` 双架构；latest/0/0.4 指向 0.4.1，0.3 仍为 0.3.0；正式镜像全新安装 324 条迁移通过。
- 内容：CDN 回源自动识别（`CF-Ray` 或 `CDN-Loop: cloudflare` 为 `cf`，`CDN-Loop: esa` 为 `esa`，design §7.1；ESA 的头经生产 Caddy 访问日志只读核对）；PR #101、#99、#98（见下一节）。无迁移。按用户决定不出 RC、不跑浸泡。
- 验证：本机全量验证全绿（CI 门禁 249、前端 3055 项）；测试服务器去掉测试 CF 站点的 Caddy 标记后 KA/CF/ERR 19/19，经真实 Cloudflare 的慢首包 15.1 秒开始心跳、150.7 秒完成，直连与内部请求没有心跳。
- 部署说明（生产部署仍需单独授权，按 runbook）：0.4.1 上线时 Caddy 标记与 `SERVER_TRUSTED_PROXIES` 不再是首包前保活的前提（生产已配置的保持不变）；#101 新增错误码 `EMAIL_FORMAT_NOT_ALLOWED`；智商检测解析器升为 `iq-response-v6`；Grok 渠道监控可选原生 Responses。从 0.3.0 升级执行 0.4.0 的迁移 272–277，备份、蓝绿切换、观察与回退照 runbook。
- 本任务的登记已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### 网关韧性 0.4.0 正式发布完成（2026-10-17；生产未部署）

- 发布：`v0.4.0`（annotated 对象 `451f3f240`）→ main 合并提交 `4c142e1aa`（[PR #100](https://github.com/liulixin-lex/xy2api/pull/100)，头 `49f14df9a`）；RC `v0.4.0-rc.1` → `ca2bdd111`（[PR #97](https://github.com/liulixin-lex/xy2api/pull/97)）。Release run 39043056373；6 个附件双 SHA、五平台 `vcs.modified=false`、GHCR `sha256:4da9162a…` 双架构；latest/0/0.4 指向 0.4.0，0.3 仍为 0.3.0；正式镜像全新安装 324 条迁移通过。
- 内容：网关韧性（方案 `openspec/changes/gateway-resilience-20261015/`，管理员说明 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md)）、份额调度规范化、生产报错修复、晋级清单 A1–A5 与上游余额处理精简（余额失败暂停 `balance.pause_seconds`）。正式版候选 `c6b837dca` 整套验证一次通过（场景 45/45、真实客户端 17/17、升级演练、6.5 小时浸泡）；正式镜像与它只差一句英文设置提示（`5fdd1e589`）。
- 遗留与去向：
  1. XY-image 0.0.17：先交接留言并经用户确认（tasks 11、13.1、13.3）。
  2. 生产部署：每次单独授权，按 runbook；部署后 24/72 小时按 design §14 出报告（tasks 14）。上线前的管理员配置建议见归档中本任务原未完成第 1 项；0.3.0 因余额 403 被置错的账号，充值后在账号列表“恢复状态”。
  3. 0.4.1：tasks“后续”小节。
  4. 收尾清理：本地备份分支 `backup/gateway-resilience-pre-rewrite`、`backup/pre-author-rewrite-2424b8b38`；测试服务器 itest 目录与 `xy2-itest-*` 卷；`/tmp/xy2-staging/live-keys.json` 与 `ui-session.json`；测试环境在生产验证后按 staging-validation §11 清理。
  5. Sub2API v0.2.17 同步另开任务。
- 安全提醒：两台主机的 SSH 密码在对话中出现过，建议改用密钥并更换；生产 settings 中的明文代理凭据（`openai_codex_ticket_harvest_proxy_url`）建议轮换。
- 本任务的详细交接状态与“进行中的工作”登记已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

### 并入 0.4.1 的 PR #101、#99、#98 与 2026-10-17 的生产事项（摘要；详细交接已原样归档）

- PR #101 注册邮箱别名加固与用户批量启停：注册、发验证码、第三方邮箱自动注册与邮箱绑定拒绝非常规本地部分，新错误码 `EMAIL_FORMAT_NOT_ALLOWED`；别名归一去掉引号，查重计入软删除账号；管理后台多选批量启用/禁用（`POST /api/v1/admin/users/batch-status`）。具名补丁 `xy-registration-email-alias-hardening`、`xy-admin-user-batch-status`。生产 Caddy 的注册接口 `Sec-Fetch-Site` 拦截规则 2026-10-17 已单独生效，不在代码中。
- 2026-10-17 误删 93 个用户：18 个充值用户及其 25 个 API Key 已经 PITR 恢复；3 个 GitHub 登录绑定依赖按邮箱自动重新绑定，未在生产验证。明细在仓库外私有笔记。
- PR #99 智商检测忽略仅含空白的占位消息，解析器 `iq-response-v6`。0.3.1 未单独发布，并入 0.4.1。
- PR #98 Grok 渠道监控可选原生 Responses，说明见 [`GROK_MONITOR_RESPONSES.md`](GROK_MONITOR_RESPONSES.md)。
- 这些任务的交接状态与登记已原样移入 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)，操作日志按时间留在本文件。仍未结束的 Codex 联网搜索 502 诊断见下一节。

### Codex 历史联网搜索导致 502、gpt-5.6-terra 被移出分组 18（2026-10-17，只读诊断完成；待修复，未改代码、未改生产）

- 现象：一名用户（Codex Desktop 0.130.0-cometix；用户 ID 见上述仓库外私有笔记）在分组 18「GPT-不降智」用 gpt-5.6-terra 调 `/v1/responses`，北京时间 17:00:21 起持续失败。17:00:21–17:42:02 为 502（471 次，账号 378/365 的上游 SSE 返回 `response protection is unavailable`），17:42:03 起为 404 `model_not_found`（261 次）。客户端持续重试，峰值约 27 次/分钟。
- 502 根因（推断）：ChatGPT 内部 Codex 接口会拒绝“输入回放了 `web_search_call`、但请求未声明 `web_search` 工具”的请求，而 Codex 本地压缩会以 `tools:[]` 发送完整历史。xy2api 的修补 `ensureOpenAIOAuthWebSearchToolForHistory*`（`backend/internal/service/openai_oauth_web_search_history.go`）只在 `IsOpenAIOAuthLike()`（OAuth/SetupToken，`backend/internal/service/account.go:1273`）时生效。378/365 是 apikey 账号，上游为第三方 ChatGPT 系 Codex 中转（域名见私有笔记），请求未经修补。请求体不落库，这一点依据报错文本与代码注释推断。
- 佐证：同时段 378/365 为另外 11 个用户正常服务 1000 多次；24 小时内该报错 477 次，其中 471 次来自该用户。
- 404 原因：管理员在 17:38:45、17:42:03 移除了 378、365 模型映射中的 gpt-5.6-terra，而分组 18 的模型白名单仍包含它，组内已无可调度账号支持该模型（379 含该模型，但不在分组 18 且不可调度）。17:50:05 该用户被禁用，此后请求返回 401。
- 生产现状（核实于北京时间 2026-10-17 17:53）：分组 18 无账号支持 gpt-5.6-terra；该用户为 disabled。
- 待用户决定：① 恢复 378/365 的 terra 映射，或把 terra 从分组 18 白名单移除；② 代码改进：让 Codex 中转类 apikey 账号也能应用该修补（建议账号级开关，或遇此报错时带修补重试一次；不宜对全部 apikey 账号默认注入，官方 API 可能拒绝 `external_web_access`）。诊断过程见操作日志 `20261017-codex-websearch-502-terra`。
- 待核实（2026-10-17T13:00Z 补记）：main 上的 0.4.0（PR #97）新增了 `resilience_web_search_replay`（`backend/internal/service/resilience_web_search_replay.go`、`backend/internal/handler/resilience_web_search_replay.go`），上游在输出前以 web_search 历史拒绝时会按设计重放或换号，并把上游原因返回给客户端。它是否覆盖上述 apikey 中转账号，需在 0.4.0 上核实后再决定第 ② 项。

### Sub2API 0.2.15 / XY2API 0.3.0 正式发布完成（2026-10-13）

- 独立工作树 `<工作机 C>/xy2api-sync-20261013`；起始main `7b1d5d8c387c461a660befa3ac936b39715b4bf0`。固定官方annotated v0.2.15（object `86a80c13dcba86f52f9ca815b5a471cecc236227`，target `f2669c8cf62555cd92389b3f55920e9e6e7c6ff2`），真实base `0363b8cdba8cec3e2ba4b2dbd49c4481143fa55d`。146个上游提交、27项冲突裁决见[同步说明](UPSTREAM_SYNC_V0215.md)。
- [同步PR #92](https://github.com/liulixin-lex/xy2api/pull/92)已按固定head `bcb62e5801996fb3b3a3411096cae22f5fa2651c`、18/18成功检查合入main `8f47f8fb2ee82d9cd85acd7d7d9353c64e41e614`。PR90、91已自动标记merged，原head分别`2c8507f8b8ff695f4a4f20689535aa9df1098455`、`5eec0830aad240e0b3d2171505fa7299e84225a6`；保留真实merge历史。
- 备份现场经授权只读确认：应用角色无法读历史外部schema，客户端18.6/数据库18.4及TLS正常。用户选择public；新显式scope保留旧全库默认，升级后须在页面选择并保存public。native TOC保留public与管理员pg_trgm，真实PG覆盖函数/类型/序列/数据、其他schema不变及SQL错误事务回滚。恢复目标须先初始化本版本schema/扩展。见[备份说明](BACKUP_RELIABILITY.md)。生产未改权限/配置/服务，未执行导出或恢复。
- controlled退役，share默认关闭，旧策略只导入未启用草稿；异步图片独立context与R1–R4心跳/对象清理/账本/存储关闭修复均已回归。tokens/s采用完整请求耗时，缓存改称基于互斥计费Token桶的读取占比；中英文与显示开关一致，见[统计口径](USAGE_METRICS.md)。
- 316条已发布SQL/checksum保持，仅追加270/271，总计318。安装空密码DSN使用统一构造；来源审计、调度差异白名单、生成后零diff、完整Go lint 0 issues及embed构建通过。前端384文件/3026项、最后文案38项、lint/typecheck/build与24张浏览器截图通过。后端本地unit62包/23651测试及子测试通过（40条件跳过）；原integration唯一迁移242引用失败修正为271，四项真实PG复验通过，最终head远端完整integration/race/真实Redis/PG及安全门禁再次全绿。最初gofmt同类报告上限3掩盖余10处import，已按全部改动Go文件一次修正，失败记录保留。
- [RC v0.3.0-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.3.0-rc.1) annotated object `e96985ec1a7a6afb8704d788763756dd7f73d707` → `8f47f8fb2ee82d9cd85acd7d7d9353c64e41e614`；[Release run38345930894](https://github.com/liulixin-lex/xy2api/actions/runs/38345930894)成功。5平台包+checksums六资产196948970字节独立双SHA校验通过；双架构image index `sha256:f5b7500bc785c679f200266d312ec695f1857904618116d072fb1af20ba8c563`，labels版本/revision/source一致；prerelease=true/draft=false且stable aliases未移动。RC制品vcs.modified=true已在隔离checkout复现：发布前tidy删除10条生成工具多余go.sum，go.mod与5个平台二进制运行依赖未变；前端资源本身不会使工作树变脏。正式晋级纳入相同checksum整理，并在make generate末尾tidy，实际重新生成后工作树为零diff；正式五平台制品已逐一验证vcs.modified=false。
- 真实发行RC镜像完成全新318迁移、0.2.6的316→318升级、成套PostgreSQL/Redis/app-data恢复再运行旧版，health/login/me/6静态资源均通过；恢复snapshot与baseline同为`50674a127239ee9e5ee307f8ecc0eb71ad2020a4ab27534a69145c9333e85378`。三个PG客户端均18.6。4个既有部署ID、状态、配置和挂载摘要全部不变。
- [正式晋级PR #93](https://github.com/liulixin-lex/xy2api/pull/93)最终head `77d43e92b924ec22311487c4b5821521d56cdd41`两组18/18检查成功，常规merge为`1e0fbdd704b9080bb730d337e2003136c683ab78`；[正式v0.3.0](https://github.com/liulixin-lex/xy2api/releases/tag/v0.3.0) annotated object `db6f317ef6af57c74aef55112ccd8fe58d220c92`指向该提交。[Release run38353591650](https://github.com/liulixin-lex/xy2api/actions/runs/38353591650)成功，draft=false/prerelease=false。
- 正式六资产196942614字节双SHA、五平台原生格式/源码revision/版本与vcs.modified=false全部通过；GHCR linux/amd64与linux/arm64、标签version/revision/source及latest/0.3/0全部一致，index `sha256:5560a67e10b463dbab3e20fca5e719b2933f93dd9f341e890bfe34a12f7f7281`。
- 正式真实镜像全新318迁移、health/login/me与静态资源通过，pg_dump/pg_restore/psql均18.6。本任务容器/网络/卷及7处演练凭据、备份和应用数据路径已删除；4个既有部署ID/状态/配置/挂载均与起始完全相同。临时dirty复现clone已删除，诊断与校验报告保留在`<工作机 C>/xy2api-release-20261013/`。生产仅只读诊断，未部署或改变权限/设置。
- 收尾按AGENTS要求在发行后补记本文件，单独走纯文档PR；不移动已发布标签。故收尾后main可比正式tag多纯记忆提交，非本记忆文件与正式tag必须零差异，发行源始终固定为1e0fbdd704。同步/晋级短期分支待该纯文档收尾合并后删除；保留原用户feature分支和全部不可变标签。
- 后续部署事项：升级后显式保存public备份范围；新目标先初始化所需扩展，production备份仍需在实际部署后验证。share默认关闭，实际影子/逐组启用与业务账单观察是部署后的流程。非阻断维护项：GoReleaser旧配置字段弃用提醒、前端依赖审计中未达high/critical阻断等级的告警；本次未借此扩展依赖升级。

## 进行中的工作

### 20261024-cache-m3-e9 — L-01 租户种子门禁（G，进行中）

- 基于冻结M3候选6188d5d04，仅在policy关闭早退后、cohort前要求RoutingSession带v2:；非隔离旧种子记no_routing_session，不变更0.4.x粘性与显式键处理。
- 覆盖三处真实哈希入口、复合组OpenAI apikey真实Forward与policy_off零执行顺序；附平台闸门阳性/阴性对照，内部skip不代表复合组public trace。单批五tag、L01 focused unit/race、增量lint待执行。T4.0暂停，未推送或部署。

### 20261024-cache-m3-integration — hardening、L-01 与管理员策略卡片（P，进行中）

- 基于已合入 M2 的 main `16a776e347`，依次集成 G2 hardening、G P1/M3 与 UI 卡片；保留 M2、M1.1、迁移 279 和 northstar 测试隔离。项目 noreply 身份与来源尾注保留。
- 三方来源已独立审查；集成后固定候选，由一次合并流水线、独立关闭态 golden 与正式 staging 验收。全部通过后按授权经 PR、八项必需检查与 strict 合入；不部署生产。当前三方23项已导入；重复深拷贝声明的删除已折入首个P1提交。接缝保留只发布新revision，响应view建好即解锁再查账号；新增慢查询不阻塞订阅与关闭的回归。冻结M1列表除能力新增的upstream卡外完全比较，该卡按场景明确断言，完整新卡由22份v1.1实际HTTP回放覆盖。集成门禁尚未执行。


### 20261024-cache-m1-hardening — M1 后端加固（G2，变基完成，交接验证中）

- N-4：补真实logger的Responses流式、透传/原始Chat两种模式、16位会话摘要及非unknown路由/改绑；HTTP可见性覆盖真实回退中间件、Responses/Chat流式与非流式，以及上游错误耗尽。RecordUsage非abuse回归随N-2交付，均待批次门禁。

- 契约测试保留源期望：可用键HTTP回放，未来策略仅显式DTO/信封形状回放；历史输入为独立审计夹具。C-8未来功能未在当前构建行为测试中冒称通过；划分已交契约负责人核对。

- N-2：观测复用既有计费的送达判定，启用时保留上游用量快照；补晚断连/开关切换/用量改写回归。N-3 已独立提交，整批 Go 门禁尚待冻结后执行。

- 授权与基点：从 M2 后端 a99f71795 建立 cache/m1-hardening；每项小提交，不推送。M2 合入后再变基 main，先检查全部构建 tag；M2 评审阻塞优先修。
- 范围：T0a.4 N-2/N-3/N-4/N-5、T0a.5 N-3/N-7、T0a.2-fix n-2/n-5，以及 T0a.3 n-2/n-3/n-4；已完成的 observer_failed 优先级与 policy history UTC 不重复处理。
- 当前：10 个提交仅重放到 M2 main 16a776e347；产品补丁无冲突，保留 main 的 M2/R16/R17/隔离测试及记忆归档。旧门禁由 M3 最终候选流水线取代，已有失败保留；最小测试修正：正常计费模式断言为 token、未送达已关闭计时、legacy logger 夹具保留认证身份并显式注入历史摘要。五 tag 编译全部通过，计费与 logger 定向 race 通过，28 条事件严格校验通过；未送达旧测试另需将“容器为空”精确改为“计时关闭且前缀历史为空”，只补跑该组。长门禁交集成候选。
### 20261023-cache-p1-l01 — M3 上游 UUID 会话键补全（G，增量修复待新基点门禁）

- 基点main `16a776e347`（M2 已合入），分支 `cache/p1-l01`。既有响应/cohort补修 `05b3f3cfe`、管理员API `a90727351` 已交R；本轮按PM/R追加原位null和管理员候选故障降级，提交号以git log为准。仅实际注入时恢复已有响应字段为null，未生效与显式客户端值不动；不同上游无key形态仍需在实验中核对。
- 管理API：账号options静态合格、升序最多500；候选失败不阻断List/Get/PUT，空options并追加account_options_unavailable；disable方向Validate/PUT不访问账号表，开启/mixed保持必要校验。管理契约已同步m3m4 v1.1，共22个原字节M3样例；五份list含M2实际形态，回放完整比较。
- 验证范围：既有r5 focused7031与140分配对照是旧树证据；13:30剩余重任务按PM中止。本轮仅格式、diff、文档、静态契约与干净树审计，不宣称新增Go测试已通过。Q5是保存的官方文档流示例，不是实际请求录制。
- 当前：完整四提交已变基到M2 main；northstar、计数器、M1.1 emit-only/UTC与仓储测试隔离保留。1e31dbe3c五tag编译全部通过；v1.1后继e972cd4e7仅改样例/unit测试，unit补查通过，其余四tag按未变化编译输入继承。R已核对变基语义无阻塞；工作树待本次纯记忆收尾后交P。实际unit/race、zero/logger/compat/lint、22样例回放与staging仍由最终M3候选统一执行，来源没有重复长测试。未推送、未变更生产。



### 20261023-cache-p0b — 在线缓存北极星与管理员图表（G2，实现完成，待集成）

- PR #133 测试隔离补丁已完成：两个 northstar 集成测试各用独立 PostgreSQL，保留真实仓储事务与原断言；定向 3/3、完整 repository 1779 通过及 4 项原跳过。CI 索引翻转本地未复现，未改上游测试、产品或迁移；负责人批准立即交付，精确共享统计证明排队后补。

- 追加审查（R17，已完成）：priced_models 改为严格范围内已定价模型计数，priced_p_share 保持业务范围；新增范围差异回归，独立提交，lint 由 P 在 M2 最终候选上的全量 golangci 覆盖。

- 追加审查（R16）已修：邮箱账号名按 meta.platform/key 生成稳定标签，分组名采用相同判定；普通名称和原数据保留。v1.0.2 源样例自检、86 项定向 race、27 份实际 handler 输出校验均通过；lint 由 P 在 M2 最终候选上的全量 golangci 覆盖，独立提交交付集成。

- 授权：cache/p0b 基于文档归档提交 0dfeb9980（父提交为已合入 M1 的 main 72c2d0169）；只本地开发与提交，不推送、不部署。保留 main 的 T0a.4 API 对齐和 observer_failed 优先级修补。
- 范围：northstar 默认关闭的运行时策略、UTC 小时 rollup/回填/D-1、trace 小时计数与持久化、四个管理员图表接口与三种金额口径。沿用既有鉴权、审计与快照，不改变路由、用量或账单。
- 数据正确性：非 UTC 数据库时间、趋势/价格键和事件输出统一 UTC；每小时先读取前一小时再做会话 LAG，跨整点机会量不依赖持久会话状态。279 含 bucket_start B-tree，不含 session_state；跨整点独立期望值为 0/17011。
- 迁移冻结：279 的 sha256(TrimSpace) 为 58dc0c8572b7ecd1899db8f995d1f2795a40762a9d94e864ac6a8f377bc3447e，第一次在共享数据库执行后不再修改；后续结构修订使用新迁移。没有提交本地 embed 占位资源。
- 新基线验证：1613 项缓存 race、304 项配置/Wire race、6 项关闭态独立探针通过；F 契约实际 handler 输出 20 项自检通过。全 handler unit race 为 2035 通过、2 项既有跳过、0 race；完整 ci_gate 419 项通过、0 skip；default/unit/integration/e2e/embed vet 通过；默认全量 lint 0 问题。
- 编译接缝：原 unit vet 发现 M1 既有测试未传 provider 的新计数器参数，补 nil 且保留全部断言；定向及全包复验通过。所有改签名函数按不限 tag 的全 backend 引用核对，产品源码不受这行测试修复影响。
- 当前：最终真实 PostgreSQL DinD 两项通过，0 fail/skip；对账、跨整点独立期望、重复重放、D-1 事务和失败回滚均覆盖，临时资源按 ID 清理完毕。41 项同步工具测试及调度差异检查通过，干净提交再核验同步审计与文档。原始失败与环境记录保留，独立评审、staging 端到端、集成和 GitHub 必需检查由负责人安排；未推送、部署。


### 20261024-cache-m2-integration-prep — M2 与 M1.1（PR #133 等CI与增量核对）

- 执行者：P；来源映射、记忆冲突保留规则与原始失败证据在仓库外保存。原候选预检已结束，临时工作树/缓存清理，集成分支保留。
- 当前：按PM裁定追加G2 `ce0192351` 的测试隔离补丁，只改测试和记忆；产品、279、既有EXPLAIN断言不变。代码原样导入，记忆日志并集保留，项目身份和原提交说明保留。
- 下一步：普通push更新现有PR；G2完整repository结果继承，本地只补文档及audit；R核对集成差异，GitHub八项全绿后按已有授权合入，不变更生产。

### 20261023-cache-fe-panel — 管理后台缓存开关与参数（F-1a，开发中）

- 授权：缓存项目 PM 指派；基线 `e369fd3bb`，分支 `cache/fe-panel`；不推送、不合并、不部署。
- 范围：M1 只交付前端“开关与参数”页（`/admin/cache`，仅管理员，侧栏在“运维监控”之后），对接 `/api/v1/admin/cache/policies` 的列表、预校验、带 `expected_rev` 的写入与变更记录；后端接口缺失（404）时页面提示后端版本未提供。观测图表（F-1）为本分支单独提交：图表接口（P0b）存在时才显示“巡检”页签，缺失时页面只显示开关；生产构建只读真实接口，mock 数据仅开发环境可用。
- 约束：默认全部关闭；表单范围以接口返回的 schema 为准；总开关只覆盖改出站或选号的三个键；`claude_cache_fallback_policy` 与 `gateway.openai_cache.*` 只读展示并链接原设置页，不改其存储与语义。
- 状态：首版 `ec311712c` 经评审有条件通过后，已按评审与 v1.7.4 契约修复：错误按平台信封的 `reason` 识别，写入后整页重读，409 与 `rev_stale` 走三方合并，确认框列出提交后的影响，静态总闸期间仍可保存。接口契约样例作为前端测试夹具，相关测试已列入根 `Makefile` 的关键 vitest 清单；路由与侧栏入口已登记到 `tools/upstream-sync/policy.json` 的 required_literals。图表按 v1.7.4 §8 对齐金额口径与回退组标注，图表测试同样列入关键清单。待后端接口落地后联调。

### 20261021-channel-monitor-v1 — 渠道监控 v1 探测优化（PR #120 已合入 main，进入下一个版本；待真实上游验证；2026-10-22 DOCS 更新状态）

- 请求（用户 2026-10-21）：优化渠道监控 v1——“正常可调用但监控卡很久、黄绿与实际不符；分组部分满载时探测也排队导致超时”。用户对方案的要求：只动探测本身；30 秒内拿到正确回答且 ≤10 秒为绿、>10 秒为黄、30 秒内拿不到正确回答为红；不新增繁忙、过期等分类；探测协议兼容各种模型；让模型更快回答。随后“按你认为的最佳方案执行”。
- 分支：`feat/channel-monitor-v1-20261021`（工作树 `xy2api-wt/monitor-v1`，自 origin/main `a1cf8f104`），已推送，[PR #120](https://github.com/liulixin-lex/xy2api/pull/120) 于 2026-10-22 在 `v0.4.5` 之后合入 main（`27b472e22`），进入下一个版本，未发布。方案与调研在仓库外 `xy2api-artifacts/monitor-v1-20261021/`（`plan/PLAN.md` v3、`research/`、门禁日志 `gates/`）。
- 已完成（现行说明见 [`CHANNEL_MONITOR_V1.md`](CHANNEL_MONITOR_V1.md)）：
  - 探针：所有 provider 流式探测、读到答案即停；一句话加法题；按模型族带 `low` 推理强度与输出上限（推理模型 1024，Gemini flash 关思考、pro 取最低预算 128）；参数被拒时同轮修正重发（6 小时过期、按 Key 区分）；快速连接失败在预算内重试一次；10 秒、30 秒口径；HEAD ping 并行；连接复用；只跟随同主机重定向。
  - 探测通道：监控请求带与 API Key 绑定的签名头 `X-XY2API-Probe`，全局中间件校验后打标记并删除该头；调度对探测不用粘性会话，排队前先按真实并发找空闲槽（OpenAI/Grok 缺省调度、Anthropic 兜底与模型路由、份额调度）；具名补丁 `xy-channel-monitor-probe-lane`，登记 `policy.json`、`scheduler-diff.json`、`UPSTREAM_BASE.json`。
  - 调度可靠性：启动加载失败退避重试、每 5 分钟对账、首轮错峰、16 个 worker 且排队超过半个间隔跳过、`last_checked_at` 条件更新做跨实例认领、编辑后的首轮强制、检测可取消、手动检测脱离浏览器连接且不推进 `last_checked_at`、超时的一轮照常写入、quota_probe 配额并行。
  - 读路径 LATERAL 改写；前端：`error` 徽章改红、“响应时间”文案、立即检测超时 60 秒。
  - 独立代码审查（只读）4 项 P1 与选定 P2 已修，每项先写失败用例。
- 验证（本机）：gofmt、四种 vet、golangci-lint 增量 0 项；service 全量 unit 与无 tag 测试、repository/server/middleware/monitorprobe 通过；`ci_gate.py` 375 项 `-race` 0 跳过；scheduler-diff、`sync.py audit`、文档一致性、tools 41 项通过；前端 lint、typecheck 与监控相关 21 个测试文件通过。新 SQL 在本机临时 PostgreSQL 18 上与旧查询结果对照一致，执行计划在生产只读核对。
- 本机端到端（2026-10-21，临时 PostgreSQL、Redis、假上游，只监听 127.0.0.1，测完已清理；报告在 artifacts `e2e/REPORT.md`）：E1–E9 全部通过。
  - 12 秒判黄；35 秒在 30.0 秒判红。
  - 分组部分满载时，探测 5 ms 在空闲账号判绿，对照的普通请求排队 30 秒后返回 429。
  - 签名头 0 次到达上游；参数修正同一轮生效并被缓存；手动检测不推进 `last_checked_at`；重启后 28 秒内恢复。
  - 遗留一次未复现的 21.5 秒等待（详见报告，建议在测试服务器验证时加选号 debug 日志）。
- CI：首轮发现 lint 报只被 unit 测试使用的函数未使用、`monitorprobe` 的坏 MAC 用例有 1/16 概率偶发失败，`3737a6f5c` 已修；另一次 `TestCompatPreludeHeartbeatDisconnectDrainsUsageAfterContent` 偶发失败，与本 PR 无关，本机 -race 10 次通过。
- 未做与卡点：
  1. 合并已完成（用户授权，PM 2026-10-22T00:55Z）；发版、部署仍需用户授权。
  2. 测试服务器验证（真实上游的兼容矩阵、Grok 等推理模型的响应时间）需要 PM 协调测试服务器。
  3. 未做：本站探测走本机直连（保留经 CDN 的端到端覆盖）；OpenAI 高级调度器（缺省关闭）只覆盖“无粘性”。
  4. 审查 P2-7（Anthropic 补选失败时可能泄漏槽位）是原有写法，未改；只有闭合 `</think>` 的上游可能被提前判答，写在文档已知局限。
- 边界：未碰 δ、ε、RA-1 正在改的 handler、`resilience_*`、`GATEWAY_RESILIENCE.md`；`policy.json` 与它们的分支都有追加，合并时注意顺序。

## 操作陷阱

- 处理 Sub2API 同步只以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。`DEV_GUIDE.md` 的“Git 操作”曾写着直接 merge/rebase 上游 main，2026-10-17 已改为指向同步规范，这两条命令也登记为 `tools/docs_consistency.json` 的退役写法。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。
- GoReleaser 的稳定别名（`latest`、主版本、次版本）现用 `skip_push: auto`，prerelease 不再改写它们（v0.3.0-rc.1、v0.4.0-rc.1 发布后均核实未动）。早期 v0.0.7 RC 曾短暂改写别名，正式发布后已恢复。
- GitHub Actions 在本次 RC/正式 Release 中提示 `docker/login-action@v3`、`docker/setup-buildx-action@v3`、`docker/setup-qemu-action@v3` 的 Node.js 20 runtime 已弃用，Runner 强制切到 Node.js 24 后仍成功；这是后续 Action 版本维护项，不是本次发布阻塞。
- macOS 会定期删掉 `/tmp` 下 3 天没访问的文件。0.4.2 期间放在 `/tmp` 的 Go 工具链和测试服务器辅助脚本被删过一次。跨天的任务把工具链和脚本放到用户目录下，`/tmp` 只放当次的临时文件。
- 提交与打 tag 前先看一眼 `git config user.email`：仓库本地配置可能被别的会话改过（0.4.2 的 `release/0.4.2` 因此留下 4 个占位身份的合并提交）。`gh pr merge --match-head-commit` 要用完整的 40 位 SHA。
- `sync.py audit` 把工作树里的 `node_modules` 软链接也算作未跟踪文件。借用别处的依赖时放在工作树外面，或者 audit 前删掉软链接。
- 本机默认的 pnpm（12）不认这份 lockfile 的 `pnpm.overrides`，CI 用 pnpm 9。前端命令用 `npx -y pnpm@9 …`；`corepack pnpm@9 run build` 不够，`build` 脚本里嵌套的 `pnpm run` 仍会从 PATH 取到 12，报 `ERR_PNPM_VERIFY_DEPS_BEFORE_RUN`（2026-10-21 集成门禁遇到）。

## 项目速览

### 产品与架构

- XY2API 是 Sub2API 的二开 AI API 网关，负责多上游账号调度、API Key、配额/计费、协议转发、管理后台等。
- 后端位于 `backend/`：Go（版本以 `backend/go.mod` 为准，当前 1.27.3）、Gin、Ent、Wire，依赖 PostgreSQL 与 Redis；入口为 `backend/cmd/server/main.go`，前端构建产物嵌入 `backend/internal/web/dist/`。XY2API 自有的规则内核在 `backend/internal/resilience`（网关韧性）与 `backend/internal/sharesched`（份额调度）。
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

### 2026-10-20T02:46Z — 20261019-frontend-ux — 本地完成，待授权提交

- 请求/目标：拉取最新 xy2api，在本次对话内一次执行完整的前端优化方案，分三步：
  1. 先修性能；
  2. 再做动效系统；
  3. 最后做交互增强。
- 开始状态：分支 `feat/frontend-ux-20261019`，自 main `0320d4e68`（VERSION 0.4.1）；独立 clone，工作树干净。
- 完成操作：
  - **性能**
    - 表格：去掉 `<table>`/`<tbody>` 上的 `divide-y`。这类兄弟选择器让虚拟滚动每插入一行都要重算整个 tbody 的样式，Performance trace 定位到了这个问题。
    - 桌面表格改为逐行渲染的 `DataTableRow.ts`。行高测量只读 ResizeObserver 的结果。
    - 账号页 13 个弹窗改为按需加载加按需挂载，新建和编辑弹窗在页面空闲时预取。
    - 新增 chunk 加载失败自动恢复（`router.onError`、`vite:preloadError`、异步组件），10 秒内不重复刷新。
    - 去掉大面积毛玻璃，`transition-all` 都换成只过渡实际会变的属性。
    - `Intl` 实例改为缓存复用；`adminSettings.fetch` 的并发调用合并为一个请求。
    - 页面在后台时，5 处轮询跳过本轮。
    - 拆出 7 个按需加载的依赖分块。
  - **动效**
    - `styles/motion.css` 统一定义时长与缓动变量。
    - 减少动画有两条途径：系统设置，或右上角头像菜单里的开关（`useMotionPreference`）。
    - 页面区块入场（`usePageEntrance`）；弹窗进出场，`BaseDialog` 加了 `appear`。
    - 侧栏指示条在切换页面时滑到新位置。
    - 卡片悬停的阴影改由伪元素承担，只过渡透明度。
    - 仪表盘数字首次显示时滚动到目标值（`AnimatedNumber`），数据插入后区块入场一次。用户仪表盘刷新时不再整页显示加载转圈。
    - 最终复测发现，入场从透明度 0 开始时，Chromium 要等动画结束才记 LCP，冷启动因此晚了约 500ms。改为从 0.4 起步后恢复。
  - **交互**
    - Ctrl/⌘K 命令面板：页面清单取自侧栏，用 combobox/listbox 的 ARIA；其他弹窗打开时不响应快捷键。
    - `v-roving-tabs` 指令为 7 处标签栏补上方向键、Home/End 操作。
    - 手机端 DataTable 加了排序栏。
    - 账号“可调度”开关改为乐观更新：失败时回滚，请求期间返回的列表沿用提交中的值。手机端筛选折叠上游已有，没有重做。
- 修改文件：
  - `frontend/src` 下 42 个已跟踪文件被修改，另新增 17 个文件，主要有：
    - 组件与 composable：`DataTableRow.ts`、`AnimatedNumber.vue`、`CommandPalette.vue`；`useLazyMount`、`useMotionPreference`、`usePageEntrance`、`useSidebarActiveIndicator`、`useCommandPalette`
    - 工具与样式：`chunkLoadError.ts`、`rovingTabs.ts`、`styles/motion.css`
    - 以上相应的测试
  - 配置：`tailwind.config.js`、`vite.config.ts`。
  - 文档：新增 `docs/FRONTEND_UX.md`，并登记到 `docs/README.md` 和 `.gitignore` 白名单；更新本文件。
- 验证：
  - `pnpm run lint:check`、`typecheck` 通过。
  - `test:run` 395 个文件、3080 个用例通过。
  - `pnpm run build`（含 `check:i18n` 与 `vue-tsc -b`）通过，构建产物目录已删除。
  - 在提交后的临时克隆里，`check_docs_consistency.py` 和 `sync.py audit` 通过。
  - 仓库外基准：CPU 降速 4 倍、模拟接口、每组 3 轮，结果见“当前交接状态”。
  - 截图检查了以下内容：亮色和暗色表格的分隔线与固定列、弹窗、命令面板、手机端排序栏、仪表盘；控制台无报错。
- 卡点/风险：
  - 账号页滚动 27 FPS，没达到 50 FPS。
  - 分组页首次进入和新建账号弹窗首开略慢，原因见交接状态。
  - `BaseDialog.vue` 属于 `manual_merge`，下次同步上游时要保留 `appear`。
- 下一步：用户授权后提交并开 PR；合并后按“归档”规则移走本任务的交接小节和进行中条目。若要继续提升账号页滚动，可考虑行内文案预先计算或单元格懒渲染。

### 2026-10-20T03:10Z — 20261019-frontend-ux — 推送并开 PR #112

- 请求/目标：用户授权推送并开 PR。
- 开始状态：分支 `feat/frontend-ux-20261019` 有本地提交 `3a1aa3831`（前端）和 `059a19766`（文档）；此时 main 已前进到 `50055270f`（0.4.2，含 #105、#107、#108）。
- 完成操作：
  - 推送后开 [PR #112](https://github.com/liulixin-lex/xy2api/pull/112)。
  - 把 main 合并进分支，解决两处冲突：
    - `AccountsView.vue`：同时保留 `useLazyMount` 和 #108 的 `useColumnOrder` 引入。
    - 本文件：两边条目都保留，操作日志按时间排序。
- 修改文件：合并结果与本条。
- 验证：
  - 合并后 `lint:check`、`typecheck` 通过。
  - `test:run` 398 个文件、3093 个用例通过。
  - `check_docs_consistency.py`、`sync.py audit` 通过。
- 卡点/风险：CI 的 `backend-security` 失败，与本 PR 无关。原因是 govulncheck 新收录了 excelize v2.11.0 的公告 GO-2026-6688，main 上的 `50055270f` 同样失败，需要另开任务升级依赖。
- 下一步：等待审阅与合并；合并、发版、部署需用户另行授权。

### 2026-10-20T04:59Z — 20261019-readiness-0.4.2 — 0.4.2 正式发布完成

- 请求/目标：用户在本会话直接确认：条件满足就合并开放的 PR 并发版 0.4.2（2026-10-19T17:33Z）；接受浸泡分析、继续发版（2026-10-20T02:19Z）；#110、#112 一并进入 0.4.2，不再浸泡，只验证最终候选（03:11Z）。生产主机与更换密码不在范围内。旁路审查会话在各节点验收，打 tag 前做最终把关。
- 开始状态：main `0320d4e68`；PR #105–#108 开放，#110、#112 在授权后新开；最终浸泡（`0ad63fab7`）在跑。
- 完成操作：
  - 浸泡结论：10/11，存活堆 1.2 MiB/h 未过，按用户决定放行，原结果不改写（tasks“0.4.2”）。
  - `release/0.4.2` 依次 `--no-ff` 合入固定头的 #106、#105、#107、#108 → PR #109 常规 merge `50055270f`（候选 `368b418ef` 场景 55/55、升级演练通过）。
  - main 的安全扫描因 excelize 新公告 GO-2026-6688、GO-2026-6684 失败：PR #111 把 excelize 升到上游修复提交，`x/image` 随之升到 v0.44.0 → `9e3b25a14`。
  - #110、#112 分别先合入 main（不 rebase、不强推）再常规合并 → `7a0bafa2c`、`6e0ad2f3e`。
  - 最终候选 `6e0ad2f3e`：CI 与安全扫描全绿，场景 55/55，0.4.1 升级演练通过，接口与浏览器冒烟、XLSX 导出、头像上传通过。旁路审查最终把关通过，并要求在发版说明里写明浸泡未过的那一项，已照办。
  - 打 annotated `v0.4.2` → `6e0ad2f3e`，Release 核验与全新安装见“当前交接状态”。
- 修改文件：本收尾只改本文件、归档、tasks、staging-validation §8 与 `docs/README.md` 的状态列。
- 验证：见上；本收尾为纯文档，跑文档一致性检查、`sync.py audit`、`tools/tests`。
- 卡点/风险：浸泡的存活堆一项未过，由用户放行；兼容转换写出开场事件之后不再换号等已知限制见 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md) §7。
- 下一步：见“当前交接状态”中 0.4.2 的遗留与去向。

### 2026-10-20T08:41Z — 20261020-resilience-0.4.3 — 0.4.3 正式发布完成

- 请求/目标：0.4.2 生产只读观测报告里要改代码的项。用户经旁路审查会话转达，“执行，p1 和 p2，其中 p1-1 暂时不考虑”，并在本会话直接确认按 0.4.2 的流程发 0.4.3（2026-10-20），生产不部署。本会话不碰任何主机；主机上的 Caddy 改动由另一位执行者完成。
- 开始状态：main `a6d28756f`（0.4.2 收尾后），工作树干净。
- 完成操作：
  - 分支 `fix/resilience-0.4.3-20261020`，共 6 个提交（`36b26dfc8`…`972a2211c`）。每项先写失败用例，确认修复前失败。
  - PR #114，18/18 通过。测试服务器候选 `972a2211c`：场景 56/56，两条升级演练通过。
  - 旁路审查在合并前、打 tag 前各把关一次；它要求发版说明去掉本站部署情况、去掉内部编号，已照办。
  - 常规 merge 为 `888f49db8`，main 上的 CI 与安全扫描全绿。annotated `v0.4.3` 指向它，Release 核验与全新安装见“当前交接状态”。
- 修改文件：见上述提交。本收尾只改本文件、归档、tasks“0.4.3”和 `deploy/README.md`：补了一句 systemd 需要 `Restart=always`，以及按 runbook 配置的反代在排空期间的应答。
- 验证：见上。本收尾为纯文档，跑了文档一致性检查、`sync.py audit` 和 `tools/tests`。
- 卡点/风险：等待 CI 的脚本在 zsh 下不拆分 `$ids`，空转到时限被停，改用 bash 脚本。macOS 自带的 bash 3.2 没有 `mapfile`。
- 下一步：见“当前交接状态”中 0.4.3 的遗留与去向。

### 2026-10-20T12:20Z — `20261020-share-simplify` — 只读分析完成，开始实施

- 请求/目标：用户要求在保留必要功能的前提下精简份额调度（设置、界面与模块），允许打破现有框架。先由多个 agent 做只读分析，再由用户决定方案。
- 开始状态：origin/main `3092629d7`（0.4.3 加文档）；分析期间未改仓库。
- 完成操作：
  - 只读分析，报告在仓库外 `xy2api-artifacts/share-simplify-20261020/`：
    - A 后端审计：设置与模块清单、数据模型替代方案、原版调度对比，以及对 PM 方案的复核；
    - B 前端交互审计（含截图）；
    - C 生产只读分析：管理员的真实用法、账号池结构、影子数据；
    - D PM 综合；
    - E 实施说明。
  - 用户决定按 D 的推荐执行（核心方向、删除排空、删除影子、不浸泡），授权到 PR。
  - 建立分支 `feat/share-simplify-20261020`，提交 OpenSpec 变更与本登记。
- 修改文件：`openspec/changes/simplify-share-scheduling-20261020/`（新增）、`docs/README.md`、`tools/upstream-sync/policy.json`（`xy_owned`）、本文件。
- 验证：`check_docs_consistency.py`。
- 卡点/风险：见顶部同名小节。
- 下一步：等 PM 确认契约；其间先做不影响契约的后端语义改动。

### 2026-10-20T15:37Z — `20261020-share-simplify` — 实现与本机联调完成，门禁全绿，待独立审查

- 请求/目标：按 E 实施说明与 PM 的逐层批准完成后端、并入前端、本机联调、文档与登记，跑全部门禁后交 PM 安排独立审查。
- 开始状态：分支 `feat/share-simplify-20261020`（自 origin/main `3092629d7`），契约提交 `47d48d6bc` 之后。
- 完成操作：
  - 后端：语义层（`4e0a34b6e`）、韧性关闭回退修复（`afc6e7153`）、接口重写与模块删除（`b5a2242db`）、计数精简（`5e1447c7e`）、自适应权重（`6d8ba41af`）、原版组按需健康系数（`b9b0232ce`）、切换后立即生效的健康系数与因子取整（`70bb80673`）。
  - 前端分支两次 `--no-ff` 并入（`6927438d0`、`2e3f7f424`）；文档与登记（`a3a13acfc`）；`policy.json` 重复键修复与 `sync.py` 严格解码（`97b3fbcc3`）。
  - 本机联调：本地 PG 18 与 Redis（只监听本机），种子与脚本在仓库外；结束后停进程、删临时库与 Redis 第 2 库、删临时凭据，停掉本地实例并删除数据目录。
- 修改文件：相对 origin/main 共 107 个文件，+9418 −7327（本条记录之前）。后端产品 +2488 −2399，后端测试 +2533 −1344，前端产品 +2417 −2890，前端测试 +1028 −596，文档与 OpenSpec +840 −94，工具 +112 −4。
- 验证：上面“当前交接状态”所列全部门禁在干净树上通过。
- 卡点/风险：见顶部同名小节与 design §10。
- 下一步：等独立审查；审查通过并经 PM 通知后推送开 PR，不合并、不打 tag。

### 2026-10-20T16:32Z — `20261020-share-simplify` — 独立审查修复完成，门禁全绿，待复核

- 请求/目标：独立审查（仓库外报告 H，头 `684d83f5e`）结论“需修复后推送”，无 P0。PM 分工：后端修 P1-1 的后端部分、P1-2、P1-3 与 P2-1、P2-2、P2-4、P2-9 的爬升触发；前端在自己的分支修其余前端项；`reenabled` 采样粒度登记后续。
- 开始状态：`684d83f5e`，工作树干净。
- 完成操作：
  - `530f94753`：内核 `Reason.ActiveMigration`（`realign_cold`、`return_cold`；`spill_rebind` 与已退役原因不算主动），报表 `hot_migrations.active`；从切换点截断的窗口精确读取归因计数；`"affinity": null` 的空值处理；健康失败查询 `ORDER BY 6 DESC, 1, 2, 3, 4, 5`。每项先写失败用例并确认修复前失败。
  - `02fb8fe41`：proposal、design 的生产账号号码、解码夹具里的生产账号号码、`GATEWAY_RESILIENCE.md` 的第三种爬升触发、tasks 4.5、5.2、6.4 与 §8。
  - 前端 `8746c1951` 以 `--no-ff` 并入（`92d7c13c6`）。
  - 全分支扫描生产编号与主机信息：除上述两处外没有命中。
- 修改文件：见上述提交。
- 验证：
  - 在 `92d7c13c6` 的干净树上重跑：gofmt、四种 vet、golangci-lint、`make generate` 零 diff、`-tags=unit` 与无 tag 测试、`ci_gate.py`（282 个，0 SKIP）、`-tags embed` 构建、前端四项（3147 个用例）、`tools/tests`、scheduler-diff、文档一致性检查，全部通过。`sync.py audit` 在记忆提交后的干净树上运行。
  - 本机没有 Docker，集成测试在本机跳过。失败查询的排序在临时的本机 PG 18 上验证，用完即删。
- 卡点/风险：见顶部同名小节。
- 下一步：等审查 agent 复核；复核通过并经 PM 通知后推送开 PR，不合并、不打 tag。

### 2026-10-20T16:42Z — `20261020-share-simplify` — 旧策略字段名登记为退役，并入前端精度说明

- 请求/目标：PM 审核通过 `7dd363a66`，同意两处口径（`spill_rebind` 算被动、已退役原因只计 `total`）与夹具号码的改动；要求补审查 P2-9 第二点，并入前端 `061f8fea7` 后重跑。
- 完成操作：
  - `592f67c2b`：`retired` 新增一条，登记 `soft_spill_max_seconds`、`ms_per_1k_tokens`、`return_deadline_seconds`、`stable_seconds`、`on_spill`、`on_saturated`、`on_failed`、`min_volume`、`cache_domain`、`drain_until` 与 `"realign"`。只在 `sharesched/policy.go`、`policy_test.go`、`compat043/`、两个份额变更、网关韧性 design 与记忆中放行。
  - `correction`、`window_minutes`、`ttl_seconds`、`max_ms`、`max_waiting`、`drain` 在别处仍在用，不登记。
  - 仍点名旧字段的 5 处注释改写（`resilience_session.go`、`share_scheduling_exec.go`、`share_scheduling_service.go`、`share_scheduling_resilience_test.go`、`tools/share-scheduling/baseline.sql`）。
  - 前端 `061f8fea7` 以 `--no-ff` 并入（`400ab364e`）。
- 修改文件：见上述提交。
- 验证：
  - 规则在基线 `3092629d7` 上命中放行范围外 20 个文件、103 处（旧编辑器、接口类型、内核与服务），在当前树上为 0。
  - 本轮后端只改了注释，`internal/service` 的 vet、lint 与份额用例通过；完整的后端门禁沿用 `92d7c13c6` 的结果。
  - 在 `400ab364e` 上重跑前端四项（408 个文件、3148 个用例），以及 `tools/tests`、scheduler-diff、文档一致性检查，全部通过；`sync.py audit` 在记忆提交后的干净树上运行。
- 下一步：等审查 agent 复核；复核通过并经 PM 通知后推送开 PR。

### 2026-10-20T17:19Z — `20261020-share-simplify` — 复核通过，推送并开 Draft PR #117，CI 全绿

- 请求/目标：独立审查复核结论“可以推送”。PM 要求推送、开 Draft PR，等 CI，再把记忆作为追加提交推送。授权范围到 PR，不合并、不打 tag、不发版、不连任何服务器。
- 开始状态：`c0579a624`，工作树干净，提交身份为 noreply。
- 完成操作：
  - 推送前扫描待推送提交的新增行与提交说明：各任务私有清单里的主机、域名、路径 0 命中；IP、绝对路径、私钥、令牌类模式 0 命中。
  - 按规定的凭据方式推送 `feat/share-simplify-20261020`，`ls-remote` 核对远端头为 `c0579a624`。
  - 开 Draft [PR #117](https://github.com/liulixin-lex/xy2api/pull/117)（base `main`，可合并）。描述写明目标与新模型、删除与保留、R1–R3 的口径与失效条件、兼容与回退、升级注意、验证与未做项、文档链接与后续事项。
- 验证：CI（push 与 pull_request）、Security Scan（push 与 pull_request）共 18 项全部通过。`test` 作业的 unit 与 integration 都通过，integration 在真实容器里跑了 `internal/repository`。
- 修改文件：本文件（追加提交）。
- 下一步：等 PM 决定合并与发版步骤；测试服务器场景、升级演练与浸泡需要另行授权。

### 2026-10-20T18:05Z — `20261020-share-simplify` — PR #117 的 CI 说明

- 记忆提交 `e03e9e188` 的 push 运行（CI run 39465158255）中，`shell` 作业在 macOS 运行机上排队未被领取，GitHub 以容量不足取消，未执行任何步骤；重跑一次同样被取消。
- 同一提交的 pull_request 运行（39465165734）全部通过，含 `shell`。两次 Security Scan 也都通过。
- PM 决定以 pull_request 那次为准，不再重跑。PR #117 已发评论说明，合并前需重跑该作业并确认通过。
- 下一步：等 PM 决定合并与发版步骤。工作树保留。

### 2026-10-20T14:29Z — 20261020-lazy-modal-first-open — 已修复，待授权提交

- 请求/目标：账号页“测试连接”的模型、“临时不可调度”的原因等，第一次点开刷不出来，第二次才行。用户要求拉取最新代码，分析所有同类问题的根因并修复。
- 开始状态：main `3092629d7`（0.4.3，#112 已合并），新分支 `fix/lazy-modal-first-open-20261020`，工作树干净。
- 完成操作：
  - 定位：AccountsView 用 `useLazyMount` 加 `defineLazyComponent` 按需挂载 12 个弹窗。这些弹窗都用不带 `immediate` 的 `watch(() => props.show)` 加载数据：
    - TempUnschedStatusModal 监听 `[show, account.id]`；
    - AccountTestModal、AccountStatsModal、ScheduledTestsPanel 等监听 `show`。
    - IQCheckResultsModal 已带 `immediate`，不受影响。
  - 排查其他用 `v-if` 挂载、带打开状态属性的组件：ClaudeResetCreditsCell、OpenAIReferralCell、CodexAccountTicketSettings、BulkSubscriptionActionDialog、AffiliateOfflineWithdrawDialog、LoginAgreementPrompt 及 BaseDialog 自身，都已用 `immediate` 或挂载时为关闭状态，不需要改。
  - 修复：`defineLazyComponent` 不再直接用 `defineAsyncComponent`，改为自行加载并缓存组件。内层组件第一次渲染时把打开状态属性（默认 `show`，可用第二个参数指定）传为 false，`onVnodeMounted` 之后再传真实值。chunk 加载失败时仍会自动刷新页面。
  - 现有弹窗测试都是先以关闭状态挂载再打开，没覆盖到这种情况。`FRONTEND_UX.md` 已补写这条约定和测试要求。
- 修改文件：
  - `frontend/src/composables/useLazyMount.ts`
  - 新增 `frontend/src/composables/__tests__/defineLazyComponent.spec.ts`
  - `docs/FRONTEND_UX.md`
  - 本文件
- 验证：
  - 新用例在旧实现下 3 个全失败，修复后 3 个全通过。
  - `lint:check`、`typecheck`、`test:run`（402 个文件、3104 个用例）、`check_docs_consistency.py` 通过。
  - 仓库外浏览器脚本对比修复前后，修复前第一次打开时：
    - 测试连接：0 次模型请求，下拉框为空。
    - 临时不可调度：0 次状态请求，不显示原因。
    - 账号统计：0 次请求。
  - 修复后第一次打开就都有请求和数据。
  - `sync.py audit` 需要干净的工作树，提交后再跑。
- 卡点/风险：无。
- 下一步：用户授权后提交、推送、开 PR；随下一个版本发布后生产才生效。

### 2026-10-21T02:26Z — `20261021-resilience-v2-qa` — 阶段 1 完成（DOCS 代记）

- 请求/目标：0.4.4 冲刺的 QA 阶段 1，补新故障与测试服务器场景，并在基线上确认它们的结果。
- 开始状态：分支 `rv2/qa`，自 `027a434ad`，工作树干净。
- 完成操作：
  - 新故障 F31–F37 与 F34B，新场景 CV-5–CV-9、RAW-1、RAW-2、SO-1、RA-1、RA-2、ANT-3、SH-1、SH-2；DC-6 改为断言兼容转换的开场延迟，LR-1 改为份额层级取账号优先级。合计 69 个场景。
  - CV-5 按 PM 指令保留 D18：A 最多试 2 次、每次记一条 `CAPACITY_SHED`，最终在 B 完成。
  - 基线结果：15 项定向场景 8 项通过，7 项为预期红灯（CV-6、CV-8、RAW-1、RAW-2、SO-1、RA-1、DC-6）。CV-8 复现了“`response.created` 之前的无标记空 delta 让后续失败无法换号”。
- 修改文件：`tools/gateway-resilience/` 下 5 个文件（提交 `ce8c3c01d`）；场景清单登记在 `gateway-resilience-20261015/staging-validation.md` §6.1（DOCS）。
- 验证：工具全包 `-race`、Python 编译、文档一致性检查通过；场景在测试服务器的基线上运行。
- 卡点/风险：无。
- 下一步：阶段 2（唯一候选的全套场景、升级演练、3 小时浸泡）等 PM 指定提交放行。

### 2026-10-21T02:56Z — `20261021-resilience-v2-qa` — 阶段 1 补充完成（DOCS 代记）

- 请求/目标：PM 指令 3，补排队 ping 后终止帧与 Codex `Retry-After` 上限的测试服务器场景。
- 开始状态：`rv2/qa` 在 `ce8c3c01d`，工作树干净。
- 完成操作：新增 Q-1（真实用户队列 ping 后余额归零：基线上 Gateway Responses 与 Chat 写裸 JSON，Messages 已是带名字的 error）与 CX-1（`originator` 与 UA 两种 Codex 标记在基线都是 30 秒，普通客户端对照不变）。合计 71 个场景。
- 修改文件：`tools/gateway-resilience/` 下 3 个文件（提交 `3320a508a`）；场景清单在 `gateway-resilience-20261015/staging-validation.md` §6.1（DOCS）。
- 验证：Python 编译、文档一致性与 diff 检查通过；测试用的专用用户已恢复。基线 17 项定向 8 项通过、9 项预期红灯。
- 卡点/风险：无。
- 下一步：阶段 2 等 PM 指定提交放行，候选跑全部 71 项。

### 2026-10-21T03:08Z — `20261021-resilience-v2-qa` — Q-1 心跳断言补充（DOCS 代记）

- 请求/目标：PM 指令 4，Q-1 要能发现 Responses 与 Chat 在开场前收到 Claude 风格 ping 的问题（审查 R1-P1-4）。
- 开始状态：`rv2/qa` 在 `3320a508a`，工作树干净。
- 完成操作：Q-1 新增断言，Responses 与 Chat 的第一个 data 帧必须是 `response.created` 或 Chat 分块/错误，前面不能有 typed ping 或 `[DONE]`；Messages 仍允许 typed `ping`。按 PM 许可只复核基线的原始证据，没有重发请求：两条新断言为红灯，Messages 原有检查通过。
- 修改文件：`tools/gateway-resilience/` 下 2 个文件（提交 `236b12b97`）；仓库外报告 `REPORT_PHASE1.md` 追加更正与证据。
- 验证：Python 编译、文档一致性与 diff 检查通过；专用用户已恢复。
- 卡点/风险：无。
- 下一步：待命；阶段 2 等 PM 指定提交放行。

### 2026-10-21T03:35Z — `20261021-resilience-v2-qa` — 阶段 2 准备完成（DOCS 代记）

- 请求/目标：PM 指令 5，在放行前准备好候选验证的工具。
- 开始状态：`rv2/qa` 在 `236b12b97`，工作树干净。
- 完成操作：
  - 新增候选构建与阶段 2 执行脚本：只构建一个候选，记录源码树、镜像 ID、二进制与工具的 SHA-256；缺省只出计划、不自动部署；71 个场景一条命令串行，输出 JSON 汇总。
  - 升级演练增加本期的配置并在失败时留证；浸泡在缺 GC 或协程数据、SQL 失败时直接判为不通过，ERROR 刷屏门槛为连续 3 分钟每分钟 ≥60 条。
  - 测试服务器只读检查资源，余量足够，无需清理；旧基线未重启。
- 修改文件：`tools/gateway-resilience/` 下 5 个文件（提交 `a542b3cb7`）；浸泡判据登记在 `gateway-resilience-20261015/staging-validation.md` §8（DOCS）。
- 验证：本机离线反例、语法与文档检查通过。没有构建候选、没有跑阶段 2。
- 卡点/风险：按估计，06:15Z 才构建的话，浸泡可能晚于 07:30Z 开始，已在仓库外 `REPORT_PREPARATION.md` 提示 PM。
- 下一步：等 PM 放行与候选 SHA。

### 2026-10-21T03:39Z — `20261021-rv2-beta` — 功能与审查修复提交完毕（DOCS 代记）

- 请求/目标：W1.1、W1.2-lite、W5.3/W5.4 的 service 部分、释放事件采集，以及第一轮代码审查（R1）分给 β 的修复。
- 开始状态：`rv2/be-b` 自 `027a434ad`，工作树干净。
- 完成操作：提交清单见“进行中的工作”的 β 条目（HEAD `4eed85c1a`）。PM 改判 U4 后，兼容转换的暂存期改为写注释心跳；created 之前的空 delta 窗口在第一个非空 delta 时关闭；service 侧三个失败帧合成点接入 `ResolveRetryAdvice`；仅流式检测在 HTTP 400 的任何副作用之前调用。
- 修改文件：`backend/internal/` 下 β 名下的文件，以及声明接手的 `openai_compact_sse_keepalive.go`、`openai_gateway_cc_pipeline.go` 与一个 WS bridge 测试文件；上游文件钩子列在仓库外 `contract/CHANGES_B.md`，由 α 集成时登记。
- 验证：每项先写失败用例、确认在旧代码上失败；相关包 `-race` 通过。最终的 35 个测试文件、270 个顶层用例 `-race` 与 lint、vet 在跑，结论另记。
- 卡点/风险：OpenAI Responses 透传的首输出守卫延后，已写进 design §11 与网关韧性说明 §7。
- 下一步：α 集成时须包含 α 的 `5924b2b5a`（handler 取消归因）与 γ 的 `4ac81dc94`（限流分类更正）。

### 2026-10-21T04:05Z — `20261021-resilience-v2-alpha` — 功能与审查项完成，准备集成（DOCS 代记）

- 请求/目标：RetryAdvice、W5.1–W5.5、W8-lite、W6 的入口标记与管理接口，以及第一轮代码审查分给 α 的修复；04:45Z 起负责集成。
- 开始状态：`rv2/be-a` 自 `027a434ad`，工作树干净。
- 完成操作：提交清单见“进行中的工作”的 α 条目。R1 的 P1-1（handler 取消归因）、P1-4（排队心跳格式）、P1-5（事件请求 ID）与 P2-9、P2-11、P2-12、P2-13、P2-15、A-P2-3/7、A-P2-5 已修；P2-10、P2-14 本期不做；P2-16 按 PM 决定只登记。
- 修改文件：`backend/internal/handler/**`、`internal/resilience` 中 α 名下的文件，以及声明接手的 `routes/admin.go`（清除路由）、`resilience_explain.go`、`share_scheduling_openai.go`（能力原因与份额无号接线）；上游文件钩子列在仓库外 `contract/CHANGES_A.md`，集成时登记到 `policy.json`。
- 验证：每项先红后绿，相关包定向 `-race` 通过；增量 lint 收尾中。**全量门禁尚未执行。**
- 卡点/风险：后端分支间的 cherry-pick 冲突需在集成时按 owner 最新版本解决。
- 下一步：β、γ 写明最终 HEAD 后开始集成，跑全量门禁。

### 2026-10-21T04:05Z — `20261021-resilience-v2-gamma` — 分支冻结（DOCS 代记）

- 请求/目标：C0 设置、W2.1-lite 分类修正、W6-lite 仅流式学习、RetryAdvice 的分组 advisor，以及 PM 指令分给 γ 的第一轮审查修复。
- 开始状态：`rv2/be-c` 自 `027a434ad`，工作树干净。
- 完成操作：提交清单见“进行中的工作”的 γ 条目（HEAD `ae783b370`）。C0 沿用单一设置键，0.4.3 回退安全；分类只把真正的容量信号算作降载，明确的限流文案保留原类别；仅流式学习存于账号 `extra` 的两个键，带凭据指纹，只在同协议、流模式跟随客户端的路径上过滤，只因能力而无号时返回专用 503；advisor 只读 2 秒的本地快照。
- 修改文件：`backend/internal/resilience` 与 `service` 中 γ 名下的文件（含新增的 `capability*.go`、`settings_compat043_test.go`）；调度文件的钩子 `capabilityEligibilityReason` 等由 α 集成时登记到 `scheduler-diff.json` 与 `policy.json`。
- 验证：各轮先红后绿；resilience、service、handler、admin、repository 的定向 `-race` 通过，覆盖 Grok 的 A 返回 400、换 B 成功，之后的过滤与流式可用，有效期与清除，旧页面保存时保留学习状态，回退设置，分类证据，纯能力与混合原因的耗尽。γ 树上的文档检查报 10 项，由 rv2/docs 补齐，合入后才通过。
- 卡点/风险：无。按 PM 裁剪的未做项见上。
- 下一步：α 统一集成、跑全量门禁并登记钩子。

### 2026-10-21T04:10Z — `20261021-resilience-v2-qa` — 独立并行浸泡准备完成（DOCS 代记）

- 请求/目标：PM 指令 6，让 3 小时浸泡与 71 个场景并行，缩短阶段 2 的总时长。
- 开始状态：`rv2/qa` 在 `a542b3cb7`，工作树干净。
- 完成操作：新增两套 Compose 的资源上限配置与独立的浸泡栈脚本；浸泡与夹具按 Compose 项目与端口隔离；两套用同一镜像与二进制并在前后校验；日志连续留证并检查应用是否重启；场景主线仍是 71 个串行后接升级演练，升级时的临时应用限制资源，IO 重叠时段写入浸泡报告；清理只动独立的那一套。
- 修改文件：`tools/gateway-resilience/` 下 8 个文件（提交 `9ca7c3347`）；判据说明在 `gateway-resilience-20261015/staging-validation.md` §8（DOCS）。
- 验证：本机离线反例、语法与文档检查通过；测试服务器只读核对端口与子网无冲突。第二套环境未创建任何 Docker 资源，原有容器未动，未构建候选。
- 卡点/风险：主机内存余量有限，启动前须再查，不足时脚本拒绝启动。
- 下一步：等 PM 放行与候选 SHA；按仓库外 `REPORT_PARALLEL_PREPARATION.md` 的排期执行。

### 2026-10-21T04:12Z — `20261021-rv2-beta` — 分支冻结（DOCS 代记）

- 请求/目标：完成 β 的验证收尾并冻结分支，交 α 集成。
- 开始状态：`rv2/be-b` 在 `4eed85c1a`。
- 完成操作：收尾提交 `7c8ad6ec2`：两条旧测试改为显式验证开关关闭时的旧行为，修正自有 lint，WS 回归测试改用独立夹具以兼容无 tag 构建。生产代码在 645 项验证之后没有再改。
- 修改文件：只有测试与 lint 修正（见上述提交）。
- 验证：645 项 `-tags=unit -race` 0 失败 0 跳过；WS 与 Chat 空流的无 tag `-race`、`go vet`、gofmt、`diff --check` 通过。DOCS 把 `7c8ad6ec2` 的全部后端文件叠加到 rv2/docs 上，文档一致性检查通过。
- 卡点/风险：OpenAI Responses 透传的首输出守卫延后；上游在 `response.created` 之前发出的非空 delta 照常转发，严格 SDK 仍可能报错（已知限制）。
- 下一步：α 集成时保留 α 的 `5924b2b5a` 与 γ 最新的分类、能力提交，不用 β 树里提取的旧依赖覆盖。

### 2026-10-21T04:22Z — `20261021-resilience-v2-gamma` — 冻结后 lint 修复（DOCS 代记）

- 请求/目标：PM 指令 3，修 α 集成前报出的 7 项 lint，不改行为。
- 开始状态：`rv2/be-c` 在 `ae783b370`，工作树干净。
- 完成操作：提交 `d61d106aa`，7 项 lint 全部修复；冻结的 0.4.3 旧解码器测试未改，`legacy043SettingKey` 由既有回退测试断言与现行键一致后使用。
- 修改文件：γ 名下的 `internal/resilience` 与 `service` 测试及 `capability` 实现文件（只为 lint）。
- 验证：指定的增量 lint，γ 自有文件 0 项；resilience 与 service 的能力、C0 定向 `-race` 通过。γ 树上的文档检查仍报 10 项，由 rv2/docs 补齐。
- 卡点/风险：剩 α 名下 `retry_advice_test.go` 两处 SA1012。
- 下一步：按 PM 指令 4 另作 fix 提交（`param` 为 null 或空值时照常学习）；α 集成须带上 `d61d106aa` 与该补丁。

### 2026-10-21T04:25Z — `20261021-resilience-v2-qa` — F36 显式 `param: null`（DOCS 代记）

- 请求/目标：PM 04:20Z 补充，让 SO-1 覆盖错误体带 `"param": null` 的常见 OpenAI 兼容形态（对应 γ 的 PM 指令 4 修复）。
- 开始状态：`rv2/qa` 在 `9ca7c3347`，工作树干净。
- 完成操作：F36 的非流式 400 错误体加入 `"param": null`，type、code、message 不变；流式成功、能力探测与 Anthropic 错误格式不变。SO-1 已用 F36，不新增场景编号。
- 修改文件：`tools/gateway-resilience/` 下 README、`fault-upstream/main.go` 与 `resilience_v2_test.go`（提交 `0ad64390c`）。
- 验证：夹具测试先校验 `error.param` 存在且为 null（先红后绿），再验证流式成功与全部能力探测；gofmt、文档与 diff 检查通过。本轮未连接测试服务器，基线不重跑。
- 卡点/风险：测试服务器上仍是旧夹具与旧应用；阶段 2 启动前须同步本提交并重建故障工具镜像。
- 下一步：等 PM 指定候选 SHA 并放行阶段 2。

### 2026-10-21T04:29Z — `20261021-resilience-v2-gamma` — PM4 修复，恢复冻结（DOCS 代记）

- 请求/目标：PM 指令 4（第二轮审查 P1，用户确认授权）：上游错误体带 `"param": null` 或空值时，仅流式学习被误判为“指向其他参数”而拒学。
- 开始状态：`rv2/be-c` 在 `d61d106aa`。
- 完成操作：提交 `fe7d6cbbc`。`param` 缺失、为 null、空字符串或只有空白时视为未指定参数，继续做确定性判断；`param` 为 `stream` 时允许学习；其他非空参数名以及对象、数字、数组、布尔值仍拒学。设置、接口、错误码、事件字段与上游钩子都没有变化。
- 修改文件：`internal/resilience/capability.go` 与 `service` 中的能力检测及其测试。
- 验证：先写红测，旧实现对 null、空串、空白三例拒学（失败），`reasoning.effort` 与非字符串拒学的反例通过；修复后定向 `-race` 23 个顶层用例（含子用例 79 项）0 失败 0 跳过；γ 自有文件增量 lint 0 项。DOCS 已把新规则写进 design §6.1、网关韧性说明 §7 与规格。
- 卡点/风险：无。
- 下一步：α 集成时依次带上 `d61d106aa`、`fe7d6cbbc`；阶段 2 的 SO-1（F36 已带 `"param": null`）验证这项修复。

### 2026-10-21T04:46Z — `20261021-rv2-beta` — 第二轮审查修复，恢复冻结（DOCS 代记）

- 请求/目标：PM 指令 6，第二轮审查（R2）分给 β 的两项 P1，冻结后的例外。
- 开始状态：`rv2/be-b` 在 `7c8ad6ec2`，工作树干净。
- 完成操作：
  - R2-P1③（`a1a2ff211`）：raw Chat 严格模式在第一条 data 之前只暂存 event、id、retry 行，上游注释心跳立即写出并计为心跳字节，不关闭换号窗口。
  - R2-P1②（`2323768d9`）：service 的三个失败帧合成点共用 α 导出的 `RetryAdviceEligible`；不可重试的原因先写 `Suppress`（`non_retryable`）再求值，invalid_request、model_not_found 等不再带重试头与尾句。
- 修改文件：`openai_gateway_chat_completions_raw.go`、`resilience_keepalive.go` 与共用的失败帧构造及测试；上游钩子 `recordOpenAIStreamKeepaliveBytes`（raw）、`RetryAdviceEligible`（keepalive）由 α 集成时登记。
- 验证：两项先红后绿；raw 普通与大请求两例、service 54 例（30 例在旧代码失败）与相关回归 `-race` 通过；增量 lint 0 项。DOCS 叠加 `2323768d9` 的全部后端文件到 rv2/docs，文档一致性检查通过。
- 卡点/风险：无。
- 下一步：α 增量合入后，与 DOCS 最终 HEAD 一起跑一次全量门禁。

### 2026-10-21T05:11Z — `20261021-rv2-fe` — 前端完成（DOCS 代记）

- 请求/目标：0.4.4 新开关的设置界面与 i18n、账号仅流式学习状态的显示与清除、Explain 的“已学能力”列与“请求类型”、运维面板的 `pre_created_delta` 文案。
- 开始状态：`rv2/fe` 自 `027a434ad`，工作树干净。
- 完成操作：15 个提交，只改 `frontend/**`（17 个文件）；文案随 C0 与各 CHANGES 多次对齐（原生白名单不含元数据事件、Codex 上限覆盖配置拒绝、仅流式只影响会因此失败的非流式请求、`decision_v2` 关闭后的说明）。
- 修改文件：`api/admin/resilience.ts`、新增的 `api/admin/accountCapability.ts`、`utils/accountCapability.ts`、`AccountStreamOnlyBadge.vue`，以及 `ResilienceExplainResult.vue`、`OpsResilienceExplain.vue`、`GatewayResilienceSettings.vue`、中英文 i18n 与测试；上游文件 `AccountsView.vue` 两处钩子登记为具名补丁 `xy-account-stream-only-learning`。
- 验证：HEAD `bd215aa0c` 上 lint:check、typecheck、test:run（413 个文件、3182 个用例）、build（含 i18n 检查）通过；集成 `27cef7db0` 上的前端四项门禁通过。
- 卡点/风险：无。本机 pnpm 版本的坑已记入“操作陷阱”。
- 下一步：无；推送、PR 与发版由 α 与 PM 安排。

### 2026-10-21T05:35Z — `20261021-rv2-beta`、`20261021-resilience-v2-alpha` — 集成门禁期间的 PM7 修复（DOCS 代记）

- 请求/目标：全量 unit 中 service 的两条 MissingTerminal 用例失败；PM7 允许 β 在冻结后修复，并要求保留开关关闭时的旧语义断言。
- 开始状态：β `2323768d9`、α `fc244028a` 之后的集成 `093241b50`。
- 完成操作：旧的“客户端断开”用例只发 `response.created`，在默认暂存下根本没有写出，所以没有真正断开；按默认暂存补真实写失败后，新用例确认产品缺陷（暂存期心跳写失败后首输出截止到期仍换号 504，Chat EOF 结果缺断开标记）。β `766a54fc8`、`dbcc1c80a`、`c7290e396`，α `32f2a6ca7`（handler 出口）。收尾时又发现并修正两处：先查断开会跳过首输出守卫的停表；取消回调与返回清理的竞态会让上游不被关闭。
- 修改文件：β 的 Chat、Messages 转换器与 `openai_compat_model_test.go`（β 按 PM7 声明接手）、暂存测试；α 的 `openai_chat_completions.go` 与 `openai_compat_client_cancel_test.go`；文档同步 design §3.4 与 §9、`GATEWAY_RESILIENCE.md` §6、tasks 2.5。
- 验证：见“进行中的工作”的 β、α 条目；集成的全量门禁需在 `7c021b6a4` 之后重跑。
- 卡点/风险：这是冻结后的产品代码改动，不能沿用此前“只改测试”的门禁结论。
- 下一步：α 重跑后端静态检查与全量门禁，再合入 rv2/docs 最终 HEAD。

### 2026-10-21T05:55Z — `20261021-resilience-v2-alpha` — 代码门禁全绿；第二轮审查放行（DOCS 代记）

- 请求/目标：在 PM7 产品代码改动之后，对集成代码重跑全部后端门禁。
- 开始状态：集成 `7c021b6a4`，已含 β `c7290e396` 及前置、α `32f2a6ca7`。
- 完成操作：按序重跑 gofmt、四种 vet、golangci-lint、全量 unit、无 tag 全量测试与 `ci_gate.py`；前端与 `tools/tests` 的源码未再改，沿用此前通过的结果。第二轮独立审查对最终代码放行。
- 修改文件：无（α 只跑门禁）；DOCS 同步 tasks 10.2、10.3，design §9 与 `GATEWAY_RESILIENCE.md` §6 补 R2-P2-3（raw Chat 取消后的截断，β `f9edc3a4d`，不受开关控制）。
- 验证：`ci_gate` 375 项 `-race` 0 跳过，lint 0 项；明细见“进行中的工作”的 α 条目。
- 卡点/风险：文档检查、scheduler-diff 与干净树 audit 要在合入 DOCS 最终 HEAD 后复跑，本条不记为已通过。
- 下一步：α 报告可推送后等 PM 安排推送、PR 与 QA 候选验证；DOCS 到 QA 验证结束前不再改动 rv2/docs。

### 2026-10-21T06:04Z — `20261021-resilience-v2-alpha` — Go 1.27.3 安全升级（PM 冻结例外）

- 请求/目标：修复 PR #118 安全扫描发现的 `GO-2026-6809`；PM 06:07Z 授权 α 单独跨所有权原子更新全部版本固定位置，不增加审计例外。
- 开始状态：集成 `c086f4cc6`，本地门禁通过、push 与 PR 安全扫描失败；旧候选作废，尚未进行场景、升级演练或浸泡。
- 完成操作：官方 darwin/arm64 安装包 SHA256 校验通过，切换本机 Go 1.27.3 并保留旧工具链；确认 Docker Hub 的 `golang:1.27.3` 与 `golang:1.27.3-alpine` 标签存在；统一 go.mod、三个工作流、三个 Dockerfile、README 三语、开发指南与两处当前工具链记录。历史同步记录和测试中的固定版本夹具保留。
- 修改范围：只改工具链版本及相关文档；无业务代码、模块依赖图、迁移或配置字段变化。FE 的 `follow-redirects` 1.16.1 修复另由其分支提供。
- 验证：官方安装包与镜像标签已核验，`go version` 为 1.27.3，文档一致性检查通过；全量门禁与 `govulncheck` 将在合入 FE 修复后的干净集成树串行执行。
- 卡点/风险：旧候选的测试结果不替代新工具链验证；新候选必须等安全扫描与完整门禁通过，之后更新 PR #118 并由 PM 安排复审及 QA。
- 下一步：合入 FE 安全修复，重跑完整门禁、构建与两端安全扫描，记录新候选 SHA；不合并、不部署。

### 2026-10-21T11:00Z — `20261021-resilience-v2-0.4.4` — 0.4.4 发布，记忆收尾与归档（DOCS 代记）

- 请求/目标：网关韧性第二期与 0.4.4 发版（用户 2026-10-21）；发版后按 PM 2026-10-21T10:50Z“收尾放行”做纯文档收尾。
- 开始状态：main `4b0b6d94b`（PR #118 合并提交）；`docs/post-release-0.4.4` 从它建立。
- 完成操作：
  - 候选：`c086f4cc6` 本地门禁全绿，但 PR 的安全扫描报出 GO-2026-6809 与 follow-redirects 两个高危公告，作废，未做服务器验证；α `8d7a30f27` 升级 Go 1.27.3（PM 授权跨所有权改全部版本固定位置）、FE `60680d3c5` 升级 follow-redirects 1.16.1 后，最终候选 `a38ae5995` 本地 21 项门禁与 CI 18/18 全绿。
  - QA 阶段 2（候选只构建一次）：场景 70/71，RA-1 失败（PM 裁定为已知缺陷，0.4.5 修复）；升级演练 62/62；3 小时浸泡 11/12，存活堆 1.4 MiB/h 未达标，用户决定放行；浸泡夹具的工具修复 `cd72f5eb1`；临时栈与演练库已清理。
  - 发布：PR #118 合并为 `4b0b6d94b`，`v0.4.4` 对象 `333f54ad5`，Release run 39582933562 成功；产物核验第一次因校验脚本的旧远端入口失败，换现行入口重跑通过。
  - 收尾：0.4.4、#117、#116 的交接与登记原样移入归档，主文件留 0.4.4 摘要；登记三项后续（6.5 小时浸泡、pprof 诊断、0.4.5 的 RA-1 修复）；版本速览改为 0.4.4。
- 修改文件：`docs/PROJECT_MEMORY.md`、`docs/PROJECT_MEMORY_ARCHIVE.md`、`docs/README.md`、`docs/GATEWAY_RESILIENCE.md` §7、OpenSpec `gateway-resilience-v2-20261021` 的 design §11 与 tasks；cherry-pick QA 的 `cd72f5eb1`（只改 `tools/gateway-resilience/`）。
- 验证：`check_docs_consistency.py`、干净树 `sync.py audit`、`tools/tests`，结果见收尾 PR。
- 卡点/风险：正式镜像全新安装的结果写入时还没出来，tasks 与摘要里留了占位；生产部署 0.4.4 需要单独授权，并先通过 6.5 小时浸泡。
- 下一步：α 推送收尾分支、开 PR 并在 CI 全绿后合并；全新安装结果出来后补占位。

### 2026-10-21T11:05Z — `20261021-resilience-v2-0.4.4` — 收尾补充：全新安装结果与 0.4.5 预备登记（DOCS 代记）

- 请求/目标：PM 2026-10-21T10:58Z，在收尾分支上补一个提交。
- 开始状态：`docs/post-release-0.4.4` @ `6d0fdf1e3`（PR #119）。
- 完成操作：
  - 正式镜像全新安装通过（QA，按 digest 拉取，空库）：324 条迁移，`/ready`、`/health`、首页、6 个静态资源与 `/auth/me` 正常，公开版本 0.4.4，计费请求写入 1 条正费用，0 ERROR；第一次执行的失败属于安装脚本的夹具问题。替换了 tasks“发布记录”、12.2 与 0.4.4 摘要里的占位；上一条日志所说的占位已补齐。
  - 进行中登记 δ（`feat/w12-gateway-guards-045`）与 ε（`fix/review-p2-045`）两条 0.4.5 本地预备分支；RA-1 条目补审查结论；pprof 条目补诊断分支 `3298f9f9e`。
- 修改文件：`docs/PROJECT_MEMORY.md`、OpenSpec `gateway-resilience-v2-20261021` 的 tasks。
- 验证：`check_docs_consistency.py`、干净树 `sync.py audit`、`tools/tests`。
- 卡点/风险：无新增；生产部署 0.4.4 仍需单独授权并先通过 6.5 小时浸泡。
- 下一步：α 推送并在 CI 全绿后合并 PR #119。

### 2026-10-21T12:20Z — `20261021-channel-monitor-v1` — 只读分析与方案规划完成

- 请求/目标：见“进行中的工作”同名条目。
- 开始状态：主工作树在 `fix/resilience-0.4.3-20261020` @ `972a2211c`（干净，未动）；新分支自 origin/main `4b0b6d94b` 建立，PR #119 合并后快进到 `a1cf8f104`。其他工作树中只有 `045-delta`（δ）有未提交改动，未触碰。
- 完成操作：
  - 梳理 PM 7c768913 与 10 个子代理（只读）：进行中的 δ、ε、RA-1、诊断分支与 QA 浸泡都不碰渠道监控、迁移与并发核心；热点是三个网关 handler、`resilience_*`、`policy.json` 与本文件。
  - 生产只读（会话强制只读、20 秒语句超时）：4 个 v1 监控都经 CDN 打本站入口；统计 7 天 76,397 次探测的结果分布，与同批账号真实用户按 5 分钟对照，推算排队与换号耗时。用户页列表接口 p50 约 0.5 秒；LATERAL 改写的执行计划把时间轴查询从 180 ms 降到 0.87 ms。
  - 代码核实：判定与超时口径、runner 队列语义、流式排队 ping（每 10 秒一次，首次 ping 时写出 200 与 SSE 头）、各条调度等待路径；调研 17 个生态仓库与上游 v0.2.15 之后的监控提交和未合并 PR。
  - 7 天回放：一期规则下黄色占比 Grok 16.0% → 约 1.2%、GPT 约 3% → 0.5%、Claude 3.7% → 0.4%，红色约减半。回放仍用非流式总耗时近似，结果偏保守。
  - 独立审查（只读，对照代码）修正了初稿 6 处：排队 ping 与 CDN 心跳字节相同、无法据此区分排队（删去 `queued`）；官方 OAuth 账号与直连入口没有首包前心跳，不能单设 20 秒响应头超时；本轮结束即释放的租约起不到去重作用；直接复用 v2 分类会把本站 429 和 Cloudflare 502 判成容量类；流内错误识别；pond 有界队列达不到目的。
- 更正：生产运行的是 0.4.3 程序（2026-10-20 09:15Z 在线更新，2026-10-21T11:35Z 只读核实），不是此前记录的 0.4.2；“版本与环境速览”已更新。
- 修改文件：`docs/PROJECT_MEMORY.md`（本条、进行中登记、版本速览的生产版本）。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：方案待用户确认；调度缺陷影响所有请求，不在本任务范围。
- 下一步：见进行中条目。

### 2026-10-21T13:05Z — `20261021-channel-monitor-v1` — 按用户意见重规划（v3）

- 请求/目标：用户 2026-10-21：不要偏离探测本身；红色不要放宽到 60 秒；黄色改为超过 10 秒；探测协议兼容各种模型、不能出错；不显示繁忙、不要“不判红”类分类；不显示数据过期；优化探针让模型更快回答。
- 开始状态：分支 `feat/channel-monitor-v1-20261021` @ `09ecd5396`，工作树干净。
- 完成操作：方案改为 v3（仓库外 `plan/PLAN.md`，前两版另存）。补充生产只读证据：Grok 探测耗时几乎完全由输出 token（含推理）决定，输出 120–250 token 时 p50 3.4 秒，500–1000 token 时 12.3 秒；只把黄色阈值改为 10 秒，7 天回放的黄色占比 Grok 16.0% → 8.9%、GPT 约 3% → 1.3%、Claude 3.7% → 0.7%，其余靠压低推理与读到答案即停。
- 修改文件：`docs/PROJECT_MEMORY.md`（进行中条目与本条）。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：探测选号要改两处原版调度出口（只对探测生效），待用户确认。
- 下一步：见进行中条目。

### 2026-10-21T15:00Z — `20261021-channel-monitor-v1` — 本地实现完成，门禁全绿，待授权推送

- 请求/目标：用户“按你认为的最佳方案执行”方案 v3；取舍：做探测通道（只对签名探测生效），不做本机直连，推理强度取 `low`。
- 开始状态：分支在 `4442128c1`，工作树干净。
- 完成操作：先提交签名头契约；两个子代理在独立工作树并行实现“探针本身”与“探测通道”，协调者实现调度可靠性、读路径、前端与文档；两条分支常规合并；独立代码审查后修复 4 项 P1（手动检测与编辑会让下一轮被跳过、签名头可被第三方重放、MiniMax 式流尾重复 message 误判、quota_probe 超时整轮丢弃）与选定 P2；临时工作树与分支已删除。
- 修改文件：`backend/internal/service/channel_monitor_*`、`monitor_probe_scheduling*.go` 及四个调度文件的调用点、`repository/channel_monitor_repo.go`、`handler/admin/channel_monitor_handler.go`、`pkg/monitorprobe/`、`server/middleware/monitor_probe*.go`、`server/router.go`、前端 3 个文件与 1 个新测试、`docs/CHANNEL_MONITOR_V1.md`（新）、`docs/GROK_MONITOR_RESPONSES.md`、`docs/README.md`、`.gitignore`、`tools/upstream-sync/policy.json`、`scheduler-diff.json`、`UPSTREAM_BASE.json`。
- 验证：见“进行中的工作”同名条目；第一次门禁的 vet 因共享 Go 编译缓存被并发清理而报错（环境问题），重跑通过；`sync.py audit` 第一次因改名的绑定测试失败，修正后通过。
- 卡点/风险：未在真实上游验证；本机磁盘余量紧张（约 9 GiB）。
- 下一步：用户授权后推送并开 PR；测试服务器跑兼容矩阵与响应时间实测；生产部署单独授权。

### 2026-10-21T17:00Z — `20261021-channel-monitor-v1` — 推送 PR #120，CI 修复，本机端到端测试

- 请求/目标：用户“推送PR，并测试”。
- 完成操作：推送分支并开 PR #120；修复 CI 的 lint（测试专用函数移到带 unit tag 的测试文件）与偶发失败用例（`3737a6f5c`）；在本机隔离环境完成 E1–E9 端到端测试（测试专用的回环放行补丁只在临时工作树，未提交，临时工作树与分支已删除）。
- 验证：E1–E9 通过，结果见“进行中的工作”同名条目与 artifacts `e2e/REPORT.md`；本机 golangci-lint 0 项、`sync.py audit` 与文档一致性通过。
- 卡点/风险：真实上游兼容矩阵与 Grok 延迟未测（需要测试服务器或真实 Key）；一次 21.5 秒等待未能复现。
- 下一步：CI 全绿后等待用户决定合并；协调测试服务器做真实上游验证。

### 2026-10-21T15:50+00:00 — `20261021-integ-0.4.5-prep` — 本地候选门禁完成

- 请求/目标：PM 12:51Z 授权本地集成，15:18Z 要求合入 δ 与 DOCS 最终交付后从头跑 25 项门禁，15:40Z 指定全局堆阈值偶发失败的复核流程；不推送、不开 PR、不发布、不部署。
- 开始状态：main 基线 `a1cf8f104`；最终增量开始于干净的 `7297410a4`。δ `50053a8ea` 合为 `95662d477`，DOCS `2ebb17d1e` 合为 `46239531d`。只在网关说明与 tasks 有文档冲突，保留双方有效增补；ε 三个相关文件逐字不变。
- 完成操作：已有测试夹具修复 `c906ba5ca` 只改测试并完成修前/修后 `-race -count=3`；版本 0.4.5；policy 308 条 required_literals、106 条 custom_patch_tests、37 个 custom_patches，有效登记并集与零重复核对通过，已撤回透传稳定头的钩子和测试登记清除；四个原版调度文件相对 main 未变。
- 验证：候选 `46239531d` 的 attempt3 从头跑全部 25 项。首次 unit 25355 pass、39 条件 skip、1 fail，唯一失败为未改动的 `TestInflightEstimate_AccountMappingNoDBAndBoundedMemory`（8664864 > 8388608 字节）。按 PM 指令单例五次全过，HeapAlloc 差值为 4369472、2948840、2902976、2864184、3378360 字节；只用仓库外 overlay 在测量后打印值，断言和工作量未变。attempt3b 不用 overlay 的完整 unit 重跑 25356 pass、39 条件 skip、零 fail；原失败不改写，未降低阈值或跳过该用例。
- 其余门禁：无 tag 14519 pass、28 条件 skip；`ci_gate` 390 顶层 `-race`、额外 service 178 顶层/476 项、handler 94/224 项全部实际执行，零失败/跳过/竞争；gofmt、四 vet、lint（0 issues）通过。前端 413 文件/3182 项、lint/typecheck/build、tools 41 项、audit 校验器 13 项、docs、scheduler-diff、embed 构建和干净树 audit 均通过。govulncheck 调用路径 0 漏洞（另有未调用的包/模块提示）；前端审计 high/critical 为 0，13 moderate/4 low。
- 证据/清理：完整原始记录与明确引用 attempt3b 的最终 25/25 汇总保存在仓库外 `evidence/integ-045/`；旧候选和环境失败也保留。本轮独立 GOCACHE 已按授权删除，目录占用 7361564672 字节（约 6.86 GiB），未动共享缓存或其他工作树。
- 卡点/下一步：本地集成与门禁完成；REVIEW 尚需补审最终增量，测试服务器全套候选验证尚未进行。DOCS 跟进 tasks 4.2 与 0.4.6 的测试稳定化待办；后续远端操作等待 PM 指令。


### 2026-10-21T17:16:40+00:00 — `20261021-native-heartbeat-turn-state-045` — O3 实现完成，待独立审查

- 请求/目标：PM 指令 8，把原生 Responses 首次心跳提前提交 turn-state 的 O3 修正纳入 0.4.5；不新增开关，只本地提交，不推送、不修改 Git 配置、不操作生产。
- 开始状态：从 `integ/0.4.5-prep` 的干净基线 `b966306de52d5bf83cf5147971edf346464a3ad5` 建立 `feat/native-heartbeat-turn-state-045`；中断恢复后核对原未提交实现与测试，并完成验证收尾。
- 完成操作：先确认基线红测，再只修改原生 `stageFirstOutput` 心跳分支：响应尚未提交且暂存头有 turn-state 时，写这一个头并同步记溯源；首个语义输出的 `applyAttemptResponseHeaders` 闭包与基线逐字一致。后续尝试、重复心跳及既有 CDN/排队提交不覆盖头或溯源；其余上游头仍暂存。
- 修改文件：`openai_gateway_response_handling.go`、新增 `openai_native_heartbeat_turn_state_test.go`、`tools/upstream-sync/policy.json`、`docs/GATEWAY_RESILIENCE.md` 与本记忆。policy 新增 1 条 required_literal 与 6 条既有补丁测试绑定，无重复；网关说明 §6 登记无开关的受控例外。OpenSpec design/spec/tasks 由 DOCS 在 `c8e3b9669` 同步，按 16:40Z 分工未在本分支修改。
- 验证：有效红测 6 个顶层中 3 个缺陷用例因首心跳缺少 turn-state 失败、3 个边界回归通过。修后最终 49 个顶层逐项执行 `-tags=unit -p 2 -race -count=3`，147 个顶层结果、含子用例 243 项全部通过，零失败、跳过与竞争；覆盖方案 §1.6 九项清单，第 6 项因不加开关不适用，另测总开关关闭仍生效。service 的无 tag/unit vet 与增量 lint（0 issues）通过。编译/夹具及首轮 lint 的失败记录保留；提交前复核与提交后干净树审计的最终结果在仓库外 `evidence/o3-045/`。
- 卡点/风险：无实现阻塞。心跳已提交后仍不能补发 request-id 与用量头，是 O3 保留的限制；定向验证不替代新集成候选的全量门禁与 QA。
- 下一步：在仓库外交接中记录最终完整 HEAD 并通知 γ 只读独立审查；α 合入 O3 与 DOCS 后跑全量门禁、安排 QA 新候选验证。`b966306de` 只作预跑。 **更正（PM 17:38Z）：独立审查实际由 REVIEW 执行，O3 `93fd9bdf2` 已获放行，无 P0/P1/P2；本条原交接对象不再有效，原始操作记录保留。**

### 2026-10-21T18:05:12+00:00 — `20261021-integ-0.4.5-prep` — O3 新候选门禁与纯记忆交接

- 请求/目标：PM 17:42Z 放行 O3 与 DOCS 最终交付，要求新 attempt/独立缓存从头跑 25 项；17:38Z 要求更正三处 O3 审查归属；18:00Z 要求剩余门禁完成后先交最终 SHA 给 QA，unit 阻塞单独处理。
- 开始状态：干净本地 `b966306de`；快进 O3 `93fd9bdf2`，常规合入 DOCS `88ad8c43c`，仅 tasks 冲突按指令取 DOCS 整份，得到 `4bb8026db`。O3 代码/测试/policy/网关说明未改，ε EOF 防护保留。
- 验证：新 attempt4 全 25 项完成，24 PASS、unit FAIL；同一基线全局 HeapAlloc 用例两次完整运行分别 9100136、8414632 字节超出 8388608，五次单例均通过，测试文件与 main 未变。其余静态、无 tag、竞态、安全、前端、工具、文档、scheduler、embed、干净树 audit 均过，完整数字见进行中条目。失败记录不覆盖，不能声称全绿。
- 本次修改：只改 `docs/PROJECT_MEMORY.md`，更正 O3 已由 REVIEW 审查的事实、更新集成状态，历史交接对象以追加更正标明。门禁对象 `4bb8026db` 与最终 SHA 只差记忆文档；最终提交上复验 docs/干净 audit，结果及完整 SHA 写仓库外交接。
- 卡点/下一步：按 PM 授权先把最终 SHA 交 QA，unit 标记“阻塞：只有基线堆阈值用例在完整套件中失败，处理中”；之后 main `a1cf8f104` 同命令完整 unit 对照，ε 测试修复等 PM 裁定。本地缓存与临时基线树在对照结束后清理，未推送、未开 PR、未发布或部署。

### 2026-10-21T18:45:39+00:00 — `20261021-integ-0.4.5-prep` — 堆用例稳健修复并入与最终树复验安排

- 请求/目标：执行 PM 18:42Z，只合入 ε `13baa2869` 并同步指定文档，在最终 HEAD 复跑完整 unit、ci_gate、docs、干净树 audit；新失败停止报告，不自行修。
- 开始状态：`integ/0.4.5-prep` @ `0c31b5b3861f72f711b376bff61f17a94e5aa674`，工作树干净。ε 分支固定 HEAD `13baa2869f8d9c3221985710f2bb9d01fe07b987` 的父提交是 `4bb8026db`，只改一份测试，普通合并无冲突。
- 完成操作：按已验收交付保留预热与两次 GC 的修复，未放宽 8 MiB 阈值；tasks 7.4 勾为已在 0.4.5 修复，旧门禁条目补修复指引；顶部及本任务集成条目同步，main 对照结论与历史失败保留。
- 修改文件：`backend/internal/service/billing_inflight_reservation_test.go`、`openspec/changes/gateway-resilience-045-20261021/tasks.md`、`docs/PROJECT_MEMORY.md`；无产品代码变化，不追加其他 DOCS 分支提交。
- 验证：ε 的完整 service 三轮已通过，不替代本轮集成门禁；本批提交后按集成条目固定最终 HEAD，运行四项检查，原始日志与结果绑定最终 SHA 写仓库外进度和交接。ci_gate 带 `-race` 但未选择本次堆用例，竞态覆盖按实际选择记录。
- 卡点/下一步：QA 镜像候选 `0c31b5b38` 不变，测试文件不进入产品镜像；四项若有新失败即停止报告 PM。复验结束清理独立缓存并待命；未推送、未开 PR、未发布或部署。

### 2026-10-21T19:17:54+00:00 — `20261021-integ-0.4.5-prep` — 四项复跑全过与最后一次文档收尾

- 请求/目标：PM 18:42Z 要求合入 ε 并在最终树复跑；18:55Z、19:00Z、19:10Z 要求四项全过后一次性合入 DOCS、同步 tasks 4.5/4.6 与 P2 待办 7.5，随后冻结受跟踪文件。
- 开始状态：干净 `e9c686e848cbe9019673ffcb18d96aaa3f3a0415`，测试文件与 ε `13baa2869` 同 blob，产品代码与 QA 镜像候选 `0c31b5b38` 无变化。
- 已完成验证：18:55:26Z–19:08:33Z 四项退出码全为 0；完整 unit 25368 pass、39 条件 skip、0 fail，堆用例实际通过；ci_gate 390 顶层 `-race` 全过、零 skip，不包含堆用例；docs 与干净树 audit 通过。完整命令和并发参数与 attempt4 相同，未跳过用例、未放宽阈值，历史失败不改写。
- 完成操作：无冲突合入 DOCS 固定 `d1fa3a4a3`，在同一个文档提交里写入上述结果、REVIEW 最终无 P0/P1 的放行结论和两项留待 0.4.6 的测试 P2。只改记忆与 OpenSpec 五份文档；相对 `e9c686e84` 的 backend/frontend/tools/deploy 差异为空，按 PM 不重跑 unit/ci_gate。独立缓存已删除，约 3.80 GiB，未动共享缓存。
- 验证与证据：本次文档提交后仅复验 `check_docs_consistency.py` 与干净树 `sync.py audit`；最终 SHA、退出码、相对 `0c31b5b38` 的测试/文档差异，以及相对 `e9c686e84` 的代码/测试零差异，统一写仓库外 `PROGRESS_A`、`PATCH_REQUESTS` 与 `evidence/integ-045/heapfix-e9c686e84/`。QA 继续验证原镜像候选，不重建、不重新浸泡。
- 卡点/下一步：没有产品代码新增问题；本轮最后两项若有新失败停止报 PM，不修。通过后本地树冻结并待命，推送、PR 与发布等用户决定，生产部署未授权。

### 2026-10-21T18:07Z — `20261021-usage-tps-public` — 本地完成，推送并开 PR

- 请求/目标：用户 2026-10-21（附截图）：使用记录“延迟”列改为“平均TPS N t/s”；“功能开关 → 管理员使用记录”两项指标的开关左边各加一个公开按钮，打开 = 公开，关闭 = 仅管理员可见（用户明确不要第三个开关）。
- 开始状态：本地检出原在 `feat/account-recent-health-20261019` 且落后远端；`git fetch` 后自 main `a1cf8f104`（0.4.4 发布后）建 `feat/usage-tps-public-metrics-20261021`，工作树干净。
- 完成操作：
  - 文案：`formatTokenSpeed` 后缀改为 `t/s`；`usage.outputTps` 改为“平均TPS”/“Avg TPS”；设置卡片改为“显示平均TPS”，单位 t/s。运维页与渠道监控的 tokens/s 不变。
  - 公开按钮：新键 `usage_cache_hit_rate_public`、`usage_token_speed_public`（缺省 false，管理接口部分更新保留旧值）；公开设置与页面注入只给生效值 `user_usage_cache_hit_rate_enabled`、`user_usage_token_speed_enabled`（显示开关与公开按钮都打开）；用户使用记录页按它传 `UsageTable` 的两个 prop。按钮只有两种状态、不置灰，样式沿用同页模式按钮。
  - 无迁移；用户接口字段不变（本来就返回 Token 与耗时），只改页面是否展示。
- 修改文件：后端设置常量、解析、写入、视图、DTO、管理与公开处理器及其测试、API 契约夹具；前端 `usageMetrics.ts`、`SettingsView.vue`、用户 `UsageView.vue`、类型与 app store 兜底、中英文案及对应测试；`docs/USAGE_METRICS.md`。
- 验证：见“进行中的工作”本条；`sync.py audit` 在提交后的干净树执行。
- 卡点/风险：无。生产仍为旧版本，本改动随下次发版才上线。
- 下一步：推送、开 PR、等 CI 全绿；不合并、不发版、不部署。

### 2026-10-22T03:16:51Z — `20261022-pr120-release045-integration` — 合入已发布的 main，四项本地检查通过

- 请求/目标：执行 PM 00:55Z 的 B，临时工作树集成 #120；PM 02:30Z 允许指定心跳断连用例的失败作业单次重跑，其余新失败停止。
- 开始状态：临时分支 `tmp/merge-pr120-045-20261022`，干净的 #120 head `6ede2aaa1`；登记任务提交 `686d6651b` 后常规合入 origin/main `0d8e4920e`。
- 完成操作：只在本记忆发生冲突；双方新增条目与历史日志完整保留，版本速览保留监控分支对生产程序版本的只读更正和 main 的 6.5 小时浸泡通过结论。产品代码、policy、scheduler-diff 与来源登记由 Git 自动合并，不手工扩大改动。
- 验证：合并树上文档一致性、scheduler-diff、`go build ./...`、`go vet -tags=unit ./...` 全部退出 0；Go 1.27.3、GOMAXPROCS=2、GOFLAGS=-p=2。四项原始日志与时间戳在仓库外 `evidence/pr120-045-integration/`。本次记忆更新后再验文档检查，提交后的干净树 audit 与最终 SHA 记入 PROGRESS_A；检查失败即停，不自动修复。
- 卡点/下一步：尚待提交后的干净树 audit；通过后普通推送并等待新 head 的 CI 与 Security Scan，全部通过后常规合并，最终结果交 DOCS 在发布收尾中归档。未碰原监控开发工作树、未改生产、未执行 Git 配置修改。

### 2026-10-22T04:26Z — `20261021-resilience-v2-0.4.5` — 0.4.5 发布收尾：记忆归档、QA 结果、O3 覆盖更正与 7.6（DOCS 代记）

- 请求/目标：PM 2026-10-22T00:55Z 第 4 步及 01:42Z、03:40Z、04:25Z 的补充：#120 合入后，基于新的 main 出发布收尾 PR。
- 开始状态：origin/main `27b472e22`（#120 的合并提交，双亲 `0d8e4920e`、`db2304b38`）；新分支 `docs/post-release-0.4.5`，工作树干净。
- 完成操作：
  - 归档：把 0.4.5 的两节交接与 #120 发布后集成的交接、以及 O3、集成、RA-1、δ/ε/DOCS 预备分支、QA 验证、#120 集成的登记，连同已完成的 0.4.4 6.5 小时浸泡与 pprof 诊断登记，原样移入归档，主文件换成“XY2API 0.4.5 正式发布完成”一节。同时更新 0.4.4 一节的遗留、版本速览，以及渠道监控条目里 #120 的状态（已合入 `27b472e22`，进入下一个版本）。
  - OpenSpec `gateway-resilience-045-20261021`：
    - tasks 5.2 写入 QA 全部结果并勾选：71/71、补测 12/12（首轮夹具失败照实写）、透传 8/8、升级 104/104 与 105/105、3 小时判定 0.9 MiB/h、6.5 小时 12/12 与 0.5 MiB/h（含汇总工具 KeyError 与重算）；
    - 更正 O3 覆盖：测试服务器没有 OAuth 夹具，只由单元测试覆盖。design §7 同步更正，§8 的偶发用例指向 7.6；
    - 6.1–6.3 勾选；新增“发布记录”：PR #122、CI 18/18、合并提交、tag、Release run、产物核验、全新安装 29/29，并写明 tag 消息缺空行、Release 正文已补齐；两次 CI 偶发失败（tag CI 的 `/disabled`、#120 PR CI 的 `/enabled`）各自单次重跑通过；QA 工具提交的对应关系；#120 已合入、进入下一个版本；
    - 追加 0.4.6 待办 7.6（P2）。proposal 状态改为已发布，`docs/README.md` 同步。
  - 按 QA 确认的清单，以 `cherry-pick -x` 并入 4 个工具提交（只改 `tools/gateway-resilience/`）：`2adc17332`、`d1a276d87`、`7e4f63e05`、`5ec5c64e9`。
- 修改文件：`docs/PROJECT_MEMORY.md`、`docs/PROJECT_MEMORY_ARCHIVE.md`、`docs/README.md`，以及 OpenSpec 0.4.5 变更的 `tasks.md`、`design.md`、`proposal.md`；另有 4 个 QA 工具提交。
- 验证：`check_docs_consistency.py`、`tools/tests`、干净树 `sync.py audit`，结果见仓库外 `PATCH_REQUESTS`。
- 卡点/风险：无。生产部署 0.4.4、0.4.5 都未授权。
- 下一步：α 推送并开收尾 PR，CI 全绿后合并。0.4.6 待办见 tasks §7（7.2、7.3、7.5、7.6）。

### 2026-10-22T09:19Z — `20261022-pr121-main-integration` — main 集成与指定本地检查通过

- 按用户经 PM 08:15Z 的授权，在独立临时分支从 PR #121 原 head `57f30f2ac` 合入 main `cfb4fae48`；唯一冲突为本记忆，两边新增条目与历史日志均完整保留。两个 dashboard 文案文件自动合并，没有手工修改产品代码，差异范围仍为 PR 原有 29 文件。
- 本地验证：文档一致性、scheduler-diff 通过；Go 1.27.3（GOMAXPROCS=2、GOFLAGS=-p=2）build、unit vet、PR 指定四项设置测试通过；service/handler/server/web 共 10 包 unit 21060 项通过、14 项条件跳过、零失败。pnpm 9 冻结依赖安装、typecheck、lint、414 文件/3192 项测试、build（含 i18n）全部通过。
- 下一步：提交后复验文档与干净树 audit，普通推送；固定 HEAD 的 push/PR CI 与安全扫描全绿后常规合并，再核对 main CI 并移除临时树。仅已知 7.6 用例允许单次重跑；最终远端事实与清理记录写仓库外 PROGRESS_A。不涉及发版、生产或 Git 配置变更。

### 2026-10-22T14:40Z — `20261022-fallback-group-user-invisible` — REVIEW 补修、P2 集成与最终门禁通过（本地，未推送）

- 请求/目标：PM 12:00Z 按 REVIEW 补修；12:10Z 收窄、12:20Z 改为 P2 全部本期完成（ε 做 P2-3、P2-2，FE 做 P2-1、P2-4、TestFastFail）；13:36Z 由 δ 集成并跑完整门禁；13:50Z 磁盘与负载告急，改为串行。
- 开始状态：`feat/fallback-group-user-invisible-20261022` @ `47c6d3393`，工作树干净，未推送。
- 完成操作：P1-1、P2-5、P2-6、P2-7 与契约措辞（`ef5eed100`、`aea937053`）；QA 工具三提交 cherry-pick 与文档检查 allowed（`2101a6784`）；合入 FE `fc95d031f`（`fbd598204`）与 ε `f5541a3b7`（`ccf5774a1`，`policy.json` 按“保留双方条目”合并）；修 FE 用例的 errcheck（`d7cabe427`）。P2-2 的红灯在合并后的树上临时让 `fallbackBillingGroup` 返回空补跑。PR 正文原写 QA 提交“以 `cherry-pick -x` 并入”，实为普通 cherry-pick，正文已更正为列出原提交对应关系。
- 修改文件（δ 部分）：`backend/internal/repository/gateway_cache.go` 及 `gateway_cache_live_test.go`、`usage_log_repo_query.go`、`backend/internal/usageexport/caller_group_test.go`、`docs/GATEWAY_RESILIENCE.md`、`tools/upstream-sync/policy.json`、`tools/docs_consistency.json`、本记忆；ε、FE 的改动见其 CHANGES。
- 验证：见上方“进行中的工作”该任务的“验证（最终树）”；日志在仓库外 `gates/`（`*_final_ccf5774a1.log`、`*_final_d7cabe427.log`、`SERIAL_STATUS_*.log`、`T_targeted_ccf5774a1.log`、`RED_r2_*.log`）。独立 GOCACHE 已删除，前端 `node_modules` 与构建产物已删除。
- 卡点/风险：前端 `AccountsView.selectAllResults.spec.ts` 的未处理 rejection 是 main 上就有的偶发问题，CI 的前端 test 可能因此偶发失败；本机高负载下 `internal/service` 的性能与心跳时序用例可能超时或偶发失败。
- 下一步：PM 推送并开 PR；REVIEW 增量复核；QA 在最终 SHA 复跑 FBILL；合并需用户确认。

### 2026-10-22T16:14Z — `20261022-fallback-group-user-invisible`、`20261021-usage-tps-public` — 两个 PR 合并后的记忆收尾（DOCS 代记）

- 请求/目标：PM 2026-10-22T16:15Z：#121 与兜底分组都已合入 main，主文件里的状态已过时；只改文档，把两条任务的详细记录原样归档，主文件只留结果摘要。
- 开始状态：origin/main `c369e1cab`；新分支 `docs/post-merge-fallback-121-20261022`，工作树干净。
- 合并事实：
  - PR #121 于 2026-10-22T10:13Z 常规合并为 `e7fe4a8e9`；
  - PR #125（兜底分组）于 2026-10-22T16:11:41Z 常规合并为 `c369e1cab`，head `58edc20de`。
  - 两个 PR 都未发版、未部署。
- 验证与审查：
  - CI：GitHub Actions 故障期间 macOS runner 容量不足，#125 的 `shell` 作业多次被取消，重跑后通过；最终 CI 与安全扫描 18/18。
  - QA：在 `ccf5774a1` 上跑 FBILL-1 到 10，107/107，首轮全过。
  - REVIEW：增量审查无 P0、P1，上一轮的 P1 与七项 P2 全部关闭。
  - 后续项 INC-P2-1：WebSocket 每轮的 Q8 快照取在本轮完成的回调里，客户端提前发出下一轮时有毫秒级窗口，不阻断。
- 完成操作：把 #121 的交接，以及 `20261021-usage-tps-public`、`20261022-pr121-main-integration`、`20261022-fallback-group-user-invisible` 三条登记原样移入归档；主文件“当前交接状态”换成两节结果摘要。
- 修改文件：`docs/PROJECT_MEMORY.md`、`docs/PROJECT_MEMORY_ARCHIVE.md`。
- 验证：`check_docs_consistency.py`、干净树 `sync.py audit`。
- 卡点/风险：无。
- 下一步：α 推送并开收尾 PR；发版与部署等用户授权。

### 2026-10-22T19:45Z — `20261023-merge-open-prs-release-046` — PR 合并与 0.4.6 发布完成

- 请求/目标：用户要求合并仓库开放 PR 并发版，最终版本定位 `0.4.6`；生产部署不在授权范围内。
- 开始状态：origin/main `b2b16d4cd`，开放 PR #124、#126、#127 因基线过期产生冲突；本地集成分支先保留各 PR 的代码、记忆和 upstream policy 登记。
- 完成操作：解决 #124 的记忆冲突、#126 的记忆与 policy 冲突、#127 的记忆、`UPSTREAM_BASE.json` 与 policy 冲突；更新固定 head 后依次常规合并 #124 → `c020d3e71`、#126 → `8000bcb2b`、#127 → `f4340ff76`。版本 PR #129 将 `VERSION` 和 provenance 晋级为 0.4.6，并因安全门禁新报公告把 DOMPurify 顶层及传递依赖升级/覆盖到 3.4.17，合并为 `fe7a3b6db`。
- 验证：本地 `check_docs_consistency.py`、`sync.py scheduler-diff`、`sync.py audit`、工具 41 项 unittest、JSON 解析和 `git diff --check` 通过；远端四个 PR 的最新 CI 与安全扫描全部通过。#127 Gateway resilience 一次偶发失败单次重跑通过；#129 首轮 DOMPurIFY 审计失败在依赖升级后通过。
- 发布：创建 annotated `v0.4.6`（tag object `f267b5a7e`，目标 `fe7a3b6db`），Release run `39840369118` 成功，Release 非 draft/非 prerelease，6 个附件生成；未执行生产部署。
- 修改文件：`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json`、`frontend/package.json`、`frontend/pnpm-lock.yaml`，以及本记忆和文档目录状态。
- 卡点/下一步：无阻塞；生产部署仍需单独授权，后续测试 P2 按 OpenSpec tasks §7 管理。

### 2026-10-23T19:00Z — `20261023-cache-p0a-t5` — T0a.3 运行时策略接缝交付

- 保留 T0a.3 的输出、结算、拒绝、observer_failed、no_session 入口；启用取请求快照，静态仅保留总闸和上限。关闭矩阵含实际按 abuse 结算的 undelivered 请求。
- race-r2共1596执行，17个失败项均来自同一 undelivered 夹具漏设 abuse 前提；只修夹具后定向race及59处独立探针通过，0race。拒绝零分配、并发切换、正常轮询的开启/关闭各不超过60秒在r2通过。默认全量lint0问题。
- 文档与部署示例、required_literals同步；核心与接缝分开。下一步等待T0a.4提交，rebase并补受影响验证；随后在新分支开展M2汇总与管理员图表接口。

### 2026-10-23T20:03Z — `20261023-cache-m1-integration` — 三份冻结输入组装

- 从最新 main 按 G → G2 → UI 导入；冲突仅记忆与 policy，保留各任务历史及有效字面量，G2 旧任务按来源原样归档。未夹带 M2。运行时策略覆盖 trace 构造、异步与结算路径，T4 决策点只读已有 trace。
- 已按项目惯例改写 author/committer 并保留 Co-Authored-By。CSV 夹具只归一化行尾，内容不变。
- 待验证：固定候选的 CI 等价预跑、真实仓储 integration、独立终审和端到端；生产仍不变更。详细 SHA/tree 与原始结果保存在仓库外报告。

### 2026-10-23T20:13Z — `20261023-cache-m1-integration` — 调度登记元数据补齐

- 首轮工具单测发现 no-session 观测 allowlist 缺少必需 note；仅补说明，不改审计规则、diff 块、测试断言或产品代码。原失败保留，复验工具/文档/审计门禁；其余产品预跑结果按相同源码继承。

### 2026-10-23T23:43Z — `20261023-cache-m1-integration` — 预跑与终审收尾

- 修复两个测试包的运行时策略 API 接缝；observer_failed 高于 undelivered，新增双故障回归。项目提交身份和协作尾注保持一致。
- `25df25ab7`：全量 unit、四类 tag 编译、缓存 race、四项可靠性、安全、前端关键779项及构建、74处关闭态探针与产品证明通过；独立终审、五项端到端通过，48条关闭态响应与基线逐字节一致。
- DinD 仓储通过；只读挂载的 schema 测试失败在可写工作树定向复验通过。额外全量前端3348项通过但既有卸载定时器异常已记后续；韧性 prelude 时序用例首次失败、原命令独立复跑通过。保留内存中断与环境失败原始证据，未放宽断言。
- 当前提交仅更新记忆；产品源码验证结果继承。等待负责人确认推送，原生 macOS/Windows 与 GitHub 八项检查待 PR 验证；生产不变更。

### 2026-10-24T04:44Z — `20261024-cache-m2-integration-prep` — 归档与 M2 合入准备

- 请求与起点：从 main `72c2d0169` 为 M2 准备独立文档首提交，原记忆 195862 bytes，仅余 746 bytes。
- 操作：10 月 13–19 日的 35 条旧操作日志、P 的 M1 交接与登记逐段原样移入归档；保留其他进行中事项和近期日志，补记 PR #132 已合入的事实。
- 修改：仅本记忆与归档文件；产品代码、迁移、策略及契约不变。归档原文、逐段 SHA-256 与检查结果在仓库外证据中绑定此文档提交。
- 验证与边界：原文逐段核对；提交前运行文档一致性，提交后在干净树执行 upstream-sync-audit 对应四项检查。没有运行产品构建，也未声称 M2 候选门禁通过。
- 下一步：输入冻结后作为 M2 首提交导入；其后按后端、UI 顺序组装，复核迁移 279、关闭态零执行、记忆冲突及其余合入门禁。当前未推送、未部署。

### 2026-10-24T06:55Z — `20261023-cache-p0b` — 北极星后端与主分支接缝完成

- 请求与起点：实现 P0b 在线汇总和管理员图表；从 T0a.5 接缝迁至已合入 M1 的 main，再承接文档前置 0dfeb9980。归档文件精确保留 P 的版本，没有使用共享 stash。
- 变更：运行时 northstar 策略、UTC 汇总和金额分量、四接口、trace 计数、仓储接口与 Wire；同步迁移 279、校验和、现行说明、OpenSpec、索引及 upstream required_literals。
- 审查修复：非 UTC 时间统一规范；取消多余会话状态表并增加 bucket_start 索引；跨小时机会量断言独立计算；保留 M1 的 observer_failed 优先级；既有 handler 测试补 nil 计数器参数，断言不变。
- 验证：缓存 race 1613、配置/Wire race 304、关闭探针 6、handler race 2035 通过及 2 项既有跳过；ci_gate 419 顶层用例全过、0 skip；五种 vet、lint 0 问题、真实 PostgreSQL 2 项与实际 handler 契约自检 20 项均过。工具测试 41 项和调度白名单检查通过；提交后以干净树完成同步审计，结果随仓外交接记录。
- 边界与后续：仅本地提交，不推送或部署；临时数据库资源按 ID 清理，embed 的本地占位未提交且已删除。S 验证 staging，R 终审，P 负责集成和 GitHub 必需检查。非阻塞性能项留后续，不开计时窗口。

### 2026-10-24T05:33Z — `20261024-cache-history-utc` — 审计历史时间按 UTC 输出

- History/HTTP 八例 UTC 回归与相关定向测试通过；测试源码及环境证据、完整交接已移入归档。本次仅本地提交。

### 2026-10-24 — G / cache_trace emit

- 仅开启态减少分配；功能、契约与 race 通过，未推送或部署。详见归档。

### 2026-10-24T07:09Z — `20261024-cache-m2-integration-prep` — 冻结输入组装

- 按文档base→G2→UI三提交→M1.1两提交导入；仅记忆出现文本冲突，原交接和新增UTC日志均保留，未恢复已归档旧记录。M1.1的UTC映射和P0b扩展历史字段同时保留；emit属性优化不改变新增计数结算路径。
- 项目身份与协作尾注已核对，产品源码与来源逐项对应。迁移279采用最终五表/双索引版本，原迁移不变；UI共享42份契约样例齐全。按最新规则先运行五种tag全包vet，其他门禁尚未在集成候选执行；未push、未部署。

### 2026-10-24T08:18Z — 缓存 M2 图表标签隐私修复（R16）

- 请求：邮箱形态的账号名会触发面板隐私保护并隐藏排行；按 PM/F 的 R16 在后端脱敏，保持非邮箱名称。
- 修改：仅在排行响应组装时将账号标签改为与 meta.platform/key 一致的平台及账号编号，分组标签改为 group#编号；保留原始数据与金额计算，迁移 279 不变。检查其他标签来源，没有输出用户或 API key 的名称。
- 契约：同步图表样例 v1.0.2；源清单 SHA256 de64723959656bd8dd34072deceea4efe6a577f04d0126168e5f40d192113081。公开 provenance README 保留私有协作路径隔离。
- 验证：21 个合法样例、24 个反例自检通过；86 项定向 race 全过、0 skip/race；27 份真实 handler 响应通过 schema、语义和 P-3/R16 校验；lint 由 P 在 M2 最终候选上的全量 golangci 覆盖。覆盖普通/邮箱名称、Unicode 空白、聚合平台一致性及三个时间窗口。其余 a99f71795 门禁按未改动路径继承。
- 后续：仅本地提交，交 P/R 加入 M2 候选；G2 随后恢复独立的 M1 加固工作，不部署或推送。

### 2026-10-24T09:27Z — 缓存 M2 严格范围模型计数修复（R17）

- 请求：端到端对账发现 priced_models 按业务范围计数，超过严格北极星实际参与金额计算的模型数。
- 修改：计数改为严格范围已定价模型集合；单价与 priced_p_share 保持业务范围，三种 cost_view 一致，迁移 279 不变。同步现行说明、OpenSpec 和 v1.0.3 契约来源说明。
- 验证：service 全包 unit race {'run': 19539, 'pass': 19521, 'skip': 14, 'fail': 4, 'race_reports': 1}，handler 图表 race {'run': 32, 'pass': 32, 'race_reports': 0}；27 份实际 HTTP 输出通过 v1.0.3 校验，21 合法/24 反例源样例自检通过。3 项既有失败（2 项时序、1 项 auth 夹具 race）不在 diff 范围内，单独复跑排队中，结果后补。负责人已裁定不阻塞本提交、不在M2修非缓存夹具；原失败不改写、不宣称0race，产品diff仅金额计数一行。回归覆盖 below_min、未知阈值、不同平台同名模型、无严格行和三种金额视角。
- lint 由 P 在 M2 最终候选上的全量 golangci 覆盖；先前本地 privacy-lint 为超时 rc=4，原始日志保留，不记通过。
- 后续：在 R16 之后独立本地提交，交 P/R；未推送或部署。G2 继续独立 M1 加固，待 M2 合入后变基。

### 2026-10-24T09:31Z — `20261024-cache-m2-integration-prep` — 审查修补纳入并固定最终候选

- 在原M2与M1.1候选上追加UI请求竞态修补、G2 R16邮箱标签脱敏及R17严格范围模型计数。仅R16记忆追加冲突，原文并集保留；R17无冲突。迁移说明修正为实际五张表，不改SQL和checksum。
- 项目身份与协作尾注已核对，产品源码与来源逐项对应。迁移279采用最终五表/双索引版本，原迁移不变；前后端45份契约JSON字节一致，v1.0.3只更新口径说明。旧候选五种tag编译已过，当前候选门禁仍待运行；来源中的三项既有时序/夹具失败和lint超时如实保留，不改写为通过。未push、未部署。

### 2026-10-24T14:06Z — 缓存 M2 集成测试隔离（PR #133）

- 请求与起点：在 cache/p0b 的 R17 94481bcf4 后修复 M2 测试隔离。CI 两次在既有 requested_model 表达式索引断言中选择另一索引；原 PR 候选在本地按同 PostgreSQL 镜像、默认 Go 并发与原顺序运行完整 repository 包，1779 通过、4 原有跳过，未复现失败，不声称证明根因。
- 修改：仅 northstar 集成测试及本记忆。两个测试分别创建独立 PostgreSQL 和 Ent/SQL 客户端，保留真实内部事务、对账与失败回滚断言；结束关闭连接并销毁自身容器。没有共享 usage_logs 写入或 VACUUM/ANALYZE，不改既有上游测试、产品代码、迁移 279。
- 验证：Go 1.27.3、PostgreSQL 18.1-alpine3.23、Redis 8.4-alpine；隔离版定向 3/3 和完整 repository 1779 通过、4 原有跳过、0 失败。容器、网络、卷按记录的 ID 清理。测试源码校验和与验证副本一致；提交前执行文档及 diff 检查，提交后核对干净树同步审计。
- 补充证据：外部采样窗口包含紧邻之前的既有计费用例，不能当作精确前后对照。负责人已批准不等只读 wrapper 的排队结果；shared-stats precise proof queued, result to be appended。该辅助夹具不进入提交，结果随后补记，完整包不重复运行。
- 下一步：交 P 追加 PR #133，R 增量复核，最终由 GitHub CI 判定；未推送、部署或操作生产。

### 2026-10-24T14:12Z — `20261024-cache-m2-integration-prep` — PR #133 测试隔离增量

- 在729928626后追加G2 ce0192351；仅记忆末尾追加日志冲突，双方原日志完整保留；测试与来源逐字一致，279及产品未变。统一项目身份并保留原说明/现有尾注。
- 按最新裁定立即普通push以并行CI和R复核；只补docs/audit/diff检查，repository继承G2已通过的1779项及4项原跳过，不重复启动。精确共享统计辅助证明后补，不把未复现的CI失败称为红绿证明。

- 2026-10-24：G2 按授权将 M1 hardening 的 10 个增量重放到已合入 M2 的 main 16a776e347。仅首项文档冲突，保留两侧新增说明；产品代码原补丁不变。旧基线单测发现三组夹具/断言问题并作最小测试修正，不修改产品、计费或路由语义；旧 race 被停止且不记通过，后续完整门禁由 M3 集成候选覆盖。未 push。
### 2026-10-24T04:37Z — `20261023-cache-p1-l01` — 吸收已合入的 M1

- 开始：`cache/p1-l01` @ `ea16d7e22`，全部改动已暂存，tree `3bdc8f7a`；完整 binary patch、stash-create 对象及逐文件 SHA 已保存于仓库外。
- 基点：确认 PR #132 已合并、八项检查全绿后，以 origin/main `72c2d01698cd859223e943a0fa61b3454bca31ec` 为基点完成变基。代码自动合并；UPSTREAM_BASE、同步 policy 与本记忆的三处元数据冲突保留双方登记。
- 继承：M1 的 T4、运行时 API/UI 与 observer_failed 优先级；P1 不在旧头重放。G 的已完成 P0a 详细条目和操作日志原样归档，其他 agent 的条目不变。
- 验证：轻量格式、diff、文档、scheduler 检查后冻结新树；Go 门禁由绑定新 HEAD/tree 的流水线经协调队列执行，结果另行记录。本次不提交、推送或部署。

### 2026-10-24T07:10Z — `20261023-cache-p1-l01` — L-01 本地实现与验证收尾

- 完成：按 M3 v1.0.2 优先级保护客户端字段、原 compat seed 与实际 quality rotation；补齐 passthrough 排除和 Chat Responses→raw 重试来源清理。原回归业务断言保留。
- 证据：冻结 tree `90f2a7374fed0fd3726e0ba5a92624b71ddff36a` 的七阶段流水线通过；focused 6261、race 6304、兼容386条pass事件，race/兼容均单列跳过既有可选 Redis 性能比较。关闭态 native/probe、480解析对照、126新会话零分配边界通过。
- 分配：140配对中120个alloc增量为0；20个buffered配对继承已批准T4的精确减少202次，整文件来源证明与原始strict结果保留。其他路径GOGC=off的B/op均值0～+0.2、置信区间均含0。
- 日志：M3 v1.0.2的29个合法/非法样例与两次独立实际logger执行通过严格CLI；原始日志行保留，关联键与计费一致。四tag vet按新增规则补跑通过，明确不是在本轮长门禁之前执行。
- 收尾：G旧开发交接原文移入归档，其他agent记录不变；测试后只有文档变化。提交前做文档检查，提交后做干净树上游审计；独立评审、staging、CI与后续新main重基留交接，不推送或变更生产。


### 2026-10-24T13:31Z — `20261023-cache-p1-l01` — 响应隐私与分桶补修收尾

- 已完成B-M3-1与独立cohort域；提供方未传key形态依据已保存官方/D1完整示例，不冒称新的线上实验。10万key列联表通过，trace既有三个产品文件逐字不变。
- r3的stream固定+3分配已修为bytes-only事件buffer；r4漏io导入的编译失败与各轮原始证据保留。r5未插桩完整分配矩阵通过原批准口径，拒绝将20组T4负增量写成严格相等。
- 原allocation/focused自然结束，剩余单条重任务于13:30 UTC按PM优先级要求中止，race及后续未完成。已通过的分配/focused保存原树证据；余项与管理员API在M2合入变基后统一运行。最终收尾只改G记忆/归档与任务状态，附产品树继承证明。

### 2026-10-24T13:36Z — `20261023-cache-p1-l01` — 管理账号候选与契约补证准备

- FE-Q3只改管理员view/validation：分页静态资格、最多500条白名单字段；查询失败发生在提交前。请求snapshot及转发代码与r5相同，分配/golden不因本补丁重复执行。
- 原UNSUPPORTED映射已正确；新增18份原字节HTTP样例回放。账号查验失败清warnings，与其他invalid分支及F样例一致；合法rollout0仍有提示。northstar按当前pre-M2构建明确not_in_build，fallback按样例前提seed。
- PM要求先本地提交供静态评审，轻量docs/diff/clean audit收尾；所有新增Go测试与r5余项统一等待M2合入变基后的单条门禁，当前未执行，不作为完成验收。

### 2026-10-24T14:17Z — `20261023-cache-p1-l01` — null形态与管理员关闭路径补修

- B-M3-1-r1按PM选法2：保留响应字段，原位替换值为null；mock无key回显null，更新重复键/空白位置、EOF与显式客户端值断言。Q5来源更正为官方文档保存样例，未执行真实上游请求；省略形态上游的残留可区分风险已记录。
- 管理员候选只作响应元数据，分页错误丢弃partial，固定warning且不泄露底层错误。关闭、降比例、收窄的Validate/PUT均跳过账号校验与枚举，保留格式/rev校验；开启/mixed仍要求账号校验成功。同步v1.0.2五样例与v1.0.3四增量，22份F原字节，回放与故障注入测试已准备。
- PM要求本轮只轻量检查并本地提交，重门禁不启动；post-M2单批runner已更新22样例与新测试必跑清单，历史r5的通过项不改绑成本轮通过。

### 2026-10-24T14:54Z — 20261023-cache-p1-l01 — M2 变基与编译接缝

- 完整保留四个 P1 来源提交，变基到已合入 M2 的 main 16a776e347；M1.1 日志单记录编码/UTC、计数器和仓储测试隔离继承。策略 capability 显式并集、Wire 同时传账号仓储与计数器；M3 与 northstar 快照回归已补，待集成执行。
- 契约 JSON 与哈希不改；M3 实际回放按已声明范围排除 M2 northstar 卡片，先断言其可用。观测新增 persist_trace_hourly 的值与 schema 单独断言后投影，M3 字段继续逐字段比较。M2 原有策略/接口测试保留。
- 来源只做一次 default/unit/integration/e2e/embed 编译检查；结果待补。按 P 的协调，race、零执行、实际日志、兼容、lint 与22样例回放合并到最终 M3 候选执行，不重复来源长门禁。原始证据与逐提交映射在仓库外保留，未推送或部署。

- 编译接缝补充：首轮 default vet 发现 M2/P1 同名 cloneCachePolicyValue；复用 M2 已含 ID 与阈值规则切片的实现，删除 P1 的窄重复定义，保留首轮失败证据。未运行长门禁；修后五tag结果待补。

### 2026-10-24T15:27Z — 20261023-cache-p1-l01 — M2 列表契约 v1.1

- F 发布列表契约更新后，同步五份 M3 list 原字节与 SHA；其余17份未变。实际 HTTP 测试移除 M2 字段投影，连同 northstar 与 persist_trace_hourly 全量比较，仍只归一化 UTC 时间。产品运行时代码不变。
- 五tag r2已在1e31dbe3c通过；本提交只补unit tag，其他四tag按源码范围继承。实际测试执行与长门禁由P在最终M3一次覆盖；当前unit补证待完成，原始失败与所有继承证据保留。

### 2026-10-24T15:33Z — 20261023-cache-p1-l01 — M2 变基编译与契约补证收尾

- 五tag r2在1e31dbe3c全部退出0；v1.1后继e972cd4e7的unit补查退出0（约12秒），源码前后相同。其他四tag的Go编译输入未变，继承证明已保存。本提交仅更新记忆，不重复Go门禁。
- 交付范围为新main16a776e347之后的完整四来源重放、接缝、v1.1夹具以及本记忆。175个M2非重叠路径、279及仓储隔离测试保持；22样例与F原字节相同。映射、range-diff、原编译失败、修复与新旧证据绑定在仓库外交接中。
- R变基审查无阻塞；中间提交不能独立编译的bisect建议登记后续，集成人可在不改变最终产品树的前提下整理。最终集成单条流水线、实际22样例回放、S端到端和GitHub CI仍由P统筹；未把来源编译当作测试执行。未推送、合并或部署。

### 2026-10-24 — M3 集成候选组装

- P 依次导入 G2 e7e906619、G c9d2bcc77 与 UI 3b1ddef82，共23项；身份统一为项目noreply，来源说明与尾注保留。文档冲突保留双方记录；279及northstar隔离测试不变，无新迁移。
- 发布锁与账号options接缝按独立评审建议保持事务外I/O及新revision优先；旧M1期望不改写，M3契约用完整实际响应验收。五tag、unit/race、零执行/logger/compat、lint、安全、前端、DinD与独立golden/staging待最终候选一次流水线，不引用来源编译冒称测试通过。

### 2026-10-24 — 20261024-cache-m3-e9 — 租户种子注入边界

- 仅在策略启用分支、cohort之前接受v2:隔离种子；复合组、无API key与非OpenAI组旧种子沿用no_routing_session内部跳过，既有sticky不变，trace平台范围不扩展。
- 新增三入口scope、两个租户实际Forward、policy关闭顺序与trace平台对照；allocation控制组夹具补v2:，保持原断言。单条五tag、L01 unit/race及相对6188的增量lint待执行，结果在仓库外PROGRESS补记；不推送、不部署。
