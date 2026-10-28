# 设计：账号页「上游余额」列（2026-10-27）

> 管理员看到的行为、接口与原因表以现行文档 [`docs/UPSTREAM_BALANCE.md`](../../../docs/UPSTREAM_BALANCE.md) 为准。本文记录实现结构与取舍，文件路径相对仓库根目录。
>
> 依据：仓库外的开发文档与两份契约。GPT-6-astra 与 Claude 分别调研并两轮互审，前后端契约各经一轮对抗式评审，最后由 astra 审设计。

## 1. 结构

| 层 | 文件 | 职责 |
|---|---|---|
| 服务 | `backend/internal/service/upstream_balance.go` | 生命周期、每轮调度、读接口组装 |
| 服务 | `backend/internal/service/upstream_balance_target.go` | 适用判断、官方判断、身份指纹、地址拼接、站点规范化 |
| 服务 | `backend/internal/service/upstream_balance_sub2api.go`、`upstream_balance_newapi.go` | 两类上游的请求与解析 |
| 服务 | `backend/internal/service/upstream_balance_store.go` | 存储接口与记录类型 |
| 仓库 | `backend/internal/repository/upstream_balance_store.go` | Redis 实现 |
| 处理器 | `backend/internal/handler/admin/account_upstream_balance.go` | `GET /accounts/upstream-balances` |
| 前端 | `frontend/src/components/account/UpstreamBalanceCell.vue`、`NewApiWalletSection.vue`、`newApiWalletCredentials.ts` | 单元格、令牌小节、凭据组装 |

`service` 与 `handler` 包按 lint 规则不能直接引用 go-redis，所以存储接口定义在服务层，实现放在仓库层（与 `proxy_latency_cache.go` 相同）。

## 2. 为什么只用 Redis

`accounts.extra` 每次写入都会更新 `updated_at` 并发出调度 outbox 事件 `AccountChanged`（`repository/account_repo.go` 的 extra 写入路径），每分钟写一次会让调度快照不停重建。余额是可丢的观测值，所以只放 Redis：

- 成功记录保留 24 小时；
- 尝试状态保留 48 小时；
- Redis 不可用时停采。

## 3. 每轮调度

- **轮锁**：固定键，经 `LeaderLockCache` 获取，TTL 58 秒，不主动释放。这样全集群相邻两轮至少相隔 58 秒，且与各实例的时钟无关。
  - 没有使用通用的 `tryAcquireSingletonLeaderLock`，原因有二：它在任务结束时立即释放，多个实例错开的计时器会在同一分钟内重复采集；它在 Redis 出错时退回 PostgreSQL 咨询锁，而此时唯一的存储已不可用。
- **到期判断**：每个账号的下次到期时间以本轮开始时刻为基准，留 5 秒余量。排在后面完成的账号不会因此退化成两分钟一次。
- **执行限制**：
  - 每轮预算 50 秒，并发 4；
  - 单次请求 10 秒，单个账号 25 秒；
  - worker 用 recover 隔离 panic。
- **候选账号**：每轮用现有的 `ListAllWithFilters` 列出 API Key 账号，再在 Go 中过滤状态，不改动上游所有的 `account_repo.go`。

## 4. 请求组装

请求方式与上游计费探测（`upstream_billing_probe.go` 的 `probeLoadedAccount`）一致：

- 账号代理、TLS 指纹 profile、自定义请求头、禁止重定向；
- 单次尝试；
- 响应上限 64 KB（`/api/status` 为 1 MiB）。

地址规则见现行文档“识别与读取”。`buildOpenAIEndpointURL` 会把 `/v1beta` 与 `/v1/usage` 拼成 `/v1beta/usage`，也会把 `/api/*` 拼到 `/v1` 之下，因此不复用它。

## 5. 识别与限流

- 识别结果按账号缓存，`/api/status` 的换算参数按站点缓存。
- new-api 的 `/api/usage/token/` 与登录、注册共用每 IP 的严格限流桶，因此：
  - 只在判别 401 时调用；
  - 同一站点至少间隔 75 秒（`SET NX` 锁）；
  - 另外每站点每轮最多 40 次请求，超出的账号顺延到下一轮。
- **不做 billing 口径比对**。曾考虑用 `/api/usage/token/` 比对 billing 返回的是 Key 剩余还是钱包，但数值相等或不等都不能证明来源，还会多消耗严格限流桶。因此只填 Key 时口径为未知；填了访问令牌时，由 Key 剩余与钱包的大小关系确定口径。
- **限流**：
  - new-api 任一接口返回 429 后，在 Redis 写入站点冷却截止时间，所有实例与账号都遵守，嵌套的 401 判别调用也不例外；
  - sub2api 的 429 只冷却该账号；
  - 限流与退避的截止时间单独保存，不享受正常周期的 5 秒余量。
