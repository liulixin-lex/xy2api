# 只读 MCP 部署与操作

本功能默认关闭。开发完成不等于已部署；生产迁移、权限收敛、服务重启和启用都应在独立发布窗口执行。权限和逐工具来源见 [设计合同](../docs/READ_ONLY_MCP_DESIGN.md)，实际验收证据见 [OpenSpec tasks](../openspec/changes/readonly-mcp-20261105/tasks.md)。

## 1. 部署前提

- 使用本版本正常数据库迁移，包括 `290_readonly_mcp.sql` 和 `291_readonly_mcp_control_limits.sql`。它只新增 OAuth 与 MCP 访问审计控制面表，不修改业务日志。迁移文件与 checksum 必须一同发布，不能修改历史迁移。
- 同一 HTTPS 站点提供站内登录、授权页面、OAuth 和 `/mcp`；`mcp.public_url` 为固定公开 origin，例如 `https://gateway.example.com`，不含路径、query 或 fragment。多副本必须共用同一 issuer、独立 MCP secret 与数据库。
- 外部客户端需支持远程 Streamable HTTP 和 OAuth Authorization Code + PKCE S256，使用公开客户端 `token_endpoint_auth_method=none`。只接受固定 resource `${public_url}/mcp`，不能把站内 JWT、管理员全局 Key 或网关 API Key 当 MCP token。
- 容器内部或私网的应用地址不能替代公开 issuer。不得根据客户端提供的 Host/转发头自动生成 issuer。

## 2. 独立 PostgreSQL 只读账号

以下步骤由数据库管理员针对应用数据库执行。先核对实际 writer 与其他可信调用角色；不要将脚本交给 MCP 客户端执行。

```sql
CREATE ROLE xy2api_mcp_reader LOGIN NOINHERIT NOSUPERUSER NOCREATEDB
  NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE xy2api_mcp_reader SET default_transaction_read_only = on;
ALTER ROLE xy2api_mcp_reader SET statement_timeout = '2s';
ALTER ROLE xy2api_mcp_reader SET lock_timeout = '100ms';
```

通过密码管理器或 psql `\password xy2api_mcp_reader` 设置独立随机密码；使用 TLS/受控数据库网络、连接限制与单独 DSN。该账号不能成为任何其他角色成员，不能拥有数据库/schema/表/分区，不能获得业务写权限或可写序列权限。

以管理员连接到**应用数据库**后运行 [固定安全列授权脚本](mcp-reader-grants.sql)。它只对明确的 25 张业务表授予已审查列的 SELECT，以及 public schema USAGE；没有整库/未来表默认 SELECT，没有密码哈希、账号 credentials、API Key 原文或日志正文列。`upstream_errors` 的必要 JSONB 由服务端固定 SQL 提取安全标量，绝不加载完整正文到应用响应。

```sql
\i deploy/mcp-reader-grants.sql
```

还必须检查 `PUBLIC` 权限。只从 reader 撤权不能覆盖其通过 PUBLIC 获得的权限。现有迁移和扩展可能创建默认对 PUBLIC 开放执行的函数；按以下顺序执行 [函数权限收敛脚本](mcp-reader-function-privileges.sql)：

1. 阅读脚本实际筛选条件，核实受影响的可调用函数及全部可信调用角色。脚本不触碰只能由触发器调用的函数。
2. 对 writer 之外仍需执行的可信角色先显式授予相同函数 EXECUTE；不要假定数据库只有一个业务角色。
3. 在同一 psql 会话设置真实应用 writer 角色名，再执行脚本。脚本先保留 writer 的 EXECUTE，再收窄 PUBLIC/reader 权限，并在事务中完成。

```sql
SELECT set_config('mcp_setup.writer_role', 'ACTUAL_APPLICATION_WRITER', false);
\i deploy/mcp-reader-function-privileges.sql
```

这是显式部署操作，不会由应用启动或迁移自动执行。数据库若仍通过 PUBLIC 授予 schema/database CREATE 或其他写能力，应由 DBA 在保留可信业务角色权限后收敛；不能为通过预检而放宽 MCP 校验。后续新增函数或授权变更需重新核查。

启用时启动预检会拒绝同 writer 身份、特权角色、角色成员、对象所有权、列/表/序列写权限、schema/database CREATE，以及未经收敛的可执行写函数。查询本身还使用独立 Reader 连接、`READ ONLY` 事务、固定 search_path 和超时。任何错误都不会回退到 writer 查询。

## 3. 配置与反向代理

将以下配置放入实际应用配置，秘密只进入部署 secret，不进入仓库、日志或客户端：

```yaml
mcp:
  enabled: false
  public_url: "https://gateway.example.com"
  oauth_hmac_secret: ""
  reader_dsn: ""
  allowed_origins: []
```

对应环境变量为 `MCP_ENABLED`、`MCP_PUBLIC_URL`、`MCP_OAUTH_HMAC_SECRET`、`MCP_READER_DSN`、`MCP_ALLOWED_ORIGINS`。MCP secret 是至少 32 个随机字节的标准 Base64，可用 `openssl rand -base64 32` 生成；与站内 JWT secret 分开保存。Reader DSN 只使用上节账号。容器部署须显式把所需变量传入容器，不能只在宿主机设置后假定已传递。

