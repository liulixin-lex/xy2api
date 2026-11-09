# 网关韧性（Gateway Resilience）

xy2api 0.4.0 引入的一组网关能力，目标是减少用户可感知的失败：上游抖动、单号分组整体不可用、CDN 空闲超时切断、客户端断开后的误计费，以及缺少重试指引的 5xx。0.4.2 修正了两份生产就绪审查（2026-10-19）核实的问题，改动见 tasks“0.4.2”小节。0.4.4 是第二期：上游开场后、正文前的失败可以换号，分类只把真正的容量信号算作降载，失败帧按情况带重试时间，自动识别只接受流式的账号（方案 `openspec/changes/gateway-resilience-v2-20261021/`）。0.4.5 修复 0.4.4 的已知缺陷 RA-1，补齐 W1.2 的其余三条路径（OpenAI Responses 透传，以及 Gateway 入站经 Anthropic 上游的 Responses 与 Chat），并修复审查遗留的 P2（方案 `openspec/changes/gateway-resilience-045-20261021/`）。0.4.6 至 0.4.11 已发布，本文按 0.4.12 的行为描述，0.4.6 起的改动随文标注版本。0.4.9 的改动：OpenAI 首输出截止分热、冷尝试，计划内截止与慢失败冷却，判慢降序与会话迁移（见 §4.5）；`codex.rate_limits` 判为开场事件（见 §1）；无号 503 计入 SLA（见 §5.1）；首包前心跳按尝试恢复（见 §1）；无号 `Retry-After` 与失败帧共用恢复估算（见 §3）；非 OpenAI 账号的模型级冷却（见 §4.6）；Gemini 文本尝试截止（见 §4.7）。0.4.12 改进报错后的补救与路由：模型不可用与余额不足在各平台换号，余额暂停逐级加长，原生 Anthropic 与 API Key 直通在第一个内容前失败也能换号，客户端可见的错误按 Codex 与 Claude Code 的重试规则给出（见 §4.9）。升级须知见 §6。

- 方案与决策记录：`openspec/changes/gateway-resilience-20261015/`（proposal、design、tasks、runbook、staging-validation）；0.4.4 的改动见 `openspec/changes/gateway-resilience-v2-20261021/`。
- 全部能力由一份 JSON 设置控制，除协作消息明文通道默认关闭外，缺省开启，修改无需重启。关闭总开关后韧性能力全部停用、调度与错误处理按 0.3.0 逻辑运行；但有几项缺陷修复不受开关控制，见 §6。0.4.4 新增的开关单独关闭时回到 0.4.3 的对应行为，见 §4.2 与 §6。
- 本文面向运维与管理员，说明行为、配置、部署前提、观测与回退。

## 1. 能力一览

| 能力 | 设置段 | 行为摘要 |
|---|---|---|
| 失败分类与冷却 | `failure` | 上游瞬时失败（502/503/超时等）进入短冷却（缺省 30 秒），不再与账号级失败混用；上游 `Retry-After` 封顶。中转前面的 CDN 或代理写出的 5xx 错误页（Cloudflare 的 JSON 错误页、HTML 页面）按瞬时失败处理，即使文案提到 overloaded；“overloaded”“at capacity”只在 503、529、4xx 与流内事件上算容量降载（0.4.3）。已输出后的上游失败在同一账号 60 秒内达 3 次时，该账号进入同样的短冷却（池模式账号除外）。0.4.4（`failure.decision_v2`）：只有 `server_is_overloaded`、`slow_down`、529 与结构化的 overloaded 算容量降载；Grok 流空闲、raw 429、“The service is busy”“Too many pending requests”“Concurrency limit exceeded” 改为瞬时或限流，可以换到同一主机上的其他账号；余额等更强的证据优先。0.4.9：Anthropic、Gemini、Antigravity 账号不带 `Retry-After` 的瞬时失败改为按账号×模型逐级冷却（§4.6），不再第一次就冷却整个账号；带 `Retry-After` 时仍按账号冷却。Gemini 的普通 5xx 与 408 接入这套处理（以前不受罚），池模式、自定义错误码与临时不可调度规则的优先级不变。0.4.12：上游明确报告模型不可用（`MODEL_UNSUPPORTED`）与余额不足时，各平台都换号，见 §4.9 |
| 会话失败改绑 | `session` | 输出前失败在同一会话、同一账号上 10 分钟内连续 2 次后改绑；`session.post_output_rebind` 缺省开启，仅 OpenAI 账号的流内失败、读流出错或缺终态等已输出后的首次失败立即改绑，带密文片段的请求退回连击规则（§4.12）；Anthropic、Grok 等其他平台的输出后失败仍按原连击规则处理。两者都在缺省 10 分钟内避开该账号，客户端错误、内容安全拒绝与客户端断开不计，计划内首输出截止（0.4.9，§4.5）也不计。窗口按账号各自计算，同一会话其他账号的失败不会延长它（0.4.2）。绑定账号只是普通软罚（含 OpenAI 按模型的瞬时屏蔽）时保留绑定，本次由其他账号服务；同站断流暂停按 §4.13 改绑 |
| 同站断流暂停 | `same_site_hold` | 60 秒内同一上游主机至少 2 个 OpenAI 账号分别发生传输层断流，只暂停这些断流账号 300 秒；同号 3 秒内合并、暂停不延长；普通选号跳过，仍可作末位兜底，见 §4.13 |
| Claude 判慢让位 | `anthropic_latency_hold` | 缺省开启；按账号和实际出站模型记录粘性热尝试的语义首输出，持续偏慢时在同优先级内后排并可迁出会话，见 §4.5。关闭后状态失效，重新开启从新样本开始 |
| 末位兜底 | `last_resort` | 分组内没有无罚账号时，先转投兜底分组；否则先尝试本会话未暂避的候选，暂避账号留作最后选择；两类内部各自保持原排序：软罚账号按优先级、再按罚期结束时间排序（0.4.9起同优先级内 OpenAI 与 Claude 判慢期账号排在其他账号之后，见 §4.5），取第一个有空闲并发的账号继续服务，全部占满时排队等待排在第一的账号，避免单号分组整组不可用。门禁与正常选号相同（只放宽软罚），超过窗口费用或 RPM 硬上限的账号不会被选中；份额分组在两类内部保持原有份额计划和质量排序，按账号优先级与有效权重（负载因子乘自适应系数）选择，绑定账号只是普通软罚时不改绑（同站断流暂停除外）。候选来自 2 秒的分组快照，派发前按账号当前状态再核对一次，快照之后被停用、删除、移出分组或进入硬罚的账号不会被选中（0.4.2） |
| 余额不足暂停 | `balance` | 上游余额不足的账号暂停调度，期间按硬罚处理、不作末位兜底；到期后由真实请求重试。0.4.12 起时长逐级加长：首次 `pause_seconds`（缺省 300 秒），到期后仍不足依次为 3 倍、12 倍并封顶（缺省 5、15、60 分钟），成功或“恢复状态”后复位，见 §4.9；以前每次暂停同样时长。账号列表的“恢复状态”立即放回。账号已有更长的软冷却（如 403 计数冷却 10 分钟）时，改记为余额暂停并保留较晚的结束时间；暂停期间新的软冷却不会覆盖它（0.4.2）。不监控、不探测、不告警。普通用户只看到 503，余额细节只对管理员可见。Gemini 的 `RESOURCE_EXHAUSTED` 分钟级限额按限流处理，不触发暂停；Grok API Key 账号的余额字样 429 同样暂停，不再按中转给出的重置时间限流 |
| 请求预算 | `budget` | 文本请求最多尝试 4 次、首输出前总时长 240 秒（等待并发槽位也计入）；图片与视频不设首输出截止；WebSocket 不挂连接级预算。同号重试每号至多一次，账号级失败不重试；容量降载只切换一次且只切到其他上游主机。非流式请求的“首输出”就是整个响应，预算只决定能否再发起新的尝试，进行中的单次非流式请求不按预算截断（截断仍在生成的长回答只会多一次失败）；例外是 0.4.9起 Gemini 文本请求的单次尝试截止（缺省 60 秒，非流式到完整响应，只截非最后候选；Gemini 最后候选不设首事件截止，也不受剩余预算截断，见 §4.7）。分组里唯一能服务该请求模型的账号不受单次档位限制，用满剩余预算。0.4.9：OpenAI 流式文本请求的单次首输出截止分热、冷尝试（热 20/25/30/30 秒；冷尝试按请求体大小至少 45 或 75 秒）；非最后候选的截止不短于本请求已耗时；最后候选只受剩余预算约束；等待响应头与等待第一个内容共用同一截止时刻；非流式不受影响，见 §4.5。例外：Antigravity 平台在同一账号上的智能重试（上游给出的短重试等待）不计入尝试次数，只在剩余预算与 `Retry-After` 封顶内等待 |
| 可重放前缀（0.4.4） | `stream` | 第一个正文之前写出的只有开场事件时，上游失败仍可换号（三个开关都只要求总开关开启；下面三个元数据事件在兼容转换里还要求 `native_prelude_whitelist`）：原生 Responses 把 `response.queued` 与不带任何内容的 `output_item.added`、`content_part.added` 也算开场（`native_prelude_whitelist`；另按确切事件名包括三个元数据事件：0.4.6 起的 `response.metadata`（OpenAI Codex 后端过载时会在 created、in_progress 与它之后才发 `server_is_overloaded`）、0.4.9 起的 `codex.rate_limits`、0.4.12 起的 `codex.response.metadata`。它们与其他开场一样不解除首输出截止，之后只有心跳、迟迟没有正文的尝试到期换号（`first_output_timeout`）。以前它们算内容，之后的失败无法换号；`response.metadata` 在 0.4.3 至 0.4.5 还会提前解除截止，`codex.response.metadata` 在 0.4.11 及以前让首输出截止失效。其他 `codex.*` 与未知事件仍算内容。判定共用 `ClassifyStreamEvent`，Chat Completions 与 Messages 兼容转换同样适用，须同时开启 `prelude_staging`）；Chat Completions 与 Messages 经 OpenAI 兼容转换时，开场事件推迟到第一个内容或终态才写出（`prelude_staging`）；raw Chat 透传把 `error` 后跟 `[DONE]` 判为失败（`strict_terminal`；0.4.5 起 `error` 须是非空字符串，或带非空 `message`、`code`、`type` 的对象，或根上 `type` 为 `error`；`null`、空串或只含空白的字符串、`false`、`0`、`{}` 与各字段都为空的对象等占位值不算）：输出前换号，输出后按已输出后失败记账、原样转发错误。0.4.5：Gateway 入站经 Anthropic 上游的 Responses 与 Chat 反向转换同样推迟开场（`prelude_staging`，首事件截止延长到第一个内容）并明确终止（`strict_terminal`，见 §3）；OpenAI Responses 透传接入首输出截止与停滞检测（`strict_terminal`，见 §3）。0.4.12：原生 Anthropic 与 API Key 直通同样推迟开场（`prelude_staging`，见 §4.9）。不完整（incomplete）、内容拒绝、合法空回答与客户端断开不重放 |
| 首包前保活 | `keepalive` | 仅对经 CDN 的流式请求（0.4.1 起按 CDN 自带的回源头自动识别 Cloudflare 与阿里云 ESA，见 §2.1）：请求开始 15 秒后仍无输出时提交 SSE 头并每 10 秒发心跳，避免 CDN 空闲超时；官方 OAuth / setup-token 账号不发起首包前保活，已提交后接手时照常续拍。0.4.9：心跳按尝试恢复。收到上游响应头只暂停心跳，换号后的下一次尝试原地恢复同一个心跳，从请求开始起算的时钟、已提交状态与已发心跳字节都保留；OpenAI 同号重放在已提交后等头时也续拍，未提交时保持原有首拍时机；写出语义内容、终态、请求结束或心跳写失败后永久停止。以前第一次尝试收到响应头后心跳就停了，后续尝试等头期间没有心跳，可能被 CDN 空闲超时切断 |
| 停滞检测 | `stall` | 已输出后长时间无数据（缺省 120 秒）判定断流并结束请求；图片生成豁免。OpenAI 上游给出响应头后、输出前完全静默时，由首输出超时（按推理档位；0.4.9起热尝试 20/25/30/30 秒，冷尝试见 §4.5，0.4.8 及以前为 180/200/220 秒）换号，不再在 180 秒处被停滞检测以终止帧结束（0.4.2）。停滞的回合计入会话失败回合。覆盖 OpenAI Responses、Anthropic 原生，以及 Chat Completions 与 Messages 经 OpenAI 兼容转换的流（0.4.2；未覆盖的路径见“已知限制”）；0.4.9起含 Gemini 流式文本请求（§4.7） |
| 断开计费 | `disconnect` | 客户端在任何语义输出前断开：立即取消上游、不写用量、不计费，记入未送达表（首包前心跳、排队等待槽位或用户消息队列时发出的 ping 不算输出）；部分输出后断开：最多再读 180 秒并按已得用量计费；客户端已离开时不计入号的失败，排空期间的读流出错、缺终态、停滞、带内失败与流超时都不标记号失败、不记门槛软失败、不改绑、不冷却，只记 `resilience.client_gone_skip`（0.5.11）。0.4.2 起所有流式路径在第一个真实内容前写出的开场事件与心跳都不算输出；非流式与图片 JSON 以响应体是否写出为准，见 §4.4 |
| 错误语义与防风暴 | `errors` | 无号 503 带 `Retry-After`（2–30 秒）；配置错误 503 带 `Retry-After: 60` 与 `x-should-retry: false`；2 秒负缓存与同一 Key、同一路由与分组、同一模型的重复 503 快速返回，都只针对对整个分组成立的结论（组内没有平台匹配、支持该模型且无罚的账号，或配置错误），只对某个会话成立的“无号”（含末位兜底只因新会话撞上会话数上限而失败）不缓存、不计数；同一会话连续失败的回合封顶。0.4.4：合成的 `response.failed` 按情况带 `error.headers.retry-after`（`failed_frame_retry_after`）；Codex 请求的无号 `Retry-After` 上限 10 秒（`codex_retry_after_cap_seconds`）；这两项还要求 `errors.enabled`。Anthropic 协议下网关自身的失败用 `api_error`（0.4.12 起换号耗尽的容量降载也是，见 §3）；Gateway 与 OpenAI→Messages 的专用耗尽出口也走公共重试提示与回合封顶。0.4.9：无号 `Retry-After` 与失败帧的重试时间共用同一个恢复估算，并读取按模型的冷却；不可重试的错误不再带 `Retry-After`，见 §3 |
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
| 无可用账号 | 503，`Retry-After` 为 2–30 秒（带抖动），消息附 “Please try again in Ns.”。Codex 请求（按 UA 或 `originator` 识别）上限 10 秒（0.4.4，`errors.codex_retry_after_cap_seconds`）：Codex 会按它静默等待。非流式请求只因组内账号都被学为仅流式而无号时，原因码为 `capability_stream_only`，消息为 `No available account in this group supports non-streaming requests; retry with "stream": true`，不带 `Retry-After`，`x-should-retry: false`。运维记录中的无号诊断，只有本请求失败的尝试全是仅流式排除时才在末尾附 `(capability_stream_only)`；同一请求里先前的能力排除不再给限流等其他原因的无号贴上这个标记（0.4.5）。0.4.9：`Retry-After` 与合成 `response.failed` 的重试时间（见下文）共用同一个恢复估算：每个账号取仍有效限制中最晚的结束时间，再取各账号中最早的那个；计入按模型的冷却（OpenAI 的瞬时屏蔽与慢失败冷却、其他平台的模型级冷却，见 §4.5、§4.6），以前 HTTP 不读它们，按模型冷却几分钟时只估 2 秒。只看本请求有资格的账号：OpenAI 的 HTTP 应答用完整的选号资格（同 §4.5“最后候选”），失败帧只用不需要读库的资格；无号负缓存读同样的冷却，但仍按整个分组判断，不用本请求私有的资格。各出口的上限不变（HTTP 2–30 秒、Codex 10 秒、失败帧 2–10 秒） |
| 配置导致的无号（渠道定价限制等） | 503，`Retry-After: 60`，`x-should-retry: false`；Codex 请求的 `Retry-After` 同样不超过 `errors.codex_retry_after_cap_seconds`（0.4.4） |
| 分组仅限 Claude Code、请求不是 Claude Code 且没有兜底分组 | 403 `permission_error`，不带重试头，运维记录标为业务限制。0.4.12 起 Messages 与 Responses、Chat 一致；以前 Messages 返回上一行的 503，客户端会持续重试 |
| Claude OAuth/SetupToken 的非 Claude Code 客户端伪装 | 伪装请求在缺少 `temperature` 和 `top_p` 时补 `temperature=1`；Opus 4.7、4.8、5、5.5 不接受任何采样参数，因此这些型号不补。已有客户端采样字段保持原样。出站 `anthropic-version` 固定为 `2023-06-01`，不会透传客户端版本头，所以 `2026-10-21` 的全型号规则不适用于此路径 |
| 入站校验判定模型不存在 | 保持原响应，不加重试头 |
| 余额被在途请求占用 | 429，`rate_limit_error` / `concurrent_balance_reserved`，`Retry-After` 约 5 秒。0.4.12 起运维记录标为业务限制（含 Responses WebSocket），以前记为上游失败 |
| 排队已满或并发槽位已满（0.4.12） | 429 带 `Retry-After: 5`；已提交的 Responses 流在 `errors.failed_frame_retry_after` 开启时写在 `response.failed` 的 `error.headers.retry-after`；已有 `Retry-After` 或重试被抑制时不改。覆盖 Gateway、OpenAI 与 Gemini 入口、图片并发与 Live。Codex 收到不带 `Retry-After` 的 429 会直接放弃，以前这几个出口不带。API Key 额度、权限与回合封顶等终态 429 仍不带 |
| 上游余额不足 | 本请求换号（0.4.12 起各平台都换，不再先在同一账号上重试），耗尽时普通用户看到 503；账号暂停期间单号分组的 503 带 `Retry-After`（暂停剩余时间，2–30 秒）；管理员在账号列表看到临时不可调度，原因 `balance:exhausted[step=N]: <证据摘要>`（N 为暂停档位，见 §4.9）。管理员配置的错误透传规则命中时按规则返回。0.5.1：含订阅共享额度用尽（`subscription_sharing_usage_limit_exceeded`）；OpenAI 流内事件的余额码（`response.error.code` 或 `error.code`，不看正文）同样在第一个内容之前换号并暂停，之后不重放，只暂停账号，不计会话失败与输出后软冷却，见 §4.10 |
| Grok API Key 精确 422 | 仅 `api_error` 且文案完整等于 `Service temporarily unavailable`、没有请求错误 `code`/`param` 时换号；Responses、Chat 与 Messages 入口预算耗尽均返回 503，运维尝试保留上游 422 并记为 `TRANSIENT` |
| alpha search E5001 | API Key 的 alpha search HTTP 400 且 `error.code=E5001`、`error.type=server_error` 时换号；预算耗尽默认返回 502，回合封顶时为 503，运维尝试保留上游 400 并记为 `TRANSIENT`；不执行账号副作用或全局调度失败上报 |
| 上游报告模型不可用（0.4.12） | OpenAI 兼容网关的 Responses 原生、直通及 Chat 兼容转发输出前收到 HTTP 400/404 或 5xx，且结构化 `code=model_not_found` 或错误消息命中现有通用模型可用性短语（如 `model is not supported`、`model is unavailable`、`model not found`）时，按 `MODEL_UNSUPPORTED` 换号，给账号×实际出站模型记瞬时失败；首次只记数，第二次冷却 10 秒，后续沿用现有阶梯（§4.6）。不罚号、不上报调度失败，不写 30 分钟持久化模型限流。请求级拒绝优先，compact 本地回退仍先执行。其余既有路径中，400/404 带结构化模型错误码，或消息为 `unknown provider for model`、`unknown model`、`model not found`、`model is not supported` 且无更具体的错误码、`type` 或 `param` 时（数值错误码等于本次状态码不算更具体），仍沿用 30 分钟模型限流（§4.9）。无错误码、消息提到 model 的其他 404 只影响分类与换号。换号耗尽时沿用入站协议原有错误映射 |
| 首包前心跳已提交后失败 | 不再改写状态码，改为协议终止帧：Responses 为 `response.failed`（`rate_limit_exceeded` 或 `server_error`），客户端此前只收到心跳时前面先补同一响应的 `response.created`（openai-python 等严格 SDK 要求流以它开始）；Chat 为 `data: {"error":{…,"status":N}}` 后跟 `[DONE]`；Anthropic 为 `event: error` |
| 合成 `response.failed` 的重试时间（0.4.4） | `errors.failed_frame_retry_after` 开启时写在 `error.headers.retry-after`（Codex 从这里读）：本请求还有健康账号时为 1 秒；组内没有健康账号、能算出最早恢复时间时为 2–10 秒，正文 “try again in Ns” 的 N 相同；恢复时间未知时不带（0.4.3 缺省提示 30 秒）；回合封顶、用户自身余额不足、内容拒绝不带，并去掉正文里的重试句（上游账号余额不足会换号，不算）；配置拒绝、鉴权类错误与其他不可重试的原因（模型不存在、`billing_service_error` 等，只有 429 与 5xx 才可能带）不给重试建议。重试建议只在实际写出 `response.failed` 时计算，以普通 JSON 结束的无号与耗尽出口不再计算（0.4.5）。HTTP 头尚未提交时同步改写 `Retry-After`。关闭时不带 `error.headers`，正文同 0.4.3（用户余额不足与内容拒绝仍不带重试时间，这两项只受 `errors.enabled` 控制） |
| 排队等待时已发出 ping，之后遇到计费等错误（0.4.4） | 按入站协议写终止帧：Responses 为 `response.created` 后跟 `response.failed`，Chat 为 `error` 后跟 `[DONE]`；不再写裸 JSON（不受开关控制） |
| 上游给出的等待时间（0.4.5） | `errors.failed_frame_retry_after` 开启、已提交的 Responses 流最终耗尽时：按组内快照算不出恢复时间（没有快照、快照过期，或只剩本请求已失败的账号），而最后一个可重试上游（原始状态 429 或 5xx）给出了有效的 `Retry-After`（正整数秒，或未来的 HTTP 日期），就把它算作已知恢复时间，向上取整并夹到 2–10 秒，写进 `error.headers.retry-after` 与正文 “try again in Ns”。被映射为 502 的 400、404，缺失、非法、非正或已过期的值，以及网关缺省的 30 秒，都不采用；还有健康账号时的 1 秒、按快照算出的恢复时间与终态抑制优先。修复 0.4.4 的已知缺陷 RA-1 |
| 上游开场后、正文前失败（0.4.4） | 可重试的失败（过载、服务忙、断流、读错）换号，客户端只看到成功那次尝试的开场：Responses 只有一个 `response.created`（新账号的 id），Chat 只有一个 role 分块，Messages 只有一个 `message_start`。兼容转换推迟开场期间按流内心跳间隔写注释行 `:`（Codex 的 Responses 输出前改为完整 ping 事件，其他客户端仍用注释心跳），心跳不算输出、不妨碍换号；心跳已提交 200 后再耗尽时，以协议终止帧结束。`response.incomplete`、内容拒绝、只有 usage 的空回答照常返回，不重放 |
| 非流式请求被上游以“只接受流式”拒绝（0.4.4） | 例如 “This group only accepts streaming requests”：本请求换号，不计账号失败、不冷却；有效期 `capability.learn_ttl_seconds` 内，会因此失败的非流式请求暂不选该账号（范围见 §7）。`failure.decision_v2` 关闭时只学习，本请求照旧返回 400；本请求的失败尝试全部是这个理由时返回 503（不是 400），应答与上面“无可用账号”中仅流式一种相同；混有其他失败原因时按普通的耗尽处理。无号的负缓存按协议与流模式分开，能力阻断不计入快速失败，流式请求不受影响 |
| Gateway 入站经 Anthropic 上游的 Responses 与 Chat（0.4.5） | `response.created` 与 Chat 的 role 分块推迟到第一个内容或终态（`stream.prelude_staging`）。此前上游的可重试失败（`overloaded_error` 等流内错误、断流、读错、停滞）换号，客户端只看到成功那次尝试的开场；首事件截止（Anthropic 档位）延长到第一个内容。暂存期间上游的 `ping` 与注释行、以及按流内心跳间隔的保活，立即写成注释行 `:`（Chat 的上游 `ping` 仍是 `: ping`），不算输出。不可重试的流内错误（如请求过长、内容拒绝）不重放，先写开场再写终止帧（`stream.strict_terminal` 关闭时按 0.4.4 丢弃该事件，流照常以完成结束）。`stream.strict_terminal`：上游流内错误不再被丢弃；输出后断流、读错或停滞时，Responses 以 `response.failed` 结束（此前什么都没写时前面补 `response.created`），Chat 以 `data: {"error":…}` 后跟 `[DONE]` 结束，不再合成 `response.completed` 或 `finish_reason`；`message_delta` 已给出 `stop_reason`、只缺 `message_stop` 的流视同完成。合成的 `response.failed` 按 `RetryAdviceEligible` 决定是否带重试时间。客户端在第一个内容前取消：关闭上游，不换号、不计费；恰在第一次写出的同时取消、暂存期已为取消关闭上游时，随后的读错按客户端断开处理（按已得用量计费，不写终止帧、不记流错误、不计账号失败）；输出后断开：读完上游、按用量计费。输出后异常结束（流内错误、缺终态、读错、停滞，含客户端断开后上游再出错）按已读到的用量计费一次（`message_start` 的输入与缓存 Token 等），与原生 Messages 相同。两个开关都关闭时回到 0.4.4 |
| OpenAI Responses 权威失败终态（0.4.13） | 原生同步、异步与透传流收到 bare `error` 时继续等待；后续完整的 `response.failed` 写出且用量解析完毕，立即结束流，不再等待上游 EOF 或停滞定时器。仅有 bare 错误后 EOF 时仍只合成一个 failed；原生流后续收到 completed 时仍以成功终态纠正，确定性错误码保持原样 |
| OpenAI Responses 原生部分用量（0.4.13） | 实际写出语义字节后遇到 `response.failed`、读错或缺终态时，保留上游已报告的部分用量，按现有规则只入账一次；客户端输出后离开仍继续读取用量。没有真实用量时不按字符估算 Token。零字节失败写或只写出开场不返回部分结果；首输出前换号仍不返回部分结果；部分记账通过明确的不完整标记阻止成功回报和 403 计数复位，不转为成功，不清除 breaker 或余额暂停，不建立成功绑定，也不追补历史账单 |
| OpenAI Responses 透传（0.4.5，`stream.strict_terminal`） | 与原生路径相同的首输出截止（按推理档位，同时约束等待响应头）：截止前只有开场事件与心跳时换号（`first_output_timeout`）。停滞检测：输出前由截止接管，没有截止时换号；输出后以 `response.failed`（`stream_timeout`）结束。暂存期按流内心跳间隔保活：Codex 客户端写完整 `event: ping` / `data: {"type":"ping"}` 事件，其他客户端写注释心跳；上游注释行立即透传，都不算输出；心跳写失败按客户端离开处理（不换号，截止到期也不换号，内容随后到来时读完用量）。客户端在第一个输出前取消时关闭上游，输出后断开则读完上游。关闭该开关时回到 0.4.4（不设截止与停滞检测，心跳由独立的保活协程写出）。不论开关：输出后断流（`response.failed`、缺终态、读错、停滞）按已读到的用量计费一次（以前不记），第一个输出前取消不计费 |
| 心跳帧 | Responses 原生、直通和请求级 preOutput 在输出前向 Codex 客户端写完整 SSE ping 事件，其他客户端保留注释心跳；Chat 仍为 SSE 注释行 `:`；Anthropic 为 `event: ping`，0.4.12 起原生 Anthropic 与 API Key 直通在开场暂存期间写注释行 `: ping`（§4.9）。0.4.4 起排队等待并发槽位或用户消息队列时的心跳也按入站协议选择（以前 Responses 与 Chat 也会收到 Claude 风格的 ping） |
| 经 CDN 的请求遇到 502/504 | 改为 503，正文不变，并带 `X-XY-Upstream-Status: 502/504`。Cloudflare 会把源站的 502/504 换成自己的纯文本页面，丢掉 JSON 错误、`Retry-After` 和请求 ID；503 原样透传。直连请求保持原状态码。运维错误记录仍记原始的 502/504 |
| 换号耗尽的 429/5xx | 带 `Retry-After`：上游给出的值按 2–30 秒封顶，没有时取分组最早恢复时间。0.4.12：Codex 请求同样不超过 `errors.codex_retry_after_cap_seconds`（以前只有无号与失败帧受这个上限）；重试被抑制（回合封顶等）时不带。Gemini 入口在组内账号全部暂时不可用（限流、容量、瞬时失败、余额）时返回 503 并带同样的提示，以前按最后一次的状态返回 502 或 429（随 `errors`）；换号时保留上游响应头，以最后一个账号的 `Retry-After` 为准 |
| 不可重试的耗尽（0.4.9） | OpenAI 入口的 413 请求过大与 HTTP 续链被拒的 400（`previous_response_id` 只能由 OpenAI API Key 账号服务）不带 `Retry-After`，JSON 与 SSE 都清除前面尝试留下的重试提示；上游的 `Retry-After` 只在可重试的状态（429、5xx）上转发。以前会复制上游的值 |
| 同一会话连续第 5 次耗尽失败（无号、换号耗尽、流内失败） | 终止应答：不带 `Retry-After`，`x-should-retry: false`，`error.code` 为 `server_is_overloaded`，5xx 改为 503（429 保持）。Codex 对 5xx 的重试次数有上限（4 次传输层重试 × 6 次流层尝试，一个回合最多约 30 次请求），但按 `Retry-After` 等待的时长没有上限（0.158.0 起；更早的版本忽略 `Retry-After`）；封顶后不超过 5 次。会话真正成功一次或两次失败相隔超过 120 秒后重新计数 |
| 中转站插入的 `SSE-Keep-Alive` 空 delta 帧；0.4.4 起还包括 `response.created` 之前、`delta` 为空字符串或只有空白的 delta（第一个非空 delta 之后的空白属于正文，照常转发） | 直接丢弃，不转发、不算首输出（openai-python 遇到会报错，网关也会因此提前放弃换号）；但算上游存活，不会因此被判停滞。`response.created` 之前的非空 delta 是真实内容，照常立即输出，计入 `pre_created_delta`。缺省首字口径（`openai_ttft_mode=semantic`）下，只有带 `SSE-Keep-Alive` 标记的保活帧仍沿用原计时；created 前无标记的空白 delta 不计首 token。`visible` 口径不算这两类帧 |
| Claude 请求分发到 GPT 中转时，中转站拒绝续链（`previous_response_id`）并返回笼统的 4xx | 去掉续链按完整历史重放一次，并对该会话在该账号上关闭续链。0.4.2 起只认指向续链的应答：404/409、提到上一响应的文案，或 400/422 上的笼统中转文案（单独的 “Upstream request failed”、“could not be processed”、没有文案）；指名其他参数的错误（如 `temperature` 越界）、上下文超长、内容审核、图片、模型不存在等照常返回，不重放。按笼统文案猜测的重放计入请求预算的尝试次数，预算用尽时不重放 |
| 上游内容安全拒绝（如智谱 `request_check_blocked`、`content_policy_violation`） | 400 `invalid_request_error`，`code: content_policy_violation`，附脱敏后的上游原文；不重试、不换号，账号不受罚。只关闭 `errors` 时回到 0.3.0 的 502 文案。0.5.1：code 或 type 为 `content_policy`，以及 400、403、451 上的“内容政策”文案也按内容安全拒绝处理，带账号措辞（封禁、organization 等）的照旧；403 上的这类文案以前判为鉴权失败并罚号，总开关开启时不再罚号。运维记录标为业务限制，见 §5.1 |
| 上游会话状态拒绝（0.5.3） | S1/S2 的窄文案与专用状态码按 §4.12 统一识别为 `CLIENT_ERROR`，含无 type 包装的流内错误；不罚号、不计软失败。原生与直通 Responses 输出前的 S2/S3/S4 和校验通过的包装 400 使用下述 M3；其他状态拒绝保持请求级终止 |
| 上游无法解密或校验请求中的加密内容（0.5.1） | code 为 `invalid_encrypted_content`、`encrypted_context_invalid`，或文案为 encrypted content 加 “could not be verified”“could not be decrypted”“hydration failed” 之一、“encrypted function output content could not be decrypted”、“could not validate encrypted continuation context”，密文通常来自别的账号。按请求级处理：不罚 OpenAI 账号（临时不可调度规则与自定义错误码也不作用于它），不计会话失败与分组质量门槛的软失败；与上下文超长一样不受总开关影响。OpenAI 原生 Responses 在第一个内容之前遇到它时，若点名的是 reasoning（`rs_`）或 compaction（`cmp_`）条目，或没有点名条目但剥离确实改变了请求体，就删掉 reasoning 的 `encrypted_content`、丢弃带密文的 compaction 后同号重放一次；HTTP 400、502 与带内 `response.failed` 在同一账号尝试内合计一次。原生与直通 Responses 的输出前 S2/S3/S4 和校验通过的包装 400 走 M3：只替换 amsg/fco 的目标密文槽位，集合严格增长才同号重放；无法继续修复时回避当前号，最多使用一次独立状态换号额度。M3 终止未提交写 502，已提交写 `response.failed` / `server_error`，message 仅由固定拒绝原因及已校验的 item id 生成，不转出上游原文或密文回显。其他路径不能剥离时保持原有请求级失败出口。Codex 子 agent 优先回到父线程的账号，见 §4.10 |
| 上游明确点名的请求兼容问题（0.5.1） | 上游明确拒绝（结构化 code、param，或 “Unsupported parameter: X” 文案）时，删除请求顶层已有的 `top_p`、`metadata`、`temperature`、`frequency_penalty`、`presence_penalty`（原有 `max_output_tokens`、`truncation`）；输出上限、停止条件、模板、用量选项、缓存保留与身份类字段被拒时照原样返回。裸名 `prompt_cache_breakpoint` 只删 `input[i]` 与 `input[i].content[j]` 两层协议位置，不进入工具参数、输出或用户 JSON。`tool_search_call` 的 `arguments` 被拒为非对象时，只有字符串能严格解析为 JSON 对象才改成对象（保留原始字节），同一请求内满足条件的同类条目一并转换，其余照原样返回。修复后同号重放，每个请求合计最多 6 次，同一账号尝试内同一请求体不重放第二次；HTTP 错误以及原生 Responses、Chat Completions 转换流在客户端第一个内容之前收到的带内 `response.failed` 都适用；Chat 使用同一字段修复入口，HTTP 与带内修复共用请求体去重和每请求 6 次预算，先关闭旧流，再恢复已经提交的输出前心跳后原号重放；重放等头期间沿上次写出时间续拍，新流接管时不重复拍（`backend/internal/service/openai_gateway_chat_completions.go:468`）。失败尝试的暂存与错误帧不发给客户端。客户端已经开始输出后保持原有出口。已知范围：Chat 裸 `error` 事件和非流式缓冲路径尚未接入带内字段修复，仍沿用既有出口；`/v1/messages` 兼容路径不接入。不认识的 400 照旧返回 |
| content part 缓存提示被明确拒绝（Q7） | 上游 400 明确点名 `input[i].content[j].cache_control` 为未知或不支持的参数后（支持无引号路径后接句号／逗号和续句，不匹配 `.ttl`／`.type` 子字段），才一次性删除该请求所有 content part 上的 `cache_control`，原号重试一次。沿用上一行的字段修复入口、请求体去重和共享预算；Chat Completions 转换路径从原始 `messages[i].content[j]` 剥离，避免转换时重新带回。顶层、消息级、工具参数和用户 JSON 中的同名字段均保留；首次转发保留缓存提示，支持显式缓存的上游继续使用它。其他字段被拒时不删除该提示；剥离后仍被拒则沿用既有终止出口，不重复剥离。实现：`backend/internal/service/openai_responses_rejected_field_retry.go:349` |
| Codex Alpha Search 的端点不支持（0.5.1） | API Key 账号返回 404、405，或返回 400 且消息含 `do not support the requested endpoint`、`not encrypted history or tool items` 时，按端点不支持换号，不写账号错误状态；所有候选都这样失败时按换号耗尽返回 502 |
| Anthropic 中转在流内报 `rate_limit_error`（如并发上限） | 输出前可换号；耗尽时 429 带 `Retry-After`，不再是 502“访问被禁止”。`overloaded_error` 耗尽时为 503 `api_error`（0.4.12，见下一行；以前为 529），`api_error` 为 502 |
| Anthropic 协议下网关自身的无号与瞬时失败（0.4.4） | 错误 `type` 统一为 `api_error`（含只有 code 的错误体），状态码与正文不变。0.4.12 起上游的容量降载（`overloaded_error`、529）换号耗尽后也返回 503 `api_error`，消息里内嵌的 `overloaded_error` 字样一并改写；已提交的流写 `event: error`，类型同样为 `api_error`。覆盖 Gateway 与 OpenAI→Messages 的耗尽出口、错误透传规则的提前返回、原生 Anthropic 与 API Key 直通的流内终止。Claude Code 把任何 `overloaded_error` 当作 529 计数，连续 3 次就停止或切换模型；以前上游真实返回的 `overloaded_error` 会原样到达客户端。运维记录的分类仍为 `CAPACITY_SHED`，原始响应体保留；限流、权限与回合封顶的语义不变。关闭 `errors` 时不做这两项改写，回到 0.4.3 |
| OpenAI Responses 本地合成的失败（0.4.12） | 网关在原生 Responses 流里自己判定的失败（上游单行超长、输出后读错、停滞）以 `response.failed` 结束（`server_error`，沿用本响应的 id、递增的序号与已得用量），可重试时带 `error.headers.retry-after`，随后关闭上游。以前写顶层 `event: error`，Codex 会忽略它、只在断流后报错，原因与重试时间丢失。上游自己发出的错误事件照原样处理 |
| 客户端看到的上游错误消息（0.4.12） | 返回给客户端的错误消息去掉上游组织、工作区、项目与账号的标识（如 `org-…`），`code`、`type`、`param`、重试头与 response、tool 的 id 保留；运维记录仍保存受控的原文 |
| API Key 中转拒绝回放 `web_search_call` 历史（“response protection is unavailable”） | 同号补 web_search 声明重放一次，成功后该账号 6 小时内同类请求直接补声明；仍失败时 502 带原因，不换号，计入会话失败与回合封顶 |
| 请求的模型不在分组白名单 | 404，消息附本分组白名单的前 10 项 |

