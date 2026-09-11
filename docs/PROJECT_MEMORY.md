# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

最后更新：`2026-09-11T13:09:37Z`（UTC）

| 项目 | 当前事实 |
| --- | --- |
| 仓库路径 | `/xy/xy2api` |
| 当前分支 | 稳定交接分支为 `main`；无进行中同步或发布分支 |
| 发布提交 | `v0.0.7` 标签指向 `a3c0209184b65a6a9ae1383b725022f92cb9fb6e`；`main` 在其后仅有发布记录收尾 |
| 工作树 | 稳定交接状态为干净；同步、发布、验收和资源清理均已完成 |
| XY2API 产品版本 | `0.0.7`；正式 [v0.0.7 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.7) 为当前 latest |
| 已审计的 Sub2API 基线 | `v0.2.4` / commit `5de5e2bed035d43591a2e10e51f420ef6a84eb98`；已通过 PR `#22` 合入 `main` |
| 基线 provenance | `UPSTREAM_BASE.json` 状态为 `resolved`，7 个人工冲突均已记录；同步 PR [#22](https://github.com/liulixin-lex/xy2api/pull/22) 以 merge commit `ea48f08fc8` 合入 |
| 本地远端 | `origin` 可读写；`upstream` 仅允许 fetch，push URL 为 `DISABLED` |
| 当前环境工具 | `git`、`python3 3.12.3`、`gh`、Docker、Node、Corepack、pnpm 可用；本地无 Go 命令，已用 Go 1.27 Docker 完成后端验证 |

Sub2API `v0.2.4` 已通过同步 PR 合入 `main`，XY2API `v0.0.7-rc.1` 与正式 `v0.0.7` 已完成 Release、五平台制品、双架构镜像和隔离部署验收。当前无进行中同步或发布工作；生产升级前必须备份 PostgreSQL、Redis 和 `/app/data`。

## 进行中的工作

- 无。

## 当前重要事项

### Sub2API v0.2.4 与 XY2API v0.0.7 已完成

以下状态于 `2026-09-11T13:09Z` 通过本地 Git、GitHub Actions、Release、GHCR 和隔离 Docker 环境核实：

- 预检 PR [#21](https://github.com/liulixin-lex/xy2api/pull/21) 登记 3 个新冲突路径；同步 PR [#22](https://github.com/liulixin-lex/xy2api/pull/22) 的最终 head `b3d6b4d21ae042cdba016341809fef6775a1409d` 在 16/16 检查通过后，以 merge commit `ea48f08fc8d436d43dceacdc9728c3928e95cd0d` 合入。上游 annotated tag object 为 `d681d0798064ee0ffff376d19687d12f09fe600f`，目标 commit 为 `5de5e2bed035d43591a2e10e51f420ef6a84eb98`，签名状态为 `unsigned`。
- 本次纳入 70 个上游提交并裁决 7 个冲突；新增 MiniMax、HTTP/2 keepalive、Grok 媒体资格、Image 2.5、OpenAI 周成本聚合、监控排名及上游稳定性修正。完整矩阵见 `docs/upstream-sync/v0.2.4.json`。
- 上游迁移 237 与 XY2API 已发布的 237/238 编号冲突，已只追加为 `239_add_minimax_platform.sql`；历史 285 个 migration checksum 全部不变，新 checksum 为 `f4c73d2dbce114ca7ade1aac51998c3465490f4f3c9b3e868e53590f3fa8601b`。
- RC [v0.0.7-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.7-rc.1) 的 annotated tag object 为 `6b28d99a4faa815ee769f7b7aac2e95db898fb21`，目标为同步 merge commit；Release run [34596879431](https://github.com/liulixin-lex/xy2api/actions/runs/34596879431) 成功。五个制品 SHA-256 全部通过；GHCR digest `sha256:11c2515921c4800ff0a44ff60629573adf0abe1cbc7f2f467eecf22ebc3d9006` 包含 `linux/amd64` 与 `linux/arm64`，OCI version/revision 正确。
- 隔离验证覆盖 RC 全新安装、正式 `v0.0.6` 原地升级、回切旧镜像和再前滚；健康、管理员登录、286 条迁移、四处 MiniMax CHECK 约束、两个主代理共享备用代理的新关系数据，以及 PostgreSQL、Redis、`/app/data` 标记均正常。升级前三类备份已生成且 checksum 稳定；镜像回滚可用，但生产仍必须保留完整备份。
- 正式版 PR [#23](https://github.com/liulixin-lex/xy2api/pull/23) 的固定 head `0bc35438c9f1ffd92334482de25689a678ad4ab6` 在 16/16 检查通过后，以 merge commit `a3c0209184b65a6a9ae1383b725022f92cb9fb6e` 合入；合并后主线 CI run `34600004386` 与安全扫描 run `34600004385` 均成功。
- 正式 [v0.0.7 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.7) 非 draft、非 prerelease 且为 latest；annotated tag object 为 `64f44b2fa10f7214be4852d3d72cafd1be5b3307`，目标为上述正式 merge commit，Release run [34600960509](https://github.com/liulixin-lex/xy2api/actions/runs/34600960509) 成功。五个制品 SHA-256 全部通过；GHCR `0.0.7`、`latest`、`0.0`、`0` 均指向双架构 digest `sha256:9874af4356e038ff7bd908cf6e26c9389953c6a6b83a712fcee83f557fdd1349`，OCI version/revision 为 `0.0.7` / `a3c0209184b65a6a9ae1383b725022f92cb9fb6e`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装及三服务重启验证；版本、健康、登录、迁移、约束与三类持久化数据均符合预期。任务专用容器、卷、网络、镜像、下载文件、构建产物及已合并短期分支已清理，原有 `book-keeper_default` 网络未改动。

### Sub2API v0.2.3 与 XY2API v0.0.6 已完成

以下状态于 `2026-09-08T14:27Z` 通过本地 Git、GitHub Actions、Release、GHCR 和隔离 Docker 环境核实：

- 同步 PR [#17](https://github.com/liulixin-lex/xy2api/pull/17) 的最终 head `b8d84c1d97dbe4916176d8e1dd83d37d82dd7824` 在 16/16 检查通过后，以 merge commit `867097dc4b5cecb60c0b43c85f9a9d8a1e6c6d4c` 合入。固定 Sub2API `v0.2.3` 的 annotated tag object 为 `fe2b5c04b1c9503fba7e01a099b206f14867cfc1`，目标 commit 为 `8fa67d477d6651a744754392a8982ea589c26ae6`，签名状态为 `unsigned`。
- 本次纳入 111 个上游提交与 279 个上游变更文件，人工裁决 21 个冲突；保留 XY2API 认证、部署、长上下文定价和双版本契约，纳入上游模型白名单、simple mode、请求模型归一化与网关修正。详细矩阵见 `docs/upstream-sync/v0.2.3.json`。
- 迁移编号冲突已适配：保持 XY2API 已发布 `235_group_long_context_pricing_models.sql` 及 checksum 不变，将上游迁移顺延为 236/237，追加 238 认证缓存失效迁移。
- RC [v0.0.6-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.6-rc.1) 为 annotated prerelease，Release run [34232579648](https://github.com/liulixin-lex/xy2api/actions/runs/34232579648) 成功。5 个平台归档均通过 `checksums.txt` 复算；GHCR manifest digest 为 `sha256:8ad56a376bd80afba8bb6aee2652afeb3ab904e5ce2415627ff1e09443f9899c`，包含 `linux/amd64` 和 `linux/arm64`，OCI version/revision 为 `0.0.6-rc.1` / `867097dc4b5cecb60c0b43c85f9a9d8a1e6c6d4c`。
- 隔离 Docker 验证覆盖 RC 全新安装，以及从正式 `v0.0.5` 原地升级；健康、管理员登录、运行时版本、新列与 236–238 迁移、模型白名单、长上下文配置、Redis 和业务标记数据均符合预期。
- 升级前对 PostgreSQL、Redis 和 `/app/data` 生成并复算备份；在全新卷上完整恢复后，`v0.0.5` 可重新启动、登录并读取旧列和全部标记数据。因 236 包含列重命名，生产回滚必须同时恢复这三类备份，不能仅切回旧镜像。
- 正式版 PR [#18](https://github.com/liulixin-lex/xy2api/pull/18) 的固定 head `e70d41d5e3e7dbef0d351007be20ca21663a3675` 在 16/16 检查通过后，以 merge commit `e065f39c7bf62ca2634e8d565f59b01131af744b` 合入；该提交的 CI run `34236476773` 与安全扫描 run `34236476766` 成功。
- 正式 [v0.0.6 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.6) 非 draft、非 prerelease 且为当前 latest，Release run [34236559731](https://github.com/liulixin-lex/xy2api/actions/runs/34236559731) 成功。annotated tag object 为 `edcbacef7d140f67698412f8acd4e3abfbf44df2`，目标为正式 merge commit。
- 5 个正式平台归档全部通过 `checksums.txt` 复算。GHCR `0.0.6` manifest digest 为 `sha256:bc755af87107381281a59201288fd21f7ec367034c0435f222802d7df35b7e70`，包含 `linux/amd64` 与 `linux/arm64`；`0.0`、`0`、`latest` 指向同一 digest，OCI version/revision 为 `0.0.6` / `e065f39c7bf62ca2634e8d565f59b01131af744b`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装：运行时版本、健康检查、管理员登录、模型白名单列和 236–238 迁移均符合预期。上游最新正式 Release 在收尾时仍为 `v0.2.3`。

### 分组按模型启用长上下文阶梯计费与 XY2API v0.0.5 已完成

以下状态于 `2026-09-05T18:56Z` 通过本地 Git、GitHub Actions、Release 和 GHCR 核实：

- 功能 PR [#13](https://github.com/liulixin-lex/xy2api/pull/13) 的最终 head 为 `1c345b4fd3a1caa65af9afab83a84ea6936a65b8`，全部保护检查通过后以 merge commit `ab9ddee56ad238ec121fbbc9e60e446ad9e2b862` 合入；实现分组阶梯计费的全部模型/指定模型范围、精确与末尾通配匹配、旧分组兼容、统一扣费判断、认证缓存、旧 OpenAI 结算路径、模型广场实付展示和管理表单。
- 新增 migration `235_group_long_context_pricing_models.sql`，旧迁移及既有 checksum 未改写；新字段纳入 Ent、仓储、API、复制、认证快照版本与数据库缓存失效触发器。指定范围内未匹配模型保持基础档，账号开关不能越过分组名单重新启用阶梯。
- 正式版 PR [#14](https://github.com/liulixin-lex/xy2api/pull/14) 将产品版本晋级到 `0.0.5`，兼容基线保持 Sub2API `v0.2.1`；全部保护检查通过后以 merge commit `38cf85ab4a3870d7fc60041d41f60a8b9b4b0e5b` 合入。该提交的主线 CI run `33984747838` 与安全扫描 run `33984747861` 均成功。
- 正式 [v0.0.5 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.5) 非 draft、非 prerelease，且为当前 latest；Release run `33984811500` 成功。annotated tag object 为 `68aabf28257590906279f457e1442c8bf629c528`，目标为上述正式 merge commit。
- 5 个平台包已下载并通过 `checksums.txt` 的 SHA-256 复算。GHCR `0.0.5` manifest digest 为 `sha256:f7082949109df5f7150d4e4e24eb75476a59e18624d0b3bfa28da2659b9a2415`，包含 `linux/amd64` 与 `linux/arm64`；`0.0`、`0`、`latest` 指向同一 digest，两种架构的 OCI version/revision 均为 `0.0.5` / `38cf85ab4a3870d7fc60041d41f60a8b9b4b0e5b`。

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
- 当前 GoReleaser manifest 配置会让 RC 同时更新 GHCR `latest`、主版本和次版本别名；本次 RC 发布后这些别名曾短暂指向 RC，已在正式发布后全部恢复为 `v0.0.7` digest。后续应单独调整发布策略，避免 prerelease 改写稳定别名。
- GitHub Actions 在本次 RC/正式 Release 中提示 `docker/login-action@v3`、`docker/setup-buildx-action@v3`、`docker/setup-qemu-action@v3` 的 Node.js 20 runtime 已弃用，Runner 强制切到 Node.js 24 后仍成功；这是后续 Action 版本维护项，不是本次发布阻塞。

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

### 2026-09-05T18:56:06Z — `20260905-release-group-model-long-context-pricing` — 完成

- 请求/目标：规范提交并推送分组按模型启用长上下文阶梯计费功能，使用本机 GitHub CLI 凭据通过受保护分支合入，并正式发布 XY2API `v0.0.5`。
- 开始状态：位于 `feat/group-model-long-context-pricing`，功能实现和本地验证已完成但尚未提交；`main` 干净，正式版本为 `v0.0.4`，分支保护要求 CI、安全扫描和集成测试通过后才能合入。
- 完成操作：提交并推送功能改动 `2e5218621`；首次 CI 暴露缓存失效集成测试将布尔字段写为旧默认值的问题，改为确定性翻转布尔值并写入非空模型价格后，提交修正 `1c345b4fd`；通过 PR #13 合入功能。随后在独立分支将产品版本晋级到 `0.0.5`，保持 Sub2API 兼容版本 `0.2.1`，以提交 `d6fe27de0` 经 PR #14 合入。创建并推送 annotated tag `v0.0.5`，完成正式 GitHub Release、平台制品和 GHCR 镜像发布。
- 修改文件：功能提交覆盖分组 schema/迁移/Ent 生成代码、管理 API 与仓储、认证缓存、统一及旧计费路径、模型广场服务、管理前端与测试；版本晋级修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本次收尾仅修改本记忆文件。
- 验证：功能与发布 PR 的全部必需 CI/安全检查通过；真实 PostgreSQL 缓存失效集成测试通过；正式 merge 的 CI run `33984747838`、安全扫描 run `33984747861` 及 Release run `33984811500` 成功；Release 为正式 latest，5 个平台包 SHA-256 全部通过；远端标签为 annotated tag 并指向 `38cf85ab4`；GHCR `0.0.5`、`0.0`、`0`、`latest` 均为相同 amd64/arm64 manifest，OCI 版本和提交标签正确。
- 卡点/风险：无已知阻塞。首次 CI 失败来自测试未保证更新值发生变化，修正后本地真实 PostgreSQL 测试及后续两套 PR 检查均通过。新增 migration 为 additive；生产升级仍应按 Release 说明先备份 PostgreSQL、Redis 与 `/app/data`。
- 下一步：无。后续维护从 `main` 开始，并保留 `v0.0.5` 标签与 Release；动态状态以 GitHub 和 GHCR 重新核实为准。

### 2026-09-08T14:27:42Z — `20260908-sub2api-v0.2.3-full-release` — 完成

- 请求/目标：分析 GitHub 仓库与上游同步状态，按仓库标准完成 Sub2API 最新正式版同步、审计、提交、RC 验证与 XY2API 正式发版全流程。
- 开始状态：`main` 为 `5d96e0fc203145981c7eb2ddc78e226ad1502b22`，与 `origin/main` 一致且工作树干净；XY2API `0.0.5` / Sub2API compat `0.2.1`；无开放 PR。定时同步正常，但上游在其上次运行后新发布了 `v0.2.3`，因此仓库处于正常的新版本待同步状态。
- 完成操作：固定并二次核对上游 annotated tag object、目标 commit、fork 起点和 merge base；生成三方影响报告；通过预备 PR #16 登记新冲突，合并 111 个上游提交并逐项裁决 21 个冲突；完成模块路径、Ent/Wire、迁移编号与 checksum、provenance 适配，通过同步 PR #17 合入。随后发布并验收 `v0.0.6-rc.1`，通过独立版本 PR #18 晋级并发布正式 `v0.0.6`，清理任务专用容器、卷、网络、预览 worktree 和已合并短期分支。
- 修改文件：完整同步矩阵见 `docs/upstream-sync/v0.2.3.json` 和 PR #17；预备策略见 PR #16；正式版本晋级仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本次收尾仅更新本记忆文件。
- 验证：同步工具 11/11、strict doctor、provenance/compatibility audit、冲突标记与 module path 检查、Compose/部署脚本；Go 1.27 unit/integration、真实 PostgreSQL/Redis repository 集成；前端 lint、typecheck、1901 项测试和生产构建；PR #16、#17、#18 所有必需检查；RC/正式 Release runs `34232579648`、`34236559731`；两版各 5 个制品的 SHA-256；双架构 GHCR 与 OCI 标签；RC 全新安装、`v0.0.5` 原地升级、PostgreSQL/Redis/`/app/data` 完整备份恢复回滚，以及正式版全新安装均通过。
- 卡点/风险：无已知阻塞。上游迁移编号 235 与 XY2API 已发布迁移冲突，已通过只追加 236–238 解决，旧 235 及 checksum 未改写。由于 236 包含列重命名，生产回滚必须恢复升级前 PostgreSQL、Redis 和 `/app/data` 备份，不能仅切换回旧镜像。
- 下一步：无。后续维护应保留 `sub2api/v0.2.3`、`v0.0.6-rc.1` 和 `v0.0.6` 标签，从 `main`、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-11T12:24:47Z — `20260910-sub2api-v0.2.4-full-release` — 进行中

- 请求/目标：分析 GitHub 仓库同步状态，按仓库标准完成 Sub2API 下一正式版同步、审计、提交、RC 与 XY2API 正式发版全流程。
- 开始状态：`main` 为 `33d05a4`，与 `origin/main` 一致且工作树干净；XY2API `0.0.6` / Sub2API compat `0.2.3`；同步机制正常，上游已新发布正式 `v0.2.4`，因此处于正常待同步状态。
- 完成操作：固定上游 annotated tag object、目标 commit、fork 起点与 merge base；通过预检 PR #21 登记 3 个新冲突路径；生成三方报告并集成 70 个上游提交，裁决 7 个冲突，完成模块归一、迁移编号/checksum、生成代码与 provenance 适配；同步 PR #22 在 16/16 检查通过后以 `ea48f08fc8` 合入；发布并验收 `v0.0.7-rc.1`。
- 修改文件：完整同步矩阵见 `docs/upstream-sync/v0.2.4.json` 和 PR #22，预检策略见 PR #21；当前正式版候选分支仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件。
- 验证：同步工具 13/13、strict doctor/audit、Compose/部署脚本、Go 1.27 Ent/Wire 零差异、unit/integration/build、前端 lint/typecheck/i18n/1,975 tests/build、PR #21/#22 全部检查及合并后主线 CI/安全扫描均通过。RC Release run `34596879431` 成功，五个平台制品的 SHA-256 全部通过，GHCR digest `sha256:11c2515921c4800ff0a44ff60629573adf0abe1cbc7f2f467eecf22ebc3d9006` 包含 amd64/arm64 且 OCI version/revision 正确。隔离环境中 RC 全新安装、`v0.0.6` 原地升级、旧镜像回切、再前滚、286 条迁移、MiniMax 约束、共享备用代理关系、登录和三类持久化数据均通过；升级前 PostgreSQL/Redis/`/app/data` 备份已生成并复算稳定。
- 卡点/风险：无当前阻塞。迁移 239 只扩展 CHECK 约束，旧镜像已验证可在升级后 schema 与新关系数据上运行；生产仍应保留升级前三类备份，不宣称 SQL 自动降级。
- 下一步：通过独立正式版 PR 晋级 `0.0.7`，验收正式 Release/镜像/新装，清理隔离资源与已合并短期分支，再将本记录更新为完成。

### 2026-09-11T13:09:37Z — `20260910-sub2api-v0.2.4-full-release` — 完成

- 请求/目标：分析 GitHub 仓库与同步状态，按仓库标准完成 Sub2API 下一正式版的预检、三方合并、审计、PR、RC、升级/回滚验收、正式发布与清理全流程。
- 开始状态：`main` 为 `33d05a4`，与 `origin/main` 一致且工作树干净；XY2API `0.0.6` / Sub2API compat `0.2.3`；无开放 PR。自动同步机制正常，但上游已在上次检查后发布 `v0.2.4`，因此处于正常的新版本待同步状态。
- 完成操作：固定并二次核对上游 tag object、目标 commit、fork 起点与 merge base；通过 PR #21 完成策略预检，通过 PR #22 完成 70 个上游提交、7 个冲突、迁移/provenance/生成代码适配；发布并验收 `v0.0.7-rc.1`；通过独立 PR #23 晋级并发布正式 `v0.0.7`；完成所有任务资源与已合并分支清理。
- 修改文件：完整同步改动与矩阵见 `docs/upstream-sync/v0.2.4.json` 和 PR #22；预检策略见 PR #21；正式晋级仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；收尾仅更新本记忆文件。
- 验证：同步工具 13/13、strict doctor/audit、上游标签/祖先/provenance、Compose/部署脚本、Go 1.27 Ent/Wire 零差异、unit/integration/build、前端 lint/typecheck/i18n/1,975 tests/build；PR #21/#22/#23 的全部必需检查与两次合并后主线 CI/安全扫描；RC/正式 Release runs `34596879431` / `34600960509`；两版各五个制品 SHA-256、双架构 GHCR 与 OCI 标签；RC 全新安装、`v0.0.6` 升级/回滚/再前滚、正式版全新安装及三服务重启持久化均通过。
- 卡点/风险：无已知功能阻塞。迁移 239 为只追加的 CHECK 约束扩展，旧镜像回切已验证；生产仍应备份三类数据。RC 暂时改写稳定 GHCR 别名与 Release Action runtime 弃用提示已登记为后续发布维护项，本次正式别名与发布结果均正常。
- 下一步：无。后续维护应保留 `sub2api/v0.2.4`、`v0.0.7-rc.1` 和 `v0.0.7` 标签，从 `main`、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。
