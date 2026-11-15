import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PlatformUsageBreakdown from '../PlatformUsageBreakdown.vue'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

describe('PlatformUsageBreakdown', () => {
  it('keeps the split out of the cell and shows it in the shared tip at a tap (7r-3)', async () => {
    const wrapper = mount(PlatformUsageBreakdown, {
      attachTo: document.body,
      props: {
        today: 2,
        total: 12,
        byPlatform: [
          { platform: 'anthropic', today_actual_cost: 0.5, total_actual_cost: 4 },
          { platform: 'openai', today_actual_cost: 1, total_actual_cost: 6 }
        ]
      },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.text()).not.toContain('admin.users.platformBreakdown')

    await wrapper.get('[data-test="platform-breakdown"]').trigger('click')
    const tip = document.getElementById('sq-tip')!
    expect(tip.hidden).toBe(false)
    expect(tip.textContent!.split('\n')).toEqual([
      'admin.users.platformBreakdown  admin.users.today / admin.users.total',
      'OpenAI  $1.0000 / $6.0000',
      'Claude  $0.5000 / $4.0000',
      'admin.users.platformOther  $0.5000 / $2.0000'
    ])
    wrapper.unmount()
  })
})
