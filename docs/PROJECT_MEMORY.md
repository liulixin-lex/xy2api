# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

最后更新：`2026-09-04T16:22:26Z`（UTC）

| 项目 | 当前事实 |
| --- | --- |
| 仓库路径 | `/xy/xy2api` |
| 当前分支 | `main` |
| 当前 HEAD | 记忆机制初始化提交；该提交不在自身内容中固化自引用 SHA，其父提交为 `79955dbaa964732429747ae65dabf9a8bfb44a65`，读取时运行 `git rev-parse HEAD` 核实 |
| 工作树 | 记忆机制初始化变更已纳入本地提交；记录时没有其他用户或 Agent 改动，交付时应为干净状态 |
| XY2API 产品版本 | `0.0.2` |
| `main` 已审计 Sub2API 基线 | `v0.1.185` / commit `2ac784c51a5d0925b324efef2ba6b3446c364781` |
| 基线 provenance | `UPSTREAM_BASE.json` 状态为 `resolved`，同步 PR 为 `#2` |
| 本地远端 | 只有 `origin`；缺少只读 `upstream` remote，因此 Doctor 的 `ready_for_prepare=false` |
| 当前环境工具 | `git`、`python3 3.12.3`、`gh`、Docker、Node 可用；`python` 命令别名、Go、pnpm 不可用 |

当前没有用户授权的业务功能修改、上游合并、发版或远端写操作。本次任务已建立仓库记忆机制并纳入本地提交，尚未推送。

## 进行中的工作

| 任务 ID | 状态 | Agent | 范围 | 已完成 | 下一步 / 卡点 |
| --- | --- | --- | --- | --- | --- |
| — | 无 | — | — | — | — |

## 当前重要事项

### Sub2API v0.2.0 同步尚未进入 main

以下状态于 `2026-09-04T16:04Z` 通过本地 Git 和 `gh` 只读核实：

