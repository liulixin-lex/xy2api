# 网关韧性（Gateway Resilience）

xy2api 0.4.0 引入的一组网关能力，目标是减少用户可感知的失败：上游抖动、单号分组整体不可用、CDN 空闲超时切断、客户端断开后的误计费，以及缺少重试指引的 5xx。

- 方案与决策记录：`openspec/changes/gateway-resilience-20261015/`（proposal、design、tasks、runbook、staging-validation）。
- 全部能力由一份 JSON 设置控制，缺省全部开启；关闭总开关即回到 0.3.0 行为，无需重启。
- 本文面向运维与管理员，说明行为、配置、部署前提、观测与回退。

## 1. 能力一览

| 能力 | 设置段 | 行为摘要 |
|---|---|---|
| 失败分类与冷却 | `failure` | 上游瞬时失败（502/503/超时等）进入短冷却（缺省 30 秒），不再与账号级失败混用；上游 `Retry-After` 封顶 |
| 会话失败改绑 | `session` | 同一会话在同一账号上 10 分钟内连续失败 2 次后改绑，并在 10 分钟内避开该账号 |
| 末位兜底 | `last_resort` | 分组内没有无罚账号时，先转投兜底分组；否则在最高的有软罚账号层中选罚期最短者继续服务，避免单号分组整组不可用 |
| 余额可恢复 | `balance` | 上游余额不足的账号进入 `EXHAUSTED`，按 1/2/5/10/15 分钟复检（带抖动），充值后经 `RECOVERING` 小流量验证恢复为 `OK`。普通用户只看到 503，余额细节只对管理员可见 |
| 请求预算 | `budget` | 文本请求最多尝试 4 次、首输出前总时长 240 秒；图片与视频不设首输出截止；WebSocket 不挂连接级预算。同号重试每号至多一次，账号级失败不重试；容量降载只切换一次且只切到其他上游主机 |
| 首包前保活 | `keepalive` | 仅对经 CDN（`X-XY-Edge` 标记）的流式请求：请求开始 15 秒后仍无输出时提交 SSE 头并每 10 秒发心跳，避免 CDN 空闲超时；官方 OAuth / setup-token 账号不启用 |
| 停滞检测 | `stall` | 已输出后长时间无数据（缺省 120 秒）判定断流并结束请求；图片生成豁免 |
| 断开计费 | `disconnect` | 客户端在任何语义输出前断开：立即取消上游、不写用量、不计费，记入未送达表；部分输出后断开：最多再读 180 秒并按已得用量计费 |
| 错误语义与防风暴 | `errors` | 无号 503 带 `Retry-After`（2–30 秒）；配置错误 503 带 `Retry-After: 60` 与 `x-should-retry: false`；2 秒负缓存；同一 Key 的重复 503 快速返回 |
| 无号兜底分组 | `fallback_group` | 分组无可用账号时由其配置的兜底分组承接（按兜底组倍率计费），主分组稳定 120 秒后切回 |
| 在途预留 | `reservation` | 余额预留按会话缓存读比例估算输入费用；输出 Token 取模型近 7 天 P99（下限 1024） |

## 2. 部署前提

### 2.1 受信代理与边缘标记

首包前保活只对经 CDN 的请求生效，判定依据是反向代理按**对端 IP** 注入的 `X-XY-Edge` 头：

```caddy
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cf remote_ip <Cloudflare 回源网段>
	request_header @xy_edge_cf X-XY-Edge cf
	@xy_edge_esa remote_ip <ESA 回源网段>
	request_header @xy_edge_esa X-XY-Edge esa
}
```

- 先删除客户端自带的同名头，再按 `remote_ip` 注入；不得根据任何请求头判断。
- 应用只在对端属于 `SERVER_TRUSTED_PROXIES` 时读取该头，取值须在 `keepalive.edges` 白名单内（缺省 `cf`、`esa`）。
- **`SERVER_TRUSTED_PROXIES` 必须包含反向代理到应用的对端地址**（同机部署时通常是容器网络网关或回环地址）。未配置或不包含时，所有请求都按直连处理，首包前保活不会生效。
- 内网直连与本机脚本不在 CDN 网段，不会被标记。

### 2.2 反向代理

- 流式响应不得被压缩或缓冲（`encode` 不匹配 `text/event-stream`）。
- API 路径的 `handle_errors` 建议返回 JSON 错误与 `Retry-After`，避免纯文本 5xx（见 runbook §3.2）。

### 2.3 数据库迁移

0.4.0 启动时执行以下迁移，全部只追加，0.3.0 实例可继续读写：

