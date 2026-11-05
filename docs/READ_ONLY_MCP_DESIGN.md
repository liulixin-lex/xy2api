# XY2API 只读 MCP 设计方案

状态：2026-11-05 已按用户授权实现 29 个只读工具、OAuth 授权与本人连接管理；本地验证和两路代码审查的证据见 OpenSpec tasks。后端由 gpt-6.1-sol / xhigh 执行，交付审查与安全红队由 gpt-6-astra / xhigh 分两路完成；已通过完整远端CI并随v0.5.4发布（PR #199）；生产部署与目标环境验收尚未执行。方案分支 `feat/readonly-mcp-20261105`，基线 `381687250909140e8bb253f9b0c3d16d794a366b`。

## 结论

建议在现有 Go 服务中增加原生、只读 MCP，让外部 AI 按请求查询用量、失败尝试、上游账号、分组、渠道和运维分析。MCP 应调用项目现有 service/query 层并返回有来源、有范围、有脱敏的结构化数据；不得成为 SQL 代理或通用管理 API 转发器。

权限的基础规则是：根管理员按现有超级管理员权限；普通管理员每次调用都按其当前账号权限重新检查模块 view、现有跨模块 AnyOf、目标检查、特殊 flag、运维配置及脱敏；普通用户只可读取用户端明确允许的本人数据。工具清单可以过滤，但服务端每次调用都要重新授权。MCP 只提供查询能力，不新增业务写入工具。

首版工作流：用请求 ID、客户端请求 ID、时间和过滤条件定位记录；对照最终客户端结果与逐次上游尝试；再查同账号、模型、分组、渠道的邻近健康与聚合趋势。现有数据不足以把所有层可靠连接成单一完整 trace，关联结果必须显示证据和置信度。

OpenSpec 已登记实现、测试与审查证据。OAuth 的事务语义已通过本地 PostgreSQL 并发验证，官方 Inspector 已完成真实授权和工具调用；商业 Host、生产登录与代理链路、生产规模查询仍需目标环境验收。发布、部署和开放公网访问是后续阶段。

## 项目轮廓

XY2API 是自托管的 AI API 网关和管理后台。后端使用 Go 1.27.3、Gin、PostgreSQL、Redis 与 Ent；前端使用 Vue 3、TypeScript、Vite、Pinia。用户请求经过 API Key 鉴权、额度与分组检查、组合路由、渠道和账号选择、粘性／并发／韧性控制、上游转发和重试，最后记录用量与运维事件。上游包含 Anthropic、OpenAI 兼容、Gemini、Antigravity、Grok、Kimi、GLM、DeepSeek 等。

这里并非一张请求表就代表系统全部事实：已结算用量、客户端错误、被换号救回的上游失败、调度器当前状态、队列日志和异步聚合分别存于 PostgreSQL、Redis、进程内状态和外部供应商。它们并非对每个请求一一对应，也有不同的保留期、采集开关和精度。

目前管理权限目录登记了 538 条管理路由：407 条按后台模块检查，128 条仅超级管理员可用，3 条属于管理员通用入口；236 条使用 GET。这里的路由数不是 MCP 工具数。只按 HTTP Method 过滤不安全：个别 GET 会主动探测上游，一些 POST 则只是预览或 explain，只能逐个按服务方法审查。

主要代码边界：

| 模块 | 现有责任 | 对 MCP 的影响 |
|---|---|---|
| 管理路由、认证 | internal/server/routes/admin.go、middleware/admin_auth.go | 用户 JWT 按角色进后台；全局 Admin API Key 会绑定首个管理员并视为超级管理员，不能直接把全局 Key 交给外部 AI。 |
| 逐人管理权限 | internal/pkg/operatoraccess/、OperatorAccessService、docs/ADMIN_PERMISSIONS.md | 既有模块 view/manage、特殊 flag、AnyOf、target、field 与 redaction 是普通管理员的权限基准。 |
| 用量、账单 | usage_logs、UsageService、dashboard aggregation、rollup | 明细、统计和预聚合口径不同；响应必须给出来源、窗口、样本与水位。 |
| 运维观测 | ops_error_logs、ops_system_logs、ingress reject、ops_metrics、Resilience | 请求错误、逐次上游尝试、通用系统事件、分钟聚合、当前状态不是同一种记录。 |
| 当前运行态 | Redis、进程内 resilience/quality/share 状态 | 可能过期、重启丢失或仅本机有效，必须声明 state scope、生成时间和 instance。 |
| 用户 API | routes/user.go 与用户服务 | 普通用户不具备后台权限；本人数据要通过严格绑定 owner ID 的独立只读 facade。 |

当前仓库有 336 个迁移 SQL 文件（按目录计，含迁移历史／变体），不应把文件数误当成现行表数。准确 schema 以迁移器、Ent 生成代码和实际数据库版本为准。

## 权限方案

### 角色与有效权限

| 身份 | MCP 允许范围 |
|---|---|
| 根管理员，role=admin | 访问列入 MCP allowlist 的业务诊断数据。超级管理员不因此可以读取数据库凭据、密码哈希、上游 token、Admin API Key、Redis secret 或云存储凭据。 |
| 普通管理员，role=operator | 按本人当前 admin_operator_permissions 数据逐工具授权；只读 MCP 只认 view，不将 manage 当作工具写能力。必须遵循 module、明确 AnyOf、特殊 flag、target、运维开关与现有 redaction。catalog 未登记普通管理员路由一律拒绝。 |
| 普通用户，role=user | 仅通过独立用户 facade 访问本人已有用户端允许的使用记录、错误、订阅进度、配额及无凭据 profile 字段。不能访问管理面工具、他人数据、账号、上游、渠道、全站运维、管理审计。 |

普通管理员权限目前是“模块级＋部分目标规则”，不是按管理员单独配置的账号／分组 ACL。因此不能承诺现有 operator 只看指定分组。若未来要求按 MCP 限缩到特定账号或分组，须增加显式 resource-scope grant 和 SQL 行级过滤，不可以只在 tool 描述里说明。

有效权限计算：

当前用户 active 与 role ∩ MCP 固定只读工具白名单 ∩ 当前后台权限／用户归属 ∩ 该用户明确批准的 MCP scope 上限 ∩ 数据源是否存在且启用。

工具清单按授权过滤，工具执行再次检查。普通管理员被撤权、改角色、停用或删除后，下一个请求就失去对应工具。管理员权限服务或用户服务故障时 fail closed；不得沿用过期权限快照。MCP scope 是额外缩权，不授权用户当前角色本无的内容。后续新增后台权限不应静默扩大旧 MCP token 的能力，需明确重新同意。

### 后台权限映射

普通管理员工具按现有 catalog 的真实 Entry 定义检查；跨模块读取须跟随当前路由的 AnyOf，不能用“一项权限可看整个模块”取代该规则。

