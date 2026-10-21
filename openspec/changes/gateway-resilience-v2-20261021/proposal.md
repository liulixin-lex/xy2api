# 网关韧性第二期（0.4.4）：少中断、保首字

## 背景

网关韧性随 0.4.0 上线（[`gateway-resilience-20261015`](../gateway-resilience-20261015/proposal.md)），0.4.1–0.4.3 陆续修补。2026-10-20 对 0.4.3 的生产运行做了约 9 小时的只读取证（28,164 个可观察请求，证据在仓库外）。上游相关失败 341 个请求（38 名用户），网关无号或配置失败 58 个（4 名用户），合计约 1.42%。按“网关能否改进”拆开：

| 失败模式 | 规模 | 网关侧原因 |
|---|---|---|
| 原生 Responses 带内终止（主要是 Codex） | 150 个请求，27 名用户；容量 81、瞬时 58、限流 11、断流 2；全部发生在网关释放暂存之后 | 网关只把 `response.created`、`response.in_progress`、`keepalive` 当作开场事件；`response.queued`、`codex.response.metadata` 等事件一到就算“已开始输出”，之后不能再换号。其中有多少是客户端还没看到正文的“伪已输出”，要靠本期的观测字段统计 |
| 兼容 Chat 开场后终止 | 114 个请求，4 名用户；容量 61、限流 39、瞬时 14 | Chat 与 Messages 经兼容转换时，一收到上游 `response.created` 就写出 role chunk 或 `message_start`，之后不再换号。限流部分主要来自一个中转账号的 “The service is busy” |
| 供给不足的分组 | 一个分组的两个主力账号同时失败，容量失败全部未恢复 | 代码救不回，属于运维 |
| 仅流式上游 | 32 个请求，1 名用户，持续 2 小时 22 分 | 非流式请求被派给只接受流式的账号，上游回 400，网关当作客户端错误返回 |
| Claude 首事件截止过长 | typed 首输出超时 36 次，其中 31 次是 Claude 模型撞上 90 秒截止；超时换号后首字只要 1–11 秒 | `budget.anthropic_first_event_timeout_seconds=90`，Claude 成功请求的首字 P99 只有 3.9–17.2 秒。属于配置，按用户决定作为第 0 批在生产评估，不在本仓库。2026-10-21 按 7 天分布评估：流式 Messages 成功请求中首字超过 45 秒的约 0.45%、超过 60 秒的约 0.36%，且首字只是首事件时延的上界，不足以证明下调安全，**保持 90 秒，生产未改** |
| 协议与信号缺陷 | 无法从库里量化 | 排队 ping 之后写裸 JSON；三个专用耗尽出口没走公共重试提示与回合封顶；合成的 `response.failed` 不带重试时间；Grok 空闲与 raw 429 被当成容量降载；raw Chat 的 `error` 后跟 `[DONE]` 被当成成功；Anthropic 协议下网关自身的 503 被标成 `overloaded_error` |
| 观测缺口 | 284 个已输出后失败事件没有请求 ID，也没有释放暂存的事件类型、首个输出时间和字节数 | 无法判定“伪已输出”，也无法给后续优化定参数 |

2026-10-21 的生产探测（几个主力中转各 2 个最小 Codex 形态请求，只记事件类型）：多数中转在第一个 `response.output_text.delta` 之前依次发 `response.created`、`response.in_progress`、空的 `response.output_item.added`（message）与空的 `response.content_part.added`（output_text）；部分中转在 `response.created` 之前先发一个 delta；`response.metadata` 出现在正文之后；没有观察到 `codex.response.metadata`。白名单据此确定，不按事件名整体放行元数据类事件（design §3.1）。

完整方案（v2.1）与两份审查在仓库外 `xy2api-artifacts/resilience-ux-20261020/`；本变更是它按用户决定裁剪出的 0.4.4 部分。

## 用户决定（2026-10-21）

- 实现本变更后合并所有开放 PR（#116 弹窗首开修复、#117 份额调度精简与本变更），发布 0.4.4。生产部署 0.4.4 不在授权范围内。
- 第 0 批运维只做 Claude 首事件截止的调整（生产配置，不改代码）。评估结论为保持 90 秒，未写入生产（见背景）。
- 授权一次生产探测：对几个主力中转各发 1–2 个最小 Codex 形态请求，只记录首个 delta 之前的事件类型，作为原生白名单的依据。
- 推翻既有决定的事项（方案 U7–U12：余额暂停逐级加长、放宽 D18、原版调度健康偏好等）本期先不考虑。
- 工期 12 小时；超时砍范围，不砍验证。

## 范围