公开 origin 自带同源许可。只有确需浏览器直接跨源连接时才添加明确的客户端 origin，不能添加通配符；授权同意和撤销入口仍要求显式 Origin 并匹配配置许可列表（默认仅站点同源）。

代理需要转发：

- `/mcp` 的 POST/GET/DELETE；`/mcp/authorize` 是站内页面。
- `/.well-known/oauth-protected-resource/mcp`、`/.well-known/oauth-authorization-server`。
- `/oauth/mcp/register`、`/authorize`、`/token`、`/revoke`。
- `/api/v1/mcp/consent/preview`、`/consent`、`/connections` 及本人连接删除。

保留原公开 Host、Authorization、Origin、Accept、Content-Type 和 MCP 协议头。不要缓存这些路由、token 响应或身份相关工具清单；尊重 `Cache-Control: private, no-store`。不要记录 Authorization、token POST body、完整 OAuth callback query。认证页保持 `X-Frame-Options: DENY`。代理不能把这些路径转成 HTML 登录页、JS challenge 或重写到 `/v1`。

## 4. 连接与诊断

启用并通过启动预检后，用户在“个人资料 → 只读 MCP 连接”复制服务地址，在客户端添加，登录本地账号并查看授权页面。页面展示未经平台验证的客户端名称、注册回调地址、resource、当前账号及可选查询工具。只有用户明确批准的工具可以使用，之后新增工具需要新授权。

- 根管理员：可授权全部 29 个安全诊断工具；仍不会导出凭据、原始正文或任意 SQL。
- 普通管理员：22 个管理工具按实时后台模块、AnyOf、目标和字段规则过滤，再与已批准工具求交；无独立分组 ACL 的现行账号不会凭 MCP 自动产生分组隔离。
- 普通用户：最多 7 个 `xy2api.me.*` 工具，只查本人；后台模式和用户错误记录开关继续有效。

建议诊断顺序：短时间窗 `requests.search` → 用数值 `error_row_id` 查详情/尝试 → 按 `account_ids` 查被动健康 → 查看分组、渠道和独立来源趋势。原请求 ID、客户端 ID、billing ID 不保证同命名空间；不要去掉前缀后拼成“完整 trace”。

每次结果都应检查 `source`、`window`、`coverage`、`partial_reasons`、`retention_status`、`aggregation_watermark` 和 `state_scope`。`retention_status=unknown` 表示无法证明保留期完整，不等于已确认截断。当前快照使用 `not_applicable`。余额为已存快照，其凭据指纹未校验时会明确标记，不能据此承诺当前实时余额。

分组用量只返回已闭合自然日的费用桶、时区与水位。请求/错误/用量工具保持来源独立：输出后失败也可产生 usage 行，不能把 usage 当成功请求或把 usage 与错误行直接相加为唯一请求总量。渠道监控 v2 是被动真实流量聚合，MCP 查询不触发探测。

明细默认 1 小时、最大 24 小时，系统事件最大 6 小时；具体工具以其 schema 描述为准。页默认 20、最多 100，并发快照最多 100 个明确账号 ID。查询超时或结果过大时先缩小窗口和 limit。游标只能用于同账号、同 grant、同工具、同过滤条件的后续页。

## 5. 撤销、维护与回退

本人连接页面可撤销单个连接。撤销、账号禁用、token_version 变化及权限减少都会在后续请求在线复核。Refresh 令牌只可用一次，旧 refresh 重放会撤销该授权族；客户端应避免并发刷新同一个 refresh token。

OAuth 与访问审计是控制面写入，不属于业务诊断写工具。审计不存原始参数、token、请求/响应/提示词正文。检查 `mcp.audit_health` 日志中的 drop/failure 计数；审计队列有界，写入失败不能伪装成完整审计。

控制面启用时每分钟运行独立维护，每次总期限 2 秒：清理已消费/过期 pending 至多 500 条、过期 session 至多 1,000 条、自然到期且无任何 session 子行的 grant 至多 500 条、到期且无任何 grant/pending 子行的 client 至多 500 条，以及 90 天以前的访问审计至多 1,000 条。未同意 client 初始有效期 24 小时，同意后延长至 90 天；pending/code 5 分钟、access 10 分钟、grant/refresh 最长 30 天。已消费 refresh 的重放证据保留到其自然到期，不能按“已使用”立即删除。维护失败以 `mcp.control_cleanup_failed` 记录，不输出原始数据库错误；应用关闭时取消并等待 worker。业务 usage、ops、Redis 的保留政策沿用已有系统，MCP 不延长或清理业务数据。

紧急回退先将 `mcp.enabled=false` 并按常规方式重启；关闭状态不注册 MCP 路由、不建立额外 Reader 连接、不启动 MCP worker。需要禁止旧授权恢复时，在获准的控制面维护中撤销有效 grant。不要删除 OAuth 迁移表、回滚历史 checksum 或清空诊断业务数据。保留撤销证据到相关凭证过期后再清理。

## 6. 验收边界

本地测试使用一次性 PostgreSQL、合成身份和无敏感内容的 fixture；不代表已在生产配置、生产数据规模或任何指定商业 AI 客户端上验收。官方 Inspector、官方 Go SDK 与前端组件/浏览器测试的实际版本和通过情况只以 tasks 已记录的证据为准。生产启用前还需在目标部署核对公开 Host/证书、代理、最小授权、数据源开关和查询计划；不得为了兼容跳过 TLS、resource 或权限校验。
