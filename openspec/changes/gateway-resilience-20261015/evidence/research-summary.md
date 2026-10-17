# 证据：生态调研摘要（2026-10-15）

> **来源与口径：**
> - 输入：用户提供的两份 CSV（`Sub2API精选项目_2026-10-10` 36 项、`Sub2API检索候选_2026-10-10` 349 项），外加 new-api、LiteLLM、Portkey 三个非 Sub2API 网关，Codex 与 Anthropic TS SDK 的客户端源码，以及 XY-image 仓库。
> - 只读：所有仓库都用部分克隆加 sparse checkout 固定到下表 SHA，没有执行其中任何代码、脚本、测试或依赖安装。
> - 本文只记录思路、结论和文件级出处，不含第三方代码。
> - 带文件与行号、源码摘录的三份完整报告不入库（部分来源没有许可证），保存在仓库同级目录 `xy2api-artifacts/gateway-resilience-20261015/research/`：
>   - `ecosystem-retry-keepalive.md`：重试与换号、流式保活、错误判定、断连计费、错误语义、客户端行为；
>   - `ecosystem-scheduling.md`：账号调度、健康判定、熔断冷却、粘性改绑、余额判定、同源联动；
>   - `xy-image-integration.md`：XY-image 与 xy2api 的集成与兼容。

## 1. 范围与方法

- **两份 CSV 全量读取。** 候选的 349 项中，140 项是没有独有描述的通用 fork，209 项有独有描述或评审结论。
- **精读范围：** 按与本题（重试、调度、判定、保活）的相关度，挑出 34 个第三方仓库固定 SHA 精读；其余项目只按 CSV 中的元数据、描述与评审结论筛查。
- **对比基线：** xy2api `f8fec15a2`（v0.3.0 发行后的 main）。对 Sub2API fork，用与 `Wei-Shaw/sub2api` 的 merge-base 和 diff 找出它真正自研的部分。
- **证据分级：**
  - 源码：逐行读过实现；
  - 文档：只见于 README、设计文档或提交说明；
  - 推断：由间接证据推出，需要实测。

## 2. 固定版本与许可证

