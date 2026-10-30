# OpenAI 登录与 2FA 本地 Worker

本 Worker 领取管理员明确创建的 OpenAI 邮箱/密码/TOTP 登录或轮换任务。上游只运行本地固定版本的 toSub2 协议程序；没有第三方托管登录服务。部署示例不会自动安装或启动服务，也不包含任何真实账号或密钥。

## 安装前提

- POSIX 主机（Linux/macOS），Python 3.12 或更新版本，Node.js 22 或更新版本。参考 Node 22.22.2；本次 mock 与离线预检使用 Python 3.14.8、Node 24.21.0。Windows 原生不支持进程组终止和 `flock`，可用 Linux 环境。
- API 已应用托管 TOTP 迁移，并设置固定的 `TOTP_ENCRYPTION_KEY`。密钥不可随重启变化；API 端和 Worker 端设置同一个至少 32 字符随机 `XY2API_OPENAI_TOTP_WORKER_SECRET`，与管理员 token 分离。
- journal 必须是持久、可写并支持文件和目录 `fsync` 的本地 POSIX 文件系统；不得使用临时容器层或不保证 fsync/原子 rename 的网络盘。每个 Worker 实例独立 journal 与 worker_id；同一 journal 的第二个进程会被拒绝。
- 主机时钟保持同步；上游访问使用账号配置的 `proxy_url`，内部 API 请求不读取进程的代理环境变量。

## 固定依赖与安装

以下命令在仓库根目录执行。示例目录是可替换的安装位置；先创建专用用户和目录，再以该用户安装。保留 toSub2 的 MIT LICENSE。worker/rotation/journal 源自 `ranxi2001/sub2api` 固定提交 `12a49ef4ef6a2f116389701ac8f1fe620f27bbf5` 的相关工具（LGPL-3.0，与本仓库相同，见根目录 LICENSE），已独立适配 XY2API API、租约与预检。

```bash
install -d -m 0700 /opt/xy2api-totp /var/lib/xy2api-totp/journal
python3 -m venv /opt/xy2api-totp/venv
/opt/xy2api-totp/venv/bin/python -m pip install --only-binary=:all: \
  -r tools/openai_totp_requirements.txt

git clone https://github.com/poxiao33/toSub2.git /opt/xy2api-totp/tosub2
git -C /opt/xy2api-totp/tosub2 checkout --detach 8548397e89bf80e508eda64a87e0d556d43abc84
# 只安装固定的协议运行依赖，锁文件含完整传递依赖版本与 integrity。
cp tools/openai-totp-runtime/package.json tools/openai-totp-runtime/package-lock.json \
  /opt/xy2api-totp/tosub2/
cd /opt/xy2api-totp/tosub2
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
```

`curl_cffi==0.15.0`、`jsdom==26.1.0` 固定；Python 传递依赖也逐项固定。`tools/openai-totp-runtime/source.json` 记录上述 toSub2 提交的 17 个源码 SHA-256；预检拒绝缺失或被更改的源码。升级依赖或协议源码须重新审阅、更新固定版本和完整 mock 门禁，不能直接跟随上游 HEAD。

把仓库及 `tools/` 整体安装到 `/opt/xy2api-totp/xy2api`，包含 runtime 清单、Python 模块、Node adapter/preflight/journal。不要只复制 worker 单文件。程序/venv/runtime 对 Worker 用户只读；journal 为该用户所有，目录 `0700`，key、记录及锁文件 `0600`。不要将 journal 放在 checkout 或 Git 管理目录内。

## 配置与启动

使用 [环境变量示例](openai-totp-worker.env.example) 建立私有配置文件 `/etc/xy2api/openai-totp-worker.env`，权限 `0600`。随机 worker secret 可通过 `openssl rand -hex 32` 在本地生成并安全保存；不要贴入日志或提交。

API 进程也必须收到同一个 `XY2API_OPENAI_TOTP_WORKER_SECRET`。已有 Compose 文件不会自动把 `.env` 中的任意变量传入容器；使用 Compose 部署时，在部署目录创建私有覆盖文件，例如 `compose.openai-totp.yml`：

```yaml
services:
  xy2api:
    environment:
      XY2API_OPENAI_TOTP_WORKER_SECRET: ${XY2API_OPENAI_TOTP_WORKER_SECRET:?set a private worker secret}
```

把变量放入该部署的私有 `.env`，并在后续 Compose 操作中同时指定原文件与此覆盖文件（`-f docker-compose.yml -f compose.openai-totp.yml`）；`local`/`dev` 部署用对应原文件。固定 `TOTP_ENCRYPTION_KEY` 已由原 Compose 文件映射，需同时配置。原生 API 由其服务管理器注入这两个环境变量。没有启用本功能时，不需配置 Worker 密钥，接口保持不可运行。

| 环境变量 | 默认值或要求 |
| --- | --- |
| `XY2API_OPENAI_TOTP_API_URL` | `http://127.0.0.1:8080`；仅 URL origin，不能带路径、凭据、query、fragment；远程必须 HTTPS |
| `XY2API_OPENAI_TOTP_WORKER_SECRET` | 必填，至少 32 个可打印 ASCII 字符，无空白；API 与 Worker 相同 |
| `XY2API_OPENAI_TOTP_WORKER_ID` | 默认随机 UUID 标识；可显式设为 1–128 位字母、数字、点、下划线或横线 |
| `TOSUB2_ROOT` | 固定 toSub2 checkout 的绝对路径，含 `src/protocol-login.mjs` |
| `OPENAI_TOTP_JOURNAL_DIR` | 持久 journal 的绝对路径；程序不会自动删除历史记录 |
| `NODE_EXECUTABLE` | 默认 `node`；systemd 示例需按实际安装位置调整 |
| `XY2API_OPENAI_TOTP_POLL_SECONDS` | 默认 5 秒，范围 1–60 |
| `XY2API_OPENAI_TOTP_HEARTBEAT_SECONDS` | 默认 10 秒，范围 1–15 |
| `XY2API_OPENAI_TOTP_REQUEST_TIMEOUT` | 默认 10 秒，范围 1–15 |

