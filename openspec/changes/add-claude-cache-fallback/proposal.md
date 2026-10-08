# Claude 自动缓存：按分组启用

## 当前迭代（2026-10-08）

用户要求把 0.2.4 的精确规则简化为一个开关和分组下拉选择，选中分组覆盖全部账号、调用 Key 与模型；参考 LiteLLM 自动缓存及 Sub2API OAuth 断点逻辑。此次仅在 `feat/claude-cache-groups-20261008` 本地开发，不推送、合并、发版或部署。

## 变更

- 配置改为 `enabled + group_ids`，移除名称、账号、地址、Key 和模型配置；旧规则只迁移分组选择，默认关闭以避免静默扩大范围。
- 缺少客户端声明时补块级默认 5m 断点，优先会话尾部、system、tools 和较长会话的前一 user 轮次，保护已有断点及四点上限。
- 支持 Anthropic API Key、OAuth/SetupToken、透传、Vertex、Bedrock，以及 Chat/Responses 到 Anthropic 的转换。
- 保留实际 usage 结算，扩大冷写预留候选模型范围，使用现有调度快照。
- 页面保留开关、分组选择、简短费用说明与独立保存；删除旧规则编辑器。

## 边界

不改变调度、非 Anthropic 协议或 count_tokens；不新增缓存服务、数据库迁移、1h 自动补齐、能力探测、付费重放。是否真实命中取决于上游与请求前缀，本功能不替代历史根因调查。

当前行为详见 [design](design.md)、[spec](specs/claude-cache-fallback/spec.md) 与 [使用说明](../../../docs/CLAUDE_CACHE_FALLBACK.md)。0.2.4 的历史交付记录保留在 tasks；不将历史生产验收结论套用到新范围。
