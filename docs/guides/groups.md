# 分组

分组决定 API Key 能用哪些账号、按什么价格计费。本文介绍分组上的几项功能：组合分组（按请求模型自动选择供应商）、分组分类（在管理端整理大量分组）、专属标识与分组系统提示词、OpenAI 分组的 Fast、Ultrafast 策略与计费。面向配置分组与套餐的管理员。选号规则与 OpenAI 分组的会话接管见 [调度](scheduling.md)，无号兜底分组见 [罚则与选号](gateway-resilience-routing.md#无号兜底分组)，OpenAI 分组的质量门槛与分组看板见 [分组质量门槛](group-quality-gate.md)。

## 倍率区间与固定号

OpenAI 分组可在利润设置里填写“最低上游倍率”（`min_upstream_rate`），绝对倍率、最多 4 位小数，留空表示不设。它独立于利润开关，填了就生效；利润控制开启时，最低毛利率与安全缓冲再换算出上游倍率上限。非固定号必须同时满足已启用的上下限，含粘性、重试、末位兜底与排队终检。

- 预览显示低于下限的号数、固定号数、区间是否为空和固定号亏损名单。区间为空时，除固定号外没有账号可用；配置了可用兜底分组才可能转投，否则请求失败。修改已有分组时，这次改动会让分组除固定号外没有可用账号，保存前要求确认。
- 原属主低于新下限时，老会话改选区间内账号，新号成功后粘住。上限否决保留原绑定；固定号免上下限，仍受其他准入条件与[质量三条线](group-quality-gate.md)约束。
- 看板“固定”是分组 × 账号的成员设置。固定号排在**同层**最前，不会跨质量层抢先，固定开关不会触发老会话接管，接管仍只比较优先级。高于利润上限时，预览明确提示亏损。
- 从其他分组**复制账号**：目标里原有成员的固定值保留，新加入的账号不固定；源成员为空时目标成员会被清空。**复制整个分组**则连同下限与固定设置复制。
- 复制使用等锁前冻结的源成员快照，同一目标分组的成员替换串行执行。删除账号、固定设置与成员写入采用一致的加锁次序，避免并发时互相等待造成死锁。固定设置和成员变化按新版本更新调度缓存，迟到的旧缓存不会覆盖新设置。
- 上游倍率优先取能算出有效值的新鲜探测结果，算不出时回退存储倍率；实时准入与排序用同一口径。可服务数有短暂缓存，修改成员或固定后可能稍后才反映。

| 接口 / 字段 | 用途 |
|---|---|
| 分组创建、更新的 `min_upstream_rate` | 创建留空不传；更新 `0` 清除，`null` 或不传不改；读取为数值或 `null` |
| `PUT /api/v1/admin/groups/:id/members/:account_id` | 请求 `{"pinned":true}` 或 `{"pinned":false}`；需要 `groups:manage` |
| `GET /api/v1/admin/groups/:id/profit-control-preview?margin=&floor=` | `floor=0` 表示不设；返回 `below_floor`、`pinned`、`range_empty`、`pinned_loss` 及可服务统计；不保存配置 |

下限属于价格字段，修改仍受[价格权限](admin-permissions.md#加减余额)约束；固定开关使用分组管理权限。

保存确认使用本次提交的参数。预览按已保存的分组倍率计算；预览拿不到（失败、超时或取消），或同一次编辑里倍率也改了时，改为通用确认，确认后才保存。新建分组不做“无号可用”的确认。

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

OpenAI 分组和组合分组可以分别设置 Fast（出站 `priority`）和 Ultrafast。组合分组按最终账号、映射模型和入口决定是否应用策略。它覆盖 OpenAI GPT 文本请求，包括 GPT 的视觉输入和工具调用。独立图片、视频、音频及其他平台继续按各自规则处理。

| 模式 | 行为 |
|---|---|
| 跟随（`inherit`） | 先查用户专属 Fast/Flex 规则，再查全局规则，各取第一条匹配。未命中时保留客户档位。 |
| 强制打开（`force_on`） | 对适用请求写固定档位，即使客户省略档位或请求另一档。Fast 与 Ultrafast 只能强开一个。 |
| 强制关闭（`force_off`） | 将客户明确点选的禁用档改写为 `default`。最终规则结果仍为禁用档，或为缺省、`null`、`auto`、`scale` 时，也明确发送 `default`。允许另一档及规则放行的 Flex。 |

用户专属和全局规则在**系统设置 → 网关服务 → OpenAI Fast/Flex 策略**配置，仍支持 `pass`、`filter`、`force_priority` 和 `block`。强制关闭只针对加速档。另一个档位的阻止规则仍可返回 403。

配置在每个 HTTP 请求和 Responses WebSocket 的每条 `response.create` 上判定。组策略的修改经缓存失效后应用到下一轮，正在执行的轮次沿用派发时的配置。在客户端重试或 fallback 组选了新账号时，系统按实际账号与模型核对档位支持；第三方账号需要近期同步的档位元数据。没有合格账号时，请求可能以 503 返回。

### 旧套餐与管理接口

升级后的已有组为策略版本 1。旧字段 `force_openai_fast=true` 显示为强制打开 Fast，原来的 priority 出站与结算语义继续适用。旧布尔值为 `false` 的组显示为跟随。组名等普通修改会保留版本 1。

在管理端选择升级到新策略后保存，或先使用 `GET /api/v1/admin/groups/{id}` 读取最新 `revision`，再用 `PUT /api/v1/admin/groups/{id}` 提交。以下示例在一次写入中禁用 Fast、强制 Ultrafast、取消免费 Fast 并开启免费 Ultrafast。

```json
{
  "revision": "2026-11-20T18:30:00.123456Z",
  "openai_tier_policy_version": 2,
  "openai_fast_mode": "force_off",
  "openai_ultrafast_mode": "force_on",
  "free_openai_fast": false,
  "free_openai_ultrafast": true
}
```

- `revision` 使用 GET 返回的原字符串。过期保存返回 409，重新读取后再编辑。两档同时强开返回 400 `OPENAI_TIER_FORCE_CONFLICT`，整次修改不写入。
- 更新时字段省略即保留，明确的 `false` 关闭对应赠免。已有组必须明确发送 `openai_tier_policy_version: 2` 才能切换到新版契约。需要回到旧契约时，使用当前 revision 同时提交版本 1、可由旧客户端表示的 Fast 模式、跟随 Ultra 和关闭免费 Ultra。
- 旧端可重复保存同一 `force_openai_fast` 值。对版本 2 的组，旧布尔值的实质改档返回 409，使用新模式字段并带新 revision。请求同时提交新旧 Fast 字段且含义冲突时返回 400。
- 切到不支持的分组平台时，档位模式恢复为跟随，赠免清除。复制分组会复制策略版本、档位模式、赠免和价卡。

### 价卡、赠免与账单

OpenAI Token 渠道价卡和分组模型价卡都支持 `ultrafast_multiplier`。OpenAI Token 卡的 Fast 与 Ultra 显式倍率为最多六位小数的有限正数，最大 `999999.999999`。例如 4.5 表示标准档 Token 价的 4.5 倍，1 同价，0.5 打折；`null` 恢复模型默认倍率。Fast 与 Ultrafast 可用不同倍率。分组价卡命中时，如果未配置 Ultra 倍率，则按该模型默认值。

更新 `PUT /api/v1/admin/channels/{id}` 或分组的 `model_pricing` 时，先读取完整列表与 `revision`。提交整张列表，保留原条目 ID（渠道返回时）和精确的平台、模型及计费模式。旧客户端未提交 Ultra 倍率时，服务按可唯一匹配的价卡保留它。价卡改名、合并或移除而旧客户端无法匹配时，保存返回 409。显式清除某条倍率时在该条目提交 `"ultrafast_multiplier": null`；有当前 revision 的整表保存可删除对应条目。

开启免费 Fast 或免费 Ultrafast 后，实扣最多是同一请求按 Standard 复算的金额；如果档位本来更便宜，使用较低金额。其他共同计价参数继续生效，包括用户专属倍率、分时价和长上下文。以同一请求标准价 1 USD、Ultra 倍率 4.5、组倍率 0.2 为例，Ultra 原额为 4.5 USD，正常实扣 0.90 USD，开启赠免时实扣 0.20 USD。

新版会记录客户输入、实际出站、可信上游声明和结算档位。扣费以已冻结的出站报价为上限；如有更便宜的可信结果，按结算规则下调。核对记录中的 `service_tier`、`total_cost`、`actual_cost`、计价来源和赠免信息。账号侧成本单独计算。[用量指标与导出](usage.md#openai-服务档位)

## 推理档位策略

API Key 账号的 HTTP 请求在最终账号和模型确定之后执行一次分组的推理档位规则，依次为：档位映射、上限与禁用、按模型就近映射。

- 规则中的模型条件匹配最终模型，不是请求里写的模型。
- 就近映射只处理模型明确不支持的档位，等距时取较低档；模型支持的 `none` 原样保留，目录里缺少某一档本身不算不支持。模型没有明确的档位限制时，Responses 请求的 `minimal` 照旧改为 `none` 发送。
- 同一目标的修复重放或协议回落沿用已经映射的结果；换到另一个账号或模型时，从原始档位重新计算。
- 请求没有写档位时，转换时补上的默认档位不当作调用方的输入，不参与规则。模型名后缀不单独触发分组映射；仅当最终模型明确不支持后缀表达的档位时才调整正文。
- 发往 Anthropic 上游时：Sonnet 5.5 的 `none` 为 `between_tools` + `low`，`low` 为 adaptive + `low`；Haiku 5.5 的 `none` 为 `disabled` + `low`。Claude 的 `minimal` 保留既有 adaptive 转换。原生 Messages 显式写的 thinking 模式在普通档位映射时保留；第三方模型没有显式 effort 时不因 thinking 模式而补写 effort。
- OAuth Chat 与 Anthropic 分组混合调度的 Antigravity 账号继续执行一次既有分组规则；Messages 省略 `output_config.effort` 时不对转换默认值封顶、映射或拒绝。
- 请求档位与实际发送的档位分别记录，计费按实际档位，见[用量指标与导出](usage.md#请求与实际推理档位)。