Responses 空白占位：`response.output_text.delta` / `.done`、output_text 的 `response.content_part.added` / `.done`、assistant message 的 `response.output_item.added` / `.done` 逐事件按自身载荷判定，文本经 Go `strings.TrimSpace` 为空时暂存，不提交、不停止首输出截止、不计首 token。空 delta 必须存在且为字符串；refusal、非文本 part、未知或畸形载荷立即算输出，不依赖 item 状态表。原生与透传复用尝试级有界暂存；任一可见事件先按原序冲刷全部暂存，再发送该事件，字节与既有输出路径的协议变换结果相同。输出前失败丢弃全部未提交帧（含 created/in_progress），再走失败处理。

本地断流时，已经判为可见但尚缺 SSE 结束空行的末尾事件仍按既有输出路径冲刷，随后补齐原有边界并发送合成 `response.failed`；截断 JSON 与未知载荷同样保留。只有纯开场、空白占位且没有待冲刷可见事件时，才丢弃暂存。

Responses 输出前的原生、直通及请求级 preOutput 共用客户端心跳选择：按既有 originator / User-Agent 判定为 Codex 的请求发送完整的 `event: ping\ndata: {"type":"ping"}\n\n`，每拍派发一个 SSE 事件以重置客户端空闲计时；其他客户端保留注释心跳，避免要求首事件为 created 的 SDK 拒绝该流。不会为此提前释放 created。心跳仍扣除输出字节、不计首输出，停拍与帧边界保护保持；上游注释原样处理，Anthropic 与 Chat 的心跳不变。

输出前只有一个心跳时钟：请求级心跳与原生、guarded 直通、Chat、Messages 及兼容转换流内心跳交接时接续上次写出时间，换号后也按上次心跳续拍。原生路径根据下游写出时间安排下一拍；直通读泵在交接后按周期派发，不按相邻 tick 的时间差二次节流；Chat、Messages 与兼容转换流同样按实际下游写出时间重设下一拍。上游注释继续即时透传并扣除心跳字节，但不会推迟直通网关定时 ping。输出开始、客户端断开或失败终态发出后仍停拍。

Chat/Messages 兼容转换只在首次提交前暂存解码后的开场与空白事件，按上游载荷累计限制为 8 MiB。首个可见事件或成功终态到达时按序转换并冲刷；失败丢弃。提交后空白 delta 照常转换，保留前导空格和词间空格；超出暂存上限按既有输出前失败换号。

输出前的 `response.failed`、`error` 与失败 `response.incomplete` 丢弃整段暂存，终态本身沿既有失败处理发出，不计首 token、`FirstOutput` 或 `SemanticOutputAt`。`incomplete_details.reason=interrupted` 按成功终态处理，保留暂存顺序；真实内容已输出后的行为不变。

占位白名单允许文本 part、delta、done 的 `logprobs: []`、delta 的字符串 `obfuscation`、message item 的字符串 `phase`；reasoning 的 `encrypted_content: null` 等同缺省。白名单外的额外字段按递归空值规则判定：`null`、空字符串、数字、布尔，以及子项全部递归为空的数组或对象均等同缺省，不豁免其中的结构字段名；非空额外字符串（包括仅含空白的字符串）仍按可见内容处理。额外字段内部的对象／数组嵌套最多允许 32 层（不计事件和 item 外壳），超过上限立即停止递归，按非空、可见内容处理。已提供的结构字段校验类型，参与判定的对象拒绝重复键，未知或歧义形状按可见内容处理。`response.metadata` 在既有白名单开启时仍是开场，原生、直通、兼容转换与首输出观测共用此判定。

