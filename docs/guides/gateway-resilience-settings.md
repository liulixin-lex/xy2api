# 网关韧性设置参考

本文列出网关韧性设置文档的完整结构、每个字段的默认值与取值范围，以及读写接口的语义，供需要通过接口或高级 JSON 修改设置的管理员查阅。各能力的作用与关闭时机见 [网关韧性](gateway-resilience.md)。

## 存储与读写

- 设置保存在 `settings` 表，键为 `gateway_resilience_settings`，值是一份 JSON 文档。
- 管理后台：**系统设置 → 网关服务 → 网关韧性**。顶部为总开关，每项能力一张卡片，底部“高级（完整 JSON）”可编辑全部字段，“恢复默认值”把表单重置为默认值（仍需保存）。只有超级管理员可以读取和修改。
- 接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/admin/settings/gateway-resilience` | 返回 `{"settings": 当前生效值, "defaults": 默认值}` |
| `PUT` | `/api/v1/admin/settings/gateway-resilience` | 提交的字段合并到当前设置；返回同样的结构 |

- `PUT` 的规则：
  - 只需提交要改的字段，未提交的字段保留当前值。
  - 数组整体替换；`budget.model_overrides` 只要出现在请求体里，就整体替换原有映射（用来删除键）。
  - 未知字段拒绝。全部范围由服务端校验，任一字段不合法时返回 400，错误码 `GATEWAY_RESILIENCE_SETTINGS_INVALID`，消息列出全部问题，设置不生效。
- 生效：保存的实例立即生效，其他实例每 10 秒从数据库刷新一次。读取失败或库中文档不合法时，实例保留最后一次有效值；冷启动读不到时使用默认值。
- 库中文档缺少的字段按默认值补齐，所以新版本新增的字段不需要手工写入。
- 保存没有版本校验：两位管理员基于旧页面先后保存时，后保存的覆盖先保存的。多人维护时，保存前先刷新页面。

## 默认文档

```json
{
  "enabled": true,
  "failure": {
    "transient_cooldown_seconds": 30,
    "retry_after_cap_seconds": 120,
    "balance_keywords_extra": [],
    "decision_v2": true
  },
  "session": {
    "enabled": true,
    "post_output_rebind": true,
    "streak_threshold": 2,
    "streak_window_seconds": 600,
    "avoid_seconds": 600
  },
  "anthropic_latency_hold": { "enabled": true },
  "same_site_hold": { "enabled": true },
  "collaboration_plaintext": { "enabled": false, "models": [] },
  "last_resort": { "enabled": true },
  "balance": { "enabled": true, "pause_seconds": 300 },
  "budget": {
    "budget_start": "arrival",
    "budget_cut_attribution": "observe",
    "enabled": true,
    "max_attempts": 4,
    "pre_output_budget_seconds": 240,
    "first_output_timeout_seconds": { "default": 20, "high": 25, "xhigh": 30, "max": 30 },
    "anthropic_first_event_timeout_seconds": 90,
    "gemini_first_event_timeout_seconds": 60,
    "model_overrides": {},
    "same_account_retry_max": 1,
    "gemini_same_account_retries": 1,
    "upstream_retry_after_cap_seconds": 60
  },
  "keepalive": {
    "enabled": true,
    "delay_seconds": 15,
    "interval_seconds": 10,
    "edges": ["cf", "esa"],
    "skip_official_oauth": true
  },
  "stall": { "enabled": true, "text_stall_seconds": 120 },
  "disconnect": {
    "enabled": true,
    "cancel_undelivered": true,
    "partial_drain_max_seconds": 180,
    "abuse_guard": { "enabled": true, "threshold_per_hour": 30, "mode": "bill_upstream_cost" }
  },
  "errors": {
    "enabled": true,
    "no_account_retry_after": { "min": 2, "max": 30 },
    "config_retry_after_seconds": 60,
    "reservation_retry_after_seconds": 5,
    "negative_cache_seconds": 2,
    "fast_fail": { "enabled": true, "window_seconds": 30, "threshold": 10 },
    "turn_cap": { "enabled": true, "failures": 5, "window_seconds": 120 },
    "failed_frame_retry_after": true,
    "codex_retry_after_cap_seconds": 10
  },
  "fallback_group": { "enabled": true, "return_stable_seconds": 120, "binding_ttl_seconds": 600 },
  "reservation": { "cache_aware_estimate": true, "output_p99_cap": true },
  "stream": { "prelude_staging": true, "native_prelude_whitelist": true, "strict_terminal": true },
  "capability": { "stream_only_learning": true, "learn_ttl_seconds": 21600 },
  "fco_compat": { "mode": "reactive", "admin_names": [] }
}
```

所有开关都要求总开关 `enabled` 同时开启；关闭总开关时，下表的开关一律视为关闭。

## 字段

### 失败分类 `failure`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `transient_cooldown_seconds` | 30 | 1–3600 | 瞬时失败的账号短冷却时长 |
| `retry_after_cap_seconds` | 120 | 1–3600 | 上游给出等待提示时的账号冷却上限；不截短请求内同账号的最早重试时刻 |
| `balance_keywords_extra` | `[]` | 最多 64 条，每条 2–128 字符 | 追加识别为“上游余额不足”的错误文案关键词 |
| `decision_v2` | true | 布尔 | 失败分类修正；也是仅流式学习后本请求立即换号的前提 |

第三方 API Key 的 HTTP 与流内 429 同样以 `retry_after_cap_seconds` 封顶；上游给出的长配额重置时间不再当作冷却时长，上游重试提示给出的最早重试时刻仍然遵守。同一账号切换 Chat 与 Responses 时按本次实际地址判断，官方 Chat 地址继续使用官方配额窗口。

### 会话 `session`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 会话失败改绑。关闭同时停用 OpenAI 首输出避让与救场交接 |
| `post_output_rebind` | true | 布尔 | 仅 OpenAI 账号：输出后首次失败即改绑；依赖 `session.enabled` |
| `streak_threshold` | 2 | 1–20 | 同一会话在同一账号上连续失败多少次后改绑 |
| `streak_window_seconds` | 600 | 1–86400 | 连续失败的计数窗口 |
| `avoid_seconds` | 600 | 1–86400 | 失败改绑的回避时长，也是 OpenAI 首输出避让从截止时刻起算的时长 |

### 单开关能力

| 字段 | 默认 | 说明 |
|---|---|---|
| `anthropic_latency_hold.enabled` | true | Claude 判慢让位 |
| `same_site_hold.enabled` | true | OpenAI 同站断流暂停 |
| `last_resort.enabled` | true | 末位兜底 |

### 协作消息明文化 `collaboration_plaintext`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | false | 布尔 | 功能开关 |
| `models` | `[]` | 最多 16 条，每条去首尾空白后 1–128 字符 | 已验证的实际出站模型；不区分大小写，重复项自动合并。清单为空时不转换任何请求 |

### 余额暂停 `balance`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 余额不足暂停 |
| `pause_seconds` | 300 | 30–3600 | 第 1 档暂停时长；第 2、3 档为它的 3 倍、12 倍 |

### 请求预算 `budget`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 请求预算 |
| `max_attempts` | 4 | 1–10 | 每个文本请求最多尝试次数 |
| `pre_output_budget_seconds` | 240 | 60–290 | 首个输出前的总时长 |
| `budget_start` | `arrival` | `arrival` / `body_eof` | 截止时刻的锚点，见 [请求预算](gateway-resilience.md#请求预算-budget) |
| `budget_cut_attribution` | `observe` | `off` / `observe` / `enforce` | 预算截断的归因方式 |
| `first_output_timeout_seconds` | default 20、high 25、xhigh 30、max 30 | 每档 5 至预算总时长 | OpenAI 流式文本热尝试的首输出截止，按推理强度取档 |
| `anthropic_first_event_timeout_seconds` | 90 | 5 至预算总时长 | Anthropic 与 Antigravity 的首事件截止 |
| `gemini_first_event_timeout_seconds` | 60 | 5 至预算总时长 | Gemini 文本请求非最后候选的尝试截止 |
| `model_overrides` | `{}` | 键非空；值 5 至预算总时长 | 按模型覆盖首输出截止，键为精确模型名或以 `*` 结尾的前缀，最长前缀优先；命中时不再按推理强度取档 |
| `same_account_retry_max` | 1 | 0–5 | 一个请求内每个账号的同号重试次数上限 |
| `gemini_same_account_retries` | 1 | 0–5 | Gemini 普通瞬时失败的同号重试次数 |
| `upstream_retry_after_cap_seconds` | 60 | 1–600 | 本请求愿意等待上游恢复的上限，同时受剩余预算约束；超过上限跳过该账号，不截短等待后提前重试 |

### 首包前保活 `keepalive`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 首包前保活 |
| `delay_seconds` | 15 | 5–60 | 请求开始多久仍无输出时发出第一拍 |
| `interval_seconds` | 10 | 5–30 | 心跳间隔 |
| `edges` | `["cf","esa"]` | 每项 1–16 位小写字母、数字或 `-` | 视为 CDN 的边缘名白名单 |
| `skip_official_oauth` | true | 布尔 | 官方 OAuth / Setup Token 账号不发起首包前保活 |

### 停滞检测 `stall`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 停滞检测 |
| `text_stall_seconds` | 120 | 30–280 | 已输出后无有效上游活动多久判定断流 |

Anthropic 上游（原生 Messages 与协议转换）的 `ping` 和 SSE 注释不算活动，用量（包括 0）、正文、控制与结束事件才算。输出前沿用首输出截止；没有首输出截止的读取，以及结束后读完剩余数据的阶段，沿用 `gateway.stream_data_interval_timeout`；不新增阈值。网关发给客户端的心跳不影响这些计时。

### 断开计费 `disconnect`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 断开计费 |
| `cancel_undelivered` | true | 布尔 | 任何输出前断开时立即取消上游 |
| `partial_drain_max_seconds` | 180 | 1–3600 | 部分输出后断开，最多再读取上游多久以取得用量 |
| `abuse_guard.enabled` | true | 布尔 | 未送达滥用保护 |
| `abuse_guard.threshold_per_hour` | 30 | 1–100000 | 同一 API Key 每小时未送达超过此数视为滥用 |
| `abuse_guard.mode` | `bill_upstream_cost` | 只能为 `bill_upstream_cost` | 滥用时按上游成本计费 |

### 错误语义与防风暴 `errors`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 错误语义与防风暴 |
| `no_account_retry_after` | min 2、max 30 | 1 ≤ min ≤ max ≤ 300 | 无可用账号时 `Retry-After` 的范围 |
| `config_retry_after_seconds` | 60 | 1–3600 | 配置原因导致无号时的 `Retry-After` |
| `reservation_retry_after_seconds` | 5 | 1–300 | 余额被在途请求占用时 429 的 `Retry-After`（带抖动） |
| `negative_cache_seconds` | 2 | 1–60 | 无号结论的负缓存时长 |
| `fast_fail` | 开，30 秒内 10 次 | 窗口 1–3600 秒，阈值 2–10000 | 同一 API Key 重复收到相同 503 时快速返回 |
| `turn_cap` | 开，连续 5 次、间隔 120 秒内 | 次数 2–30，窗口 10–3600 秒 | 同一会话连续耗尽失败的回合封顶 |
| `failed_frame_retry_after` | true | 布尔 | 合成的 `response.failed` 按情况附带重试时间 |
| `codex_retry_after_cap_seconds` | 10 | 0–30，0 表示不另设上限 | Codex 客户端收到的 `Retry-After` 上限 |

### 无号兜底分组 `fallback_group`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 无号兜底分组 |
| `return_stable_seconds` | 120 | 1–86400 | 主分组持续健康多久后切回 |
| `binding_ttl_seconds` | 600 | 1–86400 | API Key 进入兜底后的记忆时长 |

### 在途预留 `reservation`

| 字段 | 默认 | 说明 |
|---|---|---|
| `cache_aware_estimate` | true | 余额预留按会话最近一次的缓存读比例估算输入费用 |
| `output_p99_cap` | true | 余额预留的输出 Token 取模型近 7 天 P99（下限 1024） |

### 流式开场与终止 `stream`

| 字段 | 默认 | 说明 |
|---|---|---|
| `prelude_staging` | true | 兼容转换与 Anthropic 直通推迟写出开场事件，正文前失败仍可换号 |
| `native_prelude_whitelist` | true | 原生 Responses 扩展可视为开场的事件 |
| `strict_terminal` | true | 明确识别流的失败终态 |

### 仅流式学习 `capability`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `stream_only_learning` | true | 布尔 | 仅流式账号学习 |
| `learn_ttl_seconds` | 21600 | 600–86400 | 学习结果有效期；修改只影响之后新学到的记录 |

### 独立 function_call_output 兼容 `fco_compat`

Codex 客户端会发出不带 `call_id` 的具名 `function_call_output`（`namespace` 为 `agent`），部分第三方中转会以 400 拒绝这类请求。这一节配置网关如何把内置名单与管理员追加名单中的条目改写为普通用户消息。

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `mode` | `reactive` | `off`、`reactive`、`proactive` | `off` 不处理；`reactive` 在上游以该原因拒绝后改写并在同一账号重试一次；`proactive` 发往第三方中转前直接改写 |
| `admin_names` | `[]` | 最多 64 条，每条匹配 `^[A-Za-z0-9_]{1,128}$` | 追加的 `agent` 命名空间条目名。区分大小写，不去首尾空白（带空白即不合法）；重复项合并，保存后按字典序排列 |

- 内置规则占用的名字不能追加：`browser_info`、`goal_objective`、`untrusted_context`、`ide_context`、`untrusted_content`、`proposed_plan`、`terminal_info`、`browser_tabs`、`codex_apps_open_page`，以及所有以 `codex_apps_selected_page_part_` 开头的名字。
- `PUT` 时本节字段同样“未提交即保留当前值”；一旦提交就必须合法：`mode` 只能是上述三个字符串之一（空串、`null` 或其他类型返回 400），`admin_names` 必须是字符串数组（整体为 `null` 或含非字符串元素返回 400），整节为 `null` 也返回 400。库中文档缺少本节、本节为 `null` 或 `admin_names` 为 `null` 时仍按默认值补齐。
- 账号级覆盖：OpenAI API Key 账号的 `extra.openai_fco_compat_mode` 取 `off`、`reactive` 或 `proactive`；缺失或为 `null` 时继承本节的 `mode`，其他值在创建、编辑、更新 extra 和批量编辑时被拒绝（错误码 `OPENAI_FCO_COMPAT_MODE_INVALID`）。账号编辑页的“继承全局”会删除该键，批量编辑写入 `null`。
- 批量编辑只要带了该键（含 `null`），展开筛选条件后的全部目标都必须是 OpenAI API Key 账号，否则整批拒绝（错误码 `OPENAI_BULK_TARGET_INVALID`），一个都不写。
- CRS 同步与复制账号遇到不合法的值时删除该键（即继承全局），不让同步或复制失败；同步时本地已有的合法值保留。
- 生效模式：总开关 `enabled` 关闭或本节 `mode` 为 `off` 时，所有账号一律按 `off` 处理，账号覆盖不能绕过；否则账号覆盖优先于本节的 `mode`。
- 生效范围：只处理实际走 HTTP Responses 出口、平台为 OpenAI、类型为 API Key、`base_url` 指向第三方中转的账号（WebSocket 入站经桥接走 HTTP 出站时同样适用）。OAuth、官方直连（`api.openai.com` 及其子域、Azure OpenAI）、compact 请求和 Chat / Anthropic 协议上游不处理。
- 改写内容：命中的条目原位替换为一条 `role=user` 的消息，文本为 `<external_名称>原 output</external_名称>`（页面快照类在标签内各加一个换行），其余条目的字节不变。只改写 `call_id` 缺失或为 `null`、`output` 为字符串的条目；`call_id` 为空串或空白、`output` 为数组或对象的条目保持原样。内置名单里除 `untrusted_content`、`proposed_plan` 外的名字以及追加名单要求 `output` 去掉首尾空白后非空；两类页面快照还要求 `output` 符合页面快照格式。请求里任何一层有重复的 JSON 成员时整个请求不改写。请求里的 `function_call_output` 或 `"agent"` 只以 JSON 转义拼写出现（没有任何一处按字面写出）时，整个请求不改写，也不追加这些条目的 output 文本；原有用户内容的关键词检查不变。
- `reactive` 的重试：每个入站请求（WebSocket 为每一轮）最多一次，在同一账号上进行，计入请求的重试预算；已经向客户端输出内容（含首包前的保活帧）或客户端已断开时不重试。一次请求里证实需要改写之后，这次请求后续换到的账号直接按改写后的内容发送。
- `reactive` 与 `proactive` 的取舍：`reactive` 只在被拒后才改写，被拒的那次请求要完整上传两遍；同一会话里中转有时拒绝、有时放行原形，发出的历史前缀随之变化，会降低上游提示缓存的命中。`proactive` 每次都改写，前缀稳定、不重发，适合长期使用。
- 仍被拒绝时：上游以该原因拒绝而网关已没有可做的改写（条目不在名单内、改写后仍被拒、重试额度已用完），客户端收到 400 / `upstream_standalone_tool_output_unsupported`，见[客户端约定](gateway-resilience-client.md)。`off` 以及不在生效范围内的账号仍原样返回上游的错误。
- 内容审计：兼容层追加的这类条目的 output 文本只做关键词检查，不计入也不触发语义审计。站点配置了拦截关键词、内容审计处于“前置拦截”且审计策略不是“仅 API”时，只要本节 `mode` 不为 `off`，请求末尾新到的条目里命中名单的，会以改写后的文本追加进关键词检查（不截断）；Chat 入口用 `input` 而不是 `messages` 时相同。请求里有重复的 JSON 成员时不追加。
  - 只看请求末尾新到的条目：`input` 数组末尾连续的非模型条目（`role` 不是 assistant 的消息，或 `type` 以 `_output` 结尾的条目）。`additional_tools` 和 `compaction_trigger` 不截断这一段，但它们自身不参与检查；遇到其他条目（含 `item_reference`、`{}` 和未知类型）即截止。一轮起始时随用户消息附带的条目、工具循环途中在末尾新出现的独立条目会检查；其后已有模型输出的条目不再检查。
  - 检查发生在选择账号之前，只看全局 `mode`：无论请求最终发往哪类账号、账号是否单独覆盖、是否真的被改写，都会检查。
  - 这些条目是客户端自动附带的上下文（浏览器、终端、IDE 等），命中时按站点现有的关键词规则处理（拦截或仅记录）；只要条目之后还没有模型输出，带着它再发的请求仍会命中。`mode` 设为 `off` 可撤回这项检查。安全审计的提示词检查（prompt guard）不读取这些条目。
- 回滚到不含本节的旧版本后，如果在旧版本的管理页再保存一次韧性设置，本节内容会丢失，升级回来后需要重新填写。
