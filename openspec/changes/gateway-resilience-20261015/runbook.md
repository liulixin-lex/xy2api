# 运维手册：xy2api 0.4.0 与 XY-image 0.0.17 上线

> **授权：**
> - 本手册中所有生产操作（修改 Caddy、部署容器、修改设置、修改 DNS）都要用户在执行时单独授权（AGENTS.md）。
> - 只读核查不受此限，但仍沿用只读口径（`default_transaction_read_only=on`，不写任何生产文件）。
>
> **凭据与主机信息：**
> - 凭据不写入 Git、PR、记忆文件或日志。
> - 本仓库公开。主机地址、容器名、宿主机路径与内网网段只记在仓库外的私有清单 `xy2api-artifacts/gateway-resilience-20261015/private/HOSTS.md`，本手册只用代称。
> - 本次对话中出现过生产与测试服务器的 SSH 密码，建议上线前两台都改用密钥登录，并更换密码。

## 1. 生产现状（2026-10-15 核实）

| 项目 | 现状 |
|---|---|
| 生产主机 | 单台 Ubuntu 主机（下称“生产主机”） |
| 主应用 | 0.3.0 主容器（下称 blue），镜像 `ghcr.io/liulixin-lex/xy2api:0.3.0`，监听本机回环的 blue 端口（下称 `<BLUE_PORT>`）；Compose 文件、环境文件与端口见仓库外的私有主机清单 |
| 数据库与缓存 | PostgreSQL 容器（PITR 备份）；Redis 容器 |
| Caddy | Caddy 主配置（路径见私有主机清单）。`gguuai.com` 走 Cloudflare，`api.aiaimax.com` 走阿里云 ESA，都经 `sub2api_upstream` 反代到 `<BLUE_PORT>`；`handle_errors` 返回纯文本 |
| CDN 超时 | ESA 回源超时已调为 300 秒（2026-10-15 约 06:40Z 生效）；Cloudflare 固定 125 秒 |
| XY-image | 同机独立 Compose 项目，v0.0.16（`258c0c9`）；经内网网段直连本机 Caddy，不经 CDN |
| 运行设置 | 429 冷却 45 秒（07:05Z 修改）；过载冷却 2 分钟 |

## 2. 上线前检查

1. 测试服务器的验证全部闭环（staging-validation.md 第 9 节），正式版附件与镜像核验通过。
2. 备份：
   - 确认 PITR 备份最近一次全量与 WAL 正常；
   - 额外导出 `settings` 表与应用数据目录（只读打包到宿主机备份目录）；
   - 备份 Caddy 主配置与全部片段。
3. 核对应用环境：`SERVER_TRUSTED_PROXIES` 已配置并包含 Caddy 到应用的对端地址（见 §3.1）。
4. 记录基线：用 `tools/gateway-resilience/baseline.sql` 与 `caddy_cuts.py` 采集最近 24 小时的指标，作为上线对照。
5. 选择低峰时段：从生产数据看，北京时间凌晨（UTC 18:00–22:00）的请求量最低。

## 3. Caddy 配置

### 3.1 边缘标记（D12）

新增两个片段，CF 与 ESA 回源网段分别成组，取值与全局 `trusted_proxies` 中的同类网段相同：

```caddy
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cf remote_ip <Cloudflare 回源网段>
	request_header @xy_edge_cf X-XY-Edge cf
	@xy_edge_esa remote_ip <ESA 回源网段>
	request_header @xy_edge_esa X-XY-Edge esa
}
```

- 在 `gguuai.com`、`api.aiaimax.com` 站点的 `route` 开头 `import xy_edge_marker`。
- `remote_ip` 匹配的是直连对端，不受客户端头影响。XY-image 的内网请求与本机脚本不会被标记。
- 校验：`caddy validate`。
- 用测试请求确认：经 CF 的慢首包流式请求，在第 15 秒左右出现 `resilience.keepalive_committed` 日志且带 `edge=cf`；直连请求不出现该日志。
- 应用只在对端属于 `SERVER_TRUSTED_PROXIES` 时读取该头。该列表必须包含 Caddy 到应用的对端地址（同机部署时为容器网络网关或回环地址），否则所有请求都按直连处理，首包前保活不会生效。

### 3.2 错误页（避免纯文本 5xx）

API 路径（`/v1*`、`/v1beta*`、`/responses*`）的 `handle_errors` 返回 JSON：

- 状态码一律为 503：Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉正文与头部（测试服务器 CF-3 实测），503 原样透传；
- 正文：`{"error":{"type":"api_error","code":"proxy_upstream_unavailable","message":"Service temporarily unavailable, please retry"}}`；
- 头部：`Retry-After: 5`、`X-XY-Proxy-Error: upstream_unavailable`。

网页路径保持原样。XY-image 0.0.17 会把这个标记识别为“未发出”。

### 3.3 零停机切换（`lb_policy first`）

```caddy
(sub2api_upstream) {
	reverse_proxy 127.0.0.1:<BLUE_PORT> 127.0.0.1:<GREEN_PORT> {
		lb_policy first
		health_uri /health
		health_interval 5s
		health_timeout 3s
		health_fails 2
		health_passes 1
		stream_close_delay 15m
		# 其余 header_up 与 transport 配置保持原样
	}
}
```

