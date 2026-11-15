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

`/v1/chat/completions` 中图片的 `image_url.detail` 为 `auto`、`low`、`high` 时，Gemini 没有对应参数，网关去掉该提示并记录降级，图片照常发送；其他值返回 400。质量与费用不保证与指定 `detail` 时相同。

`/v1beta` 端点的密钥读取顺序为 `x-goog-api-key` 请求头、`Authorization: Bearer`、`x-api-key`，最后是查询参数 `key`（只在 `/v1beta` 与 `/antigravity/v1beta` 下接受）；查询参数 `api_key` 已弃用，会返回 400。

开启了混合调度的 Antigravity 账号也会参与 Gemini 分组的调度，见 [Antigravity](antigravity.md)。排查上游问题时可短时开启 `gateway.gemini_debug_response_headers`（默认 `false`）记录上游响应头，排障后关闭。

### 兼容入口参数

兼容入口指 Gemini 分组的 `/v1/chat/completions` 与 `/v1/messages`。API Key 账号的兼容请求按最终映射后的模型转换；Chat 请求会先转为 Anthropic 格式，这一步不套用 Claude 的最小思考预算，也不添加默认 thinking，输出上限保持原值，到生成 Gemini 请求时才按实际模型校验和映射：

- **思考控制**：没有显式意图时保留模型默认。优先级为 `disabled`/`none`、显式 effort、数值预算、单独的 `adaptive`；中间对象中的 adaptive 不覆盖 effort。Gemini 2.5 Flash 的 low/medium/high 分别映射为 1024/8192/24576，none 为 0；2.5 Pro 不能完全关闭，最低预算为 128。已确认接受 level 的型号采用最近的支持档位，不能保证原请求的思考强度或完全关闭；未知能力省略并记录。计费只读取最终实际发出的 `thinkingLevel`，预算与默认模式不推断收费档位。
- **JSON 输出**：Chat 的 `response_format` / `text.format` 和 Messages 的 `output_config.format` 为 `json_object` 时写入 `application/json`，`json_schema` 另外保留原始 `responseJsonSchema`；text/缺省不设置格式。仅支持 MIME 的型号省略 schema 并记录，不能保证 schema；未知 JSON 能力省略格式并记录，不能保证 JSON。请求带工具时同样省略格式并记录（Gemini 2.x 不接受工具与 JSON 输出同时出现），工具照常发送，不能保证 JSON。图片和正文仍保留，输出 schema 不经过工具 schema 清理。
- **工具 schema**：仅处理 `tools[*].functionDeclarations[*].parameters`。普通 schema 位置删除 enum 中恰为空串的成员，空数组或删完为空才移除 enum；空白串与其他成员保留。not、条件组 if/then/else、oneOf、contains 内命中时省略整个受影响操作符（contains 同时省略 minContains/maxContains），保留兄弟约束；因此可能失去否定、条件、唯一分支或匹配计数约束。每次省略记录字段路径及 omit，不把 default、const、examples 或属性名称当 schema 递归。
- **工具选择**：auto/none/any 映射为 AUTO/NONE/ANY，指定函数映射为 ANY 与 allowedFunctionNames。none 移除当前 builtin 声明，保留历史；强制 builtin 保留声明并记录 map，不能保证本轮必调用。禁并行控制省略并记录 omit，不能保证只返回一个调用。畸形选择、非布尔并行控制或未声明的工具名返回指出字段的错误。
- **工具历史**：按轮次与调用出现顺序配对结果，旧式同名调用继续按 FIFO 消费；不新增历史闭合校验，缺结果、孤儿结果与同轮重复 ID 沿原有转换方式处理。
- **结束原因**：正常停止保留为自然结束或工具调用；MAX_TOKENS 优先保留长度截断，工具存在也不改成正常工具结束。安全过滤映射为 Messages refusal 或 Chat content_filter；OTHER 与未列出的非空停因沿用自然结束/完整工具的兼容映射，并记录 map；明确的坏工具停因返回上游转换错误，流式错误后不再追加成功结束帧。已读用量保留，完整工具参数必须是 JSON 对象。
- **缓存用量**：未缓存输入为 `max(promptTokenCount - cachedContentTokenCount, 0)`，缓存读取保留原值；输出只加一次 candidates 与 thoughts。流中按字段保留最近一次有效计数，缺字段不覆盖已知值，显式零或修正值正常覆盖。缺失 prompt 与缓存大于 prompt 分别记录内部不完整、矛盾证据；存在位只用于内部记录，不据此省略客户端原有计数。
- **思考文本**：上游 `thought: true` 的文本放进 Messages 的 `thinking` 块或 Chat 的 `reasoning_content`，普通正文单独输出；非流式与流式相同。之后的请求在 assistant 历史里带回的 `thinking`、`redacted_thinking` 块不发给 Gemini（只有思考块的那一轮照原方式转换），工具调用的签名不受影响。
- **AI Studio 搜索工具**：只调整搜索工具的字段拼写，输出 schema 原样保留，大整数不丢精度。

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
