# 原生流式交付与进程内恢复候选

基线：0.2.2，源码提交 `9717116f198904442ebb400d7d06792a40dea15c`。实现日期：2026-09-30。

本改动只涉及网关及既有管理入口。默认关闭新功能，没有修改客户端、SDK、历史迁移、产品版本或完整 Sub2API 兼容基线。当前是供隔离验收的候选，不构成发布、生产部署或真实供应商兼容性承诺。

## 功能准入和策略快照

现有分组调度文档增加 `native_stream.delivery/recovery/persistence`，沿用组策略版本与 CAS；旧管理客户端没有该字段时保留已存配置。初始三个字段均为 false。recovery 必须依赖 delivery；当前拒绝 persistence=true。原等待时间、优先级方向、同层 SWRR、次数和并发限制不变。

新 turn 读取策略快照；长 WebSocket 连接的每次新 turn 重新读取配置，已运行 turn 保持旧快照。配置读取失败拒绝新准入，不用部分配置运行。关闭功能仅停止新 turn 使用，已运行任务按原快照结束。

## 交付、提交和取消

启用 delivery 后，HTTP Responses 转换与透传、WebSocket 相关路径以完整协议事件为交付单位。允许网络分片重组，不等待后续文字、完整工具参数或 completed。未知用途的合法事件继续转发，原 JSON、身份和原生序号不改写。

`httpCommitted`、`attemptCommitted`、`semanticSeen` 分离。原子提交必须先于身份头或上游事件写入；首帧写入失败、部分写入不会撤回提交。本地心跳不锁定上游尝试，不作为内容。提交前由统一 Ledger 决定生成重试，提交后禁止透明换账号混流。首帧和后续交付不等待计费落库。

WS 创建消息发送后首事件失败保留“已发送、执行状态未知”，禁止适配器内部偷偷重发 HTTP 生成。池隔离摘要包含账号、凭据版本、上游、代理与必要上下文；默认每账号最多 2 条空闲、90 秒回收。新行为不启用未经本源码验收的加速插件内部重试。

取消原因分别记录 client_detached、user_stop、startup_timeout、content_timeout、admin_cancel、lease_lost、upstream_failure、slow_consumer 等。普通流断连及时取消；用户明确停止、控制面取消、租约丢失不得恢复后复活。下游断连和慢写不直接处罚供应商。

## 当前恢复支持范围

实现 TurnManager、有界内存 EventJournal、单活动 attachment、epoch 替换、同响应补发、授权范围隔离和创建幂等。恢复是继续读取同一执行，不重新选号、不重发 create，不新增生成结算。

只对请求中明确 background=true 且 stream=true、存储允许、所选账号有 Responses 能力，并从真实上游事件确认 background、响应 ID 和合法原生 sequence_number 的 HTTP/SSE 执行开放恢复。stream、previous_response_id 或 created 本身都不构成资格。不替用户添加 background/store。OAuth store:false 及能力未验证路径不自动启用恢复。

- `GET /v1/responses/{id}`：查询原响应。
- `GET /v1/responses/{id}?stream=true&starting_after=N`：原生游标之后的历史补发，原子接实时流。
- `POST /v1/responses/{id}/cancel`：取消原执行；不创建新尝试。上游取消“已请求”与“已确认”分别记录。
- 已有 `/responses` 与 Codex 路由别名保持对应行为，WebSocket upgrade 仍由原入口处理；POST catch-all 内明确分发 cancel，不截获 input_items 等其他路径。

每次控制请求重新鉴权并校验原用户、API Key、分组及当前权限；响应 ID 不是凭据。未知响应 404，过期 410，非法游标 400，已清除的历史返回 cursor_expired。未知 ID 不自动变成 create。

当前额外的未完成边界：非流式 background=true 与 Idempotency-Key 的组合，在原生交付功能开启时返回明确的 unsupported_background_idempotency（400），不发上游生成；功能未开启和无键的既有路径保持。该组合需要后继实现原账号 retrieve/poll、真实终态观察和计费后才可开放，不能把 queued/in_progress 的 HTTP 200 当作生成完成。此封闭策略不是完整幂等能力验收通过。

