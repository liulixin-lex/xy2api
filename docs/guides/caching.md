# 提示词缓存

本文说明 XY2API 与上游提示词缓存相关的全部行为：Claude 请求的自动补缓存断点、Anthropic 缓存 TTL 偏好、缓存用量的计量与计费，以及 OpenAI 请求的缓存计量、会话标识与缓存感知选路。管理员用它决定是否开启这些功能、如何核对缓存费用；调用方用它了解网关会不会改动请求里的缓存声明。

## 概览

| 功能 | 作用对象 | 配置位置 | 默认 |
|---|---|---|---|
| 自动补缓存 | 实际转发给 Claude 的 Anthropic Messages 请求 | 「设置 → 网关服务 → Claude 缓存」 | 关闭 |
| 缓存 TTL 偏好 | 同上 | 管理员默认值在同一卡片；用户在 API 密钥中选择 | 全局 5 分钟；新密钥“跟随全局” |
| 缓存写入计费分类覆盖 | 单个账号的结算 | 账号设置 | 关闭 |
| OpenAI 缓存计量 | OpenAI Responses、Chat 转换、HTTP SSE、WebSocket | 始终生效 | — |
| 统一 OpenAI 会话标识 | OpenAI API Key 账号 | 「设置 → 网关服务 → 请求转发行为」 | 开启 |
| OpenAI 缓存诊断与缓存感知选路 | OpenAI 调度 | `gateway.openai_cache` | 关闭 |
| Anthropic / Gemini 稳定兜底会话键 | 没有会话标识的请求 | 无开关 | 始终生效 |

自动补缓存、TTL 偏好和计费分类覆盖各自配置，不会互相开启。网关接受缓存声明不代表上游一定创建或命中缓存：最小缓存长度、前缀变化、请求间隔、账号路由与上游实现都会影响结果，请以上游返回的 `cache_creation_input_tokens` 和 `cache_read_input_tokens` 为准。XY2API 的用量不能替代上游供应商的独立账单。

## Claude 自动补缓存

### 启用

在「设置 → 网关服务 → Claude 缓存」卡片打开「自动补缓存」，在「应用分组」中选择一个或多个分组并保存。选中分组下的所有账号、调用密钥和模型都纳入范围，不需要填写规则名称、上游地址或模型名单。保存只提交改动过的项，只改缓存时长不会重新校验分组；保存不会调用上游，也不会预热缓存。

配置保存在设置 `claude_cache_fallback_policy` 中：

```json
{"enabled": false, "group_ids": []}
```

- `group_ids` 最多 1000 个互不重复的正整数；启用时至少选择一个分组，且每个分组都必须存在，否则返回 400 `INVALID_CLAUDE_CACHE_SCOPE`。未知字段会被拒绝；省略该设置的部分更新保留原值。
- 本实例保存后立即生效，其他实例最长约 60 秒刷新。读取配置失败时视为关闭，不补断点。
- 关闭并保存即可停用；已发送的请求照常结算。分组删除后仍可关闭保存。

### 适用范围

只对账号平台为 Anthropic、且模型确实是 Claude 的请求生效：

- Anthropic 原生 Messages（API Key、OAuth / SetupToken、API Key 透传）；
- Vertex 原生与转换路径；
- Bedrock 原生 Messages（Bearer / SigV4）；
- 经 Anthropic / Vertex 转换的 Chat Completions 与 Responses 请求。Chat / Responses → Bedrock 没有对应的转换构造器与 eventstream 解码器，这类组合可能返回上游认证错误或通用转发错误，而不是明确的“不支持”。

分组中的 OpenAI、Gemini 等其他协议请求以及 `count_tokens` 保持原行为。这是协议边界，不是模型或账号白名单。

### 补断点规则

客户端原始请求在协议位置已经带 `cache_control` 时（包括 `null` 或非法声明），不再添加断点；协议转换与非法字段净化仍按原规则执行。以下位置的标记也算客户端声明：Responses `input` 中工具调用与结果项（`function_call`、`custom_tool_call`、`tool_search_call`、`tool_search_output`、`function_call_output`、`custom_tool_call_output`）的项级标记，`function_call_output`、`custom_tool_call_output` 的数组 `output` 分片上的标记，以及 Chat 工具消息上的标记。

