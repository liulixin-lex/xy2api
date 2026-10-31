# XY2API 仓库记忆与开发日志

> 跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读本文件；执行过程中在关键节点更新；结束前写明结果、验证、卡点和下一步。项目事实变化时同步更新本文件，不能只追加日志而保留过期的顶部状态。
>
> 本文件只放进行中与近期的内容。已结束任务的交接状态、登记，以及 2026-10-25 及以前的操作日志在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)（按归档规范原样移入，最近一次为 2026-10-29）。文档目录见 [`README.md`](README.md)。

## 当前交接状态

### Anthropic 缓存 TTL 实施（2026-10-31；PR #170/#171 已合并，v0.4.15 已发布）

- 按 FINAL_PLAN 完成管理员默认 TTL、API Key 四态、最终出站 wire、usage/计费/预留、前端与文档同步；迁移只追加，旧332项不变并新增286，共333项。
- PR #170 合并提交为 `d579bca2b3498d3fdd35b0e09dad37321558f348`；版本 PR #171 合并提交为 `10fbc9c721cd7b4b11040ade04a59094a32cea90`。`v0.4.15` 正式发布，Release workflow `41175366074` 成功，五平台资产 checksum 已核验；`v0.4.14` 保持不可变。
- 真实供应商缓存命中率/收益未验证，生产未部署；后续部署与上线观察需单独授权。

### Anthropic 缓存 TTL：生产只读分析与方案 v1.0 已交付（2026-10-30；未实施）

- 生产已只读核实0.4.14；48小时11,767条Claude用量全部为APIKey，Token读取占比86.81%。旧1h开关不覆盖主体流量；发现TTL明细丢失、partial漏计及1h预留等接缝，均纳入方案。
- 管理员默认5m/1h（推荐5m），Key四态；当前部署存量off、新建inherit，最终wire统一、usage/计费解耦。gpt-6-astra/xhigh独立反方及定稿复核完成；详见本地`.work/tasks/20261030-anthropic-cache-ttl/FINAL_PLAN.md`，登记原文与结项详情已归档。
- 本轮仅方案及两份记忆，账号分组方案记录已在仓库中；未改业务/生产、推送或部署，SSH已关闭。代码另行安排。

### XY2API 0.4.14 正式发布完成（2026-10-30；未部署）

- [v0.4.14](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.14) 正式发布于 `2026-10-30T07:29:36Z`；PR #168 固定 `584a58c2f` 全部20项检查通过后常规合入 `267f8577f`，annotated tag不可变，Release run `41019263782` 成功。发布前完整查询无其他开放PR。
- 工单、普通管理员工单授权、OpenAI 2FA登录/密钥管理与decode-uri-component 0.5.1安全修复交付；兼容基线0.2.15。原330条SQL/checksum不变，仅追加284/285，总332条。
- 五平台包与checksums.txt经GitHub digest和清单双重SHA验证；全部二进制embed、revision一致、vcs.modified=false。GHCR双架构及0.4.14/0.4/0/latest同指`sha256:5f7ba432bb713fe5b36f24a5ce71fdc94b34674833e4d33849adff4d35acd013`，源码与版本标签一致。
- 正式原生包全新安装、0.4.13→0.4.14→0.4.13→0.4.14、工单生命周期、默认关闭与强制二次验证/Worker鉴权通过，用户与Key数据不变，临时服务已清理；宿主无Docker，镜像只验证manifest/labels，未冒称镜像启动。
- 工单默认关闭，2FA需单独部署Worker及固定加密材料；真实OpenAI网页登录/MFA仅mock，未实际验证。沿用vue-i18n例外至2026-11-27及N5上线校准边界。未连接或部署生产。证据在本地`.work/tasks/20261030-release0414/`；发布后收尾仅记忆，不移动标签。

### 网站工单与 OpenAI 账号 2FA：随 0.4.14 发布（2026-10-30）

- PR #168 已合入并发布，新增迁移顺延为284/285，已发布283保持不变。工单默认关闭；2FA仅超级管理员，需额外部署Worker、固定运行时和加密恢复目录。详见[工单](SUPPORT_TICKETS.md)、[2FA](OPENAI_TOTP.md)及[部署说明](../deploy/OPENAI_TOTP_WORKER.md)。
- 本地集成与完整CI通过，真实OpenAI登录/MFA尚未验证；回退前必须解决不确定任务、保留密钥和恢复日志。详细任务记录原样归档。

### XY2API 0.4.13 正式发布完成（2026-10-29；未部署）

