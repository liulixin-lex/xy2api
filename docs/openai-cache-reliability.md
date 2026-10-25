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

buffered SSE 的观测复用既有计费遍历已解析的帧；关闭时直接调用原解析器。每个 buffered 请求只设置一次异常隔离：观测返回错误后停用该请求后续观测，panic 后丢弃局部 usage 并用原解析器重算；这一步不执行结算，原有结算仍只执行一次。观测失败保持 `result.CacheTrace=nil`，由独立失败标记在结算时输出恰好一条 `observer_failed` 最小事件；它只使用计费已有的 P/read、关联键和入口标量，不重解析。异常原文不写日志，事件输出失败也不能影响计费。流式路径不增加逐帧 recover，依赖只读解析与固定字段操作；上述回调故障隔离范围是 buffered 遍历。

运行时 `cache_observability_policy.enabled` 默认 false；管理员在“缓存观测 → 开关与参数”开启。生效条件是静态 `gateway.cache_features.kill_switch=false`、观测策略开启、既有 `gateway.openai_cache.diagnostics_enabled=true`，并有持久 JWT secret。关闭时不进行额外 JSON 解析或请求体读取。观测不改变请求 bytes、选号、turn-state 或计费；事件在原有 usage 观测之后结算输出；未送达请求不输出，失败尝试不成为历史。完整、拒绝、observer_failed 和 no_session 路径共用请求入口的运行时门控，关闭请求不创建观测状态、不进入结算或输出。

- 入站 `pck_class_in` 只看原 body：none / empty / client_uuid / client_non_uuid；UUID 用固定 8-4-4-4-12 十六进制正则，不校验版本位。
- 出站 `pck_class_out` 按实际注入/改写点记录：none / passthrough / gw_compat_hex / gw_from_header / oauth_rewritten；gw_grok 仅预留来源标记（P0a 排除 Grok），gw_uuid 留给后续身份改动。`pck_out_is_uuid` 只表示形态，不用于推导来源。
- `sess_out_relation` 表示下划线 session_id 的来源：none / client_value / gw_isolated / uuid_of_pck（eq_pck 预留），不与本地 RoutingSessionID 比较。`sess_hdr_in/out` 的位 0–13 依次为 session_id、session-id、conversation_id、x-session-id、x-session-affinity、x-opencode-session、x-conversation-id、thread_id、thread-id、x-codex-turn-state、x-codex-turn-metadata、x-client-request-id、x-claude-code-session-id、x-grok-conv-id；14 留给 Anthropic metadata.user_id，15 保留。`thread_present` 同时识别 client_metadata.thread_id 的存在性。
- `usage_fields` 位 0–5：cached_tokens（含 cache_read_tokens 兼容别名）、prompt_cache_hit_tokens、cache_read_input_tokens、cachedContentTokenCount、cache write/creation 系列、prompt_cache_diagnostics。显式 0 仍记为存在；位图不改变 usage 优先级或启用新别名计费。
- `id_source` 复用本地会话信号的选择顺序；`conv_anchor` 为 API key 作用域内前导指令与首条 user 的 HMAC（8 字节）。request_id_hmac 和 session_digest 与既有 openai_cache_usage 的对应字段完全同值，不另加域、不再截断。不记录明文 body、会话头或缓存键。布尔字段使用 JSON 原生 true/false。
- `transport=plugin` 来自现有 ShouldRouteOpenAIOAuth 的只读判断；其快照属于插件前，请勿当作最终上游 wire。P0a 不输出 WS Bridge 轮次；`ingress_ws_bridge` 恒为 false。不改插件路径。
- P0a 仅覆盖 OpenAI Responses / Chat HTTP；原生 WS、非 OpenAI、monitor probe 不新增归因。快照在最终兼容处理后抓取，Chat 的最终 session_id 单独更新，失败尝试之间重置出站字段；异步结算持有独立快照。

异步结果未就绪时，入站 body 类别为 unknown；出站 prompt cache key 如没有注入来源标记也记 unknown，待 worker 确认存在性后才判 passthrough 或 none。

缓存观测覆盖至少发生一次上游尝试、并进入用量结算的 OpenAI 请求；仅入站或调度、尚未尝试上游的请求不输出 `cache_trace` 或 `openai_cache_usage`。未投递且不计费的请求不输出 `cache_trace`；若既有 abuse 规则仍按上游成本计费，则只输出 12 字段最小事件，`loss_class=n/a_sampled_out`、`na_reason=undelivered`，P/read 和关联键复用同次计费用量，不参与损失归因。已有发送时序会关闭，但这类请求不补入链历史。

事件在 `RecordUsage` 入口、后续计费持久化之前输出，与既有 `openai_cache_usage` 一致；它表示已进入结算处理，不证明数据库写入或扣费最终成功。buffered 共用遍历的观测异常按请求隔离；流式逐帧观测依赖只读解析和固定字段，不加入逐帧 recover。结算及日志输出的异常隔离独立存在。

### 路由原因与改绑原因

完整 `cache_trace` 事件的 `route_reason` 记录本次请求实际决策点，优先级为 `last_resort > fallback_group > failover_n > session_rebind > soft_divert > overflow > prev_owner > sticky_hit > sticky_new`；证据不足时为 `unknown`。它观察最终选号结果与既有切换计数，不参与排序、准入、绑定、槽位或重试预算。每次选择仅清除上次的软罚保留与末位兜底候选标记；已确认的最高路由原因、请求级切换与改绑原因保留到最终成功尝试，不会因后续尝试而降级。

`sticky_hit`、`prev_owner`、`overflow` 来自调度器已有的准确标记，`sticky_new` 仅用于明确的负载均衡新选；`soft_divert` 要求既有逻辑保留软罚所有者、最终实际选择另一账号。`last_resort` 只对应真实末位兜底选择，`fallback_group` 只读取已成功的无号兜底事实，`failover_n` 只在 handler 已批准跨账号切换后记录，同账号重试不计入。份额调度缺少上述标记的分流、新会话等路径仍为 `unknown`，不从名称推测路由原因。

`rebind_cause` 只有既有会话失败计数达到改绑阈值后才更新，独立于更高优先级的路由原因：`stall` 表示停滞，`first_event_timeout` 表示首输出超时，`pre_output_error` / `post_output` 按既有错误类别与真实首输出状态区分，其余为 `other`；未改绑为 `none`。原 `RequestState` 不新增字段或写入。

