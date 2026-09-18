import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import StripeModeSelector from '../StripeModeSelector.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Stripe mode selection', () => {
  it('replaces the old mode while preserving other payment methods', async () => {
    const wrapper = mount(StripeModeSelector, { props: { modelValue: ['alipay', 'stripe', 'wxpay'] } })
    await wrapper.get('input[value="stripe_hosted"]').setValue()
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['alipay', 'wxpay', 'stripe_hosted']])
    await wrapper.get('input[value=""]').setValue()
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([['alipay', 'wxpay']])
  })
  it('uses hosted for a legacy dual-mode configuration and can switch to embedded', async () => {
    const wrapper = mount(StripeModeSelector, { props: { modelValue: ['stripe', 'stripe_hosted'] } })
    expect((wrapper.get('input[value="stripe_hosted"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('input[value="stripe"]').setValue()
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['stripe']])
  })
})
