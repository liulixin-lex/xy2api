# 其他平台：国产供应商与多模型聚合平台

本文介绍 Kimi、智谱 GLM、DeepSeek、MiniMax 四家国产供应商，以及 OpenCode、Command Code、Cline 三个多模型聚合平台的接入方法：账号类型、默认上游地址、对外支持的协议与端点、管理后台的添加方式和已知限制。需要接入这些上游的管理员阅读。组合分组（把多个平台按模型路由到一个分组）见 [分组](../guides/groups.md)。

## 共同点

这七个平台都是“多协议 API Key 供应商”，共享以下行为：

- **账号类型**：只有 API Key（账号 `type` 为 `apikey`），在「账号管理 → 添加账号」中选择对应平台后填写 API Key。
- **上游协议**：一个账号可以使用供应商的多个原生端点（Chat Completions、Anthropic Messages、Responses），在表单的「API 协议」中选择：
  - **自适应**（`adaptive`，新建时默认）：按下文各平台的分流方式选择上游端点，没有对应端点时才做协议转换。表单会显示「协议端点」，按协议分别预填官方默认地址，可逐个修改。
  - **Chat Completions / Anthropic / Responses**：固定使用该协议端点，只显示一个 Base URL。只有提供该原生端点的平台才会出现对应选项。
- **两种分流方式**：
  - **按入站协议**（Kimi、智谱、DeepSeek、MiniMax、Cline）：自适应账号按客户端请求的协议选择同协议的原生端点；供应商不提供该协议时回落到 Chat Completions。
  - **按模型**（OpenCode、Command Code）：每个模型只在某个协议端点提供，自适应账号按「模型协议分流」规则用模型名选择协议，自上而下第一条命中生效，支持精确 ID 或末尾 `*` 通配。入站协议恰好也是该模型支持的协议时同协议直通。规则可在表单中增删，或「恢复默认」。
- **凭据字段**：接入模式存于 `credentials.account_mode`，协议存于 `credentials.api_protocol`，自适应时各协议地址存于 `credentials.api_base_urls`（`base_url` 仍作为 Chat Completions 地址），按模型分流的规则存于 `credentials.protocol_rules`。没有「显示上游地址」权限的管理员不能修改地址，新账号使用平台默认地址，见 [管理员与权限](../guides/admin-permissions.md)。
- **对外端点**：这些平台的分组经 OpenAI 网关转发，支持以下端点，鉴权与其他平台相同（`Authorization: Bearer` 或 `x-api-key`）：

| 端点 | 说明 |
|---|---|
| `POST /v1/messages` | Anthropic Messages 格式，适配 Claude Code；这些平台不需要开启分组的「允许 /v1/messages 调度」 |
| `POST /v1/messages/count_tokens` | Token 计数 |
| `POST /v1/chat/completions`（别名 `/chat/completions`） | Chat Completions |
| `POST /v1/responses`（别名 `/responses`） | Responses，适配 Codex |
| `GET /v1/models` | 模型列表 |

Embeddings、图片、视频、语音等端点对这些平台返回 `404 not_found_error`。