这些字段只出现在内部完整事件；预算、未就绪等最小事件仍保持既有 12 字段格式。无会话请求沿用 `session_digest=""`、`gap_src=no_session` 的观测，不为路由归因新建会话或历史。兜底组的原组 ID、组名不写入 trace，响应头、响应正文与用户用量 DTO 也不增加路由原因或兜底信息；用户用量仍按既有原分组口径展示。

### 出站块链与位置

运行时 `sample_percent` 默认 100，按隔离后的会话稳定采样；`chain_percent` 默认 100，在第一层样本内用独立会话分桶采样，0 不计算块链。Responses 按 tools → instructions → text/reasoning/tool_choice → input 各项分块；Chat 按 tools → messages 各项分块。只读最终原始字节，不重写请求。

- `blk_n` 是完整块数。`bytes_out` 为协议块逻辑字节总数（不含 JSON 骨架及非块字段）；`fork_blk`、`fork_byte` 是与最近三条历史中最长匹配链的分叉位置，`fork_ref` 为 none / prev1 / prevk / other_conv。`fork_raw_byte` 和 `bytes_out_raw` 保留 wire 偏移/长度。`blk_order_physical_eq_logical` 表示各块物理起点是否按逻辑顺序递增。
- 每条历史最多 512 个 uint64 检查点，另存末块哈希、块数与逻辑总长；前 64 块保持精确，尾部检查点达到上限时隔一个保留一个、步长翻倍。扫描到上一轮末块并命中其哈希，就证明完整覆盖上一轮，任意长度的纯追加均可识别。沿用原 telemetry 的 2048 会话上限和锁；最多 2048×3×512×8 = 25,165,824 字节哈希，另有少量元数据。失败或未送达尝试不成为历史。检查点序号由步长推导，分叉坐标在当前扫描中取得；历史只保留逻辑总长。可选历史指针仅在观测开启且已有会话的发送或结算时分配，默认关闭不会使 legacy map 值超过 Go 的内联阈值。

- 每条历史最多 512 个 uint64 检查点，另存末块哈希、块数与逻辑总长；默认上限下前 64 块保持精确，尾部检查点达到上限时隔一个保留一个、步长翻倍。扫描到上一轮末块并命中其哈希，就证明完整覆盖上一轮，任意长度的纯追加均可识别。沿用原 telemetry 的锁与 2048 个 diagnostics 会话；新增 trace 历史默认最多 1024 个会话，最多 1024×3×512×8 = 12,582,912 字节保留哈希，另有元数据。静态上限调低时，只减少 trace 历史与保留检查点，末块哈希仍保留。失败或未送达尝试不成为历史。检查点序号由步长推导，分叉坐标在当前扫描中取得；历史只保留逻辑总长。可选历史指针仅在采样且结算时分配，默认关闭不会使 legacy map 值超过 Go 的内联阈值。
- 块哈希使用进程启动随机种子的 xxhash64，只在内存中比较，禁止出现在日志、接口或持久化中；日志标识仍使用 HMAC。逻辑和 raw 位置在同次扫描中取得，关闭观测不扫描。
- `tail_kind`、`reasoning_items_n`、`old_reasoning_n`、`first_old_reasoning_blk/byte` 记录 reasoning 相对最后一条 user 的位置；仅最后 user 之前的 reasoning 算旧回合。`first_old_reasoning_raw_byte` 为排查保留 wire 位置。
- `include_encrypted_reasoning`、`store_false`、`reasoning_enc_n` 只记录，不推断提供方是否保留密文。`reasoning_ctx` 读取最终出站字段（absent / auto / current_turn / all_turns / other）；静态路径列表 `gateway.cache_trace.reasoning_context_paths` 默认 `["reasoning.context"]`。原生 Chat 记 absent。
- `model_gen` 细分为 pre56 / gpt56 / gpt6x / other_openai / non_openai，只记录类别，不输出上游原始型号。

提供方实际渲染模板和 tokenizer 未知，逻辑字节位置只是近似前缀位置；原始 JSON 字段顺序不参与 token 位置估算。Codex 形态 1MB / 2MB / 5MB 基准分别报告同步路径 p99 增量与 worker 吞吐；部署后的真实端到端延迟仍由 EXP-0 验证。

关闭路径由 Go 入口测试、生产转发快照和分配对照共同验证；trace 只在总闸之后创建，关闭时不入队、不读取 body、不计算链。

运行时 `cache_observability_policy.byte_budget_mib` 默认 64 MiB，静态 `gateway.cache_trace.workers` 默认 1（1–2）。入站顶层查找、锚点 HMAC、最终出站读取及块链均在 HTTP 调用返回响应头后由 worker 执行；同步路径仅保存只读引用、来源标记和头位图。预算在创建任务、查历史之前以 CAS 预留，包括待发送、排队和执行中的入站引用、出站引用、备用读取副本和任务元数据；超预算直接丢弃，不等待。未发送的替换或失败尝试释放预留，已入队任务由 worker 释放。worker 对支持一次完整 WriterTo 的不可变出站源仅在回调内借用，结果不保留原始视图；其他源在预算内复制。运行时 `cache_observability_policy.rate_mib_per_s` 默认 32 MiB/s，worker 按入站与出站原始字节之和等待令牌，突发容量等于内存预算；令牌不足时任务继续占用队列预算，只有 worker 等待，转发及结算不等待。队列及令牌等待超过 `gateway.cache_trace.max_queue_wait`（默认 10s）或已建立的会话链历史被淘汰才按 `expired` 丢弃；从实际入队开始计时。不存在独立的 body 大小门槛。覆盖原因 `na_reason` 按 `chain_percent`、`budget_full`、`expired`、`not_ready`、`hist_unavailable` 排序，仅随结算事件的 `loss_class=n/a_sampled_out` 输出。结算时结果未就绪也记覆盖缺口，响应和结算绝不等待。budget_full、expired、not_ready、observer_failed 的最小事件固定保留 12 个字段：v、platform、loss_class、na_reason、request_id_hmac、session_digest、account_id、group_id、P、read、pol_rev、ua_family；P/read 直接复用最终 usage，request_id_hmac/session_digest 与既有用量日志同值，已计算的 request HMAC 直接复用。预算拒绝不执行逐帧观测，进入解析器前已知过期的响应也直接使用原计费解析器；进入后保持该次尝试的观察决定。两者都到齐的历史才发布，超过 10 分钟不再发布；晚完成按结算时间排序，不能覆盖更新的历史。


