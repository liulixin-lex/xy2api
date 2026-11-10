# Linux 安装脚本与 systemd

本文介绍用官方安装脚本 `install.sh` 把 XY2API 二进制安装到 Linux 服务器、作为 systemd 服务运行，以及安装后的设置向导、升级、回滚和卸载。适合不想用容器、已经自己维护 PostgreSQL 和 Redis 的运维人员。容器部署见 [Docker Compose 部署](docker.md)。

## 支持的平台

官方发布包只有三种：

| 发布包 | 平台 |
| --- | --- |
| `xy2api_<版本>_linux_amd64.tar.gz` | Linux x86-64 |
| `xy2api_<版本>_windows_amd64.zip` | Windows x86-64 |
| `xy2api_<版本>_darwin_arm64.tar.gz` | macOS Apple 芯片 |

`install.sh` 只面向 Linux + systemd，实际可用的是 `linux_amd64`。在 ARM 服务器上安装 0.6.0 及以后的版本时，脚本会提示该版本没有此平台的安装包并退出；这类机器请 [从源码构建](source.md)。Windows 和 macOS 的发布包解压后直接运行 `xy2api`（Windows 为 `xy2api.exe`），首次启动同样进入下文的设置向导。

## 前置条件

- root 权限（`sudo`）、Bash 4 或更新、`curl`、`tar`、`sha256sum`、systemd；
- 已安装并运行的 PostgreSQL 和 Redis，建议 PostgreSQL 15 及以上、Redis 7 及以上（官方 Compose 文件使用 PostgreSQL 18 和 Redis 8）；
- 如果要使用管理后台的数据库备份，服务器上需要安装主版本不低于数据库服务器的 `pg_dump` 和 `psql`。

## 安装

安装最新版本：

```bash
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash
```

安装指定版本（版本号可以带或不带 `v`）：

```bash
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- install --version 0.6.0
```

在终端里运行时，脚本会依次询问界面语言、监听地址（`0.0.0.0` 监听所有网卡，`127.0.0.1` 只允许本机访问）和端口（默认 `8080`）；没有可交互的终端时使用默认值。随后脚本会：

1. 从 GitHub Releases 下载对应的发布包，并用同一版本的 `checksums.txt` 校验 SHA-256；
2. 把程序安装到 `/opt/xy2api/xy2api`，发布包里的 `deploy/` 示例文件复制到 `/opt/xy2api/`；
3. 创建系统用户 `xy2api`，创建 `/opt/xy2api/data` 和 `/etc/xy2api` 并设置属主；
4. 写入 `/etc/systemd/system/xy2api.service`（监听地址和端口写在其中的 `Environment=` 里）；
5. 启动服务并设置开机自启，最后打印访问地址。

脚本查询 GitHub API 时匿名访问；遇到 API 频率限制可以提供令牌，例如 `curl -sSL <脚本地址> | sudo UPDATE_GITHUB_TOKEN=<令牌> bash`。令牌只用于 `api.github.com`，下载发布包始终匿名。

## 设置向导

安装完成后服务处于向导模式。在浏览器打开 `http://<服务器 IP>:<端口>`，按页面填写：

- PostgreSQL 连接（主机、端口、用户、密码、库名、SSL 模式）。库不存在时向导会尝试创建；
- Redis 连接；
- 管理员邮箱与密码（8–72 字节）。

向导会测试连接、执行数据库迁移、创建管理员，把配置写入 `/opt/xy2api/config.yaml` 并创建 `/opt/xy2api/.installed`，然后自动重启进入正常模式，用刚设置的管理员登录即可。

向导页面在完成之前不需要任何认证，能访问这个端口的人都可以完成初始化。请装好后立刻完成向导；服务器直接暴露在公网时，建议安装时选择 `127.0.0.1`，再通过 SSH 隧道访问：

```bash
ssh -L 8080:127.0.0.1:8080 <用户>@<服务器>
# 然后在本机浏览器打开 http://127.0.0.1:8080
```

没有浏览器时也可以用命令行向导：先 `sudo systemctl stop xy2api`，在 `/opt/xy2api` 目录下执行 `sudo -u xy2api ./xy2api -setup` 按提示输入，完成后 `sudo systemctl start xy2api`。

向导只写入监听地址、数据库、Redis、JWT 密钥和时区等基础配置，不会设置 `TOTP_ENCRYPTION_KEY`。启用 2FA、支付或保存加密配置之前，请按下文补上固定的加密密钥。

