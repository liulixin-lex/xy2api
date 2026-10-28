# 任务：账号页「上游余额」列

> 一个 PR（`feat/upstream-balance-20261027`），开发时基于 main `d25bcd8dc`，开 PR 前 rebase 到最新 main。
> - 提交分层：记忆归档与登记 → 后端 → 前端 → 文档、OpenSpec 与同步登记。
> - 分工：设计与集成为 Claude（Opus 5.5）；后端开发为 Codex（gpt-6.1-sol）；前端开发为 Claude（Opus 5.5）；审查为 Codex（gpt-6-astra）。
> - 不改 `VERSION`，不发版，不部署。

## 1. 契约与登记

- [x] 1.1 契约定稿：
  - 前后端契约各经一轮对抗式评审，修正写入契约 Part A；
  - astra 审设计两轮：第一轮 3 项 P1、6 项 P2，第二轮 1 项 P1，修正写入 Part D；
  - 传输层既有的 Debug 日志改列为后续事项。
- [x] 1.2 项目记忆：按归档规范原样归档已结束的记录；登记 `20261027-upstream-balance`。
- [x] 1.3 OpenSpec（本目录）与 `docs/UPSTREAM_BALANCE.md`，登记到 `docs/README.md` 与 `.gitignore`。
- [x] 1.4 同步登记：`policy.json` 的具名补丁 `xy-upstream-balance`、required_literals、custom_patch_tests，`xy_owned` 与 `manual_merge` 两项分类，以及 `UPSTREAM_BASE.json` 的 `custom_patches`。

## 2. 后端

- [x] 2.1 B1：采集服务、两个适配器、识别、调度、Redis 存储、轮锁、站点锁与请求预算。
- [x] 2.2 B2：读接口、路由、wire 注入与生命周期，重新生成 `wire_gen.go`。
- [x] 2.3 B3：敏感键；站点或代理变化时删除令牌（更新、批量更新、复制三条路径）；`newapi_user_id` 校验。
- [x] 2.4 B4：CN 余额检查与渠道监控跳过中转的 kimi、deepseek 账号；更新既有测试夹具。
- [x] 2.5 B5：单元测试。覆盖地址拼接、两类上游的解析、401 判别、限流与退避、轮锁、指纹变化，以及副作用：读接口零上游请求、零 accounts 写入、零 outbox。

## 3. 前端

- [x] 3.1 F1：接口与类型、`UpstreamBalanceCell.vue`、列定义、轮询与时钟校正。
- [x] 3.2 F2：`NewApiWalletSection.vue` 与创建、编辑弹窗。
- [x] 3.3 F3：隐藏 CN 子单元格，补 zh/en 文案与规格测试，并把规格测试加入 `FRONTEND_CRITICAL_VITEST`。

## 4. 审查与验证

- [x] 4.1 astra 审 diff，并对照开发文档 §8 的 PR-B 清单；修到没有 P0、P1 为止。
  - 三轮 astra 加 Claude 对抗式核实；首轮确认的 P1 与后续 P2 均已修复，第二轮起无 P0、P1。
  - 两项 P2 列为后续：传输层既有 Debug 日志；前端无法察觉其他管理员只轮换 Key 或令牌（后端指纹在下一次成功轮询时隐藏旧值）。
- [x] 4.2 本机门禁：
  - 后端：`go build`、`make generate` 后无差异、四种 tag 的 vet、gofmt、unit、定向 `-race`、golangci-lint、`ci_gate.py`；
  - 前端：`make test-frontend`；
  - 文档一致性、`sync.py audit`。
- [x] 4.3 本机联调：本地 PostgreSQL 与 Redis，以及 sub2api、new-api 两个假上游；浏览器检查余额列、变灰、令牌小节与 CN 子单元格。截图留在仓库外。
- [ ] 4.4 推送并开 PR，等 CI 全绿。合并、发版、部署另行授权。
