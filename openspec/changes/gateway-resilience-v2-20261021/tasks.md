# 任务：网关韧性第二期（0.4.4）

> 冲刺 2026-10-21T00:47Z–12:47Z。各 agent 在自己的分支开发，α 在 `feat/resilience-v2-20261021` 集成。每项行为改动先写失败用例，确认在基线 `027a434ad` 上失败。完成一项勾一项。

## 0. 契约与登记

- [x] 0.1 项目记忆登记 `20261021-resilience-v2-0.4.4` 与各 agent 的代登记。
- [x] 0.2 OpenSpec 变更：proposal、design、tasks、规格增量；`docs/README.md` 目录（DOCS，design 按最终契约定稿于 2026-10-21T04:46Z）。
- [x] 0.3 C0 设置契约（γ，`contract/C0_SETTINGS.md`，2026-10-21T01:30Z 冻结）：字段、缺省、范围、存放、回退结论；已写入 design §1–§2。
- [x] 0.4 RetryAdvice 契约（α，`contract/RETRY_ADVICE.md`）。
- [x] 0.5 生产探测：Responses 首个 delta 之前的事件类型清单（2026-10-21，PM 指令据此定白名单，design §3.1）。

## 1. 设置（C0，γ）

- [x] 1.1 新开关的结构体、缺省值、解析、校验、复制与门控方法；热更新（γ，`95e8a6416`）。
- [x] 1.2 旧文档（0.4.3 样本）解析后新开关取缺省；新文档被 0.4.3 的解析逻辑读取的结果（回退安全，`settings_compat043_test.go`，`95e8a6416`）。

## 2. W1.1 可重放前缀（β）

- [x] 2.1 纯函数 `ClassifyStreamEvent`（`493724e11`）。
- [x] 2.2 原生 Responses 白名单扩展（`stream.native_prelude_whitelist`）；送达追踪改用同一判据；伪保活泛化与 `pre_created_delta`（β `bdbca7197`）。
- [x] 2.3 Chat、Messages 兼容转换推迟开场（`stream.prelude_staging`）；首事件守卫延长到首个内容（β `1a97eebd1`）。
- [x] 2.4 释放事件采集（`prelude_trace`，β `04aa6f1a6`）。
- [x] 2.5 用例：created→failed、created→queued→failed、created→元数据→failed、Chat busy、Messages EOF、内容拒绝与 incomplete 不重放、合法空回答、工具调用首事件、CDN 心跳后换号、客户端取消、失败尝试 usage 不混入、开关关闭回到 0.4.3、严格 SDK 事件顺序。均由 β 的用例覆盖；暂存期客户端写出失败与取消的用例与修复在集成门禁期间补齐（PM7：β `766a54fc8`、`dbcc1c80a`、`c7290e396`，α `32f2a6ca7`）。

## 3. W1.2-lite（β）

- [x] 3.1 raw Chat 透传识别 `error` 后跟 `[DONE]`（`stream.strict_terminal`；β `f9edc3a4d`）。
- [ ] 3.2 （有余力）OpenAI Responses 透传的首输出守卫：**延后**，未做，已登记到 design §11。

## 4. W2.1-lite 分类修正（γ）

- [x] 4.1 只有真正的容量信号归 `CAPACITY_SHED`；Grok 空闲、raw 429、“服务忙”类文案改判（`failure.decision_v2`；γ `0d3e72508`）。
- [x] 4.2 运维记录与换号规则共用分类结果；typed 首输出超时单独可辨（γ `0d3e72508`、`4ac81dc94`，α `c73fcb19e`，β `4eed85c1a`）。
- [x] 4.3 用例（`0d3e72508`）：Grok 空闲不被容量排除；raw 429 与 overloaded 区分；busy 允许同主机换号；429 中的余额文案仍为 BALANCE；开关关闭回到旧分类。

## 5. W5 信号与终止出口（α、β）

- [x] 5.1 RetryAdvice 类型与 context 存取（α，`fc9b62751`）；惰性 provider 与 `Message()`（`be736ba0c`）。
- [x] 5.2 W5.2 排队 ping 之后按协议写终止帧（α，`190f6515a`）。
- [x] 5.3 W5.1 三个专用耗尽出口接入重试提示与回合封顶；一次性 finalize（α，`a72a29e00`）。
- [x] 5.4 W5.4 Anthropic 自身失败用 `api_error`（α handler `fe5dbf503`；β `resilience_keepalive.go` `e30e6c384`）。
- [x] 5.5 W5.5 Codex 无号 503 的 Retry-After 上限（α `0c2a67ca5`）。
- [x] 5.6 W5.3 合成 `response.failed` 有条件带 retry-after：handler 侧（α `9b951b4de`）、分组 advisor（γ `d4db8d426`）、service 侧三个点（β `e30e6c384`）。已知缺陷：上游带 `Retry-After` 的容量失败造成耗尽时不带该头、正文也没有重试秒数（测试服务器 RA-1 失败；PM 裁定随 0.4.4 发布，0.4.5 修复，design §11）。

