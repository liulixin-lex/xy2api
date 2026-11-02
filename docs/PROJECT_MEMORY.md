# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-29 及以前（0.4.13 周期及更早）的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-11-01）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### 客户服务：悬浮在线客服与工单整合（2026-11-01 更新：已合并，随 0.5.0 发布）

- **背景**：0.4.14 的在线工单只能异步沟通、只能发文字；用户要求加全站悬浮在线客服，工单并入，左侧菜单「在线工单」改名「客户服务」。
- **目标**（2026-10-31 用户确认）：圆形气泡可拖动、松手靠边、默认右下角；点开可直接给客服发消息或建工单；访客能看到气泡，登录后才能聊天；聊天可发图，选图或粘贴时前端自动压缩；只做后台内提醒；首版不做 AI，只预留 `bot` 角色。
- **要求与约束**：已合入 main，随 0.5.0 发布；不部署、不连接生产；「在线客服」开关默认关闭，升级后不自动出现气泡；工单的表、接口和 `xy-support-tickets` 的 literal 不动。
- **已完成**：分支 `feat/support-chat-widget-20261031` 基于 0.4.16 的 main（`dcd47423e`），两个提交：功能本体与审查修复。包括迁移 287、后端、前端、文档、OpenSpec 和具名补丁 `xy-support-chat`。审查：gpt-6-astra 四轮只读审查加 Claude 多维审查与逐条反驳式核验，确认 30 项缺陷并修复 29 项（未修 1 项见卡点），另补 2 处测试空缺。全量门禁和本地栈端到端（接口 49 项、浏览器 34 项）通过，见操作日志。
- **未完成**：0.5.0 标签、Release 制品验收与记忆收尾；不部署、不连接生产。
- **卡点与风险**：无阻塞。聊天图片进入数据库备份，用「图片保留天数」控制体积；轮询在生产环境的负载未实测。未修：气泡（z-45）拖到右上角时会盖住顶栏用户菜单的右缘（降低层级会被页面浮层盖住，留待取舍）。既有问题（不在本变更）：`App.vue` 的工单 watcher 用数组 getter，登录用户每次刷新资料都会重置工单 store。
- **关键位置**：OpenSpec `add-support-chat-widget-20261031`、[`SUPPORT_TICKETS.md`](SUPPORT_TICKETS.md)；契约、派工记录、端到端脚本和截图在仓库外的交接目录（客户服务，2026-10-31）。

### 版本与环境速览（2026-11-01：0.5.0 版本准备）

