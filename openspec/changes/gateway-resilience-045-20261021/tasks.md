# 任务：网关韧性 0.4.5

> 本地准备，2026-10-21 起。各分支只在本地提交；是否发 0.4.5、推送与发版由用户决定。每项行为改动先写失败用例，确认在旧代码上失败。完成一项勾一项。

## 0. 文档

- [x] 0.1 OpenSpec 变更：proposal、design、tasks、规格增量；`docs/README.md` 目录（DOCS `docs/045-prep`）。
- [x] 0.2 `GATEWAY_RESILIENCE.md`：开头一段、§3 中 RA-1 的上游等待、§7 的 RA-1 条目；旧变更 runbook §6 的开关说明；0.4.4 变更 design §11 与 tasks 5.6 的指引。ε 与 δ 已在各自分支改过的章节不重复修改。

## 1. RA-1（α）

- [x] 1.1 最终耗尽时把最后一个可重试上游（429/5xx）的有效 `Retry-After` 当作已知恢复时间，夹到 2–10 秒；健康账号、已知恢复时间与终态抑制优先；400、404 映射的 502 不采用（`fix/retry-advice-upstream-wait-20261021` @ `4ef7be248`，定向 `-race` 141 个顶层、330 个叶子用例通过）。
- [x] 1.2 审查：可作为 0.4.5 候选的组成部分（`review/CODE_REVIEW_045_RA1.md`）。

## 2. 审查遗留的 P2（ε，`fix/review-p2-045` @ `10f2edd6b`）

- [x] 2.1 raw Chat `error` 判据（`006be0637`、`b8920cc89`），补登 required_literal（`4931a5e2d`）。
- [x] 2.2 心跳只提交网关自身的 SSE 头（`db86d3377`）。
- [x] 2.3 首次写出与取消的竞态（`2a67cbf88`、`930213caa`）。
- [x] 2.4 `lastResortGroups` 过期清理（`75fa06e97`）。
- [x] 2.5 首个内容之后免锁（`893280f62`）。
- [x] 2.6 重试建议只在写失败帧时解析，RA-1 用例保持通过（`2cec5c547`）。
- [x] 2.7 能力原因只在主因时追加（`ca7c67dc7`）。
- [x] 2.8 R2-P2-3 文档补充（`9f1c57f72`）。
- [x] 2.9 审查：可以并入（`review/CODE_REVIEW_045_EPS.md`）。最终 HEAD 的定向 `-race` 通过。
- [x] 2.10 测试：唯一调用 `gin.SetMode` 的并行用例改为顺序执行，修掉基线既有的偶发竞态（`10f2edd6b`，只改测试）。

## 3. W1.2 其余路径（δ，`feat/w12-gateway-guards-045` @ `50053a8ea`）

- [x] 3.1 Gateway 入站经 Anthropic 上游的 Responses 与 Chat：推迟开场、首事件截止延长、读取循环、停滞、明确终止帧（`bd960dc5a`）。
- [x] 3.2 审查 DELTA-P1-1（输出后异常结束按已读用量计费一次）与 DELTA-P2-3（输出前客户端离开改为 Info 日志）（`c95aa4e7c`）；DELTA-P2-1（`8a7fa67ea`）；DELTA-P2-2 对照用例（`9f8f5ba6c`）。
- [x] 3.3 OpenAI Responses 透传：首输出截止（含等待响应头）、停滞检测、读取循环、有界暂存、暂存期心跳与注释透传（`e3f902390`、`5f5386f4a`）。
- [x] 3.4 Responses 透传输出后断流按已读用量计费一次，第一个输出前取消不计费（审查 DELTA-P2-5，`87f9b9356`）。
- [x] 3.5 透传在心跳已提交 200 后命中错误透传规则时，写 `response.created` 与 `response.failed`，不再接裸 JSON（审查 DELTA-P1-2，`6420bc73c`；普通 OpenAI Responses 路径同样修复，两条路径共用一个写出函数，`74133b338`）；反向转换器停滞出口的计费用例（DELTA-P2-6，`ab227b00f`，只改测试）。
- [x] 3.6 审查：`74133b338` 可以并入 0.4.5 候选，没有剩余的 P0、P1（DELTA-P1-1、P1-2 已关闭；DELTA-P2-4 记为已知差异，`review/CODE_REVIEW_045_DELTA.md`）。前提是 δ 在该 HEAD 上的定向 `-race` 通过；之后再有代码提交需要重新复核。
- [x] 3.7 首次集成发现的问题（PM 指令 6、7）：cherry-pick ε 的两个依赖提交（`1c56a8b2d`、`37b998fa5`）；Gateway 反向转换首写与取消同时发生按客户端断开（INTEG-P2-2，`104a92e99`）；透传心跳只提交网关自身的头（INTEG-P2-1，`67c57d5e9`，后经 PM 裁定撤回，见 3.8）；旧 OAuth 透传预取消用例按 `stream.strict_terminal` 拆分（`9bd6c119e`）。
- [x] 3.8 PM 2026-10-21T15:00Z 裁定撤回 INTEG-P2-1，透传心跳的响应头保持 0.4.4（`c5ccb7e14` 回退，`0c01bf2a5` 固定写头时机）；turn-state 溯源只在响应提交前记录（`50053a8ea`，可单独撤回）。最终 HEAD 上定向 `-race -count=3`（service 585 项、handler 93 项）0 失败、0 跳过、0 竞争，增量 lint 0 项，文档检查、scheduler-diff 与干净临时 worktree 的 audit 通过。
- [x] 3.9 审查 `74133b338..50053a8ea`（8 个提交）：δ @ `50053a8ea` 可以并入 0.4.5 候选，没有 P0、P1；INTEG-P2-1 按 PM 裁定作为已知差异关闭，INTEG-P2-2 已修复关闭；撤回 `c5ccb7e14` 干净（REVIEW 2026-10-21T15:15Z，`review/CODE_REVIEW_045_DELTA.md`）。前提是该 HEAD 的定向 `-race` 通过；集成合并后要重跑全量门禁并补审合并结果（4.2、4.3）。

