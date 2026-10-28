# 设计：普通管理员角色与逐人权限（2026-10-27）

> 管理员看到的行为、开关、原因码与回退步骤以现行文档 [`docs/ADMIN_PERMISSIONS.md`](../../../docs/ADMIN_PERMISSIONS.md) 为准。本文记录实现结构与取舍，文件路径相对仓库根目录，后端路径省略 `backend/internal/`。
>
> 依据：仓库外的开发文档、开发契约与逐条路由的权限目录。GPT-6-astra 与 Claude 分别调研并互审。设计经三轮审查，每轮 astra 与 Claude 各审一遍，期间修订四版。第三轮没有 P0、P1，设计门禁通过。

## 1. 结构

| 层 | 文件 | 职责 |
|---|---|---|
| 迁移 | `backend/migrations/282_admin_operator_permissions.sql` | 新表 `admin_operator_permissions`，不改 `users` |
| 角色 | `domain/constants_operator.go`、`service/domain_constants_operator.go`、`service/user_operator.go` | `RoleOperator`、`CanAccessAdmin`、`CanAccessAdminRole` |
| 权限包 | `pkg/operatoraccess/`：`types.go`、`catalog.go`、`policy.go`、`scope.go`、`actor.go`、`redact.go`、`canonical.go` | 模块与开关、权限目录、判定、请求范围、调用者、脱敏、规范请求检查 |
| 仓库 | `repository/admin_operator_permission_repo.go`、`operator_access_query_repo.go` | 原生 SQL 读写权限行；分组成员与代理占用查询 |
| 服务 | `service/operator_access_service.go`、`operator_access_guard.go`、`operator_address_check.go` | 加载权限、`/me/access`、目标与字段规则、写入时地址检查 |
| 服务钩子 | `service/operator_actor.go`、`operator_account_rules.go`、`operator_user_rules.go`、`operator_group_rules.go` | 调用者规则，账号、用户、分组的资金、放置与凭据规则 |
| 中间件 | `server/middleware/admin_operator_scope.go`、`admin_permission.go`、`admin_permission_writer.go`；`admin_auth.go` 具名补丁 | 加载范围、按目录判定、脱敏写出 |
| 处理器 | `handler/admin/operator_access_handler.go` | `/me/access` 与权限读写 |
| 前端 | `stores/adminAccess.ts`、`composables/useAdminPermission.ts`、`components/admin/user/OperatorPermissionsTable.vue`、`i18n/locales/{zh,en}/admin/operatorAccess.ts` | 权限 store、页面只读开关、权限表、文案 |

上游原样文件只加一行钩子，调用新的 XY 自有文件，并登记 `manual_merge` 与 required_literals（第 10 节）。

## 2. 权限目录即代码

- **目录**：`pkg/operatoraccess/catalog.go` 是一张表。每条经过后台鉴权的接口占一行，共 509 行，包括 payment 管理组、`/api/v1/pages`、运维 WebSocket 与下载票据。
  - 键是 `"METHOD /path"`，路径相对 `/api/v1/admin`，`/api/v1/pages` 用绝对路径。格式与上游 `adminaccess` 相同，便于以后合并。
  - 每行写明模块、级别（`view`、`manage`、`super_only`、`any_admin`）、开关、跨模块读取（any_of）、目标规则、字段规则、脱敏与 WebSocket 标记。
- **钉住**：目录与“由服务层执行的字段规则”集合各有一份 JSON 快照（`testdata/pra_catalog.json`、`testdata/pra_service_enforced.json`），由 `TestCatalogMatchesApprovedTable` 比对。丢一个目标、字段或开关都会失败。
- **路由枚举**：测试用生产的 `ProvideRouter` 构造引擎，用哨兵替换各鉴权中间件，逐条请求 `r.Routes()`：
  - 凡经过 adminAuth 的路由，必须在目录中，并且链上有 `AdminPermission()`；
  - 新增一条未登记的路由，测试会失败；
  - 引擎不能有自定义的 NoRoute、NoMethod 兜底链，全局中间件只能是白名单里的那几个。
