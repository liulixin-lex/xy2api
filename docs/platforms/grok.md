# Grok（xAI）

本文介绍如何把 xAI Grok 账号接入 XY2API：Grok 订阅账号（xAI OAuth）与 xAI API Key 账号的添加方式、OAuth 相关环境变量、Grok 分组对外暴露的文本、媒体与语音端点，以及 Grok CLI、Claude Code、Codex CLI 的配置。部署 XY2API 的管理员和使用 Grok 模型的用户需要阅读。

## 账号类型

平台标识为 `grok`。在「账号管理 → 添加账号」中选择平台 **Grok**：

| 界面选项 | 账号 `type` | 默认上游 | 说明 |
|---|---|---|---|
| OAuth（Grok OAuth） | `oauth` | Grok CLI 聊天代理 `https://cli-chat-proxy.grok.com/v1` | Grok 订阅账号，使用 PKCE 授权 |
| API Key（Responses API） | `apikey` | `https://api.x.ai/v1` | xAI 官方 API Key，凭据使用账号的 `base_url` 与 `api_key` 字段 |

OAuth 账号的添加方式：

| 添加方式 | 说明 |
|---|---|
| OAuth 授权 | 生成 xAI 授权链接，在浏览器登录并授权，跳转到本地回调地址后，把完整回调 URL、`?code=...` 查询串或 `code` 粘贴回弹窗。回调链接缺少 `state` 时会提示 `GROK_OAUTH_STATE_REQUIRED`，请粘贴完整地址 |
| 手动输入 RT | 批量粘贴已有的 xAI refresh token，每行一个 |
| SSO Cookie 导入 | 粘贴 Grok 网页版的 SSO key，自动转换为 Grok Build OAuth 凭据，见下文 |
| 邮箱密码登录 | 默认关闭，管理后台不提供入口，本文档不提供配置说明，见下文 |

OAuth 凭据沿用账号的 JSON 字段：`access_token`、`refresh_token`、`token_type`、`expires_at`、`base_url`，以及可选的 `email`、`subscription_tier`、`entitlement_status`。

### SSO Cookie 导入

1. 「账号管理 → 添加账号」，平台选 Grok，账号类型选 OAuth，在添加方式中选择「SSO Cookie 导入」。
2. 在「Grok Web SSO Key」中每行粘贴一个 Grok 网页版的 SSO key，可一次粘贴多个。
3. 建议选择与账号所在地区一致的代理，再点击「转换并创建账号」。

系统对每个 key 走 xAI Device Flow，转换为 Grok Build OAuth 凭据后创建账号。多个 key 以 3 路并发导入，每个 key 的转换最长约 90 秒，总耗时约为“90 秒 × 批次数”。接口响应只包含 OAuth 令牌信息，不回显 SSO key。

对应的管理接口（位于 `/api/v1/admin/grok` 下）：`POST /sso-to-oauth`（请求体 `sso_tokens` 为 key 数组，可同时带 `proxy_id`、`group_ids` 等账号字段，批量创建账号）；`POST /oauth/sso-token`（请求体 `{sso_token, proxy_id}`，只转换并返回令牌信息，不创建账号）。

### 邮箱密码登录

代码中还保留了一种默认关闭、管理后台不提供入口的邮箱密码登录方式。它需要借助第三方打码服务通过 xAI 登录页的人机验证，可能违反 xAI 的服务条款，本文档不提供配置说明。请使用 SSO Cookie 导入、浏览器 OAuth 授权或 refresh token。

## 在管理后台添加

1. 添加 Grok OAuth 账号并完成 xAI 授权，或添加 Grok API Key 账号。需要改发到其他地址时，开启账号的「自定义上游地址」（预设 Grok Build CLI、官方 API，以及 us-east-1、us-west-2、eu-west-1 区域端点）；开启后对话、媒体和探测流量都发往该地址，OAuth 授权与令牌刷新仍走官方端点。
2. 创建平台为 Grok 的分组，把账号加入分组；需要生图或视频时，为分组开启生图权限。
3. 创建绑定该分组的 XY2API API 密钥。在「API 密钥」页点击「使用密钥」，可选择 Grok CLI、Claude Code、Codex CLI 和 OpenCode，弹窗会按 macOS / Linux 或 Windows 生成对应文件和地址。

