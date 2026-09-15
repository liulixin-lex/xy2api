# v0.0.10 本地代码审计与修复

- 更新时间：`2026-09-15T07:36:11+08:00`（Asia/Shanghai）。
- 执行者：Codex；当前会话未公开具体模型ID。
- 基线：`v0.0.10` / `e695c0356c972f398045f87b28a9cccda8a66176`；Sub2API兼容基线`v0.2.4`。
- 修复副本：`Y:\jiance\xy2api-audit-v0.0.10`；分支`audit/v0.0.10-fixes`，未提交。原项目`Y:\jiance\xy2api`保留。
- 交付与完整原始日志：`Y:\jiance\xy2api-update-artifacts\20260915-050045`；旧v0.0.8更新交付保存在`pre-audit-v0.0.10/`，本轮基线仅为`AUDIT_BASELINE_v0.0.10.zip`。
- 结论：已复现缺陷已作最小修复；验证结果区分通过、历史失败、跳过和部分验证，不将本地测试表述为全仓零bug。

## 修改及原因

| 项目 | 位置 | 行为 |
| --- | --- | --- |
| 答案歧义 | `pkg/iqcheck/answer.go` 的 `ambiguity` | 量词后的“或/或者”等不再把21和29的多解当作聪明；明确21、明确其它整数、未知三态保留。 |
| 重复关键字段 | `pkg/iqcheck/parse.go` 的 `validateEvent` | 关键字段大小写及Unicode折叠与Go解码一致，`status`/`Status` 等重复字段归未知。 |
| 消息边界 | `pkg/iqcheck/parse.go` 的 `terminal` | 独立最终消息2和1不再拼成21；单条消息内合法内容分片仍合并。 |
| 无效预留租约 | `repository/account_iq_monitoring.go` 的 `StartIQCheck` | 只释放同token、未开始、已禁用/修订失效的预留；按当前配置排期，不扣预算、不新增记录；其它token/已开始任务不变。 |
| 验证码网络分类 | `repository/aliyun_captcha_verifier.go` 的 `normalizeAliyunCaptchaError` | 仅有HTTP响应状态的SDK错误转业务错误；网络、取消、超时保持原始分类，验证码校验规则不变。 |
| 复用编辑弹窗 | `EditAccountModal.vue` 的 `iqValid` / `handleSubmit` | 开窗/换账号重置并重新校验；只有OpenAI受IQ校验限制，真实IQ子组件参与同实例跨账号/平台回归。 |
| 模型目录状态 | `IQCheckSettings.vue` 的来源/目录/validity | 显示模型和能力来源，缓存、陈旧和错误状态分别可见；保留自定义值，修正测试桩的te接口。 |
| 390px布局 | `EditAccountModal.vue` 的WS mode区域3处class | 窄屏从横向排布改为纵向，宽屏保持原布局；只调整class，不改WS配置行为。 |

租约修复涉及数据库事务与调度，验证码修复涉及认证链路；已在批准方案中说明风险。未改变认证准入、支付、权限或部署配置，未改历史迁移。其余新增内容为复现/回归测试及本文档。

## 已执行验证

| 范围 | 实际结果与边界 |
| --- | --- |
| 前端全量 | 271个测试文件、2006项通过，零跳过/遗漏/重复；锁文件安装、全仓lint、typecheck、i18n及生产构建通过。布局修复后追加64项相关测试、单文件lint和生产构建通过，不把重叠复测累计为2070项全量。 |
| 后端默认及unit | 按现有109包清单和service字母分组完成；原始default为6163 PASS/12 SKIP，unit为10604 PASS/16 SKIP。9项securityaudit已用真实PG/Redis补齐；压缩往返和R6新增用例有独立补测。当前覆盖名单无遗漏。R6在default执行后按同一无标签源码复用，不称作独立unit重跑。 |
| 超时记录 | default-nonservice外层110秒退出124保留；108个包全部有终态（51通过/57无测试），无失败或遗留运行包。其它默认/unit分组退出0。原全仓静态检查超时也保留，后续按相同CI配置拆3组均0 issues。 |
| 静态/构建 | 最终新增R6测试后service静态检查再次退出0（70.235秒）；前后端生产构建通过；Python工具13项通过。 |
| PostgreSQL/Redis集成 | 完整repository执行，1339条PASS测试事件、1个原有TODO子项SKIP；另外3个Docker预检skip通过正确命名管道环境补验为3 PASS/0 SKIP，无生产或测试源码修改。 |
| integration范围 | 4包直接带integration标签执行；其余105包按测试文件集合与非测试build约束一致性，明确复用默认测试结果，不声称独立执行了第二套全量integration。 |
| 新装与升级 | 57项检查通过；全新0.0.10为290条迁移，0.0.8的287条升级至290条，仅新增241–243；旧checksum/applied_at及fixture数据保持，回填/约束/幂等通过。 |
| CI shell/upstream | 7项门禁通过，含部署脚本fixture与干净临时snapshot的upstream-sync audit。Apple测试使用Linux stat/plutil适配器，不代表真实macOS Apple Container端到端。 |
| 浏览器与真实本地服务 | 桌面1280与窄屏390流程、两账号创建/编辑、自定义/同步目录、缓存陈旧错误、批量只改timeout、立即检测、三态历史、默认折叠/展开及布局共12项通过/修复验证。窄屏modal-body从364/441变为364/364；宽屏886/886，console为0错误/警告。 |
| 预算与三轮保留 | 窄屏账号真实发出10次本地Responses请求后预算延期，无第11次请求；真实SQL只保留3条，第四轮开始即淘汰的事务时序另有仓储集成断言。 |
| 诊断下载 | PARTIAL：522字节JSON及envelope白名单校验通过，但浏览器仍为.crdownload临时文件，未验证最终完成状态；工具拒绝内部下载页后未另行绕过。 |

