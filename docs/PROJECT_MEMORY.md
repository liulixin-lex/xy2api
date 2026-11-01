# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-29 及以前（0.4.13 周期及更早）的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-10-31）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### 分组质量门槛与兜底计费（2026-11-01T03:40Z 更新：修复范围已定，修复轮进行中；暂不发版）

- **背景**：减少手工分组维护，避免少成员组整组无号，并兼顾缓存命中。
- **目标**：按用户批准方案实现质量门槛、恢复/保底/固定、排序与满载借用、实时利润门、兜底服务组计费及精简后的管理界面。
- **要求与约束**：无新迁移或部署前提；仅本地集成。推送、PR、main 合并和发版待用户授权；不部署、不访问生产。
- **已完成**：`feat/gqg-wp5-integration` 已合 WP1 `6c26a11f2` 与最终 WP4 `27f7cf861`（合并 `56d557149`）；后端固定为 `8aa768168`。本机 unit/影响包复验、测试主机完整 Docker integration（60 包、0 FAIL/panic）、前端 470 文件/3609 项及构建、lint/文档/Wire/审计通过。32 场景与 tasks 5.x 全部收齐，发布草稿、配置清单与最终交付报告齐备。
- **已完成（复审，21:35Z）**：对 `231cc5a91` 做五路只读复审与缓存专项分析，主持人逐条回读核实。
- **已完成（定范围，11-01T03:40Z）**：用户授权主持人自行取舍。合并执行会话遗留与复审结论后决定：修 F1–F5、P2-1/2/4/6/7/8/10/13/14/15、Q1/Q2/Q3/Q6、Q7 与 D1（均收窄），其余写文档或不做（Q4、P2-9、P2-11 代码、D3–D8、C1–C7）。理由见仓外 `reviews/fix_round_decisions_20261101.md`。
- **未完成**（按顺序）：
  1. 修复轮：四个修复包 `feat/gqg-fx-a|b|c|d`（质量核心、调度、WS/handler、前端），主持人逐包审查后合入基线，并同步文档与发布草稿；
  2. 修复后重跑全量门禁（含测试主机 Docker 集成）；
  3. 用户授权后推送、PR、合并和发版；
  4. 升级后按 `PLAN.md` §11 一次配置。
- **卡点与风险**：
  - 复审发现 5 项发版前必须修：
    - F1：兜底中的 WS 透传连接，第 1 轮按原组计价；
    - F2：成员换组或重新入组后丢失熔断与重置历史，导致误熔断；
    - F3：满载借用不跳过已满账号，每请求 O(N) 次查库与抢槽，并抢占排队者；
    - F4：legacy 兼容循环叠加 2 秒短等；
    - F5：毛利率框清空后提交 0%。
  - F3、F4 为升级即生效的回归，与开关无关。
  - 错误率依赖运维监控写 `ops_error_logs`，监控关闭时只剩首字门槛，升级前须确认。
  - 原有已接受限制见发布草稿。
- **关键位置**：[`group-quality-gate-20261031/tasks.md`](../openspec/changes/group-quality-gate-20261031/tasks.md)；仓外 `reviews/post_impl_review_20261031.md`（复审清单、缓存分析、只读 SQL 草稿；复现用例在 `reviews/post_impl_repro/`）、`implementation/WP5-report.md`、`implementation/RELEASE_NOTES_DRAFT.md`。

### 工作区精简与规范化（2026-10-31；本分支 PR 合并即完成）

- 仓库：删除 85 个跟踪文件（跟踪内容约 71.5 MiB → 61.4 MiB）：已退役或被取代的历史文档 15 个、旧上游同步报告 11 个（只留与基线对应的 `v0.2.15.json`）、已发布且有现行文档的 OpenSpec 变更 5 个、违反无赞助政策的 `assets/partners/`（同时登记 `required_absent_paths`）、失效的 `deploy/Makefile` 与 datamanagementd 部署文件（后端该功能是废弃桩）。目录、白名单、`docs_consistency.json`、`policy.json` 与引用同步更新；不改业务代码、迁移、标签与发布产物。
- 记忆：本文件由约 157 KB 精简到约 31 KB；0.3.0–0.4.15 的交接原文、channel-monitor-v1 登记与 2026-10-26～10-29 的操作日志原样移入归档，顶部改为版本发布一览与未结事项。
- 分支：本地已合并分支与 `rec-next` 工作树已删；GitHub 删除 101 个已合并分支（含 PR 已合并、只多出文档提交的 2 个），只剩 main 与 3 个含未合入提交、待用户决定的分支。
- 本地：`.work/` 由 1.6 GB 降到约 0.2 MB，只留进行中的账号分组方案与 `reference/`；已清理 Python/TS 缓存与 `.DS_Store`。

