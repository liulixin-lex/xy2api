# 只读 MCP

XY2API 内置一个只读的 MCP（Model Context Protocol）服务，让外部 AI 客户端在你授权后查询网关的用量、错误与上游尝试、账号健康、分组、渠道和运维数据，用于排障和分析。本文面向部署 XY2API 的运维与管理员（启用、数据库授权、反向代理）以及要把 MCP 客户端连上网关的用户。

## 概览

- **只读**：只提供 29 个固定的查询工具，不提供任何写操作、导出、清理、探测、账号测试、OAuth 刷新、重平衡或 SQL 执行入口。查询不会向上游供应商发请求，也不会触发渠道监控检测。
- **按人授权**：每个连接都绑定一个具体的 XY2API 用户。用户在授权页上勾选允许的工具；每次调用都按该用户**当前**的角色和后台权限重新检查，撤权在下一次请求生效。
- **不返回敏感正文**：即使是超级管理员，也读不到凭据、API Key 原文、密码、完整提示词、错误响应正文、上游自由文本、系统日志正文、URL、邮箱和客户端 IP。
- **默认关闭**：`mcp.enabled` 默认为 `false`。关闭时不注册任何 MCP 路由，不建立额外的数据库连接，个人资料页也不显示 MCP 连接卡片。

传输方式是 Streamable HTTP（无状态模式），认证方式是 OAuth 2.1 授权码 + PKCE（S256）。服务端点为 `${mcp.public_url}/mcp`。

## 工具清单

“普通管理员所需权限”指后台权限表中的模块“查看”权限；列出多个模块时满足任一即可。超级管理员可以授权全部 29 个工具；普通用户只能使用 7 个 `xy2api.me.*` 工具。

### 管理诊断工具（22 个）

| 工具 | 用途 | 普通管理员所需权限 |
|---|---|---|
| `xy2api.requests.search` | 按请求 ID、客户端请求 ID 或时间窗与平台、账号、分组、模型、用户、API Key 过滤，查找已结算用量行和最终错误行 | `ops`；`include_recovered_upstream=true` 时另需 `ops` 访问上游健康来源 |
| `xy2api.requests.get_detail` | 按数值 `error_row_id` 读取一条错误记录的结构化元数据 | `ops` |
| `xy2api.requests.get_attempts` | 读取某条错误记录的逐次上游尝试（分页） | `ops` 或 `usage`；该行属于已恢复的上游记录时另需 `ops` |
| `xy2api.requests.search_ingress_rejections` | 入口拒绝的分钟级聚合 | `ops` |
| `xy2api.accounts.search` | 按名称、平台、状态、分组查找上游账号摘要 | `accounts`、`channels`、`groups`、`usage` 任一 |
| `xy2api.accounts.get_health` | 最多 100 个指定账号的近期实测（首字、错误、缓存与各分组质量状态）和已存的[上游余额](upstream-balance.md)快照 | `accounts` |
| `xy2api.groups.search` | 分组 ID、名称、平台、状态与分类 | `groups`、`usage`、`users` 任一 |
| `xy2api.groups.get_usage_summary` | 指定分组已闭合自然日的费用汇总 | `groups` |
| `xy2api.channels.search` | 渠道元数据和分组绑定 | `channels` |
| `xy2api.channels.get_metrics` | 渠道监控 v2 基于真实流量的被动聚合（次数、延迟、错误分类、水位） | `channels` |
| `xy2api.ops.get_error_breakdown` | 错误分类聚合与独立的用量行计数 | `ops` |
| `xy2api.ops.get_trends` | 按时间桶的用量行数与最终错误数趋势 | `ops` |
| `xy2api.ops.get_alerts` | 告警事件元数据（规则、级别、状态） | `ops` |
| `xy2api.ops.get_system_events` | 系统事件元数据（级别、组件、关联 ID），不含正文 | `ops` |
| `xy2api.ops.get_realtime_snapshot` | 平台/分组级的近期来源行速率与已结算 Token 聚合 | `ops`（需开启实时监控） |
| `xy2api.ops.get_concurrency_snapshot` | 1–100 个指定账号的当前在途、等待与分组并发 | `ops`（需开启实时监控） |
| `xy2api.routing.get_quality_state` | 本进程的[质量状态](group-quality-gate.md)快照（分组 × 账号 × 模型的正常 / 冷却与三个数）；`include_policies=true` 同时读取策略 | `accounts` |
| `xy2api.users.search` | 普通用户的安全摘要（不含邮箱，不支持按 API Key 文本搜索） | `users`、`groups`、`subscriptions` 任一 |
| `xy2api.users.get_usage_summary` | 指定普通用户的用量聚合 | `users`，且目标必须是普通用户 |
| `xy2api.users.get_subscriptions` | 指定普通用户的订阅与限额 | `users` 或 `subscriptions`，且目标必须是普通用户 |
| `xy2api.audit.search` | 后台操作审计元数据（操作者、操作、目标、状态码） | `audit_logs` |
| `xy2api.security.search_prompt_audit` | 提示词审计事件元数据，不含提示词和扫描证据 | `security_audit` |

