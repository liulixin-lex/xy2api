# 异步图片任务 v2

## 背景（2026-10-12）

0.2.6 已有 `POST /v1/images/{generations,edits}/async` 与 `GET /v1/images/tasks/{id}`，下游生图站（XY-image）已在生产使用。实际接入暴露出以下缺口：

1. **可控调度模式下任务必然失败**：任务沿用提交请求的调度账本，`beginDispatch` 检查到提交请求的 context 已在 202 后取消，返回 `upstream request failed: context canceled`（未计费）。只有 `sub2api` 模式能用。
2. **202 丢失没有幂等**：客户端重试会产生第二次生成和第二笔费用。
3. **实例重启后任务永远停在 processing**：任务是进程内 goroutine，没有心跳、清扫和优雅退出，24 小时后才过期，期间客户端不知道是否计费。
4. **转存失败被标为普通失败**：此时已经计费，任务里却看不出来；也没有重试。
5. **任务里没有计费结论和请求 ID**：客户端只能按时间和模型猜。
6. **没有准入上限、没有删除、没有能力发现、异步失败不进错误日志。**

## 变更

只做加法，0.2.6 的路径、字段、状态语义都不变：

- 任务使用独立的调度账本（保留提交时的调度模式快照和调度请求 ID），修复可控模式。
- `Idempotency-Key`：同键同请求返回原任务（200 + `Idempotent-Replayed`），同键不同请求 422，提交中 409。
- 任务视图新增 `request_id`、`billing{status,request_id}`、`model`、`endpoint`、`started_at`、`heartbeat_at`、`idempotency_key`；失败任务的计费结论查本库账本。
- 心跳（15 秒）+ 清扫器（每分钟，Redis 锁，心跳超 2 分钟收尾为 `task_interrupted`）+ 优雅退出（SIGTERM 后拒绝新提交，等待在途任务，超时取消并收尾）。终态只写一次。
- 转存重试 3 次（1/3/9 秒），仍失败为 `result_storage_failed` 且 `billing=charged`。
- 每 Key / 每实例在途上限（429 `too_many_tasks`），每 Key 查询限流。
- `DELETE /v1/images/tasks/{id}`（删除对象和记录，运行中 409）、`GET /v1/images/async/capabilities`、`?wait=N` 长轮询、同步端点的 `Prefer: respond-async`。
- 任务失败补写 `ops_error_logs`（`request_path` 为提交路径），结构化事件日志。
- 新增配置段 `image_tasks`（全部有默认值）；无数据库迁移。

## 边界

- 不做回调和 SSE（回调会暴露源站地址；长连接受代理空闲超时影响）。
- 不自动重跑任务（重跑就是第二次付费调用），不自动退款（已计费未交付的处理由运营决定）。
- Gemini `generateContent` 异步、按分组配置在途上限、会话读取任务不在本轮。
- 用户本轮授权：新建分支开发并推送分支；合并、发版、部署主站另行确认。

详见 [design](design.md)、[spec](specs/async-image-tasks/spec.md)、[使用说明](../../../docs/ASYNC_IMAGE_TASKS.md)。
