# OpenAI TOTP Worker 部署

OpenAI TOTP Worker 是一个独立运行的本地进程，负责执行管理员在后台明确创建的 OpenAI 邮箱 / 密码 / TOTP 登录导入和 2FA 密钥轮换任务。它只调用本机安装的、固定版本的 toSub2 协议程序，不依赖任何第三方托管的登录服务。本文说明 Worker 的安装、与 XY2API 的对接配置、预检与启动，以及 journal 的保管与排障。功能本身的使用方法见 [OpenAI 账号 2FA 登录导入](../guides/openai-totp.md)。不使用这个功能时不需要部署 Worker。

仓库里的示例文件不会自动安装或启动服务，也不包含任何真实账号或密钥。

## 工作方式

- Worker 通过预检后，以独立的 Bearer 密钥（与管理员令牌无关）轮询 XY2API 的 `/api/v1/internal/openai-totp/claim` 领取任务，并报告 journal 与运行时已就绪；就绪状态在服务端保持 60 秒。
- 执行任务期间，独立线程按心跳间隔（默认 10 秒）只发送心跳、不领取新任务。服务端租约为 2 分钟，由心跳续期；单个任务最长运行 15 分钟。
- 任何心跳或预检失败都会取消当前任务；超过 45 秒未能确认续租也会停止后续操作，并结束整个子进程组。已经发出的上游请求可能无法撤回，服务端把这类任务标为结果不确定，旧的领取凭证不能再完成或续期。
- 轮换过程中，新密钥先以 AES-256-GCM 加密写入本地 journal（文件与目录都 `fsync`），确认服务端已保存后才激活。注册新密钥之后的任何失败都不会自动重试注册或激活；重启后只会把 journal 中尚未确认的候选密钥补交给“结果不确定且还没有候选”的任务。
- 访问上游时使用账号配置的 `proxy_url`；调用 XY2API 内部接口时忽略进程的代理环境变量，也不跟随重定向。

## 前提

| 项目 | 要求 |
| --- | --- |
| 主机 | Linux 或 macOS。Windows 原生不支持进程组终止和 `flock`，请使用 Linux 环境 |
| Python | 3.12 或更新 |
| Node.js | 22 或更新 |
| XY2API | 已设置固定的 `TOTP_ENCRYPTION_KEY`，并与 Worker 配置同一个 `XY2API_OPENAI_TOTP_WORKER_SECRET` |
| journal 存储 | 持久、可写、支持文件与目录 `fsync` 和原子重命名的本地文件系统。不要用容器的临时层或网络盘 |
| 时钟 | 主机时间保持同步（TOTP 依赖时间窗） |

每个 Worker 实例使用自己的 journal 目录和 `worker_id`；同一个 journal 上启动第二个进程会被拒绝。

## 1. 配置 XY2API

XY2API 只有在同时满足以下两点时才开放这个功能：设置了固定的 `TOTP_ENCRYPTION_KEY`，且 `XY2API_OPENAI_TOTP_WORKER_SECRET`（配置项 `openai_totp.worker_secret`）至少 32 个字符。生成 Worker 密钥：

```bash
openssl rand -hex 32
```

密钥要求至少 32 个可打印 ASCII 字符、不含空白，与管理员令牌分开保管，不要写进日志或提交到仓库。

**Docker Compose**：仓库自带的 Compose 文件不会转发这个变量。在部署目录创建覆盖文件，并把变量写进私有的 `.env`：

```yaml
# compose.openai-totp.yml
services:
  xy2api:
    environment:
      XY2API_OPENAI_TOTP_WORKER_SECRET: ${XY2API_OPENAI_TOTP_WORKER_SECRET:?set a private worker secret}
```

之后的 Compose 命令都同时指定原文件与覆盖文件，例如 `docker compose -f docker-compose.yml -f compose.openai-totp.yml up -d`。`TOTP_ENCRYPTION_KEY` 已由原 Compose 文件传入（`standalone` 版除外，需要一并写进覆盖文件）。

**systemd**：`sudo systemctl edit xy2api`，加入 `Environment=XY2API_OPENAI_TOTP_WORKER_SECRET=...`（以及 `TOTP_ENCRYPTION_KEY`），然后重启服务。

