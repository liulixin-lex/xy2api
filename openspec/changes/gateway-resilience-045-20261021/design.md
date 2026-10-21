# 设计：网关韧性 0.4.5

本文汇总三条本地分支的行为，依据是各分支的提交与仓库外的变更登记（`contract/CHANGES_A.md` 的 0.4.5 部分、`CHANGES_EPSILON_045.md`、`CHANGES_DELTA_045.md`）和审查报告（`review/CODE_REVIEW_045_RA1.md`、`_EPS.md`、`_DELTA.md`）。0.4.4 的设计见 [`gateway-resilience-v2-20261021/design.md`](../gateway-resilience-v2-20261021/design.md)，本文只写变化。

## 0. 原则

- 沿用 0.4.4 的原则：先判语义再决定重放；只有尚未实际写出、客户端未断开且请求未取消时才换号；内容拒绝、`incomplete`、合法空回答与只有 usage 的回答不重放；失败尝试的用量不混入计费。
- 不新增设置。新路径挂在已有开关下，关闭后回到 0.4.4 的行为。
- 每项行为改动先写失败用例，确认在旧代码上失败，再修。

## 1. RA-1：上游给出的等待时间算作已知恢复时间

分支 `fix/retry-advice-upstream-wait-20261021`，α `4ef7be248`；ε 的 `2cec5c547` 改为写帧时才解析（§2.6）。

- **0.4.4 的缺陷**：handler 复制上游 `Retry-After` 的分支（`copyFailoverRetryAfter`）没有把这个等待时间交给 RetryAdvice；provider 只看本地快照，又跳过本请求已失败的账号，结果是 `unknown`。合成的 `response.failed` 因此不带重试时间，还删掉了本地的 “try again in Ns”。
- **修复**：最终耗尽写 `response.failed` 时，provider 给不出答案（不是 Suppress、秒数为 0）、请求没有取消、`errors.failed_frame_retry_after` 开启，就用最后一个可重试上游给出的 `Retry-After`：
  - 来源只认原始状态为 429 或 5xx 的上游；400、404 即使被旧 handler 映射为 502 也不采用；
  - 取值为正整数秒，或未来的 HTTP 日期（向上取整），夹到 2–10 秒，`Source=recovery`；
  - 缺失、非法、非正、已过期的值，以及网关自己的缺省 30 秒，都不采用；只认最后一个上游，最终上游缺失或非法时清掉来源；
  - 优先级不变：还有健康账号时的 1 秒、能从快照算出的恢复时间、终态抑制（回合封顶、用户自身余额、内容拒绝）都先于它。
- **一致性**：同一个请求状态让更早创建的 service writer 也拿到最终的建议；帧内 `error.headers.retry-after` 与正文秒数由同一个 writer 生成，保持一致。
- **范围**：只影响已提交的 Responses 流最终写出的 `response.failed`。Chat、Messages、尚未提交的普通 JSON 出口与开关关闭时保持 0.4.4（关闭时正文与 0.4.3 相同，RA-1 场景里仍是 “try again in 8s”）。不读数据库或 Redis，不改容量惩罚，无新设置、事件、错误码。
- **审查**：可作为 0.4.5 候选的组成部分，无 P0、P1；发布前要补文档（本变更）并在新候选上做完整验证，含测试服务器 RA-1 复跑。

## 2. 审查遗留的 P2（ε）

分支 `fix/review-p2-045`（基于 `4ef7be248`），HEAD `10f2edd6b`，12 个提交（最后一个只改测试：把唯一调用 `gin.SetMode` 的并行用例改为顺序执行，修掉基线既有的偶发竞态）。

### 2.1 raw Chat 的 `error` 判据（R2-P2-2：`006be0637`；最终口径 `b8920cc89`）

- 受 `stream.strict_terminal` 控制。Chat 分块里的 `error` 只有以下情况算失败：
  - 非空字符串（去掉首尾空白后非空）；
  - 对象里有非空的 `message`、`code` 或 `type`（字符串去空白后非空，数字非 0）；
  - 根上 `type == "error"`。
