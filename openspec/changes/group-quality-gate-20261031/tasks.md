# 实施与验收

基线分支 `feat/group-quality-gate-20261031`，各工作包从它切出 `feat/gqg-wp<N>-<短名>`。派工说明、跨包契约、代码地图与含生产数据的方案在本地交接包 `.work/tasks/20261030-account-group-tiering/handoff/`，不入库。不加数据库迁移；推送、PR、合并与发版等用户授权。

## WP1 质量门槛核心（第一阶段）

- [x] 1.1 策略设置键的解析、校验与默认值：只允许 OpenAI 分组启用；按版本写入，冲突返回 409；固定账号必须是成员；写操作进审计。
- [x] 1.2 指标查询：错误率、续轮首字 p90、续轮零缓存率，含失败去重；判定只用有效数据（窗口起点、重置时刻、本组最近一次熔断结束，取最晚）。
- [x] 1.3 判定纯函数：熔断与退避；到期与试用只看新数据；80% 恢复、2 小时清零、保底、固定账号；档位（合格快档、试用、其他，快档含零缓存条件）。
- [x] 1.4 60 秒评估循环、原子快照、失败沿用、日志事件。
- [x] 1.5 分层结果（可用、达标、固定、保底保留、否决）与“可服务”判定；利润门成本与上限函数。
- [x] 1.6 策略与状态接口，权限目录。
- [x] 1.7 只有管理端“恢复状态”通过显式选项写入重置时刻；“测试”与额度重置流程不写。用 JSONB 合并写入，下一轮清除熔断并转为试用。
- [x] 1.8 判定表驱动单测；仓储集成用例一条；恢复状态一条（不丢其他 extra 字段，“测试”不写入）。
- [x] 1.9 新增现行文档 `docs/GROUP_QUALITY_GATE.md`，并完成目录、白名单与名字登记。

## WP2 调度接入、排序与满载（第二阶段，依赖 WP1）

- [x] 2.1 装门范围与利润门相同：入口、防御性装门、WS 每个 turn、组合分组的实际分组；利润门豁免的路径与模型目录不装门。
- [x] 2.2 否决进入全部检查点，原因码统一：候选过滤、legacy 资格与 DB 复核、粘性命中、份额调度、抢槽与 WaitPlan 后的终检。
- [x] 2.3 终检否决：HTTP 释放槽位后重选；WS 非续链 turn 关闭连接并要求重连。
- [x] 2.4 强续接豁免（previous_response、guardian 父账号、被续链固定的 WS turn，含 DB 复核）；末位兜底只放宽质量熔断。
- [x] 2.5 熔断时改绑会话。
- [x] 2.6 统一比较器“档位 → 优先级 → 负载 ≥80% 排后 → 成本 → 负载 → 最近使用 → ID”，替换 legacy 全部排序点；E2 替补只在同档位、成本、负载带内重排；低倍率优先关闭时顺序与现在相同。
- [x] 2.7 粘性账号满载时先等最多 2 秒；仍满再经现有选号路径与 E2 替补借用，不改绑定；全组满才等待；强续接仍等原账号。旧质量路由 Generation>0 接管绑定时保留真实排除/avoid 集，借用跳过质量 bind 更新，同档内重复借用保持稳定替补；借用不放行 avoid 坏号，保留 ≥80% 分档；无 LoadBatch 队列满仍等原号，只有换号才标记 spillover。
- [x] 2.8 选号入口表驱动单测。
- [x] 2.9 具名补丁、`scheduler-diff.json`，以及调度文档。

## WP3 兜底、计费、利润门、可用数

A 段（第一阶段）：
- [x] 3.1 请求带两个身份：原组（`BillingAPIKey`，权限与限额）与服务组（`ServingPricingKey` / `ServingPricingGroup`，计价）。去掉各价格点回退原组的分支，包括请求计价上下文与两处利润门。
- [x] 3.2 价格按兜底组（含渠道计费模型与 Fast 规则），删除兜底专属的标准价补差；权限与限额仍按原组。
- [x] 3.3 用量字段按兜底组计价，账号侧不变；批量图片拆分授权组与计价组；在途预留金额按兜底组；WS 每个 turn 固定两份快照。
- [x] 3.4 管理端用量显示原组与黄色兜底路径，浮窗文案改为“按兜底分组计费”；用户侧不变。

