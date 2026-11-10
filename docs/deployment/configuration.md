# 配置文件与环境变量

本文说明 XY2API 从哪里读取配置、配置文件放在哪里、环境变量如何对应配置项，并列出部署时最常调整的配置、安全选项、OpenAI WebSocket 相关的开关和简易模式。适合需要调整默认行为的运维人员。完整的配置项与注释见 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)，容器可用的变量见 [`deploy/.env.example`](../../deploy/.env.example)。

## 配置的来源

程序启动时按以下优先级合并配置，前者覆盖后者：

1. 环境变量；
2. 配置文件 `config.yaml`；
3. 程序内置默认值。

此外，很多运行期设置（站点信息、安全与认证、网关韧性、备份、支付等）保存在数据库里，在管理后台“系统设置”中修改，保存后即时生效，不需要改配置文件或重启。本文只涉及配置文件和环境变量。

### 配置文件的位置

设置了 `CONFIG_FILE` 时只读取该文件。否则按顺序查找名为 `config.yaml` 的文件，用找到的第一个：

1. `$DATA_DIR`（设置了 `DATA_DIR` 时）
2. `/app/data`
3. 当前工作目录
4. `./config`
5. `/etc/xy2api`
6. `/etc/sub2api`（兼容旧安装）

设置向导和容器的自动初始化把 `config.yaml` 与安装锁 `.installed` 写到数据目录：`DATA_DIR`；未设置时，如果 `/app/data` 存在且可写就用它，否则用当前工作目录。因此 Docker 部署的配置在 `/app/data/config.yaml`，安装脚本部署的在 `/opt/xy2api/config.yaml`。

数据目录里 `config.yaml` 和 `.installed` 都不存在时，程序认为尚未安装：设置了 `AUTO_SETUP=true` 就用环境变量自动初始化，否则启动设置向导。

### 环境变量的命名

配置项的环境变量名是把键名转成大写、把 `.` 换成 `_`，例如：

| 配置项 | 环境变量 |
| --- | --- |
| `server.port` | `SERVER_PORT` |
| `database.max_open_conns` | `DATABASE_MAX_OPEN_CONNS` |
| `gateway.openai_ws.force_http` | `GATEWAY_OPENAI_WS_FORCE_HTTP` |

只有程序已知的配置项才能这样覆盖。几个例外：

- 时区用标准的 `TZ`（也接受 `TIMEZONE`），对应 `timezone`；
- `SERVER_TRUSTED_PROXIES` 和 `SECURITY_FORWARDED_CLIENT_IP_HEADERS` 用逗号分隔多个值，显式设为空字符串会清空配置文件里的列表；
- `XY2API_OPENAI_TOTP_WORKER_SECRET` 对应 `openai_totp.worker_secret`。

