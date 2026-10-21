# 网关韧性第二期设计（0.4.4）

> 基线：`release/0.4.4` `027a434ad`（origin/main `3092629d7` 即 0.4.3 加文档，合入 PR #117 与 #116）。
> 范围与用户决定见 [`proposal.md`](proposal.md)；0.4.0 的设计见 [`gateway-resilience-20261015/design.md`](../gateway-resilience-20261015/design.md)，下文“旧 design §x”指它。
> 文件路径相对 `backend/`。
> 标【待契约】的地方等各实现方的最终变更登记后定稿。

## 0. 原则

1. **能藏就藏。** 写出第一个不可重放的输出之前，保留完整的换号能力；所有流式路径用同一个判据判断“已写出的是不是可重放前缀”（§3）。
2. **先判语义，再决定重放。** 合法空回答、`response.incomplete`、内容拒绝、客户端取消、已收到终态之后的 EOF，都不能因为“还没输出文本”就重放。
3. **不改变新会话放置与粘性规则。** 本期只改失败后的处理与信号；健康偏好、冷却加长、换号目标偏好都不在本期。会改变少量流量的只有：分类修正（§5）、能力过滤（§6）、更多失败在请求内换号成功（§3）。
4. **开关与回退。** 总开关保持原义：关闭后按 0.3.0 运行。本期每个子开关关闭后回到 0.4.3 / #117 的行为。新开关与总开关、所在段的旧开关是 AND 关系（§1）。协议正确性修复默认不受开关控制，登记在 §9。
5. **自有模块，最少钩子。** 判据与类型放在 `internal/resilience`；上游文件只加钩子，登记到 `tools/upstream-sync/policy.json`。

## 1. 开关矩阵

开关、门控方法与范围以 §2（C0 契约，2026-10-21T01:32Z 冻结）为准，缺省全部开启。“关闭后”指回到共同基线 `027a434ad`（0.4.3 加 #117、#116）的行为。

| 行为 | 开关（缺省） | 门控要求 | 关闭后 |
|---|---|---|---|
| W1.1 兼容转换推迟开场 | `stream.prelude_staging`（开） | 总开关 | 收到上游 `response.created` 就写开场 |
| W1.1 原生 Responses 白名单扩展 | `stream.native_prelude_whitelist`（开） | 总开关 | 开场只认 `response.created`、`response.in_progress`、`keepalive` |
| W1.2-lite raw Chat 识别 `error` 后跟 `[DONE]` | `stream.strict_terminal`（开） | 总开关 | 当成功处理 |
| W2.1-lite 分类修正 | `failure.decision_v2`（开） | 总开关 | 旧分类，含 `RequestScopedTransient` → `CAPACITY_SHED` |
| W5.1 三个专用耗尽出口接入重试提示与回合封顶 | 现有 `errors.enabled` | 总开关 | 基线行为 |
| W5.2 排队 ping 后写协议终止帧 | 无 | — | 不受开关控制（§9） |
| W5.3 `response.failed` 有条件带 retry-after | `errors.failed_frame_retry_after`（开） | 总开关与 `errors.enabled` | 不带 `error.headers`，正文沿用旧 design §7.6 |
| W5.4 Anthropic 自身失败用 `api_error` | 现有 `errors.enabled` | 总开关 | 基线类型 |
| W5.5 Codex 无号 503 的 Retry-After 上限 | `errors.codex_retry_after_cap_seconds`（10；0–30；0 表示关闭） | 总开关与 `errors.enabled` | 2–30 秒 |
| W6-lite 仅流式学习与过滤 | `capability.stream_only_learning`（开）；有效期 `capability.learn_ttl_seconds`（21600；600–86400） | 总开关 | 不学习、不过滤 |
| W6-lite 检测到后本请求换号 | 无单独开关 | `stream_only_learning` 与 `decision_v2` 都开 | 只学习、过滤后续请求，本请求照旧返回 400 |
| W8-lite 事件字段 | 无 | — | 只加日志字段，不改行为 |

存量状态：

- `capability.stream_only_learning` 或总开关关闭时，不学习、不过滤；已有的学习记录不删除，按自己的到期时间自然过期，重新开启后未到期的记录继续生效。
- 修改 `capability.learn_ttl_seconds` 只影响之后新学到的记录，不改写已有记录的到期时间。

## 2. 设置（C0）

### 2.1 字段

| JSON 路径 | Go 字段 | 门控方法（`*resilience.Settings`） | 缺省 / 范围 |
|---|---|---|---|
| `stream.prelude_staging` | `Stream.PreludeStaging` | `PreludeStagingOn()` | true |
| `stream.native_prelude_whitelist` | `Stream.NativePreludeWhitelist` | `NativePreludeWhitelistOn()` | true |
| `stream.strict_terminal` | `Stream.StrictTerminal` | `StrictTerminalOn()` | true |
| `failure.decision_v2` | `Failure.DecisionV2` | `DecisionV2On()` | true |
| `errors.failed_frame_retry_after` | `Errors.FailedFrameRetryAfter` | `FailedFrameRetryAfterOn()` | true |
| `errors.codex_retry_after_cap_seconds` | `Errors.CodexRetryAfterCapSeconds` | `CodexRetryAfterCapOn()` | 10；0–30；0 关闭 |
| `capability.stream_only_learning` | `Capability.StreamOnlyLearning` | `StreamOnlyLearningOn()` | true |
| `capability.learn_ttl_seconds` | `Capability.LearnTTLSeconds` | 跟随 `StreamOnlyLearningOn()` | 21600；600–86400 |

- 新增两个顶层段 `stream`、`capability`；`failure`、`errors` 段各加字段。
- 门控方法对 nil 安全，都要求总开关 `enabled`；两个 `errors` 门控还要求 `errors.enabled`。
- `StreamOnlyFailoverOn()` = `StreamOnlyLearningOn() && DecisionV2On()`，只管检测到“只接受流式”后本请求是否换号。

### 2.2 存放、管理接口与热更新

