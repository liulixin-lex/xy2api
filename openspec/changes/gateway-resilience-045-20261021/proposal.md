# 网关韧性 0.4.5：RA-1 修复、W1.2 其余路径、审查遗留 P2

> 状态：**本地准备，未发布**。是否发 0.4.5 由用户决定；发版前要把三条分支合成一个候选，并完成全套验证。

## 背景

0.4.4 于 2026-10-21 发布，规格见 [`gateway-resilience-v2-20261021`](../gateway-resilience-v2-20261021/proposal.md)。发布时留下三类工作：

- **RA-1（已知缺陷）**：测试服务器 71 个场景中 RA-1 失败。耗尽由上游带 `Retry-After` 的容量失败造成、且组里没有已受罚账号时，合成的 `response.failed` 不带 `error.headers.retry-after`，正文也删掉了 “try again in Ns”（0.4.3 的正文有）。PM 2026-10-21T08:14Z 裁定随 0.4.4 发布、0.4.5 修复（0.4.4 design §11）。
- **W1.2 其余三条路径**：0.4.4 只给 raw Chat 透传加了明确终止。OpenAI Responses 透传的首输出守卫延后；Gateway 入站经 Anthropic 上游的 Responses 与 Chat 两个反向转换器，仍按 0.4.3 的开场与终止行为（开场立即写出，EOF 合成为成功）。
- **审查遗留的 P2**：0.4.4 两轮代码审查与泄漏排查确认、但没有排期的 P2。

## 改什么

| 分支 | 基线 | HEAD | 内容 | 审查 |
|---|---|---|---|---|
| `fix/retry-advice-upstream-wait-20261021`（RA-1） | `a38ae5995`（源码树与 main `4b0b6d94b` 相同） | `4ef7be248` | 最终耗尽时，把最后一个可重试上游给出的有效 `Retry-After` 当作已知恢复时间 | 可作为 0.4.5 候选的组成部分 |
| `fix/review-p2-045`（ε） | `4ef7be248` | `10f2edd6b` | 审查遗留的 P2：raw Chat `error` 判据、心跳只提交网关自身的 SSE 头、首写与取消的竞态、`lastResortGroups` 过期清理、首个内容后免锁、重试建议按需解析、能力原因只在主因时追加 | 可以并入 |
| `feat/w12-gateway-guards-045`（δ） | `4b0b6d94b` | `50053a8ea` | W1.2 其余三条路径：OpenAI Responses 透传的首输出截止、停滞检测、有界暂存、输出后断流的计费；OpenAI Responses（透传与普通路径）心跳后命中错误透传规则时的终止帧；Gateway 入站经 Anthropic 上游的 Responses 与 Chat 推迟开场、明确终止；集成时补的 INTEG-P2-2（含从 ε cherry-pick 的两个依赖提交；INTEG-P2-1 经 PM 裁定撤回，透传心跳的响应头保持 0.4.4）；透传 turn-state 溯源只在响应提交前记录 | 可以并入（`50053a8ea`；DELTA-P1-1、P1-2 已关闭，INTEG-P2-1 按 PM 裁定作为已知差异关闭） |
| O3（β，实现中） | — | — | 原生 Responses 第一次心跳时带上这一次尝试的 `x-codex-turn-state` 并记录溯源（PM 2026-10-21T16:30Z 纳入） | γ 独立审查 |
| `docs/045-prep`（DOCS） | `a1cf8f104` | 本变更 | OpenSpec、现行文档中 ε 与 δ 没覆盖的部分 | — |

行为细节见 [design](design.md)，规格增量见 `specs/`。

## 不做

- 份额 trace 表的峰值保留、按模型维度的仅流式学习（A-P2-8）、学习记录的指纹算法（A-P2-3）：都需要先设计。
- 0.5.0 批次的其他项：多主机容量切换（W2.2）、健康感知调度（W3）、缓存友好的输出后切换（W4）、按响应头与自适应的首输出截止（W7）、W8 的请求与尝试表。
- 两处基线既有的测试竞态已在本期顺带修掉，都只改测试：`gin.SetMode`（ε `10f2edd6b`），handler 测试夹具的设置仓库（α 在集成分支 `c906ba5ca`）。
- 生产部署：需要单独授权。
- 审查提出、不阻断 0.4.5 的后续留到 0.4.6（tasks §7、design §8）：turn-state 溯源改在响应头真正发出时记录；WS 状态绑定表的清理吞吐（LEAK-P2-1）；堆阈值用例的稳定化。原生 Responses 心跳后拿不到 turn-state 一项已提前到 0.4.5，用 O3 处理。

## 影响

- **设置**：没有新增字段。δ 的新路径沿用 `stream.prelude_staging` 与 `stream.strict_terminal`，RA-1 与 ε 的重试建议改动受 `errors.failed_frame_retry_after` 控制；关闭对应开关回到 0.4.4 的行为。不受开关控制的几项见 design §4。
- **迁移、计数、管理接口、前端**：无。
- **日志**：新增 Info 级 `gateway.responses.client_disconnected`、`gateway.cc.client_disconnected`（δ）。
- **上游文件钩子**：各分支已在 `tools/upstream-sync/policy.json` 登记 required_literals 与具名补丁测试，集成时合并。
- **计费**：Responses 透传与 Gateway 入站经 Anthropic 上游的流在输出后异常结束时，按已读到的用量计费一次（以前透传不记）。
- **客户端可见**：RA-1 场景的失败帧重新带重试时间与秒数；Gateway 入站经 Anthropic 上游的 Responses 与 Chat 在正文前失败可以换号，输出后失败以协议终止帧收尾；Responses 透传在首输出截止前只有开场时换号；兼容转换与 raw Chat 的心跳提交 200 时不再带失败账号的上游头（Responses 透传除外，保持 0.4.4，见 design §3.2）；raw Chat 分块里的占位 `error`（空串、`{}` 等）不再判为失败；原生 Responses 在首字前发过心跳时，客户端也能拿到这一次尝试的 `x-codex-turn-state`（O3，不受开关控制）。
