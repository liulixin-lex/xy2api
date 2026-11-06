## ADDED Requirements

### Requirement: Responses 空白占位不算输出
系统 SHALL 在原生与直通 Responses 中，对 `response.output_text.delta` / `.done`、output_text 的 `response.content_part.added` / `.done`、assistant message 的 `response.output_item.added` / `.done` 逐事件按自身载荷判定：文本经 Go `strings.TrimSpace` 后为空时，该事件 SHALL 暂存，不提交响应、不停止首输出截止、不计首 token 或语义首输出。空 delta 要求 `delta` 字段存在且为字符串，零长度与纯空白均适用。refusal、非文本 part、未知载荷、畸形载荷，以及参与判定的对象出现重复键或已提供字段类型错误的事件，SHALL 保守算作输出；已知失败终态与包装错误帧仍由前置失败分支处理。白名单 SHALL 接受上游实际携带的 `logprobs: []`、delta 的字符串 `obfuscation`、message item 的字符串 `phase`。delivery 开场分类仍依赖缺省开启的 `stream.native_prelude_whitelist`；兼容转换还依赖 `stream.prelude_staging`。

#### Scenario: 首个可见事件或成功终态冲刷暂存
- **WHEN** 输出前已暂存 created 与空白占位事件，随后到达首个可见事件或通过静默拒绝守卫的 `response.completed` / `response.done`
- **THEN** 系统 SHALL 先按原字节、原顺序冲刷全部暂存，再发送该事件
- **AND** 客户端收到的字节 SHALL 与不暂存时既有输出路径的协议变换结果相同

#### Scenario: 输出前失败丢弃暂存
- **WHEN** 输出前只有开场与空白占位，随后收到 `response.failed`、`error`、失败的 `response.incomplete`，或发生断流、首输出超时
- **THEN** 系统 SHALL 丢弃全部未提交帧（含 created），按输出前失败继续换号、修复或终止
- **AND** 失败终态 SHALL NOT 计首 token、`FirstOutput` 或 `SemanticOutputAt`
- **AND** `incomplete_details.reason=interrupted` SHALL 按成功终态处理

#### Scenario: 只有空白的 completed 不算语义输出
- **WHEN** 尚无非终态语义输出，`response.completed` / `response.done` 的 `response.output` 没有 items，无已累计用量，且顶层及 response 下的 usage/error 均缺失或为 null
- **THEN** 原生与直通 SHALL 沿用既有静默拒绝守卫换号，空白 SHALL NOT 算作已见语义输出
- **AND** 有 output 或 usage 的合法空白回答 SHALL 照常返回，但不进入首输出延迟样本，`attempt_first_output_ms` 为空

#### Scenario: 兼容转换保留空白
- **WHEN** Chat Completions 或 Messages 客户端经兼容转换接收 Responses 上游，上游先发前导空白再发正文，或在正文中间单独发一个空格 delta
- **THEN** 首次提交前，系统 SHALL 有界暂存解码后的开场与空白事件（累计上限 8 MiB），在首个可见事件或成功终态时按序转换并冲刷，失败时丢弃
- **AND** 提交后，空白 delta SHALL 照常转换，客户端收到的文本 SHALL 与上游完全相同

### Requirement: Responses 输出前心跳按客户端选择
系统 SHALL 在既有心跳启用时，于原生、直通及请求级 preOutput 的 Responses 输出前心跳中，对按既有 originator / User-Agent 判定为 Codex 的请求写完整的 `event: ping\ndata: {"type":"ping"}\n\n`，对其他客户端写 SSE 注释心跳。心跳 SHALL 扣除输出字节、不计首输出，并在输出开始、客户端断开或失败终态发出后停拍。直通 SHALL 由固定周期 ticker 每拍派发一次网关心跳，不按相邻 tick 时间差二次节流；上游注释行即时透传，不重置网关心跳周期。

#### Scenario: Codex 等待期间按周期发送 ping
- **WHEN** Codex 客户端处于空白暂存或内部换号等待，既有心跳已启用、连接可写且未触发预算或取消条件，期间上游只发注释
- **THEN** 网关 SHALL 按心跳周期写出完整 ping 事件，供客户端重置空闲计时；实际接收间隔仍受调度、写入与网络延迟影响

#### Scenario: 其他 SDK 的首事件约束
- **WHEN** 非 Codex 客户端（例如要求首事件为 `response.created` 的 SDK）处于同样的输出前等待
- **THEN** 系统 SHALL 只写注释心跳，不提前写出 ping 事件或 created

### Requirement: 流内包装错误帧的校验与路由
系统 SHALL 只在以下两种形状通过校验后，把 Responses 流内帧视为中转包装的上游错误：E1 为 SSE `event: error`，data 的 `type=proxy.upstream_error`，`status` 为 JSON 整数；E2 为 data `type=response.failed`、`response.error.type=upstream_error`，其中 `response.error.code` 为仅含字符 0–9 的非空字符串，与 JSON 整数 `response.error.status_code` 数值相等（code 可有前导零）。状态码 SHALL 在 400–599 之间。`status` / `status_code` SHALL NOT 接受字符串、小数或指数形式；系统 SHALL NOT 从 message 或解包后的内体重新推测包装状态。E1 的识别 SHALL 使用原始 SSE `event` 值；终态判断仍以 payload 的 `type` 为准。