### 版本与环境速览（2026-10-31 更新）

- 最新正式版 XY2API v0.4.15（2026-10-31），Sub2API 兼容基线 0.2.15，迁移共 333 项（最后为 286）。
- 生产：2026-10-31 只读核实已在线更新到 0.4.15 / `10fbc9c72`（当日 14:33 CST，部署非本仓库 agent 操作；容器镜像标签仍显示 0.4.11，版本以启动日志为准）。部署与上线观察每次单独授权，外部状态以重新核实为准。
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

- **部署与上线观察**：0.4.15 已于 2026-10-31 在线更新上线（非本仓库 agent 操作），上线观察未做；Anthropic TTL 的真实供应商缓存命中率与收益未验证；0.4.9 的过渡开关与 fast_fail 待上线 7 天后复核；智商检测参数化题未做真实上游校准，旧结构待上线观察 7 天后删除，单账号“不降智”分组需运维加第 2 个账号；N5 阈值须上线后校准；0.4.12 上线约四小时的只读观察未证明首字与缓存全面改善（样本小，不作因果归因）。
- **功能遗留**：WS 余额阶梯与复位暂缓；Anthropic 暂存失败尝试的上游成本由网关承担；非 OpenAI breaker 为进程内状态，多实例各自计数；迁移的空位预检有抢槽竞态；管理端保存过设置的部署 Gemini 截止仍为 90 秒；0.4.5 OpenSpec tasks §7 的 7.2/7.3/7.5/7.6；渠道监控 v1 待真实上游验证（兼容矩阵、推理模型响应时间）与审查 P2-7；无号兜底 INC-P2-1（WebSocket 快照毫秒级窗口，不阻断）。
- **工单与 2FA**：真实 OpenAI 网页登录/MFA 只有 mock；启用 2FA 须部署 Worker、固定运行时和加密恢复目录，回退前须解决不确定任务并保留密钥与恢复日志。
- **安全与依赖**：vue-i18n 安全例外 2026-11-27 到期前处理；两台主机的 SSH 密码曾出现在对话中，建议改用密钥并更换；生产 settings 中的明文代理凭据建议轮换；`.github/SECURITY.md` 仍指向上游 Sub2API 的报告渠道，应改为本仓库（该文件未分类，改动需同时登记 `policy.json`）。
- **待用户决定**：Codex 历史联网搜索导致 502（2026-10-17 诊断，原文已归档）——恢复映射或调整白名单，代码改进前先核实 0.4.0 的 `resilience_web_search_replay` 是否覆盖 apikey 中转账号；XY-image 0.0.17 先交接并经用户确认；远端保留的 3 个含未合入提交的分支（`feat/native-stream-recovery-20260930`、`fix/native-stream-concurrency-20261001`、`fix/signature-retry-preserve-tool-result-media`）是否删除；`tools/gateway-resilience/` 下 `staging/`、`fault-upstream/`、`cdn-sim/` 发布演练脚手架 0.4.6 后未再使用，是否删除。
- **测试稳定性**：CI 偶发 `TestCompatPreludeCommentHeartbeatStillAllowsFailover/enabled` 等心跳计时用例；2026-10-28 已放宽一组截止时间，其余未定位。
- **仓库外环境收尾**：测试服务器的候选镜像、浸泡与演练目录、`xy2api_upgrade` 库按 staging-validation §11 清理；预上线主机的两份切换前备份在正式部署后删除。

## 进行中的工作

### 20261030-account-group-tiering v3（2026-10-31 开始；Claude 主持，Opus 红队与 Codex gpt-6-astra 协作）