Docker Compose 只把 Compose 文件里列出的变量传进容器，其他变量需要用覆盖文件传入，见 [Docker Compose 部署](docker.md#传入更多环境变量)。

## 初始化用的变量

以下变量在自动初始化（`AUTO_SETUP=true`）时读取，结果写入 `config.yaml`。其中数据库、Redis、服务器和 JWT 相关的变量同时也是普通配置项，初始化之后仍会覆盖配置文件。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `AUTO_SETUP` | 未设置 | `true` 时用环境变量完成首次初始化，不显示向导 |
| `DATABASE_HOST` / `DATABASE_PORT` / `DATABASE_USER` / `DATABASE_PASSWORD` / `DATABASE_DBNAME` / `DATABASE_SSLMODE` | `localhost` / `5432` / `postgres` / 空 / `xy2api` / `disable` | PostgreSQL 连接 |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_USERNAME` / `REDIS_PASSWORD` / `REDIS_DB` / `REDIS_ENABLE_TLS` | `localhost` / `6379` / 空 / 空 / `0` / `false` | Redis 连接 |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | 空 | 数据库里没有任何用户时创建的管理员；留空则随机生成并打印到标准输出。密码 8–72 字节 |
| `JWT_SECRET` / `JWT_EXPIRE_HOUR` | 自动生成 / `24` | 见下文“密钥” |
| `SERVER_HOST` / `SERVER_PORT` / `SERVER_MODE` | `0.0.0.0` / `8080` / `release` | 监听地址与运行模式 |
| `SETUP_MIGRATION_TIMEOUT_SECONDS` | `0`（即 60 秒） | 初始化时数据库迁移的超时 |
| `DATA_DIR` | 未设置 | 数据目录，影响配置、日志、插件和用量导出的位置 |
| `CONFIG_FILE` | 未设置 | 指定配置文件路径 |

管理员只能通过设置向导或自动初始化创建。`config.yaml` 里的 `default.admin_email` / `default.admin_password` 不会被使用。

## 密钥

| 配置项 | 环境变量 | 说明 |
| --- | --- | --- |
| `jwt.secret` | `JWT_SECRET` | 登录令牌签名密钥，至少 32 字节。程序第一次使用的值会存入数据库（多个实例共享），之后以数据库中的值为准；配置值与之不一致时日志会提示并继续使用数据库中的值。都没有时自动生成并存入数据库 |
| `totp.encryption_key` | `TOTP_ENCRYPTION_KEY` | 64 位十六进制（32 字节），`openssl rand -hex 32` 生成。用于加密 2FA 密钥、支付配置、备份存储凭据、插件密钥等。留空时每次启动随机生成，重启后已加密的内容无法解密，也无法启用 2FA；格式不对时程序拒绝启动 |

这两项都应在部署时固定下来，并与数据库一起备份。

## 常用配置项

| 配置项 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `server.mode` | `SERVER_MODE` | `release` | `debug` 仅用于开发 |
| `server.frontend_url` | `SERVER_FRONTEND_URL` | 空 | 生成邮件中链接（如重置密码）使用的站点地址 |
| `server.trusted_proxies` | `SERVER_TRUSTED_PROXIES` | 未设置 | 直接连接 XY2API 的反向代理地址，见 [反向代理](reverse-proxy.md) |
| `server.max_request_body_size` | `SERVER_MAX_REQUEST_BODY_SIZE` | `268435456`（256 MiB） | 全局请求体上限 |
| `server.restart_drain_seconds` | `SERVER_RESTART_DRAIN_SECONDS` | `30` | 在线更新后重启时等待在途请求的秒数，0–600 |
| `database.max_open_conns` / `max_idle_conns` | `DATABASE_MAX_OPEN_CONNS` / `DATABASE_MAX_IDLE_CONNS` | `256` / `128` | 连接池。所有实例的上限之和要小于 PostgreSQL 的 `max_connections` |
| `redis.pool_size` / `min_idle_conns` | `REDIS_POOL_SIZE` / `REDIS_MIN_IDLE_CONNS` | `1024` / `128` | Redis 连接池 |
| `jwt.access_token_expire_minutes` | `JWT_ACCESS_TOKEN_EXPIRE_MINUTES` | `0` | 大于 0 时按分钟计，优先于 `jwt.expire_hour` |
| `timezone` | `TZ` | `Asia/Shanghai` | 影响统计的“今日”边界、订阅到期等 |
| `log.level` / `log.format` | `LOG_LEVEL` / `LOG_FORMAT` | `info` / `console` | 格式可选 `json` |
| `log.output.file_path` | `LOG_OUTPUT_FILE_PATH` | 自动 | 默认 `$DATA_DIR/logs/xy2api.log`，未设置 `DATA_DIR` 时为 `/app/data/logs/xy2api.log` |
| `log.service_name` | `LOG_SERVICE_NAME` | `xy2api` | 日志中的 `service` 字段，多实例时用来区分 |
| `ops.enabled` | `OPS_ENABLED` | `true` | 关闭后隐藏“运维监控”并停用相关后台任务 |
| `update.proxy_url` | `UPDATE_PROXY_URL` | 空 | 访问 GitHub（版本检查、价格数据）的代理，支持 http、https、socks5、socks5h |
| — | `UPDATE_GITHUB_TOKEN` | 空 | 仅用于 GitHub Release API 的版本检查，下载发布包仍匿名 |

Gemini OAuth 需要的 `GEMINI_OAUTH_CLIENT_ID`、`GEMINI_OAUTH_CLIENT_SECRET`、`GEMINI_CLI_OAUTH_CLIENT_SECRET` 等变量见 [Gemini](../platforms/gemini.md)。

## 安全相关配置

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `cors.allowed_origins` | `[]` | 允许跨域的来源；为空时不允许跨域请求 |
| `security.url_allowlist.enabled` | `false` | 开启后上游、价格数据和 CRS 同步地址必须在 `upstream_hosts`、`pricing_hosts`、`crs_hosts` 中 |
| `security.url_allowlist.allow_insecure_http` | `true` | 白名单关闭时是否允许 `http://` 地址 |
| `security.url_allowlist.allow_private_hosts` | `true` | 是否允许本地和私有 IP |
| `security.response_headers.enabled` | `true` | 过滤上游返回的敏感响应头 |
| `security.csp.enabled` / `policy` | `true` / 内置策略 | Content-Security-Policy；静态资源放在其他域名时需要调整 |
| `security.trust_forwarded_ip_for_api_key_acl` | `true` | 是否信任转发头里的客户端 IP，见 [反向代理](reverse-proxy.md#客户端-ip-与可信代理) |
| `billing.circuit_breaker.enabled` | `true` | 计费服务异常时拒绝请求，不放行未计费的调用 |
| `turnstile.required` | `false` | release 模式下强制要求配置 Turnstile 人机验证 |
| `gateway.upstream_response_read_max_bytes` | 128 MiB | 非流式上游响应的读取上限 |

生产环境建议至少收紧为只允许 HTTPS 上游：

```yaml
security:
  url_allowlist:
    enabled: false
    allow_insecure_http: false   # 设置后 http:// 上游地址会被拒绝：invalid url scheme: http
```

对应的环境变量是 `SECURITY_URL_ALLOWLIST_ENABLED`、`SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP`、`SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS`。关闭 URL 校验或响应头过滤时，请在网络层限制出站目标、阻断私网和回环地址。

登录、注册、2FA 和发送验证码接口自带基于 Redis 的限流，Redis 不可用时拒绝请求；这只是兜底，入口处仍应有 CDN、WAF 或反向代理限流。

## HTTP/2 明文（h2c）

`server.h2c.enabled`（`SERVER_H2C_ENABLED`）默认关闭。开启后明文端口同时接受 h2c 和 HTTP/1.1，WebSocket 与旧客户端走 HTTP/1.1。浏览器不使用 h2c，收益主要在反向代理到应用、或内网服务之间的连接。

| 配置项 | 默认值 |
| --- | --- |
| `server.h2c.max_concurrent_streams` | `50` |
| `server.h2c.idle_timeout` | `75` 秒 |
| `server.h2c.max_read_frame_size` | 1 MiB |
| `server.h2c.max_upload_buffer_per_connection` | 2 MiB |
| `server.h2c.max_upload_buffer_per_stream` | 512 KiB |

开启后可以这样验证：

```bash
curl --http2-prior-knowledge -I http://localhost:8080/health
curl --http1.1 -I http://localhost:8080/health
```

## OpenAI Responses WebSocket

客户端以 WebSocket 连接 `/v1/responses` 时，以下限制约束连接本身，与按回合占用、回合之间释放的用户和账号并发槽位相互独立：

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `gateway.openai_ws.client_first_message_timeout_seconds` | `30` | 接收并解压客户端第一条消息的总时限。上下文很大或链路较慢时可调到 120–300 |
| `gateway.openai_ws.ingress_inter_turn_idle_timeout_seconds` | `300` | 两个回合之间空闲超过该时长就关闭连接，0 为不限制 |
| `gateway.openai_ws.max_ingress_connections_per_api_key` | `64` | 每个 API Key 同时存在的客户端 WebSocket 连接上限，跨实例生效，0 为不限制 |

连接数上限通过 Redis 协调：每条连接持有 60 秒的租约，每 20 秒续期；某个实例在整个租约期内都无法确认租约时，会关闭本地连接，而不是越过全局上限继续服务。

在账号上选择 `http_bridge` 等 WebSocket 模式之前，需要先开启 v2 模式路由：

```yaml
gateway:
  openai_ws:
    mode_router_v2_enabled: true   # 或 GATEWAY_OPENAI_WS_MODE_ROUTER_V2_ENABLED=true
```

`http_bridge` 表示客户端仍用 WebSocket，上游改用 HTTP，适合逐步上线或绕开上游 WebSocket 问题。

### 强制上游使用 HTTP/SSE

出口代理或网络导致 OpenAI Responses 上游 WebSocket 反复重连时，可以全局改用 HTTP/SSE：

```yaml
gateway:
  openai_ws:
    force_http: true   # 或 GATEWAY_OPENAI_WS_FORCE_HTTP=true
```

它只改变上游协议，不影响客户端看到的协议，也不需要开启 `mode_router_v2_enabled`。它不会关闭 HTTP/2；代理不兼容 HTTP/2 时另外设置 `gateway.openai_http2.enabled: false`（`GATEWAY_OPENAI_HTTP2_ENABLED=false`）。请写在持久化的 `.env` 或 `config.yaml` 里，不要只改运行中的容器，否则更新镜像或重建容器后会丢失。

## TLS 指纹

`gateway.tls_fingerprint.enabled`（默认 `true`）是 TLS 指纹模拟的全局开关。指纹模板在管理后台“系统设置”的“TLS 指纹模板”中管理，并在账号上选择使用；平台相关说明见 [Claude](../platforms/anthropic.md)。

## 简易模式

简易模式面向个人或内部团队自用：界面隐藏用户管理、订阅、兑换码与优惠码、充值与订单、个人使用记录等面向运营的功能，并跳过余额、订阅与计费检查。

| 配置项 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `run_mode` | `RUN_MODE` | `standard` | 设为 `simple` 启用简易模式，启动日志会给出警告 |
| `simple_mode.auto_create_default_groups` | `SIMPLE_MODE_AUTO_CREATE_DEFAULT_GROUPS` | `true` | 每次启动时按平台补齐默认分组（如 `anthropic-default`、`openai-default`）。设为 `false` 后由你自己管理分组；关闭不会删除已有分组 |
| `simple_mode_key_rate_limit_enabled` | `SIMPLE_MODE_KEY_RATE_LIMIT_ENABLED` | `false` | 开启后按每个 API Key 设置的 5 小时、1 天、7 天消费额度限制请求，余额与订阅扣费仍然跳过 |

使用 Key 额度限制时注意：

- 以数据库为准，只记录 API Key 的窗口用量；
- 请求完成后才记账，同时进行中的请求可能各自按最终费用超出上限；
- 开启前的历史用量不会补算。

简易模式下首次初始化的管理员并发上限为 30（标准模式为 5）。