| 数据域 | 必需后台权限 | 边界 |
|---|---|---|
| 仪表盘 | dashboard:view 及对应 catalog AnyOf | 只开放明确列入的聚合工具。 |
| 使用记录、统计、用量关联 | usage:view；部分路径有 AnyOf | 不返回 API Key 明文；data.export flag 不授权创建或下载导出。 |
| 错误、请求日志、上游尝试、实时运维 | 按精确 catalog entry 或明确列出的 source ACL | 不能笼统要求 ops:view 或 usage:view：对一个 tool 同时访问多个不同路由的数据源时，逐源组合权限；只有 MCP 工具明确复现单一 catalog route 时，原样复现其 AnyOf 的 OR 语义。运维监控关闭时返回数据源停用；复用 ops_upstream_url 等敏感字段规则。 |
| 账号健康和上游余额 | accounts:view；账号列表等还可按特定 AnyOf 开放 | 投影只有账号 ID、名称、平台、类型、状态、必要分组及指标；不返回 credentials 或 extra。余额只读取被动 Redis snapshot。 |
| 分组与组用量 | groups:view 及对应 AnyOf | 不从查一组扩展到未授权用户明细。 |
| 渠道与监控统计 | channels:view | 只读已采集快照；监控地址／错误按现有脱敏规则，不触发监控 run。 |
| 份额调度、韧性解释 | share_scheduling:view；个别路由支持 accounts:view/ops:view | 不属于 v1；只有未来逐条复核无 Redis/进程态副作用并补齐 ACL/source 表后再考虑。 |
| 质量门槛状态 | catalog 当前为 accounts:view | 仅返回当前状态、策略摘要、样本量和更新时间；不启动评估或写策略。 |
| 指定用户、订阅、余额历史 | users:view、subscriptions:view 与原路由 AnyOf | 复用现有目标检查；普通管理员只能访问 role=user 目标，不能访问自己或其它管理员。 |
| 后台安全审计、提示词审计 | security_audit:view | v1 仅读提示词审计元数据；完整 prompt 属于未来单独设计范围。 |
| 后台操作审计 | audit_logs:view | 保留 audit_body 原有脱敏；MCP 自己的访问审计单独写。 |
| 其余支付、客服、兑换／优惠、返利等数据 | 各自 module:view | 非排障首版；逐项评估个人信息、完整兑换码、交易和消息图片，不使用通用列表工具批量开放。 |
| 设置、备份、升级、凭据导入导出 | catalog 的 KindSuperOnly 与 secret hard-deny | 不纳入普通 operator；多数根管理员工具也不应读取凭据、下载备份或运行升级。 |

特别注意：GET /accounts/:id/usage 可主动通过上游查询。MCP 只能调用被动 snapshot 或后台已持久化记录，不能直接套用该 handler 的 active 路径。类似 GET 的渠道探测、账号测试、OAuth refresh、IQ check 等功能一律不提供。

每个 MCP tool 都有一份静态 ACL map，逐条列出它实际读取的 admin catalog key、模块／等级／flags／AnyOf／target／fields／redaction 以及输出字段。MCP handler 直接调用 service 不会经过 Gin `AdminPermission`，不能假定 operator 的响应 writer 或 target/check-field hooks 会自动执行。多来源新工具默认要求所有来源均获授权；若工具逐字复现某单一路由，则沿用该 entry 的 OR AnyOf 语义。例：`GET /ops/requests` 为 `ops:view`；`GET /ops/errors` 为 `ops:view OR usage:view`；`GET /ops/errors/:id` 为 `ops:view`；`GET /ops/request-errors/:id` 又有对应 AnyOf。需从 catalog 生成／验证该映射，权限目录变更时 fail closed。

Entry.Fields 也属于授权契约：如 `GET /users` 的 `user_search` 规则依赖 `HideAPIKeySearch(ctx)` 与 `operatoraccess.Scope` context，而非通用 CheckFields。MCP reader context 必须携带本次请求的新鲜 operator scope，保留 `ServiceEnforcedFields`、API Key 查询限制和所有目标规则，不得只检查 module level。

### 普通用户自有工具

| Tool | 认证后的硬性约束 |
|---|---|
| xy2api.me.get_profile | 来源：`GET /user/profile` 对应 UserService.GetProfile；只投影用户端公开资料，不返回密码、认证 challenge、身份元数据或 token。 |
| xy2api.me.search_usage | 来源：`GET /usage` 对应 UsageService.ListWithFilters；查询始终固定 `ul.user_id = 当前认证 user id`，Key ID 先验证属主；维持用户 DTO 和 origin group 语义。 |
| xy2api.me.get_usage_detail | 来源：`GET /usage/:id` 对应 UsageService.GetByID；先按 actor user_id 做属主谓词，每条用量必须属于本人，不调用 admin DTO。 |
| xy2api.me.get_error_history | 来源：`GET /usage/errors` 对应 ListUserErrorRequests；固定 actor user_id，v1 仅输出结构化错误元数据，不返回错误正文。 |
| xy2api.me.get_error_detail | 来源：`GET /usage/errors/:id` 对应 GetUserErrorRequestDetail；固定 actor user_id，错误行不存在或不属于本人统一 NotFound，不返回自由正文。 |
| xy2api.me.get_subscription_status | 来源：`GET /subscriptions`、`/active`、`/summary`、`/progress`；调用 ListUserSubscriptions/ListActiveUserSubscriptions 后只查本人订阅与进度，进度计算不向上游发请求。 |
| xy2api.me.get_quota_status | 来源：`GET /user/platform-quotas` 对应 userPlatformQuotaRepo.ListByUser；固定 actor user_id，使用响应时过期归零，不枚举平台账号列表。 |

用户 tool 参数不得用另一个 user_id 覆盖认证 owner。所有 me.* 保留 `BackendModeUserGuard` 的角色语义：后台模式开启时禁止 role=user 的自助读取，管理员本人仍可使用其自有工具。错误工具还要求 `IsUserErrorViewAllowed` 为 true，关闭时不查询。MCP 通过返回 `(value,error)` 的窄配置读取确认开关；不能复用 DB 出错后返回 false 的 `IsBackendModeEnabled` helper 来判断已关闭。依赖读取失败即拒绝。不得以“登录者是管理员”将普通用户会话升级到 admin 工具。

## 数据来源、关联与指标

| 数据 | 权威来源 | 语义与限制 |
|---|---|---|
| 已结算用量 | usage_logs | 记录计费主体、账号、组、模型、token 桶、费用和耗时；输出后失败也可能结算，不能凭此证明客户端成功，也不是每个入站请求的完整全集。request_id 有 client:、local:、generated: 和任务类前缀，按 request_id + api_key_id 防重。 |
| 账单恢复 | usage_billing_intents、billing dedup/archive | 反映原扣费命令与结算状态，不是请求追踪表；不得把 JSON command 直接返回。只允许额外构造经过审查的脱敏对账读视图。 |
| 错误与上游尝试 | ops_error_logs 与 upstream_errors JSONB | 一条错误可有多次换号尝试；也会有最终成功请求的 2xx recovered upstream-health 行。含地址、响应正文、邮箱、IP 等潜在敏感内容。 |
| 通用运行日志 | ops_system_logs | access row 是否落库受 persist_access_logs、异步队列 drop/写入失败和保留期影响。搜不到日志不代表没有请求。 |
| 入口拒绝 | 有界内存聚合与异步持久化 | 分钟级、按 route/reason/protocol/IP 等聚合，不是逐请求日志；有容量和 drop/overflow 边界。 |
| 未投递请求 | gateway_undelivered_requests | 记录尚未输出内容时客户端已断开的专门数据，不保证每行在 ops_error_logs 有对应行。 |
| 运维时序 | ops_system_metrics、ops_metrics_*、watermark | 可能是原始、聚合或延迟水位；结果标记 query mode、时段和 watermark。 |
| 用量时序 | usage_dashboard_hourly/daily/users、usage_group_daily_rollups | 适合趋势而非逐请求事实；未回填与迟到数据需要标记覆盖度。 |
| 渠道观测 | channel monitor histories 与 channel_monitor_v2 聚合／事件 | 首版 channel metrics 只读 v2 被动采集的真实用户流量聚合，不读取探测流量，也不因 MCP 查询而运行探测。 |
| 当前调度解释 | Redis、进程内 resilience、quality gate 与 share 状态 | 多有 TTL、重启／实例边界，不是对历史选号的审计快照。解释结果注明 instance、state_scope、generated_at、stale。 |
| 并发 | Redis ZSET slots/leases | 禁止调用含 ZREMRANGEBYSCORE 修剪逻辑的 GetAccountConcurrencyBatch。先审核并发 peek 的具体 Redis 命令，只读实现限 ID 查询；不能“读取”时做清理。 |
| 上游余额 | Redis UpstreamBalanceStore | ReadBalances 只读快照与采集状态；后台采集可能对上游 API 和“最近使用”产生影响，因此 MCP 不强制刷新。 |
| 提示词审计 | prompt_audit_events.full_prompt | 182 号迁移明确添加完整原文列；这是独立高敏数据。不是所有用量／请求都存原始 body。 |
| 管理面审计 | audit_logs | 异步批写有 drop 风险；历史审计不是可证明完整的全业务事件溯源。 |

