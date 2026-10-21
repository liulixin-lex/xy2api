# 网关韧性（Gateway Resilience）

xy2api 0.4.0 引入的一组网关能力，目标是减少用户可感知的失败：上游抖动、单号分组整体不可用、CDN 空闲超时切断、客户端断开后的误计费，以及缺少重试指引的 5xx。0.4.2 修正了两份生产就绪审查（2026-10-19）核实的问题，改动见 tasks“0.4.2”小节。0.4.4 是第二期：上游开场后、正文前的失败可以换号，分类只把真正的容量信号算作降载，失败帧按情况带重试时间，自动识别只接受流式的账号（方案 `openspec/changes/gateway-resilience-v2-20261021/`）。本文已按 0.4.4 的行为更新。

- 方案与决策记录：`openspec/changes/gateway-resilience-20261015/`（proposal、design、tasks、runbook、staging-validation）；0.4.4 的改动见 `openspec/changes/gateway-resilience-v2-20261021/`。
- 全部能力由一份 JSON 设置控制，缺省全部开启，修改无需重启。关闭总开关后韧性能力全部停用、调度与错误处理按 0.3.0 逻辑运行；但有几项缺陷修复不受开关控制，见 §6。0.4.4 新增的开关单独关闭时回到 0.4.3 的对应行为，见 §4.2 与 §6。
- 本文面向运维与管理员，说明行为、配置、部署前提、观测与回退。

## 1. 能力一览

| 能力 | 设置段 | 行为摘要 |
|---|---|---|
| 失败分类与冷却 | `failure` | 上游瞬时失败（502/503/超时等）进入短冷却（缺省 30 秒），不再与账号级失败混用；上游 `Retry-After` 封顶。中转前面的 CDN 或代理写出的 5xx 错误页（Cloudflare 的 JSON 错误页、HTML 页面）按瞬时失败处理，即使文案提到 overloaded；“overloaded”“at capacity”只在 503、529、4xx 与流内事件上算容量降载（0.4.3）。已输出后的上游失败在同一账号 60 秒内达 3 次时，该账号进入同样的短冷却（池模式账号除外）。0.4.4（`failure.decision_v2`）：只有 `server_is_overloaded`、`slow_down`、529 与结构化的 overloaded 算容量降载；Grok 流空闲、raw 429、“The service is busy”“Too many pending requests”“Concurrency limit exceeded” 改为瞬时或限流，可以换到同一主机上的其他账号；余额等更强的证据优先 |
| 会话失败改绑 | `session` | 同一会话在同一账号上 10 分钟内连续失败 2 次后改绑，并在 10 分钟内避开该账号；已输出后的上游失败（流内失败事件、断流）也计入，客户端错误、内容安全拒绝与客户端断开不计。窗口按账号各自计算，同一会话其他账号的失败不会延长它（0.4.2）。绑定账号只是软罚（含 OpenAI 按模型的瞬时屏蔽）时保留绑定，本次由其他账号服务 |
| 末位兜底 | `last_resort` | 分组内没有无罚账号时，先转投兜底分组；否则软罚账号按优先级、再按罚期结束时间排序，取第一个有空闲并发的账号继续服务，全部占满时排队等待排在第一的账号，避免单号分组整组不可用。门禁与正常选号相同（只放宽软罚），超过窗口费用或 RPM 硬上限的账号不会被选中；份额分组按账号优先级与有效权重（负载因子乘自适应系数）选择，绑定账号只是软罚时不改绑。候选来自 2 秒的分组快照，派发前按账号当前状态再核对一次，快照之后被停用、删除、移出分组或进入硬罚的账号不会被选中（0.4.2） |
| 余额不足暂停 | `balance` | 上游余额不足的账号暂停调度 `pause_seconds`（缺省 300 秒），期间按硬罚处理、不作末位兜底；到期后由真实请求重试，仍不足就再暂停同样时长；账号列表的“恢复状态”立即放回。账号已有更长的软冷却（如 403 计数冷却 10 分钟）时，改记为余额暂停并保留较晚的结束时间；暂停期间新的软冷却不会覆盖它（0.4.2）。不监控、不探测、不告警。普通用户只看到 503，余额细节只对管理员可见。Gemini 的 `RESOURCE_EXHAUSTED` 分钟级限额按限流处理，不触发暂停；Grok API Key 账号的余额字样 429 同样暂停，不再按中转给出的重置时间限流 |
| 请求预算 | `budget` | 文本请求最多尝试 4 次、首输出前总时长 240 秒（等待并发槽位也计入）；图片与视频不设首输出截止；WebSocket 不挂连接级预算。同号重试每号至多一次，账号级失败不重试；容量降载只切换一次且只切到其他上游主机。非流式请求的“首输出”就是整个响应，预算只决定能否再发起新的尝试，进行中的单次非流式请求不按预算截断（截断仍在生成的长回答只会多一次失败）。分组里唯一能服务该请求模型的账号不受单次档位限制，用满剩余预算。例外：Antigravity 平台在同一账号上的智能重试（上游给出的短重试等待）不计入尝试次数，只在剩余预算与 `Retry-After` 封顶内等待 |
| 可重放前缀（0.4.4） | `stream` | 第一个正文之前写出的只有开场事件时，上游失败仍可换号（三个开关都只要求总开关开启）：原生 Responses 把 `response.queued` 与不带任何内容的 `output_item.added`、`content_part.added` 也算开场（`native_prelude_whitelist`；`codex.response.metadata`、`response.metadata` 等元数据事件不算）；Chat Completions 与 Messages 经 OpenAI 兼容转换时，开场事件推迟到第一个内容或终态才写出（`prelude_staging`）；raw Chat 透传把 `error` 后跟 `[DONE]` 判为失败（`strict_terminal`）：输出前换号，输出后按已输出后失败记账、原样转发错误。不完整（incomplete）、内容拒绝、合法空回答与客户端断开不重放 |
| 首包前保活 | `keepalive` | 仅对经 CDN 的流式请求（0.4.1 起按 CDN 自带的回源头自动识别 Cloudflare 与阿里云 ESA，见 §2.1）：请求开始 15 秒后仍无输出时提交 SSE 头并每 10 秒发心跳，避免 CDN 空闲超时；官方 OAuth / setup-token 账号不启用 |
| 停滞检测 | `stall` | 已输出后长时间无数据（缺省 120 秒）判定断流并结束请求；图片生成豁免。OpenAI 上游给出响应头后、输出前完全静默时，由首输出超时（按推理档位 180/200/220 秒）换号，不再在 180 秒处被停滞检测以终止帧结束（0.4.2）。停滞的回合计入会话失败回合。覆盖 OpenAI Responses、Anthropic 原生，以及 Chat Completions 与 Messages 经 OpenAI 兼容转换的流（0.4.2；未覆盖的路径见“已知限制”） |
| 断开计费 | `disconnect` | 客户端在任何语义输出前断开：立即取消上游、不写用量、不计费，记入未送达表（首包前心跳、排队等待槽位或用户消息队列时发出的 ping 不算输出）；部分输出后断开：最多再读 180 秒并按已得用量计费。0.4.2 起所有流式路径在第一个真实内容前写出的开场事件与心跳都不算输出；非流式与图片 JSON 以响应体是否写出为准，见 §4.4 |
| 错误语义与防风暴 | `errors` | 无号 503 带 `Retry-After`（2–30 秒）；配置错误 503 带 `Retry-After: 60` 与 `x-should-retry: false`；2 秒负缓存与同一 Key、同一路由与分组、同一模型的重复 503 快速返回，都只针对对整个分组成立的结论（组内没有平台匹配、支持该模型且无罚的账号，或配置错误），只对某个会话成立的“无号”（含末位兜底只因新会话撞上会话数上限而失败）不缓存、不计数；同一会话连续失败的回合封顶。0.4.4：合成的 `response.failed` 按情况带 `error.headers.retry-after`（`failed_frame_retry_after`）；Codex 请求的无号 `Retry-After` 上限 10 秒（`codex_retry_after_cap_seconds`）；这两项还要求 `errors.enabled`。Anthropic 协议下网关自身的失败用 `api_error`；Gateway 与 OpenAI→Messages 的专用耗尽出口也走公共重试提示与回合封顶 |
| 无号兜底分组 | `fallback_group` | 分组无可用账号时由其配置的兜底分组承接（按兜底组倍率计费），主分组稳定 120 秒后切回 |
| 仅流式能力（0.4.4） | `capability` | 非流式请求遇到上游“只接受流式”的 400 时，记住该账号对该协议的非流式请求不可用（`learn_ttl_seconds`，缺省 6 小时），本请求立即换号，不罚账号；之后的非流式请求跳过它，流式请求不受影响。本请求换号还需要 `failure.decision_v2` 开启，否则只学习、本请求照旧返回 400 |
| 在途预留 | `reservation` | 余额预留按会话缓存读比例估算输入费用；输出 Token 取模型近 7 天 P99（下限 1024） |

