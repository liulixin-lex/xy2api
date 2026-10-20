# OpenAI 分组 Fast 与全局策略

## 设置与优先级

OpenAI 或组合分组可开启“强制使用 Fast”（`force_openai_fast`）。只有请求实际选择 OpenAI 上游时生效，组合分组中的其他平台不受影响。

优先级为：**分组强制 Fast > 用户专属 Fast/Flex 规则 > 全局 Fast/Flex 规则**。

- 分组强制 Fast 开启：最终请求固定为 `service_tier=priority`。客户端未指定档位，或指定 default、flex、ultrafast 等档位，均使用 priority；全局或用户专属的 pass、filter、block、force_priority 不再覆盖它。
- 分组强制 Fast 关闭：完整沿用现有规则。用户专属规则先匹配，同层按配置顺序首条命中；`filter` 删除字段，`block` 拒绝请求，`pass` 保留字段，`force_priority` 改为 priority。
- 此优先级仅涉及 Fast/Flex 策略，不绕过认证、分组状态、模型白名单或其他请求校验。
- 分组信息必须完整且有效；缺失或未加载完整的分组不启用例外。

HTTP 原生 Responses、Chat/Messages 转换、透传及 WebSocket 使用同一策略入口。WebSocket 只处理 `response.create`，取消及会话控制帧不改写。

## 仅开启一个分组

1. 全局 OpenAI Fast/Flex 策略保留“全部 tier 值 / 过滤 / 全部账号”。这已经覆盖 Fast 与 Flex，无需再加重复的 Flex 过滤规则。
2. 仅在目标分组开启“强制使用 Fast”，其他分组关闭。
3. 要按 Fast 单价收费时关闭“免费 Fast”（`free_openai_fast=false`）。分组倍率按原业务设置，例如 `0.2`。
4. 保存后新请求生效；已经建立的 WebSocket 会话重新连接后使用新分组配置。

“过滤”只删除转发参数，不控制第三方上游自身的强制档位；不含 `service_tier` 也不能证明上游实际使用普通档。

## 计费

分组倍率与服务档位单价分开计算：

```text
total_cost = 各类 Token 用量 × 对应 Fast 单价之和
actual_cost = total_cost × 分组倍率
```

例如某次普通档费用为 1 USD、模型 Fast 单价为普通档的 2 倍，倍率为 0.2 时，Fast 的 `total_cost=2`、`rate_multiplier=0.2`、`actual_cost=0.4`。不能把分组倍率改成 0.4 替代档位计费。Fast 价按模型目录或分组/渠道明确配置选取，不对已有 Fast 费用再乘一次档位系数。

强制 Fast 分组以最终发往 OpenAI 的 priority 作为结算档位：兼容上游即使回报 default，也保留 priority 计费。上游原始档位仍保留在转发结果 `UpstreamResponseServiceTier`，不改写上游响应。管理员仅应为已确认支持或强制提供 Fast 的上游开启该分组开关。

未开启分组强制 Fast 时保留原有结算规则：API Key 上游可声明降档，不能通过响应主动提高请求的计费档位；OAuth/SetupToken 的 default 回报沿用现有兼容逻辑。即使分组开关开启，最终请求没有 priority，也不会凭空补记 Fast 费用。

“免费 Fast”继续保留 priority 档位及对应 `total_cost`，但客户 `actual_cost` 按同一请求普通档费用乘分组倍率计算。长上下文规则仍按现有阈值与输入/输出系数叠加，与 Fast 档位独立。

## 升级与回退

本改动复用既有字段，无数据库迁移、无新增设置、无默认启用。升级前应检查已经开启 `force_openai_fast` 的分组：升级后它们会优先于全局过滤或阻止，并按 Fast 档位结算。若此前曾通过提高分组倍率代替 Fast 收费，应同时恢复业务原倍率，避免重复提高费用。

只回退应用代码时无需数据库回退；分组开关、倍率等管理员配置需单独恢复，历史账单不自动改写。验收应同时检查最终转发参数、`service_tier`、`total_cost`、`rate_multiplier` 和 `actual_cost`，而不只比较实扣金额。
