# 测试服务器验证与预上线方案

> 目标：0.4.0 候选在测试服务器上经故障注入、真实客户端、真实 CDN、升级回退与浸泡全部闭环后，才提 PR、发版（D24）。
> 凭据（SSH、真实测试 Key、数据库口令）只保存在测试服务器的 `.env`（0600）或本机临时目录，不写入 Git、PR、记忆或报告。
> 本仓库公开。测试服务器的地址、已有服务与端口清单只记在仓库外的私有清单 `xy2api-artifacts/gateway-resilience-20261015/private/HOSTS.md`。

## 1. 服务器现状（2026-10-15 只读勘察）

- Ubuntu 26.04，8 核，15G 内存（可用约 9.4G），根盘可用 133G。
- 这是共享开发测试机，已在运行多套服务，**本方案一律不碰**：
  - 一套旧版 xy2api（0.2.5）和一个旧测试站；
  - 两套 XY-image，**其中一套经私网与公网备份路由调用生产主站**；
  - 一个独立的 Sub2API 实例，以及若干主机服务。
- Caddy 已承载 7 个站点。测试域名所在区在 Cloudflare 上，现有记录都是灰云直连。
- 部署前后对比已有容器的 ID 与启动时间、Caddy 站点列表，作为“未影响已有服务”的证据。

## 2. 环境布局（D28）

| 项 | 规划 |
|---|---|
| 目录 | 测试服务器上的独立目录（路径见私有主机清单）：`compose.yaml`、`.env`（0600）、`src/`、`fault-upstream/`、`cdn-sim/`、`fixtures/`、`evidence/`、`data/` |
| Compose 项目 | `xy2api-staging`；网络 `xy2api-staging-net`（内部） |
| 服务 | `app`（候选镜像，127.0.0.1:18210）、`postgres`（18，仅内部）、`redis`（8，仅内部）、`fault-upstream`（127.0.0.1:18211）、`cdn-sim-90`/`cdn-sim-125`/`cdn-sim-300`（127.0.0.1:18212–18214） |
| 升级演练库 | 同一个 postgres 中的独立数据库 `xy2api_upgrade`，与功能测试库隔离 |
| Caddy | 新增独立片段 `xy2api-staging.caddy`，在主配置末尾 import；改前备份、`caddy validate`，然后平滑 reload |
| 域名 | 两个测试子域：一个灰云直连、一个橙云经 Cloudflare（名称只记在仓库外私有目录，场景脚本从测试服务器 `.env` 的 `STG_DIRECT_HOST`、`STG_CF_HOST` 读取）；ESA 测试站点视条件而定。**DNS 修改需用户授权** |
| XY-image 测试实例 | 独立目录，使用 XY-image 仓库的 `deploy/xy2api-lab` 或精简自托管栈，端口与已有两套错开；`XY2API_BASE_URL` 指向 `stg-api` |
| 资源上限 | 每个容器设内存与 CPU 上限，并设 `oom_score_adj`，避免影响已有服务 |

## 3. 镜像与版本

- **开发期：**
  - 用 `git archive` 把分支源码同步到 `src/`；
  - 在服务器上 `docker build`，镜像打 `xy2api-staging:<短提交号>` 标签，并记录 digest；
  - 前端嵌入构建与正式发布流程一致。
- **RC 期：** 直接使用 GHCR 上的 `0.4.0-rc.1` 镜像（固定 digest），复跑关键子集。
- 每次部署都记录：提交号、digest、迁移数、设置快照。

## 4. 数据（D30）

- **功能测试库：**
  - 全新安装后，用 `fixtures/` 脚本经管理接口建立夹具：

    | 夹具分组 | 用途 |
    |---|---|
    | G-single | 单账号，原版调度 |
    | G-pair | 双账号，原版调度 |
    | G-share | 份额调度，两层，每层两个账号 |
    | G-fallback-primary / G-fallback-backup | 无号兜底 |
    | G-claude-only | 仅限 Claude Code |
    | G-pricing-restricted | 渠道定价限制 |
    | G-image | 图片 |

  - 另建若干用户与 Key，包括小余额用户（用来验证在途预留 429）。
