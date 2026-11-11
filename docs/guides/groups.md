# 分组

分组决定 API Key 能用哪些账号、按什么价格计费。本文介绍分组上的几项功能：组合分组（按请求模型自动选择供应商）、分组分类（在管理端整理大量分组）、专属标识与分组系统提示词、OpenAI 分组的 Fast 优先级与计费。面向配置分组与套餐的管理员。选号规则与 OpenAI 分组的会话接管见 [调度](scheduling.md)，无号兜底分组见 [罚则与选号](gateway-resilience-routing.md#无号兜底分组)，OpenAI 分组的质量门槛与分组看板见 [分组质量门槛](group-quality-gate.md)。

## 组合分组

组合分组（平台 `composite`，界面显示为 Composite）不绑定单一供应商：网关按请求的模型决定实际使用哪个具体平台，再在该平台的账号中选号。一个 API Key 就能同时调用多个供应商的模型。

### 支持的平台

路由可以指向任一具体平台：`anthropic`、`openai`、`gemini`、`antigravity`、`grok`、`kimi`、`zhipu`、`deepseek`、`minimax`、`opencode_go`、`typesafe`、`command_code`、`cline`。

解析出的具体平台用于账号选择、用户平台额度检查、用量计费、运维错误的平台归属、渠道模型映射与定价查找以及平台用量统计。

### 模型路由

在分组列表中，组合分组行的“路由”操作打开“Composite 路由”弹窗（简易模式下不显示，对应接口也拒绝调用）。接口：

| 方法与路径 | 用途 |
|---|---|
| `GET /api/v1/admin/groups/:id/composite-routes` | 列出路由 |
| `POST /api/v1/admin/groups/:id/composite-routes` | 新增路由 |
| `PUT /api/v1/admin/groups/:id/composite-routes/:route_id` | 修改路由 |
| `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id` | 删除路由 |
| `POST /api/v1/admin/groups/:id/composite-routes/preview` | 预览某个模型会解析到哪里 |

| 字段 | 说明 | 默认 |
|---|---|---|
| `public_model` | 客户端发送的模型名 | 必填 |
| `match_type` | `exact` 或 `prefix`；精确匹配去首尾空白后区分大小写 | `exact` |
| `target_platform` | 目标具体平台 | 必填 |
| `upstream_model` | 发往上游的模型名。精确路由留空时沿用 `public_model`；前缀路由留空时原样转发客户端的模型名 | 空 |
| `endpoint` | `any`、`messages`、`count_tokens`、`responses`、`chat_completions`、`embeddings`、`images`、`gemini` | `any` |
| `priority` | 数字越小越优先；0 按 100 处理 | 100 |
| `enabled` | 停用的路由不参与解析，但仍对管理员可见。`PUT` 时省略该字段会重新启用路由 | true |

解析顺序：

1. 显式路由。多条匹配时，精确优先于前缀，指定入口优先于 `any`，长前缀优先于短前缀，再按 `priority`、路由 ID 从小到大。
2. 分组内账号配置的模型映射。两个平台的账号都提供同一模型时，按无法确定处理。
3. 内置识别（见下）。

JSON 请求体的入口在转发前把 `model` 改写为路由的 `upstream_model`；Gemini 原生路径（如 `/v1beta/models/{model}:generateContent`）解析路径中的 `{model}`。Codex Alpha Search 与 Live 请求使用 `responses` 路由域，Live 从 `session.model` 取模型。Responses WebSocket 按第一个 `response.create` 的模型解析，只支持 OpenAI 与 Grok 目标；后续 turn 可以重复或省略该模型，更换模型需要重新连接。

### 内置识别

匹配不区分大小写，并去掉开头的 `models/`。

| 模型名 | 平台 |
|---|---|
| `claude-*`、`anthropic.claude-*` | Anthropic |
| `gemini-*`、`learnlm-*` | Gemini |
| `gpt-*`、`chatgpt-*`、`o1`/`o3`/`o4`/`o5`（单独或后接 `-`）、`codex-*`、`text-embedding-*`、`dall-e-*`、`gpt-image-*`、`tts-*`、`whisper-*`、`text-moderation-*`、`omni-moderation-*` | OpenAI |
| `grok-*` | Grok |
| `kimi-*`、`moonshot-*`、`k3`、`k3-256k` | Kimi |
| `glm-*` | 智谱 GLM |
| `deepseek-*` | DeepSeek |
| `minimax-*`、`abab5*`、`abab6*`、`abab7*` | MiniMax |
| `jev-*` | TypeSafe |

带供应商前缀的名字按前缀决定平台：`anthropic/`、`claude/`；`openai/`、`chatgpt/`；`google/`、`google-ai-studio/`、`gemini/`；`xai/`、`x-ai/`、`grok/`；`kimi/`、`moonshot/`；`zhipu/`、`glm/`、`bigmodel/`；`deepseek/`；`minimax/`；`typesafe/`、`jev/`。Antigravity 不会被自动识别，需要显式路由。

无法识别或有歧义的模型名直接失败，不猜测供应商：返回 400 `invalid_request_error`，消息为 “Model is not supported by composite groups”。Gemini 原生路径例外，按 Gemini 处理。

### 配置示例：一个套餐覆盖多家模型

1. 为各上游账号池建立具体平台的分组（例如 OpenAI、Claude、Gemini、Grok 各一个），或直接把账号加入组合分组。
2. 新建平台为 Composite、订阅类型为订阅的分组。创建时可以从具体平台分组复制账号；账号的创建、编辑与批量编辑也可以直接加入组合分组。
3. 为套餐别名添加显式路由，例如：

   | 公开模型 | 入口 | 目标平台 | 上游模型 |
   |---|---|---|---|
   | `all/gpt-5` | `responses` | `openai` | `gpt-5` |
   | `all/claude-sonnet` | `messages` | `anthropic` | `claude-sonnet-4-6` |
   | `all/gemini-pro` | `gemini` | `gemini` | `gemini-2.5-pro` |
   | `all/grok` | `responses` | `grok` | `grok-4.3` |

4. 在渠道配置中，按路由的目标具体平台配置模型映射与定价。渠道的 `group_ids` 仍是扁平列表，组合分组出现在各具体平台的分区里。
5. 为组合分组创建订阅套餐。套餐授予组合分组的访问权，每个请求仍按解析出的具体平台计费与检查额度。

标准模型名（`gpt-*`、`claude-*` 等）也可以依赖内置识别；套餐别名建议用显式路由，便于在管理端核对入口、平台与上游模型。

### 限制

- 路由只选择具体平台与上游模型，不会自动生成模型元数据、定价或上游能力记录，目标平台的渠道定价与模型映射需要单独配置。
- 不支持在多个供应商之间按任务自动择优，也不支持把一个 API Key 直接绑定到多个已有分组。

## 分组分类

分组分类是管理端整理分组用的后台标签。一个分组可以加入多个分类，也可以不加入。分类不参与请求路由、调度、计费或用户端响应，也不进入 API Key 鉴权快照；用户端的分组接口不变。

### 管理

- 在分组管理中打开分类管理，可以新增、改名、拖动排序和删除分类。“保存”一次提交全部改动，“取消”放弃未保存的改动。删除仍有分组的分类时需要确认；删除只移除分类关系，不删除分组。
- 分组列表显示分类列（可在列设置中隐藏），并可按分类筛选。创建或编辑分组时可以勾选多个分类；复制分组会复制其分类。分组先保存、分类关系后保存，第二步失败时页面提示“分组已保存、分类未保存”。
- 账号的分组选择器（创建、编辑、批量编辑账号，以及账号列表的分组弹窗）可按分类标签切换显示，只显示包含可选分组的分类；分类筛选只改变可见列表，不会清除其他分类下已勾选的分组。
- 账号列表的“归组”一格最多显示两个分组徽章，其余收进“+N”。点击它打开分组弹窗，确认时新选的分组追加在原有分组之后。
- 简易模式不显示分类界面，但分类数据保留，接口仍可使用。
- 分类名称只是标签：名为“专属分组”的分类不会改变分组的专属属性。

### 限制

名称去首尾空白后 1–32 个字符，不能含控制字符，不区分大小写去重；最多 50 个分类；一个分组最多加入 50 个分类。

### 管理接口

以下路径均以 `/api/v1/admin` 为前缀：

| 方法与路径 | 用途 |
|---|---|
| `GET /group-categories` | 获取分类及每个分类的分组 ID |
| `PUT /group-categories` | 按 `items` 的顺序新增、改名和排序；只删除 `deleted_ids` 中列出的分类 |
| `PUT /group-categories/groups/:group_id` | 用 `category_ids` 替换一个分组的分类，不存在的分类 ID 忽略 |

查询参数 `group_category=<id>` 可用于 `GET /groups`、`GET /accounts`、`GET /accounts/upstream-billing-rates`、`GET /accounts/data`，以及 `POST /accounts/bulk-update` 的 `filters.group_category`。账号匹配条件是账号绑定的任一未删除分组属于该分类。空值表示不筛选，有效但不存在的分类 ID 返回空列表。

| 错误码 | 状态 | 条件 |
|---|---|---|
| `GROUP_CATEGORY_NAME_INVALID` | 400 | 名称为空、超过 32 个字符、含控制字符或不是合法 UTF-8 |
| `GROUP_CATEGORY_NAME_DUPLICATE` | 409 | 保存后的名称不区分大小写重复 |
| `GROUP_CATEGORY_LIMIT` | 400 | 保存后的分类总数超过 50 |
| `GROUP_CATEGORY_IDS_INVALID` | 400 | ID 非正数、重复或数量超过 50 |
| `INVALID_GROUP_CATEGORY_FILTER` | 400 | 筛选值不是正整数 |
| `GROUP_NOT_FOUND` | 404 | 分组不存在或已删除 |

并发保存按最后一次生效；请求中没有列出、也没有列入 `deleted_ids` 的并发新增分类会保留，排在本次提交的分类之后。

## 专属标识与分组系统提示词

### 专属标识

分组的“专属 / 公开”（`is_exclusive`）决定权限；“向用户显示专属标识”（`show_exclusive_badge`，默认开启）只决定用户端是否展示紫色“专属”标签，隐藏标识不改变专属权限、订阅要求或授权名单。

- 用户在“API 密钥”页创建、编辑密钥或切换分组时，开启该开关的专属分组在选项和已选分组上带“专属”标签。分组说明只显示一行，悬停显示完整内容。
- 管理员在账号与公告的分组选择中始终看到真实的分组类型，不受该开关影响。

### 分组系统提示词

在分组的创建或编辑页设置“系统提示词”，网关会把它注入到发往上游的请求中。标准模式与简易模式都可配置；普通用户的分组信息、模型广场与可用渠道响应不包含提示词。

```json
{
  "prompt": "分组通用提示词",
  "scope": "selected",
  "models": ["team-alias"],
  "model_prompts": {
    "other-model": "这个模型的独立提示词"
  }
}
```

| 字段 | 界面名称 | 说明 |
|---|---|---|
| `prompt` | 分组通用提示词 | 为空时不注入通用提示词 |
| `scope` | 通用提示词应用范围 | `all`（全部模型，默认）或 `selected`（指定模型）；`selected` 且通用提示词非空时至少选一个模型 |
| `models` | 应用模型 | 通用提示词适用的模型 |
| `model_prompts` | 模型独立提示词（覆盖通用） | 按模型的独立提示词，优先于通用提示词，可用于通用名单之外的模型；内容清空即回到通用规则 |

- 模型名去首尾空白后精确匹配、区分大小写，不支持通配符；匹配的是客户端调用的模型名（在组合分组路由与模型映射之前）。模型名不能为空或重复，否则返回 400 `INVALID_SYSTEM_PROMPT_CONFIG`。
- 编辑分组时不提交该配置则保留原值，提交完整的空配置即清除；复制分组会一并复制。

注入位置：

| 协议 | 位置 |
|---|---|
| Chat Completions | `messages` 最前面的 `system` 消息 |
| Responses | `instructions` 前缀，与原值之间空一行 |
| Anthropic | `system` 的字符串前缀或文本块，排在平台固定的前导块之后 |
| Gemini、Code Assist、Antigravity | `systemInstruction.parts` 前置文本 |

- 原有的 system、developer 消息、结构化对话、工具定义与缓存控制块都保留；配置未命中时请求原样转发。换号重试时从尚未注入的原始请求重新构造，不会重复注入。
- 文本生成及其 token 计数路径使用相同的提示词。Responses WebSocket 在每个 `response.create` 注入一次；连接使用建立时的配置快照，修改对新连接生效。
- 图片、视频、实时音频与 Live 接口，以及 Gemini 的图片输出不注入。
- 请求由 [无号兜底分组](gateway-resilience-routing.md#无号兜底分组)承接时，使用兜底分组的提示词。
- 修改通过缓存失效广播刷新到其他实例。

## OpenAI 分组 Fast

OpenAI 分组与组合分组的编辑页有“OpenAI Fast 模式”设置，两个开关默认都关闭，其他平台的分组不保存它们：

- **强制使用 Fast（priority）**（`force_openai_fast`）：只在请求实际由 OpenAI 账号服务时生效，组合分组中其他平台的请求不受影响。
- **免费 Fast**（`free_openai_fast`）：Fast 请求按普通档价格向用户收费。

### 优先级

**分组强制 Fast > 用户专属 Fast/Flex 规则 > 全局 Fast/Flex 规则。**

全局与用户专属规则在 **系统设置 → 网关服务 → OpenAI Fast/Flex 策略**（设置键 `openai_fast_policy_settings`）中配置：按 `service_tier`（全部 tier 值、省略 tier、priority、ultrafast、flex）、账号范围与目标模型匹配，处理方式为透传（`pass`）、过滤（`filter`，移除 `service_tier`）、强制 priority（`force_priority`）或拦截（`block`，返回 403 `permission_error`）。填写了“指定用户”的规则即用户专属规则，选中用户的请求先匹配它们；同层按配置顺序首条命中。

- 分组强制 Fast 开启：最终请求固定为 `service_tier=priority`。客户端未指定档位，或指定 default、flex、ultrafast 等，都使用 priority；全局与用户专属规则不再覆盖它。
- 分组强制 Fast 关闭：完整沿用上述规则。
- 该优先级只涉及 Fast/Flex 策略，不绕过认证、分组状态、模型白名单或其他请求校验。
- HTTP 原生 Responses、Chat 与 Messages 转换、透传和 WebSocket（只处理 `response.create`）使用同一策略。

### 只为一个分组开启 Fast

1. 全局 OpenAI Fast/Flex 策略保留“全部 tier 值 / 过滤 / 全部账号”，它已覆盖 Fast 与 Flex。
2. 只在目标分组开启“强制使用 Fast”。
3. 要按 Fast 单价收费时关闭“免费 Fast”；分组倍率按原业务设置。
4. 保存后新请求生效；已建立的 WebSocket 会话重新连接后使用新配置。

“过滤”只删除转发的参数，不能控制第三方上游自身强制的档位；请求不含 `service_tier` 也不能证明上游实际使用普通档。只应为确认支持或强制提供 Fast 的上游开启分组强制 Fast。

### 计费

分组倍率与服务档位单价分开计算：

```text
total_cost  = Σ 各类 Token 用量 × 对应的 Fast 单价
actual_cost = total_cost × 分组倍率
```

例如某次请求普通档费用为 1 USD，模型 Fast 单价为普通档的 2 倍，分组倍率 0.2：`total_cost=2`、`rate_multiplier=0.2`、`actual_cost=0.4`。不要用把分组倍率改成 0.4 的方式代替档位计费。Fast 价按模型目录或分组、渠道的明确配置选取，不会对已有的 Fast 费用再乘一次档位系数。

- 强制 Fast 分组以最终发往 OpenAI 的 priority 作为结算档位：兼容上游即使回报 default，也按 priority 计费；上游原始档位保留在转发结果中，不改写上游响应。
- 未开启强制 Fast 时保留原有结算规则：API Key 上游可以声明降档，但不能通过响应提高请求的计费档位。最终请求没有 priority 时，不会凭空补记 Fast 费用。
- 免费 Fast：保留 priority 档位与对应的 `total_cost`，但 `actual_cost` 按同一请求的普通档费用乘分组倍率（含用户的分组专属倍率与高峰系数）计算。
- 长上下文规则仍按现有阈值与输入、输出系数叠加，与 Fast 档位独立。
- 核对账单时同时检查最终转发参数、`service_tier`、`total_cost`、`rate_multiplier` 与 `actual_cost`，而不只比较实扣金额。

如果以前通过提高分组倍率来代替 Fast 收费，开启分组强制 Fast 后要把倍率恢复为业务原值，避免重复加价。