## OAuth 配置

Grok OAuth 使用 PKCE，不需要在仓库中保存私密凭据。默认值与兼容客户端使用的公开 xAI OAuth 流程一致，均可用环境变量覆盖：

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `XAI_OAUTH_CLIENT_ID` | 公开的 xAI OAuth Client ID | |
| `XAI_OAUTH_SCOPE` | `openid profile email offline_access grok-cli:access api:access` | |
| `XAI_OAUTH_REDIRECT_URI` | `http://127.0.0.1:56121/callback` | |
| `XAI_OAUTH_AUTHORIZE_URL` | `https://auth.x.ai/oauth2/authorize` | 覆盖值必须是 `x.ai` 域下的 HTTPS 地址 |
| `XAI_OAUTH_TOKEN_URL` | `https://auth.x.ai/oauth2/token` | 同上 |
| `XAI_BASE_URL` | `https://api.x.ai/v1` | 只用于运行时诊断；实际转发地址由账号的 `base_url` 决定 |
| `XAI_GROK_CLI_VERSION` | 内置版本 | 可选，覆盖发往 `cli-chat-proxy.grok.com` 的客户端版本号；必须是合法语义化版本且不低于内置的最低版本 `1.0.13`，否则忽略并使用内置版本 |

默认的 Docker Compose 文件不传递这些 `XAI_*` 变量；需要覆盖时，把它们加入 compose 文件中 `xy2api` 服务的 `environment` 列表。二进制部署在 systemd 服务文件中用 `Environment=` 设置。

`XAI_ALLOW_UNSAFE_URL_OVERRIDES` 是调试开关，开启后放宽上述地址与账号 Base URL 的主机校验，生产环境不要开启。当前生效的 OAuth 地址与校验结果可通过 `GET /api/v1/admin/grok/runtime-sanity` 查看。

OAuth 管理接口（均位于 `/api/v1/admin/grok` 下）：

| 接口 | 用途 |
|---|---|
| `POST /oauth/auth-url` | 生成 xAI OAuth 授权链接 |
| `POST /oauth/exchange-code` | 用回调 URL、查询串或授权码换取凭据 |
| `POST /oauth/refresh-token` | 校验或刷新 refresh token |
| `POST /oauth/create-from-oauth` | 用 OAuth 凭据创建账号 |
| `POST /accounts/:id/refresh` | 刷新已有账号的令牌 |
| `GET /accounts/:id/quota`、`POST /accounts/:id/reset-quota` | 查询、重置账号额度观测 |

## 全局设置

「设置 → 网关服务 → 请求转发行为」中有三项 Grok 设置：

| 界面名称 | 设置键 | 默认 | 说明 |
|---|---|---|---|
| 默认 Grok 文本模型 | `grok_default_text_model` | `grok-4.6` | 请求模型为空，或使用 `grok`、`grok-latest` 别名时使用；可填自定义 Grok 模型 ID |
| 映射其他客户端模型到 Grok | `grok_cross_client_model_map_enabled` | 开 | 把 `gpt-*`、`codex-*`、`o1*`/`o3*`/`o4*`、`claude-*` 模型名路由到默认文本模型，方便 Codex、Claude Code 直接使用；关闭后必须写 Grok 模型 ID |
| 默认 Grok 上游 | `grok_default_base_url_mode` | `cli`（CLI 聊天代理） | 只影响未配置显式 Base URL 的账号的文本请求；可选 `api`（公共 API）、`us-east-1`、`us-west-2`、`eu-west-1`（区域 API）。媒体与语音始终使用官方 API 主机 |

## 对外端点

### 文本

| 端点 | 说明 |
|---|---|
| `POST /v1/responses`、`/responses`、`/backend-api/codex/responses` | Responses；OAuth 账号转发到订阅代理，API Key 账号转发到 `https://api.x.ai/v1/responses` |
| `GET` 上述 Responses 路径 | 接受 Codex CLI 形式的 Responses WebSocket，桥接到上游 HTTP/SSE |
| `POST /v1/chat/completions`、`/chat/completions` | Chat Completions，按账号类型转发到对应上游 |
| `POST /v1/messages` | Anthropic Messages 格式，转换为 Responses 后以 Messages 格式返回，供 Claude Code 等客户端使用 |
| `POST /v1/messages/count_tokens` | 在本地估算，不选号、不计费 |