- **跨模块读取**：页面里的下拉选择（分组、代理、用户列表等）在目录行上用 any_of 登记，不新增接口。名单来自前端调用方扫描，不靠猜测。

为什么用一张表：
- 授权集中在一个可审查的地方；
- 上游同步引入的新路由必须在合并时归类；
- 运行时对未登记的路由失败即拒绝。

## 3. 失败即拒绝的中间件

**链路**：认证 → 限流 → 审计 → 权限 → 合规 → 处理器。三个组都挂载 `AdminPermission()`：admin 组、payment 管理组、`/api/v1/pages` 组。审计在权限之前，所以被拒绝的请求也会以 403 留下记录。

**adminAuth 具名补丁 `xy-admin-operator-scope`**
- 准入条件改为 `CanAccessAdmin()`；`IsAdmin()` 保持“超级管理员”语义。
- 普通管理员在每次请求时重新读取用户与权限行，构造范围放进 context，因此撤权在下一次请求生效。
- 权限读取失败时返回 503 `OPERATOR_ACCESS_UNAVAILABLE`，不放行。
- 超级管理员与全局 admin API Key 打上超级管理员标记，后者仍映射到第一个超级管理员。

**`AdminPermission()` 对普通管理员的检查顺序**
1. 用 `Method + FullPath` 查目录。`FullPath` 为空，或没有登记，返回 403。
2. 检查级别，依次为 `super_only`、模块级别或 any_of、开关。
3. 有目标或字段规则的行，先做规范请求检查，再跑目标规则和中间件字段规则。
4. 处理器运行，服务层规则在服务内执行（第 4 节）。
5. 响应按该行的脱敏规则改写（第 6 节）。

请求体上限 16 MiB。存储的权限 JSON 不合法时按空权限处理，并记一次日志。

**规范请求检查**：处理器用 `encoding/json` 绑定，它有三个宽松之处：
- 键名不区分大小写（还会做 Unicode 折叠）；
- 重复的键以最后一个为准；
- 只解码第一个 JSON 值，忽略后面的内容。

只按字面键名检查的守卫，可能被 `{"balance":null,"Balance":1000000}` 绕过。所以在有规则的行上，请求体必须满足：
- 是 `application/json`，内容是合法 UTF-8；
- 只有一个对象，后面没有多余内容；
- 同一对象里没有折叠后相同的两个键。

否则返回 400 `ADMIN_FIELD_FORBIDDEN`，`cause=noncanonical`。之后用 `EqualFold` 读键，读到的就是处理器绑定的那一个。

**`/me/access`**：注册在 admin 组之前的独立组，只挂 adminAuth 与限流。所以它不经过合规守卫和权限目录，前端在合规确认前也能加载菜单。`admin_compliance.go` 不改。

**WebSocket**：运维实时 QPS 握手时按目录检查 `ops` 查看权限。中间件包一层可取消的 context，每 30 秒重新读取权限，失去权限就取消，连接随之关闭。`ops_ws_handler.go` 属于 compat_invariant，不改。

## 4. 服务层钩子：资金、放置与凭据

资金、角色与放置规则放在服务里，不放在中间件，因为服务看到的是解码后的输入，以下情况才判断得准：
- 字段缺省时的默认值：默认余额、默认订阅、平台默认分组；
- 绑定之后才解析出的目标：`all: true`、按筛选条件批量；
- 处理器自定义的 `null` 含义：分组限额的 `null` 表示“取消限额”，中间件按原始请求体看会以为“没改”。

**钩子位置**：每处一行，调用 XY 自有的辅助函数。
- 账号：`CreateAccount`、`UpdateAccount`、`UpdateAccountExtra`、`BulkUpdateAccounts`（在筛选条件解析之后）、`RevertAccountProxyFallback`，以及 apply-OAuth 处理器。
- 用户：`CreateUser`、`assignDefaultSubscriptions`、`UpdateUser`、`BatchUpdateConcurrency`、`BatchUpdateLimits`、`BatchUpdateUserStatus`、`BindUserAuthIdentity`。
- 分组：`CreateGroup`、`UpdateGroup`、`DeleteGroup`、`AdminUpdateAPIKeyGroupID`、`ReplaceUserGroup`。

