# 本地开发环境

本文说明如何在本机运行 XY2API 的后端和前端开发服务器，如何构建、测试和检查代码，何时需要重新生成代码，以及编写数据库迁移的规则。最后一节对照说明 CI 的作业结构。适合准备提交改动的贡献者。代码结构见 [项目结构与架构](architecture.md)；只想从源码构建并部署的读者看 [从源码构建](../deployment/source.md)。

## 前置条件

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 与 `backend/go.mod` 的 `go` 指令一致（当前 1.27.3） | 后端。CI 用 `go-version-file: backend/go.mod` 取版本 |
| Node.js | 24 | 前端。CI 与 `Dockerfile` 都使用 Node 24 |
| pnpm | 9 | 前端包管理。不要用 npm 或 yarn |
| PostgreSQL | 15 及以上 | 运行后端。CI 使用 18 |
| Redis | 7 及以上 | 运行后端。CI 使用 8.4 |
| Docker | 任意可用版本 | 集成测试通过 testcontainers 启动 PostgreSQL 与 Redis |
| golangci-lint | v2.14.0 | 后端代码检查，与 CI 一致 |
| Python 3 | CI 使用 3.12 | `tools/ci/race_suites.py` 与 `tools/` 下的脚本测试 |
| make | 可选 | 顶层 `Makefile` 的快捷目标；没有 make 时直接运行对应命令 |

本机 Go 版本低于 `go.mod` 要求时，`go` 命令会按 `GOTOOLCHAIN` 设置下载对应工具链或直接报错。

```bash
npm install -g pnpm@9
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
```

## 获取代码与安装依赖

```bash
git clone https://github.com/liulixin-lex/xy2api.git
cd xy2api
pnpm --dir frontend install
```

后端依赖在第一次 `go build` 或 `go test` 时由 Go 模块自动下载。修改 `frontend/package.json` 后必须提交更新过的 `frontend/pnpm-lock.yaml`，CI 使用 `pnpm install --frozen-lockfile`，锁文件不同步会直接失败。

## 准备 PostgreSQL 与 Redis

使用本机已有的实例即可，准备一个空数据库和一个可以登录的用户。也可以用容器临时启动：

```bash
docker run -d --name xy2api-pg -p 5432:5432 \
  -e POSTGRES_USER=xy2api -e POSTGRES_PASSWORD=<密码> -e POSTGRES_DB=xy2api \
  postgres:18-alpine
docker run -d --name xy2api-redis -p 6379:6379 redis:8.4-alpine
```

表结构不需要手工创建，后端每次启动都会执行内嵌的迁移。

## 运行后端与前端开发服务器

后端在 `backend/` 目录下运行。程序在数据目录中查找 `config.yaml`：数据目录取 `DATA_DIR`；未设置时，如果 `/app/data` 存在且可写就用它，否则用当前目录。在本机通常就是 `backend/`。`backend/config.yaml`、`backend/.installed` 和 `backend/data/` 已在 `.gitignore` 中，不会被提交。

没有 `config.yaml` 也没有 `.installed` 时，后端以安装向导模式启动，只提供 `/setup/*` 接口。下面两种方式任选其一完成初始化。配置项与环境变量的完整说明见 [配置](../deployment/configuration.md)。

### 方式一：通过安装向导

```bash
# 终端 1
cd backend
go run ./cmd/server

# 终端 2
pnpm --dir frontend run dev
```

浏览器打开 `http://localhost:3000`。前端检测到尚未安装，会跳转到安装向导，在向导里填写数据库、Redis 和管理员账号。开发服务器把 `/setup` 前缀代理给后端，所以请从首页进入，不要直接刷新 `/setup` 页面。

向导写入 `config.yaml` 后会尝试重启服务，但这只在 Linux 的 systemd 环境下有效，`go run` 不会被自动拉起。看到安装完成后，手动停止并重新执行 `go run ./cmd/server`。

### 方式二：通过环境变量自动安装

设置 `AUTO_SETUP=true` 后，后端用环境变量完成安装、写出 `config.yaml`，然后直接进入正常模式：

