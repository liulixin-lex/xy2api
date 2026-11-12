# 项目结构与架构

本文介绍 XY2API 仓库的目录组织和后端分层，说明一次网关请求从进入路由到完成计费经过哪些环节，数据存放在哪里，以及前端怎样打包进后端二进制。适合准备阅读代码、提交改动或开发插件的贡献者。本地环境、构建和测试见 [本地开发环境](local-setup.md)。

## 技术栈

| 部分 | 技术 |
|---|---|
| 后端 | Go（版本以 `backend/go.mod` 的 `go` 指令为准，当前为 1.27.3）、Gin、Ent、Wire |
| 前端 | Vue 3、TypeScript、Vite、Pinia、Vue Router、vue-i18n、Tailwind CSS |
| 数据库 | PostgreSQL 15+ |
| 缓存与跨实例协调 | Redis 7+ |
| 插件 | hashicorp/go-plugin，本机子进程 + gRPC |

## 仓库结构

```text
.
├── backend/                Go 后端（独立 Go 模块，模块路径 github.com/liulixin-lex/xy2api）
├── frontend/               Vue 3 前端（管理端与用户端是同一个单页应用）
├── deploy/                 部署文件
├── tools/                  CI 与运维辅助脚本
├── skills/                 面向 AI Agent 的技能包
├── docs/                   文档
├── Makefile                顶层构建、测试、检查和代码生成入口
├── Dockerfile              多阶段镜像：构建前端 → 构建内嵌前端的后端 → 运行镜像
├── Dockerfile.goreleaser   发布流程使用的镜像定义
├── .goreleaser.yaml        发布制品配置
└── .github/workflows/      CI（ci.yml）与发布（release.yml）
```

### backend/

| 路径 | 内容 |
|---|---|
| `cmd/server/` | 服务入口 `main.go`；Wire 注入器 `wire.go` 与生成的 `wire_gen.go`；版本文件 `VERSION`、`SUB2API_COMPAT_VERSION` |
| `cmd/jwtgen/` | 为管理员签发 JWT 的开发辅助命令 |
| `cmd/profit-preview/` | 读取导出的 JSON，离线预演分组利润准入判定 |
| `cmd/cleanup-ingress-reject-logs/` | 清理 `ops_error_logs` 中历史准入拒绝记录的维护命令，默认只做 dry run |
| `ent/schema/` | Ent 实体定义；`ent/` 下的其他文件都是生成代码 |
| `migrations/` | SQL 迁移和 `checksums.json`，编译时嵌入二进制 |
| `internal/config/` | 配置加载（Viper：配置文件 + 环境变量）、默认值和校验 |
| `internal/server/` | HTTP 服务器、全局中间件（`middleware/`）、路由注册（`routes/`） |
| `internal/handler/` | HTTP 处理器：网关、用户端、`admin/` 管理端、`dto/` 传输对象 |
| `internal/service/` | 业务逻辑：网关转发、账号调度、计费与定价、运维监控、支付、插件宿主等 |
| `internal/repository/` | 数据访问：Ent 与原生 SQL 查询、Redis 缓存、上游 HTTP 客户端、迁移执行器 |
| `internal/domain/` | 跨层共享的常量与领域类型，例如平台清单、角色、状态；`schedquality/` 是 OpenAI 质量状态的纯规则与全部常量（请求档、统计、结论、冷却、主动测试判定） |
| `internal/pkg/` | 通用工具包，以及各上游协议的适配代码（`claude`、`openai`、`gemini`、`antigravity`、`xai`、`apicompat`、`tlsfingerprint` 等） |
| `internal/resilience/` | 网关韧性规则：上游失败分类、请求预算、惩罚与余额暂停、首包前保活。纯函数，不做 I/O |
| `internal/payment/` | 支付渠道抽象、注册与负载均衡 |
| `internal/mcp/` | 需要认证的只读诊断 MCP 接口 |
| `internal/securityaudit/` | 提示词审计 |
| `internal/usageexport/` | 有界、可恢复的用量导出任务 |
| `internal/setup/` | 首次运行的安装向导，以及 `AUTO_SETUP` 自动安装 |
| `internal/web/` | 前端静态资源嵌入与 `index.html` 注入 |
| `internal/integration/` | 端到端测试（`-tags=e2e`），对一个运行中的实例发请求 |
| `internal/testutil/` | 单元测试共用的 Stub、Fixture 和辅助函数 |
| `pkg/pluginapi/` | 插件公开契约，见 [插件开发](plugins.md) |
| `resources/model-pricing/` | 随程序分发的模型价格兜底数据 |
| `scripts/` | `resolve-version.sh`（构建时解析版本号）等脚本 |

