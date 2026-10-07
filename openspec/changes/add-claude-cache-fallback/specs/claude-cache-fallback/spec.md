## ADDED Requirements

### Requirement: Default-off scoped policy

系统 MUST 默认关闭缓存补齐，并只对匹配有效分组、可选 Key 名单、实际账号、规范化完整 base URL 和最终模型的原生 Messages/API Key 请求生效。

#### Scenario: Missing or disabled settings
- **WHEN** 策略不存在、关闭、为空或过期刷新失败
- **THEN** 请求沿用原有处理，不增加缓存声明或上游调用

#### Scenario: Scope mismatch or account failover
- **WHEN** Key 换组、账号/地址不匹配、映射后模型未列出，或原调度换到未验证账号
- **THEN** 不注入；上一次尝试的注入正文不得复用到新账号

#### Scenario: Unsupported route
- **WHEN** 请求走 OAuth、SetupToken、显式 passthrough、Vertex/Bedrock 专用路径、其他平台、Chat/Responses 转换或 count_tokens
- **THEN** 不执行本功能

### Requirement: Preserve supplied declarations

系统 MUST 在原始正文和最终正文的协议位置检测声明；新功能 SHALL NOT 覆盖、移动或删除客户端已有声明。

#### Scenario: Existing root or block declaration
- **WHEN** 根、system、tools 或 message content block 中任一协议位置已经存在 cache_control
- **THEN** 本功能不修改该请求，已有 TTL 和既有处理语义保持

#### Scenario: Invalid or removed declaration
- **WHEN** 原始声明为 null/非法类型、位于 thinking，或因既有断点上限被清理
- **THEN** 不将其当作缺失补齐，继续既有校验/净化行为

#### Scenario: Same text inside user data
- **WHEN** 只有 prompt 文本、tool input 或工具 JSON schema 内出现同名词或属性
- **THEN** 不将其识别为协议声明

#### Scenario: Group-added declaration
- **WHEN** 原始请求无声明，但最终分组提示词处理已加入声明
- **THEN** 不再追加顶层声明

### Requirement: Minimal idempotent wire change

满足所有前提时系统 MUST 只添加顶层 `{"type":"ephemeral"}` 缓存声明，使用默认 5m。

#### Scenario: Eligible request without declarations
- **WHEN** 白名单命中且原始/最终正文都没有声明
- **THEN** 最终 HTTP body 增加且仅增加该顶层字段，其他字段值与内容顺序保持

#### Scenario: Repeated construction
- **WHEN** 同一候选正文重复经过补齐函数
- **THEN** 不重复添加，结果幂等；原始共享正文保持未修改

#### Scenario: Large or malformed body
- **WHEN** 输入为 2.65 MB 合法合成正文，或无法可靠解析的非法正文
- **THEN** 前者只局部插入且满足既有大小限制；后者不猜测补齐，交原有校验处理

### Requirement: Upstream execution must be verified

系统 MUST 将 HTTP 接受、缓存实际读写和账单可核对视为不同条件，不得将协议兼容名称或单次 HTTP 200 当作完整验收。

#### Scenario: Cold write followed by warm read
- **WHEN** 对拟启用账号/模型用足够长的新前缀，在有效期内发送相同正文两次
- **THEN** 首次缓存写入大于零、第二次读取大于零，且 TTL 明细、返回用量和 XY2API 账单可核对

#### Scenario: HTTP success without cache usage
- **WHEN** 上游返回 200，但合格受控样本始终没有缓存读写
- **THEN** 不判为能力验证成功，不自动扩大启用范围

#### Scenario: Short or changing prompts
- **WHEN** 请求短于模型阈值、前缀变化、并发冷启动或缓存已过期
- **THEN** 允许真实零命中，不伪造缓存用量，不自动延长 TTL 或改变路由

### Requirement: No injection-triggered replay

