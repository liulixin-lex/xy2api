# Tasks — 只读 MCP 开发计划

2026-11-05 用户已明确授权开始实施；未勾选条目仍未完成。PM 维护契约和集成；所有后端由 gpt-6.1-sol / xhigh 执行；按最新要求，gpt-6-astra / xhigh 分执行进度审查和安全红队两个方向。此前 WP0 的单路方案签字保留为历史事实。

## WP0 源码与协议预检

- [x] 0.1 核对 catalog/middleware/service ACL、owner、request ID 与 Redis/HTTP 副作用。
- [x] 0.2 生产只读限时聚合抽样，确认 billing/client/ops ID 差异及最终错误与 recovered attempts 的口径；不保留原始生产行或标识。
- [x] 0.3 核对官方 MCP Go SDK 与现有登录；确认项目无 OAuth AS，候选 OAuth 库的原子消费仍需实现验证。
- [x] 0.4 补齐静态 tool/source/ACL/output、运行开关、用户工具 owner 与敏感正文边界。
- [x] 0.5 唯一路线 gpt-6-astra / xhigh 方案签字通过：P0/P1/必要 P2 均为0；仅规划签字，不等于代码测试通过。

## WP1 身份与只读底座 — gpt-6.1-sol / xhigh

- [x] 1.1 锁定官方 MCP Go SDK；Streamable HTTP stateless handler，不自写 JSON-RPC。
- [x] 1.2 OAuth 库与目标 Host 最小兼容验证，确认 code/PKCE/refresh 的完整事务和并发单次消费。
- [x] 1.3 OAuth AS：public client、Authorization Code + PKCE S256、精确 redirect/resource、逐 client consent、opaque token、refresh rotation/reuse detection/revoke、metadata。
- [x] 1.4 每次请求 fresh token/grant/user/role/operator Scope，保留 token_version、设置 gate 与 admin compliance；拒绝全局 Key、站内 JWT、其他 resource token。
- [x] 1.5 静态 ToolPolicy：精确 catalog key、AnyOf/flags、Targets/Fields/service-context、安全投影；list 过滤默认权限，call 校验参数/target/可选源。
- [x] 1.6 独立 PG Reader login/具名 DB、READ ONLY transaction、固定 SELECT/超时；禁用时不建连接，不回退 writer DB。

## WP2 管理诊断工具 — gpt-6.1-sol / xhigh

- [x] 2.1 requests.search 默认 /ops/requests；显式 include_recovered_upstream 时额外授权 /ops/upstream-errors；provider-health 不等于 recovered；同一存储行跨源去重后总页≤100，保留 source/ID/结果语义。
- [x] 2.2 detail/attempts 仅数值型 error_row_id；attempts 固定 JSONB 安全投影，recovered 行额外授权；不查询完整 JSON/正文，不推断最终 outcome。
- [x] 2.3 accounts.search/health、groups.search/usage_summary：固定字段、被动健康/余额，无 active probe。
- [x] 2.4 独立 ops concurrency snapshot：ops:view、先检查运维总开关/实时开关，关闭时不查账号/Redis；≤100 IDs、无清理 peek，错误不伪装为零。
- [x] 2.5 ingress、ops trend/breakdown/realtime/alert/system metadata、channels metadata/metrics、quality state：遵守静态映射与入口开关，不运行检测/重算。
- [x] 2.6 users search/usage/subscriptions：AnyOf OR、user_search context、target=普通 user；audit/prompt audit 仅安全元数据。
- [x] 2.7 source coverage/retention/watermark、签名 cursor、窗口/limit/response cap；候选关联不当 exact trace，不注册空壳。

## WP3 本人工具与授权界面

- [x] 3.1 后端七个 me 工具：owner=actor、Key ID 验属主、异主/不存在统一 NotFound；backend mode/user error view gate，设置读取出错拒绝。
- [x] 3.2 订阅/配额只读及纯内存规范化，不调用窗口维护/reset/刷新。
- [x] 3.3 最小 consent UI：后端校验的 client/redirect/resource/工具集合及数据外发告知；同意/拒绝、CSRF/Origin、登录返回、防 iframe。
- [x] 3.4 本人 connections/revoke UI 与中英文文案；不显示 token，不允许代他人授权。

## WP4 可运维与必要验收