`AdminPermission` 的 JSON writer/redaction 不会自动保护 MCP service 直接返回的自由文本。`OpsErrorLogDetail` 含 error_body、upstream_error_message/detail/upstream_errors；`ops_system_logs` 直接含自由 message 和 JSON extra，catalog 没有 system-log redaction。常规 MCP 工具默认不返回 error body、upstream 原文、system message/extra、header、URL/userinfo、query parameter、客户端 IP 与用户邮箱，只返回 machine classification、reason、stage/status、时间／延迟、授权的账号/组 ID 和关系证据。若产品确定确有需求读取诊断正文，必须定义独立 `diagnostic_text:read` MCP grant、字段 allowlist、脱敏/长度上限、明确用户授权和持久读取审计；URL 替换不能让自由文本自动安全。

### 正确关联请求

- 优先精确查 client_request_id 和运维／系统 request_id，再用 usage request_id、api_key_id、账号／用户、时间窗口、平台与模型做相关候选。所有来源保存独立主键和来源名。
- billing request_id 可能被写成 client: 或 local: 前缀；不得任意移除冒号前缀后宣称唯一。强制任务前缀不得改写。
- usage_logs 是结算记录，客户端结果保持 unknown；输出后失败可以同时产生 usage 行和错误行。上游换号失败也可保存在 ops_error_logs upstream_errors 内。各来源独立保留，不能将行数相加作为唯一客户端请求数，不能凭 usage 行推断最终成功。
- 关联精度返回 exact / corroborated / candidate / partial / conflict。多个证据冲突不能 silent merge；不同 user_id 的近似记录不能链接。
- 单次调用记录关联规则与找到的记录数、窗口、缺失源、数据保留限制。低样本给不足样本结论，不伪造错误率。

第一版不新增 trace ID。受限生产抽样已证明 billing/client 与 ops ID 不能直接当同一命名空间；样本不足以代表全部历史覆盖率。MCP 首版保留 partial/candidate 和证据。如果实际需求仍无法可靠定位，再单独评估统一 gateway_trace_id 的增量记录变更；本方案不引入影子模式。

### 指标语义

缓存读取占比是持久计费桶 cache_read_tokens / (input_tokens + cache_creation_tokens + cache_read_tokens)，不是上游真实缓存命中；强制缓存计费会改变持久桶。范围比例先加总三个互斥桶后再相除。单次平均 TPS 是 output_tokens ×1000 / duration_ms，含等待首 token，不是纯解码速率。依据 docs/USAGE_METRICS.md。

first_token_ms、end_to_end_first_output_ms、attempt_first_output_ms 与门槛失败尝试口径起点不同。不得直接比较。失败分析还需拆开客户端最终错误、被救回的上游失败、上游健康事件和业务限制。ops monitoring 未启用、错误被 passthrough 排除、日志 retention 已清理，都需要在响应 coverage 中表示。

| 问题 | 默认查询策略 | 约束 |
|---|---|---|
| 精确请求定位 | requests search/detail/attempts，优先 ID，再按短窗口 | 默认一小时；page 最多 100；不扫描无限历史。 |
| 账号／组失败分析 | usage 与 ops 保持独立来源，返回对应行数、窗口和样本量 | raw 最长 24 小时；不以两者相加构造唯一客户端请求分母，长期曲线只用已验证 aggregate。 |
| 系统事件 | ops_system_logs | 默认一小时、页最多 100、最长 6 小时；只按固定元数据过滤，不搜索自由正文，受 DB deadline 限制。 |
| 全站／平台趋势 | dashboard/ops aggregation | 首版原始查询默认 1 小时、最大 24 小时；日桶遵循既有 rollup 水位，说明精度而非声称 exact raw rows。 |
| 用户历史 | 用户自有服务，或管理员授予的目标查询 | 先验证主体 ACL 再查询，不因 user_id 为参数就有权限。 |

所有过滤器 parameterized、排序列 allowlist、服务端固定最大窗口和总行数；设 statement/lock timeout。不允许 MCP query fallback 无界 raw scan。

## MCP 工具目录与 transport

同一 Go 进程内构造专用 MCPServer，复用 Wire 注入后的只读 query facade。首版向外部 AI 提供 HTTPS Streamable HTTP；本地 stdio 未实现，如将来增加仍须认证为明确 XY2API user，不能因运行在服务器上默认拿 root。

使用官方 Go SDK `github.com/modelcontextprotocol/go-sdk v1.8.0`。官方 release 标明此版支持 MCP 2026-07-28、向后兼容旧协议、修复 transport 资源上限并允许收窄协议版本；其 Go 最低版本低于本仓库 1.27.3。使用 SDK 的 `StreamableHTTPHandler` 与 `RequireBearerToken`，采用 stateless 模式，不自写 JSON-RPC。SDK 不实现项目的 Authorization Server、用户登录与 consent；这些必须由本项目完成。当前 Streamable HTTP 与旧版 host session/SSE 行为不同，须用实际客户端验证。

首版只暴露静态 tools；不启用 server sampling、任意资源读取、订阅、动态 prompt、客户端写回或长任务。每个 tool 都声明 JSON Schema、明确过滤器、默认/最大时间窗、limit、cursor 和 source/coverage 返回。`requests.search` 默认查询 `/ops/requests`；`include_recovered_upstream=true` 时才增加 `/ops/upstream-errors` source，并逐独立 Entry 检查 `ops:view`，结果标明 provider-health 来源，逐行区分已恢复、最终失败和 unknown。