原始请求没有声明时，在完成既有处理和分组 system 提示词注入之后，按剩余断点额度依次尝试：

1. 最后一条消息（user 或 assistant）中最后一个可缓存块；
2. system 的最后一个可缓存文本块；
3. tools 中最后一个非延迟加载的工具；
4. 消息不少于四条时，倒数第二条 user 消息的最后一个可缓存块。

- 已有的网关 / OAuth 断点与顶层 automatic 声明保留，并共同计入最多 4 个断点的额度。
- 空文本、`thinking`、`redacted_thinking` 和未知块类型不会被标记；字符串内容会转换为等价的 text 块，内容和顺序不变。
- 补上的断点为 `{"type":"ephemeral"}`，即协议默认的 5 分钟，不添加顶层自动缓存字段；之后由 TTL 偏好统一已有断点（强制 1 小时时也包括补上的断点）。会破坏“长 TTL 在短 TTL 之前”顺序的插入会被跳过。
- 改动只属于本次发送，不影响同号重试或换号时使用的原始请求体。

### 范围冻结

请求进入时冻结所选分组范围，发送前再检查当前开关与分组：中途开启不能扩大已接纳请求的范围，关闭或移除分组会阻止之后的补齐。账号必须属于该有效分组；可用性兜底分组不继承原分组的授权。

## Anthropic 缓存 TTL

### 管理员默认值

「Claude 缓存」卡片中的「缓存时长（跟随全局的密钥）」设置全局默认 TTL，设置键 `anthropic_cache_ttl_default`，只接受 `5m`（默认）或 `1h`，其他值返回 400。

### API 密钥偏好

用户在 API 密钥的创建、编辑弹窗中选择偏好，字段为 `anthropic_cache_ttl_mode`：

| 取值 | 界面 | 行为 |
|---|---|---|
| `inherit` | 跟随全局（显示当前默认） | 接纳请求时读取一次管理员默认值，按对应的强制项处理。全局为 5 分钟时，客户端自己写的 1 小时也会改为 5 分钟 |
| `off` | 不覆写 TTL（保留客户端设置） | TTL 处理本身不增删、不改写缓存声明 |
| `5m` | 强制 5 分钟缓存 | 已有的合法 ephemeral 声明统一为 5 分钟 |
| `1h` | 强制 1 小时缓存 | 已有的合法 ephemeral 声明统一为 1 小时 |

- 创建密钥时省略该字段为 `inherit`；更新时省略保持原值；显式空串或非法值返回 400 `INVALID_ANTHROPIC_CACHE_TTL_MODE`。TTL 功能上线前创建的密钥为 `off`，用户可自行修改。
- `off` 不表示关闭上游缓存，也不关闭分组补断点或 OAuth 既有断点。没有任何断点且没有其他注入路径时，选择 1 小时也不会创建缓存；切换偏好不会清除上游已有的缓存。
- 选项只在适用的分组中显示：网关类型为 Anthropic 的分组（如 Claude、TypeSafe）、组合分组，以及平台清单中有 `claude-*` → Anthropic 路由的多协议分组；其他分组隐藏该选项，切换分组时保留偏好。混合分组中只对实际转给 Claude 的请求生效，不根据分组名或 Anthropic 兼容端点猜测供应商。
- 密钥列表返回 `meta.anthropic_cache_ttl_default`，只公开这个默认值。
- 修改密钥后按既有鉴权缓存机制失效；全局设置保存后本实例立即生效，其他实例随设置缓存刷新。已接纳的请求及其重试使用冻结的快照。

按官方价格，5 分钟缓存写入约为普通输入的 1.25 倍，1 小时约为 2 倍；1 小时缓存只在同一对话的请求间隔为 5–60 分钟时才划算。实际价格以本站报价为准。

### 处理顺序与边界

协议转换、模型映射、正文整理、分组 system 提示词与补断点先执行，然后统一 TTL、执行上游能力清理，最后构造并签名 HTTP 请求。是否适用取决于实际账号、映射后的模型与出站协议；Anthropic 兼容接口承载 Kimi、DeepSeek、GLM 等模型时不应用 Claude TTL。

