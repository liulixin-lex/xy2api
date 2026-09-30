import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import NativeStreamSettings from '../NativeStreamSettings.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('native stream admission controls', () => {
  it('starts disabled and exposes unsupported persistence without enabling it', () => {
    const wrapper = mount(NativeStreamSettings)
    expect((wrapper.get('[data-testid="native-delivery"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-testid="native-recovery"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="native-persistence"]').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted()).toEqual({})
  })
  it('turns off recovery with delivery in the same save and never mutates the loaded snapshot', async () => {
    const snapshot = { delivery: true, recovery: true, persistence: false }
    const wrapper = mount(NativeStreamSettings, { props: { modelValue: snapshot } })
    await wrapper.get('[data-testid="native-delivery"]').setValue(false)
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ delivery: false, recovery: false, persistence: false }]])
    expect(snapshot).toEqual({ delivery: true, recovery: true, persistence: false })
  })
  it('enables recovery only under delivery and describes its actual boundary', async () => {
    const wrapper = mount(NativeStreamSettings, { props: { modelValue: { delivery: true, recovery: false, persistence: false } } })
    await wrapper.get('[data-testid="native-recovery"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ delivery: true, recovery: true, persistence: false }]])
    expect(wrapper.get('[data-testid="native-recovery"]').attributes('aria-describedby')).toBe('native-recovery-hint')
  })
})
