# 用量记录后台导出

用户 CSV 和管理员 XLSX 使用同一 PostgreSQL 持久化任务引擎。浏览器只提交任务、查询状态和启动原生下载，不再逐页读取历史记录。历史列表及 Heavy 限流保持原有行为。

## 配置与启用

`usage_export.enabled` 默认 `false`，仅控制新任务；已有任务继续处理，完成文件仍可下载。`allowed_user_ids` 非空时，仅名单用户可以新建，管理员也受限；空名单表示全部用户。所有实例必须使用一致的预算和配置。配置变更通过既有应用配置与重启流程生效。

```yaml
usage_export:
  enabled: false
  allowed_user_ids: []
  directory: ./data/usage-exports
  concurrency: 2
  queue_limit: 100
  max_rows: 1000000
  max_file_bytes: 536870912
  task_disk_bytes: 4294967296
  storage_bytes: 21474836480
  free_bytes: 2147483648
  read_seconds: 300
  run_seconds: 900
  queue_seconds: 1800
  retention_seconds: 86400
  metadata_seconds: 604800
  create_rpm: 5
  status_rpm: 60
  ticket_rpm: 10
  download_concurrency: 2
  storage:
    type: local
    region: auto
    prefix: usage-exports/
    endpoint: ''
    bucket: ''
    access_key_id: ''
    secret_access_key: ''
    force_path_style: false
```

默认目录随 `DATA_DIR` 移至该持久目录下的 `usage-exports/`。目录必须可写、非 Web 静态目录且有足够空间；不使用 `/tmp` 作为发布存储。`files/` 是发布文件，`tmp/` 是执行代次独立暂存。多实例本地存储必须挂载同一共享目录；独立本地目录不受支持。S3 使用上述独立配置，私有 bucket/prefix，通过现有 `UploadFile` 流式上传，不使用会 `ReadAll` 的上传方法。S3 模式仍需本地暂存磁盘；部署时配置对象生命周期及未完成分片清理兜底，保留时间不得短于应用的文件有效期。

每用户最多一个活动任务，包含排队任务及两个入口。任务进入队列前以 PostgreSQL 事务锁原子检查队列、总存储预留和用户预算；相同幂等键和同参数活动任务不重复计入新建额度。已完成任务再次创建会得到新快照。数据库连接池需容纳执行槽会话锁、快照事务、心跳和正常业务连接；禁止使用事务池模式的 PgBouncer 承载会话级 advisory lock。

## 数据与故障语义

迁移 `252_usage_export_tasks.sql` 新增任务、幂等键、下载凭证和独立额度表，不改变历史记录。领取采用 `FOR UPDATE SKIP LOCKED`，全局执行槽采用会话级 advisory lock。所有状态写入受状态和 generation 条件约束，最终发布在持锁连接执行；失去锁连接不能发布成功。

读取使用单个 `REPEATABLE READ READ ONLY` 事务，通过显式数据库游标每批 2,000 行。关联名称也在相同事务读取。没有额外 COUNT，排序追加唯一 ID。到第 1,000,001 条返回 `EXPORT_ROW_LIMIT`，不会发布截断结果。内部 JSONL 完整落盘、事务结束后才生成最终文件；失败时最多重试两次格式化/上传，使用原暂存数据，不重读新快照。读取中断或进程丢失快照的任务失败，须新建，不进行跨快照续读。

CSV 使用 BOM、标准 CSV 转义和公式文本防护。XLSX 使用 Excelize 固定版本及流式单工作表；ZIP writer 直接写文件，避免 Excelize 默认整包内存缓冲。文本与长数字显式作为字符串，普通 token/毫秒整数使用数值单元格，金额按原列精度格式化。发布前关闭文件、校验 ZIP CRC、计算 SHA-256/长度，完成存储后才标记成功。

后台每秒检查心跳、取消与临时目录预算；每分钟处理排队超时、失联任务、过期文件和孤儿暂存。临时文件增长以周期监测限制，部署仍应使用独立卷/文件系统配额提供硬磁盘上限。物理删除失败会重试，逻辑过期或删除立即禁止下载。S3 网络失败后的未完成分片由 SDK 中止及存储生命周期兜底。

## API 与权限

