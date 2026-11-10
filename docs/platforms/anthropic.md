# Anthropic（Claude）

本文介绍如何把 Anthropic Claude 账号接入 XY2API：支持哪些账号类型、在管理后台如何添加、网关对外暴露哪些端点、如何配置 Claude Code 等客户端，以及只对 Anthropic 平台生效的选项（OAuth 转发行为、API Key 账号的“Claude Code 请求兼容”）。部署 XY2API 的管理员和调用 Claude 模型的用户需要阅读。

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
| `POST /v1/chat/completions`（别名 `/chat/completions`） | OpenAI Chat Completions 格式，网关转换为 Messages 后转发 |
| `POST /v1/responses`（别名 `/responses`） | OpenAI Responses 格式，网关转换为 Messages 后转发 |
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

「使用密钥」弹窗还为 Anthropic 分组生成 Codex CLI（经 `/v1/responses`）和 OpenCode（`~/.config/opencode/opencode.json`）的配置，按弹窗内容复制即可。

## OAuth 账号的转发行为

「设置 → 网关服务 → 请求转发行为」中的以下开关影响转发到 Claude OAuth 账号的请求：

| 界面名称 | 设置键 | 默认 | 作用 |
|---|---|---|---|
| 指纹统一化 | `enable_fingerprint_unification` | 开 | 统一共享同一 OAuth 账号的用户的 `X-Stainless-*` 请求头；关闭后透传客户端原始请求头 |
| Metadata 透传 | `enable_metadata_passthrough` | 关 | 透传客户端原始 `metadata.user_id`，不重写 |
| CCH 签名 | `enable_cch_signing` | 关 | 对转发请求的 billing header 做 CCH 哈希签名；关闭时保留原始占位符 |
| Claude OAuth System 注入 | `enable_claude_oauth_system_prompt_injection` | 开 | 为非 Claude Code 客户端的 OAuth 请求注入 Claude Code 形态的 system 块；可在「Claude OAuth 扩展提示词」中自定义，留空用内置内容 |

## Claude Code 请求兼容（API Key 账号）

有些 Anthropic API Key 上游只接受 Claude Code 形态的请求。“Claude Code 请求兼容”让普通客户端经过 XY2API 适配后也能使用这类上游，客户端仍使用原来的端点和密钥。

### 启用

- 仅适用于 `platform=anthropic`、`type=apikey` 的账号。在账号创建、编辑或批量编辑中打开「Claude Code 请求兼容」，对应字段 `extra.anthropic_compat_profile`，取值 `off`（默认）或 `claude_code_v1`。
- 只有字段缺失或精确字符串 `off` 表示关闭；空串、`null`、`OFF` 等其他值在保存时返回 `ANTHROPIC_COMPAT_PROFILE_INVALID`；对非 Anthropic API Key 账号设置返回 `ANTHROPIC_COMPAT_PROFILE_ACCOUNT_INVALID`。
- 与「自动透传（仅替换认证）」（`anthropic_passthrough`）互斥，需先关闭自动透传。自定义请求头和 Beta 策略必须与兼容配置一致，矛盾的配置在保存时拒绝。
- 适用模型：映射后为 `claude-opus-4-8` 或 `claude-opus-5-5`，并且账号的模型准入允许该模型。
- 旧页面保存时遗漏该字段或发送 `extra:{}` 不会关闭兼容；只有明确保存 `off` 才关闭。新建、复制或导入账号会重新生成服务端管理的身份种子，该种子不会通过接口或导出返回。

### 支持范围

| 入口 | 支持 | 不支持 |
|---|---|---|
| `/v1/messages` | 文本、流式、工具往返；来源可验证的思考签名续轮 | 外来的未知签名不能迁入新的兼容会话 |
| `/v1/responses`（HTTP） | 已有转换覆盖的文本、工具和明确标记的 Anthropic thinking 信封 | 任意密文、上游保存的 `previous_response_id`、WebSocket |
| `/v1/chat/completions` | 文本、流式和普通工具 | 无法回传签名的客户端不能无损续接签名思考 |
| `count_tokens` | 相同 prompt 的独立计数 | 依赖上游计数端点，不支持时不伪造数值 |

