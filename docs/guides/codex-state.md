# Codex STATE

Codex 上游会在响应头 `x-codex-turn-state` 中下发一个不透明的会话状态值，下文称“STATE 票据”。XY2API 可以为手动开启的 OpenAI OAuth 账号自动获取、核验并在请求中注入 STATE 票据，同时核对完整响应实际返回的模型，发现异常时撤销票据。界面把票据可用性、返回模型核验和 [智商检测](iq-check.md) 结果分开显示，任何一项都不能代替另外两项。本文面向管理 OpenAI OAuth 账号池的管理员。

## 适用范围

- 适用于 OpenAI 平台的 OAuth 与 SetupToken 账号（不含影子账号），账号必须处于 active 状态，并配置了可用的固定代理（账号自身的代理设置）。其他账号返回 `CODEX_TICKET_ACCOUNT`。
- 每个账号只对选定的目标模型生效，默认目标为 `gpt-6-astra` 与 `gpt-5.6-sol`，每个模型单独维护票据；每个账号可选 1–20 个模型。非目标模型保持原有路由。
- 新建、复制、导入的账号默认关闭。只打开全局总开关不会让任何账号参与。

## 启用步骤

1. **配置持久加密密钥**：保存代理池前，设置固定的 `TOTP_ENCRYPTION_KEY`（64 位十六进制），所有副本、每次重启都使用同一个值。已经配置过的密钥不要更改，它也用于加密其他凭据。
2. **打开总开关并配置代理池**：「设置 → 网关服务 → Codex 设置」中打开「STATE 票据总开关」（设置键 `openai_codex_ticket_enabled`，数据库中的值优先于 YAML 的 `gateway.openai_codex_ticket.enabled`），并在「STATE 打票代理池」中添加采集用的动态代理，点击「检测连通性」。
3. **为账号配置固定代理**：业务请求和票据复验都走账号自身的代理，未配置时无法开启。
4. **开启账号**：「账号管理 → 编辑账号 → STATE 票据」中选择套餐、目标模型和缺票策略，然后打开开关。
5. **获取票据**：点击「立即获取」（单个模型）或「全部获取」。

### 账号设置

账号配置保存在 `extra.codex_ticket_config`：

| 字段 | 取值 | 说明 |
|---|---|---|
| `enabled` | `true` / `false` | 开关，切换后立即保存 |
| `ticket_plan` | `pro`（默认，票据长度 292）、`team`（332） | 由管理员按账号实际套餐手动选择 |
| `models` | 模型列表 | 需要保护的目标模型 |
| `missing_policy` | `block`（默认，「暂停转发，等待票据」）、`allow_unprotected`（「无票转发（不受保护）」） | 目标模型没有可用票据时的处理方式 |

- 缺票时默认暂停该目标模型的转发；`allow_unprotected` 允许不带 STATE 转发，界面明确显示“无票转发”。网关不会发送客户端自带的、过期的或已撤销的 STATE。
- 保存使用带 `expected_revision` 的 PATCH；并发修改冲突时返回 HTTP 409、`CODEX_TICKET_CONFLICT`。界面在读取最新修订后会自动重试一次“关闭”，“开启”从不自动重放。
- 关闭账号会作废正在进行的发布，并清除当前、备用和待验证的票据候选，但保留真实的上游冷却与后台调用计数。

## 代理池

| 项目 | 说明 |
|---|---|
| 数量 | 最多 20 个命名代理，名称不超过 80 个字符；启用的代理轮流使用 |
| 协议 | `http`、`https`、`socks5`、`socks5h`，地址不能带路径或查询参数 |
| 动态 SID | 用户名中的 `{sid}` 每次尝试替换为 20 位随机十六进制；`*.1024proxy.io` 主机会重新生成 `-sid-X-t-N` 部分 |
| 网络隔离 | 某个代理出现网络错误后暂停使用 30 秒，连续出错时翻倍，最长 5 分钟 |
| 连通性检测 | 走采集链路并使用新的 SID，10 秒超时、响应上限 1 MiB、最多 3 个并发；显示本次出口的 IP 与国家，本次失败时可显示带标注的上次成功结果，修改地址后旧结果作废 |
| 外层代理 | 可选的外层 CONNECT 代理用 YAML `gateway.openai_codex_ticket.harvest_dial_proxy_url`（环境变量 `GATEWAY_OPENAI_CODEX_TICKET_HARVEST_DIAL_PROXY_URL`）配置 |

