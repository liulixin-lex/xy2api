# 设计

每键占用现有 settings 一行，无数据库迁移。INSERT ON CONFLICT 与 SELECT FOR UPDATE
在同一事务中完成首次建行、读版本、校验和写入；校验失败事务回滚。
本地写锁避免旧刷新覆盖已提交的本地写入，atomic.Pointer 发布不可变快照。

管理员接口使用字面路由和既有鉴权、合规及审计中间件；validate 使用 SkipAudit。
审计历史仅投影管理员、时间、revision、字段名和理由，不能返回原始审计 body。历史时间 `at` 归一化为 RFC3339 UTC，保留原时间点。
用户侧 DTO 与公开 settings 不注册这六个键。

请求固定快照确保 attempt、failover 和流式帧保持相同策略。静态总闸与资源上限
不能反向启用运行时功能。资源预算与限速调整不重建 worker 池。


历史完整性使用 audit_logs 成功记录的 revision 聚合，检查是否从 1 开始且连续；
不将记录条数或最大 revision 与保存的 revision 比较。只有无成功记录时，读取
保存的 revision/更新时间，采用 5 秒落盘宽限。返回 null、earlier_missing 或 gap，
多键查询优先报告 earlier_missing。分页与失败记录不改变完整性结论。


## M3 / L-01 增量

新增 upstream_identity 的严格 schema 与发布时类型化快照。仅 mode=uuid_fill、
master 开启、kill 关闭且会话命中灰度时，在转发层处理合格 OpenAI apikey 请求。
账号校验仅发生在管理写入，不进入请求或选号路径。未实现的 northstar、Anthropic
和 routing 仍返回 not_in_build。L-02 与 header-only 非默认值返回 unsupported。

Responses 复用既有 request view 的顶层遍历识别键存在性，定型后做头部插入；
Chat 在处理组的既有 decode 中记录字段存在性，关闭分支沿用原解析器。显式值、
原始 lineage 与内部 compat seed 均保持原语义。转发阶段只读取请求快照，
不读写调度状态。新增完整 trace 字段不改变最小事件。

L-01 遵守既有质量机制的优先级：仅当只读 qualityRotation accessor 返回本次
实际 enforce 轮换时，在 UUID 派生/注入前让位。此检查排在客户端显式值、旧
compat seed 与 Chat 开关之后；不调用轮换推进或调度写入。记录 quality_rotation，
原始 Chat 等排除入口记录 unsupported_ingress，未产生旧 compat seed 记录
no_compat_seed。三者 applied=false，body/header 与 L-01 关闭时逐字节一致。

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

## M3 管理员账号选项

管理员账号选项通过已注入仓储的分页能力查询，按静态协议资格过滤后取前 500 个，响应只含 ID、名称、平台。停用账号仍可配置；不以健康、冷却、分组并发或选号状态筛选。未实现分页能力的最小服务返回显式空数组。候选查询失败不影响策略读取或已提交的写入，部分候选丢弃，options返回空数组，effective.warnings末尾追加account_options_unavailable。关闭方向的validate与PUT跳过账号校验及候选查询；PUT带同一提示，validate不带。开启和混合方向仍校验账号，校验查询失败拒绝；账号查验失败时清空生效warnings，与其他invalid校验一致。PUT 的 `CACHE_POLICY_UNSUPPORTED` 与 validate 的 `unsupported_in_build` 保持既有映射，以原字节契约样例逐字段核对。

## M4 T4.0 管理能力

基于 M3 候选，仅开放 index4/5 的管理员策略读写，不接入请求或路由。生产构造注入只读分组仓储；组候选与校验均在管理员操作且发布锁之外。关闭方向完全不读组、不使用旧事实。嵌套对象按叶子点路径 diff；anchor/metadata 空范围表示关闭，busy/account 空范围表示全部；routing 开启与mixed要求输入完整键名。仅支持score，其余值沿用UNSUPPORTED错误信封。观测missing为静态构建能力，on/masked提示；fallback样例显式注入覆盖能力。细节与API现行说明见openai-cache-reliability.md，20份M4原字节契约/相关反例及M3保留回放随实现提交。

X-B1：管理能力不代表行为可用。L-04、L-03、L-07 各有静态能力，生产默认均 false；未实现能力字段的启用方向返回 unsupported_in_build，PUT 返回 CACHE_POLICY_UNSUPPORTED，关闭方向不依赖分组目录。按F v1.2，rollout不是能力字段；整体默认恒为disable。数据库只激活未实现行为时blocked/active=false，blockers带not_implemented_in_build，优先于masked；过渡构建按已实现行为决定状态。schema标记能力字段，L-03依赖L-04，未来请求accessor也要与能力位做AND。原20样例字节不变、注入完整能力；追加10份默认构建样例与部分能力/故障桩回归。


## M3.1 / L-01 启用前边界

实现范围为 fail-safe 还原、账号拒绝记忆、在途汇聚上限，以及种子来源显式标志；
不修改关闭态 writer、既有调度、韧性、重试或计费解析边界。完整行为和指标见
[OpenAI 缓存可靠性](../../../docs/openai-cache-reliability.md#m31启用前安全边界)。
`max_inflight_per_key` 默认 2、范围 1–16，内存补齐旧存储并纳入 history；升 enable、降 disable。
新跳过原因按 quality_rotation → upstream_rejects → fanin_cap 排序。
实际生效时追加 l01_restore_fallback，最小事件与未生效事件形态保持。
字面还原识别转义引号与对象键；拒绝路径标记响应已提交并复用原端点 400 writer。
M3-only 基点仅回放 M31 的 10 个 M3 派生与 5 个新样例；6 个 M4 派生 list 留集成回放。

M3.1 的种子防伪保留全部既有 routing hash / legacy sticky 字节：仅在 L-01 Enabled 时
把 scopedOpenAISessionSeed 的显式 provenance bool 写入服务端内部上下文并消费。
prepare 在 policy_off 早退之后、原 no_routing_session 的位置要求 provenance=true，
再进入 cohort；客户端的 openai-routing-v2: 文本不能获得注入资格。


### M3.1 客户端转换出口

实际注入后的 Chat Responses 上游正文先经同一有界 SSE 还原缓冲，再由 buffered
或流式转换器消费，包含失败/cyber 事件及累积内容。原生 Responses 的规则错误出口
独立清除 message 中已知 U。关闭态沿用原 reader/writer；不扩大 Messages、WS、
passthrough 或 raw Chat 的注入资格，不新增重试或恢复失败归因。
