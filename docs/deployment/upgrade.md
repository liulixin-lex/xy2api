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
| 报错 | 旧版按已完成尝试计数，先看本组 60 分钟，样本不足时扩至 120 分钟并可借用同账号、同模型的其他分组证据；至少 50 次才判，没有最近 100 个名额的限制。新版改为最近 60 分钟内的最近 100 个请求名额，同请求同账号重试按最终结果算一次；至少 50 个明确结果才判达标，不足时仍可按补齐规则确认不达标 |

具体证据与恢复规则见 [分组质量门槛](../guides/group-quality-gate.md)。

默认配置下，已有 HTTP 首输出守卫的文本流式路径会继续等待首个有效输出：上游仅发送已识别的保活、空帧或进度帧时，不再解除首输出守卫，到期会按首输出超时换号。运维可能看到首输出超时与换号增多；有效输出或终态提交后解除守卫，未识别的事件仍沿用原有提交方式。

需要恢复提交时机时，可在**系统设置 → 网关服务 → 网关韧性**的流式开场设置或“高级（完整 JSON）”中关闭对应开关并保存，无需重新发版：`stream.prelude_staging` 管 Chat Completions、Messages 的兼容转换，以及 Anthropic 上游的直通与协议转换；`stream.native_prelude_whitelist` 管原生 Responses（含透传）。关闭开关只恢复对应路径原有的提交方式；WebSocket 直通保持原有守卫。细节见 [流式开场与终止](../guides/gateway-resilience.md#流式开场与终止-stream)与[设置参考](../guides/gateway-resilience-settings.md#流式开场与终止-stream)。

## OpenAI 档位策略蓝绿发布

迁移 322 增加分组的档位配置、渠道的 Ultrafast 倍率及用量档位证据列。已有 OpenAI 与组合分组回填为策略版本 1，保留旧强制 Fast 与原赠免。渠道和组卡的 Fast 倍率原样保留。新增 Ultra 倍率初始为空。迁移完成后可比较升级前后的 Fast 价卡统计。[分组管理接口](../guides/groups.md#旧套餐与管理接口)

1. **离线影子验证。** 在隔离环境创建两套实例与各自的 PostgreSQL、Redis，连接同一个可记录出站请求的模拟上游。基线实例运行旧版本，候选实例运行新版本。将同一份脱敏的 HTTP 请求、WebSocket 轮次和模拟终态各重放一遍，比较策略版本 1 的实际上游档位、拒绝状态、账单原额与实扣。候选实例再创建显式策略测试组，验证强开、强关、赠免、降档回报、价格快照和用量证据。影子验证使用隔离测试账本，不把生产请求同时转发给两个计费实例。
2. **蓝绿交叠。** 升级时先保持生产组为策略版本 1。暂停写入版本 2 和 Ultra 倍率，包括 OpenAI 分组模型价卡及渠道整表更新。新实例通过 `/ready` 后再接流量，保持旧进程的在途请求可完成。记录当前 Fast 价卡和待处理账单数量。
3. **启用。** 停止旧实例接入并排空 HTTP、WebSocket 和后台任务。确认没有旧进程仍连接生产数据库、领取计费恢复或导出任务，再为少量组显式启用版本 2 或设置 Ultra 倍率。用新请求核对出站、终态和使用记录中的 `service_tier_details` 后逐批启用。

本地冒烟用新构建的二进制启动临时 PG、Redis 与 HTTP 模拟上游，通过实际管理接口保存两项赠免和 Fast/Ultrafast 价卡，验证上游收到 Ultrafast、账单原额与实扣及已结算的计费意图。

```sh
(cd backend && go build -o /tmp/xy2api-tier-smoke ./cmd/server)
python3 tools/openai_tier_smoke.py /tmp/xy2api-tier-smoke
```

提供 `--legacy-server /path/to/previous/server` 可继续验证同库回滚。脚本先停止候选进程，再启动旧进程，用同一 API Key 请求并检查 priority 出站及旧版实扣。

迁移后的数据库可用现有只读连接运行检查。输出只有计数，无密钥、用户记录或请求正文。 [只读检查 SQL](../../tools/billing-integrity/openai-tier-preflight.sql)

```sh
psql "$AUDIT_DATABASE_URL" -X -f tools/billing-integrity/openai-tier-preflight.sql
```

### 回滚

如果尚未启用版本 2、未设置 Ultra 倍率且没有新版本的在途结算，旧程序可使用 322 迁移后的扩展表结构，数据库保留新增列。启用过显式策略后，优先回滚到仍懂新版策略的构建。原版本无法执行显式强关、强开 Ultra 或赠免 Ultra，也无法完整恢复新档位证据。必须回到原版本时按顺序处理：

1. 停止受影响的组接收新请求，排空 HTTP、WebSocket、导出和待结算账单。保持新版恢复进程工作直到相关快照都结算。仍由新版管理接口保存现有配置的备份，并用最新 revision 将渠道和分组价卡的显式 `ultrafast_multiplier` 清除为 `null`。
2. 使用新版管理 API 将各组切换回兼容执行器。先读取组的最新 revision，再在一个 `PUT /api/v1/admin/groups/{id}` 请求中明确提交 `openai_tier_policy_version: 1`、`openai_fast_mode: "inherit"` 或 `"force_on"`、`openai_ultrafast_mode: "inherit"`、`free_openai_ultrafast: false`。按原套餐需要决定是否同时更新 `free_openai_fast`。读取新的组配置并用只读 SQL 检查版本 2、显式倍率和待结算情况，再以旧程序接流量。组 ID、订阅和已经结算的记录继续保留。


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

从 **v0.6.11 回退到 v0.6.10** 时，迁移 321 新增的 `sched_line_states.last_assessment` 留在库中，旧程序不读取或清理此列，会话绑定的归属和代数两个 Redis 键的类型及取值格式不变，新会话按表现选号与接管、质量迁移提交时的首输出避让复验随回退停止。

此前从 v0.6.9 回退时，已用升级后的数据库实测 **v0.6.8 基线程序（代码 `54d380fc9`）**：旧程序能启动、转发、计费和写采样，随后也能直接再升级。推荐回退前先备份当前库，**保留升级后的数据库，只切回已验证的旧程序与配套前端**，不删除迁移记录。

| 回退期间 | 实际行为 |
|---|---|
| 质量状态 | 读取升级前保留的旧状态，继续按旧的冷却规则运行，不读取新逐线身份 |
| 倍率与固定 | 最低上游倍率和固定号不生效；旧程序复制分组成员时先删后插，会把被重建成员的固定标记清为关闭 |
| 采样与再次升级 | 回退期间写入的采样保留，但不被重新升级后的新规则当作证据；可以直接再升级，再核对倍率与固定设置 |

回退到 v0.6.8 的路径不建议恢复升级前的整库备份：那会丢失备份之后、包括升级后新增的用量、余额变动等业务数据。只有确需连数据状态一起回退并接受这些影响时，才选择整库恢复。

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