## 3A. 原生 Responses 第一次心跳时带上 turn-state（O3；PM 2026-10-21T16:30Z 纳入 0.4.5）

- [x] 3A.1 实现（β `feat/native-heartbeat-turn-state-045` @ `93fd9bdf2`，基于 `b966306de`）：原生暂存期第一次心跳提交时，如有这次尝试的 `x-codex-turn-state`，只多写这一个头并同时记录溯源；不加开关；登记 required_literal 与具名补丁测试；`GATEWAY_RESILIENCE.md` §6 与“心跳只写网关自身的头”一行注明例外（β）。用例按 `review/PROPOSAL_046.md` §1.6：单次成功拿到 turn-state；心跳后换号时守卫剥离；无心跳、无 turn-state、提交早于响应时行为不变；门票账号、R2-P2-1、原生规则终止帧的既有用例不受影响。新增 6 个顶层用例，其中首心跳带 turn-state、心跳后换号守卫剥离、心跳后错误规则终止帧 3 个在修复前失败；49 个相关顶层用例各三轮 `-race` 全部通过（147 顶层、243 项含子用例，0 失败、0 跳过、0 竞态），增量 lint 0 项，两种 vet 通过；`policy.json` 新增 1 条 required_literal，6 个用例绑定既有具名补丁 `xy-resilience-v2-streams`（仓库外 `evidence/o3-045/REPORT.md`）。分支最终 HEAD `58ad8af6f` 只在产品提交之后追加了记忆里的审查归属，产品代码、测试、`policy.json` 与说明和 `93fd9bdf2` 逐字相同；两者的干净树 audit 都通过。
- [x] 3A.2 代码审查（REVIEW，`review/CODE_REVIEW_045_O3.md`）：`93fd9bdf2` 可以并入 0.4.5，没有 P0、P1、P2；`PROPOSAL_046` §1.5 的实现要点逐条满足，§1.6 的用例除不适用的开关一项外都有；turn-state 只写一次，不泄漏失败账号的 `x-request-id` 与用量头；门票账号、CDN 首包保活、排队心跳、无心跳与 R2-P2-1 不回归。前提是合入后在新 SHA 上重跑全量门禁（4.5）。

## 4. 集成（α，PM 2026-10-21T12:51Z 授权本地集成；2026-10-21T18:20Z 范围冻结，不再出新候选）