## 2. 部署前提

### 2.1 CDN 识别（边缘）

首包前保活、经 CDN 时 502/504 改答 503 等行为只对经 CDN 的请求生效。0.4.1 起应用按以下顺序识别请求经过的 CDN（“边缘”），部署镜像即生效，不需要配置反向代理或受信代理：

1. 反向代理注入的 `X-XY-Edge` 头（可选）：只在对端属于 `SERVER_TRUSTED_PROXIES` 时读取，取值须在 `keepalive.edges` 内；有效时优先。
2. 否则按 CDN 自带的回源头推断：`CF-Ray`，或 `CDN-Loop` 含 `cloudflare`，为 `cf`；`CDN-Loop` 含 `esa` 为 `esa`（阿里云 ESA）。`CDN-Loop` 依次列出经过的 CDN（RFC 8586），取离源站最近、且在 `keepalive.edges` 内的一个。生产核对（2026-10-18）：Cloudflare 与 ESA 的回源请求全部带这些头，XY-image 等内网直连请求都不带。

- 推断出的名字仍须在 `keepalive.edges` 白名单内（缺省 `cf`、`esa`）。从白名单去掉 `cf` 或 `esa`，就关闭对应 CDN 的识别。
- 推断不要求受信对端：直连源站的客户端伪造这些头，只改变它自己那个请求的返回形式（提前提交 SSE、15 秒后发心跳、失败改为流内终止帧、502/504 改为 503），不涉及鉴权、计费或其他用户。
- 内网直连与本机脚本不带 CDN 头，不会被识别为边缘请求。
- 接入其他 CDN，或 CDN 不带上述头时，仍可用反向代理按**对端 IP** 注入 `X-XY-Edge`（取值须加入 `keepalive.edges`），并把反向代理到应用的对端地址配进 `SERVER_TRUSTED_PROXIES`（`server.trusted_proxies` 或环境变量；仓库自带的 `deploy/docker-compose.yml` 不传这个变量）：

```caddy
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cf remote_ip <Cloudflare 回源网段>
	request_header @xy_edge_cf X-XY-Edge cf
	@xy_edge_esa remote_ip <ESA 回源网段>
	request_header @xy_edge_esa X-XY-Edge esa
}
```

  先删除客户端自带的同名头，再按 `remote_ip` 注入，不得根据任何请求头判断。
- 应用不会自动信任任何代理：Docker 下对端是网桥网关，端口对外发布时直连客户端也来自同一地址，自动信任会放开来源 IP 伪造、绕过 IP 白名单与限流。CDN 部署的整体信任边界见 [`deploy/EDGE_SECURITY.md`](../deploy/EDGE_SECURITY.md)。

### 2.2 反向代理

