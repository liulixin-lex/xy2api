# 网关韧性

网关韧性是 XY2API 在账号调度与请求转发之上的一组容错能力，目标是减少用户能感知到的失败：上游抖动时在输出前换号，单账号分组整体不可用时用软罚账号兜底，经 CDN 的长请求在首个输出前发送心跳以免被空闲超时切断，客户端提前断开时不误计费，失败时给出符合 Codex、Claude Code 等客户端重试规则的错误。全部能力由一份 JSON 设置控制，除“协作消息明文化”外默认开启，修改无需重启。本文面向自建部署的运维与管理员，逐项说明每个能力的作用、默认值、何时关闭、对计费与客户端的影响，以及相关的日志事件和指标。

同一主题的其他文档：

- [设置参考](gateway-resilience-settings.md)：完整的设置文档、每个字段的默认值与取值范围、读写接口。
- [罚则与选号](gateway-resilience-routing.md)：失败分类、软罚与硬罚、首输出截止、Claude 判慢让位、模型级冷却、末位兜底与无号兜底分组。
- [客户端可见的行为](gateway-resilience-client.md)：错误码与响应头、流式终止帧与心跳、请求修复重放、送达与计费口径。
- [观测与排障](gateway-resilience-observability.md)：运维面板、选号解释、管理接口、计数项与日志事件。

## 概览

| 能力 | 设置段 | 默认 | 摘要 |
|---|---|---|---|
| 失败分类与冷却 | `failure` | 开 | 每次上游失败归类，决定是否换号、是否罚号、罚多久 |
| 会话失败改绑 | `session` | 开 | 同一会话在同一账号上连续失败后改绑并暂避该账号 |
| Claude 判慢让位 | `anthropic_latency_hold` | 开 | 首个输出持续偏慢的 Claude 账号在同优先级内后排，可迁出会话 |
| 同站断流暂停 | `same_site_hold` | 开 | 同一上游站点的多个 OpenAI 账号接连断流时暂停这些账号 |
| 末位兜底 | `last_resort` | 开 | 组内没有无罚账号时，用软罚账号继续服务 |
| 余额不足暂停 | `balance` | 开 | 上游余额不足的账号暂停调度，暂停时长逐级加长 |
| 请求预算 | `budget` | 开 | 限制每个请求的尝试次数与首个输出前的总时长 |
| 首包前保活 | `keepalive` | 开 | 经 CDN 的流式请求在首个输出前定时发心跳 |
| 停滞检测 | `stall` | 开 | 已输出后长时间无数据时结束请求并记账 |
| 断开计费 | `disconnect` | 开 | 输出前断开不计费，部分输出后断开按已得用量计费 |
| 错误语义与防风暴 | `errors` | 开 | 失败带 `Retry-After`，抑制重复 503 与无尽重试 |
| 无号兜底分组 | `fallback_group` | 开 | 分组无可用账号时由配置的兜底分组承接 |
| 在途预留 | `reservation` | 开 | 余额预留按缓存读比例与输出 P99 估算 |
| 流式开场与终止 | `stream` | 开 | 正文前的开场事件先暂存，失败时仍可换号；识别失败终态 |
| 仅流式账号学习 | `capability` | 开 | 记住只接受流式请求的账号，非流式请求暂不选它 |
| 协作消息明文化 | `collaboration_plaintext` | 关 | Codex 协作工具消息以明文投递到已验证的上游 |

## 启用与配置

- 管理后台：**系统设置 → 网关服务 → 网关韧性**。顶部是总开关，每项能力一张卡片，放该能力的开关与常用数值；底部“高级（完整 JSON）”可编辑全部字段。只有超级管理员能看到和修改这张卡片。
- 接口：`GET` / `PUT /api/v1/admin/settings/gateway-resilience`，`PUT` 按字段合并，未知字段与越界值整体拒绝（`GATEWAY_RESILIENCE_SETTINGS_INVALID`）。细节见 [设置参考](gateway-resilience-settings.md)。
- 保存的实例立即生效，其他实例每 10 秒刷新。设置保存在数据库，不需要环境变量。
- 每项开关只在总开关开启时有效。关闭总开关后，选号与错误处理回到不带韧性的原有逻辑。OpenAI 账号的[质量状态](group-quality-gate.md)不受韧性开关影响。

