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

## 客户端配置

### Codex CLI

在「使用密钥」中选择 **Codex CLI**。弹窗会读取当前密钥可用的模型目录，自动选择模型和思考深度。`base_url` 必须以 `/v1` 结尾，Codex 会直接拼接 `/responses`。

认证模式有两种：

| 模式 | 写法 | 适用 |
|---|---|---|
| 兼容模式（默认） | `requires_openai_auth = true`，密钥写在 `auth.json` 的 `OPENAI_API_KEY` | 旧版 Codex 配置 |
| API Key Mode | `requires_openai_auth = false`，密钥写在 `experimental_bearer_token`，并带 `http_headers = { "x-openai-actor-authorization" = "local-image-extension" }` | 需要授权客户端图片执行器时；保存后必须完全退出并重启 Codex Desktop 或 CLI，再新建 task |

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
- **思考深度**：支持 `low`、`medium`、`high`、`xhigh`、`max`。在最终模型映射之后，显式的 `none`、`minimal` 或关闭 thinking 会在本地被拒绝，覆盖 HTTP、协议转换、透传以及 WebSocket 的首帧、后续轮次和 `session.update`。映射到其他模型的请求不套用这条限制。
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

## 相关功能

- [OpenAI 智商检测](../guides/iq-check.md)：按“账号 × 模型”检测降智并暂停调度。
- [Codex STATE](../guides/codex-state.md)：为 OAuth 账号获取并注入 STATE 票据。
- [OpenAI 模型降档识别](../guides/quality-routing.md)、[提示词缓存](../guides/caching.md)、[分组与 Fast 优先级](../guides/groups.md)。
- 同样经 OpenAI 网关转发的 Kimi、智谱、DeepSeek、MiniMax、OpenCode、Command Code、Cline 见 [其他平台](others.md)；这些平台的分组使用 `/v1/messages` 不需要开启「允许 /v1/messages 调度」。
