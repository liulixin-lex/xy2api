# 网关韧性（Gateway Resilience）

xy2api 0.4.0 引入的一组网关能力，目标是减少用户可感知的失败：上游抖动、单号分组整体不可用、CDN 空闲超时切断、客户端断开后的误计费，以及缺少重试指引的 5xx。0.4.2 修正了两份生产就绪审查（2026-10-19）核实的问题，改动见 tasks“0.4.2”小节。0.4.4 是第二期：上游开场后、正文前的失败可以换号，分类只把真正的容量信号算作降载，失败帧按情况带重试时间，自动识别只接受流式的账号（方案 `openspec/changes/gateway-resilience-v2-20261021/`）。0.4.5 修复 0.4.4 的已知缺陷 RA-1，补齐 W1.2 的其余三条路径（OpenAI Responses 透传，以及 Gateway 入站经 Anthropic 上游的 Responses 与 Chat），并修复审查遗留的 P2（方案 `openspec/changes/gateway-resilience-045-20261021/`）。本文已按 0.4.5 的行为更新；main 上尚未发布的 0.4.6 改动单独标注（目前只有一项：`response.metadata` 改为开场事件，见 §1）。

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
| 可重放前缀（0.4.4） | `stream` | 第一个正文之前写出的只有开场事件时，上游失败仍可换号（三个开关都只要求总开关开启；0.4.6 的 `response.metadata` 在兼容转换里还要求 `native_prelude_whitelist`）：原生 Responses 把 `response.queued` 与不带任何内容的 `output_item.added`、`content_part.added` 也算开场（`native_prelude_whitelist`；0.4.6 起还包括 `response.metadata`：OpenAI Codex 后端过载时会在 created、in_progress 与 `response.metadata` 之后才发 `server_is_overloaded`。按确切事件名判定，`codex.response.metadata` 与其他未知事件仍算内容。它与其他开场一样不解除首输出截止，之后只有心跳、迟迟没有正文的尝试到期换号（`first_output_timeout`）；0.4.3 至 0.4.5 它会提前解除截止。判定共用 `ClassifyStreamEvent`，Chat Completions 与 Messages 兼容转换同样适用，须同时开启 `prelude_staging`）；Chat Completions 与 Messages 经 OpenAI 兼容转换时，开场事件推迟到第一个内容或终态才写出（`prelude_staging`）；raw Chat 透传把 `error` 后跟 `[DONE]` 判为失败（`strict_terminal`；0.4.5 起 `error` 须是非空字符串，或带非空 `message`、`code`、`type` 的对象，或根上 `type` 为 `error`；`null`、空串或只含空白的字符串、`false`、`0`、`{}` 与各字段都为空的对象等占位值不算）：输出前换号，输出后按已输出后失败记账、原样转发错误。0.4.5：Gateway 入站经 Anthropic 上游的 Responses 与 Chat 反向转换同样推迟开场（`prelude_staging`，首事件截止延长到第一个内容）并明确终止（`strict_terminal`，见 §3）；OpenAI Responses 透传接入首输出截止与停滞检测（`strict_terminal`，见 §3）。不完整（incomplete）、内容拒绝、合法空回答与客户端断开不重放 |
| 首包前保活 | `keepalive` | 仅对经 CDN 的流式请求（0.4.1 起按 CDN 自带的回源头自动识别 Cloudflare 与阿里云 ESA，见 §2.1）：请求开始 15 秒后仍无输出时提交 SSE 头并每 10 秒发心跳，避免 CDN 空闲超时；官方 OAuth / setup-token 账号不启用 |
| 停滞检测 | `stall` | 已输出后长时间无数据（缺省 120 秒）判定断流并结束请求；图片生成豁免。OpenAI 上游给出响应头后、输出前完全静默时，由首输出超时（按推理档位 180/200/220 秒）换号，不再在 180 秒处被停滞检测以终止帧结束（0.4.2）。停滞的回合计入会话失败回合。覆盖 OpenAI Responses、Anthropic 原生，以及 Chat Completions 与 Messages 经 OpenAI 兼容转换的流（0.4.2；未覆盖的路径见“已知限制”） |
| 断开计费 | `disconnect` | 客户端在任何语义输出前断开：立即取消上游、不写用量、不计费，记入未送达表（首包前心跳、排队等待槽位或用户消息队列时发出的 ping 不算输出）；部分输出后断开：最多再读 180 秒并按已得用量计费。0.4.2 起所有流式路径在第一个真实内容前写出的开场事件与心跳都不算输出；非流式与图片 JSON 以响应体是否写出为准，见 §4.4 |
| 错误语义与防风暴 | `errors` | 无号 503 带 `Retry-After`（2–30 秒）；配置错误 503 带 `Retry-After: 60` 与 `x-should-retry: false`；2 秒负缓存与同一 Key、同一路由与分组、同一模型的重复 503 快速返回，都只针对对整个分组成立的结论（组内没有平台匹配、支持该模型且无罚的账号，或配置错误），只对某个会话成立的“无号”（含末位兜底只因新会话撞上会话数上限而失败）不缓存、不计数；同一会话连续失败的回合封顶。0.4.4：合成的 `response.failed` 按情况带 `error.headers.retry-after`（`failed_frame_retry_after`）；Codex 请求的无号 `Retry-After` 上限 10 秒（`codex_retry_after_cap_seconds`）；这两项还要求 `errors.enabled`。Anthropic 协议下网关自身的失败用 `api_error`；Gateway 与 OpenAI→Messages 的专用耗尽出口也走公共重试提示与回合封顶 |
| 无号兜底分组 | `fallback_group` | 分组无可用账号时由其配置的兜底分组承接，主分组稳定 120 秒后切回。兜底只是内部路由：价格、额度与限额、权限都按原分组算（0.4.6 起，见 §4.3） |
| 仅流式能力（0.4.4） | `capability` | 非流式请求遇到上游“只接受流式”的 400 时，记住该账号对该协议的非流式请求不可用（`learn_ttl_seconds`，缺省 6 小时），本请求立即换号，不罚账号；之后会因此失败的非流式请求（上游协议相同、按非流式请求上游的路径）暂不选它；流式请求，以及网关本来就改发流式的请求，不受影响。本请求换号还需要 `failure.decision_v2` 开启，否则只学习、本请求照旧返回 400 |
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
| 无可用账号 | 503，`Retry-After` 为 2–30 秒（带抖动），消息附 “Please try again in Ns.”。Codex 请求（按 UA 或 `originator` 识别）上限 10 秒（0.4.4，`errors.codex_retry_after_cap_seconds`）：Codex 会按它静默等待。非流式请求只因组内账号都被学为仅流式而无号时，原因码为 `capability_stream_only`，消息为 `No available account in this group supports non-streaming requests; retry with "stream": true`，不带 `Retry-After`，`x-should-retry: false`。运维记录中的无号诊断，只有本请求失败的尝试全是仅流式排除时才在末尾附 `(capability_stream_only)`；同一请求里先前的能力排除不再给限流等其他原因的无号贴上这个标记（0.4.5） |
| 配置导致的无号（分组仅限 Claude Code、渠道定价限制） | 503，`Retry-After: 60`，`x-should-retry: false`；Codex 请求的 `Retry-After` 同样不超过 `errors.codex_retry_after_cap_seconds`（0.4.4） |
| 模型不存在 | 保持原响应，不加重试头 |
| 余额被在途请求占用 | 429，`rate_limit_error` / `concurrent_balance_reserved`，`Retry-After` 约 5 秒 |
| 上游余额不足 | 普通用户看到 503；账号暂停期间单号分组的 503 带 `Retry-After`（暂停剩余时间，2–30 秒）；管理员在账号列表看到临时不可调度（原因 `balance:`）。管理员配置的错误透传规则命中时按规则返回 |
| 首包前心跳已提交后失败 | 不再改写状态码，改为协议终止帧：Responses 为 `response.failed`（`rate_limit_exceeded` 或 `server_error`），客户端此前只收到心跳时前面先补同一响应的 `response.created`（openai-python 等严格 SDK 要求流以它开始）；Chat 为 `data: {"error":{…,"status":N}}` 后跟 `[DONE]`；Anthropic 为 `event: error` |
| 合成 `response.failed` 的重试时间（0.4.4） | `errors.failed_frame_retry_after` 开启时写在 `error.headers.retry-after`（Codex 从这里读）：本请求还有健康账号时为 1 秒；组内没有健康账号、能算出最早恢复时间时为 2–10 秒，正文 “try again in Ns” 的 N 相同；恢复时间未知时不带（0.4.3 缺省提示 30 秒）；回合封顶、用户自身余额不足、内容拒绝不带，并去掉正文里的重试句（上游账号余额不足会换号，不算）；配置拒绝、鉴权类错误与其他不可重试的原因（模型不存在、`billing_service_error` 等，只有 429 与 5xx 才可能带）不给重试建议。重试建议只在实际写出 `response.failed` 时计算，以普通 JSON 结束的无号与耗尽出口不再计算（0.4.5）。HTTP 头尚未提交时同步改写 `Retry-After`。关闭时不带 `error.headers`，正文同 0.4.3（用户余额不足与内容拒绝仍不带重试时间，这两项只受 `errors.enabled` 控制） |
| 排队等待时已发出 ping，之后遇到计费等错误（0.4.4） | 按入站协议写终止帧：Responses 为 `response.created` 后跟 `response.failed`，Chat 为 `error` 后跟 `[DONE]`；不再写裸 JSON（不受开关控制） |
| 上游给出的等待时间（0.4.5） | `errors.failed_frame_retry_after` 开启、已提交的 Responses 流最终耗尽时：按组内快照算不出恢复时间（没有快照、快照过期，或只剩本请求已失败的账号），而最后一个可重试上游（原始状态 429 或 5xx）给出了有效的 `Retry-After`（正整数秒，或未来的 HTTP 日期），就把它算作已知恢复时间，向上取整并夹到 2–10 秒，写进 `error.headers.retry-after` 与正文 “try again in Ns”。被映射为 502 的 400、404，缺失、非法、非正或已过期的值，以及网关缺省的 30 秒，都不采用；还有健康账号时的 1 秒、按快照算出的恢复时间与终态抑制优先。修复 0.4.4 的已知缺陷 RA-1 |
| 上游开场后、正文前失败（0.4.4） | 可重试的失败（过载、服务忙、断流、读错）换号，客户端只看到成功那次尝试的开场：Responses 只有一个 `response.created`（新账号的 id），Chat 只有一个 role 分块，Messages 只有一个 `message_start`。兼容转换推迟开场期间按流内心跳间隔写注释行 `:`（与原生 Responses 一致），心跳不算输出、不妨碍换号；心跳已提交 200 后再耗尽时，以协议终止帧结束。`response.incomplete`、内容拒绝、只有 usage 的空回答照常返回，不重放 |
| 非流式请求被上游以“只接受流式”拒绝（0.4.4） | 例如 “This group only accepts streaming requests”：本请求换号，不计账号失败、不冷却；有效期 `capability.learn_ttl_seconds` 内，会因此失败的非流式请求暂不选该账号（范围见 §7）。`failure.decision_v2` 关闭时只学习，本请求照旧返回 400；本请求的失败尝试全部是这个理由时返回 503（不是 400），应答与上面“无可用账号”中仅流式一种相同；混有其他失败原因时按普通的耗尽处理。无号的负缓存按协议与流模式分开，能力阻断不计入快速失败，流式请求不受影响 |
| Gateway 入站经 Anthropic 上游的 Responses 与 Chat（0.4.5） | `response.created` 与 Chat 的 role 分块推迟到第一个内容或终态（`stream.prelude_staging`）。此前上游的可重试失败（`overloaded_error` 等流内错误、断流、读错、停滞）换号，客户端只看到成功那次尝试的开场；首事件截止（Anthropic 档位）延长到第一个内容。暂存期间上游的 `ping` 与注释行、以及按流内心跳间隔的保活，立即写成注释行 `:`（Chat 的上游 `ping` 仍是 `: ping`），不算输出。不可重试的流内错误（如请求过长、内容拒绝）不重放，先写开场再写终止帧（`stream.strict_terminal` 关闭时按 0.4.4 丢弃该事件，流照常以完成结束）。`stream.strict_terminal`：上游流内错误不再被丢弃；输出后断流、读错或停滞时，Responses 以 `response.failed` 结束（此前什么都没写时前面补 `response.created`），Chat 以 `data: {"error":…}` 后跟 `[DONE]` 结束，不再合成 `response.completed` 或 `finish_reason`；`message_delta` 已给出 `stop_reason`、只缺 `message_stop` 的流视同完成。合成的 `response.failed` 按 `RetryAdviceEligible` 决定是否带重试时间。客户端在第一个内容前取消：关闭上游，不换号、不计费；恰在第一次写出的同时取消、暂存期已为取消关闭上游时，随后的读错按客户端断开处理（按已得用量计费，不写终止帧、不记流错误、不计账号失败）；输出后断开：读完上游、按用量计费。输出后异常结束（流内错误、缺终态、读错、停滞，含客户端断开后上游再出错）按已读到的用量计费一次（`message_start` 的输入与缓存 Token 等），与原生 Messages 相同。两个开关都关闭时回到 0.4.4 |
| OpenAI Responses 透传（0.4.5，`stream.strict_terminal`） | 与原生路径相同的首输出截止（按推理档位，同时约束等待响应头）：截止前只有开场事件与心跳时换号（`first_output_timeout`）。停滞检测：输出前由截止接管，没有截止时换号；输出后以 `response.failed`（`stream_timeout`）结束。暂存期按流内心跳间隔写注释心跳 `:`，上游注释行立即透传，都不算输出；心跳写失败按客户端离开处理（不换号，截止到期也不换号，内容随后到来时读完用量）。客户端在第一个输出前取消时关闭上游，输出后断开则读完上游。关闭该开关时回到 0.4.4（不设截止与停滞检测，心跳由独立的保活协程写出）。不论开关：输出后断流（`response.failed`、缺终态、读错、停滞）按已读到的用量计费一次（以前不记），第一个输出前取消不计费 |
| 心跳帧 | Responses / Chat 为 SSE 注释行 `:`；Anthropic 为 `event: ping`。0.4.4 起排队等待并发槽位或用户消息队列时的心跳也按入站协议选择（以前 Responses 与 Chat 也会收到 Claude 风格的 ping） |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，并带 `X-XY-Upstream-Status: 502/504`。Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉 JSON 错误、`Retry-After` 和请求 ID；503 原样透传。直连请求保持原状态码。运维错误记录仍记原始的 502/504 |
| 换号耗尽的 429/5xx | 带 `Retry-After`：上游给出的值按 2–30 秒封顶，没有时取分组最早恢复时间 |
| 同一会话连续第 5 次耗尽失败（无号、换号耗尽、流内失败） | 终止应答：不带 `Retry-After`，`x-should-retry: false`，`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持）。Codex 对 5xx 的重试次数有上限（4 次传输层重试 × 6 次流层尝试，一个回合最多约 30 次请求），但按 `Retry-After` 等待的时长没有上限（0.158.0 起；更早的版本忽略 `Retry-After`）；封顶后不超过 5 次。会话真正成功一次或两次失败相隔超过 120 秒后重新计数 |
| 中转站插入的 `SSE-Keep-Alive` 空 delta 帧；0.4.4 起还包括 `response.created` 之前、`delta` 为空字符串或只有空白的 delta（第一个非空 delta 之后的空白属于正文，照常转发） | 直接丢弃，不转发、不算首输出（openai-python 遇到会报错，网关也会因此提前放弃换号）；但算上游存活，不会因此被判停滞。`response.created` 之前的非空 delta 是真实内容，照常立即输出，计入 `pre_created_delta`。缺省首字口径（`openai_ttft_mode=semantic`）下，用量记录的首字时间仍从第一帧算起，与 0.3.0 一致；`visible` 口径不算这类帧 |
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
| `stream.prelude_staging`（0.4.4） | 开 | 开关；兼容转换推迟开场（§1）；0.4.5 起含 Gateway 入站的 Anthropic 反向转换；0.4.6 起 `response.metadata` 在兼容转换里算开场还要求 `native_prelude_whitelist` 开启 |
| `stream.native_prelude_whitelist`（0.4.4） | 开 | 开关；原生 Responses 开场白名单扩展（0.4.6 起含 `response.metadata`，兼容转换随之一致）；关闭后回到 0.4.3 的首输出边界 |
| `stream.strict_terminal`（0.4.4） | 开 | 开关；raw Chat 的 `error` 后跟 `[DONE]` 判为失败（`error` 须是非空字符串，或带非空 `message`、`code`、`type` 的对象，或根上 `type` 为 `error`）；0.4.5 起 Gateway 入站反向转换的断流与流内错误按协议终止帧结束，并启用该路径的停滞检测；OpenAI Responses 透传的首输出截止与停滞检测 |
| `failure.decision_v2`（0.4.4） | 开 | 开关；分类修正，也是仅流式拒绝后本请求换号的前提 |
| `errors.failed_frame_retry_after`（0.4.4） | 开 | 开关；另需 `errors.enabled` |
| `errors.codex_retry_after_cap_seconds`（0.4.4） | 10 | 0–30，0 表示不另设上限；另需 `errors.enabled` |
| `capability.stream_only_learning`（0.4.4） | 开 | 开关 |
| `capability.learn_ttl_seconds`（0.4.4） | 21600 | 600–86400；只影响之后新学到的记录 |

完整字段与校验见 `backend/internal/resilience/settings.go`。

### 4.3 无号兜底分组

- 在分组编辑页选择“无号兜底分组”：须为同平台、启用、非订阅的其他分组；不能指向自身，不能成环。
- 触发条件：主分组没有任何无罚账号，且兜底组有无罚账号。判定只在请求入口进行一次，换号耗尽不跨组。
- 进入兜底后按 API Key 记忆（Redis，TTL `binding_ttl_seconds`）；主分组持续健康 `return_stable_seconds` 后切回，抖动的主分组不会来回切换。主分组恢复后（组内重新出现无罚账号）：从未转出的 Key 立即回到主分组；已转出、仍在请求的 Key 在主分组连续健康满 `return_stable_seconds` 后的第一个请求回来（计时从第一个看到主分组健康的请求开始）；已转出、空闲超过 `binding_ttl_seconds` 的 Key 记忆过期，下一个请求直接回主分组。会话粘滞按分组隔离，回到主分组后使用主分组自己的绑定，不会被留在兜底组。
- 计费与限额（0.4.6 起）：一次兜底请求的用户价格、额度与限额，等于原分组自己服务这次请求时的结果，兜底组只决定由谁服务、怎么服务。具体是：
  - 按原分组计价：用户在原分组的专属倍率、原分组倍率与高峰倍率、图片与视频倍率、分组与渠道定价、长上下文规则；计费模型也按原分组的渠道决定：原分组的渠道按请求模型或映射模型计费时照用；计费来源是上游模型、响应模型或缺省时，原分组的账号会怎样映射无从得知，按兜底账号实际服务的模型计。
  - OpenAI 的服务等级跟随兜底组（兜底组开启“强制 Fast”时上游收到 priority），计费规则按原分组。用户不因兜底多付钱：客户端没有请求 priority、原分组也不强制 Fast、priority 只是兜底组的“强制 Fast”造成的，按标准等级向用户计费，差价由平台承担（用量记录的服务等级与总成本仍是 priority，实际扣费按标准价，与“Fast 按标准价”同样记账）。客户端自己请求 priority 或原分组本身强制 Fast 时照常按 priority 计费；原分组开启“Fast 按标准价”时照此计费。
  - 分组 RPM 上限与“用户×分组”计数按原分组；余额预留的估算与计费同口径。
  - Live 与批量图片同样按原分组计价与授权（批量图片由兜底组的账号承接）；Live 通话归属原分组，主分组恢复后旁路连接仍然可用。
  - 利润门的售价按原分组估算（用户在原分组的专属倍率、原分组倍率与高峰倍率），利润率与安全余量取实际调度的兜底组，账号成本按实际服务的账号。
  - 账号侧（账号倍率、账号统计费用、份额调度的用量）按实际服务的账号与兜底组计。此前的兜底请求按兜底组计费，不追溯。
- 权限（0.4.6 起）：模型白名单按原分组校验（含 Responses WebSocket 的每个 `response.create`），拒绝时列出的也是原分组的模型；模型列表（`/v1/models`、`/v1beta/models`、`/backend-api/codex/models`、批量图片的模型列表等 GET 查询）不经过兜底，始终按原分组返回。系统提示词仍用兜底组的配置（它通常与服务该组的上游相配）。“允许图片生成”与“允许 /v1/messages 调度”两个开关：资格按原分组，能力按兜底组。原分组不允许时，返回与原分组自己服务时相同的权限错误；原分组允许、兜底组不允许时，按无号返回 503（Responses WebSocket 以 `no available account` 关闭），错误信息不提兜底组；两者都允许才放行。用户查询图片任务能力时只看原分组。
- 用量记录：`usage_logs.group_id` 为实际服务的兜底组，`origin_group_id` 为原分组（不做迁移，历史记录同样适用）。0.4.6 起兜底对用户不可见：用户的用量列表与详情只显示原分组（列表的 `group_id` 与 `group` 都是原分组；详情 `GET /api/v1/usage/:id` 的 `group_id` 是原分组，不加载 `group`，与改前相同），不返回原分组字段；用户按分组筛选、统计、趋势、模型分布与分组分布以及 CSV 导出都把兜底记录算在原分组下（按 `COALESCE(origin_group_id, group_id)`，查询先按用户限定，不影响性能）；用户的错误请求显示 Key 当前的分组。管理端照旧显示兜底：用量列表显示“兜底：原分组 → 实际分组”，筛选、统计与导出按实际服务的分组。管理端金额口径从本版起改变：用量里的倍率（`rate_multiplier`）与实际扣费（`actual_cost`）是原分组的价格，按服务分组汇总的营收也随之按原分组的价格计；标准成本（`total_cost`）与账号侧费用仍按实际服务计。可用渠道页与用户的分组接口不再返回兜底分组与倍率，兜底分组 id 只出现在管理端的分组接口，管理员在分组编辑页查看。

### 4.4 断开计费与滥用保护

- 未送达（任何语义字节前断开）：上游立即取消，不写用量，记入未送达表。
- 同一 Key 每小时未送达超过阈值（缺省 30）时视为滥用：不再取消上游，按上游成本计费，用量 `billing_mode` 为 `undelivered_abuse`。请求体还没收完就断开（阶段“上传请求中”，`upload`）照常记入未送达表，但不计入这个次数：上游没有被调用（0.4.3）。
- 送达口径（0.4.2）：
  - 所有路径：首包前保活心跳与排队等待槽位、用户消息队列时的 ping 不算送达。
  - 所有流式（`text/event-stream`）响应：在第一个真实内容前写出的开场事件不算送达。开场事件只有：SSE 注释行、`ping`、`keepalive`，Anthropic `message_start` 与空的 text/thinking `content_block_start`，Responses `response.created`/`in_progress`/`queued`（`stream.native_prelude_whitelist` 开启时按 §1 的开场判据，另含不带内容的 added 结构与 0.4.6 起的 `response.metadata`），只带角色的 Chat 分块。其余一律算内容，包括错误事件、`[DONE]`、`tool_use` 块与任何未知事件，因此识别不了的形式只会回到原来的计法。覆盖 OpenAI `/v1/responses`（含透传）、Chat Completions 与 Messages 兼容转换、Anthropic 原生与直通、Grok `/v1/responses`、图片 SSE 等全部流式路径；只改计数，事件照常、按时写出，换号判断不受影响。
  - 只收到开场事件与心跳就断开：算未送达，立即取消上游、不计费；收到内容后断开按部分输出处理（最多再读 180 秒，按已得用量计费）。
  - 非流式与图片 JSON：以响应体是否已写出为准（图片 JSON 的保活填充不算）。
- 未送达表的写入在后台进行，同时最多 16 个，失败重试一次；全部占满时这条记录不写并记 `undelivered_record_dropped`。计费不依赖这张表。

## 5. 观测

### 5.1 管理端

- 运维页“网关韧性”面板：当前实例最近 5 分钟至 1 小时的计数、未送达请求列表；计数与未送达列表按分组名显示。
- 选号解释：运维页面板底部可选任一分组（含原版调度），可选填模型与会话哈希，只读查看每个账号的硬、软档与原因（余额暂停为 `balance_exhausted`）、恢复时间、会话失败次数与是否回避，以及末位兜底账号与兜底分组决策；填了模型时，若该模型被分组白名单或渠道定价拒绝（所有请求在选号前即失败），在账号表上方提示。份额调度 Explain 的结果中附同样的韧性视图。0.4.4 起结果多一列“已学能力”（仅流式学习记录，任何查询都显示）；可选“请求类型”（Responses 或 Chat 的非流式请求）时，按该请求形态把被学为仅流式的账号标为跳过，原因 `capability_stream_only`，恢复时间即学习到期时间；不选则保持原来的整体健康视图。末位兜底账号不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。
- 账号列表：余额不足暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文；“恢复状态”立即放回。欠费但暂时不用的账号，关掉调度即可。
- 仅流式学习（0.4.4）：被学为只接受流式的账号在账号列表的状态列下方显示“仅流式（自动学习，剩余 X 小时）”，悬停说明涉及的协议，点“清除”即可；清除后非流式请求可以再选它。选号解释中被跳过的账号显示原因 `capability_stream_only`，恢复时间即学习记录的到期时间。
- 运维错误详情：每次上游失败带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`）。
- 告警：不新增余额类指标。RC 的迁移 276 预置过两条余额告警规则，由迁移 277 删除。
- 接口：
  - `GET /api/v1/admin/resilience/stats?window=15m`（窗口最长 1 小时，计数为单实例内存口径）；
  - `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=`（时间为 RFC3339）；
  - `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=`（只读；0.4.4 起每个账号带 `capability_learned`，`protocol=responses|chat` 与 `stream=true|false` 须成对出现，否则 400；`stream=false` 时按该请求形态排除仅流式账号（主分组与兜底分组都算），原因 `capability_stream_only`、`recover_at` 与 `capability_ttl_seconds`）；
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
| `pre_created_delta` | 上游在 `response.created` 之前发出的非空 delta（按分组、账号、平台，0.4.4；每次尝试至多计一次）；照常输出，用来观察这类中转的规模 |

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
| `request_id` | 请求 ID，与运维错误记录、访问日志里的请求 ID 相同，可用来关联；请求没有这个 ID 时用下一项的值 |
| `gateway_request_id` | 网关为每个请求生成的本地 ID（UUID），客户端无法指定 |
| `model` | 入站请求的模型，没有时为选号用的模型 |
| `delivered_bytes` | 已送达客户端的内容字节（开场事件与心跳不算，口径同 §4.4） |
| `ms_since_first_output` | 距第一次写出内容的毫秒数；还没有输出时为 0 |
| `prelude_release_event` | 结束开场暂存、开始向客户端输出的那个上游事件类型（记的是释放的决定，不保证写出成功）；没有暂存过时为空 |
| `prelude_release_ms` | 上一项距 `response.created` 的毫秒数；没有收到 created 时为 0 |

