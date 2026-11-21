import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PaymentMethodSelector from '@/components/payment/PaymentMethodSelector.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

describe('PaymentMethodSelector', () => {
  it.each(['stripe', 'stripe_hosted'])('shows Stripe but submits the original %s mode', async type => {
    const wrapper = mount(PaymentMethodSelector, { props: { selected: type, methods: [{ type, display_name: 'Stripe Hosted', fee_rate: 0, available: true }] } })
    const button = wrapper.get('button')
    expect(button.text()).toBe('Stripe')
    expect(button.attributes('aria-checked')).toBe('true')
    await button.trigger('click')
    expect(wrapper.emitted('select')).toEqual([[type]])
  })
  it('wraps large custom method collections without letting labels widen the selector', () => {
    const methods = Array.from({ length: 12 }, (_, index) => ({
      type: `custom_${index}`,
      display_name: `CUSTOM_PAYMENT_METHOD_${index}`,
      fee_rate: 0,
      available: true,
    }))

    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'custom_0',
        methods,
      },
    })

    const grid = wrapper.get('[data-testid="payment-method-grid"]')
    expect(grid.classes()).toEqual(expect.arrayContaining(['grid', 'sm:grid-cols-3', 'lg:grid-cols-4']))
    expect(grid.classes()).not.toContain('sm:flex')

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(methods.length)
    expect(buttons.every(button => button.classes().includes('min-w-0'))).toBe(true)
    expect(buttons.every((button, index) => button.attributes('title') === methods[index].display_name)).toBe(true)
    expect(wrapper.findAll('[data-testid="payment-method-label"]').every(label => label.classes().includes('truncate'))).toBe(true)
  })

  it('shows the configured display name for custom EasyPay methods', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'ldc',
        methods: [{ type: 'ldc', display_name: 'LDC Pay', fee_rate: 0, available: true }],
      },
    })

    expect(wrapper.text()).toContain('LDC Pay')
    expect(wrapper.text()).not.toContain('ldc')
    expect(wrapper.text()).not.toContain('payment.methods.ldc')
  })

  it('uses the generic selected style for custom methods that contain built-in names', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'card_alipay',
        methods: [{ type: 'card_alipay', display_name: 'Card Pay', fee_rate: 0, available: true }],
      },
    })

    const button = wrapper.get('button')
    expect(button.classes()).toContain('border-primary-500')
    expect(button.classes()).not.toContain('border-[#02A9F1]')
  })
})

describe('PaymentMethodSelector keyboard', () => {
  it('skips unusable methods with the arrow keys and keeps one Tab stop', async () => {
    const methods = ['alipay', 'wxpay', 'stripe', 'airwallex'].map((type, index) => ({ type, fee_rate: 0, available: index !== 2 }))
    const wrapper = mount(PaymentMethodSelector, { props: { selected: 'wxpay', methods }, attachTo: document.body })
    const radios = wrapper.findAll('[role="radio"]')
    expect(radios.map(r => r.attributes('tabindex'))).toEqual(['-1', '0', '-1', '-1'])
    expect(radios.every(r => r.attributes('type') === 'button')).toBe(true)

    ;(radios[1].element as HTMLElement).focus()
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(radios[3].element)
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'Home' })
    expect(document.activeElement).toBe(radios[0].element)
    expect(wrapper.emitted('select')).toEqual([['airwallex'], ['alipay']])
    wrapper.unmount()
  })
})
