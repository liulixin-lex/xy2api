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

## 3. W1.2 其余路径（δ，`feat/w12-gateway-guards-045` @ `87f9b9356`）

- [x] 3.1 Gateway 入站经 Anthropic 上游的 Responses 与 Chat：推迟开场、首事件截止延长、读取循环、停滞、明确终止帧（`bd960dc5a`）。
- [x] 3.2 审查 DELTA-P1-1（输出后异常结束按已读用量计费一次）与 DELTA-P2-3（输出前客户端离开改为 Info 日志）（`c95aa4e7c`）；DELTA-P2-1（`8a7fa67ea`）；DELTA-P2-2 对照用例（`9f8f5ba6c`）。
- [x] 3.3 OpenAI Responses 透传：首输出截止（含等待响应头）、停滞检测、读取循环、有界暂存、暂存期心跳与注释透传（`e3f902390`、`5f5386f4a`）。
- [x] 3.4 Responses 透传输出后断流按已读用量计费一次，第一个输出前取消不计费（审查 DELTA-P2-5，`87f9b9356`）。
- [ ] 3.5 审查：反向转换器部分的 P1 已修；透传部分的补审结论。

## 4. 集成（α，用户决定发 0.4.5 之后）

- [ ] 4.1 合入 ε（含 RA-1）、δ 与本文档分支；手工合并 `GATEWAY_RESILIENCE.md` 第 18、133 行与 `policy.json` 的具名补丁测试；`VERSION` 与 `UPSTREAM_BASE.json` 改为 0.4.5。
- [ ] 4.2 全量门禁：gofmt、四种 vet、golangci-lint、unit 与无 tag 测试、`ci_gate.py`（`-race`，零跳过）、`govulncheck`、前端四项与依赖审计、`tools/tests`、文档一致性、scheduler-diff、干净树 `sync.py audit`。
- [ ] 4.3 对集成结果的独立审查。

## 5. 测试服务器验证（QA）

- [ ] 5.1 新故障与场景：Gateway 入站经 Anthropic 上游、Responses 透传、心跳响应头、raw Chat 占位 `error`（建议清单见 design §7），先在 0.4.4 上确认。
- [ ] 5.2 唯一候选：全部场景（含 RA-1 复跑）、升级演练 0.4.4 → 0.4.5 → 0.4.4 → 0.4.5、3 小时浸泡。

## 6. PR 与发版（用户授权后）

- [ ] 6.1 推送并开 PR，CI 与安全扫描全绿。
- [ ] 6.2 合并；打 annotated `v0.4.5`；Release 核验与正式镜像全新安装。
- [ ] 6.3 发版后纯文档收尾：记忆、归档、本文件打勾。

生产部署未授权。