### frontend/

`frontend/src/` 下的主要目录：

| 路径 | 内容 |
|---|---|
| `api/` | 按模块划分的接口调用，`client.ts` 是共享的 axios 实例，基础路径为 `/api/v1` |
| `components/` | 组件。`common/` 是通用组件（`DataTable`、`BaseDialog` 等），`layout/` 是页面外壳（`AppLayout`、`AuthLayout`、`TablePageLayout`、`CommandPalette`），其余目录按业务划分 |
| `views/` | 路由页面：`admin/`、`user/`、`auth/`、`setup/`、`public/` |
| `features/` | 自成一体的功能模块，包含各自的页面、组件和逻辑（渠道监控 V2、MCP、提示词审计、在线客服、工单等） |
| `composables/` | 组合式函数（按需挂载、动效偏好、自动刷新、命令面板等） |
| `stores/` | Pinia store（`auth`、`app`、`adminSettings` 等） |
| `router/` | 路由表、导航守卫、页面标题；`meta.d.ts` 定义路由 meta 字段 |
| `i18n/locales/` | `zh/` 与 `en/` 两套文案，键必须完全一致 |
| `styles/`、`style.css` | 全局样式与动效变量（`styles/motion.css`） |
| `types/`、`utils/` | 类型定义与工具函数 |

前端约定见 [前端约定](frontend.md)。

### deploy/、tools/、skills/

- `deploy/`：Docker Compose 文件（`docker-compose.yml` 及 `local`、`standalone`、`dev` 变体）、`.env.example`、完整配置示例 `config.example.yaml`、安装脚本 `install.sh`、`docker-deploy.sh`、容器入口 `docker-entrypoint.sh`、systemd 单元、`Caddyfile`、只读 MCP 数据库授权 SQL、OpenAI TOTP Worker 的环境示例与单元文件。`deploy/tests/` 下的 shell 测试在 CI 中运行。部署方法见 [Docker Compose 部署](../deployment/docker.md) 和 [配置](../deployment/configuration.md)。
- `tools/`：`ci/race_suites.py`（竞态与依赖服务测试套件）、`ci/changelog_section.py`（发布时从 `CHANGELOG.md` 提取说明）、`billing-integrity/audit.sql`（只读计费核对 SQL）、`openai_totp_*` 与 `openai-totp-runtime/`（[OpenAI TOTP Worker](../deployment/openai-totp-worker.md)）、`xy2api_upgrade_preflight.py`（[从 Sub2API 迁移](../deployment/migrate-from-sub2api.md) 前的检查）。`tools/tests/` 是这些脚本的单元测试。
- `skills/xy2api-admin/`：给 AI Agent 用的管理端技能包，附带一个调用管理 API 的 Node 命令行（`scripts/xy2api-admin.js`），通过 `XY2API_BASE_URL` 与 `XY2API_ADMIN_API_KEY`（或 `XY2API_JWT`）连接实例。

## 后端分层

后端按 handler → service → repository 分层：

- **handler** 解析请求、调用 service、写响应，不直接访问数据库或 Redis。
- **service** 持有业务规则。它需要的存储和外部能力以接口形式定义在 service 包内（例如 `billing_cache_port.go`、`http_upstream_port.go`、插件的 `PluginKVStore`），由 repository 实现。
- **repository** 实现这些接口，封装 Ent、原生 SQL、Redis 和上游 HTTP 客户端。
- **Wire** 负责组装：各包的 `wire.go` 声明 `ProviderSet`，`cmd/server/wire.go` 是注入器，`wire_gen.go` 是生成结果。

