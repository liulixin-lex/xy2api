# 环境与客户端适配

只在工具不可用、命令行为不同或远端操作失败时读取。本流程的逻辑门禁固定，具体客户端可以替换。

## GitHub 客户端

按可用性选择一种主路径：

1. 已连接的 GitHub connector/MCP；
2. 已认证的 `gh` CLI；
3. GitHub REST/GraphQL API；
4. 最后才使用浏览器 UI。

Git 历史、merge base、tag object 和祖先关系始终以本地 `git` 为准。PR、Release 和 branch protection 以 GitHub 重新读取的远端状态为准。

某个 connector 在单一 GraphQL mutation 上失败时，可以切换到 `gh` 完成该动作，但动作后必须重新读取 PR 状态。不要因为客户端失败而重复创建 PR、标签或 Release。

## PowerShell 与 POSIX

- PowerShell 对外部程序失败不会自动终止脚本。每个关键 `git`、`gh`、`docker` 命令后检查 `$LASTEXITCODE`，或用只封装外部命令的 checked helper。
- PowerShell 中包含 `^{commit}` 的 revision 应使用单引号，避免解析问题。
- POSIX shell 使用 `set -euo pipefail`；需要捕获预期失败时局部关闭并立即恢复。
- 文件移动或删除在同一个 shell 内完成。Windows 递归清理前解析绝对路径并确认位于预期临时目录；若宿主策略拒绝递归删除，逐文件后逐目录删除。
- 不把 PowerShell 枚举结果拼接后交给 `cmd /c` 删除或移动。

## GitHub 网络故障

1. 首次失败后重试，最多 3-4 次并退避。
2. 可临时尝试 `git -c http.version=HTTP/1.1 ...`。
3. 分别检查 DNS、TCP 443、GitHub API 和 Git HTTPS，判断是解析、边缘地址还是认证问题。
4. 只有在通过可信渠道确认可达 GitHub 地址后，才可对单条命令使用临时 `http.curloptResolve`；不得修改 hosts、全局 Git 配置或仓库 remote。
5. 网络未恢复时停止标签/推送阶段，不要在旧的本地 `origin/main` 上继续打标签。

## Go 与生成器

- 使用 `backend/go.mod` 的精确 Go 版本。宿主缺少 Go 时使用临时官方 Go 容器。
- 容器启动后先运行 `go version`。若 `go` 不在 `PATH`，检查 `/usr/local/go/bin/go` 并显式补充 `PATH`，不要改项目文件。
- 生成器只在源定义变化或最终零差异门禁需要时运行。生成后任何差异都必须审查；正式标签创建后不能把新生成结果补进已发布提交。

## Docker 与部署验证

- 开始前记录 Docker Desktop/engine 是否运行，以及现有容器列表。
- 需要时启动，结束后恢复原状态。不要单独停止用户已有容器来腾名字或端口。
- smoke 资源统一使用版本化唯一前缀、独立网络、独立卷和高位本地端口，并在 `finally`/trap 中清理。
- 正式镜像必须核验 OCI version/revision label、manifest digest 和平台列表。
- 没有 Docker 时可依赖 CI 构建和远端 manifest 验证，但必须明确说明本地启动/回滚 smoke 未执行。

## CI 与发布观察

- 工作流同时监听 push 和 PR 时可能出现两组同名检查。等待所有 required context 成功即可，不因重复而重新触发。
- 区分 failure、skipped 和 annotation。可选凭据缺失导致 DockerHub/通知步骤 skipped，不等于 GHCR 或 GitHub Release 失败。
- Node/Action 运行时弃用 annotation 属于维护事项；只有步骤失败或制品不完整时才阻断当前发布。
- 长任务使用较长间隔等待，避免频繁轮询和重复输出。

## 不同模型与客户端的最小交接

如果任务需要换模型、客户端或会话，只传递以下状态，不复制全部日志：

- 当前阶段和用户授权范围；
- `TAG`、`TAG_OBJECT`、`U`、`B`、`F`；
- 当前分支、PR、head SHA 和 merge commit；
- 已通过和待完成的门禁；
- 正在运行的 workflow/run ID；
- 已启动的临时资源及原始环境状态；
- 明确的下一步和禁止事项。

接手者必须先重新读取远端状态，再继续任何 mutation。
