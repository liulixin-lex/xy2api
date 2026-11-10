# Docker Compose 部署

本文说明如何用 Docker Compose 运行 XY2API：官方镜像与标签、仓库自带的几个 Compose 文件有什么区别、首次启动时的自动初始化、如何传入额外的环境变量，以及数据存放、健康检查和常见故障。它是推荐的部署方式，适合需要长期运行 XY2API 的运维人员。第一次部署可以先按 [快速开始](../getting-started.md) 走一遍。

## 镜像与标签

官方镜像发布在 `ghcr.io/liulixin-lex/xy2api`，只提供 `linux/amd64` 平台。ARM 服务器（`linux/arm64`）没有官方镜像，需要 [从源码构建](source.md) 自己的镜像。

| 标签 | 含义 |
| --- | --- |
| `X.Y.Z`（例如 `0.6.0`） | 固定版本，生产环境推荐 |
| `X.Y` | 该次版本线上最新的修订版 |
| `X` | 该主版本线上最新的版本 |
| `latest` | 最新的正式版本，仓库自带的 Compose 文件默认使用它 |

预发布版本（例如 `0.6.0-rc.1`）只有完整版本号这一个标签，不会更新 `X.Y`、`X` 和 `latest`。

镜像内的程序以非 root 用户 `xy2api`（UID 1000）运行，数据目录是 `/app/data`；入口脚本启动时会修正 `/app/data` 的属主再切换用户。镜像自带与 Compose 中 PostgreSQL 18 匹配的 `pg_dump`、`pg_restore` 和 `psql`，供管理后台的数据库备份与恢复使用。镜像的 `HEALTHCHECK` 访问 `/health`。

## 选择 Compose 文件

`deploy/` 目录里有四个 Compose 文件：

| 文件 | 包含的服务 | 数据存放 | 适合 |
| --- | --- | --- | --- |
| `docker-compose.local.yml` | XY2API、PostgreSQL 18、Redis 8 | 当前目录下的 `data/`、`postgres_data/`、`redis_data/` | 大多数部署；`docker-deploy.sh` 下载的就是它。整个目录打包即可迁移 |
| `docker-compose.yml` | 同上 | Docker 命名卷（`xy2api_data`、`postgres_data`、`redis_data`） | 不需要直接访问数据目录；会把 `.env` 中的 PostgreSQL 调优参数传给数据库 |
| `docker-compose.standalone.yml` | 仅 XY2API | 命名卷 `xy2api_data` | 已有外部 PostgreSQL 和 Redis |
| `docker-compose.dev.yml` | 从本地源码构建的 XY2API、PostgreSQL、Redis | 当前目录 | 开发与测试，见 [从源码构建](source.md) |

PostgreSQL 和 Redis 都不向宿主机暴露端口，只在 Compose 内部网络里访问。

## 一键部署

```bash
mkdir -p xy2api && cd xy2api
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/docker-deploy.sh | bash
docker compose up -d
docker compose logs -f xy2api
```

脚本下载 `docker-compose.local.yml`（保存为 `docker-compose.yml`）和 `.env.example`，生成 `POSTGRES_PASSWORD`、`JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 与随机的 `ADMIN_EMAIL` 写入 `.env`（权限 `600`），并创建三个数据目录。目录里已有 `docker-compose.yml` 和 `.env` 时会先询问是否覆盖。

## 手动部署

```bash
git clone https://github.com/liulixin-lex/xy2api.git
cd xy2api/deploy
cp .env.example .env
chmod 600 .env
```

编辑 `.env`，至少设置以下三项（都可以用 `openssl rand -hex 32` 生成）：

```bash
POSTGRES_PASSWORD=<随机值>
JWT_SECRET=<随机值>
TOTP_ENCRYPTION_KEY=<64 位十六进制>
```

然后选择一个 Compose 文件启动：

```bash
# 本地目录版
mkdir -p data postgres_data redis_data
docker compose -f docker-compose.local.yml up -d