| 仓库 | 固定提交 | 许可证 | 类型 | 本方案借鉴的思路 |
|---|---|---|---|---|
| Wei-Shaw/sub2api | main@`698d142468ab`（对比 v0.2.15） | LGPL-3.0 | 上游 | 现有心跳原语、语义输出边界；确认 v0.2.15 之后没有对应修复 |
| ranxi2001/sub2api | production@`b4d097693f00` | LGPL-3.0 | 内嵌 | 断开即取消上游、按已观测用量计费（Claude 路径） |
| DeanZFC/sub2api-custom | `0976d0215247` | LGPL-3.0 | 内嵌，基于 v0.2.15 | 请求级共享重试预算；无号时一次性切兜底组并按兜底组计费 |
| kiss-kedaya/sub2api | `9bbe1e2bf249` | LGPL-3.0 | 内嵌，历史不相连 | 失败时比较后删除绑定、用户级失败回避集、三遍选号；断连后 180 秒总预算 |
| zhu961212/sub2api-oai-basispoints | `7c6a0132d943` | **无许可证** | 传输插件 | 窄键本地短路；请求级错误不触发换号（只借鉴思路） |
| bejix/upstream-ops | `821e613779a7` | **无许可证文件**（README 声称 MIT，未确认） | 独立网关 | 按协议写终止帧；未提交即中止不计费；最后一个候选不设首字超时（只借鉴思路） |
| DR-lin-eng/sub2api-no2api | `186e0b730c36` | LGPL-3.0 | 内嵌 | 唯一把心跳覆盖到“等响应头、换号后再等”的 fork（仅 Responses）；首输出超时 90/180 秒 |
| MACOS-DO/sub4api | `254e7ec4932e` | LGPL-3.0 | 内嵌 | 无相关改进 |
| LuckyKuang/sub2api-plus | `3f55de2f2335` | LGPL-3.0 | 内嵌 | 共享容量降载不重试、不换号；连续断连统计（防滥用） |
| githubliangliang/sub2api | `1e09f156016c` | LGPL-3.0 | 单机版 | Gemini 入口不发注释心跳（genai SDK 不兼容） |
| EdmundMad0309/sub2api-custom | `23cacf75cf44` | LGPL-3.0 | ranxi 下游补丁集 | 403 时换端点（部分），其余同 ranxi |
| KlN-4096/sub2api（klno） | `ca686de5e5ac` | LGPL-3.0 | 内嵌，基于 v0.2.15 | 上游没给 `retry_after` 时本地退避 1 分钟；贝叶斯平滑（P2 参考） |
| tickernelz/sub2api | `61d8dd574476` | LGPL-3.0 | 内嵌 | 可注入时钟的首字与间隔看门狗（修正 G2 的思路） |
| codermyxiaoc/sub2api-guardian | `d82839262f76` | **无许可证** | 旁挂 | 额度类问题不归零、不熔断；“保底强留”只适用于瞬时故障（只借鉴思路） |
| deviseo/transit-hub | `cb3b1bb4b9c6` | LGPL-3.0 | 旁挂 | 暂停 → 观察 → 恢复的渐进状态机，带探测预算 |
| Ember1012/sub2api（Codex2API） | `3990560e0ffa` | **无许可证** | 独立 | 衰减罚分；按次数强制解绑（不采纳） |
| oyg123less/sub2api-desktop | `0978d42f5062` | LGPL-3.0-or-later | 独立 | 403 先判额度再判认证，冷却而非置错 |
| guliansheng/gateway-ops | `256d4079da70` | **AGPL-3.0** | 旁挂 | 无余额接口时用结算游标预测（只借鉴思路） |
| zcj-ui/sub2api-plus | `7301e771b002` | LGPL-3.0 | 内嵌 | 并发满、首字抖动都不改绑；429 两次确认 |
| is7Qin/sub2api（local） | `a36754c5e169` | LGPL-3.0 | 内嵌 | 先归纳错误事实再定策略；模型完全不受支持返回 404 |
| hzh6767/sub2api-account-optimizer | `f201d4da4ea8` | LGPL-3.0 | 旁挂 | 401/403 两次确认；自动写回防抖（P2 参考） |
| Sagit-chu/sub2api_Punishment | `af564205ddcc` | MIT | 旁挂 | 与本题无关 |
| oisano11/relay-tower | `630452b01d3c` | MIT | 旁挂 | 以最新可信证据判断欠费或恢复；`/v1/models` 可达不算恢复证明 |
| little-greenbean/flow-router | `4612f71ae967` | **无许可证** | 独立 | 流式首字探测（只借鉴思路） |
| cita-777/metapi | `88634b0d7207` | MIT | 独立 | 唯一的同源联动实现：分类罚分、半衰、阶梯熔断、全部熔断时放行（D7 不做，留作 P2） |
| noonwake-ai/ai-drool-router | `a86d96a0e494` | LGPL-3.0 | 旁挂 | 失败归因区分上游、预算与本地 |
| daiyibo123/sub2api_cloudflare | `92f9678ea1da` | LGPL-3.0 | 独立 | 主层不健康时切兜底层，而不是取主层里最不坏的账号 |
| langrenjh-alt/S2AM-GO | `78d4aa6198f6` | MIT | 旁挂 | 先看余额语义再看状态码；用 `/v1/usage` 查余额；按“URL + Key 指纹”区分账号 |
| Blue-Seventeen/sub2api | `98f9cb7d8233` | LGPL-3.0 | 内嵌 | 绑定“首写者胜”、按值删除 |
| QuantumNous/new-api | `19d435e7fdfd` | **AGPL-3.0** | 非 Sub2API | 断开即取消；提交后写裸 JSON 是反例（只借鉴思路） |
| BerriAI/litellm | `35970c3eacb9` | MIT（enterprise 目录除外） | 非 Sub2API | 有健康副本立即换号；只采纳 ≤60 秒的 `Retry-After`；正文写“Try again in N seconds” |
| Portkey-AI/gateway | `669825cbe89e` | MIT | 非 Sub2API | `Retry-After` 总预算 60 秒 |
| openai/codex | `e89b7c442c04` | Apache-2.0 | 客户端 | 只用于核对客户端行为（见第 4 节） |
| anthropics/anthropic-sdk-typescript | `034b16806513` | MIT | 客户端 | 只用于核对客户端行为（见第 4 节） |