`chain_sampled=false` 表示链未计算/未就绪（含插件）；未知块数/逻辑长度/reasoning 数为 −1，相关枚举为 unknown，相关布尔为 false。同步来源仍可用，异步类别未完成时 pck_class_in/id_source 和 conv_anchor 可以是 unknown；chain_sampled=true 时 conv_anchor 必须是 HMAC。

`fork_kind` 为 append（纯追加）/ identical（完全相同）/ cur_prefix（可证明的截尾，exact）/ tail_unproven（检查点间截尾，range）/ diverge（已发现不等检查点，exact 或 range）/ none（无历史或链未就绪）。append、identical、none 的 fork_precision 为 none。K=3 候选按可精确证明优先、已证明前缀最长、历史最新排序。

`fork_precision` 为 exact / range / none。none 表示完整覆盖历史或无历史，位置为 −1，通过 fork_ref 区分；range 的 fork_blk/byte 与 fork_blk_hi/byte_hi 分别给出包含真实分叉的下界和上界。截尾恰落在可验证检查点时可精确定位；落在稀疏间隔中仍保留范围，避免把无法验证的尾部误称未变。`p_prev` 和 `hist_age_s` 是最佳匹配已结算请求的 P 与结算年龄（秒）；无历史为 −1。上一响应结算尚未完成时可能匹配到更早的链，EXP-0 应结合年龄量化错位。

Responses 的 input 从第 3 块开始，Chat 的 messages 从第 1 块开始；缺失结构块也占序号。设置块保留整个 text 对象（含 verbosity）。删除结构块时 fork_raw_byte 可以为 −1；raw 字段只作排查，逻辑字段用于归因。model_gen 同时识别 openai/ 前缀和 codex-mini 系列。

### P0a 入口范围与关联字段

P0a 只为 HTTP Responses（含 compact）、Chat→Responses 和原生 Chat 生成快照。Messages 桥、WS/HTTP Bridge 轮次、count_tokens、images 和 alpha search 排除，留待 P0b。`ingress_ws_bridge` 在 P0a 输出中恒为 false；不将 WS 首轮入站信息复用于后续轮次。每个纳入观测且已结算的请求恰好输出一条事件。

`session_digest` 完全沿用 `openai_cache_usage` 的会话摘要；`request_id_hmac` 沿用该事件完整的请求 HMAC。两者不另加域或再截断，离线可以直接关联。session_digest 保留现有 v2: 加 16 位 hex、纯 16 位 hex 或无会话空串；无会话时完整事件的 gap_src 为 no_session，不创建历史，既有用量日志不输出该类事件，采集端单独计为不可关联。`sess_out_relation=fingerprint_converged` 表示指纹收敛写入账号级常量 session_id，与 gw_isolated 区分。所有标识仅内部使用，块哈希仍不出日志。

读取位只接受数值类型；null、字符串等无效值不表示可用读取字段，数字 0 算存在。非流式 Chat 的 buffered 终帧与流式路径使用相同的存在性观测，不改变计费解析或读取优先级。

conv_anchor 按既定的前导 system/developer 和首条 user 计算；Codex 的首条 user 可能只是 AGENTS 或环境信息，同一工作区的不同对话可能共享锚点，从而低估 other_conv。此限制留待 P0b 与 EXP-0 校验。

正式回归包含基线上的 510 组生产转发路径快照（开关两态逐字节比对）、成功响应 usage 观测和日志隐私断言。早期 120 组 builder 快照仅作补充，不计作独立生产路径覆盖。启用观测时类别和块链在 worker 中共享一次最终 body 读取，优先借用不可变完整缓冲区，不支持借用时按 ContentLength 预分配；既有诊断的 Lite 改写前读取保持原口径，端到端成本单独报告。

### 结算分类与覆盖缺口

完整事件包含 P/read、p_prev、hist_age_s、chain_sampled 和归因输入。分类顺序为 n/a_plugin → n/a_usage → n/a_sampled_out → n/a_truncated → below_min → cold → other_conv → full → prefix_changed → provider_reasoning_drop_suspect → under_read。缺少 P 或数值缓存读取字段属于 n/a_usage，显式数字 0 视为存在。OpenAI（含 Messages 桥）的读取类位为 usage_fields 的 bit0/2/3；只有 bit1（prompt_cache_hit_tokens 别名）时，计费解析没有可用 read，因此同样记 n/a_usage，不输出 na_reason，也不设置读量归因标记。别名存在性仍保留，计费保持原行为。覆盖缺口不计入机会量归因；plugin 和 chain_percent 仍输出完整事件，异步未就绪或拒绝输出最小事件。结算冻结一次 worker 快照，不等待，也不补发事件。

`gateway.cache_trace.min_cacheable_tokens` 默认 1024，比较上游报告的 P；P 缺失不能判 below_min。主力 GPT-5.6 及以上型号的最低可缓存长度为 1024 可见 token，旧型号和非官方后端可能不同，在线不维护模型阈值表，离线分析可按型号重新分类。`tok_per_byte_milli` 是 P/逻辑总字节数的千倍整数；未知记 −1，判定使用未取整的密度。full 比较 read 与 min(p_prev,P)，精确计数容差固定 256；涉及估算 token 位置的容差为 max(256,3%·估算值)。range 两端结论相同才采用，否则 n/a_truncated；检查点间截尾的上界按可能未分叉解释。

`reasoning_ctx_eff` 保留响应声明（未出现为 unknown），`rsn_boundary_hit` 与 `read_short_of_fork` 独立记录。只有最后输入是 user、存在旧回合 reasoning、读量靠近其边界且有效上下文为 current_turn，才可能归为 provider_reasoning_drop_suspect；该类始终是线索。上下文先取响应，再取出站显式值，再取模型默认（pre56=current_turn、gpt56=all_turns，其余 unknown）。

### 时间、客户端与响应枚举