B 段（第二阶段，依赖 WP1）：
- [x] 3.5 兜底触发改用“可服务”判定；中间件位置与组级判定方式不变。
- [x] 3.6 利润门：成本改用实时倍率；售价取计价分组（兜底时为兜底组，组合分组为父分组）；开关与毛利率取被调度分组；新增预览接口（含换算出的上游倍率上限）。
- [x] 3.7 运维分组可用数改为可服务成员数。
- [x] 3.8 兜底表驱动单测；保留 key 记忆与 120 秒回归测试；按新口径改写原有的兜底计费测试与具名补丁。
- [x] 3.9 重写 `docs/GATEWAY_RESILIENCE.md` §4.3。

## WP4 前端（第一阶段桩数据，第二阶段联调）

- [x] 4.1 分组编辑框：质量门槛区块（只对 OpenAI 分组显示）、固定账号多选、两段保存与失败提示。
- [x] 4.2 利润门：默认 20%、隐藏安全缓冲；显示换算出的上游倍率上限与通过数。
- [x] 4.3 账号列表：门槛状态标签与三项指标；分组列表：门槛列。
- [x] 4.4 中英文案；只为新增的纯逻辑写单测。

## WP5 集成（第三阶段）

- [x] 5.1 按 WP1 → WP3-A → WP2 → WP3-B → WP4 的顺序合并，解决共享登记文件的冲突，重新生成 Wire。
- [x] 5.2 全量门禁：Go unit / integration、golangci-lint、scheduler-diff、前端 typecheck / lint / test / build、文档一致性；提交后在干净树上跑 audit。
- [x] 5.3 逐个对照 `specs/` 的场景验收，在本文件写明证据。
- [x] 5.4 确认没有新的部署前提，把升级即生效的变化写进发布说明。
- [x] 5.5 更新项目记忆，写交付报告与用户的升级配置清单，然后等待授权。

## 修复轮（2026-11-01，实施后复审）

- [x] 6.1 质量核心：成员入组/再入组保留历史、关门槛即时生效、断流不稀释错误率、试用起点为熔断结束、状态转换 Warn 日志。
- [x] 6.2 WS 与 handler：透传首轮按服务组计价、cyber 计费行保留原组、后续 turn 满载短等、WS 每轮续期绑定、准入否决文案。
- [ ] 6.3 调度：借用跳过满载账号并限制探测、legacy 借用带能力过滤且每请求只短等一次、列表出错退回等待、续期用配置 TTL、DB 瞬时错误不删绑定、开门槛的组不做判慢迁移、Explain 复用可服务判定。
- [x] 6.4 前端：毛利率留空按 20%、门槛保存失败保留弹窗、无账号查看权限不请求门槛数据、成员截断不删固定账号。
- [ ] 6.5 合并后全量门禁（含 Docker 全仓集成）与干净树 audit。

## WP5 验证证据（2026-10-31）