## 部署前提

### CDN 识别

首包前保活、经 CDN 时把 502/504 改答 503 等行为只对“经 CDN 的请求”生效。网关按以下顺序判断请求来自哪个 CDN（边缘），默认不需要额外配置：

1. 反向代理注入的 `X-XY-Edge` 头：只在直连对端属于 `server.trusted_proxies`（环境变量 `SERVER_TRUSTED_PROXIES`）时读取，取值须在 `keepalive.edges` 内。
2. 否则按 CDN 自带的回源头推断：带 `CF-Ray`，或 `CDN-Loop` 含 `cloudflare`，记为 `cf`；`CDN-Loop` 含 `esa` 记为 `esa`（阿里云 ESA）。`CDN-Loop` 列出多个 CDN 时，取离源站最近且在白名单内的一个。

说明：

- 推断出的名字仍须在 `keepalive.edges` 白名单内（默认 `cf`、`esa`）。从白名单去掉某个名字即关闭对它的识别。
- 推断不要求受信对端。直连源站的客户端伪造这些头，只会改变它自己那个请求的返回形式（提前提交 SSE、发心跳、失败改为流内终止帧、502/504 改为 503），不涉及鉴权、计费或其他用户。
- 使用其他 CDN，或 CDN 不带上述头时，由反向代理按**对端 IP**（不得按任何请求头）注入 `X-XY-Edge`，先删除客户端自带的同名头，并把反向代理的地址加入 `server.trusted_proxies`。例如 Caddy：

```caddy
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cf remote_ip <Cloudflare 回源网段>
	request_header @xy_edge_cf X-XY-Edge cf
}
```

### 反向代理

- 流式响应（`text/event-stream`）不能被压缩或缓冲，否则心跳与首个输出都会被代理攒住。
- API 路径的代理错误页建议返回 JSON 与 `Retry-After`，避免纯文本 5xx。
- 信任边界、真实客户端 IP 与健康检查见 [反向代理、CDN 与边缘安全](../deployment/reverse-proxy.md)。

## 能力说明

各能力的字段范围见 [设置参考](gateway-resilience-settings.md)；事件与指标的含义见 [观测与排障](gateway-resilience-observability.md)。

### 失败分类与冷却 `failure`