- 流式响应不得被压缩或缓冲（`encode` 不匹配 `text/event-stream`）。
- API 路径的 `handle_errors` 建议返回 JSON 错误与 `Retry-After`，避免纯文本 5xx（见 runbook §3.2）。
- 健康检查（0.4.2）：`/health` 只表示进程存活，不访问任何依赖，留给容器健康检查；`/ready` 检查数据库与 Redis（各 2 秒、并行），全部可用时 200 `{"status":"ready"}`，否则 503 并列出失败项名称（不含错误细节），结果复用 1 秒。反向代理的主动健康检查（如 Caddy `health_uri /ready`）与外部监控用 `/ready`。
- 数据库连接池（0.4.2）：启动时检查 `database.max_open_conns` 的两倍（蓝绿重叠时两个进程同时在用）是否超出 PostgreSQL 可给本应用的连接（`max_connections` 减保留连接与其他数据库用户的连接），超出时记一条 warn `database connection pool exceeds the server limit`，不阻止启动。推荐值见 runbook §3.4。
- 把客户端真实 IP 传给应用：反向代理前还有一层四层分流（如 nginx `stream` 按 SNI 转发）时，要用 PROXY 协议把来源地址带到七层，否则应用看到的客户端 IP 全是 `127.0.0.1`，按 IP 的限流、白名单与审计都会失效。见 [`deploy/EDGE_SECURITY.md`](../deploy/EDGE_SECURITY.md)。
- 蓝绿切换或两个进程共用一个 Redis（0.4.2）：每个进程持有 15 秒的槽位租约（每 5 秒续期，退出时不主动删除，排空中的请求仍受保护），新进程启动时只清理没有租约的进程留下的并发槽位，不再清掉仍在服务的旧进程的槽位，另有存活进程时也不删共享的等待计数；旧进程退出后，周期清理在连续两次看不到它的租约时（约 75 秒内）收走它的残留，一次续期失败（如 Redis 短暂抖动）不会清掉存活进程。运行期的清理不删等待计数，交给它们自己的 TTL。0.4.1 及以前的进程不写租约，无法与已退出的进程区分：从这些版本首次蓝绿切到 0.4.2，以及从 0.4.2 回退到它们时，仍会清掉对方的槽位，见 runbook §3.3。

### 2.3 数据库迁移

0.4.0 启动时执行以下迁移，全部只追加（277 只删除 276 种下的两条告警规则），0.3.0 实例可继续读写：

| 迁移 | 内容 |
|---|---|
| `272_add_group_availability_fallback.sql` | `groups.availability_fallback_group_id` |
| `273_add_usage_logs_origin_group.sql` | `usage_logs.origin_group_id`（不加外键与默认值，只改元数据） |
| `274_create_gateway_undelivered_requests.sql` | 未送达请求表（保留 30 天，每 6 小时清理） |
| `275_add_ops_error_logs_disconnect_stage.sql` | 运维错误日志的断开阶段与已送达字节 |
| `276_seed_balance_alert_rules.sql` | 两条默认告警规则：“上游余额不足”“上游余额长期未恢复”（按名称幂等插入，已存在则不改）；RC 已执行，保持不变 |
| `277_remove_balance_alert_rules.sql` | 删除 276 种下的这两条规则（名称与 `metric_type` 同时匹配才删）；余额精简后不再有余额告警 |

## 3. 客户端可见的行为