- 集成分支 `feat/gqg-wp5-integration`；初始后端基线 `be0c2629d`，provenance 修复 `c8d38c070`；WP1 `6c26a11f2` 合并为 `a958a61af`，索引计划夹具修正 `8aa768168`；WP4 最终 `27f7cf861` 合并为 `56d557149`。`git diff --exit-code 8aa768168 HEAD -- backend` 通过，最终 Docker 后端结果适用于集成树。
- Go 1.27.3，`GOTOOLCHAIN=local GOMAXPROCS=2`；以下 Go/lint 命令从 `backend/` 执行。仓外原始日志、命令与通过事件在交接包 `integration/wp5/`；没有新增测试文件、放宽断言或产品兜底。
- `go test -tags=unit -p=2 -timeout=20m -count=1 -json ./...`：67 包通过；service 383.103 秒，低于 8 分钟报告线。
- 本机 `go test -tags=integration -overlay <仓外overlay.json> -timeout=20m -count=1 -json -skip '^(TestUpstreamV0214MigrationUpgrade|TestUpstreamV0215MigrationUpgrade|TestPgDumperRealPostgresBackupAndAtomicRestore)$' ./...`：59 包、7829 个顶层用例通过；repository 22.098 秒。overlay 仅替换 TestMain 和两个限流测试的环境创建段，临时 PostgreSQL/Redis 已清理；11 个既有 skip 已逐项记录。三项 Docker 专用测试未在本机运行，现已由下述真实 Docker 全仓验证覆盖。
- 后续影响包 `go test -tags=unit -p=2 -timeout=20m -count=1 -json ./internal/service ./internal/repository` 通过（service 378.725 秒、repository 16.967 秒）；本机隔离仓储 integration 782 个顶层用例通过（22.355 秒），包含质量事务隔离和修复后的索引计划测试。lint、scheduler-diff、docs 一致性复验通过。
- 主持人回传测试主机真实 Docker 结果：源码 `8aa768168`，抽查计划测试与质量服务文件 SHA-256 与提交一致；`go test -tags=integration ./... -count=1 -p 2 -timeout=40m`：60 个包 ok、58 个无测试、0 FAIL/panic；repository 91.3 秒、service 175.3 秒、migrations 通过，两项旧版本迁移与备份恢复用例通过。完整命令没有人为排除三项 Docker 用例，不把无测试包或既有 skip 表述为测试通过数。
- 历史对照：`be0c2629d` 的 Docker 全仓 59/60 包通过，唯一失败为 `TestUsageLog_UpstreamModelMismatchFilterAndPartialIndex` 的索引选择断言；新夹具改变包内时机、规划器受 autovacuum ANALYZE 时机影响。WP1 事务隔离后仍可复现；`8aa768168` 在同一事务中显式 `ANALYZE usage_logs`，保持索引断言不变，主持人定向整包 2/2 与最终全仓通过。
- `go vet ./...`、`go vet -tags=unit ./...`、`go vet -tags=integration ./...`、`go vet -tags=e2e ./...`：全部通过。
- `golangci-lint run --concurrency=2 --max-issues-per-linter=0 --max-same-issues=0 ./...`：0 issues。`python3 ../tools/gateway-resilience/ci_gate.py`：五包 84/284/90/2/18，共 478 项 `-race` 通过、0 SKIP。
- `python3 tools/upstream-sync/sync.py scheduler-diff`、`python3 tools/check_docs_consistency.py`、`python3 -m unittest discover -s tools/tests -p 'test_*.py'`：通过，工具单测 96 项。
- `go generate ./cmd/server`：Wire 生成源码无差异；工具产生的 `go.sum` 变化已恢复。policy/provenance 补丁集合一致、独立校验零错误。
- `npx -y pnpm@9 --dir frontend install --frozen-lockfile --prefer-offline` 后，`npx -y pnpm@9 --dir frontend run typecheck`、`lint:check`、`test:run`、`build` 全通过；最终 470 个文件、3609 项测试（此前 `a2b0c70d4` 为 3607 项）。
- `python3 tools/upstream-sync/sync.py audit` 在首轮 `8739f463b` / `9e1da6234` 及最终前端合并 `56d557149` 的干净树通过，`node_modules` 是本工作树普通目录。最终文档提交后的固定 HEAD 审计写入仓外交付报告。
- `git diff --exit-code 8b16126f8 HEAD -- deploy backend/internal/config backend/ent backend/migrations` 通过：无新部署前提、环境变量、Ent schema 或迁移。升级即生效、历史浮窗差异、已接受 P2 与用户配置见仓外 `implementation/RELEASE_NOTES_DRAFT.md`，配置依据为 `PLAN.md` §11。

## 逐场景验收

下表逐项核对源码与通过的既有测试。测试证据来自上述全量命令；代码核对补足并发锁、界面展示等结构性条件。没有把生产实测或 mock 浏览器冒烟当成真实供应商验证。测试名以仓库内 Go 函数或前端 spec 文件名定位。

### group-quality-gate（23 个场景）

