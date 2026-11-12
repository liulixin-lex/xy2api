# Anthropic（Claude）

本文介绍如何把 Anthropic Claude 账号接入 XY2API：支持哪些账号类型、在管理后台如何添加、网关对外暴露哪些端点、如何配置 Claude Code 等客户端，以及只对 Anthropic 平台生效的选项（OAuth 转发行为、API Key 账号的“Claude Code 请求兼容”、OpenAI 协议到 Messages 的字段转换）。部署 XY2API 的管理员和调用 Claude 模型的用户需要阅读。

## 账号类型

在「账号管理 → 添加账号」中选择平台 **Anthropic**，再选择账号类型：

| 界面选项 | 账号 `type` | 认证方式 | 适用场景 |
|---|---|---|---|
| Claude Code（OAuth / Setup Token） | `oauth` 或 `setup-token` | OAuth 授权、Setup Token（长期有效），或用 claude.ai 的 sessionKey 做 Cookie 自动授权 | Claude 订阅账号 |
| Claude Console（API Key） | `apikey` | 上游 API Key；认证头可选 `x-api-key` 或 `Authorization: Bearer` | Anthropic 官方 API 或兼容 Anthropic Messages 协议的上游 |
| AWS Bedrock（SigV4 / API Key） | `bedrock` | SigV4 签名或 Bedrock API Key | 通过 AWS Bedrock 调用 Claude |
| Vertex（Service Account） | `service_account` | Google Cloud Service Account JSON | 通过 Vertex AI 调用 Claude，建议配置模型映射，把客户端的 Claude 模型名映射到 Vertex 模型 ID |

OAuth 类账号的授权接口位于 `/api/v1/admin/accounts` 下：`generate-auth-url`、`exchange-code`（OAuth）、`generate-setup-token-url`、`exchange-setup-token-code`（Setup Token），以及 `cookie-auth`、`setup-token-cookie-auth`（sessionKey 自动授权）。一般直接在添加账号弹窗里按步骤操作即可。

## 在管理后台添加

1. 「账号管理 → 添加账号」，平台选 Anthropic，按上表选择账号类型并完成授权或填写凭据。需要代理才能访问上游时，在弹窗中选择代理。
2. 「分组管理」中创建平台为 Anthropic 的分组，把账号加入该分组。分组的调度、系统提示词等设置见 [分组](../guides/groups.md) 与 [账号调度](../guides/scheduling.md)。
3. 用户在「API 密钥」中创建绑定该分组的密钥，点击「使用密钥」即可获得各客户端的配置。

## 对外端点

Anthropic 分组的密钥可以调用以下端点。鉴权使用 `Authorization: Bearer <密钥>` 或 `x-api-key: <密钥>`；不接受 URL 查询参数里的密钥。

