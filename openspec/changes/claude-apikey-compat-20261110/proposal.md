# Anthropic API Key 请求兼容

部分 Anthropic API Key 上游只接受具有 Claude Code 请求特征的请求。普通客户端虽然使用合法网关 Key，仍可能被上游拒绝。增加账号级、默认关闭的 `claude_code_v1` 出站适配，让选定账号服务普通 Messages、Responses HTTP 和 Chat Completions 客户端。

管理员在既有账号表单开启“Claude Code 请求兼容”。认证、地址、代理、模型映射、倍率和分组准入沿用现有配置；已有完整原生 CLI 请求继续现有路径。兼容层保留业务 system、工具与明确参数，并为新适配的签名历史提供来源和前缀保护。

本期包含账号配置、三个入口的流式与非流式、独立计数、身份隔离、签名来源登记、诊断、后台表单和文档。Chat 无签名字段的思考历史、供应商保存的 continuation、Message Threads、compact 和 WebSocket 不在首版能力内。拒绝不支持的能力，不删除历史制造成功。

没有新增数据库迁移、专用管理接口、重试框架、自动熔断或影子模式。实现与验证状态见 [tasks](tasks.md)；契约见 [design](design.md)。