- 目标：按用户构想做“分组质量门槛”，不要影子模式、观察期和无用测试；只出方案，用户确认前不改代码。当前状态、未完成与卡点见顶部“分组质量门槛与兜底计费”。
- 范围：生产只读核实；过程稿、证据、三路评审都在本地 `.work/tasks/20261030-account-group-tiering/`。
- 状态（2026-10-31 12:55Z）：两轮逐题确认与四轮三路讨论完成，用户决定已全部写入方案 v3（`PLAN.md`，v2、v1.0 移入 `archive/`）。13:14Z：OpenSpec 与派工包已备好，等 astra 核签。未改代码、未改生产。
- 状态更新（2026-10-31；第四轮）：用户已明确兜底时由服务组 F 决定利润门与价格，API Key 所属组 G 保持权限/限额边界；用户侧仍显示 G，管理端 usage 行改为 G 主标签并保留 F 路径提示。正在只读核验历史、计费与展示链路并修订 `reviews/round4_astra.md`；未改业务代码、未访问生产。
- v3 要点：
  - 质量门槛默认错误率 10%、续轮首字 p90 20 秒，各组相同；
  - 熔断 5→30 分钟、80% 恢复、试用；熔断改绑；
  - 保底 max(2, 可用一半)；固定账号；“恢复状态”顺带重置；
  - 排序（v3.2）：档位（合格快档 → 试用 → 其他，零缓存率 >30% 不算快档）→ 优先级 → 负载 ≥80% 排后 → 成本；满载先等 2 秒，再借用同会话稳定的替补账号；
  - 熔断不移出分组；判定只用有效数据（熔断前旧数据在熔断结束后、以及重启前的数据都不计入）；
  - 利润门用实时成本，默认毛利率 20%，界面显示换算出的上游倍率上限与通过数；
  - 兜底触发计入两道门，到兜底组按兜底组规则选号并**改回按兜底组计费**（权限与限额仍按原组）；
  - 管理端用量显示原组加兜底路径；可用数计入两道门。
  - 不做零缓存门槛、分组级 `require_iq`、保本放行。成员关系与并发上限由用户自行配置。
- 已作废的过程结论：
  - v2 里“431 上游不支持缓存”有误，实际是监控小请求；
  - astra 第三轮回放误用 10 秒熔断线，已按用户口径重跑 `sim/replay_round3_claude.py`。

### 20261031-group-quality-gate 实施（2026-10-31T15:30Z 开始；Claude 主持派工与监督）

- 目标：按 OpenSpec `group-quality-gate-20261031` 与本地 `handoff/` 实施 WP1–WP5。后端由 Codex gpt-6-astra（xhigh）执行，前端由 Claude Opus 5.5（xhigh）执行，审查与红队由 Opus 5.5（xhigh）执行。
- 分支：各包从基线 `feat/group-quality-gate-20261031` 切出 `feat/gqg-wp<N>-*`，各用独立 worktree；主持人把通过审查的包合入基线，WP5 做最终集成。
- 协调约定：各工作包 agent 不改本文件，由主持人在基线上维护；交付报告与审查写在本地 `implementation/`、`reviews/`。
- 状态（2026-10-31T17:40Z）：
  - 基线 `c249ff211` 已合入 WP1 接口骨架、WP3-A（兜底按服务组计费，审查有条件通过、P2 已修）与 WP1 核心（审查进行中，问题在 WP1 分支修复后再合）。
  - WP2 调度首轮审查不通过（WS turn 1 续链循环、连接级豁免泄漏、非 OpenAI 粘性逃逸与排队满回退被删），修复中，并补完旧质量路由下的满载借用（2.7）。
  - WP3-B（兜底触发、实时成本、预览、可用数）开工；WP4 前端审查修复完成，开始联调。
  - 已知限制：Grok 异步视频待结算记录无生成时服务分组，跨组轮询时按轮询分组计价（需加字段，不在本变更）。

### 20261031-group-quality-gate WP5 集成（2026-10-31T20:06Z：完成，待授权）

