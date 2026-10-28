# 上游余额

账号管理页的「上游余额」列显示每个上游 API Key 账号在上游站点上还剩多少额度。

- 后台每分钟采集一次，页面每 30 秒读取一次采集结果。
- 支持两类上游：sub2api（含 XY2API）与 new-api。
- 没有设置开关，符合条件的账号自动生效。

采集结果只存在 Redis 中：

- 不写 `accounts` 表，不触发调度重建；
- 读接口只读缓存，不会因为打开页面而向上游发请求。

## 适用账号

同时满足以下条件的账号会被采集：

- 类型是 API Key，平台是具体平台；
- 已填 API Key；
- 接入地址不为空，且通过出站地址校验。

官方域名的账号显示「不适用」，例如 OpenAI、Anthropic、Gemini 等官方 API。判断官方的方式与上游计费列相同。

kimi 与 deepseek 的判断规则与账号页「用量」列的 CN 余额子单元格完全一致：接入地址是 `https`，且主机名等于平台默认地址的主机名，才算官方。CN 自适应协议账号按 Chat Completions 的实际接入地址判断。

接入地址不是官方域名的 kimi、deepseek 账号（即经中转的账号）有以下变化：

- 不再由 CN 余额检查探测，因此也不会因 CN 余额低于阈值而被停止调度；
- 余额改由本列显示；
- 用量列里原来的 CN 余额子单元格隐藏，鼠标提示为「余额见『上游余额』列」；
- 手动查询 CN 余额返回 `CN_BALANCE_RELAY_ACCOUNT`；
- 渠道监控对这类账号不再查询 CN 余额。

以前，中转 Kimi 账号的 Key 会被发到 Moonshot 官方的余额接口，这次一并修正。

## 识别与读取

首次采集时依次尝试以下规则，识别结果按账号缓存：

1. 账号填了 new-api 访问令牌，按 new-api 处理。如果该站点其实不是 new-api，下一次识别跳过这一步。
2. `GET /v1/usage` 返回 200，`mode` 为 `quota_limited` 或 `unrestricted`，且 `isValid` 是布尔值：判为 sub2api。
3. 站点根的公开接口 `GET /api/status` 返回 `success:true`，并带有 `data.quota_per_unit`：判为 new-api。
4. 都不符合：判为暂不支持。之后按退避重新识别，间隔从 30 分钟逐步拉长到 24 小时。

401、403、429，以及非 JSON 的错误页（例如 WAF 拦截页），都不会判为暂不支持。

| 上游 | 请求 | 口径与金额 |
|---|---|---|
| sub2api | `GET /v1/usage?days=1&start_date=<今天>&end_date=<今天>` | 有 `balance` 时为「钱包」；Key 限额模式取 `quota.remaining` 与各 `rate_limits[].remaining` 的最小值，为「Key额度」；订阅模式取 `remaining`，为「订阅」，日、周、月限额都为空时为不限额。`isValid:false` 视为 Key 无效 |
| new-api，只有 Key | `GET /v1/dashboard/billing/subscription` 与 `GET /v1/dashboard/billing/usage` | 剩余 = `hard_limit_usd − total_usage/100`，再按站点的 `quota_display_type` 换算成美元（CNY 除以汇率；TOKENS 用 `quota_per_unit` 反推）。口径显示「未知」 |
| new-api，Key + 访问令牌 | 另加 `GET /api/user/self` | 钱包 = `data.quota / quota_per_unit`；有效余额取 Key 剩余与钱包的较小值。Key 剩余更小时口径为「Key额度」，否则为「钱包」；Key 不限额时取钱包 |

**地址拼接**
- sub2api 的 `/v1/*` 在接入地址的路径前缀下拼接，末尾的版本段（`/v1`、`/v1beta` 等）先去掉。例如：
  - `https://h/v1` 拼成 `https://h/v1/usage`；
  - `https://h/openai/v1beta` 拼成 `https://h/openai/v1/usage`。
- new-api 的 `/api/*` 一律挂在站点根（scheme + 主机 + 非默认端口）。
- 接入地址自带的查询参数会合并进请求。

**口径与不限额**
- new-api 的 billing 接口在站点开启「令牌统计」（默认开启）时返回 Key 剩余，关闭后返回钱包。单看数值无法区分两者，所以只填 Key 时口径显示「未知」。填了访问令牌后，口径按 Key 剩余与钱包的比较确定。
- 不限额的判断：
  - 站点以美元或人民币显示额度时，`hard_limit_usd` 恰好等于 `100000000` 视为 Key 不限额；
  - 以 TOKENS 显示时，这个值也可能是恰好等于它的有限额度。此时不显示 `∞`：有钱包值就显示钱包，否则原因为「无数据」。