| Tool | 查询目标 | 普通管理员 scope |
|---|---|---|
| xy2api.requests.search | 时间窗、client/request ID 与平台/账号/group/model 固定条件；`include_recovered_upstream` 默认 false | 静态 ACL map 中精确标明 `/ops/requests` 与可选 `/ops/upstream-errors` 两个 source；页≤100，并可返回后续详情所需的数值型 `error_row_id`。 |
| xy2api.requests.get_detail | 数值型 `error_row_id`（对应既有错误记录主键） | 仅按静态 ACL map 的 GET /ops/errors/:id 读取；request/client ID 先经 search 定位，不能混用为此参数。 |
| xy2api.requests.get_attempts | 数值型 `error_row_id`（对应既有错误记录主键） | 只读该记录的上游 attempts；不返回或推断最终客户端 outcome。 |
| xy2api.requests.search_ingress_rejections | 时间、reason、route family、protocol | ops:view；只返回分钟聚合。 |
| xy2api.accounts.search/get_health | 账号摘要、被动余额和近期健康 | accounts:view 或 catalog AnyOf；无 extra/credentials、无 probe。 |
| xy2api.groups.search/get_usage_summary | 组摘要、已有组用量 rollup | search 沿用 GET /groups 的 `groups:view OR usage:view OR users:view`；用量摘要使用 GET /groups/usage-summary 的 `groups:view`。不计算实时调度候选或服务账号结果。 |
| xy2api.channels.search/get_metrics | 渠道元数据和已收集监控聚合 | channels:view；禁止测试和立即检测。 |
| xy2api.ops.get_error_breakdown/get_trends | 失败类别、趋势、水位及窗口 | ops:view；按 n/N/口径显示。 |
| xy2api.ops.get_alerts/get_system_events | 告警与系统日志只读筛选 | ops:view；禁止 resolve、silence、cleanup。 |
| xy2api.ops.get_realtime_snapshot | 平台/分组级来源独立行速率、已结算 token 吞吐 | ops:view；不把不同 source 行相加为客户端 QPS，不输出账号逐项数据。 |
| xy2api.ops.get_concurrency_snapshot | 有界账号/分组实时在途与等待数 | ops:view；先检查两个运维门控，单次最多 100 个账号，仅使用 `PeekAccountsLoadBatch` 与 `GetAccountGroupConcurrencyBatch`，不触发过期项清理。 |
| xy2api.routing.get_quality_state | 当前质量门状态、样本量、时间、stale | accounts:view；只读快照。 |
| xy2api.users.search/get_usage_summary/get_subscriptions | 被授权的普通用户目标 | 各工具逐项沿用静态表中的 catalog AnyOf、Fields 和 target 语义。 |
| xy2api.security.search_prompt_audit | 提示词审计元数据 | security_audit:view；v1 不支持读取 full_prompt、scanner_evidence 或其它正文。 |
| xy2api.audit.search | 管理面操作审计 | audit_logs:view；使用 audit_body 脱敏。 |
| xy2api.me.* | 本人 profile/usage/error/subscription/quota | 仅 role=user 的本人自有服务，或管理员本人使用其用户端权益。 |

`/ops/upstream-errors` 实际是 provider-health source，并非 recovered-only：它只是允许包含 status<400 的行，仍可能包含默认请求搜索已返回的最终失败。可选 source 不整体标 recovered；按持久化状态/事件分类，不能证明恢复的记录为 unknown。跨源同一 ops_error_logs 行按真实存储主键（含必要分区键）去重，再应用总 limit 与稳定 cursor；usage 结算行和错误尝试不是同一行，不得用 request ID 粗暴合并或把 attempts 累加成客户端请求数。

### v1 Tool × ACL × source/output 静态映射

这是 handler 开发的 allowlist；不得按名称概括成 ops/usage 任何一项可看所有关联结果。映射中的 AnyOf 是现有 catalog 的 OR 语义；一个 tool 碰多个独立现有 route 时，对各 entry 分别运行 Decide，全部 entry 必须通过。没有能映射到现有 catalog entry 的 MCP-only 数据源必须单独补写 root-only ToolPolicy 和字段投影后才能注册，不能因登录者是 root 就自动开放；Operator 不默认放行。

