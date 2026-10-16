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
5. **边缘标记是首包前保活的前提**：Caddy 必须按对端 IP 为 Cloudflare 与 ESA 注入 `X-XY-Edge`（§3.1）。缺了它首包前保活不启用，经 Cloudflare 入口的慢首包请求仍会在约 125 秒处被切断（0 字节送达）。
6. **逐个确认 OpenAI 中转的余额接口语义**（tasks 12.6）：对生产中返回过 403“Insufficient account balance”的中转（核查报告中列出的账号），确认 `/v1/usage` 或 new-api 计费接口在余额不足与充足时的返回，确保余额状态机复查能判定恢复。
7. **余额文案**：缺省关键字已含 `insufficient user balance`。单独的 `billing_error` 类型不算余额（Sub2API 系中转把订阅失效、订阅额度、用户额度等计费前置失败都映射成它）；核查中发现新的余额文案时加入 `failure.balance_keywords_extra`，无需发版。
8. 选择低峰时段：从生产数据看，北京时间凌晨（UTC 18:00–22:00）的请求量最低。

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
2. 启动 green。0.4.0 启动时执行迁移 272–276，全部只追加，0.3.0 实例可以继续服务。
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

上线后另抽查一次经 Cloudflare 入口、首包超过 125 秒的请求，确认收到心跳并完成。以下指标按组持续观察（运维页“网关韧性”统计，进程内保留 1 小时，需定时采集）：

| 指标 | 关注点 |
|---|---|
| `first_output_timeout` | 按组、账号统计（标签为阶段：等响应头或等首输出）；推理档位见日志 `OpenAI first output timeout … effort=`。某账号集中出现说明中转排队，考虑补号或调权重 |
| `budget_exhausted` | 按组；号池组与多中转组优先看，持续出现说明 4 次尝试不够或中转普遍不可用 |
| `post_output_failure` | 0.3.0 不记录输出后失败，没有基线；先观察量级与集中的账号 |
| `undelivered` | 未送达取消次数；应明显少于 0.3.0 下“断开后仍读完并计费”的次数 |
| `relay_keepalive` | 按账号；非零说明该中转用伪保活帧维持静默期，确认没有被判停滞 |
| Caddy“状态 0、0 字节、约 125 秒”的切断率 | 用 `caddy_cuts.py`；应接近 0，否则先查 `X-XY-Edge` |

管理员配置项（不依赖 0.4.0，具体分组与账号见仓库外核查报告与交接记录）：未配渠道定价的白名单模型补定价或移出白名单；实际只剩一个可用账号、且中转自身容量不足的分组补一个账号（调低我方并发无效）；因余额 403 被置为错误状态的账号确认余额后手动恢复；排队时首字可达数分钟的中转确认容量或降低其在组内的权重。

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

**关闭总开关的范围：** 韧性能力全部停用，调度与错误处理按 0.3.0 逻辑运行。以下改动不受总开关控制（多为缺陷修复），关闭后仍然生效：

| 改动 | 说明 |
|---|---|
| 全 0 会话 ID 视为缺失 | `metadata.session_id` 全为 0 时改用可缓存内容摘要做粘性键（具名补丁 `xy-placeholder-session-id`） |
| index.html 不返回 304 | 始终返回带本次 CSP nonce 的完整页面（`xy-index-nonce-no-304`） |
| 白名单 404 列出可用模型 | HTTP 入口的 404 文案附本分组白名单前 10 项（`xy-allowlist-404-hint`） |
| 上传中断记 499 | 读请求体时连接中断造成的 400 在运维记录里记为 499 |
| §9.8 续链被拒整轮重放 | Claude 请求分发到 GPT 中转、续链以笼统 4xx 被拒时去掉续链重放一次并关闭该会话续链 |

管理端只读功能（选号解释中的模型限制提示等）也不受开关影响。

**生效重试上限**（同一请求内各机制同时生效，实际上限取最小值；括号内为缺省值）：

| 机制 | 上限 | 受预算约束 |
|---|---|---|
| 请求预算（`budget`） | 文本请求最多 4 次尝试、首输出前 240 秒；图片与视频不设首输出截止；WebSocket 不挂连接级预算 | — |
| 同号重试 | 每账号 1 次（`budget.same_account_retry_max`），只用于容量降载与池模式配置的状态码；余额、鉴权、长限流、504/524、耗时 ≥60 秒的失败不同号重试 | 是 |
| 容量降载换号 | 1 次，且只换到其他上游主机 | 是 |
| 原版换号次数 | `gateway.max_account_switches`（10）、Gemini（3） | 预算先到先止 |
| 池模式同号重试 | 账号配置 `pool_mode_retry_count`（3，最多 10） | 预算开启时每账号仍只 1 次 |
| 错误级同号重试 | 部分 Grok、OpenAI 原始 Chat 错误自带上限（多为 1 次） | 是 |
| OpenAI OAuth 429 | 2 分钟窗口内同号最多 3 次、单次等待最多 8 秒；风暴时最多换号 1 次 | 等待受预算截断 |
| Antigravity 原地重试 | 最多 3 次；单号智能重试最多 3 次、单次等待 15 秒、总等待 30 秒 | 是（每次计入尝试） |
| Gemini 原地重试 | 预算开启时 1 次（`budget.gemini_same_account_retries`）；关闭时最多 5 次 | 是 |
| web_search 补声明重放 | 每请求 1 次（§18.3） | 是 |
| 续链被拒整轮重放 | 每请求 1 次（§9.8） | 否 |
| 失败回合封顶 | 同一会话连续 5 次耗尽失败后终止（`errors.turn_cap`） | — |
| 槽位等待 | 用户与账号槽位 30 秒；粘性等待 120 秒、兜底等待 30 秒（`gateway.scheduling.sticky_session_wait_timeout`、`fallback_wait_timeout`）；文本请求截到剩余首输出前预算 | 是 |

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
