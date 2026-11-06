# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 0.4.16 发布（2026-11-01）及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-11-02 的 0.5.1 发布收尾）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

- **零中断加密态与路由实施（2026-11-06 08:20 UTC；集成完成、门禁全绿，未推送）**：分支 `feat/encrypted-routing-20261105`（基于 origin/main `381687250`），本条所在提交即集成头。
  - main 已发布 0.5.4（只读 MCP），本变更随下一个版本发布。
  - 范围：空白占位不算输出、Codex 输出前 ping、流内包装错误帧、M3 状态类同号修复与一次换号、输出前流内 forbidden 软冷却、`resilience.*` 计数；OpenSpec `encrypted-routing-20261105`。无迁移、无新开关、无前端改动。
  - 门禁：代码最终头 `777597cd2` 13 项全过（gofmt、三种 vet、lint 0、unit 68 包 30,955 项、`ci_gate.py` race 525 项 0 跳过、M3 定向 race、integration 60 包、docs、干净树 audit、scheduler-diff）。其后只有一个 handler 测试补强（handler 全量 unit 与 lint 复跑通过）和文档提交。
  - 两路红队对全部开发提交与集成冲突处理签字，无未关闭的 P0–P2。
  - 待用户：Codex 0.166 冒烟（tasks 3.4）的执行方式；推送、PR、合并、发版、镜像固化与部署授权。详见“进行中的工作”。

- **0.5.4 已正式发布（2026-11-06；未部署）**：PR #199 已合并，发布提交2356b8b200eb567defdd6fe3e3939d5a5ec017e2，标签v0.5.4。只读MCP含29工具，默认关闭；迁移290/291随包提供，生产启用前按runbook配置最小权限并验收目标环境。
  - 两路完整CI共20项检查全绿；Release运行42083934644成功。5平台安装包及SHA256、Linux amd64版本输出、GHCR amd64/arm64版本与commit标签均已核验，latest指向本版。生产和指定商业Host未验收；发布不等于生产启用。详细记录已归档。

- **门槛 v3 实施（2026-11-04 14:50 UTC；PR #194 已合并，main `746ec14fc`；用户授权发版 0.5.2，版本准备中，未部署）**：分支 `feat/gate-v3-20261104`，CI 20 项全绿（pull_request 运行里 macOS `shell` 因排不到机器被取消，重跑后通过）。0.5.2 包含 #191–#194，迁移新增 289。部署需单独授权。
  - 范围：门槛 v3、判慢让位、P1-B、同站暂停、`usage_logs.attempt_first_output_ms`（迁移 289）、低倍率优先分组范围、前端；另含用户追加的会话状态两条约束：
    - 带密文片段的请求不软迁移，P1-B 退回连击规则，WS 增量轮沿用标记；
    - 状态族拒绝不触发 P1-B，不计冷却与连击。
  - 这两条约束的最终形态以 `045523185` 为准（会话状态任务记的 `93762008a` 之后，又按红队第 3 轮补了 P1-B 回退与判定收窄）。
  - 门禁：最终头全部通过（lint 0、vet、unit 68 包、`ci_gate.py` -race 0 跳过、Docker 集成 60 包），文档一致性、干净树 `sync.py audit`、scheduler-diff 也通过；前端沿用 `1830baba1` 的全量门禁结果，之后前端代码无变化。
  - 两路红队三轮均签字，无 P0/P1。
  - 待用户：部署需单独授权。上线后按 runbook 逐组启用（升级继承关闭），并做 48 小时与 7 天核对。详见“进行中的工作”。
- **Codex 本地 400 配置咨询（2026-11-04）**：已核对指定调查会话与本机 0.166.0；本地可关闭子 agent 或以本地模型目录改用 V1 来减少新协作密文，不修复已有坏历史，也不覆盖中转内部状态错误。未改客户端配置与生产；登记原文已归档。
- **会话状态归属类报错（2026-11-04；方案定稿，用户已批准定稿，astra r15 已签字闭合）**：入口 `.work/tasks/20261104-encrypted-state-breaks/FINAL_v3.md`（设计）＋`HANDOFF_FINAL.md`（功能区工作包交接，交其它主 agent 分派）＋`precheck/RESULT.md`。只读，未改代码与生产配置。
  - **结论**：去协作工具 `message.encrypted` 不改名→OpenAI 硬 400（保留工具 schema 校验）；网关侧明文化须"改命名空间节点名+回程还原"，为业界唯一源头修复（CLIProxyAPI MIT、codex2api 等；`research/*`）。本仓库已有骨架 `openai_codex_tool_names.go`（`python`→`python__sub2api`，会话级持久、覆盖 OAuth 原生/透传/WS 两路）可扩展，#4978 针对"摊平"、走"改命名空间"避开。
  - **两层（用户 2026-11-04 定稿）**：Tier 0 = E 已落地 `93762008a`（随 v3 发版）+ **A（S2 真 400）暂缓不上生产**（保留备用，过渡期 S2 维持现状、用户接受代价）；**Tier 1 立项**（P1′ 改命名空间+还原成规范 `{namespace:collaboration,裸名}`+补 `encrypted_function_args:[]` 带密文否决+不改历史、冲突先判后原子、首版原生 HTTP 同接 API Key/OAuth、WS/compact 二期），**P0 观测并入 Tier 1 的 PR**（观测默认开、P1′ 默认关）。执行序：预检 2a/2b 两步都过 → 一个 PR → 两路红队 → 发版前端到端 → 灰度；立项不越过预检门禁。
  - **评审**：astra r4–r15（r10 的 B1–B6、r11 首版范围收敛原生 HTTP、r12 清理旧绝对结论，r13 核对 SHA-256 签机制/红线 `reviews/astra_r13.md`）；用户定稿后的范围变更（A暂缓/P0并入/Tier1立项，仅改范围排期、机制/红线/§8 逐字未动）r14 复核发现回退措辞冲突、已按其原句改定，**r15 核对新哈希（FINAL_v3 `ec3a4b32…`、HANDOFF `1ec8a9da…`）后闭合签字** `reviews/astra_r15.md`。签字仅确认方案与交接文档闭合，**预检/实施/红队/上线验收均未过**。
  - **用户已定、待授权**：A 暂缓、P0 并入 Tier 1、Tier 1 立项已定；**待授权**预检 2a/2b（测试号/测试服务器）、推送/PR/合并/发版/灰度。测试 Key（分组 57）用户自建、本会话只在环境变量用未落盘。
  - 详见“进行中的工作”与本日志。
  - （20:17 UTC 追记，collab-plaintext-impl）预检 2a/2b 已在实施任务中经用户授权完成；实施已随 0.5.3 发布（PR #196），实施记录已归档，遗留见“未结事项”。
- **Claude 判慢让位红队 A r4（2026-11-03 13:20 UTC）**：`737e7c7e4` 的 r3 P1 已闭合，签字已追加至本地 `implementation/redteam_astra_r2.md`；原 overlay 与正式回归均新过旧失败，目标树干净。登记已原样归档；只读复核未改代码、未提交。

### 版本与环境速览（2026-11-06：0.5.4 已发布，生产状态以重新核实为准）