`gap_src` 为 settled / prev_unsettled / no_prev / no_session。仅 settled 输出 gap_send_ms，等于本次发送时刻减上一请求结算时刻，可为负。时序只扩展已有观测历史，不参与选号；LRU 淘汰后不让迟到 worker 重建旧链。已入队但结算未就绪的请求仍关闭时序记录，若后续得到链与已知 P，可补入历史；已发出的最小事件保持不变。冷会话重叠及超过四个未结算请求的时序只能保守报告，不能把不可得当作真实零间隔。

`codex_hdr` 的前三位依次表示 x-openai-subagent、x-codex-parent-thread-id、x-codex-window-id 的存在性，其他位为 0；不输出头值。window_seq_delta 比较相同 thread 的窗口序号，未知 −1、thread 改变 −2、序号倒退 −3，不输出 thread 原文。`ua_family` 仅看 UA 首个 / 之前的产品 token、忽略大小写，枚举为 codex_cli_rs / codex_tui / codex_desktop / codex_exec / codex_vscode / codex_other / litellm / go_http / openai_sdk / other；没有 UA 或未识别产品均为 other。

`pcd` 只记录 prompt_cache_diagnostics 的白名单枚举，未出现为 absent、未识别为 other。`resp_cache_ttl` 为 30m / 12h / 24h / in_memory / other / absent；响应字段与计费使用同一遍历结果。usage_fields 的 bit6/bit7 分别记录 30m/12h 写入明细的存在性，显式 0 同样记为存在。pol_rev 从请求开始时取得的策略快照读取，异步结算与 observer_failed 标记沿用同一版本。

cache_trace 仅为内部 Info 日志，经现有 slog→zap JSON logger 输出；事件载荷平铺，采集端剥离 level/time/logger/caller/msg/service/env/stacktrace 后校验。关闭态逐帧最多一次 attempt-local 布尔判断，直接使用原计费解析器。日志采样会降低事件覆盖率，采集端应与同请求的 openai_cache_usage 一对一核对关联键、账号、服务分组及 P/read。

## 运行时缓存策略管理

管理员专用接口位于 `/api/v1/admin/cache/policies`：列表、按键读取、
POST /{key}/validate、PUT /{key}、GET /history。当前构建提供 master（cache_feature_master）、
observability（cache_observability_policy）、northstar（cache_northstar_policy）和
upstream_identity（cache_upstream_identity_policy）、anthropic_identity
（cache_anthropic_identity_policy）和 routing（cache_routing_policy）；六者缺省关闭。
T4.0 的后两个键支持管理员读写与审计，仅保存配置，尚未接入会话身份或选号行为。

写入省略的字段保留原值，未知字段拒绝。每键的 `expected_rev` 在数据库
事务和行锁内比较；冲突返回 409。启用总开关要求理由和勾选，PUT 会重新计算，
缺失返回 422；关闭方向不要求确认。理由去掉首尾空白后最多 200 个 Unicode 字符。
validate 的校验失败返回 200、`valid=false`，不写审计；PUT 成败均由管理审计
中间件记录，成功记录四个摘要 `policy_key/rev_before/rev_after/changed_fields`。
history 缺省最近 20 条，最多 100 条；可按键、时间和 `after_rev` 查询。
历史项的 `at` 统一按 RFC3339 UTC（`Z` 后缀）返回，不受审计数据库读取会话时区影响。
完整性按每键全部已落盘的成功 revision 判断，不受分页影响，失败写入不消耗 revision。
最小 revision 大于 1 时，返回 `history_complete=false` 与 `history_incomplete_reason="earlier_missing"`；
从 1 开始但中间缺号时，原因为 `gap`。完整时原因为 null。最新一条尚未落盘不算缺失。
若没有成功记录，保存的 rev 为 0 或更新时间距服务端现在不超过 5 秒时仍视为完整，否则为 earlier_missing。
多键查询中，任一键不完整就返回 false，earlier_missing 优先于 gap。

写入实例立即发布不可变原子快照，其他实例周期刷新，正常数据库可用时最多约
60 秒生效；读库失败保留已加载快照，单键存储无法解析则关闭该键。每个请求和
WebSocket turn 使用固定快照，重试、failover 和流式过程不重新加载。后台循环
每轮获取一次。开关读取无数据库访问、零分配。

`gateway.cache_features.kill_switch` 是静态紧急总闸，默认 false。总闸开启时仍可
保存配置，界面显示 killed。它和运行时总开关均不控制既有的
`gateway.openai_cache.*`、Claude 自动缓存或韧性设置；既有机制只读展示，
管理链接为 `/admin/settings?tab=gateway`。观测不受 master 控制，
实际条件为总闸关闭、观测 enabled、原有 diagnostics_enabled 开启。
没有指标自动调整开关。

资源配置是硬上限：workers 1..2（默认 1）、max_byte_budget_mib 64、
max_rate_mib_per_s 32、max_sessions 1024、chains_per_session 3、
max_checkpoints 512。运行时 byte_budget_mib 最小 8、rate_mib_per_s 最小 1；
写入超出静态上限返回 400。重启降低上限后保留存储值，实际值取较小者，并在
clipped_fields 中报告。sample_percent 与 chain_percent 为 0..100，
按会话稳定采样；总闸需要修改配置并重启，普通参数无需重启。


运行时观测接线后，每个 HTTP 请求在鉴权与初始 serving 组解析后固定一份不可变策略；同一请求的重试、回退和流式结算沿用它。WS 入站每个 turn 重新固定策略，当前版本的 trace 采集范围仍为 HTTP Responses/Chat。事件的 `pol_rev` 使用该请求的版本向量，例如 `m3.o7.u0.a0.r0`，不会在请求中途变化。

静态资源上限支持调低：`max_byte_budget_mib` 为 8–64、`max_rate_mib_per_s` 为 1–32、`max_sessions` 为 1–1024、`chains_per_session` 为 1–3、`max_checkpoints` 为 1–512。会话与检查点上限仅限制新增 trace 历史和时序状态，既有 diagnostics 的会话保留与排序不受影响；worker 固定大小的临时扫描空间仍按 512 个检查点预留。运行时预算缩小时，已预留的任务正常排空，新任务需符合新预算；速率通过原 limiter 更新，不重建池、不等待转发。重启后静态上限变小，生效值取保存值与新上限的较小值，接口在 `clipped_fields` 提示。

