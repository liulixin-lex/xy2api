# 运维手册：xy2api 0.4.0 与 XY-image 0.0.17 上线

> **授权：**
> - 本手册中所有生产操作（修改 Caddy、部署容器、修改设置、修改 DNS）都要用户在执行时单独授权（AGENTS.md）。
> - 只读核查不受此限，但仍沿用只读口径（`default_transaction_read_only=on`，不写任何生产文件）。
>
> **凭据与主机信息：**
> - 凭据不写入 Git、PR、记忆文件或日志。
> - 本仓库公开。主机地址、容器名、宿主机路径与内网网段只记在仓库外的私有清单 `xy2api-artifacts/gateway-resilience-20261015/private/HOSTS.md`，本手册只用代称。
> - 本次对话中出现过生产与测试服务器的 SSH 密码，建议上线前两台都改用密钥登录，并更换密码。2026-10-19 的会话中又出现了预上线主机的 SSH 密码，用户决定暂不更换。

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
3. 核对应用环境：`SERVER_TRUSTED_PROXIES` 已配置并包含 Caddy 到应用的对端地址（见 §3.1）。0.4.1 起这不再是首包前保活的前提（应用按 CDN 回源头自动识别边缘）；生产已配置的值保持不变，它同时决定来源 IP 的解析。
4. 记录基线：用 `tools/gateway-resilience/baseline.sql` 与 `caddy_cuts.py` 采集最近 24 小时的指标，作为上线对照。
5. **边缘识别**：0.4.0 要求 Caddy 按对端 IP 为 Cloudflare 与 ESA 注入 `X-XY-Edge`（§3.1），否则首包前保活不启用，经 Cloudflare 入口的慢首包请求仍会在约 125 秒处被切断（0 字节送达）。0.4.1 起应用按 Cloudflare 的 `CF-Ray`/`CDN-Loop: cloudflare` 与 ESA 的 `CDN-Loop: esa` 自动识别，Caddy 标记改为可选；上线后仍按 §5 抽查一次经 Cloudflare 的慢首包请求。
6. **余额文案**：缺省关键字已含 `insufficient user balance`。单独的 `billing_error` 类型不算余额（Sub2API 系中转把订阅失效、订阅额度、用户额度等计费前置失败都映射成它）；核查中发现新的余额文案时加入 `failure.balance_keywords_extra`，无需发版。
7. 选择低峰时段：从生产数据看，北京时间凌晨（UTC 18:00–22:00）的请求量最低。