| 迁移 | 内容 |
|---|---|
| `272_add_group_availability_fallback.sql` | `groups.availability_fallback_group_id` |
| `273_add_usage_logs_origin_group.sql` | `usage_logs.origin_group_id`（不加外键与默认值，只改元数据） |
| `274_create_gateway_undelivered_requests.sql` | 未送达请求表（保留 30 天，每 6 小时清理） |
| `275_add_ops_error_logs_disconnect_stage.sql` | 运维错误日志的断开阶段与已送达字节 |

## 3. 客户端可见的行为

| 场景 | 响应 |
|---|---|
| 无可用账号 | 503，`Retry-After` 为 2–30 秒（带抖动），消息附 “Please try again in Ns.” |
| 配置导致的无号（分组仅限 Claude Code、渠道定价限制） | 503，`Retry-After: 60`，`x-should-retry: false` |
| 模型不存在 | 保持原响应，不加重试头 |
| 余额被在途请求占用 | 429，`rate_limit_error` / `concurrent_balance_reserved`，`Retry-After` 约 5 秒 |
| 上游余额不足 | 普通用户看到 503；管理员在账号列表看到余额状态 |
| 首包前心跳已提交后失败 | 不再改写状态码，改为协议终止帧：Responses 为 `response.failed`（`rate_limit_exceeded` 或 `server_error`）；Chat 为 `data: {"error":{…,"status":N}}` 后跟 `[DONE]`；Anthropic 为 `event: error` |
| 心跳帧 | Responses / Chat 为 SSE 注释行 `:`；Anthropic 为 `event: ping` |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，并带 `X-XY-Upstream-Status: 502/504`。Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉 JSON 错误、`Retry-After` 和请求 ID；503 原样透传。直连请求保持原状态码 |
| 换号耗尽的 429/5xx | 带 `Retry-After`：上游给出的值按 2–30 秒封顶，没有时取分组最早恢复时间 |
| 同一会话连续第 5 次耗尽失败（无号、换号耗尽、流内失败） | 终止应答：不带 `Retry-After`，`x-should-retry: false`，`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持）。Codex 对带 `Retry-After` 的 5xx 没有次数上限，一个回合最多约 30 次请求；封顶后不超过 5 次。会话真正成功一次或两次失败相隔超过 120 秒后重新计数 |
| 中转站在 `response.created` 前插入的 `SSE-Keep-Alive` 空 delta 帧 | 直接丢弃，不转发、不算首输出（openai-python 遇到会报错，网关也会因此提前放弃换号） |

## 4. 配置

### 4.1 入口

- 管理端“系统设置 → 网关”中的“网关韧性”卡：总开关、各能力开关、常用数值与完整 JSON。
- 接口：`GET/PUT /api/v1/admin/settings/gateway-resilience`。`PUT` 提交完整文档，服务端校验全部范围，未通过不生效；响应同时返回当前值与缺省值。
- 设置变更在保存的实例上即时生效，其他实例每 10 秒刷新一次；读取失败或存量文档非法时保留最后一次有效值。

### 4.2 常用字段（缺省值与范围）

| 字段 | 缺省 | 范围 |
|---|---|---|
| `budget.max_attempts` | 4 | 1–10 |
| `budget.pre_output_budget_seconds` | 240 | 60–290 |
| `budget.first_output_timeout_seconds` | default 180 / high 180 / xhigh 200 / max 220（按生产 7 天首字分布校准，超出比例约 0.1%–0.2%） | 5–预算 |
| `budget.anthropic_first_event_timeout_seconds` | 90 | 5–预算 |
| `budget.same_account_retry_max` | 1 | 0–5 |
| `keepalive.delay_seconds` / `interval_seconds` | 15 / 10 | 5–60 / 5–30 |
| `keepalive.edges` | `["cf","esa"]` | 小写字母、数字或 `-` |
| `stall.text_stall_seconds` | 120 | 30–280 |
| `disconnect.partial_drain_max_seconds` | 180 | 1–3600 |
| `disconnect.abuse_guard.threshold_per_hour` | 30 | 1–100000 |
| `errors.no_account_retry_after` | 2–30 | 1 ≤ min ≤ max ≤ 300 |
| `errors.negative_cache_seconds` | 2 | 1–60 |
| `errors.fast_fail` | 30 秒内 10 次 | 窗口 1–3600，阈值 2–10000 |
| `errors.turn_cap` | 连续 5 次，间隔 120 秒内 | 次数 2–30，窗口 10–3600 |
| `fallback_group.return_stable_seconds` | 120 | 1–86400 |
| `fallback_group.binding_ttl_seconds` | 600 | 1–86400 |
| `balance.recheck_schedule_seconds` | `[60,120,300,600,900]` | 1–16 项，每项 10–86400 |

完整字段与校验见 `backend/internal/resilience/settings.go`。

### 4.3 无号兜底分组

- 在分组编辑页选择“无号兜底分组”：须为同平台、启用、非订阅的其他分组；不能指向自身，不能成环。
- 触发条件：主分组没有任何无罚账号，且兜底组有无罚账号。判定只在请求入口进行一次，换号耗尽不跨组。
- 进入兜底后按 API Key 记忆（Redis，TTL `binding_ttl_seconds`）；主分组持续健康 `return_stable_seconds` 后切回，抖动的主分组不会来回切换。
- 用量记录：`usage_logs.group_id` 为实际服务的兜底组，`origin_group_id` 为原分组；用量列表显示“兜底：原分组 → 实际分组”，可用渠道页显示各组的兜底分组与倍率。

### 4.4 断开计费与滥用保护

- 未送达（任何语义字节前断开）：上游立即取消，不写用量，记入未送达表。
- 同一 Key 每小时未送达超过阈值（缺省 30）时视为滥用：不再取消上游，按上游成本计费，用量 `billing_mode` 为 `undelivered_abuse`。
- “语义字节”扣除了心跳、流内保活与图片填充，因此仅收到心跳的断开仍算未送达。

## 5. 观测

### 5.1 管理端

- 运维页“网关韧性”面板：当前实例最近 5 分钟至 1 小时的计数、账号余额状态分布、未送达请求列表。
- 账号列表：余额状态非 `OK` 时显示徽标，悬停可见下次复检时间与证据；“已充值”按钮立即复检。
- 接口：
  - `GET /api/v1/admin/resilience/stats?window=15m`（窗口最长 1 小时，计数为单实例内存口径）；
  - `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=`（时间为 RFC3339）；
  - `POST /api/v1/admin/accounts/:id/balance/recheck`。

### 5.2 计数项

| 计数 | 含义 |
|---|---|
| `keepalive_committed` | 首包前心跳提交了响应头（细分为协议） |
| `first_output_timeout` | 首输出超时触发换号 |
| `budget_exhausted` | 请求预算耗尽 |
| `last_resort` | 末位兜底选中软罚账号 |
| `fallback_group` / `fallback_return` | 转投兜底分组 / 切回主分组 |
| `session_rebind` | 会话失败改绑 |
| `undelivered` / `cdn_cut` | 未送达；按耗时推断的 CDN 切断（90 秒、125 秒、300 秒） |
| `fast_fail` | 同一 Key 的重复 503 快速返回 |
| `turn_cap` | 会话连续耗尽失败到达上限，改为终止应答 |
| `abuse_guard` | 未送达滥用保护计费 |
| `stall` | 停滞断流 |

### 5.3 日志事件

结构化日志的消息固定为事件名（前缀 `resilience.`），细节在字段中。

warn 级，进入运维页“系统日志”（`ops_system_logs`）：

- `balance_transition`、`balance_unrecovered`、`balance_probe_auth_failure`；
- `session_rebind`、`last_resort`；
- `fallback_group`、`fallback_return`；
- `budget_exhausted`、`capacity_switch_exhausted`；
- `stall`、`fast_fail`、`turn_cap`、`abuse_guard`。

info 级，只写应用标准输出（量大，明细已有计数或未送达表）：

- `keepalive_committed`（含 `group_id`、`protocol`、`edge`）；
- `undelivered`（含阶段、耗时、推断的 CDN 切断与 `edge`）；
- `transient_cooldown`、`transient_lifted`、`undelivered_cleanup`。

`*_failed` 结尾的事件表示存储或查询失败，功能会降级为原有行为。

## 6. 回退

- 单项关闭：在“网关韧性”卡中关闭对应能力，即时生效。各能力关闭后的行为见 runbook §6。
- 整体关闭：关闭总开关，网关按 0.3.0 逻辑运行。
- 版本回退：迁移只追加，0.3.0 可直接运行在 0.4.0 的库上，无需回退数据库。

## 7. 已知限制

- 统计计数保存在进程内存，多实例部署时按实例分别统计，重启后清零。
- WebSocket 不挂连接级预算，建连首回合的换号仍按 `max_account_switches`；Gemini compat 服务未接入首事件守卫，由停滞检测兜底。
- 断开阶段粒度为选号、等待响应头、首输出前、输出中四段。
- 失败回合计数在进程内，多实例时各自计数；快速失败的原样返回发生在选号之前，不参与计数。
- 未送达表固定保留 30 天。
- 滥用保护只在上游返回用量时按成本计费。
- 兜底分组仅支持标准（余额）分组；进入兜底的记忆按 API Key 而非会话。
