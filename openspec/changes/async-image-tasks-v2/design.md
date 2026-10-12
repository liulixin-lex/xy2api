# 设计

## 调度上下文

`ControlledSchedulingMiddleware` 为每个请求装一份 `ControlledRequest`，其 `clientContext` 是原请求 context，请求结束时 `Close()`。异步任务原先通过 `context.WithoutCancel` 继承了这份账本，因此在 202 之后：

- `beginDispatch` 看到 `clientContext.Err() != nil`，直接返回 `context canceled`；
- 账本已被中间件 `Close()`。

`service.DetachControlledRequest(execCtx, body)` 为任务新建账本：`clientContext` 为任务执行 context（30 分钟上限），复制提交时的模式快照（请求保持开始时的模式）、辅助请求标记和调度请求 ID（便于用 `X-Scheduling-Request-Id` 关联尝试记录），并从缓存的请求体重新读取元数据。`WrapControlledResponseWriter` 给任务的录制 writer 装上与中间件相同的首输出观察器。`sub2api` 模式下不读元数据、不包 writer，与中间件一致。

## 存储与协调（Redis）

| 键 | 内容 |
| --- | --- |
| `image_task:<id>` | 任务记录 JSON（沿用 0.2.6，新增字段 omitempty），TTL 24 小时 |
| `image_task_idem:<user>:<key>:<sha256(Idempotency-Key)>` | `{fingerprint, task_id}`；认领时 2 分钟，绑定任务后与任务同寿命 |
| `image_task:inflight` | ZSET，成员 `<api_key_id>:<task_id>`，分数为心跳毫秒，供清扫器 |
| `image_task:inflight:<api_key_id>` | ZSET，成员 `task_id`，供每 Key 准入计数 |
| `image_task:lock:sweeper` | 清扫互斥锁 |
| `image_task:poll:<api_key_id>:<秒>` | 每秒查询计数 |

- 任务记录的所有写入都走 `Update`（WATCH/MULTI 乐观事务）：心跳、完成、失败、清扫互不覆盖；终态写入要求当前仍是 `processing`，后到的结果被丢弃并记日志。
- 准入用 Lua 原子执行「数存活成员（心跳在 2 分钟内）→ 未超限则加入两个索引」。心跳用 `ZADD XX`，已释放的成员不会被心跳复活。
- 本地另有每实例计数（原子计数器），与 `sync.WaitGroup` 一起支撑优雅退出。

## 计费结论

`ImageTaskBillingLookup` 用 `(request_id, api_key_id)` 唯一键查询：`usage_logs` 有行 → `charged`；`usage_billing_intents` 有未结算行 → `pending`；都没有 → `not_charged`。图片用量在同步处理函数返回前已持久化（`runDurableUsageRecordTask` 同步执行），任务收尾时查询即为准确结论。查询失败或仍在结算时记 `pending`，之后每次查询任务时重查并落库。`request_id` 由 `UsageBillingRequestIDFromContext` 按与计费相同的优先级（client → local）得出；两者都没有时不写，避免与计费生成的随机 ID 不一致。

## 执行与收尾

提交顺序：开关/排空检查 → 鉴权与平台 → 幂等键格式 → 读体、流式拒绝、校验 → 幂等认领（重放在审核和准入之前返回）→ 内容审核 → 实例上限 → Key 上限 → 创建任务 → 绑定幂等键（失败则撤回任务，返回未提交）→ 202 → 启动执行。202 之前任何失败都释放幂等键与名额。

执行：`Start` 记录实例和开始时间 → 心跳协程 → 在与中间件相同的 ops 捕获 writer 下调用同步处理函数 → 停心跳 → 按结果收尾：

| 情况 | 结果 |
| --- | --- |
| 2xx 且 JSON 有效 | 转存（重试 3 次）→ `completed`、`charged`；转存仍失败 → `result_storage_failed`、`charged` |
| 优雅退出取消（cause = shutdown） | `task_interrupted`（503），计费查账本 |
| 执行超时 | `timeout`（504），计费查账本 |
| 其他 | 上游错误原样保存，计费查账本 |

随后把请求路径恢复为提交路径，调用与中间件共用的 `recordOpsErrorAfterRequest`，使异步失败与同步失败在后台错误日志中同样分类。清扫器与转存失败没有请求上下文，直接写一条 `ops_error_logs`（phase `internal`，source `gateway`）。

## 优雅退出

`provideCleanup` 在关闭 Redis/数据库之前先调用 `AsyncImageHandler.Drain()`：置排空标志（新提交 503），等待在途任务至 `drain_timeout_seconds`，超时则以 `errImageTaskShutdown` 取消剩余任务并再等最多 15 秒让其写入终态，然后停止清扫器。`main` 中 `Server.Shutdown` 先于清理执行，因此排空期间不再接收新连接。

## 兼容

- 新字段全部 `omitempty`；0.2.6 写入的任务照常可查。错误体格式不变（`type = code`）。
- `GET`/`DELETE` 任务和能力发现跳过余额检查（与 0.2.6 的轮询一致），鉴权、禁用、IP、分组检查照旧。
- 新路由只追加（`gateway.go` 属兼容不变量，未改已有路由）。
