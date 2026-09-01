# XY2API 上游同步执行手册

仅在实际执行同步、合并或发版时读取本文件。项目事实以仓库当前的 `policy.json`、`UPSTREAM_BASE.json`、工作流和版本文件为准，不复制旧版本的固定 SHA。

## 1. 冻结目标与状态

定义以下值，并在全过程复用：

- `TAG`: 官方稳定标签，如 `v0.1.186`。
- `TAG_REF`: `sub2api/$TAG`。
- `TAG_OBJECT`: annotated tag 对象 SHA。
- `U`: 标签最终指向的 commit。
- `F`: 当前 `main` 起点。
- `B`: `merge-base(F, U)`。
- `SYNC_BRANCH`: `sync/sub2api-$TAG`。
- `RC_VERSION`、`FINAL_VERSION`: XY2API 自己的产品版本。

开始前：

1. 确认当前目录是 XY2API 仓库，工作树干净，`main` 与 `origin/main` 一致。
2. 确认 `upstream` fetch URL 指向 `Wei-Shaw/sub2api`，push URL 为 `DISABLED`。
3. 从官方 GitHub Release/API 选择正式、非 draft、非 prerelease 的 semver 标签。
4. 读取官方 tag ref，要求对象类型为 `tag`，保存 `TAG_OBJECT` 后再抓取。
5. 以非强制方式抓取到 `refs/tags/sub2api/$TAG`，二次比对 tag object 和 `U`。
6. 计算 `F` 和 `B`，确认 `B` 同时是 `F`、`U` 的祖先。
7. 检查当前没有未完成的 `upstream-sync` PR，也没有同名同步分支被另一轮流程占用。

任何已经存在的命名空间标签都只能验证，不能移动或覆盖。

首次环境缺少只读 remote 时执行：

```text
git remote add upstream https://github.com/Wei-Shaw/sub2api.git
git remote set-url --push upstream DISABLED
```

抓取固定标签使用非强制 refspec：

```text
git fetch --no-tags upstream refs/tags/<TAG>:refs/tags/sub2api/<TAG>
```

产品版本不跟随上游版本号。用户未指定时，默认在当前 XY2API 版本上递增 patch；如果本次有明确破坏性产品变更，再按仓库版本策略提高 minor/major。改变版本文件前先明确 `RC_VERSION` 和 `FINAL_VERSION`。

## 2. 生成三方同步分支

优先使用现有自动工作流；需要本地准备时：

```text
git switch main
git pull --ff-only origin main
git switch -c <SYNC_BRANCH>
python tools/upstream-sync/sync.py prepare \
  --tag <TAG> \
  --expected-commit <U> \
  --expected-base <B> \
  --report docs/upstream-sync/<TAG>.json
```

`prepare` 已负责：真实三方 merge、清单外冲突阻断、清单内冲突暂存 XY2API 侧、范围受控的 module 归一化、provenance 和报告提交。不要手工再做一次相同 merge。

如果只是分析，不执行 `prepare`，改用：

```text
python tools/upstream-sync/sync.py report \
  --fork <F> \
  --upstream <U> \
  --output docs/upstream-sync/<TAG>.json
```

## 3. 人工裁决与语义审查

只重点检查报告中的：

- `overlap_files`；
- `merge_conflicts` 和 `unexpected_conflicts`；
- migration、API、config、dependency、generated 影响；
- 上游提交中跨文件的原子行为；
- `policy.json` 中的兼容不变量和具名二开补丁。

按所有权处理：

- `UPSTREAM`: 接收上游行为。
- `XY_OWNED`: 保留 XY2API 产品、品牌、发布和安装策略，同时移植必要功能。
- `COMPAT_INVARIANT`: 保留外部或持久化契约，并运行绑定测试。
- `MANUAL_MERGE`: 比较 base/upstream/XY2API，写明实际裁决，不用“保留 ours”代替审查。
- `GENERATED`: 修改源定义后重新生成，不逐行维护产物。

上游若同时修改解析器、快照、计费入口和统计成本，应作为原子变更接收或回退，禁止混搭。类似原则适用于 schema/生成代码、协议/客户端和配置/示例。

完成后更新：

- `UPSTREAM_BASE.json` 中每个 `manual_resolutions[].status=resolved`；
- 顶层 `status=resolved`；
- `SUB2API_COMPAT_VERSION=<TAG 去掉 v>`；
- `VERSION=<RC_VERSION>`；
- 新 migration 的 checksum 条目，只允许追加；
- 新增或改变的二开补丁及其测试绑定。

提交保持可追踪：人工裁决、二开补丁、生成代码、provenance、RC 版本分别提交。不存在对应变化时不制造空提交。

## 4. 快速门禁与完整验证

先跑便宜门禁：

