import { beforeEach, describe, expect, it } from 'vitest'
import { bindHostedRequest, clearHostedRequest, hostedRequestKey, isStripeHostedUrl } from '../stripeHosted'

describe('Stripe hosted request recovery', () => {
  beforeEach(() => sessionStorage.clear())
  it('reuses a request key after an uncertain response and isolates users and inputs', () => {
    const request = { amount: 80, payment_type: 'stripe_hosted', order_type: 'balance' as const }
    const key = hostedRequestKey(sessionStorage, 1, request)
    expect(hostedRequestKey(sessionStorage, 1, request)).toBe(key)
    expect(hostedRequestKey(sessionStorage, 2, request)).not.toBe(key)
    expect(hostedRequestKey(sessionStorage, 1, { ...request, amount: 81 })).not.toBe(key)
    expect(hostedRequestKey(sessionStorage, 1, request)).toBe(key)
    bindHostedRequest(sessionStorage, 1, request, 42)
    clearHostedRequest(sessionStorage, 1, 43)
    expect(hostedRequestKey(sessionStorage, 1, request)).toBe(key)
    clearHostedRequest(sessionStorage, 1, 42)
    expect(hostedRequestKey(sessionStorage, 1, request)).not.toBe(key)
  })
  it('only accepts official hosted checkout URLs', () => {
    expect(isStripeHostedUrl('https://checkout.stripe.com/c/pay/cs_test_1#token')).toBe(true)
    for (const raw of ['javascript:alert(1)', '//checkout.stripe.com/c/pay/cs_1', 'https://checkout.stripe.com.evil.test/c/pay/cs_1', 'https://user@checkout.stripe.com/c/pay/cs_1', 'https://checkout.stripe.com:8443/c/pay/cs_1', 'https://checkout.stripe.com/other']) {
      expect(isStripeHostedUrl(raw)).toBe(false)
    }
  })
})