`response.completed` 通过既有静默拒绝守卫后，按成功终态冲刷并发送；原生（含 Grok）与直通仍对尚无语义输出、output/usage/error 都为空的 completed 换号（#5009/#7774），空白不算已见语义输出。“已见语义输出”只由非终态事件累计；终态的 response 对象只检查重复键和 output 类型，`text` 配置对象、`metadata` 等其余字段不按 part/delta 的字段类型校验，正文仍按 output 判断。有 output 或 usage 的合法空回答保留；扫描 `response.output` 决定首 token、delivery 的 `FirstOutput` 与 `SemanticOutputAt`。仅空白的成功响应不进入 `ReportOpenAIAttemptSuccess` 的首输出延迟样本，`attempt_first_output_ms` 也为空。delivery 开场分类仍依赖缺省开启的 `stream.native_prelude_whitelist`；兼容转换还依赖 `stream.prelude_staging`。首字档位、末位候选和 240 秒总预算不变。

`response.created` 前被既有过滤器丢弃的无标记空白 delta 仍更新存活计数和 stall 活动时间，但不再计入 semantic 首 token；有 `SSE-Keep-Alive` 标记的保活帧保留原有计时行为。

## 4. 配置

### 4.1 入口

- 管理端“系统设置 → 网关”中的“网关韧性”卡：顶部为总开关；每项能力一张卡片，放该能力的开关、说明与常用数值（例如请求预算的最多尝试次数与首输出前时长，首包前保活的延迟、间隔与边缘标记），宽屏每行两张；底部的高级选项可展开编辑完整 JSON。0.4.4 新增三张卡片：“失败分类修正”（`failure.decision_v2`）、“流式开场与终止”（`stream` 段三个开关）、“仅流式账号学习”（`capability` 段）；`errors` 段的两项放在“错误语义与防风暴”卡片里。说明文字写明各开关的依赖。
- 接口：`GET/PUT /api/v1/admin/settings/gateway-resilience`。`PUT` 提交的字段合并到当前设置（`budget.model_overrides` 若出现则整体替换），未知字段拒绝，服务端校验全部范围，未通过返回 `GATEWAY_RESILIENCE_SETTINGS_INVALID` 且不生效；响应同时返回当前值与缺省值。设置存在 `settings` 表的 `gateway_resilience_settings`，0.4.4 的新字段也在这一份文档里。
- 设置变更在保存的实例上即时生效，其他实例每 10 秒刷新一次；读取失败或存量文档非法时保留最后一次有效值。

### 4.2 常用字段（缺省值与范围）

| 字段 | 缺省 | 范围 |
|---|---|---|
| `session.post_output_rebind` | true | 布尔；仅 OpenAI 账号，依赖 `session.enabled` 与总开关 |
| `same_site_hold.enabled` | true | 布尔；依赖总开关 |
| `collaboration_plaintext.enabled` | false | 布尔；依赖总开关与模型/账号资格，见 §4.14 |
| `collaboration_plaintext.models` | `[]` | 最多 16 条；每条去空白后 1–128 字符；trim 后不分大小写去重，nil 返回 [] |
| `budget.max_attempts` | 4 | 1–10 |
| `budget.pre_output_budget_seconds` | 240 | 60–290 |
| `budget.first_output_timeout_seconds` | default 20 / high 25 / xhigh 30 / max 30（0.4.9起，按生产 48 小时流式首字分布约 p97 校准；是热尝试的档位，冷尝试另有下限，见 §4.5；0.4.8 及以前为 180/180/200/220） | 5–预算 |
| `budget.anthropic_first_event_timeout_seconds` | 90（Anthropic；0.4.9起 Antigravity 也读它） | 5–预算 |
| `budget.gemini_first_event_timeout_seconds` | 60（0.4.9起只管 Gemini 平台的文本尝试截止，见 §4.7；0.4.8 及以前为 90，并同时用于 Antigravity） | 5–预算 |
| `budget.same_account_retry_max` | 1 | 0–5 |
| `budget.gemini_same_account_retries` | 1（Gemini 普通瞬时失败的同号重试，见 §4.6） | 0–5 |
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
| `balance.pause_seconds` | 300（0.4.12 起为首次暂停的时长，后两档为 3 倍、12 倍，见 §4.9） | 30–3600 |
| `stream.prelude_staging`（0.4.4） | 开 | 开关；兼容转换推迟开场（§1）；0.4.5 起含 Gateway 入站的 Anthropic 反向转换；0.4.12 起含原生 Anthropic 与 API Key 直通（§4.9）；0.4.6 起 `response.metadata` 在兼容转换里算开场还要求 `native_prelude_whitelist` 开启，0.4.9起的 `codex.rate_limits` 相同 |
| `stream.native_prelude_whitelist`（0.4.4） | 开 | 开关；原生 Responses 开场白名单扩展（0.4.6 起含 `response.metadata`，0.4.9起含 `codex.rate_limits`，0.4.12 起含 `codex.response.metadata`，兼容转换随之一致）；关闭后回到 0.4.3 的首输出边界 |
| `stream.strict_terminal`（0.4.4） | 开 | 开关；raw Chat 的 `error` 后跟 `[DONE]` 判为失败（`error` 须是非空字符串，或带非空 `message`、`code`、`type` 的对象，或根上 `type` 为 `error`）；0.4.5 起 Gateway 入站反向转换的断流与流内错误按协议终止帧结束，并启用该路径的停滞检测；OpenAI Responses 透传的首输出截止与停滞检测 |
| `failure.decision_v2`（0.4.4） | 开 | 开关；分类修正，也是仅流式拒绝后本请求换号的前提 |
| `errors.failed_frame_retry_after`（0.4.4） | 开 | 开关；另需 `errors.enabled` |
| `errors.codex_retry_after_cap_seconds`（0.4.4） | 10 | 0–30，0 表示不另设上限；另需 `errors.enabled` |
| `capability.stream_only_learning`（0.4.4） | 开 | 开关 |
| `capability.learn_ttl_seconds`（0.4.4） | 21600 | 600–86400；只影响之后新学到的记录 |

完整字段与校验见 `backend/internal/resilience/settings.go`。

### 4.3 无号兜底分组

以下计价与展示口径从本次变更版本起生效。0.4.6 至本版本之前产生的兜底用量行仍按原分组计价；管理端浮窗对这些旧行也会显示新文案“按兜底分组计费”，这是已知显示差异，历史金额不会追溯调整。

- 在分组编辑页选择“无号兜底分组”：须为同平台、启用、非订阅的其他分组；不能指向自身，不能成环。
- 触发条件：主分组没有可服务成员，且兜底组有可服务成员。双方用 `groupMemberServable` 检查平台、硬状态、实时利润门、韧性软罚与质量否决。判定在认证后、模型解析前按组执行一次，不看请求模型；原组有可服务成员但都不支持模型时不触发兜底，平台资格复用调度器规则，Anthropic/Gemini 组计入已启用混合调度的 Antigravity 成员，组合组接受具体平台成员。换号耗尽不跨组。
- 进入兜底后按 API Key 记忆（Redis，TTL `binding_ttl_seconds`）；主分组持续健康 `return_stable_seconds` 后切回，抖动的主分组不会来回切换。主分组恢复后（组内重新出现可服务成员）：从未转出的 Key 立即回到主分组；已转出、仍在请求的 Key 在主分组连续健康满 `return_stable_seconds` 后的第一个请求回来（计时从第一个看到主分组健康的请求开始）；已转出、空闲超过 `binding_ttl_seconds` 的 Key 记忆过期，下一个请求直接回主分组。会话粘滞按分组隔离，回到主分组后使用主分组自己的绑定，不会被留在兜底组。
- 利润门成本与运维分组可用数共用判定，预览接口 `GET /api/v1/admin/groups/:id/profit-control-preview?margin=` 返回 `{members, schedulable, passing, ceiling}`，详见[分组质量门槛](GROUP_QUALITY_GATE.md)。
- **价格按服务分组**：请求在兜底组服务时，分组倍率、用户在兜底组的覆盖倍率、高峰系数、图片与视频价格/倍率、长上下文规则、渠道定价与计费模型，都取兜底组。兜底组的“强制 Fast”和“Fast 按标准价”规则照常生效，不再因兜底而补差价。
- **授权与限额按原分组**：`BillingAPIKey` 保持认证时的 API Key，只用于模型白名单与模型列表、图片与 `/v1/messages` 资格、分组 RPM 与“用户 × 分组”计数、API Key 额度和余额检查。`ServingPricingKey` / `ServingPricingGroup` 代表中间件改写后的服务 Key 与分组，用于价格计算。图片与批量图片资格仍按原分组检查，服务分组的能力不足时按无可用账号返回 503，不暴露兜底组信息；`/v1/messages` 同样分别检查原组资格和服务组能力。余额预留按兜底组估价，预留对象和计数仍归原组。
- Live 资格与旁路连接身份仍归原分组；主分组恢复后，已建立的 Live 连接继续可用。
- WS 每个 turn 固定原组授权快照与兜底组计价快照；价格组按服务分组 ID 独立刷新，账号与 Key 的身份不变。透传模式的第 1 轮不经过 BeforeTurn，也按连接的服务 Key 计价，与后续轮一致。cyber 策略拦截的计费行同样记 `origin_group_id`、按兜底组计价、限额归原组，用户侧不显示兜底组名称。
- 权限（0.4.6 起）：模型白名单按原分组校验（含 Responses WebSocket 的每个 `response.create`），拒绝时列出的也是原分组的模型；模型列表（`/v1/models`、`/v1beta/models`、`/backend-api/codex/models`、批量图片的模型列表等 GET 查询）不经过兜底，始终按原分组返回。系统提示词仍用兜底组的配置（它通常与服务该组的上游相配）。“允许图片生成”与“允许 /v1/messages 调度”两个开关：资格按原分组，能力按兜底组。原分组不允许时，返回与原分组自己服务时相同的权限错误；原分组允许、兜底组不允许时，按无号返回 503（Responses WebSocket 以 `no available account` 关闭），错误信息不提兜底组；两者都允许才放行。用户查询图片任务能力时只看原分组。
- 用量记录：`usage_logs.group_id` 为实际服务的兜底组，`origin_group_id` 为原分组（不做迁移，历史记录同样适用）；`rate_multiplier`、`actual_cost` 以及图片、视频金额按兜底组，账号侧成本与 `account_rate_multiplier` 不变。扣款仍针对原 Key 和用户，金额按兜底组价格。
- 显示与查询：用户用量仍只显示原分组，不返回兜底组名称；倍率与扣费显示实际计价结果。管理端用量主标签显示原分组，下方黄色“兜底 → 兜底组名”，浮窗说明“按兜底分组计费”；筛选、统计、导出仍按服务分组。历史 `usage_logs` 金额不追溯调整。

### 4.4 断开计费与滥用保护

- 未送达（任何语义字节前断开）：上游立即取消，不写用量，记入未送达表。
- 同一 Key 每小时未送达超过阈值（缺省 30）时视为滥用：不再取消上游，按上游成本计费，用量 `billing_mode` 为 `undelivered_abuse`。请求体还没收完就断开（阶段“上传请求中”，`upload`）照常记入未送达表，但不计入这个次数：上游没有被调用（0.4.3）。
- 送达口径（0.4.2）：
  - 所有路径：首包前保活心跳与排队等待槽位、用户消息队列时的 ping 不算送达。
  - 所有流式（`text/event-stream`）响应：在第一个真实内容前写出的开场事件不算送达。开场事件只有：SSE 注释行、`ping`、`keepalive`，Anthropic `message_start` 与空的 text/thinking `content_block_start`，Responses `response.created`/`in_progress`/`queued`（`stream.native_prelude_whitelist` 开启时按 §1 的开场判据，另含不带内容的 added 结构、0.4.6 起的 `response.metadata`、0.4.9起的 `codex.rate_limits` 与 0.4.12 起的 `codex.response.metadata`），只带角色的 Chat 分块。其余一律算内容，包括错误事件、`[DONE]`、`tool_use` 块与任何未知事件，因此识别不了的形式只会回到原来的计法。覆盖 OpenAI `/v1/responses`（含透传）、Chat Completions 与 Messages 兼容转换、Anthropic 原生与直通、Grok `/v1/responses`、图片 SSE 等全部流式路径；只改计数，事件照常、按时写出，换号判断不受影响。
  - 只收到开场事件与心跳就断开：算未送达，立即取消上游、不计费；收到内容后断开按部分输出处理（最多再读 180 秒，按已得用量计费）。
  - 非流式与图片 JSON：以响应体是否已写出为准（图片 JSON 的保活填充不算）。
- 未送达表的写入在后台进行，同时最多 16 个，失败重试一次；全部占满时这条记录不写并记 `undelivered_record_dropped`。计费不依赖这张表。

### 4.5 OpenAI 首输出截止与慢账号（含 Claude 判慢让位；0.4.9）

生产 48 小时数据显示，180–220 秒的首输出截止晚于中转链路约 125 秒的截断，从未生效；20 秒仍无首字的尝试，约一半到 30 秒仍无，约一成到 60 秒仍无，而健康账号上新开的尝试几秒内就有首字。0.4.9 收紧截止，并让持续变慢的账号让位。

适用于 OpenAI 平台账号上受请求预算管理的流式文本请求：Responses（含透传）、Chat Completions 与 Messages 经 OpenAI 兼容转换。非流式、图片与 WebSocket 不受影响。

**热与冷尝试**

- 热尝试：会话粘性命中，选中的就是该会话的绑定账号，不是容量溢出时的临时借用，且本请求此前没有上游失败。截止取 `budget.first_output_timeout_seconds` 的档位（default 20 / high 25 / xhigh 30 / max 30 秒，模型自定义档位同样适用）与输入大小下限中较大者：请求体 ≥256 KiB 至少 30 秒，≥512 KiB 至少 40 秒。该下限是初值，仍受剩余预算与最后候选规则约束；后续按首字超时尝试落库的真实 warm、body_bytes 与 effort 校准（§5）。
- 冷尝试：其余尝试，包括换号重试与同号重试、新会话首轮、会话迁移后首轮、不带会话标识的请求。截止取档位与请求体大小下限中较大者：请求体 ≥128 KiB 时至少 45 秒，≥256 KiB 时至少 75 秒。用 `budget.model_overrides` 设置了模型档位时，同样取较大者。
- 非最后候选的截止不短于本请求到这次尝试开始时已耗费的时间，再受剩余预算约束：上游都慢时，被截断的尝试逐次放宽；快速失败不拉长下一次尝试。
- 最后候选（末位兜底选中的账号、最后一次允许的尝试、组内唯一能服务该模型的账号）不按档位截断，只受剩余预算约束（缺省 240 秒）。判断“唯一”时，其他账号要通过本请求完整的选号资格（能力与 transport、隐私、利润门、母账号健康），HTTP 续链（`previous_response_id`）只算 OpenAI API Key 账号；软罚账号算替代，硬罚不算。缺能力的名义候选不会让唯一能服务的账号按档位被截断。
- 等待响应头与等待第一个内容共用同一截止时刻，从本次尝试开始计时，收到响应头后不重新计时。开场事件与心跳不解除截止，口径同 §1。

**计划内截止**

- 受请求预算管理的截止到期时，按计划内截止处理（失败原因 `planned_first_output_timeout`）。客户端行为不变：输出前换号，耗尽时响应体 `error.type` 仍为 `first_output_timeout`；运维记录中这次尝试的 Reason 仍为 `first_output_timeout`，计数仍记 `first_output_timeout`。
- 不施加账号级的流超时惩罚（会把整个账号设为临时不可调度），不计入 API Key 健康熔断，也不计入会话失败（不会因此触发会话改绑）。请求预算的尝试次数与账号×模型的慢失败照常记录，OAuth 与 API Key 账号都记。
- 上游真实返回的 504、524，断流、停滞与 WebSocket 照旧处理。

**慢失败冷却**

- 按账号与实际出站模型计。连续第 2 次慢失败起冷却 60 秒×2^(n−2)（1、2、4、8 分钟），封顶 10 分钟；两次慢失败相隔超过 30 分钟时重新计数。
- API Key 账号的普通瞬时失败（5xx 等）打断连续计数，按模型屏蔽处理：连续第 2 轮起 10、45、90、180、300 秒（0.4.8 及以前 10/45 秒封顶），与 §4.6 共用同一套阶梯、同轮保护与成功保护。任何失败都不缩短已在进行的冷却。
- 成功只在这次尝试的开始时间晚于最近一次慢失败时清除冷却与计数。开始更早的成功（例如卡住几分钟后才结束的长流）不清除。
- 冷却期间账号对该模型暂不被选中，与按模型的瞬时屏蔽同属软罚：组内没有其他可用账号时，末位兜底照常使用它。对所有选号方式生效。

**判慢让位**

- OpenAI 样本取热的 HTTP 流式文本尝试：成功时记录从这次尝试开始到第一个内容送达客户端的时间，开场事件与心跳不算（口径同 §4.4），不读用量记录的首字时间；首输出超时时，不区分是否计划内截止，以画像中的尝试开始至超时被判定时的已等待时长为准：已等待不小于判慢阈值时才记超时样本，作为真实首字耗时的下界；低于阈值时无法判断快慢，不计样本。两条路径共用映射后的实际出站模型键。冷尝试、非流式、没有匹配账号及有效开始时间的画像（包括 WebSocket）不采超时样本；原有慢失败冷却规则不变。
- OpenAI 按账号与模型做时间常数 5 分钟的指数衰减计数，尝试级可见首字 ≥10 秒计为慢；有效样本 ≥6 且慢占比 ≥30% 时触发。距上次让位结束 ≤30 分钟再次触发时，时长 3→6→12→15 分钟封顶，否则从 3 分钟开始。让位期间继续累积样本，触发时清零；重启清空级数。
- 判慢期不剔除账号，不计为罚。OpenAI 门槛组在质量档内后排，compact 能力分段保留；随机、会话和缓存偏好不得越档或越判慢分隔。未开门槛的原版选号仍排到候选末尾。到期只解除后排，不改门槛状态，也不表示“已恢复”。全部满载仍排队。
- 末位兜底同样：同优先级内先选非判慢期账号，再按恢复时间。判慢期不升级为硬罚。
- Claude 原生和 API Key 透传保持原阈值（35%、固定 3 分钟），不采用 OpenAI 的递增时长，按账号和最终出站模型独立计数。只记录成功完成的流式粘性热尝试，从发送尝试到首个语义输出；开场、心跳、空或签名 delta、错误和纯终态不算。换号、同号重试、溢出及迁出后的首轮不采样，转换入口不采样。模型路由、负载感知选号和等待队列只在同优先级段内把判慢账号后排，不跨优先级。

**会话迁移（仅原版选号）**

- 会话的绑定账号处于判慢期，且同组还有一个可用的其他账号（支持该模型与能力、未被渠道限制、不在判慢期、未被运行时屏蔽、通过利润门）时，本次请求在绑定仍指向该账号的前提下解除绑定，按正常选号选出新账号并改绑。迁移后的首轮按冷尝试，之后留在新账号上。
- 迁移前按实时负载确认至少一个这样的替代账号有空闲并发（并发上限不大于 0 视为不限）；负载查询失败、缺项或全部满载时保留原绑定，不白白丢掉缓存。这只降低白迁移的概率，确认之后空位仍可能被其他请求抢走。
- 单账号分组，或组内其他账号都在判慢期时，保留原绑定，不迁移。整体拥塞时不会成批迁移。
- 质量路由钉住的会话、带 `previous_response_id` 的请求与 WebSocket 不迁移。
- 带协作或工具输出密文片段的请求同样不做软迁移：`input[]` 中的 `agent_message.content[]` 或数组形态的 `function_call_output.output[]` 含 `type=encrypted_content` 时，由 `WithOpenAIEncryptedContentMigrationGuard` 设置与续链相同的保护，跳过质量迁出、判慢迁出及其低倍率目标改道。HTTP Responses（含透传）以及接受 Responses 形状 `input[]` 的 Chat Completions 在选号前设置，WebSocket 首帧选号前设置（连接内不重选号，客户端重连后的新连接首帧照样判定）。reasoning／compaction 的顶层密文不在此列。带上述密文片段的请求，P1-B 按 §4.12 退回连击规则；硬准入、其他失败换号、同站暂停、新会话放置、利润门和旧粘性逃逸照常。
- 开启门槛的 OpenAI 分组在原版和高级调度共用统一迁出：绑定号为 2 档或本模型判慢，且有满足恢复线、未判慢、硬准入及空位条件的目标时，先占目标槽位、再 CAS 原子改绑。候选先经过快照与批量空位过滤，再按档内顺序逐个 DB 复核和占槽；全部候选失败或 CAS 失败时保留原绑定，满载候选不做逐号 DB 复核；成功后同组同会话冷却 5 分钟（进程内 LRU，16384 键），失败类改绑不受限。续链、WebSocket、质量路由所有权与固定续轮豁免；固定账号只免质量降档，不免判慢迁出。详见[调度顺序](account-scheduling.md)与[分组质量门槛](GROUP_QUALITY_GATE.md)。
- Claude 迁出目标须通过正常候选准入、与 owner 同优先级且未判慢，并由新鲜负载快照确认空槽；模型路由内的迁出只考虑路由集合。属主删除出错、负载查询失败或没有合格目标时保留绑定；属主比较确认绑定已变时重新读取绑定，按新 owner 继续，读不到则正常选号。每个分组+会话成功迁出后 30 分钟内不再迁出，命中冷却时保留绑定并记 `cooldown`；冷却状态在进程内维护，多实例各自计算。迁出后正常选号并改绑，首轮按冷尝试。

**范围与回退**