| 场景 | 响应 |
|---|---|
| 无可用账号 | 503，`Retry-After` 为 2–30 秒（带抖动），消息附 “Please try again in Ns.”。Codex 请求（按 UA 或 `originator` 识别）上限 10 秒（0.4.4，`errors.codex_retry_after_cap_seconds`）：Codex 会按它静默等待。非流式请求只因组内账号都被学为仅流式而无号时，原因码为 `capability_stream_only`，消息为 `No available account in this group supports non-streaming requests; retry with "stream": true`，不带 `Retry-After`，`x-should-retry: false` |
| 配置导致的无号（分组仅限 Claude Code、渠道定价限制） | 503，`Retry-After: 60`，`x-should-retry: false`；Codex 请求的 `Retry-After` 同样不超过 `errors.codex_retry_after_cap_seconds`（0.4.4） |
| 模型不存在 | 保持原响应，不加重试头 |
| 余额被在途请求占用 | 429，`rate_limit_error` / `concurrent_balance_reserved`，`Retry-After` 约 5 秒 |
| 上游余额不足 | 普通用户看到 503；账号暂停期间单号分组的 503 带 `Retry-After`（暂停剩余时间，2–30 秒）；管理员在账号列表看到临时不可调度（原因 `balance:`）。管理员配置的错误透传规则命中时按规则返回 |
| 首包前心跳已提交后失败 | 不再改写状态码，改为协议终止帧：Responses 为 `response.failed`（`rate_limit_exceeded` 或 `server_error`），客户端此前只收到心跳时前面先补同一响应的 `response.created`（openai-python 等严格 SDK 要求流以它开始）；Chat 为 `data: {"error":{…,"status":N}}` 后跟 `[DONE]`；Anthropic 为 `event: error` |
| 合成 `response.failed` 的重试时间（0.4.4） | `errors.failed_frame_retry_after` 开启时写在 `error.headers.retry-after`（Codex 从这里读）：本请求还有健康账号时为 1 秒；组内没有健康账号、能算出最早恢复时间时为 2–10 秒，正文 “try again in Ns” 的 N 相同；恢复时间未知时不带（0.4.3 缺省提示 30 秒）；回合封顶、用户自身余额不足、内容拒绝不带，并去掉正文里的重试句（上游账号余额不足会换号，不算）；配置拒绝与鉴权类错误不给重试建议。HTTP 头尚未提交时同步改写 `Retry-After`。关闭时不带 `error.headers`，正文同 0.4.3 |
| 排队等待时已发出 ping，之后遇到计费等错误（0.4.4） | 按入站协议写终止帧：Responses 为 `response.created` 后跟 `response.failed`，Chat 为 `error` 后跟 `[DONE]`；不再写裸 JSON（不受开关控制） |
| 上游开场后、正文前失败（0.4.4） | 可重试的失败（过载、服务忙、断流、读错）换号，客户端只看到成功那次尝试的开场：Responses 只有一个 `response.created`（新账号的 id），Chat 只有一个 role 分块，Messages 只有一个 `message_start`。兼容转换推迟开场期间按流内心跳间隔写注释行 `:`（与原生 Responses 一致），心跳不算输出、不妨碍换号；心跳已提交 200 后再耗尽时，以协议终止帧结束。`response.incomplete`、内容拒绝、只有 usage 的空回答照常返回，不重放 |
| 非流式请求被上游以“只接受流式”拒绝（0.4.4） | 例如 “This group only accepts streaming requests”：本请求换号，不计账号失败、不冷却；该账号对该协议的非流式请求在 `capability.learn_ttl_seconds` 内被跳过。`failure.decision_v2` 关闭时只学习，本请求照旧返回 400；本请求的失败尝试全部是这个理由时返回 503（不是 400），应答与上面“无可用账号”中仅流式一种相同；混有其他失败原因时按普通的耗尽处理。无号的负缓存按协议与流模式分开，能力阻断不计入快速失败，流式请求不受影响 |
| 心跳帧 | Responses / Chat 为 SSE 注释行 `:`；Anthropic 为 `event: ping` |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，并带 `X-XY-Upstream-Status: 502/504`。Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉 JSON 错误、`Retry-After` 和请求 ID；503 原样透传。直连请求保持原状态码。运维错误记录仍记原始的 502/504 |
| 换号耗尽的 429/5xx | 带 `Retry-After`：上游给出的值按 2–30 秒封顶，没有时取分组最早恢复时间 |
| 同一会话连续第 5 次耗尽失败（无号、换号耗尽、流内失败） | 终止应答：不带 `Retry-After`，`x-should-retry: false`，`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持）。Codex 对 5xx 的重试次数有上限（4 次传输层重试 × 6 次流层尝试，一个回合最多约 30 次请求），但按 `Retry-After` 等待的时长没有上限（0.158.0 起；更早的版本忽略 `Retry-After`）；封顶后不超过 5 次。会话真正成功一次或两次失败相隔超过 120 秒后重新计数 |
| 中转站插入的 `SSE-Keep-Alive` 空 delta 帧；0.4.4 起还包括 `response.created` 之前、`delta` 为空字符串或只有空白的 delta | 直接丢弃，不转发、不算首输出（openai-python 遇到会报错，网关也会因此提前放弃换号）；但算上游存活，不会因此被判停滞。`response.created` 之前的非空 delta 是真实内容，照常立即输出，计入 `pre_created_delta`。缺省首字口径（`openai_ttft_mode=semantic`）下，用量记录的首字时间仍从第一帧算起，与 0.3.0 一致；`visible` 口径不算这类帧 |
| Claude 请求分发到 GPT 中转时，中转站拒绝续链（`previous_response_id`）并返回笼统的 4xx | 去掉续链按完整历史重放一次，并对该会话在该账号上关闭续链。0.4.2 起只认指向续链的应答：404/409、提到上一响应的文案，或 400/422 上的笼统中转文案（单独的 “Upstream request failed”、“could not be processed”、没有文案）；指名其他参数的错误（如 `temperature` 越界）、上下文超长、内容审核、图片、模型不存在等照常返回，不重放。按笼统文案猜测的重放计入请求预算的尝试次数，预算用尽时不重放 |
| 上游内容安全拒绝（如智谱 `request_check_blocked`、`content_policy_violation`） | 400 `invalid_request_error`，`code: content_policy_violation`，附脱敏后的上游原文；不重试、不换号，账号不受罚。只关闭 `errors` 时回到 0.3.0 的 502 文案 |
| Anthropic 中转在流内报 `rate_limit_error`（如并发上限） | 输出前可换号；耗尽时 429 带 `Retry-After`，不再是 502“访问被禁止”。`overloaded_error` 为 529，`api_error` 为 502 |
| Anthropic 协议下网关自身的无号与瞬时失败（0.4.4） | 错误 `type` 统一为 `api_error`（含只有 code 的错误体），状态码与正文不变；只有上游真实返回 `overloaded_error` 时才保留该类型，状态码 529 本身不再足以生成它。以前首包心跳后的 503 终止帧与按状态码推断的失败会被标成 `overloaded_error`。关闭 `errors` 时回到 0.4.3 |
| API Key 中转拒绝回放 `web_search_call` 历史（“response protection is unavailable”） | 同号补 web_search 声明重放一次，成功后该账号 6 小时内同类请求直接补声明；仍失败时 502 带原因，不换号，计入会话失败与回合封顶 |
| 请求的模型不在分组白名单 | 404，消息附本分组白名单的前 10 项 |

## 4. 配置

### 4.1 入口

- 管理端“系统设置 → 网关”中的“网关韧性”卡：顶部为总开关；每项能力一张卡片，放该能力的开关、说明与常用数值（例如请求预算的最多尝试次数与首输出前时长，首包前保活的延迟、间隔与边缘标记），宽屏每行两张；底部的高级选项可展开编辑完整 JSON。0.4.4 新增三张卡片：“失败分类修正”（`failure.decision_v2`）、“流式开场与终止”（`stream` 段三个开关）、“仅流式账号学习”（`capability` 段）；`errors` 段的两项放在“错误语义与防风暴”卡片里。说明文字写明各开关的依赖。
- 接口：`GET/PUT /api/v1/admin/settings/gateway-resilience`。`PUT` 提交的字段合并到当前设置（`budget.model_overrides` 若出现则整体替换），未知字段拒绝，服务端校验全部范围，未通过返回 `GATEWAY_RESILIENCE_SETTINGS_INVALID` 且不生效；响应同时返回当前值与缺省值。设置存在 `settings` 表的 `gateway_resilience_settings`，0.4.4 的新字段也在这一份文档里。
- 设置变更在保存的实例上即时生效，其他实例每 10 秒刷新一次；读取失败或存量文档非法时保留最后一次有效值。

### 4.2 常用字段（缺省值与范围）

| 字段 | 缺省 | 范围 |
|---|---|---|
| `budget.max_attempts` | 4 | 1–10 |
| `budget.pre_output_budget_seconds` | 240 | 60–290 |
| `budget.first_output_timeout_seconds` | default 180 / high 180 / xhigh 200 / max 220（按生产 7 天首字分布校准，超出比例约 0.1%–0.2%） | 5–预算 |
| `budget.anthropic_first_event_timeout_seconds` | 90 | 5–预算 |
| `budget.same_account_retry_max` | 1 | 0–5 |
| `keepalive.delay_seconds` / `interval_seconds` | 15 / 10 | 5–60 / 5–30 |
| `keepalive.edges` | `["cf","esa"]` | 小写字母、数字或 `-` |
| `stall.text_stall_seconds` | 120 | 30–280 |
| `disconnect.partial_drain_max_seconds` | 180 | 1–3600 |
| `disconnect.abuse_guard.threshold_per_hour` | 30 | 1–100000 |
| `errors.no_account_retry_after` | 2–30 | 1 ≤ min ≤ max ≤ 300 |
| `errors.negative_cache_seconds` | 2 | 1–60 |
| `errors.fast_fail` | 30 秒内 10 次 | 窗口 1–3600，阈值 2–10000 |
| `errors.turn_cap` | 连续 5 次，间隔 120 秒内 | 次数 2–30，窗口 10–3600 |
| `fallback_group.return_stable_seconds` | 120 | 1–86400 |
| `fallback_group.binding_ttl_seconds` | 600 | 1–86400 |
| `balance.pause_seconds` | 300 | 30–3600 |
| `stream.prelude_staging`（0.4.4） | 开 | 开关；兼容转换推迟开场（§1） |
| `stream.native_prelude_whitelist`（0.4.4） | 开 | 开关；原生 Responses 开场白名单扩展 |
| `stream.strict_terminal`（0.4.4） | 开 | 开关；raw Chat 的 `error` 后跟 `[DONE]` 判为失败 |
| `failure.decision_v2`（0.4.4） | 开 | 开关；分类修正，也是仅流式拒绝后本请求换号的前提 |
| `errors.failed_frame_retry_after`（0.4.4） | 开 | 开关；另需 `errors.enabled` |
| `errors.codex_retry_after_cap_seconds`（0.4.4） | 10 | 0–30，0 表示不另设上限；另需 `errors.enabled` |
| `capability.stream_only_learning`（0.4.4） | 开 | 开关 |
| `capability.learn_ttl_seconds`（0.4.4） | 21600 | 600–86400；只影响之后新学到的记录 |

完整字段与校验见 `backend/internal/resilience/settings.go`。

### 4.3 无号兜底分组

- 在分组编辑页选择“无号兜底分组”：须为同平台、启用、非订阅的其他分组；不能指向自身，不能成环。
- 触发条件：主分组没有任何无罚账号，且兜底组有无罚账号。判定只在请求入口进行一次，换号耗尽不跨组。
- 进入兜底后按 API Key 记忆（Redis，TTL `binding_ttl_seconds`）；主分组持续健康 `return_stable_seconds` 后切回，抖动的主分组不会来回切换。
- 用量记录：`usage_logs.group_id` 为实际服务的兜底组，`origin_group_id` 为原分组；用量列表显示“兜底：原分组 → 实际分组”，可用渠道页显示各组的兜底分组与倍率。

### 4.4 断开计费与滥用保护

- 未送达（任何语义字节前断开）：上游立即取消，不写用量，记入未送达表。
- 同一 Key 每小时未送达超过阈值（缺省 30）时视为滥用：不再取消上游，按上游成本计费，用量 `billing_mode` 为 `undelivered_abuse`。请求体还没收完就断开（阶段“上传请求中”，`upload`）照常记入未送达表，但不计入这个次数：上游没有被调用（0.4.3）。
- 送达口径（0.4.2）：
  - 所有路径：首包前保活心跳与排队等待槽位、用户消息队列时的 ping 不算送达。
  - 所有流式（`text/event-stream`）响应：在第一个真实内容前写出的开场事件不算送达。开场事件只有：SSE 注释行、`ping`、`keepalive`，Anthropic `message_start` 与空的 text/thinking `content_block_start`，Responses `response.created`/`in_progress`/`queued`，只带角色的 Chat 分块。其余一律算内容，包括错误事件、`[DONE]`、`tool_use` 块与任何未知事件，因此识别不了的形式只会回到原来的计法。覆盖 OpenAI `/v1/responses`（含透传）、Chat Completions 与 Messages 兼容转换、Anthropic 原生与直通、Grok `/v1/responses`、图片 SSE 等全部流式路径；只改计数，事件照常、按时写出，换号判断不受影响。
  - 只收到开场事件与心跳就断开：算未送达，立即取消上游、不计费；收到内容后断开按部分输出处理（最多再读 180 秒，按已得用量计费）。
  - 非流式与图片 JSON：以响应体是否已写出为准（图片 JSON 的保活填充不算）。
- 未送达表的写入在后台进行，同时最多 16 个，失败重试一次；全部占满时这条记录不写并记 `undelivered_record_dropped`。计费不依赖这张表。

## 5. 观测

### 5.1 管理端

- 运维页“网关韧性”面板：当前实例最近 5 分钟至 1 小时的计数、未送达请求列表；计数与未送达列表按分组名显示。
- 选号解释：运维页面板底部可选任一分组（含原版调度），可选填模型与会话哈希，只读查看每个账号的硬、软档与原因（余额暂停为 `balance_exhausted`）、恢复时间、会话失败次数与是否回避，以及末位兜底账号与兜底分组决策；填了模型时，若该模型被分组白名单或渠道定价拒绝（所有请求在选号前即失败），在账号表上方提示。份额调度 Explain 的结果中附同样的韧性视图。0.4.4 起结果多一列“已学能力”（仅流式学习记录，任何查询都显示）；可选“请求类型”（Responses 或 Chat 的非流式请求）时，按该请求形态把被学为仅流式的账号标为跳过，原因 `capability_stream_only`，恢复时间即学习到期时间；不选则保持原来的整体健康视图。末位兜底账号不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。
- 账号列表：余额不足暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文；“恢复状态”立即放回。欠费但暂时不用的账号，关掉调度即可。
- 仅流式学习（0.4.4）：被学为只接受流式的账号在账号列表的状态列下方显示“仅流式（自动学习，剩余 X 小时）”，悬停说明涉及的协议，点“清除”即可；清除后下一个非流式请求可以再选它。选号解释中被跳过的账号显示原因 `capability_stream_only`，恢复时间即学习记录的到期时间。
- 运维错误详情：每次上游失败带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`）。
- 告警：不新增余额类指标。RC 的迁移 276 预置过两条余额告警规则，由迁移 277 删除。
- 接口：
  - `GET /api/v1/admin/resilience/stats?window=15m`（窗口最长 1 小时，计数为单实例内存口径）；
  - `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=`（时间为 RFC3339）；
  - `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=`（只读；0.4.4 起每个账号带 `capability_learned`，`protocol=responses|chat` 与 `stream=true|false` 须成对出现，否则 400；`stream=false` 时按该请求形态排除仅流式账号，原因 `capability_stream_only`、`recover_at` 与 `capability_ttl_seconds`）；
  - 账号列表与详情的 `capability_learned`（0.4.4）：`[{protocol, mode: "stream_only", expires_at}]`，没有记录时为空数组；`DELETE /api/v1/admin/accounts/:id/capability-learning` 清除。