## 最新发布日志逐项对应

以下6条对应固定版本Release原文，完整测试事件/行号映射见交付目录`audit-evidence/release-v0.0.10-evidence.json`及同名Markdown。

### R1 — 解析 / 压缩 / 媒体 / 诊断

> 改进 OAuth/Responses 响应解析，兼容合法辅助 SSE 事件、压缩和媒体类型差异；损坏或未完成响应记为未知，并提供脱敏诊断。

- 实现：`backend/internal/pkg/iqcheck/parse.go:57-582`；`backend/internal/pkg/iqcheck/diagnostic.go:13-78`；`backend/internal/service/iq_check_service.go:168-287`。
- 用例：`TestHTTPStreamContracts`、`TestHTTPBoundsCompressionAndDiagnostics`、`TestOAuthContractFixture`、`TestDiagnosticFailureEventAndCompression`、`TestHTTPCaseAliasedFields`、`TestHTTPFinalMessageBoundaries`、`TestHTTPCompressedUpstreamRoundTrip`、`TestIQCheckCompleteOAuthStream`。
- 结果：本地协议fixture、损坏/未完成响应及HTTP gzip/deflate/br/zstd往返已执行；不是逐个真实模型供应商验收。
- 边界：浏览器smart/degraded/unknown与诊断有效负载已有实测；下载finalize仍partial
- 边界：HTTP压缩/媒体矩阵来自自动本地fixture，不是浏览器逐格式或真实供应商逐模型验收

### R2 — IQ 调度 / 预算 / 配额组 / 状态

> 增加每日预算、共享并发和配额组、错误冷却及 Retry-After 退避；支持固定间隔与可选节约模式。

- 实现：`backend/internal/domain/iq_check.go:144-278`；`backend/internal/repository/account_iq_monitoring.go:14-226`；`backend/internal/repository/account_iq_check.go:145-342`；`backend/internal/service/iq_check_monitoring.go:32-149`；`backend/migrations/243_iq_check_monitoring.sql:2-50`。
- 用例：`TestIQMonitoringBudgetAndFreshness`、`TestRetryAfter`、`TestIQMonitoringErrorCodes`、`TestIQMonitoringSchedule`、`TestIQMonitoringSettingsPreserveGateAndBudget`、`TestIQMonitoringBudgetStartAndRecovery`、`TestIQMonitoringSharedQuotaAndPause`、`TestIQMonitoringStaleResultKeepsCooldown`、`TestIQMonitoringReleasesInvalidPendingClaim`、`TestIQMonitoringRejectedStartPreservesActiveReservation`、`TestIQCheckClaimsAcrossReplicasAndRestart`、`TestIQMonitoringBusinessSlots`、`TestIQMonitoringRejectedStartDoesNotProbe`、`IQ check settings changes only a selected schedule field and keeps model settings`、`IQ check settings disables fields excluded from a partial bulk edit without resetting their values`、`IQ check settings emits the blue switch setting and an independent bounded interval`、`BulkEditAccountModal updates only selected IQ fields across accounts`。
- 结果：真实数据库integration覆盖预算、共享配额、暂停、stale、跨副本恢复与lease修复；服务slot/拒绝执行回归通过。
- 边界：本地真实后端/DB与模拟上游实测10次请求后daily_budget延期，无第11次请求；不是真实供应商额度验收
- 边界：浏览器未穷举running/pending/paused每种状态；对应repository/service自动测试另列
- 边界：停止旧worker及一致升级属于部署验收

