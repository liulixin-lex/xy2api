# 从 Sub2API 迁移

XY2API 派生自 Sub2API，沿用了它的数据库结构、缓存键和对外接口。已经在运行 Sub2API 的部署，可以只替换程序或镜像、继续使用原来的数据库、Redis、数据目录和密钥切换到 XY2API。本文说明哪些东西必须保持不变、如何用预检工具检查，以及 Docker Compose 和 systemd 两种部署的切换与回滚步骤。

## 兼容范围

以下内容在 XY2API 中保持与 Sub2API 一致，切换后无需转换：

- 已发布的数据库迁移文件及其校验和，已有的表、索引和约束名（例如 `sub2api_plugin_installations`）；
- Redis 中已在使用的键、浏览器本地存储的键；
- 兼容接口 `/v1/sub2api/billing` 及其响应对象 `sub2api.key_billing`；
- 插件 v1 的协议与 `.s2plugin` 包格式；插件声明的 `requires.sub2api` 等版本要求与 XY2API 的 Sub2API 兼容基线比较，`xy2api --version` 会同时打印产品版本和兼容基线；
- 账号导入导出格式 `sub2api-data` 与 `sub2api-bundle`；
- 配置搜索路径 `/etc/sub2api`；
- 价格数据源：仍从 `Wei-Shaw/model-price-repo` 同步并做 SHA-256 校验，远程不可用时使用本地缓存或随版本发布的回退文件。

XY2API 自己的默认值只用于全新安装：数据库名与用户 `xy2api`、应用数据卷 `xy2api_data`、仪表盘缓存前缀 `xy2api:`。已有部署首次切换时**不要**跟着改成这些值。

## 必须保持不变

| 项目 | Sub2API 常见值 | 说明 |
| --- | --- | --- |
| 数据库名与用户 | `POSTGRES_DB=sub2api`、`POSTGRES_USER=sub2api` | 新版 `.env.example` 的默认值是 `xy2api`，照抄会连不上原来的库 |
| 数据库密码与 SSL 设置 | 原值 | PostgreSQL 只在首次初始化时读取 `POSTGRES_PASSWORD` |
| Redis DB 编号与密码 | `REDIS_DB=0` | 改了会丢失会话粘性、并发计数等运行状态 |
| `JWT_SECRET` | 原值 | 改了用户需要重新登录 |
| `TOTP_ENCRYPTION_KEY` | 原值 | 改了已加密的 2FA 密钥、支付配置和其他加密数据无法解密 |
| 应用数据目录或卷 | 命名卷 `<项目名>_sub2api_data`，或宿主机目录 | 保存 `config.yaml`、`.installed`、插件等 |
| Compose 项目名 | 原部署目录名 | PostgreSQL、Redis 命名卷的名称带项目名前缀，换了会得到空卷 |
| 仪表盘缓存前缀 | `sub2api:` | 只影响缓存，改了会在切换后重新生成统计缓存 |

首次切换只换程序，数据库名、目录名这类改名以后单独规划。

## 升级预检

切换前运行只读预检，它不会修改任何配置或数据。预检工具在仓库的 `tools/` 目录里，不在发布包中，需要先 `git clone` XY2API 仓库，在仓库根目录用 Python 3 运行：

```bash
python3 tools/xy2api_upgrade_preflight.py --env-file /path/to/.env --compose-file /path/to/docker-compose.yml
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--env-file` | 必填 | 部署使用的 `.env` |
| `--compose-file` | 无 | 用于检查是否保留了 `/var/lib/sub2api` 的数据目录挂载 |
| `--root` | `/` | 查找旧安装路径的根目录 |
| `--skip-docker` | 关闭 | 不调用 `docker volume ls` |
| `--expected-postgres-db` / `--expected-redis-db` / `--expected-dashboard-prefix` | `sub2api` / `0` / `sub2api:` | 原部署的实际值不同时传入 |
| `--json` | 关闭 | 以 JSON 输出 |

发现错误时返回码为 2，没有错误时为 0。常见结果：

| 代码 | 级别 | 含义与处理 |
| --- | --- | --- |
| `postgres-db-changed` / `redis-db-changed` | error | `POSTGRES_DB` 或 `REDIS_DB` 与原值不同，改回原值 |
| `missing-stable-secret` | error | `POSTGRES_PASSWORD`、`JWT_SECRET` 或 `TOTP_ENCRYPTION_KEY` 为空，从原部署复制 |
| `legacy-volume-not-selected` | error | 找到唯一的旧应用数据卷，但 `APP_DATA_VOLUME_NAME` 还是默认值；按提示设置 |
| `legacy-volume-ambiguous` / `legacy-volume-unresolved` | error | 有多个候选卷或找不到，人工确认后显式设置 `APP_DATA_VOLUME_NAME` |
| `app-volume-missing` | error | `APP_DATA_VOLUME_NAME` 指向的卷不存在 |
| `dashboard-prefix-unverified` | warning | 设置 `DASHBOARD_CACHE_KEY_PREFIX=sub2api:`，或在 `config.yaml` 中把 `dashboard_cache.key_prefix` 设为原前缀。Compose 文件不转发这个变量，容器部署需用覆盖文件传入或写进 `config.yaml` |
| `legacy-bind-mount` / `legacy-installation-detected` | info | 检测到旧的数据目录挂载或安装路径，切换验证完成前保持不动 |
| `ready` | info | 没有发现会改变数据身份的问题 |

