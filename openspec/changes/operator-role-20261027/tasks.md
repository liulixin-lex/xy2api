# 任务：普通管理员角色与逐人权限

> 一个 PR（`feat/operator-role-20261027`），叠加在 PR-B #158（`feat/upstream-balance-20261027`）之上，PR 描述写明依赖 PR-B。开 PR 前 rebase 到 PR-B，或已合入 PR-B 的 main。
> - 提交分层：记忆登记 → 后端四块各一个提交 → 前端 → 文档、OpenSpec 与同步登记。
> - 分工：设计与集成为 Claude（Opus 5.5）；后端开发为 Codex（gpt-6.1-sol）；前端开发为 Claude（Opus 5.5）；审查为 Codex（gpt-6-astra）。
> - 不改 `VERSION`，不发版，不部署。

## 1. 契约与登记

- [x] 1.1 契约定稿：
  - 开发契约四个修订版与逐条路由的权限目录（509 行）；
  - 三轮设计审查，第三轮没有 P0、P1；
  - 未决问题 D1–D14 按推荐答案执行，已接受风险 E1–E9 写入现行文档。
- [x] 1.2 项目记忆：按归档规范原样归档已结束的记录；登记 `20261027-operator-role`。
- [x] 1.3 OpenSpec（本目录）与 `docs/ADMIN_PERMISSIONS.md`，登记到 `docs/README.md` 与 `.gitignore`。
- [x] 1.4 同步登记：
  - `policy.json`：具名补丁 `xy-admin-operator-scope`、required_literals、custom_patch_tests，`xy_owned` 与 `manual_merge` 两项分类；
  - `UPSTREAM_BASE.json`：`custom_patches`。

## 2. 后端

- [x] 2.1 块 1：存储、角色与权限包。
  - 迁移 282 与 `checksums.json`；`RoleOperator`、`CanAccessAdmin`；
  - `operatoraccess` 包：目录、判定、范围、调用者、脱敏、规范请求检查，以及两份快照；
  - 仓库层与 `OperatorAccessService`：加载范围、权限读写与审计差异、`/me/access` 数据、打开 `requireActor`。
- [x] 2.2 块 2：认证、权限中间件与字段规则。
  - adminAuth 具名补丁与 `setOperatorScope`；三处 `AdminPermission()` 挂载；
  - 目标与字段规则守卫、写入时地址检查；
  - 脱敏写出，含 SSE 事件模式与账号测试的流类型声明；
  - 重新生成 `wire_gen.go`。
- [x] 2.3 块 3：服务钩子、`IsAdmin` 调用点与 WebSocket。
  - 账号、用户、分组的服务钩子与调用者规则；
  - 34 处 `IsAdmin` 调用点：Backend 模式、导出下载、渠道监控 v2、角色保护与二次验证；
  - 用户搜索不匹配 API Key；WebSocket 每 30 秒复核。
- [x] 2.4 块 4：接口、路由枚举与链路测试。
  - `/me/access` 与 `operator-permissions` 接口、路由与 wire；
  - 生产引擎上的路由枚举、兜底链与全局中间件白名单测试；
  - 每条规则的允许、拒绝与边界用例，处理器到服务的链路测试；
  - 原因码与 metadata 对照；`api_contract_test.go` 快照不变。

## 3. 前端

- [x] 3.1 类型与接口、`stores/adminAccess.ts`、auth store 的 `canAccessAdmin` 与 `isSuperAdmin`；`adminSettings` 对普通管理员改读 `/me/access`。
- [x] 3.2 路由 meta 与守卫、侧边栏与命令面板过滤、403 时刷新权限，余额轮询收到 403 后停止。
- [x] 3.3 页面：
  - `useAdminPermission()` 与各页只读模式、超级管理员专属控件；
  - 账号页按上游地址开关处理地址与代理，OAuth 类账号只读，另有批量编辑、分组与计费字段的限制。
- [x] 3.4 用户弹窗与权限表、用户列表的角色筛选与标识、普通管理员的合规确认；86 处 `isAdmin` 逐一判定。
- [x] 3.5 zh/en 文案；规格测试，并加入 `FRONTEND_CRITICAL_VITEST`。

## 4. 审查与验证

- [x] 4.1 astra 审 diff，对照开发文档 §8 的 PR-A 清单；修到没有 P0、P1 为止。
- [ ] 4.2 本机门禁：
  - 后端：`go generate ./cmd/server` 后无差异、build、vet、unit、`-tags=unit`（API 契约快照不变）、golangci-lint；集成测试（迁移回退与重新升级、查询仓库）；`-tags embed` 的嵌入前端测试；
  - 前端：`make test-frontend`；
  - 文档一致性、`sync.py audit`。
- [x] 4.3 本机联调：
  - 本地 PostgreSQL 与 Redis；
  - 浏览器检查超级管理员，以及只查看、有管理权限、各开关打开或关闭的普通管理员：菜单、只读页、权限表、地址开关与合规弹窗；
  - 截图留在仓库外。
- [ ] 4.4 推送并开 PR，写明依赖 PR-B #158，等 CI 全绿。合并、发版、部署另行授权。