- 沿用 `settings` 表的单键 `gateway_resilience_settings`，在同一份 JSON 里扩展；不新增设置键、迁移或单独的扩展文档。
- `resilience.Parse` 先取缺省值再 `json.Unmarshal`：旧文档缺少的新字段取缺省值，显式的 false 与 0 保留。
- 管理接口 `GET/PUT /api/v1/admin/settings/gateway-resilience` 的路径与形状不变：GET 返回 `data: {settings, defaults}`，两份都含新字段；PUT 的请求体是设置对象本身，缺项保留当前值，未知字段仍拒绝，数值越界返回 `GATEWAY_RESILIENCE_SETTINGS_INVALID`。
- 热更新不变：保存的实例立即生效，其他实例每 10 秒重新读取；epoch 防止旧读取覆盖新保存。

### 2.3 回退到 0.4.3

依据 0.4.3 的源码（`3092629d7`）：

| 位置 | 事实 |
|---|---|
| `backend/internal/resilience/settings.go:16` | 存储键 `gateway_resilience_settings` |
| `backend/internal/resilience/settings.go:306`、`:311` | `Parse` 用 `json.Unmarshal` 读存储，**忽略未知字段**，不是严格解码 |
| `backend/internal/service/resilience_service.go:224`、`:257` | 启动与周期读取经 `Parse`；`UpdateSettings` 整体编码后保存 |
| `backend/internal/handler/admin/resilience_handler.go:38`、`:51` | 严格解码（`decodeStrictJSON`）只用于管理端 PUT |

结论与边界：

- 0.4.3 能正常读取 0.4.4 保存的设置：新段与新字段被忽略，原有字段取保存的值，行为与 0.4.3 一致。
- **在 0.4.3 上再次保存设置会丢弃新字段**（0.4.3 整体编码它认识的结构）。之后再升级到 0.4.4 时，新字段回到缺省值（全部开启、上限 10 秒、有效期 6 小时）；回退期间关掉过的新开关需要重新关闭。
- 回退后，仍打开着 0.4.4 管理页面的浏览器提交设置时会带上新字段，被 0.4.3 的严格 PUT 拒绝；刷新页面，用随 0.4.3 发布的前端即可。这不影响服务启动与读取已保存的设置。
- 回退安全由 `settings_compat043_test.go` 证明：用冻结的 0.4.3 结构、缺省、解析与校验读取新文档，逐字段比较；并覆盖“0.4.3 保存后再升级”和“严格 PUT 拒绝新字段”。

## 3. W1.1 可重放前缀

### 3.1 事件分类

纯函数 `resilience.ClassifyStreamEvent(protocol, eventType, payload)`（`internal/resilience/prelude.go`）对一个完整的 SSE 事件返回：

| 取值 | 含义 |
|---|---|
| `Prelude` | 开场事件，可以丢弃后由新尝试重放 |
| `Content` | 不可重放的输出 |
| `TerminalSuccess` | 正常终态 |
| `TerminalFailure` | 失败终态；`Retryable` 表示可以换号（错误分类复用 `Classify`） |
| `Unknown` | 按 `Content` 处理，保守 |

开场事件：

| 协议 | `Prelude` |
|---|---|
| Responses | `response.created`、`response.in_progress`、`response.queued`、`keepalive`；不带内容的 `response.output_item.added`（reasoning 项无 `encrypted_content` 且摘要为空，message 项的文本为空）、`content_part.added`、`reasoning_summary_part.added` |
| Chat | 只带 role 的 chunk；空 content 增量 |
| Messages | `message_start`、`ping`；空 text/thinking 的 `content_block_start` |

依据生产探测（2026-10-21，去标识）：

- 多数中转在第一个 `response.output_text.delta` 之前依次发 `response.created`、`response.in_progress`、空的 message `output_item.added`、空的 output_text `content_part.added`，都在上表内。
- `codex.response.metadata` 没有观察到，`response.metadata` 出现在正文之后，二者都**不**加入开场；`codex.*`、rate_limits 一类不按名字通配放行，仍按 `Unknown` 即内容处理。
- added 结构只在严格确认已知字段为空时算开场；带正文、工具参数、加密载荷或未知非空字段的，一律算内容。
- 部分中转在 `response.created` **之前**先发一个 delta。中转伪保活帧的判据（旧 design §7.8，`resilience_relay_keepalive.go`）泛化：
  - `response.created` 之前、`delta` 明确为空字符串或只有空白的 Responses delta：丢弃，算上游存活、不算输出，计入既有的 `relay_keepalive`；
  - `delta` 缺失、为 null 或不是字符串的不丢；`response.created` 之后的普通空 delta 照常转发；带 `SSE-Keep-Alive` 标记的帧行为不变；
  - `response.created` 之前的非空 delta 是真实内容，立即输出（网关不重排上游的真实内容），计数 `pre_created_delta`（按分组、账号、平台），并记 info 事件 `resilience.pre_created_delta`（`group_id`、`account_id`、`bytes`，只记 UTF-8 字节数，不记内容）；
  - 过滤器按行有界解析：超过 4 KiB 的行原样透传，并保守地停止识别之后的空帧（这一行可能就是 created），不冒险丢真实数据。
- 带标记的伪保活帧仍只受总开关控制（旧行为）；泛化部分受 `stream.native_prelude_whitelist`（`NativePreludeWhitelistOn()`）控制，关闭时只识别带标记的帧。实现提交 `bdbca7197`。

终态：Responses 的 `response.completed`/`response.done` 为成功，`response.failed`/`error` 为失败，`response.incomplete` 为不可重试的失败终态；Chat 的 `[DONE]` 为成功，带 `error` 的 chunk 为失败；Messages 的 `message_stop` 为成功。

### 3.2 判断顺序

暂存期内收到失败或连接结束时，依次判断：

1. 客户端已取消或请求已被拒绝：结束，不重放。
2. 终态语义：合法空回答（只有 usage）、`response.incomplete`、内容拒绝，照常结束，不重放。
3. 是否已有不可回滚的写出：已写出 `Content` 的，按已输出后失败处理（旧 design §7.6）。
4. 失败分类（§5）。
5. 是否可重试：可重试的转为 `UpstreamFailoverError`，进入现有换号循环，受请求预算约束。

