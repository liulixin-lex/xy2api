# 网关韧性：客户端可见的行为

本文说明网关韧性开启时客户端会看到什么：各种失败场景的状态码、错误码与响应头，`Retry-After` 的取值，流式请求在失败时怎样结束，心跳的格式，网关会自动修复并重放哪些请求，以及断开时的计费口径。适合调用网关 API 的开发者与排查客户端报错的管理员。下文的默认值都可在 [设置参考](gateway-resilience-settings.md)中调整。

## 错误响应

| 场景 | 响应 |
|---|---|
| 分组内没有可用账号 | 503，`Retry-After` 2–30 秒（带抖动，按组内最早恢复时间估算），消息附 “Please try again in Ns.” |
| 非流式请求，但组内账号都只接受流式 | 503，消息为 `No available account in this group supports non-streaming requests; retry with "stream": true`，带 `x-should-retry: false`，不带 `Retry-After` |
| 配置原因导致无号（如渠道定价不含该模型） | 503，`Retry-After: 60`，`x-should-retry: false` |
| 模型不在分组白名单 | 404，消息为 `Model "<model>" is not available for this group`，并列出本分组白名单的前 10 项 |
| 分组仅限 Claude Code，请求不是 Claude Code 且没有兜底分组 | 403 `permission_error`，不带重试头 |
| 用户余额被在途请求占用 | 429 `rate_limit_error`，code `concurrent_balance_reserved`，`Retry-After` 约 5 秒 |
| 排队已满或并发槽位已满 | 429，`Retry-After: 5` |
| 上游余额不足 | 本请求换号；全部耗尽时 503。单账号分组在账号暂停期间返回 503，`Retry-After` 为暂停剩余时间（2–30 秒） |
| 上游明确报告模型不可用，候选耗尽 | 404，OpenAI 与 Anthropic 为 `model_not_found`，Gemini 为 `NOT_FOUND`，消息为 `Model "<model>" is not supported by any configured account in this group`，不附重试建议 |
| 上游内容安全拒绝 | 400 `invalid_request_error`，code `content_policy_violation`，附脱敏后的上游原文（最多 500 字符）；不重试、不换号 |
| 换号耗尽的 429 或 5xx | 带 `Retry-After`：上游给出的值封顶到 2–30 秒，没有时取组内最早恢复时间 |
| 5xx 但上游错误类型为请求错误，候选耗尽 | 保留上游状态与脱敏后的错误体，带 `x-should-retry: false`（只对识别该头的客户端生效，例如 OpenAI 官方 SDK） |
| 不可重试的耗尽（413 请求过大、续链被拒的 400） | 不带 `Retry-After`，并清除前面尝试留下的重试提示 |
| 同一会话连续第 5 次耗尽失败（120 秒内） | 终止应答：`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持），`x-should-retry: false`，不带 `Retry-After`。会话成功一次或两次失败相隔超过 120 秒后重新计数 |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，带 `X-XY-Upstream-Status: 502` 或 `504`。直连请求保持原状态码 |
| Gemini 入口组内账号全部暂时不可用 | 503 并带重试提示 |

补充说明：

- 返回给客户端的上游错误消息会去掉上游组织、工作区、项目与账号的标识（如 `org-…`），`code`、`type`、`param`、重试头与响应 ID 保留；运维记录仍保存受控的原文。
- Anthropic 协议（`/v1/messages`）下，网关自身的无号与瞬时失败，以及换号耗尽的上游容量降载（`overloaded_error`、529），统一返回 503、错误类型 `api_error`；已提交的流写 `event: error`，类型同样为 `api_error`。Claude Code 会把任何 `overloaded_error` 当作 529 计数并在连续 3 次后停止或切换模型，这样改写可以避免误触发。
- 关闭 `errors` 段后不加重试头、不做负缓存与回合封顶，上述错误形态回到原样（内容安全拒绝回到 502）。

## Retry-After 的取值

- 估算方法：对本请求有资格的每个账号，取它仍有效的限制中最晚的结束时间（含按模型的冷却），再取各账号中最早的那个。
- 各出口的范围：HTTP 无号 2–30 秒（`errors.no_account_retry_after`）；Codex 客户端（按 `User-Agent` 或 `originator` 识别）不超过 `errors.codex_retry_after_cap_seconds`（默认 10 秒），因为 Codex 会按它静默等待；流内失败帧 2–10 秒。
- 负缓存：同一分组、平台、模型的“无号”结论缓存 2 秒，这期间的请求直接返回同样的 503。只对整个分组成立的结论缓存；只对某个会话成立的无号（例如新会话撞上会话数上限）不缓存。
- 快速失败：同一 API Key 在 30 秒内收到 10 次相同的 503 后，后续请求在选号之前直接返回该 503，直到成功一次。

## 流式请求的失败结束方式

### 已提交后的失败

HTTP 200 一旦因心跳或开场提交，状态码就不能再改，之后的失败按入站协议写终止帧：

| 协议 | 终止帧 |
|---|---|
| Responses | `response.failed`（`rate_limit_exceeded` 或 `server_error`）；客户端此前只收到心跳时，前面先补同一响应的 `response.created`（openai-python 等严格 SDK 要求流以它开始） |
| Chat Completions | `data: {"error":{…,"status":N}}`，随后 `data: [DONE]` |
| Anthropic Messages | `event: error` |

网关在原生 Responses 流里自己判定的失败（上游单行超长、输出后读错、停滞）同样以 `response.failed` 结束（`server_error`，沿用本响应的 ID 与已得用量）。

### `response.failed` 的重试时间

`errors.failed_frame_retry_after` 开启时（默认），合成的 `response.failed` 在 `error.headers.retry-after` 中给出重试时间（Codex 从这里读取），正文 “try again in Ns” 的 N 与之相同：

- 本请求还有健康账号：1 秒。
- 组内没有健康账号且能算出最早恢复时间：2–10 秒。
- 按快照算不出恢复时间，但最后一个可重试的上游失败（429 或 5xx）给出了有效的 `Retry-After`：采用它，夹到 2–10 秒。
- 恢复时间未知：不带。
- 回合封顶、用户自身余额不足、内容拒绝，以及模型不存在、请求或鉴权错误等不可重试的失败：不带，并去掉正文里的重试句。

### 正文前失败的换号

上游已经发出开场事件、但还没有正文时失败（过载、服务忙、断流、读错），网关会换号重试，客户端只看到成功那次尝试的开场：Responses 只有一个 `response.created`（新账号的 ID），Chat 只有一个 role 分块，Messages 只有一个 `message_start`。`response.incomplete`、内容拒绝与只有用量的合法空回答照常返回，不重放。一旦写出正文（哪怕只写出一部分），就不再换号。

### 中转伪保活帧

部分中转会插入 `SSE-Keep-Alive` 空 delta 帧，或在 `response.created` 之前发出空字符串、纯空白的 delta。网关直接丢弃它们，不转发、不算首个输出（openai-python 遇到会报错），但仍算上游存活，不会因此被判停滞。`response.created` 之前的非空 delta 是真实内容，照常立即转发；这类上游下严格 SDK 仍可能报 “Expected to have received response.created”。

## 心跳

心跳不算输出、不计费，也不妨碍换号。

| 入站协议 | 首个输出前的心跳 |
|---|---|
| Responses（原生、透传） | Codex 客户端收到完整事件 `event: ping` / `data: {"type":"ping"}`；其他客户端收到 SSE 注释行 `:` |
| Chat Completions | SSE 注释行 `:` |
| Anthropic Messages | `event: ping`；原生 Anthropic 与 API Key 直通在开场暂存期间写注释行 `: ping` |

排队等待并发槽位或用户消息队列时的 ping 同样按入站协议选择格式。之后若遇到计费等错误，Responses 写 `response.created` 后跟 `response.failed`，Chat 写 `error` 后跟 `[DONE]`，不会写裸 JSON。

心跳提交 200 时只写网关自身的流式响应头（`Content-Type`、`Cache-Control` 等），失败尝试的上游响应头不会写出。原生 Responses 在第一次心跳提交时只额外带上本次尝试的 `x-codex-turn-state`，以保持 Codex 回合内的状态连续性；OpenAI Responses 透传在心跳提交时带出该次尝试的全部上游头。

## 请求修复与重放

以下情况网关在本地修复请求后，在同一账号上重放，客户端不感知失败尝试。重放消耗本请求预算中的尝试次数，预算不足时不重放、原错误照常返回。

| 上游拒绝 | 网关处理 |
|---|---|
| 明确点名不支持的参数（结构化 `code`/`param`，或 “Unsupported parameter: X”） | 删除请求顶层的 `top_p`、`metadata`、`temperature`、`frequency_penalty`、`presence_penalty`、`max_output_tokens`、`truncation` 中被点名的那个后重放；每个请求合计最多 6 次，同一请求体不重放第二次。其他字段被拒时原样返回 |
| 明确拒绝 content part 上的 `cache_control` | 删除该请求所有 content part 上的 `cache_control`，重放一次；顶层、消息级与工具参数里的同名字段保留 |
| 无法解密或校验请求中的加密内容（`invalid_encrypted_content`、`encrypted_context_invalid` 等，密文通常来自别的账号） | 按请求级处理，不罚号。OpenAI 原生 Responses 在第一个内容之前遇到时，删除 reasoning 的 `encrypted_content`、丢弃带密文的 compaction 后重放一次 |
| 上游会话状态拒绝 | 按请求级拒绝处理，不罚号、不计会话失败；原生与透传 Responses 在输出前可替换协作消息与工具输出中的密文槽位后重放或换号一次 |
| API Key 中转拒绝回放 `web_search_call` 历史（“response protection is unavailable”） | 补上 web_search 工具声明后重放一次；成功后该账号 6 小时内同类请求直接补声明。仍失败时返回 502 并说明原因，不换号 |
| Claude 请求分发到 GPT 中转时，续链（`previous_response_id`）被笼统的 4xx 拒绝 | 去掉续链、按完整历史重放一次，并对该会话在该账号上关闭续链。指名其他参数的错误、上下文超长、内容审核、模型不存在等照常返回 |

Codex 子 agent 的请求如果在 `agent_message` 条目中带加密的任务正文，网关优先把它调度到父线程的粘性账号（只有父线程的账号能解开），父线程 ID 取自请求头、turn metadata 与请求体的 `client_metadata.x-codex-parent-thread-id`。父账号不可用时普通选号。

## 送达与计费口径

“已输出”与计费、换号共用同一个判据：客户端是否收到过语义内容。

- 不算输出的事件：SSE 注释行、`ping`、`keepalive`，Anthropic `message_start` 与空的 text/thinking `content_block_start`，Responses 的 `response.created`、`response.in_progress`、`response.queued`、`response.metadata`、`codex.rate_limits`、`codex.response.metadata` 与不带内容的 added 事件，只带角色的 Chat 分块，以及网关心跳与排队 ping。
- 其余事件都算输出，包括错误事件、`[DONE]` 与 `tool_use` 块。
- 非流式与图片 JSON：以响应体是否已写出为准（图片 JSON 的保活填充不算）。

| 情况 | 计费 |
|---|---|
| 任何输出前客户端断开（只收到开场与心跳） | 立即取消上游，不写用量、不计费，记入未送达表；超过滥用阈值的 API Key 按上游成本计费 |
| 部分输出后客户端断开 | 最多再读上游 180 秒（`disconnect.partial_drain_max_seconds`），按已得用量计费 |
| 输出后上游异常结束（流内错误、缺终态、读错、停滞） | 按已读到的用量计费一次 |
| 换号前失败的尝试 | 不向用户计费 |

原生 Anthropic 与 API Key 直通在开场暂存期失败并换号的尝试，不向用户计费、不产生用量记录；上游可能已对这次尝试的输入计费，这部分成本由网关承担。
