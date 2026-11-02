import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AccountGroupsCellButton from '../AccountGroupsCellButton.vue'
import type { Group } from '@/types'
import type { GroupQualityGateAccountStatus } from '@/types/groupQualityGate'
import type { GroupQualityCell } from '../groupQualityPresentation'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai' }) as Group
const GroupBadgeStub = { props: ['name', 'showPlatformIcon'], template: '<span data-test="badge" :data-icon="String(showPlatformIcon)">{{ name }}</span>' }

const mountCell = (groups: Group[]) => mount(AccountGroupsCellButton, {
  props: { groups, accountName: 'acc-1' },
  global: { stubs: { GroupBadge: GroupBadgeStub } }
})
const cell = (state: GroupQualityGateAccountStatus['state'], patch: Partial<GroupQualityGateAccountStatus> = {}): GroupQualityCell => ({
  kind: state,
  status: {
    account_id: 1, state, until: null, reasons: [], strikes: 0, usable: true, passing: true, guard_held: false, pinned: false,
    vetoed: false, tier: 0, headroom: 0.4, metrics: { calls: 0, soft_failures: 0, error_rate: null, warm_samples: 0, warm_ttft_p90_ms: null }, ...patch
  },
  maxErrorRate: 0.1,
  maxWarmTtftMs: 20000,
  fastWarmTtftMs: 10000,
  effectiveFastWarmTtftMs: null,
  stale: false
})

describe('AccountGroupsCellButton', () => {
  it('is a single button with up to 2 badges, each titled with its full name, and a plain +N', async () => {
    const wrapper = mountCell([group(1), group(2), group(3), group(4), group(5)])
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('type')).toBe('button')
    expect(buttons[0].attributes('aria-label')).toBe('admin.groupCategories.editAccountGroupsAria:{"name":"acc-1"}')
    expect(buttons[0].attributes('title')).toBe('Group 1、Group 2、Group 3、Group 4、Group 5')
    expect(wrapper.findAll('[data-test="badge"]').map((badge) => [badge.text(), badge.attributes('title')])).toEqual([['Group 1', 'Group 1'], ['Group 2', 'Group 2']])
    expect(wrapper.text()).toContain('+3')

    await buttons[0].trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it('shows no +N for two groups or fewer, and +1 for a third', () => {
    const wrapper = mountCell([group(1), group(2)])
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('+')
    expect(mountCell([group(1), group(2), group(3)]).text()).toContain('+1')
  })

  it('shows the platform icon only for groups on a different platform than the account', () => {
    const wrapper = mount(AccountGroupsCellButton, {
      props: { groups: [group(1), { ...group(2), platform: 'anthropic' } as Group], accountName: 'acc-1', accountPlatform: 'openai' },
      global: { stubs: { GroupBadge: GroupBadgeStub } }
    })
    expect(wrapper.findAll('[data-test="badge"]').map((badge) => badge.attributes('data-icon'))).toEqual(['false', 'true'])
  })

  it('shows a dash that still opens the dialog when there are no groups', async () => {
    const wrapper = mountCell([])
    expect(wrapper.get('button').text()).toBe('—')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  describe('quality gate', () => {
    afterEach(() => {
      delete document.documentElement.dataset.motion
      document.body.innerHTML = ''
    })

    const mountGated = (quality: Map<number, GroupQualityCell>) => {
      // Reduced motion: the panel shows without the GSAP enter, so visibility is checked directly.
      document.documentElement.dataset.motion = 'reduced'
      return mount(AccountGroupsCellButton, {
        props: { groups: [group(1), group(2), group(3)], accountName: 'acc-1', quality },
        global: { stubs: { GroupBadge: GroupBadgeStub } },
        attachTo: document.body
      })
    }
    const panel = () => document.querySelector<HTMLElement>('[data-testid="account-groups-quality-tooltip"]')!

    it('shows plain badges for eligible groups and opens the panel only from the small indicator', async () => {
      const wrapper = mountGated(new Map([[1, cell('eligible')], [2, cell('ejected', { reasons: ['error_rate'] })]]))
      expect(wrapper.findAll('[data-testid="group-quality-badge"]').map(badge => badge.attributes('data-state'))).toEqual(['ejected'])
      expect(wrapper.findAll('[data-test="badge"]').map(badge => badge.text())).toEqual(['Group 1'])
      const indicator = wrapper.get('[data-testid="group-quality-indicator"]')
      expect(indicator.attributes('data-state')).toBe('ejected')
      expect(indicator.attributes('aria-label')).toBe('admin.groupQualityGate.indicator.attention:{"count":1}')

      await wrapper.get('[data-testid="account-groups-cell"]').trigger('mouseenter')
      expect(panel().style.display).toBe('none')

      await indicator.trigger('click')
      await new Promise(resolve => setTimeout(resolve))
      expect(panel().style.display).not.toBe('none')
      expect(panel().querySelectorAll('[data-testid="quality-panel-block"]')).toHaveLength(1)
      expect(panel().querySelector('[data-testid="quality-panel-eligible"]')!.textContent).toContain('Group 1')

      await indicator.trigger('keydown', { key: 'Escape' })
      expect(panel().style.display).toBe('none')
      wrapper.unmount()
    })

    it('keeps the indicator grey when every gated group is eligible', () => {
      const wrapper = mountGated(new Map([[1, cell('eligible')], [3, cell('eligible', { pinned: true })]]))
      const indicator = wrapper.get('[data-testid="group-quality-indicator"]')
      expect([indicator.attributes('data-state'), indicator.attributes('aria-label')]).toEqual(['none', 'admin.groupQualityGate.indicator.ok'])
      expect(wrapper.find('[data-testid="group-quality-badge"]').exists()).toBe(false)
      wrapper.unmount()
    })
  })
})