### 5.2 计数项

| 计数 | 含义 |
|---|---|
| `keepalive_committed` | 首包前心跳提交了响应头（细分为协议） |
| `first_output_timeout` | 首输出超时触发换号 |
| `budget_exhausted` | 请求预算耗尽 |
| `last_resort` | 末位兜底选中软罚账号 |
| `fallback_group` / `fallback_return` | 转投兜底分组 / 切回主分组 |
| `session_rebind` | 会话失败改绑 |
| `undelivered` / `cdn_cut` | 未送达；按耗时推断的 CDN 切断（90 秒、125 秒、300 秒） |
| `fast_fail` | 同一 Key、同一模型的重复 503 快速返回 |
| `turn_cap` | 会话连续耗尽失败到达上限，改为终止应答 |
| `abuse_guard` | 未送达滥用保护计费 |
| `stall` | 停滞断流 |
| `post_output_failure` | 已输出后的上游失败（细分为 `stream_failed`、`stream_read_error`、`missing_terminal`） |
| `relay_keepalive` | 从上游流中丢弃的中转伪保活帧（按账号；0.4.4 起含 `response.created` 之前的空 delta） |
| `pre_created_delta` | 上游在 `response.created` 之前发出的非空 delta（按分组、账号、平台，0.4.4）；照常输出，用来观察这类中转的规模 |