| 场景 | 实现与验收证据 | 结果 |
|---|---|---|
| Concurrent edit | `UpdatePolicy` 版本检查；仓储 `CompareAndSwapPolicies` 事务内 `FOR UPDATE` 后比较版本；`TestGroupQualityGatePolicyValidationAndStatusContract` 验证重复旧版本失败、缓存失效；前端 `groupsQualityGate.spec.ts` 验证 409 提示 | 通过 |
| Rate limits are not quality failures | `IsGroupQualitySoftFailure` 复用 `shareSoftFailureAttempt`；`TestShareSoftFailureAttempt` 的 rate limit / reset 行均为 false | 通过 |
| Cold request excluded | 仓储 LAG 按账号/会话分区、间隔 ≤5 分钟；`TestGroupQualityGateRepositoryLoadsWarmMetricsAndDeduplicatesFailures` 的首请求与 6 分钟间隔不计续轮 | 通过 |
| Late trial failure records | `accumulateQualityTrial` 重扫并以请求/失败身份去重；`TestGroupQualityGateEvaluation/trial_counts_failures_arriving_three_minutes_late_before_twenty_requests` | 通过 |
| Repeated ejection backs off | `groupQualityBackoff`；`TestGroupQualityGateEvaluation/backoff_caps_at_thirty_minutes` 覆盖 5/10/20/30/30 | 通过 |
| Stale data does not hold a recovered account | `qualityValidStarts` 及到期转试用；评估表 old samples / direct recovery requires original metric samples 行 | 通过 |
| Borderline recovery | `RecoveryReady` 的 80% 与原触发项样本要求；评估表 borderline ttft 行验证转试用，old samples 行验证试用 2 次软失败再熔断 | 通过 |
| Small group keeps two accounts | `applyQualityGuard` 目标 `min(max(2,(usable+1)/2),countable)`，严重度排序；评估表 guard ties 行覆盖 3 个可用保留 2 个，least severe 行核对严重度稳定 | 通过（代码条件与表用例组合） |
| Single usable member | 评估表 single usable member retained 行，硬状态与利润失败不进入分母 | 通过 |
| Pinned account outside the limits | 评估表 pinned failures excluded 行；固定不否决、不达标不计入保底，状态仍显示不达标 | 通过 |
| Ejected sticky account | `TestOpenAIGroupQualityScheduling` 三路径 × 普通/原子缓存 × 排除集的 ejected sticky rebind 行，绑定改到新号 | 通过 |
| Account ejected while waiting for a slot | `TestAcquireResponsesAccountSlotProfitRecheck/quality_veto` acquired/quick/queued 行：槽位释放一次、排队加减一次、排除后新号成功 | 通过 |
| WebSocket continuation | `TestOpenAIResponsesWebSocket_QualityPinnedTurnOneUsesAdmissionResult` 与 `QualityTurnAdmissionThroughHooks` 的 previous response / incomplete tools 行 | 通过 |
| WebSocket fresh turn | `TestOpenAIResponsesWebSocket_QualityTurnAdmissionThroughHooks/fresh_turn` 关闭码及重连文案；连接内不换号 | 通过 |
| Cheap but slow account | `resolveGroupQualityTier` 与调度表 tier before priority and cost；实际 legacy 排序点复用比较器 | 通过 |
| Trial account ordering | 调度表 trial after fast / trial before other；评估表试用档位与晋级清旧档位 | 通过 |
| Non-caching account | 评估表 zero cache and fast line only affect tier, disabled groups too，20 样本降档而 19 样本不降档、不熔断 | 通过 |
| Short wait keeps the cache | 调度表三路径 short retry：2 秒内获得原号且仅一个释放函数、不入队 | 通过 |
| Saturated sticky account | 调度表三路径 borrow stable、质量路由/avoid 与在途稳定行：借用其他号、原绑定保持、近期成功/哈希稳定 | 通过 |
| Low-rate priority off | 调度表 flag off retains original order；四个 legacy 排序点与 E2 的关闭分支保持原规则，WP2 定向复核已覆盖 | 通过 |
| Metric query times out | 评估表 query failure preserves ejection and applies reset / stale suppresses ejection 行：无新熔断、旧熔断到期、重置可执行 | 通过 |
| Administrator recovers an account | 评估表 manual reset clears all groups；`TestRateLimitService_RecoverAccountState_QualityGateResetOnlyWhenExplicit` 保留其他 extra 字段；只有 RecoverState 设置选项 | 通过 |
| Successful test | 同一恢复测试调用 `RecoverAccountAfterSuccessfulTest`，断言不更新 extra；额度重置调用未启用质量重置选项 | 通过 |