XY-image（liulixin-lex/XY-image）：基准 v0.0.15 `a900026627d2`，生产 v0.0.16 `258c0c99f8e3`；克隆时 main 为 `6f0d6a7092c6`。

## 3. 按主题的结论与采纳

| 主题（对应 spec） | 生态做法与结论 | 本方案的取舍 |
|---|---|---|
| 失败分类（upstream-failure-classification） | 先看正文语义再看状态码（S2AM-GO、sub2api-desktop）；“错误事实 → 策略”分层（is7Qin）；客户端取消不计入账号失败（upstream-ops）；429 正文含余额字样应走余额路径 | 自研统一分类表（design §2），每个类别决定是否换号、是否同号重试、罚则软硬；上游余额与本站用户余额严格区分 |
| 余额可恢复（account-penalties-and-balance-recovery） | **生态共识：余额不足必须可恢复。** xy2api 已有 CN 供应商的可恢复链路，可泛化。sub2api 系中转的 `/v1/usage` 能免费查余额，但同 host 不一定同钱包。证据要取最新的可信信号，`/v1/models` 可达不算恢复（relay-tower）；恢复要渐进、带探测预算（transit-hub） | D6：正常 / 疑似 / 耗尽 / 恢复中状态机；复检 1→2→5→10→15 分钟，±15% 抖动；探测从低成本到高成本；半开恢复；管理员“已充值”；永不永久置错。非 OpenAI 平台的 403 改为计数加冷却 |
| 同源联动 | 源码里只有 metapi 做了（站点级健康与熔断） | D7：不做，列入 P2 进入条件 |
| 会话改绑（session-failure-handling） | kiss-kedaya：可归因失败时比较后删除绑定，写入用户级回避集，三遍选号；sub2api-plus（zcj-ui）：并发满、首字抖动不改绑；按次数或时长强制解绑会损失提示缓存（Ember，不采纳） | D4、D5：单次瞬时失败保留绑定、临时溢出；10 分钟内同账号失败 ≥2 次或确定性状态才在同层按权重改绑；复用份额调度的 `soft_spill`/`spill_rebind`；层级与权重只由管理员决定；只剩软罚账号时取罚期最短者 |
| 无号兜底（availability-fallback-group） | DeanZFC：仅在调度返回无号时一次性切到同平台兜底组，按兜底组计费；sub2api_cloudflare：主层与兜底层语义；kiss：按 Key 的有序多分组。**更正：xy2api 0.3.0 的 `fallback_group_id` 不是无号兜底** | D8：新增分组字段，每请求只转投一次，同平台、环检测，按兜底组计费并对用户可见；主组连续健康 120 秒后切回。按 Key 的有序多分组不做 |
| 请求预算（request-budget-and-retry） | DeanZFC：请求级共享预算，等待同时监听客户端取消；kiss、LiteLLM：优先换号；Portkey：`Retry-After` 总预算。**没有项目对“首输出前的所有尝试”设总截止时间** | D16、D18：每请求最多 4 次上游尝试；首输出前总预算 240 秒（低于 Codex 的 300 秒）；同号重试仅限请求级降载与池模式配置码，每账号 1 次；Gemini 原地重试 5→1 |
| 首包前保活（pre-output-keepalive） | 上游已有等槽位 ping、compact 心跳、图片前导空白，缺的正是“发上游 → 等响应头 → 换号”这一段；DR-lin-eng 是唯一补上的 fork；new-api 在发上游前发 `: PING`，但提交后写裸 JSON，是反例；bejix 按协议写终止帧；tickernelz 的看门狗可修正 G2 类粒度问题。**没有项目实现“缓冲 reasoning 直到可见文本”** | D3、D12、D15–D17、D21：只对经 CDN 的流式请求，15 秒无响应头才提交并每 10 秒心跳；按协议选择心跳格式；首输出超时 60/120/180 秒；停滞 120 秒、5 秒粒度；提交后按协议写可重试终止帧并立即关流。reasoning 暂存不做（D22） |
| 断开与计费（client-disconnect-and-billing） | 上游与 xy2api 不随客户端取消、无限 drain 并计费；bejix：未提交即中止、不计费；ranxi：取消并按已观测用量计费；kiss：断连后 180 秒总预算；LuckyKuang：连续断连统计防滥用 | D2、D9、D20：零语义字节送达即取消、不计费、另存未送达记录；部分送达后最多再读 180 秒；每 Key 每小时 30 次后按上游成本计费 |
| 错误语义与防风暴（error-semantics-and-storm-protection） | 上游无号的 404/429/503 都不带 `Retry-After`；LiteLLM 把冷却秒数写进正文；zhu 按窄键本地短路；is7Qin 对“模型完全不受支持”返回 404。**没有项目实现“按 API Key 的重复无号快速失败”** | D13、D14、D19：状态码不变，补 `Retry-After` 与 `x-should-retry`；负缓存 2 秒；按 Key 快速失败返回原样应答；上游余额只给管理员；用户余额与在途预留的提示分开 |

