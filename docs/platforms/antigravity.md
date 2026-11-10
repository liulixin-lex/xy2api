# Antigravity

本文介绍如何把 [Antigravity](https://antigravity.so/) 账号接入 XY2API。授权后，网关通过专用端点提供 Claude 与 Gemini 模型，也可以让 Antigravity 账号以“混合调度”方式参与 Anthropic、Gemini 分组。部署 XY2API 的管理员和使用 Antigravity 分组的用户需要阅读。

## 账号类型

在「账号管理 → 添加账号」中选择平台 **Antigravity**：

| 界面选项 | 账号 `type` | 说明 |
|---|---|---|
| Antigravity OAuth | `oauth` | 使用 Google 账号授权 |
| 通过 Base URL + API Key 连接 | `upstream` | 连接上游 Antigravity 服务，填写上游 Base URL（例如 `https://cloudcode-pa.googleapis.com`）与上游 API Key |

OAuth 账号的添加方式：

- **OAuth 授权**：生成授权链接，在新标签页登录 Google 账号并授权。授权后页面可能加载较久，地址栏变为 `http://localhost:8085/callback?code=...` 时表示完成，复制完整链接或其中的 `code` 粘贴回弹窗。
- **手动输入 RT**：粘贴已有的 Antigravity Refresh Token，支持每行一个批量创建。

**GCP Project ID（可选）**：standard-tier 且上游没有自动返回 `project_id` 的账号，需要填写用户自己的 GCP 项目 ID。

管理接口：`POST /api/v1/admin/antigravity/oauth/auth-url`、`POST /api/v1/admin/antigravity/oauth/exchange-code`、`POST /api/v1/admin/antigravity/oauth/refresh-token`；内置默认模型映射可通过 `GET /api/v1/admin/accounts/antigravity/default-model-mapping` 查看。

## 在管理后台添加

1. 添加 Antigravity 账号并完成授权。
2. 创建平台为 Antigravity 的分组，把账号加入分组。
3. 创建绑定该分组的 API 密钥；「使用密钥」弹窗提供 Claude Code、Gemini CLI、Codex CLI 与 OpenCode 的配置。

账号未配置模型映射时使用内置默认映射：常用的 Claude 与 Gemini 模型原样透传，旧版本模型迁移到新版本；Antigravity 不提供 Haiku，`claude-haiku-4-5` 等会映射到 `claude-sonnet-4-6`。

## 专用端点

专用端点只调度 Antigravity 账号，不参与混合调度。

| 端点 | 模型 |
|---|---|
| `POST /antigravity/v1/messages` | Claude 模型（Anthropic Messages 格式） |
| `POST /antigravity/v1/messages/count_tokens` | Token 计数 |
| `GET /antigravity/v1/models`、`GET /antigravity/models` | 模型列表 |
| `GET /antigravity/v1/usage` | 当前密钥的额度与用量 |
| `/antigravity/v1beta/`（`GET models`、`GET models/:model`、`POST models/{model}:generateContent` 等） | Gemini 模型（Gemini 原生格式） |

`/antigravity/v1beta` 的密钥读取方式与 Gemini 原生端点相同，见 [Gemini](gemini.md#对外端点)。

## 客户端配置

### Claude Code

```bash
export ANTHROPIC_BASE_URL="https://your-xy2api.example.com/antigravity"
export ANTHROPIC_AUTH_TOKEN="sk-your-xy2api-key"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

Windows 的写法和 `~/.claude/settings.json` 的持久化方式见 [Anthropic](anthropic.md#claude-code)，只需把地址换成 `/antigravity` 结尾。

### Gemini CLI

```bash
export GOOGLE_GEMINI_BASE_URL="https://your-xy2api.example.com/antigravity"
export GEMINI_API_KEY="sk-your-xy2api-key"
export GEMINI_MODEL="gemini-2.0-flash"
```

### OpenCode

「使用密钥」弹窗会生成两份 `opencode.json`：Claude 配置的 `baseURL` 为 `https://your-xy2api.example.com/antigravity/v1`，Gemini 配置的 `baseURL` 为 `https://your-xy2api.example.com/antigravity/v1beta`。

## 混合调度

Antigravity 账号可以在账号设置中打开「在 /v1/messages 中使用」（`mixed_scheduling`）。开启后，该账号也会参与 Anthropic 与 Gemini 分组的调度，即通用端点 `/v1/messages` 和 `/v1beta/` 也可能把请求交给它。

**注意**：Anthropic Claude 与 Antigravity Claude **不能在同一上下文中混用**。如果同时有 Anthropic 账号和 Antigravity 账号，开启混合调度会导致频繁的 400 错误。开启前请用分组把两类账号隔离开。

## 其他选项

| 选项 | 位置 | 说明 |
|---|---|---|
| 允许超量请求（AI Credits） | 账号设置 | 只有免费配额被明确判定为耗尽后才使用 AI Credits；普通的并发 429 限流不会切换到超量请求 |
| Antigravity UA 版本 | 「设置 → 网关服务 → 请求转发行为」，设置键 `antigravity_user_agent_version` | 出站 User-Agent 中的版本号。后台设置优先；留空时使用环境变量 `ANTIGRAVITY_USER_AGENT_VERSION`，再留空使用内置默认值。版本号格式为 `x.y.z` |
| `ANTIGRAVITY_OAUTH_CLIENT_SECRET` | 环境变量 | 可选，覆盖内置的 Antigravity OAuth client secret；通常无需设置 |
