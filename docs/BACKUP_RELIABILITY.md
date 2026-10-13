# 数据库备份与恢复排障

手动备份与定时备份共用 `PgDumper`，按保存的备份范围导出 SQL，再压缩、上传到配置的对象存储。`gzip/dump failed: pg_dump exited with error: exit status 1` 表示导出进程失败；它本身不能区分数据库权限、客户端版本、连接或运行库问题，也不表示定时器没有触发。

## 明确选择备份范围

系统设置的数据备份页可以保存两种范围，字段为 `backup_scope`：

| 值 | 范围 |
| --- | --- |
| `database` | 完整目标数据库的全部非系统 schema。默认值；已有配置和旧备份记录的空字段也按此解释。 |
| `public` | 仅 XY2API 使用的 `public` schema 及其中对象、表数据；不包含其他应用或历史迁移 schema，也不包含 PostgreSQL large objects。 |

范围同时作用于之后的手动备份和定时备份，无须开启定时器才能使用。每份新记录保存实际范围；后台任务启动后再修改设置，不会改变该次备份的范围。旧客户端保存设置时省略该字段，会保留已有选择；未知值直接拒绝。只支持上述两种明确范围，不接受通配符或任意排除列表。

本次用户已选择日常使用 `public` 应用备份。发布代码仍保留原有全库默认值，上线后由管理员在备份页选择并保存 `public`。本次对生产仅做只读核查，没有修改生产设置、权限或运行备份。

## 本次修复

- 导出失败记录保留 `pg_dump` 的有界错误诊断，不再仅显示退出码。stderr 原始缓冲最多 8 KiB，去除数据库密码、连接 URI 中的凭据和常见 SQL/行数据摘录；超过上限标记截断。错误继续进入现有备份记录和管理员界面。
- 临时文件创建/写入失败等情况下，提前关闭导出流会终止未完成的进程、等待回收并释放迁移锁，避免备份长时间停留在执行中。取消/超时保留可识别的上下文错误。
- 导出与恢复均禁止交互式密码提示；凭据通过进程环境传递。
- 恢复明确以 `psql --file=- --single-transaction --set=ON_ERROR_STOP=on --no-psqlrc` 读取输入，SQL 错误必须返回失败，事务回滚，用户的 `.psqlrc` 不会改变恢复行为。错误输出有界，成功语句输出不在内存中累计。
- 显式选择 `public` 后，导出使用 `--schema=public --strict-names`，找不到该 schema 会失败。恢复使用备份本身的范围，不读取当前新任务的范围设置；其他 schema 的现有数据不被该备份删除。

`public` 备份先由 `pg_dump` 生成磁盘上的临时 custom archive，再按 `pg_restore --list` 提供的对象目录，仅省略 `public` namespace 本身的定义、注释和 ACL，输出既有 `.sql.gz` 格式。表、数据、序列、函数、类型和索引仍保留；不对 SQL、函数体或业务数据进行文本替换。这样恢复不会先删除整个 `public` schema，也不会删除数据库管理员拥有的 `pg_trgm` 扩展。临时归档和目录在成功关闭、失败或取消后清理，全程持有迁移锁；导出期间需为临时归档及最终压缩 SQL 预留磁盘空间。

**`public` 备份的恢复目标必须先按当前 XY2API 版本初始化数据库，具备 `public` schema 与所需扩展（包括 `pg_trgm`）。** 备份不包含扩展的安装文件或扩展定义；直接恢复到未经初始化的空数据库不在此范围内。部署镜像同时提供匹配版本的 `pg_dump`、`pg_restore` 和 `psql`。导出删除 namespace 定义不删除其中对象，恢复仍会覆盖备份内的应用表和数据。

全库备份失败时不会自动改为只导出 `public`；两种范围都不会忽略所选范围内无权限的表，或将部分导出标记为成功。本功能不包含 PostgreSQL 集群级角色、Redis 或应用数据目录；完整升级回退仍须按发布文档保存对应数据。自定义跨 schema 依赖可能使独立恢复失败，必须在目标环境验证，不会自动扩大备份范围。