`keepalive_committed` 发生在输出之前，`delivered_bytes` 与 `ms_since_first_output` 总是 0。日志不含正文、Key 或完整会话值。

`*_failed` 结尾的事件表示存储或查询失败，功能会降级为原有行为。

## 6. 回退

- 单项关闭：在“网关韧性”卡中关闭对应能力，即时生效。各能力关闭后的行为见 runbook §6。0.4.4 新增的开关关闭后回到 0.4.3 的对应行为，不是 0.3.0（两处小例外：关闭 `failure.decision_v2` 后分类输入的证据补全仍保留，见下表；关闭 `errors.failed_frame_retry_after` 后用户余额不足与内容拒绝仍不带重试时间，见 §3）；关闭 `capability.stream_only_learning` 后已学到的记录不删除，按到期时间自然过期，期间重新开启仍然生效。
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
| 分类输入的证据补全（0.4.4） | 分类时依次取调用方给的 code、type、message，再取 `error` 字段，最后回落到 `response.error.*`。关闭 `failure.decision_v2` 回到旧的分类规则，但这项补全保留，个别运维分类可能与 0.4.3 不同 |
| 换号与写出的协议正确性（0.4.4） | 只有还没向客户端实际写出、客户端未断开且请求未取消时才换号；是否“已开始输出”按实际写出的字节判断；客户端取消或写出失败（客户端已断开）不再被记成上游首输出超时，也不计账号失败；0.4.5 起，开启暂存时客户端恰好在第一次写出的同时取消，暂存期为取消而关闭上游造成的读错误或缺终态的结束按客户端断开处理（按已得用量计费、不计账号失败），不再当作输出后的上游断流。关闭 `stream` 段的开关只回退暂存与终态策略，不撤销这些修正 |
| 心跳只提交网关自身的 SSE 头（0.4.5） | Chat 与 Messages 经 OpenAI 兼容转换、raw Chat 透传，在第一个输出之前用心跳提交 HTTP 200 时（暂存期，或关闭暂存时上游第一行之前），只写 `Content-Type`、`Cache-Control` 等网关自身的流式响应头；这次尝试的上游响应头（如 `x-request-id`）不再写出，之后换号成功也不会带上失败账号的头。没有心跳、直接输出时仍带上游响应头，与原生 Responses 的暂存一致。原生 Responses 有一项受控例外（O3）：第一次心跳尚未提交时，只另带本次暂存的 `x-codex-turn-state`，并同时记录账号溯源，由溯源加出站守卫保护，详见下一行。OpenAI Responses 透传则在心跳提交 200 时带出这次尝试的全部上游头（含 `x-request-id`、`x-codex-*` 与 `x-codex-turn-state`），与 0.4.4 相同。原因是长推理请求通常先发心跳，只写网关自身的头会让最终成功的请求也拿不到 `x-codex-turn-state`，Codex 因此丢失回合内的状态连续性，可能损害缓存命中。代价是心跳之后换号成功时，客户端拿到的仍是第一次尝试的头（已知差异）。turn-state 的溯源只记到客户端实际拿到的那次尝试：响应已被前一次尝试的心跳提交后，后续尝试自己铸造的 turn-state 既不下发也不记录，所以客户端下次回带第一次尝试的 blob 时，出站守卫照常按溯源剥离，不会发给其他账号的上游 |
| 原生 Responses 心跳保留 turn-state（0.4.5 O3） | 首个语义输出前，若原生路径第一次注释心跳将提交 HTTP 响应，且本次尝试的暂存头含 `x-codex-turn-state`，只提前写出这一个上游头，并在写心跳的同一时刻记录账号溯源。`x-request-id` 和 `x-codex-*` 用量头仍暂存到首个语义输出；响应已由心跳提交时不再补写。后续心跳不刷新溯源；换号到 B 后也不替换 A 的头或溯源，客户端下次回带时发往 B 的请求剥离该值、发往 A 的请求保留。上游没有 turn-state，或 CDN 首包保活、排队心跳已经提交响应时，不补头、不记录溯源；没有心跳则沿用首个语义输出时一次提交全部暂存头的行为。门票账号仍按其管理模型的门票覆盖出站 turn-state。此项属于协议修正，不新增开关，也不受总开关控制 |
| raw Chat 取消后的截断（0.4.4） | 账号不支持 `/responses` 时的 raw Chat 透传，判断客户端是否中止时也看请求是否已取消（`clientAborted` 含 `ctx.Err()`）：取消之后的截断按成功处理、按已得用量部分计费，不记上游失败；开启 `stream.strict_terminal` 时，取消之后收到的 `error` 同样不记上游失败、不换号。这项判断本身不受 `stream.strict_terminal` 控制 |
| 排队心跳按入站协议的格式（0.4.4） | 等待并发槽位或用户消息队列时，Responses、compact、Chat 收到 SSE 注释 `:`，Messages 收到 `ping` |
| 排队 ping 之后按协议写终止帧（0.4.4） | Gateway 入站的 Responses 与 Chat 在 HTTP 已提交后（如排队等待时已发出 ping）遇到计费、在途预留、模型等错误，写 `response.created` 后跟 `response.failed`，或 `error` 后跟 `[DONE]`，不再写裸 JSON；未提交时状态码与正文不变 |
| 心跳后命中错误透传规则（0.4.5） | OpenAI Responses 流（普通路径与透传）只写过心跳（上游注释、网关心跳或保活协程）、200 已提交时，流内 `response.failed` 命中管理员配置的错误透传规则，改为写 `response.created` 后跟 `response.failed`（同一 id，序号 0、1），带规则的类型与消息，是否带重试时间按规则状态码与 `RetryAdviceEligible`；以前在心跳后追加裸 JSON。未提交时仍按规则的状态码返回 JSON |
| OpenAI Responses 透传的有界暂存（0.4.5） | 第一个输出前的开场事件改用原生路径的暂存（8 MiB 上限，超过 64 KiB 写入已解除链接的临时文件），超出上限时换号；以前在内存中不设上限 |
| 上传中断记 499 | 读请求体时连接中断造成的 400 在运维记录里记为 499 |
| §9.8 续链被拒整轮重放 | Claude 请求分发到 GPT 中转、续链以笼统 4xx 被拒时去掉续链重放一次并关闭该会话续链（0.4.2 收窄判定，见 §3） |
| `/ready` 就绪检查 | 检查数据库与 Redis，见 §2.2（0.4.2） |
| 并发槽位的进程租约 | 新进程启动时保留仍有租约的其他进程的槽位，见 §2.2（0.4.2） |