文本模型：`grok-4.7`、`grok-4.6`、`grok-4.5`、`grok-4.3`、`grok-build-0.1`、`grok-composer-2.5-fast`、`grok-4.20-0309-reasoning`、`grok-4.20-0309-non-reasoning`、`grok-4.20-multi-agent-0309`。另接受 `grok-3-mini`、`grok-3-mini-fast`，以及 `grok-build`、`grok-composer`、`grok-4.5-latest` 等别名；模型名可带 `xai/`、`x-ai/`、`grok/` 前缀。

### 图片与视频

| 端点 | 说明 |
|---|---|
| `POST /v1/images/generations`、`/v1/images/edits`（及不带 `/v1` 的别名） | 生图、改图；异步与批量接口见 [异步图片任务](../api/images.md)与[批量生图](../api/batch-images.md) |
| `POST /v1/videos`、`/v1/videos/generations` | 创建视频生成任务 |
| `POST /v1/videos/edits`、`/v1/videos/extensions` | 视频编辑、延长 |
| `GET /v1/videos/{request_id}`、`/v1/videos/{request_id}/content` 等 | 查询任务状态与下载内容 |

媒体模型：`grok-imagine-image-quality`、`grok-imagine-image`、`grok-imagine-image-2.0`、`grok-imagine-video`、`grok-imagine-video-1.5`。旧名 `grok-imagine`、`grok-imagine-1`、`grok-imagine-edit` 映射到 `grok-imagine-image-quality`，`grok-imagine-video-1.5-preview`、`grok-video-1.5` 映射到 `grok-imagine-video-1.5`。

- 生成、编辑、延长请求需要分组开启生图权限，否则返回 `403 permission_error`。
- JSON 改图与视频请求的 `image`、`images`、`mask` 字段会规范为 xAI 的图片对象（使用 `url` 字段）；旧的 `image_url` 写法仍然接受。改图最多 3 张源图。
- 成功的生图响应必须至少包含一张图片；上游返回空的 HTTP 200 会触发换号，不计为成功。

### 语音与搜索

以下端点只对 Grok 分组开放，其他平台返回 `404`：

| 端点 | 说明 |
|---|---|
| `POST /v1/tts`、`POST /v1/stt` | xAI 语音合成、语音转写 |
| `/v1/custom-voices`（`GET`、`POST`）与 `/v1/custom-voices/:voice_id`（`GET`、`PATCH`、`DELETE`）、`/v1/custom-voices/:voice_id/audio` | 自定义音色 |
| `GET /v1/realtime` | xAI Voice Realtime WebSocket |
| `POST /v1/web_search`、`POST /v1/x_search` | 使用 Grok 账号原生能力的网页搜索与 X 搜索 |

## 客户端配置

下文的 `base_url` 是 XY2API 的公开地址（以 `/v1` 结尾），不是 `api.x.ai`，也不是内部的 xAI OAuth 代理地址。配置文件含 XY2API API 密钥，合并前先备份原文件，并在系统支持时收紧文件权限。

### Grok CLI（Grok Build）

最小配置：保存为 `~/.grok/config.toml`（Windows 为 `%USERPROFILE%\.grok\config.toml`）：

```toml
[models]
default = "grok"
web_search = "grok"

[model."grok"]
model = "grok-4.5"
base_url = "https://your-xy2api.example.com/v1"
name = "Grok 4.5"
api_key = "sk-your-xy2api-key"
api_backend = "responses"
context_window = 500000
supports_backend_search = true
```

`api_backend` 必须为 `"responses"`；省略时 Grok CLI 默认走 Chat Completions。不想把密钥写进文件时，用 `env_key = "XAI_API_KEY"` 代替 `api_key`，并导出环境变量。

「使用密钥」弹窗生成的是完整模板：先导出 `GROK_MODELS_BASE_URL` 与 `XAI_API_KEY`，`config.toml` 中包含 `[endpoints]`（`models_base_url`、`models_list_url`、`xai_api_base_url`、`cli_chat_proxy_base_url` 都指向 XY2API）、`[auth] preferred_method = "api_key"`、多个文本模型条目、`[session]` 自动压缩阈值，以及 `[features]` 中的图片 / 视频模型覆盖（仅在分组允许生图时启用）。