撤回观测：在管理员面板关闭观测，检查本实例 `loaded.rev` 与保存的 `stored.rev` 一致，并等待其他实例最多 60 秒刷新。已在处理的请求沿用原快照；下一请求采用新策略。关闭总开关只停止受它控制的行为类策略，观测不受总开关控制。若 settings 故障，修改静态 kill switch 并按部署流程重启；总闸期间仍可保存策略，解除前可先关闭领域开关。每次 PUT 的操作者、理由、前后版本和字段名写入现有审计日志，409 冲突须刷新当前版本后重新确认。

## 在线北极星与管理员图表

管理员 GET 接口为 `/api/v1/admin/cache/{northstar,breakdown,stats,experiments}`，
沿用管理员鉴权。普通用户无权访问，用户设置与 DTO 不包含这些数据。
`northstar` 和 `breakdown` 只查询小时汇总，缓存响应 60 秒；
未产生完整小时时返回 503 / `CACHE_ROLLUP_NOT_READY`。
`stats` 是当前进程的最近 1h/24h 分钟计数，重启清零；观测关闭时返回
200、空数组和 null 比率。`experiments` 在本阶段返回空列表。

`cache_northstar_policy.rollup_enabled` 默认 false，独立于 master，
受静态 `gateway.cache_features.kill_switch` 控制。它沿用策略管理的 revision、
loaded rev、约 60 秒跨实例刷新与管理员审计。首次开启从最近完整小时开始，
再按 `backfill_days` 小块向前回填，默认 7 天、最大 30 天。
变更排除组、媒体规则、模型阈值或客户端家族规则时须同时更新 `policy_version`；
不同版本不拼接。阈值仅允许 512、1024、2048、4096、6144，精确规则优先，
前缀规则取最长匹配。未知阈值模型保留在业务范围与排名中，严格北极星不包含它们。

汇总统一使用 UTC 和互斥入库 token 桶：
P = input + cache creation + cache read。平台优先取服务分组配置，其次取账号配置。
会话机会量为 max(min(P_prev, P) − read, 0)，在所有 P>0 行中按完成时刻和 ID
配对；同 API key、服务分组与平台的记录会话间隔超过 1 小时断开，空会话不重建。
数据库仅保留会话复合键摘要，不向接口输出摘要、用户 ID、API key、会话值或 UA 原文。
账号、host、客户端家族、协议为汇总维度；账号名称与 host 取聚合时快照，
host 不含凭据、端口、路径或查询参数。少于 10 个请求的组并入 other。

后台沿用 dashboard 的 leader 锁，在其更新水位后独立执行；北极星使用自己的水位，
不修改 dashboard 表、间隔或水位，也不写 usage_logs。默认关闭时入口即返回，
不执行聚合、回填、清理 SQL。每天 00:30 UTC 后第一次运行，在同一事务内重算前一
UTC 日 24 个小时，吸收迟到行；失败回滚，可安全重复或从已提交回填边界恢复。
小时完成标记区分真实空桶与缺失小时，窗口边界日和缺小时标为 partial。
汇总保留 180 天，trace 小时计数保留 90 天。

金额以已结算分项成本计算窗口内 platform/model 加权单价，
不是当前价目表。Anthropic 有 creation 时使用实际混合 creation 单价。
无正价或分母不足时金额为 null。list 是价差节省，
charged 使用逐请求 actual/total（零总额回退请求倍率），upstream 只使用未重新定价的
account_stats_cost 与账号倍率；美元加权命中率始终使用 list。
北极星的 priced_models 只计严格范围内有行的已定价 (platform, model_key)，
priced_p_share 仍按业务范围的有价 P / 业务 P 计算，三种 cost_view 计数一致。
窗口均价也用于小时趋势；标注价格覆盖、窗口内价格变化和本地改写候选，
这些值仅供展示，不进入选号、sticky、准入、overflow 或自动开关。
本阶段没有固定构成权重表，standardized 字段返回 null。
事件只来自缓存配置审计及当前进程启动，不补历史生产部署时间线。

`cache_observability_policy.persist_trace_hourly` 只在观测开启时生效；
小时计数在整点后的 leader 周期幂等写入，携带进程标识防止覆盖其他进程数据。
计数与统计不修改 cache_trace 日志载荷、计费或出站内容。

撤回：关闭 rollup_enabled，检查 loaded rev 并等待其他实例刷新；
下一后台周期不再聚合或回填，已提交汇总仍可查询。关闭观测会停止新的 trace 计数，
已在途请求沿用入口快照。紧急总闸按部署流程配置并重启。
迁移 279 只新增北极星小时汇总、汇总水位、小时完成标记、事件与 trace 小时汇总五张表；
回退程序可保留这些表，禁止回退时修改既有 usage/dashboard 数据或已登记迁移校验和。

北极星按 UTC 存取小时边界和事件时间，数据库会话时区不会改变图表分桶。小时查询同时读取前一小时，在会话内配对后再截取目标小时；因此跨整点机会量不依赖持久化会话状态。迁移 279 为跨口径版本的保留期清理提供单列时间索引，首次在共享数据库执行后保持校验和不变，结构修订需另加迁移。

管理员图表对符合邮箱形态的账号名使用 `<platform>#<account_id>` 稳定标签，并保证与响应行的 `meta.platform` 和 `key` 一致；分组名采用同一邮箱判定并替换为 `group#<group_id>`。普通名称不变，原始账号、分组和汇总数据不改写；API key 或用户名称不进入排行响应。

未送达且未按 abuse 计费的请求不输出 cache_trace，但仍关闭已记录的发送计时，避免影响同一会话下一请求的 gap_src；不向前缀历史发布结果。所有未送达结算分支均遵守观测会话数上限。

未送达分类与计费使用同一次判定结果：RecordUsage 先执行既有 DisconnectOn 与断连检查，再把正常、未计费或 abuse 分类交给观测结算。观测不重复读取运行时开关，也不改变是否计费的决定。

观测保留计费解析之前的上游用量快照。若凭据或定价错误使请求未到达计费判定，关闭发送计时但不输出事件或训练前缀历史。

请求的字节预算准入使用原子快照与原子预留，不等待 worker 队列锁；运行时调小预算仍允许已有预留排空。

策略存取期间不持有快照发布锁；事务返回乱序时只发布较新 revision，刷新期间若本实例已有新发布则丢弃这次可能过期的读取。仓储不支持事务写入时返回存储错误。

