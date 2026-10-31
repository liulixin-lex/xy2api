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
| [`SHARE_SCHEDULING.md`](SHARE_SCHEDULING.md) | 份额调度（0.3.0 起，默认关闭）：层级取账号优先级、权重取负载因子与自适应系数 |
| [`account-scheduling.md`](account-scheduling.md) | 账号调度：日常开关、原版调度规则与 0.4.0 韧性规则 |
| [`openai-cache-reliability.md`](openai-cache-reliability.md) | OpenAI 缓存计量与可靠性 |
| [`CLAUDE_CACHE_FALLBACK.md`](CLAUDE_CACHE_FALLBACK.md) | Claude 自动缓存（按分组） |
| [`ANTHROPIC_CACHE_TTL.md`](ANTHROPIC_CACHE_TTL.md) | Claude 缓存 TTL：管理员默认、Key 偏好、最终请求、计费与迁移 |
| [`OPENAI_QUALITY_ROUTING.md`](OPENAI_QUALITY_ROUTING.md) | OpenAI 会话质量路由 |
| [`OPENAI_IQ_CHECK.md`](OPENAI_IQ_CHECK.md) | OpenAI 智商检测：按“账号 × 模型”检测与冷却、参数化题目、自报模型识别、升级与回退 |
| [`CODEX_STATE.md`](CODEX_STATE.md) | Codex STATE 与智商检测（按出站模型联动） |
| [`CODEX_STATE_RELIABILITY.md`](CODEX_STATE_RELIABILITY.md) | STATE 可靠性控制与策略 |
| [`GPT61_SOL_SUPPORT.md`](GPT61_SOL_SUPPORT.md) | GPT-6.1 Sol 适配（来源清单 [`GPT61_SOL_PROVENANCE.json`](GPT61_SOL_PROVENANCE.json)） |
| [`OPENAI_FAST.md`](OPENAI_FAST.md) | OpenAI 分组 Fast 优先级、全局过滤与档位计费 |
| [`COMPOSITE_GROUPS.md`](COMPOSITE_GROUPS.md) | 组合分组 |
| [`GROUP_SYSTEM_PROMPTS.md`](GROUP_SYSTEM_PROMPTS.md) | 分组标识与系统提示词 |
| [`GROUP_CATEGORIES.md`](GROUP_CATEGORIES.md) | 分组分类（管理端整理，不影响调度、计费和用户端） |
| [`UPSTREAM_BALANCE.md`](UPSTREAM_BALANCE.md) | 账号页「上游余额」列：sub2api 与 new-api 的识别和口径、每分钟采集、对上游的影响、new-api 访问令牌 |
| [`ADMIN_PERMISSIONS.md`](ADMIN_PERMISSIONS.md) | 普通管理员与后台权限：模块「查看 / 管理」、资金与数据开关、显示上游地址与网络设置、只有超级管理员能做的事、保护措施与原因码、迁移 282 与回退 |
| [`SUPPORT_TICKETS.md`](SUPPORT_TICKETS.md) | 网站在线工单：功能开关、用户归属、管理员权限、消息限制与数据保留 |
| [`OPENAI_TOTP.md`](OPENAI_TOTP.md) | OpenAI 账号 2FA 直接登录导入、专用加密凭据、轮换/恢复与安全导出；[Worker 部署](../deploy/OPENAI_TOTP_WORKER.md) |
| [`CHANNEL_MONITOR_V1.md`](CHANNEL_MONITOR_V1.md) | 渠道监控 v1：判定口径（10 秒黄、30 秒红）、流式探测与请求形状、探测选号、调度可靠性 |
| [`GROK_MONITOR_RESPONSES.md`](GROK_MONITOR_RESPONSES.md) | Grok 渠道监控的 Responses 模式（0.4.1 起，可选） |
| [`channel-monitor-v2-safe-defaults.md`](channel-monitor-v2-safe-defaults.md) | 渠道监控 V2 模式（`channel_monitor_mode` 可选）：安全默认值与回填设计 |
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

部署文档在 `deploy/`：[`README.md`](../deploy/README.md)、[`DOCKER.md`](../deploy/DOCKER.md)、[`EDGE_SECURITY.md`](../deploy/EDGE_SECURITY.md)（含 CDN 识别与可选的边缘标记）、[`APPLE_CONTAINER.md`](../deploy/APPLE_CONTAINER.md)、[`OPENAI_TOTP_WORKER.md`](../deploy/OPENAI_TOTP_WORKER.md)。工具说明见 [`tools/gateway-resilience/README.md`](../tools/gateway-resilience/README.md)、[`skills/`](../skills/)。

