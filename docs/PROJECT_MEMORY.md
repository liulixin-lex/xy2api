# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 0.4.16 发布（2026-11-01）及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-11-02 的 0.5.1 发布收尾）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### 版本与环境速览（2026-11-03：0.5.1 已发布）

- 当前产品版本 XY2API v0.5.1（2026-11-03 正式发布），Sub2API 兼容基线 0.2.15，迁移共 333 项（最后为 288，0.5.1 无新迁移）。
- 生产：2026-11-02 05:03 UTC 核实运行 0.5.0（应用于 04:51 UTC 重启后健康），0.5.1 未部署（部署另行授权）；`ops_monitoring_enabled=true`，质量门策略 version 9，五个 OpenAI 门槛组（A–E）均启用质量门槛与利润门。门槛进程内状态已复位；后续仍按新请求评估。外部状态以重新核实为准。
- 预上线主机仍运行 0.4.2 的浸泡候选 `0ad63fab7`，换成正式镜像需要授权。
- 上游：Sub2API v0.2.17 已发布、尚未同步（2026-10-19 定时同步的 prepare 失败），需按 [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) 另开任务。
- 工具链：Go 以 `backend/go.mod` 为准（当前 1.27.3）；golangci-lint v2.14.0（与 CI 同版）。
- 文档一致性：`python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业执行），规则在 `tools/docs_consistency.json`，改动时按 [`AGENTS.md`](../AGENTS.md)“变更同步”同步文档。
- 本地 `.work/` 只保留进行中的任务，已结束任务的过程稿与证据已于 2026-10-31 删除；发布资产以 GitHub Release 为准。

### 版本发布一览

已发布的各版本均为正式 Release（非 draft、非 prerelease），附件与镜像在发布时已核验；发布说明在 tag 正文。完整交接、验证与核验原文见 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（“2026-10-31 记忆重整原样归档”及更早章节）。

| 版本 | 日期 | 发布提交（相关 PR） | 主要内容 | 迁移 |
|---|---|---|---|---|
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

- **分组质量门槛第二版（0.5.1）**：上线 24 小时后按 OpenSpec `group-quality-gate-v2-20261102` design §7 只读复核，需要用户在会话内授权；号池只有 1–2 个号的五个组的可用账号数告警，待用户在管理端配置（组号见本地交接包）。档位记忆与观察状态只在进程内，重启后重新积累。
- **GPT 报错补漏（0.5.1）**：
  - 上线 7 天后按本地复测 SQL 只读复测，需要用户在会话内授权。
  - 管理员处理三件事：为小号池组配兜底分组与告警；联系旧版 LiteLLM 客户端的 Key 用户升级；有 gpt-6.1-sol 工具调用流量的分组配支持 Responses 的号。
  - 残留：Grok 经 Messages 入口的非换号错误，仍会走通用 OpenAI 账号副作用（基线既有）。
  - 本版保持 HTTP/2，HTTP/1.1 维持暂缓。
- **门槛漏记补齐（0.5.1）**：
  - 上线后 24 小时按本地 `.work/tasks/20261102-gate-error-volume/handoff/sql/post_release_verify.sql` 只读验收 V1–V6，需要用户在会话内授权。
  - 上游错误率及其告警会因归因口径上升，平台内部错误相应下降，这不代表质量变化。
  - WebSocket 不补记；拿不到发送边界时保守少记。
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
  - 12:00 UTC：修复与复核完成，本地提交到 `feat/recent-health-v2-20261103`，**未推送**。
    - 修复轮在 测试服务器集成测试中又发现两处，主持人已修：CTE 漏选 `model`；占位行测试的期望写错。
    - 占位行语义：参与相邻判断但不返回。落在其他账号上时打断续轮；落在同一账号上时不打断，但它的提示 Token 为 0，下一行不进缓存样本。
    - 双路红队复核：无阻断，残留 P3 已修。
    - 剩余遗留：英文表头说明在高度约 800 的窗口里，离顶部只有约 5px 余量，彻底解决要让通用 HelpTooltip 支持向下翻转。
    - 上线排在 `fix/post-output-event-attempt-20261103` 之后；推送、PR、发版与部署都需要用户授权。

## 操作陷阱

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
