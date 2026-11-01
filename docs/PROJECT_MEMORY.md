# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-29 及以前（0.4.13 周期及更早）的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-10-31）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### 工作区精简与规范化（2026-10-31；本分支 PR 合并即完成）

- 仓库：删除 85 个跟踪文件（跟踪内容约 71.5 MiB → 61.4 MiB）：已退役或被取代的历史文档 15 个、旧上游同步报告 11 个（只留与基线对应的 `v0.2.15.json`）、已发布且有现行文档的 OpenSpec 变更 5 个、违反无赞助政策的 `assets/partners/`（同时登记 `required_absent_paths`）、失效的 `deploy/Makefile` 与 datamanagementd 部署文件（后端该功能是废弃桩）。目录、白名单、`docs_consistency.json`、`policy.json` 与引用同步更新；不改业务代码、迁移、标签与发布产物。
- 记忆：本文件由约 157 KB 精简到约 31 KB；0.3.0–0.4.15 的交接原文、channel-monitor-v1 登记与 2026-10-26～10-29 的操作日志原样移入归档，顶部改为版本发布一览与未结事项。
- 分支：本地已合并分支与 `rec-next` 工作树已删；GitHub 删除 101 个已合并分支（含 PR 已合并、只多出文档提交的 2 个），只剩 main 与 3 个含未合入提交、待用户决定的分支。
- 本地：`.work/` 由 1.6 GB 降到约 0.2 MB，只留进行中的账号分组方案与 `reference/`；已清理 Python/TS 缓存与 `.DS_Store`。

### 版本与环境速览（2026-10-31 更新）

