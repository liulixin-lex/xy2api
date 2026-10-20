# 文档目录

本目录列出仓库内全部文档及其状态。“现行”文档描述当前版本的行为与操作；“历史记录”只供追溯，不代表现行实现。新增、退役或改名文档时同步更新本目录（`tools/check_docs_consistency.py` 会检查 `docs/` 下每个文件与每个 OpenSpec 变更都已登记）。

## 入口与协作

| 文档 | 说明 |
|---|---|
| [`../AGENTS.md`](../AGENTS.md) | Agent 协作约定：仓库记忆、事实优先级、变更同步 |
| [`../DEV_GUIDE.md`](../DEV_GUIDE.md) | 开发环境、CI 门禁、常见坑点与提交前检查 |
| [`PROJECT_MEMORY.md`](PROJECT_MEMORY.md) | 项目记忆：当前交接状态、进行中的工作、近期操作日志 |
| [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md) | 项目记忆归档：已结束任务的交接状态、登记与较早的操作日志（只读） |
| [`UPSTREAM_SYNC.md`](UPSTREAM_SYNC.md) | Sub2API 上游同步规范（唯一标准流程） |
| [`BRANDING_AND_MIGRATION_COMPATIBILITY.md`](BRANDING_AND_MIGRATION_COMPATIBILITY.md) | 品牌改造与数据兼容边界 |

## 现行功能与运维