## 6. W6-lite 仅流式能力（γ、FE）

- [x] 6.1 学习、有效期、存放（账号 `extra` 两键加指纹）；本请求换号不计账号失败（γ `2cb3343de`、`126fead6f`；β 的 OpenAI 三处检测钩子 `09ef67f11`）。
- [x] 6.2 选号过滤（非流式请求）；无号原因码 `capability_stream_only` 与只因能力而无号的专用应答（γ `2cb3343de`、`a03ad9c79`、`f61b7d497`，α `6322aa374`）；`scheduler-diff.json` 与 `capabilityEligibilityReason` 钩子登记（α `5f8935d62`）。
- [x] 6.3 Explain 原因；管理接口（查看、清除）：DTO 与清除路由 α `bf38c06c5`，Explain 与入口标记 α `affef416f`。
- [x] 6.4 前端：账号的仅流式标识与清除；Explain 文案与请求类型选择（FE `e042880ad`、`ade0f689d`、`74e392ffd`，HEAD `bd215aa0c`；后端 DTO、路由与 Explain 已由 α 提交，联调随集成）。

## 7. W8-lite 事件字段（α、β）

- [x] 7.1 （α `0a5bd1c49`、β 采集 `04aa6f1a6` 与 `keepalive_committed` `e30e6c384`；PM 决定的 `request_id`/`gateway_request_id` 口径 α `d842a23e0`）五类事件补 `request_id`、`model`、已送达字节、距首个输出的毫秒数、释放暂存的事件与毫秒数。

## 8. 前端设置（FE）

- [x] 8.1 新开关的设置界面与中英文说明；单测；lint、typecheck、test、build（FE `41dfd5ea6`、`34d87ef10`；HEAD `bd215aa0c` 四项全过，3182 个用例）。

## 9. 文档与登记（DOCS、α）

- [x] 9.1 `docs/GATEWAY_RESILIENCE.md`：能力表、客户端可见行为、新字段、事件字段、不受开关控制的修复、已知限制；Codex 重试说法更正（另同步旧变更的 design §10 衔接、staging-validation §6.1 与 §8、runbook §6）。
- [x] 9.2 `tools/docs_consistency.json`：新设置与事件名能在文档中找到（规则不变，旧 design §10 列出新字段名并指向新变更）；本期没有发布过又改名的标识，不需要登记退役名。
- [x] 9.3 `policy.json` required_literals 与具名补丁、`scheduler-diff.json`（α 集成时汇总各 CHANGES：`5f8935d62`、`af3695b89`、`685408f8f`；具名补丁的 provenance `093241b50`）。
- [x] 9.4 `python3 tools/check_docs_consistency.py` 在集成树通过（集成分支 `4f099f8e1` 合入 rv2/docs 最终 HEAD 的预演通过）。

## 10. 集成与门禁（α）

- [x] 10.1 按序 `--no-ff` 合入六个分支；`VERSION` 与 `UPSTREAM_BASE.json` 改为 0.4.4（α，集成分支 `feat/resilience-v2-20261021`；第二轮审查的修复随后增量合入）。
- [x] 10.2 全量门禁：gofmt、四种 vet、golangci-lint、unit 与无 tag 测试、`ci_gate.py`（`-race`，零跳过）、前端四项、`tools/tests`、文档一致性、干净树 `sync.py audit`。最终候选 `a38ae5995`（Go 1.27.3）上 21 项全部通过：上述各项，加 `govulncheck`（无可达漏洞）、前端冻结安装与生产依赖审计（无高危、严重）、带前端产物的后端构建；`ci_gate` 375 项 `-race` 0 跳过，前端 3182 个用例，工具 41 例；没有新增审计例外。此前的 `c086f4cc6` 本地门禁全绿，但安全扫描报出 GO-2026-6809 与 follow-redirects 两个高危公告，候选作废、未做服务器验证；升级 Go 1.27.3（α `8d7a30f27`）与 follow-redirects 1.16.1（FE `60680d3c5`）后重跑。
- [x] 10.3 独立审查与修复：第一轮 1 个 P0、13 个 P1 全部关闭；第二轮在最终代码 `7c021b6a4` 上放行，新增 4 个 P1 已修复复核，剩余 P2 留 0.4.5（仓库外 `review/CODE_REVIEW_R1.md`、`CODE_REVIEW_R2.md`）。