cache_trace 在送达分类确定后、真正扣费及用量落库之前输出，与既有 openai_cache_usage 一样不代表账单提交成功；之后扣费或落库失败不会撤回已输出的观测事件。

流式观测在既有逐帧只读解析路径上运行，没有 buffered 观测回调的 panic 隔离；结算入口的 recover 只保护结算，不覆盖流式逐帧解析。

路由归因反映请求中观察到的最高优先级决策，不保证最终选中的账号来自该决策：无会话也可记 sticky_new，但不会创建粘性绑定；failover 观察点早于 OAuth 429 的停止检查，因此被停止的切换也会计数；放弃或等待失败的候选仍参与最高级归因。多次故障改绑以快照前最后一次 rebind_cause 为准，快照后的改绑不进入事件。M1 只输出九个已接入原因及 unknown；capacity_shed、hard_clear、iq_bypass、quality_excluded、non_fault_rebind、affinity_pref 均为保留值，不单独输出，请求内因容量降载换号归 failover_n，其他行为按实际经过的既有九个决策点归因。

## OpenAI UUID 会话键补全（L-01）

注入只接受服务端确认已按租户隔离的路由种子；复合组或其他入口仍使用旧内容种子时，
L-01 记 `no_routing_session` 并跳过，不进入实验分桶。既有本地 sticky 与兼容转发不变；
关闭策略在读取种子和作用域来源之前早退。这里的 skip 是内部决策；复合组仍被既有 trace 平台闸门排除，
不会因此新增客户端可见字段或 cache_trace 事件。

管理员在缓存策略的 `upstream_identity` 中设置 `mode=uuid_fill`、
`rollout_percent` 和可选的 `account_ids`。默认 `mode=off`、比例 0、账号列表为空、
`chat_compat_uuid=false`、`max_inflight_per_key=2`（可设 1–16）。它受 `cache_feature_master` 和静态紧急总闸共同控制；
观测开关独立。配置按既有 revision、预校验、确认和审计流程更新，快照每 30 秒刷新；API 的
`refresh_interval_s=60` 表示 60 秒内生效的上限；
请求内沿用同一快照。启用、扩大比例或账号范围需要理由和勾选确认，撤回不需要。
账号列表只接受存在的 OpenAI apikey 账号，空列表表示所有合格账号。

管理接口的 `account_ids.options` 返回最多 500 个合格账号，按 ID 升序，元素只含 `id`、`name`、`platform`；无候选时返回空数组。候选排除 Codex、透传、Grok 等不合格协议，停用或暂不可调度的合格账号仍可用于配置。候选不受总开关、当前比例或已保存账号列表过滤；超过候选上限的已保存账号 ID 仍保留。候选查询仅用于管理员响应，不进入转发、选号或请求快照路径。查询失败（含分页中途失败）时，策略照常返回，丢弃部分候选并给出 `options: []`，在 `effective.warnings` 末尾追加 `account_options_unavailable`；不返回底层错误文本。关闭方向的预校验与 PUT 不访问账号表，原有格式、revision 与确认规则仍有效；PUT 同样给出候选未加载提示，需要候选时另发 GET。validate 的 warnings 只反映生效状态，不带候选提示。非法账号校验不返回生效状态警告；有效配置比例为 0 时仍显示 `no_effect_rollout_zero`。

功能仅覆盖 Responses HTTP 与可选的 Chat→Responses。客户端显式给出
`prompt_cache_key`（包括空串、null 或其他类型），或任一已有会话头时，
L-01 都不覆盖客户端设置。Codex/OAuth、透传、Grok、探测、原始 Chat、Messages
和 WebSocket 不参与。没有网关路由会话的请求也不参与。
`header_only_uuid=true` 与 `forward_session_headers=normalize` 属于后续功能，
本版本的写入返回 `CACHE_POLICY_UNSUPPORTED`。

Responses 为缺键请求补上稳定 UUID，并在最终出站没有 `session_id` 时使用同值。
UUID 的有效熵为 64-bit，按网关已有的 API key、serving 分组及模型作用域派生；不使用计费源分组。
同一请求首次确定分桶后不因账号或 host 切换重新分桶；同一服务作用域的重建与
重试使用相同 UUID，服务作用域变化时身份随之变化。生成的键不反馈到选号、sticky
或原有 lineage 身份。关闭或对照组保持原出站行为，不新增整请求解析。

Chat 的 `chat_compat_uuid=true` 只把已有兼容种子实际注入的出站 pck 改成原有
session header 的 UUID；U_chat 只按 API key 作用域，serving 分组回退前后保持相同。
内部种子及 header 派生不变，不重复隔离或二次 UUID 化。
不产生旧兼容种子的请求保持原行为，记 `l01_skip=no_compat_seed`；原始 Chat 等
排除入口记 `unsupported_ingress`。本请求实际触发既有 quality enforce 身份轮换时，
L-01 让位并记 `quality_rotation`，不会推进轮换状态；只配置轮换而未触发的请求
仍按通常规则处理。三个原因都记 `l01_applied=false`，出站与 L-01 关闭时相同。
跳过原因先判策略和入口，再判账号及会话分桶；处理组内依次判客户端 pck、
会话头、旧 compat seed、Chat 开关，再判实际质量轮换，最后按顺序检查 `upstream_rejects` 和 `fanin_cap`。

完整内部 cache_trace 增加 `l01_arm`（none/on/off）、`l01_applied`、
`l01_skip` 和 `l01_policy_rev`。实际注入来源为 `pck_class_out=gw_uuid`，同值
session header 来源为 `sess_out_relation=eq_pck`；最小事件仍保留原有 12 字段。
这些字段只记录枚举、布尔和 revision，不输出生成的 UUID、会话种子或头原文。
撤回时设置 `mode=off` 或比例 0；在途请求保持旧快照，新请求按刷新后的策略执行。

L-01 灰度使用独立域 `SHA256("l01-cohort|" + session)`，再按首字节映射到 0–99；
观测采样的原哈希保持不变，避免小比例观测只包含实验组。

