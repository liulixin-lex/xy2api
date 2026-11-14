import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import LiveStatusIndicator from '../LiveStatusIndicator.vue'
import PendingChangesPill, { formatPendingCount } from '../PendingChangesPill.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('LiveStatusIndicator', () => {
  it('pauses when live or out of date and resumes when paused', async () => {
    const pause = vi.fn()
    const resume = vi.fn()
    const wrapper = mount(LiveStatusIndicator, { props: { status: 'live', pause, resume } })
    await wrapper.trigger('click')
    expect(pause).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ status: 'stale' })
    await wrapper.trigger('click')
    expect(pause).toHaveBeenCalledTimes(2)

    await wrapper.setProps({ status: 'paused' })
    await wrapper.trigger('click')
    expect(resume).toHaveBeenCalledTimes(1)
    expect(pause).toHaveBeenCalledTimes(2)
  })

  it('shows paused as the shared hollow ring', () => {
    const wrapper = mount(LiveStatusIndicator, { props: { status: 'paused', pause: vi.fn(), resume: vi.fn() } })
    expect(wrapper.get('.status-dot').classes()).toContain('status-dot--off')
    expect(wrapper.text()).toContain('common.liveStatus.paused')
  })
})

describe('PendingChangesPill', () => {
  it('writes the count as N or N+', () => {
    expect(formatPendingCount(3)).toBe('3')
    expect(formatPendingCount(50, true)).toBe('50+')
  })

  it('is absent with nothing pending and merges on click', async () => {
    const label = (n: string) => `${n} changes`
    const wrapper = mount(PendingChangesPill, { props: { count: 0, label } })
    expect(wrapper.find('[data-test="pending-changes"]').exists()).toBe(false)

    await wrapper.setProps({ count: 20, more: true })
    const pill = wrapper.get('[data-test="pending-changes"]')
    expect(pill.text()).toContain('20+ changes')
    await pill.trigger('click')
    expect(wrapper.emitted('merge')).toHaveLength(1)
  })
})