- 只访问顶层、tools、system 与 messages 内容块的协议字段，不递归修改工具 schema、工具入参或用户数据；保留合法声明的类型、其他属性、内容与断点位置。
- Chat / Responses 转换保留工具调用与结果上的缓存标记：Chat `role=tool`（以及旧版 `role=function`）消息的消息级标记、或其内容分片上的最后一个标记，落在对应的 `tool_result` 块；只含 `tool_calls` 的 assistant 消息的标记落在最后一个 `tool_use` 块。Responses `function_call_output`（含降级后的 `custom_tool_call_output`）以项级标记为准，否则取数组 `output` 中最后一个带标记的分片；`function_call` 的项级标记落在 `tool_use`。每个工具块只保留一个标记。这些标记只随 Anthropic 转换转发，不加入发往 OpenAI Responses 上游的请求。
- 顶层 automatic 与显式断点共占最多 4 个槽位，空间不足时不新增断点，也不删除合法的客户端声明。网关构造的 Chat / Responses 转换请求中，顶层 automatic 与末块显式标记一个为 1 小时、另一个为 5 分钟或省略时统一为 1 小时，并把之前的 ephemeral 断点提升到 1 小时，以保持“长 TTL 在前”的顺序。`off` 保留合法的混合 TTL。
- `thinking`、`redacted_thinking`、空文本与延迟加载的工具不能新增缓存标记；旧的 Bedrock 集成不支持顶层 automatic；能力清理移除的字段不会在签名前补回。
- `/v1/messages/count_tokens` 不执行 TTL 覆写或补断点，也不创建、刷新缓存。

官方 1 小时缓存只需 `{"type":"ephemeral","ttl":"1h"}`，不需要 `extended-cache-ttl-2025-04-11` beta。XY2API 默认不添加该 beta，只在 OAuth / SetupToken 模拟 Claude Code 的固定 beta 列表中发送；API Key 与透传账号原样转发客户端的 beta，Vertex / Bedrock 的 beta 白名单不含它。

## 缓存用量与计费

### 用量解析

上游返回的 `cache_creation.ephemeral_5m_input_tokens`、`ephemeral_1h_input_tokens` 及其是否出现，会贯穿原生请求与 Anthropic / Vertex 转换的流式和非流式用量（Bedrock 仅原生 Messages）。后续事件省略字段时保留先前值，显式 0 清零；累计事件不相加。总创建量缺失时可由明确的子桶合计；明确的 0 仍为 0。OpenAI 格式的响应不添加非标准 TTL 字段。

请求了 1 小时不代表上游实际写了 1 小时，缺失明细时也不会按密钥偏好填桶。设总创建量为 C、5 分钟桶为 A、1 小时桶为 B：

| 上游报告 | 结算 |
|---|---|
| A + B = C | 各自按本站报价计费 |
| 全部 TTL 明细缺失 | 未知部分按基础缓存写入价（5 分钟档）计费 |
| 0 < A + B < C | 已知桶按各自价格，差额按基础写入价补齐 |
| A + B > C > 0 | 按确定性分配规则以 C 封顶，并记录不一致 |
| C 明确为 0、A + B > 0 | 创建总量按 0 封顶并记录不一致 |

不追补历史费用。渠道显式价格、用户与分组倍率、账号统计价格和长上下文规则继续生效；渠道只配置一个缓存写入价时，两种 TTL 同价（渠道可单独配置 1 小时写入价）。

### 计费调整

- 账号的「缓存写入计费分类覆盖」（`extra.cache_ttl_override_enabled`、`extra.cache_ttl_override_target`，目标 `5m` 或 `1h`）只调整结算分类，不反写上游响应，使用记录中以 `cache_ttl_overridden` 标记“计费调整”。粘性会话换号时的强制缓存计费（ForceCacheBilling）同样只作用于结算副本。这两类调整后的桶不能当作真实的缓存效率。
- 本功能不会因为请求里有缓存字段就假定命中、伪造用量或额外扣费。

### 余额预留

首次缓存写入可能比普通输入更贵。余额预留会结合冻结的 TTL 策略、客户端声明与既有注入路径，区分无风险、5 分钟、1 小时三种冷写风险；有风险时取普通输入价与本站对应 TTL 写入价中的较高者，并按调度快照中账号映射后的模型取上界，不使用历史命中率折扣。没有断点也没有注入路径时，不会因为选择 1 小时而提高预留。预留可能暂时多占余额，**不是实际扣款**，最终按上游用量结算。