- 最新正式版 XY2API v0.4.15（2026-10-31），Sub2API 兼容基线 0.2.15，迁移共 333 项（最后为 286）。
- 生产：2026-10-30 只读核实运行 0.4.14 / `267f8577f`（部署非本仓库 agent 操作）；0.4.15 未部署。部署与上线观察每次单独授权，外部状态以重新核实为准。
- 预上线主机仍运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.3）；golangci-lint v2.14.0（与 CI 同版）。
- 文档一致性：`python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业执行），规则在 `tools/docs_consistency.json`，改动时按 [`AGENTS.md`](../AGENTS.md)“变更同步”同步文档。
- 本地 `.work/` 只保留进行中的任务，已结束任务的过程稿与证据已于 2026-10-31 删除；发布资产以 GitHub Release 为准。

### 版本发布一览

各版本均为正式 Release（非 draft、非 prerelease），附件与镜像在发布时已核验；发布说明在 tag 正文。完整交接、验证与核验原文见 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（“2026-10-31 记忆重整原样归档”及更早章节）。

| 版本 | 日期 | 发布提交（相关 PR） | 主要内容 | 迁移 |
|---|---|---|---|---|
| 0.4.15 | 2026-10-31 | `10fbc9c72`（#170、#171） | Anthropic 缓存 TTL：管理员默认 5m/1h、API Key 四态、最终出站统一、usage/计费/预留解耦，见 [`ANTHROPIC_CACHE_TTL.md`](ANTHROPIC_CACHE_TTL.md) | 286 |
| 0.4.14 | 2026-10-30 | `267f8577f`（#168） | 网站工单（默认关闭）与 OpenAI 账号 2FA 直接登录/密钥管理，见 [`SUPPORT_TICKETS.md`](SUPPORT_TICKETS.md)、[`OPENAI_TOTP.md`](OPENAI_TOTP.md)；decode-uri-component 0.5.1 安全修复 | 284、285 |
| 0.4.13 | 2026-10-29 | `66778769c`（#163、#164、#166） | 普通管理员可调整账号分组；补救优化 N1–N5（Responses 部分用量与终态关流、E2 近期成功替补、热大输入首字下限） | 283 |
| 0.4.12 | 2026-10-28 | `fdedd33dc`（#158、#159、#160、#161） | 上游余额列与 new-api 访问令牌、普通管理员角色与逐人权限、报错补救与路由（余额阶梯、开场限流换号），见 [`UPSTREAM_BALANCE.md`](UPSTREAM_BALANCE.md)、[`ADMIN_PERMISSIONS.md`](ADMIN_PERMISSIONS.md)；回退须按文档处理迁移 282 | 282 |
| 0.4.11 | 2026-10-28 | `368958301`（#154、#155） | 智商检测记录分页与弹窗精简、默认全局并发 8、限流类结果不在轮内重试 | — |
| 0.4.10 | 2026-10-28 | `6042b7b68`（#150、#151） | OpenAI 智商检测按“账号 × 模型”重构与 fail-closed 门禁（见 [`OPENAI_IQ_CHECK.md`](OPENAI_IQ_CHECK.md)）；创建密钥弹窗加宽与专属标识 | 281 |
| 0.4.9 | 2026-10-27 | `da96d7652`（#146、#147） | OpenAI 首字截止分热/冷、计划内截止豁免惩罚、判慢与会话迁移；网关韧性复盘（心跳按尝试恢复、非 OpenAI 账号×模型 breaker、Gemini 5xx 与尝试截止） | — |
| 0.4.8 | 2026-10-26 | `4e7c9096c`（#142、#144） | 回退 0.4.7 缓存改动，只保留 OpenAI 会话 UUID 与跨轮稳定锚点（见 [`openai-cache-reliability.md`](openai-cache-reliability.md)）；之后不能再运行 0.4.7 | 280 |
| 0.4.7 | 2026-10-25 | `33f15f792` | 缓存策略组合（已由 0.4.8 回退，保留 #131 分组弹窗） | 279 |
| 0.4.6 | 2026-10-23 | `fe7a3b6db`（#129） | Responses 开场判据、按分组并发槽位、分组分类（#124/#126/#127）；渠道监控 v1（#120）、使用记录平均 TPS（#121）、无号兜底分组对用户不可见（#125） | 278 |
| 0.4.5 | 2026-10-22 | `0d8e4920e`（#122） | 网关韧性 0.4.5：RA-1、Responses 透传首输出截止与有界暂存、O3 | — |
| 0.4.4 | 2026-10-21 | `4b0b6d94b`（#118） | 网关韧性第二期（8 个新开关缺省开启）、份额调度精简（#117）、Go 1.27.3 | — |
| 0.4.3 | 2026-10-20 | `888f49db8`（#114） | CF 风格 5xx 按 TRANSIENT、上传中断记 upload 阶段、在线更新先排空 | — |
| 0.4.2 | 2026-10-20 | `6e0ad2f3e`（#109–#112） | 生产就绪审查修复、`/ready`、并发槽位进程租约、分组强制 Fast、账号筛选与近期健康、命令面板 | — |
| 0.4.1 | 2026-10-18 | `edea0d493`（#103） | CDN 回源自动识别；注册邮箱别名加固与用户批量启停（#101）、Grok 监控 Responses（#98） | — |
| 0.4.0 | 2026-10-17 | `4c142e1aa`（#100） | 网关韧性（见 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md)）、份额调度规范化、余额失败暂停 | 272–277 |
| 0.3.0 | 2026-10-13 | `1e0fbdd70`（#92、#93） | 同步 Sub2API 0.2.15（见 [`UPSTREAM_SYNC_V0215.md`](UPSTREAM_SYNC_V0215.md)）、可控调度退役、份额调度（默认关闭） | 270、271 |

### 未结事项（2026-10-31 汇总；动手前重新核实）

- **部署与上线观察**：0.4.15 未部署；Anthropic TTL 的真实供应商缓存命中率与收益未验证；0.4.9 的过渡开关与 fast_fail 待上线 7 天后复核；智商检测参数化题未做真实上游校准，旧结构待上线观察 7 天后删除，单账号“不降智”分组需运维加第 2 个账号；N5 阈值须上线后校准；0.4.12 上线约四小时的只读观察未证明首字与缓存全面改善（样本小，不作因果归因）。
- **功能遗留**：WS 余额阶梯与复位暂缓；Anthropic 暂存失败尝试的上游成本由网关承担；非 OpenAI breaker 为进程内状态，多实例各自计数；迁移的空位预检有抢槽竞态；管理端保存过设置的部署 Gemini 截止仍为 90 秒；0.4.5 OpenSpec tasks §7 的 7.2/7.3/7.5/7.6；渠道监控 v1 待真实上游验证（兼容矩阵、推理模型响应时间）与审查 P2-7；无号兜底 INC-P2-1（WebSocket 快照毫秒级窗口，不阻断）。
- **工单与 2FA**：真实 OpenAI 网页登录/MFA 只有 mock；启用 2FA 须部署 Worker、固定运行时和加密恢复目录，回退前须解决不确定任务并保留密钥与恢复日志。
- **安全与依赖**：vue-i18n 安全例外 2026-11-27 到期前处理；两台主机的 SSH 密码曾出现在对话中，建议改用密钥并更换；生产 settings 中的明文代理凭据建议轮换；`.github/SECURITY.md` 仍指向上游 Sub2API 的报告渠道，应改为本仓库（该文件未分类，改动需同时登记 `policy.json`）。
- **待用户决定**：账号分组档位化方案 v1.0（本地 `.work/tasks/20261030-account-group-tiering/`，PLAN §8 五个决策点）；Codex 历史联网搜索导致 502（2026-10-17 诊断，原文已归档）——恢复映射或调整白名单，代码改进前先核实 0.4.0 的 `resilience_web_search_replay` 是否覆盖 apikey 中转账号；XY-image 0.0.17 先交接并经用户确认；远端保留的 3 个含未合入提交的分支（`feat/native-stream-recovery-20260930`、`fix/native-stream-concurrency-20261001`、`fix/signature-retry-preserve-tool-result-media`）是否删除；`tools/gateway-resilience/` 下 `staging/`、`fault-upstream/`、`cdn-sim/` 发布演练脚手架 0.4.6 后未再使用，是否删除。
- **测试稳定性**：CI 偶发 `TestCompatPreludeCommentHeartbeatStillAllowsFailover/enabled` 等心跳计时用例；2026-10-28 已放宽一组截止时间，其余未定位。
- **仓库外环境收尾**：测试服务器的候选镜像、浸泡与演练目录、`xy2api_upgrade` 库按 staging-validation §11 清理；预上线主机的两份切换前备份在正式部署后删除。

## 进行中的工作

- **20261031-claude-cache-ui**（2026-10-31 开始）：用户要求去掉使用记录里的「TTL 策略: 不覆写 TTL · 未应用」文字，并询问「Anthropic 缓存默认 TTL」与「Claude 自动缓存」是否冲突、是否合并、是否按分组。分析结论：两者没有冲突（先补断点、再统一时长、最后做 4 槽与长 TTL 顺序修正），建议保留两项、放进同一张卡片，TTL 不按分组。用户选定分两个 PR，均为纯前端、不改接口与存储，由 Claude 直接实现：
  - PR 1 `fix/usage-ttl-text-removal-20261031`：删除使用记录列表与 Token 悬浮详情里的策略与应用状态文字，接口字段保留，「TTL 明细未报告」保留。
  - PR 2 `feat/claude-cache-settings-card-20261031`：叠在 PR 1 上，把默认 TTL 移进「Claude 缓存」卡片（两行、一个保存，只提交改动字段），主表单不再提交 `anthropic_cache_ttl_default`。
  - 两个 PR 只开到 CI 全绿，不合并、不发版、不部署。
  - 状态（2026-10-31T16:35Z 核实）：PR #175、#176 已开，两者 20 项 CI 全部通过、可合并，未合并。本地浏览器环境与截图在仓库外的交接目录（Claude 缓存 UI，2026-10-31），用完已停掉并清理数据。
  - 2026-10-31 追加（#176）：用户对照参考仓库后选定先补文案。卡片「缓存时长」与 Key 弹窗提示写明：选 5 分钟（含跟随全局 5 分钟）时，客户端自己写的 1h 也改为 5m；Key 弹窗补写入价倍数与 1 小时适用间隔。
  - 留给用户决定、本次未做：「跟随全局」改为只延长、不缩短（参考仓库原则，会改 0.4.15 行为；用户确认后再做，可替代原「全局加不覆写选项」）；客户端自己声明缓存时，余额预留是否恢复温会话折扣（0.4.15 起按冷写预留）；OAuth 系统提示块 TTL 下拉补说明；自动缓存分组选择器只列能到 Anthropic 的分组。
- **20261031-tool-cache-markers**（2026-10-31 开始）：用户对照参考仓库 kiss-kedaya/sub2api 的 Anthropic 缓存 TTL 后，按建议顺序开工 P0/P1 后端项，叠在 #176 上单独开 PR，由 Claude 实现：
  - Chat/Responses → Anthropic 转换保留工具结果（`role=tool`、`function_call_output`）上的缓存标记。现状丢标记，但仍按原始正文判定「客户端已声明」，自动补缓存不补、预留按冷写算。
  - 仅转换路由：顶层 automatic 与末块显式 TTL 冲突时向长 TTL 调和（原生 `/v1/messages` 不变），与上一项同 PR，避免 off 模式 Key 出现新 400。
  - 观测：策略 1h 已应用但上游只报 5m 时，结算日志标为降级；文档补运维查询，并写明官方 1h 不需要 beta、本站默认不加。
  - 只开到 CI 全绿，不合并、不发版、不部署；真实上游实测需另行授权。
  - 状态（2026-10-31T19:40Z）：分支 `fix/tool-cache-markers-20261031`（叠在 #176 上）已本地实现并验证，见操作日志；具名补丁 `xy-converted-tool-cache-markers`。合并顺序 #175 → #176 → 本 PR。
  - 发现、留给用户决定（本 PR 不改）：
    - 0.4.15 起 `ChatCompletionsToResponses` 会把 Chat 请求里的顶层、消息、分片、工具 `cache_control` 写进发往 OpenAI/Grok 的 Responses 请求（0.4.14 不发）。严格上游可能对未知字段返回 400；0.4.15 尚未部署，建议另开小 PR 在 OpenAI 出口去掉。本 PR 的工具标记已改用内部字段，不扩大这一范围。
    - 原始声明识别仍漏 Responses `additional_tools`、Codex `namespace` 子工具、无 type 的内容项上的标记（既有问题）；`/v1/responses` 旧版 Chat 形状请求的工具标记不转发。
    - 结算日志 `ttl_mode`/`ttl_source` 在生产恒为 off/unrecorded：`usageRecordContext` 不复制策略 context（既有问题）。
    - 「跟随全局只延长、不缩短」待用户确认。

## 操作陷阱

- 处理 Sub2API 同步只以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。`DEV_GUIDE.md` 的“Git 操作”曾写着直接 merge/rebase 上游 main，2026-10-17 已改为指向同步规范，这两条命令也登记为 `tools/docs_consistency.json` 的退役写法。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。
- GoReleaser 的稳定别名（`latest`、主版本、次版本）现用 `skip_push: auto`，prerelease 不再改写它们（v0.3.0-rc.1、v0.4.0-rc.1 发布后均核实未动）。早期 v0.0.7 RC 曾短暂改写别名，正式发布后已恢复。
- GitHub Actions 在本次 RC/正式 Release 中提示 `docker/login-action@v3`、`docker/setup-buildx-action@v3`、`docker/setup-qemu-action@v3` 的 Node.js 20 runtime 已弃用，Runner 强制切到 Node.js 24 后仍成功；这是后续 Action 版本维护项，不是本次发布阻塞。
- 本机门禁要包括 golangci-lint（版本见 `tools/docs_consistency.json` 的 pins，本机需自行安装），运行时加 `--max-issues-per-linter=0 --max-same-issues=0`：CI 注释只显示同类问题的前几条，照注释逐条修会漏（2026-10-28 PR #151 遇到）。
- macOS 会定期删掉 `/tmp` 下 3 天没访问的文件。0.4.2 期间放在 `/tmp` 的 Go 工具链和测试服务器辅助脚本被删过一次。跨天的任务把工具链和脚本放到用户目录下，`/tmp` 只放当次的临时文件。
- 提交与打 tag 前先看一眼 `git config user.email`：仓库本地配置可能被别的会话改过（0.4.2 的 `release/0.4.2` 因此留下 4 个占位身份的合并提交）。`gh pr merge --match-head-commit` 要用完整的 40 位 SHA。
- `sync.py audit` 把工作树里的 `node_modules` 软链接也算作未跟踪文件。借用别处的依赖时放在工作树外面，或者 audit 前删掉软链接。
- 本机默认的 pnpm（12）不认这份 lockfile 的 `pnpm.overrides`，CI 用 pnpm 9。前端命令用 `npx -y pnpm@9 …`；`corepack pnpm@9 run build` 不够，`build` 脚本里嵌套的 `pnpm run` 仍会从 PATH 取到 12，报 `ERR_PNPM_VERIFY_DEPS_BEFORE_RUN`（2026-10-21 集成门禁遇到）。
- repository 集成测试（testcontainers）在开发机上要加 `TESTCONTAINERS_HOST_OVERRIDE=127.0.0.1`。该机 Docker 给同一容器的 IPv4 与 IPv6 映射分配了不同主机端口，`localhost` 先解析到 `::1` 时会连到另一个容器（实测连到 redis），lib/pq 卡在 startup、不受 ping 超时约束，表现为测试无输出挂起。另外只同步 `backend/` 时 `TestBillingIntent_ReadOnlyAuditScript` 会因缺 `tools/billing-integrity/audit.sql` 失败，要连 `tools/` 一起同步（2026-10-28 遇到）。排查时若设了 `TESTCONTAINERS_RYUK_DISABLED=true`，harness 的 `TestMain` 以 `os.Exit` 退出、defer 的 `Terminate` 不执行，每次运行都会留下一对 postgres/redis 容器，测完按 `label=org.testcontainers=true` 核对后手动删除。
- 流式心跳测试不要把首输出截止时间设成 keepalive 的 2 倍。ticker 先于 opening 事件更新 `lastDataAt` 创建，第一拍可能因 `elapsed < interval` 被跳过，心跳推迟到第二拍，正好撞上 2 s 截止（CI 约 1/120 失败）。2026-10-28 把这组测试的截止时间改为 3 s、完成帧改为 3.2 s。

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

- 任务结束（合并、发布或放弃）后，由执行者把它在“当前交接状态”的小节与“进行中的工作”的条目原样移到 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md) 末尾；本文件只在“版本发布一览”加一行，把仍未结束的遗留并入“未结事项”。“当前交接状态”固定为：进行中的大任务小节、版本与环境速览、版本发布一览、未结事项。
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

### 2026-10-29 — 20261029-support-tickets-2fa — 本地开发与验证完成

- 请求/范围：对照 Sub2API 固定提交 `12a49ef4ef6a2f116389701ac8f1fe620f27bbf5`，补齐网站工单、OpenAI 邮箱/密码/TOTP 直接登录导入及托管密钥管理；用户追加普通管理员新建时可勾工单权限。GPT 6.1-sol 三路编码，主 agent 集成审查；平台用户原有TOTP不重复实现。
- 完成：工单默认关闭、用户归属/并发上限、管理员view/manage权限表/菜单/接口/脱敏；创建授权部分失败只重试权限。2FA独立加密专表、full-admin与强制step-up、内部worker Bearer/短租约、候选日志fsync→数据库同步提交→激活、uncertain锁定/只验证恢复、安全导出与页面秘密清理。服务端登录导入只创建账号，内部随机marker防伪造/复制且可崩溃重入；活跃任务保护账号删除/身份编辑。
- 文件/登记：新增迁移283/284及追加checksum，共331项；Wire真实生成；管理路由目录523条、权限模块18个；docs/OpenSpec/部署runbook、固定runtime源码与依赖、CI关键前端和worker mock门禁、`xy-support-tickets`与`xy-managed-openai-totp`具名补丁登记完成。
- 验证：最终 `go test -tags=unit ./...` 全部通过（服务层368.204s）；相关六包race与真实隔离PG -race五项通过。前端全量467文件3535项、typecheck/eslint/生产build、Go lint 0 issues及embed构建通过。Python工具96项、Node日志/runner12项、固定runtime离线预检与假服务smoke通过；generate+tidy两轮哈希一致，迁移只追加校验、scheduler-diff、docs一致性、代码提交干净树audit通过。
- 修复记录：首轮全量Go设置快照漏新字段、一次gofmt、工单异步竞态、失效任务队首阻塞、终态不可见、无候选verify_old、导入marker伪造均已修正并补回归；未绕过门禁。隔离PG已停止，保留仓外测试日志和三路报告。
- Git/后续：代码本地提交 `569bc4759`；本次收尾仅文档/记忆归档。工作分支 `feat/support-tickets-2fa-20261029`。未push、PR、合并、发版或部署；产品版本与Sub2API兼容基线不变。真实OpenAI协议仅mock，后续启用须配置worker与持久恢复目录并在获授权环境验证真实账号。

### 2026-10-30 — 20261030-support-tickets-2fa-pr — 已推送分支并创建 PR #168

- 请求/目标：用户先授权推送功能分支，再明确要求创建 PR。2026-10-29 的推送已完成，本轮以 `main` 为目标创建 [PR #168](https://github.com/liulixin-lex/xy2api/pull/168)，状态 OPEN、非草稿。
- 开始状态：分支 `feat/support-tickets-2fa-20261029`，HEAD `0e296b368` 与远端一致，工作树干净；原有三个提交包括方案 `811c7b07d`、功能 `569bc4759` 和验证记录 `0e296b368`。
- 完成操作：PR 正文列出工单、普通管理员授权、OpenAI 2FA、迁移 283/284、部署前提及 mock 验证边界；补正顶部的推送与 PR 状态，保留先前操作日志原文。本轮只修改本记忆文件，不改产品代码。
- 验证：2026-10-30 只读核实 [CI 40913579117](https://github.com/liulixin-lex/xy2api/actions/runs/40913579117) 七项作业成功，`shell` 的 runner_id 为 0、步骤为空且取消；[Security Scan 40913579111](https://github.com/liulixin-lex/xy2api/actions/runs/40913579111) 因 Vue I18n 及其传递依赖的高危公告失败。该分支没有修改前端依赖清单和锁文件；已通过的本地产品验证沿用前一条记录，本轮不重复运行长测试。
- 卡点/下一步：创建后 GitHub 报 PR 与当前 `main` 存在合并冲突，需在合并前处理；最新 PR 检查仍待结果，既有依赖漏洞需修复。本轮未添加审计例外、合并、发布、部署或运行真实 OpenAI 登录/MFA。交接记录提交前执行文档一致性和 diff 检查，提交后核对远端分支与 PR HEAD。

### 2026-10-30 — 20261030-release0414 — PR #168 集成冲突已处理

- 业务冲突仅账号操作菜单：保留 #163 账号管理即可复制/影子的权限，新增 2FA 仅超级管理员入口；权限目录自动合并后核对原权限不变。provenance/policy 按三方变更并集保留，不恢复已退役 literal。
- 已发布 330 条 SQL/checksum 原样保留；未发布工单与 2FA 顺延为 284、285，总计332，现行文档同步。两侧历史原文保留，旧迁移编号属于历史事实，本文追加更正。
- 前端依赖未改；main 已有用户批准至 2026-11-27 的 vue-i18n 例外，新页面无翻译 HTML 或 escapeParameter，合入后按既有例外验证。
- 下一步：专项验证及固定 HEAD 完整 CI，通过后合并与版本发布。

### 2026-10-30 — 20261030-release0414 — 安全依赖修复与版本准备

- 新安全公告 GHSA-qr28-fpjm-wq7f 阻断本次 CI，来自 query-string 的 decode-uri-component 0.4.1；以 pnpm override 固定修复版 0.5.1，仅该传递依赖与 integrity 变化，不新增例外。
- VERSION 与 provenance 产品版本升为 0.4.14，兼容基线保持 0.2.15。版本准备同 #168 集成提交验收，不做上游版本同步，无需上游 RC 晋级。
- 本机集成六包专项 unit、56 项权限/2FA前端、前端全量3544项、Python96项、Node12项通过；临时 PostgreSQL/Redis 下36个顶层仓储用例/套件含工单、2FA、283观测和既有看板 -race 通过，0跳过，临时资源已清理。依赖修复后全量前端与完整 CI 另验。
- 发布门槛：最终固定 head 的所有 CI/安全检查成功，常规合并后 annotated tag，独立验证正式附件/镜像及330→332迁移与回退；不连接生产或真实OpenAI登录/MFA。

### 2026-10-30 — 20261030-release0414 — 新增PR合并与0.4.14发布验收完成

- 授权与范围：用户要求全部新增PR无缝合并后发版0.4.14；起点main 93865dbcc，完整查询只开放#168。不部署、不操作真实OpenAI账号。
- 合并：普通merge保留双方provenance/policy和全部记忆，账号菜单保留#163复制/影子权限并加入超级管理员2FA入口。未发布迁移由283/284顺延284/285，原330条已发布SQL/checksum逐一不变；现行文档同步。
- 安全：新公告GHSA-qr28-fpjm-wq7f导致首轮集成CI失败，将decode-uri-component固定0.5.1，未加豁免。vue-i18n沿用既有用户批准例外至2026-11-27，新功能未引入翻译HTML或escapeParameter。
- 验证：六包权限/工单/2FA定向unit、依赖修后全量前端467文件3544项与build、Python96项/Node12项、隔离PG/Redis36个仓储顶层用例/套件-race零跳过、文档/调度/audit通过。#168最终固定head全部20项CI成功，常规合并后树一致；未重跑成功CI或放宽断言。候选演练首轮漏准备隔离管理员合规确认而收到423，修正测试夹具后通过，产品未为此修改。
- 发布：annotated v0.4.14固定合并提交，Release成功、非draft/prerelease；五平台附件双SHA、全部源码身份、双架构镜像与稳定别名独立核验。正式原生包全新/升级/回退/重升、工单与鉴权门禁通过，用户与Key指纹保持不变，临时资源清理。
- 记忆：自身进行中登记及#168旧交接原样追加归档，原归档前缀完整保留；纯记忆收尾使用独立PR。最终main除两份记忆外须与发行源码相同，最终PR/main/tag状态写本地报告，避免递归收尾PR。
- 后续边界：真实OpenAI网页协议需要获授权环境实测；2FA回退须先解决不确定任务、核实有效密钥并保留数据库及journal加密材料。N5校准、安全例外维护继续按既有边界处理；本次未部署生产。

### 2026-10-30T15:10Z — 20261030-account-group-tiering — 生产只读分析与方案 v1.0，待确认

- 请求/目标：用户要求查看线上 OpenAI 上游账号与分组分配情况，规划一套便捷、清晰的分组管理方案（可改代码）；随后明确只要最终网关改进方案。
- 开始状态：main `23b19fd9b`，工作树干净。
- 完成操作：经授权 SSH 只读查询生产（只读事务，未改数据或配置，SSH 已关闭）。核实生产实际运行 0.4.14 / `267f8577f`（更正顶部“最后一次只读核实为 0.4.12”，部署非本仓库 agent 操作）。发现：分组调整全靠手工逐个编辑，7 天约 550 次，按小时成功率反复进出高倍率组；账号 priority 全局、`account_groups.priority` 调度不读；利润门全部关闭；存在成本高于分组倍率的放置。方案为“分组声明质量门槛与成本上限、账号维护档位、系统对账生成 `account_groups`”，调度器不改，分 P0 配置、P1 矩阵与手工档位、P2 建议模式、P3 自动执行四阶段。
- 修改文件：仅本记忆；方案与可复跑查询在本地 `.work/tasks/20261030-account-group-tiering/`。
- 验证：无代码改动，未运行门禁。
- 卡点/风险：待用户确认 PLAN §8 的 5 个决策点（档位门槛、最低毛利率与 67 组处理、托管范围、手工例外时长、是否开自动执行）。
- 下一步：用户确认后按 PLAN 拆分实施（交其他 agent），先做 P0 配置项。

### 2026-10-31 — 20261031-workspace-slim — 工作区精简与规范化完成，开 PR 合并

- 请求/目标：用户要求精简多次迭代留下的数据，只保留核心文件，使工作区标准化、简洁，便于后续版本开发。先只读审计并给出分类方案，用户 2026-10-31 选定：`.work/` 已结束任务直接删除；删除已合并远端分支（含 PR 已合并的 2 个）；跟踪文件按审计清单全部精简；推送、开 PR 并合并。
- 开始状态：main `af83176ee`，工作树干净；本地 10 个分支（9 个已合并）与 1 个已合并分支的工作树；远端 104 个非 main 分支；`.work/` 1.6 GB（0.4.12 发布二进制与附件、764 MB 单测日志及 12 个已结束任务目录）。
- 完成操作：两路只读审计逐项核对引用、CI、`policy.json` 所有权与 provenance 链后删除：历史文档（可控调度、UI 调度设计、早期智商检测、v0.0.10 审计、v0.2.1 修复 4 份、计费完整性修复 2 份、调度前置调研、截图）、`docs/upstream-sync/` 中除 `v0.2.15.json` 外的报告（无工具读取）、OpenSpec `operator-role-20261027`、`upstream-balance-20261027`、`async-image-tasks-v2`、`stabilize-openai-iq-check`、`iq-detection-operations`、`assets/partners/`、`deploy/Makefile` 与 datamanagementd 三个文件。保留：上游来源的 2 个 OpenSpec（删除会让 audit 失败）、被代码注释与契约引用的韧性/份额调度设计、近期 TTL 与工单变更、`channel-monitor-v2-safe-defaults.md`（V2 模式仍可选，目录移到现行）、`UPSTREAM_SYNC_V0214/V0215.md`、`third-party/`。记忆重整：顶部旧节、channel-monitor-v1 登记与 10-26～10-29 日志原样追加到归档新章节，校验归档原前缀不变、移出内容逐行可在归档找到；修正过时状态（TTL“未实施”、#120/#121/#125“未发版”，三者实际在 v0.4.6）。分支：删除 9 个本地已合并分支与同级目录中的 `rec-next` 工作树，`git push --delete` 删除 101 个远端分支。本地 `.work/` 只留 `tasks/20261030-account-group-tiering/` 与 `reference/` 并更新其 README。
- 修改文件：85 个删除；`.gitignore`、`README_CN.md`、`deploy/README.md`、`docs/README.md`、两份记忆、`replace-controlled-with-share-scheduling` 的 design/proposal（改为指向 Git 历史）、`tools/docs_consistency.json`、`tools/upstream-sync/policy.json`、`tools/tests/test_upstream_sync.py`（断言 `assets/partners` 留在 `required_absent_paths`）。
- 验证：`check_docs_consistency.py` ok；`sync.py scheduler-diff` 与白名单一致；`tools/tests` 96 项与 pnpm 例外检查单测通过；`git diff --check` 通过。`sync.py audit` 在提交后的干净树运行，结果见 PR。未运行 Go/前端门禁：没有改动 Go、前端源码或依赖。
- 卡点/风险：删除的文件仍在 Git 历史中，未改写历史；`.work/` 已删内容不可恢复（用户选择直接删除）。之前给用户的计数“102 个远端分支”多算了 main，实际为 101 个。
- 下一步：PR CI 全绿后常规合并并删除本分支。未结事项见顶部，其中新增两项待用户决定：3 个含未合入提交的远端分支、`tools/gateway-resilience/` 发布演练脚手架；另有 `.github/SECURITY.md` 报告渠道应改为本仓库。

### 2026-10-31T14:12Z — 20261031-claude-cache-ui — PR 1：使用记录去掉 TTL 策略文字，本地验证完成

- 请求/目标：用户截图显示使用记录 TOKEN 列每行都有「TTL 策略: 不覆写 TTL · 未应用」，要求去掉。同时询问「Anthropic 缓存默认 TTL」与「Claude 自动缓存」的关系；只读分析与用户决定见“进行中的工作”，PR 2 另记。
- 开始状态：main `874560eb9`（0.4.15 之后），新 worktree，分支 `fix/usage-ttl-text-removal-20261031`。
- 完成操作：`UsageTable.vue` 删掉列表那一行、Token 悬浮详情的策略行和两个标签函数（管理员与用户使用记录共用这个组件）。保留缓存读取占比、1h 与「计费调整」标签、「TTL 明细未报告」提示，以及接口字段 `anthropic_cache_ttl_policy/applied`。中英文删除 8 个不再使用的 `usage.anthropicCacheTTL.*` 键。原测试改写为反向断言，并确认它在旧组件上会失败。同步 `ANTHROPIC_CACHE_TTL.md`、`USAGE_METRICS.md` 和 OpenSpec design/spec/tasks，在 `docs_consistency.json` 退役 testid、函数名与键名（正则逐项核对：命中全部旧名，不误伤保留的键和字段）。
- 修改文件：`frontend/src/components/admin/usage/UsageTable.vue` 及其 spec、`frontend/src/i18n/locales/{zh,en}/dashboard.ts`、上述文档、`tools/docs_consistency.json`、本记忆。
- 验证：定向 vitest 9 个文件 93 项、前端全量 468 个文件 3548 项、CI 关键清单 95 个文件 1021 项（用 pnpm 9）、eslint、vue-tsc、`check_docs_consistency.py`、`sync.py audit`（干净提交）与 `scheduler-diff` 均通过。浏览器验证与 PR 2 合并进行，见 PR 2 记录。未改 Go，未跑后端门禁。
- 卡点/风险：无。
- 下一步：推送并开 PR，CI 全绿后停在 PR，由另一位作者合并；不发版、不部署。

### 2026-10-31T14:30Z — 20261031-claude-cache-ui — PR 2：默认 TTL 移入「Claude 缓存」卡片，本地验证完成

- 请求/目标：用户问两项设置是否冲突、是否合并、是否按分组。结论：不冲突，执行顺序为先补断点、再统一时长、最后做 4 槽与长 TTL 顺序修正；保留两项，放进同一张卡片；TTL 不按分组。用户选定实施。
- 开始状态：叠在 PR 1 上，分支 `feat/claude-cache-settings-card-20261031`。
- 完成操作：
  - `ClaudeCacheFallbackSettings.vue` 改为两行加一个保存：第一行「自动补缓存」（开关与分组），第二行「缓存时长（跟随全局的密钥）」（5 分钟/1 小时两态按钮）。
  - 保存只提交改动过的字段，并逐项核对回显；只改时长时不提交、也不校验分组策略（后端对省略字段保持原值）。
  - `SettingsView.vue` 删掉原 TTL 下拉、选项、表单字段与提交字段，`SettingsForm` 不再含该字段，避免主表单用旧值覆盖。
  - 中英文文案修正互相矛盾的说法（原卡片写「默认缓存 5 分钟」）。同步两份文档与两个 OpenSpec 变更，退役旧下拉的 id、选项名与 i18n 键（带命名空间，不误伤 KeysView 和后端的同名标识）。
  - 窄屏下第二行改为上下排列。
- 审查：四个维度分别查找、逐条反驳核验。确认 3 条低级别问题并已修复：旧后端测试夹具缺时长，导致策略校验没有被单独覆盖；退役正则漏了 `-hint` id；英文提示引用了已删除的标签名。驳回 2 条：模板字符串形式的退役，以及 Bedrock 旧模型不支持 TTL 的措辞。
- 修改文件：上述两个组件及其 spec、`frontend/src/i18n/locales/{zh,en}/admin/settings.ts`、`docs/ANTHROPIC_CACHE_TTL.md`、`docs/CLAUDE_CACHE_FALLBACK.md`、两个 OpenSpec 变更、`tools/docs_consistency.json`、本记忆。
- 验证：
  - 卡片、设置页与 locale 定向测试 77 项；前端全量与 CI 关键清单（pnpm 9）；eslint、vue-tsc；`check_docs_consistency.py`。
  - 本地后端连一次性 PostgreSQL 18 和 Redis，加 vite，用 playwright-core 和本机 Chrome 跑 20 项检查，全部通过：
    - 管理员与用户使用记录不再出现 TTL 策略文字，1h 标签、读取占比和「TTL 明细未报告」仍在；
    - 设置页只剩一张卡片；只改时长时请求只带 TTL，只改分组时只带策略，刷新后保持；
    - 主表单保存（270 个字段）不含 TTL，后端值不变；
    - 手机宽度无横向溢出，无页面错误。
  - 未改 Go，未跑后端门禁。
- 卡点/风险：无。合并顺序须先 PR 1，再 PR 2（PR 2 的 base 为 PR 1 分支）。
- 下一步：推送两个分支并开 PR，CI 全绿后停下；不合并、不发版、不部署。用户待决事项见“进行中的工作”。

### 2026-10-31T16:35Z — 20261031-claude-cache-ui — PR 2 追加：提示写明 5 分钟会缩短客户端的 1 小时

- 请求/目标：用户对照参考仓库后同意建议顺序，第一项是在 #176 写明现有行为（只改文案）。
- 开始状态：分支 `feat/claude-cache-settings-card-20261031`，HEAD `e1d523f3e`，与远端一致；#175、#176 均 OPEN、CI 全绿。
- 完成操作：卡片「缓存时长（跟随全局的密钥）」提示与 Key 弹窗 `keys.cacheTTL.hint`（中英文）写明：选 5 分钟（含跟随全局 5 分钟）时，客户端自己写的 1 小时也改为 5 分钟，保留客户端设置要选「不覆写 TTL」；Key 弹窗补写入价倍数和 1 小时的适用间隔。`ANTHROPIC_CACHE_TTL.md` 的 inherit 行与 OpenSpec tasks 同步。不改行为、接口和控件。
- 修改文件：`frontend/src/i18n/locales/{zh,en}/admin/settings.ts`、`frontend/src/i18n/locales/{zh,en}/dashboard.ts`、`docs/ANTHROPIC_CACHE_TTL.md`、`openspec/changes/anthropic-cache-ttl-20261030/tasks.md`、本记忆。
- 验证：KeysView、卡片、设置页与 locale 定向 vitest 4 个文件 108 项、改动文件 eslint、vue-tsc、`check_docs_consistency.py`、`git diff --check` 通过。
- 卡点/风险：无。若之后上线「跟随全局只延长、不缩短」，这两条提示要按新规则改。
- 下一步：推送后等 #176 CI 全绿；后端项见 `20261031-tool-cache-markers`。

### 2026-10-31T19:40Z — 20261031-tool-cache-markers — 转换路由保留工具缓存标记，本地验证完成

- 请求/目标：用户对照参考仓库 kiss-kedaya/sub2api 后同意建议顺序：先修 Chat/Responses → Anthropic 丢工具结果缓存标记，同 PR 做转换路由的顶层/末块 TTL 调和与 1h 降级观测；「只延长不缩短」另待确认。
- 开始状态：从 #176 分支 `2fa64cc18` 新建 worktree，分支 `fix/tool-cache-markers-20261031`。
- 完成操作：
  - 多 agent 只读梳理转换链、finalize、观测、参考提交 `aefe48ebf`/`8025f5aad` 与治理规则后定稿设计，3 路挑错；主协调补充：Chat 工具标记只经内部字段带给 Anthropic 转换，不写进发往 OpenAI 的 Responses 请求。
  - apicompat：Chat `role=tool`/`role=function` 标记（消息级，否则最后一个带标记分片）落 `tool_result`，只含 `tool_calls` 的 assistant 标记落最后一个 `tool_use`；Responses `function_call_output` 取项级或数组 `output` 最后一个分片标记，`function_call` 落 `tool_use`；无嵌套副本。Chat 中转经 JSON，内部字段按 input 位置对齐恢复，长度不符则不恢复。
  - service：4 个转换出口改用 `enforceConvertedCacheControlLimit`，顶层 automatic 与末块显式 5m/1h 不一致时统一为 1h（双向）；原生 Messages、透传、Bedrock、count_tokens 不变。原始声明识别覆盖 Responses 工具项；结算日志 `ttl_status` 增加 `downgraded`，策略取自结算结果。
  - 文档：`ANTHROPIC_CACHE_TTL.md`（工具标记规则、beta 说明、`ttl_status` 取值、1h 降级巡检 SQL）、`CLAUDE_CACHE_FALLBACK.md`、两个 OpenSpec 变更的 dated 后续；policy 与 `UPSTREAM_BASE.json` 登记具名补丁。
- 审查：Claude 三个维度（wire 正确性与路由隔离、转换与声明一致性、测试/治理/文档）查找并逐条反驳核验，2 条均为设计内的已知取舍，驳回；gpt-6-astra（xhigh，只读快照）报 1 条小问题：`tool_search_output` 输出会被转成字符串，文档不应承诺分片标记，已收窄文档与 OpenSpec。
- 修改文件：apicompat `types.go`、`anthropic_cache_control.go`、`chatcompletions_to_responses.go`、`responses_to_anthropic_request.go`；service `claude_cache_fallback.go`、`gateway_anthropic_cache_ttl.go` 及 4 个转换出口；测试 `anthropic_cache_test.go`、`claude_cache_fallback_test.go`、`gateway_anthropic_cache_ttl_test.go`、新增 `anthropic_native_cache_ttl_test.go`（unit）；上述文档、OpenSpec、`tools/upstream-sync/policy.json`、`UPSTREAM_BASE.json`、本记忆。
- 验证：gofmt；`go build ./...`；apicompat/service/handler 在四种 tag 下 vet；`go test -tags=unit` apicompat/service（372s）/handler（56s）全过；无 tag 的缓存测试 34 个顶层用例；相关 `-race` 53 个顶层用例零跳过；golangci-lint v2.14.0 0 issues；`check_docs_consistency.py`；tools 单测 96 项；干净提交上 `sync.py audit` 与 `scheduler-diff` 通过。临时回退实现后新测试按预期失败（只锁现状的守卫用例除外）。未做真实上游验证。
- 卡点/风险：真实 Anthropic/Vertex/OAuth 对 `tool_result`/`tool_use` 标记的接受与命中、Kimi 等 Anthropic 兼容上游的接受度未验证；只给工具结果打标记的 Responses 客户端不再触发分组补缓存（改为 `client_declared`）。发现的既有问题见“进行中的工作”。
- 下一步：推送并以 #176 分支为 base 开 PR，CI 全绿后停下；不合并、不发版、不部署。