预检只检查 `POSTGRES_DB`，不检查 `POSTGRES_USER`；请自己确认它也保持原值。预检面向 Compose 部署的 `.env`；systemd 部署没有 `.env`，按上一节的表格自行核对 `config.yaml` 即可。

## Docker Compose 部署

1. **备份**数据库与 Redis，见 [备份与恢复](upgrade.md#备份与恢复)。
2. **保留旧文件**：在原部署目录里把旧的 Compose 文件和 `.env` 改名保存，例如 `docker-compose.sub2api.yml`、`.env.sub2api`，回滚时还要用。
3. **在同一目录**放入新的 Compose 文件（保持目录不变，Compose 项目名才不变），以旧 `.env` 为基础编辑新的 `.env`：原有的 `POSTGRES_USER`、`POSTGRES_DB`、`POSTGRES_PASSWORD`、`REDIS_*`、`JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 一律保留原值，再补上新模板里需要的变量。
4. **确定应用数据**：
   - 命名卷部署：用 `docker volume ls` 找到旧应用数据卷的完整名称（通常是 `<旧项目名>_sub2api_data`，例如 `deploy_sub2api_data`，不是裸的 `sub2api_data`），可用 `docker volume inspect <卷名>` 核对，然后在 `.env` 中设置 `APP_DATA_VOLUME_NAME=<完整卷名>`；
   - 本地目录部署：保留 `data/`、`postgres_data/`、`redis_data/` 原样；
   - 自定义的宿主机目录挂载（例如 `/var/lib/sub2api/...:/app/data`）：在新 Compose 文件里保留同样的挂载。
5. **运行预检**，处理所有 error。
6. **停止旧容器**。新文件的容器名是 `xy2api`、`xy2api-postgres`、`xy2api-redis`，与旧容器不同，不停掉旧的会同时运行两套：

   ```bash
   docker compose -f docker-compose.sub2api.yml --env-file .env.sub2api down     # 不要加 -v
   ```

7. **启动 XY2API**：`docker compose up -d`，查看 `docker compose logs -f xy2api`。数据目录里已有 `config.yaml`，程序不会重新初始化，也不会创建新的管理员。
8. **验证**（见下文），确认无误后再清理旧文件。

## systemd 部署

Sub2API 的安装脚本把程序装在 `/opt/sub2api`，服务名 `sub2api`，设置向导把配置写在 `/opt/sub2api/config.yaml`。XY2API 使用 `/opt/xy2api` 和服务名 `xy2api`。

1. 备份数据库与 Redis，以及 `/opt/sub2api/config.yaml`（如果你把配置放在 `/etc/sub2api/config.yaml`，备份那一份）。
2. 停止并禁用旧服务：

   ```bash
   sudo systemctl stop sub2api
   sudo systemctl disable sub2api
   ```

3. 在安装 XY2API 之前，把配置和数据复制到新目录，这样首次启动不会进入设置向导：

   ```bash
   sudo mkdir -p /opt/xy2api
   sudo cp /opt/sub2api/config.yaml /opt/xy2api/config.yaml
   sudo cp -a /opt/sub2api/data /opt/xy2api/ 2>/dev/null || true
   ```

   `/etc/sub2api` 虽然仍在配置搜索路径里，但程序判断是否需要初始化时只看工作目录（`/opt/xy2api`）里有没有 `config.yaml` 或 `.installed`，所以仍需复制。
4. 运行安装脚本，见 [Linux 安装脚本与 systemd](binary.md)。脚本会修正目录属主并启动 `xy2api` 服务。
5. 旧服务单元里如果有自定义的 `Environment=`（例如 `TOTP_ENCRYPTION_KEY`、Gemini OAuth 变量），用 `sudo systemctl edit xy2api` 照样加上，然后重启。
6. 验证无误后，再删除 `sub2api.service` 和 `/opt/sub2api`。

## 切换后验证

- 用原管理员账号登录管理后台；
- 用已有 API Key 调用一次，检查使用记录与扣费；
- 检查模型价格、OAuth 账号的令牌刷新、插件、定时任务；
- 打开“运维监控”等实时页面，确认管理后台的 WebSocket 连接正常；
- 启用了 2FA 或支付的部署，确认 2FA 登录和支付配置可读。

## 回滚

1. 停止 XY2API（`docker compose down` 或 `systemctl stop xy2api`）。
2. 用保存的旧 Compose 文件（`docker compose -f docker-compose.sub2api.yml --env-file .env.sub2api up -d`）或 `sub2api` 服务重新启动 Sub2API，数据库、Redis 和数据目录都没有改名，可以直接使用。
3. 不要修改 `schema_migrations` 的校验和，也不要改写已执行的迁移。

XY2API 新增的表和列会留在数据库里。Sub2API 能否在其上正常运行取决于两者的版本差异，不作保证；切换前的备份是最终的回退手段。

## 并行验证

需要让两个版本短时间并存验证时，给 XY2API 使用独立的容器名、监听端口、日志路径和应用数据目录。两个版本可以短时间指向同一个数据库做兼容性验证，但不要让不同版本长期同时运行迁移或后台任务。
