# 实施与验收

集成分支 `feat/encrypted-routing-20261105`（基于 `381687250`），一个 PR、一次发版。开发分支由集成方普通 merge 合入，保留源提交。逐项裁决、审查报告、门禁记录和预检证据都在本地交接包，不入库。

约束：
- 推送、PR、合并、发版、部署、镜像固化与生产操作须经用户授权；
- 不做影子模式和测试矩阵，每个判定一组定向回归；不新增配置开关、迁移或 Redis；
- 改到 `tools/upstream-sync/policy.json` 的 literal 所守护的代码行时，在同一提交同步 literal，提交后在干净工作树上跑 `sync.py audit`；
- 每路交付前运行相关包测试、golangci-lint 与 `python3 tools/check_docs_consistency.py`。

## 1 流式判定与包装帧

- [x] 1.1 空白占位判定：原生、直通、两种首字口径共用 `ResponsesEventOutputDisposition`；暂存、冲刷与丢弃；失败的 incomplete 丢弃暂存，`interrupted` 按成功处理；尚无语义输出且无 output items、无已累计用量、usage/error 缺失或 null 的 completed/done 仍由静默拒绝守卫换号；合法空白回答不记首输出延迟样本。
- [x] 1.2 白名单按实测形状放行 `logprobs`、`obfuscation`、`phase`；参与判定的对象拒绝重复键并校验字段类型，歧义按可见处理。
- [x] 1.3 Chat/Messages 兼容转换在首次提交前有界暂存（8 MiB），提交后照常转换空白。
- [x] 1.4 E1/E2 严格校验，原始 SSE event 与 payload 终态类型分开保存；畸形帧按协议失败处理并计数；解包体用于后续分类，仅包装 400 为 M3 保留原始外层体。
- [x] 1.5 包装 401/403：四类请求级拒绝统一终止映射（HTTP 400 / 流内 failed，code 按拒绝语义）；其余走 forbidden；输出后不进入凭证或 403 计数处罚。
- [x] 1.6 输出前心跳按客户端选择（Codex 用 ping 事件，其他客户端用注释）；直通读泵固定周期派发，去掉相邻 tick 的二次节流。

## 2 状态机与 forbidden

- [x] 2.1 输出前流内 forbidden：专用 Reason；OpenAI API Key 在既有失败处理启用且有结构化证据时记账号×模型软冷却，只有 OpenAI API Key 排除同站；用尽默认写 502 / `server_error`，并按既有开关与会话条件接入通用耗尽提示和 turn cap。
- [x] 2.2 M3：槽位身份、目标路径替换、严格增长判据、S4 立即学习、批量学习以上游接受为条件；Bad 的组/会话/账号隔离、常量 kind、256 字节引用上限。
- [x] 2.3 M3 状态换号额度（上限 1）、属主校验删绑与会话回避、末位守卫豁免、`AttemptProfile` 重置；DeepSeek/Kimi 归一化账号不启用 M3。
- [x] 2.4 M3 终止：未提交写 502，已提交写 `server_error`，使用固定消息模板；改写后的兜底只对非请求级 400/422 生效；直通流内字段修复仅在 M3 已启用，且本帧命中 M3 信号或当前账号已有 M3 替换槽位时启用。
- [x] 2.5 `resilience.*` 计数与 `last_resort_avoided`；文档 `docs/GATEWAY_RESILIENCE.md`；policy literal 同步。

## 3 集成与门禁

- [x] 3.1 原生、直通各一条真实 E2 包装 400 → M3 同号重放的 Forward 衔接回归，核对实际出站体。
- [x] 3.2 两路红队（协议与客户端、路由与韧性）逐轮签字。
- [x] 3.3 最终集成头全量门禁：gofmt、vet、golangci-lint、后端全量 unit、既有 race 门禁及 M3 定向 `-race`、integration、干净树 audit。代码最终头 `777597cd2` 13 项全过；其后的 handler 测试补强已复跑 handler 全量 unit 与 lint。
- [x] 3.4 Codex 0.166 冒烟：暂存后原样放出，与直通无可见差异；ping 期间不超时；同一连接内修复成功时只感到延迟。PR 头 `e596bc670` 隔离网关实测：三个测试上游普通提示均通过，观察到空白暂存，首输出前 34–51 秒仅靠 ping 维持且 12 秒空闲超时未触发；M3 未能在真实上游触发（测试上游对构造的 agent_message 返回通用 400），由单测与 Forward 衔接回归覆盖。

## 4 发布（须用户授权）

- [ ] 4.1 发布前核实当前运行版本，固化可回退镜像并核对重建后的版本一致性。
- [ ] 4.2 推送、PR、合并、发版、部署。
- [ ] 4.3 上线后按与基线相同的统计口径验收：Codex 可见失败总数，以及输出前最终失败按原因拆分（forbidden、5xx、首字超时、预算耗尽、M3 terminal）。同时盯占位站的换号次数、首字超时次数、慢退避、额外上游调用和延迟迁移量。