- **客户端配置**：「使用密钥」弹窗提供 Claude Code、Codex CLI 与 OpenCode 三个选项卡。Claude Code 的写法与 [Anthropic](anthropic.md#claude-code) 相同（`ANTHROPIC_BASE_URL` 填根地址，不带 `/v1`）；Codex CLI 使用 `env_key = "XY2API_API_KEY"`、`wire_api = "responses"`、`supports_websockets = false`，并可加载当前分组的远程模型目录。

## Kimi（`kimi`）

| 接入模式 | 说明 | Chat Completions / Responses | Anthropic |
|---|---|---|---|
| 按量付费（`payg`，默认） | 消耗账户余额，按 Token 计费；余额不足自动冷却，充值后恢复 | `https://api.moonshot.cn/v1` | `https://api.moonshot.cn/anthropic` |
| Coding Plan（`coding`） | 订阅制编程套餐，按 5 小时 / 每周滚动用量窗口限流 | `https://api.kimi.com/coding/v1` | `https://api.kimi.com/coding` |

- 提供 Chat Completions、Responses（`/v1/responses`）、Anthropic 三种原生端点，按入站协议分流。
- 按量付费账号可查询余额（固定使用 `https://api.moonshot.cn/v1/users/me/balance`）；Coding Plan 账号可查询 5 小时 / 每周窗口用量。

## 智谱 GLM（`zhipu`）

| 接入模式 | Chat Completions | Anthropic |
|---|---|---|
| 按量付费（`payg`，默认） | `https://open.bigmodel.cn/api/paas/v4` | `https://open.bigmodel.cn/api/anthropic` |
| Coding Plan（`coding`） | `https://open.bigmodel.cn/api/coding/paas/v4` | `https://open.bigmodel.cn/api/anthropic` |

- 只提供 Chat Completions 与 Anthropic 两种原生端点。**没有原生 Responses 端点**，Responses 请求会转换为 Chat Completions，协议选项中也没有 Responses。
- Coding Plan 账号可查询用量窗口。团队版 GLM Coding Plan 需要在表单中填写「组织 ID」与「项目 ID」（`credentials.zhipu_organization`、`credentials.zhipu_project`），用量查询改走团队版端点；个人版留空。表单的问号提示中有获取这两个 ID 的步骤。
- 没有公开的余额查询接口，按量付费账号只能根据上游返回的 429 / 402 被动冷却。

## DeepSeek（`deepseek`）

| 接入模式 | Chat Completions / Responses | Anthropic |
|---|---|---|
| 按量付费（`payg`，唯一模式） | `https://api.deepseek.com` | `https://api.deepseek.com/anthropic` |

- 提供 Chat Completions、Responses、Anthropic 三种原生端点，按入站协议分流。官方 Responses 路径为 `/responses`（不带 `/v1`），自定义 Base URL 的账号同样按此路径拼接。
- **没有 Coding Plan**，表单中只有按量付费。
- 按量付费账号可查询余额。

## MiniMax（`minimax`）

| 接入模式 | Chat Completions / Responses | Anthropic |
|---|---|---|
| 按量付费（`payg`，默认） | `https://api.minimaxi.com/v1` | `https://api.minimaxi.com/anthropic` |
| Coding Plan（`coding`） | `https://api.minimaxi.com/v1` | `https://api.minimaxi.com/anthropic` |

- 提供 Chat Completions、Responses（`/v1/responses`）、Anthropic 三种原生端点，按入站协议分流。
- 按量付费与 Coding / Token Plan 共用同一个推理域名，由 API Key 区分套餐，所以两种模式的默认地址相同。地址预设中还有国际站 `https://api.minimax.io/v1` 与 `https://api.minimax.io/anthropic`。
- Coding Plan 账号可查询用量窗口（按 Base URL 自动选择国内站或国际站的查询地址）。没有余额查询接口。

## OpenCode（`opencode_go`）

平台界面名称为 OpenCode，标识保持 `opencode_go`。新建账号时在「账号类型」中选择 Zen 或 GO：

| 接入模式 | 说明 | Chat Completions / Responses | Anthropic |
|---|---|---|---|
| Zen（`zen`，新建时默认） | 按量付费网关，消耗账户余额，按 Token 计费 | `https://opencode.ai/zen/v1` | `https://opencode.ai/zen` |
| GO（`go`） | 订阅制网关，按 5 小时 / 周 / 月滚动用量窗口限流 | `https://opencode.ai/zen/go/v1` | `https://opencode.ai/zen/go` |

**按模型分流**，内置的默认规则（未命中任何规则的模型走 Chat Completions）：

| 模式 | Responses | Anthropic | Chat Completions |
|---|---|---|---|
| GO | `grok-*`、`gpt-*`、`muse-spark-*` | `minimax-*`、`qwen*` | 其余模型 |
| Zen | `grok-*`、`gpt-*`、`muse-spark-*` | `claude-*`、`qwen*`（`qwen3.8-max` 除外） | `qwen3.8-max` 及其余模型 |

- `/v1/models` 在尚未同步上游列表时回退到内置的模型目录。
- **用量**：GO 账号可以查询 5 小时 / 周 / 月用量窗口；Zen 账号没有订阅用量窗口。定时刷新需要同时打开「设置 → 网关服务 → OpenCode Go 用量刷新」中的「启用全局自动刷新」（默认关闭）与账号上的「自动刷新用量」。全局设置中「请求持续时的最长等待」为 5–1440 分钟，「请求安静等待」为 1–60 分钟且必须小于刷新间隔。手动刷新过于频繁时返回 `OPENCODE_GO_USAGE_REFRESH_RATE_LIMITED`。上游返回 403 可能是订阅缺失或失效，也可能是 WAF 或访问策略拦截。

## Command Code（`command_code`）

| 接入模式 | Chat Completions / Responses | Anthropic |
|---|---|---|
| 按量付费（`payg`，唯一模式） | `https://api.commandcode.ai/provider/v1` | `https://api.commandcode.ai/provider` |

- 订阅套餐与按量计费共用同一个 API Key 和端点，所以只有一个接入模式。
- **按模型分流**：`claude-*` 只走 Anthropic Messages（发往其他端点会被上游拒绝）；`gpt-*` 同时支持 Responses 与 Chat Completions（入站为其中之一时同协议直通，其余入站首选 Responses）；其余模型只保证 Chat Completions。未命中规则的模型按上游 `/models` 返回的 `supported_endpoints` 选择协议，模型目录不可用时走 Chat Completions。
- 模型名可以带厂商前缀（例如 `deepseek/deepseek-v4-flash`），原样发往上游。连接测试未指定模型时使用 `deepseek/deepseek-v4-flash`。
- **用量**：只有 Base URL 指向官方主机的账号能查询积分与用量窗口（积分单位为美元）。订阅套餐有 5 小时 / 每周滚动窗口，只限制套餐内积分；账户有充值积分时，窗口不参与停调与 429 冷却判断。

## Cline（`cline`）

| 接入模式 | Chat Completions |
|---|---|
| 按量付费（`payg`，唯一模式） | `https://api.cline.bot/api/v1` |

- **只提供 Chat Completions**。Responses 与 Anthropic 格式的入站请求由网关转换为 Chat Completions 后转发。
- 模型名带厂商前缀，例如 `anthropic/claude-sonnet-4-6`。
- **额度来源由模型名决定**：
  - `cline-pass/*`：ClinePass 订阅，受 5 小时 / 7 天 / 30 天三档滚动用量上限限制；
  - `cline-free/*`：免费模型，不扣积分也不占订阅额度，只有逐模型的每日额度；
  - 其余模型：按量扣账户积分。
- 积分或订阅一边用完时，只冷却该边的模型（模型级冷却键 `cline:credits`、`cline:pass`），另一边照常调度，不停用整个账号。
- 连接测试未指定模型时使用 `deepseek/deepseek-v4-flash`；用量探测显示账号订阅了 ClinePass 且订阅额度未在冷却时，改用 `cline-pass/glm-5.3-flash`，不消耗积分。
- 只有 Base URL 指向官方 `api.cline.bot` 的账号能查询积分与用量。

## 额度与余额查询

| 平台 | 余额查询 | 用量窗口查询 |
|---|---|---|
| Kimi | 按量付费 | Coding Plan |
| 智谱 GLM | 无 | Coding Plan（含团队版） |
| DeepSeek | 按量付费 | 无（没有 Coding Plan） |
| MiniMax | 无 | Coding Plan |
| OpenCode | — | GO 模式 |
| Command Code | — | 官方主机的账号 |
| Cline | — | 官方主机的账号 |

账号列表中的余额 / 用量单元格可以手动查询，对应管理接口 `GET /api/v1/admin/cn-providers/accounts/:id/balance` 与 `GET /api/v1/admin/cn-providers/accounts/:id/quota`。账号不适用时返回 `CN_QUOTA_NOT_CODING_PLAN`、`CN_QUOTA_INVALID_PLATFORM` 等错误。OpenCode 另有专用的用量接口 `GET /api/v1/admin/accounts/:id/opencode-go-usage` 与 `POST /api/v1/admin/accounts/:id/opencode-go-usage/refresh`。

## 限制与注意事项

- [渠道监控](../guides/channel-monitor.md)支持 Kimi、智谱、DeepSeek、MiniMax 作为供应商（走 OpenAI 兼容 Chat 协议），不支持 OpenCode、Command Code、Cline。
- 这些平台的模型是否支持某个协议、某个参数，以上游实际行为为准；切换到非默认协议前，请在目标账号上先用连接测试验证。
