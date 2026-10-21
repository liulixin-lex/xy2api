# 渠道监控 v1：探测口径与可靠性

本文说明渠道监控 v1（主动探测）的判定口径、探测请求、调度与读路径。v2（按真实流量统计的被动监控）见 [`channel-monitor-v2-safe-defaults.md`](channel-monitor-v2-safe-defaults.md)；两者由设置 `channel_monitor_mode` 二选一。

## 1. 判定口径

每个监控按间隔对每个模型发一次探测，一轮的结果是三种颜色之一：

| 颜色 | 条件 | 历史中的状态 |
|---|---|---|
| 绿 | 30 秒内拿到正确回答，且响应时间不超过 10 秒 | `operational` |
| 黄 | 拿到正确回答，但响应时间超过 10 秒 | `degraded` |
| 红 | 30 秒内拿不到正确回答：超时、网络错误、非 2xx、流内错误、空回复或答错 | `error` / `failed` |

- 响应时间是从发出请求到收到完整答案的时间，也就是用户实际等到回答的时间。
- 30 秒是一轮的总预算，包含网关里的排队、换号与至多一次重试。
- 历史表结构与状态值没有变化，7/15/30 天可用率仍把 `operational` 与 `degraded` 计为可用。
- 0.4.4 及以前的口径是“非流式请求的完整耗时 ≥ 6 秒为黄、30 秒等不到响应头为红”。推理模型正常就要 3–15 秒，生产里 Grok 监控约 16% 的探测因此显示为黄色。

## 2. 探测请求

### 2.1 流式请求，读到答案即停

所有 provider 都发流式请求（`Accept: text/event-stream`）：OpenAI Chat 与 Responses、Grok、Anthropic Messages、Gemini（`/v1beta/models/{model}:streamGenerateContent?alt=sse`），以及走 OpenAI 兼容 Chat 的 Kimi、智谱、DeepSeek、MiniMax。

- 边读边拼接模型正文，正文里出现期望答案、并且答案已经完整（后面跟着非数字字符或流结束）时立即判定并断开，不等模型把解释说完。
- 不算回答的内容：SSE 注释行与心跳、`event: ping`、只有 role 的增量、usage、生命周期事件、空增量。推理增量（Responses 的 reasoning、Chat 的 `reasoning_content`/`reasoning`、Anthropic 的 thinking）只算“有输出”，不参与答案校验；正文里的 `<think>…</think>` 先剥离。
- 流内错误照常判红：Responses 的 `response.failed`、Chat 的 `data: {"error":…}`（其后的 `[DONE]` 不是成功）或 `event: error`、Anthropic 的 `event: error`、Gemini 的错误对象。没有任何正文就结束判为空回复。
- 读法按响应的 Content-Type 选择：上游忽略 `stream: true`、直接返回 JSON 时也能判定。
- 专用 HTTP 客户端：拨号 10 秒；不设整体超时，由一轮 30 秒的预算控制；空闲连接保持 10 分钟、HTTP/2 空闲 30 秒发 PING，2–5 分钟间隔的探测可以复用连接，省去每次的 TLS 握手。仍校验目标不是内网地址。
- 对 endpoint 源站的 HEAD ping 与模型请求并行，不再先串行等它；它只测到入口（经 CDN 时是 CDN 边缘），不影响判定。

### 2.2 请求形状

题目是一句零样本加法：`Reply with only the number. a + b = ?`，a、b 在 1–20 之间，每次随机。Responses 的 `instructions`、Anthropic 的 `system` 只有一句 “Reply with only the number.”。

每种协议只发最小请求，不带 `temperature`、`top_p`、`stream_options`：

| 协议 | 字段 |
|---|---|
| OpenAI Chat（含 Grok、国产四家） | `model`、`messages`、`max_tokens`、`stream` |
| OpenAI Responses（含 Grok） | `model`、`instructions`、`input`、`max_output_tokens`、`stream` |
| Anthropic Messages | `model`、`system`、`messages`、`max_tokens`、`stream` |
| Gemini | `contents`、`generationConfig.maxOutputTokens` |

推理强度与输出上限按模型族给（推理 token 计在输出上限里，上限太小会“推理吃光预算、正文为空”；读到答案即停，上限给大不会变慢）：

| 模型族 | 推理参数 | 输出上限 |
|---|---|---|
| OpenAI GPT-5 及以上、o 系列、codex | Responses `reasoning.effort: "low"`；Chat `reasoning_effort: "low"` | 1024 |
| Grok 4 及以上、grok-3-mini、grok-code | `reasoning_effort: "low"` | 1024 |
| Gemini 2.5 及以上的 flash | `thinkingConfig.thinkingBudget: 0` | 1024 |
| Gemini 2.5 及以上的 pro | 不加 | 1024 |
| Anthropic | 不加（缺省不开 thinking） | 16 |
| 国产四家、名字像推理模型的（think、reason、r1、qwq、glm-4.5 及以上等） | 不加 | 1024 |
| 其他非推理模型 | 不加 | 16 |

