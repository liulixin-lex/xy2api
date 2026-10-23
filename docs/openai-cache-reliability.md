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

## cache_trace v1（P0a）

buffered SSE 的观测复用既有计费遍历已解析的帧；关闭时直接调用原解析器。每个 buffered 请求只设置一次异常隔离：观测返回错误后停用该请求后续观测，panic 后丢弃局部 usage 并用原解析器重算；这一步不执行结算，原有结算仍只执行一次。观测失败的尝试不发布 trace，避免把缺失观测误报为上游缺少 usage；异常原文不写日志。

静态 `gateway.cache_trace.enabled` 默认 false（环境变量 `GATEWAY_CACHE_TRACE_ENABLED`），仅在 `gateway.openai_cache.diagnostics_enabled` 也为 true 且有持久 JWT secret 时采集。关闭时不进行额外 JSON 解析或请求体读取。观测不改变请求 bytes、选号、turn-state 或计费；事件输出由 T0a.3 在原有 usage 结算时接入；此前提交只准备快照。

- 入站 `pck_class_in` 只看原 body：none / empty / client_uuid / client_non_uuid；UUID 用固定 8-4-4-4-12 十六进制正则，不校验版本位。
- 出站 `pck_class_out` 按实际注入/改写点记录：none / passthrough / gw_compat_hex / gw_from_header / oauth_rewritten；gw_grok 仅预留来源标记（P0a 排除 Grok），gw_uuid 留给后续身份改动。`pck_out_is_uuid` 只表示形态，不用于推导来源。
- `sess_out_relation` 表示下划线 session_id 的来源：none / client_value / gw_isolated / uuid_of_pck（eq_pck 预留），不与本地 RoutingSessionID 比较。`sess_hdr_in/out` 的位 0–13 依次为 session_id、session-id、conversation_id、x-session-id、x-session-affinity、x-opencode-session、x-conversation-id、thread_id、thread-id、x-codex-turn-state、x-codex-turn-metadata、x-client-request-id、x-claude-code-session-id、x-grok-conv-id；14 留给 Anthropic metadata.user_id，15 保留。`thread_present` 同时识别 client_metadata.thread_id 的存在性。
- `usage_fields` 位 0–5：cached_tokens（含 cache_read_tokens 兼容别名）、prompt_cache_hit_tokens、cache_read_input_tokens、cachedContentTokenCount、cache write/creation 系列、prompt_cache_diagnostics。显式 0 仍记为存在；位图不改变 usage 优先级或启用新别名计费。
- `id_source` 复用本地会话信号的选择顺序；`conv_anchor` 为 API key 作用域内前导指令与首条 user 的 HMAC（8 字节）。request_id_hmac 和 session_digest 与既有 openai_cache_usage 的对应字段完全同值，不另加域、不再截断。不记录明文 body、会话头或缓存键。布尔字段在事件中编码为 0/1。
- `transport=plugin` 来自现有 ShouldRouteOpenAIOAuth 的只读判断；其快照属于插件前，请勿当作最终上游 wire。P0a 不输出 WS Bridge 轮次；`ingress_ws_bridge` 恒为 0。不改插件路径。
- P0a 仅覆盖 OpenAI Responses / Chat HTTP；原生 WS、非 OpenAI、monitor probe 不新增归因。快照在最终兼容处理后抓取，Chat 的最终 session_id 单独更新，失败尝试之间重置出站字段；异步结算持有独立快照。

异步结果未就绪时，入站 body 类别为 unknown；出站 prompt cache key 如没有注入来源标记也记 unknown，待 worker 确认存在性后才判 passthrough 或 none。

### 出站块链与位置

`gateway.cache_trace.chain_percent`（`GATEWAY_CACHE_TRACE_CHAIN_PERCENT`）默认 100，按隔离后的会话稳定采样；0 不计算块链。Responses 按 tools → instructions → text/reasoning/tool_choice → input 各项分块；Chat 按 tools → messages 各项分块。只读最终原始字节，不重写请求。

