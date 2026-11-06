# 设计

## 1. 进程与身份

远程 MCP 使用官方 Go SDK v1.8.0 与已验证兼容的 Streamable HTTP revision。独立 internal/mcp transport 和 tool registry 由 Wire 注入窄 reader interfaces；不复用 admin handler、global Admin API Key、admin session JWT 或 user API Key。

身份验证绑定 MCP 自身 OAuth resource：本地 OAuth AS 使用成熟引擎实现 Authorization Code + PKCE S256、public client、精确 redirect/resource 绑定、one-time code、refresh rotation/reuse detection、revocation 与 discovery metadata；token 使用专用 opaque/resource-bound strategy，不复用站内 JWT secret。resource server 校验 issuer、audience/resource、expiry/revoke、scope，并将不可变 subject 映射到本地 active XY2API user。对 opaque token 按可信 store 检查 resource/签发来源；对未来 JWT 才检查 iss/aud/signature。拒绝未绑定本 MCP resource 或其他用途 token。Root/Operator/User 通过当前用户与 operator 权限记录派生；不得信任客户端 role claim。tools/list 与每次 tools/call 均在线复核，承诺撤权后下次请求生效，不缓存 permission allow。

## 2. Tool 级 ACL

每个工具均有固定 ToolPolicy 项，关联确切 operatoraccess catalog Entry key(s)、source/module/level、AnyOf/flags、target、Fields、service-enforced rules、redaction 和输出 DTO allowlist。OperatorAccessService / operatoraccess.Decide 与 target Guard 可复用；不能假设 Gin AdminPermission writer 已执行。Service context 带当前 Operator Scope，以遵守 HideAPIKeySearch 等已有规则。

Strict 复现单一路由时保留完整原 Entry 语义，包括 OR 型 AnyOf。跨多个独立 route/source 新聚合时，对每个 Entry 独立 Decide 并全部通过；无匹配 entry 的 operator 数据 deny by default。准确表见 docs/READ_ONLY_MCP_DESIGN.md “v1 Tool × ACL × source/output 静态映射”。

普通 User 的 tool owner ID 只从验证的本地 user subject 注入。me 工具保留 backend mode 与 user error view gate，使用可返回 error 的窄配置读取，失败拒绝；admin 工具保留已有 compliance gate。operator 管理 tool 的 target Guard 继续保留 role=user 限定，不能因 root actor 跳过 MCP tool allowlist。

## 3. 数据与关联

Reader 只投影固定的 Request/User/Account/Group/Ops 字段，不返回完整 Ent/domain 对象。跨 source 结果保留 source id、source request_id、client_request_id、billing request_id，不允许通用去前缀。Tool result 带来源、UTC window、filter、coverage/retention/watermark 与 relation exact/corroborated/candidate/partial/conflict。

Requests search 默认读取 `/ops/requests`；显式 `include_recovered_upstream` 时再逐项授权并读取 `/ops/upstream-errors`，以发现成功请求中的 recovered upstream row。两个 source 的 source id、request id 和结果类型分别保留；attempts 工具只按 search 返回的数值型 error_row_id 查上游尝试，不能把 request/client ID 当成错误行 ID。多个 source 同时出现时才按已验证的证据规则报告关联，只有持久化证据支持 retry/fallback 成功时才标 recovered，否则 unknown；近似匹配不能跨主体合并。

## 4. 只读

业务 query 使用独立 PG read-only identity、显式 SELECT projection、parameterized filters、固定 order/limit、window cap 和 statement_timeout。MCP reader 不持业务 writer interface。

Redis 读入口先按 command capture 审核。允许审查确认的 bounded peek，如 PeekAccountsLoadBatch / GetAccountGroupConcurrencyBatch / UpstreamBalanceService.ReadBalances；不使用 GetAccountConcurrencyBatch/GetAccountsLoadBatch（会移除过期成员）或现有 ShareSchedulingService.Explain/Preview（会清理 slots、更新 eligibility/health 或发布 scheduler snapshots）。

不调用网络型 account usage active probe、上游账号 OAuth refresh、channel monitor run/test、余额 probe、账号 test、IQ、model sync、quality evaluator、rebalance。

## 5. 字段、scope、审计

诊断 DTO 默认排除 error_body、自由 upstream message/detail/JSON、system message/extra、API key、header、URL/userinfo、client IP/email、请求/提示词正文。System log catalog 未给 text redaction，MCP 不能沿用“已有 ops scope”来输出自由文本。v1 永不把完整敏感正文或 JSONB 读入应用或返回；attempts 在 SQL 内投影安全 scalar。

如将来增加自由文本读取，必须有单独 scope、field allowlist、redaction 与持久审计；写入同一 MCP audit store 成功后才执行正文 SELECT，写失败则拒绝。当前版本不增加 sensitive-text scope。

访问审计按 actor、credential fingerprint、client、tool、权限版本、filter 摘要、行数/耗时、status 记录；不存全文、Bearer token 或 payload。Root也使用其明确 OAuth host grant 与 MCP allowlist。

## 6. 工作包

2026-11-05 已按用户授权完成29工具Reader、OAuth控制面和前端；本地验证与两路审查证据见 tasks.md，生产部署及目标环境验收尚未执行。工作包与主方案统一：WP0 源码与协议预检；WP1 身份与只读底座；WP2 管理诊断工具；WP3 本人工具与授权界面；WP4 可运维与必要验收；WP5 发布准备。依赖为 WP0 → WP1 → WP2/WP3 → WP4 → WP5。

全部后端 gpt-6.1-sol / xhigh；实施按用户最新指令由 gpt-6-astra / xhigh 分交付核对与安全红队两路审查；PM 监督契约、进度和缺陷闭环。PAT、高敏正文、share/resilience explain、统一 trace 迁移、影子模式和泛化测试平台不在本方案首版范围。发布与生产写操作待后续明确授权。