### 本人数据工具（7 个）

| 工具 | 用途 |
|---|---|
| `xy2api.me.get_profile` | 本人最小资料：ID、用户名、角色、状态、余额、并发 |
| `xy2api.me.search_usage` | 本人的用量记录（请求模型、计费 Token、费用、延迟、来源分组） |
| `xy2api.me.get_usage_detail` | 按 `usage_row_id` 读取本人一条用量 |
| `xy2api.me.get_error_history` | 本人的请求错误元数据 |
| `xy2api.me.get_error_detail` | 按 `error_row_id` 读取本人一条错误 |
| `xy2api.me.get_subscription_status` | 本人订阅状态、到期时间、分组限额与周期用量 |
| `xy2api.me.get_quota_status` | 本人的平台额度、用量与窗口 |

本人工具的归属在 SQL 里固定为当前用户，参数不能指定其他用户；不存在和不属于本人的记录都返回 `NOT_FOUND`。超级管理员和普通管理员也可以用这些工具查询自己的数据。

## 权限模型

每次 `tools/list` 和每次 `tools/call` 都会重新执行以下检查，不缓存跨请求的授权结果：

1. 访问令牌有效、未撤销、属于当前 `mcp.public_url` 与资源 `${public_url}/mcp`。
2. 用户存在、处于启用状态，且授权之后没有修改过密码或邮箱（修改后旧连接全部失效，需要重新授权）。
3. 工具在该连接获批的工具列表中。
4. 按当前角色判定：超级管理员可用全部管理工具；普通管理员按其当前后台权限表逐个来源检查（见上表），规则与后台接口相同，包括目标用户检查和脱敏；普通用户只能用本人工具。权限读取失败时一律拒绝。
5. 数据源开关：

| 条件 | 影响的工具 | 结果 |
|---|---|---|
| 未完成部署与运营合规确认 | 全部管理工具 | 拒绝 |
| 运维监控关闭（配置 `ops.enabled` 或设置 `ops_monitoring_enabled`） | `requests.*`、`ops.*` | `SOURCE_DISABLED` |
| 实时监控关闭（设置 `ops_realtime_monitoring_enabled`） | `ops.get_realtime_snapshot`、`ops.get_concurrency_snapshot` | `SOURCE_DISABLED` |
| 后台模式开启（`backend_mode_enabled`） | 普通用户的本人工具 | 拒绝（管理员不受影响） |
| 未允许用户查看错误请求（`allow_user_view_error_requests`） | `me.get_error_history`、`me.get_error_detail` | `SOURCE_DISABLED` |

工具列表只显示当前可用的工具；数据源关闭的工具仍会列出，调用时返回 `SOURCE_DISABLED`。

**授权范围不会自动扩大**：连接只包含用户当初勾选的工具。之后版本新增的工具、或用户后来获得的新权限，都需要重新授权才能进入该连接。反过来，权限被收回后，即使工具仍在连接里也会被拒绝。

**没有按分组或账号的隔离**：普通管理员权限是模块级的。有某模块查看权限的普通管理员，通过 MCP 能看到该模块下全部数据，与在后台看到的范围相同。详见[管理员与权限](admin-permissions.md)。

## 连接客户端

客户端需要支持：远程 MCP（Streamable HTTP）、OAuth 2.1 授权码流程与 PKCE S256、动态客户端注册（公开客户端，`token_endpoint_auth_method=none`）。

1. 在 XY2API 用户界面打开“个人资料 → 只读 MCP 连接”，复制“MCP 服务地址”（形如 `https://gateway.example.com/mcp`）。
2. 在客户端中添加这个远程 MCP 服务地址。客户端首次请求会收到 `401` 和指向 `/.well-known/oauth-protected-resource/mcp` 的 `WWW-Authenticate` 头，随后自动完成服务发现和客户端注册，并在浏览器中打开授权页。
3. 在浏览器中登录 XY2API 账号。授权页显示：客户端名称（由客户端自报，未经平台验证）、授权后返回地址、MCP 服务地址、当前账号和可选的查询工具。只勾选你需要的工具，然后点击“允许所选查询”。
4. 浏览器回到客户端，客户端用授权码换取访问令牌，之后即可调用工具。