- OpenAI 原版选号的五条判慢分区（直接选择、负载选择、负载查询失败回退、等待回退、粘性满载借用）均遵守质量分段。启用门槛的高级调度按“档×判慢”六池依次运行 TopK 与权重；未启用的高级调度保持原状。份额调度的新放置与 spill 候选接入分档和档内判慢，合法绑定属主保留；同站暂停按失败类改绑，不因叠加其他软罚而保留旧绑定。Claude 仍要求默认开启的 `gateway.scheduling.load_batch_enabled` 和并发服务；缺少时不后排、不迁出。热、冷截止与慢失败冷却不分选号方式。
- 慢失败计数、冷却与判慢期都在进程内，多实例时各自维护，重启后清零。
- 开关：OpenAI 判慢降序与样本记录随韧性总开关，没有另设开关；Claude 还受 `anthropic_latency_hold` 独立控制，关闭任一开关即不采样、不后排、不迁出并使本进程状态失效，重开只用新样本。两者的会话迁移另外要求“会话失败改绑”（`session`）开启，关闭不会删除或迁回绑定。OpenAI 热、冷截止，计划内截止与慢失败冷却随请求预算（`budget`）生效，关闭总开关或请求预算时回到原有的首输出超时处理。
- 要恢复旧档位，在 `gateway_resilience_settings` 中设置 `budget.first_output_timeout_seconds`（例如 180/180/200/220），保存即生效，不需要重新部署；冷尝试的下限仍取较大者，档位不低于 75 秒时不再起作用。

### 4.6 非 OpenAI 账号的模型级冷却（0.4.9）

一个账号上某个模型持续失败时，逐级加长该模型的冷却，同账号的其他模型照常服务。适用于 Anthropic、Gemini、Antigravity 平台账号，池模式账号除外；OpenAI 与 Grok 账号用 §4.5 的按模型屏蔽，阶梯相同。

- **触发**：不带 `Retry-After` 的瞬时失败按“账号×模型”记录，包括 HTTP 5xx 与 408、流内瞬时失败、首输出超时、停滞。模型是选号所用的请求模型，不是账号映射后的出站模型。
- **阶梯**：连续第 2 轮起冷却 10、45、90、180、300 秒，之后保持 300 秒；两次失败相隔超过 30 分钟时重新计数。
- **同轮**：冷却期间到达的失败（通常是同一批在途请求）算同一轮，不升级、不延长冷却。
- **清零**：成功只在请求开始时间晚于最近一次失败时清除计数与冷却；开始更早、晚到的成功不清除。
- **性质**：软罚。冷却期间该模型的请求暂不选该账号，会话绑定保留；组内没有其他可用账号时，末位兜底照常使用它。选号解释填了模型时显示为软档，原因 `model_transient`；无号 `Retry-After` 计入它（§3）。
- **例外**：上游带 `Retry-After` 的瞬时失败仍按账号冷却（§1 的短冷却）。池模式、自定义错误码与临时不可调度规则在更前面处理，优先级不变。
- **与以前的差别**：Anthropic 等账号的普通 5xx 以前第一次就让整个账号冷却 30 秒；现在单次失败不冷却，从连续第 2 轮起只冷却该模型。Gemini 的普通 5xx 与 408 以前不受罚，现在同样进入这套处理。
- **Gemini 同号重试**：Gemini 账号遇到普通瞬时失败时，该账号×模型已有未清除的失败就不再同号重试，直接换号；健康账号的偶发失败仍按 `budget.gemini_same_account_retries`（缺省 1 次）同号重试。
- 计数与冷却在进程内，多实例时各自维护，重启后清零。没有另设开关，随韧性总开关生效。

### 4.7 Gemini 文本尝试截止（0.4.9）

0.4.8 及以前，Gemini 的首事件截止只在配置里，三个入口都没有用上：卡在上游的请求一直等到预算或客户端超时，运维记录的阶段也停在选号。

- **范围**：Gemini 平台账号上受请求预算管理的文本请求，覆盖 Gemini 原生与 Messages、Chat Completions 兼容转换三个入口。图片或其他非文本输出（映射后的模型名含 `image`，或 `responseModalities` 不只有 TEXT）、countTokens，以及不能安全重放的非流式请求（含带 tools 的请求）不设截止。
- **时长**：非最后候选受 `budget.gemini_first_event_timeout_seconds`（缺省 60 秒）约束，再受剩余预算约束，从本次尝试开始计时，同号重试与退避不重置。最后候选（含组内唯一能服务该模型的候选）不设首事件截止，一直等到第一个内容、终态或客户端断开为止：截短唯一可用的号只会把慢答案变成错误（预上线验证 KA-6）。这条路径在第一个事件之前没有心跳，客户端或 CDN 自己的超时（例如 300 秒）会先生效。
- **结束点**：流式到第一个真实内容（文本、函数调用或内联数据）或终态，ping、空行与只有 usage 的事件不解除截止；非流式到完整响应收齐。有截止的流式请求在第一个内容之前不向客户端写出，到期时仍可换号。
- **到期**：确认没有写出内容时按首输出超时换号（`first_output_timeout`，计数与运维记录同 §5.2），并计入 §4.6 的模型级冷却。客户端取消不算截止。
- **输出后**：Gemini 流式文本请求接入停滞检测（`stall.text_stall_seconds`，缺省 120 秒），停滞时按入站协议写终止帧结束，记 `stall`，计入账号与会话失败。
- **阶段**：发出上游请求时记 `header_wait`，收到响应头后记 `pre_output`。
- **字段归属**：`gemini_first_event_timeout_seconds` 只管 Gemini；Antigravity 改读 `budget.anthropic_first_event_timeout_seconds`（缺省 90，与以前的有效值相同）。升级影响见 §6。

### 4.8 OpenAI 智商检测的模型级门禁

智商检测判为降智后，只冷却“账号 × 出站模型”（判定规则见 [OpenAI 智商检测](OPENAI_IQ_CHECK.md)），不再把整个账号移出候选池。

- **载体**：持久化模型限流 `extra.model_rate_limits` 中的 `iq:<出站模型>` 键，`reason` 为 `iq_degraded`。OpenAI 账号读取模型限流时一并读取这个键；账号上没有 `iq:` 键时不额外计算出站模型。
- **fail-closed**：键存在即生效，不看到期时间；只有复检判为聪明、关闭或删除目标、账号身份变化时才移除。剩余时间（用于粘性清除与无号 `Retry-After`）至少按 30 秒计，缺少到期时间时按 30 秒计。
- **compact**：compact 请求还会按账号的 compact 映射检查对应的 `iq:` 键。统一资格检查、advanced 初筛、末位兜底与其复核都带上这一项。
- **advanced 初筛**：按请求模型（含 compact 映射）检查 `iq:` 键，命中即拒绝（原因计入 `model_rate_limited`），被冷却的账号不再占用 top-K。普通模型限流在初筛中保持原行为。
- **末位兜底**：`iq:` 键不软化，剩余时间再短也不放行；兜底复核时再查一次。
- **会话粘性**：命中 `iq:` 键的账号按硬惩罚处理，绑定的会话在下一次请求时迁走，在途请求不受影响。
- **不被抹掉**：测试连接成功、清除限流、恢复状态、管理员编辑、批量编辑与导入都保留现有的 `iq:` 键；入参里带的 `iq:` 键会被丢弃，导出时剥离。
- **与账号 × 模型 breaker 的关系**：§4.5 与 §4.6 的进程内 breaker 与本门禁并存。两者的键规范化方式相同，但存储与原因分开，候选需要同时通过两者。份额调度不再有单独的 `iq_degraded` 拒绝原因，按模型限流处理。

### 4.9 报错后的补救与路由（0.4.12）

生产 48 小时（2026-10-26 至 28）的可补救失败集中在三处：各平台按状态码各自决定是否换号，统一分类只用于惩罚；写出过协议字节就被当成已经输出；出口的错误形态不符合 Codex 与 Claude Code 的重试规则。例如 Grok 中转的 `unknown provider for model` 19 小时直接失败 4,080 次，Gemini 402 余额 11 分钟直接失败 517 次，原生 Anthropic 零内容的流内 429 无法换号 178 次。本版只修这三处，不加开关，不改请求预算与首输出档位。

**按分类换号**

- 上游错误分为余额不足（`BALANCE`）或模型不可用（`MODEL_UNSUPPORTED`，判据见 §3）时，Gemini（原生、Messages 与 Chat 兼容转换）、Grok、Anthropic（原生、API Key 直通、Bedrock，以及 Gateway 入站经 Anthropic 上游的 Responses 与 Chat）、Antigravity 的各入口都换号；以前这些平台按状态码判断，400、402 直接返回客户端。402 `deactivated_workspace` 按鉴权失败换号，不做余额暂停。其他类别的换号规则不变。
- 平台原有的不换号判断优先：内容拒绝、上下文超长、签名与 beta 头、续链与重放约束。同一个上游响应只惩罚一次。
- 推理请求的上游 HTTP 404 仅在以下条件下新增模型不可用判定：正文为空；文本或 HTML 剥去标签后仅含 `404`、`not found`、`page not found` 及标准服务器签名（nginx、openresty、apache、cloudflare、caddy，可带数字版本号）；或 JSON 错误的 `code`/`type` 为空或泛称 `not_found`/`not_found_error`/`NotFound`、`message` 为空或仅为 `not found`，且没有 `param` 字段。大小写不敏感。符合条件时，视为当前账号不能服务该模型或端点，归为 `MODEL_UNSUPPORTED`，复用号×模型冷却与候选换号，不在同号重试。已有模型不可用文案/代码仍按原模型分类；明确的账号鉴权、余额和请求拒绝分类也保持原处理。候选耗尽时使用各协议的 404 模型不支持语义（OpenAI/Anthropic 为 `model_not_found`，Gemini 为 `NOT_FOUND`），文案为 `Model "<model>" is not supported by any configured account in this group`，不附重试建议。此出口是上游证据，不标为本地业务限制。
- 若 API Key 账号启用了自定义错误码且配置了 404，以上新增的泛称推理 404 保持既有账号错误策略，不进入这条号×模型冷却路径。若同时命中临时不可调度规则，仍按既有顺序先执行临时规则，未命中才按自定义码停号。Grok 独立入口沿用原有终止行为，不执行自定义码停号，也不换号。
- 该规则只用于推理；Anthropic/OpenAI 的 `count_tokens`、Gemini 的 `countTokens`、资源 GET 与本地路由保持原行为。具体资源 404（包括纯文本 `File not found`、具体 `code` 配泛称消息，以及任何含 `param` 字段的 JSON）保留原处理，不新增换号或冷却。各错误信封中的字段须同时满足条件，外层泛称消息不能覆盖内层具体资源证据。标准 nginx 404 页面符合上述文本规则；出现其他词的页面不符合。空正文或仅含泛称信息仍无法区分模型、端点与未声明的资源缺失，按账号不能服务本次模型/端点处理。
- OpenAI 兼容网关的 Responses 原生、直通及 Chat 兼容转发输出前的 HTTP 400 或 5xx：结构化 `error.code` / `response.error.code` 为 `model_not_found`，或错误消息命中 `isExplicitOpenAIModelAvailabilityMessage` 的既有通用短语，且结构化错误的 `param` 缺失、为空或为 `model` 时，按账号×实际出站模型不可用换号，禁止同号重试，复用瞬时失败计数与冷却（§4.6），不写 `model_rate_limits`，不进入 401/403 处罚，也不上报调度失败。上下文超限、内容安全、cyber 与状态类拒绝优先终止；普通 400 仍走原路径。compact 本地回退的顺序与单次重试约束不变。HTTP 404 使用上述通用推理规则和同一瞬时冷却出口，并沿用第二个上游主机出现同类模型错误即按请求级拒绝止损的护栏；其余候选耗尽后保留最后一条上游错误的状态和脱敏 type/code/param/message（包括 400），不追加 Retry-After 或重试建议，不计 turn cap；已提交流沿用终止帧规则，不重复追加通用失败帧。结构化 code 不依赖消息存在或措辞；检查标准 HTTP/Responses 错误信封，以及 `error.message` / `response.error.message` 中一层 JSON 错误信封。所有这些信封的状态、上下文、内容安全和 param 守卫都先于 code 命中；不递归扫描回显的请求内容，普通消息路径仍复用既有短语。
- Responses 原生与直通在 HTTP 200 后收到 `response.failed` 或 `error` 时，用既有流内语义状态和同一 Q2 通用谓词判断模型不可用（仍要求 `param` 缺省、为空或为 `model`，不增加短语）。仅当语义状态来自默认分支、且 `code=model_not_found` 或命中既有“model not found / does not exist”消息子集时，映射为 404；经过校验的包装状态优先；显式整数状态（包括 5xx）按与 code 相同的错误信封范围读取，先检查外层，再检查已支持的一层 JSON 错误消息，取首个有效状态并保持原值，不扫描请求内容。`invalid_request_error` 推导的 400 保持不变。无不存在证据的 `unavailable / not available` 仍保留默认 502。输出前在既有本地修复之后进入上述共享出口：换号、记号×模型瞬时失败、复用跨主机止损及耗尽返回；模型不可用的语义状态为 404 时，耗尽文案使用上述通用模型不支持文案。嵌套的 `response.error` 转成标准 `error`，保留脱敏后的 type/code/param/message，不附重试建议、不计 turn cap。输出后记同一号×模型的瞬时失败与冷却，并标记失败轮次、按尝试去重记录 `post_output_failure`（reason=`model_unavailable_after_output`）。终止帧沿原流写出，不重放、不调用会话失败、不计会话连击、不触发 P1-B 改绑或账号级输出后冷却；OAuth 裸 error 与 response.failed 成对到达时沿用既有延迟处理，只应用一次。状态、上下文、cyber、内容安全拒绝及校验通过的包装 401/403 保持原优先级。
- 同一请求的模型不可用若已在另一上游主机上出现过，则按请求级拒绝终止（`CLIENT_ERROR`），不再给当前号×模型记失败或冷却；同主机兄弟号仍照常换号、记数。该跨主机护栏同样适用于符合上述窄条件的推理 HTTP 404；流内模型错误沿用同一规则。主机口径复用 `accountUpstreamHost`，不以账号名称或站点文案判定。指向其他输入字段的 `param` 不进入 Q2，保留原错误处理。
- 上述共享模型不可用判定先于管理员配置的临时不可调度规则，因此命中的模型不可用 400 不再执行这些规则。Responses 直通在 `shouldFailoverOpenAIPassthroughResponse` 之前调用同一共享出口；WS 的 HTTP bridge 与 compact 回退响应有意不接入，保留各自既有行为。
- 上游 HTTP 5xx 仅凭 `type=invalid_request_error` 不再判为 `CLIENT_ERROR`，而归为 `UPSTREAM_REQUEST_RESTRICTION`（`upstream_request_restriction`）：在现有预算内换号，不在同号重试，不罚号、不记账号健康失败、不累计会话连败，也不写软冷却或模型冷却。与模型不可用共用上述跨主机护栏和终止出口：同一请求的这类 5xx 若在第二台上游主机复现，立即按请求级拒绝终止；同主机兄弟号仍可换号。候选耗尽也保留最后一条上游状态和脱敏错误体，不附重试建议、不计 turn cap，已提交流沿用既有终止帧规则。未提交的 HTTP 响应保留上游 5xx 状态并带 `x-should-retry: false`：该扩展头只对识别它的客户端生效，例如 OpenAI 官方 SDK；Codex 0.166 不读取此头，会在自身预算内重试 5xx。跨主机止损使每次重试最多触达两台上游主机。两类错误分别保留主机证据；明确模型不可用仍优先记号×模型冷却。4xx 的该 type、上下文超限、内容安全、cyber 和状态类请求拒绝维持原判定。其他 5xx 的瞬时故障处理不变。
- OpenAI 兼容网关的 HTTP 原生、Responses 直通及 Chat/Messages 兼容终止出口会记录未知拒绝连击：只处理最终分类为 `CLIENT_ERROR`、且 `code` 缺失或不在既有 `clientErrorCodes` 中的拒绝，排除 HTTP 401/403、上下文超限、cyber、bio 及内容安全/策略屏蔽。0.5.12 起，原生与直通 Responses 的输出前 SSE 状态拒绝，在既有修复与换号逻辑最终决定终止后进入同一出口；成功、输出后、客户端取消和已交给 failover 的结果不累计。当前请求仍按原错误状态和正文终止，不追加上游调用；同一实际分组、执行作用域、账号在 `session.streak_window_seconds` 内连续收到同类拒绝，达到 `session.streak_threshold` 后，通过 `AvoidOpenAIStateAccount` 按 `session.avoid_seconds` 回避该号。不罚号、不新增账号健康失败或普通会话失败计数；随现有 `session.enabled` 与总开关生效。
- 客户端提供线程标识时，HTTP 与 SSE 的连击、跨主机证据及暂避按 API Key、线程和执行 lane 派生的作用域累计；无明确线程时沿用请求会话键。作用域在账号命名空间改写前冻结。选号合并共享会话与执行作用域两份暂避集；子 agent 不删除或重写共享父线程的粘性绑定，也不写父线程暂避集，父线程自身拒绝仍可改绑。WS 保持原有处理，本版不新增 WS 连击。SSE 终止状态拒绝补上游 `terminal_rejection` 事件并显式传递请求拒绝结果，使恢复结果与 HTTP 一样记为 `terminal_rejection`；按上游上下文判定的运维归属计数会相应增加。
- 拒绝类型由“上游状态码 + code + 归一化消息”的 SHA-256 摘要区分；消息先脱敏、转小写并折叠空白，再将数字、UUID/长十六进制串和 request id 统一替换为占位符，不保存正文。所有数字都归一化是有意取舍，可能把 `32 MiB` 与 `64 MiB` 合并：最坏只会让该会话在 `AvoidSeconds` 内回避一个号，不罚号，跨主机出现同类拒绝后就停止回避。反过来，如果不归一化数字，上游消息中变化的大小、计数等会让摘要持续不同，连击可能永远达不到阈值，使会话卡在反复失败中。message 命中 `IsContextWindowError` 的上下文超限始终不触发回避。同号成功、其他失败或不同拒绝会重置连击。跨主机护栏沿用 `accountUpstreamHost`：同一会话的同类拒绝一旦在另一上游主机出现，就停止继续回避或延长回避；该证据保留至少 `max(streak_window_seconds, avoid_seconds)`，不会因同号连击复位而消失。已有的回避按原期限结束，不提前撤销。
- 连击及跨主机证据使用进程内 LRU，总计最多 16,384 条；多实例各自计数，重启后清零，过期或淘汰后重新积累。会话回避仍复用原有存储，不扩展 Redis 接口、不加开关。已知确定性 code（例如上下文超限）不触发回避；如果同样的请求问题只以未知 code 或无 code 文案返回，也可能先回避一个号，代价是后续请求换到另一主机后再失败一次，随后由跨主机护栏停止扩散。
- 结构化模型不可用的消息判据允许 `param` 为空或为 `model`；其他参数名仍不允许仅凭消息判定。
- 其余模型不可用路径：符合上述条件的推理 HTTP 404、结构化的模型错误码、§3 的几种明确消息，以及原有的 OpenAI 401 与 Codex 套餐限制两个特例，让该账号对该模型 30 分钟不可用。沿用 OpenAI“模型不存在”的模型限流（`model_rate_limits`），同账号的其他模型照常服务；末位兜底在到期前不放回。原有池模式、自定义错误码与管理员显式临时不可调度规则仍生效。
- 余额不足、鉴权失败、模型不可用不在同一账号上重试（Gemini 以前先同号重试，生产中一个账号 48 小时重试 341 次），换号耗尽后也不清空已排除的账号重新选号。其他失败照旧可在 2 秒后重选。OAuth 刷新后的认证重试、签名整流与续链重放这类修复性重试不受影响。
- Grok 每个上游错误只记一条运维记录，以前同一个错误记两次。

**余额暂停逐级加长**

- 档位：`balance.pause_seconds` 的 1 倍、3 倍、12 倍，第 3 档封顶，缺省 5、15、60 分钟。
- 升档：上一段暂停到期之后才开始的请求再次遇到余额不足，才升一档；暂停到期前已在途的请求失败不升档，暂停期间的失败也不延长暂停。判断与写入在同一个数据库事务内对账号行加锁完成，并发失败不会连升。
- 复位：开始时间晚于暂停结束时间的请求成功后清除余额暂停，下次失败从第 1 档开始；只清余额暂停，不清鉴权等其他状态；管理员“恢复状态”同样复位。
- 原因格式 `balance:exhausted[step=N]: <证据摘要>`（含上游状态与截断后的原文），N 为 1 至 3；升级前写入、不带档位的原因按第 1 档计。
- 范围同余额暂停：API Key 与 Upstream 类账号，不含 Antigravity 与自带余额恢复的渠道。尝试开始时间来自各 HTTP 入口的文本与图片请求；WebSocket 尚未覆盖，见 §7。
- 仍不主动探测，到期后由真实请求试探。生产中余额暂停到期后被用户请求反复撞上（单个账号 162 次），约 700 次换号本可避免。

**第一个内容之前的失败可以换号**

