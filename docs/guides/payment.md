# 内置支付

XY2API 内置支付系统，用户可以自助充值余额或购买订阅套餐，无需另外部署支付服务。本文面向配置支付的超级管理员，说明支持的支付方式、后台设置、各服务商的凭证与回调、Stripe 站内与托管两种模式，以及订单状态与退款。支付配置与服务商只有超级管理员能修改。

## 支持的支付方式

| 服务商 | 用户可用的支付方式 | 说明 |
|---|---|---|
| **易支付（EasyPay）** | 支付宝、微信支付，以及自定义支付方式 | 兼容易支付协议的第三方聚合支付 |
| **支付宝官方** | 桌面扫码；移动端手机网站支付，或当面付唤起 App | 直接对接支付宝开放平台 |
| **微信官方** | Native 扫码、H5、公众号/JSAPI | 直接对接微信支付 APIv3，按终端环境自动分流 |
| **Stripe 站内** | 银行卡、支付宝、微信支付、Link 等 | 在本站页面内用 Stripe Payment Element 付款，支持多币种 |
| **Stripe 托管** | 由 Stripe Dashboard 中开启的付款方式决定 | 跳转到 Stripe 官方收银台（Checkout）付款，支持多币种 |
| **Airwallex** | 由 Airwallex 账户开通的付款方式决定 | 在本站页面内用 Airwallex 支付组件付款，支持多币种 |