已有连接列在同一张卡片中，显示授权时间、到期时间和允许的工具，可以随时“撤销连接”。撤销在下一次请求生效。

可以用下面的请求确认服务已启用并能被外部访问：

```bash
curl -s https://gateway.example.com/.well-known/oauth-protected-resource/mcp
```

```json
{
  "resource": "https://gateway.example.com/mcp",
  "authorization_servers": ["https://gateway.example.com"],
  "scopes_supported": ["xy2api.requests.search", "..."],
  "bearer_methods_supported": ["header"]
}
```

授权服务器元数据在 `/.well-known/oauth-authorization-server`，其中列出 `/oauth/mcp/authorize`、`/oauth/mcp/token`、`/oauth/mcp/register`、`/oauth/mcp/revoke` 四个端点。

**不能**把站内登录 JWT、全局 admin API Key 或网关 API Key（`sk-...`）当作 MCP 令牌使用。

### 令牌与连接的有效期

| 对象 | 有效期 |
|---|---|
| 授权请求与授权码 | 5 分钟，授权码只能使用一次 |
| 访问令牌 | 10 分钟 |
| 刷新令牌与授权（连接） | 最长 30 天 |
| 已注册但从未获得授权的客户端 | 1 天；首次授权后延长到 90 天 |

刷新令牌每次使用后轮换，旧刷新令牌被重放会撤销整个连接。客户端不要并发刷新同一个刷新令牌。

## 部署与配置

### 1. 数据库迁移

MCP 的 OAuth 与访问审计表由迁移 `290_readonly_mcp.sql` 与 `291_readonly_mcp_control_limits.sql` 创建，随常规数据库迁移执行。它们只新增控制面表，不修改业务数据。

### 2. 创建只读数据库账号

MCP 查询使用独立的 PostgreSQL 登录，**不能**与应用的主账号相同。由数据库管理员连接应用数据库执行：

```sql
CREATE ROLE xy2api_mcp_reader LOGIN NOINHERIT NOSUPERUSER NOCREATEDB
  NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE xy2api_mcp_reader SET default_transaction_read_only = on;
ALTER ROLE xy2api_mcp_reader SET statement_timeout = '2s';
ALTER ROLE xy2api_mcp_reader SET lock_timeout = '100ms';
```

用 `\password xy2api_mcp_reader` 或密码管理器设置独立的随机密码。这个账号不能成为任何其他角色的成员，不能拥有数据库、schema、表或分区，不能有任何写权限。

### 3. 授予列级只读权限

以管理员身份连接**应用数据库**，执行 [`deploy/mcp-reader-grants.sql`](../../deploy/mcp-reader-grants.sql)：

```sql
\i deploy/mcp-reader-grants.sql
```

脚本只对 25 张业务表中选定的安全列授予 `SELECT`，以及 `public` schema 的 `USAGE`。它不授予整库或未来表的默认权限，也不包含密码哈希、账号凭据、API Key 原文和日志正文列。

### 4. 收敛函数执行权限

只从 reader 撤权无法覆盖它通过 `PUBLIC` 获得的权限。迁移和扩展创建的函数默认对 `PUBLIC` 开放执行，启动预检会因此拒绝启用。执行 [`deploy/mcp-reader-function-privileges.sql`](../../deploy/mcp-reader-function-privileges.sql) 前：

1. 阅读脚本的筛选条件，确认会受影响的函数（非触发器函数、`SECURITY DEFINER` 或非 `IMMUTABLE`，且 reader 当前可执行）。
2. 如果除应用主账号外还有其他可信角色要调用这些函数，先给它们显式授予 `EXECUTE`。
3. 在同一个 psql 会话中设置真实的应用主账号角色名，再执行脚本。脚本先给该角色保留 `EXECUTE`，再撤销 `PUBLIC` 和 reader 的执行权，全部在一个事务中完成。

```sql
SELECT set_config('mcp_setup.writer_role', 'ACTUAL_APPLICATION_WRITER', false);
\i deploy/mcp-reader-function-privileges.sql
```

