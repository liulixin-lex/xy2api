# XY2API 文档

第一次使用请从 [快速开始](getting-started.md) 读起：部署、登录后台、接入上游账号、发放 API Key，再把客户端指向网关。

## 部署与运维

| 文档 | 内容 |
|---|---|
| [Docker Compose](deployment/docker.md) | 推荐的部署方式：一键脚本、手动部署、数据目录、常用命令 |
| [Linux 安装脚本](deployment/binary.md) | 二进制安装、systemd 服务、指定版本安装、卸载 |
| [从源码构建](deployment/source.md) | 构建前端与内嵌前端的单一二进制 |
| [配置](deployment/configuration.md) | 配置文件、环境变量、首个管理员账号、简易模式 |
| [反向代理与 CDN](deployment/reverse-proxy.md) | Nginx、Caddy、Cloudflare 等前置代理的必要设置与边缘安全 |
| [升级、备份与恢复](deployment/upgrade.md) | 在线更新、数据库迁移、回滚、备份恢复 |
| [从 Sub2API 迁移](deployment/migrate-from-sub2api.md) | 复用 Sub2API 的数据与配置 |
| [OpenAI TOTP Worker](deployment/openai-totp-worker.md) | 托管 OpenAI 账号 2FA 登录所需的 Worker |

## 上游平台

| 平台 | 内容 |
|---|---|
| [Anthropic（Claude）](platforms/anthropic.md) | OAuth 与 API Key 账号、Claude Code 兼容模式 |
| [OpenAI / Codex](platforms/openai.md) | OAuth 与 API Key 账号、Codex CLI、Responses WebSocket、模型支持 |
| [Gemini](platforms/gemini.md) | Code Assist / AI Studio OAuth 与 API Key |
| [Grok / xAI](platforms/grok.md) | 订阅 OAuth 与 API Key、Grok CLI、图片与视频 |
| [Antigravity](platforms/antigravity.md) | 专用端点与混合调度 |
| [TypeSafe / Jev](platforms/typesafe.md) | System One 原生协议 |
| [其他平台](platforms/others.md) | Kimi、智谱 GLM、DeepSeek、MiniMax、OpenCode Go、Command Code、Cline |

## 功能指南

| 主题 | 文档 |
|---|---|
| 调度与可用性 | [账号调度](guides/scheduling.md) · [网关韧性](guides/gateway-resilience.md)（[设置](guides/gateway-resilience-settings.md) · [罚则与选号](guides/gateway-resilience-routing.md) · [客户端行为](guides/gateway-resilience-client.md) · [观测](guides/gateway-resilience-observability.md)） · [分组质量门槛](guides/group-quality-gate.md) |
| 分组与模型 | [分组](guides/groups.md) · [提示词缓存](guides/caching.md) · [智商检测](guides/iq-check.md) · [模型降档识别](guides/quality-routing.md) · [Codex STATE](guides/codex-state.md) · [渠道监控](guides/channel-monitor.md) |
| 计费与运营 | [内置支付](guides/payment.md)（[English](guides/payment.en.md)） · [用量指标与导出](guides/usage.md) · [上游余额](guides/upstream-balance.md) · [在线客服与工单](guides/customer-support.md) |
| 管理与集成 | [管理员与权限](guides/admin-permissions.md) · [OpenAI 账号 2FA 导入](guides/openai-totp.md) · [只读 MCP](guides/mcp.md) |

## API 参考

| 文档 | 内容 |
|---|---|
| [异步图片任务](api/images.md) | OpenAI、Grok 生图与改图的异步提交和轮询 |
| [批量生图](api/batch-images.md) | Gemini Batch API 与 Vertex 批量生成 |
| [Seedance 视频](api/seedance.md) | Seedance 原生视频接口 |
| [支付集成](api/payment-integration.md) | 管理端支付集成接口 |

网关对外提供的 Anthropic、OpenAI、Gemini 兼容接口与官方协议一致，各平台支持的端点见上方平台文档。

## 开发

| 文档 | 内容 |
|---|---|
| [架构](development/architecture.md) | 代码结构与请求处理流程 |
| [本地开发](development/local-setup.md) | 环境准备、运行、测试、代码生成 |
| [插件开发](development/plugins.md) | 插件 API、打包与安全模型 |
| [前端约定](development/frontend.md) | 性能约定、动效与通用交互 |

参与贡献请先阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 合规

[部署与运营合规承诺](legal/admin-compliance.zh.md)（[English](legal/admin-compliance.en.md)）