## 2. 安装 Worker

以下以 `/opt/xy2api-totp` 为安装目录、`xy2api-totp` 为运行用户，路径可以替换。Worker 需要 XY2API 仓库的完整 `tools/` 目录（运行时清单、Python 模块、Node 适配器、预检与 journal 程序），请检出与服务端相同的版本，不要只复制单个文件。

```bash
sudo useradd -r -s /usr/sbin/nologin -d /opt/xy2api-totp xy2api-totp
sudo install -d -m 0755 /opt/xy2api-totp

# XY2API 仓库（与服务端版本一致）
sudo git clone --branch v<版本> https://github.com/liulixin-lex/xy2api.git /opt/xy2api-totp/xy2api

# Python 依赖（全部固定版本）
sudo python3 -m venv /opt/xy2api-totp/venv
sudo /opt/xy2api-totp/venv/bin/python -m pip install --only-binary=:all: \
  -r /opt/xy2api-totp/xy2api/tools/openai_totp_requirements.txt

# toSub2 协议程序，检出固定提交
sudo git clone https://github.com/poxiao33/toSub2.git /opt/xy2api-totp/tosub2
sudo git -C /opt/xy2api-totp/tosub2 checkout --detach 8548397e89bf80e508eda64a87e0d556d43abc84

# 只安装锁定的 Node 运行依赖（锁文件含完整的传递依赖版本与 integrity）
sudo cp /opt/xy2api-totp/xy2api/tools/openai-totp-runtime/package.json \
        /opt/xy2api-totp/xy2api/tools/openai-totp-runtime/package-lock.json \
        /opt/xy2api-totp/tosub2/
cd /opt/xy2api-totp/tosub2 && sudo npm ci --omit=dev --ignore-scripts --no-audit --no-fund
```

- 固定的 toSub2 提交与它 17 个源码文件的 SHA-256 记录在 `tools/openai-totp-runtime/source.json`，预检会拒绝缺失或被改动的源码。升级依赖或协议源码需要随 XY2API 版本一起更新，不要直接跟随 toSub2 的最新代码。
- toSub2 使用 MIT 许可证，请保留 checkout 中的 LICENSE。来源说明见 `tools/openai-totp-runtime/NOTICE.md`。
- 程序、venv 和 toSub2 目录对运行用户只读。journal 目录归运行用户所有，权限 `0700`，其中的密钥、记录和锁文件为 `0600`。不要把 journal 放在 Git 管理的目录里。

## 3. 配置 Worker

以 [`deploy/openai-totp-worker.env.example`](../../deploy/openai-totp-worker.env.example) 为模板创建 `/etc/xy2api/openai-totp-worker.env`（目录不存在时先创建），属主 root、权限 `0600`：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `XY2API_OPENAI_TOTP_API_URL` | `http://127.0.0.1:8080` | XY2API 地址，只能是 origin，不能带路径、凭据、查询参数或片段。`http` 只允许 `localhost`、`127.0.0.1`、`::1`，远程地址必须 HTTPS |
| `XY2API_OPENAI_TOTP_WORKER_SECRET` | 无，必填 | 与 XY2API 相同的 Worker 密钥 |
| `XY2API_OPENAI_TOTP_WORKER_ID` | `totp-<随机 UUID>` | 1–128 个字母、数字、`.`、`_` 或 `-` |
| `TOSUB2_ROOT` | 无，必填 | toSub2 checkout 的绝对路径 |
| `OPENAI_TOTP_JOURNAL_DIR` | 无，必填 | journal 的绝对路径。程序不会自动删除历史记录 |
| `NODE_EXECUTABLE` | `node` | Node.js 可执行文件，systemd 下建议写绝对路径 |
| `XY2API_OPENAI_TOTP_POLL_SECONDS` | `5` | 轮询间隔，1–60 |
| `XY2API_OPENAI_TOTP_HEARTBEAT_SECONDS` | `10` | 心跳间隔，1–15 |
| `XY2API_OPENAI_TOTP_REQUEST_TIMEOUT` | `10` | 内部接口请求超时（秒），1–15 |

## 4. 预检与启动