### 3.3 原生 Responses 白名单（`stream.native_prelude_whitelist`）

- 改动位置：`openai_gateway_passthrough.go` 的 `openAIStreamEventIsMetadata` 一带，普通路径的暂存与透传的 pendingLines 共用 §3.1 的判据（上游钩子 `resilience.ClassifyStreamEvent("responses", eventType, []byte(trimmed))`，登记到 `policy.json` 的 required_literals）。
- 开关关闭时回到旧的首输出边界。`response.queued` 可以暂存；`codex.response.metadata`、`response.metadata` 与未知事件不在白名单，到达即释放暂存，并按原顺序转发。
- 送达追踪 `resilience_delivery_sse.go` 的开场名单也改用 §3.1；送达计费口径不变：开场不算送达。
- **严格 SDK 约束**：换号后只转发新账号的 `response.created`，旧尝试的开场全部丢弃、不拼接；response id 与序号沿用新尝试的值。客户端看到的第一个事件始终是 `response.created`，id 不变。

### 3.4 兼容转换推迟开场（`stream.prelude_staging`）

实现提交 `1a97eebd1`（β）。

- 适用：Chat 与 Messages 入站、Responses 上游的两条转换（`openai_gateway_chat_completions.go` 的 `handleChatStreamingResponse`、`openai_gateway_messages.go` 的 `handleAnthropicStreamingResponse`），由 `PreludeStagingOn()` 控制。
- 开场事件是网关合成的，所以在转换器状态里推迟生成 role chunk 或 `message_start`，等首个 `Content` 或终态到来时一并按序写出。暂存期只保留 ID、model 与 service tier，不做字节缓冲；空的 reasoning added 在真正的推理 delta 或 signature 到来时再重建块结构。工具调用的标识本身就是内容，到达即带着开场释放。
- 暂存期内上游的可重试 `response.failed`、`error`、EOF、读错，在写出任何字节之前返回 `UpstreamFailoverError` 进入换号（§3.2），失败尝试的用量不混入计费；客户端只看到最终成功那次尝试的一个开场。迟到的 created 不改变已经输出的 ID。
- 取消优先：暂存期内客户端取消就关闭上游，返回取消错误，不计费、不上报账号失败、不记 Warn（审查 R1-P1-1；handler 与调度上报对取消的跳过由 α、γ 落实）；第一次实际写出正文之后取消，回到原有的 drain 机制，继续读完并按已得用量计费（审查 P0）。【待契约：β、α、γ 的修复提交】
- 首事件守卫（`resilience_compat_stream.go` 的 `armOpenAICompatFirstEvent`）延长到首个内容或终态；中转伪保活帧仍按“存活”处理（旧 design §7.8）。
- `clientOutputStarted` 按实际写出的字节置位：短写或部分写出之后不再重放。
- **暂存期写注释心跳**（PM 2026-10-21T02:42Z 改判方案 U4）：按 `stream_keepalive_interval` 写 `:\n\n`，与原生 Responses 的 `stageFirstOutput` 期间（基线 `openai_gateway_response_handling.go` 约 1000–1012 行）和基线一致。方案原推荐“不提交”，依据是“原生不提交”，这个前提不成立。心跳记为心跳字节（`recordOpenAIStreamKeepaliveBytes`），不置 `clientOutputStarted`，不影响换号；心跳会提交 200，之后若耗尽，以带内终止帧收尾（旧 design §7.6）。经 CDN 的请求照旧另有 handler 的首包前保活。【待契约：β 的提交】
- 开关关闭时保留旧的“首行”守卫与流内心跳。
- 与 64 KiB 以上请求的静默拒绝缓冲（`openai_silent_refusal.go`）叠加：先推迟生成开场，释放后再交给旧的探测器，原有的 pendingSSE 缓冲仍由它维护。只有 usage 的合法空回答正常结束；`response.incomplete` 不会因结果为空误触发静默拒绝的重放；原有“无 usage 的静默拒绝”规则不变。
- 兼容路径 `first_token_ms` 的取点不变（上游第一条数据），避免口径跳变。
- 上游文件钩子（`policy.json` 的 required_literals，α 登记）：`openai_gateway_chat_completions.go` 的 `deferredOpenAIChatChunks`、`canReplayPrelude`；`openai_gateway_messages.go` 的 `deferredOpenAIAnthropicEvents`、`canReplayPrelude`。

## 4. W1.2-lite raw Chat 明确终止（`stream.strict_terminal`）

- 位置：`openai_gateway_chat_completions_raw.go`、`openai_raw_stream_truncation.go`（账号不支持 `/responses` 时的原生 Chat 透传）。
- 实现提交 `f9edc3a4d`（β），由 `StrictTerminalOn()` 控制。
- 合法 JSON 的 `error` 字段或 `type: "error"` 一出现，之后的 `[DONE]`、usage、finish 都不再把结果改回成功。
- 提交前且可重试：返回换号；提交后：原样转发错误与 `[DONE]`，按已输出后失败记账（计入会话失败），并标记终态已写出，handler 不再补一个。内容拒绝不换号；客户端取消优先，不误记为截断。
- 第一条 data 之前的 SSE 框架行（`event`、`id`、`retry`、注释）有界暂存，使 `event: error` 不会抢先提交响应；不新增 role 或内容的暂存。
- 没有 error 的流仍算成功，包括只有 usage 的、或有 finish 但没有 `[DONE]` 的。
- 开关关闭时回到旧判定：`error` 后跟 `[DONE]` 算成功。
- 上游文件钩子：`openai_gateway_chat_completions_raw.go` 的 `resilience.Current().StrictTerminalOn()`、`terminal.failurePayload`（登记到 required_literals）。
- OpenAI Responses 透传的首输出守卫【待契约：CHANGES_B 是否完成，未完成则列入 §10】。

