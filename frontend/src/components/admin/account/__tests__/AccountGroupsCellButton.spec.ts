import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AccountGroupsCellButton from '../AccountGroupsCellButton.vue'
import type { Group } from '@/types'
import type { GroupQualityGateAccountStatus, GroupQualityGateGroupStatus } from '@/types/groupQualityGate'
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
const cell = (kind: GroupQualityGateAccountStatus['status'], patch: Partial<GroupQualityGateAccountStatus> = {}, groupPatch: Partial<GroupQualityGateGroupStatus> = {}): GroupQualityCell => {
  const account: GroupQualityGateAccountStatus = {
    account_id: 1, status: kind, tier: kind === 'failing' ? 2 : kind === 'retest' ? 1 : 0, pinned: kind === 'pinned', latency_hold: null,
    ttft: { state: 'pass', value: 9800, factor: 1.08, samples: 23, sessions: 6, bad_until: null },
    error: { state: 'pass', value: 0.04, samples: 80, failures: 3, wilson_low: 0.013, wilson_high: 0.105, bad_until: null },
    cache: { state: 'pass', value: 0.82, samples: 24, wilson_low: 0.62, wilson_high: 0.93, bad_until: null },
    ...patch
  }
  const group: GroupQualityGateGroupStatus = {
    group_id: 1, enabled: true,
    policy: { enabled: true, max_ttft_p90_seconds: 15, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false },
    expected_p90_ms: 9100, expected_samples: 87, summary: { passing: 1, unknown: 0, retest: 0, failing: 0, pinned: 0 }, accounts: [account],
    ...groupPatch
  }
  return { kind, account, group, stale: false }
}

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

    it('badges failing, retest and unknown groups and opens the panel only from the small indicator', async () => {
      const wrapper = mountGated(new Map([
        [1, cell('passing')],
        [2, cell('failing', { error: { state: 'fail', value: 0.2, samples: 60, failures: 12, wilson_low: 0.12, wilson_high: 0.31, bad_until: '2026-11-04T12:00:00Z' } })],
        [3, cell('unknown', { cache: { state: 'none', value: null, samples: 4, wilson_low: null, wilson_high: null, bad_until: null } })]
      ]))
      // 未知组排在达标组前面，用灰色状态标签（不用平台色），悬停说明暂按 0 档；达标组收进 +1。
      const badges = wrapper.findAll('[data-testid="group-quality-badge"]')
      expect(badges.map(badge => badge.attributes('data-state'))).toEqual(['failing', 'unknown'])
      expect(badges[1].attributes('title')).toBe('admin.groupQualityGate.unknownBadgeTitle:{"group":"Group 3"}')
      expect(wrapper.find('[data-test="badge"]').exists()).toBe(false)
      expect(wrapper.text()).toContain('+1')
      const indicator = wrapper.get('[data-testid="group-quality-indicator"]')
      expect(indicator.attributes('data-state')).toBe('failing')
      expect(indicator.attributes('aria-label')).toBe('admin.groupQualityGate.indicator.attention:{"count":1}')

      await wrapper.get('[data-testid="account-groups-cell"]').trigger('mouseenter')
      expect(panel().style.display).toBe('none')

      await indicator.trigger('click')
      await new Promise(resolve => setTimeout(resolve))
      expect(panel().style.display).not.toBe('none')
      const blocks = [...panel().querySelectorAll<HTMLElement>('[data-testid="quality-panel-block"]')]
      expect(blocks.map(block => block.dataset.state)).toEqual(['failing', 'unknown', 'passing'])
      const errorRow = blocks[0].querySelector<HTMLElement>('[data-metric="error"]')!
      expect(errorRow.dataset.state).toBe('fail')
      expect(errorRow.textContent).toContain('20%')
      expect(errorRow.textContent).toContain('12/60')
      expect(blocks[0].querySelector('[data-testid="quality-panel-bad-memory"]')).not.toBeNull()
      expect(blocks[1].querySelector<HTMLElement>('[data-metric="cache"]')!.dataset.state).toBe('none')

      await indicator.trigger('keydown', { key: 'Escape' })
      expect(panel().style.display).toBe('none')
      wrapper.unmount()
    })

    it('flags too few group samples on the first-token row', async () => {
      const wrapper = mountGated(new Map([[1, cell('unknown', { ttft: { state: 'none', value: null, factor: null, samples: 12, sessions: 2, bad_until: null } }, { expected_p90_ms: null, expected_samples: 20 })]]))
      await wrapper.get('[data-testid="group-quality-indicator"]').trigger('click')
      await new Promise(resolve => setTimeout(resolve))
      const composition = panel().querySelector<HTMLElement>('[data-testid="quality-panel-composition"]')!
      expect(composition.dataset.insufficient).toBe('true')
      expect(panel().querySelector('[data-metric="ttft"]')!.textContent).toContain('admin.groupQualityGate.tooltip.groupSamplesShort')
      wrapper.unmount()
    })

    it('keeps the indicator grey when nothing needs attention and turns it into a clock during a latency hold', () => {
      const quiet = mountGated(new Map([[1, cell('passing')], [3, cell('pinned')]]))
      const indicator = quiet.get('[data-testid="group-quality-indicator"]')
      expect([indicator.attributes('data-state'), indicator.attributes('aria-label')]).toEqual(['none', 'admin.groupQualityGate.indicator.ok'])
      expect(quiet.find('[data-testid="group-quality-badge"]').exists()).toBe(false)
      quiet.unmount()

      const until = new Date(Date.now() + 5 * 60_000).toISOString()
      const held = mountGated(new Map([[1, cell('passing', { latency_hold: { until, models: ['gpt-5.5'] } })]]))
      const heldIndicator = held.get('[data-testid="group-quality-indicator"]')
      expect([heldIndicator.attributes('data-state'), heldIndicator.attributes('aria-label')]).toEqual(['hold', 'admin.groupQualityGate.indicator.hold'])
      expect(document.querySelector('[data-testid="quality-panel-latency-hold"]')).not.toBeNull()
      held.unmount()
    })
  })
})
