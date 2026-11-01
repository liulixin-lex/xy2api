# Claude 自动缓存

在管理后台「设置 → 网关 → Claude 缓存」卡片打开「自动补缓存」，从下拉框选择适用分组并保存即可。同一卡片的「缓存时长（跟随全局的密钥）」一行设置 [Anthropic 缓存 TTL](ANTHROPIC_CACHE_TTL.md) 的管理员默认值；保存只提交改动过的项，只改时长不会重新校验分组。支持选择多个分组；选中分组下所有账号、调用 Key 和模型均纳入，无需填写规则名称、上游地址或模型名单。

默认关闭。旧版精确规则升级后仅保留其分组选择，开关自动视为关闭；需要管理员重新开启并保存，才采用新的分组范围。保存配置不调用上游，也不预热缓存。

## 补齐行为

仅作用于当前已有构造器支持、且实际转发为 Anthropic Messages 协议的请求：Anthropic 原生 Messages（API Key、OAuth/SetupToken、透传）、现有 Vertex 原生/转换路径，以及 Bedrock 原生 Messages（Bearer/SigV4）。Chat Completions / Responses 仅在现有 Anthropic/Vertex 转换路径适用；Chat/Responses → Bedrock 缺少转换构造器及 eventstream 解码器，可能落入既有上游认证或通用转发错误，并非显式 unsupported，本轮不扩展该路径。分组中的 OpenAI/Gemini 等其他协议、`count_tokens` 保持原行为；这属于协议边界，不是模型或账号白名单。

客户端原始请求在协议位置已经带 `cache_control` 时，本功能不再添加断点，包括 null 或非法声明。协议转换及非法字段净化仍按既有规则执行。消息断点清理重建策略已退役，不会自动开启分组补断点。Responses `input` 中工具调用与结果项（`function_call`、`custom_tool_call`、`tool_search_call`、`tool_search_output`、`function_call_output`、`custom_tool_call_output`）的项级标记，以及 `function_call_output`、`custom_tool_call_output` 数组 `output` 分片上的标记也算声明，这些项转换为 `tool_use`/`tool_result` 块时标记随之保留；Chat 工具消息上的标记原本就算声明，现在也随 Anthropic 转换转发（`/v1/responses` 的旧版 Chat 形状请求除外，2026-10-31 起）。

原始请求没有声明时，在完成现有处理及分组 system 注入后，按剩余断点额度依次尝试：

1. 最后一条消息中最后一个可缓存块。
2. system 的最后一个可缓存文本块。
3. tools 中最后一个非延迟加载工具。
4. 至少四条消息时，倒数第二条 user 消息的最后一个可缓存块。

已有网关/OAuth 断点及顶层 automatic 声明保留并共同计入最多四个断点的额度。空文本、thinking、redacted_thinking 和未知块类型不会被标记。字符串内容转换为等价的 text 块，保持内容及顺序；变更只属于本次发送，不污染重试或换号使用的共享正文。

补断点生成 `{"type":"ephemeral"}`，即协议默认 5 分钟，不添加顶层自动缓存字段。随后由独立的 [Anthropic TTL 偏好](ANTHROPIC_CACHE_TTL.md) 统一已有断点；强制 1h 时包括本次补齐的断点。off 保留合法混合 TTL，继续遵守长 TTL 在短 TTL 之前的顺序和四槽约束。

## 范围与费用

请求进入时冻结选中分组范围，发送前再检查当前开关和分组。中途开启不能扩大已预留请求的范围；关闭或移除分组会阻止后续补齐。账号须属于该有效分组；分组 fallback 不继承原分组的授权。

首次缓存写入可能比普通输入更贵。余额预留结合冻结 TTL 策略、客户端声明及既有注入路径区分 5m/1h 冷写风险，取普通输入与本站对应 TTL 写入单价较高者，并考虑调度快照中的账号映射模型；冷写时不采用历史命中比例折扣。预留沿用现有 token 估算、输出上限及倍率，可能暂时多占用余额，**不作为实际扣款**。

实际结算继续使用上游返回的普通输入、缓存写入/读取、5m/1h 明细与输出用量，执行原有定价和耐久结算。本功能不会根据请求里有缓存字段就假定命中、伪造 usage 或额外扣费。ForceCacheBilling 与账号「缓存写入计费分类覆盖」仍独立配置，只作用于结算副本，不反写上游响应；它们不由本功能开启。部分 TTL 桶的未知差额按基础写价结算，详见 [TTL 计费说明](ANTHROPIC_CACHE_TTL.md#用量计费与预留)。

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
