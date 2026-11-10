# 反向代理、CDN 与边缘安全

本文说明如何把 XY2API 放到 Caddy、Nginx 或 CDN 后面：哪些代理设置会破坏流式响应和会话粘性，如何把真实客户端 IP 传给应用，CDN 场景下的信任边界、健康检查和限流分工。对外提供服务的部署都需要看。

## 基本原则

XY2API 的 SSE 和 WebSocket 请求可能持续十几分钟。反向代理需要：

- **不设置响应写超时**，读超时放宽到 30 分钟左右；应用本身也不设置 `WriteTimeout`。
- **不缓冲、不压缩 `text/event-stream`**，否则流式输出会攒到结束才一次发出。
- **支持 WebSocket 升级**（`/v1/responses` 的 `GET` 请求、管理后台的实时数据）。
- **保留名字含下划线的请求头**，例如 Codex 用来保持会话粘性的 `session_id`。
- **用 TCP 对端地址重写客户端 IP 头**，不要原样转发客户端自带的值。
- 不要在代理或应用前加一个全局的请求并发上限：一个 SSE 请求就可能占用它很久。连接数与未认证请求的限制放在边缘，已认证用户与 API Key 的并发由应用自己控制。

## 应用自带的限制

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `server.max_header_bytes` | 64 KiB | HTTP/1 请求头上限，HTTP/2 头列表按同一值限制 |
| `server.read_header_timeout` | 10 秒 | 读取完整请求头的时限，不限制请求处理和响应流 |
| `server.max_request_body_size` | 256 MiB | 全局请求体上限 |
| `gateway.max_body_size` | 256 MiB | 多模态、Gemini、图片、视频与批量生图接口 |
| `gateway.text_max_body_size` | 32 MiB | 纯文本接口 `/v1/embeddings`、`/v1/alpha/search` |
| `api_key_auth_cache.invalid_abuse.*` | 60 秒内 120 次失败后封禁 60 秒 | 按可信客户端 IP（IPv6 按 /64 聚合）统计无效凭据，单实例生效 |

无效凭据限制只是单实例的兜底，多实例部署仍需在负载均衡、CDN 或 WAF 上限流。

## Caddy

仓库自带的 [`deploy/Caddyfile`](../../deploy/Caddyfile) 是“客户端直连 Caddy”的基线配置：请求头上限 64 KiB、读取请求头 10 秒超时、请求体 256 MiB，转发头取自 TCP 对端（`{remote_host}`），并删除客户端自带的 `CF-Connecting-IP`。使用时把站点名 `api.xy2api.com` 改成你的域名，Caddy 会自动申请证书。

这份配置里需要注意的几点：

- 没有设置 `flush_interval`，Caddy 会自动立即刷新 `text/event-stream`，同时把客户端断开传给上游。不要全局设置它：正值会增加流式延迟，`-1` 在部分 Caddy 版本上会让客户端断开后请求继续执行。
- 压缩用显式的 Content-Type 列表，不包含 `text/event-stream`。不要改成 `text/*` 或简写的 `encode gzip zstd`，两者都会匹配 SSE。
- 自带的主动健康检查用 `health_uri /health`，只能判断进程是否存活。多实例、需要把数据库或 Redis 不可用的实例摘掉时，改成 `/ready`。
- `handle_errors` 返回纯文本。客户端更容易处理 JSON 错误与 `Retry-After`，可以对 API 路径单独返回，例如：

```caddyfile
handle_errors {
	@api path /v1/* /v1beta/* /responses* /chat/completions* /messages*
	handle @api {
		header Content-Type application/json
		header Retry-After 5
		respond `{"error":{"type":"api_error","message":"service temporarily unavailable"}}` 503
	}
	respond "{err.status_code} {err.status_text}"
}
```

Caddy 本身没有通用的请求频率限制，需要时使用可信的 CDN/WAF、限流模块或主机防火墙。

## Nginx

在 `http` 块中定义共享区，并开启下划线请求头（Nginx 默认会丢弃 `session_id` 这类头，多账号时 Codex 的会话粘性会失效）。下面的速率是保守的起点，请按实际流量调整：