这两个脚本都不会由应用启动或迁移自动执行。如果数据库仍通过 `PUBLIC` 授予 schema 或数据库的 `CREATE` 权限（例如 PostgreSQL 15 之前 `public` schema 的默认设置），需要 DBA 在保留应用主账号权限后收回。

### 5. 应用配置

| 配置键 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `mcp.enabled` | `MCP_ENABLED` | `false` | 总开关 |
| `mcp.public_url` | `MCP_PUBLIC_URL` | 空 | 固定的公开 HTTPS origin，例如 `https://gateway.example.com`，不含路径、查询参数或片段。作为 OAuth issuer，资源地址为 `${public_url}/mcp` |
| `mcp.oauth_hmac_secret` | `MCP_OAUTH_HMAC_SECRET` | 空 | 至少 32 个随机字节的标准 Base64，可用 `openssl rand -base64 32` 生成。与站内 JWT 密钥分开保存 |
| `mcp.reader_dsn` | `MCP_READER_DSN` | 空 | 第 2 步只读账号的 PostgreSQL DSN |
| `mcp.allowed_origins` | `MCP_ALLOWED_ORIGINS` | `[]` | 额外允许的浏览器 Origin，必须是精确的 origin，不能用通配符。公开 origin 本身已默认允许 |

```yaml
mcp:
  enabled: true
  public_url: "https://gateway.example.com"
  oauth_hmac_secret: ""   # 从部署密钥注入，不要写进仓库
  reader_dsn: ""          # 从部署密钥注入
  allowed_origins: []
```

多副本部署时，所有实例必须使用相同的 `public_url`、`oauth_hmac_secret` 和数据库。容器部署要把这些环境变量显式传入容器。其余配置方式见[配置文件与环境变量](../deployment/configuration.md)，示例见 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)。

### 6. 反向代理

站内登录、授权页、OAuth 端点和 `/mcp` 必须在同一个 HTTPS 站点上。代理需要转发以下路径：

- `/mcp` 的 `POST`、`GET`、`DELETE`；
- `/mcp/authorize`（前端授权页）；
- `/.well-known/oauth-protected-resource/mcp`、`/.well-known/oauth-authorization-server`；
- `/oauth/mcp/register`、`/oauth/mcp/authorize`、`/oauth/mcp/token`、`/oauth/mcp/revoke`；
- `/api/v1/mcp/consent/preview`、`/api/v1/mcp/consent`、`/api/v1/mcp/connections` 及 `DELETE /api/v1/mcp/connections/:id`。

要求：

- 保留原始 `Host`。服务端只接受与 `mcp.public_url` 主机一致的 `Host`，否则返回 `403`；issuer 不会根据客户端提供的 Host 或转发头生成。
- 保留 `Authorization`、`Origin`、`Accept`、`Content-Type` 和 MCP 协议头。
- 不缓存这些路由。服务端对它们返回 `Cache-Control: private, no-store`，代理应遵守。
- 不在访问日志中记录 `Authorization`、token 请求体和完整的 OAuth 回调查询串。
- 不要把这些路径改写成 HTML 登录页、JS 质询页或重写到 `/v1`。

通用代理设置见[反向代理、CDN 与边缘安全](../deployment/reverse-proxy.md)。

### 7. 重启与启动预检

修改配置后重启服务。启用时，服务在启动阶段检查只读账号：它必须与主账号不同、不是特权角色、不是任何角色的成员、不拥有任何对象，没有表、列、序列的写权限，没有 schema 或数据库的 `CREATE` 权限，也不能执行未经收敛的可写函数。任一项不满足，服务拒绝启动并输出 `MCP reader login has write privileges or cannot be verified` 或 `MCP reader must use a separate unprivileged login`。查询时还会使用独立连接、`READ ONLY` 事务、固定 `search_path`，并设置 `statement_timeout = 2s`、`lock_timeout = 100ms`。任何错误都不会回退到主账号查询。

其他启动错误：`mcp.public_url must be an HTTPS origin`、`mcp.oauth_hmac_secret must be base64 encoded with at least 32 random bytes`、`MCP requires a dedicated read-only DSN`。

## 查询结果与限制

每个工具的返回都包含 `schema_version`、`query_id`、`source`、`generated_at`、`coverage`、`partial_reasons`、`retention_status`、`retention_limited` 和 `items`，按需带 `window`、`filters_applied`、`aggregation_watermark`、`state_scope`、`next_cursor`。