**资金**
- 没有 `users.balance_adjust` 时：
  - 新建用户把余额设为 0，跳过默认订阅；
  - 分组价格字段在服务自身的规范化（`normalizeLimit`）之后与原值比较。
- 价格字段与“自由字段”两份清单覆盖输入结构的全部字段。`TestOperatorGroupFieldListCoversInputs` 与 `TestOperatorGroupFieldListCoversUpdateRequest` 在出现未归类字段时失败，因为漏掉一个价格字段就等于放行。

**放置**
- 把账号加入或移出分组需要 `users.balance_adjust`。把高价账号放进便宜分组，效果等于降价。具体覆盖：
  - 账号的 `group_ids`；
  - 平台默认分组（新建时设 `SkipDefaultGroupBind`）；
  - 复制账号与 Spark 影子账号；
  - 分组的 `copy_accounts_from_group_ids`；
  - 删除仍有账号的分组：只走“必须为空”的删除路径。
- 没有开关时只能新建专属分组：新的公开分组对所有用户可用，价格又由创建者决定。

**凭据**
- **网络身份键**：地址键、`proxy_id`、`type`、`platform`、`api_protocol`、`account_mode`、`aws_region`。`TestOperatorNetworkKeysCoverBaseURLGetters` 解析地址、协议、模式与区域相关的取值函数，函数里读到的键不在清单中就失败。
- **补全再合并**：更新时，现有合并逻辑会用输入整体替换非敏感键。所以钩子先把输入里缺省的网络身份键补成原值，缺省永远不算修改。没有开关时，地址键提交 `null` 或空串也按原值补全，因为打码后的表单无法回传地址。
- **只用 Key 的账号**（`apikey`、`upstream`、`bedrock`）：网络身份一变，`SensitiveCredentialKeys` 中每个已保存的密钥都要填新值或清除。该清单在运行时读取，PR-B 的 `newapi_access_token` 与以后新增的密钥自动覆盖。没有重新提交的请求头值被丢弃。
- **可刷新的账号**（`oauth`、`setup-token`、`service_account`）：普通管理员不能修改其网络身份，有没有开关都一样。apply-OAuth 只能刷新令牌，不能换类型。原因是令牌刷新与缓存按账号 ID 存放，修改前已经开始的刷新或请求，可能把旧身份下的令牌写回。设计负责人选择了这条简单规则，没有增加缓存或传输层代码。
- **批量更新与 `UpdateAccountExtra`**：两者都无法逐个账号重填密钥，所以对普通管理员一律不改网络设置。
- **Bedrock 区域**：普通管理员提交的区域必须符合 `^[a-z]{2}(-[a-z]+)+-[0-9]+$`，这样主机名总在 `amazonaws.com` 之下。

渠道、渠道监控、代理、兑换码、风控配置等规则只看请求体与存储值，放在中间件的守卫里。

## 5. 显式调用者

第一版在 context 里没有普通管理员范围时跳过全部规则，于是用裸 context 调用受保护的函数就能绕过。现在受保护的函数要求以下三种调用者之一：
- 普通管理员范围：执行规则；
- 超级管理员标记：由 adminAuth 在 JWT 与 admin API Key 两条路径上设置，跳过规则；
- 系统标记：后台代码用 `operatoraccess.WithSystemActor(ctx, name)` 设置，名称不能为空，跳过规则。

三者都没有时，返回 503 `ADMIN_ACTOR_MISSING`，在第一次写入之前返回。

- context 键是包内未导出类型，请求头、查询参数和 gin 键都设置不了它。
- 这条规则按实例开启：`adminServiceImpl` 增加 `requireActor` 字段，由 `NewOperatorAccessService` 在生产实例上打开。现有约 220 处用 `context.Background()` 直接调用的单元测试不受影响，新测试显式打开。
- 当前 16 个受保护函数的 34 个调用点都在后台处理器里，传入的都是请求 context。链路测试会在某条路径丢失调用者时失败。以后新增后台调用者，必须用系统标记并在 PR 中说明。

## 6. 脱敏

