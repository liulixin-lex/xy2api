# stripe 托管

`stripe_hosted` 是独立的 Stripe Checkout 托管支付方式，支持余额充值和套餐单次购买。原 `stripe` 入口继续使用原站内流程，两者独立配置、独立启停。套餐仍按本站有效期发放，不创建 Stripe 循环订阅。

## 配置

1. 使用项目要求的 Go 1.27.0 构建后端，部署时执行新增迁移 `250_stripe_hosted_idempotency.sql`。保留迁移 checksum；不要改写历史 SQL。
2. 配置并备份持久化 `TOTP_ENCRYPTION_KEY`。托管实例的 Secret Key、Webhook Signing Secret 使用此密钥进行 AES-256-GCM 加密保存。丢失密钥将影响历史订单查单与退款。
3. 在管理设置中配置可信的「前端地址」，例如 `https://app.example.com`，不带路径、查询参数或片段。仅测试密钥允许 `http://localhost:端口` 或 `http://127.0.0.1:端口`。客户端 Host、Referer 和 return_url 不参与托管返回地址生成。
4. 启用支付方式「stripe 托管」，新建同名类型实例，配置 Stripe Secret Key、Webhook Signing Secret、币种和限额。无需 Publishable Key；托管方式没有站内弹窗或组件模式。
5. 保存后重新打开实例，复制 `/api/v1/payment/webhook/stripe_hosted/<实例ID>` 对应的完整外网 HTTPS 地址。在 Stripe Dashboard 设置该端点，API 版本选择 **2026-03-25.dahlia**，与 `stripe-go/v85 v85.0.0` 对齐。不要使用 preview。
6. 订阅 `checkout.session.completed`、`checkout.session.async_payment_succeeded`、`checkout.session.async_payment_failed`、`checkout.session.expired`。Stripe CLI 本地转发时使用 CLI 输出的签名密钥，不混用 Dashboard 端点密钥。
7. 付款方式、Logo、品牌颜色在 Stripe Dashboard 管理。首版使用固定金额，关闭 Adaptive Pricing 和自动税费，不启用优惠码、运费及可调整数量。

新旧方式不自动切换。托管配置验收后可关闭旧 `stripe`，需要兼容备用时再手动开启。禁用托管实例只阻止新订单，历史订单的验签、恢复和退款继续可用。被任何订单引用的实例不能删除或直接更换密钥、币种；更换账户或凭据时新建实例，并保留旧实例以处理历史订单。

## 接口与状态

`POST /api/v1/payment/orders` 使用 `payment_type=stripe_hosted` 和 `Idempotency-Key` 请求头，键为 16 至 128 个字母、数字、下划线或短横线。同一用户同键同参数恢复原订单；同键不同参数返回冲突。账本报价、手续费、数量、返回地址和 Session 过期时间在第一次请求前冻结。

响应使用 `pay_url`、`payment_mode=redirect`、`expires_at`；不返回 `client_secret`。浏览器在当前标签打开官方 `https://checkout.stripe.com/c/pay/...` URL。自定义 Checkout 域名不在首版允许范围。

`POST /api/v1/payment/orders/<id>/stripe-hosted/resume` 仅允许订单所属用户调用，并先向 Stripe 查单再返回可继续支付的 URL。接口禁止缓存。返回页上的 `success`、`checkout`、`session_id` 仅为导航信息，不能触发到账。

- `PENDING`：尚未提交付款，可以恢复原 Session。
- `PROCESSING`：已提交延迟付款，等待 Stripe 确认，不按页面过期处理。
- `PAID` / `RECHARGING`：Stripe 已确认，本站履约未完成；界面继续等待。
- `COMPLETED`：余额或套餐已发放。
- `FAILED` / `EXPIRED` / `CANCELLED`：付款失败、Session 已过期或经服务端确认关闭。已验证的延迟成功仍可幂等到账。

有效期采用配置值并约束在 31 分钟至 24 小时，最短值为 Stripe 30 分钟下限预留请求时间。以 Stripe 返回的实际过期时间为准。点击 Checkout 的返回链接不会取消订单。主动取消先查单、再关闭 Session，遇到支付竞争继续确认状态。

创建响应不确定时保留原订单。23 小时恢复窗口内只重放同一个 Stripe 幂等请求；超过窗口或原过期时间不再新建 Session。此类未能确定 Session ID 的订单需管理员在 Stripe Dashboard 按订单 metadata 核实，不能直接标记付款成功或改建新 Session。

后台每轮最多处理 20 个托管订单，并保留分页游标。回调、用户查单和后台补偿共用履约入口。只有经过实例、账户、测试/正式模式、Session、币种、整数最小单位金额校验的付款才能入账。

退款从原 Session 查到 PaymentIntent 后执行，沿用原权限和余额、套餐扣回额度。在数据库事务中保存冻结退款请求、扣回余额或套餐时长，再请求 Stripe。网络不确定时保留扣回并进入 `REFUND_PENDING`，由管理端查单确认；确认成功不重复扣回，确认失败在事务中仅恢复一次。无上游退款记录且仍在 23 小时窗口内时，重放原退款幂等请求；超过窗口保留待确认状态，需人工核实。已确认失败的退款不允许直接换参数再提交，需先核查原 Stripe 退款记录。查单使用按手续费与余额倍率换算后的网关金额。

## 验证与上线门禁

本地验收使用模拟 Stripe HTTP 服务、签名事件和独立 PostgreSQL，不会调用真实账户或产生真实扣款。记录在 `/xy/artifacts/stripe-hosted/VERIFICATION.txt`。

真实测试账户上线前仍须验收：官方托管页、付款失败、3DS、返回和取消、延迟付款、CLI/Dashboard Webhook、重复事件、全额及部分退款。实际支付方式可用性由账户地区、币种和 Dashboard 设置决定。本地模拟不能替代这些结果，也不构成绝对零漏洞保证。

官方依据：[Checkout](https://docs.stripe.com/payments/checkout)、[履约要求](https://docs.stripe.com/checkout/fulfillment)、[Session 创建参数](https://docs.stripe.com/api/checkout/sessions/create)。

## 回滚

交付目录的 `ROLLBACK.sh` 接收独立的 `.tar.gz` 源码归档副本的绝对路径，从同目录 `BASELINE.tar.gz` 恢复原始字节，并拒绝覆盖四角色中的正式归档。该脚本不接受部署目录，不修改数据库。`DIFF_FILE.patch` 可以在解压后的基线副本上重建候选代码。

数据库迁移是可空字段和唯一索引的追加，不改写旧订单；旧源码可读取迁移后的旧订单。源码恢复不等于数据库回滚，也不意味着旧源码能处理 `stripe_hosted` 或 `PROCESSING`。已有托管订单时应先禁用新下单，保留新版本的回调、补偿和退款处理直到历史订单处理完毕。不要删除仍被订单引用的实例、密钥或退款记录；不要通过删除幂等列恢复业务。
