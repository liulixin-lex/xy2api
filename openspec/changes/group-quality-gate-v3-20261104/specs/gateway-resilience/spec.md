## ADDED Requirements

### Requirement: 尝试级可见首字持久化
系统 SHALL 在 `usage_logs.attempt_first_output_ms` 中记录最终尝试开始至首个语义输出的毫秒数。语义输出判定 SHALL 与 `end_to_end_first_output_ms` 相同，排除错误帧、`[DONE]`、纯终态、开场与心跳，保留终态携带的真实内容。系统 SHALL 通过 OpenAI `RecordUsage` 与通用 `buildRecordUsageLog` 写入，并在单条、批量、尽力批量和无结果插入路径及读取路径中保留该值。

#### Scenario: 换号后从最终尝试起算
- **WHEN** 请求在排队与一次失败尝试后换号，最终尝试成功产生语义输出
- **THEN** `AttemptFirstOutputMs()` SHALL 返回 `SemanticOutputAt − AttemptStart`，不含排队、选号和此前失败尝试的时间
- **AND** 原有 `first_token_ms` 与 `end_to_end_first_output_ms` 的口径 SHALL 保持不变

#### Scenario: 无法计算时保留缺失
- **WHEN** 缺少请求状态、最终尝试起点或语义输出，或语义输出早于当前尝试
- **THEN** `UsageLog.AttemptFirstOutputMs` SHALL 为 nil，数据库 SHALL 写入 NULL
- **AND** 可计算的 0 毫秒 SHALL 保留为 0

#### Scenario: 兼容迁移与回退
- **WHEN** 执行迁移 `289_usage_log_attempt_first_output.sql`
- **THEN** 系统 SHALL 幂等追加可空 BIGINT 列，不设默认值、不加索引、不回填历史记录
- **AND** 旧版本 SHALL 可忽略并保留该列
- **AND** 本列 SHALL 不加入运维错误日志、用户端接口或管理端接口

### Requirement: P1-B 输出后首次失败改绑

系统 SHALL 在 `session.post_output_rebind` 开启时，仅为最终上游账号平台为 OpenAI（`PlatformOpenAI`）且不带下述密文片段的请求，在输出后的流内失败事件、读流出错与缺终态传递专用会话失败信号，首次即执行现有连击改绑动作：按属主删除绑定、加入会话避开、标记会话和本请求的避开集合。系统 SHALL 沿用 `ReportStreamFailure` 的 attempt 标识去重，HTTP 请求内同一失败只立即改绑一次，WebSocket 连接按 turn 计；避开时长 SHALL 沿用 `session.avoid_seconds`（缺省 600 秒）。

#### Scenario: 输出后信号与输出前失败隔离

- **WHEN** 不带上述密文片段的 OpenAI 请求在输出后首次发生上述三种已确认失败之一
- **THEN** 系统 SHALL 立即执行改绑，记一条 Warn `resilience.session_rebind`，原因与会话改绑计数标签均为 `post_output_first_failure`
- **AND** 同请求的重复信号 SHALL 不重复改绑
- **AND** 输出前失败 SHALL 仍按原有连击阈值（缺省 2）处理

#### Scenario: 带密文片段的请求退回连击规则

- **WHEN** 请求 `input[]` 中 `agent_message.content[]` 或数组形态 `function_call_output.output[]` 含 JSON 解码后 `type=encrypted_content` 的片段，且发生真实输出后失败
- **THEN** 系统 SHALL 发送普通会话失败信号并沿用连击阈值（缺省 2），首次不立即改绑或会话避开，达到阈值才改绑
- **AND** 账号输出后冷却计数、同站暂停记账及其他失败类改绑 SHALL 保持生效
- **AND** HTTP SHALL 读取请求标记；WS 原生池、透传和 HTTP 桥 SHALL 按每个 turn 的 payload 更新输出后路径实际读取的上下文，前一轮标记不得遗留
- **AND** 带密文的 turn 之后，不带密文的 turn SHALL 在首次符合条件的输出后失败时触发 P1-B

#### Scenario: 会话状态族拒绝不触发输出后三项动作