```nginx
underscores_in_headers on;

limit_conn_zone $binary_remote_addr zone=xy2api_conn:20m;
limit_req_zone  $binary_remote_addr zone=xy2api_auth:20m rate=5r/s;
limit_req_zone  $binary_remote_addr zone=xy2api_api:40m rate=30r/s;

map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;
    # ssl_certificate / ssl_certificate_key ...

    client_header_timeout 10s;
    client_max_body_size 256m;
    large_client_header_buffers 4 16k;
    limit_conn xy2api_conn 40;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header CF-Connecting-IP "";
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_read_timeout 1800s;
    proxy_send_timeout 1800s;

    location ^~ /api/v1/auth/ {
        limit_req zone=xy2api_auth burst=10 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }

    location ~ ^/(v1/)?(embeddings|alpha/search)$ {
        client_max_body_size 32m;
        limit_req zone=xy2api_api burst=60 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }

    location / {
        limit_req zone=xy2api_api burst=60 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

`X-Forwarded-For` 用 `$remote_addr` 覆盖而不是追加。只有在 Nginx 的 real-IP 模块限定了可信代理网段时，才使用传入的 `$http_x_forwarded_for`。

`proxy_buffering off` 不会关闭 gzip。在 `http` 块开启 gzip 时，`gzip_types` 不要包含 `text/event-stream`，也不要写 `gzip_types *`：

```nginx
gzip on;
gzip_types text/plain text/css application/json application/javascript application/xml image/svg+xml;
```

共享的全局配置无法按类型排除 SSE 时，在 API 路径的 `location` 里设置 `gzip off;`。

### 前面还有一层四层 SNI 分流

443 端口先由 Nginx `stream` 块按 SNI（`ssl_preread`）转发 TCP 时，HTTP 层看到的 `$remote_addr` 是上一跳的回环地址，XY2API 会把所有客户端记成 `127.0.0.1`，按 IP 的限流、IP 白名单、注册风控和审计都会失效。用 PROXY 协议把来源地址带过去：

```nginx
stream {
    map $ssl_preread_server_name $sni_backend {
        api.example.com 127.0.0.1:9446;
        default         127.0.0.1:8443;
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_protocol on;          # 该 server 的所有后端都必须能解析 PROXY 头
        proxy_pass $sni_backend;
    }
}

http {
    server {
        listen 127.0.0.1:9446 ssl http2 proxy_protocol;
        server_name api.example.com;
        set_real_ip_from 127.0.0.1;
        real_ip_header proxy_protocol;
        # 其余同上，并把两个地址头改为：
        # proxy_set_header X-Real-IP $proxy_protocol_addr;
        # proxy_set_header X-Forwarded-For $proxy_protocol_addr;
    }
}
```

有后端不支持 PROXY 头时，给 XY2API 站点单独开一个接收 PROXY 头的监听，而不是再加一层会丢掉它的转发。配置后用外网请求测试，在 XY2API 访问日志的 `client_ip` 字段确认地址正确。

## 客户端 IP 与可信代理

XY2API 用客户端 IP 做 API Key 的 IP 白名单与黑名单、会话 IP/UA 绑定、限流和审计。有两种解析模式，由 `security.trust_forwarded_ip_for_api_key_acl` 决定，也可以在管理后台“系统设置 → 安全与认证”的“API Key IP 访问控制”中切换，即时生效：

| 模式 | 行为 | 前提 |
| --- | --- | --- |
| 信任转发头（默认，`true`） | 依次读取自定义请求头、`CF-Connecting-IP`、`X-Real-IP`、`X-Forwarded-For`，不校验直接对端 | 源站不能被直接访问，且边缘代理会覆盖上述所有请求头 |
| 可信代理链（`false`） | 只接受 `server.trusted_proxies` 中地址传来的转发头；其他情况用 TCP 对端地址 | 在 `server.trusted_proxies`（或 `SERVER_TRUSTED_PROXIES`）里列出直接连接 XY2API 的代理的精确地址或网段 |

- 自定义请求头用 `security.forwarded_client_ip_headers`（或逗号分隔的 `SECURITY_FORWARDED_CLIENT_IP_HEADERS`）配置，也可以在同一张设置卡里修改，最多 16 个。只在信任转发头模式下生效，按配置顺序优先于内置请求头；请求头的值可以是逗号分隔的多个 IP，无效项跳过，公网地址优先于私网地址。
- `server.trusted_proxies` 显式设为空列表表示不信任任何转发 IP。
- 信任转发头模式下，入口不在 Cloudflare 后面时必须删除客户端自带的 `CF-Connecting-IP`（Caddy `header_up -CF-Connecting-IP`，Nginx `proxy_set_header CF-Connecting-IP "";`），否则客户端可以自报 IP 绕过 API Key 的 IP 白名单。
- 读取设置失败或保存的请求头列表无效时，程序按可信代理链模式运行且不使用自定义请求头。
- 切换模式会改变已有会话的 IP 指纹。

反向代理与 XY2API 在同一台机器上时，可信代理链模式的配置是：

```yaml
server:
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
```

Docker 部署时应用看到的对端是 Docker 网桥网关，端口对外发布后直连客户端也来自同一地址，所以程序不会自动信任任何代理。用 Docker 部署又要用可信代理链模式时，把宿主机端口改为只绑定 `127.0.0.1`（`BIND_HOST=127.0.0.1`），再把网桥网段写进 `SERVER_TRUSTED_PROXIES`（需要用覆盖文件传入，见 [Docker Compose 部署](docker.md#传入更多环境变量)）。

## CDN

1. **先封住源站**：防火墙只允许 CDN 的回源网段（或内网负载均衡）访问源站，应用端口不对公网开放。
2. **在源站代理上只信任 CDN 网段**，并从解析出的客户端地址生成转发头。Caddy 示例（网段换成 CDN 官方发布、持续更新的回源地址）：

```caddyfile
{
	servers {
		trusted_proxies static 192.0.2.0/24 2001:db8:1234::/48
		trusted_proxies_strict
		client_ip_headers CF-Connecting-IP X-Forwarded-For
	}
}