- 上游返回的负数（例如欠费的钱包）原样显示，不截断为 0。

**Key 失效时**
- new-api 的 billing 返回 401 时，再用 `/api/usage/token/` 区分两种情况：
  - 余额 ≤0 或已过期：显示 $0，原因「额度用尽或过期」；
  - 其他：原因为鉴权失败。
- 该判别结果每 60 分钟重新确认一次。
- `/api/usage/token/` 返回 `success:false`（HTTP 200）一律按鉴权失败处理，不当作数值。

## 显示

| 情况 | 单元格 |
|---|---|
| 有值 | `$12.34`；不限额为 `∞` |
| 有值但较旧 | 同上，文字变灰。条件：距最后成功超过 3 分钟且之后有失败尝试，或距最后成功超过 10 分钟 |
| 无值、暂不可得 | `—` |
| 不适用 | `—` |

**鼠标提示**
- 一律显示：口径（钱包 / Key额度 / 订阅 / 未知）、原因、「更新于 N 前」。
- 时间按服务器时间校正，本机时钟不准也不影响变灰判断。
- 单元格没有手动刷新按钮。

**原因**

| 原因 | 含义 |
|---|---|
| `unidentified` | 上游类型无法识别，按退避重试 |
| `rate_limited` | 上游返回 429；有 `Retry-After` 时遵守，没有时冷却 5 分钟。new-api 按站点冷却：同一站点的所有账号在截止时间前都不再请求 |
| `auth_failed` | Key 鉴权失败 |
| `token_invalid` | 访问令牌连续 3 次失效，改为只用 Key 采集 |
| `key_ungrouped` | sub2api 的 Key 未分组 |
| `key_exhausted` | Key 额度用尽或已过期 |
| `upstream_error` | 网络错误、5xx、响应过大或格式错误；按指数退避重试，最长间隔 10 分钟 |
| `no_data` | 已识别，但上游没有返回额度数值 |
| `redis_unavailable` | Redis 不可用，采集暂停 |

## 采集节奏与对上游的影响

**节奏**
- 每分钟一轮，全集群只有一个实例执行（Redis 单例锁）。每个账号每分钟最多采集一次。
- 每轮最多 4 个并发，单次请求超时 10 秒，单个账号最多用时 25 秒。
- 响应上限：`/api/status` 为 1 MiB，其余接口为 64 KB。
- 禁止重定向，使用账号自己的代理、TLS 指纹和自定义请求头。
- 发请求前按网关相同的上游地址策略检查接入地址（`security.url_allowlist` 的主机白名单与是否允许 http）。不通过的账号本轮不发任何请求，按上游错误记录。
- 同一站点每轮最多 40 次请求，超出的账号顺延到下一轮优先处理。所以一个 new-api 站点挂了 20 个以上账号时，每个账号的采集间隔会超过 1 分钟。

**sub2api**
- `/v1/usage` 每次调用都会对该 Key 的全部用量记录做一次汇总，并刷新该 Key 的最近使用时间。
- 某个账号的单次响应超过 3 秒时，改为每 5 分钟采集一次；连续 3 次在 1 秒内返回后恢复每分钟。

**new-api**
- `/api/usage/token/` 与登录、注册共用每 IP 每 20 分钟 20 次的限流桶。
- 本功能只在判别 401 时调用它，同一站点至少间隔 75 秒（`upstream_balance:ct:<站点>` 锁）。
- 正常情况下每分钟只调用 billing，以及填了令牌时的 `/api/user/self`。这两个接口只受站点的全局限流约束。
- 任一接口返回 429 后，整个站点进入冷却（`upstream_balance:cooldown:<站点>`），所有实例在截止时间前都不再请求该站点。
- rc 版的个人访问令牌（PAT）每次调用都会在上游写一条审计记录。

## new-api 访问令牌

创建或编辑 API Key 账号时，可以展开「new-api 钱包余额（可选）」小节，填写「访问令牌」与「用户 ID（旧版 new-api 需要）」。

- rc 版建议使用只有 `profile:read` 权限的令牌。
- v0.x 的令牌没有权限范围，等于整个上游账号的权限，填写前请确认可以接受。
- v0.x 鉴权失败时，HTTP 状态码仍是 200，响应体为 `success:false`。本功能按鉴权失败处理。

**保存与显示**
- 令牌按敏感凭据保存（字段 `newapi_access_token`）：接口返回时不包含它，只返回 `credentials_status.has_newapi_access_token`。
- 已配置时显示「已配置」与「清除」：
  - 清除：提交 `null`；
  - 不改动：不提交，保留原值；
  - 填新值：替换。
