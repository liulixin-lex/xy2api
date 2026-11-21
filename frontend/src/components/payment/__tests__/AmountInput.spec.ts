import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import AmountInput from '../AmountInput.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params && 'amount' in params ? `${key} ${String(params.amount)}` : key,
  }),
}))
enableAutoUnmount(afterEach)

function mountInput(value: number | null = null) {
  return mount(AmountInput, { props: { modelValue: value } })
}

describe('recharge amount input', () => {
  it.each(['10abc', '10.555', '-10', '1e2'])('restores the accepted amount after rejecting %s', async (value) => {
    const wrapper = mountInput(10)
    const input = wrapper.get('input')
    await input.setValue(value)
    expect((input.element as HTMLInputElement).value).toBe('10')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('restores the last typed amount rather than a stale prop', async () => {
    const wrapper = mountInput()
    const input = wrapper.get('input')
    await input.setValue('12.50')
    await input.setValue('12.500')
    expect((input.element as HTMLInputElement).value).toBe('12.50')
    expect(wrapper.emitted('update:modelValue')).toEqual([[12.5]])
  })

  it('preserves decimal editing and allows clearing the amount', async () => {
    const wrapper = mountInput()
    const input = wrapper.get('input')
    for (const value of ['0', '0.', '0.5', '0.50', '']) await input.setValue(value)
    expect(wrapper.emitted('update:modelValue')).toEqual([[null], [null], [0.5], [0.5], [null]])
    expect((input.element as HTMLInputElement).value).toBe('')
  })
})

describe('recharge bonus hints on quick amounts', () => {
  const tiers = [
    { min_amount: 100, bonus_percent: 20 },
    { min_amount: 500, bonus_percent: 30 },
  ]

  it('renders no badge or second line when no tiers are configured', () => {
    const wrapper = mount(AmountInput, { props: { modelValue: null, amounts: [50, 100, 500] } })
    expect(wrapper.find('[data-testid="quick-amount-bonus-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quick-amount-credited"]').exists()).toBe(false)
  })

  it('bonus mode: price tag only on amounts that hit a tier, credited totals on every button', () => {
    const wrapper = mount(AmountInput, { props: { modelValue: null, amounts: [50, 100, 500], bonusTiers: tiers } })
    const below = wrapper.get('[data-testid="quick-amount-50"]')
    const first = wrapper.get('[data-testid="quick-amount-100"]')
    const second = wrapper.get('[data-testid="quick-amount-500"]')

    expect(below.find('[data-testid="quick-amount-bonus-badge"]').exists()).toBe(false)
    expect(below.get('[data-testid="quick-amount-credited"]').text()).toContain('$50.00')
    expect(first.get('[data-testid="quick-amount-bonus-badge"]').text()).toBe('+20%')
    expect(first.get('[data-testid="quick-amount-credited"]').text()).toContain('$120.00')
    expect(second.get('[data-testid="quick-amount-bonus-badge"]').text()).toBe('+30%')
    expect(second.get('[data-testid="quick-amount-credited"]').text()).toContain('$650.00')
  })

  it('bonus mode: matches tiers by the entered amount but credits by the multiplier', () => {
    const wrapper = mount(AmountInput, {
      props: { modelValue: null, amounts: [1000], bonusTiers: tiers, multiplier: 0.14 },
    })
    const button = wrapper.get('[data-testid="quick-amount-1000"]')
    expect(button.get('[data-testid="quick-amount-bonus-badge"]').text()).toBe('+30%')
    // 1000 × 0.14 = 140 base, +30% = 182
    expect(button.get('[data-testid="quick-amount-credited"]').text()).toContain('$182.00')
  })

  it('discount mode: tag reads N% OFF and the second line shows the discounted payment', () => {
    const wrapper = mount(AmountInput, {
      props: { modelValue: null, amounts: [50, 500], bonusTiers: tiers, bonusMode: 'discount', currency: 'USD' },
    })
    const below = wrapper.get('[data-testid="quick-amount-50"]')
    const hit = wrapper.get('[data-testid="quick-amount-500"]')
    expect(below.find('[data-testid="quick-amount-bonus-badge"]').exists()).toBe(false)
    expect(below.get('[data-testid="quick-amount-credited"]').text()).toContain('50.00')
    expect(hit.get('[data-testid="quick-amount-bonus-badge"]').text()).toBe('30% OFF')
    // 500 × (1 − 30%) = 350
    expect(hit.get('[data-testid="quick-amount-credited"]').text()).toContain('350.00')
  })
})

describe('quick amounts as one radio group', () => {
  it('marks the picked amount, keeps only it in the Tab order, and lights it when the typed amount matches', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: null, amounts: [50, 100, 500] } })
    const radios = () => wrapper.findAll('[role="radio"]')
    expect(wrapper.get('[role="radiogroup"]').exists()).toBe(true)
    expect(radios().map(r => r.attributes('tabindex'))).toEqual(['0', '-1', '-1'])

    await wrapper.setProps({ modelValue: 100 })
    expect(radios().map(r => r.attributes('aria-checked'))).toEqual(['false', 'true', 'false'])
    expect(radios().map(r => r.attributes('tabindex'))).toEqual(['-1', '0', '-1'])
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('100')

    await wrapper.setProps({ modelValue: 120 })
    expect(radios().every(r => r.attributes('aria-checked') === 'false')).toBe(true)
  })

  it('moves and picks with the arrow keys', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 50, amounts: [50, 100, 500] }, attachTo: document.body })
    const radios = wrapper.findAll('[role="radio"]')
    ;(radios[0].element as HTMLElement).focus()
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'ArrowRight' })
    expect(document.activeElement).toBe(radios[1].element)
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'ArrowLeft' })
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(radios[2].element)
    expect(wrapper.emitted('update:modelValue')).toEqual([[100], [50], [500]])
  })

  it('drops the quick amount block when no amount fits the range', () => {
    const wrapper = mount(AmountInput, { props: { modelValue: null, amounts: [50, 100], min: 6000 } })
    expect(wrapper.find('[role="radiogroup"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('payment.quickAmounts')
  })

  it('flags an unusable amount inside the field', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 20, amounts: [50], error: '' } })
    const input = wrapper.get('input')
    expect(input.attributes('aria-invalid')).toBeUndefined()
    expect(wrapper.get('[data-testid="amount-error"]').text()).toBe('')

    await wrapper.setProps({ error: 'payment.amountTooLow' })
    expect(input.attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('[data-testid="amount-error"]').text()).toBe('payment.amountTooLow')
    expect(input.attributes('aria-describedby')).toBe(wrapper.get('[data-testid="amount-error"]').attributes('id'))
    expect(wrapper.get('[data-testid="amount-field"]').classes()).toContain('border-status-back')
  })
})

describe('Enter in the amount field', () => {
  it('asks the page to submit, except while an IME candidate is being confirmed', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 100 } })
    const input = wrapper.get('input')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    expect(wrapper.emitted('submit')).toBeUndefined()
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('submit')).toHaveLength(1)
  })
})
