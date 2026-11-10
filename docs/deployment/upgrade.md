# 升级、回滚与备份

本文说明各种部署方式如何升级和回退 XY2API、启动时的数据库迁移如何工作，以及用管理后台或命令行备份、恢复数据库的方法与排障。负责维护 XY2API 实例的运维人员在每次升级前都应看一遍。

## 升级前

1. 阅读目标版本的发布说明（GitHub Releases 与仓库根目录的 `CHANGELOG.md`），确认是否有需要提前处理的兼容性变化。
2. 备份数据库，见下文 [备份与恢复](#备份与恢复)。同时保存 `.env`、`config.yaml` 和应用数据目录。
3. 选择低峰时段。新版本的数据库迁移在启动时执行，涉及大表结构变更时会短暂锁表。
4. 程序内嵌前端，前后端总是同一版本，不需要单独升级前端。

## Docker Compose

生产环境建议在 Compose 文件里固定版本号，而不是使用 `latest`：

```yaml
services:
  xy2api:
    image: ghcr.io/liulixin-lex/xy2api:0.6.1
```

升级：

```bash
docker compose pull
docker compose up -d
docker compose logs -f xy2api      # 观察迁移与启动日志
curl -s http://localhost:8080/ready
```

用了 `-f` 指定文件的部署，两条命令都要带上相同参数。

管理后台的在线更新在容器里只替换容器内的程序，镜像和 Compose 文件仍是旧版本；容器重建（修改配置后 `up -d`、删除容器、迁移主机）时会回到旧版本。容器里执行在线更新时，后台会提示“当前运行在容器中：请改为更新镜像”，日志记录 `system.online_update_in_container`。容器部署请始终通过更换镜像升级。

## 安装脚本（systemd）

```bash
# 升级到最新版本
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- upgrade
# 升级到指定版本
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- upgrade --version 0.6.1
```

脚本停止服务，把当前程序备份为 `/opt/xy2api/xy2api.backup`（指定版本时为 `xy2api.backup.<原版本>`），下载并校验新版本后重新启动。

也可以在管理后台的版本信息中检查并“立即更新”。在线更新下载与当前平台匹配的发布包，替换程序后重启：进程先停止接收新连接，最多等待 `server.restart_drain_seconds`（默认 30 秒，范围 0–600，0 为立即退出；环境变量 `SERVER_RESTART_DRAIN_SECONDS`）让在途请求完成，再以退出码 0 退出，由 systemd 重新拉起。因此服务需要 `Restart=always`（安装脚本写入的单元即如此），`Restart=on-failure` 不会拉起。等待期间新请求被拒绝，WebSocket 连接会断开；反向代理按 [反向代理](reverse-proxy.md) 配置了 JSON 错误页和 `/ready` 健康检查时，客户端得到的是可重试的 503。

在线更新同样受发布平台限制：没有对应发布包的平台（例如 ARM Linux）会提示找不到兼容的版本。

## 源码构建

拉取新代码或切到新标签，重新执行 `make build`，替换程序后重启服务，见 [从源码构建](source.md)。源码构建的程序不提供在线更新。

## 数据库迁移

- 迁移文件随程序内嵌，每次启动时自动按文件名顺序执行尚未执行过的迁移，无需手动操作。
- 已执行的迁移记录在 `schema_migrations` 表（文件名、校验和、执行时间）。已执行迁移的内容被改动时校验和不一致，程序拒绝启动。
- 迁移通过 PostgreSQL advisory lock 串行执行：多个实例同时启动时只有一个在迁移，其余等待。
- 普通迁移在事务中执行；文件名以 `_notx.sql` 结尾的迁移用于 `CREATE INDEX CONCURRENTLY` 这类语句，不包在事务里。
- 迁移只向前执行，没有自动的降级脚本。
- 首次初始化时迁移的超时由 `SETUP_MIGRATION_TIMEOUT_SECONDS` 控制（默认 60 秒）；正常启动时迁移最长 10 分钟。
- 启动时数据库还在恢复（例如刚重启）会自动重试，见 [Docker Compose 部署](docker.md#健康检查与启动恢复)。

查看迁移状态：

```sql
SELECT filename, applied_at FROM schema_migrations ORDER BY filename DESC LIMIT 10;
```

不要手动修改或删除 `schema_migrations` 中的记录。

## 回滚

| 部署方式 | 方法 |
| --- | --- |
| Docker Compose | 把 `image:` 改回原版本号，`docker compose up -d` |
| 安装脚本 | `curl -sSL <install.sh 地址> \| sudo bash -s -- rollback 0.6.0`；或手动把 `xy2api.backup*` 复制回 `/opt/xy2api/xy2api` 后重启 |
| 管理后台 | 版本信息中的“版本回退”，可选最近 3 个更早的版本，需要重启生效。源码构建不支持；容器里与在线更新一样只是临时替换，请改用镜像回退 |

回滚只替换程序，数据库保持升级后的结构：旧版本启动时只检查自己携带的迁移，新版本加的表和列会留在库里。大多数情况下旧版本可以直接运行；发布说明标明不兼容，或回退后行为异常时，停止服务，从升级前的备份恢复数据库，再启动旧版本。

## 备份与恢复

### 管理后台的数据库备份

在“系统设置 → 数据备份”中配置。备份导出为压缩的 SQL 文件，上传到 S3 兼容的对象存储（例如 Cloudflare R2）：

1. 填写“S3 存储配置”（端点、区域、存储桶、Key 前缀、访问密钥），“测试连接”后保存。
2. 选择“备份范围”。
3. 需要定时备份时，在“备份策略”中启用并填写 Cron 表达式（例如 `0 2 * * *`）和保留规则；也可以开启“月度归档”长期保留。
4. 在“备份记录”中可以随时“创建备份”、下载或从某份备份恢复。

备份范围（设置键 `backup_scope`）同时作用于手动和定时备份，每份备份记录自己的范围，修改设置不影响已有备份：

| 取值 | 界面名称 | 内容 |
| --- | --- | --- |
| `database` | 整个数据库 | 目标数据库中所有非系统 schema（默认）。数据库账号必须能读取所有 schema 和表，不会跳过无权限的数据 |
| `public` | 应用数据（public schema） | 只导出 XY2API 使用的 `public` schema，不含其他 schema 和 PostgreSQL large objects |

两种范围都**不包含** Redis、应用数据目录、`.env` / `config.yaml` 和加密密钥。`TOTP_ENCRYPTION_KEY` 丢失后，备份里已加密的字段无法解密，请另行妥善保存。客服聊天图片存放在 `public` schema 的表中，两种范围都会包含尚未清理的图片。

恢复以单个事务执行（`psql --single-transaction`，遇错即停）：任何 SQL 出错都会整体回滚，不会留下半恢复的数据库。恢复按备份自身的范围进行，`public` 备份只覆盖备份内的表和数据，不影响其他 schema。`public` 备份的恢复目标必须是已经由当前版本 XY2API 初始化过的数据库（具备 `public` schema 和 `pg_trgm` 等扩展），不能直接恢复到空库。

备份和恢复调用本机的 `pg_dump` 与 `psql`，客户端主版本不能低于数据库服务器。官方镜像内置 PostgreSQL 18 的客户端；用安装脚本部署时需要在服务器上安装匹配版本。备份期间需要为临时文件和压缩后的 SQL 预留磁盘空间。

### 用命令行备份

不使用后台功能时，可以直接用 `pg_dump`。Compose 部署示例：

```bash
docker compose exec -T postgres pg_dump -U xy2api -d xy2api -Fc > xy2api-$(date +%F).dump
# 恢复到同名空库（先停止 xy2api 服务）
docker compose stop xy2api
docker compose exec -T postgres pg_restore -U xy2api -d xy2api --clean --if-exists < xy2api-2026-11-10.dump
docker compose start xy2api
```

用户名和库名按 `.env` 中的 `POSTGRES_USER`、`POSTGRES_DB` 替换。本地目录版也可以在 `docker compose down` 之后直接打包 `postgres_data/` 等数据目录。

Redis 主要保存缓存和运行状态，Compose 中已开启 AOF 持久化。需要完整回到某个时间点时，与数据库一起备份 `redis_data/`。

### 备份失败排障

`pg_dump exited with error: exit status 1` 只表示导出进程失败，备份记录里的“查看失败原因”会给出截断、脱敏后的错误输出：

| 错误输出 | 排查方向 |
| --- | --- |
| `permission denied for schema ...` / `permission denied for table ...` | 数据库里有当前账号无权读取的 schema 或表（例如其他应用的数据）。由数据库维护者授权，或把备份范围改为“应用数据（public schema）” |
| `server version mismatch` | `pg_dump` 主版本低于数据库服务器，换用匹配的客户端 |
| `password authentication failed` / `no password supplied` | 应用的数据库连接配置与目标库不一致 |
| SSL 错误、拒绝连接、解析失败 | 应用所在环境到数据库的网络、DNS、SSL 模式与证书 |
| `Error loading shared library` / `symbol not found` | 运行环境里的 PostgreSQL 客户端或运行库不完整 |
| `context canceled` / `deadline exceeded` | 任务被取消或超时；检查数据库负载、锁等待和临时磁盘空间 |

数据库维护者可以用应用的数据库账号执行以下只读查询，找出无权读取的对象：

```sql
SELECT current_database(), current_user, version();

SELECT n.nspname, c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
  AND c.relkind IN ('r', 'p', 'm', 'S')
  AND NOT has_table_privilege(current_user, c.oid, 'SELECT')
ORDER BY n.nspname, c.relname;
```

备份成功后，建议定期在隔离环境里试着恢复一次，确认备份可以用于回退。pg_dump 的权限与版本要求见 [PostgreSQL 官方文档](https://www.postgresql.org/docs/current/app-pgdump.html)。