- 代理地址用现有的密钥加密服务加密保存；保存时地址留空表示保留原凭据；列表修改需携带代理池修订号，检测结果不推进修订号。
- 进程只有临时生成的加密密钥时，读写非空代理池返回 `CODEX_PROXY_ENCRYPTION_REQUIRED`，此时请配置固定的 `TOTP_ENCRYPTION_KEY` 并重启。空代理池不需要加密密钥。
- 代理池一旦存在，空列表也是有效配置，不会回退到旧的单地址设置或 YAML 配置。
- 代理密码不会出现在账号状态和诊断中。

## 票据的获取与生命周期

**获取与复验**：采集经动态代理出口发出；候选必须满足格式、套餐长度以及完整响应中实际返回模型的要求，然后通过同一账号的固定业务代理复验。复验使用业务传输链路，只发一次、禁止重定向。HTTP、SSE、JSON、压缩响应、Messages 转换和插件响应都使用相同的核验，只有 HTTP 200 或只有响应头不算通过。

**基础配置**（YAML `gateway.openai_codex_ticket`，环境变量为 `GATEWAY_OPENAI_CODEX_TICKET_<键名大写>`）：

| 键 | 默认 | 说明 |
|---|---|---|
| `ttl_seconds` | `3600` | 票据有效期 |
| `refresh_before_seconds` | `600` | 提前续期时间（开启自适应调度后不使用） |
| `harvest_probe_interval_seconds` | `6` | 扫描间隔 |
| `harvest_attempt_timeout_seconds` | `25` | 单次请求超时 |
| `harvest_dial_proxy_url` | 空 | 外层 CONNECT 代理 |

`target_length`、`models`、`fail_closed` 只用于一次性迁移旧的全局设置，日常配置以账号设置为准。

**集群并发**：环境变量 `CODEX_TICKET_MAX_CONCURRENCY` 限制整个集群同时进行的 STATE 任务数，默认 2，取值 1–10，超出范围按 2 处理。默认的 Docker Compose 文件不传递该变量和 `GATEWAY_OPENAI_CODEX_TICKET_*` 变量，需要时加入 `xy2api` 服务的 `environment` 列表；`gateway.openai_codex_ticket` 下的键也可以直接写在 `config.yaml` 中。每个账号同一时间只有一个 STATE 任务，并与业务请求共享账号并发。各实例须使用相同的并发上限与 YAML / 环境配置；修改这些部署配置前先停止旧的工作进程。

**重试与冷却**（默认策略）：

- 自动任务每轮最多 8 次候选尝试，轮内间隔 1 秒，普通失败后等待 5 分钟；手动任务每轮最多 3 次。每次尝试最多一次采集加一次固定复验。
- 401、403、429 立即结束本轮，并遵守更长的 `Retry-After` 或账号冷却。HTTP 200 中的 JSON / SSE 明确失败也参与分类：认证、配额与限流停止本轮，限流与配额冷却在同账号所有模型间共享；模型容量错误只结束当前模型的本轮。冷却取 `Retry-After`、账号冷却与默认 5 分钟中最晚的时间。
- 切换套餐或账号开关不会清除冷却。「立即获取」「全部获取」即使未到续期时间或处于普通失败退避中也会接受，但不能绕过真实的认证、限流和容量冷却；临时选择的代理只用于该任务。

**有效期**：票据封装经过严格 Base64URL 解码、`0x80` 版本字节、密文块对齐与内嵌秒级时间检查。这些检查不能验签、解密，也不能保证上游实际寿命。截止时间取“首次观察时间 + TTL”“内嵌时间 + TTL − 安全余量”“原截止时间”三者中最早的，安全余量为 30 秒与 TTL 1/10 中的较小值；内嵌时间超过当前时间 30 秒的候选不发布。同一值重新取得、固定代理复验或进程重启都不会延长原截止时间。

**响应头**：受管理的响应必须只有一个（大小写不敏感的）STATE 值；重复的相同值、冲突值、逗号合并或非法字符都不能用于采集。注入前会清除同名的变体头。

**持久化**：运行状态保存在 `accounts.extra`，领取、续租、发布和撤销经过 PostgreSQL 行锁。租约过期后可被接管，旧租约不能发布；持久化失败时不发布新票；续期失败时继续使用旧票直到原截止时间。

## 守护与撤销

守护绑定实际注入的账号、最终出站模型、配置修订和票据身份。只有以下两种情况会撤销本次实际使用的票据：

- 完整成功的响应中返回模型与目标不符；
- 完整成功的响应带回长度为 312 的合法 STATE（上游的实验信号）。

失败、截断、损坏、超限的响应不算已确认的异常。对于在终态处结束读取的协议转换，守护最多用 250 毫秒读完剩余数据以确认完整性，失败或超时不撤销。守护不重放请求、不改写响应或计费。15 分钟内三次恢复后再出现异常，该模型进入 5 分钟冷却。被撤销的票据保留 `revoked` 标记和原截止时间，不能注入，也不能作为旧票复验。