分层由 golangci-lint 的 `depguard` 规则强制（`backend/.golangci.yml`）：`internal/service` 和 `internal/handler` 不能导入 `internal/repository`、`gorm.io/gorm` 和 `github.com/redis/go-redis/v9`。少数运维聚合文件和 `service/wire.go` 是登记过的例外。新增依赖时先在 service 定义接口，不要扩大例外清单。

## 网关请求的处理路径

以 `POST /v1/messages` 为例，其他网关端点结构相同。

1. **进入服务器。** `internal/server/http.go` 创建 `http.Server`（可选 h2c），`router.go` 的 `SetupRouter` 挂载全局中间件：请求日志、会话绑定上下文（客户端 IP 与 UA）、渠道监控探测头处理、CORS、安全响应头（含 CSP）、Server-Timing。内嵌前端时，前端中间件也在这里挂载。
2. **匹配路由组。** `internal/server/routes/gateway.go` 注册网关路由组：`/v1`（Claude 与 OpenAI 兼容）、`/v1beta`（Gemini）、`/backend-api/codex`、`/antigravity/v1` 等。管理端与用户端接口在 `/api/v1` 下，健康检查是 `/health` 和 `/ready`。
3. **路由组中间件。** 每个网关路由组依次经过：请求体大小限制 → 客户端请求 ID → 运维错误日志记录 → 入站端点归一化 → **API Key 鉴权** → 可用性兜底分组 → 分组模型白名单 → 分组系统提示词 → 组合分组目标平台解析 → 要求 Key 已绑定分组。
   - API Key 鉴权（`internal/server/middleware/api_key_auth.go`）依次从 `Authorization: Bearer`、`x-api-key`、`x-goog-api-key` 读取 Key，查询参数里的 Key 会被拒绝。它分两层：鉴权（Key 有效、用户状态、IP 限制）总是执行；计费执行（过期、配额、订阅、余额）在 `/v1/usage` 等只读端点上跳过。鉴权结果有两级缓存：进程内缓存和 Redis。
4. **按分组平台分派。** 路由处理函数根据 Key 所属分组的平台选择处理器：OpenAI、Grok 与多协议 API Key 平台走 `OpenAIGatewayHandler`（`openai_gateway_handler.go`），其余平台（Claude、Gemini、Antigravity 等）走 `GatewayHandler`（`gateway_handler.go`、`gemini_v1beta_handler.go`），再由它们调用对应平台的 service。
5. **准入与预占。** 处理器先占用户并发槽位（等待有上限），再调用 `BillingCacheService.CheckBillingEligibility` 检查余额、订阅和配额，然后按估算预占在途额度。
6. **选号。** 进入失败切换循环（`internal/handler/failover_loop.go`）。每一轮调用 service 选号（Claude 系为 `gateway_scheduling.go` 的 `SelectAccountWithLoadAwareness`，OpenAI 系为 `openai_gateway_scheduling.go` 与 `openai_selection_*.go`），综合粘性会话、分组与模型、账号可调度状态、负载和优先级。可调度账号列表来自 Redis 中的调度快照，快照由 `scheduler_snapshot_service.go` 根据数据库的 scheduler outbox 事件重建。选中账号后占账号并发槽位，槽位满时进入等待队列。调度规则见 [账号调度](../guides/scheduling.md)。
7. **转发。** service 按平台改写请求，经 repository 的上游 HTTP 客户端发出，按账号的代理与 TLS 指纹配置出站。命中插件绑定的 OpenAI OAuth 账号改由插件进程发出上游请求，见 [插件开发](plugins.md)。
8. **失败处理。** 上游失败经 `internal/resilience` 分类后，决定同账号重试、切换账号、临时停调度或直接返回。一旦已经向客户端写出响应内容，就不再切换账号。见 [网关韧性](../guides/gateway-resilience.md)。
9. **计费。** 转发成功后，处理器调用 `GatewayService.RecordUsage`（`gateway_usage_billing.go`）：按定价计算费用，写 `usage_logs`，扣减余额或累计订阅、配额用量。计费任务在请求处理器返回前同步执行，不受客户端断开影响（`internal/handler/durable_usage_task.go`）。每次计费是一条至多应用一次的命令（`usage_billing.go`），失败由恢复流程补偿。用量口径见 [用量指标与导出](../guides/usage.md)。