## 目录与文件

| 路径 | 内容 |
| --- | --- |
| `/opt/xy2api/xy2api` | 程序 |
| `/opt/xy2api/config.yaml` | 向导写入的配置（服务工作目录下的配置优先于 `/etc/xy2api`） |
| `/opt/xy2api/.installed` | 安装锁；与 `config.yaml` 都不存在时才会进入向导 |
| `/opt/xy2api/data/` | 插件、用量导出等运行数据 |
| `/opt/xy2api/xy2api.backup*` | 升级或换版本前自动备份的旧程序 |
| `/opt/xy2api/*.example*`、`*.yml` 等 | 发布包附带的部署示例文件 |
| `/etc/systemd/system/xy2api.service` | systemd 单元 |
| `/etc/xy2api/` | 配置搜索路径之一，安装脚本只创建空目录 |

服务单元以 `xy2api` 用户运行，`ProtectSystem=strict`，只有 `/opt/xy2api` 可写，并设置了 `Restart=always`、`RestartSec=5`。仓库里的 [`deploy/xy2api.service`](../../deploy/xy2api.service) 是同样内容的参考文件。

## 服务管理

```bash
sudo systemctl status xy2api     # 状态
sudo journalctl -u xy2api -f     # 实时日志
sudo systemctl restart xy2api    # 重启
sudo systemctl stop xy2api       # 停止
sudo systemctl enable xy2api     # 开机自启
```

## 修改监听地址、端口与其他环境变量

用 systemd 的覆盖配置修改，不要直接改单元文件：

```bash
sudo systemctl edit xy2api
```

```ini
[Service]
Environment=SERVER_HOST=127.0.0.1
Environment=SERVER_PORT=3000
Environment=TOTP_ENCRYPTION_KEY=<openssl rand -hex 32 的输出>
```

```bash
sudo systemctl daemon-reload
sudo systemctl restart xy2api
```

环境变量优先于 `config.yaml`。变量较多或含密钥时，可以改用 `EnvironmentFile=/etc/xy2api/xy2api.env`（文件权限 `600`），或直接写进 `/opt/xy2api/config.yaml` 对应的配置项（例如 `totp.encryption_key`）。可用的配置项见 [配置文件与环境变量](configuration.md)；Gemini OAuth 需要的变量见 [Gemini](../platforms/gemini.md)。

## 升级、换版本与回滚

```bash
# 升级到最新版本
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- upgrade

# 升级或回退到指定版本
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- upgrade --version 0.6.1
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- rollback 0.6.0

# 列出最近的可用版本
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | bash -s -- list-versions
```

脚本会停止服务、备份当前程序（`upgrade` 备份为 `xy2api.backup`，指定版本时备份为 `xy2api.backup.<原版本>`）、下载新版本并重新启动。配置和数据不动。也可以在管理后台的版本信息里使用“立即更新”和“版本回退”。数据库迁移、回滚的限制和备份方法见 [升级、回滚与备份](upgrade.md)。

## 卸载

```bash
curl -sSL https://raw.githubusercontent.com/liulixin-lex/xy2api/main/deploy/install.sh | sudo bash -s -- uninstall -y
```

卸载会停止并删除服务、删除整个 `/opt/xy2api`（包括 `config.yaml` 和 `data/`）以及 `xy2api` 用户；交互运行时还会询问是否删除 `/etc/xy2api`，加 `--purge` 则直接删除。数据库和 Redis 里的数据不受影响。需要保留配置时，先备份 `/opt/xy2api/config.yaml`。

## 排障

| 现象 | 处理 |
| --- | --- |
| 服务启动失败 | `sudo journalctl -u xy2api -n 50` 查看错误 |
| 一直停在设置向导 | `/opt/xy2api` 下没有 `config.yaml` 和 `.installed`；配置放在别处时把它复制到 `/opt/xy2api/` |
| 数据库或 Redis 连接失败 | `sudo systemctl status postgresql` / `redis` 确认服务运行，核对 `config.yaml` 里的地址、用户和密码 |
| 端口被占用 | 按上文修改 `SERVER_PORT` |
| 在线更新后没有重新启动 | 在线更新以退出码 0 结束进程，依赖 `Restart=always` 拉起；不要改成 `Restart=on-failure` |
| 提示该版本没有此平台的安装包 | 服务器不是 x86-64，见 [从源码构建](source.md) |