## 观测

用户与管理员的用量接口包含两个可空字段：

| 字段 | 含义 |
|---|---|
| `anthropic_cache_ttl_policy` | 最终结算那次尝试适用的冻结策略 `off` / `5m` / `1h`；非目标出口与历史记录为 `NULL` |
| `anthropic_cache_ttl_applied` | 最终能力清理后，至少有一个合法 ephemeral 声明且全部符合指定的 5m / 1h 时为 `true`（原值已相同也算）；`off`、无断点或未满足时为 `false`；非目标出口与历史记录为 `NULL` |

使用记录页面不显示这两个字段；`NULL` 表示未记录，不等于未应用。“已应用”只描述最终请求，不证明上游采用了该 TTL 或命中缓存。上游 TTL 明细缺失或部分缺失时，Token 详情会显示相应的计费提示。缓存读取占比的显示由设置 `admin_usage_cache_hit_rate_enabled`、`usage_cache_hit_rate_public` 控制，见 [用量指标与导出](usage.md#缓存读取占比)。

结构化日志不记录正文或凭据：

- `gateway.claude_cache_fallback`：补断点的原因，取值有 `scope_mismatch`、`client_declared`、`invalid_body`、`final_declared`、`no_cacheable_content`、`injected`（`disabled`、`unsupported_route` 不记录）。
- `gateway.claude_cache_fallback.billing`：每个带 TTL 策略的 Claude 请求都会记录，包含最终策略 `ttl_policy`、应用结果 `ttl_applied`、`ttl_mode`、`ttl_source`、上游原始与计费后的各桶用量、`force_cache_billing`、`ttl_override` 与结算结果。`ttl_status` 取第一个成立的值：`missing`（有创建量但无 TTL 明细）、`partial`（明细之和小于总量）、`inconsistent`（明细之和大于总量）、`downgraded`（策略为 1 小时且已应用，上游却只报告 5 分钟写入），否则为 `consistent`。

`downgraded` 说明上游或中转没有按 1 小时写入；官方 1 小时不需要额外 beta，不要靠添加 beta 处理。判断策略以 `ttl_policy` 为准。

### 1 小时降级巡检

按账号统计策略为 1 小时且已应用、上游却只报告 5 分钟写入的请求。启用了计费分类覆盖的行会改写 5m / 1h 桶，查询中排除。需要按模型或入口拆分时，把 `u.model`、`u.inbound_endpoint` 加入 `s` 和 `GROUP BY`。

```sql
WITH s AS (
  SELECT
    u.account_id,
    u.created_at >= now() - interval '24 hours'                  AS d1,
    u.cache_creation_tokens > 0                                  AS wrote,
    u.cache_creation_5m_tokens > 0 AND u.cache_creation_1h_tokens = 0
      AND u.cache_creation_5m_tokens = u.cache_creation_tokens  AS downgraded,
    u.cache_creation_tokens > 0
      AND u.cache_creation_5m_tokens + u.cache_creation_1h_tokens = 0 AS missing,
    u.cache_creation_5m_tokens AS c5m,
    u.cache_creation_1h_tokens AS c1h
  FROM usage_logs u
  WHERE u.created_at >= now() - interval '7 days'
    AND u.anthropic_cache_ttl_policy = '1h'
    AND u.anthropic_cache_ttl_applied IS TRUE
    AND u.cache_ttl_overridden IS FALSE
)
SELECT
  s.account_id,
  acc.name                                                     AS account_name,
  count(*) FILTER (WHERE d1 AND wrote)                         AS writes_24h,
  count(*) FILTER (WHERE d1 AND downgraded)                    AS downgraded_24h,
  count(*) FILTER (WHERE wrote)                                AS writes_7d,
  count(*) FILTER (WHERE downgraded)                           AS downgraded_7d,
  count(*) FILTER (WHERE missing)                              AS ttl_missing_7d,
  round(100.0 * count(*) FILTER (WHERE downgraded)
        / nullif(count(*) FILTER (WHERE wrote), 0), 1)         AS downgraded_pct_7d,
  sum(c5m)                                                     AS creation_5m_7d,
  sum(c1h)                                                     AS creation_1h_7d
FROM s
LEFT JOIN accounts acc ON acc.id = s.account_id
GROUP BY s.account_id, acc.name
HAVING count(*) FILTER (WHERE wrote) > 0
ORDER BY downgraded_7d DESC, writes_7d DESC;
```

## OpenAI 缓存

### 用量计量

Responses、Chat Completions 转换、HTTP SSE 与原生 WebSocket 的缓存读取和写入都从上游真实返回的 usage 解析，不根据请求大小、缓存选项或历史命中推算，也不自动发送额外的模型请求。

- **读取**按以下优先级取第一个存在的值：`input_tokens_details.cached_tokens`、`prompt_tokens_details.cached_tokens`、`cache_read_input_tokens`、`cache_read_tokens`、`cached_tokens`。
- **写入**按以下优先级：`input_tokens_details` / `prompt_tokens_details` 中的 `cache_write_tokens`，再是两者中的 `cache_creation_tokens`，然后是顶层的 `cache_write_tokens`、`cache_creation_input_tokens`、`cache_write_input_tokens`、`cache_creation_tokens`。
- 显式的 0 优先于后续别名，负数按 0 处理；缺失、`null` 与非数字值不算命中证据。结构错误的终态 usage 仍会被协议校验拒绝。
- 协议转换后保留明确的 `cached_tokens: 0`，并统一为一个规范的写入值，避免客户端看到互相矛盾的写入别名。

### 请求中的缓存参数

- API Key 账号原样保留客户端指定的 `prompt_cache_key`、`prompt_cache_retention`、`prompt_cache_options` 和 `safety_identifier`。其他 OpenAI 账号（Codex OAuth 协议）会去掉后三项，Debug 日志记录字段名与原因；Grok 会去掉 `prompt_cache_retention`、`safety_identifier` 与 `metadata`。
- 网关不会自动添加 24 小时 retention，也不替用户选择缓存模式。API Key 是透明转发边界，参数被保留不代表代理背后的所有模型都支持；各供应商的能力请在目标端点上验证。OpenAI 的规则见官方的 [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)。

### 统一 OpenAI 会话标识

「设置 → 网关服务 → 请求转发行为」中的「统一 OpenAI 会话标识」（设置键 `enable_openai_session_uuid`，默认开启）只作用于 OpenAI API Key 账号：

- **兼容路径**：Chat Completions 与 Anthropic Messages 转 Responses 时，如果会话键由网关自动生成，请求体中的 `prompt_cache_key` 使用与 `session_id` 头相同的 UUID。
- **原生 HTTP `/v1/responses`**：请求既没有 `prompt_cache_key` 字段、也没有显式会话头时，网关用租户隔离的路由身份派生一个 UUID 填入 `prompt_cache_key`，不写会话头。前提是配置了 JWT secret；compact、WebSocket、透传、OAuth、Grok 以及模型门之外的模型不补。
- **客户端显式值**：请求体带 `prompt_cache_key`（包括空值）或带显式会话头时，行为与关闭该设置完全相同。
- **例外**：质量路由轮换生效时，请求体使用轮换值，可能与 `session_id` 头不同；OpenCode 的 `X-OpenCode-Session` 按原有来源顺序派生。
- 某个上游不接受该字段时，关闭此设置即可恢复原行为。开启或关闭后，上游的粘性分区和缓存分区会重建一次。

### 粘性会话

已认证 GPT 会话的本地粘性绑定按 API 密钥、分组、客户端请求的模型和稳定会话信号隔离，经持久 JWT secret 做 HMAC 后生成带版本的 `v2:` 键。绑定由 Redis 脚本维护：首次写入者持有，只有当前所有者能续期和删除。并发的冷请求会读取首个所有者并重新选择，释放临时抢到的槽位；所有者确实没有容量时，单次溢出不改变长期绑定。准入前的占位最多保留 30 秒。

无显式身份时，内容回退只使用开头连续的 system / developer 指令、工具定义和首条 user，之后动态变化的 system / developer 消息不改变已有身份。网关不改写用户的提示词或工具顺序，前缀复用仍取决于客户端保持内容不变。利润门模式下，准入成功后才绑定；`previous_response_id`、任务所有者、质量保护与权重调度的优先级仍高于缓存。

### 缓存诊断与缓存感知选路

```yaml
gateway:
  openai_cache:
    diagnostics_enabled: false
    aware_routing_enabled: false
    rollout_percent: 0
```

对应环境变量 `GATEWAY_OPENAI_CACHE_DIAGNOSTICS_ENABLED`、`GATEWAY_OPENAI_CACHE_AWARE_ROUTING_ENABLED`、`GATEWAY_OPENAI_CACHE_ROLLOUT_PERCENT`。`rollout_percent` 取值 0–100，按稳定的认证会话选择灰度样本。

- **诊断**：日志区分冷请求、间隔超过 30 分钟、前缀变化、账号变化和可比的续请求，记录原始 usage 与结算后的 token 桶和成本。会话、前缀与请求 ID 只记录 HMAC，不记录提示词、客户端缓存键或会话头。前缀指纹只概括稳定的 instructions、tools、格式、reasoning 与开头的 system / developer 内容，不做上游 tokenizer 级别的比较，“可比”只是近似条件，实际命中以返回的缓存 usage 为准。两项都关闭时不会额外解析大请求的前缀。
- **缓存感知选路**：只用于没有有效绑定的新选择，只在既有优先级、负载等排序条件相同的候选之间打破平局。每个候选需要至少 20 个最近 30 分钟内的可比续请求样本，否则保持原顺序；只用输入不少于 1024 token、带明确缓存 usage 且桶总量合理的请求训练。学习数据保存在进程内（最多 2048 个会话、4096 个候选统计），重启后重新积累，多实例结果可能不同。评分只作启发式排序，不替代自定义价格、上游账单或利润计算。
- **建议**：先只开启诊断，按模型、账号、缓存模式、格式和长度比较相同前缀的续请求；再从 1% 开启选路偏好，观察缓存 token 比例、首字延迟、错误与切换率、结算与上游账单。回退只需关闭 `aware_routing_enabled` 并把比例归零。

## Anthropic 与 Gemini 稳定兜底会话键

这项处理没有开关，只用于既没有分享调度锚点、也没有 `metadata.user_id` 会话的请求；Claude Code 这类带 metadata 的客户端不受影响。

- **组成**：客户端 IP、规范化后的 User-Agent、API 密钥、分组，以及规范化后的 system 与首条 user 文本，一起计算哈希。
- **规范化**：去掉 `<system-reminder>` 标签内的内容、开头的 billing 行与多余空白；首条 user 只剩 reminder 时，顺延到下一条有文本的 user。
- **入口一致**：`count_tokens` 与 Gemini 原生入口使用与 Messages 相同的算法，同一段内容得到同一个键。
- **边界**：system 中随日期变化的句子会让键每天最多变一次；cwd、git status 这类内容不做通用删除；客户端换 IP 会换键；同一租户、同一台机器上开头完全相同的两段对话会共用一个账号。

## 升级与回滚

- 迁移 `286_anthropic_cache_ttl.sql` 为 `api_keys` 增加 `anthropic_cache_ttl_mode`（存量密钥为 `off`，新密钥默认 `inherit`，带四值约束），为 `usage_logs` 增加两个可空列，不回填、不加索引。PostgreSQL 加列与加约束需要表锁，约束还可能扫描存量行，请按表规模预留低峰窗口。
- 旧版的全局 1 小时注入、消息断点清理重建与 OAuth 计费补贴已退出运行路径。依赖这些旧行为的部署需要改为配置分组自动补缓存；需要保留补贴时，改用账号的「缓存写入计费分类覆盖」。旧版精确规则升级后只保留分组选择，开关为关闭，需要管理员重新开启。
- 迁移 `280_drop_cache_northstar.sql` 删除 `cache_trace_hourly`、`cache_observability_events`、`cache_northstar_hours`、`cache_northstar_rollup_state`、`cache_northstar_hourly` 五张缓存观测表及其索引和序列，数据不可恢复，升级前请按常规流程备份。
- 回退到旧版本前，先停用自动补缓存与新的 TTL 偏好入口；旧程序可以忽略新增列，但不执行新的 TTL 语义，不能视为行为等价。不需要删除新列，也不回算历史账单。
