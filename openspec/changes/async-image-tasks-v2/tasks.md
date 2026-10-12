# 任务记录

## 0. 起点

- [x] 拉取最新 `main`（`7b1d5d8c3`，0.2.6），新建分支 `feat/async-image-tasks-v2-20261012`，登记项目记忆。
- [x] 核对 0.2.6 异步任务实现、调度中间件、计费请求 ID、用量持久化顺序、错误日志中间件与关停流程。

## 1. 调度

- [x] `DetachControlledRequest` / `WrapControlledResponseWriter`；单测覆盖提交 context 取消后新账本仍可用、模式与请求 ID 保留、无账本时不变。

## 2. 存储、协调与计费

- [x] 任务记录新增字段；`Update`（WATCH/MULTI）与 `Delete`；并发写入重试单测。
- [x] Redis 协调器：幂等认领/绑定/释放、在途索引（Lua 准入、`ZADD XX` 心跳）、清扫锁、查询限流；miniredis 单测。
- [x] 账本查询 `usage_logs` / `usage_billing_intents`；sqlmock 单测。

## 3. 服务与接口

- [x] 创建/开始/心跳/完成/失败（终态只写一次）、计费刷新、转存重试、删除、清扫、准入上限。
- [x] 提交流程（幂等、审核、两级准入、撤回）、重放、长轮询、DELETE、能力发现、`Prefer: respond-async`、排空。
- [x] 错误日志：中间件后处理抽出为 `recordOpsErrorAfterRequest`，异步执行复用；清扫与转存失败直接写入。
- [x] 配置段 `image_tasks`、Wire 重新生成、清理阶段排空与停止清扫器。

## 4. 验证

- [x] service / repository / handler / server 相关单测（Go 1.27.2）。
- [ ] 后端完整 unit、golangci-lint、来源审计（干净提交后）。
- [ ] 真实 PostgreSQL/Redis 环境的端到端演练（可控模式出图、重启中断收尾、幂等重放、DELETE），在下游生图站实验室进行。

## 5. 交付

- [ ] 推送分支（已授权）。
- [ ] PR、合并、版本、主站部署：等待用户另行授权。
