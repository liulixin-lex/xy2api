# 设计：Claude 按分组自动缓存

本设计对应 2026-10-08 本地迭代，替代 0.2.4 的账号/Key/地址/模型精确规则方案。实现参考的固定源码链接见 [使用说明](../../../docs/CLAUDE_CACHE_FALLBACK.md)。

## 配置与迁移

既有 `claude_cache_fallback_policy` 使用强类型 `enabled:boolean, group_ids:int64[]`，不新增数据库迁移。写入要求字段齐全、无未知字段、JSON ≤1MiB、分组最多 1000 个正整数且不重复，启用至少一组。保存只校验组存在，不枚举其账号或 Key，不调用上游；关闭允许保留已经删除的组。

读取旧 rules 时提取去重的 group_id，但禁用开关。旧格式 HTTP 写入被拒绝；旧 UI 不能误把精确许可扩大为全组启用。其他设置的部分更新不改该字段。缓存用独立不可变快照；写入刷新本实例，其他实例最长 60 秒刷新，过期读取失败视为关闭。

## Admission 与发送

Messages、Chat Completions、Responses 的 Anthropic handler 在重写前记录原始协议位置的 cache_control 存在性和 admission 分组快照。null/非法值也表示客户端声明；工具 schema/input 中同名业务数据不算。转换协议额外识别 function 工具及 input 消息位置。

每次账号尝试建立独立状态。发送前检查 admission 与当前快照都覆盖有效分组，且账号确实属于此组。中途开启不改变已预留的请求；关闭或移除组可阻止补齐，fallback 分组不继承原授权。ID、地址、调用 Key 和模型不再作细分匹配。

API Key、OAuth/SetupToken、透传、Vertex 和 Bedrock 的 Anthropic builder 在最终分组 system 后调用 helper；Bedrock 在 SigV4 签名前处理。Chat/Responses 转换使用同一 helper。其他平台协议与 count_tokens 不接入。

## 断点算法

客户端原始声明存在时不补齐。原始没有声明但最终出现网关/OAuth 块级断点时保留并计数；最终顶层声明存在时直接跳过。

在剩余四断点预算内，依次尝试最后一条消息的最后可缓存块、system 最后文本块、最后非延迟 tools 定义、消息数 ≥4 时倒数第二条 user 消息的最后可缓存块。目标已有断点就不再向前添加重复点。允许 text/image/document/tool_use/tool_result；空文本、thinking、redacted_thinking、未知类型跳过。

使用局部 JSON 修改；字符串转等价 text 数组。新增字段只有 `cache_control:{type:ephemeral}`，不自动生成 1h。按 tools → system → messages 检查 TTL，跳过会让 5m 排在已有 1h 前的候选位置。不修改原断点 TTL，也不通过提高 TTL 来修复顺序。

记录本次修改前/后正文引用，在成功 wire 同步到 ParsedRequest 前恢复为修改前的精确正文（包括原字符串形态）。已有其他转换仍按原同步流程执行。并发尝试不共享可变补齐状态。

## 计费与兼容性

冷写预留对所有可能补齐的选中组请求使用普通输入与 5m 写入价较高者。模型候选包括渠道及账号映射，账号映射读取既有调度快照，不新增每模型缓存或直接数据库查询。预留不是扣款或精确 token 报价；最终未注入时按原生命周期释放。

真实结算、SSE usage 合并、强制缓存计费、TTL 覆写、耐久意图及幂等不变。诊断删除 rule_id，保留分组/账号/Key、决策原因及原始/结算数值，不记录正文或凭据。混合/缺失/矛盾 TTL 明细继续由原计费策略处理，不能从注入动作推断缓存命中或供应商收费。

不新增网络调用、能力探测、自动降级重放或版本变更。未知兼容网关可能拒绝或忽略声明，按原错误流程处理。测试使用本地合成正文和 mock 上游，不声称验证所有真实上游。

## 界面

沿用设置卡片与通用 Select/Toggle。选择分组后以可移除标签显示，已选项不重复出现；支持搜索。只展示范围一句话、费用一句话及保存状态。保留加载/目录失败重试、失败草稿、删除组停用、旧后端/忽略响应失败处理；目录重试不覆盖已编辑内容。中英文及移动端一致。
