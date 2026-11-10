<div align="center">

<img src="assets/logo.svg" alt="XY2API" width="112" />

# XY2API

**自托管的 AI API 中转网关，统一接入订阅账号与 API Key**

[![CI](https://github.com/liulixin-lex/xy2api/actions/workflows/ci.yml/badge.svg)](https://github.com/liulixin-lex/xy2api/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/liulixin-lex/xy2api)](https://github.com/liulixin-lex/xy2api/releases/latest)
[![License: LGPL-3.0](https://img.shields.io/badge/license-LGPL--3.0-blue.svg)](LICENSE)

[English](README.md) | 简体中文

</div>

XY2API 把你的 Claude、OpenAI、Gemini、Grok 等上游账号统一接到一个网关后面。用户用自己的 API Key，通过原生 API 和熟悉的命令行工具调用；鉴权、账号调度、故障切换、用量计量和计费由 XY2API 完成。

> [!IMPORTANT]
> 使用本项目可能违反 Anthropic、OpenAI 等上游服务商的服务条款，请在使用前阅读相关协议，由此产生的一切风险由使用者自行承担。请仅在所在国家或地区法律允许的范围内使用。本软件按“现状”提供，不附带任何担保；对账号封禁、服务中断、数据丢失或其他任何损失，作者不承担责任。

## 功能

- **多上游、原生协议**：Anthropic Messages、OpenAI Chat Completions 与 Responses（HTTP 与 WebSocket）、Gemini、Grok、Antigravity，以及 Kimi、智谱 GLM、DeepSeek、MiniMax、OpenCode Go、Kiro 等；同时支持 OAuth 订阅账号和 API Key。
- **直接用现有客户端**：Claude Code、Codex CLI、Gemini CLI、Grok CLI、OpenCode，以及任何兼容 OpenAI 或 Anthropic 的 SDK；支持一键导入 CC Switch。
- **调度与韧性**：粘性会话提高提示词缓存命中，按用户和账号限制并发与速率，可选份额调度，首个输出前自动换号，兜底分组，请求预算，经过 CDN 也不断流的保活。
- **质量控制**：分组质量门槛、OpenAI 会话质量路由、定期模型质量（智商）检测、渠道监控。
- **计费**：按 Token 精确计量（含缓存读写），分组倍率、订阅、余额与兑换码；内置易支付、支付宝、微信支付和 Stripe 充值。
- **管理**：Web 管理后台、普通管理员分级权限、用量导出、在线客服与工单、插件，以及供 AI 辅助排障的只读 MCP。

## 快速开始

需要一台装有 Docker 20.10+ 和 Docker Compose v2 的 Linux x86-64 服务器。

```bash
mkdir -p xy2api && cd xy2api
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/docker-deploy.sh | bash
docker compose up -d
docker compose logs xy2api | grep "Generated admin"   # 首次启动生成的管理员账号
```

浏览器打开 `http://<服务器>:8080` 登录，添加上游账号，创建分组和 API Key，再把客户端指向网关。以 Claude Code 为例：

```bash
export ANTHROPIC_BASE_URL="http://<服务器>:8080"
export ANTHROPIC_AUTH_TOKEN="sk-<你的 XY2API 密钥>"
```

完整步骤见 [快速开始](docs/getting-started.md)。

## 安装

| 方式 | 说明 |
|---|---|
| Docker Compose（推荐） | [docs/deployment/docker.md](docs/deployment/docker.md) |
| Linux 安装脚本（systemd） | [docs/deployment/binary.md](docs/deployment/binary.md) |
| 从源码构建 | [docs/deployment/source.md](docs/deployment/source.md) |

每个 [Release](https://github.com/liulixin-lex/xy2api/releases) 提供 `linux/amd64`、`windows/amd64`、`darwin/arm64` 三个平台的二进制包，以及 `linux/amd64` 镜像 `ghcr.io/liulixin-lex/xy2api`。XY2API 依赖 PostgreSQL 15+ 和 Redis 7+。

对外提供服务时请在前面加反向代理处理 TLS，见 [反向代理与 CDN](docs/deployment/reverse-proxy.md)（使用 Nginx 时需要 `underscores_in_headers on;`，否则 Codex 的粘性会话会失效）。升级时会自动执行数据库迁移，升级前请先阅读 [升级与备份](docs/deployment/upgrade.md)。从 Sub2API 迁移见 [docs/deployment/migrate-from-sub2api.md](docs/deployment/migrate-from-sub2api.md)。

## 文档

完整文档在 [docs/](docs/README.md)：部署与配置、各平台接入、功能指南、API 参考和开发者指南。

## 参与贡献

欢迎提交问题、建议和 Pull Request，请先阅读 [贡献指南](CONTRIBUTING.md) 和 [行为准则](CODE_OF_CONDUCT.md)。使用问题请到 [Discussions](https://github.com/liulixin-lex/xy2api/discussions) 提问，安全漏洞请按 [安全策略](.github/SECURITY.md) 私密报告。

版本变更记录见 [CHANGELOG.md](CHANGELOG.md)。

## 许可证

XY2API 以 [GNU 宽通用公共许可证 v3.0 或更高版本](LICENSE) 授权。

XY2API 是 Wesley Liddick 的 [Sub2API](https://github.com/Wei-Shaw/sub2api) 的派生项目，XY2API 修改部分 Copyright (c) 2026 liulixin-lex。上游与第三方来源见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
