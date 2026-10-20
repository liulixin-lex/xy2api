# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-12 及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（2026-10-17 原样移入）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

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

### 版本与环境速览（2026-10-20T08:30Z 核实）

- 正式版：XY2API v0.4.3（2026-10-20，Sub2API 兼容基线 0.2.15）。生产主机 2026-10-20 起经在线更新运行 0.4.2 程序，容器镜像仍是 0.3.0；部署需要用户单独授权。
- 待定：XY-image 0.0.17（先交接，并经用户确认）；0.4.3 生产部署（单独授权）。预上线主机运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。见上两节。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-17 定时检测；2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.2）；golangci-lint v2.14.0（与 CI 同版）。
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

- 无。

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

### 2026-10-16 — 20261016-grok-monitor-responses — 原生协议功能与 PR 交付

- 请求：根据只读审查中 Grok 监控固定 Chat、账号测试使用 Responses 的证据，在指定仓库增加原生协议选项并提交 PR；未授权生产变更或合并发布。
- 开始状态：干净 main `f8fec15a23ab37a6efdbc02b0db0376ed6b60dbb`；原工作树不变，独立分支与工作树完成实现。没有把生产凭据、IP、用户信息或原始日志提交到仓库。
- 修改：后端监控/模板校验、Responses adapter 选择、文本提取入口、合并字段保护、部分更新保留协议；前端监控及模板编辑/筛选/展示、请求示例和中英文说明。无迁移或依赖升级，Chat及OpenAI原有行为保持。
- 验证：本地18项前端新增及相关回归、类型/lint/生产构建、19项工具、来源审计及调度差异检查通过；补充后端HTTP路径/字段/错误与创建/更新/模板回归。初轮编译空间问题、可选参数类型错误及缺endpoint测试夹具均保留记录，修正后的后端与最终完整CI明细保存在外部VALIDATION及PR。
- 交付：[PR #98](https://github.com/liulixin-lex/xy2api/pull/98)；文档说明非流式JSON和既有6/30/45秒阈值边界。源码功能与使用说明完整提交，本Agent不合并、不发布、不部署。下一步由维护者评审并按目标供应商验证实际服务可用性。

### 2026-10-16T18:35:42Z — `20261016-iq-conflict-aiportx` — 只读诊断完成

- 请求/目标：账号 #307 `port015`（OpenAI API Key，上游 aiportx 中转，检测模型 gpt-6-astra/low/compat）的智商检测持续返回 `conflicting_final_response`（“最终响应互相冲突”），查明原因。
- 开始状态：`main` HEAD `f8fec15a2`，工作树干净；生产 `xy2api-prod-app` 运行 `xy2api:0.3.0`，与本地 `iqcheck` 源码一致。
- 完成操作：只读查询生产库 `accounts.iq_check` 与 `account_iq_check_results/attempts` 的诊断；在生产宿主机用与 IQ 探测相同的请求体直连该账号上游，共发出 11 次测试请求。key 只在服务器内通过 curl 的 stdin 配置传递，未打印、未落盘、未回传；请求不经 XY2API，未写生产数据。
- 结论：该中转会在真实输出前注入占位 assistant 消息（`msg_ft_*`，正文为单个空格，无 `phase`，响应 ID 变为 `resp_ft_*`），并把它写进 `response.completed.output`。`parse.go` 的 `terminal()` 把无 phase 且文本非空的 assistant 消息都当作最终回答，空格也算非空，因此与真实的 `final_answer` 冲突（`parse.go:401-404`）。生产 9 次调用的诊断（done_messages 2/3、terminal_items 3/4、ignored_items 2/1）与“1–2 条占位 + reasoning + 最终答案”的结构逐项吻合。
- 其他观察：占位注入不是每次都有。直连拿到完整响应的 6 次中有 2 次出现，都是短提示词；4 次检测原题都没有出现，而生产 9 次调用全部命中。差异可能与 IQ 探测携带的 `prompt_cache_key` 或客户端特征有关，未证实。带 `prompt_cache_key` 的 curl 请求在该中转上，HTTP/1.1 会超时，HTTP/2 会被整段缓冲约 66 秒，原因未查明，不影响上述结论。该账号业务流量正常，最近一小时有上百次成功请求。本机与生产均无 Go 1.27 工具链，未用真实解析器回放，结论依据代码逐行推导和既有用例 `parse_http_test.go:182`。
- 修改文件：仅本文件（本条及顶部交接项）。未改业务代码，未部署，未改生产配置。
- 下一步（待用户授权）：在 `terminal()` 中把纯空白的 assistant 消息视为非答案（与 design.md:92“只提取完整最终回答”及 `Grade` 先做 TrimSpace 一致），并补占位消息回归用例；或者请上游关闭占位注入。

### 2026-10-16T20:21:37Z — `20261016-iq-blank-placeholder-0.3.1` — PR #99 已开，按用户决定止于提交 PR

- 请求/目标：修复账号 #307 智商检测误判（见 `20261016-iq-conflict-aiportx`）。用户先授权修复测试、提交 PR、发版部署三步全部完成；`gh pr merge` 被 Claude Code 自动权限检查拦截后，用户改为“提交了 PR 就行”。合并、打 tag、发版和生产部署均未执行。
- 开始状态：`main` `f8fec15a2`，工作树只有上一条诊断记录的未提交改动；分支 `fix/iq-blank-placeholder-20261016`。
- 完成操作：`terminal()` 跳过仅含空白的 assistant 消息，并记为忽略类型 `blank`；解析器版本升为 `iq-response-v6`；产品版本 0.3.1（`VERSION` 与 `UPSTREAM_BASE.json`）；`docs/OPENAI_IQ_CHECK.md` 同步说明。先写回归用例，在未修复代码上确认失败后再修复。用真实解析器回放按抓包结构构造的流，1 条和 2 条占位的诊断计数与生产 9 次调用的两种形态逐字段一致。
- 修改文件：`backend/internal/pkg/iqcheck/{parse.go,diagnostic.go,parse_http_test.go,reliability_test.go}`、`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json`、`docs/OPENAI_IQ_CHECK.md`、本文件。
- 验证：本机 Go 1.27.2（`/tmp/xy2-toolchain`，安装包 SHA256 与官方一致）下以下检查通过：`go test -race ./internal/pkg/iqcheck/`、`-tags=unit` 的 IQ 相关 service/repository/handler 单测、gofmt、go vet、golangci-lint v2.14.0（iqcheck 0 issues）。`sync.py audit`、`scheduler-diff` 和同步工具 19 项测试也通过。PR #99 head `67497e325` 的 8 项必需检查（push 与 PR 两份）全部成功；本条为纯文档提交，推送后新 head 需重新通过检查。
- 卡点/风险：本机全局凭据助手是 Git Credential Manager，HTTPS 推送会挂起等待交互。本次改用单条命令 `git -c credential.helper= -c 'credential.helper=!gh auth git-credential' push`。合并由维护者执行。
- 下一步：维护者审阅 PR #99，并以 merge commit 合并；随后从合并提交创建 annotated `v0.3.1` tag 触发 Release，核验制品与 GHCR 镜像。生产部署需另行授权。届时按 2026-10-15 升级现场的方式只替换镜像行，用 `docker compose -p production` 原地重建。无迁移，回滚即恢复 0.3.0 镜像行；pgBackRest 与 WAL 归档可提供 PITR。

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

### 2026-10-17T08:35Z — 20261017-repo-consistency-sweep — 完成

- 请求/目标：用户要求全量审查工作区代码、结构与文档，按最新进度规范化、补齐漏改的代码与文档，并建立机制，让后续开发时刻保持最新、不漏模块或文档。
- 开始状态：`release/0.4.0`，HEAD `5fdd1e589`，工作树干净。主 agent 在同一工作区等 `soak-c6b837dca` 浸泡，已告知；约定由它在浸泡结束后复跑门禁并随 PR #100 推送，我不推送。
- 审查结论：
  - 代码：`backend/`、`frontend/` 没有需要改的残留。余额精简删掉的标识符只剩迁移测试与历史注记；韧性相关包没有未引用的函数；前端韧性 143 个文案键都有引用，API 没有死函数。本任务未改任何源码，浸泡候选与正式镜像不因此产生差异。
  - 与代码不符的方案与文档（均已修正）：design §2.2 余额行、§8.2 与 disconnect spec 列了不存在的 `slot_wait` 阶段、§9.2 与 error spec 的“余额状态”、§12 的 `resilience.balance_transition` 与缺漏事件；keepalive spec 写首输出超时会软罚账号；proposal 的 D13、D16 缺修订；管理员文档 §5.3 漏 5 个日志事件；account-scheduling、SHARE_SCHEDULING 未写 0.4.0 规则；部署文档未写 CDN 部署所需的 `X-XY-Edge` 与受信代理；ASYNC_IMAGE_TASKS、USAGE_METRICS 未跟上 0.4.0；tools README 与首字校准 SQL 停在旧分档与 F24；三语 README 与 DEV_GUIDE 的 Go、lint 版本，DEV_GUIDE 直接 merge/rebase 上游的写法；两份 openspec 中已随 0.3.0 完成的任务未勾选；docs 白名单漏了 13 个已跟踪文件。
  - 结构：项目记忆 454 KB，历史原样移入 `docs/PROJECT_MEMORY_ARCHIVE.md`，主文件 92 KB；主 agent 的 0.4.0 两节与 0.3.0 发布记录逐字保留，逐行对账无丢失。新增 `docs/README.md` 文档目录（现行、记录、历史与 OpenSpec 变更状态）。
- 机制：`tools/check_docs_consistency.py`（规则 `tools/docs_consistency.json`，单测 9 个）检查相对链接、文档目录与白名单、Go 与 lint 版本、韧性计数/日志事件/设置是否写进文档、退役名字是否残留、记忆主文件大小与日志结构，接入 backend-ci 的 upstream-sync-audit 作业；AGENTS.md 加“变更同步”表与归档规则；DEV_GUIDE、同步规范与同步 Skill 的门禁列表同步。用修正前的管理员文档回放，检查器正好报出本次发现的 5 个漏记事件。
- 修改文件：docs（GATEWAY_RESILIENCE、account-scheduling、SHARE_SCHEDULING、ASYNC_IMAGE_TASKS、USAGE_METRICS、STRIPE_HOSTED、UPSTREAM_SYNC，新增 README 与记忆归档）；openspec（gateway-resilience 的 design、proposal、3 份 spec、tasks、2 份 evidence，async-image-tasks-v2 与 share-scheduling 的 tasks）；deploy（EDGE_SECURITY、config.example.yaml 与 Caddyfile 的注释）；tools（gateway-resilience 的 README 与 SQL、检查脚本、规则、单测）；三语 README、DEV_GUIDE、AGENTS.md、.gitignore、backend-ci.yml、同步 Skill 的 runbook。提交自 `f568a3437` 至本条所在提交（`git log 5fdd1e589..` 可列出）。
- 验证：`sync.py audit`、`sync.py scheduler-diff`、`tools/tests` 37 个、`check_docs_consistency.py`、Caddyfile 策略脚本与 `config.example.yaml` 解析均通过；`git diff 5fdd1e589..HEAD -- backend frontend` 为空。未改源码，未跑 Go 与前端测试。
- 卡点/风险：无。留给 0.4.1 决定：测试环境 `fixtures.py` 有 10 个分组从未被场景使用（含 `G-balance-429`），fault-upstream 的 F19/F20 只服务已删除的余额探测；浸泡期间不动测试服务器脚本。
- 下一步：主 agent 浸泡结束后复跑门禁并随 PR #100 推送这些提交，同时刷新它的顶部状态（仍为 04:45Z 的内容）。

### 2026-10-17T09:00Z — 20261017-repo-consistency-sweep — 公开仓库主机信息清理（完成）

- 请求/目标：主 agent 推送前扫描发现归档中有旧测试站域名与开发机路径；用户授权“可以处理，由你决定”。
- 决定：只清理当前跟踪文件，不改写已公开的 Git 历史（main 与已发布标签不强推）。能定位真实主机的信息（测试站域名及与之绑定的 Compose 项目名、端口、凭据目录，同机共存服务名，生产 SSH 账号）与开发机、宿主机路径一律改为代称；对外公开的生产入口域名、产品默认安装路径 `/opt/xy2api`（部署文档与脚本）、测试工具里的测试环境项目名与端口、上游 Sub2API 带来的 openspec 文件和测试代码里的示例保持不变。
- 完成：19 个文件改为代称（记忆主文件与归档、13 份早期功能与修复记录、4 份 openspec 文件；`<测试站点>`、`<工作机 A>` 等），归档开头写明；代称对照表追加到仓库外私有主机清单。检查脚本新增 `private` 检查（规则在 `tools/docs_consistency.json`）：这些标识、文档中的公网 IPv4、`ubuntu@`/`root@` 主机写法入库即失败；新增单测 1 个。AGENTS.md 工作原则写明公开仓库的主机信息规则。
- 验证：`check_docs_consistency.py`、`tools/tests` 38 个、`sync.py audit` 与 `scheduler-diff` 通过；`BILLING_INTEGRITY_PROVENANCE.json` 仍为合法 JSON；全仓库复扫无残留。
- 卡点/风险：Git 历史仍含原文，只能靠轮换或下线测试站消除（该测试站若仍在运行，建议下线或更换域名）。
- 下一步：随 PR #100 由主 agent 推送。

### 2026-10-17T09:25Z — 20261017-repo-consistency-sweep — 主机信息检查改为只存哈希（完成）

- 起因：主 agent 复核 `95e6828f5` 时指出，`private` 规则为了拦截被清理的标识，把它们的原文又写进了规则文件。
- 修改：主机类标识（测试站域名与项目名、共存服务名、Windows 工作机目录名）改为登记规范化后的 SHA-256，检查时对文本中每个词串的整词片段算哈希比对；工作机路径根与用户目录用通用正则取出目录名再比对哈希；报错只显示哈希前缀。公网 IPv4 与 `ubuntu@`/`root@` 两条通用规则不变。规则文件里不再有这些原文（哈希只是不写原文，短词仍可被字典反查）。AGENTS.md 写明登记方式。
- 验证：用 `a12d33b82` 上清理前的归档回放，命中 189 处，与清理范围一致；检查器单测 11 个、`tools/tests` 39 个、一致性检查（约 2.6 秒）通过。
- 下一步：随 PR #100 由主 agent 推送。

### 2026-10-17T10:05:55Z — `20261017-codex-websearch-502-terra` — 只读诊断完成，待修复

- 请求/目标：用户反馈某账号持续报错（gpt-5.6-terra，404），要求只读排查生产；随后要求先把问题记录下来，暂不修复。
- 开始状态：分支 `fix/iq-blank-placeholder-20261016`，HEAD `07b9dcfcc`，工作树干净；生产为 `ghcr.io/liulixin-lex/xy2api:0.3.0`。
- 完成操作：只读查询生产库（会话设 `default_transaction_read_only=on`）的 `ops_error_logs`、`usage_logs`、`accounts`、`account_groups`、`groups`、`audit_logs`，并读取 Caddy 访问日志；对照源码定位 404 文案（`backend/internal/handler/no_account_error.go:129`）和 web_search 修补的生效条件。结论见顶部同名条目。
- 修改文件：本文件（仅本条记录与顶部条目）。生产无任何改动。
- 验证：审计日志中两次账号映射修改的时间，与 378、365 的 502 停止时间及 404 起始时间逐秒吻合。未改代码，未运行测试。
- 卡点/风险：请求体不落库，“历史含 `web_search_call`”无法直接证实。本条记录最初写在 PR #99 分支的工作树中（未提交），后移到 `fix/registration-alias-batch-disable-20261017` 随该分支提交。
- 下一步：等待用户决定 terra 的处理方式，以及是否实施代码改进。

### 2026-10-17T10:35:29Z — `20261017-restore-deleted-paying-users` — 完成

- 请求/目标：用户批量清理注册机账号时可能误删 90 多个用户，要求核查其中有无充值用户；随后授权恢复充值用户及其 API Key。
- 开始状态：分支 `fix/iq-blank-placeholder-20261016`，HEAD `07b9dcfcc`（未改代码）；生产 0.3.0。
- 完成操作：审计日志与 `users.deleted_at` 核对出同一批 93 个软删除用户。按累计充值、订单（状态为大写，`COMPLETED`）和兑换码判定 18 人有充值，并单事务恢复这 18 个用户（行数守卫）。API Key 原值已被墓碑覆盖，审计表也无记录，因此用同一镜像起隔离临时容器（无端口映射、`--archive-mode=off`、`--target-action=pause`，限制 CPU 与内存），从 R2 仓库 PITR 到 UTC 09:54:50，日志确认停在第一笔删除事务提交前。核对 25 个 Key 的 id、用户、名称与生产一致后，经管道单事务写回（数量、墓碑、冲突守卫），随后删除临时容器与数据。
- 修改文件：本文件。生产数据：`users` 18 行、`api_keys` 25 行（仅 `deleted_at`、`key`、`updated_at`）。
- 验证：18 人 active，余额合计与累计充值合计均与删除前一致；25 个 Key 全部可用，md5 汇总与快照一致；恢复后数分钟内已有 3 个 Key 成功调用 27 次。临时目录已删除，磁盘空间已释放。
- 卡点/风险：3 个 GitHub 用户的登录绑定未恢复，依赖按邮箱自动重新绑定，未在生产验证。
- 下一步：可考虑为“删除用户”增加可恢复能力（例如保留 Key 原值或真正写入 `deleted_api_key_audits`），并明确 `deleted_api_key_audits` 的用途。

### 2026-10-17T13:50Z — `20261017-registration-alias-batch-disable` — 本地完成，已推送并开 PR（待审阅）

- 请求/目标：用户同意别名加固方案（拒绝非常规本地部分、别名归一去引号、探针兼容存量带引号的行、查重计入软删除账号），并要求在用户管理多选中增加批量禁用，以后清理注册机不再删号；同时提供批量启用，便于撤回。做到提 PR 为止。
- 开始状态：自 `origin/main` `ca2bdd111` 新建分支 `fix/registration-alias-batch-disable-20261017`，工作树干净。
- 完成操作：
  - `58601c744` 别名加固：新错误码 `EMAIL_FORMAT_NOT_ALLOWED`，覆盖注册、发验证码（同步与异步）和共享的注册邮箱策略（第三方邮箱自动注册/登录、邮箱绑定）；缺少本地部分或域名的输入仍交给原有校验。仓储探针增加带引号的形式，仍走迁移 190 的表达式索引；别名查重用 `SkipSoftDelete` 计入软删除账号。注册页与验证页显示本地化提示。
  - `a37bbc64f` 批量状态：服务、仓储、处理函数各一个新文件。仓储能力是可选接口 `UserStatusBatchRepository`，不改 `UserRepository` 及其测试桩；缺少该能力时返回 501 `USER_STATUS_BATCH_UNAVAILABLE`。上游文件只加路由、`AdminService` 方法和处理函数测试桩方法，连同前端 API、视图与中英文案处的钩子一起登记到 `required_literals`。
  - 推送前改写了尚未推送的文档提交：主机路径、供应商域名、用户与 Key 标识、金额明细移到仓库外私有笔记（位置见顶部“误删 93 个用户后的恢复”）。
- 修改文件：见上述两个提交；本文件。
- 验证：gofmt；`go vet`（默认、`unit`、`integration` 标签）；golangci-lint v2.14.0 0 issues；`go test -tags=unit` 覆盖 service、handler（含 admin）、server（含 middleware、routes）、repository，全部通过。批量 SQL 在本机临时 PostgreSQL 18（应用全部迁移）上验证通过，临时实例已删除。前端 `check:i18n`、`typecheck`、eslint 通过，vitest 全量 389 个文件、3051 个用例通过。`sync.py audit`、`scheduler-diff` 通过。仓储集成测试（testcontainers）因本机无 Docker 未运行，以 CI 为准。
- 卡点/风险：无代码卡点；集成测试结果以 CI 为准。
- 下一步：用户审阅 PR；合并、发版、生产部署以及 Caddy/Cloudflare 的 `Sec-Fetch-Site` 规则均待另行授权。

### 2026-10-17T14:25Z — `20261017-signup-fetch-metadata-rule` — 完成（生产 Caddy 规则已生效）

- 请求/目标：PR #101 的 18 项检查全绿后停在 PR，合并由另一位作者执行；用户授权在生产 Caddy 加规则拦截注册机，只用“缺少 `Sec-Fetch-Site`”这一个条件。
- 开始状态：分支 `fix/registration-alias-batch-disable-20261017`，HEAD `3bd704f50`（PR #101）。
- 完成操作：只读查看 Caddy 配置后，在主站点 `route` 块最前面加了 `@signupNoFetchMeta`（POST、两个注册路径、`not header Sec-Fetch-Site *`）与 `respond @signupNoFetchMeta 403`。该站点的所有处理都在 `route` 内按书写顺序执行；规则放在 `route` 外会排在反代之后，永远不生效。先备份，候选文件 `caddy validate` 通过后才覆盖，再校验正式文件并 `systemctl reload caddy`（平滑重载，未重启 XY2API）。另外两个域名无需改动：一个 308 跳转到主站点，一个只转发模型 API。
- 修改文件：生产 Caddy 配置（位置与备份见仓库外私有笔记）；本文件。
- 验证：从外网经 CDN 访问，两个注册接口不带 `Sec-Fetch-Site` 返回 403，源站访问日志确认由 Caddy 返回；带 `Sec-Fetch-Site: same-origin` 时请求到达应用（空请求体返回 400）；登录接口不带该头不受影响；首页与公开设置接口返回 200。
- 卡点/风险：不发送 `Sec-Fetch-Site` 的旧浏览器（如 16.4 以前的 Safari）无法注册，此前按日志估计约占正常注册的 0.1%，用户已接受。回滚方式：恢复备份后重载。
- 下一步：观察两个注册接口的 403 数量与正常注册是否继续成功；PR #101 等待另一位作者合并。

### 2026-10-17T15:00Z — `20261017-registration-alias-batch-disable` — 合入最新 main、解决记忆冲突

- 请求/目标：PR #100（0.4.0 晋级）合入 main 后，PR #101 显示冲突。按用户要求（验证没问题后交另一位作者合并），在 PR 分支上合入 main 并复验。
- 开始状态：PR 分支 HEAD `c940704e0`（18 项检查全绿）；origin/main 由 `ca2bdd111` 前进到 `4c142e1aa`。
- 完成操作：普通 merge，不改写历史、不强推。只有本文件冲突：main 把历史移到 `PROJECT_MEMORY_ARCHIVE.md` 并调整了结构。以 main 版本为准，本分支三节交接状态放在“版本与环境速览”之后，四条日志按时间接在末尾；与 main 版本相比只增不删（+71/−0）。`policy.json` 与 `UPSTREAM_BASE.json` 自动合并，两侧内容都在：`xy2api_version` 为 0.4.0，本分支两个具名补丁及其测试绑定与 `required_literals` 均保留。
- 观察（生产只读）：注册拦截规则生效后约 27 分钟内，两个注册接口除本人 4 个验证请求外没有任何 POST，生效前 16 分钟也没有。注册机暂未再来，实际拦截效果待下一波确认。
- 修改文件：本文件（合并结果与本条）。
- 验证（合并后的工作树）：`check_docs_consistency.py` 通过，`tools/tests` 39 个通过，`scheduler-diff` 通过；后端 build、`go vet`（默认、`unit`、`integration`）、golangci-lint 0 issues，`go test -tags=unit` 覆盖 service、handler、server、repository，全部通过；前端 `check:i18n`、`typecheck`、eslint 通过，vitest 全量 388 个文件、3049 个用例通过。`sync.py audit` 在提交后的干净树上运行。
- 卡点/风险：无。
- 下一步：CI 复跑全绿后交另一位作者合并。

### 2026-10-17T15:40Z — 20261015-gateway-resilience — 0.4.0 正式发布完成

- 请求/目标：按用户经评审转交的晋级清单（A1–A5）与“上游余额处理精简”完成 0.4.0：新候选整套验证只跑一次，推送 PR #100，CI 全绿后合并、打 `v0.4.0`；生产部署单独授权，不访问生产，公开仓库不写生产账号号码。
- 开始状态：`release/0.4.0`（自 main `ca2bdd111`），`v0.4.0-rc.1` 已发布并验证。
- 完成操作：
  - A1 `24cfc6511`、A2 `dd5ae0c0f`、A5 `8798e40d2`、A3/A4 文档 `116be5d6d`；余额精简 `cc5fc3208`（63 个文件，删 2296 行、增 764 行，含迁移 277）、文档 `f66c5562f`、测试工具 `e039c2b6f`、`c6b837dca`；自查措辞 `5fdd1e589`。评审 agent 的仓库规范化与主机信息清理（`f568a3437`…`15ce5301b`，不改源码）随 PR #100 合入。
  - 候选 `c6b837dca`：场景 45/45、真实客户端 17/17、升级演练（含 277）、6.5 小时浸泡 11 项全过（存活堆 0.4 MiB/h）；design §14 15 项达标，4 项为合成负载所致。
  - PR #100 头 `49f14df9a` 两组 18/18 → 常规 merge `4c142e1aa`（14:34:55Z），main 上 CI 与安全扫描全绿；annotated `v0.4.0`（对象 `451f3f240`）→ `4c142e1aa`，Release run 39043056373 成功；制品、二进制、镜像与稳定别名核验全过；正式镜像全新安装 324 条迁移通过。
- 修改文件：见上述提交；本收尾只改 tasks 13.1/13.3、本文件与归档（本任务的交接状态与登记原样移入归档）。
- 验证：各步骤的本机门禁、CI 与测试服务器结果见上；本收尾为纯文档，跑文档一致性检查、`sync.py audit`、`tools/tests`。
- 卡点/风险：GitHub 网页合并的提交作者为账号默认邮箱（此前的合并提交相同），需要时在账号设置中改为 noreply。正式镜像与浸泡候选只差一句英文设置提示。
- 下一步：XY-image 0.0.17（先交接并经用户确认）；生产部署（单独授权，按 runbook）；0.4.1 见 tasks“后续”；收尾清理见“当前交接状态”。

### 2026-10-18T12:30Z — 20261018-release-0.4.1 — 0.4.1 正式发布完成

- 请求/目标：用户经评审转达，发布 0.4.1：应用自动识别 CDN 回源，并合并 PR #98、#99、#101；不跑浸泡、不出 RC；只读查看一次生产 Caddy 访问日志确认 ESA 回源头。生产部署与生产配置改动未授权。
- 开始状态：main `b9a2a11aa`（0.4.0 收尾后），工作树干净。
- 完成操作：
  - 生产只读核查（10:55Z，sudo 只读日志，未改任何东西，未落盘口令，去标识证据在仓库外 `production/ESA_HEADER_CHECK_20261018.md`）：ESA 回源全部带 `CDN-Loop: esa;loop=N`，Cloudflare 回源全部带 `CF-Ray` 与 `CDN-Loop: cloudflare`，XY-image 内网直连两者都不带。
  - `release/0.4.1`：合并 #101 `b9067e0ab`、#99 `0ef405e22`（0.3.1 版本号丢弃）、#98 `2e0d5be24`（新文档登记）；三处只在记忆冲突，记录原样归档 `db78baf80`；CDN 识别 `c7da44849`（先写失败用例，文档同提交，测试服务器新增 CF-6）；版本 `d488d2b5d`；验证记录 `7f112a981`。
  - PR #103 18/18 → 常规 merge `edea0d493`；main CI 与安全扫描全绿；annotated `v0.4.1` → `edea0d493`，Release run 39126008209 成功；制品、二进制、镜像与稳定别名核验全过；正式镜像全新安装通过。
- 修改文件：见上述提交；测试服务器 Caddy 片段验证时临时去掉标记，验证后恢复原样。
- 验证：本机全量与 CI 门禁（249）、前端全绿；测试服务器 KA/CF/ERR 19/19；PR、main 与 Release 工作流全绿。
- 卡点/风险：无。生产 0.3.0 未改；部署需单独授权。
- 下一步：生产部署 0.4.1（单独授权）；XY-image 0.0.17（先交接并经用户确认）；0.4.1 之后的后续事项见 tasks“后续”。

### 2026-10-19 — 20261019-group-fast-priority — 本地实施与隔离验证完成

- 请求/目标：先规划并克隆项目到当前工作区，实现分组强制 Fast 高于全局策略，保持分组倍率与 Fast 单价独立。
- 开始状态：main `0320d4e6807cc3fff04a5c55959a70e49ec5a61c`，初始工作树干净；新分支 `fix/group-fast-priority-20261019`。
- 完成操作：在共用策略入口优先返回 force_priority；结算复用同一有效分组判定且只保留实际出站 priority，不凭上游响应升档。同步全局/分组中英文说明、管理员文档与部署说明，登记具名补丁和必需钩子，无新增数据库字段或迁移。
- 修改文件：service 的请求策略、结算及其测试；四份中英文设置文案；OPENAI_FAST、文档目录、部署说明、白名单、来源登记与本记忆。
- 验证：旧源码实际复现过滤/阻止及 default 降档失败；最终回归 305 项全通过，后端 build、前端 6 项与 typecheck/lint、scheduler-diff 通过。独立副本使用同一测试输入和 Go overlay 保留验证夹具，修改版通过、回滚版重新失败，恢复与补丁重建哈希匹配。中途下载连接、pnpm 版本、Windows 路径/换行问题已处理并保留失败日志。
- 卡点/风险：仓库文档检查与工具测试存在 Windows 路径兼容问题，未扩展本次业务修改范围；仓库外 POSIX 路径适配的全部文档检查为零问题。未声称原生全量工具测试或 Linux CI 通过。
- 下一步：用户审阅源码后决定提交与远端交付；生产配置、历史账单与运行服务本轮未修改。

### 2026-10-19 — 20261019-group-fast-priority — 提交与 PR 交付

- 请求/目标：用户授权提交、推送并创建 PR。
- 开始状态：`fix/group-fast-priority-20261019`，起点 main `0320d4e68`；远端 main 未变化，无同名 PR。
- 完成操作：功能提交 `ee777e42c`；Git HTTPS 多次连接失败，改用 GitHub 官方 Git API 上传同一源码树并创建原 SHA 的提交与分支，创建 [PR #105](https://github.com/liulixin-lex/xy2api/pull/105)。没有改写提交内容、强推、合并、发布或部署。
- 修改文件：本轮仅更新本记忆的授权与交付状态；业务代码和已验证源码保持不变。
- 验证：提交前文档检查通过；补齐固定上游标签后 `sync.py audit` 通过；远端分支 SHA 与本地 `ee777e42ce92fa3e29e2b45772e604015cad1297` 一致。
- 卡点/风险：CI 诊断连接器暂时无法连接 GitHub，不声明远端检查通过。PR 已创建，不影响审阅。
- 下一步：核实远端 CI 并审阅 PR；生产操作另行授权。

### 2026-10-19T12:40Z — 20261019-readiness-0.4.2 — 审查核实、预上线主机只读核查、0.4.2 修复（本机完成，待授权）

- 请求/目标：读前序开发与评审会话、开发文档与两份审查报告，核实合理性与可行性；SSH 只读核查预上线站点；规划并解决问题，使其具备上线条件。同机旁路审查会话逐项验收，并转达用户决定：部署专用的插件宿主补丁不再需要，交付官方镜像。
- 开始状态：`main` `0320d4e68`，工作树干净；分支 `fix/readiness-0.4.2-20261019`。
- 完成操作：
  - 核实：报告 R01、R02、R05–R10、P1-C、跨进程槽位误清理均与代码一致；R03、R08 为 tasks 已登记项；R04 按设计只约束新尝试；实例层面的问题（真实 IP、连接池、降级限流、健康检查、备份、告警、容量、业务验收）写入 runbook §3.4。处置表在 tasks“0.4.2”。
  - 修复：`3dca0fa4e`…`78f24a907`，每项先写失败用例、确认修复前失败；SQL 与 Lua 在本机 PostgreSQL 18、Redis 8.10 实测。
  - 预上线主机：只读查看容器、nginx、数据库与 Redis 状态；只读导出表结构与迁移、插件行（不含配置与凭据列）用于本机演练。主机上未做任何修改。
- 修改文件：见上述提交；本文件。
- 验证：四种 vet、golangci-lint 0 问题、全量 unit 与无 tag 测试、`ci_gate.py`（274 个 `-race`、零跳过）、`tools/tests`、scheduler-diff、文档一致性通过；`sync.py audit` 在干净树上通过（见顶部条目）；本机切换演练通过，发现切换前必须卸载插件（runbook §3.5）。集成测试需要 Docker（本机没有），待 CI；测试服务器场景未跑。
- 卡点/风险：推送、PR、测试服务器验证、预上线主机部署与配置均未授权；SMTP、异机备份存储、测试 Key 需用户提供。
- 下一步：向用户申请三项授权（Draft PR、测试服务器整套验证、预上线主机部署与配置）。

### 2026-10-19T14:10Z — `20261019-exclusive-group-label` — 本地完成，已推送并开 PR（待审阅）

- 请求/目标：账号编辑的分组选择中，专属分组显示紫色文字标签。做到 PR 检查全绿为止，合并由另一位作者执行。
- 开始状态：自 origin/main `0320d4e68` 新建分支 `feat/exclusive-group-label-20261019`，工作树干净；PR #101 已于 2026-10-18 随 0.4.1 合入。
- 完成操作：`GroupSelector.vue` 在 `is_exclusive` 为真时显示标签，配色沿用模型广场的专属标识（浅紫底、紫色字，暗色模式有对应变体），字号 10px 以适配两列选项。新用例在旧组件上确认会失败。文档与 `required_literals` 同步。
- 修改文件：`frontend/src/components/common/GroupSelector.vue`、`frontend/src/components/common/__tests__/GroupSelector.spec.ts`、`docs/GROUP_SYSTEM_PROMPTS.md`、`tools/upstream-sync/policy.json`、本文件。
- 验证：前端 typecheck、`lint:check` 通过，vitest 全量 389 个文件、3056 个用例通过。另用 Vite 临时预览页（真实组件、样式与中文文案，暗色模式）加无头 Chrome 截图核对：专属分组显示紫色“专属”，公开分组不显示，隐藏用户端标识的专属分组仍显示；临时文件已删除。`check_docs_consistency.py`、`tools/tests`、`scheduler-diff` 通过，`sync.py audit` 在提交后的干净树上运行。
- 卡点/风险：无。

### 2026-10-19T16:45Z — `20261019-account-list-usability` — 本地完成，已推送并开 PR（待审阅）

- 请求/目标：见顶部同名小节的用户要求①②③。做到 PR 检查全绿为止，合并由另一位作者执行。
- 开始状态：自 origin/main `0320d4e68` 新建分支 `feat/account-list-usability-20261019`，工作树干净；PR #105、#106、#107 均未合并，本分支与它们互相独立（#106 改的 `account_repo.go` 区段与本次不重叠）。
- 完成操作：样图的排列就是同步前（`0c80e8296`）的平铺筛选，0.2.15 同步引入了“主筛选 + 折叠更多筛选”；本次恢复平铺并保留同步带来的无障碍标签与手机两列。默认排序若不处理 NULL 会把从未使用的账号排到最前，因此后端同改。列拖动沿用仓库已在用的 `vue-draggable-plus`（支付服务商列表、分组排序）。
- 修改文件：后端 `account_repo.go`、新增 `account_repo_list_order_test.go`；前端 `AccountTableFilters.vue` 及其 spec、`AccountsView.vue`、新增 `AccountColumnSettings.vue`、`useColumnOrder.ts` 及三个 spec、中英文案；`tools/upstream-sync/policy.json`、`UPSTREAM_BASE.json`、`tools/docs_consistency.json`、本文件。
- 验证：新用例（筛选平铺、默认排序与旧排序作废、列顺序保存与恢复、后端 NULLS LAST 的 SQL）均在旧代码上确认会失败。前端 typecheck、`lint:check`、`check:i18n` 通过，vitest 全量 392 个文件、3067 个用例通过；后端 gofmt、`go vet`（默认、`unit`、`integration`）、golangci-lint 0 issues，repository 包 unit 测试通过。另用 Vite 临时预览页（真实组件、样式与中文文案，1600 像素宽、暗色模式）加无头 Chrome 截图核对：第一行操作按钮，第二行七个筛选项平铺；列显示每行带拖动手柄。临时文件已删除。`check_docs_consistency.py`、`tools/tests`、`scheduler-diff` 通过，`sync.py audit` 在提交后的干净树上运行。
- 卡点/风险：拖动本身由 `vue-draggable-plus` 实现，单测用替身模拟拖动结果，未在真实浏览器里实际拖动；手机上筛选项两列排布。
- 下一步：PR 检查全绿后交另一位作者合并。

### 2026-10-19T19:00Z — `20261019-account-recent-health` — 本地完成，已推送并开 PR（待审阅）

- 请求/目标：见顶部同名小节；口径经用户确认后按计划实施。做到 PR 检查全绿为止，合并由另一位作者执行。
- 开始状态：基于 PR #108 的分支（头 `1b76c280d`）新建 `feat/account-recent-health-20261019`；#107、#108 均未合并，origin/main `0320d4e68`。
- 完成操作：错误请求页的列表 WHERE 有隐含条件（只算 status≥400 的客户可见错误、cyber_policy 例外），直接 COUNT 会多算已恢复的上游记录，因此批量计数复用 `buildOpsErrorLogsWhere`。账号处理器只持有 `AccountUsageService`，新查询作为用量日志仓储的可选能力实现，避免改动 Wire 与生成代码。前端链接按 `BASE_URL` 生成，与 `createWebHistory(BASE_URL)` 一致，组件不依赖注入的 router。
- 修改文件：后端新增 `account_recent_health.go`（service）、`account_recent_health_repo.go`、`account_handler_recent_health.go` 及各自测试（含仓储集成测试），`routes/admin.go` 加一行路由；前端新增 `AccountRecentHealth.vue` 及其 spec、`AccountsView.recentHealth.spec.ts`，改 `AccountsView.vue`、`UsageView.vue`、`UsageFilters.vue`、`api/admin/accounts.ts`、中英文案，8 个已有 AccountsView spec 补新接口的 mock（用 `vi.fn(impl)`，经得起 `vi.restoreAllMocks()`），`UsageView.spec.ts` 新增用例；`docs/USAGE_METRICS.md`、`policy.json`、`UPSTREAM_BASE.json`、本文件。
- 验证：后端 gofmt、`go vet`（默认、`unit`、`integration`）、golangci-lint 0 issues；service 与 handler 新单测通过；仓储查询在本机临时 PostgreSQL 18（应用全部迁移）上验证，并与错误请求页 `ListErrorLogs` 同条件的总数对照一致（3 条，含 cyber_policy、不含已恢复记录），临时实例已删除；`go test -tags=unit` 覆盖 service、handler、server、repository 通过（service 包的一次偶发失败见下条）。前端 typecheck、`lint:check`、`check:i18n` 通过，vitest 全量 394 个文件、3073 个用例通过；新页面用例在旧代码上确认会失败。Vite 临时预览页加无头 Chrome 截图核对状态单元格（正常、无数据、慢首字橙色、限流中带倒计时等），临时文件已删除。`check_docs_consistency.py`、`tools/tests`、`scheduler-diff` 通过，`sync.py audit` 在提交后的干净树上运行。
- 卡点/风险：service 包第一次整包运行时，与前端全量测试、Vite 同时进行，`TestInflightEstimate_AccountMappingNoDBAndBoundedMemory`（计费在途估算的堆增长上限 8 MiB）测得 9.9 MiB 失败；该用例与本次改动无关，单独连跑 5 次均通过，service 包单独整包重跑也全部通过。仓储集成测试以 CI 为准。
- 下一步：PR 检查全绿后交另一位作者合并（#108 之后）。

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