### 运维错误日志与上游尝试

`OpsErrorLoggerMiddleware` 把状态码 ≥ 400 的响应写入 `ops_error_logs`。其中 `upstream_errors` 是 JSON 数组，每个元素对应一次上游尝试，包括同账号重试和跨账号切换。修改或新增失败路径时遵守以下约定：

- **代理归属在事件发生时记录。** 每个元素带 `proxy_id` 和 `proxy_name`。走托管代理时记录代理 ID 和当时的名称；明确直连时为 `proxy_id=null`、`proxy_name=direct/no_proxy`；无法从事件时的证据证明路线时（旧事件、发起传输前的凭据失败、OpenAI WebSocket 使用默认 HTTP 客户端）为 `proxy_id=null`、`proxy_name=unknown`。`proxy_id` 为空当且仅当 `proxy_name` 是这两个值之一。不记录代理 URL、主机、端口或凭据。
- **不要事后推断。** 不能用账号当前的 `proxy_id` 补写历史事件。统计按代理分组时，以事件字段为准；缺失归属的旧事件归入 unknown。
- **新增失败点必须带归属。** `internal/service` 中每个非测试的 `OpsUpstreamErrorEvent{` 字面量要么设置 `ProxyName`，要么交给会自行盖章的辅助函数（如 `handleUpstreamTransportError`、`handleOpenAIUpstreamTransportError`）。`appendOpsUpstreamError` 只接收已完整的事件，不会回查账号。
- **单请求有上限。** 最新 16 次尝试保留 `detail` 与上游响应体；更早的只保留标量字段，URL 与消息截到 512 字节。每个请求最多保留 256 次尝试、约 512 KiB；有丢弃时，保留下来的最早一条带 `dropped_earlier_attempts`。

## 管理接口与权限

后台有两种角色：超级管理员（`admin`）和普通管理员（`operator`）。普通管理员只能使用被授予的模块（分“查看”和“管理”两级）与开关。安全边界在后端，前端隐藏入口只是为了体验。功能说明和配置方法见 [管理员与权限](../guides/admin-permissions.md)。

**检查方式**

- `/api/v1/admin` 路由组的中间件顺序为：`adminAuth` → 面板限流 → 操作日志 → `AdminPermission` → `AdminComplianceGuard`。`adminAuth` 为超级管理员打上标记，为普通管理员加载权限范围。
- 超级管理员直接放行。
- 普通管理员按权限目录 `internal/pkg/operatoraccess/catalog.go` 检查。目录以“方法 + 路径”为键，路径去掉 `/api/v1/admin` 前缀，例如 `PUT /accounts/:id`。查不到条目的接口一律返回 403。

**新增管理接口时必须做的事**

1. 在权限目录中登记一条 `Entry`：

   | 字段 | 含义 |
   |---|---|
   | `Kind` | `KindModule`：按模块检查；`KindSuperOnly`：只有超级管理员；`KindAnyAdmin`：任何能进后台的人 |
   | `Module` + `Level` | 所属模块，以及 `LevelView` 或 `LevelManage`。任何写操作都用 `LevelManage` |
   | `AnyOf` | 只用于查看级接口。列出的模块中任一有查看权限即可访问，用于跨模块的下拉读取 |
   | `Flags` | 额外要求的开关，必须全部具备，例如涉及资金的 `users.balance_adjust`、导出用的 `data.export` |
   | `Target` | 目标规则，例如 `user_path_id`：确保普通管理员只操作普通用户 |
   | `Fields` | 字段规则，例如 `account_update`：拒绝或改写无权修改的字段 |
   | `Redact` | 返回前的脱敏规则，例如 `account`、`api_key` |
   | `WS` | WebSocket 接口，连接期间每 30 秒复核一次权限 |