- `retention_status=unknown` 表示无法证明数据保留期完整，不等于已确认被截断；当前状态快照为 `not_applicable`。
- 用量行与错误行是独立来源。输出后失败的请求可能同时有用量行和错误行，不能把两者相加当作唯一请求数，也不能把有用量行当作客户端成功。
- 请求 ID、客户端请求 ID 和计费请求 ID 不保证在同一命名空间，不要去掉前缀后拼成“完整链路”。`requests.*` 返回的每一行都带 `source`、`evidence` 和 `relation_confidence`，说明它来自哪张表、哪一行；不同来源的行不会被合并。
- 余额是已存的快照，不代表实时余额。分组用量只有已闭合自然日的费用，不含请求数和 Token 数。渠道指标只来自真实流量，查询不触发检测。

| 限制 | 值 |
|---|---|
| 明细类工具时间窗 | 默认 1 小时，最大 24 小时 |
| 聚合类工具（错误分布、趋势、分组用量、渠道指标、用户用量）| 默认 24 小时，最大 24 小时 |
| 系统事件 | 默认 1 小时，最大 6 小时 |
| 实时快照 | 默认 5 分钟，最大 1 小时 |
| 每页条数 | 默认 20，最大 100 |
| 指定账号 ID | 最多 100 个 |
| 单次调用期限 | 8 秒；数据库语句 2 秒 |
| 单个响应 | 不超过 256 KiB，超出返回 `RESULT_TOO_LARGE` |
| 单实例并发查询 | 16 |

游标只能用于同一用户、同一连接、同一工具、同一过滤条件的后续页。

工具内的错误以固定代码返回，不透传数据库或上游原文：`FORBIDDEN`、`INVALID_ARGUMENT`、`NOT_FOUND`、`SOURCE_DISABLED`、`SOURCE_UNAVAILABLE`、`QUERY_TIMEOUT`、`RATE_LIMITED`、`RESULT_TOO_LARGE`。遇到超时或结果过大时，先缩小时间窗和 `limit`。

建议的排障顺序：用短时间窗调用 `requests.search` → 用返回的数值 `error_row_id` 调用 `requests.get_detail` 和 `requests.get_attempts` → 用 `account_ids` 调用 `accounts.get_health` → 再看分组、渠道和趋势。

## 观测与维护

- **访问审计**：每次工具调用写入 `mcp_access_audit`，记录用户、客户端、连接、工具、结果、错误代码、耗时和行数，不记录令牌、参数原文或任何业务正文。写入走有界异步队列，丢弃和失败计数通过日志事件 `mcp.audit_health` 输出（字段 `dropped_total`、`failed_total`、`queued`）。
- **控制面清理**：启用时每分钟清理一次已消费或过期的授权请求、过期会话、到期且无会话的连接、到期且无引用的客户端，以及 90 天前的访问审计。清理失败记录为 `mcp.control_cleanup_failed`。MCP 不清理也不延长业务数据（用量、运维、Redis）的保留期。
- **限流**：令牌端点、撤销端点、客户端注册与授权端点按来源 IP 限流；`/mcp` 同时按来源 IP 和按连接限流。超限返回 `429`，并发满时返回 `503` 与 `Retry-After`。客户端注册在全局最多 1,000 个、每个来源 IP 最多 100 个有效客户端。

## 安全边界

- MCP 客户端背后的 AI 服务可能在你的组织之外。通过权限检查只说明这个 XY2API 用户本来就能看到这些数据，不代表同意把数据发送给第三方。授权前确认你信任该客户端。
- 工具返回的名称、标签等是不可信数据，可能包含提示词注入内容；工具描述已声明它们不是指令。MCP 不提供任何可被诱导执行的写工具。
- 工具的 `readOnlyHint` 注解只供客户端参考，安全边界在服务端。
- 访问令牌只能放在 `Authorization` 头里；URL 中带 `access_token` 或 `token` 参数的请求直接返回 `400`。

## 停用与回滚

把 `mcp.enabled` 设为 `false` 并重启即可停用。停用后不注册 MCP 路由、不建立只读连接、不运行清理任务。需要防止旧授权在重新启用后恢复时，先在“只读 MCP 连接”中撤销，或在维护窗口中撤销有效连接。不要删除 MCP 迁移创建的表或改动迁移校验和。

升级后如果新版本的迁移创建或替换了可执行函数，需要重新执行第 4 步的函数权限脚本；如果 `deploy/mcp-reader-grants.sql` 有变化，也需要重新执行，否则启动预检或查询会失败。
