# GPT-6.1 Sol 单功能适配

## 固定来源与范围

本功能基于 XY2API PR #74 合并提交 97d0f0626b9fb97fd4a9adfd10ab1c93c1802774，适配 Sub2API 的 9688571a83775b87db85917398c628b7cdfe8276（feat(models): support GPT-6.1 Sol）30 文件增量。该提交包含于正式 v0.2.11；固定 annotated tag object 为 881a722349105e21aaa38e11de90dd05046ebe4a，tag commit 为 96f4c115c9749078f90cbf210a01d39baf3f53b6。

这是 XY_OWNED 单功能适配，不是完成了 Sub2API v0.2.11 全量同步。产品版本保持 0.2.1，完整审计兼容基线保持 0.2.8；UPSTREAM_BASE、policy、313 条 SQL 及历史 checksum 不变。来源及适配文件清单见 GPT61_SOL_PROVENANCE.json。

Codex 描述符完整来自 openai/codex 提交 b1e72963c3b71a9265a551e54beff078384efed9 的 codex-rs/models-manager/models.json，经固定 Sub2API 功能提交收录。描述符包含的提示词是产品数据，其未知顶层及嵌套字段保留。没有合入套餐识别、Astra Ultrafast、余额预占、远程目录 UI 或其余 v0.2.11 功能。

## 实际行为

- 目录、映射、计价及出站 ID 为 gpt-6.1-sol。大小写、下划线、provider 前缀和已知 effort/compact 别名精确归一；未知相似名称不映射到新模型。
- 普通请求使用 low/medium/high/xhigh/max；显式 none/minimal、disabled thinking 在最终模型映射后本地拒绝。HTTP 原生、兼容转换、透传以及 WebSocket 首帧、后续回合和 session.update 均覆盖；映射到其他模型不误套新模型限制。
- 新模型 Chat-only 账号不支持工具调用时返回明确请求错误；不静默丢弃工具、不触发账号故障冷却或额外重试。总预算、取消、owner 和输出后禁止重放规则继续沿用。
- 精准补齐 apicompat 对新模型的 reasoning 识别，以及新模型专用 Anthropic effort 处理；不把上游前置提交的全模型 disabled-thinking 行为一并带入。
- Codex 默认 low，离线描述符保留 ultra 客户端自动委派元数据；不将该项另行解释为普通 wire effort。API Key 目录对真实 ID 与映射别名禁用 Responses Lite，保留上游显式 false/null、空集合及未知字段。
- IQ 默认检测模型仍保持原值。仅缺上游能力元数据时为新模型展示清楚标注的参考选项；已保存值和真实上游能力优先。未改变 IQ 判分、会话、账号开关或分组配置。

## 固定配置数值

下列数值直接来自固定功能提交，表示随源码发布的缺省配置；动态价格和渠道自定义仍按现有机制优先。

| 每百万 token（USD） | Standard | Priority | Flex/Batch 快照 |
| --- | ---: | ---: | ---: |
| 输入 | 2 | 4 | 1 |
| 输出 | 10 | 20 | 5 |
| 缓存写入 | 2.5 | 5 | 1.25 |
| 缓存读取 | 0.1 | 0.2 | 0.05 |

长上下文阈值 272000，超过阈值的输入倍率 2、输出倍率 1.5；保留分组是否启用阶梯计费、渠道覆盖和显式零价。Batch 数值来自固定价目快照，不表示新增批处理计费入口。

API/OpenCode context 为 1050000、最大输入 922000、输出 128000；官方 Codex 描述符 context 为 272000、max_context 为 872000。两个入口的口径保持各自来源。

## 验证与交付边界

新增 CI 复用原 service race 包构建，要求 4 个真实 PostgreSQL/Redis 调度测试和 5 个原生 HTTP/WebSocket 测试实际 run/PASS；原有事故回归及零跳过守卫保留。最终 CI 状态必须绑定真实提交，不能用旧 HEAD 的通过替代。

本地定向模型、计价、别名、转换、目录测试与前端全量/构建、真实存储及同输入源码三态证据统一位于外部 model-support-evidence。每个命令的 stdout/stderr、退出码及失败修正均保留；源码冻结后的结果只追加外部账本，避免为回填测试数字改变构建来源。

新模型运行验证使用全新隔离环境和实际合成 HTTP 请求。此前事故阶段 complete-05 的八阶段升级/完整备份恢复证据继续保留，其对应此前镜像；本轮不冒称重新执行八阶段，也不把健康接口成功当作模型业务请求通过。未执行生产部署或真实供应商调用。
