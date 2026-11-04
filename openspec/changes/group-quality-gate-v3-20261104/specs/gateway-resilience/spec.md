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
