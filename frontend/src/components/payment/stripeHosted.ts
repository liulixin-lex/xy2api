import type { CreateOrderRequest } from '@/types/payment'

const requestPrefix = 'payment.stripe-hosted.request.'

function requestStorageKey(userId: number, request: CreateOrderRequest): string {
  return requestPrefix + userId + '.' + JSON.stringify([request.order_type || 'balance', request.plan_id || 0, request.amount])
}

export function isStripeHostedUrl(raw: string): boolean {
  try {
    const url = new URL(raw)
    return url.protocol === 'https:' && url.host === 'checkout.stripe.com'
      && !url.username && !url.password && url.pathname.startsWith('/c/pay/')
  } catch { return false }
}

export function hostedRequestKey(storage: Storage, userId: number, request: CreateOrderRequest): string {
  const key = requestStorageKey(userId, request)
  const saved = storage.getItem(key)
  if (saved) {
    try {
      const previous = JSON.parse(saved)
      if (/^[A-Za-z0-9_-]{16,128}$/.test(previous.key)) return previous.key
    } catch { /* Replace invalid local state; the server remains authoritative. */ }
  }
  const value = crypto.randomUUID()
  storage.setItem(key, JSON.stringify({ key: value }))
  return value
}

export function bindHostedRequest(storage: Storage, userId: number, request: CreateOrderRequest, orderId: number): void {
  const key = requestStorageKey(userId, request)
  const saved = storage.getItem(key)
  if (saved) storage.setItem(key, JSON.stringify({ ...JSON.parse(saved), orderId }))
}

export function clearHostedRequest(storage: Storage, userId: number, orderId: number): void {
  for (let i = storage.length - 1; i >= 0; i--) {
    const key = storage.key(i)
    if (!key?.startsWith(requestPrefix + userId + '.')) continue
    try {
      if (JSON.parse(storage.getItem(key) || '{}').orderId === orderId) storage.removeItem(key)
    } catch { /* Corrupt browser state cannot confirm or cancel an order. */ }
  }
}
