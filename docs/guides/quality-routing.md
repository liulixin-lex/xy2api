# OpenAI 模型降档识别

XY2API 用一组“降档对”识别上游偷换模型：请求发往 GPT-5.6、GPT-6 及更新的模型，上游却在响应里声明了配置为降档的较低模型。本文说明降档对与模型名规范化规则、相关配置项，以及它们目前影响哪些功能。面向配置 OpenAI API Key 上游的管理员。

降档对（内置的与 `rules` 中追加的）目前用于：

- [OpenAI 智商检测](iq-check.md)：检测题的响应声明了配置的降档模型时，该题按答错计。
- 使用记录中的上游模型审计 `upstream_model_mismatch`：OpenAI 模型名按下文规则规范化后比较（只用规范化规则，不用降档对）。

配置段名为 `gateway.openai_quality_routing`，沿用自早期的“会话质量路由”。按会话回避降档上游的选号逻辑在当前版本中没有启用：`mode` 与 `avoid_seconds` 会被加载和校验，但不改变选号、暂避或会话标识。

## 配置

在配置文件中设置（不在管理后台）：

```yaml
gateway:
  openai_quality_routing:
    mode: enforce        # off | observe | enforce，默认 enforce
    avoid_seconds: 300   # 0–86400，0 按 300 处理
    rules:               # 追加的降级对，默认 []
      - from: gpt-6-astra
        to: gpt-5.6-terra
```

| 键 | 默认 | 说明 |
|---|---|---|
| `mode` | `enforce` | 取值 `off`、`observe`、`enforce`；可用环境变量 `GATEWAY_OPENAI_QUALITY_ROUTING_MODE` 覆盖 |
| `avoid_seconds` | 300 | 可用环境变量 `GATEWAY_OPENAI_QUALITY_ROUTING_AVOID_SECONDS` 覆盖 |
| `rules` | `[]` | 每项 `{from, to}`，表示“请求 `from`、上游声明 `to`”即为降级。只能在配置文件中设置 |

`rules` 中的 `from` 与 `to` 都必须能被下文的规则识别，且基础模型不同，否则启动时配置校验失败。示例见 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)。

## 模型名规范化

比较之前，网关把模型名规范化为“基础模型 + 可选的日期快照 + 可选的推理强度”：

- 忽略大小写；`_` 与空白改为 `-`；只去掉 `openai/` 前缀，带其他 `/` 路径的名字（其他供应商、部署名）视为无法识别；超过 200 个字符的名字视为无法识别。
- 日期快照写作 `-YYYY-MM-DD` 或 `-YYYYMMDD`，必须是真实日期；推理强度后缀至多一个（`none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`）。
- 基础模型必须是 `gpt-<版本>-(astra|sol|terra|luna)`，版本不低于 5.6。裸写的 `gpt-6` 等同 `gpt-6-astra`，`gpt-5.6` 等同 `gpt-5.6-sol`；其他裸版本（如 `gpt-6.1`）无法识别。

上游声明与出站模型比较后分为相同、等价（只差大小写、`openai/` 前缀、日期或推理强度后缀）、快照漂移、版本更新或更旧、不同、无法识别、未声明，以及降级。只有命中配置的“出站基础模型 → 声明基础模型”对才算降级；版本或变体不同本身从不算降级。

## 内置降级对

以下出站模型被上游声明为 `gpt-5.6-luna` 时视为降级（两侧的日期与推理强度变体同样适用）：

- `gpt-6-astra`
- `gpt-6.1-sol`
- `gpt-6-sol`
- `gpt-5.6-sol`
- `gpt-5.6-terra`

新的替换先在使用记录中表现为“不同”，只有加入 `rules` 后才会被当作降级。

## 范围与限制

- 按会话回避降档上游的选号逻辑当前未启用，见文首说明。

- 只针对 OpenAI 平台的 API Key 账号；OAuth 账号不受影响，图片生成与 Grok 请求不参与。
- 判断依据是上游自报的模型名，上游可以虚报，也无法据此识别第三方隐藏的号池账号。
- 智商检测与模型审计读取同一组降档对；修改 `rules` 需要重启进程。
