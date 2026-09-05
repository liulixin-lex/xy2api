# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

最后更新：`2026-09-05T18:26:58Z`（UTC）

| 项目 | 当前事实 |
| --- | --- |
| 仓库路径 | `/xy/xy2api` |
| 当前分支 | `release/v0.0.5` |
| 当前 HEAD | `ab9ddee56ad238ec121fbbc9e60e446ad9e2b862`；功能 PR `#13` 已合入 `main` |
| 工作树 | 正在准备 XY2API `v0.0.5` 版本晋级改动 |
| XY2API 产品版本 | 目标 `0.0.5`；当前正式 Release 仍为 `v0.0.4` |
| `main` 已审计 Sub2API 基线 | `v0.2.1` / commit `578785ee7fb35030b094b69624efe25670a36f5f` |
| 基线 provenance | `UPSTREAM_BASE.json` 状态为 `resolved`，同步 PR `#10` 已通过 merge commit 合入 |
| 本地远端 | `origin` 可读写；`upstream` 仅允许 fetch，push URL 为 `DISABLED` |
| 当前环境工具 | `git`、`python3 3.12.3`、`gh`、Docker、Node、Corepack 可用；本地无 Go、pnpm 命令，已用 Go 1.27 Docker 与 Corepack pnpm 10.28.0 完成验证 |

Sub2API `v0.2.1` 同步、冲突裁决、RC 验证和 XY2API `v0.0.4` 正式发布已完成。分组按模型启用长上下文阶梯计费已通过 PR `#13` 合入 `main`，正在晋级并发布 XY2API `v0.0.5`。

## 进行中的工作

- `20260905-release-group-model-long-context-pricing`：功能提交 `2e5218621` 与测试修正 `1c345b4fd` 已推送，PR `#13` 全部保护检查通过后以 merge commit `ab9ddee56` 合入 `main`；当前在 `release/v0.0.5` 准备产品版本晋级，兼容基线保持 Sub2API `v0.2.1`。

## 当前重要事项

### Sub2API v0.2.1 与 XY2API v0.0.4 已完成

以下状态于 `2026-09-05T12:57Z` 通过本地 Git、GitHub Actions、Release 和隔离 Docker 环境核实：

