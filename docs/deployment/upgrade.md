# 升级、回滚与备份

本文说明各种部署方式如何升级和回退 XY2API、启动时的数据库迁移如何工作，以及用管理后台或命令行备份、恢复数据库的方法与排障。负责维护 XY2API 实例的运维人员在每次升级前都应看一遍。

## 升级前

1. 阅读目标版本的发布说明（GitHub Releases 与仓库根目录的 `CHANGELOG.md`），确认是否有需要提前处理的兼容性变化。
2. 备份数据库，见下文 [备份与恢复](#备份与恢复)。同时保存 `.env`、`config.yaml` 和应用数据目录。
3. 选择低峰时段。数据库迁移在启动时执行，相关迁移遇到繁忙表时限时等锁，超时后启动流程会重试；实际执行结构变更仍会持锁，不能把等锁上限当作总锁表时长。
4. 程序内嵌前端，前后端总是同一版本，不需要单独升级前端。

## 本版升级须知

- 本版执行迁移 **313–316、318**。其中 313–316 设置 **5 秒等锁上限**，拿不到表锁时启动流程会退避后重试；单实例在迁移与重试期间不对外服务。5 秒限制等锁，不代表整次迁移或持锁总时长。
- 首次从旧版本升级时，所有账号 × 模型 × 分组的新质量身份从**未知**开始。旧质量状态不迁移，原来因质量门槛冷却的号当场回到前排，之后按新口径的真实请求重新取证。这与同版程序重启后恢复已保存身份不同。
- 现有分组的首字上限、报错上限、缓存下限三个数沿用，但判定含义变化，见下表。升级后应到分组看板与门槛试算核对各组表现。
- `database.max_open_conns`（环境变量 `DATABASE_MAX_OPEN_CONNS`）至少为 2，小于 2 启动报错。消费租约占用一条连接，读队列与更新调度快照还需其他连接；默认值 256 不受影响，曾设为 1 的部署应提前调高。

| 线 | 升级后的判定口径 |
|---|---|
| 首字 | 使用首个有效输出，与前排同伴比较（规则 B）；全局默认“把握”80%、“容忍”20%、“比同伴高出”5 个百分点 |
| 缓存 | 不再保留 3 个百分点的容差；最少续接轮由 30 降为 20，按最近 30 分钟的输入 Token 加权 |
| 报错 | 最近 60 分钟内的最近 100 个请求名额；至少 50 个明确结果才判达标，不足时仍可按补齐规则确认不达标；原分组上限低于 5% 时按 5% 算 |

具体证据与恢复规则见 [分组质量门槛](../guides/group-quality-gate.md)。

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
- 相关迁移限时等待繁忙表的锁；等锁超时或启动时数据库仍在恢复（例如刚重启），启动流程会自动重试。持续失败时检查启动日志与数据库锁等待，见 [Docker Compose 部署](docker.md#健康检查与启动恢复)。

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

回滚只替换程序，数据库保持升级后的结构：旧版本启动时只检查自己携带的迁移，新版本加的表和列会留在库里。回退前先核对目标版本的兼容性说明；是否恢复整库备份，应另外评估新增业务数据的损失。本版已验证的回退路径见下文。

### 本版回退与再次升级

本次验收已用升级后的数据库实测 **v0.6.8 基线程序（代码 `54d380fc9`）**：旧程序能启动、转发、计费和写采样，随后也能直接再升级。推荐回退前先备份当前库，**保留升级后的数据库，只切回已验证的旧程序与配套前端**，不删除迁移记录。

| 回退期间 | 实际行为 |
|---|---|
| 质量状态 | 读取升级前保留的旧状态，继续按旧的冷却规则运行，不读取新逐线身份 |
| 倍率与固定 | 最低上游倍率和固定号不生效；旧程序复制分组成员时先删后插，会把被重建成员的固定标记清为关闭 |
| 采样与再次升级 | 回退期间写入的采样保留，但不被重新升级后的新规则当作证据；可以直接再升级，再核对倍率与固定设置 |

本次回退路径不建议恢复升级前的整库备份：那会丢失备份之后、包括升级后新增的用量、余额变动等业务数据。只有确需连数据状态一起回退并接受这些影响时，才选择整库恢复。

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
