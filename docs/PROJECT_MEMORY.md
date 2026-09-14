# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

最后更新：`2026-09-14T16:35:00Z`（UTC）

| 项目 | 当前事实 |
| --- | --- |
| 仓库路径 | `/xy/xy2api` |
| 当前分支 | 发布已合入 `main`；本条文档在 `docs/v0.0.10-closeout` 收尾，后续用Git核实实际HEAD |
| 发布提交 | `v0.0.10` / `e695c0356c972f398045f87b28a9cccda8a66176`，PR #29 |
| 工作树 | 协议解析、低负载监测、原生Select和三轮保留均已提交、推送并发布；本次收尾仅更新记忆 |
| XY2API 产品版本 | `VERSION` 与 `UPSTREAM_BASE.json.xy2api_version` 均为 `0.0.10`；正式latest及制品已核验 |
| 已审计的 Sub2API 基线 | `v0.2.4` / commit `5de5e2bed035d43591a2e10e51f420ef6a84eb98`；已通过 PR `#22` 合入 `main` |
| 基线 provenance | `UPSTREAM_BASE.json` 状态为 `resolved`，7 个人工冲突均已记录；同步 PR [#22](https://github.com/liulixin-lex/xy2api/pull/22) 以 merge commit `ea48f08fc8` 合入 |
| 本地远端 | `origin` 可读写；`upstream` 仅允许 fetch，push URL 为 `DISABLED` |
| 当前环境工具 | `git`、`python3 3.12.3`、`gh`、Docker、Node、Corepack、pnpm 可用；本地无 Go 命令，已用 Go 1.27 Docker 完成后端验证 |

Sub2API 兼容基线保持 `v0.2.4`。下方历史日志保留原样；响应兼容和监测改进已在v0.0.10发布，没有升级生产实例。

## 进行中的工作

- `20260914-v0.0.10-release`：功能PR #29、主线CI、安全扫描、Release及五平台包/双架构镜像均已通过；发布完成。本条记录随收尾文档合入，实际PR状态以GitHub为准。

- `20260914-iq-ui-refinement`：已由v0.0.10发布承接；UI和三轮保留已提交发布，未部署生产。

- `20260914-iq-monitoring-implementation`：已由v0.0.10发布承接；历史本地验证证据保留，真实账号观察尚未执行。

- `20260914-iq-monitoring-plan`：已由本轮实施承接；原文档验证保持历史记录，当前实现与验收进度以monitoring/tasks.md及monitor-impl-*记录为准。

- `20260914-iq-protocol` 已随v0.0.10提交发布，无真实上游调用或生产部署。

- `20260914-iq-stability-implementation` 的后续发布已在 v0.0.9 日志记录；历史未提交描述不代表当前状态。

- `20260914-iq-stability-plan` 已由本轮实施承接；完整规格与任务证据在 `openspec/changes/stabilize-openai-iq-check/`。

- `v0.0.8` 功能、提交、发布和制品验收已完成；本次文档提交记录收尾状态。原始 v0.0.8 功能无遗留实施事项；新增改进需求见上一条。四项交付文件及发布日志继续保留于 `/xy/artifacts/openai-iq-check/`。

## 当前重要事项

### OpenAI 智商检测与 XY2API v0.0.8 已完成

- 功能和版本通过 [PR #25](https://github.com/liulixin-lex/xy2api/pull/25) 合入；最终 head `307679ee82407a04c6f4055d8ce68c6d4151b3f5` 的 16/16 检查通过，merge commit 为 `accb4ad7656a2ca3d2eee3df601e38f4e5b02157`。
- 正式 annotated tag `v0.0.8` 的 tag object 为 `3c646ffd000bfdc052183bd2ec94b9b2d3cfea56`。Release run [34803057678](https://github.com/liulixin-lex/xy2api/actions/runs/34803057678) 成功，Release 非 draft、非 prerelease 且为 latest；合并后的主线 CI `34803053670` 与安全扫描 `34803053686` 均成功。
- 5 个平台安装包均已实际下载并通过 SHA-256 复算；Linux amd64 二进制 `-version` 显示产品 `0.0.8`、兼容 `0.2.4` 和正确发布提交，退出码为 0。
- GHCR `0.0.8`、`latest`、`0.0`、`0` 均指向 `sha256:b1aa56e04248f496c8c2bce7230d99d924ac6e13dabac88583f73c479c46237a`，包含 linux/amd64 与 linux/arm64，OCI version/revision 正确。
- 功能默认关闭；判题接受整数 answer JSON 和明确结论为21的解释，异常为未知。追加 migration 240，历史迁移校验不变。使用说明见 `docs/OPENAI_IQ_CHECK.md`。
- 发布门禁发现并修复原有清理测试参数、SQL mock 新字段/锁查询，以及新增集成测试遗留 scheduler_outbox 的隔离问题。最终完整单测、集成测试和静态检查通过；本机完整仓储集成包也通过。无真实账号调用或生产部署。

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

### 2026-09-13T19:41:31Z — `20260913-openai-iq-check` — 源码实施与功能验证完成

- 请求/目标：按用户批准方案实现 OpenAI 周期糖果题检测；用户随后明确允许最终答案为21的解释文字，优先要求简洁JSON，并要求将实现写入真实仓库的标准路径。
- 开始状态：`main` / `20c307a9b4a6ee1a8ef126d9aa0baa9aa5c500e6`，原工作树干净。最初在独立副本实施；随后按最新要求写入 `/xy/xy2api`，origin 已确认是 `github.com/liulixin-lex/xy2api`。原始源码归档保留于交付目录 `BASELINE.tar.gz`。
- 完成操作：新增 `Account.IQCheck` 独立调度限制、默认关闭/15分钟的配置、三态筛选、最多10个跨实例租约、120秒请求超时、两轮记录事务保留、失效旧任务保护；复用 OAuth/代理/TLS/插件传输。请求固定 `gpt-6-astra` / `low`、`store:false`，要求整数 `answer` JSON，不注入标准答案且不携带历史；兼容明确解释性结论。创建/更新/批量设置、管理员接口、蓝色开关、频率编辑、立即检测、历史弹窗与中英文文案已接入。
- 变更位置：`backend/internal/pkg/iqcheck/`、`service/iq_check_service.go`、`repository/account_iq_check.go`、账号 DTO/路由与缓存、追加迁移240及校验清单、Ent/Wire生成文件；前端沿用账号页面及组件目录。使用说明为 `docs/OPENAI_IQ_CHECK.md`。
- 验证：判分与服务定向测试通过，覆盖JSON/解释/歧义数字、完整与中断响应、OAuth/Setup Token/API key、固定参数、独立请求、异常状态、人工限制与旧缓存候选。真实 PostgreSQL 集成验证了状态过滤、两轮保留、关闭/重开/换凭据、正常刷新、软删除、跨实例10租约和过期恢复。前端269个测试文件、1977项测试通过，lint、类型/i18n检查与生产构建通过；Go服务端构建通过。原有迁移校验值全部不变。
- 交付：固定四角色为 `/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、可执行 `ROLLBACK.sh`。事务脚本在隔离副本上对相同模拟账号状态输入运行原版、修改版、回滚版，验证原始源码哈希及可确定重建的归档哈希；实际状态与字面输出以 `checkpoint.json` 和 `VERIFICATION.txt` 为准。回滚脚本恢复源码，不是数据库降级脚本。
- 限制：未调用真实账号。浏览器自动化返回 `browser_no_host`，未进行真实浏览器视觉验收；移动端沿用现有 DataTable 的同名单元格插槽。测试期间因4GB内存压力调整为串行构建；已保留失败及重跑日志，不将中断的编译记为通过。
- 后续：代码可从本地工作区审阅；本任务未授权推送、发布或部署。需要继续交付核验时执行上述事务脚本，它会复用已确认阶段。

### 2026-09-14T03:50:45Z — `20260914-openai-iq-check-v0.0.8-release` — 完成

- 请求/目标：使用本机 GitHub 登录态提交、推送智商检测功能，并发布下一正式版。
- 开始状态：`main` / `20c307a9b4`，功能已在工作区实现和验证，产品版本 `0.0.7`，尚未推送。用户本轮明确授权提交、推送、合入和发版。
- 完成操作：以本机 `liulixin-lex` 登录态推送 `release/0.0.8`，通过受保护 PR #25 合入功能、版本晋级及测试修复；推送 annotated `v0.0.8`，由现有 Release 工作流生成正式 latest Release 和镜像；下载制品并校验，更新仓库记忆。
- 修改文件：功能覆盖 backend/frontend 标准目录、追加 migration 240、Ent/Wire；版本修改 `VERSION` 与 `UPSTREAM_BASE.json`；发布修复覆盖 `wire_gen_test.go`、账号 SQL mock、IQ 集成清理及错误返回值显式处理。本次收尾只更新本文件。
- 验证：PR 最终16/16检查、主线CI/安全扫描与Release均成功；本机完整仓储单测/集成包、服务清理回归通过；5个平台包SHA-256、Linux版本输出、双架构OCI标签和稳定镜像别名均通过。相同模拟输入完成 BASELINE/MODIFIED/ROLLBACK，补丁可确定重建、回滚源码哈希恢复一致；固定四角色文件保留完整命令和字面日志。
- 卡点/风险：无发布阻塞。前几轮CI失败和本机过时静态检查中止均保留日志，未将失败记为通过。未进行本轮生产升级或真实账号检测；浏览器视觉验证限制沿用实现记录。
- 下一步：无待发布事项；后续从 `main` 和正式 `v0.0.8` 重新核实动态状态。原始源码归档和回滚脚本用于源码恢复，不是数据库降级。

### 2026-09-14T07:31:07Z — `20260914-iq-stability-plan` — 规划完成

- 请求/目标：规划规范稳定的糖果题检测；追加在编辑间隔旁配置模型及思考深度，支持账号模型拉取与自定义输入。
- 开始状态：`main` / `dff987fb16d206e354a21bd4055987c2b5d73549`，工作树干净；四角色交付来自上一版完成状态。
- 完成操作：核对请求、判分、租约、状态及模型发现接口，实际运行当前解析器边界审计；新增 OpenSpec proposal/design/tasks/spec，明确 JSON/纯数字兼容、精确答案提取、未知原因、只读模型目录、可配置 model/reasoning_effort、旧字段省略保留、任务快照及两轮保留。
- 关键发现：歧义答案、重复 JSON 键和浮点精度可能误判聪明，解释中的反例可能误判降智；只改间隔会重置状态；模型目录可能回退默认模型，同步接口会写账号元数据，方案将提取只读公共逻辑。
- 修改文件：只新增 `openspec/changes/stabilize-openai-iq-check/` 中4份方案文档并更新本文件；业务源码、数据库与发布版本未修改。
- 验证：当前解析器副本审计命令退出0，结果是已复现缺陷而非新功能通过；方案链接、JSON示例和20个 WHEN/THEN 场景结构检查通过，git diff --check通过。OpenSpec CLI未安装，未声称运行官方校验器；首次自写检查误设场景数量17导致退出1，修正为检查场景结构后通过，失败记录保留。固定四角色继续由事务脚本收纳，字面 BASELINE/MODIFIED/ROLLBACK 及哈希以 VERIFICATION.txt 为准。
- 边界：单次判定和未知解除自身限制保持；无法可靠提取答案改为未知属于拟议行为变更。未调用真实账号，未实现或发布本改进；模型实际支持及任意自然语言准确识别不作保证。
- 下一步：实施任务见 `openspec/changes/stabilize-openai-iq-check/tasks.md`；不要把方案中的未来行为当作当前代码已实现。


### 2026-09-14T09:14:35Z — `20260914-iq-stability-implementation` — 完成

- 请求/目标：执行稳定检测改进方案，账号编辑间隔旁提供自定义模型与思考深度；接受明确最终答案21的解释文字，规范简洁输出并防止歧义误判。
- 开始状态：`main` / `dff987fb16d206e354a21bd4055987c2b5d73549`，已有4份OpenSpec方案和记忆改动；在 `feat/iq-check-stability` 实施。前一轮四角色完整备份于 `/xy/artifacts/openai-iq-check/pre-stability-implementation/`；原始源码归档不变。
- 完成操作：`IQCheckSettings` 可选字段 `model/reasoning_effort/output_mode`，默认 Astra/low/compat；只读模型目录、5分钟缓存、来源标记、自定义值和上游默认省略参数；candy-v2 精确答案提取与拒答/冲突/重复JSON/精度边界；任务快照、配置失效、只改间隔保留状态、租约过期释放和周期抖动；双协议规范输出与严格Schema；记录规范化答案及原文折叠；复制/导入保留配置但关闭检测；批量仅修改勾选字段。
- 文件位置：沿用 `backend/internal/domain/iq_check.go`、`pkg/iqcheck/`、`service/iq_check*.go`、`repository/account_iq_check.go`、原账号管理路由/处理器，前端原组件/types/api/i18n目录。追加迁移241，既有SQL及checksum不变；JSON域与SQL记录表不改变Ent/Wire结构或依赖，未制造生成代码变更。使用及待发布说明写入已跟踪的 `docs/OPENAI_IQ_CHECK.md`。
- 验证：最终Go相关单测（含配置、三协议模拟、目录只读、精确数值、复制和导入导出）通过；完整repository PostgreSQL/Redis集成通过；前端原相关118项、补充59项（包含重叠复验）及记录弹窗2项通过；全仓lint、类型/i18n、前端生产构建及服务端构建通过。实际Chromium在1280px/390px验证三控件排列、无横向溢出、目录拉取、已知深度校验、上游默认和聚焦；Impeccable detector为[]。
- 事务：相同模拟输入运行真实 `Account.IsSchedulable()`，BASELINE 的 degraded=true，MODIFIED 的 degraded=false，ROLLBACK 的 degraded=true；人工关闭始终false。补丁可重建同哈希归档；回滚源码manifest SHA-256=`eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7` 与原始一致。额外旧版/新版解析器同输入和回滚输出、追加schema旧列读取/事务回滚均通过。
- 失败记录：首次构建退出137（4GB内存下并行Go/Vite），改为串行限制Node堆后通过；副本协议命名不一致已修复；测试桩缺字段、断言误读脱敏revision和批量API参数已修正。所有失败及重跑字面stdout/stderr/退出码保存在四角色验证账本，不将中止记为通过。任务专用6个测试容器已清理。
- 交付：固定 `/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、可执行 `ROLLBACK.sh`，后者依赖同目录保留的 `BASELINE.tar.gz`。最终复验入口 `stability_finalize.py`，机器状态见 `checkpoint.json`。源码回滚不操作生产数据库。
- 边界：未调用真实账号逐模型组合验收，不能保证第三方模型清单或返回格式始终遵循规范；未对任意自然语言作100%语义识别承诺。新行为是无法可靠提取答案归未知并解除检测自身限制。没有新版本发布，产品仍0.0.8；本轮按批准设计完成本地实现和验证，发布流程单独执行。


### 2026-09-14T10:00:39Z — `20260914-openai-iq-check-v0.0.9-release` — 完成

- 请求/目标：提交、推送并发布 XY2API `v0.0.9`。
- 完成操作：功能分支 `feat/iq-check-stability` 通过 PR #27 合入 `main`；补齐 CI errcheck 后全部必需检查通过；推送 annotated `v0.0.9` 标签并完成正式 Release。
- 验证：主线 CI run `34829548235`、安全扫描 `34829548211` 与 Release run `34829581131` 成功；Release 为 latest、非 draft、非 prerelease。五个平台制品 SHA-256 通过；Linux amd64 二进制显示 `0.0.9`/Sub2API `0.2.4`；GHCR `0.0.9` 双架构 digest `sha256:b742241597192b6f62dc32898436f78cda6d3f20afe4cc4a1bb4356988b82ab6`，`latest`、`0.0`、`0` 别名一致。
- 交付：`/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh` 已按最终提交重新打开；源码事务 BASELINE/MODIFIED/ROLLBACK 行为及恢复哈希通过。
- 卡点/风险：无。未调用真实账号或部署生产。
- 下一步：无。


### 2026-09-14T12:09:54Z — `20260914-iq-protocol` — 本地实现与验证

- 请求：按只读分析方案优化本地xy2api，未授权本轮部署、发版或真实检测请求。开始于docs/v0.0.9-closeout的cb5dc5ae0，实际修改分支fix/iq-response-protocol。
- 改动：iqcheck.ParseHTTP统一媒体识别和有界解压；SSE按事件类型解析，辅助事件不强制符合Response类型，只从完成终态assistant最终输出判分；重复关键字段、冲突终态、损坏流和超限均未知。新增iq-response-v3诊断，迁移242增加account_iq_check_results.diagnostic，随最近两轮一并清理。界面失败原因提前，诊断可折叠且适配390px。
- 验证：解析器全包、服务及仓储定向单测通过；完整repository PostgreSQL/Redis集成通过（两轮保留、旧NULL记录、4KiB约束、配置和租约边界）；前端全仓lint、组件/i18n测试、生产构建（含类型）通过；Chromium1280/390布局及键盘交互通过。新增解析模块golangci-lint为0 issues，整个service/repository静态分析运行超过10分钟后主动中止，未标为通过。隔离干净副本上游审计通过。
- 修复过程：初次类型检查发现重复locale键，已删除后构建通过；Go编译期间变更造成vet导入异常，最终单测重跑通过；迁移242校验改为仓库要求的TrimSpace算法，全部历史校验保持一致；并发构建内存压力导致主动中止前端，串行重跑通过。所有退出码与字面输出保存protocol-*记录，不掩盖失败。
- 同输入证据：本轮0.0.9基线标准SSE聪明，辅助response扩展及误标SSE未知invalid_response；修改版这三者均聪明，损坏JSON未知invalid_event_json、仅delta未知incomplete_response。执行ROLLBACK_CURRENT.sh后哈希及输出恢复基线，再应用补丁恢复修改版。原累计BASELINE.tar.gz不变，四角色仍沿用openai-iq-check原路径。
- 限制：fixture为合成样本，没有取得生产失败原文，不能声称主站根因已证明或所有OAuth已恢复。后续部署后可用每轮diagnostic区分真实失败；本轮版本仍0.0.9。服务端构建退出0；累计补丁逐文件重建一致，ROLLBACK恢复原始哈希，协议回放三轮退出0。回放器对空目录误判断的问题已修正后通过；历史失败保留。最终归档结果见同目录VERIFICATION.txt和protocol-final-audit.json。


### 2026-09-14T12:56:47Z — `20260914-iq-monitoring-plan` — 规划完成

- 请求：结合AA26-251A与全网资料规划稳定检测。目标对象保持/xy/xy2api，分支fix/iq-response-protocol、HEAD cb5dc5ae0，保留已有未提交修复。
- 证据：CISA直连403，读取NSA发布页所链同题官方PDF；第13–14页含高置信度恶意蒸馏响应调整建议，不是OpenAI实际部署、阈值或本项目触发原因证明。核对Astra模型/推理/格式/限流/数据控制官方文档；论坛只作体验报告。
- 方案：真实端点请求配置、共享业务名额、pending/start分离、去重与日预算、Retry-After及原因分类暂停；默认固定间隔，显式节约模式15→30→60；单题21与单次判分保留。排期/旧评分/新鲜度区分，正文仅最近两轮。明确不实施身份伪装、掩盖自动化或绕过反滥用机制；不能保证消除服务方误分类。
- 文件：openspec/changes/stabilize-openai-iq-check/monitoring/的proposal、design、tasks、spec、sources共5份Markdown，以及本记忆；业务代码未改，生产未查询/修改/探测。模型仍默认gpt-6-astra/low，版本仍0.0.9。
- 验证：文档结构/链接/26场景通过，逐文件核对业务源码与pre-monitoring-plan归档一致；理论96→24轮/天及75%计算通过，明确排期等待15→60分钟取舍。实际代码回放BASELINE与MODIFIED评分相同，只有方案存在性变化；执行ROLLBACK_CURRENT.sh后哈希及输出恢复，再应用补丁。首次回滚探针误把空目录当作方案，退出1，改检查proposal.md后通过；原失败日志保留。
- 交付：沿用/xy/artifacts/openai-iq-check/四角色并扩展原验证账本；原四角色备份pre-monitoring-plan，原始BASELINE.tar.gz不变。累计源码补丁/回滚及最终角色哈希以monitoring-transaction-state.json、VERIFICATION.txt为准，不用文档验证冒充未来实现验证。
- 下一步：按monitoring/tasks.md另行实施并执行相关测试；真实账号验收/提交/发布/部署未在本轮执行。

### 2026-09-14T14:45:09Z — `20260914-iq-monitoring-implementation` — 本地实现与验证完成

- 目标：执行已批准的低负载质量监测方案，保留既有OAuth协议兼容修复；在feat/iq-monitoring-controls实施，基于cb5dc5ae0，产品版本保持0.0.9。没有真实账号调用、提交、推送、发版或部署。
- 变更：IQCheck增加scheduling_mode/max_interval_minutes/daily_request_limit/timeout_seconds/quota_group，默认fixed和120秒；未显式填写预算时按基础间隔推导。分离pending和实际start，使用业务并发名额、原子日预算、显式共享组预算及冷却；错误分类退避/暂停，遵循Retry-After，手动运行去重。adaptive连续3/6次聪明后延长至2/4倍间隔，单次判题和独立质量限制保持。
- 协议和诊断：API key使用本应用真实标识并禁用普通HTTP重定向，OAuth保留现有认证协议；不伪装人工会话，不绕过拒绝，不反复请求直到答对。兼容合法辅助SSE事件及有界解压；只有完整最终回答判分。新增白名单错误、用量、冷却和脱敏诊断下载，正文和诊断随最近两轮清理。列表区分执行状态、历史评分及新鲜度；设置和批量编辑支持新字段，省略字段保留原值。
- 数据：追加迁移243，共享组计数及冷却持久化；保留迁移242。所有历史迁移字节与checksum复验通过，Wire已生成。升级时须停止旧检测工作者后统一升级，避免旧工作者写回JSON丢失新字段。
- 验证：monitor-impl-unit-coherent、完整PostgreSQL/Redis repository集成、CI同配置相关包golangci-lint（0 issues）、前端66项相关测试、全仓lint、类型/i18n、前后端构建均退出0。Chromium1280/390验证实际Vue控件无横向溢出，模型目录、深度、节约设置及键盘交互通过。测试使用模拟上游，不证明生产OAuth具体根因或服务方误分类率。
- 失败留存：内存压力、测试时间精度及测试桩等失败和成功重跑均保留。额外unit标签静态分析报告52项问题，涉及29个未改文件；按仓库CI不附加标签的相关包检查通过，没有为消除历史诊断修改无关文件。
- 同输入事务：当前源码基线为15分钟/无预算门禁，修改版为60分钟/日预算延期，降智限制仍有效；当前副本回滚后哈希和输出恢复。累计原始BASELINE为pre-IQ源码（无解析器和监测控制），MODIFIED解析合法辅助事件为聪明、损坏/不完整流为未知并执行预算门禁，ROLLBACK恢复BASELINE；三轮退出0。累计源码manifest恢复为eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7。
- 交付：沿用/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh；基线归档和历次四角色备份保留。字面命令、输出、退出码和哈希见VERIFICATION.txt，最终归档与工作区一致性见monitor-impl-final-audit.json。ROLLBACK.sh只恢复独立源码副本，不操作生产数据库。

### 2026-09-14T15:12:00Z — `20260914-iq-ui-refinement` — 本地完成

- 用户要求先提交既有检测优化，再改进编辑和记录UI、原生模型下拉与同步按钮，并把记录保留从2改为3。先提交44个文件为ea94b83d1，未推送。基线source.tar.gz及原四角色备份位于pre-ui-refinement。
- UI：直接使用common/Select替代datalist及原生select，支持搜索、自定义模型、键盘选择和关闭、弹层定位及焦点返回；同步按钮沿用ModelWhitelistSelector的绿色描边操作样式。显式同步刷新目录，保持所选模型；失败、重复点击及账号切换处理保留。思考深度显示本地化档位，仍允许未知模型自定义值；已声明不支持的档位禁用且阻止保存。蓝色启闭使用原Toggle组件。
- 表单和记录：常用设置优先，预算/超时/格式/配额组收进高级设置，批量自动展开；删除面向开发者的大段提示。记录以状态、答案、模型、深度、耗时为主，原文和技术诊断默认折叠。增加刷新、加载失败重试、独立下载错误和过期请求丢弃，下载失败保留列表。
- 保留：StartIQCheck原子删除和ListIQCheckRecords读取均改为LIMIT 3，诊断下载复用同一查询。第四轮开始删除最早轮，历史记录不补造，无需迁移。真实PostgreSQL/Redis IQ仓储集成通过，断言数据库实际计数为3、最新三条内容和正常状态转换。
- 验证：前端70项相关测试、修改文件lint、类型/i18n与生产构建通过；实际Chromium1280/390检查设置、原生下拉弹层和三条记录，无横向溢出。同步/选择、深度验证、Escape及节约模式通过，机械检测为[]。测试桩缺te、旧input选择器和动画断言已修复重跑；浏览器脚本将最大间隔误算成基础间隔的断言已修复，失败日志保留。构建输出AccountsView包含最终蓝色Toggle样式。
- 事务：相同四次输入在源码中的实际查询上回放（SQLite兼容语法fixture，PostgreSQL验证另见集成）：BASELINE保留[4,3]，MODIFIED保留[4,3,2]，ROLLBACK恢复[4,3]，其他账号记录保持。回滚源码哈希相同，再应用补丁恢复修改版。累计四角色继续使用最初pre-IQ基线，不能将其无IQ功能行为与本轮ea94b83d1基线混同。
- 交付：固定/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh；最终审计ui-refinement-final-audit.json。预览http://localhost:5188/__iq-review（模拟目录和记录，不使用真实账号）；?view=records显示记录。未提交新UI改动、推送、发版或部署。

### 2026-09-14T16:35:00Z — `20260914-v0.0.10-release` — 发布完成

- 请求：远端推送、合并并发布0.0.10。开始于feat/iq-monitoring-controls / ea94b83d1及已验证的13个UI和三轮保留改动文件。
- 操作：提交UI与版本f8de1197c，先合入遗留文档PR #28，再合并最新main到功能分支20102fbbb；PR #29固定head全部16项检查通过后合入e695c0356。创建annotated v0.0.10标签，Release run34867937142成功，正式非预发布且为latest。主线CI34867929408及安全扫描34867929289均成功。
- 制品：五个平台包实际下载并复算SHA-256全部通过，Linux amd64运行时输出0.0.10、兼容0.2.4及正确完整提交。GHCR amd64/arm64版本和revision正确；0.0.10/latest/0.0/0均指向sha256:380c3297e24cfcfd8b2fff9d31ebdaacad0060a7e94fcc709b4f320ab4310a61。
- 变更：响应协议解析、限额/退避/节约模式、原生检测设置和记录UI、最近三轮保留；追加迁移242/243，历史迁移未修改。VERSION和provenance产品字段为0.0.10，Sub2API兼容仍0.2.4。升级需停止旧检测工作者后统一升级。
- 事务：沿用固定四角色和原始pre-IQ基线；同输入BASELINE无检测保留功能，MODIFIED第四轮后保留[4,3,2]且采用原生Select，ROLLBACK恢复BASELINE并匹配源码manifest哈希eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7。补丁重建归档一致，额外本轮UI基线两轮→三轮→两轮证据保留。v010-*字面日志扩展VERIFICATION.txt，所有四角色重新打开核验。
- 失败留存：首次清理过期构建缓存遇目录类型后仅清理普通缓存文件；首次PR28 expected SHA错误被拒后使用实际完整SHA成功；旧gh不支持checks --json，改用pr view。无本轮CI失败，不把准备命令错误当作发布结果。没有生产部署或真实账号检测。
- 后续：发布已完成；收尾文档经受保护PR合入。动态状态与源码交付最终一致性见openai-iq-check/v010-final-audit.json。