| 代号 | 内容 |
|---|---|
| C0 | 新设置开关的契约与实现：命名、存放、回退兼容 |
| W1.1 | 统一“可重放前缀”判据：原生 Responses 的开场白名单扩展（`response.queued` 与空的 added 结构；中转伪保活泛化为 `response.created` 之前的空 delta）；Chat、Messages 兼容转换推迟生成开场事件（role chunk、`message_start`），直到首个内容或终态；暂存期内可重试的失败转为换号 |
| W1.2-lite | raw Chat 透传识别 `error` 后跟 `[DONE]`：提交前换号，提交后按失败记账 |
| W2.1-lite | 分类修正：只有真正的容量信号（`server_is_overloaded`、`slow_down`、529、结构化 overloaded）算 `CAPACITY_SHED`；Grok 空闲改为瞬时或停滞；raw 429 改为限流（不覆盖余额、reset 等更强证据）；“The service is busy”、“Too many pending requests”、“Concurrency limit exceeded” 改为限流或瞬时；typed 首输出超时单独可辨 |
| W5.1 | 三个专用耗尽出口（Gateway Responses、Gateway Chat、OpenAI→Messages）接入公共重试提示与回合封顶 |
| W5.2 | 排队 ping 之后的计费或终态错误，按入站协议写终止帧 |
| W5.3 | 合成的 `response.failed` 有条件地带 `error.headers.retry-after` |
| W5.4 | Anthropic 协议下，网关自身的无号与瞬时失败统一用 `api_error` |
| W5.5 | Codex 请求的无号 503，`Retry-After` 上限降到 10 秒 |
| W6-lite | 仅流式能力学习：确定性的 “only accepts streaming requests” 类 400，标记该账号“非流式 + 当前协议”不可用，本请求立即换号 |
| W8-lite | 不加表、不加迁移；给五类韧性事件补请求 ID、模型、已送达字节、首个输出与释放暂存的时间 |
| FE | 新开关的设置界面与 i18n；账号仅流式学习状态的显示与清除；Explain 新原因 |
| QA | 新故障与测试服务器场景；候选验证：全套场景、升级演练、3 小时浸泡 |

## 不做

- 容量多主机换号（方案 W2.2；D18 维持原状）、健康感知调度（W3）、已输出后失败与缓存友好换号（W4）、OpenAI 侧响应头截止与自适应首输出截止（W7）。
- W8 的两张新表与迁移、观测面板。
- W1.2 的其余路径：OpenAI Responses 透传的首输出守卫（如未完成）、Gateway 入站经 Anthropic 上游的两个反向转换器、Gemini、Bedrock、CN、Antigravity T2。
- 自动把非流式请求改发流式再聚合（force_stream）；手工能力设置（只留字段位）。
- Sub2API v0.2.17 同步。

## 修订的设计层约束

本期修订 0.4.0 设计中的三条约束，原文保留在旧变更中，以本变更为准：

| 原约束 | 位置 | 修订 |
|---|---|---|
| 送达口径“只改计数，不压住、不推迟开场事件” | [design §8.1](../gateway-resilience-20261015/design.md)（0.4.2，审查 R03） | 送达口径不变；但兼容转换在 `stream.prelude_staging` 开启时推迟生成开场事件，直到首个内容或终态（W1.1） |
| 兼容转换“第一行之后立即写出开场事件，此后不再换号” | [design §7.4](../gateway-resilience-20261015/design.md)（0.4.2，全局复审 G1） | 开场事件推迟后，首事件守卫延长到首个内容；暂存期内可重试的失败转为换号（W1.1） |
| 失败帧正文总带 “Please try again in Ns.”（N = clamp(冷却剩余, 2, 30)） | [design §7.6](../gateway-resilience-20261015/design.md) | 由 `RetryAdvice` 决定：有健康备选时不带或只带 1 秒；无健康候选且能算出恢复时间时带 2–10 秒，`error.headers.retry-after` 与正文一致；回合封顶、余额终态、内容拒绝不带（W5.3） |

## 延续的用户决定

| 决定 | 本期 |
|---|---|
| D6 修订（2026-10-17）：余额暂停固定时长、不递增、无状态机与半开 | 不变 |
| D18：同号重试只用于容量降载；容量只换一次且必须跨主机 | 不变。W2.1-lite 把不是容量的信号移出 `CAPACITY_SHED`，使它们不再受这条限制，但容量本身的规则不动 |
| Q16（2026-10-15）：网关错误的 HTTP 状态码维持不变 | 不变。W5.2 只修排队 ping 之后的协议格式，W5.4 只改 Anthropic 错误体的 `type` |
| tasks §15：API Key 账号的通用健康熔断与降权等，等 0.4.0 上线满 7 天且指标达标后再立项 | 不变，本期不做 |
| 不改余额提示文案（2026-10-17） | 不变 |

## 影响

| 方面 | 影响 |
|---|---|
| 设置 | 新增开关见 design §2；缺省全部开启；总开关关闭时按 0.3.0 运行，子开关关闭回到 0.4.3 / #117 的行为 |
| 迁移 | 无 |
| 管理接口 | 账号的仅流式学习状态与清除接口（design §6） |
| 客户端可见 | 兼容流的开场事件可能更晚到达（与原生 Responses 一致）；暂存期内的失败改为换号；`response.failed` 的重试时间；Anthropic 错误 `type`；Codex 无号 503 的 `Retry-After` 上限 |
| 上游文件 | 钩子登记在 `tools/upstream-sync/policy.json`，调度文件差异登记在 `scheduler-diff.json`（集成时由 α 汇总） |
