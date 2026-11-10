# Gemini

本文介绍如何把 Google Gemini 账号接入 XY2API：三种 OAuth 授权方式、AI Studio API Key 与 Vertex Service Account 账号的添加方法，自建 OAuth Client 时需要的环境变量，Gemini 分组对外暴露的端点，以及 Gemini CLI 的配置。部署 XY2API 的管理员和使用 Gemini SDK / CLI 的用户需要阅读。

## 账号类型

在「账号管理 → 添加账号」中选择平台 **Gemini**，账号类型有三类：

| 账号类型 | 账号 `type` | 说明 |
|---|---|---|
| OAuth 授权（Gemini） | `oauth` | 用 Google 账号授权，再选择下面的 OAuth 子类型 |
| API 密钥（AI Studio） | `apikey` | 填写以 `AIza` 开头的 AI Studio API Key；Base URL 留空使用官方 Gemini API。接入最快，适合轻量测试，免费层限流严格 |
| Vertex（Service Account） | `service_account` | 使用 Google Cloud Service Account JSON 调用 Vertex AI |

OAuth 子类型：

| 子类型 | `oauth_type` | OAuth Client | 是否需要 GCP 项目 | 适合 |
|---|---|---|---|---|
| Google One | `google_one` | 内置 Gemini CLI 客户端，无需配置 | 否 | 个人账号，使用 Google One 订阅配额（界面默认选项） |
| Code Assist | `code_assist` | 内置 Gemini CLI 客户端，无需配置 | 是，需要已激活的 GCP 项目与 Project ID | 企业用户、GCP 开发者 |
| AI Studio OAuth（自定义授权） | `ai_studio` | 必须使用自建 OAuth Client | 否 | 需要用 OAuth 而非 API Key、由组织统一管理的场景 |

Code Assist 与 Google One 始终使用内置的 Gemini CLI 公共客户端，即使配置了自建 Client 也不会改用。内置客户端不能申请 AI Studio 所需的 `generative-language` 权限，所以 AI Studio OAuth 必须自建 Client；未配置时界面会显示“未配置”，生成授权链接会失败。

添加前的准备（界面「使用帮助」中也有说明）：账号需满 18 岁；建议使用美国 IP 且账号归属地为美国；先激活 Gemini Web，避免 `User not initialized`；使用 Code Assist 时先激活 GCP 项目。

## 在管理后台添加

### Google One 与 Code Assist OAuth

1. 保持 `GEMINI_OAUTH_CLIENT_ID`、`GEMINI_OAUTH_CLIENT_SECRET` 为空即可，无需额外配置。
2. 添加 Gemini 账号，选择「OAuth 授权」和对应子类型，生成授权链接并在浏览器中完成授权。
3. 把授权后跳转的回调链接（推荐，包含 `code` 和 `state`）或其中的 `code` 粘贴回弹窗。
4. Code Assist 的 Project ID 可以留空，兑换授权码后会自动探测；探测失败时填写后重新生成授权链接。