### availability-fallback-pricing（9 个场景）

| 场景 | 实现与验收证据 | 结果 |
|---|---|---|
| Profit gate blocks the whole group | `groupMemberServable` 共用利润/质量判断；`TestAvailabilityFallbackServableMembers/profit_excludes_the_origin` 转入服务组 | 通过 |
| Both gates off | `TestGroupMemberServableUsesSharedResilienceAndProfitThreshold`、`TestAvailabilityFallbackOnlySoftPenalties` 与 servable 表的 composite gates off 行；运维调用同一函数 | 通过 |
| Fallback group with a higher rate | servable 表服务组上限与否决行；`TestProfitControlPricesFallbackAtServingGroup` 验证通用/OpenAI 两条利润售价取服务组用户覆盖 | 通过 |
| Return to the origin group | `TestAvailabilityFallbackReturnAndFlapping`、两个 `TestFallbackReturn...` 保留 key 记忆、120 秒稳定回归与未迁移 key 立即回归 | 通过 |
| Forced Fast in the fallback group | `TestOpenAIRecordUsageUnderFallbackAppliesServingFastRules`：priority、Fast-at-standard、强制 Fast 遇上游 default 均按服务组；预留/WS 双快照测试同步通过 | 通过 |
| Model not allowed in the origin group | `TestResponsesWebSocketAllowlistUnderFallbackIsTheOriginGroup`；`BillingAPIKey`/原组 allowlist 链；批量图片与 messages 资格分离测试通过 | 通过 |
| User views a fallback request | `TestUserUsageLogShowsTheCallerGroup` 隐藏服务组；`TestOpenAIRecordUsageUnderFallbackUsesServingGroupPrices` 等验证实际价格；`UsageTable.fallback.spec.ts` 验证管理员路径与用户隐藏 | 通过 |
| Missing account cost | `openAIProfitGateCost` 先拒绝 nil；`TestOpenAIProfitControlVetoReason/nil_raw_rejects_even_with_probe`、共用成员函数 nil 用例 | 通过 |
| Mixed and composite member eligibility | `groupMemberUsable` 复用调度平台资格；servable 表 composite/Anthropic/Gemini mixed、`TestFallbackProfitControlPreview` 两项 mixed 行 | 通过 |

### WP4 与已接受边界

- `groupsQualityGate.spec.ts` 覆盖单位换算、默认值、阈值与成员 ID 校验、409/失败提示；`groupsProfitControl.spec.ts` 覆盖默认 20%、旧缓冲并入毛利、零可调度中性与零通过确认；`groupQualityPresentation.spec.ts` 覆盖策略/状态组合、固定/保底、空指标与提示面板定位。
- 实际组件核对：OpenAI 编辑框门槛、固定账号芯片/搜索、顺序保存、无价格权限剔除利润字段、五个平台单行利润预览；门槛列前移、两张列表显示等待评估/读取失败及停用未生效状态；中英文 key 对齐。合格/试用提示取成员有效 metrics/headroom，熔断中取完整窗口并注明“熔断时窗口”；低样本不标红，余量仅 <20% 显示。前端四门禁均通过。
- WP4 最终浏览器冒烟已使用合成的最终后端与真实隔离 PostgreSQL/Redis；用户授权在临时实例接受合规承诺。编辑/固定账号保存、利润预览、门槛列、兜底用量与六类状态提示通过，控制台零错误；截图 `implementation/WP4-screens/real-1..5` 在仓外，环境已清理。先前 mock 是首轮历史验证，不代表最终验证范围。
- 已接受限制（详见发布草稿）：Grok 异步视频跨组轮询计价、非调度实例 OAuth 参考值更新、入组重置转试用、确定性 fallback 排队、WS 分组查询与热路径探测 JSON 开销、利润预览使用已保存价格、触屏提示与宽表布局。
