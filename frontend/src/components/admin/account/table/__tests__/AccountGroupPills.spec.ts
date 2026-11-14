import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountGroupPills from '../AccountGroupPills.vue'
import type { AdminGroup } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai', status: 'active' }) as AdminGroup
const quality = { account_id: 7, status: 'normal', reset_at: null, stop: null, substandard: [], exemptions: [] } as never
const mountPills = (count: number, withQuality = true) => mount(AccountGroupPills, {
  attachTo: document.body,
  props: { account: { id: 7, name: 'acc-7' } as never, groups: Array.from({ length: count }, (_, i) => group(i + 1)), quality: withQuality ? quality : null }
})

afterEach(() => {
  document.body.innerHTML = ''
})

// Groups column (7d-12): each pill has two hit zones.
describe('AccountGroupPills', () => {
  it('opens the quality panel on the dot and edits the groups on the text', async () => {
    const wrapper = mountPills(2)
    const [first, second] = wrapper.findAll('[data-test="group-pill"]')
    await second.get('[data-test="group-pill-dot"]').trigger('click')
    expect(wrapper.emitted('open-quality')).toEqual([[2]])
    expect(wrapper.emitted('edit-groups')).toBeUndefined()
    await first.get('[data-test="group-pill-text"]').trigger('click')
    expect(wrapper.emitted('edit-groups')).toHaveLength(1)
    expect(wrapper.emitted('open-quality')).toHaveLength(1)
  })

  it('has no dot without quality data: the whole pill edits', () => {
    const wrapper = mountPills(1, false)
    expect(wrapper.find('[data-test="group-pill-dot"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="group-pill"]').classes()).toContain('pill--nodot')
  })

  it('lists the folded groups when "+N" is clicked, without opening the editor (7d-7)', async () => {
    const wrapper = mountPills(12)
    const more = wrapper.get('[data-test="group-pill-more-text"]')
    expect(more.text()).toBe('+4')
    await more.trigger('click')
    expect(wrapper.emitted('edit-groups')).toBeUndefined()
    const tip = document.getElementById('sq-tip')!
    expect(tip.hidden).toBe(false)
    expect(tip.textContent!.split('\n').slice(1).map((line) => line.split(' · ')[0])).toEqual(
      [9, 10, 11, 12].map((id) => expect.stringContaining(`Group ${id}`))
    )
  })
})