## 4. 客户端行为（决定设计边界，源码核对）

- **Codex**（openai/codex@`e89b7c442c04`）：
  - 流空闲 300 秒，只有能解析的 SSE data 事件会重置计时，注释行不算。注释心跳只能防 CDN，所以首输出前的总时长必须小于 300 秒。
  - `response.failed` 要等流结束才上报，所以写完失败事件必须立即关流。
  - HTTP 429 不带 `Retry-After` 就终止回合；带了则按它等待，**没有上限**，所以服务端必须封顶。
  - 5xx 不带 `Retry-After` 时，传输层重试 4 次、回合层 5 次，一个失败回合最多约 30 次请求。
  - SSE 路径只从响应头读取 `x-codex-turn-state`。所以选中官方 OAuth 账号时不提前提交（D21）。
  - `output_item.done` 一到就写入历史，已输出后透明重放不安全。
- **Anthropic TS SDK**（`034b16806513`）：
  - 只把命名事件交给上层：`event: ping` 跳过，`event: error` 抛错；**没有 event 行的 data 帧被静默丢弃**（即 G3 的后果）。
  - 依次认 `x-should-retry`、`retry-after-ms`、`retry-after`。
  - Claude Code 本体闭源，它自己的重试与空闲看门狗没有核对。
- **Gemini go-genai / python-genai：** 遇到 SSE 注释行会出错。依据是 githubliangliang 的兼容代码与测试，没有直接读 Google SDK 源码。所以 Gemini 入口不发心跳、不提前提交。

## 5. 上游 Sub2API 现状

v0.2.15 到 main@`698d142468ab` 共 104 个提交（66 个非 merge）。其中没有任何关于以下内容的提交：

- 首包前心跳；
- 首输出超时；
- 粘性改绑；
- 断连 drain；
- 无号 `Retry-After`。

相关的只有 Gemini/Vertex 的 429/403 分类，以及临时不可调度规则的作用范围。因此本次改进全部以 xy2api 自有补丁实现并登记（D10），不向 Sub2API 官方提交。

## 6. 不采纳的做法

| 做法 | 来源 | 理由 |
|---|---|---|
| 旁挂守护经管理 API 改写账号字段 | guardian、transit-hub、relay-tower、account-optimizer、S2AM-GO、gateway-ops | 有 15 秒到 10 分钟的延迟；与内置调度、份额调度形成多写者冲突；感知不到会话粘性。只吸收算法，内化到网关 |
| 余额或认证失效的账号也“保底强留” | guardian | 没钱的号留在池子里只会消耗用户请求。末位兜底只用于软罚（D5） |
| 5xx 一次就暂停 | transit-hub | 对中转商过激；生产中 502/524 多为短暂故障 |
| 按次数或时长强制解绑会话 | Ember1012 | 直接损失提示缓存 |
| 单主调度加冷备 | relay-tower | 浪费容量；xy2api 已有份额调度保护缓存 |
| 把 `/v1/models` 或凭据探针当作恢复证明 | relay-tower 与 KlN 的反例 | 不能证明余额恢复 |
| 无上限退避 | metapi（Fibonacci 上限 30 天） | 必须封顶 |
| 429 一律按限流处理 | 多数项目 | 正文是余额时应走余额路径 |
| 通用文本非流式的前导空白保活 | 上游图片保活的推广 | 首拍后状态码固定为 200，SDK 基于状态码的重试与 `Retry-After` 全部失效 |
| 提交后写裸 JSON 错误 | new-api | 客户端无法识别 |