保存后检查生效配置并发一个测试请求：

```bash
grok inspect
grok -p "Reply with xy2api-ok" -m grok
```

### Claude Code

```bash
export ANTHROPIC_BASE_URL="https://your-xy2api.example.com"
export ANTHROPIC_AUTH_TOKEN="sk-your-xy2api-key"
export ANTHROPIC_MODEL="grok-4.5"
export ANTHROPIC_DEFAULT_OPUS_MODEL="grok-4.5"
export ANTHROPIC_DEFAULT_SONNET_MODEL="grok-4.5"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="grok-4.5"
export ANTHROPIC_DEFAULT_FABLE_MODEL="grok-4.5"
export CLAUDE_CODE_SUBAGENT_MODEL="grok-4.5"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

`ANTHROPIC_BASE_URL` 不带 `/v1`。也可以把这些变量写入 `~/.claude/settings.json` 的 `env` 字段。

### Codex CLI

先导出 `XY2API_API_KEY`，再写入 `~/.codex/config.toml`：

```toml
model_provider = "xy2api"
model = "grok-4.5"

[model_providers.xy2api]
name = "XY2API Grok"
base_url = "https://your-xy2api.example.com/v1"
env_key = "XY2API_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
```

只放文本模型；图片和视频使用 Imagine 模型 ID 调用媒体端点。弹窗还可以生成远程模型目录（`model_catalog_url`）配置，与 OpenAI 分组相同，见 [OpenAI](openai.md#codex-cli)。

## 额度与媒体资格

- **额度展示是被动的**：XY2API 不会臆造订阅额度，只在 xAI 的成功或限流响应带有速率限制头时记录白名单内的头。收到第一个可用响应之前，后台额度显示为未知，但仍显示本站的用量统计。
- **媒体资格**：新的生图、生视频请求会单独判断账号资格。API Key 账号始终有资格；OAuth 账号有明确的免费档或计费 403 证据时不参与新的媒体生成；尚未观测到计费信息时会先探测；计费接口返回成功但缺少套餐字段时记为 `billing_inconclusive`，仍保留资格。只影响媒体生成，不影响文本请求和视频状态查询。没有可用账号时返回 `503`，错误类型 `grok_media_no_eligible_account`。
- **手动覆盖**：通过账号创建 / 更新接口设置 `extra.grok_media_eligible`：`false` 排除、`true` 强制允许；更新时设为 `null` 清除覆盖、恢复自动判断，省略该字段则保持原值。也可以使用 `GET`/`PUT /api/v1/admin/accounts/:id/grok-media-eligibility`。仅有周额度周期本身不视为付费档的证据。

## 免费档本地软闸

对订阅等级（`subscription_tier` / `plan_type`）明确为 `free` 的 Grok OAuth 账号，网关在本地按滚动窗口统计用量，接近名义额度时停止为它调度新请求。它只是调度过滤：管理员的额度查询与导入探测不受影响；等级未知或付费的账号不受限制；统计或查询失败时放行，不会因数据库问题挡住全部 Grok 流量。配置位于 `config.yaml` 的 `gateway.grok`：

| 配置键 | 默认 | 说明 |
|---|---|---|
| `free_quota_soft_gate_enabled` | `true` | 是否启用 |
| `free_quota_token_limit` | `500000` | 滚动窗口内的名义 token 额度，必须为正数 |
| `free_quota_soft_gate_percent` | `95` | 达到名义额度的该百分比时停止新调度，取值 1–100 |
| `free_quota_window_hours` | `24` | 滚动窗口长度（小时），必须为正数 |
| `free_quota_stats_cache_seconds` | `60` | 用量统计缓存时间；请求路径不等待数据库，缓存未命中时放行并在后台刷新，生产环境建议不低于 60 |

## 相关文档

- Kimi、智谱、DeepSeek 等其他平台见 [其他平台](others.md)。
- Grok 渠道监控可选择 Responses 模式，见 [渠道监控](../guides/channel-monitor.md#grok-的-responses-模式)。
- 分组、调度与组合分组见 [分组](../guides/groups.md)、[账号调度](../guides/scheduling.md)。