- `blk_n` 是完整块数。`bytes_out` 为协议块逻辑字节总数（不含 JSON 骨架及非块字段）；`fork_blk`、`fork_byte` 是与最近三条历史中最长匹配链的分叉位置，`fork_ref` 为 none / prev1 / prevk / other_conv。`fork_raw_byte` 和 `bytes_out_raw` 保留 wire 偏移/长度。`blk_order_physical_eq_logical` 表示各块物理起点是否按逻辑顺序递增。
- 每条历史最多 512 个 uint64 检查点，另存末块哈希、块数与逻辑总长；前 64 块保持精确，尾部检查点达到上限时隔一个保留一个、步长翻倍。扫描到上一轮末块并命中其哈希，就证明完整覆盖上一轮，任意长度的纯追加均可识别。沿用原 telemetry 的 2048 会话上限和锁；最多 2048×3×512×8 = 25,165,824 字节哈希，另有少量元数据。失败或未送达尝试不成为历史。检查点序号由步长推导，分叉坐标在当前扫描中取得；历史只保留逻辑总长。可选历史指针仅在采样且结算时分配，默认关闭不会使 legacy map 值超过 Go 的内联阈值。
- 块哈希使用进程启动随机种子的 xxhash64，只在内存中比较，禁止出现在日志、接口或持久化中；日志标识仍使用 HMAC。逻辑和 raw 位置在同次扫描中取得，关闭观测不扫描。
- `tail_kind`、`reasoning_items_n`、`old_reasoning_n`、`first_old_reasoning_blk/byte` 记录 reasoning 相对最后一条 user 的位置；仅最后 user 之前的 reasoning 算旧回合。`first_old_reasoning_raw_byte` 为排查保留 wire 位置。
- `include_encrypted_reasoning`、`store_false`、`reasoning_enc_n` 只记录，不推断提供方是否保留密文。`reasoning_ctx` 读取最终出站字段（absent / auto / current_turn / all_turns / other）；静态路径列表 `gateway.cache_trace.reasoning_context_paths` 默认 `["reasoning.context"]`。原生 Chat 记 absent。
- `model_gen` 细分为 pre56 / gpt56 / gpt6x / other_openai / non_openai，只记录类别，不输出上游原始型号。

提供方实际渲染模板和 tokenizer 未知，逻辑字节位置只是近似前缀位置；原始 JSON 字段顺序不参与 token 位置估算。Codex 形态 1MB / 2MB / 5MB 基准分别报告同步路径 p99 增量与 worker 吞吐；部署后的真实端到端延迟仍由 EXP-0 验证。

关闭路径由 Go 入口测试、生产转发快照和分配对照共同验证；trace 只在总闸之后创建，关闭时不入队、不读取 body、不计算链。

异步预算 `gateway.cache_trace.async_byte_budget` 默认 67108864（64MiB），`async_workers` 默认 1（1–8；代码构造配置的 0 使用默认）。入站顶层查找、锚点 HMAC、最终出站读取及块链均在 HTTP 调用返回响应头后由 worker 执行；同步路径仅保存只读引用、来源标记和头位图。预算在创建任务、查历史之前以 CAS 预留，包括待发送、排队和执行中的入站引用、出站引用、备用读取副本和任务元数据；超预算直接丢弃，不等待。未发送的替换或失败尝试释放预留，已入队任务由 worker 释放。worker 对支持一次完整 WriterTo 的不可变出站源仅在回调内借用，结果不保留原始视图；其他源在预算内复制。`gateway.cache_trace.async_bytes_per_second` 默认 33554432（32MiB/s），worker 按入站与出站原始字节之和等待令牌，突发容量等于内存预算；令牌不足时任务继续占用队列预算，只有 worker 等待，转发及结算不等待。队列及令牌等待超过 `gateway.cache_trace.max_queue_wait`（默认 10s）或已建立的会话链历史被淘汰才按 `expired` 丢弃；从实际入队开始计时。不存在独立的 body 大小门槛。覆盖原因 `na_reason` 按 `chain_percent`、`budget_full`、`expired`、`not_ready`、`hist_unavailable` 排序，仅随结算事件的 `loss_class=n/a_sampled_out` 输出。结算时结果未就绪也记覆盖缺口，响应和结算绝不等待。budget_full、expired、not_ready 的精简事件保留版本、平台、原因、账号/分组数字 ID、最终计费 P/read 及关联键；P/read 直接复用最终 usage，request_id_hmac/session_digest 与既有用量日志同值，已计算的 request HMAC 直接复用。预算拒绝不执行逐帧观测，已知过期的响应也直接使用原计费解析器。两者都到齐的历史才发布，超过 10 分钟不再发布；晚完成按结算时间排序，不能覆盖更新的历史。