## 5. W2.1-lite 分类修正（`failure.decision_v2`）

实现：纯规则入口 `resilience.ClassifyWithSettings(outcome, settings)`，按 `DecisionV2On()` 在新分类与原 `Classify` 之间切换；service 的 `classifyUpstreamHTTP`、`ClassifyFailoverError`、`opsResilienceCategory` 与已输出后失败（`resilience_post_output.go` 的 OpenAI、Anthropic 两处，α）都改用它，避免口径分叉。已输出后失败对会话与账号仍统一按 `TRANSIENT` 计（旧 design §7.6），不变。

根因：`service/resilience_penalty.go` 的 `ClassifyFailoverError` 把所有 `RequestScopedTransient=true` 的失败都当成 `CAPACITY_SHED`，使 Grok 空闲、raw 429、“服务忙”类文案也受“容量只换一次、必须跨主机、整主机排除”的约束。

| 信号 | 0.4.3 | 0.4.4 |
|---|---|---|
| `server_is_overloaded`、`slow_down`、529、结构化 overloaded、503 加 overloaded 文案（保留 0.4.3 的 CDN 错误页例外） | `CAPACITY_SHED` | 不变 |
| Grok 流空闲（`grok_stream_idle.go`） | `CAPACITY_SHED` | `TRANSIENT`：允许换号，不消耗容量的跨主机额度，不触发整主机排除 |
| raw Chat 与 Grok 的可重试 429 | `CAPACITY_SHED` | `RATE_LIMIT`；余额、reset、内容拒绝等更强的结构化证据优先 |
| “The service is busy”、“Too many pending requests”、“Concurrency limit exceeded for account” | `CAPACITY_SHED` | 5xx → `TRANSIENT`，429 按 raw 429 → `RATE_LIMIT`；可以换到任何主机，含同一主机 |
| typed 首输出超时 | `TRANSIENT` | 运维记录仍为 `TRANSIENT`（兼容）；Kind 已是 `first_output_timeout`，Detail 在 `timeout_ms` 旁加 `timeout_seconds`（实际生效的截止，不是配置值），Reason 为 `first_output_timeout`；OpenAI（β）与 Anthropic 首事件守卫（γ）都这样记录。handler 的 `FIRST_OUTPUT_TIMEOUT` 不变；不加列、不加迁移 |

- 运维记录的 `resilience_category` 与 handler 的换号规则用同一个分类结果，不再分叉。HTTP 错误的运维记录在构造时用 `opsHTTPResilienceCategory(status, headers, body, message)` 直接填分类（总开关关闭时为空），保留只靠响应头 reset 判定的证据；流内事件仍从结构化正文推导。
- `RequestScopedTransient` 只在 `decision_v2` 开启时不再单独推出 `CAPACITY_SHED`；关闭时与 0.4.3 相同。实现提交 `0d3e72508`。
- D18 不变：容量降载仍是同号先试一次、只跨主机换一次。
- `RATE_LIMIT` 计入会话失败，改绑可能增多；开关关闭即回到旧分类。

## 6. W6-lite 仅流式能力（`capability.stream_only_learning`）

契约：CHANGES_G“W6 契约冻结”（2026-10-21T01:57Z）。

### 6.1 范围与触发

- **请求范围**：OpenAI 入站的 Responses 与 Chat 普通文本请求。入口在解析出客户端的 `stream` 后、首次选号前用 `resilience.WithCapabilityRequest(ctx, protocol, stream)` 标记请求（α；`protocol` 为 `responses` 或 `chat`）。未标记、协议未知或 `stream=true` 的请求不过滤；compact（含路径与 native v2 两种）、带显式图片意图的请求不标记，图片端点与 WebSocket 不改（α `affef416f`）；Messages→OpenAI、Gemini、CN、Bedrock 不在本期。
- **账号范围**：只学习 OpenAI 与 Grok 平台的账号；OpenAI 强制流式的 OAuth 账号本来就以流式请求上游，不过滤。
- **触发**：检测钩子 `OpenAIGatewayService.LearnStreamOnlyFailure(ctx, account, protocol, upstreamStream, status, errorBody, outboundBodies ...[]byte)`，在处理 400 的副作用与写应答之前调用（β 的 Forward、透传、raw Chat 调用点传实际发给上游的最终请求体；Grok 原生由 γ 传改写后的请求体）。只在实际发给上游的请求为非流式、状态为 400、错误字段确定表示“只接受流式”（表驱动，如 “only accepts streaming requests”、“stream must be true”）时学习；只看错误字段，忽略上游回显的请求内容。以下情况不学：截断的 JSON、余额与内容拒绝；错误带 `param` 且不是 `stream`；出站请求体本身就含这句能力短语（不分大小写，解析 JSON 转义后比较），避免用户在提示词里写了这句话导致误学。上游请求本来就是流式时不动作。钩子还要求请求带合法的 `WithCapabilityRequest` 标记且 `stream=false`：没有标记的 compact、图片、WebSocket 完全没有学习副作用。不会自动改发流式。

### 6.2 学习与存放

- 学到的是“账号 × 上游协议 × 非流式 → 不可用”，有效期 `capability.learn_ttl_seconds`。
- 存放在账号 `extra` 的两个键：`capability_learned_responses`、`capability_learned_chat`，值为 `{expires_at: <RFC3339 UTC>, fingerprint: <SHA-256>}`，用既有的 JSONB 原子局部合并写入（含事务 outbox），并同步刷新该账号的调度快照；不加表、不加迁移，也不另设 Redis 或进程内缓存。每个账号最多两条，是否过期在读取时判断。
- 选择理由：复用现有的原子合并与快照同步，不必为 Redis 或进程内缓存另管生命周期；账号行本来就随调度快照读取。
- 指纹绑定平台、账号类型与上游凭据配置：管理员改了账号的上游配置，或者旧请求的学习迟到写入，都不会封住新配置。
- `capability_manual` 键保留给以后的手工覆盖，本期不读不写。
- 普通的账号保存在锁定行上合并，保留最新的学习状态：旧页面既不能覆盖新学到的记录，也不能恢复已清除的记录。复制账号、创建与导入时去掉学习键。管理端 DTO 只给出汇总 `capability_learned`，不展示这两个内部键与指纹。
- 学习不原地修改共享的 Account 对象，仓库写入后重新读取快照。写入失败只记事件，本请求仍可在预算内换号。