- 当前准备产品版本 XY2API v0.5.0；最新正式版仍为 v0.4.16（2026-11-01），Sub2API 兼容基线 0.2.15，迁移共 333 项（最后为 288）。
- 生产：2026-11-02 05:03 UTC 核实运行 0.5.0（应用于 04:51 UTC 重启后健康）；`ops_monitoring_enabled=true`，质量门策略 version 9，五个 OpenAI 门槛组（A–E）均启用质量门槛与利润门。门槛进程内状态已复位；后续仍按新请求评估。外部状态以重新核实为准。
- 预上线主机仍运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.3）；golangci-lint v2.14.0（与 CI 同版）。
- 文档一致性：`python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业执行），规则在 `tools/docs_consistency.json`，改动时按 [`AGENTS.md`](../AGENTS.md)“变更同步”同步文档。
- 本地 `.work/` 只保留进行中的任务，已结束任务的过程稿与证据已于 2026-10-31 删除；发布资产以 GitHub Release 为准。

### 版本发布一览

已发布的各版本均为正式 Release（非 draft、非 prerelease），附件与镜像在发布时已核验；发布说明在 tag 正文。完整交接、验证与核验原文见 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（“2026-10-31 记忆重整原样归档”及更早章节）。

| 版本 | 日期 | 发布提交（相关 PR） | 主要内容 | 迁移 |
|---|---|---|---|---|
| 0.5.0 | 2026-11-01 | 准备中（#181、#182；版本 PR 待合并） | 悬浮在线客服与客户服务工单整合；账号管理页精简与信息层级优化 | 287、288 |
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

- **分组质量门槛第二版（2026-11-02 批准）**：实施、双路红队与本地门禁完成，已合并 [PR #185](https://github.com/liulixin-lex/xy2api/pull/185)（2026-11-02，随 0.5.1 发布），见进行中的工作“20261102-group-quality-gate-v2”。可用账号数告警（号池只有 1–2 个号的五个组，组号见本地交接包）待用户在管理端配置。
- **GPT 报错补漏（2026-11-02 已批准；WP1–WP4、WP6 已实施，[PR #187](https://github.com/liulixin-lex/xy2api/pull/187) 待合并，随 0.5.1 发布）**：订阅共享额度、加密内容跨号、内容拒绝口径、请求兼容类 400 四项，加第 7 版的 400 收尾（WP6），合成一个 PR，叠在 #185 上同期发版；小号池组的兜底分组与告警由管理员在管理端配置。用户追加的 HTTP/1.1 与 HTTP/2 深挖结论（astra 第 8 轮收窄）：本版保持 HTTP/2；现有数据不足以证明改 HTTP/1.1 有净收益，维持暂缓。400 深挖的最大来源是一个旧版 LiteLLM 客户端，需管理员联系该 Key 的用户升级。实施交接包在本地 `.work/tasks/20261102-gpt-error-review/handoff/README.md`，见进行中的工作“20261102-gpt-error-review”。
- **0.4.16 升级配置与量化**：本次只发布、未部署或连接生产。升级前由用户确认 `ops_monitoring_enabled` 开启，补齐真实成本、核对 OAuth 参考倍率，按目标组配置利润门/质量门槛/固定账号、成员关系、并发、优先级与可用数告警。错误率依赖运维日志；C1–C7 缓存量化及是否调整会话绑定 TTL 仍待用户自行执行与决策。
- **0.4.16 已接受边界**：质量状态进程内、多实例各自计算且重启重新积累；Grok 异步视频跨组轮询仍按轮询组计价；批量图片用量不记分组；非调度实例 OAuth 参考倍率可能需重启刷新；利润预览按已保存价格；高流量指标查询与热路径缓存等优化暂缓。完整边界见 v0.4.16 Release 与现行调度文档。
- **Claude 缓存后续**：真实供应商工具缓存标记接受度、物理命中和收益未验证；“跟随全局只延长、不缩短”、客户端声明缓存时恢复温会话预留折扣、OAuth 系统提示 TTL 说明及分组选择器资格待决定。既有 OpenAI/Grok 出站缓存字段、`additional_tools`/namespace 等声明识别、旧 Chat 形状 Responses 工具标记与 `ttl_mode`/`ttl_source` 上下文传递仍待独立处理；本版未扩展这些行为。
- **部署与上线观察**：0.4.15 已于 2026-10-31 在线更新上线（非本仓库 agent 操作），上线观察未做；Anthropic TTL 的真实供应商缓存命中率与收益未验证；0.4.9 的过渡开关与 fast_fail 待上线 7 天后复核；智商检测参数化题未做真实上游校准，旧结构待上线观察 7 天后删除，单账号“不降智”分组需运维加第 2 个账号；N5 阈值须上线后校准；0.4.12 上线约四小时的只读观察未证明首字与缓存全面改善（样本小，不作因果归因）。
- **功能遗留**：WS 余额阶梯与复位暂缓；Anthropic 暂存失败尝试的上游成本由网关承担；非 OpenAI breaker 为进程内状态，多实例各自计数；迁移的空位预检有抢槽竞态；非 `v2:` 会话键的绑定删除不做属主比较，同一会话两个并发请求同时迁出时，后一个可能删掉前一个刚写的新绑定（随后按同样档位顺序重选，不选错号）；账号不在任何 OpenAI 组时做的手动恢复，在它首次入组时仍会转观察（门槛第二版红队判定接受）；管理端保存过设置的部署 Gemini 截止仍为 90 秒；0.4.5 OpenSpec tasks §7 的 7.2/7.3/7.5/7.6；渠道监控 v1 待真实上游验证（兼容矩阵、推理模型响应时间）与审查 P2-7；无号兜底 INC-P2-1（WebSocket 快照毫秒级窗口，不阻断）。
- **工单与 2FA**：真实 OpenAI 网页登录/MFA 只有 mock；启用 2FA 须部署 Worker、固定运行时和加密恢复目录，回退前须解决不确定任务并保留密钥与恢复日志。
- **安全与依赖**：vue-i18n 安全例外 2026-11-27 到期前处理；两台主机的 SSH 密码曾出现在对话中，建议改用密钥并更换；生产 settings 中的明文代理凭据建议轮换；`.github/SECURITY.md` 仍指向上游 Sub2API 的报告渠道，应改为本仓库（该文件未分类，改动需同时登记 `policy.json`）。
- **待用户决定**：Codex 历史联网搜索导致 502（2026-10-17 诊断，原文已归档）——恢复映射或调整白名单，代码改进前先核实 0.4.0 的 `resilience_web_search_replay` 是否覆盖 apikey 中转账号；XY-image 0.0.17 先交接并经用户确认；远端保留的 3 个含未合入提交的分支（`feat/native-stream-recovery-20260930`、`fix/native-stream-concurrency-20261001`、`fix/signature-retry-preserve-tool-result-media`）是否删除；`tools/gateway-resilience/` 下 `staging/`、`fault-upstream/`、`cdn-sim/` 发布演练脚手架 0.4.6 后未再使用，是否删除。
- **测试稳定性**：CI 偶发 `TestCompatPreludeCommentHeartbeatStillAllowsFailover/enabled` 等心跳计时用例；2026-10-28 已放宽一组截止时间，其余未定位。`TestGatewayServiceRecordUsage_PeakRateAffectsTokenModeImageOutputTokens` 与 OpenAI 同名用例把峰值时段写成 00:00–23:59，而判定区间是 `[PeakStart, PeakEnd)`，在 23:59 那一分钟运行会失败（2026-10-31 本地遇到，干净 main 相同）。`-race` 下两条用例偶发数据竞争，都在测试代码里，干净 main 同样能复现（2026-11-02）：`TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant` 是测试替身的竞争，10 次约 2 次；`TestRecordCyberPolicyEvent_RuntimeSnapshotRefreshFailureKeepsStaleScope` 在构造函数启动后台 worker 之后才改 `runtimeCacheTTL`。
- **仓库外环境收尾**：测试服务器的候选镜像、浸泡与演练目录、`xy2api_upgrade` 库按 staging-validation §11 清理；预上线主机的两份切换前备份在正式部署后删除。

## 进行中的工作

### 20261101-accounts-ui-polish（2026-11-01：已合并 #182）

- 目标：账号管理页精简。分组列门槛详情改为悬停小标识才显示；智商检测列改为竖向排列并随模型数自适应对齐；逐列梳理信息与交互，去掉重复与冲突，保留必要功能。
- 分支与 worktree：`feat/accounts-ui-polish-20261101`（基于 main `c2a247c97`），仓外工作树 `accounts-ui-polish`。
- 状态：#182 已合并，随 0.5.0 发布。只改前端与相关现行文档，不改后端接口；设计、报告与截图在仓外任务目录。已接受：合格分组的逐组实测不在本页显示；固定列分隔线对所有带固定列的管理表格生效。“冷却只在状态列显示”已由下一条取代。

### 20261101-accounts-status-column — 账号页状态列信息重新分配（2026-11-01 开始；Claude）

- 目标：用户反馈状态列数据过多、显示冲突（“正常”下面压着红色降智冷却块和三行近期数据），要求合理分配位置，完成后推送并提 PR。
- 分支：`feat/accounts-status-column-20261101`，基于 main `84e610681`（v0.5.0），复用仓外工作树 `accounts-ui-polish`。
- 状态：实施与本地验证完成，已推送并开 PR，等待合并授权；发版另行授权。

### 20261031-support-chat-widget — 客户服务：悬浮在线客服与工单整合（已合并 #181）

- 请求：用户要求新增全站在线客服——圆形悬浮气泡（可拖动、自动靠边、默认右下角），点开可直接给客服发消息或创建工单；现有在线工单并入，左侧菜单「在线工单」改为「客户服务」。用户已确认：访客可见气泡但需登录才能聊天；聊天可发图片，输入时前端自动压缩；客服提醒只做后台内；首版不做 AI，仅预留 `bot` 角色。方案经用户批准。
- 开始状态：分支 `feat/support-chat-widget-20261031`，基于 origin/main `874560eb9`，独立工作树，干净。
- 方案要点：聊天单独建 `support_chat_*` 表（迁移 287），工单表与接口不动；沿用 `support_tickets` 权限模块与 `support_ticket_enabled` 总开关，新增 `support_chat_enabled` 子开关（默认关闭，升级不自动出现气泡）；自适应轮询（不做 WS/SSE）；图片前端压缩后存数据库并鉴权读取，按保留天数清理；客服可删除消息、删除会话、禁言。
- 分工：设计/契约/文档/OpenSpec/policy 由主 agent；后端 gpt-6.1-sol，前端 Opus，漏洞审查 gpt-6-astra。交付止于 PR，不合并、不发版、不部署。
- 状态（2026-11-01）：见顶部「客户服务」小节与操作日志。

### 20261102-group-quality-gate-v2 — 分组质量门槛第二版（已合并 #185，随 0.5.1 发布）

- 来源：20261102-gate-postlaunch-review 的生产只读评估（见操作日志）。用户 2026-11-02 批准后端、前端、文档合成一个 PR；运维只批准可用账号数告警（号池只有 1–2 个号的五个组，由用户在管理端配置）；用量记录首字保持 `openai_ttft_mode=semantic`。
- 内容：门槛续轮首字改读端到端首字；两档排序，快档 = 已知且 p90 ≤ min(快档线, 组内最快 + 4s)，无数据归“其他”；试用改为不降档的观察（20 个请求或 30 分钟），手动恢复同路径，账号菜单提供恢复入口；门槛组绑定号 p90 超过快档线、且有快档空位时迁出；`guard_hold`、`last_resort`、`migrate` 记为 Warn。
- 材料：OpenSpec `group-quality-gate-v2-20261102` 在本地工作树分支 `feat/group-quality-gate-v2-20261102`（基于 `84e610681`，未提交、未推送）；派工说明、代码地图与证据在本地 `.work/tasks/20261102-gate-postlaunch-review/handoff/`。
- 进展（11-02 08:00 UTC）：实施由主持 agent 6c3b2b9e 在上述工作树推进（后端、前端子 agent 各一）。重启后的只读复核发现档位记忆有缺口：熔断清空记忆，或短时变慢被迁空后，主力低价号回不了 0 档，按实际流量这部分毛利会降到一半以下。用户批准修正，记忆改为 `DataTier/TierAt/FastAt`：30 分钟内沿用上次判定，之后 6 小时内快过则回 0 档重新检验，熔断与恢复都不清空。astra 第四轮无异议，修正已发给 6c3b2b9e，说明在本地 `handoff/amendment_20261102.md`。另一处缺口已补：离组保留原来只按最后一次熔断结束计 2 小时，从没熔断过的号离组再加回会丢档位记忆。现改为从最后一次熔断结束与最后一次按数据定档中较晚的时刻起算 2 小时，6c3b2b9e 已采纳。
- 下一步：主持 agent 把修正落实到规格、任务与代码后开 PR；发版按惯例交给 astra；不部署。
- 实施（2026-11-02 起，Claude 主持）：工作树 HEAD `84e610681`，仅 OpenSpec 与 `docs/README.md` 未提交。分工：后端（`backend/`）Codex gpt-6.1-sol xhigh；前端（`frontend/`）Claude Opus 5.5 high；现行文档与 `tools/docs_consistency.json` 由主持人；完成后 gpt-6-astra xhigh（逻辑红队）与 Opus 5.5 xhigh（规格与产品闭环红队）逆向审查。规格补充一条：门槛组迁移另要求低倍率优先开启（档位只在开启时决定选号顺序），生产已开启，效果不变。档位记忆修正已并入 spec/design/tasks 与 `GROUP_QUALITY_GATE.md` 并发给后端（批准由方案主持代为转达）。
- 结果（11-02）：本地提交 `e1b21005a`（分支 `feat/group-quality-gate-v2-20261102`，基于 `84e610681`，未推送）。红队 A 5 条：修 2 条（stale 轮次不刷新档位记忆；仓储用例断言端到端优先），不修 3 条（旧格式会话键删除、候选排除判慢中账号是正确行为、无组时恢复的边界，见未结事项）；红队 B 21 条：除记忆登记一条（看的是 HEAD 版本）外全部采纳，含公开仓库脱敏（OpenSpec 去掉生产分组 ID 与经营数字）。门禁：Go build/vet 三标签、service/repository 全量 unit、门槛定向 `-race`、测试机仓储集成、golangci-lint 0、前端 lint/typecheck/485 文件 3787 项/build、文档一致性、scheduler-diff、干净树 audit、tools 96 项均通过。
- 推送与 PR（11-02 用户授权，[PR #185](https://github.com/liulixin-lex/xy2api/pull/185)）：记忆按用户选择脱敏后随本 PR 提交——未提交部分中的生产分组号、账号号与经营比例改为代称或定性说法（门槛组 A–E、枢纽号等，对照只在本地交接包 `handoff/aliases.md`）。PR 分支已含主工作区截至 11-02 10:00 UTC 的这两份记忆；PR 合并后，主工作区先把之后新增的本地条目与 main 对比保留，再丢弃其余本地改动并拉取 main。
- CI（11-02 10:40 UTC，head `cf0a2ee0e`）：test、golangci-lint、frontend、upstream-sync-audit、两项 security 等 9 项通过；`shell`（macos-15，只检查部署脚本，本 PR 未改）两次都未分到 runner（runner_id 0、0 步、约 15 分钟后取消），与 PR #168 当时现象相同，属 CI 基础设施问题，合并前需重跑通过或由用户确认 Actions 的 macOS 额度。

### 20261102-gate-error-volume — 门槛错误率熔断的体量偏差评估（方案已批准，实施中）

- 来源：用户 2026-11-02 提问。现在按请求数百分比算错误率，大号报错率被压低，无法熔断，但报错请求已经很多。要求 Claude 与 astra 深入分析，给出最终方案。HTTP/1.1 改动暂不做。
- 材料：本地 `.work/tasks/20261102-gate-error-volume/`，含 README、`evidence/`（11-01 12:00 起的生产只读导出，不入库）、`sim/`、`reviews/`、`PLAN.md`、`handoff/`。
- 结论（astra 第 2 轮签字）：
  - 按请求比率判定是合理的，不加按绝对数熔断。
  - 用户看到“大号报错多却不熔断”，根因是漏记：网关自己检测到的输出后断流（读流出错、缺终态、停滞）只通知了快层，没有写进逐次尝试记录。门槛既漏掉了这些失败，又把对应的用量行算成了成功。27.4 小时内 OpenAI 有 260 行这样的失败，全部走 HTTP。
  - 补齐后，现行 10% 线会在 11-02 约 15:06 熔断枢纽号。
- 方案（用户 11-02 批准）：判据不变。在失败源头记下事实，写错误日志时补一条失败尝试（kind `stream_failed`，类别 TRANSIENT 或 STALL）。
  - 去重边界是“每次实际发送前的上游事件条数”。astra 第 3 轮修正：compact 回退等原地重发不会刷新尝试快照，不能用尝试开始时间。
  - 归因修正（用户批准，astra 第 4 轮签字）：补项先写回请求上下文再归因，这类失败从“平台 / 内部”改归 `upstream / provider / upstream_http`。SLA、总错误率、业务标志与严重级别不变；上游错误率及其告警、用户侧分类、渠道监控 V2 的上游计数会变，文档要写明。
  - WebSocket 本轮不做（生产没有 WS 流量）。不新增配置。
- 交接：本地 `.work/tasks/20261102-gate-error-volume/handoff/`，入口 `README.md`，另有 `SPEC.md`、`CODE_MAP.md`、`VERIFY.md` 与只读验收 SQL。SQL 已在生产只读试跑，修复前基线已记入。
  - 分支：从 P0 集成分支 `fix/gpt-error-recovery-20261102` 拉出 `fix/gate-post-output-attempts-20261102`，PR base 先设为 P0 分支。两者共改 `resilience_post_output.go` 与 `ops_error_logger.go`，本修复跟随 P0 的分类口径。
  - 发版顺序：#185 → P0 → 本 PR → 0.5.1。P0 若要先发版，本修复进下一个补丁版本。
  - 停滞的“已出字”判断复用 `RequestState.FirstOutputNoted()`，不改 `noteResilienceStall` 的签名，因为它在两个上游文件里是 required_literals。
- 状态（交接时）：未改代码，未改生产。
- 实施（2026-11-02 19:20 UTC 起，Claude 主持；用户授权推送、PR、合并全部 PR 与发布 0.5.1，不部署）：
  - 分支 `fix/gate-post-output-attempts-20261102`，叠在 P0（PR #187）之上，在仓外独立工作树。
  - 分工：Codex gpt-6.1-sol xhigh 两个子 agent。F 负责代码与测试，G 负责 OpenSpec `gate-post-output-attempts-20261102` 与四份现行文档。`docs/README.md` 与 `policy.json` 由主持人登记。
  - 主持人复核纠正 1 处：F 起初只在“发送边界到事实发生时”这一段查同账号事件，与规格不符。规格要求边界之后到请求结束有同账号事件就不补。HTTP 流出字后不会再对同一账号发起真实请求，所以有上界反而可能重复补记。F 已改，并确认不存在这样的路径。
  - 门禁：四种构建标签 vet、文档一致性、scheduler-diff、干净树 audit 通过。`-race` 回归与第 3 轮红队（astra 逻辑、Opus 规格；同时确认 P0 第 2 轮修复）进行中。

### 20261102-gpt-error-review — GPT 一周报错复盘与韧性优化方案（已实施，PR #187 待合并，随 0.5.1 发布）

- 请求：用户 2026-11-02 授权只读连接生产，分析最近一周 GPT（OpenAI 平台）报错的构成；核实网关韧性、补救、重试与路由是否已化解，以及用户端表现（客户端显示重连还是直接中断）；结合 main 与开放 PR 的最新代码，提出进一步可靠的优化方案。Claude 主持，gpt-6-astra xhigh 反方独立研究并交叉讨论，产出联合方案。
- 开始状态：main `84e610681`（v0.5.0），工作树仅有既有两份记忆的未提交改动（保留不动）；开放 PR #184、#185。
- 范围：只读分析与方案，不改代码、不改生产配置；本地证据放 `.work/tasks/20261102-gpt-error-review/`（不入库）。
- 结论（11-02，详见本地 `FINAL.md`）：
  - **报错构成**：一周 OpenAI 运维错误行里，绝大多数是业务限制（少数脚本 Key 在余额不足时循环请求）和已被换号恢复的行。客户端可见的非业务失败约占请求的 1.3%（行级近似，分母含业务限制行时约 1.1%），主要有四类：
    - 输出后断流：SSE 无续接，不能换号；
    - 小号池组整体故障时的耗尽；
    - 两处上游错误被误判为客户端错误；
    - 用户侧错误。
  - **首输出截止换号**：用户无感，但约 5k 个请求每次白等约 33 s，一周合计约 21 小时。
  - **用户端表现**（Codex 0.159–0.166 源码核实）：
    - 输出后断流和 5xx：显示 `Reconnecting… n/5` 并重发整个回合，约八成随后成功。
    - 直接中断的情形：HTTP 400（显示原文）、不带 Retry-After 的 429、`server_is_overloaded` 不带重试建议（含 turn_cap，提示“请换模型”）、quota 一类错误。
    - 403、404 等其他 4xx：先重试 5 次再中断。
    - WS 空闲被 1008 关闭：客户端静默重连，用户无感。
  - **网关已做对的**：上游过载码已改写为可重试码；无号 429 已带 Retry-After。
- 联合方案（astra 第 2 轮收敛，第 3 轮于 11-02 12:10 UTC 签字）：
  - **P0，一个 PR**：
    1. `subscription_sharing_usage_limit_exceeded` 按上游账号额度处理：输出前换号并冷却。本周 139 次直接失败。
    2. 加密 reasoning 的剥离重放补齐带内 `response.failed` 形态与 “decrypted or decoded” 文案。本周 29 次；PR #185 的迁出会让它变多。（更正：跨模式 sanitizer 早已由 handler 在换号时调用，不需要再接。）
    3. 流式路径的在途余额预留（`concurrent_balance_reserved`）与精确的内容策略拒绝置 `is_business_limited`。看板 SLA 只按这个字段排除。要在 `classifyOpsErrorLog` 加一个不受残留上游上下文门控的精确信号分支，只调用业务 marker 不够；普通 400 仍计入 SLA。
  - **P1**：
    - 为无兜底的小号池组配置兜底与可用数告警。
    - “耗尽后转兜底组一次”为条件项，配置后 7 天仍复现再做。
  - **P2**：断流率排序、首输出截止动态化，都等 #185 上线 7 天后复测再定。
  - **不做**：输出后续接、对冲请求、Redis 组断路器、改 turn_cap、改 WS 首帧超时。
- 批准（11-02）：用户批准 P0 三项合成一个 PR；兜底分组由管理员配置。规划方写交接时重新核对代码，更正两点：跨模式 sanitizer 已接好；在途预留的 594 条误记行可能多在 0.4.12 修复上线前，实施前先跑只读核查，0.4.15 之后若已无此类行就取消这一半。
- 交接：本地 `.work/tasks/20261102-gpt-error-review/handoff/`，内容包括入口 README（范围、更正、分派、规则、完成标准）、代码地图、WP1–WP3、验证与红队清单、后续项、只读 SQL。WP2 须与 #185 同期或更早上线。
- 第 2 轮（11-02，用户要求按 0.5.0 时间线重估，并与 #185 同期合入、发版、上线；只读复查生产）：
  - **时间线**：窗口起点是 0.4.8（定稿误写为 0.4.12）。报错路径从 0.4.13 起固定，路由从 0.4.16 起固定，0.5.0 没有改报错路径。
  - **非业务最终失败**（每万请求）：0.4.8–0.4.11 为 154，0.4.12 为 95，0.4.13–0.4.15 为 145（含小组事故），0.4.16–0.5.0 为 87。
  - **范围调整**：
    - 在途预留 0.4.12 已修复，撤掉；
    - 加密内容自 0.4.13 起为 0（同期会话跨号很频繁），转为上线后观察项；
    - 订阅共享额度改为复用余额暂停阶梯：分类表加码与文案，带内余额码事件与 HTTP 对齐，额度用尽不再计入 #185 门槛的软失败；
    - 内容拒绝只保留精确信号，分类表补图片中转的码与中文文案。
  - **新发现，交用户决定**：
    - 0.4.16 之后输出后断流翻倍，八成来自枢纽号，主要是流量集中所致；按暴露折算，它的单位风险与同组其他号相近。
    - 当前的耗尽几乎都在一个无兜底小组，需要管理员配置兜底。
  - 交接包已改写为第 2 版，入口不变，叠在 #185 的 head 上实施。astra 第 4 轮复核见本地 `r2/astra_r4.md`。
- 第 3 轮（11-02，用户要求一步到位，重点深挖加密跨号与 400，且只能在本网关优化；只读复查，依据见本地 `r3/ANALYSIS_r3.md`）：
  - **加密跨号**：按完整留存期拆开，最终失败约 290 次，10-29 之后为 0（涉事中转号已停用）。主要形态是 Codex 多 agent 的 `agent_message` 密文，它就是子 agent 的任务正文，不能剥离。网关已有的父线程亲和只给自动审查用；Codex 源码证实子线程在 `client_metadata` 里带父线程 ID。带内加密拒绝现在会换号并记 TRANSIENT，会拉低 #185 门槛。另发现存量缺陷：现有父线程亲和对父线程 ID 直接求哈希，而 routing v2（10-10）以来生产绑定都按“API Key × 分组 × 模型”加了作用域，所以亲和在生产上很可能从未命中；WP2 一并修正（同模型子 agent 可命中，自动审查因模型不同仍是已知边界）。
  - **400**：0.4.13 起 84 小时的“上游明确点名”请求兼容问题：确定可修约 40 次（`top_p`、`metadata`、深层 `prompt_cache_breakpoint`、alpha search 能力类 400；Chat 路径没有被拒字段重试）；另有 `tool_search_call` 的 arguments 类型约 118 次，来自一个自动化客户端反复重发同一段坏历史（不经网关的工具降级兜底），字符串能严格解析为对象的部分可修，比例要上线后才知道。中转通用包装、未点名字段、中转自身配置缺失等不处理，理由写进交接包。
  - **断流**：按首字后时长分档，各号单位风险相近，暂不加路由策略；网关自身的停滞切断约占枢纽号断流的 9%，阈值不改。
  - **502/503**（用户追加；固定窗口 11-01 11:54 至 11-02 14:00 UTC 重算，更正此前的“每万约 68”）：OpenAI 最终 5xx 每万成功请求 62.3 行。82% 是中转侧的输出后断流（中转发出 failed 事件、对端重置单条 HTTP/2 流、读流中断、枢纽号在固定时点后完全静默等），Codex 都会重连，没有硬中断形态（带内过载码已改写）；13% 是无兜底小组的耗尽。另有一种整条 HTTP/2 连接被对端重置、同号在途流一起断的情况（11-02 共 3 次）。关闭 OpenAI 上游 HTTP/2 能缩小它的波及面，但用户已决定 HTTP/1.1 改动暂不做，只在复测里持续统计。不新增代码项，只新增管理员操作（兜底分组；去掉中转号不支持的模型）。
  - **范围（第 3 版）**：WP1 订阅共享额度（补 astra 第 4 轮两项：输出后不计账号失败、换号错误保留原始载荷）；WP2 加密跨号（父线程亲和推广、统一判据、请求级处理、可剥离才同号重放，HTTP 与带内同样处理）；WP3 内容拒绝口径（验收与 `invalid_prompt` 边界分开）；WP4 请求兼容类 400。生产查询只经只读包装脚本。
- 第 4 版（11-02，astra 第 5 轮阻断项，依据本地 `r3/astra_r5.md`）：WP4 的 arguments 只在 `tool_search_call` 且字符串严格解析为对象时转换，自动删除字段只新增 5 个（`top_p`、`metadata` 与 3 个采样参数），深层 cache 标记只删两层；WP2 的账号副作用在 `handleOpenAIAccountUpstreamError` 最前面早退（早于临时不可调度规则，不受总开关影响），HTTP 终态与带内事件的运维记录按完整载荷计算尝试类别；WP3 传入终态的 type。
- 第 5 版（11-02，astra 第 6 轮阻断项，依据本地 `r3/astra_r6.md`）：
  - WP2 的加密拒绝早退扩到 `CheckErrorPolicy` 与 `HandleUpstreamError` 开头。兼容路径不换号时会直接执行临时不可调度规则，count_tokens 也会直接调用通用入口。
  - 兼容终态的运维事件也按完整载荷记类别。
  - WP3 改用归一化之前的原始 type；改写 type/code 的终态出口设“内容拒绝已确认”标记。
  - 复测 SQL 的 V3 断言词表与分类器同源。
- 第 5 版于 11-02 15:59 UTC 获 astra 第 7 轮签字（本地 `r3/astra_r7.md`），非阻断措辞已改。
- 第 6 版（11-02，用户追加：深挖 HTTP/1.1 与 HTTP/2；依据本地 `r3/ANALYSIS_r3.md` §6，astra 第 8 轮 `r3/astra_r8.md`）：
  - **本版保持 HTTP/2，维持“HTTP/1.1 暂缓”。**
    - 11-02 14:00 UTC 起，枢纽站点的连接被频繁重置。不同号的不同连接常在 3 秒内相继断开，有时跨两个对端地址。断开频次与平均负载不成比例，也没有 GOAWAY 等协议层信号，像站点相关的故障。
    - astra 第 8 轮收窄措辞：这只说明现有数据不足以证明改 HTTP/1.1 有净收益，不能完全排除 HTTP/2 特有的触发。
  - **评估后不做**：
    - 输出前连接重置的同号重试：现有通道用尽后会走 1 分钟的空响应停调；量小，换号已能救回；
    - 调整传输参数与连接池；
    - 入口 Caddy 调参。
  - **曾提出、已撤下：快层“3 次/60 秒”按同一瞬间合并（原 WP5）。**
    - astra 第 8 轮阻断：与 20261102-gate-error-volume 方案保留的快层逐请求计数冲突；证据只说明批量会触发快层，没有证明暂停无益；共享 tracker 还作用于其他平台。
    - 原文只在本地留档。复测 SQL 加 V7，只观察快层暂停，不设目标。
- 第 7 版（11-02，用户追加：深挖 400；依据本地 `r4/ANALYSIS_r4.md`，astra 第 9、10 轮 `r4/astra_r9.md`、`r4/astra_r10.md`）：
  - **规模**：0.5.0 上线后约 21.7 小时，运维终态 400 全平台约百行，其中一部分是 HTTP 200 之后的带内终态，状态由日志按 code/type 推断。同期全部 4xx 有四分之三是业务限制，按设计返回。用户看到的“很多 400”主要是这两类。
  - **最大来源**：一个旧版 LiteLLM 客户端（早于 LiteLLM 支持 `tool_search`），反复重发带字符串 arguments 的 `tool_search_call` 历史。WP4 只修能严格解析为对象的部分，根治要管理员联系该 Key 的用户升级。
  - **新增 WP6**（astra 第 9 轮找出，源码已核实，第 10 轮同意）：
    - Grok：不换号时，同一次尝试只执行一次账号副作用、只登记一次，做法是兼容出口加 `accountErrorAlreadyHandled`。只有 422 + `api_error` + 固定句 “Service temporarily unavailable” 按瞬时失败换号；换号判据、`ClassifyFailoverError` 与尝试类别共用这一个判据。
    - alpha search：上游非 2xx 不再按账号成功上报，只补一条尝试记录。不改报失败，以免客户端自己的 4xx 进入错误统计与健康熔断。E5001 + `server_error` 走现有换号预算，用尽时返回 502/503。
    - 流式终态：本网关实际写出终态时记下的状态，优先于 SSE code/type 推断。413 换号用尽后不再显示成 400。
  - **观察**：
    - Grok 400 + `invalid_request_error`；
    - Gemini 中转的请求体拒绝；
    - Anthropic 中转的按模型冷却 400；
    - Claude 的 `temperature`；
    - gpt-6.1-sol 工具调用选到只支持 Chat 的号。管理员提示：有这类流量的分组要配支持 Responses 的号。
    - 中转带内的请求体超限。
  - **不做**：通用 400 换号、额外的请求体超限重试、静默改写 thinking 或 reasoning effort、新增罚号机制。
  - **复测**：SQL 加 V8，查 Grok 422 最终失败、重复登记、alpha E5001 与请求体超限以 400 结束的行。已在历史窗口试跑，基线在本地。
- 下一步：
  - 实施方按第 6 版交接包实施 WP1–WP4，已于 16:50 UTC 开工，见下方“实施”。
  - 第 7 版的 WP6 不能和 A–D 并行，因为改的文件与 A、C、D 重叠。要等 A–D 合入集成分支后，另派一个子 agent 在集成分支上实施，见交接包 README 的“分派建议”。
  - 红队复核后交用户。发版顺序为 #185、本 PR、0.5.1。推送与发版待用户授权（惯例交 astra），部署另行授权。
  - 上线 7 天后的复测为只读，需要用户在会话内授权，或由用户自己运行 SQL。
- 实施（2026-11-02 16:50 UTC 起，Claude 主持）：
  - **分支**：集成分支 `fix/gpt-error-recovery-20261102`，基于 #185 head `cf0a2ee0e`，在仓外独立工作树。
  - **分工**：四个后端子 agent（Codex gpt-6.1-sol xhigh）并行，各占一个仓外工作树与本地分支 `wip/gpterr-{a,b,c,d}`：
    - A：WP1 与 WP3；
    - B：WP2 的 §1、§2、§4、§5，加 WP4 §3；
    - C：WP4 §1、§2、§4；
    - D：WP2 §3，父线程亲和与作用域修正。从 B 拆出，缩短关键路径。
  - **不需要前端**：本方案没有界面改动。
  - **主持人负责**：合并共用文件，统一写文档、`policy.json` 与本文件。
  - **复核**：合并后由 gpt-6-astra xhigh（逻辑）与 Opus 5.5 xhigh（规格与产品闭环）做只读红队。
  - **记录**：派工规则与各实施者的报告在本地任务目录 `impl/`。
  - **集成（18:20 UTC）**：四个实施提交加文档登记提交已在集成分支（`c3eb3ae3c`），只在本地。
    - 主持人决定 1：流内余额码只认 `response.error.code`、`error.code`，与分类提取一致。
    - 主持人决定 2：带内 lineage 复用 Forward 的会话哈希，不在请求中途调用 `GenerateSessionHash`。
    - 主持人决定 3：请求修复的预备不限平台，避免回退已有的 HTTP 剥离。
    - 合并时补齐 `scheduler-diff.json`：会话键重构改动了原版调度文件。
    - 验证：build、vet、文档一致性、scheduler-diff 通过；全量 `-race` 回归与双路红队进行中。
    - 本机注意：四个实施者并行编译 service 包时，Go 构建缓存把磁盘占满，内存也吃紧；已清理一天以上未用的缓存。门禁一律顺序运行。
  - **全量回归与红队第 1 轮（19:05 UTC）**：
    - 全量 `-race`：resilience、handler、apicompat 通过；service 只有 `TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant` 失败。这是测试替身的数据竞争，在 #185 干净的 head 上 10 次复现 2 次，属于既有问题，与本任务无关，记入未结事项。
    - 红队阻断项共 5 条，采纳 4 条：
      - astra：WS 轮次的内容拒绝读到连接共享 gin 上的残留。修法是把判定移到调用方，WS 轮次只用本轮快照。
      - astra：内嵌 JSON 的加密拒绝在路由与运维类别上不一致。修法是收窄判据，取值来源与分类器相同。
      - Opus：带内余额码遇到语义 429 时，绕过了外层头剥离。修法是 429 仍走原分支。
      - Opus：manual_merge 文件 `openai_gateway_forward.go` 的核心行没有登记。已补登记。
    - 驳回 1 条：astra 认为中转 502 的 `invalid_prompt` 改记 `CLIENT_ERROR` 不该算软失败变化。理由是换号尝试本来就按完整载荷记录，2c 也明写对所有错误生效。文档已写明这一点。
    - 采纳 Opus 的建议项：
      - 文档 11 处更正；
      - 原生 HTTP 被拒字段重放改打 `request_repair_replay`；
      - #185 的 OpenSpec 措辞同步；
      - 补登记。
    - 本地复测 SQL 的 V2、V5 原来按 `ops_system_logs` 查这个 Info 级事件，查不到。已改为用 docker 日志抽样。
    - 修正后集成分支 head 为 `91999fbd0`：build、vet、定向 `-race`、文档一致性、scheduler-diff 通过。
  - **WP6（19:10 UTC 起）**：按第 7 版交接包，由子 agent E（Codex gpt-6.1-sol xhigh）在基于 `91999fbd0` 的本地分支 `wip/gpterr-e` 上实施。
    - 合入时丢弃了 E 对本文件的改动。
    - 两个测试在无标签与 integration 构建下引用了 unit 专用辅助函数，CI 有一步不带标签的 `go test`，会编译失败。主持人已改为不依赖它们。
  - **红队第 2 轮（20:40 UTC）**：
    - astra 阻断 1 项：Grok 精确 422 用尽后仍返回 502。按方案改为 503，尝试保留上游 422。
    - Opus 阻断 2 项：
      - Grok chat bridge 出口仍重复登记、重复走账号副作用，已改调 `…AfterAccountHandling`；
      - WP6 关键行补登记。
    - 采纳的建议：
      - 通用 GatewayHandler 三处 Responses 终态接入带状态 writer，行为全网关一致；
      - alpha 非 2xx 只在 ≥400 时记尝试；
      - §3、§6 补行。
    - 不采纳：Opus 建议文档改写 502（与方案的 503 冲突）、删除 Grok 新增 class（E 证明删除后带顶层额度标记的 422 会引入冷却差异）。
    - 不修：Grok Messages 非换号出口仍可能走通用 OpenAI 副作用，是基线既有边界，记为残留。
  - **推送与 PR（21:20 UTC）**：#185 合并后，集成分支合入 main，推送并开 [PR #187](https://github.com/liulixin-lex/xy2api/pull/187)（base main）。
    - 改动包 `-race` 回归只有两条既有测试竞争，见未结事项“测试稳定性”。
    - 第 3 轮红队确认 P0 第 2 轮修复（与门槛补齐的审查合并进行），有阻断再追加提交。

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

### 2026-10-31 — 20261031-workspace-slim — 工作区精简与规范化完成，开 PR 合并

- 请求/目标：用户要求精简多次迭代留下的数据，只保留核心文件，使工作区标准化、简洁，便于后续版本开发。先只读审计并给出分类方案，用户 2026-10-31 选定：`.work/` 已结束任务直接删除；删除已合并远端分支（含 PR 已合并的 2 个）；跟踪文件按审计清单全部精简；推送、开 PR 并合并。
- 开始状态：main `af83176ee`，工作树干净；本地 10 个分支（9 个已合并）与 1 个已合并分支的工作树；远端 104 个非 main 分支；`.work/` 1.6 GB（0.4.12 发布二进制与附件、764 MB 单测日志及 12 个已结束任务目录）。
- 完成操作：两路只读审计逐项核对引用、CI、`policy.json` 所有权与 provenance 链后删除：历史文档（可控调度、UI 调度设计、早期智商检测、v0.0.10 审计、v0.2.1 修复 4 份、计费完整性修复 2 份、调度前置调研、截图）、`docs/upstream-sync/` 中除 `v0.2.15.json` 外的报告（无工具读取）、OpenSpec `operator-role-20261027`、`upstream-balance-20261027`、`async-image-tasks-v2`、`stabilize-openai-iq-check`、`iq-detection-operations`、`assets/partners/`、`deploy/Makefile` 与 datamanagementd 三个文件。保留：上游来源的 2 个 OpenSpec（删除会让 audit 失败）、被代码注释与契约引用的韧性/份额调度设计、近期 TTL 与工单变更、`channel-monitor-v2-safe-defaults.md`（V2 模式仍可选，目录移到现行）、`UPSTREAM_SYNC_V0214/V0215.md`、`third-party/`。记忆重整：顶部旧节、channel-monitor-v1 登记与 10-26～10-29 日志原样追加到归档新章节，校验归档原前缀不变、移出内容逐行可在归档找到；修正过时状态（TTL“未实施”、#120/#121/#125“未发版”，三者实际在 v0.4.6）。分支：删除 9 个本地已合并分支与同级目录中的 `rec-next` 工作树，`git push --delete` 删除 101 个远端分支。本地 `.work/` 只留 `tasks/20261030-account-group-tiering/` 与 `reference/` 并更新其 README。
- 修改文件：85 个删除；`.gitignore`、`README_CN.md`、`deploy/README.md`、`docs/README.md`、两份记忆、`replace-controlled-with-share-scheduling` 的 design/proposal（改为指向 Git 历史）、`tools/docs_consistency.json`、`tools/upstream-sync/policy.json`、`tools/tests/test_upstream_sync.py`（断言 `assets/partners` 留在 `required_absent_paths`）。
- 验证：`check_docs_consistency.py` ok；`sync.py scheduler-diff` 与白名单一致；`tools/tests` 96 项与 pnpm 例外检查单测通过；`git diff --check` 通过。`sync.py audit` 在提交后的干净树运行，结果见 PR。未运行 Go/前端门禁：没有改动 Go、前端源码或依赖。
- 卡点/风险：删除的文件仍在 Git 历史中，未改写历史；`.work/` 已删内容不可恢复（用户选择直接删除）。之前给用户的计数“102 个远端分支”多算了 main，实际为 101 个。
- 下一步：PR CI 全绿后常规合并并删除本分支。未结事项见顶部，其中新增两项待用户决定：3 个含未合入提交的远端分支、`tools/gateway-resilience/` 发布演练脚手架；另有 `.github/SECURITY.md` 报告渠道应改为本仓库。


### 2026-11-01 — 20261101-release0416 — 正式发布与记忆收尾

- 请求/目标：用户授权合并分组质量门槛及全部开放 PR 并发布 0.4.16；不部署、不连接生产。
- 完成操作：#175、#176、#177、功能 #178 与版本 #179 均在固定完整 head 的全部 CI 成功后普通 merge；版本合并 `dcd47423e152e5d546aaf55cc5b1318281d3e5e4` 上创建 annotated `v0.4.16`（tag object `9c3d5d6ab0d5628904bc575e0f80723ab2f112cd`）。Release run `41281113095` 成功，正式版非 draft/prerelease。
- 验证：最终集成本地 build/unit vet/缓存与兜底联动、前端 470 文件/3624 项及 typecheck/lint/build、docs/scheduler-diff/干净树 audit 通过；既有基线 Go 67 包、韧性 race 478 项和隔离 Docker 60 包结果完整保留。五平台包与 checksums.txt 的大小、GitHub digest、SHA-256 清单核验成功，所有二进制 embed/revision/clean 一致；GHCR amd64/arm64 的 version/revision/source 与发布提交一致，0.4.16/0.4/0/latest 同指 `sha256:077802908764d509612c42bbf2f792a3d2839960e6d45662ac7cc93e29e58d5d`。
- 修改文件：发布后的收尾仅两份记忆。分组方案、实施/复审/修复轮、缓存 PR 与本次发版的原始交接/登记/日志原样追加归档，既有归档前缀不变；仍未结事项保留顶部；已合并 #173 的工作区精简顶部交接也原样归档。收尾 PR 需全绿后普通合并；最终结果写本地报告，避免递归记忆 PR。
- 卡点/风险：无发布阻塞。升级监控确认、一次性配置、缓存量化、真实上游验证和已接受限制仍保留；生产状态未重新核实。
- 下一步：收尾 PR 合并后删除本次已合并远端分支；本地分支/worktree 留给主持人。标签保持不可变，main 除两份记忆外须与发行源码一致。

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