- `null`、空串或只有空白的字符串、`false`、`0`、`{}`、所有字段都为空的对象不算，分块按正常内容透传。`true` 与非 0 数字这类顶层取值目前也不算。
- 判据集中在 `resilience.ChatErrorField(root)`，raw Chat 的 `ObserveDataLine` 与 `ClassifyStreamEvent("chat", …)` 共用；上游文件 `openai_raw_stream_truncation.go` 的调用点补登 required_literal（`4931a5e2d`）。

### 2.2 心跳只提交网关自身的 SSE 头（R2-P2-1：`db86d3377`）

- 不受开关控制。第一个输出之前用心跳提交 HTTP 200 时，只写 `Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`、`X-Accel-Buffering: no`，不写这次尝试经过过滤的上游头（如 `x-request-id`）。换号成功后，客户端不会拿到失败账号的头。
- 覆盖 Chat 与 Messages 兼容转换的心跳（暂存期；关闭暂存时上游第一行之前），以及 raw Chat 严格模式下首个 data 之前透传的上游注释心跳。没有心跳、直接输出时，照旧带上游头。做法与原生 Responses 的 `applyAttemptResponseHeaders` 一致。

### 2.3 首次写出与客户端取消同时发生（R2-P2-4：`2a67cbf88`；EOF 分支 `930213caa`）

- 只在 `stream.prelude_staging` 开启时存在这个竞态（取消回调只在暂存时注册）。客户端在第一次写出的同时取消，取消回调先关闭了上游响应体；随后的读错误或缺终态的 EOF，以前被当成上游断流，账号记失败。
- 现在取消回调关闭响应体之前先置位“已由网关关闭”的标志；之后的读错误或 EOF 按客户端断开收尾：按已得用量计费，不报账号失败，不记输出后断流。
- 判据用“网关确实关过响应体”，而不是 `ctx.Err()`：首写已停掉回调、客户端之后才取消时，上游仍在被读，此时的读错误是真实断流，照旧归咎账号。

### 2.4 末位兜底分组快照的过期清理（泄漏排查 P2：`75fa06e97`）

- 不受开关控制，纯内存回收。写入新快照前、以及会话标记循环每分钟一次，删掉过期条目；分组的 `[]Account` 副本不再常驻内存。过期条目对 RetryAdvice 等同于没有快照。

### 2.5 首个内容之后免锁（R1 P2-14：`893280f62`）

- 不受开关控制，纯性能。`RequestState` 增加原子的“首个输出已记录”标志；之后每次写出不再读时钟、不再拿锁。

### 2.6 重试建议只在写失败帧时解析（R1 P2-10：`2cec5c547`）

- 只在 `errors.failed_frame_retry_after` 开启时有 provider。以前每个无号或耗尽出口都同步解析一次（最多 100 ms），以普通 JSON 结束的出口用不到结果。
- 现在出口只读取 RA-1 的上游等待（证明它的 `Retry-After` 头随后会被改写，必须当场取）并安装 provider；真正解析只在写 `response.failed` 时发生，写帧后把结果固定为本请求的最终建议。帧的内容不变。
- 与 RA-1 原实现的唯一差异：显式写入的 `unknown` 不再被提升；写帧之前的显式写入全部是 Suppress（能力、配置拒绝、回合封顶等），审查已逐个核对。

### 2.7 能力原因只在它是主因时追加（A-P2-4：`ca7c67dc7`）

- 不受开关控制，只改诊断文案。无号错误的 `(capability_stream_only)` 后缀，改为本请求失败的尝试全部是仅流式排除时才追加，与 0.4.4 的 `IsCapabilityOnlyExhaustion` 同一规则。客户端看到的状态码、消息与重试头不变。

### 2.8 文档登记（R2-P2-3：`9f1c57f72`）

