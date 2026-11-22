# OpenAI（ChatGPT / Codex）

本文介绍如何把 OpenAI 账号接入 XY2API：ChatGPT OAuth 与 API Key 两类账号的添加方式、OpenAI 分组对外暴露的端点、Codex CLI 等客户端的配置、Responses WebSocket 的相关配置，以及 `gpt-6.1-sol` 等模型的支持说明。部署 XY2API 的管理员和使用 Codex、OpenAI 兼容客户端的用户需要阅读。

## 账号类型

在「账号管理 → 添加账号」中选择平台 **OpenAI**：

| 界面选项 | 账号 `type` | 说明 |
|---|---|---|
| OAuth（ChatGPT OAuth） | `oauth` | ChatGPT 订阅账号，请求转发到 Codex 后端 |
| API Key（Responses API） | `apikey` | OpenAI 官方 API 或兼容 OpenAI 协议的上游，填写 Base URL 与 API Key |

OAuth 账号有多种添加方式，都在添加账号弹窗中选择：

| 添加方式 | 说明 |
|---|---|
| OAuth 授权 | 生成授权链接，在浏览器登录后，把跳转到 `http://localhost.../auth/callback?code=...` 的完整地址或其中的 `code` 粘贴回来 |
| 手动输入 RT / Mobile RT / AT | 批量粘贴已有的 Refresh Token、Mobile Refresh Token 或 Access Token，每行一个 |
| Codex OAuth auth.json / AT 导入 | 粘贴 Codex 的 `auth.json` 或 accessToken，每行一个 |
| Agent Identity auth.json | 导入 `auth_mode=agentIdentity` 的 Codex `auth.json`，不保存 OAuth access token 与 refresh token，每次上游请求动态签名 |
| Codex Personal Access Token | 输入 `at-` 开头的 PAT，先经 OpenAI whoami 校验再创建；不保存 refresh token |

服务器无法直连 OpenAI 时，OAuth 请求会失败并提示 `OPENAI_OAUTH_PROXY_REQUIRED`，请在弹窗中选择可访问 OpenAI 的代理。开启两步验证的账号可以用邮箱、密码和 TOTP 登录导入，见 [OpenAI 账号 2FA 登录导入](../guides/openai-totp.md)。

对应的管理接口位于 `/api/v1/admin/openai` 下：`generate-auth-url`、`exchange-code`、`refresh-token`、`create-from-oauth`、`create-from-codex-pat`、`accounts/:id/refresh`、`accounts/:id/quota`（查询额度）、`accounts/:id/quota/refresh`、`accounts/:id/reset-quota` 等。

## 在管理后台添加

1. 按上表添加账号，需要时选择代理。
2. 创建平台为 OpenAI 的分组并加入账号。希望 Claude Code 也能使用该分组时，在分组的「OpenAI Messages 调度配置」中打开「允许 /v1/messages 调度」，并按需配置 Opus、Sonnet、Haiku 系列的默认映射模型。
3. 用户创建绑定该分组的 API 密钥，在「使用密钥」弹窗中获取 Codex CLI、Codex CLI（WebSocket）、Claude Code（开启 Messages 调度后显示）和 OpenCode 的配置。

## 对外端点

鉴权使用 `Authorization: Bearer <密钥>` 或 `x-api-key: <密钥>`。