- v0.4.13 于 2026-10-29T17:37:48Z 正式发布，非 draft、非 prerelease；版本 PR #166 固定 head `c179c4347` 的 18 项检查成功后常规合入 `66778769c`，annotated tag 对象 `e6104dae8` 指向该提交。功能 PR #163/#164 与交接 #165 已先合入，发布前完整查询无其他开放 PR。
- Release run `40921036209` 成功。五平台包及 checksums.txt 的大小与 SHA-256 同时匹配 GitHub 元数据和清单；全部二进制为 embed、revision `66778769c`、`vcs.modified=false`。原生程序报告 0.4.13 / 兼容 0.2.15。
- GHCR linux/amd64 与 linux/arm64 的 version/revision/source 一致，`0.4.13`、`0.4`、`0`、`latest` 同指 index `sha256:5f0eaafbfc105fa9a0b9c100fa583c2936f73fa38e7a9030b7509aae42a3d28b`。宿主无 Docker，本次核验镜像 manifest/labels，实际启动验证使用正式原生二进制。
- 唯一临时 PostgreSQL/Redis 完成 0.4.12→0.4.13→0.4.12→0.4.13 与 0.4.13 全新安装：329→330 迁移、ready/health、登录与用户信息、公有版本和 6 个静态资源通过，用户/Key 数据摘要不变，临时资源已清理。旧 329 项 SQL/checksum 不变，仅新增 283；表锁与可保留列回退说明见 [用量指标](USAGE_METRICS.md#迁移与回退)。
- 已知边界：沿用批准的 vue-i18n 安全例外（截至 2026-11-27），N5 阈值须上线后校准，WS 与既有维护事项未扩大范围。没有新增发布阻塞；不声称测试能证明绝对无潜在问题。未连接或部署生产。
- 发布与验证证据在本地 `.work/tasks/20261029-release0413/`。本次收尾只归档记忆，正式标签保持不可变，合并后 main 除两份记忆外须与发行源码一致。

### 补救优化 N1–N5：随 0.4.13 发布（2026-10-29）

- PR #164 已合入并随 v0.4.13 发布：原生 Responses 部分用量与终态关流、迁移 283 观测、E2 窗口内近期成功替补、热大输入 30/40 秒下限。分批审查与最终 CI 已闭合；新增观测集成测试的共享库清理已修复。
- 详细执行原文已归档。N5 上线后校准、WS 阶梯及既有非阻断维护事项仍按原边界后续处理，本轮未部署。

### 普通管理员账号分组修复：随 0.4.13 发布（2026-10-29）

- PR #163 已合入并随 v0.4.13 发布；有账号管理权限即可调整账号分组、复制与影子操作，资金、价格和网络权限边界保持。没有新增迁移或设置；详细交接已归档。

### XY2API 0.4.12 生产上线效果已只读核验（2026-10-29）

- 实际程序为正式版 0.4.12 / `fdedd33dc`，2026-10-28 22:43:45Z 启动，迁移 282 已执行；镜像标签仍为旧版。此次只读观察未部署、未改生产、未发业务测试请求，SSH 已关闭。
- 上线后 235 分钟：Gemini 5 次余额 402、Anthropic 7 次开场后限流均换号并关联成功用量；余额阶梯 5→15→60 分钟有直接日志，上游余额采集与 operator 权限均已有真实使用。
- 首字/缓存尚未证明全面改善：OpenAI 208 个长输入超时补救请求的缓存读取占比 51.04%（其他长输入 79.92%），入站至最终语义首输出估算中位 38.9 秒；另有 43 次真实输出后失败。样本约四小时、负载与请求构成变化明显，不作版本因果归因。
- 本地报告 `.work/tasks/20261029-production-0412-observation/REPORT.md`；模型硬门、WS 及逐字节错误提示缺少现场触发证据，详情见报告。业务代码未改，本轮记忆变更未提交。

### XY2API 0.4.12 正式发布完成（2026-10-28；本轮未部署）

- [v0.4.12](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.12) 于 2026-10-28T22:06:21Z 正式发布：annotated tag 对象 `c36904c45` 指向版本 PR [#161](https://github.com/liulixin-lex/xy2api/pull/161) 的合并提交 `fdedd33dc`；Release run `40775331704` 成功，非 draft、非 prerelease。
- 本版包含上游余额列 [#158](https://github.com/liulixin-lex/xy2api/pull/158)、普通管理员权限 [#159](https://github.com/liulixin-lex/xy2api/pull/159)、报错补救与路由 [#160](https://github.com/liulixin-lex/xy2api/pull/160)，以及 #157 的心跳测试稳定性修复。所有 PR 均在固定 head 的全部检查通过后以 merge commit 合入。
- 五平台包与 checksums.txt 共 201,250,293 字节，下载后与 GitHub digest、checksums 双重核对；五平台均为 embed 构建、`vcs.modified=false`、revision `fdedd33dc`。原生程序实际报告 0.4.12 / 兼容 0.2.15。GHCR 的 `0.4.12`、`0.4`、`0`、`latest` 同指 linux/amd64+linux/arm64 index `sha256:30be3ce4d551d22d96360cef108eed1f7597966cb3ff5a165883801f54d9294a`，版本与源码标签一致。
- 本版有迁移 282；回退须先清除 new-api 访问令牌，并处理 operator 角色、权限表和迁移记录，不能只换旧镜像。见 [管理员权限](ADMIN_PERMISSIONS.md#迁移与回退)、[上游余额](UPSTREAM_BALANCE.md#迁移与回退)。
- WS 余额阶梯与复位仍暂缓，Anthropic 暂存失败尝试的上游成本由网关承担；部署及部署后的 48 小时观测需另行授权。本轮未连接生产。详细任务原文已归档，本地执行证据在 `.work/tasks/20261028-error-recovery-routing/implementation/RELEASE_report.md`。

### 智商检测记录界面精简与上线观察修复：随 0.4.11 发布（2026-10-28；未部署）

- 检测记录分页（每页 10 轮、总数、“仅异常”）与弹窗精简；答对题紧凑诊断；默认全局并发 8；403/429/`rate_limited`/带 `Retry-After` 的未知结果不在轮内重试；不可自动领取的账号显示“暂停：账号不可调度”；排期抖动生效。用户 2026-10-28 授权推送并合并，全部保护检查通过后以合并提交合入。
- [v0.4.11](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.11) 已于 2026-10-28T10:22:18Z 正式发布，发布说明写在 tag 正文。
- 遗留：生产暂不部署；判定规则相关观察项 7 天后复核（本地 `.work/tasks/20261028-iq-records-ui-ops/PLAN.md` §6）；单账号“不降智”分组需运维加第 2 个账号。发版、部署另行授权。
- 详细登记已原样归档。

### 创建密钥弹窗加宽与专属标识：随 0.4.10 发布（2026-10-28 更新）

- 用户端“API 密钥”的创建/编辑弹窗改为 `wide`，分组下拉与输入框同宽（`Select` 可选 `matchTriggerWidth`）。分组说明按用户要求只显示一行，超出省略，悬停弹出完整说明（`GroupOptionItem` 可选 `compactDescription` + `TruncatedTextPopover`）。专属分组在开启“向用户显示专属标识”时，选项、已选值、行内切换浮层都带“专属”标签（`ExclusiveGroupTag`）。只改前端。
- [PR #150](https://github.com/liulixin-lex/xy2api/pull/150)：2026-10-28 用户授权合并。与 #151 在本记忆和 `policy.json` 登记处冲突，两侧都保留后全部保护检查通过，以合并提交 `1ef9213bd` 合入 main，[v0.4.10](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.10) 已于 2026-10-28T04:53:58Z 正式发布；部署未授权。
- 已知：main 上 `resilienceLocale.spec.ts` 有 2 项失败（0.4.9 文案已含 `codex.rate_limits`，旧断言要求不含 `codex.`），不在 CI 关键集里，未修。（更正 2026-10-28：已随 PR #151 的 `9d9594975` 修复。）

### OpenAI 智商检测按模型重构：随 0.4.10 发布（2026-10-28；生产已部署）

- 2026-10-28 只读核实：生产已运行 0.4.10（05:25:56Z 启动，迁移 281 同时执行；部署非本仓库 agent 操作）。上线观察与后续改动见进行中的 `20261028-iq-records-ui-ops`。

- 账号 × 模型检测与 `iq:<出站模型>` fail-closed 门禁，现行说明见 [`OPENAI_IQ_CHECK.md`](OPENAI_IQ_CHECK.md)。用户 2026-10-28 授权推送并合并；PR #151 全部保护检查通过后以 merge commit 合入。同 PR 将 `golang.org/x/net` 升到 v0.62.0（GO-2026-6860，govulncheck 新公告）。
- 遗留：参数化题未做真实上游校准；发版说明须写明默认降级组合新增 6.1-sol、6-sol → 5.6-luna 会改变质量路由，升级须停全部旧副本后执行迁移 281、启动对账失败即不启动；上线观察 7 天后再删旧结构。[v0.4.10](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.10) 已于 2026-10-28T04:53:58Z 正式发布，发布说明（含升级要求）写在 tag 正文；部署未授权。
- 详细交接与实施记录已原样归档。

### 网关韧性复盘：随 0.4.9 发布（2026-10-27 更新）

- **背景**：0.4.9（`ed2e429f6`）提交后，用户要求复盘原有韧性策略，看哪些需要同步调整、哪些多余。Claude 与 Codex 独立分析并互审，astra 两轮找漏洞后定稿为 v3。用户批准，并选定：第二批同进 0.4.9；非 OpenAI 本轮就做模型级 breaker；过渡开关在上线 7 天后再删。
- **结论**：没有需要整块删除的功能。问题在覆盖不均：生产上 Gemini 普通 5xx 从不冷却，一个坏号 27 小时失败 10,886 次；Gemini 也没有尝试截止。另一类是 0.4.9 带来的联动缺口。
- **已完成**（本分支 `ed2e429f6` 之上的提交 `7140cda93`，已推送；与 0.4.9 首字改动一起在 [PR #146](https://github.com/liulixin-lex/xy2api/pull/146)）：
  1. 首包前心跳按尝试恢复：收头只暂挂，写出语义内容、到达终态或请求结束才永久关闭；
  2. planned 截断不计入会话失败；
  3. OpenAI“最后候选”使用本请求的完整调度资格（含 HTTP 续链只认 API Key）；
  4. 无号 Retry-After、失败帧、负缓存共用读取模型 breaker 的恢复投影；不可重试的错误不带 Retry-After；
  5. 判慢迁移前先确认替代号有实时空位；末位兜底排序加入 hold；
  6. 非 OpenAI（不走 OpenAI 网关的平台）的账号×模型 breaker：阶梯 10/45/90/180/300 秒，同轮不升级，成功按请求起点保护；OpenAI 普通阶梯同步改为同一阶梯；带 retry_after 时仍按账号冷却；
  7. Gemini 的 5xx/408 接入通用惩罚；同号重试只在该模型已经失败时跳过；
  8. Gemini 文本尝试截止 60 秒（只截非最后候选，覆盖 6 个完整读取分支），输出后 stall，阶段标签；Antigravity 改读 Anthropic 字段；
  9. 原版文件钩子已登记：scheduler-diff 块 `resilience-review-20261027`、具名补丁 `xy-resilience-coverage`；文档与 zh/en 文案已同步。
- **审查**：astra 审查了 C 路（3 项应改）、D 路（6 项应改）及复核（1 项应改），全部修复；D 路还按总设计师要求收窄了同号重试的跳过范围，并修正了 OpenAI 网关平台的范围回归。
- **验证**（本机，`7140cda93`）：`go build ./...`；四种 tag 的 vet；gofmt；service、handler、resilience、server、repository 全量 unit；`ci_gate.py` 446 项（`-race`、0 跳过）；`sync.py audit` 与 scheduler-diff；文档一致性；tools 单测；diff-check。前端 vue-tsc、i18n eslint 与相关 61 项测试由文档 agent 运行，通过。
- **未完成**：
  1. PR #146 的 CI 全绿（头提交 `60b6fdf87`）：golangci-lint 的两处问题已修复；push 触发的 test 因既有计时测试不稳定，重跑两次后通过；2026-10-27 用户授权“合并并发版”，以合并提交合入 main `c6cfcfe2f`；
  2. [v0.4.9](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.9) 已于 2026-10-27T07:45:20Z 正式发布：版本 PR [#147](https://github.com/liulixin-lex/xy2api/pull/147) 合并提交 `da96d7652`，annotated tag 对象 `8b2dca6ed`；Release run 40467640370 成功。五平台附件与 checksums 一致，二进制报告 0.4.9 / 兼容 0.2.15 / commit da96d7652、vcs.modified=false；GHCR `0.4.9`、`0.4`、`latest` 为同一 amd64+arm64 index，标签 version/revision 一致。无新迁移。生产未部署，部署待授权；
  3. 上线 7 天后复核过渡开关与 fast_fail。
- **卡点与风险**：
  - CI 上偶发失败的既有测试：`TestCompatPreludeCommentHeartbeatStillAllowsFailover/enabled` 和 `TestCompatHeartbeatCommitsOnlyStableStreamHeaders/...messages=enabled`。main 在 2026-10-26（0.4.8 发布时）已失败过一次，本机 `-race` 加满 CPU 负载反复运行均未复现。`ci_gate.py` 只打印汇总、不输出断言，原因未定位；两者的心跳 1 秒与首输出截止 2 秒只差 1 秒，是可疑点；
  - 已管理端保存过设置的部署，Gemini 截止仍为 90 秒（库里存有显式值）；
  - 非 OpenAI breaker 是进程内状态，多实例各自计数；
  - 迁移的空位预检仍有抢槽竞态。
- **关键位置**：本地交接包 `.work/tasks/20261027-resilience-review/`（不入库；2026-10-27 自 `.work/resilience-review/` 迁入），含 `FINAL_PLAN.md`、`IMPLEMENTATION_SPLIT.md`、`reviews/`、`implementation/`（C 路报告与 D 路交接）。D 路 worktree 与分支已删除。

### OpenAI 首字、成功率、缓存与账号路由优化：随 0.4.9 发布（2026-10-27 更新）

- **背景**：生产 48 小时只读分析发现首输出截止 180–220 秒晚于中转约 125 秒的截断、遗留选号不感知延迟且硬粘性、`codex.rate_limits` 提前提交了流、无号 503 不计入 SLA。用户 2026-10-26 批准 6 项改动，要求精简，不做影子、灰度和冗余兜底。
- **已完成**（分支 `feat/openai-ttft-health-routing-20261026`，基于 main `4162a4f03`，已本地提交 `ed2e429f6`）：
  1. 首字截止分热/冷：热 20/25/30/30 秒，冷尝试按请求体 ≥128KiB/≥256KiB 至少 45/75 秒，按已耗时递增，最后候选不截断；
  2. 计划内截止 `planned_first_output_timeout`，豁免账号级惩罚，账号×模型慢失败冷却 1/2/4/8/10 分钟；
  3. `codex.rate_limits` 判为开场事件；
  4. 热且成功的流判慢，hold 账号在候选中排到最后（仅 legacy）；
  5. owner 在 hold 且同组有健康替代时一次性迁移；
  6. 无号 503 计入 SLA。
  同步更新了现行文档、前端文案、校准工具、具名补丁 `xy-openai-ttft-health-routing` 与调度差异白名单。
- **实现与方案的差异**（均经审查确认）：
  - warm 判定改用选号层 SessionSticky（OpenAI 路径不记录 sticky owner）；
  - 判慢样本与 hold 存在独立的 map，不放在 breaker 条目里；
  - 成功清零统一走通用成功回报，从请求状态里取匹配的 attempt 起点；
  - 判慢降序随韧性总开关，会话迁移另外要求 session 开关；
  - “组内无账号支持所请求模型”的 503 同样计入 SLA（消息无法可靠区分容量耗尽与模型不受支持）。
- **验证**（本机）：resilience/service/handler 三包全量 unit 通过；四种 build tag 的 vet、`go build ./...`、`ci_gate.py`（-race、0 跳过）、scheduler-diff、文档一致性、tools 单测通过。前端 lint/typecheck 未在本机运行（无 node_modules，改动只是两条 i18n 文案），由 CI 覆盖；`sync.py audit` 要求干净工作树，提交后运行。
- **未完成**：已随 PR #146 合入并在 v0.4.9 发布；部署需要用户授权，部署后按本地交接包 FINAL_PLAN §8 观测。
- **残留风险**：
  - 官方 OAuth 收头前无心跳；换号后心跳不恢复的问题已由韧性复盘修复（见上一节）；
  - 被截断的尝试可能已被中转计费；负载快照全满时仍排队；
  - Forward 内部的原地重发沿用本次 attempt 的 profile 与截止；
  - 非 load 选号路径在并发迁移时可能重新读到新绑定；
  - 看板 SLA 预计因口径修正下降约 1.5 个点。
- **关键位置**：本地交接包 `.work/tasks/20261026-openai-ttft-health-routing/`（不入库；2026-10-27 自 `.work/` 根目录迁入）：`plan/FINAL_PLAN.md`、`plan/IMPLEMENTATION_SPLIT.md`（两路拆分与接缝）、`integration/`（B 路补丁）。

### XY2API 0.4.8 已发布：缓存精简（2026-10-26）

- [v0.4.7](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.7) 已于 2026-10-25T16:49:36Z 发布（2026-10-26 经 GitHub Release API 只读核实）；#131 的分组弹窗改动保留。
- [PR #142](https://github.com/liulixin-lex/xy2api/pull/142) 于 2026-10-26T05:35:31Z 常规合入 main `1a9d9ac99`，18 项检查通过。回退缓存 PR #132–#140，保留已发布迁移 279 并新增 280 删除其五张表；升级路径与不可恢复的数据删除见 [迁移与回退](openai-cache-reliability.md#迁移与回退)。
- 只保留两项确定性会话键处理，见 [会话键](openai-cache-reliability.md#会话键)：OpenAI 会话标识统一为 UUID（后台开关 `enable_openai_session_uuid`，默认开启）；Anthropic/Gemini 兜底键改为跨轮稳定锚点（无开关）。
- 0.4.7 期间的 31 项缓存任务（M1–M4、P0a/P0b、L-01/L-03/L-04/L-07/L-09a/L-17、PR C/D 组装、管理后台缓存面板等）已终止，登记原文在 [记忆归档](PROJECT_MEMORY_ARCHIVE.md)；GitHub 上 8 个已合并的 feat/cache-* 分支已删除，测试服务器与工作机上的缓存期开发产物先存档后清理。
- [v0.4.8](https://github.com/liulixin-lex/xy2api/releases/tag/v0.4.8) 已于 2026-10-26T07:08:31Z 正式发布：版本 PR [#144](https://github.com/liulixin-lex/xy2api/pull/144) 合并提交 `4e7c9096c`，annotated tag 对象 `6841ae45f`；Release run 40282569807 成功。五平台附件与 checksums 一致，二进制报告 0.4.8 / 兼容 0.2.15 / commit 4e7c9096c、vcs.modified=false；GHCR `0.4.8`、`0.4`、`latest` 为同一 amd64+arm64 index，标签 version/revision 一致。升级时执行 280；之后不能再运行 0.4.7，可以回退到 0.4.6。生产仍为 0.4.6，未部署。

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

### 版本与环境速览（2026-10-31 更新 0.4.15 发布）

- 最新正式版为 XY2API v0.4.15（Sub2API 兼容基线 0.2.15）。本次发布未连接或部署生产，不推断生产当前状态。v0.4.14 及更早版本的发布速览原文已保存在记忆归档。
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

### 2026-10-26 — 20261026-cache-slim — W1b 文档与登记

- 请求/目标：整体回退缓存 PR #132–#140，保留已发布迁移 279，新增迁移 280；补退役登记、升级与回退说明和记忆归档。
- 开始状态：chore/cache-slim 的干净 HEAD 6ca14b4f6；前置回退为 2cf359a99，W1 的 13 项主验证及 R1 代码关已通过。
- 完成操作：从 e369fd3bb 与 a19d7c5b9 提取专属新增名字并登记精确退役规则；原样归档 31 条已终止缓存交接/登记和两条发布准备记录，只追加归档；按 GitHub Release API 更正 0.4.7 发布于 2026-10-25T16:49:36Z。
- 修改文件：tools/docs_consistency.json、docs/openai-cache-reliability.md、本记忆与归档。仅表名规则额外允许现行迁移说明，其余规则只放行必要历史和迁移位置。
- 验证：文档一致性检查通过；工具单测 41 项通过、0 失败；git diff --check 通过。五条规则在 a19d7c5b9 均命中，在 e369fd3bb 均零命中，精简树只在允许位置命中；18 项既有名称保留探针、33 条归档原文及既有归档前缀核验通过，主文件保留超过 15KB 余量。未重复运行不受文档改动影响的 Go 与前端门禁。
- 卡点/风险：迁移 280 删除五表数据，不再支持运行 0.4.7；可以回退到 0.4.6。
- 下一步：W1b 本地提交交付主会话；B1/B2 进行中，PR 未开，由主会话继续集成和最终验证。

### 2026-10-26 — 20261026-cache-slim — B1/B2 集成

- 请求/目标：在回退后的精简分支上只保留两项确定性会话键处理，提高上游缓存的跨轮稳定性。
- 开始状态：chore/cache-slim 8466de91f；B1 wip/b1-session-uuid e70e01f7e、B2 wip/b2-stable-anchor 69fb08c11 均基于 e369fd3bb，分别通过设计关与代码关。
- 完成操作：两次 cherry-pick 依次得到 a289014eb、90e0015b5；记忆冲突取精简分支版本，现行文档占位替换为“会话键”一节。
- 验证：文档一致性检查、upstream-sync audit 与 scheduler-diff 通过；B1 集成后 build、vet、定向 race 与 admin/server/migrations unit 通过。全量门禁由整体复审执行。
- 卡点/风险：B1 开启后上游粘性与缓存分区会重建一次；B2 使不带 metadata 的已有 Anthropic/Gemini 会话一次性重新绑定。
- 下一步：整体复审、推送、开 PR、等待必需检查后合入；不发版。

### 2026-10-26 — 20261026-cache-slim — 合入与收尾

- 请求/目标：缓存精简合入 main，并清理缓存期分支与开发产物。
- 完成操作：PR #142（head `f29712930`）常规合入 main `1a9d9ac99`；GitHub 8 个已合并 feat/cache-* 分支删除；测试服务器与工作机上的缓存期 worktree、分支、构建产物和 staging 资源，按清单先存档（patch、未跟踪文件 tar、bundle、MANIFEST）后删除；未合入分支的提交由汇总 bundle 覆盖并逐项核对。
- 验证：本地全量门禁通过；PR 那一轮 CI 的 `TestCompatHeartbeatCommitsOnlyStableStreamHeaders` 在 race 下偶发失败一次（测试文件与基线相同，push 那一轮同一提交通过，本地 30 次复跑均通过），只重跑失败的 job 后 18 项通过。
- 记忆：主文件中 31 条已终止缓存任务的摘要合并为一条；cache-slim 进行中条目原样移入归档。
- 卡点/风险：无。未发版、未操作生产。

### 2026-10-26 — 20261026-release048 — 0.4.8 版本准备

- 请求/目标：用户授权发布包含 #142、#143 的 0.4.8。
- 修改：程序 VERSION 与 provenance 产品版本 0.4.7→0.4.8，兼容基线 0.2.15 不变；登记本任务。
- 验证与下一步：本地文档、audit、scheduler-diff、工具单测后提交版本 PR；全部保护检查通过后合入并打 annotated tag，触发 Release，核验产物。不操作生产。

### 2026-10-26 — 20261026-release048 — 0.4.8 发布完成

- 完成操作：#144 常规合入 main `4e7c9096c`（18 项检查通过）；在该提交上创建 annotated v0.4.8 并推送，Release run 40282569807 的 verify-version、build-frontend、release 均成功，2026-10-26T07:08:31Z 正式发布（非 draft、非 prerelease）。
- 验证：checksums 核验 darwin_arm64 与 linux_amd64 附件；解包二进制报告 XY2API 0.4.8、兼容 0.2.15、commit 4e7c9096c，构建信息 vcs.modified=false；GHCR 0.4.8/0.4/latest 指向同一 amd64+arm64 index，镜像标签 version=0.4.8、revision=4e7c9096c。
- 记忆：版本准备登记原样归档。未部署生产。

### 2026-10-26T17:13Z — 20261026-openai-ttft-health-routing — 方案获批，交接待实施

- **请求/目标**：只读分析生产 48 小时 OpenAI 慢首字请求；与 Codex 联合审查首字、成功率、缓存、路由；定稿方案。用户批准后，整理工作区、同步 main、交付最终方案，不写代码。
- **开始状态**：main `4162a4f03`。本任务曾在本地分支 `feat/openai-ttft-health-routing-20261026` 上留有未提交的起步改动。
- **完成操作**：
  - 生产只读分析：usage_logs、ops_error_logs、容器日志、Caddy 日志；
  - Codex 三轮评审与外部网关调研；
  - 方案三次修订：判慢只降序不剔除、首字截止分热/冷、撤回全局拥塞阈值；用户批准 6 项；
  - 起步改动导出为补丁存入本地交接包；空分支删除，工作树恢复干净并切回 main，核对与远端一致（`4162a4f03`）；
  - 删除生产分析的本地临时文件（含导出的设置表），关闭到生产的 SSH 连接，清理 8c8g 上的临时脚本。
- **修改文件**：只有本记忆文件，包括任务登记、交接状态与本日志。
- **验证**：
  - 起步补丁在 `4162a4f03` 上 `git apply --check` 通过；
  - 文档一致性检查通过；
  - 补丁此前在分支上通过定向单测与 `go vet -tags unit`，全量单测未跑；本轮无代码改动。
- **卡点/风险**：无阻塞，残留风险见“当前交接状态”。
- **下一步**：实施 agent 按本地交接包实施；提交、PR、部署须用户授权。

### 2026-10-26T19:16Z — 20261026-openai-ttft-health-routing — 实施完成，待授权提交

- **请求/目标**：按本地交接包的已批准方案实施 6 项改动；用户要求首字、成功率、缓存、路由四方面平衡，精简，不做无用测试与兜底。
- **开始状态**：main `4162a4f03`；新建分支 `feat/openai-ttft-health-routing-20261026`。
- **分工**：总设计师（Claude）制定两路拆分与唯一接缝并集成；A 路（Codex gpt-6.1-sol）做起步补丁与改动 1、2、3、6，B 路（同模型）做改动 4、5；两名 astra 审查员先审拆分接缝（5 项全部采纳），再分别审两路代码（B 路采纳 2 项、A 路采纳 3 项，其余记为不改的理由或残留）；文档与前端文案由 Claude 完成。
- **修改文件**：resilience（档位、递增、开场判定、attempt profile）；service（热/冷截止、计划内截止与慢失败冷却、判慢 hold 与分区、会话迁移、成功回报）；handler（三个 HTTP handler 写 attempt profile、SLA 分类）；`docs/GATEWAY_RESILIENCE.md`、`docs/account-scheduling.md`、`docs/USAGE_METRICS.md`；zh/en 韧性文案；`tools/gateway-resilience` 的校准 SQL 与 README；具名补丁登记（`policy.json`、`UPSTREAM_BASE.json`）与 `scheduler-diff.json`；本记忆。
- **验证**：三包全量 unit；四种 build tag 的 vet；`go build ./...`；`ci_gate.py`（-race、0 跳过）；scheduler-diff；文档一致性；tools 单测。基线 `4162a4f03` 上 handler、service 全量 unit 同样通过，作为对照。前端 lint/typecheck 本机未运行（无依赖）。
- **卡点/风险**：无阻塞；残留风险见“当前交接状态”。
- **下一步**：提交、推送、PR、合并、发版、部署均待用户授权。

### 2026-10-27 — 20261027-resilience-review — 网关韧性复盘：方案定稿、实施、验证，待授权提交

- **请求/目标**：用户要求先提交 0.4.9（已提交为 `ed2e429f6`），再与 Codex 一起复盘原有“网关韧性”：需不需要同步调整，有无多余、臃肿、无用的功能，给出最终方案。用户批准方案 v3 后要求实施，并选定第二批同进 0.4.9、本轮就做非 OpenAI 模型级 breaker。
- **开始状态**：分支 `feat/openai-ttft-health-routing-20261026`，HEAD `ed2e429f6`，工作树只有本记忆文件的改动。
- **完成操作**：
  - **生产只读取数**：48 小时流量构成、7 天未送达、7 天 Warn 事件、Gemini 与 Anthropic 的时延，以及问题账号的配置形态。数据库会话为只读；未导出整张设置表；本地临时文件已删除。
  - **方案**：Claude 与 Codex 各自独立分析，再互审 8 个分歧；总设计师合并成稿，astra 找漏洞两轮，定稿为 v3。被纠正或撤回的立场：
    - 迁移“延后删绑定”（v2 同号 `SET NX` 证伪）；
    - 账号级冷却升级（会连带伤到同账号的健康模型）；
    - M8 卡片误导（标签“失败分类修正”是准确的）。
  - **实施**：C 路（0.4.9 联动 7 项）在主检出；D 路（Gemini 5xx、模型级 breaker、Gemini 截止）在 worktree，后经三方合并集成。文档 agent 同步了 `docs/GATEWAY_RESILIENCE.md`、`docs/account-scheduling.md` 与 zh/en 的 `budgetHint`。
  - **审查**：astra 审 C 路 3 项、D 路 6 项、复核 1 项，均为“应改”，全部修复。
  - **登记**：scheduler-diff 新增块 `resilience-review-20261027`，覆盖 `gateway_scheduling.go`、`openai_account_scheduler.go`、`gemini_messages_compat_service.go`；具名补丁 `xy-resilience-coverage`（policy 与 `UPSTREAM_BASE.json`）含 21 个补丁测试、8 条必需字面量。
- **修改文件**：
  - backend 的 resilience、service、handler：约 40 个文件，新增 `resilience_selection_eligibility.go`、`resilience_model_breaker.go`、`resilience_gemini_attempt.go` 及两个测试文件；
  - `tools/upstream-sync/policy.json`、`tools/upstream-sync/scheduler-diff.json`、`UPSTREAM_BASE.json`；
  - 上述文档与 i18n；
  - 本记忆文件。
- **验证**（最终工作树）：
  - `go build ./...`；四种 tag 的 vet；gofmt；
  - service、handler、resilience、server、repository 全量 unit；
  - `ci_gate.py`：446 项 `-race`，0 跳过；
  - scheduler-diff、文档一致性、tools 单测 41 项、diff-check；
  - 前端：vue-tsc、i18n eslint、相关 61 项测试。
  - 未运行：golangci-lint（本机未安装，由 CI 运行）；`sync.py audit`（要求干净工作树，提交后运行）。
- **卡点/风险**：无阻塞。残留风险见“当前交接状态”的同名小节。
- **下一步**：等待用户授权提交，之后运行 `sync.py audit`；推送、PR、合并、发版、部署各自待授权；上线 7 天后复核过渡开关与 fast_fail。

### 2026-10-27 — 20261027-resilience-review — 提交、推送并开 PR #146

- **请求/目标**：用户授权提交、推送并向 main 开 PR（不合并），提交后删除 D 路 worktree。
- **完成操作**：
  - 提交 `7140cda93`（作者为仓库 noreply 身份，通过 `-c` 传入），位于 `ed2e429f6` 之上；
  - `sync.py audit` 首次报告补丁测试名已改，修正 policy 登记后 amend 进同一个提交，复跑通过；
  - 推送分支 `feat/openai-ttft-health-routing-20261026`，开 [PR #146](https://github.com/liulixin-lex/xy2api/pull/146)，包含两个提交；
  - 删除 D 路 worktree 与分支 `wip/resil-gemini-20261027`，交接记录复制到本地交接包 `d-route/`。
- **修改文件**：本记忆文件（随本条另行提交）。
- **验证**：`sync.py audit` 通过；工作树干净。
- **下一步**：等 CI；合并、发版、部署各自待授权。

### 2026-10-27 — 20261027-resilience-review — 修复 PR #146 的 golangci-lint

- **请求/目标**：PR #146 的 CI 只有 golangci-lint 失败，其余 test、upstream-sync-audit、前端与安全扫描均通过。
- **完成操作**：补检 `resilience_gemini_attempt.go` 里 `bytes.Buffer.WriteString` 的返回值（errcheck）；删除已无调用方的 `clearOpenAIAccountModelTransientState`（unused）。
- **验证**：本机按 CI 锁定的 golangci-lint v2.14.0 全量运行，0 issues；`go build ./...`、service unit vet、gofmt、文档一致性通过。
- **下一步**：等 CI 复跑；合并、发版、部署各自待授权。

### 2026-10-27 — 20261027-release049 — 0.4.9 版本准备

- 请求/目标：用户授权“合并并发版”：合入 PR #146 并发布 0.4.9。
- 完成：#146 以合并提交合入 main `c6cfcfe2f`。
- 修改：程序 VERSION 与 provenance 产品版本 0.4.8→0.4.9，兼容基线 0.2.15 不变；登记本任务。
- 验证与下一步：本地文档、audit、scheduler-diff、工具单测后提交版本 PR；全部保护检查通过后合入并打 annotated tag，触发 Release，核验产物。不操作生产。

### 2026-10-27 — 20261027-release049 — 0.4.9 发布完成

- 完成操作：#147 常规合入 main `da96d7652`（全部检查通过）；在该提交上创建 annotated v0.4.9 并推送，Release run 40467640370 的 verify-version、build-frontend、release 均成功，2026-10-27T07:45:20Z 正式发布（非 draft、非 prerelease）。
- 验证：checksums 核验 darwin_arm64 与 linux_amd64 附件；解包二进制报告 XY2API 0.4.9、兼容 0.2.15、commit da96d7652，构建信息 vcs.modified=false；GHCR 0.4.9/0.4/latest 指向同一 amd64+arm64 index，镜像标签 version=0.4.9、revision=da96d7652。
- 记忆：版本准备登记原样归档。未部署生产。

### 2026-10-27 — 20261027-docs-drop-unreleased-049 — 去掉 0.4.9“未发布”标注

- 请求/目标：v0.4.9 已发布，用户要求更新现行文档中仍写着“0.4.9（未发布）”的标注。
- 修改：`docs/GATEWAY_RESILIENCE.md`、`docs/account-scheduling.md`、`docs/USAGE_METRICS.md` 只改版本标注，行为描述不变；韧性文档引言改为按 0.4.9 的行为描述。
- 验证：文档一致性通过；全仓库再无 0.4.8/0.4.9 的“未发布”残留。未部署生产。

### 2026-10-27T14:30Z — 20261027-key-dialog-exclusive — PR #150 已开

- 请求/目标：创建密钥弹窗加宽，分组下拉同宽、长说明换行；按“向用户显示专属标识”在选分组时显示专属标签。
- 开始状态：main `d25bcd8dc`，工作树 `xy2api-wt/key-dialog`。
- 完成操作：`Select` 加 `matchTriggerWidth`，`GroupOptionItem` 加 `exclusive`（默认关，管理端不变），KeysView 弹窗改 `wide` 并接入两处；policy 登记，文档补充。
- 修改文件：`Select.vue`、`GroupOptionItem.vue`、`KeysView.vue` 及三个 spec、`policy.json`、`GROUP_SYSTEM_PROMPTS.md`。
- 验证：lint、typecheck、i18n、build 通过；相关 spec 41 项通过；全量 vitest 3288/3290（2 项为 main 既有的 `resilienceLocale.spec.ts`）；`sync.py audit`、scheduler-diff、文档一致性、tools 41 项通过；本机无头 Chrome 实测通过。
- 卡点/风险：无。
- 下一步：等 CI 与合并；发版、部署另行授权。

### 2026-10-27T16:30Z — 20261027-key-dialog-exclusive — PR #150 审查修复与一行说明

- 请求/目标：两轮多维对抗审查（Claude workflow）的修复；用户追加“说明省略、悬停弹出完整内容、只留一行”。
- 完成操作：`width="wide"` 等钩子补登记；已选值带专属标签；KeysView 的 Select/DataTable 桩渲染插槽，绑定可被测试捕获；新增 `TruncatedTextPopover`（只在被截断时弹出，挂 body、层级高于下拉、不拦截点击，按空间大的一侧放置并限高，滚动/移开即关）；弹窗说明一行省略。中途的“完整换行”方案（`676a497df`）已按用户反馈撤回。
- 验证：lint、typecheck、i18n、build 通过；CI 关键集 57 文件 732 项通过；全量 vitest 只剩 main 既有的 `resilienceLocale.spec.ts` 2 项；绑定逐个删除的变异检查均被测试捕获；本机无头 Chrome 实测通过；文档一致性、scheduler-diff、tools 41 项通过。
- 下一步：推送后等 CI，停在 PR。

### 2026-10-27 — 20261027-iq-per-model — 只读分析与方案定稿，待批准

- **请求/目标**：见“进行中的工作”同名条目。
- **开始状态**：main `d25bcd8dc`，工作树干净，与远端一致。
- **完成操作**：
  - 生产只读：数据库会话只读，查询检测配置、48 小时结果与小时指标、业务流量模型分布、表写入统计；未导出设置表；连接已关闭，本机临时脚本已删除。
  - Claude 与 Codex 各自独立分析，互审三轮收口；判定规则用蒙特卡洛模拟比较三种方案；参数化题的闭式解经全枚举验证。
- **修改文件**：只有本记忆文件（任务登记与本条）。
- **验证**：文档一致性检查。
- **卡点/风险**：方案待用户批准；参数化题未用真实上游校准。
- **下一步**：等待用户对 `FINAL_PLAN.md` §10 五个决策点的答复。

### 2026-10-27 — 20261027-iq-per-model — 工作区整理，方案定稿 v1.1（实施交其他 agent）

- **请求/目标**：用户明确代码由另行安排的 agent 编写；本任务整理工作区、同步最新 main，并把方案、调研背景、目标与注意事项定为最终版。
- **开始状态**：main `d25bcd8dc`，与 origin 一致（重新 fetch 核实）；工作树只有本记忆文件的任务登记。
- **完成操作**：
  - 本地 `.work/` 重组为 `tasks/<日期>-<任务>/`（各带 README）与 `reference/`，顶层 `README.md` 为索引；旧路径在各任务 README 有对照。
  - IQ 方案定稿：Codex 第四轮只读审查 v1.0（P0 5、P1 12、P2 1），关键论据经代码核实后全部采纳，形成 v1.1；另写背景文档（缺陷→方案对应、双方取舍、实施注意事项）。参数化题闭式解与暴力枚举在全部 46,656 组参数上一致。
  - 记忆维护：已结束的 `20261027-resilience-review`、`20261026-openai-ttft-health-routing` 进行中条目原样移入归档文件；“当前交接状态”中的本地交接包路径改为新布局。
- **修改文件**：`docs/PROJECT_MEMORY.md`、`docs/PROJECT_MEMORY_ARCHIVE.md`（未提交）。
- **验证**：文档一致性检查通过。
- **卡点/风险**：方案 §17 的 5 个决策点待用户确认；参数化题未用真实上游校准。
- **下一步**：用户确认决策点后，由实施 agent 按方案 §15 分 A/B/C 三路实施并在本条目登记接手；提交本记忆改动需用户授权。

### 2026-10-27 — 20261027-iq-per-model — 方案 v1.2：每题题面显示与自报模型识别

- **请求/目标**：用户要求把“每题显示题面、正确答案与推导”加入方案；并依据实际请求、其他网关实现与联网检索，完善上游自报模型与本站请求模型的识别逻辑，确保识别准确。
- **开始状态**：main `d25bcd8dc`，与 origin 一致；工作树只有本记忆文件与归档文件的未提交改动。
- **完成操作**：
  - 生产只读：30 天自报模型统计（按发出值与声明值组合汇总）、OAuth 与 API Key 的声明情况、反向代理日志中的模型响应头、应用的质量路由配置（窄范围检查，值打码）；未导出设置表；连接已关闭，本机临时脚本已删除。
  - Codex 第五轮做自报模型识别专项源码核实；Claude 核实官方 Codex 客户端的响应头读取，并检索模型替换审计的研究与测量。
  - 方案改为 v1.2：新增 §4.5（来源与提取、规范化、分类、降级规则、多声明合并、三处使用、记录与显示），并同步题目存储、判题表、接口、弹窗、改动清单、测试、文档登记、拆分与验收。识别原型的生产回放与第五轮 25 条向量断言全部通过。
- **修改文件**：`docs/PROJECT_MEMORY.md`（未提交）；本地交接包不入库。
- **验证**：文档一致性检查通过。
- **卡点/风险**：方案 §17 的 5 个决策点待用户确认；参数化题未用真实上游校准；业务转发路径仍不读模型响应头（另开任务）。
- **下一步**：同上一条；实施 agent 以交接包内的 `FINAL_PLAN.md` §4.5 与识别原型为准。

### 2026-10-27 — 20261027-iq-integfix — PostgreSQL 集成回归修复完成

- **请求/目标**：在独立 worktree 修复真实 PostgreSQL repository 集成套件发现的 12 项 IQ 按模型改造回归，按 FINAL_PLAN v1.2 与 ROUTES 契约裁定代码和测试责任；全部验证通过后提交并写仓库外报告。
- **开始状态**：分支 `wip/iq-integfix-20261027`，HEAD `3ae964a7e27bfe3f74a89e83e1b79c810bb5b8cc`，工作树干净。
- **完成操作**：修复 `Create` 事务上下文、清限流后空对象、BulkUpdate 锁查询参数类型/编号、NULL diagnostic 读取；身份重置断言改读持久化快照。按契约修正四处既有用例：增加第二个自动目标、让投影输入有效改变 reset 时间、断言规范出站 gate、并发 test 留言数匹配目标数。上游登记字面量原样保留，未改两个 scheduler 原版文件。
- **修改文件**：`backend/internal/repository/account_repo.go`、`account_iq_targets.go`、`account_repo_iq_targets_integration_test.go` 与本记忆。
- **验证**：8c8g 真实 PostgreSQL `go test -tags=integration -count=1 -timeout 24m ./internal/repository/` 通过（0 失败）；本机 `go build ./...`、default/unit/integration/e2e 四组 vet、`go test -count=1 -tags=unit ./internal/repository/... ./internal/service/...`、gofmt、文档一致性、diff-check 均通过。
- **提交**：实现与测试 `72310c050d5036d7dc1b23930b285192b3354aeb`（`fix(iq): repair PostgreSQL integration regressions`）；本记忆收尾随下一条纯记忆提交。
- **卡点/风险**：无。未推送、未开 PR、未连接生产。
- **下一步**：无。详细逐项根因、代码/测试裁决和测试用例名记于本机 `.work/tasks/20261027-iq-per-model/implementation/INTEGFIX_REPORT.md`。

### 2026-10-27T23:53Z — 20261027-iq-r4fix — 修复、验证并本地提交

- 请求/目标：修复 astra R4 的 3 个 P1 与 1 个 P2；每项一个最小单测，准确执行 compact 协议分流，不加剧业务请求中断。
- 开始状态：分支 `wip/iq-r4fix-20261027`，HEAD `9b3597b7e0859f2e92e783236a34279e7f326065`，工作树干净。
- 完成操作：
  - fresh last-resort 复核改为读取 fresh 账号后重新计算 compact 出站模型；Codex ticket 出站预测仅在实际 compact Responses 分支应用 fallback，raw Chat 沿用普通映射。
  - Retry-After 或普通题间等待超出 `lease_until - 10s` 安全窗口时不继续等待，立即以具体 unknown 原因 Complete，并保留汇总 Retry-After。
  - 出站快照返回 false 或 error 时，以独立 3 秒 context 按旧 token Defer，revision 不符时由既有仓储逻辑清旧租约并保留新排期。
  - 更新 `docs/OPENAI_IQ_CHECK.md` 的 compact 门禁说明；没有修改 repository、调度白名单文件或 policy。
- 修改文件：`backend/internal/service/iq_check_service.go`、`iq_check_service_test.go`、`iq_check_gate_test.go`、`resilience_last_resort_iq_gate_test.go`、`resilience_session.go`、`share_scheduling_gateway.go`、`share_scheduling_openai.go`、`openai_codex_ticket.go`、`docs/OPENAI_IQ_CHECK.md`、本记忆文件。
- 单测：`TestOpenAILastResortFreshCompactGateUsesFreshMapping`、`TestRawChatCompactRequestUsesOrdinaryOutboundForIQGateAndTicket`、`TestIQExecutionCompletesBeforeLeaseWhenRetryAfterExceedsBudget`、`TestIQExecutionReleasesLeaseWhenOutboundSnapshotFails`，通过。
- 验证：`go build ./...`；default/unit/integration/e2e 四组 `go vet`；`gofmt -l` 空；完整 `go test -count=1 -tags=unit ./internal/service/... ./internal/pkg/iqcheck/... ./internal/pkg/openai/... ./internal/handler/...` 最终通过；`ci_gate.py` 451 项 `-race`、0 跳过；`sync.py scheduler-diff`、`sync.py audit`、文档一致性、`git diff --check` 通过。`ci_gate.py` 初次因 `go` 不在 PATH 未启动，加入指定 Go 目录后通过。第一次完整 unit 曾返回一次失败，但输出因测试日志量过大被截断；service 单包复跑与第二次完整命令都通过，未定位首轮具体断言。
- 提交：`41b5f9f5c`（`fix(iq): repair R4 model gates and lease completion`）。未推送、未开 PR、未连接服务器。
- 卡点/风险：最终完整 unit 与韧性 race 门禁全过；首轮全量 unit 的失败断言未能从被截断输出恢复，已由单包和完整复跑验证通过。
- 下一步：无。

### 2026-10-28 — 20261027-iq-per-model — 集成收尾，本地完成待授权推送

- 请求/目标：按交接包方案 v1.2 完成 OpenAI 智商检测按模型重构；总设计师编排 Codex（后端）、Claude（前端）、astra（审查）。
- 开始状态：分支 `feat/iq-per-model-20261027`，HEAD `3ae964a7e`（R3 修复已合入），前端、文档与登记未提交。
- 完成操作：
  - 提交前端、文档、上游同步登记；R3 修复改了统一资格函数签名，scheduler-diff 与 policy 字面量随之更新。
  - 开发机集成测试长时间无输出，定位为 Docker IPv4/IPv6 端口不同导致连错容器，见“操作陷阱”。之后对比 main 基线，确认 12 个失败均为本分支回归，派 integfix 修复（4 处代码缺陷；4 处测试期望与方案不符，按方案修正，未放宽）。
  - astra R4 复核 R3 修复：八项关闭、两项部分关闭，另报 3 个 P1 + 1 个 P2（fresh 复核沿用旧 compact 出站值、raw Chat 被 compact 兜底误挡、长 Retry-After 等到租约过期、快照失败遗留租约），派 r4fix 修复。
  - 两个分支合入，补新延后原因的中英文案；`OPENAI_IQ_CHECK.md` 补 compact 兜底、raw Chat 与启动对账失败的说明；操作日志 2026-10-20 至 10-21 原样归档。
- 修改文件：`frontend/`（IQ 管理界面）、`docs/`、`openspec/` 取代说明、`deploy/config.example.yaml`、`tools/docs_consistency.json`、`tools/upstream-sync/{policy,scheduler-diff}.json`；后端修复见各分支提交。
- 验证：开发机 repository 集成套件 0 失败；本机 build、四组 vet、gofmt、全量 unit、`ci_gate.py`、scheduler-diff、policy 字面量、文档一致性、tools 41 项、前端 lint/typecheck/3261 项测试/build 全部通过。`sync.py audit` 需干净工作树，提交本记忆后运行。
- 卡点/风险：未做真实上游校准；默认降级组合新增两条会改变生产质量路由。未推送、未开 PR、未部署，生产只读。
- 下一步：用户授权后推送并开 PR；合并、发版、部署另行授权。

### 2026-10-28 — 20261027-iq-per-model — 推送、开 PR #151 并合并

- 请求/目标：用户 2026-10-28 授权“全部推送并合并”（不含发版、部署）。
- 开始状态：分支 `feat/iq-per-model-20261027`，HEAD `eb75150e5`，origin/main 仍为 `d25bcd8dc`，工作树干净。
- 完成操作：推送分支并开 PR #151。首轮 backend-security 因 govulncheck 新公告 GO-2026-6860（`golang.org/x/net/html` 文本节点合并的二次复杂度，经 Ollama Cloud 用量解析可达）失败，与本改动无关、main 同样会失败；按惯例把 `golang.org/x/net` 升到修复版 v0.62.0，其余 `golang.org/x` 模块随之小幅升级（`39947b7ce`）。本机 build、vet、govulncheck（0 个可达漏洞）、定向测试、文档一致性与 `sync.py audit` 通过。任务记录原样归档。随后 golangci-lint 报 62 项（errcheck 52：`rows.Close`/`tx.Rollback`/Builder 写入未处理返回值；staticcheck 7；unused 3：遗留的 `iqGateKeyPresent` 与两个质量路由正则），均来自本改动、本机门禁漏跑；按仓库写法修正并删除死代码，本机 golangci-lint 0 项，开发机集成套件与受影响 unit 复跑通过。
- 修改文件：`backend/go.mod`、`backend/go.sum`；lint 涉及 `repository/account_iq_targets.go` 等 IQ 新文件；本记忆与归档。
- 验证：PR #151 全部保护检查通过后合并。
- 卡点/风险：见“当前交接状态”的遗留项。
- 下一步：发版与部署待用户授权。

### 2026-10-28 — 20261028-release0410 — 合并其余 PR 并准备 0.4.10

- 请求/目标：用户 2026-10-28 要求“将其它的 PR 也一同合并，然后发版 0.4.10”。
- 开始状态：main `468e6a59c`（#151 已合入）；唯一的开放 PR 为 #150，其分支与 main 冲突。
- 完成操作：在独立 worktree 把 main 合进 #150 的分支。冲突只在本记忆与 `policy.json` 的 required_literals，两侧都保留；resilience-review 与 ttft 两条进行中条目已在 main 归档，按 main 删除。为 #150 的“已知失败 spec”追加更正（已由 `9d9594975` 修复）。全部保护检查通过后以合并提交合入 main `1ef9213bd`。版本分支把 VERSION 与 UPSTREAM_BASE 产品版本从 0.4.9 升到 0.4.10。
- 修改文件：`backend/cmd/server/VERSION`、`UPSTREAM_BASE.json`、本记忆。
- 验证：文档一致性、`sync.py audit`、scheduler-diff、tools 单测通过后提交版本 PR。
- 卡点/风险：本机磁盘一度被 Go 构建缓存（约 96G）占满，`go clean -cache` 后恢复。
- 下一步：版本 PR 合入后打 annotated tag v0.4.10，触发 Release，核验产物。不部署生产。

### 2026-10-28 — 20261028-release0410 — 0.4.10 发布完成

- 完成操作：版本 PR #152 全部检查通过后以合并提交合入 main `6042b7b68`；在该提交上创建 annotated v0.4.10（tag 对象 `922929557`，正文为更新内容与升级要求，用 `--cleanup=whitespace` 保留 `##` 标题行）并推送。Release run 40633537613 的 verify-version、build-frontend、release 均成功，2026-10-28T04:53:58Z 正式发布（非 draft、非 prerelease），Release 页面正文即 tag 正文。
- 验证：checksums 核验 darwin_arm64 与 linux_amd64 附件；解包二进制报告 XY2API 0.4.10、兼容 0.2.15、commit 6042b7b68，构建信息 vcs.modified=false；GHCR 0.4.10/0.4/latest 指向同一 index（amd64+arm64），镜像标签 version=0.4.10、revision=6042b7b68（开发机 Docker 无 buildx，改用 GHCR registry API 匿名只读核验）。
- 记忆：#150 与版本准备登记原样归档；版本速览更新为 0.4.10。未部署生产。

### 2026-10-28 — 20261028-iq-records-ui-ops — 上线观察与方案草案

- 请求/目标：见“进行中的工作”同名条目。
- 开始状态：main `478f1b335`，与 origin 一致，工作树干净。
- 完成操作：生产只读（数据库只读会话，未导出设置表）：核实生产版本与迁移；统计上线后的轮次、题目、吞吐、门禁一致性、分组无号日志与错误日志。阅读弹窗与结果接口代码，确认列表累加机制。写方案草案并请 Codex 复核。
- 修改文件：本记忆（任务登记、顶部状态更正为“生产已部署”与本条）。
- 验证：文档一致性检查。
- 卡点/风险：观察只有 1 小时，判定规则相关的现象只列为观察项。
- 下一步：合入复核意见，交用户确认。

### 2026-10-28 — 20261028-iq-records-ui-ops — 方案定稿 v1.1（实施交其他 agent）

- 请求/目标：用户确认并发默认改 8、生产暂不部署；合入 Codex 复核后定稿。
- 开始状态：main `478f1b335`，工作树只有本记忆的未提交改动。
- 完成操作：Codex 第六轮复核意见逐条对照代码核实后全部采纳；补排期抖动修复；生产只读连接已关闭，本机临时脚本已删除。
- 修改文件：本记忆（未提交）；本地交接包不入库。
- 验证：文档一致性检查。
- 卡点/风险：判定规则相关现象只观察了 1 小时，列为 7 天后复核项。
- 下一步：由实施 agent 接手并在本条目登记。

### 2026-10-28 — 20261028-iq-records-ui-ops — 实施只读审查（astra）

- 请求/目标：按 PLAN v1.1、ROUTES.md 与实施报告只读审查 `git diff 478f1b335 9660fcacf`，检查分页/异常定义、紧凑诊断、轮内重试、暂停推导、抖动、前端轮询与文档一致性，并把结果写入实现目录。
- 完成操作：审查只读 worktree `9660fcac` 的后端、前端和 `docs/OPENAI_IQ_CHECK.md`；报告写入 `.work/tasks/20261028-iq-records-ui-ops/implementation/review_impl_astra.md`，列出 3 个 P2（原始错误码、答案着色、`total=0` 页码回退）。未修改被审 worktree，未连接服务器。
- 验证：`GOTOOLCHAIN=local ~/sdk/go1.27.3/bin/go test -count=1 ./internal/pkg/iqcheck ./internal/service ./internal/handler/admin` 通过；前端 Vitest 定向 `IQCheckResultsModal.spec.ts`、`iqCheckPresentation.spec.ts`、`admin.accounts.iqCheck.spec.ts`，21 项通过。
- 未完成/下一步：实施 agent 需修复并复核 3 个 P2；集成、全量门禁、推送、PR、合并、发版和部署按原授权边界执行。

### 2026-10-28 — 20261028-iq-records-ui-ops — 实施完成，本地提交待授权推送

- 请求/目标：用户要求按更新后的方案 v1.1 完成实施。
- 开始状态：main `478f1b335`，工作树只有规划者对本记忆的未提交登记（已保留并随本分支提交）。
- 完成操作：总设计师先定接口契约（本地 `implementation/ROUTES.md`），后端（Codex，worktree）与前端（Claude Opus，主检出只改 `frontend/`）并行实施；astra 审查实施，3 个 P2 由前端修复；总设计师同步 `OPENAI_IQ_CHECK.md` 与退役登记。`before_id`/`next_before_id` 的退役规则用 `paths` 限定在 IQ 文件，因为运维告警接口仍用 `before_id`；已用临时命中验证规则生效。
- 修改文件：`backend/`（记录分页、紧凑诊断、默认并发、轮内重试、暂停推导、抖动及定向测试）、`frontend/`（记录弹窗、题目与技术详情、i18n 删减与新增文案、对应 spec）、`docs/OPENAI_IQ_CHECK.md`、`tools/docs_consistency.json`、本记忆。
- 验证：后端 agent 在开发机跑 repository 集成套件 0 失败、ci_gate 451 项 `-race` 0 跳过、golangci-lint 0 项、四组 vet、unit；集成后本机复跑 build、vet、gofmt、golangci-lint、定向 unit、前端 lint/typecheck/全量测试/build、文档一致性、scheduler-diff、tools 单测；干净工作树 `sync.py audit`。
- 卡点/风险：前端只列表滚动依赖 `:has()`，不支持的浏览器回到整个弹窗滚动；“失败位置”遇到没有文案的阶段或原因时仍显示原值（目前后端发出的值都有文案）。
- 下一步：用户授权后推送并开 PR；合并、发版、部署另行授权。

### 2026-10-28 — 20261028-iq-records-ui-ops — 推送、开 PR #154 并合并

- 请求/目标：用户 2026-10-28 授权“推送、合并”（不含发版、部署）。
- 开始状态：分支 `feat/iq-records-ui-ops-20261028`，HEAD `77aa5214b`，origin/main 仍为 `478f1b335`，工作树干净。
- 完成操作：推送并开 PR #154；本条目与任务登记原样归档，顶部只留结果摘要。
- 验证：PR #154 全部保护检查通过后以合并提交合入。
- 卡点/风险：见顶部遗留项。
- 下一步：发版、部署待用户授权。

### 2026-10-28 — 20261028-release0411 — 0.4.11 版本准备

- 请求/目标：用户 2026-10-28 要求“现在发版”。
- 开始状态：main `6596bafb9`，与 origin 一致，无开放 PR，工作树干净。
- 修改：程序 VERSION 与 provenance 产品版本 0.4.10→0.4.11，兼容基线 0.2.15 不变；登记本任务。
- 验证与下一步：本地文档、audit、scheduler-diff、工具单测后提交版本 PR；全部保护检查通过后合入并打 annotated tag，触发 Release，核验产物。不操作生产。

### 2026-10-28 — 20261028-release0411 — 0.4.11 发布完成

- 完成操作：版本 PR #155 首轮 `test` 撞上已知的心跳计时不稳定测试（`TestCompatHeartbeatCommitsOnlyStableStreamHeaders`，版本 PR 只改版本号），只重跑失败 job 后全部检查通过，以合并提交合入 main `368958301`；在该提交上创建 annotated v0.4.11（正文为更新内容与升级说明）并推送。Release run 40670083466 的 verify-version、build-frontend、release 均成功，2026-10-28T10:22:18Z 正式发布（非 draft、非 prerelease）。
- 验证：checksums 核验 darwin_arm64 与 linux_amd64；二进制报告 XY2API 0.4.11、兼容 0.2.15、commit 368958301，vcs.modified=false；GHCR 0.4.11/0.4/latest 指向同一 index（amd64+arm64），镜像标签 version=0.4.11、revision=368958301（GHCR registry API 匿名只读核验）。
- 记忆：版本准备登记原样归档；版本速览更新为 0.4.11。未部署生产。

### 2026-10-28 — 20261028-heartbeat-test-timing — 修复心跳计时不稳定测试

- 请求：用户反馈 v0.4.11 tag 触发的 CI（run 40670083446）`test` 失败，要求尽快检查并修复。
- 根因：`TestStagedHeartbeatFailureOutcomeMatchesCompatPath` 在 2.00 s 报首输出超时。keepalive ticker 在 opening 事件更新 `lastDataAt` 之前创建，第一拍可能被跳过，心跳落到第二拍（2 s），与 2 s 首输出截止赛跑。同族测试（#154、#155 上的失败）也是这个原因。生产代码行为正确，不改。
- 修改：6 个心跳测试的首输出截止时间 2 s→3 s，完成帧 2.2 s→3.2 s（`gateway_convert_client_gone_parity_test.go`、`resilience_compat_stream_prelude_test.go`、`resilience_heartbeat_headers_test.go`、`openai_passthrough_first_output_test.go`），测试语义不变；操作陷阱补一条。
- 验证：`-race -tags=unit` 编译的测试二进制，8 个进程并发、每个跑 5 轮，共 40 轮全部通过。
- 其他：定时 Upstream Sync（run 40667898182）失败，原因是 prepare 遇到清单外冲突被阻塞，与本次修复无关，属于另开的上游同步任务（见版本速览中的 v0.2.17 项）。未部署生产。

### 2026-10-28 — 20261027-upstream-balance — 开发与首轮审查完成，rebase 到 0.4.10 的 main

- 请求/目标：按已批准的 DEV_PLAN 先做 PR-B（上游余额列），再做 PR-A（普通管理员）。
- 开始状态：分支 `feat/upstream-balance-20261027` 基于 `d25bcd8dc`；开发期间 main 前进到 `478f1b335`（#150–#153，0.4.10）。
- 完成操作：后端分块开发（125 项契约条目）；gpt-6-astra 与 Claude 对抗式核实两路全量审查，确认的问题已修：采集前缺上游地址策略检查、失败计数不清零且退避溢出、已识别 sub2api 被一次 404 判为不支持、`/api/status` 识别绕过站点缓存、singleflight 内 panic 逃出 recover、单账号更新漏校验用户 ID、代理变化不清令牌、前端两个监听器互相取消请求、`updated_at` 被当作身份变化。rebase 时去掉本分支早先的记忆归档提交，改在 main 的记忆上重新登记；0.4.7 进行中条目原样归档。
- 修改文件：`backend/`（`upstream_balance*`、凭据钩子、CN 中转跳过、wire）、`frontend/`（余额列、令牌小节）、`docs/UPSTREAM_BALANCE.md`、OpenSpec `upstream-balance-20261027`、`policy.json`、`UPSTREAM_BASE.json`、`Makefile`、本记忆与归档。
- 验证：rebase 前本机 build、`make generate` 幂等、四种 vet、gofmt、相关包 unit 与 `-race`、golangci-lint、`ci_gate.py`（446 项）、`make test-frontend`、文档一致性、`sync.py audit` 通过；rebase 后的全量门禁在第二轮审查后重跑。
- 卡点/风险：无。传输层既有 Debug 日志会带出代理地址，属后续事项。
- 下一步：第二轮审查、全量门禁、浏览器截图，推送并开 PR。

### 2026-10-28 — 20261027-upstream-balance — 审查完成，准备推送

- 请求/目标：完成 PR-B 的审查循环与门禁，推送并开 PR。
- 开始状态：分支 `feat/upstream-balance-20261027` 基于 main `478f1b335`；main 之后又合入 #154。
- 完成操作：第二、三轮审查（gpt-6-astra 加 Claude 对抗式核实）后修复 P2：账号预算超时误判为 Redis 故障、预算补写覆盖已保存结果、billing 成功后鉴权计数不清零、已识别 sub2api 在其他 4xx 时被删除类型、识别阶段 `/api/status` 4xx 分类、IPv6 站点末尾多冒号、一处跨秒偶发失败的测试断言；补前端身份变化后只请求一次的 spec。第三轮 astra 结论 READY。两项 P2 列为后续：传输层既有 Debug 日志；前端无法察觉其他管理员只轮换 Key 或令牌。
- 修改文件：`backend/internal/service/upstream_balance*`、`account_credentials_newapi_token*`，前端 spec，OpenSpec tasks，本记忆。
- 验证：build、`make generate` 无差异、四种 vet、gofmt、全量 unit、相关包 `-race`、golangci-lint 0 项、`ci_gate.py`（451 项 `-race`，0 跳过）、`make test-frontend`、账号相关前端 1201 项、前端 build、文档一致性、`sync.py audit`；本机端到端用最终代码重跑，12 个账号状态符合预期。rebase 到最新 main 后重跑全量门禁。
- 卡点/风险：无。
- 下一步：rebase、推送、开 PR，CI 全绿后停止。

### 2026-10-28 — 20261027-upstream-balance — 推送并开 PR #158，CI 全绿

- 请求/目标：推送 PR-B 并开 PR，CI 全绿后停止。
- 开始状态：分支 `feat/upstream-balance-20261027`；main 在推送前后依次合入 #155（0.4.11）、#156、#157。
- 完成操作：rebase 到最新 main（只在本记忆冲突，按脚本重新登记），推送并开 [PR #158](https://github.com/liulixin-lex/xy2api/pull/158)。首轮 CI 全绿后 main 合入 #157 导致记忆冲突，再次 rebase 并 `--force-with-lease` 推送，第二轮 CI 全绿，GitHub 显示可合并。
- 修改文件：本记忆。
- 验证：rebase 后代码与本机全量门禁通过的版本逐字节一致（只差 main 自身的改动）；CI 全部检查通过。
- 卡点/风险：无。
- 下一步：等待合并；不合并、不打 tag、不发版、不部署。

### 2026-10-28 — 20261028-error-recovery-routing — 生产只读分析与方案定稿 v1.0，待确认

- 请求/目标：只读分析生产近 48 小时报错与补救/路由；与 Codex astra（xhigh）联合审查报错补救模块；参考外部网关；给出精简最佳方案。
- 开始状态：main `d57883bec`，工作树干净；生产运行 0.4.11。
- 完成操作：
  - 生产只读：`ops_error_logs`、`usage_logs`、容器日志（约 3.7 小时）；数据库会话只读，未导出设置表；本地临时日志已删除，SSH 连接已关闭。
  - 外部网关与 Codex / Claude Code 客户端重试规则调研（源码依据）。
  - Claude 独立草案 → astra r1 独立审查 → 合并 v0.9 → astra r2 对抗审查 → 定稿 v1.0。r2 否决“模型不支持复用瞬时阶梯”（改用现有 30 分钟账号×模型不可用），补 service 层原地重试、余额阶梯原子性与成功栅栏、可恢复 429 必带 Retry-After 等边界。
- 修改文件：只有本记忆文件（任务登记与本日志）；方案与证据在本地交接包。
- 验证：文档一致性检查通过；本轮无代码改动。
- 卡点/风险：历史 Gemini 余额号未暂停多半是 0.4.8 漏接（0.4.9 已修），部署版本与日志不可得，属推断；240s 计时来源未闭环。
- 下一步：用户确认 FINAL_PLAN §3 决策点后实施；提交、PR、发版、部署各自待授权。

### 2026-10-28 — 20261028-error-recovery-routing — 实施、审查与集成完成，交 astra 发布

- 请求/目标：用户授权实施 FINAL_PLAN v1.0，Claude 作产品经理编排：后端 gpt-6.1-sol、前端与审查 gpt-6-astra，均为 xhigh。完成后授权 astra 推送、开 PR、合并全部开启的 PR 并发布 0.4.12，不部署。
- 开始状态：main `d57883bec`；集成分支 `feat/error-recovery-routing-20261028`；实施工作树 `../xy2api-wt/err-{r1,r2,r3,fe,fix}`。
- 完成操作：
  - 实施：R1 `a85205475`、R2 `3e6d37f3b`+`9562a71c2`、R3 `5dc07a78a`+`9cf601d49`、FE `deb270292`+`957138e18`。每路先经 astra 审查，修复后再合入。只有 `gateway_forward.go` 有一处 import 冲突。
  - R1 审查：3 项 P1 与 5 项 P2，由新的 sol 实施 FIX `15209e662`，内容包括转换入口、成功路径预筛、脱敏漏口、数值 code、旧 404 分类、开关边界、Gemini 503 与并发提示、图片尝试时间、Bedrock。WS 余额阶梯按约定暂缓。
  - 集成终审：剩 M1（只发过暂存心跳后耗尽时追加裸 JSON）与 M2（硬类原地重试旁路总开关），由 `3f4147212` 修复，合并提交 `66765e6dd`。
  - 文档与登记 `c8d1f7059` 及后续修正：现行文档、runbook、policy（替换 5 条失效 literal，新增钩子，具名补丁 `xy-error-recovery-routing` 绑定 23 个测试）、`UPSTREAM_BASE.json`、scheduler-diff 审阅块 `error-recovery-routing-20261028`。
- 修改文件：后端 resilience、service、handler、repository；前端韧性设置文案；`docs/GATEWAY_RESILIENCE.md`、`docs/account-scheduling.md`、runbook、`tools/upstream-sync/{policy,scheduler-diff}.json`、`UPSTREAM_BASE.json`、本文件。无数据库迁移，无新开关。
- 验证：
  - FIX 门禁：build、四种 tag 的 vet、service/handler/resilience/repository 全量 unit、`ci_gate.py` 474 项 `-race` 零跳过、golangci-lint 0 issues。
  - 余额阶梯的 PostgreSQL 行锁并发测试：在本机临时 PostgreSQL 18 上用 overlay 测试桩执行，30 次通过，测后清理。
  - `sync.py audit`、scheduler-diff、文档一致性、tools 41 项通过。
  - 前端 lint、typecheck、vitest 由 FE 执行并通过。
- 卡点/风险：WS 余额阶梯暂缓；计费残留是 Anthropic 暂存期失败尝试的上游成本由网关承担（已写入 §7）。
- 下一步：astra 执行 `RELEASE_BRIEF.md`；完成后归档本任务。

### 2026-10-28 — 20261028-release0412 — 终审修复验收与发布接手

- 请求/目标：用户授权推送本任务、依次合并 #158/#159/本任务、发布 0.4.12 与记忆归档，不部署。
- 开始状态：`feat/error-recovery-routing-20261028`，`c4ec8cd7b`，工作树干净；远端开放 PR 仍为 #158、#159。
- 完成操作：核对 M1/M2/F2 的最小修改与真实入口测试、D1–D4 文档修正；快速验收通过。已核实 #159 含迁移 282，与交接包的无迁移描述不符，发布说明按代码及现行文档更正，并保留 #158 的回退前令牌清除条件。
- 修改文件：仅本记忆登记；产品代码未改。详细执行记录在本地 RELEASE_report。
- 验证：三个定向 Go 测试通过；发布所需本地文档、调度登记与工具门禁继续执行，干净提交上运行 audit。
- 下一步：按固定 head 和全绿 CI 依次常规合并，再执行版本 PR、annotated tag/Release 与归档 PR；发现业务冲突或非偶发 CI 失败即停止并记录。

### 2026-10-28 — 20261028-release0412 — 合入 #158、#159，本任务跟进 main

- 完成操作：#158 以固定 head 常规合入 `39ef32f51`；#159 先普通 merge main，产品 tree 与原 head 完全相同，本地文档/audit/scheduler-diff/工具 41 项通过后普通推送，全部检查成功后合入 `5b06d6ef4`。#160 已开，初轮 18 项检查通过，随后普通 merge 最新 main。
- 冲突：仅 provenance、policy 和记忆。补丁登记按并集合并；保留已替换的旧字面量删除，不恢复失效保护；两侧任务与操作日志完整保留，已归档的 0.4.7 条目不重新引入。业务代码未手改。
- CI：#159 两次 shell 作业均未分配到 macOS runner、无执行步骤，平台 annotation 明确容量不足；只重跑该作业，第二次实际重跑在 19 秒内通过，最终 18/18 SUCCESS。没有绕过检查或改 runner 配置。
- 下一步：#160 新合并提交的本地门禁与完整 CI，通过后合并；版本 PR 与发布尚未执行，不操作生产。

### 2026-10-28 — 20261028-release0412 — 功能 PR 全部合入，准备版本 PR

- 完成操作：#160 跟进 main 的 `88e85638c` 全部 18 项检查成功，无需重跑；按固定 head 常规合入 main `62c2fe72c`。合并树与已验证 head 完全相同，开放 PR 已清空。
- 修改文件：仅 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 的产品版本和本记忆。正式版本为 0.4.12，Sub2API 兼容基线保持 0.2.15。
- 验证与下一步：本地文档、audit、scheduler-diff、工具单测通过后推送版本 PR；完整检查成功后合并、打 annotated tag 并验证发布制品。部署不在授权内。

### 2026-10-28 — 20261028-release0412 — 0.4.12 发布完成与记忆归档

- 请求/目标：按用户授权完成快速验收、全部开放功能 PR 合并、0.4.12 发布与记忆收尾，不部署。
- 完成操作：M1/M2/F2 与 D1–D4 验收通过；#158→`39ef32f51`、#159→`5b06d6ef4`、#160→`62c2fe72c`、版本 #161→`fdedd33dc` 均固定 head 常规合并。冲突只有登记文件，双方有效登记及历史并集保留；没有业务代码手改、force push、rebase 他人分支或 admin 绕过。
- 发布：annotated v0.4.12 对象 `c36904c45e0af145e2e26a82bbb2d3538bd0d763` 指向 `fdedd33dc93966656ab9fed96327427437e38b05`；Release run `40775331704` 成功，于 2026-10-28T22:06:21Z 正式发布。正文据实更正交接包的无迁移/直接回退假设，列明迁移 282、权限和令牌清理条件。
- 验证：三个真实入口定向测试通过；各阶段文档、scheduler-diff、工具 41 项与干净树 audit 通过；四个 PR 的最终固定 head 均 18/18 SUCCESS。#159 的 macOS runner 两次未分配，只有该作业进行了两次实际重跑，最终 19 秒通过。PG 行锁并发 30 次是 PM 提供的既有验证，本轮未重复执行。
- 制品：6 个资产、201,250,293 字节双 SHA 一致；五平台源码 revision 与版本构建一致，均 `vcs.modified=false`；darwin_arm64 实际版本命令通过；GHCR 双架构、版本/源码/source 标签与全部稳定别名核验通过。未执行部署、安装演练或任何生产操作。
- 记忆：本次将报错补救、上游余额、普通管理员和版本任务的交接/登记，以及普通管理员旧收尾日志和发布前版本速览原样追加归档；保留其他进行中任务与原有历史。正式标签不移动，发布后收尾只改记忆文件。
- 后续：部署及生产观察另行授权；WS 余额阶梯等残留见现行文档与顶部摘要。Release 的旧 Node.js Action 弃用提示属后续维护项，不影响本次成功发布。

### 2026-10-29 — 20261029-recovery-optimization-next — N1–N5 与审查修复本地交付完成

- 请求/目标：按 FINAL_PLAN v1.1 实施 0.4.12 后补救与路由优化，逐项提交并按产品经理转达的审查修复，完成最终固定代码门禁；仅本地工作。
- 开始状态：`feat/recovery-optimization-20261029`，HEAD/origin/main 为 `03fc67de3`，工作树干净。
- 完成操作：N1→N5 独立提交 `47d950eb5`、`18265f80c`、`9cb7708d8`、`c0d81eb46`、`b1e43534b`；追加 M1/M2 `7d8599e43`、N3 精确分类 `438588d04`、N4 端点模型 `873c36972`、跨入口拒绝 `85c1255f9`、普通 Responses 协议键 `fe7ca1b02`。未改写已审提交；所有审查 P1 已独立复核闭合。最终收尾只改本记忆。
- 修改文件：backend 的原生/透传响应流、调度与成功登记、handler 观测、usage/ops 模型与 SQL、迁移 283/校验和及 Ent 生成代码；三份现行文档、policy、UPSTREAM_BASE 与 scheduler-diff。go.sum 无增量；文档未放宽目标契约。
- 验证：最终代码 `fe7ca1b02` 串行 gofmt、build、mod verify、四组 vet、service/handler/resilience/repository 8 包 unit、ci_gate 475 项 race 零跳过、golangci-lint 0 issues、文档/调度差异/tools 41 项、diff-check 与干净树 audit 全通过；各批真实 Forward/handler/RecordUsage/E2 窄 race 通过；临时 PostgreSQL 18 实测迁移幂等、usage 四路径与 ops 单批 NULL/值往返。测试服务、数据及本任务停止后残留构建目录均已清理。final4 磁盘满中断，不算最终验证；清理未用的备用编译缓存后 final5 全套通过。
- 卡点/风险：无实施阻塞；额外宽筛选发现未改内容审计测试的 TTL 竞态，报告单独留证。N5 常量需部署后校准，LRU 重启丢失符合设计。未做生产或前端操作。
- 下一步：产品经理最终验收，合入前再查迁移编号；推送、PR、发布和部署均需后续授权。报告在仓库外任务包 implementation/REPORT.md。

### 2026-10-29 — 20261029-recovery-optimization-next — 产品经理验收与文档收尾

- 请求/开始状态：产品经理确认全分支终审可合入、无 P0/P1，要求仅文档收尾；本分支 HEAD `ff31aaed1`，工作树干净。
- 完成操作：USAGE_METRICS 删除最后一次尝试的“成功”限定，说明输出后失败保留的部分用量从所属尝试计时，统计口径不变；更新本任务验收与迁移编号核实结果。PR #163 也改记忆，合入时保留双方记录并解决冲突。按要求保留三处成功模型计算的既有代码与 literal。
- 验证与边界：仅改两份文档，提交前运行文档一致性与 diff-check，提交后在干净树跑 audit；结果追加仓库外 implementation/REPORT.md。不推送。
- 下一步：推送、PR、发布、部署与上线后的 N5 校准均待另行授权。
### 2026-10-28 — 20261028-operator-account-groups — 修复并开 PR #163

- 请求/目标：0.4.12 发布后用户反馈，普通管理员有“账号管理”管理权限却不能调整账号分组。用户选定：有账号管理（管理）就能改分组，不加开关。
- 开始状态：自 origin/main `03fc67de3` 新建 `fix/operator-account-groups-20261028`（工作树 `xy2api-wt/operator-account-groups`），工作树干净。
- 完成操作：原因是 0.4.12 契约把账号放组归到 `users.balance_adjust`。账号新建、编辑、批量编辑放开 `group_ids` 并恢复平台默认分组绑定；duplicate、shadow 去掉余额开关（目录与快照同步）；分组的 `copy_accounts_from_group_ids` 改为加减余额或账号管理任一。计费 extra、分组价格、渠道绑定等不变。前端选择器、分组弹窗、复制与影子操作、复制账号选择器、加减余额说明同步；`ADMIN_PERMISSIONS.md` 与 OpenSpec 记录已接受风险。编码由 gpt-6.1-sol 完成，gpt-6-astra 只读审查 READY。
- 修改文件：`operator_account_rules.go`、`operator_group_rules.go`、`operatoraccess/catalog.go` 与 `testdata/pra_catalog.json`、相关 Go 测试；三个账号弹窗、`operatorAccountRules.ts`、`AccountActionMenu.vue`、`AccountsView.vue`、`GroupsView.vue`、zh/en `operatorAccess.ts` 及规格；`ADMIN_PERMISSIONS.md`、OpenSpec `operator-role-20261027`；本记忆。
- 验证：build、gofmt、四种 vet、相关包 unit 与 `-race`、golangci-lint 0 issues；前端 lint、typecheck、相关 878 项、`make test-frontend` 917 项；文档一致性、`git diff --check`、干净树 `sync.py audit` 通过。本机浏览器以只有账号管理（管理）、分组管理（查看）的普通管理员改分组成功（`[2]`→`[3]`），复制账号继承分组。
- 卡点/风险：无。已接受风险：账号管理者可把高价账号放进便宜分组，效果类似降价。
- 下一步：等 PR #163 CI 全绿后停止；合并、发版、部署待用户授权。

### 2026-10-29 — 20261028-operator-account-groups — 修复首轮 CI 失败

- 完成操作：`TestEveryRuleHasAllowDenyAndBoundaryCases/account_create/deny` 仍用 `group_ids` 作拒绝样例，改为无加减余额时设置计费 extra（`cache_ttl_override_enabled`）。之前本机按 `-run` 筛选未覆盖该用例，本次 service 全包 unit 不筛选运行。vue-i18n 新公告 GHSA-83rm-6c8r-g728 使 `frontend-security` 失败（main 同样受影响）；用户选定在本 PR 加 30 天豁免。核实结果：未配置 `escapeParameter`；有两处 `v-html` 使用翻译文本（`CustomPageView` 的静态“页面不存在”、`UseKeyModal` 经 `escapeHtml` 的注释），均不含用户可控参数，豁免理由据实写明。
- 验证：`go test -tags=unit ./internal/service/...` 全包、server/operatoraccess/handler unit、vet、gofmt、service 包 golangci-lint 通过；按 CI 参数复现 pnpm audit 后例外检查通过，相关工具单测 13 项与 tools 41 项、文档一致性通过。
- 下一步：推送后等 CI 全绿即停止；vue-i18n 11 迁移在 2026-11-27 前另开任务。

### 2026-10-29 — 20261029-production-0412-observation — 上线约四小时效果只读核验完成

- 请求/目标：用户授权 SSH 只读核验本次上线功能是否生效、实际效果如何；不改生产、不发业务测试请求。
- 开始状态：main `03fc67de3`，工作树干净；先登记本任务，随后核对生产实际程序，而非按容器标签推断版本。
- 完成操作：核实正式版 0.4.12 / `fdedd33dc`、启动时间与迁移 282；对比上线前后各 235 分钟及前一天同一时段，读取数据库只读事务、应用日志与限定前缀 Redis，保留脱敏聚合。连接已关闭。
- 结论：Gemini 同一余额号上线前 34 个直接失败请求，上线后 5 次均换号并关联成功用量；Anthropic 7 次开场后限流均补救成功；余额阶梯 5/15/60 分钟、余额采集与 operator 使用有直接证据。OpenAI 首字与缓存未证明整体改善；208 个长输入超时补救请求缓存读取占比 51.04%、入站至首输出估算中位 38.9 秒，43 次输出后失败仍影响连续性。HTTP 200 不当作成功：Gemini 4 条、OpenAI 1 条零输出记录已关联 header_wait 未送达。
- 修改文件：仅本记忆及归档；业务代码、生产配置和数据未改。报告与证据在本地 `.work/tasks/20261029-production-0412-observation/REPORT.md`，不提交生产原始数据。进行中登记原样移入归档。
- 验证：数据库 default_transaction_read_only=on、BEGIN READ ONLY、单条 20 秒超时；跨表关联成功用量、未送达与应用日志，避免把心跳 200/部分用量误算完整成功。文档一致性、diff-check、报告证据链接与 JSON 解析检查均通过。
- 限制/下一步：四小时样本且流量/上下文/账号状态变化，不能归因净收益；unknown-provider/模型硬门、WS 和部分响应头/失败帧无现场触发证据。后续优先观察长上下文补救时延与缓存、主要账号的输出后失败；本轮不修改策略、不提交或推送。


### 2026-10-29 — 20261029-recovery-optimization-next — 最小优化方案讨论 r1 完成

- 请求/范围：与总设计师再讨论上线后优化，逐项核实热首字截止、近期成功替补、输出后失败与客户端终态、持久观测、count_tokens、Gemini 零输出、WS 残留；仅允许生产只读聚合与本地报告。
- 起点与保留：main `03fc67de3`，保留前轮生产观察已存在的记忆/归档改动及其他任务。数据库只读事务、20 秒语句超时，未查询设置、凭据、请求内容或发业务探测。生产连接已退出，无活动控制 socket。
- 发现/取舍：热尝试输入下限缺失属代码事实，但历史只能用同会话上一轮同号代理 Warm；建议初始 30/40 秒下限并持久记录真实 Warm/body 字段。近期成功替补限定 E2 原负载窗；端到端 TTFT 不等于最终 attempt TTFT，建议复用原落库、一次可空列迁移。失败率处罚、通用 404 负能力与 WS 阶梯暂缓。
- 新增缺口：原生 Responses 外层错误返回丢掉流层部分用量；bare error 后即使完整权威 failed 已送达仍等待上游 EOF。用仓库外 Go overlay 在当前代码复现，原生/直通用量对照与不关闭上游的终态反例共 3 个子例通过；这是现状缺陷断言，不是修复已完成。
- 证据更正：前轮称 count_tokens 为已知能力缺口过强，本轮只确认单账号通用 404；Gemini 四次零输出的最终原因是客户端侧取消，尝试截止已换号且未耗尽总预算。不能将心跳 200 或部分用量视作完整成功。
- 修改与验证：仅本记忆及归档，讨论稿、分桶表与脱敏聚合在本地任务目录；未改业务代码/跟踪测试、未提交推送或部署。文档一致性与 diff-check 通过，报告链接及聚合 JSON 另核验；原进行中登记原样追加归档。
- 限制/下一步：约四小时样本不能证明版本净收益或精确校准 byte 阈值，未做真实客户端复验。交总设计师融合最小清单，再由新执行者实施；本轮无待执行生产操作。


### 2026-10-29 — 20261029-recovery-optimization-next-r2 — 融合前四项补证完成

- 请求/范围：只读核对字节/token阈值、输出后429副作用与重置提示、60分钟会话峰值、三列可空观测迁移；简短结论已写本地 discussion/astra_r2.md。
- 事实更正：生产确认request_body_bytes不存在，迁移136已删除，因此旧234条及其它ops样本不能给出bytes/token分位数。按已有估算暂采256/512KiB的30/40秒下限，明确不是实测校准。
- 429闭环：原生与直通都已调用统一限流；15次流内rate_limit_exceeded均在同账号事件10.43毫秒内匹配45秒冷却日志，无写失败日志。冷却日志缺请求ID，关联口径为同账号近时，不冒称强关联。已落库体没有数值reset，暂不新加入口或处罚。
- 容量/迁移：48小时内滚动60分钟会话峰值3076，附模型维度3089；建议固定16384条LRU。当前最高迁移282，下一个283；三列无默认可空DDL不重写表但需表锁，Ent与手写usage批量/单条SQL接缝已列清。
- 验证/边界：生产数据库会话只读、20秒超时，仅导出聚合，未查设置/凭据/请求内容，SSH主动关闭；未改业务代码/跟踪测试、未提交推送部署。报告链接/聚合JSON、文档一致性与diff-check完成后收尾，未运行无关全量测试。
- 下一步：交总设计师融合；两项Responses必修不变，本轮无需重复接入流内429惩罚。原进行中登记原样归档，保留其他记忆改动。


### 2026-10-29 — 20261029-recovery-optimization-next-plan-check — 融合稿快速核对完成

- 请求/范围：快速核对FINAL_PLAN v1.0是否准确承接r1/r2与现有代码，未重新做分析、未连接生产。
- 结果：本地 discussion/astra_plan_check.md仅列4条需改：超时profile不能支撑正常成功按真实Warm/BodyBytes验收；LRU写入的实际出站模型与E2请求模型须统一查询契约；历史缓存/热代理/部分用量/45秒冷却证据需保留原范围；干净树audit应在提交后。
- 修改/验证：仅报告与记忆/归档，保留既有未提交改动；不改方案或业务、不提交推送。文档一致性、diff-check完成后收尾；本次无产品修改，不重跑产品测试。
- 下一步：总设计师修订并交新执行者；本审查者按逐批交付继续审代码。原进行中登记原样归档。

### 2026-10-29 — 20261029-recovery-optimization-next-batch1-review — 首批审查完成，N1两项P1待修

- 请求/范围：只审N1 `47d950eb5` 与N2 `18265f80c`，固定origin/main `03fc67de3`到后者，不读实施树后继代码；真实Forward/handler/计账边界与文档policy。
- 结果：本地 reviews/batch1_review.md已交付。N2通过；N1的语义标记在实际flush前置真，零字节失败写会放行部分入账；部分计账的NoteAccountSuccess在客户端取消、drain不完整时可清掉旧余额阶梯。额外反例在真实handler、Forward→RecordUsage分别复现；较新余额暂停仍有CAS保护，不夸大结论。
- 验证：固定提交service/handler定向unit通过；sync audit、文档一致性与diff-check通过。额外Go overlay两项按预期修复行为断言均失败，证据留本地报告目录；未重跑全量或race，报告区分实施者既有验证。
- 修改/边界：仅本地报告、补充反例及记忆/归档，未改业务或跟踪测试；固定提交测试副本已正常移除，不改实施者工作树、不连接生产、不提交推送。既有未提交记忆/归档完整保留，原进行中登记原样归档。
- 下一步：总设计师转交M1/M2最小修法；实施者修复后回归真实链路，N3及后续各批另行审查。


### 2026-10-29 — 20261029-recovery-optimization-next-batch2-review — 第二批审查完成，N3两组P1待修

- 请求/范围：固定18265f80c到7d8599e43，复核第一批M1/M2及N3迁移、观测、文档登记；未审实施树后续N4/N5。
- 结果：本地reviews/batch2_review.md已交付。M1/M2原反例与真实链路修复测试通过；N3补充四个函数五个场景在预期断言失败，明确error:null漏记/空数组误记，以及确定性拒绝漏分类/显式客户端断开被覆盖。列两组P1最小修法及SSE重复解析可简化项。
- 验证：定向unit（service25.225s、handler0.690s、repository0.052s）、SQL形状补测0.049s通过；283旧checksum零变更且TrimSpace摘要正确；Ent重新生成零代码差异，生成器go.sum增量仅在副本恢复；固定树audit、文档一致性、范围diff-check通过。实库仅核对交付测试与已有通过日志，未冒称独立复跑；未做全量或重复race。
- 修改/边界：仅本地报告/overlay证据与记忆归档，保留其他既有未提交改动；固定副本已正常移除，不改实施者工作树、业务或跟踪测试，不连接生产、不提交推送。第一批旧交接与本次登记原样归档。
- 下一步：负责人转交N3-1/N3-2修法，修复后仅复核对应窄反例；第一批M1/M2不重开设计，后续各批另审。

### 2026-10-29 — 20261029-recovery-optimization-next-batch3-review — 第三批审查完成，N4一项P1待修

- 请求/范围：固定审查7d8599e43到b1e43534b的N4/N5，不含实施者后续N3修复；核对模型键、完成登记、E2顺序和约束、LRU、Warm及首输出预算、文档登记。
- 结果：本地reviews/batch3_review.md已交付。N5通过；N4发现一项P1：Chat/Messages经API Key透传账号实际执行正常account mapping，但E2查询按Responses透传规则返回原别名，未命中成功写入的实际出站模型。Responses映射对照正常；Chat与Messages真实Forward overlay分别复现排序仍选HRW账号。列出按端点解析模型的最小改法及三入口窄测试。其余N4边界与排序保持方案。
- 验证：N4/N5定向 service unit 通过（0.146s），筛选下handler无测试；额外 Chat/Messages 两例在预期排序断言失败。固定树 sync audit、scheduler-diff、文档一致性和范围diff-check通过。无全量build/vet/unit/race/lint。
- 修改/边界：仅本地报告、overlay证据及本记忆归档；固定副本正常清理，保留原有记忆改动；未改业务代码、实施树或跟踪测试，未连接生产、未提交推送。
- 下一步：实施者修正N4候选模型键并复核三入口；N5不重开。此前N3两项P1仍待修后另行复核。

### 2026-10-29 — 20261029-recovery-optimization-next-fix-reviews — N3/N4修复复核完成，各余一项P1

- 请求/顺序：先固定复核b1e43534b→438588d04，再复核438588d04→873c36972；分别追加batch2_review与batch3_review，不读取移动实施HEAD。主检出仍为main 03fc67de3，既有记忆/归档改动保留。
- N3：原四个TestReviewRecovery函数五个场景通过；新增真实handler六例中native Responses两对照正确，透传Responses/Chat的确定性400与cyber拒绝四例仍记upstream_failure。N3-2-R1要求在明确拒绝的非failover出口补同一typed标记，保持原响应和行为；Messages/Grok共用出口由源码确认。C1解析抽取安全；C2仍余两处重复赋值，非阻塞。
- N4：真实Chat/Messages Forward→登记→E2反例已通过，映射变更旧历史失效，Responses结果UpstreamModel仍为空但实际出站模型可登记。普通文本Responses的最低能力是Chat，新比较器用它判端点，读取gpt-5.4而非实际alias，窄反例仍选HRW账号；N4-1-R1要求改用明确入站协议，不能提升能力门而剔除合法Chat fallback。
- 验证：N3原反例service0.050s/handler0.049s、既有定向service0.231s/handler6.084s通过；新增跨入口四例失败。N4组合service0.222s仅新增responses_text反例失败，其余定向均通过，handler0.066s通过。两个固定树audit、文档和diff-check通过，N4 scheduler-diff通过；本次未运行全量或race，不将失败反例说成验收通过。
- 修改/收尾：仅本地审查报告/overlay证据和记忆/归档；两个固定副本已正常移除，未改业务、跟踪测试或实施者工作树，未连接生产、提交、推送。旧交接与本轮两条登记原样归档，保留其他事项；记忆文档一致性与diff-check通过后统一通知。
- 下一步：实施者分别修N3-2-R1与N4-1-R1，仅复核对应窄用例；已闭合N3-1、显式断开、原Chat/Messages和写端契约不重开。

### 2026-10-29 — 20261029-recovery-optimization-next-final-review — 两项二次复核与全分支终审通过

- 顺序与范围：先复核873c36972→85c1255f9、再复核85c1255f9→fe7ca1b02，最后审03fc67de3→fe7ca1b02十提交的N1–N5整体接缝。主检出仍为main 03fc67de3，保留其他既有记忆改动；不读取移动实施HEAD。
- 结果：N3六例拒绝覆盖与C2闭合；N4四入口、Responses普通文本、Chat fallback及空UpstreamModel契约闭合。分别追加batch2/batch3二次复核，总报告FINAL_review明确可合入、无新增P0/P1。部分失败记账不进入成功复位/outcome/近期成功；终态关流、模型键、预算及候选边界一致。
- 定向证据：N3 handler6.087s；N4 service0.175s/handler0.236s；最终原batch1/batch2反例及组合回归service0.404s/handler0.934s；bare终态与SQL形状service3.530s/repository0.061s。固定树audit、scheduler-diff、文档、范围diff-check通过；283仅追加且TrimSpace校验匹配。无全量/race/实库重跑，Ent沿用此前生成器复验。
- 限制与后续：实施者最终HEAD全量final4因磁盘中断后转final5，尚未交付最终结果，不宣称最终门禁已过。可后续复用三个完成点的模型局部变量、删除用量文档“成功的”括注；完成后收尾实施记忆。没有新增必删helper或建议复制外部overlay进CI。
- 远端与边界：只读ls-remote和该head全部状态PR查询未见本任务远端分支/PR，main未变；证据不覆盖推后删除历史。未连生产、修改业务/实施树、提交或推送。固定审查副本正常移除，报告/日志留本地任务目录，本轮登记与旧交接原样追加归档；主检出记忆仅维护本轮事项。
- 环境说明：N3首次定向编译遇磁盘不足且无诊断；实施者清理自身备用缓存恢复空间后重跑通过。本审查停止了自己的旧构建缓存扫描，未取得删除结果，不把释放空间记为本审查操作。

### 2026-10-29 — 20261029-agent-succession — 会话与项目负责人职责接续完成

- 请求/目标：读取指定前任及其子 Agent 会话，结合工作区现有资料恢复项目背景、架构、目标和后续任务。
- 开始状态：主检出 main `03fc67de3`，两份记忆文件已有未提交修改；功能工作树 `feat/recovery-optimization-20261029` / `cca0c4c26` 干净，全部保留。
- 完成操作：完整读取主记忆，恢复主会话、七个 Codex 子会话及研究分支八份会话的任务与交付；阅读现行资料、方案、审查、实施/发布报告和关键代码；核对最终代码后的两提交仅改文档、final5 原日志与流水线退出 0、当前远端/PR 和迁移编号。将 17 份临时门禁日志原样复制到本地接续包并生成摘要校验清单，未复制原始会话或敏感信息。
- 结果：N1–N5 本地实施和验收已完成，FINAL_review 无新增 P0/P1；原主检出“最终门禁未交付”状态由新交接摘要补充闭环。当前唯一开放 #163 全绿、无迁移，283 在已核实的 main/开放 PR 范围内空闲；合入前再查。产品目标继续是减少重连和等待，并保护首输出、缓存、路由资格和计费，保持精简。
- 修改文件：本记忆新增接续摘要/日志，自己的进行中条目原样追加归档；本地 `.work/README.md` 更新索引和完成状态，新增接续报告及校验日志副本。前人记录、业务代码与功能工作树均保留；未提交、推送、创建 PR、发布、部署或连接生产。
- 验证：最终功能树 `sync.py audit`、`sync.py scheduler-diff`、范围 `git diff --check` 通过；核对已有 final5 的 8 包 unit、475 项 race 零跳过、lint 0 issues、build/四组 vet/tools 等结果，不声称本轮重新运行重门禁。主检出文档一致性与 diff-check 结果留本地接续包。
- 卡点与下一步：接续本身无阻塞，现有授权内的实施/验收已完成；后续推送、PR、合并、0.4.13 发版和部署仍按用户明确授权。上线后按 FINAL_PLAN v1.1 §5 校准 N5；WS 阶梯、失败率策略、count_tokens 等保持原暂缓决定。

### 2026-10-29 — 20261029-recovery-pr-integration — #163 合入与补救优化集成准备

- 授权/起点：用户要求推送、PR 与合并，暂不发版；main `03fc67de3`，功能分支 `cca0c4c26`，主检出两份未提交记忆完整备份并保留。
- 完成：核实 #163 最终 head `557b59d87`、18/18 成功检查及保护要求，常规合并为 `01e539d41`；功能分支 merge 最新 main，仅记忆追加日志冲突。保留双方记录及主检出的生产观察/审查/接续内容，已结束条目与 10 月 22–25 日旧日志逐段原样追加归档，有摘要校验清单。
- 代码与验证：两支业务改动没有共同文件，自动合并，无手工产品改动；N1–N5 原最终门禁与 #163 CI 按各自不变文件继承。本地集成文档、调度登记、工具单测及提交后 audit 结果写本地任务报告，完整集成测试由新 PR 固定 HEAD 的 CI 覆盖。
- 下一步：推送本分支并创建 PR，所有必需检查成功后按固定 head 合并；不改产品版本、不打 tag、不发布、不部署。

### 2026-10-29 — 20261029-recovery-pr-integration — 修复首轮 CI 的观测集成测试污染

- 现象与根因：PR #164 首轮 push/PR 的 unit 均通过，repository 集成均报看板请求数 2→10、活跃用户 1→2；新增 TestRecoveryObservabilityMigrationAndRoundTrip 在共享数据库提交 8 条用量与一个用户，却未清理。
- 修改：仅该测试加唯一自有前缀和 t.Cleanup，按外键顺序清理自有 usage、ops、Key、用户、账号；保留四条写入路径、迁移幂等、NULL/值与原看板断言，不改产品代码或迁移。首版前缀使请求 ID 超过 64 字节，本地验证发现后缩短随机后缀，未推送错误版本。
- 验证：本机临时 PostgreSQL 18 + Redis 原样复现旧测试的两个失败；修复后对应定向通过，五类自有记录均为 0；完整用量仓储筛选 31 个顶层用例/套件通过、零跳过。尝试全仓储时，既有备份测试因本机无 Docker 中断，不冒称全仓储通过；完整矩阵由新 HEAD 的 CI 验证。临时服务已停止并删除。
- 记录：初次本地 PostgreSQL 启动失败未执行测试，调整临时 socket 目录后运行；原始失败、通过和环境阻塞日志均保留在本地任务证据目录。此前被新生产核验取代的旧版本速览补存归档，确保原记录完整。
- 下一步：提交前文档/格式/集成 vet，提交后干净树 audit；追加修复提交并普通推送，按新固定 HEAD 全部保护检查通过后合并，不发版、不部署。

### 2026-10-29 — 20261029-recovery-pr-integration — #163 与 #164 合并完成，暂不发版

- 授权/范围：用户要求继续推送、PR 与合并，明确暂不发版；本轮交付 #163 账号分组权限修复与 #164 补救优化，不创建版本标签、Release 或部署。
- 远端结果：#163 固定 `557b59d87`、18/18 成功检查后合并为 `01e539d41`；#164 固定 `bbb42596f`、18/18 成功检查后于 2026-10-29T08:15:16Z 合并为 `c8dc3c3af`，业务树与已验证 head 相同。均常规 merge，无 admin 绕过、force push 或 rebase。
- CI 修复：首轮新增观测集成测试留下 8 条用量污染旧看板，已用唯一前缀和定向清理修复。原失败在真实 PostgreSQL 复现，修后目标与完整用量仓储筛选通过；本机全仓储受既有 Docker 备份测试限制，最终 GitHub 完整 integration 已通过，不把本地受限结果当作全通过。
- 验证：两个 PR 的全部最终检查成功；#164 unit/integration、gateway reliability/resilience、真实 Redis 缓存与真实 PostgreSQL 充值均完成。合入前后树相同；283 唯一新增、旧 329 项 checksum 不变。主检出旧记录逐段包含于最终主记忆/归档，原归档前缀完整保留，核对后才同步本地 main。
- 收尾：已结束交接及自身进行中登记原样归档，操作日志只追加；本次纯文档提交只更新最终交接事实，业务/版本文件不变。原失败、红绿证明、CI 状态、归档与同步校验在本地任务证据目录。
- 下一步：发版由用户暂缓，部署和上线后 24–48 小时 N5 校准需另行授权；本轮没有待修业务阻塞。vue-i18n 限期例外维护与 WS/内容审计测试等既有事项继续保留。

### 2026-10-29 — 20261029-release0413 — 版本准备与全部 PR 核对

- 请求/授权：用户要求全部 PR 无缝合入 main 后正式发布 0.4.13；推送、PR、常规合并与 annotated tag/Release 在范围，部署和生产操作不在范围。
- 起点：main `a270a2f73`、工作树干净；GitHub REST 全页开放列表与显式指定仓库的 gh 查询均为空，#163–#165 已 merged。用户补充仍有其他 PR，已请求具体编号/仓库，同时继续独立的版本与核验准备，发布前会再次实时核对。
- 修改：VERSION 与 UPSTREAM_BASE 产品版本升至 0.4.13，兼容 0.2.15 不变；现行用量/韧性文档将迁移 283 与观测从未发布改为 0.4.13，并保留锁与可保留列回退说明。任务状态登记在本记忆，证据仅放本地任务目录。
- 验证与下一步：本地文档/调度登记/工具门禁，提交后干净树 audit；版本 PR 全部保护检查成功且其他 PR 交付完毕后合并与发布，独立核验全部附件和镜像，最后纯记忆收尾。不把通过检查表述为绝对不存在潜在问题，保留既有安全例外与上线观测边界。

### 2026-10-29 — 20261029-release0413 — 全部 PR 合并与 0.4.13 发布验证完成

- 授权与起点：用户要求当前仓库全部 PR 合入 main 后发布 0.4.13；起点 `a270a2f73`，原开放 PR 为零。用户补充其他 PR 后再次用 REST 分页、显式仓库 gh 与全部关闭列表核对，确认 #163–#165 均 merged，另 8 项为上游同步 Issue；未擅自关闭 Issue 或同步上游。
- 版本与合并：提交 `c179c4347` 更新双版本中的产品版本与现行迁移/观测版本标注；41 项工具测试、文档一致性、调度差异、diff-check、干净树 audit 通过。PR #166 全部 18 项检查成功，固定完整 head 常规合入 `66778769c`，合并树与通过检查的 head 相同。
- CI 故障处置：PR CI 的 macOS shell 排队后取消，runner/steps 为空，GitHub 明确 hosted runner 容量不足；首次重试因整轮仍运行未受理。等待单测、集成与后续门禁全部通过后只重试该作业，获得 runner 后 19 秒通过；不改 CI 或分支保护、不重跑成功测试。
- 发布：核对所有开放 PR、版本、标签及 Release 不存在后创建 annotated v0.4.13，远端对象 `e6104dae874cf96f607663956fa01a02451f3688` 与 peeled target `66778769cdd0e35b691436d254ed02c39028c7a9` 二次一致。Release run `40921036209` 成功，正式发布说明完整保留迁移 283、回退、阈值与限期安全例外。
- 验证：475 项韧性 race、16 项真实 Redis 缓存、4 项真实 PostgreSQL 计费必需测试零跳过通过，完整 unit/integration、安全与前端通过。五平台资产双 SHA、全部二进制源码身份、GHCR 双架构与稳定别名、原生全新/升级/回退/重升均核验；用户与 Key 数据保留，临时服务和数据已清理。不冒称执行了 Docker 镜像启动。
- 收尾：已结束的 N1–N5、管理员分组和自身进行中记录及旧版本速览原样追加归档，保留其他任务与原归档前缀。两份记忆的纯文档收尾走 PR；标签不移动，最终 main 必须除记忆外与发布提交零差异。
- 边界与下一步：本次不部署、不连接生产。vue-i18n 依赖迁移、上线后 N5 校准与既有明确暂缓事项继续按原边界处理；本地报告记录最终归档 PR、远端与工作树核验，不再为记下自身合并 SHA 递归创建收尾 PR。

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