8. 含 Anthropic TTL 迁移 286 的版本上线前，按 [TTL 升级与回退](../../../deploy/README.md#anthropic-缓存-ttl-升级与回退) 核对旧策略、存量 Key off/新建 inherit、表锁窗口与回退语义；本次不自动映射旧注入或计费补贴。

## 3. Caddy 配置

### 3.1 边缘标记（D12；0.4.1 起可选）

0.4.1 起应用按 CDN 自带的回源头自动识别 Cloudflare 与 ESA（design §7.1），本节只在接入其他 CDN 或自定义部署时需要；已配置的标记照常生效，并优先于自动识别。

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
- 应用只在对端属于 `SERVER_TRUSTED_PROXIES` 时读取该头。该列表须包含 Caddy 到应用的对端地址（同机部署时为容器网络网关或回环地址），否则该头被忽略（0.4.0 下所有请求按直连处理；0.4.1 起改按 CDN 回源头识别）。

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
		health_uri /ready
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
- 健康检查用 `/ready`（0.4.2 起，检查数据库与 Redis）；0.4.1 及以前只有 `/health`，它不访问依赖、恒为 200。
- 两个实例重叠运行是安全的（0.4.2 起）：新实例启动时保留仍有进程租约的旧实例的并发槽位，旧实例退出后约 75 秒内清走残留。
- **首次从 0.3.0/0.4.1 切到 0.4.2，以及从 0.4.2 回退到这些版本：** 旧版本不写租约，新进程无法判断它是否存活，仍会清掉对方的槽位；旧版本启动时也会清掉 0.4.2 进程的槽位。重叠期内账号与用户的并发可能暂时超出上限，直到那些请求结束。规避：在低峰切换并尽快把流量切到新实例、缩短重叠时间；对并发上限严格的中转，可以改为先停旧实例再起新实例（有短暂中断）。之后的 0.4.2→0.4.x 切换不受影响。
- 切换只调整顺序后执行 `systemctl reload caddy`（平滑重载）。
- 先让新实例通过本机健康检查，再调整顺序。
- `encode` 不匹配 `text/event-stream`（现有配置如此），不得新增，避免 SSE 被缓冲或压缩。

### 3.4 预上线主机核查与上线前配置（2026-10-19）

两份生产就绪审查与对预上线主机的只读核查（地址与路径见仓库外私有清单）发现以下实例层面的问题。它们不在代码里，承接付费流量前按本节逐项处理并复核；生产主机按同样的清单核对一遍。

| 项 | 核查所见 | 上线前要求 |
|---|---|---|
| 客户端真实 IP | 443 端口由 nginx `stream` 按 SNI 分流，四层转发到七层站点时没有带 PROXY 协议，应用记录的客户端 IP 全是 `127.0.0.1` | 让该站点在四层直接分流到一个 `listen ... ssl http2 proxy_protocol` 的七层监听，并设 `set_real_ip_from 127.0.0.1; real_ip_header proxy_protocol;`，`X-Real-IP`/`X-Forwarded-For` 用 `$proxy_protocol_addr`。改后用外部请求核对访问日志里的 `client_ip`。按 IP 的限流、IP 白名单、注册风控与审计都依赖它。实例的 `api_key_acl_trust_forwarded_ip` 为缺省的开启，安全路径按 `CF-Connecting-IP` > `X-Real-IP` > `X-Forwarded-For` 读原始头：七层要覆盖（不是追加）`X-Real-IP`、`X-Forwarded-For`，并清掉客户端自带的 `CF-Connecting-IP`，否则客户端可以伪造地址 |
| 数据库连接池 | 应用 `DATABASE_MAX_OPEN_CONNS=256`、`MAX_IDLE=128`，PostgreSQL `max_connections=100` | 每个进程的上限 ≤（可用连接 − 后台与维护预留）÷ 2，可用连接 = `max_connections` − `superuser_reserved_connections` −（16 起）`reserved_connections` − 其他数据库用户的连接；除以 2 是因为蓝绿重叠时两个进程的连接池同时在用。`max_connections=100`、预留 3 时可用 97，留 15 给后台与维护，取 `MAX_OPEN=40`、`MAX_IDLE=10`。0.4.2 起应用启动时按同一口径检查，超出只记一条 warn（`database connection pool exceeds the server limit`），不阻止启动，查询失败时跳过 |
| 调度快照回源数据库 | `GATEWAY_SCHEDULING_DB_FALLBACK_ENABLED=true`，`MAX_QPS=0`（不限）、`TIMEOUT_SECONDS=0`（不额外收紧） | 给回源一个独立超时 `TIMEOUT_SECONDS=3`（起点，按演练调整）。`MAX_QPS` 按“Redis 不可用”演练的结果确定：建议起点为实例峰值 RPS 的 1.5 倍，演练中观察数据库连接与延迟后再调；不改代码缺省值。预上线主机目前没有流量，先不设，Redis 故障演练后再定 |
| 健康检查 | 容器与反代只用 `/health`（恒 200） | 0.4.2 起反代主动健康检查与外部监控用 `/ready`；容器健康检查保持 `/health` |
| 备份与恢复 | 没有该网关的定时备份，只有升级时的本机快照；`archive_mode=off` | 先定 RPO/RTO；至少每日异机备份数据库（或与生产一样用 pgBackRest 加对象存储做 PITR），Redis 与应用数据目录随同；做一次恢复演练并记录 |
| 告警 | 告警规则开启，但 SMTP 未配置 | 配置邮件（或其他通知渠道）并实际触发一次告警确认送达 |
| 主机容量 | 4 核、3.8 GiB 内存，与多套其他服务共用，swap 已在使用 | 承接付费流量前给应用与数据库留足内存（或迁到独立主机），并为容器设内存上限，避免与其他服务互相挤占 |
| 业务验收 | 1 个用户、0 个 API Key、0 条用量，份额策略 0 行 | 用拟上线制品建测试 Key，按 staging-validation 跑正常请求、失败换号、断开计费、并发满载、数据库与 Redis 故障恢复；份额调度先在代表性分组跑影子模式对照，再逐组启用 |
| 部署专用插件宿主补丁 | 实例运行的是 0.4.1 加部署专用的插件宿主补丁（不在本仓库，带部署专用迁移） | 用户 2026-10-19 决定不再需要：切到官方 0.4.2 镜像，步骤与回退见 §3.5 |

**承接付费流量前的验收清单（P1-A、P1-F）：**

1. 备份：确定 RPO/RTO；配置每日（或更密的）数据库备份并传到另一台主机或对象存储，应用数据目录与 Redis 随同；做一次恢复演练（恢复到临时实例、启动应用、核对行数与登录），记录耗时。
2. 告警：配置 SMTP（或其他通知渠道），在运维页触发一次告警并确认收到。
3. 业务验收：用拟上线镜像建测试 Key，按 staging-validation 跑正常请求（Responses、Chat、Messages、图片）、失败换号、只发开场事件就断开、静默上游换号、并发满载、数据库与 Redis 短暂不可用后恢复（同时观察 `/ready` 与连接数）。
4. 份额调度：在代表性分组先开影子模式，对照原版的落点、缓存读占比、首字与换号，样本覆盖高峰与低谷后再逐组启用 share；回退为改回原版调度。
5. 复核 §3.4 其余各项（真实 IP、连接池、降级参数、健康检查、容量），记录结果。

需要用户提供：SMTP 账号（或通知渠道）、异机备份的存储位置与凭据、测试用的上游 Key（或确认使用实例现有的三个 OpenAI API Key 账号，验收会产生少量上游费用）、可以操作主机的时间窗口。

**用户 2026-10-19 的决定（预上线阶段）：** 授权切换预上线主机（§3.5），生产主机本次不操作。告警与异机备份暂缓：切换时只在主机上做一份带时间戳的本机备份；清单第 1、2 项在承接付费流量前仍要完成。测试 Key 由用户随后在预上线站点添加，第 3、4 项等用户通知后再做。

### 3.5 预上线主机：从“0.4.1 加插件宿主补丁”切到官方 0.4.2（2026-10-19）

用户 2026-10-19 决定：部署专用的插件宿主补丁不再需要，0.4.2 的交付物就是官方镜像。主机上的操作（停用插件、切换镜像、修改配置）随部署授权一起执行；用户 2026-10-19 已授权，镜像用测试服务器验证过的同一个候选镜像（`docker save` 后传到主机再 `load`，不在主机上构建）。

1. **迁移：** 官方迁移执行器只遍历镜像自带的 `*.sql` 文件（`applyMigrationsFS`），库中多出的部署专用迁移记录（`253_plugin_lifecycle.sql`）与它建的表被忽略。不删表、不执行 DROP，保留回退到原镜像的能力。0.4.2 相对 0.4.1 没有新增迁移。
2. **插件：** 切换前必须在管理后台停用并卸载该插件（由用户在后台操作，不直接改数据库）。演练中安装记录留在库里时，官方版本的插件对账每秒失败一次并记一条 warn（`plugin_reconcile_failed`），日志持续增长；网关转发不受影响。插件包目录在观察期结束后再清理。
3. **演练（2026-10-19 已做）：** 从主机只读导出表结构（`pg_dump --schema-only`），加上 `schema_migrations` 与插件表的行（不导出配置、凭据与插件包内容），在本机临时 PostgreSQL 18 上恢复，用 0.4.2 候选启动。结果：库中 325 条迁移记录（含部署专用的一条）被接受、没有新迁移、启动无报错；`/health` 200，`/ready` 200，停掉 Redis 后 `/ready` 503 `{"failed":["redis"]}`；连接池 256 对 `max_connections` 100 时启动打出连接池告警；后台插件页能列出该插件，对账如上每秒失败；网关路由正常应答。演练数据已删除。
   - **更正（2026-10-19）：** 这次演练用的是不嵌入前端的构建。嵌入前端的正式构建在修复 `0c81393f0` 之前，`/ready` 被前端接管、永远返回 200，所以上面“`/ready` 200、停掉 Redis 后 503”的结论对正式镜像不成立。正式镜像以测试服务器场景 RDY-1 与预上线主机切换后的实测为准。
4. **切换方式：** 先停旧容器再启动新容器，不做蓝绿。站点没有流量，这样也避开了从没有进程租约的版本第一次切换时槽位被清的问题（§3.3）。
5. **回退：** 恢复 compose 与配置的备份，用原镜像重建容器，数据库无需改动；nginx 恢复备份后 reload。
6. 切换后按 §3.4 逐项复核（真实 IP、连接池、降级参数、`/ready`、容量），再做业务验收；备份与告警按用户决定暂缓。
7. **执行记录（2026-10-19，用户授权）：**
   - 只读基线：容器、镜像、配置文件哈希，以及主机上按 SNI 分流的全部站点各请求一次的状态码。
   - 卸载插件前先在主机上做了一份带时间戳的本机备份：`pg_dump -Fc`（校验含表数据）、Redis `SAVE` 后的数据目录、compose、`.env`、配置、nginx 两个文件、应用数据目录、旧镜像 ID，`SHA256SUMS` 校验通过。然后用户在后台停用并卸载插件，安装与绑定记录随之删除。
   - 镜像：测试服务器验证过的候选 `b7c68e059`，`docker save`/`load` 传到主机，image ID（`sha256:50037e0d…`）与二进制 SHA-256 都和测试服务器一致；compose 只改镜像那一行，项目、目录与环境文件取自原容器的 compose 标签。
   - 先停旧容器再启动新容器。`.env` 改为 `DATABASE_MAX_OPEN_CONNS=40`、`MAX_IDLE=10`、`GATEWAY_SCHEDULING_DB_FALLBACK_TIMEOUT_SECONDS=3`，`MAX_QPS` 保持 0（Redis 故障演练后再定）。启动日志里实际生效的连接池是 40/10，没有连接池告警。
   - 核对：二进制自报 `0.4.2 … commit: b7c68e059`；迁移记录仍是 325 条；`/health`、`/ready` 在主机内部和经域名访问都返回 JSON；后台页面 200；网关不带 Key 返回 401；0 条 ERROR，没有插件日志。
   - 真实 IP：443 入口把本站的 SNI 直接送到一个带 `proxy_protocol` 的七层监听（与同机另一站点的做法相同），站点用 realip 取地址，覆盖 `X-Real-IP`、`X-Forwarded-For`，清掉 `CF-Connecting-IP`；旧监听保留用于回退。`nginx -t` 之后 reload，全部站点的状态码与基线一致。经域名的外部请求在日志里记为外部地址；带伪造的 `X-Forwarded-For`、`CF-Connecting-IP`、`X-Real-IP` 时，记录的地址不变。
   - 回退：恢复备份中的 compose 与 `.env`，用原镜像重建容器；nginx 恢复两个文件后 reload。数据库无需改动（插件记录在备份里）。
   - **最终候选（全局复审后）：** G1、G2 改了代码，`b7c68e059` 作废。最终候选 `0ad63fab7`（image `sha256:445a27f7…`）在 CI 全绿、测试服务器场景 55/55 与升级演练通过后切换（16:19Z）。切换前先做了一份新的本机备份；卸载插件后 dump 小了约 20 MB，是插件安装行里的插件包被删除，逐表比对确认没有其他差异。compose 只改镜像那一行，`.env` 不变。先停旧容器再启动新容器。核对项与上次相同，全部通过；nginx 没有再改动，外部请求的 `client_ip` 仍是外部地址，伪造的转发头依然无效。回退：用上一份 compose 文件和 `b7c68e059` 镜像重建容器。

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

### 4.2 xy2api 0.4.x（蓝绿）

1. 在生产部署目录下新建 `compose.green.yaml`：
   - 镜像固定为 `ghcr.io/liulixin-lex/xy2api:<版本>@sha256:<正式 digest>`（0.4.2 起建议的上线版本；0.4.2 没有新增迁移）；
   - 容器名取 blue 容器名加 `-green` 后缀，端口 `127.0.0.1:<GREEN_PORT>:8080`（与 blue 错开）；
   - 环境文件、挂载、网络与当前实例相同。
2. 启动 green。0.4.0 启动时执行迁移 272–277，全部只追加（277 只删除 276 种下的两条余额告警规则），0.3.0 实例可以继续服务。
   - 观察迁移日志与耗时；
   - `curl -fsS 127.0.0.1:<GREEN_PORT>/ready` 返回 200（0.4.1 及以前用 `/health`）。
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

上线后另抽查一次经 Cloudflare 入口、首包超过 125 秒的请求，确认收到心跳并完成。以下指标按组持续观察（运维页“网关韧性”统计，进程内保留 1 小时，需定时采集）：

| 指标 | 关注点 |
|---|---|
| `first_output_timeout` | 按组、账号统计（标签为阶段：等响应头或等首输出）；推理档位见日志 `OpenAI first output timeout … effort=`。某账号集中出现说明中转排队，考虑补号或调权重 |
| `budget_exhausted` | 按组；号池组与多中转组优先看，持续出现说明 4 次尝试不够或中转普遍不可用 |
| `post_output_failure` | 0.3.0 不记录输出后失败，没有基线；先观察量级与集中的账号 |
| `undelivered` | 未送达取消次数；应明显少于 0.3.0 下“断开后仍读完并计费”的次数 |
| `relay_keepalive` | 按账号；非零说明该中转用伪保活帧维持静默期，确认没有被判停滞。缺省首字口径（`semantic`）下这类中转的首字时间按第一帧记录（与 0.3.0 一致），首字统计偏乐观 |
| 未送达记录的 `estimated_upstream_cost` 合计 | 按日、按组汇总（运维页未送达列表或 `gateway_undelivered_requests`）；是未送达取消后由平台承担的上游成本，集中在少数 Key 时对照滥用阈值 |
| Caddy“状态 0、0 字节、约 125 秒”的切断率 | 用 `caddy_cuts.py`；应接近 0，否则先查 `resilience.keepalive_committed` 是否出现且带 `edge`（0.4.1 起自动识别；使用 Caddy 标记时查 `X-XY-Edge`） |

口径：用量记录的 `first_token_ms` 与 `duration_ms` 从最后一次（成功的）尝试开始计时，不含此前失败的尝试与换号等待（与 0.3.0 相同）。换过号的请求，客户端实际等待更长；评估首字与切断时结合运维错误记录中各次尝试的时间（`upstream_errors`）。

管理员配置项（不依赖 0.4.0，具体分组与账号见仓库外核查报告与交接记录）：未配渠道定价的白名单模型补定价或移出白名单；实际只剩一个可用账号、且中转自身容量不足的分组补一个账号（调低我方并发无效）；因余额 403 被置为错误状态的账号确认余额后手动恢复；欠费但暂时不用的账号关掉调度即可（否则余额暂停每次到期后，真实请求会再试一次；0.4.12 起暂停逐级加长到基数的 12 倍，见 `docs/GATEWAY_RESILIENCE.md` §4.9）；排队时首字可达数分钟的中转确认容量或降低其在组内的权重。

## 6. 应急处置

**按能力关闭**（“网关韧性”设置卡，即时生效）：

| 开关 | 关闭后的行为 |
|---|---|
| `keepalive.enabled` | 不再提前提交与心跳，回到 0.3.0 |
| `budget.enabled` | 回到原换号次数与首输出口径 |
| `session.enabled` / `last_resort.enabled` | 粘性与选号回到 0.3.0 |
| `balance.enabled` | 余额错误按 0.3.0 处理 |
| `disconnect.enabled` | 断开后按 0.3.0 继续读完并计费 |
| `errors.enabled` | 不加 `Retry-After`，不做负缓存与快速失败；Anthropic 已提交流的错误帧不带 `event: error` 行（0.3.0 格式） |
| `fallback_group.enabled` | 不再转投兜底组 |

0.4.4 新增的开关关闭后回到 0.4.3 的对应行为（详见 [`gateway-resilience-v2-20261021/design.md`](../gateway-resilience-v2-20261021/design.md) §1）：

| 开关 | 关闭后的行为 |
|---|---|
| `stream.prelude_staging` | 兼容转换收到上游 `response.created` 就写开场，之后不再换号（`response.metadata` 也就不再按开场暂存）；0.4.5 起 Gateway 入站经 Anthropic 上游的 Responses 与 Chat 收到 `message_start` 就写开场 |
| `stream.native_prelude_whitelist` | 原生 Responses 的开场只认 `response.created`、`response.in_progress`、`keepalive`；0.4.6 起 `response.metadata`（原生与兼容转换）回到算输出、到达即解除首输出截止 |
| `stream.strict_terminal` | raw Chat 透传的 `error` 后跟 `[DONE]` 当成功处理；0.4.5 起 Gateway 入站经 Anthropic 上游的断流与 EOF 合成为成功，OpenAI Responses 透传不设首输出截止与停滞检测 |
| `failure.decision_v2` | 旧分类：Grok 空闲、raw 429、“服务忙”类文案按容量降载处理；仅流式拒绝只学习，本请求返回 400。分类输入的证据补全保留，个别运维分类可能与 0.4.3 不同 |
| `errors.failed_frame_retry_after` | 合成的 `response.failed` 不带 `error.headers`，正文与 0.4.3 相同（0.4.4 的 RA-1 场景因此恢复 “try again in Ns”）；0.4.5 起也不再采用上游给出的等待时间。这只回退重试提示，不代表其他行为回到 0.4.3 |
| `errors.codex_retry_after_cap_seconds` 设为 0 | Codex 请求的无号 `Retry-After` 与其他客户端一样为 2–30 秒 |
| `capability.stream_only_learning` | 不学习、不过滤；已有记录按到期时间自然过期 |

**关闭总开关的范围：** 韧性能力全部停用，调度与错误处理按 0.3.0 逻辑运行。以下改动不受总开关控制（多为缺陷修复），关闭后仍然生效：

| 改动 | 说明 |
|---|---|
| 全 0 会话 ID 视为缺失 | `metadata.session_id` 全为 0 时改用可缓存内容摘要做粘性键（具名补丁 `xy-placeholder-session-id`） |
| index.html 不返回 304 | 始终返回带本次 CSP nonce 的完整页面（`xy-index-nonce-no-304`） |
| 白名单 404 列出可用模型 | HTTP 入口的 404 文案附本分组白名单前 10 项（`xy-allowlist-404-hint`） |
| 上传中断记 499 | 读请求体时连接中断造成的 400 在运维记录里记为 499 |
| §9.8 续链被拒整轮重放 | Claude 请求分发到 GPT 中转、续链以笼统 4xx 被拒时去掉续链重放一次并关闭该会话续链 |
| 换号与写出的协议正确性（0.4.4） | 只在尚未实际写出、客户端未断开且请求未取消时换号；按实际写出字节判断已输出；取消不记成首输出超时 |
| 排队心跳按入站协议的格式（0.4.4） | 等待槽位与用户消息队列时，Responses、compact、Chat 写注释行，Messages 写 `ping` |
| 排队 ping 之后按协议写终止帧（0.4.4） | Gateway 入站的 Responses 与 Chat 在 HTTP 已提交后遇到计费等错误，写协议终止帧而不是裸 JSON |
| 心跳只提交网关自身的 SSE 头（0.4.5） | 第一个输出之前用心跳提交 200 时，只写网关的流式头，不带这次尝试的上游头；换号后客户端不会拿到失败账号的 `x-request-id` |
| OpenAI Responses 透传的有界暂存（0.4.5） | 第一个输出前的开场暂存上限 8 MiB（超过 64 KiB 写入已解除链接的临时文件），超出时换号 |
| 末位兜底快照的过期清理（0.4.5） | 过期的分组快照在写入新快照前与每分钟的会话标记清理时删除，不再常驻内存 |

管理端只读功能（选号解释中的模型限制提示等）也不受开关影响。

**生效重试上限**（同一请求内各机制同时生效，实际上限取最小值；括号内为缺省值）：

| 机制 | 上限 | 受预算约束 |
|---|---|---|
| 请求预算（`budget`） | 文本请求最多 4 次尝试、首输出前 240 秒；图片与视频不设首输出截止；WebSocket 不挂连接级预算 | — |
| 同号重试 | 每账号 1 次（`budget.same_account_retry_max`），只用于容量降载与池模式配置的状态码；余额、鉴权、长限流、504/524、耗时 ≥60 秒的失败不同号重试；0.4.12 起模型不可用也不同号重试，余额、鉴权、模型不可用在请求预算关闭时同样不重试（随总开关） | 是 |
| 容量降载换号 | 1 次，且只换到其他上游主机 | 是 |
| 原版换号次数 | `gateway.max_account_switches`（10）、Gemini（3） | 预算先到先止 |
| 池模式同号重试 | 账号配置 `pool_mode_retry_count`（3，最多 10） | 预算开启时每账号仍只 1 次 |
| 错误级同号重试 | 部分 Grok、OpenAI 原始 Chat 错误自带上限（多为 1 次） | 是 |
| OpenAI OAuth 429 | 2 分钟窗口内同号最多 3 次、单次等待最多 8 秒；风暴时最多换号 1 次 | 等待受预算截断 |
| Antigravity 原地重试 | 最多 3 次；单号智能重试最多 3 次、单次等待 15 秒、总等待 30 秒 | 是（每次计入尝试） |
| Gemini 原地重试 | 预算开启时 1 次（`budget.gemini_same_account_retries`）；关闭时最多 5 次；0.4.12 起余额、鉴权、模型不可用读完响应体后直接换号，不原地重试 | 是 |
| web_search 补声明重放 | 每请求 1 次（§18.3） | 是 |
| 续链被拒整轮重放 | 每请求 1 次（§9.8） | 否 |
| 失败回合封顶 | 同一会话连续 5 次耗尽失败后终止（`errors.turn_cap`） | — |
| 槽位等待 | 用户与账号槽位 30 秒；粘性等待 120 秒、兜底等待 30 秒（`gateway.scheduling.sticky_session_wait_timeout`、`fallback_wait_timeout`）；文本请求截到剩余首输出前预算 | 是 |

**整体回退：**

- Caddy 上游顺序改回 `<BLUE_PORT> <GREEN_PORT>`（blue 仍在时），或重新启动 0.3.0 镜像后再调整顺序；
- 数据库无需回退，新列与新表对 0.3.0 透明；
- 余额暂停中的账号在暂停到期后回到流量里，0.3.0 下再遇余额失败按原有逻辑处理（402 置错、403 计数）；欠费且暂时不用的账号可先关掉调度；
- 从 0.4.4 回退到 0.4.3：无迁移，直接换回镜像；0.4.3 读取设置时忽略 0.4.4 新增的字段，原有字段不变。**在 0.4.3 上再次保存设置会丢弃新字段**，再升级时它们回到缺省（全部开启），回退期间关掉过的新开关要重新关闭；还开着 0.4.4 管理页面的浏览器要先刷新，否则保存会因未知字段被拒（设计见 `gateway-resilience-v2-20261021/design.md` §2.3）。
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