| 端点 | 说明 |
|---|---|
| `POST /v1/responses`、`/responses`、`/backend-api/codex/responses` | OpenAI Responses；`/responses/*` 子路径只放行可转发的子路径，其余返回 `404` |
| `GET` 上述 Responses 路径 | Responses WebSocket（Codex CLI 的 WebSocket 模式） |
| `POST /v1/responses/input_tokens` | Responses 输入 Token 计数，不经过生成与用量记录流程 |
| `POST /v1/chat/completions`、`/chat/completions` | Chat Completions |
| `POST /v1/messages` | Anthropic Messages 格式，转换为 Responses 后转发；需要分组开启「允许 /v1/messages 调度」 |
| `POST /v1/messages/count_tokens` | Token 计数 |
| `POST /v1/embeddings`、`/embeddings` | 仅 OpenAI 分组可用 |
| `POST /v1/images/generations`、`/v1/images/edits` 及异步、批量接口 | 生图，见 [异步图片任务](../api/images.md)与[批量生图](../api/batch-images.md) |
| `GET /v1/models` | 模型列表；带 `client_version` 查询参数时返回 Codex 模型清单格式 |
| `GET /backend-api/codex/models` | Codex 模型清单（`chatgpt_base_url` 模式） |
| `POST /v1/alpha/search` | Codex 使用的独立搜索端点 |
| `POST /v1/live`、`/backend-api/codex/realtime/calls` | ChatGPT Live 会话，单会话上限由 `gateway.live.max_session_duration_seconds` 控制（默认 3600 秒） |

所有推理 HTTP 入口拒绝重复的顶层 `model` 字段（含大小写和 JSON 转义后的同名键），返回 `400`；OpenAI 封套为 `invalid_request_error`，`code=duplicate_model`、`param=model`。嵌套 schema 中的 `model` 不受影响，multipart 文件内容不作 JSON 扫描。

Responses（含 compact）的 JSON 正文必须是合法 UTF-8。非法编码在正文改写和选号前返回 `400 invalid_request_error`，`code=invalid_utf8`、`param=body`，消息包含解压后原始正文中首个非法字节的 0 起始偏移；合法 BOM、Unicode 与已有字符串控制字节兼容保持不变。

API Key 原生非流式 Images 没有有效图片时返回 `invalid_image_response`（`param: data`）。响应尚未提交时为 HTTP 502；JSON 保活已提交时停止后续心跳，以 HTTP 200 返回相同错误封套。少于请求张数时按实际有效图片数收费。