### 5.3 日志事件

结构化日志的消息固定为事件名（前缀 `resilience.`），细节在字段中。

warn 级，进入运维页“系统日志”（`ops_system_logs`）：

- `balance_paused`（含账号、暂停时长与上游原文）；
- `session_rebind`、`last_resort`；
- `fallback_group`、`fallback_return`；
- `budget_exhausted`、`capacity_switch_exhausted`；
- `stall`、`fast_fail`、`turn_cap`、`abuse_guard`；
- `post_output_failure`（含 `group_id`、`account_id`、`reason`、窗口内次数与是否施加冷却）；
- `undelivered_record_dropped`：未送达记录的后台写入全部占满，这条记录未写（含 `request_id`）；
- `last_resort_verify_failed`：末位兜底派发前复核账号时读库失败，按快照中的账号继续；
- `capability_learning_failed`（0.4.4）：仅流式学习结果写入失败（含 `account_id`、`protocol`、`error`）；本请求仍可在预算内换号，只是下一次不会跳过该账号；
- `web_search_history_rejected`：API Key 中转拒绝回放 web_search 历史，补声明重放后仍失败或无法重放（含 `account_id`、`upstream_status`、是否已重放、是否透传）。

info 级，只写应用标准输出（量大，明细已有计数或未送达表）：

- `keepalive_committed`（含 `group_id`、`protocol`、`edge`）；
- `unclassified`：只按状态码归类的上游 4xx 文案采样（脱敏、截断，每分钟约 10 条），发现新的余额文案时加入 `failure.balance_keywords_extra`；
- `undelivered`（含阶段、耗时、推断的 CDN 切断与 `edge`）；
- `transient_cooldown`、`transient_lifted`、`undelivered_cleanup`；
- `content_policy_refusal`（上游内容安全拒绝，含账号、平台、状态与截断的上游原文）、`content_policy_no_failover`（换号循环因内容安全拒绝结束）；
- `web_search_replay`（补 web_search 声明后同号重放）、`web_search_declaration_learned`（重放成功，该账号 6 小时内同类请求直接补声明）；
- `pre_created_delta`（0.4.4）：上游在 `response.created` 之前发出非空 delta，含 `group_id`、`account_id` 与 `bytes`（只记字节数，不记内容）；
- `capability_learned`（0.4.4）：账号被学为仅流式，含 `account_id`、`protocol`、`expires_at`。

0.4.4 起，`post_output_failure`、`session_rebind`、`last_resort`、`turn_cap`、`keepalive_committed` 另带以下字段，用于把事件与请求关联、判断失败发生在输出的哪个阶段（只是日志字段，不新增表）：