- 上游的书写顺序决定谁接流量：第一个健康的上游接全部流量，第二个只作热备。
- 切换只调整顺序后执行 `systemctl reload caddy`（平滑重载）。
- 先让新实例通过本机健康检查，再调整顺序。
- `encode` 不匹配 `text/event-stream`（现有配置如此），不得新增，避免 SSE 被缓冲或压缩。

## 4. 部署顺序

### 4.1 XY-image 0.0.17

1. 按 XY-image 自身的升级步骤（其部署目录中 `DEPLOYMENT.md` 的“升级步骤”节）执行：
   - 确认没有在途任务；
   - 热备份；
   - 在另一台机器构建镜像并导入；
   - 更新 `runtime/compose.json` 中的三处镜像；
   - 执行 `compose up`。
2. 验证：
   - `/api/ready` 正常；
   - 对话、同步生图、异步生图各跑一次；
   - 待核对任务能正常收敛。
3. 0.0.17 与 xy2api 0.3.0 兼容，可以独立运行观察。

### 4.2 xy2api 0.4.0（蓝绿）

1. 在生产部署目录下新建 `compose.green.yaml`：
   - 镜像固定为 `ghcr.io/liulixin-lex/xy2api:0.4.0@sha256:<正式 digest>`；
   - 容器名取 blue 容器名加 `-green` 后缀，端口 `127.0.0.1:<GREEN_PORT>:8080`（与 blue 错开）；
   - 环境文件、挂载、网络与当前实例相同。
2. 启动 green。0.4.0 启动时执行迁移 272–275，全部只追加，0.3.0 实例可以继续服务。
   - 观察迁移日志与耗时；
   - `curl -fsS 127.0.0.1:<GREEN_PORT>/health` 返回 200。
3. 冒烟（直连 `<GREEN_PORT>`，不经 Caddy）：
   - 管理员登录；
   - `GET /api/v1/admin/settings/gateway-resilience` 返回缺省值；
   - 用内部测试 Key 跑一次 Responses 流式、Chat、Messages、图片。
4. 修改 Caddy 上游顺序为 `<GREEN_PORT> <BLUE_PORT>`，然后 reload。观察 30 分钟：
   - 状态码分布；
   - 首字分位；
   - `resilience.*` 日志；
   - 心跳提交数；
   - 未送达记录；
   - 计费是否正常。
5. 正常后停止 blue。把 green 改名为主实例，或保持 green 为主，按团队习惯更新 compose 文件，并更新生产主机上的部署记录文件。

### 4.3 运行设置

`gateway_resilience_settings` 缺省全部开启（D23）。上线后只核对设置值，不做灰度。要逐项关闭时，在“网关韧性”设置卡中操作，无需重启。

## 5. 部署后验证

| 时间点 | 检查 |
|---|---|
| 30 分钟 | 状态码分布与基线一致或更好；没有 panic、重启或迁移错误；`resilience.keepalive_committed` 只出现在带 CF/ESA 标记的请求上；XY-image 请求没有被标记 |
| 24 小时 | 按 design §14 输出指标报告：切断率、切断后计费、无号 503、换号耗时、同会话连续失败、余额置错、余额误报、`Retry-After` 覆盖率、缓存与首字 |
| 72 小时 | 复核同一组指标；核对计费（未送达记录与用量无重复、预留无孤儿）；更新 PROJECT_MEMORY |

## 6. 应急处置

**按能力关闭**（“网关韧性”设置卡，即时生效）：

| 开关 | 关闭后的行为 |
|---|---|
| `keepalive.enabled` | 不再提前提交与心跳，回到 0.3.0 |
| `budget.enabled` | 回到原换号次数与首输出口径 |
| `session.enabled` / `last_resort.enabled` | 粘性与选号回到 0.3.0 |
| `balance.enabled` | 余额错误按 0.3.0 处理 |
| `disconnect.enabled` | 断开后按 0.3.0 继续读完并计费 |
| `errors.enabled` | 不加 `Retry-After`，不做负缓存与快速失败 |
| `fallback_group.enabled` | 不再转投兜底组 |

**整体回退：**

- Caddy 上游顺序改回 `<BLUE_PORT> <GREEN_PORT>`（blue 仍在时），或重新启动 0.3.0 镜像后再调整顺序；
- 数据库无需回退，新列与新表对 0.3.0 透明；
- 回退后在记忆中记录原因与证据。

**触发条件**（任一满足即先关闭相关开关，必要时整体回退）：

- 30 分钟内 SLA 错误率比基线高 1 个百分点以上；
- 首字 P95 比基线高 10% 以上；
- 出现未送达但被计费、预留孤儿、负余额异常增长；
- 服务崩溃或重启循环；
- XY-image 待核对任务异常增多。

## 7. 交接

- 更新 `docs/PROJECT_MEMORY.md`：部署时间、镜像 digest、迁移结果、指标报告、遗留问题。
- 证据保存在仓库外的证据目录，不含凭据。