受管理账号的 Responses WebSocket 改为逐轮 HTTP 桥接，以便每轮重新读取票据；配置变化后，已建立的原生连接会被关闭并提示客户端重连。

## 自适应策略（可选）

以下策略开关都位于 `gateway.openai_codex_ticket`，默认全部关闭：

```yaml
gateway:
  openai_codex_ticket:
    unified_probe_enabled: false
    standby_enabled: false
    adaptive_scheduling_enabled: false
    quality_observation_enabled: false
    quality_isolation_enabled: false
    adaptive_concurrency_account_ids: []
```

| 开关 | 作用 |
|---|---|
| `unified_probe_enabled` | 采集、复验使用统一的请求构造 |
| `standby_enabled` | 每个模型保留一张当前票、一张已验证的备用票和一个待验证候选。只有当前票失效或剩余不足 2 分钟时才提升备用票，且备用票须剩余 10 分钟以上、5 分钟内复验过，否则先复验。完整的业务响应可以提供新的待验证候选，但必须先通过固定代理复验才能发布 |
| `adaptive_scheduling_enabled` | 每轮最多 3 次候选尝试，尝试间隔为 16–24 秒随机值且等待期间释放租约；普通失败退避 2 / 5 / 10 分钟。续期时间改为默认提前 20 分钟，或按近期续期耗时的 2 × p95 + 5 分钟，限制在 15–30 分钟且不超过 TTL 的一半 |
| `quality_observation_enabled` | 运行独立的 12 题质量集（逻辑、代码、工具 JSON 格式、上下文各 3 题），每 6 小时 3 题，每账号每模型每天最多 36 次调用（含重试）。协议不完整或失败记为未知，保留上次有效结果 |
| `quality_isolation_enabled` | 质量隔离。至少 72 小时且完成 3 套后建立基线（前三套的中位数）；两次完整得分都不高于基线 − 3、且间隔至少 15 分钟时隔离该账号的该模型，两次不低于基线 − 1 时恢复。隔离只影响该账号的该模型，不改动 STATE 开关 |
| `adaptive_concurrency_account_ids` | 对列出的账号做并发校准，例如 `[101, 102]`：把账号并发上限压到不超过 5，出现新的 429 冷却时减半（最少 1），30 分钟内没有新的 429 且业务恢复成功后加 1 |

开启自适应调度或质量观测后启用共享的后台调用预算：每账号每小时 24 次、每模型 16 次，并为恢复保留 8 次；没有票据、或剩余不足 5 分钟且没有备用票时使用救援额度 48 / 32。预算在采集前为复验预留容量，在实际发出时扣减。

建议的启用顺序：先只使用控制与诊断能力；再开启 `unified_probe_enabled` 做对照；观察一段基线后再开启 `standby_enabled` 与 `adaptive_scheduling_enabled`。

## 与智商检测的联动

智商检测按“账号 × 模型”运行，与票据按出站模型对应：

- **出站前**：检测请求取同账号、同出站模型的票据，并接受守护；受管理的检测复用业务传输，避免测试专用的 TLS 设置造成差异。
- **缺票**：缺票策略为暂停时，缺票或票据正在采集只推迟本目标（约 6 秒后或按票据侧的重试时间），本轮不计入记录、不改判定，也不影响同账号其他模型；无票策略下记为未保护。检测请求同样占用共享的后台预算。
- **票据身份校验**：每道得出答对或答错的题都记下所用票据的身份、固定代理指纹与票据配置修订。完成时逐题校验，任何一题不一致则整轮作废（`cancelled_by_ticket_change`）并立即重排，不改判定。更换代理本身属于账号身份变化，会重置全部检测目标。
- **恢复与换票**：此前没有有效票据、新票据发布成功时，所有出站模型与之匹配的已开启检测目标重新排队，清除暂停，保留降智判定与门禁。取得票据不会直接把模型标为聪明。
- **降智后换票**：只有一轮的最终结果为降智（原因为 `wrong_answer` 或 `model_mismatch`）时，才在同账号同模型至少间隔 15 分钟的限制内换票复检；正常轮首题答错、网络错误和未知结果不触发。
- 票据采集、扫描和质量任务前的账号检查只看账号状态、过期、冷却与额度，**不检查智商门禁**，避免“票据等智商恢复、智商复检又等票据”的相互等待。

每个模型的 `iq_status` 汇总出站模型相同的检测目标：没有这样的目标为 `not_configured`，都未开启为 `not_enabled`，任一已开启目标降智为 `degraded`，全部聪明为 `smart`，其余为 `unknown`；`iq_retest` 显示其中排队（`queued`）或检测中（`running`）的状态。

## 状态与诊断