Message Threads、compact 以及依赖上游保存状态的续接不受支持。某个模型或请求成功，不代表其他模型和全部功能都可用，启用前请在目标账号上逐项验证所需能力。

### 工作方式

- 兼容层统一设置受控的 User-Agent、`X-App`、API 版本（`2023-06-01`）、Beta 组合、最小身份文本块，以及按租户隔离的 `metadata.user_id`。调用方的其他 metadata、业务 system 的角色和内容、工具名称与 schema、工具调用 ID 和显式生成参数保持不变；不会把强制工具选择改为 auto，也不会为了让请求成功而删除签名或工具历史。
- 已经是完整一致的原生 Claude Code 请求、无需改写时走原路径。仅伪造 User-Agent 不能绕过检查。
- 会话头 `X-Claude-Code-Session-Id` 由适配器管理，不能用账号的静态请求头覆盖。会话身份按用户、网关密钥、上游账号与显式会话隔离；没有明确会话的请求各自使用独立锚点。
- 每次换号都从原始请求重新构造，不沿用上一个账号的派生状态，继续消耗原请求的预算。

### 签名历史

网关在租户与密钥范围内为 `thinking.signature`、`redacted_thinking.data` 建立来源索引，只保存来源约束，不保存原始签名或请求正文。已知历史绑定原上游账号、profile、模型、最终 system/tools 等配置；关键配置改变后旧历史会明确报不兼容，不会跨账号删除历史后重试。

| 限制 | 值 |
|---|---|
| 每个请求最多不同签名数 | 128 |
| 请求或响应累计 opaque 数据 | 各不超过 1 MiB |
| 来源闲置有效期 | 24 小时（成功续轮会刷新） |

超限或来源过期时明确报错，不截断历史。流式内容实时返回，上游完整成功后网关先保存本轮签名来源，再发送结束信号；登记失败时客户端收到协议错误，真实用量照常结算。

**注意**：所有携带思考签名、redacted 数据或 Anthropic Responses 思考信封的请求，都会在选号前查询来源索引，包括未开启兼容的 API Key 账号和 OAuth 账号。查询失败（例如 Redis 故障）时返回 `503 anthropic_compat_state_unavailable`，不会向上游发请求。没有签名的请求不受影响。

### 验证与观测

- 账号测试对兼容账号使用“普通客户端请求，经兼容适配”的口径，并固定目标账号；旧版 CLI 形态的连通性测试不能替代兼容验收。HTTP 200 不能单独证明流式请求成功，应以完整终态判断。
- 渠道监控反映入口或分组的可用性，不等于其中某个账号已通过兼容验收，见 [渠道监控](../guides/channel-monitor.md)。
- 日志事件 `gateway.anthropic_compat_attempt` 记录 `profile`、`profile_revision`、`entrypoint`、`decision`、`source`、`account_id`、`terminal`、`error_class`，不含签名、prompt、metadata、会话 ID 或认证头。关闭兼容且无相关历史的请求不产生该事件。

### 上线与回滚

1. 升级后所有账号默认关闭兼容。确认数据库与 Redis 可用：来源索引存于 Redis，所用 HMAC 根密钥由服务端持久保存、跨实例共享，两者都应纳入备份与恢复流程。
2. 先在隔离流量中按目标模型验证三个入口的流式与非流式、业务 system、工具往返、签名续轮和计数，并确认实际命中目标账号。
3. 只把验证过的能力接入普通流量，观察上游拒绝、真实完成率、签名错误、首字延迟和用量。
4. 回滚时先停止该账号承接普通流量，等在途请求结束并处理已绑定的会话，再关闭兼容。只关开关而继续接普通请求，会恢复上游原来的拒绝。

## 限制与注意事项

- Anthropic Claude 与 Antigravity Claude 不能在同一上下文中混用。如果 Antigravity 账号开启了混合调度，请用分组隔离，见 [Antigravity](antigravity.md)。
- 提示词缓存的自动补断点、缓存 TTL 与计费规则见 [提示词缓存](../guides/caching.md)。