| Tool | 必须映射的现有 catalog key | 最小数据来源 | 默认允许输出 |
|---|---|---|---|
| xy2api.requests.search | GET /ops/requests：ops:view；若 `include_recovered_upstream=true`，再独立检查 GET /ops/upstream-errors：ops:view | 默认 OpsRequestDetail 的 usage_logs 与 ops_error_logs UNION；显式 opt-in 时增加 provider-health source | source、source row ID、原 request_id、platform/model、duration/TTFT/status、phase/severity、user/API-key/account/group ID、stream。provider-health source 保留数值型 error row ID，按持久化证据分类，已证明恢复的行不计为最终客户端失败；MCP 过滤 message；如加新 usage-only fields 要同时加 GET /usage 权限。总结果页≤100。 |
| xy2api.requests.get_detail（数值型 `error_row_id`） | GET /ops/errors/:id：ops:view | 单条 ops_error_logs | 结构化状态、machine classification、ID、stage、延时、归属。排除 error_body、upstream message/detail/JSON、client IP、email、自由文本。request/client ID 必须先通过 `requests.search` 定位；本工具不将它当作行 ID。 |
| xy2api.requests.get_attempts（数值型 `error_row_id`） | GET /ops/request-errors/:id、GET /ops/request-errors/:id/upstream-errors：分别独立按各自 Entry Decide（两者现有 AnyOf 均为 ops:view OR usage:view）；被定位行为 recovered upstream 时另检查 GET /ops/upstream-errors/:id：ops:view | 单 ops_error_logs 行中 upstream_errors 的 SQL allowlist 投影；不复用现有路由的跨行宽窗关联扫描 | 仅上游 attempt index、时间、平台、状态码、stage、账号/组/请求引用及机器原因。排除 URL、response body、自由 error/detail。没有该 ID 的错误记录时统一 NotFound；不返回或推断最终客户端 outcome。 |
| xy2api.requests.search_ingress_rejections | GET /ops/ingress-rejections：ops:view | ingress minute aggregate | 分钟、route family、protocol、reason、count；默认排除 client IP 和 user/Key 细粒度 ID。 |
| xy2api.accounts.search | GET /accounts：accounts:view OR channels:view OR groups:view OR usage:view；保留 account redaction | accounts / groups / account_groups | 账号 ID/名称/platform/type/status/group ID；严禁 raw account.extra/credentials。 |
| xy2api.accounts.get_health | POST /accounts/recent-health/batch：accounts:view + account redaction；GET /accounts/upstream-balances：accounts:view | recent health usage/soft-failure snapshot、balance Redis snapshot | 账号 ID、失败/首字/cache 统计与样本/时窗、金额及观察时间/状态；不显示余额失败自由文本、不 probe。 |
| xy2api.groups.search | GET /groups：groups:view OR usage:view OR users:view | groups/category metadata | ID、名称、platform、enabled/category 摘要；无绑定用户行或价格写入表。 |
| xy2api.groups.get_usage_summary | GET /groups/usage-summary：groups:view | usage_group_daily_rollups + usage_group_rollup_state | group ID、与请求窗相交的已闭合自然日桶 actual_cost、配置时区、发布水位与 coverage。现行表不存 requests/tokens，首版不推造此类计数。不可联表读用户与账号凭据。 |
| xy2api.channels.search | GET /channels：channels:view | channels/channel_groups | ID、名称、状态和 group 绑定摘要；无地址、secret、header/body override。 |
| xy2api.channels.get_metrics | GET /channel-monitor-v2/snapshot、/matrix、/errors：每个被查询的 Entry 都要独立 Decide（均 channels:view） | channel_monitor_v2 的分钟聚合与错误分类 | window、passive_real_user_traffic、count/latency/taxonomy/watermark；关闭时 source disabled，水位延迟明确 partial。排除地址、API Key 和自由错误正文。 |
| xy2api.ops.get_error_breakdown | GET /ops/dashboard/error-distribution：ops:view | ops raw/preagg metrics | 分类 count/sample/watermark/filters；不导出行级内容。 |
| xy2api.ops.get_trends | GET /ops/dashboard/error-trend：ops:view | ops raw/preagg trend | bucket、count、source、coverage/watermark。 |
| xy2api.ops.get_alerts | GET /ops/alert-events：ops:view | alert events/rules metadata as returned by read service | 时间、级别、状态、rule ID、机器 reason；没有 resolve/silence。 |
| xy2api.ops.get_system_events | GET /ops/system-logs：ops:view | ops_system_logs | 仅规范 level/component/timestamp/correlation IDs；message、host、extra 一律默认不返回。catalog 无 redaction，MCP 自己的 DTO allowlist 提供此边界。 |
| xy2api.ops.get_realtime_snapshot | GET /ops/realtime-traffic：ops:view | 先执行 `RequireMonitoringEnabled`；实时监控开关关闭时不查询指标并返回 disabled；否则读取所选窗口聚合 | measurement time、platform/group、来源独立行数、已结算 token 与对应吞吐；不能把 usage 行和错误行相加为独立客户端 QPS。不回用户和 account 逐项细节。 |
| xy2api.ops.get_concurrency_snapshot | GET /ops/concurrency：ops:view | 先执行 `RequireMonitoringEnabled` 与 `IsRealtimeMonitoringEnabled`；任一门控关闭时不得访问账号或 Redis reader 并返回 source-disabled；开启时固定条件筛出的最多 100 个账号，调用 `PeekAccountsLoadBatch`、`GetAccountGroupConcurrencyBatch` | platform/group/account ID 与名称、在途/等待/容量计数、采集时间；不调用现有会做 Redis 过期清理的通用并发 helper，不返回账号 extra/credentials。 |
| xy2api.routing.get_quality_state | GET /group-quality-gate/status：accounts:view；如要同时读 policies，再额外检查 GET /group-quality-gate/policies 的 accounts:view | process quality snapshot + policy view | group/account IDs、三个维度状态/样本/时间/stale。不得调用写策略或重算评估入口。 |
| xy2api.users.search | GET /users：users:view OR groups:view OR subscriptions:view，Fields=user_search | users/group/subscription summary | 仅 role=user 的最少 ID、状态、显示名；不得接受 API Key 文本查询。保持 HideAPIKeySearch scope。 |
| xy2api.users.get_usage_summary | GET /users/:id/usage：users:view + Target=user_path_id；若新增独立 usage repository route 则同时声明 GET /usage 的 usage:view | 指定普通用户 usage query | 按 target 检查后固定 user ID，API Key 打码，不含 admin account detail。 |
| xy2api.users.get_subscriptions | GET /users/:id/subscriptions：users:view OR subscriptions:view，然后检查 Target=user_path_id | 指定用户订阅 | 保留 catalog 的 AnyOf OR 语义；再执行目标检查。普通用户工具只允许 actor 本人，不接受 API key/customer identifier 作为目标。 |
| xy2api.audit.search | GET /audit-logs：audit_logs:view + Redact=audit_body | audit_logs 的固定元数据投影 | 时间、操作者 ID、固定操作类型、目标 ID、状态；不读取 request/response body、自由 URL/query 或任意 JSON 正文，仍不得弱化现有 audit_body 规则。 |
| xy2api.security.search_prompt_audit | GET /prompt-audit/events、GET /prompt-audit/events/:id：security_audit:view，分别 Decide | prompt audit metadata/event | metadata-only；默认不返回 full_prompt、scanner_evidence 与任意自由正文。v1 不实现敏感文本 opt-in。 |
| xy2api.me.get_profile | GET /user/profile；owner=actor，backend-mode gate | users 的固定本人投影 | ID、username、role、status、balance、concurrency；无 email、avatar URL、password hash、identity metadata。 |
| xy2api.me.search_usage | GET /usage；owner=actor，backend-mode gate | usage_logs + 用户端允许的 group 元数据 | ID、原 request ID、请求模型、用户计费 token/cost/latency、origin group；无上游账号/成本/地址/凭据；时间窗分页。 |
| xy2api.me.get_usage_detail | GET /usage/:id；owner=actor，backend-mode gate | 同 search 的固定投影 + user_id/id 谓词 | 同 search 字段，非法/他人 ID 统一 NotFound；查询不先装载他人的完整实体。 |
| xy2api.me.get_error_history | GET /usage/errors；owner=actor，backend-mode + user-error-view gate | ops_error_logs 的用户安全元数据 | 时间、用户侧请求 ID、requested model、status、粗分类；无 account ID、upstream attempts 或正文。 |
| xy2api.me.get_error_detail | GET /usage/errors/:id；owner=actor，同上两个 gate | 相同投影 + user_id/id 谓词 | 同 history 安全字段与允许的延时；他人/不存在统一 NotFound。 |
| xy2api.me.get_subscription_status | GET /subscriptions、GET /subscriptions/active、GET /subscriptions/summary、GET /subscriptions/progress；owner=actor，backend-mode gate | user_subscriptions + groups 中限额/名称；只读 owner-bound reader | 订阅 ID、组 ID/名、状态、到期时间、日/周/月用量与限额；normalize 仅修改响应内存，不跑窗口维护。 |
| xy2api.me.get_quota_status | GET /user/platform-quotas；owner=actor，backend-mode gate | user_platform_quotas 固定字段 | platform、quota、usage、window/reset/status；过期响应内存归零，不执行 reset 或维护。 |

如果某 tool 与本表实现不同，需在改代码前更新同一 OpenSpec/static ACL table。测试要以 tool source set × catalog entry × output field 为输入矩阵，而不是只检查模块名。安全敏感文本 v1 全部 default-deny，避免要求 audit-before-read 敏感内容路径；未来若增加该 capability，持久访问审计写入失败测试必须证明 SELECT 和正文返回均不会发生。

表中 route key 是授权与语义依据，不授权调用原 handler 的完整查询。所有 ops 诊断源继承 `RequireMonitoringEnabled` 的业务门控，实时工具额外继承实时开关；管理员诊断入口保留现有 `AdminComplianceGuard` 已确认状态且只读该状态，不替用户确认。工具的输出不超过该路由当前主体可见字段与本表 allowlist 的交集。任何可选 source 若未获授权则拒绝本次请求，不静默退化；需要按行决定 source 时，先查受基本权限保护的最小分类字段，再授权额外 source，之后才查内容。

运行态 Reader 还必须保留底层错误；现有返回空 map/零值的 best-effort wrapper 不能让 Redis 不可用变成“并发为零”，必要时直接包装可返回 error 的已核验底层只读方法。工具名与权限映射应形成可测试静态表，而不是从所有 GET route 动态生成。对于权限目录中的 POST view 例子（如 preview/explain/batch stats），必须证明调用链不写 DB、不发网络请求、不清理 Redis 后才能包装。

响应包含 query_id、source、generated_at、UTC window、filters_applied、coverage、partial_reasons、retention_limited、aggregation_watermark、state_scope、items 和分页信息。每条诊断记录包含 source ref、机器分类、request/client ID、attempt_index、status、group/account/model、关联 evidence 和 relation_confidence。不可解析原始 JSON 绝不原样透传。