| 文档 | 说明 |
|---|---|
| [`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md) | 网关韧性（0.4.0）：失败分类、余额暂停、会话改绑、末位兜底、请求预算、首包前保活、断开计费、错误语义 |
| [`SHARE_SCHEDULING.md`](SHARE_SCHEDULING.md) | 份额调度（0.3.0 起，默认关闭） |
| [`account-scheduling.md`](account-scheduling.md) | 账号调度：日常开关、原版调度规则与 0.4.0 韧性规则 |
| [`openai-cache-reliability.md`](openai-cache-reliability.md) | OpenAI 缓存计量与可靠性 |
| [`CLAUDE_CACHE_FALLBACK.md`](CLAUDE_CACHE_FALLBACK.md) | Claude 自动缓存（按分组） |
| [`OPENAI_QUALITY_ROUTING.md`](OPENAI_QUALITY_ROUTING.md) | OpenAI 会话质量路由 |
| [`OPENAI_IQ_CHECK.md`](OPENAI_IQ_CHECK.md) | OpenAI 智商检测 |
| [`IQ_CURRENT_HEALTH.md`](IQ_CURRENT_HEALTH.md) | 智商检测的当前健康、协议兼容与调度避让 |
| [`CODEX_STATE.md`](CODEX_STATE.md) | Codex STATE 与智商检测 |
| [`CODEX_STATE_RELIABILITY.md`](CODEX_STATE_RELIABILITY.md) | STATE 可靠性控制与策略 |
| [`GPT61_SOL_SUPPORT.md`](GPT61_SOL_SUPPORT.md) | GPT-6.1 Sol 适配（来源清单 [`GPT61_SOL_PROVENANCE.json`](GPT61_SOL_PROVENANCE.json)） |
| [`OPENAI_FAST.md`](OPENAI_FAST.md) | OpenAI 分组 Fast 优先级、全局过滤与档位计费 |
| [`COMPOSITE_GROUPS.md`](COMPOSITE_GROUPS.md) | 组合分组 |
| [`GROUP_SYSTEM_PROMPTS.md`](GROUP_SYSTEM_PROMPTS.md) | 分组标识与系统提示词 |
| [`GROK_MONITOR_RESPONSES.md`](GROK_MONITOR_RESPONSES.md) | Grok 渠道监控的 Responses 模式（0.4.1 起，可选） |
| [`ASYNC_IMAGE_TASKS.md`](ASYNC_IMAGE_TASKS.md) | 异步生图任务 |
| [`BATCH_IMAGE_MVP.md`](BATCH_IMAGE_MVP.md) | Gemini 批量生图 |
| [`seedance-api.md`](seedance-api.md) | Seedance 原生视频 API |
| [`PAYMENT.md`](PAYMENT.md) / [`PAYMENT_CN.md`](PAYMENT_CN.md) | 内置支付配置 |
| [`STRIPE_HOSTED.md`](STRIPE_HOSTED.md) | Stripe 站内与托管模式 |
| [`ADMIN_PAYMENT_INTEGRATION_API.md`](ADMIN_PAYMENT_INTEGRATION_API.md) | 管理端支付集成 API |
| [`USAGE_METRICS.md`](USAGE_METRICS.md) | 用量指标口径 |
| [`USAGE_EXPORTS.md`](USAGE_EXPORTS.md) | 用量记录后台导出 |
| [`BACKUP_RELIABILITY.md`](BACKUP_RELIABILITY.md) | 数据库备份与恢复排障 |
| [`PLUGIN_DEVELOPMENT.md`](PLUGIN_DEVELOPMENT.md) | 插件开发 |
| [`FRONTEND_UX.md`](FRONTEND_UX.md) | 前端性能约定、动效系统与“减少动画”、命令面板等通用交互 |
| [`legal/`](legal/) | 部署与运营合规承诺（中英文） |

部署文档在 `deploy/`：[`README.md`](../deploy/README.md)、[`DOCKER.md`](../deploy/DOCKER.md)、[`EDGE_SECURITY.md`](../deploy/EDGE_SECURITY.md)（含 CDN 识别与可选的边缘标记）、[`APPLE_CONTAINER.md`](../deploy/APPLE_CONTAINER.md)、[`DATAMANAGEMENTD_CN.md`](../deploy/DATAMANAGEMENTD_CN.md)。工具说明见 [`tools/gateway-resilience/README.md`](../tools/gateway-resilience/README.md)、[`skills/`](../skills/)。

## 设计、修复与同步记录

记录当时的设计、验证与取舍；其中的行为描述以对应的现行文档与代码为准。

| 文档 | 说明 |
|---|---|
| [`BILLING_INTEGRITY_REPAIR.md`](BILLING_INTEGRITY_REPAIR.md) | 计费完整性修复规范与验收（2026-10-07，来源清单 [`BILLING_INTEGRITY_PROVENANCE.json`](BILLING_INTEGRITY_PROVENANCE.json)） |
| [`channel-monitor-v2-safe-defaults.md`](channel-monitor-v2-safe-defaults.md) | 渠道监控 V2 安全默认值与回填设计 |
| [`SCHEDULING_ANALYSIS.md`](SCHEDULING_ANALYSIS.md) | 2026-10-09 调度现状分析与外部调研（份额调度的前置调研） |
| [`UPSTREAM_SYNC_V0214.md`](UPSTREAM_SYNC_V0214.md) / [`UPSTREAM_SYNC_V0215.md`](UPSTREAM_SYNC_V0215.md) | Sub2API v0.2.9–v0.2.15 同步记录 |
| [`upstream-sync/`](upstream-sync/) | 每次上游同步的文件矩阵与影响报告（`sync.py report` 生成） |
| [`third-party/`](third-party/) | 第三方来源与许可说明 |
| [`screenshots/`](screenshots/) | 文档引用的截图 |

## 历史记录（已退役或被取代）

| 文档 | 说明 |
|---|---|
| [`CONTROLLED_SCHEDULING.md`](CONTROLLED_SCHEDULING.md) / [`CONTROLLED_SCHEDULING_ACCEPTANCE.md`](CONTROLLED_SCHEDULING_ACCEPTANCE.md) | 智能可控调度，0.3.0 退役，由份额调度取代 |
| [`ui-scheduling-dual-mode.md`](ui-scheduling-dual-mode.md) / [`ui-scheduling-one-click.md`](ui-scheduling-one-click.md) | 已退役调度方案的界面设计 |
| [`IQ_RELIABILITY_IMPLEMENTATION.md`](IQ_RELIABILITY_IMPLEMENTATION.md) | 智商检测早期实施记录，现行说明见 `OPENAI_IQ_CHECK.md` |
| [`AUDIT_v0.0.10.md`](AUDIT_v0.0.10.md) | v0.0.10 本地代码审计 |
| [`V021_ISSUE_SUMMARY.md`](V021_ISSUE_SUMMARY.md) / [`V021_RELIABILITY_FIX.md`](V021_RELIABILITY_FIX.md) / [`V021_REMEDIATION_PLAN.md`](V021_REMEDIATION_PLAN.md) / [`ISSUES_AND_CHANGES.md`](ISSUES_AND_CHANGES.md) | v0.2.1 可靠性修复记录 |

## OpenSpec 变更（`openspec/changes/`）

| 变更 | 内容 | 状态 |
|---|---|---|
| [`gateway-resilience-20261015`](../openspec/changes/gateway-resilience-20261015/) | 网关韧性 | 随 0.4.0 交付；部署与 0.4.1 后续见其 tasks |
| [`replace-controlled-with-share-scheduling`](../openspec/changes/replace-controlled-with-share-scheduling/) | 剔除可控调度、引入份额调度 | 0.3.0 已发布，默认关闭；影子运行与灰度待做 |
| [`async-image-tasks-v2`](../openspec/changes/async-image-tasks-v2/) | 异步生图任务 v2 | 0.3.0 已发布 |
| [`add-claude-cache-fallback`](../openspec/changes/add-claude-cache-fallback/) | Claude 自动缓存 | 0.2.4 起发布；真实上游账单核验未做 |
| [`stabilize-openai-iq-check`](../openspec/changes/stabilize-openai-iq-check/) | 智商检测稳定性 | 已完成 |
| [`iq-detection-operations`](../openspec/changes/iq-detection-operations/) | 智商检测运营 | 已结束（线上验收按用户决定不再执行） |
| [`add-openai-compatible-prompt-audit`](../openspec/changes/add-openai-compatible-prompt-audit/) | 提示词审计 | 上游 Sub2API 的变更，随同步引入 |
| [`codex-manifest-pinned-accounts`](../openspec/changes/codex-manifest-pinned-accounts/) | Codex manifest 固定账号 | 上游 Sub2API 的变更，随同步引入 |
