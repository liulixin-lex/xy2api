<div align="center">

<img src="assets/logo.svg" alt="XY2API" width="112" />

# XY2API

**Self-hosted AI API gateway for subscription and API-key accounts**

[![CI](https://github.com/liulixin-lex/xy2api/actions/workflows/ci.yml/badge.svg)](https://github.com/liulixin-lex/xy2api/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/liulixin-lex/xy2api)](https://github.com/liulixin-lex/xy2api/releases/latest)
[![License: LGPL-3.0](https://img.shields.io/badge/license-LGPL--3.0-blue.svg)](LICENSE)

English | [简体中文](README.zh-CN.md)

</div>

XY2API puts your Claude, OpenAI, Gemini, Grok and other upstream accounts behind one gateway.
Users call it with their own API keys through the native APIs and CLIs they already use; XY2API
handles authentication, account scheduling, failover, usage metering and billing.

> [!IMPORTANT]
> Using this project may violate the terms of service of Anthropic, OpenAI or other upstream
> providers. Read their terms before use; you bear all resulting risk. Use it only where it is
> lawful. The software is provided as is, without warranty; the authors are not liable for account
> suspensions, service interruptions, data loss or any other damage.

## Features

- **Many upstreams, native protocols** — Anthropic Messages, OpenAI Chat Completions and Responses
  (HTTP and WebSocket), Gemini, Grok, Antigravity, plus Kimi, Zhipu GLM, DeepSeek, MiniMax,
  OpenCode Go, Kiro and others. OAuth subscription accounts and API keys are both supported.
- **Works with existing clients** — Claude Code, Codex CLI, Gemini CLI, Grok CLI, OpenCode and any
  OpenAI- or Anthropic-compatible SDK. One-click configuration export for CC Switch.
- **Scheduling and resilience** — strict priority tiers with session takeover, sticky sessions for
  prompt-cache hits, per-account concurrency and rate limits, failover before first output,
  fallback groups, request budgets and CDN-safe keepalives.
- **Quality controls** — per-group quality gates with cooldowns and graded active tests, OpenAI
  session quality routing, periodic model-quality ("IQ") checks and channel monitoring.
- **Billing** — token-level metering including cache reads and writes, per-group rates,
  subscriptions, balances and redeem codes; built-in EasyPay, Alipay, WeChat Pay and Stripe top-up.
- **Administration** — web dashboard, role-based admin permissions, usage export, customer support
  chat and tickets, plugins, and a read-only MCP server for AI-assisted diagnostics.

## Quick start

Requirements: a Linux x86-64 server with Docker 20.10+ and Docker Compose v2.

```bash
mkdir -p xy2api && cd xy2api
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/docker-deploy.sh | bash
docker compose up -d
docker compose logs xy2api | grep "Generated admin"   # first-run admin login
```

Open `http://<server>:8080`, sign in, add an upstream account, create a group and an API key, then
point your client at the gateway. For Claude Code:

```bash
export ANTHROPIC_BASE_URL="http://<server>:8080"
export ANTHROPIC_AUTH_TOKEN="sk-<your-xy2api-key>"
```

See [Getting started](docs/getting-started.md) for the full walkthrough.

## Installation

| Method | Guide |
|---|---|
| Docker Compose (recommended) | [docs/deployment/docker.md](docs/deployment/docker.md) |
| Linux install script with systemd | [docs/deployment/binary.md](docs/deployment/binary.md) |
| Build from source | [docs/deployment/source.md](docs/deployment/source.md) |

Each [release](https://github.com/liulixin-lex/xy2api/releases) ships binaries for
`linux/amd64`, `windows/amd64` and `darwin/arm64`, and the image
`ghcr.io/liulixin-lex/xy2api` for `linux/amd64`. XY2API needs PostgreSQL 15+ and Redis 7+.

Put a reverse proxy in front of it for TLS; see [reverse proxy and CDN](docs/deployment/reverse-proxy.md)
(Nginx needs `underscores_in_headers on;` for Codex sticky sessions). Upgrades run database
migrations automatically; read [upgrade and backup](docs/deployment/upgrade.md) first.
Migrating from Sub2API: [docs/deployment/migrate-from-sub2api.md](docs/deployment/migrate-from-sub2api.md).

## Documentation

The full documentation is in [docs/](docs/README.md) (Simplified Chinese): deployment and
configuration, per-platform setup, feature guides, API references and developer guides.

## Contributing

Bug reports, ideas and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[code of conduct](CODE_OF_CONDUCT.md). Ask questions in
[Discussions](https://github.com/liulixin-lex/xy2api/discussions) and report security issues
privately as described in the [security policy](.github/SECURITY.md).

Changes are recorded in [CHANGELOG.md](CHANGELOG.md).

## License

XY2API is licensed under the [GNU Lesser General Public License v3.0 or later](LICENSE).

XY2API is a fork of [Sub2API](https://github.com/Wei-Shaw/sub2api) by Wesley Liddick.
XY2API modifications are Copyright (c) 2026 liulixin-lex. See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for upstream and third-party attributions.