### 6.3 本请求与选号

- **本请求**：`failure.decision_v2` 开启时，钩子返回可换号的哨兵错误（`IsCapabilityStreamOnly()`；不上报账号调度失败、不同号重试），handler 只计请求预算的尝试次数，不记会话失败、不冷却。这次拒绝不写运维的上游错误记录（写入会把账号记为本请求失败），只记中性事件 `resilience.capability_learned`。`decision_v2` 关闭时只学习，钩子返回空，本请求照旧返回 400。
- **选号**：`Account.CapabilityExclusion(ctx, model, now)` 只在确定的路径上过滤：上游协议与入站相同，且上游的流模式跟随客户端（PM 指令，2026-10-21）。跨协议的转换（如 Chat 转 Responses 上游，这类转换本来就以 `stream=true` 请求上游）、按模型路由的路径，以及路由无法确定的 Grok OAuth Chat 都不过滤；Grok 的两种协议各自独立。不新增路由提示的 helper。OpenAI 选号的共同资格检查（`openai_gateway_scheduling.go`）与 `openai_account_scheduler.go` 的 `isAccountRequestCompatibleReason` 都经钩子 `capabilityEligibilityReason` 过滤，覆盖粘性会话与末位兜底的最后检查；钩子登记到 `scheduler-diff.json` 与 `policy.json`（α）。
- **只因能力而无号的应答**（PM 2026-10-21T02:42Z）：两种情形用同一个应答——
  - 选号时全部候选都因能力被过滤（`service.IsCapabilityOnlySelectionError`）；
  - 本请求的失败尝试全部是“只接受流式”的哨兵、没有其他原因（`service.IsCapabilityOnlyExhaustion`；handler 共用的 `resilienceAttemptFailed` 记录有无其他失败，不借用“本请求已失败账号”）。

  应答为 503（Anthropic 协议为 `api_error`），原因码 `capability_stream_only`，消息 `No available account in this group supports non-streaming requests; retry with "stream": true`（常量 `service.CapabilityOnlyMessage`），不带 `Retry-After`、不加重试尾句，写 `x-should-retry: false`，并把请求的 `RetryAdvice` 置为 `Suppress`（`Source` 为 `capability_stream_only`）：这些账号恢复前重试同样的非流式请求没有意义。混有其他失败原因时保留原来的无号或耗尽提示。不再把上游的 400 归咎于用户。共用的耗尽出口 `resilienceCapacityExhausted`（γ）与 OpenAI 专用出口（α，在透传规则之前）都按此处理。【待契约：γ 与 α 的提交】
- 请求标记里另存本请求的中性排除集合（不借用“本请求已失败账号”），用于最终无号的原因与重试建议的候选排除：被能力挡掉的账号不会被建议成健康备选。
- 负缓存的键按协议与流模式隔离，能力阻断也不计入全组的快速失败：同一分组的流式请求不会因为非流式请求被挡而拿到缓存的 503。
- 份额调度：份额 Explain 与候选原因把 `capability_stream_only` 和 `capability_mismatch` 都映射为 `sharesched.ReasonCapabilityMismatch`（`share_scheduling_openai.go`，α）。
- 实现提交：γ `2cb3343de`（学习、存放、过滤、哨兵、清除接口与 Grok 原生调用点）；β 接 OpenAI 其余转发的检测钩子；α 接入口标记、DTO、路由与 Explain。

### 6.4 观测与管理

- 事件：`resilience.capability_learned`（info：`account_id`、`protocol`、`expires_at`）；`resilience.capability_learning_failed`（warn：`account_id`、`protocol`、`error`）。
- 账号列表与详情返回 `capability_learned: [{protocol: "responses"|"chat", mode: "stream_only", expires_at: <RFC3339 UTC>}]`（`Account.CapabilityLearned(now)`，导出类型 `resilience.LearnedCapability{Protocol, Mode, ExpiresAt}`），没有记录时为空数组；功能关闭时仍可查看与清除未到期的记录。
- 清除：`DELETE /api/v1/admin/accounts/:id/capability-learning`，成功时 `data` 为 `{capability_learned: []}`，只更新这两个键（`AccountHandler.ClearCapabilityLearning`，服务方法 `ClearAccountCapabilityLearning` 经可选窄接口调用，不改 `AdminService`）。
- DTO（α，`bf38c06c5`）：`Account` 与 `AccountListItem`（完整列表、精简列表与详情）都带 `capability_learned`，没有记录时为 `[]`（不省略）；在脱敏前的 service 账号上读取有效记录，过期或指纹不符的不展示；两个内部 `extra` 键从详情与列表中删除，指纹不返回前端。清除路由沿用管理端的鉴权与审计链。
- Explain：每个账号记录都带 `capability_learned`。`protocol`（`responses` 或 `chat`）与 `stream`（`true` 或 `false`）必须成对出现，缺一个、空值、重复或非法值都返回 400；不带这两个参数时保持原来的整体健康视图。带 `stream=false` 时按该请求形态判断：被阻断的账号原因为 `capability_stream_only`，`recover_at` 为到期时间，另给 `capability_ttl_seconds`，并从本次的健康候选与末位兜底中排除（不变成全局硬罚，也不覆盖已有更强的硬罚原因）；`stream=true` 时不排除。带这两个参数时，主分组与兜底分组都应用能力过滤。Explain 没有请求体，只对上述确定路径排除；路由无法确定时不猜测排除，只展示学习记录。实现 α `affef416f`。
- 前端（FE）：账号列表状态列下方的“仅流式（自动学习，剩余 X）”标识与清除按钮（`AccountStreamOnlyBadge`；`AccountsView.vue` 的两处钩子登记为具名补丁 `xy-account-stream-only-learning`）；设置卡片“失败分类修正”“流式开场与终止”“仅流式账号学习”；运维页与份额页的 Explain 多一列“已学能力”和可选的“请求类型”（`ade0f689d`）；运维面板的 `pre_created_delta` 计数文案（`74e392ffd`）。