### R3 — 模型 / 思考深度 / 来源 / 批量设置

> 检测模型和思考深度使用原生下拉控件，支持搜索、自定义输入和同步上游支持的模型。

- 实现：`frontend/src/components/account/IQCheckSettings.vue:14-36`；`frontend/src/components/account/IQCheckSettings.vue:95-141`；`frontend/src/components/account/EditAccountModal.vue:4285-4288`；`frontend/src/components/account/EditAccountModal.vue:4858-4864`；`frontend/src/components/account/BulkEditAccountModal.vue:2090-2094`；`frontend/src/api/admin/accounts.ts:75-92`；`frontend/src/types/index.ts:1477-1482`；`backend/internal/service/iq_check_models.go:36-164`；`frontend/src/components/account/EditAccountModal.vue:1811-1821`。
- 用例：`TestIQCheckModelDiscoveryReadOnly`、`IQ check settings fetches actual models, validates declared effort and permits upstream default`、`IQ check settings discards results after switching accounts, and keeps custom input usable after failure`、`IQ check settings uses native dropdown search, custom selection, Escape and outside-click behavior`、`IQ check settings keeps selection through a failed refresh and merges repeated clicks`、`IQ validity and catalog feedback re-emits invalidity when restored settings have the same validation error`、`IQ validity and catalog feedback separates upstream model source from upstream effort capabilities`、`IQ validity and catalog feedback separates upstream model source from reference effort capabilities`、`IQ validity and catalog feedback separates upstream model source from undefined effort capabilities`、`IQ validity and catalog feedback uses the reasoning capability source when the model declares no reasoning support`、`IQ validity and catalog feedback keeps ID-only capabilities and unsynced custom model sources unknown`、`IQ validity and catalog feedback shows stale, cached and error feedback together after a catalog failure without changing selections`、`IQ validity and catalog feedback shows stale, cached and error feedback together after a request failure without changing selections`、`EditAccountModal IQ validity lifecycle saves a non-OpenAI account after invalid OpenAI input (close first: true)`、`EditAccountModal IQ validity lifecycle saves a non-OpenAI account after invalid OpenAI input (close first: false)`、`EditAccountModal IQ validity lifecycle rehydrates valid saved IQ settings when reopening the same account in the same instance`、`EditAccountModal IQ validity lifecycle still blocks invalid saved OpenAI settings after closing and reopening`、`EditAccountModal IQ validity lifecycle keeps invalid OpenAI settings blocked when replacing the account with id 8 and the same validation error`、`EditAccountModal IQ validity lifecycle keeps invalid OpenAI settings blocked when replacing the account with id 1 and the same validation error`、`EditAccountModal IQ validity lifecycle ignores unrelated IQ validity at the non-OpenAI submit entry point`、`BulkEditAccountModal updates only selected IQ fields across accounts`、`IQ check configuration API sends a partial setting without defaulting omitted fields`、`IQ check configuration API uses the read-only IQ catalog endpoint with cancellation`。
- 结果：真实IQ组件挂载，同一EditAccountModal实例跨关闭/打开/换账号回归通过。控件是项目自带Select，不称为HTML原生select。 本地浏览器批量只改timeout=45，两账号其余9字段真实DB比对保持；390px为364=364，1280px为886=886，console=[]。
- 边界：浏览器设置/复开/同步/来源/stale+error/timeout-only批量及390/1280布局已实测
- 边界：跨平台复用实例来自真实组件自动回归，未新增第三个浏览器账号；并非所有流程在两种宽度重复
- 边界：真实供应商逐模型能力目录未验收

### R4 — 默认折叠 / 文案 / 脱敏诊断

> 简化检测设置和记录文案，原始回答及技术诊断默认折叠。