- 0.4.4 已在 `GATEWAY_RESILIENCE.md` §6 登记 raw Chat 取消后的截断；ε 补一句：严格终止下，取消后收到的 `error` 也不记上游失败、不换号。

## 3. W1.2 其余三条路径（δ）

分支 `feat/w12-gateway-guards-045`（基于 `4b0b6d94b`），HEAD `74133b338`，10 个提交。

### 3.1 Gateway 入站经 Anthropic 上游的 Responses 与 Chat（`bd960dc5a`、`c95aa4e7c`、`8a7fa67ea`、`9f8f5ba6c`）

两条转换共用驱动 `service/gateway_convert_stream.go`，上游文件只留转换闭包与钩子。

- `stream.prelude_staging`（开）：
  - `response.created`、Chat 的 role 分块推迟到第一个内容或终态，判据是 `resilience.ClassifyStreamEvent("messages", …)`；工具调用作为首个事件时立即释放；
  - 此前的可重试失败（流内 `overloaded_error` 等、EOF、读错、停滞）换号，客户端只看到成功那次尝试的开场；
  - Anthropic 首事件截止（`budget.anthropic_first_event_timeout_seconds`）延长到第一个内容，也覆盖等待响应头；
  - 暂存期的上游 `ping` 与注释行、以及按 `gateway.stream_keepalive_interval` 的保活，立即写成注释心跳，计为心跳字节。
- `stream.strict_terminal`（开）：
  - 上游流内错误不再被丢弃；
  - 输出后断流、读错或停滞：Responses 以 `response.failed` 收尾（此前什么都没写时先补 `response.created`），Chat 以 `data: {"error":…}` 后跟 `[DONE]` 收尾，不再合成成功；
  - `message_delta` 已带 `stop_reason`、只缺 `message_stop` 的流视同完成；
  - 合成的 `response.failed` 按 `RetryAdviceEligible` 决定是否带重试时间；
  - 启用该路径的停滞检测（输出前由首事件截止接管）。
- 不重放：不可重试的流内错误（请求过长、内容拒绝、余额等）、`max_tokens` 的 incomplete、refusal、合法空回答与只有 usage 的回答。strict 关、staging 开时，输出前被丢弃的非重试错误之后的 EOF 也不重放（DELTA-P2-1）。
- 两个开关都关：与 0.4.4 逐行等价。

### 3.2 OpenAI Responses 透传（`e3f902390`、`5f5386f4a`、`87f9b9356`、`6420bc73c`、`74133b338`）

- `stream.strict_terminal`（开）：
  - 首输出截止与原生路径相同（请求预算下按推理档位，否则 `gateway.openai_first_output_timeout_seconds`），同时约束等待响应头；截止前只有开场事件与心跳时换号（原因 `first_output_timeout`，阶段 `semantic_output` 或 `response_headers`）；
  - 读取循环驱动截止、停滞检测与暂存期心跳。停滞：输出前由截止接管，没有截止时换号；输出后以 `response.failed`（`stream_timeout`）收尾；
  - 暂存期心跳由读取循环写 `:`，计为心跳字节；写失败按客户端离开处理（不换号，截止到期也不换号，内容随后到来时停表并读完用量）；
  - 上游注释行在暂存期立即透传，不再随开场扣留；
  - 第一个输出前请求取消：关闭上游，返回非换号错误；输出后断开：读完上游。