2. 在 `internal/pkg/operatoraccess/testdata/pra_catalog.json` 中加入同样的一行，并更新 `catalog_test.go` 里的条目总数。
   - 新增目标、字段或脱敏规则时，要在 `TestCatalogEntriesAreConsistent` 的清单中登记。
   - 目标和字段规则在 `internal/service/operator_access_guard.go` 中实现，脱敏规则在 `operatoraccess/redact.go` 中实现。
   - 由 service 自行执行的字段规则，还要登记到 `ServiceEnforcedFields` 和 `testdata/pra_service_enforced.json`。
3. 路由注册在 `internal/server/routes/admin.go` 的 `admin` 组里，就会自动经过 `AdminPermission`。另建后台路由组时，要按 `adminAuth` → `AdminPermission` → `AdminComplianceGuard` 的顺序挂中间件。
4. 前端的管理页路由声明 `meta.permission`；按钮和菜单用 `useAdminAccessStore().can('accounts:manage')` 或 `can('data.export')` 这类调用决定是否显示。
5. 删除接口时，同时删除目录条目和 testdata 中的那一行。

`internal/server/admin_permission_catalog_test.go` 与 `internal/pkg/operatoraccess/catalog_test.go`（`unit` 标签）会枚举全部路由。以下任一情况都会导致测试失败：有未登记的后台路由、有找不到路由的目录条目、中间件顺序不对、目录与 testdata 不一致。

**其他规则**

- **写入要有调用者。** AdminService 中写入用户、账号、分组的函数要求上下文带有调用者，即普通管理员的权限范围、超级管理员标记（两者都由 `adminAuth` 设置）或系统标记，三者都没有时返回 503 `ADMIN_ACTOR_MISSING`，不写入任何数据。后台任务调用这些函数时，用 `operatoraccess.WithSystemActor(ctx, "<任务名>")` 标记。
- **新增模块或开关。** 同时修改 `operatoraccess/types.go` 的 `Modules`/`Flags`、前端 `types/adminAccess.ts` 的 `ADMIN_MODULE_IDS`/`ADMIN_FLAG_IDS`，并更新用户文档。
- **失败即拒绝。** 带目标或字段规则的接口会先读入请求体并检查是否规范：出现重复的键或只差大小写的同名键时，返回 400 `ADMIN_FIELD_FORBIDDEN`，`cause=noncanonical`。

普通管理员可能收到的原因码：

| 原因码 | HTTP | 何时返回 |
|---|---|---|
| `ADMIN_PERMISSION_DENIED` | 403 | 缺少模块权限或开关。`metadata.required` 为所需权限（如 `accounts:manage`、`data.export`），未登记的接口为 `catalog`；有 `AnyOf` 时，`metadata.any_of` 列出可替代的查看权限 |
| `SUPER_ADMIN_REQUIRED` | 403 | 只有超级管理员能做 |
| `ADMIN_TARGET_FORBIDDEN` | 403 | 目标用户不是普通用户，或是自己 |
| `ADMIN_FIELD_FORBIDDEN` | 400 | 提交了无权修改的字段，见 `metadata.field` 与 `metadata.cause` |
| `ACCOUNT_API_KEY_REQUIRED` | 400 | 修改网络设置时没有重填已保存的密钥 |
| `OPERATOR_NETWORK_CHANGE_FORBIDDEN` | 403 | 无权修改网络设置 |
| `ADMIN_PERMISSION_INVALID` | 400 | 提交的权限 JSON 不合法 |
| `OPERATOR_ACCESS_UNAVAILABLE` | 503 | 权限读取失败，按拒绝处理 |
| `ADMIN_ACTOR_MISSING` | 503 | 写入函数没有收到调用者身份 |
| `ADMIN_REDACTION_FAILED` | 500 | 脱敏失败，不返回原始内容 |
| `REQUEST_TOO_LARGE` | 413 | 带目标或字段规则的接口，请求体超过 16 MiB |

## 数据存储

### PostgreSQL 与 Ent