## 7. XY-image 调研要点（详见 `../xy-image-appendix.md`）

- **调用方式：**
  - 只有对话走流式，全部是 OpenAI 兼容接口；
  - 生图全部非流式：OpenAI/Grok 走 xy2api 异步任务，Gemini 永远同步；
  - 不使用 `/v1/messages`，也不使用 Gemini 流式接口。
- **心跳本身兼容：** openai-node 跳过注释行。真正的风险是“提交后错误改为流内事件”：XY-image 会把几乎所有流内错误当作“主站繁忙”，丢失 4xx 语义，上下文超长也不再触发压缩。因此确定性检查必须在提交前完成，内网流量不提前提交（D12），XY-image 侧配套 X1、X2。
- **重试风暴：** XY-image 在网关的 429/503 上忽略 `Retry-After`。异步提交被每 Key 在途上限挡回后，每 5–7.5 秒整包重提交一次，而 xy2api 先审计后准入，每次都触发审计。对应 xy2api 的“准入先于审计”与 XY-image 的 X3、X5、X6。
- **计费核对：** 核对器只看有没有用量行，所以“未送达”请求**不能**写 0 元用量行（D9）。
- **同步生图超时：** 实际受 Node 内置 undici 默认 300 秒上限约束，代码中的 600 秒不生效。对应 X4。

## 8. 许可证与借鉴规则

| 许可证 | 项目 | 用法 |
|---|---|---|
| LGPL-3.0（与 xy2api 相同） | transit-hub、KlN、zcj-ui/sub2api-plus、is7Qin、account-optimizer、ai-drool-router、sub2api_cloudflare、DeanZFC、kiss-kedaya、Blue-Seventeen、ranxi、DR-lin-eng、MACOS-DO、LuckyKuang、githubliangliang、EdmundMad0309、tickernelz；sub2api-desktop 为 LGPL-3.0-or-later | 允许移植，但需保留版权与许可声明并登记来源 |
| MIT | metapi、relay-tower、S2AM-GO、Punishment、LiteLLM（enterprise 目录除外）、Portkey | 允许移植，但需保留 MIT 声明 |
| AGPL-3.0 | gateway-ops、new-api | 不得并入 xy2api，只借鉴思路 |
| 无许可证（保留所有权利） | guardian、Ember1012、upstream-ops（bejix）、flow-router、zhu961212 | 只借鉴思路，不得复制代码 |
| Apache-2.0 / MIT（客户端） | openai/codex、anthropic-sdk-typescript | 只用于核对客户端行为 |

**本方案的统一做法：** 全部在 xy2api 的架构中重新实现，不移植任何第三方代码片段，所以不需要新增第三方声明。如果日后确需移植 LGPL 或 MIT 代码，必须在同一 PR 中登记来源与许可。

## 9. 局限与上线前需实测的项

- **new-api 计费接口：** 鉴权方式与返回口径未核验，故障注入上游先模拟接口形状（tasks 0.5），上线前用真实 new-api 系中转实测（tasks 12.6）。
- **Claude Code：** 本体闭源，它自己的重试策略和空闲看门狗没有核对。Claude Code ≥ 2.1.193 的看门狗可能不认 `event: ping`（推断），所以首输出前的总时长按保守值设计，并在真实客户端端到端测试中验证。
- **Gemini SDK：** 不兼容注释行的结论来自第三方兼容代码，不是 Google SDK 源码；真实客户端矩阵中覆盖。
- **只读了文档的部分：** sub2api-plus 的粘性重做、ai-drool-router 的熔断与评分、account-optimizer 的单实例锁，只以 README 或设计文档为准。
- **测试数量：** 都是静态计数，没有运行。
- **候选项目：** 只精读了按相关度挑出的仓库，其余候选按 CSV 元数据与描述筛查，可能遗漏只在代码中体现的相关改进。
