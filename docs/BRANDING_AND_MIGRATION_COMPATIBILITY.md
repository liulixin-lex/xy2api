# XY2API 品牌与迁移兼容边界

更新日期：2026-09-01

本文档用于避免后续同步上游或继续品牌改造时，误把数据协议和持久化标识一起重命名。

## 应改为 XY2API 的内容

- GitHub 仓库、Issue、Release、安装脚本和更新检查地址。
- Go module/import 路径、容器镜像、发布压缩包、二进制和 systemd 服务名称。
- 页面标题、Logo、默认站点名、WebAuthn/TOTP 展示名称和文档中的产品名称。
- 新安装的默认数据库名 `xy2api`、Dashboard Redis 缓存前缀 `xy2api:` 和日志服务名。
- 新版本专用的临时目录、安装目录和数据管理 socket。

这些名称属于代码归属、发布渠道、新安装默认值或展示品牌，不应继续依赖原仓库。

## 必须保留 Sub2API 标识的内容

以下标识已经成为数据库、缓存、浏览器状态或外部接口契约。直接重命名会导致启动失败、旧数据不可读、滚动升级不兼容或插件失效：

- 所有已经发布的 SQL migration 内容。尤其是 `001_init.sql`、`002_account_type_migration.sql`、`003_subscription.sql`，连注释也不能修改。
- 已存在的数据库表、索引和约束名，例如 `sub2api_plugin_installations` 和 `sub2api_plugin_bindings`。
- HTTP 兼容接口 `/v1/sub2api/billing` 及响应对象 `sub2api.key_billing`。
- 插件 v1 的 gRPC package、manifest JSON 字段、magic cookie、UI bridge 消息和 `.s2plugin` 包格式。
- 账号导入导出类型 `sub2api-data`、`sub2api-bundle`。
- 已投入使用的 Redis key、浏览器 localStorage/sessionStorage key、跨标签页刷新锁和管理端 WebSocket subprotocol。
- 用于稳定 UUID、请求标记、工具别名、签名/KDF 的命名空间；更名会改变计算结果或破坏在途会话。
- xAI OAuth 的 `referrer=sub2api`、旧 Grok Header 和其他已验证的第三方集成标识。
- 旧配置目录 `/etc/sub2api` 和旧发布包中的 `sub2api` 二进制名，作为升级兼容入口保留。

保留这些值不代表运行时仍依赖原 Sub2API 仓库；它们是 XY2API 对既有数据和客户端承担的兼容契约。

## 现有环境切换原则

1. 首次切换只替换应用镜像或二进制，不同时改数据库名、表名、Redis DB、数据目录和密钥。
2. 现有环境应显式继续使用原数据库名（例如 `sub2api`）；代码中的 `xy2api` 默认值仅用于全新安装。
   - 原 Compose 命名卷部署：先用 `docker volume ls` 找到旧应用数据卷的完整物理名称，再在 `.env` 中设置 `APP_DATA_VOLUME_NAME=<完整物理卷名>`。旧配置没有显式 `name:` 时，名称通常是 `<旧 Compose 项目名>_sub2api_data`，例如 `deploy_sub2api_data` 或 `sub2api_sub2api_data`，不能直接假定为裸的 `sub2api_data`。可再用 `docker volume inspect <完整物理卷名>` 核对挂载点和标签。
   - 原宿主机目录部署：首次切换继续保持 `/var/lib/sub2api/production-app:/app/data`，确认迁移完成后再单独规划目录改名。
3. 保留原 JWT、TOTP/支付加密密钥、数据库 SSL 配置、Redis 密码与 DB 编号，否则登录态、加密配置或支付恢复令牌会失效。
4. 保留原价格同步链路：默认从 `Wei-Shaw/model-price-repo` 拉取基于 LiteLLM `model_prices_and_context_window.json` 的镜像数据，并通过配套 SHA-256 文件校验；远程不可用时继续使用应用数据目录中的缓存或随版本发布的本地回退文件。该仓库是价格数据源，不属于 XY2API 代码、镜像或更新渠道。
5. 并行部署时使用独立容器名、监听端口、日志路径和应用数据目录。两个版本可以指向同一数据库做短时兼容验证，但不应让不同 schema 版本长期同时执行迁移或后台写任务。
6. 切流前先备份数据库和 Redis；切流后验证登录、API Key、计费、定价、OAuth、插件、定时任务和管理端实时连接，再停止旧实例。
7. 回滚时恢复旧镜像和旧流量入口即可，不修改 `schema_migrations` checksum，也不回写历史 migration。

## 启动阻断防回归

`backend/migrations/checksums.json` 固定校验当前全部 273 个已发布 migration，规范化算法与启动运行器相同：先按 UTF-8 解码并执行 `strings.TrimSpace`，再计算 SHA-256。

`backend/migrations/branding_compatibility_test.go` 会校验 SQL 文件集合与 manifest 完全一致，并逐个比对 hash。任何品牌改造或上游同步导致既有 SQL 变化，都必须被视为数据兼容性回归；新 migration 只能追加 SQL 和新的 checksum 条目。

## 产品版本与兼容基线

XY2API 产品版本和 Sub2API 兼容基线是两个独立概念：

- `backend/cmd/server/VERSION` 表示 XY2API 产品发布版本。
- `backend/cmd/server/SUB2API_COMPAT_VERSION` 表示完成同步和兼容审计的 Sub2API 基线。
- 插件 v1 继续使用原有 `requires.sub2api`、`recommended_sub2api_version` 和 `tested_sub2api_versions` JSON 字段，并与兼容基线比较。
- 插件兼容响应保留 `current_sub2api_version`，同时返回 `current_xy2api_version`，避免现有客户端被破坏。

发布工作流只校验 tag 与产品版本文件一致，不再在发布时修改源码或在发布后回写 `main`。

## 现有部署升级预检

切换镜像前执行只读预检：

```bash
python tools/xy2api_upgrade_preflight.py --env-file deploy/.env --compose-file deploy/docker-compose.yml
```

工具检查旧数据库名、Redis DB 与前缀、稳定密钥、Compose 命名卷、bind mount 和常见 systemd 或数据目录。发现不明确或多候选时返回非零状态并阻止升级，不会自动修改配置或搬移数据。

完整上游同步与发布流程见 `docs/UPSTREAM_SYNC.md`。