- [x] 4.1 控制面与 Reader 分离；有界审计/drop 计数、限频/并发、取消与响应大小限制；身份响应 HTTP private/no-store、SDK private/ttl=0，工具过滤不改共享 registry。
- [x] 4.2 root/operator/user、AnyOf/flags/target/Fields/owner、在线撤权、gate/fail-closed 的表驱动安全矩阵。
- [x] 4.3 只读 PG 角色与 Redis 命令捕获证明无业务写/清理；网络 stub 无上游 probe；canary 无敏感原文。
- [x] 4.4 本地真实 PG fixture 验证结算与最终失败双行、recovered、来源缺失、分页/时间界限/取消；只按已证明的存储行关系返回，不推造跨源唯一请求数。生产规模的索引/执行计划验收列入 5.2。
- [x] 4.5 OAuth PKCE/resource/redirect/replay/refresh/revoke、官方 SDK roundtrip、至少一个拟支持真实 OAuth Host；如实登记未验证项。
- [x] 4.6 Astra 两方向代码/执行审查均无未解决 P0/P1；修复后针对性复测。
- [x] 4.7 按实际改动完成仓库必要门禁、迁移 checksum 与 docs consistency；不加影子模式/泛化测试平台。

## WP5 发布准备

- [x] 5.1 配置/反向代理/只读 role/迁移/保留/回退 runbook；默认关闭，不改网关热路径。
- [ ] 5.2 在目标部署核对 schema/只读角色、完整登录与代理链路、指定商业 Host、生产规模 query budget/执行计划；本地证据已齐，目标环境验收尚未执行，不能宣布已上线。
- [x] 5.3 已按2026-11-06授权推送、创建并合并PR #199、正式发布v0.5.4；标签指向2356b8b200eb567defdd6fe3e3939d5a5ec017e2，Release运行42083934644成功，5平台包/SHA256与双架构镜像已核验。
- [ ] 5.4 生产迁移、只读角色配置及启用待独立部署窗口执行；本轮发布授权不含生产部署。

## 逐工具实现核对

以下仅登记真实 Reader 执行证据；整体授权、Host 互操作和审查签字仍由 WP1/WP4 独立验收。共 22 个管理诊断工具、7 个本人工具；权限与输出不另复制，统一以设计静态表和代码 ToolPolicies 为准。

证据：`TestReaderPostgresReadOnlyToolMatrix` 在全迁移的一次性本地 PostgreSQL 上以独立、仅安全列 SELECT 账号逐工具执行；同时验证敏感 canary、owner、分页及只读 Redis。2026-11-05 Reader/Runtime suite 在同库连续执行两次通过（2.534s），完整 MCP 实库 suite 另见下方记录。

| Tool | 实现与真实数据源查询 | 最终授权/代码验收 |
|---|---|---|
| `xy2api.requests.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.requests.get_detail` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.requests.get_attempts` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.requests.search_ingress_rejections` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.accounts.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.accounts.get_health` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.groups.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.groups.get_usage_summary` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.channels.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.channels.get_metrics` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_error_breakdown` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_trends` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_alerts` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_system_events` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_realtime_snapshot` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.ops.get_concurrency_snapshot` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.routing.get_quality_state` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.users.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.users.get_usage_summary` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.users.get_subscriptions` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.audit.search` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.security.search_prompt_audit` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_profile` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.search_usage` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_usage_detail` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_error_history` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_error_detail` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_subscription_status` | 通过本地矩阵 | 见 WP1/WP4 |
| `xy2api.me.get_quota_status` | 通过本地矩阵 | 见 WP1/WP4 |

## 实施验收记录