```bash
cd backend
AUTO_SETUP=true \
DATABASE_HOST=127.0.0.1 DATABASE_USER=xy2api DATABASE_PASSWORD=<密码> DATABASE_DBNAME=xy2api \
REDIS_HOST=127.0.0.1 \
SERVER_MODE=debug \
go run ./cmd/server
```

`ADMIN_EMAIL` 和 `ADMIN_PASSWORD` 留空时会随机生成，并在首次启动时打印到标准输出。`deploy/config.example.yaml` 里的 `default.admin_email` 和 `default.admin_password` 不会用来创建管理员；直接复制配置示例启动会跳过安装向导，得到一个没有管理员的实例。

### 前端开发服务器

`pnpm --dir frontend run dev` 启动 Vite 开发服务器，监听 `0.0.0.0`，把 `/api`、`/v1` 和 `/setup` 代理到后端，并在 `index.html` 中注入后端的公开设置，与生产行为一致。

| 环境变量 | 默认值 | 作用 |
|---|---|---|
| `VITE_DEV_PROXY_TARGET` | `http://localhost:8080` | 代理目标，即后端地址 |
| `VITE_DEV_PORT` | `3000` | 开发服务器端口 |

开发服务器同时运行 `vue-tsc` 类型检查，类型错误会显示在终端和浏览器里。不带 `embed` 标签运行的后端不提供页面，直接访问 `:8080` 会看到 `Frontend not embedded`，这是正常现象。

## 构建

顶层 `Makefile`：

| 目标 | 作用 |
|---|---|
| `make build` | 先构建前端，再以 `TAGS=embed` 构建后端，得到内嵌前端的单个二进制 `backend/bin/server` |
| `make build-backend` | 只构建后端，不内嵌前端 |
| `make build-frontend` | `pnpm --dir frontend run build`，产物输出到 `backend/internal/web/dist/` |
| `make test` | 后端单元测试，以及前端 `lint:check`、`typecheck`、`vitest run` |
| `make lint` | `golangci-lint run ./...`（在 `backend/` 下）和前端 `lint:check` |
| `make generate` | 重新生成 Ent 与 Wire 代码，并执行 `go mod tidy` |

后端构建使用 `CGO_ENABLED=0`、`-trimpath`、`-ldflags="-s -w -X main.Version=…"`。版本号由 `backend/scripts/resolve-version.sh` 决定：当前提交恰好是 `vX.Y.Z` 标签时用标签版本，否则读 `backend/cmd/server/VERSION`。

前端 `pnpm run build` 依次执行三步：i18n 键完整性检查（`check:i18n`）、`vue-tsc -b` 类型检查、`vite build`。任何一步失败都会中止构建。

## 测试

### 后端单元测试

```bash
make -C backend test-unit          # 即 cd backend && go test -tags=unit ./...
```

单元测试文件带 `//go:build unit`。没有构建标签的测试文件在任何 `go test` 下都会编译运行，例如 `backend/migrations` 中校验迁移校验和的测试。`make -C backend test` 只跑无标签测试并执行 golangci-lint，不包含单元测试。

### 集成测试

```bash
make -C backend test-integration   # 即 go test -tags=integration ./...
```

集成测试带 `//go:build integration`，通过 testcontainers 启动 PostgreSQL（`postgres:18.1-alpine3.23`）和 Redis（`redis:8.4-alpine`），需要本机 Docker 正在运行。`internal/repository` 的测试套件在找不到 Docker 时打印提示并跳过；设置了 `CI` 环境变量时则直接失败。

需要验证旧版本 PostgreSQL 时，用 `SUB2API_TEST_POSTGRES_IMAGE` 替换镜像：

```bash
cd backend
SUB2API_TEST_POSTGRES_IMAGE=postgres:15-alpine go test -tags=integration ./internal/repository/
```

### 竞态与依赖服务套件