| 字段 | 含义 |
|---|---|
| `request_id` | 请求 ID，与运维错误记录、访问日志里的请求 ID 相同，可用来关联 |
| `gateway_request_id` | 网关为每个请求生成的本地 ID（UUID），客户端无法指定 |
| `model` | 入站请求的模型，没有时为选号用的模型 |
| `delivered_bytes` | 已送达客户端的内容字节（开场事件与心跳不算，口径同 §4.4） |
| `ms_since_first_output` | 距第一次写出内容的毫秒数；还没有输出时为 0 |
| `prelude_release_event` | 结束开场暂存、开始向客户端输出的那个上游事件类型（记的是释放的决定，不保证写出成功）；没有暂存过时为空 |
| `prelude_release_ms` | 上一项距 `response.created` 的毫秒数；没有收到 created 时为 0 |

`keepalive_committed` 发生在输出之前，`delivered_bytes` 与 `ms_since_first_output` 总是 0。日志不含正文、Key 或完整会话值。

`*_failed` 结尾的事件表示存储或查询失败，功能会降级为原有行为。

## 6. 回退

- 单项关闭：在“网关韧性”卡中关闭对应能力，即时生效。各能力关闭后的行为见 runbook §6。0.4.4 新增的开关关闭后回到 0.4.3 的对应行为，不是 0.3.0；关闭 `capability.stream_only_learning` 后已学到的记录不删除，按到期时间自然过期，期间重新开启仍然生效。
- 整体关闭：关闭总开关，韧性能力全部停用，调度与错误处理按 0.3.0 逻辑运行。份额分组随之回到 0.3.0 的软溢出规则（300 秒内结束的状态都只临时借用、不改绑，余额暂停不特殊处理），健康降权与恢复爬升停用，见 [份额调度](SHARE_SCHEDULING.md)。
- 从 0.4.4 回退到 0.4.3：无迁移，直接换回镜像。仅流式学习记录留在账号 `extra` 里，0.4.3 不读，回退后不再过滤；再升级时未到期的记录仍然生效。0.4.3 读取设置时忽略 0.4.4 新增的字段，原有字段与行为不变。**在 0.4.3 上再次保存设置会丢弃新字段**：再升级到 0.4.4 时它们回到缺省值（全部开启），回退期间关掉过的新开关要重新关闭。回退后仍开着 0.4.4 管理页面的浏览器，保存会因未知字段被拒，刷新页面即可。
- 版本回退：迁移只追加，0.3.0 可直接运行在 0.4.0 的库上，无需回退数据库。回退时余额暂停中的账号在暂停到期后回到流量里，0.3.0 下再遇余额失败按原有逻辑处理（402 置错、403 计数）；不希望欠费账号被试探的，回退前关掉其调度。
- 实际生效的重试与尝试上限（多种机制取最小值）见 runbook §6“生效重试上限”。

不受总开关控制的改动（多为缺陷修复，关闭总开关后仍然生效）：

| 改动 | 说明 |
|---|---|
| 全 0 会话 ID 视为缺失 | `metadata.session_id` 全为 0 时改用可缓存内容摘要做粘性键（具名补丁 `xy-placeholder-session-id`） |
| index.html 不返回 304 | 始终返回带本次 CSP nonce 的完整页面（`xy-index-nonce-no-304`） |
| 白名单 404 列出可用模型 | HTTP 入口的 404 文案附本分组白名单前 10 项（`xy-allowlist-404-hint`） |
| 排队 ping 之后按协议写终止帧（0.4.4） | Gateway 入站的 Responses 与 Chat 在 HTTP 已提交后（如排队等待时已发出 ping）遇到计费、在途预留、模型等错误，写 `response.created` 后跟 `response.failed`，或 `error` 后跟 `[DONE]`，不再写裸 JSON；未提交时状态码与正文不变 |
| 上传中断记 499 | 读请求体时连接中断造成的 400 在运维记录里记为 499 |
| §9.8 续链被拒整轮重放 | Claude 请求分发到 GPT 中转、续链以笼统 4xx 被拒时去掉续链重放一次并关闭该会话续链（0.4.2 收窄判定，见 §3） |
| `/ready` 就绪检查 | 检查数据库与 Redis，见 §2.2（0.4.2） |
| 并发槽位的进程租约 | 新进程启动时保留仍有租约的其他进程的槽位，见 §2.2（0.4.2） |

管理端只读功能（选号解释中的模型限制提示等）也不受开关影响。

## 7. 已知限制