api.example.com {
	reverse_proxy 127.0.0.1:8080 {
		header_up X-Real-IP {client_ip}
		header_up X-Forwarded-For {client_ip}
	}
}
```

   只有在源站无法被直接访问、Caddy 只信任这些对端的前提下，才可以采用 `CF-Connecting-IP`。不要在 CDN 后面原样使用自带 Caddyfile 里的 `{remote_host}`，那样所有客户端都会被记成 CDN 节点地址，无效凭据限制会误伤无关用户。
3. **在 XY2API 上**把 `server.trusted_proxies` 设为 Caddy 的地址或所在私网，让应用只接受 Caddy 改写过的头。
4. **在 CDN/WAF 上**配置连接数、请求头与请求体大小、机器人挑战以及按 IP/ASN 的速率限制。

### 经 CDN 的流式请求

CDN 有空闲超时（Cloudflare 约 100–125 秒）。首个 token 来得慢的流式请求，可能在客户端收到任何字节之前就被切断。XY2API 识别出请求经过 CDN 时，会提前发送 SSE 头并发送心跳；源站的 502/504 会改为 503 返回，原状态码放在 `X-XY-Upstream-Status` 响应头里（Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉 JSON 错误、`Retry-After` 和请求 ID）。

- Cloudflare 和阿里云 ESA 按它们自带的回源头自动识别：`CF-Ray` 或 `CDN-Loop: cloudflare` 识别为 `cf`，`CDN-Loop: esa` 识别为 `esa`，不需要配置代理或可信代理。伪造这些头只会改变伪造者自己那次请求的返回方式，不影响鉴权、计费和其他用户。
- 其他 CDN 可以由反向代理按 **TCP 对端地址** 注入 `X-XY-Edge` 头，先删除客户端自带的同名头：

```caddyfile
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cdn remote_ip <该 CDN 的回源网段>
	request_header @xy_edge_cdn X-XY-Edge <边缘名称>
}
```

  应用只在对端属于 `server.trusted_proxies` 时读取这个头，且取值必须在网关韧性设置 `keepalive.edges` 中（默认 `cf`、`esa`，新的名称需要加进去）。从 `keepalive.edges` 去掉 `cf` 或 `esa` 即关闭对应 CDN 的识别。

这些行为的完整说明见 [网关韧性](../guides/gateway-resilience.md)。

## 健康检查

| 接口 | 用途 |
| --- | --- |
| `/health` | 只表示进程存活，不访问任何依赖。用于容器健康检查和存活探针，数据库恢复期间不会导致进程被重启 |
| `/ready` | 检查数据库和 Redis（各 2 秒），不可用时返回 503 和失败项名称。用于反向代理主动健康检查（如 Caddy `health_uri /ready`）、就绪探针与外部监控 |

## DDoS 边界

应用内的检查只能减轻连接到达 Go 进程之后的放大效应，挡不住流量型攻击、TLS 洪泛、带宽打满或大量分布式来源。这些需要上游网络容量、CDN/WAF 过滤、云厂商防火墙和源站隔离。遭遇大量拒绝请求时，避免使用高基数指标或逐请求写数据库的安全日志。

## 排障

| 现象 | 原因与处理 |
| --- | --- |
| 所有请求的客户端 IP 都是 `127.0.0.1` 或同一个地址 | 前面有四层转发没传 PROXY 协议，或转发头取自上一跳；见上文 SNI 分流与 CDN 部分 |
| 流式输出卡住、结束时一次性出现 | 代理缓冲或压缩了 `text/event-stream`；检查 `proxy_buffering`、`gzip_types` 或 Caddy 的 `encode` 匹配 |
| 长请求在固定时长后断开 | 代理读超时太短，或 CDN 空闲超时；放宽超时，CDN 场景确认请求被识别为边缘请求 |
| Codex 多账号时频繁换号、缓存命中低 | Nginx 丢弃了 `session_id` 请求头，开启 `underscores_in_headers on;` |
| 返回 413 | 代理的请求体上限小于应用；对齐 `client_max_body_size` 或 `request_body max_size` |
| WebSocket 连接失败 | 代理没有转发 `Upgrade` / `Connection` 头 |