- 最新产品版本 XY2API v0.5.4（2026-11-06 正式发布），Sub2API 兼容基线 0.2.15，迁移共336项（本版新增290/291）。本轮未部署，生产版本需独立重新核实。
- 生产：2026-11-03 只读核实运行 0.5.1（02:09 UTC 上线）；`ops_monitoring_enabled=true`，门槛策略第 23 版（04:20 UTC），五个 OpenAI 门槛组（A–E）均启用质量门槛与利润门，低倍率优先已开。外部状态以重新核实为准。
- 预上线主机仍运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.3）；golangci-lint v2.14.0（与 CI 同版）。
- 文档一致性：`python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业执行），规则在 `tools/docs_consistency.json`，改动时按 [`AGENTS.md`](../AGENTS.md)“变更同步”同步文档。
- 本地 `.work/` 只保留进行中的任务，已结束任务的过程稿与证据已于 2026-10-31 删除；发布资产以 GitHub Release 为准。

### 版本发布一览

已发布的各版本均为正式 Release（非 draft、非 prerelease），附件与镜像在发布时已核验；发布说明在 tag 正文。完整交接、验证与核验原文见 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（“2026-10-31 记忆重整原样归档”及更早章节）。

| 版本 | 日期 | 发布提交（相关 PR） | 主要内容 | 迁移 |
|---|---|---|---|---|
| 0.5.4 | 2026-11-06 | 2356b8b20（#199） | 29工具只读MCP、OAuth授权与连接管理、在线账号权限和独立只读Reader，默认关闭；发布前端构建堆调整，见 [部署runbook](../deploy/MCP_READONLY.md) | 290、291 |
| 0.5.3 | 2026-11-04 | `92830a0d2`（#196、#197） | 会话状态类拒绝按请求级处理与观测（`state_rejection`）；Codex 协作消息明文通道（默认关闭、按站灰度），见 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md) §4.12、§4.14 | — |
| 0.5.2 | 2026-11-04 | `f204d00bb`（#191、#192、#193、#194、#195） | OpenAI 分组质量门槛 v3、判慢让位与急性保护；Claude 判慢让位与粘性迁出；账号页近期表现第 2 版；输出后带内失败补记；会话状态保护，见 [`GROUP_QUALITY_GATE.md`](GROUP_QUALITY_GATE.md) | 289 |
| 0.5.1 | 2026-11-03 | `afaef43b0`（#184、#185、#186、#187、#188、#189） | 分组质量门槛第二版（用户可见首字、两档与档位记忆、限时观察、慢号迁出）；GPT 报错补漏（订阅共享额度、加密内容、内容拒绝口径、请求兼容 400、Grok 与 alpha search 收尾）；输出后断流补记为上游失败尝试并改归因；账号页状态列与客服气泡样式。见 [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md)、[`GROUP_QUALITY_GATE.md`](GROUP_QUALITY_GATE.md)、[`USAGE_METRICS.md`](USAGE_METRICS.md) | — |
| 0.5.0 | 2026-11-01 | `84e610681`（#181、#182、#183） | 悬浮在线客服与客户服务工单整合；账号管理页精简与信息层级优化 | 287、288 |
| 0.4.16 | 2026-11-01 | `dcd47423e`（#175、#176、#177、#178、#179） | 分组质量门槛、兜底服务组计费、满载借用与 WS 粘性；Claude 缓存卡片、用量文案和工具缓存标记，见 [`GROUP_QUALITY_GATE.md`](GROUP_QUALITY_GATE.md)、[`ANTHROPIC_CACHE_TTL.md`](ANTHROPIC_CACHE_TTL.md) | — |
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

- **Codex 协作明文通道与状态族（0.5.3）**：
  - 部署需用户授权。
  - 灰度按 `deploy/README.md`“Codex 协作消息明文通道”六步执行，先开 d0c7a8、c2a95e（gpt-6.1-sol、gpt-6-astra，API Key）。
  - b672e6 待用户决定：账号 470 的开→关续轮两次遇到 cyber_policy，472 在发版前端到端时不可调度。
  - OAuth 验证前不开。
  - 上线后按 `GATEWAY_RESILIENCE.md` §4.14 做 48 小时与 7 天核对；Codex 升级后按 client_version 复核。
  - 已知限制：模型写在正文里的私有别名不改写。
  - 实施记录已归档，本地过程稿在 `.work/tasks/20261104-encrypted-state-breaks/implementation/`。
- **Release 前端构建内存临界（0.5.3发现，0.5.4已修复）**：0.5.4发布流程已在Build frontend步骤设置NODE_OPTIONS=--max-old-space-size=4096，本轮构建通过。历史OOM记录见0.5.3操作日志。
- **OpenAI 门槛 v3、判慢让位与急性保护（2026-11-04 定稿，待实施）**：用户 11-04 批准五轮决策（任务 20261103-gpt-errors-gate-cache 第二议题，记录已归档）。方案与交接在本地 `.work/tasks/20261103-gpt-errors-gate-cache/`（不入库）：`PLAN_T2_FINAL.md` 是唯一依据，`HANDOFF.md` 写工作包 WP1–WP11、改动点（基于 `c89f8e66d`）、门禁与角色。用户 11-04 决定一次开发、一次发版，不分批。范围包括 `usage_logs` 尝试级可见首字列 `attempt_first_output_ms`、判慢 30% 加递增时长与档内后排、P1-B 输出后首败改绑、同站断流暂停、v3 评估与分档、统一迁出、低倍率优先按组、高级调度分档和前端；一个集成分支、一个 PR。发版前与开发并行做只读核对（用端到端首字近似新列复核初始值，≥4 周回测回滚线，需会话内授权），报告经用户确认后发版；上线后 48 小时用新列核对，之后 7 天验收。用户将交另一主 agent 分派子 agent 实施；推送、PR、合并、发版、部署与生产查询逐次授权。第一议题的 P0 运维建议（本地 `FINAL.md` §7）用户未单独答复，现网状态未核实。
- **公开 GPT 分组门槛标定（2026-11-03，05:15 UTC 定稿）**：线上门槛策略第 23 版（04:20 UTC）为主力组 10%/30000/15000、测试组 10%/25000/13000、专属组 50 10%/25000/13000、企业专线组 8%/20000/10000、微稳定组 10%/30000/20000，低倍率优先已开。目标是使用记录页首字绿（语义首字 <10s）且缓存命中高。定稿（含 astra 第 3 轮）：所有启用门槛的组首字熔断 30000；快档线主力、测试、企业专线、专属组 15000，微稳定组维持 20000（迁出后几次请求上游成本在该组约翻倍，最便宜的组以缓存和成本优先）；错误率 10%（企业专线 8%）。企业专线组维持不含页面常黄的高缓存号，其余组保留缓存最好的两个号。分级：A 企业专线（8%，成员更严）、B 主力与测试、C 微稳定（快档 20s）。撤回此前 20000/10000 的分级建议和“B 档移出页面常黄号”的成员建议。依据（03:09–04:53 UTC）：换号后首个请求缓存命中仅 30–54%，页面黄 15–40%（整体约 12%），约两次请求后恢复。熔断造成的换号最差（页面黄 40%，30 分钟内再换号 75%），所以熔断线保持 30s，由迁出来引导。同模型同推理档下，快档线 10/15/20s 的三个组页面黄 0/13.6/36.3%，缓存 75.7/78.7/85.6%。快档线低于组内最快号的 p90 时快档为空，迁出失效：当天最快约 11s，10s 线让企业专线组失去引导。约 55% 的换号不是门槛触发，多发生在计划内首输出截止换号之后（全局 `budget.first_output_timeout_seconds`），这是下一步可分析的杠杆。配置后 24 小时只读复核需会话内授权。
- **分组质量门槛第二版（0.5.1）**：上线 24 小时后按 OpenSpec `group-quality-gate-v2-20261102` design §7 只读复核，需要用户在会话内授权；号池只有 1–2 个号的五个组的可用账号数告警，待用户在管理端配置（组号见本地交接包）。档位记忆与观察状态只在进程内，重启后重新积累。
- **GPT 报错补漏（0.5.1）**：
  - 上线 7 天后按本地复测 SQL 只读复测，需要用户在会话内授权。
  - 管理员处理三件事：为小号池组配兜底分组与告警；联系旧版 LiteLLM 客户端的 Key 用户升级；有 gpt-6.1-sol 工具调用流量的分组配支持 Responses 的号。
  - 残留：Grok 经 Messages 入口的非换号错误，仍会走通用 OpenAI 账号副作用（基线既有）。
  - 本版保持 HTTP/2。2026-11-03 定稿：OpenAI 上游改 HTTP/1.1 作为常设配置。做法是在数据卷 `config.yaml` 设 `gateway.openai_http2.enabled: false`，再用管理端重启接口生效。切换待用户执行或授权（见进行中的工作“20261103-051-errors-scheduling-review”）。
- **门槛漏记补齐（0.5.1）**：
  - 上线后 24 小时按本地 `.work/tasks/20261102-gate-error-volume/handoff/sql/post_release_verify.sql` 只读验收 V1–V6，需要用户在会话内授权。
  - 上游错误率及其告警会因归因口径上升，平台内部错误相应下降，这不代表质量变化。
  - WebSocket 不补记；拿不到发送边界时保守少记。
  - 带内失败事件（裸 `error`）的补记缺口由 PR #191 修复，待合并发版；验收用本地修订版 `.work/tasks/20261103-051-errors-scheduling-review/scripts/post_release_verify_inband.sql`。
- **账号列表近期表现第 2 版（main 已合入，未发版）**：PR #192 于 2026-11-03 12:55 UTC 合入（`af1642909`），在 #191 之后。发版与部署需用户授权，并避开 HTTP/1.1 切换的 24 小时对比窗口（到 11-04 12:30 UTC）。上线后只读复核三项指标的显示与查询耗时，需会话内授权。遗留：英文表头说明在高度约 800 的窗口里顶部余量只有约 5px。
- **0.4.16 升级配置与量化**：本次只发布、未部署或连接生产。升级前由用户确认 `ops_monitoring_enabled` 开启，补齐真实成本、核对 OAuth 参考倍率，按目标组配置利润门/质量门槛/固定账号、成员关系、并发、优先级与可用数告警。错误率依赖运维日志；C1–C7 缓存量化及是否调整会话绑定 TTL 仍待用户自行执行与决策。
- **0.4.16 已接受边界**：质量状态进程内、多实例各自计算且重启重新积累；Grok 异步视频跨组轮询仍按轮询组计价；批量图片用量不记分组；非调度实例 OAuth 参考倍率可能需重启刷新；利润预览按已保存价格；高流量指标查询与热路径缓存等优化暂缓。完整边界见 v0.4.16 Release 与现行调度文档。
- **Claude 缓存后续**：真实供应商工具缓存标记接受度、物理命中和收益未验证；“跟随全局只延长、不缩短”、客户端声明缓存时恢复温会话预留折扣、OAuth 系统提示 TTL 说明及分组选择器资格待决定。既有 OpenAI/Grok 出站缓存字段、`additional_tools`/namespace 等声明识别、旧 Chat 形状 Responses 工具标记与 `ttl_mode`/`ttl_source` 上下文传递仍待独立处理；本版未扩展这些行为。
- **部署与上线观察**：0.4.15 已于 2026-10-31 在线更新上线（非本仓库 agent 操作），上线观察未做；Anthropic TTL 的真实供应商缓存命中率与收益未验证；0.4.9 的过渡开关与 fast_fail 待上线 7 天后复核；智商检测参数化题未做真实上游校准，旧结构待上线观察 7 天后删除，单账号“不降智”分组需运维加第 2 个账号；N5 阈值须上线后校准；0.4.12 上线约四小时的只读观察未证明首字与缓存全面改善（样本小，不作因果归因）。
- **功能遗留**：WS 余额阶梯与复位暂缓；Anthropic 暂存失败尝试的上游成本由网关承担；非 OpenAI breaker 为进程内状态，多实例各自计数；迁移的空位预检有抢槽竞态；非 `v2:` 会话键的绑定删除不做属主比较，同一会话两个并发请求同时迁出时，后一个可能删掉前一个刚写的新绑定（随后按同样档位顺序重选，不选错号）；账号不在任何 OpenAI 组时做的手动恢复，在它首次入组时仍会转观察（门槛第二版红队判定接受）；管理端保存过设置的部署 Gemini 截止仍为 90 秒；0.4.5 OpenSpec tasks §7 的 7.2/7.3/7.5/7.6；渠道监控 v1 待真实上游验证（兼容矩阵、推理模型响应时间）与审查 P2-7；无号兜底 INC-P2-1（WebSocket 快照毫秒级窗口，不阻断）。
- **工单与 2FA**：真实 OpenAI 网页登录/MFA 只有 mock；启用 2FA 须部署 Worker、固定运行时和加密恢复目录，回退前须解决不确定任务并保留密钥与恢复日志。
- **安全与依赖**：vue-i18n 安全例外 2026-11-27 到期前处理；两台主机的 SSH 密码曾出现在对话中，建议改用密钥并更换；生产 settings 中的明文代理凭据建议轮换；`.github/SECURITY.md` 仍指向上游 Sub2API 的报告渠道，应改为本仓库（该文件未分类，改动需同时登记 `policy.json`）。
- **待用户决定**：Codex 历史联网搜索导致 502（2026-10-17 诊断，原文已归档）——恢复映射或调整白名单，代码改进前先核实 0.4.0 的 `resilience_web_search_replay` 是否覆盖 apikey 中转账号；XY-image 0.0.17 先交接并经用户确认；远端保留的 3 个含未合入提交的分支（`feat/native-stream-recovery-20260930`、`fix/native-stream-concurrency-20261001`、`fix/signature-retry-preserve-tool-result-media`）是否删除；`tools/gateway-resilience/` 下 `staging/`、`fault-upstream/`、`cdn-sim/` 发布演练脚手架 0.4.6 后未再使用，是否删除。
- **测试稳定性**：CI 偶发 `TestCompatPreludeCommentHeartbeatStillAllowsFailover/enabled` 等心跳计时用例；2026-10-28 已放宽一组截止时间，其余未定位。`TestGatewayServiceRecordUsage_PeakRateAffectsTokenModeImageOutputTokens` 与 OpenAI 同名用例把峰值时段写成 00:00–23:59，而判定区间是 `[PeakStart, PeakEnd)`，在 23:59 那一分钟运行会失败（2026-10-31 本地遇到，干净 main 相同）。`-race` 下两条用例偶发数据竞争，都在测试代码里，干净 main 同样能复现（2026-11-02）：`TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant` 是测试替身的竞争，10 次约 2 次；`TestRecordCyberPolicyEvent_RuntimeSnapshotRefreshFailureKeepsStaleScope` 在构造函数启动后台 worker 之后才改 `runtimeCacheTTL`。`TestGatewayConvertCancelBeforeOutputBillsNothing` 偶发 3 秒超时（2026-11-04，记忆 PR #198 的 push CI）。原因在测试替身：上游 body 是 `io.NopCloser(pipe)`，客户端取消时关 body 不起作用，只有 `cancel()` 抢在读循环处理完首个事件之前才能通过。在 `cancel()` 前加 200ms 延迟可稳定复现。生产不受影响：真实 body 是 `cancelOnCloseBody`，`Close` 会先取消本次请求，阻塞中的读随即返回。修法：让替身的 `Close` 关闭 pipe。
- **仓库外环境收尾**：测试服务器的候选镜像、浸泡与演练目录、`xy2api_upgrade` 库按 staging-validation §11 清理；预上线主机的两份切换前备份在正式部署后删除。

## 进行中的工作

- **20261105-encrypted-routing-impl**（2026-11-05 11:00 UTC 开始，Claude 主持与集成）：按本地 `.work/tasks/20261105-encrypted-routing/FINAL.md`（astra、Opus 已签字，用户 11-05 全部批准）实施“空白占位不算输出 + 包装错误帧 + 状态类同号学习重放与一次换号 + 通用 forbidden 软冷却 + 最小计数”，一次发布。实施交接与裁决在同目录 `implementation/`（不入库）。
  - 集成分支 `feat/encrypted-routing-20261105`（Paseo 工作树 `enc-routing-integration`，基于 origin/main `381687250`）。
  - 分工（用户 11-05 指定）：后端 gpt-6-astra xhigh 两路（甲：流式判定与包装帧；乙：状态机与 forbidden）；红队 astra xhigh（协议与客户端）与 Opus xhigh（路由与韧性）两路逆向审查。
  - 授权：生产只读补数据。推送、PR、合并、发版、部署、镜像固化需在发布前再确认；测试 Key 待用户提供。
  - 13:00 UTC：
    - **分工调整**（用户追加）：PM 只调度；丙测试（预检、门禁，在独立 detached 检出中跑），丁合并集成。
    - **设计稿**：DESIGN_A/B 经两路红队快审，裁决后开工。主要裁决：
      - 同号重放留在 Forward 内；
      - §3.4 只覆盖输出前的流内帧；
      - 只有包装 400 进 M3，包装帧按“迟到的 HTTP 状态”路由；
      - M3 终止改为 Codex 可重试的形式：未提交写 502，已提交写 `server_error`。这一条偏离 FINAL 原先的“写真 4xx”。
    - **预检 1**：三个测试 Key 对应的三个上游，两个模型都接受占位形状；混合真实密文的情况未覆盖，因为拿不到有效的协作密文。
    - 裁决全文在本地 `implementation/DECISIONS.md`。
  - 11-06 08:20 UTC：
    - 测试 Key 已由用户提供（三个，对应三个上游），只经环境变量使用，未落盘。
    - 开发完成：甲的空白占位、包装帧、forbidden 衔接与心跳修复，乙的 M3 及审查修复，均由丁普通 merge 合入。
    - 实施中相对 FINAL 的裁决（全文见本地 DECISIONS）：
      - Responses 输出前心跳按客户端选择：Codex 写完整 ping 事件，其他客户端保留注释（要求首事件为 created 的 SDK 遇到 ping 会报错）；
      - M3 批量 Bad 只在上游接受派生请求后写入，`[DONE]` 本身不算证据；DeepSeek/Kimi 归一化账号不启用 M3；M3 客户端与 ops 消息用固定模板；
      - 包装 401/403 命中请求级拒绝时按请求级终止（HTTP 400 / 流内 failed，code 按拒绝语义）；
      - 改写后的派生形状兜底只对非请求级 400/422。
    - 已接受的已知差异：M3 终止不计入 turn cap；包装 401/403 上下文超限未提交时写 HTTP 400，Codex 0.166 不据此自动压缩；compact 回退的 retry ops 沿用基线观测口径。
    - 未完成：Codex 0.166 冒烟（预检 2）；发布前镜像固化；推送、PR、发版、部署。均待用户决定。



- **20261104-release0502-handoff**（2026-11-04 17:22 UTC，发版接力）：用户再次授权继续 0.5.2 发版（包含 #191–#194，新增迁移 289），不部署生产。
  - 版本 PR #195：head `2d41304954e51cf62809cbf38ac8a44cd6fa34ff`，base `746ec14fc`；除两个 macOS `shell` 检查取消外均通过，GitHub Actions 事故已恢复。
  - 下一步：只重跑取消的检查；全绿后固定 head 普通合并，annotated `v0.5.2` 指向合并提交（正文保留 Markdown），等待 Release 并核验原生包、校验和及镜像。
  - 保留主工作区和其它工作树的既有改动；此次只在版本工作树维护发版记忆。部署及生产配置不在授权范围。
  - 17:40 UTC：用户授权 Linux 优先、可用备用开发机执行发布验证。把必需 `shell` job 迁到 Ubuntu，保留 Apple 脚本语法检查及全部 Linux 部署策略测试；Apple 专属生命周期测试转为独立手动 macOS workflow。镜像仍为 Linux 双架构，原生包继续交叉编译。备用开发机首次连接超时，短重试后可用 GitHub Ubuntu runner 完成。

- **20261104-gate-v3-impl**（2026-11-04 09:35 UTC 开始，Claude 主持与集成）：按本地 `.work/tasks/20261103-gpt-errors-gate-cache/` 的 `PLAN_T2_FINAL.md` 与 `HANDOFF.md` 实施门槛 v3、判慢让位与急性保护，一次开发、一次发版。本地实施记录在同目录 `implementation/`（不入库）。
  - 集成分支 `feat/gate-v3-20261104`（Paseo 工作树 `gate-v3-integration`，基于 origin/main `c89f8e66d`）。契约提交 `f9edba129`：档位常量改名为 `GroupQualityGateTierPass`/`Retest`/`Fail`，门槛视图加 `MigrationTarget`，OpenSpec `group-quality-gate-v3-20261104`（design §5 为各路共用的接口契约）。
  - 分工（用户 11-04 指定）：后端 gpt-6-astra xhigh 三路并行（甲 WP1/6/7，乙 WP2，丙 WP3/4/5），前端 Opus high（WP8），发版前只读核对 WP10；红队 astra xhigh 与 Opus xhigh 两路。各路在集成分支的子分支开发，由主持方合入。
  - 授权：用户 11-04 授权生产只读，用于补充数据与 WP10。推送、PR、合并、发版、部署未授权。
  - 10:00 UTC：甲路 WP1（`97d6cc839`）合入集成分支（`92295a36f`，另 `dab17d847` 去掉 ent 生成带入的 go.sum 条目）。实施期裁决记在本地 `implementation/DECISIONS.md`。
  - 10:15 UTC：WP10 发版前核对完成（本地 `implementation/WP10_prerelease_report.md`）。生产策略 v38 中五个门槛组 v2 均已关闭，升级后继承关闭，需上线后按组启用；迁出估计约 475 次/日（319–475）；企业高质量组严格模式下约 92% 时间为待复测；端到端首字只有 10-30 起的数据，失败缺提示规模，缓存 1.5 个百分点与成本 3% 常落在历史背景波动内，四条回滚线待用户决定；同站告警线候选待确认。
  - 10:35 UTC：乙（`336e4ec25`）、丙（`a657509cf`、`c2b4f66b8`、`dd0a1bc74`）、前端（4 个提交 + `ffa92ecb5`）已合入集成分支，头 `1f99e721b`；集成修正 `47aa5b23b`（接通判慢状态查询、删 v2 桩）与 `3aef04ff0`（利润预览 `passing` 保持利润门口径，契约那一条是 PM 写错）。本机资源饱和，按用户 11-04 指示清理闲置工作树与日志，重门禁（lint、vet、全量 unit、`ci_gate.py`、Docker 集成）改在备用开发机串行跑。甲路 WP6/WP7 收尾中。
  - 10:40 UTC：甲路 WP6/WP7（`7227c36cb`）合入，四路全部集成，头 `c9d497844`；`deploy/README.md` 三处 v3 小节合并为“门槛 v3 升级与回退”一节。备用开发机全量门禁运行中（本地日志 `implementation/gate_integ_c9d497844.log`）；红队 A（astra，paseo `4dd92ceb`）与 B（Opus，`322451cf`）开始审查。
  - 10:45 UTC 用户确认 WP10 三项：升级继承关闭（生产五组 v2 均已关闭，不自动启用）；回滚线以上一个版本为基线、越线即回滚到上一个版本；同站告警线由 PM 定（关开关只看其他站点限流/过载上升 >1 个百分点，次数线只作复核告警）。已写入 tasks §6、runbook（`374f1edb0`）与本地 PLAN §16。
  - 11:15 UTC 红队第 1 轮：无 P0；P1 三项已修并合入——共享调度同站暂停仍保留软罚绑定（A-01）、共享调度新放置漏判慢后排（A-02）、P1-B 作用于全平台（B1，裁决为只对 OpenAI 账号）；另修比较器传递性（A-03，两步排序）、首次评估不记 tier 事件、文档与验收口径（组号改代称等，`c58dda086`）。备用机门禁首轮：lint 6 项与 `TestAPIContracts` 设置快照已修。集成头 `9d6464ae9`，干净树 `sync.py audit` 与 scheduler-diff 通过；全量门禁重跑与红队第 2 轮进行中。
  - 11:31 UTC 红队第 2 轮两路签字，无 P0/P1；P3 文案由 PM 修（`f6b9c1c5d`）。备用机门禁在 `9d6464ae9` 上：lint 0 项、vet 通过、`ci_gate.py` 全部通过（-race、0 跳过）；全量 unit 的 FAILED 是门禁脚本过滤输出为空导致的误报，脚本已改为按 `go test` 退出码判断，将在最终头上重跑。（本条目此前几行的时刻记快了 20–90 分钟，已按提交时间更正。）
  - 11:45 UTC 范围追加（用户经会话状态报错任务转交，见本地 `.work/tasks/20261104-encrypted-state-breaks/HANDOFF_gate_v3.md`）：带协作或工具输出密文片段的请求不做软迁移，由后端丁负责（paseo `b549cb76`）；会话状态族拒绝不触发 P1-B，不计输出后冷却与连击，由甲负责。两边落在同一版本。裁决记在本地 `DECISIONS.md`。
  - 13:10 UTC：会话状态约束进入集成头 `045523185`。
    - 两条约束都已合入：丁 `ec42f2c8e`、`b96c643cb`；甲 `1e60cf6a6`、`b34bc1311`、`d2cfb2dc3`；PM 文档 `47d33c75d`、`fec46bca8`。
    - 红队第 3 轮先后修复三项 P1：
      - 带密文会话首次断流即被 P1-B 改绑：带密文请求的 P1-B 退回连击规则；WS 增量轮（带 `previous_response_id`）沿用上一轮标记。
      - Unicode 转义能绕过密文识别：去掉原始字节预筛。
      - 状态族文案过宽：先看专用 code，code 为空才用窄文案。
    - 两路红队已签字，无 P0/P1。
    - `f6b9c1c5d` 的 unit（68 包）与 Docker 集成（60 包）全部通过。最终头的全量门禁正在备用机上运行。
  - 13:35 UTC：最终头 `045523185` 的全量门禁全部通过，之后追加文档提交 `ef24105f9`（勾选 tasks 5.1/5.2）。PM 巡检心跳已删除。
    - 已完成：开发、三轮红队、全量门禁、OpenSpec §0–§5。
    - 未完成：推送、开 PR（PR 需带上本记忆文件的改动）、合并、发版、部署，以及上线后 §6 的核对。
    - 卡点：等待用户授权。
    - 下一步：用户授权后推送分支并开 PR，等 CI 全绿后再等合并授权。
  - 13:50 UTC：用户授权推送并开 PR。记忆文件按主工作区的版本带入；与 origin/main 三方核对后确认，主工作区版本已包含 origin 的全部更新。已推送并开 PR #194。合并、发版、部署未授权。
  - 14:38 UTC：CI 20 项全绿（pull_request 运行里 macOS `shell` 排队超时被取消，单独重跑后通过）。用户授权后按头提交 `c96a488fb` 合并 PR #194，得到 main `746ec14fc`。未发版、未部署。本地 main 工作区有其他任务未提交的记忆改动，没有拉取。
  - 14:50 UTC：用户授权发版 0.5.2。版本准备分支 `chore/release-0.5.2`：VERSION、`UPSTREAM_BASE.json`、`docs/README.md` 的 OpenSpec 状态。合并后在合并提交上打 annotated tag `v0.5.2`。不部署。

- **20261103-claude-ttft-routing**（2026-11-03 05:10 UTC 开始，Claude 主持）：Claude（Anthropic）30 天首字与组内路由只读分析（gpt-6-astra xhigh 反方三轮签字），用户批准后实施 Claude 判慢让位与粘性迁出。本地 `.work/tasks/20261103-claude-ttft-routing/`（不入库）：`FINAL.md` 是方案，§7 记实施期变更；`implementation/` 存简报、红队报告与缓存回放。
  - 14:07 UTC 状态：**PR #193 已合并**，合并提交为 main `c89f8e66d`；CI 20 项全部通过（首轮 upstream-sync-audit 因 `policy.json` 旧字面量失败，`607402d71` 修复）。未发版、未部署，均需另行授权。
  - 关键事实：
    - Claude 选号不看延迟，顺序为门控 → 粘性 → 账号自身优先级 → 负载率 → LRU。分组成员上的优先级只影响列表顺序。
    - 负载率按 load_factor 取整计算。load_factor 设得很大时，在途少于 10 恒为 0，会形成静态偏好。
    - 页面首字从末次尝试开始计时，第一个内容 delta 就算（含空 delta 和签名 delta），已是最宽口径。端到端首输出另计。
    - OpenAI 判慢规则回放到 Claude 能分辨慢时段：被判慢号页面黄约 48%，同期同池其他号约 11%。但这些请求 93–98% 属于热会话，收益要靠迁出。
  - 实施要点（见 `GATEWAY_RESILIENCE.md` §4.5）：
    - 样本：尝试开始 → 首个语义输出，只采原生与透传路径上成功的流式粘性热尝试；按账号与出站模型计数。
    - 后排：只在同优先级段内后排。
    - 迁出：目标对齐 OpenAI（准入、同优先级、未判慢、新鲜空槽）。用户决定去掉“≥6 个新鲜样本”，原因是生产流量集中，其他号样本不足。
    - 冷却：每分组+会话 30 分钟迁出冷却。用户要求缓存不降太多；回放显示悲观情况下 Max 命中率降幅从 1.5–3.4 个百分点压到 0.5–1.1 个百分点，基线 95.2%。
    - 开关：`anthropic_latency_hold`，缺省开，关闭即失效，重开不复活。
    - 边界：负载批量选号关闭时不生效；判慢与冷却都在进程内。
  - 配置建议（管理员执行）：
    - C1：清掉 Max 两组中一个站点两个号的 load_factor 偏置，宜在上线前或同时做，否则迁出几乎都落到该站点。
    - C2：Kiro 两组各补一个其他站点的账号；补齐后自动参与让位与迁出，没有按组开关。
    - C3：运营确认 Max 的可用性兜底组。
  - 下一步：
    1. 发版与部署另行授权。
    2. 上线 7 天按 `FINAL.md` §3、§7 只读验收，需会话内授权。迁出可归因的额外缓存写入超过 Max 组同期上下文 1.5 个百分点，或冷却下仍出现 30 分钟内二次迁出，即关开关回退。

- **20261103-051-errors-scheduling-review**（2026-11-03 05:12 UTC 开始，Claude 主持，gpt-6-astra xhigh 反方）：用户授权只读连接生产，评估 0.5.1 上线后的表现，分析仍在出现的大批量报错，以及调度与门槛“不智能”的根因，规划更深入、更可靠、更高效的优化方案。只读，不改代码与生产配置。复用已开的 SSH 复用连接，不关闭他人的连接。Claude 首字路由属于上一条任务的范围，本任务不重复。本地 `.work/tasks/20261103-051-errors-scheduling-review/`（不入库）。
  - 07:01 UTC 状态：联合方案定稿（astra 三轮后签字，无阻断），入口为本地 `PLAN.md`，**待用户决定**。未改代码，未写生产。
  - 关键事实：
    - 0.5.1 的修复都生效：400、订阅共享、加密、Grok、alpha 收尾均已归零。上线后 OpenAI 非业务报错反而升高，来自同期的站点故障，不是 0.5.1 造成的。
    - 批量断流：
      - 11-02 14:00 至 11-03 约 05:40，主力站点五个号的断流（每千流分钟）同时升到正常期的 4–14 倍。
      - 故障期间，单号单条 HTTP/2 连接上有 ≥10 条并发流时，连接会被对端重置，整条连接上的流一起断（连接哈希证实）。
      - 05:45 以后，同一个号在 20–79 条并发下零重置。
    - 门槛：
      - 抖动来自全池变慢。“首字截止不计入错误率”经回放不减少熔断，已撤回。
      - 健康号之间的断流率本来就差 2–3 倍，因此不加断流维度，不改门槛代码。
    - #188 补记有约 13% 的缺口：带内失败事件没有 `response.failed` 时，原生 `finalizeStream` 和透传 `ensureResponseFailedTerminal` 合成终态，但不登记最终账号的尝试。
    - 运维记录中截止的 Reason 不改：抽查的首字截止全部是计划内截止，现行文档也约定 Reason 保持 `first_output_timeout`。
  - 方案：
    - 应急操作：某号同一秒出现 ≥5 条输出后断流时，停调度 30–60 分钟或把并发降到 ≤9。不能只降优先级，溢出也不要集中到同站的单个号；站点正常时不常设。
    - 全局 HTTP/1.1 受控试跑（`gateway.openai_http2.enabled=false`，重启生效）：
      - 低峰时段切换，避开门槛的 24 小时复核。
      - 守门线：断流/千流分钟、续轮首字 p90、输出前连接失败、耗尽与救回。全部守住就保留为待验证的配置；收益等下一次站点异常时，按批量受影响用户数比较。
      - 11-02 曾决定暂缓，需要用户重新决定。
    - 一个小 PR：补 #188 裸 error 的缺口，两处合成终态各登记一次 `stream_failed`，同步 `GATEWAY_RESILIENCE.md` 与 `policy.json`。
    - 管理员事项：两个小号池组的模型供给；两个 Gemini 小组的供给或兜底；同站断流最高的号维持优先级 2。
  - 下一步：用户决定试跑与 PR；批准后派实施 agent，astra 红队，发版经 astra。
  - 07:32 UTC：
    - 用户批准 #188 缺口 PR。改法定为在快层的 `noteResiliencePostOutputFailure` 中让带内失败事件也记失败事实，沿用发送边界去重；这样一处改动覆盖原生（OAuth 与 API Key）、透传等全部 HTTP 路径，比逐个路径登记更小。
    - 已派后端 agent（gpt-6.1-sol xhigh），在 Paseo 工作树分支 `fix/post-output-event-attempt-20261103`（基于 `764e406c1`）实施。简报在本地 `implementation/BRIEF_post_output_event.md`。
    - 传输协议一项，用户交给 Claude 与 astra 参考成熟网关后定夺：astra 第 4 轮独立论证与成熟网关调研进行中。
    - 新事实：近 24 小时 OpenAI 流量全部来自直连的 API Key 账号；故障期批量断流约占输出后断流的三分之一；连接级重置与单连接并发流数强相关（5–9 条档零次，≥10 条档显著；astra 第 5 轮指出不能作概率证明，已改为方向性证据）。
  - 08:35 UTC：
    - 传输协议定稿，本地 `DECISION_transport.md`。
      - 决定：OpenAI 上游改用 HTTP/1.1，作为常设配置。只改 `gateway.openai_http2.enabled=false`（生效方式见 09:56 UTC 条），空闲超时保持 90 秒；H1-c 的 `idle_race` 上升时，再单独降到 60 秒并补全平台守门。
      - 建议在 UTC 22:00 低峰切换；24 小时守门通过即保留，越线即回退。
      - C（按上游选协议）、D（H2 每连接流上限）、E（站点故障域）不写代码，各附启动条件。
      - 依据：成熟网关一手资料调研（本地 `research/mature_gateways.md`）——主流代理、负载均衡与 AI 网关默认用 HTTP/1.1 连上游；同栈的 new-api 已合入按渠道 HTTP/1.1 与 HTTP/2 分片。
      - astra 第 5 轮的三项阻断已修正（撤回概率论证、更正 new-api 引述、空闲超时不同改），第 6 轮复核中。
    - #188 缺口实施完成：提交 `ba99ef176`，在分支内，未推送。核心为一处条件加按 reason 区分的补项消息；测试、四份文档、OpenSpec、`policy.json` 同步，本地检查全过。Anthropic 路径已有同账号 `stream_error` 事件，会被去重跳过。
    - 红队 A（astra xhigh）与 B（Opus xhigh）审查中。
  - 09:56 UTC：
    - 传输协议经 astra 第 7 轮签字，无阻断，定稿。
    - 生效方式更正（只读核查生产）：
      - 生产 compose 不传 `GATEWAY_OPENAI_HTTP2_ENABLED`，也没有 `.env`，原先“改 `.env`”无效。
      - 改为在数据卷 `config.yaml` 追加 `gateway.openai_http2.enabled: false`，再调用管理端重启接口 `POST /api/v1/admin/system/restart`，排空 30 秒。
      - 不用 `docker restart` 或重建容器：收到 SIGTERM 时程序只等 5 秒。
      - 步骤、连接数基线与回退在本地 `DECISION_transport.md`。
    - #188 缺口 PR #191 已开，分支 `fix/post-output-event-attempt-20261103`，3 个提交：
      - `ba99ef176`：补记；
      - `e83db24b5`：按红队 B 的 P2-1，带内失败类别与直接登记共用推导，限流、降载不计软失败，快层仍按 `TRANSIENT`；
      - `5b8ba43b0`：补两处文档措辞。
    - 两路红队复核无阻断：B 的 64 组类别矩阵一致；service、handler 全包通过（无标签与 unit 各一遍）。
    - 下一步：
      - CI 通过后，合并与发版需用户另行授权。`20261103-recent-health-metrics` 的上线排在本 PR 之后。
      - HTTP/1.1 切换由用户执行或明确授权，定在 11-04 UTC 22:00。11-03 当晚切换的话，前 24 小时基线含站点故障与 0.5.0 时段。PR #191 的部署要么在 11-03 22:00 前后完成，要么推到 11-05 22:00 以后。
  - 10:27 UTC：PR #191 CI 20 项全部通过，可合并。astra 用代码核对了新的切换做法（配置读取与优先级、重启排空、连接池上限），无阻断。
  - 12:07 UTC：
    - 用户授权合并 #191，暂不发版。已合并为 `e89522a89`，没有发版，没有部署。
    - 用户授权现在就在线上做蓝绿切换，由 Opus（xhigh）执行。做法按仓库 runbook §3.3、§4.2：
      - 新起 green 容器，镜像与 blue 相同，启动时读到数据卷 `config.yaml` 中追加的 `gateway.openai_http2.enabled: false`；
      - Caddy 改为两个上游加 `lb_policy first`，reload 切到 green；
      - blue 跑完在途请求后停止，保留容器以便回退。
    - Opus 先只读预检，PM 审阅后再执行。简报在本地 `handoff/BRIEF_bluegreen_h1.md`。
    - 现在切换的话，前 24 小时基线含站点故障，验收改为同时段对比：切换前的干净时段（11-03 05:45 起）对次日同一时段。
  - 12:53 UTC：蓝绿切换完成（Opus 执行，PM 核对）。
    - 12:17:38 UTC Caddy reload，流量切到新实例。新实例与旧实例同为 0.5.1、同一镜像摘要，启动时读到 `gateway.openai_http2.enabled: false`。
    - 旧实例的在途请求 12:20 排空，12:38:55 停止，容器保留以便回退。
    - 切换后 30 分钟：
      - OpenAI 5xx 10 条、成功 1,721 条，与切换前 30 分钟（12 条、1,582 条）相当；
      - 报错类型都是中转在流里报的错，与切换前相同；连接类错误为 0，没有成批断流；新实例零重启。
    - 回退步骤写在生产部署目录的回退说明里。0.5.0 的自动回退脚本按旧拓扑断言，已经失效，需要时手工回退。
    - 下次发版要注意：现在主用的是新实例，原 compose 描述的是已停止的旧实例。
    - 本地报告 `handoff/bluegreen_h1_report.md`。
    - 待办：1 小时速查（13:19 UTC）；明天 12:30 UTC 起跑同时段 24 小时验收（`scripts/h1_trial_verify_samehours.sql`）。
- **20261104-encrypted-state-breaks**（2026-11-04 07:45 UTC 开始，Claude 主持，gpt-6-astra xhigh 反方；方案定稿、用户已批准定稿 A暂缓/P0并入Tier1/Tier1立项，astra r15 已签字闭合）：用户授权只读连接生产。第一阶段分析截图报错；09:35 UTC 起扩大到最近一周全部 400，要求检索成熟网关方案、与 astra 深入讨论、规划能明确解决问题的方案；用户最终要求按最佳方案写成功能区工作包，交其它主 agent 分派执行。只读，未改代码与生产配置。本地 `.work/tasks/20261104-encrypted-state-breaks/`（不入库），入口 `FINAL_v3.md`（设计）+`HANDOFF_FINAL.md`（工作包交接）。FINAL_v2 方案细节与更早过程登记已原样归档。
  - 一周事实（10-28 至 11-04 09:50 UTC）：OpenAI 最终 400 共 2,043；0.5.1 后状态族 837/1,004（83%）。S1 中转状态 452（442 输出后失败，Codex 重连多能恢复）。S2–S4 按文案归类共 311（**可能涉及** Codex 多 agent v2 协作片段，但具体来源与验证域尚待确认——不等于 311 全是协作密文）。生产 64 个 OpenAI 号全非透传；错误透传规则表为空。
  - 预检实测（12:30 UTC，用户授权测试 Key+临时分组 57，经网关跨 3 站）：保留标记→`gAAAA…` 密文无 `encrypted_function_args`；去 `message.encrypted` 不改名→gpt-6.1-sol 收 OpenAI 原文 400「reserved…must match the configured schema」，gpt-6-astra 被中转包 400。→ 去标记不改名不可行，须"改命名空间节点名+回程还原"（本轮所查实现里唯一源头修复，CLIProxyAPI MIT、codex2api 等；`research/*`）。详见 `precheck/RESULT.md`（测试 Key 只在环境变量用、未落盘）。
  - 最终方案两层（FINAL_v3，取代 FINAL_v2；用户 2026-11-04 定稿）：Tier 0 = E 已落地 `93762008a`（随 v3 发版）+ **A（S2 真 400）暂缓不上生产**（保留备用、过渡期 S2 维持现状）；**Tier 1 立项**（P1′ 扩展本仓库已有改名+还原骨架 `openai_codex_tool_names.go` 到 `collaboration` 命名空间，还原成规范 `{namespace:collaboration,裸名}`+补 `encrypted_function_args:[]` 带密文否决+不改历史、冲突先判后原子，首版原生 HTTP 同接 API Key/OAuth、WS/compact 二期），**P0 观测并入 Tier 1 的 PR**（默认开、P1′ 默认关）；执行序：预检 2a/2b 两步过 → 一个 PR（P0+P1′）→ 两路红队 → 发版前端到端 → 灰度。不做：按格式改写存量密文、占位/信封/改 id、首版摘要索引、S1 重放。
  - 评审：astra r4–r15（r10 的 B1–B6、r11 首版范围收敛原生 HTTP、r12 清理旧绝对结论，r13 核对 SHA-256 签机制/红线 `reviews/astra_r13.md`）；用户定稿后的范围变更（A暂缓/P0并入/Tier1立项，仅改范围排期、机制/红线/§8 逐字未动）r14 复核发现回退措辞冲突、已按其原句改定，r15 核对新哈希后闭合签字 `reviews/astra_r15.md`。签字仅确认方案与交接文档闭合，**预检/实施/红队/上线验收均未过**。
  - 用户已定：A 暂缓、P0 并入 Tier 1、Tier 1 立项。下一步：交接包交其它主 agent 分派；**待授权**预检 2a/2b（测试号/测试服务器）、推送/PR/合并/发版/灰度。E 随 v3 发版。用户侧 V1/关闭子 agent 指引无法联系用户、留文档。
- **20261103-recent-health-metrics**（2026-11-03 06:36 UTC 开始，Claude 主持，gpt-6-astra xhigh 反方）：用户授权只读连接生产，分析账号管理页“近期表现”列（错误数、首字、缓存率）的计算口径与数据来源，找出让数字偏离账号真实表现的特殊情况，提出优化方案。只读，不改代码与生产配置；复用已开的 SSH 复用连接，不关闭他人的连接。本地 `.work/tasks/20261103-recent-health-metrics/`（不入库）。
  - 08:30 UTC 状态：联合定稿（astra 三轮，签字无阻断），入口为本地 `FINAL.md`，**待用户批准**，未改代码。09:20 UTC 按用户追问（样本量、比率与次数、P90 与平均、缓存统计方式）更新为第 2 版。
  - 现行口径：
    - 错误 = 10 分钟内状态码 ≥400 的错误行数，记在最后一个账号上；
    - 首字 = 3 分钟内语义 `first_token_ms` 的平均；
    - 缓存 = 30 分钟内全部用量的读取占比。
  - 主要失真：
    - 中转站伪保活让语义首字比可见首字快 4–9s；
    - 被换号救回的失败不计，客户端原因却计入；
    - 错误用条数不用比率；
    - 3 分钟窗口样本过少；
    - 迁入、新会话拉低缓存率。
  - 定稿口径（09:20 UTC 第 2 版，astra 第 5 轮签字）：判定定义与门槛相同，覆盖全部平台；窗口改为“最近 N 次、限最长时长”。
    - 失败：最近 200 次请求、限 60 分钟的软失败率，按尝试归因；显示“x%（f/n）”，按 Wilson 下限着色。
    - 首字：最近 50 个严格续轮、限 2 小时的可见首字 P90；样本 ≥50 才着色。
    - 缓存：最近 50 个严格续轮、限 2 小时的“保持率”，即本轮缓存读取 ≥ 上一轮提示 50% 的请求占比；Token 占比只放在悬停提示。
    - 严格续轮 = 会话紧邻的前一请求在同一账号、间隔 ≤5 分钟。门槛现行判定会多出 2.98% 的样本，这部分首字 p90 为 34.9s。
    - 第 4 轮统计依据（离线回放 6.5 小时）：
      - 比次数，失败率能把流量的影响分开；
      - 判断“是否有一成以上请求 ≥10s”时，p90 零错分，平均值错分 50%；
      - Token 占比受客户端每轮新增内容影响，某号 Token 占比 67.5%，但 99.4% 的续轮命中；
      - 最近 N 次窗口让可用率翻倍，误报更少。
    - 不迁移，不改全局首字设置。
  - 上线前提：20261103-051-errors-scheduling-review 先在源头补齐输出后断流的补记缺口（0.5.1 后共 290 条，其中 70 条没有对应尝试事件）。
  - 09:40 UTC：用户批准第 2 版口径；质量门槛本轮不改；上线顺序排在输出后断流补记修复之后。
    - Claude 主持派工：后端 gpt-6.1-sol xhigh，前端 Opus 5.5 high，在 Paseo 工作树分支 `feat/recent-health-v2-20261103`（基于 `764e406c1`）实施。完成后由 gpt-6-astra 与 Opus 5.5 xhigh 双路红队审查。
    - 实施简报在本地 `handoff/README.md`，含接口契约。
    - 推送与 PR 需用户另行授权。
  - 11:00 UTC：前后端完成首轮实施。
    - 集成测试原先因本机无 Docker 被跳过，改到 测试服务器上跑，发现一处测试写错：错误行的写入时间落在左闭右开窗口的上界之外。主持人修正后全部通过。
    - 生产只读 EXPLAIN：新的用量查询扫描约 1.1 万行，耗时 66ms。
    - 双路红队均无 P0/P1。裁定新增三条规则：严格续轮要求与上一轮同模型（Anthropic 续轮中 8.9% 换了模型，这部分保持率只有 46.5%）；会话分区按 `COALESCE(origin_group_id, group_id)`；成功判据为 `actual_cost > 0 OR output_tokens > 0`。
    - 其余为文档、测试、退役登记与显示细节，修复单在本地 `handoff/FIXES_r1.md`，已分派前后端修复。
    - 后端 agent 曾违反简报改动工作树里的记忆文件，已还原。
  - 12:00 UTC：修复与复核完成，提交 `d81434a1f`（`feat/recent-health-v2-20261103`），干净树 `sync.py audit`、`scheduler-diff` 通过；用户授权后已推送并开 PR #192（暂不合并，排在补记修复之后）。
    - 修复轮在 测试服务器集成测试中又发现两处，主持人已修：CTE 漏选 `model`；占位行测试的期望写错。
    - 占位行语义：参与相邻判断但不返回。落在其他账号上时打断续轮；落在同一账号上时不打断，但它的提示 Token 为 0，下一行不进缓存样本。
    - 双路红队复核：无阻断，残留 P3 已修。
    - 剩余遗留：英文表头说明在高度约 800 的窗口里，离顶部只有约 5px 余量，彻底解决要让通用 HelpTooltip 支持向下翻转。
    - 上线排在 `fix/post-output-event-attempt-20261103` 之后；推送、PR、发版与部署都需要用户授权。
  - 12:56 UTC：用户授权合并。把 main（含 #191）合入本分支，无冲突；#191 补记事件的 kind 为 `stream_failed`，近期表现归入断流类，无需改动。CI 20 项全绿后，按头提交 `257c91af0` 合并 #192，得到 `af1642909`。**未发版、未部署**，后续见“未结事项”。

## 操作陷阱

- `sync.py scheduler-diff` 只比对原版调度差异白名单，不检查 `policy.json` 的必需兼容字面量。改动调度钩子后，若代码被重构，旧字面量会悄悄失效，要提交后在干净工作树上跑 `python3 tools/upstream-sync/sync.py audit`。2026-11-03 PR #193 首轮 CI 的 upstream-sync-audit 因此失败：Layer 2 调用改写后，`policy.json` 仍登记旧写法。
- 处理 Sub2API 同步只以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。`DEV_GUIDE.md` 的“Git 操作”曾写着直接 merge/rebase 上游 main，2026-10-17 已改为指向同步规范，这两条命令也登记为 `tools/docs_consistency.json` 的退役写法。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。
- `git tag -a -F <文件>` 默认按注释删掉以 `#` 开头的行，发布说明的 `##` 小标题会丢失。打 tag 时加 `--cleanup=verbatim`。v0.5.1 的 tag 因此丢了小标题，已在 Release 正文补回，tag 本身未改。
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

### 2026-11-01 — 20261101-accounts-ui-polish — 账号管理页精简完成，已开 PR

- 请求/目标：分组门槛详情只在悬停小标识时显示；智商检测列竖排并随模型数自适应；逐列精简账号管理页。Claude 主持，Opus 5.5 xhigh 设计与实施。
- 开始状态：main `c2a247c97`，分支 `feat/accounts-ui-polish-20261101`。
- 完成操作：分组列加 20px 状态小标识，合格组为普通标签，异常组带状态签，同平台不显示平台图标，最多 2 个标签加“+N”；智商检测竖排，超过 3 个模型收为 2 行加“+N”；降智冷却只在状态列显示；状态列的近期错误数加文字说明并按次数分级着色；容量表头加说明；固定列在可横向滚动时加分隔线；更多菜单去掉重复图标并分组。面板出现用 GSAP，开启“减少动画”时跳过，收起或卸载时 revert。
- 修改文件：frontend 账号页相关组件、i18n、已有 spec；现行文档 `GROUP_QUALITY_GATE.md`、`OPENAI_IQ_CHECK.md`、`GROUP_CATEGORIES.md`、`account-scheduling.md`、`USAGE_METRICS.md`。
- 验证：前端 typecheck、lint:check、build 通过；test:run 470 个文件、3632 项通过；`check_docs_consistency.py` ok。截图用 mock 接口，未连后端。
- 卡点/风险：无。截图未覆盖真实数据下的长分组名组合。
- 下一步：已按用户授权推送并开 PR；合并与发版另行授权。
### 2026-11-01 — 20261031-support-chat-widget — 实施、两轮审查修复与本地验证完成，开 PR

- 请求/目标：用户 2026-10-31 要求全站悬浮在线客服（拖动、靠边、默认右下角），可直接发消息或建工单，在线工单并入并改名「客户服务」；方案经用户批准，交付止于 PR。
- 开始状态：分支 `feat/support-chat-widget-20261031`，基于 `874560eb9` 开发；期间 main 合入 #175–#179 并发布 0.4.16，本分支 rebase 到 `dcd47423e`（10 个列表类冲突手工合并，权限目录 527 + 11 = 538，迁移 287 仍空闲）。
- 完成操作：契约、文档、OpenSpec、policy 由主 agent；后端 gpt-6.1-sol 三轮实现加一轮审查修复；前端 Opus 实现并经两路复核与修复。审查：gpt-6-astra 四轮只读审查；Claude 五个维度独立审查，每条由反驳式核验确认（20 项全部成立，多数降级，修 19 项）；修复后又经四个维度复核（5 项）、astra 三轮复核（4 项）和两次 Claude 反驳式复核（1 项缺陷、2 处测试空缺），均已修复。第一轮修复都带测试；之后每处修复都核对过单独去掉时测试会失败。未补测试 1 处：`SendMessage` 唯一约束冲突的回退分支在用户 advisory 锁和会话行锁下无法触达，需故障注入。主要修复：`App.vue` 聊天 watcher 每分钟重置 store、删除被禁言会话解除禁言、删除消息不清图片数据、卸载后轮询残留、图片可被共用浏览器缓存跨账号读取、chunked JSON 超限与尾随数据、purge 关停误报、各种会话替换与旧摘要竞态（最后改为接口返回 `chat_id`，客户端按会话 id 判断）、员工在用户页看到聊天页签。
- 修改文件：迁移 287 与 checksum；后端 service/repository/handler/routes/Wire/权限目录/审计/设置；前端气泡与面板、聊天组件与 store、收件箱与提醒、客户服务页、设置卡片、zh/en；`SUPPORT_TICKETS.md`、`ADMIN_PERMISSIONS.md`、`BACKUP_RELIABILITY.md`、`README.md`、OpenSpec、`docs_consistency.json`、`policy.json`、`UPSTREAM_BASE.json`、`Makefile`。
- 验证：后端 build、vet（三种标签）、Wire 再生成一致、全量 unit、仓储集成（本地 PostgreSQL，`-race`）、golangci-lint 0 问题；前端 lint、typecheck、i18n、484 文件 3769 项、build；文档一致性、`git diff --check`；干净树 `sync.py audit` 通过；本地栈接口 49 项、浏览器 34 项全部通过。CI 结果见 PR。
- 卡点/风险：见顶部「客户服务」小节（气泡与用户菜单重叠未修；既有工单 watcher 问题；生产轮询负载未实测）。
- 下一步：推送并开 PR，CI 全绿后交另一位作者合并；发版与部署另行授权。仓库外的端到端环境与审查快照工作树在 PR 开出后清理。

### 2026-11-01 — 20261031-support-chat-widget — 气泡改为「GG」标识并调整明暗配色

- 请求/目标：PR #181 开出且 CI 全绿后，用户要求在悬浮气泡中间放「GG」两个字，并优化深色、浅色模式的配色。
- 完成操作：`SupportBubble.vue` 收起时显示 GG，打开时仍是关闭图标；浅色为 `primary-600→700` 渐变（白字对比度不低于 3.7），深色为 `primary-600→800` 渐变加一圈细边和微光，使气泡与深色底分开；键盘聚焦圈保持外圈。`SupportWidget.spec.ts` 增加收起显示 GG、打开显示关闭图标的用例；`SUPPORT_TICKETS.md` 同步气泡说明。
- 验证：前端 lint、typecheck、i18n、484 文件 3770 项、build 通过；文档一致性通过；本地栈按浅色／深色、桌面／手机、收起／打开逐一截图确认。
- 卡点/风险：无。此前「推送并开 PR」已完成（#181），仍由另一位作者合并。
- 下一步：推送本次提交，等待 CI；发版与部署另行授权。

### 2026-11-01 — 20261031-support-chat-widget — 气泡「GG」下方加小字

- 请求/目标：用户要求在「GG」下面加一行「在线客服」小字。
- 完成操作：`SupportBubble.vue` 在 GG 下加 9px 小字，文案走新 i18n 键 `supportChat.bubble.label`（zh「在线客服」，en「Support」；最初的「Live chat」在手机尺寸下贴近圆边，改短）；规格补断言；`SUPPORT_TICKETS.md` 同步。实测 Chrome 中文环境不会放大小字，56px 与 52px 气泡内文字块上下居中，最远角距圆心 23px，小于半径 26px。
- 验证：前端 lint、typecheck、i18n、test、build 与文档一致性见提交；浅色／深色、桌面／手机、中文／英文逐一截图确认。
- 卡点/风险：无。
- 下一步：推送并等待 CI；合并仍由另一位作者负责。

### 2026-11-01 — 20261031-support-chat-widget — 收件箱显示客服消息已读状态

- 请求/目标：用户要求管理员／客服发出的消息在用户读过后，后台对话框里能看到状态。
- 完成操作：`SupportChatThread.vue` 在客服视角下给每条未删除的客服消息显示「✓ 已读」（主色）或「未读」（灰色），依据是已有的 `peer_read_message_id`（客服视角即用户读游标），后端不改；用户视角保持只在最后一条自己的消息下显示「已读／已发送」。新增 i18n 键 `supportChat.unread`；组件规格覆盖逐条状态与轮询后变已读；`SUPPORT_TICKETS.md`、OpenSpec spec 与 tasks 同步。
- 验证：本地栈浏览器检查（仓库外 `pw/verify-staff-receipt.js`）：客服连发两条均为未读，用户打开面板后约 3.4 秒两条都变已读，用户侧回执仍只有一个，无控制台错误；浅色与深色截图确认。前端门禁见提交。
- 卡点/风险：既有小问题，未改：客服刚发出的消息作者先显示「客服」，下一次轮询后才显示客服账号名（发送响应不带作者名）。
- 下一步：推送并等待 CI；合并仍由另一位作者负责。

### 2026-11-01 — 20261031-support-chat-widget — 客服身份只给账号 ID、用户侧去掉已读、双向撤回

- 请求/目标：用户要求用户只看到客服账号 ID、不显示邮箱和用户名；用户侧不显示自己消息是否已读；支持双向撤回；管理员可直接清空用户侧消息。
- 完成操作：
  - 后端：聊天和工单消息新增 `staff_id`（仅客服消息）；用户视角的 `peer_read_message_id` 恒为 0（`mapChatViewer`）。
  - 撤回：用户撤回 `DELETE /api/v1/support-chat/messages/:id`，2 分钟内有效，按数据库时钟判断，超时返回 409 `SUPPORT_CHAT_RECALL_EXPIRED`；撤回清空正文和图片，若没有剩下的未回复用户消息则结束等待；审计动作 `user.support_chats.recall`。消息新增 `recalled`（删除方与作者同侧），列表预览新增 `recalled`。
  - 迁移 288：放宽 `deleted_by_role`，允许 `user`。路由钉入 `xy-support-chat`。
  - 前端：用户只见「客服 #ID」，用户侧不显示已读，自己 2 分钟内的消息旁有「撤回」；客服消息旁为「撤回」（带确认），用户消息仍为删除；墓碑文案分你／用户／客服撤回和「该消息已删除」。
  - 「清空用户侧消息」沿用已有的删除单条用户消息（不限时）与「删除会话」，未新增按钮。
  - 文档、OpenSpec、规格测试同步。
- 验证：
  - 后端：unit；本地 PostgreSQL 集成测试（`-race`，新增撤回子测试）。
  - 前端：相关规格测试。
  - 本地栈全新库：接口 63 项、浏览器 35 项全部通过；`verify-staff-receipt.js`、`verify-recall.js` 全部通过（均在仓库外）。
  - 完整门禁见提交。
- 审查：Claude 四个维度（隐私、撤回正确性、前端、测试强度），每个配一名反驳式核验员，确认 4 项并全部修复：
  - P2：640px 以上的触屏设备上，撤回／删除按钮不可见。
  - P3：客户端时钟偏快时，撤回按钮提前消失；服务端拒绝后按钮仍在。
  - P3：撤回后重算等待，改为按消息 id 顺序锚定，避免并发发送时时间戳倒序把会话误移出待回复。
  - P3：文档写「客服账号删除后只显示『客服』」，与实际不符，已改为如实描述。
  - 另有 13 项被核验驳回。
  - 注意：`e2e/down.sh --wipe` 会连同集成测试库 `xy2api_sc_test` 一起删掉，之后要重新建库。
- 卡点/风险：无。用户撤回的 2 分钟时限和「清空」的理解，已在汇报中请用户确认。
- 下一步：推送并等待 CI；合并由另一位作者负责。


### 2026-11-01 — 20261101-release0500 — PR 合并与版本准备

- 请求/目标：用户授权合并全部开放 PR 并发布 0.5.0；不部署、不连接生产。
- 完成操作：PR #181（客户服务）与 #182（账号管理页精简）在固定 head 全部检查通过后普通合并，main 合并提交为 `5fe8187c7`；创建 0.5.0 版本分支，更新产品 VERSION 与 provenance。
- 修改文件：`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json`、本记忆。已发布迁移与 Sub2API 兼容基线不变。
- 验证：PR #182 的 10 项 CI 全部成功；版本提交前执行文档一致性、diff 检查与 upstream audit。
- 卡点/风险：版本 PR、annotated `v0.5.0`、Release 制品与镜像待完成；不部署、不连接生产。
- 下一步：合并版本 PR，打 `v0.5.0`，核验正式 Release 后完成记忆收尾。

### 2026-11-01 — 20261101-gate-calibration — 0.4.16 分组门槛参数标定完成（只读）

- 请求/目标：用户授权只读连接生产，用最近 10 天 OpenAI 请求标定倍率 0.13（B 组）、0.2（A 组）、0.3（C 组）三个组的利润门毛利率、错误率熔断线、续轮首字熔断线和快档线；Claude 主持，Codex astra（xhigh，反方）独立回放后三轮交叉质询。
- 开始状态：main `c2a247c97`，工作树干净。
- 完成操作：只读导出 10 天数据到本地 `.work/tasks/20261101-gate-calibration/`（不入库）；按源码实现逐分钟回放并用生产真实熔断事件校验（C 组的一次熔断，p90 15795ms 一致）。astra 第一轮回放未按上次熔断结束截断窗口，放大了严格阈值的代价，修正后双方收敛。
- 结论（双方签字，待用户决定是否配置）：B 组利润门 20%、10%/20000/10000（新启用）；A 组利润门 22%、10%/15000/10000；C 组利润门 30%、8%/15000/10000。依据：0.4.16 成本排序下新会话几乎全部落到最便宜的枢纽号（在途峰值 45，未到 80% 负载），它的续轮 p90 最大 7.98s，所以快档线取 10s；普通站点的续轮 p90 中位数为 7–9s，8s 熔断线只会在正常波动里反复熔断；三组刚降价，历史毛利须按现价重算。
- 其他发现：C 组绝大部分营收来自一个已转到 A 组的用户；A→C 兜底按 0.3 计费且很少增加容量；枢纽号是五组共用的单点。
- 修改文件：仅本记忆文件；未改代码、未改生产配置。
- 验证：回放器与生产事件逐字段比对；双方脚本互相复跑。未运行仓库测试（无代码改动）。
- 卡点/风险：回放基于人工运维下的历史分配，熔断期间的信号不是反事实；C 组无近期流量可验证。
- 下一步：用户决定后在管理端配置，按 FINAL.md 的回退条件核对。

### 2026-11-02 — 20261102-gate-recovery — 生产门槛状态复位完成

- 请求/目标：用户明确授权连接生产，将因分组质量门槛处于熔断或试用的账号恢复正常；先读取指定 agent 会话，再执行可验证的恢复操作。
- 开始状态：main `84e610681`；工作树仅有既有 `docs/PROJECT_MEMORY.md` 未提交改动；生产应用运行 0.5.0，质量门状态由应用进程内维护，数据库保留门槛策略与历史运维日志。
- 完成操作：读取会话并确认现有单账号恢复接口会进入试用；优雅重启生产应用清空进程内门槛状态；临时关闭五个已启用门槛组，等待策略刷新后按原阈值重新启用，策略版本由 7 经 8 到 9，未改账号、分组、价格或请求数据；策略通过事务 SQL 更新，未经过管理 API 审计；关闭空闲 SSH 复用连接。
- 修改文件：仅更新本记忆文件；生产仅变更门槛策略的 enabled 开关与版本号，阈值、固定账号列表保持不变。
- 验证：应用容器重启后 `running healthy`，内部 `/ready` 返回 `{"status":"ready"}`；策略 version 9 且五个门槛组均 `enabled=true`；重新启用后一个评估周期内没有新的 `group_quality_gate.eject`、`group_quality_gate.trial` 或 `group_quality_gate.recover` 事件。数据库中的历史试用事件未被删除，不能代表当前进程状态。
- 卡点/风险：应用短暂重启可能中断在途请求；质量门仍会依据后续新请求重新评估，若账号再次满足熔断条件可能再次进入门槛状态；当前未改代码，单账号“立即恢复合格”语义仍待后续产品修复。
- 下一步：无。若要永久避免门槛影响，应另行明确授权关闭策略或实施代码级恢复流程。

### 2026-11-02 — 20261102-gate-postlaunch-review — 门槛/利润门/低倍率优先上线评估完成，第二版方案获批（只读）

- 请求/目标：用户授权只读连接生产，承接 cf43b400、72bf6929 两个会话的产品职责，评估 0.4.16 的分组质量门槛、利润门、低倍率优先，并扩到 0.4.12–0.5.0 与四个平台；目标是首字、成功率、缓存、路由与补救最佳。与 Codex astra（xhigh，反方）三轮讨论。
- 开始状态：main 由 `c2a247c97` 快进到 `84e610681`（v0.5.0）；本文件已有其他任务的未提交内容，保留不动。
- 完成操作：
  - 全平台只读导出到本地 `evidence2/`（不入库），离线回放与分桶检验；
  - 生产容器 04:51 UTC 的重启与门槛策略 version 9 为 20261102-gate-recovery 所做（用户确认授权），本任务的门槛状态分析截至 04:51；
  - 写出 OpenSpec 变更与本地交接包；
  - 修复本文件“## 操作陷阱”标题与首条开头的丢失，此前被合并冲突拼接成半截标题。
- 结论：
  - **利润门与低倍率优先**：按设计工作，不改。OpenAI 毛利明显上升。
  - **门槛的问题**：
    - 门槛读语义首字（中转保活帧也算），部分站点比用户可见首字少 5–11s，门槛组 15% 续轮流量落在伪快档号上；
    - 无数据号被当作快档，改绑只有 27% 落到真快档；
    - 门槛组不迁出慢号；
    - 试用排在全部快档之后，攒不满 20 个请求，最长卡 326 分钟；手动恢复同样转试用，菜单不显示入口。
  - **失败的大头**：Anthropic 与 Grok 各一个小号池组供给中断，与代码无关。
  - **可用率口径**：OpenAI 用户×分钟可用率 99.13–99.61%，失败以输出后断流为主；输出后断流不看状态码，一律计为失败。
  - **首字相关模块**：首字截止、开场白名单、保活丢弃、停滞、预算、末位兜底、连击改绑都不读门槛，与方案不冲突。
- 修改文件：本记忆文件与归档；工作树分支中的 OpenSpec 与 `docs/README.md`（未提交）。未改代码，未改生产配置。
- 验证：三轮评审中 astra 复跑了 Claude 的全部脚本，第三轮三项均无阻断性异议；`python3 tools/check_docs_consistency.py` 通过。
- 卡点/风险：回放不模拟并发空位，迁移收益是上界；Anthropic、Grok 的供给问题不在本方案内，相关兜底与补供给未获批。
- 下一步：见进行中的工作“20261102-group-quality-gate-v2”。

### 2026-11-02 — 20261102-gate-postlaunch-review — 重启后只读复核，档位记忆修正获批（只读）

- 请求/目标：用户授权再次只读连接生产，分析最新数据，判断熔断、试用、恢复机制能否进一步优化。
- 完成操作：导出 11-02 04:00–07:29 UTC 的数据到本地 `evidence3/`（不入库），结合重启前 16 小时的门槛事件做复核。结束后已关闭 SSH 复用连接，删除临时文件。生产无任何写入。
- 结论：
  - **试用又卡住**：一个号 06:21 转入试用，68 分钟里零请求。重启前 102 次试用中，95 次在本组零请求，成败取决于账号在其他组的流量。
  - **运维仍在手工调号**：2.6 小时内改分组 22 次，净变化 4 个号；另有 1 次手动恢复。
  - **首字熔断判得准**：68 次中 67 次，被熔断号比组内最快号慢 5s 以上。
  - **伪快档仍占 13%**：其中一个号语义 p90 7.8s，用户可见 11.3s。
  - **第二版档位记忆有缺口**：见进行中的工作。修正已获批并转给实施主持 agent。
  - **不建议改**：给无数据号开快档试用通道、相对熔断线、退避上限、重启后保留熔断次数，数据都不支持。
- 修改文件：本记忆文件；本地交接包与评审稿。未改代码，未改生产配置。
- 下一步：见进行中的工作“20261102-group-quality-gate-v2”。

### 2026-11-02 — 20261102-group-quality-gate-v2 — 实施与双路红队完成，本地提交待 PR

- 请求/目标：按交接包与 OpenSpec `group-quality-gate-v2-20261102` 实施门槛第二版；Claude 主持派工，后端 gpt-6.1-sol xhigh、前端 Opus 5.5 high，gpt-6-astra xhigh 与 Opus 5.5 xhigh 逆向审查。
- 开始状态：工作树分支 `feat/group-quality-gate-v2-20261102`，HEAD `84e610681`，仅 OpenSpec 与 `docs/README.md` 未提交。
- 完成操作：后端（端到端首字、两遍定档与实时快档线、三字段档位记忆、观察 20 请求/30 分钟、门槛组迁出、Warn 事件、状态接口新字段）；前端（恢复入口、观察中/可见首字文案、面板档位与实时快档线、设置提示）；现行文档、OpenSpec、`docs_consistency.json`、`scheduler-diff.json`（调用行与 note）、`policy.json`（OpenSpec 归属）。实施中三次纠偏：观察满 20 请求要求新鲜数据；档位记忆修正；离组保留按最后熔断结束与按数据定档较晚者起算。规格补充：迁出要求本次选号开启低倍率优先。
- 修改文件：见提交 `e1b21005a`（39 个文件）；本记忆。
- 验证：见进行中条目“结果”一行，全部通过。
- 卡点/风险：Codex gpt-6.1-sol 多次 429，按约 2–3 分钟退避重发。修正批准由方案主持代为转达，用户可推翻。
- 下一步：用户已授权推送并开 PR，记忆按选项 (a) 脱敏随 PR 提交；合并与发版交 astra；上线 24 小时后按 design §7 复核（需用户在会话中授权生产只读）。

### 2026-11-01 — 20261101-accounts-status-column — 状态列信息重新分配，开 PR

- 请求/目标：状态列只留状态，其余信息放到合适的列；完成后推送并提 PR。
- 开始状态：main `84e610681`（v0.5.0）；原分支的 PR #182 已合并，本次在新分支实施。
- 完成操作：新增可隐藏的“近期表现”列（紧跟状态列，表头说明三项窗口；隐藏时不请求），显示“错误 n”、首字、缓存率，三项都无数据时显示短横线；降智冷却从状态列移到智商检测列，冷却中的模型显示“降智 复检 HH:mm”（到点后为“等待复检”），复检时间按目标自身的 `iq:` 键匹配，出站模型映射时只在检测记录里显示；状态列只留状态徽标、429/529/错误标记、模型限流与仅流式标识。测试绑定字面量随测试改名更新到 `policy.json`。
- 修改文件：`AccountsView.vue`、`AccountRecentHealth.vue`、`AccountStatusIndicator.vue`、`IQCheckCell.vue`、中英 i18n、对应 spec、`policy.json`；文档 `USAGE_METRICS.md`、`OPENAI_IQ_CHECK.md`、`account-scheduling.md`。
- 验证：前端 typecheck、lint:check、build 通过；test:run 470 个文件、3633 项通过；`check_docs_consistency.py` ok；干净树 `sync.py audit` 通过。mock 截图：1920 下状态、近期表现、智商、分组、今日统计全部可见。
- 卡点/风险：1440 宽度下新增列使分组列左移约 90px，分组小标识仍可见，标签需横向滚动；可在列显示里隐藏或调整“近期表现”。0.5.0 记忆收尾若另开 PR，与本 PR 都改本文件，后合并的一方按并集解决。
- 下一步：等待合并授权；发版另行授权。

### 2026-11-02 — 20261102-support-bubble-style — 本地验证完成，推送并开 PR

- 请求/目标：
  - 用户觉得气泡看着发糊，先要样式图、不改代码；仓外出了 6 种。
  - 用户选 B「纯色清晰版」，并要求外圈加动效（旋转光圈或呼吸）。
- 开始状态：main `84e610681`（0.5.0 版本合并后），新分支 `feat/support-bubble-style-20261102`，工作树干净。
- 完成操作：
  - 气泡外观：
    - 纯色 primary-700，深色模式同色并加细边。样式图的深色底 primary-600 与白字对比度只有 3.74:1，改用 700 后为 5.47:1。
    - 去掉渐变与文字阴影。
    - 尺寸 60px，手机 56px。
    - GG 19px 粗体（手机 18px）；小字 11px 中等字重（手机 10px）。
    - 悬停时底色加深一档。
  - 外圈光环：
    - conic 渐变弧用 radial 遮罩裁成 2.5px 的环，离按钮 2.5px，3.2 秒转一圈，只转 transform。
    - 面板打开时不渲染；开启“减少动画”（系统或菜单）时也不渲染，运行中切换立即生效。
  - FRONTEND_UX 登记了这处持续动画例外，并写明：循环动画不能只靠全局的减少动画样式。
- 修改文件：`SupportBubble.vue`、`useFloatingBubble.ts`、`SupportWidget.spec.ts`、`SUPPORT_TICKETS.md`、`FRONTEND_UX.md`、本记忆。
- 验证：
  - 前端 lint、typecheck、vitest（484 个文件、3784 项）、build 通过。
  - 文档一致性检查、upstream audit 通过。
  - 本地栈 Chrome 端到端 21 项通过（仓外脚本），覆盖：尺寸、动画、遮罩、颜色、悬停、角标、面板开合、两种减少动画、访客、无控制台错误，并截了 1x 与手机图。
- 审查：三个维度（交互与无障碍、CSS 与性能、测试与文档治理），每个配一名反驳式核验员。
  - 确认 2 项并修复：
    - 深色模式小字对比度不足；
    - 本记忆把小字字重写错。
  - 按核验意见另做了两处：
    - 补了运行中切换减少动画的测试（变异验证能抓到只读一次的写法）；
    - 去掉“唯一例外”的不实措辞。
  - 驳回 1 项：访客没有站内开关停止动画。首页闪烁光标等既有动画同样如此，可另开任务，把“减少动画”放到访客可见处。
- 卡点/风险：无。深色底色与样式图不同，已请用户确认。
- 下一步：等 CI 与另一位作者合并；发版另行授权。

### 2026-11-02 12:10 UTC — 20261102-gpt-error-review — 一周 GPT 报错复盘完成，联合方案签字（只读）

- 请求/目标：
  - 用户授权只读连接生产，分析最近一周 GPT 报错；
  - 核实韧性、补救、重试与路由是否已化解，以及用户端表现（重连或中断）；
  - 结合最新代码与 PR，提出进一步的优化方案；
  - Claude 主持，gpt-6-astra xhigh 反方。
- 开始状态：main `84e610681`（v0.5.0），工作树只有既有两份记忆的未提交改动（保留）；开放 PR #184、#185。
- 完成操作：
  - 经 SSH 复用连接只读导出一周的 `ops_error_logs`、`ops_system_logs` 与 `usage_logs` 聚合，共 11 组查询，结果在本地 `evidence/`。docker 日志只有约 2.5 小时，Caddy 日志无权限，所以一周口径只能用数据库。
  - 子 agent 读 openai/codex 0.159–0.166 源码，确认客户端对各类错误是重连还是中断；Claude 抽查 4 处成立。
  - astra 三轮：
    - 第 1 轮：独立分析；
    - 第 2 轮：交叉质询后收敛，放弃 Redis 组断路器，并纠正 Claude 两处判断（过载码已有出站改写；无号 429 已带 Retry-After）；
    - 第 3 轮：指出 SLA 只看 `is_business_limited`，P0 #3 必须越过上游上下文门控，修正后签字。
- 结论：见进行中的工作 “20261102-gpt-error-review”。定稿与证据在本地 `.work/tasks/20261102-gpt-error-review/FINAL.md`（含生产编号，不入库）。
- 修改文件：仅本记忆文件。未改代码，未改生产配置。
- 验证：`python3 tools/check_docs_consistency.py` 通过；`git diff --check` 通过。结束时已关闭 SSH 复用连接。
- 卡点/风险：
  - 线上响应头没有落库，429 的 Retry-After 只能按代码核实；
  - WS 1008 的来源（auto-review 预开连接）是推断，可在超时日志里记 `x-openai-subagent` 来确认；
  - “Encrypted function output content” 是否能靠剥离 reasoning 恢复，要等首例上线核对。
- 下一步：
  - 用户批准 P0（一个 PR）后派实施 agent；
  - 小号池组的兜底与计价由运营决定；
  - #185 上线 7 天后复测 P1、P2 的条件项（需会话内授权生产只读）。

### 2026-11-02 12:46 UTC — 20261102-gpt-error-review — P0 获批，实施交接包就绪（未改代码）

- 请求/目标：用户批准 P0；兜底分组由管理员设置；要求规划方把方案与所需文件在工作区分类放好，由新主 agent 读取后分派子 agent 实施。
- 开始状态：main `84e610681`，工作树只有两份记忆的既有未提交改动（保留）；PR #185 开放。
- 完成操作：
  - 逐项重新核对 P0 涉及的代码，写成可直接实施的交接包，放在本地 `.work/tasks/20261102-gpt-error-review/handoff/`：
    - 入口 README；
    - 代码地图；
    - WP1–WP3：签名、改动点、不做项、测试、文档与 `policy.json` 登记、验收；
    - 验证与红队清单；
    - 后续项；
    - 4 份只读 SQL：前置核查、形态核查、上线后复测、P1/P2 复测。
  - 核对中得出三处更正，记入 `FINAL.md` §7 与交接 README：
    1. 跨模式 sanitizer 已接好，不需要再接；
    2. 在途预留误记行可能多发生在 0.4.12 修复之前，所以 WP3-A 以前置核查为条件；
    3. WP2 的判据补上 502 “could not be verified … hydration failed” 这一形态，透传路径是否接入视账号模式核查结果而定。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：`python3 tools/check_docs_consistency.py`。
- 下一步：新主 agent 从 `handoff/README.md` 开始；WP3 实施前先请用户运行前置核查 SQL；推送与发版待用户授权。

### 2026-11-02 14:05 UTC — 20261102-gpt-error-review — 第 2、3 轮重估，交接包第 3 版（只读，未改代码）

- 请求/目标：第 2 轮，用户要求按 0.5.0 时间线重估，并与 #185 同期合入、发版、上线；第 3 轮，要求一步到位，重点深挖加密跨号与 400，且只能在本网关优化。
- 开始状态：main `84e610681`；#185 head `cf0a2ee0e` 仍开放，与本方案涉及的代码文件无差异（已核对）；工作树只有两份记忆的既有未提交改动（`PROJECT_MEMORY_ARCHIVE.md` 属其他 agent，未动）。
- 完成操作：
  - 经用户会话内授权，用只读包装脚本查询生产，逐版本窗口复查；证据与脚本在本地 `r2/`、`r3/`。
  - 交接包改写为第 3 版：WP1 订阅共享额度、WP2 加密跨号、WP3 内容拒绝口径、WP4 请求兼容类 400；断流、停滞阈值、中转通用 400 等写明不做的理由。
  - astra 第 4 轮的三项阻断已改：输出后余额事件不计账号失败；验收 SQL 与 `invalid_prompt` 边界分开；生产查询只经只读包装脚本。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：上线后复测 SQL 已对 0.4.13 起的窗口试跑通过；`python3 tools/check_docs_consistency.py`。
- 下一步：等 astra 第 5 轮签字；随后新主 agent 从 `handoff/README.md` 开始实施；推送与发版待用户授权，部署另行授权。

### 2026-11-02 14:50 UTC — 20261102-gate-postlaunch-review — 最近一小时 503 复核（只读）

- 请求/目标：用户授权只读连接生产，分析最近一小时集中出现的 503，判断 #185 或后续能否解决。
- 完成操作：只读查询数据库、Caddy 与 docker 日志，证据在本地 `evidence4/`（不入库）。复用了其他会话已开的 SSH 复用连接，未关闭，以免中断该会话；生产无任何写入。
- 结论：
  - **OpenAI 断流（37 条，16 个用户）**：约 86% 来自枢纽号的两次同秒批量断流。一个账号的在途流复用同一条 HTTP/2 连接，上游 CDN 重置这条连接时约 16 条流一起断。48 小时内共 3 次，都在今天。客户端那边是 200 后的带内错误，Codex 会重连。
  - **Anthropic 小组无号（23 条，2 个用户）**：组内一个号上游余额耗尽，另一个号的 key 被上游收回；这两个组都没有配置可用性兜底分组。
  - **#185 两类都不解决。** 第一类建议把 `gateway.openai_http2.enabled` 设为 `false` 并随 #185 一起上线，使一次重置只断一条流。中转单流失败仍按 20261102-gpt-error-review 的结论处理。第二类由管理员补号、配兜底分组和可用账号数告警。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。未改代码，未改生产配置。
- 下一步：由用户决定是否改这项配置；上线后核对同秒批量断流是否消失、断流率与首字没有变差（需会话内授权生产只读）。

### 2026-11-02 15:40 UTC — 20261102-gpt-error-review — astra 第 5 轮阻断项修订，交接包第 4 版（只读，未改代码）

- 请求/目标：继续第 3 轮（一步到位；用户追加深挖 502/503），按 astra 第 5 轮阻断意见修订方案后再交签字。
- 完成操作：
  - 经用户会话内授权，用只读包装脚本补查两项：`tool_search_call` arguments 失败的来源，以及 502/503 的固定窗口重算与同秒批量断开。脚本与证据在本地 `r3/`。
  - 交接包改为第 4 版，要点见进行中的工作“20261102-gpt-error-review”的“第 4 版”。
- 更正：本日 14:05 前后交给用户的 502/503 数字（453 行、每万约 68）有误。按当时证据应为 436 行、每万 65.4；现统一改用固定窗口：410 行，每万 62.3。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：复测 SQL 第 4 版已对 0.4.13 起的窗口试跑通过；`python3 tools/check_docs_consistency.py`。
- 下一步：等 astra 第 6 轮签字；随后新主 agent 从 `handoff/README.md` 开始实施；推送与发版待用户授权，部署另行授权。

### 2026-11-02 15:55 UTC — 20261102-gpt-error-review — astra 第 6 轮阻断项修订，交接包第 5 版（只读，未改代码）

- 请求/目标：astra 第 6 轮仍有 4 项阻断（兼容路径的临时不可调度旁路、兼容终态记录类别、WP3 的 type 被归一化、V3 词表不同源），修订后再交签字。
- 完成操作：交接包改为第 5 版，要点见进行中的工作“20261102-gpt-error-review”的“第 5 版”；复测 SQL 经只读包装脚本，再次对 0.4.13 起的窗口试跑通过。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：`python3 tools/check_docs_consistency.py`。
- 下一步：等 astra 第 7 轮签字；随后新主 agent 从 `handoff/README.md` 开始实施；推送与发版待用户授权，部署另行授权。

### 2026-11-02 16:29 UTC — 20261102-gpt-error-review — HTTP/1.1 与 HTTP/2 深挖，交接包第 6 版（只读，未改代码）

- 请求/目标：用户追加“深入分析 HTTP/1.1、HTTP/2 相关问题，是否还有改进空间，使线上请求达到最佳状态”。
- 开始状态：main `84e610681`，#185 head `cf0a2ee0e` 未变；工作树只有两份记忆文件的既有改动。
- 完成操作：
  - 经用户会话内授权只读核查：
    - 数据库只用只读包装脚本；
    - docker 日志只取时间、账号、本地端口和对端地址哈希；
    - Caddy 配置只看与流式和协议相关的指令。
    - 脚本与证据在本地 `r3/`（v29–v31）。
  - 结论与第 6 版要点见进行中的工作“20261102-gpt-error-review”的“第 6 版”。
  - 发现 WP5 与 20261102-gate-error-volume 方案的“不做”项冲突，已在交接包标为待用户选择，并请 astra 第 8 轮一并裁定。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：复测 SQL 的 V7 已在历史窗口试跑；`python3 tools/check_docs_consistency.py`。
- 卡点/风险：WP5 需要用户在两份方案之间明确选择。
- 下一步：等 astra 第 8 轮意见；向用户报告并请求两项决定。

### 2026-11-02 16:39 UTC — 20261102-gpt-error-review — astra 第 8 轮复核，WP5 撤下，交接包第 6 版定稿（只读，未改代码）

- 请求/目标：完成 16:29 记录的 HTTP/1.1 与 HTTP/2 深挖复核。
- 完成操作：
  - astra 第 8 轮同意本版保持 HTTP/2、不加同号重试、不调传输参数；要求把“HTTP/1.1 不会减少断流”收窄为“现有数据不足以证明有净收益”。
  - 阻断两项：WP5（与 20261102-gate-error-volume 方案冲突，净收益未证）；V7 的“接近 0、约减半”验收。
  - 已按意见撤下 WP5（原文只在本地留档），V7 改为只观察，分析与交接包措辞收窄。第 5 版 WP1–WP4 的签字继续有效。
  - 本地证据里两处残留的明文地址已脱敏。
  - 生产 SSH 复用连接已关闭。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：无。HTTP/1.1 维持用户的暂缓决定，不需要新的决定。
- 下一步：新主 agent 从 `handoff/README.md` 开始实施 WP1–WP4；推送与发版待用户授权（惯例交 astra），部署另行授权。

### 2026-11-02 17:34 UTC — 20261102-gate-error-volume — 方案与归因修正获批，实施交接包就绪（只读，未改代码）

- 请求/目标：用户同意补记方案并加入归因修正，要求与 astra 定稿后把交接包放进本任务目录，不与其他项目混放，由另一个主 agent 派子 agent 实施。
- 开始状态：main `84e610681`，工作树只有两份记忆文件的未提交改动；#185 head `cf0a2ee0e`；P0 集成分支 `fix/gpt-error-recovery-20261102` 在实施中（核对时为 `9924c6d34`）。
- 完成操作：
  - astra 第 4 轮签字同意归因修正，无阻断。它补充了补项写回请求上下文的顺序、不虚构上游状态、各读取方的口径变化与文档清单，已并入交接包。
  - 交接包 `handoff/`：入口 README（分支、发版顺序、分派、规则、完成标准）、规格、代码地图（含上游同步登记）、测试与验收。
  - 核对代码后调整一处：停滞的“已出字”判断改用现有的 `FirstOutputNoted()`，不改 `noteResilienceStall` 的签名，免去改上游同步登记。
  - 经用户在会话内授权，用只读连接在生产试跑验收 SQL，修复前基线记入 `VERIFY.md`；之后 V4 加了归因列，上线后首次运行时先确认。生产 SSH 复用连接已关闭。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：依赖 P0 先合入集成分支。P0 若要先发版，本修复进下一个补丁版本，由用户决定。
- 下一步：另一个主 agent 从 `handoff/README.md` 开始实施；推送与发版待用户授权（惯例交 astra），部署另行授权。

### 2026-11-02 17:52 UTC — 20261102-gpt-error-review — 400 深挖，astra 第 9、10 轮，交接包第 7 版新增 WP6（只读，未改代码）

- 请求/目标：用户追加：线上 400 报错仍多，要求连接生产只读分析，与 astra 共同讨论进一步方案。
- 开始状态：main `84e610681`，工作树只有两份记忆文件的未提交改动；集成分支 `fix/gpt-error-recovery-20261102` 在实施中（核对时为 `cacd94acd`）。
- 完成操作：
  - 经用户在会话内授权，只读查询生产（只读事务的包装脚本），并在主机上聚合入口访问日志。窗口是 0.5.0 上线以来约 21.7 小时，另有 7 天对照。
  - 结论：非业务 400 规模不大，最大来源是一个旧版 LiteLLM 客户端，其余零散。
  - astra 第 9 轮补出三处本网关缺陷，并更正四处口径；第 10 轮同意 WP6 的范围，以及观察与不做清单。
  - 交接包升为第 7 版：
    - 新增 `WP6_400_cleanup.md`；
    - README、代码地图、测试与红队清单、后续项随之更新；
    - 复测 SQL 加 V8，已在历史窗口试跑。
  - 实施已按第 6 版开工，所以 WP6 改为 A–D 合入后单独派工。
  - 生产 SSH 复用连接已关闭，本地证据已做明文地址检查。
- 修改文件：仅本记忆文件；本地 `.work/` 不入库。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：
  - WP6 与 A、C、D 改过的文件重叠，不能并行。
  - 姊妹任务 20261102-gate-error-volume 的分支，要在 WP6 合入后先同步集成分支。
- 下一步：
  - 实施方在 A–D 合入后派 WP6。
  - 管理员联系旧版 LiteLLM 客户端的 Key 用户升级。
  - 推送与发版待用户授权（惯例交 astra），部署另行授权。

### 2026-11-02 21:25 UTC — 20261102-gpt-error-review — WP1–WP4、WP6 实施与两轮红队完成，开 PR #187

- 请求/目标：用户授权清理本机磁盘，完成当前工作区任务后提交、推送、开 PR，合并远端全部 PR 到 main，再实施 20261102-gate-error-volume 并发布 0.5.1。不部署。
- 开始状态：集成分支 `fix/gpt-error-recovery-20261102` 只在本地（head `91999fbd0`），基于 #185 head `cf0a2ee0e`；开放 PR #184、#185、#186。
- 完成操作：
  - 本机磁盘清理：一天以上未用的 Go 构建缓存与 `/tmp` 旧任务残留，空闲空间从约 15Gi 增至约 68Gi。移除已合入的 A–D 工作树与本地分支。
  - 合并 #184。#185 合入 main 时，在本文件冲突；按并集解决，顺手把一处生产分组号改为代称。macOS `shell` 任务三次没分到 runner，重跑通过后合并。#186 合入 main 后推送，等待 CI。
  - WP6 合入集成分支，修复第 2 轮红队的 3 项阻断与采纳的建议（见进行中的工作）。
  - 集成分支合入 main 后推送，开 PR #187。
- 修改文件：见 PR #187；本记忆（合并了主工作区未提交的条目）。
- 验证：
  - `go build`；`go vet` 四种标签；
  - 改动包 `-race` unit：只有两条既有测试竞争；
  - 文档一致性、scheduler-diff、干净树 audit。
- 卡点/风险：
  - Grok Messages 非换号出口的既有旁路未修。
  - 第 3 轮红队仍在进行。
- 下一步：
  - 第 3 轮红队无阻断且 CI 通过后合并 #186、#187；
  - 门槛漏记补齐变基到 main，开 PR、合并；
  - 发布 0.5.1。

### 2026-11-02 22:00 UTC — 20261102-gate-error-volume — 输出后断流补记实施与三轮复核完成，开 PR

- 请求/目标：按本地交接包实施“输出后断流补记为失败尝试（含归因修正）”，随 0.5.1 发布；不部署。
- 开始状态：P0 集成分支 head `91999fbd0`；从它拉出 `fix/gate-post-output-attempts-20261102`，之后两次变基到 P0 新 head。
- 完成操作：
  - F 实现，内容见进行中的工作：
    - 请求级事件序号与按账号的发送边界；
    - 输出后失败事实；
    - 日志层先合并、再归因；
    - WS 隔离。
  - G 写 OpenSpec `gate-post-output-attempts-20261102` 与四份现行文档。
  - 主持人纠正去重区间，登记 `docs/README.md` 与 `policy.json`（含 OpenSpec 归属）。
  - 第 3 轮红队的阻断项与采纳的建议均已修复。
- 修改文件：`resilience/state.go`、`service/ops_upstream_context.go`、`service/resilience_post_output.go`、`service/resilience_stall.go`、`service/plugin_runtime.go`、`repository/http_upstream.go`、`handler/ops_error_logger.go` 及测试；`GATEWAY_RESILIENCE.md`、`USAGE_METRICS.md`、`GROUP_QUALITY_GATE.md`、`SHARE_SCHEDULING.md`、`docs/README.md`、OpenSpec、`policy.json`、本记忆。
- 验证：见进行中的工作的“门禁”。
- 卡点/风险：
  - WS 不补记，维持现状；
  - 拿不到发送边界时保守少记；
  - 上线后上游错误率与告警会因归因口径上升，不代表质量变差。
- 下一步：
  - #187 合并后，本 PR 改 base 为 main，同步并合并；
  - 发布 0.5.1；
  - 上线后按本地 `sql/post_release_verify.sql` 只读验收，需要用户在会话内授权。

### 2026-11-03 00:10 UTC — 20261102-release0501 — 全部 PR 合并，0.5.1 版本准备

- 请求/目标：用户授权合并远端全部 PR 并发布 0.5.1；不部署。
- 开始状态：main `ed06fa369`（#188 合并后），远端没有开放 PR。
- 完成操作：
  - 合并顺序 #184 → #185 → #186 → #187 → #188，每个都在同步 main 后、CI 全部通过时普通合并。
  - 合并 #185、#186、#187 前在本文件出现冲突，均按并集解决。
  - macOS `shell` 作业多次没有分到托管机器，重跑后通过。
  - #187、#188 首轮 CI 的 golangci-lint 报出 6 处问题，已修复；之后本机装了与 CI 同版本的 lint 预检。
  - 版本 PR：VERSION 与 `UPSTREAM_BASE.json` 改为 0.5.1；OpenSpec 状态与 tasks 勾选；本文件按归档规范整理。
- 修改文件：`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json`、`docs/README.md`、两份 OpenSpec tasks、本文件与归档。
- 验证：`python3 tools/check_docs_consistency.py`、干净树 `sync.py audit`；版本 PR 的 CI 进行中。
- 卡点/风险：无。
- 下一步：合并版本 PR，打 `v0.5.1`，核验 Release，记忆收尾。

### 2026-11-03 01:05 UTC — 20261102-release0501 — v0.5.1 正式发布与记忆收尾

- 请求/目标：完成 0.5.1 发布；不部署。
- 开始状态：版本 PR #189 合并为 main `afaef43b0`。
- 完成操作：
  - 在 `afaef43b0` 上打 annotated tag `v0.5.1`，tag 正文为发布说明。
  - Release workflow `41554355840` 全部成功（verify-version、build-frontend、release）。
  - 核验 Release：
    - 正式发布，非 draft、非 prerelease；
    - 五平台包与 `checksums.txt`，linux_amd64 包的 SHA-256 一致；
    - GHCR 的 `0.5.1`、`0.5`、`latest` 指向同一索引，含 linux/amd64、linux/arm64。
  - tag 正文的 `##` 小标题被 git 当注释删掉，已在 Release 正文补回，并把 PR 引用改为 #189（见操作陷阱）。
  - 删除本地已合并分支与仓外工作树；主工作区的未提交记忆核对已全部进入 main 后丢弃，主工作区快进到 main，备份在本地 `.work/tasks/20261102-release0501/backup/`。
- 修改文件：本文件与归档。
- 验证：Release 制品与镜像核验如上；`python3 tools/check_docs_consistency.py`。
- 卡点/风险：0.5.1 未部署。部署与上线后的只读复核（门槛第二版 24 小时、门槛漏记补齐 V1–V6、GPT 报错补漏 7 天）需要用户另行授权，见未结事项。
- 下一步：无。

### 2026-11-03 03:25 UTC — 20261103-gpt-group-gate-config — 公开 GPT 分组门槛标定完成（只读）

- 请求/目标：用户授权只读连接生产，分析每个公开 GPT 分组的错误率线、首字熔断线、快档线怎样配置能使分配最佳；用户要求尽快。
- 开始状态：main `764e406c1`，工作树干净；生产 0.5.1 于 02:09 UTC 上线；五个门槛策略与低倍率优先自 11-02 15:39 UTC 起关闭。
- 完成操作：只读导出 10-30 起的 OpenAI 用量、失败尝试、门槛事件与审计（本地，不入库）；按 0.5.1 语义写逐分钟回放并用 11-02 实际熔断校验（49 对 48 次，多数同一分钟）；扫描错误率线与首字线，评估快档占用；astra（xhigh，反方）独立回放一轮，采纳其两处修正。
- 修改文件：仅本记忆与归档。未改代码，未改生产配置。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：回放用历史流量，不重造改道后的流量；迁出数字只是信号上限。
- 下一步：见未结事项。

### 2026-11-03 03:25 UTC — 20261103-gpt-group-gate-config — 下午首字与分组分级（只读）

- 请求/目标：用户要求下午使用记录页首字尽量绿（页面为语义首字，<10s 绿），并让各分组分好级；与 astra 讨论后给最终配置。
- 完成操作：只读核实新配置已生效；今早主力站点两个号页面首字 ≥10s 占 13–52%，同站另一号保持绿，门槛已把会话迁离变慢的枢纽号；过去四个下午稳定偏绿的是三个主力同站号与特惠组最便宜的号；回放企业专线组单站点四号方案。astra 第 2 轮阻断单站点（保底留 2 个号时兜底组不会接管软退化），已改为保留一个异站后备。
- 修改文件：仅本记忆。未改代码，未写生产。生产 SSH 复用连接已关闭。
- 下一步：用户在管理端改企业专线组的阈值与成员；24 小时后只读复核（需授权）。

### 2026-11-03 05:15 UTC — 20261103-gpt-group-gate-config — 页面首字与缓存兼顾的定稿（只读）

- 请求/目标：用户要求进一步分析，给出既让使用记录页首字绿、又让缓存命中高的最佳配置。
- 完成操作：
  - 只读补导 02:00–04:53 UTC 增量，读取线上门槛策略（第 23 版）与成员。
  - 按换号原因（迁出、熔断、其他）量化页面黄与缓存损失；按源账号估算迁出得失；做同模型同推理档、不同快档线的分组对照。
  - 核对代码：有效快档线 = min(策略值, 组内最快 p90 + 4s)。
  - astra 第 3 轮超时未交，结论待补。
- 修改文件：仅本记忆。未改代码，未写生产。
- 更正 03:25 的两条记录：
  - “冷缓存请求页面不更黄”不成立：换号首个请求页面黄 15–40%，整体约 12%。
  - “B 档移出页面常黄号”撤回：其中两个号缓存命中最高。
- 卡点/风险：
  - 对照组的用户与模型构成不同；迁出收益按源账号同期其他会话估算，都是近似。
  - 生产 SSH 复用连接有另一任务正在使用，未关闭。
- 下一步：用户按定稿调整测试组、企业专线组、微稳定组（专属组 50 同理）；24 小时只读复核（需授权）。

### 2026-11-03 05:20 UTC — 20261103-gpt-group-gate-config — 合并 astra 第 3 轮，微稳定组快档线改回维持 20s（只读）

- 完成操作：astra 第 3 轮交付（`reviews/astra_r3.md`，本地不入库）。双方一致：不把快档线统一收紧到 10s；主力组 10%/30000/15000；企业专线组 8%/30000/15000。
- 分歧与处理：
  - 微稳定组：astra 主张维持 20s。它的依据是迁出后最多 3 次请求的缓存读占比从约 77% 降到约 56%，零缓存从 8% 升到 27%，每请求上游成本约翻倍（41 个关联切换，事件关联观察）。采纳，撤回 05:15 定稿中把它改为 15s 的建议。
  - 测试组：astra 写 20000 并主张不收紧。仍建议 30000/15000，因为熔断造成的换号代价最高，企业专线组已回到 30s，测试组作为它的兜底没有理由更严。
  - 企业专线组成员：astra 主张保留缓存最高的那个页面常黄号。仍建议维持现状（已不含该号），以页面为先，该组流量很小。
- 修改文件：仅本记忆。未改代码，未写生产。
- 下一步：用户按定稿调整测试组、企业专线组、专属组 50（微稳定组不动）；24 小时只读复核需授权。

### 2026-11-03 05:30 UTC — 20261103-gpt-group-gate-config — 枢纽号断流与门槛错误率口径（只读）

- 请求/目标：用户看到最便宜的枢纽号“错误 31–32”却未熔断，担心用户端重连与中断。
- 完成操作：只读查询生产。
  - 账号列表“错误”是最近 10 分钟状态码 ≥400 的错误请求数。该号这些行全部是输出后断流。
  - 04:36:56、05:02:12、05:09:13 UTC 三次同秒集体断流，分别断 11、13、18 条流，影响 7、10、12 个用户；每次几乎打断该号全部在途流。
  - 同一时刻同中转站其他号的在途流存活，说明重置发生在单个账号的那条 HTTP/2 连接上。
  - 52/54 个受影响客户端 3 分钟内恢复（中位 13 秒），其中 85% 落到别的号，下一次缓存命中约 49%。
- 门槛为何未熔断：门槛看 15 分钟软失败率，这两次断流所在窗口约 7%，低于 10%。03:45–04:32 门槛已因错误率熔断该号 6 次（11–33%）。
- 0.5.1 上线后可见断流率：缓存好的页面绿号 0.4%；枢纽号 2.1%（大部分来自集体断流）；同站另一号 5.5%（逐条断，不是集体断流）。
- 软失败把用户看不到的输出前换号和看得到的输出后断流混在一起。错误线降到 6% 会让断流率仅 0.4% 的页面绿号 3 小时内熔断 4 次，反而伤缓存和首字，所以门槛线不动。
- 建议（管理端可逆，待用户决定）：
  - 断流率 5.5% 的号优先级降为 2。
  - 枢纽号并发上限从 100 降到约 12（近 1 小时在途 p50 11、p90 19、最大 21），把超出的新会话让给另一站点、几乎同价的号。
  - 改用 HTTP/1.1 只作为后续可控试验：总断流是否下降仍未证实。
- 修改文件：仅本记忆。未改代码，未写生产。
- 下一步：用户按建议调整后，1–2 小时只读复核集体断流规模、各号断流率与首字（需授权）。

### 2026-11-03 05:40 UTC — 20261103-gpt-group-gate-config — 断流号应停调度而非只降优先级（只读）

- 用户 05:37–05:38 UTC 已把枢纽号与断流率最高的同站号降为优先级 2。
- 降优先级只影响新会话，已绑定的会话照常使用该号。05:10 后枢纽号基本不接流量，它的会话转到同站那个号上；05:30–05:38 该号断流 24 次（约 14%），15 分钟内有 37 个会话绑在它上面。
- 建议改为立即关闭该号的调度：在途流照常完成，绑定会话下一次请求换号，只付一次缓存代价。留在原号上的会话每次断流都要重连并重新生成，约四分之三的重试本来也会换号。枢纽号只剩 8 个会话，先让其自然收尾，再出现集体断流也停调度。
- 修改文件：仅本记忆。未写生产。

### 2026-11-03 06:42 UTC — 20261103-claude-ttft-routing — Claude 30 天首字与组内路由分析，联合方案定稿（只读，待批准）

- 请求/目标：用户授权只读连接生产，分析最近 30 天 Claude 请求：使用记录首字与下游首字如何更快、组内路由如何挑最快的号、能否借鉴 OpenAI 分组配置、还有哪些可靠的进一步方案；与风格相反的 astra（xhigh）讨论后给出最佳方案。
- 开始状态：main `764e406c1`，工作树只有两份记忆文件的既有改动（保留）；生产 0.5.1；复用其他会话已开的 SSH 复用连接（未关闭）。
- 完成操作：
  - 经只读包装脚本导出 31 天 Anthropic 分组的用量、运维错误、系统事件与账号（本地，不入库），用 DuckDB 离线分析；子 agent 梳理 Claude 选号与首字打点代码。
  - astra 三轮质询：
    - 第 1 轮：发现 load_factor 偏置；阻断主持方对无对照数据的外推，以及原样扩门槛。
    - 第 2 轮：要求只在同优先级段内后排，并补全热会话标记、出站模型与迁出准入。
    - 第 3 轮：修正签名错误的因果措辞、开关回退契约与收益表述后签字。
  - 主持方补两项回放：判慢让位规则回放，以及签名错误与换站的关联；据后者驳回“带签名思考的请求不得迁出”。
- 修改文件：仅本记忆。未改代码，未写生产。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：
  - 回放的对照非随机，只给方向，不承诺净提速。
  - 迁出的缓存与签名代价要按实际迁出事件验收。
  - 分析期间有人在改 Max 号的优先级（05:08 与 05:44 快照不同），配置建议以 05:44 快照为准。
- 下一步：见进行中的工作“20261103-claude-ttft-routing”。

### 2026-11-03 07:01 UTC — 20261103-051-errors-scheduling-review — 0.5.1 上线评估、批量断流根因与优化方案定稿（只读，待用户决定）

- 请求/目标：用户授权只读连接生产，评估 0.5.1 上线后的表现，分析大批量报错的根因，以及调度与门槛为什么“不智能”，规划更深入、更可靠、更高效的优化方案；与 astra（xhigh）讨论。
- 开始状态：main `764e406c1`，工作树只有两份记忆文件的既有改动（保留）；生产运行 0.5.1；复用其他会话已开的 SSH 复用连接（未关闭）。
- 完成操作：
  - 经只读包装脚本做聚合查询，辅以脱敏的容器连接哈希（本地脚本与结果不入库）；复用 20261103-gpt-group-gate-config 的回放器（只读），离线回放门槛。
  - astra 三轮质询：
    - 第 1 轮：坚持“传输不改”，要求拿出同连接证据；定位 #188 缺口的候选路径。
    - 第 2 轮：认可单连接重置的证据；用同号同小时配对，确认高并发与连接级重置强相关；提出限并发、H1 试跑与验收线。
    - 第 3 轮：同意三处调整（限并发改为应急操作、H1 收益保持条件式表述、运维 Reason 不改）并签字。
  - 主持方撤回两项：首字截止挪出错误率（回放显示不减少熔断）；凭候选摘要把小组 503 改为 4xx。
- 修改文件：仅本记忆。未改代码，未写生产。
- 验证：
  - H1 验收 SQL 在生产只读演练通过，切换点取 0.5.1 上线时刻，作为 H2 基线口径。
  - `python3 tools/check_docs_consistency.py` 通过。
- 卡点/风险：
  - 全局 H1 影响全部 OpenAI 上游，需要重启。正常期测不出收益，只能确认没有副作用，收益要等下一次站点异常验证。
  - 站点故障的触发在中转一侧，网关只能缩小影响面。
- 下一步：见进行中的工作“20261103-051-errors-scheduling-review”。

### 2026-11-03 08:30 UTC — 20261103-recent-health-metrics — 近期表现口径复盘，联合定稿待批准（只读）

- 请求/目标：用户授权只读连接生产，分析账号管理页“近期表现”列的计算口径和数据来源，以及如何不被特殊情况带偏、如实反映每个上游账号；与 gpt-6-astra xhigh 深入讨论。
- 开始状态：main `764e406c1`；工作树只有两份记忆文件的既有改动（属其他 agent，保留）。复用已开的生产 SSH 复用连接，未关闭。
- 完成操作：
  - 读现行实现（仓储、服务、处理器、组件、`USAGE_METRICS.md`）；
  - 只读查询 24 小时与 0.5.1 上线后的用量和运维错误，做 3 分钟、30 分钟窗口回放和逐账号对照；
  - astra 独立分析一轮、交叉质询两轮，撤回其跨 0.5.1 上线点的错误回放，签字无阻断。
  - 结论见进行中的工作；定稿与证据在本地 `FINAL.md`，不入库。
- 修改文件：仅本记忆文件。未改代码，未写生产。
- 验证：`python3 tools/check_docs_consistency.py`。
- 卡点/风险：上线依赖输出后断流补记缺口的源头修复。回放数字是约 4.5 小时的汇总，只说明方向。
- 下一步：用户批准后，按 `FINAL.md` 第 4 节派工（后端 sol、前端 Opus、双路红队）；门槛是否同步改严格续轮由用户决定，另做回放。

### 2026-11-03 08:57 UTC — 20261103-claude-latency-hold-redteam-astra-r1 — 只读审查完成

- 请求/目标：按 `implementation/BRIEF.md`、`FINAL.md` §3 与 `implementation/REDTEAM.md` A 节，对提交 `35cdde366` 相对 `764e406c1` 做只读正确性与契约审查；不改审查对象代码、不提交。
- 开始状态：主工作区 `main` 在 `764e406c1`，仅 `docs/PROJECT_MEMORY.md` 与归档已有未提交改动（保留）；目标 detached 树 clean。
- 完成操作：复核排序阶段、迁出准入与 owner 删除、warm/语义样本/出站模型、设置与同步登记；报告写入任务本地 `implementation/redteam_astra_r1.md`。发现关闭 load-batch 选择或缺少并发服务时普通选号绕过 Claude 判慢状态的一项 P1。
- 修改文件：仅主工作区记忆文件；本地审查报告在 `.work/tasks/20261103-claude-ttft-routing/implementation/redteam_astra_r1.md`。未改代码。
- 验证：目标树 `python3 tools/check_docs_consistency.py` → `documentation consistency: ok`；`python3 tools/upstream-sync/sync.py scheduler-diff` → `original scheduler diff matches the reviewed allowlist`；`git diff 764e406c1 35cdde366 --check` 通过。未运行 Go 测试/编译，避免占用实施中的 service 包编译资源。
- 卡点/风险：无 P0 阻断；1 项 P1 见报告。
- 下一步：实施方评估并处理报告中的 P1，再复审修复。

### 2026-11-03 09:20 UTC — 20261103-recent-health-metrics — 统计方式追问，定稿第 2 版（只读）

- 请求/目标：用户追问三项指标在样本过少或过大时是否偏离；失败率与次数、P90 与平均、缓存按时间与按请求，哪种更好、更直观。
- 完成操作：
  - 只读导出 0.5.1 后的逐请求数值数据到本地，没有个人信息；
  - Claude 与 astra 各自离线回放：抽样误差、颜色翻转、Wilson 着色、窗口可用率、缓存前缀保持；
  - astra 第 4 轮主张固定窗口和 Token 占比，第 5 轮看过证据后签字改为“最近 N 次、限时长”和保持率。
  - 结论见进行中的工作。
- 修改文件：仅本记忆文件。未改代码，未写生产。
- 验证：`python3 tools/check_docs_consistency.py`。
- 下一步：等用户批准第 2 版；门槛是否同步另行回放，由用户决定。

### 2026-11-03 09:56 UTC — 20261103-051-errors-scheduling-review — 传输协议定稿，#188 缺口 PR #191 已开

- 请求/目标：用户把传输协议交给 Claude 与 astra，参考成熟网关后定夺；批准 #188 缺口 PR；要求继续完善方案。
- 开始状态：主工作区 `main` 在 `764e406c1`，记忆文件有其他任务的未提交改动（保留）；实施分支基于 `764e406c1`。
- 完成操作：
  - 成熟网关一手资料调研（检索 agent，xhigh）；astra 第 4–7 轮论证，第 7 轮签字。
  - 定稿：OpenAI 上游改 HTTP/1.1，作为常设配置；空闲超时不动；C、D、E 不写代码，各附启动条件；生效后 24 小时守门。
  - 只读核查生产，更正生效方式（见进行中的工作 09:56 UTC 条）。
  - 实施 agent（gpt-6.1-sol xhigh）完成补记与类别对齐；两路红队各两轮，无阻断。
  - Claude 补两处文档措辞，推送分支，开 PR #191。
- 修改文件：分支内的代码、测试、四份现行文档、OpenSpec、`docs/README.md`、`policy.json`；主工作区仅本记忆文件。未写生产。
- 验证：
  - service、handler 全包，无标签与 `-tags=unit` 各一遍，均通过；
  - `go vet`、golangci-lint 0 问题；
  - 文档一致性、`sync.py audit`、`git diff --check` 均通过；
  - CI 运行中。
- 卡点/风险：合并、发版、部署与 HTTP/1.1 切换均未获授权。
- 下一步：
  - CI 通过后，等用户授权合并与发版。
  - HTTP/1.1 切换由用户执行或授权；切换后 1 小时、24 小时各跑一次本地 `scripts/h1_trial_verify.sql`。

### 2026-11-03 11:08 UTC — 20261103-claude-latency-hold-redteam-astra-r2 — 只读审查完成，签字

- 请求/目标：按 `implementation/BRIEF.md`、`FINAL.md` §7 与红队 A 范围，只读审查 `7a282cca4` 相对 `35cdde366` 的增量；不改目标代码、不提交。
- 开始状态：主工作区 `main` 在 `764e406c1`；`docs/PROJECT_MEMORY.md` 与归档文件有其他任务的既有未提交改动，保留。目标为 detached `7a282cca4`；审查中短暂看到的容量条件未提交改动在收尾时已消失，目标工作树干净。
- 完成操作：核对无锁 generation 读取/复位、30 分钟迁出冷却与容量淘汰、准入及路由集合、owner compare-delete 与预取、前端开关和文档同步；报告写入 `.work/tasks/20261103-claude-ttft-routing/implementation/redteam_astra_r2.md`。未改目标代码。
- 发现：无 P0，按用户要求签字；报告含 1 项 P1、2 项 P2、1 项 P3。
- 验证：`python3 tools/upstream-sync/sync.py scheduler-diff` → `original scheduler diff matches the reviewed allowlist`；`python3 tools/check_docs_consistency.py` → `documentation consistency: ok`；`git diff --check 35cdde366 7a282cca4` 通过。未运行 Go 测试/编译，遵守资源限制并以代码审查为主。
- 卡点/风险：无阻断；建议修复代数读方可回写旧 reset、owner changed 后继续使用旧预取 ID，并补迁出成功冷却登记/容量淘汰测试。设置页提示建议补充 30 分钟冷却。
- 下一步：实施方评估报告发现并修复后，按需复审。

### 2026-11-03 11:40 UTC — 20261103-claude-latency-hold-redteam-astra-r3 — 三项修复定向复核

- 请求/目标：只读审查 `4f21d8a61` 对 r2 的 P1 与两项 P2 修复，不扩展范围。
- 开始状态：主工作区 `main` `764e406c1`，保留项目记忆既有修改；目标 detached `23db41ff1` 干净。
- 完成操作：复核代数读路径、owner 补读后的两条粘性路径，以及真实迁出冷却登记与有界淘汰测试。结果追加至 `.work/tasks/20261103-claude-ttft-routing/implementation/redteam_astra_r2.md` 的“r3 复核”。
- 发现：两个 P2 已闭合。P1 仍存在入口第二次代数校验与 helper 快速返回之间的窗口；用 Go overlay 对真实实现做交错回归，确定性复现旧 hold 返回。无需靠 race 才能触发。
- 验证：定向四项 service unit tests `-p 1` 通过；overlay 复现测试失败，证明缺口；`git diff --check 4f21d8a61^ 4f21d8a61` 通过。目标树保持只读且干净。
- 卡点/风险：P1 尚未闭合，暂不签字；具体最小修法与复现命令见报告 r3 节。其他实现未审。
- 下一步：修复最终快照返回前的 generation/settings 校验并补该交错回归后，再复核此项。

### 2026-11-03 12:00 UTC — 20261103-recent-health-metrics — 第 2 版实施、双路红队与本地提交

- 请求/目标：用户批准第 2 版口径，本轮不改门槛，上线排在输出后断流补记修复之后。按惯例派工实施。
- 开始状态：Paseo 工作树分支 `feat/recent-health-v2-20261103`，基于 `764e406c1`。
- 完成操作：
  - 后端 gpt-6.1-sol 负责：仓储 SQL（严格续轮、同模型、按原分组分区、成功判据）；服务层统计（最近 N 次加时长上限、Wilson 区间、P50/P90、缓存保持四档、三类失败）；共用门槛的软失败查询；处理器、测试与文档；`retired` 和 `policy.json` 登记。
  - 前端 Opus 负责：近期表现组件、加载状态、截断显示、中英文案与设置提示、spec。
  - 两路红队（astra 与 Opus xhigh）首轮无 P0/P1，按修复单修复后复核，无阻断。
  - 主持人另修四处：
    - 集成测试的错误行写在窗口上界上；
    - CTE 漏选 `model`；
    - 占位行测试的期望写错；
    - 文档措辞与 SQL 注释。
  - 磁盘：清理 2 小时内未用的 Go 构建缓存，从 76G 降到 32G。
- 修改文件：
  - 后端：`account_recent_health*.go`（service、repository、handler 及测试）、`group_quality_gate.go`、`group_quality_gate_repo.go`；
  - 前端：`AccountRecentHealth.vue`、`AccountsView.vue`、`api/admin/accounts.ts`、中英 i18n（accounts、settings）、相关 spec 与注释；
  - 文档与登记：`USAGE_METRICS.md`、`GROUP_QUALITY_GATE.md`、`tools/docs_consistency.json`、`policy.json`、本记忆。
- 验证：
  - 测试服务器 集成测试：近期表现 3 个用例与门槛仓储用例通过；
  - 本机：build、四种 vet、定向 unit、改动包 golangci-lint（0 issues）；
  - 前端：lint:check、typecheck、305 个相关用例、build；
  - `check_docs_consistency.py`；
  - 生产只读 EXPLAIN：用量查询 86ms，软失败查询 1ms。
- 卡点/风险：英文表头说明的顶部余量小（见进行中的工作）；上线依赖补记修复。
- 下一步：用户授权后推送并开 PR，排在补记修复之后合并；上线后只读复核。

### 2026-11-03 12:53 UTC — 20261103-051-errors-scheduling-review — 合并 #191，生产蓝绿切换 OpenAI 上游到 HTTP/1.1

- 请求/目标：用户要求先合并 PR #191，暂不发版；随后在线上做蓝绿切换，由 Opus 执行。
- 开始状态：main 在 `764e406c1`，PR #191 的 CI 全部通过；生产单实例，0.5.1，OpenAI 上游走 HTTP/2。
- 完成操作：
  - 合并 #191，得到 `e89522a89`；未发版，未部署。
  - 只读核查确认可以蓝绿：仓库 runbook §3.3、§4.2；0.4.2 起支持两个进程重叠；数据库连接有余量。
  - Opus（xhigh）先只读预检，PM 审阅后执行：
    - 新起实例，配置文件追加 `gateway.openai_http2.enabled: false`；
    - Caddy 改为两个上游加 `lb_policy first`，一处直连旧实例的 `forward_auth` 同步改到新实例；
    - 旧实例排空后停止。
  - 切换后 30 分钟没有触发任何回退条件。
- 修改文件：仓库只改本记忆文件。生产改动：新实例的 compose 文件、Caddy 两个配置文件、数据卷 `config.yaml`，以及部署记录文件，全部先做了备份。
- 验证：
  - 新实例 `/ready` 全程 200，零重启，没有 error 级日志；
  - OpenAI 5xx 与成功数和切换前同一水平，连接类错误为 0；
  - PM 独立复核了容器状态、Caddy 上游与配置文件。
- 卡点/风险：0.5.0 的自动回退脚本已失效。
- 下一步：
  - 1 小时与 24 小时同时段验收，守门越线就按回退说明切回；
  - #191 发版部署要避开 24 小时对比窗口（到 11-04 12:30 UTC）。

### 2026-11-03 12:56 UTC — 20261103-recent-health-metrics — 同步 main 并合并 #192

- 请求/目标：用户授权把 main 同步进 PR #192，CI 通过后合并；发版与部署未授权。
- 开始状态：#191 已合入 main（`e89522a89`）；PR #192 头为 `d81434a1f`。
- 完成操作：
  - 核对 #191：补记事件的 kind 为 `stream_failed`，近期表现归入断流类，无需改动；
  - 把 main 合入分支，无冲突，并补记 PR 状态，分支头 `257c91af0`；
  - CI 20 项全绿，main 未前进，按头提交合并 #192，得到 `af1642909`。
- 修改文件：分支里只改本记忆；主工作区只改本记忆中本任务的条目、“未结事项”一条与本日志。
- 验证：文档一致性；干净树 `sync.py audit`、`scheduler-diff`；合并后 `go build`、unit vet、定向单测（近期表现、门槛、输出后断流）；CI 20 项。
- 卡点/风险：未发版、未部署。
- 下一步：发版需用户授权，并避开 HTTP/1.1 切换的 24 小时对比窗口（到 11-04 12:30 UTC）；上线后只读复核（需会话内授权）。

### 2026-11-03 13:20 UTC — 20261103-claude-latency-hold-redteam-astra-r4 — 最终定向复核，签字

- 请求/目标：只读复核 `23db41ff1..737e7c7e4` 对 r3 P1 的修复；重跑原 overlay，并证明新回归能在旧实现上行为失败；不扩展范围。
- 开始状态：主工作区 `main` `e89522a89`，保留两份记忆的其他任务既有修改；目标 detached `737e7c7e4` 干净。
- 完成操作：核对快照、活动判断、两条 partition、迁出 owner 及其调用方的最终 generation 校验；未发现漏校验入口、新增锁或新的数据竞争。结果与可复现命令已追加至本地 `.work/tasks/20261103-claude-ttft-routing/implementation/redteam_astra_r2.md` 的“r4 最终复核：签字”。
- 修改文件：仅记忆、归档与本地任务报告/overlay 证据；未改审查对象代码、未提交。
- 验证：原 r3 overlay 新实现 PASS、旧实现 FAIL；正式新回归新实现 PASS，旧实现第 102 行旧 hold 断言 FAIL。旧正式回归仅 overlay 实现文件，为旧版本缺失名称补未触达的兼容 helper，原函数不变；非编译失败。Go 均 `GOTOOLCHAIN=local`、`-p 1 -tags=unit -count=1`，未加 race；目标增量 `git diff --check` 通过。
- 卡点/风险：r3 P1 已闭合，本轮无新发现；没有扩展至其他部分或执行全包/race 测试。
- 下一步：本轮审查完成，交回主持方；推送、PR、合并与部署不在本次范围。

### 2026-11-03 13:20 UTC — 20261103-claude-ttft-routing — Claude 判慢让位与粘性迁出实施完成，两路红队签字（未推送）

- 请求/目标：用户批准按本地 `FINAL.md` 第 3 节开发：把 OpenAI 判慢让位移植到 Claude，含同优先级段内后排、粘性迁出与独立开关 `anthropic_latency_hold`。配置项 C1–C3 由管理员处理。追加要求：缓存时长与命中率不能降太多。
- 开始状态：main `764e406c1`；Paseo 工作树分支 `feat/claude-latency-hold-20261103`；主工作区只有两份记忆文件的既有改动（保留）。
- 完成操作：
  - 分工：Claude 主持；后端 gpt-6.1-sol xhigh；前端 Opus 5.5 high；红队 gpt-6-astra xhigh、Opus 5.5 xhigh。
  - 过程：后端实现后，主持自审，纠正热路径全局锁和迁出前置判断顺序；两路红队共四轮。
  - 用户决定：迁出目标对齐 OpenAI，去掉“≥6 个新鲜样本”。
  - 缓存保护：按离线回放加每会话 30 分钟迁出冷却。
  - 收口问题：变异测试补强；落后读方与关→开交错的代数竞态；属主比较失败后改按当前绑定继续；负载批量选号关闭时不生效，写入文档。
- 修改文件：`backend/internal/{resilience,service,handler,repository}`（新增 `anthropic_account_latency_hold.go` 及测试）、`docs/GATEWAY_RESILIENCE.md`、`docs/account-scheduling.md`、`tools/docs_consistency.json`、`tools/upstream-sync/{policy,scheduler-diff}.json`、前端韧性设置页、API 类型与中英文案，共 28 个文件。
- 验证：
  - 后端：定向测试（-race）、build、四种 vet、golangci-lint 0 问题、文档一致性、scheduler-diff、`git diff --check`；`ci_gate.py` 共 485 项，0 跳过。
  - 前端：lint、typecheck、vitest 485 个文件 3790 项、build。
  - 与 origin/main 试合并后，build、vet、定向测试均通过。
- 卡点/风险：
  - 回放是非随机、基于历史分配的，只说明方向和量级。
  - 让位、冷却都在进程内，多实例各自计算。
  - Kiro 补第二站点后会自动参与迁出。
- 下一步：见进行中的工作“20261103-claude-ttft-routing”13:20 条目。

### 2026-11-03 14:07 UTC — 20261103-claude-ttft-routing — PR #193 推送并合并

- 请求/目标：用户授权推送、开 PR 并合并。不发版，不部署。
- 开始状态：分支 head `737e7c7e4`。origin/main 已前进到 `af1642909`（含 #191、#192）。
- 完成操作：
  - 合并 origin/main 进分支，无冲突。本机预检通过：build、golangci-lint 0 问题、前端 typecheck、设置页测试、文档一致性、scheduler-diff。
  - 记忆单独提交，只带本任务条目。
  - 首轮 CI 的 upstream-sync-audit 失败：`policy.json` 仍登记第 1 轮的 Layer 2 调用写法。`607402d71` 改为当前的两处调用，干净树 `sync.py audit` 通过。
  - CI 20 项全部通过、PR 状态 CLEAN 后，按固定 head 普通合并，合并提交为 `c89f8e66d`。远端分支按惯例保留。
- 修改文件：分支内 `docs/PROJECT_MEMORY.md`、`tools/upstream-sync/policy.json`；主工作区本文件（“操作陷阱”新增 audit 一条）。
- 验证：CI 20 项 pass。
- 卡点/风险：无。主工作区仍停在 `e89522a89`，带多任务未提交的记忆改动，未快进，以免冲掉其他 agent 的内容。
- 下一步：发版与部署另行授权。上线前请管理员先做或同时做 C1。上线 7 天只读验收，需会话内授权。


### 2026-11-04 09:45 UTC — 20261104-encrypted-state-breaks — 会话状态归属类报错分析，联合定稿签字（只读，待用户批准）

- 请求/目标：用户截图 Codex 报 “Encrypted content is invalid for this conversation … retrying unchanged ciphertext will not repair it.”，输入“继续”仍失败。用户问三件事：为什么频繁、网关现有哪些措施、如何进一步减少用户端断流与中断。要求派 astra xhigh 深入讨论；授权只读连接生产。
- 开始状态：主工作区 main `e89522a89`，两份记忆文件带其他会话的未提交改动；origin/main `c89f8e66d`，代码在 `/tmp` 下的只读工作树里读；生产运行 0.5.1 green 实例。
- 完成操作：
  - 只读查生产（`~/bin/xyq` 与 docker 日志），读代码，派调研子代理查 Codex 0.166 源码与公开资料。
  - 与 astra（paseo `a36ef602`）进行三轮讨论：第 1、2 轮做数据与方案复核，第 3 轮两次指出因果表述过度，修订后签字。
  - 结论与方案见“进行中的工作”同名条目与本地 `FINAL.md`。
  - 另发现分组 83 自 07:30 UTC 起号池被筛空，已当场告知用户。
- 修改文件：仅本文件（登记与本日志）；本地 `.work/tasks/20261104-encrypted-state-breaks/`（不入库）。
- 验证：`python3 tools/check_docs_consistency.py`（见下）。未改代码与生产配置。
- 卡点/风险：
  - 签发与验证粒度未知；52 条新文案无法关联会话，须靠第 0 项观测补齐。
  - 方案 2 的实际救回数要上线后实测。
- 下一步：用户批准 0–2（及是否做 3）后按角色派工实施；是否联系 S-blx/S-kdy 运营方由用户决定。

### 2026-11-04 09:16 UTC — 20261103-gpt-errors-gate-cache — 第二议题定稿与实施交接（只读，未改代码）

- 请求/目标：用户第五轮答复“全部同意”，要求定最终稿，把任务目录整理干净规范，交另一主 agent 分派子 agent 实施。
- 开始状态：main `e89522a89`，落后 origin/main `c89f8e66d` 16 个提交；工作树只有多任务共用的两份记忆改动。改动点基于 origin/main `c89f8e66d`。
- 完成操作：
  - 写定稿 `PLAN_T2_FINAL.md`（唯一依据）与交接 `HANDOFF.md`（WP1–WP11、改动点、门禁、角色），按代码地图补入九处漏洞（见归档条目）。
  - astra 终审 12 条（5 条阻断），处理见 PLAN §14：
    - 迁出不再依赖低倍率优先；
    - 补两个判慢分区的回退调用点（`openai_gateway_scheduling.go` :1477、:1526）；
    - 第二次发布须等校准报告经用户确认；
    - 新列定名 `attempt_first_output_ms`，开关键定为 `same_site_hold.enabled` 与 `session.post_output_rebind`；
    - 事件定名，验收按 `ops_system_logs` 统计；
    - v3 字段放进独立子对象；固定账号只免降档；
    - 成本线按代码中的账号成本口径，分层固定权重。
    - P1-B 遇到质量路由持有的绑定时删除不生效，但会话避开在进入质量路由前已并入排除集，仍然有效；没有其他正常候选时保留末位兜底。
  - astra 复核签字，无阻断。
  - 任务目录整理：README 重写为索引，旧草稿移入 `archive/`，删除缓存与空文件。
  - 记忆：本任务进行中条目原样归档，“未结事项”新增一条指向实施交接。
- 修改文件：`docs/PROJECT_MEMORY.md`、`docs/PROJECT_MEMORY_ARCHIVE.md`（只动本任务条目）；本地任务目录（不入库）。
- 验证：`python3 tools/check_docs_consistency.py` 通过。未改代码，未运行代码测试。
- 卡点/风险：
  - 回放用语义首字，真实迁出会多于估计（端到端口径约 445–835 次/日），靠发布 1 后的校准把关。
  - 47 组首字基本不可判，50 组约 67% 可判。
- 下一步：实施方按 `HANDOFF.md` 开工。发布 1 上线 ≥48 小时后只读校准，需会话内授权；报告经用户确认后才发布第二批。推送、PR、合并、发版、部署逐次授权。

### 2026-11-04 09:24 UTC — 20261103-gpt-errors-gate-cache — 用户改为一次开发、一次发版（只读，未改代码）

- 请求/目标：用户要求这次一起开发完，不分两批。
- 开始状态：同上一条；本地任务目录的 `PLAN_T2_FINAL.md`、`HANDOFF.md` 已经 astra 终审签字。
- 完成操作：
  - PLAN §11.1 改为一次开发、一次发版，并新增 §15 记录这一决定。
  - 原先发布 1 之后的新列校准，改为发版前与开发并行的只读核对：用端到端首字中没有失败尝试的请求近似新列，复核初始值；≥4 周回测回滚线与同站告警线。报告经用户确认后发版。
  - 上线后 48 小时用新列核对；偏离时只调整各组设置，不再发版。
  - HANDOFF 依赖表改为单批：一个集成分支、一个 PR；后端按文件归属分两路（甲：WP1、WP6、WP7；乙：WP2–WP5），前端 WP8 并行。
  - 本文件“未结事项”中的对应条目同步修改。上一条日志的“下一步”以本条为准。
- 修改文件：`docs/PROJECT_MEMORY.md`（本任务条目与本条日志）；本地任务目录（不入库）。
- 验证：`python3 tools/check_docs_consistency.py` 通过。
- 卡点/风险：一次上线多项改动，7 天对比只能评估整体，各项单独效果靠事件区分；P1-B 与同站暂停可单独关开关。
- 下一步：实施方按 `HANDOFF.md` 开工；发版前核对需用户授权生产只读。

### 2026-11-04 10:12 UTC — 20261104-codex-local-400-advice — 本地配置咨询完成

- 请求/目标：判断用户指定调查会话中的 400 是否属于 Codex 新版问题，以及能否在客户端配置中解决。
- 开始状态：main `e89522a89`，origin/main `c89f8e66d`，落后 16 个提交；两份记忆有其他任务的未提交改动，保留。
- 完成操作：读取调查会话活动与本地既有报告，核对本机 Codex CLI 0.166.0、自定义 Responses provider，以及官方配置参考。结论为 MultiAgentV2 与跨签发方状态兼容有关，尚不能证明是 0.166 独有回归；本地关闭子 agent 可挡住新协作密文，保留子 agent 则可通过本地模型目录改为 V1。仅设 `multi_agent_v2=false` 不足以覆盖目录选择；已有坏历史不自动修复，中转内部状态类另行处理。
- 修改文件：仅两份记忆中本任务记录；未改代码、客户端配置或生产，未委派 agent。
- 验证：官方配置参考确认 `agents.enabled` 与 `model_catalog_json`；`codex --version`、`codex features list` 和脱敏 TOML 读取完成。CLI 接受临时 `agents.enabled=false` 配置，但 features 列表不表示会话最终工具能力；未进行真实模型请求验证。`python3 tools/check_docs_consistency.py` 与 `git diff --check` 通过。
- 卡点/风险：没有真实上游验证，本地配置只作为有边界的缓解；网关主调查仍在独立进行，本任务不替其定稿。
- 下一步：用户如选择配置调整，再按是否保留子 agent 确定方案；本轮咨询结束。

### 2026-11-04 10:53 UTC — 20261104-encrypted-state-breaks — 一周 400 全量分析与最终方案定稿（只读，待用户批准）

- 请求/目标：用户要求把范围扩大到最近一周所有 400 类报错（重点仍是加密密文），分析成因与网关层面的改善办法，检索成熟网关方案，与 astra 深入讨论，规划能明确解决问题的方案。
- 开始状态：main `e89522a89`，origin/main `c89f8e66d`；两份记忆有其他任务的未提交改动，保留。只读工作树 `/tmp/xy-main`（origin/main）。
- 完成操作：
  - 一周 400 家族分类，数字以 astra 第 5 轮的固定窗口复算为准。
  - 调研子代理两路：成熟网关源码；官方文档、Codex 目录字段与社区。
  - astra 第 4–8 轮：r4 画 400 路径全图；r5 否决按格式猜明文；r6 提出 5 项阻断，r7 剩 1 项（模型资格）；r8 签字。
  - 新查：S2–S4 失败时机（220/311 没有记录语义输出，其中 S2 74/74）；生产账号形态；错误透传规则表为空；规则在原生路径的生效顺序。
  - 核实 Codex 0.166 在 `OutputItemDone` 派发工具调用；明文投递条件为 `collaboration` 命名空间、三个指定工具、`encrypted_function_args: []`。
  - 方案与待决事项见进行中条目与本地 `FINAL_v2.md`。
- 修改文件：
  - `docs/PROJECT_MEMORY.md`：交接、进行中条目、本条日志；
  - `docs/PROJECT_MEMORY_ARCHIVE.md`：本任务第一阶段交接与过程登记，原样归档；
  - 本地任务目录（不入库）。
- 验证：`python3 tools/check_docs_consistency.py` 通过。未改代码，未运行代码测试，未调用生产模型。
- 更正：
  - 本任务上一条日志标题写的 “09:45 UTC”，实际约为 09:25 UTC。
  - 归档的过程登记中 “10:50 UTC 调研与 astra 第 4 轮”，实际约 10:10 UTC 写入。
- 卡点/风险：
  - P1 的效果取决于预检与能力范围，不承诺 S2–S4 全部消失；
  - S1 需要中转方修复；
  - 已有的真密文无法恢复。
- 下一步：等用户决定。生产 SSH 复用连接未关闭，以免影响其他会话；到期会自动关闭。

### 2026-11-04 13:20 UTC — 20261104-encrypted-state-breaks — 扩大调研 + 最终方案与交接包（只读，待 astra r11 与用户批准）

- 请求/目标：用户要求先广泛深入检索成熟网关（sub2api 官方/二开、CLIProxyAPI、各大 GPT 网关）如何处理协作消息，再做最终规划；并写成功能区工作包交其它主 agent 执行。
- 开始状态：main `e89522a89`，origin/main `c89f8e66d`；两份记忆有其他任务未提交改动，保留。
- 完成操作：
  - 预检（用户授权测试 Key + 临时分组 57，经网关）：去协作工具 `message.encrypted` 不改名→OpenAI 硬 400「reserved…must match the configured schema」。要网关侧明文化必须改命名空间。`precheck/RESULT.md`、`precheck/codex0166_*.json`。
  - 三路 xhigh 调研：业界唯一源头修复是"改命名空间+回程还原"（CLIProxyAPI MIT、codex2api 2.3k★、OmniRoute、magpie、已撤 sub2api PR #7520）；new-api/done-hub 等不做；已有密文各家都救不回。
  - 核对本仓库：已有保留工具改名+还原骨架 `openai_codex_tool_names.go`，覆盖 OAuth 原生/透传/WS 两路、含会话级持久；原生 Forward 有 schema 归一化（审计须覆盖）。
  - 成稿 `FINAL_v3.md`（分 Tier 0/Tier 1）与 `HANDOFF_FINAL.md`（工作包、文件落点、门禁、角色、顺序）。astra r4–r10 评审，r10 六阻断（删历史重标、冲突先判原子化、还原成规范身份、首版 HTTP 同接两类认证、schema 审计覆盖原生路径、收窄 §0/§8 与恢复 A 的响应头条件）已并入，r11 闭合确认中。
- 修改文件：`docs/PROJECT_MEMORY.md`（交接、进行中、本条）；本地任务目录（不入库）。未改代码、生产配置，未调用生产模型。
- 更正：本任务 10:53 UTC 那条日志把"改名+还原"描述为"#4978 禁区大改造"，经抓包与调研核实，实为保留命名空间结构的较轻操作、且本仓库有骨架可扩展；最终以 `FINAL_v3.md` 为准。
- 卡点/风险：Tier 1 效果取决于两步预检（尤其端到端补不补 `encrypted_function_args:[]`）；回程还原四出口/三形态漏一处即泄漏私有名；已有真密文不可恢复。
- 下一步：astra r11 签字后交接；用户决定 A/P0/Tier 1 立项与预检授权。生产 SSH 复用连接未关（他人可能在用），到期自动关闭。

### 2026-11-04 13:35 UTC — 20261104-gate-v3-impl — 开发、审查与门禁完成，待授权推送与开 PR

- 请求/目标：按本地交接包 `.work/tasks/20261103-gpt-errors-gate-cache/`（PLAN_T2_FINAL、HANDOFF）一次开发门槛 v3 与急性保护；中途用户经会话状态任务追加两条约束，与 v3 同版本发布。
- 开始状态：main `e89522a89`，origin/main `c89f8e66d`；两份记忆文件有其他任务的未提交改动，予以保留。
- 完成操作：
  - 契约先行，四路并行（后端甲、乙、丙 astra xhigh，前端 Opus high），由 PM 集成；后端丁处理约束 1。
  - 红队 A（astra）与 B（Opus）三轮，P1 共 7 项全部修复，两路签字。
  - 重门禁移到备用开发机运行。门禁脚本的误报已修：原先过滤后输出为空即判失败，现改为按 `go test` 退出码判断。
  - 实施期裁决记在本地 `implementation/DECISIONS.md`。
- 修改文件：集成分支 47 个提交（149 个文件）；`docs/PROJECT_MEMORY.md`（交接、进行中、本条）；本地任务目录（不入库）。
- 验证：
  - 最终头 `045523185`：lint 0、vet、unit 68 包、`ci_gate.py`（-race、0 跳过）、Docker 集成 60 包全部通过；
  - `ef24105f9`：文档一致性、干净树 `sync.py audit`、scheduler-diff 通过；
  - 前端：lint、typecheck、3801 个测试、build 在 `1830baba1` 上通过，此后前端无变化。
- 更正：本条目 11:31 UTC 之前的进行中记录，时刻记快了 20–90 分钟，已按提交时间更正；`DECISIONS.md` 中标 12:05、13:10 的行同样记错时钟，先后以行序为准。
- 卡点/风险：
  - 推送、PR、合并、发版、部署均未授权；
  - 迁移 289 的 ALTER 会短暂锁 `usage_logs`，需在低峰执行；
  - 状态族判定是局部实现，会话状态任务后续的统一识别器替换它时，须保留本轮的负例与断流对照。
- 下一步：等用户授权推送并开 PR。

### 2026-11-04 13:55 UTC — 20261104-encrypted-state-breaks — astra r11–r13 收尾清理与最终签字（只读，待用户批准）

- 请求/目标：接续扩大调研后的最终方案，完成 astra 收尾审查、取得签字，交接包交其它主 agent 分派执行。
- 开始状态：承接本任务 13:20 UTC 登记；两份记忆文件有其他任务未提交改动，予以保留。
- 完成操作：
  - r11：把 Tier 1 首版范围统一收敛到原生 HTTP（流/JSON/SSE→JSON），WS/compact 标为二期（FINAL_v3 §5.1/§8、HANDOFF Stream 2 头/WP-C2/C4）。
  - r12：清理前轮遗留的过度确定断言——S2–S4 成因改“按文案归类、可能涉及协作片段、来源与验证域待确认”；“子消息是密文/只认签发账号”改“是否真加密、验证粒度须分别核实”；删“已有真密文无法恢复”绝对判断，保留“本方案不解密、不保证恢复已有历史”；“没有比 B 更省/协议真值表强制”改“有多个改名先例、不能证明所有上游一致或无其他路径”；“三家一致/多家共同约束”改“本方案红线/本方案选用的约束”。
  - r13：astra 核对两文件 SHA-256（FINAL_v3 `e8b1b82b…`、HANDOFF_FINAL `8480e6f2…`）一致后签字（`reviews/astra_r13.md`）。
  - 同步更新交付物状态行（FINAL_v3 标题、HANDOFF 状态段、README 评审范围 r1–r13）与本文件交接/进行中条目；FINAL_v2 方案细节与更早过程登记此前已原样归档于 `PROJECT_MEMORY_ARCHIVE.md`。
- 修改文件：`docs/PROJECT_MEMORY.md`（交接、进行中、本条）；本地任务目录（不入库）。未改代码、生产配置，未调用生产模型。
- 更正：本任务 13:20 UTC 日志的“已有真密文各家都救不回”“改名+还原是唯一”等绝对措辞，按 astra r12 已在定稿中收窄为限定表述；日志为历史记录不改写，以 `FINAL_v3.md` 定稿为准。
- 卡点/风险：签字仅确认方案与交接文档闭合，预检 2a/2b、实施回归、两路红队、上线验收均未过，后续按定稿门禁执行。
- 下一步：交接包交其它主 agent 分派；用户决定 A（改生产需授权）是否现做、P0 是否先行、Tier 1 是否立项并授权预检 2a/2b。

### 2026-11-04 14:15 UTC — 20261104-encrypted-state-breaks — 用户定稿决策并入、astra r14–r15 复核签字（只读）

- 请求/目标：用户就定稿做出 4 项范围/排期决策，据此改定两份文档并取得 astra 对范围变更的最终确认。
- 开始状态：承接本任务 r13 签字版（FINAL_v3 `e8b1b82b…`、HANDOFF `8480e6f2…`）；两份记忆文件有其他任务未提交改动，予以保留。
- 用户决策（2026-11-04）：① A（S2 真 400 规则）暂缓、不上生产（保留备用，过渡期 S2 维持现状、用户接受代价）；② P0 并入 Tier 1 的 PR（观测默认开、P1′ 默认关）；③ Tier 1 立项；④ E 随 v3 发版。
- 完成操作：
  - 改定 FINAL_v3（标题/状态→定稿；§0 建议→定稿决策；§5 Tier 0 改为"已落地 E + 暂缓 A"、P0 移入新增 §5.0；§9 待决→已定）与 HANDOFF（Stream 0 标注 E 无待办/A 暂缓/P0 并入 Stream 2；门禁与顺序改为预检为第一步→一个 PR→红队）。机制 §5.1–5.4、红线、边界 §8 逐字未动。
  - astra r14 复核：发现我把"预检不过"的回退误写成"直接启用暂缓的 A"，与"A 暂缓不上生产"冲突（且启用 A 属改生产需授权）。已按 r14 原句改定：预检不过→P1′ 首版不发、Tier 0 维持现状并回报用户；启用 A 须另行取得授权。并把各处"可随时启用 A"补上"改生产需授权"。
  - astra r15 核对新哈希（FINAL_v3 `ec3a4b3269227f0b823e4859b54c1dbba8533d934b3ba7738b2b43713aeb00ca`、HANDOFF `1ec8a9da929be0e9d51b361153aabb96a4477067bc1d5c2797ca40fb0c2c133b`）后闭合签字（`reviews/astra_r15.md`）。签名溯源写入 HANDOFF 开头；因文件不能内含自身哈希，权威哈希只记在 astra_r13/r15.md，交付物不再改动以保持与 r15 签字字节一致。
- 修改文件：`docs/PROJECT_MEMORY.md`（交接、进行中、本条）；本地任务目录（不入库）。未改代码、生产配置，未调用生产模型。
- 卡点/风险：A 暂缓=Tier 1 上线前 S2 无新增止血（用户接受）；签字只确认文档闭合，预检 2a/2b、实施、红队、上线验收均未过。
- 下一步：两份文档可交下一个主 agent 分派；第一步预检 2a/2b（需测试号/测试服务器授权），推送/PR/合并/发版/灰度各需单独授权。

### 2026-11-04 20:17 UTC — 20261104-collab-plaintext-impl — 集成、四轮红队、门禁与发版前端到端完成，用户授权 PR、合并与发版 0.5.3

- 请求/目标：按 `FINAL_v3.md`/`HANDOFF_FINAL.md` 实施 Tier 1，即 P0 状态族请求级识别与观测（默认开）和 P1′ 协作消息明文通道（默认关）。用户要求：
  - 严格审查每个子 agent，逻辑闭合；
  - 精简，不加无用测试、兜底或影子模式；
  - 最终可上线。

  19:30 UTC 起，用户授权在确认具备上线条件后开 PR、合并并发版 0.5.3（macOS 没有机器时改用备用开发机）。部署与灰度不在授权内。
- 开始状态：基于 PR #194 头 `c96a488fb` 建集成分支 `feat/collab-plaintext-20261104`；19:12 UTC 合入 origin/main 0.5.2（`821eb58d5`）。
- 完成操作：
  - **分工**：后端三路（甲：改写与还原核心；乙：接线、资格、观测与文档；丙：P0），另有前端一路、预检一路。PM 逐提交审查后合入。
  - **预检**：2a/2b 通过。c2 自然选工具对照发现 developer 寻址示例与改名后的声明失配，于是追加 developer/system 文本同步改写：只改名时 13/16，改名加改写后 15/16。
  - **会话连续性**：用户决定增加，仅限 API Key，且历史协作调用的 EFA 须恰为 `[]`。PM 在 r2 曾把它放宽到明文 message；红队 B 指出这超出了用户批准的范围，r3 已撤回。
  - **两路红队四轮**（A 报 P1 12 项，B 报 P1 3 项，PM 自审 1 项），均已修复，B 于 r4 签字、A 于增量复核 b 签字：
    - A：错误体与 JSON 转义；协议容器与 schema 中的重复键；OAuth 归一化抹掉 store/previous；“归一化洗掉原始歧义”共出现四次，最终改为在原始字节上跑全部改写检查（只算 plan），按类封口；
    - B：灰度池一致性、连续性信号、Warn 口径与 runbook；
    - PM：换号耗尽后的错误体。
  - **PM 追加**：功能未开启时，入口不做原始 plan 检查。
  - **红队 B 增量复核 b 的 P2**：资格门通过、但原始阶段查出冲突的请求，恢复记为 `conflict` 并告警（丙 `da7ec7c62`）。
  - **红队 A 增量复核的 P2（R4c-A-1）**：测试的日志截获同时恢复 slog 与标准库 log 的 writer、flags；3 个指标测试改为精确差值断言（丙 `9c0245a78`，只改测试）。
  - **发版前真实端到端**：
    - `deb7c5f4b` 构建：PASS 16（含 observe 2）、TRANSIENT 4（b672e6）、FAIL 0；正文中引用私有别名 1 次，记为已知限制。
    - `da7ec7c62` 复跑（之后只改测试）：PASS 16（含 observe 2）、TRANSIENT 4（b672e6 上游 502/503）、FAIL 0；c2 子 agent 两个模型闭环；正文引用 0 次。
- 修改文件：
  - 后端：service、resilience、handler 相关文件；
  - 前端：网关韧性设置卡片、账号编辑与批量编辑；
  - 文档与登记：`docs/GATEWAY_RESILIENCE.md`、`deploy/README.md`、OpenSpec `codex-collaboration-plaintext-20261104`、`docs/README.md`、`tools/upstream-sync/policy.json`、`tools/docs_consistency.json`、本文件。
- 验证（最终代码头 `9c0245a78`）：
  - 本机（`9c0245a78`）：vet、unit 67 个包、`ci_gate.py` -race 0 跳过、lint 0；
  - 备用开发机（`da7ec7c62`，之后只改 unit 测试）：Linux unit 68 个包、Docker integration 60 个包；
  - 前端（`ff4fc83a1`，与最终头的前端树相同）：lint、typecheck、486 个文件 3806 个测试、build；
  - docs consistency、干净树 sync audit 通过。
- 卡点/风险：见“当前交接状态”中对应的小节。
- 下一步：推送、开 PR → CI 全绿后合并 → `chore(release): prepare 0.5.3` → tag `v0.5.3` → 核验 Release。部署与灰度另行授权。

### 2026-11-04 20:52 UTC — 20261104-collab-plaintext-impl — PR #196 合并，准备 0.5.3

- 请求/目标：按用户 19:30 UTC 授权，功能 PR 合并后发版 0.5.3。
- 开始状态：main 含 PR #196；本 PR 为 `chore(release): prepare 0.5.3`。
- 完成操作：
  - `backend/cmd/server/VERSION` 与 `UPSTREAM_BASE.json` 的 xy2api_version 改为 0.5.3；
  - `docs/README.md` 中本变更的状态改为“随 0.5.3 发布”；
  - `GATEWAY_RESILIENCE.md` 版本表：“上游会话状态拒绝”标为 0.5.3；另两行（权威失败终态、原生部分用量）随 0.4.13 发布（`18265f80c`、`47d950eb5`），但一直标着“未发布”，一并更正。
- 修改文件：上述 4 个文件与本文件。
- 验证：`check_docs_consistency.py`、干净树 `sync.py audit`；CI。
- 卡点/风险：无。
- 下一步：合并后在合并提交上打 annotated tag `v0.5.3`，核验 Release；发版后用记忆 PR 补版本表并归档本任务。部署与灰度另行授权。

### 2026-11-04 21:58 UTC — 20261104-collab-plaintext-impl — v0.5.3 发布完成，任务归档

- 请求/目标：按用户 19:30 UTC 授权，合并 PR 并发版 0.5.3；部署与灰度不在授权内。
- 开始状态：PR #196 已于 20:49 UTC 合并（`f8aa06f75`）。
- 完成操作：
  - 版本准备 PR #197 在 CI 通过后普通合并（`92830a0d2`）。
  - annotated tag `v0.5.3` 指向该合并提交，发版说明写在 tag 正文（`--cleanup=verbatim`）。
  - Release 41915422743 第 1 次运行时，build-frontend 的 `vue-tsc -b` Node 堆 OOM，没有产出任何发布物；`gh run rerun --failed` 后通过。
  - 核验：Release 非 draft、非 prerelease，标为 Latest；5 个包加 checksums.txt，sha256 全部一致；darwin_arm64 二进制的构建信息（只读，未执行）为 `92830a0d2`、未修改、release 构建；GHCR 的 `0.5.3`、`latest`、`0.5`、`0` 为同一摘要（`sha256:d97c2bfa1536…`），含 linux/amd64 与 linux/arm64，镜像标签 revision 为 `92830a0d2`、version 为 0.5.3；tag 上的 CI（8 项，含 macOS shell）与 Security Scan 通过。
  - 本任务在“当前交接状态”的小节和“进行中的工作”的条目已原样移到归档文件；遗留并入“未结事项”。
  - 版本表补登 0.5.2 行：0.5.2 发版 agent 的发版后记录只留在共享工作区，没有提交。
- 修改文件：本文件与 `PROJECT_MEMORY_ARCHIVE.md`。
- 验证：`check_docs_consistency.py`、干净树 `sync.py audit`；CI。
- 卡点/风险：Release 前端构建内存临界；本 PR 的 push CI 遇到一次测试替身竞态导致的偶发失败，根因已定位、与 0.5.3 无关。两项都见“未结事项”。
- 下一步：部署与灰度待用户授权，见“未结事项”。

### 2026-11-05 — 20261105-readonly-mcp-design — 源码审计和只读 MCP 方案完成

- 请求/目标：拉取 `liulixin-lex/xy2api`，深入分析管理员权限、数据记录及分析链路，规划根管理员、普通管理员、普通用户可用的只读 MCP。
- 开始状态：克隆到本地工作区；`main` HEAD `381687250909140e8bb253f9b0c3d16d794a366b`，初始工作树干净。本任务期间未切分支。
- 完成操作：按 `AGENTS.md` 完整阅读项目记忆、核实代码/Git；逐项核对 `operatoraccess` catalog/middleware/service、admin/user 路由、usage/ops/账单/运维/韧性/调度/余额/提示词审计/保留配置；统计 catalog 为 538 条管理路由（236 GET、407 module、128 super-only、3 other admin）；浏览 MCP 当前规范、Streamable HTTP、Authorization、Tools 与官方 Go SDK 文档。记录关键调用 ID 差异、尝试级 rescue 事件、读接口的 Redis 清理副作用、active 上游探测和 prompt 全文敏感边界，并编制查询工具、三类权限、认证、只读硬边界、工作包与验收方案。
- 修改文件：`docs/READ_ONLY_MCP_DESIGN.md`；`docs/README.md` 的文档索引；`.gitignore` 的 docs 白名单；本文件交接摘要与操作日志。
- 验证：新方案以 intent-to-add 临时进入索引后，`python3 tools/check_docs_consistency.py` → `documentation consistency: ok`；`git diff --check` 通过。intent-to-add 已移除，工作树保持新文件未跟踪。未运行代码测试（无业务代码变更）。没有连接生产、写数据库、提交、推送或部署。
- 卡点/风险：跨表 request/client/billing ID 不是统一精确关联；需 WP0 用受控样本量化覆盖度。MCP 外发高敏事件／日志会将数据交给第三方模型 host；根管理员全读设计之外仍须明确 host consent。实时 Redis/进程状态以及系统日志有 TTL、drop、开关和实例边界。OAuth host 兼容需实施前验证。
- 下一步：如批准立项，先按设计 WP0 核查并建 OpenSpec；未经另行授权不连接生产或部署。

### 2026-11-05 — 20261105-readonly-mcp-plan-review — 后端可行性与单路逆向审查

- 请求/目标：用户指定 PM 监督与分工、后端 gpt-6.1-sol xhigh、逆向审查仅一路 gpt-6-astra xhigh；完成严谨、精简、可执行的只读 MCP 方案。
- 开始状态：main 基线 `381687250909140e8bb253f9b0c3d16d794a366b` 上已有本任务方案文档；本轮创建 `feat/readonly-mcp-20261105` 分支继续。
- 完成操作：后端核对 fresh ACL、只读 Redis/PG 接口、OAuth 缺口、库候选和 owner/gate；生产按会话授权仅做 READ ONLY transaction、statement timeout 与有限聚合抽样，发现 billing/client/ops ID 不能直接等同且 recovered/final-error 来源不同。单路红队发现的 AnyOf、数值 ID、工具 source、敏感文本、运行开关和 me 工具映射问题已逐项修订。OpenSpec 统一为 WP0–WP5；未测代码/Host 项均未勾选。
- 范围更正：PM 曾误把规划扩展为实现，已立即停止；backend agent 精确撤回自己临时新增文件和 config 修改，backend tracked diff 与 untracked code 均为零；未改依赖/迁移 checksum/Wire/router。最终仅交付开发规划，不声称已实现。
- 修改文件：主方案、OpenSpec proposal/design/spec/tasks、文档索引、docs 白名单、本记忆；生产原始行、标识与凭据不入仓。
- 验证：唯一 Astra 方案签字通过（P0/P1/必要 P2=0）；`python3 tools/check_docs_consistency.py` 与 `git diff --check` 通过。backend tracked diff=0、无未跟踪代码；没有业务实现，未运行代码测试或真实 Host E2E。
- 卡点/风险：实际 OAuth Host 兼容、库 code/refresh 原子性、只读角色配置及性能预算需后续实施验证；规划审查不能证明上线无漏洞。
- 下一步：方案已交付，实施与真实 Host 验证仍未进行；后续按 WP1–WP5 推进。推送/PR/合并/发版/生产写不在本次执行范围；本任务详细登记已归档。


### 2026-11-05 — 20261105-readonly-mcp-implementation — 只读 MCP 本地实现与验收完成

- 请求/目标：按用户“开始实施”开发只读 MCP；PM监督与集成，所有后端由gpt-6.1-sol xhigh执行，Astra xhigh分别进行交付审查与安全红队。
- 开始状态：`feat/readonly-mcp-20261105`，HEAD `381687250909140e8bb253f9b0c3d16d794a366b`；保留上一轮方案/OpenSpec既有改动，未回退其他工作。
- 完成操作：实现22个管理诊断与7个本人查询工具；官方Go SDK stateless transport、本地OAuth AS/PKCE/opaque token/refresh rotation与重放撤销、fresh ACL/token_version/目标和owner检查、独立PG Reader与只读Redis、来源/统计/保留语义、有界控制面审计/限流/清理。完成consent与本人connections/revoke界面、中英文及默认关闭配置。新增迁移290/291并追加checksum；固定列授权和显式函数权限收敛脚本、部署/回退runbook、上游钩子与OpenSpec登记同步完成。
- 修改文件：`backend/internal/mcp/`、config/Wire/router/handler、纯质量快照/Redis只读错误传播、迁移及依赖；`frontend/src/features/mcp/`及profile/router/i18n；CI/Makefile；`docs/READ_ONLY_MCP_DESIGN.md`、`deploy/MCP_READONLY.md`、OpenSpec和本记忆。未改网关调度热路径、未加影子模式。
- 验证：CI等效真实PG suite 65个tests/subtests、12个关键必跑测试全部PASS且0skip/fail；29工具安全列Reader矩阵、Redis只读命令捕获、code/refresh并发单次消费/重放撤销、fresh ACL/AnyOf/flags/target/owner/token_version与控制面容量清理通过。相关7个后端包分拆lint均0问题、server构建/接线/cleanup/config及受影响service/repository定向通过；一次七包lint超时后按包完成。前端全量lint/typecheck/build、115文件1282关键回归及MCP16项流程测试通过，桌面/手机浏览器检查通过。Inspector2.14.0以真实本地TLS/PG OAuth完成授权、7工具发现和profile调用；未禁TLS。两路Astra签字无未解决P0/P1，最后Flush增量也复核通过。
- 仓库门禁：文档一致性、迁移checksum、`git diff --check`、scheduler-diff、upstream-sync工具15项单测与临时干净快照audit通过；仅新增本任务OpenSpec的xy_owned登记及必要字面量，不放宽全局检查。主HEAD/索引未因审计改变，临时审计工作树已移除；主分支实现改动未提交或推送。一次性测试PG、UI fixture与验证二进制已清理，其他资源保留。
- 卡点/边界：无本地开发阻断；没有运行全仓后端unit/integration或触发GitHub CI。Inspector使用合成身份和integration-only consent fixture，产品UI单独验证；合法nullable schema触发49项可移植性warning但0error。未验证生产完整登录/代理链路、指定商业Host及生产数据规模执行计划，不承诺全Host兼容或零漏洞。本轮未连接生产。
- 下一步：按 `deploy/MCP_READONLY.md` 与OpenSpec WP5，在后续明确授权内完成目标环境验收、提交/推送/发布及部署；启用前显式配置独立Reader和函数权限收敛。MCP保持默认关闭，未执行生产迁移或业务写操作。详细过程登记已原样归档。


### 2026-11-06 — 20261106-release054-mcp — 推送、合并与0.5.4发布完成

- 请求/目标：用户明确授权推送、创建PR、合并全部PR并正式发版0.5.4；不含生产部署和MCP启用。
- 开始状态：feat/readonly-mcp-20261105，HEAD 381687250909140e8bb253f9b0c3d16d794a366b，保留上轮75文件待发布实现；远端无其他open PR且origin/main未前移。
- 完成操作：VERSION及UPSTREAM_BASE同步0.5.4；为release frontend加入4GiB Node堆并登记保护字面量。后端gpt-6.1-sol xhigh核对测试兼容；Astra xhigh发布增量签字无P0/P1/必要P2。提交0335f39d6633e6cfb2aab75fcfc55a86620866b8推送并创建PR #199，在20项检查全绿、分支保护CLEAN后以固定head普通合并；合并提交2356b8b200eb567defdd6fe3e3939d5a5ec017e2与已测head树相同。annotated v0.5.4标签对象ea9b6478cb7c1ff3a46833c491ef34346903041b指向该提交；未强推或改写已发布tag。Release运行42083934644成功，正式版非draft、非prerelease且为latest，发布说明已核对并保留Markdown。
- 修改文件：主功能提交包含既有MCP实现及版本/发布流程准备；本次收尾只更新PROJECT_MEMORY、归档、文档索引、设计状态和OpenSpec任务证据，不改发布源码或标签。
- 验证：PR CI42078746808、push CI42078709061及两路Security Scan共20项全绿，包含全仓unit/integration、golangci-lint、MCP真实PG12关键测试0skip、前端、Windows插件、upstream/docs、部署脚本、网关可靠性/韧性、真实Redis缓存与支付PG。首次冷编译耗时较长，未跳过检查或重试掩盖失败。发布verify-version/frontend/release全成功。下载5平台安装包逐项SHA256、归档二进制与MCP runbook存在性通过；Linux amd64 -version报告0.5.4/兼容0.2.15及发布commit。GHCR 0.5.4和latest清单一致，linux/amd64摘要b20d943cc890acc5411835d940110f717f6ec218f5e947a7490d1f50d7e4c5ce、linux/arm64摘要34e61a1d69f1da0e55d2177e5c177c85f028564d3b3b5fbfb101015148a61209，镜像version/revision标签均正确。安装包临时副本已清理。
- 卡点/风险：无发布阻断；未连接生产、迁移生产数据库、配置生产Reader或启用MCP。目标登录/代理/商业Host及生产规模查询计划仍需按WP5.2/WP5.4验收，MCP默认关闭。
- 下一步：发布产物可供用户使用；生产部署与MCP启用需独立授权和部署验收。详细发布前登记已原样归档，本次交接文档PR不改变v0.5.4发布提交。


### 2026-11-06 — 20261105-encrypted-routing-impl — 集成完成，最终门禁全绿，待发布授权

- 请求/目标：按 FINAL（用户 11-05 全部批准）一次发布零中断方案。Claude 任 PM 调度；gpt-6-astra xhigh 负责开发（甲、乙）、测试（丙）、集成（丁）；astra 与 Opus 两路红队逆向审查。
- 开始状态：origin/main `381687250`，分支 `feat/encrypted-routing-20261105`。
- 完成操作：空白占位暂存与冲刷、兼容转换保留空白、按客户端选择心跳并修复直通 ticker 漏拍；E1/E2 严格校验与路由、包装 401/403 请求级终止映射；M3（槽位替换、严格增长重放、Bad 接受门槛、一次状态换号、可重试终止）；输出前流内 forbidden 软冷却；`resilience.*` 计数；`docs/GATEWAY_RESILIENCE.md`、OpenSpec `encrypted-routing-20261105`、policy literal 与目录归属同步。
- 修改文件：`backend/internal/{handler,resilience,service}` 相关代码与测试（77 个文件，+5705/−468）、`docs/GATEWAY_RESILIENCE.md`、`docs/README.md`、`openspec/changes/encrypted-routing-20261105/`、`tools/upstream-sync/policy.json`、本文件。
- 验证：`777597cd2` 13 项门禁全过（见当前交接状态）；`9e10b731c` handler 全量 unit 与 lint；文档提交后 docs 与干净树 audit。
- 卡点/风险：M3 store 的 race 检查只覆盖串行路径（store 复用既有加锁的 lineage 存储）；Codex 0.166 冒烟未做。
- 下一步：用户决定冒烟方式，并授权推送、PR、发版、镜像固化与部署；上线后按 tasks §4.3 验收。
