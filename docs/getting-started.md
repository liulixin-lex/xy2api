# 快速开始

本文带你在一台 Linux 服务器上用 Docker Compose 部署 XY2API，然后完成第一次调用：登录管理后台，接入上游账号，创建分组和 API Key，再把 Claude Code、Codex 或任意 OpenAI 兼容客户端指向你的网关。适合第一次部署 XY2API 的管理员，全程大约 15 分钟。生产环境还需要的反向代理、配置调整和升级方法，见文末“下一步”。

## 准备

| 项目 | 要求 |
| --- | --- |
| 服务器 | Linux x86-64。官方镜像只提供 `linux/amd64`，ARM 服务器请看 [从源码构建](deployment/source.md) |
| 软件 | Docker 20.10 或更新、Docker Compose v2（`docker compose version` 能输出版本）、`curl`、`openssl` |
| 网络 | 服务器能访问 GitHub 和 `ghcr.io`；浏览器能访问服务器的 8080 端口（正式使用建议放到 HTTPS 反向代理后面） |
| 上游账号 | 至少一个：Claude（OAuth、Setup Token 或 API Key）、OpenAI / Codex、Gemini 等 |

## 1. 部署

在一个空目录里运行部署准备脚本，然后启动：

```bash
mkdir -p xy2api && cd xy2api
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/docker-deploy.sh | bash
docker compose up -d
docker compose ps
```

脚本只准备文件，不启动容器。它会：

- 下载 `docker-compose.local.yml`（保存为 `docker-compose.yml`）和 `.env.example`；
- 生成随机的 `POSTGRES_PASSWORD`、`JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 和管理员登录邮箱 `ADMIN_EMAIL`，写入 `.env`，并把 `.env` 权限设为 `600`；
- 创建 `data/`、`postgres_data/`、`redis_data/` 三个数据目录（整个目录打包即可迁移）。

脚本会在终端打印生成的凭据。它们都已保存在 `.env` 里，请妥善保管，不要提交到任何仓库。

首次启动时容器会自动初始化：连接 PostgreSQL 和 Redis、执行数据库迁移、创建管理员、写出 `data/config.yaml`。可以用健康检查确认服务已就绪：

```bash
curl -s http://localhost:8080/health   # {"status":"ok"}，进程存活
curl -s http://localhost:8080/ready    # {"status":"ready"}，数据库和 Redis 均可用
```

## 2. 首次登录

管理员账号只在第一次启动、数据库里还没有任何用户时创建：

- **登录邮箱**：脚本生成的 `ADMIN_EMAIL`，可以用 `grep ^ADMIN_EMAIL .env` 查看。
- **密码**：未在 `.env` 里设置 `ADMIN_PASSWORD` 时自动生成，只在首次启动的日志里出现一次：

```bash
docker compose logs xy2api | grep "Generated admin"
# Generated admin password (one-time): 3f9c...
```

如果你没有用脚本、`ADMIN_EMAIL` 也留空，日志里还会多一行 `Generated admin email (login username): admin-<随机串>@xy2api.local`。

请立刻记下密码：它只写到容器的标准输出，容器被删除重建后就查不到了。也可以在第一次启动前在 `.env` 里自己设置 `ADMIN_PASSWORD`（8–72 字节）。

在浏览器打开 `http://<服务器 IP>:8080`，用邮箱和密码登录。登录后建议先到“个人资料”修改密码。

## 3. 添加上游账号

上游账号是 XY2API 实际用来调用 AI 服务的凭据。

1. 进入“账号管理”，点击“添加账号”。
2. 填写“账号名称”，选择“平台”和“账号类型”。不同平台支持的方式不同，例如 Claude 可以用 OAuth、Setup Token 或 Claude Console 的 API Key，OpenAI 可以用 OAuth 或 API Key。
3. 按对话框提示完成授权或粘贴密钥，保存。
4. 在账号列表的操作里使用“测试连接”，确认账号可用。

对话框里的“分组”此时可以先不选，下一步建好分组后再回来勾选。各平台账号的细节见 [Claude](platforms/anthropic.md)、[OpenAI / Codex](platforms/openai.md)、[Gemini](platforms/gemini.md)、[Grok](platforms/grok.md)、[Antigravity](platforms/antigravity.md)。

## 4. 创建分组

分组决定一个 API Key 能用哪些上游账号、按什么倍率计费。

1. 进入“分组管理”，点击“创建分组”。
2. 填写“分组名称”，选择“平台”（必须与上一步账号的平台一致，创建后不能修改）。
3. “费率倍数”先用 `1.0`；“专属分组”保持关闭，表示所有用户都能使用。
4. 保存后回到“账号管理”，编辑刚才的账号，在“分组”里勾选这个分组并保存。

没有加入任何分组的账号不会被调度。分组的更多选项见 [分组](guides/groups.md)。

## 5. 创建 API Key

默认的标准模式会对每次请求计费，API Key 所属用户的余额不足时请求会被拒绝（HTTP 403，提示余额不足）。管理员账号初始余额为 0，所以先充值：