`tools/ci/race_suites.py` 运行一组需要竞态检测或真实服务的测试。每个套件执行 `go test -race -json -count=1`，测试失败、被跳过或一个测试都没匹配到都算失败，因此缺少服务时不会悄悄变成空跑。在仓库根目录执行：

```bash
python3 tools/ci/race_suites.py                         # 全部套件
python3 tools/ci/race_suites.py resilience gateway-reliability
```

| 套件 | 内容 | 构建标签 | 必需的环境变量 |
|---|---|---|---|
| `resilience` | 网关韧性测试文件中的全部测试（`internal/resilience`，以及 service、handler、handler/admin、server/middleware 中的 resilience 测试文件） | `unit` | 无 |
| `gateway-reliability` | 指定的网关可靠性、请求体重放安全与份额调度测试 | `unit` | 无 |
| `openai-cache` | OpenAI 缓存计量、用量换算与粘性容量测试 | `unit` | `SCHEDULING_TEST_REDIS_ADDR` |
| `stripe-hosted` | Stripe 托管模式的数据库测试 | `unit` | `STRIPE_HOSTED_TEST_DSN` |
| `mcp-postgres` | 只读 MCP 的 PostgreSQL 测试 | `integration` | `MCP_READER_TEST_DSN` |

缺少某个套件必需的环境变量时，该套件直接报失败。各变量的要求：

- `SCHEDULING_TEST_REDIS_ADDR`：Redis 地址，例如 `127.0.0.1:6379`。请使用可随意写入的实例。
- `STRIPE_HOSTED_TEST_DSN`：PostgreSQL 连接串。测试为每个用例创建并删除一个临时 schema，用户需要建 schema 的权限。
- `MCP_READER_TEST_DSN`：PostgreSQL 连接串。主机必须是 `127.0.0.1`、`localhost` 或 `[::1]`，数据库名必须是 `mcp_readonly_test`。测试会在其中执行迁移并创建只读角色，用户需要建角色的权限（CI 使用超级用户）。

CI 中这三个变量指向 `backend` 作业的 PostgreSQL 与 Redis 服务容器，写法见 `.github/workflows/ci.yml` 的 “Race detector and service-backed suites” 步骤。

### 端到端测试

`backend/internal/integration/` 下的测试带 `//go:build e2e`，对一个已经运行的实例发真实请求，CI 不运行。

```bash
make -C backend test-e2e-local     # go test -tags=e2e -v -timeout=300s ./internal/integration/...
```

| 环境变量 | 默认值 | 作用 |
|---|---|---|
| `BASE_URL` | `http://localhost:8080` | 被测实例地址 |
| `ENDPOINT_PREFIX` | 空 | 设为 `/antigravity` 时测试 Antigravity 专用端点 |
| `CLAUDE_API_KEY`、`GEMINI_API_KEY` | 无 | 被测实例上创建的网关 API Key；都未设置且未开启 Mock 时测试跳过 |
| `E2E_MOCK` | 无 | 设为 `true` 时不需要真实 Key |

### 前端测试

```bash
pnpm --dir frontend exec vitest run    # 或 pnpm --dir frontend run test:run
pnpm --dir frontend run test           # 监听模式
pnpm --dir frontend run test:coverage  # 覆盖率报告
```

测试使用 Vitest + jsdom，文件匹配 `src/**/*.{test,spec}.{js,ts,jsx,tsx}`，公共初始化在 `src/__tests__/setup.ts`。覆盖率设有最低阈值（语句 69%、分支 59%、函数 59%、行 71%，见 `frontend/vitest.config.ts`），低于阈值时 `test:coverage` 失败。

## 代码检查

