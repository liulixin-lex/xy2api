# 任务：网关韧性第二期（0.4.4）

> 冲刺 2026-10-21T00:47Z–12:47Z。各 agent 在自己的分支开发，α 在 `feat/resilience-v2-20261021` 集成。每项行为改动先写失败用例，确认在基线 `027a434ad` 上失败。完成一项勾一项。

## 0. 契约与登记

- [x] 0.1 项目记忆登记 `20261021-resilience-v2-0.4.4` 与各 agent 的代登记。
- [ ] 0.2 OpenSpec 变更：proposal、design、tasks、规格增量；`docs/README.md` 目录。
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
- [ ] 2.5 用例：created→failed、created→queued→failed、created→元数据→failed、Chat busy、Messages EOF、内容拒绝与 incomplete 不重放、合法空回答、工具调用首事件、CDN 心跳后换号、客户端取消、失败尝试 usage 不混入、开关关闭回到 0.4.3、严格 SDK 事件顺序。

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
- [ ] 5.5 W5.5 Codex 无号 503 的 Retry-After 上限（α）。
- [x] 5.6 W5.3 合成 `response.failed` 有条件带 retry-after：handler 侧（α `9b951b4de`）、分组 advisor（γ `d4db8d426`）、service 侧三个点（β `e30e6c384`）。

## 6. W6-lite 仅流式能力（γ、FE）

- [x] 6.1 学习、有效期、存放（账号 `extra` 两键加指纹）；本请求换号不计账号失败（γ `2cb3343de`、`126fead6f`；β 的 OpenAI 三处检测钩子 `09ef67f11`）。
- [ ] 6.2 选号过滤（非流式请求）；无号原因码 `capability_stream_only` 与只因能力而无号的专用应答（γ `2cb3343de`、`a03ad9c79`、`f61b7d497`，α `6322aa374`）；`scheduler-diff.json` 与 `capabilityEligibilityReason` 钩子登记（α 集成时）。
- [x] 6.3 Explain 原因；管理接口（查看、清除）：DTO 与清除路由 α `bf38c06c5`，Explain 与入口标记 α `affef416f`。
- [ ] 6.4 前端：账号的仅流式标识与清除；Explain 文案与请求类型选择（FE `e042880ad`、`ade0f689d`、`74e392ffd`；等 α 的 DTO、路由与 Explain 联调）。

## 7. W8-lite 事件字段（α、β）

- [x] 7.1 （α `0a5bd1c49`、β 采集 `04aa6f1a6` 与 `keepalive_committed` `e30e6c384`；PM 决定的 `request_id`/`gateway_request_id` 口径 α `d842a23e0`）五类事件补 `request_id`、`model`、已送达字节、距首个输出的毫秒数、释放暂存的事件与毫秒数。

## 8. 前端设置（FE）

- [ ] 8.1 新开关的设置界面与中英文说明；单测；lint、typecheck、test、build（FE `41dfd5ea6`、`34d87ef10`）。

## 9. 文档与登记（DOCS、α）

- [ ] 9.1 `docs/GATEWAY_RESILIENCE.md`：能力表、客户端可见行为、新字段、事件字段、不受开关控制的修复、已知限制；Codex 重试说法更正。
- [ ] 9.2 `tools/docs_consistency.json`：新设置与事件名能在文档中找到；退役名登记。
- [ ] 9.3 `policy.json` required_literals 与具名补丁、`scheduler-diff.json`（α 集成时汇总各 CHANGES）。
- [ ] 9.4 `python3 tools/check_docs_consistency.py` 在集成树通过。

## 10. 集成与门禁（α）

- [ ] 10.1 按序 `--no-ff` 合入六个分支；`VERSION` 与 `UPSTREAM_BASE.json` 改为 0.4.4。
- [ ] 10.2 全量门禁：gofmt、四种 vet、golangci-lint、unit 与无 tag 测试、`ci_gate.py`（`-race`，零跳过）、前端四项、`tools/tests`、文档一致性、干净树 `sync.py audit`。
- [ ] 10.3 独立审查与修复。

## 11. 测试服务器验证（QA）

- [x] 11.1 新故障 F31–F37、F34B 与场景 CV-5–CV-9、RAW-1、RAW-2、SO-1、RA-1、RA-2、ANT-3、Q-1、CX-1，DC-6、LR-1 随新行为修改，先在基线上确认（基线结果见 design §12；QA `ce8c3c01d`、`3320a508a`、`236b12b97`，合计 71 个场景，基线 17 项定向中 8 项通过、9 项预期红灯）。
- [x] 11.2 PR #117 的份额场景 SH-1、SH-2：切换到份额、健康系数生效、回到原版（`ce8c3c01d`，基线通过）。
- [ ] 11.3 唯一候选：全套 71 个场景、升级演练（0.4.3 → 候选，含回退与再前滚）、3 小时浸泡。

## 12. PR 与发版（PM 安排，α 执行）

- [ ] 12.1 推送并开 PR，CI 与安全扫描全绿。
- [ ] 12.2 合并 #116、#117 与本变更的 PR；打 annotated `v0.4.4`；Release 核验与正式镜像全新安装。
- [ ] 12.3 发版后纯文档收尾：记忆、归档、本文件打勾。

生产部署 0.4.4 未授权。
