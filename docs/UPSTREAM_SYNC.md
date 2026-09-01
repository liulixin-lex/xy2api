# XY2API 上游同步规范

更新日期：2026-09-01

本文档规定 XY2API 同步 Sub2API 正式版本的唯一标准流程。同步过程允许出现受控冲突，但合入 `main` 时必须没有未解决冲突、未分类二开差异或被破坏的兼容契约。

## 不可破坏的不变量

1. 只同步正式、非 draft、非 prerelease 的 annotated tag，不直接同步移动的 `upstream/main`。
2. 使用真实共同祖先执行 `git merge --no-ff`，禁止 rebase、squash、全量源码覆盖和逐 PR cherry-pick。
3. 官方合并、人工冲突裁决、XY2API 补丁、生成代码、版本发布分别提交。
4. 只对本次上游变更范围内的 `.go` 和 `.proto` 做精确 module path 归一化，禁止全仓品牌字符串替换。
5. 已发布 migration、插件 v1、HTTP、Redis、浏览器和 WebSocket 兼容标识不得随品牌改造重命名。
6. 自动化只创建 draft PR，不自动合并、不自动发布；同一时间最多存在一个同步 PR。

## 固定远端和标签

首次配置：

```bash
git remote add upstream https://github.com/Wei-Shaw/sub2api.git
git remote set-url --push upstream DISABLED
git fetch --no-tags upstream refs/tags/v0.1.185:refs/tags/sub2api/v0.1.185
```

必须保存官方 tag object，不能自行创建只指向相同 commit 的替代标签。首轮同步的固定值为：

- 官方标签：`v0.1.185`
- 官方 tag object：`c8134f0f55b75719ac228b75a0861f2050b4e164`
- 官方目标 commit：`2ac784c51a5d0925b324efef2ba6b3446c364781`
- 共同祖先：`52374af94031f04df8de6fc91deb77a179e04b06`
- XY2API 同步起点：`61fdb85a9dc03ac6df49226d5f9f91cd13a4abb2`

校验命令：

```bash
git cat-file -t sub2api/v0.1.185
git rev-parse sub2api/v0.1.185
git rev-parse 'sub2api/v0.1.185^{commit}'
git merge-base 61fdb85a9dc03ac6df49226d5f9f91cd13a4abb2 'sub2api/v0.1.185^{commit}'
git merge-base --is-ancestor 52374af94031f04df8de6fc91deb77a179e04b06 61fdb85a9dc03ac6df49226d5f9f91cd13a4abb2
git merge-base --is-ancestor 52374af94031f04df8de6fc91deb77a179e04b06 'sub2api/v0.1.185^{commit}'
```

`git cat-file -t` 必须输出 `tag`。有 PGP 或 SSH 签名的标签必须通过 `git verify-tag`；未签名标签在 provenance 中明确记录为 `unsigned`。

## 五类所有权

机器可读策略位于 `tools/upstream-sync/policy.json`。所有相对上游的差异必须落入以下一类：

| 类别 | 含义 | 默认处理 |
| --- | --- | --- |
| `UPSTREAM` | 上游实现，XY2API 没有独立行为 | 接收官方版本 |
| `XY_OWNED` | 产品版本、品牌、发布、安装等二开所有权 | 保留 XY2API，移植必要功能 |
| `COMPAT_INVARIANT` | 已形成外部或持久化契约 | 保持字节或协议兼容并绑定测试 |
| `MANUAL_MERGE` | README、配置、Compose 等双边都合理 | 逐项人工裁决并记录原因 |
| `GENERATED` | Ent、Wire、protobuf 等生成产物 | 只从源定义重新生成 |

`python tools/upstream-sync/sync.py audit` 会拒绝未分类差异、上游 module path 残留、兼容字面量丢失、CLA/广告文件回流和二开补丁清单漂移。

## 标准同步步骤

从已经加固并通过 CI 的 `main` 开始：

```bash
git switch main
git pull --ff-only origin main
git switch -c sync/sub2api-v0.1.185
python tools/upstream-sync/sync.py prepare --tag v0.1.185 --expected-commit 2ac784c51a5d0925b324efef2ba6b3446c364781 --expected-base 52374af94031f04df8de6fc91deb77a179e04b06 --report docs/upstream-sync/v0.1.185.json
```

`prepare` 执行以下受控动作：

1. 校验工作树干净、标签为 annotated tag、目标 commit 和可选共同祖先未变化。
2. 生成三方文件矩阵、提交数量、重叠文件和 migration 影响报告。
3. 执行 `git merge --no-ff --no-commit`。
4. 清单外冲突立即 abort；清单内冲突暂时保留 XY2API 侧并登记为 `pending`。
5. 单独提交官方 merge，再只对上游变更范围执行 module path 归一化。
6. 写入 `UPSTREAM_BASE.json` 和版本化报告。

自动产生的 `pending` 不是已解决。维护者必须逐个比较 base、upstream 和 XY2API，完成裁决后把 `manual_resolutions[].status` 和顶层 `status` 改为 `resolved`，并在 `resolution` 中说明保留和移植了什么。