- **升级演练库：**
  - 从生产只读导出表结构：`pg_dump --schema-only`，不带数据；
  - 用 `tools/gateway-resilience/synth_data.py` 生成同量级数据：usage_logs 约 330 万、ops_system_logs 约 580 万、ops_error_logs 约 37 万、usage_billing_dedup 约 330 万、accounts 约 400、users 约 1900、api_keys 约 2.5 万；
  - 真实用户数据不离开生产。

## 5. 上游（D29）

- **故障注入上游**（`fault-upstream`）：每个场景有固定 ID，由请求头 `X-Fault: <id>[;param=…]` 或账号 base_url 路径前缀选择：

| 场景 ID | 行为 |
|---|---|
| F01 | 慢响应头（参数：秒） |
| F02 | 先 200 SSE，N 秒后 `response.failed`：busy |
| F03 | 同 F02：rate_limit_exceeded |
| F04 | 同 F02：insufficient_quota |
| F05 | 同 F02：context_length_exceeded |
| F06 | 已输出后停滞（参数：秒） |
| F07 | 502 Cloudflare 错误页，`retry_after:60` |
| F08 | 524，`retry_after:120` |
| F09 | 503 无正文 |
| F10 | 402 余额 |
| F11 | 403 “insufficient balance” |
| F12 | 429 “Insufficient account balance” |
| F13 | 429 带重置时间 |
| F14 | 429 无重置时间 |
| F15 | 401 invalid key |
| F16 | 529 / `server_is_overloaded` |
| F17 | 正常长推理（参数：首字秒数） |
| F18 | 图片长耗时（参数：秒） |
| F19 | `/v1/usage` 余额（可切换为 0 或正数；余额精简后网关不再探测，保留作中转模拟） |
| F20 | new-api 计费接口（同上） |
| F21 | Anthropic 流式，先 `message_start` 后停滞 |
| F22 | Gemini 流式 |
| F23 | 慢流：每隔 `every` 秒一个 delta，持续 `secs` 秒后正常结束 |
| F24 | 中转伪保活：空 `SSE-Keep-Alive` delta（一个在 `response.created` 之前），随后 `response.failed` busy |
| F25 | 号池中转：带 `previous_response_id` 的 Responses 请求得到笼统 400，其余正常 |
| F26 | 先输出若干 delta，`after` 秒后流内失败（busy 或 rate_limit_exceeded）（生产 R2） |
| F27 | 智谱形状的内容安全 403（顶层 `code: request_check_blocked`）（生产 R4） |
| F29 | Codex 后端中转：输入回放 `web_search_call` 而未声明 web_search 时，200 SSE 后 `response.failed` “response protection is unavailable”，其余正常（生产 R3） |
| BAL | 余额（`key=…`，经 `/_control` 设置）为正时正常，否则同 F12；`reset=秒` 时另带中转的请求窗口耗尽头（生产 R1） |
| MIX | 每次按 `p`% 的概率随机注入一种故障（浸泡用） |

- **真实测试 Key：** 由用户提供少量专用中转 Key，只配低价模型，总预算不超过 20 美元。每次真实测试前后记录余额，用于真实客户端与 new-api 余额接口实测；生产 Key 一律不复制。

## 6. 测试矩阵

### 6.1 规格场景（自动化）

8 个 spec 中的每个 Scenario 都对应一个测试编号 `S-<spec缩写>-<序号>`，例如 `S-CLS-1`、`S-PEN-3`、`S-SES-2`、`S-FB-4`、`S-BUD-2`、`S-KA-5`、`S-DC-3`、`S-ERR-6`：

- 能在 Go 集成测试中覆盖的，用真实 PostgreSQL/Redis 加 `fault-upstream`，纳入 CI 的“Gateway resilience”作业；
- 依赖时间或 CDN 的，在测试服务器上用脚本执行，输出 JSON 证据。

0.4.2 新增（`tools/gateway-resilience/staging/scenarios.py`，夹具 `G-msg-long`、`G-silent`、`G-lease`）：

