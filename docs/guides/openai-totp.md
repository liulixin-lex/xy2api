# OpenAI 账号 2FA 登录导入

本功能让超级管理员用上游 OpenAI 账号的邮箱、密码和 TOTP 密钥直接登录并导入账号，并在 XY2API 中加密保存这些登录凭据，必要时更换（轮换）OpenAI 验证器密钥。它适合需要批量接入、维护开启了两步验证的 OpenAI 账号的站点。本文面向超级管理员；登录与轮换由本地 worker 执行，worker 的安装见 [OpenAI TOTP Worker 部署](../deployment/openai-totp-worker.md)。

这里管理的是**上游 OpenAI 账号**的凭据，与用户登录 XY2API 时使用的平台两步验证分开存储，互不影响。

## 前提

| 条件 | 说明 |
|---|---|
| 固定的 `TOTP_ENCRYPTION_KEY` | 登录秘密用它加密保存。未配置固定密钥时拒绝保存和运行任务 |
| 管理员已启用平台 TOTP | 保存、轮换、恢复、导出和确认导入都要求当前管理员已启用平台两步验证，并通过近期二次验证 |
| 就绪的 worker | 需要匹配版本的 worker、登录运行时及其依赖，以及可写、支持同步到磁盘的持久恢复目录。没有就绪的 worker 时拒绝新建登录和轮换任务 |
| worker 密钥 | worker 使用独立的高强度 Bearer 密钥 `XY2API_OPENAI_TOTP_WORKER_SECRET`（配置键 `openai_totp.worker_secret`），与管理台凭据分开 |

上述二次验证要求无条件生效，不受全局 step-up 开关影响，全局 admin API Key 也不能代替已验证的登录会话。普通管理员没有访问权限。不要把账号秘密交给第三方托管的登录服务。

## 登录导入

1. 进入“账号管理”，在工具栏的“更多操作”菜单中选择“2FA登录导入”。
2. 填写 OpenAI 登录邮箱、密码、TOTP 密钥（Base32，不是六位验证码），可选填账号名称、登录代理和账号分组。
3. 点击“开始登录”。worker 完成登录后，服务端验证返回的 Session 身份并加密保存，浏览器不接收 Session 令牌。
4. 状态变为“待导入”后点击“导入账号”，沿用 Session JSON 导入逻辑创建账号。

- 直接登录导入总是创建新账号，不会覆盖已有账号的登录凭据。重复确认同一个任务返回原导入结果。
- 关闭弹窗会清空表单，但服务端任务会继续。记下任务编号，之后可以用“恢复已有登录任务”继续查询和导入。
- 登录遇到验证码、安全挑战或网络错误时显示失败，不会自动重试上游登录。

原有的 JSON 文件、Session/PAT 导入方式不受影响，也不需要 worker。

## 已有账号的凭据管理

在 OpenAI OAuth 母账号的操作菜单中选择“管理2FA”，可以：

- **保存登录凭据**：填写与账号邮箱一致的密码和当前 TOTP 密钥。已保存的密码与密钥不会回填到表单。影子账号、API Key 账号以及其他平台不能使用。
- **更换2FA密钥**：让 worker 登录 OpenAI，申请并激活新的验证器密钥。这会替换 OpenAI 上的验证器，请保留页面查看验证结果。
- **安全导出**：明确选择导出来源——当前密钥（任务已解决时可导出）、候选密钥或旧密钥（用于不确定任务的恢复）。密码需要单独勾选才包含在 JSON 文件中。可以显示验证器二维码或下载 JSON。

保存的登录秘密单独存储；普通账号的详情、列表、复制和批量导出都不携带这些密码与 TOTP 密钥。导出响应和 worker 凭据响应带 `Cache-Control: no-store`；请求审计只记录动作和安全元数据，不记录登录秘密和导出内容。页面关闭、切换账号时会清理输入和二维码。下载的文件包含秘密，请自行安全保管。

## 轮换与中断恢复