# 命名卷版
docker compose up -d
```

后续所有 `docker compose` 命令都要带上同一个 `-f` 参数。

## 关键变量

完整列表见 [`deploy/.env.example`](../../deploy/.env.example) 和 [配置文件与环境变量](configuration.md)。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | 无，必填 | PostgreSQL 密码。只在数据库首次初始化时生效，之后修改它不会改变数据库里的密码 |
| `POSTGRES_USER` / `POSTGRES_DB` | `xy2api` / `xy2api` | 数据库用户名与库名 |
| `JWT_SECRET` | 空 | 登录令牌签名密钥，至少 32 个字符。留空时首次初始化自动生成。程序第一次使用的值会存入数据库，之后以数据库里的值为准，再修改此变量不会替换它（日志会提示不一致） |
| `TOTP_ENCRYPTION_KEY` | 空 | 64 位十六进制（32 字节）。加密 2FA 密钥、支付配置、备份存储凭据等；留空时每次启动随机生成，重启后已加密的数据无法解密，也不能启用 2FA。务必固定 |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | 空 | 首次初始化时创建的管理员。留空则随机生成并打印到日志；密码 8–72 字节 |
| `BIND_HOST` | `0.0.0.0` | 宿主机端口映射的监听地址。反向代理在同一台机器上时建议改为 `127.0.0.1` |
| `SERVER_PORT` | `8080` | 宿主机端口。容器内始终监听 8080 |
| `TZ` | `Asia/Shanghai` | 时区，影响“今日”统计边界、订阅到期与日志时间 |
| `REDIS_PASSWORD` | 空 | Redis 密码，同时用于 Redis 服务端和应用 |
| `APP_DATA_VOLUME_NAME` | `xy2api_data` | 仅命名卷版：应用数据卷的实际名称。从 Sub2API 迁移时需要改，见 [从 Sub2API 迁移](migrate-from-sub2api.md) |
| `UPDATE_PROXY_URL` | 空 | 访问 GitHub（版本检查、价格数据）使用的代理 |

`standalone` 版另外需要 `DATABASE_HOST`、`DATABASE_PASSWORD`、`REDIS_HOST`（必填）以及 `DATABASE_PORT`、`DATABASE_USER`、`DATABASE_DBNAME`、`DATABASE_SSLMODE`、`REDIS_PORT`。数据库在宿主机上时可以用 `host.docker.internal`，该文件已映射到宿主机网关。注意 `standalone` 版不传递 `TOTP_ENCRYPTION_KEY`、`UPDATE_PROXY_URL` 和 `SECURITY_URL_ALLOWLIST_*`，需要用下文的覆盖文件补上。

## 首次启动的自动初始化

Compose 文件给容器设置了 `AUTO_SETUP=true`。数据目录里既没有 `config.yaml` 也没有 `.installed` 时，程序会：

1. 用环境变量连接 PostgreSQL 和 Redis（数据库不存在时尝试创建）；
2. 执行全部数据库迁移；
3. 数据库里还没有用户时创建管理员，生成的邮箱或密码打印到标准输出（`Generated admin email (login username): ...`、`Generated admin password (one-time): ...`）；
4. 把连接参数写入 `/app/data/config.yaml`，并创建 `/app/data/.installed`。

之后的启动直接读取这份配置，不再初始化。不要在首次启动前就把自己的 `config.yaml` 挂载进去：程序看到已有配置会跳过初始化，管理员不会被创建。需要自定义配置时，先完成首次启动，再修改 `data/config.yaml`（本地目录版）或改用挂载文件，然后重启容器。

## 传入更多环境变量

Compose 只把 `environment:` 里列出的变量传进容器，`.env` 里有但文件没有引用的变量不会生效。例如 `LOG_*`、`SERVER_H2C_*`、`SERVER_MAX_REQUEST_BODY_SIZE`、`OPS_ENABLED`、`DASHBOARD_AGGREGATION_*`、`JWT_ACCESS_TOKEN_EXPIRE_MINUTES`、`SERVER_TRUSTED_PROXIES` 都不在仓库自带的 Compose 文件里。

推荐在部署目录放一个覆盖文件，不改动下载来的原文件：

```yaml
# docker-compose.override.yml
services:
  xy2api:
    environment:
      - SERVER_TRUSTED_PROXIES=127.0.0.1/32
      - LOG_LEVEL=info
      - SERVER_H2C_ENABLED=true