- `newapi_user_id` 是普通字段，必须是 1–20 位数字，否则返回 `400 NEWAPI_USER_ID_INVALID`。

**令牌不跟随地址走**
- 修改账号的接入地址后，如果站点（scheme + 主机 + 端口）变了，而请求里没有同时提交新令牌，后端会删除已保存的令牌。
- 修改账号的代理（设置、更换或取消）时同样处理：请求里没有新令牌，就删除已保存的令牌。
- 批量编辑如果使任一所选账号的实际站点改变（包括只切换协议），或改变了任一所选账号的代理，除非同时提交了令牌，所选账号的令牌一律清除。
- 复制账号时不复制令牌。

**已知事实**：调度缓存会把账号完整凭据（包括 API Key 与令牌）以明文存进 Redis。这是现有行为，本功能没有改变它。

## 存储

| Redis 键 | 内容 | 过期 |
|---|---|---|
| `upstream_balance:<账号ID>` | 最后一次成功的金额、口径、上游类型、时间与身份指纹 | 24 小时，只在成功时写入，失败不续期 |
| `upstream_balance:state:<账号ID>` | 最近一次尝试、原因、下次到期时间与退避计数 | 48 小时 |
| `upstream_balance:kind:<账号ID>` | 识别结果 | 已识别 24 小时；暂不支持按退避 |
| `upstream_balance:site:<站点>` | new-api `/api/status` 的换算参数 | 30 分钟 |
| `upstream_balance:ct:<站点>` | `/api/usage/token/` 的 75 秒间隔锁 | 75 秒 |
| `upstream_balance:cooldown:<站点>` | new-api 站点 429 后的冷却截止时间 | 到截止时间为止 |

**身份指纹**
- 指纹覆盖实际请求所用的全部身份：平台、类型、实际接入地址、API Key、访问令牌、用户 ID、代理 ID、代理连接信息（地址与账号密码的摘要）、自定义请求头。只存最终哈希。
- 读取时用账号当前的设置重新计算。修改其中任何一项后（包括原地修改代理），旧值不再显示，直到新的采集成功。

**Redis 不可用时**：采集暂停；读接口对每个适用账号返回 `unavailable` / `redis_unavailable`，HTTP 仍为 200。

## 管理接口

`GET /api/v1/admin/accounts/upstream-balances?ids=1,2,3`

**参数**
- `ids` 可以逗号分隔，也可以重复传参，最多 200 个。
- 超出返回 `400 UPSTREAM_BALANCE_TOO_MANY_IDS`；非正数或非数字返回 400。
- 不存在的账号不出现在结果中。页面按 200 个一组分批请求。

**响应**

```json
{
  "items": [{
    "account_id": 12,
    "state": "ok",
    "amount_usd": 12.34,
    "unlimited": false,
    "basis": "wallet",
    "upstream_kind": "sub2api",
    "reason": null,
    "observed_at": "2026-10-27T12:00:00Z",
    "last_attempt_at": "2026-10-27T12:01:00Z"
  }],
  "server_time": "2026-10-27T12:01:05Z"
}
```

**`state` 的取值**

| `state` | 含义 |
|---|---|
| `ok` | 当前身份下有成功值；`reason` 是该值之后最近一次失败的原因，没有则为 `null` |
| `unavailable` | 没有可用的值 |
| `not_applicable` | 账号不适用 |

## 日志

事件名以 `upstream_balance.` 开头：

- 一轮失败为 `upstream_balance.round_failed`（Warn），worker 内的 panic 也记为此事件，`stage=panic`；
- Redis 不可用时为 `upstream_balance.paused`（Warn），同一实例 10 分钟内只记一次；
- 每轮结束为 `upstream_balance.round_done`（Debug），字段 `due`、`polled`、`skipped`、`duration_ms`；
- 每个账号的结果为 `upstream_balance.account_result`（Debug），字段 `account_id`、`upstream_kind`、`reason`、`http_status`、`duration_ms`。

日志中不出现 Key、令牌、URL、站点、请求头、响应体或原始错误文本。

## 迁移与回退

本功能没有数据库迁移。

**回退到旧版本**
- 旧版本不认识 `newapi_access_token` 是敏感字段，管理接口会把它原样返回。回退前先在账号编辑页清除所有访问令牌。
- `upstream_balance:*` 键会在 48 小时内自动过期，也可以手工删除。
- 经中转的 kimi、deepseek 账号会恢复由 CN 余额检查探测。
