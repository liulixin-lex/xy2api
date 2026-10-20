# 任务：精简份额调度

> 一个 PR（`feat/share-simplify-20261020`），提交分层：契约与 OpenSpec → 后端语义 → 后端模块删除 → 自适应权重（PM 2026-10-20 追加）→ 前端 → 文档与登记。每项行为改动先写失败用例，确认修复前失败。不改 `VERSION`。

## 1. 契约与登记

- [x] 1.1 OpenSpec 变更：proposal、design（含管理接口契约 §7）、tasks、规格增量。
- [x] 1.2 项目记忆登记 `20261020-share-simplify`；`docs/README.md` 的 OpenSpec 目录；`policy.json` 的 `xy_owned`。
- [x] 1.3 PM 确认契约后再改接口形状（2026-10-20 批准，附 5 处修订：切换锚点、当前窗口截断、旧形状 `shadow`、列表摘要缓存、成员视图缓存范围）。

## 2. 后端语义（接口形状暂不变）

- [x] 2.1 解码接受并忽略已删除字段（策略、账号规则）；锁定生产形态 v4 策略与旧版解码器 golden。系统设置 PUT 的旧字段随设置形状一起在 3.1 处理。
- [x] 2.2 层级取 `Account.Priority`、权重取 `EffectiveLoadFactor()`：权重只经 `shareWeight` 一个函数取值（候选的 5 个构造点、成员视图、账号份额），之后叠加系数只改这一处；规则不再参与，含离组规则。
- [x] 2.3 参数常量化（design §1）；会话保持的读法；`shadow` 读成原版（三处）。
- [x] 2.4 软溢出改用韧性判定（design §4）。
- [x] 2.5 满载与原版对齐（design §5），给出 500 账号基准前后对比。
- [x] 2.6 删除纠偏与选号开销（500 账号基准迁到 `share_scheduling_benchmark_test.go`）。
- [x] 2.7 `backend-ci.yml` 点名的 7 个用例改用负载因子夹具，名字不变。

## 3. 后端接口与模块删除（契约确认后）

- [x] 3.1 按 design §7 重写管理接口：设置（PUT 接受并忽略旧字段，保存时保留存量文档里的旧字段）、分组列表（30 秒摘要缓存）、策略视图与成员视图（全部状态成员、两种份额、实际份额、30 秒请求数缓存）、保存（只改两个字段、切换锚点）、预览与风险提示、解释（模拟新会话、空模型、负载率）、报表（数据库指标与基线、当前窗口截断）、账号份额。失败率按 PM 决定改由第 4 层 `health` 提供。
- [x] 3.2 删除影子、重平衡（含 `ClearBindings`、Lua 条件删除与受保护前缀表）、决策样本、预览旧内容、首字直方图计数；`rebalance`、`decisions` 返回 410 `SHARE_SCHEDULING_ENDPOINT_RETIRED`。缓存域、排空随语义层已删。
- [x] 3.3 Redis：小时桶按类别设保留期（`cache` 194 小时、`share` 26 小时）；基线只读整点小时桶；切换锚点 `xy2:share:switched:<group>`。
- [x] 3.4 死常量清理（影子、决策样本、首字直方图、重平衡、旧设置范围）；内核原因码在语义层已清。
- 验证：service、handler 单元测试；仓库层集成测试本地无 Docker，改用本地 PostgreSQL 18 与 Redis 临时跑通（临时测试文件已删除），CI 仍以 testcontainers 为准；本地 PostgreSQL 执行计划见 design §8。

## 4. 自适应权重（第 4 层，PM 2026-10-20 追加，0 个设置项）

依据仓库外 `G_ECOSYSTEM_RESEARCH.md` §1 的 R1–R3 与 `D_PM_SYNTHESIS.md` §10。

- [x] 4.1 R1 有界健康降权：失败判定纯函数 `shareSoftFailureAttempt` 与表驱动用例；系数函数（收缩、死区、下限、量化、单账号层、全员一样差、基线资格 ≥20 次尝试）；后台每 60 秒按窗口一次读取全部成员账号；失效保护（过期、查询失败、运维监控关、韧性总开关关、无生效份额分组）。
- [x] 4.2 R2 恢复后慢启动：余额暂停到期、硬罚到期（含超过 300 秒的限流）、刷新器观察到连续不合格至少 5 分钟；新进程无记忆时不爬升；韧性总开关关闭时不爬升。
- [x] 4.3 R3 可解释：系数在 `shareWeight` 一处叠加并返回分解；成员视图、解释、预览按 design §7、§11 给出字段与提示；报表 `details.hot_migrations`。
- [x] 4.4 design §7 契约增量、§8 执行计划（R1 两条查询）、§11；规格增量“Adaptive weights”。

## 5. 前端（另一分支，PM 安排并入）

- [x] 5.1 按 PM 说明 §5 重做份额页、账号管理与系统设置区块；退役文案键名转给后端登记。

## 6. 文档与登记

- [x] 6.1 重写 `docs/SHARE_SCHEDULING.md`（升级说明、已移除项、精度边界）；更新 `GATEWAY_RESILIENCE.md`“与份额的关系”、`account-scheduling.md`、旧变更的状态说明、`docs/README.md`。
- [x] 6.2 `tools/docs_consistency.json` 的 `retired` 登记字段名、路由名与文案键（含前端清单）；`policy.json` 登记 `AccountsView.vue` 的两处份额钩子。
- [ ] 6.3 项目记忆收尾。

## 7. 验证

- [ ] 7.1 后端：gofmt；`go vet`（默认、unit、integration、e2e）；golangci-lint v2.14.0；`go test -tags=unit ./...`；无标签测试；`ci_gate.py`（-race、零跳过）。
- [ ] 7.2 仓库工具：`tools/tests`、`sync.py scheduler-diff`、`check_docs_consistency.py`、干净树上 `sync.py audit`。
- [ ] 7.3 PR 的 CI 全绿。
- 不做（未授权）：测试服务器场景（含 R1 的故障上游注入：一个账号 20% 返回 5xx，对比新会话分布）、升级演练、浸泡。

## 8. 后续事项（不在本 PR）

- R4：下次同步上游 v0.2.16、v0.2.17 时，补份额检查（模型级临时停调与 compact 模型支持在份额门禁中生效，成员视图标注“仅部分模型受限”），放在同步任务里。
- R1 方案 C：韧性在施加软罚处写 Redis 按分钟的软罚计数，补上卡流与传输错误。触发条件：生产出现“卡流为主、未被降权”的账号。
- 账号列表缺少 `admin.accounts.status.disabled` 文案（本机联调发现的原有问题，与本 PR 无关）。