- 2026-11-05，交付方向 Astra：usage 双行口径、真实分类、legacy 模型、日桶与 retention 问题均已关闭；29 个 policy/description/UI label 名称集合一致，产品代码范围 P0/P1=0。为静态增量复核，不代替运行证据。
- 2026-11-05，安全方向 Astra：只读身份、refresh downscope、匿名限流、认证前准入、控制面存量/清理、20-grant 并发、重复闸和外键级联越批问题均关闭；当前审查范围 P0/P1/必要 P2=0。为源码审查，不等于生产无漏洞保证。
- Reader：完整迁移的一次性本地 PostgreSQL、仅安全列 SELECT 登录、29-tool matrix；owner/敏感 canary/游标/日桶/双来源用量错误/分类/模型/retention 通过。合成 fixture 幂等性修复后同库连续 `-count=2` 通过（2.534s）。Redis 捕获仅 TIME/ZCOUNT/GET/ZRANGEBYSCORE/MGET，存储字节与过期 slot 不变。repository 错误吞并、service 纯快照定向通过。
- 前端：全量 lint/typecheck/生产 build 通过；项目关键集合 115 文件、1,282 测试通过；MCP 16 项流程测试通过。真实 Chromium 在桌面/390px 手机检查授权和连接页，无横向溢出及 page error。沿用既有界面风格。
- 官方 MCP Inspector **2.14.0** + Node **24.22.1**：本地 TLS fixture 使用真实 NewServer/PG OAuth 与合成普通用户、safe query fixture，DCR → PKCE S256/resource → 浏览器预览/同意 → loopback callback → tools/list 的全部 7 个 me 工具 → 新会话 tools/call `xy2api.me.get_profile` 均通过（schema_version=1、isError=false）。只信任该临时自签 CA，未禁用 TLS 验证；OAuth secret 仅内存、不写产品配置。Schema 检查 0 errors/49 portability warnings（7 工具），经离线复用 Inspector 检查器确认均为 SDK 输出 schema 的合法 nullable type 数组提示，部分单一 type 方言可能不兼容，未阻断 Inspector 发现/调用。过程中发现并修复 application_type 兼容与 required:null；不得把此前仅2工具的首次结果当通过。
- Host 边界：Inspector 的浏览器授权使用 integration-only consent fixture，产品页面另有组件和浏览器验证；尚未在生产完整登录/代理链路或用户指定商业 AI Host 上验收，不宣称通用兼容。后续上线核对仍见 WP5。
- 后端：MCP/config/handler/server/cmd 五包、service/repository 两包分别 lint 通过（0 issues）；服务二进制构建、接线/清理、config、纯质量快照与 Redis peek 回归通过。完整本地 MCP 实库 suite 通过（15.854s），新增并发 refresh 回归通过（0.781s），AnyOf/flags/target 委托与 token_version 在线撤权回归通过（0.840s）。2026-11-06 发布阶段又完成远端全仓 unit/integration、golangci-lint、网关 reliability/resilience、真实 Redis 缓存和支付 PostgreSQL 回归。
- CI：复用现有 PostgreSQL service 建独立 `mcp_readonly_test` 数据库及合成测试 writer；MCP 专用步骤要求 12 项关键测试全部通过、所选范围 0 skip/fail，排除手动 Inspector fixture。Workflow YAML 已解析通过；从 workflow 原样提取的本地等效命令与 JSON 断言通过：65 个 tests/subtests、12 个必跑关键测试全部通过、0 skip/fail。2026-11-06 PR #199 的 push/pull_request 两路 CI 与安全扫描共20项检查全绿；PR CI运行42078746808、push CI运行42078709061，专用MCP门禁均确认12项关键测试PASS、0 SKIP。固定head `0335f39d6633e6cfb2aab75fcfc55a86620866b8` 后合并，未绕过分支保护。
- 已通过 `python3 tools/check_docs_consistency.py`、`git diff --check`、`sync.py scheduler-diff` 与 upstream-sync 工具 15 项单测；迁移 290/291 checksum 一致。最终代码的独立临时干净快照 `sync.py audit` 通过；首次审计发现新 OpenSpec 目录缺归属登记，已按现有方式只补该目录的 xy_owned 规则后复验。主分支 HEAD/索引不变，临时审计工作树已删除。

## 交付状态

WP1–WP4 与部署 runbook 已完成；2026-11-06 经用户授权推送并合并 PR #199，合并提交 `2356b8b200eb567defdd6fe3e3939d5a5ec017e2`。MCP 默认关闭，WP5.2 的目标环境验收及 WP5.4 的部署/启用尚未执行。v0.5.4 标签指向该合并提交，发布流水线42083934644成功；正式Release非draft/非prerelease且为latest，5原生包SHA256/归档内容、Linux amd64版本和GHCR双架构version/revision/latest均核验通过。本轮没有连接生产；一次性 PostgreSQL 容器、临时 UI fixture 和构建验证二进制已清理，不影响其他任务资源。