## 认证、授权、限额和数据安全

**认证：** 首选 OAuth2.1 Authorization Code + PKCE，并按当前 MCP Authorization、Resource metadata、issuer 和 client consent 要求实现与实测。授权与 token 请求均须绑定 `resource`；首选成熟 OAuth 引擎签发 opaque token；resource server 根据可信 AS 的受保护 token/grant 存储验证签发来源、精确 resource、期限／撤销／scope 与本地 user ID。若未来采用 JWT，另需校验签名、可信 `iss` 和精确 `aud`；JWT 不属于首版必须组件。缺 audience 或目标错误时拒绝；明确拒绝后台 session JWT、Admin API Key、用户上游 API Key、其它 resource token 和 `client_credentials` 机器身份。用户授权对象是具体 XY2API user，现有 admin JWT 的登录态不等于用户同意让第三方 host 读取 MCP 数据。支持企业 OIDC 需用受信任的 issuer 映射到本地用户，由本服务 role/ACL 作最终判定，不信任传入 role claim。

当前仓库只有站内 JWT 登录和 OAuth/OIDC client，没有可复用的 MCP Authorization Server。首版需新增本地 OAuth 2.1 Authorization Code + PKCE 授权端点、受保护资源／授权服务器 metadata、公开客户端注册、按 client 的明确 consent、resource-bound access token、刷新轮换与撤销；授权时复用站内登录身份，但不把站内 JWT 当 MCP token。Client ID 注册仅接受 public client，redirect URI 精确匹配，authorization code 单次使用并绑定 PKCE S256、client、resource、scope 与 user。授权与撤销需要简洁的前端 consent/revoke 页面。首版不以手填 PAT 替代 OAuth；如以后加 PAT，只能作为明确标注的 host-specific 辅助方式，不宣称兼容通用 OAuth host。

**逐请求授权：** 每次 tools/list 与每次 tools/call 都重新解析可信凭证、检查 user active/role、在线读取当前 operator 权限、MCP scope 和目标 owner。本版明确保证“撤权后下一次请求生效”，因此不缓存跨请求 permission allow；静态 tool schema 可 cache。tools/list 仅展示本次主体可用的 allowlist：root 展示全部 v1 工具；operator 仅展示默认必需来源的 module/level/AnyOf/flags 当前通过、且可在其字段规则下安全使用的工具；普通 user 只展示 me.*。Target、参数相关 Fields 和可选 source 依赖调用参数，不能在 tools/list 阶段代替检查；必须在每次 tools/call 中按真实目标、参数和源记录分类严格校验。即使列表过滤已生效，每次 tools/call 仍重新授权。权限依赖不可用时立即拒绝。若未来提议权限缓存，必须作为权限契约修改另行批准，并同时定义最大撤权延迟、可靠版本通知与回归条件，不能悄然采用短暂 stale allow。

**只读隔离：** PostgreSQL 单独只读 login 和明确表/列 SELECT grant；事务 SET READ ONLY 与 query timeout。repository Reader 接口只能调用固定 SELECT 和白名单 projection。Redis 命令需要逐项 allowlist，并用 capture tests 拒绝写操作。禁止后台管理业务写 API、数据导出任务、清理、探测、model sync、上游账号 OAuth refresh、重平衡、账号测速、主动 usage probe。MCP 自身 OAuth refresh/revoke 仅属于独立授权控制面。

**脱敏：** Operator 用与管理目录等价的账号 address、ops URL、body、API Key、请求头和 audit body 规则。服务直接返回结构 DTO 并经 schema allowlist，不把 admin Entity/JSON 그대로 序列化。根管理员也受 secret hard-deny。v1 永不读取或返回 full_prompt、error_body、上游自由正文或系统日志正文；未来若要加入，必须另立 scope、consent 和持久审计方案。

**防止 abuse：** rate limit 按 subject/tool，昂贵 raw detail 更严格；默认短窗口、页≤100、最大窗口硬限制、DB deadline、statement timeout、最大响应 256KiB 和有界并发。不能在无结果时退化成全表扫。v1 不新增业务结果缓存，减少撤权与跨主体缓存风险；只读已有聚合，并保留其水位与覆盖度。

**审计：** 记录主体、token fingerprint、oauth client、tool、权限版本、窗口与结构过滤摘要、耗时、行数、脱敏和错误码；不写完整 token、完整 tool 参数、request body、prompt/response 语料。普通诊断请求审计使用有界异步写入，队列丢弃和持久化失败显式计数，不能声称绝对完整。v1 不含正文工具；未来若立项正文工具，必须先设计持久访问审计失败即拒绝的规则。

**MCP 运行安全：** HTTPS、严格 Origin、localhost 仅绑定 127.0.0.1、禁止 query-string token、反向代理设置大小/超时、Header 日志脱敏。 所有带主体身份的响应使用 HTTP `Cache-Control: private, no-store`；SDK `SetCacheable` 对工具列表等设 `ttlMs=0, cacheScope=private`，避免其默认 public 缓存语义。按主体生成授权后的工具视图，禁止请求间动态修改共享全局 registry 导致工具串号。仅公开 discovery metadata 可按公共配置缓存。Tool 描述明确错误日志和 prompt 是不可信文本，不可被其指示调用其它工具。readOnlyHint 等 annotation 只供 host 使用，不是安全控制。

## 工程结构、授权生命周期与交付顺序

### 最小工程结构

| 组件 | 责任 | 禁止依赖 |
|---|---|---|
| `internal/mcp` transport/registry/policy | 官方 SDK HTTP handler、输入 schema、fresh actor、静态 ToolPolicy、DTO 与响应上限 | 不获得通用 AdminHandler、HTTP proxy、SQL 执行字符串入口 |
| OAuth service/store | 标准 authorize/token/revoke、client/grant/code/refresh lifecycle、站内 consent | 不复用 Admin 全局 Key、站内 JWT secret；不写业务账号/账单 |
| DiagnosticReader | PG 固定参数 SELECT、安全 JSONB 投影、owner predicate、rollup 与水位 | 不注入现有业务 writer DB 或完整 Ent Entity repository |
| RuntimeReader | 限 ID 的无清理 Redis peek、已有余额快照、纯进程状态快照 | 不注入上游 HTTP client、账号刷新/调度/维护接口 |
| MCP audit writer | 最少事件字段、有界异步队列、失败计数和保留 | 不保存完整参数、token、prompt/body 或数据库错误原文 |
| Wire/config/routes | 显式启用、具名 DB 类型隔离、资源 URL、Origin/Host 与连接释放 | 不把主业务 `*sql.DB` 当 read-only fallback |
| 既有前端的两个入口 | OAuth consent 页面、本人 MCP 连接及撤销 | 不复制管理员权限编辑器，不允许 operator 为他人建授权 |

优先沿用现有目录边界；只有确定复用的纯计算才放 service。不要另起微服务、队列基础设施或通用查询 DSL。使用官方 MCP Go SDK v1.8.0 和稳定版 go-oauth2/oauth2/v4 v4.6.0。候选 Fosite v0.49.0 与项目现有 ristretto 依赖不兼容，因此不采用，也不为此改造网关缓存。OAuth 引擎负责标准授权/token 与 PKCE；完整兑换事务、一次性消费、refresh family 重放撤销由 PostgreSQL store 实现并通过并发验证，不能只依赖库默认内存行为。