- **DC-6**：只写出开场事件就断开。Messages 兼容转换（`message_start`）、Chat Completions、Anthropic 原生（`message_start` 后静默）各一次，5 秒后客户端断开：上游被取消、没有用量行、写一条未送达记录（审查 R03）。Chat 的上游走 `/v1/responses`、立即发出 `response.created`，场景断言客户端收到了只带 role 的分块；Messages 与 Chat 兼容转换的未送达阶段记为 `header_wait`（已知限制），Anthropic 原生为 `pre_output`。
- **FO-2、FO-3**：上游给出响应头与 `response.created` 后完全静默，xhigh 在 200 秒档位、max 在 220 秒档位换号并由第二个账号完成，不在 180 秒处以 `stream_timeout` 结束（审查 P1-C）。
- **LEASE-1**：第一个进程在并发 1 的账号上处理请求时，同库同 Redis 启动第二个进程（compose profile `overlap` 的 `app2`，端口 18216）并经它请求同一账号：第二个请求等第一个结束后才到上游，两个进程都正常服务（进程租约）。场景结束时删除 `app2`。`wait_ready` 只认 `{"status":"ready"}`，不认任意 200。
- **RDY-1**：候选镜像的 `/ready`、`/health` 由后端自己应答（JSON），不被嵌入的前端接管。0.4.2 第一个候选在测试服务器上 `/ready` 返回了 `index.html`，因此补了这个场景。

### 6.2 真实客户端端到端

| 客户端 | 路径 | 场景 |
|---|---|---|
| Codex CLI 与 Desktop（最新版）、Claude Code（最新版）、OpenAI Python/Node SDK、Anthropic SDK、OpenCode（AI SDK）、go-genai/python-genai、curl | 直连 `stg-api`；`cdn-sim` 90/125/300；真实 CF（`stg-cf`） | 首拍前失败（应得原状态码与 `Retry-After`）、心跳后换号成功、预算耗尽、已输出后忙或限流、停滞、断开、单号组余额耗尽、单号组瞬时故障（末位兜底）、兜底组切换与切回、配置类 503、快速失败 |

每个用例记录：客户端版本、回合请求数、最终错误文案、端到端耗时、网关日志中的分类与动作。验收要点：

- Codex 一个失败回合的请求数 ≤5；
- Claude Code 能显示 `event: error` 的真实原因；
- OpenAI Python SDK 在网关自行结束的 Responses 流上读到 `response.failed`（先收到 `response.created`），不报协议错误（`C-openai-py-budget-cdn`，RC 后补）；
- Gemini SDK 不受心跳影响；
- 经 CDN 的长请求不再被切断。

### 6.3 真实 CDN（D31）

经授权建立 `stg-cf` 橙云记录后，用真实客户端经 Cloudflare 访问，验证：

- 首字超过 125 秒的请求不再被切断；
- 心跳格式；
- `Retry-After` 透传；
- 边缘标记只出现在 CF 请求上。
- 0.4.1：去掉 `stg-cf` 站点的 Caddy 标记（只保留删除客户端自带的 `X-XY-Edge`）后，CF-1、CF-4 仍在约 15 秒开始心跳并穿过 125 秒，即应用按 `CF-Ray`/`CDN-Loop` 自动识别；直连（CF-2、CF-5、KA-2）与内部请求（CF-6，直连应用端口、XY-image 的 User-Agent）不提前提交。

有 ESA 测试站点时同样验证 90 秒与 300 秒。

### 6.4 XY-image（D32）

XY-image 0.0.17 测试实例接入测试 xy2api，覆盖：

- 对话：普通、上下文超长压缩、心跳后错误；
- 同步生图：Gemini 长耗时；
- 异步生图：批量、每 Key 在途上限、`too_many_tasks`；
- 余额不足提示、未送达核对、Caddy 错误标记。

同时用 XY-image 0.0.16 对测试 xy2api 跑一遍兼容性。

## 7. 升级、回退与前滚（升级演练库）

1. 在 `xy2api_upgrade` 上先启动 0.3.0，确认健康。
2. 换成 0.4.0 候选：记录迁移 272–276 的耗时、锁等待与表大小变化，确认没有长时间锁住 usage_logs。
3. 回退到 0.3.0 镜像：健康、登录、网关请求、计费都正常，新列与新表被忽略。
4. 再前滚到 0.4.0：设置与数据保持。
5. 还原演练：用 pg_dump/pg_restore 做一次成套备份与恢复（含 `settings`）。

脚本 `upgrade_drill.py` 的旧版本缺省为 0.3.0，可用 `UPGRADE_FROM` 指定。0.4.2 从上一个正式版 0.4.1 演练（`UPGRADE_FROM=ghcr.io/liulixin-lex/xy2api:0.4.1`）：0.4.2 没有新迁移，重点核对升级后网关与设置正常、回退到 0.4.1 后登录、网关与计费正常、再前滚时设置保持。