轮换任务依次经过 `queued`、`running`、`enrolling`、`prepared`、`activating`、`verifying`，最终为 `succeeded`、`failed` 或 `uncertain`。

- 新的候选密钥必须先写入加密的本地恢复日志并同步到磁盘，再由数据库确认持久化，之后才允许激活。
- 激活请求、数据库确认或 worker 连接出现不确定结果时，任务进入 `uncertain`（界面显示“需要恢复”）：锁定凭据修改和新的轮换，数据库同时阻止删除、软删除或更改该账号的身份字段，以保留恢复材料。
- 恢复时选择“验证新密钥”或“验证旧密钥”。恢复只做验证，不会再次申请或激活新密钥；验证使用独立的登录会话。只有明确的 TOTP 拒绝才判定旧密钥失效，网络故障、密码错误和安全挑战都不算；无法确认时继续保持不确定状态。
- 如果提示候选密钥缺失，先从 worker 的恢复日志中恢复，再验证新密钥。

**不要**通过删除任务、修改数据库状态或删除本地恢复日志来解锁。应恢复原 worker、原持久目录及其密钥，补回已写入日志的候选，然后执行验证恢复。任务过期后，旧 worker 不能复活租约或提交成功结果。

## 观测与排障

任务失败时界面显示错误码：

| 错误码 | 含义 |
|---|---|
| `login_failed` | 登录凭据未通过验证 |
| `mfa_rejected` | 上游拒绝了二次验证码 |
| `verification_failed` | 密钥验证未通过 |
| `runtime_unavailable` | 登录运行环境不可用 |
| `journal_unavailable` | 持久化恢复日志不可用 |
| `worker_timeout` | worker 超时或租约已失效 |
| `identity_mismatch` | 登录会话身份与指定账号不匹配 |
| `operation_failed` | 操作未能完成 |

“没有就绪的 worker”时，启动具备登录运行环境与持久化恢复日志的 worker 后刷新页面。

### 接口

| 接口 | 说明 |
|---|---|
| `GET /api/v1/admin/accounts/:id/openai-totp` | 读取凭据与任务状态 |
| `PUT /api/v1/admin/accounts/:id/openai-totp` | 保存登录凭据（需二次验证） |
| `POST /api/v1/admin/accounts/:id/openai-totp/rotate` | 启动轮换（需二次验证） |
| `POST /api/v1/admin/accounts/:id/openai-totp/reconcile` | 验证新密钥或旧密钥以恢复不确定任务（需二次验证） |
| `POST /api/v1/admin/accounts/:id/openai-totp/export` | 导出（需二次验证） |
| `POST /api/v1/admin/accounts/openai-login` | 新建登录任务（需二次验证） |
| `GET /api/v1/admin/accounts/openai-login/:task_id` | 查询登录任务 |
| `POST /api/v1/admin/accounts/openai-login/:task_id/import` | 确认导入（需二次验证） |

worker 使用 `/api/v1/internal/openai-totp` 下的 `claim`、`:task_id/phase`、`:task_id/finish`、`recover` 内部接口，只接受 worker 密钥，每个领取结果另带任务令牌。远程访问必须使用 HTTPS，禁止重定向和凭据跨站发送。

## 限制与注意事项

- OpenAI 网页登录与 MFA 流程不是公开的稳定 API，上游随时可能改变登录或换绑行为。启用前请在目标账号上验证，并先在非关键账号上试用轮换。
- 回滚应用程序不能撤销 OpenAI 上已激活的密钥。

## 备份与回滚

- 备份必须同时包含数据库、`TOTP_ENCRYPTION_KEY`、worker 的加密恢复目录及其加密密钥。缺少任一部分，都可能无法恢复正在轮换的账号。
- 本功能的表由迁移 `285_openai_totp.sql` 创建（专用凭据、任务和 worker 就绪状态表）。
- 回滚前先停止新任务、解决所有不确定任务，并核对当前有效密钥。不要清空仍有未解决任务的恢复目录。通用流程见[升级与回滚](../deployment/upgrade.md)。
