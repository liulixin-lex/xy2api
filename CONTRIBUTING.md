# 贡献指南

感谢你愿意改进 XY2API！本文说明如何提交问题、准备开发环境、编写代码和提交 Pull Request。

> **English:** Contributions in English are welcome. Use [Conventional Commits](https://www.conventionalcommits.org/) for PR titles, keep each PR focused on one change, make CI pass and add a line to the `[Unreleased]` section of `CHANGELOG.md` for user-visible changes.

参与本项目即表示你同意遵守[行为准则](CODE_OF_CONDUCT.md)。

## 提问与报告问题

- 使用问题：到 [Discussions](https://github.com/liulixin-lex/xy2api/discussions/categories/q-a) 提问，见 [SUPPORT.md](SUPPORT.md)。
- 缺陷与功能建议：使用 [Issue 模板](https://github.com/liulixin-lex/xy2api/issues/new/choose)。
- 安全漏洞：按[安全策略](.github/SECURITY.md)私密报告。

较大的改动（新功能、调度或计费规则变化、数据库结构变化）请先开 Issue 讨论方案，避免白做。

## 开发环境

环境准备、本地运行、测试命令见 [本地开发](docs/development/local-setup.md)，代码结构见 [架构说明](docs/development/architecture.md)。常用命令：

```bash
make build          # 构建前端并生成内嵌前端的 backend/bin/server
make test           # 后端单元测试 + 前端 lint、类型检查与全部单元测试
make lint           # golangci-lint 与前端 ESLint
make generate       # 修改 backend/ent/schema 后重新生成 Ent 与 Wire 代码
```

## 分支与提交

- 从最新的 `main` 拉分支，分支名用 `<type>/<简短描述>`，例如 `fix/anthropic-cache-usage`。
- PR 标题使用 [Conventional Commits](https://www.conventionalcommits.org/)，英文，不超过 72 个字符：

  ```
  <type>(<scope>): <subject>
  ```

  | type | 用途 |
  |---|---|
  | `feat` | 新功能 |
  | `fix` | 缺陷修复 |
  | `perf` | 性能改进 |
  | `refactor` | 不改变行为的重构 |
  | `docs` | 仅文档 |
  | `test` | 仅测试 |
  | `build` | 构建、打包、发布产物 |
  | `ci` | CI 配置 |
  | `chore` | 依赖升级、杂项维护 |

  `scope` 可选，取受影响的模块，例如 `gateway`、`scheduling`、`billing`、`admin-ui`、`deploy`。不兼容的变化在类型后加 `!`（如 `feat(api)!: …`），并在说明中写清迁移方式。
- PR 以 squash 方式合并，**PR 标题就是 `main` 上的提交标题**，PR 内部的提交信息不必拘泥格式。

## Pull Request 要求

- 一个 PR 只做一件事；与改动无关的格式化、重命名请单独提交。
- CI 必须全部通过：`lint`、`backend`、`windows`、`frontend`、`scripts`、`govulncheck`。
- 新功能和缺陷修复需要测试。后端测试使用构建标签区分：`-tags=unit`（单元）、`-tags=integration`（集成，需要 Docker）。
- 用户可见的变化（新功能、行为或默认值变化、配置项、修复）写进 `CHANGELOG.md` 的 `[Unreleased]` 一节，用中文、一句话，以用户能感知的结果开头。
- 行为或配置变化同步更新 `docs/` 下对应文档和 `deploy/config.example.yaml`、`deploy/.env.example`。
- 前端文案同时更新 `frontend/src/i18n/locales/` 下的中文和英文，键必须对齐（有测试检查）。
- 不要提交密钥、Token、真实账号数据、服务器地址或生产日志。

## 代码约定

- Go：`gofmt`，通过 `golangci-lint`（版本见 CI）。修改接口后同步补全测试替身。
- 前端：Vue 3 + TypeScript，使用 **pnpm**（不要用 npm 生成锁文件），修改依赖后提交 `frontend/pnpm-lock.yaml`。
- 数据库迁移：在 `backend/migrations/` 新增编号递增的 `.sql` 文件，**不得修改已发布的迁移**，并在 `backend/migrations/checksums.json` 追加对应校验和。规则见 [迁移说明](backend/migrations/README.md)。
- 修改 `backend/ent/schema` 后运行 `make generate`，并提交生成的代码。

## 发布流程（维护者）

1. 确认 `CHANGELOG.md` 的 `[Unreleased]` 内容完整。
2. 开一个 `chore(release): vX.Y.Z` PR：把 `[Unreleased]` 改为 `[X.Y.Z] - YYYY-MM-DD`，更新 `backend/cmd/server/VERSION`。
3. 合并后在该提交上打带注释的标签 `vX.Y.Z` 并推送。发布工作流会校验版本号，用 CHANGELOG 中对应小节作为 Release 说明，构建三个平台的二进制和 linux/amd64 镜像。
4. 需要先验证发布流程时，手动运行 Release 工作流并勾选 `dry_run`。

版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。在 1.0 之前，次版本号（0.**X**.0）用于新功能、数据库迁移或不兼容的变化，修订号（0.6.**X**）只用于修复。候选版本（`-rc.N`）只用于次版本。

## 许可

提交贡献即表示你同意以 [LGPL-3.0-or-later](LICENSE) 授权你的贡献。