管理端只读功能（选号解释中的模型限制提示等）也不受开关影响。

## 7. 已知限制

- 统计计数保存在进程内存，多实例部署时按实例分别统计，重启后清零。
- WebSocket 不挂连接级预算，建连首回合的换号仍按 `max_account_switches`；Gemini compat 服务未接入首事件守卫，由停滞检测兜底。
- Chat Completions 与 Messages 经 OpenAI 兼容转换时（0.4.2），首输出档位约束等待响应头和第一行上游数据。0.4.4 起开场事件推迟到第一个内容（`stream.prelude_staging`），首事件守卫随之延长到第一个内容，此前的失败可以换号；第一个内容写出之后的静默由停滞检测结束并记账，不再换号；关闭该开关时回到 0.4.2 的行为（第一行之后写出开场，不再换号）；如果此后上游只发中转站伪保活帧、不出内容，按 §7.8 算存活，停滞检测不会结束它。停滞检测未覆盖或不记账的路径：原生 Chat Completions 透传（账号不支持 `/responses` 时）没有停滞检测；CN Anthropic 原生上的 Responses 与 Chat 转换停滞时只结束请求、不冷却账号；Anthropic API Key 透传、Bedrock、CN Anthropic 原生 Messages 记账但不分输出前后，统一按 `stream_data_interval_timeout`；Antigravity 与图片按各自的规则。
- 0.4.4 的可重放前缀与明确终止只覆盖 OpenAI Responses（含透传的开场判定）、Chat Completions 与 Messages 经 OpenAI 兼容转换、raw Chat 透传。以下路径仍按 0.4.3 的开场与终止行为：Gemini 原生与兼容转换、Bedrock、CN Anthropic 原生、Antigravity（T2）。0.4.5 补上 Gateway 入站经 Anthropic 上游的 Responses 与 Chat 反向转换，以及 OpenAI Responses 透传的首输出截止与停滞检测。
- 兼容转换推迟开场期间的注释心跳会提交 200，此后的失败只能以带内终止帧告知客户端（与原生 Responses 相同）。
- 仅流式学习只认表中确定的结构码与文案（如 “only accepts streaming requests”、“stream must be true”）；其他措辞的同类拒绝仍按客户端错误返回。只覆盖 OpenAI 入站的 Responses 与 Chat 普通文本请求，以及 OpenAI、Grok 平台的账号；compact、图片、WebSocket、Messages 经 OpenAI 转换，以及 Gemini、CN、Bedrock 不学习、不过滤。只在上游协议与入站相同、上游流模式跟随客户端的路径上过滤；跨协议转换（如 Chat 转 Responses 上游）、按模型路由的路径与路由无法确定的 Grok OAuth Chat 不过滤。错误的 `param` 指向 `stream` 以外的参数，或请求体里本来就有这句话（例如写在提示词里）时不学习；`"param": null` 或空值不影响学习。学习结果存在账号 `extra`（`capability_learned_responses`、`capability_learned_chat`），绑定账号的上游配置：改了上游配置的账号不受旧记录影响；复制、创建、导入与导出账号都不带学习结果。
- 0.4.4 容量判据收紧后，“Codex is at capacity”与非 503 上的 “servers are currently overloaded” 不再算容量降载，会让账号进入软冷却并计入会话失败；Grok 重试耗尽的原始 429 不再转成无号 503，客户端看到 429。
- 0.4.4、0.4.5 把 `response.metadata` 算作输出（0.4.6 修复，见 §1）：OpenAI Codex 后端过载时先发 created、in_progress、`response.metadata`，再发 `server_is_overloaded`。这两个版本不换号，错误直接到客户端，日志为 `gateway.failover_suppressed_after_semantic_output`；没有开关能绕过。0.4.3 也同样不换号。0.4.6 起 `response.metadata` 也不再解除首输出截止：发了它之后只有心跳、超过档位（180/200/220 秒）仍无正文的尝试会换号，而以前只要不断有心跳就一直等到正文。缺省的语义首字时间 `first_token_ms` 仍从 `response.metadata` 起算，看不出这类请求离截止还有多远；需要时用 `openai_ttft_mode=visible`。关闭 `stream.native_prelude_whitelist` 回到以前的行为。
- 0.4.4 的已知缺陷 RA-1（0.4.5 已修复，见 §3“上游给出的等待时间”）：耗尽由上游带 `Retry-After` 的容量失败造成、且组里没有已受罚的账号时，0.4.4 合成的 `response.failed` 不带 `error.headers.retry-after`，正文也不再附 “try again in Ns”（0.4.3 的正文有）。仍在运行 0.4.4 时，需要 0.4.3 的正文可以把 `errors.failed_frame_retry_after` 设为 false。
- 原生 Responses 路径上，上游在 `response.created` 之前发出的非空 delta 照常先转发（网关不重排上游的真实内容），这类上游下严格 SDK 仍可能报 “Expected to have received response.created”。
- 仅流式学习记录绑定账号凭据：OAuth 类账号刷新令牌后会重新学习一次。学习粒度是“账号 + 协议”，不分模型：中转只对某个模型要求流式时，该账号所有模型的非流式请求都会被跳过，可以在账号列表手动清除。
- 断开阶段粒度为上传请求（0.4.3）、选号、等待响应头、首输出前、输出中五段。Chat Completions 与 Messages 经 OpenAI 兼容转换的路径，上游响应头到达后阶段不推进到“首输出前”：只写出开场分块（`message_start`、只带 role 的 chat 分块）就断开时，未送达记录与计数的阶段记为“等待响应头”。这只影响按阶段的统计，不影响是否计费、是否取消上游（两者只看是否写出过内容）。
- 兜底判断、末位兜底与无号 `Retry-After` 读取分组账号的进程内快照（2 秒）。本网关施加的冷却与余额暂停会立即刷新快照；其他来源（上游 429 限流、错误状态、管理员改动、其他实例）最多 2 秒后可见。末位兜底在派发前按账号当前状态复核所选账号，快照的滞后只影响兜底判断与 `Retry-After` 的估算。过期的快照在下一次写入或每分钟一次的会话标记清理中删除，不再常驻内存（0.4.5）。
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