账号的 STATE 票据面板每个模型默认显示“票据”“返回模型核验”“智商检测”三项，时间与守护统计收在详情里。保护状态为「STATE 保护中」「无票转发」「暂停转发，等待 STATE」「未开启」之一。面板每 3 秒刷新，浏览器标签页隐藏时暂停；刷新失败时保留最后一次成功的数据和未保存的编辑。

状态接口按模型返回 `issued_at`、`first_observed_at`、`last_replay_at`、`last_business_at`、`last_business_result`、`last_stage`、`last_reason`、`last_code`、`last_http_status`、`observed_length`、`updated_at`，以及 `quality`、`standby`、`budget`、`task`、`counters`、`protection`、`state` 等字段。业务观察的“已核验”与采集复验、智商判分分开显示；中断、超限、损坏、失败、未知编码显示为未确认。账号列表与详情中的 `codex_turn_tickets` 是不含票据内容的摘要数组。

**获取任务**：`POST .../codex-ticket/harvest` 返回 202 表示已接受。任务状态为 `queued`、`harvesting`、`verifying`、`waiting`、`succeeded`、`unchanged`、`failed`、`cancelled`；等待原因包括 `upstream_capacity`、`account_cooldown`、`account_health`、`attempt_interval`、`execution_slot`、`budget`、`proxy_unavailable`。同一账号、模型的重复请求共享正在运行的任务，重复的 `request_id` 是幂等的。常见拒绝：`CODEX_TICKET_GLOBAL_DISABLED`（总开关未开）、`CODEX_TICKET_DISABLED`（账号未开）、`CODEX_TICKET_ACCOUNT_INACTIVE`、`CODEX_PROXY_DISABLED`。

**管理接口**（位于 `/api/v1/admin` 下）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/accounts/:id/codex-ticket` | 账号配置与逐模型状态 |
| PUT / PATCH | `/accounts/:id/codex-ticket` | 保存配置；PATCH 需要 `expected_revision`，省略 `enabled` 时保持原值 |
| POST | `/accounts/:id/codex-ticket/harvest` | 可选 JSON `{"model": "gpt-6-astra", "proxy_id": 1, "request_id": "..."}`；不指定模型时获取全部已开启模型 |
| GET | `/accounts/:id/codex-ticket/diagnostics` | `{"version": 1, "tickets": [...]}`，每个模型包含最近 20 条脱敏事件和采集轮次、成功、失败、守护触发、业务核验等累计计数 |
| GET | `/accounts/:id/codex-ticket/events` | `?model=&before=<id>`，最近 72 小时的事件，按时间倒序，每次最多 100 条 |
| GET / PUT | `/settings/codex-ticket/proxies` | 读取、保存采集代理池 |
| POST | `/settings/codex-ticket/proxies/test` | 检测代理，参数为 `{id, expected_revision}` 或 `{url}` |

**存储**：迁移 251 新增 `codex_ticket_events`（保留 72 小时）与 `codex_ticket_hourly`（保留 30 天）。事件队列上限 1024，丢弃的观察会计入 `events_dropped`。事件只含 ID、长度、公开的请求结构摘要与阶段结果，不含票据、Bearer 令牌、代理凭据或提示词。业务观察每 30 秒按账号合并一次，最多缓存 8192 个票据观察组；进程退出或达到上限时可能丢失统计，这些统计不参与安全判断。

API 不返回原始 STATE、代理密码或完整业务响应。普通的账号编辑、导入、导出、复制和令牌刷新都不能覆盖或泄露服务端管理的票据、配置与运行状态。

## 升级与回滚

- **升级前**：停止旧版本的采集工作进程，备份数据库和配置，并按“启用步骤”配置固定的 `TOTP_ENCRYPTION_KEY`。首次启动时，程序在开放 HTTP 路由前以带版本标记的幂等事务承接旧的全局启用范围（标记 `codex_ticket_account_migration_v1`、`codex_ticket_envelope_migration_v2`），迁移失败则启动失败。未过期的旧票必须通过固定代理复验，保留原截止时间，失败后重新采集；缺少固定代理的账号会显示配置原因。
- **回滚**：先关闭新增的自适应策略开关，再停止新的工作进程、关闭 STATE 总开关，然后部署兼容的旧版本并按旧方式重新启用。旧程序不理解新的租约，不能与新工作进程同时运行。保留真实的冷却、撤销与预算记录；不要把数据库备份中过期或已撤销的票据恢复到业务中，也不要从导出的公开账号数据恢复私有票据。
- **验证建议**：票据长度、模型字段和本地模拟都不能证明上游会持续接受 STATE。大范围启用前，请先在测试账号上覆盖至少两个续期周期，观察采集成功率、恢复耗时、续期失败、误撤销、无票转发与智商检测结果。
