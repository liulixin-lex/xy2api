# 网关韧性：客户端可见的行为

本文说明网关韧性开启时客户端会看到什么：各种失败场景的状态码、错误码与响应头，`Retry-After` 的取值，流式请求在失败时怎样结束，心跳的格式，网关会自动修复并重放哪些请求，以及断开时的计费口径。适合调用网关 API 的开发者与排查客户端报错的管理员。下文的默认值都可在 [设置参考](gateway-resilience-settings.md)中调整。

## 错误响应

本次请求最后一次真实上游失败已知时，按该失败映射状态与消息，不进入下面的本地无号分类。

| 场景 | 响应 |
|---|---|
| 入站请求体上传超时 | 408 `invalid_request_error`，消息 `Timed out reading request body`；OpenAI 协议 code 为 `request_upload_timeout`。不调用上游；真正取消或断连只记 499，不补写响应 |
| 普通上游 408、409、422 | 保留状态码与脱敏消息；Responses 与 Chat 错误体保留安全的 `type/code/param`，Messages 保留既有 `type/message` 形状，缺省 code 分别为 `request_timeout/conflict/unprocessable_entity`；管理员配置的错误规则优先 |
| 分组内有账号，但没有一个支持所请求的模型 | 404，`model_not_found`，消息为 `Model "<model>" is not supported by any configured account in this group` |
| 有支持该模型的账号，但没有一个能满足本次请求的能力 | 400 `invalid_request_error`；带工具的 Chat 按最终模型与档位需要 Responses 时，`code` 为 `responses_required`，`param` 与消息指出 `tools` 或 `functions`。有能力的账号只是暂时繁忙时，按实际无号原因处理 |
| 能服务的账号都只是被限流、并发已满或排队已满 | 429 `rate_limit_error`，消息为 `All available accounts are currently rate-limited. Please retry later.` |
| 非流式请求，但组内账号都只接受流式 | 503，消息为 `No available account in this group supports non-streaming requests; retry with "stream": true`，带 `x-should-retry: false`，不带 `Retry-After` |
| 账号池为空，或其他无号情况（混合限制、硬暂停、依赖故障、数据库查询失败等） | 503，`Retry-After` 2–30 秒（带抖动，按组内最早恢复时间估算），消息附 “Please try again in Ns.” |
| 配置原因导致无号（如渠道定价不含该模型） | 503，`Retry-After: 60`，`x-should-retry: false` |
| 模型不在分组白名单 | 404，消息为 `Model "<model>" is not available for this group`，并列出本分组白名单的前 10 项 |
| 分组仅限 Claude Code，请求不是 Claude Code 且没有兜底分组 | 403 `permission_error`，不带重试头 |
| 用户余额被在途请求占用 | 429 `rate_limit_error`，code `concurrent_balance_reserved`，`Retry-After` 约 5 秒 |
| 排队已满或并发槽位已满 | 先在本请求剩余的时间与尝试次数内，再找一次有即时空位的其他合格账号（续链等必须回到原账号的请求除外）；仍没有空位时返回 429，`Retry-After: 5`，不再进入第二次排队 |
| 上游余额不足 | 本请求换号；全部耗尽时 503。单账号分组在账号暂停期间返回 503，`Retry-After` 为暂停剩余时间（2–30 秒） |
| 上游明确报告模型不可用，候选耗尽 | 404，OpenAI 与 Anthropic 为 `model_not_found`，Gemini 为 `NOT_FOUND`，消息为 `Model "<model>" is not supported by any configured account in this group`，不附重试建议 |
| 上游内容安全拒绝 | 400 `invalid_request_error`，code `content_policy_violation`，附脱敏后的上游原文（最多 500 字符）；不重试、不换号 |
| 换号耗尽的 429 或 5xx | 带 `Retry-After`：先取当前失败保存的原始恢复时刻，再封顶到 2–30 秒；没有时取组内最早恢复时间 |
| 5xx 但上游错误类型为请求错误，候选耗尽 | 保留上游状态与脱敏后的错误体，带 `x-should-retry: false`（只对识别该头的客户端生效，例如 OpenAI 官方 SDK） |
| 不可重试的耗尽（413 请求过大、续链被拒的 400） | 不带 `Retry-After`，并清除前面尝试留下的重试提示 |
| 同一会话连续第 5 次耗尽失败（120 秒内） | 终止应答：`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持），`x-should-retry: false`，不带 `Retry-After`。会话成功一次或两次失败相隔超过 120 秒后重新计数 |
| 非流式 Responses 请求，上游返回的 SSE 没有合法的 `response` 结束事件 | 502 JSON，`code` 为 `upstream_stream_truncated`，不再以 200 原样返回 SSE；已读到的用量照常计费。经 CDN 时按下方 CDN 规则改为 503 |
| 非流式 Responses 请求，上游返回的 SSE 结束事件损坏（坏 UTF-8、坏 JSON、缺 `response`、状态矛盾） | 502 JSON，`code` 为 `upstream_protocol_error`；其余同上 |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，带 `X-XY-Upstream-Status: 502` 或 `504`。直连请求保持原状态码 |
| Gemini 入口组内账号全部暂时不可用 | 同样按无号原因区分普通容量耗尽与混合限制；需要重试时带重试提示 |
| Gemini 兼容入口选择未声明的工具或并行控制类型错误 | 400 `invalid_request_error`，错误指出 `tool_choice.name` 或 `tool_choice.disable_parallel_tool_use`；不发送上游、不换号或罚号 |

补充说明：

- 上游明确报告 `No tool call found for function call output` 或 `No tool output found for function call` 的工具历史 400 时，原生、原样转发和 Chat→Responses 都保留上游脱敏后的 `type`、`code`、`param`、`message`；上游没给 `param` 时不补。请求只发送一次，不换号、不罚号。
- 非流式 Responses 按完整正文识别 JSON 与 SSE：完整 JSON 优先于上游 Content-Type，SSE 聚合后返回 `application/json; charset=utf-8`，用量按真实正文读取。
- 上游给出的重试时间（如 `Retry-After`）之前，网关不会再选用同一账号，末位兜底也一样；冷却上限只决定网关何时重新检查这个账号。
- 返回给客户端的上游错误消息会去掉上游组织、工作区、项目与账号的标识（如 `org-…`），`code`、`type`、`param`、重试头与响应 ID 保留；运维记录仍保存受控的原文。
- Anthropic 协议（`/v1/messages`）下，网关自身的 503 失败，以及换号耗尽的上游容量降载（`overloaded_error`、529），统一返回 503、错误类型 `api_error`；已提交的流写 `event: error`，类型同样为 `api_error`。Claude Code 会把任何 `overloaded_error` 当作 529 计数并在连续 3 次后停止或切换模型，这样改写可以避免误触发。
- 关闭 `errors` 段后不加重试头、不做负缓存与回合封顶，上述错误形态回到原样（内容安全拒绝回到 502）。

原生 Responses HTTP 的续接错误使用以下稳定字段，`error.type` 均为 `invalid_request_error`：

| 场景 | HTTP / `error.code` | `error.param` / message |
|---|---|---|
| 响应归属不通过 | 400 / `previous_response_owner_mismatch` | `previous_response_id` / `previous_response_id is not available for this user` |
| 可选账号都不支持 HTTP 续接（OAuth、SetupToken） | 400 / `http_continuation_unsupported` | `previous_response_id` / `The selected upstream does not support previous_response_id over HTTP; send complete input history` |
| 既有工具输出校验发现缺 call_id | 400 / `tool_output_missing_call_id` | `input[i].call_id` / `input[i] (<type>) requires a non-empty string call_id; only a named function_call_output may omit call_id or use null` |
| 上游中转不接受没有 call_id 的工具输出条目，且网关已无可做的改写 | 400 / `upstream_standalone_tool_output_unsupported` | 无 / `The selected upstream does not accept tool output items without call_id. Remove the item or provide its call_id.` |
| 上游明确报告续接状态过期或不存在 | 410 / `upstream_continuation_expired` | `previous_response_id` / `The upstream continuation state is no longer available; send complete input history` |

`upstream_standalone_tool_output_unsupported` 只在尚未向客户端输出时返回；已经输出（含首包前的保活帧）之后上游才以该原因拒绝时，流按上游原错误结束。兼容模式关闭或账号不在生效范围内时也返回上游原错误。

响应归属按用户判断，同一用户换了 API 密钥仍可续接自己的响应。走原生 Responses 的 API Key 账号收到的 `previous_response_id` 与 `store`（缺省、`false`、`true`）都不改动；DeepSeek、Kimi 等无状态上游照旧去掉 `previous_response_id`、把 `store` 设为 `false` 后发送，上游走 Chat Completions 或 Anthropic 协议的 API Key 账号也照旧转换后发送，都不返回上面的错误。上游只返回普通的 404、409 或含义不明的 4xx 时，不当作续接过期，照原样返回。以上错误都不罚号、不计会话失败；尚未提交时不带 `Retry-After`，并写 `x-should-retry: false`；已提交的流保持原 HTTP 状态，以同样的错误码写一个错误终止帧。

实际错误字段中的会话政策拒绝（`session_blocked_by_cyber_policy`、`cyber_policy`）会结束本次请求，不跨账号重发；防护墙 1010 拦截不视为账号凭据失效。

## Retry-After 的取值

- 估算方法：对本请求有资格的每个账号，取它仍有效的限制中最晚的结束时间（含按模型的冷却），再取各账号中最早的那个。
- 上游的等待提示：HTTP 响应头、失败事件里的 `error.headers` 与错误体中给出的有效等待时间，取其中最晚的时刻（对已确认的 HTTP 错误，也会按原有方式从错误体解析一次，一起比较）。网关在同一账号上的重试不会早于这个时刻；返回给客户端的重试建议仍按下面的范围限幅。
- 各出口的范围：HTTP 无号 2–30 秒（`errors.no_account_retry_after`）；Codex 客户端（按 `User-Agent` 或 `originator` 识别）不超过 `errors.codex_retry_after_cap_seconds`（默认 10 秒），因为 Codex 会按它静默等待；流内失败帧 2–10 秒。
- 负缓存：只缓存对整个分组成立的无号结论，默认 2 秒；命中后返回相同的状态码与原因。只对某个请求或会话成立的失败不缓存；带工具的 Chat 请求也不读写这份缓存、不计入快速失败。
- 快速失败：同一 API Key 在 30 秒内收到 10 次相同的 503 后，后续请求在选号之前直接返回该 503，直到成功一次。

## 流式请求的失败结束方式

### 已提交后的失败

HTTP 200 一旦因心跳或开场提交，状态码就不能再改，之后的失败按入站协议写终止帧：

| 协议 | 终止帧 |
|---|---|
| Responses | `response.failed`（`rate_limit_exceeded` 或 `server_error`）；客户端此前只收到心跳时，前面先补同一响应的 `response.created`（openai-python 等严格 SDK 要求流以它开始） |
| Chat Completions | `data: {"error":{…,"status":N}}`，随后 `data: [DONE]` |
| Anthropic Messages | `event: error` |

已经写出 JSON、已经结束或已经取消的响应不会再追加错误，也不会改动已提交的 HTTP 状态。

网关在原生 Responses 流里自己判定的失败（上游单行超长、输出后读错、停滞）同样以 `response.failed` 结束（`server_error`，沿用本响应的 ID 与已得用量）。

原生与原样转发的 Responses 流还按以下规则结束，每个响应只写一个结束事件，成功结束后不会再追加失败：

- 收齐完整的 SSE 事件后才判断是否结束。坏 UTF-8、坏 JSON、缺 `response` 或状态矛盾的结束事件不算成功；已经输出时以 `response.failed` 结束，保留已读到的用量。连续多行 `data:` 拼起来不是合法 JSON、但每行都是完整 JSON 对象时，继续按独立事件处理；拼起来合法的多行 JSON 保持一个事件。
- 原生、原样转发，以及上游为 Responses 的 Chat、Messages 流：顶层 `error` 对象即使没有 `type`、只有数字 `code` 也算失败；这个数字不当作上游 HTTP 状态码，写给客户端时使用字符串。无 `type` 的失败确认之后，再来的 `response.completed` 不会把它改成成功。`error` 为 `null`、空对象，以及正文里回显的错误文字不算失败。
- 原生 OAuth 上游先给出字符串 `type:"error"`、随后又给出合法 `response.completed` 时，流式和非流式都继续按成功结束。通用的 SSE 事件名（如 `message`、`error`）不会覆盖合法 payload 的结束类型。

Chat Completions 上游（包括原样转发的 raw Chat，以及转为 Responses、Messages 的流）以下列任一情况结束时算正常结束：给出有效的 `finish_reason`；收到 `[DONE]`；或者最后一段正文之后给出完整的输入、输出用量（显式 0 也算），然后连接正常关闭。只收到早期、部分或空的用量不算；正常结束之前读取出错、以损坏的帧结尾或收到明确的错误，按上表写一次终止帧。中间无法解析的保活帧之后仍收到合法事件的，继续读取。已经正常结束、只是缺少用量的，照常成功。按“完整用量后正常关闭”判定为成功的，运维记录中不算收到了上游结束事件。

非流式 Chat 与 Responses 请求的上游是 Anthropic 时，网关把 Anthropic 的流拼成一个响应：收到 `message_stop`、带有效 `stop_reason` 的 `message_delta` 或兼容的 `[DONE]` 时返回成功；在此之前断流、读取出错或收到错误时返回 502，保留已读到的用量。已经正常结束后连接关闭，或者缺少结尾的用量，不影响成功。

`/v1/chat/completions` 转为 Responses 的流式请求（见 [OpenAI](../platforms/openai.md#chat-completions-转-responses)）另有以下结束方式。错误按上表只写一次，之前不会发出成功的 `finish_reason`：

- 上游没有有效结束：Responses 上游以 `response.completed`、兼容的 `response.done` 或 `response.incomplete` 结束；合法结束事件没有 status、done 没带 response 或 incomplete 未给原因时沿用正常结束。中途断开且没有有效结束事件、`response.failed` 或取消时以错误结束，不补成 `stop`。非流式请求也一样，从 done 事件恢复出了正文不代表成功结束。
- 工具参数：上游生成的普通函数参数不是合法 JSON 时以错误结束，不修补；成功结束而参数为空时按 `{}` 交付。明确的输出上限截断以 `length` 结束，incomplete 未给原因或工具参数不完整时以 `stop` 结束，都不会以 `tool_calls` 交给客户端执行。流内事件之间的同一调用身份或参数与已发部分矛盾时以错误结束；结束事件更换 item ID 则按内容与顺序对应。

Responses、Messages 请求转为 Chat Completions 或 Anthropic 上游时（见 [其他平台](../platforms/others.md#协议转换)）：

- 长度截断与内容过滤是合法结束：Responses 以 `response.incomplete` 结束，事件类型与响应体的 `status` 一致；只有转换出错时才以 `response.failed` 结束。Anthropic 上游的 `pause_turn` 按错误结束。
- 转为 Responses 时，Chat 上游因 `length` 截断的工具调用不发送参数完成事件，也不发送完成的工具项（即使参数是合法 JSON 或为空）；已收到的文本、参数片段和用量照常保留。
- 上游生成的工具参数不是合法 JSON，或工具调用片段无法确定归属、位于非 0 的 choice 中时，按上表写终止帧，不会删掉该调用后报成功。转为 Messages 时，工具参数未完成前不会因交错的正文关闭工具块；长度或过滤结束时保留参数片段和排队正文，不补空参数。

经 `/v1/messages` 转为 Responses 的流式请求（见 [OpenAI](../platforms/openai.md#claude-code-与-opencode)）另有以下结束方式。`event: error` 只写一次，之后不再发送 `message_stop`：

- 上游失败或取消：以 `event: error` 结束。截断、内容过滤与拒答仍按正常结束返回，停止原因见上述链接中的表。
- 工具参数：普通函数的参数不是完整的 JSON 对象时以 `event: error` 结束；成功结束而参数为空时按 `{}` 交付，截断或断流时的空参数不补成 `{}`。完成事件中的参数与已发出的增量相同时不重发，只多出尾部时补发尾部，互相矛盾时以 `event: error` 结束，已发出的参数不会再发一遍；Read 工具去掉空 `pages` 不算矛盾。
- 并行工具：按上游输出顺序逐个交付。前一个工具的参数完成之前，后面的工具以及随后的正文、思考与搜索块先暂存，完成后依次发出。重复的完成事件不会重复结束块。结束事件里的输出列表按 item ID、call ID 对应已流式的工具，排列与流式顺序不同、缺少或换了 item ID 时不重复交付、不报错；流式事件里同一工具的 item ID、call ID 或输出位置互相矛盾，或参数未完成就断流时，以 `event: error` 结束。

Gemini 分组的 `/v1/chat/completions`、`/v1/messages` 流式请求（见 [Gemini](../platforms/gemini.md#兼容入口参数)），遇到无法转换的结束原因（如 `MALFORMED_FUNCTION_CALL`），或工具参数不是 JSON 对象时，按上表写一次终止帧，保留已读到的用量，不再补发成功结束。

compact 心跳提交后的失败也只写一次 `response.failed`；已确认请求模型时携带映射前的 `response.model`，未观察到模型时不猜测。

### `response.failed` 的重试时间

`errors.failed_frame_retry_after` 开启时（默认），合成的 `response.failed` 在 `error.headers.retry-after` 中给出重试时间（Codex 从这里读取），正文 “try again in Ns” 的 N 与之相同：

- 本请求还有健康账号：1 秒。
- 组内没有健康账号且能算出最早恢复时间：2–10 秒。
- 按快照算不出恢复时间，但最后一个可重试的上游失败（429 或 5xx）给出了有效的 `Retry-After`：采用它，夹到 2–10 秒。
- 以上都没有给出建议，但上游的失败带了有效的等待提示（HTTP 头、`response.error.headers`、`error.headers` 或错误正文，取其中最晚的恢复时刻）：采用剩余等待，夹到 2–10 秒。同一账号仍按上游原始时刻恢复，不受这个限幅影响。
- 恢复时间未知：不带。
- 回合封顶、用户自身余额不足、内容拒绝，以及模型不存在、请求或鉴权错误等不可重试的失败：不带，并去掉正文里的重试句。

### 正文前失败的换号

上游已经发出开场事件、但还没有正文时失败（过载、服务忙、断流、读错），网关会换号重试，客户端只看到成功那次尝试的开场：Responses 只有一个 `response.created`（新账号的 ID），Chat 只有一个 role 分块，Messages 只有一个 `message_start`。包括工具被 length 截断在内的 `response.incomplete`、内容拒绝与只有用量的合法空回答照常返回，不重放。一旦写出正文（哪怕只写出一部分），就不再换号。

非流式 Responses 聚合时按输出项与内容索引补齐 delta 和 done，部分终态不会丢掉其他文本或工具结果；终态已明确给出的值（包括空值）优先，未知输出项与 compaction 字段保留。

Responses 的 `response.done` 按其中的 `status` 处理：`failed`、`cancelled` 按失败结束，`incomplete` 照常返回；`response.completed` 的状态必须与事件类型一致。流式请求在没有 `response` 结束事件时，以下两种结束仍算正常：收到 `[DONE]`；或最后一段内容之后给出了完整的输入、输出用量，然后连接正常关闭。之后若又来新内容、读错或坏帧，后一种不再算正常。运维记录中，这两种结束不算作收到了上游结束事件。

raw Chat 的 role 与无内容开场也先暂存，和请求是否达到 64 KiB 无关；完整错误帧先分类，正文前失败不会泄露该次尝试的开场。正文后只写一次 Chat 错误终态并停止，不再续发成功内容。暂存仍受原 8 MiB 上限约束。

### 中转伪保活帧

部分中转会插入 `SSE-Keep-Alive` 空 delta 帧，或在 `response.created` 之前发出空字符串、纯空白的 delta。网关直接丢弃它们，不转发、不算首个输出（openai-python 遇到会报错），但仍算上游存活，不会因此被判停滞。`response.created` 之前的非空 delta 是真实内容，照常立即转发；这类上游下严格 SDK 仍可能报 “Expected to have received response.created”。

Anthropic 上游（原生 Messages 与协议转换）发来的 `ping` 和 SSE 注释只说明连接还在，不推迟首输出截止与停滞检测的计时；用量（包括 0）、正文、控制与结束事件照常推进计时。上面所说的中转伪保活帧仍算上游存活，规则不变。

## 心跳

心跳不算输出、不计费，也不妨碍换号；网关发给客户端的心跳不会延长等待上游业务进展的期限。

| 入站协议 | 首个输出前的心跳 |
|---|---|
| Responses（原生、透传） | Codex 客户端收到完整事件 `event: ping` / `data: {"type":"ping"}`；其他客户端收到 SSE 注释行 `:` |
| Chat Completions | SSE 注释行 `:` |
| Anthropic Messages | `event: ping`；原生 Anthropic 与 API Key 直通在开场暂存期间写注释行 `: ping` |

排队等待并发槽位或用户消息队列时的 ping 同样按入站协议选择格式。之后若遇到计费等错误，Responses 写 `response.created` 后跟 `response.failed`，Chat 写 `error` 后跟 `[DONE]`，不会写裸 JSON。

心跳提交 200 时只写网关自身的流式响应头（`Content-Type`、`Cache-Control` 等），失败尝试的上游响应头不会写出。原生 Responses 在第一次心跳提交时只额外带上本次尝试的 `x-codex-turn-state`，以保持 Codex 回合内的状态连续性；OpenAI Responses 透传在心跳提交时带出该次尝试的全部上游头。

## 请求修复与重放

以下情况网关在本地修复请求后，在同一账号上重放，客户端不感知失败尝试。重放消耗本请求预算中的尝试次数，预算不足时不重放；密文拒绝按下述专用错误返回，其他错误照常返回。

| 上游拒绝 | 网关处理 |
|---|---|
| 明确点名不支持的参数（结构化 `code`/`param`，或 “Unsupported parameter: X”） | 删除请求顶层的 `top_p`、`metadata`、`temperature`、`frequency_penalty`、`presence_penalty`、`max_output_tokens`、`truncation` 中被点名的那个后重放；每个请求合计最多 6 次，同一请求体不重放第二次。API Key 账号显式发送的 `reasoning.effort: none` 被第三方上游以 400 明确拒绝（`param` 为 `reasoning.effort` 或 `reasoning`）时，也在这一预算内删除后重放一次，并记录 `reasoning.effort/omit` 降级；其他字段被拒时原样返回 |
| 明确拒绝 content part 上的 `cache_control` | 删除该请求所有 content part 上的 `cache_control`，重放一次；顶层、消息级与工具参数里的同名字段保留 |
| 无法解密或校验请求中的加密内容（`invalid_encrypted_content`、`encrypted_context_invalid` 等，密文通常来自别的账号） | 按请求级处理，不罚号。OpenAI 原生、原样转发及 Chat/Messages 转为 Responses 的请求在第一个内容之前遇到时，只修复上游唯一定位的 reasoning 或 compaction 项后重放一次；reasoning 保留 id、summary 与非 null content，其他项原样保留。重放后上游又指出另一项时，说明不止一项失效，再清理其余顶层 reasoning 的密文、丢弃带密文的 compaction 后重放一次。上游没有给出位置，或给出的项 ID 在请求里找不到时：请求顶层只有一项带密文的 reasoning 或 compaction 就修复该项；有多项时清理全部顶层 reasoning 的密文、丢弃带密文的 compaction 后重放一次，并在请求级 `degradations` 中记录 `input[].encrypted_content/omit`。位置冲突、重复 ID 或越界时不做猜测性清理 |
| 上游会话状态拒绝 | 按请求级拒绝处理，不罚号、不计会话失败；原生与透传 Responses 在输出前可替换协作消息与工具输出中的密文槽位后重放或换号一次 |
| API Key 中转拒绝回放 `web_search_call` 历史（“response protection is unavailable”） | 补上 web_search 工具声明后重放一次；成功后该账号 6 小时内同类请求直接补声明。仍失败时返回 502 并说明原因，不换号 |
| Claude 请求分发到 GPT 中转时，续链（`previous_response_id`）被笼统的 4xx 拒绝 | 去掉续链、按完整历史重放一次，并对该会话在该账号上关闭续链。指名其他参数的错误、上下文超长、内容审核、模型不存在等照常返回 |

原生、原样转发及 Chat/Messages 转为 Responses 的 HTTP 请求明确拒绝 reasoning/compaction 密文、但位置冲突、重复 ID、越界、预算不足或上述修复后仍被拒时，返回 400 `invalid_request_error`，`error.code` 为 `encrypted_content_rejected`。上游提供的脱敏 `param` 保留；唯一位置说明只删除被指认项重试，否则提示用完整可读历史重建请求或新建会话。脱敏后的上游 code、message 与位置附在 `Upstream detail:` 后，不回显密文或凭据。不换号、不罚号、不计会话失败；尚未提交时清除 `Retry-After`、写 `x-should-retry: false`，已提交的流保持原 HTTP 状态并只写一次协议错误终态。

账号只支持 Chat Completions 的 API Key 路由（以及 Grok API Key）收到 Responses 格式的正文时，网关在发送前把它转换为 Chat 请求：能表达的字段照转，不能表达的按既有规则省略并记录降级，然后只发送一次；这一步不是重放，不消耗重放次数。普通的 `messages` 请求不受影响。

网关会记住被上游拒绝后去掉的密文项，下次请求中唯一命中同一项时，在第一次发送前就去掉它。这份记录按会话、账号、上游地址和本次实际使用的凭据分开保存：换账号、换 API Key、换实例、重启或记录过期后都不沿用，第一次发送保留原文；旧版本按会话保存的记录不再读取。即使没有记录，本次被明确拒绝后仍按上面的规则修复。

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
| 输出后上游异常结束（流内错误、缺终态、读错、停滞，以及经 `/v1/messages` 转为 Responses 时工具参数无效或与已发增量矛盾） | 按已读到的用量计费一次 |
| 换号前失败的尝试 | 不向用户计费 |

raw Chat 以“完整用量后正常关闭”判定为成功的响应，按读到的用量计费；只是缺少用量，不会把已经正常结束的响应算作失败。

原生 Anthropic 与 API Key 直通在开场暂存期失败并换号的尝试，不向用户计费、不产生用量记录；上游可能已对这次尝试的输入计费，这部分成本由网关承担。

上游的完整响应只是缺少用量时照常返回；使用记录会标出“上游未给用量”，与上游给出的真实 0 区分。流式和非流式拼装都保留中途已拿到的用量，结束事件不带用量时不会把它清零。HTTP 200 正文明确表示失败时不当作成功计费；失败流没有向客户端交付语义内容时不写用量。