- 前台对用户只显示“支付宝”“微信支付”两个按钮，不区分官方还是易支付。官方渠道直接对接支付平台 API，资金直达商户账户、手续费更低；易支付通过第三方平台聚合，接入门槛更低。后台中同一时刻只能有一个启用的实例提供“支付宝”（“微信支付”同理），启用第二个时会提示先停用现有实例。
- Stripe 站内与 Stripe 托管只能二选一（见 [Stripe 站内与托管](#stripe-站内与托管)），用户端统一显示 Stripe。
- 微信官方、支付宝官方和易支付按人民币（CNY）收款；Stripe 与 Airwallex 可以在服务商配置中选择 CNY、HKD、USD、EUR、GBP、AUD、CAD、SGD、JPY、KRW、NZD 等币种（以账户实际支持为准）。

**易支付服务商参考**：以下是两家兼容易支付协议的第三方聚合支付，费率与结算条件以服务商官网为准。

- 国内渠道、人民币结算：ZPay（`z-pay.cn`），支付宝/微信官方接口直连，T+1 自动到账，支持无营业执照的个人用户（有每日额度限制）。
- 国际渠道、USDT 或美元结算：启润支付 Kyren（`kyrenpay.com`），提供国际版微信支付与支付宝、本地货币支付和美元结算。

支付渠道的安全性、稳定性与合规性请自行鉴别，本项目不对任何第三方支付服务商做担保或背书。

## 快速开始

1. 进入管理后台“系统设置 → 支付设置”。
2. 打开“启用支付”，设置金额范围、订单超时等基本参数。
3. 在“启用的服务商”中选择要使用的服务商类型。
4. 在“服务商管理”中点击“添加服务商”，至少添加一个实例并填写凭证。
5. 按 [Webhook 配置](#webhook-配置) 在支付平台登记回调地址。
6. 用户即可在前台充值或购买套餐。

## 系统设置

### 基本设置

| 设置项 | 设置键 | 默认值 | 说明 |
|---|---|---|---|
| 启用支付 | `payment_enabled` | 关闭 | 总开关 |
| 启用的服务商 | `ENABLED_PAYMENT_TYPES` | 空 | 禁用某个服务商类型会同时禁用其实例 |
| 最低金额 | `MIN_RECHARGE_AMOUNT` | 1 | 单笔最低充值金额 |
| 最高金额 | `MAX_RECHARGE_AMOUNT` | 不限 | 留空表示不限制 |
| 每日限额 | `DAILY_RECHARGE_LIMIT` | 不限 | 每用户每日累计充值上限，留空表示不限制 |
| 订单超时时间 | `ORDER_TIMEOUT_MINUTES` | 30 | 单位分钟，至少 1 |
| 最大待支付订单数 | `MAX_PENDING_ORDERS` | 3 | 同一用户同时存在的待支付订单上限 |
| 负载均衡策略 | `LOAD_BALANCE_STRATEGY` | 轮询 | `round-robin`（轮询）或 `least-amount`（优先当日累计金额最少的实例） |
| 商品名前缀 / 后缀 | `PRODUCT_NAME_PREFIX` / `PRODUCT_NAME_SUFFIX` | 空 | 支付页面显示的商品名 |
| 帮助图片 / 帮助文本 | `PAYMENT_HELP_IMAGE_URL` / `PAYMENT_HELP_TEXT` | 空 | 充值页显示的客服二维码和说明 |
| 禁用余额充值 | `BALANCE_PAYMENT_DISABLED` | 关闭 | 打开后只能购买订阅套餐 |

### 金额换算与优惠

| 设置项 | 设置键 | 默认值 | 说明 |
|---|---|---|---|
| 余额充值倍率 | `BALANCE_RECHARGE_MULTIPLIER` | 1 | 用户每支付 1 CNY 可获得多少 USD 余额，必须大于 0 |
| 订阅 CNY 换算汇率 | `SUBSCRIPTION_USD_TO_CNY_RATE` | 0（不换算） | CNY 通道下套餐每 1 USD 价格收取多少 CNY。为 0 时套餐按价格数值直接收款；启用后所有套餐价格必须按 USD 定价 |
| 充值手续费率 | `RECHARGE_FEE_RATE` | 0 | 充值时额外收取的手续费百分比 |
| 充值优惠阶梯 | `RECHARGE_BONUS_TIERS`、`RECHARGE_BONUS_MODE` | 空 | 余额充值按用户输入金额命中“不超过该金额的最大档”。“赠金”模式实付不变、额外赠送百分比余额；“折扣”模式到账不变、实付按百分比打折（百分比必须小于 100）。订阅订单不参与 |
| 充值赠送活动文案 | `RECHARGE_BONUS_NOTICE` | 空 | 支持 Markdown，显示在充值页金额选择区顶部 |

### 支付宝选项

| 设置项 | 设置键 | 默认值 | 说明 |
|---|---|---|---|
| 支付宝强制二维码支付 | `ALIPAY_FORCE_QRCODE` | 关闭 | 移动端支付宝用户统一扫码，不跳转手机网站支付 |
| 支付宝移动端当面付唤起 | `ALIPAY_MOBILE_PRECREATE_DEEP_LINK` | 关闭 | 只对路由到支付宝官方的移动端订单生效：服务端调用 `alipay.trade.precreate`，前端尝试打开支付宝 App，未成功时显示动态二维码备用页并继续轮询订单 |

- 当面付唤起需要该支付宝应用已开通“当面付/扫码支付”。部署环境变量 `ALIPAY_MOBILE_PRECREATE_DEEP_LINK=true` 可强制开启，未设置时使用后台值。
- 两个选项同时开启时，当面付唤起优先。关闭当面付唤起即恢复移动端手机网站支付。

### 取消频率限制

防止用户反复创建并取消订单。开启后，用户在时间窗口内取消次数超限将无法创建新订单。

| 设置项 | 设置键 | 默认值 |
|---|---|---|
| 限制取消频率 | `CANCEL_RATE_LIMIT_ENABLED` | 关闭 |
| 窗口模式 | `CANCEL_RATE_LIMIT_WINDOW_MODE` | 滚动 / 固定 |
| 时间窗口 / 周期 | `CANCEL_RATE_LIMIT_WINDOW` / `CANCEL_RATE_LIMIT_UNIT` | 1；周期可选分钟、小时、天 |
| 最大取消次数 | `CANCEL_RATE_LIMIT_MAX` | 10 |

## 服务商配置

在“服务商管理 → 添加服务商”中选择类型后填写。异步通知地址和同步跳转地址由站点的回调基础地址自动拼接，管理员只需确认域名正确。

### 易支付

| 参数 | 必填 | 说明 |
|---|---|---|
| 商户 ID（PID） | 是 | |
| 商户密钥（PKey） | 是 | |
| API 基础地址 | 是 | 易支付服务商的接口地址 |
| 支付宝渠道 ID / 微信渠道 ID | 否 | 指定上游通道 |
| 易支付自定义支付方式 | 否 | 额外的支付方式：填写支付方式标识（小写字母、数字、下划线、短横线，不能使用或以 `alipay`、`wxpay` 开头）、上游 `type` 和显示名称。上游 `type` 作为易支付的 `type` 参数提交 |

支付模式可选“二维码”（默认）或“弹窗”。

### 支付宝官方

| 参数 | 必填 |
|---|---|
| App ID | 是 |
| 应用私钥（RSA2） | 是 |
| 支付宝公钥 | 是 |

- **桌面端**：优先调用 `alipay.trade.precreate`（当面付）直接渲染二维码；接口不可用时降级到 `alipay.trade.page.pay`（电脑网站支付），并把收银台链接渲染成二维码，同时保留打开收银台的入口。
- **移动端**：默认调用 `alipay.trade.wap.pay`（手机网站支付）；开启“支付宝移动端当面付唤起”后改用当面付。未开通手机网站支付时前端改走扫码支付。

需要在支付宝开放平台为应用开通对应的产品（当面付、电脑网站支付、手机网站支付）。

### 微信官方

| 参数 | 必填 | 说明 |
|---|---|---|
| App ID | 是 | |
| 商户号（MchID） | 是 | |
| 商户 API 私钥 | 是 | PEM 格式 |
| API v3 密钥 | 是 | 32 字节 |
| 证书序列号 | 是 | 商户证书序列号 |
| 微信支付公钥 / 公钥 ID | 是 | PEM 格式 |
| 公众号 App ID、H5 应用名称、H5 应用地址 | 否 | 仅在公众号支付或 H5 场景有特殊要求时填写 |

桌面端优先 Native 扫码；移动端在微信内走 JSAPI（需要微信网页授权获取 OpenID），在其他浏览器中走 H5。

### Stripe 站内

| 参数 | 必填 | 说明 |
|---|---|---|
| 密钥（Secret Key） | 是 | `sk_live_...` 或 `sk_test_...` |
| 公开密钥（Publishable Key） | 是 | `pk_live_...` 或 `pk_test_...` |
| Webhook 密钥 | 是 | `whsec_...` |
| 支付币种 | 否 | 默认 CNY |

### Stripe 托管

| 参数 | 必填 | 说明 |
|---|---|---|
| 密钥（Secret Key） | 是 | |
| Webhook 密钥 | 是 | 与密钥属于同一 Stripe 账户和同一测试/正式环境 |
| 支付币种 | 否 | 默认 CNY |

不需要 Publishable Key。其余要求见 [Stripe 站内与托管](#stripe-站内与托管)。

### Airwallex

| 参数 | 必填 | 说明 |
|---|---|---|
| Client ID | 是 | |
| API Key | 是 | 建议使用 Scoped API Key，账户级权限只为 Payment Acceptance 勾选读取和写入 |
| Webhook 密钥 | 是 | |
| API 基础地址 | 是 | 必须与 API Key 所属环境一致：沙箱为 `https://api-demo.airwallex.com/api/v1`，生产为 `https://api.airwallex.com/api/v1`（默认）。混用会返回 `credentials_invalid` / Access Denied |
| 国家/地区代码 | 否 | 两位 ISO 代码，默认 `CN` |
| 支付币种 | 否 | 默认 CNY |
| Airwallex 账户 ID | 否 | 不涉及多账户、组织级密钥或连接账户收款时可以不填 |

## 服务商实例管理

同一种服务商可以创建多个实例：

- **独立限额**：每个实例可设置单笔最低、单笔最高和每日限额。全部留空使用全局配置，部分填写时留空项表示不限制。分配订单时自动跳过超出限额的实例。
- **负载均衡**：同一支付方式有多个启用实例时，按负载均衡策略分配订单。
- **独立启停与排序**：可以单独启用或停用实例，拖拽调整顺序。有待支付订单的实例不能停用，也不能修改影响订单的关键配置（返回 `PENDING_ORDERS`）。
- **退款控制**：每个实例可以单独“允许退款”，并进一步“允许用户退款”（用户自助申请）。
- **支付方式**：每个实例可以只支持部分支付方式。

## Webhook 配置

支付成功由服务商回调确认，系统会校验回调签名并自动完成充值，无需人工干预。回调地址必须使用 HTTPS（Stripe 强制要求，其他服务商强烈建议），并确保防火墙允许支付平台访问。

| 服务商 | 回调地址 |
|---|---|
| 易支付 | `https://your-domain.com/api/v1/payment/webhook/easypay`（同时接受 GET 与 POST） |
| 支付宝官方 | `https://your-domain.com/api/v1/payment/webhook/alipay` |
| 微信官方 | `https://your-domain.com/api/v1/payment/webhook/wxpay` |
| Stripe 站内 | `https://your-domain.com/api/v1/payment/webhook/stripe` |
| Stripe 托管 | `https://your-domain.com/api/v1/payment/webhook/stripe_hosted/<实例ID>` |
| Airwallex | `https://your-domain.com/api/v1/payment/webhook/airwallex` |

把 `your-domain.com` 换成你的域名。易支付、支付宝、微信的回调地址在添加服务商时自动填入。

- **Stripe 站内**：在 Stripe Dashboard 的 Developers → Webhooks 中添加端点，订阅 `payment_intent.succeeded`、`payment_intent.payment_failed`，把生成的 `whsec_...` 填入服务商配置。
- **Stripe 托管**：订阅 `checkout.session.completed`、`checkout.session.async_payment_succeeded`、`checkout.session.async_payment_failed`、`checkout.session.expired`。
- **Airwallex**：在 Airwallex 后台添加 Webhook 端点，事件至少选择 Payment Intent → Succeeded（`payment_intent.succeeded`），建议同时选择 Payment Intent → Cancelled（`payment_intent.cancelled`）；API version 选择账户默认或最新稳定版本。

## Stripe 站内与托管

在“支付设置 → Stripe 收款模式”中单选“站内”“托管”或“关闭”。站内模式在本站完成付款；托管模式跳转到 Stripe 收银台。两种模式都支持余额充值和套餐单次购买，套餐按本站有效期发放，不创建 Stripe 循环订阅。同时启用两种模式会返回 `STRIPE_MODE_CONFLICT`。

切换模式只影响新订单。已有订单继续按原方式完成到账确认和退款；模式关闭或切换不会删除服务商配置，也不会停用历史订单的回调。

### 托管模式的配置要求

1. 配置并备份固定的 `TOTP_ENCRYPTION_KEY`。托管实例的密钥和 Webhook 密钥用它加密保存，未配置时拒绝保存（`PAYMENT_ENCRYPTION_REQUIRED`）。丢失这个密钥会影响历史订单的查单与退款。
2. 在系统设置中配置可信的“前端地址”（`frontend_url`），例如 `https://app.example.com`，不带路径、查询参数或片段。只有测试密钥允许 `http://localhost:端口` 或 `http://127.0.0.1:端口`。收银台的返回地址只由这个设置生成，不使用客户端的 Host、Referer 或 `return_url`。
3. 选择“Stripe 托管”模式并保存，添加该类型的服务商，填写密钥、Webhook 密钥、币种和限额。
4. 保存后重新打开服务商，复制本站生成的完整 Webhook 地址。末尾的编号是本部署的实例 ID，不要照抄其他部署的编号。在 Stripe Dashboard 添加该端点，API 版本选择 `.dahlia` 系列（后端 SDK 为 `2026-03-25.dahlia`，日期不必一致），不要选 preview 版本。
5. 付款方式、Logo、品牌颜色在 Stripe Dashboard 中管理。本站使用固定金额，请关闭 Adaptive Pricing 和自动税费，不启用优惠码、运费和可调整数量。

**配置锁定**：被任何订单引用过的托管实例不能删除（`PROVIDER_IN_USE`），也不能直接修改密钥和币种（`HOSTED_CONFIG_LOCKED`），即使订单已完成。更换 Stripe 账户或凭据时请新建实例，并停用旧实例；停用只阻止新订单，历史订单的验签、恢复和退款继续可用。

### 托管模式的订单流程

- 下单请求 `POST /api/v1/payment/orders` 使用 `payment_type=stripe_hosted`，并且必须带 `Idempotency-Key` 请求头（16–128 个字母、数字、下划线或短横线）。同一用户用相同的键和相同参数重试会得到原订单；相同的键配不同参数返回冲突。
- 响应包含 `pay_url`、`payment_mode=redirect` 和 `expires_at`，不返回 `client_secret`。浏览器在当前标签页打开 Stripe 官方收银台地址。
- 订单有效期取“订单超时时间”，并限制在 31 分钟到 24 小时之间，以 Stripe 返回的实际过期时间为准。
- 用户可以调用 `POST /api/v1/payment/orders/:id/stripe-hosted/resume` 继续支付：服务端先向 Stripe 查单，再返回可继续支付的地址。收银台返回页上的参数只用于页面导航，不能触发到账；点击收银台的返回链接也不会取消订单。
- 只有经过实例、账户、测试/正式环境、Session、币种和最小单位金额全部校验的付款才会入账。回调、用户查单和后台补偿共用同一个履约入口，后台每轮最多处理 20 个托管订单。
- 创建 Session 时如果结果不确定，订单会保留，23 小时内只重放同一个 Stripe 幂等请求，不会另建 Session。超过窗口仍无法确定的订单，请在 Stripe Dashboard 中按订单 metadata 核实，不要直接标记付款成功。

## 支付流程

```text
用户选择金额和支付方式
       │
       ▼
  创建订单（PENDING）
  ├─ 校验金额范围、待支付订单数、每日限额
  ├─ 按负载均衡选择服务商实例
  └─ 调用服务商获取支付信息
       │
       ▼
  用户完成支付（扫码 / 跳转 / 站内组件 / Stripe 收银台）
       │
       ▼
  回调验签 → 订单 PAID → 充值或发放套餐 → 订单 COMPLETED
```

### 订单状态

| 状态 | 说明 |
|---|---|
| `PENDING` | 待支付 |
| `PROCESSING` | 等待到账：已提交延迟付款（例如 Stripe 托管的异步付款方式），等待服务商确认，不按页面过期处理 |
| `PAID` | 已支付，等待充值到账 |
| `RECHARGING` | 充值中 |
| `COMPLETED` | 已完成，余额或套餐已发放 |
| `EXPIRED` | 超时未支付 |
| `CANCELLED` | 已取消 |
| `FAILED` | 付款失败，或充值失败（可由管理员重试） |
| `REFUND_REQUESTED` | 用户已申请退款 |
| `REFUNDING` | 退款中 |
| `REFUND_PENDING` | 退款结果待确认 |
| `PARTIALLY_REFUNDED` | 部分退款 |
| `REFUNDED` | 已退款 |
| `REFUND_FAILED` | 退款失败 |

### 超时与补单

后台任务每 60 秒检查一次超时订单，标记过期前先向服务商查询支付状态。如果用户实际已支付但回调延迟，系统会通过查询补单。

### 退款

- 管理员在“订单管理”中对订单退款、查询退款结果或重新履约。普通管理员需要“退款”开关（见[管理员与权限](admin-permissions.md)）。
- 开启了“允许用户退款”的实例，用户可以对余额订单提交退款申请。
- 退款会扣回对应的余额或套餐时长；余额不足或退款金额超过充值金额时拒绝退款。
- Stripe 托管订单的退款先在数据库中冻结退款请求并扣回余额或套餐时长，再请求 Stripe。网络结果不确定时订单进入 `REFUND_PENDING`，由管理员“查询退款”确认：确认成功不会重复扣回，确认失败只恢复一次。已确认失败的退款不要直接换参数再提交，先在 Stripe 中核查原退款记录。

## 订阅套餐

在“订单管理 → 订阅套餐”中管理可购买的套餐：绑定分组、价格与原价、币种、有效期与单位、功能说明、商品名、是否上架和排序。新增、修改、删除套餐需要“加减余额”权限。用户购买套餐生成 `subscription` 类型订单，支付完成后按有效期发放订阅。

## 从 Sub2APIPay 迁移

如果你之前用 Sub2APIPay 作为外部支付系统：

1. 在 XY2API 管理后台启用支付并配置服务商（可以使用相同的支付凭证）。
2. 把支付平台的回调地址改为 XY2API 的回调地址。
3. 确认新订单通过内置支付正常处理后，停用 Sub2APIPay。

Sub2APIPay 中的历史订单不会自动迁移，建议保留一段时间以便查询。仍需通过 Admin API 对接外部支付系统时，见[管理端支付集成 API](../api/payment-integration.md)。

## 升级与回滚

Stripe 托管模式依赖迁移 `250_stripe_hosted_idempotency.sql`（追加可空字段和唯一索引，不改写旧订单）。回滚到不支持托管模式的版本前，先停止新的托管下单，保留当前版本直到已有托管订单的回调、补偿和退款全部处理完毕。不要删除仍被订单引用的实例、密钥或退款记录。通用流程见[升级与回滚](../deployment/upgrade.md)。