仅当本次尝试实际注入 UUID 时，在客户端写回前将 Responses 中现有的
`prompt_cache_key` 原值原位替换为 `null`，保留字段及成员顺序；普通 JSON 和
单行 SSE 保留其他字节，多行 data 仍按完整事件合并为一行。还原覆盖普通 JSON、
流式生命周期事件（含 EOF）、SSE→JSON 及 compact。
本来为 null 的值保持原样，原本省略的字段不新增，输出内容/工具参数不改。
实际注入标记与观测开关独立并限定当前账号；关闭或跳过 L-01 时不运行响应还原，
客户端显式提供的 key 保持原样。UUID 仅用于上游身份，不进入客户端输出。
null 形态依据保存的兼容 Responses 官方流示例及协议允许的空值，不代表已对每个
上游实测；某上游若对无 key 请求省略该字段，两组仍可能呈现不同形态，需要在
启用实验中监测。


## Claude/Gemini 身份与粘性策略管理（T4.0）

anthropic_identity 默认 anchor_group_ids=[]、metadata_group_ids=[]、
gemini_anchor=false、rollout_percent=0；metadata 范围必须是 anchor 范围的子集。
两组 ID 只接受存在的 anthropic 分组，空列表表示关闭。
routing 默认 keep_sticky_on_busy.enabled=false、group_ids=[]、rollout_percent=0，
affinity_mode=score。busy 的空分组列表表示全部；本构建只接受 score，
其他取值在 validate 中为 unsupported_in_build、PUT 为 CACHE_POLICY_UNSUPPORTED。

策略管理与请求行为使用独立的静态构建能力。T4.0 的 L-04、L-03、L-07
能力均为 false；能力字段的启用或扩张在 validate 中为 unsupported_in_build，
PUT 返回 400 CACHE_POLICY_UNSUPPORTED，不保存待未来版本自动激活的配置。
关闭、收窄及整体回到默认值仍可保存且不查询分组表；rollout 不是能力字段，
单独增加仍按既有方向与确认规则处理。字段 schema 标记 not_implemented_in_build。
数据库只激活未实现行为时，effective 为 blocked、active=false，带同名 blocker；
过渡构建中已实现行为照常决定状态。L-03 依赖 L-04，生产构造不声明这三项能力。
后续行为实现必须显式声明各自的构建能力，请求 accessor 也必须与能力位做 AND。

嵌套对象整体或部分提交时按点路径的实际变化生成 diff、direction 与审计。
anthropic_identity 为 high，开启和 mixed 要求理由及勾选；
routing 为 highest，还要精确输入 cache_routing_policy。
关闭方向不要求确认、不查分组表，也不复用之前查询的回退事实；
格式、子集和 revision 校验仍然执行。

三个分组选择字段都给出按 ID 升序、最多 500 项的 options，只有 id/name/platform。
读取候选失败时仍可读取或保存策略，所有相关字段的 options 同时为空，
effective.warnings 最后追加 group_options_unavailable，不返回内部错误。
关闭写入主动跳过候选查询，使用同一提示；validate 不带候选提示。
northstar.excluded_group_ids 保持原形态，不添加 options。

anthropic_observability_missing 由静态构建能力决定，只在配置状态为 on
或 masked_by_master 时出现；off（包括 rollout=0）不出现。
构建是否包含 Anthropic 观测由构造参数声明，不在请求中探测。
选中的组若被任一组的 availability_fallback_group_id 引用，管理员响应提示
fallback_group_selected；它不进入用户输出。两个提示共存时，missing 在前。
T4.0 只提供上述管理与存储能力；实际请求、分桶、metadata 改写、sticky 与调度接线
由后续任务交付，当前版本不会因这两个键改变出站请求或选号。

### M3.1：启用前安全边界

种子的租户作用域由 `scopedOpenAISessionSeed` 显式返回。客户端提供的
`openai-routing-v2:` 文本不能作为已隔离来源；复合组、无 API key 等入口仍跳过注入。
只有 L-01 开启时才记录、消费服务端内部的来源布尔值；prepare 在 policy_off 早退后，
按原 no_routing_session 顺序检查它。routing hash、legacy sticky key 与 lineage 的派生
保持原实现，包括关闭态下伪造前缀的既有 hash 字节；客户端文本不会赋予注入资格。

当且仅当实际注入的请求收到 400，且 `error.param` 或 message 指向
`prompt_cache_key` 时，本进程对该账号记忆 24 小时，后续记 `upstream_rejects`。
首次 400 不新增重试、不切账号、不调用 penalty、session_rebind 或冷却；客户端自带
key 的拒绝保持原有行为，不会标记账号。拒绝记忆有界，容量耗尽时保守停止注入，
不会通过驱逐仍有效记录恢复注入。
拒绝响应先标记已提交，再沿用 Responses 或 Chat 的基线确定性 400 writer；
不含 U 时保留基线错误格式，不依赖上游 Content-Type，也不追加 handler 兜底错误。

进程内按 RoutingSessionID 记录实际参与 L-01 的在途请求，表最多 65536 项；
达到 `max_inflight_per_key`、表满或计数异常时记 `fanin_cap` 并使用原出站 body/header。
错误、取消和原有重试结束均释放预留；内部原有 Responses 重试复用同一预留。
增大上限属于 enable，减小属于 disable。旧存储在内存中补默认 2，不写回、不改 revision；
validate 和 PUT 使用同一补齐基础，history 保留 `max_inflight_per_key` 的变更。
`mode=uuid_fill` 且账号列表为空时，提示 `l01_all_accounts_unverified`，与 master、rollout 无关。

还原只运行于实际注入的尝试。结构化还原失败时，把已知的 `"<U>"` 替换为 `null`；
仅完整、未转义的字符串值替换为 null；U 作为对象键、嵌在消息字符串中，或分片
上下文不明确时只删除这 36 字节，保留引号和 JSON 合法性。还原缓冲的 SSE 事件超过上限时改用
有界字面替换流式输出，跨分片保留足够尾部以防 U 跨客户端写入边界泄漏。还原自身
永不返回错误，不影响既有失败归因。既有 scanner 单物理行上限和无效 usage JSON
解析错误仍执行原来的韧性与计费校验，不用这项兜底绕过。