- **WHEN** HTTP SSE 或 WebSocket 在输出后收到 S1（中转会话状态）、S2（会话密文无效）、S3（加密函数输出无法解密）、S4（加密内容无法校验或 hydration failed）或其他加密内容拒绝
- **THEN** 无论是否带 `invalid_request_error` 包装，无论是裸 `error`、`response.failed` 或二者先后出现，系统 SHALL 保留原绑定，不触发 P1-B 或会话避开，不计账号输出后冷却，也不增加会话连击
- **AND** 请求最终失败、运维记录与真实用量 SHALL 照常保留，输出前分类与换号规则 SHALL 保持不变
- **AND** 系统 SHALL 依次匹配专用 code `client_tool_continuation_unavailable` / `invalid_encrypted_content` / `encrypted_context_invalid`、现有 `IsEncryptedContentRejection`；仅 code 为空时 SHALL 用 `opaque conversation state` / `unrecognized conversation state` / `encrypted content is invalid for this conversation` 补判，不得仅凭 `for item amsg_` 或 `invalid for this conversation` 排除
- **AND** 门槛 v3 错误维、近期表现与份额健康 SHALL 仍按运维分类计数；无包装 S1 的原分类为 `TRANSIENT`、计入，生产中已核对的带 `invalid_request_error` 包装 S1 为 `CLIENT_ERROR`、不计入
- **AND** 读流出错与缺终态等真实断流 SHALL 继续执行输出后保护；只有请求带密文片段时 P1-B 退回上述连击规则

#### Scenario: 非 OpenAI 账号保持原连击规则

- **WHEN** Anthropic、Grok 或其他非 OpenAI 平台的账号发生输出后失败
- **THEN** 系统 SHALL 不发送 P1-B 专用信号，首次失败 SHALL 不立即改绑
- **AND** 失败记录与原有会话连击 SHALL 保持生效，缺省连续 2 次失败才改绑

#### Scenario: 同一 WebSocket 连接的多个 turn

- **WHEN** 同一 WebSocket 连接内两个不带上述密文片段的 turn 分别发生输出后失败
- **THEN** 每个 turn SHALL 各执行一次 P1-B 改绑
- **AND** 同一 turn 内的重复失败信号 SHALL 不重复动作

#### Scenario: 开关关闭

- **WHEN** `session.post_output_rebind` 关闭
- **THEN** 输出后失败 SHALL 回到原有连击规则
- **AND** `session.enabled` 或总开关关闭时 P1-B SHALL 一并失效

#### Scenario: 绑定属主与质量路由

- **WHEN** 绑定已换为另一属主，或绑定由质量路由持有
- **THEN** 系统 SHALL 沿用原有删除权限，不越权删除绑定
- **AND** 会话避开 SHALL 继续生效，有其他正常候选时下一请求不选出错号
- **AND** 没有其他正常候选时 SHALL 保留现有末位兜底

### Requirement: 同站传输层断流暂停

系统 SHALL 以 OpenAI 账号 `GetOpenAIBaseURL()` 的主机名为站点键，未配置时使用官方站点。只计 `resilienceStreamBrokenAfterOutput` 确认的输出后读流出错与缺终态，不计中转流内失败、输出前失败、客户端取消或非 OpenAI 账号。同号 3 秒内 SHALL 合并一次；当同站至少 2 个账号在 60 秒内分别断流时，只对这些断流账号中未暂停的各暂停 300 秒。

#### Scenario: 同站部分账号断流

- **WHEN** 同站两个账号在窗口内分别发生符合条件的断流
- **THEN** 两个账号 SHALL 暂停，同站没有断流的账号 SHALL 不暂停
- **AND** 每个账号每次暂停 SHALL 记一条 Warn `resilience.same_site_hold`，字段为 `host_hash`、`account_id`、`peer_account_id`、`until`
- **AND** `host_hash` SHALL 使用主机名 SHA-256 的前 16 个十六进制字符，不记录明文主机名

#### Scenario: 暂停期间再次断流

- **WHEN** 暂停期间再次断流，或同站又有另一账号断流
- **THEN** 已有暂停 SHALL 不延长、不重新计时；新满足条件且未暂停的账号 SHALL 单独取得 300 秒暂停

#### Scenario: 普通选号、绑定与末位兜底

- **WHEN** 账号处于同站暂停
- **THEN** 系统 SHALL 按运行时冷却语义跳过普通选号，绑定会话在下一请求改绑
- **AND** 系统 SHALL 将该暂停映射为软惩罚，组内无其他可用号时仍可末位兜底
- **AND** 同站暂停 SHALL 不保留原软罚属主的绑定；关闭同站开关后，普通瞬时软罚 SHALL 仍保留绑定
- **AND** 暂停到期 SHALL 不主动迁回，不改变门槛统计

#### Scenario: 关闭与重启

- **WHEN** `same_site_hold.enabled` 或总开关关闭
- **THEN** 系统 SHALL 不新建暂停，已有暂停在判断与软惩罚映射时立即失效
- **AND** 重开 SHALL 从新样本开始，旧暂停 SHALL 不复活
- **AND** 进程重启 SHALL 清空跟踪器，各实例独立