- 分支 `feat/gqg-wp5-integration`：WP1 合入 `a958a61af`；索引计划测试显式 ANALYZE 修正 `8aa768168`；最终 WP4 合并 `56d557149`，后端零差异。阶段详情已原样归档。
- 门禁与交付：影响包 unit service 378.725s、repository 16.967s；本机仓储集成通过；测试主机 Docker 全仓 60 包通过；前端四门禁 470 文件/3609 项通过。tasks 5.x/32 场景、最终显示口径、发布说明和配置清单已收齐，最终文档提交的干净树审计见仓外报告。
- 下一步：无实施阻塞，等待用户授权推送/PR/合并/发版；测试环境已清理，未部署或连接生产。

### 20261031-group-quality-gate 实施后复审（2026-10-31T20:40Z 开始，21:35Z 复审完成；Claude 主持）

- 范围：对基线 `231cc5a91` 做五路只读审查，分别为 WP1 核心、WP2 调度、WP3 计费、前端、缓存端到端。结论、复现与 SQL 草稿见顶部“关键位置”。
- 状态：复审完成，等用户确定修复范围；修复轮实施交其他 agent。没有改业务代码、推送或连接生产。

### 20261101-group-quality-gate 修复轮（2026-11-01T03:40Z 开始；Claude 主持）

- 范围：按 `reviews/fix_round_decisions_20261101.md` 修复，四个包各用独立 worktree，从基线切 `feat/gqg-fx-*`。各包 agent 不改本文件，由主持人维护。
- 状态：派工中。

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

### 2026-10-31T10:30Z — 20261030-account-group-tiering v2 — 方案定稿，待用户确认

- 请求/目标：用户提出新构想：账号可进全部分组；每个分组设首字、缓存、错误率阈值；组内熔断、自动停用与恢复、打分。要求结合生产数据与各类 AI 网关做法，给出精简开发方案，并与 Codex gpt-6-astra（xhigh）互补讨论。要求不做影子模式、无用测试和兜底。
- 开始状态：main `874560eb9`，工作树干净。
- 完成操作：
  - 经授权 SSH 只读查询生产（只读事务），结束后关闭 SSH 通道。
  - 核实生产已在线更新到 0.4.15，已更正顶部状态。
  - 核实所有分组实际走原版调度；利润门、低倍率优先、API Key 健康熔断均关闭。
  - 7 天人工运维量：改分组 549 次、停启调度 276 次、恢复 135 次、测试 233 次。
  - 50 组 10-30 的 394 次无号 503：组内无可调度成员，人工加入 355 后恢复。
  - 发现续轮口径能把上游缓存与首字从调度影响中剥离出来，例如 431 续轮零缓存率为 100%。
  - Codex 独立评审代码与回放，并对定稿核签（有条件通过），修正项已全部并入。双方分歧与取舍记录在 PLAN §8。
- 修改文件：仅本记忆（顶部生产版本、未结事项、进行中条目）。方案与证据在本地 `.work/tasks/20261030-account-group-tiering/`，v1.0 原文改名为 `PLAN_v1.0.md` 保留。
- 验证：`python3 tools/check_docs_consistency.py` ok。无代码改动，未运行其他门禁。
- 卡点/风险：等待用户确认 PLAN §9。成本为默认 1.0 的账号需先补真实成本，才能开利润门。
- 下一步：用户确认后交实施 agent，按 PLAN §4 和 §5 执行；部署另行授权。

### 2026-10-31T12:32Z — 20261030-account-group-tiering — 第四轮 Astra 核验完成

- 请求/目标：用户更新兜底决策，要求沿用第四轮代码/数据评审，补查原组计费的 Git 历史、F 计费完整改点与用户/管理员可见字段。
- 开始状态：`main` @ `874560eb9`；工作树已有 `docs/PROJECT_MEMORY.md` 改动，核实为此前交接状态更新；未覆盖其他改动。
- 完成操作：仅读本地源码、Git 历史和已导出回放；核实 v0.4.0/#100 首次把路由与计费移至 F，v0.4.6/#125 引入现行 G 计费口径；写入 round4 Astra 评审，包括 G/F 计价/授权拆分、WS 每 turn、用户价显示风险、管理员 usage 路径、四个 fallback mapping 回放结果。未访问生产、未改业务代码或模拟脚本。
- 修改文件：`.work/tasks/20261030-account-group-tiering/reviews/round4_astra.md`；本文件仅更新该任务状态并追加本条日志。
- 验证：`git diff --check` 通过；评审 73 行，低于 120 行；未运行业务测试。
- 卡点/风险：用户指定用户 UI 保持 G，但 usage 会展示 F 的 `rate_multiplier/actual_cost`，尚未决定是否加不含 F 名称的解释文案。
- 下一步：将评审交主持人综合；后续若实施，拆分 G entitlement 与 F pricing，部署另需授权。