- 实现：`frontend/src/components/account/IQCheckSettings.vue:50-66`；`frontend/src/components/admin/account/IQCheckResultsModal.vue:25-55`；`frontend/src/components/admin/account/IQCheckResultsModal.vue:73-112`；`frontend/src/api/admin/accounts.ts:1167-1169`；`backend/internal/service/iq_check_monitoring.go:151-169`；`backend/internal/handler/admin/account_handler.go:3398-3414`；`backend/migrations/242_iq_check_diagnostic.sql:2-4`。
- 用例：`IQ results shows three records and keeps them visible when a diagnostic download fails`、`IQ results discards a late response after changing accounts`、`IQ results shows the same extracted answer for JSON and prose while preserving folded originals`、`IQ results distinguishes unfinished and legacy attempts`、`IQ results shows bounded diagnostics separately from an absent answer`、`TestHTTPBoundsCompressionAndDiagnostics`、`TestDiagnosticFailureEventAndCompression`、`TestIQCheckRepositoryLifecycle`。
- 结果：组件断言details无open；过滤unexpected_secret/脚本、失败后保留记录及切账号丢弃迟到结果已执行。 主线程最终UI/SQL与下载负载白名单校验已exit0；payload有效，下载完成状态仍PARTIAL。
- 边界：原文/诊断默认折叠及展开已浏览器实测；522字节有效JSON和envelope白名单已核验
- 边界：诊断下载PARTIAL：.crdownload未finalize；内部下载管理页被工具策略阻断后未绕过；有效payload不等于下载完成
- 边界：未宣称中英每条文案逐项浏览器验收

### R5 — 最近三轮 / 第四轮开始清理

> 每个账号保留最近三轮记录，第四轮开始时清理最旧记录。

- 实现：`backend/internal/repository/account_iq_monitoring.go:209-214`；`backend/internal/repository/account_iq_check.go:390-418`；`frontend/src/components/admin/account/IQCheckResultsModal.vue:24-57`。
- 用例：`TestIQCheckRepositoryLifecycle`、`TestIQMonitoringBudgetStartAndRecovery`、`IQ results shows three records and keeps them visible when a diagnostic download fails`。
- 结果：真实数据库生命周期每次start即检查数量，第四轮检查最旧淘汰且count3；deferred不新增轮次另有覆盖。 UI与实际SQL定稿确认两账号各3条，mobile三条为smart21/unknown歧义/unknown歧义；10次请求后预算延期无新增第11次请求。
- 边界：本地真实SQL确认两账号各3条；mobile在10次请求后保留smart21/unknown歧义/unknown歧义，预算延期不新增第11轮
- 边界：第四轮开始即时淘汰的严格事务时序由repository自动集成断言，最终UI/SQL快照只证明保留状态

### R6 — 21 / 降智 / 未知 / 关闭检测解除IQ gate

> 答案21仍判为聪明，明确其他答案为降智，无法可靠判定为未知；关闭检测可解除检测自身的调度限制。

- 实现：`backend/internal/pkg/iqcheck/answer.go:124-268`；`backend/internal/pkg/iqcheck/iqcheck.go:34-48`；`backend/internal/domain/iq_check.go:214-216`；`backend/internal/service/account.go:184-220`；`backend/internal/repository/account_iq_check.go:23-76`；`backend/internal/service/account_iq_audit_test.go:11-71`。
- 用例：`TestGrade`、`TestExactExplainedNumbers`、`TestGradeQuantityAlternatives`、`TestProtocolAmbiguityAndRefusal`、`TestIQCheckSchedulingGate`、`TestAccountIQAuditSchedulingGate`、`TestIQCheckRepositoryLifecycle`。
- 结果：21、其他整数、歧义/拒答与量词解释回归通过；新增R6测试1顶层/15子项实际pass，关闭degraded解除IQ gate及unknown放行均有直接行为断言，且人工禁用/非active/过期/过载/限流/临时禁调仍拒绝。
- 边界：新增真实Account.IsSchedulable/domain用例已直接执行：degraded启用拒绝、关闭恢复，unknown放行且六类其它约束仍有效
- 边界：smart/degraded/unknown状态浏览器实测；关闭IQ后真实网关流量重新路由未单独做浏览器验收
- 边界：结果只代表固定单题判分，不等同真实供应商综合模型能力认证

## 保留的跳过项与范围限制