## 7. W5 信号与终止出口

### 7.1 RetryAdvice（W5.3）

`internal/resilience/retry_advice.go`（`fc9b62751`）定义无 import 环的 `RetryAdvice{Seconds, Source, Suppress}`，挂在请求的 `RequestState` 上，经 request context 传递：

- `WithRetryAdvice(ctx, advice)`：必要时挂接 RequestState 并写入；`SetRetryAdvice(ctx, advice)`：更新已有状态，已派生的子 context 立即可见；`RetryAdviceFrom(ctx)`：线程安全快照，缺省为零值。
- `Suppress` 在同一请求内不可撤销（封顶与终态优先），写入时 `Seconds` 归零。
- `Headers()` 只生成 `retry-after` 一项（整数秒，防御性夹到 10）；`Seconds<=0` 或 `Suppress` 时为空。合成帧的 `error.headers` 只用它，不复制上游的任意头。
- 惰性求值（PM 决定，2026-10-21T01:41Z）：
  - handler 在请求开始、选号前用 `WithRetryAdviceProvider(ctx, provider)` 安装请求级 provider（`RetryAdviceProvider func(ctx) RetryAdvice`），安装时不查询；
  - 合成点调用 `ResolveRetryAdvice(ctx)`：先读显式写入的 advice（含显式的零值 `unknown`，以及封顶、余额终态、内容拒绝固定的 `Suppress`），没有才调用 provider；最多等 100 ms，超时或取消就丢弃结果，等待期间新写入的显式 advice 仍然优先；
  - 惰性结果不缓存，下一次读取能看到候选变化；provider 只读当前请求与进程内快照，无副作用，不访问数据库或 Redis，不持有池化的 Gin context。

  这样 service 层在 handler 耗尽出口之前合成的 `response.failed` 也能拿到当时的候选情况。
- `Suppress` 由识别出终态的一方显式写入：handler 的回合封顶、内容拒绝、用户余额终态（α）；service 在自己合成这类终态帧时，先写 `Suppress` 再 `ResolveRetryAdvice`（β）。provider 本身看不到这些终态。
- 正文：`RetryAdvice.Message(message)` 统一处理重试尾句：去掉本地固定格式的旧尾句 “Please try again in Ns.”；秒数为正时追加与 `Headers()` 同样夹到 10 秒的尾句；`unknown` 与 `Suppress` 只去掉旧尾句。只改网关自己的固定后缀，不改写上游的自然语言。
- 分组 advisor（γ）只读末位兜底的分组快照（`lastResortGroups` 中未过期的条目，沿用既有的 2 秒缓存 `lastResortCandidateTTL`，锁内取引用、锁外计算），不做冷读取，不访问 Redis 或数据库。快照只在单一候选判断、末位兜底或原无号提示用过时才有；缺失或过期时返回 `unknown`。候选只算未尝试（排除 `RequestState` 里本请求已失败的账号）、模型与平台兼容、能力允许、无硬罚的账号；OpenAI 另看进程内的按模型屏蔽。入口是 `GatewayService` 与 `OpenAIGatewayService` 的同名方法 `RetryAdvice(ctx, groupID, model)`（γ `d4db8d426`），α 的 provider 自动识别；原 `NoAccountRetryAfter` 保留给 HTTP 无号提示。

| 情况 | `Source` | `response.failed` 的 retry-after |
|---|---|---|
| 本请求此刻还有健康备选 | `healthy_candidate` | 1 秒 |
| 组内没有健康候选，且能算出最早恢复时间 | `recovery` | 向上取整并夹到 2–10 秒；正文 “try again in Ns” 的 N 与之一致 |
| 恢复时间未知（没有快照、快照过期、请求已取消，或剩下的只有不会自行恢复的硬状态） | `unknown` | 不带 |
| 回合封顶、用户自身余额不足的终态、内容拒绝、只因能力而无号（§6.3） | `turn_cap`、`balance_terminal`、`content_rejection`、`capability_stream_only`（`Suppress`） | 不带，并删除原有的重试尾句。上游账号余额不足（可换号）与在途预留超额不算终态 |
| 请求上没有 advice | — | 保持 0.4.3 |

- 写入位置：`error.headers.retry-after`（Codex 从这里读重试等待）。
- 合成点：handler 侧 `handler/stream_error_event.go`（α，`9b951b4de`）；service 侧 `resilience_keepalive.go`、`openai_compact_stream_bridge.go`、`openai_gateway_response_handling.go`（β）。
- handler 接入（α）：
  - Responses、Chat、Messages 的六个 HTTP 入口在解析模型后、选号前安装 provider（上游文件钩子 `installResilienceRetryAdvice`，位于 `gateway_handler.go`、`gateway_handler_responses.go`、`gateway_handler_chat_completions.go`、`openai_gateway_handler.go`、`openai_chat_completions.go`）。闭包只保留 service 与分组、模型的初值，调用时读 RequestState 里当前的分组与选号模型（兼容兜底分组），不保留 Gin context 或可变的 API Key 对象。
  - 无号与耗尽出口按当前模型求一次 advice 并写入请求状态，覆盖“已复制上游 `Retry-After`”的分支；旧的 HTTP advisor 不再传空模型。
  - `response.failed` 的错误体增加可省略的 `headers`，开关开启时只放 `retry-after`；正文经 `RetryAdvice.Message` 保持同样的秒数；HTTP 头还没提交时一并改写，已经提交的头无法回写，以流内帧为准。
  - 回合封顶、用户余额终态、内容拒绝清掉旧的 HTTP `Retry-After`、延迟标记、内嵌的重试头和本地的固定尾句。配置拒绝与鉴权类错误不给候选重试建议；在途预留超额保持原有的可重试语义。
  - 恢复时间未知时不再提示，不再像 0.4.3 那样缺省写 30 秒。