```

不带 `-f` 运行 `docker compose` 时，Compose 会自动合并同目录的 `docker-compose.override.yml`；用 `-f` 指定文件时要把它一起列出，例如 `docker compose -f docker-compose.local.yml -f docker-compose.override.yml up -d`。修改后重新执行 `up -d` 让容器按新配置重建。

也可以把配置写进 `config.yaml`。环境变量的优先级高于配置文件，详见 [配置文件与环境变量](configuration.md)。

## 数据与持久化

| 位置 | 内容 |
| --- | --- |
| `/app/data`（`data/` 或卷 `xy2api_data`） | `config.yaml`、`.installed`、日志 `logs/`、插件、用量导出等应用数据 |
| PostgreSQL 数据（`postgres_data/` 或同名卷） | 全部业务数据。Compose 显式设置了 `PGDATA=/var/lib/postgresql/data`，保证数据落在挂载的目录或卷里 |
| Redis 数据（`redis_data/` 或同名卷） | 缓存与运行状态，开启了 RDB 快照和 AOF（每秒同步） |

命名卷版里，应用数据卷用 `APP_DATA_VOLUME_NAME` 指定了固定名称（默认 `xy2api_data`）；`postgres_data` 和 `redis_data` 的实际名称带 Compose 项目名前缀（默认是部署目录名），例如 `deploy_postgres_data`。换目录或改项目名后，Compose 会为数据库和 Redis 创建新的空卷，看起来就像数据丢了。可以用 `docker volume ls` 核对。

本地目录版迁移到新服务器：

```bash
docker compose down
cd .. && tar czf xy2api-deploy.tar.gz xy2api/
# 复制到新服务器后
tar xzf xy2api-deploy.tar.gz && cd xy2api && docker compose up -d
```

数据库备份与恢复见 [升级、回滚与备份](upgrade.md#备份与恢复)。

## 健康检查与启动恢复

| 接口 | 行为 | 用途 |
| --- | --- | --- |
| `/health` | 不访问任何依赖，始终返回 `200 {"status":"ok"}` | 容器健康检查、存活探针 |
| `/ready` | 并行检查 PostgreSQL 和 Redis（各 2 秒超时，结果复用 1 秒）；全部可用时 `200 {"status":"ready"}`，否则 `503 {"status":"not_ready","failed":["database","redis"]}`，只列出失败项名称 | 反向代理主动健康检查、就绪探针、外部监控 |

宿主机或 Docker 重启后，PostgreSQL 可能还在恢复。程序启动时遇到数据库“正在启动”或连接类错误会按指数退避重试（1 秒起，最长间隔 30 秒），数据库就绪后继续启动；认证失败、迁移校验和不一致、SQL 错误等永久性错误会立即退出。`depends_on: condition: service_healthy` 只在全新启动时控制顺序，不能代替这一重试。命名卷版给应用留了 120 秒的健康检查宽限期。

所有服务都设置了 `restart: unless-stopped`。

## 常用命令

以下以默认文件名为例；用了 `-f` 的部署请带上相同参数。

| 操作 | 命令 |
| --- | --- |
| 启动 / 停止 | `docker compose up -d` / `docker compose down` |
| 查看状态 | `docker compose ps` |
| 查看日志 | `docker compose logs -f xy2api` |
| 只重启应用 | `docker compose restart xy2api` |
| 升级到新镜像 | `docker compose pull && docker compose up -d`，详见 [升级](upgrade.md) |
| 检查数据库 | `docker compose exec postgres pg_isready` |
| 检查 Redis | `docker compose exec redis redis-cli ping` |
| 删除全部数据（不可恢复） | 本地目录版：`docker compose down` 后删除三个数据目录；命名卷版：`docker compose down -v` |

## 排障

| 现象 | 处理 |
| --- | --- |
| 端口被占用 | 修改 `.env` 的 `SERVER_PORT` 后 `docker compose up -d` |
| 应用日志报数据库认证失败 | 数据库初始化后又改了 `POSTGRES_PASSWORD`。改回原值，或在数据库里修改该用户的密码 |
| 日志出现 `database connection pool exceeds the server limit` 或数据库报连接数过多 | 应用连接池上限 `DATABASE_MAX_OPEN_CONNS` 超过了 PostgreSQL 的 `max_connections`。`.env.example` 里连接池为 256，本地目录版不向 PostgreSQL 传调优参数（`max_connections` 保持默认 100）；把连接池调到 100 以下，或在覆盖文件里给 `postgres` 服务加上 `-c max_connections=...` |
| 重启后用户 2FA 失效、加密的设置读不出来 | `TOTP_ENCRYPTION_KEY` 为空或被改动。设置固定值；已经丢失的密钥无法找回 |
| 容器反复重启 | `docker compose logs --tail=100 xy2api` 查看启动错误；`docker compose ps` 确认 PostgreSQL 和 Redis 健康 |
| 改了 `.env` 不生效 | 该变量不在 Compose 文件的 `environment:` 里，见上文“传入更多环境变量”；修改后需要 `up -d` 重建容器，`restart` 不会重新读取 `.env` |

## 限制与注意事项

- 只有 `linux/amd64` 镜像。
- 管理后台的在线更新在容器里只替换容器内的程序，容器重建后会回到镜像里的版本。容器部署请通过更换镜像升级，见 [升级](upgrade.md)。
- 对外提供服务时请放在 HTTPS 反向代理后面，并按 [反向代理](reverse-proxy.md) 配置真实客户端 IP。
