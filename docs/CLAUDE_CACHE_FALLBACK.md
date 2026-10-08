# Claude 自动缓存

在管理后台「设置 → 网关」打开「Claude 自动缓存」，从下拉框选择适用分组并保存即可。支持选择多个分组；选中分组下所有账号、调用 Key 和模型均纳入，无需填写规则名称、上游地址或模型名单。

默认关闭。旧版精确规则升级后仅保留其分组选择，开关自动视为关闭；需要管理员重新开启并保存，才采用新的分组范围。保存配置不调用上游，也不预热缓存。

## 补齐行为

仅作用于实际转发为 Anthropic Messages 协议的请求，包括 API Key、OAuth/SetupToken、透传、Vertex 和 Bedrock。Chat Completions / Responses 请求转换为 Anthropic Messages 后也适用。分组中的 OpenAI/Gemini 等其他协议、`count_tokens` 保持原行为；这属于协议边界，不是模型或账号白名单。

客户端原始请求在协议位置已经带 `cache_control` 时，本功能不再添加断点，包括 null 或非法声明。现有 OAuth 重写、协议转换及非法字段净化仍按原配置执行。本功能不自动开启旧的 OAuth 消息断点重写。

原始请求没有声明时，在完成现有处理及分组 system 注入后，按剩余断点额度依次尝试：

1. 最后一条消息中最后一个可缓存块。
2. system 的最后一个可缓存文本块。
3. tools 中最后一个非延迟加载工具。
4. 至少四条消息时，倒数第二条 user 消息的最后一个可缓存块。

已有网关/OAuth 断点保留并计入最多四个断点的额度。空文本、thinking、redacted_thinking 和未知块类型不会被标记。字符串内容转换为等价的 text 块，保持内容及顺序；变更只属于本次发送，不污染重试或换号使用的共享正文。

新断点使用 `{"type":"ephemeral"}`，即默认 5 分钟；不添加顶层自动缓存字段、不自动延长到 1 小时。已有 1 小时断点时，跳过会违反「长 TTL 在短 TTL 前」顺序的补齐位置。

## 范围与费用

请求进入时冻结选中分组范围，发送前再检查当前开关和分组。中途开启不能扩大已预留请求的范围；关闭或移除分组会阻止后续补齐。账号须属于该有效分组；分组 fallback 不继承原分组的授权。

首次缓存写入可能比普通输入更贵。余额预留取普通输入与配置的 5 分钟写入单价较高者，并考虑调度快照中的账号映射模型。预留沿用现有 token 估算、输出上限及倍率，可能暂时多占用余额，**不作为实际扣款**。

实际结算继续使用上游返回的普通输入、缓存写入/读取、5m/1h 明细与输出用量，执行原有定价和耐久结算。本功能不会根据请求里有缓存字段就假定命中、伪造 usage 或额外扣费。ForceCacheBilling 与 TTL 计费覆写仍按原配置执行；它们不由本功能开启。

兼容网关接受声明不代表一定创建或命中缓存。最小缓存长度、前缀变化、间隔、账号路由及上游实现都会影响结果；需结合真实 `cache_creation_input_tokens` 和 `cache_read_input_tokens` 判断。不新增自动探测或遇到字段错误删字段重放。XY2API 的 usage 不能替代供应商或 NewAPI 的独立账单。

## 停用与配置

关闭并保存即可。本实例立即更新，其他实例最长约 60 秒刷新；过期配置读取失败时停止补齐。已发送请求正常结算。分组删除后仍可关闭保存，启用保存需校验所选分组存在。

配置仍存于 settings 表，无新数据库迁移：

```json
{"enabled": false, "group_ids": []}
```

`group_ids` 最多 1000 个、为互不重复的正整数；启用时至少选择一组。旧格式写入被拒绝，省略该设置的部分更新保留原值。回退前先停用；0.2.4 的旧格式解析器会将新格式视为关闭。

## 实现参考

- [LiteLLM prompt caching](https://docs.litellm.ai/docs/tutorials/prompt_caching)：已有客户端声明优先、system/会话尾部自动断点、四断点上限。[参考源码](https://github.com/BerriAI/litellm/blob/87e961fad0d603dda2169d5994c4ce77ecb37a33/litellm/integrations/anthropic_cache_control_hook.py)。本实现按用户要求去掉模型白名单，仅按出站协议限定。
- [Sub2API OAuth 消息缓存](https://github.com/Wei-Shaw/sub2api/blob/3f1a2ea0a760730e3bc528105c00b4ee4f23e469/backend/internal/service/gateway_messages_cache.go)：会话尾部与较长会话的倒数第二条 user 锚点。这里复用其位置思路，未采用其先移除已有消息断点的行为。
- [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)：块级声明、tools → system → messages 的前缀顺序、断点及混合 TTL 约束。

本轮只进行本地开发、模拟上游与离线回归；没有验证所有真实上游的缓存支持，也没有推送、发版或修改线上服务。