- 远端分支：`origin/sync/sub2api-v0.2.0`，head 为 `e8bfe57df33b19c099b61794df1a376f712d262c`，从当前 `main` 看为 `0 behind / 69 ahead`。
- Draft PR：[#5](https://github.com/liulixin-lex/xy2api/pull/5)，状态 `OPEN`、`MERGEABLE/CLEAN`，当时已有检查均成功，但仍是 Draft、没有 review decision。
- PR 对应的 branch provenance 已标记 `resolved`，兼容版本为 `0.2.0`；这不改变 `main` 当前仍只声明兼容 `0.1.185` 的事实。
- v0.2.0 固定 annotated tag object 为 `dd07c4d8d484878e617c945cc8bacc304a5a6560`，目标 commit 为 `aa236488351eb71e120fc2b6fb32e36b0374c918`，标签未签名但已在 provenance 中记录为 `unsigned`。
- 同步报告记录：上游 60 个 commit、148 个上游变更文件、87 个双边重叠文件、7 个人工冲突、4 个新 migration；分支已追加 migration checksum、重新生成 Ent，并通过 PR 上的同步审计、后端、前端、lint、安全及 Windows 插件检查。

未经用户明确要求，不要代为切换/改写该分支、转 Ready、合并、打标签或发版。继续处理前重新读取 PR head、检查结果和 `UPSTREAM_BASE.json`，不能只依赖本快照。

### 自动同步工作流当前存在待处理问题

- PR #5 的 `labels=[]`，但 `main` 上 `.github/workflows/upstream-sync.yml` 只用 `upstream-sync` 标签判断是否已有同步 PR。因此 `gh pr list --state open --label upstream-sync` 返回空列表，工作流不能识别现有 Draft PR。
- `2026-09-02`、`2026-09-03`、`2026-09-04` 三次定时 Upstream Sync 均失败；最近运行 ID 为 `33851921097`。
- 可确认的失败表象是：同步命令的非零退出码被“Prepare three-way synchronization”步骤捕获，随后“Create or update blocking issue”步骤失败；仓库实际关闭了 GitHub Issues。
- 高可信推断（未取得原始 `upstream-sync.log`）：`main` 的 policy 尚未包含 v0.2.0 出现的新增人工冲突路径，因此自动 `prepare` 会阻断。PR #5 内已有 `fix: harden upstream sync automation`，补充了这些路径、禁用 Issues 时的报告降级、失败日志 artifact 和显式失败步骤，但这些修复尚未进入 `main`。
- PR #5 当前无标签与“只能有一个同步 PR”的设计不一致。是否补标签、是否先合并工作流修复，需要维护者决定；本次分析没有执行任何远端修改。

### 文档冲突与操作陷阱

- `DEV_GUIDE.md` 的“Git 操作”仍写着直接 `git merge upstream/main` 和 `git rebase upstream/main`。这与正式同步规范冲突，处理 Sub2API 同步时不得照此执行；以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。

## 项目速览

### 产品与架构

- XY2API 是 Sub2API 的二开 AI API 网关，负责多上游账号调度、API Key、配额/计费、协议转发、管理后台等。
- 后端位于 `backend/`：Go `1.27.0`、Gin、Ent、Wire，依赖 PostgreSQL 与 Redis；入口为 `backend/cmd/server/main.go`，前端构建产物嵌入 `backend/internal/web/dist/`。
- 前端位于 `frontend/`：Vue 3 + TypeScript + Vite + Pinia + Vue Router，包管理器固定为 pnpm。
- `deploy/` 保存 Compose、镜像与部署脚本；`openspec/` 保存重要功能规格和验证证据；`skills/` 保存面向 Agent 的项目技能。
- 修改 Ent schema 后需重新生成 `backend/ent/`；修改 Wire provider 后需重新生成 `backend/cmd/server/wire_gen.go`。生成产物必须和源定义一起审查、提交。

### 常用验证入口

```bash
# 仓库同步工具与升级预检单测
python3 -m unittest discover -s tools/tests -p 'test_*.py' -v

# 只应在干净工作树执行
python3 tools/upstream-sync/sync.py audit

# 上游同步环境体检；准备新同步前要求 strict 通过
python3 skills/xy2api-upstream-sync/scripts/doctor.py --strict

# 后端（需要 go 1.27.0）
cd backend && go test -tags=unit ./...
cd backend && go test -tags=integration ./...

# 前端（需要 pnpm）
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

根 `Makefile` 提供 `build`、`test`、`test-backend`、`test-frontend`；后端 `Makefile` 提供 `build`、`generate`、`test-unit`、`test-integration` 和 `test-e2e`。验证前应按变更范围选择，不要机械运行无关长测试。

## 上游同步机制摘要

### 事实来源

| 文件 | 职责 |
| --- | --- |
| `skills/xy2api-upstream-sync/SKILL.md` | 阶段选择、授权边界、停止条件和交付证据 |
| `skills/xy2api-upstream-sync/references/runbook.md` | prepare、人工裁决、PR、RC、正式版和收尾执行手册 |
| `docs/UPSTREAM_SYNC.md` | 项目正式同步规范与兼容不变量 |
| `tools/upstream-sync/sync.py` | `report`、`prepare`、`normalize`、`audit` 实现 |
| `tools/upstream-sync/policy.json` | 所有权分类、兼容字面量、禁止回流文件、二开补丁与绑定测试 |
| `UPSTREAM_BASE.json` | 当前 main 已审计上游基线与不可变 provenance |
| `docs/upstream-sync/<tag>.json` | 每次同步的三方文件矩阵和影响报告 |
| `.github/workflows/upstream-sync.yml` | 选择稳定 release、固定标签、准备分支与 Draft PR |
| `.github/workflows/release.yml` | 校验产品版本标签并构建/发布制品，不负责改版本文件 |

### 标准流程

1. 只选择正式、非 draft、非 prerelease 的 semver annotated tag；保存官方 tag object，并固定到 `refs/tags/sub2api/<tag>`。
2. 冻结目标 commit、当前 fork 起点和真实 merge base；禁止跟随移动的 `upstream/main`。
3. `report` 计算上游与二开两侧 commit、变更文件、重叠矩阵、所有权和 migration/API/config/dependency/generated 影响。
4. `prepare` 要求干净工作树，执行 `git merge --no-ff --no-commit`。清单外冲突立即阻断；清单内冲突先保留 XY2API 侧并登记 `pending`，不能把它当作已解决。
5. 官方 merge 单独提交；仅对本次上游变更涉及的 `.go`/`.proto` 做 module path 字节替换并单独提交。禁止全仓 `sub2api -> xy2api` 替换。
6. 人工逐项比较 base/upstream/XY2API，按 `UPSTREAM`、`XY_OWNED`、`COMPAT_INVARIANT`、`MANUAL_MERGE`、`GENERATED` 五类裁决；更新 provenance、兼容版本、migration manifest、二开补丁和生成代码。
7. `audit` 检查干净工作树、冲突标记、已发布 migration checksum、policy 结构、具名补丁测试、必需/禁止契约、module path、标签/祖先关系、双版本一致性、pending 状态和未分类差异。
8. 自动化最多创建一个 Draft PR，从不自动 merge 或 release。同步合入后先做 RC，隔离验证制品/升级/回滚，再用独立正式版 PR 晋级产品版本。

### 不可破坏的兼容边界

- 已发布 migration 及 `backend/migrations/checksums.json`：既有 SQL/hash 不得改写，只能追加新编号与 checksum。
- 插件 v1 包名、Magic Cookie、`requires.sub2api` 等第三方兼容字段继续使用 Sub2API 语义。
- 已发布 HTTP 路由、Redis/浏览器 key、稳定 UUID/KDF 输入、WebSocket 子协议、旧配置目录、旧二进制名和 Grok 兼容 header 不因品牌改造重命名。
- `VERSION` 是 XY2API 产品版本；`SUB2API_COMPAT_VERSION` 是已完成审计的 Sub2API 基线，二者独立演进。
- 品牌、仓库、镜像、安装/发布策略和 sponsor-free 政策归 XY2API；同步功能时不能让 CLA、广告或赞助资源回流。
- 冲突裁决、二开兼容补丁、生成产物、provenance、RC/正式版本应保持可追踪的分层提交。

## 记忆维护规范

### 更新时机

- 开始任务：核实 Git 状态并在“进行中的工作”登记。
- 执行中：只在范围、关键事实、决策、文件集合、验证结果或卡点变化时更新。
- 结束前：更新顶部当前状态；将自己的进行中条目删除或标为阻塞；在日志末尾追加记录。
- 外部状态可能变化时（PR、CI、Release、上游 tag、价格/依赖等），记录“核实时间”，后续 Agent 必须重新查询。

### 日志模板

```markdown
### <UTC 时间> — <任务 ID> — <状态>

- 请求/目标：
- 开始状态：分支、HEAD、工作树，以及相关远端标识。
- 完成操作：关键检查、决策和修改，不逐条粘贴命令流水账。
- 修改文件：
- 验证：命令与结果；未运行项及原因。
- 卡点/风险：无则写“无”。
- 下一步：无则写“无”。
```

## 操作日志

<!-- 按时间从旧到新追加。历史记录只允许追加更正，不应静默改写。 -->

### 2026-09-04T16:08:05Z — `20260904-memory-bootstrap` — 完成

- 请求/目标：仔细分析 XY2API 项目及其 Sub2API 上游同步机制，建立可供后续 Agent 实时维护的仓库记忆和开发日志。
- 开始状态：位于 `main`，HEAD `79955dbaa964732429747ae65dabf9a8bfb44a65`，与 `origin/main` 一致，工作树干净；主线版本为 XY2API `0.0.2` / Sub2API compat `0.1.185`。
- 完成操作：阅读项目结构、入口、Makefile、同步规范、Skill/Runbook、policy、同步脚本、Doctor、升级预检、工作流、版本化报告及相关 Git 历史；只读核实远端分支、PR #5、检查结果和近期定时工作流；建立根级 Agent 入口、当前状态区、进行中工作区、长期注意事项和追加式操作日志；将 Agent 入口登记为 `xy_owned`。
- 修改文件：`.gitignore`（允许跟踪两份协作文档）、`AGENTS.md`（强制读取/更新协议）、`docs/PROJECT_MEMORY.md`（本文件）、`tools/upstream-sync/policy.json`（分类 `AGENTS.md`）。未修改业务代码，未执行远端写操作。
- 验证：修改前 `python3 tools/upstream-sync/sync.py audit` 通过；Doctor 的 audit 通过但因缺少 `upstream` remote 而 `ready_for_prepare=false`；修改前后 `python3 -m unittest discover -s tools/tests -p 'test_*.py' -v` 均为 8/8 通过；policy JSON 可解析，`AGENTS.md` 与 `docs/PROJECT_MEMORY.md` 均解析为 `xy_owned`；`git diff --check` 通过；两份新文档已从 `.gitignore` 中显式放行。Go 与 pnpm 当前不可用，且本次仅为文档/策略改动，未运行后端和前端套件。
- 卡点/风险：记忆机制本身无阻塞。另有未获授权处理的既存事项：v0.2.0 Draft PR #5 无 `upstream-sync` 标签，自动同步连续失败，且相关工作流修复仍只在该 PR 分支；详见“当前重要事项”。
- 下一步：维护者可审查并提交本次 4 项改动。后续 Agent 应在每次仓库任务中按 `AGENTS.md` 更新本文件；如要处理 PR #5、修复自动同步或继续 v0.2.0 集成，需先取得对应授权并重新核实远端状态。

### 2026-09-04T16:22:26Z — `20260904-commit-memory-bootstrap` — 完成

- 请求/目标：将记忆机制初始化改动提交到当前本地 `main`。
- 开始状态：HEAD `79955dbaa964732429747ae65dabf9a8bfb44a65`，与 `origin/main` 一致；仅有上一任务产生的 `.gitignore`、`AGENTS.md`、`docs/PROJECT_MEMORY.md`、`tools/upstream-sync/policy.json` 4 项改动。
- 完成操作：重新读取仓库记忆、复核提交范围，更新本次操作日志，并以 `docs: establish repository agent memory` 创建单一聚焦提交。未推送远端。
- 修改文件：仍为上述 4 项记忆机制文件，没有混入业务代码或其他既有改动。
- 验证：`git diff --check` 和 Markdown 尾随空白检查通过；同步工具与升级预检单测 8/8 通过；两份协作文档的 `xy_owned` 分类通过；提交后检查本地工作树状态。
- 卡点/风险：无。
- 下一步：无需继续操作；若要推送，应由用户另行明确要求。后续任务开始时用 Git 核实本次提交 SHA 和工作树状态。