```text
python -m unittest discover -s tools/tests -p test_*.py -v
python tools/upstream-sync/sync.py audit
git diff --check
扫描冲突标记
解析 deploy/docker-compose.yml
```

然后按影响运行专项测试，最后依赖 CI 完成固定矩阵：

- Go unit、integration、race-sensitive、build、golangci-lint；
- 前端 frozen install、lint、typecheck、完整 Vitest、生产构建；
- 后端和前端安全扫描；
- Windows 插件归档、macOS/Linux 部署脚本；
- pricing、Codex、WebSocket、数据库重试、插件协议和兼容不变量专项测试。

生成源变化时，用 `backend/go.mod` 声明的 Go 版本运行 `make generate`，之后要求 `git status --porcelain` 为空。未改变生成源时无需反复生成。

数据库验证至少包括：

- 全新 XY2API 数据库；
- 上一兼容基线的数据库副本；
- migration 文件数与 checksum manifest 完全一致；
- 瞬时数据库错误重试、永久错误快速失败；
- 升级预检对多候选或不明确数据位置返回阻断。

## 5. 同步 PR

1. 推送不可变命名空间标签和同步分支。
2. 创建带 `upstream-sync` 标签的 draft PR，正文包含 `TAG_OBJECT`、`U`、`B`、报告路径、影响域和人工审查项。
3. PR 创建后，把真实 PR URL 写入 `UPSTREAM_BASE.json.sync_pr`，单独提交并推送。
4. 等所有检查结束；重复的 push/PR 工作流按检查上下文归并，不重复重跑。
5. 确认 PR head SHA 未变化、所有 pending 已解决，再转 Ready。
6. 留下审计 review/comment，记录版本、兼容基线、关键裁决和测试证据。
7. 使用 `expected_head_sha` 或等价保护执行 merge commit 合并，不 squash。
8. 拉取 `main`，确认同步 PR merge commit、`U` 均为 `HEAD` 祖先，再执行 audit。

如果仓库只有一名维护者且 GitHub 不允许自批准，保留 PR-only、严格状态检查、管理员执行和审计 comment；增加维护者后把批准数提升为至少 1。

## 6. RC 发布

RC 只能从已经合入 `main` 且 `VERSION` 等于 RC 标签版本的提交创建。

1. 同时确认本地标签、远端标签和 GitHub Release 均不存在。
2. 创建 annotated RC tag，说明产品版本、兼容基线、上游 commit、同步 PR、保留补丁、升级注意和回滚方式。
3. 推送标签，等待 Release 工作流完成。
4. 验证 Release 为 prerelease、资产完整、`checksums.txt` 与下载资产一致。
5. 验证 GHCR manifest 的平台和 digest；DockerHub 只有在凭据存在时才是发布目标。
6. 使用唯一容器/网络/卷/端口做全新启动检查。
7. 在隔离数据上验证从上一正式版升级到 RC，并按 migration 风险验证回滚：
   - 没有 SQL migration 且 schema 向后兼容时，可验证“上一正式版 -> RC -> 上一正式版”镜像回滚。
   - 存在 migration 时，先判断旧程序能否读取新 schema；不兼容时必须验证数据库/Redis 备份恢复或补偿迁移，不能只切回旧镜像。
   - 两种路径都要确认密钥、用户、配置和持久化标识保持。

不要在 RC 通过前创建正式版本提交或标签。

## 7. 正式版晋级

1. 从最新 `main` 创建 `release/<FINAL_VERSION>`。
2. 通常只把 `VERSION` 和 `UPSTREAM_BASE.json.xy2api_version` 从 RC 改为正式版本。
3. 创建 draft release PR，附 RC 的工作流、checksum、镜像、启动和回滚证据。
4. 等完整 CI 与安全扫描通过，转 Ready，留审计记录。
5. 固定 PR head SHA，以 merge commit 合入受保护 `main`。
6. 拉取并审计 `main`，再确认正式标签和 Release 不存在。
7. 在正式 merge commit 上创建 annotated tag 并推送。
8. 验证正式 Release 为 `draft=false`、`prerelease=false`，独立核验资产 SHA、镜像平台/digest、版本 label 和源码 revision。
9. 用正式镜像做一次隔离全新启动；RC 已覆盖升级回滚时，正式版只需验证正式制品与 RC 的预期差异。

## 8. 收尾

- 删除已合并的同步和 release 短期分支，保留命名空间标签、RC 和正式标签。
- 删除临时下载、容器、网络和卷；恢复开始前的 Docker/容器运行状态。
- 确认现有用户部署未被停止、重建或改写。
- 复核 `main == origin/main == 正式标签 commit`、工作树干净、audit 和 `git diff --check` 通过。
- 复核分支保护、所需检查、无开放同步 PR、自动同步工作流仍为一次一个 draft PR。
- 将非阻断警告单列为后续事项，不与本次发布成功混淆。