- 不受开关控制：
  - 第一个输出前的暂存改用原生路径的有界暂存（8 MiB 上限，超过 64 KiB 写入已解除链接的临时文件），首个输出时整体写出；超出上限时换号。以前是不设上限的内存切片。
  - 输出后断流（`response.failed`、缺终态、读错、停滞）按已读到的用量计费一次，与 Responses handler 处理普通路径部分结果的做法相同（#5148）；以前透传在任何流式错误时都丢掉结果、不记用量。第一个输出前取消与换号不带结果，不会重复计费（审查 DELTA-P2-5，`87f9b9356`）。
  - 流内 `response.failed` 在第一个输出前命中管理员配置的错误透传规则时：如果心跳已提交 200，停掉保活，按规则的类型与消息写 `response.created` 后跟 `response.failed`（同一个 id），是否带重试提示按规则的状态码与 `RetryAdviceEligible`；尚未提交时仍按规则返回状态码与 JSON。以前会在注释行之后接一段裸 JSON（审查 DELTA-P1-2，与 0.4.4 的 R2-P1① 同类，`6420bc73c`）。普通（非透传）OpenAI Responses 路径有同样的问题，按同样方式修复，两条路径共用 `writeOpenAIStreamRuleError`，暂存的上游开场不会释放（PM 指令 5，`74133b338`）。
- 关闭开关：不设截止与停滞检测，心跳仍由独立协程写出，注释随开场暂存（0.4.4 行为）。

### 3.3 计费与客户端离开

- 输出后异常结束（流内错误、缺终态、读错、停滞，含客户端断开后上游再出错）：handler 按已读到的用量计费一次，与原生 Messages 相同；换号错误不带结果，不会重复计费（反向转换器 DELTA-P1-1 `c95aa4e7c`，停滞出口的用例 DELTA-P2-6 `ab227b00f`；Responses 透传 DELTA-P2-5 `87f9b9356`）。
- 暂存期请求取消：关闭上游，不换号、不计费、不报账号失败，记 Info 级 `gateway.responses.client_disconnected` 或 `gateway.cc.client_disconnected`（DELTA-P2-3）。
- 暂存期心跳写失败、请求未取消：与 0.4.4 兼容路径（PM7）一致，不换号，结果标记客户端断开，读完上游，不报账号失败；是否向用户计费交给既有的送达与未送达规则（DELTA-P2-2，`9f8f5ba6c` 的对照用例）。
- 输出后客户端断开：不关上游，读完并按用量计费。

## 4. 开关与回退

| 开关 | 0.4.5 新增覆盖 | 关闭后 |
|---|---|---|
| `stream.prelude_staging` | Gateway 入站经 Anthropic 上游的两个反向转换器推迟开场；ε 的首写与取消竞态只存在于暂存开启时 | 反向转换器收到 `message_start` 就写开场（0.4.4） |
| `stream.strict_terminal` | 反向转换器的明确终止与停滞检测；Responses 透传的首输出截止、停滞检测、读取循环心跳；raw Chat `error` 新判据 | 反向转换器 EOF 合成为成功；透传不设截止，注释随开场暂存；raw Chat 的 `error` 后跟 `[DONE]` 当成功（0.4.4 及以前） |
| `errors.failed_frame_retry_after` | RA-1 的上游等待回退；重试建议只在写帧时解析 | 合成的 `response.failed` 不带 `error.headers`，正文与 0.4.3 相同 |

不受开关控制（协议正确性、计费、内存与性能）：心跳只提交网关自身的 SSE 头（§2.2）；`lastResortGroups` 过期清理（§2.4）；首个内容之后免锁（§2.5）；能力原因后缀（§2.7）；Responses 透传的有界暂存与输出后断流计费，以及 OpenAI Responses（透传与普通路径）心跳后命中错误透传规则时的终止帧（§3.2）。

回退到 0.4.4：无迁移，无新设置字段，直接换回镜像。

## 5. 上游钩子与登记

- 各分支已在 `tools/upstream-sync/policy.json` 登记：
  - RA-1：handler 的上游等待记录与 provider 安装（ε `2cec5c547` 把字面量改为 `installRetryAdviceProvider(c, diag, apiKey, model, upstreamRecoverySeconds(c))`）；
  - ε：`preludeBodyClosed.Store(true)`、`if canceledAfterOutput() {`、`writeStableStreamHeaders(c)` 等字面量，`openai_raw_stream_truncation.go` 的 `resilience.ChatErrorField(root)`；具名补丁 `xy-resilience-v2-streams`、`xy-resilience-v2-handler-exits` 新增测试；
  - δ：`gateway_forward_as_responses.go`、`gateway_forward_as_chat_completions.go` 各 4 条，两个 Gateway handler 各 2 条，`openai_gateway_passthrough.go` 6 条，另有 `87f9b9356` 补登的透传计费钩子；`xy-resilience-v2-streams` 7 条、`xy-resilience-v2-handler-exits` 2 条测试绑定。