### OAuth 控制面设计

保留五类记录；物理表是否合并由库的事务契约决定，不另建重复 token 系统。

| 记录 | 必需字段与约束 |
|---|---|
| clients | 随机 client ID、有限长度显示名、精确 redirect URI allowlist、public client 标识、创建/失效时间；不发 client secret、不抓远程 logo/metadata；client name 明示为对方提供的未验证名称 |
| pending_authorizations | 随机 request ID、已验证的 client/resource/redirect/PKCE S256/state/scopes、短到期、绑定的本地 user、一次性状态；不能在前端改写最终 grant |
| grants | user/client/resource、批准工具集合与契约版本、issued user token_version、到期/撤销时间；新工具/升权不自动扩大旧 grant |
| OAuth sessions | code/access/refresh 的验证材料与关联 grant、期限、消费/撤销状态、refresh family；不存原始 bearer 或 code verifier；code/refresh 兑换要求数据库事务内单次消费 |
| access_audit | actor/client/grant/tool、decision、授权版本摘要、时间窗/行数/耗时/错误码；无凭据或原始业务 payload |

认证流：Host 发现 protected-resource metadata → AS metadata → 注册 public client（或使用预注册 client）→ Authorization Code + PKCE S256 → 跳转站内登录/consent → 用户确认具体 client、redirect、资源与读取范围 → 单次 code → 专用 opaque access token → MCP tools。服务端对 authorize 与 token 两端都要求精确 resource；HTTPS/loopback redirect 规则、state 回传、`iss` 响应、code replay 与 refresh reuse 由库和窄适配层共同保证。拒绝 implicit/password/client_credentials；不因为采用成熟库就省掉 resource/consent/store 契约。

建议初始期限：pending/code ≤5 分钟、access 10 分钟、refresh/grant 最长30天，可在实现评审时按部署策略缩短。撤销 grant、账号停用/删除、token_version 变化或角色降权不等待 access 到期；下一请求在线复核即生效。Refresh 轮换必须原子，检测旧 refresh 重放撤销该 grant/family。所有秘密仅从部署 secret 提供，MCP 签发密钥与站内 JWT 隔离。

建议接口契约（路径在实现接线时一次定稿）：

- MCP：`/mcp`，HTTPS Streamable HTTP、stateless；不接站内 JWT。
- OAuth：authorize/token/revoke/register 与 RFC metadata；注册限频、请求大小与每 IP/全局容量有界，过期 client/pending/token 由独立控制面清理任务维护，MCP tools 不触发清理。
- Consent：authorize 校验后只把 `request_id` 交给 `/mcp/authorize`；登录后的 preview/decision 接口用站内认证、CSRF/Origin 约束，返回后端校验的摘要或库生成的 redirect。拒绝也只能回已验证 redirect；非法 redirect 直接本地错误。
- 连接管理：本人 grants 列表与 revoke；界面说明外部 AI 将收到哪些数据。完整 token 不进地址、日志、localStorage 或连接列表。授权 UI 防 iframe，登录 return URL 只允许本站。

### 只读数据库与响应合同

Reader 配置使用独立 `ReaderDB` 类型和 DSN，禁止缺失时回退主库 writer。只读 role 不得 superuser/owner/inherit writer role，不得写业务表或执行可写业务函数；显式 SELECT grant 仅覆盖安全字段/视图。启动预检与测试验证配置，而非只比较用户名。每次 query 开 `READ ONLY` transaction，固定 search_path、statement_timeout 和 lock_timeout；清理/审计/OAuth writer 使用另一接口。查询 attempts 时 SQL 在 JSONB 内按字段白名单投影并限数量，不将完整 `upstream_errors` 带入 Go 再删字段。

不一次新增所有索引。先在受控数据库用真实规模形状 EXPLAIN 核对分区裁剪、时间条件、user/key/request ID 过滤和 top-N；只有发现缺口才追加并发建索引迁移。明细默认1小时、最大24小时、页默认20/最大100；系统事件最大6小时；并发明确 ID 最多100。精确行 ID 也带有界时间窗口以避免分区全扫，AI 可以显式改窗，不能自动无限回溯。建议 reader statement timeout 2秒、tool deadline 5秒、lock timeout 100ms、响应最大256KiB，最终以实现前受控基线校准，不作为未经测量的性能承诺。

分页使用固定 `(created_at,id,source)` 稳定排序和签名 cursor，绑定 actor/grant、工具、filter/time window 与契约版本；cursor 不承载 bearer。`limit+1` 判断下一页，不为每次搜索强制做全量 COUNT。SQL、排序与过滤列均固定白名单；不得使用自由 SQL、正则或全文搜索。所选可选 source 无权限时整次拒绝；源暂不可用时只在该工具明确允许 partial 的契约内返回 partial，绝不补作未授权查询。

公共响应包含 `schema_version/query_id/generated_at/source/coverage/partial_reasons/retention_status/retention_limited/items`，按需提供 `window/filters_applied/next_cursor/aggregation_watermark/state_scope`。coverage 每源给出 available、样本和说明；retention_status 明确区分 unknown、not_applicable 和 limited，只有已知水位或保留范围不足时才置 retention_limited=true。没有可靠 retention/watermark 就返回 unknown，不伪造完整。认证失败使用标准 HTTP 401/OAuth challenge；工具内错误使用 `FORBIDDEN/INVALID_ARGUMENT/NOT_FOUND/SOURCE_DISABLED/SOURCE_UNAVAILABLE/QUERY_TIMEOUT/RATE_LIMITED/RESULT_TOO_LARGE` 的固定 code/message/suggested_action，不透传 SQL、DSN、密码或服务原始错误。超过响应上限返回可缩小窗口/limit 的结构错误，不裁半个 JSON。

关联只输出原始 source ref 与 evidence。持久化相同唯一标识才为 exact；经过明确 `client:` 规则且同 key/user、短窗口、候选唯一可为 corroborated；仅邻近时间是 candidate；多候选为 conflict；缺失源为 partial。relation status 不等于概率，不根据邻近成功记录推断某次失败已恢复。运行态快照只解释当前状态，不声称还原历史选号。

### 开发工作包与分工

用户指定：PM 负责产品契约与集成监督；全部后端由 **gpt-6.1-sol / xhigh** 执行；实施阶段按最新指令，由 **gpt-6-astra / xhigh** 分交付核对与安全红队两路审查。前端只做既有风格的最小 consent/revoke，不扩展为界面重构。

| 阶段 | 交付物 | 通过条件 |
|---|---|---|
| WP0 源码与协议预检 | 权限/source 图、读副作用清单、受限生产关联结论、SDK/AS 可行性、单路设计审查 | 方案审查问题闭合；库与目标 Host 的实际兼容仍需实现 spike |
| WP1 身份与只读底座 | OAuth AS/store、SDK transport、fresh actor/ToolPolicy、独立 PG Reader、配置与开关 | PKCE/resource/重放/撤销及 role/ACL deny 路径通过，不能先用 admin credential 临时打通 |
| WP2 管理诊断工具 | 请求 search/detail/attempts → accounts/groups → ops 并发/趋势 → channels/quality/user-target/audit metadata | 每交一个工具同时完成 source、ACL、gate、DTO 与限额；按本方案静态表逐项勾选，不铺空壳 |
| WP3 本人工具与授权界面 | 七个 me 工具、登录/consent、本人 connections/revoke、中文英文文案 | owner、backend mode、user error view、过期额度纯展示及拒绝授权均闭环 |
| WP4 可运维与验收 | 有界审计、限频/并发、取消/超时、只读 role 配置 runbook、迁移/回退、关键测试 | 官方 SDK roundtrip + 至少一个拟支持真实 OAuth Host；受控性能基线；交付和红队均无未解决 P0/P1 |
| WP5 发布准备 | 文档/依赖/迁移一致性、变更摘要、上线检查与回退步骤 | 开关默认关闭；发布、迁移与生产启用在后续明确授权后执行 |