## 根据实际诊断处理

| 诊断内容 | 核查方向 |
| --- | --- |
| `permission denied for schema ...` / `permission denied for table ...` | 数据库内是否存在当前数据库用户无读取权限的业务或历史 schema/table。由数据库维护者确认备份范围与最小权限；不要直接跳过这些数据。 |
| `server version mismatch` | `pg_dump` 的主版本不能低于服务器主版本。使用与数据库匹配的客户端。 |
| `password authentication failed` / `no password supplied` | 当前应用的数据库主机、用户、数据库名和密码配置是否与目标数据库一致。避免在日志或问题反馈中粘贴凭据。 |
| SSL、拒绝连接、解析失败 | 运行应用的容器内到数据库的网络、名称解析、SSL 模式和证书。 |
| `Error loading shared library` / `symbol not found` | 当前运行制品内的 PostgreSQL 客户端与运行库是否齐全、匹配。 |
| `context canceled` / `deadline exceeded` | 任务退出、服务关闭或操作预算耗尽；排查数据库负载、锁等待与临时磁盘。 |

以下只读 SQL 可由数据库维护者以应用使用的数据库身份执行，确认当前连接与未授权对象；不修改权限或数据：

```sql
SELECT current_database(), current_user, version();

SELECT n.nspname,
       has_schema_privilege(current_user, n.oid, 'USAGE') AS can_use_schema
FROM pg_namespace n
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
ORDER BY n.nspname;

SELECT n.nspname, c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
  AND c.relkind IN ('r', 'p', 'm', 'S')
  AND NOT has_table_privilege(current_user, c.oid, 'SELECT')
ORDER BY n.nspname, c.relname;
```

这些查询用于初步定位，不覆盖大对象、行级安全、扩展及跨对象依赖等全部权限要求。修复后的成功备份须在隔离环境验证恢复，再用于升级回退。

## 验证范围

单元回归覆盖诊断保留、密码与 URI 脱敏、超长 stderr、进程启动失败、提前关闭/取消、迁移锁释放、恢复参数、范围保存/兼容、对象目录筛选与任务快照。真实 PostgreSQL 集成测试 `TestPgDumperRealPostgresBackupAndAtomicRestore` 验证完整导出含额外 schema、成功恢复、SQL 中途失败回滚，以及受限角色的权限错误不会被掩盖；还覆盖受限角色显式导出 `public`、普通应用角色恢复时保留管理员拥有的 `pg_trgm`、恢复到另一个已初始化目标、函数/类型/序列与 SQL 外观文本数据保持、其他 schema 不变和缺失 `public` 时失败。

2026-10-13 在独立无网络只读容器检查已发布 `0.2.6` 镜像，`pg_dump` 与 `psql` 均为 PostgreSQL 18.6，运行库可加载。随后按用户授权只读核查生产：实际应用客户端也是 18.6，连接 PostgreSQL 18.4 的 TLS 验证成功；当前应用角色对三个非系统 schema 无 `USAGE`，其中两个 schema 共 36 张表无 `SELECT` 权限。这足以阻止当前全库导出。应用角色拥有数据库及 `public` 表，当前数据库无 large objects。隔离 PostgreSQL 已复现相同权限失败；没有在生产运行导出、恢复、改权限或重启。

代码修复不会授予缺失的数据库权限。若选择全库，仍需数据库维护者确认其他 schema 的数据归属并配置所需权限；选择 `public` 后，其他 schema 必须由其维护者单独保护。不能以本地回归通过宣称生产备份已成功。

PostgreSQL 官方说明：[pg_dump 的范围、权限与版本兼容](https://www.postgresql.org/docs/current/app-pgdump.html)、[psql 的单事务与 ON_ERROR_STOP](https://www.postgresql.org/docs/current/app-psql.html)。