用户 `/api/v1/usage/exports`；管理员 `/api/v1/admin/usage/exports`。控制接口保留原认证、角色和全局面板保护，不计入 Heavy 额度。

| 方法 | 相对路径 | 行为 |
|---|---|---|
| POST | 空路径 | `Idempotency-Key` 创建，202；键冲突409 |
| GET | 空路径 | 最近24小时创建的任务分页列表 |
| GET | `/:id` | 状态、阶段、计数、快照、大小、过期时间 |
| POST | `/:id/cancel` | 取消排队或运行任务 |
| DELETE | `/:id` | 删除终态任务，立即撤销下载 |
| POST | `/:id/download-ticket` | 当前登录会话签发60秒单次凭证 |
| GET | `/:id/download` | 原生浏览器流式下载 |

下载凭证只保存 SHA-256，使用 HttpOnly、SameSite=Strict、精确路径的短期 Cookie 传递，不出现在 URL/JSON 或访问日志中。下载兑换重新检查创建者、当前账号状态、JWT版本/有效期、有效会话家族、管理员角色、会话绑定及面板访问约束。原生下载不携带长期登录 Token 查询参数。首版无 Range/断点续传；失败后重新申请凭证下载同一文件，不重新生成任务。

前端共享任务区域：有活动任务且页面可见时每5秒观察；隐藏或卸载停止观察，后台继续。429 遵守 Retry-After（秒数或HTTP日期），其余暂时错误采用2/4/8/16/30秒正向抖动退避，最多5次重试及120秒等待预算。通用 API 拦截器不自动重试任意写操作。导出开关关闭直接提示，不循环等待。

## 验证与上线门禁

在隔离 PostgreSQL 数据库运行测试；`internal/usageexport` 测试会重建该数据库的导出测试表，绝不能将 DSN 指向业务库。

```sh
USAGE_EXPORT_TEST_DSN='<isolated PostgreSQL DSN>' go test ./internal/usageexport -count=1
USAGE_EXPORT_TEST_DSN='<isolated PostgreSQL DSN>' go test ./internal/handler ./internal/repository -run TestUsageExport -count=1
USAGE_EXPORT_TEST_DSN='<isolated PostgreSQL DSN>' USAGE_EXPORT_CAPACITY=1 USAGE_EXPORT_CAPACITY_FORMAT=xlsx go test ./internal/usageexport -run TestExportCapacity -v -count=1
USAGE_EXPORT_TEST_DSN='<isolated PostgreSQL DSN>' USAGE_EXPORT_LOAD=1 go test ./internal/repository -run TestUsageExportLoadGate -v -count=1
```

S3 集成测试通过 `USAGE_EXPORT_TEST_S3` 指向独立 MinIO 测试实例，固定合成测试凭据仅用于该实例。容量测试支持 `USAGE_EXPORT_CAPACITY_ROWS`；百万条文件测试使用 PostgreSQL 游标生成合成字段，不等同于生产全字段数据分布。RSS 在 Linux 从 `/proc/self/status` 每25毫秒采样。负载测试使用百万条实际 usage_logs 结构的合成记录，1/2任务各读50万条并生成CSV，同时测 COUNT+100行列表 SQL 的P95；不是完整HTTP P95。

本次验收结果及所有失败重跑保存在交付目录 `VERIFICATION.txt`。共享开发机的缓存和其他编译负载会影响时间，不能从某次并行查询比基线快推断性能提升。上线前必须在相同数据、稳定负载及部署等效环境复测完整HTTP P95≤基线1.20倍、百万条耗时/内存、真实字段长度与磁盘容量。通过后设置少量 `allowed_user_ids` 灰度；没有生产验收证据前保持默认关闭。

任务表可查询排队时间、读取进度、快照、完成时间、文件大小、失败码、存储预留；结构化日志记录任务ID、条数、执行耗时及下载字节数，不记录正文、凭证或存储密钥。运营应观察排队/执行数量、失败/取消率、长事务、磁盘及下载流量；不得以后台任务成功数替代用户下载成功率。

回退顺序：关闭新建、等待结束或取消活动任务、停止工作者，再回退应用。迁移为追加式，回退不删表，不清理其他业务文件。交付回滚脚本只恢复指定源码副本，不能代替数据库或部署回滚。