- **不限额**：以 TOKENS 显示额度的站点，不限额哨兵 `100000000` 与恰好等额的有限额度无法区分，所以不据此显示 `∞`。
- **鉴权判断**：
  - 401、403 只有在响应体是 JSON 时才判为鉴权结果，WAF 拦截页按上游错误处理；
  - 公开的 `/api/status` 从不导致“暂不支持”。
- **日志**：采集服务只记录分类、状态码、账号 ID 与耗时，不记录原始错误、URL、站点、代理或请求头。传输层原有的 Debug 日志会带出代理地址，它对所有网关请求都存在，不在本变更处理，作为后续事项。

## 6. 身份指纹

**组成**：平台、类型、实际接入地址、API Key、访问令牌、用户 ID、代理 ID、代理连接信息摘要（地址与账号密码），以及生效的自定义请求头（与上游计费探测的身份字段相同）。

**校验**：
- 写入时记录指纹，读取时用当前账号重新计算，不一致就视为无值；
- 修改其中任一项后旧值不再显示，包括原地修改代理；
- 账号更新路径不需要主动清理 Redis。

## 7. 令牌安全

- `newapi_access_token` 加入 `SensitiveCredentialKeys`。DTO 脱敏、审计日志脱敏与合并保留都由这一个列表驱动。
- 合并函数 `MergePreservingSensitiveCreds` 不改：
  - 提交 `null` 时值存为 nil；
  - `GetCredential` 与 `has_*` 都把 nil 当作没有；
  - 该文件与上游逐字节一致，所以只加一行。
- 单个账号更新时，如果实际接入地址的站点变了，或代理变了（设置、更换或取消），且请求没有带新令牌，在 XY 自有的辅助函数里删除已存令牌。
- 批量更新对所有选中账号应用同一份凭据增量（JSONB 合并），删掉增量里的键无法清除已存的值。处理方法：对每个选中账号模拟合并，只要有一个账号的实际站点改变（包括只切换协议）或代理改变，且增量里不含令牌，就在增量里显式写入 `newapi_access_token: null`。
- 复制账号不带令牌。
- 这些规则防止 v0.x 的全权限令牌被带到新的主机。
- 调度缓存会把账号完整凭据明文存进 Redis。这是现有行为，只在文档中说明。

## 8. CN 余额检查

- 在 `validatePayGAccount` 里拒绝中转的 kimi、deepseek 账号，原因码为 `CN_BALANCE_RELAY_ACCOUNT`。这同时覆盖定时检查、手动查询和 `QueryBalanceForAccount`。
- 渠道监控对这类账号按“无余额接口”处理。
- 官方判断与前端 `officialAccountAPISupported` 一致，并用同一组表格用例对齐。

## 9. 前端

- **读取**：
  - 列表按当前页账号 ID 分批读取，每批不超过 200 个，每 30 秒一次；
  - 弹窗或菜单打开、页面隐藏、列被隐藏时不读取；
  - 分页、筛选或 ID 集合变化，弹窗关闭，页面重新可见，列重新显示时立即读取。
- **时钟**：用响应中的 `server_time` 校正本机时钟差。
- **单元格**：不提供手动刷新。
- **令牌小节**：放在 API Key 输入框之后，默认折叠，每行一个控件。
- **类型**：定义在 XY 自有的 `api/admin/accounts.ts`，不改动上游的 `types/index.ts`。

## 10. 同步登记

- 具名补丁 `xy-upstream-balance`：在 `tools/upstream-sync/policy.json` 与 `UPSTREAM_BASE.json` 中登记，并给出绑定测试。
- required_literals：覆盖路由行、wire 注入、敏感键、账号更新钩子、CN 中转判断、前端列定义与令牌小节。
- `service/account_credentials_redact.go` 改动后与上游不再一致，加入 `manual_merge`。
- 本 OpenSpec 目录加入 `xy_owned`。
- 文档登记：`docs/UPSTREAM_BALANCE.md` 加入 `docs/README.md` 与 `.gitignore` 白名单。
- 新的前端规格测试加入根 `Makefile` 的 `FRONTEND_CRITICAL_VITEST`。

## 11. 风险

- sub2api 的 `/v1/usage` 每次都会对该 Key 的全部用量记录做汇总。响应超过 3 秒时，自动降到每 5 分钟一次。
- new-api rc 版的个人访问令牌（PAT）每次调用都会在上游写审计记录。v0.x 的令牌等于整个上游账号的权限。表单提示与文档中已写明。
- 只填 Key 的 new-api 账号，口径一律显示「未知」，因为站点关闭「令牌统计」后 billing 返回的是钱包。
- 以美元或人民币显示额度的 new-api 站点，如果有限额度恰好等于 1 亿美元，会被显示为不限额。这种情况实际不会出现，接受此残留风险。
- 回退到旧版本时，旧版本会在接口中返回令牌明文，需要先清除令牌。