获取 Project ID：打开 [Google Cloud Console](https://console.cloud.google.com/)，点击页面顶部的项目下拉框，复制项目 ID（不是项目名称），常见格式如 `my-project-123456` 或 `cloud-ai-companion-xxxxx`。

### AI Studio OAuth（自建 OAuth Client）

**第一步：在 Google Cloud Console 创建 OAuth Client**

1. 打开 [Google Cloud Console - Credentials](https://console.cloud.google.com/apis/credentials)，新建或选择一个项目。
2. 在「APIs & Services → Library」中搜索并启用 **Generative Language API**。
3. 在「OAuth consent screen」中选择 External 用户类型，填写应用名称和联系邮箱；添加权限范围 `https://www.googleapis.com/auth/generative-language.retriever`（可选再加 `https://www.googleapis.com/auth/cloud-platform`）；把要授权的 Google 账号加为测试用户。
4. 在「Credentials → Create Credentials → OAuth client ID」中创建 **Web application**（或 Desktop app）类型的客户端，Authorized redirect URIs 填 `http://localhost:1455/auth/callback`。
5. 复制 Client ID 与 Client Secret。
6. 回到「OAuth consent screen」，点击 **PUBLISH APP** 发布为 Production。Testing 模式下最多 100 个测试用户，refresh token 7 天后过期；敏感权限可能需要 Google 审核。

**第二步：配置 XY2API**

Docker Compose 部署写入 `.env`：

```bash
GEMINI_OAUTH_CLIENT_ID=your-client-id.apps.googleusercontent.com
GEMINI_OAUTH_CLIENT_SECRET=GOCSPX-your-client-secret
```

二进制（systemd）部署编辑 `/etc/systemd/system/xy2api.service`，在 `[Service]` 段现有的 `Environment=` 行之后加入：

```ini
Environment=GEMINI_OAUTH_CLIENT_ID=your-client-id.apps.googleusercontent.com
Environment=GEMINI_OAUTH_CLIENT_SECRET=GOCSPX-your-client-secret
```

然后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl restart xy2api
```

**第三步：在管理后台创建账号**

添加 Gemini 账号，选择「OAuth 授权 → AI Studio OAuth」。授权后浏览器会跳转到 `http://localhost:1455/auth/callback?code=...&state=...`，复制地址栏中的完整地址（推荐）或其中的 `code` 粘贴回弹窗，系统会自动解析 `code` 与 `state`。

### API Key

在 [Google AI Studio](https://aistudio.google.com/app/apikey) 点击 Create API key，然后在管理后台添加 Gemini「API 密钥」账号并粘贴。

### 授权方式对比

| | Google One OAuth | Code Assist OAuth | AI Studio OAuth | API Key |
|---|---|---|---|---|
| 配置难度 | 低（无需配置） | 低（无需配置） | 中（需自建 OAuth Client） | 低 |
| 需要 GCP 项目 | 否 | 是 | 否 | 否 |
| 自建 OAuth Client | 否 | 否 | 必须 | 不适用 |
| 适合 | 个人用户 | GCP 开发者、企业 | 需要 OAuth 的组织 | 快速测试 |

## 配置参考

| 环境变量 | YAML 键 | 默认 | 说明 |
|---|---|---|---|
| `GEMINI_OAUTH_CLIENT_ID` | `gemini.oauth.client_id` | 空 | 自建 OAuth Client ID，仅 AI Studio OAuth 使用 |
| `GEMINI_OAUTH_CLIENT_SECRET` | `gemini.oauth.client_secret` | 空 | 自建 OAuth Client Secret；与 Client ID 必须同时为空或同时填写，否则报 `GEMINI_OAUTH_CLIENT_NOT_CONFIGURED` |
| `GEMINI_OAUTH_SCOPES` | `gemini.oauth.scopes` | 空 | 留空时按 OAuth 子类型自动选择；使用内置客户端时会剔除其不能申请的权限 |
| `GEMINI_QUOTA_POLICY` | `gemini.quota` | 空 | 以 JSON 覆盖本地配额模拟（仅 Code Assist），见下文 |
| `GEMINI_CLI_OAUTH_CLIENT_SECRET` | — | 空 | 内置 Gemini CLI 客户端的 secret 后备值。程序已内置该公共客户端的 secret，通常无需设置；只有内置值缺失时才读取，缺失且未设置时报 `GEMINI_CLI_OAUTH_CLIENT_SECRET_MISSING` |

回调地址：使用内置客户端时为 `https://codeassist.google.com/authcode`；使用自建客户端时为 `http://localhost:1455/auth/callback`，必须在 Google OAuth Client 中登记且完全一致。

修改 Client ID 或 Secret 后，用旧 Client 授权的账号会因 refresh token 与 Client 不匹配而刷新失败，需要重新授权，或恢复原来的 Client 配置。

**账号等级与本地配额**：Gemini 官方没有用量查询接口。添加账号时可选择账号等级（Google One Free / Pro / Ultra、GCP Standard / Enterprise、Google AI Free / Pay-as-you-go），系统会先尝试自动识别，识别失败时使用所选等级。界面显示的“每日配额”是按等级模拟的估算值，只用于进度显示和调度预检，以 Google 实际报错为准。Code Assist 的模拟值可以用 `gemini.quota.tiers` 或 `GEMINI_QUOTA_POLICY` 覆盖，例如：

```bash
GEMINI_QUOTA_POLICY={"tiers":{"LEGACY":{"pro_rpd":50,"flash_rpd":1500,"cooldown_minutes":30},"PRO":{"pro_rpd":1500,"flash_rpd":4000,"cooldown_minutes":5},"ULTRA":{"pro_rpd":2000,"flash_rpd":0,"cooldown_minutes":5}}}
```

管理接口：`POST /api/v1/admin/gemini/oauth/auth-url`、`POST /api/v1/admin/gemini/oauth/exchange-code`、`GET /api/v1/admin/gemini/oauth/capabilities`（返回 `ai_studio_oauth_enabled` 与需要登记的回调地址）。

## 对外端点

| 端点 | 说明 |
|---|---|
| `GET /v1beta/models`、`GET /v1beta/models/:model` | Gemini 原生模型列表 |
| `POST /v1beta/models/{model}:generateContent` | Gemini 原生生成 |
| `POST /v1beta/models/{model}:streamGenerateContent?alt=sse` | Gemini 原生流式生成 |
| `POST /v1/messages` | Anthropic Messages 格式，转换后转发到 Gemini |
| `POST /v1/chat/completions` | Chat Completions 格式，转换后转发到 Gemini |

`/v1beta` 端点的密钥读取顺序为 `x-goog-api-key` 请求头、`Authorization: Bearer`、`x-api-key`，最后是查询参数 `key`（只在 `/v1beta` 与 `/antigravity/v1beta` 下接受）；查询参数 `api_key` 已弃用，会返回 400。

开启了混合调度的 Antigravity 账号也会参与 Gemini 分组的调度，见 [Antigravity](antigravity.md)。排查上游问题时可短时开启 `gateway.gemini_debug_response_headers`（默认 `false`）记录上游响应头，排障后关闭。

## 客户端配置

### Gemini CLI

`GOOGLE_GEMINI_BASE_URL` 填 XY2API 的根地址，不带 `/v1beta`。

macOS / Linux：

```bash
export GOOGLE_GEMINI_BASE_URL="https://your-xy2api.example.com"
export GEMINI_API_KEY="sk-your-xy2api-key"
export GEMINI_MODEL="gemini-2.0-flash"
```

Windows PowerShell：

```powershell
$env:GOOGLE_GEMINI_BASE_URL="https://your-xy2api.example.com"
$env:GEMINI_API_KEY="sk-your-xy2api-key"
$env:GEMINI_MODEL="gemini-2.0-flash"
```

`GEMINI_MODEL` 可换成账号有权限的其他模型。这些环境变量只在当前终端会话生效，需要持久化时写入 `~/.bashrc`、`~/.zshrc` 等配置文件。

### OpenCode

「使用密钥」弹窗为 Gemini 分组生成 OpenCode 配置（provider 的 `baseURL` 为 `https://your-xy2api.example.com/v1beta`），保存为 `~/.config/opencode/opencode.json`。
