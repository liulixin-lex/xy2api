import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import type { SchedAccountSummary, SchedAccountSummaryOverride } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import SchedGroupsCell from '../SchedGroupsCell.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      ({
        'admin.schedQuality.state.cooling': '冷却',
        'admin.schedQuality.state.normal': '正常',
        'admin.schedQuality.state.nodata': '暂无数据',
        'admin.schedQuality.state.exempted': '豁免',
        'admin.schedQuality.state.demoted': '后排',
        'admin.schedQuality.override.allGroups': '全部分组',
        'admin.schedQuality.override.forever': '一直',
        'admin.schedQuality.tip.until': `至 ${params?.time}`,
        'admin.schedQuality.more': `+${params?.n}`
      })[key] ?? (params ? `${key}:${JSON.stringify(params)}` : key)
  })
}))

type TipTarget = HTMLElement & { __tipText?: string }

const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summary = (patch: Partial<SchedAccountSummary> = {}): SchedAccountSummary => ({ ...structuredClone(base), ...patch })
const cooling = (group_id: number) => ({ ...base.cooling[0], group_id })
const override = (group_id: number, mode: 'exempt' | 'demote', expires_at: string | null = null): SchedAccountSummaryOverride => ({ group_id, mode, expires_at })
const names: Record<number, string> = { 53: '晨星组', 54: '北辰组', 55: '青岚组', 56: '白露组', 57: '霜降组' }
const groupName = (id: number) => names[id] ?? `#${id}`

const cell = (s: SchedAccountSummary, openai = true) => mount(SchedGroupsCell, { props: { summary: s, openai, groupName } })
const capsules = (w: ReturnType<typeof cell>) => w.findAll('.sq-capsule').map((c) => c.text().replace(/\s+/g, ' '))
const more = (w: ReturnType<typeof cell>) => w.findAll('button').filter((b) => !b.classes().includes('sq-capsule')).map((b) => b.text())

describe('SchedGroupsCell', () => {
  it('shows a dot and the normal word when nothing is cooling or overridden', () => {
    const w = cell(summary({ cooling: [], groups: { total: 3, normal: 3, cooling: 0, nodata: 0 } }))
    expect(w.text()).toBe('正常')
    expect(w.find('.sq-dot').exists()).toBe(true)
    expect(w.find('.sq-capsule').exists()).toBe(false)
  })

  it('shows no data when every group of the account has none, and a dash for a non-OpenAI account', () => {
    const w = cell(summary({ model: null, cooling: [], groups: { total: 2, normal: 0, cooling: 0, nodata: 2 } }))
    expect(w.text()).toBe('暂无数据')
    expect(w.get('button').classes()).toContain('sq-text-3')
    const other = cell(summary({ overrides: [override(0, 'exempt')] }), false)
    expect(other.text()).toBe('—')
    expect(other.find('button').exists()).toBe(false)
  })

  it('shows one capsule with only the group name and the state word and opens the drawer on its group', async () => {
    const w = cell(summary())
    expect(capsules(w)).toEqual(['晨星组 冷却'])
    expect(w.get('.sq-capsule').classes()).toContain('sq-capsule--cool')
    await w.get('.sq-capsule').trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ model: 'gpt-6.1-sol', groupId: 53 })
  })

  it('shows three capsules and +1 for four cooling groups', async () => {
    const w = cell(summary({ cooling: [cooling(53), cooling(54), cooling(55), cooling(56)] }))
    expect(capsules(w)).toEqual(['晨星组 冷却', '北辰组 冷却', '青岚组 冷却'])
    expect(more(w)).toEqual(['+1'])
    await w.findAll('button').at(-1)!.trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ model: 'gpt-6.1-sol', toGroups: true })
  })

  // Scenario 28.
  it('shows one capsule per group with the override winning over its cooling', () => {
    const w = cell(summary({ cooling: [cooling(53), cooling(54)], overrides: [override(53, 'exempt')] }))
    expect(capsules(w)).toEqual(['晨星组 豁免', '北辰组 冷却'])
    expect(more(w)).toEqual([])
  })

  it('shows the all-groups override and 4 own exceptions as the first 3 and +2', () => {
    const w = cell(summary({
      cooling: [cooling(53)],
      overrides: [override(0, 'demote'), override(53, 'exempt'), override(54, 'demote'), override(55, 'exempt'), override(56, 'exempt')]
    }))
    expect(capsules(w)).toEqual(['全部分组 后排', '晨星组 豁免', '北辰组 后排'])
    expect(more(w)).toEqual(['+2'])
  })

  it('shows only the all-groups capsule, without +N, when the other groups are normal or covered by it', async () => {
    const w = cell(summary({ cooling: [cooling(53), cooling(54)], overrides: [override(0, 'exempt')] }))
    expect(capsules(w)).toEqual(['全部分组 豁免'])
    expect(more(w)).toEqual([])
    expect(w.get('.sq-capsule').classes()).toContain('sq-capsule--all')
    // The all-groups capsule opens the drawer at its top.
    await w.get('.sq-capsule').trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ model: 'gpt-6.1-sol' })
  })

  // Scenario 34: exempt and demote share one colour (manual, dashed) and differ by icon and word; cooling keeps its own.
  it('gives exempt and demote one manual colour, told apart by icon and word, unlike cooling', () => {
    const kinds = (w: ReturnType<typeof cell>) => w.findAll('.sq-capsule').map((c) => [c.attributes('data-sq-kind'), ...c.classes().filter((name) => name.startsWith('sq-capsule--'))])
    const manual = cell(summary({ cooling: [cooling(55)], overrides: [override(53, 'exempt'), override(54, 'demote')] }))
    expect(kinds(manual)).toEqual([['exempt', 'sq-capsule--manual'], ['demote', 'sq-capsule--manual'], ['cooling', 'sq-capsule--cool']])
    const [exempt, demote, cool] = manual.findAll('.sq-capsule')
    expect(exempt.find('svg path').attributes('d')).not.toBe(demote.find('svg path').attributes('d'))
    expect(cool.find('svg').exists()).toBe(false)
    expect(kinds(cell(summary()))).toEqual([['cooling', 'sq-capsule--cool']])
  })

  it('puts the expiry of an override in its tip and its accessible name', () => {
    const later = new Date(Date.now() + 3 * 86_400_000)
    later.setHours(18, 30, 0, 0)
    const w = cell(summary({ cooling: [], overrides: [override(0, 'exempt', later.toISOString()), override(54, 'demote')] }))
    const [all, own] = w.findAll('.sq-capsule')
    const day = `${String(later.getMonth() + 1).padStart(2, '0')}-${String(later.getDate()).padStart(2, '0')}`
    expect((all.element as TipTarget).__tipText).toBe(`至 ${day} 18:30`)
    expect(all.attributes('aria-label')).toBe(`全部分组 豁免 至 ${day} 18:30`)
    expect((own.element as TipTarget).__tipText).toBe('一直')
    expect(own.attributes('aria-label')).toBe('北辰组 后排 一直')
  })
})
