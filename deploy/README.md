# deploy/

这个目录存放部署 XY2API 用到的脚本、Compose 文件与配置示例。部署步骤与说明见 [`docs/deployment/`](../docs/deployment/)，第一次部署从 [快速开始](../docs/getting-started.md) 开始。

| 文件 | 用途 |
| --- | --- |
| [`docker-deploy.sh`](docker-deploy.sh) | Docker 部署准备脚本：下载 `docker-compose.local.yml` 与 `.env.example`，生成密钥与数据目录，见 [Docker Compose 部署](../docs/deployment/docker.md) |
| [`docker-compose.local.yml`](docker-compose.local.yml) | XY2API + PostgreSQL + Redis，数据存放在当前目录（`docker-deploy.sh` 使用的版本） |
| [`docker-compose.yml`](docker-compose.yml) | XY2API + PostgreSQL + Redis，数据存放在 Docker 命名卷 |
| [`docker-compose.standalone.yml`](docker-compose.standalone.yml) | 只运行 XY2API，连接外部 PostgreSQL 与 Redis |
| [`docker-compose.dev.yml`](docker-compose.dev.yml) | 从本地源码构建并运行，仅用于开发测试，见 [从源码构建](../docs/deployment/source.md) |
| [`.env.example`](.env.example) | Compose 部署的环境变量模板，复制为 `.env` 使用 |
| [`docker-entrypoint.sh`](docker-entrypoint.sh) | 镜像入口脚本：修正 `/app/data` 属主后以 `xy2api` 用户启动程序 |
| [`install.sh`](install.sh) | Linux + systemd 安装、升级、回滚与卸载脚本，见 [Linux 安装脚本与 systemd](../docs/deployment/binary.md) |
| [`xy2api.service`](xy2api.service) | systemd 单元参考文件 |
| [`config.example.yaml`](config.example.yaml) | 带注释的完整配置示例，见 [配置文件与环境变量](../docs/deployment/configuration.md) |
| [`Caddyfile`](Caddyfile) | 客户端直连 Caddy 时的反向代理基线配置，见 [反向代理、CDN 与边缘安全](../docs/deployment/reverse-proxy.md) |
| [`codex-instructions.md.tmpl`](codex-instructions.md.tmpl) | `gateway.forced_codex_instructions_template_file` 可用的 Codex 指令模板示例 |
| [`openai-totp-worker.env.example`](openai-totp-worker.env.example) | OpenAI TOTP Worker 的环境变量模板，见 [OpenAI TOTP Worker 部署](../docs/deployment/openai-totp-worker.md) |
| [`xy2api-openai-totp-worker.service`](xy2api-openai-totp-worker.service) | OpenAI TOTP Worker 的 systemd 单元示例 |
| [`mcp-reader-grants.sql`](mcp-reader-grants.sql) | 只读 MCP 专用数据库账号的字段级 `SELECT` 授权，见 [只读 MCP](../docs/guides/mcp.md) |
| [`mcp-reader-function-privileges.sql`](mcp-reader-function-privileges.sql) | 只读 MCP 的函数权限收紧脚本，保留应用写入账号的权限 |
| [`tests/`](tests/) | 安装脚本、Compose 文件与 Caddyfile 的检查脚本 |
| [`.gitignore`](.gitignore) | 忽略运行时数据目录、`.env` 与备份文件 |