- OpenAI Responses 的流内中转包装帧（`event: error` + `type=proxy.upstream_error`，或 `response.failed` + `response.error.type=upstream_error`）只在状态码、字段类型与相等关系校验通过后按迟到的上游 HTTP 状态路由。输出前包装 400 交给加密态请求修复；包装 401/403 先在解包后的错误体上复用状态类拒绝、上下文超限、cyber 与内容安全判定，命中时按请求级终止，不换号、不冷却、不罚号、不上报调度失败，也不归为 AUTH；其余包装 401/403（含停用码、凭证码）走请求级 forbidden：换号、账号×模型软冷却、本请求内排除同站、用尽写 502，不进入凭证处罚、停用或 HTTP 403 连续失败计数。包装帧表示中转已接受本网关的 Key，帧内认证错误归于中转自己的上游。其他状态复用 HTTP 的换号判定。包装 400–599 以外或结构不完整的帧按普通失败处理，畸形帧各记一条 `resilience.wrapped_error_malformed`；已校验的包装 401/403 在输出后也不进入凭证、停用或 HTTP 403 计数处罚，终止帧照常投递，不再换号或记 forbidden 软冷却；非包装流内 403 及 HTTP 401/403 保持原行为。
- 包装校验单独保留原始 SSE `event`，仅用于 E1 校验和输出前包装分派；终态判断仍以 payload 的 `type` 为准。已输出时，即使原始行是 `event: error`，载荷里的 `response.failed` 仍完整投递并立即结束，不等另一帧或 EOF。非流式聚合出口同样分开保存原始 event 与终态类型。
- 包装帧的 `upstream_body` 解包后用于首次 HTTP 路由和后续失败分类，保留内层容量码、余额码等语义；仅包装 400 为 M3 保留原始外层体。`WrappedStatus` 始终保留校验过的上游状态码。
- 包装 401/403 在真实输出前命中上述四项请求级排除时，统一按解包后的拒绝语义终止：未提交写 HTTP 400；只发过心跳时写 `response.failed`。上下文超限用 `context_length_exceeded`，cyber 用 `cyber_policy`，内容安全和状态类拒绝用 `invalid_prompt`。普通 Responses 采样不会盲目重试这三类请求拒绝。Codex 0.166 将 HTTP 400 的 `context_length_exceeded` 归为 `InvalidRequest`，仅流内同名 code 进入 `ContextWindowExceeded`；压缩时机取决于调用路径和会话配置，不保证本轮自动恢复。`cyber_policy`、`invalid_prompt` 在 HTTP 400 和流内均有专门处理。message 使用经过清理的内层错误消息，不带 `Retry-After` 或重试建议，不进入通用的 401/403→502 映射或错误透传规则；包装状态只保留作诊断。真实输出后的包装终态保持原行为。
- forbidden 的本请求内同站排除只用于 OpenAI API Key 账号，与账号×模型软冷却的平台和账号类型范围一致。OAuth、Grok API Key 等其他账号只排除当前失败账号，同站其他账号仍可被选择；此分支不占容量换号额度。
- forbidden 的账号×模型软冷却只在有结构化证据时记录：载荷中的数字状态 403、校验通过的包装 401/403，或错误对象的 `code=upstream_error` 且没有 `type` 字段。仅从文案推导出的 forbidden 仍然换号，用尽写 502，但不记录软冷却；字符串状态码不作为数字状态证据，空值或空字符串的 `type` 也不等于缺少该字段。
- 直通流式与 SSE→JSON 聚合的失败分类在错误帧不带模型时使用本次请求的调度模型；compact 已改写的上游模型优先保留，不再二次映射。该模型也传给输出前和输出后的流内终态账号副作用处理（含 bare error 在 EOF 时的延迟处理），使模型级 429 限流和自定义临时不可调度规则使用号×模型范围，与原生路径一致。
- forbidden 用尽的 502 分支同样应用通用耗尽重试提示：未提交时写 `Retry-After`，已提交的 Responses 流按现有终止帧规则携带提示。达到会话连续失败上限时沿用通用终止策略（503、`server_is_overloaded`、不再建议重试）。
- `codex.response.metadata` 判为开场事件（§1）。以前它算内容，之后的失败不能换号，首输出截止也随之解除，出现过 240 秒的挂起。
- 原生 Anthropic 与 API Key 直通（`stream.prelude_staging`）：`message_start`、`ping`、空的 text 与 thinking `content_block_start` 暂存到第一个内容或终态，`tool_use` 起始算内容、立即写出。暂存期间上游的可重试失败（流内 `overloaded_error`、`rate_limit_error`，断流、读错、停滞）丢弃本次开场并换号，客户端只看到成功那次尝试的一个 `message_start`；不可重试的拒绝写出开场后以 `event: error` 结束；合法的空回答照常写出。
- 暂存期间按流内心跳间隔写注释行 `: ping`（以前是 `event: ping`），不算输出；失败尝试的上游响应头与 request-id 不写出。暂存最多 8 MiB（超过 64 KiB 写入已解除链接的临时文件），超出时换号；预算开启的文本请求最多暂存到本请求预算用尽。图片请求与没有预算的请求不设这个时限。
- 换号判断、送达与计费共用同一个“已输出”判据：只有开场与心跳时可换号；写出了内容，或写出时只写了一部分，都不再换号。客户端取消不换号。

**客户端看到的错误**

按 Codex 与 Claude Code 的重试规则调整出口，逐项见 §3：Anthropic 的容量类耗尽返回 503 `api_error`；Codex 的换号耗尽 `Retry-After` 受 `errors.codex_retry_after_cap_seconds` 封顶（缺省 10 秒）；排队与并发上限的 429 带 `Retry-After: 5`；原生 Responses 的本地失败以 `response.failed` 结束；Messages 的 Claude Code 专属拒绝返回 403；在途预留的 429 标为业务限制；Gemini 全池暂不可用返回 503 并带重试提示；错误消息去掉上游组织等标识。

**替补账号与会话绑定**

- Messages 请求成功后刷新会话绑定时，用不随客户端断开而取消、3 秒限时的上下文。以前客户端在响应写完后立即断开，绑定可能写不进去，下一轮换到别的账号。
- OpenAI 原版选号的负载感知路径（`gateway.scheduling.load_batch_enabled`，缺省开启）：会话的绑定账号不可用，或本请求已经换过号时，同一优先级、负载率不超过该优先级最低负载率加 20 个百分点、且费率与 compact 层级相同的候选，先按本会话近期成功时间降序，再比完整缓存证据、稳定哈希（HRW）和账号 ID。超出负载窗口的候选位置不变；费率排序、compact 分层、判慢期置后、模型硬门与排除集、派发前复核和抢槽仍优先。第一名无空位时依次试下一个，全部满时照旧排队或走末位兜底；健康 owner、无会话、首次选号及份额分组不变，续链与 WebSocket 规则不变。
- 近期成功只登记完整完成且已送达语义输出的 HTTP Responses、Chat Completions 与 Messages 请求；失败、部分用量、取消、开场和空终态不登记。进程内 LRU 固定最多 16,384 个键，每键最多 3 个账号，只保存账号 ID 和成功时间，每个账号独立按 60 分钟过期，重启丢失。键为实际分组、稳定会话与实际出站规范模型；写入使用与成功调度回报一致的已解析出站模型（依次取结果模型、请求中的实际出站记录、既有账号解析），透传结果模型为空时仍保留原用量 NULL 契约，查询时按本次端点与每个候选的实际转发规则解析请求模型：Responses 沿用透传/compact 规则（普通文本即使最低账号能力为 Chat 也不改变该协议），Chat 与 Messages 即使账号开启透传也执行普通模型映射，已规范化的模型不再次映射。模型映射改变后旧历史自然不命中。不进 Redis，不改粘性绑定格式，不新增开关；近期成功只影响替补排序，不改变热/冷判定，替补尝试仍为冷尝试。

**开关**：没有新增开关。余额暂停与阶梯随总开关与 `balance.enabled`（关闭后已写入的暂停照常到期，“恢复状态”照常清除）；硬类不同号重试随总开关；开场暂存随 `stream.prelude_staging`，`codex.response.metadata` 随 `stream.native_prelude_whitelist`；容量类改写、Codex 上限与 Gemini 耗尽改 503 随 `errors`。按分类换号、模型不可用的 30 分钟不可用、硬类耗尽后不清空排除集重选，以及其余几项属于路由、协议与记账修正，不受开关控制，见 §6。

### 4.10 报错补漏（0.5.1）

0.5.0 之后的 OpenAI 报错复盘里，以下几类失败可以在网关内化解或改正口径。本版不加开关，不新增预算参数，不改首输出档位与上游传输。客户端可见的行为逐项见 §3。

**订阅共享额度用尽**

- `subscription_sharing_usage_limit_exceeded` 与文案 “subscription sharing usage limit” 归入余额不足：HTTP 形态换号并进入余额暂停阶梯（§4.9）。以前 502 形态按瞬时失败只冷却该模型，同一账号被反复撞上；带内形态不换号、不暂停，`response.failed` 原样写给客户端（运维记为 400），Codex 按未知错误重连 5 次，每次都落回同一账号。
- OpenAI 流内事件只认结构化的余额码，覆盖全部余额码（以前带内 `insufficient_quota` 换号但不暂停）：第一个内容之前换号并暂停；之后不重放，只暂停，不计会话失败与输出后软冷却。换号时保留上游原始载荷，没有关键词的余额码在换号循环里同样按余额处理。

**加密内容跨号**

- 判据统一见 §3，HTTP、中转包成 502 的形态与带内事件都按请求级处理。账号副作用的三个入口（OpenAI 账号错误处理、错误策略检查、通用上游错误处理）在匹配临时不可调度规则与自定义错误码之前早退，Chat 等兼容入口与 count_tokens 也经过这三个入口。
- Codex 子 agent 的父线程亲和：以前只用于 Codex 自动审查（模型 `codex-auto-review`）；现在输入里有 `agent_message` 条目、且其 `content` 带 `encrypted_content` 片段的子线程，也优先选父线程的粘性账号，因为只有父线程的账号能解开这段任务正文。父线程 ID 取自请求头、turn metadata 与请求体的 `client_metadata.x-codex-parent-thread-id`，非空候选不一致时放弃，不接受客户端指定账号。HTTP 与 WebSocket 首帧共用这一规则。
- 父线程的会话键与普通请求一样按 API Key、分组、模型加作用域。以前直接对父线程 ID 求哈希，查不到 routing v2 起加了作用域的绑定，自动审查的亲和实际没有生效。修正后，模型相同的子 agent 能命中父绑定；自动审查（`codex-auto-review`）因模型不同仍查不到，见 §7。
- 父号选择仍遵守排除集、能力、隐私与利润门；分组质量门槛的豁免只用于命中的父号那一次粘性选择。父号不可用时普通选号，不带豁免，也不改写父线程的绑定。计算父线程哈希不改子请求的会话、质量路由与 lineage 状态。这只降低同模型、同 API Key、同分组且父绑定仍在时的跨号，边界见 §7。
- 状态族识别见 §4.12；rs/cmp 沿用既有原生路径剥离，S3/S4 不剥离旁路 reasoning。M3 另按 item 引用和完整槽位摘要替换 amsg/fco 密文，覆盖原生与直通 Responses 的 HTTP、输出前流内及非流式聚合出口。
- 同号剥离重放见 §3。HTTP 与带内都消耗本请求预算的一次原地重试（与 web_search 补声明相同），预算不足时不重放、原错误照常返回。带内重放与 web_search 补声明同一结构：在第一个内容之前收到可修复的拒绝时，先在本地算出新请求体，预算许可后才关闭这次响应、按新请求体重新发起；被拒字段的带内修复同样走这条路径。

**内容安全拒绝的运维口径**

- 运维记录的业务限制以前只在没有上游错误上下文时才判，上游内容拒绝因此全部计入 SLA。现在按本次终态判断：终态出口改写了 type 或 code 时由出口打标记；否则用原始的 type、code，以及最后一次上游的状态与原文，调用同一份韧性分类，只认内容安全拒绝。普通 400 与上游 5xx 仍计入 SLA，见 §5.1。
- 分类表补 `content_policy` 码与“内容政策”文案。`invalid_prompt`（“flagged as potentially violating our usage policy”）不在此列：仍计入 SLA，被中转包成 502 时仍换号。

**请求兼容类 400**

- 见 §3。被拒字段修复以前只接入原生、透传与两条 WebSocket 路径，现在补上 Chat Completions 路径与原生 Responses 的带内形态；Chat 路径在重放前先关闭上一次响应、取消它的上游请求。

**Grok 与 alpha search**

- Grok API Key 账号只有在 HTTP 422、`error.type=api_error`、`error.message` 完整等于 `Service temporarily unavailable`，且 `error.code` 与 `error.param` 均为空时，才按瞬时失败使用现有换号预算。原始尝试状态保留为 422；同形的 OAuth、其他平台响应、带请求错误字段的载荷，以及 400 请求错误不适用；预算耗尽返回 503。管理员配置的错误透传规则仍优先。
- alpha search 的 API Key 账号只有在 `/v1/alpha/search` 返回 HTTP 400 且载荷为 `error.code=E5001`、`error.type=server_error` 时进入现有换号预算；文案不参与判定。账号状态与全局调度健康不因该信号更新，耗尽后默认返回 502，回合封顶时为 503，尝试事件保留上游 400。API Key 上游以 404/405，或 HTTP 400 的两个能力提示（“do not support the requested endpoint”、“not encrypted history or tool items”）表示不支持 alpha search 时，仍按端点能力换号，不能和 E5001 分类混为一类。
- alpha search 两个原样透传非 2xx 的出口都不报账号成功，也不执行计费；只有 HTTP 状态 ≥ 400 时登记一条 `http_error` 尝试，类别按完整上游状态、响应头与载荷计算，3xx 不登记错误尝试。

**流式终态状态**

- 网关实际写出 `response.failed`（含 Gateway 入站的兼容 Responses）或 Chat 终止帧后，运维记录优先采用该终态对应的网关状态；writer 未能写出终态时不设置此状态，重复终态不覆盖先前已写出的状态，WebSocket 下一回合清除旧状态。没有本网关终态状态时，仍按 SSE code/type 推断。上游原样转发的终态不带本网关状态，继续按原规则推断。

**运维记录与分组质量门槛**

- 运维错误记录里每次尝试的 `resilience_category` 按完整上游载荷计算（原生与兼容路径的终态、带内事件），与换号决策一致，也与换号尝试的记录（此前已按完整载荷）一致；只有 code 的加密拒绝不再按文案记成 `TRANSIENT`，被中转包成 502 的 `invalid_prompt` 等请求错误也按载荷记为 `CLIENT_ERROR`。
- 分组质量门槛只把 `TRANSIENT`、`FIRST_OUTPUT_TIMEOUT`、`STALL` 计为软失败，额度用尽（`BALANCE`）与加密拒绝（`CLIENT_ERROR`）从此不再计入。

**开关**：没有新增开关。余额暂停随总开关与 `balance.enabled`（关闭时流内余额码照样换号，只是不暂停）；内容拒绝不罚号随总开关（关闭时 403 按原处理）；总开关关闭时 `resilience_category` 为空（现有语义）。加密拒绝的请求级处理、同号剥离重放、被拒字段修复、Alpha Search 换号与父线程亲和不受开关控制，见 §6。

### 4.11 输出后失败尝试补记（0.5.1）

HTTP 流已向客户端输出可见内容后，快层确认账号发生读流出错、缺少终态、上游实际报告的带内失败事件（`stream_failed`）或停滞时，先记录失败事实；写运维错误记录时，若本次发送之后没有该账号的上游事件，再补一条尝试，随后按上游上下文归因。读流出错与缺终态分别记 `stream_read_error`、`missing_terminal`，类别为 `TRANSIENT`；停滞记 `stall`，类别为 `STALL`。带内失败事件记 `stream_failed`，其运维类别使用与直接登记相同的状态及类别规则，因而 `RATE_LIMIT`、`CAPACITY_SHED` 等不计入门槛和份额健康的软失败。快层自己的会话失败、冷却与 `post_output_failure` 计数通常仍按 `TRANSIENT` 处理。Q2 命中的流内模型不可用单独记 `model_unavailable_after_output`、类别为 `MODEL_UNSUPPORTED`：保留失败轮次、日志、指标和失败事实补项，但跳过会话失败及账号级输出后冷却，仅走号×模型冷却。开场元数据和心跳不算可见内容。对读流出错、缺终态与停滞，客户端取消或请求上下文结束若先于快层确认则不补记；已确认失败后才取消（最终响应仍可能为 HTTP 200）仍照常补记。0.5.11 起，客户端已离开（请求上下文已结束）时，读流出错、缺终态、停滞和上游随后送达的带内失败事件都不计入号的失败：不补记失败事实，也不记会话失败、输出后计数或冷却，只记 `resilience.client_gone_skip`；此前带内失败事件在这种情况下仍按上游实际报告补记。出字前停滞及未被快层计入的失败不补；带内事件已有直接登记（例如 `response.failed`）时，发送边界去重会跳过补项。

每次实际 HTTP 发送（含插件转发与同号原地重发）建立新边界；只用该边界后的同账号事件判断是否已有记录，因此前一次尝试的事件不会挡住后一次补记。拿不到有效边界时，本请求只要已有该账号事件就不补，可能漏记但避免重复。补项为 `stream_failed`，`at_unix_ms` 记快层确认失败事实的时刻；OAuth 类账号的裸 `error` 可能延迟到终态或停滞时才确认。补项写非空安全消息：带内失败事件使用 `upstream reported a failure after output`，读流出错、缺终态和停滞沿用原断流消息 `upstream stream broke after output`；不写入上游原文或虚构的上游 HTTP 状态。它只提供证据，不再次暂停、记录会话失败或改变计费、最终状态码，也不是 `failover`，不增加换号数。

补项先进入请求的上游事件上下文，再进行归因并写入错误记录。常规、带内终态和显式 HTTP 流错误路径都读取合并后的上下文；阶段为 `upstream`、责任方为 `provider`、来源为 `upstream_http`。这里的 `upstream_http` 是既有来源分类，不表示实际收到 HTTP 5xx；细因看尝试的类别与原因。带内终态的顶层上游状态可能是逻辑状态，也不代表实际收到的 HTTP 状态。流错误快照只用于 WebSocket 回合，不含补项；WebSocket 回合与连接退出均不补记，其漏记与归因保持原状。监控过滤、脱敏和队列限制保持不变；写运维记录前 Anthropic 的 `event: error` 已有同账号 `stream_error` 上游事件，因此按发送边界去重，不会重复补记；其他平台共用已有快层与运维边界，不扩展失败类别。

### 4.12 P1-B：输出后首次失败改绑

`session.post_output_rebind` 缺省为 true，**仅 OpenAI 账号**生效，以最终上游账号的平台（`PlatformOpenAI`）为准。Anthropic、Grok 等其他平台的输出后失败仍按原连击规则处理。仅已确认输出后的流内失败事件（例如 `response.failed`）、读流出错、缺终态携带专用会话失败信号；沿用 `ReportStreamFailure` 的 attempt 标识去重：HTTP 请求内同一失败只立即改绑一次，WebSocket 连接按 turn 计，每个 turn 可各触发一次。动作复用现有连击改绑：按属主删除绑定、加入会话避开、标记会话、把账号加入本请求的避开集合。避开时长沿用 `session.avoid_seconds`，缺省 600 秒。输出前失败仍需达到 `session.streak_threshold`（缺省 2），不降低全局阈值；客户端取消、客户端错误和内容安全拒绝仍不罚号。

带密文片段的请求退回原有连击规则（`session.streak_threshold`，缺省连续 2 次）：只识别 `input[]` 内 `agent_message.content[]` 或数组形态 `function_call_output.output[]` 中的 `type=encrypted_content`，按 JSON 解码后的结构匹配，合法 Unicode 转义同样有效，非法 JSON 不命中。第一次真实输出后失败不立即改绑或避开原号，达到连击阈值才改绑；账号输出后冷却计数、同站暂停记账和其他失败类改绑不变。HTTP 读取请求上的密文保护标记。WebSocket 原生池、透传及 HTTP 桥在连接内保留密文标记，并按客户端每个 turn 的 payload 更新实际用于输出后处理的上下文：带非空 `previous_response_id` 的增量轮继承上一轮标记，并识别本轮新增密文；不带该 ID 的全量轮只按本轮 payload 判定，无密文时可以清除标记。首帧没有上一轮，即使带 `previous_response_id` 也只按本帧 payload 判定。同一轮内部重试保留本轮标记。未受该保护的请求，真实输出后失败仍首次即改绑。

会话状态族拒绝属于请求级问题：原生与透传 HTTP、SSE、Chat / Messages 桥接及 WebSocket 的换号与账号副作用入口共用 `ClassifyStateRejection`；不罚号、不计会话连击、不计门槛与份额健康的软失败。除 §3 的 M3 明确换号或终止时按请求态回避外，输出后不触发 P1-B（含会话避开）或账号冷却。HTTP SSE 与 WebSocket 的裸 `error`、`response.failed` 或二者先后出现均适用。状态拒绝按以下顺序识别：