| 用例 | 原因 |
| --- | --- |
| `internal/handler/TestDingTalkOAuthStart_Disabled` | auth_dingtalk_oauth_test.go:21: helper newTestAuthHandlerWithDingTalk added in Task 1.10; sentinel only |
| `internal/service/TestEstimateOpenAIInputTokens_CompareWithOpenAIAPI` | openai_gateway_count_tokens_test.go:314: OPENAI_API_KEY not set |
| `internal/service/TestPluginRuntimeIntegration` | plugin_runtime_integration_test.go:29: 未提供 SUB2API_TEST_PLUGIN_PACKAGE，跳过本地插件进程集成测试 |
| `internal/pkg/tlsfingerprint/TestDialerBasicConnection` | dialer_test.go:160: 跳过网络测试（需要设置 TLSFINGERPRINT_NETWORK_TESTS=1） |
| `internal/pkg/tlsfingerprint/TestJA3Fingerprint` | dialer_test.go:160: 跳过网络测试（需要设置 TLSFINGERPRINT_NETWORK_TESTS=1） |
| `internal/pkg/tlsfingerprint/TestAllProfiles` | dialer_test.go:160: 跳过网络测试（需要设置 TLSFINGERPRINT_NETWORK_TESTS=1） |
| `internal/service/TestAuthPendingIdentityService_UpsertAdoptionDecision_ClearsLegacyNullSessionReference` | auth_pending_identity_service_test.go:365: legacy NULL pending_auth_session_id rows only exist in production PostgreSQL history; sqlite unit schema rejects NULL |
| `internal/repository/TestConcurrencyCacheSuite/TestGetAccountsLoadBatch` | concurrency_cache_integration_test.go:525: TODO: Fix this test - CurrentConcurrency returns 0 instead of expected value in CI |
| `internal/pkg/tlsfingerprint/TestDialerAgainstCaptureServer` | dialer_capture_test.go:48: 跳过外部 TLS 指纹 capture 测试：未设置 TLSFINGERPRINT_CAPTURE_URL |

- unit的3项可选TLS网络测试保留原SKIP；integration另有真实tls.peet.ws通过，它们是不同build-tag源码，结果不互相覆盖。真实模型API key、插件包和外部capture端点未提供的测试保持SKIP。
- 模型检测请求只使用本地fixture，没有真实模型账号或真实OAuth端到端验收。版本/依赖获取、Codex版本同步及既有TLS集成曾访问公网，不描述成全离线运行。
- IQ是固定糖果题的质量信号，不是综合智商或供应商能力认证；关闭IQ后重新路由的真实网关流量未另做浏览器验收，真实IsSchedulable方法的放行/保留其它限制已覆盖。
- 无推送、发布、生产部署或生产数据库操作；升级验证使用隔离资源，源码回滚不等同数据库降级。

## 失败与修正留存

- 修复前失败用例及成功复验均保留，不覆盖历史红灯。验证码闭环测试曾受代理503影响，仅清理测试子进程代理后验证，不改生产代理行为。
- Windows缺sh及IANA时区、Docker命名管道预检、CRLF与Linux工具差异通过测试环境适配处理，不为环境误报修改备份/部署/限流逻辑。
- 外层124、首次布局测试worker上下界冲突、UI证据SQL列名/UTC比较、CI报告断言及引号、最后清理脚本Docker错误文字大小写等失败均在日志保留；已成功步骤复用，未从头重测掩盖。

## 四角色与相同输入回滚

- `MODIFIED_FILE.zip`：修复后的源码与项目文档，不含依赖目录、dist或测试二进制。
- `DIFF_FILE.patch`：相对`AUDIT_BASELINE_v0.0.10.zip`的字节级补丁，不相对旧`BASELINE.zip`。
- `VERIFICATION.txt`：命令、输入、字面stdout/stderr、退出码、哈希、三态回放与所有历史失败/跳过的日志账本。
- 可执行`ROLLBACK.sh`：接收独立源码副本并使用同目录审计基线恢复，保留原项目/修复工作区；不操作数据库。
- 同一`AUDIT_PROBE_INPUTS.json`分别调用真实Grade/ParseHTTP。三种缺陷输入的基线结果是smart；修复预期为unknown/ambiguous_answer、unknown/conflicting_final_response、unknown/duplicate_critical_field；明确21/29及合法分片不变。实际BASELINE/MODIFIED/ROLLBACK输出、恢复哈希、重新应用补丁及四角色重开结果，以`audit-transaction.json`、`FINAL_RESULT.json`及`VERIFICATION.txt`的已执行记录为准。
- 本轮隔离进程及容器已清理，浏览器视口恢复且任务标签页已关闭；证据为`audit-ui/runtime-cleanup.json`及`ui-acceptance.json`。

## 下一步

- 可在本地审阅修复副本；当前没有待自动执行的生产操作。
- 若需要完整浏览器文件交付验收，可手工确认诊断文件最终下载完成；真实供应商账号/插件/macOS等另行提供条件后验证。
- MCP记忆写入接口未提供，本轮仅同步项目文档与本地日志，MCP记忆未同步。
