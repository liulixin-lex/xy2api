# Anthropic 缓存 TTL

现有全局布尔仅覆盖 OAuth，API Key 主体流量无法使用统一默认；转换可能丢掉 TTL 桶，部分明细和 1h 预留也有财务接缝。本变更一次贯通管理员 5m/1h 默认、Key 四态、最终实际 Claude 出站、用量/计费/预留，以及前端与升级说明。

范围按已有入口/供应商构造器组合限定：转换仅覆盖现有 Anthropic 与 Vertex 路径；Bedrock 仅覆盖原生 Messages（Bearer/SigV4）。Chat/Responses → Bedrock 缺少转换构造器和 eventstream 解码器，不在本变更范围；现有请求可能表现为上游认证或通用转发错误，并非显式 unsupported。新增该转换留待后续。

存量 Key 迁移 off、新建 inherit，避免默认降掉客户端合法 1h。TTL、分组补断点和账号计费分类覆盖职责独立；正式退役消息断点清理重建与旧布尔，不增加后台服务、影子模式、自动探测、调度策略或生产操作。

现行契约见 [Anthropic 缓存 TTL](../../../docs/ANTHROPIC_CACHE_TTL.md)。