先做离线预检。它检查 Node→Python/curl_cffi 调用链、依赖版本与源码校验和、journal 的真实写入和文件 / 目录 `fsync`，不访问 XY2API 或 OpenAI，不注册就绪状态：

```bash
sudo install -d -m 0700 -o xy2api-totp -g xy2api-totp /var/lib/xy2api-totp
sudo sh -c 'set -a; . /etc/xy2api/openai-totp-worker.env; set +a;
  cd /opt/xy2api-totp/xy2api &&
  exec runuser -u xy2api-totp -- /opt/xy2api-totp/venv/bin/python tools/openai_totp_worker.py --check'
# 只输出：openai_totp_worker: preflight_ok
```

环境文件权限是 `0600`，所以由 root 读取后再切换到运行用户执行。journal 目录不存在时会以 `0700` 自动创建。

Linux 上用 systemd 运行。参考 [`deploy/xy2api-openai-totp-worker.service`](../../deploy/xy2api-openai-totp-worker.service)，按实际情况调整 `User`、`Group`、安装目录和 Node 路径：

```bash
sudo cp /opt/xy2api-totp/xy2api/deploy/xy2api-openai-totp-worker.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now xy2api-openai-totp-worker
```

示例单元会在启动前执行一次 `--check`，关闭 core dump（`LimitCORE=0`），只允许写 `/var/lib/xy2api-totp`（由 `StateDirectory` 以 `0700` 创建），并以 `KillMode=control-group` 在停止时结束所有 Node / Python 子进程。

在容器里运行 Worker 时，必须显式挂载持久的 journal 目录。容器里的 `127.0.0.1` 是 Worker 容器自己，不是 XY2API；跨容器访问请走 HTTPS 的私有入口。

## Journal 保管

- `key.bin` 与 `*.sealed` 必须一起备份到私有的加密备份里。缺少 `key.bin` 而存在密文时，程序不会生成新密钥。
- 备份时先停止 Worker，或使用一致性的文件系统快照，避免只复制到一部分文件。残留的 `.write-*` 是未完成的写入，不能代替已提交的 `*.sealed`。
- 文件损坏、权限过宽、软链接或硬链接都会让预检失败。
- 出现失败时保留现场：不要删除 journal、重建密钥、改写候选、直接重启轮换任务，也不要把候选密钥写进普通账号的凭据。管理员完成恢复或安全导出、确认任务终态并备份后，再按你的密钥保留策略手动归档或销毁。程序不会自动清理凭据。
- 临时会话文件放在私有目录，正常结束时删除；异常断电可能遗留，操作系统的临时目录也要按敏感数据管理。

## 观测与排障

Worker 的日志只输出固定的事件码，不输出密钥、原始异常或子进程的输出：

| 日志 | 含义 |
| --- | --- |
| `openai_totp_worker: preflight_ok` | `--check` 通过 |
| `openai_totp_worker: startup_failed` | 配置校验、预检或 journal 加锁失败，进程退出 |
| `openai_totp_worker: task_poll_failed` | 某次领取或执行任务失败，Worker 会在下一个轮询周期继续 |

`startup_failed` 的常见原因：

- `XY2API_OPENAI_TOTP_API_URL` 带了路径，或对非本机地址使用了 `http`；
- Worker 密钥少于 32 个字符或含空白；
- `TOSUB2_ROOT`、`OPENAI_TOTP_JOURNAL_DIR` 不是绝对路径，`NODE_EXECUTABLE` 找不到；
- `curl_cffi` 版本不对，或 toSub2 源码与 `source.json` 不一致；
- journal 目录权限过宽、属主不是运行用户，或已被另一个 Worker 进程占用。

后台提示功能不可用时，先确认 XY2API 进程确实拿到了 `TOTP_ENCRYPTION_KEY` 和 Worker 密钥（容器部署检查覆盖文件是否生效），再确认 Worker 正在运行并能访问 `XY2API_OPENAI_TOTP_API_URL`。

## 升级

升级 XY2API 后，把 Worker 的仓库检出切到同一版本，重新安装 `tools/openai_totp_requirements.txt` 中的 Python 依赖；`source.json` 中的 toSub2 提交有变化时，同时更新 toSub2 检出和 Node 依赖，然后运行 `--check` 并重启 Worker。