## v0.1.185 人工裁决矩阵

- `README.md`、`README_CN.md`：保留 XY2API 品牌、仓库、镜像、安装方式和不引入赞助商、CLA、广告的政策，只移植功能说明。
- `backend/internal/config/config.go`：保留 `/etc/xy2api` 优先、`/etc/sub2api` 回退和 XY2API 新装默认值，完整加入 `pricing.override_file`。
- `deploy/config.example.yaml`：保留 XY2API 路径与默认值，加入 override 文件语义和无效文件回退说明。
- `deploy/docker-compose.yml`：保留 XY2API 镜像、服务名、数据库默认值和 `APP_DATA_VOLUME_NAME`，吸收 PostgreSQL `SELECT 1` 探针、启动宽限、重试次数和等待策略。
- 定价、价格快照、BillingService、账户统计成本作为一个原子变更接收，不能混用新解析器和旧快照。
- 数据库瞬时错误重试、Codex 能力与 priority tier、API Key instructions、delegation bootstrap、WebSocket 容量错误与空闲回收以上游实现为基准，再验证二开契约。

提交应保持分层：

```text
merge: sub2api v0.1.185
sync: normalize xy2api module paths for sub2api v0.1.185
sync: resolve v0.1.185 manual conflicts
fix: preserve xy2api compatibility patches after v0.1.185
generate: refresh wire output for v0.1.185
docs: finalize sub2api v0.1.185 provenance
chore: prepare xy2api 0.0.2-rc.1
```

## Migration 不可变性

`backend/migrations/checksums.json` 固定全部已发布 migration，算法必须与运行器一致：`sha256(strings.TrimSpace(utf8))`。

- 既有 SQL 和既有 hash 禁止修改。
- 新 migration 只能按编号追加 SQL 和 manifest 条目。
- 发现既有 migration 改写、manifest 缺项或未知旧条目时立即阻断。
- 回滚不回写 migration，不修改 `schema_migrations` checksum。

## 版本模型

- `backend/cmd/server/VERSION`：XY2API 产品版本。
- `backend/cmd/server/SUB2API_COMPAT_VERSION`：完成审计的 Sub2API 兼容基线。
- 插件 manifest 的 `requires.sub2api`、`recommended_sub2api_version`、`tested_sub2api_versions` JSON 契约保持不变，只与兼容基线比较。
- API 保留 `current_sub2api_version`，新增 `current_xy2api_version`。

发布工作流不会改写版本文件。RC 提交使用 `VERSION=0.0.2-rc.1` 并创建同名标签；staging 通过后，再提交 `VERSION=0.0.2` 并创建正式标签。标签和版本文件不一致时发布立即失败。

## 自动同步 PR

`.github/workflows/upstream-sync.yml` 每日 `03:30 UTC` 检查正式 release：

1. 有未完成的 `upstream-sync` PR 时不改变目标，新标签保持排队。
2. 从 GitHub API 固定 tag object，再以命名空间标签抓取并二次比对。
3. 生成同步分支、provenance 和报告，创建 draft PR。
4. 清单外冲突、标签对象变化、签名验证失败或脚本异常会阻止 PR。
5. 工作流从不自动合并或打发布标签。

## 合入门禁

基础门禁：

```bash
python -m unittest discover -s tools/tests -p 'test_*.py' -v
python tools/upstream-sync/sync.py audit
git diff --check
docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet
```

后端使用仓库固定的 Go 1.27 工具链执行 `make generate`、生成后零差异检查、unit、integration、race-sensitive 测试、`go build ./cmd/server` 和 golangci-lint。前端执行 frozen install、lint、typecheck、完整 Vitest 和生产构建。

还必须完成定价、Codex、WebSocket、数据库重试、全新数据库、Sub2API 0.1.184 数据库副本、插件协议、旧部署升级和镜像回滚专项测试。

## GitHub 分支保护

仓库设置中对 `main` 启用：

- Require a pull request before merging，至少 1 名审批者。
- Require status checks：`upstream-sync-audit`、后端 unit/integration、前端、golangci-lint、部署脚本和安全扫描。
- Require conversation resolution；关闭 linear history 要求以允许 merge commit；禁止 force push 和 branch deletion。
- 管理员也必须遵守规则；发布只能从已合入 `main` 的 tag 触发。

这些仓库级设置不能由源码文件代替，必须在 GitHub Ruleset 或 Branch protection 中实际启用。

## 升级预检与回滚

首次切换现有 Sub2API 环境前运行：

```bash
python tools/xy2api_upgrade_preflight.py --env-file deploy/.env --compose-file deploy/docker-compose.yml
```

出现 error 或多个数据候选时停止升级，由维护者确认数据库名、物理卷或 bind mount、systemd 目录、Redis DB 与前缀和密钥。工具只检查，不搬移数据。

`v0.1.185` 没有新增 SQL migration。回滚只恢复上一版 XY2API 镜像或二进制和流量，不改数据库名、Redis 标识、migration checksum 或密钥。
