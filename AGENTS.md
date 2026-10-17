# XY2API Agent 协作约定

本文件作用于整个仓库。所有 Agent 在处理仓库任务时，都必须遵守以下约定。

## 仓库记忆

1. 开始任何仓库分析、修改、测试或 Git/GitHub 操作前，先完整阅读 [`docs/PROJECT_MEMORY.md`](docs/PROJECT_MEMORY.md)。已结束任务的历史在 [`docs/PROJECT_MEMORY_ARCHIVE.md`](docs/PROJECT_MEMORY_ARCHIVE.md)，需要追溯时再查。
2. 读取记忆后，仍需检查当前分支、`HEAD` 和工作树；记忆是交接入口，不替代代码、Git 与远端事实。
3. 首次修改仓库前，在记忆文件的“进行中的工作”中登记任务。长任务只在关键发现、决策、验证结果或阻塞变化时更新，不逐条记录无意义命令。
4. 结束任务前，更新“当前交接状态”，清理或更新自己的进行中条目，并在“操作日志”追加一条完整记录。未完成的任务必须留下已完成内容、未完成内容、卡点和明确下一步。
5. 操作日志只追加，不改写历史事实；发现旧记录错误时追加更正说明。不得删除、覆盖其他 Agent 的进行中事项或未提交改动。
6. 多 Agent 共用工作区时，修改记忆文件前重新读取最新内容，只维护自己的任务状态，并保留其他 Agent 的更新。
7. 不在记忆文件中写入密钥、Token、密码、个人敏感信息、完整生产数据或大段原始日志；只保留可复现命令、摘要和必要标识。
8. 记忆主文件只放进行中与近期的内容。任务结束后，把它在“当前交接状态”和“进行中的工作”中的详细记录原样移到归档文件，主文件只留几行结果摘要；较早的操作日志按同样方式移走。主文件超过上限时，文档一致性检查会报错。

## 事实优先级

发生不一致时，按以下顺序重新核实并修正记忆：

1. 当前代码、配置、测试与 Git/远端实际状态；
2. `UPSTREAM_BASE.json`、`tools/upstream-sync/policy.json` 和对应版本化同步报告；
3. `docs/UPSTREAM_SYNC.md`、仓库内 `xy2api-upstream-sync` Skill 与工作流；
4. `docs/PROJECT_MEMORY.md` 的交接摘要；
5. 其他通用或历史文档。

## 工作原则

- 默认使用中文沟通，方案保持精准、清晰、可维护，不引入无关复杂度。
- 修改前先理解相关模块及现有测试；验证范围与实际改动和风险匹配。
- 工作树中的既有改动属于用户或其他 Agent，未经明确授权不得覆盖、回退或顺手整理。
- 上游同步必须使用仓库既有同步机制。禁止按 `upstream/main` 直接 merge/rebase、全量覆盖源码、仓库级品牌替换或绕过 provenance、兼容性与 migration 门禁。
- 推送、PR/Issue/标签/Release 变更、合并、部署和生产数据操作必须处于用户明确授权范围内。

## 变更同步

改代码时，在同一个提交或 PR 里同步相关文档与登记，不留到以后补：

| 改动 | 同步更新 |
|---|---|
| 行为、默认值、开关、设置字段 | 对应的现行文档（见 [`docs/README.md`](docs/README.md)）、进行中的 OpenSpec design/spec/tasks、前端文案与设置界面、部署 runbook |
| 指标、日志事件、管理接口 | 现行文档的观测与接口章节 |
| 删除或改名功能、标识符 | 全仓库搜索旧名（代码、测试、前端与 i18n、文档、工具、测试环境脚本）；把旧名登记到 `tools/docs_consistency.json` 的 `retired`，只在历史记录里允许保留 |
| 数据库迁移 | `backend/migrations/checksums.json`（只追加）、现行文档的迁移与回退说明 |
| 上游文件钩子、原版调度文件 | `tools/upstream-sync/policy.json`（required_literals 或具名补丁）、`scheduler-diff.json` |
| Go、golangci-lint 等版本 | `tools/docs_consistency.json` 的 `pins` 列出的全部位置 |
| 新增、退役、改名文档或 OpenSpec 变更 | `docs/README.md` 的目录与状态、`.gitignore` 的 docs 白名单 |
| 部署前提（反向代理、环境变量） | `deploy/` 下的部署文档与配置示例 |

提交前运行 `python3 tools/check_docs_consistency.py`（CI 的 upstream-sync-audit 作业也会运行）。它检查相对链接、文档目录与白名单、版本号、代码定义的名字是否写进文档、退役名字是否残留、记忆主文件的大小与结构。报出的问题按提示修复，不靠放宽规则绕过；规则确需调整时，与对应代码放在同一提交并写明原因。