- **位置**：脱敏是 `AdminPermission()` 内的响应改写，按目录行选择规则，只对普通管理员执行。DTO 映射不改，一处覆盖所有路由。
- **规则**：`account`、`api_key`、`redeem_code`、`promo_code`、`order_code`、`proxy_password`、`monitor`、`monitor_template`、`audit_body`、`ops_upstream_url`。各自的范围与解除开关见现行文档“保护措施”。
- **机制**：
  - 只缓冲 JSON 响应，数字按 `json.Number` 往返；
  - 处理器设置了 `ETag` 时，按改写后的内容重新计算，并由中间件自己回答 304；
  - 重新计算 `Content-Length`；
  - 改写失败返回 500 `ADMIN_REDACTION_FAILED`，绝不返回原文。
- **流式响应**：带 `account` 规则的 SSE 响应逐行改写并立即转发，不缓冲整个流。账号测试的处理器在调用服务前先声明 `text/event-stream`（一行改动），因为服务在设置响应头之前就可能写出错误事件。
- **错误详情**：没有 `accounts.upstream_address` 时，错误字段换成固定文字 `upstream error details hidden`，而不是逐个删除主机名。主机名清单仍会漏掉 TLS 证书名、重定向目标和不带协议的地址，固定文字什么都不会漏，也更容易测试。5xx 响应只做这一步。
- **操作日志请求体**：
  - 按 `EqualFold` 匹配键名，同一对象里的别名和重复键都会处理；
  - 无法解析的请求体（包括超过 16 KB 被截断的）整体隐藏；
  - 存储时的脱敏与审计中间件不改。
- **防止搜索还原**：被打码的值不能靠搜索逐字试出来。
  - 用户搜索对普通管理员不匹配 API Key；
  - 没有 `redeem_codes.issue` 时，兑换码只能按邮箱搜索，优惠码不能搜索；
  - 两者都不能按码排序。

## 7. 上游地址（D14）与模型映射（D12）

**D14：普通管理员什么时候能写地址**

代码事实：账号的出站路径都不检查实际连接的 IP。
- `security.url_allowlist` 只在请求前单独做一次 DNS 查询，拨号时会重新解析；连接池会缓存通过的主机；走代理时由代理解析。
- 只有渠道监控的 `safeDialContext` 在拨号时检查。

决定：
- 没有 `accounts.upstream_address`：普通管理员不能写任何地址、代理或其他网络字段，返回 403 `OPERATOR_NETWORK_CHANGE_FORBIDDEN`，`cause=address`。
- 有这个开关：可以在只用 Key 的账号和代理上写，但每个新地址要通过写入时的公网检查：
  - 用 `ValidateHTTPURL` 检查协议与字面地址，再用 `isPrivateOrLoopbackHost` 检查主机；
  - 解析失败按内网处理。

  同时要重填全部密钥，代理的“在用”与改主机需新密码两条规则照常生效。
- 可刷新的账号、批量更新与 `UpdateAccountExtra`：一律不能改。

理由：
- 开发文档把这个开关定义为“编辑弹窗显示接入地址”，第二版设计对所有普通管理员一律禁止，与之矛盾。
- 这个开关是超级管理员明确给出的信任授权。保存后 DNS 重绑定的风险只对持有者接受。
- PR-A 不加传输层代码。以后在 `HTTPUpstream`、共享 `httpclient` 连接池与代理拨号器里加拨号时的公网 IP 检查（受设置控制），覆盖全部出站路径后可以关闭这项风险。
- 渠道监控的地址不需要开关，因为每次连接都在拨号时检查。

**D12：模型映射不加开关**
- 账号的 `credentials.model_mapping`、分组的 `model_routing` 与组合路由决定由哪个上游模型服务请求。用户按请求的模型计费，所以没有价格字段被改动。
- 映射是日常运维（模型改名、回退）。影响更大的放置已经由开关控制。审查时这一项被核实者否决，astra 也没有提出。
- 记为已知限制。以后要收紧时：
  - 把 `model_mapping` 加入账号计费字段；
  - 把 `model_routing` 加入分组价格字段；
  - 组合路由在目录行上加开关。

