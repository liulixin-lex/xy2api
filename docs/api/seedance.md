# Seedance 视频 API

XY2API 原样支持火山方舟（Ark）的 Seedance 异步视频任务协议：客户端按 Ark 的格式提交 `content[]`，网关只改写模型名并转发给配置了 Seedance 能力的账号，不需要转换成 OpenAI `messages` 或 Grok `prompt`。本文面向配置 Seedance 账号的管理员和调用视频接口的开发者。

## 配置

1. 创建 **OpenAI 平台**的 **API Key** 账号，填写 Ark API Key，Base URL 填 `https://ark.cn-beijing.volces.com/api/v3`。兼容服务可以填自己的 `/api/v3` 或 `/v3` 地址；只填站点根地址时，网关会自动补上 `/api/v3`。
2. 在账号的端点能力中勾选 **Seedance (Ark)**。该能力默认不勾选，避免普通 OpenAI 请求被调度到这些账号，也避免 Seedance 请求落到其他 OpenAI 账号。新建、编辑和批量编辑账号都可以设置。
3. 把账号加入 OpenAI 分组，并开启分组的“允许图片生成”媒体权限。组合分组也可以路由到这些账号。
4. 配置模型映射，例如把公开模型名 `seedance-video` 映射到实际的 `doubao-seedance-*` 模型或 `ep-*` 推理接入点。为对应模型配置**输出 Token 价格**；本接口不使用 Grok 的按秒视频价格。

## 接口

```text
POST   /api/v3/contents/generations/tasks             创建任务
GET    /api/v3/contents/generations/tasks/{task_id}   查询任务
DELETE /api/v3/contents/generations/tasks/{task_id}   删除任务
```

同样的路径也可以用 `/v3`、`/v1` 前缀或不带版本前缀访问。使用 Ark SDK 时，把 Base URL 改为 `https://<你的网关>/api/v3`，API Key 填 XY2API 的 API Key。

请求需要 `Content-Type: application/json`，API Key 所属分组必须是 OpenAI 或组合分组。文本、图片、视频、音频内容、角色及扩展参数都原样传递，只按账号配置改写 `model`；响应保持上游原生格式。

### 创建任务

```bash
curl https://api.example.com/api/v3/contents/generations/tasks \
  -H "Authorization: Bearer $XY2API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "seedance-video",
    "content": [{"type": "text", "text": "海浪轻轻拍打沙滩"}],
    "duration": 5,
    "resolution": "720p",
    "ratio": "16:9",
    "generate_audio": true
  }'
```

`model` 必须是非空字符串，`content` 必须是非空数组。上游返回的任务 ID 原样返回：

```json
{ "id": "cgt-20261110123456-abcde" }
```

### 查询任务

用创建响应中的 `id` 查询，直到状态为 `succeeded`、`failed`、`cancelled` 等终态：

```bash
curl https://api.example.com/api/v3/contents/generations/tasks/cgt-20261110123456-abcde \
  -H "Authorization: Bearer $XY2API_KEY"
```

响应是上游的原生任务对象，字段以火山方舟文档为准。网关会读取其中的 `model`、`status` 和 `usage.completion_tokens`：

```json
{
  "id": "cgt-20261110123456-abcde",
  "model": "doubao-seedance-...",
  "status": "succeeded",
  "content": { "video_url": "https://..." },
  "usage": { "completion_tokens": 108900, "total_tokens": 108900 }
}
```

### 删除任务

```bash
curl -X DELETE https://api.example.com/api/v3/contents/generations/tasks/cgt-20261110123456-abcde \
  -H "Authorization: Bearer $XY2API_KEY"
```

删除遵循上游语义，不会自动退款。

## 任务归属与计费

- 查询和删除只能访问同一用户、同一 API Key、同一分组创建的任务，并且始终发往创建任务的那个上游账号，不会转到其他账号查询。
- 不开放上游的任务列表接口，防止共享账号上的任务暴露给其他用户。
- 创建时不计费。**首次查询到 `succeeded`** 时，按上游返回的 `usage.completion_tokens` 和创建时的模型快照计费；重复查询有去重保护，不会重复扣费。失败、排队和运行中的任务不计费。
- 任务与账号的绑定以及创建时的模型快照存放在 Redis 中，保留 24 小时。请保持 Redis 数据持久，并在 24 小时内查询到完成结果。
- 网关不会在后台轮询任务。只依赖上游回调、从不通过网关查询的任务**不会**自动结算。
- 创建请求的上游错误不会自动重试，以免重复创建付费任务。上游错误码和响应体原样返回。

## 错误

| 情况 | HTTP | 说明 |
|---|---|---|
| `Content-Type` 不是 `application/json` | 415 | `Seedance requires application/json` |
| API Key 所属分组不是 OpenAI 或组合分组 | 403 | `Seedance requires an OpenAI or composite group` |
| 分组未开启图片生成 | 403 | `permission_error` |
| 分组中没有勾选 Seedance 能力的可用账号 | 503 | `seedance_no_eligible_account` |
| 上游返回错误 | 上游状态码 | 原样返回 |

## 参考

- [火山方舟 Go SDK 内容生成模型定义](https://github.com/volcengine/volcengine-go-sdk/blob/master/service/arkruntime/model/content_generation.go)
- [创建视频生成任务](https://www.volcengine.com/docs/82379/1520757)
- [查询视频生成任务](https://www.volcengine.com/docs/82379/1521309)
