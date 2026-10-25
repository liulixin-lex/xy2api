# 缓存观测的平台与字段扩展

状态：开发中。通过现有运行时策略选择 OpenAI、Anthropic、Gemini 和 Messages 桥的观测范围；默认不启用新平台，有审计，每请求固定 loaded revision，其他实例约 60 秒内刷新。

观测不得改变出站、计费、选号、回退或韧性。本变更不引入 prompt_cache_hit_tokens 的计费别名，该变化会影响仅有此别名时的计费输入，需单独裁定。

见[设计](design.md)、[任务](tasks.md)和[现行说明](../../../docs/openai-cache-reliability.md)。
