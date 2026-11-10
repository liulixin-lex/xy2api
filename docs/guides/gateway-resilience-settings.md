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
  "capability": { "stream_only_learning": true, "learn_ttl_seconds": 21600 }
}
```

所有开关都要求总开关 `enabled` 同时开启；关闭总开关时，下表的开关一律视为关闭。

## 字段

### 失败分类 `failure`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `transient_cooldown_seconds` | 30 | 1–3600 | 瞬时失败的账号短冷却时长 |
| `retry_after_cap_seconds` | 120 | 1–3600 | 上游带 `Retry-After` 时，按它冷却，但不超过此值 |
| `balance_keywords_extra` | `[]` | 最多 64 条，每条 2–128 字符 | 追加识别为“上游余额不足”的错误文案关键词 |
| `decision_v2` | true | 布尔 | 失败分类修正；也是仅流式学习后本请求立即换号的前提 |

### 会话 `session`

| 字段 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `enabled` | true | 布尔 | 会话失败改绑 |
| `post_output_rebind` | true | 布尔 | 仅 OpenAI 账号：输出后首次失败即改绑；依赖 `session.enabled` |
| `streak_threshold` | 2 | 1–20 | 同一会话在同一账号上连续失败多少次后改绑 |
| `streak_window_seconds` | 600 | 1–86400 | 连续失败的计数窗口 |
| `avoid_seconds` | 600 | 1–86400 | 改绑后避开原账号的时长 |

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
| `upstream_retry_after_cap_seconds` | 60 | 1–600 | 请求内等待上游给出的重试时间（例如 Antigravity 的短重试）时的上限，同时受剩余预算约束 |

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
| `text_stall_seconds` | 120 | 30–280 | 已输出后无数据多久判定断流 |

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