- **表结构以 SQL 迁移为准。** `backend/migrations/*.sql` 嵌入二进制，每次启动由 `internal/repository/migrations_runner.go` 按文件名顺序执行。执行时持有 PostgreSQL advisory lock，多实例同时启动也只有一个实例执行迁移。已执行的文件记录在 `schema_migrations` 表。不使用 Ent 的自动迁移。编写规则见 [本地开发环境](local-setup.md#数据库迁移)。
- **Ent 提供类型化查询。** `ent/schema/` 生成 `ent/` 下的客户端代码，启用了 `sql/upsert`、`intercept`、`sql/execquery`（事务内执行原生 SQL）和 `sql/lock`（`FOR UPDATE`），主键类型为 `int64`。修改表结构时 schema 与迁移都要改。
- **启动顺序**（`internal/repository/ent.go` 的 `InitEnt`）：连接数据库 → 执行迁移 → 创建 Ent 客户端 → 补齐系统密钥（配置里没有 `jwt.secret` 时生成并存入数据库，保证多实例一致） → 完整校验配置。

### Redis

Redis 存放缓存、跨实例协调状态和插件的持久键值。主要用途（不完整）：

| 用途 | 键前缀 | 实现 |
|---|---|---|
| API Key 鉴权缓存与 Key 限流 | `apikey:auth:`、`apikey:ratelimit:`、`apikey:rate:` | `repository/api_key_cache.go`、`billing_cache.go` |
| 余额与订阅计费缓存 | `billing:balance:`、`billing:sub:` | `repository/billing_cache.go` |
| 用户、Key、账号并发槽位 | `concurrency:user:`、`concurrency:api_key:`、`concurrency:account:` | `repository/concurrency_cache.go` |
| 粘性会话 | `sticky_session:` | `repository/gateway_cache.go` |
| 调度快照 | `sched:` | `repository/scheduler_cache.go` |
| RPM、会话数与窗口成本限制 | `rpm:`、`session_limit:account:`、`window_cost:account:` | `repository/rpm_cache.go`、`session_limit_cache.go` |
| 单实例后台任务选主 | `leader:lock:` | `repository/leader_lock_cache.go` |
| 插件键值存储 | `plugin:kv:v1:` | `repository/plugin_kv_store.go` |

多实例部署时，所有实例必须连接同一个 PostgreSQL 和 Redis。只应由一个实例执行的后台任务（备份、看板聚合等）通过 Redis 选主锁协调。

## 前端如何嵌入后端

- `pnpm run build` 把前端构建到 `backend/internal/web/dist/`（`vite.config.ts` 的 `build.outDir`）。该目录不入库。
- 后端带 `-tags=embed` 编译时，`internal/web/embed_on.go` 用 `//go:embed all:dist` 把产物打进二进制；不带该标签时编译 `embed_off.go`，访问页面会返回 404 和提示 `Frontend not embedded. Build with -tags embed to include frontend.`，API 不受影响。因此带 `-tags=embed` 编译前必须先构建前端，`make build` 会按顺序完成这两步。
- 内嵌前端由中间件提供：`/api/`、`/v1/`、`/v1beta/`、`/backend-api/`、`/antigravity/`、`/setup/` 等网关与接口前缀直接放行给路由；存在的静态文件直接返回，带哈希的资源设置长期缓存；其余路径一律返回 `index.html`，交给前端路由处理。
- 返回 `index.html` 前，后端把公开设置以 `window.__APP_CONFIG__` 注入页面（带 CSP nonce），并按站点名称和 Logo 改写标题与图标。结果有缓存，系统设置更新时失效。
- 进程工作目录下的 `data/public/` 中如果有与内嵌产物同路径的文件，优先返回该文件，可用来替换个别静态资源。
- 开发时 Vite 开发服务器在 `index.html` 里注入同样的 `window.__APP_CONFIG__`（向后端请求 `/api/v1/settings/public`），行为与生产一致。

## 延伸阅读

- [本地开发环境](local-setup.md)：前置条件、运行、构建、测试、代码生成、迁移规则、CI。
- [插件开发](plugins.md)：插件协议、包格式与签名。
- [前端约定](frontend.md)：性能、动效与通用交互约定。
- [从源码构建](../deployment/source.md)：面向部署者的构建步骤。