所有透传的上游错误体均执行同一隐私处理；状态、其它头和剩余字节保持，变化后
重算 Content-Length。普通没有 U 的错误体逐字节保持。`l01_restore_fallback` 仅在
实际生效时输出 `none`、`literal` 或 `sse_oversize`，后者优先；未生效时不输出该字段。
通过既有 resilience 指标接口提供 `l01_restore_fallback_total{kind}`（含 error_body）
和 `l01_upstream_reject_total`；首次拒绝另记不含 UUID、prompt 的告警日志。
实验应监测 fanin_cap 比例及两组 429 率差，差值超过 0.5pp 时人工停止扩量；不自动翻转。


#### M3.1 转换出口隐私补修

Chat Completions 经 Responses 上游的成功 HTTP 响应，在实际 L-01 注入标记为真时，
先通过同一有界 SSE 还原缓冲再进入转换器。这覆盖 buffered JSON、累积内容补齐、
转换流式 chunk、response.failed 与 cyber 错误的全部客户端出口；既有用量解析与错误
判定继续执行。分片、EOF 和超大事件沿用相同的结构还原与字面兜底，不新增重试。
Responses 原生流式的规则错误直接写客户端，独立清除已解码 message 中的已知 U。
未注入或策略关闭时使用原 reader/writer，不分配还原对象。Messages 兼容入站不在
L-01 允许的 ingress 中，维持原有身份与转换逻辑，不扩大注入范围。

### 多平台观测的 stats 口径（M2b）

平台扩展由 observability 的 platforms 控制，默认仍只有 openai。stats 的分钟桶、小时桶、host EWMA 与会话配对按平台隔离，各自沿用容量上限；Messages 桥属于 openai 分区。all 只汇总当前覆盖的平台，先合并计数再折叠小样本。同名 host 按统一时刻衰减后合并；PCK、会话头、UUID 比例与类别完整率只使用选中的 OpenAI 分区。

单独查询已关闭平台仍可读取窗口内历史，带 platform_not_covered；all 排除该平台。provider 视图的 OpenAI 专属数组为空、比率为 null，并带 openai_only_metrics。仅启用 Messages 桥时，OpenAI 视图带 openai_native_excluded；未启用桥时继续标注 messages_bridge_excluded。默认配置的 openai、all 查询和小时行保持 M2 口径，不更改迁移或计费。

### M2b Messages 桥观测接线（开发中）

Messages 桥由运行时观测策略中的 messages_bridge 独立选择，默认 platforms 仍只含 openai。仅经 Responses 出站的桥进入 OpenAI 事件和统计分区；经原生 Anthropic 或 raw Chat 出站的子路径不在本期覆盖内。发送标记及最终请求头观察位于实际发送边界，流式用量观察复用现有解析，账单数值与原规则保持一致。关闭平台、总闸或诊断开关时不启动该平台的异步观测；每请求继续固定策略快照。当前接线待验证，不能据此开启生产。


## T2.2：Claude/Gemini 本地锚点（L-04）

本构建声明 L-04=true、L-03=false、L-07=false。请求 accessor 同时检查能力、
行为快照过期保护、总闸、master 与键的有效性；默认名单为空、Gemini 关闭、rollout=0，
因此默认沿用原有请求、会话 hash 和选号。过期保护在独立维护功能合入前使用返回 false 的集成接缝。

anthropic_identity 的 schema 末尾追加 metadata_rollout_percent（0..100，默认0）和
max_inflight_per_anchor（1..16，默认2），两个字段均记录 history；调高为 enable。
metadata_rollout_percent 不得超过请求后的 rollout_percent，错误码为 exceeds_rollout_percent，
limit.max 为请求后的 rollout_percent；无效字段不参与 diff。旧存储只在内存中补默认，
不写回、不改 rev。metadata 灰度不是能力字段，可以先存储，但 L-03 尚未实现，不生效。

服务端使用认证 API key ID、有效 serving 组 ID、入站模型族、规范化 system 和首条非空
user 文本生成 a2。HMAC-SHA256 使用 JWT secret，消息以 cache-anchor-v2 加 NUL 开头，
五字段各用8字节大端长度前缀，截取前16字节小写 hex，带 a2: 前缀。忽略图片、工具、
thinking 和整块 system-reminder，只压缩文本中的 Unicode 空白；不截断输入、不使用
未隔离的旧 session 或观测 digest。两文本均空、缺 key/组/secret、平台不匹配均不派生。

share active 保持既有路径；metadata、cacheable_content 与 Gemini CLI 等既有前档优先。
其余候选按同一 anth-cohort|a2 哈希的嵌套阈值分桶，本期只会产生 l04_only/off。
原有 sticky 之外的出站 body/header、metadata 和重试规则不变。候选携带 serving 作用域，
使用时不匹配则回到基线；中间件换组、prompt-too-long 和 Claude Code 限制回退均重新判定。
count_tokens 不进入派生或计数。Gemini 只在启用路径补 serving GroupID，保留 CLI 优先级。

按 a2 维护进程内最多65536项在途计数，归零删除；超限、容量满或异常时回到基线，
记 fanin_cap 并增加 l04_fanin_skip_total。请求结束和换组释放预留；多实例上限近似为
单实例上限乘实例数。fanin_cap 超过候选的5%时人工缩小名单，不自动改变策略。

完整 Anthropic/Gemini trace 在支持 L-04 的构建增加 l04_arm、l04_applied、l04_skip、
l04_policy_rev，实际使用时 id_source=anchor_v2；OpenAI 与最小事件不变。
观测开关不改变锚点、分桶或任何 L-04 决策；session_digest 仅由 T0b.4 对实际 hash 做隔离摘要。

JWT secret 以 DB security_secrets.jwt_secret 为准。轮换将同时改变 OpenAI sticky、L-01 U、
trace 锚点和 a2，导致一次全站冷启动；旧 Redis 键按 TTL 回收，期间约双倍内存。
轮换安排在低峰期，在实验记录标明断点，之后24小时观察命中回升与 Redis 内存；
实验按断点分段，不跨密钥周期合并。优先选择已启用 claude_cache_fallback 或上游会补断点的组。

锚点文本只取原生 Messages 或原生 Gemini 的入站解析结果，由 handler 入口显式声明。
Responses 与 Chat 兼容入口均不把 instructions/input/role=system 映射成锚点输入，
即使携带同形 messages 也视为空锚点，记 empty_anchor，保留原有 hash 与出站字节。
兼容入口的文本映射留待独立流量评估后决定，不由请求 body 自称原生来源。
