# 从源码构建

本文说明如何从源码构建内嵌前端的 XY2API 程序和容器镜像，以及构建后如何首次运行。官方只发布 Linux x86-64、Windows x86-64 和 macOS Apple 芯片的程序包，容器镜像只有 `linux/amd64`；在 ARM 服务器、Intel Mac 上部署，或需要自行修改代码时，按本文构建。本地开发环境、测试与代码生成见 [本地开发](../development/local-setup.md)。

## 前置条件

| 工具 | 版本 |
| --- | --- |
| Go | 以 `backend/go.mod` 为准（当前为 1.27.3） |
| Node.js | 24（CI 使用的版本），最低 22 |
| pnpm | 9 |
| Git、make | 任意较新版本 |

运行时还需要 PostgreSQL 和 Redis，要求同 [Linux 安装脚本与 systemd](binary.md#前置条件)。

## 构建程序

```bash
git clone https://github.com/liulixin-lex/xy2api.git
cd xy2api
git checkout v<版本>                       # 构建发布版本时切到对应标签
pnpm --dir frontend install --frozen-lockfile
make build
```

根目录的 `make build` 先构建前端（产物输出到 `backend/internal/web/dist/`），再以 `embed` 标签编译后端，生成内嵌前端的单个程序 `backend/bin/server`。部署时可以把它改名为 `xy2api`。

| 命令 | 结果 |
| --- | --- |
| `make build` | 前端 + 内嵌前端的后端，推荐 |
| `make build-frontend` | 只构建前端 |
| `make build-backend` | 只编译后端，不内嵌前端；这样的程序没有管理界面 |

版本号由 `backend/scripts/resolve-version.sh` 决定：在打了标签的提交上取标签（去掉 `v`），否则取 `backend/cmd/server/VERSION` 文件。用 `./backend/bin/server --version` 查看。

### 交叉编译

后端是纯 Go 程序（`CGO_ENABLED=0`），可以在任意机器上为其他平台编译。先构建一次前端，再指定目标平台：

```bash
make build-frontend
GOOS=linux GOARCH=arm64 make -C backend build TAGS=embed
```

## 首次运行

把程序复制到目标机器后直接启动，首次运行会进入设置向导：

```bash
./xy2api
# 浏览器打开 http://<主机>:8080，按向导配置数据库、Redis 和管理员
```

向导会把 `config.yaml` 和 `.installed` 写到数据目录：设置了 `DATA_DIR` 时写到该目录，否则写到当前工作目录。完成后自动重启进入正常模式。

**不要在首次运行前手动复制 `config.example.yaml` 为 `config.yaml`。** 程序发现配置文件已存在就会跳过向导，管理员账号不会被创建，登录时会提示邮箱或密码错误。`config.yaml` 里的 `default.admin_email` / `default.admin_password` 不会用来创建管理员。如果已经先放了配置文件，可以临时把它移走，完成向导后再把需要的配置项合并回去：

```bash
mv config.yaml config.yaml.bak
./xy2api            # 完成向导，生成新的 config.yaml
# 停止程序，把 config.yaml.bak 中需要的配置合并进新的 config.yaml，再启动
```

无人值守的场景可以跳过网页向导，用环境变量完成初始化（与 Docker 部署相同的流程）：

```bash
AUTO_SETUP=true \
DATABASE_HOST=127.0.0.1 DATABASE_USER=xy2api DATABASE_PASSWORD=<密码> DATABASE_DBNAME=xy2api \
REDIS_HOST=127.0.0.1 \
ADMIN_EMAIL=<管理员邮箱> ADMIN_PASSWORD=<8–72 字节> \
TOTP_ENCRYPTION_KEY=$(openssl rand -hex 32) \
./xy2api
```

`TOTP_ENCRYPTION_KEY` 要保存下来并在以后每次启动时提供，否则重启后已加密的数据无法解密。可用的变量见 [配置文件与环境变量](configuration.md)。

## 作为 systemd 服务运行

`install.sh` 只安装官方发布包。自己构建的程序可以参照 [`deploy/xy2api.service`](../../deploy/xy2api.service) 手动安装：

```bash
sudo useradd -r -s /bin/sh -d /opt/xy2api xy2api
sudo install -d -o xy2api -g xy2api /opt/xy2api /opt/xy2api/data
sudo install -m 0755 -o xy2api -g xy2api backend/bin/server /opt/xy2api/xy2api
sudo cp deploy/xy2api.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now xy2api
```

之后的设置向导、目录结构和服务管理与 [Linux 安装脚本与 systemd](binary.md) 相同。源码构建的程序在管理后台显示为“源码构建”，不提供在线更新和在线回退；升级时拉取新代码、重新构建并替换程序后重启。

## 构建容器镜像

仓库根目录的 `Dockerfile` 是多阶段构建：Node 构建前端，Go 编译内嵌前端的程序，最终镜像与官方镜像结构相同。

```bash
docker build -t xy2api:local .
# 为 ARM 服务器构建
docker buildx build --platform linux/arm64 -t xy2api:local-arm64 --load .
```

| 构建参数 | 默认值 | 说明 |
| --- | --- | --- |
| `VERSION` | 空（取标签或 `VERSION` 文件） | 写入程序的版本号 |
| `GOPROXY` | `https://goproxy.cn,direct` | Go 模块代理，不需要时可改为 `https://proxy.golang.org,direct` |
| `GOSUMDB` | `sum.golang.google.cn` | 可改为 `sum.golang.org` |
| `NPM_CONFIG_REGISTRY` | 空（使用 npm 默认源） | npm 镜像源 |
| `NODE_IMAGE` / `GOLANG_IMAGE` / `ALPINE_IMAGE` / `POSTGRES_IMAGE` | `node:24-alpine` / `golang:1.27.3-alpine` / `alpine:3.21` / `postgres:18-alpine` | 各阶段基础镜像；`POSTGRES_IMAGE` 决定镜像内 `pg_dump` 的版本，应与数据库主版本一致 |

例如 `docker build --build-arg GOPROXY=https://proxy.golang.org,direct --build-arg GOSUMDB=sum.golang.org -t xy2api:local .`。

构建好的镜像可以替换 Compose 文件里的 `image:` 使用，其余步骤同 [Docker Compose 部署](docker.md)。

### 用 `docker-compose.dev.yml` 构建并运行

`deploy/docker-compose.dev.yml` 直接从当前源码构建镜像，并启动 PostgreSQL 和 Redis，适合测试改动：

```bash
cd deploy
cp .env.example .env    # 至少设置 POSTGRES_PASSWORD
docker compose -f docker-compose.dev.yml up --build
```

这个文件面向开发测试：应用以 `debug` 模式运行，端口默认只绑定 `127.0.0.1`；构建时默认使用 npmmirror 镜像源，运行时默认通过宿主机 `host.docker.internal:7897` 上的代理访问外网（可用 `XY2API_DEV_HTTP_PROXY`、`XY2API_DEV_HTTPS_PROXY`、`XY2API_DEV_ALL_PROXY`、`XY2API_DEV_NO_PROXY` 覆盖）。它还为批量生图预置了一组 Vertex 相关变量。没有对应环境时请先编辑该文件，不要直接用于生产。

## 排障

| 现象 | 处理 |
| --- | --- |
| 打开页面是 404 或空白 | 程序没有内嵌前端：用 `make build`（或 `TAGS=embed`）重新编译，并确认 `backend/internal/web/dist/` 已生成 |
| 前端构建内存不足 | 设置 `NODE_OPTIONS=--max-old-space-size=4096` 后重试 |
| 首次登录提示邮箱或密码错误 | 首次运行前就有 `config.yaml`，向导被跳过，见上文 |
| Go 模块下载慢或失败 | 设置 `GOPROXY`；构建镜像时用 `--build-arg GOPROXY=...` |