1. 进入“用户管理”，在要使用的用户（包括管理员自己）的操作里选择“充值”，输入金额。
2. 进入“API 密钥”，点击“创建密钥”，填写名称并选择上一步的分组，创建后复制以 `sk-` 开头的密钥。
3. 在密钥列表里点击“使用密钥”，对话框会按分组的平台生成 Claude Code、Codex CLI、Gemini CLI、OpenCode 等客户端的现成配置，可以直接复制。

只想自用、不需要计费？可以改用 [简易模式](deployment/configuration.md#简易模式)，它跳过余额和计费检查。

先用 `curl` 确认密钥可用：

```bash
curl -s http://<服务器 IP>:8080/v1/models \
  -H "Authorization: Bearer sk-你的密钥"
```

## 6. 配置客户端

下表中的 `https://api.example.com` 换成你的访问地址（测试阶段可以是 `http://<服务器 IP>:8080`）。如果站点对外地址与浏览器里看到的不同，可以在“系统设置”的“API 端点地址”里填写，“使用密钥”对话框会使用它生成配置。

| 客户端 | 填写的地址 | 密钥放在 |
| --- | --- | --- |
| Claude Code、Anthropic SDK | `https://api.example.com`（不带 `/v1`，客户端会自己拼 `/v1/messages`） | `ANTHROPIC_AUTH_TOKEN` |
| Codex CLI | `https://api.example.com/v1` | `~/.codex/auth.json` |
| OpenAI 兼容客户端 | `https://api.example.com/v1` | `Authorization: Bearer` 请求头 |

网关也接受 `x-api-key` 请求头（Gemini 客户端用的 `x-goog-api-key` 同样可以）。`/v1` 下的通用接口不接受把密钥放在 URL 查询参数里。

### Claude Code

在终端里设置环境变量（只对当前会话生效）：

```bash
export ANTHROPIC_BASE_URL="https://api.example.com"
export ANTHROPIC_AUTH_TOKEN="sk-你的密钥"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

要长期生效，写到 `~/.claude/settings.json`（Windows 为 `%USERPROFILE%\.claude\settings.json`）。这个文件含密钥，不要提交到项目仓库：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://api.example.com",
    "ANTHROPIC_AUTH_TOKEN": "sk-你的密钥",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

最直接的组合是 Anthropic 平台的分组。其他平台的分组能否给 Claude Code 用，以“使用密钥”对话框里有没有 Claude Code 标签为准。

### Codex CLI

使用 OpenAI 平台分组的密钥。最小配置如下，`model` 填分组里账号支持的模型；“使用密钥”对话框生成的版本还带有模型目录和推理强度等设置，建议直接复制那份。

`~/.codex/config.toml`（Windows 为 `%USERPROFILE%\.codex\config.toml`），放在文件开头：

```toml
model_provider = "OpenAI"
model = "<模型名>"

[model_providers.OpenAI]
name = "OpenAI"
base_url = "https://api.example.com/v1"
wire_api = "responses"
requires_openai_auth = true
```

`~/.codex/auth.json`：

```json
{
  "OPENAI_API_KEY": "sk-你的密钥"
}
```

Codex 依赖 `session_id` 请求头保持会话粘性。如果前面有 Nginx，需要开启 `underscores_in_headers on;`，见 [反向代理](deployment/reverse-proxy.md)。

### OpenAI 兼容客户端

把 Base URL 设为 `https://api.example.com/v1`，API Key 填 `sk-` 密钥即可。网关按密钥所属分组的平台自动转发以下接口：

| 接口 | 用途 |
| --- | --- |
| `POST /v1/chat/completions` | Chat Completions |
| `POST /v1/responses` | Responses（`GET` 为 WebSocket） |
| `POST /v1/messages` | Anthropic Messages |
| `GET /v1/models` | 模型列表 |

```bash
curl https://api.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "<模型名>", "messages": [{"role": "user", "content": "你好"}]}'
```

Gemini CLI 的配置同样可以从“使用密钥”对话框复制，账号与 OAuth 配置见 [Gemini](platforms/gemini.md)。

## 常见问题

| 现象 | 排查 |
| --- | --- |
| 浏览器打不开页面 | `docker compose ps` 看容器是否 healthy；`docker compose logs --tail=100 xy2api` 看错误；检查防火墙和云厂商安全组是否放行端口 |
| 登录提示邮箱或密码错误 | 邮箱以 `.env` 的 `ADMIN_EMAIL` 为准；密码只在首次启动日志里；数据库已有用户时不会再创建管理员 |
| 请求返回 401 | 请求头里没有密钥，或密钥填错、已禁用 |
| 请求返回 403 且提示余额不足 | 给密钥所属用户充值，或改用简易模式 |
| 请求返回 503、提示没有可用账号 | 账号没有加入该分组、平台不一致、账号被禁用或正在限流；先在“账号管理”里“测试连接” |

## 下一步

- [Docker Compose 部署](deployment/docker.md)：Compose 文件的区别、变量、数据目录与排障。
- [反向代理、CDN 与边缘安全](deployment/reverse-proxy.md)：配置 HTTPS、真实客户端 IP 与流式响应。
- [配置文件与环境变量](deployment/configuration.md)：常用配置项与简易模式。
- [升级、回滚与备份](deployment/upgrade.md)：上线前先配置好数据库备份。
- [账号调度](guides/scheduling.md)、[网关韧性](guides/gateway-resilience.md)：多账号时的选号与容错。