1. 专用 code `client_tool_continuation_unavailable`、`invalid_encrypted_content`、`encrypted_context_invalid`（不区分大小写）直接命中。
2. 现有 `IsEncryptedContentRejection` 命中时排除；它覆盖 S3 的加密函数输出无法解密，以及 S4 的 `encrypted content` 搭配 `could not be verified`、`could not be decrypted` 或 `hydration failed` 等完整拒绝文案。
3. 仅在 code 为空时，用 `opaque conversation state`、`unrecognized conversation state`、`encrypted content is invalid for this conversation` 补判 S1/S2。单独出现 `for item amsg_` 或 `invalid for this conversation` 不足以排除；明确的 server/rate-limit 错误码不会被这一步的文案覆盖。

命中后按 S1 → S2 → S3 → S4 → reasoning / compaction → S6 细分为 `relay_state`、`conversation_invalid`、`function_output`、`agent_message`、`reasoning`、`other_encrypted`。分类结果为 `CLIENT_ERROR`，不带加密判据时 reason 为 `conversation_state`，带加密判据时仍为 `encrypted_content`。无 `invalid_request_error` 包装的 S1 也按请求级拒绝处理。请求的最终失败、既有运维事件与真实用量照常保留；只给已有 `upstream_errors` 条目附 `state_family`，不为状态拒绝新增尝试事件。请求级判断与修复决定分离：`Encrypted` 仅作诊断，有限 rs/cmp 剥离仍使用原有加密判据与预算，S1 不扩大修复。原生与直通 Responses 输出前的 S2/S3/S4 和校验通过的包装 400 改走 M3；其他路径保持现有错误出口。


M3 每个请求按账号保存已替换槽位集合；槽位身份为 kind、item 引用、part 下标及完整槽位的规范化 JSON 摘要。每次出站在既有构造器之前替换，保留 item、引用、数组顺序及明文，不读 arguments 或字符串 output。 命中既有 DeepSeek/Kimi 请求归一化判定的账号不启用 M3（包括 OpenAI 平台映射到 DeepSeek 的账号），继续走原有请求处理。形状歧义时终止派生。S4 必须唯一定位点名 item，立即把有引用槽位写入 Bad；S2/S3/包装 400 的整批 Bad 只在本次重放获得上游接受证据后写入：收到 response.completed / response.done 成功终态、完整成功的非流式 Response，或真实语义输出后收到 incomplete / cancelled 终态或干净 EOF。语义输出沿用 §3.1 判定；failed、error、读取异常和没有输出的 incomplete / cancelled / EOF 均不提交，读取器返回 nil 本身不算证据。`[DONE]` 只结束传输，既不算成功证据，也不累计为语义输出；仅沿用此前已有的真实输出证据。Bad 复用 lineage 进程内存储，按组、进场会话与账号隔离，TTL 沿用粘性 TTL（默认 1 小时）；引用最多 256 字节，超长引用按无引用处理，只在当前请求替换，不持久学习；Bad 的 kind 使用常量，ref 独立拷贝，不连带保留请求体。共享 Bad 的增长不影响本请求的重放判据。

M3 换号额度上限 1，与容量额度独立；换号仍受请求预算约束，同号修复仅豁免末位尝试次数守卫，保留截止时间和取消检查。换号与终止均对最后出错号执行属主校验删绑和会话回避，写入本请求排除集，不排同站、不计连败及调度健康失败。改写后的错误先用既有有界字段、compact、web_search 修复；耗尽后，仅未被既有分类判为请求级的 400 / 422 视为派生形状可疑并换号。401 / 403、其他 4xx，以及上下文超限、内容安全、cyber 等请求级拒绝继续走原有处罚或终止路径。每次重放重置 AttemptProfile，首字窗口按新的尝试起点和剩余预算重算。 M3 的客户端与 ops 消息共用固定模板，仅包含已识别的拒绝原因和唯一校验成功的 item id；不保留上游 message、拒绝载荷或密文回显。已启用 M3 的输出前包装 400（包括非流式聚合出口）在通用上游错误记录前交给 M3 延迟入口，原始拒绝证据仅供本请求修复，不写入 ops 历史事件；非 M3 和输出后的观测保持原有行为。

质量路由持有的绑定不越权删除；会话避开集合在进入质量路由之前并入排除集，因此有其他正常候选时下一请求不再选出错号。组内只剩该号时仍保留现有末位兜底。绑定已换成其他属主时，沿用现有 owner 校验；非 `v2:` OpenAI 会话键仍沿用原有删除语义。关闭该开关回到原有输出后失败连击逻辑；关闭 `session.enabled` 时 P1-B 一并失效。

事件沿用 Warn `resilience.session_rebind`，`reason=post_output_first_failure`；`session_rebind` 计数使用同一原因。普通连击的原因仍为 `resil_failure_streak`。

### 4.13 OpenAI 同站断流暂停

站点按 `GetOpenAIBaseURL()` 的主机名归并（忽略协议、端口和路径；未配置时归到官方站点）。只计 `resilienceStreamBrokenAfterOutput` 确认的传输层断流：输出后读流出错或缺终态。中转在流内报告的错误、输出前失败、客户端取消及非 OpenAI 账号不计入。

同号 3 秒内的断流合并一次。某号断流时，同站另一账号在此前 60 秒内也断过流，才对这些断流账号中尚未暂停的各暂停 300 秒；没有断流的同站账号不受影响。暂停中的再次断流不延长到期时间。上述时长与至少 2 个账号的条件均为代码常量。

暂停接入现有运行时冷却检查，普通选号跳过；同时映射为软惩罚，所以组内没有其他可用号时仍可作末位兜底。绑定会话下次请求通过现有属主删除与绑定流程改绑，属于失败类改绑，不受质量迁出冷却限制；同站暂停不享有普通瞬时软罚的“保留原绑定”例外，暂停到期后不主动迁回。不改变门槛统计，断流照常进入错误维。

`same_site_hold.enabled` 缺省为 true。关闭后不建立新暂停，已有暂停在检查时立即失效；重新开启从新样本开始。跟踪器在韧性服务内独立保存，不复用代理断流熔断；进程重启清空，各实例独立。Warn 事件为 `resilience.same_site_hold`，每个账号每次暂停一条，只记主机名 SHA-256 的前 16 个十六进制字符，不记录明文主机名。

### 4.14 Codex 协作消息明文通道（P1′）

管理端网关韧性中的 `collaboration_plaintext.enabled` 默认关闭；`models` 是经过验证的实际出站模型清单，模型映射之后再匹配（去首尾空白、不分大小写精确匹配）。账号页的 `extra.openai_collaboration_plaintext` 仅布尔 true 生效；单账号关闭删除键、批量关闭写 false 都表示关闭。全局总开关、功能开关、模型、账号资格及 Codex UA/originator 全部满足才转换；ForceCodexCLI 不绕过真实客户端门。代码同时接通 API Key 与 OAuth，仅原生 Responses HTTP；预检仅覆盖 API Key 中转站，OAuth 未预检，验证前不要在 OAuth 账号开启。WS、透传、compact、协议桥接、摊平账号不转换。功能未生效（总开关或功能开关关闭，或模型清单为空）时，入口不做原始 plan 检查；只观测统计照旧。入口仅对原始字节含字面 `"collaboration"` 的请求，在原始字节上执行与改写相同的全部检查（只算 plan、不改写），并冻结结果；否则禁止获得 plan。原样拿不到 plan 的请求，归一化后也不转换。这类请求包括：协议容器、协作声明或 developer/system 文本片段中有重复键或其他歧义，有冲突，store:true，previous_response_id 非空，或形态不受支持。只有原始请求任何位置都没有字面 `"collaboration"` 时，转义声明随后被归一化也只观测；若原始字节已有该字面，则照常按原始 JSON 解码后的值判定（含转义声明），原始歧义仍会被拦截。资格门全部通过、但原始阶段判为冲突的请求记 outcome=conflict（eligibility=account），照常触发 Warn；资格门未满足、根对象非法/重复键、store/previous 排除与不支持形态统一记 outcome=unsupported，不另细分原因。

请求声明 collaboration 命名空间时，先判冲突再原子改名为 collaboration-xy，只删 spawn_agent/send_message/followup_task 的 message.encrypted。tool_choice 仅支持缺省或 auto/none/required 字符串；对象选择和 Chat 嵌套 function 形态均 unsupported、原样不转换。历史 function_call/custom_tool_call 中，namespace=collaboration-xy 或 name 使用 collaboration-xy. / collaboration-xy__ 前缀时，只有裸名属于本次已校验声明的工具才归一化；显式外部 namespace、未知工具、alias 声明和 developer/system 文本里的 alias 仍判 conflict。历史 alias 的进入来源尚未定位，此处只兼容本方案定义的名字。仅 applied 时，把顶层 instructions 与 developer/system 消息的文本（字符串或 input_text.text）里精确的 functions.collaboration. 同步替换成 functions.collaboration-xy.，避免寻址示例与声明失配；用户消息、工具输出和参数不替换。DevTextRewrites 记录本次精确字面串的实际替换次数，仅作为异常 Warn 的字段，不单独触发 Warn、不新增指标。回程三种身份回声统一恢复 namespace=collaboration 和裸叶名，仅符合声明、message 为非空明文且没有密文否决的消息调用补 encrypted_function_args:[]。非空标记、gAAAA 前缀或无空白长 base64 等可疑形态否决补标记；早先非空证据持续到该尝试结束。wait/list 等同 namespace 工具只恢复名字；未知私有叶名恢复身份、计 uncovered、不补标记。

流式、JSON、SSE→JSON 共用本次尝试的冻结 plan，不跨请求继承。不改 IDs、arguments、output 正文或输入中的密文片段；instructions 的已改写结构化回声对称恢复。错误信封按 JSON 字符串解码后匹配别名（含转义），只重写含别名的字符串，其余字节不变；非 JSON 错误体按字面替换。非 2xx HTTP 错误体和 SSE type=error 的完整错误信封均覆盖；所有 JSON 响应/事件的顶层 error 与 response.error 也会还原，包括 HTTP 200 失败 JSON。歧义载体仍本地终止（未提交 502、已提交协议终态），不泄漏私有名、不经 failover 重放。applied 尝试的 UpstreamFailoverError.ResponseBody 与非空 ClientMessage 在返回 handler 前已还原，因此换号耗尽后 handler 侧的错误透传规则按规范名匹配，服务侧按原始错误匹配；规则关键词不应包含私有别名。内部运维日志（如 openai.forward_failed）保留上游原文，可能含私有别名，仅供诊断，不下发客户端。nil plan 不改字节。

已知限制：模型生成的正文（包括协作消息的 message 正文和输出文本）可能引用私有工具地址，如 collaboration-xy.send_message。按红线网关不改写正文；子 agent 在 P1′ 下看到的正是同名别名工具，协议 namespace/name 仍正确还原。发版前真实上游一轮中出现 1 次（astra 父 agent 的委派说明），该次父子闭环正常，功能未受影响；这不属于协议身份还原失败。

schema 兼容：工具 schema 清理器对精确名为 collaboration 的整个 namespace 跳过递归修复，包含 wait/list；其他工具的 null type、object union、null required、lookaround 修复不变。normalizeCodexTools 不递归 namespace。改动前清理器对真实 Codex 0.166 fixture 的顶层与 Lite additional_tools 声明均逐字节不变；该样本在 P1′ 关闭时没有额外变化。对原本需修复的其它 collaboration schema，豁免仍会让其保持原样，此行为独立于开关。