依赖 WP0 → WP1 → WP2/WP3 → WP4 → WP5。优先把“授权 → 找请求 → 看尝试 → 查账号/组 → 撤销”做成完整链路，再扩展其余表中工具。PM 在工作包切换、契约变化或审查发现时调度；两路 Astra 按领域分阶段复核，共用同一工具映射。代码或测试证据未完成的条目保持未勾选。

只读 MCP 仍需新增授权和审计的控制面迁移；迁移只追加 checksum，不修改既有 SQL。回退先关闭 MCP 路由并撤销授权，不回滚删除授权记录或诊断业务数据；撤销状态要保留到关联 token 自然过期。没有真实 Host 或生产条件时如实登记未验证项，不虚报上线就绪。

## 测试门槛

- Registry 精确 allowlist、伪造 tools/call、非法输入、超窗、分页和排序必须单测。
- 每个 MCP 数据域覆盖 root allow、operator allow/deny、AnyOf/flag/target 和 user self/foreign owner。
- 测试运行中撤销 operator 权限、改角色、停用账号；下一次 call 立即反映。ACL DB 错误 fail closed。
- 伪造 user/account/group/key/error/prompt ID、批量 ID、self/admin targets 都必须拒绝或统一 not found，不泄露存在性。
- Canary 字段放置 token、密码、userinfo URL、header、error body、system log、prompt，断言所有 v1 工具均不查询完整敏感字段、不回显。
- PostgreSQL ro login 覆盖所有工具，Redis 捕获命令无 ZREM cleanup / SET / DEL / EXPIRE / 写 EVAL。
- 请求 ID 测试：client:/local:/generated:/任务前缀、不同 api_key_id、重试、rescued upstream、recovered 2xx、最终失败、冲突关联。
- 聚合测试：业务限制、错误尝试、最终失败区分；低样本；时区；origin/serving group；raw/rollup 水位。
- 业务 Reader 网络 stub 证明无账户主动查询、上游 OAuth refresh、monitor run、quota probe 或任意 URL 请求；不把 MCP 自身 OAuth token 生命周期当作业务查询。
- MCP transport/authorization 测试 Origin 拒绝、401 challenge、协议协商、超时取消、Bearer 从不入 URL/log。目标 host 配置做真实 E2E。
- 文档一致性、Migration checksum、后端 unit/integration/vet/lint 与前端测试按实际改动通过。上游 scheduler audit 只在其拥有文件变更时执行，且按 AGENTS.md clean worktree 规则处理。

## 明确不做与风险

- 不开放原始 SQL、数据库 shell、任意 service 方法、任意 Admin API endpoint、全表 list/all、动态 SQL、文件工具和 shell 工具。
- 不开放可能与上游交互的 GET：主动账号 usage、测试账号、监控立即检测、刷新 token、探测余额、模型同步和调度重平衡。
- 不开放告警处理、日志清理、审计清空、凭证或备份导出、设置变更及账号／分组修改。
- 根管理员“全量读取”不等于外部 AI 可导出 server credentials、Access Token、备份和数据库 secret。
- MCP 连接的 provider 可能在组织外；通过 ACL 仅代表 XY2API 用户可查看，不必然等于同意向远程 AI 主机传输。授权界面要说明 scope/主机/高敏 prompt。
- 运维表可能受监控关闭、异步 sink drop、passthrough、过滤或 retention 影响。没有日志不能当零失败。
- 进程内韧性状态、Redis 实时 cache 和聚合 watermark 可能不完整。必须标来源和范围。
- 日志文本可能含 prompt injection；只做证据展示，不因此增加任何执行工具。
- 新权限模块默认不能进入 MCP；对未来未知路由/字段/data source fail closed。

## 来源代码与文档

- 管理路由／身份：backend/internal/server/router.go、server/routes/admin.go、routes/user.go、middleware/admin_auth.go。
- Operator ACL：backend/internal/pkg/operatoraccess/{catalog.go,types.go,policy.go,scope.go,redact.go}；backend/internal/service/operator_access_service.go、operator_access_guard.go；backend/internal/server/middleware/admin_permission.go；docs/ADMIN_PERMISSIONS.md。
- Usage 与语义：backend/internal/service/{usage_log.go,gateway_usage_billing.go,ops_request_details.go}、backend/internal/repository/ops_repo_request_details.go、backend/internal/pkg/usagestats/usage_log_types.go、backend/internal/handler/dto/mappers.go、docs/USAGE_METRICS.md。
- Ops：backend/internal/service/{ops_models.go,ops_service.go,ops_upstream_context.go,ops_system_log_sink.go,ops_ingress_reject.go}、backend/internal/handler/ops_error_logger.go。
- 实时状态：backend/internal/handler/admin/resilience_handler.go、backend/internal/service/resilience_explain.go、share_scheduling_admin.go、backend/internal/repository/concurrency_cache.go、concurrency_group_slot_cache.go、docs/GATEWAY_RESILIENCE.md、docs/SHARE_SCHEDULING.md。
- 外部 probe：backend/internal/handler/admin/account_handler.go、backend/internal/service/account_usage_service.go、upstream_balance.go、upstream_balance_store.go、docs/UPSTREAM_BALANCE.md。
- 高敏迁移：backend/migrations/{033_ops_monitoring_vnext.sql,035_usage_logs_partitioning.sql,054_ops_system_logs.sql,181_prompt_audit.sql,182_prompt_audit_full_prompt.sql,274_create_gateway_undelivered_requests.sql,282_admin_operator_permissions.sql}。
- 仓库规则：AGENTS.md、docs/PROJECT_MEMORY.md、docs/README.md、.gitignore、tools/check_docs_consistency.py、tools/docs_consistency.json、backend/go.mod。

## MCP 官方参考（2026-11-05 检视）

- [当前协议](https://modelcontextprotocol.io/specification/2026-07-28)
- [HTTP Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
- [Tools、schema 和 annotations](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
- [Security Best Practices](https://modelcontextprotocol.io/docs/2026-07-28/tutorials/security/security_best_practices)
- [官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk)

实施前重新核实 MCP revision、SDK Go 支持矩阵、OAuth 和实际 host 行为；当日检索结果不是永久兼容承诺。

## 方案审查结论

2026-11-05，唯一逆向审查员 gpt-6-astra / xhigh 签字：P0=0、P1=0、必要 P2=0，方案可作为实施依据。审查闭合项包括 AnyOf/目标/字段规则、数值错误行 ID、两级监控门控、七个本人工具、provider-health 与 recovered 分类、跨源去重分页、JSONB 安全投影和身份响应缓存隔离。本结论不是“实现无漏洞”保证；代码实现、实际 Host 验证和上线验收均未执行，tasks 保持未勾选。
