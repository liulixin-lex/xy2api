# Built-in Payment

XY2API has a built-in payment system that lets users top up their balance or buy subscription plans by themselves, without deploying a separate payment service. This guide is for super administrators who configure payments. It covers the supported payment methods, admin settings, provider credentials and callbacks, the Stripe embedded and hosted modes, and order statuses and refunds. Only super administrators can change payment settings and providers.

## Supported Payment Methods

| Provider | Methods available to users | Description |
|---|---|---|
| **EasyPay** | Alipay, WeChat Pay, and custom methods | Third-party aggregators compatible with the EasyPay protocol |
| **Alipay (Direct)** | Desktop QR code; mobile website payment or Face-to-Face handoff to the Alipay app | Direct integration with Alipay Open Platform |
| **WeChat Pay (Direct)** | Native QR, H5, Official Account/JSAPI | Direct integration with WeChat Pay APIv3, routed by client environment |
| **Stripe Embedded** | Card, Alipay, WeChat Pay, Link, etc. | Pay on this site with the Stripe Payment Element; multi-currency |
| **Stripe Hosted** | Whatever is enabled in the Stripe Dashboard | Redirect to the official Stripe Checkout page; multi-currency |
| **Airwallex** | Whatever the Airwallex account supports | Pay on this site with the Airwallex payment component; multi-currency |

