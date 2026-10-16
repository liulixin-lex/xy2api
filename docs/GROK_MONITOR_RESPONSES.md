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

本次复用现有非流式 Responses JSON 探测：默认请求包含 `model`、`instructions`、算术挑战 `input`、`max_output_tokens=50` 和 `stream=false`。响应解析支持顶层 `output_text`，以及 `output` 中位于 reasoning 条目之后的 message/output_text。

- 默认和合并模式仍验证挑战答案。
- 合并模式保护 `model`、`instructions`、`input`、`stream`，允许调整输出上限等非关键字段。
- 替换模式要求非空 `instructions` 与 `input`，并沿用“2xx且文本非空”的判定；自行填写的模型仍须有权限使用。
- 上游非2xx响应继续作为错误展示；不通过改协议隐藏真实故障。
- 保留完整响应达到6秒标记慢、响应头等待30秒、请求总时限45秒的既有规则。

这是 Responses **JSON** 探测，不等同于账号页的流式 Responses 测试。当前监控不解析 SSE；不要用替换请求体强行设置 `stream=true`。只提供流式输出的上游需要另行实现流事件结束及错误判定。

xAI 的 Responses API 支持流式和非流式请求，参见 [官方说明](https://docs.x.ai/developers/tools/streaming-and-sync)。真实上游支持情况仍应在启用后检查，合成测试不能证明外部供应商可用。

## 验证范围

后端回归覆盖真实本地 HTTP 请求的路径、Bearer 鉴权、Grok 模型、Responses 请求字段、reasoning 前置响应解析、合并字段保护、替换模式校验、HTTP错误保留、部分更新保留协议，以及监控/模板创建和更新规则。既有 Grok Chat 和 OpenAI Responses 测试继续运行。

前端回归覆盖 Grok Responses 创建、编辑回读、保留旧 Key、切回 Chat、模板筛选和快照清除、模板创建/更新、协议不支持的供应商切换，以及高级请求示例。新增测试纳入 `make test-frontend-critical`。

无需数据库迁移、Ent 生成或依赖升级。本功能未部署到生产，也未修改生产监控配置。