`chain_sampled=0` 表示链未计算/未就绪（含插件）；未知块数/逻辑长度/reasoning 数为 −1，相关枚举为 n/a。同步来源仍可用，但异步类别未完成时 pck_class_in/id_source 可以是 unknown，conv_anchor 为空；这类记录不得当作正常分类样本。布尔位置字段在 chain_sampled=0 时不解读。

`fork_kind` 为 append（纯追加）/ identical（完全相同）/ cur_prefix（可证明的截尾，exact）/ tail_unproven（检查点间截尾，range）/ diverge（已发现不等检查点，exact 或 range）/ none（无历史或链未就绪）。append、identical、none 的 fork_precision 为 none。K=3 候选按可精确证明优先、已证明前缀最长、历史最新排序。

`fork_precision` 为 exact / range / none。none 表示完整覆盖历史或无历史，位置为 −1，通过 fork_ref 区分；range 的 fork_blk/byte 与 fork_blk_hi/byte_hi 分别给出包含真实分叉的下界和上界。截尾恰落在可验证检查点时可精确定位；落在稀疏间隔中仍保留范围，避免把无法验证的尾部误称未变。`p_prev` 和 `hist_age_s` 是最佳匹配已结算请求的 P 与结算年龄（秒）；无历史为 −1。上一响应结算尚未完成时可能匹配到更早的链，EXP-0 应结合年龄量化错位。

Responses 的 input 从第 3 块开始，Chat 的 messages 从第 1 块开始；缺失结构块也占序号。设置块保留整个 text 对象（含 verbosity）。删除结构块时 fork_raw_byte 可以为 −1；raw 字段只作排查，逻辑字段用于归因。model_gen 同时识别 openai/ 前缀和 codex-mini 系列。

### P0a 入口范围与关联字段

P0a 只为 HTTP Responses（含 compact）、Chat→Responses 和原生 Chat 生成快照。Messages 桥、WS/HTTP Bridge 轮次、count_tokens、images 和 alpha search 排除，留待 P0b。`ingress_ws_bridge` 在 P0a 输出中恒为 0；不将 WS 首轮入站信息复用于后续轮次。事件输出由 T0a.3 接入，T0a.1/T0a.2 及其修复只准备快照。

`session_digest` 完全沿用 `openai_cache_usage` 的会话摘要；`request_id_hmac` 沿用该事件完整的请求 HMAC。两者不另加域或再截断，离线可以直接关联。`sess_out_relation=fingerprint_converged` 表示指纹收敛写入账号级常量 session_id，与 gw_isolated 区分。所有标识仅内部使用，块哈希仍不出日志。

读取位只接受数值类型；null、字符串等无效值不表示可用读取字段，数字 0 算存在。非流式 Chat 的 buffered 终帧与流式路径使用相同的存在性观测，不改变计费解析或读取优先级。

conv_anchor 按既定的前导 system/developer 和首条 user 计算；Codex 的首条 user 可能只是 AGENTS 或环境信息，同一工作区的不同对话可能共享锚点，从而低估 other_conv。此限制留待 P0b 与 EXP-0 校验。

正式回归包含基线上的 510 组生产转发路径快照（开关两态逐字节比对）、成功响应 usage 观测和日志隐私断言。早期 120 组 builder 快照仅作补充，不计作独立生产路径覆盖。启用观测时类别和块链在 worker 中共享一次最终 body 读取，优先借用不可变完整缓冲区，不支持借用时按 ContentLength 预分配；既有诊断的 Lite 改写前读取保持原口径，端到端成本单独报告。