- **做什么**：把每次上游失败归为一类（余额不足、鉴权、限流、容量降载、瞬时失败、首输出超时、停滞、模型不可用、客户端错误等，完整列表见 [罚则与选号](gateway-resilience-routing.md#失败分类)），据此决定是否换号、是否罚号、罚多久。瞬时失败（502、503、超时、传输错误）让账号进入短冷却；CDN 或代理写出的 5xx 错误页按瞬时失败处理。同一账号 60 秒内输出后失败 3 次，也进入同样的短冷却（池模式账号除外）。
- **主要字段**：`transient_cooldown_seconds` 默认 30；上游带 `Retry-After` 时按它冷却，封顶 `retry_after_cap_seconds`（默认 120）；`balance_keywords_extra` 追加余额不足的识别文案；`decision_v2` 默认开，只把真正的容量信号（`server_is_overloaded`、`slow_down`、529、结构化的 overloaded）算作容量降载，“服务繁忙”“排队过多”“并发超限”一类按瞬时或限流处理，可以换到同一主机的其他账号。
- **何时关闭**：一般不关闭。关闭 `decision_v2` 回到较粗的旧分类规则，同时“仅流式账号学习”只学习、不在本请求换号。
- **计费与客户端**：分类本身不改变计费。分类结果写进运维错误记录的 `resilience_category`。[分组质量门槛](group-quality-gate.md)的错误维用同一套分类规则，但不看韧性开关：同一个失败，开关开或关结论都一样。
- **观测**：`resilience.transient_cooldown`、`resilience.transient_lifted`、`resilience.unclassified`（只按状态码归类的 4xx 文案采样，可据此补充 `balance_keywords_extra`）。

### 会话失败改绑 `session`

- **做什么**：同一会话在同一账号上 10 分钟内连续失败 2 次后改绑，并在 10 分钟内避开该账号。OpenAI 账号另有“输出后首次失败即改绑”（`post_output_rebind`）：已开始输出后的流内失败、读流出错或缺终态第一次发生就改绑。客户端错误、内容安全拒绝、客户端断开与计划内的首输出截止不计入。
- **主要字段**：`streak_threshold` 2、`streak_window_seconds` 600、`avoid_seconds` 600、`post_output_rebind` true。
- **何时关闭**：只想观察失败、不想让会话离开原账号时关闭。关闭 `session.enabled` 会同时停用输出后首次失败改绑与 Claude 判慢迁出；OpenAI 分组的后排迁出与接管不受它影响（见 [调度](scheduling.md#openai-会话的去留)）。
- **计费与客户端**：不影响计费。改绑会让会话换到新账号，上游提示缓存需要重新建立。
- **观测**：计数 `session_rebind`；日志 `resilience.session_rebind`（原因 `resil_failure_streak`、`post_output_first_failure`、`terminal_rejection_streak`）。

细则（软罚期间保留绑定、带密文片段的请求如何处理）见 [罚则与选号](gateway-resilience-routing.md#会话失败改绑)。

### Claude 判慢让位 `anthropic_latency_hold`

- **做什么**：按账号与实际出站模型记录 Claude 会话续轮的首个有效输出耗时。近 5 分钟内 ≥10 秒的样本占 35% 以上时，该账号对该模型进入 3 分钟判慢期，在同一优先级内排到后面（不剔除、不跨优先级）；已绑定在它上面的会话，若同优先级有未判慢且有空闲并发的账号则迁出，同一会话 30 分钟内最多迁出一次。
- **何时关闭**：Claude 上游普遍较慢、迁出反而打散缓存时关闭。关闭后立即停止后排与迁出，已迁出的会话不迁回；重新开启后只按新样本判断。
- **前提**：需要 `gateway.scheduling.load_batch_enabled`（默认 true）；会话迁出还需要“会话失败改绑”开启。
- **观测**：`anthropic_account_latency_hold_started`、`anthropic_account_latency_sticky_migrated`、`anthropic_account_latency_sticky_kept`。

OpenAI 账号没有判慢让位：首字偏慢由[分组质量门槛](group-quality-gate.md)的首字维与急性保护处理。

### 同站断流暂停 `same_site_hold`

- **做什么**：上游站点按 OpenAI 账号的 Base URL 主机名归并。60 秒内同一站点至少 2 个 OpenAI 账号分别发生输出后的传输层断流（读流出错、缺终态）时，只暂停这些断流账号 300 秒；同站没有断流的账号不受影响。中转在流内报告的错误、输出前失败、客户端取消不计入。时长与阈值是固定值。
- **影响**：暂停按软罚处理，普通选号跳过，组内没有其他可用账号时仍可作末位兜底。绑定在暂停账号上的会话下次请求即改绑，暂停到期后不迁回。
- **何时关闭**：暂停导致其他站点的限流或过载明显上升时关闭。关闭后不再建立新暂停，已有暂停立即失效。
- **观测**：`resilience.same_site_hold`（只记录主机名摘要，不记录明文主机名）。

### 末位兜底 `last_resort`

- **做什么**：分组内没有无罚账号时，先转投该分组配置的无号兜底分组；没有可用的兜底分组时，按优先级与罚期结束时间在软罚账号中取第一个有空闲并发的继续服务，全部占满时排队等待排在第一的账号。单账号分组因此不会因一次瞬时失败整组不可用。
- **不放宽的条件**：只放宽软罚。硬罚、余额暂停、模型不可用、智商检测降智、利润门、窗口费用与 RPM 上限都不会被放宽。
- **何时关闭**：希望软罚期间直接返回 503、由客户端自行重试时关闭。
- **观测**：计数 `last_resort`；日志 `resilience.last_resort`、`resilience.last_resort_avoided`。

### 余额不足暂停 `balance`

- **做什么**：上游报告余额不足（含订阅共享额度用尽）时本请求换号，该账号暂停调度。暂停时长逐级加长：第 1 档 `pause_seconds`（默认 300 秒），到期后仍不足依次为 3 倍、12 倍并封顶（默认 5、15、60 分钟）。只有上一段暂停到期之后才开始的请求再次失败才升档；开始时间晚于暂停结束的请求成功后复位。到期后由真实请求试探，不主动探测、不告警。
- **范围**：API Key 与 Upstream 类账号；Antigravity 与自带余额恢复的渠道不在此列。
- **何时关闭**：上游余额文案误判较多时关闭；关闭后余额不足仍会换号，只是不暂停。已写入的暂停照常到期。
- **计费与客户端**：普通用户只看到 503，余额细节只对管理员可见。账号列表把它显示为临时不可调度，原因 `balance:exhausted[step=N]: <证据摘要>`；“恢复状态”立即放回并复位档位。欠费但暂时不用的账号，直接关掉“调度”开关。
- **观测**：`resilience.balance_paused`（含档位 `step`）、`resilience.balance_lift_failed`。

### 请求预算 `budget`

- **做什么**：限制每个文本请求最多尝试 `max_attempts` 次（默认 4），并限定首个输出前的总时长。截止时刻 D 为“锚点 + `pre_output_budget_seconds`”（默认 240 秒），且不晚于请求到达后 290 秒。等待并发槽位的时间也计入。图片与视频请求不设首输出截止，WebSocket 不挂连接级预算。
- **流式与非流式**：流式请求在首个输出前受截止约束；非流式请求的“首个输出”就是整个响应，预算只决定能否再发起新的尝试，进行中的单次非流式请求不按预算截断（Gemini 例外，见下）。
- **单次尝试截止**：OpenAI 流式文本请求按推理强度取首输出截止 `first_output_timeout_seconds`（default 20 / high 25 / xhigh 30 / max 30 秒，热尝试），冷尝试与大请求体另有下限；`model_overrides` 按模型覆盖。Anthropic 与 Antigravity 的首事件截止为 90 秒，Gemini 文本请求非最后候选的尝试截止为 60 秒。最后候选只受剩余预算约束。规则见 [罚则与选号](gateway-resilience-routing.md#首输出截止)。
- **同号重试**：每个账号在一个请求内最多同号重试 `same_account_retry_max` 次（默认 1）；余额、鉴权、模型不可用不在同一账号上重试；容量降载只换号一次且只换到其他上游主机。
- **截止锚点 `budget_start`**：`arrival`（默认）从请求到达算起；`body_eof` 在上传请求体用了至少 1 秒时，从请求体收完算起，仍不晚于到达后 290 秒。适合大请求体、上传慢的客户端，只影响新请求。
- **截断归因 `budget_cut_attribution`**：一次尝试的截止被剩余预算压短、没给满档位时称为“预算截断”。`off` 不判定；`observe`（默认）只记录（指标 `budget_cut_fot` 等）；`enforce` 时这类截断在 OpenAI 的[质量状态](group-quality-gate.md)里不算首输出超时，不计入首字维与急性保护。
- **何时关闭**：只在需要完全回到无截止、无预算的原有重试逻辑时关闭；想放宽时优先调大档位或预算总时长。
- **计费与客户端**：失败的尝试不向用户计费。预算耗尽时按入站协议返回错误或写终止帧，见 [客户端可见的行为](gateway-resilience-client.md)。
- **观测**：计数 `first_output_timeout`、`budget_exhausted`；日志 `resilience.budget_exhausted`、`resilience.capacity_switch_exhausted`、`resilience.budget_timeout_plan`。

### 首包前保活 `keepalive`

- **做什么**：只对经 CDN 的流式请求生效（见“CDN 识别”）。请求开始 `delay_seconds`（默认 15 秒）后仍无输出时，提交 SSE 响应头并每 `interval_seconds`（默认 10 秒）发一次心跳，避免 CDN 空闲超时切断。换号后心跳在下一次尝试中继续，写出内容、终态或心跳写失败后停止。心跳格式按入站协议选择，见 [客户端可见的行为](gateway-resilience-client.md#心跳)。
- **例外**：`skip_official_oauth`（默认 true）时，官方 OAuth / Setup Token 账号不主动发起首包前保活。
- **影响**：心跳提交 HTTP 200 后，之后的失败只能以流内终止帧告知客户端，不能再改状态码。心跳不算输出、不计费，也不妨碍换号。
- **何时关闭**：CDN 空闲超时足够长，或客户端不接受注释心跳时关闭。
- **观测**：计数 `keepalive_committed`；日志 `resilience.keepalive_committed`。

### 停滞检测 `stall`

- **做什么**：已向客户端输出后，上游超过 `text_stall_seconds`（默认 120 秒）没有任何数据，判定断流并结束请求，按入站协议写终止帧；图片生成豁免。停滞计入账号失败与会话失败。输出前的静默由首输出截止接管并换号。
- **计费**：按已读到的用量计费一次。
- **何时关闭**：上游长时间无输出属于正常（例如极长推理且不发任何事件）时调大阈值，必要时关闭。
- **观测**：计数 `stall`；日志 `resilience.stall`。

### 断开计费 `disconnect`

- **做什么**：客户端在任何语义输出前断开时，立即取消上游、不写用量、不计费，记入未送达表；部分输出后断开时，最多再读 `partial_drain_max_seconds`（默认 180 秒）并按已得用量计费。开场事件、心跳与排队时的 ping 都不算输出。客户端已离开后上游再出错，不计入账号失败、不改绑、不冷却。
- **滥用保护**：同一 API Key 每小时未送达超过 `abuse_guard.threshold_per_hour`（默认 30）次时视为滥用，不再取消上游，按上游成本计费，用量记录的 `billing_mode` 为 `undelivered_abuse`。请求体还没收完就断开的不计入次数。
- **何时关闭**：需要所有请求都按上游实际用量计费时关闭。
- **观测**：计数 `undelivered`、`cdn_cut`、`abuse_guard`；未送达列表与接口见 [观测与排障](gateway-resilience-observability.md#未送达请求)。送达口径见 [客户端可见的行为](gateway-resilience-client.md#送达与计费口径)。

### 错误语义与防风暴 `errors`

- **做什么**：
  - 无可用账号时 503 带 `Retry-After`（`no_account_retry_after`，默认 2–30 秒，按组内最早恢复时间估算）；配置原因导致无号时 `Retry-After: 60` 并带 `x-should-retry: false`。
  - 同一分组、平台、模型的“无号”结论负缓存 `negative_cache_seconds`（默认 2 秒）；同一 API Key 在 30 秒内收到 10 次相同 503 后快速返回（`fast_fail`）。
  - 同一会话连续 5 次耗尽失败（间隔 120 秒内）后改为终止应答，不再建议重试（`turn_cap`）。
  - 合成的 `response.failed` 按情况附带重试时间（`failed_frame_retry_after`）；Codex 客户端收到的 `Retry-After` 不超过 `codex_retry_after_cap_seconds`（默认 10 秒，0 表示不另设）。
  - Anthropic 协议下网关自身的失败与换号耗尽的容量降载统一为 503 `api_error`，避免 Claude Code 把 `overloaded_error` 计为 529 后停止或切换模型。
- **何时关闭**：只在下游系统依赖旧的错误形态时关闭；关闭后不加重试头、不做负缓存与回合封顶，错误形态回到原样。
- **观测**：计数 `fast_fail`、`turn_cap`；日志 `resilience.fast_fail`、`resilience.turn_cap`。各场景的响应见 [客户端可见的行为](gateway-resilience-client.md)。

### 无号兜底分组 `fallback_group`

- **做什么**：在分组编辑页为分组选择“无号兜底分组”（同平台、启用、非订阅的其他分组，不能指向自身或成环）。主分组没有可服务成员、兜底组有时，请求由兜底组承接；该 API Key 进入兜底后记忆 `binding_ttl_seconds`（默认 600 秒），主分组持续健康 `return_stable_seconds`（默认 120 秒）后切回，抖动的主分组不会来回切换。换号耗尽不跨组。
- **计费**：价格按实际服务的兜底组计算（分组倍率、用户在兜底组的覆盖倍率、高峰系数、图片与视频价格、长上下文规则、渠道定价）；授权、模型白名单、额度与限额、余额检查仍按原分组。用量记录的 `group_id` 为兜底组，`origin_group_id` 为原分组；用户侧只显示原分组。
- **何时关闭**：不希望任何请求落到兜底组时，关闭开关或清空分组上的兜底配置。
- **观测**：计数 `fallback_group`、`fallback_return`；日志 `resilience.fallback_group`（`reason` 为 `balance_exhausted` 或 `no_servable_account`）、`resilience.fallback_return`。完整口径见 [罚则与选号](gateway-resilience-routing.md#无号兜底分组)。

### 在途预留 `reservation`

- **做什么**：用户余额会为在途请求预留一部分。`cache_aware_estimate` 按该会话最近一次的缓存读比例估算输入费用；`output_p99_cap` 把输出 Token 估算封顶为该模型近 7 天的 P99（下限 1024）。两者都让预留更接近实际费用，减少余额充足却因预留被拒的情况。
- **客户端**：余额被在途请求占用时返回 429 `rate_limit_error`（`concurrent_balance_reserved`），`Retry-After` 约 5 秒（`errors.reservation_retry_after_seconds`），运维记录标为业务限制。
- **何时关闭**：需要更保守的预留（按最坏情况估算）时关闭。
- **观测**：`resilience.output_p99_refresh_failed`、`resilience.cache_ratio_store_failed`（存储失败时降级为原有估算）。

### 流式开场与终止 `stream`

- **做什么**：上游流在第一个正文之前通常先发“开场事件”（Responses 的 `response.created`、Chat 的 role 分块、Anthropic 的 `message_start` 等）。这些事件先暂存不写给客户端，暂存期间上游失败（过载、服务忙、断流、读错）仍可换号，客户端只看到成功那次尝试的开场。
  - `prelude_staging`：Chat Completions 与 Messages 经兼容转换、Gateway 入站经 Anthropic 上游的 Responses 与 Chat、原生 Anthropic 与 API Key 直通推迟写出开场，暂存最多 8 MiB，超出时换号。
  - `native_prelude_whitelist`：原生 Responses 把 `response.queued`、`response.metadata`、`codex.rate_limits`、`codex.response.metadata` 与不含内容的 added 事件也算作开场。
  - `strict_terminal`：明确识别失败终态，例如 raw Chat 透传中 `error` 后跟 `[DONE]` 判为失败；输出后断流、读错或停滞时按协议写失败终态，不再合成成功结束；启用 Responses 透传的首输出截止与停滞检测。
- **何时关闭**：只在某个客户端依赖开场事件的写出时机时，关闭对应的子开关。
- **计费与客户端**：只写出开场时断开按未送达处理，不计费。不完整（incomplete）、内容拒绝与合法空回答照常返回，不重放。
- **观测**：计数 `relay_keepalive`、`pre_created_delta`；日志字段 `prelude_release_event` 等，见 [观测与排障](gateway-resilience-observability.md#请求关联字段)。

### 仅流式账号学习 `capability`

- **做什么**：非流式请求遇到上游明确的“只接受流式请求”400（如 “only accepts streaming requests”“stream must be true”）时，记住该账号在当前协议下不收非流式请求，有效期 `learn_ttl_seconds`（默认 6 小时）；本请求立即换号，不计账号失败、不冷却。之后会因此失败的非流式请求暂不选它；流式请求，以及网关本来就改发流式的请求不受影响。
- **范围**：OpenAI 入站的 Responses 与 Chat 普通文本请求，OpenAI 与 Grok 平台的账号；只在上游协议与入站相同、上游流模式跟随客户端的路径上过滤。学习粒度是“账号 + 协议”，不分模型。
- **管理**：账号列表的状态列显示“仅流式（自动学习，剩余 X 小时）”，可点“清除”；接口 `DELETE /api/v1/admin/accounts/:id/capability-learning`。学习结果存在账号 `extra`，复制、导入与导出账号不带它；改了上游配置的账号不受旧记录影响。
- **何时关闭**：关闭后不再学习，已学到的记录不参与选号、按到期时间自然失效。本请求换号还要求 `failure.decision_v2` 开启，否则只学习、本请求照旧返回 400。
- **客户端**：组内账号全部被学为仅流式时，非流式请求返回 503，消息提示改用 `"stream": true`，带 `x-should-retry: false`、不带 `Retry-After`。
- **观测**：日志 `resilience.capability_learned`、`resilience.capability_learning_failed`；选号解释中被跳过的账号原因为 `capability_stream_only`。

### 协作消息明文化 `collaboration_plaintext`

- **做什么**：Codex 的协作工具（`spawn_agent`、`send_message`、`followup_task`）默认以密文投递消息，换号后新账号无法解开，会话会报错。开启后，符合条件的新协作消息以明文投递：网关把请求里的 `collaboration` 命名空间改名后发给上游，并在响应中把名字还原，客户端看到的协议身份不变。不修复已有会话历史，不改写模型生成的正文。
- **生效条件**（全部满足）：总开关与 `collaboration_plaintext.enabled` 开启；最终出站模型在 `models` 清单内（模型映射之后、不区分大小写精确匹配，清单为空时不生效）；账号开启“协作消息明文化”（账号 `extra.openai_collaboration_plaintext`）；请求来自真实的 Codex 客户端，经原生 Responses HTTP。WebSocket、透传、compact、协议桥接与开启“摊平 Codex namespace 工具”的账号不转换；请求形态有歧义或冲突时原样转发。
- **启用前**：该能力依赖上游对明文协作消息的实际支持，启用前请在目标账号与模型上用真实 Codex 客户端验证父子调用与续轮；同站账号统一配置，并把兜底分组、满载借用可能用到的账号一并纳入核验范围。
- **关闭**：关闭账号开关只停止该账号后续请求的转换（软回退）；关闭功能开关、总开关或清空 `models` 停止全部转换（硬回退）。进行中的响应仍会还原；已用过该通道的会话在部分上游可能被拒绝，需要新开会话。转换状态改变时，进行中会话的下一次请求从工具声明处起提示缓存失效一次，建议低峰、整站切换。
- **观测**：计数 `collab_plaintext`、`collab_call`、`collab_call_action`；日志 `resilience.collab_plaintext`（冲突、异常计数或转换后被拒时记录）。

## 不受开关控制的行为

以下属于协议正确性修正，关闭总开关后仍然生效：

- 是否“已开始输出”按实际写给客户端的字节判断；只有尚未写出、客户端未断开且请求未取消时才换号；客户端取消或写出失败不计为上游首输出超时，也不计账号失败。
- 心跳只提交网关自身的流式响应头，失败尝试的上游响应头不会写出。
- 排队等待并发槽位时的心跳与随后的终止帧按入站协议的格式写出。
- 上游加密内容无法解密、会话状态拒绝按请求级处理，不罚号；对应的同号修复重放照常进行（见 [客户端可见的行为](gateway-resilience-client.md#请求修复与重放)）。
- 余额不足、模型不可用在各平台换号；上游明确报告模型不可用时，该账号对该模型 30 分钟不可用。
- 全为 0 的 `metadata.session_id` 视为缺失；读请求体时连接中断在运维记录里记为 499；模型不在分组白名单时 404 文案附白名单前 10 项。

## 限制与注意事项

- 会话失败与回避、末位兜底索引、计数项、Claude 判慢状态与模型级冷却都保存在进程内：多实例部署时各实例分别维护，重启后清零。蓝绿切换的短暂重叠期共用 Redis 是支持的，长期多活需要评估这些进程内状态的影响。
- 兜底判断、末位兜底与无号 `Retry-After` 读取 2 秒的分组快照；网关自身施加的冷却会立即刷新快照，其他来源（上游限流、管理员改动、其他实例）最多 2 秒后可见。
- 未送达表固定保留 30 天，每 6 小时清理一次；滥用保护只在上游返回用量时按成本计费。
- 兜底分组只支持标准（余额）分组；进入兜底的记忆按 API Key 而不是会话。
- WebSocket 不挂连接级预算，建连首回合的换号次数按网关的账号切换上限；余额暂停的升档与复位在 WebSocket 路径上尚不生效（照常暂停）。
- 输出后的失败补记只覆盖 HTTP，WebSocket 不补。