补齐功能 MUST NOT 增加自动付费探测、预热或请求重放。

#### Scenario: Unsupported parameter or interrupted stream
- **WHEN** 上游拒绝缓存字段，或流式响应中断/客户端取消
- **THEN** 记录原因并进入既有错误/结算流程，不因本功能删字段重发，不违反输出后禁重放约束

### Requirement: Bill actual classified usage

系统 MUST 按上游返回用量和既有定价/倍率结算，不以注入成功推定缓存成功；总缓存写入和 TTL 细分 SHALL NOT 重复收费。

#### Scenario: Streamed and nonstreamed usage
- **WHEN** JSON 返回用量，或 SSE start/delta 返回重复/零值用量
- **THEN** 最终分类和费用正确，不被尾部零值清空或重复累加

#### Scenario: TTL missing or contradictory
- **WHEN** 上游缺少写入 TTL 明细、细分与总量矛盾，或默认 5m 探测实际报告 1h
- **THEN** 记录诊断，不按请求声明伪造明细，不将该样本标为计费验收成功

#### Scenario: Existing billing override
- **WHEN** 原有 ForceCacheBilling 或 TTL 计费覆写生效
- **THEN** 本功能不主动触发或绕过它们，诊断可区分原始 usage 与结算 usage

#### Scenario: Durable settlement and deletion
- **WHEN** 调用期间 Key 被删除、出现既有结算恢复或幂等重试
- **THEN** 既有耐久意图、原价结算、扣款幂等及配额规则保持

### Requirement: Include cold writes in inflight estimates

系统 MUST 为可能自动补齐的请求考虑 5m 冷写预留价格，不得按尚未发生的缓存命中减少预留，也不得将预留直接记为最终扣费。

#### Scenario: Cold-write price exceeds ordinary input
- **WHEN** 原始无声明的候选请求可能命中启用规则，且配置的 5m 写入价高于普通输入价
- **THEN** 输入侧预留按两者较高价估算并保留输出/倍率规则；按响应实际用量结算与释放

#### Scenario: Concurrent low balance and cancellation
- **WHEN** 低余额下并发冷写、既有重试/取消，或最终选择了不匹配的账号
- **THEN** 不因新功能重复扣费或泄漏预留，不能把缓存优惠预期当作已可用余额

### Requirement: Controlled settings and bounded propagation

策略 MUST 仅管理员可配置，复用部分更新契约；跨实例关闭必须有可验证的传播上界。

#### Scenario: Omitted setting in legacy update
- **WHEN** 旧管理端保存其他设置且省略缓存策略
- **THEN** 已有策略保持，不被空值覆盖

#### Scenario: Disable policy across instances
- **WHEN** 管理员成功关闭策略
- **THEN** 当前实例刷新，其他实例最长 60 秒后不再为新转发注入；过期刷新失败不无限保留开启状态

#### Scenario: Save policy without running probes
- **WHEN** 管理员选择范围并保存
- **THEN** 只更新设置，不自动调用付费模型；公共设置和非管理员响应不泄漏规则

### Requirement: Content-free diagnostics

诊断 MUST 可关联补齐决策、上游用量与结算记录，且 SHALL NOT 保存认证文本、邮箱、对话正文或工具输入。

#### Scenario: Log injection and skip reasons
- **WHEN** 请求被注入、跳过或遇到缓存兼容性异常
- **THEN** 仅记录请求/规则/资源 ID、模型、固定原因码及数值用量；畸形字段和上游原始错误正文不得原样写入

### Requirement: Reversible enablement

管理员 MUST 可通过关闭总开关或移除规则停止新增补齐，无需删除缓存、修改历史账单或回滚数据库。

#### Scenario: Stop a pilot
- **WHEN** 灰度发现参数错误或费用不符并关闭对应规则
- **THEN** 在传播窗口内恢复缺失请求的既有行为，已经发送的请求正常结算，客户端原有声明继续有效
