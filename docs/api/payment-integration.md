# 管理端支付集成 API

本文说明外部支付系统（例如 Sub2APIPay 或自建收银台）如何通过 XY2API 的 Admin API 在支付成功后给用户充值、查询用户和人工修正余额，以及嵌入页面能拿到哪些参数。如果你使用 XY2API [内置支付](../guides/payment.md)，不需要这些接口。读者是对接外部支付系统的开发者。

## 认证

服务间调用建议使用全局 admin API Key（在“系统设置”中生成，格式为 `admin-` 加 64 位十六进制字符）：

```text
x-api-key: admin-<64hex>
Content-Type: application/json
Idempotency-Key: <幂等键>
```

管理员登录 JWT（`Authorization: Bearer <jwt>`）也能访问这些接口，但不适合服务间调用。全局 admin API Key 按超级管理员处理，请妥善保管，只在服务端使用。普通管理员调用“生成并兑换”与余额调整需要“加减余额”权限（见[管理员与权限](../guides/admin-permissions.md)）。

成功响应的格式为：

```json
{ "code": 0, "message": "success", "data": { } }
```

失败响应的 `code` 为 HTTP 状态码，`reason` 为机器可读的原因码：

```json
{ "code": 409, "message": "redeem code already used by another user", "reason": "REDEEM_CODE_CONFLICT" }
```

## 幂等

写接口支持 `Idempotency-Key` 请求头（不超过 128 个可见 ASCII 字符）。相同的键和相同的请求体在有效期内重放时返回第一次的结果，并带响应头 `X-Idempotency-Replayed: true`；相同的键配不同的请求体返回 `409 IDEMPOTENCY_KEY_CONFLICT`；首次请求仍在处理中返回 `409 IDEMPOTENCY_IN_PROGRESS`。幂等记录默认保留 24 小时（`idempotency.default_ttl_seconds`）。

是否必须携带该请求头由配置 `idempotency.observe_only` 决定：

| `idempotency.observe_only` | 不带 `Idempotency-Key` 时 |
|---|---|
| `true`（默认） | 放行，按普通请求执行（不做幂等保护） |
| `false` | 返回 `400 IDEMPOTENCY_KEY_REQUIRED` |

建议对接方始终携带 `Idempotency-Key`。配置说明见 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)。

## 创建并兑换

`POST /api/v1/admin/redeem-codes/create-and-redeem`

在一个操作里创建兑换码并兑换给指定用户，适合支付成功后的充值。

```bash
curl -X POST "${BASE}/api/v1/admin/redeem-codes/create-and-redeem" \
  -H "x-api-key: ${KEY}" \
  -H "Idempotency-Key: pay-cm1234567890-success" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "s2p_cm1234567890",
    "type": "balance",
    "value": 100.00,
    "user_id": 123,
    "notes": "external payment order: cm1234567890"
  }'
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `code` | 是 | 兑换码，3–128 个字符。建议由订单号生成，同一订单始终使用同一个码 |
| `type` | 否 | `balance`（默认）、`concurrency`、`subscription`、`invitation` |
| `value` | 是 | 数值：余额金额、并发数等 |
| `user_id` | 是 | 目标用户 ID |
| `group_id` | `subscription` 时必填 | 订阅分组 |
| `validity_days` | `subscription` 时必填 | 订阅天数，正数增加、负数扣减，不能为 0 |
| `notes` | 否 | 备注 |
| `expires_at` / `expires_in_days` | 否 | 兑换码过期时间，二者只能填一个；`expires_in_days` 为 1–3650 |

响应：

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "redeem_code": {
      "id": 456,
      "code": "s2p_cm1234567890",
      "type": "balance",
      "value": 100,
      "status": "used",
      "used_by": 123,
      "used_at": "2026-11-10T08:00:00Z",
      "created_at": "2026-11-10T08:00:00Z",
      "group_id": null,
      "validity_days": 0,
      "notes": "external payment order: cm1234567890"
    }
  }
}
```

按兑换码判断的幂等语义（与 `Idempotency-Key` 独立）：

| 情况 | 结果 |
|---|---|
| 兑换码不存在 | 创建并兑换给 `user_id` |
| 兑换码已存在、未使用 | 兑换给 `user_id`（上次创建后未完成兑换时的补偿） |
| 兑换码已被同一 `user_id` 使用 | `200`，返回该兑换码，不重复充值 |
| 兑换码已被其他用户使用 | `409 REDEEM_CODE_CONFLICT` |
| 兑换码已过期 | 返回错误，不充值 |

## 查询用户

`GET /api/v1/admin/users/:id`：充值前可选的校验，确认用户存在且状态正常。

```bash
curl -s "${BASE}/api/v1/admin/users/123" -H "x-api-key: ${KEY}"
```

## 余额调整

`POST /api/v1/admin/users/:id/balance`：人工补偿或扣减。

```bash
curl -X POST "${BASE}/api/v1/admin/users/123/balance" \
  -H "x-api-key: ${KEY}" \
  -H "Idempotency-Key: balance-subtract-cm1234567890" \
  -H "Content-Type: application/json" \
  -d '{
    "balance": 100.00,
    "operation": "subtract",
    "notes": "manual correction"
  }'
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `balance` | 是 | 金额，必须大于 0 |
| `operation` | 是 | `set`（设为该值）、`add`（增加）、`subtract`（扣减） |
| `notes` | 否 | 备注 |

成功时 `data` 为更新后的用户对象。

## 嵌入页面参数

管理员可以在后台配置自定义菜单页面，用 iframe 嵌入外部页面（例如外部收银台）。XY2API 打开这类页面时会在 URL 上追加以下查询参数：

| 参数 | 说明 |
|---|---|
| `user_id` | 当前用户 ID |
| `token` | 当前用户的登录令牌 |
| `theme` | `light` 或 `dark` |
| `lang` | 当前界面语言，例如 `zh`、`en` |
| `ui_mode` | 固定为 `embedded` |
| `src_host` | 嵌入方的站点 origin |
| `src_url` | 嵌入方当前页面的完整 URL |

```text
https://pay.example.com/pay?user_id=123&token=<jwt>&theme=light&lang=zh&ui_mode=embedded&src_host=https%3A%2F%2Fapi.example.com&src_url=...
```

`token` 是用户的登录凭证，嵌入页面只应在服务端用它向 XY2API 校验用户身份，不要写入日志或转发给第三方。

## 失败处理建议

- 把“支付成功”和“充值成功”作为两个状态分别记录；回调验签成功后立即标记支付成功。
- 支付成功但充值失败的订单允许之后重试。重试时保持相同的 `code`，并使用新的 `Idempotency-Key`；即使上一次实际已经兑换成功，相同的 `code` 也保证不会重复充值。
- 网络超时、无法确定结果时，用同一个 `Idempotency-Key` 重放同一请求，可以拿到第一次的结果。