- [x] 4.1 按 RA-1 → ε → DOCS → δ 合入；手工合并 `GATEWAY_RESILIENCE.md` 的可重放前缀与严格终止说明、`policy.json` 的具名补丁测试，两边增补全部保留；`VERSION` 与 `UPSTREAM_BASE.json` 改为 0.4.5。基线既有的 `contentModerationHandlerSettingRepo` 竞争以只改测试的独立提交 `c906ba5ca` 修复，修前复现、修后 `-race -count=3` 通过。四个原版调度文件相对 main 无变化，scheduler-diff 保留既有差异登记。 已增量合入 δ 最终 `50053a8ea` 与 DOCS 最终 `2ebb17d1e`，保留 ε `930213caa`；首次候选 `43be712aa` 门禁为 22/25，不作为候选，见 design §7。
- [x] 4.2 全量门禁：gofmt、四种 vet、golangci-lint、unit 与无 tag 测试、`ci_gate.py`（`-race`，零跳过）、`govulncheck`、前端四项与依赖审计、`tools/tests`、文档一致性、scheduler-diff、干净树 `sync.py audit`。
  - 2026-10-21T15:51Z，α 在本地集成分支 `integ/0.4.5-prep`：门禁对象是产品代码 `46239531d`，25/25 通过。最终本地候选 `b966306de` 只追加了记忆与归档提交，代码与 `46239531d` 逐字一致，并在最终 HEAD 上复验文档检查与干净树 audit；`VERSION` 与 `UPSTREAM_BASE.json` 为 0.4.5。
  - unit：第三轮原始结果 24 项通过、1 项失败，失败的是 `TestInflightEstimate_AccountMappingNoDBAndBoundedMemory` 的全局堆阈值（基线既有的偶发，见 7.4）。单独重跑 5 次全过；按 PM 指示用原命令完整重跑 unit（attempt3b）通过，以它替代该项结果，原失败保留。没有跳过用例，也没有放宽阈值。 **已修复**：PM 18:42Z 放行 ε 的只改测试提交 `13baa2869`，见 7.4；原始失败记录保留，最终树完整 unit 与 ci_gate 结果另由 α 留证。
  - 其余：无 tag 测试；`ci_gate.py` 390 个顶层用例 `-race` 0 跳过；service 与 handler 的额外定向 `-race` 全部执行、0 失败；前端 413 个文件、3182 个用例；四种 vet 与 lint；`govulncheck` 无可达漏洞；依赖审计无高危、严重；`tools/tests` 41 例；文档检查、scheduler-diff、带前端产物的构建、干净树 audit。
- [x] 4.3 对集成结果的独立审查：`46239531d` 可以作为 0.4.5 本地候选，没有 P0、P1，也没有新的 P2（REVIEW 增量补审 2026-10-21T15:35Z）；`b966306de` 只追加记忆与归档、无代码改动，确认为最终本地候选（`review/CODE_REVIEW_045_INTEG.md`）。
- 以上 4.1–4.3 针对不含 O3 的 `b966306de`，它现在只是预跑候选（design §7）。O3 并入后：
- [x] 4.4 合入 O3，得到新的候选 SHA：O3 `93fd9bdf2` 快进合入，DOCS `88ad8c43c` 合入（tasks.md 整份采用 DOCS），门禁对象 `4bb8026db`；O3 的产品代码、测试、`policy.json` 与说明与 β 交付逐字一致，ε 的三个相关文件不变，309 条 required_literals、112 条 custom_patch_tests 无重复。最终 SHA `0c31b5b38` 只在门禁对象之后追加记忆（α，2026-10-21T18:05Z）。
- [ ] 4.5 在新 SHA 上重跑全量门禁（同 4.2 的各项）。第 4 轮（`4bb8026db`）25 项中 24 项通过；**unit 阻塞**：只有 7.4 那条基线堆阈值用例在完整套件中失败，两次完整运行分别为 9,100,136、8,414,632 字节（阈值 8,388,608），单独跑 5 次全部通过。测试文件与 main 逐字相同，没有跳过用例或放宽阈值，原失败保留。其余 24 项通过：gofmt、四种 vet、lint 0 项、无 tag 测试、`ci_gate.py` 390 个顶层 `-race` 0 跳过、额外 service 与 handler `-race`、`govulncheck`、前端四项与依赖审计、`tools/tests`、文档检查、scheduler-diff、带前端产物的构建、干净树 audit。PM 2026-10-21T18:00Z：不等 unit，先把最终 SHA 交 QA；α 随后在 main `a1cf8f104` 上用同一完整 unit 命令对照，2026-10-21T18:13Z 在基线上复现同一失败（8,724,016 字节），说明这是 O3 之前就有的基线问题，但候选的 unit 门禁仍不能判为通过。ε 只改测试的修复 `13baa2869` 在 PM 定的 18:50Z 期限前交付，完整 service 套件 `-count=3` 通过；PM 18:42Z 放行，α 合入为 `e9c686e84`（只多这一个测试文件与文档，产品代码不变，测试不进镜像，QA 的候选 `0c31b5b38` 不重建），正在这个 HEAD 上复跑完整 unit 与 `ci_gate`，通过才勾本项（7.4）。
- [ ] 4.6 对 O3 并入后的集成结果做独立审查（REVIEW）。