- 统计计数保存在进程内存，多实例部署时按实例分别统计，重启后清零。
- WebSocket 不挂连接级预算，建连首回合的换号仍按 `max_account_switches`；Gemini compat 服务未接入首事件守卫，由停滞检测兜底。
- Chat Completions 与 Messages 经 OpenAI 兼容转换时（0.4.2），首输出档位约束等待响应头和第一行上游数据。0.4.4 起开场事件推迟到第一个内容（`stream.prelude_staging`），首事件守卫随之延长到第一个内容，此前的失败可以换号；第一个内容写出之后的静默由停滞检测结束并记账，不再换号；关闭该开关时回到 0.4.2 的行为（第一行之后写出开场，不再换号）；如果此后上游只发中转站伪保活帧、不出内容，按 §7.8 算存活，停滞检测不会结束它。停滞检测未覆盖或不记账的路径：原生 Chat Completions 透传（账号不支持 `/responses` 时）没有停滞检测；CN Anthropic 原生上的 Responses 与 Chat 转换停滞时只结束请求、不冷却账号；Anthropic API Key 透传、Bedrock、CN Anthropic 原生 Messages 记账但不分输出前后，统一按 `stream_data_interval_timeout`；Antigravity 与图片按各自的规则。
- 0.4.4 的可重放前缀与明确终止只覆盖 OpenAI Responses（含透传的开场判定）、Chat Completions 与 Messages 经 OpenAI 兼容转换、raw Chat 透传。以下路径仍按 0.4.3 的开场与终止行为：Gemini 原生与兼容转换、Bedrock、CN Anthropic 原生、Antigravity（T2）、Gateway 入站经 Anthropic 上游的 Responses 与 Chat 反向转换。
- 兼容转换推迟开场期间的注释心跳会提交 200，此后的失败只能以带内终止帧告知客户端（与原生 Responses 相同）。
- 仅流式学习只认表中确定的结构码与文案（如 “only accepts streaming requests”、“stream must be true”）；其他措辞的同类拒绝仍按客户端错误返回。只覆盖 OpenAI 入站的 Responses 与 Chat 普通文本请求，以及 OpenAI、Grok 平台的账号；compact、图片、WebSocket、Messages 经 OpenAI 转换，以及 Gemini、CN、Bedrock 不学习、不过滤。只在上游协议与入站相同、上游流模式跟随客户端的路径上过滤；跨协议转换（如 Chat 转 Responses 上游）、按模型路由的路径与路由无法确定的 Grok OAuth Chat 不过滤。错误带非 `stream` 的 `param`，或请求体里本来就有这句话（例如写在提示词里）时不学习。学习结果存在账号 `extra`（`capability_learned_responses`、`capability_learned_chat`），绑定账号的上游配置：改了上游配置的账号不受旧记录影响；复制、创建与导入账号不带学习结果。
- 0.4.4 容量判据收紧后，“Codex is at capacity”与非 503 上的 “servers are currently overloaded” 不再算容量降载，会让账号进入软冷却并计入会话失败；Grok 重试耗尽的原始 429 不再转成无号 503，客户端看到 429。
- 原生 Responses 路径上，上游在 `response.created` 之前发出的非空 delta 照常先转发（网关不重排上游的真实内容），这类上游下严格 SDK 仍可能报 “Expected to have received response.created”。
- 仅流式学习记录绑定账号凭据：OAuth 类账号刷新令牌后会重新学习一次。学习粒度是“账号 + 协议”，不分模型：中转只对某个模型要求流式时，该账号所有模型的非流式请求都会被跳过，可以在账号列表手动清除。
- 断开阶段粒度为上传请求（0.4.3）、选号、等待响应头、首输出前、输出中五段。Chat Completions 与 Messages 经 OpenAI 兼容转换的路径，上游响应头到达后阶段不推进到“首输出前”：只写出开场分块（`message_start`、只带 role 的 chat 分块）就断开时，未送达记录与计数的阶段记为“等待响应头”。这只影响按阶段的统计，不影响是否计费、是否取消上游（两者只看是否写出过内容）。
- 兜底判断、末位兜底与无号 `Retry-After` 读取分组账号的进程内快照（2 秒）。本网关施加的冷却与余额暂停会立即刷新快照；其他来源（上游 429 限流、错误状态、管理员改动、其他实例）最多 2 秒后可见。末位兜底在派发前按账号当前状态复核所选账号，快照的滞后只影响兜底判断与 `Retry-After` 的估算。
- 失败回合计数在进程内，多实例时各自计数；快速失败的原样返回发生在选号之前，不参与计数；封顶后的终止应答也不计入快速失败。
- 未送达表固定保留 30 天。
- 滥用保护只在上游返回用量时按成本计费。
- 兜底分组仅支持标准（余额）分组；进入兜底的记忆按 API Key 而非会话。主组健康且本实例近 `binding_ttl_seconds` 内未转出任何 Key 时不读 Redis，因此重启前转出的 Key 重启后立即回到主组。
- 按单实例部署设计。会话失败与回避、末位兜底、兜底转移各有一份进程内索引（用于省去 Redis 往返）；多实例时，另一实例记下的回避不会被本实例读取，本实例的成功也不会清零另一实例的计数。已输出后失败的账号计数、需要补 web_search 声明的账号记忆也在进程内，多实例时各自维护。蓝绿切换的重叠期内两个进程共用 Redis 是支持的（并发槽位按进程租约保留，见 §2.2），长期多活需另行评估上述进程内状态。
- 内容安全拒绝的提前结束与 web_search 补声明重放未接入 Responses WebSocket 路径（账号免罚已生效）；Grok 的进程内阻断（模型配额、团队限流）不显示在选号解释中。OpenAI 上游连续 5xx 后的按模型瞬时屏蔽（10/45 秒）可作为末位兜底，屏蔽期间绑定在该账号上的会话保留绑定（0.4.2）；它不显示在选号解释中，也不参与无号 `Retry-After` 的估算。
- 韧性设置的保存没有版本校验：两位管理员基于旧页面先后保存时，后保存的覆盖先保存的（份额策略有版本校验）。单管理员维护时无影响；多人维护时保存前先刷新页面。
- 未送达的计数按“开场事件之后是否写出过内容”判定，Gemini 原生流等不带这些开场事件的路径，第一个数据块即算送达。

## 8. 与份额调度的关系

份额调度（[说明](SHARE_SCHEDULING.md)）复用网关韧性的判定：

- **软溢出**：份额分组的绑定账号只在韧性判为软罚、且在 300 秒内恢复时保留绑定、临时借用其他账号；硬罚（含余额暂停）下一次请求即改绑。
- **健康降权**：份额权重的健康系数只统计韧性会给短时冷却的失败类别（瞬时错误、首字超时、停滞），数据来自运维记录 `ops_error_logs.upstream_errors` 中每次尝试的 `resilience_category`。余额、鉴权、限流与降载已有各自的处置，不重复扣分。
- **恢复爬升**：余额暂停到期、硬罚到期（含超过 300 秒的限流），或本实例观察到账号连续至少 5 分钟不可用后重新可用（按 60 秒采样），份额分组里该账号的新会话权重在 10 分钟内从 10% 回升到 100%；暂停与冷却本身不变。
- **末位兜底**与**会话连续失败改绑**在份额分组中照常生效，见上文。
- 关闭韧性总开关时，以上份额行为全部回到 0.3.0。