- **后端**：在 `backend/` 下运行 `golangci-lint run ./...`。启用的检查有 `depguard`、`errcheck`、`gosec`、`govet`、`ineffassign`、`staticcheck`、`unused`，格式化器为 `gofmt`（并把 `interface{}` 改写为 `any`）。`depguard` 用于约束分层，见 [项目结构与架构](architecture.md#后端分层)。生产代码中的 `gosec` 告警只能逐处用 `//nolint:gosec` 豁免，并写明信任边界。
- **构建标签**：golangci-lint 只检查默认构建下的文件，带 `unit`、`integration`、`e2e` 标签的测试文件不在其中。改动测试后可以用 `go vet -tags=unit ./...`、`go vet -tags=integration ./...`、`go vet -tags=e2e ./internal/integration/...` 确认能编译。
- **前端**：`pnpm --dir frontend run lint:check` 只检查；`pnpm --dir frontend run lint` 会自动修复。`pnpm --dir frontend run typecheck` 单独做类型检查。

## 代码生成

| 改动 | 命令（在 `backend/` 下） | 生成结果 |
|---|---|---|
| `ent/schema/*.go` | `go generate ./ent` | `ent/` 下的客户端代码 |
| Wire provider（各包 `wire.go` 的 `ProviderSet`）或 `cmd/server/wire.go` | `go generate ./cmd/server` | `cmd/server/wire_gen.go` |
| 两者都改，或不确定 | `make generate`（仓库根目录） | 以上两项并执行 `go mod tidy` |

生成的代码要和改动一起提交。重新生成 Wire 不应改变 `go.mod` 和 `go.sum`。Ent 会把 `json.RawMessage` 字段生成为同类型的 `jsontext.Value`，这是预期行为。

Ent schema 只决定生成的查询代码，不会修改数据库。改了 schema 的同时要写对应的 SQL 迁移。

插件协议 `pkg/pluginapi/v1/plugin.proto` 修改后，需要在 `backend/` 下用 `protoc` 配合 `protoc-gen-go` 与 `protoc-gen-go-grpc` 重新生成 `plugin.pb.go` 和 `plugin_grpc.pb.go`。仓库中没有对应的 `go:generate` 指令，现有文件头部记录了生成器版本。

## 数据库迁移

迁移文件位于 `backend/migrations/`，与 `checksums.json` 一起嵌入二进制。执行器是 `backend/internal/repository/migrations_runner.go`，规则以代码为准，`backend/migrations/README.md` 有补充说明。

**执行方式**

- 每次启动按文件名字典序执行尚未执行的 `*.sql`，期间持有 PostgreSQL advisory lock。
- 已执行的文件记录在 `schema_migrations` 表（`filename`、`checksum`、`applied_at`）。

**命名**

- 格式为 `NNN_description.sql`，`NNN` 取当前最大编号加一，描述用小写下划线。

**不可变**

- 校验和是文件内容去掉首尾空白后的 SHA-256。已执行的文件内容一变，启动时报 `migration <文件名> checksum mismatch` 并拒绝启动。
- `checksums.json` 固定了每个已发布迁移的校验和，`TestPublishedMigrationChecksumsAreImmutable` 会检查。新增迁移时在其中追加一条，不要修改已有条目。计算方法：

  ```bash
  python3 -c "import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],encoding='utf-8').read().strip().encode()).hexdigest())" backend/migrations/NNN_description.sql
  ```

- 已发布的迁移不能修改、删除、改名或调整编号。要回退表结构变更，就新写一个反向迁移。

**只向前**

- 文件中不要写 Down 段。执行器不解析 goose 风格的 Up/Down 标记，会把文件里所有 SQL 都执行一遍。

**幂等**

- 优先使用 `IF NOT EXISTS`、`IF EXISTS`，保证重放时不出错。

**事务**

- 普通 `*.sql` 整个文件在一个事务中执行。
- 文件中不能出现 `CONCURRENTLY` 一词，注释里也不行，否则校验失败。

**并发索引**

- `CREATE INDEX CONCURRENTLY` 和 `DROP INDEX CONCURRENTLY` 必须放在 `*_notx.sql` 文件中。这类文件不包事务，按分号拆分后逐条执行。
- 文件中只能有并发索引语句，并且必须分别带 `IF NOT EXISTS` 或 `IF EXISTS`。
- 文件中不能出现 `BEGIN`、`COMMIT`、`ROLLBACK` 字样。

**测试**

- 运行 `make -C backend test-integration`，集成测试会在全新数据库上执行全部迁移。
- 涉及数据改写的迁移，可以在 `backend/migrations/` 下加针对该文件的测试，已有测试可参照。

部署侧的升级、迁移失败处理和回滚见 [升级与回滚](../deployment/upgrade.md)。

## CI

`.github/workflows/ci.yml` 在以下时机触发：推送到 `main`、每个 Pull Request、每周一 03:00（UTC），也可以手动触发。

| 作业 | 环境 | 内容 |
|---|---|---|
| `lint` | Ubuntu | golangci-lint v2.14.0，工作目录 `backend/`，`--timeout=30m` |
| `backend` | Ubuntu | 单元测试 → 编译 `linux/amd64`、`windows/amd64`、`darwin/arm64` |
| `integration` | Ubuntu，附 PostgreSQL 18 与 Redis 8.4 服务容器 | 集成测试 → 创建 MCP 测试库与角色 → `tools/ci/race_suites.py` |
| `windows` | Windows | 在 Windows 上测试未签名开发插件包的安装 |
| `frontend` | Ubuntu，Node 24 + pnpm 9 | `pnpm install --frozen-lockfile`、`lint:check`、`vitest run`、`build`（含 i18n 检查与类型检查） |
| `scripts` | Ubuntu，Python 3.12 + Node 24 | `deploy/` 安装脚本语法检查与 `deploy/tests/*-test.sh`；`tools/tests` 单元测试与 `tools/*.mjs` 语法检查 |
| `govulncheck` | Ubuntu | `govulncheck@v1.9.0 ./...` |

`.github/workflows/release.yml` 在推送 `v*` 标签时发布，也可以手动以 dry run 方式运行。它要求标签形如 `vX.Y.Z` 或 `vX.Y.Z-rc.N` 且与 `backend/cmd/server/VERSION` 一致，再用 `tools/ci/changelog_section.py` 从 `CHANGELOG.md` 取发布说明：正式版需要非空的 `## [X.Y.Z]` 段，预发布版使用 `## [Unreleased]` 段。随后构建前端，由 GoReleaser 发布二进制和 GHCR 镜像。

## 提交前检查

- `make lint` 与 `make test` 通过。
- 改动了 repository、SQL 或迁移：`make -C backend test-integration` 通过；本机没有 Docker 时在 PR 中说明。
- 改动了网关韧性、份额调度、OpenAI 缓存、支付或 MCP 相关代码：运行 `tools/ci/race_suites.py` 中对应的套件。
- 改动了 Ent schema 或 Wire provider：生成代码已提交，并已补写迁移。
- 改动了 `package.json`：`pnpm-lock.yaml` 已提交。
- 新增或修改界面文案：`zh` 与 `en` 两套键都已添加。
- 给接口新增了方法：测试中实现该接口的所有 stub 和 mock 都已补全。

## 常见问题

| 现象 | 处理 |
|---|---|
| CI 中 `pnpm install --frozen-lockfile` 失败 | 在本地 `pnpm install`，提交更新后的 `pnpm-lock.yaml` |
| `pnpm install` 报权限或链接错误，之前用 npm 装过依赖 | 删除 `frontend/node_modules` 后重新 `pnpm install` |
| 给接口加方法后报 `does not implement ... (missing method ...)` | 在测试文件里找到实现该接口的 stub/mock 逐一补全；用 `go vet -tags=unit ./...` 确认 |
| 修改 `ent/schema` 后代码没有变化 | 运行 `make generate` |
| 启动时报 `checksum mismatch` | 已执行的迁移被改过。从 git 恢复原文件，把改动写成新迁移 |
| 访问后端端口看到 `Frontend not embedded` | 后端未带 `embed` 标签。开发时通过前端开发服务器访问，或用 `make build` |
| 集成测试输出 `docker is not available; skipping` | 启动 Docker 后重试 |
| Windows 上没有 make | 直接运行 `Makefile` 中对应的命令，例如 `go test -tags=unit ./...` |
