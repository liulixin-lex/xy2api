## Why

经 NewAPI 等网关转发的 Claude 请求，可能没有携带缓存声明。当前 XY2API 的 Anthropic API Key 路径保留合法声明，但不自动补齐；因此稳定前缀也可能持续按普通输入处理。

2026-10-07 的合成实验已在同一生产上游验证：没有声明时重复请求不读写缓存；仅添加顶层 `cache_control` 后，全新前缀首次写入 11,409 个 5m token，第二次读取同样的 11,409 token。15 条样本的返回用量、入库记录和费用计算一致。这支持提供可选的补齐能力，不代表已确认历史低命中用户的根因；该用户的原始请求体不可用。

## What Changes

- 新增默认关闭的「Claude 缓存补齐」策略，管理员按分组、可选 Key 名单、实际账号、上游地址与最终模型启用。
- 首版仅支持原生 `/v1/messages` 转发至已验证的 Anthropic API Key 上游，同时覆盖流式和非流式响应。
- 原始请求及最终出站正文都没有协议位置上的 `cache_control` 时，只添加顶层 `{"type":"ephemeral"}`，使用默认 5m。已有声明不由本功能覆盖、移动或延长。
- 复用现有系统设置表存储策略；首版不增加业务表、计费表或 SQL 迁移。
- 增加不含对话正文和认证信息的补齐决策、上游缓存用量和费用核对记录。
- 在途预留考虑可能发生的 5m 冷写费用，最终扣款仍使用真实 usage，复用既有耐久结算。
- 首版不新增自动重放、自动付费探测、1h 强制升级、缓存读数伪造或会话路由改写。

## Capabilities

### New Capabilities

- `claude-cache-fallback`：有范围限制的缺失声明补齐、客户端声明保护、上游兼容性验证和可核对的计费行为。

### Modified Capabilities

不改变既有缓存声明、OAuth 缓存策略、计费单价与倍率规则、持久结算和调度契约。

## Impact

- 后端主要涉及请求最终构造、独立缓存策略 helper、管理员设置读取/更新及诊断事件；具体接入点见 [design.md](design.md)。
- 管理端增加开关和范围规则，明确提示首次写入可能增加费用；普通请求协议不新增 XY2API 私有缓存字段。
- 当前上游通过测试不等于所有 Anthropic 兼容服务都支持。未验证的账号、地址、模型组合默认不补齐。
- 自动缓存只提供复用机会；前缀变化、短请求、TTL 过期、并发冷启动和上游内部路由仍可能导致零命中。

## Baseline and Status

- 仓库：`liulixin-lex/xy2api`；2026-10-08 `git fetch origin` 及 `git ls-remote` 核实的 `origin/main` 为 `317b019134a09f1a5cb16f8c15b0a3365f9ee920`。
- 产品 0.2.3，Sub2API 兼容 0.2.14；与本次生产诊断核验的业务源码相同。
- 分支：`plan/claude-cache-fallback-20261008`；工作树：`/lex/xy2api-claude-cache`。
- 当前交付为方案，功能尚未实现或部署。原 `/lex/xy2api` 及其未提交文档保持。
- 规范：[spec.md](specs/claude-cache-fallback/spec.md)；实施顺序：[tasks.md](tasks.md)。

## Sources

- [Anthropic 官方缓存协议与计价](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)：顶层自动缓存、显式断点和 usage 分类。
- [LiteLLM 自动注入教程](https://docs.litellm.ai/docs/tutorials/prompt_caching)与[实现 PR 33573](https://github.com/BerriAI/litellm/pull/33573)：默认关闭、限定提供商/模型、保留客户端断点；其默认块级策略与本方案的顶层策略不同。
- [OpenRouter 缓存兼容性说明](https://openrouter.ai/docs/guides/best-practices/prompt-caching)：不同上游对顶层声明和块级断点的支持不同。
- 本机受限诊断证据：`/lex/cache-diagnosis-20261007/REPORT.md`、`probe-root5m.jsonl`、`billing-reconciliation.json`。不把生产日志、邮箱、API Key 或连接凭据复制进仓库。
