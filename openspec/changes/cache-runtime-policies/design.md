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
