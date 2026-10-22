# Grok 渠道监控的 Responses 模式

Grok 监控现在可以在保留 `provider=grok` 的同时选择 `api_mode=responses`，无需把供应商类型改成 OpenAI。已有监控和模板仍默认使用 Chat Completions；升级不会自动切换协议。

## 为什么增加这个选项

在 0.3.0 的只读审查中，Grok 渠道监控固定请求 `/v1/chat/completions`，而账号页的文本测试使用 `/v1/responses`。两条路径可能具有不同的上游模型路由和可用性，因此账号测试成功不能证明 Chat 监控一定成功。新增明确的协议选项，可以让监控覆盖实际使用的 Responses 路径。

本功能不改变业务请求路由、账号选择、错误换号或上游模型配置。它也不会消除真实的限流、余额不足和网络错误。

## 设置方法

编辑或创建渠道监控：

| 项目 | 设置 |
| --- | --- |
| 供应商 | Grok |
| 检测模式 | 探活，或已有配额配置的探活＋配额 |
| API 模式 | Responses API |
| 接口地址 | 目标基础地址，例如 `https://gateway.example` 或 `https://gateway.example/v1` |
| API Key | 该站点中绑定目标 Grok 分组的 Key；编辑时留空保留已保存的 Key |
| 主模型 | 目标分组和上游均支持的模型 |

程序追加 `/v1/responses`，并正确处理已有 `/v1` 的基础路径。仍按 Key 的站内权限、分组白名单和账号资格选择上游；监控供应商标签不会替代这些权限。

请求模板也支持 Grok Responses。模板列表按供应商与协议共同筛选；切换协议会清除表单中的旧模板和自定义请求快照，避免把 Chat 的 `messages` 请求体带入 Responses。

## 请求与判定

请求形状、判定口径、参数自适应与重试以 [`CHANNEL_MONITOR_V1.md`](CHANNEL_MONITOR_V1.md) 为准。与 Grok Responses 相关的要点：

- 默认请求为流式 Responses：`model`、`instructions`、一句话加法题 `input`、`max_output_tokens`、`stream=true`；Grok 4 及以上带 `reasoning_effort: "low"`，上游不认时同一轮自动去掉重发。
- 正文按 `response.output_text.delta` 拼接，reasoning 事件不参与答案校验；`response.failed` 等流内错误判红。上游直接返回 JSON 时按 `output_text` 与 `output` 中的 message 解析。
- 合并模式保护 `model`、`instructions`、`input`、`stream`，允许调整输出上限等非关键字段。
- 替换模式原样发送请求体，按其中的 `stream` 决定读法，要求非空 `instructions` 与 `input`，以“2xx 且正文非空”判定；自行填写的模型仍须有权限使用。
- 30 秒内拿到正确回答且响应时间不超过 10 秒为正常，超过 10 秒为降级，30 秒内拿不到正确回答为错误。上游非 2xx 照常作为错误展示，不通过改协议隐藏真实故障。

xAI 的 Responses API 支持流式和非流式请求，参见 [官方说明](https://docs.x.ai/developers/tools/streaming-and-sync)。真实上游支持情况仍应在启用后检查，合成测试不能证明外部供应商可用。

## 验证范围

后端回归覆盖真实本地 HTTP 请求的路径、Bearer 鉴权、Grok 模型、Responses 请求字段、reasoning 前置响应解析、合并字段保护、替换模式校验、HTTP错误保留、部分更新保留协议，以及监控/模板创建和更新规则。既有 Grok Chat 和 OpenAI Responses 测试继续运行。

前端回归覆盖 Grok Responses 创建、编辑回读、保留旧 Key、切回 Chat、模板筛选和快照清除、模板创建/更新、协议不支持的供应商切换，以及高级请求示例。新增测试纳入 `make test-frontend-critical`。

无需数据库迁移、Ent 生成或依赖升级。本功能未部署到生产，也未修改生产监控配置。