## 11. 测试服务器验证（QA）

- [x] 11.1 新故障 F31–F37、F34B 与场景 CV-5–CV-9、RAW-1、RAW-2、SO-1、RA-1、RA-2、ANT-3、Q-1、CX-1，DC-6、LR-1 随新行为修改，先在基线上确认（基线结果见 design §12；QA `ce8c3c01d`、`3320a508a`、`236b12b97`、`0ad64390c`，合计 71 个场景，基线 17 项定向中 8 项通过、9 项预期红灯）。
- [x] 11.2 PR #117 的份额场景 SH-1、SH-2：切换到份额、健康系数生效、回到原版（`ce8c3c01d`，基线通过）。
- [x] 11.3 唯一候选：全套 71 个场景、升级演练（0.4.3 → 候选，含回退与再前滚）、3 小时浸泡。候选 `a38ae5995` 只构建一次：
  - 场景 **70/71**，RA-1 未通过（W5.3 已知缺陷，PM 2026-10-21T08:14Z 裁定随 0.4.4 发布、0.4.5 修复，design §11）；
  - 升级演练 0.4.3 → 候选 → 0.4.3 → 候选与备份恢复 62/62 通过；
  - 3 小时浸泡 **11/12，未达标**：GC 后存活堆斜率 1.4 MiB/h，门槛 ≤1（热身后 5 个周期中位数 56、57、58、58、59 MiB）；其余项通过。用户 2026-10-21 决定按时发布，生产部署前另跑 6.5 小时浸泡；
  - 浸泡夹具的工具修复 QA `cd72f5eb1`（只改 tools，候选未重建）。报告在仓库外 `staging/REPORT_a38ae5995.md`。

## 12. PR 与发版（PM 安排，α 执行）

- [x] 12.1 推送并开 PR，CI 与安全扫描全绿（[PR #118](https://github.com/liulixin-lex/xy2api/pull/118)，头 `a38ae5995`，push 与 pull_request 两组共 18 个作业通过）。
- [x] 12.2 合并 #116、#117 与本变更的 PR；打 annotated `v0.4.4`；Release 核验与正式镜像全新安装。合并提交 `4b0b6d94b`（源码树与候选相同）；`v0.4.4` 对象 `333f54ad5`；Release run 39582933562 全部成功；产物核验与正式镜像全新安装通过（见下方“发布记录”）。
- [x] 12.3 发版后纯文档收尾：记忆、归档、本文件打勾（`docs/post-release-0.4.4`）。

## 发布记录（2026-10-21）

- 合并：[PR #118](https://github.com/liulixin-lex/xy2api/pull/118) 于 10:06Z 合并为 `4b0b6d94b`，源码树与候选 `a38ae5995` 相同；#116、#117 随之标为已合并。
- 标签：annotated `v0.4.4`，对象 `333f54ad5`，解引用为 `4b0b6d94b`。正文即发版说明，验证一节写明 70/71 与浸泡 11/12 未达标、用户决定放行。
- Release run 39582933562：版本校验、前端构建、GoReleaser 发布三个作业全部成功。
- 产物核验（现行校验入口，全部通过）：6 个附件共 198,881,025 字节，双 SHA 一致；五个平台的二进制 `vcs.revision=4b0b6d94b`、`vcs.modified=false`，含前端产物；GHCR 有 linux/amd64 与 linux/arm64，两个架构的 version、revision、source 标签正确；`0.4.4`、`latest`、`0`、`0.4` 指向同一 index `sha256:2f5133cb…`，`0.3` 仍是 0.3.0 的 digest。第一次核验因校验脚本用了旧的远端入口失败，属于环境问题，没有改 tag、Release 或源码，换用现行入口重跑通过。
- 正式镜像全新安装（测试服务器，空库，按 digest `sha256:2f5133cb…` 拉取 `ghcr.io/liulixin-lex/xy2api:0.4.4`）：通过。启动 2.2 秒，324 条迁移；`/ready` 返回 JSON，`/health`、首页、6 个静态资源与 `/auth/me` 均为 200；公开设置的版本为 0.4.4；经故障上游的计费请求返回 200，`usage_logs` 记 1 条正费用；安装日志 0 条 ERROR；临时容器、卷与数据库已清理。第一次执行时，安装脚本的默认数据库指向已清理的演练库而退出，属于夹具问题，修正后重跑通过，未发现产品问题。

生产部署 0.4.4 未授权：需要单独授权，并且要先通过生产部署前的 6.5 小时浸泡。