## 设计、修复与同步记录

记录当时的设计、验证与取舍；其中的行为描述以对应的现行文档与代码为准。

| 文档 | 说明 |
|---|---|
| [`UPSTREAM_SYNC_V0214.md`](UPSTREAM_SYNC_V0214.md) / [`UPSTREAM_SYNC_V0215.md`](UPSTREAM_SYNC_V0215.md) | Sub2API v0.2.9–v0.2.15 同步记录 |
| [`upstream-sync/`](upstream-sync/) | 上游同步的文件矩阵与影响报告（`sync.py report` 生成）；只保留与 `UPSTREAM_BASE.json` 对应的当前基线报告，更早的报告可从 Git 历史查阅 |
| [`third-party/`](third-party/) | 第三方来源与许可说明 |

## 历史记录

已退役或被取代的设计与修复记录（可控调度、早期智商检测、v0.0.10 审计、v0.2.1 可靠性修复、计费完整性修复、调度前置调研等）已于 2026-10-31 从仓库删除，需要追溯时查 Git 历史；结论摘要在 [`PROJECT_MEMORY_ARCHIVE.md`](PROJECT_MEMORY_ARCHIVE.md)。

## OpenSpec 变更（`openspec/changes/`）

已发布且由现行文档完整覆盖的变更（普通管理员角色、上游余额、异步生图 v2、早期智商检测两项）已于 2026-10-31 删除，可从 Git 历史查阅。

| 变更 | 内容 | 状态 |
|---|---|---|
| [`anthropic-cache-ttl-20261030`](../openspec/changes/anthropic-cache-ttl-20261030/) | Anthropic TTL、Key 四态、usage/计费/预留及旧策略退役 | 随 0.4.15 发布（PR #170）；生产未部署，真实供应商缓存收益未验证 |
| [`gateway-resilience-045-20261021`](../openspec/changes/gateway-resilience-045-20261021/) | 网关韧性 0.4.5：RA-1 修复、OpenAI Responses 透传与 Gateway 入站经 Anthropic 上游的明确终止、审查遗留 P2 | 0.4.5、0.4.6 已发布（生产未部署）；后续待办见其 tasks §7 |
| [`gateway-resilience-v2-20261021`](../openspec/changes/gateway-resilience-v2-20261021/) | 网关韧性第二期：可重放前缀与推迟开场、分类修正、失败帧重试提示、仅流式能力学习、事件字段 | 0.4.4 已发布；已知缺陷 RA-1 已在 0.4.5 修复（`gateway-resilience-045-20261021`）；`response.metadata` 的开场判据于 2026-10-22 修订（0.4.6，见 045 tasks 7.7）；浸泡见其 tasks |
| [`simplify-share-scheduling-20261020`](../openspec/changes/simplify-share-scheduling-20261020/) | 精简份额调度：层级取账号优先级、权重取负载因子与自适应系数，每组只剩调度方式与会话保持 | 随 0.4.4 发布（PR #117） |
| [`gateway-resilience-20261015`](../openspec/changes/gateway-resilience-20261015/) | 网关韧性 | 随 0.4.0 交付，0.4.1、0.4.2 跟进；部署与后续见其 tasks |
| [`replace-controlled-with-share-scheduling`](../openspec/changes/replace-controlled-with-share-scheduling/) | 剔除可控调度、引入份额调度 | 0.3.0 已发布，默认关闭；份额规则由 `simplify-share-scheduling-20261020` 精简，影子运行不再执行 |
| [`add-claude-cache-fallback`](../openspec/changes/add-claude-cache-fallback/) | Claude 自动缓存 | 0.2.4 起发布；真实上游账单核验未做 |
| [`add-openai-compatible-prompt-audit`](../openspec/changes/add-openai-compatible-prompt-audit/) | 提示词审计 | 上游 Sub2API 的变更，随同步引入 |
| [`add-support-tickets-and-managed-openai-totp-20261029`](../openspec/changes/add-support-tickets-and-managed-openai-totp-20261029/) | 网站工单与托管 OpenAI 账号 TOTP 管理 | 随 0.4.14 发布（PR #168）；真实 OpenAI 登录/MFA 未验证 |
| [`codex-manifest-pinned-accounts`](../openspec/changes/codex-manifest-pinned-accounts/) | Codex manifest 固定账号 | 上游 Sub2API 的变更，随同步引入 |