在仓库根目录进行离线检查；这里只测试 Node→Python/curl_cffi 配置、版本/源码、journal 真实写入和文件/目录 fsync，不注册 readiness，不访问 API 或 OpenAI，不执行 MFA：

```bash
set -a
. /etc/xy2api/openai-totp-worker.env
set +a
/opt/xy2api-totp/venv/bin/python tools/openai_totp_worker.py --check
# 应只输出：openai_totp_worker: preflight_ok
/opt/xy2api-totp/venv/bin/python tools/openai_totp_worker.py
```

生产 Linux 可审阅并安装 [systemd 示例](xy2api-openai-totp-worker.service)，调整 `User`、`Group`、安装目录和 Node 路径，再启动。示例已关闭 core dump，限制写目录并以 control-group 终止所有 Node/Python 子进程。单独容器运行时必须显式挂载持久 journal；不要把 API 的容器 loopback 误作 Worker 容器自身地址，跨容器应经 HTTPS 私有入口访问。

## 协议、租约和故障恢复

- Worker 在预检通过后，以独立 Bearer 调用 `/api/v1/internal/openai-totp/claim`，提交 `journal_ready:true,runtime_ready:true`。运行任务期间，独立线程每 10 秒只发送 `heartbeat_only:true`，不再领取任务。
- 任务心跳附 `task_id,claim_token`；后端核对当前 holder、worker_id、未过期租约后续租。后端租约为 120 秒，绝对运行上限 15 分钟；ready 持续 60 秒。无 task 的心跳只登记 readiness。成功心跳响应 `{code:0,data:null}`。
- 任何心跳/预检失败都会取消任务；超过 45 秒未确认续租也会停止后续操作。可终止的 runner 整个进程组立即结束。已经发出的 HTTP 请求可能无法撤回；后端以 uncertain 处理，旧 claim 不能完成、续活或重新领取这次操作。
- 所有 phase/finish 绑定 claim_token。严格顺序：`enrolling` → enroll 一次 → 候选 AES-256-GCM journal 写入/file fsync/atomic rename/directory fsync → `prepared(secret)` 数据库同步提交确认 → journal ACK → `activating` → activate 一次 → `verifying`。
- 验证使用独立 runner/新 cookie jar 确认新 key 接受；等到下一 TOTP 时间窗，再用独立 runner确认旧 key 明确被 MFA 拒绝。密码拒绝、网络失败、403、挑战页、异常退出都不能算旧 key 被拒绝。Node adapter 禁用上游安全校验页面的自动重放，并强制保留 TLS 证书验证。
- enroll 后任何失败、丢 ACK、失联、激活超时均不得自动 enroll/activate 重试。重启只尝试把未 ACK journal 候选通过 `/recover` 补回 **uncertain 且尚无 candidate** 的任务；active/prepared 的 409 会保留 journal 并继续轮询，不改候选、不阻塞 readiness。
- 管理员明确选择 `verify_new` 或 `verify_old` 后只验证已保存的 key，不做上游 mutation。候选存在时需验证反向 key 拒绝；候选未拿到时，`verify_old` 只确认旧 key 可登录以解除锁。`verify_new` 必须有候选。
- 登录任务通过一次独立认证后的 `/api/auth/session` 取完整 JSON，Worker 核对规范邮箱与必要字段，然后只交给服务端加密保存/校验/导入，浏览器不经过 session JSON。

## Journal 保管与操作边界

`key.bin` 与 `*.sealed` 必须一起备份到私有加密备份；缺 key 且存在密文时不会生成新 key。损坏、权限过宽、软链/硬链记录会使预检失败。备份时停止 Worker 或采用一致性文件系统快照，避免只复制部分文件。遗留 `.write-*` 为未完成写入，不能代替已提交 `*.sealed`。

保留失败现场；不要删除 journal、重建 key、改写 candidate、直接重启轮换任务或把候选写入普通账号 credentials。完成管理员恢复/安全导出并确认任务终态与备份后，再按本地秘密保留策略人工归档/销毁；程序不自动清理凭据。日志只输出固定事件码，不输出秘密、原始异常、子进程 stdout/stderr。临时 session 文件位于私有目录，正常结束会删除；异常断电可能遗留，操作系统临时目录也须作为敏感数据管理。

## Mock 验证

```bash
python3 -m unittest tools.tests.test_openai_totp_rotation tools.tests.test_openai_totp_worker -v
node --test tools/tests/test_openai_totp_journal.mjs
```

覆盖双写顺序、journal/fsync/数据库 ACK 失败、激活超时不重试、旧 key 拒绝的证据、独立进程、Bearer/无重定向/无环境代理、持有者心跳/取消/过期、journal 恢复、身份不符和错误脱敏。全部使用合成账号、假 API、假 session 或本地子进程。Mock 不能证明非公开 OpenAI 网页协议对真实账号兼容；本次未执行真实登录或 MFA。