## 8. 角色与 `IsAdmin` 调用点

- `IsAdmin()` 与前端的 `isAdmin` 继续表示超级管理员；新增 `CanAccessAdmin` / `canAccessAdmin` 表示“能进后台”。后端 34 处、前端 86 处逐一判定，不做全局替换。
- **改用 `CanAccessAdmin` 的**：
  - Backend 模式守卫、登录、刷新令牌与 Passkey 登录；
  - 用户自己的导出下载。
- **管理端导出下载**：在消费下载票据时重新读取权限，要求超级管理员，或有 `usage:view` 与 `data.export` 的普通管理员。
- **渠道监控 v2 的管理员视图**：按权限范围判断，不按角色。这些接口也挂在用户路由上，那里没有范围，普通管理员看到的是用户视图。
- **保持超级管理员语义的**：后台限流豁免、Server-Timing、TOTP、风控自动封禁、首次安装、`GetFirstAdmin`。
- **角色保护**：
  - 最后一个管理员保护与不能给自己降级，条件改为“从 admin 改成任何其他角色”；
  - 新建特权角色，以及涉及 admin 或 operator 的角色变更，需要二次验证；
  - 普通管理员提交 `role` 一律拒绝。

## 9. 叠加在 PR-B 上

- **分支**：PR-A 从 PR-B 的分支（`feat/upstream-balance-20261027`，PR #158）拉出，PR 描述写明依赖。开 PR 前 rebase 到 PR-B 或已合入 PR-B 的 main 上。
- **PR-B 的接口与凭据**：
  - 余额读接口 `GET /accounts/upstream-balances` 是目录中的一行，属于账号查看；
  - PR-B 新增的敏感键 `newapi_access_token` 自动纳入“重填全部密钥”规则；
  - PR-B 自己的“站点或代理变化时删除令牌”逻辑保留，在 PR-A 的钩子之后执行。
- **余额轮询**：前端在收到 403 后停止，权限恢复后才继续。
- **迁移编号**：main 已有 `281_iq_targets.sql`，本变更用 282。
- **同步文件**：`policy.json` 与 `UPSTREAM_BASE.json` 两个 PR 都要改，PR-A 一律追加到数组末尾，不依赖行号。

## 10. 同步登记

- **具名补丁** `xy-admin-operator-scope`：
  - 在 `tools/upstream-sync/policy.json` 与 `UPSTREAM_BASE.json` 中登记；
  - custom_patch_tests 绑定目录、中间件、调用者、账号、用户与分组规则的测试，以及前端规格测试。
- **required_literals**：覆盖 adminAuth 的两行与超级管理员标记、三处挂载、`/me/access` 与权限路由、每个服务钩子、各 `CanAccessAdmin` 调用点、wire 注入。
- **`manual_merge` 新增**：
  - 后端：`server/middleware/admin_auth.go`、`server/routes/admin.go`、`server/middleware/backend_mode_guard.go`、`handler/admin/proxy_handler.go`、`handler/admin/user_handler.go`、`handler/auth_handler.go`、`service/admin_user.go`；
  - 前端：被修改的上游原样文件。
- **`xy_owned`**：本 OpenSpec 目录。
- **文档**：`docs/ADMIN_PERMISSIONS.md` 登记到 `docs/README.md` 与 `.gitignore` 白名单。
- **前端规格测试**：加入根 `Makefile` 的 `FRONTEND_CRITICAL_VITEST`。

## 11. 风险

现行文档“已知限制”逐条说明了已接受的风险。摘要：
- 超级管理员修改 OAuth 类账号网络设置时的令牌刷新竞态（原有，普通管理员无法触发）；
- 模型映射（D12）；
- 开关持有者保存地址后的 DNS 重绑定（D14）；
- 超级管理员不受 Bedrock 区域格式约束；
- 账号测试显示上游正常回复，个别字段里可能残留主机名；
- 运维监控错误详情显示上游地址；
- 账号自身倍率；
- 绑定真实第三方身份的首次绑定赠送；
- 原代理被超级管理员删除后复制回退中的账号。