#### Scenario: 校验通过的包装帧按迟到的 HTTP 状态路由
- **WHEN** 输出前收到校验通过的包装帧
- **THEN** 包装 400 SHALL 保留已校验的 `WrappedStatus=400` 与原始外层体，交由既有有界修复及 M3（适用时）处理，聚合出口同样适用
- **AND** 包装 401/403 SHALL 按“包装 401/403 的请求级终止与 forbidden 处理”需求处理
- **AND** 其他状态 SHALL 复用 HTTP 的换号判定；`upstream_body` 是有效 JSON 值或可解析的 JSON 字符串时，后续分类 SHALL 使用解包体，保留内层容量码、余额码等语义，否则沿用外层体

#### Scenario: 畸形包装帧
- **WHEN** 输出前收到看起来是包装帧，但类型、状态、相等关系或范围校验失败的帧
- **THEN** 系统 SHALL 按通用协议失败处理，并记一条 `resilience.wrapped_error_malformed`（含 `account_id`、`path`，不含正文）
- **AND** SHALL NOT 用该帧的状态、code 或 message 触发请求修复、M3、认证或 403 账号副作用，也不触发错误透传规则

#### Scenario: 输出后的包装失败帧
- **WHEN** 已有真实输出后收到原始 SSE event 为 `error`、载荷为 `response.failed` 的帧
- **THEN** 系统 SHALL 完整投递该失败终态并立即结束，不等待另一帧或 EOF
- **AND** 已校验的包装 401/403 SHALL NOT 进入凭证、停用或 HTTP 403 计数处罚
- **AND** 此时 SHALL NOT 再换号或记录 forbidden 软冷却

### Requirement: 包装 401/403 的请求级终止与 forbidden 处理
系统 SHALL 在输出前对校验通过的包装 401/403 先用解包后的错误体判断状态类拒绝、上下文超限、cyber 与内容安全。命中时按请求级终止：不换号、不冷却、不罚号、不上报调度失败、不归为 AUTH。其余包装 401/403（含停用码、凭证码）SHALL 按输出前流内 forbidden 处理。

#### Scenario: 请求级拒绝的终止码
- **WHEN** 包装 401/403 在输出前命中请求级拒绝
- **THEN** 未提交时 SHALL 写 HTTP 400；只发过心跳时 SHALL 写 `response.failed`
- **AND** code SHALL 为：上下文超限 `context_length_exceeded`，cyber `cyber_policy`，内容安全与状态类拒绝 `invalid_prompt`
- **AND** message SHALL 取内层错误消息并经过现有脱敏与重试建议清理
- **AND** SHALL NOT 带 `Retry-After` 或重试建议，也不进入通用 401/403→502 映射或错误透传规则

### Requirement: M3 状态类拒绝的同号修复与一次换号
系统 SHALL 在 OpenAI 账号的原生与直通 Responses 的 HTTP、输出前流内及非流式聚合出口，对输出前的 S2、S3、S4 与校验通过的包装 400，执行 M3：在既有构造器之前，把 `agent_message.content[]` 与 `function_call_output.output[]` 中目标密文槽位整段换成 `{"type":"input_text","text":"[encrypted content omitted]"}`，保留外层 item、id、call_id、数组顺序与明文；只有本次已替换的槽位集合比上一次严格变大且预算允许时，才同号重放。既有字段、compact 与 web_search 修复入口的优先顺序 SHALL 保留。命中既有 DeepSeek/Kimi 请求归一化判定的账号 SHALL NOT 启用 M3；需要派生时遇到形状歧义 SHALL 终止派生，不发送其他形状。后续请求 SHALL 可依据本号已有 Bad 派生，不对未命中当前拒绝或本号 Bad 的槽位主动替换。

#### Scenario: S4 逐项学习
- **WHEN** S4 点名的 item id 能在出站体中唯一定位
- **THEN** 系统 SHALL 只选择该 item 尚未替换的密文槽位，把符合引用约束的槽位立即写入 Bad；有新增槽位且预算允许时再替换并同号重放，学习不等待重放成功
- **AND** 没有新增槽位时 SHALL 按无增长处理，不凭已有 Bad 再重放
- **AND** 无法唯一定位时 SHALL NOT 学习，直接进入换号步骤

#### Scenario: S2/S3/包装 400 只在上游接受后写 Bad
- **WHEN** S2、S3 或包装 400 触发整批替换并重放
- **THEN** 只有本次重放收到 `response.completed` / `response.done` 成功终态、完整成功的非流式 Response（有效 JSON 对象、`object=response`、`status=completed`、output 为数组且 error 缺失或 null），或在真实语义输出之后收到 `response.incomplete` / `response.cancelled` / `response.canceled` / `[DONE]` / 干净 EOF 时，才 SHALL 把这批符合引用约束的槽位写入 Bad
- **AND** failed、error、读取异常，以及没有语义输出的 incomplete / cancelled / canceled / `[DONE]` / EOF SHALL NOT 写入；`[DONE]` 本身不新增接受证据