## 5. 测试服务器验证（QA）

- [x] 5.1 新故障与场景（QA 工具 `b6805ec73`、夹具修正 `64e28dba5`：F38、F39 与 8 项串行补测；在正式 0.4.4 上 6 项复现旧行为，F39-5R、F39-5C 旧版也通过，F38-12 旧版返回裸 422 JSON）：Gateway 入站经 Anthropic 上游、Responses 透传、心跳响应头、raw Chat 占位 `error`（建议清单见 design §7），先在 0.4.4 上确认。
- [ ] 5.2 唯一候选 `0c31b5b38`（O3 并入后的最终 SHA，PM 2026-10-21T18:00Z 交 QA；与门禁对象 `4bb8026db` 只差记忆；范围已冻结，PM 18:20Z）。只构建一次；主栈 71 个场景（含 RA-1 复跑与 O3 场景）、12 项补测、0.4.3 → 0.4.5 与 0.4.4 → 0.4.5 两条升级演练；浸泡以 3 小时为发版判据（热身后至少 3 个周期时出阶段判定），继续跑满 6.5 小时作为生产部署的证据，不阻塞发版决策。QA 2026-10-21T18:27Z 完成唯一构建并开始浸泡与场景；3 小时阶段判定约 21:27Z，报告在仓库外 `staging/REPORT_045_0c31b5b38.md`。
- [x] 5.3 预跑 `b966306de`（QA 2026-10-21T15:53Z 起）：71 个场景 71 个通过，RA-1、RA-2 通过（2026-10-21T17:11Z），作为预跑证据保留，报告在仓库外 `staging/REPORT_045_b966306de.md`。这个 SHA 不做升级演练与浸泡（PM 2026-10-21T16:30Z），不是最终候选。

## 6. PR 与发版（用户授权后）

- [ ] 6.1 推送并开 PR，CI 与安全扫描全绿。
- [ ] 6.2 合并；打 annotated `v0.4.5`；Release 核验与正式镜像全新安装。
- [ ] 6.3 发版后纯文档收尾：记忆、归档、本文件打勾。

## 7. 后续（0.4.6 待办，不阻断 0.4.5）

7.1–7.3 由审查在 δ 复核与集成审查中提出（design §8），7.4 来自 0.4.5 第三轮门禁。7.1 与 7.4 已提前到 0.4.5 处理，7.2、7.3 仍留在 0.4.6。

- [x] 7.1 原生 Responses 路径心跳之后拿不到 turn-state：已在 0.4.5 用 O3 处理（3A，design §3.4）。
- [ ] 7.2 turn-state 溯源改为在响应头真正发出时才记录，消除“响应到达后、提交前失败并最终耗尽”与 CDN 首包保活微秒级窗口两处不准的记录；同时收窄 `GATEWAY_RESILIENCE.md` 里“溯源只记客户端实际拿到的那次尝试”的措辞（该行归 δ；本变更的 design 与规格已改为“响应尚未提交时到达的尝试”）。
- [ ] 7.3 LEAK-P2-1：WS 状态绑定表的清理吞吐固定为每表每分钟 512 条。评估按上次清理以来的写入量调整扫描量，或表满时优先淘汰已过期的条目；是否做具名补丁按上游同步机制决定（`review/LEAK_AUDIT_0.4.4.md` 第 5 节）。
- [x] 7.4 `TestInflightEstimate_AccountMappingNoDBAndBoundedMemory` **已在 0.4.5 修复**（ε `test/inflight-heap-robust-045` @ `13baa2869`，PM 18:42Z 验收并放行，只改测试）。一次 GC 后的 `HeapAlloc` 仍含 sync.Pool victim 与待 finalizer 对象，改为预热 1000 个未定价模型后，在测量前后各连续两次 GC 再读稳定堆；n=20000、8 MiB 阈值与原断言意图保持。ε 单例 `-count=20` 与完整 service `-count=3` 通过（25848 项通过、27 条件跳过、0 失败，堆用例 3/3），灵敏度核对仍能检出按模型缓存。修复前的第三轮偶发、attempt4/4b 与 main 对照失败证据保留；集成最终树按 PM 指令复跑完整 unit 与 ci_gate，结果由 α 留证。

生产部署未授权。
