# TypeSafe / Jev

本文介绍如何把 TypeSafe API Key 账号接入 XY2API，并通过 Jev 原生的 System One 协议调用 `jev-latest` 模型。System One 是一个非流式、按“问题”给出判断结果的评估接口，与常见的对话接口不兼容。需要用 Jev 做文本评估的管理员和调用方需要阅读。

## 概览

| 项目 | 值 |
|---|---|
| 平台标识 | `typesafe`（界面名称 TypeSafe / Jev） |
| 账号类型 | API Key |
| 默认上游 | `https://api.typesafe.ai` |
| 对外端点 | `POST /v1/systemone` |
| 模型 | `jev-latest`；TypeSafe 分组的 `/v1/models` 也只返回该模型 |
| 问题类型 | `noul`、`choice`、`score` |

## 在管理后台添加

1. 「账号管理 → 添加账号」，平台选 **TypeSafe / Jev**，填写 TypeSafe API Key。Base URL 默认填入 `https://api.typesafe.ai`，一般无需修改。
2. 创建平台为 TypeSafe 的分组并加入账号。
3. 创建绑定该分组的 API 密钥。「使用密钥」弹窗的 **System One** 选项卡提供 curl 与 PowerShell 示例。

组合分组也可以把 `/v1/systemone` 请求路由到 TypeSafe 账号。

## 请求格式

请求和成功响应都保持 System One 原生 JSON 结构，网关原样转发请求体。该端点不兼容 Chat Completions、Responses、Anthropic Messages，也不支持流式请求，Claude Code、Codex 等客户端不能使用。

```bash
curl https://your-xy2api.example.com/v1/systemone \
  -H 'Authorization: Bearer sk-your-xy2api-key' \
  -H 'Content-Type: application/json' \
  --data '{"model":"jev-latest","state":"待评估文本","questions":{"safety":{"type":"noul","instructions":"评估文本是否不安全"}}}'
```

网关在转发前按 TypeSafe OpenAPI 的线上协议 schema 校验请求（TypeSafe SDK v0.5.7 也使用该 schema），不合法时直接返回 `400`：

- 顶层只允许 `model`、`state`、`questions`、`stream` 四个字段；`model` 必须是 `jev-latest`；`questions` 必须是非空对象。
- `stream` 可以省略、为 `null` 或 `false`；为 `true` 时拒绝。
- 每个问题只允许 `type`、`instructions`、`criteria` 三个字段；所有类型的 `instructions` 都可以省略或为 `null`。
- `noul`：`criteria` 可以省略或为 `null`；提供时须为对象，其中 `true`、`false` 的描述支持字符串、对象、数组或 `null`。
- `choice`：`criteria` 必须是对象，每个选项的描述支持字符串、对象、数组或 `null`。
- `score`：`criteria` 必须是至少包含一档描述的数组，每档支持字符串、对象或数组；只有一档也合法。SDK 中以整数为键的 Score 映射会由 SDK 在发送前转换为数组。

请求体使用文本请求的大小上限 `gateway.text_max_body_size`（默认 32 MiB）。

## 计费

`jev-latest` 的内置价格为输入 $0.042 / 百万 tokens、输出 $0。渠道定价可以覆盖这两个值。

## 错误处理

| 上游状态 | 处理 |
|---|---|
| `401`、`402`、`403`、`429`、`529`、`5xx`、网络或传输错误 | 沿用账号错误策略（包括自定义错误码与临时不可调度规则），并切换到其他账号重试 |
| `400`、`413`、`422` | 视为调用方请求本身的问题，直接返回，不换号重试，也不改变账号状态 |

## 限制

- TypeSafe 分组（以及路由到 TypeSafe 的组合分组请求）调用 `/v1/messages`、`/v1/chat/completions`、`/v1/responses`、`/v1/messages/count_tokens` 时返回 `404`。
- 非 TypeSafe 分组、且不能路由到 TypeSafe 的组合分组调用 `/v1/systemone` 时返回 `404 not_found_error`。
