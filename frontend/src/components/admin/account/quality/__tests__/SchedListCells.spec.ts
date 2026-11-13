import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import type { SchedAccountSummary, SchedSubstandard } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import SchedGroupsCell from '../SchedGroupsCell.vue'
import SchedNumCell from '../SchedNumCell.vue'
import SchedStatusCell from '../SchedStatusCell.vue'

const WORDS: Record<string, string> = {
  'admin.schedQuality.state.normal': '正常',
  'admin.schedQuality.state.observing': '观察中',
  'admin.schedQuality.state.disabled': '停用',
  'admin.schedQuality.state.substandard': '不达标',
  'admin.schedQuality.state.exempted': '豁免',
  'admin.schedQuality.dim.ttfo': '首字',
  'admin.schedQuality.dim.error': '报错',
  'admin.schedQuality.dim.cache': '缓存',
  'admin.schedQuality.kind.rate_limit': '限流',
  'admin.schedQuality.state.manual': '手动',
  'admin.schedQuality.override.allGroups': '全部分组'
}
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      if (key === 'admin.schedQuality.more') return `+${params?.n}`
      if (key === 'admin.schedQuality.samples') return `${params?.n}次`
      return WORDS[key] ?? key
    },
    te: (key: string) => key in WORDS
  })
}))

const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summary = (patch: Partial<SchedAccountSummary> = {}): SchedAccountSummary => ({ ...structuredClone(base), ...patch })
const at = (h: number, m: number) => {
  const d = new Date()
  d.setHours(h, m, 0, 0)
  return d.toISOString()
}
const sub = (group_id: number, patch: Partial<SchedSubstandard> = {}): SchedSubstandard =>
  ({ group_id, model: 'gpt-6.1-sol', dims: ['ttfo'], until: at(12, 30), other_group: false, exempt: false, ...patch })
const names: Record<number, string> = { 53: '晨星组', 54: '北辰组', 55: '青岚组', 56: '白露组' }
const groupName = (id: number) => names[id] ?? `#${id}`
const groups = (s: SchedAccountSummary) => mount(SchedGroupsCell, { props: { summary: s, groupName } })
const capsules = (w: ReturnType<typeof groups>) => w.findAll('.sq-capsule').map((c) => c.text().replace(/\s+/g, ' '))

describe('SchedNumCell (FINAL §6 colours)', () => {
  const num = (s: SchedAccountSummary, dim: 'ttfo' | 'error' | 'cache') => mount(SchedNumCell, { props: { summary: s, dim } })

  it('shows the value over its sample count; red when it makes the account substandard, grey when the sample is short', () => {
    const s = summary()
    const ttfo = num(s, 'ttfo')
    expect(ttfo.findAll('span > span').map((el) => el.text())).toEqual(['11.6s', '42次'])
    expect(ttfo.get('span > span').classes()).toEqual(['sq-red'])
    expect(num(s, 'error').get('span > span').classes()).toEqual([])
    expect(num(s, 'cache').get('span > span').classes()).toEqual(['sq-text-3'])
  })

  it('lets red win over grey, and shows a dash without samples', () => {
    const s = summary({ ttfo: { p50_ms: 9000, n: 6, short: true, red: true } })
    expect(num(s, 'ttfo').get('span > span').classes()).toEqual(['sq-red'])
    expect(num(summary({ cache: { ratio: null, n: 0, short: true, red: false } }), 'cache').text()).toBe('—')
  })
})

describe('SchedStatusCell (FINAL §2)', () => {
  const status = (s: SchedAccountSummary) => mount(SchedStatusCell, { props: { summary: s } }).get('[data-sq-status]')

  it('reads 正常, 观察中 in grey, and 停用·reason·until in red when automatic, grey when manual', () => {
    expect(status(summary()).text()).toBe('正常')
    const observing = status(summary({ status: 'observing' }))
    expect([observing.text(), observing.classes()]).toEqual(['观察中', ['whitespace-nowrap', 'text-sm', 'sq-text-3']])
    const auto = status(summary({ status: 'disabled', stop: { source: 'auto', reason: 'rate_limit', until: at(14, 20) } }))
    expect([auto.text(), auto.classes()]).toEqual(['停用·限流·14:20', ['whitespace-nowrap', 'text-sm', 'sq-cool']])
    const manual = status(summary({ status: 'disabled', stop: { source: 'manual', reason: 'manual_off', until: null } }))
    expect([manual.text(), manual.classes()]).toEqual(['停用·手动', ['whitespace-nowrap', 'text-sm', 'sq-text-3']])
  })
})

describe('SchedGroupsCell (FINAL §2, §7)', () => {
  it('shows a dash when the account is substandard nowhere and has no exemption', () => {
    expect(groups(summary({ substandard: [] })).text()).toBe('—')
  })

  it('shows one red capsule per substandard group: name, 不达标·dimensions, recovery time', async () => {
    const w = groups(summary({ substandard: [sub(53), sub(54, { dims: ['error', 'cache'], until: at(13, 5) })] }))
    expect(capsules(w)).toEqual(['晨星组 不达标·首字 · 12:30', '北辰组 不达标·报错·缓存 · 13:05'])
    expect(w.findAll('.sq-capsule').every((c) => c.classes().includes('sq-capsule--cool'))).toBe(true)
    await w.get('.sq-capsule').trigger('click')
    expect(w.emitted('open')?.[0][0]).toEqual({ groupId: 53, model: 'gpt-6.1-sol' })
  })

  it('adds the model to the name only when the account is substandard on more than one model', () => {
    const w = groups(summary({ substandard: [sub(53), sub(53, { model: 'gpt-6-astra', until: at(12, 45) })] }))
    expect(capsules(w)).toEqual(['晨星组 gpt-6.1-sol 不达标·首字 · 12:30', '晨星组 gpt-6-astra 不达标·首字 · 12:45'])
  })

  it('leaves exempt rows out of the red capsules and shows exemptions as their own capsules', () => {
    const w = groups(summary({ substandard: [sub(53, { exempt: true })], exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }))
    expect(capsules(w)).toEqual(['全部分组 豁免'])
    expect(w.get('.sq-capsule').classes()).toContain('sq-capsule--manual')
  })

  it('shows the three capsules that end first and +N for the rest', async () => {
    const w = groups(summary({ substandard: [sub(56, { until: at(13, 0) }), sub(53), sub(54, { until: at(12, 40) }), sub(55, { until: at(12, 50) })] }))
    expect(capsules(w).map((text) => text.split(' ')[0])).toEqual(['晨星组', '北辰组', '青岚组'])
    await w.get('[data-sq-more]').trigger('click')
    expect(w.get('[data-sq-more]').text()).toBe('+1')
    expect(w.emitted('open')?.[0][0]).toEqual({ groupId: null, model: null })
  })
})