| 端点 | 说明 |
|---|---|
| `POST /v1/messages` | Anthropic Messages，原生协议 |
| `POST /v1/messages/count_tokens`（别名 `/messages/count_tokens`） | Token 计数 |
| `POST /v1/chat/completions`（别名 `/chat/completions`） | OpenAI Chat Completions 格式，网关转换为 Messages 后转发，见[协议转换字段表](#协议转换字段表) |
| `POST /v1/responses`（别名 `/responses`） | OpenAI Responses 格式，网关转换为 Messages 后转发，见[协议转换字段表](#协议转换字段表) |
| `GET /v1/models`、`GET /v1/models/:model` | 模型列表 |
| `GET /v1/usage` | 当前密钥的额度与用量（供 CC Switch 等工具读取） |

Embeddings、图片、视频、语音等端点只对 OpenAI 或 Grok 分组开放，Anthropic 分组调用会返回 `404 not_found_error`。Kimi、DeepSeek 等其他平台的端点范围见 [其他平台](others.md)。

## 客户端配置

### Claude Code

`ANTHROPIC_BASE_URL` 填 XY2API 的根地址，不带 `/v1`，Claude Code 会自己拼接 `/v1/messages`。

macOS / Linux：

```bash
export ANTHROPIC_BASE_URL="https://your-xy2api.example.com"
export ANTHROPIC_AUTH_TOKEN="sk-your-xy2api-key"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

Windows PowerShell：

```powershell
$env:ANTHROPIC_BASE_URL="https://your-xy2api.example.com"
$env:ANTHROPIC_AUTH_TOKEN="sk-your-xy2api-key"
$env:CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

需要持久化时，写入用户级配置 `~/.claude/settings.json`（Windows 为 `%USERPROFILE%\.claude\settings.json`）。该文件含 API 密钥，不要提交到项目仓库：

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "ANTHROPIC_BASE_URL": "https://your-xy2api.example.com",
    "ANTHROPIC_AUTH_TOKEN": "sk-your-xy2api-key",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

### 其他客户端

Cline、Cherry Studio、new-api 中转、OpenAI SDK 等客户端使用同一个 API 密钥，按客户端使用的协议填写 Base URL：

| 客户端协议 | Base URL | 端点 |
|---|---|---|
| Anthropic 兼容 | 站点根地址，例如 `https://your-xy2api.example.com`（不带 `/v1`） | `/v1/messages` |
| OpenAI 兼容 | 站点根地址加 `/v1`，例如 `https://your-xy2api.example.com/v1` | `/v1/chat/completions`、`/v1/responses` |

「使用密钥」弹窗的「其他客户端」一节给出同样的地址；分组开启「仅限 Claude Code」时不显示这一节，这类分组会拒绝普通客户端的请求或转到兜底分组。弹窗还为 Anthropic 分组生成 Codex CLI（经 `/v1/responses`）和 OpenCode（`~/.config/opencode/opencode.json`）的配置，按弹窗内容复制即可。

上游只接受 Claude Code 客户端的 API Key 账号，要开启 [Claude Code 请求兼容](#claude-code-请求兼容api-key-账号) 后才能服务这些客户端。

## OAuth 账号的转发行为

「设置 → 网关服务 → 请求转发行为」中的以下开关影响转发到 Claude OAuth 账号的请求：

| 界面名称 | 设置键 | 默认 | 作用 |
|---|---|---|---|
| 指纹统一化 | `enable_fingerprint_unification` | 开 | 统一共享同一 OAuth 账号的用户的 `X-Stainless-*` 请求头；关闭后透传客户端原始请求头 |
| Metadata 透传 | `enable_metadata_passthrough` | 关 | 透传客户端原始 `metadata.user_id`，不重写 |
| CCH 签名 | `enable_cch_signing` | 关 | 对转发请求的 billing header 做 CCH 哈希签名；关闭时保留原始占位符 |
| Claude OAuth System 注入 | `enable_claude_oauth_system_prompt_injection` | 开 | 为非 Claude Code 客户端的 OAuth 请求注入 Claude Code 形态的 system 块；可在「Claude OAuth 扩展提示词」中自定义，留空用内置内容 |

## Claude Code 请求兼容（API Key 账号）

有些 Anthropic API Key 上游（常见于第三方中转）只接受 Claude Code 客户端发出的请求。给这类账号开启「Claude Code 请求兼容」后，omp、Cline、Cherry Studio、new-api 中转、OpenAI 兼容客户端等普通客户端也能经它调用 Claude，客户端的地址、密钥和请求都不用改。

网关在转发前为请求补上 Claude Code 的请求形态：

| 项 | 网关的处理 |
|---|---|
| system | 去掉请求里已有的 billing 块（以 `x-anthropic-billing-header:` 开头的文本块）和重复的 Claude Code 身份块，在最前面放一个 billing 块和一个身份块；其余 system 块的内容与顺序不变 |
| 请求头 | `User-Agent: claude-cli/2.1.114 (external, sdk-cli)`、`X-App: cli`、`anthropic-version: 2023-06-01`；`anthropic-beta` 补齐 `claude-code-20250219`、`interleaved-thinking-2025-05-14`、`context-management-2025-06-27`、`prompt-caching-scope-2026-01-05`、`advisor-tool-2026-03-01`，客户端自带的其他 beta 保留（Beta 策略过滤的除外） |
| 会话身份 | 改写 `metadata.user_id`（`metadata` 的其他字段不变），设置 `X-Claude-Code-Session-Id`。两者按用户、API 密钥、上游账号和会话派生，不同用户、不同密钥互不相同 |

会话依次取：客户端会话头（`session_id`、`conversation_id`、`X-Claude-Code-Session-Id` 等）→ `metadata.user_id` 中的 session_id → 粘性会话标识；都没有时，每个请求各用一个随机会话。同一会话每轮生成的 system 相同。

### 启用

1. 在「账号管理」中创建、编辑或批量编辑 Anthropic「Claude Console（API Key）」账号，打开「Claude Code 请求兼容」。批量编辑时，两个开关都可以选「不修改」「开启」「关闭」。
2. 对每个要开放的模型做一次[账号测试](#账号测试)，不通过的模型从账号的模型映射中去掉。
3. 「接收新会话」默认开启。关闭后，已经粘性绑定在本账号的会话继续使用本账号，新会话不再分到本账号，用于下线前排空。停用兼容时先关闭「接收新会话」，等已绑定的会话结束后再关闭兼容。

账号 `extra` 中的配置字段（管理接口、导入导出使用）：

| 字段 | 取值 | 说明 |
|---|---|---|
| `anthropic_compat_profile` | `off`（默认）、`claude_code` | 字段缺失等于 `off`。其他值（包括旧值 `claude_code_v1`、`claude_code_v2`）保存时返回 `ANTHROPIC_COMPAT_PROFILE_INVALID`（`anthropic_compat_profile must be off, claude_code`）；对非 Anthropic API Key 账号设置返回 `ANTHROPIC_COMPAT_PROFILE_ACCOUNT_INVALID`。导入时旧值 `claude_code_v1`、`claude_code_v2` 自动改为 `claude_code` |
| `anthropic_compat_new_sessions` | `true`（默认）、`false` | 接收新会话；非布尔值返回 `ANTHROPIC_COMPAT_ADMISSION_INVALID` |
| `anthropic_compat_identity_seed` | 服务端生成 | 派生会话身份的种子，不通过接口或导出返回。只在 API Key、端点或认证方式（是否为 `Authorization: Bearer`）实际变化时更换：比较前去掉首尾空白和端点末尾的 `/`，端点留空等同 `https://api.anthropic.com`。单个编辑、批量编辑与 CRS 同步都按这一规则；新建、复制、导入账号时生成新种子 |

编辑账号时请求里不带前两个字段，则保留原值；只有明确保存 `off` 才关闭兼容。

兼容与以下账号或系统配置有冲突：

| 配置 | 要求 | 不满足时 |
|---|---|---|
| 自动透传（仅替换认证） | 关闭 | 保存时拒绝（`ANTHROPIC_COMPAT_PROFILE_CONFLICT`） |
| 请求头覆写 | `User-Agent`、`X-App`、`anthropic-version` 只能覆写成上表中的值；覆写 `anthropic-beta` 时要包含上表全部 5 个 beta；不能覆写 `X-Claude-Code-Session-Id` | 保存时拒绝（`ANTHROPIC_COMPAT_PROFILE_HEADER_CONFLICT`） |
| 「设置 → 网关服务 → Beta 策略」 | 不拦截本次请求带的 beta，不过滤上表的 5 个 beta | 保存时不检查；请求时该账号不参与选号，见[排查](#排查) |

### 支持范围

| 项 | 说明 |
|---|---|
| 入口 | `/v1/messages`、`/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`。Chat Completions 与 Responses 先按[协议转换字段表](#协议转换字段表)转为 Messages，与普通账号相同 |
| 模型 | 经账号模型映射后以 `claude-` 开头、且账号的模型映射或白名单允许的模型。`/v1/models` 按同样规则列出；账号没有配置映射或映射含通配符时，还包括网关默认 Claude 模型列表中符合条件的模型 |
| 验证 | 所有模型使用同一套请求头与 beta。haiku、sonnet 等模型在只接受 Claude Code 的上游上能否使用，网关没有实测，需管理员用[账号测试](#账号测试)逐个确认 |

### 会话与签名历史

兼容账号对 thinking 签名历史的处理与普通 API Key 账号完全相同。

| 情形 | 行为 |
|---|---|
| 同一会话的后续轮次 | 粘性调度把会话留在同一账号，thinking 签名发回产生它的账号 |
| 会话换到其他账号或模型 | 上游读不了的 thinking 块（例如 Sonnet 5.5、Haiku 5.5、Fable 5.2 的 thinking 只在产生它的上游账号有效）由上游直接丢弃，请求照常成功，模型看不到这部分推理 |
| thinking 之前的 system、tools 或消息变了 | 上游可能返回签名错误（400），网关按[签名重试](#签名重试)处理 |

已知代价：会话在普通账号与兼容账号之间切换后（例如账号限流或故障时换号），两边的 system 不同（兼容账号多了 billing 块与身份块），之后每轮都可能先收到签名 400、再重试，多一次上游往返。

#### 签名重试

兼容账号上的签名类 400 与普通 API Key 账号一样，沿用「设置 → 网关服务 → 请求整流器」中的「API Key 签名整流」开关（需同时开启请求整流器总开关）：开启时，网关去掉历史中的 thinking 后重试一次。

| 项 | 说明 |
|---|---|
| 默认值 | 新部署默认关闭。使用兼容账号时建议开启；关闭时，在普通账号与兼容账号之间切换过的会话之后每轮都会收到 400，只能新开会话 |
| 生效入口 | 只在 `/v1/messages` 与 `/v1/messages/count_tokens`；`/v1/chat/completions`、`/v1/responses` 没有签名重试，上游的 400 直接返回客户端（与普通账号相同） |

### 账号测试

对兼容账号使用账号列表中的「测试连接」：网关按普通客户端的请求形态、经同一套兼容适配，直接向该账号的上游发两轮流式请求。没有选择模型时测 `claude-opus-5-5`，所选模型先经账号模型映射；结果只说明这一个模型。

发请求前先检查账号：与自动透传或请求头覆写冲突时测试不开始，直接显示与保存时相同的错误；没有身份种子或 Beta 策略拦截、过滤了必需的 beta 时，第 1 步直接失败（见下表）。

| 步骤 | 请求 | 通过条件 |
|---|---|---|
| 第 1 步 `adapted_turn` | 开启 thinking（Claude 4.6 及以后为 adaptive、effort `high`，更早的模型为 enabled、`budget_tokens` 1024），提供一个无参数工具并要求调用 | 响应完整结束，并返回带签名的 thinking |
| 第 2 步 `signed_continuation` | 带上第 1 步完整的 assistant 内容（含 thinking 签名）与工具结果继续；模型没有调用工具时，改为带完整 assistant 内容的普通追问 | 响应完整结束 |

失败时测试停在该步，并显示错误原文：

| 失败 | 含义 | 处理 |
|---|---|---|
| 第 1 步 `compatibility account is not ready: <原因>` | 账号未就绪，原因为 `identity_uninitialized`、`beta_policy_blocked` 或 `required_beta_filtered` | 按[排查](#排查)表处理 |
| 第 1 步 `diagnostic model is outside the profile capability range` | 映射后的模型不以 `claude-` 开头 | 换模型或修改模型映射 |
| 第 1 步 `API returned <状态码>: …` | 上游拒绝了请求：模型、凭据、端点或 beta 不被接受 | 按错误原文处理；模型不可用就从映射里去掉 |
| 第 1 步 `the model did not return thinking in this turn` | 模型这次没有思考，或上游不返回 thinking | 重试；反复出现时不要开放该模型 |
| 第 1 步 `model returned thinking without signed history` | thinking 没有签名，后续轮次无法续接 | 不要开放该模型 |
| 第 2 步 `API returned <状态码>: …` | 上游不接受它自己刚返回的签名历史，多轮 thinking 对话会失败 | 不要开放该模型 |
| 任一步 `upstream diagnostic error: …` | 上游在流中返回了错误事件 | 按错误原文处理 |
| 任一步 `request failed: …`，或流不完整（如 `compatibility diagnostic ended without a complete Anthropic terminal event`） | 连不上上游、上游断流或 SSE 不完整 | 重试；持续出现时检查上游与代理 |

测试接口的 SSE 中，开始事件带 `"test_scope":"ordinary_client_compatibility"` 与 `"compat_profile":"claude_code"`，每步一个 `compat_step` 事件（`code`、`status` 为 `passed` 或 `failed`，失败时带 `error`），最后是 `test_complete`。

### 排查

每个请求中，兼容账号被排除或被使用时各记一行日志 `gateway.anthropic_compat_attempt`（普通账号不记），不含请求正文、签名、密钥与身份种子：

| 字段 | 说明 |
|---|---|
| `request_id` | 请求 ID |
| `account_id` | 账号 ID |
| `entry` | 入口：`messages`、`count_tokens`、`chat`、`responses` |
| `model` | 模型 |
| `decision` | `adapted`：已适配并发往上游；`ineligible`：该账号本次不参与 |
| `reason` | `ineligible` 的原因，见下表 |

| `reason` | 含义 | 处理 |
|---|---|---|
| `draining` | 「接收新会话」已关闭，本次不是绑定在该账号上的会话 | 预期行为；要接新会话时重新开启 |
| `model_unsupported` | 映射后的模型不以 `claude-` 开头，或账号的模型映射、白名单不允许该模型 | 检查模型映射与白名单 |
| `configuration_conflict` | 账号配置与兼容冲突（自动透传、请求头覆写） | 按[启用](#启用)中的冲突表修改 |
| `identity_uninitialized` | 账号没有有效的身份种子 | 在后台编辑并保存一次该账号 |
| `beta_policy_blocked` | Beta 策略拦截了本次请求中的某个 beta | 调整 Beta 策略或客户端发送的 beta |
| `required_beta_filtered` | Beta 策略过滤了兼容必需的 beta | 调整 Beta 策略 |
| `request_context_missing` | 请求没有经过上述四个入口的兼容准备（网关内部调用路径） | 正常使用不会出现；出现时带上 `request_id` 反馈 |

分组里没有可用账号时，客户端收到网关通常的「没有可用账号」错误；按 `request_id` 查这条日志，即可知道每个兼容账号被排除的原因。上游返回的错误和断流与普通账号走同一路径，见 [网关韧性](../guides/gateway-resilience.md)。签名 400 频繁时，先确认请求整流器与「API Key 签名整流」已开启、请求走的是 `/v1/messages`，再看会话是否在普通账号与兼容账号之间来回切换。

## 协议转换字段表

Anthropic 分组的 `/v1/chat/completions`、`/v1/responses` 请求由网关转换为 Anthropic Messages 后发往上游，普通账号与兼容账号的转换相同。字段语义以 Anthropic 官方 [OpenAI SDK 兼容文档](https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk) 为准：官方支持的映射，官方忽略的忽略。标「网关扩展」的是官方文档之外网关额外提供的映射。所有字段的 JSON `null` 都视为未提供。

| 字段 | 处理 | 说明 |
|---|---|---|
| `model`、`messages`、`input`、`instructions` | 映射 | 转为 Messages 的 `system` 与 `messages` |
| `max_tokens`、`max_completion_tokens`、`max_output_tokens` | 映射 | 转为 `max_tokens`，`max_completion_tokens` 优先于 `max_tokens`；都没有时为 8192 |
| `stream`、`stream_options` | 映射 | 按入口协议返回流式事件 |
| `temperature`、`top_p` | 映射或丢弃 | `temperature` 大于 1 时按 1 发送，负数返回 400。Claude 4.6 及以前：两者同时出现时丢弃 `top_p`。Claude 4.7 及以后（5.5 除外）只接受默认采样：`temperature` 不为 1、`top_p` 不为 0.99 时丢弃该参数，两者同时出现时丢弃 `top_p`。Claude 5.5 各模型按自己的采样规则丢弃不接受的值。丢弃都不报错 |
| `stop` | 映射 | 转为 `stop_sequences`；只含空白的项丢弃，空字符串返回 400 |
| function 工具、Responses `custom` 工具 | 映射 | 转为 Anthropic 工具；`parameters` 缺失、为 `null` 或为空数组 `[]`（PHP 客户端常见）时用空的 object schema |
| Anthropic 原生工具类型（`bash_20250124` 这类 `名称_YYYYMMDD`） | Responses 透传，Chat 丢弃 | Responses 按原定义转发；Chat Completions 中去掉 |
| `web_search`、`web_search_preview`、`code_execution` | 映射（网关扩展） | 两个入口都支持；前两种转为 Anthropic `web_search_20250305` |
| `google_search`、`web_search_20250305` | Responses 映射（网关扩展），Chat 丢弃 | Responses 中转为 Anthropic `web_search_20250305`；Chat Completions 中去掉 |
| 其他工具类型（`local_shell`、`image_generation`、`file_search` 等 OpenAI 专有或未知类型） | 忽略 | 从请求中去掉；`tool_choice` 指定了被去掉的工具时返回 400 |
| 工具 schema，根为 object | 映射 | `properties` 为 `null` 或空数组 `[]` 时视为 `{}`；`type` 是含 `"object"` 的数组（如 `["object","null"]`）时按 object 处理并保留属性 |
| 工具 schema，根为 `allOf` | 映射（网关扩展） | 合并各分支的 `properties`，`required` 取并集；没有 `type` 的分支视为 object |
| 工具 schema，根为 `anyOf`、`oneOf` | 映射（网关扩展） | 合并各分支的 `properties`，`required` 取交集；没有 `type` 的分支（如只写了 `required`）视为 object |
| 工具 schema，根不是 object，或联合分支显式声明了非 object 类型 | 报错 | 400，`param` 指向 `tools[i]…parameters` |
| `tool_choice`、旧版 `function_call` | 映射 | `auto`→`auto`，`required`→`any`，`none`→`none`，指定函数→`tool`；`allowed_tools` 按 `mode` 转为 `auto` 或 `any`，不限制工具子集。不支持强制选择工具的 Claude 5.5 模型收到 `required` 或指定函数时返回 400 |
| `parallel_tool_calls` | 映射 | `false` 转为 `disable_parallel_tool_use: true` |
| `response_format`、`text.format` 的 `json_schema` | 映射 | 转为 Anthropic 结构化输出；`strict` 为 `true`、`false` 或未指定都映射 |
| `response_format` 的 `json_object` | 忽略（网关放宽） | Anthropic 没有不带 schema 的 JSON 模式，网关去掉该字段，按普通请求生成，模型可能输出普通文本而不是 JSON；需要保证 JSON 时改用 `json_schema` |
| `text.format` 的 `text` | 映射（网关扩展） | 普通文本输出 |
| `reasoning_effort`、Responses `reasoning.effort` | 映射（网关扩展） | 接受 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`，其他值返回 400。Claude 4.6 及以后：adaptive thinking + `output_config.effort`，`minimal` 按 `low`，模型没有的档位取它的最高档，没有 effort 档位的模型不发送 effort。Claude 4.5 及以前：enabled thinking + `budget_tokens`，`low` 不开启 thinking，`max_tokens` 容纳不下最小 thinking 预算时返回 400。`none`：Claude 5 以前关闭 thinking；Claude 5 及以后 thinking 常开，按 `low` 处理（Sonnet 5.5 改为 `between_tools`）。Claude 5.5 模型没有指定时按模型默认档位开启 thinking |
| `n` | 校验 | 只能为 1，其他值返回 400 |
| `logprobs`、`top_logprobs`、`seed`、`presence_penalty`、`frequency_penalty`、`logit_bias`、`user`、`metadata`、`prediction`、`service_tier`、`audio`、`store`、`modalities`、function 的 `strict` | 忽略 | 与官方一致 |

转换失败时网关不向上游发请求，直接返回 400，`param` 是出错的字段路径：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "unsupported_parameter",
    "param": "tools[0].function.parameters.type",
    "message": "unsupported protocol conversion at tools[0].function.parameters.type: tool schema root must allow type object"
  }
}
```

流式响应已经开始时，Chat Completions 以流内错误事件、Responses 以 `response.failed` 事件返回，`type` 与 `code` 相同。

## 限制与注意事项

- Anthropic Claude 与 Antigravity Claude 不能在同一上下文中混用。如果 Antigravity 账号开启了混合调度，请用分组隔离，见 [Antigravity](antigravity.md)。
- 提示词缓存的自动补断点、缓存 TTL 与计费规则见 [提示词缓存](../guides/caching.md)。