- Users only see two buttons, `Alipay` and `WeChat Pay`, regardless of whether they are served directly or through EasyPay. Direct channels connect to the payment platform API, send funds straight to the merchant account and usually charge lower fees; EasyPay aggregates through a third-party platform and is easier to set up. In the admin console only one enabled instance can provide `Alipay` at a time (the same applies to `WeChat Pay`); enabling a second one prompts you to disable the existing instance first.
- Stripe Embedded and Stripe Hosted are mutually exclusive (see [Stripe Embedded and Hosted](#stripe-embedded-and-hosted)); users always see "Stripe".
- WeChat Pay (Direct), Alipay (Direct) and EasyPay charge in CNY. Stripe and Airwallex can be configured per provider to charge in CNY, HKD, USD, EUR, GBP, AUD, CAD, SGD, JPY, KRW, NZD and others, subject to what the account supports.

**EasyPay provider references**: the two providers below are third-party aggregators compatible with the EasyPay protocol. Check their official sites for current fees and settlement terms.

- Domestic channel, CNY settlement: ZPay (`z-pay.cn`), direct integration with official Alipay/WeChat Pay APIs, T+1 automatic settlement, supports individuals without a business license (with a daily limit).
- International channel, USDT or USD settlement: Kyren (`kyrenpay.com`), offers international WeChat Pay and Alipay, local-currency checkout and USD settlement.

Evaluate the security, reliability and compliance of any payment provider yourself. This project does not endorse or guarantee any third-party payment provider.

## Quick Start

1. Open the admin console, go to **Settings → Payment**.
2. Turn on **Enable Payment** and set the basic parameters such as amount range and order timeout.
3. Under **Enabled Providers**, select the provider types you want to use.
4. In **Provider Management**, click **Add Provider**, add at least one instance and fill in its credentials.
5. Register the callback URL on the payment platform as described in [Webhook Configuration](#webhook-configuration).
6. Users can now top up or buy plans from the user console.

## System Settings

### Basic Settings

| Setting | Key | Default | Description |
|---|---|---|---|
| Enable Payment | `payment_enabled` | Off | Master switch |
| Enabled Providers | `ENABLED_PAYMENT_TYPES` | Empty | Disabling a provider type also disables its instances |
| Minimum Amount | `MIN_RECHARGE_AMOUNT` | 1 | Minimum amount per top-up |
| Maximum Amount | `MAX_RECHARGE_AMOUNT` | No limit | Empty means no limit |
| Daily Limit | `DAILY_RECHARGE_LIMIT` | No limit | Per-user daily cumulative limit; empty means no limit |
| Order Timeout | `ORDER_TIMEOUT_MINUTES` | 30 | In minutes, minimum 1 |
| Max Pending Orders | `MAX_PENDING_ORDERS` | 3 | Maximum concurrent pending orders per user |
| Load Balance Strategy | `LOAD_BALANCE_STRATEGY` | Round Robin | `round-robin`, or `least-amount` (prefer the instance with the lowest daily cumulative amount) |
| Product Name Prefix / Suffix | `PRODUCT_NAME_PREFIX` / `PRODUCT_NAME_SUFFIX` | Empty | Product name shown on the payment page |
| Help Image / Help Text | `PAYMENT_HELP_IMAGE_URL` / `PAYMENT_HELP_TEXT` | Empty | Support QR code and instructions shown on the top-up page |
| Disable Balance Recharge | `BALANCE_PAYMENT_DISABLED` | Off | When on, users can only buy subscription plans |

### Conversion and Promotions

| Setting | Key | Default | Description |
|---|---|---|---|
| Balance Recharge Multiplier | `BALANCE_RECHARGE_MULTIPLIER` | 1 | How many USD of balance the user receives for each 1 CNY paid; must be greater than 0 |
| Subscription USD to CNY Rate | `SUBSCRIPTION_USD_TO_CNY_RATE` | 0 (disabled) | CNY charged per 1 USD of plan price on CNY channels. At 0 the plan price is charged as-is; when enabled, all plan prices must be set in USD |
| Recharge Fee Rate | `RECHARGE_FEE_RATE` | 0 | Percentage fee charged on top of the top-up amount |
| Recharge Promotion Tiers | `RECHARGE_BONUS_TIERS`, `RECHARGE_BONUS_MODE` | Empty | Balance top-ups match the highest tier not above the entered amount. In **Bonus** mode the payment stays the same and the given percentage is credited on top; in **Discount** mode the credit stays the same and the payment is reduced by the percentage (must be below 100). Subscription orders are not affected |
| Recharge Bonus Notice | `RECHARGE_BONUS_NOTICE` | Empty | Markdown shown at the top of the amount picker on the top-up page |

### Alipay Options

| Setting | Key | Default | Description |
|---|---|---|---|
| Force Alipay QR Code | `ALIPAY_FORCE_QRCODE` | Off | Mobile Alipay users always scan a QR code instead of being redirected to mobile website payment |
| Mobile Alipay Precreate Handoff | `ALIPAY_MOBILE_PRECREATE_DEEP_LINK` | Off | Applies only to mobile orders routed to Alipay (Direct): the server calls `alipay.trade.precreate`, the frontend tries to open the Alipay app, and if that does not succeed it shows a dynamic QR fallback page and keeps polling the order |

- Precreate handoff requires the Alipay app to have Face-to-Face / QR payment enabled. Setting the environment variable `ALIPAY_MOBILE_PRECREATE_DEEP_LINK=true` forces it on; when unset, the admin setting is used.
- When both options are on, precreate handoff takes priority. Turning precreate handoff off restores mobile website payment.

### Cancel Rate Limiting

Prevents users from repeatedly creating and cancelling orders. When enabled, users who exceed the cancel limit within the window cannot create new orders.

| Setting | Key | Default |
|---|---|---|
| Limit Cancel Rate | `CANCEL_RATE_LIMIT_ENABLED` | Off |
| Window Mode | `CANCEL_RATE_LIMIT_WINDOW_MODE` | Rolling / Fixed |
| Window / Unit | `CANCEL_RATE_LIMIT_WINDOW` / `CANCEL_RATE_LIMIT_UNIT` | 1; unit is minutes, hours or days |
| Max Cancels | `CANCEL_RATE_LIMIT_MAX` | 10 |

## Provider Configuration

In **Provider Management → Add Provider**, choose a type and fill in its fields. The notify URL and return URL are built automatically from the site's callback base URL; you only need to confirm the domain is correct.

### EasyPay

| Parameter | Required | Description |
|---|---|---|
| Merchant ID (PID) | Yes | |
| Merchant Key (PKey) | Yes | |
| API Base URL | Yes | The EasyPay provider's API address |
| Alipay Channel ID / WeChat Channel ID | No | Pin a specific upstream channel |
| EasyPay custom payment methods | No | Additional methods: a method identifier (lowercase letters, digits, underscores, hyphens; must not be or start with `alipay` or `wxpay`), the upstream `type`, and a display name. The upstream `type` is sent as the EasyPay `type` parameter |

Payment mode can be **QR Code** (default) or **Popup**.

### Alipay (Direct)

| Parameter | Required |
|---|---|
| App ID | Yes |
| Application private key (RSA2) | Yes |
| Alipay public key | Yes |

- **Desktop**: calls `alipay.trade.precreate` (Face-to-Face) first and renders the QR code directly; if unavailable, falls back to `alipay.trade.page.pay` (Computer Website Pay), renders the cashier URL as a QR code, and keeps a link to open the cashier page.
- **Mobile**: calls `alipay.trade.wap.pay` (Mobile Website Pay) by default; with **Mobile Alipay Precreate Handoff** on, uses Face-to-Face instead. If Mobile Website Pay is not enabled for the merchant, the frontend switches to QR payment.

Enable the corresponding products (Face-to-Face, Computer Website Pay, Mobile Website Pay) for the app in Alipay Open Platform.

### WeChat Pay (Direct)

| Parameter | Required | Description |
|---|---|---|
| App ID | Yes | |
| Merchant ID (MchID) | Yes | |
| Merchant API private key | Yes | PEM format |
| APIv3 key | Yes | 32 bytes |
| Certificate serial number | Yes | Merchant certificate serial number |
| WeChat Pay public key / public key ID | Yes | PEM format |
| Official Account App ID, H5 app name, H5 app URL | No | Only needed when Official Account payment or H5 has special requirements |

Desktop prefers Native QR; on mobile, payments inside WeChat use JSAPI (which requires WeChat web authorization to obtain the OpenID), and other browsers use H5.

### Stripe Embedded

| Parameter | Required | Description |
|---|---|---|
| Secret Key | Yes | `sk_live_...` or `sk_test_...` |
| Publishable Key | Yes | `pk_live_...` or `pk_test_...` |
| Webhook Secret | Yes | `whsec_...` |
| Payment currency | No | Default CNY |

### Stripe Hosted

| Parameter | Required | Description |
|---|---|---|
| Secret Key | Yes | |
| Webhook Secret | Yes | Must belong to the same Stripe account and the same test/live mode as the secret key |
| Payment currency | No | Default CNY |

No Publishable Key is needed. See [Stripe Embedded and Hosted](#stripe-embedded-and-hosted) for the remaining requirements.

### Airwallex

| Parameter | Required | Description |
|---|---|---|
| Client ID | Yes | |
| API Key | Yes | A scoped API key is recommended, with only Read and Write for Payment Acceptance under account-level permissions |
| Webhook Secret | Yes | |
| API Base URL | Yes | Must match the API key environment: `https://api-demo.airwallex.com/api/v1` for sandbox, `https://api.airwallex.com/api/v1` for production (default). Mixing them returns `credentials_invalid` / Access Denied |
| Country/region code | No | Two-letter ISO code, default `CN` |
| Payment currency | No | Default CNY |
| Airwallex account ID | No | Can be left empty unless you use multiple accounts, organization-level keys or connected accounts |

## Provider Instance Management

You can create multiple instances of the same provider type:

- **Independent limits**: each instance can set a minimum, maximum and daily limit. Leaving all empty uses the global settings; when partially filled, empty fields mean no limit. Instances whose limits would be exceeded are skipped automatically.
- **Load balancing**: when several enabled instances serve the same payment method, orders are distributed by the load balance strategy.
- **Independent toggle and ordering**: enable or disable instances individually and drag to reorder. An instance with pending orders cannot be disabled, and order-affecting configuration cannot be changed while orders are pending (`PENDING_ORDERS`).
- **Refund control**: each instance can **Allow refunds**, and further **Allow user refunds** (self-service requests).
- **Payment methods**: each instance can support a subset of payment methods.

## Webhook Configuration

Payment success is confirmed by the provider callback. The system verifies the callback signature and credits the order automatically, with no manual step. Callback URLs must use HTTPS (required by Stripe, strongly recommended for others), and your firewall must allow requests from the payment platforms.

| Provider | Callback URL |
|---|---|
| EasyPay | `https://your-domain.com/api/v1/payment/webhook/easypay` (accepts GET and POST) |
| Alipay (Direct) | `https://your-domain.com/api/v1/payment/webhook/alipay` |
| WeChat Pay (Direct) | `https://your-domain.com/api/v1/payment/webhook/wxpay` |
| Stripe Embedded | `https://your-domain.com/api/v1/payment/webhook/stripe` |
| Stripe Hosted | `https://your-domain.com/api/v1/payment/webhook/stripe_hosted/<instance ID>` |
| Airwallex | `https://your-domain.com/api/v1/payment/webhook/airwallex` |

Replace `your-domain.com` with your domain. For EasyPay, Alipay and WeChat Pay the callback URL is filled in automatically when you add the provider.

- **Stripe Embedded**: in the Stripe Dashboard go to Developers → Webhooks, add an endpoint, subscribe to `payment_intent.succeeded` and `payment_intent.payment_failed`, and copy the generated `whsec_...` into the provider configuration.
- **Stripe Hosted**: subscribe to `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed` and `checkout.session.expired`.
- **Airwallex**: add a webhook endpoint in Airwallex, select at least Payment Intent → Succeeded (`payment_intent.succeeded`), preferably also Payment Intent → Cancelled (`payment_intent.cancelled`), and use the account default or latest stable API version.

## Stripe Embedded and Hosted

Under **Settings → Payment → Stripe checkout mode**, choose **Embedded**, **Hosted** or **Off**. Embedded completes payment on this site; Hosted redirects to Stripe Checkout. Both modes support balance top-ups and one-time plan purchases. Plans are granted with this site's validity period; no recurring Stripe subscription is created. Enabling both modes returns `STRIPE_MODE_CONFLICT`.

Switching modes only affects new orders. Existing orders keep their original flow for payment confirmation and refunds; turning a mode off or switching does not delete provider configuration or stop callbacks for historical orders.

### Hosted Mode Requirements

1. Configure and back up a fixed `TOTP_ENCRYPTION_KEY`. Hosted instance secrets and webhook secrets are encrypted with it, and saving is refused without it (`PAYMENT_ENCRYPTION_REQUIRED`). Losing this key affects order lookup and refunds for historical orders.
2. Set a trusted **Frontend URL** (`frontend_url`) in system settings, for example `https://app.example.com`, with no path, query or fragment. Only test keys may use `http://localhost:<port>` or `http://127.0.0.1:<port>`. The Checkout return URL is built only from this setting; the client's Host, Referer or `return_url` are not used.
3. Select **Stripe Hosted** mode and save, then add a provider of that type with the secret key, webhook secret, currency and limits.
4. After saving, reopen the provider and copy the full webhook URL generated by this site. The trailing number is this deployment's instance ID; do not copy the number from another deployment. Add that endpoint in the Stripe Dashboard and choose an API version in the `.dahlia` family (the backend SDK uses `2026-03-25.dahlia`; the date does not need to match). Do not use a preview version.
5. Manage payment methods, logo and brand color in the Stripe Dashboard. This site uses fixed amounts, so turn off Adaptive Pricing and automatic tax, and do not enable promotion codes, shipping or adjustable quantities.

**Configuration lock**: a hosted instance referenced by any order cannot be deleted (`PROVIDER_IN_USE`), and its secret key and currency cannot be changed directly (`HOSTED_CONFIG_LOCKED`), even if the orders are completed. To change the Stripe account or credentials, create a new instance and disable the old one. Disabling only blocks new orders; signature verification, recovery and refunds for historical orders keep working.

### Hosted Mode Order Flow

- The order request `POST /api/v1/payment/orders` uses `payment_type=stripe_hosted` and must carry an `Idempotency-Key` header (16–128 letters, digits, underscores or hyphens). Retrying with the same key and the same parameters returns the original order; the same key with different parameters returns a conflict.
- The response contains `pay_url`, `payment_mode=redirect` and `expires_at`, and no `client_secret`. The browser opens the official Stripe Checkout URL in the current tab.
- The order validity uses **Order Timeout**, clamped to 31 minutes – 24 hours; the actual expiry returned by Stripe is authoritative.
- Users can call `POST /api/v1/payment/orders/:id/stripe-hosted/resume` to continue paying: the server queries Stripe first and then returns a URL to resume. Parameters on the Checkout return page are only for navigation and never credit an order; clicking the Checkout back link does not cancel the order.
- Only payments that pass checks on instance, account, test/live mode, Session, currency and minor-unit amount are credited. Callbacks, user lookups and background reconciliation share one fulfillment entry; background reconciliation handles at most 20 hosted orders per round.
- If the Session creation result is uncertain, the order is kept and only the same Stripe idempotent request is replayed within 23 hours; no new Session is created. For orders still unresolved after that window, check the Stripe Dashboard by order metadata instead of marking them paid manually.

## Payment Flow

```text
User selects amount and payment method
       │
       ▼
  Create order (PENDING)
  ├─ Validate amount range, pending order count, daily limit
  ├─ Select provider instance by load balancing
  └─ Call provider to get payment info
       │
       ▼
  User pays (QR / redirect / embedded component / Stripe Checkout)
       │
       ▼
  Callback verified → order PAID → balance or plan granted → order COMPLETED
```

### Order Status Reference

| Status | Description |
|---|---|
| `PENDING` | Waiting for payment |
| `PROCESSING` | Waiting for funds: a delayed payment method (for example in Stripe Hosted) was submitted and awaits provider confirmation; not treated as page expiry |
| `PAID` | Paid, awaiting credit |
| `RECHARGING` | Crediting |
| `COMPLETED` | Completed; balance or plan granted |
| `EXPIRED` | Timed out without payment |
| `CANCELLED` | Cancelled |
| `FAILED` | Payment failed, or credit failed (an admin can retry) |
| `REFUND_REQUESTED` | User requested a refund |
| `REFUNDING` | Refund in progress |
| `REFUND_PENDING` | Refund result awaiting confirmation |
| `PARTIALLY_REFUNDED` | Partially refunded |
| `REFUNDED` | Refunded |
| `REFUND_FAILED` | Refund failed |

### Timeout and Reconciliation

A background job checks for timed-out orders every 60 seconds and queries the provider before marking an order expired. If the user actually paid but the callback was delayed, the order is reconciled through that query.

### Refunds

- Admins refund an order, query the refund result or retry fulfillment from **Orders**. Operators need the **Refund** flag (see [Admin Permissions](admin-permissions.md); the page is in Chinese).
- For instances with **Allow user refunds** on, users can request refunds for balance orders.
- A refund deducts the corresponding balance or plan time; it is refused when the balance is insufficient or the amount exceeds the top-up amount.
- For Stripe Hosted orders, the refund request is frozen and the balance or plan time is deducted in a database transaction before Stripe is called. If the network result is uncertain, the order moves to `REFUND_PENDING` and an admin confirms it with **Query refund**: a confirmed success does not deduct again, and a confirmed failure restores the deduction exactly once. Do not resubmit a confirmed-failed refund with different parameters; check the original refund record in Stripe first.

## Subscription Plans

Manage purchasable plans under **Orders → Plans**: bound group, price and original price, currency, validity period and unit, feature description, product name, for-sale flag and ordering. Creating, editing or deleting plans requires the **Balance adjust** permission. Buying a plan creates a `subscription` order, and the subscription is granted for the plan's validity period once payment completes.

## Migrating from Sub2APIPay

If you previously used Sub2APIPay as an external payment system:

1. Enable payment in the XY2API admin console and configure providers (you can reuse the same payment credentials).
2. Change the callback URLs on the payment platforms to XY2API's callback URLs.
3. After confirming that new orders are processed by the built-in payment, decommission Sub2APIPay.

Historical orders in Sub2APIPay are not migrated automatically; keep it running for a while if you need to look them up. If you still need an external payment system to integrate through the Admin API, see [Admin Payment Integration API](../api/payment-integration.md) (in Chinese).

## Upgrade and Rollback

Stripe Hosted mode depends on migration `250_stripe_hosted_idempotency.sql` (adds nullable columns and a unique index; old orders are not rewritten). Before rolling back to a version without hosted mode, stop new hosted orders and keep the current version until callbacks, reconciliation and refunds for existing hosted orders are all finished. Do not delete instances, keys or refund records still referenced by orders. See [Upgrade and Rollback](../deployment/upgrade.md) (in Chinese) for the general procedure.
