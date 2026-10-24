# 设计

每键占用现有 settings 一行，无数据库迁移。INSERT ON CONFLICT 与 SELECT FOR UPDATE
在同一事务中完成首次建行、读版本、校验和写入；校验失败事务回滚。
本地写锁避免旧刷新覆盖已提交的本地写入，atomic.Pointer 发布不可变快照。

管理员接口使用字面路由和既有鉴权、合规及审计中间件；validate 使用 SkipAudit。
审计历史仅投影管理员、时间、revision、字段名和理由，不能返回原始审计 body。
用户侧 DTO 与公开 settings 不注册这六个键。

请求固定快照确保 attempt、failover 和流式帧保持相同策略。静态总闸与资源上限
不能反向启用运行时功能。资源预算与限速调整不重建 worker 池。


历史完整性使用 audit_logs 成功记录的 revision 聚合，检查是否从 1 开始且连续；
不将记录条数或最大 revision 与保存的 revision 比较。只有无成功记录时，读取
保存的 revision/更新时间，采用 5 秒落盘宽限。返回 null、earlier_missing 或 gap，
多键查询优先报告 earlier_missing。分页与失败记录不改变完整性结论。