### 2026-10-31T13:14Z — 20261030-account-group-tiering v3 — 方案定稿，OpenSpec 与派工包就绪

- 请求/目标：用户完成第二轮逐题确认（含兜底改回按兜底组计费、用户侧扣费如实显示、成员与并发自行配置），要求写好最终方案后规范工作区、分类放好开发资料，以便派 agent 群实施。
- 开始状态：main `874560eb9`；工作树仅有本记忆的未提交改动（本任务此前的登记）。
- 完成操作：
  - 用户决定汇总为方案 v3（本地 `PLAN.md`）；按用户口径重跑熔断节奏回放（`sim/replay_round3_claude.py`），astra 第三轮回放误用 10 秒熔断线，已作废；
  - 新增 OpenSpec `group-quality-gate-20261031`（proposal、design、tasks、两份 spec），不含生产数据；登记 `docs/README.md` 与 `policy.json` ownership；
  - 本地任务目录分为 `handoff/`（派工）、`implementation/`、`reviews/`、`sim/`、`evidence/`、`archive/`（v2、v1.0 与作废回放）；派工包引用的函数与文件已逐一核实存在。
- 修改文件：`openspec/changes/group-quality-gate-20261031/**`（新增）、`docs/README.md`、`tools/upstream-sync/policy.json`、本记忆。
- 验证：临时 index 纳入新目录后 `check_docs_consistency.py` ok（真实 index 不变）；`git diff --check` 通过。无代码改动，未运行其他门禁。
- 卡点/风险：astra 对 v3 的核签未完成；基线分支未创建，提交须用户同意。
- 下一步：并入核签意见；经同意后提交到本地基线分支；用户派工。

### 2026-10-31T14:10Z — 20261030-account-group-tiering v3.2 — 方案定稿，提交本地基线分支

- 请求/目标：用户同意提交本地基线分支；追加要求为熔断账号自动恢复并重新加入、尽量保证高缓存命中，并讨论利润率与上游倍率上限，随后对 Q1–Q5 全部同意。
- 开始状态：main `874560eb9`，工作树为本任务的记忆改动与未跟踪的 OpenSpec 目录。
- 完成操作：
  - astra 核签 v3（有条件通过），逐条核实后并入：兜底时序描述、计价双身份、装门范围与利润门相同、WS 续链豁免、分层保底与可服务、恢复状态显式选项（核实“测试”与额度重置也调用该函数）、OpenAI 分组限定。
  - astra 复核 v3.1：补“模型不支持不触发兜底”用例与保底封顶后，提交 `07776536f`。
  - 按用户追加要求修订为 v3.2：有效数据口径修正、试用档位、满载先等 2 秒、新会话避开负载 ≥80%、零缓存率降档、利润率加显示换算上限。
  - astra 复核 v3.2：补进程启动起点、试用累计计数、档位记忆清除、三条满载路径共用同会话稳定顺序、不限并发账号负载中性。
- 修改文件：`openspec/changes/group-quality-gate-20261031/**`、`docs/README.md`、`tools/upstream-sync/policy.json`、本记忆；本地 `PLAN.md`、`handoff/`、`reviews/`。
- 验证：`check_docs_consistency.py` ok；`git diff --check` 通过；`07776536f` 提交后在干净树上 `sync.py audit` 通过；入库文件没有账号、站点、成本与生产指标。未运行 Go 与前端门禁：没有代码改动。
- 卡点/风险：见顶部“分组质量门槛与兜底计费”。
- 下一步：用户按 `handoff/README.md` 派实施 agent；推送、PR、合并、发版另行授权。

### 2026-10-31T19:05Z — 20261031-group-quality-gate WP5 — 第一段后端门禁完成

