# OpenAI 缓存可靠性

本次改动覆盖 Responses、Chat Completions 转换、HTTP SSE 和原生 WebSocket。缓存读取和写入统一从真实上游 usage 解析；不根据请求大小、缓存选项或历史命中推算计费用量，不自动发送额外模型请求。

## 协议与计量

- 读取优先级：`input_tokens_details.cached_tokens`、`prompt_tokens_details.cached_tokens`、`cache_read_input_tokens`、`cache_read_tokens`、`cached_tokens`。显式零优先于后续别名；负数归零；缺失、null 和非法别名不作为命中证据。
- 写入优先级：Responses / Chat 明细中的 `cache_write_tokens`、明细 `cache_creation_tokens`，随后顶层 `cache_write_tokens`、`cache_creation_input_tokens`、`cache_write_input_tokens`、`cache_creation_tokens`。保留来源用于核对统计与结算。既有严格协议验证仍会拒绝结构错误的终态 usage。
- 转换后保留明确的 `cached_tokens: 0`，统一 canonical 写入值，避免客户端看到互相矛盾的写入别名。终态缓存诊断可随转换响应或 SSE finish chunk 返回，与是否请求 usage chunk 独立。
- API Key 上游保留客户端明确指定的 `prompt_cache_key`、`prompt_cache_retention`、`prompt_cache_options` 和 `safety_identifier`，不再根据 User-Agent 删除。旧 Codex OAuth 协议继续过滤不支持的后三项，Debug 记录字段名及协议原因。网关不自动添加 24h retention，也不替用户选择缓存模式。

不同供应商的参数能力仍须以其实际端点验收；API Key 是透明转发边界，保留参数不等于证明代理背后的所有模型都支持该参数。OpenAI 规则参见 [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching) 与 [Diagnostics](https://developers.openai.com/api/docs/guides/prompt-caching/diagnostics)。

## 会话与并发

认证 GPT 会话的本地粘连身份按 API Key ID、分组、客户端请求模型和稳定会话信号隔离，使用持久 JWT secret 的 HMAC 后再生成版本化 `v2:` 键。旧的未隔离绑定不复用；上线后的第一次请求会重新建绑定。客户端显式缓存键和会话头的上游兼容行为保持原有规则。

无显式身份时，内容回退只使用开头连续的 system/developer 指令、工具定义和首条 user；后续动态 system/developer 消息不会改变已有回退身份。网关不改写用户实际提示词或工具顺序，前缀复用仍依赖客户端保持相同内容。

Redis Lua 实现首次写入者持有绑定、只有当前所有者能续期和删除。常规无利润门的并发冷请求会读取首次保留的所有者并重新选择，释放临时抢到的槽位；若所有者确实没有容量，单次溢出不改长期绑定。WaitPlan 后的准入也尊重溢出标记。准入前保留最多 30 秒，失败或取消后通过到期清理；已通过准入的同所有者请求延长正常会话 TTL。

利润门模式沿用准入成功后才绑定的约束，避免被否决请求留下长期绑定。两个同时通过准入的首请求仍可能分别使用不同账号，首次原子绑定决定后续所有者；不会为缓存命中绕过利润门。原有 previous_response_id、任务所有者、质量保护及权重调度仍优先。

## 诊断与灰度

默认配置：

```yaml
gateway:
  openai_cache:
    diagnostics_enabled: false
    aware_routing_enabled: false
    rollout_percent: 0
```

Compose / 环境变量对应 `GATEWAY_OPENAI_CACHE_DIAGNOSTICS_ENABLED`、`GATEWAY_OPENAI_CACHE_AWARE_ROUTING_ENABLED` 和 `GATEWAY_OPENAI_CACHE_ROLLOUT_PERCENT`。比例范围 0..100，按稳定的认证会话选择灰度样本。配置变更依现有部署流程生效，本次发布不会改变线上配置。

启用诊断后，日志区分 cold、超过 30 分钟、前缀变化、账号变化和可比续请求，记录原始 usage 与实际结算后的 token 桶/成本。会话、前缀和请求 ID 仅记录 HMAC；不记录提示词、客户端缓存键、会话头或比较响应 ID。关闭两个功能时不额外解析大请求前缀。

诊断前缀指纹只概括稳定的 instructions、tools、格式、reasoning 和开头连续的 system/developer 内容；不包含完整首条 user，也不做供应商 tokenizer 的逐 token 前缀比较。长度桶按请求体字节数划分。因此“可比”是观测和排序的近似条件，不能单独证明两个请求具有相同的上游缓存键或相同的前 1024 token；实际命中以返回的缓存 usage 为准。

缓存感知选路仅用于没有有效已有绑定的新选择，并只在既有优先级和负载等排序条件相同的候选中打破平局。每个候选都必须有至少 20 个最近 30 分钟内的可比续请求样本，否则保持原顺序。仅训练输入 >=1024、明确缓存 usage 且桶总量合理的请求；冷请求、换账号、前缀变化和超时样本不训练。advanced 模式额外保持队列、健康评分及 compact 能力相等；权重和利润优先策略不被该偏好取代。

学习数据按进程保存，最多 2048 会话和 4096 候选统计，重启后重新积累。评分使用缓存读/写的相对输入成本及有界首字延迟，仅作启发式排序，不能替代自定义价格、供应商真实账单或网关利润计算。多实例结果可能不同，稳定灰度归属不依赖进程随机数。

建议先仅启用诊断，按模型、账号、缓存模式、格式和长度桶比较相同前缀的续请求；再从 1% 启用偏好，观察缓存 token 比例、首字延迟、错误/切换率、结算与上游账单。用量别名修复可能使报告命中率变化，应分别比较真实缓存收益与统计修正。回退偏好只需关闭 `aware_routing_enabled` 并将比例归零，基础计量和租户隔离修复仍保留。

本地和 CI 使用合成上游、并发测试及独立 Redis 验证，不证明生产命中率已经提升。短提示、不稳定的前置内容、请求间隔、错误切换和代理内部凭据池仍可能导致低命中；发布后需结合诊断确认，不能一律归因于网关调度。
