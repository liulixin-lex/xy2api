import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import type { SchedAccountSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import SchedNumberCell from '../SchedNumberCell.vue'
import SchedGroupsCell from '../SchedGroupsCell.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      ({
        'admin.schedQuality.result.timeout': '超时',
        'admin.schedQuality.state.cooling': '冷却',
        'admin.schedQuality.state.testing': '测试中',
        'admin.schedQuality.state.normal': '正常',
        'admin.schedQuality.state.nodata': '暂无数据'
      })[key] ?? (params ? `${key}:${JSON.stringify(params)}` : key)
  })
}))

const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summary = (patch: Partial<SchedAccountSummary> = {}): SchedAccountSummary => ({ ...structuredClone(base), ...patch })
const names: Record<number, string> = { 53: '晨星组', 54: '北辰组', 55: '青岚组', 56: '白露组' }
const groupName = (id: number) => names[id] ?? `#${id}`

describe('SchedNumberCell', () => {
  const cell = (dim: 'ttfo' | 'error' | 'cache', s?: SchedAccountSummary, extra: Record<string, unknown> = {}) =>
    mount(SchedNumberCell, { props: { accountId: 479, dim, summary: s, ...extra } })

  it('shows the three numbers in the neutral text colour, never a verdict colour', () => {
    const texts = (['ttfo', 'error', 'cache'] as const).map((dim) => cell(dim, summary()))
    expect(texts.map((w) => w.text())).toEqual(['12.4s', '0.5%', '81%'])
    for (const w of texts) {
      expect(w.get('button').classes()).toContain('sq-text')
      expect(w.get('button').classes().some((c) => c.startsWith('sq-v-'))).toBe(false)
    }
  })

  it('shows the timeout word for a timed-out p90 and a dash for missing values', () => {
    const timedOut = summary({ ttfo: { p90_ms: null, timeout: true, n: 150 } })
    expect(cell('ttfo', timedOut).text()).toBe('超时')
    expect(cell('ttfo', timedOut).get('button').classes()).toContain('sq-text')
    const empty = summary({ model: null, ttfo: { p90_ms: null, timeout: false, n: 3 }, error: { rate: null, n: 3 }, cache: { ratio: null, n: 0 } })
    for (const dim of ['ttfo', 'error', 'cache'] as const) {
      const w = cell(dim, empty)
      expect(w.text()).toBe('—')
      expect(w.get('button').classes()).toContain('sq-text-3')
    }
    expect(cell('error', undefined).text()).toBe('—')
  })

  it('keeps only the first-output number in the Tab order and opens the drawer on click', async () => {
    const first = cell('ttfo', summary(), { focusable: true })
    const second = cell('error', summary())
    expect(first.get('button').attributes('tabindex')).toBe('0')
    expect(second.get('button').attributes('tabindex')).toBe('-1')
    await first.get('button').trigger('click')
    expect(first.emitted('open')).toHaveLength(1)
  })

  it('shows a skeleton while the first summary is loading', () => {
    expect(cell('ttfo', undefined, { loading: true }).find('.skeleton').exists()).toBe(true)
  })
})

describe('SchedGroupsCell', () => {
  const cell = (s: SchedAccountSummary) => mount(SchedGroupsCell, { props: { summary: s, groupName } })
  const capsules = (w: ReturnType<typeof cell>) => w.findAll('.sq-capsule').map((c) => c.text().replace(/\s+/g, ' '))

  it('shows a dot and the normal word when nothing is cooling', () => {
    const w = cell(summary({ cooling: [], groups: { total: 3, normal: 3, cooling: 0, nodata: 0 } }))
    expect(w.text()).toBe('正常')
    expect(w.find('.sq-dot').exists()).toBe(true)
    expect(w.find('.sq-capsule').exists()).toBe(false)
  })

  it('shows one capsule with only the group name and the state word', async () => {
    const w = cell(summary())
    expect(capsules(w)).toEqual(['晨星组 冷却'])
    expect(w.get('.sq-capsule').classes()).toContain('sq-capsule--cool')
    await w.get('.sq-capsule').trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ model: 'gpt-6.1-sol', groupId: 53 })
  })

  it('shows three capsules and +1 for four cooling groups; a testing group reads 测试中', async () => {
    const cooling = [53, 54, 55].map((group_id, i) => ({ ...base.cooling[0], group_id, testing: i === 1 }))
    const w = cell(summary({ cooling, cooling_more: 1, groups: { total: 5, normal: 1, cooling: 4, nodata: 0 } }))
    expect(capsules(w)).toEqual(['晨星组 冷却', '北辰组 测试中', '青岚组 冷却'])
    expect(w.findAll('.sq-capsule')[1].classes()).toContain('sq-capsule--test')
    const more = w.findAll('button').at(-1)!
    expect(more.text()).toBe('admin.schedQuality.more:{"n":1}')
    await more.trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ model: 'gpt-6.1-sol', toGroups: true })
  })

  it('shows no data when every group of the account has none', () => {
    const w = cell(summary({ model: null, cooling: [], groups: { total: 2, normal: 0, cooling: 0, nodata: 2 } }))
    expect(w.text()).toBe('暂无数据')
    expect(w.get('button').classes()).toContain('sq-text-3')
  })
})