API Key 账号的请求显式写了 `reasoning.effort: none` 或 `reasoning_effort: none` 时，不论 Base URL 是否为 OpenAI 官方地址，都原样发送；最终模型明确不支持 `none` 时按[推理档位策略](../guides/groups.md#推理档位策略)映射。第三方上游以 400 且 `param` 为 `reasoning.effort` 或 `reasoning` 明确拒绝时，在既有修复预算内删除后重发一次，并记录 `reasoning.effort/omit` 降级；重发不会补回 `none`。`/v1/chat/completions` 收到 Responses 格式的正文时保留 `metadata`。Chat 请求的 `stream_options` 在发往 Responses 上游时省略，并记录 `stream_options/omit` 降级，因此 `include_usage` 不起作用，返回的用量仍是上游给出的值。

`/v1/chat/completions` 收到 Responses 格式的正文、而账号上游只支持 Chat Completions（含 Grok API Key）时，网关在发送前把它转换为 Chat 请求，保留能表达的多模态内容与工具历史；字段非法或目标确实无法表达时返回 400，`param` 指向该字段。

### Chat Completions 转 Responses

`/v1/chat/completions` 请求转换为 Responses 发往上游，响应再转换回 Chat Completions。下文的“返回错误”指：非流式返回错误 JSON；流式已经输出后写 `data: {"error":…}` 和 `data: [DONE]`，见 [网关韧性](../guides/gateway-resilience-client.md#已提交后的失败)。

| 请求参数 | 处理 |
|---|---|
| `max_completion_tokens`、`max_tokens`、`max_output_tokens` | 按这个顺序取第一个非 null 值；进行 Chat 协议转换时，选中值须为正整数，否则返回 400。其他同时填写的合法别名记录 `<字段>/omit` 降级。余额的在途预留按同一顺序估算，解析失败时沿用原估算方式，不因此拒绝请求。发往 Responses 时 1–15 提高到 16 并记录 `max_output_tokens/map`，16 及以上原值发送（`/v1/messages` 转 Responses 也一样） |
| `stop` | 合法的字符串或数组省略，记录 `stop/omit` 降级。Responses 没有停止序列，返回文本可能更长；空字符串等非法值仍返回 400。发往 Anthropic 上游时照旧转为 `stop_sequences` |
| `metadata`、`user` | 字符串表与字符串原样转发；类型不符时照旧省略该字段并记录 `<字段>/omit`，不报错；null 视为未提供 |
| `text.verbosity`、`verbosity` | 转发；两者都有时用 `text.verbosity`，顶层别名记录 `verbosity/omit` 降级 |
| `response_format`、`text.format` | 两者都有时用 `response_format`，`text.format` 记录 `text.format/omit` 降级；只有 `text.format` 时按 Responses 格式校验后使用，格式非法时照旧省略并记录 `text.format/omit`，不报错；`response_format` 非法仍返回 400。Chat 的 `response_format.json_schema` 没写 `strict` 时按 `false` 发送，显式值原样保留 |
| `include` | 字符串数组按原顺序去重后转发，并保留 `reasoning.encrypted_content`；类型不符时省略该字段并记录 `include/omit`，不报错 |
| `reasoning` | 接受档位字符串，或含 `effort`、`summary` 的对象；同时有非空 `reasoning_effort` 时以后者为准。其他形状与未知子字段照旧忽略，不因解码新增报错。显式的 `summary` 保留；只给档位、没有 `summary` 时补 `auto`；只给 `summary` 时不补档位 |
| 历史中的 `tool_calls[].function.arguments` | 原样保留，空字符串按 `{}` 处理；不会因为上一轮参数不是完整 JSON 而新增本地拒绝 |

表外的其他未知字段不转发，`store` 固定为 false。GPT-6.1 Terra 按推理模型处理：最终档位为 `none` 时保留 `temperature`、`top_p`，其他档位不发送这两个参数。

| 响应 | 处理 |
|---|---|
| `finish_reason` | 非流式与流式相同：明确的 `max_output_tokens` 截断为 `length`，内容过滤为 `content_filter`，两者优先于 `tool_calls`；有完整工具调用时为 `tool_calls`；拒答为 `stop`，正文放在 `message.refusal`（流式为 `delta.refusal`）。上游 `failed`、`cancelled` 返回错误；其他未列出的原因按正常结束处理，只有工具调用都已结束且参数完整时才为 `tool_calls`。合法结束事件缺状态、`response.done` 没带 response 时保持既有正常结束行为；incomplete 未给原因时照旧为 `stop`，不以 `tool_calls` 结束 |
| `reasoning_content` | 逐项取非空的 summary，没有时取 `reasoning_text`，两者不拼接；流式中的原始推理文本在该项结束后才发出。`encrypted_content` 不会作为思考文本输出 |
| 工具参数 | 上游生成的普通函数参数不是合法 JSON 时返回错误，不修补；成功结束而参数为空时按 `{}` 交付。custom 工具调用以 `function` 形式返回，`arguments` 为原始输入文本 |
| 流式工具参数 | 只在 `function_call_arguments.done`、`output_item.done` 或结束事件里给出的参数也会发出，只补还没发出的部分；重复完成事件和结束后迟到的增量不会重复输出。流内事件之间的同一调用身份或参数前后矛盾时返回错误；结束事件重新生成 item ID 不会因此报错 |
| 流式正文 | done 事件仅补充延长了已发正文的部分；修整、替换已发正文不会使流失败或重复输出。结束事件中的正文仅在此前未发出可见正文时补齐 |

上游以流式返回、网关拼成非流式结果时（Chat、Responses、Messages 的非流式请求都适用），正文与拒答按每个输出项的每段内容分别恢复：结束事件中的非空内容优先，其次是该段的 done 事件，最后是增量；同一段不会重复拼接。没有项标识的增量接到当前项，`reasoning_text` 内容部件不会阻断正文恢复。只在 done 事件中出现的网页搜索项会保留，custom 工具调用保留原类型。恢复出了正文不代表上游成功结束，没有有效结束事件时仍返回错误。

`/v1/responses` 的流式事件与非流式结果中，function_call 项原样保留上游的 `encrypted_content` 与 `encrypted_function_args`（缺省、null 与空值不混同），网关不解密；Chat Completions 没有对应字段，转为 Chat 时不携带。

## 客户端配置

### Codex CLI

在「使用密钥」中选择 **Codex CLI**。弹窗会读取当前密钥可用的模型目录，自动选择模型和思考深度。已获取的上游实时模型元数据优先于网关内置目录。`base_url` 必须以 `/v1` 结尾，Codex 会直接拼接 `/responses`。

认证模式有两种：

| 模式 | 写法 | 适用 |
|---|---|---|
| 兼容模式（默认） | `requires_openai_auth = true`，密钥写在 `auth.json` 的 `OPENAI_API_KEY` | 旧版 Codex 配置 |
| API Key Mode | `requires_openai_auth = false`，密钥写在 `experimental_bearer_token`，并带 `http_headers = { "x-openai-actor-authorization" = "local-image-extension" }` | 需要授权客户端图片执行器时；保存后必须完全退出并重启 Codex Desktop 或 CLI，再新建 task |

模型别名的能力来自唯一的映射目标；配置合并追加的别名与完整目录使用同一能力来源。多个账号将同一别名映射到不同目标时，不猜测其中一个目标的能力，已获取的上游目录条目保持不变。

模型目录来源有两种：**远程目录**（Codex 0.156.0 及以上，配置 `model_catalog_url` 并开启 `api_key_model_discovery`）和 **本地文件**（旧版客户端，下载目录保存为 `~/.codex/codex-models.json` 并配置 `model_catalog_json`）。完整目录超过 1 MiB 时弹窗会自动改用本地文件。

兼容模式 + 远程目录的 `~/.codex/config.toml` 示例（Windows 为 `%userprofile%\.codex\config.toml`），以下内容需放在文件开头：

```toml
model_provider = "OpenAI"
model = "gpt-5.6-sol"
review_model = "gpt-5.6-sol"
disable_response_storage = true
network_access = "enabled"
windows_wsl_setup_acknowledged = true

[model_providers.OpenAI]
name = "OpenAI"
base_url = "https://your-xy2api.example.com/v1"
model_catalog_url = "https://your-xy2api.example.com/v1/models"
wire_api = "responses"
requires_openai_auth = true

[features]
api_key_model_discovery = true
goals = true
```

同目录的 `auth.json`：

```json
{
  "OPENAI_API_KEY": "sk-your-xy2api-key"
}
```

### Codex CLI（WebSocket）

选择 **Codex CLI (WebSocket)** 时，在 `[model_providers.OpenAI]` 中增加 `supports_websockets = true`，并在 `[features]` 中增加 `responses_websockets_v2 = true`，其余与上面相同。

### Claude Code 与 OpenCode

分组开启「允许 /v1/messages 调度」后，Claude Code 的配置与 Anthropic 分组相同（`ANTHROPIC_BASE_URL` 填根地址，不带 `/v1`），见 [Anthropic](anthropic.md#claude-code)。OpenCode 配置文件为 `~/.config/opencode/opencode.json`，按弹窗内容复制。

`/v1/messages` 请求转换为 Responses 发往上游，响应再转换回 Messages。下表中的“返回错误”指：非流式返回错误 JSON；流式以 `event: error` 结束、不发送 `message_stop`，见 [网关韧性](../guides/gateway-resilience-client.md#已提交后的失败)。

| 项 | 处理 | 说明 |
|---|---|---|
| 工具的 `strict` | 映射 | 显式 `true` 仅在 schema 各层对象都设为 `additionalProperties:false` 且所有属性都列入 `required` 时保留；否则按 `false` 发送，并记录 `tools[i].strict/omit` 降级，schema 不改写。显式 `false` 与未指定仍按 `false` 发送 |
| 工具的 `allowed_callers` | 省略并记录 | 未提供、`null` 或只含 `direct` 时正常转换。其他字符串列表（包括空列表、空字符串）在 Responses 中无法表达，网关省略这项限制并记录 `tools[i].allowed_callers/omit` 降级；工具声明与 schema 保留，模型仍可直接调用该函数。不是数组或成员不是字符串时返回 400，`param` 为 `tools[i].allowed_callers` |
| 历史 thinking 的签名 | 过滤 | 去掉首尾空白后，空值、`gAAAA` 开头的值以及 CC Switch 的 `ccswitch-openai-reasoning-v1:` 本地包装（包括内容损坏的）不作为 `encrypted_content` 发出；其他签名（包括 CC Switch 的其他版本）照旧发送，正文与工具历史不变 |
| 结束原因 | 映射 | `completed` 为 `end_turn`，有完整工具调用时为 `tool_use`；`incomplete` 的 `max_output_tokens` 或未给原因为 `max_tokens`，`content_filter` 为 `refusal`；正常完成但含拒答时为 `refusal`。`failed`、`cancelled` 或无法确认结束状态时返回错误。其他未列出的非空原因按正常结束处理，并记录 `status/map` 或 `incomplete_details.reason/map` 降级 |
| 拒答（`refusal`） | 映射 | 拒答正文作为 `text` 块返回，非流式与流式相同；流式只补发尚未发出的部分，不重复 |
| reasoning | 映射 | 有非空 summary 时只输出 summary，否则输出原始推理文本（`reasoning_text`），两者不拼接。summary 增量实时转发；原始推理文本在该 reasoning 项结束（或响应结束）时一次给出。`encrypted_content` 作为 thinking 块的 `signature`，在块结束前发出 |
| 网页搜索结果 | 映射 | 只对已完成的 `web_search_call` 生成结果：取上游实际返回的 HTTP(S) 来源，重复的 URL 只保留第一条，只填上游给出的标题和内容；`open_page` 使用实际打开的 URL；上游没有来源时结果为空列表 |
| `custom_tool_call` | 映射 | 转为 `tool_use`，自由文本输入包装为 `{"input":"原文"}`（空字符串同样包装），保留调用 ID 与名称；流式在输入完成后一次给出 |
| 工具参数 | 校验 | 普通函数的参数必须是完整的 JSON 对象，成功结束而参数为空时按 `{}` 交付；坏 JSON、数组、`null`、标量，以及带 `encrypted_function_args` 的加密参数（Messages 无法表达）返回错误，不修补、不解密 |

### 反向代理

在 Nginx 后使用 Codex CLI 时，需要在 `http` 块中加入 `underscores_in_headers on;`。Nginx 默认丢弃名称含下划线的请求头（如 `session_id`），会导致多账号下的粘性会话失效。更多见 [反向代理](../deployment/reverse-proxy.md)。

## Responses WebSocket 配置

以下配置位于 `config.yaml` 的 `gateway.openai_ws`，也可用对应环境变量设置（键名大写、`.` 换成 `_`）：

| 配置键 | 默认 | 说明 |
|---|---|---|
| `client_first_message_timeout_seconds` | `30` | 完整读取并解压首条客户端消息的总超时；大上下文或慢链路可调到 120–300 |
| `ingress_inter_turn_idle_timeout_seconds` | `300` | 两轮之间空闲超过该时长即关闭客户端连接；`0` 关闭此限制 |
| `max_ingress_connections_per_api_key` | `64` | 每个 API 密钥在全部实例上的 WebSocket 连接上限；`0` 关闭 |
| `mode_router_v2_enabled` | `false` | 新版模式路由。关闭时忽略账号级 WS 模式（包括 `http_bridge`）；环境变量 `GATEWAY_OPENAI_WS_MODE_ROUTER_V2_ENABLED` |
| `force_http` | `false` | 全局强制上游使用 HTTP/SSE 而不是 WebSocket，不需要开启 `mode_router_v2_enabled`；环境变量 `GATEWAY_OPENAI_WS_FORCE_HTTP` |

- 连接上限通过 Redis 协调：租约 60 秒、每 20 秒续期；某个进程在整个租约周期内无法确认租约时，会关闭本地连接，而不是在全局上限之外继续运行。
- 出口代理或网络频繁断开上游 WebSocket 时，可设置 `force_http: true`。它不改变客户端侧协议，也不强制 HTTP/1.1；代理不兼容 HTTP/2 时另设 `gateway.openai_http2.enabled: false`（`GATEWAY_OPENAI_HTTP2_ENABLED=false`）。
- 需要“客户端 WebSocket、上游 HTTP”时，先开启 `mode_router_v2_enabled`，再把账号的 WS 模式设为 `http_bridge`。
- 默认的 Docker Compose 文件只传递 `GATEWAY_OPENAI_WS_FORCE_HTTP` 与 `GATEWAY_OPENAI_HTTP2_ENABLED`；其他 `gateway.openai_ws` 键要用环境变量设置时，需要把变量加入 compose 文件中 `xy2api` 服务的 `environment` 列表，或改用 `config.yaml`。
- 这些设置应写在部署持久保存的 `.env` 或 `config.yaml` 中，不要只改运行中的容器，否则镜像更新或重建容器后会丢失。

## 模型支持：GPT-6.1 Sol

网关内置支持 `gpt-6.1-sol`：

- **名称**：模型目录、映射、计价和发往上游的 ID 都是 `gpt-6.1-sol`。大小写、下划线、`openai/` 等前缀和已知的思考深度、compact 后缀会精确归一到该模型；不认识的近似名称不会被映射过去。
- **思考深度**：支持 `low`、`medium`、`high`、`xhigh`、`max`。API Key 账号的 HTTP 请求在最终模型确定后，把显式的 `none`、`minimal` 映射为 `low` 发送，按 `low` 计费。OAuth 账号与 WebSocket（首帧、后续轮次和 `session.update`）仍在本地拒绝 `none`、`minimal` 与关闭 thinking。映射到其他模型的请求不套用这条限制。
- **工具调用**：只支持 Chat Completions 的账号无法为该模型执行工具调用，网关返回明确的请求错误（`gpt-6.1-sol requires Responses for tool calls`），不会静默丢弃工具，也不会把账号判为故障。
- **Codex**：默认思考深度 `low`。API Key 账号的 Codex 模型目录对该模型及其映射别名不启用 Responses Lite。
- **上下文**：API 与 OpenCode 口径为 context 1,050,000、最大输入 922,000、最大输出 128,000；Codex 模型描述符口径为 context 272,000、`max_context_window` 872,000。两个入口各按各自的口径。

内置默认价格（美元 / 百万 token）：

| 项目 | Standard | Priority | Flex / Batch |
|---|---:|---:|---:|
| 输入 | 2 | 4 | 1 |
| 输出 | 10 | 20 | 5 |
| 缓存写入 | 2.5 | 5 | 1.25 |
| 缓存读取 | 0.1 | 0.2 | 0.05 |

输入超过 272,000 token 时启用长上下文计价：输入 ×2、输出 ×1.5。分组是否启用阶梯计费、渠道自定义价格和显式零价仍然优先。

## 模型支持：GPT-6.1 Terra

- **名称**：`gpt-6.1-terra`，供应商前缀、大小写、下划线与已知推理/compact 后缀精确归一；未知日期或近似别名不自动匹配。
- **推理档位**：支持 `none`、`low`、`medium`、`high`、`xhigh`、`max`；`minimal` 按最近可用档位映射到 `none`。工具调用只能走 Responses 接口。OAuth 原生 Responses 的 `minimal` 同样先归一为 `none` 再发送。
- **Codex**：离线目录采用官方描述符，默认 `low`，context window 为 500000、max context window 为 872000。已获取的实时上游元数据优先，API Key 账号及其映射别名不启用 Responses Lite。目录中的 `ultra` 是 Codex 工作流能力，不代表 HTTP 推理档位。
- **路由**：仍遵守分组映射与受限目录；新增型号不会自动使未配置的账号可服务。

## 模型支持：GPT-6.2 Sol 与 GPT-6.2 Astra

- **名称与目录**：`gpt-6.2-sol`、`gpt-6.2-astra` 加入默认模型列表、白名单与映射预设；支持供应商前缀、大小写、下划线与已知推理/compact 后缀（如 `gpt-6.2-sol-high`），未知日期或近似名称不自动归一。仍遵守分组映射与受限目录；使用默认模型列表的分组（含无可调度账号时）的 `/v1/models` 会新增列出这两个模型，不改变调度；显式 mapping 或白名单仍按配置过滤。新增型号不会自动使未配置的账号可服务。
- **Codex**：内嵌描述符与默认提示词来自 openai/codex 官方快照，推理档位 `low`–`ultra`，默认 `low`；context window 分别为 500000（Sol）与 272000（Astra），max context window 均为 872000；支持图片输入与 Fast（`priority`）档。实时上游或账号元数据优先，已知后缀沿用相同描述符；API Key 账号及其映射别名关闭 Responses Lite。目录中的 `ultra` 是 Codex 工作流能力，不代表 HTTP 推理档位。
- **推理强度**：API Key 账号的 HTTP 请求把 `none`、`minimal` 映射为 `low` 发送；与 `gpt-6.1-sol` 不同，`/v1/messages` 的 `thinking.type=disabled` 和 Responses 透传也按 `low` 发送而不是拒绝。OAuth 账号与 WebSocket 在本地拒绝这两档及关闭 thinking。显式 `max` 原样保留。
- **请求**：`/v1/messages` 客户端直接携带已知 effort 后缀时拆为基名与 effort，并按基名计价，此时以带后缀名称配置的映射或渠道价不会命中；API Key 账号在其它情况下保留模型名和映射目标原样，`model_mapping` 的键仍区分大小写。`temperature`、`top_p` 在 Chat → Responses、Messages → Chat、Responses → Chat 转换及 Codex 协议路径中不发送，API Key 的原生 Responses 与 Chat 直转保留原值。
- **工具调用**：只走 Responses。明确标记为不支持 Responses 的账号不再承接带工具的 6.2 请求；已标记支持或尚未标记的账号不受影响。
- **缓存**：客户端未提供会话标识时自动补充 `prompt_cache_key`，按最终模型与下游 API Key 隔离。`/v1/messages` 的历史裁剪、续聊与 todo guard 不对 6.2 启用。
- **可选兼容**：`gateway.gpt62_chat_system_as_developer` 默认 `false`。开启后，6.2 的 Chat → Responses 转换将 `system` 消息在原位改为 `developer`，用于兼容会把 system 提升到不可缓存 instructions 的中转；实际缓存收益未经真实上游验证。
- **计费**：目录已有精确 6.2 价格时扣费不变。修复目录缺失或已知后缀误用其它模型家族价格的问题；缺失时也会启用 6.2 的长上下文与缓存写入价格，不完整渠道价卡的留空字段随之变化。供应商前缀模型也会匹配渠道价格（含显式零价与 Ultrafast 倍率）；分组和渠道显式配置仍优先。Ultrafast 售价沿用现有价卡；官方 API 采购价在太平洋时间 2027-03-01 零点前按 Standard 的 3 倍、之后 6 倍计算。免费 Fast 与免费 Ultrafast 分别设置，不按订阅套餐推断 Ultrafast 能力，也不新增官方 API 的 6.2 Ultrafast 目录广告。

## 相关功能

- [OpenAI 智商检测](../guides/iq-check.md)：按“账号 × 模型”检测降智并暂停调度。
- [Codex STATE](../guides/codex-state.md)：为 OAuth 账号获取并注入 STATE 票据。
- [OpenAI 模型降档识别](../guides/quality-routing.md)、[提示词缓存](../guides/caching.md)、[分组与 Fast 优先级](../guides/groups.md)。
- 同样经 OpenAI 网关转发的 Kimi、智谱、DeepSeek、MiniMax、OpenCode、Command Code、Cline 见 [其他平台](others.md)；这些平台的分组使用 `/v1/messages` 不需要开启「允许 /v1/messages 调度」。