- 上游 annotated tag object 为 `adc26f68f687685e847bfb997559f48e79cac475`，目标 commit 为 `578785ee7fb35030b094b69624efe25670a36f5f`；标签未签名并已在 provenance 中明确记录为 `unsigned`。命名空间标签 `sub2api/v0.2.1` 保留。
- 预备 PR [#9](https://github.com/liulixin-lex/xy2api/pull/9) 先登记 5 个新人工冲突路径；同步 PR [#10](https://github.com/liulixin-lex/xy2api/pull/10) 的固定 head `8dd45d110dc1832986932178d7b91305feee5fed` 在 16/16 检查通过后，以 merge commit `294528940231ba6ed7764ccdabe4bbdabaa3783d` 合入。9 个冲突已逐项裁决，82 个上游 commit 与 297 个文件变更已纳入审计报告。
- 4 个新增 migration 均为 additive，既有 277 个 checksum 未改写且新增 checksum 已独立复算；迁移总数为 281。新增内容包括 usage log upstream request ID 及索引、channel reasoning multiplier、group Codex models manifest 配置。
- RC [v0.0.4-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.4-rc.1) 的 annotated tag object 为 `17bcf4009ee9241c8d35b0f5f6ecbcbb3519c20e`，目标为同步 merge commit；Release run `33964718089` 成功，5 个平台包 SHA256 全部通过，GHCR digest 为 `sha256:f8e357a3c08b88eda3e6c9c7ca0d8d9119809b8a57553ce7ce324b05112c953c`，包含 `linux/amd64` 与 `linux/arm64`。
- 正式版 PR [#11](https://github.com/liulixin-lex/xy2api/pull/11) 的固定 head `6dd99debce540f07a70f9b45ae9f115b5123333e` 在 16/16 检查通过后，以 merge commit `ae4c010d0a8aed750936b59655bf7ab8f85a777b` 合入；该提交的主线 CI run `33966180480` 和安全扫描 run `33966180489` 均成功。
- 正式 [v0.0.4 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.4) 非 draft、非 prerelease；annotated tag object 为 `7e38081004ce4dea4079a86eabe63624e76428ba`，目标为上述正式 merge commit，Release run `33966678769` 成功。5 个平台包 SHA256 全部通过；GHCR digest 为 `sha256:a4eddd09a4343fd9b2e276bcac7691db395d49697043262378c2db68cec5f9c1`，包含双架构，OCI version/revision 正确，`latest`、`0.0`、`0` 均指向同一 digest。
- 隔离 Docker 验证覆盖 RC 全新安装、正式 `v0.0.3` 原地升级到 RC、回切 `v0.0.3` 旧镜像和正式 `v0.0.4` 全新安装；健康、初始化、281 条 migration、新 schema、管理员、Redis 与 `/app/data` 持久化均符合预期。任务专用容器、卷、网络、下载文件和镜像已清理，原有 `book-keeper` 资源未改动。
- 同步 merge 后的主线 CI run `33964691713` 曾因 Docker Hub 拉取 Testcontainers Ryuk 时连接被对端重置而失败，不是代码或测试断言失败；后续 PR 两套集成测试及正式 merge 主线集成测试均通过。

### Sub2API v0.2.0 已合入并完成 RC 验证

以下状态于 `2026-09-04T18:12Z` 通过本地 Git、GitHub Actions、Release 和隔离 Docker 环境核实：

- 同步 PR [#5](https://github.com/liulixin-lex/xy2api/pull/5) 的固定 head `b73310ae84d10d675bf6ad7fac0c840559996aea` 已在 16/16 检查通过后，以 merge commit `48f0f0f10b79b32971649e227e4ceb7f8201e4dd` 合入 `main`。
- v0.2.0 固定 annotated tag object 为 `dd07c4d8d484878e617c945cc8bacc304a5a6560`，目标 commit 为 `aa236488351eb71e120fc2b6fb32e36b0374c918`；标签未签名，provenance 明确记录为 `unsigned`。
- 7 个冲突已逐项裁决；4 个 additive migration 的 checksum 独立复算一致，迁移记录从 273 增至 277，实际新增 7 个字段；既有 migration 未改写。
- RC [v0.0.3-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.3-rc.1) 为 annotated prerelease，Release run `33903033269` 成功；5 个平台包 SHA256 全部通过，GHCR manifest digest 为 `sha256:aedd8c30a43deda75ae4825678ba39a1cc9c16264b9c5548384eeb700944818f`，包含 `linux/amd64` 与 `linux/arm64`。
- 隔离验证中，RC 全新安装健康；从正式 `v0.0.2` 原地升级后用户、Redis 与 `/app/data` 数据保留；再回切 `v0.0.2` 镜像仍健康，数据库与数据可读。

### 自动同步失败已修复

- 2026-09-02 至 09-04 的失败由两个问题叠加：v0.2.0 新冲突路径不在旧 policy 的人工清单中，随后错误报告又尝试写入已关闭的 GitHub Issues，掩盖了首个阻断原因。
- 工作流现在将阻断写入 Job Summary、上传日志 artifact、显式返回原始失败；已有同步 PR 同时按 `upstream-sync` 标签与 `sync/sub2api-` 分支前缀识别。
- 另修复 GitHub Actions 对 skipped 步骤空输出进行宽松数值比较导致误执行 push 的问题，所有后续步骤同时要求 sync step 的 outcome 为 success。
- 分支场景回归 run `33901867104` 与 `main` 已同步场景 run `33902956072` 均成功；两次都只执行必要的选择/标签核验，仓库写操作按预期跳过。

### v0.0.3 正式发布已完成

- 正式版 PR [#7](https://github.com/liulixin-lex/xy2api/pull/7) 的固定 head `bad95a10a6267e2397274324f06a581b67839fba` 在 16/16 检查通过后，以 merge commit `a0e68c6e4f649fc58f95bed78aa1b883ab349cf8` 合入。
- annotated tag object 为 `bf25502f68bf0a49e6fa73048970ac257d7c0d16`，目标为上述 merge commit；正式 [v0.0.3 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.3) 非 draft、非 prerelease，run `33905898627` 成功。
- 5 个平台压缩包的 SHA256 已独立复算通过；GHCR manifest digest 为 `sha256:14d79a3cd5f6ef29e96e503f9f60b520f806c1f6f5b5050ff5607d7f43a89a1d`，包含 `linux/amd64` 与 `linux/arm64`，OCI version/revision 为 `0.0.3` / `a0e68c6e4f649fc58f95bed78aa1b883ab349cf8`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装：健康、setup completed、277 条 migration、1 个管理员、7 个 v0.2.0 新字段齐全。
- RC/正式验证的临时容器、卷、网络和本次新拉取镜像已删除；原有 `book-keeper` 容器、网络、卷和镜像未改动。已合并的记忆、同步、发布短期分支已在本地与远端删除。

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

### 2026-09-04T18:12:06Z — `20260904-full-upstream-sync-release-v0.0.3` — 进行中

- 请求/目标：分析同步失败并按最佳规范修复自动化、集成 Sub2API `v0.2.0`、完成 RC 验证并发布 XY2API `v0.0.3`。
- 开始状态：本地 `main` 比 `origin/main` 多一个未推送的记忆提交；Draft PR #5 head 为 `e8bfe57df33b19c099b61794df1a376f712d262c`，缺少同步标签；2026-09-02 至 09-04 的定时同步连续失败。
- 完成操作：通过 PR #6 保护并合入记忆提交；为 PR #5 补标签，修复已有 PR 识别与 skipped-step 守卫，复核固定上游标签、三方差异、7 个冲突和 4 个 migration 后，以 merge commit 合入；手工回归同步工作流；创建并验收 `v0.0.3-rc.1` annotated tag、Release 与双架构镜像；在隔离 Docker 环境完成正式 `v0.0.2` 到 RC 的升级、旧镜像回滚和 RC 全新安装。
- 修改文件：同步 PR 覆盖 `UPSTREAM_BASE.json`、版本文件、同步报告、工作流/policy、4 个新增 migration、上游功能与测试；正式版分支当前仅准备修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 与本记忆文件。
- 验证：同步工具 9/9、provenance/compatibility audit、Compose 解析、Go 1.27.0 Ent/Wire 零差异、PR #5 的 16/16 GitHub checks、两个同步回归 run、RC Release run、5 个平台包 SHA256、GHCR amd64/arm64、全新安装、原地升级和旧镜像回滚均通过。
- 卡点/风险：无当前阻塞；正式版本尚未发布。
- 下一步：完成正式版 PR、`v0.0.3` 标签与产物/镜像/启动验收，清理隔离资源和已合并短期分支，再将本条记录更新为完成。

### 2026-09-04T18:43:33Z — `20260904-full-upstream-sync-release-v0.0.3` — 完成

- 请求/目标：分析 GitHub 上游同步失败，按最佳规范修复同步链路，集成 Sub2API `v0.2.0` 并发布 XY2API `v0.0.3`。
- 开始状态：本地有未推送的记忆提交；Draft PR #5 缺少同步标签；旧 policy 未覆盖 v0.2.0 新冲突，阻断报告又写入已关闭的 Issues，导致 2026-09-02 至 09-04 定时同步连续失败。
- 完成操作：通过 PR #6 纳入记忆机制；修复冲突清单、Job Summary/artifact 报告、已有 PR 识别与 skipped-step 守卫；完成固定标签 provenance、三方差异和 7 个冲突人工裁决，通过 PR #5 合入上游；完成两个同步回归；发布并验收 `v0.0.3-rc.1`；通过独立 PR #7 晋级并发布 `v0.0.3`；删除已合并短期分支和全部任务专用 Docker 资源。
- 修改文件：同步与上游功能修改详见 PR #5；正式晋级修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本收尾提交只更新本记忆文件。
- 验证：PR #5 与 #7 各 16/16 checks；同步/升级预检 9/9；provenance/compatibility audit、Compose、Ent/Wire 零差异；同步回归 runs `33901867104`、`33902956072`；RC/formal Release runs `33903033269`、`33905898627`；两版各 5 个资产 SHA256；两版 GHCR amd64/arm64 与 OCI 标签；RC 全新安装、v0.0.2 原地升级和旧镜像回滚；正式版全新安装均通过。
- 卡点/风险：无已知阻塞。4 个新 migration 为 additive，旧镜像已验证可在升级后的 schema 上运行；数据库回滚仍应继续采用升级前备份策略，不宣称 SQL 自动降级。
- 下一步：无。后续常规维护应保留 `sub2api/v0.2.0`、`v0.0.3-rc.1`、`v0.0.3` 标签，并从本文件、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-05T12:57:29Z — `20260905-full-upstream-sync-v0.2.1` — 完成

- 请求/目标：按仓库标准全流程同步 Sub2API 最新正式版 `v0.2.1`，完成审计、PR 合入、RC/正式发布验证与收尾。
- 开始状态：本地 `main` 为 `90c46b18dcc0e2146e2e210d6e6b633cfaef07ef`，与 `origin/main` 一致且工作树干净；XY2API `0.0.3` / Sub2API compat `0.2.0`；`doctor.py --strict` 通过；上游最新稳定 Release 为 `v0.2.1`。
- 完成操作：冻结 annotated tag object、目标 commit、fork 起点和 merge base；通过预备 PR #9 扩充具名人工冲突策略；生成三方影响报告并以真实 merge 集成 82 个上游 commit，逐项裁决 9 个冲突，归一化模块路径、复算 4 个新增 migration checksum、确认生成产物零差异；同步 PR #10、正式版 PR #11 均在固定 head 16/16 检查通过后以 merge commit 合入；创建并验收 `v0.0.4-rc.1` 与 `v0.0.4` annotated Release；删除已合并短期分支和全部任务专用环境资源。
- 修改文件：同步改动与完整矩阵见 `docs/upstream-sync/v0.2.1.json` 和 PR #10；正式晋级仅修改产品版本、provenance 产品版本和本记忆文件；本次收尾仅修改本记忆文件。
- 验证：同步/升级预检 10/10；strict doctor、provenance/compatibility audit、diff/冲突标记、Compose、祖先关系与 migration checksum 通过；PR #10、#11 各 16/16 checks；正式 merge 主线 CI/安全扫描通过；RC/正式 Release runs `33964718089`、`33966678769` 成功；两版各 5 个资产 SHA256、GHCR amd64/arm64 和 OCI 标签通过；RC 全新安装、`v0.0.3` 原地升级、旧镜像回切及正式版全新安装通过。
- 卡点/风险：无已知阻塞。Go 1.27.0 容器中完整重复生成受 4 GiB 内存限制曾被 SIGKILL，但单并发唯一 Ent/Wire 生成成功且产物零差异，CI 编译、测试与 lint 均通过。4 个新 migration 为 additive，旧镜像回切已验证；生产数据库回滚仍应使用升级前备份，不宣称 SQL 自动降级。同步 merge 的一次主线集成测试因 Docker Hub 拉取 Ryuk 时连接重置失败，后续三套等价集成测试均成功。
- 下一步：无。后续维护应保留 `sub2api/v0.2.1`、`v0.0.4-rc.1`、`v0.0.4` 标签，并从本文件、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-05T17:02:30Z — `20260905-group-model-long-context-pricing` — 完成

- 请求/目标：让分组只对指定模型启用长上下文阶梯计费，并让实际扣费、认证缓存与模型广场实付价格保持一致；支持精确名和末尾 `*` 前缀匹配，旧分组继续按全部模型处理。
- 开始状态：从干净 `main` 的 `078820857b1c3036769777a7956d17dba431d464` 新建 `feat/group-model-long-context-pricing`；本地无 Go、pnpm 命令，Docker、Node 与 Corepack 可用。
- 完成操作：新增分组范围和模型名单字段、迁移与 Ent 生成代码；在统一定价解析层实现模型匹配和账号开关约束，并覆盖统一、OpenAI 旧计费与模型广场链路；更新认证查询、快照版本和数据库失效触发器；创建、编辑、读取、复制分组完整传递配置；新增管理表单的范围选择、搜索/手动模型标签输入及模型广场部分启用说明。
- 修改文件：后端涉及 `backend/ent/schema/group.go`、生成的 `backend/ent/`、`backend/migrations/235_group_long_context_pricing_models.sql`、分组管理/仓储/认证缓存/计费/模型广场服务及测试；前端涉及分组类型与管理页、新增 `LongContextPricingFields.vue`、账号提示逻辑、模型广场说明和中英文文案；同步更新本记忆文件。
- 验证：Go 1.27 Docker 下完整 `internal/service` 单测通过，新增定向 service 测试通过，`internal/handler`、`internal/repository`、`internal/server`、`migrations` 单测通过，真实 PostgreSQL 集成测试 `TestAuthCacheInvalidationTrigger_ProfitControlColumns` 通过；前端定向 14/14 测试、类型检查、全量 lint 和生产构建通过；Ent 再生成前后差异 SHA256 均为 `f16ec3a90a0b20689a0ff7d12430396d98e43dc5d2263a6efb190767fbe33ddd`；新增迁移 checksum 复算一致；Playwright Chromium 在 1440×1000/1100×760 桌面和 390×844 移动视口检查模型广场与真实表单组件，无重叠或布局回归。
- 卡点/风险：无功能阻塞。默认 Go 编译在 4 GiB 环境中曾因超大 Ent 包被 SIGKILL，改用单并发、关闭测试阶段 vet、禁用内联并降低 GC 阈值后全部通过；Impeccable detector 仅报告 `GroupsView.vue` 四处既有红底灰字警告，本次新增组件无命中。
- 下一步：审查并提交当前特性分支；本任务未推送、未发布、未部署。