Idempotency-Key 作用于用户、API Key、分组与规范化接口；同键异正文 409，并发重复请求不启动第二次生成或抢占 writer。24 小时内过期正文保留防重复状态，返回结果过期，不重新生成。X-Request-ID 仅用于诊断展示。

离线生成预算是每 turn 累计 120 秒，不因重连或心跳清零；仍占原生成并发额度。默认终态正文 5 分钟、幂等记录 24 小时、单 turn 64 MiB、单租户 128 MiB、进程 512 MiB、可恢复单事件 16 MiB。与普通实时协议限制独立。在线配额不足可降级为不可恢复实时交付；离线无法完整记录时取消，不谎称无损。store:false 清除终态正文和附件持有引用。

## 能力矩阵和边界

| 路径/故障 | 候选能力 | 验证范围 |
| --- | --- | --- |
| HTTP/SSE 首事件、delta、未知事件 | 逐完整事件交付、Flush、不可回退提交 | 本地真实 HTTP 回放与失败写入测试 |
| HTTP 下游断线，上游仍存活 | 符合资格时原 ID 游标补发、原执行继续 | 本地 handler、并发、授权和生命周期夹具 |
| WebSocket 客户端 | 原生交付、连接池、正确取消与尝试边界 | 本地 WS 夹具；不等同于真实客户端全兼容 |
| WS 同响应断线续接 | 不宣称支持 | 没有已验收的原生客户端恢复协议 |
| 上游在输出后断线 | 保留部分结果，准确终止 | 不换账号拼接；没有假装支持原生上游 resume |
| 真实外部客户端/真实供应商 | 恢复能力待逐项确认 | 没有进行收费生成或复制影子请求 |
| 进程崩溃、跨实例、持久化 | 阶段 C 未实现 | 不承诺跨实例恢复或零数据损失 |

未实现的上游同响应 resume 不会消耗或伪造“恢复成功”次数；以后接入时必须落实 3 次/10 秒总预算、200/500/1000 ms ±20% 抖动、原 deadline 与 Retry-After。Redis owner、租约接管、异步 PostgreSQL 日志和 durable_watermark 均留待阶段 C 单独验收。

## 诊断与验收

原有请求详情增加原生交付时间线，元数据缓存限 5,000 条、保留 5 分钟。该入口是请求结束后的有界诊断快照，支持刷新恢复状态，不是持久化或持续直播的全量事件时间线。只记录真实观测的首事件、首内容、含重试端到端首内容、完整事件至 Flush、提交/发送状态及恢复计数；未记录项明确显示缺失，不从 created 或 HTTP 200 推算内容成功。接口在既有管理员鉴权下，要求完整原始 scope，不返回正文或凭据。

测试使用本地可控 HTTP/WS 及隔离 PostgreSQL/Redis。新增 required CI 门禁要求精确测试 RUN/PASS，空匹配、失败和跳过都拒绝；性能测试独立于 race 与全套并行负载。基准测量点为读取到完整事件最后字节，包含分类及 Flush。最终逐门禁结果与最初失败证据在交付事务 VERIFICATION.txt 及其引用日志中，不用历史成功代替当前源码验证。

仍需按测试分组灰度和真实客户端矩阵验收后才能扩量。选择、及时交付、取消、同响应恢复四类证据全部满足前，不标记为“原生流式与可靠恢复优化完成”。

## 回滚

运行中回滚先通过现有组策略关闭新功能准入，等待旧快照 turn 收尾并保留尚未到期的记录。不能直接杀长连接充当无损回滚。跨进程迁移内存 journal 当前不可用，应如实告知维护窗口的恢复边界。

本次附带 ROLLBACK.sh 是离线源码制品事务，接受一份复制的源码 tar，将其恢复为与 BASELINE.tar 字节完全相同的归档。其验证不代表生产连接或数据库自动回滚；脚本不会修改生产工作目录、容器或数据库。