#### Scenario: Bad 的范围与内存上界
- **WHEN** 槽位写入 Bad
- **THEN** 条目 SHALL 按组、进场会话与账号隔离，存于进程内 lineage 存储，TTL 沿用粘性 TTL
- **AND** kind SHALL 存为常量，引用 SHALL 独立拷贝且不超过 256 字节；无引用或超长引用 SHALL 只在当前请求替换，不写入 Bad

#### Scenario: 无可替换时换号一次后终止
- **WHEN** 当前号上没有可新增替换的槽位
- **THEN** 系统 SHALL 对该号做属主校验删绑、会话回避并写入本请求排除集，使用独立的状态换号额度（上限 1）换号，不排除同站、不计连败或调度健康失败
- **AND** 新号上仍无法修复，或额度、预算耗尽时，系统 SHALL 终止：未提交写 HTTP 502，已提交写 `response.failed` / `server_error`
- **AND** 客户端与 ops 消息 SHALL 只由固定模板、已识别的拒绝原因及唯一校验成功的 item id 生成，不含上游 message 或密文回显

#### Scenario: 改写后的其他 4xx
- **WHEN** 已替换槽位的请求在既有有界修复耗尽后收到 4xx
- **THEN** 只有未被既有分类判为请求级的 400 / 422 SHALL 视为派生形状可疑并换号
- **AND** 401 / 403、其他 4xx 及请求级拒绝 SHALL 沿各自既有路径处理，不因本兜底进入 M3；已校验包装 401/403 仍适用上述请求级终止或 forbidden 规则

### Requirement: 输出前流内 forbidden 作为瞬时类处理
系统 SHALL 对输出前、非停用非凭证类且未命中状态类拒绝、上下文超限、cyber 或内容安全的流内 forbidden，以及未命中请求级拒绝的包装 401/403，赋予 `OpenAIStreamForbiddenReason`（瞬时类，不同号重试）。仅 OpenAI API Key 账号在韧性失败处理开启（`FailureOn()`）且有结构化证据时 SHALL 记录账号×模型软冷却；证据为数字状态 403、校验通过的包装 401/403，或错误对象 `code=upstream_error` 且缺失 `type` 字段（null 或空字符串不算缺失）。这些 forbidden SHALL NOT 进入 HTTP 403 连续失败计数、停号或调度健康失败。仅 OpenAI API Key 账号在本请求内排除同站其他账号，其他账号只排除当前号；此路径不占容量换号额度。

#### Scenario: 用尽时的终止
- **WHEN** forbidden 导致候选用尽
- **THEN** 未触发回合封顶时，未提交 SHALL 写 HTTP 502，已提交 SHALL 写 `response.failed` / `server_error`；重试提示 SHALL 沿用既有错误处理开关与耗尽规则
- **AND** 满足既有错误处理、回合封顶开关及会话条件且达到连续失败上限时，SHALL 沿用通用终止策略（503 / `server_is_overloaded`、不建议重试）

#### Scenario: 仅文案推导的 forbidden
- **WHEN** forbidden 只能从文案推导
- **THEN** 系统 SHALL 照常换号，用尽时沿用上述终止规则，但 SHALL NOT 记录软冷却

### Requirement: 恢复路径的最小观测
系统 SHALL 以结构化日志记录以下事件，完整事件名均带 `resilience.` 前缀：`m3_learned`、`m3_replaced`、`m3_replay_ok`、`m3_state_switch`、`m3_terminal`、`committed_terminal`、`last_resort_avoided`、`blank_placeholder_staged`、`blank_placeholder_outcome`、`forbidden_cooldown` 与 `wrapped_error_malformed`。除既有追踪上下文外，业务字段 SHALL 限于组、账号、模型、路径、计数、冷却时长、原因与结果，SHALL NOT 记录密文、完整摘要或上游正文。

M3 学习、替换与重放成功事件 SHALL 含 `group_id`、`account_id`、`count`；换号事件含 `switch_count`，终止事件含 `cause`。`last_resort_avoided` SHALL 依据已加载的会话回避集合（含此前请求留下的回避）记录 `group_id`、`account_id`、`reason=session_avoid`。`forbidden_cooldown` SHALL 在满足冷却门控时含 `account_id`、`group_id`、`model`、`failure_streak`、`cooldown_ms`；首次 `cooldown_ms=0`，不代表已暂停账号。

#### Scenario: 占位请求的结局可关联
- **WHEN** 请求内曾实际暂存空白占位
- **THEN** 每次实际暂存过空白的尝试 SHALL 只记一条 `resilience.blank_placeholder_staged`（含 `account_id`）；handler 结束时 SHALL 记一条 `resilience.blank_placeholder_outcome`
- **AND** outcome 字段 SHALL 为 `placeholder_timeouts`（仅累计暂存过空白的尝试发生的首输出超时，普通静默尝试不计）、`final_outcome`（现有请求恢复结果）、`budget_cause`（未耗尽时为空）