灰度预检覆盖 API Key 站 d0c7a8 / c2a95e / b672e6 × gpt-6.1-sol / gpt-6-astra、Codex 0.166。账号开关表示该站已验证全局清单内全部模型；同站账号统一配置。灰度池须包含兜底分组、满载借用等可达账号，排除未验证 API Key 账号及未验证 OAuth 路由；关闭账号开关后，存量请求按账号资格重新判定；切到清单外最终模型时形成开→关。列出账号和启停步骤见 [部署 runbook](../deploy/README.md#codex-协作消息明文通道)。

Codex 0.166 provider 名不是精确的 OpenAI 时，每轮删除历史 EFA，关账号开关对这类客户端等同硬回退。该形态的开→关续轮在 c2a95e、d0c7a8 两个目标模型已闭环；b672e6 补跑持续 503、无结论。保留 EFA 的 OpenAI provider 开→关在前两站也通过，但 b672e6 账号 470 两次 cyber_policy；不承诺硬回退后存量会话不中断。

提示缓存：持续启用且输入相同时，变换后的前缀稳定；使转换状态改变的开/关（含总开关、功能开关、模型清单或账号开关变化）会使进行中会话下一请求从工具声明处起失效一次，之后同态稳定。真实 Codex 0.166 的 58,104 字节样本中，首个差异在字节 19,695，位于 input[0] additional_tools 的 [46, 25,018) 区间；不据此推算其它会话的精确失效率。反复切换会重复失效，建议低峰、整站切换。

完整契约与验收见 [OpenSpec design](../openspec/changes/codex-collaboration-plaintext-20261104/design.md)。

## 5. 观测

### 5.1 管理端

- 运维页“网关韧性”面板：当前实例最近 5 分钟至 1 小时的计数、未送达请求列表；计数与未送达列表按分组名显示。
- 选号解释：运维页面板底部可选任一分组（含原版调度），可选填模型与会话哈希，只读查看每个账号的硬、软档与原因（余额暂停为 `balance_exhausted`）、恢复时间、会话失败次数与是否回避，以及末位兜底账号与兜底分组决策；填了模型时，若该模型被分组白名单或渠道定价拒绝（所有请求在选号前即失败），在账号表上方提示。份额调度 Explain 的结果中附同样的韧性视图。0.4.4 起结果多一列“已学能力”（仅流式学习记录，任何查询都显示）；可选“请求类型”（Responses 或 Chat 的非流式请求）时，按该请求形态把被学为仅流式的账号标为跳过，原因 `capability_stream_only`，恢复时间即学习到期时间；不选则保持原来的整体健康视图。末位兜底账号不计模型、渠道、额度等逐请求条件，实际选中的可能是后一个候选。0.4.9：填了模型时，非 OpenAI 账号的模型级冷却（§4.6）显示为软档，原因 `model_transient`；冷却在进程内，只反映当前实例。
- 账号列表：余额不足暂停显示为临时不可调度，原因以 `balance:` 开头并附上游原文；“恢复状态”立即放回。欠费但暂时不用的账号，关掉调度即可。
- 仅流式学习（0.4.4）：被学为只接受流式的账号在账号列表的状态列下方显示“仅流式（自动学习，剩余 X 小时）”，悬停说明涉及的协议，点“清除”即可；清除后非流式请求可以再选它。选号解释中被跳过的账号显示原因 `capability_stream_only`，恢复时间即学习记录的到期时间。
- 运维错误详情：每次上游失败带失败分类 `resilience_category`（如 `BALANCE`、`TRANSIENT`、`CAPACITY_SHED`）。0.5.1 起按完整上游载荷计算（含只有 code 的错误与带内事件），与换号决策一致。
- 流式终态：本网关实际写出的 `response.failed` 与 Chat 终止帧，其网关状态优先于 SSE code/type 推断，并用于运维行的状态与上游状态字段；没有写出本网关终态时不设置该状态。上游原样转发的终态仍按 code/type 推断，不读取之前尝试写入的上游状态。
- 输出后失败补项及归因（0.5.1）：见 §4.11；管理端供应商错误列表可能多显示这类记录，用户侧分类也随归因变化，指标影响见 [用量指标口径](USAGE_METRICS.md#输出后失败的归因与指标)。
- 运维看板的 SLA（0.4.9）：选号阶段找不到可用账号的失败（无号 503，阶段 `routing`）计入 SLA，不再标为业务限制；余额、用户并发、本地模型配置等业务限制不变。只影响升级后写入的记录，见 [用量指标口径](USAGE_METRICS.md#运维看板的-sla)。0.5.1 起上游内容安全拒绝（按韧性分类判定）标为业务限制，不计入 SLA（§4.10）。
- 告警：不新增余额类指标。RC 的迁移 276 预置过两条余额告警规则，由迁移 277 删除。
- 接口：
  - `GET /api/v1/admin/resilience/stats?window=15m`（窗口最长 1 小时，计数为单实例内存口径）；
  - `GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=`（时间为 RFC3339）；
  - `GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=`（只读；0.4.4 起每个账号带 `capability_learned`，`protocol=responses|chat` 与 `stream=true|false` 须成对出现，否则 400；`stream=false` 时按该请求形态排除仅流式账号（主分组与兜底分组都算），原因 `capability_stream_only`、`recover_at` 与 `capability_ttl_seconds`）；
  - 账号列表与详情的 `capability_learned`（0.4.4）：`[{protocol, mode: "stream_only", expires_at}]`，没有记录时为空数组；`DELETE /api/v1/admin/accounts/:id/capability-learning` 清除。

### 5.2 计数项

0.4.13 观测：`usage_logs` 与既有 `ops_error_logs` 行新增可空 `end_to_end_first_output_ms`，从入站 delivery 初始化到首个成功送达的语义输出，包含排队和所有先前尝试；没有输出为 NULL。开场、心跳、真实错误、`[DONE]`、纯终态与空内容数组/对象不算；正常 `error: null` 等占位字段不排除真实内容，旧首字与计费判据不变。运维行另记 `recovery_outcome`，由 handler 按实际结果分类：`completed`、`post_output_failure`、`client_cancel`、`budget_exhausted`、`selection_exhausted`、`terminal_rejection`、`upstream_failure`。取消优先于之前的超时/换号；下游写失败已明确报告断开时，即使请求 context 尚未取消也保留 `client_cancel`。OpenAI 原生/透传 Responses 与 Chat/Messages 中已确定的请求或策略拒绝均记 `terminal_rejection`，由明确的拒绝信号传递，不根据最终 HTTP 状态或错误字符串推断；部分输出后上游失败仍是失败。只附在本来产生的行上，正常成功不新增运维行，不增加接口或前端展示。

首字超时的现有失败尝试 JSON 另带 `warm`（缺失/false/true）、`body_bytes`、`effort`、`attempt_elapsed_ms`、`timeout_ms`，沿既有队列脱敏裁剪，不保存正文、不新增成功尝试事件。这些维度描述失败的那次尝试，不能套到随后成功的尝试上。迁移 283 的锁与回退要求见 [用量指标](USAGE_METRICS.md#迁移与回退)。


| 计数 | 含义 |
|---|---|
| `keepalive_committed` | 首包前心跳提交了响应头（细分为协议）；0.5.6 起也计入流内心跳或上游注释完成的首次提交，计数会增加，不宜直接与 0.5.5 的观察期数据比较；0.5.11 起也包含槽位等待 ping 的首次提交，比较升级前后数据时需计入这部分口径变化 |
| `first_output_timeout` | 首输出超时触发换号（0.4.9 起含计划内截止，见 §4.5，以及 Gemini 文本尝试截止，见 §4.7） |
| `budget_exhausted` | 请求预算耗尽 |
| `last_resort` | 末位兜底选中软罚账号 |
| `fallback_group` / `fallback_return` | 转投兜底分组 / 切回主分组 |
| `session_rebind` | 会话失败改绑；未知终止拒绝达到既有会话阈值时复用该计数，原因 `terminal_rejection_streak` |
| `undelivered` / `cdn_cut` | 未送达；按耗时推断的 CDN 切断（90 秒、125 秒、300 秒） |
| `fast_fail` | 同一 Key、同一模型的重复 503 快速返回 |
| `turn_cap` | 会话连续耗尽失败到达上限，改为终止应答 |
| `abuse_guard` | 未送达滥用保护计费 |
| `stall` | 停滞断流（0.4.9 起含 Gemini 流式文本） |
| `state_rejection` | 请求级状态拒绝，按分组、账号与家族计数；不受总开关影响 |
| `post_output_failure` | 已输出后的上游失败（细分为 `stream_failed`、`stream_read_error`、`missing_terminal`、`model_unavailable_after_output`） |
| `relay_keepalive` | 从上游流中丢弃的中转伪保活帧（按账号；0.4.4 起含 `response.created` 之前的空 delta） |
| `pre_created_delta` | 上游在 `response.created` 之前发出的非空 delta（按分组、账号、平台，0.4.4；每次尝试至多计一次）；照常输出，用来观察这类中转的规模 |

`collab_plaintext` 按 outcome、实际出站 model 统计尝试（label 编码为 `outcome=<值>;model=<值>`，全部资格门通过时追加 `;eligibility=account`）；`collab_call` 按 `efa=absent|empty|nonempty` 统计三个消息工具的逻辑调用（Calls/EFA 分母不含 wait/list/interrupt）；只观测模式另以 `msg=opaque|text` 记录有效字符串 message 的可疑/文本形态，缺失或无效 message 不计入这两项；`collab_call_action` 按 `action=marker_added|vetoed|contradicted|uncovered` 统计动作。EFA、message 形态与 action 是独立边际计数，不代表联合分布，流式多事件不重复计同一调用。message 形态仅为诊断，不证明可投递。

48 小时/7 天验收的持久化来源为 resilience.state_rejection 事件中的 collab_plaintext 标识，以及 usage_logs 按账号、模型和客户端 UA 汇总的请求量。usage_logs 只能近似合格流量，无法精确得出“符合资格的新协作投递”分母，不应把近似请求量当精确覆盖率。进程计数仅保留当前实例最近 1 小时，可作灰度抽查；多实例须分别采集，不以它替代长期验收。

### 5.3 日志事件

结构化日志的消息固定为事件名（前缀 `resilience.`），细节在字段中。

warn 级，进入运维页“系统日志”（`ops_system_logs`）：

- `resilience.collab_plaintext`：conflict/invalid、vetoed/contradicted/uncovered 非零、恢复报错，或 applied 后最终失败被现有分类判为客户端错误且不是 429/5xx 等价时记录；HTTP 200 输出前的流内拒绝同样覆盖，in_band=true；含 account_id、group_id、model、client_version、outcome、applied、eligibility（account，资格未通过时为空）、upstream_status（无响应时为 0）、headers_committed 与各计数（含 msg_opaque/msg_text、dev_text_rewrites）。401/402/403 等账号侧或余额类别、瞬时 5xx、429、网络故障或断流不单独触发这条失败 Warn；本地恢复错误及前述异常计数仍独立触发。原始阶段 invalid（根对象非法/重复键）归为 unsupported，不因该判定触发 Warn；决策点改写返回 invalid 仍触发 Warn。正常提示替换不单独触发 Warn，不存正文/参数/密文/原始错误。

- `balance_paused`（含账号、暂停时长与上游原文；0.4.12 起另含档位 `step`）；
- `session_rebind`（P1-B 原因 `post_output_first_failure`，普通连击原因 `resil_failure_streak`，未知终止拒绝连击原因 `terminal_rejection_streak`；含会话哈希前缀、组、账号、类别、连击次数及避开时长；终止拒绝另带 `execution_scope` 表示是否使用线程作用域，不记拒绝正文或摘要）、`last_resort`；
- `openai.sticky_bypass`：原会话 owner 与最终 selected 不同的选号收尾诊断；含通用请求关联字段（`resilience.EventFields`）、`group_id`、脱敏 `session`、`owner`、`selected`、`reason`。每个请求最多一条。缓存确认实际删绑、成功迁移或原因未记录（`unrecorded`）为 Warn；删除失败、未执行删除、owner 比较未命中，以及保留绑定的临时放弃（如 `soft_owner_preserved`、`concurrency_full`、`capacity_overflow`、`owner_excluded`）为 Info。仅返回 error、不能报告实际删除结果的缓存也保守记 Info。`reason` 在原 owner 已于本请求失败、或本请求在 M3 状态换号处明确标记回避，并且列入本轮传入的排除集时优先为 `owner_failed_in_request`；该原因优先于判慢/质量迁移，即使缓存绑定已被前序清理也能归因。M3 标记仅作请求内诊断，不写 AccountFailed、不参与罚号或候选资格；从会话载入的历史回避不算这一标记，历史回避加本轮非失败排除仍取原有原因；其余取最终选号轮最先记录的原因码，包括 owner 状态/资格、请求门槛、容量与迁移原因；资格判断沿用对应 gate 的原因码。每轮内部重选（含代理隔离重试和原子绑定对账）清空原因与绑定变化标志，请求级日志去重标志保留。
- `same_site_hold`（`host_hash`、`account_id`、`peer_account_id`、`until`）；
- `fallback_group`、`fallback_return`；
- `budget_exhausted`、`capacity_switch_exhausted`；
- `stall`、`fast_fail`、`turn_cap`、`abuse_guard`；
- `state_rejection`（完整事件名 `resilience.state_rejection`）：原生与透传每次 Forward 结束时至多一次，桥接与 WS 在判断点记录；仅计数与标识，不存正文。含通用请求字段、`group_id`、`account_id`、`family`、`encrypted`、`path`（`native_http`、`native_sse`、`passthrough_http`、`passthrough_sse`、`chat`、`messages`、`ws`）、`stage`（`pre_output` / `post_output`；WS 缺少本 turn 输出事实时为 `unknown`）、`headers_committed`、距尝试开始的 `error_ms`、`client_version`。原生与透传另带最终 `rule_id`（来自 gin 键 `ops_error_passthrough_rule_id`，每次尝试清零；桥接与 WS 不带）。`collab_plaintext` 取本次尝试的 `Applied`，并带 `collab_calls`、`collab_efa_absent`、`collab_efa_empty`、`collab_efa_nonempty`、`collab_marker_added`、`collab_vetoed`、`collab_contradicted`、`collab_uncovered`。`frag_*` 统计 Forward 入口请求体；`attempt_frag_*` 统计最终拒绝尝试实际发送的请求体（取当次修复状态中的 body，未修复则取传入 body）。两组均有 `amsg`、`fco`、`other`、`reasoning`、`compaction`、`opaque_like`、`text_like`；`reasoning` / `compaction` 是 `other` 的子项，兼容总数不变。`family` 及 `state_rejection` 计数依据拒绝响应分类，不依据片段数量。片段只扫描协议位置，`frag_opaque_like` / `frag_text_like` 只用于形态诊断，不进入修复或改写决定；不递归工具参数或正文。WS 处罚入口没有 Gin / 当前请求体 / 本 turn 输出状态，只记录可得字段，省略 `headers_committed`、`client_version` 及片段计数，不从连接累计字节推断阶段；
- `wrapped_error_malformed`：Responses 原生/透传流内或非流式聚合出口识别到像包装帧但类型、状态或字段不一致时每帧一条，含 `account_id` 与 `path`，不记帧正文。输出前直接按通用协议失败处理，拒绝继续使用该帧的状态、code、message 触发请求修复、认证/403 账号副作用或错误透传规则；
- `post_output_failure`（含 `group_id`、`account_id`、`reason`、窗口内次数与是否施加冷却）。`model_unavailable_after_output` 只记失败轮次和观测，不调用会话失败或 P1-B；其 `failures=0`、`cooldown=false` 表示未施加账号级输出后冷却，号×模型计数和冷却另见 `openai_model_transient_state`，不带 `rebind_guard`。其他原因仅在已确认的 OpenAI 输出后失败、`PostOutputRebindOn()` 生效（包含 SessionOn）且请求有实际会话键时附加枚举 `rebind_guard`：`none` 表示未受守卫；`blocked` 表示实际守卫分支拦下 P1-B 首败改绑，仍可能按连击阈值改绑。不适用时省略整个字段，不以 none 代替。仅扩展既有事件，沿用按 attempt 去重（WebSocket 按 turn），不改变处理行为。请求状态已有实际会话键时，另带 `session`，读取 `RequestState.SessionIdentity()` 并与 `session_rebind` 共用 `shortSessionHash`（保留前 8 字节，短键原样保留）；无键时省略，不推导替代值。可用 `group_id`、`session`、`account_id` 和时间窗口关联后续同号输出后失败；摘要有前缀碰撞可能，无后续失败不等于下一请求成功。`request_id` 仍用于请求内关联。
- `undelivered_record_dropped`：未送达记录的后台写入全部占满，这条记录未写（含 `request_id`）；
- `last_resort_verify_failed`：末位兜底派发前复核账号时读库失败，按快照中的账号继续；
- `balance_lift_failed`（0.4.12）：请求成功后清除过期的余额暂停时写库失败（含 `account_id`）；原因保留，之后再遇余额不足仍从原档位往上升；
- `capability_learning_failed`（0.4.4）：仅流式学习结果写入失败（含 `account_id`、`protocol`、`error`）；本请求仍可在预算内换号，只是下一次不会跳过该账号；
- `web_search_history_rejected`：API Key 中转拒绝回放 web_search 历史，补声明重放后仍失败或无法重放（含 `account_id`、`upstream_status`、是否已重放、是否透传）。
- `m3_learned`、`m3_replaced`、`m3_replay_ok`：M3 分别记录写入 Bad、实际出站槽位替换、同号重放成功；含 `group_id`、`account_id`、`count`，不记密文或完整摘要。
- `m3_state_switch`：M3 回避当前号并消耗唯一状态换号额度；含 `group_id`、`account_id`、`switch_count`。
- `m3_terminal`：M3 在没有可用同号修复或换号额度时终止；含 `group_id`、`account_id`、`cause`。
- `committed_terminal`：M3 终止时下游响应头已提交，网关以 `response.failed` / `server_error` 收尾；含 `group_id`、`account_id`、`cause`。
- `last_resort_avoided`：末位兜底选择了会话回避集合中的账号，依据本次 fallback 已加载的会话集合（包括此前请求留下的回避），不依赖当前请求新写入的回避标记；含 `group_id`、`account_id`、`reason=session_avoid`、`candidates`、`penalty_reason`。

`resilience.last_resort` 与 `resilience.last_resort_avoided` 的 `candidates` 是本轮候选扫描数：普通末位兜底按分组扫描到的账号数，份额调度按份额计划完成 include 过滤后的候选数。`penalty_reason` 是按原因计数后稳定排序的 `reason:count` 摘要；同一个账号可能因多个独立条件计入多项，因此各项计数之和不必等于 `candidates`。

info 级，只写应用标准输出（量大，明细已有计数或未送达表）：

- `blank_placeholder_staged`（完整事件名 `resilience.blank_placeholder_staged`）：原生/透传 Responses 每次尝试实际暂存过空白占位后只记一条，含 `account_id`，不记录占位正文；
- `blank_placeholder_outcome`（完整事件名 `resilience.blank_placeholder_outcome`）：Responses handler 结束时，仅请求内曾实际暂存空白才记一条。`placeholder_timeouts` 只累计暂存过空白的尝试所发生的首输出超时，普通静默尝试不计；`final_outcome` 读取现有请求恢复结果，`budget_cause` 读取预算的耗尽原因（未耗尽为空）。用于关联下一跳结果和有占位请求最终预算耗尽的次数，不改写恢复结果；
- `keepalive_committed`（含 `group_id`、`protocol`、`edge`）；
- `client_gone_skip`（完整事件名 `resilience.client_gone_skip`，0.5.11）：客户端已离开时，网关跳过号失败标记、门槛软失败、输出后计数与冷却、会话改绑（含 P1-B）以及流超时计数，只记这一条，用于统计排空期间被跳过的上游故障。stream_timeout 的 client_gone_skip 只在处置开启（且 action 不为 none）时记录。含通用请求字段、`group_id`、`account_id`、`platform`、`reason`、`category`；`reason` 为输出后失败的原因（`stream_read_error`、`missing_terminal`、`stream_failed` 等）、`stall` 或 `stream_timeout`，`category` 为本应记入的类别（`stream_timeout` 为空）。同一次停滞可能同时出现 `stall` 和 `stream_timeout` 两条，统计时按 `reason` 过滤或按请求去重；
- `openai.sticky_bypass`：保留原绑定的临时放弃，字段与 Warn 事件相同；仅写应用标准输出。
- `unclassified`：只按状态码归类的上游 4xx 文案采样（脱敏、截断，每分钟约 10 条），发现新的余额文案时加入 `failure.balance_keywords_extra`；
- `undelivered`（含阶段、耗时、推断的 CDN 切断与 `edge`）；
- `transient_cooldown`、`transient_lifted`、`undelivered_cleanup`；
- `forbidden_cooldown`：失败处理启用时，OpenAI API Key 账号在输出前收到有结构化证据的流内 forbidden，进入账号×模型软冷却计数时每次一条。证据限于数字状态 403、校验通过的包装 401/403，或错误对象 `code=upstream_error` 且没有 `type`；包装帧内的停用/凭证码同样按此处理。仅文案推导、非包装帧的停用/凭证错误，以及包装或非包装帧的状态类拒绝、上下文超限、cyber 与内容安全拒绝不触发。含 `account_id`、`group_id`、`model`、`failure_streak`、`cooldown_ms`（首次为 0）；不进入 HTTP 403 连续失败计数；
- `content_policy_refusal`（上游内容安全拒绝，含账号、平台、状态与截断的上游原文）、`content_policy_no_failover`（换号循环因内容安全拒绝结束）；
- `web_search_replay`（补 web_search 声明后同号重放）、`web_search_declaration_learned`（重放成功，该账号 6 小时内同类请求直接补声明）；
- `request_repair_replay`（0.5.1）：网关修好上游点名的问题后同号重放（§3、§4.10），覆盖 OpenAI 原生 Responses 的 HTTP 与带内修复、Chat Completions 路径的被拒字段修复；透传与 WebSocket 路径的被拒字段重放沿用原有日志。含 `account_id`、`kind`（`encrypted_content` 或被拒字段的原因）与 `form`（`http` 或 `inband`）；Chat Completions 路径另带 `path=chat`。不记请求体；
- `pre_created_delta`（0.4.4）：上游在 `response.created` 之前发出非空 delta，含 `group_id`、`account_id` 与 `bytes`（只记字节数，不记内容）；
- `capability_learned`（0.4.4）：账号被学为仅流式，含 `account_id`、`protocol`、`expires_at`；
- `model_breaker`（0.4.9）：非 OpenAI 账号×模型新设一段冷却（§4.6），含 `account_id`、`platform`、`model`、`failure_streak`、`cooldown`；同轮内不再升级的失败不记。

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
| `prelude_release_event_keys` | 首次释放事件的顶层字段，按键名排序后用逗号连接为 `键名:JSON类型`；类型为 `object`、`array`、`string`、`number`、`boolean`、`null`，不展开嵌套对象、不记录值。最多 32 个键，完整摘要（含类型和分隔符）最多 256 字节；任一超限时只记 `[truncated;keys=N]`，N 为总键数。仅在下述范围内且有可解析的顶层字段时出现 |
| `prelude_release_item_type` | 释放事件的 `item.type` 或 `part.type`；非 item/part 事件或缺少类型时为空 |
| `prelude_release_item_keys` | 上述对象的字段名，排序后用逗号连接；最多 32 个键、连接后最多 256 字节，任一超限时只记 `[truncated;keys=N]`（N 为总键数），不保留任何键名片段或字段值 |
| `prelude_release_has_encrypted` | 上述对象是否带非空字符串 `encrypted_content`；没有时为 false |

事件层 schema 与 Q1 的 item/part schema 在同一个释放边界采集，随 `resilience.EventFields` 写入 `resilience.post_output_failure` 等系统日志。范围按 `ResponsesEventOutputDisposition` 的事件分支划分：标准顶层仅含 `type`、`sequence_number`、`output_index`、`item` 的 `response.output_item.added` / `.done` 省略 `prelude_release_event_keys`；这两类事件若出现其余顶层字段则记录完整顶层 schema。其他首次释放事件均采集，包括文本、content part、成功终态及未知事件；未知的 `response.output_item.*` 不因前缀相同而省略。没有先收到 created 的首个释放事件也适用。空对象、非对象或无法解析的 JSON 不产生此字段；每次尝试仅冻结首次释放的 schema。此观测不改变未知事件按可见内容处理的判定、开场暂存或失败出口。

`keepalive_committed` 发生在输出之前，`delivered_bytes` 与 `ms_since_first_output` 总是 0。日志不含正文、Key 或完整会话值。

0.5.1 起，`resilience.stall` 也带上述请求关联字段，另带失败时刻 `at_unix_ms`，与补项使用的失败时刻相同，可供对账。

`*_failed` 结尾的事件表示存储或查询失败，功能会降级为原有行为。

OpenAI 判慢降序与会话迁移的事件（见 §4.5）不带 `resilience.` 前缀。进入判慢期为 Warn。门槛组统一迁出使用 Warn `group_quality_gate.migrate`，含 `session_hash` 前缀、`group_id`、`from_account_id`、`to_account_id`、`trigger`（`tier`/`latency_hold`）；未开门槛组的旧判慢迁出仍为 Info，只写应用标准输出：

| 事件 | 含义与字段 |
|---|---|
| `openai_account_latency_hold_started` | 账号对某模型进入判慢期；`account_id`、`model`（实际出站模型）、`sample_count`（触发时衰减后的有效样本数）、`slow_fraction`（慢占比）、`hold_seconds`、`level`（1–4）、`hold_until` |
| `openai_account_latency_sticky_migrated` | 会话从判慢期的账号解除绑定；`from_account_id`、`alternative_account_id`（证明同组有健康替代的账号，不一定是本次最终选中的账号）、`model`（原绑定账号的实际出站模型，与上一项一致） |
| `anthropic_account_latency_hold_started` | Warn；Claude 账号进入判慢期，含 `platform`、`account_id`、`model`、`sample_count`、`slow_fraction`、`hold_seconds`（固定 180）、`hold_until` |
| `anthropic_account_latency_sticky_migrated` | Warn；属主比较删除成功；含 `group_id`、`model`（最终出站模型）、`from_account_id`、`alternative_account_id`（证明同段存在合格替代，不一定是最终选中的账号）、脱敏 `session` 和 `latency_hold` 原因 |
| `anthropic_account_latency_sticky_kept` | Info；含 `group_id`、`model`、`from_account_id`、`alternative_account_id`、`reason`、脱敏 `session`。原因：`session_off`、`no_target`、`no_free_slot`、`owner_changed`、`cooldown`；`no_free_slot` 包含新鲜负载查询失败或并发服务缺失。`owner_changed` 表示本请求未删除绑定：删除出错或缓存不支持属主比较删除时保留 owner，比较确认绑定已变时重新读取并使用新 owner，读不到则正常选号 |

慢失败与模型不可用的瞬时冷却沿用 `openai_model_transient_state`（warn 级），字段如下：

| 字段 | 含义 |
|---|---|
| `account_id`、`model` | 账号与实际出站模型；模型映射只执行一次 |
| `failure_streak` | 该账号×模型的连续瞬时失败次数；首次只记数，第二次开始冷却 |
| `cooldown_ms` | 本次施加的冷却毫秒数；没有新冷却时为 0 |
| `block_scope` | 固定为 `account_model`，不影响同号其他模型 |
| `slow_streak` | 连续慢失败次数；普通瞬时失败与明确模型不可用会打断慢失败连击 |
| `reason` | 计划内截止为 `planned_first_output_timeout`；HTTP 400/404 或 5xx，以及 Responses 原生/直通输出前后流内明确模型不可用为 `openai_model_unavailable`；API Key 普通瞬时失败不带此字段 |

每次计划内截止或明确模型不可用记一条，不新增计数器。计划内截止本身看计数 `first_output_timeout` 与运维记录中 Reason 为 `first_output_timeout` 的尝试。明确模型不可用的运维尝试分类为 `MODEL_UNSUPPORTED`；跨主机重复后终止的尝试为 `CLIENT_ERROR`。

## 6. 回退

协作消息明文通道的**软回退**是关闭账号开关：该账号后续请求停止转换，在途仍按冻结 plan 还原，部分上游的存量会话可能需新开。**硬回退**是关韧性总开关、collaboration_plaintext.enabled 或清空 models：所有账号的后续尝试停止转换。OAuth/清单外模型边界、实测结果与缓存代价见 §4.14；操作顺序见 [部署 runbook](../deploy/README.md#codex-协作消息明文通道)。无需重启，schema 豁免不随开关回退。

- 单项关闭：在“网关韧性”卡中关闭对应能力，即时生效。各能力关闭后的行为见 runbook §6。0.4.4 新增的开关关闭后回到 0.4.3 的对应行为，不是 0.3.0（两处小例外：关闭 `failure.decision_v2` 后分类输入的证据补全仍保留，见下表；关闭 `errors.failed_frame_retry_after` 后用户余额不足与内容拒绝仍不带重试时间，见 §3）；关闭 `capability.stream_only_learning` 后已学到的记录不删除，按到期时间自然过期，期间重新开启仍然生效。
- 状态拒绝的请求级分类、不罚号及 `state_rejection` 观测不受总开关影响，无新增设置。M3 同样生效；预算关闭时仍只允许一次状态换号。rs/cmp 有限修复保持既有行为。
- 整体关闭：关闭总开关，韧性能力全部停用，调度与错误处理按 0.3.0 逻辑运行。份额分组随之回到 0.3.0 的软溢出规则（300 秒内结束的状态都只临时借用、不改绑，余额暂停不特殊处理），健康降权与恢复爬升停用，见 [份额调度](SHARE_SCHEDULING.md)。
- 从 0.4.4 回退到 0.4.3：无迁移，直接换回镜像。仅流式学习记录留在账号 `extra` 里，0.4.3 不读，回退后不再过滤；再升级时未到期的记录仍然生效。0.4.3 读取设置时忽略 0.4.4 新增的字段，原有字段与行为不变。**在 0.4.3 上再次保存设置会丢弃新字段**：再升级到 0.4.4 时它们回到缺省值（全部开启），回退期间关掉过的新开关要重新关闭。回退后仍开着 0.4.4 管理页面的浏览器，保存会因未知字段被拒，刷新页面即可。
- 版本回退：迁移只追加，0.3.0 可直接运行在 0.4.0 的库上，无需回退数据库。回退时余额暂停中的账号在暂停到期后回到流量里，0.3.0 下再遇余额失败按原有逻辑处理（402 置错、403 计数）；不希望欠费账号被试探的，回退前关掉其调度。
- 实际生效的重试与尝试上限（多种机制取最小值）见 runbook §6“生效重试上限”。
- 0.4.9的首输出档位：在 `gateway_resilience_settings` 中设置 `budget.first_output_timeout_seconds`，不需要重新部署即可恢复旧档位，见 §4.5。
- 升级到 0.4.9的首事件字段：`budget.gemini_first_event_timeout_seconds` 缺省从 90 改为 60，且只管 Gemini；Antigravity 改读 `budget.anthropic_first_event_timeout_seconds`。设置文档里没有这两个字段时，Gemini 生效 60 秒、Antigravity 仍为 90 秒。在管理端保存过韧性设置的部署，文档里已写着当时的值（通常两者都是 90），升级后 Gemini 仍为 90，要用新缺省须改成 60；分别自定义过这两个字段的部署，Antigravity 的生效值从 Gemini 字段变为 Anthropic 字段，可能变化。
- 0.4.9的冷却行为：Anthropic、Gemini、Antigravity 账号不带 `Retry-After` 的普通 5xx 不再第一次就冷却整个账号 30 秒，改为按模型逐级冷却（§4.6）；OpenAI 按模型的普通瞬时屏蔽从 10/45 秒封顶改为逐级到 300 秒。这两项没有单独的开关，关闭总开关时一起停用。
- 输出后失败补记（0.5.1）不新增开关，只记录快层实际确认的失败；关闭韧性总开关时不产生补项，只关闭停滞检测时不产生 `stall` 补项，见 §4.11。

不受总开关控制的改动（多为缺陷修复，关闭总开关后仍然生效）：

| 改动 | 说明 |
|---|---|
| 全 0 会话 ID 视为缺失 | `metadata.session_id` 全为 0 时改用可缓存内容摘要做粘性键（具名补丁 `xy-placeholder-session-id`） |
| index.html 不返回 304 | 始终返回带本次 CSP nonce 的完整页面（`xy-index-nonce-no-304`） |
| 白名单 404 列出可用模型 | HTTP 入口的 404 文案附本分组白名单前 10 项（`xy-allowlist-404-hint`） |
| 分类输入的证据补全（0.4.4） | 分类时依次取调用方给的 code、type、message，再取 `error` 字段，最后回落到 `response.error.*`。关闭 `failure.decision_v2` 回到旧的分类规则，但这项补全保留，个别运维分类可能与 0.4.3 不同 |
| 换号与写出的协议正确性（0.4.4） | 只有还没向客户端实际写出、客户端未断开且请求未取消时才换号；是否“已开始输出”按实际写出的字节判断；客户端取消或写出失败（客户端已断开）不再被记成上游首输出超时，也不计账号失败；0.4.5 起，开启暂存时客户端恰好在第一次写出的同时取消，暂存期为取消而关闭上游造成的读错误或缺终态的结束按客户端断开处理（按已得用量计费、不计账号失败），不再当作输出后的上游断流。关闭 `stream` 段的开关只回退暂存与终态策略，不撤销这些修正 |
| 心跳只提交网关自身的 SSE 头（0.4.5） | Chat 与 Messages 经 OpenAI 兼容转换、raw Chat 透传，在第一个输出之前用心跳提交 HTTP 200 时（暂存期，或关闭暂存时上游第一行之前），只写 `Content-Type`、`Cache-Control` 等网关自身的流式响应头；这次尝试的上游响应头（如 `x-request-id`）不再写出，之后换号成功也不会带上失败账号的头。没有心跳、直接输出时仍带上游响应头，与原生 Responses 的暂存一致。原生 Responses 有一项受控例外（O3）：第一次心跳尚未提交时，只另带本次暂存的 `x-codex-turn-state`，并同时记录账号溯源，由溯源加出站守卫保护，详见下一行。OpenAI Responses 透传则在心跳提交 200 时带出这次尝试的全部上游头（含 `x-request-id`、`x-codex-*` 与 `x-codex-turn-state`），与 0.4.4 相同。原因是长推理请求通常先发心跳，只写网关自身的头会让最终成功的请求也拿不到 `x-codex-turn-state`，Codex 因此丢失回合内的状态连续性，可能损害缓存命中。代价是心跳之后换号成功时，客户端拿到的仍是第一次尝试的头（已知差异）。turn-state 的溯源只记到客户端实际拿到的那次尝试：响应已被前一次尝试的心跳提交后，后续尝试自己铸造的 turn-state 既不下发也不记录，所以客户端下次回带第一次尝试的 blob 时，出站守卫照常按溯源剥离，不会发给其他账号的上游 |
| 原生 Responses 心跳保留 turn-state（0.4.5 O3） | 首个语义输出前，若原生路径第一次心跳将提交 HTTP 响应，且本次尝试的暂存头含 `x-codex-turn-state`，只提前写出这一个上游头，并在写心跳的同一时刻记录账号溯源。`x-request-id` 和 `x-codex-*` 用量头仍暂存到首个语义输出；响应已由心跳提交时不再补写。后续心跳不刷新溯源；换号到 B 后也不替换 A 的头或溯源，客户端下次回带时发往 B 的请求剥离该值、发往 A 的请求保留。上游没有 turn-state，或 CDN 首包保活、排队心跳已经提交响应时，不补头、不记录溯源；没有心跳则沿用首个语义输出时一次提交全部暂存头的行为。门票账号仍按其管理模型的门票覆盖出站 turn-state。此项属于协议修正，不新增开关，也不受总开关控制 |
| raw Chat 取消后的截断（0.4.4） | 账号不支持 `/responses` 时的 raw Chat 透传，判断客户端是否中止时也看请求是否已取消（`clientAborted` 含 `ctx.Err()`）：取消之后的截断按成功处理、按已得用量部分计费，不记上游失败；开启 `stream.strict_terminal` 时，取消之后收到的 `error` 同样不记上游失败、不换号。这项判断本身不受 `stream.strict_terminal` 控制 |
| 排队心跳按入站协议的格式（0.4.4） | 等待并发槽位或用户消息队列时，Responses、compact、Chat 收到 SSE 注释 `:`，Messages 收到 `ping` |
| 排队 ping 之后按协议写终止帧（0.4.4） | Gateway 入站的 Responses 与 Chat 在 HTTP 已提交后（如排队等待时已发出 ping）遇到计费、在途预留、模型等错误，写 `response.created` 后跟 `response.failed`，或 `error` 后跟 `[DONE]`，不再写裸 JSON；未提交时状态码与正文不变 |
| 心跳后命中错误透传规则（0.4.5） | OpenAI Responses 流（普通路径与透传）只写过心跳（上游注释、网关心跳或保活协程）、200 已提交时，流内 `response.failed` 命中管理员配置的错误透传规则，改为写 `response.created` 后跟 `response.failed`（同一 id，序号 0、1），带规则的类型与消息，是否带重试时间按规则状态码与 `RetryAdviceEligible`；以前在心跳后追加裸 JSON。未提交时仍按规则的状态码返回 JSON |
| OpenAI Responses 透传的有界暂存（0.4.5） | 第一个输出前的开场事件改用原生路径的暂存（8 MiB 上限，超过 64 KiB 写入已解除链接的临时文件），超出上限时换号；以前在内存中不设上限 |
| Gemini 5xx 与 408 交给通用错误处理（0.4.9） | 以前 Gemini 只把 401、403、529 交给通用错误处理，其余非 429 直接返回；现在 5xx 与 408 也交给它，账号的临时不可调度规则与原版对 5xx 的处理同样生效，自定义错误码未命中时仍跳过。模型级冷却（§4.6）仍随总开关 |
| 报错后的路由、出口与绑定（0.4.12） | 余额不足、模型不可用与 402 `deactivated_workspace` 在各平台换号，明确的模型不可用让该账号对该模型 30 分钟不可用，这几类耗尽后不清空排除集重选；原生 Responses 本地失败写 `response.failed`；Messages 的 Claude Code 专属拒绝返回 403；在途预留的 429 标为业务限制；排队与并发上限的 429 带 `Retry-After: 5`；客户端错误消息去掉上游标识；成功后的会话绑定不随客户端断开取消；OpenAI 原版选号的替补按会话稳定哈希排序。见 §3、§4.9 |
| 报错补漏（0.5.1） | 加密内容拒绝按请求级处理（不换号、不罚号，临时不可调度规则与自定义错误码不作用于它）与同号剥离重放；被拒字段修复，含 Chat 路径与带内形态；Alpha Search 能力类 400 换号；Grok 精确 422 换号并在耗尽时返回 503；alpha search E5001 换号并在耗尽时默认返回 502（回合封顶时为 503）；alpha 非 2xx 不计成功，只有 HTTP 状态 ≥ 400 时登记错误尝试；Codex 子 agent 的父线程亲和及其会话键作用域修正；流内余额码换号（暂停仍随总开关与 `balance.enabled`）；上游内容安全拒绝在运维记录中标为业务限制，见 §4.10。Grok 精确 422 与 E5001 换号、alpha 非 2xx 的成功上报修正及网关终态状态记录不受韧性总开关回退；尝试事件的 `resilience_category` 仍随总开关。 |
| 上传中断记 499 | 读请求体时连接中断造成的 400 在运维记录里记为 499 |
| §9.8 续链被拒整轮重放 | Claude 请求分发到 GPT 中转、续链以笼统 4xx 被拒时去掉续链重放一次并关闭该会话续链（0.4.2 收窄判定，见 §3） |
| `/ready` 就绪检查 | 检查数据库与 Redis，见 §2.2（0.4.2） |
| 并发槽位的进程租约 | 新进程启动时保留仍有租约的其他进程的槽位，见 §2.2（0.4.2） |

管理端只读功能（选号解释中的模型限制提示等）也不受开关影响。

## 7. 已知限制

- 模型不可用的账号×模型瞬时冷却已覆盖 HTTP 错误响应，以及 Responses 原生/直通的流内失败（§4.9）。Chat/Messages 兼容流、非流式聚合、WS 与 HTTP bridge 的流内模型错误仍保留既有处理，本次未扩展。

- 输出后失败补记仅覆盖 HTTP，WebSocket 不补；缺少有效发送边界且本请求已有同账号事件时保守跳过，可能漏记。历史记录不回填，见 §4.11。

- Anthropic 没有显式 metadata session 时，兜底粘性键由客户端 IP、规范化 User-Agent、API Key、Group、规范化 system 和首条有效 user 文本构成；system/user 文本会去掉完整的 `<system-reminder>...</system-reminder>` 区间并折叠空白，system 的每段文本只去掉开头的 `x-anthropic-billing-header:` 行。Claude Code prompt 中的日期句、cwd、git status 与 `## Session Context` 环境段不做通用删除，顶层日期按天变化时可能每天换键。会话升级后首次请求会使用新键并重新绑定；同一 Key、Group、IP、UA、system 和首条 user 完全相同的独立对话会共享账号绑定及韧性失败状态。
- 统计计数保存在进程内存，多实例部署时按实例分别统计，重启后清零。
- WebSocket 不挂连接级预算，建连首回合的换号仍按 `max_account_switches`。Gemini 文本请求的尝试截止与输出后停滞检测见 §4.7（0.4.9）；0.4.8 及以前 Gemini 三个入口都没有尝试截止。
- Chat Completions 与 Messages 经 OpenAI 兼容转换时（0.4.2），首输出档位约束等待响应头和第一行上游数据。0.4.4 起开场事件推迟到第一个内容（`stream.prelude_staging`），首事件守卫随之延长到第一个内容，此前的失败可以换号；第一个内容写出之后的静默由停滞检测结束并记账，不再换号；关闭该开关时回到 0.4.2 的行为（第一行之后写出开场，不再换号）；如果此后上游只发中转站伪保活帧、不出内容，按 §7.8 算存活，停滞检测不会结束它。停滞检测未覆盖或不记账的路径：原生 Chat Completions 透传（账号不支持 `/responses` 时）没有停滞检测；CN Anthropic 原生上的 Responses 与 Chat 转换停滞时只结束请求、不冷却账号；Anthropic API Key 透传、Bedrock、CN Anthropic 原生 Messages 记账但不分输出前后，统一按 `stream_data_interval_timeout`（0.4.12 起 API Key 透传开启 `stream.prelude_staging` 时，第一个内容前的停滞改为换号，之后的停滞仍这样记账）；Antigravity 与图片按各自的规则。
- 0.4.4 的可重放前缀与明确终止只覆盖 OpenAI Responses（含透传的开场判定）、Chat Completions 与 Messages 经 OpenAI 兼容转换、raw Chat 透传。以下路径仍按 0.4.3 的开场与终止行为：Gemini 原生与兼容转换、Bedrock、CN Anthropic 原生、Antigravity（T2）。0.4.5 补上 Gateway 入站经 Anthropic 上游的 Responses 与 Chat 反向转换，以及 OpenAI Responses 透传的首输出截止与停滞检测。0.4.12 补上原生 Anthropic 与 API Key 直通的开场暂存（§4.9）。
- 0.4.12 起原生 Anthropic 与 API Key 直通在开场暂存期失败、已换号的尝试不向用户计费，也不产生该尝试的用量记录；上游可能已对这次尝试的输入计费，这部分成本由网关承担。以前原生 Anthropic 在 `message_start` 之后失败时，按它带的输入用量向用户计费并把错误返回客户端。
- 余额暂停的升档与成功复位（§4.9）需要每次尝试的开始时间，Responses WebSocket 与 WS→HTTP 桥接还没有记录：这些路径上的余额不足照常暂停，但不升档，成功也不复位。HTTP 文本与图片入口已覆盖。
- 替补账号的稳定哈希（§4.9）只在候选、负载窗口、费率与 compact 分层、近期成功记录、缓存证据与判慢期都不变时保持同一个替补；负载波动跨过 20 个百分点的边界时，替补会换。
- 兼容转换推迟开场期间的注释心跳会提交 200，此后的失败只能以带内终止帧告知客户端（与原生 Responses 相同）。
- 仅流式学习只认表中确定的结构码与文案（如 “only accepts streaming requests”、“stream must be true”）；其他措辞的同类拒绝仍按客户端错误返回。只覆盖 OpenAI 入站的 Responses 与 Chat 普通文本请求，以及 OpenAI、Grok 平台的账号；compact、图片、WebSocket、Messages 经 OpenAI 转换，以及 Gemini、CN、Bedrock 不学习、不过滤。只在上游协议与入站相同、上游流模式跟随客户端的路径上过滤；跨协议转换（如 Chat 转 Responses 上游）、按模型路由的路径与路由无法确定的 Grok OAuth Chat 不过滤。错误的 `param` 指向 `stream` 以外的参数，或请求体里本来就有这句话（例如写在提示词里）时不学习；`"param": null` 或空值不影响学习。学习结果存在账号 `extra`（`capability_learned_responses`、`capability_learned_chat`），绑定账号的上游配置：改了上游配置的账号不受旧记录影响；复制、创建、导入与导出账号都不带学习结果。
- 0.4.4 容量判据收紧后，“Codex is at capacity”与非 503 上的 “servers are currently overloaded” 不再算容量降载，会让账号进入软冷却并计入会话失败；Grok 重试耗尽的原始 429 不再转成无号 503，客户端看到 429。
- 0.4.4、0.4.5 把 `response.metadata` 算作输出（0.4.6 修复，见 §1）：OpenAI Codex 后端过载时先发 created、in_progress、`response.metadata`，再发 `server_is_overloaded`。这两个版本不换号，错误直接到客户端，日志为 `gateway.failover_suppressed_after_semantic_output`；没有开关能绕过。0.4.3 也同样不换号。0.4.6 起 `response.metadata` 也不再解除首输出截止：发了它之后只有心跳、超过档位（180/200/220 秒）仍无正文的尝试会换号，而以前只要不断有心跳就一直等到正文。开场白名单开启时，缺省的语义首字时间 `first_token_ms` 也不从 `response.metadata` 起算（它与其他开场事件一样算元数据）。关闭 `stream.native_prelude_whitelist` 回到以前的行为。0.4.9起热尝试的档位为 20/25/30/30 秒，冷尝试另有下限，见 §4.5；`codex.rate_limits` 同样判为开场事件。
- 0.4.4 的已知缺陷 RA-1（0.4.5 已修复，见 §3“上游给出的等待时间”）：耗尽由上游带 `Retry-After` 的容量失败造成、且组里没有已受罚的账号时，0.4.4 合成的 `response.failed` 不带 `error.headers.retry-after`，正文也不再附 “try again in Ns”（0.4.3 的正文有）。仍在运行 0.4.4 时，需要 0.4.3 的正文可以把 `errors.failed_frame_retry_after` 设为 false。
- 原生 Responses 路径上，上游在 `response.created` 之前发出的非空 delta 照常先转发（网关不重排上游的真实内容），这类上游下严格 SDK 仍可能报 “Expected to have received response.created”。
- 仅流式学习记录绑定账号凭据：OAuth 类账号刷新令牌后会重新学习一次。学习粒度是“账号 + 协议”，不分模型：中转只对某个模型要求流式时，该账号所有模型的非流式请求都会被跳过，可以在账号列表手动清除。
- 断开阶段粒度为上传请求（0.4.3）、选号、等待响应头、首输出前、输出中五段。Chat Completions 与 Messages 经 OpenAI 兼容转换的路径，上游响应头到达后阶段不推进到“首输出前”：只写出开场分块（`message_start`、只带 role 的 chat 分块）就断开时，未送达记录与计数的阶段记为“等待响应头”。Gemini 三个入口 0.4.9起按 §4.7 记等待响应头与首输出前，0.4.8 及以前停在选号。这只影响按阶段的统计，不影响是否计费、是否取消上游（两者只看是否写出过内容）。
- 兜底判断、末位兜底与无号 `Retry-After` 读取分组账号的进程内快照（2 秒）。本网关施加的冷却与余额暂停会立即刷新快照；其他来源（上游 429 限流、错误状态、管理员改动、其他实例）最多 2 秒后可见。末位兜底在派发前按账号当前状态复核所选账号，快照的滞后只影响兜底判断与 `Retry-After` 的估算。按模型的冷却（§4.5、§4.6）在进程内，`Retry-After` 读到的是当前实例的状态。过期的快照在下一次写入或每分钟一次的会话标记清理中删除，不再常驻内存（0.4.5）。
- 失败回合计数在进程内，多实例时各自计数；快速失败的原样返回发生在选号之前，不参与计数；封顶后的终止应答也不计入快速失败。
- 未送达表固定保留 30 天。
- 滥用保护只在上游返回用量时按成本计费。
- 兜底分组仅支持标准（余额）分组；进入兜底的记忆按 API Key 而非会话。主组健康且本实例近 `binding_ttl_seconds` 内未转出任何 Key 时不读 Redis，因此重启前转出的 Key 重启后立即回到主组。
- 按单实例部署设计。会话失败与回避、末位兜底、兜底转移各有一份进程内索引（用于省去 Redis 往返）；多实例时，另一实例记下的回避不会被本实例读取，本实例的成功也不会清零另一实例的计数。已输出后失败的账号计数、需要补 web_search 声明的账号记忆也在进程内，多实例时各自维护。蓝绿切换的重叠期内两个进程共用 Redis 是支持的（并发槽位按进程租约保留，见 §2.2），长期多活需另行评估上述进程内状态。
- 内容安全拒绝的提前结束与 web_search 补声明重放未接入 Responses WebSocket 路径（账号免罚已生效）；Grok 的进程内阻断（模型配额、团队限流）不显示在选号解释中。OpenAI 上游连续 5xx 后的按模型瞬时屏蔽（10/45 秒；0.4.9起逐级到 300 秒）可作为末位兜底，屏蔽期间绑定在该账号上的会话保留绑定（0.4.2）；它不显示在选号解释中，0.4.8 及以前也不参与无号 `Retry-After` 的估算（0.4.9 起参与，见 §3）。0.4.9的慢失败冷却与判慢期（§4.5）同样不显示在选号解释中。
- 韧性设置的保存没有版本校验：两位管理员基于旧页面先后保存时，后保存的覆盖先保存的（份额策略有版本校验）。单管理员维护时无影响；多人维护时保存前先刷新页面。
- 未送达的计数按“开场事件之后是否写出过内容”判定，Gemini 原生流等不带这些开场事件的路径，第一个数据块即算送达。
- Codex 子 agent 的父线程亲和（§4.10）按本次请求的模型查父绑定：子线程与父线程模型不同时（自动审查即如此）查不到。父线程已迁号或绑定过期、父号在输出前失败后换到的号解不开、历史密文来自多个账号时，仍按请求级失败结束。
- 带内 rs/cmp 剥离仍只接入 OpenAI 原生 Responses；M3 覆盖原生与直通 Responses 的 HTTP、输出前流内及聚合出口。原生带内被拒字段修复保持原有范围；直通带内被拒字段修复仅在本次命中 M3 信号（含已校验的包装 400）或当前账号已有 M3 替换槽位时启用，普通直通字段拒绝不新增重放。Chat Completions、Messages 与 WS HTTP 桥不接 M3（Chat 只修 HTTP 被拒字段）；WebSocket 沿用自身的加密恢复。

## 8. 与份额调度的关系

份额调度（[说明](SHARE_SCHEDULING.md)）复用网关韧性的判定：

- **软溢出**：份额分组的绑定账号只在韧性判为软罚、且在 300 秒内恢复时保留绑定、临时借用其他账号；硬罚（含余额暂停）下一次请求即改绑。
- **健康降权**：份额权重的健康系数只统计韧性会给短时冷却的失败类别（瞬时错误、首字超时、停滞），数据来自运维记录 `ops_error_logs.upstream_errors` 中每次尝试的 `resilience_category`。余额、鉴权、限流与降载已有各自的处置，不重复扣分。
- **恢复爬升**：余额暂停到期、硬罚到期（含超过 300 秒的限流），或本实例观察到账号连续至少 5 分钟不可用后重新可用（按 60 秒采样），份额分组里该账号的新会话权重在 10 分钟内从 10% 回升到 100%；暂停与冷却本身不变。
- **末位兜底**与**会话连续失败改绑**在份额分组中照常生效，见上文。
- 关闭韧性总开关时，以上份额行为全部回到 0.3.0。

