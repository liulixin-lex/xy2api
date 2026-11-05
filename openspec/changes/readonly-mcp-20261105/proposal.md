# 只读 MCP 诊断入口

运维管理员需要通过 AI 快速定位请求、最终错误、换号尝试、账号与分组健康，不应手工登录服务器查数据库。当前 backend 权限只在 Gin admin middleware 内执行；MCP 直接调用 service/repository 不会自动继承目录规则，且部分看似读取的 GET/Explain 会 probe upstream 或清理 Redis。

在现有 Go service 中新增静态 allowlist 的只读 MCP。每个 tool 按当前 XY2API 用户、MCP OAuth resource/audience、逐项业务 ACL、target/Fields/redaction 和 source owner 决定权限。普通用户只读本人数据；root 与 operator 只读明确设计的诊断范围。服务读取受限于 PostgreSQL read-only identity、已审计的 Redis 读命令与固定查询投影，不开放任意 SQL 或 admin API proxy。

日志和 usage 分别记录请求上下文、最终计费事件、错误与每次 upstream attempt；现有 request/client/billing ID 不完全同源。首版保留来源、字段 allowlist 和关联证据/置信度，不宣称一一完整 trace。方案及逐 tool ACL 表见 docs/READ_ONLY_MCP_DESIGN.md。

源码与受限数据预检、方案签字已完成。2026-11-05 用户明确授权开始代码实施；全部后端由 gpt-6.1-sol / xhigh 执行，按最新指令由 gpt-6-astra / xhigh 分交付核对与安全红队两个方向审查。当前工作不包含 push、PR、merge、release、生产数据库变更、deploy 或 enablement；实际实施与验证状态见 tasks.md。