- `errors.failed_frame_retry_after` 关闭：不带 `error.headers`，正文与提示沿用旧 design §7.6。

### 7.2 W5.1 耗尽出口统一

- Gateway Responses、Gateway Chat、OpenAI→Messages 的专用耗尽出口（`handleAnthropicFailoverExhausted` 等）统一调用公共的 `applyExhaustedRetryHint`：429 保持 429；到达回合封顶的 5xx 改为 503，删除 HTTP `Retry-After` 与正文里的重试时间，写 `x-should-retry: false` 与 `server_is_overloaded`，同时把请求的 `RetryAdvice` 置为 `Suppress`。
- 一个请求的回合计数只增加一次，多次调用读取同一结果。
- 一次性终止：三个专用出口与公共 Responses/Chat 终态 writer 共用同一个请求标记（`beginResilienceTerminal`）；已尝试写出终态（含短写与断连）后不再追加。
- OpenAI→Messages 的专用 writer 也应用公共的 code 与消息翻译和运维的流内错误标记；Gateway Chat 在只发过排队心跳时也走终止帧，不再直接返回。
- 不改已写出的真实输出或上游自己的终态。
- 现有限制：快速失败与配置拒绝不计入回合封顶；回合按进程内 TurnKey 的连续失败与间隔计数，不严格等于客户端的一轮生成。

### 7.3 W5.2 排队 ping 之后的终止帧

Gateway 入站的 Responses 与 Chat 专用错误出口（`gateway_handler_responses.go` 的 `responsesErrorResponse`、`gateway_handler_chat_completions.go` 的 `chatCompletionsErrorResponse`）在 HTTP 已提交（例如排队等待时已发出 ping）时改写 SSE 终止帧：

- Responses：`response.created` 后跟 `response.failed`，同一 id 与 `created_at`，序号 0 → 1（`writeResponsesFailedSSEWithOpening`）；
- Chat：`error` 分块后跟 `[DONE]`（`writeChatCompletionsErrorSSE`）。

覆盖计费复查、在途预留、之后的模型与终态错误。未提交时保留原有的 JSON 结构与状态码。不受任何韧性开关控制：关闭总开关、没有送达追踪时也不再追加裸 JSON。

### 7.4 W5.4 Anthropic 的错误类型

受现有 `errors` 开关控制，关闭时回到 0.4.3 的类型。

- 网关生成的瞬时 5xx（含只有 code、没有 type 的错误体）统一用 `api_error`；状态码 529 本身不再足以生成 `overloaded_error`。
- 只有上游真实返回 `error.type`（或顶层 `type`）为 `overloaded_error` 的换号失败，才保留该类型。两个 Anthropic handler 分别覆盖 HTTP 应答与 SSE 终止帧（`anthropicFailoverErrorType`、本地 writer 的 `anthropicLocalErrorType`）；首包保活的转换点 `resilience_keepalive.go` 不再给 503 补 `overloaded_error`（β）。
- 无号原本就用 `api_error`，不变；管理员配置的错误透传规则仍然优先。
- HTTP 状态码、正文与用户余额提示不变（Q16）。

### 7.5 W5.5 Codex 的 Retry-After 上限

- 识别 Codex：沿用 `openai.IsCodexOfficialClientByHeaders` 的官方 UA 与 `originator` 家族，另覆盖 UA 中含 `codex_cli_rs`、`codex_exec`、`Codex Desktop`（大小写不敏感，含复合 UA）。只用于重试提示，不影响鉴权。
- 上限取 `errors.codex_retry_after_cap_seconds`（`CodexRetryAfterCapOn()`；总开关或 `errors.enabled` 关闭时不应用）。截短的是无号应答的 HTTP `Retry-After` 与正文里的秒数；配置拒绝（仅限 Claude Code、渠道定价限制）的 `Retry-After: 60` 对 Codex 同样截到上限，仍带 `x-should-retry: false`、正文不加重试句。其他耗尽错误与其他客户端不变（无号 2–30 秒，配置拒绝 60 秒）。
- 原因：Codex 0.158.0 起按 `Retry-After` 等待且不封顶，静默等满。
- 实现在自有的 `handler/resilience_errors.go`，无新增上游钩子。

## 8. W8-lite 事件字段

不加表、不加迁移，不记录正文、Key 或完整会话值。

| 事件 | 新增字段 |
|---|---|
| `resilience.post_output_failure`、`resilience.session_rebind`、`resilience.last_resort`、`resilience.keepalive_committed`、`resilience.turn_cap` | `request_id`、`gateway_request_id`、`model`、`delivered_bytes`、`ms_since_first_output`、`prelude_release_event`、`prelude_release_ms` |

- 字段统一由 `resilience.EventFields(ctx)` 生成，挂在请求的 `RequestState` 上：
  - `request_id` 取 `ctxkey.RequestID`，与运维记录和访问日志一致（PM 2026-10-21T02:42Z）；`gateway_request_id` 是 RequestState 新建时生成的本地 UUID，客户端无法指定。两者都不改变计费与幂等键或 ClientRequestID。【待契约：α 的提交】
  - `model` 优先取入站模型，没有时取选号用的模型；
  - `delivered_bytes` 读 `RequestState.DeliveredBytes()`（送达口径，开场与心跳不算）；
  - `ms_since_first_output` 从第一次成功写出内容起算（开场事件与心跳不启动时钟，之后的输出不重置），没有输出时为 0；
  - `prelude_release_event`、`prelude_release_ms` 由 β 的 `prelude_trace.go` 采集（`NotePreludeCreated`、`NotePreludeReleased`、`PreludeTrace()`，`04aa6f1a6`）：每次尝试先重置，`response.created` 记下时刻，第一次释放时冻结事件与非负的毫秒差；迟到的 created 不覆盖已有的释放，没有 created 时毫秒为 0。覆盖普通 Responses、透传、Chat 与 Messages 转换。它们表示“决定释放暂存”的那一刻，不保证写出成功。上游文件钩子 `beginOpenAIPreludeTrace`、`releaseOpenAIPrelude`（四个网关文件，登记到 required_literals）。
