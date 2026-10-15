# 附录：XY-image 0.0.17 配套改动

> 仓库 https://github.com/liulixin-lex/XY-image（公开，TypeScript）。
> 生产版本 0.0.16（`258c0c9`），文件位置以 v0.0.15（`a900026`）为基准；0.0.16 没有改动下列与 xy2api 集成相关的核心文件（逐文件比对）。
> 范围按 D11/Q13 B：xy2api 改动所需的配套，加上 XY-image 自身的高危问题。完整调研见仓库外的 `xy-image-integration.md`。

## 1. 对 xy2api 的兼容约束（已写入 design）

1. 确定性检查（鉴权、余额、权限、定价限制、仅限 Claude Code、模型映射、内容审计）都在提交 SSE 头之前完成，以正常 HTTP 状态返回；首次提交推迟到首个心跳（D3，15 秒）。
2. XY-image 经内网直连，不带 CDN 标记，因此不会被提前提交（D12）。
3. OpenAI 格式端点只用注释行心跳，不发数据事件。
4. 流内错误 JSON 只占一行，带稳定的 `code` 与 `error.status`；内容策略仍为 `type: content_policy_violation`。
5. 首输出超时与停滞判定对图片端点和异步任务豁免。
6. 新 code 与文案不得落入 XY-image 的“Key 不可用”集合（`INVALID_API_KEY`、`API_KEY_DISABLED`、`API_KEY_EXPIRED`、`API_KEY_REQUIRED`、`GROUP_DELETED`、`GROUP_DISABLED`、`GROUP_NOT_ALLOWED`、`SUBSCRIPTION_NOT_FOUND`、`SUBSCRIPTION_INVALID`），也不得匹配 “group is not allowed / 专属分组不再允许”文案。
7. 用户余额不足保留 403 加 `INSUFFICIENT_BALANCE`。
8. 快速失败返回与原 503 完全相同的应答；异步轮询、capabilities、DELETE、`/v1/usage`、`/v1/models`、幂等重放都豁免；异步准入继续返回 `429 too_many_tasks` 加 `Retry-After`。
9. 未送达请求不写用量行（D9）。XY-image 的核对器“24 小时查不到即判为未扣费”会自然收敛。
10. 不为 XY-image 流量开启 `image_nonstream_keepalive_interval`。
11. 所有应答都带 `X-Client-Request-ID`；异步 `request_id` 保持 `client:<id>` 格式；Caddy 的 API 错误页改为带 `X-XY-Proxy-Error` 标记的 JSON（runbook §3.2）。

## 2. 0.0.17 改动清单

| 编号 | 位置（v0.0.15） | 改动 | 解决的问题 |
|---|---|---|---|
| X1 | `apps/server/src/agent/tool-call-stream.ts`（`streamEventError`，约 136–157） | 按 `error.status`、`http_status`、`type`、`code` 还原状态语义，例如 `invalid_request_error`/`context_length_exceeded` → 400、`authentication_error` → 401、权限与余额 → 403、`not_found_error` → 404、`rate_limit_error`/`concurrent_balance_reserved` → 429；附带 `gatewayDiagnostics`，使 `model-options.ts` 的降级生效；无法归类时才落到 502/503 | 流内错误一律当作“主站繁忙”（高） |
| X2 | `apps/server/src/agent/deep-agent.ts`（约 391–416）、`agent/context-window.ts` | 上下文超长的判定覆盖流内错误，触发压缩 | 上下文超长不压缩 |
| X3 | `features/jobs/dispatch-gate.ts`、`features/jobs/auto-retry.ts`、`worker-message.ts`、`features/xy2api/image-task.ts` | 读取 capabilities 中的 `max_in_flight_per_key`，按 `xy2api_key_id` 在本地节流，超出时留在队列中，不发请求；从未发出的等待不受 20 分钟上限约束，文案改为“主站同时处理上限”；自动重试延迟取 `max(基础延迟, Retry-After)` 并设上限 | 批量异步任务循环重复提交、反复触发内容审计、20 分钟后误导性死信（高） |
| X4 | `generation/providers/xy2api-openai-image.ts`、`xy2api-gemini-image.ts`、`xy2api-xai-image.ts` | 为生图单独建 undici Agent，`headersTimeout` 与 `bodyTimeout` 对齐 600 秒 | 同步生图实际在 300 秒被截断，用户付费却拿不到图（高） |
| X5 | `features/xy2api/errors.ts`、`image-runner.ts`（`markKeyInvalid`） | `concurrent_balance_reserved` 映射为可重试的 `rate_limited`；携带 `Retry-After`；`markKeyInvalid` 只对真正的 Key 或分组失效生效；带 `X-XY-Proxy-Error` 的 5xx 归为 `xy2api_unavailable`（未发出、不扣费、可重试），取代已失效的 Cloudflare 52x 判断 | Caddy 纯文本 5xx 使任务待核对 24 小时（中）；误把 Key 标为失效的风险 |
| X6 | `apps/server/src/agent/model-retry.ts` | 读取 `Retry-After`；加耗时预算：单次失败超过 60 秒或累计超过 120 秒不再重试 | 对话重试叠加在网关换号之上，等待成倍拉长（中） |
| X7 | `generation/providers/xy2api-reference.ts`、`image-runner.ts` | 结果下载失败时保留任务句柄，稍后再查再下载（结果 24 小时内有效），不直接进入待核对 | 异步结果只下载 3 次，图片丢失（中） |
| X8 | `agent/tool-call-stream.test.ts`、`deploy/xy2api-lab/mock-upstream.mjs`、`features/xy2api/compat.ts` | 新增用例：`:` 心跳行、心跳后的错误事件、流内 400 上下文超长、首包慢与中途停滞（mock `delay=`）；`XY2API_VERIFIED_VERSIONS` 加入 0.4.0；更新兼容 fixture | 心跳与流内错误没有测试 |

不在本次范围：

- 核对器接入“按请求 ID 查账本”（需要 xy2api 新接口，P2）；
- 识别 2xx 正文中的错误（只在开启非流式图片保活时需要，本次不开）；
- 提示词优化超时由 45 秒改为 90–120 秒、文档与文案修正、遗留同步接口保护（低优先级，可另行处理）。

## 3. 发布顺序与兼容

- 0.0.17 必须同时兼容 xy2api 0.3.0（生产现状）与 0.4.0。X1–X7 都对旧版应答格式降级处理。
- 生产部署顺序：先 XY-image 0.0.17，观察正常后再部署 xy2api 0.4.0（runbook §4）。
- 验证：XY-image 自身的 CI；`deploy/xy2api-lab` 全量；测试服务器上分别与 xy2api 0.4.0 候选（staging-validation §6.4）和 0.3.0 联调。