- 请求/目标：在已审查后端基线上完成全量门禁和具名补丁登记；第一段结束后交回主持人，等通知合并 WP4。
- 开始状态：`feat/gqg-wp5-integration` @ `be0c2629d`，工作树干净。
- 完成操作：核对交接契约与各包审查；补齐唯一新增具名补丁 `xy-openai-group-quality-scheduling` 的 provenance（`c8d38c070`），两份补丁集合一致；未改业务实现、测试断言或测试入口。
- 修改文件：`UPSTREAM_BASE.json`、本记忆；完整命令、门禁结果、overlay 与日志在仓外交接包 `integration/wp5/`。
- 验证：`go test -tags=unit -p=2 -timeout=20m -count=1 -json ./...` 全通过（service 383.103s，低于 480s 报告线）；隔离 PostgreSQL/Redis 下 `go test -tags=integration -overlay <仓外文件> -timeout=20m -count=1 -json -skip <三项Docker专用用例> ./...` 通过（59 包，repository 22.098s）；`go vet` 默认/unit/integration/e2e 全通过；全仓 golangci-lint 两个 issues 上限均为 0，0 issues；`ci_gate.py` 五包 478 项 race、0 SKIP；scheduler-diff、docs consistency、Python 工具 96 项、diff 检查通过。Wire 重生成 `wire_gen.go` 无差异，工具产生的 go.sum 变化已恢复。
- 卡点/风险：本机没有 Docker，按主持人决定排除两项旧版本迁移与一项备份用例；主持人已另起真实 testcontainers 全仓验证，待结果。集成另有 11 个既有显式跳过项（环境/平台/opt-in/已有 TODO），清单保存在仓外；未新增跳过。临时 PostgreSQL/Redis 进程已停止、数据目录已删除。
- 下一步：接收 Docker 集成结果与 WP4 合并指令，执行第二段前端门禁、干净树 audit、逐场景验收、发布说明和最终记忆；不推送、PR、main 合并、标签或部署。

### 2026-10-31T19:14Z — 20261031-group-quality-gate WP5 — 第二段本地集成与交付完成

- 请求/目标：主持人授权合入 WP4 `a2b0c70d4`，完成前端门禁、干净树审计、逐场景验收、发布草稿、配置清单和记忆；Docker 全量结果另行同步。
- 开始状态：`feat/gqg-wp5-integration` @ `330194d39`，工作树干净；后端第一段门禁已通过。
- 完成操作：WP4 无冲突合并为 `8739f463b`，后端与 `be0c2629d` 完全相同；将 WP4 已接受的旧缓冲合入毛利、空组中性预览和界面状态同步到文档/design/spec；32 个 Scenario 全部对照实现与既有通过用例，tasks 4.x/5.x 更新。仓外发布草稿写明升级计费、历史浮窗、排序、OAuth 与全部已接受限制，配置按 PLAN §11；报告不超过 60 行。
- 修改文件：WP4 前端 43 文件（合并）；`docs/GROUP_QUALITY_GATE.md`、`docs/README.md`、OpenSpec design/spec/tasks、两份记忆。旧顶部阶段状态与本人详细登记原样追加归档，其他 agent 条目及历史日志保留。
- 验证：pnpm 9 frozen-lockfile 安装后 typecheck、lint:check、test:run（470 文件/3607 项）、build 全通过；`8739f463b` 干净树 `sync.py audit` 通过，node_modules 非软链接；文档一致性与 diff 检查通过。沿用后端第一段实际结果；部署/config/Ent/迁移目录相对功能基线无差异。最终文档提交后的固定 HEAD 审计结果记入仓外报告。
- 卡点/风险：本机 overlay 集成的排除/skip 如前条日志；完整 Docker 结果未收到，tasks 5.2 仍未勾选。WP4 浏览器冒烟为本地 mock，未代操作人接受管理合规承诺、未宣称真实管理后端浏览器或供应商验证。无新增部署前提、无推送/PR/main 合并/标签/部署/生产连接。
- 下一步：收到主持人 Docker 结果后补 tasks 5.2 与报告；再等待用户授权推送、PR、合并和发版。

### 2026-10-31T20:06Z — 20261031-group-quality-gate WP5 — 最终集成与完整门禁收齐