- 各分支在干净临时 worktree 上的 `sync.py audit` 与 `scheduler-diff` 都已通过；集成后要在合并结果上重跑，并核对两边的 required_literals 都还在。

## 6. 集成说明

- 顺序：ε 已包含 RA-1，合入 ε 即带上 RA-1；δ 基于 main，独立合入；最后合入本文档分支。
- 已知冲突点（审查已在临时文件上逐个文件三方合并核实）：
  - 两个 Gateway handler（`gateway_handler_responses.go`、`gateway_handler_chat_completions.go`）RA-1 与 δ 都改过，但改动不相交，能自动合并；
  - `docs/GATEWAY_RESILIENCE.md` 第 18 行（§1“可重放前缀”）与第 133 行（§4.2 `stream.strict_terminal`）ε 与 δ 都改过，需要手工合成一行，两边的增补都保留；
  - `tools/upstream-sync/policy.json` 的同一具名补丁（`xy-resilience-v2-streams`、`xy-resilience-v2-handler-exits`）两边都新增了测试；
  - `docs/PROJECT_MEMORY.md`：RA-1 与 ε 分支基于 0.4.4 发版前的记忆，与 main 的收尾版本冲突，以 main 为准，只保留分支里属于本任务的状态。
- 本文档分支只改 ε 与 δ 没碰过的位置：`GATEWAY_RESILIENCE.md` 开头一段、§3 表中“排队等待时已发出 ping”之后新增一行、§7 的 RA-1 条目；旧变更 runbook §6；0.4.4 变更 design §11 与 tasks 5.6 的指引。

## 7. 验证计划

- 集成后在唯一候选上跑全量门禁：gofmt、四种 vet、golangci-lint、unit 与无 tag 测试、`ci_gate.py`（`-race`，零跳过）、`govulncheck`、前端四项与依赖审计、`tools/tests`、文档一致性、scheduler-diff、干净树 `sync.py audit`。
- 测试服务器：0.4.4 的 71 个场景全部复跑（RA-1 必须通过），加上 δ、ε 建议的新场景（仓库外变更登记的建议清单，由 QA 定编号）：
  - Gateway 入站经 Anthropic 上游：`message_start` 后流内 `overloaded_error` 换号；输出后断流以终止帧收尾并按用量计费一次；`message_start` 后静默超过首事件截止换号；`invalid_request_error` 不重放；`message_delta(end_turn)` 后直接 EOF 正常完成；两个开关关闭与 0.4.4 一致；
  - Responses 透传：created 后静默超过首输出截止换号；响应头不来时换号；输出后停滞以 `response.failed` 收尾；中转注释立即可见；暂存期客户端断开不换号；
  - 心跳之后换号成功的兼容 Chat 与 Messages 请求，响应头里没有失败账号的 `x-request-id`；
  - raw Chat 上游在正常分块里放 `"error": ""` 时正常完成。
- 升级演练 0.4.4 → 0.4.5 → 0.4.4 → 0.4.5；3 小时浸泡。验证结果写入 tasks。

## 8. 已知差异与延后

- 心跳先提交 200 时，成功尝试的上游过滤头不再写出，客户端拿到的头会因首字快慢而不同（DELTA-P2-4，与 §2.2 同向，记为已知差异）。
- raw Chat 的顶层 `"error": true` 与非 0 数字目前不算失败。
- 基线既有的 handler 测试夹具竞态见 proposal“不做”。