## 8. 浸泡（6.5 小时）

> 原定 24 小时；用户 2026-10-16 决定缩短为 6.5 小时，同时以 GC 后存活堆判定泄漏（见验收）。

- **负载：**
  - 并发 20–50，混合 Responses、Chat、Messages、Gemini、图片，流式与非流式按 7:3；
  - 每类故障按约 5% 注入；
  - 每小时人为让一个单号组的账号余额耗尽，再恢复（余额精简后该账号按 `balance.pause_seconds` 暂停，到期由真实请求重试）。
- **监控：** 协程数、并发槽位、在途预留余额、Redis 键数量（会话计数、回避集、锁）、PostgreSQL 连接、进程内存、日志量。进程内存以容器 cgroup 的匿名内存（堆与栈）为准；`docker stats` 还计入随日志写入增长、可回收的页缓存，只作参考。
- **验收：**
  - 结束时各项回到基线；
  - GC 后存活堆（应用以 `GODEBUG=gctrace=1` 运行）去掉第一个小时后，完整小时中位数的斜率不超过 1 MiB/h（至少三个完整小时）。匿名内存含运行时保留而未归还的内存，诊断中曾在堆持平时仍缓慢上升，只作参考；
  - 匿名内存同时不得明显上升：负载最后六分之一的中位数不超过预热后（第二个六分之一）中位数的 1.2 倍加 16 MiB（取不到匿名内存时退回为：结束时 `docker stats` 不超过基线的 1.5 倍或基线加 64 MiB）；
  - 没有孤儿预留与槽位；
  - 没有 panic；
  - 日志中没有凭据；
  - 未送达记录与用量没有重复。

## 9. 退出条件（全部闭环，D24）

- [x] 8 个 spec 的全部 Scenario 都有通过证据。
- [x] 真实客户端矩阵通过；Codex 一个失败回合 ≤5 次请求。
- [x] `cdn-sim` 三档与真实 CF 验证通过。
- [x] 升级、回退、前滚与成套备份恢复通过。
- [x] 浸泡通过（6.5 小时，用户决定）：`934bcf2bc` 11 项检查全部通过，存活堆斜率 0.3 MiB/h。正式版候选 `c6b837dca`（晋级清单 A1–A5 与余额精简）整套复验一次：场景 45/45、真实客户端 17/17、升级演练（含迁移 277）、浸泡 11 项全过，存活堆斜率 0.4 MiB/h。
- [x] XY-image 0.0.17 实验室与兼容测试通过；0.0.16 对 0.4.0 兼容。
- [x] design §14 的指标在测试环境回放中达标：15 项达标；未达标的无号 503 次数、连续无号时长与缓存读取占比为合成负载所致（人为断余额的单账号分组、故障上游的固定用量），在生产部署后的 24/72 小时报告中复核。
- [x] 未关闭的 P0/P1 缺陷为零；已知限制都写进 PR 与文档。
- [x] 上游同步审计、scheduler-diff、lint、全量单测与集成测试通过。集成测试在测试服务器容器内运行，唯一失败项 `TestPgDumperRealPostgresBackupAndAtomicRestore` 因容器缺 `pg_restore`，v0.3.0 同样失败，由 PR 的 CI 复核。

全部勾选后，才进入 tasks 第 13 节（PR 与发版）。

## 10. 证据与报告

- 测试服务器的 `evidence/<日期>/<测试编号>/`：输入、命令、输出 JSON、相关日志片段（脱敏）、退出码。
- 汇总报告 `STAGING_REPORT.md`，同时复制到本机仓库同级目录 `xy2api-artifacts/gateway-resilience-20261015/staging/`。
- 失败记录保留，不删除、不改写。

## 11. 清理

正式发布并完成生产部署验证后，或用户另有要求时：

- 仅对 `xy2api-staging` 项目执行 `docker compose down -v`；
- 删除 `xy2api-staging.caddy` 片段的 import，并平滑 reload；
- 经授权删除测试 DNS 记录；
- 请用户吊销测试 Key；
- 已有服务的状态与配置保持不变（清理前后对比容器 ID、启动时间与 Caddy 站点）。