- 请求/目标：合 WP1 成员有效指标与事务隔离，独立修复 Docker 暴露的既有索引计划测试，再合用户要求的精简 WP4；收齐最终 Docker 证据、文档和记忆。
- 开始状态：`feat/gqg-wp5-integration` @ `9e1da6234`，工作树干净；随后按新增指令继续执行最终 WP4。
- 完成操作：WP1 `6c26a11f2` 合为 `a958a61af`；`8aa768168` 仅在计划测试事务内加 `ANALYZE usage_logs`，消除 autovacuum ANALYZE 时机依赖，索引断言未改；WP4 `27f7cf861` 合为 `56d557149`，backend 与 `8aa768168` 零差异。成员 metrics/headroom、熔断中完整窗口显示、紧凑提示/芯片与列顺序同步现行文档、OpenSpec、注释及退役键登记。
- 修改文件：WP1 合并 8 文件、WP4 合并 17 个前端文件；计划测试、现行文档/目录、OpenSpec design/spec/tasks、类型注释、docs_consistency、两份记忆。仓外交付报告与发布草稿覆盖计费/历史文案、界面/指标、测试稳定性、配置清单与限制，报告少于 60 行；其他 agent 事项和历史日志保留。
- 验证：service/repository unit 378.725s/16.967s；本机隔离仓储 integration 782 顶层用例通过（22.355s），临时服务已清理。主持人测试主机对源码 `8aa768168`（抽查两文件 SHA-256 一致）执行 `go test -tags=integration ./... -count=1 -p 2 -timeout=40m`：60 包 ok、58 无测试、0 FAIL/panic，repository 91.3s、service 175.3s、migrations 及旧版本迁移/备份均通过。原 `be0c2629d` 的唯一计划断言失败已消除。lint 0 issues、scheduler-diff/docs/diff 检查通过；最终前端 typecheck/lint/test/build 全通过（470 文件/3609 项），`56d557149` 干净树 audit 通过；最终文档提交审计固定 HEAD 记仓外报告。
- 卡点/风险：无实施或门禁阻塞。WP4 真实隔离后端冒烟已获用户临时实例合规承诺授权并通过，合成数据与环境已清理；此前 mock 验证为历史阶段，不再是最终联调限制。真实上游/生产未测试；已接受限制保留在发布说明。
- 下一步：交用户授权推送、PR、合并与发版；未推送、未改 main、未打标签、未部署或连接生产。

### 2026-10-31T21:35Z — 20261031-group-quality-gate 复审 — 实施后复审与缓存分析完成，待用户定修复范围

- 请求/目标：用户要求对已完成的实施再审查一遍，并分析缓存还能如何更高效、稳定、命中率更高。
- 开始状态：`feat/group-quality-gate-20261031` @ `231cc5a91`，工作树干净；生产 SSH 控制通道未开，未连生产。
- 完成操作：
  - 五路并行只读审查：WP1 核心、WP2 调度、WP3 计费、前端、缓存端到端（含官方缓存文档）。
  - 主持人回读代码核实全部 P0/P1 与缓存关键结论。
  - 结论：发版前须修 F1–F5，其中 F3、F4 为满载回归，升级即生效；另有约 16 项 P2。
  - 缓存：网关不改缓存键与会话头，损失主要来自换号和切组。
    - 主要缺口：WS 长连接不续期绑定、WS 后续 turn 满载立即关闭、判慢迁移 10 秒线、续期 TTL 写死 1 小时、E2 不覆盖 WS。
    - 整理了 7 项快赢、D1–D8 决策和 C1–C7 只读量化查询。
- 修改文件：本文件（顶部状态、进行中登记、本条日志）。仓外新增 `reviews/post_impl_review_20261031.md` 与 `reviews/post_impl_repro/`；本机记忆更正零缓存口径。
- 验证：三处复现用例均以 `-overlay` 临时运行，未进工作树：F1 输出计价组为原组；F2 新入组误熔断；F3、F4 借用探测与重复短等。本条未改代码，未重跑门禁。
- 卡点/风险：修复前不宜发版。错误率依赖运维监控写 `ops_error_logs`，升级前须确认生产已开启。量化缓存收益需用户在对话中重新提供 SSH 访问。
- 下一步：等用户确认修复范围（F1–F5 加选定的 P2、Q1–Q7）及 D1/D2 后，写修复派工包交其他 agent，修复后重跑门禁，再等发版授权。