- 名字里写着 non-reasoning、non-thinking、no-think 的模型按非推理处理。
- merge 模式在最小请求上浅合并用户字段，用户写了推理参数或输出上限时以用户为准；`model`、消息与 `stream` 等关键字段不可覆盖（Anthropic、Gemini 也加入了 `stream`）。
- replace 模式原样发送用户 body，不注入任何参数，按其中的 `stream` 决定流式还是非流式读法，读到正文完整为止。

### 2.3 参数自适应

上游以 400 或 422 拒绝我们加的可选参数时，同一轮内修正后重发，不判红：

- 错误文本提到推理参数，或没有点名地拒绝了某个参数 → 去掉注入的推理参数；
- Chat 要求用 `max_completion_tokens` 代替 `max_tokens` → 改名；
- 拒绝流式 → 改为非流式。

修正按 provider、endpoint、API 模式与模型记在进程内，之后每轮直接用修正后的形状；进程重启后最多再学一次。用户在 merge 中自己写的推理参数与 replace 模式不修正。

### 2.4 重试

只对快速失败的连接级错误立即重试一次：连接被重置、EOF、CDN 的 5xx 错误页、5 秒内返回的 502/503。超时、答错不重试；剩余预算不足 10 秒不重试。每个模型每轮最多发 2 次请求，与参数修正共用。响应时间从这一轮第一次发请求算起，包含修正与重试；第二次也失败时，message 以 `attempt 2/2:` 开头。

### 2.5 费用

探测照常经过网关计费。读到答案即断开时，经本站网关的请求按已输出的部分计费，网关会继续读完上游以结算；推理模型的输出上限从 50 提到 1024，话多的模型单次费用可能比以前高。

## 3. 探测请求在网关里的选号

监控发出的每个探测请求都带 `X-XY2API-Probe` 头。它用进程内随机密钥签名，含监控 ID 与时间，60 秒内有效，外部无法伪造。全局中间件校验后在请求上下文里打上探测标记，并且总是删除这个头，它不会被转发到上游。打到其他实例（蓝绿重叠期间）的探测校验不通过，按普通请求处理。

带探测标记的请求在选号时：

- **没有粘性会话**：不读也不写会话绑定（探测每次题目不同，本就没有需要保持的会话），所以不会被钉在某个已绑定的账号上等它空出来。
- **有空闲槽位时不排队**：选号在决定“等待某个账号”之前，先按真实并发上限把同一批候选再试一遍，拿到空闲槽就直接用。正常选号按负载率排序，负载率用负载因子计算，负载因子小于并发数的账号在仍有空闲槽时也会被当作已满；探测的这一遍不看负载率。只有整组确实全满时探测才排队，排队时间计入 30 秒预算。

覆盖的路径：OpenAI 与 Grok 的缺省调度（兜底等待前）、Anthropic 的兜底等待与模型路由（探测不在路由账号上等 120 秒，而是到整组找空槽）、份额调度（兜底等待前去掉负载门再试一次计划中的各步，上限仍按原有的 Codex 自适应上限）。OpenAI 高级调度器（缺省关闭）只覆盖了“没有粘性会话”这一条。

真实用户请求的选号行为不变。

## 4. 调度可靠性

- 启动：首轮在 0 到 min(间隔, 60 秒) 之间随机延后，避免所有监控同时首发。加载监控失败时不再整体停测，而是从 5 秒起退避重试（上限 5 分钟）。
- 对账：每 5 分钟比对一次库中启用的监控与内存任务，补建缺失的、撤销多余的；只处理早于本次快照的变化，不与新建、编辑、删除的回调冲突。
- 并发：同时执行 16 个监控的检测。检测在队列里等待超过半个间隔时跳过本轮，由下一轮按时执行，不在很晚的时刻补测。
- 多实例：每轮开始前用一条条件更新认领本轮——`last_checked_at` 早于 0.8 ×（间隔 − 抖动）才能认领，认领不到就跳过。蓝绿重叠时同一轮只有一个实例探测。`last_checked_at` 记的是一轮的开始时间。
- 取消：监控被停用、删除或进程停机时，在飞的检测会被取消，取消的检测不写历史。
- 手动“立即检测”：同一监控已有检测在跑时直接复用那一轮的结果，不再并发第二次；请求超时为 60 秒。

## 5. 读路径

最新状态与时间轴按“监控 × 当前配置的模型”逐对走 `(monitor_id, model, checked_at DESC)` 索引取最近 N 条，不再读遍全部历史。生产只读执行计划（4 个监控、约 24 万行历史）：时间轴约 180 ms → 1 ms，最新状态约 178 ms → 0.1 ms。已从监控配置里移除的模型不再出现在最新状态里。

## 6. 升级与回退

- 无迁移、无新设置；阈值写在代码常量里。
- 回退到 0.4.4 时，历史数据照常读取；旧版本恢复旧口径。

## 7. 配置建议

- 监控尽量绑定真实用户在用的分组与模型；只用于监控的分组若与真实分组共享账号，探测会占用这些账号的并发槽位。
- 同一个 Key、同一分组、同一模型只建一个监控；重复的监控只会在页面上多出一张相同的卡片。
- 检测间隔不要太短：探测在网关输出前断开会计入该 Key 的“未送达”，同一 Key 每小时超过 30 次会被按滥用计费（[`GATEWAY_RESILIENCE.md`](GATEWAY_RESILIENCE.md) §4.4）。