- 首输出前的事件（`keepalive_committed`）在持有心跳锁时记录，改用 `resilience.PreOutputEventFields(ctx)`：不读送达探针，`delivered_bytes` 与 `ms_since_first_output` 固定为 0，避免锁重入。
- 日志点：`resilience_session.go` 的 `session_rebind`、两处 `last_resort`、`post_output_failure`、`turn_cap`（α）；`resilience_keepalive.go` 的 `keepalive_committed`（β）。【待契约：最终提交】

## 9. 不受开关控制的修复

| 修复 | 说明 |
|---|---|
| 排队 ping 之后按协议写终止帧（W5.2） | §7.3；总开关关闭时同样生效 |
| 【待契约】 | 各实现方登记的其他协议正确性修复 |

## 10. 回退语义

- **总开关关闭**：按 0.3.0 运行，本期新开关一并失效；§9 的修复仍生效。
- **子开关关闭**：回到 0.4.3 / #117 的行为（§1“关闭后”一列），即时生效，无需重启。
- **版本回退到 0.4.3**：无迁移，直接换回镜像。设置的兼容与“在 0.4.3 上再次保存会丢弃新字段”的边界见 §2.3。已学到的仅流式能力存在账号 `extra` 的两个键里，0.4.3 不读它们，回退后不再过滤；记录留在 `extra` 中，再升级时未到期的仍然生效，最长 `capability.learn_ttl_seconds` 后自然过期。

## 11. 已知限制与延后

- 仍未覆盖的路径：Gemini、Bedrock、CN、Antigravity T2、Gateway 入站经 Anthropic 上游的两个反向转换器（Responses、Chat）；它们的开场事件与终止行为保持 0.4.3。
- 推迟开场期间的注释心跳会提交 200：此后的失败只能以带内终止帧告知客户端，不能再改状态码（与原生 Responses 相同）。
- 容量判据收紧后，“Codex is at capacity”与非 503 上的 “servers are currently overloaded”（OpenAI 请求级降载）不再算 `CAPACITY_SHED`，会让账号进入软冷却并计入会话失败（临时不可调度仍跳过）；重试耗尽的 Grok 原始 429 不再转成无号 503，客户端看到 429。符合方案（不凭文案判容量），上线后按会话改绑与冷却观察（审查 P2-3）。
- 原生路径上，上游在 `response.created` 之前发出的非空 delta 照常先转发，网关不重排；这类上游下“Responses 第一个事件总是 created”不成立，严格 SDK 仍可能报错（审查 P2-7）。
- 仅流式学习记录按凭据指纹失效：OAuth 类账号刷新令牌后指纹改变，需要重新学习一次（审查 A-P2-3）。
- 学习粒度是“账号 + 协议”，不分模型：中转只对某个模型要求流式时，该账号所有模型的非流式请求都会被跳过（审查 A-P2-8）。
- 观测只有日志字段，没有请求与尝试表；“伪已输出”比例要从 `prelude_release_event` 日志统计。

## 12. 验证

测试服务器场景（QA，`tools/gateway-resilience/staging`；清单与已有场景的修改登记在旧变更 [`staging-validation.md`](../gateway-resilience-20261015/staging-validation.md) §6.1）。“基线”一列是 QA 在 `027a434ad` 上的实测：红灯表示本期要修的行为，回归表示基线已经满足、本期防止退化。

| 场景 | 故障 | 覆盖 | 基线 |
|---|---|---|---|
| CV-5 | F31 | §3.3 created → failed(overloaded)：A 按 D18 最多试 2 次（各记一条 `CAPACITY_SHED`），在 B 成功，客户端只有 B 的 `response.created` | 回归 |
| CV-6 | F32 | §3.4 Chat 兼容 created → failed(“The service is busy”) 换号，只有一个 role 分块；不按容量误判（§5） | 红灯 |
| CV-7 | F33 | §3.3 created、in_progress → failed 换号 | 回归 |
| CV-8 | F34B | §3.1 created 之前的空 delta 与空的 added 结构在换号时丢弃 | 红灯 |
| CV-9 | F34 | §3.1 `codex.response.metadata` 算输出，不再换号 | 回归 |
| RAW-1 | F35 | §4 raw Chat `error` + `[DONE]` 在输出前换到 B | 红灯 |
| RAW-2 | F35 | §4 单号组：记为失败，不当成功、不计费 | 红灯 |
| SO-1 | F36 | §6 仅流式：学习、本请求换号、跳过下一个非流式请求、流式照常、清除后重新学习 | 红灯 |
| RA-1 | F37 | §7.1 心跳已提交后全组过载：`response.failed` 带 2–10 秒 retry-after，正文一致 | 红灯（正文 8 秒但帧不带 headers） |
| RA-2 | — | §7.1 心跳已提交后 B 健康：请求完成，没有延迟提示 | 回归 |
| ANT-3 | — | §7.4 Messages 无号为 `api_error` | 回归（无号本来就是 `api_error`；本期改的是心跳后的终止帧与按状态码推断的类型，由单测覆盖） |
| SH-1、SH-2 | — | PR #117 份额：切换到份额按优先级与负载因子选号、真实瞬时失败降低健康权重、回到原版 | 通过（基线已含 #117） |
| DC-6（修改） | — | §3.4 兼容转换只写开场就断开：改为断言开场延迟 | 红灯 |

排队 ping 之后的终止帧（§7.3）由单测 `TestCompatibleQueueErrorsEndInInboundProtocol` 覆盖，测试服务器场景 Q-1 为可选。

候选验证：全部 69 个场景（原 56 个加上表 13 个）、升级演练（0.4.3 → 候选，含回退与再前滚）、3 小时浸泡（存活堆斜率 ≤1 MiB/h）。结果见 tasks。
