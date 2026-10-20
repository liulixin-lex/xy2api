# 任务：精简份额调度

> 一个 PR（`feat/share-simplify-20261020`），提交分层：契约与 OpenSpec → 后端语义 → 后端模块删除 → 前端 → 文档与登记。每项行为改动先写失败用例，确认修复前失败。不改 `VERSION`。

## 1. 契约与登记

- [x] 1.1 OpenSpec 变更：proposal、design（含管理接口契约 §7）、tasks、规格增量。
- [x] 1.2 项目记忆登记 `20261020-share-simplify`；`docs/README.md` 的 OpenSpec 目录；`policy.json` 的 `xy_owned`。
- [ ] 1.3 PM 确认契约后再改接口形状。

## 2. 后端语义（接口形状暂不变）

- [ ] 2.1 解码接受并忽略已删除字段（策略、账号规则、系统设置 PUT）；锁定生产形态 v4 策略与旧版解码器 golden。
- [ ] 2.2 层级取 `Account.Priority`、权重取 `EffectiveLoadFactor()`：`Candidate.Weight` 与 5 个构造点；规则不再参与，含离组规则。
- [ ] 2.3 参数常量化（design §1）；会话保持的读法；`shadow` 读成原版（三处）。
- [ ] 2.4 软溢出改用韧性判定（design §4）。
- [ ] 2.5 满载与原版对齐（design §5），给出 500 账号基准前后对比。
- [ ] 2.6 删除纠偏与选号开销（500 账号基准迁到 `share_scheduling_benchmark_test.go`）。
- [ ] 2.7 `backend-ci.yml` 点名的 7 个用例改用负载因子夹具，名字不变。

## 3. 后端接口与模块删除（契约确认后）

- [ ] 3.1 按 design §7 重写管理接口：设置、分组列表、策略视图与成员视图（含失败率与 30 秒缓存）、保存（只改两个字段）、预览与风险提示、解释（模拟新会话、负载率）、报表（Metrics 与基线）、账号份额。
- [ ] 3.2 删除影子、重平衡、决策样本、缓存域、排空、预览旧内容、首字直方图计数；`rebalance`、`decisions` 返回 410。
- [ ] 3.3 Redis：`cache` 类小时桶保留 194 小时；基线只读小时桶。
- [ ] 3.4 死常量清理。

## 4. 前端（另一分支，PM 安排并入）

- [ ] 4.1 按 PM 说明 §5 重做份额页、账号管理与系统设置区块；退役文案键名转给后端登记。

## 5. 文档与登记

- [ ] 5.1 重写 `docs/SHARE_SCHEDULING.md`（升级说明、已移除项）；更新 `GATEWAY_RESILIENCE.md`“与份额的关系”、`account-scheduling.md`、旧变更的状态说明、`docs/README.md`。
- [ ] 5.2 `tools/docs_consistency.json` 的 `retired` 登记字段名、路由名与文案键。
- [ ] 5.3 项目记忆收尾。

## 6. 验证

- [ ] 6.1 后端：gofmt；`go vet`（默认、unit、integration、e2e）；golangci-lint v2.14.0；`go test -tags=unit ./...`；无标签测试；`ci_gate.py`（-race、零跳过）。
- [ ] 6.2 仓库工具：`tools/tests`、`sync.py scheduler-diff`、`check_docs_consistency.py`、干净树上 `sync.py audit`。
- [ ] 6.3 PR 的 CI 全绿。
- 不做（未授权）：测试服务器场景、升级演练、浸泡。
